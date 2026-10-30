#!/usr/bin/env python3
"""Fail if the firmware decryption key is anywhere in the repository.

The repository must never hold the pcdfw head key (see README.md). The
installer finds the key on the unit itself. This check cannot hold the key
either, so it holds only a 32-bit fingerprint: the first 4 bytes of sha256 of
the key, as raw bytes and as lowercase hex. 32 bits say nothing useful about
a 64-bit key, and a random 16-byte window matches with a chance of 2^-32.

The check hashes every 16-byte window of every file (the key as raw bytes)
and every 32-character hex window, lowercased (the key as hex, as in a key
file). The IV is not checked: it is the vendor's name, and a text match would
fire on every mention of the vendor.

  ci/check-no-key.py [--history] [PATH...]

Without PATH it scans the files that git tracks plus the untracked files that
are not ignored. A PATH that is a directory is every file under it. A PATH
that is not a file or a directory is an error, not a skip. --history also
scans every blob in the git history, so a key
that was committed and later removed still fails.
"""
import hashlib
import os
import subprocess
import sys

FP_RAW = "f3b5c5f1"
FP_HEX = "b59af4e1"
HEX = set(b"0123456789abcdefABCDEF")


def hits(data):
    """Offsets where a window matches a fingerprint."""
    out = []
    n = len(data)
    for i in range(n - 15):
        if hashlib.sha256(data[i:i + 16]).hexdigest()[:8] == FP_RAW:
            out.append(("raw", i))
    run = 0
    for i, b in enumerate(data):
        run = run + 1 if b in HEX else 0
        if run >= 32:
            w = data[i - 31:i + 1].lower()
            if hashlib.sha256(w).hexdigest()[:8] == FP_HEX:
                out.append(("hex", i - 31))
    return out


def git(*args, inp=None):
    return subprocess.run(["git", *args], capture_output=True, check=True, input=inp).stdout


def main():
    args = sys.argv[1:]
    history = "--history" in args
    # A named PATH is relative to the caller's directory: resolve it before the chdir.
    named = [os.path.abspath(a) for a in args if a != "--history"]
    missing = [p for p in named if not os.path.isfile(p) and not os.path.isdir(p)]
    for p in missing:
        print("FAIL: not a file or directory: %s" % p)
    if missing:
        sys.exit(1)
    # A named directory (a support bundle) is every file under it.
    paths = []
    for p in named:
        if os.path.isdir(p):
            for d, _, files in sorted(os.walk(p)):
                paths += [os.path.join(d, f) for f in sorted(files) if os.path.isfile(os.path.join(d, f))]
        else:
            paths.append(p)
    paths = list(dict.fromkeys(paths))
    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    os.chdir(root)
    found = 0
    if not named:
        # A tracked file can be deleted in the work tree; skip only those.
        paths = [p for p in git("ls-files", "-z", "--cached", "--others", "--exclude-standard").decode().split("\0")
                 if p and os.path.isfile(p)]
    for p in paths:
        with open(p, "rb") as f:
            data = f.read()
        for kind, off in hits(data):
            print("FOUND key (%s) in %s at offset %d" % (kind, p, off))
            found += 1
    scanned = len(paths)
    if history:
        objs = git("rev-list", "--objects", "--all").decode().split("\n")
        shas = [o.split(" ", 1) for o in objs if o]
        blobs = [s for s in shas if git("cat-file", "-t", s[0]).strip() == b"blob"]
        for s in blobs:
            for kind, off in hits(git("cat-file", "blob", s[0])):
                print("FOUND key (%s) in history blob %s (%s) at offset %d"
                      % (kind, s[0], s[1] if len(s) > 1 else "?", off))
                found += 1
        scanned += len(blobs)
    if found:
        print("FAIL: the decryption key is in the repository (%d hit(s)). Remove it, and from the history too." % found)
        sys.exit(1)
    print("ok: no decryption key in %d file(s)%s" % (scanned, " and blobs" if history else ""))


if __name__ == "__main__":
    main()
