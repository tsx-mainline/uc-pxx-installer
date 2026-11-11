// SPDX-License-Identifier: GPL-2.0-or-later
// main.js: the page. It runs the steps of enter.js in order, one at a time.
import {
  DEVICE_USE_APPROVED, EXPECT_AB_FIRST32_SHA256, EXPECT_FASTBOOT_PRODUCT, FIRMWARE, IMAGES,
  MAINLINE_INSTALL_APPROVED, MODEL, PAGE_VERSION, STEP2, STEP2_USB, STEP2_USB_ENABLED, UNLOCK, UNLOCK_GUIDE_ENABLED,
} from "./config.js";
import {
  ADVICE, bootInstaller, checkBrowser, checkImage, classifyDevice, fastbootState, onlyOne, readGate, readState, Refusal,
  restart, sameUnit, unitTag, writeBootCommand,
} from "./enter.js";
import { Fastboot, PID as FB_PID, VID as FB_VID } from "./fastboot.js";
import { RockUsb, PID as RK_PID, VID as RK_VID } from "./rockusb.js";
import { Shell } from "./serial.js";
import * as drivecopy from "./drivecopy.js";
import * as unlock from "./unlock.js";
import * as step2usb from "./step2usb.js";

const $ = (id) => document.getElementById(id);
const FILTERS = [{ vendorId: RK_VID, productId: RK_PID }, { vendorId: FB_VID, productId: FB_PID }];
const IMAGE_FALLBACK = "Download the image from the project's Releases page and choose the file on your computer.";

// phase: start → image-ok → download-checked → restarted → fastboot-checked → booted
// goal: null until the user chooses on the start screen. Only "userdebug" enables
// the device flow; "mainline" is gated off (MAINLINE_INSTALL_APPROVED).
const S = {
  phase: "start", goal: null, image: null, rk: null, before: null, fb: null, fbState: null,
  busy: false, stopped: false, shell: null, browser: null, lines: [], rkTag: "", expectAb: [], paramError: null,
  fbReady: false, unlockShown: false,
  drive: { source: null, dir: null, busy: false },
};

// After the restart the unit re-enumerates in fastboot. This watch tells the
// user WHEN it is there, so they no longer guess the ~10 s. It is read-only and
// adds no permission: it only polls navigator.usb.getDevices() (which lists the
// devices the page may already see) and listens to the "connect" event, both of
// which report a fastboot device only when it is actually present. It never
// opens or connects a device on its own — the user still presses the button.
let fbWatch = null;
function fastbootPresent(devices) {
  return devices.some((d) => d.vendorId === FB_VID && d.productId === FB_PID);
}
function stopFastbootWatch() {
  if (fbWatch) {
    clearInterval(fbWatch);
    fbWatch = null;
  }
}
// markFastbootReady: the fastboot device is present. Signal readiness; do NOT
// connect (the user presses "Connect to fastboot").
function markFastbootReady() {
  if (S.stopped || S.phase !== "restarted" || S.fbReady) return;
  S.fbReady = true;
  stopFastbootWatch();
  log("fastboot device detected");
  setText("fastboot-status", 'Fastboot detected. Press "Connect to fastboot".', "ok");
  render();
}
async function pollFastboot() {
  if (S.stopped || S.phase !== "restarted") {
    stopFastbootWatch();
    return;
  }
  if (!navigator.usb) return;
  let devices;
  try {
    devices = await navigator.usb.getDevices();
  } catch {
    return;
  }
  if (fastbootPresent(devices)) markFastbootReady();
}
function startFastbootWatch() {
  stopFastbootWatch();
  S.fbReady = false;
  fbWatch = setInterval(pollFastboot, 1000);
  pollFastboot();
}

// The A/B sector sha256 values (first 32 bytes) this session is bound to:
// the config constant and the URL parameter ?expect-ab=. Both can only add a
// refusal; neither can allow anything.
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
}

