#!/usr/bin/python3 -I
# SPDX-License-Identifier: GPL-2.0-or-later
"""The tests of uc-pxx-webdrive. They never reach a USB device:

- Unit: the wrapper's checks (sysfs device rules, files, the GO file and its
  ledger, the docker argv) with fake folders; no docker.
- E2E: the wrapper's main() with a test Env: a fake sysfs, /dev/null in the
  place of the usbfs node, test pins and a small test set; the real pinned
  container, the real page copy (lan-site.sh with the test pins), the real
  headless Chromium and policy, and test/fakexfer.js in the place of
  navigator.usb (a model of internal/xfer). Run on the build host:
      python3 -I tools/webusb-driver/test/test_webdrive.py [-k NAME]
  WD_HOME= the folder that holds node_modules (setup), default ~/uc-pxx/webusb-driver/home.
- Real-pins dry runs (WD_REAL=1): the production pins, the real page copy, the
  real RC-7 payload (WD_PAYLOAD=) and finish set (WD_FINISH=), --dry-run, the
  fake navigator.usb only for the page's device list.
"""
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
import threading
import time
import types
import unittest

HERE = os.path.dirname(os.path.realpath(__file__))
TOOL = os.path.dirname(HERE)
REPO = os.path.dirname(os.path.dirname(TOOL))


def load_tool():
    loader = importlib.machinery.SourceFileLoader("webdrive", os.path.join(TOOL, "uc-pxx-webdrive"))
    spec = importlib.util.spec_from_loader("webdrive", loader)
    m = importlib.util.module_from_spec(spec)
    loader.exec_module(m)
    return m


W = load_tool()
sha = lambda b: hashlib.sha256(b).hexdigest()  # noqa: E731


def det_bytes(n, seed):
    out = bytearray(n)
    x = seed
    for i in range(n):
        x = (x * 1103515245 + 12345) & 0xFFFFFFFF
        out[i] = x >> 24
    return bytes(out)


def mk_sysfs(root, devs):
    """devs: [(name, vid, pid, bus, dev, serial, product, maker)]"""
    os.makedirs(root, exist_ok=True)
    for name, vid, pid, bus, dev, serial, product, maker in devs:
        d = os.path.join(root, name)
        os.makedirs(d)
        for k, v in (("idVendor", vid), ("idProduct", pid), ("busnum", str(bus)), ("devnum", str(dev)), ("serial", serial),
                     ("product", product), ("manufacturer", maker)):
            if v is not None:
                with open(os.path.join(d, k), "w") as f:
                    f.write(v + "\n")
        os.makedirs(os.path.join(root, name + ":1.0"))


XFER = ("1-1", "1d6b", "0105", 1, 5, "UC-PXX-STEP2", "UC-P10 mainline step 2", "uc-pxx")
HUB = ("usb1", "1d6b", "0002", 1, 1, "0000:00:14.0", "xHCI Host Controller", "Linux")


class FakeStat:
    def __init__(self, bus, dev, gid=46, major=189, chr_=True):
        self.st_mode = (stat.S_IFCHR if chr_ else stat.S_IFREG) | 0o660
        self.st_rdev = os.makedev(major, (bus - 1) * 128 + (dev - 1))
        self.st_gid = gid


class TestEnv(W.Env):
    def __init__(self, base, sysfs, pins, scen=None, test=True, allow_fake=True):
        self.base, self.sysfs, self._pins, self.scen, self.test, self.allow_fake = base, sysfs, pins, scen, test, allow_fake
        self.argvs = []
        self.after_container = None

    def pins_path(self):
        return self._pins

    def node_path(self, bus, dev):
        return "/dev/null"

    def stat_node(self, p):
        return FakeStat(1, 5)

    def access_node(self, p):
        return True

    def pgrep(self, name):
        return []

    def driver_args(self, mode, i):
        a = [] if not self.test else ["--pins", "/test/pins.json"]
        if self.scen:
            s = self.scen[i - 1] if isinstance(self.scen, list) else self.scen
            a += ["--fake", "/test/fakexfer.js", "--scenario", f"/test/{s}.json"]
        return a

    def docker_args(self, mode, i):
        return ["--mount", f"type=bind,src={self.base}/t,dst=/test,readonly", "--memory", os.environ.get("WD_MEM", "2g")]


