#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""rpcequiv-check.py OLD.out NEW.out -- the round-3 comparison of two rpcequiv outputs.

OLD = the round-2 sources (ref2/, binary 9200e2f0...), NEW = the round-3 sources. Test code only.
Exit 0 only if:
  - both runs have the same lines (count, order, tags, keys) and both ended with the END line;
  - every '-' line and every 'R' line (the seek resync) is byte-identical;
  - the 'C' lines (FS OPEN, an access mode WRONLY/RDWR, neither CREAT nor APPEND) that differ are
    EXACTLY the predicted set: store v0 (the good store), a name of a file that exists there
    (testobj fd 0, markobj fd 1, odd fd 2 -- used=2 is found by name, as the vendor --, dir/file fd 3),
    and a payload cap that holds the whole name with its NUL. Every other 'C' line is identical;
  - in each differing line OLD answered fs res -1 (the round-2 write gate) and NEW answered
    res = fd = the file's fd, with r/e/w (return, errno, the reply bytes) equal to OLD's, and r/e/w/fs
    equal to NEW's own OPEN RDONLY (flags 0x1) of the same store/name/fd/len/cap: an OPEN RDWR is
    now answered exactly as an OPEN RDONLY was and is.
It prints the changed-case table (flags x name -> count).
"""
import re
import sys
from collections import Counter

LINE = re.compile(r'^([-CR]) (\d+) r=(-?\d+) e=(\d+) io=(\S+) w=(\S+) shm=([0-9a-f]{16}) (fs=\S+)(?: key=(\S+))?$')
NAMES = ['testobj', 'markobj', 'nope', 'dir', 'dir/file', 'odd', '""', 'n*200', 'unterminated']
EXIST_FD = {0: 0, 1: 1, 5: 2, 4: 3}            # name index -> fd in store v0
NAME_LEN = {0: 8, 1: 8, 5: 4, 4: 9}            # with the NUL
LENS = [0, 16, 180, 600, 70000]


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


def key_fields(k):
    out = {}
    for part in k.split(','):
        m = re.match(r'^(op|[vfndlc])(.+)$', part)
        out[m.group(1)] = m.group(2)
    return {'v': int(out['v']), 'op': int(out['op']), 'f': int(out['f'], 16) if out['f'] != 'ffffffff' else -1,
            'n': int(out['n']), 'd': int(out['d']), 'l': int(out['l']), 'c': int(out['c'])}


def predicted(k):
    if k['v'] != 0 or k['n'] not in EXIST_FD:
        return False
    length = LENS[k['l']]
    cap = length if k['c'] == 0 else 4072
    cap = min(cap, 80000 - 64)
    return cap >= NAME_LEN[k['n']]


def main():
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    old, old_end = parse(sys.argv[1])
    new, new_end = parse(sys.argv[2])
    if old_end != new_end or len(old) != len(new):
        sys.exit(f'FAIL: different lengths / END lines: {old_end!r} vs {new_end!r}')
    tags = Counter()
    changed = Counter()
    same_c = Counter()
    new_by_key = {}
    for ln, g in new:
        if g[8]:
            new_by_key[g[8]] = g
    for (lo, go), (ln, gn) in zip(old, new):
        if go[0] != gn[0] or go[1] != gn[1] or go[8] != gn[8]:
            sys.exit(f'FAIL: the sequences differ in tag/number/key:\n  {lo}\n  {ln}')
        tag = go[0]
        tags[tag] += 1
        if tag in '-R':
            if lo != ln:
                sys.exit(f'FAIL: a {tag!r} line differs (outside the round-3 class):\n  {lo}\n  {ln}')
            continue
        k = key_fields(go[8])
        exp = predicted(k)
        if lo == ln:
            if exp:
                sys.exit(f'FAIL: a predicted change did not happen:\n  {lo}')
            same_c['v%d' % k['v'] if k['v'] != 0 else 'v0 ' + NAMES[k['n']]] += 1
            continue
        if not exp:
            sys.exit(f'FAIL: a C line changed outside the predicted set:\n  {lo}\n  {ln}')
        fd = EXIST_FD[k['n']]
        if not go[7].startswith('fs=-1/'):
            sys.exit(f'FAIL: round 2 did not answer res -1:\n  {lo}')
        if gn[7] != 'fs=%d/%d' % (fd, fd):
            sys.exit(f'FAIL: round 3 did not answer "served, fd {fd}":\n  {ln}')
        if (go[2], go[3], go[5]) != (gn[2], gn[3], gn[5]):
            sys.exit(f'FAIL: r/e/w differ (the RPC reply itself must be equal):\n  {lo}\n  {ln}')
        rk = 'v%d,op1,f1,n%d,d%d,l%d,c%d' % (k['v'], k['n'], k['d'], k['l'], k['c'])
        ro = new_by_key.get(rk)
        if ro is None or (ro[2], ro[3], ro[5], ro[7]) != (gn[2], gn[3], gn[5], gn[7]):
            sys.exit(f'FAIL: OPEN flags 0x{k["f"]:x} is not answered as the OPEN RDONLY of the same case:\n'
                     f'  {ln}\n  {ro}')
        changed[(k['f'], NAMES[k['n']])] += 1
    print(f'   lines: {sum(tags.values())} ("-" {tags["-"]}, "C" {tags["C"]}, "R" {tags["R"]}); {new_end}')
    print('   every "-" line and every "R" (seek resync) line: identical')
    print(f'   "C" lines identical: {sum(same_c.values())} '
          f'({", ".join(f"{k} {v}" for k, v in sorted(same_c.items()))})')
    print(f'   "C" lines CHANGED: {sum(changed.values())} -- exactly the predicted set (store v0, an existing file,'
          ' the whole name in the payload); each: round 2 res -1 -> round 3 "served, fd N", reply = OPEN RDONLY:')
    flags = sorted({f for f, _ in changed})
    names = [n for n in NAMES if any(n == m for _, m in changed)]
    print('     %-10s %s' % ('flags', ' '.join('%-9s' % n for n in names)))
    for f in flags:
        print('     0x%-8x %s' % (f, ' '.join('%-9d' % changed[(f, n)] for n in names)))
    return 0


if __name__ == '__main__':
    sys.exit(main())
