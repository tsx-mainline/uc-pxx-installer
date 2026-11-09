// SPDX-License-Identifier: GPL-2.0-or-later
// avbab.js: read-only decoder of the A/B metadata (AvbABData), the first 32
// bytes of eMMC LBA 49156. The same rules as internal/avbab (Go):
//
//   0..3   magic "\0AB0"      4 version_major (1)   5 version_minor   6..7 reserved
//   8..11  slot A {priority, tries_remaining, successful_boot, reserved}
//   12..15 slot B {same}
//   16..27 reserved           28..31 crc32 of bytes 0..27, BIG-endian
//
// This file only decodes. Nothing in this page writes LBA 49156.

const CRC_TABLE = (() => {
  const t = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    t[n] = c >>> 0;
  }
  return t;
})();

export function crc32(bytes) {
  let c = 0xffffffff;
  for (const b of bytes) c = CRC_TABLE[(c ^ b) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

export function parseAb(sector) {
  if (!(sector instanceof Uint8Array) || sector.length < 32) throw new Error("A/B sector shorter than 32 bytes");
  const raw = sector.slice(0, 32);
  const v = new DataView(raw.buffer);
  const slots = [0, 1].map((i) => {
    const o = 8 + 4 * i;
    return { priority: raw[o], tries: raw[o + 1], successful: raw[o + 2], reserved: raw[o + 3] };
  });
  return { raw, slots, crcStored: v.getUint32(28, false), crcComputed: crc32(raw.subarray(0, 28)) };
}

// libavb can hold only successful {tries 0, succ 1}, trying {tries > 0,
// succ 0} or unbootable {tries 0, succ 0}. Successful with tries > 0 is the
// state that bricked a unit.
export function slotLegal(s) {
  if (s.priority > 15 || s.tries > 7 || s.reserved !== 0) return false;
  if (s.successful === 1) return s.tries === 0;
  return s.successful === 0;
}

export function slotBootable(s) {
  return s.priority > 0 && (s.successful === 1 || s.tries > 0);
}

export function slotSuccessful(s) {
  return s.successful === 1 && s.tries === 0 && s.priority >= 1;
}

// libavb_ab AVB_AB_MAX_TRIES_REMAINING: the tries of a slot that was never
// tried (the factory-stock "never successful" slot has tries 7, succ 0).
export const MAX_TRIES = 7;

// otherSlotKind mirrors the reviewed CLI other_slot_kind (host/p10-enter
// p10enter-plain f631a8a). May the route run with this A/B state? The slot that
// boots next must be a keeper (successful). The other slot must be a keeper too
// ("successful", the state of unit 2), or never tried (trying with MAX_TRIES
// tries and a priority 1..below the next slot's, succ 0, reserved 0 — the
// factory-stock state of a unit whose other slot never booted, e.g. unit 3
// A{15,0,1} B{14,7,0}). The route boots neither slot, and the next slot stays
// the successful fallback. Every other state -> null (CLI exit 23).
export function otherSlotKind(slots, next) {
  const k = slots[next];
  const o = slots[1 - next];
  if (!slotSuccessful(k)) return null;
  if (slotSuccessful(o)) return "successful";
  if (o.successful === 0 && o.tries === MAX_TRIES && o.priority >= 1 && o.priority < k.priority && o.reserved === 0) {
    return "never successful";
  }
  return null;
}

export function slotState(s) {
  if (!slotLegal(s)) return "ILLEGAL";
  if (!slotLegal(s)) return "ILLEGAL";
  if (!slotBootable(s)) return "unbootable";
  return s.successful === 1 ? "successful" : "trying";
}

// validateAb returns null when the sector is valid, else the reason.
export function validateAb(ab) {
  const r = ab.raw;
  if (r[0] !== 0 || r[1] !== 0x41 || r[2] !== 0x42 || r[3] !== 0x30) return "bad magic";
  if (r[4] !== 1) return `unsupported version_major ${r[4]}`;
  if (r[6] !== 0 || r[7] !== 0) return "reserved bytes 6..7 not zero";
  for (let i = 16; i < 28; i++) if (r[i] !== 0) return "reserved bytes 16..27 not zero";
  if (ab.crcStored !== ab.crcComputed) {
    const le = new DataView(r.buffer).getUint32(28, true);
    return le === ab.crcComputed ? "crc32 stored little-endian (must be big-endian)" : "crc32 mismatch";
  }
  for (let i = 0; i < 2; i++) {
    if (!slotLegal(ab.slots[i])) return `slot ${"ab"[i]} in an illegal state`;
  }
  if (!slotBootable(ab.slots[0]) && !slotBootable(ab.slots[1])) return "no bootable slot";
  return null;
}

// pickSlot is the libavb choice: the bootable slot with the higher priority,
// slot A on a tie, -1 if none.
export function pickSlot(ab) {
  const [a, b] = ab.slots.map(slotBootable);
  if (a && b) return ab.slots[1].priority > ab.slots[0].priority ? 1 : 0;
  if (a) return 0;
  if (b) return 1;
  return -1;
}

export function isTie(ab) {
  return slotBootable(ab.slots[0]) && slotBootable(ab.slots[1]) && ab.slots[0].priority === ab.slots[1].priority;
}

export function describeSlot(s) {
  return `{prio ${s.priority} tries ${s.tries} succ ${s.successful}} ${slotState(s)}`;
}

// checkBeforeEntry is the rule of the entry page (plan §3.1 step 2): a valid
// sector, no tie, and the slot that boots next is successful. It returns
// { ok, reason, next } with next = "a" or "b".
export function checkBeforeEntry(sector) {
  if (!(sector instanceof Uint8Array) || sector.length !== 512) {
    return { ok: false, reason: "the A/B read did not return 512 bytes" };
  }
  const ab = parseAb(sector);
  const text = `A ${describeSlot(ab.slots[0])}, B ${describeSlot(ab.slots[1])}`;
  const bad = validateAb(ab);
  if (bad) return { ok: false, reason: `A/B metadata not valid: ${bad}`, text };
  if (isTie(ab)) return { ok: false, reason: "both slots have the same priority (the boot choice is not proven)", text };
  const i = pickSlot(ab);
  const next = "ab"[i];
  if (!slotSuccessful(ab.slots[i])) {
    return { ok: false, reason: `the slot that boots next (${next}) is not marked successful`, text, next };
  }
  return { ok: true, next, text };
}
