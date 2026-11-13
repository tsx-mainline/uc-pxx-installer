// SPDX-License-Identifier: GPL-2.0-or-later
// guest.cjs (tests only; root in the guest of test/guest/run.sh): the real uc-pxx-xfer as the unit's transfer
// interface on dummy_hcd, and uc-pxx-webdrive's driver as the host, each driver run in a namespace made as the tool's
// container: a private mount and network namespace (loopback only), no udevd, a /dev/bus/usb tmpfs with only the one
// usbfs node of the gadget (none in a dry run), uid 1000 with the node's group 46, read-only binds of the tool, its
// modules, the page copy, the set and the policy. The checks are printed (PASS/FAIL) and saved to results.json.
"use strict";
const fs = require("fs");
const crypto = require("crypto");
const { spawn, execFileSync } = require("child_process");

const OUT = "/wd/out";
const XF = "/xg/bin/uc-pxx-xfer";
const STAGE = "/run/uc-pxx-step2/stage";
const XD = STAGE + "/xfer";
const CFS = "/sys/kernel/config/usb_gadget/uc-pxx-step2";
const INFO = "/run/uc-pxx-step2/xfer-info.json";
const LIST = "/usr/share/uc-pxx-usb/finish.json";
const PINS = JSON.parse(fs.readFileSync("/wd/tool/pins.json"));
const PIN_EU = PINS.payload.files[0][2];
const R = { checks: [], runs: [] };
const t0 = Date.now();
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const say = (m) => console.log(`[${((Date.now() - t0) / 1000).toFixed(1)}] ${m}`);
function check(name, ok, detail) {
  R.checks.push({ name, ok: !!ok, detail: detail === undefined ? null : detail });
  say(`${ok ? "PASS" : "FAIL"} ${name}${detail !== undefined ? " " + JSON.stringify(detail).slice(0, 600) : ""}`);
}
const sh = (c) => {
  try {
    return execFileSync("sh", ["-c", c], { encoding: "utf8" }).trim();
  } catch (e) {
    return "ERR " + (e.stdout || "") + (e.stderr || "") + e.message;
  }
};
const save = () => fs.writeFileSync(`${OUT}/results.json`, JSON.stringify(R, null, 1));

// ---- the receiver ----
let xf = null, xfN = 0;
async function startXfer(sub, args) {
  xfN++;
  const log = fs.openSync(`${OUT}/receiver-${xfN}.log`, "w");
  xf = spawn(XF, [sub, "--phy", "none", ...args], { stdio: ["ignore", log, log] });
  xf.exited = new Promise((r) => xf.on("exit", (c, s) => r({ c, s })));
  for (let i = 0; i < 100; i++) {
    const u = sh(`cat ${CFS}/UDC 2>/dev/null`);
    if (u.length > 0 && !u.startsWith("ERR")) break;
    await sleep(100);
  }
  await sleep(1500);
  say(`uc-pxx-xfer ${sub} ${args.join(" ")}: UDC ${sh(`cat ${CFS}/UDC`)}`);
}
async function stopXfer() {
  if (!xf) return;
  xf.kill("SIGTERM");
  await Promise.race([xf.exited, sleep(15000)]);
  xf = null;
  await sleep(800);
}
const info = (step2, lines, slot = "b") => fs.writeFileSync(INFO, JSON.stringify({ model: "UC-P10", slot, token: "ucpxx-5a454728", step2, lines }));
const xstate = () => {
  try {
    return JSON.parse(fs.readFileSync(`${XD}/state.json`, "utf8"));
  } catch (e) {
    return { error: String(e) };
  }
};

// ---- the gadget's usbfs node (sysfs: what the host tool reads) ----
function gadget() {
  const out = [];
  for (const n of fs.readdirSync("/sys/bus/usb/devices")) {
    if (n.includes(":")) continue;
    const d = `/sys/bus/usb/devices/${n}`;
    const r = (a) => {
      try {
        return fs.readFileSync(`${d}/${a}`, "utf8").trim();
      } catch {
        return null;
      }
    };
    if (r("idVendor") === "1d6b" && r("idProduct") === "0105") out.push({ n, bus: +r("busnum"), dev: +r("devnum"), serial: r("serial") });
  }
  return out;
}

