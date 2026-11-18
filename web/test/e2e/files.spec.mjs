// SPDX-License-Identifier: GPL-2.0-or-later
// The Files step (DESIGN v3 M2, DESIGN-R23 M2): the Crestron file is checked
// against its pin with a bar; the base comes from this site by itself, its
// manifest pinned first, or from a chosen folder; the ssh key's rules
// (required for Console only). Nothing is sent to a unit in this step.
import { readFileSync } from "node:fs";
import { body, bytes, expect, FW, loadImage, logText, MANIFEST, open, prepare, rec, ROOTFS_ZST, TEMPLATE, test } from "./helpers.mjs";

const K = Object.fromEntries(JSON.parse(readFileSync(new URL("../unit/sshkey-vectors.json", import.meta.url), "utf8")).vectors
  .map((v) => [v.name, v]));

async function noUnit(page) {
  expect((await rec(page)).filter((e) => e.op !== "attach" && e.op !== "requestDevice")).toEqual([]);
}

function file(path, buf) {
  return { name: path.slice(path.lastIndexOf("/") + 1), mimeType: "application/octet-stream", buffer: buf };
}

test("a wrong firmware file: the plain refusal, Next stays off; the right one: OK", async ({ page }) => {
  await open(page, { start: "rockusb" }, { stopAt: "files" });
  const bad = Buffer.from(FW);
  bad[12345] ^= 1;
  await page.setInputFiles("#fw-file", { name: "pcdfw-1.0.0.94-userdebug.bin", mimeType: "application/octet-stream", buffer: bad });
  await expect(page.locator("#fw-status")).toHaveText("This is not the Crestron userdebug firmware file. Choose pcdfw-1.0.0.94-userdebug.bin.");
  await expect(page.locator("#files-next")).toBeDisabled();
  await page.setInputFiles("#fw-file", { name: "x.bin", mimeType: "application/octet-stream", buffer: FW.subarray(1) });
  await expect(page.locator("#fw-status")).toHaveText("This is not the Crestron userdebug firmware file. Choose pcdfw-1.0.0.94-userdebug.bin.");
  await expect(page.locator("#log")).toContainText("bytes, the pin says");
  await page.setInputFiles("#fw-file", { name: "pcdfw-1.0.0.94-userdebug.bin", mimeType: "application/octet-stream", buffer: FW });
  await expect(page.locator("#fw-status")).toHaveText("pcdfw-1.0.0.94-userdebug.bin: OK");
  await expect(page.locator("#files-next")).toBeEnabled();
  await noUnit(page);
});

test("the base: a changed manifest is refused before any other base file is fetched", async ({ page }) => {
  const seen = [];
  page.on("request", (r) => seen.push(new URL(r.url()).pathname));
  const m = Buffer.from(MANIFEST.toString().replace("0.3.0-test-base", "0.3.0-test-bas2"));
  await open(page, { start: "rockusb" }, { goal: "mainline", stopAt: "files", base: { files: { "manifest.json": m } } });
  await expect(page.locator("#base-status")).toHaveText("This folder is not Mainline Linux. Choose the folder uc-pxx-mainline.");
  expect(seen.filter((p) => p.startsWith("/base/"))).toEqual(["/base/uc-pxx-mainline/manifest.json"]);
  await expect(page.locator("#files-next")).toBeDisabled();
});

test("the base: a changed rootfs.ext4.zst is refused", async ({ page }) => {
  const z = Buffer.from(ROOTFS_ZST);
  z[5] ^= 1;
  await open(page, { start: "rockusb" }, { goal: "mainline", stopAt: "files", base: { files: { "rootfs.ext4.zst": z } } });
  await expect(body(page)).toHaveAttribute("data-base", "error");
  await expect(page.locator("#log")).toContainText("rootfs.ext4.zst has sha256");
});

