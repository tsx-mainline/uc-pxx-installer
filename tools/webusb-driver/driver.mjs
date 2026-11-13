// SPDX-License-Identifier: GPL-2.0-or-later
// driver.mjs: the in-container part of uc-pxx-webdrive. It runs only inside the
// pinned Playwright container that the host tool (uc-pxx-webdrive) starts with
// --network none, and does one run of one mode:
//
//   status  the page's read-only status view (HELLO only; the page form of the
//           sender's `hello`)
//   step2   the page's step-2 section: the payload from the page's own origin
//           (STEP2.PAYLOAD_URL of the page copy), "Continue by itself" ticked,
//           "Connect and send"; the run ends at the unit's COMMIT answer
//   finish  the page's finish section: the uc-pxx-finish folder, "Connect and
//           send the finish"; the run ends at the unit's COMMIT answer
//
// With --dry-run every step runs except the ones that open the device: no
// status read, no send (and the host tool passes no USB device node).
//
// It checks every file it serves or gives the page against the pins before the
// server starts, serves them on 127.0.0.1:8137 only (the container has no
// network), checks that the Chromium policy file is active, drives the page,
// logs every page state and text and every WebUSB transfer (inpage.js), and
// stops on any screen, text or answer it does not expect: it then clicks
// nothing more. A send is never stopped: the driver waits for the page's send
// to end by itself, with no timeout, and closes the browser only when no OUT
// transfer is in flight.
import { createHash } from "node:crypto";
import fs from "node:fs";
import { createServer } from "node:http";
import path from "node:path";

const EXIT = Object.freeze({ OK: 0, USAGE: 2, LOCAL: 3, LINK: 4, REFUSED: 5, UNEXPECTED: 6, KEEP_POWERED: 7, OPERATOR: 130 });
const ORIGIN = "http://127.0.0.1:8137";
const PROBE = "/__wd_policy_probe__";
const OUT = "/out";
const SITE = "/site";
const PAYLOAD = "/payload/uc-pxx-mainline";
const FINISH = "/finish/uc-pxx-finish";
const MODULES = "/modules";
const POLICY_FILE = "/etc/opt/chrome_for_testing/policies/managed/uc-pxx-webdrive.json";

class Stop extends Error {
  constructor(code, why) {
    super(why);
    this.code = code;
  }
}

// ---- arguments ----
function parseArgs(a) {
  const o = { mode: null, dryRun: false, pins: "/tool/pins.json", fake: null, scenario: null };
  for (let i = 0; i < a.length; i++) {
    const k = a[i];
    if (k === "--mode") o.mode = a[++i];
    else if (k === "--dry-run") o.dryRun = true;
    else if (k === "--pins") o.pins = a[++i]; // tests only (the host tool never passes it)
    else if (k === "--fake") o.fake = a[++i]; // tests only: a fake navigator.usb
    else if (k === "--scenario") o.scenario = a[++i]; // tests only
    else throw new Stop(EXIT.USAGE, `unknown argument ${k}`);
  }
  if (!["status", "step2", "finish"].includes(o.mode)) throw new Stop(EXIT.USAGE, "--mode status|step2|finish");
  if ((o.fake === null) !== (o.scenario === null)) throw new Stop(EXIT.USAGE, "--fake and --scenario go together");
  return o;
}

// ---- logs ----
const files = {};
function out(name, line) {
  if (!files[name]) files[name] = fs.openSync(path.join(OUT, name), "a", 0o644);
  fs.writeSync(files[name], line + "\n");
}
const iso = () => new Date().toISOString();
function log(m) {
  const line = `[${iso()}] ${m}`;
  out("driver.log", line);
  console.log(line);
}

