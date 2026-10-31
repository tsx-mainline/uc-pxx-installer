#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""Tests of host/p10-enter with fake rkdeveloptool, fastboot and ssh.

    python3 -I host/tests/test_p10_enter.py [-v]

No real device is used. Each test makes its own fake programs (an absolute
interpreter path, no PATH lookup), passes them to the tool as explicit paths,
and runs the tool with PATH set to that folder only: a real fastboot or
rkdeveloptool cannot be reached. On a host that has a unit, run the tests in
a container without USB devices anyway (ci/lint.sh says how).

After every test:
  - no fake logged FORBIDDEN (a write that is not the BCB, a write over LBA
    49156, rd other than 0, getvar all, a command to another fastboot device,
    an ssh without -n or with an unknown remote command, ...);
  - no rkdeveloptool wl covered LBA 49156;
  - every fake command that started also ended (nothing was killed);
  - every booted byte string had the given sha256 and came from LOGDIR/boot.img.
"""
import ast
import hashlib
import importlib.machinery
import importlib.util
import json
import os
import shutil
import signal
import stat
import subprocess
import sys
import tempfile
import time
import unittest
import zlib

HERE = os.path.dirname(os.path.realpath(__file__))
HOST = os.path.dirname(HERE)
TOOL = os.path.join(HOST, "p10-enter")
FAKEMOD = os.path.join(HERE, "fakebin")
STATES = os.path.join(HOST, "bcb-known-states.json")
PAYLOAD_SHA = "892627cb4356d1d16c37bf243d700b5c9a0ab672d263de9ccd640c86b5cd6a5a"
STATES_SHA = "37373dd625b7435429cbfe4067f1ac82d21ccddf9c3f8febcf1e8017a57de79a"
AB_LBA, BCB_LBA = 49156, 49184
SERIAL = "FAKE0001"
# The remote commands, written out here on purpose (not taken from the tool):
# the fake ssh refuses any other string.
REMOTE = {
    "state_check": (
        "p=$(command -v flock); "
        "if [ -z \"$p\" ]; then echo 'flock missing'; "
        "elif [ ! -e /run/tsx/autoupdate.lock ]; then echo 'lock absent'; "
        "elif flock -n /run/tsx/autoupdate.lock true; then echo 'lock free'; "
        "else echo 'lock held'; fi; "
        "if [ -e /run/tsx/restart-block ]; then "
        "echo \"restart-block present: $(head -n 1 /run/tsx/restart-block)\"; "
        "else echo 'restart-block absent'; fi; "
        "f=/var/lib/tsx/autoupdate/fields; "
        "if [ -e \"$f\" ]; then echo 'fields present'; grep -E '^(in_progress|post_update)( |$)' \"$f\"; "
        "else echo 'fields absent'; fi; "
        "echo 'check end'"),
    "ab_read": "dd if=/dev/mmcblk0 bs=512 skip=49156 count=1",
    "reboot_dry": "/usr/sbin/uc-pxx-reboot --dry-run fastboot",
    "reboot": "/usr/sbin/uc-pxx-reboot fastboot",
}
SSH_PREFIX = ["-n", "-o", "BatchMode=yes", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=6",
              "-o", "ConnectTimeout=15", "root@unit.invalid"]
GETVARS = ["serialno", "current-slot", "slot-successful:a", "slot-successful:b", "slot-unbootable:a",
           "slot-unbootable:b", "slot-retry-count:a", "slot-retry-count:b", "variant", "product"]
REQUIRED = GETVARS[:8]
LEAVE = "leave fastboot only by a cold power cycle"
NEVER_REBOOT = "Never send `fastboot reboot`"
STATE_OK = "lock free\nrestart-block absent\nfields present\nin_progress 0\npost_update \ncheck end\n"


def tool_sha():
    with open(TOOL, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def go_text(ab_sha, payload_sha=PAYLOAD_SHA, tsha=None):
    return ("Rule-9 GO record (test)\n\npayload-sha256: %s\nab-sha256: %s\ntool-sha256: %s\n"
            % (payload_sha, ab_sha, tsha or tool_sha()))


def load_tool():
    loader = importlib.machinery.SourceFileLoader("p10enter", TOOL)
    spec = importlib.util.spec_from_loader("p10enter", loader)
    m = importlib.util.module_from_spec(spec)
    sys.dont_write_bytecode = True
    loader.exec_module(m)
    return m


P = load_tool()


def ab_sector(a=(15, 0, 1), b=(14, 0, 1), little=False, magic=b"\0AB0"):
    r = magic + bytes([1, 0, 0, 0]) + bytes(a) + b"\0" + bytes(b) + b"\0" + bytes(12)
    crc = zlib.crc32(r) & 0xffffffff
    r += crc.to_bytes(4, "little" if little else "big")
    return r + bytes(512 - len(r))


def payload():
    return b"bootonce-bootloader" + bytes(2048 - 19)


def dfl_signals():
    """In the child: signals at their defaults, as a shell starts a job."""
    for s in (signal.SIGINT, signal.SIGQUIT, signal.SIGTERM, signal.SIGHUP, signal.SIGPIPE):
        signal.signal(s, signal.SIG_DFL)


class Base(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="p10enter-test-")
        self.state = os.path.join(self.tmp, "state")
        os.makedirs(os.path.join(self.state, "sectors"))
        self.bin = os.path.join(self.tmp, "bin")
        os.makedirs(self.bin)
        for prog in ("rkdeveloptool", "fastboot", "ssh"):
            p = os.path.join(self.bin, prog)
            with open(p, "w") as f:
                f.write("#!%s\n# fake %s for the p10-enter tests\nimport sys\nsys.dont_write_bytecode = True\n"
                        "sys.path.insert(0, %r)\nimport fake_unit\nfake_unit.main(%r)\n"
                        % (sys.executable, prog, FAKEMOD, prog))
            os.chmod(p, 0o755)
            self.assertTrue(os.access(p, os.X_OK), "the fake %s is not executable" % prog)
            self.assertEqual(shutil.which(prog, path=self.bin), p)
        self.fake = {p: os.path.join(self.bin, p) for p in ("rkdeveloptool", "fastboot", "ssh")}
        self.image = os.path.join(self.tmp, "installer.img")
        with open(self.image, "wb") as f:
            f.write(os.urandom(1 << 20))
        with open(self.image, "rb") as f:
            self.sha = hashlib.sha256(f.read()).hexdigest()
        self.gofile = os.path.join(self.tmp, "GO-session.md")
        self.go_override = None       # text of the GO file; None = the right one for the run
        self.set_sector(AB_LBA, ab_sector())
        self.ab_sha = hashlib.sha256(ab_sector()[:32]).hexdigest()
        self.set_mode("rockusb")
        self.config({})
        self.logdir = os.path.join(self.tmp, "log")
        self.out = ""

    def tearDown(self):
        try:
            self.check_calls()
        finally:
            shutil.rmtree(self.tmp, ignore_errors=True)

    def check_calls(self):
        """The rules that hold after every run (see the top of this file)."""
        ev = self.calls()
        self.assertEqual([e for e in ev if e["ev"] == "FORBIDDEN"], [], "a fake logged FORBIDDEN")
        for e in ev:
            if e["prog"] == "rkdeveloptool" and e["argv"][:1] == ["wl"] and e["ev"] == "start":
                lba = int(e["argv"][1])
                n = (os.path.getsize(e["argv"][2]) + 511) // 512
                self.assertFalse(lba <= AB_LBA < lba + n, "wl covered LBA 49156")
        starts = [e for e in ev if e["ev"] == "start"]
        ends = [e for e in ev if e["ev"] == "end"]
        self.assertEqual(len(starts), len(ends), "a fake command started but did not end")
        for e in ev:
            if e["ev"] == "boot-data":
                self.assertEqual(e["sha256"], self.sha, "fastboot sent bytes that were not checked")
                self.assertEqual(e["path"], os.path.join(self.logdir, "boot.img"))

    # fake unit state
    def set_mode(self, m):
        with open(os.path.join(self.state, "mode"), "w") as f:
            f.write(m)

    def mode(self):
        with open(os.path.join(self.state, "mode")) as f:
            return f.read().strip()

    def config(self, c):
        c = dict(c)
        c["remote"] = REMOTE
        with open(os.path.join(self.state, "config.json"), "w") as f:
            json.dump(c, f)

    def set_sector(self, lba, data):
        for i in range(0, len(data), 512):
            with open(os.path.join(self.state, "sectors", str(lba + i // 512)), "wb") as f:
                f.write(data[i:i + 512])

    def get_sectors(self, lba, n):
        out = b""
        for i in range(n):
            p = os.path.join(self.state, "sectors", str(lba + i))
            if os.path.exists(p):
                with open(p, "rb") as f:
                    out += f.read()
            else:
                out += bytes(512)
        return out

    def calls(self):
        p = os.path.join(self.state, "calls.log")
        if not os.path.exists(p):
            return []
        with open(p) as f:
            return [json.loads(ln) for ln in f]

    def starts(self):
        return [(e["prog"], e["argv"]) for e in self.calls() if e["ev"] == "start"]

    def log_text(self):
        p = os.path.join(self.logdir, "p10-enter.log")
        if not os.path.exists(p):
            return ""
        with open(p) as f:
            return f.read()

    def env(self):
        return {"PATH": self.bin, "FAKE_STATE": self.state, "HOME": self.tmp, "LC_ALL": "C.UTF-8"}

    def argv(self, route, *extra, sha=None, ab_sha=None, go=True):
        a = [route]
        if route in ("ssh", "rockusb"):
            a += ["--image", self.image, "--sha256", sha or self.sha, "--wait-fastboot", "3",
                  "--expect-serialno", SERIAL, "--fastboot", self.fake["fastboot"]]
        a += ["--rkdeveloptool", self.fake["rkdeveloptool"]]
        if route == "ssh":
            a += ["--target", "root@unit.invalid", "--ssh-opt", "BatchMode=yes", "--ssh", self.fake["ssh"]]
            if ab_sha:
                a += ["--expect-ab-sha256", ab_sha]
        if route in ("rockusb", "dump"):
            a += ["--wait-rockusb", "3"]
        if route == "rockusb":
            a += ["--expect-ab-sha256", ab_sha or self.ab_sha]
            with open(self.gofile, "w") as f:
                f.write(self.go_override if self.go_override is not None else go_text(ab_sha or self.ab_sha))
            if go:
                a += ["--bcb-go", PAYLOAD_SHA, "--go-file", self.gofile]
        return a + ["--log-dir", self.logdir, "--poll-interval", "0.05"] + list(extra)

    def run_argv(self, argv):
        r = subprocess.run([sys.executable, "-I", TOOL] + argv, env=self.env(), cwd=self.tmp,
                           input=b"", stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                           timeout=120)   # stdin a pipe: an ssh that inherits it is caught
        self.out = r.stdout.decode()
        return r.returncode

    def run_tool(self, route, *extra, **kw):
        return self.run_argv(self.argv(route, *extra, **kw))

    def fresh(self):
        """Before the next run in one test: check the runs so far, then start a new call log."""
        self.check_calls()
        if os.path.exists(self.logdir):
            shutil.rmtree(self.logdir)
        for f in ("calls.log", "bcb_reads", "fb_count", "fb_shown", "ld_count", "tampered"):
            p = os.path.join(self.state, f)
            if os.path.exists(p):
                os.unlink(p)

    def assertExit(self, rc, key):
        self.assertEqual(rc, P.EXIT[key][0], "want %s, output:\n%s" % (key, self.out))

    def wl_calls(self):
        return [a for p, a in self.starts() if p == "rkdeveloptool" and a[:1] == ["wl"]]

    def rd_calls(self):
        return [a for p, a in self.starts() if p == "rkdeveloptool" and a[:1] == ["rd"]]

    def boot_calls(self):
        return [a for p, a in self.starts() if p == "fastboot" and "boot" in a]

    def ssh_remotes(self):
        return [a[-1] for p, a in self.starts() if p == "ssh"]

    def assertNothingWritten(self):
        self.assertEqual(self.wl_calls(), [])
        self.assertEqual(self.rd_calls(), [])
        self.assertEqual(self.boot_calls(), [])

    def assertNotBooted(self):
        self.assertEqual(self.boot_calls(), [])


class HappyPaths(Base):
    def test_ssh_happy(self):
        self.set_mode("mainline")
        rc = self.run_tool("ssh")
        self.assertExit(rc, "OK")
        st = self.starts()
        self.assertEqual(st[0], ("fastboot", ["devices"]))   # nothing else connected first
        ssh = [a for p, a in st if p == "ssh"]
        self.assertEqual(ssh, [SSH_PREFIX + [REMOTE[k]] for k in ("state_check", "ab_read", "reboot_dry",
                                                                  "reboot")])
        self.assertEqual([a for p, a in st if p == "rkdeveloptool" and a[0] != "ld"], [])
        getvars = [a[3] for p, a in st if p == "fastboot" and "getvar" in a]
        self.assertEqual(getvars, GETVARS)
        self.assertEqual(self.boot_calls(), [["-s", SERIAL, "boot", os.path.join(self.logdir, "boot.img")]])
        self.assertEqual(len([e for e in self.calls() if e["ev"] == "boot-data"]), 1)
        self.assertEqual(self.mode(), "booted")
        with open(os.path.join(self.logdir, "ab-ssh.bin"), "rb") as f:
            self.assertEqual(f.read(), ab_sector())
        self.assertIn("fastboot getvars match the A/B sector read over ssh", self.log_text())
        self.assertIn("no tsx-autoupdate run, no restart block, no install, no hold", self.log_text())
        with open(os.path.join(self.logdir, "state-check.txt")) as f:
            self.assertEqual(f.read(), STATE_OK)

    def test_ssh_happy_variants(self):
        for cfg, extra in (({"ssh_rc": 0}, {}), ({"unit_lock": "absent"}, {}),
                           ({"unit_fields": {"in_progress": "", "post_update": ""}}, {}),
                           ({"ssh_stderr_warning": "** WARNING: connection is not using a post-quantum key "
                                                   "exchange algorithm."}, {}),           # probe P13, R2-2
                           ({"getvar_override": {"current-slot": "_a"}}, {}),
                           ({"getvar_fail": ["product", "variant"]}, {}),
                           ({}, {"ab_sha": hashlib.sha256(ab_sector()[:32]).hexdigest()})):
            self.fresh()
            self.set_mode("mainline")
            self.config(cfg)
            self.assertExit(self.run_tool("ssh", **extra), "OK")

    def test_rockusb_happy(self):
        rc = self.run_tool("rockusb")
        self.assertExit(rc, "OK")
        seq = [(p, a[:3]) for p, a in self.starts() if p == "rkdeveloptool" and a[0] != "ld"]
        self.assertEqual(seq, [
            ("rkdeveloptool", ["rl", "49156", "1"]),
            ("rkdeveloptool", ["rl", "49184", "4"]),
            ("rkdeveloptool", ["rl", "49184", "4"]),      # the guard read
            ("rkdeveloptool", ["wl", "49184", os.path.join(self.logdir, "bcb-payload.bin")]),
            ("rkdeveloptool", ["rl", "49184", "4"]),      # read-back
            ("rkdeveloptool", ["rl", "49156", "1"]),      # A/B unchanged
            ("rkdeveloptool", ["rd", "0"]),
        ])
        self.assertEqual(len(self.wl_calls()), 1)
        pp = os.path.join(self.logdir, "bcb-payload.bin")
        with open(pp, "rb") as f:
            self.assertEqual(hashlib.sha256(f.read()).hexdigest(), PAYLOAD_SHA)
        self.assertEqual(stat.S_IMODE(os.stat(pp).st_mode), 0o400)
        self.assertEqual(stat.S_IMODE(os.stat(os.path.join(self.logdir, "boot.img")).st_mode), 0o400)
        with open(os.path.join(self.logdir, "bcb-after.bin"), "rb") as f:
            self.assertEqual(f.read(), payload())
        self.assertEqual(self.get_sectors(AB_LBA, 1), ab_sector(), "the A/B sector changed")
        self.assertEqual(self.boot_calls(), [["-s", SERIAL, "boot", os.path.join(self.logdir, "boot.img")]])
        getvars = [a[3] for p, a in self.starts() if p == "fastboot" and "getvar" in a]
        self.assertEqual(getvars, GETVARS)
        self.assertEqual(self.mode(), "booted")
        self.assertTrue(os.path.exists(os.path.join(self.logdir, "rule9-go.md")))
        self.assertIn("rule-9 GO record", self.log_text())

    def test_rockusb_slot_b_next(self):
        sec = ab_sector(a=(14, 0, 1), b=(15, 0, 1))
        self.set_sector(AB_LBA, sec)
        self.assertExit(self.run_tool("rockusb", ab_sha=hashlib.sha256(sec[:32]).hexdigest()), "OK")

    def test_rockusb_bcb_already_command(self):
        self.set_sector(BCB_LBA, payload())
        self.assertExit(self.run_tool("rockusb"), "OK")
        self.assertEqual(self.wl_calls(), [], "no write when the BCB already holds the command")

    def test_rockusb_waits_for_device(self):
        self.config({"rockusb_after": 5})
        self.assertExit(self.run_tool("rockusb"), "OK")


class RockusbRefusals(Base):
    def test_unknown_bcb_refused(self):
        for bcb in (os.urandom(2048), b"\xff" * 2048, bytes(512) + b"x" + bytes(1535)):
            self.set_sector(BCB_LBA, bcb)
            self.fresh()
            rc = self.run_tool("rockusb")
            self.assertExit(rc, "BCB_UNKNOWN")
            self.assertIn("unknown BCB, run the read-only dump first", self.out)
            self.assertNothingWritten()
            self.assertEqual(self.get_sectors(BCB_LBA, 4), bcb)
            with open(os.path.join(self.logdir, "bcb-before.bin"), "rb") as f:
                self.assertEqual(f.read(), bcb)

    def test_bcb_changed_before_guard_read(self):           # mutation M2
        self.config({"bcb_change_at_guard": (b"x" * 2048).hex()})
        self.assertExit(self.run_tool("rockusb"), "BCB_UNKNOWN")
        self.assertIn("changed between two reads", self.out)
        self.assertNothingWritten()

    def test_guard_ld_maskrom(self):                        # mutation M5
        self.config({"guard_ld": "maskrom"})
        self.assertExit(self.run_tool("rockusb"), "MASKROM")
        self.assertNothingWritten()

    def test_guard_ld_changed(self):                        # mutation M1
        for g in ("msc", "two", "unparsed"):
            self.fresh()
            for f in ("bcb_reads",):
                p = os.path.join(self.state, f)
                if os.path.exists(p):
                    os.unlink(p)
            self.config({"guard_ld": g})
            self.assertExit(self.run_tool("rockusb"), "AMBIGUOUS_DEVICE")
            self.assertNothingWritten()

    def test_single_device_not_loader(self):               # mutation M4
        for mode in ("MSC", "Unknown"):
            self.fresh()
            self.config({"ld_mode": mode})
            self.assertExit(self.run_tool("rockusb"), "AMBIGUOUS_DEVICE")
            self.assertEqual([a for p, a in self.starts() if p == "rkdeveloptool" and a[0] != "ld"], [])

    def test_ld_unparsed_line(self):                        # F10, probe P5
        self.config({"ld_extra": "DevNo=2\tVid=0x2207,Pid=0x330c\tLoader"})
        self.assertExit(self.run_tool("rockusb"), "AMBIGUOUS_DEVICE")
        self.assertNothingWritten()
        self.fresh()
        self.config({"ld_extra": "DevNo=2\tVid=0x2207,Pid=0x330c,LocationID=105\tMSC"})   # probe P5b
        self.assertExit(self.run_tool("rockusb"), "AMBIGUOUS_DEVICE")
        self.assertNothingWritten()
        for out in ("DevNo=1\tVid=0x2207,Pid=0x330c\tLoader",            # one line that does not parse
                    "DevNo=1 Vid=2207 Pid=330c Loader"):
            self.fresh()
            self.config({"ld_output": out})
            self.assertExit(self.run_tool("rockusb"), "AMBIGUOUS_DEVICE")
            self.assertEqual([a for p, a in self.starts() if p == "rkdeveloptool" and a[0] != "ld"], [])

    def test_readback_mismatch(self):
        self.config({"wl_corrupt": True})
        rc = self.run_tool("rockusb")
        self.assertExit(rc, "READBACK_MISMATCH")
        self.assertEqual(len(self.wl_calls()), 1)
        self.assertEqual(self.rd_calls(), [], "reset after a mismatch")
        self.assertNotBooted()
        self.assertIn("Do not restart", self.out)
        self.assertIn("bcb-before.bin", self.out)
        self.assertEqual(self.get_sectors(AB_LBA, 1), ab_sector())

    def test_write_failed(self):
        self.config({"wl_rc": 1})
        self.assertExit(self.run_tool("rockusb"), "WRITE_FAILED")
        self.assertEqual(self.rd_calls(), [])
        self.assertNotBooted()

    def test_ab_changed_by_write(self):
        self.config({"wl_touch_ab": True})
        self.assertExit(self.run_tool("rockusb"), "AB_CHANGED")
        self.assertEqual(self.rd_calls(), [])

    def test_ab_invalid(self):
        bad = [ab_sector(little=True),                      # CRC little-endian
               ab_sector(a=(15, 1, 1)),                      # illegal successful+tries
               ab_sector(a=(15, 3, 0)),                      # next slot only trying
               ab_sector(magic=b"\0AB1"),
               ab_sector(a=(0, 0, 0), b=(0, 0, 0))]          # no bootable slot
        for sec in bad:
            self.set_sector(AB_LBA, sec)
            self.fresh()
            self.assertExit(self.run_tool("rockusb", ab_sha=hashlib.sha256(sec[:32]).hexdigest()),
                            "AB_INVALID")
            self.assertNothingWritten()

    def test_not_both_successful(self):                     # F2
        for b in ((14, 3, 0), (0, 0, 0), (14, 7, 0)):
            sec = ab_sector(b=b)
            self.set_sector(AB_LBA, sec)
            self.fresh()
            self.assertExit(self.run_tool("rockusb", ab_sha=hashlib.sha256(sec[:32]).hexdigest()),
                            "AB_NOT_BOTH_SUCCESSFUL")
            self.assertNothingWritten()
            self.assertEqual([a for p, a in self.starts() if a[:3] == ["rl", "49184", "4"]], [],
                             "the BCB is not even read")

    def test_expect_ab_mismatch(self):                      # F2
        self.assertExit(self.run_tool("rockusb", ab_sha="1" * 64), "AB_NOT_EXPECTED")
        self.assertNothingWritten()

    def test_expect_ab_required(self):
        a = self.argv("rockusb")
        i = a.index("--expect-ab-sha256")
        del a[i:i + 2]
        self.assertExit(self.run_argv(a), "USAGE")
        self.assertEqual(self.starts(), [])

    def test_no_go(self):
        self.assertExit(self.run_tool("rockusb", go=False), "NO_GO")
        self.assertEqual(self.starts(), [])
        self.assertExit(self.run_tool("rockusb", "--bcb-go", "1" * 64, "--go-file", self.gofile, go=False),
                        "NO_GO")
        self.assertExit(self.run_tool("rockusb", "--bcb-go", PAYLOAD_SHA, go=False), "NO_GO")
        self.assertExit(self.run_tool("rockusb", "--bcb-go", PAYLOAD_SHA, "--go-file",
                                      os.path.join(self.tmp, "nope"), go=False), "NO_GO")
        self.assertEqual(self.starts(), [])

    def test_go_file_content(self):                         # R2-3, probe P17
        cases = ["not a GO\n",
                 go_text(self.ab_sha, payload_sha="1" * 64),
                 go_text("2" * 64),
                 go_text(self.ab_sha, tsha="3" * 64),
                 go_text(self.ab_sha).replace("tool-sha256", "tool-sha"),
                 go_text(self.ab_sha) + "payload-sha256: %s\n" % PAYLOAD_SHA,
                 go_text(self.ab_sha).upper(),
                 ""]
        for text in cases:
            self.fresh()
            self.go_override = text
            self.assertExit(self.run_tool("rockusb"), "NO_GO")
            self.assertEqual(self.starts(), [], "a program ran with a GO file that does not name the session")
        self.fresh()
        self.go_override = "Opus GO, session 7\nnotes\n\n" + go_text(self.ab_sha) + "more notes\n"
        self.assertExit(self.run_tool("rockusb"), "OK")

    def test_payload_file_changed_before_write(self):      # F12
        self.config({"tamper_payload_on_guard": os.path.join(self.logdir, "bcb-payload.bin")})
        self.assertExit(self.run_tool("rockusb"), "INTERNAL")
        self.assertIn("SAFETY", self.out + self.log_text())
        self.assertNothingWritten()

    def test_maskrom_at_start(self):
        for route in ("rockusb", "dump"):
            self.set_mode("maskrom")
            self.fresh()
            self.assertExit(self.run_tool(route), "MASKROM")
            self.assertIn("STOP", self.out)
            self.assertEqual([a for p, a in self.starts() if p == "rkdeveloptool" and a[0] != "ld"], [],
                             "only ld may run when Maskrom is seen")
            self.assertNothingWritten()

    def test_maskrom_after_reset(self):
        self.config({"rd_maskrom": True})
        self.assertExit(self.run_tool("rockusb"), "MASKROM")
        self.assertNotBooted()

    def test_no_rockusb(self):
        self.set_mode("off")
        self.assertExit(self.run_tool("rockusb"), "NO_ROCKUSB")
        self.assertNothingWritten()

    def test_several_rockusb(self):
        self.config({"several": True})
        self.assertExit(self.run_tool("rockusb"), "AMBIGUOUS_DEVICE")
        self.assertNothingWritten()

    def test_fastboot_already_present(self):
        self.config({"fastboot_present": True})
        self.assertExit(self.run_tool("rockusb"), "AMBIGUOUS_DEVICE")
        self.assertNothingWritten()
        self.set_mode("mainline")
        self.fresh()
        self.assertExit(self.run_tool("ssh"), "AMBIGUOUS_DEVICE")
        self.assertEqual(self.ssh_remotes(), [])

    def test_no_fastboot_message(self):                     # F15
        self.config({"unit_no_fastboot": True})
        self.assertExit(self.run_tool("rockusb"), "NO_FASTBOOT")
        self.assertIn("when the reset was sent", self.out)
        self.assertNotIn("The BCB holds bootonce-bootloader", self.out)

    def test_crash_names_the_state(self):                   # F5 catch-all
        self.config({"chmod_fastboot_on_rd": self.fake["fastboot"]})
        rc = self.run_tool("rockusb")
        self.assertExit(rc, "CRASH")
        self.assertIn("when the reset was sent", self.log_text())
        self.assertNotBooted()


class EntryChecks(Base):
    """F1, F9: in fastboot, before the boot."""

    def test_uboot_changed_ab_on_entry(self):               # F1, probe P1
        for a, b in (((15, 7, 0), (14, 7, 0)), ((15, 0, 1), (14, 3, 0)), ((15, 0, 1), (0, 0, 0)),
                     ((14, 0, 1), (15, 0, 1))):
            self.fresh()
            self.set_mode("rockusb")
            self.set_sector(AB_LBA, ab_sector())
            for f in ("bcb_reads", "fb_count", "ld_count"):
                p = os.path.join(self.state, f)
                if os.path.exists(p):
                    os.unlink(p)
            self.config({"uboot_rewrite_ab": ab_sector(a=a, b=b).hex()})
            self.assertExit(self.run_tool("rockusb"), "AB_ENTRY_DIFF")
            self.assertIn("keep the unit in fastboot", self.out)
            self.assertIn(LEAVE, self.out)                          # R2-4
            self.assertIn(NEVER_REBOOT, self.out)
            self.assertNotBooted()

    def test_b_next_uboot_says_a(self):                     # probe P12
        sec = ab_sector(a=(14, 0, 1), b=(15, 0, 1))
        self.set_sector(AB_LBA, sec)
        self.config({"getvar_override": {"current-slot": "a"}})
        self.assertExit(self.run_tool("rockusb", ab_sha=hashlib.sha256(sec[:32]).hexdigest()), "AB_ENTRY_DIFF")
        self.assertIn(LEAVE, self.out)
        self.assertNotBooted()
        self.fresh()                                        # probe P12b
        self.set_mode("rockusb")
        self.set_sector(AB_LBA, sec)
        for f in ("bcb_reads", "fb_count", "ld_count"):
            p = os.path.join(self.state, f)
            if os.path.exists(p):
                os.unlink(p)
        self.config({"getvar_override": {"current-slot": "_b"}})
        self.assertExit(self.run_tool("rockusb", ab_sha=hashlib.sha256(sec[:32]).hexdigest()), "OK")

    def test_getvar_override_differs(self):                 # probe P1, slot flip
        for ov in ({"slot-successful:a": "no", "slot-retry-count:a": "7", "slot-successful:b": "no",
                    "slot-retry-count:b": "7"},
                   {"current-slot": "b"}, {"slot-unbootable:b": "yes"}, {"slot-retry-count:b": "1"},
                   {"slot-successful:a": "maybe"}):
            self.fresh()
            self.set_mode("rockusb")
            self.config({"getvar_override": ov})
            self.assertExit(self.run_tool("rockusb"), "AB_ENTRY_DIFF")
            self.assertNotBooted()

    def test_required_getvar_unreadable(self):              # F1, mutation M14
        for v in REQUIRED:
            self.fresh()
            self.set_mode("rockusb")
            self.config({"getvar_fail": [v]})
            self.assertExit(self.run_tool("rockusb"), "GETVAR_UNREADABLE")
            self.assertIn(v, self.out)
            self.assertIn(LEAVE, self.out)
            self.assertIn(NEVER_REBOOT, self.out)
            self.assertNotBooted()
        self.fresh()
        self.set_mode("rockusb")
        self.config({"getvar_override": {"current-slot": ""}})
        self.assertExit(self.run_tool("rockusb"), "GETVAR_UNREADABLE")

    def test_ssh_entry_differs(self):                       # F4, probe P1b
        self.set_mode("mainline")
        self.config({"getvar_override": {"slot-successful:a": "no", "slot-retry-count:a": "5"}})
        self.assertExit(self.run_tool("ssh"), "AB_ENTRY_DIFF")
        self.assertNotBooted()

    def test_foreign_fastboot_device(self):                 # F9, probe P3
        self.config({"unit_no_fastboot": True, "foreign_serial": "OTHER0001"})
        self.assertExit(self.run_tool("rockusb"), "NOT_THE_UNIT")
        self.assertEqual([a for p, a in self.starts() if p == "fastboot" and a[:1] == ["-s"]], [],
                         "a command went to the other device")

    def test_serialno_differs(self):                        # F9
        self.config({"getvar_override": {"serialno": "OTHER0001"}})
        self.assertExit(self.run_tool("rockusb"), "NOT_THE_UNIT")
        self.assertIn(LEAVE, self.out)
        self.assertIn(NEVER_REBOOT, self.out)
        self.assertNotBooted()

    def test_two_fastboot_devices(self):                    # mutation M6, probe P4
        self.config({"second_fastboot": True})
        self.assertExit(self.run_tool("rockusb"), "AMBIGUOUS_DEVICE")
        self.assertNotBooted()


ANDROID = " Android Fastboot"      # the interface text of the unit's U-Boot, fastboot 34.0.5 (M-4, M4-1)
NO_PERM = ("no permissions (missing udev rules? user is in the plugdev group); "
           "see [http://developer.android.com/tools/device.html]\tfastboot")
# Lines in a form the tool does not know: each must stop the check before anything is sent.
UNKNOWN_LINES = (NO_PERM, "????????????\tfastboot", "FAKE0009 fastboot", "FAKE0009\tAndroid Fastboot",
                 "FAKE0009\t Android Fastboot x", "FAKE0009\tfastboot\tx", "FAKE0009\tother",
                 "* daemon started", "FAKE0009", "FAKE0009 \tfastboot")


class FastbootDevices(Base):
    """M4-1: both measured forms of a `fastboot devices` line are a device; any other non-empty line
    stops the check before anything is sent, and in the wait never counts as "no device"."""
    def test_android_form_happy(self):
        for route in ("rockusb", "ssh"):
            self.fresh()
            self.set_sector(BCB_LBA, bytes(2048))
            self.set_mode("rockusb" if route == "rockusb" else "mainline")
            self.config({"fb_iface": ANDROID})
            self.assertExit(self.run_tool(route), "OK")
            self.assertEqual(self.boot_calls(), [["-s", SERIAL, "boot", os.path.join(self.logdir, "boot.img")]])
            self.assertEqual(self.mode(), "booted")

    def test_precheck_device_present_each_form(self):
        for iface in ("fastboot", ANDROID):
            for route in ("rockusb", "ssh"):
                self.fresh()
                self.set_mode("rockusb" if route == "rockusb" else "mainline")
                self.config({"fastboot_present": True, "fb_iface": iface})
                self.assertExit(self.run_tool(route), "AMBIGUOUS_DEVICE")
                self.assertIn("a fastboot device is already connected", self.out)
                self.assertNothingWritten()
                self.assertEqual(self.ssh_remotes(), [])
                self.assertEqual([a for p, a in self.starts() if p == "rkdeveloptool"], [])

    def test_precheck_unknown_line(self):
        for line in UNKNOWN_LINES:
            for route in ("rockusb", "ssh"):
                self.fresh()
                self.set_mode("rockusb" if route == "rockusb" else "mainline")
                self.config({"fb_extra_line": line})
                self.assertExit(self.run_tool(route), "AMBIGUOUS_DEVICE")
                self.assertIn("a line in an unknown form", self.out, line)
                self.assertIn(repr(line), self.out)
                self.assertNothingWritten()
                self.assertEqual(self.ssh_remotes(), [])
                self.assertEqual([a for p, a in self.starts() if p == "rkdeveloptool"], [])

    def test_precheck_fails(self):
        for route in ("rockusb", "ssh"):
            self.fresh()
            self.set_mode("rockusb" if route == "rockusb" else "mainline")
            self.config({"fb_rc": 1})
            self.assertExit(self.run_tool(route), "READ_FAILED")
            self.assertNothingWritten()
            self.assertEqual(self.ssh_remotes(), [])

    def test_empty_lines_are_not_devices(self):
        self.config({"fb_extra_line": "", "fb_iface": ANDROID})
        self.assertExit(self.run_tool("rockusb"), "OK")

    def test_wait_unknown_then_known(self):
        """A line in an unknown form in the wait (no permissions, before udev): wait on."""
        for route in ("rockusb", "ssh"):
            self.fresh()
            self.set_sector(BCB_LBA, bytes(2048))
            self.set_mode("rockusb" if route == "rockusb" else "mainline")
            self.config({"fb_unit_line": NO_PERM, "fb_unit_line_count": 3, "fb_iface": ANDROID})
            self.assertExit(self.run_tool(route), "OK")
            self.assertIn("a line in an unknown form", self.log_text())
            self.assertIn("waiting on", self.log_text())

    def test_wait_unknown_until_the_end(self):
        for line in (NO_PERM, "%s\tother", "%s fastboot"):
            for route in ("rockusb", "ssh"):
                self.fresh()
                self.set_sector(BCB_LBA, bytes(2048))
                self.set_mode("rockusb" if route == "rockusb" else "mainline")
                self.config({"fb_unit_line": line})
                self.assertExit(self.run_tool(route), "NO_FASTBOOT")
                self.assertIn(repr(line.replace("%s", SERIAL)), self.out)
                self.assertIn("may be the unit in fastboot", self.out)
                self.assertIn(LEAVE, self.out)
                self.assertIn(NEVER_REBOOT, self.out)
                self.assertEqual([a for p, a in self.starts() if p == "fastboot" and a[:1] == ["-s"]], [])

    def test_wait_unknown_next_to_the_unit(self):
        for iface in ("fastboot", ANDROID):
            for line in (NO_PERM, "FAKE0009\tother"):
                self.fresh()
                self.set_mode("rockusb")
                self.config({"fb_iface": iface, "fb_second_line": line})
                self.assertExit(self.run_tool("rockusb"), "AMBIGUOUS_DEVICE")
                self.assertIn("a line in an unknown form", self.out)
                self.assertIn(LEAVE, self.out)
                self.assertEqual([a for p, a in self.starts() if p == "fastboot" and a[:1] == ["-s"]], [],
                                 "a command went to a device")

    def test_two_devices_android_form(self):
        self.config({"second_fastboot": True, "fb_iface": ANDROID})
        self.assertExit(self.run_tool("rockusb"), "AMBIGUOUS_DEVICE")
        self.assertNotBooted()
        self.assertEqual([a for p, a in self.starts() if p == "fastboot" and a[:1] == ["-s"]], [])

    def test_foreign_device_android_form(self):
        self.config({"unit_no_fastboot": True, "foreign_serial": "OTHER0001", "fb_iface": ANDROID})
        self.assertExit(self.run_tool("rockusb"), "NOT_THE_UNIT")
        self.assertEqual([a for p, a in self.starts() if p == "fastboot" and a[:1] == ["-s"]], [])


class SshRefusals(Base):
    def setUp(self):
        Base.setUp(self)
        self.set_mode("mainline")

    def assertNoRestart(self):
        self.assertNotIn(REMOTE["reboot"], self.ssh_remotes())
        self.assertNotIn(REMOTE["reboot_dry"], self.ssh_remotes())
        self.assertEqual(self.mode(), "mainline")

    def test_restart_block(self):                           # F3
        cases = [{"unit_restart_block": "busy"}, {"unit_restart_block": "hold 3"},
                 {"unit_restart_block": ""},
                 {"unit_lock": "held"}, {"unit_lock": "noflock"}, {"unit_fields": None},   # R2-1
                 {"unit_fields": {"in_progress": "0"}}, {"unit_fields": {"post_update": ""}},
                 {"unit_fields": {"in_progress": "1", "post_update": ""}},
                 {"unit_fields": {"in_progress": "0", "post_update": "hold 2"}},
                 {"unit_fields": {"in_progress": "0", "post_update": "retry 1"}},
                 {"unit_fields": {"in_progress": "0", "post_update": "owed"}},
                 {"state_extra": "grep: /var/lib/tsx/autoupdate/fields: Permission denied"}]
        for c in cases:
            self.fresh()
            self.config(c)
            self.assertExit(self.run_tool("ssh"), "RESTART_BLOCKED")
            self.assertEqual(self.ssh_remotes()[-1], REMOTE["state_check"])
            self.assertNoRestart()
            if c.get("unit_restart_block") == "busy":
                self.assertIn("restart-block present: busy", self.out)       # mutation X2
            if c.get("unit_lock") == "held":
                self.assertIn("holds /run/tsx/autoupdate.lock", self.out)
            if c.get("unit_lock") == "noflock":
                self.assertIn("flock is missing", self.out)
            if "unit_fields" in c and c["unit_fields"] is None:
                self.assertIn("fields file /var/lib/tsx/autoupdate/fields is missing", self.out)

    def test_state_real_formats(self):                      # probe P15, with the lock line (R2-1)
        res = {}
        cases = (("normal (trailing space)", STATE_OK, "OK"),
                 ("in_progress empty", STATE_OK.replace("in_progress 0", "in_progress "), "OK"),
                 ("lock absent", STATE_OK.replace("lock free", "lock absent"), "OK"),
                 ("owed", STATE_OK.replace("post_update ", "post_update owed"), "RESTART_BLOCKED"),
                 ("retry 1", STATE_OK.replace("post_update ", "post_update retry 1"), "RESTART_BLOCKED"),
                 ("hold 40", STATE_OK.replace("post_update ", "post_update hold 40"), "RESTART_BLOCKED"),
                 ("in_progress 1", STATE_OK.replace("in_progress 0", "in_progress 1"), "RESTART_BLOCKED"),
                 ("busy file", STATE_OK.replace("restart-block absent", "restart-block present: busy"),
                  "RESTART_BLOCKED"),
                 ("empty file", STATE_OK.replace("restart-block absent", "restart-block present: "),
                  "RESTART_BLOCKED"),
                 ("lock held", STATE_OK.replace("lock free", "lock held"), "RESTART_BLOCKED"),
                 ("no lock line", STATE_OK.replace("lock free\n", ""), "RESTART_BLOCKED"),
                 ("two lock lines", "lock free\n" + STATE_OK, "RESTART_BLOCKED"),
                 ("fields absent", "lock free\nrestart-block absent\nfields absent\ncheck end\n",
                  "RESTART_BLOCKED"),
                 ("post_update missing", STATE_OK.replace("post_update \n", ""), "RESTART_BLOCKED"),
                 ("in_progress twice", STATE_OK.replace("in_progress 0", "in_progress 0\nin_progress 0"),
                  "RESTART_BLOCKED"))
        for name, raw, want in cases:
            self.fresh()
            self.set_mode("mainline")
            self.config({"state_raw": raw})
            res[name] = self.run_tool("ssh")
            self.assertExit(res[name], want)
            if want != "OK":
                self.assertNoRestart()

    def test_state_answer_cut_short(self):                  # R2-7, mutation X8
        for raw in ("lock free\nrestart-block absent\nfields present\nin_progress 0\n",
                    "lock free\nrestart-block absent\nfields present\nin_progress 0\npost_update \n",
                    STATE_OK + "extra after the end\n", ""):
            self.fresh()
            self.set_mode("mainline")
            self.config({"state_raw": raw})
            self.assertExit(self.run_tool("ssh"), "SSH_FAILED")
            self.assertIn("did not run to its end", self.out)
            self.assertNoRestart()

    def test_state_check_failed(self):
        self.config({"state_rc": 2})
        self.assertExit(self.run_tool("ssh"), "SSH_FAILED")
        self.assertNoRestart()

    def test_ssh_ab_not_ok(self):                           # F4
        for sec in (ab_sector(a=(15, 3, 0)), ab_sector(little=True), ab_sector(b=(5, 0, 0)),
                    bytes(512)):
            self.fresh()
            self.set_sector(AB_LBA, sec)
            self.assertExit(self.run_tool("ssh"), "AB_INVALID")
            self.assertNoRestart()

    def test_ssh_ab_read_failed(self):
        for c in ({"ssh_dd_rc": 1}, {"ssh_dd_short": True}):
            self.fresh()
            self.config(c)
            self.assertExit(self.run_tool("ssh"), "SSH_FAILED")
            self.assertNoRestart()

    def test_ssh_expect_ab_mismatch(self):
        self.assertExit(self.run_tool("ssh", ab_sha="2" * 64), "AB_NOT_EXPECTED")
        self.assertNoRestart()

    def test_ssh_dry_fails(self):
        self.config({"ssh_dry_rc": 127})
        self.assertExit(self.run_tool("ssh"), "SSH_FAILED")
        self.assertNotIn(REMOTE["reboot"], self.ssh_remotes())

    def test_ssh_dry_wrong_text(self):                      # mutation M3, probe P6
        self.config({"dry_output": "usage: uc-pxx-reboot [--dry-run] loader|fastboot"})
        self.assertExit(self.run_tool("ssh"), "SSH_FAILED")
        self.assertNotIn(REMOTE["reboot"], self.ssh_remotes())

    def test_ssh_reboot_fails(self):
        self.config({"ssh_rc": 1})
        self.assertExit(self.run_tool("ssh"), "SSH_FAILED")
        self.assertNotBooted()

    def test_no_fastboot(self):
        self.config({"ssh_noop": True})
        self.assertExit(self.run_tool("ssh"), "NO_FASTBOOT")
        self.assertNotBooted()

    def test_unit_not_reachable(self):
        self.set_mode("off")
        self.assertExit(self.run_tool("ssh"), "SSH_FAILED")
        self.assertEqual(self.ssh_remotes(), [REMOTE["state_check"]])


class ImageChecks(Base):
    def test_wrong_image_sha(self):
        for route in ("ssh", "rockusb"):
            self.fresh()
            self.assertExit(self.run_tool(route, sha="0" * 64), "IMAGE_SHA")
            self.assertEqual(self.starts(), [], "a device command ran with a wrong image")

    def test_missing_image(self):
        os.unlink(self.image)
        self.assertExit(self.run_tool("rockusb"), "IMAGE_SHA")
        self.assertEqual(self.starts(), [])

    def test_original_changed_after_check(self):            # F6, probe P2
        self.set_mode("mainline")
        self.config({"tamper_image_on_boot": self.image})
        self.assertExit(self.run_tool("ssh"), "OK")
        b = [e for e in self.calls() if e["ev"] == "boot-data"]
        self.assertEqual([e["sha256"] for e in b], [self.sha])

    def test_copy_changed_before_boot(self):                # F6
        self.set_mode("mainline")
        self.config({"tamper_copy_on_getvar": os.path.join(self.logdir, "boot.img")})
        self.assertExit(self.run_tool("ssh"), "IMAGE_SHA")
        self.assertNotBooted()


class Usage(Base):
    def test_log_dir_exists(self):
        os.makedirs(self.logdir)
        self.assertExit(self.run_tool("rockusb"), "USAGE")
        self.assertEqual(self.starts(), [])

    def test_bad_arguments(self):
        self.set_mode("mainline")
        cases = []
        for t in ("-oProxyCommand=x", "a b", "root@host;reboot"):
            a = self.argv("ssh")
            a[a.index("root@unit.invalid")] = t
            cases.append(a)
        for o in ("ProxyCommand x", "-oFoo=1", "A=b c"):
            a = self.argv("ssh")
            a[a.index("BatchMode=yes")] = o
            cases.append(a)
        for k, v in (("--wait-fastboot", "nan"), ("--wait-fastboot", "inf"), ("--poll-interval", "0"),
                     ("--expect-serialno", "a b"), ("--sha256", "xyz")):
            a = self.argv("ssh")
            a[a.index(k) + 1] = v
            cases.append(a)
        for a in cases:
            self.assertFalse(os.path.exists(self.logdir))
            self.assertExit(self.run_argv(a), "USAGE")
            self.assertEqual(self.starts(), [])


class NoAbort(Base):
    """A slow transfer is never cut short, by a timeout, a signal or a dead terminal."""

    def test_slow_write_not_aborted(self):
        self.config({"slow_wl": 4})
        a = self.argv("rockusb")
        a[a.index("--wait-rockusb") + 1] = "1"
        a[a.index("--wait-fastboot") + 1] = "1"
        self.assertExit(self.run_argv(a), "OK")
        wl = [e for e in self.calls() if e["prog"] == "rkdeveloptool" and e["argv"][:1] == ["wl"]]
        self.assertEqual([e["ev"] for e in wl], ["start", "end"])
        self.assertGreaterEqual(wl[1]["t"] - wl[0]["t"], 3.9)
        self.assertEqual(wl[1]["rc"], 0)

    def test_slow_boot_not_aborted(self):
        self.set_mode("mainline")
        self.config({"slow_boot": 4})
        a = self.argv("ssh")
        a[a.index("--wait-fastboot") + 1] = "1"
        self.assertExit(self.run_argv(a), "OK")
        b = [e for e in self.calls() if e["prog"] == "fastboot" and "boot" in e["argv"]
             and e["ev"] in ("start", "end")]
        self.assertEqual([e["ev"] for e in b], ["start", "end"])
        self.assertGreaterEqual(b[1]["t"] - b[0]["t"], 3.9)

    def _signal_during(self, route, match, slow, sig, close=None):
        """close: None, 'both' (stdout and stderr one pipe, closed by the reader), 'stdout'."""
        self.config(slow)
        err = subprocess.DEVNULL if close == "stdout" else subprocess.STDOUT
        p = subprocess.Popen([sys.executable, "-I", TOOL] + self.argv(route), env=self.env(),
                             cwd=self.tmp, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=err,
                             start_new_session=True, preexec_fn=dfl_signals)
        end = time.time() + 60
        while time.time() < end:
            if any(e["ev"] == "start" and match(e) for e in self.calls()):
                break
            time.sleep(0.05)
        else:
            p.kill()
            p.wait()
            self.fail("the command did not start")
        time.sleep(0.5)
        if close:
            p.stdout.close()          # like `| tee` that the same Ctrl-C ended, or a closed terminal
        os.killpg(p.pid, sig)          # like Ctrl-C: the whole process group of the tool
        rc = p.wait(timeout=120)
        self.out = "" if close else p.stdout.read().decode()
        if not close:
            p.stdout.close()
        return rc

    @staticmethod
    def is_wl(e):
        return e["prog"] == "rkdeveloptool" and e["argv"][:1] == ["wl"]

    @staticmethod
    def is_boot(e):
        return e["prog"] == "fastboot" and "boot" in e["argv"]

    def assertWriteThenReadbacks(self):
        ev = self.calls()
        wl = [e for e in ev if self.is_wl(e) and e["ev"] in ("start", "end")]
        self.assertEqual([e["ev"] for e in wl], ["start", "end"], "the write was cut short")
        after = [e for e in ev if e["t"] > wl[1]["t"] and e["ev"] == "start"]
        self.assertEqual([e["argv"][:2] for e in after], [["rl", "49184"], ["rl", "49156"]],
                         "after the write: read-back and A/B check only, then stop")
        self.assertEqual(self.get_sectors(BCB_LBA, 4), payload())
        self.assertEqual(self.rd_calls(), [])
        log = self.log_text()
        self.assertIn("BCB write verified by read-back", log)
        self.assertIn("STOP (exit 18", log)
        self.assertIn("the reset was not sent", log)

    def test_signals_during_write(self):                    # SIGINT, SIGHUP (F7), SIGQUIT (F5), SIGTERM
        for sig in (signal.SIGINT, signal.SIGHUP, signal.SIGQUIT, signal.SIGTERM):
            self.fresh()
            for f in ("bcb_reads", "fb_count", "ld_count", "calls.log"):
                p = os.path.join(self.state, f)
                if os.path.exists(p):
                    os.unlink(p)
            self.set_sector(BCB_LBA, bytes(2048))
            rc = self._signal_during("rockusb", self.is_wl, {"slow_wl": 2}, sig)
            self.assertExit(rc, "INTERRUPTED")
            self.assertWriteThenReadbacks()
            self.assertIn("the reset was not sent", self.out)

    def test_sigint_during_write_output_gone(self):         # F5, probes P8, P9
        for close in ("both", "stdout"):
            self.fresh()
            for f in ("bcb_reads", "fb_count", "ld_count", "calls.log"):
                p = os.path.join(self.state, f)
                if os.path.exists(p):
                    os.unlink(p)
            self.set_sector(BCB_LBA, bytes(2048))
            rc = self._signal_during("rockusb", self.is_wl, {"slow_wl": 2}, signal.SIGINT, close=close)
            self.assertExit(rc, "INTERRUPTED")
            self.assertWriteThenReadbacks()

    def test_signal_during_guard_read(self):                # mutation M9: the safe point before the write
        def is_guard(e):
            return (e["prog"] == "rkdeveloptool" and e["argv"][:3] == ["rl", "49184", "4"]
                    and e["argv"][3].endswith("bcb-guard.bin"))
        rc = self._signal_during("rockusb", is_guard, {"slow_guard_rl": 2}, signal.SIGINT)
        self.assertExit(rc, "INTERRUPTED")
        self.assertNothingWritten()
        self.assertIn("Nothing was written to the unit", self.out)

    def test_ctrl_z_during_ssh(self):                       # R2-5, probe P14
        """Ctrl-Z at the terminal goes to the tool's process group. The group must not be orphaned
        (else the kernel drops the stop): the tool runs in its own group of this test's session."""
        self.set_mode("mainline")
        self.config({"slow_state": 3})

        def own_group():
            dfl_signals()
            signal.signal(signal.SIGTSTP, signal.SIG_DFL)
            os.setpgid(0, 0)
        p = subprocess.Popen([sys.executable, "-I", TOOL] + self.argv("ssh"), env=self.env(), cwd=self.tmp,
                             stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                             preexec_fn=own_group)
        end = time.time() + 60
        while time.time() < end and not any(e["ev"] == "start" and e["prog"] == "ssh" for e in self.calls()):
            time.sleep(0.05)
        time.sleep(0.5)
        os.killpg(p.pid, signal.SIGTSTP)
        try:
            rc = p.wait(timeout=60)
        except subprocess.TimeoutExpired:
            os.killpg(p.pid, signal.SIGCONT)
            rc = p.wait(timeout=60)
            self.fail("the tool waited on a stopped ssh")
        self.out = p.stdout.read().decode()
        p.stdout.close()
        self.assertExit(rc, "OK")
        self.assertIn("Ctrl-Z is ignored", self.out)

    def test_signals_during_boot(self):                     # probe P11 and SIGTERM
        for sig, close in ((signal.SIGTERM, None), (signal.SIGINT, "both")):
            self.fresh()
            for f in ("fb_count", "calls.log"):
                p = os.path.join(self.state, f)
                if os.path.exists(p):
                    os.unlink(p)
            self.set_mode("mainline")
            rc = self._signal_during("ssh", self.is_boot, {"slow_boot": 2}, sig, close=close)
            self.assertExit(rc, "OK")
            b = [e for e in self.calls() if self.is_boot(e) and e["ev"] in ("start", "end")]
            self.assertEqual([e["ev"] for e in b], ["start", "end"], "fastboot boot was cut short")
            self.assertIn("fastboot boot done", self.log_text())