# ------------------------------------------------------------------ unit tests
class Unit(unittest.TestCase):
    def setUp(self):
        self.d = tempfile.mkdtemp(prefix="wdu-")
        self.pins = W.load_pins(W.Env())

    def tearDown(self):
        shutil.rmtree(self.d, ignore_errors=True)

    def env(self, devs):
        root = os.path.join(self.d, "sysfs")
        mk_sysfs(root, devs)
        return TestEnv(self.d, root, os.path.join(TOOL, "pins.json"))

    def test_one_device(self):
        e = self.env([HUB, XFER])
        d = W.pick_device(e, W.find_devices(e), self.pins)
        self.assertEqual((d["name"], d["node"], d["gid"]), ("1-1", "/dev/null", 46))

    def test_no_device(self):
        e = self.env([HUB])
        with self.assertRaises(W.Fail) as c:
            W.pick_device(e, W.find_devices(e), self.pins)
        self.assertEqual(c.exception.code, W.EXIT_LINK)

    def test_two_devices(self):
        e = self.env([HUB, XFER, ("1-2",) + XFER[1:3] + (1, 6) + XFER[5:]])
        with self.assertRaises(W.Fail) as c:
            W.pick_device(e, W.find_devices(e), self.pins)
        self.assertEqual(c.exception.code, W.EXIT_UNEXPECTED)

    def test_other_unit_ids(self):
        for other in (("2-1", "2207", "330c", 2, 3, None, None, None), ("2-1", "18d1", "d00d", 2, 3, "BBE4ZZDT24", "x", "y"),
                      ("2-1", "18d1", "4ee7", 2, 3, "x", "x", "y"), ("2-1", "1d6b", "0104", 2, 3, "x", "x", "y")):
            e = self.env([HUB, XFER, other])
            with self.assertRaises(W.Fail) as c:
                W.pick_device(e, W.find_devices(e), self.pins)
            self.assertEqual(c.exception.code, W.EXIT_UNEXPECTED, other)
            shutil.rmtree(os.path.join(self.d, "sysfs"))

    def test_identity_strings(self):
        for i, bad in ((5, "OTHER"), (6, "FunctionFS Gadget"), (7, "Linux")):
            x = list(XFER)
            x[i] = bad
            e = self.env([HUB, tuple(x)])
            with self.assertRaises(W.Fail) as c:
                W.pick_device(e, W.find_devices(e), self.pins)
            self.assertEqual(c.exception.code, W.EXIT_UNEXPECTED)
            shutil.rmtree(os.path.join(self.d, "sysfs"))

    def test_node_checks(self):
        e = self.env([XFER])
        for st in (FakeStat(1, 5, major=1), FakeStat(1, 6), FakeStat(1, 5, chr_=False)):
            e.stat_node = lambda p, st=st: st
            with self.assertRaises(W.Fail) as c:
                W.pick_device(e, W.find_devices(e), self.pins)
            self.assertEqual(c.exception.code, W.EXIT_LOCAL)
        e.stat_node = lambda p: FakeStat(1, 5)
        e.access_node = lambda p: False
        with self.assertRaises(W.Fail):
            W.pick_device(e, W.find_devices(e), self.pins)

    def files(self, name, specs):
        d = os.path.join(self.d, name)
        os.makedirs(d)
        out = []
        for n, b in specs:
            with open(os.path.join(d, n), "wb") as f:
                f.write(b)
            out.append([n, len(b), sha(b)])
        return d, out

    def test_check_exact(self):
        d, spec = self.files("uc-pxx-finish", [("a", b"1"), ("b", b"22")])
        W.check_exact(d, spec, "uc-pxx-finish")
        with self.assertRaises(W.Fail):
            W.check_exact(d, spec, "uc-pxx-mainline")  # the folder name
        with self.assertRaises(W.Fail):
            W.check_exact(d, [spec[0], ["b", 2, "0" * 64]], "uc-pxx-finish")
        with open(os.path.join(d, "extra"), "w") as f:
            f.write("x")
        with self.assertRaises(W.Fail):
            W.check_exact(d, spec, "uc-pxx-finish")
        os.unlink(os.path.join(d, "extra"))
        os.unlink(os.path.join(d, "b"))
        os.symlink(os.path.join(d, "a"), os.path.join(d, "b"))
        with self.assertRaises(W.Fail):
            W.check_exact(d, [spec[0], ["b", 1, sha(b"1")]], "uc-pxx-finish")  # a link is never followed

    def test_check_site_and_tree(self):
        d, spec = self.files("site", [("index.html", b"<p>"), ("x.js", b"1")])
        site = {n: h for n, _, h in spec}
        W.check_site(d, site)
        os.makedirs(os.path.join(d, "js"))
        with open(os.path.join(d, "js", "more.js"), "w") as f:
            f.write("2")
        with self.assertRaises(W.Fail):
            W.check_site(d, site)
        os.symlink("/etc/passwd", os.path.join(d, "js", "l"))
        with self.assertRaises(W.Fail):
            W.tree_sha(d)

    def go(self, text):
        p = os.path.join(self.d, f"go-{len(os.listdir(self.d))}.txt")
        with open(p, "w") as f:
            f.write(text)
        return p

    def go_text(self, me, **over):
        v = {"session": "U2-5 test", "webdrive-sha256": me, "a-payload-manifest-sha256": self.pins["payload"]["files"][0][2],
             "a-rootfs-sha256": self.pins["payload"]["files"][2][2], "finish-list-sha256": self.pins["finish"]["files"][0][2], "verdict": "GO"}
        v.update(over)
        return "".join(f"{k}: {x}\n" for k, x in v.items() if x is not None)

    def test_go_file(self):
        me = "a" * 64
        W.check_go(self.go(self.go_text(me)), me, self.pins)
        for over in ({"verdict": "NO-GO"}, {"verdict": None}, {"webdrive-sha256": "b" * 64}, {"a-payload-manifest-sha256": "0" * 64},
                     {"a-rootfs-sha256": None}, {"finish-list-sha256": "1" * 64}):
            with self.assertRaises(W.Fail, msg=over):
                W.check_go(self.go(self.go_text(me, **over)), me, self.pins)
        with self.assertRaises(W.Fail):
            W.check_go(self.go(self.go_text(me) + "verdict: GO\n"), me, self.pins)  # twice
        with self.assertRaises(W.Fail):
            W.check_go(self.go(b"\xff\xfe".decode("latin1")), me, self.pins)

    def test_ledger(self):
        W.ledger_check(self.d, "x" * 64, "step2")
        W.ledger_add(self.d, "x" * 64, "step2", "/r")
        with self.assertRaises(W.Fail):
            W.ledger_check(self.d, "x" * 64, "step2")
        W.ledger_check(self.d, "x" * 64, "finish")  # one use per mode
        W.ledger_check(self.d, "y" * 64, "step2")

    def test_argv(self):
        e = self.env([XFER])
        dev = W.pick_device(e, W.find_devices(e), self.pins)
        e.docker_args = lambda m, i: []
        e.driver_args = lambda m, i: []
        for mode, dry in (("status", False), ("step2", False), ("finish", True)):
            a = W.docker_argv(e, self.pins, "/h", "/r", "n", mode, dry, "/p/uc-pxx-mainline", "/f/uc-pxx-finish", None if dry else dev)
            s = " ".join(a)
            self.assertIn("--network none", s)
            self.assertIn("--read-only", s)
            self.assertIn("--cap-drop ALL", s)
            self.assertEqual(a.count("--device"), 0 if dry else 1)
            if not dry:
                self.assertIn("/dev/null:/dev/null:rw", s)
            self.assertIn(self.pins["image"]["ref"], a)
            self.assertNotIn("--privileged", s)
            self.assertNotIn("--network host", s)
            for m in [x for i, x in enumerate(a) if i and a[i - 1] == "--mount"]:
                if ",dst=/out" not in m and ",dst=/scratch" not in m:
                    self.assertTrue(m.endswith(",readonly"), m)
            self.assertEqual("/payload/uc-pxx-mainline" in s, mode == "step2")
            self.assertEqual("/finish/uc-pxx-finish" in s, mode == "finish")
            self.assertEqual(a[-1] == "--dry-run", dry)

    def test_self_check(self):
        me = W.self_check()
        self.assertEqual(sorted(me["files"]), sorted(W.RUNTIME))

    def test_usage(self):
        self.assertEqual(W.main(["step2"]), W.EXIT_USAGE)  # --payload is required
        self.assertEqual(W.main(["bogus"]), W.EXIT_USAGE)
        self.assertEqual(W.main(["watch", "--every", "1"]), W.EXIT_USAGE)