function render() {
  const live = !S.busy && !S.stopped;
  const dev = DEVICE_USE_APPROVED && S.browser && S.browser.ok && !S.paramError;
  const chosen = S.goal === "userdebug"; // only the userdebug path is shippable
  // The goal buttons: userdebug enabled once the browser is OK; mainline stays
  // off until MAINLINE_INSTALL_APPROVED (shown "coming soon").
  const preDevice = ["start", "image-ok"].includes(S.phase);
  $("goal-userdebug").disabled = !(live && dev) || !preDevice;
  $("goal-mainline").disabled = !(live && dev && MAINLINE_INSTALL_APPROVED) || !preDevice;
  // The image can be loaded any time; the device flow (connect onward) is
  // reachable only after the userdebug choice.
  $("image-file").disabled = !live || !["start", "image-ok"].includes(S.phase);
  for (const b of document.querySelectorAll(".image-site")) b.disabled = $("image-file").disabled;
  $("connect").disabled = !(live && dev && chosen && S.image && S.phase === "image-ok");
  $("confirm-box").hidden = S.phase !== "download-checked";
  $("write-bcb").disabled = !(live && dev && S.phase === "download-checked" && $("confirm").checked);
  $("connect-fastboot").disabled = !(live && dev && S.phase === "restarted");
  $("boot").hidden = S.phase !== "fastboot-checked";
  $("boot").disabled = !(live && dev && S.phase === "fastboot-checked");
  $("serial-open").disabled = !(S.browser && S.browser.serial) || S.busy || !!S.shell;
  $("term-form").hidden = !S.shell;
  $("busy").hidden = !S.busy;
  // The optional unlock guide: offered once the userdebug path is chosen.
  $("unlock-section").hidden = !(UNLOCK_GUIDE_ENABLED && chosen);
  // The drive-copy helper controls (host I/O only).
  renderDrive();
  renderStep2();
  document.body.dataset.phase = S.phase;
  document.body.dataset.goal = S.goal || "none";
  document.body.dataset.busy = String(S.busy);
  document.body.dataset.stopped = String(S.stopped);
  document.body.dataset.fastbootReady = S.phase === "restarted" && S.fbReady ? "yes" : "no";
}

// renderDrive: the drive-copy section (File System Access API). It is host-side
// file I/O only and does not depend on the unit flow.
function renderDrive() {
  const ok = drivecopy.supported(window);
  $("drivecopy-controls").hidden = !ok;
  if (!ok) {
    setText("drivecopy-support",
      "Your browser cannot write the drive for you (it needs the File System Access API in Chrome, Edge or Chromium). " +
      "Copy the firmware .bin onto UC-PXX-INST (or P10-INSTALL) by hand.", "warn");
  } else {
    setText("drivecopy-support", "This browser can copy and check the firmware for you.");
  }
  const d = S.drive;
  $("drivecopy-source-name").textContent = d.source ? `${d.source.name} (${d.source.size} bytes)` : "";
  $("drivecopy-dir-name").textContent = d.dir ? d.dir.name : "";
  $("drivecopy-source").disabled = d.busy;
  $("drivecopy-dir").disabled = d.busy;
  $("drivecopy-run").disabled = d.busy || !(d.source && d.dir);
}

function osAdvice() {
  const p = (navigator.userAgentData && navigator.userAgentData.platform) || "";
  if (p === "Windows") return "Windows: the unit needs the WinUSB driver for this mode (see the guide). Then try again.";
  if (p === "Linux") return "Linux: your user needs access to the device (a udev rule, see the guide). Then try again.";
  return "Close other programs that use the unit (for example fastboot or rkdeveloptool). Then try again.";
}

function stop(e) {
  let r = e;
  if (!(e instanceof Refusal)) {
    if (e && e.name === "TransferError") r = new Refusal("read-interrupted", e.message, ADVICE.readInterrupted);
    else {
      // The advice follows how far the entry got (review F5).
      const written = S.before && S.before.writeStarted;
      const advice = !written ? ADVICE.refusedNothingWritten : S.phase === "download-checked" ? ADVICE.writeInterrupted : ADVICE.afterWrite;
      r = new Refusal(written ? "unexpected-after-write" : "unexpected", `Unexpected error: ${e && e.name}: ${e && e.message}`, advice);
    }
  }
  log(`STOP ${r.code}: ${r.message}`);
  if (r.fatal) {
    stopFastbootWatch();
    S.stopped = true;
    document.body.dataset.refusal = r.code;
    $("stop").hidden = false;
    setText("stop-message", r.message);
    setText("stop-advice", r.advice);
  } else {
    document.body.dataset.notice = r.code;
    setText("notice", `${r.message} ${r.advice}`, "warn");
  }
}

