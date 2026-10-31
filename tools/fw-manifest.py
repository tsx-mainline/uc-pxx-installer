#!/usr/bin/env python3
"""Build (or check) the installer manifest for one official UC-P10 firmware.

Input: the pcdfw file and the directory that pcdfw-unpack.py wrote for it.
Output (JSON): the pcdfw sha256, the build identity (from system build.prop), the
vbmeta facts the installer's preflight needs (AVB public-key hash, rollback
index), and one entry per partition image: target partition, size, sha256, action.

The installer accepts an image only if its sha256 is in a manifest under the same
partition name. A sha256 is a fingerprint, not a key: nothing secret is in here.

Before it writes anything, the tool checks the AVB data of the firmware offline,
with its own parser of the AOSP libavb format (standard library only), and stops
on the first failure:

  - vbmeta: header, block sizes, algorithm SHA256_RSA4096, the hash over the
    header and the auxiliary block, the RSA signature (PKCS#1 v1.5, SHA-256) with
    the public key in the image, flags 0, no non-zero reserved, unused or
    trailing bytes (so every byte of the vbmeta image is checked);
  - the ATX (Android Things) certificate chain in the public key metadata: the
    PIK certificate = the pinned one (PIK_CERT_SHA256), its usage; the PSK
    certificate signed by the PIK key (SHA512_RSA4096), its usage, its subject =
    the pinned product (PSK_SUBJECT); the vbmeta key = the PSK key;
  - the rollback index, its location and the PIK and PSK key versions = the
    reviewed values (EXPECT);
  - every descriptor: hash descriptors (boot, dtbo) recomputed; hashtree
    descriptors (system, vendor) recomputed block by block, every level of the
    tree stored in the image compared, and the root digest; the dm-verity table
    on the kernel command line = the system hashtree descriptor; the AVB footer
    of each covered image; no chain, property or unknown descriptor; exactly the
    partitions of COVERED, each once.

Not covered by AVB, and so only by the sha256 of the manifest: uboot, trust,
oem, and the FEC data of system and vendor (its place and size are checked).

Schema 1 (the default) is the format of the manifests in manifests/, which the
installer embeds. Schema 2 adds the facts of the design for a later release
(status, boot chain, the AVB facts). The tool always writes status "recognise",
no evidence and no nvram model: a person sets those in the reviewed commit.

Usage:  fw-manifest.py PCDFW UNPACK_DIR --fw-version V (-o OUT | --check MANIFEST)
                       [--schema 1|2] [--debugfs PATH]
--check recomputes everything and compares it with MANIFEST (in its schema).
Exit status: 0 OK, 1 a check failed.
"""
import argparse
import hashlib
import json
import os
import re
import struct
import subprocess
import sys
import tempfile

# seg_id -> A/B partition base name (see pcdfw-unpack.py for the evidence)
PARTITION = {
    0xfd02: "uboot",
    0xfd03: "trust",
    0xfe01: "dtbo",
    0xfe02: "vbmeta",
    0xfe04: "boot",
    0xfe06: "vendor",
    0xfe08: "oem",
    0xfe10: "system",
}
# trust is version-independent (1.0.0.94 == 1.0.6.20); write it only if it differs
ACTION = {"trust": "write-if-different"}
BUILD_KEYS = ("ro.build.fingerprint", "ro.build.display.id", "ro.build.type",
              "ro.build.version.release", "ro.build.version.incremental", "ro.build.date")

# ---------------------------------------------------------------------------
# Pins and reviewed values (plan FIRMWARE-GENERAL, sections 1.2, 1.3 and 2.1).
#
# The root of trust of the unit is the product root key in the ATX permanent
# attributes, which the tool cannot read. So it pins the PIK certificate that a
# locked UC-P10 U-Boot accepted, and the PSK subject (sha256 of the product id).
PIK_CERT_SHA256 = "dbd1313e5617c949e383943129415230d14054301d25c2fec7954ca4e4d8f2d4"
PSK_SUBJECT = "f08d249f209eae1961d0c2dc936148cd8fa989a29e2b93628b0c28f9a38d6538"
PINS = {"pik_cert_sha256": PIK_CERT_SHA256, "psk_subject": PSK_SUBJECT}
# The same in every known firmware. Each U-Boot writes the PIK/PSK versions of
# the slot that boots into the TEE, so another value can strand a keeper: a
# change here needs a review.
EXPECT = {"rollback_index": 0, "rollback_index_location": 0,
          "pik_key_version": 42, "psk_key_version": 42}
# The partitions that the vbmeta must cover, and how.
COVERED = {"boot": "hash", "dtbo": "hash", "system": "hashtree", "vendor": "hashtree"}
# The verity table of this partition is on the kernel command line (system as root).
DM_PARTITION = "system"
# Not in the vbmeta, so only the boot-chain set and the sha256 cover them.
NOT_COVERED = ("uboot", "trust", "oem")
# The model of the installer's nvram reader (plan FIRMWARE-GENERAL, section 5.1).
NVRAM_MODELS = ("gs-nvram-72005-v1",)

# libavb constants
AVB_MAGIC = b"AVB0"
FOOTER_MAGIC = b"AVBf"
HEADER_SIZE = 256
FOOTER_SIZE = 64
ALGORITHMS = {0: "NONE", 1: "SHA256_RSA2048", 2: "SHA256_RSA4096", 3: "SHA256_RSA8192",
              4: "SHA512_RSA2048", 5: "SHA512_RSA4096", 6: "SHA512_RSA8192"}
