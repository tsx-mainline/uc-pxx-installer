// SPDX-License-Identifier: GPL-2.0-or-later
// Every refusal: the page stops, says why, and writes nothing it should not.
import { body, connectFastboot,
  cbws, cmds, expect, IMAGE, loadImage, makeImage, notice, open, phase, PIN, rec, refused, saveLog, sha, test, toChecked,
  toFastbootChecked, writeAndRestart,
} from "./helpers.mjs";

// toFastboot: the rockusb route up to the press of "Connect to fastboot".
async function toFastboot(page) {
  await toChecked(page);
  await writeAndRestart(page);
  await phase(page, "restarted");
  await connectFastboot(page);
}

async function noDownload(page) {
  expect(cmds(await rec(page)).filter((c) => !c.startsWith("getvar:"))).toEqual([]);
}

// The reads before a refusal that fires after the gate (GPT, factory) and the
// A/B + BCB reads, with nothing written.
const GATE_READS = ["14:0:34", "14:3666944:8"];
const READS_ONLY = [...GATE_READS, "14:49156:1", "14:49184:4"];
const SLOT_VARS = ["current-slot", "slot-successful:a", "slot-successful:b", "slot-unbootable:a", "slot-unbootable:b",
  "slot-retry-count:a", "slot-retry-count:b"];

// A BCB read of only 0xCC (the download mode's "not served" byte) hides the
// command field, so a pending recovery could not be seen: stop, nothing written
// (parity with host/uc-pxx-enter exit 6). A block that is only MOSTLY 0xCC is a
// readable leftover and is written as before.
test("a BCB that reads all 0xCC (not served) is refused, nothing written", async ({ page }) => {
  await open(page, { start: "rockusb", bcb: "unserved" });
  await loadImage(page);
  await page.click("#connect");
  await stoppedFor(page, "bcb-unserved", "could not be read");
  expect(cbws(await rec(page))).toEqual(READS_ONLY);
});

async function noWrite(page) {
  const c = cbws(await rec(page));
  expect(c.filter((x) => x.startsWith("15:"))).toEqual([]);
  expect(c.filter((x) => x.startsWith("ff:"))).toEqual([]);
}

async function stoppedFor(page, code, text) {
  await refused(page, code);
  await expect(page.locator("#stop")).toBeVisible();
  if (text) await expect(page.locator("#stop-message")).toContainText(text);
  for (const b of ["#connect", "#write-bcb", "#connect-fastboot", "#boot"]) await expect(page.locator(b)).toBeDisabled();
  await expect(page.locator("#stop-save-log")).toBeEnabled();
}

test("Maskrom: stop at once, the device is never opened", async ({ page }) => {
  await open(page, { start: "maskrom" });
  await loadImage(page);
  await phase(page, "image-ok");
  await page.click("#connect");
  await stoppedFor(page, "maskrom", "Maskrom");
  await expect(page.locator("#stop-advice")).toContainText("Do nothing more with it");
  const r = await rec(page);
  expect(r.filter((e) => e.dev === "rockusb" && e.op !== "attach")).toEqual([]);
  const log = await saveLog(page, "#stop-save-log");
  expect(log.text).toContain("STOP maskrom");
});

test("a Rockchip device without bcdDevice 0x9999 is not taken for the download mode", async ({ page }) => {
  await open(page, { start: "rockusb", bcdDevice: 0x0100 });
  await loadImage(page);
  await page.click("#connect");
  await stoppedFor(page, "not-loader", "bcdDevice 0x100");
  expect((await rec(page)).filter((e) => e.op === "open")).toEqual([]);
});

test("two units connected: refused before anything is opened", async ({ page }) => {
  await open(page, { start: "rockusb", second: true });
  await loadImage(page);
  await page.click("#connect");
  await notice(page, "two-devices");
  expect((await rec(page)).filter((e) => e.op === "open")).toEqual([]);
  await expect(page.locator("#notice")).toContainText("Connect only one unit");
});

test("no device chosen in the browser list: nothing happens, no stop", async ({ page }) => {
  await open(page, { start: "rockusb", chooser: "cancel" });
  await loadImage(page);
  await page.click("#connect");
  await expect(page.locator("#log")).toContainText("no device chosen");
  await expect(page.locator("#connect")).toBeEnabled();
  await expect(page.locator("#stop")).toBeHidden();
});

