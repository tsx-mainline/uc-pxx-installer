// SPDX-License-Identifier: GPL-2.0-or-later
// download.js (W4): the chosen-file check against a pin, the same-origin
// download with progress, the part download into a fake private storage (off
// in this page version: PARTS.PAYLOAD_URL null), the delete, and (1.3.1) the
// gzip-encoded responses of GitHub Pages on every download path.
import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash, randomBytes } from "node:crypto";
import { gzipSync } from "node:zlib";
import { bodyLength, checkFile, deleteDownloads, downloadPart, fetchBase, fetchBytes, folderBase, OPFS_DIR, parseBase, storageFree } from "../../site/js/download.js";
import { checkImage } from "../../site/js/enter.js";
import { checkSet, FILES, LIST } from "../../site/js/finishusb.js";

const sha = (b) => createHash("sha256").update(b).digest("hex");

test("checkFile: the pin's size first, then the sha256; a placeholder pin refuses every file", async () => {
  const b = randomBytes(300000);
  const pin = { size: b.length, sha256: sha(b) };
  const seen = [];
  assert.deepEqual(await checkFile(new Blob([b]), pin, (d) => seen.push(d)), { ok: true, sha256: sha(b) });
  assert.ok(seen.at(-1) === b.length);
  let hashed = 0;
  const count = async (f) => { hashed++; return { sha256: "x", size: f.size }; };
  const r1 = await checkFile(new Blob([b.subarray(1)]), pin, () => {}, count);
  assert.equal(r1.ok, false);
  assert.match(r1.why, /bytes, the pin says/);
  assert.equal(hashed, 0, "a wrong size is refused before any byte is hashed");
  const c = Buffer.from(b); c[100] ^= 1;
  const r2 = await checkFile(new Blob([c]), pin);
  assert.equal(r2.ok, false);
  assert.match(r2.why, /is not the pin/);
  for (const p of [{ size: 1, sha256: "PART1-SHA256-PLACEHOLDER" }, { size: null, sha256: null }, null]) {
    const r = await checkFile(new Blob([b]), p, () => {}, count);
    assert.equal(r.ok, false);
    assert.match(r.why, /pins no such file/);
  }
  assert.equal(hashed, 0);
});

function response(body, { status = 200, len = true, cutAt = -1 } = {}) {
  let sent = 0;
  return {
    ok: status >= 200 && status < 300, status,
    headers: { get: (k) => (k === "content-length" && len ? String(body.length) : null) },
    body: { getReader: () => ({ async read() {
      if (sent >= body.length) return { done: true };
      if (cutAt >= 0 && sent >= cutAt) throw new TypeError("network error");
      const n = Math.min(65536, body.length - sent);
      const v = body.subarray(sent, sent + n);
      sent += n;
      return { done: false, value: v };
    } }) },
  };
}

test("fetchBytes: the bytes with progress; a 404 is missing; a cut or a network error is not", async () => {
  const b = randomBytes(200000);
  const calls = [];
  const seen = [];
  const r = await fetchBytes("images/x.img", (d, t) => seen.push([d, t]), async (u, o) => { calls.push([u, o]); return response(b); });
  assert.equal(r.ok, true);
  assert.equal(sha(r.bytes), sha(b));
  assert.deepEqual(seen.at(-1), [b.length, b.length]);
  assert.deepEqual(calls[0][1], { cache: "no-store", credentials: "omit", referrerPolicy: "no-referrer" });
  assert.deepEqual(await fetchBytes("x", () => {}, async () => response(b, { status: 404 })), { ok: false, missing: true, why: "HTTP 404" });
  const cut = await fetchBytes("x", () => {}, async () => response(b, { cutAt: 70000 }));
  assert.equal(cut.ok, false);
  assert.equal(cut.missing, false);
  const net = await fetchBytes("x", () => {}, async () => { throw new TypeError("Failed to fetch"); });
  assert.equal(net.ok, false);
  assert.equal(net.missing, false);
});