test("the site has no base: the folder on this computer is checked the same way and used", async ({ page }) => {
  await open(page, { start: "rockusb" }, { goal: "mainline", stopAt: "files", base: { missing: true } });
  await expect(page.locator("#base-status")).toHaveText("This site does not have Mainline Linux. Choose the folder uc-pxx-mainline.");
  // a picked folder (an <input webkitdirectory>): Playwright cannot set webkitRelativePath, so the test gives the files
  const pick = (z) => page.evaluate(([m, t, z2]) => {
    const mk = (p, b) => {
      const f = new File([new Uint8Array(b)], p.slice(p.lastIndexOf("/") + 1));
      Object.defineProperty(f, "webkitRelativePath", { value: p });
      return f;
    };
    const el = document.getElementById("base-folder");
    Object.defineProperty(el, "files", { configurable: true, value: [mk("dl/uc-pxx-mainline/manifest.json", m),
      mk("dl/uc-pxx-mainline/boot-template.img", t), mk("dl/uc-pxx-mainline/rootfs.ext4.zst", z2), mk("dl/other/manifest.json", m)] });
    el.dispatchEvent(new Event("change"));
  }, [[...MANIFEST], [...TEMPLATE], [...z]]);
  await pick(bytes(ROOTFS_ZST.length, 99));
  await expect(page.locator("#base-status")).toHaveText("This folder is not Mainline Linux. Choose the folder uc-pxx-mainline.");
  await pick(ROOTFS_ZST);
  await expect(page.locator("#base-status")).toHaveText("Using the folder uc-pxx-mainline from this computer: OK");
  await expect(body(page)).toHaveAttribute("data-base", "ok");
  await noUnit(page);
});

test("Console only: the key is required; a refused key says why; a confirmed key opens Next", async ({ page }) => {
  await open(page, { start: "rockusb" }, { goal: "mainline", system: "console", stopAt: "files" });
  await page.setInputFiles("#fw-file", { name: "pcdfw-1.0.0.94-userdebug.bin", mimeType: "application/octet-stream", buffer: FW });
  await expect(body(page)).toHaveAttribute("data-base", "ok");
  await expect(page.locator("#sshkey-input")).toBeVisible();
  await expect(page.locator("#sshkey-optional")).toBeHidden(); // R34: console needs the key: no "Optional" line
  await expect(page.locator("#sshkey-label")).toHaveText("SSH key");
  await expect(page.locator("#sshkey-status")).toHaveText("Console only needs an SSH key.");
  await expect(page.locator("#files-next")).toBeDisabled();
  await page.fill("#sshkey-input", "ssh-dss AAAAB3NzaC1kc3MAAACBAP test");
  await expect(page.locator("#sshkey-status")).toContainText("This key is refused:");
  await expect(page.locator("#sshkey-status")).toContainText("Correct it, or empty the field to go on without a key.");
  await page.fill("#sshkey-input", K["ed25519"].input);
  await expect(page.locator("#sshkey-status")).toHaveText(`Key: ssh-ed25519, 256 bits, fingerprint ${K["ed25519"].fingerprint}`);
  await expect(page.locator("#sshkey-confirm-text")).toHaveText(`This is my key (fingerprint ${K["ed25519"].fingerprint})`);
  await expect(page.locator("#files-next")).toBeDisabled();
  await page.check("#sshkey-confirm");
  await expect(page.locator("#files-next")).toBeEnabled();
  await noUnit(page);
});

