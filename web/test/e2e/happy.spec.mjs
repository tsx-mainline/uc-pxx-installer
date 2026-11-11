// SPDX-License-Identifier: GPL-2.0-or-later
// Happy paths: download mode → BCB → fastboot → boot → shell → log.
import { body, connectFastboot, cbws, cmds, ENTRY_CMDS, expect, IMAGE, open, phase, rec, saveLog, sha, test, toChecked, writeAndRestart } from "./helpers.mjs";

// The gate reads (GPT, factory) then A/B + BCB; the re-read; the write, read
// back and A/B re-read; the reset.
const GATE_READS = ["14:0:34", "14:3666944:8"];
const ENTRY_CBWS = [...GATE_READS, "14:49156:1", "14:49184:4", "14:49156:1", "14:49184:4", "15:49184:4", "14:49184:4", "14:49156:1", "ff:0:0"];
const GETVARS = ENTRY_CMDS;

test("stock entry, slot A active: the exact transfer sequence, the image arrives intact, shell and log work", async ({ page }) => {
  await open(page, { start: "rockusb" });
  await toChecked(page);
  await expect(page.locator("#unit-status")).toContainText("Slot a boots next, both slots are successful");
  await expect(page.locator("#unit-status")).toContainText("empty (no command set)");
  // Nothing written yet: only the gate reads and the A/B + BCB reads.
  expect(cbws(await rec(page))).toEqual(ENTRY_CBWS.slice(0, 4));
  await writeAndRestart(page);
  await phase(page, "restarted");
  // First run: the fastboot device is a new USB device the page has no
  // permission for, so the watch cannot see it yet; the step is not "ready" and
  // the user presses the button (which asks for the device).
  await expect(body(page)).toHaveAttribute("data-fastboot-ready", "no");
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
  await expect(page.locator("#term")).toContainText("uc-pxx-inst status image");
  await expect(page.locator("#term")).not.toContainText("\u001b[");
  await page.fill("#term-input", "uname -m");
  await page.press("#term-input", "Enter");
  await expect(page.locator("#term")).toContainText("aarch64");
  const sent = (await rec(page)).filter((e) => e.dev === "serial" && e.op === "write").map((e) => e.text);
  expect(sent).toEqual(["uname -m\r"]);
  const open115 = (await rec(page)).find((e) => e.dev === "serial" && e.op === "open");
  expect(open115.baud).toBe(115200);

  const log = await saveLog(page);
  expect(log.name).toMatch(/^uc-pxx-enter-\d{4}-\d\d-\d\dT[\d-]+Z\.log$/);
  for (const s of ["rockusb read LBA 49156 x1", "rockusb read LBA 49184 x4", "rockusb read LBA 0 x34 (GPT)",
    "factory data: not readable in the download mode", "BCB state: empty (no command set)",
    "rockusb write LBA 49184 x4: 892627cb4356d1d16c37bf243d700b5c9a0ab672d263de9ccd640c86b5cd6a5a", "BCB read back OK",
    "A/B sector unchanged by the BCB write", "rockusb reset", "fastboot getvar current-slot: a", `image sha256 ${sha(IMAGE)} OK`,
    "fastboot device: the same USB serial string as in download mode",
    'fastboot: benign both-trying reset (current-slot "a"; slot-unbootable a and b both "no"; slot-successful a and b ' +
      'both "no"; slot-retry-count a == 6 and b == 7): current slot a, retry a=6 b=7',
    "fastboot getvar slot-retry-count:a: 6", "fastboot getvar slot-retry-count:b: 7",
    "A/B first 32 bytes sha256 1d2e3769805d32c2065ac3ff7e204ebf60921c0d2d3f66f0d47612d0889d7d2d",
    "fastboot boot OKAY", "--- serial transcript ---", "aarch64",
    "A/B sector first 32 bytes: 00414230010000000f0001000e000100000000000000000000000000da933679"]) {
    expect(log.text).toContain(s);
  }
  // The factory block's sha is never in the log (it holds the MAC); no unit serial either.
  expect(log.text).not.toContain("FAKE-SERIAL");
});

// The two 2026-11-08 hardware runs, pinned: pre-entry D3 A{14,0,1} B{15,0,1}
// (slot B runs, slot A successful), the post-entry getvars the measured libavb
// reset (current-slot a; unbootable a/b no; successful a/b no; retry a 6, b 7),
// the same USB serial string in both modes; one run had the leftover BCB
// command field d8 85 00 ... that the page wrote over. Both must stay accepted.
const MEASURED = { "current-slot": "a", "slot-unbootable:a": "no", "slot-unbootable:b": "no", "slot-successful:a": "no",
  "slot-successful:b": "no", "slot-retry-count:a": "6", "slot-retry-count:b": "7" };
