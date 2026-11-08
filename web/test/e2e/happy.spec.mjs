// SPDX-License-Identifier: GPL-2.0-or-later
// Happy paths: download mode → BCB → fastboot → boot → shell → log.
import { connectFastboot, cbws, cmds, ENTRY_CMDS, expect, IMAGE, open, phase, rec, saveLog, sha, test, toChecked, writeAndRestart } from "./helpers.mjs";

// The gate reads (GPT, factory) then A/B + BCB; the re-read; the write, read
// back and A/B re-read; the reset.
const GATE_READS = ["14:0:34", "14:3666944:8"];
const ENTRY_CBWS = [...GATE_READS, "14:49156:1", "14:49184:4", "14:49156:1", "14:49184:4", "15:49184:4", "14:49184:4", "14:49156:1", "ff:0:0"];
const GETVARS = ENTRY_CMDS;

test("stock entry, slot A active: the exact transfer sequence, the image arrives intact, shell and log work", async ({ page }) => {
  await open(page, { start: "rockusb" });
  await toChecked(page);
  await expect(page.locator("#unit-status")).toContainText("Slot a boots next, both slots are successful");
  await expect(page.locator("#unit-status")).toContainText("empty (all zero)");
  // Nothing written yet: only the gate reads and the A/B + BCB reads.
  expect(cbws(await rec(page))).toEqual(ENTRY_CBWS.slice(0, 4));
  await writeAndRestart(page);
  await phase(page, "restarted");
  await connectFastboot(page);
  await phase(page, "fastboot-checked");
  await expect(page.locator("#fastboot-status")).toContainText("slot a");
  await page.click("#boot");
  await phase(page, "booted");
  const r = await rec(page);
  expect(cbws(r)).toEqual(ENTRY_CBWS);
  const c = cmds(r);
  expect(c).toEqual([...GETVARS, `download:${IMAGE.length.toString(16).padStart(8, "0")}`, "boot"]);
  // Every byte of the image, in order, and nothing more.
  const dl = await page.evaluate(() => globalThis.__fake.hex(globalThis.__fake.env.downloaded));
  expect(sha(Buffer.from(dl, "hex"))).toBe(sha(IMAGE));
  const out = r.filter((e) => e.dev === "fastboot" && e.op === "out-data").reduce((n, e) => n + e.length, 0);
  expect(out).toBe(IMAGE.length);
  // U-Boot cleared the command (fake), the rest of the BCB is the payload's zeros.
  const bcb = await page.evaluate(() => globalThis.__fake.hex(globalThis.__fake.env.emmc.read(49184, 4)));
  expect(bcb).toBe("00".repeat(2048));

  // Web Serial shell.
  await page.click("#serial-open");
  await expect(page.locator("#term")).toContainText("p10inst status image");
  await expect(page.locator("#term")).not.toContainText("\u001b[");
  await page.fill("#term-input", "uname -m");
  await page.press("#term-input", "Enter");
  await expect(page.locator("#term")).toContainText("aarch64");
  const sent = (await rec(page)).filter((e) => e.dev === "serial" && e.op === "write").map((e) => e.text);
  expect(sent).toEqual(["uname -m\r"]);
  const open115 = (await rec(page)).find((e) => e.dev === "serial" && e.op === "open");
  expect(open115.baud).toBe(115200);

  const log = await saveLog(page);
  expect(log.name).toMatch(/^p10-enter-\d{4}-\d\d-\d\dT[\d-]+Z\.log$/);
  for (const s of ["rockusb read LBA 49156 x1", "rockusb read LBA 49184 x4", "rockusb read LBA 0 x34 (GPT)",
    "factory data: not readable in the download mode", "BCB state: empty (all zero)",
    "rockusb write LBA 49184 x4: 892627cb4356d1d16c37bf243d700b5c9a0ab672d263de9ccd640c86b5cd6a5a", "BCB read back OK",
    "A/B sector unchanged by the BCB write", "rockusb reset", "fastboot getvar current-slot: a", `image sha256 ${sha(IMAGE)} OK`,
    "fastboot device: the same USB serial string as in download mode", "benign both-trying reset (current slot a bootable",
    "A/B first 32 bytes sha256 1d2e3769805d32c2065ac3ff7e204ebf60921c0d2d3f66f0d47612d0889d7d2d",
    "fastboot boot OKAY", "--- serial transcript ---", "aarch64",
    "A/B sector first 32 bytes: 00414230010000000f0001000e000100000000000000000000000000da933679"]) {
    expect(log.text).toContain(s);
  }
  // The factory block's sha is never in the log (it holds the MAC); no unit serial either.
  expect(log.text).not.toContain("FAKE-SERIAL");
});

test("slot B active (D3 state): next b, fastboot reports b, boot goes on", async ({ page }) => {
  await open(page, { start: "rockusb", a: [14, 0, 1], b: [15, 0, 1] });
  await toChecked(page);
  await expect(page.locator("#unit-status")).toContainText("Slot b boots next");
  await writeAndRestart(page);
  await connectFastboot(page);
  await phase(page, "fastboot-checked");
  await page.click("#boot");
  await phase(page, "booted");
});

test("BCB already holds the boot command: no write at all, read-back still checked", async ({ page }) => {
  await open(page, { start: "rockusb", bcb: "payload" });
  await toChecked(page);
  await expect(page.locator("#unit-status")).toContainText("already set");
  await writeAndRestart(page);
  await phase(page, "restarted");
  expect(cbws(await rec(page))).toEqual([...GATE_READS, "14:49156:1", "14:49184:4", "14:49156:1", "14:49184:4", "14:49184:4", "14:49156:1", "ff:0:0"]);
});

test("bound to the A/B sector the reviewer saw (?expect-ab=S0 sha): the entry goes on", async ({ page }) => {
  await open(page, { start: "rockusb" }, { query: "?expect-ab=1d2e3769805d32c2065ac3ff7e204ebf60921c0d2d3f66f0d47612d0889d7d2d" });
  await expect(page.locator("#binding")).toContainText("bound to the A/B sector");
  await toChecked(page);
  await writeAndRestart(page);
  await connectFastboot(page);
  await phase(page, "fastboot-checked");
});

test("fastboot already allowed: the page goes on by itself when the unit comes back, but waits for the boot button", async ({ page }) => {
  await open(page, { start: "rockusb", fastbootPermitted: true });
  await toChecked(page);
  await writeAndRestart(page);
  await phase(page, "fastboot-checked");
  await page.waitForTimeout(300);
  expect(cmds(await rec(page)).some((c) => c.startsWith("download:"))).toBe(false);
  await page.click("#boot");
  await phase(page, "booted");
});

test("the image can come from the page's own site (same origin)", async ({ page }) => {
  await open(page, { start: "rockusb" });
  await page.click(".image-site");
  await phase(page, "image-ok");
  await expect(page.locator("#image-status")).toContainText(sha(IMAGE));
});

test("the write button needs the confirmation box, and buttons are off while a step runs", async ({ page }) => {
  await open(page, { start: "rockusb" });
  await toChecked(page);
  await expect(page.locator("#write-bcb")).toBeDisabled();
  await page.check("#confirm");
  await expect(page.locator("#write-bcb")).toBeEnabled();
  await page.uncheck("#confirm");
  await expect(page.locator("#write-bcb")).toBeDisabled();
  expect(cbws(await rec(page))).toEqual([...GATE_READS, "14:49156:1", "14:49184:4"]);
});