SHA256_RSA4096 = 2
RSA4096_BYTES = 512
AVB_KEY_SIZE = 8 + 2 * RSA4096_BYTES                    # AvbRSAPublicKeyHeader + n + rr
TAG_PROPERTY, TAG_HASHTREE, TAG_HASH, TAG_CMDLINE, TAG_CHAIN = 0, 1, 2, 3, 4
HASHTREE_FIXED = 164                                     # after the 16-byte descriptor header
HASH_FIXED = 116
CMDLINE_FIXED = 8
CMDLINE_FLAGS = 0x3                                      # only if hashtree not disabled / disabled
HASH_ALGS = {"sha1": 20, "sha256": 32, "sha512": 64}
BLOCK = 4096
# libavb_atx
ATX_SIGNED_SIZE = 4 + AVB_KEY_SIZE + 32 + 32 + 8         # version, key, subject, usage, key_version
ATX_CERT_SIZE = ATX_SIGNED_SIZE + RSA4096_BYTES
ATX_METADATA_SIZE = 4 + 2 * ATX_CERT_SIZE
USAGE_PIK = hashlib.sha256(b"com.google.android.things.vboot.ca").digest()
USAGE_PSK = hashlib.sha256(b"com.google.android.things.vboot").digest()
# PKCS#1 v1.5 DigestInfo prefixes (RFC 8017, section 9.2)
DIGESTINFO = {
    "sha256": bytes.fromhex("3031300d060960864801650304020105000420"),
    "sha512": bytes.fromhex("3051300d060960864801650304020305000440"),
}
# U-Boot
LOADER_MAGIC = b"LOADER  "
LOADER_COPY = 1 << 20
USER_UBOOT = b"device is forced LOCKED for USER BUILD"


class AvbError(Exception):
    """The firmware failed a check. The text says which and why."""


def fail(fmt, *args):
    raise AvbError(fmt % args if args else fmt)


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for b in iter(lambda: f.read(8 << 20), b""):
            h.update(b)
    return h.hexdigest()


# ---------------------------------------------------------------------------
# Reading images. Every read goes through an opener (default: open), so the
# tests can present changed or truncated images without copying them.

class Image:
    def __init__(self, name, path, opener):
        self.name = name
        self.f = opener(path, "rb")
        self.f.seek(0, 2)
        self.size = self.f.tell()

    def read(self, off, n):
        if off < 0 or n < 0 or off + n > self.size:
            fail("%s: read of %d bytes at %d is outside the image (%d bytes)", self.name, n, off, self.size)
        self.f.seek(off)
        b = self.f.read(n)
        if len(b) != n:
            fail("%s: short read at %d", self.name, off)
        return b

    def chunks(self, off, n, step=4 << 20):
        """The bytes [off, off+n) in chunks of step (a multiple of BLOCK)."""
        end = off + n
        while off < end:
            k = min(step, end - off)
            yield self.read(off, k)
            off += k

    def close(self):
        self.f.close()


# ---------------------------------------------------------------------------
# RSA: PKCS#1 v1.5 verification only, public exponent 65537 (as in libavb)

def parse_avb_key(blob, what):
    """AvbRSAPublicKeyHeader (key_num_bits, n0inv; big-endian), then n and rr."""
    if len(blob) != AVB_KEY_SIZE:
        fail("%s: public key is %d bytes, not %d (RSA-4096)", what, len(blob), AVB_KEY_SIZE)
    bits, n0inv = struct.unpack_from(">II", blob, 0)
    if bits != 4096:
        fail("%s: public key has %d bits, not 4096", what, bits)
    n = int.from_bytes(blob[8:8 + RSA4096_BYTES], "big")
    rr = int.from_bytes(blob[8 + RSA4096_BYTES:], "big")
    if n.bit_length() != 4096 or n % 2 == 0:
        fail("%s: public key modulus is not a 4096-bit odd number", what)
    if (n0inv * n + 1) % (1 << 32) != 0:
        fail("%s: public key n0inv does not match the modulus", what)
    if rr != pow(2, 2 * 4096, n):
        fail("%s: public key rr does not match the modulus", what)
    return n


def rsa_verify(n, sig, digest, alg, what):
    if len(sig) != RSA4096_BYTES:
        fail("%s: signature is %d bytes, not %d", what, len(sig), RSA4096_BYTES)
    s = int.from_bytes(sig, "big")
    if s >= n:
        fail("%s: signature is not smaller than the modulus", what)
    em = pow(s, 65537, n).to_bytes(RSA4096_BYTES, "big")
    t = DIGESTINFO[alg] + digest
    want = b"\x00\x01" + b"\xff" * (RSA4096_BYTES - 3 - len(t)) + b"\x00" + t
    if em != want:
        fail("%s: signature does not verify", what)


# ---------------------------------------------------------------------------
# vbmeta

def cstring(b, what):
    """A NUL-terminated ASCII string in a fixed field; the rest must be zero."""
    i = b.find(b"\0")
    if i < 0:
        fail("%s: not NUL-terminated", what)
    if any(b[i:]):
        fail("%s: bytes after the NUL are not zero", what)
    s = b[:i]
    if not all(0x20 <= c < 0x7f for c in s):
        fail("%s: not printable ASCII", what)
    return s.decode("ascii")