// a fake private storage (OPFS): directories and files in memory
function fakeStorage() {
  const mkdir = () => {
    const kids = new Map();
    const dir = {
      kids,
      async getDirectoryHandle(n, o) {
        if (!kids.has(n)) {
          if (!(o && o.create)) throw new DOMException("x", "NotFoundError");
          kids.set(n, mkdir());
        }
        return kids.get(n);
      },
      async getFileHandle(n, o) {
        if (!kids.has(n)) {
          if (!(o && o.create)) throw new DOMException("x", "NotFoundError");
          kids.set(n, { bytes: new Uint8Array(0) });
        }
        const f = kids.get(n);
        return {
          async getFile() { return new Blob([f.bytes]); },
          async createWritable() {
            const ch = [];
            return { async write(v) { ch.push(Buffer.from(v)); }, async close() { f.bytes = Buffer.concat(ch); }, async abort() { f.aborted = true; } };
          },
        };
      },
      async removeEntry(n, o) {
        if (!kids.has(n)) throw new DOMException("x", "NotFoundError");
        assert.deepEqual(o, { recursive: true });
        kids.delete(n);
      },
    };
    return dir;
  };
  const root = mkdir();
  return { root, async getDirectory() { return root; }, async estimate() { return { quota: 5e9, usage: 1e9 }; } };
}

test("downloadPart: the pin first, then the bytes into the private storage, then the check; reuse; delete", async () => {
  const b = randomBytes(150000);
  const part = { file: "p1.zst", size: b.length, sha256: sha(b) };
  const st = fakeStorage();
  const urls = [];
  const f = async (u, o) => { urls.push([u, o.redirect]); return response(b); };
  const r = await downloadPart("https://dl.example/rc10/", part, { fetchFn: f, storage: st });
  assert.equal(r.ok, true);
  assert.deepEqual(urls, [["https://dl.example/rc10/p1.zst", "error"]]);
  assert.equal(sha(Buffer.from(await r.file.arrayBuffer())), sha(b));
  // a second run reuses the checked file: no fetch
  const r2 = await downloadPart("https://dl.example/rc10/", part, { fetchFn: async () => assert.fail("fetched"), storage: st });
  assert.equal(r2.reused, true);
  // the server's size is not the pin's: refused before the body is read
  const st2 = fakeStorage();
  const r3 = await downloadPart("https://dl.example/rc10/", { ...part, size: b.length + 1 }, { fetchFn: f, storage: st2 });
  assert.equal(r3.ok, false);
  assert.match(r3.why, /offers/);
  // a changed body: refused after the check
  const c = Buffer.from(b); c[7] ^= 1;
  const r4 = await downloadPart("https://dl.example/rc10/", part, { fetchFn: async () => response(c), storage: fakeStorage() });
  assert.equal(r4.ok, false);
  assert.match(r4.why, /is not the pin/);
  // no https folder URL, or no pinned size: nothing fetched
  for (const [base, p] of [["http://dl.example/", part], ["https://dl.example/x", part], [null, part], ["https://dl.example/", { ...part, size: null }]]) {
    const x = await downloadPart(base, p, { fetchFn: async () => assert.fail("fetched"), storage: fakeStorage() });
    assert.equal(x.ok, false);
  }
  assert.equal(await storageFree(st), 4e9);
  assert.ok(st.root.kids.has(OPFS_DIR));
  await deleteDownloads(st);
  assert.ok(!st.root.kids.has(OPFS_DIR));
  await deleteDownloads(st); // nothing there: no error
});

// the base (DESIGN-R23 M2): a schema-3 payload folder
const TMPL = randomBytes(70000), ZST = randomBytes(130000);
const MAN = Buffer.from(JSON.stringify({ schema: 3, boot_template: { file: "boot-template.img", size: TMPL.length, sha256: sha(TMPL) },
  rootfs: { file: "rootfs.ext4", size: 999, sha256: "1".repeat(64) }, rootfs_zst: { file: "rootfs.ext4.zst", size: ZST.length, sha256: sha(ZST) } }));
const site = (files) => async (u) => {
  const n = u.slice(u.lastIndexOf("/") + 1);
  return files[n] ? response(files[n]) : response(Buffer.alloc(0), { status: 404 });
};

test("parseBase: schema 3 needs boot_template and rootfs_zst with names, sizes, sha256s", () => {
  assert.equal(parseBase(MAN).ok, true);
  assert.equal(parseBase(Buffer.from("{")).ok, false);
  const noZst = JSON.parse(MAN); delete noZst.rootfs_zst;
  assert.equal(parseBase(Buffer.from(JSON.stringify(noZst))).ok, false);
  const wrong = JSON.parse(MAN); wrong.rootfs_zst.file = "rootfs.ext4";
  assert.equal(parseBase(Buffer.from(JSON.stringify(wrong))).ok, false);
});

