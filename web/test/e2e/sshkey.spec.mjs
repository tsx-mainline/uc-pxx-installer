// SPDX-License-Identifier: GPL-2.0-or-later
// The optional ssh public key of the drive step: the field, its check, the
// confirm box, and SSH-KEY.PUB on the drive. The drive is a fake File System
// Access directory (window.showDirectoryPicker / showOpenFilePicker replaced
// before the page loads), so main.js runs its real handlers. All of this is
// host-side file I/O: no unit transfer happens (checked in each test), and the
// after-test invariants of helpers.mjs still apply.
import { readFileSync } from "node:fs";
import { body, expect, open, rec, test } from "./helpers.mjs";

const DOC = JSON.parse(readFileSync(new URL("../unit/sshkey-vectors.json", import.meta.url), "utf8"));
const K = Object.fromEntries(DOC.vectors.map((v) => [v.name, v]));

// installDrive: the fake drive. seed: { name: text } files already on it.
// corrupt: a file name whose read-back gets one extra byte.
async function installDrive(page, seed = {}, opts = {}) {
  await page.addInitScript(([seed, opts]) => {
    const enc = new TextEncoder();
    const files = new Map(Object.entries(seed).map(([k, v]) => [k, enc.encode(v)]));
    const calls = [];
    const fw = new Uint8Array(4096).map((_, i) => (i * 7) & 0xff);
    const dir = {
      kind: "directory",
      name: "UC-PXX-INST",
      async removeEntry(n) { calls.push(["removeEntry", n]); files.delete(n); },
      async getFileHandle(name, o) {
        calls.push(["getFileHandle", name, !!(o && o.create)]);
        if (!files.has(name)) {
          if (!o || !o.create) throw new DOMException("not found", "NotFoundError");
          files.set(name, new Uint8Array(0));
        }
        return {
          kind: "file",
          name,
          async createWritable() {
            calls.push(["createWritable", name]);
            const ch = [];
            return {
              async write(v) { ch.push(new Uint8Array(v)); },
              async close() {
                const b = new Uint8Array(ch.reduce((n, c) => n + c.length, 0));
                let o = 0;
                for (const c of ch) { b.set(c, o); o += c.length; }
                files.set(name, b);
              },
              async abort() { calls.push(["abort", name]); },
            };
          },
          async getFile() {
            const b = files.get(name);
            return new File([opts.corrupt === name ? new Uint8Array([...b, 0x78]) : b], name);
          },
        };
      },
    };
    globalThis.__drive = {
      calls,
      names: () => [...files.keys()].sort(),
      text: (n) => (files.has(n) ? new TextDecoder().decode(files.get(n)) : null),
      size: (n) => (files.has(n) ? files.get(n).length : null),
    };
    window.showDirectoryPicker = async () => dir;
    window.showOpenFilePicker = async () => [{ async getFile() { return new File([fw], "fw-test.bin"); } }];
  }, [seed, opts]);
}

const drive = (page) => page.evaluate(() => ({
  names: globalThis.__drive.names(), calls: globalThis.__drive.calls, key: globalThis.__drive.text("SSH-KEY.PUB"),
  other: globalThis.__drive.text("README.TXT"), fw: globalThis.__drive.size("fw-test.bin"),
}));
const keyState = (page, s) => expect(body(page)).toHaveAttribute("data-sshkey", s);
const noUnitTransfer = async (page) => expect((await rec(page)).filter((e) => e.op === "cbw" || e.op === "cmd")).toEqual([]);

async function chooseBoth(page) {
  await page.click("#drivecopy-source");
  await expect(page.locator("#drivecopy-source-name")).toContainText("fw-test.bin");
  await page.click("#drivecopy-dir");
  await expect(page.locator("#drivecopy-dir-name")).toHaveText("UC-PXX-INST");
}

