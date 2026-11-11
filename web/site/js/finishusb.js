// SPDX-License-Identifier: GPL-2.0-or-later
// finishusb.js: the page's client of the finish of step 2 over the USB cable
// (mainline-rc-4; owner 2026-11-11, Q-T8 = option 5). After step 2 the unit
// runs the new mainline slot (the step-2 target). On its first boot that
// slot's USB interface (uc-pxx-xfer, the same device and frames as step 2)
// waits for the PINNED finish set: finish.json and the two files it names.
// The page sends them from the release folder's uc-pxx-finish folder. The unit
// is the authority: it takes finish.json only when its sha256 is the one of
// the slot's own list (HELLO pins[0]), then only the two files it names, with
// their sizes, and checks their sha256 itself. It runs the finish from RAM by
// the slot's reviewed finish rules. Nothing here writes the unit's storage.
//
// The frames and the session are those of step2usb.js (Step2Link), unchanged.
import { Step2Link, T } from "./step2usb.js";

export { Step2Link, T };

export const DIR = "uc-pxx-finish";
export const LIST = "finish.json";
export const KIND = "uc-pxx-finish";
export const VERSION = 1;
// The two files of the set, in this order (internal/xfer/handover FinishNames).
export const FILES = Object.freeze(["uc-pxx-step2-finish", "uc-pxx-bootctl-inst"]);
export const MAX_LIST = 4 << 10; // handover.MaxFinishList
export const MAX_FILE = 32 << 20; // handover.MaxFinishFile
const SHA = /^[0-9a-f]{64}$/;

export class FinishError extends Error {
  constructor(message) {
    super(message);
    this.name = "FinishError";
  }
}

async function sha256hex(buf) {
  const d = new Uint8Array(await crypto.subtle.digest("SHA-256", buf));
  return Array.from(d, (x) => x.toString(16).padStart(2, "0")).join("");
}

function exactKeys(o, keys) {
  return o !== null && typeof o === "object" && !Array.isArray(o) &&
    Object.keys(o).length === keys.length && keys.every((k) => Object.prototype.hasOwnProperty.call(o, k));
}

// parseList parses finish.json strictly (the rule of handover.ParseFinishList,
// with exact keys): kind, version, exactly the two files in their order, each
// with a size of 1 to MAX_FILE bytes and a lowercase sha256. Throws
// FinishError.
export function parseList(bytes) {
  if (!(bytes instanceof Uint8Array) || bytes.length === 0 || bytes.length > MAX_LIST) {
    throw new FinishError(`finish.json has ${bytes && bytes.length} bytes (1 to ${MAX_LIST})`);
  }
  let j;
  try {
    j = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes));
  } catch {
    throw new FinishError("finish.json is not JSON");
  }
  if (!exactKeys(j, ["kind", "version", "files"])) throw new FinishError("finish.json has other keys than kind, version, files");
  if (j.kind !== KIND || j.version !== VERSION) {
    throw new FinishError(`finish.json is kind ${JSON.stringify(j.kind)} version ${JSON.stringify(j.version)}, not ${KIND} version ${VERSION}`);
  }
  if (!Array.isArray(j.files) || j.files.length !== FILES.length) throw new FinishError(`finish.json does not name exactly the ${FILES.length} files`);
  const files = j.files.map((f, i) => {
    if (!exactKeys(f, ["name", "size", "sha256"]) || f.name !== FILES[i] || !Number.isSafeInteger(f.size) || f.size <= 0 ||
      f.size > MAX_FILE || typeof f.sha256 !== "string" || !SHA.test(f.sha256)) {
      throw new FinishError(`finish.json file ${i} is not ${FILES[i]} with a size of 1 to ${MAX_FILE} bytes and a sha256`);
    }
    return Object.freeze({ name: f.name, size: f.size, sha256: f.sha256 });
  });
  return Object.freeze({ kind: j.kind, version: j.version, files: Object.freeze(files) });
}

// findSets: the uc-pxx-finish folders among the picked files (files: File
// objects with webkitRelativePath, an <input webkitdirectory>). The person may
// pick the uc-pxx-finish folder itself or a folder above it. Each result has
// the folder's direct files by name and the names of anything else in it.
export function findSets(files) {
  const dirs = new Map();
  for (const f of files) {
    const parts = (f.webkitRelativePath || f.name).split("/");
    // the nearest folder named uc-pxx-finish on the path
    let k = -1;
    for (let i = parts.length - 2; i >= 0; i--) if (parts[i] === DIR) { k = i; break; }
    if (k < 0) continue;
    const dir = parts.slice(0, k + 1).join("/");
    if (!dirs.has(dir)) dirs.set(dir, { dir, files: {}, other: [] });
    const d = dirs.get(dir);
    const rest = parts.slice(k + 1).join("/");
    if (parts.length === k + 2 && (rest === LIST || FILES.includes(rest)) && !d.files[rest]) d.files[rest] = f;
    else d.other.push(rest);
  }
  return [...dirs.values()];
}

