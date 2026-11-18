// SPDX-License-Identifier: GPL-2.0-or-later
// The whole flow of the RC-10 page with the fake unit (DESIGN v3 §2, §3):
// Prepare, Files, Connect, Start (the entry, the installer, the step-1 files
// over the cable), Install, and for mainline Step 2 (the unit's own) and the
// Finish. Both goals, on a Windows and a non-Windows platform string. At each
// step the page's visible text is checked: no time estimate (only the bars'
// computed time left), and the cards fold as the steps go.
import { body, cbws, cmds, expect, finishSet, FW, IMAGE, logText, open, phase, prov, rec, ROOTFS_ZST, s1, sha, TEMPLATE, test, toBooted } from "./helpers.mjs";
import { readFileSync } from "node:fs";

const K = Object.fromEntries(JSON.parse(readFileSync(new URL("../unit/sshkey-vectors.json", import.meta.url), "utf8")).vectors
  .map((v) => [v.name, v]));

// visibleText: the text a person sees now, without the bars' amount lines
// and the log (Details).
async function visibleText(page) {
  return page.evaluate(() => {
    const out = [];
    const walk = (el) => {
      if (el.hidden || el.matches(".amount, #log, #term, footer, details:not([open]) > :not(summary)")) return;
      const cs = getComputedStyle(el);
      if (cs.display === "none" || cs.visibility === "hidden") return;
      for (const n of el.childNodes) {
        if (n.nodeType === 3) out.push(n.textContent);
        else if (n.nodeType === 1) walk(n);
      }
    };
    walk(document.body);
    return out.join(" ").replace(/\s+/g, " ");
  });
}

// the time-estimate rule (owner R17): no "about N s/min", no "N to M
// minutes", no "takes", no "in about"; a hold of OK "for 3 seconds" is an
// instruction, not an estimate
function timeWords(text) {
  const t = text.replace(/for 3 seconds/g, "");
  return t.match(/\babout (?:\d+\s*(?:s|sec|seconds?|mins?|minutes?|hours?)\b|an? (?:second|minute|hour))|\b\d+\s*(?:–|-|to)\s*\d+\s*(?:s|sec|seconds?|min|minutes?)\b|\btakes? (?:about |up to )?(?:\d|a |an )|\bin about\b|\b\d+\s*(?:s|sec|seconds?|min|minutes?|hours?)\b/gi) || [];
}

async function noTime(page) {
  expect(timeWords(await visibleText(page))).toEqual([]);
}

async function card(page, id, cls) {
  if (cls === "open") await expect(page.locator(`#card-${id}`)).not.toHaveClass(/folded|later/);
  else await expect(page.locator(`#card-${id}`)).toHaveClass(new RegExp(cls));
}