// RC-10d (owner 2026-11-17): the .pub file can be chosen as well as pasted; the same check runs (RSA at least 3072 bits)
test("RC-10d: Choose the .pub file: a good key fills the field and is checked; RSA 2048 and a private key are refused", async ({ page }) => {
  await open(page, { start: "rockusb" }, { goal: "mainline", system: "console", stopAt: "files" });
  await page.setInputFiles("#fw-file", { name: "pcdfw-1.0.0.94-userdebug.bin", mimeType: "application/octet-stream", buffer: FW });
  await expect(page.locator("#sshkey-choose")).toBeEnabled();
  const pub = (name, text) => ({ name, mimeType: "text/plain", buffer: Buffer.from(text) });
  await page.setInputFiles("#sshkey-file", pub("id_rsa.pub", K["rsa-2048"].input));
  await expect(page.locator("#sshkey-status")).toContainText("This key is refused:");
  await expect(page.locator("#files-next")).toBeDisabled();
  await page.setInputFiles("#sshkey-file", pub("id_ed25519", K["private-key"].input));
  await expect(page.locator("#sshkey-status")).toContainText("This is a private key. The page did not read it into the field.");
  await expect(page.locator("#sshkey-input")).toHaveValue("");
  await expect(page.locator("#files-next")).toBeDisabled();
  await page.setInputFiles("#sshkey-file", pub("big.pub", "x".repeat(9000)));
  await expect(page.locator("#sshkey-status")).toHaveText("This file is too large for a public key. Choose the .pub file.");
  await page.setInputFiles("#sshkey-file", pub("id_ed25519.pub", K["ed25519"].input));
  await expect(page.locator("#sshkey-status")).toHaveText(`Key: ssh-ed25519, 256 bits, fingerprint ${K["ed25519"].fingerprint}`);
  await expect(page.locator("#sshkey-input")).toHaveValue(K["ed25519"].input);
  await page.check("#sshkey-confirm");
  await expect(page.locator("#files-next")).toBeEnabled();
  const l = await logText(page);
  expect(l).toContain("ssh key file: read");
  expect(l).not.toContain(K["ed25519"].input.split(" ")[1]);
  expect(l).not.toContain("PRIVATE KEY");
  await noUnit(page);
});

test("Kiosk and Home Assistant: no key is fine; an unconfirmed key keeps Next off", async ({ page }) => {
  await open(page, { start: "rockusb" }, { goal: "mainline", stopAt: "files" });
  await page.setInputFiles("#fw-file", { name: "pcdfw-1.0.0.94-userdebug.bin", mimeType: "application/octet-stream", buffer: FW });
  await expect(page.locator("#files-next")).toBeEnabled();
  await expect(page.locator("#sshkey-input")).toBeVisible(); // R34: on the Files card, no click
  await page.fill("#sshkey-input", K["ed25519"].input);
  await expect(page.locator("#files-next")).toBeDisabled();
  await page.fill("#sshkey-input", "");
  await expect(page.locator("#files-next")).toBeEnabled();
});

test("the userdebug goal has no base row, no system choice and no key field", async ({ page }) => {
  await open(page, { start: "rockusb" }, { stopAt: "files" });
  await expect(page.locator("#row-base")).toBeHidden();
  await expect(page.locator("#more")).toBeHidden();
  await expect(page.locator("#systems")).toBeHidden();
});

test("re-look R-1: the base download fails after Next: the page opens Files again with the error and Try again", async ({ page }) => {
  let hold;
  const gate = new Promise((r) => { hold = r; });
  await open(page, { start: "rockusb" }, { goal: "mainline", stopAt: "prepare" });
  // registered after open(): this route wins over the helper's
  await page.route("**/base/uc-pxx-mainline/rootfs.ext4.zst", async (route) => { await gate; await route.abort("internetdisconnected"); });
  await prepare(page, "mainline");
  await page.setInputFiles("#fw-file", { name: "pcdfw-1.0.0.94-userdebug.bin", mimeType: "application/octet-stream", buffer: FW });
  await expect(page.locator("#files-next")).toBeEnabled(); // the base still downloads
  await page.click("#files-next");
  await expect(body(page)).toHaveAttribute("data-ui", "connect");
  await expect(page.locator("#connect")).toBeDisabled();
  hold();
  await expect(body(page)).toHaveAttribute("data-ui", "files");
  await expect(page.locator("#base-status")).toHaveText("The download stopped. Check the internet connection, then press Try again.");
  await expect(page.locator("#base-again")).toBeEnabled();
  await noUnit(page);
});

