// SPDX-License-Identifier: GPL-2.0-or-later
// unlock.js: the optional, opt-in unlock guide (tier A). It is a GUIDE: it shows
// one-liner scripts the user runs on THEIR OWN computer over LAN adb. It drives
// no device over WebUSB and makes no eMMC write. It never offers `lock`.
// See PAGE-DRIVEN-INSTALL-DESIGN §4.1 and RULES.md (TEE unlock is owner-gated
// for real device use; this page only shows the guide).

// The warning shown before the guide. Plain, and it says what unlock costs.
export const WARNING = [
  "Unlocking is optional. It sets the unit's verified-boot state to “orange”",
  "(ro.boot.verifiedbootstate=orange) so the unit can boot unsigned systems",
  "(this is what mainline Linux needs). It does not wipe the unit.",
  "Do this only on a unit you own. Once a unit runs mainline, never re-lock it:",
  "a re-lock makes the unsigned system fail verify. These scripts never re-lock.",
].join(" ");

// render builds the guide into `root` (an element). It reads config.UNLOCK and
// fetches the one-liner text same-origin (tools/). `fetchText(url)` is injected
// so tests can drive it; by default it is a same-origin GET.
export async function render(root, UNLOCK, fetchText = defaultFetchText) {
  root.textContent = "";
  const warn = el("p", WARNING);
  warn.className = "warn";
  root.append(warn);

  // The client: download link + the pinned sha, and the fallback note.
  const c = UNLOCK.client;
  const cp = el("p");
  cp.append(
    document.createTextNode("Unlock client (download into the folder you run the script from): "),
    link(`tools/${c.file}`, c.file),
    el("br"),
    el("code", `sha256 ${c.sha256}`),
    el("br"),
    document.createTextNode(
      `If this site does not have the client, download ${c.file} from the project's Releases page. ` +
      "The script checks its sha256 either way."),
  );
  root.append(cp);

  // One tab per OS with the one-liner text.
  for (const s of UNLOCK.scripts) {
    const box = el("div");
    box.append(el("h3", `${s.os} (${s.shell})`));
    const dl = el("p", "Run it on your computer after you set the unit's IP. Download: ");
    dl.append(link(`tools/${s.file}`, s.file));
    box.append(dl);
    const pre = el("pre", "loading…");
    box.append(pre);
    root.append(box);
    try {
      pre.textContent = await fetchText(`tools/${s.file}`);
    } catch (e) {
      pre.textContent = `Could not load ${s.file} from this site (${e && e.message}). ` +
        `Download it from the Releases page.`;
    }
  }

  const note = el("p",
    `The script writes a small marker to ${UNLOCK.markerPath} (unencrypted storage) so the ` +
    "installer can tell the unit was unlocked. The authoritative check stays the read-back the script does.");
  note.className = "warn";
  root.append(note);
}

// defaultFetchText: a same-origin GET of a text file (CSP connect-src 'self').
async function defaultFetchText(url) {
  const r = await fetch(url, { cache: "no-store", credentials: "omit", referrerPolicy: "no-referrer" });
  if (!r.ok) throw new Error(`HTTP ${r.status}`);
  return r.text();
}

function el(tag, text) {
  const e = document.createElement(tag);
  if (text !== undefined) e.textContent = text;
  return e;
}
function link(href, text) {
  const a = el("a", text);
  a.href = href;
  a.setAttribute("download", "");
  return a;
}
