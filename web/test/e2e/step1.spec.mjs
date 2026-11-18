// SPDX-License-Identifier: GPL-2.0-or-later
// Step 1 over the cable (I-6) with the fake RAM installer: a cut and Send
// again, the drive offer after the second failure, the drive fallback, the
// HELLO checks (the token, the firmware pin), the unit's own outcomes, and a
// link lost during the install.
import { readFileSync } from "node:fs";
import { body, expect, FW, logText, MANIFEST, open, rec, refused, ROOTFS_ZST, s1, sha, TEMPLATE, test, toBooted } from "./helpers.mjs";

const K = Object.fromEntries(JSON.parse(readFileSync(new URL("../unit/sshkey-vectors.json", import.meta.url), "utf8")).vectors
  .map((v) => [v.name, v]));

async function toStep1(page, step1, opts = {}) {
  await open(page, { start: "rockusb", step1, ...(opts.sc || {}) }, opts);
  await toBooted(page);
  await page.waitForFunction(() => globalThis.__fake.rec.some((e) => e.dev === "step1" && e.op === "attach"));
  await page.click("#connect-step1");
}

const u1 = (page) => page.evaluate(() => JSON.parse(JSON.stringify(globalThis.__fake.env.step1)));

test("a cut in the firmware file: the transfer text, Send again starts again from HELLO, the install goes on", async ({ page }) => {
  await toStep1(page, s1({ fault: "cutFirmware" }));
  await expect(page.locator("#s1-error")).toBeVisible();
  await expect(page.locator("#s1-error")).toContainText("The transfer stopped. Nothing was written to the unit. Check the cable, then press Send again.");
  await expect(page.locator("#drive-offer")).toBeHidden(); // only after a second failure
  expect((await u1(page)).commits).toEqual([]);
  await page.click("#send-again");
  await expect(body(page)).toHaveAttribute("data-ui", "done", { timeout: 10000 });
  const u = await u1(page);
  expect(u.commits.map((c) => c.body)).toEqual([{ mode: "official" }]);
  expect(u.puts.filter((p) => p.name === "firmware.bin")).toHaveLength(2);
  // review NIT-4: Send again is a new start: its HELLO is logged again, though it is the same as the first
  expect((await logText(page)).split("step 1: unit interface 1d6b:0105, mode step1, state waiting-firmware,").length - 1).toBe(2);
});

test("two failed sends: the drive is offered; the drive fallback copies the firmware file and reads it back", async ({ page }) => {
  await page.addInitScript(() => {
    const files = new Map();
    const dir = {
      kind: "directory", name: "UC-PXX-INST",
      async *entries() { for (const k of files.keys()) yield [k, {}]; },
      async getDirectoryHandle() { throw new DOMException("x", "NotFoundError"); },
      async getFileHandle(name, o) {
        if (!files.has(name)) {
          if (!(o && o.create)) throw new DOMException("x", "NotFoundError");
          files.set(name, new Uint8Array(0));
        }
        return {
          async createWritable() {
            const ch = [];
            return { async write(v) { ch.push(new Uint8Array(v)); }, async close() { files.set(name, new Uint8Array(await new Blob(ch).arrayBuffer())); }, async abort() {} };
          },
          async getFile() { return new File([files.get(name)], name); },
        };
      },
    };
    globalThis.__drive = { names: () => [...files.keys()].sort(), size: (n) => files.get(n).length };
    window.showDirectoryPicker = async () => dir;
  });
  await toStep1(page, s1({ fault: "cutAlways" }), { sc: { platform: "Windows" } });
  await expect(page.locator("#s1-error")).toBeVisible();
  await page.click("#send-again");
  await expect(page.locator("#drive-offer")).toBeVisible({ timeout: 10000 });
  await page.click("#use-drive");
  await expect(page.locator("#drive")).toBeVisible();
  await expect.poll(() => logText(page)).toContain("step 1: DRIVE taken");
  await page.click("#drive-pick");
  await expect(page.locator("#d-eject1")).toHaveText("Done. Now eject the drive on this computer:");
  await expect(page.locator("#d-eject-how")).toHaveText("In File Explorer, right-click UC-PXX-INST and choose Eject.");
  expect(await page.evaluate(() => globalThis.__drive.names())).toEqual(["pcdfw-1.0.0.94-userdebug.bin"]);
  expect(await page.evaluate(() => globalThis.__drive.size("pcdfw-1.0.0.94-userdebug.bin"))).toBe(FW.length);
  // the unit reads the drive: the page reads its phase from the USB link
  await page.evaluate(() => { globalThis.__fake.env.step1.drivePhase = "reading"; });
  await expect(page.locator("#d-reading")).toHaveText("The unit reads the firmware file. Keep this page open.");
  const l = await logText(page);
  expect(l).toContain("drive: firmware copy read back OK");
});

test("a unit that runs another installer (token): the page stops, keep the unit powered; nothing is sent", async ({ page }) => {
  await toStep1(page, s1({ fault: "otherToken" }));
  await refused(page, "step1-other");
  await expect(page.locator("#stop-title")).toHaveText("The page stopped. Keep the unit connected and powered, and do not restart it.");
  await expect(page.locator("#stop-why")).toHaveText("Why: the unit runs another installer than the one the page started.");
  expect((await u1(page)).puts).toEqual([]);
});

test("the unit does not pin this firmware file: nothing is sent, the transfer text and Send again", async ({ page }) => {
  await toStep1(page, s1({ fault: "noFwPin" }));
  await expect(page.locator("#s1-error")).toBeVisible();
  expect((await u1(page)).puts).toEqual([]);
  await expect(page.locator("#log")).toContainText("the unit does not take this firmware file");
});

