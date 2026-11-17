// SPDX-License-Identifier: GPL-2.0-or-later
// download.js: the files of the Files step (DESIGN v3 M2, W4; owner R21/R22).
//
// - fetchBytes: the installer image (and the finish set) from this site
//   (same origin), with a bar.
// - fetchBase / folderBase: the mainline base (DESIGN-R23 M2: a schema-3
//   payload folder) from this site, or from a folder the person chose: the
//   manifest first, its sha256 the pin before any other byte is fetched or
//   read, then each file with the manifest's size and sha256.
// - checkFile: a file the person chose (the Crestron .bin, a mainline part):
//   its size, then its sha256 with the streaming hash, against the pin, before
//   the page uses any byte of it.
// - downloadPart: NOT USED in this page version: a large file (for example a
//   bundle) from a same-origin or CORS host into the browser's private
//   storage (OPFS), with a bar, then checked like a chosen file. Kept for a
//   later host (owner R22).
// - deleteDownloads: removes the page's own folder in the private storage.
// Nothing here talks to the unit. The fetch options are today's: no
// credentials, no cache, no referrer, no redirect for a part.
import { sha256OfFile } from "./sha256stream.js";

export const OPFS_DIR = "uc-pxx-download";
const OPTS = Object.freeze({ cache: "no-store", credentials: "omit", referrerPolicy: "no-referrer" });

export class DownloadError extends Error {
  constructor(message) {
    super(message);
    this.name = "DownloadError";
  }
}

// bodyLength: the Content-Length when it is the length of the body fetch
// gives, else 0 (not known). With a Content-Encoding (GitHub Pages serves gzip)
// it is the length of the encoded bytes, and fetch gives the decoded bytes.
// Only a length: the callers check each file's size and sha256 against its pin.
export function bodyLength(r) {
  const enc = String(r.headers.get("content-encoding") || "").trim().toLowerCase();
  if (enc !== "" && enc !== "identity") return 0;
  return Number(r.headers.get("content-length")) || 0;
}

// fetchBytes reads a same-origin file into memory with progress. A missing
// file is { ok: false, missing: true }; a network error or a cut transfer is
// { ok: false, missing: false }. size: the caller's pinned size (or none),
// only for the bar when the server gives no body length.
export async function fetchBytes(url, onProgress = () => {}, fetchFn = fetch, size = 0) {
  let r;
  try {
    r = await fetchFn(url, OPTS);
  } catch (e) {
    return { ok: false, missing: false, why: `${e && e.name}: ${e && e.message}` };
  }
  if (r.status === 404) return { ok: false, missing: true, why: "HTTP 404" };
  if (!r.ok) return { ok: false, missing: false, why: `HTTP ${r.status}` };
  const total = bodyLength(r);
  const barTotal = total || (Number.isSafeInteger(size) && size > 0 ? size : 0);
  const parts = [];
  let done = 0;
  try {
    const reader = r.body.getReader();
    for (;;) {
      const { value, done: end } = await reader.read();
      if (end) break;
      parts.push(value);
      done += value.byteLength;
      onProgress(done, Math.max(barTotal, done));
    }
  } catch (e) {
    return { ok: false, missing: false, why: `the download stopped at ${done} bytes (${e && e.message})` };
  }
  if (total && done !== total) return { ok: false, missing: false, why: `${done} of ${total} bytes` };
  const out = new Uint8Array(done);
  let o = 0;
  for (const p of parts) {
    out.set(p, o);
    o += p.length;
  }
  return { ok: true, bytes: out };
}

export const BASE_NAMES = Object.freeze({ manifest: "manifest.json", template: "boot-template.img", rootfsZst: "rootfs.ext4.zst" });
export const MAX_MANIFEST = 65536;
const SHA = /^[0-9a-f]{64}$/;

// parseBase: the parts of a schema-3 manifest the page checks (the unit
// checks the whole manifest): boot_template and rootfs_zst with their file
// names, sizes and sha256s.
export function parseBase(bytes) {
  let j;
  try {
    j = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes));
  } catch {
    return { ok: false, why: "the manifest is not JSON" };
  }
  const t = j && j.boot_template, z = j && j.rootfs_zst;
  const good = (f, name) => f && f.file === name && Number.isSafeInteger(f.size) && f.size > 0 && typeof f.sha256 === "string" && SHA.test(f.sha256);
  if (!good(t, BASE_NAMES.template) || !good(z, BASE_NAMES.rootfsZst)) {
    return { ok: false, why: "the manifest does not name boot-template.img and rootfs.ext4.zst with sizes and sha256s (schema 3)" };
  }
  return { ok: true, manifest: j };
}