// step runs one action. There is never a second one at the same time, and
// nothing here cancels a transfer: a slow unit only shows a note.
// note: what this step does to the unit, for the waiting text (review F16).
const NOTE = Object.freeze({
  read: "This step only reads; nothing is written.",
  write: "This step writes the boot command. Do not power off the unit: the page must read it back.",
  memory: "This step sends the installer into the unit's memory; nothing is written to its storage.",
  none: "",
});

async function step(fn, note = NOTE.none) {
  if (S.busy || S.stopped) return;
  S.busy = true;
  setText("notice", "");
  delete document.body.dataset.notice;
  render();
  const t0 = Date.now();
  setText("busy", `Working. Do not unplug the unit and do not close this page. ${note}`);
  const slow = setInterval(() => setText("busy",
    `Still waiting for the unit (${Math.round((Date.now() - t0) / 1000)} s). Do not unplug it and do not close this page. ${note}`), 30000);
  try {
    await fn();
  } catch (e) {
    stop(e);
  } finally {
    clearInterval(slow);
    setText("busy", "Working. Do not unplug the unit and do not close this page.");
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
}

async function setImage(bytes, from) {
  S.image = null;
  S.phase = "start";
  log(`image from ${from}: ${bytes.length} bytes`);
  const r = await checkImage(bytes, IMAGES, MODEL.base);
  S.image = { bytes, sha256: r.sha256, pin: r.pin };
  S.phase = "image-ok";
  log(`image sha256 ${r.sha256} OK: ${r.pin.label}`);
  setText("image-status", `OK: ${r.pin.label}. sha256 ${r.sha256}.`, "ok");
}

async function openOr(promise) {
  try {
    return await promise;
  } catch (e) {
    if (e instanceof Refusal) throw e;
    throw new Refusal("open-failed", `The page cannot open the unit (${e && e.name}: ${e && e.message}).`, osAdvice(), false);
  }
}

// chooseUserdebug: the start-screen choice. It only sets the goal and reveals
// the rest of the (unchanged) flow. The mainline choice is gated off and does
// nothing until MAINLINE_INSTALL_APPROVED.
function chooseUserdebug() {
  if (!["start", "image-ok"].includes(S.phase) || !DEVICE_USE_APPROVED || !(S.browser && S.browser.ok) || S.paramError) return;
  S.goal = "userdebug";
  log("goal: Android userdebug");
  setText("goal-status", "Chosen: Android userdebug. Load the installer image below.", "ok");
  render();
}
function chooseMainline() {
  if (!MAINLINE_INSTALL_APPROVED) {
    setText("goal-status", "Mainline Linux is not ready yet. It turns on when the mainline installer ships.", "warn");
    return;
  }
  // Reserved: when a mainline image is pinned and reviewed, this sets S.goal.
  S.goal = "mainline";
  log("goal: Mainline Linux");
  render();
}

async function useFastboot(dev, before) {
  S.fb = await openOr(Fastboot.open(dev, log));
  S.fbState = await fastbootState(S.fb, before, EXPECT_FASTBOOT_PRODUCT, log);
  S.phase = "fastboot-checked";
  setText("fastboot-status", `Fastboot OK: slot ${S.fbState.slot}. Press "Start the installer".`, "ok");
}

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
  if (S.goal !== "userdebug") throw new Refusal("no-goal", "Choose what to install first.", "", false);
  const dev = await chooseDevice();
  if (!dev) return;
  onlyOne(await navigator.usb.getDevices());
  const kind = classifyDevice(dev);
  log(`device ${dev.vendorId.toString(16)}:${dev.productId.toString(16)}: ${kind}`);
  if (kind === "fastboot") {
    // No pre-entry state to compare with: refused before the device is opened.
    throw new Refusal("no-pre-entry", "The unit is already in fastboot.", ADVICE.noPreEntry);
  }
  S.rk = await openOr(RockUsb.open(dev, log));
  S.rkTag = unitTag(dev);
  // The read-only layout and model gate first (GPT + factory), then the A/B and
  // BCB state. Nothing is written in either step.
  await readGate(S.rk, log, MODEL.base);
  S.before = await readState(S.rk, log, S.expectAb);
  S.phase = "download-checked";
  const fallback = S.before.otherKind === "successful" ? "both slots are successful" : "the other slot is the fallback";
  // A unit that runs slot B is accepted only when slot A is successful (readState);
  // tell the user before the write that the installer keeps A and replaces B.
  const runsB = S.before.next === "b"
    ? " Note: this unit runs slot B. The installer keeps slot A (it started successfully before) and installs over " +
      "slot B, the system the unit runs now. The unit's screen shows both versions before you start the install."
    : "";
  setText("unit-status",
    `Download mode OK (${MODEL.label}). Slot ${S.before.next} boots next, ${fallback}. Boot command block: ${S.before.bcbCommand.name}.${runsB}`, "ok");
}

