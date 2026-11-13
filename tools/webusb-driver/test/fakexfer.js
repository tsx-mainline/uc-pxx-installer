// SPDX-License-Identifier: GPL-2.0-or-later
// fakexfer.js (tests only): a fake navigator.usb with the unit's transfer
// interface, modelled on the receiver of internal/xfer (receiver.go: hello,
// status, decide, put, commit) and the seal's re-enumeration in the
// status-only mode. A classic script: the driver's test mode injects it before
// inpage.js, so a test never reaches a real device. Every frame and every data
// transfer is recorded in __fakexfer; every broken protocol rule in
// __fakexfer.violations.
(function () {
  "use strict";
  const G = globalThis;
  const enc = new TextEncoder();
  const dec = new TextDecoder();
  const rec = [];
  const violations = [];
  const hex = (b) => Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  async function sha(chunks) {
    const n = chunks.reduce((a, c) => a + c.length, 0);
    const all = new Uint8Array(n);
    let o = 0;
    for (const c of chunks) {
      all.set(c, o);
      o += c.length;
    }
    return hex(new Uint8Array(await crypto.subtle.digest("SHA-256", all)));
  }
  function frameOf(type, seq, obj) {
    let j = JSON.stringify(obj);
    while ((16 + enc.encode(j).length) % 64 === 0) j += " ";
    const body = enc.encode(j);
    const b = new Uint8Array(16 + body.length);
    const v = new DataView(b.buffer);
    b.set([0x50, 0x58, 0x58, 0x31]);
    v.setUint16(4, type | 0x80, true);
    v.setUint32(8, body.length, true);
    v.setUint32(12, seq, true);
    b.set(body, 16);
    return b;
  }
  const netErr = () => new DOMException("A transfer error has occurred.", "NetworkError");

  const SETS = {
    payload: { first: "manifest.json", max: 65536, names: ["manifest.json", "boot-template.img", "rootfs.ext4"], waiting: "waiting-payload", complete: "payload-complete" },
    finish: { first: "finish.json", max: 4096, names: ["finish.json", "uc-pxx-step2-finish", "uc-pxx-bootctl-inst"], waiting: "waiting-finish", complete: "finish-complete" },
  };

  class Dev {
    constructor(u, sc, mode, n) {
      this.u = u;
      this.sc = sc;
      this.mode = mode; // payload | finish | status
      this.statusOnly = mode === "status";
      this.kind = mode === "status" ? (sc.kind || "payload") : mode;
      this.vendorId = 0x1d6b;
      this.productId = 0x0105;
      this.serialNumber = sc.serial === undefined ? "UC-PXX-STEP2" : sc.serial;
      this.productName = sc.product === undefined ? "UC-P10 mainline step 2" : sc.product;
      this.manufacturerName = sc.maker === undefined ? "uc-pxx" : sc.maker;
      this.n = n;
      this.opened = false;
      this.claimed = false;
      this.gone = false;
      this.configuration = null;
      this.configurations = [{ configurationValue: 1, interfaces: [{ interfaceNumber: 0, claimed: false, alternates: [{
        alternateSetting: 0, interfaceClass: 0xff, interfaceSubclass: 0x50, interfaceProtocol: 0x01,
        endpoints: [{ endpointNumber: 1, direction: "out", type: "bulk", packetSize: 512 }, { endpointNumber: 2, direction: "in", type: "bulk", packetSize: 512 }] }] }] }];
      this.inQ = [];
      this.waiters = [];
      this.sess = null;
      this.session = 0;
      this.pending = null;
      this.complete = false;
      this.statusCalls = 0;
      this.st = { state: this.statusOnly ? "status-only" : SETS[this.kind].waiting, detail: "" };
      this.info = Object.assign({}, sc.info || {});
    }
    r(e) {
      rec.push(Object.assign({ n: rec.length, dev: this.n }, e));
    }
    state() {
      if (this.statusOnly) return "status-only";
      if (this.complete) return SETS[this.kind].complete;
      if (this.sess && this.sess.want) return "receiving";
      return SETS[this.kind].waiting;
    }
    infoNow() {
      if (this.statusOnly && Array.isArray(this.sc.after)) {
        const i = Math.min(this.statusCalls++, this.sc.after.length - 1);
        const a = this.sc.after[i];
        const step2 = typeof a === "string" ? a : a.step2;
        return Object.assign({}, this.info, { step2, lines: [`(fake) ${step2}`] }, typeof a === "object" ? a : {});
      }
      return this.info;
    }
    room() {
      return this.sc.room === undefined ? 3000000000 : this.sc.room;
    }
    async open() {
      this.r({ op: "open" });
      if (this.gone) throw new DOMException("The device was disconnected.", "NotFoundError");
      this.opened = true;
    }
    async close() {
      this.r({ op: "close" });
      this.opened = false;
    }
    async selectConfiguration() {
      this.configuration = this.configurations[0];
    }
    async claimInterface(i) {
      this.r({ op: "claim", i });
      this.claimed = true;
    }
    async releaseInterface() {}
    async selectAlternateInterface(i, a) {
      this.r({ op: "set-alt" });
      if (!this.claimed) violations.push("SET_INTERFACE before the claim");
      this.session++;
      this.sess = { want: null, got: {}, manifestSha: "" };
      this.pending = null;
      this.inQ.length = 0;
    }
    async reset() {
      violations.push("reset() reached the fake device");
    }
    async forget() {
      violations.push("forget() reached the fake device");
    }
    async controlTransferIn() {
      violations.push("a control transfer reached the fake device");
      throw netErr();
    }
    async controlTransferOut() {
      violations.push("a control transfer reached the fake device");
      throw netErr();
    }
    async clearHalt() {
      violations.push("clearHalt() reached the fake device");
    }
    queue(b) {
      const w = this.waiters.shift();
      if (w) w.resolve(b);
      else this.inQ.push(b);
    }
    reply(type, seq, obj) {
      this.r({ op: "reply", type, body: obj });
      this.queue(frameOf(type, seq, obj));
    }
    unplug() {
      if (this.gone) return;
      this.gone = true;
      this.opened = false;
      this.r({ op: "unplug" });
      for (const w of this.waiters.splice(0)) w.reject(netErr());
      this.u.detach(this);
    }
    async transferIn(ep, len) {
      if (!this.opened || !this.claimed) violations.push("transferIn on a closed device");
      if (this.gone) throw netErr();
      const b = this.inQ.length ? this.inQ.shift() : await new Promise((resolve, reject) => this.waiters.push({ resolve, reject }));
      if (b.length > len) violations.push("a reply longer than the read");
      return { status: "ok", data: new DataView(b.buffer) };
    }
    async transferOut(ep, data) {
      if (!this.opened || !this.claimed) violations.push("transferOut on a closed device");
      if (this.gone) throw netErr();
      const b = new Uint8Array(data.buffer ? data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength) : data);
      await Promise.resolve();
      if (this.gone) throw netErr();
      if (!this.sess) violations.push("a transfer before SET_INTERFACE");
      const f = this.sc.fault || "";
      if (this.pending) {
        const p = this.pending;
        const left = p.size - p.have;
        if (b.length > 1048576 || (b.length !== 1048576 && b.length !== left)) violations.push(`a data transfer of ${b.length} bytes (${left} left)`);
        p.chunks.push(b);
        p.have += b.length;
        this.r({ op: "data", name: p.name, len: b.length, have: p.have });
        if (this.sc.slowData) await sleep(this.sc.slowData);
        if (f === "cutRootfs" && (p.name === "rootfs.ext4" || p.name === "uc-pxx-step2-finish") && p.have >= 1048576 && !this.cut) {
          this.cut = true;
          this.unplug();
          throw netErr();
        }
        if (p.have >= p.size) {
          this.pending = null;
          const s = await sha(p.chunks);
          const set = SETS[this.kind];
          if (p.name === set.first) {
            const pins = this.sc.pins || [];
            let m = null;
            try {
              m = JSON.parse(dec.decode(await new Blob(p.chunks).arrayBuffer()));
            } catch {
              m = null;
            }
            if (!pins.includes(s) || !m) {
              this.reply(4, p.seq, { ok: false, name: p.name, sha256: s, reason: `${p.name} sha256 ${s} is not pinned` });
            } else {
              this.sess.want = this.kind === "payload" ? { "boot-template.img": m.boot_template, "rootfs.ext4": m.rootfs }
                : Object.fromEntries(m.files.map((x) => [x.name, x]));
              this.sess.got[p.name] = s;
              this.sess.manifestSha = s;
              this.reply(4, p.seq, { ok: true, name: p.name, sha256: s });
            }
          } else {
            let unit = s;
            if (f === `badSha:${p.name}`) unit = "0".repeat(64);
            const ok = unit === this.sess.want[p.name].sha256;
            if (ok) this.sess.got[p.name] = unit;
            // lyingSha: the unit says ok but names another sha (the driver's guard must refuse the COMMIT)
            const named = f === `lyingSha:${p.name}` ? "f".repeat(64) : unit;
            this.reply(4, p.seq, ok || f === `lyingSha:${p.name}` ? { ok: true, name: p.name, sha256: named } : { ok: false, name: p.name, sha256: unit, reason: "sha256 is not the one the first file names" });
            if (f === `lyingSha:${p.name}`) this.sess.got[p.name] = unit;
          }
        }
        return { status: "ok", bytesWritten: b.length };
      }
      if (b.length % 64 === 0) violations.push(`a frame of ${b.length} bytes (a multiple of 64)`);
      const v = new DataView(b.buffer);
      if (b.length < 16 || dec.decode(b.subarray(0, 4)) !== "PXX1" || v.getUint16(6, true) !== 0 || v.getUint32(8, true) !== b.length - 16) {
        violations.push("a bad frame");
        return { status: "stall", bytesWritten: 0 };
      }
      const type = v.getUint16(4, true), seq = v.getUint32(12, true);
      const body = JSON.parse(dec.decode(b.subarray(16)));
      this.r({ op: "frame", type, body });
      const set = SETS[this.kind];
      if (type === 1) {
        if (f === "noHelloAnswer") return { status: "ok", bytesWritten: b.length };
        const h = { proto: 1, usb: "1d6b:0105", mode: this.statusOnly ? "status" : this.mode, state: this.state(), status_only: this.statusOnly,
          session: this.session, pins: this.statusOnly ? [] : (this.sc.pins || []), mem_available: 3000000000 };
        if (this.st.detail) h.detail = this.st.detail;
        if (!this.statusOnly) h.room = this.room();
        h.info = this.infoNow();
        this.reply(1, seq, h);
      } else if (type === 6) {
        this.reply(6, seq, { state: this.state(), bytes: 0, total: 0, session: this.session, mode: this.statusOnly ? "status" : this.mode,
          status_only: this.statusOnly, info: this.infoNow() });
      } else if (type === 2) {
        const n = body.name, size = body.size;
        let why = "";
        if (this.statusOnly) why = "status only: nothing is received now";
        else if (this.complete) why = `the ${this.kind} is complete and waits for the seal`;
        else if (n === set.first) {
          if (this.sess.want) why = `the ${set.first} of this session is already received`;
          else if (!(this.sc.pins || []).length) why = "no payload is pinned by the step-2 gate: every manifest is refused";
          else if (size <= 0 || size > set.max) why = `${set.first} of ${size} bytes`;
        } else if (set.names.includes(n)) {
          if (!this.sess.want) why = `${set.first} first`;
          else if (this.sess.got[n]) why = `${n} is already received`;
          else if (this.sess.want[n].size !== size) why = `${n}: ${size} bytes, ${set.first} says ${this.sess.want[n].size}`;
        } else why = `not a file of the ${this.kind}`;
        if (!why && size > this.room()) why = `not enough free RAM: ${size} bytes needed, ${this.room()} free`;
        if (!why && f === `refuse:${n}`) why = `not enough free RAM: ${size} bytes needed, 1000 free`;
        if (why) this.reply(2, seq, { accept: false, name: n, reason: why });
        else {
          this.reply(2, seq, { accept: true, name: n });
          this.pending = { name: n, size, have: 0, chunks: [], seq };
        }
      } else if (type === 5) {
        const all = this.sess && this.sess.want && set.names.every((x) => this.sess.got[x]);
        let why = "";
        if (this.statusOnly) why = "status only: nothing is received now";
        else if (this.complete) why = `the ${this.kind} is already complete`;
        else if (this.kind === "finish" && body.auto) why = "a finish set has no automatic start (auto must be false)";
        else if (!all) why = "incomplete";
        else if (f === "commitRefused") why = "the session was ended";
        if (why) this.reply(5, seq, { ok: false, reason: why, auto: !!body.auto });
        else {
          this.complete = true;
          G.__fakexfer.committed = { auto: !!body.auto, session: this.session, kind: this.kind, manifest: this.sess.manifestSha };
          if (f !== "lostCommitReply") this.reply(5, seq, { ok: true, auto: !!body.auto });
          // the seal: the transport stops and comes back in the status-only mode
          setTimeout(() => {
            this.unplug();
            if (this.sc.after) setTimeout(() => this.u.attach(new Dev(this.u, this.sc, "status", this.n + 100)), 80);
          }, f === "lostCommitReply" ? 10 : 150);
        }
      } else violations.push(`frame type ${type}`);
      return { status: "ok", bytesWritten: b.length };
    }
  }

  class FakeUsb extends EventTarget {
    constructor(sc) {
      super();
      this.sc = sc;
      this.attached = [];
    }
    async getDevices() {
      return this.sc.permitted === false ? [] : this.attached.slice();
    }
    async requestDevice() {
      rec.push({ n: rec.length, op: "requestDevice" });
      throw new DOMException("No device selected.", "NotFoundError");
    }
    attach(d) {
      this.attached.push(d);
      rec.push({ n: rec.length, op: "attach", dev: d.n });
      const ev = new Event("connect");
      ev.device = d;
      this.dispatchEvent(ev);
    }
    detach(d) {
      this.attached = this.attached.filter((x) => x !== d);
      const ev = new Event("disconnect");
      ev.device = d;
      this.dispatchEvent(ev);
    }
  }

  function install(sc) {
    const u = new FakeUsb(sc);
    const nav = G.Navigator ? G.Navigator.prototype : G.navigator;
    Object.defineProperty(nav, "usb", { get: () => u, configurable: true });
    const n = sc.devices === undefined ? 1 : sc.devices;
    for (let i = 0; i < n; i++) u.attach(new Dev(u, sc, sc.mode || "payload", i + 1));
    G.__fakexfer = { rec, violations, committed: null };
  }
  G.FakeXfer = { install };
})();
