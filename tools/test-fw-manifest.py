#!/usr/bin/env python3
"""Tests of tools/fw-manifest.py: its AVB checks and the schema-2 manifests.

  python3 -I tools/test-fw-manifest.py [-v]

The synthetic tests need nothing: they make small firmwares with their own test
signer (RSA-4096 keys made at run time from a fixed seed: a test product root
key, PIK and PSK; never a real key) and change them in every way the tool must
refuse. The real-firmware tests run only when these are set, and are skipped
otherwise (they need no key, only the decrypted unpack):

  UC_PXX_UNPACK   a folder with unpack-094, unpack-620 and unpack-098
                  (pcdfw-unpack.py output of 1.0.0.94, 1.0.6.20 and the 1.0.0.98 dummy)
  UC_PXX_FW       the folder with the three pcdfw files (for the firmware sha256)
  UC_PXX_DEBUGFS  debugfs (default: debugfs on PATH)
"""
import hashlib
import importlib.util
import io
import json
import math
import os
import random
import shutil
import struct
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location("fw_manifest", os.path.join(HERE, "fw-manifest.py"))
fwm = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(fwm)
AvbError = fwm.AvbError

V2 = os.path.join(HERE, "manifests-v2")
V1 = os.path.join(os.path.dirname(HERE), "manifests")
REAL = {  # unpack folder, pcdfw file, version, schema-2 manifest, schema-1 manifest (embedded) or None
    "094": ("unpack-094", "pcdfw-1.0.0.94-userdebug.bin", "1.0.0.94",
            "manifest-1.0.0.94-userdebug.json", "manifest-1.0.0.94-userdebug.json"),
    "620": ("unpack-620", "pcdfw-1.0.6.20.bin", "1.0.6.20", "manifest-1.0.6.20.json", "manifest-1.0.6.20.json"),
    "098": ("unpack-098", "pcdfw_1.0.0.98_based_94_dummy.bin", "1.0.0.98", "manifest-1.0.0.98-dummy.json", None),
}

# ---------------------------------------------------------------------------
# The test signer (written apart from the tool: its own encoders)

SMALL_PRIMES = [p for p in range(3, 3000) if all(p % q for q in range(2, int(p ** 0.5) + 1))]
DI = {"sha256": "3031300d060960864801650304020105000420",
      "sha512": "3051300d060960864801650304020305000440"}


def _probable_prime(n, rng):
    d, s = n - 1, 0
    while d % 2 == 0:
        d //= 2
        s += 1
    for _ in range(24):
        x = pow(rng.randrange(2, n - 2), d, n)
        if x in (1, n - 1):
            continue
        for _ in range(s - 1):
            x = x * x % n
            if x == n - 1:
                break
        else:
            return False
    return True


def _prime(bits, rng):
    while True:
        c = rng.getrandbits(bits) | (3 << (bits - 2)) | 1
        if all(c % p for p in SMALL_PRIMES) and _probable_prime(c, rng):
            return c


class TestKey:
    """An RSA-4096 test key, made from a seed. Test data only."""

    def __init__(self, seed):
        rng = random.Random(seed)
        while True:
            p, q = _prime(2048, rng), _prime(2048, rng)
            phi = (p - 1) * (q - 1)
            if p != q and math.gcd(65537, phi) == 1 and (p * q).bit_length() == 4096:
                break
        self.n, self.d = p * q, pow(65537, -1, phi)

    def blob(self):
        n0inv = (1 << 32) - pow(self.n, -1, 1 << 32)
        rr = pow(2, 8192, self.n)
        return struct.pack(">II", 4096, n0inv) + self.n.to_bytes(512, "big") + rr.to_bytes(512, "big")

    def sign(self, digest, alg):
        t = bytes.fromhex(DI[alg]) + digest
        em = b"\x00\x01" + b"\xff" * (512 - 3 - len(t)) + b"\x00" + t
        return pow(int.from_bytes(em, "big"), self.d, self.n).to_bytes(512, "big")


_KEYS = {}


def keys():
    if not _KEYS:
        _KEYS.update(prk=TestKey(101), pik=TestKey(202), psk=TestKey(303))
    return _KEYS


USAGE_CA = hashlib.sha256(b"com.google.android.things.vboot.ca").digest()
USAGE_VB = hashlib.sha256(b"com.google.android.things.vboot").digest()
SUBJECT = hashlib.sha256(b"test product id!").digest()


def atx_cert(key_blob, subject, usage, version, signer, cert_version=1):
    signed = struct.pack("<I", cert_version) + key_blob + subject + usage + struct.pack("<Q", version)
    return signed + signer.sign(hashlib.sha512(signed).digest(), "sha512")


def descriptor(tag, body):
    body += b"\0" * (-len(body) % 8)
    return struct.pack(">QQ", tag, len(body)) + body


def hash_desc(name, size, salt, digest, alg=b"sha256", flags=0):
    n = name.encode()
    return descriptor(2, struct.pack(">Q32sIIII60s", size, alg, len(n), len(salt), len(digest), flags, b"")
                      + n + salt + digest)


def hashtree_desc(name, t, salt, flags=0):
    n = name.encode()
    return descriptor(1, struct.pack(">IQQQIIIQQ32sIIII60s", 1, t["size"], t["size"], len(t["tree"]), 4096, 4096,
                                     t["roots"], t["fec_offset"], t["fec_size"], b"sha1", len(n), len(salt),
                                     len(t["root"]), flags, b"") + n + salt + t["root"])