async function writeAndRestart() {
  need(["download-checked"]);
  if (!$("confirm").checked) throw new Refusal("not-confirmed", "Tick the box first.", "", false);
  onlyOne(await navigator.usb.getDevices());
  await writeBootCommand(S.rk, S.before, log);
  await restart(S.rk);
  S.rk = null;
  S.phase = "restarted";
  setText("unit-status", "The boot command is written and read back. The unit restarts into fastboot (about 10 s).", "ok");
  setText("fastboot-status",
    'The unit restarts into fastboot (about 10 s). When it is detected this step says so; otherwise press "Connect to fastboot" and choose the unit.');
  startFastbootWatch();
}

async function connectFastboot(dev) {
  need(["restarted"]);
  const d = dev || (await chooseDevice());
  if (!d) return;
  onlyOne(await navigator.usb.getDevices());
  if (classifyDevice(d) !== "fastboot") {
    throw new Refusal("not-fastboot", "This is not the unit in fastboot.", "Wait for fastboot, then choose it.", false);
  }
  log(`fastboot device: ${sameUnit(S.rkTag, d)}`);
  await useFastboot(d, S.before);
}

// connectFastbootClick runs when the user presses "Connect to fastboot". If the
// fastboot device is one the page may already see (the watch found it), it is
// used directly, with no chooser; otherwise the chooser is shown. Either way
// the user started it, and no device is opened before the press.
async function connectFastbootClick() {
  let dev = null;
  if (navigator.usb) {
    try {
      dev = (await navigator.usb.getDevices()).find((d) => d.vendorId === FB_VID && d.productId === FB_PID) || null;
    } catch {
      dev = null;
    }
  }
  return connectFastboot(dev);
}

async function boot() {
  need(["fastboot-checked"]);
  $("progress").hidden = false;
  await bootInstaller(S.fb, S.image, S.fbState.maxDownload, (n, total) => {
    $("progress").max = total;
    $("progress").value = n;
  });
  S.fb = null;
  S.phase = "booted";
  setText("fastboot-status", "The installer starts. The unit screen shows its status in about a minute.", "ok");
}

function saveLog() {
  const head = [`UC-P10 installer entry page log, ${PAGE_VERSION}`, `saved ${new Date().toISOString()}`,
    `browser: ${S.browser ? S.browser.message : "?"}`, ""];
  const tail = ["", "--- serial transcript ---", S.shell ? S.shell.transcript : "(no shell opened)"];
  const blob = new Blob([[...head, ...S.lines, ...tail].join("\n") + "\n"], { type: "text/plain" });
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = `uc-pxx-enter-${new Date().toISOString().replace(/[:.]/g, "-")}.log`;
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 10000);
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

