// SPDX-License-Identifier: GPL-2.0-or-later
// enter.js: the steps of the entry, with their guards. The UI (main.js) calls
// these in order. Each guard throws a Refusal; the UI then stops.
//
// This is the --plain-entry web front end. It mirrors the reviewed CLI
// `p10-enter rockusb --plain-entry` (uc-pxx-installer p10enter-plain f631a8a,
// tool sha256 084394fb...), RULES.md "The BCB bootonce-bootloader route into
// fastboot" and the p10-enter `--plain-entry` entry:
//   route_rockusb: read the GPT and gate the layout (CLI exit 27); read the
//     factory block and gate the model (exit 28, a 0xCC-only read is "not
//     readable", not a stop); read + decode the A/B sector (the next slot a
//     keeper, the other slot a keeper or never tried); read the BCB and require
//     a known state; write "bootonce-bootloader" to the BCB (only when a write
//     is needed), read it back, confirm the A/B sector did not change during the
//     write, reset (rd 0).
//   check_plain_entry: after the reset, U-Boot's BCB clear scribbles LBA 49156
//     and libavb resets the A/B block to a valid, bootable, both-slots-trying
//     state. The page tolerates THAT benign reset (not the pre-entry A/B) and
//     then boots. Any other post-entry state (zero bootable, any slot
//     successful, unreadable getvars) keeps the unit in fastboot and asks for
//     help; it never boots. The page never writes LBA 49156.
import { checkBeforeEntry, otherSlotKind, parseAb } from "./avbab.js";
import { classifyCommand, checkedPayload, commandText, PAYLOAD_SHA256, sha256hex } from "./bcb.js";
import { parseSize } from "./fastboot.js";
import { checkLayout, gptParts } from "./partmap.js";
import { factoryModel, factoryUnserved } from "./factory.js";
import { deviceMode, VID as RK_VID, PID as RK_PID } from "./rockusb.js";
import { VID as FB_VID, PID as FB_PID } from "./fastboot.js";

// fatal: the page stops until it is loaded again. advice: what the user does.
export class Refusal extends Error {
  constructor(code, message, advice, fatal = true) {
    super(message);
    this.name = "Refusal";
    this.code = code;
    this.advice = advice;
    this.fatal = fatal;
  }
}

const SAVE = "Save the log (button below).";
const ASK = "Then ask for help, and give the log.";
const KEEP = "Keep the unit connected and powered, and do not restart it.";

export const ADVICE = Object.freeze({
  maskrom:
    "Stop. The unit is in Maskrom mode. Do nothing more with it: no other tool, no other page. " + SAVE + " " + ASK,
  refusedNothingWritten: "Nothing was written to the unit. " + SAVE + " " + ASK,
  runsSlotB:
    "Nothing was written to the unit. The installer always keeps slot A and installs over slot B, so it needs a " +
    "unit whose slot A has started successfully before. This unit runs slot B and its slot A never did. " + SAVE +
    " " + ASK,
  bcbCommandPending:
    "Nothing was written to the unit. A command is already set in its boot command block, so a recovery or update may " +
    "be in progress. This page did not change it. " + SAVE + " " + ASK,
  layout:
    "Nothing was written to the unit. The page did not recognise the unit's storage layout, so it stopped before it " +
    "wrote anything. " + SAVE + " " + ASK,
  model:
    "Nothing was written to the unit. This page is for the UC-P10, and the unit did not identify as one. " + SAVE + " " + ASK,
  writeInterrupted:
    "The unit went away while the page wrote its boot command. Do not start again. " + SAVE + " " + ASK,
  readBack: "The boot command did not read back correctly. " + KEEP + " " + SAVE + " " + ASK,
  downloadInterrupted:
    "The transfer of the installer stopped. The page wrote nothing in this step. Do not start again. " + SAVE + " " + ASK,
  resetInterrupted:
    "The boot command is written and read back, but the restart command did not reach the unit. " + SAVE + " " + ASK,
  readInterrupted: "The unit went away during a read. Nothing was written to the unit. " + SAVE + " " + ASK,
  entryNotBenign:
    "After the restart the unit's A/B state in fastboot is not the normal reset this page expects. The page stopped " +
    "before the installer and wrote nothing to the unit's slots. " + KEEP + " " + SAVE + " " + ASK,
  fastbootStop:
    "The page stopped in fastboot, before the installer. Earlier it wrote only the boot command, which U-Boot used. " +
    KEEP + " " + SAVE + " " + ASK,
  noPreEntry:
    "The unit is already in fastboot, so the page cannot read its storage first. Power the unit off and on, start it " +
    "in download mode, and connect again.",
  afterWrite: "The boot command was written and read back before this error. " + KEEP + " " + SAVE + " " + ASK,
});