for (const [code, line, text] of [
  ["canceled", "#i-canceled", "The install was canceled on the unit. Nothing was written."],
  ["refused", "#i-refused", "The unit did not install. Nothing was written. Save the log and ask for help."],
  ["hold", "#i-stop", "KEEP THE UNIT POWERED. Do not restart it. Save the log and ask for help."],
]) {
  test(`the unit's install ends "${code}": the Install card says so`, async ({ page }) => {
    await toStep1(page, s1({ flow: ["plan", code] }));
    await expect(page.locator(line)).toHaveText(text, { timeout: 10000 });
    await expect(body(page)).toHaveAttribute("data-ui", "install");
    // canceled and refused: nothing was written, the page keeps following the
    // unit (it may ask for the files again, review MINOR-A); hold is final
    await expect(body(page)).toHaveAttribute("data-step1", code === "hold" ? "done" : "install");
  });
}

// review MINOR-A (record §6.6): the flow asks for the files again after it left
// the files screen; the sealed files are gone: the page sends them again
for (const [what, flow, again] of [
  ["canceled on the unit", ["plan", "canceled"], ["canceled"]],
  ["a refused plan", ["plan", "refused"], ["refused"]],
  ["a switch from the unit's menu", ["switch-running", "switch-running", "safe"], ["safe"]],
]) {
  test(`review MINOR-A: after ${what} the unit asks for the files again: the page sends them again and the install goes on`, async ({ page }) => {
    await toStep1(page, s1({ flow, againAfter: again, flow2: ["plan", "install", "install", "done"] }));
    await expect(body(page)).toHaveAttribute("data-ui", "done", { timeout: 15000 });
    const u = await u1(page);
    expect(u.puts.filter((p) => p.name === "firmware.bin" && p.accept)).toHaveLength(2);
    expect(u.commits.map((c) => c.body)).toEqual([{ mode: "official" }, { mode: "official" }]);
    await expect(page.locator("#log")).toContainText("step 1: the unit asks for the files again: the page sends them again");
    await expect(body(page)).not.toHaveAttribute("data-refusal", /./);
  });
}

test("review MINOR-A: after an install ran the page never sends the files again", async ({ page }) => {
  await toStep1(page, s1({ flow: ["plan", "install", "refused"], againAfter: ["refused"], flow2: ["plan"] }));
  await expect(page.locator("#i-refused")).toBeVisible({ timeout: 10000 });
  await expect.poll(async () => (await u1(page)).stage, { timeout: 10000 }).toBe("firmware");
  await page.waitForTimeout(1500);
  const u = await u1(page);
  expect(u.puts.filter((p) => p.name === "firmware.bin")).toHaveLength(1);
  expect(u.commits).toHaveLength(1);
});

test("review MINOR-A: the mainline goal: after a refused plan the page sends the firmware and the base again", async ({ page }) => {
  await toStep1(page, s1({ flow: ["plan", "refused"], againAfter: ["refused"], flow2: ["plan", "install", "done"] }), { goal: "mainline" });
  await expect(body(page)).toHaveAttribute("data-ui", /system/, { timeout: 20000 });
  const u = await u1(page);
  expect(u.commits.map((c) => c.body)).toEqual([{ mode: "mainline" }, {}, { mode: "mainline" }, {}]);
  expect(u.puts.filter((p) => p.name === "rootfs.ext4.zst" && p.accept)).toHaveLength(2);
});

test("the link goes away during the install: the line about the unit's screen", async ({ page }) => {
  await toStep1(page, s1({ fault: "lostInInstall" }));
  await expect(page.locator("#i-lost")).toHaveText(
    "If the unit's screen says Stopped or Keep the unit powered: do not disconnect the power. Save the log and ask for help.", { timeout: 10000 });
});

// RC-10d (unit-6 run): the normal end of a USB session (a failed transfer, then the device goes away at once) is one
// info line in the log, not a link error; the install goes on
test("RC-10d: the unit ends the USB session at a phase change: one info line, no link error, the install goes on", async ({ page }) => {
  await toStep1(page, s1({ fault: "endInInstall" }));
  await expect(body(page)).toHaveAttribute("data-ui", "done", { timeout: 15000 });
  await expect(page.locator("#i-done")).not.toHaveAttribute("hidden");
  const l = await logText(page);
  expect(l).toMatch(/step 1: the unit ended the USB session \((NetworkError|TransferError): .+ at \d\d:\d\d:\d\d\.\d{3}, then the device went away: normal at a phase change\)/);
  expect(l).not.toMatch(/step 1: link: /);
});

test("the mainline goal: a cut in the base: Send again sends the base again (not the firmware)", async ({ page }) => {
  await toStep1(page, s1({ fault: "cutBase" }), { goal: "mainline" });
  await expect(page.locator("#s1-error")).toBeVisible({ timeout: 10000 });
  await page.click("#send-again");
  await expect(body(page)).toHaveAttribute("data-ui", /install|system/, { timeout: 10000 });
  const u = await u1(page);
  expect(u.puts.filter((p) => p.name === "firmware.bin")).toHaveLength(1);
  expect(u.puts.filter((p) => p.name === "rootfs.ext4.zst")).toHaveLength(2);
  expect(u.commits.map((c) => c.body)).toEqual([{ mode: "mainline" }, {}]);
});

