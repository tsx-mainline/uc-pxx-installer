// SPDX-License-Identifier: GPL-2.0-or-later
// The finish phases of the guest test (README.md; mainline-rc-4, owner Q-T8 = option 5): the permanent USB interface
// of a normal mainline build (`uc-pxx-xfer usb`, internal/xfer RunUSB) receives the PINNED finish set from the real page
// (js/finishusb.js; config.js patched in flight only to turn the finish section on and to pin the guest's list), checks
// and seals it in /run/uc-pxx-finish, and starts the sealed executor once per boot. The guest's own list
// /usr/share/uc-pxx-usb/finish.json names the amd64 finish executor (the release recipe) and the real r2 (aarch64: only
// bytes here, never run). The guest has no eMMC: the executor must end with a refusal and write nothing.
"use strict";
const fs = require("fs");
const crypto = require("crypto");
const { spawn, execFileSync } = require("child_process");
const { chromium } = require(fs.readFileSync("/npm-root-g", "utf8").trim() + "/playwright");

const PHASE = process.argv[2] || "finish";
const OUT = "/xg/out";
const XF = "/xg/bin/uc-pxx-xfer";
const CFS = "/sys/kernel/config/usb_gadget/uc-pxx-step2";
const LIST = "/usr/share/uc-pxx-usb/finish.json";
const STAGE = "/run/uc-pxx-usb/stage";
const XD = STAGE + "/xfer";
const SEALED = "/run/uc-pxx-finish";
const STARTED = "/run/uc-pxx-usb/finish-started";
const FLOG = "/var/log/uc-pxx-finish.log";
const SRC = "/tmp/fset/uc-pxx-finish"; // the page's folder (tmpfs)
const WRONG = "/tmp/fwrong/uc-pxx-finish"; // another list (a space more): the unit's pin refuses it
const MiB = 1048576;
const IDLE = 8;
const R = { phase: PHASE, checks: [], cuts: [], timings: {} };
const t0 = Date.now();
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const say = (m) => console.log(`[${((Date.now() - t0) / 1000).toFixed(1)}] ${m}`);
function check(name, ok, detail) {
  R.checks.push({ name, ok: !!ok, detail: detail === undefined ? null : detail });
  say(`${ok ? "PASS" : "FAIL"} ${name}${detail !== undefined ? " " + JSON.stringify(detail) : ""}`);
}
const sh = (c) => {
  try {
    return execFileSync("sh", ["-c", c], { encoding: "utf8" }).trim();
  } catch (e) {
    return "ERR " + (e.stdout || "") + (e.stderr || "") + e.message;
  }
};
const save = () => fs.writeFileSync(`${OUT}/results-${PHASE}.json`, JSON.stringify(R, null, 1));
const sha = (b) => crypto.createHash("sha256").update(b).digest("hex");
function console0(m) {
  try {
    fs.writeFileSync("/dev/ttyS0", m + "\n");
  } catch {}
}

// ---- the finish set of the guest ----
const EXE = fs.readFileSync("/xg/bin/uc-pxx-step2-finish");
const R2 = fs.readFileSync("/xg/bin/uc-pxx-bootctl-inst");
const L = { kind: "uc-pxx-finish", version: 1, files: [{ name: "uc-pxx-step2-finish", size: EXE.length, sha256: sha(EXE) },
  { name: "uc-pxx-bootctl-inst", size: R2.length, sha256: sha(R2) }] };
const LB = Buffer.from(JSON.stringify(L) + "\n");
const LSHA = sha(LB);
const WB = Buffer.from(JSON.stringify(L) + " \n");
const WSHA = sha(WB);
R.list = { sha256: LSHA, size: LB.length, exe: L.files[0], r2: L.files[1], wrong: WSHA };
const TOTAL = LB.length + EXE.length + R2.length;
const EXE0 = LB.length, R20 = LB.length + EXE.length; // the page's progress where each file starts
function folders() {
  for (const [d, b] of [[SRC, LB], [WRONG, WB]]) {
    fs.mkdirSync(d, { recursive: true });
    fs.writeFileSync(`${d}/finish.json`, b);
    fs.writeFileSync(`${d}/uc-pxx-step2-finish`, EXE);
    fs.writeFileSync(`${d}/uc-pxx-bootctl-inst`, R2);
  }
}
function putList() {
  fs.mkdirSync("/usr/share/uc-pxx-usb", { recursive: true });
  fs.writeFileSync(LIST, LB, { mode: 0o644 });
  fs.chmodSync(LIST, 0o644);
}
const dropList = () => sh(`rm -f ${LIST}; rmdir /usr/share/uc-pxx-usb 2>/dev/null; true`);

