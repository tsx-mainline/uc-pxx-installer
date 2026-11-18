// SPDX-License-Identifier: GPL-2.0-or-later
// The page's words (owner R4, R14, R17; DESIGN v3 §2): no time estimate in any
// text of the page, and every text of the mock-up's normal-path frames is in
// the page, except the ones the owner changed after the mock-up (R21 to R24),
// each listed with its reason.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const SITE = fileURLToPath(new URL("../../site/", import.meta.url));
const html = readFileSync(join(SITE, "index.html"), "utf8");
const JS = Object.fromEntries(readdirSync(join(SITE, "js")).filter((f) => f.endsWith(".js"))
  .map((f) => [f, readFileSync(join(SITE, "js", f), "utf8")]));
const strip = (s) => s.replace(/<!--[\s\S]*?-->/g, "").replace(/<[^>]+>/g, " ").replace(/&hellip;/g, "…").replace(/&minus;/g, "−")
  .replace(/&nbsp;/g, " ").replace(/&#10003;/g, "✓").replace(/\s+/g, " ");
const pageText = strip(html.slice(html.indexOf("<body>")));
// the string literals of a script, without comments
function literals(src) {
  const c = src.replace(/\/\*[\s\S]*?\*\//g, "").split("\n").map((l) => l.replace(/(^|[^:"'`\\])\/\/.*$/, "$1")).join("\n");
  return [...c.matchAll(/"((?:[^"\\\n]|\\.)*)"|`((?:[^`\\]|\\.)*)`/g)].map((m) => m[1] ?? m[2]);
}

// timeWords: "about N s/min", "N to M minutes", "takes", "in about", any
// number with a time unit; a hold of OK "for 3 seconds" is an instruction
export function timeWords(text) {
  const t = text.replace(/for 3 seconds/g, "");
  return t.match(/\babout (?:\d+\s*(?:s|sec|seconds?|mins?|minutes?|hours?)\b|an? (?:second|minute|hour))|\b\d+\s*(?:–|-|to)\s*\d+\s*(?:s|sec|seconds?|min|minutes?)\b|\btakes? (?:about |up to )?(?:\d|a |an )|\bin about\b|\b\d+\s*(?:s|sec|seconds?|mins?|minutes?|hours?)\b/gi) || [];
}

test("the time rule catches the old page's estimates", () => {
  for (const t of ["The unit restarts into fastboot (about 10 s).", "The unit screen shows its status in about a minute.",
    "restarts into mainline Linux (1–3 minutes)", "takes about 10 minutes", "2 to 5 minutes", "about 40 s left"]) {
    assert.notDeepEqual(timeWords(t), [], t);
  }
  assert.deepEqual(timeWords("Hold OK (the Mic key) for 3 seconds to start the install."), []);
  assert.deepEqual(timeWords("This computer has about 2.5 GB of free disk space for the download."), []);
});

test("no time estimate in index.html, texts.js or any screen text of the scripts", () => {
  assert.deepEqual(timeWords(pageText), []);
  for (const [f, src] of Object.entries(JS)) {
    // progress.js makes the bars' computed time left (owner Q1: keep it); log
    // lines are not screen text (log( ... ) calls are left out)
    if (f === "progress.js") continue;
    const noLog = src.replace(/\blog\((?:[^()]|\((?:[^()]|\([^()]*\))*\))*\)/g, "");
    for (const s of literals(noLog)) assert.deepEqual(timeWords(s), [], `${f}: ${s}`);
  }
  // progress.js: the only time words are the computed "about N s/min left"
  const p = literals(JS["progress.js"]).filter((s) => timeWords(s).length || /\babout\b.*\bleft\b/.test(s));
  assert.deepEqual(p, ["about ${Math.max(1, Math.round(seconds))} s left", "about ${Math.round(seconds / 60)} min left"]);
});

// The mock-up texts that this page does not show, by owner decision after
// the mock-up v3 (ENDUSER-PAGE-REQUIREMENTS-2026-11-15.md):
const CHANGED = {
  "Mainline Linux, part 1": "R23: one base, downloaded by the page: its row is 'Mainline Linux'",
  "Mainline Linux, part 2": "R23: no part 2 (step 2 clones slot B into slot A)",
  "Sending mainline Linux, part 1": "R23: the bar is 'Sending mainline Linux' (the base)",
  "Sending mainline Linux, part 2": "R23: no part 2",
  "Do not disconnect the cable.": "R23: it went with the part-2 send",
  "Step 2 over the cable": "R23: the step is 'Install the system' (DESIGN-R23 §7.4.1)",
  "This computer has about 2.5 GB of free disk space for the download.":
    "R23: the page keeps the 97 MB of the image and the base in memory and stores nothing on the disk: the box is gone",
  "More options": "R34 (owner 2026-11-16): the SSH key field is on the Files card itself, not under More options",
  "Delete the downloaded files": "R23: nothing is kept in the page's storage",
  "Keep them if you install another unit.": "R23: goes with the delete button",
  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA… me@laptop": "the mock-up's example key (the person's own input)",
  "Key: ssh-ed25519, 256 bits, fingerprint SHA256:7pVN…spuU": "made from the key: texts.js keyGood",
  "This is my key (fingerprint SHA256:7pVN…spuU)": "made from the key: texts.js keyConfirm",
  "Step 2 starts on the unit by itself. To stop it, press Back on the unit.": "RC-10d (owner 2026-11-17): the end of the page in plain words, no 'step 2'",
  "Step 2 runs on the unit. Do not disconnect the power.": "RC-10d (owner 2026-11-17): no 'step 2' (the copy)",
  "Done. Mainline Linux is installed. You can disconnect the cable.": "RC-10d (owner 2026-11-17): it names the USB-C cable, and a line says what to do next",
  "With a key you can log in to the new system as root over your network. Paste the one line of your .pub file. Never paste a private key.":
    "RC-10d (owner 2026-11-17): the .pub file can be chosen as well as pasted",
};

