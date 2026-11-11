// SPDX-License-Identifier: GPL-2.0-or-later
// The start screen (goal choice), the drive-copy helper and the unlock guide.
// These exercise the new page surfaces; the device write surface is unchanged
// (the happy/refusals/guard specs still pin it). No new eMMC write is possible
// from any of this: the after-test invariants in helpers still apply.
import { body, chooseUserdebug, connectFastboot, expect, loadImage, open, phase, rec, test, toChecked, writeAndRestart } from "./helpers.mjs";

test("the device flow is gated behind the Android userdebug choice", async ({ page }) => {
  await open(page, { start: "rockusb" }, { chooseGoal: false });
  // Before any choice: goal is none, the mainline button is off ("coming soon").
  await expect(body(page)).toHaveAttribute("data-goal", "none");
  await expect(page.locator("#goal-mainline")).toBeDisabled();
  await expect(page.locator("#goal-mainline")).toContainText("coming soon");
  // Loading the image does not enable connect without a choice.
  await loadImage(page);
  await phase(page, "image-ok");
  await expect(page.locator("#connect")).toBeDisabled();
  // Choosing Android userdebug reveals the flow.
  await chooseUserdebug(page);
  await expect(page.locator("#connect")).toBeEnabled();
  await expect(page.locator("#goal-status")).toContainText("Android userdebug");
});

test("clicking the disabled mainline button does nothing (no goal, no device action)", async ({ page }) => {
  await open(page, { start: "rockusb" }, { chooseGoal: false });
  // The button is disabled; force a click via the DOM to prove the handler refuses.
  await page.evaluate(() => document.getElementById("goal-mainline").removeAttribute("disabled"));
  await page.click("#goal-mainline");
  await expect(body(page)).toHaveAttribute("data-goal", "none");
  await expect(page.locator("#goal-status")).toContainText("not ready yet");
  expect((await rec(page)).filter((e) => e.op === "cbw")).toEqual([]);
});

test("the userdebug flow still reaches boot after the choice (write surface unchanged)", async ({ page }) => {
  await open(page, { start: "rockusb" }); // open() auto-selects userdebug
  await toChecked(page);
  await writeAndRestart(page);
  await phase(page, "restarted");
  await connectFastboot(page);
  await phase(page, "fastboot-checked");
  await page.click("#boot");
  await phase(page, "booted");
  // the only cbw write is the BCB at 49184 (also checked by the invariants).
  const writes = (await rec(page)).filter((e) => e.op === "cbw" && e.opcode === 0x15);
  expect(writes.every((e) => e.lba === 49184 && e.count === 4)).toBe(true);
});

test("the unlock guide is OFF by default (owner-gated)", async ({ page }) => {
  await open(page, { start: "rockusb" });
  await expect(page.locator("#unlock-section")).toBeHidden();
});

test("the unlock guide: when enabled, opt-in, shows the one-liners and the pinned client sha, never offers lock", async ({ page }) => {
  await open(page, { start: "rockusb" }, { unlockGuide: true });
  // The section appears once the userdebug goal is chosen (open() did that).
  await expect(page.locator("#unlock-section")).toBeVisible();
  await page.click("#unlock-show");
  const guide = page.locator("#unlock-guide");
  await expect(guide).toContainText("6ba5d28c0854f9ca110f22fe5a3a108a1ba068cab40755256061bb83292fc205");
  await expect(guide).toContainText("tee_unlock.android-arm64");
  // the served one-liner text is shown and mentions the marker and orange.
  await expect(guide).toContainText("/cache/uc-pxx/unlock-marker");
  await expect(guide).toContainText("verifiedbootstate");
  // it warns and never offers re-lock.
  await expect(guide).toContainText("never re-lock");
  await expect(guide).not.toContainText("$LL lock");
});

test("drive-copy: the helper copies and verifies the firmware via an injected handle (host I/O only)", async ({ page }) => {
  await open(page, { start: "rockusb" });
  await expect(page.locator("#drivecopy-section")).toBeVisible();
  // Inject a fake File System Access directory + source, then drive the module
  // core directly (showDirectoryPicker is not available in headless Chromium).
  const out = await page.evaluate(async () => {
    const dc = await import("/js/drivecopy.js");
    const cfg = await import("/js/config.js");
    const data = new Uint8Array(4096);
    for (let i = 0; i < data.length; i++) data[i] = (i * 7) & 0xff;
    const expSha = await dc.sha256OfBlob(new Blob([data]));
    const files = new Map();
    const dir = {
      name: "UC-PXX-INST",
      async getFileHandle(name, opts) {
        if (!files.has(name)) { if (!opts || !opts.create) throw new Error("nf"); files.set(name, null); }
        return {
          async createWritable() {
            const ch = [];
            return { async write(v) { ch.push(new Uint8Array(v)); }, async close() {
              let n = 0; for (const c of ch) n += c.length; const b = new Uint8Array(n); let o = 0;
              for (const c of ch) { b.set(c, o); o += c.length; } files.set(name, b);
            }, async abort() { files.set(name, null); } };
          },
          async getFile() { return new Blob([files.get(name)]); },
        };
      },
    };
    const r = await dc.writeAndVerify(dir, new Blob([data]), "fw.bin",
      { label: "test", size: data.length, sha256: expSha });
    return { ok: r.ok, sha: r.sha256, expSha, bytes: files.get("fw.bin").length, fwPinned: cfg.FIRMWARE.sha256 };
  });
  expect(out.ok).toBe(true);
  expect(out.sha).toBe(out.expSha);
  expect(out.bytes).toBe(4096);
  expect(out.fwPinned).toBe("c9766c0a96ca4a199c405143cc3639c2457c060c1535517f252f7c0d3f579718");
  // no device transfer happened during the drive-copy.
  expect((await rec(page)).filter((e) => e.op === "cbw")).toEqual([]);
});
