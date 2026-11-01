#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""Independent checker for dry-run installs (it shares no code with p10inst).

    dryrun-check.py hash IMAGE OUT.json
        sha256 of every GPT partition, of every gap between them (sectors
        0..first partition with the GPT and the idbloader, the backup GPT),
        of misc without the A/B sector, and the A/B sector itself; of backup
        also without its two key-record sectors, and each of them.
    dryrun-check.py compare BEFORE.json AFTER.json PLAN.json
        after an install: only the target's write ranges and the A/B sector
        changed (and, with a keep-key step, the two key-record sectors of
        backup, each holding the plan's record sha256); each target partition
        holds the plan's payload sha256; the
        A/B sector decodes (with this file's own decoder) to the plan's final
        state; bytes 32..511 of the A/B sector did not change.
    dryrun-check.py cut IMAGE PLAN.json START_SECTOR.bin
        after a cut: the power-cut rule (the keeper is successful and only its
        priority may drop 15->14; the target is as before, disabled or armed; a
        bootable target holds none or all of the payload; the target boots
        next only when armed with the whole payload). Prints one JSON line.
    dryrun-check.py ab IMAGE
        decode the A/B sector.
Exit status: 0 OK, 1 a check failed, 2 usage or I/O error.
"""
import hashlib
import json
import struct
import sys
import zlib

SECTOR = 512
AB_LBA = 49156
# The key-record sectors (KEY-KEEP.md section 3.1): 2048 sectors and 1 sector
# before the end of backup. Own constants: this file shares no code with p10inst.
KEEP_PART, KEEP_FROM_END = "backup", (2048, 1)
MISC_START = 49152
CHUNK = 8 << 20


def sha_range(f, off, n):
    h = hashlib.sha256()
    f.seek(off)
    while n > 0:
        b = f.read(min(CHUNK, n))
        if not b:
            raise IOError("short read")
        h.update(b)
        n -= len(b)
    return h.hexdigest()


def parse_gpt(f):
    f.seek(0, 2)
    size = f.tell()
    last = size // SECTOR - 1
    f.seek(SECTOR)
    hdr = f.read(SECTOR)
    if hdr[:8] != b"EFI PART":
        raise ValueError("no GPT signature")
    hsize, = struct.unpack_from("<I", hdr, 12)
    crc, = struct.unpack_from("<I", hdr, 16)
    h2 = bytearray(hdr[:hsize])
    h2[16:20] = b"\0\0\0\0"
    if zlib.crc32(bytes(h2)) != crc:
        raise ValueError("GPT header CRC")
    my, alt, first, lastu = struct.unpack_from("<QQQQ", hdr, 24)
    ent_lba, = struct.unpack_from("<Q", hdr, 72)
    n, esz, ecrc = struct.unpack_from("<III", hdr, 80)
    f.seek(ent_lba * SECTOR)
    ents = f.read(n * esz)
    if zlib.crc32(ents) != ecrc:
        raise ValueError("GPT entries CRC")
    if alt != last:
        raise ValueError("backup header is not at the last LBA")
    parts = []
    for i in range(n):
        e = ents[i * esz:(i + 1) * esz]
        if e[:16] == bytes(16):
            continue
        s, en = struct.unpack_from("<QQ", e, 32)
        name = e[56:128].decode("utf-16-le").split("\0")[0]
        parts.append({"index": i + 1, "name": name, "start": s, "end": en})
    parts.sort(key=lambda p: p["start"])
    return size, last, parts


def decode_ab(sec):
    """Own AvbABData decoder: returns (slots, pick, error)."""
    slots = [tuple(sec[8 + 4 * i:12 + 4 * i]) for i in range(2)]
    err = None
    if sec[:4] != b"\0AB0" or sec[4] != 1:
        err = "bad magic or version"
    elif any(sec[6:8]) or any(sec[16:28]):
        err = "reserved bytes not zero"
    elif struct.unpack(">I", sec[28:32])[0] != zlib.crc32(sec[:28]):
        err = "crc32 (big-endian) mismatch"
    else:
        for i, (p, t, s, r) in enumerate(slots):
            legal = p <= 15 and t <= 7 and r == 0 and ((s == 1 and t == 0) or s == 0)
            if not legal:
                err = "slot %s illegal" % "ab"[i]
    boot = [p > 0 and (s == 1 or t > 0) for (p, t, s, r) in slots]
    if not any(boot) and err is None:
        err = "no bootable slot"
    if boot[0] and boot[1]:
        pick = 1 if slots[1][0] > slots[0][0] else 0
    else:
        pick = 0 if boot[0] else (1 if boot[1] else -1)
    return slots, pick, err, boot


def successful(s):
    return s[2] == 1 and s[1] == 0 and s[0] >= 1


def cmd_hash(img, out):
    res = {"parts": {}, "gaps": {}}
    with open(img, "rb") as f:
        size, last, parts = parse_gpt(f)
        prev = 0
        for p in parts:
            if p["start"] > prev:
                res["gaps"]["%d-%d" % (prev, p["start"] - 1)] = sha_range(f, prev * SECTOR, (p["start"] - prev) * SECTOR)
            n = (p["end"] - p["start"] + 1) * SECTOR
            ent = {"start": p["start"], "sectors": p["end"] - p["start"] + 1,
                   "sha256": sha_range(f, p["start"] * SECTOR, n)}
            if p["name"].startswith("vbmeta_"):
                ent["head8192"] = sha_range(f, p["start"] * SECTOR, 8192)
                ent["tail"] = sha_range(f, p["start"] * SECTOR + 8192, n - 8192)
            if p["name"] == "misc":
                a = (AB_LBA - p["start"]) * SECTOR
                ent["without_ab"] = hashlib.sha256(
                    bytes.fromhex(sha_range(f, p["start"] * SECTOR, a)) +
                    bytes.fromhex(sha_range(f, (AB_LBA + 1) * SECTOR, n - a - SECTOR))).hexdigest()
            if p["name"] == KEEP_PART and ent["sectors"] >= 4096:
                end = p["end"] + 1
                recs = [end - x for x in KEEP_FROM_END]
                ent["record_lbas"] = recs
                ent["records"] = [sha_range(f, l * SECTOR, SECTOR) for l in recs]
                h, prev_l = hashlib.sha256(), p["start"]
                for l in recs + [end]:
                    h.update(bytes.fromhex(sha_range(f, prev_l * SECTOR, (l - prev_l) * SECTOR)))
                    prev_l = l + 1
                ent["without_records"] = h.hexdigest()
            res["parts"][p["name"]] = ent
            prev = p["end"] + 1
        res["gaps"]["%d-%d" % (prev, last)] = sha_range(f, prev * SECTOR, (last + 1 - prev) * SECTOR)
        f.seek(AB_LBA * SECTOR)
        res["ab_sector"] = f.read(SECTOR).hex()
    with open(out, "w") as o:
        json.dump(res, o, indent=1, sort_keys=True)
    print("hashed %d partitions and %d gaps of %s" % (len(res["parts"]), len(res["gaps"]), img))
    return 0


def cmd_compare(bf, af, pf):
    b, a, plan = (json.load(open(x)) for x in (bf, af, pf))
    errs = []
    writes = {s["write"]["partition"]: s["write"] for s in plan["steps"] if s.get("write")}
    keeps = [s["keep_key"] for s in plan["steps"] if s.get("op") == "keep-key"]
    if len(keeps) > 1 or (keeps and keeps[0]["partition"] != KEEP_PART):
        errs.append("the plan has %d keep-key steps, or one outside %s" % (len(keeps), KEEP_PART))
        keeps = []
    target = plan["target"]
    if b["gaps"] != a["gaps"]:
        errs.append("a gap between partitions changed (GPT, idbloader area or backup GPT)")
    for name, ent in b["parts"].items():
        now = a["parts"].get(name)
        if now is None or now["start"] != ent["start"] or now["sectors"] != ent["sectors"]:
            errs.append("%s: the layout changed" % name)
            continue
        if name == "misc":
            if now["without_ab"] != ent["without_ab"]:
                errs.append("misc changed outside the A/B sector")
            continue
        if name == KEEP_PART and keeps:
            kk = keeps[0]
            if "without_records" not in now or now["record_lbas"] != kk["lbas"]:
                errs.append("%s: the plan's key-record sectors %s are not this checker's %s"
                            % (name, kk["lbas"], now.get("record_lbas")))
            elif now["without_records"] != ent["without_records"]:
                errs.append("%s changed outside its two key-record sectors" % name)
            elif now["records"] != [kk["record_sha256"]] * 2:
                errs.append("%s: a key-record sector does not hold the plan's record" % name)
            continue
        if name in writes:
            w = writes[name]
            if name.startswith("vbmeta_"):
                if w["length"] != 8192 or now["head8192"] != w["sha256"] or now["tail"] != ent["tail"]:
                    errs.append("%s: not the payload, or bytes after the image changed" % name)
            elif w["length"] != now["sectors"] * SECTOR or now["sha256"] != w["sha256"]:
                errs.append("%s: does not hold the payload sha256" % name)
            continue
        if now["sha256"] != ent["sha256"]:
            errs.append("%s changed (not a write target of the plan)" % name)
    for name in writes:
        if not name.endswith("_" + target):
            errs.append("the plan writes %s, not a partition of the target" % name)
    sb, sa = bytes.fromhex(b["ab_sector"]), bytes.fromhex(a["ab_sector"])
    slots, pick, err, boot = decode_ab(sa)
    if err:
        errs.append("A/B after: " + err)
    if sa[32:] != sb[32:]:
        errs.append("A/B bytes 32..511 changed")
    if sa[:32].hex() != plan["ab"]["final"]["hex32"]:
        errs.append("A/B is %s, the plan says %s" % (sa[:32].hex(), plan["ab"]["final"]["hex32"]))
    k = 0 if plan["keeper"] == "a" else 1
    if pick != 1 - k or not successful(slots[k]) or slots[1 - k][:3] != (15, 7, 0):
        errs.append("after the install the target is not the trying next boot with the keeper as successful fallback")
    for e in errs:
        print("FAIL:", e)
    if errs:
        return 1
    print("compare OK: only the %d target ranges%s and the A/B sector changed; A %s B %s, boots next %s (own decoder)"
          % (len(writes), ", the two key-record sectors of backup" if keeps else "", slots[0][:3], slots[1][:3], "ab"[pick]))
    return 0


def cmd_cut(img, pf, startf):
    plan = json.load(open(pf))
    start = open(startf, "rb").read()
    k = 0 if plan["keeper"] == "a" else 1
    t = 1 - k
    out = {"ok": False}
    with open(img, "rb") as f:
        f.seek(AB_LBA * SECTOR)
        sec = f.read(SECTOR)
        slots, pick, err, boot = decode_ab(sec)
        s0, _, _, _ = decode_ab(start)
        out["ab"] = {"a": slots[0][:3], "b": slots[1][:3], "next": "ab"[pick] if pick >= 0 else "-"}

        def bad(why):
            out["error"] = why
            print(json.dumps(out))
            return 1
        if err:
            return bad("A/B not valid: " + err)
        if sec[32:] != start[32:] or sec[:8] != start[:8] or sec[16:28] != start[16:28]:
            return bad("A/B bytes outside the slots changed")
        ks, ks0 = slots[k], s0[k]
        if not successful(ks) or ks[1:] != ks0[1:] or not (ks[0] == ks0[0] or (ks0[0] == 15 and ks[0] == 14)):
            return bad("the keeper changed or is not successful")
        ts = slots[t]
        if ts == s0[t]:
            tstate = "as before"
        elif ts == (0, 0, 0, 0):
            tstate = "disabled"
        elif ts == (15, 7, 0, 0):
            tstate = "armed"
        else:
            return bad("target in an unexpected state")
        out["target"] = tstate
        complete = None
        if boot[t]:
            old = new = True
            for s in plan["steps"]:
                w = s.get("write")
                if not w:
                    continue
                h = sha_range(f, w["part_start_lba"] * SECTOR + w["offset"], w["length"])
                old = old and h == w["before_sha256"]
                new = new and h == w["sha256"]
            if not old and not new:
                return bad("the target is bootable while partly written")
            if tstate == "armed" and not new:
                return bad("the target is armed but does not hold the payload")
            complete = new
        out["target_complete"] = complete
        if pick == t and sec != start and not (tstate == "armed" and complete):
            return bad("the target boots next without the whole payload")
        out["boots"] = "keeper" if pick == k else "target"
    out["ok"] = True
    print(json.dumps(out))
    return 0


def cmd_ab(img):
    with open(img, "rb") as f:
        f.seek(AB_LBA * SECTOR)
        sec = f.read(SECTOR)
    slots, pick, err, boot = decode_ab(sec)
    print("A %s B %s next %s %s" % (slots[0][:3], slots[1][:3], "ab"[pick] if pick >= 0 else "-", err or "valid"))
    return 1 if err else 0


def main(a):
    try:
        if len(a) == 3 and a[0] == "hash":
            return cmd_hash(a[1], a[2])
        if len(a) == 4 and a[0] == "compare":
            return cmd_compare(a[1], a[2], a[3])
        if len(a) == 4 and a[0] == "cut":
            return cmd_cut(a[1], a[2], a[3])
        if len(a) == 2 and a[0] == "ab":
            return cmd_ab(a[1])
    except (OSError, ValueError, KeyError) as e:
        print("dryrun-check: %s" % e, file=sys.stderr)
        return 2
    print(__doc__, file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
