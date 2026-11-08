// SPDX-License-Identifier: GPL-2.0-or-later
// gpt.js: a read-only decoder of the primary GPT (LBA 0..33), and the layout
// gate for the two absolute LBAs the page reads and writes. A direct port of
// host/p10-enter gpt_parts() and check_layout() (uc-pxx-installer p10enter-plain
// f631a8a). The page reads the GPT only to GATE: it never uses the GPT to
// choose an LBA. misc and cfg must be exactly where the page's constants put
// them, or the page refuses before any write (CLI exit 27 "LAYOUT").
import { crc32 } from "./avbab.js";

export const SECTOR = 512;
export const GPT_LBA = 0;
export const GPT_SECTORS = 34; // protective MBR + primary header + 32 entry sectors

// Where misc and cfg must be. These are the same absolute LBAs rockusb.js and
// the factory read use; the gate proves the GPT agrees with them.
export const MISC_START = 49152;
export const MISC_SECTORS = 8192;
export const CFG_START = 3666944;
export const CFG_MIN_SECTORS = 8; // the factory read is 8 sectors from CFG_START

function u32le(b, o) {
  return new DataView(b.buffer, b.byteOffset, b.byteLength).getUint32(o, true);
}
function u64le(b, o) {
  const v = new DataView(b.buffer, b.byteOffset, b.byteLength);
  return v.getUint32(o, true) + v.getUint32(o + 4, true) * 0x100000000;
}

// gptParts parses the primary GPT and returns { name: [firstLba, sectors] }.
// It checks the signature, the header CRC32, the entry-array CRC32 and the
// entry size. It throws Error with a reason on anything invalid.
export function gptParts(data) {
  if (!(data instanceof Uint8Array) || data.length !== GPT_SECTORS * SECTOR) {
    throw new Error(`${data && data.length} bytes, want ${GPT_SECTORS * SECTOR}`);
  }
  const h = data.subarray(SECTOR, 2 * SECTOR);
  if (new TextDecoder().decode(h.subarray(0, 8)) !== "EFI PART") throw new Error("no GPT signature at LBA 1");
  const hsize = u32le(h, 12);
  if (!(hsize >= 92 && hsize <= SECTOR)) throw new Error(`GPT header size ${hsize}`);
  // header CRC32: bytes 0..15, then four zero bytes for the CRC field, then 20..hsize
  const forCrc = new Uint8Array(hsize);
  forCrc.set(h.subarray(0, 16), 0);
  forCrc.set(h.subarray(20, hsize), 20);
  if ((crc32(forCrc) >>> 0) !== u32le(h, 16)) throw new Error("the GPT header CRC32 does not match");
  if (u64le(h, 24) !== 1) throw new Error("the header is not the primary one (my LBA is not 1)");
  const entLba = u64le(h, 72);
  const n = u32le(h, 80);
  const esz = u32le(h, 84);
  if (entLba !== 2 || esz !== 128 || !(n > 0 && n <= 128)) {
    throw new Error(`entries at LBA ${entLba}, ${n} of ${esz} bytes: want LBA 2, at most 128 of 128`);
  }
  const ents = data.subarray(2 * SECTOR, 2 * SECTOR + n * esz);
  if ((crc32(ents) >>> 0) !== u32le(h, 88)) throw new Error("the GPT entry array CRC32 does not match");
  const parts = {};
  for (let i = 0; i < n; i++) {
    const e = ents.subarray(i * esz, (i + 1) * esz);
    if (e.subarray(0, 16).every((x) => x === 0)) continue;
    const first = u64le(e, 32);
    const last = u64le(e, 40);
    let name = new TextDecoder("utf-16le").decode(e.subarray(56, 128));
    const z = name.indexOf("\0");
    if (z >= 0) name = name.slice(0, z);
    if (name in parts) throw new Error(`the partition name "${name}" twice`);
    if (last < first) throw new Error(`partition "${name}" ends before it starts`);
    parts[name] = [first, last - first + 1];
  }
  return parts;
}

// checkLayout returns an error text, or null when misc and cfg are where the
// page reads and writes by absolute LBA.
export function checkLayout(parts) {
  const misc = parts.misc;
  const cfg = parts.cfg;
  if (!misc || misc[0] !== MISC_START || misc[1] !== MISC_SECTORS) {
    return `misc is ${misc ? `[${misc}]` : "absent"}, not LBA ${MISC_START} with ${MISC_SECTORS} sectors`;
  }
  if (!cfg || cfg[0] !== CFG_START || cfg[1] < CFG_MIN_SECTORS) {
    return `cfg is ${cfg ? `[${cfg}]` : "absent"}, not at LBA ${CFG_START}`;
  }
  return null;
}
