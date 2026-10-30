#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""Tests of host/p10-enter with fake rkdeveloptool, fastboot and ssh.

    python3 -I host/tests/test_p10_enter.py [-v]

No real device is used: PATH starts with tests/fakebin, and the fakes keep a
fake eMMC in a temporary folder. After every test:
  - no fake logged FORBIDDEN (a write that is not the BCB, a write over LBA
    49156, getvar all, a verb the tool must not send);
  - no rkdeveloptool wl covered LBA 49156;
  - every fake command that started also ended (nothing was killed).
"""
import ast
import hashlib
import importlib.machinery
import importlib.util
import json
import os
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import unittest
import zlib

HERE = os.path.dirname(os.path.realpath(__file__))
HOST = os.path.dirname(HERE)
TOOL = os.path.join(HOST, "p10-enter")
FAKEBIN = os.path.join(HERE, "fakebin")
STATES = os.path.join(HOST, "bcb-known-states.json")
PAYLOAD_SHA = "892627cb4356d1d16c37bf243d700b5c9a0ab672d263de9ccd640c86b5cd6a5a"
AB_LBA, BCB_LBA = 49156, 49184


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


class Base(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="p10enter-test-")
        self.state = os.path.join(self.tmp, "state")
        os.makedirs(os.path.join(self.state, "sectors"))
        self.image = os.path.join(self.tmp, "installer.img")
        with open(self.image, "wb") as f:
            f.write(os.urandom(1 << 20))
        with open(self.image, "rb") as f:
            self.sha = hashlib.sha256(f.read()).hexdigest()
        self.set_sector(AB_LBA, ab_sector())
        self.set_mode("rockusb")
        self.config({})
        self.logdir = os.path.join(self.tmp, "log")

    def tearDown(self):
        try:
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
        finally:
            shutil.rmtree(self.tmp, ignore_errors=True)

    # fake unit state
    def set_mode(self, m):
        with open(os.path.join(self.state, "mode"), "w") as f:
            f.write(m)

    def mode(self):
        with open(os.path.join(self.state, "mode")) as f:
            return f.read().strip()

    def config(self, c):
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

    def env(self):
        e = dict(os.environ)
        e["PATH"] = FAKEBIN + os.pathsep + e.get("PATH", "")
        e["FAKE_STATE"] = self.state
        return e

    def argv(self, route, *extra, sha=None):
        a = [route]
        if route in ("ssh", "rockusb"):
            a += ["--image", self.image, "--sha256", sha or self.sha, "--wait-fastboot", "3"]
        if route == "ssh":
            a += ["--target", "root@unit.invalid", "--ssh-opt", "BatchMode=yes"]
        if route in ("rockusb", "dump"):
            a += ["--wait-rockusb", "3"]
        if route == "rockusb" and "--no-go" not in extra:
            a += ["--bcb-go", PAYLOAD_SHA]
        extra = [x for x in extra if x != "--no-go"]
        return a + ["--log-dir", self.logdir, "--poll-interval", "0.05"] + list(extra)

    def run_tool(self, route, *extra, sha=None):
        r = subprocess.run([sys.executable, "-I", TOOL] + self.argv(route, *extra, sha=sha),
                           env=self.env(), cwd=self.tmp, stdout=subprocess.PIPE,
                           stderr=subprocess.STDOUT, timeout=120)
        self.out = r.stdout.decode()
        return r.returncode

    def assertExit(self, rc, key):
        self.assertEqual(rc, P.EXIT[key][0], "want %s, output:\n%s" % (key, self.out))

    def wl_calls(self):
        return [a for p, a in self.starts() if p == "rkdeveloptool" and a[:1] == ["wl"]]

    def boot_calls(self):
        return [a for p, a in self.starts() if p == "fastboot" and "boot" in a]


class HappyPaths(Base):
    def test_ssh_happy(self):
        self.set_mode("mainline")
        rc = self.run_tool("ssh")
        self.assertExit(rc, "OK")
        st = self.starts()
        progs = [p for p, _ in st]
        self.assertNotIn("rkdeveloptool", [p for p, a in st if a[:1] in (["wl"], ["rl"], ["rd"])])
        ssh = [a for p, a in st if p == "ssh"]
        self.assertEqual(ssh, [["-o", "BatchMode=yes", "root@unit.invalid", "/usr/sbin/uc-pxx-reboot",
                                "--dry-run", "fastboot"],
                               ["-o", "BatchMode=yes", "root@unit.invalid", "/usr/sbin/uc-pxx-reboot",
                                "fastboot"]])
        self.assertEqual(st[0], ("fastboot", ["devices"]))   # nothing else connected first
        self.assertEqual(progs.index("ssh"), 1)
        self.assertEqual(self.boot_calls(), [["-s", "FAKE0001", "boot", self.image]])
        booted = [e for e in self.calls() if e["ev"] == "boot-data"]
        self.assertEqual([e["sha256"] for e in booted], [self.sha])
        self.assertEqual(self.mode(), "booted")
        self.assertTrue(os.path.exists(os.path.join(self.logdir, "p10-enter.log")))

    def test_ssh_happy_rc0(self):
        self.set_mode("mainline")
        self.config({"ssh_rc": 0})
        self.assertExit(self.run_tool("ssh"), "OK")

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
        wl = self.wl_calls()
        self.assertEqual(len(wl), 1)
        with open(os.path.join(self.logdir, "bcb-payload.bin"), "rb") as f:
            self.assertEqual(hashlib.sha256(f.read()).hexdigest(), PAYLOAD_SHA)
        with open(os.path.join(self.logdir, "bcb-after.bin"), "rb") as f:
            self.assertEqual(f.read(), payload())
        self.assertEqual(self.get_sectors(AB_LBA, 1), ab_sector(), "the A/B sector changed")
        self.assertEqual(self.boot_calls(), [["-s", "FAKE0001", "boot", self.image]])
        getvars = [a[3] for p, a in self.starts() if p == "fastboot" and "getvar" in a]
        self.assertEqual(getvars, list(P.GETVARS))
        self.assertEqual(self.mode(), "booted")

    def test_rockusb_slot_b_next(self):
        self.set_sector(AB_LBA, ab_sector(a=(14, 0, 1), b=(15, 0, 1)))
        self.config({"current_slot": "b"})
        self.assertExit(self.run_tool("rockusb"), "OK")

    def test_rockusb_bcb_already_command(self):
        self.set_sector(BCB_LBA, payload())
        self.assertExit(self.run_tool("rockusb"), "OK")
        self.assertEqual(self.wl_calls(), [], "no write when the BCB already holds the command")

    def test_rockusb_waits_for_device(self):
        self.config({"rockusb_after": 5})
        self.assertExit(self.run_tool("rockusb"), "OK")


class Refusals(Base):
    def assertNothingWritten(self):
        self.assertEqual(self.wl_calls(), [])
        self.assertEqual([a for p, a in self.starts() if a[:1] == ["rd"]], [])
        self.assertEqual(self.boot_calls(), [])

    def test_unknown_bcb_refused(self):
        for bcb in (os.urandom(2048), b"\xff" * 2048, bytes(512) + b"x" + bytes(1535)):
            self.set_sector(BCB_LBA, bcb)
            if os.path.exists(self.logdir):
                shutil.rmtree(self.logdir)
            rc = self.run_tool("rockusb")
            self.assertExit(rc, "BCB_UNKNOWN")
            self.assertIn("unknown BCB, run the read-only dump first", self.out)
            self.assertNothingWritten()
            self.assertEqual(self.get_sectors(BCB_LBA, 4), bcb)
            with open(os.path.join(self.logdir, "bcb-before.bin"), "rb") as f:
                self.assertEqual(f.read(), bcb)

    def test_readback_mismatch(self):
        self.config({"wl_corrupt": True})
        rc = self.run_tool("rockusb")
        self.assertExit(rc, "READBACK_MISMATCH")
        self.assertEqual(len(self.wl_calls()), 1)
        self.assertEqual([a for p, a in self.starts() if a[:1] == ["rd"]], [], "reset after a mismatch")
        self.assertEqual(self.boot_calls(), [])
        self.assertIn("Do not restart", self.out)
        self.assertEqual(self.get_sectors(AB_LBA, 1), ab_sector())

    def test_write_failed(self):
        self.config({"wl_rc": 1})
        self.assertExit(self.run_tool("rockusb"), "WRITE_FAILED")
        self.assertEqual([a for p, a in self.starts() if a[:1] == ["rd"]], [])
        self.assertEqual(self.boot_calls(), [])

    def test_ab_changed_by_write(self):
        self.config({"wl_touch_ab": True})
        self.assertExit(self.run_tool("rockusb"), "AB_CHANGED")
        self.assertEqual([a for p, a in self.starts() if a[:1] == ["rd"]], [])

    def test_maskrom_at_start(self):
        for route in ("rockusb", "dump"):
            self.set_mode("maskrom")
            if os.path.exists(self.logdir):
                shutil.rmtree(self.logdir)
            rc = self.run_tool(route)
            self.assertExit(rc, "MASKROM")
            self.assertIn("STOP", self.out)
            self.assertEqual([a for p, a in self.starts() if p == "rkdeveloptool" and a[0] != "ld"], [],
                             "only ld may run when Maskrom is seen")
            self.assertNothingWritten()

    def test_maskrom_after_reset(self):
        self.config({"rd_maskrom": True})
        self.assertExit(self.run_tool("rockusb"), "MASKROM")
        self.assertEqual(self.boot_calls(), [])

    def test_wrong_image_sha(self):
        for route in ("ssh", "rockusb"):
            if os.path.exists(self.logdir):
                shutil.rmtree(self.logdir)
            rc = self.run_tool(route, sha="0" * 64)
            self.assertExit(rc, "IMAGE_SHA")
            self.assertEqual(self.starts(), [], "a device command ran with a wrong image")

    def test_image_changed_before_boot(self):
        self.set_mode("mainline")
        self.config({"tamper_image_on_getvar": self.image})
        self.assertExit(self.run_tool("ssh"), "IMAGE_SHA")
        self.assertEqual(self.boot_calls(), [])

    def test_missing_image(self):
        os.unlink(self.image)
        self.assertExit(self.run_tool("rockusb"), "IMAGE_SHA")
        self.assertEqual(self.starts(), [])

    def test_ab_invalid(self):
        bad = [ab_sector(little=True),                      # CRC little-endian
               ab_sector(a=(15, 1, 1)),                      # illegal successful+tries
               ab_sector(a=(15, 3, 0)),                      # next slot only trying
               ab_sector(magic=b"\0AB1"),
               ab_sector(a=(0, 0, 0), b=(0, 0, 0))]          # no bootable slot
        for sec in bad:
            self.set_sector(AB_LBA, sec)
            if os.path.exists(self.logdir):
                shutil.rmtree(self.logdir)
            self.assertExit(self.run_tool("rockusb"), "AB_INVALID")
            self.assertNothingWritten()

    def test_slot_check(self):
        self.config({"flip_slot": True})
        rc = self.run_tool("rockusb")
        self.assertExit(rc, "SLOT_CHECK")
        self.assertEqual(self.boot_calls(), [])

    def test_no_go(self):
        rc = self.run_tool("rockusb", "--no-go")
        self.assertExit(rc, "NO_GO")
        self.assertEqual(self.starts(), [])
        rc = self.run_tool("rockusb", "--bcb-go", "1" * 64)
        self.assertExit(rc, "NO_GO")
        self.assertEqual(self.starts(), [])

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
        shutil.rmtree(self.logdir)
        self.assertExit(self.run_tool("ssh"), "AMBIGUOUS_DEVICE")
        self.assertEqual([p for p, _ in self.starts() if p == "ssh"], [])

    def test_ssh_dry_fails(self):
        self.set_mode("mainline")
        self.config({"ssh_dry_rc": 127})
        self.assertExit(self.run_tool("ssh"), "SSH_FAILED")
        self.assertEqual(len([p for p, _ in self.starts() if p == "ssh"]), 1)

    def test_ssh_reboot_fails(self):
        self.set_mode("mainline")
        self.config({"ssh_rc": 1})
        self.assertExit(self.run_tool("ssh"), "SSH_FAILED")
        self.assertEqual(self.boot_calls(), [])

    def test_no_fastboot(self):
        self.set_mode("mainline")
        self.config({"ssh_noop": True})
        self.assertExit(self.run_tool("ssh"), "NO_FASTBOOT")
        self.assertEqual(self.boot_calls(), [])

    def test_log_dir_exists(self):
        os.makedirs(self.logdir)
        self.assertExit(self.run_tool("rockusb"), "USAGE")
        self.assertEqual(self.starts(), [])

    def test_bad_target(self):
        self.set_mode("mainline")
        for t in ("-oProxyCommand=x", "a b", "root@host;reboot"):
            self.assertFalse(os.path.exists(self.logdir))
            a = self.argv("ssh")
            a[a.index("root@unit.invalid")] = t
            r = subprocess.run([sys.executable, "-I", TOOL] + a, env=self.env(),
                               stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=60)
            self.out = r.stdout.decode()
            self.assertExit(r.returncode, "USAGE")
            self.assertEqual([p for p, _ in self.starts() if p == "ssh"], [])


class NoAbort(Base):
    """A slow transfer is never cut short, by a timeout or by a signal."""

    def test_slow_write_not_aborted(self):
        self.config({"slow_wl": 4})
        a = self.argv("rockusb")
        a[a.index("--wait-rockusb") + 1] = "1"
        a[a.index("--wait-fastboot") + 1] = "1"
        r = subprocess.run([sys.executable, "-I", TOOL] + a, env=self.env(), stdout=subprocess.PIPE,
                           stderr=subprocess.STDOUT, timeout=120)
        self.out = r.stdout.decode()
        self.assertExit(r.returncode, "OK")
        ev = self.calls()
        wl = [e for e in ev if e["prog"] == "rkdeveloptool" and e["argv"][:1] == ["wl"]]
        self.assertEqual([e["ev"] for e in wl], ["start", "end"])
        self.assertGreaterEqual(wl[1]["t"] - wl[0]["t"], 3.9)
        self.assertEqual(wl[1]["rc"], 0)

    def test_slow_boot_not_aborted(self):
        self.set_mode("mainline")
        self.config({"slow_boot": 4})
        a = self.argv("ssh")
        a[a.index("--wait-fastboot") + 1] = "1"
        r = subprocess.run([sys.executable, "-I", TOOL] + a, env=self.env(), stdout=subprocess.PIPE,
                           stderr=subprocess.STDOUT, timeout=120)
        self.out = r.stdout.decode()
        self.assertExit(r.returncode, "OK")
        b = [e for e in self.calls() if e["prog"] == "fastboot" and "boot" in e["argv"]
             and e["ev"] in ("start", "end")]
        self.assertEqual([e["ev"] for e in b], ["start", "end"])
        self.assertGreaterEqual(b[1]["t"] - b[0]["t"], 3.9)

    def _signal_during(self, route, match, slow, sig):
        self.config(slow)
        p = subprocess.Popen([sys.executable, "-I", TOOL] + self.argv(route), env=self.env(),
                             cwd=self.tmp, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                             start_new_session=True)
        end = time.time() + 60
        while time.time() < end:
            if any(e["ev"] == "start" and match(e) for e in self.calls()):
                break
            time.sleep(0.05)
        else:
            p.kill()
            self.fail("the command did not start")
        time.sleep(0.5)
        os.killpg(p.pid, sig)          # like Ctrl-C: the whole process group of the tool
        out, _ = p.communicate(timeout=120)
        self.out = out.decode()
        return p.returncode

    def test_sigint_during_write(self):
        def is_wl(e):
            return e["prog"] == "rkdeveloptool" and e["argv"][:1] == ["wl"]
        rc = self._signal_during("rockusb", is_wl, {"slow_wl": 3}, signal.SIGINT)
        self.assertExit(rc, "INTERRUPTED")
        ev = self.calls()
        wl = [e for e in ev if is_wl(e)]
        self.assertEqual([e["ev"] for e in wl], ["start", "end"], "the write was cut short")
        after = [e for e in ev if e["t"] > wl[1]["t"] and e["ev"] == "start"]
        self.assertEqual([e["argv"][:2] for e in after], [["rl", "49184"], ["rl", "49156"]],
                         "after the write: read-back and A/B check only, then stop")
        self.assertEqual(self.get_sectors(BCB_LBA, 4), payload())
        self.assertIn("A/B sector was not written", self.out)

    def test_sigterm_during_boot(self):
        self.set_mode("mainline")

        def is_boot(e):
            return e["prog"] == "fastboot" and "boot" in e["argv"]
        rc = self._signal_during("ssh", is_boot, {"slow_boot": 3}, signal.SIGTERM)
        self.assertExit(rc, "OK")
        b = [e for e in self.calls() if is_boot(e) and e["ev"] in ("start", "end")]
        self.assertEqual([e["ev"] for e in b], ["start", "end"], "fastboot boot was cut short")


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
        self.assertIn("rkdeveloptool wl 49184 LOGDIR/bcb-payload.bin", self.out)
        self.assertIn("rkdeveloptool rl 49156 1 LOGDIR/ab-before.bin", self.out)
        self.assertNotIn("wl 49156", self.out)
        self.assertIn(PAYLOAD_SHA, self.out)
        self.assertIn("fastboot -s SERIAL boot " + self.image, self.out)

    def test_dry_run_ssh(self):
        rc = self.run_tool("ssh", "--dry-run")
        self.assertExit(rc, "OK")
        self.assertEqual(self.starts(), [])
        self.assertIn("ssh -o BatchMode=yes root@unit.invalid /usr/sbin/uc-pxx-reboot fastboot", self.out)
        self.assertIn("bytes written to the unit: none", self.out)
        self.assertNotIn(" wl ", self.out)

    def test_dry_run_wrong_sha(self):
        self.assertExit(self.run_tool("ssh", "--dry-run", sha="0" * 64), "IMAGE_SHA")
        self.assertEqual(self.starts(), [])

    def test_dump(self):
        bcb = os.urandom(2048)
        self.set_sector(BCB_LBA, bcb)
        rc = self.run_tool("dump")
        self.assertExit(rc, "OK")
        self.assertEqual([a[0] for p, a in self.starts()], ["ld", "rl", "rl"])
        with open(os.path.join(self.logdir, "bcb.bin"), "rb") as f:
            self.assertEqual(f.read(), bcb)
        self.assertIn(hashlib.sha256(bcb).hexdigest(), self.out)
        self.assertIn("NOT a known state", self.out)
        self.assertIn("Nothing was written", self.out)
        self.assertEqual(self.mode(), "rockusb")


class NeverWrites49156(unittest.TestCase):
    """LBA 49156 cannot be written: the only write command is built for 49184..49187."""

    def test_builder_refuses_everything_but_the_bcb(self):
        self.assertEqual(P.rk_write_bcb_cmd("rk", "f", 2048), ["rk", "wl", "49184", "f"])
        for n in (0, 512, 1536, 2560, 4096, 49156 * 512):
            with self.assertRaises(P.SafetyError):
                P.rk_write_bcb_cmd("rk", "f", n)
        for lba in (0, 64, 2047, 49152, 49153, 49155, 49156, 49157, 49183, 49185):
            with self.assertRaises(P.SafetyError):
                P.check_write_range(lba, 2048)
            with self.assertRaises(P.SafetyError):
                P.check_write_range(lba, 512)
        for verb in ("wl", "wlx", "wf", "db", "ul", "ef", "ppt", "gpt", "prm", "rcb"):
            with self.assertRaises(P.SafetyError):
                P.rk_cmd("rk", verb, 49156, "f")

    def test_constants(self):
        self.assertEqual(P.AB_LBA, 49156)
        self.assertEqual(P.BCB_LBA, 49184)
        self.assertEqual(P.BCB_SECTORS, 4)
        self.assertFalse(P.BCB_LBA <= P.AB_LBA < P.BCB_LBA + P.BCB_SECTORS)

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
        f = owner[0]
        lists = [n for n in ast.walk(f) if isinstance(n, ast.List) and any(c is wl[0] for c in n.elts)]
        self.assertEqual(len(lists), 1)
        lba = lists[0].elts[2]
        self.assertEqual(ast.dump(lba), ast.dump(ast.parse("str(BCB_LBA)", mode="eval").body),
                         "the write LBA must be the constant BCB_LBA")
        # BCB_LBA and AB_LBA are assigned once, at module level, to constants
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
        # AB_LBA is only an argument of rk_read_cmd, or a comparison in check_write_range
        for n in ast.walk(tree):
            if isinstance(n, ast.Call):
                fname = getattr(n.func, "id", getattr(n.func, "attr", ""))
                if any(isinstance(a, ast.Name) and a.id == "AB_LBA" for a in n.args):
                    self.assertIn(fname, ("read_lba", "rk_read_cmd"), "AB_LBA passed to " + fname)
        # no number 49156 other than the constant
        self.assertEqual(len([n for n in consts if n.value == 49156]), 1)
        self.assertEqual(len([n for n in consts if n.value == "49156"]), 0)
        # the only process start is Runner.run (Popen), with no timeout and no kill
        self.assertEqual(src.count("subprocess.Popen("), 1)
        for bad in ("subprocess.run(", "subprocess.call(", "check_output(", "os.system(", "os.popen(",
                    "timeout=", ".kill(", ".terminate(", "os.kill(", "killpg(", "send_signal(",
                    "shell=True", "os.exec", "os.spawn"):
            self.assertNotIn(bad, src, bad)
        # fastboot: only devices, getvar of the fixed list, boot
        self.assertEqual(P.FB_VERBS, ("devices", "getvar", "boot"))
        self.assertNotIn("all", P.GETVARS)
        for v in ("flash", "erase", "format", "oem", "flashing", "set_active", "reboot", "update",
                  "continue", "stage", "upload"):
            with self.assertRaises(P.SafetyError):
                P.fb_cmd("fb", "s", v, "x")
        with self.assertRaises(P.SafetyError):
            P.fb_cmd("fb", "s", "getvar", "all")

    def test_states_file(self):
        with open(STATES) as f:
            doc = json.load(f)
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

    def test_bad_states_file(self):
        tmp = tempfile.mkdtemp(prefix="p10enter-states-")
        try:
            shutil.copy(TOOL, os.path.join(tmp, "p10-enter"))
            with open(STATES) as f:
                doc = json.load(f)
            doc["states"][1]["sha256"] = "0" * 64     # a command state that is not the payload
            with open(os.path.join(tmp, "bcb-known-states.json"), "w") as f:
                json.dump(doc, f)
            r = subprocess.run([sys.executable, "-I", os.path.join(tmp, "p10-enter"), "dump",
                                "--log-dir", os.path.join(tmp, "log")],
                               stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=60,
                               env=dict(os.environ, PATH=FAKEBIN + os.pathsep + os.environ["PATH"],
                                        FAKE_STATE=tmp))
            self.assertEqual(r.returncode, P.EXIT["INTERNAL"][0], r.stdout.decode())
            self.assertFalse(os.path.exists(os.path.join(tmp, "calls.log")), "a program ran")
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

    def test_exit_codes_distinct(self):
        codes = [c for c, _ in P.EXIT.values()]
        self.assertEqual(len(codes), len(set(codes)))
        self.assertEqual(P.EXIT["OK"][0], 0)


if __name__ == "__main__":
    unittest.main()
