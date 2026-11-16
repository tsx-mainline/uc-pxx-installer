#!/usr/bin/env python3
# testdata/gen.py BUNDLE_PY OUTDIR: makes py-bundle.tar and py-bundle.json with
# the tar code of uc-pxx-linux tools/mainline-payload/bundle.py (write_bundle,
# tarinfo) and its bundle.json form (json.dumps(indent=1, sort_keys=True,
# ensure_ascii=True) + "\n"), from made-up indexes and packages: the bytes of
# a Python-made bundle for TestPythonBundle. Run with python3 -I.
import hashlib, importlib.util, io, json, os, sys, tarfile

spec = importlib.util.spec_from_file_location("bundle", sys.argv[1])
bp = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bp)
out = sys.argv[2]
br = "v3.24"

def gz_index(pkgs):
    txt = "".join("C:Q1fake%s=\nP:%s\nV:%s\nA:aarch64\nS:%d\nI:%d\n\n" % (n, n, v, len(d), i) for n, v, d, i in pkgs).encode()
    parts = []
    for name, data in ((".SIGN.RSA.fake.rsa.pub", b"sig"), ("APKINDEX", txt)):
        t = io.BytesIO()
        with tarfile.open(fileobj=t, mode="w", format=tarfile.USTAR_FORMAT) as tf:
            ti = tarfile.TarInfo(name); ti.size = len(data); ti.mtime = 0
            tf.addfile(ti, io.BytesIO(data))
        raw = t.getvalue()
        if name != "APKINDEX":
            raw = raw[:512 + (len(data) + 511) // 512 * 512]  # no end of archive, as abuild's signature part
        z = io.BytesIO()
        import gzip
        with gzip.GzipFile(fileobj=z, mode="wb", mtime=0) as g:
            g.write(raw)
        parts.append(z.getvalue())
    return b"".join(parts)

pk = {"main": [("musl-utils", "1.2.5-r10", b"m" * 777, 9000)],
      "community": [("chromium", "142.0.1-r0", b"c" * 140000, 500000), ("a" * 70 + "-long-name", "1.0-r0", b"l" * 5, 10)],
      "common": [("tsx-kiosk", "0.3.13_pre20261113-r0", b"k" * 2048, 40960)],
      "uc-pxx": [("uc-pxx-kiosk", "0.2.0-r0", b"u" * 1, 4096)]}
idx, lines, members = [], [], {}
for repo in ("main", "community", "common", "uc-pxx"):
    data = gz_index(pk[repo])
    rel = "%s/%s/aarch64/APKINDEX.tar.gz" % (br, repo)
    members[rel] = data
    idx.append({"repo": repo, "path": rel, "size": len(data), "sha256": hashlib.sha256(data).hexdigest()})
    for n, v, d, i in pk[repo]:
        rel = "%s/%s/aarch64/%s-%s.apk" % (br, repo, n, v)
        members[rel] = d
        lines.append({"name": n, "version": v, "repo": repo, "path": rel, "size": len(d), "sha256": hashlib.sha256(d).hexdigest(),
                      "checksum": "Q1fake%s=" % n, "scripts": [".post-install"] if n == "tsx-kiosk" else [],
                      "triggers": ["/usr/share/fonts/*"] if n == "chromium" else []})
lines.sort(key=lambda d: d["path"])
lva = b"lva tar bytes" * 100
members["lva.tar"] = lva
doc = {"schema": 1, "kind": "uc-pxx-profile-bundle", "release": "0.2.0-rc10", "profile": "kiosk-ha", "alpine": br, "arch": "aarch64",
       "indexes": idx, "packages": lines, "lva": {"path": "lva.tar", "size": len(lva), "sha256": hashlib.sha256(lva).hexdigest()},
       "add": ["tsx-kiosk", "uc-pxx-kiosk"], "world": sorted(["tsx-kiosk", "uc-pxx-kiosk"]),
       "base": sorted(["busybox=1.37.0-r20", "musl=1.2.5-r10"])}
js = (json.dumps(doc, indent=1, sort_keys=True, ensure_ascii=True) + "\n").encode("ascii")
order = [("bundle.json", js)] + [(i["path"], members[i["path"]]) for i in idx] + [(d["path"], members[d["path"]]) for d in lines] + [("lva.tar", lva)]
tar = os.path.join(out, "py-bundle.tar")
bp.write_bundle(tar, order)
with open(os.path.join(out, "py-bundle.json"), "wb") as f:
    f.write(js)
print(hashlib.sha256(open(tar, "rb").read()).hexdigest(), os.path.getsize(tar))
