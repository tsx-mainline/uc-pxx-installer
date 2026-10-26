#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""The device mode of p10inst against real block devices: loop devices on an
image file, never a disk. Run it as root on a build host, never on a unit.

    devmode-loop.py --p10inst DEVTEST_BIN --bootctl BC --fw FW.bin --layout LAYOUT --work DIR
                    --shape fwd|rev --userdebug UNPACK --stock UNPACK
                    --mainline-boot IMG --mainline-system IMG [--kernel] [--refusals] [--kills] [--busy]

DEVTEST_BIN is a devtest build of p10inst (go build -tags install,devtest)
for this host. BC is the reviewed uc-pxx-bootctl-inst (aarch64, sha256
5d83215b...): the device mode runs it with chroot into the test root, through
binfmt_misc qemu-aarch64 with the F flag. The image is the fixture of
ci/dryrun-real.py (the unit's layout, a mainline keeper, a stock target).

For each run the script attaches the image with losetup -P, and builds a test
root: dev/mmcblk0 and dev/mmcblk0pN are block nodes (mknod) of the loop device
and its partitions, sys/ is a fake sysfs of a UC-P10 eMMC whose partition
names, starts, sizes and device numbers come from the kernel's sysfs of the
loop device, proc/ and etc/ are those of the installer RAM image. Then it runs
`p10inst install --device /dev/mmcblk0 --test-root ROOT`.

  --kernel    the kernel behaviour the device mode relies on, on a small
              scratch loop device: the read-only flag of the disk applies to
              every partition, the flag of a partition only to it; fsync of a
              partition node sends a flush to the disk (the flush counter in
              /sys/block/loopN/stat); O_EXCL fails on a partition in use.
  --refusals  wrong devices and wrong inputs on the real loop devices: nothing
              may change (the whole image is hashed before and after).
  (always)    a full install, checked with ci/dryrun-check.py compare; the
              kernel read-only flags are on again at the end; a second
              install without --keeper is refused.
  --kills     SIGKILL (--test-kill-after N) at the step boundaries and inside
              the big writes; after each, ci/dryrun-check.py cut, then the
              device mode again, which must complete.
  --busy      a target partition held open with O_EXCL by another process:
              the install stops with the target disabled, then completes.
"""
import argparse
import hashlib
import importlib.util
import json
import os
import subprocess
import sys
import time

sys.dont_write_bytecode = True  # it runs as root: leave no root-owned __pycache__ in the tree
HERE = os.path.dirname(os.path.abspath(__file__))
CHECK = [sys.executable, "-I", os.path.join(HERE, "dryrun-check.py")]
MODEL = "Crestron UC-P10"
HOST = "fe330000.mmc"
BOOTCTL_SHA = "5d83215bb5cab5b5f68c6db6500af8bea91180338b67e46850c266afb34ee400"
CMDLINE = "console=ttyS2,115200n8 loglevel=3 panic=30 rdinit=/init p10inst.ram=1"

spec = importlib.util.spec_from_file_location("dryrun_real", os.path.join(HERE, "dryrun-real.py"))
dryrun_real = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dryrun_real)


def sh(*cmd, check=True):
    r = subprocess.run(list(cmd), capture_output=True, text=True)
    if check and r.returncode != 0:
        raise SystemExit("FAIL: %s: %s" % (" ".join(cmd), r.stderr.strip()))
    return r.stdout.strip()


def put(path, data, mode=0o644):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "wb" if isinstance(data, bytes) else "w") as f:
        f.write(data)
    os.chmod(path, mode)


def read(p):
    with open(p) as f:
        return f.read().strip()


class Loop:
    """One image attached with losetup -P. Detach only this one."""

    def __init__(self, img, nparts):
        self.dev = sh("losetup", "--show", "-f", "-P", img)
        self.name = os.path.basename(self.dev)
        t = time.time()
        stable = 0
        while stable < 3:  # three reads in a row with every partition complete
            stable = stable + 1 if self.complete(nparts) else 0
            if time.time() - t > 30:
                self.close()
                raise SystemExit("FAIL: %s: the partitions did not appear" % self.dev)
            time.sleep(0.2)

    def complete(self, nparts):
        ps = self.parts()
        return len(ps) == nparts and all(os.path.exists("/sys/class/block/%s/%s" % (p, f)) and
                                         os.path.exists("/dev/" + p)
                                         for p in ps for f in ("uevent", "dev", "start", "size"))

    def parts(self):
        d = "/sys/class/block/" + self.name
        try:
            names = os.listdir(d)
        except FileNotFoundError:
            return []
        return sorted((p for p in names if p.startswith(self.name + "p")), key=lambda p: int(p.split("p")[-1]))

    def ro(self):
        out = {self.name: read("/sys/class/block/%s/ro" % self.name)}
        for p in self.parts():
            out[p] = read("/sys/class/block/%s/ro" % p)
        return out

    def flushes(self):
        return int(read("/sys/block/%s/stat" % self.name).split()[15])

    def close(self):
        if self.dev:
            sh("losetup", "-d", self.dev)
            self.dev = None
            t = time.time()
            while self.parts():  # the kernel removes the partitions after the detach
                if time.time() - t > 20:
                    raise SystemExit("FAIL: %s: the partitions stay after the detach" % self.name)
                time.sleep(0.1)


def build_root(root, loop, bootctl, o=None):
    """A test root for the device mode, on the real loop devices."""
    o = o or {}
    sh("rm", "-rf", root)
    card = "sys/devices/platform/%s/mmc_host/mmc0/mmc0:0001" % o.get("host", HOST)
    blk = card + "/block/mmcblk0"
    R = lambda p: os.path.join(root, p.lstrip("/"))
    ls = "/sys/class/block/" + loop.name
    put(R(card + "/type"), o.get("type", "MMC") + "\n")
    put(R(card + "/cid"), "150100424a54443452068c2f2e01d500\n")
    put(R(blk + "/dev"), read(ls + "/dev") + "\n")
    put(R(blk + "/size"), read(ls + "/size") + "\n")
    put(R(blk + "/queue/logical_block_size"), read(ls + "/queue/logical_block_size") + "\n")
    os.makedirs(R(blk + "/holders"))
    os.symlink("../../../mmc0:0001", R(blk + "/device"))
    os.makedirs(R("sys/class/block"))
    os.symlink("../../../" + blk, R("sys/class/block/mmcblk0"))
    os.makedirs(R("dev"))
    ma, mi = read(ls + "/dev").split(":")
    os.mknod(R("dev/mmcblk0"), 0o600 | 0o060000, os.makedev(int(ma), int(mi)))
    for p in loop.parts():
        n = int(p.split("p")[-1])
        dev = "mmcblk0p%d" % n
        ps = "/sys/class/block/" + p
        ue = dict(l.split("=", 1) for l in read(ps + "/uevent").splitlines() if "=" in l)
        name = ue.get("PARTNAME", "")
        pd = R(blk + "/" + dev)
        put(pd + "/uevent", "MAJOR=179\nMINOR=%d\nDEVNAME=%s\nDEVTYPE=partition\nPARTN=%d\nPARTNAME=%s\n" % (n, dev, n, name))
        put(pd + "/partition", "%d\n" % n)
        for k in ("start", "size", "dev"):
            put(pd + "/" + k, read(ps + "/" + k) + "\n")
        os.makedirs(pd + "/holders")
        os.symlink("../../../" + blk + "/" + dev, R("sys/class/block/" + dev))
        pma, pmi = read(ps + "/dev").split(":")
        os.mknod(R("dev/" + dev), 0o600 | 0o060000, os.makedev(int(pma), int(pmi)))
    put(R("proc/device-tree/model"), (o.get("model", MODEL) + "\0").encode())
    put(R("proc/device-tree/compatible"), b"crestron,uc-p10\0rockchip,rk3399\0")
    put(R("proc/cmdline"), o.get("cmdline", CMDLINE) + "\n")
    put(R("proc/mounts"), "rootfs / rootfs rw 0 0\nproc /proc proc rw 0 0\nrun /run tmpfs rw 0 0\n")
    if not o.get("no_marker"):
        put(R("etc/p10inst/ram-image"), "p10inst installer RAM image\n")
    with open(bootctl, "rb") as f:
        b = f.read()
    if o.get("bad_bootctl"):
        b += b"\0"
    put(R("usr/libexec/p10inst/uc-pxx-bootctl-inst"), b, 0o755)
    os.makedirs(R("run"))
    return root


def run(cmd, log, timeout=3600):
    t = time.time()
    with open(log, "w") as f:
        f.write("$ " + " ".join(cmd) + "\n")
        f.flush()
        r = subprocess.run(cmd, stdout=f, stderr=subprocess.STDOUT, timeout=timeout)
    with open(log) as f:
        out = f.read()
    return r.returncode, out, time.time() - t


class Dev:
    def __init__(self, a):
        self.a = a
        self.w = a.work
        self.rig = dryrun_real.Rig(a)  # the fixture of the dry runs
        # the test root lives on its own small tmpfs: /tmp is often mounted nodev
        self.devfs = os.path.join(self.w, "devfs")
        os.makedirs(self.devfs, exist_ok=True)
        if not os.path.ismount(self.devfs):
            sh("mount", "-t", "tmpfs", "-o", "size=64m,mode=0755,dev", "p10dev", self.devfs)
        self.root = os.path.join(self.devfs, "root")
        self.keeper = "a" if a.shape == "fwd" else "b"
        self.nparts = sum(1 for l in open(a.layout) if l.split() and l.split()[0].isdigit())
        self.lines = []

    def say(self, s):
        print(s, flush=True)
        self.lines.append(s)

    def install(self, tag, extra, opts=None, keeper=True):
        lp = Loop(self.rig.img, self.nparts)
        try:
            build_root(self.root, lp, self.a.bootctl, opts)
            cmd = [self.a.p10inst, "install", "--device", "/dev/mmcblk0", "--test-root", self.root, "--fw", self.a.fw,
                   "--prereqs-confirmed", "--yes", "--log", os.path.join(self.w, tag + ".jsonl")]
            if keeper:
                cmd += ["--keeper", self.keeper]
            rc, out, dt = run(cmd + extra, os.path.join(self.w, tag + ".log"))
            ro = lp.ro()
        finally:
            lp.close()
        return rc, out, dt, ro

    def image_sha(self):
        out = os.path.join(self.w, "now.json")
        dryrun_real.run(CHECK + ["hash", self.rig.img, out], None, 0)
        with open(out) as f:
            return hashlib.sha256(f.read().encode()).hexdigest()

    def plan(self):
        lp = Loop(self.rig.img, self.nparts)
        try:
            build_root(self.root, lp, self.a.bootctl)
            rc, out, dt = run([self.a.p10inst, "plan", "--device", "/dev/mmcblk0", "--test-root", self.root, "--fw",
                               self.a.fw, "--keeper", self.keeper, "--prereqs-confirmed",
                               "--json", os.path.join(self.w, "plan.json")], os.path.join(self.w, "plan.log"))
        finally:
            lp.close()
        if rc != 0:
            raise SystemExit("FAIL: plan --device: exit %d\n%s" % (rc, out[-3000:]))
        self.say("plan --device OK (%.0f s), id %s" % (dt, json.load(open(os.path.join(self.w, "plan.json")))["id"]))

    def full(self):
        self.rig.fresh()
        self.plan()
        rc, out, dt, ro = self.install("full", ["--plan", os.path.join(self.w, "plan.json")])
        if rc != 0 or "RESULT: install done" not in out:
            raise SystemExit("FAIL: full install: exit %d\n%s" % (rc, out[-3000:]))
        self.say("install --device OK (%.0f s)" % dt)
        bad = [k for k, v in ro.items() if v != "1"]
        self.say("kernel read-only flag after the install: %s" % ("on for all %d devices" % len(ro) if not bad else "OFF on %s" % bad))
        if bad:
            raise SystemExit("FAIL: read-only flags")
        self.rig.compare("full")
        rc, out, _, _ = self.install("again", [], keeper=False)
        if rc != 1 or "must be successful" not in out:
            raise SystemExit("FAIL: a second install without --keeper: exit %d\n%s" % (rc, out[-2000:]))
        self.say("a second install without --keeper: refused (the target boots next, it is not successful yet)")
        self.rig.compare("again")
        self.steps = [json.loads(l) for l in open(os.path.join(self.w, "full.jsonl")) if '"op"' in l and '"writes"' in l]

    def refusals(self):
        self.rig.fresh()
        before = self.image_sha()
        lp = Loop(self.rig.img, self.nparts)
        loopdev = lp.dev
        lp.close()
        cases = [
            ("--device /dev/loopN", {}, ["--device", loopdev], "must be exactly /dev/mmcblk0", True),
            ("an SD card", {"type": "SD"}, [], "an SD card", True),
            ("a loop device without the fake markers", {"host": "virtual"}, [], "not on the eMMC controller", True),
            ("another board", {"model": "Rockchip RK3399 EVB"}, [], "not a UC-P10", True),
            ("a booted mainline", {"cmdline": "root=/dev/mmcblk0p22 uc-pxx.id=ucpxx-00000000 p10inst.ram=1"}, [], "booted system", True),
            ("no RAM image marker", {"no_marker": True}, [], "no marker file", True),
            ("a bootctl that is not the reviewed binary", {"bad_bootctl": True}, [], "bootctl-binary", True),
        ]
        for i, (name, o, extra, want, root) in enumerate(cases):
            if extra:
                rc, out, _, _ = self.refuse_dev()
            else:
                rc, out, _, _ = self.install("refuse%d" % i, [], o)
            ok = rc == 1 and want in out
            self.say("refusal: %-42s %s" % (name, "refused" if ok else "NOT REFUSED (exit %d)" % rc))
            if not ok:
                raise SystemExit("FAIL: %s\n%s" % (name, out[-2000:]))
        # the plan id: an edited plan file
        p = json.load(open(os.path.join(self.w, "plan.json")))
        p["keeper_why"] = "edited"
        ef = os.path.join(self.w, "plan-edited.json")
        json.dump(p, open(ef, "w"))
        rc, out, _, _ = self.install("refuse-plan", ["--plan", ef])
        ok = rc == 1 and "does not match its id" in out
        self.say("refusal: %-42s %s" % ("an edited plan file (--plan)", "refused" if ok else "NOT REFUSED (exit %d)" % rc))
        if not ok:
            raise SystemExit("FAIL: edited plan\n%s" % out[-2000:])
        p = json.load(open(os.path.join(self.w, "plan.json")))
        p["id"] = "0" * 64
        json.dump(p, open(ef, "w"))
        rc, out, _, _ = self.install("refuse-plan2", ["--plan", ef])
        ok = rc == 1 and "does not match its id" in out
        self.say("refusal: %-42s %s" % ("a plan file with another id (--plan)", "refused" if ok else "NOT REFUSED (exit %d)" % rc))
        if not ok:
            raise SystemExit("FAIL: plan id\n%s" % out[-2000:])
        # the real host, without a test root: not the RAM image
        rc, out, _ = run([self.a.p10inst, "install", "--device", "/dev/mmcblk0", "--fw", self.a.fw, "--yes"],
                         os.path.join(self.w, "refuse-host.log"))
        ok = rc == 1 and "not the installer RAM image" in out
        self.say("refusal: %-42s %s" % ("this build host itself (no --test-root)", "refused" if ok else "NOT REFUSED (exit %d)" % rc))
        if not ok:
            raise SystemExit("FAIL: host\n%s" % out[-2000:])
        after = self.image_sha()
        self.say("refusals: the whole image is unchanged (%s)" % ("yes" if after == before else "NO"))
        if after != before:
            raise SystemExit("FAIL: a refusal changed the image")

    def refuse_dev(self):
        lp = Loop(self.rig.img, self.nparts)
        try:
            build_root(self.root, lp, self.a.bootctl)
            cmd = [self.a.p10inst, "install", "--device", lp.dev, "--test-root", self.root, "--fw", self.a.fw,
                   "--keeper", self.keeper, "--prereqs-confirmed", "--yes"]
            rc, out, dt = run(cmd, os.path.join(self.w, "refuse-dev.log"))
        finally:
            lp.close()
        return rc, out, dt, None

    def cut_check(self, tag):
        st = self.rig.check_cut(tag)
        return st

    def resume(self, tag, st):
        if st.get("boots") == "target":
            rc, out, _, _ = self.install(tag + "-resume", [], keeper=False)
            if rc != 1:
                raise SystemExit("FAIL: %s: the target boots next, but the install ran again" % tag)
            self.rig.compare(tag)
            return "install without --keeper refused (target boots next), state complete"
        rc, out, dt, ro = self.install(tag + "-resume", [])
        if rc != 0:
            raise SystemExit("FAIL: %s: resume: exit %d\n%s" % (tag, rc, out[-3000:]))
        self.rig.compare(tag)
        return "resumed, install complete (%.0f s)" % dt

    def kills(self):
        final = max(s["writes"] for s in self.steps)
        points = sorted({s["writes"] for s in self.steps if 0 < s["writes"] < final} | {final})
        prev = {s["step"]: s["writes"] for s in self.steps}
        for s in self.steps:
            if s["op"] == "write" and s["step"] - 1 in prev and s["writes"] - prev[s["step"] - 1] > 100:
                points.append((prev[s["step"] - 1] + s["writes"]) // 2)
        points = sorted(set(points))
        self.say("kill points (SIGKILL right after primitive write N): %s of %d" % (points, final))
        for n in points:
            tag = "kill%d" % n
            self.rig.fresh()
            rc, out, _, ro = self.install(tag, ["--test-kill-after", str(n)])
            if rc != -9:
                raise SystemExit("FAIL: %s: exit %d, want SIGKILL\n%s" % (tag, rc, out[-2000:]))
            st = self.cut_check(tag)
            how = self.resume(tag, st)
            off = sum(1 for v in ro.values() if v != "1")
            self.say("  %-9s A%s B%s next %s target %-9s (ro flag off on %d devices) -> %s" % (
                tag, tuple(st["ab"]["a"]), tuple(st["ab"]["b"]), st["ab"]["next"], st["target"], off, how))

    def busy(self):
        self.rig.fresh()
        tgt = "b" if self.keeper == "a" else "a"
        lp = Loop(self.rig.img, self.nparts)
        try:
            build_root(self.root, lp, self.a.bootctl)
            idx = None
            for p in lp.parts():
                if "PARTNAME=system_" + tgt in read("/sys/class/block/%s/uevent" % p):
                    idx = p
            fd = os.open("/dev/" + idx, os.O_RDONLY | os.O_EXCL)
            try:
                rc, out, _ = run([self.a.p10inst, "install", "--device", "/dev/mmcblk0", "--test-root", self.root, "--fw",
                                  self.a.fw, "--keeper", self.keeper, "--prereqs-confirmed", "--yes"],
                                 os.path.join(self.w, "busy.log"))
            finally:
                os.close(fd)
        finally:
            lp.close()
        ok = rc == 1 and "open " in out and "busy" in out and "safe to power off" in out
        self.say("busy: system_%s held with O_EXCL: %s" % (tgt, "the install stopped, target disabled, safe" if ok else "NOT AS EXPECTED (exit %d)" % rc))
        if not ok:
            raise SystemExit("FAIL: busy\n%s" % out[-3000:])
        st = self.cut_check("busy")
        self.say("  busy      A%s B%s next %s target %s -> %s" % (tuple(st["ab"]["a"]), tuple(st["ab"]["b"]), st["ab"]["next"],
                                                              st["target"], self.resume("busy", st)))


def umount(d):
    if os.path.ismount(d):
        sh("umount", d)


def kernel_tests(work, p10inst):
    """The kernel behaviour the device mode relies on, on a small scratch loop device."""
    lay = os.path.join(work, "small-layout.txt")
    with open(lay, "w") as f:
        f.write("disk 80000\n5 misc 49152 8192\n13 boot_a 58496 512\n14 boot_b 59008 512\n")
    img = os.path.join(work, "small.img")
    if os.path.exists(img):
        os.remove(img)
    sh(p10inst, "fake-emmc", "--layout", lay, "-o", img, "--ab", "a=15,0,1,b=14,0,1")
    lp = Loop(img, 3)
    res = []
    try:
        p = {read("/sys/class/block/%s/uevent" % q).split("PARTNAME=")[1].split()[0]: q for q in lp.parts()}
        node = "/dev/" + p["boot_b"]

        def setro(dev, v):
            sh("blockdev", "--setro" if v else "--setrw", dev)

        def try_write():
            fd = os.open(node, os.O_WRONLY)
            try:
                os.pwrite(fd, b"\0" * 512, 0)
                return "ok"
            except OSError as e:
                return e.strerror
            finally:
                os.close(fd)

        setro(lp.dev, True)
        setro(node, False)
        r1 = try_write()
        setro(lp.dev, False)
        setro(node, True)
        r2 = try_write()
        setro(node, False)
        r3 = try_write()
        res.append(("disk ro, partition rw: a partition write", r1, r1 != "ok"))
        res.append(("disk rw, partition ro: a partition write", r2, r2 != "ok"))
        res.append(("disk rw, partition rw: a partition write", r3, r3 == "ok"))
        f0 = lp.flushes()
        fd = os.open(node, os.O_RDONLY)
        os.fsync(fd)
        os.close(fd)
        f1 = lp.flushes()
        res.append(("fsync of a partition node: flushes on the disk", "%d -> %d" % (f0, f1), f1 > f0))
        fd = os.open(node, os.O_RDONLY | os.O_EXCL)
        try:
            fd2 = os.open(node, os.O_RDWR | os.O_EXCL)
            os.close(fd2)
            r4 = "opened"
        except OSError as e:
            r4 = e.strerror
        os.close(fd)
        res.append(("O_EXCL open of a partition held with O_EXCL", r4, r4 != "opened"))
    finally:
        lp.close()
        os.remove(img)
    ok = True
    for what, got, good in res:
        print("kernel %s: %-55s %s" % ("OK  " if good else "FAIL", what, got), flush=True)
        ok = ok and good
    print("kernel: %s" % sh("uname", "-r"), flush=True)
    if not ok:
        raise SystemExit("FAIL: kernel behaviour")


def main():
    ap = argparse.ArgumentParser()
    for n in ("p10inst", "bootctl", "fw", "layout", "work", "userdebug", "stock", "mainline-boot", "mainline-system"):
        ap.add_argument("--" + n, required=True)
    ap.add_argument("--shape", choices=["fwd", "rev"], required=True)
    for n in ("kernel", "refusals", "kills", "busy"):
        ap.add_argument("--" + n, action="store_true")
    a = ap.parse_args()
    if os.geteuid() != 0:
        raise SystemExit("devmode-loop.py: needs root (losetup, mknod, chroot)")
    with open(a.bootctl, "rb") as f:
        if hashlib.sha256(f.read()).hexdigest() != BOOTCTL_SHA:
            raise SystemExit("devmode-loop.py: %s is not the reviewed uc-pxx-bootctl-inst" % a.bootctl)
    a.bootctl_exec, a.user_key_libs = "", ""
    os.makedirs(a.work, exist_ok=True)
    t = time.time()
    if a.kernel:
        kernel_tests(a.work, a.p10inst)
    d = Dev(a)
    try:
        d.rig.make()
        d.full()
        if a.refusals:
            d.refusals()
        if a.kills:
            d.kills()
        if a.busy:
            d.busy()
    finally:
        umount(d.devfs)
        for p in (d.rig.img, d.rig.pristine):
            if os.path.exists(p):
                os.remove(p)
    print("RESULT: %s: all checks passed (%.0f s)" % (a.shape, time.time() - t))
    return 0


if __name__ == "__main__":
    sys.exit(main())
