// SPDX-License-Identifier: GPL-2.0-or-later
// A unit that goes away mid-transfer, and a slow unit. The page never retries,
// never cancels a transfer and never closes a device during one.
import { toFastbootChecked, cbws, cmds, expect, IMAGE, loadImage, open, phase, rec, refused, test, toChecked, writeAndRestart } from "./helpers.mjs";

async function noMoreAfterUnplug(page, dev) {
  const r = await rec(page);
  const i = r.findIndex((e) => e.dev === dev && e.op === "unplug");
  expect(i).toBeGreaterThanOrEqual(0);
  // After the unplug: no new command, no open, no close of that device.
  expect(r.slice(i + 1).filter((e) => e.dev === dev && ["cbw", "cmd", "open", "close", "out-data"].includes(e.op))).toEqual([]);
}

test("disconnect during the BCB write: stop with the write advice, no retry, no reset", async ({ page }) => {
  await open(page, { start: "rockusb", fault: "disconnectDuringWrite" });
  await toChecked(page);
  await writeAndRestart(page);
  await refused(page, "write-interrupted");
  await expect(page.locator("#stop-advice")).toContainText("Do not start again");
  await noMoreAfterUnplug(page, "rockusb");
  expect(cbws(await rec(page)).filter((c) => c.startsWith("15:"))).toHaveLength(1);
  await page.waitForTimeout(300);
  expect(cbws(await rec(page)).filter((c) => c.startsWith("15:"))).toHaveLength(1);
});

test("disconnect during a read: stop, nothing written", async ({ page }) => {
  await open(page, { start: "rockusb", fault: "disconnectDuringRead" });
  await loadImage(page);
  await page.click("#connect");
  await refused(page, "read-interrupted");
  await expect(page.locator("#stop-advice")).toContainText("Nothing was written");
  expect(cbws(await rec(page))).toEqual(["14:0:34"]); // the first read is the GPT read
  await noMoreAfterUnplug(page, "rockusb");
});

test("disconnect during the fastboot download: stop, no boot, no retry", async ({ page }) => {
  await open(page, { start: "rockusb", fault: "disconnectDuringDownload" });
  await toFastbootChecked(page);
  await page.click("#boot");
  await refused(page, "download-interrupted");
  await expect(page.locator("#stop-advice")).toContainText("The page wrote nothing in this step");
  const c = cmds(await rec(page));
  expect(c.filter((x) => x.startsWith("download:"))).toHaveLength(1);
  expect(c).not.toContain("boot");
  await noMoreAfterUnplug(page, "fastboot");
  const sent = (await rec(page)).filter((e) => e.dev === "fastboot" && e.op === "out-data").reduce((n, e) => n + e.length, 0);
  expect(sent).toBeLessThan(IMAGE.length);
});

test("the unit leaves the bus before the reset CSW: normal, the page goes on", async ({ page }) => {
  await open(page, { start: "rockusb", fault: "resetNoCsw" });
  await toChecked(page);
  await writeAndRestart(page);
  await phase(page, "restarted");
  await expect(page.locator("#log")).toContainText("left the bus before its CSW");
});

test("the reset command does not reach the unit: stop with the reset advice", async ({ page }) => {
  await open(page, { start: "rockusb", fault: "resetOutFails" });
  await toChecked(page);
  await writeAndRestart(page);
  await refused(page, "reset-interrupted");
  await expect(page.locator("#stop-advice")).toContainText("written and read back");
});

test("a slow unit: after 30 s a note, but the transfer is not cut short; it completes when the unit answers", async ({ page }) => {
  await page.clock.install();
  await open(page, { start: "rockusb", fault: "holdFirstRead" });
  await loadImage(page);
  await phase(page, "image-ok");
  await page.click("#connect");
  await expect(page.locator("#busy")).toBeVisible();
  await page.clock.fastForward("05:00");
  await expect(page.locator("#busy")).toContainText("Still waiting for the unit (");
  await expect(page.locator("#busy")).toContainText("Do not unplug it");
  await expect(page.locator("#busy")).toContainText("This step only reads; nothing is written.");
  await expect(page.locator("#connect")).toBeDisabled();
  let r = await rec(page);
  expect(r.filter((e) => ["close", "usb-reset", "unplug"].includes(e.op))).toEqual([]);
  expect(cbws(r)).toEqual(["14:0:34"]); // the held transfer is the first read, the GPT
  await page.evaluate(() => globalThis.__fake.env.release());
  await phase(page, "download-checked");
  r = await rec(page);
  expect(cbws(r)).toEqual(["14:0:34", "14:3666944:8", "14:49156:1", "14:49184:4"]);
});

test("leaving the page during an entry asks first", async ({ page }) => {
  await open(page, { start: "rockusb" });
  await toChecked(page);
  const asked = await page.evaluate(() => {
    const ev = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(ev);
    return ev.defaultPrevented;
  });
  expect(asked).toBe(true);
});

test("the unit comes back in Maskrom after the reset: the page stops by itself", async ({ page }) => {
  await open(page, { start: "rockusb", afterReset: "maskrom" });
  await toChecked(page);
  await writeAndRestart(page);
  await refused(page, "maskrom");
  await expect(page.locator("#stop-advice")).toContainText("Do nothing more with it");
  await expect(page.locator("#connect-fastboot")).toBeDisabled();
  const r = await rec(page);
  const back = r.findIndex((e) => e.op === "attach" && e.n > r.findIndex((x) => x.op === "unplug"));
  expect(r.slice(back + 1).filter((e) => e.op === "open")).toEqual([]);
});
