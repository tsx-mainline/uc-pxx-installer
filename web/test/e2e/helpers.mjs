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
export const PIN = { model: "UC-P10", label: "Test image", file: "test.img", size: IMAGE.length, sha256: sha(IMAGE) };
export const PRODUCT = "evb_rk3399"; // the fake's product; it is informational on this route (check_plain_entry)
export const ENTRY_CMDS = ["current-slot", "slot-successful:a", "slot-successful:b", "slot-unbootable:a",
  "slot-unbootable:b", "slot-retry-count:a", "slot-retry-count:b", "product", "max-download-size"].map((v) => `getvar:${v}`);

// expectViolations: violations a test causes on purpose (none so far).
export const test = base.extend({
  page: async ({ page }, use) => {
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

// open loads the page with a scenario. The page's config.js is patched to
// approve device use and to pin the test image, unless shippedConfig.
export async function open(page, scenario, opts = {}) {
  const pins = opts.pins || [PIN];
  if (!opts.shippedConfig) {
    await page.route("**/js/config.js", async (route) => {
      const r = await route.fetch();
      const body = await r.text();
      const approvalRe = /export const DEVICE_USE_APPROVED = (?:true|false);/;
      if (!approvalRe.test(body)) throw new Error("config.js: approval line changed");
      let b = body.replace(approvalRe, `export const DEVICE_USE_APPROVED = ${opts.approved !== false};`);
      let b2 = b.replace(/export const IMAGES = Object\.freeze\(\[[\s\S]*?\]\);/, `export const IMAGES = Object.freeze(${JSON.stringify(pins)});`);
      if (b2 === b) throw new Error("config.js: IMAGES changed");
      // The shipped config pins a real product value; tests may override it.
      const product = opts.product === undefined ? PRODUCT : opts.product;
      const prodRe = /export const EXPECT_FASTBOOT_PRODUCT = [^;]+;/;
      if (!prodRe.test(b2)) throw new Error("config.js: EXPECT_FASTBOOT_PRODUCT changed");
      b2 = b2.replace(prodRe, `export const EXPECT_FASTBOOT_PRODUCT = ${JSON.stringify(product)};`);
      const abLine = "export const EXPECT_AB_FIRST32_SHA256 = null;";
      if (!b2.includes(abLine)) throw new Error("config.js: EXPECT_AB_FIRST32_SHA256 changed");
      if (opts.expectAbConfig !== undefined) {
        b2 = b2.replace(abLine, abLine.replace("null", JSON.stringify(opts.expectAbConfig)));
      }
      // Point the release image base at the test server so the image fetch
      // stays same-origin (the "no external request" guard stays enforced).
      const relRe = /export const RELEASE_IMAGE_BASE = "[^"]*";/;
      if (!relRe.test(b2)) throw new Error("config.js: RELEASE_IMAGE_BASE changed");
      b2 = b2.replace(relRe, `export const RELEASE_IMAGE_BASE = ${JSON.stringify(ORIGIN + "/images")};`);
      await route.fulfill({ response: r, body: b2 });
    });
  }
  await page.route("**/images/test.img", (route) => (opts.noSiteImage ? route.fulfill({ status: 404, body: "" })
    : route.fulfill({ status: 200, contentType: "application/octet-stream", body: IMAGE })));
  await page.addInitScript({ path: FAKE });
  await page.addInitScript((sc) => globalThis.FakeEnv.install(sc), scenario);
  await page.goto("/index.html" + (opts.query || ""));
  expect(await page.evaluate(() => globalThis.__fake && globalThis.__fake.installed)).toBe(true);
}

export const body = (page) => page.locator("body");
export const phase = (page, p) => expect(body(page)).toHaveAttribute("data-phase", p);
export const refused = (page, code) => expect(body(page)).toHaveAttribute("data-refusal", code);
export const notice = (page, code) => expect(body(page)).toHaveAttribute("data-notice", code);

export async function loadImage(page, buf = IMAGE) {
  await page.setInputFiles("#image-file", { name: "installer.img", mimeType: "application/octet-stream", buffer: buf });
}

export async function toChecked(page) {
  await loadImage(page);
  await phase(page, "image-ok");
  await page.click("#connect");
  await phase(page, "download-checked");
}

export async function writeAndRestart(page) {
  await page.check("#confirm");
  await page.click("#write-bcb");
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
  const [dl] = await Promise.all([page.waitForEvent("download"), page.click(button)]);
  const path = await dl.path();
  const { readFile } = await import("node:fs/promises");
  return { name: dl.suggestedFilename(), text: await readFile(path, "utf8") };
}

// connectFastboot waits until the fake unit is in fastboot (as a person waits
// for it), then presses the button.
export async function connectFastboot(page) {
  await page.waitForFunction(() => globalThis.__fake.rec.some((e) => e.dev === "fastboot" && e.op === "attach"));
  await page.click("#connect-fastboot");
}

// toFastbootChecked: the rockusb route up to "fastboot-checked".
export async function toFastbootChecked(page) {
  await toChecked(page);
  await writeAndRestart(page);
  await phase(page, "restarted");
  await connectFastboot(page);
  await phase(page, "fastboot-checked");
}
