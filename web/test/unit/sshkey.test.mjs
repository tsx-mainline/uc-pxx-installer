// SPDX-License-Identifier: GPL-2.0-or-later
// Unit tests for sshkey.js (the SSH-KEY.PUB rule) and for the key file I/O of
// drivecopy.js. The vectors (sshkey-vectors.json, made by sshkey-vectors.py
// with throwaway keys; fingerprints by ssh-keygen -l) are shared with the
// installer's Go tests. Node built-ins only; no browser, no device.
import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash, randomBytes } from "node:crypto";
import { readFileSync } from "node:fs";
import {
  b64decodeCanonical, b64encode, checkKey, FILE_NAME, fingerprint, MAX_BYTES, RSA_MAX_BITS, RSA_MIN_BITS, TYPES, validate,
} from "../../site/js/sshkey.js";
import { KEY_FILE, readKeyFile, writeKeyFile } from "../../site/js/drivecopy.js";

const DOC = JSON.parse(readFileSync(new URL("./sshkey-vectors.json", import.meta.url), "utf8"));
const V = DOC.vectors;
const byName = Object.fromEntries(V.map((v) => [v.name, v]));

test("the vector file: unique names, the rule constants of sshkey.js, ssh-keygen fingerprints on every valid key", () => {
  assert.equal(new Set(V.map((v) => v.name)).size, V.length);
  assert.deepEqual(DOC.rule, {
    max_bytes: MAX_BYTES, types: [...TYPES], rsa_min_bits: RSA_MIN_BITS, rsa_max_bits: RSA_MAX_BITS,
    comment: "^[A-Za-z0-9._@+=:-]{1,64}$", file_name: FILE_NAME,
  });
  assert.equal(FILE_NAME, "SSH-KEY.PUB");
  for (const v of V) {
    assert.equal(typeof v.input, "string", v.name);
    assert.equal(typeof v.valid, "boolean", v.name);
    if (!v.valid) continue;
    assert.match(v.fingerprint, /^SHA256:[A-Za-z0-9+/]{43}$/, v.name);
    // ssh-keygen -l printed exactly this size and fingerprint for the input
    assert.equal(v.ssh_keygen_l.length, 1, v.name);
    assert.deepEqual(v.ssh_keygen_l[0].split(" ").slice(0, 2), [String(v.bits), v.fingerprint], v.name);
    assert.equal(v.file, `${v.line}\n`, v.name);
  }
  // the cases the rule must cover are all in the file
  for (const n of ["ed25519", "ed25519-lf", "ed25519-crlf", "rsa-3072", "rsa-4096", "rsa-16384", "rsa-2048", "rsa-3071",
    "rsa-16385", "options-restrict", "options-command", "two-lines", "two-lf", "cr-only", "b64-bad-char",
    "b64-noncanonical", "b64-no-padding", "b64-urlsafe", "ed25519-trailing-garbage", "rsa-trailing-garbage", "ecdsa-p256",
    "ssh-dss", "sk-ed25519", "sk-ecdsa", "ed25519-cert", "comment-quote", "comment-space", "comment-backslash",
    "comment-65", "oversize", "rsa-e-even", "rsa-e-1", "rsa-n-leading-zero", "rsa-n-negative", "rsa-e-leading-zero",
    "type-mismatch-rsa-ed", "private-key", "empty"]) {
    assert.ok(byName[n], `vector ${n}`);
  }
  assert.ok(V.filter((v) => v.valid).length >= 8 && V.filter((v) => !v.valid).length >= 60);
  assert.ok(byName.oversize.input.length > MAX_BYTES && byName["oversize-lf"].input.length === MAX_BYTES + 1);
});

