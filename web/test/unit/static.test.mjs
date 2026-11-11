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
  assert.deepEqual(jsFiles.sort(), ["avbab.js", "bcb.js", "config.js", "drivecopy.js", "enter.js", "factory.js", "fastboot.js", "finishusb.js", "main.js", "partmap.js", "rockusb.js", "serial.js", "step2usb.js", "unlock.js", "usbio.js"]);
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
    // .close() is a USB-device concern; drivecopy.js closes a file writable
    // (FileSystemWritableFileStream), never a USB device.
    if (f !== "usbio.js" && f !== "drivecopy.js") assert.doesNotMatch(c, /\.close\s*\(/, f);
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
    // Only main.js (the site image) and unlock.js (the one-liner text) fetch,
    // and both are same-origin (connect-src 'self').
    if (f !== "main.js" && f !== "unlock.js") assert.doesNotMatch(c, /fetch\(/, f);
  }
  const fetches = code(src["main.js"]).match(/fetch\([^)]*\)/g);
  assert.deepEqual(fetches, ['fetch(`images/${pin.file}`, { cache: "no-store", credentials: "omit", referrerPolicy: "no-referrer" })']);
  // unlock.js fetches only tools/ paths (relative, same-origin).
  const ufetch = code(src["unlock.js"]).match(/fetch\([^)]*\)/g) || [];
  assert.deepEqual(ufetch, ['fetch(url, { cache: "no-store", credentials: "omit", referrerPolicy: "no-referrer" })']);
  assert.match(code(src["unlock.js"]), /fetchText\(`tools\/\$\{s\.file\}`\)/);
  for (const m of html.matchAll(/(?:src|href)="([^"]+)"/g)) assert.match(m[1], /^(js\/[a-z]+\.js|style\.css|LICENSE\.txt)$/);
});

