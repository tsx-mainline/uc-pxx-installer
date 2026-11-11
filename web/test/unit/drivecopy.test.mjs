// SPDX-License-Identifier: GPL-2.0-or-later
// Unit tests for drivecopy.js: the File System Access drive-copy core. It is
// host-side file I/O only; these tests use a fake FileSystemDirectoryHandle and
// Node's WebCrypto, with no browser and no device.
import { test } from "node:test";
import assert from "node:assert/strict";
import { fatName, sha256OfBlob, supported, writeAndVerify } from "../../site/js/drivecopy.js";

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