const R34 = "Optional: your SSH public key, for logging in over the network. Without one, ssh stays off. You can add it later on the unit's setup page (Access).";
for (const system of ["kiosk-ha", "kiosk"]) {
  test(`R34 (${system}): the SSH key field is on the Files card with its one plain line, no click; no key is fine`, async ({ page }) => {
    await open(page, { start: "rockusb" }, { goal: "mainline", system, stopAt: "files" });
    await page.setInputFiles("#fw-file", { name: "pcdfw-1.0.0.94-userdebug.bin", mimeType: "application/octet-stream", buffer: FW });
    await expect(page.locator("#sshkey-optional")).toBeVisible();
    await expect(page.locator("#sshkey-optional")).toHaveText(R34);
    await expect(page.locator("#sshkey-input")).toBeVisible();
    await expect(page.locator("#sshkey-label")).toHaveText("SSH key (optional)");
    await expect(page.locator("#sshkey-status")).toHaveText("");
    await expect(page.locator("#files-next")).toBeEnabled();
    expect(await page.locator("#more").evaluate((e) => e.tagName)).toBe("DIV"); // not under a "More options" fold
  });
}

test("R34: the userdebug goal shows no SSH key line and no key field", async ({ page }) => {
  await open(page, { start: "rockusb" }, { stopAt: "files" });
  await expect(page.locator("#sshkey-optional")).toBeHidden();
  await expect(page.locator("#sshkey-input")).toBeHidden();
});

// R25 (owner, U4-RC10 run 2026-11-16): next to a greyed Next the page says what is missing, one line per thing, live
const why = (page, id) => page.locator(`#${id}-why p`);
// a box with no line is checked by its hidden attribute (an empty box has no size, so it would count as hidden anyway)

test("R25: Prepare says what is missing next to the greyed Next, line by line, until Next is on", async ({ page }) => {
  await open(page, { start: "rockusb" }, { stopAt: "prepare" });
  await expect(page.locator("#prepare-next")).toBeDisabled();
  await expect(why(page, "prepare")).toHaveText(["Choose what to install.", "Tick each box that does not say Optional."]);
  await page.click("#goal-mainline");
  await expect(why(page, "prepare")).toHaveText(["Choose a system.", "Tick each box that does not say Optional."]);
  await page.click("#system-kiosk");
  await expect(why(page, "prepare")).toHaveText(["Tick each box that does not say Optional."]);
  // an optional box does not count
  for (const c of await page.locator("#card-prepare input.req").all()) if (await c.isVisible()) await c.check();
  await expect(page.locator("#prepare-next")).toBeEnabled();
  await expect(page.locator("#prepare-why")).toHaveAttribute("hidden", "");
  await page.locator("#card-prepare input.req:visible").first().uncheck();
  await expect(why(page, "prepare")).toHaveText(["Tick each box that does not say Optional."]);
});

test("R25: Get the files names the missing firmware file, a wrong one, then nothing once Next is on", async ({ page }) => {
  await open(page, { start: "rockusb" }, { stopAt: "files" });
  await expect(body(page)).toHaveAttribute("data-image", "ok");
  await expect(page.locator("#files-next")).toBeDisabled();
  await expect(why(page, "files")).toHaveText(["Choose the Crestron firmware file to go on."]);
  const bad = Buffer.from(FW);
  bad[7] ^= 1;
  await page.setInputFiles("#fw-file", { name: "pcdfw-1.0.0.94-userdebug.bin", mimeType: "application/octet-stream", buffer: bad });
  await expect(why(page, "files")).toHaveText(["Choose the right Crestron firmware file (pcdfw-1.0.0.94-userdebug.bin)."]);
  await page.setInputFiles("#fw-file", { name: "pcdfw-1.0.0.94-userdebug.bin", mimeType: "application/octet-stream", buffer: FW });
  await expect(page.locator("#files-next")).toBeEnabled();
  await expect(page.locator("#files-why")).toHaveAttribute("hidden", "");
  await noUnit(page);
});