// checkSet checks a candidate on the page, before any device is opened: exactly
// the three files, finish.json parses, the two files have the list's sizes and
// sha256, and finish.json's sha256 is one of the page's lists. A cheap early
// refusal only: the unit checks everything again.
export async function checkSet(cand, lists) {
  const where = cand.dir;
  if (cand.other.length) return { ok: false, why: `${where} holds other files (${cand.other.slice(0, 3).join(", ")}): it must hold exactly ${LIST}, ${FILES.join(", ")}` };
  const missing = [LIST, ...FILES].filter((n) => !cand.files[n]);
  if (missing.length) return { ok: false, why: `${where} has no ${missing.join(", ")}` };
  const lf = cand.files[LIST];
  if (lf.size <= 0 || lf.size > MAX_LIST) return { ok: false, why: `${where}/${LIST} has ${lf.size} bytes (1 to ${MAX_LIST})` };
  const bytes = new Uint8Array(await lf.arrayBuffer());
  const sha = await sha256hex(bytes);
  let list;
  try {
    list = parseList(bytes);
  } catch (e) {
    return { ok: false, sha, why: `${where}: ${e.message}` };
  }
  for (const f of list.files) {
    const p = cand.files[f.name];
    if (p.size !== f.size) return { ok: false, sha, why: `${where}/${f.name} has ${p.size} bytes, ${LIST} says ${f.size} (the download is incomplete?)` };
  }
  for (const f of list.files) {
    const s = await sha256hex(await cand.files[f.name].arrayBuffer());
    if (s !== f.sha256) return { ok: false, sha, why: `${where}/${f.name} has sha256 ${s}, ${LIST} says ${f.sha256} (the download is damaged?)` };
  }
  if (!lists.length) {
    return { ok: false, sha, why: "this page version pins no finish set yet, so it sends none (a reviewed page version pins the release's finish.json)" };
  }
  const pin = lists.find((p) => p.sha256 === sha);
  if (!pin) return { ok: false, sha, why: `${where}/${LIST} sha256 ${sha} is not a finish set of this page` };
  return { ok: true, sha, pin, list, total: lf.size + list.files.reduce((n, f) => n + f.size, 0) };
}

// checkHello decides on the unit's HELLO, before any byte is sent. go: "send"
// (a finish-mode unit that pins exactly this list), "watch" (the unit already
// has a finish set, or shows a finish in progress: follow its status), or
// "stop" (send nothing; why says why).
export function checkHello(h, sha, lists) {
  const s2 = (h && h.info && h.info.step2) || "";
  if (!h || typeof h !== "object") return { go: "stop", why: "the unit's answer is not a HELLO" };
  if (h.mode === "payload") {
    return { go: "stop", why: "The unit waits for the step-2 payload (a STEP-2 build), not for the finish set. Nothing was sent. Use the step-2 section." };
  }
  if (h.mode === "status") {
    const busy = /^finish: (checking|started|not-yet)/.test(s2);
    return { go: busy ? "watch" : "stop",
      why: `The unit takes no finish set now (status only${s2 ? `: ${s2}` : ""}). Nothing was sent.` };
  }
  if (h.mode !== "finish") return { go: "stop", why: `The unit's interface is in mode ${JSON.stringify(h.mode)}, not finish. Nothing was sent.` };
  if (h.state === "finish-complete") return { go: "watch", why: "The unit already has the finish set." };
  if (h.state !== "waiting-finish" && h.state !== "receiving") {
    return { go: "stop", why: `The unit is not waiting for the finish set (state ${h.state}). Nothing was sent.` };
  }
  if (!Array.isArray(h.pins) || h.pins.length !== 1 || typeof h.pins[0] !== "string" || !SHA.test(h.pins[0])) {
    return { go: "stop", why: "The unit does not name exactly one finish list of its slot. Nothing was sent." };
  }
  if (!lists.some((p) => p.sha256 === sha)) return { go: "stop", why: `finish.json sha256 ${sha} is not a finish set of this page. Nothing was sent.` };
  if (h.pins[0] !== sha) {
    return { go: "stop", why: `This finish set is not the one this unit's slot names (the unit: ${h.pins[0]}, the folder: ${sha}). ` +
      "Nothing was sent. Use the release folder of the build that runs in this slot." };
  }
  return { go: "send" };
}

