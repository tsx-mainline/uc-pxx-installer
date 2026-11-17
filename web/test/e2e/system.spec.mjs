// SPDX-License-Identifier: GPL-2.0-or-later
// "Install the system" (DESIGN-R23 M6) with the fake slot B: the choice, the
// bars, the network retry and the cable fallback (the bundle checked against
// its pin first), the failed check, the step-2 consent and the unit's step-2
// outcomes; then the finish's own outcomes.
import { body, BUNDLE, bytes, expect, finishSet, logText, open, prov, s1, sha, test, toBooted } from "./helpers.mjs";

async function toSystem(page, sc = {}, opts = {}) {
  const LIST = finishSet();
  await open(page, { start: "rockusb", step1: s1({ after: true }), finishPin: LIST.sha, ...sc, prov: prov(sc.prov) },
    { goal: "mainline", step2: true, finish: { lists: [{ label: "test", sha256: LIST.sha }] }, finishSet: LIST.files, ...opts });
  await toBooted(page);
  await page.waitForFunction(() => globalThis.__fake.rec.some((e) => e.dev === "step1" && e.op === "attach"));
  await page.click("#connect-step1");
  await expect(body(page)).toHaveAttribute("data-ui", "system", { timeout: 15000 });
}
const pv = (page) => page.evaluate(() => JSON.parse(JSON.stringify(globalThis.__fake.env.prov || {})));

test("no network on the unit: the retry line and the cable offer; the chosen bundle is checked, sent, and the install goes on", async ({ page }) => {
  await toSystem(page, { prov: { net: "fail", retryTicks: 20 } });
  await expect(page.locator("#p-network")).toBeVisible({ timeout: 10000 });
  await expect(page.locator("#p-network")).toHaveText("The unit cannot download the packages. Check the unit's network cable. It tries again by itself.");
  await expect(page.locator("#p-cable-row")).toBeVisible();
  // 5 tries spent (fail network): its own line, the unit waits for a new COMMIT (review MINOR-C, MINOR-G)
  await expect(page.locator("#p-network-failed")).toBeVisible({ timeout: 10000 });
  await expect(page.locator("#p-network-failed")).toHaveText("The unit could not download the packages. Send them over the cable instead.");
  await expect(page.locator("#p-network")).toBeHidden();
  await expect(page.locator("#p-cable")).toHaveText("Send the packages over the cable instead");
  await page.click("#p-cable");
  await expect(page.locator("#bundle-link")).toHaveText("Download from GitHub");
  await expect(page.locator("#bundle-link")).toHaveAttribute("rel", "noopener noreferrer");
  // a wrong file first: refused on the page, nothing sent
  const bad = Buffer.from(BUNDLE);
  bad[1] ^= 1;
  await page.setInputFiles("#bundle-file", { name: "uc-pxx-profile-kiosk-ha-0.3.0-test.tar", mimeType: "application/octet-stream", buffer: bad });
  await expect(page.locator("#bundle-status")).toHaveText("This is not the package file. Choose uc-pxx-profile-kiosk-ha-0.3.0-test.tar.");
  expect((await pv(page)).puts).toEqual([]);
  await page.setInputFiles("#bundle-file", { name: "uc-pxx-profile-kiosk-ha-0.3.0-test.tar", mimeType: "application/octet-stream", buffer: BUNDLE });
  await expect(page.locator("#bundle-status")).toHaveText("uc-pxx-profile-kiosk-ha-0.3.0-test.tar: OK");
  await expect(body(page)).toHaveAttribute("data-ui", "finish", { timeout: 15000 });
  const p = await pv(page);
  expect(p.commits).toEqual([{ profile: "kiosk-ha", source: "net" }, { profile: "kiosk-ha", source: "cable" }]);
  expect(p.puts).toEqual([{ name: "uc-pxx-profile-kiosk-ha-0.3.0-test.tar", size: BUNDLE.length, accept: true }]);
  expect(p.files["uc-pxx-profile-kiosk-ha-0.3.0-test.tar"]).toBe(sha(BUNDLE));
  // §6.10: the PUT_END is the hand-over point; the link came back status only,
  // its mode still "provision" (the built kind: receiver.go provHello)
  expect(p.handovers).toBe(1);
  const after = p.frames.slice(p.frames.findIndex((f) => f.type === 2) + 1);
  expect(after.some((f) => f.mode === "provision" && f.statusOnly)).toBe(true);
  expect(after.filter((f) => f.type === 2 || (f.type === 5 && f.mode !== "step2"))).toEqual([]);
  // the page read a STATUS of the same session right before each COMMIT of the choice (review NIT-R1)
  p.frames.forEach((f, i) => { if (f.type === 5 && f.mode === "provision") expect(p.frames[i - 1].type).toBe(6); });
  expect(p.consent).toEqual({ auto: true, plan_id: "0123456789ab" });
  const l = await logText(page);
  expect(l).toContain(" fail network");
  expect(l).toContain("handed over");
  // uc-pxx-usb's own status interface before the system install and between its done and the consent: the page waited
  expect(l).toContain("mode status, state status-only");
  expect((await page.evaluate(() => globalThis.__fake.env.usbStatus.frames.filter((f) => f.type === 5 || f.type === 2)))).toEqual([]);
});

