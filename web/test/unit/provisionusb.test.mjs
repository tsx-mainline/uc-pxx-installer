// SPDX-License-Identifier: GPL-2.0-or-later
// provisionusb.js against the installer's "Page interface" §6.10 and its
// "Built" paragraph (MAINLINE-RC10-2026-11-15.md; the provision and consent
// kinds of uc-pxx-installer mainline-rc-10 7f8ea48): the status map decides on state and fail (never
// on reason), the choice only in a choice state with a profile the unit
// offers and not against a fixed one, the cable only with the bundle the unit
// pins, the url only with net and in its form, the bundle only with its pinned
// size, the consent only with the plan id the unit shows.
import { test } from "node:test";
import assert from "node:assert/strict";
import { cableMatches, checkChoice, checkConsent, CHOICE_STATES, profileOf, Q, sendBundle, sendChoice, sendConsent, urlOk, view } from "../../site/js/provisionusb.js";

test("view: the bars, the lines, the end; failed decides on fail, never on reason", () => {
  assert.deepEqual(view({ state: "downloading", done: 10, total: 364 }), { line: "", bar: "download", done: 10, total: 364, end: false });
  // the cable bundle: the page's own send bar; after the hand-over receiving (done = total): wait
  assert.deepEqual(view({ state: "receiving", done: 2, total: 2, reason: "the bundle was received: the unit checks it" }), { line: "", end: false });
  assert.deepEqual([...Q.STATES], ["waiting", "downloading", "receiving", "installing", "applying", "checking", "done", "retry-wait", "failed"]);
  assert.equal(Q.MODE, "provision");
  assert.equal(Q.GAP_MODE, "status");
  assert.deepEqual(view({ state: "installing", done: 120, total: 276 }), { line: "", bar: "install", done: 120, total: 276, end: false });
  assert.equal(view({ state: "applying" }).line, "p-checking");
  assert.equal(view({ state: "checking" }).line, "p-checking");
  assert.deepEqual(view({ state: "done" }), { line: "", end: true, next: "consent" });
  assert.deepEqual(view({ state: "retry-wait", reason: "no route", next_try_s: 30 }), { line: "p-network", end: false, cable: true });
  // 5 tries spent: its own line (the unit waits for a COMMIT, it does not try again by itself)
  assert.deepEqual(view({ state: "failed", fail: "network", reason: "5 tries spent" }), { line: "p-network-failed", end: false, cable: true });
  // the reason text is never parsed
  assert.deepEqual(view({ state: "failed", reason: "network: no route" }), { line: "p-wrong", end: true });
  assert.deepEqual(view({ state: "failed", fail: "check", reason: "network: the content check" }), { line: "p-wrong", end: true });
  for (const f of ["bundle", "space", "install", "check", "", undefined, "Network"]) assert.deepEqual(view({ state: "failed", fail: f }), { line: "p-wrong", end: true });
  assert.deepEqual([...Q.FAILS], ["network", "bundle", "space", "install", "check"]);
  assert.deepEqual(view({ state: "x" }), { line: "", end: false });
  assert.equal(view({ state: "downloading", done: -1, total: "x" }).total, 0);
});

test("urlOk: the built rule (handover.CheckBaseURL): http or https, a host, no user info, no query or fragment, the path ends in /, printable ASCII, at most 512 bytes", () => {
  for (const u of ["http://192.0.2.10:8080/bundles/", "https://mirror.example/x/", "http://h/", "https://h:8443/a/b/"]) assert.equal(urlOk(u), true, u);
  const long = "https://h/" + "a".repeat(501) + "/";
  assert.equal(long.length, 512);
  assert.equal(urlOk(long), true);
  for (const u of [long.slice(0, -1) + "a/", "ftp://h/", "file:///x/", "http://user@h/", "http://u:p@h/", "http://@h/", "https:///x/", "h/x/", "", null, 7,
    "https://h/" + "é".repeat(252) + "/",
    // the built rule: the path ends in "/" (at least "/"), no query, no fragment, no space or control byte
    "http://h", "http://h/x", "http://h/x/?a=1", "http://h/x/?", "http://h/x/#f", "http://h/#/", "http://h/a b/", "http://h/a\tb/",
    "HTTP://h/", "http://h\\x/", "http://:80/", "http://h/%zz/", "http://h/%2/", "http://h/a%/"]) assert.equal(urlOk(u), false, String(u));
  assert.equal(urlOk("http://h/a%20b/"), true);
});

