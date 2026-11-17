// SPDX-License-Identifier: GPL-2.0-or-later
// Compressed downloads (1.3.1): GitHub Pages serves the site's files with
// Content-Encoding: gzip, so Content-Length is the gzip size and the browser
// gives the decoded bytes. Every download of the page (the image, the base,
// the finish set) must still work, every bar must stay at or below 100%, and
// the pins (sizes, sha256s) must still refuse a changed file.
import { readFileSync } from "node:fs";
import { gzipSync } from "node:zlib";
import { body, expect, finishSet, FW, IMAGE, logText, MANIFEST, open, prov, ROOTFS_ZST, s1, sha, TEMPLATE, test, toBooted } from "./helpers.mjs";

const K = Object.fromEntries(JSON.parse(readFileSync(new URL("../unit/sshkey-vectors.json", import.meta.url), "utf8")).vectors
  .map((v) => [v.name, v]));

// bars: every amount line and fill width the page shows, recorded as it changes
async function watchBars(page) {
  await page.evaluate(() => {
    globalThis.__bars = [];
    new MutationObserver(() => {
      for (const p of document.querySelectorAll(".prog")) {
        globalThis.__bars.push([p.querySelector(".label").textContent, p.querySelector(".amount").textContent,
          parseFloat(p.querySelector(".bar > div").style.width) || 0]);
      }
    }).observe(document.body, { subtree: true, childList: true, characterData: true, attributes: true });
  });
}

function checkBars(bars) {
  expect(bars.length).toBeGreaterThan(0);
  for (const [label, amount, pct] of bars) {
    expect(pct, `${label}: ${amount}`).toBeLessThanOrEqual(100);
    const m = amount.match(/^([\d,]+) (MB|KB) of ([\d,]+) (MB|KB)/);
    if (m) expect(Number(m[1].replace(/,/g, "")), `${label}: ${amount}`).toBeLessThanOrEqual(Number(m[3].replace(/,/g, "")));
  }
}

test("the test site really sends gzip: Content-Length is the gzip size, the body the full image", async ({ page }) => {
  await open(page, { start: "rockusb" }, { gzip: true, stopAt: "prepare" });
  const r = await page.evaluate(async () => {
    const x = await fetch("images/test.img", { cache: "no-store" });
    return { enc: x.headers.get("content-encoding"), len: Number(x.headers.get("content-length")), got: (await x.arrayBuffer()).byteLength };
  });
  expect(r).toEqual({ enc: "gzip", len: gzipSync(IMAGE).length, got: IMAGE.length });
  expect(r.len).not.toBe(IMAGE.length);
});

test("gzip, userdebug: the installer image downloads and passes its pin; the bar ends at 100%", async ({ page }) => {
  await open(page, { start: "rockusb" }, { gzip: true, stopAt: "prepare" });
  await watchBars(page);
  await page.click("#goal-userdebug");
  await expect(body(page)).toHaveAttribute("data-image", "ok");
  const l = await logText(page);
  expect(l).toContain(`image download images/test.img: ${IMAGE.length} bytes`);
  expect(l).toContain(`image sha256 ${sha(IMAGE)} OK`);
  const bars = await page.evaluate(() => globalThis.__bars);
  checkBars(bars);
  expect(bars.filter((b) => b[0] === "Installer").at(-1)[1]).toBe(`${Math.round(IMAGE.length / 1e6)} MB · done`);
});

test("gzip: a changed image is refused (its sha256 pin)", async ({ page }) => {
  const img = Buffer.from(IMAGE);
  img[4000] ^= 1;
  await open(page, { start: "rockusb" }, { gzip: true, stopAt: "prepare", siteImageBody: img });
  await page.click("#goal-userdebug");
  await expect(body(page)).toHaveAttribute("data-image-refusal", "image-sha");
});

