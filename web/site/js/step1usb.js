// SPDX-License-Identifier: GPL-2.0-or-later
// step1usb.js: the page's client of step 1 over the USB cable (I-6, DESIGN v3
// §9.6; owner 2026-11-15). The RAM installer presents the step-2 transport's
// USB identity (1d6b:0105, the vendor interface ff/50/01, the PXX1 frames of
// step2usb.js) with HELLO mode "step1" (uc-pxx-installer mainline-rc-10
// 85f2b22, internal/xfer kind step1). Its two stages:
//   firmware (state waiting-firmware, HELLO fw_pins: the firmware files the
//     installer takes, size and sha256): firmware.bin, for mainline the
//     optional SSH-KEY.PUB, then COMMIT { mode: "official" | "mainline" };
//   bundle (mainline only, state waiting-bundle, HELLO pins: the manifest
//     sha256s of the step-1 gate): the base payload of DESIGN-R23 (schema 3):
//     manifest.json first, then boot-template.img and rootfs.ext4.zst (the
//     unit unpacks it on arrival), then COMMIT {}.
// After each COMMIT the unit seals the files and its link comes back status
// only, state "busy" (info.phase says what the installer does) or "drive".
// The unit is the authority: every size and sha256 is checked there again.
// Nothing here writes the unit's storage: the install starts only on the unit
// (the one hold on its plan screen, I-5).
//
// Every protocol name of step 1 is in P, so the page follows the installer's
// "Page interface" (MAINLINE-RC10-2026-11-15.md) in one place.
import { Step2Link, T } from "./step2usb.js";

export { Step2Link };

export const P = Object.freeze({
  MODE: "step1",
  DRIVE: 7, // the frame type of the drive switch (internal/xfer TypeDrive)
  FIRMWARE_NAME: "firmware.bin",
  KEY_NAME: "SSH-KEY.PUB",
  MANIFEST: "manifest.json",
  TEMPLATE: "boot-template.img",
  ROOTFS_ZST: "rootfs.ext4.zst",
  // the receiver's states
  WAITING_FIRMWARE: "waiting-firmware",
  FIRMWARE_COMPLETE: "firmware-complete",
  WAITING_BUNDLE: "waiting-bundle",
  BUNDLE_COMPLETE: "bundle-complete",
  RECEIVING: "receiving",
  BUSY: "busy",
  DRIVE_STATE: "drive",
  // the flow's phases (info.phase): the closed set of the installer,
  // flow.PhaseWords of uc-pxx-installer mainline-rc-10 (4af28d1, 85015e6; the "Page
  // interface" of MAINLINE-RC10-2026-11-15.md §6.6), with "unlock" of
  // mainline-rc-10c (R26). Every word is mapped here; a word outside the set
  // is unknown: the page waits and logs it.
  // a child runs
  STASH: "stash", // the unit reads the firmware file (the mainline stash)
  CHECK: Object.freeze(["check", "planning"]), // the unit checks the files and makes the plan
  INSTALL: Object.freeze(["install", "progress"]), // the install writes: do not disconnect
  // the install writes the lock state (the bootloader unlock) between the
  // keeper and the slot writes (RC-10c, R26); before and after it "install".
  // An installer before RC-10c never says it.
  UNLOCK: "unlock",
  // the unit writes, not an install (a switch or a repair of the unit's menu):
  // do not disconnect
  WRITING: Object.freeze(["switch-running", "repair-running", "switching"]),
  // the files screen: the receiver's state says what the unit takes
  FILES: Object.freeze(["waiting-firmware", "waiting-bundle"]),
  // the drive mode's files screen (DRIVE_PREFIX + the drive phase)
  DRIVE_PHASES: Object.freeze(["drive: waiting-firmware", "drive: waiting-bundle", "drive: reading"]),
  // the other screens
  PLAN: "plan",
  DONE: Object.freeze(["done"]),
  QUESTION: "question", // the unit asks the person a question on its screen
  CANCELED: "canceled",
  REFUSED: "refused",
  STOPPED: Object.freeze(["stopped", "hold"]),
  // the unit is on another screen (its menu: not an error, nothing for the
  // page to do): the page waits and logs it
  OTHER: Object.freeze(["status", "log", "restart", "repair", "safe", "switch", "switch-confirm"]),
  DRIVE_PREFIX: "drive: ",
});