test("the userdebug goal sends only the firmware file (no key, no base)", async ({ page }) => {
  await toStep1(page, s1());
  await expect(body(page)).toHaveAttribute("data-ui", "done", { timeout: 10000 });
  const u = await u1(page);
  expect(u.puts.map((p) => p.name)).toEqual(["firmware.bin"]);
  expect((await rec(page)).filter((e) => e.op === "requestDevice")).toHaveLength(3);
});

test("review MAJOR-1: the first HELLOs have no token yet: the page waits, no stop, and goes on", async ({ page }) => {
  await toStep1(page, s1({ noTokenHellos: 3 }));
  await expect(body(page)).toHaveAttribute("data-ui", "done", { timeout: 10000 });
  await expect(body(page)).not.toHaveAttribute("data-refusal", /./);
  await expect(page.locator("#log")).toContainText("the unit does not name its installer yet: the page asks again");
  // review NIT-4: the waits are logged once (one HELLO line, one wait line), not for each tick
  const l = await logText(page);
  expect(l.split("the unit does not name its installer yet: the page asks again").length - 1).toBe(1);
  expect(l.split(", token -, ").length - 1).toBe(1);
});

async function fakeDrive(page) {
  await page.addInitScript(() => {
    const files = new Map();
    window.showDirectoryPicker = async () => ({
      kind: "directory", name: "UC-PXX-INST",
      async *entries() { for (const k of files.keys()) yield [k, {}]; },
      async getFileHandle(name, o) {
        if (!files.has(name)) { if (!(o && o.create)) throw new DOMException("x", "NotFoundError"); files.set(name, new Uint8Array(0)); }
        return {
          async createWritable() {
            const ch = [];
            return { async write(v) { ch.push(new Uint8Array(v)); }, async close() { files.set(name, new Uint8Array(await new Blob(ch).arrayBuffer())); }, async abort() {} };
          },
          async getFile() { return new File([files.get(name)], name); },
        };
      },
    });
  });
}

test("review MAJOR-2: after the drive fallback the page follows the unit's plan, install and Done", async ({ page }) => {
  await fakeDrive(page);
  await toStep1(page, s1({ fault: "cutAlways", driveGoesOn: true }));
  await page.click("#send-again");
  await expect(page.locator("#drive-offer")).toBeVisible({ timeout: 10000 });
  await page.click("#use-drive");
  await expect.poll(() => logText(page), { timeout: 10000 }).toContain("step 1: DRIVE taken");
  await page.click("#drive-pick");
  await expect(page.locator("#d-eject1")).toBeVisible();
  await page.evaluate(() => { globalThis.__fake.env.step1.drivePhase = "reading"; });
  await expect.poll(() => logText(page), { timeout: 10000 }).toContain("step 1: unit drive plan");
  await expect(body(page)).toHaveAttribute("data-ui", "done", { timeout: 10000 });
});

test("review MINOR-4: a refused DRIVE frame: back to the transfer text", async ({ page }) => {
  await toStep1(page, s1({ fault: "cutAlways" }));
  await page.click("#send-again");
  await expect(page.locator("#drive-offer")).toBeVisible({ timeout: 10000 });
  await page.evaluate(() => { globalThis.__fake.env.sc.step1.fault = "refuseDrive"; });
  await page.click("#use-drive");
  await expect.poll(() => logText(page), { timeout: 10000 }).toContain("step 1: DRIVE refused");
  await expect(page.locator("#drive")).toBeHidden();
  await expect(page.locator("#s1-error")).toBeVisible();
});

// fakeDriveTree: a drive with folders (the File System Access API's shape as
// drivecopy.js uses it). __drive.tree() lists it ({name: size | {…}}),
// __drive.sha(path) hashes one file, __driveSwap() empties it (the unit's RAM
// drive comes back empty after the stash). pre: the host's own entries at the
// top ({name: "dir" | "file"}). __driveStale(): the handle the page has is gone
// (a new drive): every call on it throws until the page picks the drive again.
async function fakeDriveTree(page, pre = {}) {
  await page.addInitScript((pre) => {
    let gen = 0, picked = 0;
    const live = () => { if (picked !== gen) throw new DOMException("x", "NotFoundError"); };
    function mkdir(name) {
      const files = new Map();
      return {
        kind: "directory", name, _files: files,
        async *entries() { live(); for (const [k, v] of files) yield [k, v]; },
        async getDirectoryHandle(n, o) {
          live();
          if (!files.has(n)) { if (!(o && o.create)) throw new DOMException("x", "NotFoundError"); files.set(n, mkdir(n)); }
          const x = files.get(n);
          if (x.kind !== "directory") throw new DOMException("x", "TypeMismatchError");
          return x;
        },
        async getFileHandle(n, o) {
          live();
          if (!files.has(n)) { if (!(o && o.create)) throw new DOMException("x", "NotFoundError"); files.set(n, { kind: "file", data: new Uint8Array(0) }); }
          const f = files.get(n);
          if (f.kind !== "file") throw new DOMException("x", "TypeMismatchError");
          return {
            kind: "file", name: n,
            async createWritable() {
              const ch = [];
              return { async write(v) { ch.push(typeof v === "string" ? new TextEncoder().encode(v) : new Uint8Array(v)); },
                async close() { f.data = new Uint8Array(await new Blob(ch).arrayBuffer()); }, async abort() {} };
            },
            async getFile() { return new File([f.data], n); },
          };
        },
      };
    }
    const root = mkdir("UC-PXX-INST");
    for (const [n, k] of Object.entries(pre)) root._files.set(n, k === "dir" ? mkdir(n) : { kind: "file", data: new Uint8Array(3) });
    const tree = (d) => Object.fromEntries([...d._files].map(([k, v]) => [k, v.kind === "directory" ? tree(v) : v.data.length]));
    const at = (path) => path.reduce((d, n) => d._files.get(n), root);
    globalThis.__drive = {
      tree: () => tree(root),
      sha: async (path) => [...new Uint8Array(await crypto.subtle.digest("SHA-256", at(path).data))].map((x) => x.toString(16).padStart(2, "0")).join(""),
    };
    globalThis.__driveSwap = () => root._files.clear();
    globalThis.__driveStale = () => { gen++; };
    window.showDirectoryPicker = async () => { picked = gen; return root; };
  }, pre);
}
const tree = (page) => page.evaluate(() => globalThis.__drive.tree());
const driveSha = (page, path) => page.evaluate((p) => globalThis.__drive.sha(p), path);
const unitReads = (page) => page.evaluate(() => { globalThis.__fake.env.step1.drivePhase = "reading"; });
const ML = { "manifest.json": MANIFEST.length, "boot-template.img": TEMPLATE.length, "rootfs.ext4.zst": ROOTFS_ZST.length };

