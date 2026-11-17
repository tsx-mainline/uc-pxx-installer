// SPDX-License-Identifier: GPL-2.0-or-later
// web/test/lan-site.sh: the non-served test copy of the RC-10c dev page. It
// patches only the placeholder lines of the dev config.js; the RC-10c
// local-test config has no placeholder, so lan-site.sh refuses it, and it
// refuses each bad input for its own reason. The shipped config is not changed.
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

test("lan-site.sh refuses the RC-10c local-test config (no placeholder line to patch); the shipped config unchanged", () => {
  const before = readFileSync(CONFIG, "utf8");
  const { d } = inputs();
  let err = "";
  try {
    execFileSync("sh", [SCRIPT, join(d, "out"), d], { stdio: "pipe" });
  } catch (e) {
    err = String(e.stderr);
  }
  assert.match(err, /config\.js: the line '.*' is not there once/);
  assert.equal(readFileSync(CONFIG, "utf8"), before);
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