// --- drive-copy (host-side file I/O only; never touches the unit) -----------
async function driveChooseSource() {
  try {
    S.drive.source = await drivecopy.pickSource(window);
    log(`drive-copy: firmware chosen (${S.drive.source.size} bytes)`);
  } catch (e) {
    if (e && e.name === "AbortError") return;
    setText("drivecopy-status", `Could not choose a file: ${e && e.message}`, "warn");
  }
  render();
}
async function driveChooseDir() {
  try {
    S.drive.dir = await drivecopy.pickDirectory(window);
    log(`drive-copy: drive chosen (${S.drive.dir.name})`);
  } catch (e) {
    if (e && e.name === "AbortError") return;
    setText("drivecopy-status", `Could not choose a drive: ${e && e.message}`, "warn");
  }
  render();
}
async function driveRun() {
  if (S.drive.busy || !(S.drive.source && S.drive.dir)) return;
  S.drive.busy = true;
  render();
  const prog = $("drivecopy-progress");
  prog.hidden = false;
  prog.value = 0;
  prog.max = S.drive.source.size || 1;
  setText("drivecopy-status", "Copying…");
  try {
    const name = drivecopy.fatName(S.drive.source.name);
    const r = await drivecopy.writeAndVerify(S.drive.dir, S.drive.source, name, FIRMWARE, (n, total) => {
      prog.value = n;
      prog.max = total;
    });
    // Green only on a verified match against the firmware pin; a copy we cannot
    // verify (different size) stays a neutral warning (review C).
    setText("drivecopy-status", r.reason, r.ok && r.verified ? "ok" : "warn");
    log(`drive-copy: ${r.ok ? (r.verified ? "VERIFIED" : "copied (unverified)") : "FAILED"} sha256 ${r.sha256}`);
  } catch (e) {
    setText("drivecopy-status", `Copy failed: ${e && e.message}. Use the manual copy.`, "warn");
    log(`drive-copy: error ${e && e.name}: ${e && e.message}`);
  } finally {
    prog.hidden = true;
    S.drive.busy = false;
    render();
  }
}

// --- mainline step 2 over the USB cable (STEP2_USB_ENABLED; off by default) --
// The unit's transport (uc-pxx-xfer) takes the payload into RAM; the unit
// checks everything and starts step 2 only on its own screen. The page never
// writes the unit's storage. Phases: off, idle, folder-ok, sending, error,
// sent (STATUS every few seconds, across the unit's restart of the transport
// after the seal), done.
const S2 = { phase: STEP2_USB_ENABLED ? "idle" : "off", cand: null, check: null, link: null, busy: false, timer: null,
  pollBusy: false, last: "" };

function isStep2Device(d) {
  return d.vendorId === STEP2_USB.vendorId && d.productId === STEP2_USB.productId;
}

function renderStep2() {
  $("step2-section").hidden = !STEP2_USB_ENABLED;
  const ok = STEP2_USB_ENABLED && S.browser && S.browser.ok && !S2.busy;
  $("step2-folder").disabled = !ok || !["idle", "folder-ok", "error"].includes(S2.phase);
  $("step2-auto").disabled = !ok || !["idle", "folder-ok", "error"].includes(S2.phase);
  $("step2-connect").disabled = !ok || !["folder-ok", "error"].includes(S2.phase);
  document.body.dataset.step2 = S2.phase;
}

async function step2Folder(ev) {
  const files = Array.from((ev.target && ev.target.files) || []);
  S2.cand = S2.check = null;
  S2.phase = "idle";
  const cands = step2usb.findPayloads(files);
  log(`step 2: folder with ${files.length} files, ${cands.length} uc-pxx-mainline folder(s)`);
  const why = [];
  for (const c of cands) {
    const r = await step2usb.checkPayload(c, STEP2.PAYLOADS);
    log(`step 2: ${c.dir}: ${r.ok ? "OK" : r.why}`);
    if (r.ok) {
      S2.cand = c;
      S2.check = r;
      break;
    }
    why.push(r.why);
  }
  if (S2.cand) {
    S2.phase = "folder-ok";
    setText("step2-folder-status", `OK: ${S2.check.pin.label} (${S2.cand.dir}, ${S2.check.total} bytes).`, "ok");
  } else {
    setText("step2-folder-status", cands.length ? `No step-2 payload of this page in the folder: ${why.join(" ")}`
      : "No uc-pxx-mainline folder with manifest.json, boot-template.img and rootfs.ext4 in the chosen folder.", "warn");
  }
  render();
}

