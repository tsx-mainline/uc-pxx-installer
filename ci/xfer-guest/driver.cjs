// SPDX-License-Identifier: GPL-2.0-or-later
// The driver of the guest test (README.md), as root in the guest: it starts the real uc-pxx-xfer (run, as root; the
// receiver drops to its own user), serves the real page (web/site) on 127.0.0.1:8137, and drives headless Chromium.
// The page's config.js is patched in flight only to turn the step-2 section on and to pin the payload's manifest.
// The driver also plays the root step-2 service where C2 is not built yet: the seal (check-ready, the rename, root
// ownership, read-only) and the info file of the status-only mode.
"use strict";
const fs = require("fs");
const path = require("path");
const crypto = require("crypto");
const { spawn, execFileSync } = require("child_process");
const { chromium } = require(fs.readFileSync("/npm-root-g", "utf8").trim() + "/playwright");

const PHASE = process.argv[2] || "main";
const OUT = "/xg/out";
const XF = "/xg/bin/uc-pxx-xfer";
const STAGE = "/run/uc-pxx-step2/stage";
const XD = STAGE + "/xfer";
const CFS = "/sys/kernel/config/usb_gadget/uc-pxx-step2";
const INFO = "/run/uc-pxx-step2/xfer-info.json";
const PAY = "/payload";
const MAN = fs.readFileSync(PAY + "/uc-pxx-mainline/manifest.json");
const PIN = crypto.createHash("sha256").update(MAN).digest("hex");
const M = JSON.parse(MAN);
const SIZES = { manifest: MAN.length, template: M.boot_template.size, rootfs: M.rootfs.size };
const ROOTFS0 = SIZES.manifest + SIZES.template; // the page's progress value where the rootfs starts
const TOTAL = ROOTFS0 + SIZES.rootfs;
const MiB = 1048576;
const IDLE = 8; // seconds (--idle of the receiver in this test)
const R = { phase: PHASE, pin: PIN, sizes: SIZES, checks: [], cuts: [], timings: {} };
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
function console0(m) {
  try {
    fs.writeFileSync("/dev/ttyS0", m + "\n");
  } catch {}
}