for (const [name, files, why] of [["manifest.json", () => Buffer.from(MANIFEST.toString().replace("0.3.0-test-base", "0.3.0-test-bas2")), "is not the pin"],
  ["rootfs.ext4.zst", () => { const z = Buffer.from(ROOTFS_ZST); z[5] ^= 1; return z; }, "rootfs.ext4.zst has sha256"],
  ["boot-template.img", () => TEMPLATE.subarray(1), `boot-template.img has ${TEMPLATE.length - 1} bytes, the manifest says ${TEMPLATE.length}`],
  ["rootfs.ext4.zst", () => Buffer.concat([ROOTFS_ZST, Buffer.from("x")]), `rootfs.ext4.zst has ${ROOTFS_ZST.length + 1} bytes, the manifest says ${ROOTFS_ZST.length}`]]) {
  test(`gzip: a changed ${name} of the base is refused (${why})`, async ({ page }) => {
    await open(page, { start: "rockusb" }, { gzip: true, goal: "mainline", stopAt: "files", base: { files: { [name]: files() } } });
    await expect(body(page)).toHaveAttribute("data-base", /error|bad/);
    await expect(page.locator("#log")).toContainText(why);
    await expect(page.locator("#files-next")).toBeDisabled();
  });
}

test("gzip, mainline: the image, the base and the finish set download; the unit gets the pinned bytes; Finish done", async ({ page }) => {
  const LIST = finishSet();
  await open(page, { start: "rockusb", step1: s1({ after: true }), prov: prov(), finishPin: LIST.sha },
    { gzip: true, goal: "mainline", stopAt: "prepare", step2: true, finish: { lists: [{ label: "test", sha256: LIST.sha }] }, finishSet: LIST.files });
  await watchBars(page);
  await page.click("#goal-mainline");
  for (const c of await page.locator("#card-prepare input.req").all()) if (await c.isVisible()) await c.check();
  await page.click("#system-kiosk-ha");
  await page.click("#prepare-next");
  await expect(body(page)).toHaveAttribute("data-image", "ok");
  await expect(body(page)).toHaveAttribute("data-base", "ok");
  await expect(page.locator("#base-status")).toHaveText("Mainline Linux: OK");
  await page.setInputFiles("#fw-file", { name: "pcdfw-1.0.0.94-userdebug.bin", mimeType: "application/octet-stream", buffer: FW });
  await page.fill("#sshkey-input", K["ed25519"].input);
  await page.check("#sshkey-confirm");
  await page.click("#files-next");
  await toBooted(page);
  await page.waitForFunction(() => globalThis.__fake.rec.some((e) => e.dev === "step1" && e.op === "attach"));
  await page.click("#connect-step1");
  await expect(body(page)).toHaveAttribute("data-ui", "finish", { timeout: 30000 });
  await expect(page.locator("#f-done")).toBeVisible({ timeout: 15000 });
  const dl = await page.evaluate(() => globalThis.__fake.hex(globalThis.__fake.env.downloaded));
  expect(sha(Buffer.from(dl, "hex"))).toBe(sha(IMAGE));
  const u = await page.evaluate(() => JSON.parse(JSON.stringify(globalThis.__fake.env.step1)));
  expect(u.files["rootfs.ext4.zst"].sha256).toBe(sha(ROOTFS_ZST));
  expect(u.files["boot-template.img"].sha256).toBe(sha(TEMPLATE));
  const fin = await page.evaluate(() => JSON.parse(JSON.stringify(globalThis.__fake.env.finish)));
  expect(fin.committed).toBeTruthy();
  const l = await logText(page);
  expect(l).toContain(`finish: the set of this site: OK, finish.json sha256 ${LIST.sha}`);
  checkBars(await page.evaluate(() => globalThis.__bars));
});

test("gzip: a changed file of the finish set is refused, nothing is sent", async ({ page }) => {
  const LIST = finishSet();
  const bad = Buffer.from(LIST.files["uc-pxx-step2-finish"]);
  bad[10] ^= 1;
  await open(page, { start: "rockusb" }, { gzip: true, stopAt: "prepare", finish: { lists: [{ label: "test", sha256: LIST.sha }] },
    finishSet: { ...LIST.files, "uc-pxx-step2-finish": bad } });
  const r = await page.evaluate(async () => {
    const d = await import("/js/download.js");
    const f = await import("/js/finishusb.js");
    const files = {};
    for (const n of [f.LIST, ...f.FILES]) {
      const x = await d.fetchBytes(`finish/${n}`);
      if (!x.ok) return { fetched: false, why: x.why };
      files[n] = new Blob([x.bytes]);
    }
    const c = await f.checkSet({ dir: "finish/", files, other: [] }, [{ label: "t", sha256: (await import("/js/config.js")).FINISH.LISTS[0].sha256 }]);
    return { fetched: true, ok: c.ok, why: c.why };
  });
  expect(r.fetched).toBe(true);
  expect(r.ok).toBe(false);
  expect(r.why).toMatch(/uc-pxx-step2-finish has sha256/);
});
