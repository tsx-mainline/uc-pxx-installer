// SPDX-License-Identifier: GPL-2.0-or-later
// bcb.js: the boot command block (BCB) in misc, eMMC LBA 49184..49187
// (misc start 49152 + 32 sectors, 2048 bytes), and the one payload this page
// may write there.

export const BCB_BYTES = 2048;
export const BOOT_COMMAND = "bootonce-bootloader";

// The reviewed payload: "bootonce-bootloader", then zeros up to 2048 bytes.
// The same bytes as ucp10/tools/p10-bcb-fastboot.sh.
export const PAYLOAD_SHA256 = "892627cb4356d1d16c37bf243d700b5c9a0ab672d263de9ccd640c86b5cd6a5a";

// The BCB command strings this unit's U-Boot ACTS ON, by an exact strcmp of the
// command field (bytes 0..31) only. Verified from the 1.0.0.94 userdebug
// disassembly (and the same for all 37 official U-Boots):
//   android_bootloader_boot_flow (0x223d4c) runs, in order,
//     strcmp(cmd, "bootonce-bootloader") (0x223e34) -> clear + 36-sector write +
//       fastboot,
//     strcmp(cmd, "boot-recovery")       (0x223e6c) -> recovery,
//     strcmp(cmd, "boot_recovery")       (0x223e80) -> recovery,
//     otherwise -> normal boot;
//   rockchip_get_boot_mode (0x226df8) tests only strcmp(cmd, "boot-recovery").
// Every OTHER command field is ignored (normal boot), so a leftover is harmless.
// Sources: tsx-mainline/plan/uc-pxx S2-BCB-UNKNOWN-CMD.md §2, STOCK-ENTRY-ROUTES.md
// ("strcmp chain bootonce-bootloader -> boot-recovery -> boot_recovery, each an
// exact strcmp"), S2-EXIT13-ANALYSIS.md §1; RULES.md "U-Boot ignores an unknown
// BCB command string (exact strcmp only), so leftover bytes there are harmless".
//
// BOOT_COMMAND ("bootonce-bootloader") is this page's own entry command. The two
// below are the recovery/OTA commands the page must NOT overwrite.
export const RECOVERY_COMMANDS = Object.freeze(["boot-recovery", "boot_recovery"]);

// NOTE: host/bcb-known-states.json (sha256 ed0824f2...) is the pinned
// whole-block allow-list of the CLI (p10-enter). This page no longer gates on a
// whole-block sha: a real unit's BCB after a boot is a RAM leftover that varies
// from boot to boot (seen: command field "b?", and d8 85 00 ... on 2026-11-08),
// is not one of those three states, and refusing it would block every
// re-install. The page gates on the command field (classifyCommand below, and
// enter.js readState), so that json is NOT used here.

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

// commandText shows the command field (bytes 0..31) for the log and the UI.
// Printable ASCII only; any other byte becomes "?". The command field may hold
// U-Boot RAM (which can contain the MAC), so this printable-escaped form is the
// ONLY rendering of it that ever reaches a log; its raw bytes are never logged.
export function commandText(bytes) {
  let s = "";
  for (const b of bytes.subarray(0, 32)) {
    if (b === 0) break;
    s += b >= 0x20 && b < 0x7f ? String.fromCharCode(b) : "?";
  }
  return s;
}

// rawCommandField decodes the command field (bytes 0..31) up to the first NUL
// as one char per byte, non-ASCII bytes kept. This is the exact string U-Boot
// strcmp()s: keeping the raw bytes means a leftover can never accidentally
// compare equal to an act-on command. For display use commandText, never this.
export function rawCommandField(bytes) {
  let s = "";
  for (const b of bytes.subarray(0, 32)) {
    if (b === 0) break;
    s += String.fromCharCode(b);
  }
  return s;
}

// classifyCommand is the page's dynamic BCB pre-state gate. It looks ONLY at the
// command field and returns { cmd, display, action, reason, name }:
//   action "write" — the field is empty (no command pending), reason "empty";
//     OR an unrecognised leftover U-Boot ignores, reason "leftover". The page
//     writes BOOT_COMMAND.
//   action "skip"  — BOOT_COMMAND is already set (reason "already-set"). The
//     entry command is in place; the page skips the write and goes on
//     (idempotent: a re-run after a half-finished entry still works).
//   action "stop"  — a recovery/OTA command U-Boot acts on is pending (reason
//     "recovery-pending"). The page must NOT clobber it; it stops, nothing
//     written.
export function classifyCommand(bytes) {
  const cmd = rawCommandField(bytes);
  const display = commandText(bytes);
  if (cmd === "") {
    return { cmd, display, action: "write", reason: "empty", name: "empty (no command set)" };
  }
  if (cmd === BOOT_COMMAND) {
    return { cmd, display, action: "skip", reason: "already-set", name: "the boot command is already set" };
  }
  if (RECOVERY_COMMANDS.includes(cmd)) {
    return { cmd, display, action: "stop", reason: "recovery-pending", name: `a pending command ("${display}")` };
  }
  return { cmd, display, action: "write", reason: "leftover", name: `an unrecognised leftover command ("${display}")` };
}
