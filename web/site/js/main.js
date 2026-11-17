// SPDX-License-Identifier: GPL-2.0-or-later
// main.js: the page (DESIGN v3). It runs the steps in order, one action at a
// time: Prepare, Files, Connect (download mode, read only), Start (the boot
// command, fastboot, the installer, then the step-1 files over the USB cable),
// Install (the unit's own plan screen), and for mainline Step 2 and Finish.
//
// Every guard of the entry is in enter.js and unchanged: the browser check,
// the image pin (per goal), onlyOne, classifyDevice and the Maskrom stop, the
// GPT/layout and model gate, the A/B rule (slot A must be successful when B
// runs), the expect-ab binding, the BCB command-field guard and its read-back,
// sameUnit, the exact benign reset, max-download-size and the image re-hash.
// Their words now go to the log; the screen shows one plain line (owner R4).
import {
  DEVICE_USE_APPROVED, EXPECT_AB_FIRST32_SHA256, EXPECT_FASTBOOT_PRODUCT, FINISH, FINISH_USB_ENABLED, FIRMWARE, IMAGES,
  MAINLINE_INSTALL_APPROVED, MODEL, PAGE_VERSION, BASE, PROVISION, RELEASES_URL, STEP1, STEP2, STEP2_USB, STEP2_USB_ENABLED, SYSTEMS, UNLOCK,
  UNLOCK_GUIDE_ENABLED,
} from "./config.js";
import {
  ADVICE, bootInstaller, checkBrowser, checkImage, classifyDevice, fastbootState, onlyOne, readGate, readState, Refusal,
  restart, sameUnit, unitTag, writeBootCommand,
} from "./enter.js";
import { Fastboot, PID as FB_PID, VID as FB_VID } from "./fastboot.js";
import { RockUsb, PID as RK_PID, VID as RK_VID } from "./rockusb.js";
import { Shell } from "./serial.js";
import * as drivecopy from "./drivecopy.js";
import * as sshkey from "./sshkey.js";
import * as unlock from "./unlock.js";
import * as finishusb from "./finishusb.js";
import * as step1usb from "./step1usb.js";
import * as provisionusb from "./provisionusb.js";
import * as download from "./download.js";
import { sha256OfFile } from "./sha256stream.js";
import { Bar } from "./progress.js";
import { finishView, step2View, stepsFor } from "./steps.js";
import { ejectLine, stopView, TEXT } from "./texts.js";

const $ = (id) => document.getElementById(id);
const FILTERS = [{ vendorId: RK_VID, productId: RK_PID }, { vendorId: FB_VID, productId: FB_PID }];
const XFER_FILTER = [{ vendorId: STEP2_USB.vendorId, productId: STEP2_USB.productId }];
// When the unit's link is not seen by itself after this long, the Step 2 and
// Finish cards offer the Connect button (a timer, not a text: no time shown).
const CONNECT_HINT_MS = 120000;
// the Connect line for the installer's link: only when the page did not see
// the link by itself for this long (the RAM installer boots first)
const STEP1_HINT_MS = STEP1.CONNECT_HINT_S * 1000;

// phase (the entry, as before): start → image-ok → download-checked →
// restarted → fastboot-checked → booted. ui: the open step.
const S = {
  phase: "start", ui: "prepare", goal: null, image: null, rk: null, before: null, fb: null, fbState: null,
  busy: false, stopped: false, shell: null, browser: null, lines: [], rkTag: "", expectAb: [], paramError: null,
  platform: "", fbReady: false, unlockShown: false,
  imageState: "none", fw: { state: "none", file: null }, base: { state: "none", data: null }, system: null,
  key: { text: "", check: null, seq: 0 },
};

function show(id, on = true) {
  $(id).hidden = !on;
}

function log(msg) {
  const t = new Date().toISOString().slice(11, 23);
  const line = `[${t}] ${msg}`;
  S.lines.push(line);
  const el = $("log");
  el.textContent += line + "\n";
  el.scrollTop = el.scrollHeight;
}

function setText(id, text, cls) {
  const el = $(id);
  el.textContent = text;
  el.className = cls || "";
  el.hidden = text === "";
}

// bar(slot, label): the slot's bar, made once per label, reset and shown.
const bars = new Map();
function bar(slot, label) {
  let b = bars.get(slot);
  if (!b || b.label !== label) {
    b = new Bar(document, label);
    $(slot).textContent = "";
    $(slot).append(b.el);
    bars.set(slot, b);
  }
  b.clear();
  $(slot).hidden = false;
  return b;
}
function hideBar(slot) {
  $(slot).hidden = true;
}

// --- the A/B binding (expect-ab): an extra refusal only ----------------------
function bindings() {
  const out = [];
  if (EXPECT_AB_FIRST32_SHA256 !== null) out.push(EXPECT_AB_FIRST32_SHA256);
  const q = new URLSearchParams(location.search).get("expect-ab");
  if (q !== null) {
    if (!/^[0-9a-f]{64}$/.test(q)) {
      S.paramError = "The URL parameter expect-ab must be 64 lowercase hex digits. The device buttons are off.";
    } else out.push(q);
  }
  return out;
}

// --- render -------------------------------------------------------------------
const CARDS = Object.freeze(["prepare", "files", "connect", "start", "install", "system", "finish", "done"]);

function render() {
  const live = !S.busy && !S.stopped;
  const dev = DEVICE_USE_APPROVED && S.browser && S.browser.ok && !S.paramError;
  const steps = stepsFor(S.goal);
  const order = steps.map(([id]) => id);
  const at = order.indexOf(S.ui);
  // the pills
  const ol = $("steps");
  ol.textContent = "";
  steps.forEach(([, name], i) => {
    const li = document.createElement("li");
    li.textContent = `${i + 1} ${name}`;
    if (i < at) li.className = "done";
    if (i === at) {
      li.className = "now";
      li.setAttribute("aria-current", "step");
    }
    ol.append(li);
  });
  // the cards: done ones fold, the open one shows its body, later ones only the title
  for (const id of CARDS) {
    const c = $(`card-${id}`);
    const i = order.indexOf(id);
    c.hidden = i < 0;
    if (i < 0) continue;
    c.querySelector(".num").textContent = `${i + 1}. `;
    c.classList.toggle("folded", i < at);
    c.classList.toggle("later", i > at);
    c.querySelector(".tick").hidden = !(i < at);
    if (i === at) {
      const body = c.querySelector(".body");
      if ($("busy").parentNode !== body) body.prepend($("busy"));
      if ($("notice").parentNode !== body) body.append($("notice"));
      if ($("foot").parentNode !== c) c.append($("foot"));
    }
  }
  // Prepare
  const pre = S.ui === "prepare" && live && dev;
  $("goal-userdebug").disabled = !pre;
  $("goal-mainline").disabled = !(pre && MAINLINE_INSTALL_APPROVED);
  $("goal-userdebug").classList.toggle("chosen", S.goal === "userdebug");
  $("goal-mainline").classList.toggle("chosen", S.goal === "mainline");
  for (const x of SYSTEMS) {
    $(`system-${x.id}`).disabled = !pre;
    $(`system-${x.id}`).classList.toggle("chosen", S.system === x.id);
  }
  show("system-console-text", S.system === "console");
  show("check-zadig", S.platform === "Windows");
  // the boxes of this goal (owner R24: the Crestron web page items only for
  // userdebug), the Windows box only on Windows
  const reqs = [...document.querySelectorAll("#card-prepare input.req")].filter((x) => !x.closest("li").hidden &&
    !(x.closest(".only-userdebug") && S.goal !== "userdebug") && !(x.closest(".only-mainline") && S.goal !== "mainline"));
  $("prepare-next").disabled = !(pre && S.goal && (S.goal !== "mainline" || S.system) && reqs.every((x) => x.checked));
  // Files
  const files = S.ui === "files" && live;
  $("fw-choose").disabled = !files || S.fw.state === "checking";
  $("base-choose").disabled = !files || S.base.state === "loading";
  $("base-again").disabled = !files || S.base.state === "loading";
  $("image-choose").disabled = !files || S.imageState === "loading";
  $("image-again").disabled = !files || S.imageState === "loading";
  const ks = keyState();
  renderKey(ks);
  $("files-next").disabled = !(files && filesNextOk(ks));
  // Connect
  $("connect").disabled = !(live && dev && S.ui === "connect" && S.image && S.phase === "image-ok" && filesReady(ks));
  show("unit-status", S.before !== null);
  show("connect-next-row", S.ui === "connect" && S.phase === "download-checked");
  $("connect-next").disabled = !live;
  // Start
  $("start").disabled = !(live && dev && S.ui === "start" && S.phase === "download-checked");
  show("start-row", S.phase === "download-checked" || S.phase === "image-ok");
  $("connect-fastboot").disabled = !(live && dev && S.phase === "restarted");
  $("connect-step1").disabled = !(S1.phase === "wait" && !S.stopped);
  $("use-drive-wait").disabled = !(S1.phase === "wait" && !S1.link && !S1.fwSent && !S.stopped);
  // Details
  $("serial-open").disabled = !(S.browser && S.browser.serial) || S.busy || !!S.shell;
  show("term-form", !!S.shell);
  $("xstatus-read").disabled = !(S.browser && S.browser.ok) || XS.busy || xferInUse("status");
  show("xstatus-finish", finishOffer());
  show("busy", S.busy || S1.sending || SY.sending || F.sending);
  show("unlock-section", UNLOCK_GUIDE_ENABLED && S.goal === "userdebug" && S.ui === "done");
  const b = document.body.dataset;
  b.phase = S.phase;
  b.ui = S.ui;
  b.goal = S.goal || "none";
  b.busy = String(S.busy);
  b.stopped = String(S.stopped);
  b.fastbootReady = S.phase === "restarted" && S.fbReady ? "yes" : "no";
  b.image = S.imageState;
  b.fw = S.fw.state;
  b.base = S.base.state;
  b.system = S.system || "none";
  b.sshkey = ks;
  b.step1 = S1.phase;
  b.sys = SY.phase;
  b.finish = F.phase;
  b.finishEnd = F.outcome || "none";
}

// filesNextOk: Next on the Files step: the person's part is done (the
// firmware file, the key); the downloads may still run ("You can go on while
// it downloads"). The unit is read and written only when filesReady.
function filesNextOk(ks) {
  const going = (x) => x === "ok" || x === "loading";
  if (!going(S.imageState) || S.fw.state !== "ok") return false;
  if (S.goal === "mainline") {
    if (!going(S.base.state) || !systemOf()) return false;
    return systemOf().keyRequired ? ks === "ready" : ks === "none" || ks === "ready";
  }
  return S.goal === "userdebug";
}