test("every vector: accepted or refused as the file says; type, bits, fingerprint, line and file of each valid key", async () => {
  for (const v of V) {
    const r = await validate(v.input);
    assert.equal(r.ok, v.valid, `${v.name}: ${r.reason || "accepted"}`);
    if (v.valid) {
      assert.deepEqual({ type: r.type, bits: r.bits, fingerprint: r.fingerprint, line: r.line, file: r.file },
        { type: v.type, bits: v.bits, fingerprint: v.fingerprint, line: v.line, file: v.file }, v.name);
    } else {
      assert.equal(typeof r.reason, "string", v.name);
      assert.ok(r.reason.length > 10 && !/undefined|null|NaN|\[object/.test(r.reason), `${v.name}: ${r.reason}`);
      assert.equal(r.fingerprint, undefined, v.name);
    }
  }
});

test("plain reasons for the common mistakes", () => {
  const why = (n) => checkKey(byName[n].input).reason;
  assert.match(why("rsa-2048"), /2048 bits; at least 3072/);
  assert.match(why("options-restrict"), /starts with options/);
  assert.match(why("two-lines"), /one line/);
  assert.match(why("ecdsa-p256"), /"ecdsa-sha2-nistp256" is not accepted/);
  assert.match(why("comment-quote"), /comment may have 1 to 64 characters/);
  assert.match(why("comment-space"), /no spaces/);
  assert.match(why("oversize"), /8193 bytes long; at most 8192/);
  assert.match(why("b64-noncanonical"), /not valid base64/);
  assert.match(why("ed25519-trailing-garbage"), /extra bytes/);
  assert.match(why("private-key"), /PRIVATE key/);
  assert.match(why("type-mismatch-rsa-ed"), /says ssh-rsa, but the key data is an "ssh-ed25519" key/);
  assert.deepEqual(checkKey(undefined), { ok: false, reason: "The key is empty." });
});

test("the size limit counts bytes, with the line end", () => {
  const ed = byName.ed25519.line;
  const pad = (n) => `${ed.split(" ").slice(0, 2).join(" ")} ${"c".repeat(n)}`;
  // a valid key never reaches the limit (the comment is at most 64), so the
  // limit is seen on a refused line: the size reason comes first.
  const long = `${pad(64)}${"x".repeat(MAX_BYTES - pad(64).length + 1)}`;
  assert.match(checkKey(long).reason, /at most 8192 bytes/);
  assert.doesNotMatch(checkKey(long.slice(0, MAX_BYTES)).reason, /bytes long/);
  // non-ASCII is counted in UTF-8 bytes (and refused anyway)
  assert.match(checkKey("é".repeat(MAX_BYTES / 2 + 1)).reason, /8194 bytes long/);
});

test("b64encode / b64decodeCanonical: round trip; only the canonical padded form decodes", () => {
  for (let n = 0; n < 70; n++) {
    const b = randomBytes(n);
    const s = b64encode(b);
    assert.equal(s, b.toString("base64"));
    if (n === 0) assert.equal(b64decodeCanonical(s), null);
    else assert.deepEqual(Buffer.from(b64decodeCanonical(s)), b);
  }
  assert.equal(b64encode(Buffer.from("ab"), false), "YWI");
  for (const bad of ["YWI", "YWI==", "YWJ=", "YW=I", "YWI=\n", " YWI=", "YW-_", "YWI=YWI=", "====", "Y===", null, 7]) {
    assert.equal(b64decodeCanonical(bad), null, String(bad));
  }
  assert.deepEqual(Buffer.from(b64decodeCanonical("YWI=")), Buffer.from("ab"));
});

test("fingerprint = SHA256: + base64 without padding of sha256(blob) (ssh-keygen -l form)", async () => {
  const blob = randomBytes(51);
  const want = `SHA256:${createHash("sha256").update(blob).digest("base64").replace(/=+$/, "")}`;
  assert.equal(await fingerprint(blob), want);
  const ed = await validate(byName.ed25519.input);
  assert.equal(ed.fingerprint, `SHA256:${createHash("sha256").update(ed.blob).digest("base64").replace(/=+$/, "")}`);
});

// A fake FileSystemDirectoryHandle with one file per name; `corrupt` changes
// what a read returns; it records every call so a test can see what was done.
function fakeDir(seed = {}, { corrupt = false } = {}) {
  const files = new Map(Object.entries(seed).map(([k, v]) => [k, Buffer.from(v)]));
  const calls = [];
  const nf = () => Object.assign(new Error("not found"), { name: "NotFoundError" });
  return {
    name: "UC-PXX-INST", files, calls,
    async removeEntry(n) { calls.push(["removeEntry", n]); files.delete(n); },
    async getFileHandle(name, opts) {
      calls.push(["getFileHandle", name, !!(opts && opts.create)]);
      if (!files.has(name)) {
        if (!opts || !opts.create) throw nf();
        files.set(name, Buffer.alloc(0));
      }
      return {
        async createWritable() {
          calls.push(["createWritable", name]);
          const chunks = [];
          return {
            async write(v) { chunks.push(Buffer.from(v)); },
            async close() { files.set(name, Buffer.concat(chunks)); },
            async abort() {},
          };
        },
        async getFile() {
          const b = files.get(name);
          return new Blob([corrupt ? Buffer.concat([b, Buffer.from("x")]) : b]);
        },
      };
    },
  };
}

test("writeKeyFile writes SSH-KEY.PUB = line + LF, reads it back, touches no other file, deletes nothing", async () => {
  const k = await validate(byName["ed25519-crlf"].input);
  const dir = fakeDir({ "fw.bin": "firmware", "SSH-KEY.PUB": "an older key file\n" });
  const r = await writeKeyFile(dir, k.file);
  assert.equal(r.ok, true, r.reason);
  assert.equal(KEY_FILE, "SSH-KEY.PUB");
  assert.equal(dir.files.get("SSH-KEY.PUB").toString("latin1"), `${byName.ed25519.line}\n`);
  assert.equal(dir.files.get("fw.bin").toString(), "firmware");
  assert.deepEqual([...dir.files.keys()].sort(), ["SSH-KEY.PUB", "fw.bin"]);
  assert.deepEqual(dir.calls, [["getFileHandle", "SSH-KEY.PUB", true], ["createWritable", "SSH-KEY.PUB"]]);
});

test("writeKeyFile: a read-back that differs is a failure; content that is not one line + LF is refused before any write", async () => {
  const k = await validate(byName["rsa-3072"].input);
  const bad = fakeDir({}, { corrupt: true });
  const r = await writeKeyFile(bad, k.file);
  assert.equal(r.ok, false);
  assert.match(r.reason, /did not read back as written/);
  for (const c of [k.line, `${k.line}\r\n`, `${k.line}\n\n`, `a\nb\n`, "", null]) {
    const dir = fakeDir();
    await assert.rejects(writeKeyFile(dir, c), /one line with one LF/);
    assert.deepEqual(dir.calls, []);
  }
});

test("readKeyFile: absent, present (text), too large (no read); it never creates or writes", async () => {
  const none = fakeDir({ "fw.bin": "x" });
  assert.deepEqual(await readKeyFile(none), { present: false });
  const one = fakeDir({ "SSH-KEY.PUB": byName.ed25519.file });
  assert.deepEqual(await readKeyFile(one), { present: true, size: byName.ed25519.file.length, text: byName.ed25519.file });
  const big = fakeDir({ "SSH-KEY.PUB": Buffer.alloc(70000, 0x41) });
  assert.deepEqual(await readKeyFile(big), { present: true, size: 70000, text: null });
  for (const d of [none, one, big]) {
    assert.ok(d.calls.every((c) => c[0] === "getFileHandle" && c[2] === false), JSON.stringify(d.calls));
  }
  const err = { async getFileHandle() { throw Object.assign(new Error("denied"), { name: "NotAllowedError" }); } };
  await assert.rejects(readKeyFile(err), /denied/);
});
