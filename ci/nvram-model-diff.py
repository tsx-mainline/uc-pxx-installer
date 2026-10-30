#!/usr/bin/env python3
"""Compare the nvram reader (internal/nvram, Go) with the model of the Android
driver (ci/nvram_drvmodel.py, from the independent release review).

The rule that must hold for every store:
  - the reader says "off" only when the driver gives exactly "false"
    (class A: "off" while the model gives anything else, also "missing" or a
    result that depends on memory the driver does not clear);
  - the reader never says "not clear" or "off" when the driver can give a
    value that Android reads as true, "true" or "1" (class B), in any world.

Every other difference must be one of the stricter, safe differences that the
round-2 review lists (AUTOUPDATE-HARDENING-R2-REVIEW, section 3 and R2-1):
  S-on       "on" while the driver does not give true (a true among duplicate
             records, a tie, a refused or hidden true record)
  S-false    "not clear" while the driver gives exactly "false" (a part that
             is not as the driver's own commit writes it)
  R2-1       "not clear" while Android reads false: "0", a missing key, a case
             variant ("TRUE", " true") or another value
Any other result (an unknown state, the made-up secret in the output) fails.

The cases:
  - the targeted cases of the review (U2, D1..D15, N3) plus the cases of
    TestDriverRule that the random stores rarely make. Each one also has the
    state that the reviewed reader (39a1acd) gives. A different state fails
    too, so that a change of the reader that is safe but not reviewed shows.
  - random stores: 1 to 3 changed parts on a ring of the unit-2 shape (the
    random generator of the review, with fixed seeds and no os.urandom).

  ci/nvram-model-diff.py [--cases N] [--seeds 2,3] [--long] [--harness BIN]

--cases is the number of random stores per seed (default 1500, or
NVRAM_MODEL_CASES). --seeds (default 2,3, or NVRAM_MODEL_SEEDS). --long runs
30000 per seed, as the review did. --harness uses a test binary of
internal/nvram that is already built (go test -c); otherwise the script builds
one with the go tool. Exit status: 0 the rule holds, 1 a failure, 2 a setup
error.
"""
import argparse
import collections
import json
import os
import random
import shutil
import struct
import subprocess
import sys
import tempfile
import time
import zlib

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(HERE)
sys.path.insert(0, HERE)
from nvram_drvmodel import drv, NONDET, MISSING, PART, HDR, DATA  # noqa: E402

TRUE = {"true", "1"}  # NvramManager.getBool
SECRET = "ZZ-MADE-UP-SECRET"  # a made-up value of other keys: must never come out
MAXEX = 5


class Harness:
    """The Go reader: TestModelHarness of internal/nvram on one base ring."""

    def __init__(self, binary, base, tmp):
        self.path = os.path.join(tmp, "base-%d.ring" % id(base))
        with open(self.path, "wb") as f:
            f.write(base)
        env = dict(os.environ, NVRAM_MODEL_HARNESS=self.path)
        self.p = subprocess.Popen([binary, "-test.run=^TestModelHarness$"], stdin=subprocess.PIPE,
                                  stdout=subprocess.PIPE, env=env)

    def run(self, parts):
        w = self.p.stdin
        w.write(b"%d\n" % len(parts))
        for i, p in parts.items():
            assert len(p) == PART
            w.write(bytes([i]) + p)
        w.flush()
        line = self.p.stdout.readline()
        try:
            return json.loads(line), line.decode("utf-8", "replace")
        except ValueError:
            raise SystemExit("FAIL: the Go harness gave no result: %r" % line[:200])

    def close(self):
        self.p.stdin.close()
        rest = self.p.stdout.read()
        rc = self.p.wait()
        os.remove(self.path)
        if rc != 0:
            raise SystemExit("FAIL: the Go harness exited %d: %s" % (rc, rest.decode("utf-8", "replace")[-500:]))


