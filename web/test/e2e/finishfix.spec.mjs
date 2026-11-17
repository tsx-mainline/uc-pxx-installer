// SPDX-License-Identifier: GPL-2.0-or-later
// The finish through the restart into the new slot, and "Send the finish set"
// from the unit's status (page-rc-10 3cfdc1a, 86935ef; unit 4, 2026-11-16).
// The fake's sc.prov.statusAfter models the installer's order (serviceclone.go):
// after the consent, step 2's transport comes back status only (mode "status")
// and says "started", "armed", "restarting"; the page's finish opens its own
// sessions on that old link before the new slot's finish interface comes.
import { body, expect, finishSet, logText, open, phase, prov, s1, test, toBooted, toChecked, writeAndRestart } from "./helpers.mjs";

async function toRestart(page, p = {}) {
  const LIST = finishSet();
  await open(page, { start: "rockusb", step1: s1({ after: true }), finishPin: LIST.sha, prov: prov(p) },
    { goal: "mainline", step2: true, finish: { lists: [{ label: "test", sha256: LIST.sha }] }, finishSet: LIST.files });
  await toBooted(page);
  await page.waitForFunction(() => globalThis.__fake.rec.some((e) => e.dev === "step1" && e.op === "attach"));
  await page.click("#connect-step1");
  await expect(body(page)).toHaveAttribute("data-ui", "system", { timeout: 15000 });
  await expect(body(page)).toHaveAttribute("data-ui", "finish", { timeout: 20000 });
}
const fin = (page) => page.evaluate(() => JSON.parse(JSON.stringify(globalThis.__fake.env.finish || null)));
const s2s = (page) => page.evaluate(() => JSON.parse(JSON.stringify(globalThis.__fake.env.s2status || null)));
const SENT = [
  { name: "finish.json", accept: true },
  { name: "uc-pxx-step2-finish", accept: true },
  { name: "uc-pxx-bootctl-inst", accept: true },
];
const names = (f) => (f ? f.puts.map((x) => ({ name: x.name, accept: x.accept })) : []);

// (a) the restart race: the old slot's status link first, then the new slot's finish link
for (const after of [
  ["restarting"],
  ["restarting", "restarting", "restarting"],
  ["armed 0123456789ab", "restarting"],
  ["started 0123456789ab", "armed 0123456789ab", "restarting"],
  ["started 0123456789ab copy 900 of 1792", "started 0123456789ab", "restarting"], // R32: the longer copy code
]) {
  test(`the finish meets the old slot's status link (${after.join(", ")}): it waits, then sends the pinned set to the new slot`, async ({ page }) => {
    await toRestart(page, { statusAfter: after });
    await expect(body(page)).toHaveAttribute("data-finish-end", "finished", { timeout: 20000 });
    await expect(page.locator("#f-done")).toBeVisible();
    await expect(page.locator("#f-refused")).toBeHidden();
    const s = await s2s(page);
    // the page's step 2 saw the whole walk on the status-only link, then the finish opened a session per code
    expect(s.ended).toBe(true);
    expect(s.hellos).toEqual(after);
    expect(s.frames.filter((f) => f.type === 2 || f.type === 5)).toEqual([]);
    const f = await fin(page);
    expect(names(f)).toEqual(SENT);
    expect(f.committed).toEqual({ auto: false, session: f.committed.session });
    const l = await logText(page);
    expect(l).toContain("finish: nothing sent: The unit takes no finish set now (status only: ");
    expect(l).toContain("finish: the unit took the finish set");
    // review NIT-4: the finish's HELLO of the old slot's link is logged once for each distinct code, not each tick
    const hellos = l.split("\n").filter((x) => x.includes("finish: unit interface 1d6b:0105, mode status,"));
    for (const c of new Set(after)) expect(hellos.filter((x) => x.includes(`step2 "${c}"`)), c).toHaveLength(1);
  });
}

test("the old slot's link says restarting and stays: the page waits and sends nothing", async ({ page }) => {
  await toRestart(page, { statusAfter: ["restarting"], statusStays: true });
  await expect.poll(async () => ((await s2s(page)) || { hellos: [] }).hellos.length, { timeout: 10000 }).toBeGreaterThan(3);
  await expect(body(page)).toHaveAttribute("data-finish", "wait");
  await expect(body(page)).toHaveAttribute("data-finish-end", "none");
  expect(await fin(page)).toBe(null);
});

