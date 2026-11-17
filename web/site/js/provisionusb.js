// SPDX-License-Identifier: GPL-2.0-or-later
// provisionusb.js: the page's client of "Install the system" (DESIGN-R23 §3.6,
// §7.4): after step 1, slot B's USB link (uc-pxx-xfer, kind provision) takes
// the person's system choice, B installs it by itself from the pinned bundle
// (or from the bundle the page sends over the cable), and then the page gives
// the step-2 consent (AUTO-STEP2: the unit's 30 s countdown, Back stops it) and
// follows the clone. The frames and the session are those of step2usb.js.
//
// The definition is the installer's "Page interface" §6.10 with its "Built"
// paragraph (uc-pxx-installer mainline-rc-10, MAINLINE-RC10-2026-11-15.md:
// "Provision and the step-2 consent"), as the code at 7f8ea48 sends it:
// internal/xfer provision.go (HELLO, COMMIT, the cable PUT, STATUS), the
// consent kind consent.go and receiver.go mode(), internal/xfer/handover
// provision.go (the states, the fail words, CheckBaseURL) and
// internal/provision service.go (what the root does with a choice). Every name
// the page uses is in Q below, so the page follows that definition in one
// place. The unit is the authority: it checks the choice, the url, the
// bundle's size and sha256 again (and the root provision service checks every
// bundle member again after the hand-over).
import { Step2Link, T } from "./step2usb.js";

export { Step2Link };

export const Q = Object.freeze({
  // the kind provision says "provision" in every HELLO and STATUS, also when
  // it is status only (status_only true: after the hand-over of a cable
  // bundle, until the work ends); receiver.go provHello / provStatus
  MODE: "provision",
  // after "done" the root gives the gadget back to uc-pxx-usb, whose own
  // status interface says "status" (state "status-only") until step 2 takes
  // the gadget for its consent: the page waits (§6.10 "Built")
  GAP_MODE: "status",
  WAITING: "waiting", // the unit waits for the choice (COMMIT {profile, source[, url]}), or for the cable bundle
  SOURCE_NET: "net", // the unit fetches the pinned bundle itself (console: nothing is fetched)
  SOURCE_CABLE: "cable", // the page sends the bundle (one PUT after the COMMIT, same session)
  PROFILES: Object.freeze(["kiosk-ha", "kiosk", "console"]),
  // url (net only, handover.CheckBaseURL): a base URL, http or https, a host,
  // no user info, no query or fragment, the path ends in "/" (at least "/"),
  // printable ASCII, at most 512 bytes; the unit appends the pinned file name;
  // the file name, size and sha256 stay the pins
  URL_MAX: 512,
  // the states of a STATUS while the unit installs (handover.ProvisionStates)
  DOWNLOADING: "downloading",
  RECEIVING: "receiving", // the cable bundle arrives (and, after its PUT_END, until the root takes it)
  INSTALLING: "installing",
  APPLYING: "applying",
  CHECKING: "checking",
  DONE: "done",
  RETRY: "retry-wait",
  FAILED: "failed",
  STATES: Object.freeze(["waiting", "downloading", "receiving", "installing", "applying", "checking", "done", "retry-wait", "failed"]),
  // fail (only in failed): one word (handover.FailWords); the page decides
  // on it, never on reason
  FAIL_NETWORK: "network", // the first try and 5 retries spent (or no cable bundle in 6 h): the page offers the cable
  FAILS: Object.freeze(["network", "bundle", "space", "install", "check"]),
  // the step-2 consent on the link after the system is installed (the
  // uc-pxx-xfer kind consent: HELLO mode "step2", state waiting-consent,
  // info.plan_id; COMMIT {auto, plan_id})
  CONSENT_MODE: "step2",
  CONSENT_WAITING: "waiting-consent",
  PLAN_ID: /^[0-9a-f]{12}$/,
});

