// SPDX-License-Identifier: GPL-2.0-or-later
// bcb.js: the boot command block (BCB) in misc, eMMC LBA 49184..49187
// (misc start 49152 + 32 sectors, 2048 bytes), and the one payload this page
// may write there.

export const BCB_BYTES = 2048;
export const BOOT_COMMAND = "bootonce-bootloader";

// The reviewed payload: "bootonce-bootloader", then zeros up to 2048 bytes.
// The same bytes as ucp10/tools/p10-bcb-fastboot.sh.
export const PAYLOAD_SHA256 = "892627cb4356d1d16c37bf243d700b5c9a0ab672d263de9ccd640c86b5cd6a5a";

// The BCB states before the write that the page accepts: the SAME set as the
// pinned host/bcb-known-states.json of p10-enter (sha256 ed0824f2...). Any
// other content stops the page with nothing written (a stale/unknown BCB is
// refused BEFORE the write, MF-2). A state is added only from a fresh read-only
// dump of a unit, with an Opus review and the owner's approval. The archive
// states afaa05c4... and fecfe0e2... are NOT here: their cause is unknown and
// they may be from unit 1. Unit 2's normal BCB (529bdb7b...) IS here from the
// read-only dump of 2026-10-30 (M-1): its command field is empty (first sector
// all zero), so no command is pending and the page writes the payload over it.
// The decoders.test asserts this set equals the pinned json's three states.
export const KNOWN_BCB = Object.freeze([
  Object.freeze({
    sha256: "e5a00aa9991ac8a5ee3109844d84a55583bd20572ad3ffcd42792f3c36b183ad",
    firstSectorSha256: "076a27c79e5ace2a3d47f9dd2e83e4ff6ea8872b3c2218f66c92b89b55f36560",
    name: "empty (all zero)",
    kind: "clean",
    needsWrite: true,
  }),
  Object.freeze({
    sha256: PAYLOAD_SHA256,
    firstSectorSha256: "91f3806a509ae09a4673b3da4e3e25744b7c2cb1117fa3bf95d6a780ce233e14",
    name: "the boot command is already set",
    kind: "command",
    needsWrite: false,
  }),
  Object.freeze({
    sha256: "529bdb7b79cda345bf3a1cec3e72a8f208cac57627c4c115c43cc470a140d319",
    firstSectorSha256: "076a27c79e5ace2a3d47f9dd2e83e4ff6ea8872b3c2218f66c92b89b55f36560",
    name: "unit 2's normal boot command block (command field empty)",
    kind: "clean",
    needsWrite: true,
  }),
]);

export async function sha256hex(bytes) {
  const d = await crypto.subtle.digest("SHA-256", bytes);
  return Array.from(new Uint8Array(d), (b) => b.toString(16).padStart(2, "0")).join("");
}

export function bcbPayload() {
  const p = new Uint8Array(BCB_BYTES);
  p.set(new TextEncoder().encode(BOOT_COMMAND), 0);
  return p;
}

// checkedPayload builds the payload and checks it against the pinned sha.
export async function checkedPayload() {
  const p = bcbPayload();
  if ((await sha256hex(p)) !== PAYLOAD_SHA256) throw new Error("BCB payload does not match its pinned sha256");
  return p;
}

// classifyBcb returns { sha256, known } with known = the KNOWN_BCB entry or null.
export async function classifyBcb(bytes) {
  if (!(bytes instanceof Uint8Array) || bytes.length !== BCB_BYTES) {
    return { sha256: null, known: null };
  }
  const sha = await sha256hex(bytes);
  return { sha256: sha, known: KNOWN_BCB.find((k) => k.sha256 === sha) || null };
}

// commandText shows the command field (bytes 0..31) for the log.
export function commandText(bytes) {
  let s = "";
  for (const b of bytes.subarray(0, 32)) {
    if (b === 0) break;
    s += b >= 0x20 && b < 0x7f ? String.fromCharCode(b) : "?";
  }
  return s;
}