async function toDrive(page, step1, opts) {
  await toStep1(page, step1, opts);
  await expect(page.locator("#s1-error")).toBeVisible({ timeout: 10000 });
  await page.click("#send-again");
  await expect(page.locator("#drive-offer")).toBeVisible({ timeout: 10000 });
  await page.click("#use-drive");
  await expect.poll(() => logText(page), { timeout: 10000 }).toContain("step 1: DRIVE taken");
}

async function checkFolder(page) {
  expect(await tree(page)).toEqual({ "uc-pxx-mainline": ML });
  expect(await driveSha(page, ["uc-pxx-mainline", "manifest.json"])).toBe(sha(MANIFEST));
  expect(await driveSha(page, ["uc-pxx-mainline", "boot-template.img"])).toBe(sha(TEMPLATE));
  expect(await driveSha(page, ["uc-pxx-mainline", "rootfs.ext4.zst"])).toBe(sha(ROOTFS_ZST));
}

test("mainline drive fallback (record §6.9): phase 1 the firmware, the key and the empty folder; the stash; phase 2 the folder by itself; the install goes on", async ({ page }) => {
  await fakeDriveTree(page);
  await toDrive(page, s1({ fault: "cutAlways", driveMainline: true }), { goal: "mainline", key: K.ed25519.input, sc: { platform: "Windows" } });
  await page.click("#drive-pick");
  await expect(page.locator("#d-eject1")).toBeVisible({ timeout: 10000 });
  await expect(page.locator("#d-eject1")).toHaveText("Done. Now eject the drive on this computer:");
  await expect(page.locator("#d-eject-how")).toHaveText("In File Explorer, right-click UC-PXX-INST and choose Eject.");
  expect(await tree(page)).toEqual({ "pcdfw-1.0.0.94-userdebug.bin": FW.length, "SSH-KEY.PUB": K.ed25519.file.length, "uc-pxx-mainline": {} });
  expect(await driveSha(page, ["pcdfw-1.0.0.94-userdebug.bin"])).toBe(sha(FW));
  // the eject: the unit reads the drive, its stash runs, the drive comes back empty
  await unitReads(page);
  await expect(page.locator("#d-reading")).toBeVisible({ timeout: 10000 });
  await expect(page.locator("#d-reading")).toHaveText("The unit reads the firmware file. Keep this page open.");
  // phase 2 by itself when the unit asks for the folder (no button, R13)
  await expect(page.locator("#d-eject2")).toBeVisible({ timeout: 10000 });
  await expect(page.locator("#d-eject2")).toHaveText("Done. Eject the drive again:");
  await expect(page.locator("#d-manual-part")).toBeHidden();
  await checkFolder(page);
  await unitReads(page);
  await expect(body(page)).toHaveAttribute("data-ui", /install|system/, { timeout: 10000 });
  await expect(body(page)).toHaveAttribute("data-ui", "system", { timeout: 10000 });
  const l = await logText(page);
  expect(l).toContain("drive: folder uc-pxx-mainline made, 0 entries");
  // review MINOR-E: the folder and the key before the .bin (an early eject never looks like an official drive)
  expect(l.indexOf("drive: folder uc-pxx-mainline made")).toBeLessThan(l.indexOf("drive: SSH-KEY.PUB written"));
  expect(l.indexOf("drive: SSH-KEY.PUB written")).toBeLessThan(l.indexOf("drive: firmware copy read back OK"));
  expect(l).toContain("drive: rootfs.ext4.zst copy read back OK");
  expect(l).toContain("step 1: unit drive check");
  const u = await u1(page);
  expect(u.commits).toEqual([]); // the drive mode: no COMMIT over the link
  expect(u.files).toEqual({}); // the cut sends: the unit took no file over the link
});

test("mainline drive fallback after the firmware went over the cable: the unit asks for the folder: phase 2 only", async ({ page }) => {
  await fakeDriveTree(page);
  await toDrive(page, s1({ fault: "cutBaseAlways", driveMainline: true }), { goal: "mainline" });
  await page.click("#drive-pick");
  await expect(page.locator("#d-eject1")).toBeVisible({ timeout: 10000 });
  await expect(page.locator("#d-eject2")).toBeHidden();
  await checkFolder(page);
  const u = await u1(page);
  expect(u.commits.map((c) => c.body)).toEqual([{ mode: "mainline" }]);
  await unitReads(page);
  await expect(body(page)).toHaveAttribute("data-ui", "system", { timeout: 10000 });
});