def parse_vbmeta_header(blob, what):
    """AvbVBMetaImageHeader (big-endian). Returns a dict; checks the block layout."""
    if len(blob) < HEADER_SIZE:
        fail("%s: %d bytes, shorter than the vbmeta header", what, len(blob))
    if blob[:4] != AVB_MAGIC:
        fail("%s: no AVB0 magic", what)
    v = struct.unpack_from(">IIQQI11QII", blob, 4)
    h = dict(zip(("major", "minor", "auth_size", "aux_size", "algorithm",
                  "hash_offset", "hash_size", "sig_offset", "sig_size",
                  "pk_offset", "pk_size", "pkm_offset", "pkm_size",
                  "desc_offset", "desc_size", "rollback_index", "flags",
                  "rollback_index_location"), v))
    h["release_string"] = cstring(blob[128:176], what + ": release string")
    if any(blob[176:HEADER_SIZE]):
        fail("%s: reserved header bytes are not zero", what)
    if h["major"] != 1 or h["minor"] != 0:
        fail("%s: needs libavb %d.%d, not 1.0 (what the UC-P10 U-Boots were checked with)",
             what, h["major"], h["minor"])
    if h["auth_size"] % 64 or h["aux_size"] % 64:
        fail("%s: block sizes are not multiples of 64", what)
    total = HEADER_SIZE + h["auth_size"] + h["aux_size"]
    if total > len(blob):
        fail("%s: truncated: the header says %d bytes, the image has %d", what, total, len(blob))
    for a, b, blk in (("hash_offset", "hash_size", "auth_size"), ("sig_offset", "sig_size", "auth_size"),
                      ("pk_offset", "pk_size", "aux_size"), ("pkm_offset", "pkm_size", "aux_size"),
                      ("desc_offset", "desc_size", "aux_size")):
        if h[a] + h[b] > h[blk]:
            fail("%s: %s + %s is outside its block", what, a, b)
    h["total"] = total
    return h


def parse_descriptors(d, what):
    """Split the descriptor block. Returns a list of (tag, body, raw bytes)."""
    out = []
    pos = 0
    while pos < len(d):
        if len(d) - pos < 16:
            fail("%s: descriptor header truncated at %d", what, pos)
        tag, nbf = struct.unpack_from(">QQ", d, pos)
        if nbf % 8:
            fail("%s: descriptor at %d: size %d is not a multiple of 8", what, pos, nbf)
        end = pos + 16 + nbf
        if end > len(d):
            fail("%s: descriptor at %d runs past the descriptor block", what, pos)
        out.append((tag, d[pos + 16:end], d[pos:end]))
        pos = end
    return out


def take(body, fixed, lengths, what):
    """The variable fields after a fixed part, padded with zeros to 8 bytes, exactly."""
    need = fixed + sum(lengths)
    if (need + 7) // 8 * 8 != len(body):
        fail("%s: descriptor size %d does not match its fields (%d)", what, len(body), need)
    if any(body[need:]):
        fail("%s: descriptor padding is not zero", what)
    out, pos = [], fixed
    for n in lengths:
        out.append(body[pos:pos + n])
        pos += n
    return out


def part_name(b, what):
    try:
        s = b.decode("ascii")
    except UnicodeDecodeError:
        fail("%s: partition name is not ASCII", what)
    if not re.fullmatch(r"[a-z0-9_]{1,32}", s):
        fail("%s: bad partition name %r", what, s)
    return s


def hash_alg(field, what):
    name = cstring(field, what + ": hash algorithm")
    if name not in HASH_ALGS:
        fail("%s: hash algorithm %r is not supported", what, name)
    return name


def decode_descriptor(tag, body, what):
    if tag == TAG_HASHTREE:
        if len(body) < HASHTREE_FIXED:
            fail("%s: hashtree descriptor truncated", what)
        (ver, image_size, tree_offset, tree_size, dbs, hbs, roots, fec_offset, fec_size,
         alg, nlen, slen, rlen, flags, reserved) = struct.unpack_from(">IQQQIIIQQ32sIIII60s", body, 0)
        name, salt, root = take(body, HASHTREE_FIXED, (nlen, slen, rlen), what + ": hashtree")
        name = part_name(name, what)
        w = "%s: hashtree %s" % (what, name)
        alg = hash_alg(alg, w)
        if any(reserved):
            fail("%s: reserved bytes are not zero", w)
        if len(root) != HASH_ALGS[alg]:
            fail("%s: root digest is %d bytes, not %d for %s", w, len(root), HASH_ALGS[alg], alg)
        return {"type": "hashtree", "partition": name, "dm_verity_version": ver,
                "image_size": image_size, "tree_offset": tree_offset, "tree_size": tree_size,
                "data_block_size": dbs, "hash_block_size": hbs, "fec_num_roots": roots,
                "fec_offset": fec_offset, "fec_size": fec_size, "hash_algorithm": alg,
                "salt": salt.hex(), "root_digest": root.hex(), "flags": flags}
    if tag == TAG_HASH:
        if len(body) < HASH_FIXED:
            fail("%s: hash descriptor truncated", what)
        image_size, alg, nlen, slen, dlen, flags, reserved = struct.unpack_from(">Q32sIIII60s", body, 0)
        name, salt, digest = take(body, HASH_FIXED, (nlen, slen, dlen), what + ": hash")
        name = part_name(name, what)
        w = "%s: hash %s" % (what, name)
        alg = hash_alg(alg, w)
        if any(reserved):
            fail("%s: reserved bytes are not zero", w)
        if len(digest) != HASH_ALGS[alg]:
            fail("%s: digest is %d bytes, not %d for %s", w, len(digest), HASH_ALGS[alg], alg)
        return {"type": "hash", "partition": name, "image_size": image_size,
                "hash_algorithm": alg, "salt": salt.hex(), "digest": digest.hex(), "flags": flags}
    if tag == TAG_CMDLINE:
        if len(body) < CMDLINE_FIXED:
            fail("%s: kernel cmdline descriptor truncated", what)
        flags, clen = struct.unpack_from(">II", body, 0)
        cmd, = take(body, CMDLINE_FIXED, (clen,), what + ": kernel cmdline")
        if flags & ~CMDLINE_FLAGS or flags == CMDLINE_FLAGS:
            fail("%s: kernel cmdline flags 0x%x", what, flags)
        if not all(0x20 <= c < 0x7f for c in cmd):
            fail("%s: kernel cmdline is not printable ASCII", what)
        return {"type": "kernel_cmdline", "flags": flags, "cmdline": cmd.decode("ascii")}
    if tag == TAG_CHAIN:
        fail("%s: chain partition descriptor: not allowed (no known UC-P10 firmware has one)", what)
    if tag == TAG_PROPERTY:
        fail("%s: property descriptor: not allowed (no known UC-P10 firmware has one)", what)
    fail("%s: unknown descriptor tag %d", what, tag)


