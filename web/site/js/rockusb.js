/*
 * rockusb.js: a small rockusb client for WebUSB. It can read two fixed
 * places of the UC-P10 eMMC, write one fixed place (the BCB) with one fixed
 * payload, and reset the unit. Nothing else.
 *
 * This file is a port of a subset of rkdeveloptool
 *   https://github.com/rockchip-linux/rkdeveloptool
 *   commit 304f073752fd25c854e1bcf05d8e7f925b1f4e14
 *   files RKComm.h, RKComm.cpp (InitializeCBW, RKU_ReadLBA, RKU_WriteLBA,
 *   RKU_ResetDevice, UFI_CHECK_SIGN) and RKScan.cpp (the Maskrom/Loader test)
 *   (C) Copyright 2017 Fuzhou Rockchip Electronics Co., Ltd, Seth Liu.
 *   SPDX-License-Identifier: GPL-2.0+
 *
 * Changes (2026-10-29, the tsx-mainline contributors): ported from C++ and
 * libusb to JavaScript and WebUSB; only READ_LBA, WRITE_LBA and DEVICE_RESET
 * are kept; reads are limited to LBA 49156 (1 sector), the BCB (49184, 4
 * sectors), the primary GPT (0, 34 sectors) and the factory block (3666944, 8
 * sectors); the only write is the BCB with the reviewed payload; the CSW
 * residue is checked too; no retry and no buffer clearing after an error.
 * (2026-11-08, --plain-entry: the read-only GPT and factory reads were added so
 * the page can gate the layout and the model as the reviewed CLI does; the
 * write allow-list is byte-unchanged.)
 *
 * Copyright (C) 2017 Fuzhou Rockchip Electronics Co., Ltd
 * Copyright (C) 2026 the tsx-mainline contributors
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 2 of the License, or (at your option)
 * any later version. It is distributed WITHOUT ANY WARRANTY; see the GNU
 * General Public License (LICENSE.txt next to this page) for details.
 *
 * SPDX-License-Identifier: GPL-2.0-or-later
 */
import { bulkIn, bulkOut, openInterface, TransferError } from "./usbio.js";
import { PAYLOAD_SHA256, sha256hex } from "./bcb.js";

export const VID = 0x2207;
export const PID = 0x330c;
export const SECTOR = 512;

// LBA 49156: the A/B metadata. READ ONLY. No code path writes it.
export const AB_LBA = 49156;
// LBA 49184..49187: the BCB. The only place this file writes.
export const BCB_LBA = 49184;
export const BCB_SECTORS = 4;
// The primary GPT (0..33) and the factory block (3666944, 8 sectors). READ
// ONLY: the page reads them only to gate the layout and the model.
export const GPT_LBA = 0;
export const GPT_SECTORS = 34;
export const FACTORY_LBA = 3666944;
export const FACTORY_SECTORS = 8;

// The only reads: [lba, sectors].
const READS = Object.freeze([
  Object.freeze([AB_LBA, 1]),
  Object.freeze([BCB_LBA, BCB_SECTORS]),
  Object.freeze([GPT_LBA, GPT_SECTORS]),
  Object.freeze([FACTORY_LBA, FACTORY_SECTORS]),
]);

export const OP = Object.freeze({ READ_LBA: 0x14, WRITE_LBA: 0x15, DEVICE_RESET: 0xff });

const CBW_SIGN = 0x43425355; // "USBC"
const CSW_SIGN = 0x53425355; // "USBS"
const CBW_LEN = 31;
const CSW_LEN = 13;

