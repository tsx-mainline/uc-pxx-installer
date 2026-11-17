// SPDX-License-Identifier: GPL-2.0-or-later
// steps.js and texts.js: the step lists, the step-2 and finish status maps,
// the stop box (DESIGN §2.8).
import { test } from "node:test";
import assert from "node:assert/strict";
import { copyOf, finishView, MIB, s2Key, step2View, stepsFor } from "../../site/js/steps.js";
import { ejectLine, STOP_TITLE, STOP_WHY, stopView, TEXT } from "../../site/js/texts.js";
import { readFileSync } from "node:fs";

test("the steps of each goal (DESIGN v3 §0, §3)", () => {
  assert.deepEqual(stepsFor("mainline").map(([, n]) => n), ["Prepare", "Files", "Connect", "Start", "Install", "System", "Finish"]);
  assert.deepEqual(stepsFor("userdebug").map(([, n]) => n), ["Prepare", "Files", "Connect", "Start", "Install", "Done"]);
});

const st = (step2) => ({ info: { step2 } });

test("step2View: the unit's step-2 codes (internal/step2/service.go) to the card's lines", () => {
  assert.deepEqual(step2View(st("confirm")), { line: "s2-auto", end: false });
  for (const c of ["started 0123456789ab", "writing system_b", "armed 0123456789ab"]) assert.equal(step2View(st(c)).line, "s2-runs");
  assert.deepEqual(step2View(st("restarting")), { line: "s2-restarts", end: true, next: "finish" });
  for (const c of ["plan-refused: the unit is not ready", "plan-refused: the seal", "never"]) assert.deepEqual(step2View(st(c)), { line: "s2-refused", end: true });
  assert.deepEqual(step2View(st("not now")), { line: "s2-stopped", end: true });
  for (const c of ["stop: step 2 stopped", "hold: ab state not known"]) assert.deepEqual(step2View(st(c)), { line: "s2-stop", end: true });
  assert.deepEqual(step2View(st("")), { line: "", end: false });
  assert.deepEqual(step2View({}), { line: "", end: false });
});

test("R32: the copy code of step 2: the amounts in whole MiB, then the plain code again; the other forms are no copy", () => {
  assert.equal(MIB, 1048576);
  assert.deepEqual(copyOf("started 0123456789ab copy 0 of 1792"), { d: 0, t: 1792, done: 0, total: 1792 * 1048576 });
  assert.deepEqual(copyOf("started 0123456789ab copy 900 of 1792"), { d: 900, t: 1792, done: 900 * 1048576, total: 1792 * 1048576 });
  assert.deepEqual(copyOf("started 0123456789ab copy 1792 of 1792"), { d: 1792, t: 1792, done: 1792 * 1048576, total: 1792 * 1048576 });
  for (const c of ["started 0123456789ab", "started 0123456789ab copy 1793 of 1792", "started 0123456789ab copy 5 of 0",
    "started 0123456789AB copy 5 of 1792", "started 0123456789a copy 5 of 1792", "started 0123456789ab copy 5 of 1792 ", "started 0123456789ab copy -5 of 1792",
    "started 0123456789ab copy 5.5 of 1792", "started 0123456789ab copy  5 of 1792", "armed 0123456789ab copy 5 of 1792", "xstarted 0123456789ab copy 5 of 1792",
    "started 0123456789ab copy 12345678 of 12345678", "", null, undefined, 5]) {
    assert.equal(copyOf(c), null, String(c));
  }
  // the System card: the same s2-runs line, with the copy while it runs
  assert.deepEqual(step2View(st("started 0123456789ab copy 448 of 1792")), { line: "s2-runs", end: false, copy: copyOf("started 0123456789ab copy 448 of 1792") });
  assert.deepEqual(step2View(st("started 0123456789ab")), { line: "s2-runs", end: false });
  assert.deepEqual(step2View(st("started 0123456789ab copy 1793 of 1792")), { line: "s2-runs", end: false });
  // the log key: one line for the whole copy
  assert.equal(s2Key("started 0123456789ab copy 448 of 1792"), "started 0123456789ab copy");
  assert.equal(s2Key("started 0123456789ab copy 1792 of 1792"), s2Key("started 0123456789ab copy 0 of 1792"));
  assert.equal(s2Key("started 0123456789ab"), "started 0123456789ab");
  assert.equal(s2Key("armed 0123456789ab"), "armed 0123456789ab");
  assert.equal(s2Key(undefined), "");
  // main.js: the finish waits through the old slot's codes, the longer copy code too (only the three words)
  const m = readFileSync(new URL("../../site/js/main.js", import.meta.url), "utf8");
  const src = m.match(/h\.mode === "status" && (\/\^\(started\|armed\|restarting\)\\b\/)\.test\(s2\)/);
  assert.ok(src, "the finish wait regex");
  const re = new RegExp(src[1].slice(1, -1));
  for (const c of ["started 0123456789ab copy 900 of 1792", "started 0123456789ab", "armed 0123456789ab", "restarting"]) assert.ok(re.test(c), c);
  for (const c of ["startedx", "idle", "stop: x", "finish: started"]) assert.ok(!re.test(c), c);
});

