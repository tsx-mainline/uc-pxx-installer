// SPDX-License-Identifier: GPL-2.0-or-later
// Static checks of the page sources: the structural guards that a reader can
// see without running anything.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const SITE = fileURLToPath(new URL("../../site/", import.meta.url));
const JS = join(SITE, "js");
const jsFiles = readdirSync(JS).filter((f) => f.endsWith(".js"));
const src = Object.fromEntries(jsFiles.map((f) => [f, readFileSync(join(JS, f), "utf8")]));

// code removes // and /* */ comments (good enough for these files: no
// comment markers inside strings, checked below).
function code(s) {
  return s.replace(/\/\*[\s\S]*?\*\//g, "").split("\n").map((l) => l.replace(/(^|[^:"'`])\/\/.*$/, "$1")).join("\n");
}

test("the expected files, each with the GPL-2.0-or-later SPDX line", () => {
  assert.deepEqual(jsFiles.sort(), ["avbab.js", "bcb.js", "config.js", "enter.js", "factory.js", "fastboot.js", "gpt.js", "main.js", "rockusb.js", "serial.js", "usbio.js"]);
  for (const [f, s] of Object.entries(src)) assert.match(s, /SPDX-License-Identifier: GPL-2.0-or-later/, f);
  for (const f of ["index.html", "style.css"]) assert.match(readFileSync(join(SITE, f), "utf8"), /SPDX-License-Identifier: GPL-2.0-or-later/, f);
  assert.match(readFileSync(join(SITE, "LICENSE.txt"), "utf8"), /GNU GENERAL PUBLIC LICENSE\s+Version 2, June 1991/);
});

test("rockusb.js carries the rkdeveloptool notice, the pinned commit and the change note", () => {
  const s = src["rockusb.js"];
  assert.match(s, /rockchip-linux\/rkdeveloptool/);
  assert.match(s, /304f073752fd25c854e1bcf05d8e7f925b1f4e14/);
  assert.match(s, /\(C\) Copyright 2017 Fuzhou Rockchip Electronics Co\., Ltd/);
  assert.match(s, /Changes \(2026-10-29/);
  assert.match(s, /either version 2 of the License, or \(at your option\)\s+\* any later version/);
});

test("LBA 49156 is a constant in one place, used only for one read", () => {
  const hits = [];
  for (const [f, s] of Object.entries(src)) {
    code(s).split("\n").forEach((l, i) => {
      if (/49156/.test(l)) hits.push(`${f}:${i + 1}:${l.trim()}`);
    });
  }
  assert.equal(hits.length, 1, hits.join("\n"));
  assert.match(hits[0], /^rockusb\.js:\d+:export const AB_LBA = 49156;$/);
  for (const [f, s] of Object.entries(src)) if (f !== "rockusb.js") assert.doesNotMatch(code(s), /\bAB_LBA\b/, f);
  const uses = code(src["rockusb.js"]).split("\n").filter((l) => /\bAB_LBA\b/.test(l)).map((l) => l.trim());
  assert.deepEqual(uses, [
    "export const AB_LBA = 49156;",
    "Object.freeze([AB_LBA, 1]),",
    'if (lba <= AB_LBA && AB_LBA < lba + count) throw new Error("rockusb: a write over the A/B sector is refused");',
    "const d = await this.#transact(OP.READ_LBA, AB_LBA, 1, null, false);",
    "this.#log(`rockusb read LBA ${AB_LBA} x1: ${await sha256hex(d)}`);",
  ]);
});

test("LBA 49184 is a constant; the one write uses it and the reviewed payload", () => {
  const c = code(src["rockusb.js"]);
  assert.match(c, /export const BCB_LBA = 49184;/);
  assert.match(c, /export const BCB_SECTORS = 4;/);
  const writes = c.split("\n").filter((l) => /WRITE_LBA/.test(l)).map((l) => l.trim());
  assert.deepEqual(writes, [
    "export const OP = Object.freeze({ READ_LBA: 0x14, WRITE_LBA: 0x15, DEVICE_RESET: 0xff });",
    "if (op === OP.WRITE_LBA) {",
    "if ((op === OP.WRITE_LBA) !== (dataOut !== null)) throw new Error(\"rockusb: data only with a write\");",
    "if (op === OP.WRITE_LBA) await bulkOut(this.#dev, this.#ep.epOut, dataOut);",
    "await this.#transact(OP.WRITE_LBA, BCB_LBA, BCB_SECTORS, payload, false);",
  ]);
  assert.match(c, /if \(\(await sha256hex\(payload\)\) !== PAYLOAD_SHA256\) throw/);
  for (const [f, s] of Object.entries(src)) {
    if (f !== "rockusb.js") assert.doesNotMatch(code(s), /WRITE_LBA|0x15\b|49184/, f);
  }
  // every decimal number of 4+ digits in rockusb.js: no other LBA is named
  // there (2048 and 2207 are in two error texts; 3666944 is the factory read)
  const nums = new Set(c.match(/\b\d{4,}\b/g));
  assert.deepEqual([...nums].sort(), ["2048", "2207", "3666944", "49156", "49184"]);
});

test("only usbio.js calls transferIn/transferOut; nothing resets, forgets or control-transfers", () => {
  for (const [f, s] of Object.entries(src)) {
    const c = code(s);
    if (f !== "usbio.js") assert.doesNotMatch(c, /transfer(In|Out)\s*\(/, f);
    assert.doesNotMatch(c, /controlTransfer|isochronous|\.forget\s*\(|\.reset\s*\(|clearHalt/, f);
    if (f !== "usbio.js") assert.doesNotMatch(c, /\.close\s*\(/, f);
  }
  assert.doesNotMatch(code(src["usbio.js"]), /setTimeout|AbortController|Promise\.race/);
});

test("fastboot.js sends only guarded commands; no getvar all anywhere", () => {
  const c = code(src["fastboot.js"]);
  assert.match(c, /if \(!commandAllowed\(cmd\)\) throw/);
  const sends = c.split("\n").filter((l) => /#command\(/.test(l)).map((l) => l.trim());
  assert.equal(sends.length, 4, sends.join("\n")); // the definition, getvar, download, boot
  for (const [f, s] of Object.entries(src)) {
    assert.doesNotMatch(code(s), /getvar:all|"all"|flash:|erase:|oem |set_active|reboot|continue"/, f);
  }
});

test("no third-party request, no telemetry, no storage", () => {
  const html = readFileSync(join(SITE, "index.html"), "utf8");
  assert.match(html, /<meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'">/);
  assert.match(html, /<meta name="referrer" content="no-referrer">/);
  assert.doesNotMatch(html.replace(/<!--[\s\S]*?-->/g, ""), /https?:|\/\/[a-z]/i);
  assert.doesNotMatch(readFileSync(join(SITE, "style.css"), "utf8"), /url\(|@import|https?:/);
  for (const [f, s] of Object.entries(src)) {
    const c = code(s);
    assert.doesNotMatch(c, /https?:|XMLHttpRequest|WebSocket|sendBeacon|EventSource|localStorage|sessionStorage|indexedDB|document\.cookie|import\(|serviceWorker|eval\(|new Function/, f);
    if (f !== "enter.js") assert.doesNotMatch(c, /serialNumber/, f);
    if (f !== "main.js") assert.doesNotMatch(c, /fetch\(/, f);
  }
  const fetches = code(src["main.js"]).match(/fetch\([^)]*\)/g);
  assert.deepEqual(fetches, ['fetch(`images/${pin.file}`, { cache: "no-store", credentials: "omit", referrerPolicy: "no-referrer" })']);
  for (const m of html.matchAll(/(?:src|href)="([^"]+)"/g)) assert.match(m[1], /^(js\/[a-z]+\.js|style\.css|LICENSE\.txt)$/);
});

test("the shipped config: device use not approved; the release-1.2 image is pinned; no superseded image (MF-4, MF-5)", async () => {
  const cfg = await import("../../site/js/config.js");
  assert.equal(cfg.DEVICE_USE_APPROVED, false);
  assert.equal(cfg.MODEL.base, "UC-P10");
  assert.equal(cfg.EXPECT_FASTBOOT_PRODUCT, "evb_rk3399"); // MF-5: it was null
  assert.ok(cfg.IMAGES.length >= 1);
  for (const i of cfg.IMAGES) {
    assert.match(i.sha256, /^[0-9a-f]{64}$/);
    assert.ok(i.size === null || (Number.isInteger(i.size) && i.size > 0));
    assert.match(i.file, /^[A-Za-z0-9._-]+\.img$/);
    assert.ok(typeof i.model === "string" && i.model.length > 0);
  }
  const shas = cfg.IMAGES.map((i) => i.sha256);
  // MF-4: the release-1.2 UC-P10 install image is pinned...
  assert.ok(shas.includes("af3a8e81103159fef1b0ceb34e937a4894d0cbfc62cb559e317248e25d8e3910"));
  // ...the superseded status image the old page pinned is gone...
  assert.ok(!shas.includes("b6a1440406c9b9e55f2e96cd306bbd1cd2b5999b1865c91fe053a07ad51b7038"));
  // ...and the UC-P8 install image is not pinned (it is allowed on no unit yet).
  assert.ok(!shas.includes("65a9519094c3ec945970a4a48941aba6cd35ce53121de58bfd11e5f3bc43d005"));
  assert.ok(cfg.IMAGES.every((i) => i.model === cfg.MODEL.base));
  assert.ok(Object.isFrozen(cfg.IMAGES));
  assert.doesNotMatch(src["config.js"].replace(/\/\/.*$/gm, ""), /b6a1440406c9b9e5/);
});

test("the USB serial string is read in one place and never logged", () => {
  const c = code(src["enter.js"]);
  const lines = c.split("\n").filter((l) => /serialNumber/.test(l)).map((l) => l.trim());
  assert.deepEqual(lines, ['return typeof dev.serialNumber === "string" ? dev.serialNumber : "";']);
  for (const [f, s] of Object.entries(src)) {
    for (const l of code(s).split("\n")) {
      // the one allowed line logs sameUnit's fixed text, never the string itself (unit test "sameUnit")
      if (l.trim() === "log(`fastboot device: ${sameUnit(S.rkTag, d)}`);") continue;
      if (/\blog\(/.test(l)) assert.doesNotMatch(l, /rkTag|unitTag|tagBefore|serialNumber/, `${f}: ${l}`);
    }
  }
});

test("after the entry, the benign both-trying reset is validated and an unread slot getvar stops (--plain-entry)", () => {
  const c = code(src["enter.js"]);
  // the required getvars are the seven slot vars; serialno is never read
  assert.match(c, /export const SLOT_VARS = Object\.freeze\(\["current-slot", "slot-successful:a", "slot-successful:b",/);
  assert.doesNotMatch(c, /"serialno"/);
  assert.match(c, /const missing = SLOT_VARS\.filter\(\(n\) => v\[n\] === null \|\| v\[n\] === ""\);/);
  // the A/B rule is check_plain_entry: neither slot successful, current slot
  // bootable, and the current slot took a try
  assert.match(c, /if \(sa !== "no" \|\| sb !== "no"\) why\.push/);
  assert.match(c, /is unbootable: no bootable slot/);
  assert.match(c, /did not take a try/);
  // the page never writes LBA 49156 and never sends getvar all / at-*
  for (const [f, s] of Object.entries(src)) {
    assert.doesNotMatch(code(s), /getvar:at-|at-vboot-state/, f);
  }
});