async function manifestPinned(bytes, pin) {
  if (!SHA.test(String(pin))) return { ok: false, why: "this page version pins no base yet" };
  if (bytes.length <= 0 || bytes.length > MAX_MANIFEST) return { ok: false, why: `the manifest has ${bytes.length} bytes` };
  const sha = (await sha256OfFile(new Blob([bytes]))).sha256;
  if (sha !== pin) return { ok: false, sha, why: `the manifest sha256 ${sha} is not the pin ${pin}` };
  const p = parseBase(bytes);
  return p.ok ? { ok: true, sha, manifest: p.manifest } : { ok: false, sha, why: p.why };
}

// fetchBase: the base from url (a folder URL ending in "/": this site's
// BASE.dir). progress(done, total) over the two big files.
export async function fetchBase(url, pin, onProgress = () => {}, fetchFn = fetch) {
  const m = await fetchBytes(url + BASE_NAMES.manifest, () => {}, fetchFn);
  if (!m.ok) return { ok: false, missing: m.missing, why: `${BASE_NAMES.manifest}: ${m.why}` };
  const c = await manifestPinned(m.bytes, pin);
  if (!c.ok) return { ok: false, wrong: true, why: c.why };
  const t = c.manifest.boot_template, z = c.manifest.rootfs_zst;
  const total = t.size + z.size;
  const files = { manifest: new Blob([m.bytes]) };
  let base = 0;
  for (const [k, want] of [["template", t], ["rootfsZst", z]]) {
    const r = await fetchBytes(url + want.file, (n) => onProgress(base + Math.min(n, want.size), total), fetchFn, want.size);
    if (!r.ok) return { ok: false, missing: r.missing, why: `${want.file}: ${r.why}` };
    if (r.bytes.length !== want.size) return { ok: false, wrong: true, why: `${want.file} has ${r.bytes.length} bytes, the manifest says ${want.size}` };
    const s = (await sha256OfFile(new Blob([r.bytes]))).sha256;
    if (s !== want.sha256) return { ok: false, wrong: true, why: `${want.file} has sha256 ${s}, the manifest says ${want.sha256}` };
    files[k] = new Blob([r.bytes]);
    base += want.size;
  }
  return { ok: true, sha: c.sha, manifest: c.manifest, files, total: m.bytes.length + total };
}

// folderBase: the base from the files of a folder the person chose (an
// <input webkitdirectory>): the folder named uc-pxx-mainline with the three
// files; the same checks as fetchBase. progress(done, total) while hashing.
export async function folderBase(fileList, pin, onProgress = () => {}) {
  const dirs = new Map();
  for (const f of fileList) {
    const p = f.webkitRelativePath || f.name;
    const i = p.lastIndexOf("/");
    const dir = i < 0 ? "" : p.slice(0, i), name = p.slice(i + 1);
    if (dir.slice(dir.lastIndexOf("/") + 1) !== "uc-pxx-mainline") continue;
    if (!dirs.has(dir)) dirs.set(dir, {});
    for (const [k, n] of Object.entries(BASE_NAMES)) if (name === n) dirs.get(dir)[k] = f;
  }
  let why = "the folder holds no folder uc-pxx-mainline with manifest.json, boot-template.img and rootfs.ext4.zst";
  for (const [dir, d] of dirs) {
    if (!d.manifest || !d.template || !d.rootfsZst) continue;
    if (d.manifest.size > MAX_MANIFEST) { why = `${dir}/manifest.json has ${d.manifest.size} bytes`; continue; }
    const c = await manifestPinned(new Uint8Array(await d.manifest.arrayBuffer()), pin);
    if (!c.ok) { why = `${dir}: ${c.why}`; continue; }
    const t = c.manifest.boot_template, z = c.manifest.rootfs_zst;
    if (d.template.size !== t.size || d.rootfsZst.size !== z.size) { why = `${dir}: the files' sizes are not the manifest's`; continue; }
    const total = t.size + z.size;
    const r1 = await sha256OfFile(d.template, (n) => onProgress(n, total));
    const r2 = await sha256OfFile(d.rootfsZst, (n) => onProgress(t.size + n, total));
    if (r1.sha256 !== t.sha256 || r2.sha256 !== z.sha256) { why = `${dir}: a file's sha256 is not the manifest's`; continue; }
    return { ok: true, sha: c.sha, manifest: c.manifest, dir, files: { manifest: d.manifest, template: d.template, rootfsZst: d.rootfsZst },
      total: d.manifest.size + total };
  }
  return { ok: false, why };
}