// ---- file checks (before the server starts) ----
function shaFile(p) {
  const fd = fs.openSync(p, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW | fs.constants.O_NONBLOCK);
  try {
    const st = fs.fstatSync(fd);
    if (!st.isFile()) throw new Stop(EXIT.LOCAL, `${p} is not a regular file`);
    const h = createHash("sha256");
    const b = Buffer.allocUnsafe(1 << 20);
    let n, total = 0;
    while ((n = fs.readSync(fd, b, 0, b.length, null)) > 0) {
      h.update(b.subarray(0, n));
      total += n;
    }
    if (total !== st.size) throw new Stop(EXIT.LOCAL, `${p}: read ${total} of ${st.size} bytes`);
    return { sha256: h.digest("hex"), size: st.size };
  } finally {
    fs.closeSync(fd);
  }
}
// checkExact: dir holds exactly the named regular files, with these sizes and shas.
function checkExact(dir, spec) {
  const st = fs.lstatSync(dir);
  if (!st.isDirectory()) throw new Stop(EXIT.LOCAL, `${dir} is not a folder`);
  const names = fs.readdirSync(dir).sort();
  const want = Object.keys(spec).sort();
  if (JSON.stringify(names) !== JSON.stringify(want)) {
    throw new Stop(EXIT.LOCAL, `${dir} holds ${JSON.stringify(names)}, it must hold exactly ${JSON.stringify(want)}`);
  }
  for (const n of want) {
    const l = fs.lstatSync(path.join(dir, n));
    if (!l.isFile()) throw new Stop(EXIT.LOCAL, `${dir}/${n} is not a regular file (no link is followed)`);
    const r = shaFile(path.join(dir, n));
    if (r.size !== spec[n].size || r.sha256 !== spec[n].sha256) {
      throw new Stop(EXIT.LOCAL, `${dir}/${n}: size ${r.size} sha256 ${r.sha256}; the pin is size ${spec[n].size} sha256 ${spec[n].sha256}`);
    }
    log(`checked ${dir}/${n}: ${r.size} bytes, sha256 ${r.sha256}`);
  }
}
// checkSite: the page copy holds exactly the pinned files (regular files and
// folders only), each with its pinned sha.
function checkSite(dir, pins) {
  const found = [];
  const walk = (d, rel) => {
    for (const e of fs.readdirSync(d, { withFileTypes: true })) {
      const r = rel ? `${rel}/${e.name}` : e.name;
      const l = fs.lstatSync(path.join(d, e.name));
      if (l.isDirectory()) walk(path.join(d, e.name), r);
      else if (l.isFile()) found.push(r);
      else throw new Stop(EXIT.LOCAL, `the page copy holds ${r}, which is not a regular file or a folder`);
    }
  };
  walk(dir, "");
  found.sort();
  const want = Object.keys(pins).sort();
  if (JSON.stringify(found) !== JSON.stringify(want)) {
    throw new Stop(EXIT.LOCAL, `the page copy holds ${JSON.stringify(found)}, the pins name ${JSON.stringify(want)}`);
  }
  for (const r of want) {
    const s = shaFile(path.join(dir, r));
    if (s.sha256 !== pins[r]) throw new Stop(EXIT.LOCAL, `the page copy's ${r} has sha256 ${s.sha256}, the pin is ${pins[r]}`);
  }
  log(`checked the page copy: ${want.length} files, each its pinned sha256`);
}

// ---- the server: 127.0.0.1:8137, GET/HEAD, only the pinned paths ----
const TYPES = { ".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8",
  ".txt": "text/plain; charset=utf-8", ".sh": "text/plain; charset=utf-8", ".ps1": "text/plain; charset=utf-8",
  ".json": "application/json" };
function serve(routes, state) {
  const srv = createServer((req, res) => {
    let p;
    try {
      p = decodeURIComponent(new URL(req.url, ORIGIN).pathname);
    } catch {
      p = "?";
    }
    if (p === PROBE) state.probeReached = true;
    if (req.method !== "GET" && req.method !== "HEAD") {
      out("server.log", `[${iso()}] ${req.method} ${p} -> 405`);
      res.writeHead(405).end();
      return;
    }
    if (req.headers.range) {
      out("server.log", `[${iso()}] ${req.method} ${p} range ${req.headers.range} -> 416`);
      res.writeHead(416).end();
      return;
    }
    const r = routes.get(p === "/" ? "/index.html" : p);
    if (!r) {
      out("server.log", `[${iso()}] ${req.method} ${p} -> 404`);
      res.writeHead(404, { "cache-control": "no-store" }).end();
      return;
    }
    res.writeHead(200, { "content-type": TYPES[path.extname(r.file)] || "application/octet-stream", "content-length": r.size,
      "cache-control": "no-store", "x-content-type-options": "nosniff" });
    if (req.method === "HEAD") {
      out("server.log", `[${iso()}] HEAD ${p} -> 200`);
      res.end();
      return;
    }
    const t0 = Date.now();
    const s = fs.createReadStream(r.file);
    let sent = 0;
    s.on("data", (c) => {
      sent += c.length;
      state.served[p] = (state.served[p] || 0) + c.length; // read for the response (counted at once: the log line may come after the run)
    });
    s.on("close", () => out("server.log", `[${iso()}] GET ${p} -> 200 ${sent} of ${r.size} bytes in ${Date.now() - t0} ms`));
    s.pipe(res);
  });
  return new Promise((ok, bad) => {
    srv.on("error", bad);
    srv.listen(8137, "127.0.0.1", () => ok(srv));
  });
}

// ---- the run ----
const R = { schema: 1, mode: null, dry_run: false, fake: false, start: iso(), exit: null, outcome: "", why: "", tool: {}, versions: {},
  device: null, status: null, committed: null, unit_sha256: null, served: {}, counts: {}, page_errors: [], external: [] };

