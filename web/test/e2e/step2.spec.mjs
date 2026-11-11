// SPDX-License-Identifier: GPL-2.0-or-later
// Step 2 over the cable (STEP2-USB-TRANSPORT §3.5, §7 item 4): the page sends
// the picked release folder's normal payload to the fake step-2 transport of
// fake/fakeusb.js, follows the unit through the seal (a re-enumeration in
// status-only mode, the same permission) to "restarting", and shows the
// refusals and the cut texts. No step-1 device is involved; the after-test
// invariants of helpers.mjs still hold (no write of any kind).
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

// release: a release folder with the normal payload (and a STEP-2 one, which
// the page must not pick: its manifest is not a step-2 payload of the page).
function release({ rootfs = 3 * 1048576 + 4096, tamperRootfs = false } = {}) {
  const dir = mkdtempSync(join(tmpdir(), "rel-"));
  const mk = (sub, seed) => {
    const d = join(dir, sub, "uc-pxx-mainline");
    mkdirSync(d, { recursive: true });
    const tmpl = bytes(34816, seed), root = bytes(rootfs, seed + 1);
    const man = Buffer.from(JSON.stringify({ schema: 2, kind: "uc-pxx-mainline", version: `v0.0.${seed}-test`, model: "UC-P10",
      boot_template: { file: "boot-template.img", size: tmpl.length, sha256: sha(tmpl) },
      rootfs: { file: "rootfs.ext4", size: root.length, sha256: sha(root), label: "tsxroot" } }, null, 2));
    writeFileSync(join(d, "manifest.json"), man);
    writeFileSync(join(d, "boot-template.img"), tmpl);
    if (tamperRootfs) root[root.length - 1] ^= 1;
    writeFileSync(join(d, "rootfs.ext4"), root);
    return { man, tmpl, root, sha: sha(man) };
  };
  const step2 = mk("step2-payload", 3);
  const normal = mk("normal-payload", 7);
  return { dir, normal, step2, pins: [{ label: "test normal payload", manifest_sha256: normal.sha }] };
}

const step2 = (page) => expect(body(page)).toHaveAttribute("data-step2", /.*/);
const s2phase = (page, p) => expect(body(page)).toHaveAttribute("data-step2", p, { timeout: 30000 });

async function u(page) {
  return page.evaluate(() => JSON.parse(JSON.stringify(globalThis.__fake.env.step2 || null)));
}

test("ships off: the step-2 section is hidden and does nothing", async ({ page }) => {
  await open(page, { start: "step2" });
  await expect(page.locator("#step2-section")).toBeHidden();
  await step2(page);
  await expect(body(page)).toHaveAttribute("data-step2", "off");
  expect((await rec(page)).filter((e) => e.dev === "step2" && e.op !== "attach")).toEqual([]);
});

for (const auto of [false, true]) {
  test(`the payload, the seal, the step-2 states to "restarting" (${auto ? "continue by itself" : "hold OK"})`, async ({ page }) => {
    const r = release();
    await open(page, { start: "step2", step2Pins: [r.normal.sha] }, { step2: { pins: r.pins } });
    await expect(page.locator("#step2-section")).toBeVisible();
    await expect(page.locator("#step2-connect")).toBeDisabled();
    await page.setInputFiles("#step2-folder", r.dir);
    await s2phase(page, "folder-ok");
    await expect(page.locator("#step2-folder-status")).toContainText("normal-payload/uc-pxx-mainline");
    if (auto) await page.check("#step2-auto");
    await page.click("#step2-connect");
    await s2phase(page, "done");
    await expect(page.locator("#step2-status")).toContainText("restarts into the new slot");
    await expect(page.locator("#step2-lines")).toContainText("restarting");
    const x = await u(page);
    expect(x.committed).toEqual({ auto, session: 1 });
    // the order: HELLO, the manifest, the template, the rootfs, COMMIT; then STATUS after the seal
    const kinds = x.frames.map((f) => (f.type === 2 ? f.body.name : f.type));
    expect(kinds.slice(0, 5)).toEqual([1, "manifest.json", "boot-template.img", "rootfs.ext4", 5]);
    expect(x.frames.slice(5).every((f) => f.type === 1 || f.type === 6)).toBe(true);
    // the rootfs in 1 MiB transfers, the last one shorter
    expect(x.data.filter((d) => d.name === "rootfs.ext4").map((d) => d.n)).toEqual([1048576, 1048576, 1048576, 4096]);
    // one chooser only: after the seal the page found the unit again by its permission
    const all = await rec(page);
    expect(all.filter((e) => e.op === "requestDevice").length).toBe(1);
    expect(all.filter((e) => e.dev === "step2" && e.op === "attach").length).toBe(2);
    expect(all.filter((e) => e.op === "set-alt").length).toBeGreaterThanOrEqual(2);
    // no step-1 device was touched
    expect(all.filter((e) => e.dev === "rockusb" || e.dev === "fastboot")).toEqual([]);
  });
}

test("a folder whose payload this page does not pin: refused before any device is opened", async ({ page }) => {
  const r = release();
  await open(page, { start: "step2", step2Pins: [r.normal.sha] }, { step2: { pins: [{ label: "other", manifest_sha256: "0".repeat(64) }] } });
  await page.setInputFiles("#step2-folder", r.dir);
  await expect(page.locator("#step2-folder-status")).toContainText("No step-2 payload of this page");
  await expect(page.locator("#step2-connect")).toBeDisabled();
  expect((await rec(page)).filter((e) => e.dev === "step2" && e.op !== "attach")).toEqual([]);
});

