// SPDX-License-Identifier: GPL-2.0-or-later
// web/test/lan-site.sh: the non-served test copy pins exactly the given files
// and marks every changed line TEST ONLY; it refuses files that are not their
// pins. The shipped config is not changed.
import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
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
  assert.equal(changed.length, 10);
  for (const l of changed) assert.match(l, /TEST ONLY \(web\/test\/lan-site\.sh\): never served/);
  for (const v of [p.IMAGE_SHA256, p.BASE_MANIFEST_SHA256, p.FINISH_LIST_SHA256, "pxx-inst-t1", "uc-pxx-inst-test.img", p.PROVISION_URL]) assert.ok(c.includes(v), v);
  assert.match(c, /export const MAINLINE_INSTALL_APPROVED = true;/);
  assert.match(c, /export const STEP2_USB_ENABLED = true;/);
  assert.match(c, /export const FINISH_USB_ENABLED = true;/);
  assert.equal(readFileSync(CONFIG, "utf8"), before);
  assert.equal(readFileSync(join(out, "index.html"), "utf8"), readFileSync(fileURLToPath(new URL("../../site/index.html", import.meta.url)), "utf8"));
});

test("lan-site.sh refuses a file that is not its pin, and a bad pin", () => {
  for (const over of [{ IMAGE_SHA256: "0".repeat(64) }, { BASE_MANIFEST_SHA256: "1".repeat(64) }, { FINISH_LIST_SHA256: "x" }, { IMAGE_TOKEN: "a b" },
    { PROVISION_URL: "ftp://x/" }, { PROVISION_URL: "http://u@x/" }, { PROVISION_URL: "http:///x/" },
    { PROVISION_URL: `http://x/${"a".repeat(503)}/` }, { PROVISION_URL: "http://x/?a=1/" }, { PROVISION_URL: "http://x/#f/" },
    { PROVISION_URL: "http://x/a b/" }, { PROVISION_URL: "http://x/%zz/" }, { PROVISION_URL: "http://x/a%/" }]) {
    const { d } = inputs(over);
    assert.throws(() => execFileSync("sh", [SCRIPT, join(d, "out"), d], { stdio: "pipe" }), JSON.stringify(over));
  }
});

test("lan-site.sh: optional bundle pins for the cable mode; pins.env is data, not shell code", () => {
  const { d } = inputs({ BUNDLE_RELEASE: "0.3.0-rc10", BUNDLE_KIOSK_HA_SHA256: "a".repeat(64), BUNDLE_KIOSK_HA_SIZE: 1234 });
  const out = join(d, "out");
  execFileSync("sh", [SCRIPT, out, d], { stdio: "pipe" });
  const c = readFileSync(join(out, "js/config.js"), "utf8");
  // bundle.py's name: uc-pxx-profile-<id>-<release>.tar
  assert.match(c, /file: "uc-pxx-profile-kiosk-ha-0\.3\.0-rc10\.tar", size: 1234, sha256: "a{64}", url: null \}\) \}\), \/\/ TEST ONLY/);
  assert.match(c, /file: "uc-pxx-profile-kiosk-RELEASE-PLACEHOLDER\.tar", size: null, sha256: "BUNDLE-KIOSK-SHA256-PLACEHOLDER"/);
  for (const over of [{ PROVISION_URL: "http://x/$(touch /tmp/no)/" }, { IMAGE_TOKEN: "a;b" }, { EXTRA: "1" }, { BUNDLE_RELEASE: "1", BUNDLE_KIOSK_SIZE: "x", BUNDLE_KIOSK_SHA256: "b".repeat(64) },
    { BUNDLE_KIOSK_SIZE: "5", BUNDLE_KIOSK_SHA256: "b".repeat(64) }, { BUNDLE_RELEASE: "a/b", BUNDLE_KIOSK_SIZE: "5", BUNDLE_KIOSK_SHA256: "b".repeat(64) }]) {
    const t = inputs(over);
    assert.throws(() => execFileSync("sh", [SCRIPT, join(t.d, "out"), t.d], { stdio: "pipe" }), JSON.stringify(over));
  }
});