const PIN = { file: "b.tar", size: 5, sha256: "a".repeat(64) };
const H = (o = {}) => ({ mode: "provision", state: "waiting", profile: "", status_only: false,
  profiles: [{ name: "kiosk-ha", ...PIN }, { name: "kiosk", file: "k.tar", size: 9, sha256: "b".repeat(64) }, { name: "console", file: null, size: null, sha256: null }], ...o });

test("checkChoice: the unit's profiles, the fixed profile, the cable pin, the url", () => {
  assert.deepEqual([...CHOICE_STATES], ["waiting", "retry-wait", "failed"]);
  const ok = (h, c) => checkChoice(h, c).go;
  assert.equal(ok(H(), { profile: "kiosk-ha", source: "net" }), "ok");
  assert.equal(ok(H({ state: "failed" }), { profile: "kiosk-ha", source: "cable", pin: PIN }), "ok");
  assert.equal(ok(H({ state: "downloading" }), { profile: "kiosk-ha", source: "net" }), "stop");
  assert.equal(ok(H({ mode: "payload" }), { profile: "kiosk-ha", source: "net" }), "stop");
  assert.equal(ok(H(), { profile: "kiosk-xx", source: "net" }), "stop");
  assert.equal(ok(H(), { profile: "kiosk-ha", source: "usb" }), "stop");
  assert.equal(ok(H({ profiles: [] }), { profile: "kiosk-ha", source: "net" }), "stop");
  assert.equal(ok(H({ profiles: undefined }), { profile: "kiosk-ha", source: "net" }), "stop");
  // HELLO profile (the fixed one, else the last choice, else the root's): the same one only
  assert.equal(ok(H({ state: "failed", profile: "kiosk-ha" }), { profile: "kiosk-ha", source: "net" }), "ok");
  assert.match(checkChoice(H({ state: "failed", profile: "kiosk" }), { profile: "kiosk-ha", source: "net" }).why, /system is kiosk \(fixed, or its last choice\)/);
  // a status-only link takes no COMMIT (the window before the root starts it again in full mode): wait, nothing sent
  assert.equal(ok(H({ state: "failed", status_only: true }), { profile: "kiosk-ha", source: "net" }), "wait");
  // the cable only with the unit's pin of this profile (file, size, sha256); console has none
  assert.equal(ok(H(), { profile: "kiosk-ha", source: "cable", pin: { ...PIN, sha256: "c".repeat(64) } }), "stop");
  assert.equal(ok(H(), { profile: "kiosk-ha", source: "cable", pin: { ...PIN, size: 6 } }), "stop");
  assert.equal(ok(H(), { profile: "kiosk-ha", source: "cable", pin: { ...PIN, file: "x.tar" } }), "stop");
  assert.equal(ok(H(), { profile: "kiosk-ha", source: "cable", pin: null }), "stop");
  assert.equal(ok(H(), { profile: "console", source: "cable", pin: null }), "stop");
  assert.equal(ok(H(), { profile: "console", source: "net" }), "ok");
  // the url: net only, in its form
  assert.equal(ok(H(), { profile: "kiosk-ha", source: "net", url: "http://192.0.2.10/b/" }), "ok");
  assert.equal(ok(H(), { profile: "kiosk-ha", source: "net", url: "http://x@192.0.2.10/b/" }), "stop");
  assert.equal(ok(H({ state: "failed" }), { profile: "kiosk-ha", source: "cable", pin: PIN, url: "http://192.0.2.10/b/" }), "stop");
  // a url the unit does not take is the page's own config error (review NIT-4)
  assert.equal(checkChoice(H(), { profile: "kiosk-ha", source: "net", url: "http://192.0.2.10/b" }).config, true);
  assert.notEqual(checkChoice(H({ profile: "kiosk" }), { profile: "kiosk-ha", source: "net" }).config, true);
  assert.equal(profileOf(H(), "kiosk").file, "k.tar");
  assert.equal(cableMatches(H(), "kiosk-ha", PIN), true);
  assert.equal(cableMatches(H(), "console", null), false);
});