function filesReady(ks) {
  if (S.imageState !== "ok" || S.fw.state !== "ok") return false;
  if (S.goal === "mainline") {
    if (S.base.state !== "ok" || !systemOf()) return false;
    return systemOf().keyRequired ? ks === "ready" : ks === "none" || ks === "ready";
  }
  return S.goal === "userdebug";
}

// --- stop and notices ----------------------------------------------------------
function stop(e) {
  let r = e;
  if (!(e instanceof Refusal)) {
    if (e && e.name === "TransferError") r = new Refusal("read-interrupted", e.message, ADVICE.readInterrupted);
    else {
      const written = S.before && S.before.writeStarted;
      const advice = !written ? ADVICE.refusedNothingWritten : S.phase === "download-checked" ? ADVICE.writeInterrupted : ADVICE.afterWrite;
      r = new Refusal(written ? "unexpected-after-write" : "unexpected", `Unexpected error: ${e && e.name}: ${e && e.message}`, advice);
    }
  }
  log(`STOP ${r.code}: ${r.message} ${r.advice || ""}`);
  if (r.fatal) {
    stopFastbootWatch();
    stopLoops();
    S.stopped = true;
    document.body.dataset.refusal = r.code;
    const written = !!(S.before && S.before.writeStarted) || ["restarted", "fastboot-checked", "booted"].includes(S.phase);
    const v = stopView(r.code, written);
    document.body.dataset.stopKind = v.kind;
    $("stop-title").textContent = v.title;
    setText("stop-why", v.why);
    show("stop");
  } else {
    document.body.dataset.notice = r.code;
    setText("notice", noticeText(r), "warn");
  }
}

function noticeText(r) {
  if (r.code === "two-devices") return TEXT.twoUnits;
  if (r.code === "open-failed") {
    if (S.platform === "Windows") {
      for (const id of ["zadig-rk", "zadig-fb"]) $(id).open = true; // the box opens by itself
      return `${TEXT.openFailed} ${TEXT.openWin}`;
    }
    return `${TEXT.openFailed} ${S.platform === "Linux" ? TEXT.openLinux : TEXT.openOther}`;
  }
  if (r.code === "other-device" || r.code === "not-fastboot") return TEXT.notThisMode;
  return `${r.message} ${r.advice || ""}`.trim();
}

// step runs one action. There is never a second one at the same time, and
// nothing here cancels a transfer.
async function step(fn) {
  if (S.busy || S.stopped) return;
  S.busy = true;
  setText("notice", "");
  delete document.body.dataset.notice;
  render();
  try {
    await fn();
  } catch (e) {
    stop(e);
  } finally {
    S.busy = false;
    render();
  }
}

// need: the same conditions as the buttons, checked again in each handler.
function need(phases) {
  if (!DEVICE_USE_APPROVED) {
    throw new Refusal("not-approved", "Device use is not approved for this page version.", "Do not use this page on a unit.", false);
  }
  if (!S.browser || !S.browser.ok) throw new Refusal("browser", "This browser is not supported.", S.browser ? S.browser.message : "", false);
  if (S.paramError) throw new Refusal("bad-parameter", S.paramError, "Fix the page address.", false);
  if (!phases.includes(S.phase) || !S.image) throw new Refusal("wrong-step", `This step is not possible now (${S.phase}).`, "", false);
  if (!filesReady(keyState())) throw new Refusal("wrong-step", "The files are not ready.", "", false);
}

async function openOr(promise) {
  try {
    return await promise;
  } catch (e) {
    if (e instanceof Refusal) throw e;
    throw new Refusal("open-failed", `The page cannot open the unit (${e && e.name}: ${e && e.message}).`, "", false);
  }
}

// --- Prepare --------------------------------------------------------------------
// imagesFor: the pins of this model that name the goal (the page boots only those)
function imagesFor(goal) {
  return IMAGES.filter((i) => Array.isArray(i.goals) && i.goals.includes(goal) && i.model === MODEL.base);
}

function chooseGoal(goal) {
  if (S.ui !== "prepare" || S.busy || S.stopped || !DEVICE_USE_APPROVED || !(S.browser && S.browser.ok) || S.paramError) return;
  if (goal === "mainline" && !MAINLINE_INSTALL_APPROVED) return;
  if (S.goal === goal) return;
  S.goal = goal;
  if (goal !== "mainline") S.system = null;
  log(`goal: ${goal === "mainline" ? "Mainline Linux" : "Android userdebug"}`);
  // an image checked for the other goal is used only when it is pinned for this one too
  if (S.image && !imagesFor(goal).some((p) => p.sha256 === S.image.sha256)) {
    S.image = null;
    S.phase = "start";
    S.imageState = "none";
  }
  render();
  if (S.imageState === "none") fetchImage();
  if (goal === "mainline" && S.base.state === "none") fetchBaseNow();
}

function systemOf() {
  return SYSTEMS.find((x) => x.id === S.system) || null;
}

function chooseSystem(id) {
  if (S.ui !== "prepare" || S.busy || S.stopped || S.goal !== "mainline") return;
  S.system = id;
  log(`system: ${systemOf().label}`);
  render();
}

function prepareNext() {
  if ($("prepare-next").disabled) return;
  const ticks = [...document.querySelectorAll("#card-prepare ul.check input")].map((x) => (x.checked ? "1" : "0")).join("");
  log(`checklist ticked: ${ticks}`);
  S.ui = "files";
  render();
}

// --- Files ----------------------------------------------------------------------
async function setImage(bytes, from) {
  S.image = null;
  S.phase = "start";
  S.imageState = "loading";
  delete document.body.dataset.imageRefusal;
  log(`image from ${from}: ${bytes.length} bytes`);
  const r = await checkImage(bytes, imagesFor(S.goal), MODEL.base);
  S.image = { bytes, sha256: r.sha256, pin: r.pin };
  S.phase = "image-ok";
  S.imageState = "ok";
  log(`image sha256 ${r.sha256} OK: ${r.pin.label}`);
  setText("image-status", TEXT.imageOk, "ok");
  show("image-retry", false);
  show("image-hand", false);
}

// backToFiles: a download failed after Next on the Files step (the downloads
// may still run then): before the unit is read, the page opens Files again so
// the person sees the error and Try again (re-look R-1)
function backToFiles() {
  if (S.ui === "connect" && S.before === null && !S.busy) S.ui = "files";
}

async function fetchImage() {
  const pin = imagesFor(S.goal)[0];
  if (!pin || S.imageState === "loading" || !["start", "image-ok"].includes(S.phase)) return;
  S.imageState = "loading";
  setText("image-status", "");
  show("image-retry", false);
  render();
  const b = bar("bar-image", "Installer");
  const r = await download.fetchBytes(`images/${pin.file}`, (n, t) => b.update(n, t), undefined, pin.size);
  log(`image download images/${pin.file}: ${r.ok ? `${r.bytes.length} bytes, ${b.mbps()} MB/s` : r.why}`);
  if (!r.ok) {
    hideBar("bar-image");
    S.imageState = "error";
    backToFiles();
    setText("image-status", r.missing ? TEXT.imageMissing : TEXT.downloadStopped, "warn");
    show("image-retry", !r.missing);
    show("image-hand");
    render();
    return;
  }
  try {
    await setImage(r.bytes, `this site, ${pin.file}`);
  } catch (e) {
    S.imageState = "error";
    document.body.dataset.imageRefusal = (e && e.code) || "error";
    log(`image refused (${e && e.code}): ${e && e.message} ${e && e.advice ? e.advice : ""}`);
    backToFiles();
    setText("image-status", TEXT.imageWrong, "warn");
    show("image-hand");
  }
  render();
}

async function imageFromFile(f) {
  // a new image only before the unit is read (the old page's rule)
  if (S.imageState === "loading" || !["start", "image-ok"].includes(S.phase) || S.busy || S.stopped) return;
  S.imageState = "loading";
  render();
  try {
    await setImage(new Uint8Array(await f.arrayBuffer()), `file ${f.name}`);
  } catch (e) {
    S.imageState = "error";
    document.body.dataset.imageRefusal = (e && e.code) || "error";
    log(`image refused (${e && e.code}): ${e && e.message} ${e && e.advice ? e.advice : ""}`);
    setText("image-status", TEXT.imageWrong, "warn");
    show("image-hand");
  }
  render();
}