test("fetchBase: the manifest first, its pin before any other request; then each file against the manifest", async () => {
  const urls = [];
  const f = (files) => async (u, o) => { urls.push(u); return site(files)(u, o); };
  const r = await fetchBase("base/uc-pxx-mainline/", sha(MAN), () => {}, f({ "manifest.json": MAN, "boot-template.img": TMPL, "rootfs.ext4.zst": ZST }));
  assert.equal(r.ok, true);
  assert.equal(r.sha, sha(MAN));
  assert.deepEqual(urls, ["base/uc-pxx-mainline/manifest.json", "base/uc-pxx-mainline/boot-template.img", "base/uc-pxx-mainline/rootfs.ext4.zst"]);
  urls.length = 0;
  const r2 = await fetchBase("base/", "0".repeat(64), () => {}, f({ "manifest.json": MAN }));
  assert.equal(r2.ok, false);
  assert.equal(r2.wrong, true);
  assert.deepEqual(urls, ["base/manifest.json"]); // nothing more after a wrong manifest
  const r3 = await fetchBase("base/", "BASE-MANIFEST-SHA256-PLACEHOLDER", () => {}, f({ "manifest.json": MAN }));
  assert.match(r3.why, /pins no base/);
  const z = Buffer.from(ZST); z[3] ^= 1;
  const r4 = await fetchBase("base/", sha(MAN), () => {}, f({ "manifest.json": MAN, "boot-template.img": TMPL, "rootfs.ext4.zst": z }));
  assert.match(r4.why, /rootfs.ext4.zst has sha256/);
  const r5 = await fetchBase("base/", sha(MAN), () => {}, f({}));
  assert.equal(r5.missing, true);
});

test("folderBase: the folder uc-pxx-mainline of the picked files, the same checks", async () => {
  const fl = (p, b) => { const x = new File([b], p.slice(p.lastIndexOf("/") + 1)); Object.defineProperty(x, "webkitRelativePath", { value: p }); return x; };
  const good = [fl("d/uc-pxx-mainline/manifest.json", MAN), fl("d/uc-pxx-mainline/boot-template.img", TMPL), fl("d/uc-pxx-mainline/rootfs.ext4.zst", ZST)];
  const r = await folderBase(good, sha(MAN));
  assert.equal(r.ok, true);
  assert.equal(r.dir, "d/uc-pxx-mainline");
  assert.equal((await folderBase(good.slice(0, 2), sha(MAN))).ok, false);
  assert.equal((await folderBase(good, "0".repeat(64))).ok, false);
  const z = Buffer.from(ZST); z[0] ^= 1;
  assert.equal((await folderBase([good[0], good[1], fl("d/uc-pxx-mainline/rootfs.ext4.zst", z)], sha(MAN))).ok, false);
  assert.equal((await folderBase([fl("d/other/manifest.json", MAN), good[1], good[2]], sha(MAN))).ok, false);
});

// Compressed downloads (1.3.1): GitHub Pages serves the site's files with
// Content-Encoding: gzip. Content-Length is then the size of the gzip bytes,
// and fetch gives the decoded bytes. A model of that: the header says
// content-length = the gzip size, the body is the full bytes.
function encoded(body, { enc = "gzip", wire = gzipSync(body).length, cutAt = -1 } = {}) {
  const r = response(body, { cutAt });
  r.headers = { get: (k) => (k === "content-length" ? String(wire) : k === "content-encoding" ? enc : null) };
  return r;
}
// compressible bytes (the gzip size is far below the body size, as on the live site) and random bytes (above it)
const PLAIN = Buffer.alloc(300000).map((_, i) => (i % 251) & 0x0f);
const NOISE = randomBytes(200000);
const grow = (b) => Buffer.concat([b, Buffer.from("x")]);

test("bodyLength: Content-Length only without a Content-Encoding (or identity)", () => {
  const h = (o) => ({ headers: { get: (k) => (k in o ? o[k] : null) } });
  assert.equal(bodyLength(h({ "content-length": "5" })), 5);
  assert.equal(bodyLength(h({ "content-length": "5", "content-encoding": "identity" })), 5);
  assert.equal(bodyLength(h({ "content-length": "5", "content-encoding": " Identity " })), 5);
  for (const e of ["gzip", "br", "deflate", "zstd", "GZIP", "gzip, identity"]) assert.equal(bodyLength(h({ "content-length": "5", "content-encoding": e })), 0, e);
  assert.equal(bodyLength(h({})), 0);
});

