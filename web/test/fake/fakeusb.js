// SPDX-License-Identifier: GPL-2.0-or-later
// fakeusb.js: a fake navigator.usb and navigator.serial for the tests. It is a
// classic script (no import/export): Playwright injects it before the page
// loads, and it replaces the browser's own WebUSB and Web Serial, so a test
// can never reach a real device.
//
// The fake unit has an eMMC (a sparse map of 512-byte sectors), a U-Boot
// rockusb mode (CBW/CSW: READ_LBA, WRITE_LBA, DEVICE_RESET), a U-Boot fastboot
// mode, and after "boot" the installer's CDC-ACM shell. Every transfer is
// recorded in __fake.rec, and every broken rule in __fake.violations.
(function () {
  "use strict";
  const G = globalThis;
  const rec = [];
  const violations = [];
  const AB_LBA = 49156;
  const BCB_LBA = 49184;

  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const tick = () => Promise.resolve();

  function u8(data) {
    if (data instanceof ArrayBuffer) return new Uint8Array(data.slice(0));
    return new Uint8Array(data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength));
  }
  function hex(b) {
    return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
  }
  function fromHex(s) {
    const o = new Uint8Array(s.length / 2);
    for (let i = 0; i < o.length; i++) o[i] = parseInt(s.substr(2 * i, 2), 16);
    return o;
  }
  const crcTable = (() => {
    const t = [];
    for (let n = 0; n < 256; n++) {
      let c = n;
      for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
      t.push(c >>> 0);
    }
    return t;
  })();
  function crc32(b) {
    let c = 0xffffffff;
    for (const x of b) c = crcTable[(c ^ x) & 0xff] ^ (c >>> 8);
    return (c ^ 0xffffffff) >>> 0;
  }
  // abSector builds a valid AvbABData sector; crc "le" stores the CRC little-endian.
  function abSector(a, b, crc) {
    const s = new Uint8Array(512);
    s.set([0, 0x41, 0x42, 0x30, 1, 0, 0, 0], 0);
    s.set([a[0], a[1], a[2], 0], 8);
    s.set([b[0], b[1], b[2], 0], 12);
    const c = crc32(s.subarray(0, 28));
    new DataView(s.buffer).setUint32(28, crc === "bad" ? c ^ 1 : c, crc === "le");
    return s;
  }
  function pick(sec) {
    const sl = [0, 1].map((i) => ({ p: sec[8 + 4 * i], t: sec[9 + 4 * i], s: sec[10 + 4 * i] }));
    const ok = sl.map((x) => x.p > 0 && (x.s === 1 || x.t > 0));
    if (ok[0] && ok[1]) return sl[1].p > sl[0].p ? 1 : 0;
    return ok[0] ? 0 : ok[1] ? 1 : -1;
  }
  // plainReset: U-Boot's BCB-route A/B scribble as libavb resets it (the benign
  // both-trying reset), as MEASURED on the hardware (2026-11-08, both runs, one
  // from D3 where slot B ran): libavb finds bad magic and writes its defaults,
  // A priority 15 and B priority 14, both trying with MAX_TRIES tries, then
  // boots slot A, which takes one try -> A{15,6,0} B{14,7,0}, whatever the
  // pre-entry state was. fastboot then reports current-slot a, retry a 6 b 7.
  function plainReset() {
    const MAX = 7;
    return abSector([15, MAX - 1, 0], [14, MAX, 0]);
  }
  // buildGpt makes a valid primary GPT (LBA 0..33) with the given partitions.
  // parts: [[name, firstLba, sectors], ...]. The parser in partmap.js checks the
  // signature, both CRC32s, the entry size and the primary-header flag.
  function buildGpt(parts) {
    const SEC = 512, N = 128, ESZ = 128;
    const data = new Uint8Array(34 * SEC);
    const dv = new DataView(data.buffer);
    const ents = new Uint8Array(N * ESZ);
    const edv = new DataView(ents.buffer);
    parts.forEach(([name, first, sectors], i) => {
      const o = i * ESZ;
      ents[o] = 0xa5; // a non-zero type GUID
      ents[o + 16] = 0x5a; // a non-zero unique GUID
      edv.setUint32(o + 32, first >>> 0, true);
      edv.setUint32(o + 36, Math.floor(first / 0x100000000), true);
      const last = first + sectors - 1;
      edv.setUint32(o + 40, last >>> 0, true);
      edv.setUint32(o + 44, Math.floor(last / 0x100000000), true);
      for (let k = 0; k < name.length; k++) edv.setUint16(o + 56 + 2 * k, name.charCodeAt(k), true);
    });
    data.set(ents, 2 * SEC);
    const h = new Uint8Array(92);
    const hv = new DataView(h.buffer);
    h.set(new TextEncoder().encode("EFI PART"), 0);
    hv.setUint32(8, 0x00010000, true); // revision
    hv.setUint32(12, 92, true); // header size
    hv.setUint32(24, 1, true); // my LBA = 1 (primary)
    hv.setUint32(32, 33, true); // alt LBA
    hv.setUint32(40, 34, true); // first usable
    hv.setUint32(48, 33, true); // last usable (toy value)
    hv.setUint32(72, 2, true); // entries LBA
    hv.setUint32(80, N, true); // number of entries
    hv.setUint32(84, ESZ, true); // entry size
    hv.setUint32(88, crc32(ents), true); // entry array CRC32
    hv.setUint32(16, crc32(h), true); // header CRC32 (CRC field already 0)
    data.set(h, SEC);
    return data;
  }
  const DEFAULT_GPT = () => buildGpt([
    ["uboot", 16384, 8192], ["misc", 49152, 8192], ["boot_a", 106496, 65536],
    ["cfg", 3666944, 262144], ["userdata", 3932160, 100000],
  ]);
  // buildFactory makes a 4096-byte (8-sector) factory block with one tag-650
  // model record and the correct checksum (the rule of factory.js).
  function buildFactory(model) {
    const data = new Uint8Array(8 * 512);
    const dv = new DataView(data.buffer);
    const mb = new TextEncoder().encode(model);
    const words = Math.ceil(mb.length / 2);
    data.set([0x41, 0x30, 0x30], 40); // hw rev "A00" -> VA.00 (printable)
    dv.setUint16(90, 1, true); // one record
    dv.setUint16(92, 650, true); // tag 650 = model
    dv.setUint16(94, words, true);
    data.set(mb, 96);
    const L = 96 + 2 * words; // even
    dv.setUint32(0, L, true);
    let total = 0;
    for (let i = 0; i < L; i += 2) if (i !== 4) total = (total + ((data[i] << 8) | data[i + 1])) & 0xffffffff;
    let want = (-total) & 0xffff;
    if (want === 0) { data[L - 1] = 1; total = (total + 1) & 0xffffffff; want = (-total) & 0xffff; }
    dv.setUint16(4, want, false); // big-endian checksum
    return data;
  }

  class Emmc {
    constructor() {
      this.sectors = new Map();
    }
    read(lba, n) {
      const o = new Uint8Array(512 * n);
      for (let i = 0; i < n; i++) {
        const s = this.sectors.get(lba + i);
        if (s) o.set(s, 512 * i);
      }
      return o;
    }
    write(lba, data) {
      for (let i = 0; i < data.length / 512; i++) this.sectors.set(lba + i, data.slice(512 * i, 512 * (i + 1)));
    }
  }

  function netErr() {
    return new DOMException("A transfer error has occurred.", "NetworkError");
  }

  class FakeDevice {
    constructor(env, o) {
      this.env = env;
      this.name = o.name;
      this.vendorId = o.vendorId;
      this.productId = o.productId;
      this.usbVersionMajor = o.bcdUSB >> 8;
      this.usbVersionMinor = (o.bcdUSB >> 4) & 0xf;
      this.usbVersionSubminor = o.bcdUSB & 0xf;
      this.deviceVersionMajor = o.bcdDevice >> 8;
      this.deviceVersionMinor = (o.bcdDevice >> 4) & 0xf;
      this.deviceVersionSubminor = o.bcdDevice & 0xf;
      this.productName = o.name;
      this.manufacturerName = "fake";
      this.serialNumber = o.serial === undefined ? "FAKE-SERIAL-0001" : o.serial;
      this.opened = false;
      this.configuration = null;
      this.configurations = [{
        configurationValue: 1,
        interfaces: [{
          interfaceNumber: 0, claimed: false,
          alternates: [{
            alternateSetting: 0, interfaceClass: o.cls[0], interfaceSubclass: o.cls[1], interfaceProtocol: o.cls[2],
            endpoints: [
              { endpointNumber: 1, direction: "in", type: "bulk", packetSize: 512 },
              { endpointNumber: 2, direction: "out", type: "bulk", packetSize: 512 },
            ],
          }],
        }],
      }];
      this.inQueue = [];
      this.waiters = [];
      this.inflight = 0;
      this.gone = false;
      this.claimed = false;
      this.openError = o.openError || null;
    }
    r(e) {
      rec.push(Object.assign({ n: rec.length, dev: this.name }, e));
    }
    async open() {
      this.r({ op: "open" });
      if (this.gone) throw new DOMException("The device was disconnected.", "NotFoundError");
      if (this.openError) throw new DOMException("Access denied.", this.openError);
      this.opened = true;
    }
    async close() {
      this.r({ op: "close", inflight: this.inflight });
      if (this.inflight > 0) violations.push(`${this.name}: close() during a transfer`);
      this.opened = false;
    }
    async selectConfiguration() {
      this.configuration = this.configurations[0];
    }
    async claimInterface(n) {
      this.r({ op: "claim", iface: n });
      this.claimed = true;
    }
    async releaseInterface() {}
    async reset() {
      this.r({ op: "usb-reset" });
      violations.push(`${this.name}: USBDevice.reset() called`);
    }
    async forget() {}
    async clearHalt() {}
    async selectAlternateInterface(i, a) {
      this.r({ op: "set-alt", iface: i, alt: a });
      if (!this.claimed) violations.push(`${this.name}: SET_INTERFACE before the claim`);
      if (this.onSetAlt) this.onSetAlt();
    }
    async controlTransferIn() {
      violations.push(`${this.name}: control transfer`);
      throw netErr();
    }
    async controlTransferOut() {
      violations.push(`${this.name}: control transfer`);
      throw netErr();
    }
    queue(bytes) {
      const w = this.waiters.shift();
      if (w) w.resolve(bytes);
      else this.inQueue.push(bytes);
    }
    async transferOut(ep, data) {
      if (!this.opened || !this.claimed) violations.push(`${this.name}: transferOut on a closed device`);
      if (this.gone) throw netErr();
      const b = u8(data);
      this.inflight++;
      try {
        await tick();
        if (this.gone) throw netErr();
        return await this.onOut(b);
      } finally {
        this.inflight--;
      }
    }
    async transferIn(ep, length) {
      if (!this.opened || !this.claimed) violations.push(`${this.name}: transferIn on a closed device`);
      if (this.gone) throw netErr();
      this.inflight++;
      try {
        const b = this.inQueue.length ? this.inQueue.shift() : await new Promise((resolve, reject) => this.waiters.push({ resolve, reject }));
        if (b.length > length) {
          this.r({ op: "in", length, babble: b.length });
          return { status: "babble", data: new DataView(b.buffer, 0, length) };
        }
        this.r({ op: "in", length, got: b.length });
        if (b.bogus) return { status: "ok", data: { byteLength: b.length } }; // not a DataView: a page bug path
        return { status: "ok", data: new DataView(b.buffer) };
      } finally {
        this.inflight--;
      }
    }
    unplug() {
      if (this.gone) return;
      this.gone = true;
      this.opened = false;
      this.r({ op: "unplug" });
      for (const w of this.waiters.splice(0)) w.reject(netErr());
      this.env.usb.detach(this);
    }
  }

  // ---- the rockusb mode of U-Boot ----
  function makeRockusb(env, sc, extra) {
    const maskrom = sc.start === "maskrom";
    const d = new FakeDevice(env, {
      name: extra ? "rockusb-2" : "rockusb", vendorId: 0x2207, productId: 0x330c,
      bcdUSB: maskrom ? 0x0200 : 0x0201, bcdDevice: maskrom ? 0x0100 : sc.bcdDevice || 0x9999, cls: [0xff, 6, 5],
      openError: sc.openError, serial: sc.rockusbSerial,
    });
    let pending = null; // a write that waits for its data
    let reads = 0;
    const f = sc.fault || "";
    function csw(tag, status) {
      const c = new Uint8Array(13);
      const v = new DataView(c.buffer);
      v.setUint32(0, 0x53425355, true);
      v.setUint32(4, sc.fault === "badCswTag" ? tag ^ 1 : tag, true);
      c[12] = status || 0;
      return c;
    }
    d.onOut = async (b) => {
      if (pending) {
        const p = pending;
        p.got.push(b);
        p.have += b.length;
        d.r({ op: "out-data", length: b.length, lba: p.lba });
        if (f === "disconnectDuringWrite") {
          d.unplug();
          throw netErr();
        }
        if (p.have < p.need) return { status: "ok", bytesWritten: b.length };
        pending = null;
        if (p.have !== p.need) violations.push(`rockusb: write data ${p.have} != ${p.need}`);
        const all = new Uint8Array(p.have);
        let o = 0;
        for (const x of p.got) {
          all.set(x, o);
          o += x.length;
        }
        if (f === "corruptWrite") all[100] ^= 0x55;
        if (f !== "ignoreWrite") {
          env.hostWrites.push({ lba: p.lba, sectors: all.length / 512 });
          env.emmc.write(p.lba, all);
        }
        if (f === "abChangesOnWrite") {
          env.abOwnChange = true;
          env.emmc.write(AB_LBA, abSector([15, 7, 0], [14, 0, 1]));
        }
        d.queue(csw(p.tag));
        return { status: "ok", bytesWritten: b.length };
      }
      if (b.length !== 31) {
        violations.push(`rockusb: OUT of ${b.length} bytes where a CBW was due`);
        return { status: "stall", bytesWritten: 0 };
      }
      const v = new DataView(b.buffer);
      const e = {
        op: "cbw", sig: v.getUint32(0, true), tag: v.getUint32(4, true), len: v.getUint32(8, true), flags: b[12],
        cblen: b[14], opcode: b[15], sub: b[16], lba: v.getUint32(17, false), count: v.getUint16(22, false),
      };
      d.r(e);
      if (e.sig !== 0x43425355) violations.push("rockusb: bad CBW signature");
      if (e.sub !== 0 || b[13] !== 0 || b[21] !== 0 || b.subarray(24).some((x) => x !== 0)) violations.push("rockusb: CBW subcode or reserved bytes not 0");
      if (e.opcode === 0x14) {
        if (e.flags !== 0x80 || e.cblen !== 0x0a || e.len !== e.count * 512) violations.push("rockusb: bad READ_LBA CBW");
        reads++;
        if (f === "disconnectDuringRead") {
          setTimeout(() => d.unplug(), 0);
          return { status: "ok", bytesWritten: 31 };
        }
        if (f === "holdFirstRead" && reads === 1) {
          env.release = () => {
            d.queue(env.emmc.read(e.lba, e.count));
            d.queue(csw(e.tag));
          };
          return { status: "ok", bytesWritten: 31 };
        }
        if (f === "badDataAfterWrite" && env.hostWrites.length) {
          d.queue({ length: e.count * 512, bogus: true });
          return { status: "ok", bytesWritten: 31 };
        }
        d.queue(env.emmc.read(e.lba, e.count));
        d.queue(csw(e.tag));
        if (sc.secondAfterRead && reads === 2) {
          const d2 = makeRockusb(env, Object.assign({}, sc, { start: "rockusb", fault: "", secondAfterRead: false }), true);
          env.usb.permitted.add(env.usb.key(d2));
          env.usb.attach(d2);
        }
        // read 4 is readState's BCB read (after the GPT, factory and A/B reads);
        // corrupt the BCB after it so writeBootCommand's re-read sees the change.
        if (f === "changeBcbAfterFirstRead" && reads === 4) {
          const x = env.emmc.read(BCB_LBA, 4);
          x[2000] ^= 1;
          env.emmc.write(BCB_LBA, x);
        }
      } else if (e.opcode === 0x15) {
        if (e.flags !== 0x00 || e.cblen !== 0x0a || e.len !== e.count * 512) violations.push("rockusb: bad WRITE_LBA CBW");
        if (e.lba <= AB_LBA && AB_LBA < e.lba + e.count) violations.push("rockusb: WRITE over LBA 49156");
        if (e.lba !== BCB_LBA || e.count !== 4) violations.push(`rockusb: WRITE of LBA ${e.lba}+${e.count}`);
        pending = { lba: e.lba, need: e.count * 512, have: 0, got: [], tag: e.tag };
      } else if (e.opcode === 0xff) {
        if (e.flags !== 0 || e.cblen !== 6 || e.len !== 0) violations.push("rockusb: bad DEVICE_RESET CBW");
        if (f === "resetOutFails") {
          d.unplug();
          throw netErr();
        }
        if (f !== "resetNoCsw") d.queue(csw(e.tag));
        setTimeout(() => {
          d.unplug();
          env.ubootRestart();
        }, 20);
      } else {
        violations.push(`rockusb: opcode 0x${e.opcode.toString(16)}`);
        d.queue(csw(e.tag, 1));
      }
      return { status: "ok", bytesWritten: 31 };
    };
    return d;
  }

  // ---- the fastboot mode of U-Boot ----
  function makeFastboot(env, sc) {
    const d = new FakeDevice(env, { name: "fastboot", vendorId: 0x18d1, productId: 0xd00d, bcdUSB: 0x0200, bcdDevice: 0x0100, cls: [0xff, 0x42, 3],
      serial: sc.fastbootSerial });
    let dl = null;
    const f = sc.fault || "";
    const resp = (s) => d.queue(new TextEncoder().encode(s));
    function getvar(name) {
      const ab = env.emmc.read(AB_LBA, 1);
      const i = pick(ab);
      const slot = (x) => ({ p: ab[8 + 4 * x], t: ab[9 + 4 * x], s: ab[10 + 4 * x] });
      if ((sc.getvarFail || []).includes(name)) return null;
      if (sc.getvarOverride && name in sc.getvarOverride) return sc.getvarOverride[name];
      switch (name) {
        case "product": return sc.product || "evb_rk3399";
        case "current-slot": return i < 0 ? null : "ab"[i];
        case "slot-successful:a": return slot(0).s ? "yes" : "no";
        case "slot-successful:b": return slot(1).s ? "yes" : "no";
        case "slot-retry-count:a": return String(slot(0).t);
        case "slot-retry-count:b": return String(slot(1).t);
        case "slot-unbootable:a": return slot(0).p ? "no" : "yes";
        case "slot-unbootable:b": return slot(1).p ? "no" : "yes";
        case "max-download-size": return sc.maxDownload || "0x08000000";
        default: return null;
      }
    }
    d.onOut = async (b) => {
      if (dl) {
        dl.have += b.length;
        dl.chunks.push(b);
        d.r({ op: "out-data", length: b.length });
        if (f === "disconnectDuringDownload" && dl.have >= dl.need / 2) {
          d.unplug();
          throw netErr();
        }
        if (dl.have > dl.need) violations.push("fastboot: more data than announced");
        if (dl.have >= dl.need) {
          const all = new Uint8Array(dl.have);
          let o = 0;
          for (const x of dl.chunks) {
            all.set(x, o);
            o += x.length;
          }
          env.downloaded = all;
          dl = null;
          resp("INFOdownload done");
          resp("OKAY");
        }
        return { status: "ok", bytesWritten: b.length };
      }
      const cmd = new TextDecoder().decode(b);
      d.r({ op: "cmd", cmd });
      if (cmd === "getvar:all") {
        violations.push("fastboot: getvar all");
        return { status: "ok", bytesWritten: b.length }; // the real U-Boot wedges: no answer
      }
      if (cmd.startsWith("getvar:")) {
        const v = getvar(cmd.slice(7));
        resp(v === null ? "FAILunknown variable" : "OKAY" + v);
      } else if (/^download:[0-9a-f]{8}$/.test(cmd)) {
        const n = parseInt(cmd.slice(9), 16);
        if (f === "downloadFail") resp("FAILno");
        else {
          dl = { need: n, have: 0, chunks: [] };
          env.announced = n;
          resp("DATA" + cmd.slice(9));
        }
      } else if (cmd === "boot") {
        if (!env.downloaded) {
          resp("FAILno image");
        } else if (f === "bootFail") {
          resp("FAILboot failed");
        } else {
          resp("OKAY");
          env.booted = true;
          setTimeout(() => {
            d.unplug();
            setTimeout(() => env.serial.attach(), 20);
            // sc.step1.serviceFailed: the step-1 service did not start (record
            // §6.6): the unit offers only its drive (1d6b:0104, no vendor
            // interface): no transfer link at all
            if (sc.step1 && !sc.step1.serviceFailed) setTimeout(() => env.usb.attach(makeStep1(env, sc, false)), sc.step1.bootMs || 40);
          }, 20);
        }
      } else {
        violations.push(`fastboot: command ${JSON.stringify(cmd)}`);
        resp("FAILunknown command");
      }
      return { status: "ok", bytesWritten: b.length };
    };
    return d;
  }

  // ---- the step-2 transport of a running mainline slot (uc-pxx-xfer) ----
  // A model of the receiver of internal/xfer: the manifest first and only
  // when its sha256 is pinned; then the two files it names, their exact sizes,
  // their sha256; SET_INTERFACE starts a new session and drops a partial
  // copy. After COMMIT it re-enumerates in status-only mode (the seal) and
  // walks through the step-2 states of sc.step2States, one per STATUS.
  // Faults (sc.step2Fault): "unplugRootfs" (a cable pull after 1 MiB of
  // rootfs.ext4), "refuseRootfs" (not enough RAM), "pinsEmpty".
  const FR = { HEADER: 16, REPLY: 0x80 };
  function frameOf(type, seq, obj) {
    let j = JSON.stringify(obj);
    let body = new TextEncoder().encode(j);
    while ((16 + body.length) % 64 === 0) body = new TextEncoder().encode((j += " "));
    const b = new Uint8Array(16 + body.length);
    const v = new DataView(b.buffer);
    b.set([0x50, 0x58, 0x58, 0x31]);
    v.setUint16(4, type, true);
    v.setUint32(8, body.length, true);
    v.setUint32(12, seq, true);
    b.set(body, 16);
    return b;
  }
  async function sha256hex(chunks) {
    const n = chunks.reduce((a, c) => a + c.length, 0);
    const all = new Uint8Array(n);
    let o = 0;
    for (const c of chunks) {
      all.set(c, o);
      o += c.length;
    }
    return hex(new Uint8Array(await crypto.subtle.digest("SHA-256", all))) + "|" + n;
  }
  function makeStep2(env, sc, statusOnly) {
    const d = new FakeDevice(env, { name: "step2", vendorId: 0x1d6b, productId: 0x0105, bcdUSB: 0x0210, bcdDevice: 0x0201,
      cls: [0xff, 0x50, 0x01], serial: "UC-PXX-STEP2" });
    const u = env.step2 || (env.step2 = { sessions: 0, frames: [], data: [], committed: null, statusCalls: 0, files: {}, puts: [] });
    let sess = null, pending = null;
    const f = sc.step2Fault || "";
    const pins = f === "pinsEmpty" ? [] : (sc.step2Pins || []);
    const states = sc.step2States || ["confirm", "started 0123456789ab", "writing system_b", "armed 0123456789ab", "restarting"];
    const reply = (type, seq, obj) => {
      const fr = frameOf(type | FR.REPLY, seq, obj);
      if (fr.length % 64 === 0) violations.push("step2: a reply of a multiple of 64 bytes");
      d.queue(fr);
    };
    const state = () => statusOnly ? "status-only" : u.committed ? "payload-complete" : sess && sess.want ? "receiving" : "waiting-payload";
    d.onSetAlt = () => {
      u.sessions++;
      sess = { want: null, got: {} };
      pending = null;
      d.inQueue.length = 0;
    };
    d.onOut = async (b) => {
      if (!sess) violations.push("step2: a transfer before SET_INTERFACE");
      if (pending) {
        const p = pending;
        const left = p.size - p.have;
        u.data.push({ name: p.name, n: b.length });
        if (b.length > 1048576 || (b.length !== 1048576 && b.length !== left)) violations.push(`step2: a data transfer of ${b.length} bytes (${left} left)`);
        if (b.length > left) violations.push("step2: more data than announced");
        p.chunks.push(b);
        p.have += b.length;
        if (f === "unplugRootfs" && p.name === "rootfs.ext4" && p.have >= 1048576 && !u.cut) {
          u.cut = true;
          d.unplug();
          throw netErr();
        }
        if (p.have >= p.size) {
          pending = null;
          const [s] = (await sha256hex(p.chunks)).split("|");
          if (p.name === "manifest.json") {
            let m = null;
            try { m = JSON.parse(new TextDecoder().decode(p.chunks.length === 1 ? p.chunks[0] : new Uint8Array(await new Blob(p.chunks).arrayBuffer()))); } catch { m = null; }
            if (!pins.includes(s) || !m) {
              reply(4, p.seq, { ok: false, name: p.name, sha256: s, reason: `manifest sha256 ${s} is not one of the ${pins.length} payloads that the step-2 gate pins` });
            } else {
              sess.want = { "boot-template.img": m.boot_template, "rootfs.ext4": m.rootfs };
              sess.got["manifest.json"] = s;
              reply(4, p.seq, { ok: true, name: p.name, sha256: s });
            }
          } else {
            const ok = s === sess.want[p.name].sha256;
            if (ok) sess.got[p.name] = s;
            reply(4, p.seq, { ok, name: p.name, sha256: s, reason: ok ? undefined : "sha256 is not the manifest's" });
          }
        }
        return { status: "ok", bytesWritten: b.length };
      }
      if (b.length % 64 === 0) violations.push(`step2: a frame of ${b.length} bytes (a multiple of 64)`);
      const v = new DataView(b.buffer, b.byteOffset, b.byteLength);
      if (b.length < 16 || b[0] !== 0x50 || b[1] !== 0x58 || b[2] !== 0x58 || b[3] !== 0x31 || v.getUint16(6, true) !== 0 ||
        v.getUint32(8, true) !== b.length - 16) {
        violations.push("step2: a bad frame");
        return { status: "stall", bytesWritten: 0 };
      }
      const type = v.getUint16(4, true), seq = v.getUint32(12, true);
      const body = JSON.parse(new TextDecoder().decode(b.subarray(16)));
      u.frames.push({ type, seq, body });
      d.r({ op: "step2-frame", type, name: body.name });
      if (type === 1) {
        reply(1, seq, { proto: 1, usb: "1d6b:0105", mode: statusOnly ? "status" : "payload", state: state(), status_only: !!statusOnly, session: u.sessions, pins,
          mem_available: 3000000000, room: 2000000000 });
      } else if (type === 6) {
        let info;
        if (statusOnly) {
          const i = Math.min(u.statusCalls++, states.length - 1);
          info = { model: "UC-P10", slot: "b", step2: states[i], lines: [`line ${i}: ${states[i]}`] };
          if (states[i] === "restarting" && sc.step1 && sc.step1.after === "step2" && !u.restarted) {
            u.restarted = true;
            setTimeout(() => {
              d.unplug();
              setTimeout(() => env.usb.attach(makeFinish(env, sc, "finish")), 60);
            }, 30);
          }
        }
        reply(6, seq, { state: state(), session: u.sessions, status_only: !!statusOnly, bytes: 0, total: 0, updated: 0, info });
      } else if (type === 2) {
        const n = body.name, size = body.size;
        let why = "";
        if (statusOnly) why = "status only: nothing is received now";
        else if (u.committed) why = "the payload is complete and waits for the seal";
        else if (n === "manifest.json") { if (sess.want) why = "the manifest of this session is already received"; if (!pins.length) why = "no payload is pinned by the step-2 gate: every manifest is refused"; }
        else if (n === "boot-template.img" || n === "rootfs.ext4") {
          if (!sess.want) why = "manifest first";
          else if (sess.got[n]) why = `${n} is already received`;
          else if (sess.want[n].size !== size) why = `${n}: ${size} bytes, the manifest says ${sess.want[n].size}`;
          else if (f === "refuseRootfs" && n === "rootfs.ext4") why = `not enough free RAM: ${size} bytes needed, 1000 free`;
        } else why = "not a file of the payload";
        u.puts.push({ name: n, size, accept: !why });
        if (why) reply(2, seq, { accept: false, name: n, reason: why });
        else {
          reply(2, seq, { accept: true, name: n });
          pending = { name: n, size, have: 0, chunks: [], seq };
        }
      } else if (type === 5) {
        const all = sess && sess.want && sess.got["manifest.json"] && sess.got["boot-template.img"] && sess.got["rootfs.ext4"];
        if (statusOnly || u.committed || !all) reply(5, seq, { ok: false, auto: !!body.auto, reason: "incomplete" });
        else {
          u.committed = { auto: !!body.auto, session: u.sessions };
          reply(5, seq, { ok: true, auto: !!body.auto });
          // the seal: the step-2 service stops the transport, renames the folder and starts it again, status-only
          setTimeout(() => {
            d.unplug();
            setTimeout(() => env.usb.attach(makeStep2(env, sc, true)), 60);
          }, 80);
        }
      } else violations.push(`step2: frame type ${type}`);
      return { status: "ok", bytesWritten: b.length };
    };
    return d;
  }

  // ---- the uc-pxx-usb interface of a normal mainline slot: the finish set ----
  // A model of the finish receiver of internal/xfer (Kind finish, one pin:
  // the sha256 of the slot's own finish.json): finish.json first (1 to 4096
  // bytes) and only when its sha256 is the pin, then uc-pxx-step2-finish and
  // uc-pxx-bootctl-inst with the list's sizes, their sha256; COMMIT only with
  // auto false. After the COMMIT the root side takes the gadget off, checks and
  // seals the set, and starts the transport again in status-only mode (a
  // re-enumeration); each STATUS then walks one step of sc.finishStates (the
  // info codes of internal/step2 FinishService).
  // sc.finishMode: the first device's mode, "finish" (default), "status" or
  // "payload". sc.finishFault: "cutBin" (a cable pull after 1 MiB of
  // uc-pxx-step2-finish), "lostCommitReply" (the COMMIT is taken, the gadget
  // goes away before the reply), "dropCommit" (the link ends at the COMMIT,
  // nothing taken, the unit comes back in finish mode). A state "code|line"
  // gives its own line (e.g. the refusal that uc-pxx-usb writes when the
  // sealed executor ended without a final code).
  const FINISH_LINES = {
    "finish: waiting": "This slot is a step-2 target: the page may send the pinned finish executor.",
    "finish: checking": "The check after step 2 (RKSS ranges, the keeper, the layout, the key) runs.",
    "finish: started": "The finish runs on the unit (the check after step 2).",
    "finish: not-yet": "This slot has not marked itself yet: the finish waits.",
    "finish: later": "This slot did not mark itself: the finish checks again at the next boot.",
    "finish: done": "Step 2 is finished: slot A boots first, slot B is the fallback.",
    "finish: switched": "Step 2 is finished: slot A boots first, slot B is the fallback.",
    "finish: supervised": "A supervised step 2: the finish runs only by the supervised procedure (dry run, rule-9 GO, --expect-id).",
    "finish: refused": "the finish: the keeper's vbmeta is not the one the record names",
    "finish: not a target": "This slot is not a step-2 target: nothing to finish.",
    "stop: the check after step 2": "STOP: the RKSS range changed",
    "hold: ab state not known": "the A/B state after the switch is not known",
    "idle": "step 2 is finished on this slot (its done record is there)",
  };
  const FINISH_NAMES = ["finish.json", "uc-pxx-step2-finish", "uc-pxx-bootctl-inst"];
  function makeFinish(env, sc, mode) {
    const d = new FakeDevice(env, { name: "xfer", vendorId: 0x1d6b, productId: 0x0105, bcdUSB: 0x0210, bcdDevice: 0x0201,
      cls: [0xff, 0x50, 0x01], serial: "UC-PXX-STEP2" });
    const u = env.finish || (env.finish = { sessions: 0, frames: [], data: [], committed: null, statusCalls: 0, puts: [], devices: 0, code: "" });
    u.devices++;
    let sess = null, pending = null;
    const f = sc.finishFault || "";
    const pin = sc.finishPin || "";
    const states = sc.finishStates || ["finish: checking", "finish: started", "finish: checking", "finish: not-yet", "finish: done"];
    if (mode === "finish") u.code = "finish: waiting";
    else if (mode === "status" && !u.committed) u.code = sc.finishStatusCode || "idle";
    else if (mode === "payload") u.code = "";
    const info = () => {
      if (mode === "payload") return undefined;
      const [code, line] = u.code.split("|");
      return { model: "UC-P10", slot: "b", token: "pxx-ab12cd", step2: code, lines: code ? [line || FINISH_LINES[code] || code] : [] };
    };
    // every status-only receiver says this (the info file says why)
    const detail = () => (mode === "status" ? "status only: nothing is received now" : undefined);
    const reply = (type, seq, obj) => {
      const fr = frameOf(type | FR.REPLY, seq, obj);
      if (fr.length % 64 === 0) violations.push("finish: a reply of a multiple of 64 bytes");
      d.queue(fr);
    };
    const state = () => mode === "status" ? "status-only" : mode === "payload" ? "waiting-payload"
      : u.committed ? "finish-complete" : sess && sess.want ? "receiving" : "waiting-finish";
    const pins = () => mode === "finish" ? [pin] : mode === "payload" ? ["9".repeat(64)] : [];
    d.onSetAlt = () => {
      u.sessions++;
      sess = { want: null, got: {} };
      pending = null;
      d.inQueue.length = 0;
    };
    // the gadget goes away (while the page waits for a reply), then the
    // transport comes back in nextMode
    const reenumerate = (nextMode, ms) => setTimeout(() => {
      d.unplug();
      setTimeout(() => env.usb.attach(makeFinish(env, sc, nextMode)), ms);
    }, 10);
    d.onOut = async (b) => {
      if (!sess) violations.push("finish: a transfer before SET_INTERFACE");
      if (pending) {
        const p = pending;
        const left = p.size - p.have;
        u.data.push({ name: p.name, n: b.length });
        if (b.length > 1048576 || (b.length !== 1048576 && b.length !== left)) violations.push(`finish: a data transfer of ${b.length} bytes (${left} left)`);
        if (b.length > left) violations.push("finish: more data than announced");
        p.chunks.push(b);
        p.have += b.length;
        if (f === "cutBin" && p.name === "uc-pxx-step2-finish" && p.have >= 1048576 && !u.cut) {
          u.cut = true;
          d.unplug();
          throw netErr();
        }
        if (p.have >= p.size) {
          pending = null;
          const [s] = (await sha256hex(p.chunks)).split("|");
          if (p.name === "finish.json") {
            let l = null;
            try { l = JSON.parse(new TextDecoder().decode(new Uint8Array(await new Blob(p.chunks).arrayBuffer()))); } catch { l = null; }
            if (s !== pin || !l) {
              reply(4, p.seq, { ok: false, name: p.name, sha256: s, reason: `finish.json sha256 ${s} is not this slot's own finish list` });
            } else {
              sess.want = {};
              for (const x of l.files) sess.want[x.name] = x;
              sess.got["finish.json"] = s;
              reply(4, p.seq, { ok: true, name: p.name, sha256: s, next: l.files });
            }
          } else {
            const ok = s === sess.want[p.name].sha256;
            if (ok) sess.got[p.name] = s;
            reply(4, p.seq, { ok, name: p.name, sha256: s, reason: ok ? undefined : `sha256 ${s} is not the one finish.json names` });
          }
        }
        return { status: "ok", bytesWritten: b.length };
      }
      if (b.length % 64 === 0) violations.push(`finish: a frame of ${b.length} bytes (a multiple of 64)`);
      const v = new DataView(b.buffer, b.byteOffset, b.byteLength);
      if (b.length < 16 || b[0] !== 0x50 || b[1] !== 0x58 || b[2] !== 0x58 || b[3] !== 0x31 || v.getUint16(6, true) !== 0 ||
        v.getUint32(8, true) !== b.length - 16) {
        violations.push("finish: a bad frame");
        return { status: "stall", bytesWritten: 0 };
      }
      const type = v.getUint16(4, true), seq = v.getUint32(12, true);
      const body = JSON.parse(new TextDecoder().decode(b.subarray(16)));
      u.frames.push({ type, seq, body, mode });
      d.r({ op: "xfer-frame", type, name: body.name, mode });
      if (type === 1) {
        reply(1, seq, { proto: 1, usb: "1d6b:0105", mode, state: state(), detail: detail(), status_only: mode === "status", session: u.sessions,
          pins: pins(), mem_available: 3000000000, room: mode === "status" ? 0 : 2000000000, info: info() });
      } else if (type === 6) {
        if (mode === "status" && u.committed) {
          const i = Math.min(u.statusCalls++, states.length - 1);
          u.code = states[i];
        }
        reply(6, seq, { state: state(), detail: detail(), mode, session: u.sessions, status_only: mode === "status", bytes: 0, total: 0, updated: 0, info: info() });
      } else if (type === 2) {
        const n = body.name, size = body.size;
        let why = "";
        if (mode !== "finish") {
          violations.push(`finish: a PUT_BEGIN of ${n} to a unit in mode ${mode}`);
          why = mode === "status" ? "status only: nothing is received now" : "not a file of the payload";
        } else if (u.committed) why = "the finish is complete and waits for the seal";
        else if (n === "finish.json") {
          if (sess.want) why = "the finish.json of this session is already received";
          else if (size <= 0 || size > 4096) why = `finish.json of ${size} bytes (1 to 4096)`;
        } else if (FINISH_NAMES.includes(n)) {
          if (!sess.want) why = "finish.json first";
          else if (sess.got[n]) why = `${n} is already received`;
          else if (sess.want[n].size !== size) why = `${n}: ${size} bytes, finish.json says ${sess.want[n].size}`;
        } else why = "not a file of the finish";
        u.puts.push({ name: n, size, accept: !why });
        if (why) reply(2, seq, { accept: false, name: n, reason: why });
        else {
          reply(2, seq, { accept: true, name: n });
          pending = { name: n, size, have: 0, chunks: [], seq };
        }
      } else if (type === 5) {
        const all = sess && sess.want && FINISH_NAMES.every((n) => sess.got[n]);
        if (body.auto !== false) violations.push("finish: a COMMIT with auto not false");
        if (mode !== "finish" || u.committed || !all || body.auto !== false) {
          reply(5, seq, { ok: false, auto: !!body.auto, reason: body.auto ? "a finish set has no automatic start (auto must be false)" : "incomplete" });
        } else if (f === "dropCommit" && !u.dropped) {
          u.dropped = true;
          reenumerate("finish", 60);
        } else {
          u.committed = { auto: body.auto, session: u.sessions };
          u.code = "finish: checking";
          if (f === "lostCommitReply") reenumerate("status", 120);
          else {
            reply(5, seq, { ok: true, auto: false });
            // the root side: unbind, check, seal, start the finish, status only
            setTimeout(() => reenumerate("status", 60), 70);
          }
        }
      } else violations.push(`finish: frame type ${type}`);
      return { status: "ok", bytesWritten: b.length };
    };
    return d;
  }

  // ---- the RC-10 RAM installer's step-1 link (I-6) ----
  // A model of the step-1 receiver of uc-pxx-installer mainline-rc-10 85f2b22
  // (internal/xfer kind step1) and of the flow behind it: the same USB
  // identity and frames as step 2, HELLO mode "step1" with the RAM image's
  // token (info.token) and the unlock status (info.unlock). The firmware stage
  // (state waiting-firmware, HELLO stage "firmware", fw_pins [{size, sha256}]):
  // firmware.bin of a pinned size, then its sha256 checked, SSH-KEY.PUB
  // (optional), COMMIT {mode}. After a COMMIT the link goes away and comes back
  // status only (state "busy", info.phase: what the flow does; one phase per
  // STATUS). mainline: busy/reading, then the bundle stage (state
  // waiting-bundle, stage "bundle", pins [the base manifest sha256]):
  // manifest.json (pinned), boot-template.img and rootfs.ext4.zst with the
  // manifest's sizes and sha256s, COMMIT {}; then busy/checking and the flow's
  // phases sc.step1.flow (plan, installing, done). After "done" the unit
  // restarts: the device goes away and, with sc.step1.after, slot B's link
  // comes (the system install, then the step-2 consent and the clone).
  // Faults (sc.step1.fault): "cutFirmware" / "cutBase" (a cable pull after
  // 1 MiB, the link comes back), "cutAlways", "otherToken", "noFwPin",
  // "lostInInstall" (the link goes away at "installing" and stays away).
  // sc.step1.unlockFrom N: the unlock word is "locked" until the flow passed
  // its N-th phase (index into flow), then "unlocked" (the RC-10d installer's
  // word after the install's lock-state write); unset: always "unlocked".
  // sc.step1.againAfter [phase words]: when the flow shows one of them (a
  // Back, a refused plan, the end of a switch), it asks for the files again
  // (record §6.6): the sealed files are gone, the link comes back in the
  // firmware stage; the flow then goes on with sc.step1.flow2.
  // sc.step1.driveGiveBack N: in the drive mode the first N reads of a drive
  // phase give the drive back (a refusal; the page sees "drive: reading" once
  // first): the unit then says "drive: waiting-firmware" also after the swap,
  // as the flow does; sc.step1.giveBackRemake: as a new, empty drive
  // (globalThis.__driveSwap); sc.step1.giveBackAfterSwap: only reads of the
  // folder (after the stash) are given back.
  function makeStep1(env, sc, statusOnly) {
    const o = sc.step1 || {};
    const u = env.step1 || (env.step1 = { sessions: 0, frames: [], puts: [], commits: [], stage: "firmware", phase: "", files: {},
      ticks: 0, flowAt: 0, cuts: 0, drive: false, devices: 0, phases: [] });
    u.devices++;
    const d = new FakeDevice(env, { name: "step1", vendorId: 0x1d6b, productId: 0x0105, bcdUSB: 0x0210,
      bcdDevice: u.drive ? 0x0302 : 0x0301, cls: [0xff, 0x50, 0x01], serial: "UC-PXX-STEP2" });
    let sess = null, pending = null;
    const f = o.fault || "";
    const flow = (u.round && o.flow2) || o.flow || ["plan", "plan", "install", "install", "done"];
    const reply = (type, seq, obj) => {
      const fr = frameOf(type | FR.REPLY, seq, obj);
      if (fr.length % 64 === 0) violations.push("step1: a reply of a multiple of 64 bytes");
      d.queue(fr);
    };
    // the installer writes its token only with its status report (b2a7eec
    // flowio/usb.go): sc.step1.noTokenHellos HELLOs come without it
    const info = () => ({ model: "UC-P10", token: f === "otherToken" ? "pxx-inst-other" : (u.hellos || 0) <= (o.noTokenHellos || 0) ? undefined : o.token,
      phase: u.drive === true ? `drive: ${u.drivePhase || "waiting-firmware"}` : u.phase || (u.stage === "firmware" ? "waiting-firmware" : "waiting-bundle"),
      unlock: o.unlockFrom === undefined ? "unlocked" : u.flowAt > o.unlockFrom ? "unlocked" : "locked", lines: [] });
    const state = () => {
      if (u.drive) return "drive";
      if (statusOnly) return "busy";
      if (sess && sess.want) return "receiving";
      return u.stage === "firmware" ? "waiting-firmware" : "waiting-bundle";
    };
    const reenum = (nextStatusOnly, ms) => setTimeout(() => {
      d.unplug();
      setTimeout(() => env.usb.attach(makeStep1(env, sc, nextStatusOnly)), ms);
    }, 15);
    const cut = () => {
      u.cuts++;
      d.unplug();
      setTimeout(() => env.usb.attach(makeStep1(env, sc, false)), 100);
      throw netErr();
    };
    d.onSetAlt = () => {
      u.sessions++;
      sess = { want: null, got: {} };
      pending = null;
      d.inQueue.length = 0;
    };
    // a STATUS of a busy unit walks the flow on by one step
    // the drive mode (state "drive" for the rest of the boot; info.phase goes
    // on): sc.step1.driveGoesOn (userdebug: the unit reads the drive, then
    // check, the flow) or sc.step1.driveMainline (the unit reads the drive,
    // the stash runs, the drive comes back empty for the mainline folder
    // ("drive: waiting-bundle", globalThis.__driveSwap empties the test's
    // drive, the gadget re-enumerates); read again, then check, the flow).
    // A test moves "drive: waiting-*" to "drive: reading" (the person's eject).
    const tick = () => {
      if (u.drive) {
        if (u.drive === true && u.drivePhase === "reading" && (u.gives || 0) < (o.driveGiveBack || 0) && (!o.giveBackAfterSwap || u.swapped)) {
          // the page sees "drive: reading" once, then the drive comes back for the same phase
          if (!u.readSeen) { u.readSeen = true; return; }
          u.readSeen = false;
          u.gives = (u.gives || 0) + 1;
          // a refused read gives the drive back on the firmware screen in
          // both phases (flow MainlineDriveRead: m.ml = nil, also after the
          // swap); the RAM stash stays (u.swapped): the next read of the
          // folder goes on to the check
          u.drivePhase = "waiting-firmware";
          if (o.giveBackRemake && globalThis.__driveSwap) globalThis.__driveSwap();
          return;
        }
        if (u.drive === true && u.drivePhase === "reading" && (o.driveGoesOn || o.driveMainline)) {
          u.drivePhase = "";
          u.drive = "on";
          u.phase = o.driveMainline && !u.swapped ? "stash" : "check";
          u.ticks = 0;
        } else if (u.drive === "on" && u.phase === "stash") {
          if (++u.ticks >= (o.readTicks || 2)) {
            const swap = () => {
              u.ticks = 0;
              u.phase = "";
              u.drive = true;
              u.drivePhase = "waiting-bundle";
              u.swapped = true;
              if (globalThis.__driveSwap) globalThis.__driveSwap();
            };
            // sc.step1.driveLinkGone: the vendor link goes away at the swap and
            // does not come back (the page has only the drive)
            if (o.driveLinkGone) setTimeout(() => { d.unplug(); swap(); }, 15);
            else {
              swap();
              reenum(true, 40);
            }
          }
        } else if (u.drive === "on") {
          if (u.phase === "check" || flow.includes(u.phase)) flowStep();
        }
        return;
      }
      if (!statusOnly) return;
      if (o.againAfter && o.againAfter.includes(u.phase) && !u.round) {
        u.round = 1;
        u.flowAt = 0;
        u.phase = "";
        u.stage = "firmware";
        reenum(false, 40);
        return;
      }
      if (u.phase === "stash") {
        if (++u.ticks >= (o.readTicks || 2)) {
          u.ticks = 0;
          u.phase = "";
          u.stage = "bundle";
          reenum(false, 40); // the flow asks for the bundle: the receiver starts again
        }
        return;
      }
      if (u.phase === "check" && ++u.ticks < (o.checkTicks || 2)) return;
      if (u.phase === "check" || flow.includes(u.phase)) flowStep();
    };
    function flowStep() {
      const c = flow[Math.min(u.flowAt++, flow.length - 1)];
      u.phase = c;
      u.phases.push(c);
      if (c === "install" && f === "lostInInstall" && !u.lost) {
        u.lost = true;
        setTimeout(() => d.unplug(), 10);
      }
      if (c === "done" && !u.doneOnce) {
        u.doneOnce = true;
        setTimeout(() => {
          d.unplug();
          // slot B boots: uc-pxx-usb's own status interface first, until
          // uc-pxx-provision (after the slot's mark) takes the gadget
          if (o.after) {
            setTimeout(() => env.usb.attach(makeUsbStatus(env, sc, () => env.usb.attach(makeProvision(env, sc, "provision")),
              (sc.prov && sc.prov.bootGapMs) || 150)), 60);
          }
        }, 30);
      }
    }
    d.onOut = async (b) => {
      if (!sess) violations.push("step1: a transfer before SET_INTERFACE");
      if (pending) {
        const p = pending;
        const left = p.size - p.have;
        if (b.length > 1048576 || (b.length !== 1048576 && b.length !== left)) violations.push(`step1: a data transfer of ${b.length} bytes (${left} left)`);
        if (b.length > left) violations.push("step1: more data than announced");
        p.chunks.push(b);
        p.have += b.length;
        if (p.have >= 1048576 && !u.cutDone && ((f === "cutFirmware" && p.name === "firmware.bin") ||
          (f === "cutBase" && p.name === "rootfs.ext4.zst")) || (f === "cutAlways" && p.have >= 1048576) ||
          (f === "cutBaseAlways" && p.name === "rootfs.ext4.zst" && p.have >= 1048576)) {
          u.cutDone = true;
          pending = null;
          cut();
        }
        if (p.have >= p.size) {
          pending = null;
          const [s] = (await sha256hex(p.chunks)).split("|");
          let ok = true, why;
          if (p.name === "firmware.bin") ok = (o.fwPins || [{ size: o.fwSize, sha256: o.fwSha }]).some((x) => x.size === p.size && x.sha256 === s);
          else if (p.name === "manifest.json") {
            ok = s === o.baseSha;
            if (ok) {
              const m = JSON.parse(new TextDecoder().decode(new Uint8Array(await new Blob(p.chunks).arrayBuffer())));
              sess.want = { "boot-template.img": m.boot_template, "rootfs.ext4.zst": m.rootfs_zst };
            }
          } else if (sess.want && sess.want[p.name]) ok = s === sess.want[p.name].sha256;
          if (!ok) why = `sha256 ${s} is not a pinned file`;
          if (ok) {
            sess.got[p.name] = true;
            u.files[p.name] = { sha256: s, size: p.size, text: p.name === "SSH-KEY.PUB" ? new TextDecoder().decode(new Uint8Array(await new Blob(p.chunks).arrayBuffer())) : undefined };
            if (p.name === "firmware.bin") sess.want = sess.want || {};
          }
          reply(4, p.seq, { ok, name: p.name, sha256: s, reason: why });
        }
        return { status: "ok", bytesWritten: b.length };
      }
      if (b.length % 64 === 0) violations.push(`step1: a frame of ${b.length} bytes (a multiple of 64)`);
      const v = new DataView(b.buffer, b.byteOffset, b.byteLength);
      if (b.length < 16 || b[0] !== 0x50 || b[1] !== 0x58 || b[2] !== 0x58 || b[3] !== 0x31 || v.getUint32(8, true) !== b.length - 16) {
        violations.push("step1: a bad frame");
        return { status: "stall", bytesWritten: 0 };
      }
      const type = v.getUint16(4, true), seq = v.getUint32(12, true);
      const body = JSON.parse(new TextDecoder().decode(b.subarray(16)));
      u.frames.push({ type, seq, body, state: state() });
      d.r({ op: "step1-frame", type, name: body.name });
      if (type === 1) {
        u.hellos = (u.hellos || 0) + 1;
        const h = { proto: 1, usb: "1d6b:0105", mode: "step1", state: state(), status_only: !!statusOnly, session: u.sessions,
          pins: !statusOnly && u.stage === "bundle" ? [o.baseSha] : [], mem_available: 3000000000, room: statusOnly ? 0 : 2000000000, info: info() };
        if (!statusOnly && !u.drive) {
          h.stage = u.stage;
          if (u.stage === "firmware") h.fw_pins = f === "noFwPin" ? [] : (o.fwPins || [{ size: o.fwSize, sha256: o.fwSha }]);
        }
        reply(1, seq, h);
      } else if (type === 6) {
        tick();
        reply(6, seq, { state: state(), mode: "step1", session: u.sessions, status_only: !!statusOnly, bytes: 0, total: 0, info: info() });
      } else if (type === 2) {
        const n = body.name, size = body.size;
        let why = "";
        if (statusOnly || u.drive) why = "the unit takes no file now (busy)";
        else if (u.stage === "firmware") {
          if (n === "firmware.bin") { if (sess.got["firmware.bin"]) why = "already"; else if (!(o.fwPins || [{ size: o.fwSize }]).some((x) => x.size === size)) why = `firmware.bin of ${size} bytes: no firmware that this installer takes has this size`; }
          else if (n === "SSH-KEY.PUB") { if (!sess.got["firmware.bin"]) why = "firmware.bin first"; else if (size <= 0 || size > 8192) why = "SSH-KEY.PUB of " + size + " bytes"; }
          else why = "not a file of the firmware stage";
        } else if (n === "manifest.json") { if (sess.want) why = "already"; }
        else if (n === "boot-template.img" || n === "rootfs.ext4.zst") {
          if (!sess.want) why = "manifest.json first";
          else if (sess.want[n].size !== size) why = `${n}: ${size} bytes, the manifest says ${sess.want[n].size}`;
        } else why = "not a file of the bundle";
        u.puts.push({ name: n, size, accept: !why, stage: u.stage });
        if (why) {
          violations.push(`step1: a PUT_BEGIN of ${n} refused: ${why}`);
          reply(2, seq, { accept: false, name: n, reason: why });
        } else {
          reply(2, seq, { accept: true, name: n });
          pending = { name: n, size, have: 0, chunks: [], seq };
        }
      } else if (type === 5) {
        u.commits.push({ body, stage: u.stage });
        const keys = Object.keys(body).filter((k) => !(k === "auto" && body.auto === false));
        if (!statusOnly && u.stage === "firmware" && sess.got["firmware.bin"] && (body.mode === "official" || body.mode === "mainline") &&
          keys.length === 1 && !(sess.got["SSH-KEY.PUB"] && body.mode !== "mainline")) {
          u.mode = body.mode;
          u.phase = body.mode === "mainline" ? "stash" : "check";
          if (sc.step1.askAgain) { u.phase = ""; u.stage = "firmware"; reenum(false, 40); reply(5, seq, { ok: true, auto: false, mode: body.mode }); return { status: "ok", bytesWritten: b.length }; }
          reply(5, seq, { ok: true, auto: false, mode: body.mode });
          reenum(true, 40);
        } else if (!statusOnly && u.stage === "bundle" && sess.got["manifest.json"] && sess.got["boot-template.img"] && sess.got["rootfs.ext4.zst"] && keys.length === 0) {
          u.phase = "check";
          reply(5, seq, { ok: true, auto: false });
          reenum(true, 40);
        } else {
          violations.push(`step1: a COMMIT ${JSON.stringify(body)} in stage ${u.stage}`);
          reply(5, seq, { ok: false, reason: "incomplete" });
        }
      } else if (type === 7) {
        // the receiver refuses DRIVE when it is status only (85f2b22 receiver.go drive())
        if (statusOnly || (sc.step1 && sc.step1.fault === "refuseDrive")) {
          reply(7, seq, { ok: false, reason: "status only" });
        } else {
          reply(7, seq, { ok: true });
          u.drive = true;
          // the drive mode keeps the stage: after the stash the unit asks for the mainline folder
          u.drivePhase = u.stage === "bundle" ? "waiting-bundle" : "waiting-firmware";
          u.swapped = u.stage === "bundle";
          reenum(true, 50);
        }
      } else violations.push(`step1: frame type ${type}`);
      return { status: "ok", bytesWritten: b.length };
    };
    return d;
  }

  // ---- slot B's link after step 1: the system install, the consent, the clone ----
  // A model of the installer's "Page interface" §6.10 with its "Built"
  // paragraph, as uc-pxx-installer mainline-rc-10 7f8ea48 sends it
  // (internal/xfer provision.go, consent.go, receiver.go mode();
  // internal/xfer/handover provision.go; internal/provision service.go).
  // The kind provision: HELLO {proto, usb, mode "provision", state, detail?,
  // status_only, session, pins [], mem_available, room, info, profiles
  // [{name, file, size, sha256}] (null for console), profile (the fixed one,
  // else the last COMMIT taken, else the root's; "" when none)}; STATUS {state,
  // detail?, session, done, total, fail?, reason?, next_try_s?, profile,
  // updated, mode "provision", status_only, info}: mode "provision" also when
  // status only. COMMIT {profile, source, url?} -> {ok, reason?} (no auto) in
  // waiting, retry-wait or failed, refused when status only, after a received
  // bundle, or while the unit works; url only with net (handover.CheckBaseURL);
  // console only with net and a seeded key; once apk add started the profile is
  // fixed. After a cable COMMIT the root waits for the bundle (state waiting,
  // total = the bundle size, "Send the packages over the cable."); the same
  // session takes one PUT of the pinned file and size (state receiving); its
  // PUT_END {ok, name, sha256, reason?} with ok is the hand-over: STATUS says
  // receiving with done = total until the root starts the receiver again status
  // only (re-enumeration); a refused bundle leaves the state waiting (another
  // PUT may follow in the same session). A cable pull during the PUT ends the
  // session: the link comes back in full mode, the root still waits for the
  // bundle. Each STATUS walks the states (net: downloading 2 steps, installing
  // 2 steps, checking, done; cable: installing 2 steps, checking, done); failed
  // carries fail (one word) and a reason (after a status-only phase the
  // receiver comes back in full mode); retry-wait a reason and next_try_s.
  // After done the root gives the gadget back to uc-pxx-usb: its own status
  // interface (mode "status", state "status-only") for sc.prov.gapMs, then
  // step 2's consent kind: mode "step2", state "waiting-consent", info.plan_id;
  // COMMIT {auto, plan_id} -> {ok, reason?, auto} (plan_id the one shown); then
  // each STATUS walks sc.step2States (info.step2); at "restarting" A's finish
  // link comes.
  // sc.prov: net "fail" (retry-wait, then failed: network, until a cable
  // choice), check "fail", total, fixed (the unit's profile is fixed already),
  // noKey (no root key was seeded), profiles (the unit's list), planId,
  // noPlanIdHellos (the first consent HELLOs without plan_id), gapMs (the
  // status interface after done), bootGapMs (the same at slot B's boot, before
  // provisioning takes the gadget), cutBundle, retryTicks, failAfterRetry.
  function urlTaken(u) {
    // handover.CheckBaseURL: 1..512 bytes of printable ASCII, http or https,
    // a host, no user info, no query or fragment, the path ends in "/"
    if (typeof u !== "string" || u.length === 0 || u.length > 512 || !/^[\x21-\x7e]+$/.test(u) || /[?#]/.test(u)) return false;
    const m = /^([A-Za-z][A-Za-z0-9+.-]*):\/\/([^/]*)(\/[^]*)?$/.exec(u);
    if (!m || !/^https?$/i.test(m[1]) || m[2] === "" || m[2].includes("@") || !(m[3] || "").endsWith("/")) return false;
    try { return !!new URL(u).hostname; } catch { return false; }
  }
  // uc-pxx-usb's own status interface on slot B (the payload kind, status
  // only): mode "status", state "status-only"; it takes nothing
  function makeUsbStatus(env, sc, then, ms) {
    const u = env.usbStatus || (env.usbStatus = { frames: [], devices: 0 });
    u.devices++;
    const d = new FakeDevice(env, { name: "slot-b", vendorId: 0x1d6b, productId: 0x0105, bcdUSB: 0x0210, bcdDevice: 0x0201,
      cls: [0xff, 0x50, 0x01], serial: "UC-PXX-STEP2" });
    const reply = (type, seq, obj) => d.queue(frameOf(type | FR.REPLY, seq, obj));
    // sc.prov.gapStep2: a leftover step-2 code in uc-pxx-usb's info (the page
    // must not take it for the outcome of this run's step 2)
    const info = () => ({ model: "UC-P10", slot: "b", token: "pxx-ab12cd", step2: (sc.prov && sc.prov.gapStep2) || "", lines: [] });
    const detail = "status only: nothing is received now";
    d.onSetAlt = () => { d.inQueue.length = 0; };
    d.onOut = async (b) => {
      const v = new DataView(b.buffer, b.byteOffset, b.byteLength);
      const type = v.getUint16(4, true), seq = v.getUint32(12, true);
      const body = JSON.parse(new TextDecoder().decode(b.subarray(16)));
      u.frames.push({ type, body, mode: "status", statusOnly: true });
      if (type === 1) reply(1, seq, { proto: 1, usb: "1d6b:0105", mode: "status", state: "status-only", detail, status_only: true, session: 1, pins: [],
        mem_available: 3e9, room: 0, info: info() });
      else if (type === 6) reply(6, seq, { state: "status-only", detail, session: 1, bytes: 0, total: 0, updated: 0, mode: "status", status_only: true, info: info() });
      else if (type === 5) { violations.push("slot-b: a COMMIT to uc-pxx-usb's status interface"); reply(5, seq, { ok: false, reason: "status only: nothing is received now", auto: false }); }
      else if (type === 2) { violations.push("slot-b: a PUT to uc-pxx-usb's status interface"); reply(2, seq, { accept: false, name: body.name, reason: "status only: nothing is received now" }); }
      else violations.push(`slot-b: frame type ${type}`);
      return { status: "ok", bytesWritten: b.length };
    };
    setTimeout(() => { d.unplug(); setTimeout(then, 30); }, ms);
    return d;
  }
  // step 2's own transport after the consent, status only (serviceclone.go:
  // usb.tr.Start(true, nil), the default kind: HELLO/STATUS mode "status",
  // state "status-only", info.step2 from /run/uc-pxx-step2/xfer-info.json with
  // the plan id). Each STATUS walks sc.prov.statusWalk (default "started",
  // "armed", "restarting"). Once a STATUS said the walk's last code, the HELLOs
  // of the next sessions (the finish's) get sc.prov.statusAfter one by one (the
  // last one repeats); after the last one the old slot restarts: the gadget
  // goes away and the new slot's finish interface comes (not with
  // sc.prov.statusStays: the old slot's link stays as it is).
  function makeStep2Status(env, sc) {
    const o = sc.prov || {};
    const u = env.s2status || (env.s2status = { frames: [], devices: 0, at: 0, after: 0, ended: false, gone: false, hellos: [] });
    u.devices++;
    const d = new FakeDevice(env, { name: "slot-b", vendorId: 0x1d6b, productId: 0x0105, bcdUSB: 0x0210, bcdDevice: 0x0201,
      cls: [0xff, 0x50, 0x01], serial: "UC-PXX-STEP2" });
    const reply = (type, seq, obj) => d.queue(frameOf(type | FR.REPLY, seq, obj));
    const walk = o.statusWalk || ["started 0123456789ab", "armed 0123456789ab", "restarting"];
    const after = Array.isArray(o.statusAfter) ? o.statusAfter : ["restarting"];
    let code = walk[0];
    const info = () => ({ model: "UC-P10", slot: "b", token: "pxx-ab12cd", step2: code, plan_id: "0123456789ab", lines: [`step 2: ${code}`] });
    const detail = "status only: nothing is received now";
    d.onSetAlt = () => { d.inQueue.length = 0; };
    d.onOut = async (b) => {
      const v = new DataView(b.buffer, b.byteOffset, b.byteLength);
      const type = v.getUint16(4, true), seq = v.getUint32(12, true);
      const body = JSON.parse(new TextDecoder().decode(b.subarray(16)));
      u.frames.push({ type, body, mode: "status", statusOnly: true });
      if (type === 1) {
        if (u.ended) {
          code = after[Math.min(u.after, after.length - 1)];
          u.after++;
          u.hellos.push(code);
          if (u.after >= after.length && !o.statusStays && !u.gone) {
            u.gone = true;
            setTimeout(() => { d.unplug(); setTimeout(() => env.usb.attach(makeFinish(env, sc, "finish")), 60); }, 30);
          }
        }
        reply(1, seq, { proto: 1, usb: "1d6b:0105", mode: "status", state: "status-only", detail, status_only: true, session: u.devices, pins: [],
          mem_available: 3e9, room: 0, info: info() });
      } else if (type === 6) {
        if (!u.ended) {
          code = walk[Math.min(u.at++, walk.length - 1)];
          if (u.at >= walk.length) u.ended = true;
        }
        reply(6, seq, { state: "status-only", detail, session: u.devices, bytes: 0, total: 0, updated: 0, mode: "status", status_only: true, info: info() });
      } else if (type === 5) { violations.push("slot-b: a COMMIT to step 2's status-only transport"); reply(5, seq, { ok: false, reason: "status only: nothing is received now", auto: false }); }
      else if (type === 2) { violations.push("slot-b: a PUT to step 2's status-only transport"); reply(2, seq, { accept: false, name: body.name, reason: "status only: nothing is received now" }); }
      else violations.push(`slot-b: frame type ${type}`);
      return { status: "ok", bytesWritten: b.length };
    };
    return d;
  }
  function makeProvision(env, sc, mode, statusOnly = false) {
    const o = sc.prov || {};
    const u = env.prov || (env.prov = { frames: [], commits: [], puts: [], at: 0, s2at: 0, devices: 0, done: false, files: {},
      profile: "", fixed: o.fixed || "", st: { state: "waiting" }, s2: { state: "waiting-consent" }, hellos2: 0, handovers: 0 });
    u.devices++;
    const d = new FakeDevice(env, { name: "slot-b", vendorId: 0x1d6b, productId: 0x0105, bcdUSB: 0x0210, bcdDevice: 0x0201,
      cls: [0xff, 0x50, 0x01], serial: "UC-PXX-STEP2" });
    let sess = null, pending = null, received = false;
    const total = o.total || 364000000;
    const planId = o.planId || "0123456789ab";
    const profiles = o.profiles || [
      { name: "kiosk-ha", file: o.bundleName, size: o.bundleSize, sha256: o.bundleSha },
      { name: "kiosk", file: "uc-pxx-profile-kiosk-0.3.0-test.tar", size: o.bundleSize, sha256: o.bundleSha },
      { name: "console", file: null, size: null, sha256: null }];
    const end = o.check === "fail" ? "failed-check" : "done";
    const net = ["downloading:0", "downloading:1", "installing:0", "installing:1", "checking", end];
    // cable: after the hand-over the root's progress still says waiting (the
    // take-over, the space and lock checks) until apk add starts
    const cable = ["waiting:handed", "waiting:handed", "installing:0", "installing:1", "checking", end];
    // sc.prov.failAfterRetry "check": the retries end in failed: check (not network)
    // sc.prov.retryTicks: how many STATUS replies say retry-wait (default 2)
    const fail = [...Array(o.retryTicks || 2).fill("retry-wait"), o.failAfterRetry === "check" ? "failed-check" : "failed"];
    const s2 = sc.step2States || ["confirm", "confirm", "started 0123456789ab", "armed 0123456789ab", "restarting"];
    const cur = () => (mode === "provision" ? u.st : u.s2);
    const set = (x) => { if (mode === "provision") u.st = x; else u.s2 = x; };
    const reply = (type, seq, obj) => d.queue(frameOf(type | FR.REPLY, seq, obj));
    const info = () => {
      const i = { model: "UC-P10", slot: "b", token: "pxx-ab12cd", step2: cur().step2 || "", lines: [] };
      if (mode === "step2" && u.hellos2 > (o.noPlanIdHellos || 0)) i.plan_id = planId;
      return i;
    };
    const profileNow = () => u.fixed || u.profile || "";
    // the provision kind's state of HELLO/STATUS: the receiver's own while it
    // receives (or holds a received bundle), else the root's progress
    const pst = () => {
      if (pending) return { state: "receiving", done: pending.have, total: pending.size };
      if (received) return { state: "receiving", done: received, total: received, reason: "the bundle was received: the unit checks it" };
      return u.st;
    };
    d.onSetAlt = () => { sess = {}; pending = null; d.inQueue.length = 0; };
    const walk = () => {
      if (mode === "provision" && u.commits.length && !pending && !received && !(u.source === "cable" && !u.handovers)) {
        const list = u.source === "cable" ? cable : o.net === "fail" ? fail : net;
        const c = list[Math.min(u.at++, list.length - 1)];
        const [s, k] = c.split(":");
        let st = { state: s, done: 0, total: 0 };
        if (s === "downloading") st = { state: s, done: (Number(k) + 1) * total / 2, total };
        if (s === "installing") {
          st = { state: s, done: (Number(k) + 1) * 138, total: 276 };
          u.fixed = u.fixed || u.profile; // apk add has started: the profile is fixed
        }
        // the page decides on fail; the reason is words only
        if (s === "failed") st = { state: s, done: 0, total: 0, fail: "network", reason: "the unit cannot download the packages (6 tries): no route to host" };
        if (s === "failed-check") st = { state: "failed", done: 0, total: 0, fail: "check", reason: "the content check: /usr/bin/x differs" };
        if (s === "retry-wait") st = { state: s, done: 0, total: 0, reason: "no route to host", next_try_s: 10 };
        if (s === "waiting") st = { state: s, done: 0, total: o.bundleSize || 0, reason: "Send the packages over the cable." };
        set(st);
        // a failure frees the stage; a status-only receiver comes back in full mode
        if (st.state === "failed" && statusOnly && !u.refull) {
          u.refull = true;
          setTimeout(() => { d.unplug(); setTimeout(() => env.usb.attach(makeProvision(env, sc, "provision", false)), 40); }, 15);
        }
        if (s === "done" && !u.done) {
          u.done = true;
          // done for a moment, then uc-pxx-usb's own status interface, then step 2's consent
          setTimeout(() => {
            d.unplug();
            setTimeout(() => env.usb.attach(makeUsbStatus(env, sc, () => env.usb.attach(makeProvision(env, sc, "step2")), o.gapMs || 150)), 30);
          }, 20);
        }
      } else if (mode === "step2" && u.consent && !o.statusAfter) {
        const c = s2[Math.min(u.s2at++, s2.length - 1)];
        set({ state: "consent-complete", step2: c });
        if (c === "restarting" && !u.restarted) {
          u.restarted = true;
          setTimeout(() => { d.unplug(); setTimeout(() => env.usb.attach(makeFinish(env, sc, "finish")), 60); }, 30);
        }
      }
    };
    const refuse = (seq, why) => {
      violations.push(`slot-b: COMMIT refused: ${why}`);
      reply(5, seq, mode === "provision" ? { ok: false, reason: why } : { ok: false, reason: why, auto: false });
    };
    d.onOut = async (b) => {
      if (pending) {
        const p = pending;
        p.chunks.push(b);
        p.have += b.length;
        if (b.length > 1048576 || (b.length !== 1048576 && b.length !== p.size - p.have + b.length)) violations.push(`slot-b: a data transfer of ${b.length} bytes`);
        // sc.prov.cutBundle N: the first N bundle sends are cut after 1 MiB
        // (the session ends; the link comes back in full mode; the root still
        // waits for the cable bundle)
        if (p.have >= 1048576 && (u.cuts || 0) < (o.cutBundle || 0)) {
          u.cuts = (u.cuts || 0) + 1;
          pending = null;
          d.unplug();
          setTimeout(() => env.usb.attach(makeProvision(env, sc, "provision")), 60);
          throw netErr();
        }
        if (p.have >= p.size) {
          pending = null;
          const [sh] = (await sha256hex(p.chunks)).split("|");
          const ok = sh === p.pin.sha256;
          reply(4, p.seq, { ok, name: p.name, sha256: sh, reason: ok ? undefined : `${p.name}: sha256 ${sh} is not the pin ${p.pin.sha256}` });
          if (!ok) sess.cable = p.pin; // the page may send it again in this session
          if (ok) {
            // the hand-over point: the receiver's ready record; STATUS says
            // receiving (done = total) until the root starts it again status only
            u.files[p.name] = sh;
            u.handovers++;
            u.at = 0;
            received = p.size;
            setTimeout(() => {
              d.unplug();
              setTimeout(() => {
                set({ state: "waiting", done: 0, total: p.size, reason: "Send the packages over the cable." });
                env.usb.attach(makeProvision(env, sc, "provision", true));
              }, 40);
            }, 60);
          }
        }
        return { status: "ok", bytesWritten: b.length };
      }
      const v = new DataView(b.buffer, b.byteOffset, b.byteLength);
      const type = v.getUint16(4, true), seq = v.getUint32(12, true);
      const body = JSON.parse(new TextDecoder().decode(b.subarray(16)));
      if (!sess) violations.push("slot-b: a transfer before SET_INTERFACE");
      u.frames.push({ type, body, mode, statusOnly });
      d.r({ op: "slotb-frame", type, mode });
      if (type === 1) {
        if (mode === "step2") u.hellos2++;
        if (mode === "provision") {
          const s = pst();
          reply(1, seq, { proto: 1, usb: "1d6b:0105", mode: "provision", state: s.state, status_only: statusOnly, session: u.devices, pins: [],
            mem_available: 3e9, room: statusOnly ? 0 : 2e9, info: info(), profiles, profile: profileNow() });
        } else {
          reply(1, seq, { proto: 1, usb: "1d6b:0105", mode: "step2", state: cur().state, status_only: false, session: 1, pins: [],
            mem_available: 3e9, room: 2e9, info: info() });
        }
      } else if (type === 6) {
        walk();
        if (mode === "provision") {
          const s = pst();
          const r = { state: s.state, session: u.devices, done: s.done || 0, total: s.total || 0, profile: profileNow(), updated: 0, mode: "provision",
            status_only: statusOnly, info: info() };
          for (const k of ["fail", "reason", "next_try_s"]) if (s[k] !== undefined) r[k] = s[k];
          reply(6, seq, r);
        } else reply(6, seq, { state: cur().state, session: 1, bytes: 0, total: 0, updated: 0, mode: "step2", status_only: false, info: info() });
      } else if (type === 5) {
        const keys = Object.keys(body).sort();
        if (mode === "provision") {
          const p = profiles.find((x) => x.name === body.profile);
          const st = pst().state;
          if (statusOnly) refuse(seq, `status only: the unit takes no choice now (${st})`);
          else if (received) refuse(seq, "the bundle was received: the unit checks it");
          else if (!["waiting", "retry-wait", "failed"].includes(st)) refuse(seq, `the unit works now (${st}): read STATUS`);
          else if (keys.some((k) => !["profile", "source", "url"].includes(k)) || !["net", "cable"].includes(body.source)) refuse(seq, `COMMIT ${JSON.stringify(body)}`);
          else if (!p) refuse(seq, `profile ${JSON.stringify(body.profile)} is not one of this unit's profiles`);
          else if (u.fixed && body.profile !== u.fixed) refuse(seq, `the profile is fixed: ${u.fixed}`);
          else if ("url" in body && body.source !== "net") refuse(seq, "a url only with the source net");
          else if (body.profile === "console" && body.source !== "net") refuse(seq, "the console profile has no bundle: source net (nothing is fetched)");
          else if (body.profile === "console" && o.noKey) refuse(seq, "no root ssh key was seeded in step 1: the console profile has no other way in");
          else if ("url" in body && !urlTaken(body.url)) refuse(seq, `the url ${JSON.stringify(body.url)}`);
          else {
            u.commits.push(body);
            u.profile = body.profile;
            u.source = body.source;
            u.at = 0;
            sess.cable = body.source === "cable" ? p : null;
            set(body.source === "cable" ? { state: "waiting", done: 0, total: p.size, reason: "Send the packages over the cable." } : { state: "downloading", done: 0, total });
            reply(5, seq, { ok: true });
          }
        } else if (cur().state !== "waiting-consent") refuse(seq, "the consent of this plan was given already");
        else if (JSON.stringify(keys) !== JSON.stringify(["auto", "plan_id"]) || typeof body.auto !== "boolean") refuse(seq, `consent COMMIT ${JSON.stringify(body)}`);
        else if (body.plan_id !== planId) {
          violations.push(`slot-b: consent of plan ${body.plan_id}, not ${planId}`);
          reply(5, seq, { ok: false, reason: `plan_id ${JSON.stringify(body.plan_id)} is not the plan the unit shows (${planId}): read HELLO again`, auto: body.auto });
        } else {
          u.consent = body;
          set({ state: "consent-complete", step2: "" });
          reply(5, seq, { ok: true, auto: body.auto });
          // sc.prov.statusAfter: the installer's own order (serviceclone.go):
          // the consent transport stops, step 2's transport comes back status
          // only (mode "status") for the copy and the restart
          if (o.statusAfter && !u.statusLink) {
            u.statusLink = true;
            setTimeout(() => { d.unplug(); setTimeout(() => env.usb.attach(makeStep2Status(env, sc)), 30); }, 20);
          }
        }
      } else if (type === 2) {
        const p = sess.cable;
        let why = "";
        if (statusOnly) why = "status only: nothing is received now";
        else if (mode !== "provision") why = "the consent of step 2 takes no file: COMMIT {auto, plan_id}";
        else if (received) why = "the bundle was received already: the unit checks it";
        else if (!p) why = "a PUT needs a COMMIT with the source cable in this session";
        else if (body.name !== p.file || body.size !== p.size) why = `${body.name} of ${body.size} bytes is not the pinned bundle of ${p.name}`;
        u.puts.push({ name: body.name, size: body.size, accept: !why });
        if (why) { violations.push(`slot-b: PUT ${body.name}: ${why}`); reply(2, seq, { accept: false, name: body.name, reason: why }); }
        else {
          sess.cable = null; // one PUT per cable COMMIT (again after a refused bundle)
          reply(2, seq, { accept: true, name: body.name });
          pending = { name: body.name, size: body.size, have: 0, chunks: [], seq, pin: p };
        }
      } else violations.push(`slot-b: frame type ${type}`);
      return { status: "ok", bytesWritten: b.length };
    };
    return d;
  }

  // ---- navigator.usb ----
  class FakeUsb extends EventTarget {
    constructor(env, sc) {
      super();
      this.env = env;
      this.sc = sc;
      this.attached = [];
      this.permitted = new Set();
      this.onconnect = null;
      this.ondisconnect = null;
    }
    // WebUSB keeps a permission per device (vendor, product, serial string):
    // the transfer devices (1d6b:0105, serial UC-PXX-STEP2) of step 1, step 2
    // and the finish share one permission (DESIGN §9.6.3).
    key(d) {
      return d.vendorId === 0x1d6b && d.productId === 0x0105 ? `${d.vendorId}:${d.productId}:${d.serialNumber}` : `${d.vendorId}:${d.productId}:${d.name}`;
    }
    async requestDevice(opt) {
      rec.push({ n: rec.length, dev: "usb", op: "requestDevice" });
      if (!opt || !Array.isArray(opt.filters) || opt.filters.length === 0) violations.push("requestDevice without filters");
      const m = this.attached.filter((d) => opt.filters.some((f) => (f.vendorId === undefined || f.vendorId === d.vendorId) &&
        (f.productId === undefined || f.productId === d.productId)));
      if (!m.length || this.sc.chooser === "cancel") throw new DOMException("No device selected.", "NotFoundError");
      this.permitted.add(this.key(m[0]));
      return m[0];
    }
    async getDevices() {
      return this.attached.filter((d) => this.permitted.has(this.key(d)));
    }
    attach(d) {
      this.attached.push(d);
      rec.push({ n: rec.length, dev: d.name, op: "attach" });
      if (this.permitted.has(this.key(d))) {
        const ev = new Event("connect");
        ev.device = d;
        this.dispatchEvent(ev);
      }
    }
    detach(d) {
      this.attached = this.attached.filter((x) => x !== d);
      if (this.permitted.has(this.key(d))) {
        const ev = new Event("disconnect");
        ev.device = d;
        this.dispatchEvent(ev);
      }
    }
  }

  // ---- navigator.serial: the installer's shell ----
  class FakePort {
    constructor(env) {
      this.env = env;
      this.readable = null;
      this.writable = null;
    }
    getInfo() {
      return { usbVendorId: 0x1d6b, usbProductId: 0x0104 };
    }
    async open(o) {
      rec.push({ n: rec.length, dev: "serial", op: "open", baud: o && o.baudRate });
      let ctl;
      this.readable = new ReadableStream({ start: (c) => { ctl = c; } });
      const enc = new TextEncoder();
      ctl.enqueue(enc.encode("\x1b[1muc-pxx-inst\x1b[0m status image\r\n# "));
      let line = "";
      this.writable = new WritableStream({
        write: (chunk) => {
          const t = new TextDecoder().decode(chunk);
          rec.push({ n: rec.length, dev: "serial", op: "write", text: t });
          for (const ch of t) {
            if (ch === "\r") {
              ctl.enqueue(enc.encode(line + "\r\n" + (line === "uname -m" ? "aarch64\r\n" : "") + "# "));
              line = "";
            } else line += ch;
          }
        },
      });
    }
    async close() {}
  }
  class FakeSerial extends EventTarget {
    constructor(env) {
      super();
      this.env = env;
      this.ports = [];
    }
    attach() {
      this.ports.push(new FakePort(this.env));
      rec.push({ n: rec.length, dev: "serial", op: "attach" });
    }
    async requestPort(o) {
      rec.push({ n: rec.length, dev: "serial", op: "requestPort", filters: o && o.filters });
      const ok = o && o.filters && o.filters.some((f) => f.usbVendorId === 0x1d6b && f.usbProductId === 0x0104);
      if (!ok || !this.ports.length) throw new DOMException("No port selected.", "NotFoundError");
      return this.ports[0];
    }
    async getPorts() {
      return [];
    }
  }

  function install(sc) {
    sc = sc || {};
    rec.length = 0;
    violations.length = 0;
    const env = { sc, emmc: new Emmc(), downloaded: null, announced: null, booted: false, release: null,
      hostWrites: [], abOwnChange: false };
    const ab = sc.abHex ? fromHex(sc.abHex) : abSector(sc.a || [15, 0, 1], sc.b || [14, 0, 1], sc.crc);
    const abFull = new Uint8Array(512);
    abFull.set(ab.subarray(0, 512));
    env.emmc.write(AB_LBA, abFull);
    // misc around the two places, so a stray read or write shows in the sha
    for (let l = 49152; l < 49192; l++) if (l !== AB_LBA && (l < BCB_LBA || l > BCB_LBA + 3)) env.emmc.write(l, new Uint8Array(512).fill(l & 0xff));
    let bcb = new Uint8Array(2048);
    if (sc.bcb === "payload") bcb.set(new TextEncoder().encode("bootonce-bootloader"));
    else if (sc.bcb === "unserved") bcb.fill(0xcc); // the download mode's "not served" byte over the whole block
    else if (sc.bcb === "unserved-partial") { bcb.fill(0xcc); bcb[100] = 0; } // mostly 0xCC: a leftover, written
    else if (sc.bcb === "recovery") bcb.set(new TextEncoder().encode("boot-recovery"));
    else if (sc.bcb === "boot_recovery") bcb.set(new TextEncoder().encode("boot_recovery"));
    else if (sc.bcb === "random") for (let i = 0; i < 2048; i++) bcb[i] = (i * 131 + 7) & 0xff;
    else if (sc.bcb === "leftover") for (let i = 32; i < 2048; i++) bcb[i] = (i * 37 + 11) & 0xff; // empty command, rest not zero
    else if (sc.bcb === "bq-leftover") { bcb[0] = 0x62; bcb[1] = 0x01; } // command field "b?" (0x62 + a non-printable byte): a post-boot/post-install RAM leftover (afaa05c4...), which U-Boot ignores
    else if (sc.bcb === "hw-leftover") {
      // the leftover of the 2026-11-08 hardware run: command field d8 85 00 ...
      // (RAM garbage that varies from boot to boot), the rest of the block not zero
      for (let i = 3; i < 2048; i++) bcb[i] = (i * 73 + 29) & 0xff;
      bcb[0] = 0xd8; bcb[1] = 0x85; bcb[2] = 0x00;
    } else if (sc.bcb === "payload-tail") {
      // "bootonce-bootloader" in the command field, but the rest of the block not zero
      bcb.set(new TextEncoder().encode("bootonce-bootloader"));
      for (let i = 64; i < 2048; i++) bcb[i] = (i * 37 + 11) & 0xff;
    }
    env.emmc.write(BCB_LBA, bcb);
    // The primary GPT (LBA 0..33) and the factory block (cfg, 3666944, 8). By
    // default a valid GPT and an unserved (0xCC) factory block, as the real
    // download mode serves (the model is then gated by the image + RAM image).
    let gpt;
    if (sc.gpt === "nosig") gpt = new Uint8Array(34 * 512);
    else if (sc.gpt === "badmisc") {
      gpt = buildGpt([["uboot", 16384, 8192], ["misc", 49152, 4096], ["cfg", 3666944, 262144]]);
    } else gpt = DEFAULT_GPT();
    env.emmc.write(0, gpt);
    let fac;
    if (sc.factory === "ucp10") fac = buildFactory("UC-P10-xxxx");
    else if (sc.factory === "ucp8") fac = buildFactory("UC-P8-xxxx");
    else if (sc.factory === "bad") { fac = buildFactory("UC-P10"); fac[94] ^= 0xff; }
    else fac = new Uint8Array(8 * 512).fill(0xcc); // "unserved": the real download mode
    env.emmc.write(3666944, fac);
    env.abBefore = hex(env.emmc.read(AB_LBA, 1));
    env.usb = new FakeUsb(env, sc);
    env.serial = new FakeSerial(env);
    // U-Boot after a reset: act on the BCB (start fastboot, clear the command).
    env.ubootRestart = () => {
      const b = env.emmc.read(BCB_LBA, 4);
      const cmd = new TextDecoder().decode(b.subarray(0, 19));
      if (sc.afterReset === "maskrom") {
        setTimeout(() => {
          const m = makeRockusb(env, Object.assign({}, sc, { start: "maskrom", fault: "" }), false);
          env.usb.attach(m);
        }, 30);
        return;
      }
      if (cmd !== "bootonce-bootloader") {
        rec.push({ n: rec.length, dev: "uboot", op: "normal-boot" });
        return;
      }
      b.fill(0, 0, 32);
      env.emmc.write(BCB_LBA, b);
      // --plain-entry: clearing the BCB runs U-Boot's buggy misc writer, which
      // scribbles LBA 49156; libavb resets the A/B block to the benign
      // both-trying state. By default the fake does exactly that reset. A test
      // can override with entryAb (an explicit post-entry A/B) or keepAbOnEntry
      // (A/B survives, e.g. a stale keeper), or the legacy flipSlotOnEntry.
      env.abOwnChange = true;
      if (sc.entryAb) env.emmc.write(AB_LBA, abSector(sc.entryAb[0], sc.entryAb[1]));
      else if (sc.keepAbOnEntry) env.abOwnChange = false; // U-Boot left A/B unchanged (the forbidden preserve case)
      else if (sc.flipSlotOnEntry) env.emmc.write(AB_LBA, abSector([15, 0, 1], [14, 0, 1]));
      else env.emmc.write(AB_LBA, plainReset());
      setTimeout(() => {
        const fb = makeFastboot(env, sc);
        if (sc.fastbootPermitted) env.usb.permitted.add(env.usb.key(fb));
        env.usb.attach(fb);
      }, 30);
    };
    if (sc.start === "rockusb" || sc.start === "maskrom") env.usb.attach(makeRockusb(env, sc, false));
    if (sc.start === "fastboot") env.usb.attach(makeFastboot(env, sc));
    if (sc.start === "step2") env.usb.attach(makeStep2(env, sc, false));
    // a test re-plugs the step-2 device (after a cut)
    env.step2Replug = () => env.usb.attach(makeStep2(env, sc, false));
    // a test lets the unit go on in drive mode
    env.step1Drive = (st) => { env.step1.driveState = st; };
    if (sc.start === "finish") env.usb.attach(makeFinish(env, sc, sc.finishMode || "finish"));
    env.finishReplug = () => env.usb.attach(makeFinish(env, sc, "finish"));
    if (sc.second) {
      const d2 = makeRockusb(env, Object.assign({}, sc, { start: "rockusb", fault: "" }), true);
      env.usb.permitted.add(env.usb.key(d2));
      env.usb.attach(d2);
    }
    const nav = G.Navigator ? G.Navigator.prototype : G.navigator;
    const def = (k, v) => Object.defineProperty(nav, k, { get: () => v, configurable: true });
    def("usb", sc.noUsb ? undefined : env.usb);
    def("serial", sc.noSerial ? undefined : env.serial);
    def("userAgentData", sc.noUAData ? undefined : {
      brands: (sc.brands || ["Google Chrome", "Chromium"]).map((b) => ({ brand: b, version: "140" })),
      mobile: !!sc.mobile, platform: sc.platform || "Linux",
    });
    G.__fake = { rec, violations, env, hex, installed: true };
  }

  G.FakeEnv = { install, abSector, crc32, hex, fromHex, sleep };
})();