// ---- uc-pxx-xfer usb ----
let xf = null, xfN = 0;
async function startUSB(extra = []) {
  xfN++;
  const log = fs.openSync(`${OUT}/usb-${PHASE}-${xfN}.log`, "w");
  xf = spawn(XF, ["usb", "--phy", "none", "--idle", `${IDLE}s`, ...extra], { stdio: ["ignore", log, log] });
  xf.exited = new Promise((r) => xf.on("exit", (c, s) => r({ c, s })));
  for (let i = 0; i < 100; i++) {
    const u = sh(`cat ${CFS}/UDC 2>/dev/null`);
    if (u.length > 0 && !u.startsWith("ERR")) break;
    await sleep(100);
  }
  await sleep(800);
  say(`uc-pxx-xfer usb ${extra.join(" ")}: UDC ${sh(`cat ${CFS}/UDC`)}`);
}
async function stopUSB() {
  if (!xf) return null;
  xf.kill("SIGTERM");
  const r = await Promise.race([xf.exited, sleep(15000).then(() => null)]);
  xf = null;
  await sleep(500);
  return r;
}
const xdir = () => sh(`ls -A ${XD} 2>&1`).split("\n").filter((x) => x).sort();
const xstate = () => {
  try {
    return JSON.parse(fs.readFileSync(`${XD}/state.json`, "utf8"));
  } catch (e) {
    return { error: String(e) };
  }
};
const incoming = () => xdir().filter((x) => x.startsWith("incoming."));