async function main() {
  const A = parseArgs(process.argv.slice(2));
  R.mode = A.mode;
  R.dry_run = A.dryRun;
  R.fake = !!A.fake;
  log(`uc-pxx-webdrive driver: mode ${A.mode}${A.dryRun ? " (DRY RUN: the device is never opened)" : ""}${A.fake ? " (FAKE USB: a test, no device)" : ""}`);
  const pinsBytes = fs.readFileSync(A.pins);
  const P = JSON.parse(pinsBytes);
  R.tool = { pins_path: A.pins, pins_sha256: sha(pinsBytes), driver_sha256: sha(fs.readFileSync(new URL(import.meta.url))),
    inpage_sha256: sha(fs.readFileSync("/tool/inpage.js")), fake: A.fake ? sha(fs.readFileSync(A.fake)) : null };
  log(`pins ${A.pins} sha256 ${R.tool.pins_sha256}; driver ${R.tool.driver_sha256}; inpage ${R.tool.inpage_sha256}`);
  R.versions.node = process.version;

  // 1. every file, before anything is served
  checkSite(SITE, P.site);
  const routes = new Map();
  for (const rel of Object.keys(P.site)) routes.set(`/${rel}`, { file: `${SITE}/${rel}`, size: fs.statSync(`${SITE}/${rel}`).size });
  const payloadSpec = {}, finishSpec = {};
  for (const [n, s, h] of P.payload.files) payloadSpec[n] = { size: s, sha256: h };
  for (const [n, s, h] of P.finish.files) finishSpec[n] = { size: s, sha256: h };
  if (A.mode === "step2") {
    checkExact(PAYLOAD, payloadSpec);
    for (const n of Object.keys(payloadSpec)) routes.set(`/payload/uc-pxx-mainline/${n}`, { file: `${PAYLOAD}/${n}`, size: payloadSpec[n].size });
  }
  if (A.mode === "finish") checkExact(FINISH, finishSpec);
  const pol = fs.readFileSync(POLICY_FILE);
  if (sha(pol) !== P.policy_sha256) throw new Stop(EXIT.LOCAL, `the policy file ${POLICY_FILE} has sha256 ${sha(pol)}, the pin is ${P.policy_sha256}`);
  log(`policy file ${POLICY_FILE} sha256 ${sha(pol)}`);

  // 2. the server, the browser, the policy check
  const sv = { probeReached: false, served: R.served };
  const srv = await serve(routes, sv);
  log(`serving ${routes.size} pinned paths on ${ORIGIN} (inside the container; it has no network)`);
  const pw = await import(`${MODULES}/playwright-core/index.mjs`);
  R.versions.playwright = JSON.parse(fs.readFileSync(`${MODULES}/playwright-core/package.json`)).version;
  // handleSIG*: false: Playwright's own handlers would close the browser on a
  // signal, which cuts a transfer; the driver's handlers wait for the end of a send
  const browser = await pw.chromium.launch({ channel: "chromium", headless: true, timeout: 120000,
    handleSIGINT: false, handleSIGTERM: false, handleSIGHUP: false });
  R.versions.browser = browser.version();
  log(`browser ${R.versions.browser}, playwright-core ${R.versions.playwright}, node ${process.version}`);
  let page = null;
  try {
    const ctx = await browser.newContext({ acceptDownloads: false, serviceWorkers: "block" });
    const probe = await ctx.newPage();
    let blocked = false;
    try {
      await probe.goto(ORIGIN + PROBE, { timeout: 30000 });
    } catch (e) {
      blocked = /ERR_BLOCKED_BY_ADMINISTRATOR/.test(String(e && e.message));
    }
    await probe.close();
    if (!blocked || sv.probeReached) throw new Stop(EXIT.LOCAL, "the Chromium policy file is not active (the probe URL was not blocked)");
    log("the Chromium policy file is active (the probe URL is blocked by the administrator policy)");

    // 3. the page
    page = await ctx.newPage();
    const D = new Drive(page, P, A);
    await D.install();
    R.outcome = await D.run();
    R.exit = EXIT.OK;
  } catch (e) {
    if (!(e instanceof Stop)) {
      R.exit = EXIT.UNEXPECTED;
      R.why = `driver error: ${e && e.stack ? e.stack.split("\n").slice(0, 3).join(" | ") : e}`;
    } else {
      R.exit = e.code;
      R.why = e.message;
    }
    log(`STOP (exit ${R.exit}): ${R.why}`);
  } finally {
    // end() returns only when no OUT transfer is in flight and the page does
    // not send (no timeout), or when the page is gone
    if (page && Drive.current) await Drive.current.end().catch((e) => log(`end: ${e && e.message}`));
    await browser.close().catch(() => {});
    await new Promise((ok) => srv.close(() => ok()));
  }
}