test("fetchBytes, gzip: the full body is taken (Content-Length is the gzip size); the bar uses the pinned size and never passes 100%", async () => {
  for (const b of [PLAIN, NOISE]) {
    const wire = gzipSync(b).length;
    assert.notEqual(wire, b.length);
    for (const enc of ["gzip", "br", "GZIP"]) {
      const seen = [];
      const r = await fetchBytes("images/x.img", (d, t) => seen.push([d, t]), async () => encoded(b, { enc }), b.length);
      assert.equal(r.ok, true, r.why);
      assert.equal(sha(r.bytes), sha(b));
      assert.ok(seen.every(([d, t]) => t === b.length && d <= t), "the total is the pinned size");
      assert.deepEqual(seen.at(-1), [b.length, b.length]);
    }
    // no pinned size (the finish set): the bar's total is never below done
    const seen = [];
    const r = await fetchBytes("finish/finish.json", (d, t) => seen.push([d, t]), async () => encoded(b));
    assert.equal(r.ok, true, r.why);
    assert.ok(seen.every(([d, t]) => d <= t));
    // a body longer than the pinned size: the bar stays at 100% (the callers refuse the size)
    const seen2 = [];
    const r2 = await fetchBytes("images/x.img", (d, t) => seen2.push([d, t]), async () => encoded(grow(b)), b.length);
    assert.equal(r2.bytes.length, b.length + 1);
    assert.ok(seen2.every(([d, t]) => d <= t));
    // a cut transfer is still refused
    const cut = await fetchBytes("x", () => {}, async () => encoded(b, { cutAt: 65536 }), b.length);
    assert.equal(cut.ok, false);
    assert.match(cut.why, /the download stopped at 65536 bytes/);
  }
});

test("fetchBytes, no encoding or identity: a body that is not the Content-Length is still refused", async () => {
  for (const enc of [null, "identity"]) {
    for (const wire of [PLAIN.length - 1, PLAIN.length + 1]) {
      const r = await fetchBytes("x", () => {}, async () => encoded(PLAIN, { enc, wire }), PLAIN.length);
      assert.equal(r.ok, false);
      assert.equal(r.why, `${PLAIN.length} of ${wire} bytes`);
    }
  }
});

test("the image, gzip: the fetched bytes pass the pin check (checkImage); a short, long or changed body is refused", async () => {
  const img = Buffer.from(PLAIN); img.write("ANDROID!", 0, "latin1");
  const pins = [{ model: "UC-P10", goals: ["userdebug"], file: "x.img", size: img.length, sha256: sha(img), label: "x" }];
  const get = async (b) => (await fetchBytes("images/x.img", () => {}, async () => encoded(b), img.length)).bytes;
  assert.equal((await checkImage(await get(img), pins, "UC-P10")).sha256, sha(img));
  const changed = Buffer.from(img); changed[1000] ^= 1;
  for (const b of [img.subarray(0, img.length - 1), grow(img), changed]) {
    await assert.rejects(checkImage(await get(b), pins, "UC-P10"), (e) => e.code === "image-sha");
  }
});

