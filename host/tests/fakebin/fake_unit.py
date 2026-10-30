# SPDX-License-Identifier: GPL-2.0-or-later
"""A fake UC-P10 for the p10-enter tests: fake rkdeveloptool, fastboot and ssh.

No real device is ever touched. The state lives in $FAKE_STATE:
  config.json   the test's settings (see CONFIG)
  mode          off | rockusb | maskrom | mainline | fastboot | android | booted
  sectors/LBA   512-byte sectors of the fake eMMC (missing = zeros)
  calls.log     one JSON line per event: {"prog", "argv", "ev": start|end|FORBIDDEN, "t"}
Any command that p10-enter must never send (a write that is not the BCB, a
write over LBA 49156, getvar all, a fastboot verb other than devices/getvar/
boot, an unknown ssh command) logs FORBIDDEN and exits 99.
"""
import hashlib
import json
import os
import sys
import time

AB_LBA, BCB_LBA = 49156, 49184
CMD = b"bootonce-bootloader"
CONFIG = {
    "rockusb_after": 0,       # ld calls before the rockusb device shows
    "several": False,         # ld lists two devices
    "fastboot_after": 2,      # fastboot devices calls before the device shows
    "slow_rl": 0, "slow_wl": 0, "slow_boot": 0,
    "wl_rc": 0,               # rc of wl (it then writes nothing)
    "wl_corrupt": False,      # wl writes one byte wrong
    "wl_touch_ab": False,     # a broken device: wl also changes LBA 49156
    "rd_maskrom": False,      # after rd the unit shows Maskrom
    "flip_slot": False,       # fastboot reports the other slot
    "current_slot": "a",
    "fastboot_present": False,  # a fastboot device is there from the start
    "ssh_dry_rc": 0, "ssh_rc": 255, "ssh_noop": False,
    "tamper_image_on_getvar": None,   # path: changed on the first getvar
}


def S(*p):
    return os.path.join(os.environ["FAKE_STATE"], *p)


def cfg():
    c = dict(CONFIG)
    try:
        with open(S("config.json")) as f:
            c.update(json.load(f))
    except FileNotFoundError:
        pass
    return c


def log(prog, ev, **kw):
    rec = {"prog": prog, "argv": sys.argv[1:], "ev": ev, "t": time.time()}
    rec.update(kw)
    with open(S("calls.log"), "a") as f:
        f.write(json.dumps(rec) + "\n")


def forbidden(prog, why):
    log(prog, "FORBIDDEN", why=why)
    sys.stderr.write("FAKE: FORBIDDEN: %s\n" % why)
    sys.exit(99)


def get(name, default=""):
    try:
        with open(S(name)) as f:
            return f.read().strip()
    except FileNotFoundError:
        return default


def put(name, val):
    with open(S(name), "w") as f:
        f.write(str(val))


def counter(name):
    n = int(get(name, "0")) + 1
    put(name, n)
    return n


def read_sec(lba):
    try:
        with open(S("sectors", str(lba)), "rb") as f:
            return f.read()
    except FileNotFoundError:
        return bytes(512)


def write_sec(lba, data):
    os.makedirs(S("sectors"), exist_ok=True)
    with open(S("sectors", str(lba)), "wb") as f:
        f.write(data)