// sendSet sends finish.json, then the two files with the list's sizes, then
// COMMIT {auto: false} (a finish set has no automatic start). It stops at the
// first refusal. progress(name, sent, size, doneBytes, totalBytes).
// Result: { ok: true }, { ok: false, why } (nothing taken), or
// { ok: "unknown", why } (the link ended at the COMMIT: the unit may have taken
// the set; only its status says).
export async function sendSet(link, cand, list, progress, log) {
  const order = [[LIST, cand.files[LIST]], ...list.files.map((f) => [f.name, cand.files[f.name]])];
  for (const f of list.files) {
    if (cand.files[f.name].size !== f.size) return { ok: false, why: `${f.name} has ${cand.files[f.name].size} bytes, ${LIST} says ${f.size}` };
  }
  const total = order.reduce((n, [, f]) => n + f.size, 0);
  let done = 0;
  for (const [name, file] of order) {
    const r = await link.putFile(name, file, (s, size) => progress && progress(name, s, size, done + s, total));
    if (!r.accepted) return { ok: false, why: `the unit refused ${name}: ${r.reason}` };
    if (!r.ok) return { ok: false, why: `the unit did not take ${name}: ${r.reason}` };
    done += file.size;
    if (log) log(`finish: ${name} sent (${file.size} bytes), unit sha256 ${r.sha256}`);
  }
  if (log) log("finish: COMMIT (no automatic start)");
  let c;
  try {
    c = await link.commit(false);
  } catch (e) {
    return { ok: "unknown", why: `the link ended at the COMMIT (${e && e.message})` };
  }
  if (c.ok !== true) return { ok: false, why: `the unit did not take the finish set: ${c.reason}` };
  return { ok: true };
}

// finishText: what the page shows for the unit's status (HELLO or STATUS).
// Returns { text, cls, end, outcome }: end true = the watch stops.
export function finishText(st) {
  const s2 = (st && st.info && st.info.step2) || "";
  const lines = (st && st.info && Array.isArray(st.info.lines)) ? st.info.lines : [];
  const first = lines.find((l) => typeof l === "string" && l.trim() !== "") || "";
  if (/^(stop|hold)\b/.test(s2)) {
    return { text: `KEEP THE UNIT POWERED. Do not restart it and do not disconnect the power. Save the log and ask for help (${s2}).`,
      cls: "stop", end: true, outcome: "stop" };
  }
  if (s2 === "finish: done" || s2 === "finish: switched") {
    return { text: "Finished: step 2 is complete on this unit. Slot A boots first, slot B is the fallback.", cls: "ok", end: true, outcome: "finished" };
  }
  if (s2 === "finish: supervised") {
    return { text: "A supervised unit: the finish runs only by the supervised procedure. The page's part ends here.", cls: "warn", end: true, outcome: "supervised" };
  }
  if (s2 === "finish: not a target") {
    return { text: "This unit is not a step-2 target: nothing to finish.", cls: "warn", end: true, outcome: "not-target" };
  }
  if (s2 === "finish: refused") {
    return { text: `The unit refused the finish: ${first || "no reason given"}. Save the log.`, cls: "warn", end: true, outcome: "refused" };
  }
  if (s2 === "finish: later") {
    return { text: "The finish did not run yet: this slot did not mark itself. The unit checks again at its next start: " +
      "restart the unit later, then send the finish again.", cls: "warn", end: true, outcome: "later" };
  }
  if (s2 === "finish: not-yet") {
    return { text: "Waiting for the unit to mark itself (its first boot is not marked yet). Keep the cable and the power connected.", cls: "", end: false };
  }
  if (s2 === "finish: checking") return { text: "The unit checks the finish set (the check after step 2 runs). Keep the cable and the power connected.", cls: "", end: false };
  if (s2 === "finish: started") return { text: "The finish runs on the unit. Keep the cable and the power connected.", cls: "", end: false };
  const state = (st && st.state) || "";
  if (state === "finish-complete" && (s2 === "finish: waiting" || s2 === "")) {
    return { text: "The unit has the finish set: it checks it. Keep the cable and the power connected.", cls: "", end: false };
  }
  if (s2 === "finish: waiting") return { text: "The unit waits for the finish set.", cls: "", end: false };
  return { text: `The unit's status: ${s2 || "none"}${state ? ` (interface ${state})` : ""}.`, cls: "", end: false };
}

// statusRows: the fields of the read-only status view, in order. Only these
// fields of HELLO are shown (the unit never sends a serial or a MAC; the page
// shows nothing else).
export function statusRows(h) {
  const i = (h && h.info) || {};
  const s = (v) => (typeof v === "string" && v !== "" ? v : "-");
  const more = h && h.detail; // shown only, never decided on
  return [
    ["Model", s(i.model)],
    ["Slot", typeof i.slot === "string" && i.slot ? i.slot.toUpperCase() : "-"],
    ["Build token", s(i.token)],
    ["Interface mode", s(h && h.mode)],
    ["Interface state", s(h && h.state) + (typeof more === "string" && more ? ` (${more})` : "")],
    ["Step 2", s(i.step2)],
    ["Plan id", s(i.plan_id)],
  ];
}