# ------------------------------------------------------------------ e2e tests
def lan_site(out, manifest_sha, finish_sha):
    subprocess.run(["sh", os.path.join(REPO, "web", "test", "lan-site.sh"), out, "http://127.0.0.1:8137/payload/uc-pxx-mainline/",
                    manifest_sha, finish_sha], check=True, stdout=subprocess.DEVNULL)


def site_pins(d):
    out = {}
    for rel in W.walk_files(d):
        out[rel] = W.sha_file(os.path.join(d, rel))[0]
    return out


class E2E(unittest.TestCase):
    """Each test is one or more runs of the wrapper's main() with a fake unit."""

    @classmethod
    def setUpClass(cls):
        cls.base = tempfile.mkdtemp(prefix="wde-", dir=os.environ.get("WD_TMP") or None)
        b = cls.base
        os.makedirs(os.path.join(b, "t"))
        home = os.path.join(b, "home")
        os.makedirs(home)
        src = os.path.expanduser(os.environ.get("WD_HOME", "~/uc-pxx/webusb-driver/home"))
        subprocess.run(["cp", "-a", os.path.join(src, "node_modules"), os.path.join(home, "node_modules")], check=True)
        cls.home = home
        # the test set
        tmpl, root = det_bytes(34816, 3), det_bytes(3 * 1048576 + 4096, 4)
        man = json.dumps({"schema": 2, "kind": "uc-pxx-mainline", "version": "0.0.0-test", "model": "UC-P10", "token": "ucpxx-5a454728",
                          "boot_template": {"file": "boot-template.img", "size": len(tmpl), "sha256": sha(tmpl)},
                          "rootfs": {"file": "rootfs.ext4", "size": len(root), "sha256": sha(root), "label": "tsxroot"}}, indent=2).encode()
        fin, r2 = det_bytes(2 * 1048576 + 333, 5), det_bytes(777976, 6)
        flist = json.dumps({"kind": "uc-pxx-finish", "version": 1, "files": [{"name": "uc-pxx-step2-finish", "size": len(fin), "sha256": sha(fin)},
                            {"name": "uc-pxx-bootctl-inst", "size": len(r2), "sha256": sha(r2)}]}, separators=(",", ":")).encode() + b"\n"
        cls.pay = os.path.join(b, "set", "uc-pxx-mainline")
        cls.fin = os.path.join(b, "set", "uc-pxx-finish")
        os.makedirs(cls.pay)
        os.makedirs(cls.fin)
        for d, n, x in ((cls.pay, "manifest.json", man), (cls.pay, "boot-template.img", tmpl), (cls.pay, "rootfs.ext4", root),
                        (cls.fin, "finish.json", flist), (cls.fin, "uc-pxx-step2-finish", fin), (cls.fin, "uc-pxx-bootctl-inst", r2)):
            with open(os.path.join(d, n), "wb") as f:
                f.write(x)
        cls.msha, cls.fsha = sha(man), sha(flist)
        lan_site(os.path.join(home, "site"), cls.msha, cls.fsha)
        P = W.load_pins(W.Env())
        P["site"] = site_pins(os.path.join(home, "site"))
        P["payload"]["files"] = [["manifest.json", len(man), cls.msha], ["boot-template.img", len(tmpl), sha(tmpl)], ["rootfs.ext4", len(root), sha(root)]]
        P["finish"]["files"] = [["finish.json", len(flist), cls.fsha], ["uc-pxx-step2-finish", len(fin), sha(fin)], ["uc-pxx-bootctl-inst", len(r2), sha(r2)]]
        P["expect"]["step2"]["pins_allowed"] = [cls.msha, "b5df32c78cd0c5673f3e69ca9375d5b11d15cd1d7a70ca343d3421ef6f87dbe0"]
        cls.P = P
        cls.pins = os.path.join(b, "t", "pins.json")
        with open(cls.pins, "w") as f:
            json.dump(P, f, indent=1)
        shutil.copyfile(os.path.join(HERE, "fakexfer.js"), os.path.join(b, "t", "fakexfer.js"))
        cls.sysfs = os.path.join(b, "sysfs")
        mk_sysfs(cls.sysfs, [HUB, XFER])
        cls.me = W.self_check()["webdrive_sha256"]
        cls.n = 0

    @classmethod
    def tearDownClass(cls):
        if not os.environ.get("WD_KEEP"):
            subprocess.run(["chmod", "-R", "u+w", cls.base])
            shutil.rmtree(cls.base, ignore_errors=True)

    # scenarios: what the fake unit is and does
    def scn(self, name, **kw):
        info_b = {"model": "UC-P10", "slot": "b", "token": "ucpxx-5a454728", "step2": "waiting-payload",
                  "lines": ["Step 2 waits for its payload: send it from the installer page."]}
        info_a = {"model": "UC-P10", "slot": "a", "token": "ucpxx-5a454728", "step2": "finish: waiting",
                  "lines": ["This slot is a step-2 target: the page may send the pinned finish executor."]}
        base = {"payload": {"mode": "payload", "pins": [self.msha, "b5df32c78cd0c5673f3e69ca9375d5b11d15cd1d7a70ca343d3421ef6f87dbe0"], "info": info_b,
                            "after": ["sealing", "sealed", "confirm"]},
                "finish": {"mode": "finish", "kind": "finish", "pins": [self.fsha], "info": info_a, "after": ["finish: checking"]}}
        s = dict(base[kw.pop("kind", "payload")])
        s.update(kw)
        with open(os.path.join(self.base, "t", f"{name}.json"), "w") as f:
            json.dump(s, f)
        return name

    def run_tool(self, argv, scen, sysfs=None, env=None):
        type(self).n += 1
        run = os.path.join(self.base, "runs", f"{self.n:03d}-{self._testMethodName}")
        e = env or TestEnv(self.base, sysfs or self.sysfs, self.pins, scen)
        full = argv + ["--home", self.home, "--run", run]
        rc = W.main(full, e)
        r = None
        if os.path.exists(os.path.join(run, "result.json")):
            with open(os.path.join(run, "result.json")) as f:
                r = json.load(f)
        return rc, r, run

    def usb(self, run):
        out = []
        p = os.path.join(run, "usb.jsonl")
        if os.path.exists(p):
            with open(p) as f:
                out = [json.loads(x) for x in f]
        return out

    def out_frames(self, run):
        return [(x["type"], (x.get("body") or {}).get("name")) for x in self.usb(run) if x["op"] == "out-frame"]

    def no_violations(self, r):
        fu = r.get("fake_unit") or {}
        self.assertEqual(fu.get("violations"), [], "the fake unit saw a protocol violation")
        self.assertEqual(r.get("page_errors"), [])
        self.assertEqual(r.get("external"), [])

    def go(self, **over):
        type(self).n += 1
        v = {"session": f"U2-5 e2e test {self.n} {time.time()}", "webdrive-sha256": self.me, "a-payload-manifest-sha256": self.msha,
             "a-rootfs-sha256": self.P["payload"]["files"][2][2], "finish-list-sha256": self.fsha, "verdict": "GO"}
        v.update(over)
        p = os.path.join(self.base, "t", f"go-{self.n}.txt")
        with open(p, "w") as f:
            f.write("".join(f"{k}: {x}\n" for k, x in v.items() if x is not None))
        return p

    # ---- status ----
    def test_status_read(self):
        rc, r, run = self.run_tool(["status"], self.scn("st-ok"))
        self.assertEqual(rc, 0, r and r.get("why"))
        self.assertEqual(r["outcome"], "status-read")
        self.assertEqual(r["status"]["rows"]["Slot"], "B")
        self.assertEqual(r["status"]["rows"]["Interface mode"], "payload")
        self.assertEqual(r["status"]["hello"]["pins"][0], self.msha)
        self.assertEqual(set(t for t, _ in self.out_frames(run)), {1}, "status sends HELLO only")
        self.no_violations(r)
        self.assertEqual([x["op"] for x in r["fake_unit"]["rec"] if x["op"] == "frame"], ["frame"])
        with open(os.path.join(run, "files.sha256")) as f:
            self.assertIn("result.json", f.read())

    def test_status_finish_unit(self):
        rc, r, run = self.run_tool(["status"], self.scn("st-fin", kind="finish"))
        self.assertEqual(rc, 0)
        self.assertEqual(r["status"]["rows"]["Step 2"], "finish: waiting")
        self.assertIn("summary: the unit is ready for finish", open(os.path.join(run, "driver.log")).read())

    def test_status_dry(self):
        rc, r, run = self.run_tool(["status", "--dry-run"], self.scn("st-dry"))
        self.assertEqual(rc, 0, r and r.get("why"))
        self.assertEqual(r["outcome"], "dry-run")
        self.assertFalse([x for x in r["fake_unit"]["rec"] if x["op"] in ("open", "claim", "set-alt", "frame", "data")], "a dry run opens nothing")
        w = json.load(open(os.path.join(run, "wrapper.json")))
        self.assertNotIn("--device", w["argv"])

    def test_status_no_device(self):
        s = os.path.join(self.base, "sysfs-none")
        mk_sysfs(s, [HUB])
        rc, r, run = self.run_tool(["status"], self.scn("st-none"), sysfs=s)
        self.assertEqual(rc, W.EXIT_LINK)
        self.assertIsNone(r, "no container is started")

    def test_status_two_devices(self):
        s = os.path.join(self.base, "sysfs-two")
        mk_sysfs(s, [HUB, XFER, ("1-2",) + XFER[1:3] + (1, 6) + XFER[5:]])
        rc, r, run = self.run_tool(["status"], self.scn("st-two"), sysfs=s)
        self.assertEqual(rc, W.EXIT_UNEXPECTED)
        self.assertIsNone(r)

    def test_status_page_sees_two(self):
        rc, r, run = self.run_tool(["status"], self.scn("st-page2", devices=2))
        self.assertEqual(rc, W.EXIT_UNEXPECTED)
        self.assertIn("exactly one", r["why"])
        self.assertFalse([x for x in r["fake_unit"]["rec"] if x["op"] == "open"])

    def test_status_policy_not_granted(self):
        rc, r, run = self.run_tool(["status"], self.scn("st-noperm", permitted=False))
        self.assertEqual(rc, W.EXIT_LINK)
        self.assertIn("0 devices", r["why"])

    def test_status_wrong_serial_in_page(self):
        rc, r, run = self.run_tool(["status"], self.scn("st-serial", serial="OTHER"))
        self.assertEqual(rc, W.EXIT_UNEXPECTED)
        self.assertIn("not the unit's transfer interface", r["why"])

    def test_status_no_answer(self):
        t0 = time.time()
        rc, r, run = self.run_tool(["status"], self.scn("st-noans", fault="noHelloAnswer"))
        self.assertEqual(rc, W.EXIT_LINK)
        self.assertIn("the unit's status answer: none after", r["why"])
        self.assertIn("a pending IN", open(os.path.join(run, "driver.log")).read())
        self.assertLess(time.time() - t0, 400)

    # ---- step 2 ----
    def test_step2_send(self):
        rc, r, run = self.run_tool(["step2", "--payload", self.pay, "--go-file", self.go()], self.scn("s2-ok"))
        self.assertEqual(rc, 0, r and r.get("why"))
        self.assertEqual(r["outcome"], "payload-committed")
        self.assertEqual(r["committed"], {"ok": True, "auto": True})
        fu = r["fake_unit"]
        self.assertEqual(fu["committed"]["auto"], True)
        self.assertEqual(fu["committed"]["manifest"], self.msha)
        self.no_violations(r)
        fr = self.out_frames(run)
        # the page's status read (HELLO), then its send: HELLO, the three PUTs, COMMIT
        self.assertEqual(fr[:6], [(1, None), (1, None), (2, "manifest.json"), (2, "boot-template.img"), (2, "rootfs.ext4"), (5, None)])
        self.assertTrue(all(t in (1, 6) for t, _ in fr[6:]))
        data = [x["len"] for x in self.usb(run) if x["op"] == "out-data" and x["name"] == "rootfs.ext4"]
        self.assertEqual(data, [1048576, 1048576, 1048576, 4096])
        self.assertEqual(r["served"]["/payload/uc-pxx-mainline/rootfs.ext4"], self.P["payload"]["files"][2][1])
        with open(os.path.join(self.home, "used-go.txt")) as f:
            self.assertIn("step2", f.read())

    def test_step2_dry(self):
        rc, r, run = self.run_tool(["step2", "--dry-run", "--payload", self.pay], self.scn("s2-dry"))
        self.assertEqual(rc, 0, r and r.get("why"))
        self.assertEqual(r["outcome"], "dry-run")
        self.assertFalse([x for x in r["fake_unit"]["rec"] if x["op"] in ("open", "claim", "set-alt", "frame", "data")])
        self.assertEqual(r["served"]["/payload/uc-pxx-mainline/rootfs.ext4"], self.P["payload"]["files"][2][1], "the dry run downloads the payload")
        self.assertIn("is ticked", open(os.path.join(run, "driver.log")).read())

    def test_step2_needs_go(self):
        rc, r, run = self.run_tool(["step2", "--payload", self.pay], self.scn("s2-nogo"))
        self.assertEqual(rc, W.EXIT_USAGE)
        rc, r, run = self.run_tool(["step2", "--payload", self.pay, "--go-file", self.go(**{"a-rootfs-sha256": "0" * 64})], self.scn("s2-badgo"))
        self.assertEqual(rc, W.EXIT_LOCAL)
        self.assertIsNone(r)

    def test_step2_go_reuse(self):
        g = self.go()
        rc, r, run = self.run_tool(["step2", "--payload", self.pay, "--go-file", g], self.scn("s2-reuse", info={"model": "UC-P10", "slot": "a",
                                   "token": "ucpxx-5a454728", "step2": "waiting-payload"}))
        self.assertEqual(rc, W.EXIT_UNEXPECTED)  # wrong slot: nothing sent, but the GO is used
        self.assertFalse([x for x in r["fake_unit"]["rec"] if x["op"] == "frame" and x["type"] == 2])
        rc, r, run = self.run_tool(["step2", "--payload", self.pay, "--go-file", g], self.scn("s2-reuse2"))
        self.assertEqual(rc, W.EXIT_LOCAL)
        self.assertIsNone(r)

    def test_step2_unit_not_waiting(self):
        for name, kw in (("s2-st", {"mode": "status"}), ("s2-idle", {"info": {"model": "UC-P10", "slot": "b", "token": "ucpxx-5a454728", "step2": "idle"}}),
                         ("s2-pins", {"pins": ["b5df32c78cd0c5673f3e69ca9375d5b11d15cd1d7a70ca343d3421ef6f87dbe0"]}),
                         ("s2-oddpin", {"pins": [self.msha, "e" * 64]}), ("s2-room", {"room": 1000}),
                         ("s2-token", {"info": {"model": "UC-P10", "slot": "b", "token": "ucpxx-other", "step2": "waiting-payload"}})):
            rc, r, run = self.run_tool(["step2", "--payload", self.pay, "--go-file", self.go()], self.scn(name, **kw))
            self.assertEqual(rc, W.EXIT_UNEXPECTED, name)
            self.assertIn("nothing was sent", r["why"], name)
            self.assertFalse([x for x in r["fake_unit"]["rec"] if x["op"] == "frame" and x["type"] == 2], name)

    def test_step2_cut(self):
        rc, r, run = self.run_tool(["step2", "--payload", self.pay, "--go-file", self.go()], self.scn("s2-cut", fault="cutRootfs"))
        self.assertEqual(rc, W.EXIT_LINK, r and r.get("why"))
        self.assertIn("The transfer stopped", r["why"])
        self.assertIsNone(r["fake_unit"]["committed"])

    def test_step2_refused(self):
        for name, f in (("s2-ram", "refuse:rootfs.ext4"), ("s2-sha", "badSha:rootfs.ext4"), ("s2-commit", "commitRefused")):
            rc, r, run = self.run_tool(["step2", "--payload", self.pay, "--go-file", self.go()], self.scn(name, fault=f))
            self.assertEqual(rc, W.EXIT_REFUSED, (name, r and r.get("why")))
            self.assertIsNone(r["fake_unit"]["committed"], name)

    def test_step2_lying_unit_sha(self):
        rc, r, run = self.run_tool(["step2", "--payload", self.pay, "--go-file", self.go()], self.scn("s2-lie", fault="lyingSha:rootfs.ext4"))
        self.assertEqual(rc, W.EXIT_UNEXPECTED, r and r.get("why"))
        self.assertIsNone(r["fake_unit"]["committed"], "the guard refused the COMMIT before it was sent")
        self.assertTrue([x for x in self.usb(run) if x["op"] == "guard" and "sha256 of rootfs.ext4" in x["why"]])

    def test_step2_lost_commit_answer(self):
        rc, r, run = self.run_tool(["step2", "--payload", self.pay, "--go-file", self.go()], self.scn("s2-lost", fault="lostCommitReply"))
        self.assertEqual(rc, W.EXIT_LINK, r and r.get("why"))
        self.assertEqual(r["fake_unit"]["committed"]["auto"], True)  # the unit took it: the session reads the status

    def test_step2_payload_changed_on_host(self):
        p = os.path.join(self.base, "set2", "uc-pxx-mainline")
        shutil.copytree(self.pay, p)
        os.chmod(os.path.join(p, "rootfs.ext4"), 0o644)
        with open(os.path.join(p, "rootfs.ext4"), "r+b") as f:
            f.seek(100)
            f.write(b"\x00\x01")
        rc, r, run = self.run_tool(["step2", "--payload", p, "--go-file", self.go()], self.scn("s2-host"))
        self.assertEqual(rc, W.EXIT_LOCAL)
        self.assertIsNone(r)

    def test_step2_sigint_during_send(self):
        """an operator stop during the send: the send runs to the end"""
        name = self.scn("s2-int", slowData=3000)
        e = TestEnv(self.base, self.sysfs, self.pins, name)
        stopper = {}

        def poke():
            for _ in range(600):
                time.sleep(0.5)
                out = subprocess.run(["docker", "ps", "--format", "{{.Names}}", "--filter", "name=^uc-pxx-webdrive-step2"],
                                     stdout=subprocess.PIPE, text=True).stdout.split()
                runs = sorted(os.listdir(os.path.join(self.base, "runs")))
                usb = os.path.join(self.base, "runs", runs[-1], "usb.jsonl") if runs else ""
                if out and usb and os.path.exists(usb) and '"out-data"' in open(usb).read() and '"rootfs.ext4"' in open(usb).read():
                    subprocess.run(["docker", "kill", "-s", "INT", out[0]])
                    subprocess.run(["docker", "kill", "-s", "TERM", out[0]])
                    stopper["at"] = time.time()
                    return
        t = threading.Thread(target=poke)
        t.start()
        rc, r, run = self.run_tool(["step2", "--payload", self.pay, "--go-file", self.go()], name, env=e)
        t.join()
        self.assertIn("at", stopper, "the stop was sent during the send")
        self.assertEqual(r["fake_unit"]["committed"]["auto"], True, "the send ran to the end")
        self.assertIn(rc, (0, W.EXIT_OPERATOR))
        self.assertIn("the run stops at the next safe point", open(os.path.join(run, "driver.log")).read())

    # ---- finish ----
    def test_finish_send(self):
        rc, r, run = self.run_tool(["finish", "--finish", self.fin, "--go-file", self.go()], self.scn("f-ok", kind="finish"))
        self.assertEqual(rc, 0, r and r.get("why"))
        self.assertEqual(r["outcome"], "finish-committed")
        self.assertEqual(r["committed"], {"ok": True, "auto": False})
        self.assertEqual(r["fake_unit"]["committed"]["auto"], False)
        self.no_violations(r)
        fr = self.out_frames(run)
        self.assertEqual(fr[:6], [(1, None), (1, None), (2, "finish.json"), (2, "uc-pxx-step2-finish"), (2, "uc-pxx-bootctl-inst"), (5, None)])

    def test_finish_dry(self):
        rc, r, run = self.run_tool(["finish", "--dry-run", "--finish", self.fin], self.scn("f-dry", kind="finish"))
        self.assertEqual(rc, 0, r and r.get("why"))
        self.assertFalse([x for x in r["fake_unit"]["rec"] if x["op"] in ("open", "frame", "data")])

    def test_finish_wrong_unit(self):
        for name, kw in (("f-pin", {"pins": ["c" * 64]}), ("f-mode", {"mode": "payload", "kind": "payload"}),
                         ("f-slot", {"info": {"model": "UC-P10", "slot": "b", "token": "ucpxx-5a454728", "step2": "finish: waiting"}})):
            kind = kw.pop("kind", "finish")
            rc, r, run = self.run_tool(["finish", "--finish", self.fin, "--go-file", self.go()], self.scn(name, kind=kind, **kw))
            self.assertEqual(rc, W.EXIT_UNEXPECTED, name)
            self.assertFalse([x for x in r["fake_unit"]["rec"] if x["op"] == "frame" and x["type"] == 2], name)

    def test_finish_cut_and_lost(self):
        rc, r, run = self.run_tool(["finish", "--finish", self.fin, "--go-file", self.go()], self.scn("f-cut", kind="finish", fault="cutRootfs"))
        self.assertEqual(rc, W.EXIT_LINK, r and r.get("why"))
        self.assertIsNone(r["fake_unit"]["committed"])
        rc, r, run = self.run_tool(["finish", "--finish", self.fin, "--go-file", self.go()], self.scn("f-lost", kind="finish", fault="lostCommitReply"))
        self.assertEqual(rc, W.EXIT_LINK, r and r.get("why"))
        self.assertIn("COMMIT", r["why"])

    # ---- watch ----
    def test_watch(self):
        st = lambda c: {"model": "UC-P10", "slot": "b", "token": "ucpxx-5a454728", "step2": c, "lines": [c]}  # noqa: E731
        names = [self.scn(f"w-{i}", mode="status", info=st(c), after=None) for i, c in
                 enumerate(["sealed", "confirm", "started 0123456789ab", "armed 0123456789ab", "restarting"])]
        e = TestEnv(self.base, self.sysfs, self.pins, names)
        real = W.find_devices
        runs = os.path.join(self.base, "runs")

        def fd(env):
            done = 0
            for d in os.listdir(runs):
                if d.endswith("test_watch"):
                    done = len([x for x in os.listdir(os.path.join(runs, d)) if os.path.exists(os.path.join(runs, d, x, "result.json"))])
            if done >= len(names):  # after the restart: no device
                return [d for d in real(env) if d["pid"] != "0105"]
            return real(env)
        W.find_devices = fd
        try:
            rc, r, run = self.run_tool(["watch", "--every", "5", "--max-minutes", "10"], None, env=e)
        finally:
            W.find_devices = real
        self.assertEqual(rc, 0)
        self.assertEqual(len([x for x in os.listdir(run) if x.endswith("-status")]), 5)

    def test_watch_stop(self):
        names = [self.scn("w-stop", mode="status", after=None, info={"model": "UC-P10", "slot": "b", "token": "ucpxx-5a454728",
                          "step2": "stop: step 2 stopped", "lines": ["Step 2 stopped"]})]
        rc, r, run = self.run_tool(["watch", "--every", "5", "--max-minutes", "5"], None, env=TestEnv(self.base, self.sysfs, self.pins, names))
        self.assertEqual(rc, W.EXIT_KEEP_POWERED)