test("every text of the mock-up's normal-path frames is in the page (or listed as changed by the owner)", () => {
  const m = JSON.parse(readFileSync(new URL("./mockup-v3-texts.json", import.meta.url), "utf8"));
  assert.equal(m.sha256, "b83684f49197f0ffb1acfe7d4961eae54bafd2cf9f9dc158d597c2f63660050e");
  const all = pageText + " " + Object.values(JS).map((s) => literals(s).join(" ")).join(" ");
  const missing = [];
  for (const t of m.texts) {
    if (CHANGED[t]) continue;
    if (!all.includes(t)) missing.push(t);
  }
  assert.deepEqual(missing, []);
  // the changed ones are not in the page
  for (const c of Object.keys(CHANGED)) if (!c.startsWith("Key:") && !c.startsWith("This is my key")) assert.ok(!pageText.includes(c), c);
  // the step titles in the page are the mock-up's
  for (const x of ["What do you want to install?", "Get the files", "Put the unit in download mode", "Start the installer", "Install",
    "Install the system", "Finish"]) assert.ok(pageText.includes(x), x);
});

test("R24: the Crestron web page items are in the userdebug-only group", () => {
  const g = html.slice(html.indexOf('<div class="only-userdebug" id="check-crestron">'), html.indexOf("</div>", html.indexOf('id="check-crestron"')));
  for (const t of ["Auto update is off", "XiO Cloud connection is off", "Optional: the update server URL is changed"]) assert.ok(g.includes(t), t);
});

test("no developer detail on the screen: no LBA, sector, slot letter or A/B words in the page's screen texts", async () => {
  const t = await import("../../site/js/texts.js");
  const p = { file: "f.zst", label: "Mainline Linux", installs: "the kiosk" };
  const k = { type: "ssh-ed25519", bits: 256, fingerprint: "SHA256:x" };
  const vals = [...Object.values(t.TEXT).map((v) => (typeof v === "function" ? v(v.length === 1 && v.toString().includes("reason") ? "r" : { ...p, ...k }) : v)),
    ...Object.values(t.STOP_WHY).map(([, w]) => w), ...Object.values(t.STOP_TITLE), t.STOP_SAVE];
  const screen = pageText + " " + vals.join(" ");
  assert.doesNotMatch(screen, /\bLBA\b|\bsectors?\b|\bslot [ab]\b|\bA\/B\b|\bBCB\b|lock_state|0x0[01]\b/i);
});
