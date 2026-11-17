// SPDX-License-Identifier: GPL-2.0-or-later
// Helpers for the page tests. Every test runs the real page in headless
// Chromium with the fake WebUSB/Web Serial of ../fake/fakeusb.js, and after
// each test the invariants below are checked on the fake's transfer record.
import { test as base, expect } from "@playwright/test";
import { createHash } from "node:crypto";
import { fileURLToPath } from "node:url";

export { expect };
export const FAKE = fileURLToPath(new URL("../fake/fakeusb.js", import.meta.url));
export const ORIGIN = "http://127.0.0.1:8137";
export const sha = (b) => createHash("sha256").update(b).digest("hex");

export function makeImage(size = 1536 * 1024 + 123, magic = true) {
  const b = Buffer.alloc(size);
  let x = 12345;
  for (let i = 0; i < size; i++) {
    x = (Math.imul(x, 1103515245) + 12345) >>> 0;
    b[i] = x >>> 24;
  }
  if (magic) b.write("ANDROID!", 0, "latin1");
  return b;
}
export const IMAGE = makeImage();
export const TOKEN = "pxx-inst-test01";
export const PIN = { model: "UC-P10", goals: ["userdebug", "mainline"], label: "Test image", file: "test.img", size: IMAGE.length,
  token: TOKEN, sha256: sha(IMAGE) };
// the test firmware file and part 1 (in place of the 973 MB Crestron file and
// the 400 MB part): random bytes of more than 1 MiB, so a cut can fall inside
export function bytes(n, seed) {
  const b = Buffer.alloc(n);
  let x = seed;
  for (let i = 0; i < n; i++) {
    x = (Math.imul(x, 1103515245) + 12345) >>> 0;
    b[i] = x >>> 24;
  }
  return b;
}
export const FW = bytes(3 * 1048576 + 77, 7);
export const FW_PIN = { label: "Crestron userdebug firmware file", file: "pcdfw-1.0.0.94-userdebug.bin",
  url: "https://crestrondevicefiles.blob.core.windows.net/firmware/pcdfw-1.0.0.94-userdebug.bin", size: FW.length, sha256: sha(FW) };
// the test base (a schema-3 payload folder; the template and the compressed
// root file system are random bytes of the right sizes: the fake checks shas)
export const TEMPLATE = bytes(400000 + 17, 13);
export const ROOTFS_ZST = bytes(2 * 1048576 + 333, 11);
export const MANIFEST = Buffer.from(JSON.stringify({ kind: "uc-pxx-mainline", schema: 3, version: "0.3.0-test-base",
  boot_template: { file: "boot-template.img", size: TEMPLATE.length, sha256: sha(TEMPLATE) },
  rootfs: { file: "rootfs.ext4", size: 268435456, sha256: "7".repeat(64) },
  rootfs_zst: { file: "rootfs.ext4.zst", size: ROOTFS_ZST.length, sha256: sha(ROOTFS_ZST) } }));
export const BASE_PIN = { label: "Mainline Linux", dir: "base/uc-pxx-mainline/", manifest_sha256: sha(MANIFEST), PAYLOAD_URL: null };
export const BUNDLE = bytes(1048576 + 4000, 17);
export const SYSTEMS_PIN = [
  { id: "kiosk-ha", label: "Kiosk and Home Assistant", installs: "Kiosk and Home Assistant", keyRequired: false,
    bundle: { file: "uc-pxx-profile-kiosk-ha-0.3.0-test.tar", size: BUNDLE.length, sha256: sha(BUNDLE), url: null } },
  { id: "kiosk", label: "Kiosk only", installs: "the kiosk", keyRequired: false,
    bundle: { file: "uc-pxx-profile-kiosk-0.3.0-test.tar", size: BUNDLE.length, sha256: sha(BUNDLE), url: null } },
  { id: "console", label: "Console only (for your own software)", installs: "the console system", keyRequired: true, bundle: null },
];
export const PRODUCT = "evb_rk3399"; // the fake's product; it is informational on this route (check_plain_entry)
export const ENTRY_CMDS = ["current-slot", "slot-successful:a", "slot-successful:b", "slot-unbootable:a",
  "slot-unbootable:b", "slot-retry-count:a", "slot-retry-count:b", "product", "max-download-size"].map((v) => `getvar:${v}`);