// The getvars this page reads after the entry. SLOT_VARS are required and are
// what check_plain_entry validates; product is informational (never a stop, as
// in the CLI); max-download-size is needed to send the image. serialno is NOT
// read (the page logs no unit serial; identity across the reset is the USB
// serial-string match, sameUnit()).
export const SLOT_VARS = Object.freeze(["current-slot", "slot-successful:a", "slot-successful:b",
  "slot-unbootable:a", "slot-unbootable:b", "slot-retry-count:a", "slot-retry-count:b"]);
export const ENTRY_GETVARS = Object.freeze([...SLOT_VARS, "product", "max-download-size"]);
export const MAX_TRIES = 7;

// The browser brands this page accepts. Chrome, Edge and Chromium share one
// WebUSB engine, so Chromium itself is accepted (patched for R4). A Chromium
// fork that ships its own vendor brand next to "Chromium" (Brave, Opera, …) is
// still refused: its WebUSB and device-permission behaviour is not tested. The
// rule is "every real brand is one of these three", not "one of these three is
// present" — so "Brave" + "Chromium" is refused while a plain Chromium passes.
const ACCEPTED_BRANDS = Object.freeze(["Google Chrome", "Microsoft Edge", "Chromium"]);

// isGreaseBrand matches the GREASE placeholder brand the user-agent adds to the
// brand list ("Not.A/Brand", "Not;A=Brand", "Not)A;Brand", …): it always holds
// "Not" and "Brand". It is meant to be ignored, so it never counts as a real
// brand. No shipping browser name holds both words, so this cannot hide one.
function isGreaseBrand(brand) {
  return /not/i.test(brand) && /brand/i.test(brand);
}

// checkBrowser: Chrome, Edge or Chromium on a desktop, secure context, WebUSB.
export function checkBrowser(nav, secure) {
  const msg = "This page works only in Google Chrome, Microsoft Edge or Chromium on a desktop computer (Windows, macOS or Linux).";
  if (!secure) return { ok: false, message: "Open this page over https. " + msg };
  const uad = nav.userAgentData;
  const brands = uad && Array.isArray(uad.brands) ? uad.brands.map((b) => b.brand) : [];
  const real = brands.filter((b) => !isGreaseBrand(b));
  const chrome = real.includes("Google Chrome");
  const edge = real.includes("Microsoft Edge");
  // Accept only when every real brand is one this page tests; a fork's own
  // brand (Brave, Opera, …) next to "Chromium" is refused.
  const accepted = real.length > 0 && real.every((b) => ACCEPTED_BRANDS.includes(b));
  if (!accepted) return { ok: false, message: msg };
  if (uad.mobile) return { ok: false, message: msg };
  // ChromeOS (and any other or empty platform) is refused: its WebUSB rules are not tested.
  if (!["Windows", "macOS", "Linux"].includes(uad.platform)) return { ok: false, message: msg };
  if (!nav.usb) return { ok: false, message: "WebUSB is off in this browser (a policy may block it). " + msg };
  const name = edge ? "Microsoft Edge" : chrome ? "Google Chrome" : "Chromium";
  if (!nav.serial) return { ok: true, serial: false, message: `${name}: WebUSB OK. Web Serial is off, so the shell is not available.` };
  return { ok: true, serial: true, message: `${name}: WebUSB and Web Serial OK.` };
}