// PHASE_WORDS: the closed set (flow.PhaseWords), for the page and its tests.
export const PHASE_WORDS = Object.freeze([P.STASH, ...P.CHECK, ...P.INSTALL, P.UNLOCK, ...P.WRITING, ...P.FILES, ...P.DRIVE_PHASES,
  P.PLAN, ...P.DONE, P.QUESTION, P.CANCELED, P.REFUSED, ...P.STOPPED, ...P.OTHER]);

export const T1 = Object.freeze({ ...T, DRIVE: P.DRIVE });

// phaseOf: the flow's phase of a HELLO or STATUS (info.phase), "" if none.
export function phaseOf(st) {
  const i = st && st.info;
  return i && typeof i.phase === "string" ? i.phase : "";
}

// classify maps the unit's HELLO or STATUS to the page's phase: send-firmware,
// send-base, receiving, sealing (a COMMIT was taken, the unit seals), reading,
// checking, plan, question, installing, unlocking (the install unlocks the
// bootloader), writing (a switch or repair writes),
// done, canceled, refused, stop, other (the unit's menu), drive (with the
// drive phase) or unknown (the page waits and logs it). A phase word outside
// the installer's closed set is unknown, whatever the state says.
export function classify(st) {
  const state = st && typeof st.state === "string" ? st.state : "";
  const ph = phaseOf(st);
  if (ph !== "" && !PHASE_WORDS.includes(ph)) return { phase: "unknown", drive: "" };
  // drive mode: the receiver's state stays "drive" for the rest of the boot
  // while info.phase goes on (the drive phases, then stash, check, plan, …)
  if (P.DRIVE_PHASES.includes(ph)) return { phase: "drive", drive: ph.slice(P.DRIVE_PREFIX.length) };
  if (state === P.DRIVE_STATE && (ph === "" || P.FILES.includes(ph))) return { phase: "drive", drive: "" };
  if (P.STOPPED.includes(ph)) return { phase: "stop", drive: "" };
  if (ph === P.REFUSED) return { phase: "refused", drive: "" };
  if (ph === P.CANCELED) return { phase: "canceled", drive: "" };
  if (P.DONE.includes(ph)) return { phase: "done", drive: "" };
  if (P.INSTALL.includes(ph)) return { phase: "installing", drive: "" };
  if (ph === P.UNLOCK) return { phase: "unlocking", drive: "" };
  if (P.WRITING.includes(ph)) return { phase: "writing", drive: "" };
  if (ph === P.PLAN) return { phase: "plan", drive: "" };
  if (ph === P.QUESTION) return { phase: "question", drive: "" };
  if (P.OTHER.includes(ph)) return { phase: "other", drive: "" };
  if (ph === P.STASH) return { phase: "reading", drive: "" };
  if (P.CHECK.includes(ph)) return { phase: "checking", drive: "" };
  // the files screen (or no phase yet): the receiver's state decides
  if (state === P.WAITING_FIRMWARE) return { phase: "send-firmware", drive: "" };
  if (state === P.WAITING_BUNDLE) return { phase: "send-base", drive: "" };
  if (state === P.RECEIVING) return { phase: "receiving", drive: "" };
  if (state === P.FIRMWARE_COMPLETE || state === P.BUNDLE_COMPLETE) return { phase: "sealing", drive: "" };
  if (state === P.DRIVE_STATE) return { phase: "drive", drive: "" };
  return { phase: "unknown", drive: "" };
}

// checkHello decides on the unit's HELLO before any byte is sent: a step-1
// unit of the image the page started (the RAM image's token, info.token).
// "wait": the installer has not written its token yet (it comes with its
// status report, after the link is up): the page asks again, it sends nothing.
export function checkHello(h, want) {
  if (!h || typeof h !== "object") return { go: "stop", why: "the unit's answer is not a HELLO" };
  if (h.mode !== P.MODE) return { go: "stop", why: `the unit's interface is in mode ${JSON.stringify(h.mode)}, not ${P.MODE}` };
  const token = h.info && h.info.token;
  if (typeof token !== "string" || token === "") return { go: "wait", why: "the unit does not name its installer yet" };
  if (token !== want.token) {
    return { go: "stop", why: `the unit runs the installer ${JSON.stringify(token || "")}, not the one the page started (${want.token})` };
  }
  return { go: "ok" };
}

function fwPinned(h, size, sha) {
  return Array.isArray(h.fw_pins) && h.fw_pins.some((p) => p && p.size === size && p.sha256 === sha);
}