// s1: the fake RAM installer's step-1 link for this test's files
export function s1(o = {}) {
  return { token: TOKEN, fwSha: FW_PIN.sha256, fwSize: FW.length, baseSha: BASE_PIN.manifest_sha256, ...o };
}
// prov: the fake slot B's system install
export function prov(o = {}) {
  return { bundleSha: sha(BUNDLE), bundleSize: BUNDLE.length, bundleName: "uc-pxx-profile-kiosk-ha-0.3.0-test.tar", ...o };
}
// expectViolations: violations a test causes on purpose (none so far).
export const test = base.extend({
  page: async ({ page }, use, testInfo) => {
    const external = [];
    page.external = external;
    // Any request that is not to the test server is recorded and aborted.
    await page.route((url) => !url.toString().startsWith(ORIGIN + "/"), (route) => {
      external.push(route.request().url());
      return route.abort();
    });
    page.on("websocket", (ws) => external.push(ws.url()));
    await use(page);
    if (page.isClosed()) return;
    if (testInfo.status !== testInfo.expectedStatus) {
      const l = await page.evaluate(() => document.getElementById("log").textContent).catch(() => "");
      console.log(`--- page log of "${testInfo.title}" ---\n${l}`);
    }
    const f = await page.evaluate(() => globalThis.__fake ? JSON.parse(JSON.stringify({
      rec: globalThis.__fake.rec, violations: globalThis.__fake.violations, hostWrites: globalThis.__fake.env.hostWrites,
      abOwnChange: globalThis.__fake.env.abOwnChange, abBefore: globalThis.__fake.env.abBefore,
      abNow: globalThis.__fake.hex(globalThis.__fake.env.emmc.read(49156, 1)),
    })) : null).catch(() => null);
    expect(external, "no request outside the page's own origin").toEqual([]);
    if (!f) return;
    checkInvariants(f);
  },
});

// The rules that hold in every test, whatever the scenario.
export function checkInvariants(f) {
  expect(f.violations, "fake unit: broken rules").toEqual([]);
  for (const e of f.rec.filter((x) => x.op === "cbw")) {
    expect([0x14, 0x15, 0xff]).toContain(e.opcode);
    if (e.opcode === 0x15) {
      expect(e.lba, "the only write is the BCB").toBe(49184);
      expect(e.count).toBe(4);
      expect(e.lba <= 49156 && 49156 < e.lba + e.count, "no write covers LBA 49156").toBe(false);
    }
    if (e.opcode === 0x14) expect(["49156:1", "49184:4", "0:34", "3666944:8"]).toContain(`${e.lba}:${e.count}`);
  }
  for (const w of f.hostWrites) expect(w).toEqual({ lba: 49184, sectors: 4 });
  if (!f.abOwnChange) expect(f.abNow, "LBA 49156 unchanged").toBe(f.abBefore);
  const cmds = f.rec.filter((x) => x.op === "cmd").map((x) => x.cmd);
  for (const c of cmds) {
    expect(c).toMatch(/^(getvar:(product|current-slot|slot-successful:[ab]|slot-unbootable:[ab]|slot-retry-count:[ab]|max-download-size)|download:[0-9a-f]{8}|boot)$/);
  }
  // A download only after every entry getvar was read, in this order (review F1).
  const di = cmds.findIndex((c) => c.startsWith("download:"));
  if (di >= 0) expect(cmds.slice(0, di)).toEqual(ENTRY_CMDS);
  expect(cmds).not.toContain("getvar:all");
  // A transfer that started was never followed by a close of the same device.
  expect(f.rec.filter((x) => x.op === "close" && x.inflight > 0)).toEqual([]);
  expect(f.rec.filter((x) => x.op === "usb-reset")).toEqual([]);
}