function fakeLink(answer = { ok: true }) {
  const sent = [];
  return {
    sent,
    async request(type, body) { sent.push({ type, body }); return answer; },
    async putFile(name, file) { sent.push({ put: name, size: file.size }); return { accepted: true, ok: true, sha256: "s" }; },
  };
}

test("sendChoice: COMMIT {profile, source[, url]} only after the check; a refusal is not ok", async () => {
  const l = fakeLink();
  assert.equal((await sendChoice(l, H({ state: "downloading" }), { profile: "kiosk-ha", source: "net" })).ok, false);
  assert.equal((await sendChoice(l, H({ profile: "kiosk" }), { profile: "kiosk-ha", source: "net" })).ok, false);
  assert.equal((await sendChoice(l, H(), { profile: "kiosk-ha", source: "net", url: "ftp://h/" })).ok, false);
  assert.equal((await sendChoice(l, H(), { profile: "kiosk-ha", source: "net", url: "ftp://h/" })).config, true);
  const w = await sendChoice(l, H({ status_only: true }), { profile: "kiosk-ha", source: "net" });
  assert.equal(w.ok, false);
  assert.equal(w.wait, true);
  assert.deepEqual(l.sent, []);
  assert.deepEqual(await sendChoice(l, H(), { profile: "kiosk-ha", source: "net", url: null }), { ok: true });
  assert.deepEqual(await sendChoice(l, H({ state: "failed" }), { profile: "kiosk-ha", source: Q.SOURCE_CABLE, pin: PIN }), { ok: true });
  assert.deepEqual(await sendChoice(l, H(), { profile: "kiosk", source: "net", url: "http://192.0.2.10:8080/b/" }), { ok: true });
  assert.deepEqual(l.sent.map((x) => x.body), [{ profile: "kiosk-ha", source: "net" }, { profile: "kiosk-ha", source: "cable" },
    { profile: "kiosk", source: "net", url: "http://192.0.2.10:8080/b/" }]);
  const r = fakeLink({ ok: false, reason: "the profile is fixed: kiosk" });
  assert.match((await sendChoice(r, H(), { profile: "kiosk-ha", source: "net" })).why, /did not take the choice: the profile is fixed/);
});

test("sendBundle: the pinned size first; the PUT_END ok is the hand-over", async () => {
  const l = fakeLink();
  assert.equal((await sendBundle(l, PIN, new Blob(["1234"]))).ok, false);
  assert.deepEqual(l.sent, []);
  const logs = [];
  assert.deepEqual(await sendBundle(l, PIN, new Blob(["12345"]), null, (m) => logs.push(m)), { ok: true });
  assert.deepEqual(l.sent, [{ put: "b.tar", size: 5 }]);
  assert.match(logs[0], /handed over/);
});

test("sendConsent: COMMIT {auto: true, plan_id} with the unit's plan id, only on a waiting unit; no plan id yet = wait", async () => {
  const l = fakeLink({ ok: true, auto: true });
  const W = (info) => ({ mode: "step2", state: "waiting-consent", info });
  assert.equal(checkConsent({ mode: "step2", state: "consent-complete", info: { plan_id: "0123456789ab" } }).go, "stop");
  assert.equal(checkConsent({ mode: "provision", state: "waiting-consent", info: { plan_id: "0123456789ab" } }).go, "stop");
  assert.equal(checkConsent(W(undefined)).go, "wait");
  for (const id of ["", "0123456789a", "0123456789abc", "0123456789AB", 12]) assert.equal(checkConsent(W({ plan_id: id })).go, "wait");
  const r = await sendConsent(l, W({}));
  assert.equal(r.ok, false);
  assert.equal(r.wait, true);
  assert.deepEqual(l.sent, []);
  assert.deepEqual(await sendConsent(l, W({ plan_id: "0123456789ab" })), { ok: true, planId: "0123456789ab" });
  assert.deepEqual(l.sent, [{ type: 5, body: { auto: true, plan_id: "0123456789ab" } }]);
  const n = fakeLink({ ok: false, reason: "plan_id is not the plan the unit shows", auto: true });
  const x = await sendConsent(n, W({ plan_id: "0123456789ab" }));
  assert.equal(x.ok, false);
  assert.notEqual(x.wait, true);
});