def rkdeveloptool(c):
    a = sys.argv[1:]
    verb = a[0] if a else ""
    mode = get("mode", "off")
    if verb == "ld":
        if mode == "rockusb" and counter("ld_count") <= c["rockusb_after"]:
            print("not found any devices!")
            return 0
        if mode == "maskrom":
            print("DevNo=1\tVid=0x2207,Pid=0x330c,LocationID=104\tMaskrom")
        elif mode == "rockusb":
            print("DevNo=1\tVid=0x2207,Pid=0x330c,LocationID=104\tLoader")
            if c["several"]:
                print("DevNo=2\tVid=0x2207,Pid=0x330c,LocationID=105\tLoader")
        else:
            print("not found any devices!")
        return 0
    if verb in ("rl", "wl", "rd") and mode != "rockusb":
        print("Creating Comm Object failed!")
        return 1
    if verb == "rl":
        lba, n, path = int(a[1]), int(a[2]), a[3]
        time.sleep(c["slow_rl"])
        with open(path, "wb") as f:
            for i in range(n):
                f.write(read_sec(lba + i))
        print("Read LBA to file (100%)")
        return 0
    if verb == "wl":
        lba, path = int(a[1]), a[2]
        with open(path, "rb") as f:
            data = f.read()
        n = (len(data) + 511) // 512
        if lba <= AB_LBA < lba + n:
            forbidden("rkdeveloptool", "write over LBA 49156")
        if lba != BCB_LBA or len(data) != 2048:
            forbidden("rkdeveloptool", "a write that is not the 2048-byte BCB")
        time.sleep(c["slow_wl"])
        if c["wl_rc"]:
            print("Write LBA failed!")
            return c["wl_rc"]
        if c["wl_corrupt"]:
            data = data[:100] + bytes([data[100] ^ 0xff]) + data[101:]
        for i in range(n):
            write_sec(lba + i, data[i * 512:(i + 1) * 512])
        if c["wl_touch_ab"]:
            write_sec(AB_LBA, bytes(512))
        print("Write LBA from file (100%)")
        return 0
    if verb == "rd":
        bcb = b"".join(read_sec(BCB_LBA + i) for i in range(4))
        if c["rd_maskrom"]:
            put("mode", "maskrom")
        elif bcb.startswith(CMD):
            for i in range(4):          # U-Boot acts on the command and clears it
                write_sec(BCB_LBA + i, bytes(512))
            put("mode", "fastboot")
        else:
            put("mode", "android")
        print("Reset Device OK.")
        return 0
    forbidden("rkdeveloptool", "verb %r" % verb)


def fastboot(c):
    a = sys.argv[1:]
    serial = None
    if a[:1] == ["-s"]:
        serial, a = a[1], a[2:]
    verb = a[0] if a else ""
    mode = get("mode", "off")
    present = c["fastboot_present"] or (mode == "fastboot")
    if verb == "devices":
        if present and (c["fastboot_present"] or counter("fb_count") > c["fastboot_after"]):
            print("FAKE0001\tfastboot")
        return 0
    if verb not in ("getvar", "boot"):
        forbidden("fastboot", "verb %r" % verb)
    if serial != "FAKE0001" or not present:
        sys.stderr.write("< waiting for any device >\n")
        return 1
    if verb == "getvar":
        v = a[1]
        if v == "all":
            forbidden("fastboot", "getvar all")
        t = c["tamper_image_on_getvar"]
        if t and get("tampered") != "1":
            with open(t, "ab") as f:
                f.write(b"x")
            put("tampered", "1")
        slot = c["current_slot"]
        if c["flip_slot"]:
            slot = "b" if slot == "a" else "a"
        vals = {"product": "rk3399", "current-slot": slot, "slot-successful:a": "yes",
                "slot-successful:b": "yes", "slot-retry-count:a": "0", "slot-retry-count:b": "0"}
        sys.stderr.write("%s: %s\nFinished. Total time: 0.001s\n" % (v, vals.get(v, "")))
        return 0
    path = a[1]
    with open(path, "rb") as f:
        sha = hashlib.sha256(f.read()).hexdigest()
    log("fastboot", "boot-data", sha256=sha)
    time.sleep(c["slow_boot"])
    sys.stderr.write("Sending 'boot.img' OKAY\nBooting OKAY\n")
    put("mode", "booted")
    return 0


def ssh(c):
    a = sys.argv[1:]
    while a and a[0] == "-o":
        a = a[2:]
    remote = a[1:]
    if remote == ["/usr/sbin/uc-pxx-reboot", "--dry-run", "fastboot"]:
        if c["ssh_dry_rc"]:
            print("sh: /usr/sbin/uc-pxx-reboot: not found")
            return c["ssh_dry_rc"]
        print("would stop the system with init, then restart with the argument 'fastboot'")
        return 0
    if remote == ["/usr/sbin/uc-pxx-reboot", "fastboot"]:
        if not c["ssh_noop"]:
            put("mode", "fastboot")
        if c["ssh_rc"] == 255:
            sys.stderr.write("Connection to unit closed by remote host.\n")
        return c["ssh_rc"]
    forbidden("ssh", "remote command %r" % (remote,))


def main(prog):
    c = cfg()
    log(prog, "start")
    rc = {"rkdeveloptool": rkdeveloptool, "fastboot": fastboot, "ssh": ssh}[prog](c)
    log(prog, "end", rc=rc)
    sys.exit(rc)