// patch replaces exactly one match of re in body (the test fails if the
// config line changed).
function patch(body, re, text, what) {
  if (!re.test(body)) throw new Error(`config.js: ${what} changed`);
  return body.replace(re, text);
}

// open loads the page with a scenario. The page's config.js is patched to
// approve device use, pin the test image for both goals, the test firmware
// and part 1, and turn on the mainline goal (the shipped config keeps it off
// until the release chain), unless shippedConfig. Then, unless opts.stopAt is
// "prepare", it walks Prepare and Files with the goal opts.goal (default
// "userdebug") and stops on the Connect step.
export async function open(page, scenario, opts = {}) {
  const pins = opts.pins || [PIN];
  if (!opts.shippedConfig) {
    await page.route("**/js/config.js", async (route) => {
      const r = await route.fetch();
      let b = await r.text();
      b = patch(b, /export const DEVICE_USE_APPROVED = (?:true|false);/, `export const DEVICE_USE_APPROVED = ${opts.approved !== false};`, "approval");
      b = patch(b, /export const IMAGES = Object\.freeze\(\[[\s\S]*?\n\]\);/, `export const IMAGES = Object.freeze(${JSON.stringify(pins)});`, "IMAGES");
      const product = opts.product === undefined ? PRODUCT : opts.product;
      b = patch(b, /export const EXPECT_FASTBOOT_PRODUCT = [^;]+;/, `export const EXPECT_FASTBOOT_PRODUCT = ${JSON.stringify(product)};`, "product");
      const abLine = "export const EXPECT_AB_FIRST32_SHA256 = null;";
      if (!b.includes(abLine)) throw new Error("config.js: EXPECT_AB_FIRST32_SHA256 changed");
      if (opts.expectAbConfig !== undefined) b = b.replace(abLine, abLine.replace("null", JSON.stringify(opts.expectAbConfig)));
      b = patch(b, /export const FIRMWARE = Object\.freeze\(\{[\s\S]*?\n\}\);/, `export const FIRMWARE = Object.freeze(${JSON.stringify(opts.fwPin || FW_PIN)});`, "FIRMWARE");
      b = patch(b, /export const BASE = Object\.freeze\(\{[\s\S]*?\n\}\);/, `export const BASE = Object.freeze(${JSON.stringify(opts.basePin || BASE_PIN)});`, "BASE");
      b = patch(b, /export const SYSTEMS = Object\.freeze\(\[[\s\S]*?\n\]\);/, `export const SYSTEMS = Object.freeze(${JSON.stringify(opts.systems || SYSTEMS_PIN)});`, "SYSTEMS");
      b = patch(b, /export const PROVISION = Object\.freeze\(\{ PROVISION_URL: null, STATUS_EVERY_S: 1 \}\);/,
        `export const PROVISION = Object.freeze({ PROVISION_URL: ${JSON.stringify(opts.provisionUrl || null)}, STATUS_EVERY_S: 0.1 });`, "PROVISION");
      if (opts.mainline !== false) b = patch(b, /export const MAINLINE_INSTALL_APPROVED = false;/, "export const MAINLINE_INSTALL_APPROVED = true;", "mainline");
      if (opts.step2) b = patch(b, /export const STEP2_USB_ENABLED = false;/, "export const STEP2_USB_ENABLED = true;", "step 2");
      b = patch(b, /STATUS_EVERY_S: 2,\n\}\);/, "STATUS_EVERY_S: 0.2,\n});", "step-2 status");
      b = patch(b, /export const STEP1 = Object\.freeze\(\{ STATUS_EVERY_S: 1, CONNECT_HINT_S: 20 \}\);/,
        "export const STEP1 = Object.freeze({ STATUS_EVERY_S: 0.1, CONNECT_HINT_S: 0.3 });", "STEP1");
      if (opts.finish) {
        b = patch(b, /export const FINISH_USB_ENABLED = false;/, "export const FINISH_USB_ENABLED = true;", "finish");
        b = patch(b, /  LISTS: Object\.freeze\(\[[\s\S]*?\n  \]\),/, `  LISTS: Object.freeze(${JSON.stringify(opts.finish.lists || [])}),`, "FINISH.LISTS");
      }
      b = patch(b, /  STATUS_EVERY_S: 2,\n\}\);\n\n\/\/ -{10}/, "  STATUS_EVERY_S: 0.2,\n});\n\n// ----------", "finish status");
      if (opts.unlockGuide) b = patch(b, /export const UNLOCK_GUIDE_ENABLED = false;/, "export const UNLOCK_GUIDE_ENABLED = true;", "unlock");
      await route.fulfill({ response: r, body: b });
    });
  }
  // siteImage: "missing" (404) or "abort" (a network error: fetch() rejects);
  // otherwise the site serves the test image.
  const siteImage = opts.noSiteImage ? "missing" : opts.siteImage;
  await page.route("**/images/test.img", (route) => (siteImage === "missing" ? route.fulfill({ status: 404, body: "" })
    : siteImage === "abort" ? route.abort("internetdisconnected")
      : route.fulfill({ status: 200, contentType: "application/octet-stream", body: opts.siteImageBody || IMAGE })));
  // the base on this site (base/uc-pxx-mainline/); opts.base: { missing, files: {name: buffer} }
  const baseFiles = { "manifest.json": MANIFEST, "boot-template.img": TEMPLATE, "rootfs.ext4.zst": ROOTFS_ZST, ...((opts.base && opts.base.files) || {}) };
  for (const [name, buf] of Object.entries(baseFiles)) {
    await page.route(`**/base/uc-pxx-mainline/${name}`, (route) => (opts.base && opts.base.missing ? route.fulfill({ status: 404, body: "" })
      : route.fulfill({ status: 200, contentType: "application/octet-stream", body: buf })));
  }
  if (opts.finishSet) {
    for (const [name, buf] of Object.entries(opts.finishSet)) {
      await page.route(`**/finish/${name}`, (route) => route.fulfill({ status: 200, contentType: "application/octet-stream", body: buf }));
    }
  }
  await page.addInitScript({ path: FAKE });
  await page.addInitScript((sc) => globalThis.FakeEnv.install(sc), scenario);
  await page.goto("/index.html" + (opts.query || ""));
  expect(await page.evaluate(() => globalThis.__fake && globalThis.__fake.installed)).toBe(true);
  if (opts.stopAt === "prepare") return;
  if (!(await page.locator("#goal-userdebug").isEnabled().catch(() => false))) return;
  await prepare(page, opts.goal || "userdebug", opts.system);
  if (opts.stopAt === "files") return;
  await files(page, opts);
}