async function step2Device() {
  const have = (await navigator.usb.getDevices()).find(isStep2Device);
  if (have) return have;
  try {
    return await navigator.usb.requestDevice({ filters: [{ vendorId: STEP2_USB.vendorId, productId: STEP2_USB.productId }] });
  } catch (e) {
    if (e && e.name === "NotFoundError") {
      log("step 2: no device chosen");
      return null;
    }
    throw e;
  }
}

async function step2Open(dev) {
  S2.link = await step2usb.Step2Link.open(dev, STEP2_USB, log);
  const h = await S2.link.hello();
  log(`step 2: unit transport ${h.usb}, state ${h.state}${h.detail ? ` (${h.detail})` : ""}, ${(h.pins || []).length} pin(s), ` +
    `${h.mem_available} bytes of RAM free`);
  return h;
}

const S2_CUT = "Nothing was written to the unit's storage, and the unit drops a partial copy by itself. Check the cable, " +
  "then press the button again: the page starts again from the manifest.";

async function step2Send() {
  if (S2.busy || !["folder-ok", "error"].includes(S2.phase) || !STEP2_USB_ENABLED) return;
  S2.busy = true;
  render();
  try {
    const dev = await step2Device();
    if (!dev) return;
    const h = await step2Open(dev);
    if (h.state === "payload-complete" || h.status_only) {
      S2.phase = "sent";
      setText("step2-status", "The unit already has the payload. Follow the unit's screen.", "ok");
      step2Watch();
      return;
    }
    if (h.state !== "waiting-payload" && h.state !== "receiving") {
      S2.phase = "error";
      setText("step2-status", `The unit is not waiting for step 2 (state ${h.state}).`, "warn");
      return;
    }
    if (!Array.isArray(h.pins) || !h.pins.includes(S2.check.sha)) {
      S2.phase = "error";
      setText("step2-status", `The unit's step-2 gate does not take this payload (manifest sha256 ${S2.check.sha}). ` +
        "Nothing was sent. Use the release folder that matches the unit's step 1.", "warn");
      return;
    }
    S2.phase = "sending";
    render();
    const prog = $("step2-progress");
    prog.hidden = false;
    prog.value = 0;
    prog.max = S2.check.total;
    const t0 = Date.now();
    const auto = $("step2-auto").checked;
    log(`step 2: sending ${S2.cand.dir} (${S2.check.total} bytes), ${auto ? "continue by itself (30 s countdown)" : "hold OK on the unit"}`);
    const r = await step2usb.sendPayload(S2.link, S2.cand, auto, (name, sent, size, done, total) => {
      prog.value = done;
      const s = Math.max((Date.now() - t0) / 1000, 0.001);
      setText("step2-status", `Sending ${name}: ${Math.floor(done / 1048576)} of ${Math.floor(total / 1048576)} MiB ` +
        `(${(done / 1e6 / s).toFixed(1)} MB/s). Do not disconnect the cable and do not close this page.`);
    }, log);
    prog.hidden = true;
    if (!r.ok) {
      S2.phase = "error";
      setText("step2-status", `${r.why}. Nothing was written to the unit's storage.`, "warn");
      return;
    }
    log(`step 2: payload complete in ${((Date.now() - t0) / 1000).toFixed(1)} s`);
    S2.phase = "sent";
    setText("step2-status", auto
      ? "The unit has the payload. It starts step 2 by itself after a 30 s countdown on its screen (Back on the unit cancels)."
      : "The unit has the payload. Confirm on the unit's screen: hold OK for 3 s.", "ok");
    step2Watch();
  } catch (e) {
    $("step2-progress").hidden = true;
    S2.link = null;
    S2.phase = "error";
    log(`step 2: ${e && e.name}: ${e && e.message}`);
    setText("step2-status", `The transfer stopped (${e && e.message}). ${S2_CUT}`, "warn");
  } finally {
    S2.busy = false;
    render();
  }
}