test("the System card says the unit needs the internet now (the owner's run opens it here)", async ({ page }) => {
  await toSystem(page, { prov: { total: 364000000 } });
  await expect(page.locator("#p-internet")).toHaveText("The unit needs an internet connection now.");
  await expect(page.locator("#p-internet")).toBeVisible();
});

test("the bars of the system install: the download in MB, then the packages counted", async ({ page }) => {
  await toSystem(page, { prov: { total: 364000000 } });
  await expect.poll(() => logText(page), { timeout: 10000 }).toContain("system: unit provision installing");
  const l = await logText(page);
  expect(l).toContain("system: unit provision downloading");
  await expect(body(page)).toHaveAttribute("data-ui", "finish", { timeout: 15000 });
});

test("the system on the unit is not as expected: the plain line, no consent", async ({ page }) => {
  await toSystem(page, { prov: { check: "fail" } });
  await expect(page.locator("#p-wrong")).toHaveText("The system on the unit is not as expected. Save the log and ask for help.", { timeout: 10000 });
  await expect(body(page)).toHaveAttribute("data-sys", "done");
  expect((await pv(page)).consent).toBeUndefined();
});

for (const [code, id, text] of [
  ["not now", "#s2-stopped", "Step 2 was stopped on the unit. Nothing was written. Restart the unit to see the question again, then hold OK for 3 seconds to start."],
  ["plan-refused: the plan refuses", "#s2-refused", "The unit did not start step 2. Nothing was written. Save the log and ask for help."],
  ["stop: step 2 stopped", "#s2-stop", "KEEP THE UNIT POWERED. Do not restart it. Save the log and ask for help."],
]) {
  test(`step 2 ends "${code}" on the unit: the System card says so`, async ({ page }) => {
    await toSystem(page, { step2States: ["confirm", code] });
    await expect(page.locator(id)).toHaveText(text, { timeout: 15000 });
    await expect(body(page)).toHaveAttribute("data-ui", "system");
  });
}

for (const [states, id, outcome] of [
  [["finish: checking", "finish: later"], "#f-later", "later"],
  [["finish: checking", "finish: refused"], "#f-refused", "refused"],
  [["finish: started", "stop: the check after step 2"], "#f-stop", "stop"],
]) {
  test(`the finish ends "${states.at(-1)}": the Finish card says so`, async ({ page }) => {
    await toSystem(page, { finishStates: states });
    await expect(body(page)).toHaveAttribute("data-finish-end", outcome, { timeout: 15000 });
    await expect(page.locator(id)).toBeVisible();
  });
}

test("review MINOR-11: with STEP2_USB_ENABLED and FINISH_USB_ENABLED false the page sends no choice and no consent", async ({ page }) => {
  const LIST = finishSet();
  await open(page, { start: "rockusb", step1: s1({ after: true }), prov: prov(), finishPin: LIST.sha }, { goal: "mainline", finishSet: LIST.files });
  await toBooted(page);
  await page.waitForFunction(() => globalThis.__fake.rec.some((e) => e.dev === "step1" && e.op === "attach"));
  await page.click("#connect-step1");
  await expect(body(page)).toHaveAttribute("data-ui", "system", { timeout: 15000 });
  await expect(page.locator("#s2-refused")).toBeVisible({ timeout: 10000 });
  const p = await pv(page);
  expect(p.commits).toEqual([]);
  expect(p.frames.filter((f) => f.type === 5 || f.type === 2)).toEqual([]);
});

test("review MINOR-11: the finish slot names another finish list: nothing is sent", async ({ page }) => {
  await toSystem(page, { finishPin: "9".repeat(64) });
  await expect(body(page)).toHaveAttribute("data-finish-end", "refused", { timeout: 15000 });
  const fin = await page.evaluate(() => JSON.parse(JSON.stringify(globalThis.__fake.env.finish)));
  expect(fin.puts).toEqual([]);
});

// the installer's "Page interface" §6.10
test("§6.10: the consent waits for the unit's plan id, then COMMIT {auto: true, plan_id}", async ({ page }) => {
  await toSystem(page, { prov: { noPlanIdHellos: 2, planId: "fedcba987654" } });
  await expect(body(page)).toHaveAttribute("data-ui", "finish", { timeout: 15000 });
  expect((await pv(page)).consent).toEqual({ auto: true, plan_id: "fedcba987654" });
  expect(await logText(page)).toContain("step 2: the unit does not name its plan yet: the page asks again");
});