test("R25: Console only: the firmware file and the key are named; an unconfirmed key asks for the tick", async ({ page }) => {
  await open(page, { start: "rockusb" }, { goal: "mainline", system: "console", stopAt: "files" });
  await expect(body(page)).toHaveAttribute("data-base", "ok");
  await expect(why(page, "files")).toHaveText(["Choose the Crestron firmware file to go on.", "Paste your SSH key. Console only needs one."]);
  await page.setInputFiles("#fw-file", { name: "pcdfw-1.0.0.94-userdebug.bin", mimeType: "application/octet-stream", buffer: FW });
  await expect(why(page, "files")).toHaveText(["Paste your SSH key. Console only needs one."]);
  await page.fill("#sshkey-input", "ssh-dss AAAAB3NzaC1kc3MAAACBAP test");
  await expect(why(page, "files")).toHaveText(["Correct the SSH key."]);
  await page.fill("#sshkey-input", K["ed25519"].input);
  await expect(why(page, "files")).toHaveText(["Tick the box under the key to confirm that it is your key."]);
  await page.check("#sshkey-confirm");
  await expect(page.locator("#files-next")).toBeEnabled();
  await expect(page.locator("#files-why")).toHaveAttribute("hidden", "");
});

test("R25: Kiosk: a refused optional key says to correct or empty it; the site has no installer: the hand way", async ({ page }) => {
  await open(page, { start: "rockusb" }, { goal: "mainline", stopAt: "files", noSiteImage: true });
  await expect(body(page)).toHaveAttribute("data-image", "error");
  await page.setInputFiles("#fw-file", { name: "pcdfw-1.0.0.94-userdebug.bin", mimeType: "application/octet-stream", buffer: FW });
  await expect(why(page, "files")).toHaveText(["Get the installer: download it from GitHub, then choose the file."]);
  await page.fill("#sshkey-input", "ssh-dss AAAAB3NzaC1kc3MAAACBAP test");
  await expect(why(page, "files")).toHaveText(["Get the installer: download it from GitHub, then choose the file.", "Correct the SSH key, or empty the field."]);
  await page.fill("#sshkey-input", "");
  await loadImage(page);
  await expect(page.locator("#files-next")).toBeEnabled();
  await expect(page.locator("#files-why")).toHaveAttribute("hidden", "");
});

test("R25: Connect is greyed while Mainline Linux still downloads, and says so; then it is on", async ({ page }) => {
  let hold;
  const gate = new Promise((r) => { hold = r; });
  await open(page, { start: "rockusb" }, { goal: "mainline", stopAt: "prepare" });
  await page.route("**/base/uc-pxx-mainline/rootfs.ext4.zst", async (route) => {
    await gate;
    await route.fulfill({ status: 200, contentType: "application/octet-stream", body: ROOTFS_ZST });
  });
  await prepare(page, "mainline");
  await page.setInputFiles("#fw-file", { name: "pcdfw-1.0.0.94-userdebug.bin", mimeType: "application/octet-stream", buffer: FW });
  await expect(page.locator("#files-next")).toBeEnabled();
  await expect(page.locator("#files-why")).toHaveAttribute("hidden", ""); // the download may still run at Next
  await page.click("#files-next");
  await expect(page.locator("#connect")).toBeDisabled();
  await expect(why(page, "connect")).toHaveText(["Wait while the page downloads Mainline Linux."]);
  hold();
  await expect(page.locator("#connect")).toBeEnabled();
  await expect(page.locator("#connect-why")).toHaveAttribute("hidden", "");
  await noUnit(page);
});
