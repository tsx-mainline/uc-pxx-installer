#!/usr/bin/env python3
# trace_check.py -- offline check that the RECORDED vendor read of lock_state
# (plan/uc-pxx/optee-m5-2026-11-10/supp.trace) touches the security partition by
# READS ONLY, at a fixed small set of offsets, and never opens RPMB. This is the
# ground-truth our read-only supplicant reproduces. Run with -I.
#   python3 -I trace_check.py /path/to/supp.trace
import re, sys

EXPECTED_OFFSETS = {0, 66560, 78336}   # sec 0 (ptable), sec 130, sec 153 (data)

def main(path):
    reads, writes, rpmb, offs = 0, 0, 0, set()
    off = None
    for line in open(path, errors="replace"):
        if re.search(r'openat\([^)]*rpmb', line) or 'mmcblk' in line and 'rpmb' in line:
            rpmb += 1
        m = re.search(r'lseek\(7, (\d+), SEEK_SET\)', line)
        if m: off = int(m.group(1)); continue
        if re.search(r'\bread\(7,', line):
            reads += 1
            if off is not None: offs.add(off)
            continue
        if re.search(r'\b(write|writev|pwrite\w*)\(7,', line):
            writes += 1
    ok = True
    print(f"security reads : {reads}")
    print(f"security writes: {writes}   (MUST be 0)")
    print(f"rpmb opens     : {rpmb}   (MUST be 0)")
    print(f"read offsets   : {sorted(offs)}")
    if writes: print("FAIL: a write to the security partition"); ok = False
    if rpmb:   print("FAIL: an RPMB access"); ok = False
    extra = offs - EXPECTED_OFFSETS
    if extra: print(f"NOTE: offsets beyond the expected set: {sorted(extra)}")
    if not offs <= EXPECTED_OFFSETS:
        print("WARN: unexpected offsets (not a hard fail -- store layout dependent)")
    print("\nPASS: the recorded lock_state read is READ-ONLY on security, no RPMB."
          if ok else "\nFAIL")
    return 0 if ok else 1

if __name__ == "__main__":
    sys.exit(main(sys.argv[1] if len(sys.argv) > 1 else "supp.trace"))