test("a valid ed25519 key: type, size and fingerprint shown; the box is required; Copy writes the firmware and SSH-KEY.PUB = line + LF", async ({ page }) => {
  await installDrive(page, { "README.TXT": "keep me\n" });
  await open(page, { start: "rockusb" });
  await keyState(page, "none");
  await expect(page.locator("#sshkey-status")).toContainText("ssh server of the new system stays off");
  await chooseBoth(page);
  await expect(page.locator("#sshkey-drive")).toHaveText("The drive has no SSH-KEY.PUB.");
  await page.fill("#sshkey-input", K["ed25519-lf"].input);
  await keyState(page, "unconfirmed");
  const fp = K.ed25519.fingerprint;
  await expect(page.locator("#sshkey-status")).toContainText(`Key: ssh-ed25519, 256 bits, fingerprint ${fp}.`);
  await expect(page.locator("#sshkey-confirm-text")).toHaveText(`This is my key (fingerprint ${fp})`);
  await expect(page.locator("#sshkey-line")).toHaveText(K.ed25519.line);
  // not confirmed: neither write is possible
  await expect(page.locator("#drivecopy-run")).toBeDisabled();
  await expect(page.locator("#sshkey-write")).toBeDisabled();
  await page.check("#sshkey-confirm");
  await keyState(page, "ready");
  await expect(page.locator("#drivecopy-run")).toBeEnabled();
  await page.click("#drivecopy-run");
  await expect(page.locator("#drivecopy-status")).toContainText("SSH-KEY.PUB written and read back OK.");
  await expect(page.locator("#sshkey-drive")).toContainText(`fingerprint ${fp}. Check this fingerprint on the unit's screen`);
  await expect(page.locator("#sshkey-drive")).toHaveClass("ok");
  const d = await drive(page);
  expect(d.key).toBe(`${K.ed25519.line}\n`);
  expect(d.fw).toBe(4096);
  expect(d.other).toBe("keep me\n");
  expect(d.names).toEqual(["README.TXT", "SSH-KEY.PUB", "fw-test.bin"]);
  expect(d.calls.filter((c) => c[0] === "removeEntry" || c[0] === "abort")).toEqual([]);
  expect(d.calls.filter((c) => c[0] === "createWritable").map((c) => c[1])).toEqual(["fw-test.bin", "SSH-KEY.PUB"]);
  expect(await page.locator("#log").textContent()).toContain(`SSH-KEY.PUB written and read back OK ${fp}`);
  await noUnitTransfer(page);
});

test("rsa 3072 and 4096 are accepted with their sizes; the written line is the normalised one", async ({ page }) => {
  await installDrive(page);
  await open(page, { start: "rockusb" });
  for (const n of ["rsa-3072", "rsa-4096"]) {
    await page.fill("#sshkey-input", K[n].input);
    await keyState(page, "unconfirmed");
    await expect(page.locator("#sshkey-status")).toContainText(`ssh-rsa, ${K[n].bits} bits, fingerprint ${K[n].fingerprint}.`);
  }
  await page.click("#drivecopy-dir");
  await page.check("#sshkey-confirm");
  await page.click("#sshkey-write");
  await expect(page.locator("#drivecopy-status")).toHaveText("SSH-KEY.PUB written and read back OK.");
  expect((await drive(page)).key).toBe(K["rsa-4096"].file);
  await noUnitTransfer(page);
});

test("refused keys: a plain reason, no confirm box, both writes off, nothing written", async ({ page }) => {
  await installDrive(page);
  await open(page, { start: "rockusb" });
  await chooseBoth(page);
  const cases = {
    "rsa-2048": "2048 bits; at least 3072",
    "options-restrict": "starts with options",
    "two-lines": "must be one line",
    "ecdsa-p256": "is not accepted",
    "ssh-dss": "is not accepted",
    "sk-ed25519": "is not accepted",
    "ed25519-cert": "is not accepted",
    "b64-noncanonical": "not valid base64",
    "b64-bad-char": "not valid base64",
    "ed25519-trailing-garbage": "extra bytes",
    "rsa-trailing-garbage": "extra bytes",
    "comment-quote": "comment may have",
    "comment-backslash": "comment may have",
    "comment-space": "no spaces",
    "oversize": "at most 8192 bytes",
    "private-key": "PRIVATE key",
  };
  for (const [n, why] of Object.entries(cases)) {
    await page.fill("#sshkey-input", K[n].input);
    await keyState(page, "bad");
    await expect(page.locator("#sshkey-status")).toContainText(why);
    await expect(page.locator("#sshkey-status")).toContainText("empty the field to go on without a key");
    await expect(page.locator("#sshkey-confirm-row")).toBeHidden();
    await expect(page.locator("#sshkey-manual")).toBeHidden();
    await expect(page.locator("#drivecopy-run")).toBeDisabled();
    await expect(page.locator("#sshkey-write")).toBeDisabled();
  }
  // emptying the field is "no key" again: the firmware copy is possible
  await page.fill("#sshkey-input", "");
  await keyState(page, "none");
  await expect(page.locator("#drivecopy-run")).toBeEnabled();
  expect((await drive(page)).calls.filter((c) => c[0] === "createWritable")).toEqual([]);
  await noUnitTransfer(page);
});