for (const code of ["idle", "never", "plan-refused: the unit changed", "startedx"]) {
  test(`a status-only link with step 2 "${code}" in the finish: refused, nothing sent`, async ({ page }) => {
    await toRestart(page, { statusAfter: [code], statusStays: true });
    await expect(body(page)).toHaveAttribute("data-finish-end", "refused", { timeout: 10000 });
    await expect(page.locator("#f-refused")).toBeVisible();
    expect(await fin(page)).toBe(null);
    expect((await s2s(page)).hellos).toEqual([code]);
  });
}

test("a status-only link with a hold in the finish: the finish ends, nothing sent", async ({ page }) => {
  await toRestart(page, { statusAfter: ["hold: ab state not known"], statusStays: true });
  await expect(body(page)).toHaveAttribute("data-finish-end", /^(refused|stop)$/, { timeout: 10000 });
  expect(await fin(page)).toBe(null);
});

// (b) "Send the finish set" from the unit's status (after a reload: no goal)
async function toStatus(page, sc = {}, opts = {}) {
  const LIST = finishSet();
  await open(page, { start: "finish", finishPin: LIST.sha, ...sc },
    { stopAt: "prepare", finish: { lists: [{ label: "test", sha256: LIST.sha }] }, finishSet: LIST.files, ...opts });
  await expect(body(page)).toHaveAttribute("data-goal", "none");
  await page.locator("#details summary").click();
  await page.click("#xstatus-read");
  await expect.poll(() => logText(page), { timeout: 10000 }).toContain("status: unit interface");
  await expect(page.locator("#xstatus-read")).toBeEnabled();
}

test("after a reload: the unit's status of a slot that waits for the finish set offers Send the finish set; it sends the pinned set", async ({ page }) => {
  await toStatus(page, { finishStates: ["finish: checking", "finish: started", "finish: started", "finish: started", "finish: not-yet", "finish: done"] });
  await expect(page.locator("#xstatus-finish")).toBeVisible();
  expect(await fin(page)).toMatchObject({ puts: [] });
  await page.click("#xstatus-finish");
  await expect(page.locator("#xstatus-finish")).toBeHidden();
  await expect(body(page)).toHaveAttribute("data-goal", "mainline");
  await expect(body(page)).toHaveAttribute("data-ui", "finish");
  // while the finish runs: the status read is off, and a second press does nothing
  await expect(body(page)).toHaveAttribute("data-finish", "wait");
  await expect(page.locator("#xstatus-read")).toBeDisabled();
  await page.evaluate(() => document.getElementById("xstatus-finish").click());
  await expect(body(page)).toHaveAttribute("data-finish-end", "finished", { timeout: 20000 });
  await expect(page.locator("#card-finish")).toBeVisible();
  await expect(page.locator("#f-done")).toBeVisible();
  const f = await fin(page);
  expect(names(f)).toEqual(SENT);
  const l = await logText(page);
  expect(l.split("finish: started from the unit's status").length - 1).toBe(1);
  // after the finish: the unit's status (now status only) offers nothing
  await page.click("#xstatus-read");
  await expect.poll(async () => (await logText(page)).split("status: unit interface").length - 1, { timeout: 10000 }).toBe(2);
  await expect(page.locator("#xstatus-finish")).toBeHidden();
});

for (const mode of ["status", "payload"]) {
  test(`the unit's status in mode ${mode}: no Send the finish set`, async ({ page }) => {
    await toStatus(page, { finishMode: mode });
    await expect(page.locator("#xstatus-finish")).toBeHidden();
    expect(names(await fin(page))).toEqual([]);
  });
}

