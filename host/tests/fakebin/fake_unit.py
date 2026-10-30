# SPDX-License-Identifier: GPL-2.0-or-later
"""A fake UC-P10 for the p10-enter tests: fake rkdeveloptool, fastboot and ssh.

No real device is ever touched. The state lives in $FAKE_STATE:
  config.json   the test's settings (see CONFIG)
  mode          off | rockusb | maskrom | mainline | fastboot | android | booted
  sectors/LBA   512-byte sectors of the fake eMMC (missing = zeros)
  calls.log     one JSON line per event: {"prog", "argv", "ev": start|end|FORBIDDEN|..., "t"}
Any command that p10-enter must never send (a write that is not the BCB, a
write over LBA 49156, rd other than rd 0, getvar all, a fastboot verb other
than devices/getvar/boot, a fastboot command to a device that is not the unit,
an ssh without -n, with an unknown remote command, or with a stdin that is not
/dev/null) logs FORBIDDEN and exits 99.

The fake U-Boot answers the slot getvars from the fake LBA 49156, as the real
one reads the A/B sector: a test changes the A/B state "on entry" with
uboot_rewrite_ab, or the answers with getvar_override.
"""
import hashlib
import json
import os
import stat
import sys
import time
import zlib

AB_LBA, BCB_LBA = 49156, 49184
CMD = b"bootonce-bootloader"
SERIAL = "FAKE0001"
CONFIG = {
    "rockusb_after": 0,       # ld calls before the rockusb device shows
    "several": False,         # ld lists two devices
    "ld_extra": None,         # an extra line in every ld output
    "ld_mode": "Loader",      # the mode word of the one device
    "ld_output": None,        # in the download mode: the whole ld output instead of the normal one
    "guard_ld": None,         # after the first BCB read, ld shows: maskrom | msc | two | unparsed
    "bcb_change_at_guard": None,   # hex: the BCB bytes become this after the first BCB read
    "slow_guard_rl": 0,       # the second BCB read (the guard read) takes this long
    "tamper_payload_on_guard": None,  # path: made 4096 bytes on the guard read
    "fastboot_after": 2,      # fastboot devices calls before the device shows
    "slow_rl": 0, "slow_wl": 0, "slow_boot": 0,
    "wl_rc": 0,               # rc of wl (it then writes nothing)
    "wl_corrupt": False,      # wl writes one byte wrong
    "wl_touch_ab": False,     # a broken device: wl also changes LBA 49156
    "rd_maskrom": False,      # after rd the unit shows Maskrom
    "uboot_rewrite_ab": None,  # hex: U-Boot writes this to LBA 49156 when it enters fastboot
    "unit_no_fastboot": False,  # after the reset the unit boots normally
    "foreign_serial": None,   # another fastboot device shows when the unit does not
    "second_fastboot": False,  # a second fastboot device next to the unit
    "fastboot_present": False,  # a fastboot device is there from the start
    "getvar_override": {},    # name: value
    "getvar_fail": [],        # names that fastboot does not answer
    "chmod_fastboot_on_rd": None,   # path: made not executable at rd (the tool then cannot start it)
    "tamper_image_on_boot": None,   # path: changed just before fastboot reads its file
    "tamper_copy_on_getvar": None,  # path: changed on the first getvar
    "remote": {},             # the exact remote commands the test expects: name -> string
    "unit_restart_block": None,     # text of /run/tsx/restart-block (None = absent)
    "unit_fields": {"in_progress": "0", "post_update": ""},   # None = the file is absent
    "state_extra": None,      # an extra line in the state check answer
    "state_rc": 0,
    "ssh_dd_rc": 0, "ssh_dd_short": False,
    "ssh_dry_rc": 0, "dry_output": None, "ssh_rc": 255, "ssh_noop": False,
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


def ab_vars():
    """What a U-Boot reads from LBA 49156 (the AOSP meaning of the vars)."""
    r = read_sec(AB_LBA)
    out = {}
    ok = r[:4] == b"\0AB0" and int.from_bytes(r[28:32], "big") == zlib.crc32(r[:28]) & 0xffffffff
    slots = [tuple(r[8 + 4 * i:12 + 4 * i]) for i in range(2)] if ok else [(0, 0, 0, 0)] * 2
    for i, (p, t, s, _) in enumerate(slots):
        n = "ab"[i]
        out["slot-successful:" + n] = "yes" if s == 1 else "no"
        out["slot-retry-count:" + n] = str(t)
        out["slot-unbootable:" + n] = "no" if (p > 0 and (s == 1 or t > 0)) else "yes"
    boot = [p > 0 and (s == 1 or t > 0) for p, t, s, _ in slots]
    out["current-slot"] = "b" if (boot[1] and (not boot[0] or slots[1][0] > slots[0][0])) else "a"
    return out


def rkdeveloptool(c):
    a = sys.argv[1:]
    verb = a[0] if a else ""
    mode = get("mode", "off")
    if verb == "ld":
        if mode == "rockusb" and counter("ld_count") <= c["rockusb_after"]:
            print("not found any devices!")
            return 0
        g = c["guard_ld"] if int(get("bcb_reads", "0")) >= 1 else None
        if mode == "maskrom" or (mode == "rockusb" and g == "maskrom"):
            print("DevNo=1\tVid=0x2207,Pid=0x330c,LocationID=104\tMaskrom")
        elif mode == "rockusb" and c["ld_output"] is not None:
            print(c["ld_output"])
        elif mode == "rockusb":
            if g == "msc":
                print("DevNo=1\tVid=0x2207,Pid=0x330c,LocationID=104\tMSC")
            elif g == "unparsed":
                print("DevNo=1\tVid=0x2207,Pid=0x330c,LocationID=104\tLoader")
                print("DevNo=2\tVid=0x2207,Pid=0x330c\tLoader")
            else:
                print("DevNo=1\tVid=0x2207,Pid=0x330c,LocationID=104\t%s" % c["ld_mode"])
            if c["several"] or g == "two":
                print("DevNo=2\tVid=0x2207,Pid=0x330c,LocationID=105\tLoader")
            if c["ld_extra"]:
                print(c["ld_extra"])
        else:
            print("not found any devices!")
        return 0
    if verb in ("rl", "wl", "rd") and mode != "rockusb":
        print("Creating Comm Object failed!")
        return 1
    if verb == "rl":
        lba, n, path = int(a[1]), int(a[2]), a[3]
        change = None
        if lba == BCB_LBA:
            k = counter("bcb_reads")
            if k == 2:                 # the guard read
                time.sleep(c["slow_guard_rl"])
                if c["tamper_payload_on_guard"]:
                    p = c["tamper_payload_on_guard"]
                    os.chmod(p, 0o600)
                    with open(p, "wb") as f:
                        f.write(CMD + bytes(4096 - len(CMD)))
            if k == 1 and c["bcb_change_at_guard"]:
                change = bytes.fromhex(c["bcb_change_at_guard"])
        time.sleep(c["slow_rl"])
        with open(path, "wb") as f:
            for i in range(n):
                f.write(read_sec(lba + i))
        if change is not None:
            for i in range(4):
                write_sec(BCB_LBA + i, change[i * 512:(i + 1) * 512])
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
        if a[1:] != ["0"]:
            forbidden("rkdeveloptool", "rd %r (only rd 0)" % (a[1:],))
        bcb = b"".join(read_sec(BCB_LBA + i) for i in range(4))
        if c["chmod_fastboot_on_rd"]:
            os.chmod(c["chmod_fastboot_on_rd"], 0o644)
        if c["rd_maskrom"]:
            put("mode", "maskrom")
        elif bcb.startswith(CMD) and not c["unit_no_fastboot"]:
            for i in range(4):          # U-Boot acts on the command and clears it
                write_sec(BCB_LBA + i, bytes(512))
            if c["uboot_rewrite_ab"]:
                write_sec(AB_LBA, bytes.fromhex(c["uboot_rewrite_ab"]))
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
        n = counter("fb_count")
        if present and (c["fastboot_present"] or n > c["fastboot_after"]):
            print("%s\tfastboot" % SERIAL)
            if c["second_fastboot"]:
                print("FAKE0002\tfastboot")
        if c["foreign_serial"] and mode == "android" and n > c["fastboot_after"]:
            print("%s\tfastboot" % c["foreign_serial"])
        return 0
    if verb not in ("getvar", "boot"):
        forbidden("fastboot", "verb %r" % verb)
    if serial != SERIAL or not present:
        forbidden("fastboot", "a command to a device that is not the unit (%r)" % serial)
    if verb == "getvar":
        v = a[1]
        if v == "all":
            forbidden("fastboot", "getvar all")
        t = c["tamper_copy_on_getvar"]
        if t and get("tampered") != "1":
            os.chmod(t, 0o600)
            with open(t, "ab") as f:
                f.write(b"x")
            put("tampered", "1")
        vals = {"product": "rk3399", "variant": "fake", "serialno": SERIAL}
        vals.update(ab_vars())
        vals.update(c["getvar_override"])
        if v in c["getvar_fail"]:
            sys.stderr.write("getvar:%s FAILED (remote: 'Variable not implemented')\n"
                             "Finished. Total time: 0.001s\n" % v)
            return 1
        sys.stderr.write("%s: %s\nFinished. Total time: 0.001s\n" % (v, vals.get(v, "")))
        return 0
    path = a[1]
    if c["tamper_image_on_boot"]:
        with open(c["tamper_image_on_boot"], "ab") as f:
            f.write(b"changed after the tool's last check")
    with open(path, "rb") as f:
        sha = hashlib.sha256(f.read()).hexdigest()
    log("fastboot", "boot-data", sha256=sha, serial=serial, path=path)
    time.sleep(c["slow_boot"])
    sys.stderr.write("Sending 'boot.img' OKAY\nBooting OKAY\n")
    put("mode", "booted")
    return 0


def stdin_is_null():
    try:
        st, nul = os.fstat(0), os.stat(os.devnull)
    except OSError:
        return True          # a closed stdin is fine too
    return stat.S_ISCHR(st.st_mode) and st.st_rdev == nul.st_rdev


def ssh(c):
    a = sys.argv[1:]
    if a[:1] != ["-n"]:
        forbidden("ssh", "ssh without -n")
    if not stdin_is_null():
        forbidden("ssh", "ssh with a stdin that is not /dev/null")
    a = a[1:]
    while a and a[0] == "-o":
        a = a[2:]
    if len(a) != 2:
        forbidden("ssh", "not TARGET and one remote command: %r" % (a,))
    remote = a[1]
    R = c["remote"]
    if remote not in R.values():
        forbidden("ssh", "remote command %r" % (remote,))
    if get("mode", "off") != "mainline":
        sys.stderr.write("ssh: connect to host unit.invalid port 22: No route to host\n")
        return 255
    if remote == R["state_check"]:
        if c["state_rc"]:
            print("sh: syntax error")
            return c["state_rc"]
        rb = c["unit_restart_block"]
        print("restart-block absent" if rb is None else "restart-block present: %s" % rb)
        f = c["unit_fields"]
        if f is None:
            print("fields absent")
        else:
            print("fields present")
            for k in ("in_progress", "post_update"):
                if k in f:
                    print("%s %s" % (k, f[k]) if f[k] else k)
        if c["state_extra"]:
            print(c["state_extra"])
        print("check end")
        return 0
    if remote == R["ab_read"]:
        if c["ssh_dd_rc"]:
            sys.stderr.write("dd: can't open '/dev/mmcblk0': Permission denied\n")
            return c["ssh_dd_rc"]
        d = read_sec(AB_LBA)
        sys.stdout.buffer.write(d[:100] if c["ssh_dd_short"] else d)
        sys.stdout.flush()
        sys.stderr.write("1+0 records in\n1+0 records out\n")
        return 0
    if remote == R["reboot_dry"]:
        if c["ssh_dry_rc"]:
            print("sh: /usr/sbin/uc-pxx-reboot: not found")
            return c["ssh_dry_rc"]
        print(c["dry_output"] or "would stop the system with init, then restart with the argument 'fastboot'")
        return 0
    if remote == R["reboot"]:
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
