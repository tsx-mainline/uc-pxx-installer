#!/usr/bin/env python3
"""Unpack a Crestron/Grandstream UC-P10 "pcdfw" firmware container into its
individual partition images.

This fully reverses the Grandstream "pack" format as implemented by the device's
own native libraries (libwebupdate.so / libprovdetect.so: prov_fw_version_detect,
prov_dec_header, prov_dec_body, set_head_key, set_body_key; OpenSSL
AES_set_decrypt_key + AES_cbc_encrypt). No per-device secret is required. Only the
head key and the IV come from outside the image: both are hard-coded in the device's
updater libraries. They are NOT in this repository: pcdfw-findkey.py finds them in
those libraries and writes the key file that --keys reads. Every body key is stored
inside the image, encrypted under the head key.

Container layout (little-endian)
--------------------------------
  0x00 .. 0x58   plaintext pack header (88 bytes)
                   +0x00 u32   signature = 0x43A78F39  (file bytes 39 8f a7 43)
                   +0x04 u16[] hw_id / oem compatibility list
                   +0x34 u32[8] per-segment "version/size-table" field (= each
                               segment's seghdr +0x14; informational)
  0x58 .. EOF    a chain of segments. Each segment is:
                   [0x200-byte header region][size_used bytes of body]
                 The 96-byte segment header (seghdr) sits at the start of the
                 header region and is AES-128-CBC encrypted with the HEAD key in
                 independent 32-byte chunks (IV reset every 32 bytes). seghdr:
                   +0x00 u32  size_used   (body length, bytes)
                   +0x04 u32  size        (= size_used + 0x200; whole-segment stride)
                   +0x08 u16  fw_vmask
                   +0x0a u16  seg_id      (0xfd02..0xfe10 -> partition, see MAP)
                   +0x0c u32  idt_len     (0 = raw body; else inner index length)
                   +0x10 u32  signature   = 0x43A78F39
                   +0x10 .. +0x20  this segment's 16-byte AES body key (overlaps
                                   the signature: the key's first 4 bytes ARE the
                                   signature; next 4 = size-table field; next 4 =
                                   a per-segment incrementing date; last 4 = tail)
                   +0x14 u32  version (shown d.d.d.d)
                   +0x18 u32  date
                   +0x1c u16  oem_id ; +0x1e u16 hw_id
                 The next segment starts at current_offset + size.

Body decryption (prov_dec_body)
-------------------------------
  AES-128-CBC with the segment body key and the IV, RE-INITIALISED
  every 4096 bytes (the device decrypts in 0x1000-byte calls). Any trailing run of
  < 16 bytes in the final window is copied through verbatim.

Raw segments (idt_len == 0)
---------------------------
  The decrypted body IS the partition image (uboot, trust, dtbo, vbmeta, boot).

Chunked segments (idt_len != 0: the large filesystems system/vendor/oem)
------------------------------------------------------------------------
  The decrypted body begins with an index table (idt) of idt_len bytes = N entries
  of 0x48 bytes each:
    +0x00 u32 signature 0x43A78F39
    +0x08 u32 chunk index (0..N-1)
    +0x10 .. +0x30  32-byte SHA-256 of the chunk payload (dedup/integrity)
    +0x30 u32 storage offset of the chunk (relative to end of idt; cumulative)
    +0x34 u32 stored (compressed+encrypted) length of the chunk
  After the idt come the chunk payloads back-to-back. Each chunk is decrypted the
  same way (body key, 4096-byte IV reset) and is a complete little ZIP holding one
  DEFLATE entry "tmp/tmp.tmp" that inflates to 0x100000 (1 MiB) of partition data
  (the final chunk may be shorter). The partition image is the concatenation, in
  index order, of every chunk's inflated bytes.

Usage:  pcdfw-unpack.py <pcdfw.bin> --keys KEYFILE -o <outdir> [-v]
Needs:  pip install pycryptodome
"""
import sys, os, struct, argparse, zlib, binascii
from Crypto.Cipher import AES