// deviceMode: RKScan.cpp types a device as Maskrom when bit 0 of bcdUSB is 0,
// else Loader. On this unit the U-Boot download mode also has bcdDevice
// 0x9999 (ucp10/tools/p10-wait-rockusb.sh). Only "loader" may go on.
export function deviceMode(dev) {
  if (dev.vendorId !== VID || dev.productId !== PID) return { mode: "other", why: "not 2207:330c" };
  if ((dev.usbVersionSubminor & 1) === 0) {
    return { mode: "maskrom", why: `bcdUSB ${dev.usbVersionMajor}.${dev.usbVersionMinor}${dev.usbVersionSubminor} has bit 0 clear` };
  }
  const bcd = (dev.deviceVersionMajor << 8) | (dev.deviceVersionMinor << 4) | dev.deviceVersionSubminor;
  if (bcd !== 0x9999) return { mode: "unknown", why: `bcdDevice 0x${bcd.toString(16)} is not 0x9999` };
  return { mode: "loader", why: "bcdUSB bit 0 set, bcdDevice 0x9999" };
}

// buildCbw is InitializeCBW plus the address and length fields. It is the
// guard of this file: it refuses any write that is not the BCB and any read
// that is not in READS.
export function buildCbw(op, lba, count, tag) {
  if (op === OP.WRITE_LBA) {
    if (lba !== BCB_LBA || count !== BCB_SECTORS) throw new Error(`rockusb: write of LBA ${lba}+${count} refused`);
    if (lba <= AB_LBA && AB_LBA < lba + count) throw new Error("rockusb: a write over the A/B sector is refused");
  } else if (op === OP.READ_LBA) {
    if (!READS.some(([l, c]) => l === lba && c === count)) throw new Error(`rockusb: read of LBA ${lba}+${count} refused`);
  } else if (op === OP.DEVICE_RESET) {
    if (lba !== 0 || count !== 0) throw new Error("rockusb: reset takes no address");
  } else {
    throw new Error(`rockusb: opcode 0x${Number(op).toString(16)} refused`);
  }
  const b = new Uint8Array(CBW_LEN);
  const v = new DataView(b.buffer);
  v.setUint32(0, CBW_SIGN, true);
  v.setUint32(4, tag >>> 0, true);
  v.setUint32(8, op === OP.DEVICE_RESET ? 0 : count * SECTOR, true);
  b[12] = op === OP.READ_LBA ? 0x80 : 0x00; // ucCBWFlags
  b[13] = 0; // LUN
  b[14] = op === OP.DEVICE_RESET ? 0x06 : 0x0a; // ucCBWCBLength
  b[15] = op; // ucOperCode
  b[16] = 0; // ucReserved: RWMETHOD_IMAGE / RST_NONE_SUBCODE
  v.setUint32(17, lba, false); // dwAddress, big-endian
  b[21] = 0;
  v.setUint16(22, count, false); // usLength, big-endian
  return b;
}

function newTag() {
  const t = new Uint32Array(1);
  crypto.getRandomValues(t);
  return t[0];
}

function checkCsw(csw, tag) {
  const v = new DataView(csw.buffer, csw.byteOffset, csw.byteLength);
  if (csw.length !== CSW_LEN || v.getUint32(0, true) !== CSW_SIGN || v.getUint32(4, true) !== (tag >>> 0)) {
    throw new TransferError("short", "rockusb: CSW signature or tag does not match");
  }
  if (v.getUint32(8, true) !== 0) throw new TransferError("short", `rockusb: CSW residue ${v.getUint32(8, true)}`);
  if (csw[12] !== 0) throw new TransferError("stall", `rockusb: command failed (CSW status ${csw[12]})`);
}

export class RockUsb {
  #dev;
  #ep;
  #busy = false;
  #log;

  constructor(dev, ep, log) {
    this.#dev = dev;
    this.#ep = ep;
    this.#log = log || (() => {});
  }

  // open checks the mode first: a Maskrom device is never opened.
  static async open(dev, log) {
    const m = deviceMode(dev);
    if (m.mode !== "loader") throw new Error(`rockusb: device mode ${m.mode} (${m.why})`);
    const ep = await openInterface(dev, 0xff, 0x06, 0x05);
    return new RockUsb(dev, ep, log);
  }