def parse_vbmeta(blob, what="vbmeta"):
    """Parse and check a signed vbmeta image (the whole partition image)."""
    h = parse_vbmeta_header(blob, what)
    if any(blob[h["total"]:]):
        fail("%s: bytes after the vbmeta blocks are not zero", what)
    if h["algorithm"] != SHA256_RSA4096:
        fail("%s: algorithm %s, not SHA256_RSA4096", what, ALGORITHMS.get(h["algorithm"], h["algorithm"]))
    if h["hash_size"] != 32 or h["sig_size"] != RSA4096_BYTES:
        fail("%s: hash or signature size does not fit SHA256_RSA4096", what)
    auth = blob[HEADER_SIZE:HEADER_SIZE + h["auth_size"]]
    aux = blob[HEADER_SIZE + h["auth_size"]:h["total"]]
    # the hash does not cover the authentication block: no byte in it but the hash and the signature
    used = bytearray(auth)
    for a, n in ((h["hash_offset"], 32), (h["sig_offset"], RSA4096_BYTES)):
        used[a:a + n] = bytes(n)
    if any(used):
        fail("%s: bytes in the authentication block outside the hash and the signature are not zero", what)
    digest = hashlib.sha256(blob[:HEADER_SIZE] + aux).digest()
    if auth[h["hash_offset"]:h["hash_offset"] + 32] != digest:
        fail("%s: the hash over the header and the auxiliary block does not match", what)
    key = aux[h["pk_offset"]:h["pk_offset"] + h["pk_size"]]
    n = parse_avb_key(key, what)
    rsa_verify(n, auth[h["sig_offset"]:h["sig_offset"] + h["sig_size"]], digest, "sha256", what)
    h["public_key"] = key
    h["public_key_metadata"] = aux[h["pkm_offset"]:h["pkm_offset"] + h["pkm_size"]]
    h["descriptors"] = parse_descriptors(aux[h["desc_offset"]:h["desc_offset"] + h["desc_size"]], what)
    return h


# ---------------------------------------------------------------------------
# ATX certificate chain (libavb_atx AvbAtxPublicKeyMetadata; little-endian fields)

def parse_atx_cert(b, what):
    version, = struct.unpack_from("<I", b, 0)
    if version != 1:
        fail("%s: certificate version %d, not 1", what, version)
    key = b[4:4 + AVB_KEY_SIZE]
    p = 4 + AVB_KEY_SIZE
    kv, = struct.unpack_from("<Q", b, p + 64)
    return {"raw": b, "signed": b[:ATX_SIGNED_SIZE], "key": key, "subject": b[p:p + 32],
            "usage": b[p + 32:p + 64], "key_version": kv, "signature": b[ATX_SIGNED_SIZE:]}


def check_atx(pkm, vbmeta_key, pins):
    """The vbmeta key is the PSK of the pinned PIK. Returns the chain facts."""
    if len(pkm) != ATX_METADATA_SIZE:
        fail("ATX: public key metadata is %d bytes, not %d", len(pkm), ATX_METADATA_SIZE)
    version, = struct.unpack_from("<I", pkm, 0)
    if version != 1:
        fail("ATX: metadata version %d, not 1", version)
    pik = parse_atx_cert(pkm[4:4 + ATX_CERT_SIZE], "ATX PIK")
    psk = parse_atx_cert(pkm[4 + ATX_CERT_SIZE:], "ATX PSK")
    pik_sha = hashlib.sha256(pik["raw"]).hexdigest()
    if pik_sha != pins["pik_cert_sha256"]:
        fail("ATX: PIK certificate %s is not the pinned one %s", pik_sha, pins["pik_cert_sha256"])
    if pik["usage"] != USAGE_PIK:
        fail("ATX: PIK certificate usage is not vboot.ca")
    pik_n = parse_avb_key(pik["key"], "ATX PIK")
    rsa_verify(pik_n, psk["signature"], hashlib.sha512(psk["signed"]).digest(), "sha512",
               "ATX: PSK certificate (by the PIK key)")
    if psk["usage"] != USAGE_PSK:
        fail("ATX: PSK certificate usage is not vboot")
    if psk["subject"].hex() != pins["psk_subject"]:
        fail("ATX: PSK subject %s is not the pinned product %s", psk["subject"].hex(), pins["psk_subject"])
    parse_avb_key(psk["key"], "ATX PSK")
    if psk["key"] != vbmeta_key:
        fail("ATX: the vbmeta public key is not the PSK key")
    return {"public_key_metadata_sha256": hashlib.sha256(pkm).hexdigest(),
            "pik_cert_sha256": pik_sha, "pik_key_version": pik["key_version"],
            "psk_cert_sha256": hashlib.sha256(psk["raw"]).hexdigest(),
            "psk_subject": psk["subject"].hex(), "psk_key_version": psk["key_version"]}


# ---------------------------------------------------------------------------
# Footers, hash and hashtree descriptors

