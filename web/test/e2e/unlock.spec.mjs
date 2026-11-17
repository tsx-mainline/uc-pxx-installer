// SPDX-License-Identifier: GPL-2.0-or-later
// The optional unlock guide (owner-gated, OFF in the shipped config): kept
// from a9f5136; it shows only on the Done step of the userdebug goal.
import { body, expect, open, s1, test, toBooted } from "./helpers.mjs";

test("the unlock guide is OFF by default", async ({ page }) => {
  await open(page, { start: "rockusb", step1: s1() });
  await toBooted(page);
  await page.waitForFunction(() => globalThis.__fake.rec.some((e) => e.dev === "step1" && e.op === "attach"));
  await page.click("#connect-step1");
  await expect(body(page)).toHaveAttribute("data-ui", "done", { timeout: 10000 });
  await expect(page.locator("#unlock-section")).toBeHidden();
});

test("the unlock guide, when enabled: on the Done step, opt-in, the pinned client sha, never a re-lock", async ({ page }) => {
  await open(page, { start: "rockusb", step1: s1() }, { unlockGuide: true });
  await expect(page.locator("#unlock-section")).toBeHidden(); // not before the install is done
  await toBooted(page);
  await page.waitForFunction(() => globalThis.__fake.rec.some((e) => e.dev === "step1" && e.op === "attach"));
  await page.click("#connect-step1");
  await expect(body(page)).toHaveAttribute("data-ui", "done", { timeout: 10000 });
  await expect(page.locator("#unlock-section")).toBeVisible();
  await page.click("#unlock-show");
  const guide = page.locator("#unlock-guide");
  await expect(guide).toContainText("6ba5d28c0854f9ca110f22fe5a3a108a1ba068cab40755256061bb83292fc205");
  await expect(guide).toContainText("/cache/uc-pxx/unlock-marker");
  await expect(guide).toContainText("never re-lock");
  await expect(guide).not.toContainText("$LL lock");
});
