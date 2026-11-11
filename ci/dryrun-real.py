#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""Dry-run installs of a real firmware into a file-backed eMMC image.

Run it on a build host, never against a device. It needs the install build of
uc-pxx-inst, uc-pxx-bootctl, the firmware and the unpacked images of the two
supported firmwares (for the slot contents), and a mainline boot image and
root file system for the keeper slot. None of these files is in the repo.

    dryrun-real.py --uc-pxx-inst BIN --bootctl BC [--bootctl-exec RUNNER] --fw FW.bin
                   --layout LAYOUT --work DIR --shape fwd|rev
                   --keeper-fw UNPACK_DIR --target-fw UNPACK_DIR
                   --mainline-boot IMG --mainline-system IMG [--cuts] [--kills]
                   [--user-key-libs LIB,LIB]

    dryrun-real.py --uc-pxx-inst BIN --bootctl BC [--bootctl-exec RUNNER] --layout LAYOUT --work DIR --shape ml
                   --keeper-fw UNPACK_DIR --target-fw UNPACK_DIR --mainline-bundle DIR --stash DIR [--cuts] [--kills]

    dryrun-real.py --uc-pxx-inst BIN --bootctl BC [--bootctl-exec RUNNER] --layout LAYOUT --work DIR --shape direct
                   --keeper-fw UNPACK_DIR --target-fw UNPACK_DIR [--b-fw stock|userdebug] --mainline-bundle DIR
                   (--stash DIR | --stash-fw FW.bin) [--cuts] [--kills]