  // #transact runs one command: CBW, data, CSW. One at a time, never cut short.
  async #transact(op, lba, count, dataOut, resetting) {
    if (this.#busy) throw new Error("rockusb: a command is still running");
    this.#busy = true;
    try {
      const tag = newTag();
      const cbw = buildCbw(op, lba, count, tag);
      if ((op === OP.WRITE_LBA) !== (dataOut !== null)) throw new Error("rockusb: data only with a write");
      if (dataOut && dataOut.length !== count * SECTOR) throw new Error("rockusb: write data has the wrong length");
      await bulkOut(this.#dev, this.#ep.epOut, cbw);
      let data = null;
      if (op === OP.READ_LBA) data = await bulkIn(this.#dev, this.#ep.epIn, count * SECTOR, true);
      if (op === OP.WRITE_LBA) await bulkOut(this.#dev, this.#ep.epOut, dataOut);
      let csw;
      try {
        csw = await bulkIn(this.#dev, this.#ep.epIn, CSW_LEN, true);
      } catch (e) {
        // A reset may leave the bus before its CSW. Only then is that fine.
        if (resetting && e instanceof TransferError && e.kind === "interrupted") return "gone";
        throw e;
      }
      checkCsw(csw, tag);
      return data;
    } finally {
      this.#busy = false;
    }
  }

  async readAbSector() {
    const d = await this.#transact(OP.READ_LBA, AB_LBA, 1, null, false);
    this.#log(`rockusb read LBA ${AB_LBA} x1: ${await sha256hex(d)}`);
    return d;
  }

  async readBcb() {
    const d = await this.#transact(OP.READ_LBA, BCB_LBA, BCB_SECTORS, null, false);
    this.#log(`rockusb read LBA ${BCB_LBA} x${BCB_SECTORS}: ${await sha256hex(d)}`);
    return d;
  }

  // readGpt reads the primary GPT (LBA 0..33). Read only. The page gates the
  // layout from it; it is not unit-identifying, so its sha is logged.
  async readGpt() {
    const d = await this.#transact(OP.READ_LBA, GPT_LBA, GPT_SECTORS, null, false);
    this.#log(`rockusb read LBA ${GPT_LBA} x${GPT_SECTORS} (GPT): ${await sha256hex(d)}`);
    return d;
  }

  // readFactory reads the factory block (cfg, LBA 3666944, 8 sectors). Read
  // only. It holds the unit's MAC address: its bytes stay in memory and its
  // sha256 (a fingerprint of the unit) is NOT logged (only the model is shown).
  async readFactory() {
    const d = await this.#transact(OP.READ_LBA, FACTORY_LBA, FACTORY_SECTORS, null, false);
    this.#log(`rockusb read LBA ${FACTORY_LBA} x${FACTORY_SECTORS} (factory block; sha not logged: it holds the MAC)`);
    return d;
  }

  // writeBcb writes the reviewed payload, and nothing else, to the BCB.
  async writeBcb(payload) {
    if (!(payload instanceof Uint8Array) || payload.length !== BCB_SECTORS * SECTOR) {
      throw new Error("rockusb: BCB payload must be 2048 bytes");
    }
    if ((await sha256hex(payload)) !== PAYLOAD_SHA256) throw new Error("rockusb: BCB payload is not the reviewed payload");
    this.#log(`rockusb write LBA ${BCB_LBA} x${BCB_SECTORS}: ${PAYLOAD_SHA256}`);
    await this.#transact(OP.WRITE_LBA, BCB_LBA, BCB_SECTORS, payload, false);
  }

  // resetNormal is "rkdeveloptool rd" (subcode 0): a normal restart.
  async resetNormal() {
    this.#log("rockusb reset (normal restart)");
    const r = await this.#transact(OP.DEVICE_RESET, 0, 0, null, true);
    if (r === "gone") this.#log("rockusb: the unit left the bus before its CSW (normal for a reset)");
  }
}
