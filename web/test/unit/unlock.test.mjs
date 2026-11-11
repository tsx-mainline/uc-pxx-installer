// SPDX-License-Identifier: GPL-2.0-or-later
// Unit tests for the unlock guide: the served one-liner scripts agree with the
// pinned client sha in config.js, never offer `lock`, write the /cache marker,
// and the config UNLOCK pin is well-formed. No browser, no device.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { UNLOCK } from "../../site/js/config.js";

const SITE = fileURLToPath(new URL("../../site/", import.meta.url));
const TOOLS = join(SITE, "tools");
const scriptText = Object.fromEntries(UNLOCK.scripts.map((s) => [s.file, readFileSync(join(TOOLS, s.file), "utf8")]));

test("the UNLOCK pin is well-formed", () => {
  assert.match(UNLOCK.client.sha256, /^[0-9a-f]{64}$/);
  assert.match(UNLOCK.client.file, /^[A-Za-z0-9._-]+$/);
  assert.equal(typeof UNLOCK.client.size, "number");
  assert.equal(UNLOCK.markerPath, "/cache/uc-pxx/unlock-marker");
  assert.equal(UNLOCK.scripts.length, 2);
});

test("each one-liner embeds exactly the pinned client sha256 and checks it", () => {
  for (const [file, s] of Object.entries(scriptText)) {
    assert.ok(s.includes(UNLOCK.client.sha256), `${file}: pinned sha present`);
    // the sha is used as the gate, not just printed.
    assert.match(s, /CLIENT_SHA256/, file);
  }
});

test("the scripts never offer `lock` (only read and unlock)", () => {
  for (const [file, s] of Object.entries(scriptText)) {
    // the unit-side client is invoked only with read or unlock, never lock.
    assert.doesNotMatch(s, /\$LL lock|\/tee_unlock[^\n]*\block\b/, file);
    assert.match(s, /\$LL unlock|LL unlock/, file);
    assert.match(s, /\$LL read|LL read/, file);
  }
});

test("the scripts write the unlock marker to the /cache path config names", () => {
  for (const [file, s] of Object.entries(scriptText)) {
    assert.ok(s.includes(UNLOCK.markerPath), `${file}: marker path`);
    assert.match(s, /lock_state=0x01/, file);
    assert.match(s, /verifiedbootstate=orange/, file);
  }
});

test("the scripts stop on a non-0x00 read, gate the model and build type, and verify orange", () => {
  for (const [file, s] of Object.entries(scriptText)) {
    assert.match(s, /0x00.{0,3}LOCKED/, file); // bash "0x00 (LOCKED)", PS "0x00 \(LOCKED\)"
    assert.match(s, /UC-P10/, file);
    assert.match(s, /userdebug/, file);
    assert.match(s, /verifiedbootstate/, file);
  }
});

test("both scripts carry the SPDX line", () => {
  for (const [file, s] of Object.entries(scriptText)) assert.match(s, /SPDX-License-Identifier: GPL-2.0-or-later/, file);
});
