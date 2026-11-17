// SPDX-License-Identifier: GPL-2.0-or-later
// The progress bar's text (W7): amount, total, the computed time left from
// the mean speed of the last 10 s, KB for small files, MB = 10^6.
import { test } from "node:test";
import assert from "node:assert/strict";
import { amountText, countText, leftText, Rate, WINDOW_MS } from "../../site/js/progress.js";

test("amountText: starting, the amount with the time left, done", () => {
  assert.equal(amountText(0, 973e6, NaN), "starting …");
  assert.equal(amountText(486.5e6, 973e6, 40), "487 MB of 973 MB · about 40 s left");
  assert.equal(amountText(1e6, 1189e6, 600), "1 MB of 1,189 MB · about 10 min left");
  assert.equal(amountText(1e6, 973e6, NaN), "1 MB of 973 MB"); // no speed yet: no time
  assert.equal(amountText(973e6, 973e6, 0), "973 MB · done");
  assert.equal(amountText(1000, 900, 0), "1 KB · done");
  assert.equal(amountText(1500, 290000, 3), "2 KB of 290 KB · about 3 s left");
});

test("leftText: seconds under 90, minutes from 90; at least 1 s", () => {
  assert.equal(leftText(0.2), "about 1 s left");
  assert.equal(leftText(89.4), "about 89 s left");
  assert.equal(leftText(90), "about 2 min left");
  assert.equal(leftText(180), "about 3 min left");
  assert.equal(leftText(NaN), "");
  assert.equal(leftText(-1), "");
});

test("Rate: the mean speed of the last 10 s only", () => {
  let t = 0;
  const r = new Rate(() => t);
  assert.ok(Number.isNaN(r.speed()));
  r.add(0);
  // 1 MB/s for 20 s, then 10 MB/s for 10 s: the window sees only the fast part
  for (let s = 1; s <= 20; s++) { t = s * 1000; r.add(s * 1e6); }
  for (let s = 1; s <= 10; s++) { t = 20000 + s * 1000; r.add(20e6 + s * 10e6); }
  assert.ok(Math.abs(r.speed() - 10e6) / 10e6 < 0.15, String(r.speed()));
  assert.ok(Math.abs(r.left(120e6, 220e6) - 10) < 2);
  assert.equal(WINDOW_MS, 10000);
});

test("countText: the packages the unit installs, n of N", () => {
  assert.equal(countText(120, 276), "120 of 276");
  assert.equal(countText(300, 276), "276 of 276");
  assert.equal(countText(0, 0), "starting …");
});