class DryRunAndDump(Base):
    def test_dry_run_rockusb(self):
        rc = self.run_tool("rockusb", "--dry-run")
        self.assertExit(rc, "OK")
        self.assertEqual(self.starts(), [], "the dry run ran a program")
        self.assertFalse(os.path.exists(self.logdir))
        dump = [ln for ln in self.out.splitlines() if ln[:8].isalnum() and ln[8:10] == "  "]
        self.assertEqual(len(dump), 128, "every byte: 128 lines of 16")
        self.assertTrue(dump[0].startswith("00000000  62 6f 6f 74 6f 6e 63 65 2d 62 6f 6f 74 6c 6f 61"))
        self.assertTrue(dump[1].startswith("00000010  64 65 72 00 00"))
        self.assertTrue(dump[127].startswith("000007f0  00 00"))
        rk = self.fake["rkdeveloptool"]
        self.assertIn("%s wl 49184 LOGDIR/bcb-payload.bin" % rk, self.out)
        self.assertIn("%s rl 49156 1 LOGDIR/ab-before.bin" % rk, self.out)
        self.assertIn("%s rd 0" % rk, self.out)
        self.assertNotIn("wl 49156", self.out)
        self.assertIn(PAYLOAD_SHA, self.out)
        self.assertIn("-s FAKE0001 boot LOGDIR/boot.img", self.out)
        self.assertIn("'SERIAL<TAB>fastboot' or 'SERIAL<TAB> Android Fastboot'), else exit 19", self.out)   # M4-1
        self.assertIn("a line in an unknown form: wait on, next to a device = exit 19", self.out)
        for v in GETVARS:
            self.assertIn("getvar " + v, self.out)

    def test_dry_run_ssh(self):
        rc = self.run_tool("ssh", "--dry-run")
        self.assertExit(rc, "OK")
        self.assertEqual(self.starts(), [])
        self.assertIn("/usr/sbin/uc-pxx-reboot fastboot", self.out)
        self.assertIn("'dd if=/dev/mmcblk0 bs=512 skip=49156 count=1'", self.out)
        self.assertIn("bytes written to the unit: none", self.out)
        self.assertNotIn(" wl ", self.out)
        self.assertIn("'SERIAL<TAB>fastboot' or 'SERIAL<TAB> Android Fastboot'), else exit 19", self.out)   # M4-1
        self.assertIn("a line in an unknown form: wait on, next to a device = exit 19", self.out)

    def test_dry_run_wrong_sha(self):
        self.assertExit(self.run_tool("ssh", "--dry-run", sha="0" * 64), "IMAGE_SHA")
        self.assertEqual(self.starts(), [])

    def test_dump(self):
        bcb = os.urandom(2048)
        self.set_sector(BCB_LBA, bcb)
        rc = self.run_tool("dump")
        self.assertExit(rc, "OK")
        self.assertEqual([a[:3] for p, a in self.starts()],
                         [["ld"], ["rl", "49156", "1"], ["rl", "49184", "4"]])
        with open(os.path.join(self.logdir, "bcb.bin"), "rb") as f:
            self.assertEqual(f.read(), bcb)
        self.assertIn(hashlib.sha256(bcb).hexdigest(), self.out)
        self.assertIn("NOT a known state", self.out)
        self.assertIn("Nothing was written", self.out)
        self.assertEqual(self.mode(), "rockusb")


