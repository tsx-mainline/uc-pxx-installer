// SPDX-License-Identifier: GPL-2.0-or-later
// web/test/lan-site.sh: the non-served test copy of the RC-10 page branch
// (page-rc-10; its tests of the pins and the TEST ONLY lines are there). It
// patches only the placeholder lines of a pre-release config.js; the release
// 1.3.0 config has no placeholder, so lan-site.sh refuses it and the shipped
// config is not changed.
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

test("lan-site.sh refuses the release 1.3.0 config (no placeholder line to patch); the shipped config unchanged", () => {
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

test("lan-site.sh still refuses a file that is not its pin, and a bad pin", () => {
  for (const over of [{ IMAGE_SHA256: "0".repeat(64) }, { BASE_MANIFEST_SHA256: "1".repeat(64) }, { FINISH_LIST_SHA256: "x" }, { IMAGE_TOKEN: "a b" },
    { PROVISION_URL: "ftp://x/" }, { PROVISION_URL: "http://u@x/" }, { PROVISION_URL: "http://x/?a=1/" }, { PROVISION_URL: "http://x/$(touch /tmp/no)/" },
    { EXTRA: "1" }]) {
    const { d } = inputs(over);
    assert.throws(() => execFileSync("sh", [SCRIPT, join(d, "out"), d], { stdio: "pipe" }), JSON.stringify(over));
  }
});