// ---- the receiver ----
let xf = null, xfLog = 0, xfN = 0;
async function startXfer(args) {
  xfN++;
  const log = fs.openSync(`${OUT}/receiver-${PHASE}-${xfN}.log`, "w");
  xf = spawn(XF, ["run", "--phy", "none", ...args], { stdio: ["ignore", log, log] });
  xf.exited = new Promise((r) => xf.on("exit", (c, s) => r({ c, s })));
  for (let i = 0; i < 100; i++) {
    if (sh(`cat ${CFS}/UDC 2>/dev/null`).length > 0 && !sh(`cat ${CFS}/UDC`).startsWith("ERR")) break;
    await sleep(100);
  }
  await sleep(800);
  say(`uc-pxx-xfer run ${args.join(" ")}: UDC ${sh(`cat ${CFS}/UDC`)}`);
}
async function stopXfer() {
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
async function newPage(statusEvery = 0.5) {
  const p = await browser.newPage();
  p.on("console", (m) => fs.appendFileSync(`${OUT}/page-console-${PHASE}.log`, m.text() + "\n"));
  await p.route("**/js/config.js", async (route) => {
    const r = await route.fetch();
    let b = await r.text();
    const rep = (a, z) => {
      if (!b.includes(a)) throw new Error("config.js changed: " + a);
      b = b.replace(a, z);
    };
    rep("export const STEP2_USB_ENABLED = false;", "export const STEP2_USB_ENABLED = true;");
    rep("PAYLOADS: Object.freeze([]),", `PAYLOADS: Object.freeze([{ label: "the normal payload of the guest test", manifest_sha256: "${PIN}" }]),`);
    rep("STATUS_EVERY_S: 2,", `STATUS_EVERY_S: ${statusEvery},`);
    await route.fulfill({ response: r, body: b });
  });
  await p.goto("http://127.0.0.1:8137/index.html");
  await p.waitForFunction(() => document.body.dataset.step2 === "idle", null, { timeout: 20000 });
  return p;
}
const phase = (p) => p.evaluate(() => document.body.dataset.step2).catch(() => "closed");
async function waitPhase(p, want, ms) {
  const end = Date.now() + ms;
  for (;;) {
    const ph = await phase(p);
    if ((Array.isArray(want) ? want : [want]).includes(ph)) return ph;
    if (Date.now() > end) return `timeout (${ph})`;
    await sleep(50);
  }
}
const progress = (p) => p.evaluate(() => Number(document.getElementById("step2-progress").value)).catch(() => -1);
const statusText = (p) => p.evaluate(() => document.getElementById("step2-status").textContent).catch(() => "");
const pageLog = (p) => p.evaluate(() => document.getElementById("log").textContent).catch(() => "");
async function pick(p) {
  await p.setInputFiles("#step2-folder", PAY);
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

// ---- the phases ----
async function main() {
  // T0: the descriptors a Windows host reads (MS OS 1.0, BOS) and the interface, before the browser opens the device
  await startXfer(["--pin", PIN, "--idle", `${IDLE}s`]);
  const d = JSON.parse(sh("python3 /xg/msos.py 1d6b:0105"));
  R.descriptors = d;
  R.lsusb = sh("lsusb -v -d 1d6b:0105 2>/dev/null | head -80");
  check("msos string 0xEE = MSFT100 + vendor code 0xcd", d.string_ee_text === "MSFT100" && d.string_ee_vendor_code === "0xcd", d.string_ee);
  check("msos ext compat id = WINUSB", d.ext_compat_id === "WINUSB", d.ext_compat);
  check("msos ext property DeviceInterfaceGUID (REG_SZ) with the GUID", /DeviceInterfaceGUID/.test(d.ext_prop_text || "") &&
    (d.ext_prop.hex || "").slice(28, 36) === "01000000" &&
    /6c1f7a0e-5d2b-4e8a-9f31-0b7c2e4d8a15/.test(d.ext_prop_text || ""), d.ext_prop_text || d.ext_prop);
  check("BOS served (WebUSB platform capability)", (d.bos.hex || "").includes("38b60834a909a0478bfda0768815b665"), d.bos);
  check("interface ff/50/01, two bulk endpoints, serial UC-PXX-STEP2", /bInterfaceClass\s+255/.test(R.lsusb) &&
    /bInterfaceSubClass\s+80/.test(R.lsusb) && /bInterfaceProtocol\s+1/.test(R.lsusb) && /iSerial\s+\d+ UC-PXX-STEP2/.test(R.lsusb) &&
    (R.lsusb.match(/Transfer Type\s+Bulk/g) || []).length === 2);
  check("a vendor IN and OUT request to the interface are stalled (review F1)", /Broken pipe|Errno 32/.test(JSON.stringify(d.vendor_in_iface)) &&
    /Broken pipe|Errno 32/.test(JSON.stringify(d.vendor_out_iface)), { in: d.vendor_in_iface, out: d.vendor_out_iface, status: d.get_status_iface });
  await sleep(500);
  check("the receiver lives on after the requests (review F1)", xf && xf.exitCode === null && sh(`ps -o pid= -C uc-pxx-xfer | wc -l`) === "2",
    sh(`tail -5 ${OUT}/receiver-${PHASE}-${xfN}.log`));
  const id = sh(`ps -o user=,uid= -C uc-pxx-xfer`);
  R.processes = id;
  check("the receiver runs as uc-pxx-xfer, the supervisor as root", /uc-pxx-xfer|990/.test(id) && /root/.test(id), id);
  const ep0 = sh("stat -c '%u:%g %a' /run/uc-pxx-xfer/ffs/ep0 /run/uc-pxx-xfer/ffs");
  check("FunctionFS mounted for the receiver's user (ep0 990:990 0600, the mount 0500)", ep0 === "990:990 600\n990:990 500", ep0);

  server = spawn("node", ["/xg/test/serve.mjs"], { stdio: "ignore" });
  await sleep(800);
  browser = await chromium.launch({ channel: "chromium", headless: true });
  R.chromium = browser.version();
  let p = await newPage();
  check("the page accepts the browser", await p.evaluate(() => document.body.dataset.browser) === "ok", await p.evaluate(() => document.getElementById("browser-status").textContent));
  check("the page sees the device by the policy", await waitVisible(p, 10000));

  // T0b: a protocol violation halts both endpoints; the host's next transfer fails at once (no hang); SET_INTERFACE resyncs (review N5)
  const halt = await p.evaluate(async () => {
    const out = {};
    const d = (await navigator.usb.getDevices()).find((x) => x.vendorId === 0x1d6b && x.productId === 0x0105);
    await d.open();
    if (!d.configuration) await d.selectConfiguration(1);
    await d.claimInterface(0);
    await d.selectAlternateInterface(0, 0);
    const bad = new Uint8Array(100).fill(0x41);
    out.bad = (await d.transferOut(1, bad)).status;
    const hello = new TextEncoder().encode('PXX1\x01\x00\x00\x00\x02\x00\x00\x00\x07\x00\x00\x00{}');
    try { out.after = (await d.transferOut(1, hello)).status; } catch (e) { out.after = String(e); }
    await d.selectAlternateInterface(0, 0);
    out.resync = (await d.transferOut(1, hello)).status;
    const r = await d.transferIn(2, 66064);
    out.reply = new TextDecoder().decode(new Uint8Array(r.data.buffer).subarray(16));
    await d.close();
    return out;
  });
  check("a bad frame halts the endpoints (the next transfer stalls at once), SET_INTERFACE resyncs, HELLO answers",
    halt.bad === "ok" && halt.after === "stall" && halt.resync === "ok" && /"state":"waiting-payload"/.test(halt.reply), halt);

  // T1: the unit's gate does not pin the payload: refused before any byte
  await stopXfer();
  await startXfer(["--pin", "0".repeat(64), "--idle", `${IDLE}s`]);
  await waitVisible(p, 10000);
  check("folder picked", (await pick(p)) === "folder-ok", await p.evaluate(() => document.getElementById("step2-folder-status").textContent));
  await p.click("#step2-connect");
  const r1 = await waitPhase(p, "error", 30000);
  check("unit pins refuse the payload before any byte", r1 === "error" && /does not take this payload/.test(await statusText(p)) &&
    incoming().length === 0, await statusText(p));
  await stopXfer();
  await startXfer(["--pin", PIN, "--idle", `${IDLE}s`]);
  await waitVisible(p, 10000);

  // T2: the cut sweep: UDC unbind at many offsets (the supervisor binds it again: a re-plug)
  const cuts = [["manifest end", SIZES.manifest], ["template +1 MiB", SIZES.manifest + 1 * MiB], ["template +17 MiB", SIZES.manifest + 17 * MiB],
    ["template end - 1 MiB", ROOTFS0 - 1 * MiB], ["rootfs +1 MiB", ROOTFS0 + 1 * MiB], ["rootfs +64 MiB", ROOTFS0 + 64 * MiB],
    ["rootfs +256 MiB", ROOTFS0 + 256 * MiB], ["rootfs +512 MiB", ROOTFS0 + 512 * MiB], ["rootfs +768 MiB", ROOTFS0 + 768 * MiB],
    ["rootfs +1000 MiB", ROOTFS0 + 1000 * MiB], ["rootfs end - 8 MiB", TOTAL - 8 * MiB]];
  for (const [name, at] of cuts) {
    const ph0 = await phase(p);
    if (!["folder-ok", "error"].includes(ph0)) {
      check(`cut ${name}: the page is ready`, false, ph0);
      continue;
    }
    await p.click("#step2-connect");
    await waitPhase(p, "sending", 30000);
    const got = await waitProgress(p, at, 120000);
    const before = { dir: xdir(), state: xstate() };
    fs.writeFileSync(`${CFS}/UDC`, "\n");
    const ph = await waitPhase(p, "error", 30000);
    await sleep(600);
    const after = { dir: xdir(), state: xstate() };
    const text = await statusText(p);
    const ok = got >= at && ph === "error" && after.dir.every((x) => x === "state.json") && after.state.state === "waiting-payload" &&
      /Nothing was written/.test(text);
    R.cuts.push({ name, at, got, before, after, phase: ph, text, ok });
    check(`cut ${name} at ${got}: no folder left, nothing complete, the cut text`, ok, { got, after: after.dir, state: after.state.detail });
    check(`cut ${name}: the device comes back`, await waitVisible(p, 15000));
  }

  // T3: the tab is closed during the rootfs; no USB event: the idle timer removes the folder and drops the link
  await p.click("#step2-connect");
  await waitPhase(p, "sending", 30000);
  await waitProgress(p, ROOTFS0 + 128 * MiB, 120000);
  const inc3 = incoming();
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
  const st3 = xstate();
  R.timings.tabCloseFolderGoneS = gone;
  check(`tab close: the partial folder is removed by the idle timer (${IDLE} s)`, inc3.length === 1 && gone >= IDLE - 1 && gone <= IDLE + 4 &&
    /idle/.test(st3.detail || ""), { before: inc3, gone, state: st3 });
  const rl = sh(`grep -c "idle: the gadget was taken off" ${OUT}/receiver-${PHASE}-${xfN}.log`);
  check("tab close: the supervisor dropped the link (unbind) and the device comes back", Number(rl) >= 1 && await (async () => {
    const q = await newPage();
    const v = await waitVisible(q, 15000);
    await q.close();
    return v;
  })(), rl);

  // T4: page A closed during the rootfs and page B at once: B's SET_INTERFACE ends A's stale session; B sends the whole payload
  let a = await newPage();
  await waitVisible(a, 15000);
  await pick(a);
  await a.click("#step2-connect");
  await waitPhase(a, "sending", 30000);
  await waitProgress(a, ROOTFS0 + 256 * MiB, 120000);
  const aDir = incoming();
  await a.close();
  p = await newPage();
  await waitVisible(p, 15000);
  await pick(p);
  const tB = Date.now();
  await p.click("#step2-connect");
  let staleGone = -1;
  for (let i = 0; i < 400; i++) {
    const inc = incoming();
    if (aDir.length && !inc.includes(aDir[0])) {
      staleGone = (Date.now() - tB) / 1000;
      break;
    }
    await sleep(25);
  }
  R.timings.staleSessionGoneS = staleGone;
  check(`resync: page B's SET_INTERFACE ends page A's stale session (${aDir.join(",")}) at once`, staleGone >= 0 && staleGone < IDLE - 2, staleGone);
  const tSend = Date.now();
  const r4 = await waitPhase(p, ["sent", "error"], 600000);
  const sendS = (Date.now() - tSend) / 1000;
  R.timings.fullSendS = sendS;
  R.timings.fullMBps = TOTAL / 1e6 / sendS;
  const lg = await pageLog(p);
  fs.writeFileSync(`${OUT}/page-log-${PHASE}-full.txt`, lg);
  R.timings.pageLines = lg.split("\n").filter((l) => /sent \(|payload complete/.test(l));
  check(`the whole payload (${TOTAL} bytes) sent and committed`, r4 === "sent", { phase: r4, sendS, MBps: R.timings.fullMBps, text: await statusText(p) });
  const stC = xstate();
  check("the receiver: payload-complete, the manifest pinned", stC.state === "payload-complete" && stC.manifest_sha256 === PIN, stC);
  check("the stage holds only the record, the ready folder and the state", JSON.stringify(xdir()) === JSON.stringify(["complete", "complete.json", "state.json"]), xdir());
  const cr = sh(`${XF} check-ready --pin ${PIN}`);
  R.checkReady = cr;
  check("check-ready (internal/xfer CheckReady as root) accepts the hand-over", !cr.startsWith("ERR") && /"auto": false/.test(cr), cr.slice(0, 400));
  const sums = sh(`cd ${XD}/complete && sha256sum manifest.json boot-template.img rootfs.ext4`);
  R.readySums = sums;
  check("the ready folder's files have the manifest's sha256s", sums.includes(PIN + "  manifest.json") &&
    sums.includes(M.boot_template.sha256 + "  boot-template.img") && sums.includes(M.rootfs.sha256 + "  rootfs.ext4"), sums);
  check("the ready files belong to the receiver's user, 0600", /^-rw------- 1 uc-pxx-xfer/.test(sh(`ls -l ${XD}/complete/rootfs.ext4`)),
    sh(`ls -ln ${XD}/complete`));
  // a second page cannot add or change anything now (PUT refused: complete)
  // T5: the seal as the step-2 service will do it (C2), then the status-only receiver and the page across the re-enumeration
  const stop = await stopXfer();
  check("uc-pxx-xfer stops on SIGTERM (exit 0) and takes the gadget apart", stop && stop.c === 0 && sh(`ls ${CFS} 2>&1`).includes("No such file"), { stop, cfs: sh(`ls ${CFS} 2>&1`) });
  sh(`mkdir -p ${STAGE}/payload && mv ${XD}/complete ${STAGE}/payload/uc-pxx-mainline && chown -R root:root ${STAGE}/payload && ` +
    `chmod 0444 ${STAGE}/payload/uc-pxx-mainline/* && chmod 0555 ${STAGE}/payload/uc-pxx-mainline`);
  const info = (step2, lines) => fs.writeFileSync(INFO, JSON.stringify({ model: "UC-P10", slot: "b", token: "pxx-inst-test01", step2, lines,
    serial: "MUST-NOT-PASS", mac: "02:00:00:00:00:01" }));
  info("confirm", ["hold OK for 3 s on the unit"]);
  await startXfer(["--status-only", "--info", INFO]);
  const seen = [];
  for (const [s2, line] of [["confirm", "waiting for the person"], ["started 0123456789ab", "disable B"], ["writing system_b", "system_b 40%"],
    ["armed 0123456789ab", "armed"], ["restarting", "sync; reboot"]]) {
    info(s2, [line]);
    const end = Date.now() + 15000;
    let txt = "";
    while (Date.now() < end) {
      txt = await statusText(p);
      const lines = await p.evaluate(() => document.getElementById("step2-lines").textContent).catch(() => "");
      if (lines.includes(line)) break;
      await sleep(100);
    }
    seen.push({ s2, txt });
  }
  R.statusOnly = seen;
  const r5 = await waitPhase(p, "done", 20000);
  check("the page follows the status-only transport after the seal to restarting (done)", r5 === "done" &&
    /restarts into the new slot/.test(await statusText(p)), seen);
  const lg2 = await pageLog(p);
  fs.writeFileSync(`${OUT}/page-log-${PHASE}-end.txt`, lg2);
  check("the status-only info never passes the serial or the MAC", !/MUST-NOT-PASS|02:00:00:00:00:01/.test(lg2 + JSON.stringify(seen)));
  check("one requestDevice never needed (the policy's permission, kept across re-enumerations)", !/no device chosen/.test(lg2));
  check("the sealed payload is intact after the status-only run", sh(`cd ${STAGE}/payload/uc-pxx-mainline && sha256sum -c - <<EOF
${PIN}  manifest.json
${M.boot_template.sha256}  boot-template.img
${M.rootfs.sha256}  rootfs.ext4
EOF`).split("\n").filter((l) => / OK$/.test(l)).length === 3);
  await stopXfer();
  // T6: the receiver wrote only into the tmpfs stage (the 9p root is checked from the host: out/rootwrites-main.txt)
  R.stage = sh(`find ${STAGE} -ls`);
  R.mounts = sh("cat /proc/mounts");
}

async function powercut() {
  await startXfer(["--pin", PIN, "--idle", `${IDLE}s`]);
  server = spawn("node", ["/xg/test/serve.mjs"], { stdio: "ignore" });
  await sleep(800);
  browser = await chromium.launch({ channel: "chromium", headless: true });
  const p = await newPage();
  await waitVisible(p, 10000);
  await pick(p);
  await p.click("#step2-connect");
  await waitPhase(p, "sending", 30000);
  const at = await waitProgress(p, ROOTFS0 + 400 * MiB, 300000);
  R.cutAt = at;
  R.before = { dir: xdir(), state: xstate() };
  save();
  sh("sync");
  say(`power cut at ${at}`);
  console0(`XG-POWERCUT-NOW ${at}`);
  await sleep(600000); // the host kills the VM
}

async function afterPowercut() {
  const before = JSON.parse(fs.readFileSync(`${OUT}/results-powercut.json`, "utf8"));
  R.powercutBefore = before.before;
  check("before the cut: a partial folder in the RAM stage", (before.before.dir || []).some((x) => x.startsWith("incoming.")), before.before);
  R.stage = sh(`ls -A ${STAGE}`);
  check("after the cut: the stage (a new tmpfs) is empty", R.stage === "", R.stage);
  const found = sh("find / -xdev \\( -name 'incoming.*' -o -name complete.json -o -name rootfs.ext4 -o -name boot-template.img \\) -print 2>/dev/null");
  check("after the cut: no staged file anywhere on the root file system", found === "", found);
  await startXfer(["--pin", PIN, "--idle", `${IDLE}s`]);
  check("after the cut: the receiver starts clean (waiting-payload)", xstate().state === "waiting-payload", xstate());
  await stopXfer();
}

(async () => {
  try {
    if (PHASE === "main") await main();
    else if (PHASE === "powercut") await powercut();
    else if (PHASE === "after-powercut") await afterPowercut();
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
  await stopXfer();
  process.exit(R.ok ? 0 : 1);
})();
