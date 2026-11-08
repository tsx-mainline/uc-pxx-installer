// SPDX-License-Identifier: GPL-2.0-or-later
// Unit tests: the A/B decoder, the keeper/never-tried rule, the BCB payload and
// states, the fastboot guard.
import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { checkBeforeEntry, crc32, otherSlotKind, parseAb, validateAb } from "../../site/js/avbab.js";
import { BCB_BYTES, bcbPayload, checkedPayload, classifyBcb, commandText, KNOWN_BCB, PAYLOAD_SHA256 } from "../../site/js/bcb.js";
import { commandAllowed, GETVARS, parseSize } from "../../site/js/fastboot.js";

const hexb = (s) => Uint8Array.from(s.match(/../g), (x) => parseInt(x, 16));
const sector = (first32hex) => {
  const s = new Uint8Array(512);
  s.set(hexb(first32hex.padEnd(64, "0")));
  return s;
};
const sha = (b) => createHash("sha256").update(b).digest("hex");

// Real sectors of unit 2 (DEVICE-MODE-REVIEW.md, table "S0 now" .. "D3").
const S0 = "00414230010000000f0001000e000100000000000000000000000000da933679";
const D1 = "00414230010000000f00010000000000000000000000000000000000671e21a4";
const D2 = "00414230010000000e0001000f070000000000000000000000000000179272c2";
const D3 = "00414230010000000e0001000f000100000000000000000000000000119c9cae";
// Unit 3 factory-stock first 16 bytes: A {15,0,1} successful, B {14,7,0} never tried.
const U3 = "00414230010000000f0001000e070000";

test("A/B: the recorded unit-2 sectors have the recorded shas", () => {
  assert.equal(sha(hexb(S0)), "1d2e3769805d32c2065ac3ff7e204ebf60921c0d2d3f66f0d47612d0889d7d2d");
  assert.equal(sha(hexb(D2)), "7757e94c1ac016e51f523d286c6df4ee86a0795fa758d2b662c7a84523fc6883");
  assert.equal(crc32(hexb(S0).subarray(0, 28)), 0xda933679);
});

test("A/B: S0 (A successful, higher) goes on, next a", () => {
  const r = checkBeforeEntry(sector(S0));
  assert.equal(r.ok, true);
  assert.equal(r.next, "a");
});

test("A/B: D1 (B disabled) goes on, next a", () => {
  assert.deepEqual([checkBeforeEntry(sector(D1)).ok, checkBeforeEntry(sector(D1)).next], [true, "a"]);
});

test("A/B: D3 (B successful, higher) goes on, next b", () => {
  const r = checkBeforeEntry(sector(D3));
  assert.equal(r.ok, true);
  assert.equal(r.next, "b");
});

test("A/B: D2 (B trying boots next) is refused", () => {
  const r = checkBeforeEntry(sector(D2));
  assert.equal(r.ok, false);
  assert.match(r.reason, /boots next \(b\) is not marked successful/);
});

function withSlots(a, b, mut) {
  const s = new Uint8Array(512);
  s.set([0, 0x41, 0x42, 0x30, 1, 0, 0, 0, ...a, 0, ...b, 0]);
  if (mut) mut(s);
  new DataView(s.buffer).setUint32(28, crc32(s.subarray(0, 28)), false);
  return s;
}