for (const [name, bcb] of [["empty BCB", undefined], ["leftover BCB command field d8 85 00", "hw-leftover"]]) {
  test(`hardware run 2026-11-08: D3 (slot B runs, both successful), ${name}: accepted with a note, boots`, async ({ page }) => {
    const sc = { start: "rockusb", a: [14, 0, 1], b: [15, 0, 1], getvarOverride: MEASURED };
    if (bcb) sc.bcb = bcb;
    await open(page, sc);
    await toChecked(page);
    await expect(page.locator("#unit-status")).toContainText("Slot b boots next, both slots are successful");
    await expect(page.locator("#unit-status")).toContainText("The installer keeps slot A");
    if (bcb) await expect(page.locator("#unit-status")).toContainText('unrecognised leftover command ("??")');
    await writeAndRestart(page);
    await phase(page, "restarted");
    expect(cbws(await rec(page))).toEqual(ENTRY_CBWS);
    await connectFastboot(page);
    await phase(page, "fastboot-checked");
    await expect(page.locator("#fastboot-status")).toContainText("slot a");
    await page.click("#boot");
    await phase(page, "booted");
    const log = await saveLog(page);
    for (const s of ["A/B first 32 bytes sha256 0f88402d8a90d16d607e7925703aa0dfafb4e227a9623c976c2261a8ef70820e",
      "this unit runs slot b; slot a is successful too", "fastboot device: the same USB serial string as in download mode",
      "fastboot getvar current-slot: a", "fastboot getvar slot-retry-count:a: 6", "fastboot getvar slot-retry-count:b: 7",
      "fastboot: benign both-trying reset", "fastboot boot OKAY"]) {
      expect(log.text).toContain(s);
    }
    if (bcb) expect(log.text).toContain('overwriting an unrecognised leftover BCB command field: "??"');
    const bcbNow = await page.evaluate(() => globalThis.__fake.hex(globalThis.__fake.env.emmc.read(49184, 4)));
    expect(bcbNow).toBe("00".repeat(2048)); // the payload was written, then U-Boot cleared the command
  });
}

test("slot B active (D3) with the fake's own reset model: the reset is A{15,6,0} B{14,7,0}, boot goes on", async ({ page }) => {
  await open(page, { start: "rockusb", a: [14, 0, 1], b: [15, 0, 1] });
  await toChecked(page);
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

test("\"bootonce-bootloader\" with a non-zero rest of the block: written over (no false read-back mismatch), boots", async ({ page }) => {
  await open(page, { start: "rockusb", bcb: "payload-tail" });
  await toChecked(page);
  await expect(page.locator("#unit-status")).toContainText("the boot command, with a leftover in the rest of the block");
  await writeAndRestart(page);
  await phase(page, "restarted");
  expect(cbws(await rec(page))).toEqual(ENTRY_CBWS); // the payload is written
  await connectFastboot(page);
  await phase(page, "fastboot-checked");
  const log = await saveLog(page);
  expect(log.text).toContain("overwriting the BCB: its command field holds the boot command, but the rest of the block");
  expect(log.text).toContain("BCB read back OK");
  expect(log.text).not.toContain("readback-mismatch");
});

test("an unrecognised leftover BCB command field (\"b?\"): the page overwrites it, logs the overwrite, and boots", async ({ page }) => {
  await open(page, { start: "rockusb", bcb: "bq-leftover" });
  await toChecked(page);
  await expect(page.locator("#unit-status")).toContainText('unrecognised leftover command ("b?")');
  await writeAndRestart(page);
  await phase(page, "restarted");
  // the transfer shape is identical to the empty-field entry (it writes the payload)
  expect(cbws(await rec(page))).toEqual(ENTRY_CBWS);
  await connectFastboot(page);
  await phase(page, "fastboot-checked");
  await page.click("#boot");
  await phase(page, "booted");
  const log = await saveLog(page);
  expect(log.text).toContain('BCB state: an unrecognised leftover command ("b?")');
  expect(log.text).toContain('overwriting an unrecognised leftover BCB command field: "b?"');
  // the raw 2048-byte BCB (which may hold U-Boot RAM) is never in the shared log
  expect(log.text).not.toMatch(/[0-9a-f]{4096}/);
});

test("bound to the A/B sector the reviewer saw (?expect-ab=S0 sha): the entry goes on", async ({ page }) => {
  await open(page, { start: "rockusb" }, { query: "?expect-ab=1d2e3769805d32c2065ac3ff7e204ebf60921c0d2d3f66f0d47612d0889d7d2d" });
  await expect(page.locator("#binding")).toContainText("bound to the A/B sector");
  await toChecked(page);
  await writeAndRestart(page);
  await connectFastboot(page);
  await phase(page, "fastboot-checked");
});

test("fastboot already allowed: the page detects the unit and signals readiness, waits for the user, then connects and boots", async ({ page }) => {
  await open(page, { start: "rockusb", fastbootPermitted: true });
  await toChecked(page);
  await writeAndRestart(page);
  await phase(page, "restarted");
  // The watch (getDevices poll + connect event) finds the already-permitted
  // fastboot device and signals readiness; it does NOT connect on its own.
  await expect(body(page)).toHaveAttribute("data-fastboot-ready", "yes");
  await expect(page.locator("#fastboot-status")).toContainText("Fastboot detected");
  await page.waitForTimeout(200);
  await phase(page, "restarted"); // still waiting for the user: no auto-connect
  expect(cmds(await rec(page)).some((c) => c.startsWith("getvar:"))).toBe(false); // nothing read from the unit yet
  // The user presses the button; the detected device is used with no chooser.
  await page.click("#connect-fastboot");
  await phase(page, "fastboot-checked");
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