test("open fails (no driver): a notice with the Windows driver advice, nothing sent", async ({ page }) => {
  await open(page, { start: "rockusb", openError: "SecurityError", platform: "Windows" });
  await loadImage(page);
  await page.click("#connect");
  await notice(page, "open-failed");
  await expect(page.locator("#notice")).toContainText("WinUSB");
  expect(cbws(await rec(page))).toEqual([]);
});

test("open fails on Linux: the udev advice", async ({ page }) => {
  await open(page, { start: "rockusb", openError: "SecurityError", platform: "Linux" });
  await loadImage(page);
  await page.click("#connect");
  await notice(page, "open-failed");
  await expect(page.locator("#notice")).toContainText("udev");
});

// The layout and the model gate (readGate), before the A/B and BCB reads.
test("a GPT without a signature: layout stop, nothing written, only the GPT was read", async ({ page }) => {
  await open(page, { start: "rockusb", gpt: "nosig" });
  await loadImage(page);
  await page.click("#connect");
  await stoppedFor(page, "layout", "GPT");
  expect(cbws(await rec(page))).toEqual(["14:0:34"]);
});

test("misc in the wrong place: layout stop", async ({ page }) => {
  await open(page, { start: "rockusb", gpt: "badmisc" });
  await loadImage(page);
  await page.click("#connect");
  await stoppedFor(page, "layout", "misc");
  expect(cbws(await rec(page))).toEqual(["14:0:34"]);
});

test("the unit identifies as a UC-P8: model stop, only the GPT and the factory block were read", async ({ page }) => {
  await open(page, { start: "rockusb", factory: "ucp8" });
  await loadImage(page);
  await page.click("#connect");
  await stoppedFor(page, "model", "UC-P8");
  await expect(page.locator("#stop-advice")).toContainText("This page is for the UC-P10");
  expect(cbws(await rec(page))).toEqual(GATE_READS);
});

test("a usable UC-P10 factory block: the model is shown and the entry goes on (its sha is not logged)", async ({ page }) => {
  await open(page, { start: "rockusb", factory: "ucp10" });
  await toChecked(page);
  await expect(page.locator("#log")).toContainText("factory data: model UC-P10");
  const log = await saveLog(page);
  expect(log.text).not.toMatch(/factory block.*[0-9a-f]{64}/);
});

const AB_CASES = [
  ["bad CRC", { crc: "bad" }, "crc32 mismatch"],
  ["CRC little-endian", { crc: "le" }, "little-endian"],
  ["the brick state (successful with tries 1)", { a: [15, 1, 1] }, "slot a in an illegal state"],
  ["next slot trying (D2)", { a: [14, 0, 1], b: [15, 7, 0] }, "boots next (b) is not marked successful"],
  ["tie", { a: [15, 0, 1], b: [15, 0, 1] }, "same priority"],
  ["no bootable slot", { a: [15, 0, 0], b: [0, 0, 0] }, "no bootable slot"],
  ["not an A/B sector", { abHex: "00".repeat(512) }, "bad magic"],
];
for (const [name, sc, text] of AB_CASES) {
  test(`A/B refused: ${name}; nothing written`, async ({ page }) => {
    await open(page, Object.assign({ start: "rockusb" }, sc));
    await loadImage(page);
    await page.click("#connect");
    await stoppedFor(page, "ab-refused", text);
    expect(cbws(await rec(page))).toEqual(READS_ONLY);
  });
}

// A recovery/OTA command U-Boot acts on (exact strcmp) must NOT be clobbered:
// the page stops before any write. Every OTHER command field (empty or an
// unrecognised leftover) is accepted and written over (see happy.spec).
for (const [name, bcb] of [["boot-recovery", "recovery"], ["boot_recovery", "boot_recovery"]]) {
  test(`a recovery/OTA command is pending in the BCB (${name}): STOP before any write; the raw bytes are NOT in the shared log (S-3)`, async ({ page }) => {
    await open(page, { start: "rockusb", bcb });
    await loadImage(page);
    await page.click("#connect");
    await stoppedFor(page, "bcb-command-pending", "A command is already set");
    expect(cbws(await rec(page))).toEqual(READS_ONLY);
    await expect(page.locator("#stop-advice")).toContainText("Nothing was written to the unit");
    await expect(page.locator("#stop-advice")).toContainText("recovery or update may be in progress");
    await expect(page.locator("#stop-advice")).toContainText("ask for help");
    const log = await saveLog(page, "#stop-save-log");
    // the command field (printable-escaped) and the sha are logged; the raw 2048 bytes are not (S-3)
    expect(log.text).toMatch(new RegExp(`BCB holds a command U-Boot acts on \\("${name}", sha256 [0-9a-f]{64}\\)`));
    expect(log.text).not.toMatch(/[0-9a-f]{4096}/);
  });
}