test("A/B: refusals", () => {
  const cases = [
    ["bad magic", withSlots([15, 0, 1], [14, 0, 1], (s) => (s[1] = 0x58)), /bad magic/],
    ["version", withSlots([15, 0, 1], [14, 0, 1], (s) => (s[4] = 2)), /version_major/],
    ["reserved 6", withSlots([15, 0, 1], [14, 0, 1], (s) => (s[6] = 1)), /reserved bytes 6/],
    ["reserved 20", withSlots([15, 0, 1], [14, 0, 1], (s) => (s[20] = 1)), /reserved bytes 16/],
    ["slot reserved", withSlots([15, 0, 1], [14, 0, 1], (s) => (s[11] = 1)), /illegal/],
    ["brick state succ 1 tries 1", withSlots([15, 1, 1], [14, 0, 1]), /slot a in an illegal state/],
    ["priority 16", withSlots([16, 0, 1], [14, 0, 1]), /illegal/],
    ["tries 8", withSlots([15, 8, 0], [14, 0, 1]), /illegal/],
    ["successful 2", withSlots([15, 0, 2], [14, 0, 1]), /illegal/],
    ["no bootable slot", withSlots([15, 0, 0], [14, 0, 0]), /no bootable slot/],
    ["tie", withSlots([15, 0, 1], [15, 0, 1]), /same priority/],
    ["next trying", withSlots([15, 7, 0], [14, 0, 1]), /not marked successful/],
    ["short", new Uint8Array(31), /512 bytes/],
  ];
  for (const [name, s, re] of cases) {
    const r = checkBeforeEntry(s);
    assert.equal(r.ok, false, name);
    assert.match(r.reason, re, name);
  }
});

test("A/B: CRC stored little-endian is refused and named", () => {
  const s = withSlots([15, 0, 1], [14, 0, 1]);
  const c = crc32(s.subarray(0, 28));
  new DataView(s.buffer).setUint32(28, c, true);
  assert.equal(validateAb(parseAb(s)), "crc32 stored little-endian (must be big-endian)");
  s[31] ^= 0xff;
  assert.equal(validateAb(parseAb(s)), "crc32 mismatch");
});

// otherSlotKind mirrors the CLI other_slot_kind: the next slot a keeper, the
// other a keeper ("successful") or never tried ("never successful").
test("otherSlotKind: both successful (S0) and the factory never-tried state (U3) are accepted", () => {
  assert.equal(otherSlotKind(parseAb(sector(S0)).slots, 0), "successful"); // A{15,0,1} B{14,0,1}, next a
  assert.equal(otherSlotKind(parseAb(sector(U3)).slots, 0), "never successful"); // A{15,0,1} B{14,7,0}, next a
});

test("otherSlotKind: a non-keeper next, or an other slot that is not keeper/never-tried, is null", () => {
  const sl = (a, b) => parseAb(withSlots(a, b)).slots;
  assert.equal(otherSlotKind(sl([15, 7, 0], [14, 0, 1]), 0), null); // next not a keeper (trying)
  assert.equal(otherSlotKind(sl([15, 0, 1], [0, 0, 0]), 0), null); // other disabled (priority 0)
  assert.equal(otherSlotKind(sl([15, 0, 1], [14, 3, 0]), 0), null); // other trying but only 3 tries
  assert.equal(otherSlotKind(sl([14, 0, 1], [15, 7, 0]), 0), null); // never-tried but priority not below the next's
  assert.equal(otherSlotKind(sl([15, 0, 1], [14, 7, 0]), 0), "never successful"); // never-tried, priority below
});

test("BCB: the payload is the command and zeros, with the pinned sha", async () => {
  const p = bcbPayload();
  assert.equal(p.length, BCB_BYTES);
  assert.equal(new TextDecoder().decode(p.subarray(0, 19)), "bootonce-bootloader");
  assert.ok(p.subarray(19).every((b) => b === 0));
  assert.equal(sha(p), PAYLOAD_SHA256);
  assert.deepEqual(await checkedPayload(), p);
});