test("mainline drive fallback without the link after the stash: the button Copy mainline Linux", async ({ page }) => {
  await fakeDriveTree(page);
  await toDrive(page, s1({ fault: "cutAlways", driveMainline: true, driveLinkGone: true }), { goal: "mainline" });
  await page.click("#drive-pick");
  await expect(page.locator("#d-eject1")).toBeVisible();
  await unitReads(page);
  await expect(page.locator("#d-manual-part")).toBeVisible({ timeout: 10000 });
  await expect(page.locator("#d-manual-part")).toContainText('When the unit\'s screen says "Mainline Linux, step 2 of 2: the folder", press Copy mainline Linux.');
  expect(await tree(page)).toEqual({});
  await page.click("#drive-copy-part");
  await expect(page.locator("#d-eject2")).toBeVisible({ timeout: 10000 });
  await checkFolder(page);
});

test("mainline drive fallback in a browser that cannot write to the drive: the cable line, no hand-copy list", async ({ page }) => {
  await page.addInitScript(() => { window.showDirectoryPicker = undefined; });
  await toDrive(page, s1({ fault: "cutAlways", driveMainline: true }), { goal: "mainline" });
  await page.click("#drive-pick");
  await expect(page.locator("#drive-mainline")).toBeVisible();
  await expect(page.locator("#drive-mainline")).toHaveText(
    "This browser cannot write to the drive. Mainline Linux needs the USB cable here: restart the unit in download mode, then load this page again.");
  await expect(page.locator("#d-hand")).toBeHidden();
});

test("review MINOR-D: the unit gives phase 1 back as it was: the person chooses the drive again, the page copies over it and goes on", async ({ page }) => {
  await fakeDriveTree(page);
  await toDrive(page, s1({ fault: "cutAlways", driveGoesOn: true, driveGiveBack: 1 }), {});
  await page.click("#drive-pick");
  await expect(page.locator("#d-eject1")).toBeVisible({ timeout: 10000 });
  await unitReads(page);
  await expect.poll(() => logText(page), { timeout: 10000 }).toContain("drive: the unit gave the drive back after it read phase 1");
  await expect(page.locator("#drive-pick-row")).toBeVisible();
  await expect(page.locator("#d-eject1")).toBeHidden();
  await page.click("#drive-pick");
  await expect(page.locator("#d-eject1")).toBeVisible({ timeout: 10000 });
  expect(await tree(page)).toEqual({ "pcdfw-1.0.0.94-userdebug.bin": FW.length });
  await unitReads(page);
  await expect(body(page)).toHaveAttribute("data-ui", "done", { timeout: 10000 });
  const l = await logText(page);
  expect(l.match(/drive: firmware copy read back OK/g)).toHaveLength(2);
});

test("review MINOR-D: phase 1 over the cable, the folder given back as a new empty drive (drive: waiting-firmware): the page copies it again by itself", async ({ page }) => {
  await fakeDriveTree(page);
  await toDrive(page, s1({ fault: "cutBaseAlways", driveMainline: true, driveGiveBack: 1, giveBackRemake: true }), { goal: "mainline" });
  await page.click("#drive-pick");
  await expect(page.locator("#d-eject1")).toBeVisible({ timeout: 10000 });
  await checkFolder(page);
  await unitReads(page);
  await expect.poll(() => logText(page), { timeout: 10000 }).toContain("drive: the unit gave the drive back after it read phase 2");
  await expect.poll(async () => (await logText(page)).match(/drive: rootfs\.ext4\.zst copy read back OK/g)?.length || 0, { timeout: 10000 }).toBe(2);
  await checkFolder(page);
  await unitReads(page);
  await expect(body(page)).toHaveAttribute("data-ui", "system", { timeout: 10000 });
});

test("review of d2420c2 MINOR-1: phase 1 by the drive, the folder given back as it was (the unit says drive: waiting-firmware): the folder again, not phase 1", async ({ page }) => {
  await fakeDriveTree(page);
  await toDrive(page, s1({ fault: "cutAlways", driveMainline: true, driveGiveBack: 1, giveBackAfterSwap: true }), { goal: "mainline" });
  await page.click("#drive-pick");
  await expect(page.locator("#d-eject1")).toBeVisible({ timeout: 10000 });
  await unitReads(page);
  await expect(page.locator("#d-eject2")).toBeVisible({ timeout: 10000 });
  await checkFolder(page);
  await unitReads(page);
  await expect.poll(() => logText(page), { timeout: 10000 }).toContain("drive: the unit gave the drive back after it read phase 2");
  await expect.poll(async () => (await logText(page)).match(/drive: rootfs\.ext4\.zst copy read back OK/g)?.length || 0, { timeout: 10000 }).toBe(2);
  await checkFolder(page);
  const l = await logText(page);
  expect(l).not.toContain("phase 1: ");
  expect(l).not.toContain("after it read phase 1");
  expect(l).toContain("drive: folder uc-pxx-mainline opened again");
  await unitReads(page);
  await expect(body(page)).toHaveAttribute("data-ui", "system", { timeout: 10000 });
});

test("review NIT-5: an old drive handle after the swap: logged once, the person chooses the drive again, phase 2 goes on", async ({ page }) => {
  await fakeDriveTree(page);
  await toDrive(page, s1({ fault: "cutAlways", driveMainline: true }), { goal: "mainline" });
  await page.click("#drive-pick");
  await expect(page.locator("#d-eject1")).toBeVisible({ timeout: 10000 });
  await page.evaluate(() => globalThis.__driveStale());
  await unitReads(page);
  await expect(page.locator("#drive-pick-row")).toBeVisible({ timeout: 10000 });
  await page.waitForTimeout(1000); // many ticks of "drive: waiting-bundle"
  expect((await logText(page)).match(/the old drive handle/g)).toHaveLength(1);
  await page.click("#drive-pick");
  await expect(page.locator("#d-eject2")).toBeVisible({ timeout: 10000 });
  await checkFolder(page);
});

