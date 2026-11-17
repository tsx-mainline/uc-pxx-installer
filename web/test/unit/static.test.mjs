// SPDX-License-Identifier: GPL-2.0-or-later
// Static checks of the page sources: the structural guards that a reader can
// see without running anything.
import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
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
  assert.deepEqual(jsFiles.sort(), ["avbab.js", "bcb.js", "config.js", "download.js", "drivecopy.js", "enter.js", "factory.js", "fastboot.js", "finishusb.js", "main.js",
    "partmap.js", "progress.js", "provisionusb.js", "rockusb.js", "serial.js", "sha256stream.js", "sshkey.js", "step1usb.js", "step2usb.js", "steps.js", "texts.js",
    "unlock.js", "usbio.js"]);
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
    // .close() is a USB-device concern; drivecopy.js and download.js close a
    // file writable (FileSystemWritableFileStream), never a USB device.
    if (!["usbio.js", "drivecopy.js", "download.js"].includes(f)) assert.doesNotMatch(c, /\.close\s*\(/, f);
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

test("no third-party request, no telemetry; the only storage is the page's own private folder; the outside links", async () => {
  const cfg = await import("../../site/js/config.js");
  const html = readFileSync(join(SITE, "index.html"), "utf8");
  // the CSP: connect-src 'self' only while BASE.PAYLOAD_URL is null (another host adds its origin in a reviewed change)
  assert.equal(cfg.BASE.PAYLOAD_URL, null);
  assert.equal(cfg.PROVISION.PROVISION_URL, null);
  assert.match(cfg.BASE.dir, /^[a-z0-9-]+\/[a-z0-9-]+\/$/); // a relative, same-origin folder
  assert.match(html, /<meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'">/);
  assert.equal((html.match(/Content-Security-Policy/g) || []).length, 1);
  assert.match(html, /<meta name="referrer" content="no-referrer">/);
  // the only URLs in the page: two links that open a normal browser download in a new tab (navigations, not
  // fetches): Crestron's firmware file (R19; the page never fetches it) and the project's Releases page
  const urls = [...html.replace(/<!--[\s\S]*?-->/g, "").matchAll(/https?:\/\/[^"<\s]+/g)].map((m) => m[0]);
  assert.deepEqual([...new Set(urls)].sort(), [cfg.RELEASES_URL, cfg.FIRMWARE.url].sort());
  assert.equal(cfg.FIRMWARE.url, "https://crestrondevicefiles.blob.core.windows.net/firmware/pcdfw-1.0.0.94-userdebug.bin");
  for (const m of html.matchAll(/<a [^>]*href="(https:[^"]+)"[^>]*>/g)) {
    assert.match(m[0], /target="_blank"/);
    assert.match(m[0], /rel="noopener noreferrer"/);
  }
  assert.doesNotMatch(readFileSync(join(SITE, "style.css"), "utf8"), /url\(|@import|https?:/);
  for (const [f, s] of Object.entries(src)) {
    const c = code(s);
    if (f !== "config.js" && f !== "download.js") assert.doesNotMatch(c, /https?:/, f);
    assert.doesNotMatch(c, /XMLHttpRequest|WebSocket|sendBeacon|EventSource|localStorage|sessionStorage|indexedDB|document\.cookie|import\(|serviceWorker|eval\(|new Function/, f);
    if (f !== "enter.js") assert.doesNotMatch(c, /serialNumber/, f);
    // only download.js fetches (same origin: the image, the finish set) and unlock.js (tools/)
    if (f !== "download.js" && f !== "unlock.js") assert.doesNotMatch(c, /\bfetch\(/, f);
    if (f !== "download.js") assert.doesNotMatch(c, /navigator\.storage|getDirectory\(|removeEntry/, f);
  }
  // config.js: its only URLs are the firmware link, the Releases page and the cable mode's "Download from GitHub"
  // links of the bundles (null while a bundle is a placeholder; else a release asset of this repository, by its file
  // name: the release that the base's profiles.json names)
  const bundleLinks = cfg.SYSTEMS.filter((x) => x.bundle && x.bundle.url !== null).map((x) => x.bundle.url);
  for (const x of cfg.SYSTEMS) {
    if (x.bundle && x.bundle.url !== null) {
      assert.ok(x.bundle.url.startsWith("https://github.com/tsx-mainline/uc-pxx-installer/releases/download/"), x.id);
      assert.match(x.bundle.url.slice("https://github.com/tsx-mainline/uc-pxx-installer/releases/download/".length), /^[A-Za-z0-9._-]+\/[A-Za-z0-9._-]+$/);
      assert.ok(x.bundle.url.endsWith(`/${x.bundle.file}`), x.id);
    }
  }
  assert.deepEqual([...code(src["config.js"]).matchAll(/https?:\/\/[^"]+/g)].map((m) => m[0]).sort(), [cfg.RELEASES_URL, cfg.FIRMWARE.url, ...bundleLinks].sort());
  assert.match(code(src["main.js"]), /\$\("bundle-link"\)\.href = sys\.bundle\.url \|\| RELEASES_URL;/); // a link (navigation), never a fetch
  const d = code(src["download.js"]);
  // download.js names https only in the check of a download host's URL (PARTS.PAYLOAD_URL, null here)
  assert.deepEqual(d.split("\n").filter((l) => /https?:/.test(l)).map((l) => l.trim()), ['if (typeof base !== "string" || !/^https:\\/\\/[^\\s]+\\/$|^\\/[^\\s]*\\/$/.test(base)) {']);
  assert.deepEqual(d.match(/fetchFn\([^)]*\)/g), ["fetchFn(url, OPTS)", 'fetchFn(base + part.file, { ...OPTS, redirect: "error" })']);
  assert.match(d, /const OPTS = Object\.freeze\(\{ cache: "no-store", credentials: "omit", referrerPolicy: "no-referrer" \}\);/);
  // the one delete: the page's own folder in the private storage, by its fixed name
  assert.deepEqual(d.match(/removeEntry\([^)]*\)/g), ["removeEntry(OPFS_DIR, { recursive: true })"]);
  assert.match(d, /export const OPFS_DIR = "uc-pxx-download";/);
  // main.js fetches only same-origin relative paths through download.js
  const mm = code(src["main.js"]);
  assert.deepEqual(mm.match(/download\.fetch\w+\([^,)]*/g), ["download.fetchBytes(`images/${pin.file}`",
    "download.fetchBase(baseUrl(", "download.fetchBytes(`${FINISH.SITE_DIR}${n}`"]);
  assert.match(mm, /return BASE\.PAYLOAD_URL \|\| BASE\.dir;/);
  assert.doesNotMatch(mm, /downloadPart|deleteDownloads/); // the private-storage download is not used in this version
  assert.equal(cfg.FINISH.SITE_DIR, "finish/");
  // unlock.js fetches only tools/ paths (relative, same-origin).
  const ufetch = code(src["unlock.js"]).match(/fetch\([^)]*\)/g) || [];
  assert.deepEqual(ufetch, ['fetch(url, { cache: "no-store", credentials: "omit", referrerPolicy: "no-referrer" })']);
  assert.match(code(src["unlock.js"]), /fetchText\(`tools\/\$\{s\.file\}`\)/);
  for (const m of html.matchAll(/(?:src|href)="([^"]+)"/g)) assert.match(m[1], /^(js\/[a-z]+\.js|style\.css|LICENSE\.txt|https:\/\/.+)$/);
});

// hex: a sha256 pin (64 hex digits); a placeholder is not one
const hex = (x) => typeof x === "string" && /^[0-9a-f]{64}$/.test(x);

test("the shipped config (1.4.0-pre, RC-10c dev state): every release pin a placeholder NOT FOR DEVICE, the three flags off", async () => {
  const cfg = await import("../../site/js/config.js");
  assert.equal(cfg.DEVICE_USE_APPROVED, true);
  assert.equal(cfg.MODEL.base, "UC-P10");
  assert.equal(cfg.EXPECT_FASTBOOT_PRODUCT, "evb_rk3399");
  assert.equal(cfg.PAGE_VERSION, "1.4.0-pre (mainline RC-10c) NOT FOR DEVICE");
  assert.equal(cfg.IMAGES.length, 1);
  const [i] = cfg.IMAGES;
  assert.deepEqual([...i.goals], ["userdebug", "mainline"]); // owner Q2: one image for both goals
  assert.equal(i.model, "UC-P10");
  assert.match(i.file, /^[A-Za-z0-9._-]+\.img$/);
  // the image: a placeholder that no file can match (not hex), marked NOT FOR DEVICE
  assert.deepEqual({ label: i.label, file: i.file, size: i.size, token: i.token, sha256: i.sha256 }, {
    label: "UC-P10 installer 1.4.0-pre (mainline RC-10c) NOT FOR DEVICE", file: "uc-pxx-inst-ucp10-rc10c.img", size: null,
    token: "RC10C-PLACEHOLDER", sha256: "RC10C-IMAGE-SHA256-PLACEHOLDER-NOT-FOR-DEVICE" });
  assert.ok(!hex(i.sha256));
  // the base, the bundles and the finish list: placeholders too
  assert.equal(cfg.BASE.manifest_sha256, "RC10C-BASE-MANIFEST-SHA256-PLACEHOLDER-NOT-FOR-DEVICE");
  assert.ok(!hex(cfg.BASE.manifest_sha256));
  assert.deepEqual(cfg.SYSTEMS.map((x) => [x.id, x.keyRequired, !!x.bundle]), [["kiosk-ha", false, true], ["kiosk", false, true], ["console", true, false]]);
  assert.deepEqual(cfg.SYSTEMS.filter((x) => x.bundle).map((x) => ({ ...x.bundle })), [
    { file: "uc-pxx-profile-kiosk-ha-RELEASE-PLACEHOLDER.tar", size: null, sha256: "BUNDLE-KIOSK-HA-SHA256-PLACEHOLDER-NOT-FOR-DEVICE", url: null },
    { file: "uc-pxx-profile-kiosk-RELEASE-PLACEHOLDER.tar", size: null, sha256: "BUNDLE-KIOSK-SHA256-PLACEHOLDER-NOT-FOR-DEVICE", url: null },
  ]);
  assert.deepEqual(cfg.FINISH.LISTS.map((l) => ({ ...l })), [{ label: "the RC-10c finish set (placeholder)", sha256: "RC10C-FINISH-LIST-SHA256-PLACEHOLDER-NOT-FOR-DEVICE" }]);
  for (const l of cfg.FINISH.LISTS) assert.ok(!hex(l.sha256));
  // the flags of RULES item G: off until the integration build pins the image, the base and the finish list
  assert.equal(cfg.MAINLINE_INSTALL_APPROVED, false);
  assert.equal(cfg.STEP2_USB_ENABLED, false);
  assert.equal(cfg.FINISH_USB_ENABLED, false);
  assert.equal(cfg.UNLOCK_GUIDE_ENABLED, false);
  assert.equal(cfg.BASE.PAYLOAD_URL, null);
  assert.equal(cfg.PROVISION.PROVISION_URL, null);
  // no pin of an earlier release is left in the code (comments may name them): the RC-10b image, base, finish list
  // and bundles of 1.3.0, the R-C userdebug image, its predecessor, the status image, the UC-P8 image
  for (const old of ["f7069de1", "04f4f064", "66dfb112", "5e2b78dc", "ac7fa681", "3c522be8", "af3a8e81", "b6a14404", "c9f0ca57"]) {
    assert.doesNotMatch(code(src["config.js"]), new RegExp(old), old);
  }
  assert.ok(Object.isFrozen(cfg.IMAGES) && Object.isFrozen(cfg.IMAGES[0]) && Object.isFrozen(cfg.IMAGES[0].goals));
  assert.ok(Object.isFrozen(cfg.SYSTEMS) && cfg.SYSTEMS.every((x) => Object.isFrozen(x)));
  // the firmware pin of the installer's firmware gate (manifests/manifest-1.0.0.94-userdebug.json): a real pin
  const man = JSON.parse(readFileSync(join(SITE, "../../manifests/manifest-1.0.0.94-userdebug.json"), "utf8"));
  assert.equal(cfg.FIRMWARE.sha256, man.firmware.sha256);
  assert.equal(cfg.FIRMWARE.size, man.firmware.size);
  // the page's serial filter: the RC-10 gadget 1d6b:0105 and the older 1d6b:0104
  assert.deepEqual(cfg.GADGETS.map((g) => [g.usbVendorId, g.usbProductId]), [[0x1d6b, 0x0105], [0x1d6b, 0x0104]]);
});

// The Pages workflow refuses to deploy the placeholders: its own scripts (the heredocs of pages.yml), run on the
// shipped config.js as the workflow runs them, stop with their error before any download or upload.
function workflowScript(name) {
  const w = readFileSync(join(SITE, "../../.github/workflows/pages.yml"), "utf8");
  const m = w.match(new RegExp(`cat > "\\$RUNNER_TEMP/${name}" <<'EOF'\\n([\\s\\S]*?)\\n *EOF\\n`));
  assert.ok(m, name);
  const lines = m[1].split("\n");
  const ind = Math.min(...lines.filter((l) => l.trim()).map((l) => l.match(/^ */)[0].length));
  return lines.map((l) => l.slice(ind)).join("\n") + "\n";
}

test("the Pages workflow refuses the placeholder config: the image pin, the base pin and the finish list", () => {
  const d = mkdtempSync(join(tmpdir(), "pagesyml-"));
  writeFileSync(join(d, "config.mjs"), readFileSync(join(JS, "config.js")));
  for (const n of ["pins.mjs", "sets.mjs"]) writeFileSync(join(d, n), workflowScript(n));
  const run = (args) => {
    try {
      return { status: 0, out: execFileSync(process.execPath, args, { stdio: "pipe", cwd: d }).toString(), err: "" };
    } catch (e) {
      return { status: e.status, out: String(e.stdout), err: String(e.stderr) };
    }
  };
  // the image pins (the step before any download): not 64 hex digits
  let r = run([join(d, "pins.mjs"), join(d, "config.mjs")]);
  assert.notEqual(r.status, 0);
  assert.match(r.err, /config\.js: bad sha256 for uc-pxx-inst-ucp10-rc10c\.img/);
  // the base: not pinned
  mkdirSync(join(d, "base"));
  r = run([join(d, "sets.mjs"), join(d, "config.mjs"), "base", join(d, "base")]);
  assert.notEqual(r.status, 0);
  assert.match(r.err, /config\.js: the base is not pinned \(BASE\.manifest_sha256\)/);
  // the finish set: any finish.json is not in FINISH.LISTS
  mkdirSync(join(d, "finish"));
  writeFileSync(join(d, "finish", "finish.json"), "{\"kind\":\"uc-pxx-finish\",\"version\":1,\"files\":[]}");
  r = run([join(d, "sets.mjs"), join(d, "config.mjs"), "finish", join(d, "finish")]);
  assert.notEqual(r.status, 0);
  assert.match(r.err, /finish\.json is not pinned in FINISH\.LISTS/);
  // the check itself: a config with real pins passes the image step (the scripts are not vacuous)
  const good = readFileSync(join(JS, "config.js"), "utf8").replace('sha256: "RC10C-IMAGE-SHA256-PLACEHOLDER-NOT-FOR-DEVICE"', `sha256: "${"a".repeat(64)}"`);
  writeFileSync(join(d, "config-good.mjs"), good);
  r = run([join(d, "pins.mjs"), join(d, "config-good.mjs")]);
  assert.equal(r.status, 0, r.err);
  assert.equal(r.out, `${"a".repeat(64)}  uc-pxx-inst-ucp10-rc10c.img\n`);
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

test("the transfer device: one identity for step 1, step 2 and the finish; only usbio transfers; no write path", async () => {
  const cfg = await import("../../site/js/config.js");
  assert.deepEqual({ ...cfg.STEP2_USB }, { vendorId: 0x1d6b, productId: 0x0105, cls: 0xff, sub: 0x50, proto: 0x01 });
  assert.ok(Object.isFrozen(cfg.STEP2_USB) && Object.isFrozen(cfg.STEP2) && Object.isFrozen(cfg.STEP1) && Object.isFrozen(cfg.PARTS));
  // the one place of the USB id in Go (internal/xfer/proto.go) has the same values
  const go = readFileSync(join(SITE, "../../internal/xfer/proto.go"), "utf8");
  assert.match(go, /USBVendorID\s+= 0x1d6b/);
  assert.match(go, /USBProductID\s+= 0x0105/);
  assert.match(go, /IfaceClass\s+= 0xff/);
  assert.match(go, /IfaceSubclass\s+= 0x50/);
  assert.match(go, /IfaceProtocol\s+= 0x01/);
  assert.equal((code(src["config.js"]).match(/0x0105/g) || []).length, 2); // STEP2_USB and the serial gadget
  const c = code(src["step2usb.js"]);
  assert.match(c, /import \{ bulkIn, bulkOut, openInterface \} from "\.\/usbio\.js";/);
  assert.doesNotMatch(c, /fetch\(|controlTransfer|\.reset\(|clearHalt|\.forget\(|\.close\(/);
  // selectAlternateInterface (the session resync) only in step2usb.js
  for (const [f, s] of Object.entries(src)) if (f !== "step2usb.js") assert.doesNotMatch(code(s), /selectAlternateInterface/, f);
  // step1usb.js: the step-2 session and frames, nothing else; no device id, no request, no transfer of its own
  const s1 = code(src["step1usb.js"]);
  assert.deepEqual(s1.match(/^import .*$/gm), ['import { Step2Link, T } from "./step2usb.js";']);
  assert.doesNotMatch(s1, /fetch\(|transfer(In|Out)|controlTransfer|selectAlternateInterface|requestDevice|getDevices|serialNumber|0x0105|\.close\(/);
  // its COMMITs: { mode } with "official" or "mainline" after the firmware, {} after part 1
  assert.deepEqual(s1.match(/request\(T\.COMMIT, [^)]*\)/g), ["request(T.COMMIT, { mode })", "request(T.COMMIT, {})"]);
  assert.match(s1, /if \(mode !== "official" && mode !== "mainline"\) throw/);
  assert.match(s1, /if \(keyFile && mode !== "mainline"\) throw/);
  // a file is sent only in the state that asks for it and only when the unit pins it
  assert.match(s1, /if \(classify\(h\)\.phase !== "send-firmware"\) return \{ ok: false/);
  assert.match(s1, /if \(!fwPinned\(h, fw\.size, firmwareSha\)\) return \{ ok: false/);
  assert.match(s1, /if \(classify\(h\)\.phase !== "send-base"\) return \{ ok: false/);
  assert.match(s1, /if \(!Array\.isArray\(h\.pins\) \|\| !h\.pins\.includes\(base\.sha\)\) return \{ ok: false/);
  assert.match(s1, /if \(want && f\.size !== want\.size\) return \{ ok: false/);
  // provisionusb.js: the same session and frames; its COMMITs are the choice and the consent
  const pu = code(src["provisionusb.js"]);
  assert.deepEqual(pu.match(/^import .*$/gm), ['import { Step2Link, T } from "./step2usb.js";']);
  assert.doesNotMatch(pu, /fetch\(|transfer(In|Out)|controlTransfer|selectAlternateInterface|requestDevice|getDevices|serialNumber|0x0105|\.close\(/);
  // (§6.10: the choice {profile, source[, url]} after checkChoice; the consent {auto: true, plan_id})
  assert.deepEqual(pu.match(/request\(T\.COMMIT, [^)]*\)/g), ["request(T.COMMIT, body)", "request(T.COMMIT, { auto: true, plan_id: d.planId })"]);
  assert.match(pu, /const d = checkChoice\(h, c\);\n  if \(d\.go !== "ok"\) return \{ ok: false, wait: d\.go === "wait", config: !!d\.config, why: d\.why \};\n  const body = \{ profile: c\.profile, source: c\.source \};\n  if \(c\.url != null\) body\.url = c\.url;/);
  assert.match(pu, /const d = checkConsent\(h\);\n  if \(d\.go !== "ok"\) return/);
  // the step-1 entry code paths do not know the transfer device
  for (const f of ["rockusb.js", "bcb.js", "enter.js", "fastboot.js", "avbab.js", "partmap.js", "factory.js", "serial.js", "usbio.js"]) {
    assert.doesNotMatch(code(src[f]), /STEP2|step2|step1|0x0105|FINISH|finishusb|uc-pxx-finish/, f);
  }
  // the entry's device filters stay rockusb + fastboot only; the transfer filter is the transfer device only
  const m = code(src["main.js"]);
  assert.match(m, /const FILTERS = \[\{ vendorId: RK_VID, productId: RK_PID \}, \{ vendorId: FB_VID, productId: FB_PID \}\];/);
  assert.match(m, /const XFER_FILTER = \[\{ vendorId: STEP2_USB\.vendorId, productId: STEP2_USB\.productId \}\];/);
  assert.deepEqual(m.match(/requestDevice\(\{ filters: [A-Z_]+ \}\)/g),
    ["requestDevice({ filters: FILTERS })", "requestDevice({ filters: XFER_FILTER })", "requestDevice({ filters: XFER_FILTER })",
      "requestDevice({ filters: XFER_FILTER })"]);
  assert.equal((m.match(/requestDevice\(/g) || []).length, 4);
  // no auto-connect of rockusb or fastboot: their opens are only in the handlers of a press (step(...))
  assert.match(m, /\$\("connect"\)\.addEventListener\("click", \(\) => step\(connectUnit\)\);/);
  assert.match(m, /\$\("connect-fastboot"\)\.addEventListener\("click", \(\) => step\(connectFastboot\)\);/);
  assert.equal((m.match(/RockUsb\.open\(/g) || []).length, 1);
  assert.equal((m.match(/Fastboot\.open\(/g) || []).length, 1);
  // the installer goes to the unit only after fastbootState passed, in the same handler
  const cf = m.slice(m.indexOf("async function connectFastboot()"), m.indexOf("// --- the unit's transfer link"));
  assert.ok(cf.indexOf("fastbootState(") > 0 && cf.indexOf("fastbootState(") < cf.indexOf("bootInstaller("));
  assert.ok(cf.indexOf("sameUnit(") < cf.indexOf("Fastboot.open("));
  assert.ok(cf.indexOf("onlyOne(") < cf.indexOf("Fastboot.open("));
});

test("the finish: off in the shipped config (RC-10c dev state); its finish list a placeholder; the set from this site; the step-2 framing", async () => {
  const cfg = await import("../../site/js/config.js");
  assert.equal(cfg.FINISH_USB_ENABLED, false);
  assert.ok(Object.isFrozen(cfg.FINISH) && Object.isFrozen(cfg.FINISH.LISTS));
  assert.deepEqual(Object.keys(cfg.FINISH).sort(), ["LISTS", "SITE_DIR", "STATUS_EVERY_S"]);
  assert.deepEqual(cfg.FINISH.LISTS.map((l) => l.sha256), ["RC10C-FINISH-LIST-SHA256-PLACEHOLDER-NOT-FOR-DEVICE"]);
  const c = code(src["finishusb.js"]);
  assert.deepEqual(c.match(/^import .*$/gm), ['import { Step2Link, T } from "./step2usb.js";']);
  assert.doesNotMatch(c, /fetch\(|transfer(In|Out)|controlTransfer|selectAlternateInterface|\.reset\(|clearHalt|\.forget\(|\.close\(|serialNumber|requestDevice/);
  assert.deepEqual(c.match(/\.commit\([^)]*\)/g), [".commit(false)"]);
  const go = readFileSync(join(SITE, "../../internal/xfer/handover/finish.go"), "utf8");
  for (const [k, v] of [["FinishListName", "finish.json"], ["FinishKind", "uc-pxx-finish"], ["FinishBin", "uc-pxx-step2-finish"],
    ["FinishBootctl", "uc-pxx-bootctl-inst"]]) {
    assert.match(go, new RegExp(`${k}\\s+= "${v.replace(/\./g, "\\.")}"`), k);
    assert.ok(c.includes(`"${v}"`), v);
  }
  // main.js: the set is checked by finishusb.checkSet and the unit's HELLO by checkHello before any send
  const m = code(src["main.js"]);
  assert.match(m, /const c = await finishusb\.checkSet\(cand, FINISH\.LISTS\);/);
  assert.match(m, /const d = finishusb\.checkHello\(h, F\.check\.sha, FINISH\.LISTS\);/);
  assert.match(m, /if \(!FINISH_USB_ENABLED\) \{\n\s+log\("finish: off in this page version: nothing sent"\);/);
  // the unit's "detail" text is only shown, never decided on
  for (const f of ["main.js", "finishusb.js", "step2usb.js", "step1usb.js", "steps.js"]) {
    assert.doesNotMatch(code(src[f]), /detail\s*(===|!==|==|!=)|detail\.(includes|startsWith|endsWith|match|indexOf)|\.test\([^)]*detail/, f);
  }
});

test("the ssh key: sshkey.js is pure; the key goes only to the unit (step 1, mainline, confirmed) or to the drive (fallback)", () => {
  const k = code(src["sshkey.js"]);
  assert.doesNotMatch(k, /^import |document|window|navigator|fetch\(|getFileHandle|createWritable|showDirectoryPicker|usb|serial/m);
  assert.deepEqual(code(src["drivecopy.js"]).match(/^import .*$/gm), ['import { FILE_NAME } from "./sshkey.js";']);
  for (const [f, s] of Object.entries(src)) assert.doesNotMatch(code(s), /\.move\(|keepExistingData/, f);
  assert.doesNotMatch(code(src["drivecopy.js"]), /\.remove\(|removeEntry/);
  // the drive writes of drivecopy.js: the firmware (fatName), KEY_FILE, a copy by its checked name, the folder uc-pxx-mainline
  const creates = code(src["drivecopy.js"]).split("\n").filter((l) => /create: true/.test(l)).map((l) => l.trim());
  assert.deepEqual(creates, ["const fh = await dir.getFileHandle(fname, { create: true });",
    "const fh = await dir.getFileHandle(KEY_FILE, { create: true });", "const fh = await dir.getFileHandle(name, { create: true });",
    "const d = await dir.getDirectoryHandle(name, { create: true });"]);
  assert.match(code(src["drivecopy.js"]), /if \(name !== MAINLINE_DIR\) throw new Error/);
  for (const [f, s] of Object.entries(src)) if (!["drivecopy.js", "download.js"].includes(f)) assert.doesNotMatch(code(s), /getFileHandle|createWritable/, f);
  const m = code(src["main.js"]);
  assert.deepEqual(m.match(/writeKeyFile\([^)]*\)/g), ["writeKeyFile(D.dir, key.file)"]);
  // the key the page sends: only for mainline, only confirmed
  assert.match(m, /return S\.goal === "mainline" && keyState\(\) === "ready" \? S\.key\.check : null;/);
  for (const f of ["rockusb.js", "bcb.js", "enter.js", "fastboot.js", "avbab.js", "partmap.js", "factory.js", "serial.js", "usbio.js",
    "step2usb.js", "finishusb.js", "config.js"]) {
    assert.doesNotMatch(code(src[f]), /sshkey|SSH-KEY/i, f);
  }
  const html = readFileSync(join(SITE, "index.html"), "utf8");
  // R34 (owner 2026-11-16): the key field is on the Files card itself (no "More options"), with one plain line above it
  assert.ok(!html.includes('<details id="more"'));
  const at = html.indexOf('<div class="row only-mainline" id="more">');
  assert.ok(at > html.indexOf('<section class="card" id="card-files">') && at < html.indexOf('<section class="card" id="card-connect">'));
  const sec = html.slice(at, html.indexOf("</div>", at));
  for (const id of ["sshkey-optional", "sshkey-input", "sshkey-confirm"]) assert.ok(sec.includes(`id="${id}"`), id);
  assert.ok(sec.indexOf('id="sshkey-optional"') < sec.indexOf('id="sshkey-input"'));
  assert.ok(sec.includes("<p id=\"sshkey-optional\">Optional: your SSH public key, for logging in over the network. Without one, ssh stays off. You can add it later on the unit's setup page (Access).</p>"));
  assert.match(sec, /SSH key \(optional\)/);
});

test("the Pages workflow (W12): the image, the base and the finish set into the site, each checked against config.js", () => {
  const w = readFileSync(join(SITE, "../../.github/workflows/pages.yml"), "utf8");
  assert.match(w, /branches: \[main\]/);
  assert.match(w, /sha256sum --strict -c "\$RUNNER_TEMP\/images\.sha256"/);
  assert.match(w, /if \(!hex\(BASE\.manifest_sha256\)\) throw/);
  assert.match(w, /if \(sha\(`\$\{dir\}\/manifest\.json`\) !== BASE\.manifest_sha256\) throw/);
  assert.match(w, /if \(!FINISH\.LISTS\.some\(\(l\) => l\.sha256 === s\)\) throw/);
  assert.match(w, /web\/site\/base\/uc-pxx-mainline/);
  assert.match(w, /path: web\/site\n/);
});