test("the BCB changes between the first read and the write: refused before the write", async ({ page }) => {
  await open(page, { start: "rockusb", fault: "changeBcbAfterFirstRead" });
  await toChecked(page);
  await writeAndRestart(page);
  await stoppedFor(page, "state-changed");
  await noWrite(page);
  await expect(page.locator("#stop-advice")).toContainText("Nothing was written");
});

for (const fault of ["ignoreWrite", "corruptWrite"]) {
  test(`read-back mismatch (${fault}): stop, no reset, "do not restart"; raw bytes not in the shared log`, async ({ page }) => {
    await open(page, { start: "rockusb", fault });
    await toChecked(page);
    await writeAndRestart(page);
    await stoppedFor(page, "readback-mismatch");
    await expect(page.locator("#stop-advice")).toContainText("do not restart it");
    const c = cbws(await rec(page));
    expect(c.slice(-2)).toEqual(["15:49184:4", "14:49184:4"]);
    expect(c.filter((x) => x.startsWith("ff:"))).toEqual([]);
    const log = await saveLog(page, "#stop-save-log");
    expect(log.text).toMatch(/BCB read back \(sha256 [0-9a-f]{64}/);
    expect(log.text).not.toMatch(/[0-9a-f]{4096}/);
  });
}

test("the A/B sector changes during the BCB write: stop before the reset", async ({ page }) => {
  await open(page, { start: "rockusb", fault: "abChangesOnWrite" });
  await toChecked(page);
  await writeAndRestart(page);
  await stoppedFor(page, "ab-changed");
  expect(cbws(await rec(page)).filter((x) => x.startsWith("ff:"))).toEqual([]);
});

test("a CSW with a wrong tag on the first read (the GPT): stop", async ({ page }) => {
  await open(page, { start: "rockusb", fault: "badCswTag" });
  await loadImage(page);
  await page.click("#connect");
  await stoppedFor(page, "read-interrupted", "CSW");
  expect(cbws(await rec(page))).toEqual(["14:0:34"]);
});

// --plain-entry: after the restart the unit must show exactly the measured
// libavb reset (current-slot a; slot-unbootable a and b "no"; slot-successful a
// and b "no"; slot-retry-count a 6, b 7). Any other post-entry state stops
// before the download, with the unit kept in fastboot.
const ENTRY_AB_FAILS = [
  ["a slot still successful after the entry (U-Boot did not reset)", [[15, 0, 1], [14, 7, 0]], 'slot-successful:a is "yes"'],
  ["the current slot took no try (tries 7)", [[15, 7, 0], [14, 7, 0]], 'slot-retry-count:a is "7", not 6'],
  ["a valid both-trying reset that runs slot B", [[14, 7, 0], [15, 6, 0]], 'current-slot is "b", not "a"'],
  ["the other slot nearly dead (B 1 try)", [[15, 6, 0], [14, 1, 0]], 'slot-retry-count:b is "1", not 7'],
  ["the other slot unbootable", [[15, 6, 0], [0, 0, 0]], 'slot-unbootable:b is "yes"'],
];
for (const [name, entryAb, text] of ENTRY_AB_FAILS) {
  test(`after the entry, ${name}: stop before the download, the unit is kept in fastboot`, async ({ page }) => {
    await open(page, { start: "rockusb", entryAb });
    await toFastboot(page);
    await stoppedFor(page, "entry-not-benign", text);
    await expect(page.locator("#stop-advice")).toContainText("do not restart it");
    await noDownload(page);
  });
}

const BENIGN_FAILS = [
  ["current-slot", "b", 'current-slot is "b", not "a"'],
  ["slot-successful:a", "yes", 'slot-successful:a is "yes", not "no"'],
  ["slot-successful:b", "yes", 'slot-successful:b is "yes", not "no"'],
  ["slot-unbootable:a", "yes", 'slot-unbootable:a is "yes", not "no"'],
  ["slot-unbootable:b", "yes", 'slot-unbootable:b is "yes", not "no"'],
  ["slot-retry-count:a", "7", 'slot-retry-count:a is "7", not 6'],
  ["slot-retry-count:a", "5", 'slot-retry-count:a is "5", not 6'],
  ["slot-retry-count:b", "6", 'slot-retry-count:b is "6", not 7'],
];
for (const [name, value, text] of BENIGN_FAILS) {
  test(`fastboot reports ${name} = ${value}, not the measured reset: stop before the download`, async ({ page }) => {
    await open(page, { start: "rockusb", getvarOverride: { [name]: value } });
    await toFastboot(page);
    await stoppedFor(page, "entry-not-benign", text);
    await noDownload(page);
  });
}

for (const name of SLOT_VARS) {
  test(`fastboot cannot read the required getvar ${name}: stop before the download`, async ({ page }) => {
    await open(page, { start: "rockusb", getvarFail: [name] });
    await toFastboot(page);
    await stoppedFor(page, "getvar-unreadable", name);
    await noDownload(page);
  });
}

test("fastboot cannot read max-download-size: stop before the download", async ({ page }) => {
  await open(page, { start: "rockusb", getvarFail: ["max-download-size"] });
  await toFastboot(page);
  await stoppedFor(page, "max-download", "not readable");
  await noDownload(page);
});

// product is informational on this route (check_plain_entry): a different
// product, or none, does NOT stop the page; the benign reset is the gate.
test("a fastboot device with another product: the page goes on (product is informational), logs the mismatch", async ({ page }) => {
  await open(page, { start: "rockusb", product: "OTHER-BOARD" });
  await toFastboot(page);
  await phase(page, "fastboot-checked");
  await expect(page.locator("#log")).toContainText('product "OTHER-BOARD" is not the expected "evb_rk3399" (logged, not a stop)');
});

test("no expected product recorded (config): the page still goes on", async ({ page }) => {
  await open(page, { start: "rockusb" }, { product: null });
  await expect(page.locator("#log")).toContainText("the product is informational on this route");
  await toFastboot(page);
  await phase(page, "fastboot-checked");
});

test("a fastboot device with another USB serial string than the download mode: stop (identity check)", async ({ page }) => {
  await open(page, { start: "rockusb", fastbootSerial: "OTHER0001" });
  await toFastboot(page);
  await stoppedFor(page, "other-device", "another USB serial string");
  expect(cmds(await rec(page))).toEqual([]);
  const log = await saveLog(page, "#stop-save-log");
  expect(log.text).not.toContain("OTHER0001");
  expect(log.text).not.toContain("FAKE-SERIAL");
});

test("no USB serial string in fastboot, but one in download mode: stop (identity check), no getvar", async ({ page }) => {
  await open(page, { start: "rockusb", fastbootSerial: "" });
  await toFastboot(page);
  await stoppedFor(page, "other-device", "no USB serial string, but the unit in download mode had one");
  await expect(page.locator("#stop-advice")).toContainText("do not restart it");
  expect(cmds(await rec(page))).toEqual([]);
  const log = await saveLog(page, "#stop-save-log");
  expect(log.text).not.toContain("FAKE-SERIAL");
});

test("no USB serial string in download mode: not compared, said in the log, the benign-reset check still runs", async ({ page }) => {
  await open(page, { start: "rockusb", rockusbSerial: "" });
  await toFastboot(page);
  await phase(page, "fastboot-checked");
  await expect(page.locator("#log")).toContainText("fastboot device: not compared (the download-mode device had no USB serial string)");
});

test("unit already in fastboot: refused before it is opened, no getvar (no pre-entry state)", async ({ page }) => {
  await open(page, { start: "fastboot" });
  await loadImage(page);
  await page.click("#connect");
  await stoppedFor(page, "no-pre-entry", "already in fastboot");
  await expect(page.locator("#stop-advice")).toContainText("start it in download mode");
  const r = await rec(page);
  expect(r.filter((e) => e.dev === "fastboot" && e.op !== "attach")).toEqual([]);
});

// The pre-entry A/B rule (otherSlotKind): the next slot a keeper, the other a
// keeper or never tried. The never-tried factory state is ACCEPTED.
test("the factory never-tried state (A{15,0,1} B{14,7,0}) is accepted and goes on", async ({ page }) => {
  await open(page, { start: "rockusb", a: [15, 0, 1], b: [14, 7, 0] });
  await toChecked(page);
  await expect(page.locator("#unit-status")).toContainText("Slot a boots next, the other slot is the fallback");
});

// The entry's A/B reset always makes slot A the installer's keeper. A unit that
// runs slot B while slot A was never successful would keep an unproven A and
// lose its running B: refused before any write. (Both successful while B runs,
// D3, is accepted with a note: see happy.spec.mjs.)
for (const [name, a, b] of [["slot B runs, A never tried (factory-like)", [14, 7, 0], [15, 0, 1]],
  ["slot B runs, A never tried, low priority", [2, 7, 0], [15, 0, 1]]]) {
  test(`a unit that runs slot B with an unproven slot A is refused, nothing written: ${name}`, async ({ page }) => {
    await open(page, { start: "rockusb", a, b });
    await loadImage(page);
    await page.click("#connect");
    await stoppedFor(page, "ab-runs-slot-b", "This unit runs slot B, and slot A is not marked as successful.");
    await expect(page.locator("#stop-advice")).toContainText("This unit runs slot B, and its slot A is not marked as successful.");
    await expect(page.locator("#stop-advice")).toContainText("Nothing was written to the unit");
    expect(cbws(await rec(page))).toEqual(READS_ONLY);
  });
}

for (const [name, a, b] of [["D1 (B disabled)", [15, 0, 1], [0, 0, 0]], ["B trying lower, only 5 tries", [15, 0, 1], [14, 5, 0]],
  ["A trying lower, only 5 tries", [3, 5, 0], [15, 0, 1]], ["B unbootable with priority", [15, 0, 1], [14, 0, 0]]]) {
  test(`the A/B pair is not a keeper + (keeper or never-tried): ${name} is refused, nothing written`, async ({ page }) => {
    await open(page, { start: "rockusb", a, b });
    await loadImage(page);
    await page.click("#connect");
    await stoppedFor(page, "ab-not-bootable-pair", "successful too or never tried");
    expect(cbws(await rec(page))).toEqual(READS_ONLY);
  });
}

test("bound to another A/B sector (?expect-ab=D3 sha): refused, nothing written", async ({ page }) => {
  await open(page, { start: "rockusb" }, { query: "?expect-ab=0f88402d8a90d16d607e7925703aa0dfafb4e227a9623c976c2261a8ef70820e" });
  await loadImage(page);
  await page.click("#connect");
  await stoppedFor(page, "ab-not-expected", "0f88402d");
  expect(cbws(await rec(page))).toEqual(READS_ONLY);
});

test("bound in config.js to another A/B sector: refused, nothing written", async ({ page }) => {
  await open(page, { start: "rockusb" }, { expectAbConfig: "0f88402d8a90d16d607e7925703aa0dfafb4e227a9623c976c2261a8ef70820e" });
  await loadImage(page);
  await page.click("#connect");
  await stoppedFor(page, "ab-not-expected");
});

test("config.js and the URL both bind: both must match (the URL cannot loosen config.js)", async ({ page }) => {
  await open(page, { start: "rockusb" }, {
    expectAbConfig: "0f88402d8a90d16d607e7925703aa0dfafb4e227a9623c976c2261a8ef70820e",
    query: "?expect-ab=1d2e3769805d32c2065ac3ff7e204ebf60921c0d2d3f66f0d47612d0889d7d2d",
  });
  await loadImage(page);
  await page.click("#connect");
  await stoppedFor(page, "ab-not-expected");
});

for (const q of ["?expect-ab=1D2E3769805D32C2065AC3FF7E204EBF60921C0D2D3F66F0D47612D0889D7D2D", "?expect-ab=1d2e", "?expect-ab="]) {
  test(`a malformed expect-ab parameter (${q.slice(11, 19) || "empty"}) turns the device buttons off`, async ({ page }) => {
    await open(page, { start: "rockusb" }, { query: q });
    await expect(page.locator("#binding")).toContainText("must be 64 lowercase hex digits");
    await loadImage(page);
    await phase(page, "image-ok");
    await expect(page.locator("#connect")).toBeDisabled();
    await page.evaluate(() => document.getElementById("connect").removeAttribute("disabled"));
    await page.click("#connect");
    await notice(page, "bad-parameter");
    expect((await rec(page)).filter((e) => e.op !== "attach")).toEqual([]);
  });
}

test("a second unit appears between the read and the write: refused before the write", async ({ page }) => {
  await open(page, { start: "rockusb", secondAfterRead: true });
  await toChecked(page);
  await writeAndRestart(page);
  await notice(page, "two-devices");
  expect(cbws(await rec(page))).toEqual(READS_ONLY);
});

test("an unexpected error after the write started: the advice says the write may have happened (review F5)", async ({ page }) => {
  await open(page, { start: "rockusb", fault: "badDataAfterWrite" });
  await toChecked(page);
  await writeAndRestart(page);
  await stoppedFor(page, "unexpected-after-write", "Unexpected error");
  await expect(page.locator("#stop-advice")).toContainText("while the page wrote its boot command");
  expect(cbws(await rec(page)).filter((c) => c.startsWith("ff:"))).toEqual([]);
});

test("max-download-size smaller than the image: stop before the download", async ({ page }) => {
  await open(page, { start: "rockusb", maxDownload: "0x00100000" });
  await toFastbootChecked(page);
  await page.click("#boot");
  await stoppedFor(page, "max-download", "larger than max-download-size");
  await expect(page.locator("#stop-advice")).toContainText("Earlier it wrote only the boot command");
  await noDownload(page);
});

test("max-download-size not a number: stop", async ({ page }) => {
  await open(page, { start: "rockusb", maxDownload: "128M" });
  await toFastboot(page);
  await stoppedFor(page, "max-download", "not readable");
});

test("fastboot FAIL on download: stop, no boot", async ({ page }) => {
  await open(page, { start: "rockusb", fault: "downloadFail" });
  await toFastbootChecked(page);
  await page.click("#boot");
  await stoppedFor(page, "fastboot-fail");
  expect(cmds(await rec(page))).not.toContain("boot");
});

test("fastboot FAIL on boot: stop", async ({ page }) => {
  await open(page, { start: "rockusb", fault: "bootFail" });
  await toFastbootChecked(page);
  await page.click("#boot");
  await stoppedFor(page, "fastboot-fail", "boot");
});

test("wrong image sha: refused before any USB action, connect stays off", async ({ page }) => {
  await open(page, { start: "rockusb" });
  const bad = Buffer.from(IMAGE);
  bad[bad.length - 1] ^= 1;
  await loadImage(page, bad);
  await notice(page, "image-sha");
  await expect(page.locator("#notice")).toContainText(sha(bad));
  await expect(page.locator("#connect")).toBeDisabled();
  expect((await rec(page)).filter((e) => e.op !== "attach")).toEqual([]);
});

test("known sha but the wrong size or header: refused", async ({ page }) => {
  const noMagic = makeImage(4096, false);
  await open(page, { start: "rockusb" }, { pins: [{ ...PIN, size: noMagic.length, sha256: sha(noMagic) }] });
  await loadImage(page, noMagic);
  await notice(page, "image-format");
  await expect(page.locator("#connect")).toBeDisabled();
});

test("an empty file: refused", async ({ page }) => {
  await open(page, { start: "rockusb" });
  await loadImage(page, Buffer.alloc(0));
  await notice(page, "image-empty");
});

test("the site has no image: a notice, the file choice still works", async ({ page }) => {
  await open(page, { start: "rockusb" }, { noSiteImage: true });
  await page.click(".image-site");
  await notice(page, "image-missing");
  await loadImage(page);
  await phase(page, "image-ok");
});

test("the image download from the site fails (network error): a notice, not a stop; the file choice still works", async ({ page }) => {
  await open(page, { start: "rockusb" }, { siteImage: "abort" });
  await page.click(".image-site");
  await notice(page, "image-missing");
  await expect(page.locator("#notice")).toContainText("could not download the image from this site");
  await expect(page.locator("#notice")).toContainText("Download the image from the project's Releases page and choose the file");
  await expect(page.locator("#stop")).toBeHidden();
  await expect(body(page)).not.toHaveAttribute("data-refusal", /./);
  await expect(page.locator("#image-file")).toBeEnabled();
  await expect(page.locator(".image-site")).toBeEnabled();
  // the user downloads the image and chooses the file: a usable image, the entry goes on
  await toChecked(page);
  await expect(page.locator("#image-status")).toContainText(sha(IMAGE));
});

test("a good image, then a bad one: the page forgets the good one", async ({ page }) => {
  await open(page, { start: "rockusb" });
  await loadImage(page);
  await phase(page, "image-ok");
  await loadImage(page, makeImage(1000));
  await notice(page, "image-sha");
  await phase(page, "start");
  await expect(page.locator("#connect")).toBeDisabled();
});
