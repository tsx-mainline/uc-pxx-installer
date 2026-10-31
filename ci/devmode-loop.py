#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""The device mode of p10inst against real block devices: loop devices on an
image file, never a disk. Run it as root on a build host, never on a unit.

    devmode-loop.py --p10inst DEVTEST_BIN --bootctl BC --fw FW.bin --layout LAYOUT --work DIR
                    --shape fwd|rev --keeper-fw UNPACK --target-fw UNPACK
                    --mainline-boot IMG --mainline-system IMG [--kernel] [--refusals] [--kills] [--busy]
                    [--signals] [--fw-drive DRIVE.img [--drive-checks]]

DEVTEST_BIN is a devtest build of p10inst (go build -tags install,devtest)
for this host. BC is the pinned uc-pxx-bootctl-inst (aarch64, sha256
20aa6bc5...): the device mode runs it with chroot into the test root, through
binfmt_misc qemu-aarch64 with the F flag. The image is the fixture of
ci/dryrun-real.py (the unit's layout, a mainline keeper, a stock target).

For each run the script attaches the image with losetup -P, and builds a test
root: dev/mmcblk0 and dev/mmcblk0pN are block nodes (mknod) of the loop device
and its partitions, sys/ is a fake sysfs of a UC-P10 eMMC whose partition
names, starts, sizes and device numbers come from the kernel's sysfs of the
loop device, proc/ and etc/ are those of the installer RAM image. Then it runs
`p10inst install --device /dev/mmcblk0 --test-root ROOT`.

The FUA guard (DEVMODE-R4.md section 12): p10inst and uc-pxx-bootctl-inst start
an A/B write only when /sys/class/block/mmcblk0/queue/fua is 1. A loop device
reports 0 (the loop driver has no FUA), so the fake sysfs of the test root says
1, and write_cache "write back" (the guard needs both): the guard is a fact of the unit's queue (measured 1 on unit 2), not of the
stand-in. This changes only the test root: a release build has no --test-root,
and in the RAM image bootctl runs without chroot and reads the real /sys. The
real value of the loop device is printed once; --refusals also runs the guard
with queue/fua 0 and with write_cache "write through" (refused, nothing written). proc/ also holds a fixed
kernel boot id (proc/sys/kernel/random/boot_id), which every plan records.

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
  --signals   p10inst on a pty, as on the USB serial shell: while bootctl runs
              disable, ^C to the foreground group and SIGTERM; while it runs
              arm-trying, the pty is closed (hangup). The install must still
              complete, bootctl must never be killed, and the transcript keeps
              every line. Only a bootctl that this p10inst started counts. Do
              not run go test sweeps or a second loop run at the same time:
              they slow the timing of the kill and signal points.
After every kill the resume must report the read-only flags that the kill
left off, and set them again.

  --fw-drive  DRIVE.img is a FAT drive file (as the P10-INSTALL drive) whose
              one .bin is the firmware FW.bin. Every device-mode run then
              reads the firmware from the drive (--fw-drive DRIVE.img) in
              place of --fw, and the test root has the gadget lun file of the
              drive, empty (the host does not have the drive). The full install
              must report the drive rule held. Only the refusal "this build
              host itself" still uses --fw (and once more --fw-drive).
  --drive-checks  (with --fw-drive) the drive rule on the loop devices: the
              host has the drive, another identity, a symlink, --fw with
              --fw-drive; and a change of the drive (the file touched, the lun
              attached again) while the install pauses after the disable and
              inside the data writes (--test-pause-after): the install must stop
              before the next step with the target disabled and never armed,
              then complete on a resume; a change after the arm is only
              reported.
"""
import argparse
import fcntl
import hashlib
import importlib.util
import json
import os
import pty
import signal
import subprocess
import termios
import threading
import sys
import time

sys.dont_write_bytecode = True
# The lun of the P10-INSTALL drive in the test root (internal/drivestate LunFile).
LUN = "sys/kernel/config/usb_gadget/p10inst/functions/mass_storage.usb0/lun.0/file"
DRIVE = None  # --fw-drive: the drive file  # it runs as root: leave no root-owned __pycache__ in the tree
HERE = os.path.dirname(os.path.abspath(__file__))
CHECK = [sys.executable, "-I", os.path.join(HERE, "dryrun-check.py")]
MODEL = "Crestron UC-P10"
HOST = "fe330000.mmc"
BOOTCTL_SHA = "20aa6bc5d2146faace461eec0b50f741c681b3c58827788b9b806d9444b00203"
# The kernel command line of a real installer RAM image (unit 2, 2026-10-26): the
# bootargs of its device tree, p10inst.ram=1 (image/build.sh) and the token word
# that kernel/mkimage.sh adds (uc-pxx.id=p10inst-XXXXXX).
TOKEN = "uc-pxx.id=p10inst-4f3f5e"
OTHER_TOKEN = "uc-pxx.id=p10inst-00a9ff"  # the token of another RAM image
BOOT_ID = "6f0c2a1e-3b4d-4e5f-8a9b-0c1d2e3f4a5b"        # the kernel boot id of the test root
OTHER_BOOT_ID = "11111111-2222-4333-8444-555555555555"  # an earlier boot
CMDLINE = ("console=ttyS2,115200n8 earlycon=uart8250,mmio32,0xff1a0000 loglevel=7 clk_ignore_unused "
           "pd_ignore_unused fbcon=rotate:3 vt.global_cursor_default=0 initramfs_async=0 panic=30 rdinit=/init "
           "p10inst.ram=1 " + TOKEN)

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
        # as the RAM image's rcS does (p10inst readonly): every flag on
        for d in [self.dev] + ["/dev/" + q for q in self.parts()]:
            sh("blockdev", "--setro", d)

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
    # the FUA guard (see the top of this file): the unit's value, not the loop's
    put(R(blk + "/queue/fua"), o.get("fua", "1") + "\n")
    put(R(blk + "/queue/write_cache"), o.get("write_cache", "write back") + "\n")
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
    cl = o.get("cmdline", CMDLINE)  # str, or bytes for a line that is not ASCII
    put(R("proc/cmdline"), (cl if isinstance(cl, bytes) else cl.encode()) + b"\n")
    put(R("proc/mounts"), "rootfs / rootfs rw 0 0\nproc /proc proc rw 0 0\nrun /run tmpfs rw 0 0\n")
    put(R("proc/sys/kernel/random/boot_id"), o.get("boot_id", BOOT_ID) + "\n")
    if not o.get("no_marker"):
        put(R("etc/p10inst/ram-image"), "p10inst installer RAM image\n")
    with open(bootctl, "rb") as f:
        b = f.read()
    if o.get("bad_bootctl"):
        b += b"\0"
    put(R("usr/libexec/p10inst/uc-pxx-bootctl-inst"), b, 0o755)
    os.makedirs(R("run"))
    if DRIVE:
        put(R(LUN), o.get("lun", "") + "\n")  # empty: the host does not have the drive
    return root


def fwargs(a):
    """The firmware arguments of a device-mode run: the drive, or the file."""
    return ["--fw-drive", a.fw_drive] if a.fw_drive else ["--fw", a.fw]


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

    def install(self, tag, extra, opts=None, keeper=True, lp=None):
        own = lp is None
        if own:
            lp = Loop(self.rig.img, self.nparts)
        try:
            build_root(self.root, lp, self.a.bootctl, opts)
            cmd = [self.a.p10inst, "install", "--device", "/dev/mmcblk0", "--test-root", self.root] + fwargs(self.a) + [
                   "--prereqs-confirmed", "--yes", "--log", os.path.join(self.w, tag + ".jsonl")]
            if keeper:
                cmd += ["--keeper", self.keeper]
            rc, out, dt = run(cmd + extra, os.path.join(self.w, tag + ".log"))
            ro = lp.ro()
        finally:
            if own:
                lp.close()
        return rc, out, dt, ro

    def image_sha(self):
        out = os.path.join(self.w, "now.json")
        dryrun_real.run(CHECK + ["hash", self.rig.img, out], None, 0)
        with open(out) as f:
            return hashlib.sha256(f.read().encode()).hexdigest()

    def plan(self, opts=None, name="plan"):
        lp = Loop(self.rig.img, self.nparts)
        try:
            build_root(self.root, lp, self.a.bootctl, opts)
            self.say("loop %s: its own queue/fua is %s; the test root's fake sysfs says %s (the FUA guard)" %
                     (lp.name, read("/sys/class/block/%s/queue/fua" % lp.name), (opts or {}).get("fua", "1")))
            rc, out, dt = run([self.a.p10inst, "plan", "--device", "/dev/mmcblk0", "--test-root", self.root] + fwargs(self.a) +
                              ["--keeper", self.keeper, "--prereqs-confirmed",
                               "--json", os.path.join(self.w, name + ".json")], os.path.join(self.w, name + ".log"))
        finally:
            lp.close()
        if rc != 0:
            raise SystemExit("FAIL: plan --device: exit %d\n%s" % (rc, out[-3000:]))
        p = json.load(open(os.path.join(self.w, name + ".json")))
        self.say("plan --device OK (%.0f s), id %s, RAM image token %s" % (dt, p["id"], p.get("ram_image", {}).get("token")))
        return p

    def full(self):
        self.rig.fresh()
        self.plan()
        rc, out, dt, ro = self.install("full", ["--plan", os.path.join(self.w, "plan.json")])
        if rc != 0 or "RESULT: install done" not in out:
            raise SystemExit("FAIL: full install: exit %d\n%s" % (rc, out[-3000:]))
        self.say("install --device OK (%.0f s)" % dt)
        if DRIVE:
            ok = "drive: detached and unchanged from the first read to the end" in out and \
                "the firmware is the .bin on the P10-INSTALL drive file " + DRIVE in out
            self.say("the firmware from the drive %s, the drive rule held to the end: %s" % (DRIVE, "yes" if ok else "NO"))
            if not ok:
                raise SystemExit("FAIL: the drive rule is not reported\n%s" % out[-3000:])
        # the token of the RAM image: printed, in the plan, first in the log
        tok = TOKEN.split("=", 1)[1]
        p = json.load(open(os.path.join(self.w, "plan.json")))
        first = json.loads(open(os.path.join(self.w, "full.jsonl")).readline())
        ok = ("environment: the installer RAM image, token %s (" % tok) in out and p.get("ram_image") == {"token": tok, "boot_id": BOOT_ID} \
            and first.get("event") == "environment" and first.get("ram_token") == tok and first.get("plan") == p["id"]
        self.say("RAM image token %s: printed, in the plan and in the log: %s" % (tok, "yes" if ok else "NO"))
        if not ok:
            raise SystemExit("FAIL: the RAM image token\n%s\n%s" % (first, out[-2000:]))
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
            ("the real cmdline with a mainline token", {"cmdline": CMDLINE.replace(TOKEN, "uc-pxx.id=ucpxx-dfdcdfd8")}, [],
             "booted system", True),
            ("the real cmdline, token in uppercase", {"cmdline": CMDLINE.replace(TOKEN, TOKEN[:-6] + TOKEN[-6:].upper())}, [], "booted system", True),
            ("the real cmdline with two tokens", {"cmdline": CMDLINE + " uc-pxx.id=p10inst-000000"}, [], "second uc-pxx.id=", True),
            ("the real cmdline without p10inst.ram=1", {"cmdline": CMDLINE.replace(" p10inst.ram=1", "")}, [], "no p10inst.ram=1", True),
            ("the real cmdline without the token", {"cmdline": CMDLINE.replace(" " + TOKEN, "")}, [], "no token word", True),
            ("the real cmdline, root= behind 0xA0", {"cmdline": CMDLINE.encode() + b"\xa0root=/dev/mmcblk0p22"}, [], "byte 0xa0", True),
            ("the real cmdline, a quoted root=", {"cmdline": CMDLINE + ' "root=/dev/mmcblk0p22"'}, [], "'\"'", True),
            ("no RAM image marker", {"no_marker": True}, [], "no marker file", True),
            ("a bootctl that is not the reviewed binary", {"bad_bootctl": True}, [], "bootctl-binary", True),
            ("an eMMC queue without FUA (queue/fua 0)", {"fua": "0"}, [], "does not take FUA", True),
            ("an eMMC queue in write-through", {"write_cache": "write through"}, [], "write cache is not", True),
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
        # a plan made in another RAM image: the plans differ only in the token
        # (and so in the id), and --plan refuses the first one there
        p = json.load(open(os.path.join(self.w, "plan.json")))
        q = self.plan({"cmdline": CMDLINE.replace(TOKEN, OTHER_TOKEN)}, "plan-other")
        ok = p.get("ram_image") == {"token": TOKEN.split("=", 1)[1], "boot_id": BOOT_ID} and \
            q.get("ram_image") == {"token": OTHER_TOKEN.split("=", 1)[1], "boot_id": BOOT_ID} \
            and p["id"] != q["id"]
        for x in (p, q):
            x.pop("id", None)
            x.pop("ram_image", None)
        ok = ok and p == q
        self.say("a plan in another RAM image: only the token and the id differ: %s" % ("yes" if ok else "NO"))
        if not ok:
            raise SystemExit("FAIL: the plan in another RAM image differs in more than the token")
        rc, out, _, _ = self.install("refuse-plan3", ["--plan", os.path.join(self.w, "plan.json")],
                                     {"cmdline": CMDLINE.replace(TOKEN, OTHER_TOKEN)})
        ok = rc == 1 and "the plan now has id" in out
        self.say("refusal: %-42s %s" % ("the plan of another RAM image (--plan)", "refused" if ok else "NOT REFUSED (exit %d)" % rc))
        if not ok:
            raise SystemExit("FAIL: plan of another RAM image\n%s" % out[-2000:])
        # a plan made in an earlier boot of the same RAM image (another boot id)
        rc, out, _, _ = self.install("refuse-plan4", ["--plan", os.path.join(self.w, "plan.json")],
                                     {"boot_id": OTHER_BOOT_ID})
        ok = rc == 1 and "the plan now has id" in out
        self.say("refusal: %-42s %s" % ("the plan of an earlier boot (--plan)", "refused" if ok else "NOT REFUSED (exit %d)" % rc))
        if not ok:
            raise SystemExit("FAIL: plan of an earlier boot\n%s" % out[-2000:])
        # the real host, without a test root: not the RAM image
        rc, out, _ = run([self.a.p10inst, "install", "--device", "/dev/mmcblk0", "--fw", self.a.fw, "--yes"],
                         os.path.join(self.w, "refuse-host.log"))
        ok = rc == 1 and "not the installer RAM image" in out
        self.say("refusal: %-42s %s" % ("this build host itself (no --test-root)", "refused" if ok else "NOT REFUSED (exit %d)" % rc))
        if not ok:
            raise SystemExit("FAIL: host\n%s" % out[-2000:])
        if DRIVE:
            # with --fw-drive the drive rule runs first: the host has no gadget lun
            rc, out, _ = run([self.a.p10inst, "install", "--device", "/dev/mmcblk0", "--fw-drive", DRIVE, "--yes"],
                             os.path.join(self.w, "refuse-host-drive.log"))
            ok = rc == 1 and "drive: the drive state cannot be read" in out
            self.say("refusal: %-42s %s" % ("this build host itself, --fw-drive", "refused" if ok else "NOT REFUSED (exit %d)" % rc))
            if not ok:
                raise SystemExit("FAIL: host, drive\n%s" % out[-2000:])
        after = self.image_sha()
        self.say("refusals: the whole image is unchanged (%s)" % ("yes" if after == before else "NO"))
        if after != before:
            raise SystemExit("FAIL: a refusal changed the image")

    def refuse_dev(self):
        lp = Loop(self.rig.img, self.nparts)
        try:
            build_root(self.root, lp, self.a.bootctl)
            cmd = [self.a.p10inst, "install", "--device", lp.dev, "--test-root", self.root] + fwargs(self.a) + [
                   "--keeper", self.keeper, "--prereqs-confirmed", "--yes"]
            rc, out, dt = run(cmd, os.path.join(self.w, "refuse-dev.log"))
        finally:
            lp.close()
        return rc, out, dt, None

    def cut_check(self, tag):
        st = self.rig.check_cut(tag)
        return st

    def resume(self, tag, st, lp=None):
        if st.get("boots") == "target":
            rc, out, _, _ = self.install(tag + "-resume", [], keeper=False, lp=lp)
            if rc != 1:
                raise SystemExit("FAIL: %s: the target boots next, but the install ran again" % tag)
            self.rig.compare(tag)
            return "install without --keeper refused (target boots next), state complete"
        rc, out, dt, ro = self.install(tag + "-resume", [], lp=lp)
        if rc != 0:
            raise SystemExit("FAIL: %s: resume: exit %d\n%s" % (tag, rc, out[-3000:]))
        self.rig.compare(tag)
        self.last_resume = out
        return "resumed, install complete (%.0f s)" % dt

    def drive_checks(self):
        """The drive rule of --fw-drive on the loop devices (see --drive-checks)."""
        self.rig.fresh()
        before = self.image_sha()
        ident = os.stat(DRIVE)
        other = "size=%d,mtime=%d,ctime=%d,dev=%d,ino=%d" % (ident.st_size, ident.st_mtime_ns + 1, ident.st_ctime_ns,
                                                             ident.st_dev, ident.st_ino)
        link = os.path.join(self.w, "drive-link.img")
        if os.path.lexists(link):
            os.remove(link)
        os.symlink(DRIVE, link)
        cases = [
            ("the host has the drive (lun attached)", {"lun": DRIVE}, [], "attached to the computer", 1),
            ("--fw-drive-expect: another identity", {}, ["--fw-drive-expect", other], "the drive file changed", 1),
            ("--fw-drive-expect: a bad identity", {}, ["--fw-drive-expect", "size=1"], "drive identity", 1),
        ]
        for i, (name, o, extra, want, wrc) in enumerate(cases):
            rc, out, _, _ = self.install("drive-refuse%d" % i, extra, o)
            ok = rc == wrc and want in out and "bootctl" not in open(os.path.join(self.w, "drive-refuse%d.log" % i)).read().split("\n", 1)[1]
            self.say("drive refusal: %-42s %s" % (name, "refused" if ok else "NOT REFUSED (exit %d)" % rc))
            if not ok:
                raise SystemExit("FAIL: %s\n%s" % (name, out[-2000:]))
        # a symlink to the drive, and --fw with --fw-drive (usage, exit 2)
        lp = Loop(self.rig.img, self.nparts)
        try:
            build_root(self.root, lp, self.a.bootctl)
            base = [self.a.p10inst, "install", "--device", "/dev/mmcblk0", "--test-root", self.root, "--keeper", self.keeper,
                    "--prereqs-confirmed", "--yes"]
            rc, out, _ = run(base + ["--fw-drive", link], os.path.join(self.w, "drive-refuse-link.log"))
            ok = rc == 1 and "not a regular file" in out
            self.say("drive refusal: %-42s %s" % ("a symlink to the drive", "refused" if ok else "NOT REFUSED (exit %d)" % rc))
            if not ok:
                raise SystemExit("FAIL: symlink\n%s" % out[-2000:])
            rc, out, _ = run(base + ["--fw", self.a.fw, "--fw-drive", DRIVE], os.path.join(self.w, "drive-refuse-both.log"))
            ok = rc == 2
            self.say("drive refusal: %-42s %s" % ("--fw and --fw-drive", "usage (exit 2)" if ok else "NOT REFUSED (exit %d)" % rc))
            if not ok:
                raise SystemExit("FAIL: both\n%s" % out[-2000:])
        finally:
            lp.close()
        after = self.image_sha()
        self.say("drive refusals: the whole image is unchanged (%s)" % ("yes" if after == before else "NO"))
        if after != before:
            raise SystemExit("FAIL: a drive refusal changed the image")
        # changes while the install pauses after write N
        final = max(s["writes"] for s in self.steps)
        big = max((s for s in self.steps if s["op"] == "write"), key=lambda s: s["writes"])
        prev = max(s["writes"] for s in self.steps if s["step"] < big["step"])
        points = [1, (prev + big["writes"]) // 2]
        lun = os.path.join(self.root, LUN)
        for what, act, undo in [
            ("touched", lambda: os.utime(DRIVE), lambda: None),
            ("attached", lambda: put(lun, DRIVE + "\n"), lambda: put(lun, "\n")),
        ]:
            for n in points + [final]:
                tag = "drive-%s-%d" % (what, n)
                self.rig.fresh()
                lp = Loop(self.rig.img, self.nparts)
                try:
                    build_root(self.root, lp, self.a.bootctl)
                    log = os.path.join(self.w, tag + ".log")
                    cmd = [self.a.p10inst, "install", "--device", "/dev/mmcblk0", "--test-root", self.root] + fwargs(self.a) + [
                        "--keeper", self.keeper, "--prereqs-confirmed", "--yes", "--log", os.path.join(self.w, tag + ".jsonl"),
                        "--test-pause-after", str(n)]
                    with open(log, "w") as f:
                        f.write("$ " + " ".join(cmd) + "\n")
                        f.flush()
                        p = subprocess.Popen(cmd, stdout=f, stderr=subprocess.STDOUT)
                        t = time.time()
                        while "paused after write %d" % n not in open(log).read():
                            if p.poll() is not None or time.time() - t > 3600:
                                raise SystemExit("FAIL: %s: no pause (exit %s)\n%s" % (tag, p.poll(), open(log).read()[-2000:]))
                            time.sleep(0.05)
                        act()
                        rc = p.wait(timeout=3600)
                    out = open(log).read()
                    undo()
                    armed = any('"arm-trying"' in l for l in open(os.path.join(self.w, tag + ".jsonl")))
                    if n < final:
                        ok = rc == 1 and "drive rule" in out and "STOP before" in out and not armed
                        st = self.cut_check(tag)
                        ok = ok and st.get("boots") == "keeper" and st.get("target") == "disabled"
                        how = self.resume(tag, st, lp=lp)
                        self.say("  drive %-8s after write %-5d: exit %d, stopped before the next step, target %s, armed %s -> %s" % (
                            what, n, rc, st.get("target"), armed, how))
                    else:
                        ok = rc == 0 and "RESULT: install done" in out and "after the arm: the install does not read the drive" in out
                        self.rig.compare(tag)
                        self.say("  drive %-8s after write %-5d (the arm): exit %d, only reported, install complete" % (what, n, rc))
                    if not ok:
                        raise SystemExit("FAIL: %s: exit %d armed %s\n%s" % (tag, rc, armed, out[-3000:]))
                finally:
                    lp.close()

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
            # the killed run and its resume on the same loop device: the flags
            # that the kill left off are still off when the resume starts
            lp = Loop(self.rig.img, self.nparts)
            try:
                rc, out, _, ro = self.install(tag, ["--test-kill-after", str(n)], lp=lp)
                if rc != -9:
                    raise SystemExit("FAIL: %s: exit %d, want SIGKILL\n%s" % (tag, rc, out[-2000:]))
                st = self.cut_check(tag)
                self.last_resume = ""
                how = self.resume(tag, st, lp=lp)
            finally:
                lp.close()
            off = sum(1 for v in ro.values() if v != "1")
            if off and st.get("boots") != "target":
                want = "read-only flag was OFF on %d device(s)" % off
                if want not in self.last_resume:
                    raise SystemExit("FAIL: %s: the resume did not report the flags left off (%s)" % (tag, want))
                how += "; it reported and set the %d flags left off" % off
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
                rc, out, _ = run([self.a.p10inst, "install", "--device", "/dev/mmcblk0", "--test-root", self.root] +
                                 fwargs(self.a) + ["--keeper", self.keeper, "--prereqs-confirmed", "--yes"],
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


def parent_pid(pid):
    """The parent PID from /proc/PID/stat (the name in parentheses may hold any byte), or -1."""
    try:
        with open("/proc/%d/stat" % pid, "rb") as f:
            return int(f.read().rsplit(b")", 1)[1].split()[1])
    except (OSError, IndexError, ValueError):
        return -1


def descends_from(pid, ancestor):
    """True when ancestor is on the parent chain of pid."""
    for _ in range(4096):  # a chain is never this long; it ends at 1 or at a gone process
        pid = parent_pid(pid)
        if pid == ancestor:
            return True
        if pid <= 1:
            return False
    return False


def find_bootctl(word, ancestor):
    """PIDs of a running uc-pxx-bootctl-inst with this command word, started by the p10inst under test
    (ancestor). Only its own children count: a bootctl of another run on the host (a go test sweep, a
    second loop run) must not trigger the signals."""
    out = []
    for p in os.listdir("/proc"):
        if not p.isdigit():
            continue
        try:
            with open("/proc/%s/cmdline" % p, "rb") as f:
                c = f.read().split(b"\0")
        except OSError:
            continue
        if any(x.endswith(b"/uc-pxx-bootctl-inst") for x in c) and word.encode() in c and b"chroot" not in c[0] \
                and descends_from(int(p), ancestor):
            out.append(int(p))
    return out


def signals_run(dev):
    """The install on a pty with signals while bootctl runs (review F2)."""
    dev.rig.fresh()
    lp = Loop(dev.rig.img, dev.nparts)
    try:
        build_root(dev.root, lp, dev.a.bootctl)
        tr = os.path.join(dev.w, "signals.jsonl")
        cmd = [dev.a.p10inst, "install", "--device", "/dev/mmcblk0", "--test-root", dev.root] + fwargs(dev.a) + [
               "--keeper", dev.keeper, "--prereqs-confirmed", "--yes", "--log", tr]
        master, slave = pty.openpty()

        def ctty():
            os.setsid()
            fcntl.ioctl(0, termios.TIOCSCTTY, 0)

        p = subprocess.Popen(cmd, stdin=slave, stdout=slave, stderr=slave, preexec_fn=ctty, close_fds=True)
        os.close(slave)
        seen = []
        out = bytearray()
        done = threading.Event()

        def drain():
            while not done.is_set():
                try:
                    b = os.read(master, 65536)
                except OSError:
                    return
                if not b:
                    return
                out.extend(b)

        th = threading.Thread(target=drain, daemon=True)
        th.start()
        events = [("disable", "SIGINT to the foreground group and SIGTERM"), ("arm-trying", "hangup (the pty closed)")]
        bootctl_pids = []
        for word, what in events:
            t = time.time()
            pids = []
            while not pids and p.poll() is None and time.time() - t < 1800:
                pids = find_bootctl(word, p.pid)
                time.sleep(0.002)
            if not pids:
                raise SystemExit("FAIL: signals: never saw bootctl %s (p10inst exit %s)" % (word, p.poll()))
            bootctl_pids += pids
            if word == "disable":
                os.killpg(p.pid, signal.SIGINT)
                os.kill(p.pid, signal.SIGTERM)
            else:
                done.set()
                os.close(master)
            seen.append("%s while bootctl %s ran (pid %s)" % (what, word, pids))
        rc = p.wait(timeout=3600)
        done.set()
    finally:
        lp.close()
    with open(tr.replace(".jsonl", ".out")) as f:
        transcript = f.read()
    ok = rc == 0 and "RESULT: install done" in transcript and "13/13 check the A/B sector" in transcript
    log = [json.loads(l) for l in open(tr)]
    rcs = [l["rc"] for l in log if l.get("op") == "bootctl" and "rc" in l]  # the step's own line has no rc
    ok = ok and rcs == [0, 0]
    for s in seen:
        dev.say("signals: %s" % s)
    dev.say("signals: p10inst exit %d, bootctl write exits %s, transcript complete: %s" % (
        rc, rcs, "RESULT: install done" in transcript))
    if not ok:
        raise SystemExit("FAIL: signals\n" + transcript[-3000:] + out.decode(errors="replace")[-2000:])
    dev.rig.compare("signals")


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
    # only misc and two boot partitions: no others, so no made-up nvram store
    sh(p10inst, "fake-emmc", "--layout", lay, "-o", img, "--ab", "a=15,0,1,b=14,0,1", "--nvram", "none")
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
    for n in ("p10inst", "bootctl", "fw", "layout", "work", "mainline-boot", "mainline-system"):
        ap.add_argument("--" + n, required=True)
    # The payload is --fw (any firmware with an installable manifest). The two unpacks give the slot
    # contents: --keeper-fw the boot chain of the mainline keeper (uboot, trust, dtbo, vbmeta: 1.0.0.94
    # userdebug on unit 2), --target-fw what the target held before. The old names stay.
    ap.add_argument("--keeper-fw", "--userdebug", dest="userdebug", required=True)
    ap.add_argument("--target-fw", "--stock", dest="stock", required=True)
    ap.add_argument("--shape", choices=["fwd", "rev"], required=True)
    for n in ("kernel", "refusals", "kills", "busy", "signals", "drive-checks"):
        ap.add_argument("--" + n, action="store_true")
    ap.add_argument("--fw-drive", help="a FAT drive file whose one .bin is FW.bin: the device mode reads it from there")
    a = ap.parse_args()
    global DRIVE
    if a.fw_drive:
        a.fw_drive = os.path.abspath(a.fw_drive)
        if not os.path.isfile(a.fw_drive) or os.path.islink(a.fw_drive):
            raise SystemExit("devmode-loop.py: --fw-drive %s is not a regular file" % a.fw_drive)
        DRIVE = a.fw_drive
    elif a.drive_checks:
        raise SystemExit("devmode-loop.py: --drive-checks needs --fw-drive")
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
        if a.drive_checks:
            d.drive_checks()
        if a.kills:
            d.kills()
        if a.busy:
            d.busy()
        if a.signals:
            signals_run(d)
    finally:
        umount(d.devfs)
        for p in (d.rig.img, d.rig.pristine):
            if os.path.exists(p):
                os.remove(p)
    print("RESULT: %s: all checks passed (%.0f s)" % (a.shape, time.time() - t))
    return 0


if __name__ == "__main__":
    sys.exit(main())