def classify(state, v):
    """The class of one case: a failure starts with "FAIL"."""
    can_true = any(x in TRUE for x in v)
    if state not in ("on", "off", "unknown"):
        return "FAIL-state-%s" % state
    if state == "off" and v != {"false"}:
        return "FAIL-A off, driver " + "|".join(sorted(v))
    if state != "on" and can_true:
        return "FAIL-B %s, driver can give true (%s)" % (state, "|".join(sorted(v)))
    if state == "off":
        return "ok-off"
    if state == "on":
        return "ok-on" if can_true else "S-on (on, the driver does not give true)"
    if NONDET in v:
        return "ok-unknown (the driver depends on memory)"
    if v == {"false"}:
        return "S-false (not clear, the driver gives false)"
    return "R2-1 (not clear, Android reads false)"


# ---- stores (the review's generator, with every random byte from R) ----

def csum(z):
    n = len(z)
    m = (n + 1) & ~1
    zz = z + b"\0" * (m - n)
    return (-sum(struct.unpack(">%dH" % (m // 2), zz))) & 0xffff


class Gen:
    FWV = ["true", "false", "TRUE", "True", "1", "0", " true", "true ", "false ", "", "tru", "truee", "on", "yes",
           "01", "1 "]

    def __init__(self, seed):
        R = self.R = random.Random(seed)
        self.other = [(str(k), "v%d" % k) for k in R.sample(range(100, 99999), 180)]
        self.other += [("2", SECRET + "-adminpw"), ("8", SECRET + "-hash"), ("72013", "0"), ("72014", "02:00"),
                       ("72015", "0")]
        self.base = self.base_ring()

    def records(self, fw="false", apk="true", url="https://example.invalid/fw"):
        rec = [(b"\x01", k, v) for k, v in self.other]
        rec += [(b"\x01", "72005", fw), (b"\x01", "72012", apk), (b"\x01", "72016", url)]
        self.R.shuffle(rec)
        return rec

    @staticmethod
    def text_of(rec, term=True):
        t = b"".join(p + (k.encode("latin-1") if isinstance(k, str) else k)
                     + ((b"=" + v.encode("latin-1")) if v is not None else b"") + b"\0" for p, k, v in rec)
        return t + (b"\0" if term else b"")

    def part(self, text, counter, flags=1, tlen=None, zdata=None, zlen=None, sumv=None, magic=b"FLSH", zpad=b""):
        R = self.R
        z = (zlib.compress(text, 9) if zdata is None else zdata) + zpad
        n = len(z) if zlen is None else zlen
        h = bytearray(R.randbytes(HDR))
        h[0:4] = magic
        struct.pack_into("<I", h, 4, len(text) if tlen is None else tlen & 0xffffffff)
        struct.pack_into("<I", h, 8, len(text) * 100 // 0x1ffd8)
        struct.pack_into("<I", h, 12, n & 0xffffffff)
        struct.pack_into("<H", h, 0x10, flags)
        struct.pack_into("<Q", h, 0x18, counter)
        body = (bytes(h) + z)[:PART]
        pad = R.choice([b"\0", b"\xff", None])
        body = bytearray(body + (R.randbytes(PART - len(body)) if pad is None else pad * (PART - len(body))))
        if sumv is None:
            m = (n + 1) & ~1
            sv = csum(bytes(body[HDR:HDR + min(m, DATA)])) if m <= DATA else 0
        else:
            sv = sumv
        struct.pack_into(">H", body, 0x12, sv)
        return bytes(body)

    def base_ring(self):
        # unit-2 shape: counters 96..127, 72005=true in parts 0..27, false in 28..31
        out = []
        for i in range(32):
            while True:
                p = self.part(self.text_of(self.records("true" if i < 28 else "false")), 96 + i)
                n = struct.unpack_from("<I", p, 12)[0]
                if p[HDR + n:][:1] == b"\0" or n % 2 == 0:
                    break
            out.append(p)
        return out

    def fwv(self):
        R = self.R
        return R.choice(self.FWV) if R.random() < 0.5 else R.choice(["true", "false"])

    def mut_record(self, rec):
        R = self.R
        k = R.randrange(9)
        i = [j for j, (p, kk, v) in enumerate(rec) if kk == "72005"]
        j = i[0] if i else 0
        if k == 0:
            rec[j] = (R.choice([b":", b"\x02", b"\x03", b"\x04", b"", b" ", b"\x01\x01"]), "72005", rec[j][2])
        elif k == 1:
            rec.insert(R.randrange(len(rec) + 1), (R.choice([b"\x01", b":", b"\x02", b"\x03", b""]), "72005", self.fwv()))
        elif k == 2:
            rec.insert(R.randrange(len(rec) + 1), (b"\x01", "noequals%d" % R.randrange(9), None))
        elif k == 3:
            del rec[j]
        elif k == 4:
            rec.insert(R.randrange(len(rec) + 1), (b"", "", None))  # an empty record
        elif k == 5:
            rec[j] = (rec[j][0], R.choice(["72005 ", " 72005", "072005", "72005\x01"]), rec[j][2])
        elif k == 6:
            rec[j] = (rec[j][0], "72005", self.fwv())
        elif k == 7:
            rec.insert(R.randrange(len(rec) + 1), (R.choice([b"\x02", b"\x03"]), "72005", self.fwv()))
        else:
            rec.insert(R.randrange(len(rec) + 1), (b"\x01", "", self.fwv()))
        return rec

    def case(self):
        R = self.R
        parts = {}
        top = 31
        for _ in range(R.choice([1, 1, 1, 2, 2, 3])):
            idx = R.choice([top, top, top, R.randrange(32), 0, 30])
            cnt = struct.unpack_from("<Q", parts.get(idx, self.base[idx]), 0x18)[0]
            if R.random() < 0.3:
                cnt = R.choice([127, 128, 200, 127, 126, 2**64 - 1, 0])
            recs = self.records(self.fwv())
            for _ in range(R.choice([0, 0, 1, 1, 2])):
                recs = self.mut_record(recs)
            text = self.text_of(recs, R.random() > 0.05)
            if R.random() < 0.03:
                text = text[:-1]  # no final NUL at all
            s = R.randrange(14)
            kw = {}
            if s == 0:
                kw["tlen"] = R.choice([len(text) + 1, len(text) - 1, 0, R.randrange(1 << 32),
                                       len(text) ^ (1 << R.randrange(32))])
            elif s == 1:
                z = zlib.compress(text, 9)
                kw.update(zdata=z, zlen=R.randrange(1, len(z)), flags=0)
            elif s == 2:
                kw["flags"] = 0
            elif s == 3:
                kw.update(zlen=R.choice([0x7fffffff, 0x80000000, 0xfffffffe, 0xffffffff, 0x9abcdef0]),
                          sumv=R.choice([0, 0, 0, 1]))
            elif s == 4:
                kw.update(zlen=R.choice([0xffd9, 0xffda, 0x10000, 0x20000, 0x1000000]))
            elif s == 5:
                kw["sumv"] = R.randrange(65536)
            elif s == 6:
                kw["magic"] = R.choice([b"FLSh", b"\0\0\0\0", b"HSLF"])
            elif s == 7:
                z = zlib.compress(text, 9)
                kw.update(zdata=z[:R.randrange(1, len(z))])
            elif s == 8:
                kw["zpad"] = R.randbytes(R.randrange(1, 64))
            elif s == 9:
                z = bytearray(zlib.compress(text, 9))
                for _ in range(R.randrange(1, 4)):
                    z[R.randrange(len(z))] ^= 1 << R.randrange(8)
                kw["zdata"] = bytes(z)
            elif s == 10:  # a zlib bomb: a text that inflates past the driver's buffer
                big = self.text_of(recs, False) + b"\x01pad=" + b"A" * R.choice(
                    [0x1ffd8, 0x1ffd8 - len(text) - 6, 0x30000, 0x100000]) + b"\0\0"
                kw["zdata"] = zlib.compress(big, 9)
                text = big
            elif s == 11:  # a text of exactly the buffer size
                fill = 0x1ffd8 - len(text)
                if fill > 8:
                    body = text[:-1] if text.endswith(b"\0\0") else text
                    big = (body + b"\x01pad=" + b"B" * (fill - 7) + b"\0\0")[:0x1ffd8]
                    text = big
                    kw["zdata"] = zlib.compress(big, 9)
            elif s == 12:  # a tie partner
                parts[R.randrange(32)] = self.part(self.text_of(self.records(self.fwv())), cnt)
            if s in (12, 13) and R.random() < 0.5:  # every other part not valid, part 0 with flags 0
                for q in range(32):
                    if q != idx:
                        if q:
                            parts[q] = self.part(self.text_of(self.records(self.fwv())), R.randrange(200), sumv=0x1234)
                        else:
                            parts[q] = self.part(self.text_of(self.records(self.fwv())), R.randrange(200), flags=0,
                                                 sumv=0x4321)
            parts[idx] = self.part(text, cnt, **kw)
        return parts


# ---- targeted cases: (name, parts, the state of the reviewed reader) ----

def targeted():
    R = random.Random(7)
    other = [(str(k), "v%d" % k) for k in R.sample(range(100, 99999), 180)]
    other += [("2", SECRET + "-adminpw"), ("72013", "0"), ("72014", "02:00"), ("72015", "0")]

    def text(fwrecs, term=True, apk=True):
        rec = [b"\x01%s=%s\0" % (k.encode(), v.encode()) for k, v in other]
        if apk:
            rec += [b"\x0172012=true\0", b"\x0172016=https://example.invalid/fw\0"]
        return b"".join(rec) + b"".join(fwrecs) + (b"\0" if term else b"")

    def part(t, ctr, flags=1, tlen=None, zdata=None, zlen=None, sumv="auto", magic=b"FLSH"):
        z = zlib.compress(t, 9) if zdata is None else zdata
        n = len(z) if zlen is None else zlen
        h = bytearray(HDR)
        h[0:4] = magic
        struct.pack_into("<IIIH", h, 4, len(t) if tlen is None else tlen, len(t) * 100 // 0x1ffd8, n, flags)
        struct.pack_into("<Q", h, 0x18, ctr)
        body = bytearray((bytes(h) + z)[:PART])
        body += b"\0" * (PART - len(body))
        if sumv == "auto":
            m = (n + 1) & ~1
            sv = csum(bytes(body[HDR:HDR + m])) if m <= DATA else 0
        else:
            sv = sumv
        struct.pack_into(">H", body, 0x12, sv)
        return bytes(body)

    def T(*r):
        return text(list(r))

    def fw(v):
        return T(b"\x0172005=" + v + b"\0")

    base = [part(fw(b"true" if i < 28 else b"false"), 96 + i) for i in range(32)]
    z_true = zlib.compress(fw(b"true"), 9)
    broken = lambda p0: dict([(0, p0)] + [(i, part(fw(b"false"), 5, sumv=0x1234)) for i in range(1, 32)])
    cases = [
        ("U2  unit-2 shape (part 31, counter 127, false; 28 older parts true)", {}, "off"),
        ("U2b unit-2 shape, next save (part 0 counter 128, false)", {0: part(fw(b"false"), 128)}, "off"),
        ("U2c unit-2 shape, next save turns it on (part 0 counter 128, true)", {0: part(fw(b"true"), 128)}, "on"),
        ("D1  text length +1 in the active part, true", {31: part(fw(b"true"), 127, tlen=len(fw(b"true")) + 1)}, "on"),
        ("D1b text length +1 in the active part, false",
         {31: part(fw(b"false"), 127, tlen=len(fw(b"false")) + 1)}, "unknown"),
        ("D2  zlib length 0xffffffff, checksum 0, highest counter, true",
         {31: part(fw(b"true"), 128, zlen=0xffffffff)}, "on"),
        ("D2b zlib length 0x80000000, checksum 0, highest counter, true",
         {31: part(fw(b"true"), 128, zlen=0x80000000)}, "on"),
        ("D2c zlib length 0x80000000, checksum 7 (not valid), highest counter, true",
         {31: part(fw(b"true"), 128, zlen=0x80000000, sumv=7)}, "off"),
        ("D2d zlib length 0xffffffff, checksum 1 (not valid), highest counter, true",
         {31: part(fw(b"true"), 128, zlen=0xffffffff, sumv=1)}, "off"),
        ("D2e zlib length 0x80000000, checksum 0, highest counter, false",
         {31: part(fw(b"false"), 128, zlen=0x80000000)}, "unknown"),
        ("D2f zlib length 0x7ffffffe (the sum reads past the part), checksum 0, false",
         {31: part(fw(b"false"), 128, zlen=0x7ffffffe)}, "unknown"),
        ("D3  zlib length 0xffda (past the part), highest counter, true",
         {31: part(fw(b"true"), 128, zlen=0xffda)}, "on"),
        ("D3b zlib length 0xffda (past the part), highest counter, false",
         {31: part(fw(b"false"), 128, zlen=0xffda)}, "unknown"),
        ("D4  flags 0, header zlib length shorter than the stream, true",
         {31: part(fw(b"true"), 128, flags=0, zdata=z_true, zlen=len(z_true) - 40)}, "on"),
        ("D4b flags 1, header zlib length shorter than the stream, true (cleared)",
         {31: part(fw(b"true"), 128, zdata=z_true, zlen=len(z_true) - 40)}, "unknown"),
        ("D5  no valid part; part 0 flags 0 (bad checksum) has true",
         broken(part(fw(b"true"), 5, flags=0, sumv=0x1234)), "on"),
        ("D5b no valid part; part 0 flags 0 has false", broken(part(fw(b"false"), 5, flags=0, sumv=0x1234)), "unknown"),
        ("D5c no valid part; part 0 flags 1 has true (an empty store)",
         broken(part(fw(b"true"), 5, sumv=0x1234)), "unknown"),
        ("D5d part 3 valid (false), part 0 flags 0 with true is not loaded",
         dict(list(broken(part(fw(b"true"), 5, flags=0, sumv=0x1234)).items()) + [(3, part(fw(b"false"), 6))]), "off"),
        ("D6  \":72005=true\" after \"\\x0172005=false\"", {31: part(T(b"\x0172005=false\0", b":72005=true\0"), 127)}, "on"),
        ("D6b only \":72005=true\"", {31: part(T(b":72005=true\0"), 127)}, "on"),
        ("D6c only \":72005=false\"", {31: part(T(b":72005=false\0"), 127)}, "off"),
        ("D7  a record without \"=\" before the only 72005=false",
         {31: part(T(b"\x01noequals\0", b"\x0172005=false\0"), 127)}, "unknown"),
        ("D7b a record without \"=\" between 72005=true and 72005=false",
         {31: part(T(b"\x0172005=true\0", b"\x01noequals\0", b"\x0172005=false\0"), 127)}, "on"),
        ("D8  \"\\x0372005=false\" (attr 3: hidden from get)", {31: part(T(b"\x0372005=false\0"), 127)}, "unknown"),
        ("D8b \"\\x0272005=false\" then \"\\x0172005=true\" (attr 2 lock)",
         {31: part(T(b"\x0272005=false\0", b"\x0172005=true\0"), 127)}, "on"),
        ("D8c \"\\x0372005=true\" (hidden from get, still on)", {31: part(T(b"\x0372005=true\0"), 127)}, "on"),
        ("D9  text without the final empty record, false", {31: part(text([b"\x0172005=false"], term=False), 127)},
         "unknown"),
        ("D10 72005=TRUE", {31: part(fw(b"TRUE"), 127)}, "unknown"),
        ("D10b 72005=\" true\"", {31: part(fw(b" true"), 127)}, "unknown"),
        ("D11 tie: part 30 true, part 31 false, both counter 127", {30: part(fw(b"true"), 127)}, "on"),
        ("D12 tie: part 30 false, part 31 true (driver: 31)",
         {30: part(fw(b"false"), 127), 31: part(fw(b"true"), 127)}, "on"),
        ("D12b tie: parts 30 and 31 false", {30: part(fw(b"false"), 127)}, "unknown"),
        ("D13 duplicate 72005 false,false", {31: part(T(b"\x0172005=false\0", b"\x0172005=false\0"), 127)}, "unknown"),
        ("D14 inflate past 0x1ffd8 (zlib bomb) with true",
         {31: part(fw(b"true")[:-1] + b"\x01pad=" + b"A" * 0x30000 + b"\0\0", 127)}, "unknown"),
        ("D15 no 72012/72016, 72005=false", {31: part(text([b"\x0172005=false\0"], apk=False), 127)}, "off"),
        ("N3  72005=1", {31: part(fw(b"1"), 127)}, "on"),
        ("N3b 72005=0", {31: part(fw(b"0"), 127)}, "unknown"),
        ("N3c no 72005 at all", {31: part(T(), 127)}, "unknown"),
    ]
    return b"".join(base), cases


def build_harness(tmp):
    go = shutil.which("go")
    if not go:
        raise SystemExit("ERROR: no go tool; pass --harness")
    out = os.path.join(tmp, "nvram.test")
    r = subprocess.run([go, "test", "-c", "-o", out, "./internal/nvram"], cwd=REPO)
    if r.returncode != 0:
        raise SystemExit("FAIL: go test -c ./internal/nvram")
    return out


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--cases", type=int, default=int(os.environ.get("NVRAM_MODEL_CASES", "1500")))
    ap.add_argument("--seeds", default=os.environ.get("NVRAM_MODEL_SEEDS", "2,3"))
    ap.add_argument("--long", action="store_true", help="30000 random stores per seed")
    ap.add_argument("--harness", help="a test binary of internal/nvram (go test -c)")
    a = ap.parse_args()
    if a.long:
        a.cases = 30000
    seeds = [int(s) for s in a.seeds.split(",") if s]
    t0 = time.monotonic()
    fails = []
    with tempfile.TemporaryDirectory(prefix="nvram-model-") as tmp:
        binary = a.harness or build_harness(tmp)

        def check(h, name, ring, parts):
            r, raw = h.run(parts)
            v = drv(ring)
            cls = classify(r.get("State"), v)
            if SECRET in raw or "adminpw" in raw:
                cls = "FAIL-leak (a made-up secret in the output)"
            return r, v, cls

        def patch(base, parts):
            ring = bytearray(base)
            for i, p in parts.items():
                ring[i * PART:(i + 1) * PART] = p
            return bytes(ring)

        # targeted
        base, cases = targeted()
        h = Harness(binary, base, tmp)
        stats = collections.Counter()
        for name, parts, want in cases:
            r, v, cls = check(h, name, patch(base, parts), parts)
            stats[cls.split(" ")[0]] += 1
            if cls.startswith("FAIL"):
                fails.append("%s: %s (%s)" % (name, cls, r.get("Why")))
            elif r["State"] != want:
                fails.append("%s: %s, the reviewed reader says %s (%s; driver %s)" % (
                    name, r["State"], want, r.get("Why"), "|".join(sorted(v))))
        h.close()
        print("targeted: %d cases, %s" % (len(cases), ", ".join("%s %d" % kv for kv in sorted(stats.items()))))

        # random
        total = collections.Counter()
        for seed in seeds:
            g = Gen(seed)
            base = b"".join(g.base)
            h = Harness(binary, base, tmp)
            r, v, cls = check(h, "unit-2 shape", base, {})
            if r["State"] != "off" or v != {"false"}:
                fails.append("seed %d: the unit-2 shape gives %s, driver %s" % (seed, r["State"], sorted(v)))
            stats = collections.Counter()
            ex = 0
            for n in range(a.cases):
                parts = g.case()
                r, v, cls = check(h, "", patch(base, parts), parts)
                stats[cls] += 1
                if cls.startswith("FAIL") and ex < MAXEX:
                    ex += 1
                    fails.append("seed %d case %d: %s (%s); changed parts %s" % (
                        seed, n, cls, r.get("Why"), ", ".join(
                            "%d hdr %s" % (i, p[:HDR].hex()) for i, p in sorted(parts.items()))))
            h.close()
            nf = sum(c for k, c in stats.items() if k.startswith("FAIL"))
            print("seed %d: %d stores, %d failures" % (seed, a.cases, nf))
            for k, c in sorted(stats.items()):
                print("  %-50s %d" % (k, c))
            total.update(stats)
    nf = sum(c for k, c in total.items() if k.startswith("FAIL"))
    print("random: %d stores (%d seeds), off %d, on %d, failures %d; %.1f s" % (
        sum(total.values()), len(seeds), total["ok-off"], total["ok-on"], nf, time.monotonic() - t0))
    # a run with no "off" or no "on" checks nothing
    if seeds and a.cases >= 200 and (total["ok-off"] == 0 or total["ok-on"] == 0):
        fails.append("the random stores gave no ok-off or no ok-on: the generator checks nothing")
    for f in fails:
        print("FAIL:", f)
    if fails:
        print("nvram model: FAILED")
        return 1
    print("nvram model: ok")
    return 0


if __name__ == "__main__":
    sys.exit(main())