test("a download with a changed file size: refused on the page", async ({ page }) => {
  const r = release();
  writeFileSync(join(r.dir, "normal-payload/uc-pxx-mainline/rootfs.ext4"), Buffer.alloc(10));
  await open(page, { start: "step2", step2Pins: [r.normal.sha] }, { step2: { pins: r.pins } });
  await page.setInputFiles("#step2-folder", r.dir);
  await expect(page.locator("#step2-folder-status")).toContainText("sizes are not the manifest's");
});

test("the unit's gate does not pin the payload: nothing is sent", async ({ page }) => {
  const r = release();
  await open(page, { start: "step2", step2Pins: ["1".repeat(64)] }, { step2: { pins: r.pins } });
  await page.setInputFiles("#step2-folder", r.dir);
  await page.click("#step2-connect");
  await s2phase(page, "error");
  await expect(page.locator("#step2-status")).toContainText("step-2 gate does not take this payload");
  const x = await u(page);
  expect(x.puts).toEqual([]);
  expect(x.data).toEqual([]);
});

test("the unit refuses the rootfs (RAM): no byte of it is sent, the reason is shown", async ({ page }) => {
  const r = release();
  await open(page, { start: "step2", step2Pins: [r.normal.sha], step2Fault: "refuseRootfs" }, { step2: { pins: r.pins } });
  await page.setInputFiles("#step2-folder", r.dir);
  await page.click("#step2-connect");
  await s2phase(page, "error");
  await expect(page.locator("#step2-status")).toContainText("not enough free RAM");
  await expect(page.locator("#step2-status")).toContainText("Nothing was written");
  const x = await u(page);
  expect(x.data.filter((d) => d.name === "rootfs.ext4")).toEqual([]);
  expect(x.committed).toBe(null);
});

test("the unit's sha256 check refuses a changed rootfs (same size): no commit", async ({ page }) => {
  const r = release({ tamperRootfs: true });
  await open(page, { start: "step2", step2Pins: [r.normal.sha] }, { step2: { pins: r.pins } });
  await page.setInputFiles("#step2-folder", r.dir);
  await page.click("#step2-connect");
  await s2phase(page, "error");
  await expect(page.locator("#step2-status")).toContainText("did not take rootfs.ext4");
  expect((await u(page)).committed).toBe(null);
});

test("a cable pull during the rootfs: the cut text, then a replug and a new session from the manifest", async ({ page }) => {
  const r = release();
  await open(page, { start: "step2", step2Pins: [r.normal.sha], step2Fault: "unplugRootfs" }, { step2: { pins: r.pins } });
  await page.setInputFiles("#step2-folder", r.dir);
  await page.click("#step2-connect");
  await s2phase(page, "error");
  await expect(page.locator("#step2-status")).toContainText("The transfer stopped");
  await expect(page.locator("#step2-status")).toContainText("drops a partial copy");
  await page.evaluate(() => globalThis.__fake.env.step2Replug());
  await page.click("#step2-connect");
  await s2phase(page, "done");
  const x = await u(page);
  expect(x.committed).toEqual({ auto: false, session: 2 });
  const s2 = x.frames.filter((f) => f.type === 2).map((f) => f.body.name);
  expect(s2).toEqual(["manifest.json", "boot-template.img", "rootfs.ext4", "manifest.json", "boot-template.img", "rootfs.ext4"]);
  expect((await rec(page)).filter((e) => e.op === "requestDevice").length).toBe(1);
});

test("a STOP on the unit: KEEP THE UNIT POWERED", async ({ page }) => {
  const r = release();
  await open(page, { start: "step2", step2Pins: [r.normal.sha], step2States: ["confirm", "started 0123456789ab", "stop: verify failed"] },
    { step2: { pins: r.pins } });
  await page.setInputFiles("#step2-folder", r.dir);
  await page.click("#step2-connect");
  await s2phase(page, "done");
  await expect(page.locator("#step2-status")).toContainText("KEEP THE UNIT POWERED");
});

test("a plan refusal on the unit is shown in words", async ({ page }) => {
  const r = release();
  await open(page, { start: "step2", step2Pins: [r.normal.sha], step2States: ["confirm", "plan-refused: the keeper is not successful"] },
    { step2: { pins: r.pins } });
  await page.setInputFiles("#step2-folder", r.dir);
  await page.click("#step2-connect");
  await s2phase(page, "done");
  await expect(page.locator("#step2-status")).toContainText("The unit refused step 2: the keeper is not successful");
});

test("closing the page during the transfer asks first", async ({ page }) => {
  const r = release({ rootfs: 40 * 1048576 });
  await open(page, { start: "step2", step2Pins: [r.normal.sha] }, { step2: { pins: r.pins } });
  await page.setInputFiles("#step2-folder", r.dir);
  await page.click("#step2-connect");
  await s2phase(page, "sending");
  const asked = await page.evaluate(() => {
    const ev = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(ev);
    return ev.defaultPrevented;
  });
  expect(asked).toBe(true);
  await s2phase(page, "done");
});
