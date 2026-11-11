// SPDX-License-Identifier: GPL-2.0-or-later
// The finish of step 2 over the cable (mainline-rc-4, owner Q-T8 = option 5):
// the page sends the picked uc-pxx-finish folder (finish.json and the two
// files it names) to the fake finish-mode interface of fake/fakeusb.js, follows
// the unit through its re-enumeration in status-only mode to the end of the
// finish, and shows the refusals, the cut text and each end. Also the
// read-only status view. No step-1 device is involved; the after-test
// invariants of helpers.mjs hold (no write of any kind).
import { mkdtempSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { body, expect, open, rec, sha, test } from "./helpers.mjs";

function bytes(n, seed) {
  const b = Buffer.alloc(n);
  let x = seed;
  for (let i = 0; i < n; i++) {
    x = (Math.imul(x, 1103515245) + 12345) >>> 0;
    b[i] = x >>> 24;
  }
  return b;
}

// release: a release folder with uc-pxx-finish/ (and a uc-pxx-mainline payload
// folder next to it, which the finish section ignores).
function release({ bin = 2 * 1048576 + 333, listSize = null, extra = false } = {}) {
  const dir = mkdtempSync(join(tmpdir(), "fin-"));
  const d = join(dir, "release", "uc-pxx-finish");
  mkdirSync(d, { recursive: true });
  mkdirSync(join(dir, "release", "normal-payload", "uc-pxx-mainline"), { recursive: true });
  writeFileSync(join(dir, "release", "normal-payload", "uc-pxx-mainline", "manifest.json"), "{}");
  const b = bytes(bin, 5), c = bytes(1048576 + 17, 9);
  const list = Buffer.from(JSON.stringify({ kind: "uc-pxx-finish", version: 1, files: [
    { name: "uc-pxx-step2-finish", size: listSize === null ? b.length : listSize, sha256: sha(b) },
    { name: "uc-pxx-bootctl-inst", size: c.length, sha256: sha(c) },
  ] }, null, 1) + "\n");
  writeFileSync(join(d, "finish.json"), list);
  writeFileSync(join(d, "uc-pxx-step2-finish"), b);
  writeFileSync(join(d, "uc-pxx-bootctl-inst"), c);
  if (extra) writeFileSync(join(d, "notes.txt"), "x");
  return { dir, list, b, c, sha: sha(list), lists: [{ label: "test finish set", sha256: sha(list) }] };
}

const fphase = (page, p) => expect(body(page)).toHaveAttribute("data-finish", p, { timeout: 30000 });
const fend = (page, o) => expect(body(page)).toHaveAttribute("data-finish-end", o, { timeout: 30000 });

async function u(page) {
  return page.evaluate(() => JSON.parse(JSON.stringify(globalThis.__fake.env.finish || null)));
}

async function start(page, r, sc = {}, lists = r.lists) {
  await open(page, Object.assign({ start: "finish", finishPin: r.sha }, sc), { finish: { lists } });
  await expect(page.locator("#finish-section")).toBeVisible();
  await expect(page.locator("#finish-connect")).toBeDisabled();
  await page.setInputFiles("#finish-folder", r.dir);
}

async function nothingSent(page) {
  const x = await u(page);
  expect(x.puts).toEqual([]);
  expect(x.data).toEqual([]);
  expect(x.committed).toBe(null);
}

test("ships off: the finish and status sections are hidden and do nothing", async ({ page }) => {
  await open(page, { start: "finish", finishPin: "0".repeat(64) });
  await expect(page.locator("#finish-section")).toBeHidden();
  await expect(page.locator("#xstatus-section")).toBeHidden();
  await expect(body(page)).toHaveAttribute("data-finish", "off");
  await expect(body(page)).toHaveAttribute("data-xstatus", "off");
  expect((await rec(page)).filter((e) => e.dev === "xfer" && e.op !== "attach")).toEqual([]);
});

for (const last of ["finish: done", "finish: switched"]) {
  test(`the finish set, the re-enumeration, the info codes to "finished" (${last})`, async ({ page }) => {
    const r = release();
    await start(page, r, { finishStates: ["finish: checking", "finish: started", "finish: not-yet", "finish: not-yet", last] });
    await fphase(page, "folder-ok");
    await expect(page.locator("#finish-folder-status")).toContainText("release/uc-pxx-finish");
    await page.click("#finish-connect");
    await fend(page, "finished");
    await expect(body(page)).toHaveAttribute("data-finish", "done");
    await expect(page.locator("#finish-status")).toContainText("Finished");
    await expect(page.locator("#finish-lines")).toContainText("slot A boots first");
    const x = await u(page);
    expect(x.committed).toEqual({ auto: false, session: 1 });
    const kinds = x.frames.map((f) => (f.type === 2 ? f.body.name : f.type));
    expect(kinds.slice(0, 5)).toEqual([1, "finish.json", "uc-pxx-step2-finish", "uc-pxx-bootctl-inst", 5]);
    expect(x.frames[4].body).toEqual({ auto: false });
    expect(x.frames.slice(5).every((f) => (f.type === 1 || f.type === 6) && f.mode === "status")).toBe(true);
    expect(x.puts.map((p) => [p.name, p.size])).toEqual([["finish.json", r.list.length], ["uc-pxx-step2-finish", r.b.length],
      ["uc-pxx-bootctl-inst", r.c.length]]);
    expect(x.data.filter((d) => d.name === "uc-pxx-step2-finish").map((d) => d.n)).toEqual([1048576, 1048576, 333]);
    // the "waiting for the unit to mark itself" text was shown on the way
    const log = await page.locator("#log").textContent();
    expect(log).toContain("finish: unit status status-only finish: not-yet");
    // one chooser only: after the re-enumeration the page found the unit by its permission
    const all = await rec(page);
    expect(all.filter((e) => e.op === "requestDevice").length).toBe(1);
    expect(all.filter((e) => e.dev === "xfer" && e.op === "attach").length).toBe(2);
    expect(all.filter((e) => e.dev === "rockusb" || e.dev === "fastboot")).toEqual([]);
  });
}

test("while the unit waits for its mark the page says so", async ({ page }) => {
  const r = release();
  await start(page, r, { finishStates: ["finish: checking", "finish: not-yet"] });
  await page.click("#finish-connect");
  await expect(page.locator("#finish-status")).toContainText("Waiting for the unit to mark itself", { timeout: 30000 });
  await expect(body(page)).toHaveAttribute("data-finish", "watch");
});

test("a finish.json that this page does not pin: refused on the page, nothing sent", async ({ page }) => {
  const r = release();
  await start(page, r, {}, [{ label: "other", sha256: "0".repeat(64) }]);
  await expect(page.locator("#finish-folder-status")).toContainText("is not a finish set of this page");
  await expect(page.locator("#finish-connect")).toBeDisabled();
  expect((await rec(page)).filter((e) => e.dev === "xfer" && e.op !== "attach")).toEqual([]);
  await nothingSent(page);
});

test("no list pinned (the shipped LISTS): every send is refused on the page with a clear text", async ({ page }) => {
  const r = release();
  await start(page, r, {}, []);
  await expect(page.locator("#finish-folder-status")).toContainText("pins no finish set yet");
  await expect(page.locator("#finish-connect")).toBeDisabled();
  expect((await rec(page)).filter((e) => e.dev === "xfer" && e.op !== "attach")).toEqual([]);
});

test("a list that is not the unit's pin: nothing sent", async ({ page }) => {
  const r = release();
  await start(page, r, { finishPin: "1".repeat(64) });
  await page.click("#finish-connect");
  await fphase(page, "error");
  await expect(page.locator("#finish-status")).toContainText("not the one this unit's slot names");
  await expect(page.locator("#finish-status")).toContainText("Nothing was sent");
  await nothingSent(page);
});

test("a file whose size is not the list's: refused on the page", async ({ page }) => {
  const r = release({ listSize: 2 * 1048576 + 334 });
  await start(page, r);
  await expect(page.locator("#finish-folder-status")).toContainText("uc-pxx-step2-finish has 2097485 bytes, finish.json says 2097486");
  await expect(page.locator("#finish-connect")).toBeDisabled();
  await nothingSent(page);
});

test("a uc-pxx-finish folder with another file: refused on the page", async ({ page }) => {
  const r = release({ extra: true });
  await start(page, r);
  await expect(page.locator("#finish-folder-status")).toContainText("holds other files (notes.txt)");
  await expect(page.locator("#finish-connect")).toBeDisabled();
});

for (const [mode, text] of [["status", "takes no finish set now (status only: idle)"], ["payload", "waits for the step-2 payload"]]) {
  test(`the unit's interface in ${mode} mode: the page says so and sends nothing`, async ({ page }) => {
    const r = release();
    await start(page, r, { finishMode: mode });
    await page.click("#finish-connect");
    await fphase(page, "error");
    await expect(page.locator("#finish-status")).toContainText(text);
    await expect(page.locator("#finish-status")).toContainText("Nothing was sent");
    await nothingSent(page);
  });
}

test("a cable pull during a file: the cut text, then a replug and a new session from finish.json", async ({ page }) => {
  const r = release();
  await start(page, r, { finishFault: "cutBin" });
  await page.click("#finish-connect");
  await fphase(page, "error");
  await expect(page.locator("#finish-status")).toContainText("The transfer stopped");
  await expect(page.locator("#finish-status")).toContainText("drops a partial copy");
  await expect(page.locator("#finish-status")).toContainText("starts again from finish.json");
  expect((await u(page)).committed).toBe(null);
  await page.evaluate(() => globalThis.__fake.env.finishReplug());
  await page.click("#finish-connect");
  await fend(page, "finished");
  const x = await u(page);
  expect(x.committed).toEqual({ auto: false, session: 2 });
  const puts = x.frames.filter((f) => f.type === 2).map((f) => f.body.name);
  expect(puts).toEqual(["finish.json", "uc-pxx-step2-finish", "finish.json", "uc-pxx-step2-finish", "uc-pxx-bootctl-inst"]);
  expect((await rec(page)).filter((e) => e.op === "requestDevice").length).toBe(1);
});

test("the COMMIT's reply lost in the re-enumeration: the page follows the status to the end", async ({ page }) => {
  const r = release();
  await start(page, r, { finishFault: "lostCommitReply", finishStates: ["finish: started", "finish: done"] });
  await page.click("#finish-connect");
  await fend(page, "finished");
  expect((await u(page)).committed).toEqual({ auto: false, session: 1 });
});

test("the link ends at the COMMIT and the unit did not take the set: the page says so, then sends again", async ({ page }) => {
  const r = release();
  await start(page, r, { finishFault: "dropCommit", finishStates: ["finish: done"] });
  await page.click("#finish-connect");
  await fphase(page, "error");
  await expect(page.locator("#finish-status")).toContainText("did not take the finish set");
  expect((await u(page)).committed).toBe(null);
  await page.click("#finish-connect");
  await fend(page, "finished");
  expect((await u(page)).committed).toEqual({ auto: false, session: 3 });
});

for (const [code, name] of [["stop: the check after step 2", "a STOP"], ["hold: ab state not known", "a hold"]]) {
  test(`${name} on the unit: KEEP THE UNIT POWERED`, async ({ page }) => {
    const r = release();
    await start(page, r, { finishStates: ["finish: checking", code] });
    await page.click("#finish-connect");
    await fend(page, "stop");
    await expect(page.locator("#finish-status")).toContainText("KEEP THE UNIT POWERED");
    await expect(page.locator("#finish-status")).toContainText("Do not restart it");
    await expect(page.locator("#finish-status")).toHaveClass("stop");
  });
}

test("a supervised unit: the finish runs only by the supervised procedure", async ({ page }) => {
  const r = release();
  await start(page, r, { finishStates: ["finish: checking", "finish: supervised"] });
  await page.click("#finish-connect");
  await fend(page, "supervised");
  await expect(page.locator("#finish-status")).toContainText("A supervised unit: the finish runs only by the supervised procedure");
});

test("not a step-2 target: nothing to finish", async ({ page }) => {
  const r = release();
  await start(page, r, { finishStates: ["finish: not a target"] });
  await page.click("#finish-connect");
  await fend(page, "not-target");
  await expect(page.locator("#finish-status")).toContainText("This unit is not a step-2 target: nothing to finish");
});

test("a refusal of the finish: the unit's line is shown", async ({ page }) => {
  const r = release();
  await start(page, r, { finishStates: ["finish: checking", "finish: refused"] });
  await page.click("#finish-connect");
  await fend(page, "refused");
  await expect(page.locator("#finish-status")).toContainText("The unit refused the finish: the finish: the keeper's vbmeta is not the one the record names");
});

for (const which of ["finish", "step2"]) {
  test(`the read-only status view (only ${which} enabled): the unit's fields, no serial, nothing sent`, async ({ page }) => {
    const r = release();
    const opts = which === "finish" ? { finish: { lists: r.lists } } : { step2: { pins: [] } };
    await open(page, { start: "finish", finishPin: r.sha }, opts);
    await expect(page.locator("#xstatus-section")).toBeVisible();
    await page.click("#xstatus-read");
    await expect(body(page)).toHaveAttribute("data-xstatus", "read");
    const t = page.locator("#xstatus-table");
    await expect(t).toContainText("UC-P10");
    await expect(t).toContainText("pxx-ab12cd");
    await expect(t).toContainText("finish");
    await expect(t).toContainText("waiting-finish");
    await expect(t).toContainText("finish: waiting");
    await expect(page.locator("#xstatus-table tr th")).toHaveText(["Model", "Slot", "Build token", "Interface mode", "Interface state", "Step 2", "Plan id"]);
    await expect(page.locator("#xstatus-lines")).toContainText("the page may send the pinned finish executor");
    // the fake's USB serial string is never shown or logged
    expect(await page.locator("body").textContent()).not.toContain("UC-PXX-STEP2");
    const x = await u(page);
    expect(x.frames.map((f) => f.type)).toEqual([1]);
    await nothingSent(page);
    // a second read: the permission is kept, no second chooser
    await page.click("#xstatus-read");
    await expect.poll(async () => (await u(page)).frames.length).toBe(2);
    expect((await rec(page)).filter((e) => e.op === "requestDevice").length).toBe(1);
  });
}