// view: what the page shows for a provision STATUS: { line, bar, end, cable }.
// bar: "download" (bytes) or "install" (packages), with done and total. The
// decision is on state and fail only; reason goes to the log, never parsed.
export function view(st) {
  const s = st && typeof st.state === "string" ? st.state : "";
  const n = (x) => (Number.isSafeInteger(x) && x >= 0 ? x : 0);
  if (s === Q.DOWNLOADING) return { line: "", bar: "download", done: n(st.done), total: n(st.total), end: false };
  // the cable bundle: the page's own bar "Sending the packages" shows the send;
  // after its PUT_END the unit says receiving (done = total) until the root
  // takes the bundle over: the page waits
  if (s === Q.RECEIVING) return { line: "", end: false };
  if (s === Q.INSTALLING) return { line: "", bar: "install", done: n(st.done), total: n(st.total), end: false };
  if (s === Q.APPLYING || s === Q.CHECKING) return { line: "p-checking", end: false };
  if (s === Q.DONE) return { line: "", end: true, next: "consent" };
  if (s === Q.RETRY) return { line: "p-network", end: false, cable: true };
  // 5 tries spent: the unit waits for a new COMMIT (the page offers the cable)
  if (s === Q.FAILED && st.fail === Q.FAIL_NETWORK) return { line: "p-network-failed", end: false, cable: true };
  if (s === Q.FAILED) return { line: "p-wrong", end: true };
  return { line: "", end: false };
}

// CHOICE_STATES: the states in which the unit takes a COMMIT (the choice):
// waiting, and after a failed or waiting download a new one (it starts the
// work again; the page then offers the cable).
export const CHOICE_STATES = Object.freeze([Q.WAITING, Q.RETRY, Q.FAILED]);