class NeverWrites49156(unittest.TestCase):
    """LBA 49156 cannot be written: the only write command is built for 49184..49187, and the
    exec point refuses every other command."""

    def test_builders(self):
        self.assertEqual(P.rk_write_bcb_cmd("rk", "f", 2048), ["rk", "wl", "49184", "f"])
        self.assertEqual(P.rk_reset_cmd("rk"), ["rk", "rd", "0"])
        for n in (0, 512, 1536, 2560, 4096, 49156 * 512):
            with self.assertRaises(P.SafetyError):
                P.rk_write_bcb_cmd("rk", "f", n)
        for lba in (0, 64, 2047, 49152, 49153, 49155, 49156, 49157, 49183, 49185):
            with self.assertRaises(P.SafetyError):
                P.check_write_range(lba, 2048)
            with self.assertRaises(P.SafetyError):
                P.check_write_range(lba, 512)
        for verb in ("wl", "wlx", "wf", "db", "ul", "ef", "ppt", "gpt", "prm", "rcb", "rd", "rl"):
            with self.assertRaises(P.SafetyError):
                P.rk_cmd("rk", verb)
        for lba, n in ((49156, 2), (49152, 8), (0, 64), (49184, 1), (49184, 8)):
            with self.assertRaises(P.SafetyError):
                P.rk_read_cmd("rk", lba, n, "f")

    def test_constants(self):
        self.assertEqual(P.AB_LBA, 49156)
        self.assertEqual(P.BCB_LBA, 49184)
        self.assertEqual(P.BCB_SECTORS, 4)
        self.assertFalse(P.BCB_LBA <= P.AB_LBA < P.BCB_LBA + P.BCB_SECTORS)
        self.assertEqual(P.STATES_SHA256, STATES_SHA)
        self.assertEqual(P.REBOOT_CMD, REMOTE["reboot"])
        self.assertEqual(P.REBOOT_DRY_CMD, REMOTE["reboot_dry"])
        self.assertEqual(P.AB_READ_CMD, REMOTE["ab_read"])
        self.assertEqual(P.STATE_CHECK_CMD, REMOTE["state_check"])
        self.assertEqual(list(P.GETVARS), GETVARS)
        self.assertEqual(list(P.REQUIRED_VARS), REQUIRED)
        self.assertEqual(P.Runner.HELD, ("SIGINT", "SIGTERM", "SIGHUP", "SIGQUIT"))

    def test_remote_commands_read_only(self):
        for name, r in REMOTE.items():
            for bad in (">", "of=", "bootctl", "update-boot", "arm-slot", "flash", "tsx-autoupdate ",
                        "rm ", "mv ", "tee", " | ", "&&", "touch", "-c "):
                self.assertNotIn(bad, r, "%s holds %r" % (name, bad))
            self.assertEqual(r.count("flock"), 3 if name == "state_check" else 0)   # command -v, -n, the text
        self.assertIn("flock -n /run/tsx/autoupdate.lock true;", REMOTE["state_check"])
        self.assertIn("elif [ ! -e /run/tsx/autoupdate.lock ]", REMOTE["state_check"])
        self.assertTrue(REMOTE["ab_read"].startswith("dd if=/dev/mmcblk0 "))
        self.assertEqual([r for r in REMOTE.values() if "reboot" in r], [REMOTE["reboot_dry"], REMOTE["reboot"]])

    def test_source(self):
        """Static check of the source: one write command, at a constant, never 49156."""
        with open(TOOL) as f:
            src = f.read()
        tree = ast.parse(src)
        consts = [n for n in ast.walk(tree) if isinstance(n, ast.Constant)]
        wl = [n for n in consts if n.value == "wl"]
        self.assertEqual(len(wl), 1, "exactly one 'wl' literal")
        funcs = [n for n in ast.walk(tree) if isinstance(n, ast.FunctionDef)]
        owner = [f for f in funcs if any(c is wl[0] for c in ast.walk(f))]
        self.assertEqual([f.name for f in owner], ["rk_write_bcb_cmd"])
        lists = [n for n in ast.walk(owner[0]) if isinstance(n, ast.List) and any(c is wl[0] for c in n.elts)]
        self.assertEqual(len(lists), 1)
        self.assertEqual(ast.dump(lists[0].elts[2]), ast.dump(ast.parse("str(BCB_LBA)", mode="eval").body),
                         "the write LBA must be the constant BCB_LBA")
        rd = [n for n in consts if n.value == "rd"]
        self.assertEqual(len(rd), 2, "'rd' in rk_reset_cmd and in the exec check only")
        for name, val in (("BCB_LBA", 49184), ("AB_LBA", 49156), ("BCB_SECTORS", 4)):
            assigns = [n for n in ast.walk(tree) if isinstance(n, ast.Assign)
                       and any(isinstance(t, ast.Name) and t.id == name for t in n.targets)]
            self.assertEqual(len(assigns), 1, name)
            self.assertIsInstance(assigns[0].value, ast.Constant)
            self.assertEqual(assigns[0].value.value, val)
            self.assertIn(assigns[0], tree.body)
            stores = [n for n in ast.walk(tree) if isinstance(n, ast.Name) and n.id == name
                      and isinstance(n.ctx, ast.Store)]
            self.assertEqual(len(stores), 1, name + " is stored once")
        self.assertNotIn("global", [type(n).__name__.lower() for n in ast.walk(tree)])
        for n in ast.walk(tree):
            if isinstance(n, ast.Call):
                fname = getattr(n.func, "id", getattr(n.func, "attr", ""))
                if any(isinstance(a, ast.Name) and a.id == "AB_LBA" for a in n.args):
                    self.assertIn(fname, ("read_lba", "rk_read_cmd"), "AB_LBA passed to " + fname)
        self.assertEqual(len([n for n in consts if n.value == 49156]), 1)
        self.assertEqual(len([n for n in consts if n.value == "49156"]), 0)
        # the only process start is Runner.run (Popen), behind check_exec, with no timeout and no kill
        self.assertEqual(src.count("subprocess.Popen("), 1)
        run = [f for f in funcs if f.name == "run"][0]
        self.assertTrue(any(isinstance(c, ast.Attribute) and c.attr == "Popen" for c in ast.walk(run)))
        first = run.body[1] if isinstance(run.body[0], ast.Expr) else run.body[0]
        self.assertIn("check_exec", ast.dump(first), "check_exec must be the first statement of run()")
        for bad in ("subprocess.run(", "subprocess.call(", "check_output(", "os.system(", "os.popen(",
                    "timeout=", ".terminate(", "killpg(", "send_signal(", "p.kill(",
                    "shell=True", "os.exec", "os.spawn", "stdout=subprocess.PIPE", "communicate("):
            self.assertNotIn(bad, src, bad)
        kills = [n for n in ast.walk(tree) if isinstance(n, ast.Call)
                 and ast.dump(n.func) == ast.dump(ast.parse("os.kill", mode="eval").body)]
        self.assertEqual(len(kills), 1, "one os.kill, the SIGCONT to a stopped ssh")
        self.assertEqual(ast.dump(kills[0].args[1]), ast.dump(ast.parse("signal.SIGCONT", mode="eval").body))
        self.assertEqual(src.count(".kill("), 1)
        self.assertNotIn("all", P.GETVARS)
        for v in ("flash", "erase", "format", "oem", "flashing", "set_active", "reboot", "update",
                  "continue", "stage", "upload"):
            with self.assertRaises(P.SafetyError):
                P.fb_cmd("fb", "s", v, "x")
        with self.assertRaises(P.SafetyError):
            P.fb_cmd("fb", "s", "getvar", "all")
        with self.assertRaises(P.SafetyError):
            P.ssh_cmd("ssh", [], "t", "reboot")

    def test_exec_point(self):                              # F11, F12
        tmp = tempfile.mkdtemp(prefix="p10enter-exec-")
        try:
            class A:
                ssh_opt = ["BatchMode=yes"]
                target = "root@unit.invalid"
            r = P.Runner(A())
            r.logdir = tmp
            r.rk, r.fb, r.ssh = "/x/rk", "/x/fb", "/x/ssh"
            ok = [["/x/rk", "ld"], ["/x/rk", "rd", "0"],
                  ["/x/rk", "rl", "49156", "1", os.path.join(tmp, "ab-before.bin")],
                  ["/x/rk", "rl", "49184", "4", os.path.join(tmp, "bcb-after.bin")],
                  ["/x/fb", "devices"]] + \
                [P.ssh_cmd("/x/ssh", ["BatchMode=yes"], "root@unit.invalid", REMOTE[k]) for k in REMOTE]
            for c in ok:
                r.check_exec(c)
            bad = [["/x/rk", "rd", "3"], ["/x/rk", "rd"], ["/x/rk", "rl", "49156", "2", os.path.join(tmp, "ab.bin")],
                   ["/x/rk", "rl", "49152", "8", os.path.join(tmp, "ab.bin")],
                   ["/x/rk", "rl", "49156", "1", "/elsewhere/ab.bin"],
                   ["/x/rk", "wl", "49156", os.path.join(tmp, "bcb-payload.bin")],
                   ["/x/rk", "wl", "49184", os.path.join(tmp, "other.bin")],
                   ["/x/rk", "db", "loader.bin"], ["/x/rk", "ef"],
                   ["/x/fb", "-s", "S", "getvar", "current-slot"],          # no serial known yet
                   ["/x/fb", "getvar", "all"], ["/x/fb", "flash", "boot", "x"],
                   ["/x/ssh", "root@unit.invalid", REMOTE["reboot"]],         # no -n
                   P.ssh_cmd("/x/ssh", [], "root@unit.invalid", REMOTE["reboot"]),   # other options
                   ["/x/ssh", "-n", "root@unit.invalid", "reboot"],
                   ["/usr/bin/fastboot", "devices"], ["/x/other"]]
            for c in bad:
                with self.assertRaises(P.SafetyError, msg=repr(c)):
                    r.check_exec(c)
            # the write: only the 2048-byte payload file in the log folder
            pp = os.path.join(tmp, "bcb-payload.bin")
            wl = ["/x/rk", "wl", "49184", pp]
            with self.assertRaises(FileNotFoundError):
                r.check_exec(wl)
            for data in (payload() + bytes(2048), payload()[:2047] + b"x", b"x" * 2048):
                with open(pp, "wb") as f:
                    f.write(data)
                with self.assertRaises(P.SafetyError):
                    r.check_exec(wl)
            with open(pp, "wb") as f:
                f.write(payload())
            r.check_exec(wl)
            os.unlink(pp)
            os.symlink(os.path.join(tmp, "target"), pp)
            with open(os.path.join(tmp, "target"), "wb") as f:
                f.write(payload())
            with self.assertRaises(P.SafetyError):
                r.check_exec(wl)
            # fastboot after the serial is known: getvar of the list, boot of the copy only
            r.serial = "S"
            r.boot_image = os.path.join(tmp, "boot.img")
            with open(r.boot_image, "wb") as f:
                f.write(b"image")
            r.image_sha = hashlib.sha256(b"image").hexdigest()
            r.check_exec(["/x/fb", "-s", "S", "getvar", "current-slot"])
            r.check_exec(["/x/fb", "-s", "S", "boot", r.boot_image])
            for c in (["/x/fb", "-s", "S", "getvar", "all"], ["/x/fb", "-s", "T", "getvar", "product"],
                      ["/x/fb", "-s", "S", "boot", "/other.img"], ["/x/fb", "-s", "S", "oem", "x"]):
                with self.assertRaises(P.SafetyError, msg=repr(c)):
                    r.check_exec(c)
            with open(r.boot_image, "ab") as f:
                f.write(b"changed")
            with self.assertRaises(P.Stop):
                r.check_exec(["/x/fb", "-s", "S", "boot", r.boot_image])
            r.fb = "/x/rk"
            with self.assertRaises(P.SafetyError):
                r.check_exec(["/x/rk", "ld"])
        finally:
            shutil.rmtree(tmp, ignore_errors=True)

    def test_states_file(self):
        with open(STATES, "rb") as f:
            raw = f.read()
        self.assertEqual(hashlib.sha256(raw).hexdigest(), STATES_SHA)
        doc = json.loads(raw)
        shas = sorted(e["sha256"] for e in doc["states"])
        self.assertEqual(shas, sorted([
            "e5a00aa9991ac8a5ee3109844d84a55583bd20572ad3ffcd42792f3c36b183ad",   # 2048 zeros
            PAYLOAD_SHA]))
        self.assertEqual(hashlib.sha256(bytes(2048)).hexdigest(), shas[1])
        self.assertEqual(hashlib.sha256(payload()).hexdigest(), PAYLOAD_SHA)
        self.assertEqual(hashlib.sha256(payload()[:512]).hexdigest(),
                         "91f3806a509ae09a4673b3da4e3e25744b7c2cb1117fa3bf95d6a780ce233e14")
        self.assertEqual(P.bcb_payload(), payload())
        for e in doc["states"]:
            for bad in ("529bdb7b", "afaa05c4", "fecfe0e2"):
                self.assertFalse(e["sha256"].startswith(bad))
        P.load_states(STATES)

    def test_bad_states_file(self):                         # F13: the file is pinned
        with open(STATES) as f:
            orig = json.load(f)
        edits = []
        d = json.loads(json.dumps(orig))
        d["states"][1]["sha256"] = "0" * 64
        edits.append(d)
        d = json.loads(json.dumps(orig))
        d["note"] += " (edited)"
        edits.append(d)
        d = json.loads(json.dumps(orig))
        d["states"].append(dict(orig["states"][0], name="new", sha256="1" * 64))
        edits.append(d)
        for doc in edits:
            tmp = tempfile.mkdtemp(prefix="p10enter-states-")
            try:
                shutil.copy(TOOL, os.path.join(tmp, "p10-enter"))
                with open(os.path.join(tmp, "bcb-known-states.json"), "w") as f:
                    json.dump(doc, f)
                r = subprocess.run([sys.executable, "-I", os.path.join(tmp, "p10-enter"), "dump",
                                    "--rkdeveloptool", "/nonexistent/rk", "--log-dir", os.path.join(tmp, "log")],
                                   stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=60,
                                   env={"PATH": tmp})
                self.assertEqual(r.returncode, P.EXIT["INTERNAL"][0], r.stdout.decode())
                self.assertIn("pinned", r.stdout.decode())
                self.assertFalse(os.path.exists(os.path.join(tmp, "log")), "the tool went on")
            finally:
                shutil.rmtree(tmp, ignore_errors=True)