// step2Text: what the page shows for the unit's step-2 state (the info the
// step-2 service gives the transport; STEP2-USB-TRANSPORT §3.4).
function step2Text(st) {
  const s2 = (st.info && st.info.step2) || "";
  if (/^restarting/.test(s2)) return ["The unit restarts into the new slot. The page's part ends here.", "ok", true];
  if (/^(stop|hold)/.test(s2)) {
    return ["KEEP THE UNIT POWERED. Do not restart it. Save the log and ask for help (the unit's screen shows the reason).", "stop", true];
  }
  if (/^plan-refused/.test(s2)) return [`The unit refused step 2: ${s2.slice("plan-refused".length).replace(/^[:\s]+/, "")}. Nothing was written. Save the log.`, "warn", true];
  if (/^(started|writing|armed)/.test(s2)) return [`Installing the other slot (${s2}): do not cut the power.`, "", false];
  if (st.state === "payload-complete" || st.state === "status-only") return ["The unit has the payload. Follow the unit's screen.", "ok", false];
  return [`The unit's transport: ${st.state}${st.detail ? ` (${st.detail})` : ""}.`, "", false];
}

async function step2Poll() {
  if (S2.pollBusy || S2.phase !== "sent") return;
  S2.pollBusy = true;
  try {
    if (!S2.link) {
      const d = (await navigator.usb.getDevices()).find(isStep2Device);
      if (!d) {
        setText("step2-status", "Waiting for the unit's transport (it restarts after the payload is sealed). Keep the cable connected.");
        return;
      }
      await step2Open(d);
    }
    const st = await S2.link.status();
    const [text, cls, end] = step2Text(st);
    const lines = (st.info && st.info.lines) || [];
    $("step2-lines").textContent = lines.join("\n");
    const key = `${st.state} ${(st.info && st.info.step2) || ""}`;
    if (key !== S2.last) {
      log(`step 2: unit ${key}`);
      S2.last = key;
    }
    setText("step2-status", text, cls);
    if (end) {
      S2.phase = "done";
      clearInterval(S2.timer);
      S2.timer = null;
    }
  } catch (e) {
    // the unit restarts the transport after the seal (a USB disconnect and
    // connect), or the cable was pulled: open it again at the next tick
    S2.link = null;
    log(`step 2: status: ${e && e.message}`);
  } finally {
    S2.pollBusy = false;
    render();
  }
}

function step2Watch() {
  if (S2.timer) clearInterval(S2.timer);
  S2.timer = setInterval(step2Poll, Math.max(0.2, STEP2.STATUS_EVERY_S) * 1000);
}

// --- unlock guide (opt-in; shows one-liners the user runs over adb) ----------
async function showUnlock() {
  if (S.unlockShown) return;
  S.unlockShown = true;
  $("unlock-show").disabled = true;
  try {
    await unlock.render($("unlock-guide"), UNLOCK);
    log("unlock guide shown");
  } catch (e) {
    setText("unlock-guide", `Could not load the unlock guide: ${e && e.message}`, "warn");
  }
}