test("review MINOR-F: the macOS entries at the top of the drive (and AppleDouble files) are not content: phase 1 and 2 go on", async ({ page }) => {
  await fakeDriveTree(page, { ".fseventsd": "dir", ".Spotlight-V100": "dir", ".Trashes": "dir", "._pcdfw-1.0.0.94-userdebug.bin": "file" });
  await toDrive(page, s1({ fault: "cutAlways", driveMainline: true }), { goal: "mainline", sc: { platform: "macOS" } });
  await page.click("#drive-pick");
  await expect(page.locator("#d-eject1")).toBeVisible({ timeout: 10000 });
  await expect(page.locator("#d-eject-how")).toHaveText("In Finder, press the eject button next to UC-PXX-INST.");
  const t = await tree(page);
  expect(t["pcdfw-1.0.0.94-userdebug.bin"]).toBe(FW.length);
  expect(t["uc-pxx-mainline"]).toEqual({});
  await unitReads(page);
  await expect(page.locator("#d-eject2")).toBeVisible({ timeout: 10000 });
  await checkFolder(page);
});

test("review MINOR-F: real content on the drive is still refused", async ({ page }) => {
  await fakeDriveTree(page, { "notes.txt": "file" });
  await toDrive(page, s1({ fault: "cutAlways" }), {});
  await page.click("#drive-pick");
  await expect(page.locator("#drive-status")).toHaveText("This is not an empty drive UC-PXX-INST. Choose the drive UC-PXX-INST.");
  expect(await tree(page)).toEqual({ "notes.txt": 3 });
});

// review MINOR-F (record §6.6): the step-1 service did not start; the unit
// offers only its drive: no transfer link ever comes
async function toNoService(page, opts = {}) {
  await open(page, { start: "rockusb", step1: s1({ serviceFailed: true }), ...(opts.sc || {}) }, opts);
  await toBooted(page);
  await expect(page.locator("#x1-connect")).toBeVisible({ timeout: 10000 });
  await expect(page.locator("#use-drive-wait")).toHaveText("Use the drive instead");
  await page.click("#use-drive-wait");
  await expect(page.locator("#drive")).toBeVisible();
  await expect(page.locator("#x1-connect")).toBeHidden();
}

test("review MINOR-F: the step-1 service did not start: Use the drive instead, the firmware file to the drive", async ({ page }) => {
  await fakeDriveTree(page);
  await toNoService(page);
  await page.click("#drive-pick");
  await expect(page.locator("#d-eject1")).toBeVisible({ timeout: 10000 });
  expect(await tree(page)).toEqual({ "pcdfw-1.0.0.94-userdebug.bin": FW.length });
  expect(await driveSha(page, ["pcdfw-1.0.0.94-userdebug.bin"])).toBe(sha(FW));
  await expect(page.locator("#log")).toContainText("no link for DRIVE now");
});

test("review MINOR-F: the step-1 service did not start, mainline: phase 1, then Copy mainline Linux for the folder", async ({ page }) => {
  await fakeDriveTree(page);
  await toNoService(page, { goal: "mainline" });
  await page.click("#drive-pick");
  await expect(page.locator("#d-eject1")).toBeVisible({ timeout: 10000 });
  expect(await tree(page)).toEqual({ "pcdfw-1.0.0.94-userdebug.bin": FW.length, "uc-pxx-mainline": {} });
  await expect(page.locator("#d-manual-part")).toBeVisible({ timeout: 10000 });
  await page.evaluate(() => globalThis.__driveSwap()); // the unit's stash ran: its drive comes back empty
  await page.click("#drive-copy-part");
  await expect(page.locator("#d-eject2")).toBeVisible({ timeout: 10000 });
  await checkFolder(page);
});

test("the closed phase words (record §6.6): a switch or repair that writes: do not disconnect", async ({ page }) => {
  await toStep1(page, s1({ flow: Array(8).fill("switch-running") }));
  await expect(page.locator("#i-writing")).toBeVisible({ timeout: 10000 });
  await expect(page.locator("#i-writing")).toHaveText("The unit is working. Do not disconnect the power.");
  await expect(body(page)).toHaveAttribute("data-ui", "install");
});

test("the closed phase words: another screen of the unit, and a word outside the set: the page waits and logs", async ({ page }) => {
  await toStep1(page, s1({ flow: ["status", "status", "switch", "drive: checking", "drive: checking", "drive: checking"] }));
  await expect.poll(() => logText(page), { timeout: 10000 }).toContain("step 1: unit busy drive: checking");
  const l = await logText(page);
  expect(l).toContain("step 1: unit busy status");
  expect(l).toContain("step 1: unit busy switch");
  await expect(body(page)).toHaveAttribute("data-ui", "start");
  await expect(body(page)).toHaveAttribute("data-step1", "wait");
  await expect(body(page)).not.toHaveAttribute("data-refusal", /./);
  for (const id of ["#i-plan", "#i-installing", "#i-writing", "#i-done", "#s1-error", "#drive"]) await expect(page.locator(id)).toBeHidden();
});

test("review MINOR-6: the unit asks a question: the one line for it", async ({ page }) => {
  await toStep1(page, s1({ flow: ["question", "question", "question", "question", "question", "question", "question", "question"] }));
  await expect(page.locator("#i-question")).toHaveText("On the unit, press OK when it asks a question, and hold OK for 3 seconds where the screen says Hold.", { timeout: 10000 });
});