test("Send the finish set on a page version that pins no finish set (the shipped flags): nothing is sent", async ({ page }) => {
  const LIST = finishSet();
  await open(page, { start: "finish", finishPin: LIST.sha }, { stopAt: "prepare", finishSet: LIST.files });
  await page.locator("#details summary").click();
  await page.click("#xstatus-read");
  await expect(page.locator("#xstatus-finish")).toBeVisible({ timeout: 10000 });
  await page.click("#xstatus-finish");
  await expect(body(page)).toHaveAttribute("data-finish-end", /^(no-set|off)$/, { timeout: 10000 });
  expect(names(await fin(page))).toEqual([]);
});

// statusOffer: on the Files step (before Next), a status read of a slot that waits for the finish set: the offer
async function statusOffer(page) {
  await page.evaluate(() => globalThis.__fake.env.finishReplug());
  await page.locator("#details summary").click();
  await page.click("#xstatus-read");
  await expect(page.locator("#xstatus-finish")).toBeVisible({ timeout: 10000 });
  await page.evaluate(() => { for (const d of globalThis.__fake.env.usb.attached.filter((x) => x.name === "xfer")) d.unplug(); });
}

// review MINOR-1 (PAGE-RC10-FINISHFIX-REVIEW.md): an offer from an earlier status read is not pressable inside an install
// review NIT-5 (PUBLISH-RC10B-PAGE-REVIEW.md): the offer only on the allowed steps: it is hidden at Connect
test("MINOR-1, NIT-5: a status read's finish offer is gone at Connect and in the install flow; a forced press does nothing", async ({ page }) => {
  const LIST = finishSet();
  await open(page, { start: "rockusb", finishPin: LIST.sha },
    { goal: "mainline", stopAt: "files-end", finish: { lists: [{ label: "test", sha256: LIST.sha }] }, finishSet: LIST.files });
  await statusOffer(page);
  await page.click("#files-next");
  await expect(body(page)).toHaveAttribute("data-ui", "connect");
  await expect(page.locator("#xstatus-finish")).toBeHidden(); // NIT-5
  await page.evaluate(() => document.getElementById("xstatus-finish").click());
  await expect(body(page)).toHaveAttribute("data-ui", "connect");
  await toChecked(page);
  await writeAndRestart(page);
  await phase(page, "restarted");
  await expect(page.locator("#xstatus-finish")).toBeHidden();
  await page.evaluate(() => document.getElementById("xstatus-finish").click());
  await expect(body(page)).toHaveAttribute("data-ui", "start");
  await expect(page.locator("#card-start")).not.toHaveClass(/folded/);
  await expect(page.locator("#connect-fastboot")).toBeVisible();
  expect(await logText(page)).not.toContain("finish: started from the unit's status");
});

// review NIT-1: a stop of step 2 on the old slot's status-only link is a stop (keep the unit powered), not "refused"
test("NIT-1: a status-only link with a stop code in the finish ends the finish as a stop, nothing sent", async ({ page }) => {
  await toRestart(page, { statusAfter: ["stop: the copy did not verify"], statusStays: true });
  await expect(body(page)).toHaveAttribute("data-finish-end", "stop", { timeout: 10000 });
  await expect(page.locator("#f-stop")).toBeVisible();
  expect(await fin(page)).toBe(null);
});

// review NIT-1 (PUBLISH-RC10B-PAGE-REVIEW.md, mutation m6): the normal step-2 → finish start clears an offer of an
// earlier status read, so the button does not come back after the finish ("Done" never becomes "refused")
test("NIT-1: a status read's offer, then the normal step 2 and finish to Done: the button stays hidden", async ({ page }) => {
  const LIST = finishSet();
  await open(page, { start: "rockusb", step1: s1({ after: true }), finishPin: LIST.sha, prov: prov() },
    { goal: "mainline", stopAt: "files-end", step2: true, finish: { lists: [{ label: "test", sha256: LIST.sha }] }, finishSet: LIST.files });
  await statusOffer(page);
  await page.click("#files-next");
  await toBooted(page);
  // the status read gave the page the permission for the unit's link: step 1 needs no pick
  await expect(body(page)).toHaveAttribute("data-finish-end", "finished", { timeout: 25000 });
  await expect(page.locator("#f-done")).toBeVisible();
  await expect(page.locator("#xstatus-finish")).toBeHidden();
  expect(names(await fin(page))).toEqual(SENT);
  expect(await logText(page)).not.toContain("finish: started from the unit's status");
});
