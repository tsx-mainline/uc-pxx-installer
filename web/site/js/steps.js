// SPDX-License-Identifier: GPL-2.0-or-later
// steps.js: the page's steps (DESIGN v3 §2, §3) and the pure mapping from the
// unit's status (HELLO/STATUS info) to what the page shows in step 2 and the
// finish. No DOM, no device. main.js runs the steps; step1usb.js maps the
// step-1 states.

// The steps in order, by goal: [id, pill name].
export const STEPS = Object.freeze({
  mainline: Object.freeze([["prepare", "Prepare"], ["files", "Files"], ["connect", "Connect"], ["start", "Start"],
    ["install", "Install"], ["system", "System"], ["finish", "Finish"]]),
  userdebug: Object.freeze([["prepare", "Prepare"], ["files", "Files"], ["connect", "Connect"], ["start", "Start"],
    ["install", "Install"], ["done", "Done"]]),
});

export function stepsFor(goal) {
  return STEPS[goal === "userdebug" ? "userdebug" : "mainline"];
}

// The copy of step 2 (RC-10c, R32): while the frozen block copy of system_b
// into system_a runs, the unit's code is "started <id12> copy <D> of <T>", D
// and T in whole MiB (T: the size of system_a), about once a second; before
// and after it "started <id12>". copyOf gives { d, t, done, total } (done
// and total in bytes) or null: not a copy code, T 0, or D over T.
export const MIB = 1048576;
const COPY = /^started [0-9a-f]{12} copy (\d{1,7}) of (\d{1,7})$/;
export function copyOf(s2) {
  const m = typeof s2 === "string" ? COPY.exec(s2) : null;
  if (!m) return null;
  const d = Number(m[1]), t = Number(m[2]);
  if (t <= 0 || d > t) return null;
  return { d, t, done: d * MIB, total: t * MIB };
}

// s2Key: a step-2 code for the log, without the copy amounts (one line for
// the copy, not one a second)
export function s2Key(s2) {
  return typeof s2 === "string" ? s2.replace(/^(started [0-9a-f]{12} copy) \d+ of \d+$/, "$1") : "";
}

// step2View: the line of the System card (its step-2 part) for the unit's step-2 code
// (info.step2 of the step-2 service, internal/step2/service.go). Returns
// { line, end, next, copy } with line one of the card's ids; end: the watch
// stops; next: "finish" when the unit restarts into the new slot; copy: the
// copy's amounts (copyOf) while the copy runs.
export function step2View(st) {
  const s2 = (st && st.info && typeof st.info.step2 === "string") ? st.info.step2 : "";
  if (/^(stop|hold)\b/.test(s2)) return { line: "s2-stop", end: true };
  if (s2 === "restarting" || /^restarting\b/.test(s2)) return { line: "s2-restarts", end: true, next: "finish" };
  if (/^plan-refused\b/.test(s2) || s2 === "never") return { line: "s2-refused", end: true };
  if (s2 === "not now") return { line: "s2-stopped", end: true };
  if (/^(started|writing|armed)\b/.test(s2)) {
    const copy = copyOf(s2);
    return copy ? { line: "s2-runs", end: false, copy } : { line: "s2-runs", end: false };
  }
  if (s2 === "confirm") return { line: "s2-auto", end: false };
  return { line: "", end: false };
}

// finishView: the line of the Finish card for the unit's finish code
// (finishusb.finishText's codes). Returns { line, end, outcome }.
export function finishView(st) {
  const s2 = (st && st.info && typeof st.info.step2 === "string") ? st.info.step2 : "";
  if (/^(stop|hold)\b/.test(s2)) return { line: "f-stop", end: true, outcome: "stop" };
  if (s2 === "finish: done" || s2 === "finish: switched") return { line: "f-done", end: true, outcome: "finished" };
  if (s2 === "finish: later") return { line: "f-later", end: true, outcome: "later" };
  if (s2 === "finish: refused" || s2 === "finish: supervised" || s2 === "finish: not a target") {
    return { line: "f-refused", end: true, outcome: s2.slice("finish: ".length).replace(/ /g, "-") };
  }
  if (/^finish: (checking|started|not-yet)$/.test(s2)) return { line: "f-running", end: false };
  return { line: "", end: false };
}