test("review MINOR-1: the unit asks again for a file it had taken: Send again sends it again", async ({ page }) => {
  await toStep1(page, s1({ askAgain: true }));
  await expect(page.locator("#s1-error")).toBeVisible({ timeout: 10000 });
  await page.evaluate(() => { globalThis.__fake.env.sc.step1.askAgain = false; });
  await page.click("#send-again");
  await expect(body(page)).toHaveAttribute("data-ui", "done", { timeout: 10000 });
  expect((await u1(page)).puts.filter((p) => p.name === "firmware.bin" && p.accept)).toHaveLength(2);
});

// R26 (owner, 2026-11-16 run): the install's unlock of the bootloader is its own visible step. The RC-10c installer
// says the phase word "unlock" between "install" (slot A is the fallback) and "install" again (the slot-B writes).
// watchInstall records each change of the Install card's visible lines.
async function watchInstall(page) {
  await page.evaluate(() => {
    globalThis.__lines = [];
    const ids = ["i-plan", "i-unlocking", "i-unlocked", "i-installing", "i-done", "i-stop"];
    setInterval(() => {
      const v = ids.filter((id) => !document.getElementById(id).hidden).join(",");
      if (globalThis.__lines.at(-1) !== v) globalThis.__lines.push(v);
    }, 10);
  });
}
const installLines = (page) => page.evaluate(() => globalThis.__lines.filter((x) => x !== ""));

test("R26: the unlock: 'Unlocking the bootloader' while it runs, then 'Bootloader unlocked' above the installing line", async ({ page }) => {
  const flow = ["plan", "plan", "install", "install", ...Array(10).fill("unlock"), ...Array(10).fill("install"), "done"];
  await open(page, { start: "rockusb", step1: s1({ flow }) }, { goal: "mainline" });
  await toBooted(page);
  await watchInstall(page);
  await page.waitForFunction(() => globalThis.__fake.rec.some((e) => e.dev === "step1" && e.op === "attach"));
  await page.click("#connect-step1");
  await expect(page.locator("#i-unlocking")).toHaveText("Unlocking the bootloader. Do not disconnect the power.", { timeout: 15000 });
  await expect(page.locator("#i-unlocked")).toBeHidden();
  await expect(page.locator("#i-unlocked")).toHaveText("Bootloader unlocked.", { timeout: 10000 });
  await expect(page.locator("#i-installing")).toBeVisible();
  await expect(page.locator("#i-unlocking")).toBeHidden();
  await expect(body(page)).toHaveAttribute("data-ui", "system", { timeout: 10000 });
  // the Install card is folded now: its lines keep their state
  await expect(page.locator("#i-unlocked")).not.toHaveAttribute("hidden");
  await expect(page.locator("#i-done")).not.toHaveAttribute("hidden");
  expect(await installLines(page)).toEqual(["i-plan", "i-installing", "i-unlocking", "i-unlocked,i-installing", "i-unlocked,i-done"]);
  const l = await logText(page);
  expect(l).toContain("step 1: unit busy unlock");
  expect(l.split("step 1: the unit unlocked the bootloader, the install goes on").length - 1).toBe(1);
  // plain words only on the screen
  expect(await page.locator("#card-install").innerText()).not.toMatch(/lock_state|0x0[01]/);
});

// RC-10d (unit-6 run 2026-11-17): the page polled past the seconds of the unlock. The unit's unlock word turns from
// "locked" to "unlocked" after the write: the page shows the done line also when it never saw the phase word "unlock".
test("RC-10d: no 'unlock' phase seen, but the unlock word turns locked -> unlocked: 'Bootloader unlocked'", async ({ page }) => {
  const flow = ["plan", "plan", "install", "install", ...Array(6).fill("install"), "done"];
  await open(page, { start: "rockusb", step1: s1({ flow, unlockFrom: 3 }) }, { goal: "mainline" });
  await toBooted(page);
  await watchInstall(page);
  await page.waitForFunction(() => globalThis.__fake.rec.some((e) => e.dev === "step1" && e.op === "attach"));
  await page.click("#connect-step1");
  await expect(page.locator("#i-unlocked")).toHaveText("Bootloader unlocked.", { timeout: 15000 });
  await expect(body(page)).toHaveAttribute("data-ui", "system", { timeout: 15000 });
  expect(await installLines(page)).toEqual(["i-plan", "i-installing", "i-unlocked,i-installing", "i-unlocked,i-done"]);
  const l = await logText(page);
  expect(l).not.toContain("step 1: unit busy unlock");
  expect(l.split("the unit unlocked the bootloader").length - 1).toBe(1);
});

test("RC-10d: a unit that was unlocked at the start (the word is always unlocked): no unlock line", async ({ page }) => {
  const flow = ["plan", "plan", ...Array(6).fill("install"), "done"];
  await open(page, { start: "rockusb", step1: s1({ flow }) }, { goal: "mainline" });
  await toBooted(page);
  await watchInstall(page);
  await page.waitForFunction(() => globalThis.__fake.rec.some((e) => e.dev === "step1" && e.op === "attach"));
  await page.click("#connect-step1");
  await expect(body(page)).toHaveAttribute("data-ui", "system", { timeout: 15000 });
  expect(await installLines(page)).toEqual(["i-plan", "i-installing", "i-done"]);
  expect(await logText(page)).not.toContain("unlocked the bootloader");
});