def cmdline_desc(flags, text):
    c = text.encode()
    return descriptor(3, struct.pack(">II", flags, len(c)) + c)


def make_tree(data, salt):
    """The dm-verity tree (sha1, digests padded to 32 bytes), top level first."""
    levels, cur = [], data
    while len(cur) > 4096:
        out = b"".join(hashlib.sha1(salt + cur[i:i + 4096].ljust(4096, b"\0")).digest() + bytes(12)
                       for i in range(0, len(cur), 4096))
        out += bytes(-len(out) % 4096)
        levels.append(out)
        cur = out
    return hashlib.sha1(salt + cur).digest(), b"".join(reversed(levels))


def vbmeta_blob(descs, key, pkm, rollback=0, flags=0, location=0, alg=2, minor=0, signer=None, size=8192):
    d = b"".join(descs)
    pk = key.blob() if key else b""
    aux = d + pk + pkm
    aux += bytes(-len(aux) % 64)
    auth_size = 576 if alg else 0
    hdr = struct.pack(">4sIIQQI11QII", b"AVB0", 1, minor, auth_size, len(aux), alg,
                      0, 32 if alg else 0, 32 if alg else 0, 512 if alg else 0,
                      len(d), len(pk), len(d) + len(pk), len(pkm), 0, len(d),
                      rollback, flags, location) + b"avbtool 1.1.0".ljust(48, b"\0") + bytes(80)
    assert len(hdr) == 256
    auth = b""
    if alg:
        h = hashlib.sha256(hdr + aux).digest()
        auth = (h + (signer or key).sign(h, "sha256")).ljust(auth_size, b"\0")
    blob = hdr + auth + aux
    return blob.ljust(size, b"\0") if size else blob


def put_footer(img, orig, voff, descs):
    fv = vbmeta_blob(descs, None, b"", alg=0, size=0)
    img[voff:voff + len(fv)] = fv
    img[-64:] = struct.pack(">4sIIQQQ", b"AVBf", 1, 0, orig, voff, len(fv)).ljust(64, b"\0")