SIG       = 0x43A78F39
FIRST_SEG = 0x58                         # first seghdr offset
HDR_REGION = 0x200                       # header region before each segment body
SEGHDR_LEN = 0x60                        # 96-byte seghdr (3 x 32B head chunks)
BODY_WINDOW = 0x1000                     # prov_dec_body IV-reset granularity
IDT_ENTRY  = 0x48
CHUNK_USIZE = 0x100000

# seg_id -> the A/B partition the image is written to (without the _a/_b suffix).
# The device's own provisioning names (/proc/provision/partition/<name> in
# libprovdetect.so) are "loader" for 0xfd02 and "uboot" for 0xfd03. Those names are
# misleading: byte comparison against unit 2 proved 0xfd02 == uboot_a/_b and
# 0xfd03 == trust_a/_b. Neither is the shared idbloader (sector 64), which is not
# in the pcdfw at all.
SEGID_MAP = {
    0xfd02: "uboot",    # Rockchip "LOADER" image = U-Boot -> uboot_a/_b
    0xfd03: "trust",    # "BL3X" trust/ATF FIT -> trust_a/_b
    0xfe01: "dtbo",     # Android DTBO (magic 0xd7b7ab1e)
    0xfe02: "vbmeta",   # AVB vbmeta ("AVB0")
    0xfe04: "boot",     # Android boot image ("ANDROID!")
    0xfe06: "vendor",   # ext4
    0xfe08: "oem",      # ext4
    0xfe10: "system",   # ext4
}


def dec_head(buf, key, iv):
    """prov_dec_header: AES-128-CBC, IV reset every 32 bytes (head key)."""
    out = bytearray()
    for i in range(0, len(buf) - len(buf) % 32, 32):
        out += AES.new(key, AES.MODE_CBC, iv).decrypt(buf[i:i + 32])
    return bytes(out)


def dec_body(buf, key, iv):
    """prov_dec_body: AES-128-CBC, IV reset every 4096 bytes (body key).
    A trailing run shorter than one AES block is passed through unchanged."""
    out = bytearray()
    for i in range(0, len(buf), BODY_WINDOW):
        win = buf[i:i + BODY_WINDOW]
        full = len(win) - len(win) % 16
        out += AES.new(key, AES.MODE_CBC, iv).decrypt(win[:full]) + win[full:]
    return bytes(out)


def inflate_chunk(cb):
    """A chunk is a ZIP with one DEFLATE (or stored) entry -> raw bytes."""
    if cb[:4] != b"PK\x03\x04":
        raise ValueError("chunk is not a ZIP (PK) record: %s" % cb[:4].hex())
    comp,  = struct.unpack_from("<H", cb, 8)
    crc,   = struct.unpack_from("<I", cb, 14)
    csize, = struct.unpack_from("<I", cb, 18)
    usize, = struct.unpack_from("<I", cb, 22)
    nlen,  = struct.unpack_from("<H", cb, 26)
    elen,  = struct.unpack_from("<H", cb, 28)
    off = 30 + nlen + elen
    data = cb[off:off + csize]
    if comp == 0:
        out = data
    else:
        d = zlib.decompressobj(-15)
        out = d.decompress(data) + d.flush()
    if (binascii.crc32(out) & 0xFFFFFFFF) != crc:
        raise ValueError("chunk CRC mismatch (bad key/decrypt)")
    return out, usize


def parse_seghdr(hdr):
    size_used, size = struct.unpack_from("<II", hdr, 0x00)
    fw_vmask, seg_id = struct.unpack_from("<HH", hdr, 0x08)
    idt_len, = struct.unpack_from("<I", hdr, 0x0c)
    sig,     = struct.unpack_from("<I", hdr, 0x10)
    ver,     = struct.unpack_from("<I", hdr, 0x14)
    date,    = struct.unpack_from("<I", hdr, 0x18)
    oem, hw  = struct.unpack_from("<HH", hdr, 0x1c)
    return dict(size_used=size_used, size=size, fw_vmask=fw_vmask, seg_id=seg_id,
                idt_len=idt_len, sig=sig, key=hdr[0x10:0x20], ver=ver, date=date,
                oem=oem, hw=hw)