test("BCB: the known states are exactly the pinned json's three (sha256 ed0824f2...)", () => {
  // The page's KNOWN_BCB must equal the set in host/bcb-known-states.json.
  const raw = readFileSync(new URL("../../../host/bcb-known-states.json", import.meta.url));
  assert.equal(sha(raw), "ed0824f2aabe4fe71860c900e8323ecba24de562d14091ff88e9a999ce2f00ac", "pinned json sha256");
  const states = JSON.parse(raw.toString()).states;
  assert.equal(KNOWN_BCB.length, states.length, "same count");
  assert.deepEqual(
    [...KNOWN_BCB].map((k) => k.sha256).sort(),
    states.map((s) => s.sha256).sort(),
    "same sha256 set as the pinned json",
  );
  for (const s of states) {
    const k = KNOWN_BCB.find((x) => x.sha256 === s.sha256);
    assert.ok(k, `page has json state ${s.name}`);
    assert.equal(k.firstSectorSha256, s.first_sector_sha256, s.name);
  }
  // the structural shas are the real ones
  assert.equal(KNOWN_BCB.find((k) => k.needsWrite && k.name.startsWith("empty")).sha256, sha(new Uint8Array(2048)));
  assert.equal(KNOWN_BCB.find((k) => !k.needsWrite).sha256, sha(bcbPayload()));
  assert.ok(KNOWN_BCB.some((k) => k.sha256 === "529bdb7b79cda345bf3a1cec3e72a8f208cac57627c4c115c43cc470a140d319"));
  assert.ok(Object.isFrozen(KNOWN_BCB) && KNOWN_BCB.every(Object.isFrozen));
});

test("BCB: the archive states afaa05c4 and fecfe0e2 are NOT accepted, and classifyBcb matches by sha only", async () => {
  for (const p of ["afaa05c4", "fecfe0e2"]) {
    assert.equal(KNOWN_BCB.some((k) => k.sha256.startsWith(p) || k.firstSectorSha256.startsWith(p)), false, p);
  }
  const src = readFileSync(new URL("../../site/js/bcb.js", import.meta.url), "utf8");
  assert.match(src, /known: KNOWN_BCB\.find\(\(k\) => k\.sha256 === sha\) \|\| null/);
  assert.doesNotMatch(src.replace(/\/\/.*$/gm, ""), /afaa05c4|fecfe0e2/);
});

test("BCB: known states and the unknown (stale) refusal before any write", async () => {
  assert.equal((await classifyBcb(new Uint8Array(2048))).known.name, "empty (all zero)");
  assert.equal((await classifyBcb(bcbPayload())).known.needsWrite, false);
  const odd = new Uint8Array(2048);
  odd[2047] = 1;
  assert.equal((await classifyBcb(odd)).known, null);
  const rec = new Uint8Array(2048);
  rec.set(new TextEncoder().encode("boot-recovery"));
  assert.equal((await classifyBcb(rec)).known, null);
  assert.equal((await classifyBcb(new Uint8Array(512))).known, null);
  assert.equal(commandText(bcbPayload()), "bootonce-bootloader");
});

test("fastboot: only single getvars from the list, download and boot; no serialno, no at-*, no getvar all", () => {
  for (const v of GETVARS) assert.equal(commandAllowed(`getvar:${v}`), true, v);
  assert.equal(commandAllowed("boot"), true);
  assert.equal(commandAllowed("download:01e12000"), true);
  const refused = ["getvar:all", "getvar:ALL", "getvar: all", "getvar:all\n", "getvar:", "getvar:serialno", "getvar:unlocked",
    "getvar:at-vboot-state", "getvar:at-bootloader-version",
    "download:1E12000", "download:01E12000", "download:1234567", "download:123456789", "boot ", "flash:boot_a", "erase:misc",
    "oem unlock", "flashing unlock", "set_active:a", "reboot", "reboot-bootloader", "continue", "upload", "", null, 5];
  for (const c of refused) assert.equal(commandAllowed(c), false, String(c));
  assert.ok(!GETVARS.includes("all"));
  assert.ok(!GETVARS.includes("serialno"));
  assert.ok(![...GETVARS].some((v) => v.startsWith("at-")));
});

test("fastboot: max-download-size is hex, as in AOSP fastboot", () => {
  assert.equal(parseSize("0x08000000"), 0x08000000);
  assert.equal(parseSize("08000000"), 0x08000000);
  assert.equal(parseSize(" 0X8000000 "), 0x8000000);
  assert.ok(Number.isNaN(parseSize("")));
  assert.ok(Number.isNaN(parseSize(null)));
  assert.ok(Number.isNaN(parseSize("128M")));
  assert.ok(Number.isNaN(parseSize("0x123456789")));
});