// checkImage: the sha256 of the file must be one of the pinned images, and the
// pin must be for this page's model.
export async function checkImage(bytes, images, model) {
  if (!(bytes instanceof Uint8Array) || bytes.length === 0) {
    throw new Refusal("image-empty", "The file is empty.", "Choose the installer image file.", false);
  }
  const sha = await sha256hex(bytes);
  const pin = images.find((i) => i.sha256 === sha);
  if (!pin) {
    throw new Refusal("image-sha", `This file is not a known installer image (sha256 ${sha}).`,
      "Choose the installer image file from the release. Do not use a changed file.", false);
  }
  if (model && pin.model !== model) {
    throw new Refusal("image-model", `This image is for ${pin.model}, not ${model}.`,
      "Choose the installer image for this unit's model.", false);
  }
  const magic = new TextDecoder().decode(bytes.subarray(0, 8));
  if (magic !== "ANDROID!" || (typeof pin.size === "number" && pin.size !== bytes.length)) {
    throw new Refusal("image-format", "The file has a known sha256 but not the expected size or header.",
      ADVICE.refusedNothingWritten, false);
  }
  return { sha256: sha, pin };
}

// classifyDevice: the device the user picked, before it is opened.
export function classifyDevice(dev) {
  if (dev.vendorId === RK_VID && dev.productId === RK_PID) {
    const m = deviceMode(dev);
    if (m.mode === "maskrom") throw new Refusal("maskrom", `Maskrom mode (${m.why}).`, ADVICE.maskrom);
    if (m.mode !== "loader") {
      throw new Refusal("not-loader", `A Rockchip device, but not the download mode of this unit (${m.why}).`,
        ADVICE.refusedNothingWritten);
    }
    return "rockusb";
  }
  if (dev.vendorId === FB_VID && dev.productId === FB_PID) return "fastboot";
  throw new Refusal("other-device", "This is not a UC-P10 in download mode or fastboot.", "Choose the unit in the list.", false);
}

// onlyOne: exactly one unit may be connected (of the devices this page may see).
export function onlyOne(devices) {
  const n = devices.filter((d) => (d.vendorId === RK_VID && d.productId === RK_PID) ||
    (d.vendorId === FB_VID && d.productId === FB_PID)).length;
  if (n > 1) {
    throw new Refusal("two-devices", `${n} units are connected.`,
      "Connect only one unit. Unplug the others, then load this page again.", false);
  }
}

