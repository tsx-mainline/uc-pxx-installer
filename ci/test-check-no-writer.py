#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""Tests of ci/check-no-writer.py on small cpio archives.

    ci/test-check-no-writer.py

Each case builds an uncompressed newc cpio in a temporary folder and checks
the verdict of the status-image check and of the install-image check. The
real uc-pxx-bootctl-inst is used when UC_PXX_BOOTCTL names it; without it the
cases use made-up bytes and a patched list of writer sha256s.
Exit status: 0 all cases pass, 1 a case failed.
"""
import hashlib
import importlib.util
import os
import sys
import tempfile

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("cnw", os.path.join(HERE, "check-no-writer.py"))
cnw = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cnw)

DIR, REG, TOOL = 0o040755, 0o100755, 0o100700


def cpio(path, entries):
    """entries: list of (name, mode, data)."""
    out = bytearray()

    def add(name, mode, data):
        nb = name.encode() + b"\0"
        hdr = "070701" + "".join("%08x" % v for v in (0, mode, 0, 0, 1, 0, len(data), 0, 0, 0, 0, len(nb), 0))
        out.extend(hdr.encode() + nb)
        out.extend(b"\0" * ((4 - len(out) % 4) % 4))
        out.extend(data)
        out.extend(b"\0" * ((4 - len(out) % 4) % 4))

    for e in entries:
        add(*e)
    add("TRAILER!!!", 0, b"")
    with open(path, "wb") as f:
        f.write(out)


def elf(*strings):
    return b"\x7fELF\x02\x01\x01" + b"\0" * 9 + b"".join(s + b"\0" for s in strings) + os.urandom(64)


STATUS_P10INST = elf(b"uc-pxx-installer/internal/status.Collect")
INSTALL_P10INST = elf(b"uc-pxx-installer/internal/install.Run", b"uc-pxx-installer/internal/device.Live",
                      b"uc-pxx-installer/internal/devmode.Install")
BASE = [(".", DIR, b""), ("init", REG, b"#!/bin/sh\nexec /sbin/init\n"), ("etc", DIR, b""), ("etc/init.d", DIR, b""),
        ("etc/init.d/rcS", REG, b"#!/bin/sh\np10inst readonly\n"), ("usr", DIR, b""), ("usr/bin", DIR, b"")]


def verdict(path, install=False):
    errors = []
    cnw.check_cpio(path, errors, install_image=install)
    return errors


def main():
    real = os.environ.get("UC_PXX_BOOTCTL", "")
    tool = open(real, "rb").read() if real else elf(b"uc-pxx-bootctl: %s\n", b"libavb-would-boot: %c\n") + b"made-up tool"
    tool_sha = hashlib.sha256(tool).hexdigest()
    if real:
        if tool_sha != cnw.INSTALL_TOOL_SHA:
            print("FAIL: UC_PXX_BOOTCTL is not the reviewed binary")
            return 1
    else:
        # made-up bytes stand for the reviewed binary
        cnw.WRITER_SHAS[tool_sha] = "uc-pxx-bootctl-inst (test bytes)"
        cnw.INSTALL_TOOL_SHA = tool_sha
    plain_blob = os.urandom(4096)  # not an ELF: only its sha256 can give it away
    cnw.WRITER_SHAS[hashlib.sha256(plain_blob).hexdigest()] = "p10-bootctl (test bytes)"

    libexec = [("usr/libexec", DIR, b""), ("usr/libexec/p10inst", DIR, b"")]
    cases = [
        # name, install image?, entries, the errors must name this ("" = no error)
        ("status image, clean", False, BASE + [("usr/bin/p10inst", REG, STATUS_P10INST)], ""),
        ("status image with usr/libexec/p10inst/uc-pxx-bootctl-inst (the review's F1 input)", False,
         BASE + [("usr/bin/p10inst", REG, STATUS_P10INST)] + libexec + [(cnw.INSTALL_TOOL, REG, tool)],
         "uc-pxx-bootctl-inst"),
        ("status image with the tool renamed to usr/bin/helper", False,
         BASE + [("usr/bin/p10inst", REG, STATUS_P10INST), ("usr/bin/helper", REG, tool)], "A/B writer"),
        ("status image with a non-ELF writer known only by its sha256", False,
         BASE + [("usr/bin/p10inst", REG, STATUS_P10INST), ("usr/share/x.bin", REG, plain_blob)], "A/B writer"),
        ("status image with an ELF that has the log format of uc-pxx-bootctl", False,
         BASE + [("usr/bin/p10inst", REG, STATUS_P10INST), ("usr/sbin/abtool", REG, elf(b"uc-pxx-bootctl: %s\n"))],
         "boot-control string"),
        ("status image with an ELF that has the status format of a bootctl", False,
         BASE + [("usr/bin/p10inst", REG, STATUS_P10INST), ("usr/sbin/abtool", REG, elf(b"libavb-would-boot: %c\n"))],
         "boot-control string"),
        ("status image whose p10inst holds uc-pxx-bootctl: by string packing (the release binary does)", False,
         BASE + [("usr/bin/p10inst", REG, STATUS_P10INST + b"uc-pxx-bootctl: %wchunk is not a ZIP record")], ""),
        ("status image with an empty usr/libexec/p10inst", False,
         BASE + [("usr/bin/p10inst", REG, STATUS_P10INST), ("usr/libexec", DIR, b""), ("usr/libexec/p10inst", DIR, b"")], ""),
        ("status image with an empty usr/libexec/p10inst/x", False,
         BASE + [("usr/bin/p10inst", REG, STATUS_P10INST)] + libexec + [("usr/libexec/p10inst/x", REG, b"")],
         "nothing may be under"),
        ("status image with an install p10inst", False, BASE + [("usr/bin/p10inst", REG, INSTALL_P10INST)],
         "holds the install package"),
        ("install image, correct", True,
         BASE + [("usr/bin/p10inst", REG, INSTALL_P10INST)] + libexec + [(cnw.INSTALL_TOOL, TOOL, tool)], ""),
        ("install image with the tool at mode 0755", True,
         BASE + [("usr/bin/p10inst", REG, INSTALL_P10INST)] + libexec + [(cnw.INSTALL_TOOL, REG, tool)], "mode 0755"),
        ("install image without the tool", True, BASE + [("usr/bin/p10inst", REG, INSTALL_P10INST)], "exactly one"),
        ("install image with another tool binary", True,
         BASE + [("usr/bin/p10inst", REG, INSTALL_P10INST)] + libexec + [(cnw.INSTALL_TOOL, TOOL, tool + b"\0")],
         "not the reviewed"),
        ("install image whose p10inst holds uc-pxx-bootctl: by string packing", True,
         BASE + [("usr/bin/p10inst", REG, INSTALL_P10INST + b"uc-pxx-bootctl: %wchunk")] + libexec +
         [(cnw.INSTALL_TOOL, TOOL, tool)], ""),
        ("install image with a second copy elsewhere", True,
         BASE + [("usr/bin/p10inst", REG, INSTALL_P10INST), ("usr/bin/abtool", REG, tool)] + libexec +
         [(cnw.INSTALL_TOOL, TOOL, tool)], "A/B writer"),
        ("install image with a devtest p10inst", True,
         BASE + [("usr/bin/p10inst", REG, INSTALL_P10INST + cnw.DEVTEST_MARKER)] + libexec +
         [(cnw.INSTALL_TOOL, TOOL, tool)], "devtest code"),
        ("install image with uc-pxx-bootctl too", True,
         BASE + [("usr/bin/p10inst", REG, INSTALL_P10INST), ("usr/sbin/uc-pxx-bootctl", REG, b"x")] + libexec +
         [(cnw.INSTALL_TOOL, TOOL, tool)], "forbidden tool"),
    ]
    bad = 0
    with tempfile.TemporaryDirectory() as d:
        for i, (name, inst, entries, want) in enumerate(cases):
            p = os.path.join(d, "%d.cpio" % i)
            cpio(p, entries)
            errs = verdict(p, inst)
            ok = (not errs) if not want else any(want in e for e in errs)
            print("%s %s%s" % ("ok  " if ok else "FAIL", name, "" if ok else ": %s" % errs))
            bad += not ok
    print("check-no-writer tests: %d cases, %d failed (%s bootctl)" % (len(cases), bad, "real" if real else "made-up"))
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