@unittest.skipUnless(os.environ.get("WD_REAL"), "WD_REAL=1: the dry runs with the production pins and the real RC-7 set")
class RealDry(unittest.TestCase):
    """the production pins, page copy, payload and finish set; --dry-run; the
    fake navigator.usb only gives the page its device list (nothing is opened)."""

    def setUp(self):
        self.base = tempfile.mkdtemp(prefix="wdr-", dir=os.environ.get("WD_TMP") or None)
        os.makedirs(os.path.join(self.base, "t"))
        shutil.copyfile(os.path.join(HERE, "fakexfer.js"), os.path.join(self.base, "t", "fakexfer.js"))
        self.sysfs = os.path.join(self.base, "sysfs")
        mk_sysfs(self.sysfs, [HUB, XFER])
        self.home = os.path.expanduser(os.environ.get("WD_HOME", "~/uc-pxx/webusb-driver/home"))

    def env(self, scen):
        with open(os.path.join(self.base, "t", "scn.json"), "w") as f:
            json.dump(scen, f)
        e = TestEnv(self.base, self.sysfs, os.path.join(TOOL, "pins.json"), "scn", test=False, allow_fake=True)
        return e

    def test_real_dry(self):
        info = {"model": "UC-P10", "slot": "b", "token": "ucpxx-5a454728", "step2": "waiting-payload"}
        for mode, extra in (("status", []), ("step2", ["--payload", os.environ["WD_PAYLOAD"]]), ("finish", ["--finish", os.environ["WD_FINISH"]])):
            run = os.path.join(self.base, f"run-{mode}")
            rc = W.main([mode, "--dry-run", "--home", self.home, "--run", run] + extra, self.env({"mode": "payload", "pins": [], "info": info}))
            r = json.load(open(os.path.join(run, "result.json")))
            self.assertEqual(rc, 0, (mode, r.get("why")))
            self.assertEqual(r["outcome"], "dry-run")
            self.assertEqual(r["tool"]["pins_path"], "/tool/pins.json")
            self.assertFalse([x for x in r["fake_unit"]["rec"] if x["op"] in ("open", "frame", "data")])
            print(f"real dry run {mode}: {r['outcome']}; served {r['served']}", flush=True)


if __name__ == "__main__":
    unittest.main(verbosity=2)