// prepare: the goal, every box, Next.
export async function prepare(page, goal, system = "kiosk-ha") {
  await page.click(`#goal-${goal}`);
  await expect(body(page)).toHaveAttribute("data-goal", goal);
  if (goal === "mainline") await page.click(`#system-${system}`);
  for (const c of await page.locator("#card-prepare input[type=checkbox]").all()) if (await c.isVisible()) await c.check();
  await page.click("#prepare-next");
  await expect(body(page)).toHaveAttribute("data-ui", "files");
}

// files: the image from the site (by itself), the firmware file and (mainline)
// part 1 chosen, an optional key, Next.
export async function files(page, opts = {}) {
  if (!opts.noSiteImage && opts.siteImage !== "abort" && !opts.siteImageBody) await expect(body(page)).toHaveAttribute("data-image", "ok");
  await page.setInputFiles("#fw-file", { name: "pcdfw-1.0.0.94-userdebug.bin", mimeType: "application/octet-stream", buffer: opts.fw || FW });
  await expect(body(page)).toHaveAttribute("data-fw", /ok|bad/);
  if ((opts.goal || "userdebug") === "mainline") {
    if (!(opts.base && opts.base.missing)) await expect(body(page)).toHaveAttribute("data-base", /ok|error|bad/);
    if (opts.key) {
      await page.fill("#sshkey-input", opts.key);
      await expect(body(page)).toHaveAttribute("data-sshkey", "unconfirmed");
      await page.check("#sshkey-confirm");
    }
  }
  if (opts.stopAt === "files-end") return;
  if (await page.locator("#files-next").isEnabled()) {
    await page.click("#files-next");
    await expect(body(page)).toHaveAttribute("data-ui", "connect");
  }
}

