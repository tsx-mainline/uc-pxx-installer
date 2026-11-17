// SPDX-License-Identifier: GPL-2.0-or-later
// web/test/lan-site.sh: the non-served test copy of the RC-10c dev page pins
// exactly the given files and marks every changed line TEST ONLY; it refuses
// files that are not their pins, each bad input for its own reason. The
// shipped config is not changed.
import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const SCRIPT = fileURLToPath(new URL("../lan-site.sh", import.meta.url));
const CONFIG = fileURLToPath(new URL("../../site/js/config.js", import.meta.url));
const sha = (b) => createHash("sha256").update(b).digest("hex");

function inputs(over = {}) {
  const d = mkdtempSync(join(tmpdir(), "lansite-"));
  for (const x of ["images", "base/uc-pxx-mainline", "finish"]) mkdirSync(join(d, x), { recursive: true });
  const img = Buffer.from("ANDROID!test image");
  const man = Buffer.from("{}");
  const fl = Buffer.from("{\"kind\":\"uc-pxx-finish\"}");
  writeFileSync(join(d, "images/uc-pxx-inst-test.img"), img);
  writeFileSync(join(d, "base/uc-pxx-mainline/manifest.json"), man);
  for (const f of ["boot-template.img", "rootfs.ext4.zst"]) writeFileSync(join(d, "base/uc-pxx-mainline", f), "x");
  writeFileSync(join(d, "finish/finish.json"), fl);
  for (const f of ["uc-pxx-step2-finish", "uc-pxx-bootctl-inst"]) writeFileSync(join(d, "finish", f), "y");
  const p = { IMAGE_SHA256: sha(img), IMAGE_SIZE: img.length, IMAGE_TOKEN: "pxx-inst-t1", BASE_MANIFEST_SHA256: sha(man),
    FINISH_LIST_SHA256: sha(fl), PROVISION_URL: "http://192.0.2.10:8080/bundles/", ...over };
  writeFileSync(join(d, "pins.env"), Object.entries(p).map(([k, v]) => `${k}='${v}'`).join("\n") + "\n");
  return { d, p };
}

test("lan-site.sh: the pins of the test files, the flags on, TEST ONLY on every changed line; the shipped config unchanged", () => {
  const before = readFileSync(CONFIG, "utf8");
  const { d, p } = inputs();
  const out = join(d, "out");
  execFileSync("sh", [SCRIPT, out, d], { stdio: "pipe" });
  const c = readFileSync(join(out, "js/config.js"), "utf8");
  const changed = c.split("\n").filter((l) => !before.split("\n").includes(l));
  // the page version, the image (label, file, size, token, sha256), the base, the three flags, the finish list, PROVISION_URL
  assert.equal(changed.length, 12);
  for (const l of changed) assert.match(l, /TEST ONLY \(web\/test\/lan-site\.sh\): never served/);
  for (const v of [p.IMAGE_SHA256, p.BASE_MANIFEST_SHA256, p.FINISH_LIST_SHA256, "pxx-inst-t1", "uc-pxx-inst-test.img", p.PROVISION_URL]) assert.ok(c.includes(v), v);
  assert.match(c, /export const PAGE_VERSION = "TEST ONLY 1\.4\.0-pre \(mainline RC-10c\), image pxx-inst-t1";/);
  assert.match(c, /export const MAINLINE_INSTALL_APPROVED = true;/);
  assert.match(c, /export const STEP2_USB_ENABLED = true;/);
  assert.match(c, /export const FINISH_USB_ENABLED = true;/);
  // no image, base or finish placeholder and no "NOT FOR DEVICE" string is left (the bundles stay without BUNDLE_*)
  assert.doesNotMatch(c, /RC10C-(IMAGE|BASE-MANIFEST|FINISH-LIST)-SHA256-PLACEHOLDER|"[^"\n]*NOT FOR DEVICE[^"\n]*"/);
  assert.equal(readFileSync(CONFIG, "utf8"), before);
  assert.equal(readFileSync(join(out, "index.html"), "utf8"), readFileSync(fileURLToPath(new URL("../../site/index.html", import.meta.url)), "utf8"));
});

test("lan-site.sh: optional bundle pins for the cable mode; the other bundle stays a placeholder", () => {
  const { d } = inputs({ BUNDLE_RELEASE: "0.3.0-rc10c", BUNDLE_KIOSK_HA_SHA256: "a".repeat(64), BUNDLE_KIOSK_HA_SIZE: 1234 });
  const out = join(d, "out");
  execFileSync("sh", [SCRIPT, out, d], { stdio: "pipe" });
  const c = readFileSync(join(out, "js/config.js"), "utf8");
  // bundle.py's name: uc-pxx-profile-<id>-<release>.tar
  assert.match(c, /file: "uc-pxx-profile-kiosk-ha-0\.3\.0-rc10c\.tar", size: 1234, sha256: "a{64}", url: null \}\) \}\), \/\/ TEST ONLY/);
  assert.match(c, /file: "uc-pxx-profile-kiosk-RELEASE-PLACEHOLDER\.tar", size: null, sha256: "BUNDLE-KIOSK-SHA256-PLACEHOLDER-NOT-FOR-DEVICE"/);
});

