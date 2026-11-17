// SPDX-License-Identifier: GPL-2.0-or-later
// Unit tests for drivecopy.js: the File System Access drive-copy core. It is
// host-side file I/O only; these tests use a fake FileSystemDirectoryHandle and
// Node's WebCrypto, with no browser and no device.
import { test } from "node:test";
import assert from "node:assert/strict";
import { copyChecked, emptyRoot, fatName, hostEntry, ignoredInFolder, makeFolder, MAINLINE_DIR, rootNames, sha256OfBlob, supported, writeAndVerify } from "../../site/js/drivecopy.js";
import { sha256OfFile } from "../../site/js/sha256stream.js";
import { createHash } from "node:crypto";

// A minimal Blob-like source with .size and .stream() (Node has Blob).
function src(bytes) {
  return new Blob([bytes]);
}

// A fake writable + directory handle that records the bytes written and can
// hand them back as a File (Blob). It mimics the pieces writeAndVerify uses.
function fakeDir() {
  const files = new Map();
  return {
    name: "UC-PXX-INST",
    files,
    async getFileHandle(name, opts) {
      if (!files.has(name)) {
        if (!opts || !opts.create) throw new Error("not found");
        files.set(name, null);
      }
      return {
        async createWritable() {
          const chunks = [];
          return {
            async write(v) { chunks.push(Buffer.from(v)); },
            async close() { files.set(name, Buffer.concat(chunks)); },
            async abort() { files.set(name, null); },
          };
        },
        async getFile() {
          const b = files.get(name);
          if (b === null) throw new Error("not written");
          return new Blob([b]);
        },
      };
    },
  };
}

test("supported() reflects showDirectoryPicker", () => {
  assert.equal(supported({}), false);
  assert.equal(supported({ showDirectoryPicker: () => {} }), true);
});

test("fatName accepts a plain .bin; rejects paths, AppleDouble, non-.bin, empty", () => {
  assert.equal(fatName("pcdfw-1.0.0.94-userdebug.bin"), "pcdfw-1.0.0.94-userdebug.bin");
  assert.throws(() => fatName(""), /empty/);
  assert.throws(() => fatName("a/b.bin"), /path separator/);
  assert.throws(() => fatName("sub\\b.bin"), /path separator/);
  assert.throws(() => fatName("._fw.bin"), /AppleDouble/);
  assert.throws(() => fatName("fw.img"), /\.bin/);
});