def check_footer(img, desc, raw, data_end):
    """The AVB footer at the end of a covered image, and its (unsigned) vbmeta:
    every descriptor in it must be one of the signed vbmeta."""
    w = "%s: AVB footer" % img.name
    if img.size < FOOTER_SIZE:
        fail("%s: image too small", w)
    f = img.read(img.size - FOOTER_SIZE, FOOTER_SIZE)
    if f[:4] != FOOTER_MAGIC:
        fail("%s: no AVBf magic", w)
    major, minor, orig, voff, vsize = struct.unpack_from(">IIQQQ", f, 4)
    if major != 1 or any(f[36:]):
        fail("%s: version %d.%d or reserved bytes not zero", w, major, minor)
    if orig != desc["image_size"]:
        fail("%s: original image size %d, the descriptor says %d", w, orig, desc["image_size"])
    if voff < data_end or voff + vsize > img.size - FOOTER_SIZE:
        fail("%s: its vbmeta (%d bytes at %d) overlaps the data or the footer", w, vsize, voff)
    blob = img.read(voff, vsize)
    h = parse_vbmeta_header(blob, w + " vbmeta")
    if any(blob[h["total"]:]):
        fail("%s: bytes after its vbmeta are not zero", w)
    if h["algorithm"] != 0 or h["rollback_index"] or h["flags"] or h["rollback_index_location"]:
        fail("%s: its vbmeta is not the plain unsigned one (algorithm 0, rollback 0, flags 0)", w)
    aux = blob[HEADER_SIZE + h["auth_size"]:h["total"]]
    for tag, _, r in parse_descriptors(aux[h["desc_offset"]:h["desc_offset"] + h["desc_size"]], w):
        if r not in raw:
            fail("%s: its vbmeta has a descriptor (tag %d) that the signed vbmeta does not have", w, tag)
    return {"original_image_size": orig, "vbmeta_offset": voff, "vbmeta_size": vsize}


def check_hash(img, desc):
    w = "%s: hash descriptor" % img.name
    if desc["flags"]:
        fail("%s: flags 0x%x", w, desc["flags"])
    if desc["image_size"] > img.size:
        fail("%s: image size %d is larger than the image (%d)", w, desc["image_size"], img.size)
    h = hashlib.new(desc["hash_algorithm"], bytes.fromhex(desc["salt"]))
    for b in img.chunks(0, desc["image_size"]):
        h.update(b)
    if h.hexdigest() != desc["digest"]:
        fail("%s: digest of the image does not match the vbmeta", w)
    return desc["image_size"]


def tree_levels(image_size, digest_size):
    """Sizes of the hash levels (level 0 first) and their offsets in the stored
    tree, where the top level comes first (the avbtool layout)."""
    sizes, size = [], image_size
    while size > BLOCK:
        blocks = (size + BLOCK - 1) // BLOCK
        size = (blocks * digest_size + BLOCK - 1) // BLOCK * BLOCK
        sizes.append(size)
    offsets = [sum(sizes[i + 1:]) for i in range(len(sizes))]
    return sizes, offsets


