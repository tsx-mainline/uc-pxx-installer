// SPDX-License-Identifier: GPL-2.0-or-later
// step2usb.js: the page's client of the step-2 USB transport (uc-pxx-xfer on
// the running mainline slot; STEP2-USB-TRANSPORT-2026-11-11.md §3.3, §3.5).
// It sends the three files of a normal mainline payload, from the release
// folder the person picked on this computer, to the unit's RAM folder. The
// unit is the authority: it takes a manifest only when the step-2 gate pins
// its sha256, then only the two files the manifest names, with their exact
// sizes, and checks their sha256 itself. Nothing here writes the unit's
// storage: step 2 starts only on the unit (the panel's hold, or the 30 s
// countdown the person may choose here).
//
// Frames: "PXX1", u16 type, u16 flags (0), u32 length, u32 seq, then the JSON
// body (little endian; one USB transfer each; never a multiple of 512 bytes).
// File data: exactly `size` bytes in transfers of DATA_CHUNK (the last one
// shorter). Every transfer goes through usbio.js (never aborted; a short or
// stalled transfer is an error).
import { bulkIn, bulkOut, openInterface } from "./usbio.js";

export const T = Object.freeze({ HELLO: 1, PUT_BEGIN: 2, PUT_END: 4, COMMIT: 5, STATUS: 6, REPLY: 0x80 });
export const HEADER = 16;
export const MAX_BODY = 65536;
export const FRAME_READ = HEADER + MAX_BODY + 512;
export const DATA_CHUNK = 1 << 20;
export const MAX_MANIFEST = 65536;
export const DIR = "uc-pxx-mainline";
export const NAMES = Object.freeze({ manifest: "manifest.json", template: "boot-template.img", rootfs: "rootfs.ext4" });

const enc = new TextEncoder();
const dec = new TextDecoder("utf-8", { fatal: true });
const MAGIC = [0x50, 0x58, 0x58, 0x31]; // "PXX1"

export class Step2Error extends Error {
  constructor(message) {
    super(message);
    this.name = "Step2Error";
  }
}

// encodeFrame: the frame of type, seq and the JSON of obj, padded with spaces
// until its length is not a multiple of 512.
export function encodeFrame(type, seq, obj) {
  let j = JSON.stringify(obj === undefined ? {} : obj);
  let body = enc.encode(j);
  while ((HEADER + body.length) % 512 === 0) {
    j += " ";
    body = enc.encode(j);
  }
  if (body.length > MAX_BODY) throw new Step2Error(`frame body of ${body.length} bytes`);
  const b = new Uint8Array(HEADER + body.length);
  const v = new DataView(b.buffer);
  b.set(MAGIC, 0);
  v.setUint16(4, type, true);
  v.setUint16(6, 0, true);
  v.setUint32(8, body.length, true);
  v.setUint32(12, seq >>> 0, true);
  b.set(body, HEADER);
  return b;
}

// decodeFrame: one reply transfer. Anything but a whole, well-formed frame is
// an error.
export function decodeFrame(u) {
  if (!(u instanceof Uint8Array) || u.length < HEADER) throw new Step2Error("reply shorter than a frame header");
  for (let i = 0; i < 4; i++) if (u[i] !== MAGIC[i]) throw new Step2Error("reply without the PXX1 magic");
  const v = new DataView(u.buffer, u.byteOffset, u.byteLength);
  const type = v.getUint16(4, true), flags = v.getUint16(6, true), len = v.getUint32(8, true), seq = v.getUint32(12, true);
  if (flags !== 0) throw new Step2Error("reply flags not 0");
  if (len > MAX_BODY || u.length !== HEADER + len) throw new Step2Error(`reply of ${u.length} bytes for a body of ${len}`);
  let body;
  try {
    body = JSON.parse(dec.decode(u.subarray(HEADER)));
  } catch (e) {
    throw new Step2Error(`reply body is not JSON: ${e && e.message}`);
  }
  if (body === null || typeof body !== "object" || Array.isArray(body)) throw new Step2Error("reply body is not an object");
  return { type, seq, body };
}

async function sha256hex(buf) {
  const d = new Uint8Array(await crypto.subtle.digest("SHA-256", buf));
  return Array.from(d, (x) => x.toString(16).padStart(2, "0")).join("");
}

// findPayloads: the candidate payload folders among the picked files (each a
// folder named uc-pxx-mainline that holds the three files). files: File
// objects with webkitRelativePath (an <input webkitdirectory>).
export function findPayloads(files) {
  const dirs = new Map();
  for (const f of files) {
    const p = f.webkitRelativePath || f.name;
    const i = p.lastIndexOf("/");
    const dir = i < 0 ? "" : p.slice(0, i), base = p.slice(i + 1);
    const leaf = dir.slice(dir.lastIndexOf("/") + 1);
    if (leaf !== DIR) continue;
    if (!dirs.has(dir)) dirs.set(dir, {});
    const d = dirs.get(dir);
    for (const [k, n] of Object.entries(NAMES)) if (base === n) d[k] = f;
  }
  const out = [];
  for (const [dir, d] of dirs) if (d.manifest && d.template && d.rootfs) out.push({ dir, files: d });
  return out;
}

