#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""Dry-run installs of a real firmware into a file-backed eMMC image.

Run it on a build host, never against a device. It needs the install build of
p10inst, uc-pxx-bootctl, the firmware and the unpacked images of the two
supported firmwares (for the slot contents), and a mainline boot image and
root file system for the keeper slot. None of these files is in the repo.

    dryrun-real.py --p10inst BIN --bootctl BC [--bootctl-exec RUNNER] --fw FW.bin
                   --layout LAYOUT --work DIR --shape fwd|rev
                   --keeper-fw UNPACK_DIR --target-fw UNPACK_DIR
                   --mainline-boot IMG --mainline-system IMG [--cuts] [--kills]
                   [--user-key-libs LIB,LIB]

Shapes (the end-state shape of the design, §5.10):
  fwd  slot A = mainline (keeper, successful prio 15, the --keeper-fw boot chain), slot B = the
       --target-fw firmware (for example stock 1.0.6.20) -> install the firmware --fw into B
  rev  slot B = mainline (keeper), slot A = the --target-fw firmware without trust -> install into A
The payload --fw may be any firmware whose embedded manifest is installable (1.0.0.94 userdebug,
1.0.6.20, ...): for 1.0.6.20 as payload, give for example --target-fw the 1.0.0.94 unpack.
What it checks, with ci/dryrun-check.py (its own GPT parser and A/B decoder):
  the full run: only the target's write ranges and the A/B sector change, the
  target holds the payload, the A/B state is the plan's; a second plan is
  refused (the target boots next).
  --cuts: a simulated power cut (p10inst --test-cut-after) at the start, at
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
        c = ["--image", self.img, "--fw", a.fw, "--bootctl", a.bootctl, "--prereqs-confirmed", "--hash-keeper",
             "--tmp", self.w]
        if a.bootctl_exec:
            c += ["--bootctl-exec", a.bootctl_exec]
        with open(self.img, "rb") as f:
            f.seek(AB_OFF)
            c += ["--pre-ab", f.read(32).hex()]
        return c

    def make(self):
        a = self.a
        ud, st = a.userdebug, a.stock
        u = lambda n: os.path.join(ud, n)
        s = lambda n: os.path.join(st, n)
        keep, tgt = ("a", "b") if a.shape == "fwd" else ("b", "a")
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
        for p in (self.pristine, self.img):
            if os.path.exists(p):
                os.remove(p)
        cmd = [a.p10inst, "fake-emmc", "--layout", a.layout, "-o", self.pristine, "--ab", ab, "--markers"]
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
        run(CHECK + ["hash", img or self.img, after], None, 0)
        r, _ = run(CHECK + ["compare", os.path.join(self.w, "before.json"), after, os.path.join(self.w, "plan.json")])
        say("  " + (r.stdout + r.stderr).strip().replace("\n", "\n  "))
        if r.returncode != 0:
            raise SystemExit("FAIL: compare after %s" % tag)
        os.remove(after)

    def full(self):
        self.fresh()
        a = self.a
        r, dt = run([a.p10inst, "plan"] + self.common() + ["--json", os.path.join(self.w, "plan.json")],
                    os.path.join(self.w, "plan.log"), 0)
        print("plan OK (%.0f s): %s" % (dt, [l for l in r.stdout.splitlines() if "id " in l][0].strip()))
        log = os.path.join(self.w, "install.jsonl")
        if os.path.exists(log):
            os.remove(log)
        r, dt = run([a.p10inst, "install", "--dry-run"] + self.common() + ["--plan", os.path.join(self.w, "plan.json"),
                    "--log", log], os.path.join(self.w, "install.log"), 0)
        print("install --dry-run OK (%.0f s): %s" % (dt, r.stdout.strip().splitlines()[-1]))
        self.compare("full")
        r, _ = run([a.p10inst, "plan"] + self.common(), os.path.join(self.w, "replan.log"))
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
            r, _ = run([a.p10inst, "plan"] + self.common())
            if r.returncode != 1:
                raise SystemExit("FAIL: %s: the target boots next but a new plan was accepted" % tag)
            self.compare(tag)
            return "plan refused (target boots next), state complete"
        log = os.path.join(self.w, "resume-%s.log" % tag)
        r, dt = run([a.p10inst, "install", "--dry-run"] + self.common(), log)
        how = "resumed"
        if r.returncode != 0 and "key search" in r.stdout + r.stderr:
            # FINDING: the key came only from the target's system, and the cut left it half written.
            # A user then has to give a key file; here the extracted libraries stand in for it.
            libs = []
            for l in a.user_key_libs.split(","):
                libs += ["--lib", l]
            r, dt = run([a.p10inst, "install", "--dry-run"] + self.common() + libs, log + ".keyfile", 0)
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
        bounds = sorted(set(b for b in bounds if b < final)) + [final - 1]
        print("cut points (primitive writes done before the cut): %s of %d" % (bounds, final))
        for torn, w in [(False, b) for b in bounds] + [(True, bounds[len(bounds) // 2])]:
            tag = "cut%d%s" % (w, "t" if torn else "")
            self.fresh()
            cmd = [self.a.p10inst, "install", "--dry-run"] + self.common() + ["--test-cut-after", str(w)]
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
        for pat, delay in points:
            tag = "kill-" + pat.split()[-1].rstrip("_")
            self.fresh()
            p = subprocess.Popen([self.a.p10inst, "install", "--dry-run"] + self.common(), stdout=subprocess.PIPE,
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


def main():
    ap = argparse.ArgumentParser()
    for n in ("p10inst", "bootctl", "fw", "layout", "work", "mainline-boot", "mainline-system"):
        ap.add_argument("--" + n, required=True)
    # The payload is --fw (any firmware with an installable manifest). The two unpacks give the slot
    # contents: --keeper-fw the boot chain of the mainline keeper (uboot, trust, dtbo, vbmeta: 1.0.0.94
    # userdebug on unit 2), --target-fw what the target held before. The old names stay.
    ap.add_argument("--keeper-fw", "--userdebug", dest="userdebug", required=True)
    ap.add_argument("--target-fw", "--stock", dest="stock", required=True)
    ap.add_argument("--bootctl-exec", default="")
    ap.add_argument("--user-key-libs", default="", help="comma list: the key source a user would give after a failed key search")
    ap.add_argument("--shape", choices=["fwd", "rev"], required=True)
    ap.add_argument("--cuts", action="store_true")
    ap.add_argument("--kills", action="store_true")
    a = ap.parse_args()
    r = Rig(a)
    t = time.time()
    r.make()
    r.full()
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