// sendFirmware: PUT firmware.bin, then (mainline, with a confirmed key) PUT
// SSH-KEY.PUB, then COMMIT { mode }. Only in the state waiting-firmware, and
// only when the unit's fw_pins name this file's size and sha256. keyFile: a
// Blob with the normalised key line + LF, or null. progress(name, sent, size).
// Result: { ok: true } or { ok: false, why } (nothing taken).
export async function sendFirmware(link, h, fw, firmwareSha, keyFile, mode, progress, log) {
  if (classify(h).phase !== "send-firmware") return { ok: false, why: `the unit is not waiting for the firmware file (state ${h.state})` };
  if (!fwPinned(h, fw.size, firmwareSha)) return { ok: false, why: `the unit does not take this firmware file (${fw.size} bytes, sha256 ${firmwareSha})` };
  if (mode !== "official" && mode !== "mainline") throw new Error(`step 1: mode ${JSON.stringify(mode)}`);
  if (keyFile && mode !== "mainline") throw new Error("step 1: a key only with the mainline goal");
  let r = await link.putFile(P.FIRMWARE_NAME, fw, (s, size) => progress && progress(P.FIRMWARE_NAME, s, size));
  if (!r.accepted) return { ok: false, why: `the unit refused the firmware file: ${r.reason}` };
  if (!r.ok) return { ok: false, why: `the unit did not take the firmware file: ${r.reason}` };
  if (log) log(`step 1: firmware.bin sent (${fw.size} bytes), unit sha256 ${r.sha256}`);
  if (keyFile) {
    r = await link.putFile(P.KEY_NAME, keyFile, null);
    if (!r.accepted) return { ok: false, why: `the unit refused the ssh key: ${r.reason}` };
    if (!r.ok) return { ok: false, why: `the unit did not take the ssh key: ${r.reason}` };
    if (log) log(`step 1: SSH-KEY.PUB sent (${keyFile.size} bytes), unit sha256 ${r.sha256}`);
  }
  if (log) log(`step 1: COMMIT mode ${mode}`);
  const c = await link.request(T.COMMIT, { mode });
  if (c.ok !== true) return { ok: false, why: `the unit did not take the files: ${c.reason}` };
  return { ok: true };
}

// sendBase: the base payload (base: the page's checked payload, see
// download.fetchBase: { sha, files: { manifest, template, rootfsZst },
// manifest }), then COMMIT {}. Only in the state waiting-bundle and only when
// the unit pins this manifest's sha256. progress(name, sent, size, done, total).
export async function sendBase(link, h, base, progress, log) {
  if (classify(h).phase !== "send-base") return { ok: false, why: `the unit is not waiting for the mainline files (state ${h.state})` };
  if (!Array.isArray(h.pins) || !h.pins.includes(base.sha)) return { ok: false, why: `the unit does not take this base (manifest sha256 ${base.sha})` };
  const m = base.manifest;
  const order = [[P.MANIFEST, base.files.manifest, null], [P.TEMPLATE, base.files.template, m.boot_template],
    [P.ROOTFS_ZST, base.files.rootfsZst, m.rootfs_zst]];
  for (const [name, f, want] of order) {
    if (want && f.size !== want.size) return { ok: false, why: `${name} has ${f.size} bytes, the manifest says ${want.size}` };
  }
  const total = order.reduce((n, [, f]) => n + f.size, 0);
  let done = 0;
  for (const [name, f] of order) {
    const r = await link.putFile(name, f, (s, size) => progress && progress(name, s, size, done + s, total));
    if (!r.accepted) return { ok: false, why: `the unit refused ${name}: ${r.reason}` };
    if (!r.ok) return { ok: false, why: `the unit did not take ${name}: ${r.reason}` };
    done += f.size;
    if (log) log(`step 1: ${name} sent (${f.size} bytes), unit sha256 ${r.sha256}${r.unpackedSha256 ? `, unpacked ${r.unpacked} bytes sha256 ${r.unpackedSha256}` : ""}`);
  }
  const c = await link.request(T.COMMIT, {});
  if (c.ok !== true) return { ok: false, why: `the unit did not take the mainline files: ${c.reason}` };
  return { ok: true };
}

// requestDrive asks the unit for the drive fallback (the frame DRIVE).
export async function requestDrive(link) {
  const r = await link.request(T1.DRIVE, {});
  return r && r.ok === true;
}
