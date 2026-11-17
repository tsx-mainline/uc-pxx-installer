// SPDX-License-Identifier: GPL-2.0-or-later
// The streaming SHA-256 (W5): the FIPS 180-4 / NIST vectors, every split of a
// message, random data against WebCrypto, and the file reader.
import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash, randomBytes } from "node:crypto";
import { Sha256, sha256Bytes, sha256OfFile } from "../../site/js/sha256stream.js";

const enc = (s) => new TextEncoder().encode(s);
const ref = (b) => createHash("sha256").update(b).digest("hex");

test("the NIST vectors (FIPS 180-4 examples and the long message)", () => {
  assert.equal(sha256Bytes(enc("")), "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855");
  assert.equal(sha256Bytes(enc("abc")), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
  assert.equal(sha256Bytes(enc("abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq")),
    "248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1");
  assert.equal(sha256Bytes(enc("abcdefghbcdefghicdefghijdefghijkefghijklfghijklmghijklmnhijklmnoijklmnopjklmnopqklmnopqrlmnopqrsmnopqrstnopqrstu")),
    "cf5b16a778af8380036ce59e7b0492370b249b11e8f07a51afac45037afee9d1");
  const h = new Sha256();
  const a = new Uint8Array(1000).fill(0x61);
  for (let i = 0; i < 1000; i++) h.update(a);
  assert.equal(h.hex(), "cdc76e5c9914fb9281a1c7e284d73e67f1809a48a497200e046d39ccc7112cd0"); // one million "a"
});

test("every split of the padding edges gives the same digest", () => {
  for (const n of [0, 1, 55, 56, 57, 63, 64, 65, 119, 120, 127, 128, 129, 1000]) {
    const m = randomBytes(n);
    const want = ref(m);
    for (let cut = 0; cut <= n; cut += Math.max(1, Math.floor(n / 13))) {
      assert.equal(new Sha256().update(m.subarray(0, cut)).update(m.subarray(cut)).hex(), want, `n ${n} cut ${cut}`);
    }
    const h = new Sha256();
    for (const b of m) h.update(Uint8Array.of(b));
    assert.equal(h.hex(), want, `n ${n} byte by byte`);
  }
});

test("random data in random pieces equals WebCrypto", async () => {
  for (let k = 0; k < 20; k++) {
    const m = randomBytes(1 + Math.floor(Math.random() * 300000));
    const h = new Sha256();
    for (let o = 0; o < m.length;) {
      const n = Math.min(m.length - o, 1 + Math.floor(Math.random() * 70000));
      h.update(m.subarray(o, o + n));
      o += n;
    }
    const w = Buffer.from(await crypto.subtle.digest("SHA-256", m)).toString("hex");
    assert.equal(h.hex(), w);
  }
});

test("misuse is an error: update after the digest, a digest twice, not bytes", () => {
  const h = new Sha256().update(enc("x"));
  h.hex();
  assert.throws(() => h.update(enc("y")), /after digest/);
  assert.throws(() => h.hex(), /twice/);
  assert.throws(() => new Sha256().update("text"), TypeError);
});

test("sha256OfFile reads in slices with progress and returns the size", async () => {
  const m = randomBytes(5 * 1048576 + 123);
  const seen = [];
  const r = await sha256OfFile(new Blob([m]), (d, t) => seen.push([d, t]), 1 << 20);
  assert.equal(r.sha256, ref(m));
  assert.equal(r.size, m.length);
  assert.deepEqual(seen[0], [0, m.length]);
  assert.deepEqual(seen.at(-1), [m.length, m.length]);
  assert.equal(seen.length, 7); // 0, then 6 slices
  assert.ok(seen.every(([d], i) => i === 0 || d > seen[i - 1][0]));
  // a blob that ends early is an error
  const short = { size: 10, slice: () => new Blob([]) };
  await assert.rejects(sha256OfFile(short), /ended at 0 of 10/);
});
