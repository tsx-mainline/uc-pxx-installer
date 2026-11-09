// SPDX-License-Identifier: GPL-2.0-or-later
// Unit tests: the factory-block model decoder and the 0xCC "not readable" rule
// (factory.js), a port of the CLI factory_model() and factory_unserved().
import { test } from "node:test";
import assert from "node:assert/strict";
import { factoryModel, factoryUnserved, FACTORY_SECTORS, SECTOR, UNSERVED_BYTE } from "../../site/js/factory.js";

const SIZE = FACTORY_SECTORS * SECTOR;

// buildFactory makes a 4096-byte block with one tag-650 model record and the
// correct big-endian checksum.
function buildFactory(model, opts = {}) {
  const data = new Uint8Array(SIZE);
  const dv = new DataView(data.buffer);
  const mb = new TextEncoder().encode(model);
  const words = Math.ceil(mb.length / 2);
  data.set([0x41, 0x30, 0x30], 40); // hw rev "A00" -> VA.00
  dv.setUint16(90, opts.records ?? 1, true);
  dv.setUint16(92, opts.tag ?? 650, true);
  dv.setUint16(94, words, true);
  data.set(mb, 96);
  const L = 96 + 2 * words;
  dv.setUint32(0, opts.len ?? L, true);
  let total = 0;
  for (let i = 0; i < L; i += 2) if (i !== 4) total = (total + ((data[i] << 8) | data[i + 1])) & 0xffffffff;
  let want = (-total) & 0xffff;
  if (want === 0) { data[L - 1] = 1; total = (total + 1) & 0xffffffff; want = (-total) & 0xffff; }
  dv.setUint16(4, opts.badChecksum ? (want ^ 0xffff) : want, false);
  return data;
}

test("factoryModel reads UC-P10 and UC-P8 and the hardware revision", () => {
  let r = factoryModel(buildFactory("UC-P10-0001"));
  assert.equal(r.error, null);
  assert.equal(r.base, "UC-P10");
  assert.equal(r.value, "UC-P10-0001");
  assert.equal(r.hwrev, "VA.00");
  r = factoryModel(buildFactory("UC-P8-0002"));
  assert.equal(r.base, "UC-P8");
  // "UC-P8" is tried before "UC-P10", so a UC-P8xx string is a UC-P8
  assert.equal(factoryModel(buildFactory("UC-P8")).base, "UC-P8");
});

test("factoryModel refuses a bad checksum, a non-UC model, no model record, and a wrong size", () => {
  assert.match(factoryModel(buildFactory("UC-P10", { badChecksum: true })).error, /checksum is wrong/);
  assert.match(factoryModel(buildFactory("ACME-9000")).error, /no UC-P10 or UC-P8/);
  assert.match(factoryModel(buildFactory("UC-P10", { records: 0 })).error, /0 model records/);
  assert.match(factoryModel(buildFactory("UC-P10", { tag: 1 })).error, /0 model records/);
  assert.match(factoryModel(new Uint8Array(100)).error, /bytes, want/);
  assert.match(factoryModel(buildFactory("UC-P10", { len: 3 })).error, /out of range/);
});

test("factoryUnserved: all 0xCC is 'not readable'; anything else is not", () => {
  assert.equal(factoryUnserved(new Uint8Array(SIZE).fill(UNSERVED_BYTE)), true);
  assert.equal(factoryUnserved(buildFactory("UC-P10")), false);
  const mostly = new Uint8Array(SIZE).fill(UNSERVED_BYTE);
  mostly[0] = 0; // one byte differs -> it IS data (goes to factoryModel), not "unserved"
  assert.equal(factoryUnserved(mostly), false);
  assert.equal(factoryUnserved(new Uint8Array(SIZE)), false); // all zero is not 0xCC
  assert.equal(factoryUnserved(new Uint8Array(100).fill(UNSERVED_BYTE)), false); // wrong size
});
