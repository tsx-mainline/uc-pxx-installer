// SPDX-License-Identifier: GPL-2.0-or-later
// Unit tests: the GPT decoder and the layout gate (gpt.js), a port of the CLI
// gpt_parts() and check_layout().
import { test } from "node:test";
import assert from "node:assert/strict";
import { crc32 } from "../../site/js/avbab.js";
import { checkLayout, CFG_START, gptParts, GPT_SECTORS, MISC_SECTORS, MISC_START, SECTOR } from "../../site/js/gpt.js";

// buildGpt makes a valid primary GPT with the given [name, firstLba, sectors].
function buildGpt(parts, opts = {}) {
  const N = 128, ESZ = 128;
  const data = new Uint8Array(GPT_SECTORS * SECTOR);
  const ents = new Uint8Array(N * ESZ);
  const edv = new DataView(ents.buffer);
  parts.forEach(([name, first, sectors], i) => {
    const o = i * ESZ;
    ents[o] = 0xa5;
    ents[o + 16] = 0x5a;
    edv.setUint32(o + 32, first >>> 0, true);
    edv.setUint32(o + 36, Math.floor(first / 0x100000000), true);
    const last = first + sectors - 1;
    edv.setUint32(o + 40, last >>> 0, true);
    edv.setUint32(o + 44, Math.floor(last / 0x100000000), true);
    for (let k = 0; k < name.length; k++) edv.setUint16(o + 56 + 2 * k, name.charCodeAt(k), true);
  });
  data.set(ents, 2 * SECTOR);
  const h = new Uint8Array(92);
  const hv = new DataView(h.buffer);
  h.set(new TextEncoder().encode(opts.badSig ? "XFI PART" : "EFI PART"), 0);
  hv.setUint32(8, 0x00010000, true);
  hv.setUint32(12, 92, true);
  hv.setUint32(24, opts.notPrimary ? 2 : 1, true);
  hv.setUint32(32, 33, true);
  hv.setUint32(72, 2, true);
  hv.setUint32(80, N, true);
  hv.setUint32(84, ESZ, true);
  // Byte 88 (the entry-array CRC) is covered by the header CRC, so finalise it
  // BEFORE computing the header CRC at byte 16.
  hv.setUint32(88, (crc32(ents) >>> 0) ^ (opts.badEntryCrc ? 1 : 0), true);
  hv.setUint32(16, (crc32(h) >>> 0) ^ (opts.badHeaderCrc ? 1 : 0), true);
  data.set(h, SECTOR);
  return data;
}

const GOOD = [["uboot", 16384, 8192], ["misc", MISC_START, MISC_SECTORS], ["cfg", CFG_START, 262144], ["userdata", 3932160, 100000]];

test("gptParts reads misc and cfg from a valid GPT; checkLayout passes", () => {
  const parts = gptParts(buildGpt(GOOD));
  assert.deepEqual(parts.misc, [MISC_START, MISC_SECTORS]);
  assert.deepEqual(parts.cfg, [CFG_START, 262144]);
  assert.equal(checkLayout(parts), null);
});

test("gptParts rejects a bad signature, a bad header CRC32, a bad entry CRC32, and a non-primary header", () => {
  assert.throws(() => gptParts(buildGpt(GOOD, { badSig: true })), /no GPT signature/);
  assert.throws(() => gptParts(buildGpt(GOOD, { badHeaderCrc: true })), /header CRC32/);
  assert.throws(() => gptParts(buildGpt(GOOD, { badEntryCrc: true })), /entry array CRC32/);
  assert.throws(() => gptParts(buildGpt(GOOD, { notPrimary: true })), /not the primary/);
  assert.throws(() => gptParts(new Uint8Array(100)), /bytes, want/);
});

test("checkLayout refuses misc or cfg in the wrong place", () => {
  assert.match(checkLayout(gptParts(buildGpt([["misc", MISC_START, 4096], ["cfg", CFG_START, 262144]]))), /misc is/);
  assert.match(checkLayout(gptParts(buildGpt([["misc", 1000, MISC_SECTORS], ["cfg", CFG_START, 262144]]))), /misc is/);
  assert.match(checkLayout(gptParts(buildGpt([["misc", MISC_START, MISC_SECTORS], ["cfg", 1, 262144]]))), /cfg is/);
  assert.match(checkLayout(gptParts(buildGpt([["misc", MISC_START, MISC_SECTORS], ["cfg", CFG_START, 4]]))), /cfg is/);
  assert.match(checkLayout(gptParts(buildGpt([["misc", MISC_START, MISC_SECTORS]]))), /cfg is absent/);
  assert.match(checkLayout(gptParts(buildGpt([["cfg", CFG_START, 262144]]))), /misc is absent/);
});