test("the shipped config: device use approved for release; the release-1.2 image is pinned; no superseded image (MF-4, MF-5)", async () => {
  const cfg = await import("../../site/js/config.js");
  assert.equal(cfg.DEVICE_USE_APPROVED, true); // end-user release approved (owner 2026-11-08, R4 hardware proof + f9ce0a re-proof)
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
  // MF-4: the release-1.2 (keeper-reestablish, exit-3 fix) UC-P10 install image is pinned...
  assert.ok(shas.includes("3c522be84ebb8bd59a7cac7b94cf638030937a4bef960b3b6583a1a5c8febb12"));
  // ...the prior build's image (af3a8e81) and the superseded status image are gone...
  assert.ok(!shas.includes("af3a8e81103159fef1b0ceb34e937a4894d0cbfc62cb559e317248e25d8e3910"));
  assert.ok(!shas.includes("b6a1440406c9b9e55f2e96cd306bbd1cd2b5999b1865c91fe053a07ad51b7038"));
  // ...and the UC-P8 install image is not pinned (it is allowed on no unit yet).
  assert.ok(!shas.includes("c9f0ca570aaea00706d462befe02f97c9362d7943a1f4419c8bd9b449a00e054"));
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

test("after the entry, exactly the measured both-trying reset is accepted and an unread slot getvar stops (--plain-entry)", () => {
  const c = code(src["enter.js"]);
  // the required getvars are the seven slot vars; serialno is never read
  assert.match(c, /export const SLOT_VARS = Object\.freeze\(\["current-slot", "slot-successful:a", "slot-successful:b",/);
  assert.doesNotMatch(c, /"serialno"/);
  assert.match(c, /const missing = SLOT_VARS\.filter\(\(n\) => v\[n\] === null \|\| v\[n\] === ""\);/);
  // the A/B rule is check_plain_entry tightened to the measured reset: current
  // slot a, both slots not unbootable and not successful, retry a 6 and b 7
  assert.match(c, /if \(cur !== "a"\) why\.push/);
  assert.match(c, /for \(const n of \["slot-unbootable:a", "slot-unbootable:b", "slot-successful:a", "slot-successful:b"\]\) \{\n\s+if \(norm\(n, v\[n\]\) !== "no"\) why\.push/);
  assert.match(c, /export const RESET_RETRY = Object\.freeze\(\{ a: MAX_TRIES - 1, b: MAX_TRIES \}\);/);
  assert.match(c, /parseInt\(s, 10\) !== RESET_RETRY\[slot\]\) \{\n\s+why\.push/);
  // a non-empty why is a stop before any download (no boot without the check)
  assert.match(c, /if \(why\.length\) \{\n\s+throw new Refusal\("entry-not-benign"/);
  // the page never writes LBA 49156 and never sends getvar all / at-*
  for (const [f, s] of Object.entries(src)) {
    assert.doesNotMatch(code(s), /getvar:at-|at-vboot-state/, f);
  }
});

test("step 2 over the cable: off by default, its own device filter, only usbio transfers, no write path", async () => {
  const cfg = await import("../../site/js/config.js");
  assert.equal(cfg.STEP2_USB_ENABLED, false); // a reviewed change turns it on
  assert.deepEqual({ ...cfg.STEP2_USB }, { vendorId: 0x1d6b, productId: 0x0105, cls: 0xff, sub: 0x50, proto: 0x01 });
  assert.ok(Object.isFrozen(cfg.STEP2_USB) && Object.isFrozen(cfg.STEP2) && Object.isFrozen(cfg.STEP2.PAYLOADS));
  assert.deepEqual(cfg.STEP2.PAYLOADS, []); // no payload is pinned yet
  // the one place of the USB id in Go (internal/xfer/proto.go) has the same values
  const go = readFileSync(join(SITE, "../../internal/xfer/proto.go"), "utf8");
  assert.match(go, /USBVendorID\s+= 0x1d6b/);
  assert.match(go, /USBProductID\s+= 0x0105/);
  assert.match(go, /IfaceClass\s+= 0xff/);
  assert.match(go, /IfaceSubclass\s+= 0x50/);
  assert.match(go, /IfaceProtocol\s+= 0x01/);
  const c = code(src["step2usb.js"]);
  // its transfers go through usbio.js; it fetches nothing, never resets, never sends a control transfer
  assert.match(c, /import \{ bulkIn, bulkOut, openInterface \} from "\.\/usbio\.js";/);
  assert.doesNotMatch(c, /fetch\(|controlTransfer|\.reset\(|clearHalt|\.forget\(|\.close\(/);
  // selectAlternateInterface (the resync) only in step2usb.js
  for (const [f, s] of Object.entries(src)) if (f !== "step2usb.js") assert.doesNotMatch(code(s), /selectAlternateInterface/, f);
  // the step-1 code paths do not know the step-2 device or the finish: only
  // main.js, config.js and the two transfer modules name them
  for (const f of ["rockusb.js", "bcb.js", "enter.js", "fastboot.js", "avbab.js", "partmap.js", "factory.js", "serial.js", "usbio.js"]) {
    assert.doesNotMatch(code(src[f]), /STEP2|step2|0x0105|FINISH|finishusb|uc-pxx-finish/, f);
  }
  // the step-1 device filters stay rockusb + fastboot only
  assert.match(code(src["main.js"]), /const FILTERS = \[\{ vendorId: RK_VID, productId: RK_PID \}, \{ vendorId: FB_VID, productId: FB_PID \}\];/);
  // the section is in the page and hidden by default
  const html = readFileSync(join(SITE, "index.html"), "utf8");
  assert.match(html, /<section id="step2-section" hidden>/);
});

test("the finish over the cable: off in the shipped config, no list pinned, the step-2 device and framing, no write path", async () => {
  const cfg = await import("../../site/js/config.js");
  assert.equal(cfg.FINISH_USB_ENABLED, false); // a reviewed change turns it on
  assert.ok(Object.isFrozen(cfg.FINISH) && Object.isFrozen(cfg.FINISH.LISTS));
  assert.deepEqual(Object.keys(cfg.FINISH).sort(), ["LISTS", "STATUS_EVERY_S"]);
  assert.deepEqual(cfg.FINISH.LISTS, []); // no finish set is pinned yet: every send is refused on the page
  assert.equal(cfg.FINISH.STATUS_EVERY_S, 2);
  assert.match(src["config.js"], /^export const FINISH_USB_ENABLED = false;$/m);
  assert.match(src["config.js"], /^export const FINISH = Object\.freeze\(\{ LISTS: Object\.freeze\(\[\]\), STATUS_EVERY_S: 2 \}\);$/m);
  // one USB device for step 2 and the finish: no second identity in config.js
  assert.equal((code(src["config.js"]).match(/0x0105/g) || []).length, 1);
  // finishusb.js uses the step-2 session and frames (step2usb.js), nothing else
  const c = code(src["finishusb.js"]);
  assert.deepEqual(c.match(/^import .*$/gm), ['import { Step2Link, T } from "./step2usb.js";']);
  assert.doesNotMatch(c, /fetch\(|transfer(In|Out)|controlTransfer|selectAlternateInterface|\.reset\(|clearHalt|\.forget\(|\.close\(|serialNumber|requestDevice/);
  // a finish COMMIT is always auto false
  assert.deepEqual(c.match(/\.commit\([^)]*\)/g), [".commit(false)"]);
  // the names of the set are those of internal/xfer/handover/finish.go
  const go = readFileSync(join(SITE, "../../internal/xfer/handover/finish.go"), "utf8");
  for (const [k, v] of [["FinishListName", "finish.json"], ["FinishKind", "uc-pxx-finish"], ["FinishBin", "uc-pxx-step2-finish"],
    ["FinishBootctl", "uc-pxx-bootctl-inst"]]) {
    assert.match(go, new RegExp(`${k}\\s+= "${v.replace(/\./g, "\\.")}"`), k);
    assert.ok(c.includes(`"${v}"`), v);
  }
  assert.match(go, /FinishVersion = 1\b/);
  assert.match(go, /MaxFinishFile = 32 << 20/);
  assert.match(go, /MaxFinishList = 4 << 10/);
  assert.match(c, /export const MAX_LIST = 4 << 10;/);
  assert.match(c, /export const MAX_FILE = 32 << 20;/);
  // main.js: the finish and the status view use the step-2 device filter only
  const m = code(src["main.js"]);
  assert.equal((m.match(/requestDevice\(/g) || []).length, 3); // step 1, step 2 (unchanged), unitDevice
  assert.match(m, /requestDevice\(\{ filters: \[\{ vendorId: STEP2_USB\.vendorId, productId: STEP2_USB\.productId \}\] \}\)/);
  // the sections are in the page and hidden by default
  const html = readFileSync(join(SITE, "index.html"), "utf8");
  assert.match(html, /<section id="finish-section" hidden>/);
  assert.match(html, /<section id="xstatus-section" hidden>/);
});