class Decoder(unittest.TestCase):
    def test_ab(self):
        s, n, e = P.ab_parse(ab_sector())
        self.assertEqual((n, e), (0, None))
        s, n, e = P.ab_parse(ab_sector(a=(15, 0, 1), b=(15, 0, 1)))
        self.assertEqual((n, e), (0, None), "a tie goes to slot a")
        s, n, e = P.ab_parse(ab_sector(a=(0, 0, 0), b=(15, 0, 1)))
        self.assertEqual((n, e), (1, None))
        self.assertIn("little-endian", P.ab_parse(ab_sector(little=True))[2])
        self.assertIn("illegal", P.ab_parse(ab_sector(b=(14, 1, 1)))[2])
        self.assertIn("not successful", P.ab_parse(ab_sector(a=(15, 7, 0)))[2])
        bad = bytearray(ab_sector())
        bad[20] = 1
        self.assertIn("reserved", P.ab_parse(bytes(bad))[2])

    def test_expected_getvars(self):
        s, n, _ = P.ab_parse(ab_sector(a=(15, 0, 1), b=(14, 3, 0)))
        self.assertEqual(P.expected_getvars(s, n), {
            "current-slot": "a", "slot-successful:a": "yes", "slot-successful:b": "no",
            "slot-unbootable:a": "no", "slot-unbootable:b": "no",
            "slot-retry-count:a": "0", "slot-retry-count:b": "3"})
        s, n, _ = P.ab_parse(ab_sector(a=(15, 0, 1), b=(0, 0, 0)))
        self.assertEqual(P.expected_getvars(s, n)["slot-unbootable:b"], "yes")
        s, n, _ = P.ab_parse(ab_sector(a=(15, 0, 1), b=(5, 0, 0)))
        self.assertIsNone(P.expected_getvars(s, n))
        self.assertEqual(P.norm_getvar("current-slot", " _A "), "a")

    def test_state_lines(self):
        ok = ["lock free", "restart-block absent", "fields present", "in_progress 0", "post_update ", "check end"]
        self.assertEqual(P.check_state_lines(ok), [])
        self.assertEqual(P.check_state_lines(["lock absent"] + ok[1:]), [])
        self.assertEqual(P.check_state_lines(ok[:3] + ["in_progress", "post_update", "check end"]), [])
        for bad in (["lock held"] + ok[1:], ["flock missing"] + ok[1:], ok[1:],
                    ["lock free", "restart-block absent", "fields absent", "check end"],
                    ["lock free", "restart-block present: busy", "fields present", "in_progress 0", "post_update ",
                     "check end"],
                    ["lock free", "fields present", "in_progress 0", "post_update ", "check end"],
                    ["lock free", "restart-block absent", "fields present", "in_progress 1", "post_update ",
                     "check end"],
                    ["lock free", "restart-block absent", "fields present", "in_progress 0", "post_update hold 2",
                     "check end"],
                    ["lock free", "restart-block absent", "fields present", "post_update ", "check end"],
                    ok[:-1] + ["something", "check end"]):
            self.assertNotEqual(P.check_state_lines(bad), [], bad)

    def test_fb_devices_lines(self):                         # M4-1
        self.assertEqual(P.parse_fb_devices(""), ([], []))
        self.assertEqual(P.parse_fb_devices("\n\n  \n"), ([], []))
        self.assertEqual(P.parse_fb_devices("FAKE0001\tfastboot\n"), (["FAKE0001"], []))
        # the measured bytes of fastboot 34.0.5 (serial replaced), with the empty line after it
        self.assertEqual(P.parse_fb_devices(b"FAKE0001\t Android Fastboot\n\n".decode()), (["FAKE0001"], []))
        self.assertEqual(P.parse_fb_devices("A-1.b_2\tfastboot\nB2\t Android Fastboot\n"), (["A-1.b_2", "B2"], []))
        for ln in UNKNOWN_LINES:
            self.assertEqual(P.parse_fb_devices(ln + "\n"), ([], [ln]), ln)
            self.assertEqual(P.parse_fb_devices("FAKE0001\tfastboot\n" + ln + "\n"), (["FAKE0001"], [ln]), ln)

    def test_exit_codes_distinct(self):
        codes = [c for c, _ in P.EXIT.values()]
        self.assertEqual(len(codes), len(set(codes)))
        self.assertEqual(P.EXIT["OK"][0], 0)


if __name__ == "__main__":
    unittest.main()