def dm_cmdline(t, salt):
    dev = "PARTUUID=$(ANDROID_SYSTEM_PARTUUID)"
    b = t["size"] // 4096
    fb = t["fec_offset"] // 4096
    return ('dm="1 vroot none ro 1,0 %d verity 1 %s %s 4096 4096 %d %d sha1 %s %s 10 $(ANDROID_VERITY_MODE) '
            'ignore_zero_blocks use_fec_from_device %s fec_roots 2 fec_blocks %d fec_start %d" root=/dev/dm-0'
            % (t["size"] // 512, dev, dev, b, b, t["root"].hex(), salt.hex(), dev, fb, fb))


NAMES = {"uboot": "00_uboot_fd02.img", "trust": "01_trust_fd03.img", "dtbo": "02_dtbo_fe01.img",
         "vbmeta": "03_vbmeta_fe02.img", "boot": "04_boot_fe04.img", "system": "05_system_fe10.img",
         "vendor": "06_vendor_fe06.img", "oem": "07_oem_fe08.img"}


def make_fw(d, rollback=0, flags=0, location=0, alg=2, minor=0, pik_version=42, psk_version=42,
            psk_signer="pik", psk_usage=USAGE_VB, pik_usage=USAGE_CA, subject=SUBJECT, vbmeta_key="psk",
            signer=None, edit_descs=None, user_uboot=False, boot_hash_size=None, pkm_edit=None):
    """A small, complete firmware unpack in d. Returns (paths, pins, facts)."""
    k = keys()
    rng = random.Random(7)
    salt = rng.randbytes(32)
    out = {}
    # uboot: two identical LOADER copies
    copy = bytearray(1 << 20)
    copy[:8] = b"LOADER  "
    copy[16:24] = struct.pack("<II", 0x200000, 1000)
    if user_uboot:
        copy[5000:5000 + len(fwm.USER_UBOOT)] = fwm.USER_UBOOT
    out["uboot"] = bytes(copy) * 2
    out["trust"] = b"BL3X" + rng.randbytes(65532)
    out["oem"] = rng.randbytes(65536)
    # dtbo: 136 bytes, footer
    dt = bytearray(8192)
    dt[:136] = rng.randbytes(136)
    dtbo_d = hash_desc("dtbo", 136, salt, hashlib.sha256(salt + dt[:136]).digest())
    put_footer(dt, 136, 4096, [dtbo_d])
    out["dtbo"] = bytes(dt)
    # boot: header v1, page 2048, kernel 5000, ramdisk 3000, resource 2500
    bt = bytearray(32768)
    struct.pack_into("<8sIIIIIIIIII", bt, 0, b"ANDROID!", 5000, 0x10008000, 3000, 0x11000000, 2500, 0x10f00000,
                     0x10000100, 2048, 1, 0)
    struct.pack_into("<IQI", bt, 1632, 0, 0, 1648)
    body = rng.randbytes(2048 * 7)
    bt[2048:2048 * 8] = body
    hsize = 2048 * 8 if boot_hash_size is None else boot_hash_size
    res_off = 2048 * (1 + 3 + 2)
    resource = bytes(bt[res_off:res_off + 2500])
    boot_d = hash_desc("boot", hsize, salt, hashlib.sha256(salt + bt[:hsize]).digest())
    put_footer(bt, hsize, 20480, [boot_d])
    out["boot"] = bytes(bt)
    # system: 150 blocks (a two-level tree), FEC 2 roots
    trees, tdesc = {}, {}
    for name, blocks, fec in (("system", 150, True), ("vendor", 10, False)):
        data = rng.randbytes(blocks * 4096)
        root, tree = make_tree(data, salt)
        end = len(data) + len(tree)
        t = {"size": len(data), "tree": tree, "root": root, "roots": 2 if fec else 0,
             "fec_offset": end if fec else 0, "fec_size": 2 * 4096 if fec else 0}
        img = bytearray(end + t["fec_size"] + 8192)
        img[:len(data)] = data
        img[len(data):end] = tree
        if fec:
            img[end:end + t["fec_size"]] = rng.randbytes(t["fec_size"])
        tdesc[name] = hashtree_desc(name, t, salt)
        fdesc = [tdesc[name]]
        if name == "system":
            fdesc += [cmdline_desc(1, dm_cmdline(t, salt)), cmdline_desc(2, "root=PARTUUID=$(ANDROID_SYSTEM_PARTUUID)")]
        put_footer(img, len(data), end + t["fec_size"], fdesc)
        out[name] = bytes(img)
        trees[name] = t
    descs = [tdesc["vendor"], boot_d, tdesc["system"], cmdline_desc(1, dm_cmdline(trees["system"], salt)),
             cmdline_desc(2, "root=PARTUUID=$(ANDROID_SYSTEM_PARTUUID)"), dtbo_d]
    if edit_descs:
        descs = edit_descs(descs, salt, trees)
    # ATX chain: PRK signs the PIK cert, the PIK the PSK cert
    pik_cert = atx_cert(k["pik"].blob(), hashlib.sha256(b"").digest(), pik_usage, pik_version, k["prk"])
    psk_cert = atx_cert(k["psk"].blob(), subject, psk_usage, psk_version, k[psk_signer])
    pkm = struct.pack("<I", 1) + pik_cert + psk_cert
    if pkm_edit:
        pkm = pkm_edit(pkm)
    vkey = k[vbmeta_key]
    out["vbmeta"] = vbmeta_blob(descs, vkey, pkm, rollback, flags, location, alg, minor,
                                signer=k[signer] if signer else None)
    paths = {}
    for name, data in out.items():
        paths[name] = os.path.join(d, NAMES[name])
        with open(paths[name], "wb") as f:
            f.write(data)
    pins = {"pik_cert_sha256": hashlib.sha256(atx_cert(k["pik"].blob(), hashlib.sha256(b"").digest(), USAGE_CA,
                                                         42, k["prk"])).hexdigest(),
            "psk_subject": SUBJECT.hex()}
    return paths, pins, {"resource": hashlib.sha256(resource).hexdigest(), "trees": trees, "salt": salt}


class Patched:
    """A read-only file with one byte changed (xor) and/or cut at a length."""

    def __init__(self, path, flip=None, xor=0x01, cut=None):
        self.f = open(path, "rb")
        self.f.seek(0, 2)
        self.size = self.f.tell() if cut is None else min(cut, self.f.tell())
        self.flip, self.xor, self.pos = flip, xor, 0

    def seek(self, pos, whence=0):
        self.pos = self.size + pos if whence == 2 else pos

    def tell(self):
        return self.pos

    def read(self, n=-1):
        n = self.size - self.pos if n < 0 else min(n, self.size - self.pos)
        self.f.seek(self.pos)
        b = bytearray(self.f.read(max(n, 0)))
        if self.flip is not None and self.pos <= self.flip < self.pos + len(b):
            b[self.flip - self.pos] ^= self.xor
        self.pos += len(b)
        return bytes(b)

    def close(self):
        self.f.close()


def opener_for(target, **kw):
    def opener(path, mode="rb"):
        return Patched(path, **kw) if path == target else open(path, mode)
    return opener


# ---------------------------------------------------------------------------

class Synthetic(unittest.TestCase):
    """Made-up firmwares, signed with the test keys."""

    def setUp(self):
        self.dir = tempfile.mkdtemp(prefix="fwm-test-")

    def tearDown(self):
        shutil.rmtree(self.dir)

    def fw(self, **kw):
        sub = tempfile.mkdtemp(dir=self.dir)
        return make_fw(sub, **kw)

    def ok(self, paths, pins, opener=open):
        return fwm.verify_avb(paths, pins, None, opener)

    def refused(self, paths, pins, pattern, opener=open, expect=None):
        with self.assertRaisesRegex(AvbError, pattern):
            fwm.verify_avb(paths, pins, expect, opener)

    def test_positive(self):
        paths, pins, facts = self.fw()
        r = self.ok(paths, pins)
        self.assertEqual(r["atx"]["pik_key_version"], 42)
        self.assertEqual(r["atx"]["psk_key_version"], 42)
        self.assertEqual(r["atx"]["psk_subject"], SUBJECT.hex())
        self.assertEqual([d["type"] for d in r["descriptors"]],
                         ["hashtree", "hash", "hashtree", "kernel_cmdline", "kernel_cmdline", "hash"])
        self.assertEqual(sorted(r["footers"]), ["boot", "dtbo", "system", "vendor"])

    def test_positive_manifest(self):
        paths, pins, facts = self.fw()
        pcdfw = os.path.join(self.dir, "pcdfw-test.bin")
        with open(pcdfw, "wb") as f:
            f.write(b"not a real container")
        unpack = os.path.dirname(paths["vbmeta"])
        build = lambda _: ("/system/build.prop", {"ro.build.type": "userdebug"})
        m = fwm.build_manifest(pcdfw, unpack, "9.9.9", 2, read_build=build, pins=pins, log=lambda *_: None)
        self.assertEqual(m["schema"], 2)
        self.assertEqual(m["status"], "recognise")
        self.assertIsNone(m["nvram_model"])
        self.assertIsNone(m["boot_chain"]["evidence"])
        self.assertEqual(m["boot_chain"]["boot_resource_sha256"], facts["resource"])
        self.assertEqual(m["boot_chain"]["uboot_build"], "userdebug")
        self.assertEqual(m["boot_chain"]["uboot_loader_version"], 0)
        sha = {p["partition"]: p["sha256"] for p in m["partitions"]}
        self.assertEqual(m["boot_chain"]["uboot_sha256"], sha["uboot"])
        self.assertEqual(m["avb"]["chain_partitions"], [])
        self.assertEqual(m["avb"]["descriptors"][0]["root_digest"], facts["trees"]["vendor"]["root"].hex())
        fwm.check_human(m)
        self.assertEqual(fwm.compare(m, json.loads(json.dumps(m))), [])
        m1 = fwm.build_manifest(pcdfw, unpack, "9.9.9", 1, read_build=build, pins=pins, log=lambda *_: None)
        self.assertEqual(sorted(m1), ["build", "build_prop_path", "firmware", "partitions", "schema", "vbmeta"])

    def test_user_uboot(self):
        paths, pins, _ = self.fw(user_uboot=True)
        self.assertEqual(fwm.uboot_facts(paths["uboot"])["uboot_build"], "user")

    def test_flipped_byte_in_each_covered_image(self):
        paths, pins, facts = self.fw()
        sysz = facts["trees"]["system"]["size"]
        venz = facts["trees"]["vendor"]["size"]
        cases = [
            ("boot", 0, "boot: hash descriptor: digest"),               # the header
            ("boot", 3000, "boot: hash descriptor: digest"),            # the kernel
            ("boot", 2048 * 6 + 100, "boot: hash descriptor: digest"),  # the resource
            ("dtbo", 0, "dtbo: hash descriptor: digest"),
            ("dtbo", 135, "dtbo: hash descriptor: digest"),
            ("system", 0, "system: .*data block 0 does not match"),
            ("system", sysz - 1, "system: .*data block 149 does not match"),
            ("system", sysz + 4096 + 5, "system: .*block 0 does not match"),  # stored level 0
            ("system", sysz + 7, "system: .*level 1 of the stored tree"),    # stored top level
            ("system", sysz + 4096 * 3 - 1, "system: .*level 0 of the stored tree has data"),  # level 0 padding
            ("vendor", 12345, "vendor: .*data block 3 does not match"),
            ("vendor", venz + 1, "vendor: .*data block 0 does not match"),
            ("vendor", venz + 4095, "vendor: .*level 0 of the stored tree has data"),
        ]
        for part, off, pattern in cases:
            with self.subTest(part=part, off=off):
                self.refused(paths, pins, pattern, opener_for(paths[part], flip=off))

    def test_flipped_byte_in_footer(self):
        paths, pins, facts = self.fw()
        size = os.path.getsize(paths["system"])
        voff = facts["trees"]["system"]["size"] + len(facts["trees"]["system"]["tree"]) + 8192
        for off, pattern in ((size - 64, "no AVBf magic"), (size - 64 + 19, "original image size"),
                             (size - 1, "reserved bytes"), (voff, "no AVB0 magic"),
                             (voff + 256 + 40, "signed vbmeta does not have")):
            with self.subTest(off=off):
                self.refused(paths, pins, "system: AVB footer.*" + pattern, opener_for(paths["system"], flip=off))

    def test_not_covered_by_avb(self):
        """uboot, trust, oem and the FEC data are not in the vbmeta: only the sha256 covers them."""
        paths, pins, facts = self.fw()
        fec = facts["trees"]["system"]["fec_offset"]
        for part, off in (("uboot", 100), ("trust", 100), ("oem", 100), ("system", fec + 10)):
            with self.subTest(part=part):
                self.ok(paths, pins, opener_for(paths[part], flip=off))

    def test_every_vbmeta_byte(self):
        """Any single changed byte of the vbmeta image, anywhere in its 8192 bytes, is refused."""
        paths, pins, _ = self.fw()
        with open(paths["vbmeta"], "rb") as f:
            blob = f.read()
        for off in range(len(blob)):
            b = bytearray(blob)
            b[off] ^= 0x01
            with self.assertRaises(AvbError, msg="offset %d" % off):
                fwm.parse_vbmeta(bytes(b))
            if off < 256 + 576 + 32:  # and via the files, for the header and the signature
                if off % 97 == 0:
                    self.refused(paths, pins, "vbmeta", opener_for(paths["vbmeta"], flip=off))

    def test_bad_signature(self):
        paths, pins, _ = self.fw()
        self.refused(paths, pins, "vbmeta: signature does not verify",
                     opener_for(paths["vbmeta"], flip=256 + 32 + 200))
        self.refused(paths, pins, "vbmeta: the hash over the header",
                     opener_for(paths["vbmeta"], flip=256 + 5))

    def test_signed_by_another_key(self):
        # signed and carrying the key of the product root key: a valid vbmeta, but not by the PSK
        paths, pins, _ = self.fw(vbmeta_key="prk")
        self.refused(paths, pins, "the vbmeta public key is not the PSK key")
        # the PSK key in the image, signed with another key
        paths, pins, _ = self.fw(signer="pik")
        self.refused(paths, pins, "vbmeta: signature does not verify")

    def test_wrong_pik_cert(self):
        paths, pins, _ = self.fw()
        self.refused(paths, dict(pins, pik_cert_sha256="00" * 32), "PIK certificate .* is not the pinned one")
        # another PIK certificate (here: version 41) is not the pinned one
        paths, pins, _ = self.fw(pik_version=41)
        self.refused(paths, pins, "PIK certificate .* is not the pinned one")
        # the PSK certificate not signed by the PIK key
        paths, pins, _ = self.fw(psk_signer="prk")
        self.refused(paths, pins, r"PSK certificate \(by the PIK key\): signature does not verify")
        paths, pins, _ = self.fw(psk_signer="psk")
        self.refused(paths, pins, r"PSK certificate \(by the PIK key\): signature does not verify")

    def test_atx_fields(self):
        paths, pins, _ = self.fw(psk_usage=USAGE_CA)
        self.refused(paths, pins, "PSK certificate usage")
        paths, pins, _ = self.fw(pik_usage=USAGE_VB)
        pins = dict(pins, pik_cert_sha256=hashlib.sha256(
            fwm.parse_vbmeta(open(paths["vbmeta"], "rb").read())["public_key_metadata"][4:4 + 1620]).hexdigest())
        self.refused(paths, pins, "PIK certificate usage")
        paths, pins, _ = self.fw(subject=hashlib.sha256(b"other product").digest())
        self.refused(paths, pins, "PSK subject .* is not the pinned product")
        paths, pins, _ = self.fw(pkm_edit=lambda m: m[:-8])
        self.refused(paths, pins, "public key metadata is 3236 bytes")
        paths, pins, _ = self.fw(pkm_edit=lambda m: struct.pack("<I", 2) + m[4:])
        self.refused(paths, pins, "ATX: metadata version 2")
        paths, pins, _ = self.fw(pkm_edit=lambda m: m[:1624] + struct.pack("<I", 2) + m[1628:])
        self.refused(paths, pins, "ATX PSK: certificate version 2")

    def test_rollback_index_and_key_versions(self):
        for kw, pattern in ((dict(rollback=1), "rollback_index is 1, not the reviewed 0"),
                            (dict(location=1), "rollback_index_location is 1, not the reviewed 0"),
                            (dict(psk_version=43), "psk_key_version is 43, not the reviewed 42"),
                            (dict(psk_version=41), "psk_key_version is 41, not the reviewed 42")):
            with self.subTest(**kw):
                paths, pins, _ = self.fw(**kw)
                self.refused(paths, pins, pattern)
        # a new PIK certificate (version 43), pinned: still refused for the version
        paths, pins, _ = self.fw(pik_version=43)
        pkm = fwm.parse_vbmeta(open(paths["vbmeta"], "rb").read())["public_key_metadata"]
        pins = dict(pins, pik_cert_sha256=hashlib.sha256(pkm[4:4 + 1620]).hexdigest())
        self.refused(paths, pins, "pik_key_version is 43, not the reviewed 42")
        # the same firmware, but other reviewed values
        paths, pins, _ = self.fw()
        for k in ("rollback_index", "rollback_index_location", "pik_key_version", "psk_key_version"):
            with self.subTest(expect=k):
                self.refused(paths, pins, k + " is", expect=dict(fwm.EXPECT, **{k: fwm.EXPECT[k] + 1}))
        # the rollback index changed in the image without a new signature
        self.refused(paths, pins, "the hash over the header", opener_for(paths["vbmeta"], flip=119))

    def test_truncated_vbmeta(self):
        paths, pins, _ = self.fw()
        for cut, pattern in ((0, "shorter than the vbmeta header"), (4, "shorter than the vbmeta header"),
                             (255, "shorter than the vbmeta header"), (256, "truncated"),
                             (256 + 576, "truncated"), (6000, "truncated")):
            with self.subTest(cut=cut):
                self.refused(paths, pins, "vbmeta: .*" + pattern, opener_for(paths["vbmeta"], cut=cut))
        with open(paths["vbmeta"], "rb") as f:
            blob = f.read()
        total = fwm.parse_vbmeta_header(blob, "x")["total"]
        for cut in range(0, total, 7):
            with self.assertRaises(AvbError, msg="cut %d" % cut):
                fwm.parse_vbmeta(blob[:cut])

    def test_truncated_images(self):
        paths, pins, facts = self.fw()
        t = facts["trees"]["system"]
        for part, cut in (("boot", 2048 * 8 - 1), ("dtbo", 100), ("system", t["size"] + 100),
                          ("system", t["fec_offset"] + 10), ("vendor", 4096)):
            with self.subTest(part=part, cut=cut):
                self.refused(paths, pins, part, opener_for(paths[part], cut=cut))

    def test_header_rules(self):
        for kw, pattern in ((dict(flags=1), "flags 0x1"), (dict(flags=2), "flags 0x2"),
                            (dict(alg=5), "algorithm SHA512_RSA4096, not SHA256_RSA4096"),
                            (dict(alg=1), "algorithm SHA256_RSA2048"),
                            (dict(minor=1), "needs libavb 1.1")):
            with self.subTest(**kw):
                paths, pins, _ = self.fw(**kw)
                self.refused(paths, pins, pattern)

    def test_descriptor_rules(self):
        def chain(descs, salt, trees):
            n = b"oem"
            return descs + [descriptor(4, struct.pack(">III", 1, len(n), 1032) + bytes(64) + n
                                       + keys()["psk"].blob())]

        def prop(descs, salt, trees):
            return descs + [descriptor(0, struct.pack(">QQ", 3, 1) + b"key\0v\0")]

        def unknown(descs, salt, trees):
            return descs + [descriptor(9, bytes(16))]

        def drop(i):
            return lambda descs, salt, trees: descs[:i] + descs[i + 1:]

        def oem(descs, salt, trees):
            return descs + [hash_desc("oem", 100, salt, bytes(32))]

        def twice(descs, salt, trees):
            return descs + [descs[1]]

        def system_as_hash(descs, salt, trees):
            return [descs[0], descs[1], hash_desc("system", 4096, salt, bytes(32))] + descs[3:]

        def wrong_root(descs, salt, trees):
            t = dict(trees["system"], root=bytes(20))
            return descs[:2] + [hashtree_desc("system", t, salt)] + descs[3:]

        def wrong_digest(descs, salt, trees):
            return descs[:5] + [hash_desc("dtbo", 136, salt, bytes(32))]

        def big_hash(descs, salt, trees):
            return descs[:5] + [hash_desc("dtbo", 9000, salt, bytes(32))]

        def flags(descs, salt, trees):
            return descs[:5] + [hash_desc("dtbo", 136, salt, bytes(32), flags=1)]

        def md5(descs, salt, trees):
            return descs[:5] + [hash_desc("dtbo", 136, salt, bytes(16), alg=b"md5")]

        def no_dm(descs, salt, trees):
            return descs[:3] + descs[4:]

        def dm(edit):
            def f(descs, salt, trees):
                return descs[:3] + [cmdline_desc(1, edit(dm_cmdline(trees["system"], salt)))] + descs[4:]
            return f

        def dm_always(descs, salt, trees):
            return descs[:3] + [cmdline_desc(0, dm_cmdline(trees["system"], salt))] + descs[4:]

        def bad_pad(descs, salt, trees):
            d = bytearray(descs[0])  # the vendor hashtree descriptor ends in 2 padding bytes
            d[-1] = 1
            return [bytes(d)] + descs[1:]

        def root(s):
            return s.replace(s.split(" sha1 ")[1].split(" ")[0], "0" * 40)

        cases = [
            (chain, "chain partition descriptor: not allowed"),
            (prop, "property descriptor: not allowed"),
            (unknown, "unknown descriptor tag 9"),
            (drop(5), "no descriptor for partition dtbo"),
            (drop(1), "no descriptor for partition boot"),
            (drop(0), "no descriptor for partition vendor"),
            (oem, "descriptor for partition oem, which is not one of"),
            (twice, "two descriptors for partition boot"),
            (system_as_hash, "partition system has a hash descriptor, not hashtree"),
            (wrong_root, "dm= table: does not match|root digest"),
            (wrong_digest, "dtbo: hash descriptor: digest"),
            (big_hash, "image size 9000 is larger than the image"),
            (flags, "dtbo: hash descriptor: flags 0x1"),
            (md5, "hash algorithm 'md5' is not supported"),
            (no_dm, "0 command lines with a dm= table"),
            (dm(root), "dm= table: does not match"),
            (dm(lambda s: s.replace("fec_roots 2", "fec_roots 3")), "optional arguments"),
            (dm(lambda s: s.replace(" 10 $(", " 9 $(")), "optional argument count"),
            (dm(lambda s: s.replace("root=/dev/dm-0", "root=/dev/mmcblk0p1")), "not of the form"),
            (dm(lambda s: s.replace("1 vroot", "2 vroot")), "does not match"),
            (dm_always, "a dm= table outside the verity-on command line"),
            (bad_pad, "descriptor padding is not zero"),
        ]
        for edit, pattern in cases:
            with self.subTest(case=edit.__name__ if hasattr(edit, "__name__") else pattern):
                paths, pins, _ = self.fw(edit_descs=edit)
                self.refused(paths, pins, pattern)

    def test_wrong_root_digest(self):
        # the dm= table and the descriptor agree, but the tree in the image is another one
        def edit(descs, salt, trees):
            t = dict(trees["vendor"], root=hashlib.sha1(b"x").digest())
            return [hashtree_desc("vendor", t, salt)] + descs[1:]
        paths, pins, _ = self.fw(edit_descs=edit)
        self.refused(paths, pins, "vendor: AVB footer.*signed vbmeta does not have|vendor: .*root digest")

    def test_resource_outside_the_hash(self):
        paths, pins, _ = self.fw(boot_hash_size=2048 * 7)
        self.ok(paths, pins)
        with self.assertRaisesRegex(AvbError, "resource ends at .* after the AVB-hashed part"):
            fwm.boot_resource(paths["boot"], 2048 * 7)

    def test_uboot_rules(self):
        paths, pins, _ = self.fw()
        self.assertEqual(fwm.uboot_facts(paths["uboot"]), {"uboot_build": "userdebug", "uboot_loader_version": 0})
        for off, pattern in ((5, "copies are not identical"), ((1 << 20) + 9, "copies are not identical")):
            with self.subTest(off=off):
                with self.assertRaisesRegex(AvbError, pattern):
                    fwm.uboot_facts(paths["uboot"], opener_for(paths["uboot"], flip=off))
        with self.assertRaisesRegex(AvbError, "not whole"):
            fwm.uboot_facts(paths["uboot"], opener_for(paths["uboot"], cut=(1 << 20) + 1))

    def test_human_fields(self):
        base = {"schema": 2, "status": "recognise", "nvram_model": None, "boot_chain": {"evidence": None}}
        fwm.check_human(base)
        fwm.check_human(dict(base, status="install", nvram_model="gs-nvram-72005-v1",
                             boot_chain={"evidence": "booted"}))
        for m, pattern in ((dict(base, status="installable"), "status 'installable'"),
                           (dict(base, status="install"), "needs the evidence"),
                           (dict(base, status="install", boot_chain={"evidence": "x"}), "needs a known nvram model"),
                           (dict(base, boot_chain={"evidence": " "}), "evidence must be"),
                           (dict(base, nvram_model="other"), "unknown nvram model")):
            with self.subTest(m=m):
                with self.assertRaisesRegex(AvbError, pattern):
                    fwm.check_human(m)

    def test_compare(self):
        a = {"schema": 2, "status": "install", "nvram_model": "gs-nvram-72005-v1",
             "boot_chain": {"evidence": "x", "uboot_sha256": "aa"}, "avb": {"descriptors": [{"digest": "1"}]}}
        b = json.loads(json.dumps(a))
        b.update(status="recognise", nvram_model=None)
        b["boot_chain"]["evidence"] = None
        self.assertEqual(fwm.compare(a, b), [])
        b["avb"]["descriptors"][0]["digest"] = "2"
        b["boot_chain"]["uboot_sha256"] = "ab"
        self.assertEqual(len(fwm.compare(a, b)), 2)
        b = json.loads(json.dumps(a))
        b["avb"]["descriptors"].append({})
        self.assertEqual(len(fwm.compare(a, b)), 1)


class Committed(unittest.TestCase):
    """The schema-2 manifests in tools/manifests-v2 (no firmware needed)."""

    def load(self, name):
        with open(os.path.join(V2, name)) as f:
            return json.load(f)

    def test_files(self):
        self.assertEqual(sorted(os.listdir(V2)), sorted(r[3] for r in REAL.values()))

    def test_form_and_status(self):
        status = {"094": "install", "620": "recognise", "098": "recognise"}
        for k, r in REAL.items():
            with self.subTest(fw=k):
                m = self.load(r[3])
                self.assertEqual(m["schema"], 2)
                fwm.check_human(m)
                self.assertEqual(m["status"], status[k])
                self.assertEqual(m["firmware"]["version"], r[2])
                self.assertEqual(m["firmware"]["file"], r[1])
                a = m["avb"]
                self.assertEqual(a["algorithm"], "SHA256_RSA4096")
                self.assertEqual(a["pik_cert_sha256"], fwm.PIK_CERT_SHA256)
                self.assertEqual(a["psk_subject"], fwm.PSK_SUBJECT)
                for f in ("rollback_index", "rollback_index_location", "pik_key_version", "psk_key_version"):
                    self.assertEqual(a[f], fwm.EXPECT[f])
                self.assertEqual(a["chain_partitions"], [])
                self.assertEqual(sorted(d["partition"] for d in a["descriptors"] if "partition" in d),
                                 sorted(fwm.COVERED))
                sha = {p["partition"]: p["sha256"] for p in m["partitions"]}
                c = m["boot_chain"]
                self.assertEqual((c["uboot_sha256"], c["trust_sha256"]), (sha["uboot"], sha["trust"]))
                self.assertEqual(c["uboot_build"], m["build"]["ro.build.type"])

    def test_same_as_the_embedded_manifests(self):
        """The release-1 facts of 1.0.0.94 and 1.0.6.20 are the same in both schemas."""
        for k, r in REAL.items():
            if not r[4]:
                continue
            with self.subTest(fw=k):
                with open(os.path.join(V1, r[4])) as f:
                    m1 = json.load(f)
                m2 = self.load(r[3])
                for f in ("firmware", "build", "build_prop_path", "vbmeta", "partitions"):
                    self.assertEqual(m1[f], m2[f], f)

    def test_the_triples(self):
        """094 and the dummy share a resource; each firmware has its own U-Boot; trust is one."""
        c = {k: self.load(r[3])["boot_chain"] for k, r in REAL.items()}
        self.assertEqual(c["094"]["boot_resource_sha256"], c["098"]["boot_resource_sha256"])
        self.assertNotEqual(c["094"]["boot_resource_sha256"], c["620"]["boot_resource_sha256"])
        self.assertEqual(len({x["uboot_sha256"] for x in c.values()}), 3)
        self.assertEqual(len({x["trust_sha256"] for x in c.values()}), 1)
        self.assertTrue(c["094"]["boot_resource_sha256"].startswith("290ef405"))


UNPACK = os.environ.get("UC_PXX_UNPACK")
FWDIR = os.environ.get("UC_PXX_FW")


@unittest.skipUnless(UNPACK and FWDIR and all(os.path.isdir(os.path.join(UNPACK or "", r[0])) for r in REAL.values())
                     and all(os.path.isfile(os.path.join(FWDIR or "", r[1])) for r in REAL.values()),
                     "UC_PXX_UNPACK / UC_PXX_FW not set: the real-firmware tests are skipped")
class Real(unittest.TestCase):
    """The three firmware files on hand (decrypted unpack; no key needed)."""

    def unpack(self, k):
        return os.path.join(UNPACK, REAL[k][0])

    def paths(self, k):
        return {p: v[2] for p, v in fwm.find_images(self.unpack(k)).items()}

    def build(self, k, schema):
        r = REAL[k]
        return fwm.build_manifest(os.path.join(FWDIR, r[1]), self.unpack(k), r[2], schema,
                                  os.environ.get("UC_PXX_DEBUGFS", "debugfs"), log=lambda *_: None)

    def test_positive_all_three(self):
        for k, r in REAL.items():
            with self.subTest(fw=k):
                m = self.build(k, 2)
                with open(os.path.join(V2, r[3])) as f:
                    stored = json.load(f)
                self.assertEqual(fwm.compare(stored, m), [])
                if r[4]:
                    m1 = self.build(k, 1)
                    with open(os.path.join(V1, r[4])) as f:
                        self.assertEqual(f.read(), json.dumps(m1, indent=2) + "\n")

    def test_dummy_passes_avb_and_is_only_recognised(self):
        fwm.verify_avb(self.paths("098"))
        with open(os.path.join(V2, REAL["098"][3])) as f:
            self.assertEqual(json.load(f)["status"], "recognise")

    def test_flipped_byte_in_each_covered_image(self):
        p = self.paths("094")
        r = fwm.verify_avb(p)
        d = {x["partition"]: x for x in r["descriptors"] if "partition" in x}
        with open(p["boot"], "rb") as f:
            kernel, _, ramdisk = struct.unpack_from("<III", f.read(24), 8)
        res = 2048 * (1 + -(-kernel // 2048) + -(-ramdisk // 2048))  # the resource
        cases = [("boot", 0), ("boot", 4096), ("boot", res + 1000), ("boot", d["boot"]["image_size"] - 1),
                 ("dtbo", 0), ("dtbo", 135),
                 ("system", 1000 * 4096 + 1), ("system", d["system"]["tree_offset"] + 10),
                 ("system", d["system"]["tree_offset"] + d["system"]["tree_size"] - 4096 * 3),
                 ("vendor", 7), ("vendor", d["vendor"]["tree_offset"] + 4096 * 5 + 3)]
        for part, off in cases:
            with self.subTest(part=part, off=off):
                with self.assertRaisesRegex(AvbError, part):
                    fwm.verify_avb(p, opener=opener_for(p[part], flip=off))

    def test_vbmeta_negatives_all_three(self):
        for k in REAL:
            p = self.paths(k)
            v = p["vbmeta"]
            cases = [
                ("signature", dict(opener=opener_for(v, flip=256 + 32 + 100)), "signature does not verify"),
                ("hash", dict(opener=opener_for(v, flip=256 + 3)), "hash over the header"),
                ("rollback byte", dict(opener=opener_for(v, flip=119)), "hash over the header"),
                ("PSK key version byte", dict(opener=opener_for(v, flip=256 + 576 + 2416 + 4 + 1620 + 1100)),
                 "hash over the header"),
                ("wrong PIK cert", dict(pins=dict(fwm.PINS, pik_cert_sha256="ab" * 32)), "not the pinned one"),
                ("wrong product", dict(pins=dict(fwm.PINS, psk_subject="ab" * 32)), "not the pinned product"),
                ("rollback index", dict(expect=dict(fwm.EXPECT, rollback_index=1)), "rollback_index is 0"),
                ("PIK version", dict(expect=dict(fwm.EXPECT, pik_key_version=43)), "pik_key_version is 42"),
                ("PSK version", dict(expect=dict(fwm.EXPECT, psk_key_version=41)), "psk_key_version is 42"),
                ("trailing byte", dict(opener=opener_for(v, flip=8000)), "after the vbmeta blocks"),
            ] + [("truncated %d" % c, dict(opener=opener_for(v, cut=c)), "vbmeta: ")
                 for c in (0, 100, 256, 832, 3000, 6527)]
            for name, kw, pattern in cases:
                with self.subTest(fw=k, case=name):
                    with self.assertRaisesRegex(AvbError, pattern):
                        fwm.verify_avb(p, **kw)


if __name__ == "__main__":
    unittest.main()