// ---- one driver run, as the tool's container makes it ----
let runN = 0;
async function wd(mode, opts = {}) {
  // the gadget comes back some time after the receiver starts: wait for it (at most 15 s)
  for (let i = 0; i < 75 && gadget().length !== 1; i++) await sleep(200);
  return wd1(mode, opts);
}
function wd1(mode, { dry = false, pins = null } = {}) {
  runN++;
  const g = gadget();
  const run = `${OUT}/run-${runN}-${mode}${dry ? "-dry" : ""}`;
  fs.mkdirSync(`${run}/scratch`, { recursive: true });
  sh(`chown -R 1000:1000 ${run}`);
  if (g.length !== 1) {
    say(`driver run ${runN}: ${g.length} gadgets: not run`);
    return Promise.resolve({ rc: -1, why: `${g.length} gadgets` });
  }
  const { bus, dev } = g[0];
  const B = String(bus).padStart(3, "0"), D = String(dev).padStart(3, "0"), minor = (bus - 1) * 128 + (dev - 1);
  const node = dry ? "" : `mkdir -p /dev/bus/usb/${B}; mknod -m 0660 /dev/bus/usb/${B}/${D} c 189 ${minor}; chgrp 46 /dev/bus/usb/${B}/${D};`;
  const args = ["--mode", mode].concat(dry ? ["--dry-run"] : [], pins ? ["--pins", pins] : []);
  const script = `set -e
mount --make-rprivate /
ip link set lo up
mount -t tmpfs -o mode=0755 usbfs-view /dev/bus/usb
${node}
ls -laR /dev/bus/usb > ${run}/dev-view.txt
ls /run/udev > ${run}/run-udev.txt 2>&1 || true
mount -t tmpfs -o size=1g tmpfs /tmp
for m in tool:/wd/tool site:/wd/site modules:/wd/modules finish:/wd/finish; do d=/\${m%%:*}; s=\${m#*:}; mount --bind "$s" "$d"; mount -o remount,ro,bind "$d"; done
mount --bind ${run} /out
mount --bind ${run}/scratch /scratch
mount --bind /wd/tool/policy.json /etc/opt/chrome_for_testing/policies/managed/uc-pxx-webdrive.json
mount -o remount,ro,bind /etc/opt/chrome_for_testing/policies/managed/uc-pxx-webdrive.json
exec setpriv --reuid=1000 --regid=1000 --groups=46 --no-new-privs env -i PATH=/usr/bin:/bin HOME=/tmp TMPDIR=/scratch PLAYWRIGHT_BROWSERS_PATH=/ms-playwright node /tool/driver.mjs ${args.join(" ")}
`;
  say(`driver run ${runN}: ${mode}${dry ? " (dry)" : ""}${pins ? ` pins ${pins}` : ""}; gadget bus ${bus} dev ${dev}`);
  return new Promise((ok) => {
    const p = spawn("unshare", ["--mount", "--net", "--propagation", "private", "bash", "-c", script], { stdio: ["ignore", "pipe", "pipe"] });
    const log = fs.openSync(`${run}/console.log`, "w");
    p.stdout.on("data", (b) => fs.writeSync(log, b));
    p.stderr.on("data", (b) => fs.writeSync(log, b));
    p.on("exit", (c) => {
      let r = null;
      try {
        r = JSON.parse(fs.readFileSync(`${run}/result.json`, "utf8"));
      } catch {}
      R.runs.push({ n: runN, mode, dry, rc: c, outcome: r && r.outcome, why: r && r.why });
      say(`driver run ${runN} exit ${c}: ${r && r.outcome} ${r && r.why}`);
      ok({ rc: c, r, run });
    });
  });
}