Shapes (the end-state shape of the design, §5.10):
  fwd  slot A = mainline (keeper, successful prio 15, the --keeper-fw boot chain), slot B = the
       --target-fw firmware (for example stock 1.0.6.20) -> install the firmware --fw into B
  rev  slot B = mainline (keeper), slot A = the --target-fw firmware without trust -> install into A
  ml   the mainline install (MAINLINE-INSTALL-PLAN.md, the unit-3 start; with --stash DIR, the stash of the first
       drive phase, MAINLINE-INT-2026-11-10.md task 2: the plan starts with the keep-key step): slot A = the --target-fw
       firmware (stock, successful prio 14), slot B = the --keeper-fw firmware (1.0.0.94 userdebug,
       successful prio 15, booting) -> install the mainline bundle --mainline-bundle (DIR/uc-pxx-mainline,
       ci/mainline-fixture.py --keeper KEEPER_UNPACK: the REAL kernel/mkimage.sh on made-up parts, the
       keeper's resource) into A with --unlock-confirmed. Needs no --fw, --mainline-boot or
       --mainline-system. BIN must be a devtest build (go build -tags install,devtest): the bundle is
       listed as reviewed only by its test payload gate (--test-review-bundle). The checks are those below with the mainline compare of dryrun-check.py.
  direct the direct path (MAINLINE-INT-2026-11-10.md task 3; DIRECT-DRYRUN-UNIT3-2026-11-10.md F2): a factory-stock
       unit after the BCB-route entry: slot A = the --target-fw firmware (stock, the keeper), slot B = the
       --target-fw firmware (--b-fw stock: factory) or the --keeper-fw one (--b-fw userdebug: a unit that has the
       page's userdebug install), the A/B sector the libavb reset A{15,6,0} B{14,7,0}, both key-record sectors
       zero -> plan --direct with the stash (the 1.0.0.94 userdebug boot chain and key; --stash-fw makes it from
       the image's own systems), install --dry-run --direct --replace-target-confirmed: keep-key, R(K), the six
       writes of B, the arm. The test gate lists the bundle for both slots (--test-review-slots ab: a fiction for a
       slot-A-only payload; only the gate check is). The direct compare and cut rule of dryrun-check.py.
The payload --fw may be any firmware whose embedded manifest is installable (1.0.0.94 userdebug,
1.0.6.20, ...): for 1.0.6.20 as payload, give for example --target-fw the 1.0.0.94 unpack.
What it checks, with ci/dryrun-check.py (its own GPT parser and A/B decoder):
  the full run: only the target's write ranges and the A/B sector change, the
  target holds the payload, the A/B state is the plan's; a second plan is
  refused (the target boots next).
  --cuts: a simulated power cut (uc-pxx-inst --test-cut-after) at the start, at
  every step boundary and inside the big writes; after each cut the power-cut
  rule; then the installer runs again and must complete.
  --kills: the same with SIGKILL of the process at points in the output.
"""
import argparse
import hashlib
import json
import os
import shutil
import signal
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
CHECK = [sys.executable, "-I", os.path.join(HERE, "dryrun-check.py")]
AB_OFF = 49156 * 512


def run(cmd, log=None, check_rc=None):
    t = time.time()
    r = subprocess.run(cmd, capture_output=True, text=True)
    if log:
        with open(log, "w") as f:
            f.write("$ " + " ".join(cmd) + "\n" + r.stdout + r.stderr)
    if check_rc is not None and r.returncode != check_rc:
        sys.stdout.write(r.stdout[-3000:] + r.stderr[-2000:])
        raise SystemExit("FAIL: %s: exit %d, want %d" % (cmd[1], r.returncode, check_rc))
    return r, time.time() - t


class Rig:
    def __init__(self, a):
        self.a = a
        self.w = a.work
        os.makedirs(self.w, exist_ok=True)
        self.pristine = os.path.join(self.w, "pristine.img")
        self.img = os.path.join(self.w, "emmc.img")
        self.results = []

    def common(self):
        a = self.a
        if a.shape == "direct":
            # the direct path: no --pre-ab (the BCB-route entry leaves no pre-entry sector), the stash, --direct;
            # the TEST gate lists the bundle for both slots
            c = ["--image", self.img, "--mainline", a.mainline_bundle, "--bootctl", a.bootctl, "--prereqs-confirmed",
                 "--unlock-confirmed", "--hash-keeper", "--tmp", self.w, "--test-review-bundle", a.mainline_bundle,
                 "--test-review-slots", "ab", "--stash", a.stash, "--direct"]
            if a.bootctl_exec:
                c += ["--bootctl-exec", a.bootctl_exec]
            return c
        if a.shape == "ml":
            # --test-review-bundle: a TEST payload gate that lists this made-up bundle (devtest build;
            # the release's gate lists no payload yet)
            c = ["--image", self.img, "--mainline", a.mainline_bundle, "--bootctl", a.bootctl, "--prereqs-confirmed",
                 "--unlock-confirmed", "--hash-keeper", "--tmp", self.w, "--test-review-bundle", a.mainline_bundle]
            # the CLI image mode takes the key state only with a stash (mainlineKeyOf); on unit 2 the device mode
            # plans "kept" with no stash too (devmode keptBoth): here the stash is given and the plan must say "kept"
            c += ["--stash", a.stash]
        else:
            c = ["--image", self.img, "--fw", a.fw, "--bootctl", a.bootctl, "--prereqs-confirmed", "--hash-keeper",
                 "--tmp", self.w]
        if a.bootctl_exec:
            c += ["--bootctl-exec", a.bootctl_exec]
        with open(self.img, "rb") as f:
            f.seek(AB_OFF)
            c += ["--pre-ab", f.read(32).hex()]
        return c

    def confirm(self):
        # the direct install: the person's confirmation of what slot B holds now (owner F1)
        return ["--replace-target-confirmed"] if self.a.shape == "direct" else []

    def make(self):
        a = self.a
        ud, st = a.userdebug, a.stock
        u = lambda n: os.path.join(ud, n)
        s = lambda n: os.path.join(st, n)
        keep, tgt = ("a", "b") if a.shape == "fwd" else ("b", "a")
        if a.shape == "direct":
            names = {"uboot": "00_uboot_fd02.img", "trust": "01_trust_fd03.img", "dtbo": "02_dtbo_fe01.img",
                     "vbmeta": "03_vbmeta_fe02.img", "boot": "04_boot_fe04.img", "system": "05_system_fe10.img",
                     "vendor": "06_vendor_fe06.img", "oem": "07_oem_fe08.img"}
            bsrc = u if a.b_fw == "userdebug" else s
            fill = {}
            for part, n in names.items():
                fill[part + "_a"] = s(n)     # the stock keeper
                fill[part + "_b"] = bsrc(n)  # factory stock, or the page's userdebug install
            return self.make_fill(fill, "a=15,6,0,b=14,7,0")
        if a.shape == "ml":
            names = {"uboot": "00_uboot_fd02.img", "trust": "01_trust_fd03.img", "dtbo": "02_dtbo_fe01.img",
                     "vbmeta": "03_vbmeta_fe02.img", "boot": "04_boot_fe04.img", "system": "05_system_fe10.img",
                     "vendor": "06_vendor_fe06.img", "oem": "07_oem_fe08.img"}
            fill = {}
            for part, n in names.items():
                fill[part + "_a"] = s(n)  # the stock slot
                fill[part + "_b"] = u(n)  # the userdebug keeper
            return self.make_fill(fill, "a=14,0,1,b=15,0,1")
        fill = {
            "uboot_" + keep: u("00_uboot_fd02.img"), "trust_" + keep: u("01_trust_fd03.img"),
            "dtbo_" + keep: u("02_dtbo_fe01.img"), "vbmeta_" + keep: u("03_vbmeta_fe02.img"),
            "boot_" + keep: a.mainline_boot, "system_" + keep: a.mainline_system,
            "uboot_" + tgt: s("00_uboot_fd02.img"), "dtbo_" + tgt: s("02_dtbo_fe01.img"),
            "vbmeta_" + tgt: s("03_vbmeta_fe02.img"), "boot_" + tgt: s("04_boot_fe04.img"),
            "system_" + tgt: s("05_system_fe10.img"), "vendor_" + tgt: s("06_vendor_fe06.img"),
            "oem_" + tgt: s("07_oem_fe08.img"),
        }
        if a.shape == "fwd":
            fill["trust_" + tgt] = s("01_trust_fd03.img")  # identical to the payload: skipped
        ab = "a=15,0,1,b=14,0,1" if a.shape == "fwd" else "a=14,0,1,b=15,0,1"
        return self.make_fill(fill, ab)

    def make_fill(self, fill, ab):
        a = self.a
        for p in (self.pristine, self.img):
            if os.path.exists(p):
                os.remove(p)
        cmd = [a.uc_pxx_inst, "fake-emmc", "--layout", a.layout, "-o", self.pristine, "--ab", ab, "--markers"]
        for k, v in sorted(fill.items()):
            cmd += ["--fill", "%s=%s" % (k, v)]
        r, dt = run(cmd, os.path.join(self.w, "fake-emmc.log"), 0)
        print("image: %s (%.0f s)" % (r.stdout.strip(), dt))
        # the fixture itself: each filled partition starts with its file
        lay = {}
        for line in open(a.layout):
            f = line.split()
            if len(f) == 4 and f[0].isdigit():
                lay[f[1]] = int(f[2])
        with open(self.pristine, "rb") as img:
            for k, v in sorted(fill.items()):
                n = os.path.getsize(v)
                want = hashlib.sha256(open(v, "rb").read()).hexdigest()
                img.seek(lay[k] * 512)
                got = hashlib.sha256(img.read(n)).hexdigest()
                if got != want:
                    raise SystemExit("FAIL: fixture: %s does not hold %s" % (k, v))
        print("fixture: %d partitions filled and checked against their files" % len(fill))
        if a.shape in ("ml", "direct"):
            # task 2 (review T2 MAJOR-1): --markers writes into the last sectors of every partition, so the
            # second key-record sector of backup would read as foreign data and the plan would refuse. A
            # unit's empty record sectors are zero (KEY-KEEP.md section 9): zero the two sectors here.
            bk = None
            for line in open(a.layout):
                f = line.split()
                if len(f) == 4 and f[0].isdigit() and f[1] == "backup":
                    bk = (int(f[2]), int(f[3]))
            if bk is None:
                raise SystemExit("FAIL: the layout has no backup partition")
            with open(self.pristine, "r+b") as img:
                for back in (2048, 1):
                    img.seek((bk[0] + bk[1] - back) * 512)
                    img.write(bytes(512))
            print("fixture: the two key-record sectors of backup zeroed (LBA %d, %d)" % (bk[0] + bk[1] - 2048, bk[0] + bk[1] - 1))
        with open(self.pristine, "rb") as f:
            f.seek(AB_OFF)
            self.start = f.read(512)
        self.start_file = os.path.join(self.w, "start-sector.bin")
        with open(self.start_file, "wb") as f:
            f.write(self.start)
        run(CHECK + ["hash", self.pristine, os.path.join(self.w, "before.json")], None, 0)

    def fresh(self):
        if os.path.exists(self.img):
            os.remove(self.img)
        subprocess.run(["cp", "--sparse=always", self.pristine, self.img], check=True)

    def compare(self, tag, img=None, say=print):
        # img, say: another view of the image and the job's output (devmode-loop.py --jobs: a job's snapshot)
        after = os.path.join(self.w, "after-%s.json" % tag)
        # a mainline plan: the hash of the written ranges needs the plan (dryrun-check.py hash IMAGE OUT PLAN)
        withplan = [os.path.join(self.w, "plan.json")] if self.a.shape in ("ml", "direct") else []
        run(CHECK + ["hash", img or self.img, after] + withplan, None, 0)
        r, _ = run(CHECK + ["compare", os.path.join(self.w, "before.json"), after, os.path.join(self.w, "plan.json")])
        say("  " + (r.stdout + r.stderr).strip().replace("\n", "\n  "))
        if r.returncode != 0:
            raise SystemExit("FAIL: compare after %s" % tag)
        os.remove(after)

    def full(self):
        self.fresh()
        a = self.a
        r, dt = run([a.uc_pxx_inst, "plan"] + self.common() + ["--json", os.path.join(self.w, "plan.json")],
                    os.path.join(self.w, "plan.log"), 0)
        print("plan OK (%.0f s): %s" % (dt, [l for l in r.stdout.splitlines() if "id " in l][0].strip()))
        if a.shape == "direct":
            run(CHECK + ["hash", self.pristine, os.path.join(self.w, "before.json"), os.path.join(self.w, "plan.json")], None, 0)
            p = json.load(open(os.path.join(self.w, "plan.json")))
            if p.get("kind") != "p10inst-mainline-direct-plan" or p["target"] != "b" or p["keeper"] != "a" or not p["unlock_direct"]["confirmed"]:
                raise SystemExit("FAIL: not the direct plan of target b, keeper a, unlock confirmed")
            ops = [x["op"] + (" " + x["bootctl"]["args"][0] if x.get("bootctl") else "") for x in p["steps"]]
            if ops[:3] != ["keep-key", "bootctl repair", "bootctl disable"] or ops[-2:] != ["bootctl arm-trying", "check-ab"]:
                raise SystemExit("FAIL: the direct plan's steps: %s" % ops)
            print("direct plan: target b, keeper a; slot B now: %s; sources %s" % (
                p["target_now"]["text"], ", ".join("%s<-%s" % (x["partition"], x["from"]) for x in p["write_sources"])))
            # the install refuses without the confirmation of what slot B holds (owner F1): nothing written
            r, _ = run([a.uc_pxx_inst, "install", "--dry-run"] + self.common() + ["--plan", os.path.join(self.w, "plan.json")],
                       os.path.join(self.w, "install-noconfirm.log"))
            if r.returncode != 1 or "--replace-target-confirmed" not in r.stdout:
                raise SystemExit("FAIL: the direct install without --replace-target-confirmed: exit %d" % r.returncode)
            run(CHECK + ["hash", self.img, os.path.join(self.w, "after-noconfirm.json")], None, 0)
            h1, h0 = (json.load(open(os.path.join(self.w, x))) for x in ("after-noconfirm.json", "before.json"))
            if ({k: v["sha256"] for k, v in h1["parts"].items()} != {k: v["sha256"] for k, v in h0["parts"].items()} or
                    h1["gaps"] != h0["gaps"] or h1["ab_sector"] != h0["ab_sector"]):
                raise SystemExit("FAIL: the direct install without the confirmation changed the image")
            os.remove(os.path.join(self.w, "after-noconfirm.json"))
            print("the direct install without --replace-target-confirmed: refused, the image unchanged")
        if a.shape == "ml":
            # the before hashes with the plan's ranges (the plan wrote nothing: the image is the pristine one)
            run(CHECK + ["hash", self.pristine, os.path.join(self.w, "before.json"), os.path.join(self.w, "plan.json")], None, 0)
            p = json.load(open(os.path.join(self.w, "plan.json")))
            if p.get("kind") != "p10inst-mainline-plan" or p["target"] != "a" or p["keeper"] != "b" or not p["unlock"]["confirmed"]:
                raise SystemExit("FAIL: not the mainline plan of target a, keeper b, unlock confirmed")
            print("mainline plan: target a, keeper b; bootchain %s; sources %s" % (
                p["bootchain"]["reviewed_firmware"], ", ".join("%s<-%s" % (x["partition"], x["from"]) for x in p["write_sources"])))
        log = os.path.join(self.w, "install.jsonl")
        if os.path.exists(log):
            os.remove(log)
        r, dt = run([a.uc_pxx_inst, "install", "--dry-run"] + self.common() + self.confirm() + ["--plan", os.path.join(self.w, "plan.json"),
                    "--log", log], os.path.join(self.w, "install.log"), 0)
        print("install --dry-run OK (%.0f s): %s" % (dt, r.stdout.strip().splitlines()[-1]))
        self.compare("full")
        r, _ = run([a.uc_pxx_inst, "plan"] + self.common(), os.path.join(self.w, "replan.log"))
        refused = r.returncode == 1 and "RESULT: refused" in r.stdout
        print("a second plan after the install: %s" % ("refused (the target boots next)" if refused else "NOT refused"))
        if not refused:
            raise SystemExit("FAIL: second plan accepted")
        self.steps = [json.loads(l) for l in open(log) if '"step"' in l and '"op"' in l and '"writes"' in l]

    def check_cut(self, tag, img=None):
        r, _ = run(CHECK + ["cut", img or self.img, os.path.join(self.w, "plan.json"), self.start_file])
        st = json.loads(r.stdout.strip().splitlines()[-1])
        if r.returncode != 0 or not st.get("ok"):
            raise SystemExit("FAIL: %s: %s" % (tag, r.stdout))
        return st

    def resume(self, tag, st):
        a = self.a
        if st.get("boots") == "target":
            r, _ = run([a.uc_pxx_inst, "plan"] + self.common())
            if r.returncode != 1:
                raise SystemExit("FAIL: %s: the target boots next but a new plan was accepted" % tag)
            self.compare(tag)
            return "plan refused (target boots next), state complete"
        log = os.path.join(self.w, "resume-%s.log" % tag)
        r, dt = run([a.uc_pxx_inst, "install", "--dry-run"] + self.common() + self.confirm(), log)
        how = "resumed"
        if r.returncode != 0 and "key search" in r.stdout + r.stderr:
            # FINDING: the key came only from the target's system, and the cut left it half written.
            # A user then has to give a key file; here the extracted libraries stand in for it.
            libs = []
            for l in a.user_key_libs.split(","):
                libs += ["--lib", l]
            r, dt = run([a.uc_pxx_inst, "install", "--dry-run"] + self.common() + libs, log + ".keyfile", 0)
            how = "key search in the image FAILED (target system half written); resumed with the user's key source"
        elif r.returncode != 0:
            sys.stdout.write(r.stdout[-3000:] + r.stderr[-2000:])
            raise SystemExit("FAIL: %s: resume: exit %d" % (tag, r.returncode))
        self.compare(tag)
        return "%s, install complete (%.0f s)" % (how, dt)

    def cuts(self):
        bounds = sorted({0} | {s["writes"] for s in self.steps})
        final = max(bounds)
        # inside the long writes: the middle of system, and one write into the oem image
        sw = [s for s in self.steps if s["op"] == "write"]
        prev = {s["step"]: s["writes"] for s in self.steps}
        for s in sw:
            if s["step"] - 1 in prev and s["writes"] - prev[s["step"] - 1] > 100:
                bounds.append((prev[s["step"] - 1] + s["writes"]) // 2)
        bounds = sorted(set(b for b in bounds if b < final - 1)) + [final - 1]  # final - 1 once (MAINLINE-RC-REVIEW NIT-3)
        print("cut points (primitive writes done before the cut): %s of %d" % (bounds, final))
        for torn, w in [(False, b) for b in bounds] + [(True, bounds[len(bounds) // 2])]:
            tag = "cut%d%s" % (w, "t" if torn else "")
            self.fresh()
            cmd = [self.a.uc_pxx_inst, "install", "--dry-run"] + self.common() + self.confirm() + ["--test-cut-after", str(w)]
            if torn:
                cmd.append("--test-torn")
            r, _ = run(cmd, os.path.join(self.w, "%s.log" % tag))
            if r.returncode != 1 or "simulated power cut" not in r.stdout:
                raise SystemExit("FAIL: %s: %s" % (tag, r.stdout[-1500:]))
            st = self.check_cut(tag)
            how = self.resume(tag, st)
            line = "%-8s A%s B%s next %s target %-9s -> %s" % (tag, tuple(st["ab"]["a"]), tuple(st["ab"]["b"]),
                                                             st["ab"]["next"], st["target"], how)
            print("  " + line)
            self.results.append(line)

    def kills(self):
        points = [("bootctl disable", 0.0), ("write system_", 3.0), ("write vendor_", 0.5), ("write oem_", 1.0),
                  ("verify", 1.0), ("bootctl arm-trying", 0.0)]
        if self.a.shape == "ml":  # a mainline install writes no vendor_a, no oem_a
            points = [("bootctl disable", 0.0), ("write boot_a", 0.0), ("write system_a", 1.0), ("verify", 0.5),
                      ("bootctl arm-trying", 0.0)]
        if self.a.shape == "direct":  # keep-key, R(K), then slot B; no vendor_b, no oem_b
            points = [("keep-key", 0.0), ("bootctl repair", 0.0), ("bootctl disable", 0.0), ("write boot_b", 0.0),
                      ("write system_b", 1.0), ("verify", 0.5), ("bootctl arm-trying", 0.0)]
        for pat, delay in points:
            tag = "kill-" + pat.split()[-1].rstrip("_")
            self.fresh()
            p = subprocess.Popen([self.a.uc_pxx_inst, "install", "--dry-run"] + self.common() + self.confirm(), stdout=subprocess.PIPE,
                                 stderr=subprocess.STDOUT, text=True)
            seen = False
            for line in p.stdout:
                if pat in line and "/" in line.split()[0]:
                    seen = True
                    time.sleep(delay)
                    p.send_signal(signal.SIGKILL)
                    break
            p.wait()
            if not seen:
                raise SystemExit("FAIL: %s: the pattern never showed" % tag)
            st = self.check_cut(tag)
            how = self.resume(tag, st)
            line = "%-14s A%s B%s next %s target %-9s -> %s" % (tag, tuple(st["ab"]["a"]), tuple(st["ab"]["b"]),
                                                               st["ab"]["next"], st["target"], how)
            print("  " + line)
            self.results.append(line)


def ab_sector(base, slots):
    # a test image's A/B sector (AvbABData: magic, version 1.0, two slots, the CRC32 big-endian over bytes 0..27);
    # bytes 32..511 of base kept. For dry-run images only.
    import binascii
    b = bytearray(base)
    b[0:4], b[4], b[5] = b"\0AB0", 1, 0
    for i, (pr, tr, su) in enumerate(slots):
        b[8 + 4 * i:12 + 4 * i] = bytes([pr, tr, su, 0])
    b[28:32] = (binascii.crc32(bytes(b[:28])) & 0xffffffff).to_bytes(4, "big")
    return bytes(b)


def unit2_setup(r):
    # unit 2 (MAINLINE-INT §7 R2): both record sectors hold the pinned record 7143b925 (the stash's record), LBA 1024
    # zero. First the baseline S0 (A{15,0,1} B{14,0,1}, A = mainline runs): the plan must refuse. Then the state the
    # flow's switch to B leaves (A{14,0,1} B{15,0,1}): the install below, with no first drive phase.
    a = r.a
    rec = open(os.path.join(a.stash, "key.rec"), "rb").read()
    if len(rec) != 512:
        raise SystemExit("FAIL: the stash's record is not 512 bytes")
    bk = None
    for line in open(a.layout):
        f = line.split()
        if len(f) == 4 and f[0].isdigit() and f[1] == "backup":
            bk = (int(f[2]), int(f[3]))
    with open(r.pristine, "r+b") as img:
        for back in (2048, 1):
            img.seek((bk[0] + bk[1] - back) * 512)
            img.write(rec)
        img.seek(1024 * 512)  # unit 2's LBA 1024 is zero (R2 read): the fixture's marker bytes there are replaced
        img.write(bytes(512))
    print("unit 2: both key-record sectors hold the stash's record (sha256 %s); LBA 1024 zero" % hashlib.sha256(rec).hexdigest()[:16])
    r.fresh()
    with open(r.img, "r+b") as img:
        img.seek(AB_OFF)
        s0 = ab_sector(img.read(512), [(15, 0, 1), (14, 0, 1)])
        img.seek(AB_OFF)
        img.write(s0)
    res, _ = run([a.uc_pxx_inst, "plan"] + r.common(), os.path.join(r.w, "plan-s0.log"))
    fails = [l.strip() for l in res.stdout.splitlines() if "FAIL" in l or "refused" in l.lower()]
    if res.returncode != 1:
        raise SystemExit("FAIL: unit 2's baseline S0 was not refused (exit %d)" % res.returncode)
    print("unit 2, S0 (A{15,0,1} B{14,0,1}, A runs): the userdebug-first plan refuses: %s" % "; ".join(fails[:6]))
    c = r.common()
    i = c.index("--pre-ab")
    c = c[:i] + c[i + 2:]  # the direct path takes no pre-entry sector
    res, _ = run([a.uc_pxx_inst, "plan", "--direct", "--stash", a.stash, "--test-review-slots", "ab"] + c,
                 os.path.join(r.w, "plan-s0-direct.log"))
    fails = [l.strip() for l in res.stdout.splitlines() if "FAIL" in l or "refused" in l.lower()]
    if res.returncode != 1:
        raise SystemExit("FAIL: unit 2's baseline S0 was not refused by the direct planner (exit %d)" % res.returncode)
    print("unit 2, S0: the direct plan refuses: %s" % "; ".join(fails[:6]))
    run(CHECK + ["hash", r.pristine, os.path.join(r.w, "before.json")], None, 0)


def main():
    ap = argparse.ArgumentParser()
    ml = any(("--shape=" + v) in sys.argv or any(x == "--shape" and y == v for x, y in zip(sys.argv, sys.argv[1:])) for v in ("ml", "direct"))
    for n in ("uc-pxx-inst", "bootctl", "fw", "layout", "work", "mainline-boot", "mainline-system"):
        ap.add_argument("--" + n, required=not (ml and n in ("fw", "mainline-boot", "mainline-system")))
    # The payload is --fw (any firmware with an installable manifest). The two unpacks give the slot
    # contents: --keeper-fw the boot chain of the mainline keeper (uboot, trust, dtbo, vbmeta: 1.0.0.94
    # userdebug on unit 2), --target-fw what the target held before. The old names stay.
    ap.add_argument("--keeper-fw", "--userdebug", dest="userdebug", required=True)
    ap.add_argument("--target-fw", "--stock", dest="stock", required=True)
    ap.add_argument("--bootctl-exec", default="")
    ap.add_argument("--user-key-libs", default="", help="comma list: the key source a user would give after a failed key search")
    ap.add_argument("--shape", choices=["fwd", "rev", "ml", "direct"], required=True)
    ap.add_argument("--b-fw", choices=["stock", "userdebug"], default="stock",
                    help="--shape direct: slot B holds the --target-fw firmware (factory) or the --keeper-fw one (the page's userdebug)")
    ap.add_argument("--mainline-bundle", help="--shape ml: the bundle folder (DIR/uc-pxx-mainline or the folder itself)")
    ap.add_argument("--stash-fw", help="--shape ml: make the stash here first: uc-pxx-inst mainline-stash --image (the made image) "
                    "--fw THIS.bin --out WORK/stash (the key from the image's own systems); then --stash is that folder")
    ap.add_argument("--stash", help="--shape ml: the stash folder of the first drive phase (uc-pxx-inst mainline-stash --image IMG "
                    "--fw THE-1.0.0.94-USERDEBUG.bin --keys KEYS --out DIR): the key record (task 2)")
    ap.add_argument("--keep-image", help="copy (sparse) the image of the full run to this path (the start of a step-2 dry run)")
    ap.add_argument("--unit2", action="store_true",
                    help="--shape ml on unit 2's key state (MAINLINE-RC-2026-11-11): both key-record sectors hold the pinned record "
                         "(written here from the stash of --stash-fw), the plan must say kept (no keep-key step); first the RULES baseline S0 "
                         "A{15,0,1} B{14,0,1} (A runs) must refuse, then the state after the flow's switch to B runs the install")
    ap.add_argument("--cuts", action="store_true")
    ap.add_argument("--kills", action="store_true")
    a = ap.parse_args()
    if a.shape in ("ml", "direct"):
        if not a.mainline_bundle or bool(a.stash) == bool(a.stash_fw):
            raise SystemExit("dryrun-real.py: --shape ml/direct needs --mainline-bundle and one of --stash, --stash-fw")
        if a.stash_fw:
            a.stash = os.path.join(os.path.abspath(a.work), "stash")
        b = os.path.abspath(a.mainline_bundle)
        a.mainline_bundle = b if os.path.basename(b) == "uc-pxx-mainline" else os.path.join(b, "uc-pxx-mainline")
    r = Rig(a)
    t = time.time()
    r.make()
    if a.shape in ("ml", "direct") and a.stash_fw:
        if os.path.exists(a.stash):
            shutil.rmtree(a.stash)
        run([a.uc_pxx_inst, "mainline-stash", "--image", r.pristine, "--fw", a.stash_fw, "--out", a.stash],
            os.path.join(r.w, "stash.log"), 0)
        print("stash: %s (the key from the image's systems; the record sha256 is in stash.json)" % a.stash)
    if a.unit2:
        unit2_setup(r)
    r.full()
    if a.keep_image:
        subprocess.run(["cp", "--sparse=always", r.img, a.keep_image], check=True)
        print("the image of the full run: %s" % a.keep_image)
    if a.cuts:
        r.cuts()
    if a.kills:
        r.kills()
    for p in (r.img, r.pristine):
        if os.path.exists(p):
            os.remove(p)
    print("RESULT: %s: all checks passed (%.0f s)" % (a.shape, time.time() - t))
    return 0


if __name__ == "__main__":
    sys.exit(main())