def unpack(path, outdir, head_key, iv, verbose=False):
    fsz = os.path.getsize(path)
    f = open(path, "rb")

    def rd(off, n):
        f.seek(off)
        return f.read(n)

    sig0, = struct.unpack_from("<I", rd(0, 4), 0)
    if sig0 != SIG:
        raise SystemExit("0x%08x: not a pcdfw (bad pack signature 0x%08x)" % (SIG, sig0))

    os.makedirs(outdir, exist_ok=True)
    manifest = []
    O = FIRST_SEG
    i = 0
    while O + SEGHDR_LEN <= fsz:
        hdr = dec_head(rd(O, SEGHDR_LEN), head_key, iv)
        s = parse_seghdr(hdr)
        if s["sig"] != SIG:
            if verbose:
                print("  stop: seghdr @0x%x bad sig 0x%08x" % (O, s["sig"]))
            break
        name = SEGID_MAP.get(s["seg_id"], "seg%d_%04x" % (i, s["seg_id"]))
        data_off = O + HDR_REGION
        ver = "%d.%d.%d.%d" % ((s["ver"] >> 24) & 0xff, (s["ver"] >> 16) & 0xff,
                               (s["ver"] >> 8) & 0xff, s["ver"] & 0xff)
        print("seg%d @0x%08x  id=0x%04x %-7s size_used=0x%x idt_len=0x%x v%s"
              % (i, O, s["seg_id"], name, s["size_used"], s["idt_len"], ver))

        outfn = os.path.join(outdir, "%02d_%s_%04x.img" % (i, name, s["seg_id"]))
        with open(outfn, "wb") as out:
            if s["idt_len"] == 0:
                body = dec_body(rd(data_off, s["size_used"]), s["key"], iv)
                out.write(body)
                written = len(body)
            else:
                idt = dec_body(rd(data_off, s["idt_len"]), s["key"], iv)
                payoff = data_off + s["idt_len"]
                nent = s["idt_len"] // IDT_ENTRY
                written = 0
                good = 0
                for e in range(nent):
                    ent = idt[e * IDT_ENTRY:(e + 1) * IDT_ENTRY]
                    esig, = struct.unpack_from("<I", ent, 0)
                    if esig != SIG:
                        break                    # trailing unused slots
                    dst, = struct.unpack_from("<I", ent, 0x30)
                    ln,  = struct.unpack_from("<I", ent, 0x34)
                    cb = dec_body(rd(payoff + dst, ln), s["key"], iv)
                    raw, usize = inflate_chunk(cb)
                    out.write(raw)
                    written += len(raw)
                    good += 1
                if verbose:
                    print("    %d/%d chunks -> 0x%x bytes" % (good, nent, written))

        manifest.append((i, name, s["seg_id"], O, s["size_used"], s["idt_len"], written, outfn))
        O += s["size"]
        i += 1

    print("\nunpacked %d segments; walk ended at 0x%08x (file 0x%08x)%s"
          % (i, O, fsz, "  [clean: ended at EOF]" if O == fsz else ""))
    print("\n%-4s %-8s %-7s %-10s %-12s %-9s %s"
          % ("seg", "seg_id", "name", "hdr@", "part_size", "idt_len", "file"))
    for (i, name, sid, off, su, idt, w, fn) in manifest:
        print("%-4d 0x%04x   %-7s 0x%08x 0x%-10x 0x%-7x %s"
              % (i, sid, name, off, w, idt, os.path.basename(fn)))
    return manifest


def main():
    ap = argparse.ArgumentParser(description="Unpack a UC-P10 pcdfw firmware image.")
    ap.add_argument("pcdfw")
    ap.add_argument("-o", "--out", required=True, help="output directory")
    ap.add_argument("--keys", required=True, help="key file from pcdfw-findkey.py (head_key=, iv=)")
    ap.add_argument("-v", "--verbose", action="store_true")
    args = ap.parse_args()
    keys = {}
    with open(args.keys) as f:
        for line in f:
            if "=" in line:
                k, v = line.strip().split("=", 1)
                keys[k] = bytes.fromhex(v)
    if len(keys.get("head_key", b"")) != 16 or len(keys.get("iv", b"")) != 16:
        raise SystemExit("%s: needs head_key= and iv= (16 bytes of hex each)" % args.keys)
    unpack(args.pcdfw, args.out, keys["head_key"], keys["iv"], args.verbose)


if __name__ == "__main__":
    main()