// --- the mainline base (DESIGN-R23 M2): from this site by itself, or a folder -----
function baseUrl() {
  // another host for the base only as an https folder URL (null in this version)
  if (BASE.PAYLOAD_URL !== null && !/^http[s]:\/\/[^\s"']+\/$/.test(String(BASE.PAYLOAD_URL))) return BASE.dir;
  return BASE.PAYLOAD_URL || BASE.dir;
}

async function fetchBaseNow() {
  if (S.base.state === "loading") return;
  S.base = { state: "loading", data: null };
  setText("base-status", "");
  show("base-retry", false);
  render();
  const b = bar("bar-base", "Mainline Linux");
  let r;
  try {
    r = await download.fetchBase(baseUrl(), BASE.manifest_sha256, (n, t) => b.update(n, t));
  } catch (e) {
    r = { ok: false, why: `${e && e.name}: ${e && e.message}` };
  }
  log(`base download ${baseUrl()}: ${r.ok ? `OK, manifest sha256 ${r.sha}, ${r.total} bytes, ${b.mbps()} MB/s` : r.why}`);
  if (r.ok) {
    S.base = { state: "ok", data: r };
    setText("base-status", TEXT.baseOk, "ok");
  } else {
    hideBar("bar-base");
    S.base = { state: "error", data: null };
    backToFiles();
    setText("base-status", r.missing ? TEXT.baseMissing : r.wrong ? TEXT.baseWrong : TEXT.downloadStopped, "warn");
    show("base-retry", !r.missing && !r.wrong);
  }
  render();
}

async function baseFromFolder(files) {
  if (S.base.state === "loading" || S.ui !== "files") return;
  S.base = { state: "loading", data: null };
  setText("base-status", "");
  show("base-retry", false);
  render();
  const b = bar("bar-base", "Checking the folder");
  let r;
  try {
    r = await download.folderBase(files, BASE.manifest_sha256, (n, t) => b.update(n, t));
  } catch (e) {
    r = { ok: false, why: `${e && e.name}: ${e && e.message}` };
  }
  hideBar("bar-base");
  log(`base from a folder (${files.length} files): ${r.ok ? `OK, ${r.dir}, manifest sha256 ${r.sha}` : r.why}`);
  S.base = r.ok ? { state: "ok", data: r } : { state: "bad", data: null };
  setText("base-status", r.ok ? TEXT.baseFolderOk : TEXT.baseWrong, r.ok ? "ok" : "warn");
  render();
}

// checkChosen: a chosen file (the firmware or part 1) against its pin, with
// the streaming hash and the bar "Checking the file".
async function checkChosen(which, file, pin, okText, wrongText) {
  const st = S[which];
  if (st.state === "checking") return;
  const statusId = `${which}-status`;
  const slot = `bar-${which}`;
  st.state = "checking";
  st.file = null;
  setText(statusId, "");
  render();
  const b = bar(slot, "Checking the file");
  log(`${which}: chosen, ${file.size} bytes`);
  let r;
  try {
    r = await download.checkFile(file, pin, (n, t) => b.update(n, t), sha256OfFile);
  } catch (e) {
    r = { ok: false, why: `${e && e.name}: ${e && e.message}` };
  }
  hideBar(slot);
  log(`${which}: ${r.ok ? `sha256 ${r.sha256} OK (${b.mbps()} MB/s)` : `refused: ${r.why}`}`);
  if (r.ok) {
    st.state = "ok";
    st.file = file;
    setText(statusId, okText, "ok");
  } else {
    st.state = "bad";
    setText(statusId, wrongText, "warn");
  }
  render();
}

function keyState() {
  const k = S.key;
  if (k.text === "") return "none";
  if (!k.check) return "pending";
  if (!k.check.ok) return "bad";
  return $("sshkey-confirm").checked ? "ready" : "unconfirmed";
}

function renderKey(ks) {
  const k = S.key;
  const good = ks === "ready" || ks === "unconfirmed";
  $("sshkey-input").disabled = S.ui !== "files" || S.busy;
  $("sshkey-confirm").disabled = S.ui !== "files" || S.busy;
  show("sshkey-confirm-row", good);
  const need = S.goal === "mainline" && systemOf() && systemOf().keyRequired;
  show("sshkey-optional", !need); // R34: the key is optional for kiosk and kiosk-ha; console needs it
  $("sshkey-label").textContent = need ? "SSH key" : "SSH key (optional)";
  if (ks === "none") setText("sshkey-status", need ? TEXT.keyNeeded : "", need ? "warn" : "");
  else if (ks === "pending") setText("sshkey-status", TEXT.keyChecking);
  else if (ks === "bad") setText("sshkey-status", TEXT.keyBad(k.check.reason), "warn");
  else setText("sshkey-status", TEXT.keyGood(k.check), "ok");
  $("sshkey-confirm-text").textContent = good ? TEXT.keyConfirm(k.check) : "";
}

async function keyInput() {
  const text = $("sshkey-input").value;
  const seq = ++S.key.seq;
  S.key.text = text;
  S.key.check = null;
  $("sshkey-confirm").checked = false;
  render();
  if (text === "") return;
  let r;
  try {
    r = await sshkey.validate(text);
  } catch (e) {
    r = { ok: false, reason: `The page could not check the key (${e && e.name}: ${e && e.message}).` };
  }
  if (seq !== S.key.seq) return;
  S.key.check = r;
  log(r.ok ? `ssh key: ${r.type} ${r.bits} bits ${r.fingerprint}` : `ssh key refused: ${r.reason}`);
  render();
}

// the key the page sends: only a confirmed one, only for mainline
function keyForSend() {
  return S.goal === "mainline" && keyState() === "ready" ? S.key.check : null;
}

function filesNext() {
  if ($("files-next").disabled) return;
  const k = keyForSend();
  log(`files: image ${S.imageState}, firmware OK${S.goal === "mainline" ? `, base ${S.base.state}, system ${S.system}, key ${k ? k.fingerprint : "none"}` : ""}`);
  S.ui = "connect";
  render();
}

// --- Connect (download mode, read only) -------------------------------------------
async function chooseDevice() {
  try {
    return await navigator.usb.requestDevice({ filters: FILTERS });
  } catch (e) {
    if (e && e.name === "NotFoundError") {
      log("no device chosen");
      return null;
    }
    throw e;
  }
}

async function connectUnit() {
  need(["image-ok"]);
  if (S.ui !== "connect") throw new Refusal("wrong-step", "This step is not possible now.", "", false);
  const dev = await chooseDevice();
  if (!dev) return;
  onlyOne(await navigator.usb.getDevices());
  const kind = classifyDevice(dev);
  log(`device ${dev.vendorId.toString(16)}:${dev.productId.toString(16)}: ${kind}`);
  if (kind === "fastboot") {
    // no pre-entry state to compare with: refused before the device is opened
    throw new Refusal("no-pre-entry", "The unit is already in fastboot.", ADVICE.noPreEntry);
  }
  S.rk = await openOr(RockUsb.open(dev, log));
  S.rkTag = unitTag(dev);
  // the read-only layout and model gate, then the A/B and BCB state
  await readGate(S.rk, log, MODEL.base);
  S.before = await readState(S.rk, log, S.expectAb);
  S.phase = "download-checked";
  const fallback = S.before.otherKind === "successful" ? "both slots are successful" : "the other slot is the fallback";
  log(`download mode OK (${MODEL.label}): slot ${S.before.next} boots next, ${fallback}; boot command block: ${S.before.bcbCommand.name}` +
    (S.before.next === "b" ? "; this unit runs slot b: the installer keeps slot a (successful) and installs over slot b" : ""));
}

function connectNext() {
  if (S.phase !== "download-checked" || S.busy || S.stopped) return;
  S.ui = "start";
  render();
}

// --- Start: the boot command, fastboot, the installer ------------------------------
let fbWatch = null;
function stopFastbootWatch() {
  if (fbWatch) clearInterval(fbWatch);
  fbWatch = null;
}
// markFastbootReady: the fastboot device is present. It only logs: the person
// still presses Connect (no auto-connect of rockusb or fastboot).
function markFastbootReady() {
  if (S.stopped || S.phase !== "restarted" || S.fbReady) return;
  S.fbReady = true;
  stopFastbootWatch();
  log("fastboot device detected");
  render();
}
async function pollFastboot() {
  if (S.stopped || S.phase !== "restarted") {
    stopFastbootWatch();
    return;
  }
  let devices;
  try {
    devices = await navigator.usb.getDevices();
  } catch {
    return;
  }
  if (devices.some((d) => d.vendorId === FB_VID && d.productId === FB_PID)) markFastbootReady();
}

// writeAndRestart: the press of "Start the installer" is the consent to the
// page's only write (the boot command); no tick box (owner R12).
async function writeAndRestart() {
  need(["download-checked"]);
  if (S.ui !== "start") throw new Refusal("wrong-step", "This step is not possible now.", "", false);
  onlyOne(await navigator.usb.getDevices());
  log("start: the page writes the boot command (the BCB, 4 sectors), reads it back and restarts the unit into fastboot; " +
    "the restart resets both A/B slots to trying (the benign reset the page checks before it starts the installer)");
  await writeBootCommand(S.rk, S.before, log);
  await restart(S.rk);
  S.rk = null;
  S.phase = "restarted";
  log("boot command written and read back; restart sent");
  show("l-restarting");
  show("l-waiting");
  show("fb-connect");
  S.fbReady = false;
  stopFastbootWatch();
  fbWatch = setInterval(pollFastboot, 1000);
  pollFastboot();
}

// connectFastboot runs on the press of Connect: a fastboot device the page may
// see already is used without a chooser; else the chooser. Then the exact
// benign-reset check, and only then the installer.
async function connectFastboot() {
  need(["restarted"]);
  let d = null;
  try {
    d = (await navigator.usb.getDevices()).find((x) => x.vendorId === FB_VID && x.productId === FB_PID) || null;
  } catch {
    d = null;
  }
  if (!d) d = await chooseDevice();
  if (!d) return;
  onlyOne(await navigator.usb.getDevices());
  if (classifyDevice(d) !== "fastboot") {
    throw new Refusal("not-fastboot", "This is not the unit in fastboot.", "Wait for fastboot, then choose it.", false);
  }
  log(`fastboot device: ${sameUnit(S.rkTag, d)}`);
  S.fb = await openOr(Fastboot.open(d, log));
  S.fbState = await fastbootState(S.fb, S.before, EXPECT_FASTBOOT_PRODUCT, log);
  S.phase = "fastboot-checked";
  stopFastbootWatch();
  show("fb-connect", false);
  show("l-waiting", false);
  render();
  // the installer goes into the unit's memory right after the check (the
  // person started it with "Start the installer"); nothing is written
  const b = bar("bar-boot", "Sending the installer");
  await bootInstaller(S.fb, S.image, S.fbState.maxDownload, (n, total) => b.update(n, total));
  log(`installer sent (${b.mbps()} MB/s) and started: token ${S.image.pin.token}`);
  S.fb = null;
  S.phase = "booted";
  show("l-starting");
  s1Start();
}

// --- the unit's transfer link (step 1, step 2, the finish share one device) --------
function isXfer(d) {
  return d.vendorId === STEP2_USB.vendorId && d.productId === STEP2_USB.productId;
}
async function permittedXfer() {
  try {
    return (await navigator.usb.getDevices()).find(isXfer) || null;
  } catch {
    return null;
  }
}
function helloLine(tag, h) {
  const i = h.info || {};
  return `${tag}: unit interface ${h.usb}, mode ${h.mode}, state ${h.state}${h.detail ? ` (${h.detail})` : ""}, ` +
    `${(h.pins || []).length} pin(s); model ${i.model || "-"}, slot ${i.slot || "-"}, token ${i.token || "-"}, ` +
    `step1 "${i.step1 || ""}", step2 "${i.step2 || ""}"${i.unlock ? `, unlock ${i.unlock}` : ""}, ${h.mem_available} bytes of RAM free`;
}
// xferInUse(who): another part of the page uses the link now (each open is a
// new session, SET_INTERFACE, which would end the session of another).
function xferInUse(who) {
  const s1 = S1.timer !== null || S1.busy;
  const s2 = SY.timer !== null || SY.busy;
  const f = F.timer !== null || F.busy;
  return (who !== "step1" && s1) || (who !== "step2" && s2) || (who !== "finish" && f) || (who !== "status" && XS.busy);
}
function stopLoops() {
  for (const L of [S1, SY, F]) {
    if (L.timer) clearInterval(L.timer);
    L.timer = null;
  }
}

// --- Start: step 1 over the cable (I-6) ------------------------------------------
// phase: off, wait, error, drive, install, done
// back: the unit left the files screen after the page sent files (canceled,
// refused, its menu, a switch or repair): when it asks for the files again
// (record §6.6: the sealed files are gone) the page sends them again.
// installed: an install ran (the page never sends again after that).
const S1 = { phase: "off", stage: "", pins: { pins: [], fw_pins: [] }, link: null, timer: null, busy: false, sending: false, fwSent: false, p1Sent: false, fails: 0, again: 0,
  back: false, installed: false,
  last: "", lostAt: 0, drive: { dir: null, phase1: false, phase2: false, p1tried: false, p2tried: false, read1: false, read2: false, again2: false, swap: false, busy: false, unitPhase: "", stale: false } };

function s1Start() {
  S1.phase = "wait";
  S1.since = Date.now();
  if (S1.timer) clearInterval(S1.timer);
  S1.timer = setInterval(s1Tick, Math.max(0.2, STEP1.STATUS_EVERY_S) * 1000);
  s1Tick();
}

async function s1Open(dev) {
  if (xferInUse("step1")) throw new Error("the link is in use");
  S1.link = await step1usb.Step2Link.open(dev, STEP2_USB, log);
  const h = await S1.link.hello();
  log(helloLine("step 1", h));
  const d = step1usb.checkHello(h, { token: S.image.pin.token });
  if (d.go === "wait") {
    log(`step 1: ${d.why}: the page asks again`);
    S1.link = null;
    return null;
  }
  if (d.go !== "ok") {
    S1.link = null;
    throw new Refusal("step1-other", `Step 1: ${d.why}.`, ADVICE.afterWrite);
  }
  S1.pins = { pins: Array.isArray(h.pins) ? h.pins.slice() : [], fw_pins: Array.isArray(h.fw_pins) ? h.fw_pins.slice() : [] };
  noteStage(h);
  return h;
}

async function s1Tick() {
  if (S1.busy || S.stopped || !["wait", "drive", "install"].includes(S1.phase)) return;
  S1.busy = true;
  try {
    let st;
    if (!S1.link) {
      const dev = await permittedXfer();
      if (!dev) {
        show("x1-connect", S1.phase === "wait" && !S1.fwSent && Date.now() - S1.since > STEP1_HINT_MS);
        if (S1.phase === "install" || S1.phase === "drive") {
          if (!S1.lostAt) S1.lostAt = Date.now();
          if (S1.phase === "install" && Date.now() - S1.lostAt > 3000) show("i-lost");
          // the drive mode without the link: the page cannot see when the unit
          // asks for the mainline folder: the button of DESIGN v3 §6
          const D = S1.drive;
          if (S1.phase === "drive") show("d-manual-part", S.goal === "mainline" && D.phase1 && !D.phase2 && Date.now() - S1.lostAt > STEP1_HINT_MS);
        }
        return;
      }
      show("x1-connect", false);
      show("d-manual-part", false);
      st = await s1Open(dev);
      if (!st) return;
      // the drive the person asked for while the link was away
      if (S1.wantDrive && step1usb.classify(st).phase !== "drive") {
        S1.wantDrive = false;
        const ok = await step1usb.requestDrive(S1.link);
        log(`step 1: DRIVE ${ok ? "taken" : "refused"}`);
        if (!ok) driveRefused();
        S1.link = null;
        return;
      }
    } else {
      st = await S1.link.status();
    }
    S1.lostAt = 0;
    // the pins are the HELLO's of this session (a STATUS has none)
    await s1Act({ ...st, ...S1.pins });
  } catch (e) {
    if (e instanceof Refusal) {
      stop(e);
      return;
    }
    S1.link = null;
    log(`step 1: link: ${e && e.name}: ${e && e.message}`);
  } finally {
    S1.busy = false;
    render();
  }
}

// noteStage: the unit's stage as last seen (HELLO stage, or the files screen's
// state or drive phase): the drive fallback copies the mainline folder alone
// when the unit has the firmware file already (record §6.6: the drive mode
// keeps the stage).
function noteStage(st) {
  const c = step1usb.classify(st);
  if (st.stage === "firmware" || c.phase === "send-firmware" || (c.phase === "drive" && c.drive === "waiting-firmware")) S1.stage = "firmware";
  else if (st.stage === "bundle" || c.phase === "send-base" || (c.phase === "drive" && c.drive === "waiting-bundle")) S1.stage = "bundle";
}

function s1Fail(why) {
  S1.fails++;
  S1.link = null;
  S1.phase = "error";
  log(`step 1: send failed (${S1.fails}): ${why}`);
  for (const s of ["bar-fwsend", "bar-basesend"]) hideBar(s);
  show("s1-error");
  // the drive after the second failure, for both goals (the installer's drive
  // reader takes rootfs.ext4.zst since mainline-rc-10 76e1e3f, record §6.9)
  show("drive-offer", S1.fails >= 2);
}

function installLine(id) {
  for (const x of ["i-question", "i-plan", "i-installing", "i-writing", "i-done", "i-canceled", "i-refused", "i-stop"]) show(x, x === id);
  show("i-lost", false);
}

// s1Send runs one send with the busy line on; a throw is a failed send.
async function s1Send(fn) {
  S1.sending = true;
  render();
  try {
    return await fn();
  } catch (e) {
    return { ok: false, why: `${e && e.name}: ${e && e.message}` };
  } finally {
    S1.sending = false;
  }
}

async function s1Act(st) {
  const c = step1usb.classify(st);
  const key = `${st.state} ${step1usb.phaseOf(st)}`;
  if (key !== S1.last) {
    log(`step 1: unit ${key}`);
    S1.last = key;
  }
  if (c.phase !== "send-firmware" && c.phase !== "send-base") S1.again = 0;
  noteStage(st);
  if (c.phase === "drive") return driveFollow(c.drive);
  // in drive mode the unit's stash and check are lines of the drive part
  if (S1.phase === "drive" && (c.phase === "reading" || c.phase === "checking")) return drivePhaseShow(c.phase);
  // the unit asks for the files again after it left the files screen (review
  // MINOR-A, record §6.6), never after an install ran
  if ((c.phase === "send-firmware" || c.phase === "send-base") && (S1.back || S1.phase === "install")) {
    if (S1.installed || S1.phase === "drive" || S1.phase === "error") return;
    s1Again();
  }
  switch (c.phase) {
    case "send-firmware": {
      if (S1.phase !== "wait") return;
      // right after the COMMIT the unit may still say so for a moment
      if (S1.fwSent) return ++S1.again >= 5 ? s1Fail("the unit asks for the firmware file again") : undefined;
      const k = keyForSend();
      const keyBlob = k ? new Blob([k.file]) : null;
      const b = bar("bar-fwsend", "Sending the firmware file");
      const r = await s1Send(() => step1usb.sendFirmware(S1.link, st, S.fw.file, FIRMWARE.sha256, keyBlob,
        S.goal === "mainline" ? "mainline" : "official", (name, s, size) => b.update(s, size), log));
      if (!r.ok) return s1Fail(r.why);
      log(`step 1: the unit took the firmware file (${b.mbps()} MB/s)`);
      S1.fwSent = true;
      return;
    }
    case "reading":
      if (S.goal === "mainline") show("l-reading");
      return;
    case "send-base": {
      if (S1.phase !== "wait" || S.goal !== "mainline") return;
      if (S1.p1Sent) return ++S1.again >= 5 ? s1Fail("the unit asks for the mainline files again") : undefined;
      show("l-reading");
      const b = bar("bar-basesend", "Sending mainline Linux");
      const r = await s1Send(() => step1usb.sendBase(S1.link, st, S.base.data, (name, s, size, done, total) => b.update(done, total), log));
      if (!r.ok) return s1Fail(r.why);
      log(`step 1: the unit took the mainline files (${b.mbps()} MB/s)`);
      S1.p1Sent = true;
      return;
    }
    case "checking":
      show("l-checking");
      return;
    case "plan":
    case "installing":
    case "writing":
    case "question":
      if (c.phase === "installing") S1.installed = true;
      // a switch or repair of the unit's menu: the line only; the unit may ask
      // for the files again afterwards
      if (c.phase === "writing" && !S1.installed) S1.back = true;
      S1.phase = "install";
      S.ui = "install";
      installLine({ plan: "i-plan", question: "i-question", writing: "i-writing" }[c.phase] || "i-installing");
      return;
    case "done":
      installLine("i-done");
      s1End();
      if (S.goal === "mainline") {
        S.ui = "system";
        syStart();
      } else {
        S.ui = "done";
      }
      return;
    case "canceled":
    case "refused":
      // nothing was written: the page keeps following the unit, which may ask
      // for the files again (Install again on the unit)
      S.ui = "install";
      installLine(c.phase === "canceled" ? "i-canceled" : "i-refused");
      if (S1.phase === "wait" || S1.phase === "install") {
        S1.phase = "install";
        S1.back = true;
      }
      return;
    case "stop":
      S.ui = "install";
      installLine("i-stop");
      s1End();
      return;
    default:
      // "other" (the unit's menu) and "unknown": the page waits; the log has
      // the unit's state and phase (above). After files were sent, the unit's
      // menu may lead to the files screen again.
      if (c.phase === "other" && (S1.fwSent || S1.p1Sent)) S1.back = true;
      return;
  }
}

// s1Again: the unit asks for the files again after it left the files screen:
// back to the files (wait), every file is sent again from HELLO.
function s1Again() {
  log("step 1: the unit asks for the files again: the page sends them again");
  S1.phase = "wait";
  S1.back = false;
  S1.again = 0;
  S1.fwSent = false;
  S1.p1Sent = false;
  S.ui = "start";
  installLine("");
  for (const id of ["l-reading", "l-checking"]) show(id, false);
}

function s1End() {
  S1.phase = "done";
  if (S1.timer) clearInterval(S1.timer);
  S1.timer = null;
  S1.link = null;
}

async function s1ConnectClick() {
  if (S1.phase !== "wait" || S.stopped) return;
  try {
    await navigator.usb.requestDevice({ filters: XFER_FILTER });
    log("step 1: the unit's link chosen");
    show("x1-connect", false);
    s1Tick();
  } catch (e) {
    log(`step 1: no device chosen (${e && e.name})`);
  }
}

function sendAgain() {
  if (S1.phase !== "error" || S.stopped) return;
  show("s1-error", false);
  S1.phase = "wait";
  S1.again = 0;
  // the unit asks again for a file it had taken (it gave the files back): send it again
  S1.fwSent = false;
  S1.p1Sent = false;
  log("step 1: send again (a new session from HELLO)");
  s1Tick();
}

// --- the drive fallback (DESIGN §6) -------------------------------------------------
async function useDrive() {
  // after a failed send, or (review MINOR-F) while the page sees no link: the
  // step-1 service may not have started (record §6.6: the unit then offers
  // only its drive)
  if (!(S1.phase === "error" || (S1.phase === "wait" && !S1.link && !S1.fwSent)) || S.stopped || S1.busy) return;
  S1.busy = true;
  show("s1-error", false);
  show("x1-connect", false);
  S1.phase = "drive";
  show("drive");
  log("step 1: the person chose the drive; the page asks the unit for it (frame DRIVE)");
  try {
    const dev = await permittedXfer();
    if (dev) {
      if (!S1.link) {
        S1.link = await step1usb.Step2Link.open(dev, STEP2_USB, log);
        const h = await S1.link.hello();
        log(helloLine("step 1", h));
        const d = step1usb.checkHello(h, { token: S.image.pin.token });
        if (d.go === "wait") {
          // no token yet (re-look R-2): ask for the drive when the link is ready
          S1.wantDrive = true;
          S1.link = null;
          throw new Error("the unit does not name its installer yet");
        }
        if (d.go !== "ok") throw new Refusal("step1-other", `Step 1: ${d.why}.`, ADVICE.afterWrite);
        noteStage(h);
      }
      const ok = await step1usb.requestDrive(S1.link);
      log(`step 1: DRIVE ${ok ? "taken" : "refused"}`);
      if (!ok) driveRefused();
    } else {
      S1.wantDrive = true;
      log("step 1: no link for DRIVE now: the page asks when the link is back (the unit's screen also offers the drive by its Vol- key)");
    }
  } catch (e) {
    if (e instanceof Refusal) {
      S1.busy = false;
      stop(e);
      render();
      return;
    }
    log(`step 1: DRIVE: ${e && e.name}: ${e && e.message}`);
  }
  S1.link = null;
  S1.busy = false;
  render();
}

// driveRefused: the unit did not take the drive switch: back to the transfer text
function driveRefused() {
  S1.phase = "error";
  S1.wantDrive = false;
  show("drive", false);
  show("s1-error");
  show("drive-offer", false);
}

// drivePhaseShow: the drive part's line for the unit's phase: "reading" (the
// unit reads the drive, or its stash runs), "checking" (its plan child),
// "waiting-firmware", "waiting-bundle" or "" (it waits for the person).
function drivePhaseShow(p) {
  const D = S1.drive;
  D.unitPhase = p;
  // the unit read the drive of a phase (a give-back after it is a redo: review MINOR-D)
  // (D.swap: the unit asked for the folder once; from then on its phase 1 is
  // done for this boot: its RAM stash stays)
  if (p === "reading" || p === "checking") {
    if (D.phase2) D.read2 = true;
    else if (D.phase1 && !D.swap) D.read1 = true;
  }
  const reads = p === "reading" || (p === "checking" && S.goal !== "mainline");
  show("d-reading", D.phase1 && !D.phase2 && reads);
  show("d-checking", D.phase2 && (p === "reading" || p === "checking"));
}

async function driveFollow(p) {
  if (S1.phase !== "drive") {
    // the person chose the drive on the unit (Vol-)
    S1.phase = "drive";
    show("s1-error", false);
    show("drive");
    log("step 1: the unit is in drive mode");
  }
  const D = S1.drive;
  if (p === "waiting-bundle") D.swap = true;
  // the unit gave a drive phase back after it read it (a refusal: a dirty or
  // bad drive, a read during a copy): that phase is done again (review
  // MINOR-D). A refused read of the folder resets the unit's mainline state
  // (flow MainlineDriveRead: m.ml = nil): it then says "drive:
  // waiting-firmware", and with its RAM stash of this boot it takes the folder
  // drive there (review of d2420c2 MINOR-1): the folder again.
  if (p === "waiting-firmware" && D.phase2 && D.read2) driveGiveBack(2);
  else if (p === "waiting-bundle" && D.phase2 && D.read2) driveGiveBack(2);
  else if (p === "waiting-firmware" && D.phase1 && D.read1 && !D.swap) driveGiveBack(1);
  drivePhaseShow(p);
  // the unit asks for the mainline folder (its drive came back empty): the page
  // copies it by itself, no button (R13); after phase 1, or after the
  // firmware went over the cable (the base send failed), or again after a
  // give-back of the folder
  if ((p === "waiting-bundle" || (p === "waiting-firmware" && D.again2)) && S.goal === "mainline" && !D.phase2 && !D.busy && D.dir) {
    await drivePhase2();
  }
}

// driveGiveBack: the unit wants phase n again. Phase 1: the person chooses
// the drive again (the page copies again: written over, never deleted);
// phase 2: the page copies the folder again by itself when it can.
function driveGiveBack(n) {
  const D = S1.drive;
  log(`drive: the unit gave the drive back after it read phase ${n}: the page copies it again`);
  if (n === 1) {
    D.phase1 = false;
    D.read1 = false;
  }
  D.phase2 = false;
  D.read2 = false;
  D.again2 = n === 2;
  if (n === 2) D.swap = true;
  S.ui = "start";
  installLine("");
  for (const id of ["d-eject1", "d-eject2", "d-eject-how", "d-reading", "d-checking"]) show(id, false);
  show("drive-pick-row");
}

// driveStale: the drive handle is gone (the unit made a new drive): the
// person chooses it again; logged once (review NIT-5), no retry by the tick
function driveStale(x) {
  const D = S1.drive;
  if (!D.stale) log(`drive: the old drive handle: ${x && x.name}; the person chooses the drive again`);
  D.stale = true;
  D.dir = null;
  show("drive-pick-row");
}

async function drivePick() {
  if (S1.phase !== "drive" || S1.drive.busy) return;
  if (!drivecopy.supported(window)) {
    driveByHand();
    return;
  }
  try {
    S1.drive.dir = await window.showDirectoryPicker({ id: "uc-pxx-inst", mode: "readwrite" });
    S1.drive.stale = false;
    log(`drive: chosen (${S1.drive.dir.name})`);
  } catch (e) {
    if (e && e.name === "AbortError") return;
    log(`drive: the pick failed: ${e && e.name}: ${e && e.message}`);
    driveByHand();
    return;
  }
  // the unit already has the firmware file (sent over the cable) and asks for
  // the mainline folder: phase 2 only (by its drive phase, or by its stage
  // when the link has not told the drive phase yet)
  if (S.goal === "mainline" && wantsFolder() && !S1.drive.phase2) await drivePhase2();
  else if (!S1.drive.phase1) await drivePhase1();
  else if (S.goal === "mainline" && !S1.drive.phase2) await drivePhase2();
}

function wantsFolder() {
  const p = S1.drive.unitPhase;
  return p === "waiting-bundle" || S1.drive.again2 || (p !== "waiting-firmware" && !S1.drive.phase1 && S1.stage === "bundle");
}

// driveByHand: the browser cannot write to the drive. The userdebug goal gets
// the hand-copy text; the mainline files are only copied by the page (the
// person has no folder of them when the page downloaded them).
function driveByHand() {
  if (S.goal === "mainline") {
    log("drive: this browser cannot write to the drive; the mainline files go only by the page's copy or the cable");
    setText("drive-mainline", TEXT.driveHandMainline, "warn");
    return;
  }
  show("d-hand");
}

async function drivePhase1() {
  const D = S1.drive;
  D.busy = true;
  setText("drive-status", "");
  show("drive-again-row", false);
  try {
    const key = keyForSend();
    const want = [FIRMWARE.file, ...(key ? [drivecopy.KEY_FILE] : []), ...(S.goal === "mainline" ? [drivecopy.MAINLINE_DIR] : [])].sort();
    let e;
    try {
      e = await drivecopy.emptyRoot(D.dir);
    } catch (x) {
      driveStale(x);
      return;
    }
    log(`drive: top folder holds ${JSON.stringify(e.names)}`);
    // Copy again (a failed copy of this page, or a drive the unit gave back
    // as it was): only the files of this phase may be there (written over)
    const again = D.p1tried && e.other.every((n) => want.includes(n));
    if (!e.ok && !again) {
      setText("drive-status", TEXT.driveNotEmpty, "warn");
      return;
    }
    D.p1tried = true;
    // mainline: the folder (the installer's mainline signal on a drive) and
    // the key first, the .bin last: an early eject then never looks like an
    // official drive (review MINOR-E)
    if (S.goal === "mainline") {
      const m = await drivecopy.makeFolder(D.dir, drivecopy.MAINLINE_DIR);
      const inside = m.names.filter((n) => !drivecopy.ignoredInFolder(n));
      log(`drive: folder ${drivecopy.MAINLINE_DIR} ${again ? "opened again" : "made"}, ${inside.length} entries`);
      if (inside.length) throw new Error("the folder uc-pxx-mainline is not empty");
    }
    if (key) {
      const w = await drivecopy.writeKeyFile(D.dir, key.file);
      log(`drive: SSH-KEY.PUB ${w.ok ? "written and read back OK" : "FAILED"} ${key.fingerprint}`);
      if (!w.ok) throw new Error(w.reason);
    }
    const c = bar("bar-drive-copy", "Copying the firmware file");
    const k = bar("bar-drive-check", "Checking the copy");
    const r = await drivecopy.copyChecked(D.dir, FIRMWARE.file, S.fw.file, FIRMWARE, (n, t) => c.update(n, t),
      (n, t) => k.update(n, t), sha256OfFile);
    log(`drive: firmware copy ${r.ok ? `read back OK, sha256 ${r.sha256}` : `FAILED: ${r.reason}`}`);
    if (!r.ok) throw new Error(r.reason);
    const after = (await drivecopy.rootNames(D.dir)).filter((n) => !drivecopy.hostEntry(n));
    if (JSON.stringify(after) !== JSON.stringify(want)) throw new Error(`the drive holds ${JSON.stringify(after)}`);
    D.phase1 = true;
    D.read1 = false;
    show("drive-pick-row", false);
    show("d-eject1");
    setText("d-eject-how", ejectLine(S.platform));
    drivePhaseShow(D.unitPhase);
  } catch (e) {
    log(`drive: phase 1: ${e && e.name}: ${e && e.message}`);
    setText("drive-status", TEXT.copyFailed, "warn");
    show("drive-again-row");
  } finally {
    D.busy = false;
    render();
  }
}

// drivePhase2: the mainline folder (record §6.9): on the empty drive the
// folder uc-pxx-mainline with exactly manifest.json, boot-template.img and
// rootfs.ext4.zst (the unit unpacks it after the eject), each read back
// against its pin; never a delete.
async function drivePhase2() {
  const D = S1.drive;
  if (S.goal !== "mainline" || S.base.state !== "ok" || D.busy || D.phase2) return;
  D.busy = true;
  setText("drive-status", "");
  show("drive-again-row", false);
  show("d-manual-part", false);
  try {
    let e;
    try {
      e = await drivecopy.emptyRoot(D.dir);
    } catch (x) {
      // the handle of phase 1 is gone (the drive came back as a new one): pick again
      driveStale(x);
      return;
    }
    const B = S.base.data;
    const man = B.manifest;
    const list = [[download.BASE_NAMES.manifest, B.files.manifest, { size: B.files.manifest.size, sha256: B.sha }],
      [download.BASE_NAMES.template, B.files.template, man.boot_template], [download.BASE_NAMES.rootfsZst, B.files.rootfsZst, man.rootfs_zst]];
    const want = list.map(([n]) => n).sort();
    // Copy again after a failed copy of this page: the drive holds only the
    // folder, with some of the three files (written over, never deleted)
    const again = D.p2tried && JSON.stringify(e.other) === JSON.stringify([drivecopy.MAINLINE_DIR]);
    if (!e.ok && !again) {
      log(`drive: top folder holds ${JSON.stringify(e.names)}`);
      setText("drive-status", TEXT.driveNotEmpty, "warn");
      show("drive-pick-row");
      return;
    }
    const m = await drivecopy.makeFolder(D.dir, drivecopy.MAINLINE_DIR);
    log(`drive: folder ${drivecopy.MAINLINE_DIR} ${again ? "opened again" : "made"}, ${JSON.stringify(m.names)}`);
    if (m.names.some((n) => !want.includes(n) && !drivecopy.ignoredInFolder(n))) throw new Error(`the folder uc-pxx-mainline holds ${JSON.stringify(m.names)}`);
    D.p2tried = true;
    const c = bar("bar-drive-copy", "Copying mainline Linux");
    const k = bar("bar-drive-check", "Checking the copy");
    const total = list.reduce((n, [, f]) => n + f.size, 0);
    let base = 0;
    for (const [name, f, pin] of list) {
      const r = await drivecopy.copyChecked(m.dir, name, f, pin, (n) => c.update(base + n, total), (n) => k.update(base + n, total), sha256OfFile);
      log(`drive: ${name} copy ${r.ok ? `read back OK, sha256 ${r.sha256}` : `FAILED: ${r.reason}`}`);
      if (!r.ok) throw new Error(r.reason);
      base += f.size;
    }
    // exactly these: the folder alone on the drive, the three files in it
    const top = (await drivecopy.rootNames(D.dir)).filter((n) => !drivecopy.hostEntry(n));
    const inside = (await drivecopy.rootNames(m.dir)).filter((n) => !drivecopy.ignoredInFolder(n));
    if (JSON.stringify(top) !== JSON.stringify([drivecopy.MAINLINE_DIR]) || JSON.stringify(inside) !== JSON.stringify(want)) {
      throw new Error(`the drive holds ${JSON.stringify(top)}, the folder ${JSON.stringify(inside)}`);
    }
    D.phase2 = true;
    D.read2 = false;
    D.again2 = false;
    show("drive-pick-row", false);
    show("d-eject1", false);
    show("d-reading", false);
    show(D.phase1 ? "d-eject2" : "d-eject1");
    setText("d-eject-how", ejectLine(S.platform));
    drivePhaseShow(D.unitPhase);
  } catch (x) {
    log(`drive: phase 2: ${x && x.name}: ${x && x.message}`);
    setText("drive-status", TEXT.copyFailed, "warn");
    show("drive-again-row");
  } finally {
    D.busy = false;
    render();
  }
}

function driveAgain() {
  const D = S1.drive;
  if (D.busy || !D.dir) return;
  if (S.goal === "mainline" && (D.phase1 || wantsFolder()) && !D.phase2) drivePhase2();
  else if (!D.phase1) drivePhase1();
}

// --- Install the system (DESIGN-R23 M6): the choice, B's own install, the
// cable fallback, then the step-2 consent and the clone on the unit ------------
// phase: off, wait (B's link), provision (B installs), cable (the person
// chooses the bundle), consent (the page gives AUTO-STEP2), step2 (the unit
// clones B into A), done
const SY = { phase: "off", link: null, timer: null, busy: false, sending: false, since: 0, last: "", chosen: false,
  source: provisionusb.Q.SOURCE_NET, bundle: { state: "none", file: null }, cableOk: false, cableAllowed: false, cableFails: 0, cableHold: false };

const SY_LINES = ["p-checking", "p-network", "p-network-failed", "p-wrong", "s2-auto", "s2-runs", "s2-restarts", "s2-refused", "s2-stopped", "s2-stop"];
function syLine(id) {
  for (const x of SY_LINES) show(x, x === id);
  if (id) show("s2-waiting", false);
  if (id !== "p-network" && id !== "p-network-failed") show("p-cable-row", false);
  // the cable files only while the unit's state allows the cable (review MINOR-C)
  if (id === "p-wrong" || id.startsWith("s2-")) show("p-cable-files", false);
}

function syStart() {
  if (!STEP2_USB_ENABLED) log("system: STEP2_USB_ENABLED is false in this page version: the page sends no choice and no consent");
  SY.phase = "wait";
  SY.since = Date.now();
  const sys = systemOf();
  setText("p-installs", TEXT.installs(sys));
  if (sys.bundle) $("bundle-link").href = sys.bundle.url || RELEASES_URL;
  show("s2-waiting");
  if (SY.timer) clearInterval(SY.timer);
  SY.timer = setInterval(syTick, Math.max(0.2, PROVISION.STATUS_EVERY_S) * 1000);
}

function syEnd() {
  SY.phase = "done";
  if (SY.timer) clearInterval(SY.timer);
  SY.timer = null;
  SY.link = null;
}

async function syTick() {
  if (SY.busy || S.stopped || !["wait", "provision", "consent", "step2"].includes(SY.phase) || XS.busy) return;
  SY.busy = true;
  try {
    let st;
    if (!SY.link) {
      const dev = await permittedXfer();
      if (!dev) {
        if (Date.now() - SY.since > CONNECT_HINT_MS) show("s2-connect");
        return;
      }
      show("s2-connect", false);
      SY.link = await provisionusb.Step2Link.open(dev, STEP2_USB, log);
      st = await SY.link.hello();
      log(helloLine("system", st));
      if (st.mode === "finish") {
        syEnd();
        S.ui = "finish";
        fStart();
        return;
      }
      if (st.mode === provisionusb.Q.MODE) {
        // the unit's profiles (HELLO): the cable only for the bundle it pins
        SY.cableOk = provisionusb.cableMatches(st, S.system, systemOf().bundle);
        log(`system: the unit's profiles ${JSON.stringify(st.profiles || [])}, fixed "${st.profile || ""}"; the cable ${SY.cableOk ? "matches" : "does not match"} this page's bundle`);
      }
      // the choice: net on a unit that waits for it; the cable only after a
      // STATUS that allows it (retry-wait, or failed with fail network: the
      // HELLO has no fail) and not while a failed cable send waits for the
      // person's Send again (review MINOR-B, MINOR-C)
      let st2 = null;
      if (st.mode === provisionusb.Q.MODE && provisionusb.CHOICE_STATES.includes(st.state) && !SY.chosen && !SY.cableHold &&
        (st.state === provisionusb.Q.WAITING || (SY.source === provisionusb.Q.SOURCE_CABLE && SY.cableAllowed))) {
        if (!STEP2_USB_ENABLED) {
          log("system: off in this page version: no choice sent");
          syLine("s2-refused");
          syEnd();
          return;
        }
        // the decision is on a STATUS of this session, read right before the
        // COMMIT (review NIT-R1: a new session's HELLO has no fail)
        st2 = await SY.link.status();
        log(`system: unit STATUS ${st2.mode || ""} ${st2.state}${st2.fail ? ` fail ${st2.fail}` : ""}${st2.status_only ? " (status only)" : ""}`);
        const v2 = provisionusb.view(st2);
        const cable = SY.source === provisionusb.Q.SOURCE_CABLE;
        const takes = st2.mode === provisionusb.Q.MODE && provisionusb.CHOICE_STATES.includes(st2.state) &&
          (st2.state === provisionusb.Q.WAITING || (cable && !!v2.cable));
        if (takes) {
          const r = await provisionusb.sendChoice(SY.link, { ...st, state: st2.state, status_only: st2.status_only === true },
            { profile: S.system, source: SY.source, pin: systemOf().bundle,
              url: SY.source === provisionusb.Q.SOURCE_NET ? PROVISION.PROVISION_URL : null }, log);
          if (!r.ok && r.wait) {
            // a status-only link takes no choice: ask again, nothing sent
            log(`system: ${r.why}: the page asks again`);
            SY.link = null;
            return;
          }
          if (!r.ok) {
            log(r.config ? `system: page config error: ${r.why}: nothing sent` : `system: ${r.why}`);
            syLine("p-wrong");
            syEnd();
            return;
          }
          SY.chosen = true;
          SY.phase = "provision";
          if (cable) await sySendBundle();
          return;
        }
        // the STATUS does not take the choice: the page shows what it says
      }
      if (!st2 && st.mode === provisionusb.Q.CONSENT_MODE && st.state === provisionusb.Q.CONSENT_WAITING) {
        if (!STEP2_USB_ENABLED) {
          syLine("s2-refused");
          syEnd();
          return;
        }
        const r = await provisionusb.sendConsent(SY.link, st, log);
        if (!r.ok && r.wait) {
          // no plan id yet: ask again, nothing sent
          log(`step 2: ${r.why}: the page asks again`);
          SY.link = null;
          return;
        }
        if (!r.ok) {
          log(`step 2: ${r.why}`);
          syLine("s2-refused");
          syEnd();
          return;
        }
        SY.phase = "step2";
        hideBar("bar-provision");
        syLine("s2-auto");
        return;
      }
      // the page decides on a STATUS (state and fail), not on the HELLO
      st = st2 || await SY.link.status();
    } else {
      st = await SY.link.status();
    }
    const key = `${st.mode || ""} ${st.state} ${(st.info && st.info.step2) || ""} ${st.done || ""}/${st.total || ""}`;
    if (key !== SY.last) {
      log(`system: unit ${key}${st.fail ? ` fail ${st.fail}` : ""}${st.reason ? ` (${st.reason})` : ""}${st.next_try_s ? `, next try in ${st.next_try_s} s` : ""}`);
      SY.last = key;
    }
    // uc-pxx-usb's own status interface (mode "status") before the system
    // install or between its done and the step-2 consent: the page waits (its
    // info is not this run's step 2: step 2 tells its codes on its own link)
    if (st.mode === provisionusb.Q.GAP_MODE && ["wait", "provision", "consent"].includes(SY.phase)) return;
    if (st.mode === provisionusb.Q.MODE) {
      const v = provisionusb.view(st);
      if (v.bar === "download") {
        const b = barKeep("bar-provision", "Downloading the packages");
        b.update(v.done, v.total);
        syLine("");
      } else if (v.bar === "install") {
        const b = barKeep("bar-provision", "Installing the packages", true);
        b.update(v.done, v.total);
        syLine("");
      } else if (v.line) {
        if (v.line !== "p-network") hideBar("bar-provision");
        const canCable = !!v.cable && !!systemOf().bundle && SY.cableOk;
        if (v.line === "p-network-failed" && !canCable) {
          // 5 tries spent and no cable for this unit's pin: the page cannot go on
          log("system: the unit could not download the packages and its bundle pin is not this page's: no cable");
          syLine("p-wrong");
          syEnd();
          return;
        }
        syLine(v.line);
        show("p-cable-row", canCable && SY.source !== provisionusb.Q.SOURCE_CABLE);
      }
      SY.cableAllowed = !!v.cable;
      if (!v.cable && SY.source !== provisionusb.Q.SOURCE_CABLE) show("p-cable-files", false);
      if (v.next === "consent") {
        SY.phase = "consent";
        SY.link = null; // the unit's link comes back for the consent
      } else if (v.end) syEnd();
      return;
    }
    const v = step2View(st);
    if (v.line) syLine(v.line);
    if (v.end) {
      syEnd();
      if (v.next === "finish") {
        S.ui = "finish";
        fStart();
      }
    }
  } catch (e) {
    SY.link = null;
    log(`system: link: ${e && e.name}: ${e && e.message}`);
  } finally {
    SY.busy = false;
    render();
  }
}

// barKeep: the slot's bar with this label, without a reset while it is the same
function barKeep(slot, label, count = false) {
  const b = bars.get(slot);
  if (b && b.label === label && !$(slot).hidden) return b;
  const n = new Bar(document, label, count);
  $(slot).textContent = "";
  $(slot).append(n.el);
  bars.set(slot, n);
  $(slot).hidden = false;
  return n;
}

// the cable fallback: the person downloads the bundle from GitHub and chooses it
function syCableOffer() {
  if (S.stopped || !systemOf() || !systemOf().bundle) return;
  show("p-cable-row", false);
  show("p-cable-files");
}

async function syBundleChosen(f) {
  const pin = systemOf() && systemOf().bundle;
  if (!pin || SY.bundle.state === "checking") return;
  if (SY.phase === "done" || !SY.cableAllowed || !SY.cableOk) {
    // the unit's state does not take the cable now (review MINOR-C): nothing is sent
    log(`bundle: a file chosen while the unit does not take the cable (system ${SY.phase}): ignored`);
    return;
  }
  SY.bundle = { state: "checking", file: null };
  setText("bundle-status", "");
  const b = bar("bar-bundle", "Checking the file");
  const r = await download.checkFile(f, pin, (n, t) => b.update(n, t), sha256OfFile);
  hideBar("bar-bundle");
  log(`bundle: ${r.ok ? `sha256 ${r.sha256} OK` : `refused: ${r.why}`}`);
  if (!r.ok) {
    SY.bundle = { state: "bad", file: null };
    setText("bundle-status", TEXT.bundleWrong(pin), "warn");
    return;
  }
  if (SY.phase === "done" || !SY.cableAllowed || !SY.cableOk) {
    // the unit moved on while the file was checked (review NIT-R1): nothing is sent
    SY.bundle = { state: "none", file: null };
    log(`bundle: the unit does not take the cable now (system ${SY.phase}): not sent`);
    return;
  }
  SY.bundle = { state: "ok", file: f };
  setText("bundle-status", TEXT.bundleOk(pin), "ok");
  // the choice again, now with the cable as the source (the unit takes a new
  // COMMIT after a failed or waiting download, DESIGN-R23 §3.6)
  SY.source = provisionusb.Q.SOURCE_CABLE;
  SY.chosen = false;
  SY.cableHold = false;
  SY.link = null;
  SY.phase = "wait";
}

// bundleAgain: the person's Send again after a failed cable send
function bundleAgain() {
  if (S.stopped || SY.phase !== "wait" || !SY.cableHold || SY.bundle.state !== "ok") return;
  show("bundle-again-row", false);
  setText("bundle-status", "");
  SY.cableHold = false;
  SY.link = null;
  log("system: send the packages again (a new session from HELLO)");
}

async function sySendBundle() {
  const pin = systemOf().bundle;
  const b = bar("bar-bundle", "Sending the packages");
  SY.sending = true;
  render();
  let r;
  try {
    r = await provisionusb.sendBundle(SY.link, pin, SY.bundle.file, (n, t) => b.update(n, t), log);
  } catch (e) {
    r = { ok: false, why: `${e && e.name}: ${e && e.message}` };
  } finally {
    SY.sending = false;
  }
  if (!r.ok) {
    SY.cableFails++;
    log(`system: the cable send failed (${SY.cableFails}): ${r.why}`);
    hideBar("bar-bundle");
    SY.chosen = false;
    SY.link = null;
    SY.phase = "wait";
    if (SY.cableFails >= 2) {
      // a second failure of the same send ends it (as the CLI sender's rule)
      setText("bundle-status", "");
      syLine("p-wrong");
      syEnd();
      return;
    }
    // no retry by itself: the person presses Send again (review MINOR-B)
    SY.cableHold = true;
    setText("bundle-status", TEXT.transferStopped, "warn");
    show("bundle-again-row");
    return;
  }
  // the hand-over point (PUT_END ok): the unit's link comes back status only
  SY.link = null;
}

async function connectLater(tag, tick) {
  try {
    await navigator.usb.requestDevice({ filters: XFER_FILTER });
    log(`${tag}: the unit's link chosen`);
    tick();
  } catch (e) {
    log(`${tag}: no device chosen (${e && e.name})`);
  }
}

// --- Finish (the finish set from this site, FINISH.SITE_DIR) ---------------------------
const F = { phase: "off", cand: null, check: null, link: null, timer: null, busy: false, sending: false, since: 0, last: "",
  waitKey: "", outcome: "", sent: false };

function fLine(id) {
  for (const x of ["f-running", "f-done", "f-later", "f-cut", "f-refused", "f-stop"]) show(x, x === id);
  if (id) show("f-waiting", false);
}

// fLoadSet: the finish set from this site, checked by finishusb.checkSet (the
// list parses, the two files have its sizes and sha256, the list is pinned).
async function fLoadSet() {
  const files = {};
  for (const n of [finishusb.LIST, ...finishusb.FILES]) {
    const r = await download.fetchBytes(`${FINISH.SITE_DIR}${n}`);
    if (!r.ok) return { ok: false, why: `${FINISH.SITE_DIR}${n}: ${r.why}` };
    files[n] = new Blob([r.bytes]);
  }
  const cand = { dir: FINISH.SITE_DIR, files, other: [] };
  const c = await finishusb.checkSet(cand, FINISH.LISTS);
  log(`finish: the set of this site: ${c.ok ? `OK, finish.json sha256 ${c.sha}` : c.why}`);
  if (c.ok) {
    F.cand = cand;
    F.check = c;
  }
  return c;
}

function fStart() {
  XS.offer = false;
  F.waitKey = "";
  F.phase = "wait";
  F.since = Date.now();
  F.sent = false;
  F.outcome = "";
  show("f-waiting");
  if (!FINISH_USB_ENABLED) log("finish: FINISH_USB_ENABLED is false in this page version: the page sends no finish set");
  if (F.timer) clearInterval(F.timer);
  F.timer = setInterval(fTick, Math.max(0.2, FINISH.STATUS_EVERY_S) * 1000);
}

function fError(why) {
  log(`finish: ${why}`);
  F.link = null;
  hideBar("bar-finish");
  fLine("f-cut");
  F.phase = "error";
  if (F.timer) clearInterval(F.timer);
  F.timer = null;
}

async function fTick() {
  if (F.busy || S.stopped || F.phase !== "wait" || XS.busy) return;
  F.busy = true;
  try {
    if (!F.check) {
      const c = await fLoadSet();
      if (!c.ok) {
        fEnd("f-refused", "no-set");
        return;
      }
    }
    let st;
    if (!F.link) {
      const dev = await permittedXfer();
      if (!dev) {
        if (Date.now() - F.since > CONNECT_HINT_MS) show("f-connect");
        return;
      }
      show("f-connect", false);
      F.link = await finishusb.Step2Link.open(dev, STEP2_USB, log);
      const h = await F.link.hello();
      log(helloLine("finish", h));
      if (F.sent && h.mode === "finish" && h.state === "waiting-finish") {
        // the link ended at the COMMIT and the unit did not take the set
        F.sent = false;
        fError("the unit did not take the finish set");
        return;
      }
      if (!F.sent) {
        const d = finishusb.checkHello(h, F.check.sha, FINISH.LISTS);
        if (d.go === "stop") {
          const s2 = (h.info && h.info.step2) || "";
          const wk = `${h.mode} ${s2} ${d.why}`;
          if (wk !== F.waitKey) log(`finish: nothing sent: ${d.why}`); // once for each distinct code
          F.waitKey = wk;
          // a stop or hold of step 2 on the old slot's status-only link: keep the unit powered
          if (h.mode === "status" && /^(stop|hold)\b/.test(s2)) {
            fEnd("f-stop", "stop");
            return;
          }
          // not the finish slot yet: the payload slot, or the old slot's link while the unit restarts into the new
          // one (step 2 "started"/"armed"/"restarting"): wait for the new slot's link
          if (h.mode === "payload" || (h.mode === "status" && /^(started|armed|restarting)\b/.test(s2))) F.link = null;
          else fEnd("f-refused", "refused");
          return;
        }
        if (d.go === "send") {
          if (!FINISH_USB_ENABLED) {
            log("finish: off in this page version: nothing sent");
            fEnd("f-refused", "off");
            return;
          }
          const b = bar("bar-finish", "Sending the last files");
          F.sending = true;
          render();
          let r;
          try {
            r = await finishusb.sendSet(F.link, F.cand, F.check.list, (name, s, size, done, total) => b.update(done, total), log);
          } catch (e) {
            r = { ok: false, why: `the transfer stopped (${e && e.name}: ${e && e.message})` };
          } finally {
            F.sending = false;
          }
          F.link = null;
          if (r.ok === false) {
            fError(r.why);
            return;
          }
          F.sent = true;
          log(r.ok === true ? "finish: the unit took the finish set" : `finish: ${r.why}`);
          fLine("f-running");
          return;
        }
        F.sent = true; // "watch": the unit has a set already
      }
      st = h;
    } else {
      st = await F.link.status();
    }
    const v = finishView(st);
    const key = `${st.mode} ${st.state} ${(st.info && st.info.step2) || ""}`;
    if (key !== F.last) {
      log(`finish: unit ${key}`);
      F.last = key;
    }
    if (v.line) fLine(v.line);
    if (v.end) fEnd(v.line, v.outcome);
  } catch (e) {
    F.link = null;
    log(`finish: link: ${e && e.name}: ${e && e.message}`);
  } finally {
    F.busy = false;
    render();
  }
}

function fEnd(line, outcome) {
  fLine(line);
  F.outcome = outcome;
  F.phase = "done";
  if (F.timer) clearInterval(F.timer);
  F.timer = null;
  F.link = null;
}

// the status read offers "Send the finish set" only while nothing else runs and the page is not inside an install
function finishOffer() {
  return XS.offer && !XS.busy && !S.busy && !S.stopped && F.phase !== "wait" && !F.sending && !xferInUse("finish") &&
    !["start", "install", "system"].includes(S.ui);
}

function finishFromStatus() {
  if (!finishOffer()) return;
  XS.offer = false;
  log("finish: started from the unit's status: the unit waits for the finish set");
  S.goal = "mainline"; // only a mainline install has a finish (also after a reload, or a goal picked in Prepare)
  S.ui = "finish";
  fLine("");
  hideBar("bar-finish");
  fStart();
  render();
}

function finishAgain() {
  if (S.stopped || !(F.phase === "error" || (F.phase === "done" && F.outcome === "later"))) return;
  log("finish: send again");
  fLine("");
  hideBar("bar-finish");
  fStart();
  render();
}

// --- Details: the unit's status (read only, HELLO only) ---------------------------------
const XS = { busy: false, offer: false };
async function readXferStatus() {
  if (XS.busy || xferInUse("status")) return;
  XS.busy = true;
  render();
  try {
    let dev = await permittedXfer();
    if (!dev) dev = await navigator.usb.requestDevice({ filters: XFER_FILTER });
    const link = await finishusb.Step2Link.open(dev, STEP2_USB, log);
    const h = await link.hello();
    log(helloLine("status", h));
    const tb = $("xstatus-table");
    tb.textContent = "";
    for (const [k, v] of finishusb.statusRows(h)) {
      const tr = document.createElement("tr");
      const th = document.createElement("th");
      const td = document.createElement("td");
      th.textContent = k;
      td.textContent = v;
      tr.append(th, td);
      tb.append(tr);
    }
    $("xstatus-lines").textContent = ((h.info && h.info.lines) || []).join("\n");
    // a unit whose new slot waits for the finish set (e.g. after a reload of this page): offer to send it (render)
    XS.offer = h.mode === "finish" && h.state === "waiting-finish";
  } catch (e) {
    XS.offer = false;
    log(`status: ${e && e.name}: ${e && e.message}`);
  } finally {
    XS.busy = false;
    render();
  }
}

// --- the log, the shell, the unlock guide ---------------------------------------------
function saveBlob(blob, name) {
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = name;
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 10000);
}

function saveLog() {
  const head = [`UC-P10 installer page log, ${PAGE_VERSION}`, `saved ${new Date().toISOString()}`,
    `browser: ${S.browser ? S.browser.message : "?"}`, ""];
  const tail = ["", "--- serial transcript ---", S.shell ? S.shell.transcript : "(no shell opened)"];
  saveBlob(new Blob([[...head, ...S.lines, ...tail].join("\n") + "\n"], { type: "text/plain" }),
    `uc-pxx-installer-${new Date().toISOString().replace(/[:.]/g, "-")}.log`);
}

function termWrite(t) {
  const el = $("term");
  el.textContent += t;
  if (el.textContent.length > 200000) el.textContent = el.textContent.slice(-150000);
  el.scrollTop = el.scrollHeight;
}

async function openShell() {
  try {
    S.shell = await Shell.request(navigator.serial, termWrite);
    log("serial shell open");
  } catch (e) {
    log(`serial: ${e && e.name}: ${e && e.message}`);
  }
  render();
}

async function showUnlock() {
  if (S.unlockShown) return;
  S.unlockShown = true;
  $("unlock-show").disabled = true;
  try {
    await unlock.render($("unlock-guide"), UNLOCK);
    log("unlock guide shown");
  } catch (e) {
    log(`unlock guide: ${e && e.message}`);
  }
}

// --- init ----------------------------------------------------------------------
function pickFile(inputId) {
  const el = $(inputId);
  el.value = "";
  el.click();
}

function onFile(inputId, fn) {
  $(inputId).addEventListener("change", (ev) => {
    const f = ev.target.files && ev.target.files[0];
    if (f) fn(f);
  });
}

function init() {
  S.browser = checkBrowser(navigator, window.isSecureContext);
  S.platform = (navigator.userAgentData && navigator.userAgentData.platform) || "";
  document.body.dataset.platform = S.platform;
  document.body.dataset.browser = S.browser.ok ? "ok" : "refused";
  if (!S.browser.ok) setText("browser-status", S.browser.message, "stop");
  $("gate").hidden = DEVICE_USE_APPROVED;
  log(`page ${PAGE_VERSION}; ${S.browser.message}; platform ${S.platform || "?"}; secure context ${window.isSecureContext}; ` +
    `WebUSB ${!!navigator.usb}; Web Serial ${!!navigator.serial}`);
  if (!DEVICE_USE_APPROVED) log("device use is not approved for this page version: device buttons are off");
  S.expectAb = bindings();
  if (S.paramError) {
    setText("binding", S.paramError, "stop");
    log(S.paramError);
  } else if (S.expectAb.length) {
    setText("binding", `This session is bound to the A/B sector with first-32-byte sha256 ${S.expectAb.join(" and ")}.`, "warn");
    log(`session bound to A/B first-32 sha256 ${S.expectAb.join(", ")}`);
  } else log("no expect-ab binding");
  if (EXPECT_FASTBOOT_PRODUCT === null) log("no expected fastboot product recorded: the product is informational on this route (the benign-reset check is the gate)");
  $("image-releases").href = RELEASES_URL;

  $("goal-userdebug").addEventListener("click", () => chooseGoal("userdebug"));
  $("goal-mainline").addEventListener("click", () => chooseGoal("mainline"));
  for (const x of document.querySelectorAll("#card-prepare ul.check input")) x.addEventListener("change", render);
  $("prepare-next").addEventListener("click", prepareNext);
  $("image-again").addEventListener("click", () => {
    if (S.imageState !== "error") return;
    S.imageState = "none";
    fetchImage();
  });
  $("image-choose").addEventListener("click", () => pickFile("image-file"));
  onFile("image-file", imageFromFile);
  $("fw-choose").addEventListener("click", () => pickFile("fw-file"));
  onFile("fw-file", (f) => checkChosen("fw", f, FIRMWARE, TEXT.fwOk, TEXT.fwWrong));
  $("sshkey-input").addEventListener("input", keyInput);
  $("sshkey-confirm").addEventListener("change", render);
  $("files-next").addEventListener("click", filesNext);
  $("connect").addEventListener("click", () => step(connectUnit));
  $("connect-next").addEventListener("click", connectNext);
  $("start").addEventListener("click", () => step(writeAndRestart));
  $("connect-fastboot").addEventListener("click", () => step(connectFastboot));
  $("connect-step1").addEventListener("click", s1ConnectClick);
  $("send-again").addEventListener("click", sendAgain);
  $("use-drive").addEventListener("click", useDrive);
  $("use-drive-wait").addEventListener("click", useDrive);
  $("drive-pick").addEventListener("click", drivePick);
  $("drive-again").addEventListener("click", driveAgain);
  $("drive-copy-part").addEventListener("click", () => {
    if (S1.phase === "drive" && S1.drive.phase1 && !S1.drive.phase2 && S1.drive.dir) drivePhase2();
  });
  $("connect-step2").addEventListener("click", () => connectLater("system", syTick));
  $("p-cable").addEventListener("click", syCableOffer);
  $("bundle-choose").addEventListener("click", () => pickFile("bundle-file"));
  $("bundle-again").addEventListener("click", bundleAgain);
  onFile("bundle-file", syBundleChosen);
  for (const x of SYSTEMS) $(`system-${x.id}`).addEventListener("click", () => chooseSystem(x.id));
  $("base-choose").addEventListener("click", () => pickFile("base-folder"));
  $("base-folder").addEventListener("change", (ev) => {
    const fl = Array.from((ev.target && ev.target.files) || []);
    if (fl.length) baseFromFolder(fl);
  });
  $("base-again").addEventListener("click", () => {
    if (S.base.state === "error") fetchBaseNow();
  });
  $("connect-finish").addEventListener("click", () => connectLater("finish", fTick));
  $("finish-again").addEventListener("click", finishAgain);
  $("finish-again2").addEventListener("click", finishAgain);
  $("xstatus-read").addEventListener("click", readXferStatus);
  $("xstatus-finish").addEventListener("click", finishFromStatus);
  $("serial-open").addEventListener("click", openShell);
  $("save-log").addEventListener("click", saveLog);
  $("stop-save-log").addEventListener("click", saveLog);
  $("unlock-show").addEventListener("click", showUnlock);
  $("term-form").addEventListener("submit", (ev) => {
    ev.preventDefault();
    const v = $("term-input").value;
    $("term-input").value = "";
    if (S.shell) S.shell.send(v).catch((e) => log(`serial send: ${e && e.message}`));
  });

  if (navigator.usb) {
    navigator.usb.addEventListener("disconnect", (ev) => {
      log(`USB disconnect ${ev.device.vendorId.toString(16)}:${ev.device.productId.toString(16)}`);
      if (isXfer(ev.device)) {
        S1.link = null;
        SY.link = null;
        F.link = null;
      }
    });
    navigator.usb.addEventListener("connect", (ev) => {
      const d = ev.device;
      log(`USB connect ${d.vendorId.toString(16)}:${d.productId.toString(16)}`);
      // the transfer link: the loops open it at their next tick
      if (isXfer(d)) return;
      // a unit that comes back in Maskrom stops the page at once
      try {
        classifyDevice(d);
      } catch (e) {
        if (e instanceof Refusal && e.code === "maskrom") {
          stop(e);
          render();
          return;
        }
      }
      if (S.phase === "restarted" && DEVICE_USE_APPROVED && d.vendorId === FB_VID && d.productId === FB_PID) markFastbootReady();
    });
  }
  window.addEventListener("beforeunload", (ev) => {
    const ended = S.ui === "done" || F.outcome === "finished" || S.stopped;
    if (S.busy || S1.sending || SY.sending || F.sending || (!ended && ["download-checked", "restarted", "fastboot-checked", "booted"].includes(S.phase))) {
      ev.preventDefault();
      ev.returnValue = "";
    }
  });
  render();
}

init();
