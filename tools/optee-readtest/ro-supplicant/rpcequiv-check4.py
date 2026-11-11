#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""rpcequiv-check4.py OLD.out NEW.out -- the round-4 comparison of two rpcequiv outputs (built with -DCLASS4).

OLD = the round-3 sources (ref3/, binary afaf1778...), NEW = the round-4 sources (the LS supplicant). Test
code only. Exit 0 only if:
  - both runs have the same lines (count, order, tags, keys) and both ended with the same END line;
  - every '-' line and every 'R' line (the seek resync) is byte-identical (all else is unchanged);
  - every 'C' line (FS OPEN, flags neither exactly RDONLY 0x1 nor exactly RDWR 0x4, neither CREAT nor
    APPEND) of NEW is the gate refusal: its return, errno, ioctl, reply bytes and FS res/fd equal NEW's own
    OPEN with flags CREAT (0x8) of the same store/name/fd/len/cap (a '-' line: the gate refused it in both
    rounds; the ioctl's padding_buf, the harness's per-case tag, is not compared), and the FS res is -1.
It prints how the 'C' lines changed (OLD served / OLD not found or error -> NEW refused at the gate).
"""
import re
import sys
from collections import Counter

LINE = re.compile(r'^([-CR]) (\d+) r=(-?\d+) e=(\d+) io=(\S+) w=(\S+) shm=([0-9a-f]{16}) (fs=\S+)(?: key=(\S+))?$')


def parse(path):
    rows, end = [], None
    with open(path) as f:
        for ln in f:
            ln = ln.rstrip('\n')
            if ln.startswith('END '):
                end = ln
                continue
            m = LINE.match(ln)
            if not m:
                sys.exit(f'{path}: unparsable line: {ln[:120]}')
            rows.append((ln, m.groups()))
    if end is None:
        sys.exit(f'{path}: no END line')
    return rows, end


def main():
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    old, old_end = parse(sys.argv[1])
    new, new_end = parse(sys.argv[2])
    if old_end != new_end or len(old) != len(new):
        sys.exit(f'FAIL: different lengths / END lines: {old_end!r} vs {new_end!r}')
    by_key = {g[8]: g for _, g in new if g[8]}
    tags, how, flags = Counter(), Counter(), Counter()
    for (lo, go), (ln, gn) in zip(old, new):
        if go[0] != gn[0] or go[1] != gn[1] or go[8] != gn[8]:
            sys.exit(f'FAIL: the sequences differ in tag/number/key:\n  {lo}\n  {ln}')
        tag = go[0]
        tags[tag] += 1
        if tag in '-R':
            if lo != ln:
                sys.exit(f'FAIL: a {tag!r} line differs (outside the round-4 class):\n  {lo}\n  {ln}')
            continue
        k = dict(re.match(r'^(op|[vfndlc])(.+)$', p).groups() for p in gn[8].split(','))
        if k['op'] != '1':
            sys.exit(f'FAIL: a C line that is not an OPEN: {ln}')
        twin = 'v%s,op1,f8,n%s,d%s,l%s,c%s' % (k['v'], k['n'], k['d'], k['l'], k['c'])
        t = by_key.get(twin)
        if t is None or t[0] != '-':
            sys.exit(f'FAIL: no CREAT twin line for {gn[8]}')
        # io=CALLS{SIZE,FLAGS,PADDING}: PADDING is the harness's per-case tag (0x9e0b000 + case number)
        io = lambda x: x.rsplit(',', 1)[0]
        if (gn[2], gn[3], io(gn[4]), gn[5], gn[7]) != (t[2], t[3], io(t[4]), t[5], t[7]):
            sys.exit(f'FAIL: a C line is not the gate refusal (its CREAT twin):\n  {ln}\n  {t}')
        if not (gn[7] == 'fs=-' or gn[7].startswith('fs=-1/')):
            sys.exit(f'FAIL: a C line with FS res other than -1: {ln}')
        flags[k['f']] += 1
        if lo == ln:
            how['identical (round 3 gave the same reply)'] += 1
        elif re.match(r'fs=(\d+)/\1$', go[7]):
            how['round 3 served the OPEN -> round 4 refused at the gate'] += 1
        else:
            how['round 3 not found / error (after a table read) -> round 4 refused at the gate'] += 1
    print(f'   lines: {sum(tags.values())} ("-" {tags["-"]}, "C" {tags["C"]}, "R" {tags["R"]}); {new_end}')
    print('   every "-" line and every "R" (seek resync) line: identical to round 3')
    print(f'   every "C" line of round 4 = the gate refusal (= its OPEN CREAT twin): {tags["C"]}')
    for h, n in sorted(how.items()):
        print(f'     {n:7d}  {h}')
    print('   "C" flag values: ' + ' '.join(f'0x{f}:{n}' for f, n in sorted(flags.items(), key=lambda x: int(x[0], 16))))
    return 0


if __name__ == '__main__':
    sys.exit(main())