def check_hashtree(img, desc):
    """Recompute the dm-verity hash tree: level 0 block by block against the stored
    level 0 (so a changed block fails at once), then every upper level and the root."""
    w = "%s: hashtree descriptor" % img.name
    if desc["dm_verity_version"] != 1 or desc["flags"]:
        fail("%s: dm-verity version %d, flags 0x%x", w, desc["dm_verity_version"], desc["flags"])
    if desc["data_block_size"] != BLOCK or desc["hash_block_size"] != BLOCK:
        fail("%s: block sizes %d/%d, not %d", w, desc["data_block_size"], desc["hash_block_size"], BLOCK)
    size = desc["image_size"]
    if size <= BLOCK or size % BLOCK:
        fail("%s: image size %d is not a multiple of %d above one block", w, size, BLOCK)
    if desc["tree_offset"] != size:
        fail("%s: tree offset %d is not right after the data (%d)", w, desc["tree_offset"], size)
    dsize = HASH_ALGS[desc["hash_algorithm"]]
    pad = 1 << (dsize - 1).bit_length()
    sizes, offsets = tree_levels(size, pad)
    if sum(sizes) != desc["tree_size"]:
        fail("%s: tree size %d, the image size gives %d", w, desc["tree_size"], sum(sizes))
    end = desc["tree_offset"] + desc["tree_size"]
    roots = desc["fec_num_roots"]
    if roots:
        rounds = (end // BLOCK + (255 - roots) - 1) // (255 - roots)
        if not 2 <= roots <= 24 or desc["fec_offset"] != end or desc["fec_size"] != rounds * roots * BLOCK:
            fail("%s: FEC (%d roots, %d bytes at %d) is not where and how large it must be",
                 w, roots, desc["fec_size"], desc["fec_offset"])
        end += desc["fec_size"]
    elif desc["fec_offset"] or desc["fec_size"]:
        fail("%s: FEC offset or size without FEC roots", w)
    if end > img.size:
        fail("%s: tree and FEC end at %d, after the image (%d)", w, end, img.size)
    stored = img.read(desc["tree_offset"], desc["tree_size"])
    base = hashlib.new(desc["hash_algorithm"], bytes.fromhex(desc["salt"]))
    zpad = b"\0" * (pad - dsize)
    level0 = bytearray()
    s0 = offsets[0]
    blk = 0
    for chunk in img.chunks(0, size):
        mv = memoryview(chunk)
        for i in range(0, len(chunk), BLOCK):
            h = base.copy()
            h.update(mv[i:i + BLOCK])
            d = h.digest() + zpad
            if stored[s0 + blk * pad:s0 + (blk + 1) * pad] != d:
                fail("%s: data block %d does not match the hash tree in the image", w, blk)
            level0 += d
            blk += 1
    prev = bytes(level0) + b"\0" * (sizes[0] - len(level0))
    if stored[s0:s0 + sizes[0]] != prev:
        fail("%s: level 0 of the stored tree has data after the last hash", w)
    for lv in range(1, len(sizes)):
        out = bytearray()
        for i in range(0, len(prev), BLOCK):
            h = base.copy()
            h.update(prev[i:i + BLOCK])
            out += h.digest() + zpad
        prev = bytes(out) + b"\0" * (sizes[lv] - len(out))
        if stored[offsets[lv]:offsets[lv] + sizes[lv]] != prev:
            fail("%s: level %d of the stored tree does not match", w, lv)
    h = base.copy()
    h.update(prev)
    if h.hexdigest() != desc["root_digest"]:
        fail("%s: root digest does not match the vbmeta", w)
    return end


def check_dm_table(cmdlines, desc):
    """The dm-verity table of the kernel command line (used when verity is on) must
    be exactly the hashtree descriptor of DM_PARTITION: the kernel verifies the
    system with this table, not with the descriptor."""
    w = "kernel cmdline dm= table"
    if any("dm=" in c["cmdline"] for c in cmdlines if c["flags"] != 1):
        fail("%s: a dm= table outside the verity-on command line", w)
    found = [c["cmdline"] for c in cmdlines if c["flags"] == 1 and "dm=" in c["cmdline"]]
    if len(found) != 1:
        fail("%s: %d command lines with a dm= table, not 1", w, len(found))
    m = re.fullmatch(r'dm="([^"]*)" root=/dev/dm-0', found[0])
    if not m:
        fail("%s: not of the form dm=\"...\" root=/dev/dm-0", w)
    t = m.group(1).split(" ")
    dev = "PARTUUID=$(ANDROID_%s_PARTUUID)" % DM_PARTITION.upper()
    bs = str(BLOCK)
    head = ["1", "vroot", "none", "ro", "1,0", str(desc["image_size"] // 512), "verity", "1", dev, dev,
            bs, bs, str(desc["image_size"] // BLOCK), str(desc["tree_offset"] // BLOCK),
            desc["hash_algorithm"], desc["root_digest"], desc["salt"]]
    if t[:len(head)] != head:
        fail("%s: does not match the %s hashtree descriptor", w, DM_PARTITION)
    opt = t[len(head):]
    if not opt or not opt[0].isdigit() or int(opt[0]) != len(opt) - 1:
        fail("%s: optional argument count does not match", w)
    opt = opt[1:]
    want = ["$(ANDROID_VERITY_MODE)"]
    if "ignore_zero_blocks" in opt:
        want.append("ignore_zero_blocks")
    if desc["fec_num_roots"]:
        fb = str(desc["fec_offset"] // BLOCK)
        want += ["use_fec_from_device", dev, "fec_roots", str(desc["fec_num_roots"]),
                 "fec_blocks", fb, "fec_start", fb]
    if opt != want:
        fail("%s: optional arguments %s do not match the %s hashtree descriptor", w, opt, DM_PARTITION)


def verify_avb(images, pins=None, expect=None, opener=open):
    """All AVB checks over the partition images {name: path}. Returns the facts.
    Raises AvbError on the first failure."""
    pins = PINS if pins is None else pins
    expect = EXPECT if expect is None else expect
    vb = Image("vbmeta", images["vbmeta"], opener)
    try:
        h = parse_vbmeta(vb.read(0, vb.size))
    finally:
        vb.close()
    if h["flags"]:
        fail("vbmeta: flags 0x%x (verification or hashtree disabled)", h["flags"])
    atx = check_atx(h["public_key_metadata"], h["public_key"], pins)
    got = {"rollback_index": h["rollback_index"], "rollback_index_location": h["rollback_index_location"],
           "pik_key_version": atx["pik_key_version"], "psk_key_version": atx["psk_key_version"]}
    for k in ("rollback_index", "rollback_index_location", "pik_key_version", "psk_key_version"):
        if got[k] != expect[k]:
            fail("vbmeta: %s is %d, not the reviewed %d", k, got[k], expect[k])
    descs, raw = [], [r for _, _, r in h["descriptors"]]
    for i, (tag, body, _) in enumerate(h["descriptors"]):
        descs.append(decode_descriptor(tag, body, "vbmeta descriptor %d" % i))
    parts = [d for d in descs if d["type"] in ("hash", "hashtree")]
    names = [d["partition"] for d in parts]
    for n in names:
        if names.count(n) > 1:
            fail("vbmeta: two descriptors for partition %s", n)
        if n not in COVERED:
            fail("vbmeta: descriptor for partition %s, which is not one of %s", n, ", ".join(sorted(COVERED)))
        if n not in images:
            fail("vbmeta: descriptor for partition %s, which is not in the firmware", n)
    for n, kind in sorted(COVERED.items()):
        d = [x for x in parts if x["partition"] == n]
        if not d:
            fail("vbmeta: no descriptor for partition %s", n)
        if d[0]["type"] != kind:
            fail("vbmeta: partition %s has a %s descriptor, not %s", n, d[0]["type"], kind)
    check_dm_table([d for d in descs if d["type"] == "kernel_cmdline"],
                   [d for d in parts if d["partition"] == DM_PARTITION][0])
    footers = {}
    # the cheap hash descriptors first, so a broken firmware fails fast
    for d in sorted(parts, key=lambda d: (d["type"] != "hash", d["partition"])):
        img = Image(d["partition"], images[d["partition"]], opener)
        try:
            end = check_hash(img, d) if d["type"] == "hash" else check_hashtree(img, d)
            footers[d["partition"]] = check_footer(img, d, raw, end)
        finally:
            img.close()
    return {"header": h, "atx": atx, "descriptors": descs, "footers": footers}


# ---------------------------------------------------------------------------
# Boot chain: what runs before the A/B tries are counted (not covered by AVB)

def boot_resource(path, hash_size, opener=open):
    """sha256 of the boot image's second stage (the Rockchip resource with the DTB),
    as the installer computes it. It must lie inside the AVB-hashed part of boot."""
    img = Image("boot", path, opener)
    try:
        hd = img.read(0, 48)
        if hd[:8] != b"ANDROID!":
            fail("boot: no ANDROID! magic")
        kernel, ramdisk, second = (struct.unpack_from("<I", hd, o)[0] for o in (8, 16, 24))
        page, version = struct.unpack_from("<II", hd, 36)
        if version > 2 or page < 2048 or page & (page - 1):
            fail("boot: header version %d, page size %d", version, page)
        if not second:
            fail("boot: no resource (second)")
        off = page * (1 + -(-kernel // page) + -(-ramdisk // page))
        if off + second > hash_size:
            fail("boot: the resource ends at %d, after the AVB-hashed part (%d)", off + second, hash_size)
        h = hashlib.sha256()
        for b in img.chunks(off, second):
            h.update(b)
        return h.hexdigest()
    finally:
        img.close()


def uboot_facts(path, opener=open):
    """The U-Boot image: identical 1 MiB copies, each a Rockchip LOADER image. The
    build kind: a user U-Boot forces "locked" and says so; a userdebug one does not."""
    img = Image("uboot", path, opener)
    try:
        if img.size == 0 or img.size % LOADER_COPY:
            fail("uboot: %d bytes, not whole %d-byte copies", img.size, LOADER_COPY)
        copies = [img.read(i, LOADER_COPY) for i in range(0, img.size, LOADER_COPY)]
    finally:
        img.close()
    if any(c != copies[0] for c in copies):
        fail("uboot: the %d copies are not identical", len(copies))
    c = copies[0]
    if c[:8] != LOADER_MAGIC:
        fail("uboot: no LOADER magic")
    n = c.count(USER_UBOOT)
    if n > 1:
        fail("uboot: the user-build text is there %d times", n)
    return {"uboot_build": "user" if n else "userdebug",
            "uboot_loader_version": struct.unpack_from("<I", c, 8)[0]}


# ---------------------------------------------------------------------------
# The manifest

def vbmeta_info(path):
    """AvbVBMetaImageHeader (big-endian, 256 bytes) + the public key it carries."""
    with open(path, "rb") as f:
        blob = f.read()
    if blob[:4] != b"AVB0":
        raise SystemExit("%s: not a vbmeta image" % path)
    auth_size, aux_size = struct.unpack_from(">QQ", blob, 12)
    algorithm, = struct.unpack_from(">I", blob, 28)
    pk_off, pk_size = struct.unpack_from(">QQ", blob, 64)
    rollback_index, = struct.unpack_from(">Q", blob, 112)
    flags, = struct.unpack_from(">I", blob, 120)
    release = blob[128:176].split(b"\0")[0].decode("ascii", "replace")
    aux = 256 + auth_size
    pubkey = blob[aux + pk_off: aux + pk_off + pk_size]
    return {
        "image_bytes": 256 + auth_size + aux_size,
        "algorithm": algorithm,
        "rollback_index": rollback_index,
        "flags": flags,
        "release_string": release,
        "public_key_sha1": hashlib.sha1(pubkey).hexdigest(),
        "public_key_sha256": hashlib.sha256(pubkey).hexdigest(),
    }


def build_prop(system_img, debugfs):
    """Read build.prop from the system ext4 image with debugfs (read-only)."""
    for path in ("/system/build.prop", "/build.prop"):
        with tempfile.NamedTemporaryFile(delete=False) as t:
            tmp = t.name
        try:
            subprocess.run([debugfs, "-R", "dump %s %s" % (path, tmp), system_img],
                           check=True, capture_output=True)
            with open(tmp, "rb") as f:
                text = f.read().decode("utf-8", "replace")
        finally:
            os.unlink(tmp)
        if text.strip():
            props = {}
            for line in text.splitlines():
                if "=" in line and not line.lstrip().startswith("#"):
                    k, v = line.split("=", 1)
                    props[k.strip()] = v.strip()
            return path, {k: props.get(k) for k in BUILD_KEYS}
    return None, {}


def find_images(unpack_dir):
    images = {}
    for name in sorted(os.listdir(unpack_dir)):
        m = re.fullmatch(r"(\d\d)_[a-z]+_([0-9a-f]{4})\.img", name)
        if not m:
            continue
        seg_id = int(m.group(2), 16)
        part = PARTITION.get(seg_id)
        if part is None:
            fail("%s: unknown segment id 0x%04x", name, seg_id)
        if part in images:
            fail("two images for partition %s", part)
        images[part] = (int(m.group(1)), seg_id, os.path.join(unpack_dir, name))
    missing = sorted(set(PARTITION.values()) - set(images))
    if missing:
        fail("missing partition images: %s", ", ".join(missing))
    return images


def build_manifest(pcdfw, unpack_dir, fw_version, schema=1, debugfs="debugfs",
                   read_build=None, pins=None, expect=None, opener=open, log=print):
    """The manifest dict. Every AVB check runs first; AvbError on a failure."""
    images = find_images(unpack_dir)
    paths = {p: v[2] for p, v in images.items()}
    avb = verify_avb(paths, pins, expect, opener)
    log("AVB: signature, ATX chain, %d descriptors: ok" % len(avb["descriptors"]))
    partitions = []
    for part, (idx, seg_id, path) in sorted(images.items(), key=lambda kv: kv[1][0]):
        size = os.path.getsize(path)
        digest = sha256_file(path)
        log("%-7s 0x%04x %12d  %s" % (part, seg_id, size, digest))
        partitions.append({
            "segment": idx,
            "seg_id": "0x%04x" % seg_id,
            "partition": part,
            "image": os.path.basename(path),
            "size": size,
            "sha256": digest,
            "action": ACTION.get(part, "write"),
        })
    if read_build is None:
        prop_path, build = build_prop(paths["system"], debugfs)
    else:
        prop_path, build = read_build(paths["system"])
    firmware = {
        "version": fw_version,
        "file": os.path.basename(pcdfw),
        "size": os.path.getsize(pcdfw),
        "sha256": sha256_file(pcdfw),
    }
    if schema == 1:
        return {"schema": 1, "firmware": firmware, "build": build, "build_prop_path": prop_path,
                "vbmeta": vbmeta_info(paths["vbmeta"]), "partitions": partitions}
    sha = {p["partition"]: p["sha256"] for p in partitions}
    boot_size = [d for d in avb["descriptors"] if d.get("partition") == "boot"][0]["image_size"]
    chain = {"uboot_sha256": sha["uboot"], "trust_sha256": sha["trust"],
             "boot_resource_sha256": boot_resource(paths["boot"], boot_size, opener)}
    chain.update(uboot_facts(paths["uboot"], opener))
    chain["evidence"] = None
    h = avb["header"]
    facts = {"algorithm": ALGORITHMS[h["algorithm"]],
             "required_libavb": "%d.%d" % (h["major"], h["minor"]),
             "flags": h["flags"],
             "rollback_index": h["rollback_index"],
             "rollback_index_location": h["rollback_index_location"]}
    facts.update(avb["atx"])
    # flags and dm_verity_version are checked (0 and 1), so they are not repeated
    facts["descriptors"] = [d if d["type"] == "kernel_cmdline" else
                            {k: v for k, v in d.items() if k not in ("flags", "dm_verity_version")}
                            for d in avb["descriptors"]]
    facts["chain_partitions"] = []
    facts["footers"] = avb["footers"]
    facts["not_covered"] = list(NOT_COVERED)
    return {"schema": 2, "status": "recognise", "firmware": firmware, "build": build,
            "build_prop_path": prop_path, "nvram_model": None, "boot_chain": chain,
            "vbmeta": vbmeta_info(paths["vbmeta"]), "avb": facts, "partitions": partitions}


# The fields a person sets in the reviewed commit; --check does not recompute them.
HUMAN = (("status",), ("nvram_model",), ("boot_chain", "evidence"))


def check_human(m):
    """The person-set fields of a schema-2 manifest, for form."""
    if m.get("schema") != 2:
        return
    if m.get("status") not in ("install", "recognise"):
        fail("manifest: status %r, not install or recognise", m.get("status"))
    ev = m.get("boot_chain", {}).get("evidence")
    if ev is not None and (not isinstance(ev, str) or not ev.strip()):
        fail("manifest: evidence must be null or a text")
    if m["status"] == "install":
        if ev is None:
            fail("manifest: status install needs the evidence that the boot chain booted on a UC-P10")
        if m.get("nvram_model") not in NVRAM_MODELS:
            fail("manifest: status install needs a known nvram model, not %r", m.get("nvram_model"))
    elif m.get("nvram_model") not in (None,) + NVRAM_MODELS:
        fail("manifest: unknown nvram model %r", m.get("nvram_model"))


def compare(want, got):
    """Differences between a stored manifest and a computed one, without the person-set fields."""
    want, got = json.loads(json.dumps(want)), json.loads(json.dumps(got))
    for path in HUMAN:
        for m in (want, got):
            d = m
            for k in path[:-1]:
                d = d.get(k, {}) if isinstance(d, dict) else {}
            if isinstance(d, dict):
                d.pop(path[-1], None)
    diffs = []

    def walk(a, b, at):
        if isinstance(a, dict) and isinstance(b, dict):
            for k in sorted(set(a) | set(b)):
                walk(a.get(k, "<missing>"), b.get(k, "<missing>"), at + "." + k)
        elif isinstance(a, list) and isinstance(b, list) and len(a) == len(b):
            for i, (x, y) in enumerate(zip(a, b)):
                walk(x, y, "%s[%d]" % (at, i))
        elif a != b or type(a) is not type(b):
            diffs.append("%s: manifest %r, firmware %r" % (at, a, b))
    walk(want, got, "")
    return diffs


def main():
    ap = argparse.ArgumentParser(description="Build or check the installer manifest for one firmware.")
    ap.add_argument("pcdfw")
    ap.add_argument("unpack_dir")
    out = ap.add_mutually_exclusive_group(required=True)
    out.add_argument("-o", "--out", help="write the manifest here")
    out.add_argument("--check", metavar="MANIFEST",
                     help="recompute everything and compare with this manifest (in its schema); "
                          "status, nvram_model and the evidence are checked for form only")
    ap.add_argument("--schema", type=int, choices=(1, 2), default=1,
                    help="1: the format the installer embeds (default); 2: the release-2 format")
    ap.add_argument("--debugfs", default="debugfs")
    ap.add_argument("--fw-version", required=True,
                    help="Crestron version, e.g. 1.0.6.20 (only in the encrypted segment "
                         "headers: pcdfw-unpack.py prints it). build.prop does not carry it; "
                         "the installer maps a slot to this version by ro.build.version.incremental")
    args = ap.parse_args()

    stored = None
    if args.check:
        with open(args.check) as f:
            stored = json.load(f)
        if stored.get("schema") not in (1, 2):
            sys.exit("FAIL: %s: schema %r" % (args.check, stored.get("schema")))
        args.schema = stored["schema"]
    try:
        if stored is not None:
            check_human(stored)
        manifest = build_manifest(args.pcdfw, args.unpack_dir, args.fw_version, args.schema, args.debugfs)
    except AvbError as e:
        sys.exit("FAIL: %s" % e)
    build = manifest["build"]
    print("build: %s (%s)" % (build.get("ro.build.display.id"), build.get("ro.build.type")))
    v = manifest["vbmeta"]
    print("vbmeta: rollback_index %d, public key sha1 %s" % (v["rollback_index"], v["public_key_sha1"]))
    if args.schema == 2:
        c = manifest["boot_chain"]
        print("boot chain: uboot %s (%s), trust %s, resource %s" % (
            c["uboot_sha256"], c["uboot_build"], c["trust_sha256"], c["boot_resource_sha256"]))
    if stored is not None:
        diffs = compare(stored, manifest)
        for d in diffs:
            print("DIFF %s" % d)
        if diffs:
            sys.exit("FAIL: %s does not match the firmware (%d difference(s))" % (args.check, len(diffs)))
        print("ok: %s matches the firmware%s" % (
            args.check, " (status %s)" % stored["status"] if args.schema == 2 else ""))
        return
    with open(args.out, "w") as f:
        json.dump(manifest, f, indent=2)
        f.write("\n")
    print("wrote %s" % args.out)


if __name__ == "__main__":
    main()