// urlOk: the url form the unit takes (§6.10 "Built", handover.CheckBaseURL),
// checked on the raw text (the page is never wider than the unit): 1 to 512
// bytes of printable ASCII (no space), http:// or https://, a host, no user
// info, no query or fragment, a path that starts and ends with "/" (at least
// "/"), "%" only before two hex digits. A backslash is refused too (a
// browser would read it as "/").
export function urlOk(u) {
  if (typeof u !== "string" || u.length === 0 || u.length > Q.URL_MAX || !/^[\x21-\x7e]+$/.test(u)) return false;
  if (/[?#\\]/.test(u)) return false;
  // a "%" only as an escape of two hex digits (Go's url.Parse refuses others)
  if (/%(?![0-9A-Fa-f]{2})/.test(u)) return false;
  const m = /^(https?):\/\/([^/]*)(\/.*)$/.exec(u);
  if (!m || m[2] === "" || m[2].includes("@") || !m[3].endsWith("/")) return false;
  let x;
  try {
    x = new URL(u);
  } catch {
    return false;
  }
  return x.protocol === `${m[1]}:` && x.hostname !== "" && x.username === "" && x.password === "";
}

// profileOf: the unit's entry of the profile id in HELLO profiles
// ([{name, file, size, sha256}]; file, size, sha256 null for console).
export function profileOf(h, id) {
  return (h && Array.isArray(h.profiles) && h.profiles.find((p) => p && p.name === id)) || null;
}

// cableMatches: the unit pins the same bundle file as the page (name, size,
// sha256): only then can the page send it over the cable.
export function cableMatches(h, id, pin) {
  const p = profileOf(h, id);
  return !!(p && pin && typeof p.file === "string" && p.file === pin.file && p.size === pin.size && p.sha256 === pin.sha256);
}

// checkChoice decides on the unit's HELLO (with the state of the STATUS read
// right after it, see main.js) before the choice is sent: { go: "ok" },
// { go: "wait", why } (a status-only receiver: it takes no COMMIT now; the
// root starts it again in full mode after a failure: ask again, send nothing)
// or { go: "stop", why, config? } (config: the page's own config, not the
// unit). The unit takes a choice only in a choice state; it offers this
// profile; HELLO profile is the fixed one, else the last choice the receiver
// took, else the root's ("" when none): another one is not sent; the cable
// only with the bundle the unit pins; the url only with net and in its form.
export function checkChoice(h, c) {
  if (!h || h.mode !== Q.MODE || !CHOICE_STATES.includes(h.state)) {
    return { go: "stop", why: `the unit does not wait for the system choice (mode ${h && h.mode}, state ${h && h.state})` };
  }
  if (h.status_only === true) return { go: "wait", why: `the unit's link is status only now (state ${h.state})` };
  if (!Q.PROFILES.includes(c.profile)) return { go: "stop", config: true, why: `the page's system ${JSON.stringify(c.profile)} is not a profile` };
  if (c.source !== Q.SOURCE_NET && c.source !== Q.SOURCE_CABLE) return { go: "stop", config: true, why: `source ${JSON.stringify(c.source)}` };
  if (c.url != null && (c.source !== Q.SOURCE_NET || !urlOk(c.url))) {
    return { go: "stop", config: true, why: `the url ${JSON.stringify(c.url)} is not a url the unit takes (PROVISION_URL)` };
  }
  if (!profileOf(h, c.profile)) return { go: "stop", why: `the unit does not offer the system ${c.profile}` };
  if (typeof h.profile === "string" && h.profile !== "" && h.profile !== c.profile) {
    return { go: "stop", why: `the unit's system is ${h.profile} (fixed, or its last choice), not ${c.profile}` };
  }
  if (c.source === Q.SOURCE_CABLE && !cableMatches(h, c.profile, c.pin)) {
    return { go: "stop", why: `the unit does not pin this page's bundle for ${c.profile}` };
  }
  return { go: "ok" };
}

// sendChoice: COMMIT {profile, source[, url]} after checkChoice. c: { profile,
// source, pin (the page's bundle pin, for the cable), url (net only: the
// bundle's base URL for the unit, PROVISION.PROVISION_URL, a LAN test copy
// only; null = the unit's own pinned urls, not sent) }. The reply is {ok,
// reason?}. For the cable the bundle follows by sendBundle in the same session.
export async function sendChoice(link, h, c, log) {
  const d = checkChoice(h, c);
  if (d.go !== "ok") return { ok: false, wait: d.go === "wait", config: !!d.config, why: d.why };
  const body = { profile: c.profile, source: c.source };
  if (c.url != null) body.url = c.url;
  if (log) log(`system: COMMIT profile ${c.profile}, source ${c.source}${c.url != null ? `, url ${c.url}` : ""}`);
  const r = await link.request(T.COMMIT, body);
  if (r.ok !== true) return { ok: false, why: `the unit did not take the choice: ${r.reason}` };
  return { ok: true };
}

// sendBundle: the one PUT of the bundle (the pinned name and size) after a
// cable choice, in the same session. The page checked the file's sha256 against
// the pin before (download.checkFile). The PUT_END {ok, name, sha256, reason?}
// with ok is the hand-over point: the unit takes the bundle (and checks it
// again); STATUS then says receiving (done = total) until the root starts the
// receiver again status only (mode still "provision", status_only true).
export async function sendBundle(link, pin, file, progress, log) {
  if (!pin || file.size !== pin.size) return { ok: false, why: `the bundle has ${file.size} bytes, the pin says ${pin && pin.size}` };
  const r = await link.putFile(pin.file, file, (s, size) => progress && progress(s, size));
  if (!r.accepted) return { ok: false, why: `the unit refused the bundle: ${r.reason}` };
  if (!r.ok) return { ok: false, why: `the unit did not take the bundle: ${r.reason}` };
  if (log) log(`system: ${pin.file} sent (${file.size} bytes), unit sha256 ${r.sha256}: handed over`);
  return { ok: true };
}

// checkConsent decides on the unit's HELLO before the consent: { go: "ok",
// planId }, { go: "wait", why } (no plan id yet: ask again, send nothing) or
// { go: "stop", why }.
export function checkConsent(h) {
  if (!h || h.mode !== Q.CONSENT_MODE || h.state !== Q.CONSENT_WAITING) {
    return { go: "stop", why: `the unit does not wait for step 2 (mode ${h && h.mode}, state ${h && h.state})` };
  }
  const id = h.info && h.info.plan_id;
  if (typeof id !== "string" || !Q.PLAN_ID.test(id)) return { go: "wait", why: "the unit does not name its plan yet" };
  return { go: "ok", planId: id };
}

// sendConsent: the AUTO-STEP2 consent, COMMIT {auto: true, plan_id} with the
// plan id the unit shows (info.plan_id), only on a unit that waits for it.
export async function sendConsent(link, h, log) {
  const d = checkConsent(h);
  if (d.go !== "ok") return { ok: false, wait: d.go === "wait", why: d.why };
  if (log) log(`step 2: COMMIT auto true, plan_id ${d.planId} (the unit's 30 s countdown; Back on the unit stops it)`);
  const r = await link.request(T.COMMIT, { auto: true, plan_id: d.planId });
  if (r.ok !== true) return { ok: false, why: `the unit did not take the consent: ${r.reason}` };
  return { ok: true, planId: d.planId };
}