for (const platform of ["Windows", "Linux", "macOS"]) {
  test(`userdebug on ${platform}: Prepare → Files → Connect → Start → Install → Done`, async ({ page }) => {
    await open(page, { start: "rockusb", platform, step1: s1() }, { stopAt: "prepare" });
    await noTime(page);
    await expect(page.locator("#steps li")).toHaveText(["1 Prepare", "2 Files", "3 Connect", "4 Start", "5 Install", "6 System", "7 Finish"]);
    // R24: the Crestron web page items only for the userdebug goal
    await expect(page.locator("#check-crestron")).toBeHidden();
    await page.click("#goal-mainline");
    await expect(page.locator("#check-crestron")).toBeHidden();
    await page.click("#goal-userdebug");
    await expect(page.locator("#check-crestron")).toBeVisible();
    await expect(page.locator("#check-zadig")).toBeVisible({ visible: platform === "Windows" });
    await expect(page.locator("#card-prepare input[type=checkbox]")).toHaveCount(8); // no disk-space box (R23): 3 + 1 + 3 + Zadig
    await expect(page.locator("#steps li")).toHaveText(["1 Prepare", "2 Files", "3 Connect", "4 Start", "5 Install", "6 Done"]);
    await expect(page.locator("#prepare-next")).toBeDisabled();
    for (const c of await page.locator("#card-prepare input.req").all()) if (await c.isVisible()) await c.check();
    await expect(page.locator("#prepare-next")).toBeEnabled();
    await page.click("#prepare-next");
    await card(page, "prepare", "folded");
    await card(page, "files", "open");
    await expect(page.locator("#row-base")).toBeHidden();
    await expect(page.locator("#more")).toBeHidden();
    await expect(body(page)).toHaveAttribute("data-image", "ok");
    await expect(page.locator("#image-status")).toHaveText("Installer: OK");
    await expect(page.locator("#fw-link")).toHaveAttribute("href", "https://crestrondevicefiles.blob.core.windows.net/firmware/pcdfw-1.0.0.94-userdebug.bin");
    await expect(page.locator("#fw-link")).toHaveAttribute("rel", "noopener noreferrer");
    await expect(page.locator("#files-next")).toBeDisabled();
    await page.setInputFiles("#fw-file", { name: "pcdfw-1.0.0.94-userdebug.bin", mimeType: "application/octet-stream", buffer: FW });
    await expect(page.locator("#fw-status")).toHaveText("pcdfw-1.0.0.94-userdebug.bin: OK");
    await noTime(page);
    await page.click("#files-next");
    // Connect: the old download-mode text; the Zadig box only on Windows
    await card(page, "connect", "open");
    await expect(page.locator("#zadig-rk")).toBeVisible({ visible: platform === "Windows" });
    await expect(page.locator("#zadig-rk")).not.toHaveAttribute("open", "");
    await page.click("#connect");
    await phase(page, "download-checked");
    await expect(page.locator("#unit-status")).toHaveText("Unit found: UC-P10.");
    // the details went to the log
    const l = await logText(page);
    expect(l).toContain("download mode OK (UC-P10): slot a boots next, both slots are successful; boot command block: empty (no command set)");
    await noTime(page);
    await page.click("#connect-next");
    await card(page, "start", "open");
    await expect(page.locator("#zadig-fb")).toBeVisible({ visible: platform === "Windows" });
    await page.click("#start");
    await phase(page, "restarted");
    await expect(page.locator("#l-restarting")).toBeVisible();
    await expect(page.locator("#fb-connect")).toBeVisible();
    await noTime(page);
    await page.waitForFunction(() => globalThis.__fake.rec.some((e) => e.dev === "fastboot" && e.op === "attach"));
    await page.click("#connect-fastboot");
    await phase(page, "booted");
    await expect(page.locator("#l-starting")).toHaveText("The installer is starting.");
    // the installer's link: the first time the browser needs one pick
    await expect(page.locator("#x1-connect")).toBeVisible();
    await page.waitForFunction(() => globalThis.__fake.rec.some((e) => e.dev === "step1" && e.op === "attach"));
    await page.click("#connect-step1");
    await expect(body(page)).toHaveAttribute("data-ui", "install");
    await expect.poll(() => logText(page)).toContain("step 1: unit busy plan");
    await expect(body(page)).toHaveAttribute("data-ui", "done");
    await expect(page.locator("#card-done")).toContainText("Done. The unit restarts into Android userdebug. You can disconnect the cable.");
    for (const id of ["prepare", "files", "connect", "start", "install"]) await card(page, id, "folded");
    await noTime(page);
    // what the unit got: the whole firmware file, no key, COMMIT mode official, no part
    const u = await page.evaluate(() => JSON.parse(JSON.stringify(globalThis.__fake.env.step1)));
    expect(u.puts.map((p) => p.name)).toEqual(["firmware.bin"]);
    expect(u.files["firmware.bin"].sha256).toBe(sha(FW));
    expect(u.commits.map((c) => c.body)).toEqual([{ mode: "official" }]);
    // the entry: the reviewed sequence and the image intact
    const r = await rec(page);
    expect(cbws(r)).toEqual(["14:0:34", "14:3666944:8", "14:49156:1", "14:49184:4", "14:49156:1", "14:49184:4", "15:49184:4", "14:49184:4", "14:49156:1", "ff:0:0"]);
    expect(cmds(r).slice(-2)).toEqual([`download:${IMAGE.length.toString(16).padStart(8, "0")}`, "boot"]);
    const dl = await page.evaluate(() => globalThis.__fake.hex(globalThis.__fake.env.downloaded));
    expect(sha(Buffer.from(dl, "hex"))).toBe(sha(IMAGE));
  });

  test(`mainline on ${platform}: the system choice, the base by itself, step 1 over the cable, Install, the system, Finish`, async ({ page }) => {
    const LIST = finishSet();
    await open(page, { start: "rockusb", platform, step1: s1({ after: true }), prov: prov(), finishPin: LIST.sha },
      { goal: "mainline", stopAt: "prepare", step2: true, finish: { lists: [{ label: "test", sha256: LIST.sha }] }, finishSet: LIST.files });
    await page.click("#goal-mainline");
    await expect(page.locator("#steps li")).toHaveText(["1 Prepare", "2 Files", "3 Connect", "4 Start", "5 Install", "6 System", "7 Finish"]);
    await expect(page.locator("#check-crestron")).toBeHidden(); // R24
    await expect(page.locator("#systems")).toBeVisible();
    for (const c of await page.locator("#card-prepare input.req").all()) if (await c.isVisible()) await c.check();
    await expect(page.locator("#prepare-next")).toBeDisabled(); // no system chosen yet
    await page.click("#system-kiosk-ha");
    await expect(page.locator("#prepare-next")).toBeEnabled();
    await page.click("#prepare-next");
    await expect(body(page)).toHaveAttribute("data-base", "ok");
    await expect(page.locator("#base-status")).toHaveText("Mainline Linux: OK");
    await expect(page.locator("#files-head")).toHaveText("The page downloads what it needs. You can go on while it downloads.");
    await page.setInputFiles("#fw-file", { name: "pcdfw-1.0.0.94-userdebug.bin", mimeType: "application/octet-stream", buffer: FW });
    await expect(page.locator("#sshkey-input")).toBeVisible(); // R34: on the Files card, no click
    await page.fill("#sshkey-input", K["ed25519"].input);
    await page.check("#sshkey-confirm");
    await page.click("#files-next");
    await noTime(page);
    await toBooted(page);
    await page.waitForFunction(() => globalThis.__fake.rec.some((e) => e.dev === "step1" && e.op === "attach"));
    await page.click("#connect-step1");
    await expect(page.locator("#l-reading")).toBeVisible();
    await expect(body(page)).toHaveAttribute("data-ui", /install|system|finish/, { timeout: 15000 });
    await expect(body(page)).toHaveAttribute("data-ui", /system|finish/, { timeout: 15000 });
    await expect(page.locator("#p-installs")).toHaveText("The unit installs Kiosk and Home Assistant. Keep the USB-C cable connected.");
    await expect(body(page)).toHaveAttribute("data-ui", "finish", { timeout: 15000 });
    await expect(page.locator("#f-done")).toBeVisible({ timeout: 15000 });
    await expect(page.locator("#f-done")).toHaveText("Done. Mainline Linux is installed. You can disconnect the USB-C cable.");
    // RC-10d: what to do next, and no "Keep the USB-C cable connected." above the done line
    await expect(page.locator("#f-next")).toHaveText("Next: set up the unit on its own screen.");
    await expect(page.locator("#f-keep")).toBeHidden();
    for (const id of ["prepare", "files", "connect", "start", "install", "system"]) await card(page, id, "folded");
    await noTime(page);
    const u = await page.evaluate(() => JSON.parse(JSON.stringify(globalThis.__fake.env.step1)));
    expect(u.puts.map((p) => p.name)).toEqual(["firmware.bin", "SSH-KEY.PUB", "manifest.json", "boot-template.img", "rootfs.ext4.zst"]);
    expect(u.files["rootfs.ext4.zst"].sha256).toBe(sha(ROOTFS_ZST));
    expect(u.files["boot-template.img"].sha256).toBe(sha(TEMPLATE));
    expect(u.files["SSH-KEY.PUB"].text).toBe(K["ed25519"].file);
    expect(u.commits.map((c) => c.body)).toEqual([{ mode: "mainline" }, {}]);
    const p = await page.evaluate(() => JSON.parse(JSON.stringify(globalThis.__fake.env.prov)));
    expect(p.commits).toEqual([{ profile: "kiosk-ha", source: "net" }]);
    expect(p.consent).toEqual({ auto: true, plan_id: "0123456789ab" }); // §6.10: the plan id the unit shows
    expect(p.puts).toEqual([]);
    // no requestDevice after the step-1 pick: the system step and the finish used the same permission
    expect((await rec(page)).filter((e) => e.op === "requestDevice").length).toBe(3);
    const fin = await page.evaluate(() => JSON.parse(JSON.stringify(globalThis.__fake.env.finish)));
    expect(fin.committed).toBeTruthy();
    const l = await logText(page);
    expect(l).toContain("step 1: COMMIT mode mainline");
    expect(l).toContain("system: COMMIT profile kiosk-ha, source net");
    expect(l).toContain("step 2: COMMIT auto true");
  });
}

