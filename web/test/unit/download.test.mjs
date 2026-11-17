// SPDX-License-Identifier: GPL-2.0-or-later
// download.js (W4): the chosen-file check against a pin, the same-origin
// download with progress, the part download into a fake private storage (off
// in this page version: PARTS.PAYLOAD_URL null), and the delete.
import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash, randomBytes } from "node:crypto";
import { checkFile, deleteDownloads, downloadPart, fetchBase, fetchBytes, folderBase, OPFS_DIR, parseBase, storageFree } from "../../site/js/download.js";

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