test("§6.10: the unit's system is fixed to another profile: no choice is sent, the plain line", async ({ page }) => {
  await toSystem(page, { prov: { fixed: "kiosk" } });
  await expect(page.locator("#p-wrong")).toBeVisible({ timeout: 10000 });
  const p = await pv(page);
  expect(p.commits).toEqual([]);
  expect(p.frames.filter((f) => f.type === 5 || f.type === 2)).toEqual([]);
  expect(await logText(page)).toContain("the unit's system is kiosk (fixed, or its last choice), not kiosk-ha");
});

test("§6.10: the unit pins another bundle: the cable is not offered", async ({ page }) => {
  const other = [{ name: "kiosk-ha", file: "uc-pxx-profile-kiosk-ha-0.3.0-test.tar", size: BUNDLE.length, sha256: "c".repeat(64) },
    { name: "kiosk", file: "uc-pxx-profile-kiosk-0.3.0-test.tar", size: BUNDLE.length, sha256: "c".repeat(64) }, { name: "console", file: null, size: null, sha256: null }];
  await toSystem(page, { prov: { net: "fail", profiles: other, retryTicks: 20 } });
  await expect(page.locator("#p-network")).toBeVisible({ timeout: 10000 });
  await expect(page.locator("#p-cable-row")).toBeHidden();
  // 5 tries spent and no cable for this pin: the page ends with the plain line (review MINOR-C)
  await expect(page.locator("#p-wrong")).toBeVisible({ timeout: 10000 });
  await expect(body(page)).toHaveAttribute("data-sys", "done");
  const l = await logText(page);
  expect(l).toContain("the cable does not match this page's bundle");
  expect(l).toContain("fail network");
  expect((await pv(page)).commits).toEqual([{ profile: "kiosk-ha", source: "net" }]);
});

test("§6.10: the url (a LAN test copy) goes with net; one not in the form is not sent", async ({ page }) => {
  await toSystem(page, {}, { provisionUrl: "http://192.0.2.10:8080/bundles/" });
  await expect(body(page)).toHaveAttribute("data-ui", "finish", { timeout: 15000 });
  expect((await pv(page)).commits).toEqual([{ profile: "kiosk-ha", source: "net", url: "http://192.0.2.10:8080/bundles/" }]);
});

for (const url of ["http://user@192.0.2.10/bundles/", "http://192.0.2.10/bundles", "http://192.0.2.10/b/?x=1/", "http://192.0.2.10/b/#f/"]) {
  test(`§6.10 (built rule): the url ${url} is never sent: a page config error`, async ({ page }) => {
    await toSystem(page, {}, { provisionUrl: url });
    await expect(page.locator("#p-wrong")).toBeVisible({ timeout: 10000 });
    expect((await pv(page)).commits).toEqual([]);
    expect(await logText(page)).toContain("system: page config error: the url");
  });
}

test("§6.10 (built): the cable path ends failed after the status-only phase: the receiver comes back in full mode; the plain line", async ({ page }) => {
  await toSystem(page, { prov: { net: "fail", check: "fail" } });
  await chooseBundle(page);
  await expect(page.locator("#p-wrong")).toBeVisible({ timeout: 15000 });
  await expect(body(page)).toHaveAttribute("data-sys", "done");
  const p = await pv(page);
  expect(p.handovers).toBe(1);
  expect(p.refull).toBe(true);
  expect(p.consent).toBeUndefined();
  expect(await logText(page)).toContain("fail check");
});

test("review NIT-R1 (a): the unit leaves retry-wait while the chosen bundle is hashed: nothing is sent, the download goes on", async ({ page }) => {
  const big = bytes(48 * 1048576 + 5, 23);
  const systems = [{ id: "kiosk-ha", label: "Kiosk and Home Assistant", installs: "Kiosk and Home Assistant", keyRequired: false,
    bundle: { file: "uc-pxx-profile-kiosk-ha-0.3.0-test.tar", size: big.length, sha256: sha(big), url: null } },
  { id: "kiosk", label: "Kiosk only", installs: "the kiosk", keyRequired: false,
    bundle: { file: "uc-pxx-profile-kiosk-0.3.0-test.tar", size: big.length, sha256: sha(big), url: null } },
  { id: "console", label: "Console only (for your own software)", installs: "the console system", keyRequired: true, bundle: null }];
  await toSystem(page, { prov: { net: "fail", retryTicks: 400, bundleSize: big.length, bundleSha: sha(big) } }, { systems });
  await expect(page.locator("#p-cable-row")).toBeVisible({ timeout: 10000 });
  await page.click("#p-cable");
  // the unit's next try works: it downloads while the page hashes the file
  await page.evaluate(() => {
    document.getElementById("bundle-file").addEventListener("change", () => {
      const f = globalThis.__fake.env;
      f.sc.prov.net = "ok";
      f.prov.at = 0;
      f.prov.st = { state: "downloading", done: 0, total: 364000000 };
    });
  });
  await page.setInputFiles("#bundle-file", { name: "uc-pxx-profile-kiosk-ha-0.3.0-test.tar", mimeType: "application/octet-stream", buffer: big });
  await expect.poll(() => logText(page), { timeout: 15000 }).toContain("bundle: the unit does not take the cable now");
  await expect(page.locator("#bundle-status")).toBeHidden();
  await expect(body(page)).toHaveAttribute("data-ui", "finish", { timeout: 20000 });
  const p = await pv(page);
  expect(p.commits).toEqual([{ profile: "kiosk-ha", source: "net" }]);
  expect(p.puts).toEqual([]);
});