// ---- the page ----
let server = null, browser = null;
async function newPage() {
  const p = await browser.newPage();
  p.on("console", (m) => fs.appendFileSync(`${OUT}/page-console-${PHASE}.log`, m.text() + "\n"));
  await p.route("**/js/config.js", async (route) => {
    const r = await route.fetch();
    let b = await r.text();
    const rep = (a, z) => {
      if (!b.includes(a)) throw new Error("config.js changed: " + a);
      b = b.replace(a, z);
    };
    rep("export const FINISH_USB_ENABLED = false;", "export const FINISH_USB_ENABLED = true;");
    rep("export const FINISH = Object.freeze({ LISTS: Object.freeze([]), STATUS_EVERY_S: 2 });",
      `export const FINISH = Object.freeze({ LISTS: Object.freeze([{ label: "the guest's finish set", sha256: "${LSHA}" },` +
      ` { label: "another list (the unit refuses it)", sha256: "${WSHA}" }]), STATUS_EVERY_S: 0.5 });`);
    await route.fulfill({ response: r, body: b });
  });
  await p.goto("http://127.0.0.1:8137/index.html");
  await p.waitForFunction(() => document.body.dataset.finish === "idle", null, { timeout: 20000 });
  return p;
}
const phase = (p) => p.evaluate(() => document.body.dataset.finish).catch(() => "closed");
const outcome = (p) => p.evaluate(() => document.body.dataset.finishEnd).catch(() => "closed");
async function waitPhase(p, want, ms) {
  const end = Date.now() + ms;
  for (;;) {
    const ph = await phase(p);
    if ((Array.isArray(want) ? want : [want]).includes(ph)) return ph;
    if (Date.now() > end) return `timeout (${ph})`;
    await sleep(50);
  }
}
async function waitOutcome(p, ms) {
  const end = Date.now() + ms;
  for (;;) {
    const o = await outcome(p);
    if (o !== "none" && o !== "" && o !== "closed") return o;
    if (Date.now() > end) return `timeout (${o}, phase ${await phase(p)})`;
    await sleep(100);
  }
}
const progress = (p) => p.evaluate(() => Number(document.getElementById("finish-progress").value)).catch(() => -1);
const statusText = (p) => p.evaluate(() => document.getElementById("finish-status").textContent).catch(() => "");
const linesText = (p) => p.evaluate(() => document.getElementById("finish-lines").textContent).catch(() => "");
const pageLog = (p) => p.evaluate(() => document.getElementById("log").textContent).catch(() => "");
async function pick(p, dir) {
  await p.setInputFiles("#finish-folder", dir);
  return waitPhase(p, "folder-ok", 30000);
}
async function waitProgress(p, at, ms) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await progress(p);
    if (v >= at) return v;
    if (Date.now() > end) return -1;
    await sleep(2);
  }
}
async function visible(p) {
  return p.evaluate(async () => (await navigator.usb.getDevices()).some((d) => d.vendorId === 0x1d6b && d.productId === 0x0105))
    .catch(() => false);
}
async function waitVisible(p, ms) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    if (await visible(p)) return true;
    await sleep(100);
  }
  return false;
}
// rawHello: one HELLO by WebUSB from the page's origin (the reply JSON as the unit sends it)
async function rawHello(p) {
  return p.evaluate(async () => {
    const d = (await navigator.usb.getDevices()).find((x) => x.vendorId === 0x1d6b && x.productId === 0x0105);
    await d.open();
    if (!d.configuration) await d.selectConfiguration(1);
    await d.claimInterface(0);
    await d.selectAlternateInterface(0, 0);
    const eps = d.configuration.interfaces[0].alternates[0].endpoints;
    const epOut = eps.find((e) => e.direction === "out").endpointNumber, epIn = eps.find((e) => e.direction === "in").endpointNumber;
    const hello = new TextEncoder().encode('PXX1\x01\x00\x00\x00\x02\x00\x00\x00\x07\x00\x00\x00{}');
    await d.transferOut(epOut, hello);
    const r = await d.transferIn(epIn, 66064);
    const txt = new TextDecoder().decode(new Uint8Array(r.data.buffer).subarray(16));
    const ifs = d.configuration.interfaces.map((i) => [i.interfaceNumber, i.alternates[0].interfaceClass, i.alternates[0].interfaceSubclass,
      i.alternates[0].interfaceProtocol]);
    await d.close();
    return { txt, ifs };
  });
}
const functions = () => sh(`ls ${CFS}/functions 2>&1`).split("\n").filter((x) => x);
const configLinks = () => sh(`ls ${CFS}/configs/c.1 2>&1`).split("\n").filter((x) => /\./.test(x) && !/^(MaxPower|bmAttributes)$/.test(x));

async function browserUp() {
  server = spawn("node", ["/xg/test/serve.mjs"], { stdio: "ignore" });
  await sleep(800);
  browser = await chromium.launch({ channel: "chromium", headless: true });
  R.chromium = browser.version();
}