// checkPayload reads a candidate's manifest (at most 64 KiB) and checks it
// against the page's pins and the picked files' sizes. A cheap early refusal
// only: the unit checks everything again.
export async function checkPayload(cand, pins) {
  const m = cand.files.manifest;
  if (m.size <= 0 || m.size > MAX_MANIFEST) return { ok: false, why: `${cand.dir}/manifest.json has ${m.size} bytes` };
  const bytes = new Uint8Array(await m.arrayBuffer());
  const sha = await sha256hex(bytes);
  const pin = pins.find((p) => p.manifest_sha256 === sha);
  if (!pin) return { ok: false, sha, why: `${cand.dir}: manifest sha256 ${sha} is not a step-2 payload of this page` };
  let j;
  try {
    j = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes));
  } catch {
    return { ok: false, sha, why: `${cand.dir}: the manifest is not JSON` };
  }
  const t = j && j.boot_template, r = j && j.rootfs;
  if (!t || !r || t.file !== NAMES.template || r.file !== NAMES.rootfs) return { ok: false, sha, why: `${cand.dir}: the manifest names other files` };
  if (t.size !== cand.files.template.size || r.size !== cand.files.rootfs.size) {
    return { ok: false, sha, why: `${cand.dir}: the files' sizes are not the manifest's (the download is incomplete?)` };
  }
  return { ok: true, sha, pin, version: j.version, total: m.size + t.size + r.size };
}

// Step2Link is one session with the unit's transport.
export class Step2Link {
  #dev;
  #ep;
  #seq = 0;
  #log;

  constructor(dev, ep, log) {
    this.#dev = dev;
    this.#ep = ep;
    this.#log = log || (() => {});
  }

  // open claims the vendor interface and resyncs the session with
  // SET_INTERFACE: the unit then drops any half-sent file of an earlier
  // session (a closed tab, a cut) and starts a new session.
  static async open(dev, cfg, log) {
    const ep = await openInterface(dev, cfg.cls, cfg.sub, cfg.proto);
    await dev.selectAlternateInterface(ep.iface, ep.alt);
    return new Step2Link(dev, ep, log);
  }

  async #reply(type, seq) {
    const r = decodeFrame(await bulkIn(this.#dev, this.#ep.epIn, FRAME_READ, false));
    if (r.type !== (type | T.REPLY) || r.seq !== seq) {
      throw new Step2Error(`reply type ${r.type} seq ${r.seq}, expected ${type | T.REPLY} seq ${seq}`);
    }
    return r.body;
  }

  async request(type, obj) {
    const seq = ++this.#seq;
    await bulkOut(this.#dev, this.#ep.epOut, encodeFrame(type, seq, obj));
    return this.#reply(type, seq);
  }

  hello() {
    return this.request(T.HELLO, {});
  }

  status() {
    return this.request(T.STATUS, {});
  }

  commit(auto) {
    return this.request(T.COMMIT, { auto: !!auto });
  }

  // putFile sends one file. The unit answers before any byte is sent; a
  // refusal sends nothing. onProgress(sent, size) after each transfer.
  async putFile(name, file, onProgress) {
    const seq = ++this.#seq;
    await bulkOut(this.#dev, this.#ep.epOut, encodeFrame(T.PUT_BEGIN, seq, { name, size: file.size }));
    const a = await this.#reply(T.PUT_BEGIN, seq);
    if (a.accept !== true) {
      this.#log(`step 2: the unit refused ${name}: ${a.reason}`);
      return { accepted: false, reason: String(a.reason || "refused") };
    }
    const slice = (off) => file.slice(off, Math.min(off + DATA_CHUNK, file.size)).arrayBuffer();
    let next = file.size > 0 ? slice(0) : null;
    for (let off = 0; off < file.size;) {
      const chunk = new Uint8Array(await next);
      if (chunk.length === 0) throw new Step2Error(`${name}: the file ended at ${off} of ${file.size} bytes`);
      const after = off + chunk.length;
      next = after < file.size ? slice(after) : null; // read ahead while this transfer runs
      await bulkOut(this.#dev, this.#ep.epOut, chunk);
      off = after;
      if (onProgress) onProgress(off, file.size);
    }
    const e = await this.#reply(T.PUT_END, seq);
    return { accepted: true, ok: e.ok === true, sha256: e.sha256, reason: e.reason ? String(e.reason) : "" };
  }
}

// sendPayload sends the manifest, then the two files, then COMMIT. It stops
// at the first refusal. progress(name, sent, size, doneBytes, totalBytes).
export async function sendPayload(link, cand, auto, progress, log) {
  const order = [[NAMES.manifest, cand.files.manifest], [NAMES.template, cand.files.template], [NAMES.rootfs, cand.files.rootfs]];
  const total = order.reduce((n, [, f]) => n + f.size, 0);
  let done = 0;
  for (const [name, file] of order) {
    const t0 = Date.now();
    const r = await link.putFile(name, file, (s, size) => progress && progress(name, s, size, done + s, total));
    if (!r.accepted) return { ok: false, step: name, why: `the unit refused ${name}: ${r.reason}` };
    if (!r.ok) return { ok: false, step: name, why: `the unit did not take ${name}: ${r.reason}` };
    done += file.size;
    const s = Math.max((Date.now() - t0) / 1000, 0.001);
    if (log) log(`step 2: ${name} sent (${file.size} bytes, ${(file.size / 1e6 / s).toFixed(1)} MB/s), unit sha256 ${r.sha256}`);
  }
  const c = await link.commit(auto);
  if (c.ok !== true) return { ok: false, step: "commit", why: `the unit did not take the payload: ${c.reason}` };
  return { ok: true };
}
