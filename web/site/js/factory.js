// SPDX-License-Identifier: GPL-2.0-or-later
// factory.js: a read-only decoder of the factory data block (cfg, LBA 3666944,
// 8 sectors), only for the model gate. A direct port of host/p10-enter
// factory_model() and factory_unserved() (uc-pxx-installer p10enter-plain
// f631a8a). The block ALSO holds the unit's MAC address and other secrets: this
// decoder returns only the model and the hardware revision, the page keeps the
// bytes in memory and never logs or sends them, and only the model is shown.
export const SECTOR = 512;
export const FACTORY_LBA = 3666944;
export const FACTORY_SECTORS = 8;
// The byte of a read the U-Boot download mode does not serve. Measured on unit 2
// and unit 3 (2026-11-03, DLMODE.md 8.1): `rl 3666944 8` gave 4096 bytes of
// 0xCC. A whole block of this byte means "not readable in the download mode".
export const UNSERVED_BYTE = 0xcc;

// factoryUnserved: true only when the read is the whole block and every byte is
// UNSERVED_BYTE. A block with only some such bytes is data and goes to
// factoryModel as before.
export function factoryUnserved(data) {
  return data instanceof Uint8Array && data.length === FACTORY_SECTORS * SECTOR &&
    data.every((b) => b === UNSERVED_BYTE);
}

// factoryModel returns { base, value, suffix, hwrev, error }. base is "UC-P10"
// or "UC-P8" (or null with an error). It is the rule of internal/factory
// written here on its own: the length L (u32 LE) in range and even; the
// checksum (bytes 4..5, big-endian: the negated sum of the big-endian words of
// [0, L) without the word at 4; 0 is never valid); the records from byte 92
// (u16 tag, u16 length in words) inside L; exactly one record 650, printable
// text; "UC-P8" in the first 5 bytes, else "UC-P10" in the first 6.
export function factoryModel(data) {
  const fail = (hwrev, error) => ({ base: null, value: null, suffix: null, hwrev: hwrev || null, error });
  if (!(data instanceof Uint8Array) || data.length !== FACTORY_SECTORS * SECTOR) {
    return fail(null, `${data && data.length} bytes, want ${FACTORY_SECTORS * SECTOR}`);
  }
  const v = new DataView(data.buffer, data.byteOffset, data.byteLength);
  const L = v.getUint32(0, true);
  if (!(L >= 0x57 && L <= data.length)) return fail(null, `length ${L} out of range`);
  if (L % 2) return fail(null, `length ${L} is odd`);
  let total = 0;
  for (let i = 0; i < L; i += 2) {
    if (i !== 4) total = (total + ((data[i] << 8) | data[i + 1])) & 0xffffffff;
  }
  const stored = v.getUint16(4, false);
  const want = (-total) & 0xffff;
  const rev = data.subarray(40, 43);
  const hwrev = [...rev].every((c) => c >= 0x21 && c <= 0x7e)
    ? `V${String.fromCharCode(rev[0])}.${String.fromCharCode(rev[1])}${String.fromCharCode(rev[2])}` : null;
  const count = v.getUint16(90, true);
  const models = [];
  let p = 92;
  for (let i = 0; i < count; i++) {
    if (p + 4 > L) return fail(hwrev, `record ${i + 1} past the length`);
    const tag = v.getUint16(p, true);
    const words = v.getUint16(p + 2, true);
    const end = p + 4 + 2 * words;
    if (end > L) return fail(hwrev, `record ${i + 1} (tag ${tag}) past the length`);
    if (tag === 650) models.push(data.subarray(p + 4, end));
    p = end;
  }
  if (stored === 0 || stored !== want) {
    return fail(hwrev, `the checksum is wrong (stored ${stored.toString(16).padStart(4, "0")}, computed ${want.toString(16).padStart(4, "0")})`);
  }
  if (models.length !== 1) return fail(hwrev, `${models.length} model records (tag 650), want one`);
  if (models[0].length > 64) return fail(hwrev, `the model record has ${models[0].length} bytes, more than 64`);
  let end = models[0].length;
  while (end > 0 && models[0][end - 1] === 0) end--;
  const m = models[0].subarray(0, end);
  if (m.length === 0 || ![...m].every((c) => c >= 0x20 && c <= 0x7e)) {
    return fail(hwrev, "the model record is not printable text");
  }
  const value = new TextDecoder().decode(m);
  for (const base of ["UC-P8", "UC-P10"]) {
    if (value.startsWith(base)) return { base, value, suffix: value.slice(base.length), hwrev, error: null };
  }
  return fail(hwrev, `it names no UC-P10 or UC-P8: "${value}"`);
}