test("uc-pxx-usb's status interface with a leftover step-2 code (before the system install and before the consent): the page waits, then goes on", async ({ page }) => {
  await toSystem(page, { prov: { gapStep2: "never", bootGapMs: 800 } });
  await expect(body(page)).toHaveAttribute("data-ui", "finish", { timeout: 20000 });
  await expect(page.locator("#s2-refused")).toBeHidden();
  expect((await pv(page)).consent).toEqual({ auto: true, plan_id: "0123456789ab" });
});

async function chooseBundle(page) {
  await expect(page.locator("#p-cable-row")).toBeVisible({ timeout: 10000 });
  await page.click("#p-cable");
  await page.setInputFiles("#bundle-file", { name: "uc-pxx-profile-kiosk-ha-0.3.0-test.tar", mimeType: "application/octet-stream", buffer: BUNDLE });
}

test("review MINOR-B: a cut cable send waits for Send again (no retry by itself), then goes on", async ({ page }) => {
  await toSystem(page, { prov: { net: "fail", cutBundle: 1 } });
  await chooseBundle(page);
  await expect(page.locator("#bundle-again")).toBeVisible({ timeout: 10000 });
  await expect(page.locator("#bundle-status")).toHaveText("The transfer stopped. Nothing was written to the unit. Check the cable, then press Send again.");
  // the built root still waits for the cable bundle (state waiting): the page does not send by itself
  await page.waitForTimeout(1500); // several ticks: nothing is sent again by itself
  let p = await pv(page);
  expect(p.puts).toHaveLength(1);
  expect(p.commits).toHaveLength(2);
  expect(p.st.state).toBe("waiting");
  await page.click("#bundle-again");
  await expect(body(page)).toHaveAttribute("data-ui", "finish", { timeout: 15000 });
  p = await pv(page);
  expect(p.puts.map((x) => x.accept)).toEqual([true, true]);
  expect(p.commits.map((c) => c.source)).toEqual(["net", "cable", "cable"]);
  expect(p.handovers).toBe(1);
  p.frames.forEach((f, i) => { if (f.type === 5 && f.mode === "provision") expect(p.frames[i - 1].type).toBe(6); });
});

test("review MINOR-B: a second failed cable send ends it with the plain line", async ({ page }) => {
  await toSystem(page, { prov: { net: "fail", cutBundle: 2 } });
  await chooseBundle(page);
  await expect(page.locator("#bundle-again")).toBeVisible({ timeout: 10000 });
  await page.click("#bundle-again");
  await expect(page.locator("#p-wrong")).toBeVisible({ timeout: 10000 });
  await expect(page.locator("#p-cable-files")).toBeHidden();
  await expect(body(page)).toHaveAttribute("data-sys", "done");
  await page.waitForTimeout(1000);
  expect((await pv(page)).puts).toHaveLength(2);
});

test("review MINOR-C: the retries end in failed: check: a file chosen then is not sent", async ({ page }) => {
  await toSystem(page, { prov: { net: "fail", failAfterRetry: "check", retryTicks: 30 } });
  await expect(page.locator("#p-cable-row")).toBeVisible({ timeout: 10000 });
  await page.click("#p-cable");
  await expect(page.locator("#p-wrong")).toBeVisible({ timeout: 10000 });
  await expect(page.locator("#p-cable-files")).toBeHidden();
  await page.setInputFiles("#bundle-file", { name: "uc-pxx-profile-kiosk-ha-0.3.0-test.tar", mimeType: "application/octet-stream", buffer: BUNDLE });
  await expect.poll(() => logText(page), { timeout: 5000 }).toContain("a file chosen while the unit does not take the cable");
  await page.waitForTimeout(800);
  const p = await pv(page);
  expect(p.commits).toEqual([{ profile: "kiosk-ha", source: "net" }]);
  expect(p.puts).toEqual([]);
});
