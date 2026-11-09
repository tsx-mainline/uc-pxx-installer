// SPDX-License-Identifier: GPL-2.0-or-later
// fastboot.js: the three fastboot commands the page needs, over WebUSB:
// single getvar (from a fixed list), download, boot. Nothing else can be sent:
// no getvar all (it wedges the U-Boot USB of this unit), no flash, no erase,
// no oem, no reboot, no set_active.
import { bulkIn, bulkOut, openInterface, TransferError } from "./usbio.js";
import { sha256hex } from "./bcb.js";

export const VID = 0x18d1;
export const PID = 0xd00d;

// The variables measured on this U-Boot one by one (design §10 item 2), and
// product (queried by p10-bcb-fastboot.sh), without serialno (the page logs
// no serial number).
export const GETVARS = Object.freeze([
  "product",
  "current-slot",
  "slot-successful:a",
  "slot-successful:b",
  "slot-unbootable:a",
  "slot-unbootable:b",
  "slot-retry-count:a",
  "slot-retry-count:b",
  "max-download-size",
]);

const CHUNK = 256 * 1024;

export class FastbootFail extends Error {
  constructor(message) {
    super(message);
    this.name = "FastbootFail";
  }
}

// commandAllowed is the guard: the exact command strings the page may send.
export function commandAllowed(cmd) {
  if (typeof cmd !== "string") return false;
  if (cmd === "boot") return true;
  if (/^download:[0-9a-f]{8}$/.test(cmd)) return true;
  if (cmd.startsWith("getvar:")) return GETVARS.includes(cmd.slice(7));
  return false;
}

// parseSize reads max-download-size as AOSP fastboot does: hex, "0x" optional.
export function parseSize(s) {
  const t = String(s).trim().toLowerCase().replace(/^0x/, "");
  return /^[0-9a-f]{1,8}$/.test(t) ? parseInt(t, 16) : NaN;
}

export class Fastboot {
  #dev;
  #ep;
  #busy = false;
  #log;

  constructor(dev, ep, log) {
    this.#dev = dev;
    this.#ep = ep;
    this.#log = log || (() => {});
  }

  static async open(dev, log) {
    if (dev.vendorId !== VID || dev.productId !== PID) throw new Error("fastboot: not 18d1:d00d");
    const ep = await openInterface(dev, 0xff, 0x42, 0x03);
    return new Fastboot(dev, ep, log);
  }

  async #response() {
    for (;;) {
      const r = await bulkIn(this.#dev, this.#ep.epIn, 64, false);
      const s = new TextDecoder().decode(r);
      const status = s.slice(0, 4);
      const text = s.slice(4);
      if (status === "INFO" || status === "TEXT") {
        this.#log(`fastboot ${status} ${text}`);
        continue;
      }
      if (status === "OKAY" || status === "DATA") return { status, text };
      if (status === "FAIL") throw new FastbootFail(`fastboot FAIL ${text}`);
      throw new TransferError("short", `fastboot: unknown response ${JSON.stringify(s.slice(0, 16))}`);
    }
  }

  async #command(cmd) {
    if (!commandAllowed(cmd)) throw new Error(`fastboot: command ${JSON.stringify(cmd)} refused`);
    return bulkOut(this.#dev, this.#ep.epOut, new TextEncoder().encode(cmd)).then(() => this.#response());
  }

  async #exclusive(fn) {
    if (this.#busy) throw new Error("fastboot: a command is still running");
    this.#busy = true;
    try {
      return await fn();
    } finally {
      this.#busy = false;
    }
  }

  // getvar returns the value, or null when U-Boot answers FAIL.
  async getvar(name) {
    if (!GETVARS.includes(name)) throw new Error(`fastboot: getvar ${JSON.stringify(name)} refused`);
    return this.#exclusive(async () => {
      try {
        const r = await this.#command(`getvar:${name}`);
        this.#log(`fastboot getvar ${name}: ${r.text}`);
        return r.text.trim();
      } catch (e) {
        if (e instanceof FastbootFail) {
          this.#log(`fastboot getvar ${name}: FAIL`);
          return null;
        }
        throw e;
      }
    });
  }

  // bootImage checks the sha of the exact bytes it sends, sends them, and
  // starts them. It never writes a partition.
  async bootImage(bytes, expectedSha, maxDownload, onProgress) {
    if (!(bytes instanceof Uint8Array) || bytes.length === 0) throw new Error("fastboot: no image");
    const sha = await sha256hex(bytes);
    if (sha !== expectedSha) throw new Error(`fastboot: image sha256 ${sha} is not ${expectedSha}`);
    if (!(maxDownload >= bytes.length)) throw new Error("fastboot: image larger than max-download-size");
    this.#log(`fastboot download ${bytes.length} bytes, sha256 ${sha}`);
    return this.#exclusive(async () => {
      const r = await this.#command(`download:${bytes.length.toString(16).padStart(8, "0")}`);
      if (r.status !== "DATA" || parseInt(r.text, 16) !== bytes.length) {
        throw new FastbootFail(`fastboot: download answer ${r.status}${r.text}`);
      }
      for (let off = 0; off < bytes.length; off += CHUNK) {
        await bulkOut(this.#dev, this.#ep.epOut, bytes.subarray(off, Math.min(off + CHUNK, bytes.length)));
        if (onProgress) onProgress(Math.min(off + CHUNK, bytes.length), bytes.length);
      }
      const done = await this.#response();
      if (done.status !== "OKAY") throw new FastbootFail(`fastboot: download end ${done.status}`);
      this.#log("fastboot download OKAY; sending boot");
      const b = await this.#command("boot");
      if (b.status !== "OKAY") throw new FastbootFail(`fastboot: boot answer ${b.status}`);
      this.#log("fastboot boot OKAY");
    });
  }
}