test("sha256OfBlob matches a known digest", async () => {
  const sha = await sha256OfBlob(src(Buffer.from("abc")));
  assert.equal(sha, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
});

test("writeAndVerify writes, reads back and verifies against the expected sha", async () => {
  const data = Buffer.from("firmware-bytes-0123456789");
  const expectedSha = await sha256OfBlob(src(data));
  const dir = fakeDir();
  const seen = [];
  const r = await writeAndVerify(dir, src(data), "fw.bin",
    { label: "test fw", size: data.length, sha256: expectedSha }, (n, t) => seen.push([n, t]));
  assert.equal(r.ok, true);
  assert.equal(r.sha256, expectedSha);
  assert.equal(r.size, data.length);
  assert.deepEqual(Buffer.from(dir.files.get("fw.bin")), data);
  assert.ok(seen.length >= 1);
  assert.equal(seen.at(-1)[0], data.length);
});

test("writeAndVerify flags a sha mismatch when the size matches but bytes differ", async () => {
  const data = Buffer.from("aaaa");
  const dir = fakeDir();
  const r = await writeAndVerify(dir, src(data), "fw.bin",
    { label: "x", size: data.length, sha256: "0".repeat(64) });
  assert.equal(r.ok, false);
  assert.match(r.reason, /did not verify/);
});

test("writeAndVerify returns the sha for the user to check when the size does not match the pin", async () => {
  const data = Buffer.from("zzzz");
  const dir = fakeDir();
  const r = await writeAndVerify(dir, src(data), "fw.bin", { label: "x", size: 999999, sha256: "0".repeat(64) });
  assert.equal(r.ok, true);
  assert.match(r.reason, /Check it against the firmware/);
});

// --- the drive fallback of RC-10 (DESIGN §6) ---
// tree(): a fake drive with folders, entries() and a read-back fault
function tree(seed = [], corrupt = "") {
  const mk = () => {
    const kids = new Map();
    return {
      kind: "directory", kids,
      async *entries() { for (const [k, v] of kids) yield [k, v.handle]; },
      async getDirectoryHandle(n, o) {
        if (!kids.has(n)) {
          if (!(o && o.create)) throw new DOMException("x", "NotFoundError");
          const d = mk();
          kids.set(n, { dir: d, handle: d });
        }
        return kids.get(n).dir;
      },
      async getFileHandle(n, o) {
        if (!kids.has(n)) {
          if (!(o && o.create)) throw new DOMException("x", "NotFoundError");
          kids.set(n, { bytes: new Uint8Array(0), handle: {} });
        }
        const f = kids.get(n);
        return {
          async createWritable() {
            const ch = [];
            return { async write(v) { ch.push(Buffer.from(v)); }, async close() { f.bytes = Buffer.concat(ch); }, async abort() { f.aborted = true; } };
          },
          async getFile() { return new Blob([n === corrupt ? Buffer.concat([f.bytes, Buffer.from("x")]) : f.bytes]); },
        };
      },
    };
  };
  const root = mk();
  for (const n of seed) root.kids.set(n, { bytes: new Uint8Array(1), handle: {} });
  return root;
}
const sha = (b) => createHash("sha256").update(b).digest("hex");

test("emptyRoot: only the host's own entries may be there (Windows, macOS; review MINOR-F)", async () => {
  assert.equal((await emptyRoot(tree())).ok, true);
  assert.equal((await emptyRoot(tree(["System Volume Information"]))).ok, true);
  assert.equal((await emptyRoot(tree([".fseventsd", ".Spotlight-V100", ".Trashes", ".DS_Store", "._pcdfw-1.0.0.94-userdebug.bin", "._uc-pxx-mainline"]))).ok, true);
  const r = await emptyRoot(tree(["System Volume Information", "pcdfw-1.0.0.94-userdebug.bin"]));
  assert.equal(r.ok, false);
  assert.deepEqual(r.other, ["pcdfw-1.0.0.94-userdebug.bin"]);
  // real content is not a host entry: other names, a bare "._", look-alikes
  for (const n of ["notes.txt", "._", ".fseventsd2", "fseventsd", ".trashes", ".Trash", "SSH-KEY.PUB", "uc-pxx-mainline", ".hidden"]) {
    assert.equal(hostEntry(n), false, n);
    assert.equal((await emptyRoot(tree([n]))).ok, false, n);
  }
  // inside the folder: only what the installer drops (internal/mainline ignored)
  for (const n of ["._manifest.json", ".DS_Store"]) assert.equal(ignoredInFolder(n), true, n);
  for (const n of [".fseventsd", "._", "manifest.json", "x"]) assert.equal(ignoredInFolder(n), false, n);
});

test("copyChecked: the copy is read back and hashed against the pin; a wrong size writes nothing", async () => {
  const b = Buffer.alloc(300000, 7);
  const pin = { size: b.length, sha256: sha(b) };
  const d = tree();
  const seen = { copy: 0, check: 0 };
  const r = await copyChecked(d, "pcdfw-1.0.0.94-userdebug.bin", new Blob([b]), pin, (n) => { seen.copy = n; }, (n) => { seen.check = n; }, sha256OfFile);
  assert.equal(r.ok, true);
  assert.deepEqual(seen, { copy: b.length, check: b.length });
  assert.deepEqual(await rootNames(d), ["pcdfw-1.0.0.94-userdebug.bin"]);
  const d2 = tree();
  const r2 = await copyChecked(d2, "x.bin", new Blob([b.subarray(1)]), pin, () => {}, () => {}, sha256OfFile);
  assert.equal(r2.ok, false);
  assert.deepEqual(await rootNames(d2), []);
  const d3 = tree([], "x.bin");
  const r3 = await copyChecked(d3, "x.bin", new Blob([b]), pin, () => {}, () => {}, sha256OfFile);
  assert.equal(r3.ok, false);
  assert.match(r3.reason, /the copy has/);
  for (const n of ["", "a/b.bin", "._x.bin", "a\\b"]) await assert.rejects(copyChecked(tree(), n, new Blob([b]), pin, () => {}, () => {}, sha256OfFile));
});

test("makeFolder: only uc-pxx-mainline, listed after it is made", async () => {
  const d = tree();
  const m = await makeFolder(d, MAINLINE_DIR);
  assert.deepEqual(m.names, []);
  assert.deepEqual(await rootNames(d), ["uc-pxx-mainline"]);
  await assert.rejects(makeFolder(d, "other"), /only the folder uc-pxx-mainline/);
});