function sha(b) {
  return createHash("sha256").update(b).digest("hex");
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let operatorStop = false;
for (const s of ["SIGINT", "SIGTERM", "SIGHUP"]) {
  process.on(s, () => {
    if (!operatorStop) log(`${s}: the run stops at the next safe point (a send in progress runs to the end first)`);
    operatorStop = true;
  });
}

// ---- the page drive ----
class Drive {
  static current = null;
  constructor(page, P, A) {
    this.page = page;
    this.P = P;
    this.A = A;
    this.mode = A.mode;
    this.dry = A.dryRun;
    this.pageErrors = [];
    this.external = [];
    this.crashed = false;
    this.sending = false;
    this.deferred = null;
    this.outBusy = false;
    this.counts = { "out-frame": 0, "out-data": 0, "in-frame": 0, guard: 0, state: 0, "page-log": 0 };
    Drive.current = this;
  }

  async install() {
    const { page, P, A } = this;
    page.on("pageerror", (e) => {
      this.pageErrors.push(String(e && e.message));
      out("console.log", `[${iso()}] PAGEERROR ${e && e.message}`);
    });
    page.on("console", (m) => out("console.log", `[${iso()}] ${m.type()} ${m.text()}`));
    page.on("crash", () => {
      this.crashed = true;
      log("THE PAGE CRASHED");
    });
    page.on("dialog", (d) => {
      this.pageErrors.push(`a dialog: ${d.type()} ${d.message()}`);
      d.dismiss().catch(() => {});
    });
    await page.route((u) => !u.toString().startsWith(ORIGIN + "/"), (route) => {
      this.external.push(route.request().url());
      return route.abort();
    });
    const cfg = { mode: A.mode, dryRun: A.dryRun, payloadFiles: P.payload.files, finishFiles: P.finish.files,
      firstSha: A.mode === "finish" ? P.finish.files[0][2] : P.payload.files[0][2] };
    await page.addInitScript((c) => Object.defineProperty(globalThis, "__WD_CFG", { value: Object.freeze(c) }), cfg);
    if (A.fake) {
      await page.addInitScript({ path: A.fake });
      await page.addInitScript((sc) => globalThis.FakeXfer.install(sc), JSON.parse(fs.readFileSync(A.scenario, "utf8")));
    }
    await page.addInitScript({ path: "/tool/inpage.js" });
  }

  // drain: the in-page records into the run files
  async drain() {
    let recs;
    try {
      recs = await this.page.evaluate(() => (globalThis.__wd ? globalThis.__wd.drain() : []));
    } catch {
      return;
    }
    for (const r of recs) {
      if (this.counts[r.op] !== undefined) this.counts[r.op]++;
      if (r.op === "page-log") out("page.log", r.line);
      else if (r.op === "state" || r.op === "progress") out("page-states.jsonl", JSON.stringify(r));
      else out("usb.jsonl", JSON.stringify(r));
      if (r.op === "guard") log(`GUARD refused before sending: ${r.why}`);
      if (r.op === "out-frame" || r.op === "in-frame") log(`usb ${r.op === "out-frame" ? "->" : "<-"} ${frameText(r)}`);
      if (r.op === "state") log(`page state: ${stateText(r.s)}`);
    }
  }
  snap() {
    return this.page.evaluate(() => globalThis.__wd.snapshot());
  }
  transport() {
    return this.page.evaluate(() => globalThis.__wd.transport());
  }

  // check: the rules of every moment. During a send a broken rule is only
  // noted (the send runs to the end); it stops the run after the send.
  check(s, t) {
    const bad = [];
    if (this.crashed) bad.push([EXIT.LINK, "the page crashed"]);
    if (this.pageErrors.length) bad.push([EXIT.UNEXPECTED, `page error: ${this.pageErrors[0]}`]);
    if (this.external.length) bad.push([EXIT.UNEXPECTED, `a request outside the page's origin: ${this.external[0]}`]);
    if (s.ds.browser && s.ds.browser !== "ok") bad.push([EXIT.UNEXPECTED, `the page refuses the browser: ${s.text["browser-status"]}`]);
    if (s.hidden.stop === false || s.ds.refusal) bad.push([EXIT.UNEXPECTED, `the page's STOP box: ${s.text["stop-message"]}`]);
    if (s.ds.notice) bad.push([EXIT.UNEXPECTED, `a page notice ${s.ds.notice}: ${s.text.notice}`]);
    if (s.ds.stopped && s.ds.stopped !== "false") bad.push([EXIT.UNEXPECTED, "the page stopped"]);
    if (t && t.requestDevice) bad.push([EXIT.UNEXPECTED, "the page asked for the device chooser: the policy did not grant the device"]);
    if (t && t.guards.length) bad.push([EXIT.UNEXPECTED, `the transport guard refused a frame: ${t.guards[0]}`]);
    for (const k of ["step2-status", "finish-status", "xstatus-text", "step2-fetch-status", "step2-folder-status", "finish-folder-status"]) {
      if (/KEEP THE UNIT POWERED/.test(s.text[k] || "")) bad.push([EXIT.KEEP_POWERED, `the page says: ${s.text[k]}`]);
    }
    if (!bad.length) return;
    bad.sort((a, b) => (a[0] === EXIT.KEEP_POWERED ? -1 : b[0] === EXIT.KEEP_POWERED ? 1 : 0));
    if (this.sending) {
      if (!this.deferred) {
        this.deferred = bad[0];
        log(`noted during the send (the send runs to the end): ${bad[0][1]}`);
      }
      return;
    }
    throw new Stop(bad[0][0], bad[0][1]);
  }

  // until: poll the page every 200 ms until fn(s, t) gives a value. timeoutMs
  // null: no timeout (only while the page sends).
  async until(what, fn, timeoutMs) {
    const t0 = Date.now();
    let lastNote = t0;
    for (;;) {
      await this.drain();
      const s = await this.snap();
      const t = await this.transport();
      this.check(s, t);
      const v = fn(s, t);
      if (v) return v;
      if (operatorStop && !this.sending) throw new Stop(EXIT.OPERATOR, `stopped by the operator while waiting for ${what}`);
      if (timeoutMs !== null && Date.now() - t0 > timeoutMs) throw new Stop(EXIT.LINK, `${what}: none after ${Math.round(timeoutMs / 1000)} s`);
      if (Date.now() - lastNote > 30000) {
        lastNote = Date.now();
        log(`waiting for ${what} (${Math.round((Date.now() - t0) / 1000)} s)${this.sending ? ": a send runs, no timeout" : ""}`);
      }
      await sleep(200);
    }
  }

  async click(sel) {
    if (operatorStop) throw new Stop(EXIT.OPERATOR, `stopped by the operator before pressing ${sel}`);
    const s = await this.snap();
    this.check(s, await this.transport());
    if (s.ctl[sel.slice(1)] && s.ctl[sel.slice(1)].disabled) throw new Stop(EXIT.UNEXPECTED, `${sel} is disabled`);
    log(`press ${sel}`);
    await this.page.click(sel, { timeout: 10000 });
  }

  async load() {
    const r = await this.page.goto(`${ORIGIN}/index.html`, { timeout: 60000 });
    if (!r || r.status() !== 200) throw new Stop(EXIT.LOCAL, `the page did not load (${r && r.status()})`);
    const s = await this.until("the page's browser check", (s) => s.ds.browser, 30000);
    if (s !== "ok") throw new Stop(EXIT.UNEXPECTED, "the page refuses this browser");
    const st = await this.snap();
    log(`page loaded: ${st.text["browser-status"]}`);
    // the page copy's own config (also pinned by sha): exactly the pins of this tool
    const c = await this.page.evaluate(async () => {
      const m = await import("./js/config.js");
      return { s2: m.STEP2_USB_ENABLED, f: m.FINISH_USB_ENABLED, ml: m.MAINLINE_INSTALL_APPROVED, url: m.STEP2.PAYLOAD_URL,
        payloads: m.STEP2.PAYLOADS.map((p) => ({ label: p.label, sha: p.manifest_sha256 })), lists: m.FINISH.LISTS.map((p) => ({ label: p.label, sha: p.sha256 })),
        usb: m.STEP2_USB };
    });
    const P = this.P;
    const want = { s2: true, f: true, ml: false, url: `${ORIGIN}/payload/uc-pxx-mainline/`,
      payloads: [{ label: P.labels.payload, sha: P.payload.files[0][2] }], lists: [{ label: P.labels.finish, sha: P.finish.files[0][2] }] };
    const got = { s2: c.s2, f: c.f, ml: c.ml, url: c.url, payloads: c.payloads, lists: c.lists };
    if (JSON.stringify(got) !== JSON.stringify(want)) throw new Stop(EXIT.LOCAL, `the page's config is not the pinned one: ${JSON.stringify(got)}`);
    if (c.usb.vendorId !== P.usb.vendor_id || c.usb.productId !== P.usb.product_id) throw new Stop(EXIT.LOCAL, "the page's USB id is not the pinned one");
    if (st.hidden["xstatus-section"] || st.hidden["step2-section"] || st.hidden["finish-section"] || st.hidden["step2-fetch-row"]) {
      throw new Stop(EXIT.UNEXPECTED, "a section of the page copy is hidden");
    }
    if (st.ds.step2 !== "idle" || st.ds.finish !== "idle" || st.ds.xstatus !== "idle") {
      throw new Stop(EXIT.UNEXPECTED, `the page starts in step2 ${st.ds.step2}, finish ${st.ds.finish}, status ${st.ds.xstatus}`);
    }
    log("the page copy's config: the step-2 and finish sections on, the pinned payload, finish list and payload URL; the mainline install off");
  }

  // devices: the page's own view (navigator.usb.getDevices: no device is opened)
  async devices() {
    const list = await this.page.evaluate(async () => (await navigator.usb.getDevices()).map((d) => ({ vid: d.vendorId, pid: d.productId,
      serial: d.serialNumber, product: d.productName, maker: d.manufacturerName })));
    R.device = list;
    log(`the page sees ${list.length} USB device(s) by the policy: ${JSON.stringify(list)}`);
    const U = this.P.usb;
    if (list.length !== 1) throw new Stop(list.length ? EXIT.UNEXPECTED : EXIT.LINK, `the page sees ${list.length} devices, it must see exactly one`);
    const d = list[0];
    if (d.vid !== U.vendor_id || d.pid !== U.product_id || d.serial !== U.serial || d.product !== U.product || d.maker !== U.manufacturer) {
      throw new Stop(EXIT.UNEXPECTED, `the device is not the unit's transfer interface: ${JSON.stringify(d)}`);
    }
  }

  // readStatus: the page's "Read the unit's status" (HELLO only)
  async readStatus() {
    await this.click("#xstatus-read");
    const ph = await this.until("the unit's status answer", (s) => (["read", "error"].includes(s.ds.xstatus) && s.ctl["xstatus-read"] && !s.ctl["xstatus-read"].disabled ? s.ds.xstatus : null), 120000);
    const s = await this.snap();
    const t = await this.transport();
    const rows = Object.fromEntries(s.xrows);
    R.status = { rows, lines: s.text["xstatus-lines"], text: s.text["xstatus-text"], hello: t.lastHello };
    log(`the page's status view: ${JSON.stringify(rows)}; lines ${JSON.stringify(s.text["xstatus-lines"])}`);
    if (ph === "error") throw new Stop(EXIT.LINK, `the page could not read the unit's status: ${s.text["xstatus-text"]}`);
    if (!t.lastHello) throw new Stop(EXIT.UNEXPECTED, "no HELLO answer in the transport record");
    return { rows, hello: t.lastHello };
  }

  // expectUnit: the status before a send must be exactly the pinned one
  expectUnit(st, kind) {
    const E = this.P.expect, h = st.hello, r = st.rows, bad = [];
    const eq = (k, a, b) => {
      if (a !== b) bad.push(`${k} is ${JSON.stringify(a)}, expected ${JSON.stringify(b)}`);
    };
    eq("Model", r.Model, E.model);
    eq("Build token", r["Build token"], E.token);
    if (kind === "step2") {
      eq("Slot", r.Slot, E.step2.slot.toUpperCase());
      eq("Interface mode", h.mode, "payload");
      eq("Interface state", h.state, "waiting-payload");
      eq("Step 2", r["Step 2"], "waiting-payload");
      eq("status_only", h.status_only, false);
      if (!Array.isArray(h.pins) || !h.pins.includes(this.P.payload.files[0][2])) bad.push("the unit's pins do not name the pinned payload");
      else for (const p of h.pins) if (!E.step2.pins_allowed.includes(p)) bad.push(`the unit pins ${p}, not a manifest of this release`);
      const total = this.P.payload.files.reduce((n, f) => n + f[1], 0);
      if (!(h.room >= this.P.payload.files[2][1])) bad.push(`the unit's room ${h.room} is less than the rootfs (${total} bytes in all)`);
    } else {
      eq("Slot", r.Slot, E.finish.slot.toUpperCase());
      eq("Interface mode", h.mode, "finish");
      eq("Interface state", h.state, "waiting-finish");
      eq("Step 2", r["Step 2"], "finish: waiting");
      eq("status_only", h.status_only, false);
      if (JSON.stringify(h.pins) !== JSON.stringify([this.P.finish.files[0][2]])) bad.push(`the unit's finish pin is ${JSON.stringify(h.pins)}, not exactly the pinned list`);
    }
    if (bad.length) throw new Stop(EXIT.UNEXPECTED, `the unit is not in the expected state for ${kind}: ${bad.join("; ")} (nothing was sent)`);
    log(`the unit is in the expected state for ${kind} (the page's status view and its HELLO)`);
  }

  async run() {
    await this.load();
    if (this.mode === "status") return this.runStatus();
    if (this.mode === "step2") return this.runStep2();
    return this.runFinish();
  }

  async runStatus() {
    await this.devices();
    if (this.dry) {
      log("dry run ends here: the next step would press \"Read the unit's status\" (HELLO only)");
      return "dry-run";
    }
    const st = await this.readStatus();
    // a summary for the session (no decision is made on it in this mode)
    for (const k of ["step2", "finish"]) {
      try {
        this.expectUnit(st, k);
        log(`summary: the unit is ready for ${k}`);
      } catch (e) {
        log(`summary: not ready for ${k}: ${e.message}`);
      }
    }
    return "status-read";
  }

  async runStep2() {
    const P = this.P;
    await this.devices();
    if (!this.dry) this.expectUnit(await this.readStatus(), "step2");
    await this.click("#step2-fetch");
    const total = P.payload.files.reduce((n, f) => n + f[1], 0);
    const okText = `OK: ${P.labels.payload} (${total} bytes, downloaded).`;
    const ft = await this.until("the page's payload download and check", (s) => {
      const t = s.text["step2-fetch-status"] || "";
      if (/^(Not used:|Could not download)/.test(t)) throw new Stop(EXIT.LOCAL, `the page did not take the payload: ${t}`);
      return s.ds.step2 === "folder-ok" && t.startsWith("OK: ") ? t : null;
    }, 30 * 60000);
    if (ft !== okText) throw new Stop(EXIT.UNEXPECTED, `the download text is "${ft}", expected "${okText}"`);
    log(`the page downloaded and checked the payload: ${ft}`);
    await this.page.check("#step2-auto", { timeout: 10000 });
    const s = await this.snap();
    if (!s.ctl["step2-auto"].checked) throw new Stop(EXIT.UNEXPECTED, "\"Continue by itself\" is not ticked");
    log("\"Continue by itself\" (AUTO-STEP2: the unit's 30 s countdown that Back cancels) is ticked");
    if (this.dry) {
      log("dry run ends here: the next step would press \"Connect and send to the unit\"");
      return "dry-run";
    }
    await this.click("#step2-connect");
    return this.followSend("step2", {
      sending: "sending", done: "sent", folder: "folder-ok", status: "step2-status",
      doneText: "The unit has the payload. It starts step 2 by itself after a 30 s countdown on its screen (Back on the unit cancels).",
      sendingText: /^(|Sending (manifest\.json|boot-template\.img|rootfs\.ext4): \d+ of \d+ MiB \(\d+\.\d MB\/s\)\. Do not disconnect the cable and do not close this page\.)$/,
      auto: true,
    });
  }

  async runFinish() {
    const P = this.P;
    await this.devices();
    if (!this.dry) this.expectUnit(await this.readStatus(), "finish");
    log("pick the uc-pxx-finish folder in the page");
    await this.page.setInputFiles("#finish-folder", "/finish");
    const total = P.finish.files.reduce((n, f) => n + f[1], 0);
    const re = new RegExp(`^OK: ${esc(P.labels.finish)} \\((?:[^()]*/)?uc-pxx-finish, ${total} bytes\\)\\.$`);
    const ft = await this.until("the page's check of the finish folder", (s) => {
      const t = s.text["finish-folder-status"] || "";
      if (/^(No |Could not)/.test(t)) throw new Stop(EXIT.LOCAL, `the page did not take the finish folder: ${t}`);
      return s.ds.finish === "folder-ok" ? t : null;
    }, 120000);
    if (!re.test(ft)) throw new Stop(EXIT.UNEXPECTED, `the finish folder text is "${ft}"`);
    log(`the page checked the finish set: ${ft}`);
    if (this.dry) {
      log("dry run ends here: the next step would press \"Connect and send the finish to the unit\"");
      return "dry-run";
    }
    await this.click("#finish-connect");
    return this.followSend("finish", {
      sending: "sending", done: "watch", folder: "folder-ok", status: "finish-status",
      doneText: "The unit has the finish set: it checks it and runs the finish. Keep the cable and the power connected.",
      sendingText: /^(|Sending (finish\.json|uc-pxx-step2-finish|uc-pxx-bootctl-inst): \d+ of \d+ bytes\. Do not disconnect the cable and do not close this page\.)$/,
      auto: false,
    });
  }

  // followSend: from the press of the send button to the page's end of the
  // send. Before the page starts sending (its HELLO) at most 120 s; while it
  // sends, no timeout.
  async followSend(kind, X) {
    const ds = kind === "step2" ? "step2" : "finish";
    const t0 = Date.now();
    let sawSending = false;
    let lastProg = 0;
    const end = await this.until(`the end of the ${kind} send`, (s, t) => {
      const ph = s.ds[ds], txt = s.text[X.status] || "";
      if (ph === X.sending) {
        if (!sawSending) {
          sawSending = true;
          this.sending = true;
          log(`the page sends (${kind}): no timeout from here; the send runs to the end`);
        }
        if (!X.sendingText.test(txt)) {
          if (!this.deferred) this.deferred = [EXIT.UNEXPECTED, `an unexpected text during the send: "${txt}"`];
        } else if (Date.now() - lastProg > 15000 && txt) {
          lastProg = Date.now();
          log(`progress: ${txt}`);
        }
        return null;
      }
      if (ph === X.folder && !sawSending) {
        if (Date.now() - t0 > 120000) throw new Stop(EXIT.LINK, "the page did not start the send in 120 s (no HELLO answer?)");
        return null;
      }
      this.sending = false;
      return { ph, txt, cls: s.cls[X.status] || "", committed: t.committed, unit: t.unitSha };
    }, null);
    this.sending = false;
    log(`the page's ${kind} send ended: phase ${end.ph}, "${end.txt}"`);
    R.committed = end.committed;
    R.unit_sha256 = end.unit;
    if (this.deferred) throw new Stop(this.deferred[0], this.deferred[1]);
    const c = end.committed;
    const took = c && c.ok === true && c.auto === X.auto;
    // the page's own end text (or, when its status watch already moved on in
    // the 200 ms between two looks, another text of the same watch phase)
    // after a COMMIT answer ok. The page's "done" phase (the watch's end:
    // restarting, a refusal or a stop) is not taken as the end of a send.
    if (took && end.ph === X.done) {
      if (end.txt !== X.doneText) log(`note: the page already shows its next status: "${end.txt}"`);
      for (const [n, , h] of (kind === "step2" ? this.P.payload.files : this.P.finish.files)) {
        if (end.unit[n] !== h) throw new Stop(EXIT.UNEXPECTED, `the unit's sha256 of ${n} is ${end.unit[n]}, the pin is ${h}`);
      }
      log(`the unit took the ${kind === "step2" ? "payload (auto: the 30 s countdown)" : "finish set (auto false)"} into its RAM: COMMIT ok; the unit's sha256 of each file is the pin`);
      return kind === "step2" ? "payload-committed" : "finish-committed";
    }
    if (c && c.ok === true) throw new Stop(EXIT.UNEXPECTED, `the COMMIT answer is ${JSON.stringify(c)}, the page shows phase ${end.ph} "${end.txt}"`);
    if (kind === "finish" && end.ph === "watch" && /link ended at the end of the send/.test(end.txt)) {
      throw new Stop(EXIT.LINK, "the link ended at the COMMIT: the unit may have taken the finish set (read the status)");
    }
    if (/already has the (payload|finish set)/.test(end.txt)) {
      throw new Stop(EXIT.UNEXPECTED, `the unit already had the set before this send: "${end.txt}" (nothing was sent by this run)`);
    }
    if (end.ph === "error" && /^The transfer stopped/.test(end.txt)) throw new Stop(EXIT.LINK, `the transfer stopped: "${end.txt}"`);
    if (end.ph === "error") throw new Stop(EXIT.REFUSED, `the unit refused or is not ready: "${end.txt}"`);
    throw new Stop(EXIT.UNEXPECTED, `an unexpected end of the send: phase ${end.ph}, "${end.txt}"`);
  }

  // end: stop new frames, wait until no OUT transfer is in flight (no timeout)
  // and the page does not send; a pending IN (a HELLO/STATUS answer) is left
  // after 60 s.
  async end() {
    const page = this.page;
    try {
      await page.evaluate(() => globalThis.__wd && globalThis.__wd.quiesce());
    } catch {
      return;
    }
    const t0 = Date.now();
    let note = t0;
    for (;;) {
      await this.drain();
      let f, s;
      try {
        f = await page.evaluate(() => globalThis.__wd.inflight());
        s = await this.snap();
      } catch {
        return;
      }
      const sending = s.ds.step2 === "sending" || s.ds.finish === "sending";
      this.outBusy = f.out > 0 || !!f.put || sending;
      if (!this.outBusy && (f.in === 0 || Date.now() - f.inSince > 60000)) {
        if (f.in) log("a pending IN (an answer to HELLO or STATUS, no data) is left at the close");
        break;
      }
      if (Date.now() - note > 30000) {
        note = Date.now();
        log(`waiting for the transfers to end before the close (${JSON.stringify(f)}, page ${s.ds.step2}/${s.ds.finish})`);
      }
      await sleep(200);
    }
    await this.drain();
    const s = await this.snap().catch(() => null);
    if (s) out("page-states.jsonl", JSON.stringify({ op: "final", t: Date.now(), s }));
    R.counts = this.counts;
    if (this.A.fake) {
      R.fake_unit = await page.evaluate(() => JSON.parse(JSON.stringify(globalThis.__fakexfer || null))).catch(() => null);
    }
    R.page_errors = this.pageErrors;
    R.external = this.external;
    R.transport = await this.transport().catch(() => null);
  }
}

function esc(s) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
function frameText(r) {
  const names = { 1: "HELLO", 2: "PUT_BEGIN", 4: "PUT_END", 5: "COMMIT", 6: "STATUS" };
  const ty = names[r.type & 0x7f] || r.type;
  const b = r.body || {};
  if (r.op === "out-frame") return `${ty} ${JSON.stringify(b)}`;
  const keep = {};
  for (const k of ["mode", "state", "detail", "status_only", "session", "pins", "room", "accept", "ok", "name", "sha256", "reason", "auto"]) {
    if (b[k] !== undefined) keep[k] = b[k];
  }
  if (b.info) keep.info = { slot: b.info.slot, step2: b.info.step2, token: b.info.token, plan_id: b.info.plan_id };
  return `${ty} reply ${JSON.stringify(keep)}`;
}
function stateText(s) {
  const d = s.ds;
  const parts = [`step2=${d.step2} finish=${d.finish} status=${d.xstatus}`];
  for (const k of ["step2-fetch-status", "step2-status", "finish-folder-status", "finish-status", "xstatus-text", "notice", "stop-message"]) {
    if (s.text[k]) parts.push(`${k}: ${JSON.stringify(s.text[k])}`);
  }
  return parts.join("; ");
}

main()
  .catch((e) => {
    R.exit = e instanceof Stop ? e.code : EXIT.UNEXPECTED;
    R.why = e instanceof Stop ? e.message : `driver error: ${e && e.stack}`;
    log(`STOP (exit ${R.exit}): ${R.why}`);
  })
  .finally(() => {
    R.end = iso();
    try {
      fs.writeFileSync(path.join(OUT, "result.json"), JSON.stringify(R, null, 1) + "\n");
    } catch (e) {
      console.error(`result.json: ${e && e.message}`);
    }
    log(`result: exit ${R.exit} ${R.outcome || ""} ${R.why || ""}`);
    process.exitCode = R.exit === null ? EXIT.UNEXPECTED : R.exit;
  });
