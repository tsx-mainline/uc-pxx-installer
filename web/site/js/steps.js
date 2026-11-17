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

// step2View: the line of the System card (its step-2 part) for the unit's step-2 code
// (info.step2 of the step-2 service, internal/step2/service.go). Returns
// { line, end, next } with line one of the card's ids; end: the watch stops;
// next: "finish" when the unit restarts into the new slot.
export function step2View(st) {
  const s2 = (st && st.info && typeof st.info.step2 === "string") ? st.info.step2 : "";
  if (/^(stop|hold)\b/.test(s2)) return { line: "s2-stop", end: true };
  if (s2 === "restarting" || /^restarting\b/.test(s2)) return { line: "s2-restarts", end: true, next: "finish" };
  if (/^plan-refused\b/.test(s2) || s2 === "never") return { line: "s2-refused", end: true };
  if (s2 === "not now") return { line: "s2-stopped", end: true };
  if (/^(started|writing|armed)\b/.test(s2)) return { line: "s2-runs", end: false };
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