test("finishView: the finish codes to the card's lines and outcomes", () => {
  assert.equal(finishView(st("finish: done")).outcome, "finished");
  assert.equal(finishView(st("finish: switched")).line, "f-done");
  assert.deepEqual(finishView(st("finish: later")), { line: "f-later", end: true, outcome: "later" });
  for (const c of ["finish: refused", "finish: supervised", "finish: not a target"]) assert.equal(finishView(st(c)).line, "f-refused");
  for (const c of ["finish: checking", "finish: started", "finish: not-yet"]) assert.deepEqual(finishView(st(c)), { line: "f-running", end: false });
  for (const c of ["stop: the check after step 2", "hold: ab state not known"]) assert.equal(finishView(st(c)).outcome, "stop");
  assert.equal(finishView(st("finish: waiting")).line, "");
});

test("the stop box: three titles, one Why line, and every Refusal code of enter.js has its line", () => {
  assert.deepEqual(stopView("model", false), { title: STOP_TITLE.nothing, why: "Why: this unit is not a UC-P10.", kind: "nothing" });
  assert.equal(stopView("maskrom", false).title, "Stop. The unit is in Maskrom mode. Do nothing more with it.");
  assert.equal(stopView("entry-not-benign", true).title, "The page stopped. Keep the unit connected and powered, and do not restart it.");
  assert.equal(stopView("unexpected", false).kind, "nothing");
  assert.equal(stopView("unexpected", true).kind, "keep");
  assert.equal(stopView("no-pre-entry", false).why, "Why: the unit is already in fastboot. Power it off and on, then start again in download mode.");
  // every fatal code thrown by enter.js has a line (no fallback text on a known stop)
  const src = readFileSync(new URL("../../site/js/enter.js", import.meta.url), "utf8");
  const codes = new Set([...src.matchAll(/new Refusal\("([a-z0-9-]+)"/g)].map((m) => m[1]));
  const nonFatal = ["image-empty", "image-sha", "image-model", "image-format", "two-devices"];
  for (const c of codes) if (!nonFatal.includes(c) && c !== "other-device") assert.ok(STOP_WHY[c], c);
  assert.ok(STOP_WHY["other-device"]); // the fatal identity stop (the non-fatal "choose the unit" is a notice)
});

test("the eject line by platform", () => {
  assert.equal(ejectLine("Windows"), TEXT.ejectWin);
  assert.equal(ejectLine("macOS"), "In Finder, press the eject button next to UC-PXX-INST.");
  assert.equal(ejectLine("Linux"), "In the file manager, press the eject button next to UC-PXX-INST.");
});