// ---- the phases ----
async function finish() {
  folders();
  dropList();
  // F0: no list on this slot: mode (a) status only; the gadget has only the vendor function (the USB network is off)
  await startUSB();
  await browserUp();
  let p = await newPage();
  check("the page sees the device by the policy", await waitVisible(p, 15000));
  let h = await rawHello(p);
  R.helloStatus = h;
  let hj = JSON.parse(h.txt);
  check("no finish list: HELLO mode status, status only, no pin", hj.mode === "status" && hj.status_only === true && hj.pins.length === 0, hj);
  check("HELLO never holds a serial, a MAC or a factory field", !/serial|mac|factory|UC-PXX-STEP2"/i.test(JSON.stringify(hj.info || {})) &&
    !/"serial"|"mac"/.test(h.txt), hj.info);
  const fn0 = functions();
  check("the USB network off: the gadget has only the vendor function (no ncm.*, no acm.*, no mass_storage.*)",
    fn0.length === 1 && fn0[0] === "ffs.uc-pxx-step2", fn0);
  check("one interface: 0 = ff/50/01", JSON.stringify(h.ifs) === JSON.stringify([[0, 255, 80, 1]]), h.ifs);
  await p.click("#xstatus-read");
  await sleep(1500);
  const xs = await p.evaluate(() => document.getElementById("xstatus-table").textContent + " | " + document.getElementById("xstatus-text").textContent);
  check("the page's status view reads the unit (mode status)", /status/.test(xs), xs);
  await p.close();
  await stopUSB();

  // F1: the slot's list: mode (c) finish, exactly one pin = the list's sha256
  putList();
  await startUSB();
  p = await newPage();
  await waitVisible(p, 15000);
  h = await rawHello(p);
  hj = JSON.parse(h.txt);
  R.helloFinish = hj;
  check("the slot's list: HELLO mode finish, waiting-finish, exactly one pin = the list's sha256",
    hj.mode === "finish" && hj.state === "waiting-finish" && hj.pins.length === 1 && hj.pins[0] === LSHA && hj.status_only === false, hj);
  check("the info says the finish may be sent", ((hj.info || {}).step2 || "") === "finish: waiting", hj.info);

  // F2: a list that is not the slot's: the page refuses before any byte (the unit's pin), nothing in the stage
  check("the other folder picked", (await pick(p, WRONG)) === "folder-ok", await p.evaluate(() => document.getElementById("finish-folder-status").textContent));
  await p.click("#finish-connect");
  const r2 = await waitPhase(p, "error", 30000);
  check("a list that is not the slot's: refused before any byte (the unit's pin)", r2 === "error" &&
    /not the one this unit's slot names/.test(await statusText(p)) && incoming().length === 0, { text: await statusText(p), dir: xdir() });

  // F3: the cut sweep (UDC unbind; the supervisor binds again: a re-plug)
  check("the guest's folder picked", (await pick(p, SRC)) === "folder-ok");
  const cuts = [["finish.json end", LB.length], ["executor +1", EXE0 + 1], ["executor +1 MiB", EXE0 + 1 * MiB],
    ["executor middle", EXE0 + Math.floor(EXE.length / 2)], ["executor end - 64 KiB", R20 - 65536], ["r2 +1", R20 + 1],
    ["r2 +256 KiB", R20 + 262144], ["r2 end - 4 KiB", TOTAL - 4096]];
  for (const [name, at] of cuts) {
    const ph0 = await phase(p);
    if (!["folder-ok", "error"].includes(ph0)) {
      check(`cut ${name}: the page is ready`, false, ph0);
      continue;
    }
    await p.click("#finish-connect");
    await waitPhase(p, "sending", 30000);
    const got = await waitProgress(p, at, 60000);
    fs.writeFileSync(`${CFS}/UDC`, "\n");
    const ph = await waitPhase(p, "error", 30000);
    await sleep(600);
    const after = { dir: xdir(), state: xstate() };
    const text = await statusText(p);
    const ok = got >= at && ph === "error" && after.dir.every((x) => x === "state.json") && after.state.state === "waiting-finish" &&
      /Nothing was installed or written/.test(text) && !fs.existsSync(SEALED) && !fs.existsSync(STARTED);
    R.cuts.push({ name, at, got, after, phase: ph, text, ok });
    check(`cut ${name} at ${got}: no folder left, nothing sealed, the cut text`, ok, { got, after: after.dir, state: after.state.detail, text });
    check(`cut ${name}: the device comes back`, await waitVisible(p, 15000));
  }

  // F4: the tab closed during the executor: the idle timer removes the folder and drops the link
  await p.click("#finish-connect");
  await waitPhase(p, "sending", 30000);
  await waitProgress(p, EXE0 + 2 * MiB, 60000);
  const inc4 = incoming();
  const tc = Date.now();
  await p.close();
  let gone = -1;
  for (let i = 0; i < 300; i++) {
    if (incoming().length === 0 && xdir().length > 0) {
      gone = (Date.now() - tc) / 1000;
      break;
    }
    await sleep(100);
  }
  R.timings.tabCloseFolderGoneS = gone;
  check(`tab close: the partial folder is removed by the idle timer (${IDLE} s)`, inc4.length === 1 && gone >= IDLE - 1 && gone <= IDLE + 4 &&
    /idle/.test(xstate().detail || ""), { before: inc4, gone, state: xstate() });

  // F5: the whole set: COMMIT answered, the seal, the start of the executor, the page follows to the end
  p = await newPage();
  await waitVisible(p, 15000);
  await pick(p, SRC);
  const tS = Date.now();
  await p.click("#finish-connect");
  const r5 = await waitPhase(p, ["watch", "error"], 120000);
  R.timings.sendS = (Date.now() - tS) / 1000;
  const lg = await pageLog(p);
  check("the whole set sent; the page got the COMMIT answer before the re-enumeration (reply first, then the root)", r5 === "watch" &&
    /the unit took the finish set/.test(lg), { phase: r5, text: await statusText(p) });
  const end5 = await waitOutcome(p, 90000);
  R.finishEnd = { outcome: end5, text: await statusText(p), lines: await linesText(p) };
  check("the page ends with the unit's result (refused or not a target: the guest has no eMMC)",
    ["refused", "not-target"].includes(end5), R.finishEnd);
  const sealed = sh(`stat -c '%n %u:%g %a %s' ${SEALED} ${SEALED}/* 2>&1`);
  const sums = sh(`cd ${SEALED} && sha256sum uc-pxx-step2-finish uc-pxx-bootctl-inst 2>&1`);
  R.sealed = { sealed, sums };
  check("the seal: /run/uc-pxx-finish (0700 root) holds exactly the two files, root 0700, with the list's sha256",
    sealed.split("\n").length === 3 && new RegExp(`^${SEALED} 0:0 700 `).test(sealed) && /uc-pxx-step2-finish 0:0 700 /.test(sealed) &&
    /uc-pxx-bootctl-inst 0:0 700 /.test(sealed) && sums.includes(`${L.files[0].sha256}  uc-pxx-step2-finish`) &&
    sums.includes(`${L.files[1].sha256}  uc-pxx-bootctl-inst`), R.sealed);
  const st = sh(`cat ${STARTED}`);
  check("finish-started: the executor was started once in this boot", /^started pid [0-9]+$/.test(st), st);
  const flog = sh(`cat ${FLOG}`);
  R.finishLog = flog.slice(-3000);
  check("the executor's log is there (it ran finish-service from the sealed folder)", /starts the sealed finish executor \/run\/uc-pxx-finish\/uc-pxx-step2-finish/.test(flog), R.finishLog);
  check("the receiver's stage has no ready folder left (status only now)", !xdir().includes("complete"), xdir());
  const ul = sh(`cat ${OUT}/usb-${PHASE}-${xfN}.log`);
  R.usbLog = ul.slice(-4000);
  check("uc-pxx-usb: sealed, started, then the status-only transport", /finish set sealed in \/run\/uc-pxx-finish/.test(ul) &&
    /the finish executor runs \(pid/.test(ul), R.usbLog.slice(-1500));
  check("no process of the receiver's user is left of the finish receiver (one status-only receiver now)",
    sh("ps -o user= -u 990 | wc -l") === "1", sh("ps -o pid=,user=,args= -u 990"));
  check("the finish set is in RAM only: nothing of it on the guest's root disk", sh(`find / -xdev \\( -path /xg -prune \\) -o \\( -name 'uc-pxx-step2-finish' -o -name 'incoming.*' \\) -print 2>/dev/null`) === "",
    sh(`find / -xdev \\( -path /xg -prune \\) -o \\( -name 'uc-pxx-step2-finish' -o -name 'incoming.*' \\) -print 2>/dev/null`));
  const lg5 = await pageLog(p);
  fs.writeFileSync(`${OUT}/page-log-${PHASE}-full.txt`, lg5);
  await p.close();

  // F6: once per boot: uc-pxx-usb started again in this boot takes nothing (status), the page sends nothing
  await stopUSB();
  await startUSB();
  p = await newPage();
  await waitVisible(p, 15000);
  hj = JSON.parse((await rawHello(p)).txt);
  check("a restart of uc-pxx-usb in the same boot: mode status (the executor was started in this boot)", hj.mode === "status", hj);
  await pick(p, SRC);
  await p.click("#finish-connect");
  const r6 = await waitPhase(p, ["error", "watch"], 30000);
  check("the page sends nothing to it", (r6 === "error" || r6 === "watch") && incoming().length === 0 && /Nothing was sent|follows the unit/.test(await statusText(p)),
    { phase: r6, text: await statusText(p) });
  await p.close();
  await stopUSB();

  // F7: the USB network setting on (--net): an NCM function after the vendor function; interface 0 stays the vendor one
  await startUSB(["--net"]);
  const fn1 = functions(), cl1 = configLinks();
  R.net = { functions: fn1, links: cl1, ncm: sh(`cat ${CFS}/functions/ncm.usb0/ifname ${CFS}/functions/ncm.usb0/dev_addr 2>&1`) };
  if (sh("modinfo usb_f_ncm >/dev/null 2>&1 && echo yes") !== "yes") {
    check("the guest kernel has usb_f_ncm (else the --net case is not run)", false, sh("modinfo usb_f_ncm 2>&1 | head -2"));
  } else {
    check("--net: the functions are ffs.uc-pxx-step2 and ncm.usb0 (no acm, no mass storage)",
      JSON.stringify(fn1.slice().sort()) === JSON.stringify(["ffs.uc-pxx-step2", "ncm.usb0"]), R.net);
    p = await newPage();
    await waitVisible(p, 15000);
    const hn = await rawHello(p);
    R.net.ifs = hn.ifs;
    check("--net: interface 0 is the vendor interface ff/50/01, then the NCM interfaces", JSON.stringify(hn.ifs[0]) === JSON.stringify([0, 255, 80, 1]) &&
      hn.ifs.length >= 2, hn.ifs);
    await p.close();
  }
  await stopUSB();
  check("after the stop: the gadget is taken apart", sh(`ls ${CFS} 2>&1`).includes("No such file"), sh(`ls ${CFS} 2>&1`));
  dropList();
}

async function finishPowercut() {
  folders();
  putList();
  await startUSB();
  await browserUp();
  const p = await newPage();
  await waitVisible(p, 15000);
  await pick(p, SRC);
  await p.click("#finish-connect");
  await waitPhase(p, "sending", 30000);
  const at = await waitProgress(p, EXE0 + 2 * MiB, 120000);
  R.cutAt = at;
  R.before = { dir: xdir(), state: xstate(), sealed: fs.existsSync(SEALED) };
  save();
  sh("sync");
  say(`power cut at ${at}`);
  console0(`XG-POWERCUT-NOW ${at}`);
  await sleep(600000); // the host kills the VM
}

async function afterFinishPowercut() {
  const before = JSON.parse(fs.readFileSync(`${OUT}/results-finish-powercut.json`, "utf8"));
  R.powercutBefore = before.before;
  check("before the cut: a partial folder in the RAM stage", (before.before.dir || []).some((x) => x.startsWith("incoming.")), before.before);
  R.after = { stage: sh(`ls -A ${STAGE} 2>&1`), sealed: sh(`ls -A ${SEALED} 2>&1`), started: sh(`ls ${STARTED} 2>&1`) };
  check("after the cut: no stage, no sealed folder, no started mark (all were RAM)", /No such file/.test(R.after.stage) &&
    /No such file/.test(R.after.sealed) && /No such file/.test(R.after.started), R.after);
  const found = sh(`find / -xdev \\( -path /xg -prune \\) -o \\( -name 'incoming.*' -o -name 'uc-pxx-step2-finish' -o -name complete.json \\) -print 2>/dev/null`);
  check("after the cut: nothing of the set on the guest's root disk", found === "", found);
  folders();
  await startUSB();
  await browserUp();
  const p = await newPage();
  await waitVisible(p, 15000);
  const hj = JSON.parse((await rawHello(p)).txt);
  check("after the cut: uc-pxx-usb starts clean in the finish mode (a new boot)", hj.mode === "finish" && hj.state === "waiting-finish", hj);
  await p.close();
  await stopUSB();
  dropList();
}

(async () => {
  try {
    if (PHASE === "finish") await finish();
    else if (PHASE === "finish-powercut") await finishPowercut();
    else if (PHASE === "after-finish-powercut") await afterFinishPowercut();
  } catch (e) {
    check("the driver ran without an exception", false, String(e && e.stack));
  }
  R.ok = R.checks.length > 0 && R.checks.every((c) => c.ok);
  R.seconds = (Date.now() - t0) / 1000;
  save();
  say(`RESULT ${R.ok ? "PASS" : "FAIL"}: ${R.checks.filter((c) => c.ok).length}/${R.checks.length}`);
  try {
    if (browser) await browser.close();
  } catch {}
  if (server) server.kill();
  await stopUSB();
  process.exit(R.ok ? 0 : 1);
})();
