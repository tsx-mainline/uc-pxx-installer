// SPDX-License-Identifier: GPL-2.0-or-later
// drivecopy.js: write the firmware file onto the installer's UC-PXX-INST drive
// with the File System Access API, then re-read it and check its sha256 in the
// browser. This is HOST-SIDE FILE I/O ONLY: it never touches the unit, the
// eMMC, WebUSB or the A/B state. The manual copy stays the fallback, and the
// user still ejects the drive on the host. See PAGE-DRIVEN-INSTALL-DESIGN §2.
// It also writes the optional ssh public key as SSH-KEY.PUB (writeKeyFile),
// the same way: host-side, into the same folder, read back after the write.
import { FILE_NAME } from "./sshkey.js";

// supported: the File System Access API (Chromium showDirectoryPicker). On a
// browser without it, the page shows the manual-copy fallback only.
export function supported(win = globalThis) {
  return typeof win.showDirectoryPicker === "function";
}

// sha256hex of a Blob/File by reading it whole (crypto.subtle has no streaming
// digest). The firmware is ~928 MiB; a desktop handles it, and the manual copy
// is the fallback if memory is tight.
export async function sha256OfBlob(blob, subtle = crypto.subtle) {
  const buf = await blob.arrayBuffer();
  const d = await subtle.digest("SHA-256", buf);
  return [...new Uint8Array(d)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

// A FAT top-level name the installer accepts: no path separators, not an
// AppleDouble (._NAME), non-empty. The installer's FindFirmware wants exactly
// one *.bin in the top folder; we write the one firmware file by its own name.
export function fatName(name) {
  if (typeof name !== "string" || name === "") throw new Error("empty file name");
  if (/[\\/]/.test(name)) throw new Error("the file name has a path separator");
  if (name.startsWith("._")) throw new Error("an AppleDouble name is not allowed");
  if (!/\.bin$/i.test(name)) throw new Error("the firmware file must end with .bin");
  return name;
}

// writeAndVerify streams `source` (a File/Blob) into `dir` (a
// FileSystemDirectoryHandle, readwrite) as `name`, closes it, then re-reads the
// written file through the SAME handle and returns its sha256. onProgress(n,
// total) is called while writing. It compares against expected.sha256 when the
// source size matches expected.size (so a user who picks the right firmware
// gets a definitive verify); otherwise it returns the sha for the user to check.
//
// It never deletes or overwrites anything but the one file it writes, and it
// writes only into the directory the user chose.
export async function writeAndVerify(dir, source, name, expected, onProgress = () => {}) {
  const fname = fatName(name);
  const total = source.size;
  // 1) write: stream the source into a new writable on the drive.
  const fh = await dir.getFileHandle(fname, { create: true });
  const w = await fh.createWritable();
  let written = 0;
  const reader = source.stream().getReader();
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      await w.write(value);
      written += value.byteLength;
      onProgress(written, total);
    }
    await w.close();
  } catch (e) {
    try { await w.abort(); } catch { /* ignore */ }
    throw e;
  }
  // 2) read back through the same handle and sha256.
  const back = await fh.getFile();
  if (back.size !== total) {
    return { ok: false, sha256: null, size: back.size,
      reason: `the written file is ${back.size} bytes, not ${total}. Copy again, then eject the drive.` };
  }
  const sha = await sha256OfBlob(back);
  // 3) verdict. `verified` is true only when the copy matched the known
  // firmware pin byte for byte; a copy we cannot check against the pin (a
  // different size) is reported but NOT shown as verified (the UI keeps it
  // neutral, not green).
  const canCheck = expected && typeof expected.sha256 === "string" && expected.size === total;
  if (canCheck) {
    if (sha !== expected.sha256) {
      return { ok: false, verified: false, sha256: sha, size: back.size,
        reason: "the copy did not verify (sha256 does not match the firmware). Copy again." };
    }
    return { ok: true, verified: true, sha256: sha, size: back.size,
      reason: `verified: ${expected.label || "firmware"} copied and read back OK (sha256 ${sha}).` };
  }
  return { ok: true, verified: false, sha256: sha, size: back.size,
    reason: `copied and read back OK, but this is not the firmware the page knows (size ${back.size}). ` +
      `sha256 ${sha}. Check it against the firmware you were given before you use it.` };
}

// --- the optional ssh public key: SSH-KEY.PUB in the drive's top folder -----
// KEY_FILE is sshkey.js's FILE_NAME (an 8.3 name, so FAT keeps it as it is).
export const KEY_FILE = FILE_NAME;
const KEY_READ_MAX = 64 << 10;

function sameBytes(a, b) {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
  return true;
}

// writeKeyFile writes `content` (sshkey.validate's `file`: the normalised line
// + one LF) as SSH-KEY.PUB into `dir` (the same handle the firmware goes into),
// then re-reads it through the same handle and compares it byte for byte. It
// writes only this one file (an older SSH-KEY.PUB is replaced) and deletes
// nothing.
export async function writeKeyFile(dir, content) {
  if (typeof content !== "string" || !content.endsWith("\n") || content.indexOf("\n") !== content.length - 1 ||
    content.includes("\r")) {
    throw new Error("the key file must be one line with one LF at the end");
  }
  const want = new TextEncoder().encode(content);
  const fh = await dir.getFileHandle(KEY_FILE, { create: true });
  const w = await fh.createWritable();
  try {
    await w.write(want);
    await w.close();
  } catch (e) {
    try { await w.abort(); } catch { /* ignore */ }
    throw e;
  }
  const back = await fh.getFile();
  const got = new Uint8Array(await back.arrayBuffer());
  if (!sameBytes(got, want)) {
    return { ok: false, reason: `${KEY_FILE} did not read back as written (${got.length} bytes, not ${want.length}). ` +
      `Write it again. If you do not want a key, delete ${KEY_FILE} from the drive by hand.` };
  }
  return { ok: true, reason: `${KEY_FILE} written and read back OK.` };
}

// readKeyFile reads SSH-KEY.PUB from `dir` if it is there (read only; it never
// creates, changes or deletes it): { present: false }, or { present: true,
// size, text } (text null when the file is larger than 64 KiB).
export async function readKeyFile(dir) {
  let fh;
  try {
    fh = await dir.getFileHandle(KEY_FILE);
  } catch (e) {
    if (e && e.name === "NotFoundError") return { present: false };
    throw e;
  }
  const f = await fh.getFile();
  if (f.size > KEY_READ_MAX) return { present: true, size: f.size, text: null };
  return { present: true, size: f.size, text: new TextDecoder().decode(await f.arrayBuffer()) };
}

// pickDirectory / pickSource: the user-gesture wrappers (not called by the core
// above, so the core is testable without a real picker).
export async function pickDirectory(win = globalThis) {
  return win.showDirectoryPicker({ id: "p10-install", mode: "readwrite" });
}
export async function pickSource(win = globalThis) {
  const [h] = await win.showOpenFilePicker({
    id: "p10-firmware",
    types: [{ description: "Firmware image", accept: { "application/octet-stream": [".bin"] } }],
    multiple: false,
  });
  return h.getFile();
}
