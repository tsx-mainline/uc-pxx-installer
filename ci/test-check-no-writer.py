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
REAL_OPTEE = dict(cnw.OPTEE_FILES)

DIR, REG, TOOL = 0o040755, 0o100755, 0o100700


def cpio(path, entries):
    """entries: list of (name, mode, data) or (name, mode, data, uid)."""
    out = bytearray()

    def add(name, mode, data, uid=0):
        nb = name.encode() + b"\0"
        hdr = "070701" + "".join("%08x" % v for v in (0, mode, uid, 0, 1, 0, len(data), 0, 0, 0, 0, len(nb), 0))
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
MAINLINE = b"uc-pxx-installer/internal/mainline.Verify\0uc-pxx-installer/internal/mainline/mldrive.Top\0"
BASE = [(".", DIR, b""), ("init", REG, b"#!/bin/sh\nexec /sbin/init\n"), ("etc", DIR, b""), ("etc/init.d", DIR, b""),
        ("etc/init.d/rcS", REG, b"#!/bin/sh\nuc-pxx-inst readonly\n"), ("usr", DIR, b""), ("usr/bin", DIR, b"")]


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

    libexec = [("usr/libexec", DIR, b""), ("usr/libexec/uc-pxx-inst", DIR, b"")]
    # the lock_state reader (B-4): made-up bytes stand for the three pinned files
    ofiles = {n: elf(n.encode(), b"made-up reader file") for n in cnw.OPTEE_FILES}
    for n, b in ofiles.items():
        cnw.OPTEE_FILES[n] = hashlib.sha256(b).hexdigest()
        if n in cnw.OPTEE_WRITE_FILES:
            # the made-up writer bytes are known by their sha256 anywhere, as the real ones (review MAJOR-2)
            cnw.P16_WRITER_SHAS[cnw.OPTEE_FILES[n]] = n + " (test bytes)"
    ODIR, OREG = 0o040500, 0o100500
    inst = BASE + [("usr/bin/uc-pxx-inst", REG, INSTALL_P10INST)] + libexec + [(cnw.INSTALL_TOOL, TOOL, tool)]
    olib = [("usr/lib", DIR, b""), (cnw.OPTEE_PARENT, DIR, b""), (cnw.OPTEE_DIR, ODIR, b"")]
    ofull = olib + [(cnw.OPTEE_DIR + "/" + n, OREG, b) for n, b in sorted(ofiles.items())]
    # the step-1 USB service of an install image (mainline-rc-10 I-6): made-up bytes of an xfer build
    xfb = elf(b"uc-pxx-installer/internal/xfer.(*Receiver).Serve\0uc-pxx-installer/internal/gadget.Bind\0"
              b"uc-pxx-installer/internal/mainline.ParseManifest\0", b"made-up uc-pxx-xfer")
    xf = [("usr/libexec/uc-pxx-xfer", DIR, b""), (cnw.INSTALL_XFER, REG, xfb)]
    ofull = ofull + xf
    cases = [
        # name, install image?, entries, the errors must name this ("" = no error)
        ("status image, clean", False, BASE + [("usr/bin/uc-pxx-inst", REG, STATUS_P10INST)], ""),
        ("status image with usr/libexec/uc-pxx-inst/uc-pxx-bootctl-inst (the review's F1 input)", False,
         BASE + [("usr/bin/uc-pxx-inst", REG, STATUS_P10INST)] + libexec + [(cnw.INSTALL_TOOL, REG, tool)],
         "uc-pxx-bootctl-inst"),
        ("status image with the tool renamed to usr/bin/helper", False,
         BASE + [("usr/bin/uc-pxx-inst", REG, STATUS_P10INST), ("usr/bin/helper", REG, tool)], "A/B writer"),
        ("status image with a non-ELF writer known only by its sha256", False,
         BASE + [("usr/bin/uc-pxx-inst", REG, STATUS_P10INST), ("usr/share/x.bin", REG, plain_blob)], "A/B writer"),
        ("status image with an ELF that has the log format of uc-pxx-bootctl", False,
         BASE + [("usr/bin/uc-pxx-inst", REG, STATUS_P10INST), ("usr/sbin/abtool", REG, elf(b"uc-pxx-bootctl: %s\n"))],
         "boot-control string"),
        ("status image with an ELF that has the status format of a bootctl", False,
         BASE + [("usr/bin/uc-pxx-inst", REG, STATUS_P10INST), ("usr/sbin/abtool", REG, elf(b"libavb-would-boot: %c\n"))],
         "boot-control string"),
        ("status image whose uc-pxx-inst holds uc-pxx-bootctl: by string packing (the release binary does)", False,
         BASE + [("usr/bin/uc-pxx-inst", REG, STATUS_P10INST + b"uc-pxx-bootctl: %wchunk is not a ZIP record")], ""),
        ("status image with an empty usr/libexec/uc-pxx-inst", False,
         BASE + [("usr/bin/uc-pxx-inst", REG, STATUS_P10INST), ("usr/libexec", DIR, b""), ("usr/libexec/uc-pxx-inst", DIR, b"")], ""),
        ("status image with an empty usr/libexec/uc-pxx-inst/x", False,
         BASE + [("usr/bin/uc-pxx-inst", REG, STATUS_P10INST)] + libexec + [("usr/libexec/uc-pxx-inst/x", REG, b"")],
         "nothing may be under"),
        ("status image with an install uc-pxx-inst", False, BASE + [("usr/bin/uc-pxx-inst", REG, INSTALL_P10INST)],
         "holds the install package"),
        ("install image, correct", True,
         BASE + [("usr/bin/uc-pxx-inst", REG, INSTALL_P10INST)] + libexec + [(cnw.INSTALL_TOOL, TOOL, tool)] + ofull, ""),
        ("install image with the tool at mode 0755", True,
         BASE + [("usr/bin/uc-pxx-inst", REG, INSTALL_P10INST)] + libexec + [(cnw.INSTALL_TOOL, REG, tool)], "mode 0755"),
        ("install image without the tool", True, BASE + [("usr/bin/uc-pxx-inst", REG, INSTALL_P10INST)], "exactly one"),
        ("install image with another tool binary", True,
         BASE + [("usr/bin/uc-pxx-inst", REG, INSTALL_P10INST)] + libexec + [(cnw.INSTALL_TOOL, TOOL, tool + b"\0")],
         "not the reviewed"),
        ("install image whose uc-pxx-inst holds uc-pxx-bootctl: by string packing", True,
         BASE + [("usr/bin/uc-pxx-inst", REG, INSTALL_P10INST + b"uc-pxx-bootctl: %wchunk")] + libexec +
         [(cnw.INSTALL_TOOL, TOOL, tool)] + ofull, ""),
        ("install image with a second copy elsewhere", True,
         BASE + [("usr/bin/uc-pxx-inst", REG, INSTALL_P10INST), ("usr/bin/abtool", REG, tool)] + libexec +
         [(cnw.INSTALL_TOOL, TOOL, tool)], "A/B writer"),
        ("install image with a devtest uc-pxx-inst", True,
         BASE + [("usr/bin/uc-pxx-inst", REG, INSTALL_P10INST + cnw.DEVTEST_MARKER)] + libexec +
         [(cnw.INSTALL_TOOL, TOOL, tool)], "devtest code"),
        ("install image whose uc-pxx-inst holds the download-mode restart", True,
         BASE + [("usr/bin/uc-pxx-inst", REG, INSTALL_P10INST + b"uc-pxx-installer/internal/rebootmode.Restart")] + libexec +
         [(cnw.INSTALL_TOOL, TOOL, tool)], "status build only"),
        ("status image with the download-mode restart", False,
         BASE + [("usr/bin/uc-pxx-inst", REG, STATUS_P10INST + b"uc-pxx-installer/internal/rebootmode.Restart\0")], ""),
        ("install image with uc-pxx-bootctl too", True,
         BASE + [("usr/bin/uc-pxx-inst", REG, INSTALL_P10INST), ("usr/sbin/uc-pxx-bootctl", REG, b"x")] + libexec +
         [(cnw.INSTALL_TOOL, TOOL, tool)], "forbidden tool"),
        # the mainline install (phase 4)
        ("install image with the mainline install", True,
         BASE + [("usr/bin/uc-pxx-inst", REG, INSTALL_P10INST + MAINLINE)] + libexec + [(cnw.INSTALL_TOOL, TOOL, tool)] + ofull, ""),
        ("install image whose uc-pxx-inst holds the test-only mainline package", True,
         BASE + [("usr/bin/uc-pxx-inst", REG, INSTALL_P10INST + MAINLINE + b"uc-pxx-installer/internal/mainline/mltest.NewBundle")] +
         libexec + [(cnw.INSTALL_TOOL, TOOL, tool)], "test-only package"),
        ("install image whose uc-pxx-inst holds the test payload gate", True,
         BASE + [("usr/bin/uc-pxx-inst", REG, INSTALL_P10INST + MAINLINE + b"uc-pxx-installer/internal/mainline.TestGate\0")] +
         libexec + [(cnw.INSTALL_TOOL, TOOL, tool)], "TestGate"),
        ("install image whose uc-pxx-inst has the devtest fixture flag", True,
         BASE + [("usr/bin/uc-pxx-inst", REG, INSTALL_P10INST + MAINLINE + b"test-fixture-manifests")] + libexec +
         [(cnw.INSTALL_TOOL, TOOL, tool)], "devtest code"),
        # the lock_state reader folder (MAINLINE-RC2-LS-REVIEW B-4) and the writer (mainline RC-6): exactly five files
        ("install image with the lock_state reader and writer", True, inst + ofull, ""),
        ("install image without the step-1 USB service", True, inst + ofull[:-2], "needs exactly one usr/libexec/uc-pxx-xfer"),
        ("status image with the step-1 USB service", False,
         BASE + [("usr/bin/uc-pxx-inst", REG, STATUS_P10INST)] + xf, "is in the status image"),
        ("install image with the reader only (no writer)", True,
         inst + olib + [(cnw.OPTEE_DIR + "/" + n, OREG, b) for n, b in sorted(ofiles.items()) if n in cnw.OPTEE_READ_FILES],
         "missing: tee-supplicant-rw-real, tee_legacy_setlock"),
        ("install image with one writer file only", True,
         inst + olib + [(cnw.OPTEE_DIR + "/" + n, OREG, b) for n, b in sorted(ofiles.items()) if n != "tee_legacy_setlock"],
         "missing: tee_legacy_setlock"),
        ("install image with a writer file of another sha256", True,
         inst + olib + [(cnw.OPTEE_DIR + "/" + n, OREG, b + (b"x" if n == "tee-supplicant-rw-real" else b"")) for n, b in sorted(ofiles.items())],
         "not the pinned tee-supplicant-rw-real"),
        ("install image with a reader file of another sha256", True,
         inst + olib + [(cnw.OPTEE_DIR + "/" + n, OREG, b + (b"x" if n == "tee-supplicant-ro" else b"")) for n, b in sorted(ofiles.items())],
         "not the pinned tee-supplicant-ro"),
        ("install image with a reader file at mode 0755", True,
         inst + olib + [(cnw.OPTEE_DIR + "/" + n, REG if n == "tee_legacy_read" else OREG, b) for n, b in sorted(ofiles.items())],
         "mode 0755"),
        ("install image with the reader folder at 0755", True, inst + [("usr/lib", DIR, b""), (cnw.OPTEE_PARENT, DIR, b""),
         (cnw.OPTEE_DIR, DIR, b"")] + ofull[3:], "mode 0500"),
        ("install image with a sixth file in the reader folder", True, inst + ofull + [(cnw.OPTEE_DIR + "/x", OREG, b"x")],
         "nothing but the lock_state reader and writer files"),
        ("install image with a file next to the reader folder", True, inst + ofull + [(cnw.OPTEE_PARENT + "/y", OREG, b"y")],
         "nothing but the lock_state reader and writer files"),
        ("install image with two of the three reader files", True,
         inst + olib + [(cnw.OPTEE_DIR + "/" + n, OREG, b) for n, b in sorted(ofiles.items()) if n != "tee_legacy_read"],
         "missing: tee_legacy_read"),
        ("install image with a reader file owned by uid 1000", True,
         inst + olib + [(cnw.OPTEE_DIR + "/" + n, OREG, b, 1000 if n == "optee_legacy.ko" else 0) for n, b in sorted(ofiles.items())],
         "owner 1000:0"),
        ("install image with a reader file as a link", True,
         inst + olib + [(cnw.OPTEE_DIR + "/" + n, 0o120777 if n == "tee_legacy_read" else OREG, b"/bin/sh" if n == "tee_legacy_read" else b)
                        for n, b in sorted(ofiles.items())], "not a regular file"),
        ("install image with the reader's parent folder at 0700", True,
         inst + [("usr/lib", DIR, b""), (cnw.OPTEE_PARENT, 0o040700, b""), (cnw.OPTEE_DIR, ODIR, b"")] + ofull[3:], "must be 0755 0:0"),
        ("status image with the lock_state reader", False, BASE + [("usr/bin/uc-pxx-inst", REG, STATUS_P10INST)] + ofull,
         "the install image only"),
        # review MAINLINE-RC6-CODE-REVIEW MAJOR-2: N1 no optee folder; N2-N5 a writer file outside its pinned path
        ("N1 install image with no optee folder", True, inst, "optee folder of an install image must hold"),
        ("N2 install image with a copy of the writer supplicant at usr/bin", True,
         inst + ofull + [("usr/bin/helper", REG, ofiles["tee-supplicant-rw-real"])], "outside its pinned path"),
        ("N3 install image with a copy of the lock-state client at usr/sbin", True,
         inst + ofull + [("usr/sbin/helper", REG, ofiles["tee_legacy_setlock"])], "outside its pinned path"),
        # RC-6 flow review MINOR-D: a copy with another sha (one byte more) is still found by the writers' strings
        ("N15 install image with the writer supplicant plus one byte at usr/bin", True,
         inst + ofull + [("usr/bin/helper", REG, elf(b"copy", cnw.P16_WRITER_STRINGS[0], cnw.P16_WRITER_STRINGS[1]))], "lock-state writer string"),
        ("N16 install image with the lock-state client plus one byte at usr/sbin", True,
         inst + ofull + [("usr/sbin/helper", REG, elf(b"copy", cnw.P16_WRITER_STRINGS[2]))], "lock-state writer string"),
        ("N17 status image with the writer supplicant plus one byte", False,
         BASE + [("usr/bin/uc-pxx-inst", REG, STATUS_P10INST), ("usr/bin/x", REG, elf(b"copy", cnw.P16_WRITER_STRINGS[3]))],
         "lock-state writer string"),
        ("N4 status image with the writer supplicant at usr/bin", False,
         BASE + [("usr/bin/uc-pxx-inst", REG, STATUS_P10INST), ("usr/bin/tee-supplicant", REG, ofiles["tee-supplicant-rw-real"])],
         "outside its pinned path"),
        ("N5 status image with the lock-state client at usr/sbin", False,
         BASE + [("usr/bin/uc-pxx-inst", REG, STATUS_P10INST), ("usr/sbin/setlock", REG, ofiles["tee_legacy_setlock"])],
         "outside its pinned path"),
        ("N2b install image with the writer supplicant under the reader's name in the optee folder", True,
         inst + olib + [(cnw.OPTEE_DIR + "/" + n, OREG, ofiles["tee-supplicant-rw-real"] if n == "tee-supplicant-ro" else b)
                        for n, b in sorted(ofiles.items())], "outside its pinned path"),
    ]
    bad = 0
    # the three copies of the reader pins agree (C3-5): the real sources, then one copy changed
    saved = dict(cnw.OPTEE_FILES)
    cnw.OPTEE_FILES.clear(); cnw.OPTEE_FILES.update(REAL_OPTEE)
    errs = []
    cnw.check_reader_pins(errs)
    ok = not errs
    print("%s the reader pins of check-no-writer, mkinitramfs.sh and lockstate.go agree%s" % ("ok  " if ok else "FAIL", "" if ok else ": %s" % errs))
    bad += not ok
    cnw.OPTEE_FILES["tee_legacy_read"] = "0" * 64
    errs = []
    cnw.check_reader_pins(errs)
    ok = len(errs) == 2
    print("%s a changed reader pin is found in both other copies%s" % ("ok  " if ok else "FAIL", "" if ok else ": %s" % errs))
    bad += not ok
    cnw.OPTEE_FILES.clear(); cnw.OPTEE_FILES.update(saved)
    with tempfile.TemporaryDirectory() as d:
        for i, (name, inst, entries, want) in enumerate(cases):
            p = os.path.join(d, "%d.cpio" % i)
            cpio(p, entries)
            errs = verdict(p, inst)
            ok = (not errs) if not want else any(want in e for e in errs)
            print("%s %s%s" % ("ok  " if ok else "FAIL", name, "" if ok else ": %s" % errs))
            bad += not ok
    # --mainline-install on one binary: the mainline packages are required
    bins = [("a release install binary with the mainline install", INSTALL_P10INST + MAINLINE, ""),
            ("a release install binary without the mainline install", INSTALL_P10INST, "not an install build with the mainline"),
            ("a devtest binary with the mainline install", INSTALL_P10INST + MAINLINE + cnw.DEVTEST_MARKER, "devtest code")]
    for name, data, want in bins:
        errs = []
        cnw.check_mainline_install("uc-pxx-inst", data, errs)
        ok = (not errs) if not want else any(want in e for e in errs)
        print("%s --mainline-install: %s%s" % ("ok  " if ok else "FAIL", name, "" if ok else ": %s" % errs))
        bad += not ok
    # --step2 on one binary: the step-2 session and the slot side; never the RAM side or the decrypter
    S2 = (b"uc-pxx-installer/internal/step2.Start\0uc-pxx-installer/internal/device.SlotLive\0"
          b"uc-pxx-installer/internal/install.RunOtherSlot\0uc-pxx-installer/internal/plan.ComputeOtherSlot\0"
          b"uc-pxx-installer/internal/pcdfw.Segment.Name\0")
    s2bins = [("a release build of uc-pxx-step2", S2, ""),
              ("uc-pxx-step2 without the step-2 session", S2.replace(b"internal/step2.", b"internal/stepX."), "not a build of uc-pxx-step2"),
              ("uc-pxx-step2 with the device mode", S2 + b"uc-pxx-installer/internal/devmode.Install\0", "internal/devmode"),
              ("uc-pxx-step2 with the RAM flow", S2 + b"uc-pxx-installer/internal/flowio.Run\0", "internal/flowio"),
              ("uc-pxx-step2 with the drive reader", S2 + b"uc-pxx-installer/internal/fatfs.OpenImage\0", "internal/fatfs"),
              ("uc-pxx-step2 with the decrypter", S2 + b"uc-pxx-installer/internal/pcdfw.Open\0", "decrypter"),
              ("uc-pxx-step2 with the key search", S2 + b"uc-pxx-installer/internal/pcdfw.FindKeys\0", "decrypter"),
              ("uc-pxx-step2 with the devtest marker", S2 + cnw.DEVTEST_MARKER, "devtest code"),
              ("uc-pxx-step2 with the test gate", S2 + cnw.TEST_GATE_SYM, "TestGate"),
              ("uc-pxx-step2 with devicetest", S2 + b"uc-pxx-installer/internal/device/devicetest.New\0", "devicetest")]
    for name, data, want in s2bins:
        errs = []
        cnw.check_step2("uc-pxx-step2", data, errs)
        ok = (not errs) if not want else any(want in e for e in errs)
        print("%s --step2: %s%s" % ("ok  " if ok else "FAIL", name, "" if ok else ": %s" % errs))
        bad += not ok
    bins += s2bins
    # --xfer on one binary: the step-2 USB transport; no eMMC code at all
    XF = b"uc-pxx-installer/internal/xfer.(*Receiver).Serve\0uc-pxx-installer/internal/gadget.Bind\0" \
         b"uc-pxx-installer/internal/mainline.ParseManifest\0"
    xbins = [("a build of uc-pxx-xfer", XF, ""),
             ("uc-pxx-xfer without the receiver", XF.replace(b"internal/xfer.", b"internal/xfeX."), "not a build of uc-pxx-xfer"),
             ("uc-pxx-xfer without the gadget", XF.replace(b"internal/gadget.", b"internal/gadgeX."), "not a build of uc-pxx-xfer"),
             ("uc-pxx-xfer with the device layer", XF + b"uc-pxx-installer/internal/device.SlotLive\0", "internal/device"),
             ("uc-pxx-xfer with the install engine", XF + b"uc-pxx-installer/internal/install.RunOtherSlot\0", "internal/install"),
             ("uc-pxx-xfer with the A/B tool wrapper", XF + b"uc-pxx-installer/internal/bootctl.Run\0", "internal/bootctl"),
             ("uc-pxx-xfer with the A/B decoder", XF + b"uc-pxx-installer/internal/avbab.Parse\0", "internal/avbab"),
             ("uc-pxx-xfer with the step-2 engine", XF + b"uc-pxx-installer/internal/step2.Start\0", "internal/step2"),
             ("uc-pxx-xfer with the planner", XF + b"uc-pxx-installer/internal/plan.ComputeOtherSlot\0", "internal/plan"),
             ("uc-pxx-xfer with the block reader", XF + b"uc-pxx-installer/internal/blk.Size\0", "internal/blk"),
             ("uc-pxx-xfer with the step-2 set", XF + b"uc-pxx-installer/internal/mainline/step2set.Gate\0", "step2set"),
             ("uc-pxx-xfer with an eMMC node", XF + b"/dev/mmcblk0\0", "/dev/mmcblk"),
             ("uc-pxx-xfer with the boot-control string", XF + cnw.WRITER_ELF_STRINGS[0], "boot-control string"),
             ("uc-pxx-xfer with the devtest marker", XF + cnw.DEVTEST_MARKER, "devtest code"),
             ("uc-pxx-xfer with the test gate", XF + cnw.TEST_GATE_SYM, "TestGate"),
             ("uc-pxx-xfer with a package not on the allow-list (review F3)", XF + b"uc-pxx-installer/internal/rawdisk.Open\0",
              "not on the allow-list"),
             ("uc-pxx-xfer with a method of a package not on the allow-list", XF + b"uc-pxx-installer/internal/xfer/evil.(*W).Write\0",
              "not on the allow-list"),
             ("uc-pxx-xfer with the hand-over package (allowed)", XF + b"uc-pxx-installer/internal/xfer/handover.CheckReady\0", ""),
             ("uc-pxx-xfer with file names of its packages (allowed)", XF + b"uc-pxx-installer/internal/xfer/receiver.go\0", "")]
    for name, data, want in xbins:
        errs = []
        cnw.check_xfer("uc-pxx-xfer", data, errs)
        ok = (not errs) if not want else any(want in e for e in errs)
        print("%s --xfer: %s%s" % ("ok  " if ok else "FAIL", name, "" if ok else ": %s" % errs))
        bad += not ok
    bins += xbins
    # --provision on one binary: the first-boot provisioning (mainline-rc-10 R23); no eMMC writer
    PV = b"uc-pxx-installer/internal/provision.Run\0uc-pxx-installer/internal/provision/bundle.(*Unpacker).Write\0" \
         b"uc-pxx-installer/internal/clone.LiveContent\0uc-pxx-installer/internal/blk.(*Disk).GuardReadOnly\0" \
         b"uc-pxx-installer/internal/provision/emmc.Guard\0"
    pbins = [("a build of uc-pxx-provision", PV, ""),
             ("uc-pxx-provision without the service", PV.replace(b"internal/provision.", b"internal/provisioX."), "not a build of uc-pxx-provision"),
             ("uc-pxx-provision without the content check", PV.replace(b"internal/clone.", b"internal/clonX."), "not a build of uc-pxx-provision"),
             ("uc-pxx-provision with the device layer", PV + b"uc-pxx-installer/internal/device.SlotLive\0", "internal/device"),
             ("uc-pxx-provision with the install engine", PV + b"uc-pxx-installer/internal/install.RunClone\0", "internal/install"),
             ("uc-pxx-provision with the A/B tool wrapper", PV + b"uc-pxx-installer/internal/bootctl.Run\0", "internal/bootctl"),
             ("uc-pxx-provision with the step-2 engine", PV + b"uc-pxx-installer/internal/step2.Start\0", "internal/step2"),
             ("uc-pxx-provision with the planner", PV + b"uc-pxx-installer/internal/plan.ComputeClone\0", "internal/plan"),
             ("uc-pxx-provision with the difference writer", PV + b"uc-pxx-installer/internal/clonedelta.Apply\0", "internal/clonedelta"),
             ("uc-pxx-provision with the USB receiver", PV + b"uc-pxx-installer/internal/xfer.(*Receiver).Serve\0", "internal/xfer"),
             ("uc-pxx-provision with the key seed's mount", PV + b"uc-pxx-installer/internal/keywrite/mount.MountWrite\0", "keywrite/mount"),
             ("uc-pxx-provision with the bundle test helper", PV + b"uc-pxx-installer/internal/provision/bundle/bundletest.Make\0", "bundletest"),
             ("uc-pxx-provision with an eMMC node", PV + b"/dev/mmcblk0\0", "/dev/mmcblk"),
             ("uc-pxx-provision with the boot-control string", PV + cnw.WRITER_ELF_STRINGS[0], "boot-control string"),
             ("uc-pxx-provision with the lock-state writer string", PV + cnw.P16_WRITER_STRINGS[0], "lock-state writer"),
             ("uc-pxx-provision with the devtest marker", PV + cnw.DEVTEST_MARKER, "devtest code"),
             ("uc-pxx-provision with a package not on the allow-list", PV + b"uc-pxx-installer/internal/rawdisk.Open\0", "not on the allow-list"),
             ("uc-pxx-provision with the hand-over package (allowed)", PV + b"uc-pxx-installer/internal/xfer/handover.ReadChoice\0", ""),
             ("uc-pxx-provision with the A/B decoder (allowed: parse only)", PV + b"uc-pxx-installer/internal/avbab.Parse\0", "")]
    for name, data, want in pbins:
        errs = []
        cnw.check_provision("uc-pxx-provision", data, errs)
        ok = (not errs) if not want else any(want in e for e in errs)
        print("%s --provision: %s%s" % ("ok  " if ok else "FAIL", name, "" if ok else ": %s" % errs))
        bad += not ok
    bins += pbins
    # the source rules of the provisioning: made-up lines
    srcs = [('return syscall.Mount("x", d, "tmpfs", 0, "")', ""), ('f, _ := os.OpenFile("/dev/mmcblk0", os.O_RDWR, 0)', "block device node"),
            ('const t = "/dev/tty1"', ""), ('const n = "/dev/null"', "names /dev"), ('flags := os.O_RDONLY | syscall.O_DIRECT', "O_DIRECT"),
            ('ioctl(fd, BLKROSET, 0)', "block ioctl"), ('exec.Command("/sbin/uc-pxx-bootctl", "status")', "A/B or disk tool"),
            ('syscall.Mount(n, d, "ext4", 0, "")', "not the tmpfs"), ('exec.Command("/usr/bin/curl")', "names the program"),
            ('argv := []string{"/sbin/apk", "add"}', "")]
    for line, want in srcs:
        errs = []
        for rx, why in cnw.PROV_BAD_SRC:
            if rx.search(line):
                errs.append(why)
        for m in cnw.PROV_PROGRAM_RE.finditer(line):
            if m.group(1) not in cnw.PROV_PROGRAMS:
                errs.append("names the program " + m.group(1))
        ok = (not errs) if not want else any(want in e for e in errs)
        print("%s provision source: %s%s" % ("ok  " if ok else "FAIL", line, "" if ok else ": %s" % errs))
        bad += not ok
        bins.append((line, b"", want))
    # the provisioning's import and source rules on whole files (review MAJOR-1: the reviewer's planted A/B write
    # through internal/blk in internal/provision/live.go must fail, and the Join("/dev", …) form)
    planted = ('package provision\n\nimport (\n\t"os"\n\n\t"uc-pxx-installer/internal/blk"\n)\n\n'
               'func liveGuard(slot string) (func() error, error) {\n\td, _ := blk.Find("/")\n\tp, _ := d.Part("misc")\n'
               '\tf, _ := os.OpenFile(d.DevPath(p.Dev), os.O_WRONLY, 0)\n\tf.WriteAt(make([]byte, 512), 4*512)\n\treturn nil, nil\n}\n')
    files = [("internal/provision", "live.go", planted, "imports internal/blk"),
             ("internal/provision", "x.go", 'package provision\n\nvar n = filepath.Join("/dev", "mmcblk0p2")\n', "names /dev"),
             ("internal/provision", "y.go", 'package provision\n\nvar n = filepath.Join("/", "dev", name)\n', "names /dev"),
             ("cmd/uc-pxx-provision", "main.go", 'package main\n\nimport "uc-pxx-installer/internal/blk"\n', "imports internal/blk"),
             ("internal/step2", "z.go", 'package step2\n\nimport "uc-pxx-installer/internal/provision/emmc"\n', "imports internal/provision/emmc"),
             ("internal/provision/emmc", "emmc.go", 'package emmc\n\nimport "uc-pxx-installer/internal/blk"\n\nvar _ = blk.Find\n', ""),
             ("internal/provision", "ok.go", 'package provision\n\nimport "uc-pxx-installer/internal/provision/emmc"\n\nvar _ = emmc.Guard\n', "")]
    for rel_dir, f, text, want in files:
        errs = cnw.prov_file_errors(rel_dir, f, text)
        ok = (not errs) if not want else any(want in e for e in errs)
        print("%s provision file: %s/%s%s" % ("ok  " if ok else "FAIL", rel_dir, f, "" if ok else ": %s" % errs))
        bad += not ok
        bins.append((f, b"", want))
    print("check-no-writer tests: %d cases, %d failed (%s bootctl)" % (len(cases) + len(bins) + 2, bad, "real" if real else "made-up"))
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