test("fetchBase, gzip: manifest, template and rootfs; the pin and the manifest's sizes and sha256s still hold", async () => {
  const gz = (files) => async (u) => {
    const n = u.slice(u.lastIndexOf("/") + 1);
    return files[n] ? encoded(files[n]) : response(Buffer.alloc(0), { status: 404 });
  };
  const all = { "manifest.json": MAN, "boot-template.img": TMPL, "rootfs.ext4.zst": ZST };
  const seen = [];
  const r = await fetchBase("base/uc-pxx-mainline/", sha(MAN), (d, t) => seen.push([d, t]), gz(all));
  assert.equal(r.ok, true, r.why);
  assert.equal(sha(Buffer.from(await r.files.rootfsZst.arrayBuffer())), sha(ZST));
  assert.ok(seen.every(([d, t]) => t === TMPL.length + ZST.length && d <= t));
  // a manifest that is not the pin; a short, long or changed file: refused
  const m = Buffer.from(MAN); m[m.length - 2] = 0x20;
  assert.match((await fetchBase("base/", sha(MAN), () => {}, gz({ ...all, "manifest.json": m }))).why, /is not the pin/);
  assert.match((await fetchBase("base/", sha(MAN), () => {}, gz({ ...all, "manifest.json": grow(MAN) }))).why, /is not the pin/);
  for (const [k, v] of [["boot-template.img", TMPL], ["rootfs.ext4.zst", ZST]]) {
    for (const b of [v.subarray(1), grow(v)]) {
      const seen2 = [];
      const x = await fetchBase("base/", sha(MAN), (d, t) => seen2.push([d, t]), gz({ ...all, [k]: b }));
      assert.equal(x.ok, false);
      assert.equal(x.wrong, true);
      assert.match(x.why, new RegExp(`${k.replace(/\./g, "\\.")} has ${b.length} bytes, the manifest says ${v.length}`));
      assert.ok(seen2.every(([d, t]) => d <= t), "the bar never passes 100%");
    }
    const c = Buffer.from(v); c[5] ^= 1;
    assert.match((await fetchBase("base/", sha(MAN), () => {}, gz({ ...all, [k]: c }))).why, /has sha256/);
  }
});

test("the finish set, gzip: fetched as fLoadSet does, then finishusb.checkSet; a short, long or changed file is refused", async () => {
  const b = Buffer.alloc(70000).map((_, i) => (i * 7) & 0x1f), c = Buffer.alloc(5000).map((_, i) => (i * 11) & 0x0f);
  const list = Buffer.from(JSON.stringify({ kind: "uc-pxx-finish", version: 1, files: [
    { name: "uc-pxx-step2-finish", size: b.length, sha256: sha(b) }, { name: "uc-pxx-bootctl-inst", size: c.length, sha256: sha(c) }] }));
  const lists = [{ label: "t", sha256: sha(list) }];
  const load = async (served) => {
    const files = {};
    for (const n of [LIST, ...FILES]) {
      const r = await fetchBytes(`finish/${n}`, () => {}, async () => encoded(served[n]));
      if (!r.ok) return r;
      files[n] = new Blob([r.bytes]);
    }
    return checkSet({ dir: "finish/", files, other: [] }, lists);
  };
  const good = { [LIST]: list, "uc-pxx-step2-finish": b, "uc-pxx-bootctl-inst": c };
  const r = await load(good);
  assert.equal(r.ok, true, r.why);
  assert.equal(r.sha, sha(list));
  for (const [k, v] of Object.entries(good)) {
    const ch = Buffer.from(v); ch[3] ^= 1;
    for (const x of [v.subarray(1), grow(v), ch]) assert.equal((await load({ ...good, [k]: x })).ok, false, k);
  }
});

test("downloadPart, gzip: the gzip size is not compared with the pin; the pin's size and sha256 still hold", async () => {
  for (const b of [PLAIN, NOISE]) {
    const part = { file: "p1.zst", size: b.length, sha256: sha(b) };
    const seen = [];
    const r = await downloadPart("https://dl.example/rc10/", part, { fetchFn: async () => encoded(b), storage: fakeStorage(), onProgress: (d, t) => seen.push([d, t]) });
    assert.equal(r.ok, true, r.why);
    assert.equal(sha(Buffer.from(await r.file.arrayBuffer())), sha(b));
    assert.ok(seen.every(([d, t]) => t === b.length && d <= t));
    const long = await downloadPart("https://dl.example/rc10/", part, { fetchFn: async () => encoded(grow(b)), storage: fakeStorage() });
    assert.equal(long.ok, false);
    assert.match(long.why, new RegExp(`more than ${b.length} bytes`));
    const short = await downloadPart("https://dl.example/rc10/", part, { fetchFn: async () => encoded(b.subarray(1)), storage: fakeStorage() });
    assert.equal(short.ok, false);
    assert.match(short.why, /bytes, the pin says/);
    const ch = Buffer.from(b); ch[9] ^= 1;
    const changed = await downloadPart("https://dl.example/rc10/", part, { fetchFn: async () => encoded(ch), storage: fakeStorage() });
    assert.match(changed.why, /is not the pin/);
    // identity: the Content-Length is compared before the body is read, as before
    const id = await downloadPart("https://dl.example/rc10/", part, { fetchFn: async () => encoded(b, { enc: "identity", wire: b.length + 1 }), storage: fakeStorage() });
    assert.match(id.why, /offers/);
  }
});
