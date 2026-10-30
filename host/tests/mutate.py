#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""Mutation check of the p10-enter guards.

    python3 -I host/tests/mutate.py [JOBS]

Each mutation removes or weakens one guard of host/p10-enter in its own copy of
host/, then runs host/tests/test_p10_enter.py on that copy. Every mutation
must be CAUGHT (at least one test fails). M1..M17 are the reviewer's set of
P10-ENTER-REVIEW.md F7, written for this version of the tool; the F* ones
belong to the fixes of the review. Exit 0 only when all are caught.
Run it only where the tests may run (a container without USB on a host with a
unit).
"""
import concurrent.futures as cf
import os
import re
import shutil
import subprocess
import sys
import tempfile

HOST = os.path.dirname(os.path.dirname(os.path.realpath(__file__)))

M = {
    "M1 no guard ld before the write": (
        '        st2, _ = r.rk_list()\n        if st2 == "maskrom":',
        '        st2 = "loader"\n        if st2 == "maskrom":'),
    "M2 no compare of the guard re-read": (
        '        if sha256_bytes(again) != bsha:', '        if False:'),
    "M3 ssh dry run: rc only": (
        '    if rc != 0 or REBOOT_DRY_OK not in out:', '    if rc != 0:'),
    "M4 any single rockusb device is a Loader": (
        '        if (vid.lower(), pid.lower(), mode) == ("2207", "330c", "Loader"):', '        if True:'),
    "M5 no Maskrom check just before the write": (
        '        if st2 == "maskrom":\n            raise Stop("MASKROM", "Maskrom before the write. STOP, nothing was written.")',
        '        if False:\n            pass'),
    "M6 two fastboot devices: take the first": (
        '            if len(devs) > 1:\n                raise Stop("AMBIGUOUS_DEVICE", "more than one fastboot device. " + self.state)',
        '            devs = devs[:1]'),
    "M8 reset subcode 3": ('    return [rk, "rd", "0"]', '    return [rk, "rd", "3"]'),
    "M9 no safe point just before the write": (
        '        r.safe_point()\n        r.log("writing the BCB', '        r.log("writing the BCB'),
    "M10 only SIGINT held": ('    HELD = ("SIGINT", "SIGTERM", "SIGHUP", "SIGQUIT")', '    HELD = ("SIGINT",)'),
    "M10b SIGHUP not held": ('    HELD = ("SIGINT", "SIGTERM", "SIGHUP", "SIGQUIT")',
                             '    HELD = ("SIGINT", "SIGTERM", "SIGQUIT")'),
    "M10c SIGQUIT not held": ('    HELD = ("SIGINT", "SIGTERM", "SIGHUP", "SIGQUIT")',
                              '    HELD = ("SIGINT", "SIGTERM", "SIGHUP")'),
    "M11 a BCB with a zero first sector is accepted": (
        '    st = states.get(bsha)\n    if st is None:\n        raise Stop("BCB_UNKNOWN", "unknown BCB',
        '    st = states.get(bsha) or (bcb[:512] == bytes(512) and {"name": "z0", "kind": "clean"}) or None\n'
        '    if st is None:\n        raise Stop("BCB_UNKNOWN", "unknown BCB'),
    "M13 no image check at the exec point": (
        '                if a[2] == "boot" and a[3] == self.boot_image:\n                    self.check_boot_image()',
        '                if a[2] == "boot" and a[3] == self.boot_image:\n                    pass'),
    "M14 an unreadable current-slot is accepted": (
        '        missing = [v for v in REQUIRED_VARS if vals.get(v) is None]',
        '        missing = [v for v in REQUIRED_VARS if vals.get(v) is None and v != "current-slot"]'),
    "M15 no own session for device commands": ('start_new_session=device)', 'start_new_session=False)'),
    "M16 A/B re-read after the write not compared": ('    if ab2 != ab:', '    if False:'),
    "M17 BCB read-back compared by length only": ('        if back != payload:',
                                                  '        if len(back) != len(payload):'),
    "F1a only current-slot compared": (
        '        diff = ["%s: fastboot %r, expected %r" % (k, vals[k], exp[k]) for k in SLOT_VARS',
        '        diff = ["%s: fastboot %r, expected %r" % (k, vals[k], exp[k]) for k in ("current-slot",)'),
    "F1b no entry check on the rockusb route": (
        '    r.check_entry(slots, nxt, "in the download mode before the write")', '    pass'),
    "F2a both slots successful not required": (
        '    if not (slot_successful(slots[0]) and slot_successful(slots[1])):', '    if False:'),
    "F2b --expect-ab-sha256 not compared": ('    if got != want:', '    if False:'),
    "F3a restart block ignored": ('    if why:\n        raise Stop("RESTART_BLOCKED"',
                                  '    if False:\n        raise Stop("RESTART_BLOCKED"'),
    "F3b in_progress 1 accepted": ('not in ("", "0"):', 'not in ("", "0", "1"):'),
    "F3c unexpected state lines accepted": ('        elif not m:\n            why.append(',
                                            '        elif False:\n            why.append('),
    "F4a ssh A/B error ignored": ('        raise Stop("AB_INVALID", "A/B sector: %s. Nothing was sent." % err)',
                                  '        pass'),
    "F4b no entry check on the ssh route": ('    r.check_entry(slots, nxt, "over ssh before the restart")',
                                            '    pass'),
    "F5a a broken terminal stops the tool": ('            except (OSError, ValueError):\n                self.tty_ok = False',
                                             '            except ValueError:\n                self.tty_ok = False'),
    "F5b the signal handler can raise": (
        '            except OSError:\n                pass\n\n        def tstp',
        '            except ZeroDivisionError:\n                pass\n\n        def tstp'),
    "F5c no catch-all": ('    except Exception as e:      # noqa: BLE001 - the state must be told whatever went wrong',
                         '    except ZeroDivisionError as e:'),
    "F6 the original image is booted, not the copy": ('        self.boot_image = p\n',
                                                      '        self.boot_image = os.path.abspath(self.args.image)\n'),
    "F9a fastboot serial not checked in the wait": ('                if devs[0] != self.args.expect_serialno:',
                                                    '                if False:'),
    "F9b getvar serialno not compared": ('        if vals["serialno"] != self.args.expect_serialno:',
                                         '        if False:'),
    "F10 an unparsed DevNo line is ignored": ('        if n != len(devs):\n            return "unknown", out',
                                              '        if False:\n            return "unknown", out'),
    "F11 no exec-point check": ('        prog, a = cmd[0], cmd[1:]\n        progs',
                                '        return\n        prog, a = cmd[0], cmd[1:]\n        progs'),
    "F12 payload file not checked at the exec point": ('                self.check_payload_file(p)\n                return',
                                                      '                return'),
    "F13 known-state file not pinned": ('    if fsha != STATES_SHA256:', '    if False:'),
    "F14a ssh without -n": ('    cmd = [ssh, "-n"]', '    cmd = [ssh]'),
    "F14b ssh gets the tool's stdin": ('stdin=subprocess.DEVNULL, stdout=fso or fo,',
                                       'stdin=subprocess.DEVNULL if device else None, stdout=fso or fo,'),
    "F15 old state text after the reset": (
        '    r.state = ("The BCB held bootonce-bootloader when the reset was sent;',
        '    r.state = ("The BCB holds bootonce-bootloader;'),
}


def run(item):
    name, (a, b) = item
    d = tempfile.mkdtemp(prefix="p10enter-mut-")
    try:
        shutil.copytree(HOST, os.path.join(d, "host"), ignore=shutil.ignore_patterns("__pycache__"))
        p = os.path.join(d, "host", "p10-enter")
        with open(p) as f:
            s = f.read()
        if s.count(a) != 1:
            return name, None, ["the mutation does not apply (%d matches)" % s.count(a)]
        with open(p, "w") as f:
            f.write(s.replace(a, b))
        r = subprocess.run([sys.executable, "-I", os.path.join(d, "host", "tests", "test_p10_enter.py")],
                           stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=1800)
        out = r.stdout.decode()
        fails = sorted(set(re.findall(r"^(?:FAIL|ERROR): (\w+)", out, re.M)))
        return name, r.returncode, fails
    finally:
        shutil.rmtree(d, ignore_errors=True)


def main():
    jobs = int(sys.argv[1]) if len(sys.argv) > 1 else 6
    bad = 0
    with cf.ThreadPoolExecutor(jobs) as ex:
        for name, rc, fails in sorted(ex.map(run, M.items())):
            if rc is None:
                bad += 1
                print("%-50s NOT APPLIED: %s" % (name, fails[0]))
            elif rc == 0:
                bad += 1
                print("%-50s NOT CAUGHT" % name)
            else:
                print("%-50s CAUGHT by %d: %s" % (name, len(fails), ", ".join(fails)))
    print("%d mutations, %d not caught or not applied" % (len(M), bad))
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
