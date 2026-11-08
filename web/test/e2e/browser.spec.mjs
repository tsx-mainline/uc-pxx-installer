// SPDX-License-Identifier: GPL-2.0-or-later
// The browser check and the approval gate.
import { body, expect, loadImage, open, PIN, phase, rec, test } from "./helpers.mjs";

const ONLY = "only in Google Chrome, Microsoft Edge or Chromium on a desktop computer";

for (const [name, sc, text] of [
  ["Firefox or Safari (no userAgentData, no WebUSB)", { noUAData: true, noUsb: true, noSerial: true }, ONLY],
  // Chromium itself is accepted (see below). A fork that ships its own vendor
  // brand next to "Chromium" is still refused.
  ["Brave brand", { brands: ["Brave", "Chromium"] }, ONLY],
  ["Opera brand", { brands: ["Opera", "Chromium"] }, ONLY],
  ["Chrome on a phone", { mobile: true }, ONLY],
  ["Chrome on ChromeOS", { platform: "Chrome OS" }, ONLY],
  ["Chrome on an unknown platform", { platform: "Fuchsia" }, ONLY],
  ["Chrome with WebUSB off by policy", { noUsb: true }, "WebUSB is off"],
]) {
  test(`refused: ${name}`, async ({ page }) => {
    await open(page, Object.assign({ start: "rockusb" }, sc));
    await expect(body(page)).toHaveAttribute("data-browser", "refused");
    await expect(page.locator("#browser-status")).toContainText(text);
    await loadImage(page);
    await phase(page, "image-ok");
    await expect(page.locator("#connect")).toBeDisabled();
    expect((await rec(page)).filter((e) => e.op !== "attach")).toEqual([]);
  });
}

test("Microsoft Edge is accepted", async ({ page }) => {
  await open(page, { start: "rockusb", brands: ["Microsoft Edge", "Chromium"] });
  await expect(page.locator("#browser-status")).toContainText("Microsoft Edge: WebUSB and Web Serial OK");
  await loadImage(page);
  await expect(page.locator("#connect")).toBeEnabled();
});

// Chromium itself (same WebUSB engine as Chrome/Edge) is accepted (R4). The
// GREASE placeholder "Not.A/Brand" is ignored, so "Chromium" is the only real
// brand and the page passes.
test("Chromium is accepted", async ({ page }) => {
  await open(page, { start: "rockusb", brands: ["Chromium", "Not.A/Brand"] });
  await expect(page.locator("#browser-status")).toContainText("Chromium: WebUSB and Web Serial OK");
  await loadImage(page);
  await expect(page.locator("#connect")).toBeEnabled();
});

// A GREASE placeholder with another shape is still ignored (robustness).
test("Chrome with a different GREASE placeholder is accepted", async ({ page }) => {
  await open(page, { start: "rockusb", brands: ["Not;A=Brand", "Chromium", "Google Chrome"] });
  await expect(page.locator("#browser-status")).toContainText("Google Chrome: WebUSB and Web Serial OK");
  await loadImage(page);
  await expect(page.locator("#connect")).toBeEnabled();
});

test("Chrome without Web Serial: the entry works, the shell button is off", async ({ page }) => {
  await open(page, { start: "rockusb", noSerial: true });
  await expect(page.locator("#browser-status")).toContainText("Web Serial is off");
  await expect(page.locator("#serial-open")).toBeDisabled();
  await loadImage(page);
  await expect(page.locator("#connect")).toBeEnabled();
});

test("the page as shipped: device use not approved, every device button off, the warning shows", async ({ page }) => {
  await open(page, { start: "rockusb" }, { shippedConfig: true });
  await expect(page.locator("#gate")).toBeVisible();
  await expect(page.locator("#gate")).toContainText("Do not use this page on a unit");
  await expect(page.locator("#connect")).toBeDisabled();
  await expect(page.locator("#log")).toContainText("device use is not approved");
  // The shipped pin is the release-1.2 install image; a test file is not it.
  await loadImage(page);
  await expect(page.locator("#connect")).toBeDisabled();
  await page.evaluate(() => document.getElementById("connect").removeAttribute("disabled"));
  await page.click("#connect");
  await expect(body(page)).toHaveAttribute("data-notice", "not-approved");
  expect((await rec(page)).filter((e) => e.op !== "attach")).toEqual([]);
});

test("approval off but a pinned image: still no device action", async ({ page }) => {
  await open(page, { start: "rockusb" }, { approved: false, pins: [PIN] });
  await loadImage(page);
  await phase(page, "image-ok");
  await expect(page.locator("#connect")).toBeDisabled();
  await expect(page.locator("#gate")).toBeVisible();
});

test("no inline script and a strict CSP: the page still runs (no CSP errors)", async ({ page }) => {
  const errors = [];
  page.on("console", (m) => { if (m.type() === "error") errors.push(m.text()); });
  page.on("pageerror", (e) => errors.push(String(e)));
  await open(page, { start: "rockusb" });
  await expect(page.locator("#browser-status")).toContainText("Google Chrome");
  expect(errors).toEqual([]);
  expect(await page.locator("script:not([src])").count()).toBe(0);
});