// run: the script's exit status and its error text
function run(d) {
  try {
    execFileSync("sh", [SCRIPT, join(d, "out"), d], { stdio: "pipe" });
    return { status: 0, err: "" };
  } catch (e) {
    return { status: e.status, err: String(e.stderr) };
  }
}

// review NIT-3 (PUBLISH-RC10B-PAGE-REVIEW.md): each bad input is refused for its own reason (its error text), before
// the config lines are looked at, so the test does not pass on a refusal of the config alone
const BAD = [
  [{ IMAGE_SHA256: "0".repeat(64) }, "the image is not IMAGE_SHA256"],
  [{ BASE_MANIFEST_SHA256: "1".repeat(64) }, "the base manifest is not its pin"],
  [{ FINISH_LIST_SHA256: "2".repeat(64) }, "finish.json is not its pin"],
  [{ FINISH_LIST_SHA256: "x" }, "the pins must be sha256 hex"],
  [{ IMAGE_SIZE: "12a" }, "IMAGE_SIZE must be a number"],
  [{ IMAGE_TOKEN: "a b" }, "pins.env: IMAGE_TOKEN has a character that is not allowed"],
  [{ IMAGE_TOKEN: "a;b" }, "pins.env: IMAGE_TOKEN has a character that is not allowed"],
  [{ IMAGE_TOKEN: "0123456789abcdef" }, "bad IMAGE_TOKEN"],
  [{ PROVISION_URL: "ftp://x/" }, "PROVISION_URL must be http(s) and end in /"],
  [{ PROVISION_URL: "http://u@x/" }, "PROVISION_URL needs a host and no user info"],
  [{ PROVISION_URL: "http:///x/" }, "PROVISION_URL needs a host and no user info"],
  [{ PROVISION_URL: `http://x/${"a".repeat(503)}/` }, "PROVISION_URL is longer than 512 bytes"],
  [{ PROVISION_URL: "http://x/?a=1/" }, "PROVISION_URL has a query, a fragment or a backslash"],
  [{ PROVISION_URL: "http://x/#f/" }, "PROVISION_URL has a query, a fragment or a backslash"],
  [{ PROVISION_URL: "http://x/a b/" }, "pins.env: PROVISION_URL has a character that is not allowed"],
  [{ PROVISION_URL: "http://x/%zz/" }, "PROVISION_URL has a % that is not an escape"],
  [{ PROVISION_URL: "http://x/a%/" }, "PROVISION_URL has a % that is not an escape"],
  [{ PROVISION_URL: "http://x/$(touch /tmp/no)/" }, "pins.env: PROVISION_URL has a character that is not allowed"],
  [{ EXTRA: "1" }, "pins.env: unknown key EXTRA"],
  [{ BUNDLE_KIOSK_SIZE: "5", BUNDLE_KIOSK_SHA256: "b".repeat(64) }, "a bundle pin needs BUNDLE_RELEASE (the release in uc-pxx-profile-<id>-<release>.tar)"],
  [{ BUNDLE_RELEASE: "a/b", BUNDLE_KIOSK_SIZE: "5", BUNDLE_KIOSK_SHA256: "b".repeat(64) }, "a bundle pin needs BUNDLE_RELEASE (the release in uc-pxx-profile-<id>-<release>.tar)"],
  [{ BUNDLE_RELEASE: "1", BUNDLE_KIOSK_SIZE: "x", BUNDLE_KIOSK_SHA256: "b".repeat(64) }, "BUNDLE_KIOSK_SIZE must be a number"],
  [{ BUNDLE_RELEASE: "1", BUNDLE_KIOSK_SIZE: "5", BUNDLE_KIOSK_SHA256: "b" }, "BUNDLE_KIOSK_SHA256 must be sha256 hex"],
];

test("lan-site.sh refuses each bad input for its own reason (its error text); nothing is made", () => {
  const before = readFileSync(CONFIG, "utf8");
  for (const [over, why] of BAD) {
    const { d } = inputs(over);
    const r = run(d);
    assert.notEqual(r.status, 0, JSON.stringify(over));
    assert.equal(r.err.trim(), why, JSON.stringify(over));
    assert.equal(existsSync(join(d, "out")), false, JSON.stringify(over));
  }
  assert.equal(readFileSync(CONFIG, "utf8"), before);
});
