// SPDX-License-Identifier: GPL-2.0-or-later
// inpage.js: the driver's in-page part (uc-pxx-webdrive). A classic script that
// the driver injects into the page (page.addInitScript) before the page's own
// scripts run, after a script that sets globalThis.__WD_CFG. It changes nothing
// in the page's code. It:
//   1. logs every WebUSB call and transfer (frames decoded; file data counted),
//   2. is a second, transport-level guard of the driver's mode: it lets through
//      only the frames that mode allows, and refuses a frame BEFORE it is sent
//      (it never stops a transfer that has started, and never refuses a file's
//      data: a send runs to the end),
//   3. logs every change of the page's state (body data attributes, the
//      section texts, the page's own log lines).
// The driver reads the records with __wd.drain(). Nothing here talks to any
// server; there is no network (the container has none).
(function () {
  "use strict";
  const G = globalThis;
  const CFG = G.__WD_CFG;
  if (!CFG || typeof CFG !== "object") throw new Error("uc-pxx-webdrive: no __WD_CFG");
  const MODE = CFG.mode; // "status" | "step2" | "finish"
  const DRY = CFG.dryRun === true;
  const T = { HELLO: 1, PUT_BEGIN: 2, PUT_END: 4, COMMIT: 5, STATUS: 6, REPLY: 0x80 };
  const NAMES = Object.freeze({
    step2: CFG.payloadFiles || [], // [[name, size], ...] in the send order
    finish: CFG.finishFiles || [],
  });
  const buf = [];
  let seq = 0;
  const now = () => Date.now();
  function emit(rec) {
    rec.n = seq++;
    rec.t = now();
    buf.push(rec);
  }

  // ---- transport state (one session per SET_INTERFACE, as the unit) ----
  const st = {
    quiesced: false,
    outInflight: 0,
    inInflight: 0,
    inSince: 0,
    lastOutFrame: null, // the type of the last frame sent (its reply is next)
    session: 0,
    lastHello: null, // the last HELLO reply of this session
    put: null, // { name, size, sent } while an accepted PUT's data is sent
    putAsked: null, // the PUT_BEGIN sent, its reply not read yet
    sessionFiles: [], // names whose PUT_BEGIN this session sent
    unitSha: {}, // name -> the sha256 the unit's PUT_END gave (this session)
    unitOk: {}, // name -> PUT_END ok
    commits: 0,
    committed: null, // the COMMIT reply
    guards: [], // refusals by the guard
    requestDevice: 0,
  };

  function guardFail(why, extra) {
    const r = Object.assign({ op: "guard", why }, extra || {});
    st.guards.push(why);
    emit(r);
    return new DOMException(`uc-pxx-webdrive guard: ${why}`, "NetworkError");
  }

  const dec = new TextDecoder("utf-8", { fatal: true });
  function u8(data) {
    if (data instanceof ArrayBuffer) return new Uint8Array(data);
    if (ArrayBuffer.isView(data)) return new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
    return null;
  }
  // frame: the PXX1 frame in b, or null. Never throws.
  function frame(b) {
    if (!b || b.length < 16 || b.length > 16 + 65536 || b[0] !== 0x50 || b[1] !== 0x58 || b[2] !== 0x58 || b[3] !== 0x31) return null;
    const v = new DataView(b.buffer, b.byteOffset, b.byteLength);
    const type = v.getUint16(4, true), flags = v.getUint16(6, true), len = v.getUint32(8, true), fseq = v.getUint32(12, true);
    if (len !== b.length - 16) return { type, flags, len, seq: fseq, bad: "length" };
    let body = null;
    try {
      body = JSON.parse(dec.decode(b.subarray(16)));
    } catch {
      return { type, flags, len, seq: fseq, bad: "json" };
    }
    return { type, flags, len, seq: fseq, body };
  }
  const exactKeys = (o, keys) => o !== null && typeof o === "object" && !Array.isArray(o) &&
    Object.keys(o).length === keys.length && keys.every((k) => Object.prototype.hasOwnProperty.call(o, k));

  // allowOut decides on one OUT transfer before it is sent. It returns null
  // (send it) or the reason to refuse it. File data of an accepted PUT is
  // never refused.
  function allowOut(b) {
    if (DRY) return "a dry run sends nothing";
    if (st.put) return null; // the data of an accepted PUT: a send runs to the end
    const f = frame(b);
    if (!f || f.bad) return `an OUT transfer that is not a whole PXX1 frame (${b ? b.length : "?"} bytes)`;
    if (f.flags !== 0) return "a frame with flags";
    if (st.quiesced) return "the driver ended the run (quiesced): no new frame";
    if (st.putAsked) return "a frame before the reply to the PUT_BEGIN";
    if (f.type === T.HELLO) return exactKeys(f.body, []) ? null : "a HELLO with a body";
    if (f.type === T.STATUS) {
      if (MODE === "status") return "STATUS in the status mode (HELLO only)";
      return exactKeys(f.body, []) ? null : "a STATUS with a body";
    }
    if (MODE === "status") return `frame type ${f.type} in the status mode (HELLO only)`;
    const files = NAMES[MODE];
    if (f.type === T.PUT_BEGIN) {
      if (!exactKeys(f.body, ["name", "size"])) return "a PUT_BEGIN with other keys";
      const i = st.sessionFiles.length;
      if (i >= files.length) return "a PUT_BEGIN after the last file of the set";
      const [name, size] = files[i];
      if (f.body.name !== name || f.body.size !== size) {
        return `PUT_BEGIN ${JSON.stringify(f.body.name)} ${f.body.size}: the next file of the pinned set is ${name} ${size}`;
      }
      if (i === 0) {
        const h = st.lastHello;
        const wantMode = MODE === "step2" ? "payload" : "finish";
        const wait = MODE === "step2" ? "waiting-payload" : "waiting-finish";
        if (!h) return "the first PUT_BEGIN without a HELLO answer in this session";
        if (h.mode !== wantMode || h.status_only !== false || (h.state !== wait && h.state !== "receiving")) {
          return `the unit's HELLO is mode ${h.mode} state ${h.state} status_only ${h.status_only}, not ${wantMode} ${wait}`;
        }
        if (!Array.isArray(h.pins) || !h.pins.includes(CFG.firstSha)) return "the unit's HELLO pins do not name the pinned set";
        if (MODE === "finish" && h.pins.length !== 1) return "the finish unit names more than one finish list";
      } else {
        const prev = files[i - 1][0];
        if (st.unitOk[prev] !== true) return `PUT_BEGIN ${name} before the unit took ${prev}`;
      }
      return null;
    }
    if (f.type === T.COMMIT) {
      const auto = MODE === "step2";
      if (!exactKeys(f.body, ["auto"]) || f.body.auto !== auto) {
        return `COMMIT ${JSON.stringify(f.body)}: this mode commits only {"auto":${auto}}`;
      }
      if (st.commits > 0) return "a second COMMIT in this run";
      for (const [name, , sha] of files) {
        if (st.unitOk[name] !== true) return `COMMIT before the unit took ${name}`;
        if (st.unitSha[name] !== sha) return `the unit's sha256 of ${name} is ${st.unitSha[name]}, the pin is ${sha}`;
      }
      return null;
    }
    return `frame type ${f.type}`;
  }

  function noteOutSent(b) {
    if (st.put) {
      const p = st.put;
      const off = p.sent;
      p.sent += b.length;
      emit({ op: "out-data", name: p.name, off, len: b.length });
      if (p.sent >= p.size) st.put = null;
      return;
    }
    const f = frame(b);
    if (!f) return;
    st.lastOutFrame = f.type;
    if (f.type === T.PUT_BEGIN) {
      st.putAsked = { name: f.body.name, size: f.body.size };
      st.sessionFiles.push(f.body.name);
    }
    if (f.type === T.COMMIT) st.commits++;
    emit({ op: "out-frame", type: f.type, seq: f.seq, len: b.length, body: f.body });
  }

  function noteIn(b) {
    const f = frame(b);
    if (!f || f.bad) {
      emit({ op: "in-other", len: b ? b.length : 0 });
      return;
    }
    const body = f.body || {};
    const req = f.type & ~T.REPLY;
    emit({ op: "in-frame", type: f.type, seq: f.seq, len: b.length, body });
    if (req === T.HELLO) st.lastHello = body;
    if (req === T.PUT_BEGIN) {
      const a = st.putAsked;
      st.putAsked = null;
      if (a && body.accept === true && body.name === a.name) st.put = { name: a.name, size: a.size, sent: 0 };
      if (a && a.size === 0) st.put = null;
    }
    if (req === T.PUT_END) {
      if (typeof body.name === "string") {
        st.unitOk[body.name] = body.ok === true;
        st.unitSha[body.name] = typeof body.sha256 === "string" ? body.sha256 : "";
      }
    }
    if (req === T.COMMIT) st.committed = body;
  }

  function devInfo(d) {
    return { vid: d.vendorId, pid: d.productId, serial: d.serialNumber, product: d.productName, maker: d.manufacturerName };
  }

  const wrapped = new WeakSet();
  function def(o, k, fn) {
    Object.defineProperty(o, k, { value: fn, writable: false, configurable: false, enumerable: false });
  }
  function wrapDevice(d) {
    if (!d || typeof d !== "object" || wrapped.has(d)) return d;
    wrapped.add(d);
    const real = {};
    for (const m of ["open", "close", "selectConfiguration", "claimInterface", "releaseInterface", "selectAlternateInterface",
      "transferIn", "transferOut"]) {
      if (typeof d[m] === "function") real[m] = d[m].bind(d);
    }
    const info = devInfo(d);
    // the calls the page makes on the transfer device
    for (const m of ["open", "close", "selectConfiguration", "claimInterface", "releaseInterface"]) {
      if (!real[m]) continue;
      def(d, m, async (...a) => {
        if (DRY) throw guardFail(`${m}() in a dry run`, { dev: info });
        if (st.quiesced && m !== "close") throw guardFail(`${m}() after the driver ended the run`, { dev: info });
        const t0 = now();
        try {
          const r = await real[m](...a);
          emit({ op: m, args: a, dev: info, ms: now() - t0 });
          return r;
        } catch (e) {
          emit({ op: m, args: a, dev: info, ms: now() - t0, error: `${e && e.name}: ${e && e.message}` });
          throw e;
        }
      });
    }
    def(d, "selectAlternateInterface", async (i, alt) => {
      if (DRY) throw guardFail("selectAlternateInterface() in a dry run", { dev: info });
      if (st.quiesced) throw guardFail("selectAlternateInterface() after the driver ended the run", { dev: info });
      if (st.outInflight || st.inInflight || st.put) throw guardFail("SET_INTERFACE during a transfer", { dev: info });
      const t0 = now();
      try {
        const r = await real.selectAlternateInterface(i, alt);
        st.session++;
        st.lastHello = null;
        st.put = null;
        st.putAsked = null;
        st.sessionFiles = [];
        st.unitSha = {};
        st.unitOk = {};
        emit({ op: "set-interface", iface: i, alt, session: st.session, dev: info, ms: now() - t0 });
        return r;
      } catch (e) {
        emit({ op: "set-interface", iface: i, alt, dev: info, ms: now() - t0, error: `${e && e.name}: ${e && e.message}` });
        throw e;
      }
    });
    def(d, "transferOut", async (ep, data) => {
      const b = u8(data);
      const why = allowOut(b);
      if (why) throw guardFail(why, { ep, len: b ? b.length : null });
      st.outInflight++;
      const t0 = now();
      try {
        const r = await real.transferOut(ep, data);
        if (r && r.status === "ok" && r.bytesWritten === b.length) noteOutSent(b);
        else emit({ op: "out-result", ep, len: b.length, status: r && r.status, written: r && r.bytesWritten });
        if (now() - t0 > 2000) emit({ op: "out-slow", ep, len: b.length, ms: now() - t0 });
        return r;
      } catch (e) {
        emit({ op: "out-error", ep, len: b.length, ms: now() - t0, put: st.put ? Object.assign({}, st.put) : null,
          error: `${e && e.name}: ${e && e.message}` });
        st.put = null; // the link is broken: the page opens a new session (SET_INTERFACE) before any next frame
        st.putAsked = null;
        throw e;
      } finally {
        st.outInflight--;
      }
    });
    def(d, "transferIn", async (ep, length) => {
      if (DRY) throw guardFail("transferIn() in a dry run", { ep });
      if (st.quiesced && !st.lastOutFrame) throw guardFail("transferIn() after the driver ended the run", { ep });
      st.inInflight++;
      st.inSince = now();
      const want = st.lastOutFrame;
      st.lastOutFrame = null;
      const t0 = now();
      try {
        const r = await real.transferIn(ep, length);
        const dv = r && r.data;
        const b = dv && dv.buffer ? new Uint8Array(dv.buffer, dv.byteOffset, dv.byteLength) : null;
        if (r && r.status === "ok" && b) noteIn(b);
        else emit({ op: "in-result", ep, length, status: r && r.status, for: want });
        return r;
      } catch (e) {
        emit({ op: "in-error", ep, length, ms: now() - t0, for: want, error: `${e && e.name}: ${e && e.message}` });
        st.put = null;
        st.putAsked = null;
        throw e;
      } finally {
        st.inInflight--;
        st.inSince = 0;
      }
    });
    // calls the page never makes on this device: refused, never sent
    for (const m of ["reset", "forget", "clearHalt", "controlTransferIn", "controlTransferOut", "isochronousTransferIn",
      "isochronousTransferOut"]) {
      def(d, m, async () => {
        throw guardFail(`${m}() (the page's step-2 and finish sections never make it)`, { dev: info });
      });
    }
    return d;
  }

  const usb = G.navigator && G.navigator.usb;
  if (usb) {
    const realGet = usb.getDevices.bind(usb);
    def(usb, "getDevices", async () => {
      const list = await realGet();
      const out = list.map(wrapDevice);
      emit({ op: "getDevices", count: out.length, devices: out.map(devInfo) });
      return out;
    });
    def(usb, "requestDevice", async (opt) => {
      st.requestDevice++;
      emit({ op: "requestDevice", filters: opt && opt.filters });
      // the policy grants the device: the chooser is never needed, and a
      // headless browser cannot show it
      throw new DOMException("uc-pxx-webdrive: no chooser (the device comes from the policy)", "NotFoundError");
    });
    usb.addEventListener("connect", (ev) => emit({ op: "event-connect", dev: devInfo(ev.device) }));
    usb.addEventListener("disconnect", (ev) => emit({ op: "event-disconnect", dev: devInfo(ev.device) }));
  } else {
    emit({ op: "no-webusb" });
  }

  // ---- the page's state ----
  const TEXT_IDS = ["browser-status", "notice", "binding", "busy", "stop-message", "stop-advice", "goal-status",
    "step2-folder-status", "step2-fetch-status", "step2-status", "step2-lines", "finish-folder-status", "finish-status",
    "finish-lines", "xstatus-text", "xstatus-lines", "unit-status", "fastboot-status"];
  const HIDDEN_IDS = ["stop", "gate", "busy", "step2-section", "finish-section", "xstatus-section", "step2-fetch-row",
    "step2-progress", "finish-progress"];
  const CTRL_IDS = ["step2-fetch", "step2-auto", "step2-connect", "step2-folder", "finish-folder", "finish-connect", "xstatus-read"];
  function snapshot() {
    const $ = (id) => document.getElementById(id);
    const s = { ds: Object.assign({}, document.body ? document.body.dataset : {}), text: {}, cls: {}, hidden: {}, ctl: {}, prog: {}, xrows: [] };
    for (const id of TEXT_IDS) {
      const e = $(id);
      if (e) {
        s.text[id] = e.textContent;
        s.cls[id] = e.className;
      }
    }
    for (const id of HIDDEN_IDS) {
      const e = $(id);
      if (e) s.hidden[id] = e.hidden;
    }
    for (const id of CTRL_IDS) {
      const e = $(id);
      if (e) s.ctl[id] = { disabled: e.disabled, checked: e.type === "checkbox" ? e.checked : undefined };
    }
    for (const id of ["step2-progress", "finish-progress"]) {
      const e = $(id);
      if (e) s.prog[id] = { value: e.value, max: e.max };
    }
    const tb = $("xstatus-table");
    if (tb) for (const tr of tb.querySelectorAll("tr")) s.xrows.push([tr.cells[0] && tr.cells[0].textContent, tr.cells[1] && tr.cells[1].textContent]);
    return s;
  }
  let last = "";
  let logDone = 0;
  function observe() {
    const s = snapshot();
    // the progress values and the "Sending ..." numbers change with every MiB:
    // logged as progress, not as a new state
    const norm = (t) => (/^Sending /.test(t || "") ? (t || "").replace(/[0-9.]+/g, "#") : t);
    const key = JSON.stringify(Object.assign({}, s, { prog: undefined,
      text: Object.assign({}, s.text, { "step2-status": norm(s.text["step2-status"]), "finish-status": norm(s.text["finish-status"]) }) }));
    const sendText = (s.text["step2-status"] || "") + "|" + (s.text["finish-status"] || "");
    if (key !== last) {
      last = key;
      emit({ op: "state", s });
    } else if (sendText !== observe.lastSend) {
      emit({ op: "progress", step2: s.text["step2-status"], finish: s.text["finish-status"], prog: s.prog });
    }
    observe.lastSend = sendText;
    const lg = document.getElementById("log");
    if (lg) {
      const lines = lg.textContent.split("\n");
      if (lines.length - 1 > logDone) {
        for (let i = logDone; i < lines.length - 1; i++) emit({ op: "page-log", line: lines[i] });
        logDone = lines.length - 1;
      }
    }
  }
  observe.lastSend = "";
  let pending = false;
  function schedule() {
    if (pending) return;
    pending = true;
    queueMicrotask(() => {
      pending = false;
      try {
        observe();
      } catch (e) {
        emit({ op: "observe-error", error: String(e) });
      }
    });
  }
  document.addEventListener("DOMContentLoaded", () => {
    new MutationObserver(schedule).observe(document.body, { attributes: true, childList: true, subtree: true, characterData: true });
    schedule();
  });

  Object.defineProperty(G, "__wd", {
    value: Object.freeze({
      drain: () => buf.splice(0, buf.length),
      snapshot,
      inflight: () => ({ out: st.outInflight, in: st.inInflight, inSince: st.inSince, put: st.put ? Object.assign({}, st.put) : null }),
      quiesce: () => {
        st.quiesced = true;
        emit({ op: "quiesce" });
      },
      transport: () => JSON.parse(JSON.stringify({ session: st.session, lastHello: st.lastHello, unitSha: st.unitSha, unitOk: st.unitOk,
        commits: st.commits, committed: st.committed, guards: st.guards, requestDevice: st.requestDevice })),
    }),
    writable: false,
    configurable: false,
  });
})();
