// SPDX-License-Identifier: GPL-2.0-or-later
// drivecopy.js: write the firmware file onto the installer's P10-INSTALL drive
// with the File System Access API, then re-read it and check its sha256 in the
// browser. This is HOST-SIDE FILE I/O ONLY: it never touches the unit, the
// eMMC, WebUSB or the A/B state. The manual copy stays the fallback, and the
// user still ejects the drive on the host. See PAGE-DRIVEN-INSTALL-DESIGN §2.

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