test("R26: an installer that never says unlock (before RC-10c): no unlock line at any time", async ({ page }) => {
  await open(page, { start: "rockusb", step1: s1({ flow: ["plan", "plan", ...Array(6).fill("install"), "done"] }) }, { goal: "mainline" });
  await toBooted(page);
  await watchInstall(page);
  await page.waitForFunction(() => globalThis.__fake.rec.some((e) => e.dev === "step1" && e.op === "attach"));
  await page.click("#connect-step1");
  await expect(body(page)).toHaveAttribute("data-ui", "system", { timeout: 15000 });
  expect(await installLines(page)).toEqual(["i-plan", "i-installing", "i-done"]);
  expect(await logText(page)).not.toContain("unlocked the bootloader");
});

test("R26: after the unlock started the page never sends the files again (the unlock is part of the install)", async ({ page }) => {
  await toStep1(page, s1({ flow: ["plan", "unlock", "unlock", "refused"], againAfter: ["refused"], flow2: ["plan"] }), { goal: "mainline" });
  await expect(page.locator("#i-refused")).toBeVisible({ timeout: 10000 });
  await expect.poll(async () => (await u1(page)).stage, { timeout: 10000 }).toBe("firmware");
  await page.waitForTimeout(1500);
  const u = await u1(page);
  expect(u.puts.filter((p) => p.name === "firmware.bin")).toHaveLength(1);
  expect(u.commits.map((c) => c.body)).toEqual([{ mode: "mainline" }, {}]);
});

test("R26: a stop during the unlock: KEEP THE UNIT POWERED, and no 'Bootloader unlocked'", async ({ page }) => {
  const flow = ["plan", "install", ...Array(4).fill("unlock"), "hold"];
  await toStep1(page, s1({ flow }), { goal: "mainline" });
  await expect(page.locator("#i-stop")).toBeVisible({ timeout: 15000 });
  await expect(page.locator("#i-unlocked")).toBeHidden();
  await expect(page.locator("#i-unlocking")).toBeHidden();
});

// R27 (the step-1 cable defect of 2026-11-16): on Linux the udev lines at the steps that use the devices, with the
// ModemManager ignore for the installer's link 1d6b:0105 at Start; one line about the unit's keys during a send
const MM_LINE = 'SUBSYSTEM=="usb", ATTR{idVendor}=="1d6b", ATTR{idProduct}=="0105", ENV{ID_MM_DEVICE_IGNORE}="1", MODE="0660", TAG+="uaccess"';
for (const platform of ["Linux", "Windows", "macOS"]) {
  test(`R27 on ${platform}: the udev boxes only on Linux (Connect: download mode and fastboot; Start: the installer's link)`, async ({ page }) => {
    await open(page, { start: "rockusb", platform, step1: s1() });
    const linux = platform === "Linux";
    await expect(page.locator("#udev-rk")).toBeVisible({ visible: linux });
    await expect(page.locator("#udev-rk")).not.toHaveAttribute("open");
    await page.click("#connect");
    await expect(body(page)).toHaveAttribute("data-phase", "download-checked");
    await page.click("#connect-next");
    await expect(page.locator("#udev-xfer")).toBeVisible({ visible: linux });
    if (!linux) return;
    await expect(page.locator("#udev-rk pre")).toHaveText('SUBSYSTEM=="usb", ATTR{idVendor}=="2207", ATTR{idProduct}=="330c", MODE="0660", TAG+="uaccess"\n' +
      'SUBSYSTEM=="usb", ATTR{idVendor}=="18d1", ATTR{idProduct}=="d00d", MODE="0660", TAG+="uaccess"');
    await expect(page.locator("#udev-xfer")).toHaveAttribute("open", ""); // before the installer starts: open
    await expect(page.locator("#udev-xfer summary")).toHaveText("Linux: before you start the installer");
    await expect(page.locator("#udev-xfer pre")).toHaveText(MM_LINE);
    await expect(page.locator("#udev-xfer")).toContainText("Then run sudo udevadm control --reload-rules.");
    await expect(page.locator("#udev-xfer")).toContainText("ModemManager");
  });
}

test("R27: 'Do not press keys on the unit while the page sends a file.' shows during each step-1 send, and only then", async ({ page }) => {
  await open(page, { start: "rockusb", step1: s1() }, { goal: "mainline" });
  await toBooted(page);
  // every change of the line, with the send bars that show at that moment
  await page.evaluate(() => {
    globalThis.__keys = ["hidden "]; // as it is now
    const el = document.getElementById("l-nokeys");
    new MutationObserver(() => {
      const v = `${el.hidden ? "hidden" : "shown"} ${["bar-fwsend", "bar-basesend"].filter((b) => !document.getElementById(b).hidden &&
        document.getElementById(b).childElementCount).join(",")}`;
      if (globalThis.__keys.at(-1) !== v) globalThis.__keys.push(v);
    }).observe(el, { attributes: true, attributeFilter: ["hidden"] });
  });
  await expect(page.locator("#l-nokeys")).toBeHidden();
  await page.waitForFunction(() => globalThis.__fake.rec.some((e) => e.dev === "step1" && e.op === "attach"));
  await page.click("#connect-step1");
  await expect(body(page)).toHaveAttribute("data-ui", "system", { timeout: 15000 });
  await expect(page.locator("#l-nokeys")).toHaveText("Do not press keys on the unit while the page sends a file.");
  await expect(page.locator("#l-nokeys")).toBeHidden();
  // shown for the firmware send, hidden, shown for the Mainline Linux send, hidden
  expect(await page.evaluate(() => globalThis.__keys)).toEqual(["hidden ", "shown bar-fwsend", "hidden bar-fwsend", "shown bar-fwsend,bar-basesend",
    "hidden bar-fwsend,bar-basesend"]);
});