export const body = (page) => page.locator("body");
export const phase = (page, p) => expect(body(page)).toHaveAttribute("data-phase", p);
export const refused = (page, code) => expect(body(page)).toHaveAttribute("data-refusal", code);
export const notice = (page, code) => expect(body(page)).toHaveAttribute("data-notice", code);

export async function loadImage(page, buf = IMAGE) {
  await page.setInputFiles("#image-file", { name: "installer.img", mimeType: "application/octet-stream", buffer: buf });
}

export async function toChecked(page) {
  await page.click("#connect");
  await phase(page, "download-checked");
}

// writeAndRestart: Next on Connect, then "Start the installer" (the consent
// to the boot-command write; there is no tick box any more).
export async function writeAndRestart(page) {
  await page.click("#connect-next");
  await expect(body(page)).toHaveAttribute("data-ui", "start");
  await page.click("#start");
}

export async function rec(page) {
  return page.evaluate(() => JSON.parse(JSON.stringify(globalThis.__fake.rec)));
}

export async function fakeEnv(page, expr) {
  return page.evaluate(expr);
}

export function cbws(r) {
  return r.filter((e) => e.op === "cbw").map((e) => `${e.opcode.toString(16)}:${e.lba}:${e.count}`);
}

export function cmds(r) {
  return r.filter((e) => e.op === "cmd").map((e) => e.cmd);
}

export async function saveLog(page, button = "#save-log") {
  if (button === "#save-log") await page.locator("#save-log").scrollIntoViewIfNeeded();
  const [dl] = await Promise.all([page.waitForEvent("download"), page.click(button)]);
  const path = await dl.path();
  const { readFile } = await import("node:fs/promises");
  return { name: dl.suggestedFilename(), text: await readFile(path, "utf8") };
}

// logText: the page's log as it is now
export async function logText(page) {
  return page.evaluate(() => document.getElementById("log").textContent);
}

// connectFastboot waits until the fake unit is in fastboot (as a person waits
// for it), then presses Connect. The page then checks the reset and, only if
// it is the benign one, sends the installer by itself.
export async function connectFastboot(page) {
  await page.waitForFunction(() => globalThis.__fake.rec.some((e) => e.dev === "fastboot" && e.op === "attach"));
  await page.click("#connect-fastboot");
}

// toFastboot: the entry up to the press of Connect in fastboot.
export async function toFastboot(page) {
  await toChecked(page);
  await writeAndRestart(page);
  await phase(page, "restarted");
  await connectFastboot(page);
}

// toBooted: the whole entry up to the installer in the unit's memory.
export async function toBooted(page) {
  await toChecked(page);
  await writeAndRestart(page);
  await phase(page, "restarted");
  await connectFastboot(page);
  await phase(page, "booted");
}

// finishSet: a finish set of the fake (the shape of finish.json)
export function finishSet() {
  const bin = Buffer.alloc(1500, 1);
  const bc = Buffer.alloc(900, 2);
  const list = Buffer.from(JSON.stringify({ kind: "uc-pxx-finish", version: 1, files: [
    { name: "uc-pxx-step2-finish", size: bin.length, sha256: sha(bin) },
    { name: "uc-pxx-bootctl-inst", size: bc.length, sha256: sha(bc) }] }));
  return { sha: sha(list), files: { "finish.json": list, "uc-pxx-step2-finish": bin, "uc-pxx-bootctl-inst": bc } };
}