async function payloadPhase() {
  info("waiting-payload", ["Step 2 waits for its payload: send it from the installer page."]);
  await startXfer("run", ["--pin", PIN_EU, "--pin", PINS.expect.step2.pins_allowed[1], "--idle", "8s", "--info", INFO]);
  check("one gadget 1d6b:0105 on the guest's bus, serial UC-PXX-STEP2", gadget().length === 1 && gadget()[0].serial === "UC-PXX-STEP2", gadget());
  check("no udevd in the guest (/run/udev absent)", !fs.existsSync("/run/udev"));

  let x = await wd("status");
  check("status: exit 0", x.rc === 0, x.r && x.r.why);
  check("status: the page saw exactly the one device by the policy (no udevd, one node)", x.r && x.r.device && x.r.device.length === 1, x.r && x.r.device);
  check("status: the unit's HELLO (payload, waiting-payload, the eu pin)", x.r && x.r.status && x.r.status.hello.mode === "payload" &&
    x.r.status.hello.state === "waiting-payload" && x.r.status.hello.pins.includes(PIN_EU), x.r && x.r.status && x.r.status.hello);
  check("status: HELLO only", usbOut(x.run).every((f) => f.type === 1) && usbOut(x.run).length >= 1, usbOut(x.run));
  check("status: the view of /dev/bus/usb held one node", (fs.readFileSync(`${x.run}/dev-view.txt`, "utf8").match(/^crw/mg) || []).length === 1);

  const s0 = xstate().session;
  x = await wd("step2", { dry: true });
  check("step2 dry run: exit 0 (the page downloaded and checked the real payload)", x.rc === 0 && x.r.outcome === "dry-run", x.r && x.r.why);
  check("step2 dry run: the page still saw the device (sysfs) with no node in its view", x.r && x.r.device && x.r.device.length === 1);
  check("step2 dry run: no session on the receiver", xstate().session === s0, { before: s0, after: xstate().session });
  check("step2 dry run: no frame sent", usbOut(x.run).length === 0);
  check("step2 dry run: the payload served in full", x.r && x.r.served["/payload/uc-pxx-mainline/rootfs.ext4"] === PINS.payload.files[2][1], x.r && x.r.served);

  x = await wd("step2");
  check("step2: exit 0, the unit took the payload (COMMIT ok, auto)", x.rc === 0 && x.r.outcome === "payload-committed", x.r && x.r.why);
  check("step2: the unit's sha256 of each file is the pin", x.r && PINS.payload.files.every(([n, , h]) => x.r.unit_sha256[n] === h), x.r && x.r.unit_sha256);
  const cr = sh(`${XF} check-ready --pin ${PIN_EU}`);
  check("step2: the receiver's ready folder and record (check-ready), auto true", !cr.startsWith("ERR") && /"auto": true/.test(cr) &&
    cr.includes(PIN_EU), cr.slice(0, 600));
  check("step2: the transfer order HELLO (status view), HELLO, PUT x3, COMMIT", JSON.stringify(usbOut(x.run).slice(0, 6).map((f) => f.type)) === "[1,1,2,2,2,5]",
    usbOut(x.run).slice(0, 8));
  await stopXfer();
  sh(`${XF} cleanup`);

  info("confirm", ["Confirm step 2 on the unit's screen (hold OK 3 s)."]);
  await startXfer("run", ["--status-only", "--info", INFO]);
  x = await wd("status");
  check("after the seal (status only, a new device number): status exit 0, Step 2 confirm", x.rc === 0 && x.r.status.rows["Step 2"] === "confirm" &&
    x.r.status.hello.mode === "status", x.r && x.r.status && x.r.status.rows);
  await stopXfer();

  info("waiting-payload", ["Step 2 waits for its payload."]);
  await startXfer("run", ["--pin", "0".repeat(64), "--idle", "8s", "--info", INFO]);
  x = await wd("step2");
  check("a unit whose pins do not name the payload: exit 6, nothing sent (HELLO only)", x.rc === 6 && usbOut(x.run).every((f) => f.type === 1), x.r && x.r.why);
  await stopXfer();
  sh(`${XF} cleanup`);
}

async function finishPhase() {
  const lb = fs.readFileSync("/wd/finish/uc-pxx-finish/finish.json");
  fs.mkdirSync("/usr/share/uc-pxx-usb", { recursive: true });
  fs.writeFileSync(LIST, lb, { mode: 0o644 });
  // the guest has no device tree: the slot's info has no model and no slot; the guest's pins say so
  const gp = JSON.parse(JSON.stringify(PINS));
  gp.expect.model = "-";
  gp.expect.finish.slot = "-";
  fs.writeFileSync("/wd/pins-finish-guest.json", JSON.stringify(gp));
  await startXfer("usb", ["--idle", "8s"]);
  let x = await wd("status", { pins: "/wd/pins-finish-guest.json" });
  check("finish unit: status exit 0, HELLO mode finish, waiting-finish, the one pin", x.rc === 0 && x.r.status.hello.mode === "finish" &&
    x.r.status.hello.state === "waiting-finish" && JSON.stringify(x.r.status.hello.pins) === JSON.stringify([PINS.finish.files[0][2]]),
  x.r && x.r.status && x.r.status.hello);
  x = await wd("finish", { dry: true, pins: "/wd/pins-finish-guest.json" });
  check("finish dry run: exit 0, nothing sent", x.rc === 0 && usbOut(x.run).length === 0, x.r && x.r.why);
  x = await wd("finish", { pins: "/wd/pins-finish-guest.json" });
  check("finish: exit 0, the unit took the finish set (COMMIT ok, auto false)", x.rc === 0 && x.r.outcome === "finish-committed" &&
    x.r.committed && x.r.committed.auto === false, x.r && x.r.why);
  await sleep(3000);
  say(`the usb service after the finish: ${sh(`cat /run/uc-pxx-usb/info.json 2>/dev/null; ls -la /run/uc-pxx-finish 2>&1 | head`)}`);
  await stopXfer();
}

function usbOut(run) {
  try {
    return fs.readFileSync(`${run}/usb.jsonl`, "utf8").split("\n").filter((l) => l).map((l) => JSON.parse(l)).filter((r) => r.op === "out-frame");
  } catch {
    return [];
  }
}

(async () => {
  try {
    say(`chromium policy dirs: ${sh("ls -la /etc/opt/chrome_for_testing/policies/managed /etc/chromium/policies/managed 2>&1")}`);
    await payloadPhase();
    save();
    await finishPhase();
  } catch (e) {
    check("no harness error", false, String(e && e.stack));
  }
  await stopXfer();
  save();
  const bad = R.checks.filter((c) => !c.ok).length;
  say(`RESULT ${bad ? "FAIL" : "PASS"}: ${R.checks.length - bad}/${R.checks.length}`);
})();
