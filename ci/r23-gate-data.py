#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""The release build's data of the R23 clone gate (mainline-rc-10; R23-FORMATS §4 and §9.1).

  ci/r23-gate-data.py profiles --refs REFS.txt --xfer UC-PXX-XFER > internal/clone/gate-profiles.json
      The profiles of the release from refs.txt of the R23 reference build (uc-pxx-linux rootfs/r23-refs.sh:
      R23_RELEASE, REF_A_*, REF_B_*, BUNDLE_*) and the sha256 of the uc-pxx-xfer of the base. Every
      uc-pxx-provision and uc-pxx-step2 built after it pins exactly these profiles.
  ci/r23-gate-data.py keeper --provision UC-PXX-PROVISION > internal/step2/gate-keeper.json
      The pin of uc-pxx-provision (its size and sha256) for the build of uc-pxx-step2 (after uc-pxx-provision
      was built with the profiles above).

The order of a release: uc-pxx-xfer; the base tree; the references (refs.txt); profiles; uc-pxx-provision; keeper;
uc-pxx-step2; the base image. The output is the canonical form of the uc-pxx-linux tools
(json.dumps(indent=1, sort_keys=True, ensure_ascii=True) + newline). Standard library only; run with python3 -I.
"""
import hashlib
import json
import re
import sys

NAMES = ("console", "kiosk", "kiosk-ha")
SHA = re.compile(r"^[0-9a-f]{64}$")


def die(msg):
    print("r23-gate-data: " + msg, file=sys.stderr)
    sys.exit(1)


def sha_file(p):
    h, n = hashlib.sha256(), 0
    with open(p, "rb") as f:
        for b in iter(lambda: f.read(1 << 20), b""):
            h.update(b)
            n += len(b)
    return h.hexdigest(), n


def refs(path):
    out = {}
    with open(path) as f:
        for line in f:
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            k, sep, v = line.partition("=")
            if not sep or not re.match(r"^[A-Z0-9_]+$", k) or k in out:
                die("refs.txt: the line %r" % line)
            out[k] = v
    return out


def canon(doc):
    return json.dumps(doc, indent=1, sort_keys=True, ensure_ascii=True) + "\n"


def profiles(a):
    if len(a) != 4 or a[0] != "--refs" or a[2] != "--xfer":
        die("profiles --refs REFS.txt --xfer UC-PXX-XFER")
    r = refs(a[1])
    rel = r.get("R23_RELEASE", "")
    if not re.match(r"^[A-Za-z0-9._+-]{1,64}$", rel):
        die("refs.txt: R23_RELEASE %r" % rel)
    xsha, _ = sha_file(a[3])
    out = []
    for n in NAMES:
        k = n.upper().replace("-", "_")
        if "REF_B_" + k not in r:
            continue
        p = {"name": n, "build_id": "%s-%s\n" % (rel, n), "ref_a_sha256": r.get("REF_A_" + k, ""),
             "ref_b_sha256": r["REF_B_" + k], "bundle_sha256": r.get("BUNDLE_" + k, "")}
        for f in ("ref_a_sha256", "ref_b_sha256"):
            if not SHA.match(p[f]):
                die("refs.txt: %s of %s is %r" % (f, n, p[f]))
        if (n == "console") != (p["bundle_sha256"] == "") or p["bundle_sha256"] and not SHA.match(p["bundle_sha256"]):
            die("refs.txt: the bundle of %s is %r" % (n, p["bundle_sha256"]))
        out.append(p)
    if not out:
        die("refs.txt names no profile")
    sys.stdout.write(canon({"schema": 1, "kind": "uc-pxx-clone-gate-profiles", "release": rel, "xfer_sha256": xsha, "profiles": out}))


def keeper(a):
    if len(a) != 2 or a[0] != "--provision":
        die("keeper --provision UC-PXX-PROVISION")
    s, n = sha_file(a[1])
    sys.stdout.write(canon({"schema": 1, "kind": "uc-pxx-clone-gate-keeper", "keeper_files": [
        {"path": "/usr/libexec/uc-pxx-provision/uc-pxx-provision", "size": n, "sha256": s}]}))


def main(argv):
    if not argv or argv[0] not in ("profiles", "keeper"):
        sys.stderr.write(__doc__)
        return 2
    (profiles if argv[0] == "profiles" else keeper)(argv[1:])
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