test("the browser module gives the vector file's result for every vector", async ({ page }) => {
  await open(page, { start: "rockusb" });
  const got = await page.evaluate(async (vs) => {
    const m = await import("/js/sshkey.js");
    const out = [];
    for (const v of vs) {
      const r = await m.validate(v.input);
      out.push({ name: v.name, ok: r.ok, type: r.type, bits: r.bits, fingerprint: r.fingerprint, line: r.line, file: r.file });
    }
    return out;
  }, DOC.vectors);
  for (const [i, v] of DOC.vectors.entries()) {
    const want = v.valid ? { name: v.name, ok: true, type: v.type, bits: v.bits, fingerprint: v.fingerprint, line: v.line, file: v.file }
      : { name: v.name, ok: false };
    const g = Object.fromEntries(Object.entries(got[i]).filter(([, x]) => x !== undefined && x !== null));
    expect(g).toEqual(want);
  }
});

test("no key: only the firmware is written; an SSH-KEY.PUB already on the drive is kept and named, never deleted", async ({ page }) => {
  await installDrive(page, { "SSH-KEY.PUB": K["rsa-4096"].file, "README.TXT": "keep me\n" });
  await open(page, { start: "rockusb" });
  await chooseBoth(page);
  const old = page.locator("#sshkey-drive");
  await expect(old).toContainText(`The drive already has an SSH-KEY.PUB: ssh-rsa, 4096 bits, fingerprint ${K["rsa-4096"].fingerprint}.`);
  await expect(old).toContainText("delete SSH-KEY.PUB from the drive by hand");
  await expect(old).toHaveClass("warn");
  await page.click("#drivecopy-run");
  await expect(page.locator("#drivecopy-status")).toContainText("copied and read back OK");
  await expect(page.locator("#drivecopy-status")).not.toContainText("SSH-KEY.PUB");
  await expect(old).toContainText("The page did not change it");
  const d = await drive(page);
  expect(d.key).toBe(K["rsa-4096"].file);
  expect(d.other).toBe("keep me\n");
  expect(d.names).toEqual(["README.TXT", "SSH-KEY.PUB", "fw-test.bin"]);
  // SSH-KEY.PUB was only looked up (no create, no writable); nothing was removed
  expect(d.calls.filter((c) => c[1] === "SSH-KEY.PUB")).toEqual([["getFileHandle", "SSH-KEY.PUB", false],
    ["getFileHandle", "SSH-KEY.PUB", false]]);
  expect(d.calls.filter((c) => c[0] === "removeEntry")).toEqual([]);
  await noUnitTransfer(page);
});

test("an SSH-KEY.PUB on the drive that is not a valid key is named, kept, and the person is told to delete it by hand", async ({ page }) => {
  await installDrive(page, { "SSH-KEY.PUB": K["rsa-2048"].input });
  await open(page, { start: "rockusb" });
  await page.click("#drivecopy-dir");
  await expect(page.locator("#sshkey-drive")).toContainText("is not a valid key: This RSA key has 2048 bits");
  await expect(page.locator("#sshkey-drive")).toContainText("Delete SSH-KEY.PUB from the drive by hand");
  expect((await drive(page)).key).toBe(K["rsa-2048"].input);
});