function hex(bytes) {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

// unitTag and sameUnit: the USB serial string of the download-mode device and
// of the fastboot device. Compared only when both have one; never logged. This
// is the page's identity check across the reset (it logs no unit serial), in
// place of the CLI's serialno == --expect-serialno.
export function unitTag(dev) {
  return typeof dev.serialNumber === "string" ? dev.serialNumber : "";
}

export function sameUnit(tagBefore, fbDev) {
  const now = unitTag(fbDev);
  if (!tagBefore || !now) return "not compared (no USB serial string on one of the two devices)";
  if (tagBefore !== now) {
    throw new Refusal("other-device", "The fastboot device has another USB serial string than the unit in download mode.",
      ADVICE.fastbootStop);
  }
  return "the same USB serial string as in download mode";
}

// readGate: the layout and the model gate (mirror read_layout_model). The GPT
// names where misc and cfg are; the factory block names the model. A factory
// read of only 0xCC is "not readable in the download mode" (owner 2026-11-07),
// logged, not a stop: the model is then gated only by the pinned image and the
// RAM image's own model gate.
export async function readGate(rk, log, model) {
  const gpt = await rk.readGpt();
  let parts;
  try {
    parts = gptParts(gpt);
  } catch (e) {
    throw new Refusal("layout", `The primary GPT is not valid: ${e.message}.`, ADVICE.layout);
  }
  const layoutErr = checkLayout(parts);
  if (layoutErr) throw new Refusal("layout", `The storage layout is not the expected one: ${layoutErr}.`, ADVICE.layout);
  log(`GPT: ${Object.keys(parts).length} partitions; misc and cfg where the page expects them`);

  const fac = await rk.readFactory();
  if (factoryUnserved(fac)) {
    log("factory data: not readable in the download mode (all 0xCC); the model is gated by the pinned image and the " +
      "RAM image's own model gate");
    return;
  }
  const fm = factoryModel(fac);
  if (fm.error) throw new Refusal("model", `The unit's factory data is not usable: ${fm.error}.`, ADVICE.model);
  log(`factory data: model ${fm.base} (hardware ${fm.hwrev || "?"})`); // only the model is shown; never the MAC
  if (fm.base !== model) {
    throw new Refusal("model", `The unit is a ${fm.base}; this page is for ${model}.`, ADVICE.model);
  }
}

// readState: the read-only state of the A/B sector and the BCB (mirror the A/B
// and BCB parts of route_rockusb). expectAb: sha256 values of the first 32
// bytes of the A/B sector that this session is bound to (may be empty).
export async function readState(rk, log, expectAb = []) {
  const ab = await rk.readAbSector();
  const bcb = await rk.readBcb();
  log(`A/B sector first 32 bytes: ${hex(ab.subarray(0, 32))}`);
  log(`BCB command field: "${commandText(bcb)}"`);
  const abc = checkBeforeEntry(ab);
  if (abc.text) log(`A/B: ${abc.text}`);
  if (!abc.ok) throw new Refusal("ab-refused", abc.reason, ADVICE.refusedNothingWritten);
  // The next slot must be a keeper; the other slot a keeper or never tried (the
  // factory-stock state). Mirror other_slot_kind (CLI exit 23).
  const parsed = parseAb(ab);
  const next = "ab".indexOf(abc.next);
  const kind = otherSlotKind(parsed.slots, next);
  if (!kind) {
    throw new Refusal("ab-not-bootable-pair",
      "The slot that boots next must be successful, and the other slot successful too or never tried. This unit's A/B " +
      "state is not one this page accepts.", ADVICE.refusedNothingWritten);
  }
  log(kind === "successful"
    ? `both slots successful; slot ${abc.next} boots next`
    : `slot ${abc.next} (successful) boots next; the other slot was never tried and stays the fallback`);
  // The keeper after the entry is ALWAYS slot A: the BCB-route entry makes
  // U-Boot reset the A/B block to libavb's defaults (A priority 15, B 14, both
  // trying), and the installer's keeper re-establish R(K) then keeps the
  // higher-priority slot, A, and installs over B. So slot A must be a PROVEN
  // slot before the entry: it boots next (and is successful), or it is marked
  // successful while B runs (the state after an A/B update into B). A
  // successful slot booted before and is intact (an updater marks a slot
  // unbootable before it rewrites it), and every known Crestron firmware has
  // the same anti-rollback values (index 0, key versions 42/42), so A can
  // still boot. A unit that runs B while A was never successful would keep an
  // unproven A and lose its running B: refuse it here, before any write.
  if (abc.next !== "a" && kind !== "successful") {
    throw new Refusal("ab-runs-slot-b",
      `This unit runs slot ${abc.next}, and slot A was never started successfully. The installer keeps slot A and ` +
      "installs over slot B, so here it would keep an untested slot and replace the slot the unit runs now.",
      ADVICE.runsSlotB);
  }
  if (abc.next !== "a") {
    log("this unit runs slot b; slot a is successful too: the installer keeps slot a (proven) and installs over slot b");
  }
  const ab32 = await sha256hex(ab.subarray(0, 32));
  log(`A/B first 32 bytes sha256 ${ab32}`);
  for (const want of expectAb) {
    if (ab32 !== want) {
      throw new Refusal("ab-not-expected", `The A/B sector is not the one this session is bound to (${want}).`,
        ADVICE.refusedNothingWritten);
    }
  }
  // The BCB gate is the command field alone (classifyCommand), not a whole-block
  // sha. U-Boot acts (exact strcmp) only on "bootonce-bootloader",
  // "boot-recovery" and "boot_recovery"; every other field is ignored. So:
  //   empty                 -> write the entry command,
  //   "bootonce-bootloader" -> already set, skip the write,
  //   boot(-|_)recovery     -> STOP (a recovery/OTA is pending; do not clobber),
  //   any other leftover    -> write (U-Boot ignores it; writeBootCommand logs
  //                            the overwrite).
  // The command field may hold U-Boot RAM (which can contain the MAC): only its
  // printable-escaped form (cmd.display) is logged, never its raw bytes.
  const bcbSha = await sha256hex(bcb);
  const cmd = classifyCommand(bcb);
  if (cmd.action === "stop") {
    log(`BCB holds a command U-Boot acts on ("${cmd.display}", sha256 ${bcbSha}); nothing written`);
    throw new Refusal("bcb-command-pending",
      `A command is already set in the unit's boot command block ("${cmd.display}").`, ADVICE.bcbCommandPending);
  }
  log(`BCB state: ${cmd.name}`);
  return { ab, abSha: await sha256hex(ab), ab32, slots: parsed.slots, bcb, bcbSha, bcbCommand: cmd,
    needsWrite: cmd.action === "write", next: abc.next, otherKind: kind, writeStarted: false };
}

// writeBootCommand: read again and compare with what the user saw, write the
// reviewed payload (only if needed), read it back, and confirm the A/B sector
// did NOT change during the BCB write (the write is to LBA 49184, never 49156).
// The A/B reset happens LATER, when U-Boot clears the BCB on the restart; that
// is validated by the benign-reset check, not here.
export async function writeBootCommand(rk, before, log) {
  const ab1 = await rk.readAbSector();
  const bcb1 = await rk.readBcb();
  if ((await sha256hex(ab1)) !== before.abSha || (await sha256hex(bcb1)) !== before.bcbSha) {
    throw new Refusal("state-changed", "The A/B sector or the BCB changed since the first read.", ADVICE.refusedNothingWritten);
  }
  // From here on a lost unit means the write may have happened.
  try {
    if (before.needsWrite) {
      if (before.bcbCommand.reason === "leftover") {
        log(`overwriting an unrecognised leftover BCB command field: "${before.bcbCommand.display}"`);
      }
      const payload = await checkedPayload();
      before.writeStarted = true;
      await rk.writeBcb(payload);
    } else {
      log("BCB already holds the boot command: no write");
    }
    const back = await rk.readBcb();
    if ((await sha256hex(back)) !== PAYLOAD_SHA256) {
      log(`BCB read back (sha256 ${await sha256hex(back)}; command field "${commandText(back)}")`);
      throw new Refusal("readback-mismatch", "The BCB read back does not match the boot command.", ADVICE.readBack);
    }
    log("BCB read back OK");
    const ab2 = await rk.readAbSector();
    if ((await sha256hex(ab2)) !== before.abSha) {
      throw new Refusal("ab-changed", "The A/B sector changed during the BCB write.", ADVICE.readBack);
    }
  } catch (e) {
    if (e && e.name === "TransferError") throw new Refusal("write-interrupted", e.message, ADVICE.writeInterrupted);
    throw e;
  }
  log("A/B sector unchanged by the BCB write");
}

// restart: "rkdeveloptool rd" (subcode 0). U-Boot then acts on the BCB, enters
// fastboot, and clears the BCB (which resets the A/B block to both-trying).
export async function restart(rk) {
  try {
    await rk.resetNormal();
  } catch (e) {
    if (e && e.name === "TransferError") throw new Refusal("reset-interrupted", e.message, ADVICE.resetInterrupted);
    throw e;
  }
}

// benignResetReasons validates the fastboot getvars after a --plain-entry
// restart. A faithful port of check_plain_entry (host/p10-enter p10enter-plain
// f631a8a): current-slot is a real slot; that slot is bootable (slot-unbootable
// = no) -> at least one bootable slot; NEITHER slot successful (the both-trying
// reset, no stale keeper); both retry counts in 0..MAX_TRIES and the current
// slot took a try (retry < MAX_TRIES) or exactly the libavb reset shape. It
// never accepts zero bootable slots or any successful slot. Returns
// { cur, why } with why = the reasons it is NOT the benign reset (empty = ok).
export function benignResetReasons(v) {
  const norm = (name, x) => {
    let s = String(x == null ? "" : x).trim().toLowerCase();
    if (name === "current-slot") s = s.replace(/^_+/, "");
    return s;
  };
  const why = [];
  const cur = norm("current-slot", v["current-slot"]);
  if (cur !== "a" && cur !== "b") {
    why.push(`current-slot "${v["current-slot"]}" is not a real slot`);
  } else if (norm("x", v["slot-unbootable:" + cur]) !== "no") {
    why.push(`the current slot ${cur} is unbootable: no bootable slot`);
  }
  const sa = norm("x", v["slot-successful:a"]);
  const sb = norm("x", v["slot-successful:b"]);
  if (sa !== "no" || sb !== "no") why.push(`a slot is still successful (a=${sa} b=${sb}): not the both-trying reset`);
  const ra = norm("x", v["slot-retry-count:a"]);
  const rb = norm("x", v["slot-retry-count:b"]);
  const ia = /^\d+$/.test(ra) ? parseInt(ra, 10) : null;
  const ib = /^\d+$/.test(rb) ? parseInt(rb, 10) : null;
  if (ia === null || ib === null || ia < 0 || ia > MAX_TRIES || ib < 0 || ib > MAX_TRIES) {
    why.push(`a slot-retry-count is not in 0..${MAX_TRIES} (a="${v["slot-retry-count:a"]}" b="${v["slot-retry-count:b"]}")`);
  } else if (cur === "a" || cur === "b") {
    const curR = cur === "a" ? ia : ib;
    const otherR = cur === "a" ? ib : ia;
    const resetShape = curR === MAX_TRIES - 1 && otherR === MAX_TRIES;
    if (!(curR < MAX_TRIES || resetShape)) {
      why.push(`the current slot ${cur} did not take a try (retry ${curR} of max ${MAX_TRIES}): not the both-trying reset`);
    }
  }
  return { cur, why };
}

// fastbootState: the --plain-entry post-entry check. There must be a pre-entry
// read (the entry came through this page's BCB write), every slot getvar must
// answer, the A/B state must be the benign both-trying reset, and
// max-download-size must be readable. product is logged (and a mismatch is
// warned), never a stop, as in the CLI. (The name is kept for main.js; the
// logic is check_plain_entry, not the old preserve-A/B check.)
export async function fastbootState(fb, before, expectProduct, log) {
  if (!before) throw new Refusal("no-pre-entry", "The unit was not read in download mode first.", ADVICE.noPreEntry);
  const v = {};
  for (const name of ENTRY_GETVARS) v[name] = await fb.getvar(name);
  const missing = SLOT_VARS.filter((n) => v[n] === null || v[n] === "");
  if (missing.length) {
    throw new Refusal("getvar-unreadable", `fastboot gave no value for: ${missing.join(", ")}.`, ADVICE.entryNotBenign);
  }
  const { cur, why } = benignResetReasons(v);
  if (why.length) {
    throw new Refusal("entry-not-benign",
      `The A/B state after the restart is not the benign both-trying reset: ${why.join("; ")}.`, ADVICE.entryNotBenign);
  }
  // product: informational only (as in check_plain_entry). Log it; warn on a
  // mismatch; never stop.
  if (v.product == null || v.product === "") {
    log("fastboot product: not reported (not required)");
  } else if (expectProduct !== null && v.product !== expectProduct) {
    log(`fastboot product "${v.product}" is not the expected "${expectProduct}" (logged, not a stop)`);
  } else {
    log(`fastboot product ${v.product}`);
  }
  const max = parseSize(v["max-download-size"]);
  if (!(max > 0)) {
    throw new Refusal("max-download", `max-download-size not readable (${v["max-download-size"]}).`, ADVICE.fastbootStop);
  }
  log(`fastboot: benign both-trying reset (current slot ${cur} bootable, neither slot successful, retry a=` +
    `${v["slot-retry-count:a"]} b=${v["slot-retry-count:b"]}); the page did not write the A/B sector. ` +
    `max-download-size ${max}`);
  return { vars: v, maxDownload: max, slot: cur };
}

// bootInstaller: check the sha of the bytes in memory again, then download and boot.
export async function bootInstaller(fb, image, maxDownload, onProgress) {
  if (image.bytes.length > maxDownload) {
    throw new Refusal("max-download", `The image (${image.bytes.length} bytes) is larger than max-download-size (${maxDownload}).`,
      ADVICE.fastbootStop);
  }
  if ((await sha256hex(image.bytes)) !== image.sha256) {
    throw new Refusal("image-sha", "The image in memory changed.", ADVICE.fastbootStop);
  }
  try {
    await fb.bootImage(image.bytes, image.sha256, maxDownload, onProgress);
  } catch (e) {
    if (e && e.name === "TransferError") throw new Refusal("download-interrupted", e.message, ADVICE.downloadInterrupted);
    if (e && e.name === "FastbootFail") throw new Refusal("fastboot-fail", e.message, ADVICE.downloadInterrupted);
    throw e;
  }
}