// checkFile: the chosen file against the pin { size, sha256 }. A pin without
// a 64-hex sha256 (a placeholder) refuses every file.
export async function checkFile(file, pin, onProgress = () => {}, hash = sha256OfFile) {
  if (!pin || typeof pin.sha256 !== "string" || !/^[0-9a-f]{64}$/.test(pin.sha256)) {
    return { ok: false, why: "this page version pins no such file yet" };
  }
  if (!file || typeof file.size !== "number") return { ok: false, why: "no file" };
  if (Number.isSafeInteger(pin.size) && file.size !== pin.size) {
    return { ok: false, why: `the file has ${file.size} bytes, the pin says ${pin.size}` };
  }
  const r = await hash(file, onProgress);
  if (r.size !== file.size) return { ok: false, why: `read ${r.size} of ${file.size} bytes` };
  if (r.sha256 !== pin.sha256) return { ok: false, sha256: r.sha256, why: `sha256 ${r.sha256} is not the pin ${pin.sha256}` };
  return { ok: true, sha256: r.sha256 };
}

// storageFree: the bytes the browser lets this page store, or null if the
// browser does not say.
export async function storageFree(storage = navigator.storage) {
  try {
    const e = await storage.estimate();
    if (typeof e.quota === "number" && typeof e.usage === "number") return e.quota - e.usage;
  } catch {
    /* not known */
  }
  return null;
}

async function opfsDir(storage) {
  const root = await storage.getDirectory();
  return root.getDirectoryHandle(OPFS_DIR, { create: true });
}

// downloadPart (OFF while PARTS.PAYLOAD_URL is null): base + part.file into
// the private storage, with progress, then checkFile. The pin is known before
// any byte: a size that is not the pin's stops before the body is read. A
// file already in the private storage with the pin's size is hashed again and
// reused when it matches.
export async function downloadPart(base, part, o) {
  const { fetchFn = fetch, storage = navigator.storage, onProgress = () => {}, onCheck = () => {}, log = () => {} } = o || {};
  if (typeof base !== "string" || !/^https:\/\/[^\s]+\/$|^\/[^\s]*\/$/.test(base)) {
    return { ok: false, why: `the download URL ${JSON.stringify(base)} is not an https folder URL ending in "/"` };
  }
  if (!Number.isSafeInteger(part.size) || part.size <= 0) return { ok: false, why: "this page version pins no size for this part" };
  const dir = await opfsDir(storage);
  try {
    const old = await (await dir.getFileHandle(part.file)).getFile();
    if (old.size === part.size) {
      const c = await checkFile(old, part, onCheck);
      log(`download: ${part.file} in the private storage: ${c.ok ? "reused" : c.why}`);
      if (c.ok) return { ok: true, file: old, reused: true };
    }
  } catch (e) {
    if (!(e && e.name === "NotFoundError")) throw e;
  }
  const r = await fetchFn(base + part.file, { ...OPTS, redirect: "error" });
  if (!r.ok) return { ok: false, why: `HTTP ${r.status}` };
  const len = bodyLength(r);
  if (len && len !== part.size) return { ok: false, why: `the server offers ${len} bytes, the pin says ${part.size}` };
  const fh = await dir.getFileHandle(part.file, { create: true });
  const w = await fh.createWritable();
  let done = 0;
  try {
    const reader = r.body.getReader();
    for (;;) {
      const { value, done: end } = await reader.read();
      if (end) break;
      done += value.byteLength;
      if (done > part.size) throw new DownloadError(`more than ${part.size} bytes`);
      await w.write(value);
      onProgress(done, part.size);
    }
    await w.close();
  } catch (e) {
    try { await w.abort(); } catch { /* ignore */ }
    return { ok: false, why: `the download stopped at ${done} bytes (${e && e.message})` };
  }
  const file = await fh.getFile();
  const c = await checkFile(file, part, onCheck);
  if (!c.ok) return { ok: false, why: c.why };
  return { ok: true, file };
}

// deleteDownloads removes the page's own folder of the private storage (only
// that fixed name; never a drive or a folder the person chose).
export async function deleteDownloads(storage = navigator.storage) {
  const root = await storage.getDirectory();
  try {
    await root.removeEntry(OPFS_DIR, { recursive: true });
  } catch (e) {
    if (!(e && e.name === "NotFoundError")) throw e;
  }
}