function init() {
  S.browser = checkBrowser(navigator, window.isSecureContext);
  setText("browser-status", S.browser.message, S.browser.ok ? "ok" : "stop");
  document.body.dataset.browser = S.browser.ok ? "ok" : "refused";
  $("gate").hidden = DEVICE_USE_APPROVED;
  log(`page ${PAGE_VERSION}; ${S.browser.message}`);
  if (!DEVICE_USE_APPROVED) log("device use is not approved for this page version: device buttons are off");
  S.expectAb = bindings();
  if (S.paramError) {
    setText("binding", S.paramError, "stop");
    log(S.paramError);
  } else if (S.expectAb.length) {
    setText("binding", `This session is bound to the A/B sector with first-32-byte sha256 ${S.expectAb.join(" and ")}.`, "warn");
    log(`session bound to A/B first-32 sha256 ${S.expectAb.join(", ")}`);
  }
  if (EXPECT_FASTBOOT_PRODUCT === null) log("no expected fastboot product recorded: the product is informational on this route (the benign-reset check is the gate)");

  const box = $("image-site-box");
  for (const pin of IMAGES) {
    const b = document.createElement("button");
    b.className = "image-site";
    b.textContent = `Use "${pin.label}" from this site`;
    b.addEventListener("click", () => step(async () => {
      // A missing image, a network error or a broken transfer is a notice, not
      // a stop: the user can still download the image from the Releases page
      // and choose the file (the sha256 gate applies to it the same way).
      let bytes;
      try {
        const r = await fetch(`images/${pin.file}`, { cache: "no-store", credentials: "omit", referrerPolicy: "no-referrer" });
        if (!r.ok) throw new Refusal("image-missing", `This site does not have the image (${r.status}).`, IMAGE_FALLBACK, false);
        bytes = new Uint8Array(await r.arrayBuffer());
      } catch (e) {
        if (e instanceof Refusal) throw e;
        throw new Refusal("image-missing", `The page could not download the image from this site (${e && e.name}: ${e && e.message}).`,
          IMAGE_FALLBACK, false);
      }
      await setImage(bytes, `this site, ${pin.file}`);
    }));
    box.append(b);
  }
  $("image-file").addEventListener("change", (ev) => step(async () => {
    const f = ev.target.files && ev.target.files[0];
    if (!f) return;
    await setImage(new Uint8Array(await f.arrayBuffer()), `file ${f.name}`);
  }));
  $("goal-userdebug").addEventListener("click", chooseUserdebug);
  $("goal-mainline").addEventListener("click", chooseMainline);
  $("drivecopy-source").addEventListener("click", driveChooseSource);
  $("drivecopy-dir").addEventListener("click", driveChooseDir);
  $("drivecopy-run").addEventListener("click", driveRun);
  $("unlock-show").addEventListener("click", showUnlock);
  if (STEP2_USB_ENABLED) {
    $("step2-folder").addEventListener("change", (ev) => {
      step2Folder(ev).catch((e) => {
        setText("step2-folder-status", `Could not read the folder: ${e && e.message}`, "warn");
        render();
      });
    });
    $("step2-connect").addEventListener("click", step2Send);
  }
  $("connect").addEventListener("click", () => step(connectUnit, NOTE.read));
  $("confirm").addEventListener("change", render);
  $("write-bcb").addEventListener("click", () => step(writeAndRestart, NOTE.write));
  $("connect-fastboot").addEventListener("click", () => step(connectFastbootClick, NOTE.read));
  $("boot").addEventListener("click", () => step(boot, NOTE.memory));
  $("serial-open").addEventListener("click", openShell);
  $("save-log").addEventListener("click", saveLog);
  $("stop-save-log").addEventListener("click", saveLog);
  $("term-form").addEventListener("submit", (ev) => {
    ev.preventDefault();
    const v = $("term-input").value;
    $("term-input").value = "";
    if (S.shell) S.shell.send(v).catch((e) => log(`serial send: ${e && e.message}`));
  });

  if (navigator.usb) {
    navigator.usb.addEventListener("disconnect", (ev) => {
      log(`USB disconnect ${ev.device.vendorId.toString(16)}:${ev.device.productId.toString(16)}`);
      if (STEP2_USB_ENABLED && isStep2Device(ev.device)) S2.link = null;
    });
    // A unit the page may already see comes back in fastboot: catch Maskrom at
    // once, else mark the fastboot step ready (the watch does the same by poll).
    navigator.usb.addEventListener("connect", (ev) => {
      const d = ev.device;
      log(`USB connect ${d.vendorId.toString(16)}:${d.productId.toString(16)}`);
      // The step-2 transport is back (after the seal, or a replug): the
      // status watch opens it at its next tick; nothing else here.
      if (STEP2_USB_ENABLED && isStep2Device(d)) return;
      // A unit that comes back in Maskrom stops the page at once.
      try {
        classifyDevice(d);
      } catch (e) {
        if (e instanceof Refusal && e.code === "maskrom") {
          stop(e);
          render();
          return;
        }
      }
      // The fastboot device is here: signal readiness, but do not connect on
      // our own — the user presses "Connect to fastboot" (review: don't
      // auto-connect without the user).
      if (S.phase === "restarted" && DEVICE_USE_APPROVED && d.vendorId === FB_VID && d.productId === FB_PID) {
        markFastbootReady();
      }
    });
  }
  window.addEventListener("beforeunload", (ev) => {
    if (S.busy || ["download-checked", "restarted", "fastboot-checked"].includes(S.phase) || S2.phase === "sending") {
      ev.preventDefault();
      ev.returnValue = "";
    }
  });
  render();
}

init();