test("Write only the key: with no firmware chosen, it writes SSH-KEY.PUB alone (over an older one)", async ({ page }) => {
  await installDrive(page, { "SSH-KEY.PUB": K["rsa-4096"].file });
  await open(page, { start: "rockusb" });
  await page.click("#drivecopy-dir");
  await page.fill("#sshkey-input", K["ed25519-comment-64"].input);
  await page.check("#sshkey-confirm");
  await expect(page.locator("#drivecopy-run")).toBeDisabled(); // no firmware chosen
  await expect(page.locator("#sshkey-write")).toBeEnabled();
  await page.click("#sshkey-write");
  await expect(page.locator("#sshkey-drive")).toContainText(`fingerprint ${K.ed25519.fingerprint}. Check this fingerprint`);
  const d = await drive(page);
  expect(d.key).toBe(K["ed25519-comment-64"].file);
  expect(d.names).toEqual(["SSH-KEY.PUB"]);
  await noUnitTransfer(page);
});

test("an edit after the confirm unticks the box and shows the new fingerprint", async ({ page }) => {
  await installDrive(page);
  await open(page, { start: "rockusb" });
  await chooseBoth(page);
  await page.fill("#sshkey-input", K.ed25519.input);
  await page.check("#sshkey-confirm");
  await keyState(page, "ready");
  await page.fill("#sshkey-input", K["rsa-3072"].input);
  await keyState(page, "unconfirmed");
  await expect(page.locator("#sshkey-confirm")).not.toBeChecked();
  await expect(page.locator("#sshkey-confirm-text")).toHaveText(`This is my key (fingerprint ${K["rsa-3072"].fingerprint})`);
  await expect(page.locator("#drivecopy-run")).toBeDisabled();
});

test("a read-back of SSH-KEY.PUB that differs is reported as a failure, not as OK", async ({ page }) => {
  await installDrive(page, {}, { corrupt: "SSH-KEY.PUB" });
  await open(page, { start: "rockusb" });
  await chooseBoth(page);
  await page.fill("#sshkey-input", K.ed25519.input);
  await page.check("#sshkey-confirm");
  await page.click("#drivecopy-run");
  await expect(page.locator("#drivecopy-status")).toContainText("SSH-KEY.PUB did not read back as written");
  await expect(page.locator("#drivecopy-status")).toHaveClass("warn");
  await expect(page.locator("#sshkey-drive")).toContainText("not a valid key");
  expect(await page.locator("#log").textContent()).toContain("SSH-KEY.PUB FAILED");
});

test("without the File System Access API (shipped config): the manual text names SSH-KEY.PUB next to the .bin and shows the line", async ({ page }) => {
  // headless Chromium has the API: take it away before the page loads
  await page.addInitScript(() => { window.showDirectoryPicker = undefined; });
  await open(page, { start: "rockusb" }, { shippedConfig: true });
  await expect(page.locator("#drivecopy-controls")).toBeHidden();
  await expect(page.locator("#drivecopy-support")).toContainText("save its line as SSH-KEY.PUB in the drive's top folder, next to the .bin");
  await expect(page.locator("#sshkey-box")).toBeVisible();
  await expect(page.locator("#sshkey-box")).toContainText("SSH public key for the new system (optional)");
  await expect(page.locator("#sshkey-box")).toContainText("installer shows the key's fingerprint on the unit's screen");
  await expect(page.locator("#sshkey-box")).toContainText("the ssh server stays off");
  await page.fill("#sshkey-input", K["ed25519-crlf"].input); // the textarea makes the CRLF an LF
  await keyState(page, "unconfirmed");
  await expect(page.locator("#sshkey-manual")).toBeVisible();
  await expect(page.locator("#sshkey-manual")).toContainText("save this one line as a file named SSH-KEY.PUB in the top folder of the drive, next to the .bin");
  await expect(page.locator("#sshkey-line")).toHaveText(K.ed25519.line);
  // the shipped flags are untouched by the key field
  const cfg = await page.evaluate(async () => {
    const c = await import("/js/config.js");
    return [c.STEP2_USB_ENABLED, c.FINISH_USB_ENABLED, c.MAINLINE_INSTALL_APPROVED];
  });
  expect(cfg).toEqual([false, false, false]);
  await noUnitTransfer(page);
});
