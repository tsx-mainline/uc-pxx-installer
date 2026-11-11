// SPDX-License-Identifier: GPL-2.0-or-later
// The finish client: the strict finish.json rule, the folder rule, the page's
// early checks, the HELLO decision, the texts of the unit's info codes, and a
// session against a model of the unit's finish receiver.
import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import {
  checkHello, checkSet, FILES, findSets, finishText, MAX_FILE, parseList, sendSet, statusRows, Step2Link, T,
} from "../../site/js/finishusb.js";
import { DATA_CHUNK, decodeFrame, encodeFrame } from "../../site/js/step2usb.js";

const sha = (b) => createHash("sha256").update(b).digest("hex");
const enc = (o) => new TextEncoder().encode(typeof o === "string" ? o : JSON.stringify(o));

function file(path, bytes) {
  const f = new File([bytes], path.slice(path.lastIndexOf("/") + 1));
  Object.defineProperty(f, "webkitRelativePath", { value: path });
  return f;
}

function set(dir, { bin = DATA_CHUNK + 99, listSizes = null } = {}) {
  const b = new Uint8Array(bin).map((_, i) => (i * 7 + 1) & 0xff);
  const c = new Uint8Array(5000).map((_, i) => (i * 11) & 0xff);
  const obj = { kind: "uc-pxx-finish", version: 1, files: [
    { name: "uc-pxx-step2-finish", size: listSizes ? listSizes[0] : b.length, sha256: sha(b) },
    { name: "uc-pxx-bootctl-inst", size: listSizes ? listSizes[1] : c.length, sha256: sha(c) },
  ] };
  const list = enc(obj);
  return { b, c, list, obj, sha: sha(list),
    files: [file(`${dir}/finish.json`, list), file(`${dir}/uc-pxx-step2-finish`, b), file(`${dir}/uc-pxx-bootctl-inst`, c)] };
}

test("parseList: the strict rule of handover.ParseFinishList", () => {
  const s = set("r/uc-pxx-finish");
  const l = parseList(s.list);
  assert.deepEqual(l.files.map((f) => f.name), FILES);
  assert.ok(Object.isFrozen(l) && Object.isFrozen(l.files));
  const bad = (o, re) => assert.throws(() => parseList(o instanceof Uint8Array ? o : enc(o)), re);
  const f = s.obj.files;
  bad(new Uint8Array(0), /0 bytes/);
  bad(new Uint8Array(4097).fill(0x20), /4097 bytes/);
  bad("{", /not JSON/);
  bad(new Uint8Array([0x7b, 0xff, 0x7d]), /not JSON/);
  bad([1], /other keys/);
  bad({ ...s.obj, extra: 1 }, /other keys/);
  bad({ kind: "uc-pxx-finish", version: 1 }, /other keys/);
  bad({ ...s.obj, kind: "uc-pxx-mainline" }, /kind "uc-pxx-mainline"/);
  bad({ ...s.obj, version: 2 }, /version 2/);
  bad({ ...s.obj, version: "1" }, /version "1"/);
  bad({ ...s.obj, files: [f[0]] }, /exactly the 2 files/);
  bad({ ...s.obj, files: [f[0], f[1], f[1]] }, /exactly the 2 files/);
  bad({ ...s.obj, files: [f[1], f[0]] }, /file 0 is not uc-pxx-step2-finish/);
  bad({ ...s.obj, files: [{ ...f[0], size: 0 }, f[1]] }, /file 0/);
  bad({ ...s.obj, files: [{ ...f[0], size: MAX_FILE + 1 }, f[1]] }, /file 0/);
  bad({ ...s.obj, files: [{ ...f[0], size: 1.5 }, f[1]] }, /file 0/);
  bad({ ...s.obj, files: [{ ...f[0], size: "5" }, f[1]] }, /file 0/);
  bad({ ...s.obj, files: [f[0], { ...f[1], sha256: f[1].sha256.toUpperCase() }] }, /file 1/);
  bad({ ...s.obj, files: [f[0], { ...f[1], sha256: f[1].sha256.slice(1) }] }, /file 1/);
  bad({ ...s.obj, files: [f[0], { ...f[1], mode: 493 }] }, /file 1/);
  bad({ ...s.obj, files: [f[0], { name: f[1].name, size: f[1].size }] }, /file 1/);
});

test("findSets: the uc-pxx-finish folder picked itself or below the picked folder; other files are named", () => {
  const a = set("uc-pxx-finish");
  assert.deepEqual(findSets(a.files).map((c) => [c.dir, Object.keys(c.files).sort(), c.other]),
    [["uc-pxx-finish", ["finish.json", "uc-pxx-bootctl-inst", "uc-pxx-step2-finish"], []]]);
  const b = set("rel/v1/uc-pxx-finish");
  const extra = [file("rel/v1/uc-pxx-finish/notes.txt", new Uint8Array(1)), file("rel/v1/uc-pxx-finish/sub/finish.json", a.list),
    file("rel/v1/uc-pxx-mainline/manifest.json", new Uint8Array(2)), file("rel/uc-pxx-finish-old/finish.json", a.list)];
  const c = findSets([...b.files, ...extra]);
  assert.equal(c.length, 1);
  assert.equal(c[0].dir, "rel/v1/uc-pxx-finish");
  assert.deepEqual(c[0].other.sort(), ["notes.txt", "sub/finish.json"]);
  assert.deepEqual(findSets([]), []);
});

test("checkSet: exactly the three files, the list, the sizes, the sha256s, the page's pins", async () => {
  const s = set("r/uc-pxx-finish");
  const pins = [{ label: "t", sha256: s.sha }];
  const [cand] = findSets(s.files);
  const ok = await checkSet(cand, pins);
  assert.equal(ok.ok, true);
  assert.equal(ok.sha, s.sha);
  assert.equal(ok.total, s.list.length + s.b.length + s.c.length);
  assert.match((await checkSet(cand, [])).why, /pins no finish set yet/);
  assert.match((await checkSet(cand, [{ label: "x", sha256: "0".repeat(64) }])).why, /is not a finish set of this page/);
  const extra = findSets([...s.files, file("r/uc-pxx-finish/.DS_Store", new Uint8Array(3))])[0];
  assert.match((await checkSet(extra, pins)).why, /holds other files \(\.DS_Store\)/);
  assert.match((await checkSet(findSets(s.files.slice(0, 2))[0], pins)).why, /has no uc-pxx-bootctl-inst/);
  const sized = set("r/uc-pxx-finish", { listSizes: [DATA_CHUNK + 98, 5000] });
  const r1 = await checkSet(findSets(sized.files)[0], [{ label: "t", sha256: sized.sha }]);
  assert.match(r1.why, /uc-pxx-step2-finish has 1048675 bytes, finish.json says 1048674/);
  const changed = s.b.slice();
  changed[10] ^= 1;
  const r2 = await checkSet(findSets([s.files[0], file("r/uc-pxx-finish/uc-pxx-step2-finish", changed), s.files[2]])[0], pins);
  assert.match(r2.why, /uc-pxx-step2-finish has sha256 [0-9a-f]{64}, finish.json says/);
  const notjson = await checkSet(findSets([file("r/uc-pxx-finish/finish.json", enc("{")), s.files[1], s.files[2]])[0], pins);
  assert.match(notjson.why, /not JSON/);
  const big = await checkSet(findSets([file("r/uc-pxx-finish/finish.json", new Uint8Array(5000)), s.files[1], s.files[2]])[0], pins);
  assert.match(big.why, /5000 bytes/);
});

test("checkHello: only a finish-mode unit that pins exactly this list gets a byte", () => {
  const s = "a".repeat(64), lists = [{ label: "t", sha256: s }];
  const h = (o) => ({ proto: 1, mode: "finish", state: "waiting-finish", pins: [s], info: { step2: "finish: waiting" }, ...o });
  assert.deepEqual(checkHello(h({}), s, lists), { go: "send" });
  assert.deepEqual(checkHello(h({ state: "receiving" }), s, lists), { go: "send" });
  const stop = (o, re, sh = s, ls = lists) => {
    const d = checkHello(o, sh, ls);
    assert.equal(d.go, "stop", JSON.stringify(o));
    assert.match(d.why, re);
  };
  stop(h({ mode: "payload", pins: [s] }), /waits for the step-2 payload/);
  stop(h({ mode: "status", state: "status-only", pins: [], info: { step2: "idle" } }), /takes no finish set now \(status only: idle\)/);
  stop(h({ mode: "status", state: "status-only", pins: [], info: { step2: "finish: done" } }), /status only: finish: done/);
  stop(h({ mode: undefined }), /mode undefined/);
  stop(h({ mode: "Finish" }), /mode "Finish"/);
  stop(h({ state: "starting" }), /state starting/);
  stop(h({ pins: [] }), /exactly one finish list/);
  stop(h({ pins: [s, s] }), /exactly one finish list/);
  stop(h({ pins: ["A".repeat(64)] }), /exactly one finish list/);
  stop(h({ pins: "x" }), /exactly one finish list/);
  stop(h({ pins: ["b".repeat(64)] }), /not the one this unit's slot names/);
  stop(h({}), /not a finish set of this page/, s, []);
  stop(null, /not a HELLO/);
  for (const c of ["finish: checking", "finish: started", "finish: not-yet"]) {
    assert.equal(checkHello(h({ mode: "status", state: "status-only", pins: [], info: { step2: c } }), s, lists).go, "watch", c);
  }
  assert.equal(checkHello(h({ state: "finish-complete" }), s, lists).go, "watch");
});

test("finishText: each info code of the finish to its text and end", () => {
  const t = (code, lines, state = "status-only") => finishText({ state, info: { step2: code, lines } });
  const end = (code, outcome, re, cls) => {
    const r = t(code, ["the reason line"]);
    assert.equal(r.end, true, code);
    assert.equal(r.outcome, outcome, code);
    assert.match(r.text, re, code);
    if (cls) assert.equal(r.cls, cls, code);
  };
  end("finish: done", "finished", /^Finished/, "ok");
  end("finish: switched", "finished", /^Finished/, "ok");
  end("finish: supervised", "supervised", /a supervised unit: the finish runs only by the supervised procedure/i);
  end("finish: not a target", "not-target", /This unit is not a step-2 target: nothing to finish/);
  end("finish: refused", "refused", /The unit refused the finish: the reason line/);
  end("stop: the check after step 2", "stop", /KEEP THE UNIT POWERED\. Do not restart it/, "stop");
  end("stop: step 2 stopped", "stop", /KEEP THE UNIT POWERED/, "stop");
  end("hold: ab state not known", "stop", /KEEP THE UNIT POWERED.*ask for help/, "stop");
  for (const [code, re] of [["finish: not-yet", /Waiting for the unit to mark itself/], ["finish: checking", /checks the finish set/],
    ["finish: started", /The finish runs on the unit/], ["finish: waiting", /waits for the finish set/], ["idle", /status: idle/],
    ["", /status: none/], ["stopped", /status: stopped/]]) {
    const r = t(code, []);
    assert.equal(r.end, false, code);
    assert.match(r.text, re, code);
  }
  assert.match(t("finish: waiting", [], "finish-complete").text, /has the finish set: it checks it/);
  assert.match(finishText({ state: "status-only", info: { step2: "finish: refused" } }).text, /no reason given/);
  assert.equal(finishText(null).end, false);
});

test("statusRows: only the named fields; nothing else of the HELLO", () => {
  const rows = statusRows({ mode: "finish", state: "waiting-finish", detail: "d", pins: ["x"], mem_available: 5, serial: "NOPE",
    info: { model: "UC-P10", slot: "b", token: "pxx-ab12cd", step2: "finish: waiting", plan_id: "0123456789ab", mac: "NOPE" } });
  assert.deepEqual(rows, [["Model", "UC-P10"], ["Slot", "B"], ["Build token", "pxx-ab12cd"], ["Interface mode", "finish"],
    ["Interface state", "waiting-finish (d)"], ["Step 2", "finish: waiting"], ["Plan id", "0123456789ab"]]);
  assert.ok(!JSON.stringify(statusRows({ mode: "status" })).includes("NOPE"));
  assert.deepEqual(statusRows({}).map((r) => r[1]), ["-", "-", "-", "-", "-", "-", "-"]);
});

// A model of the unit's finish receiver behind a fake WebUSB device.
function fakeUnit(opt = {}) {
  const rec = [];
  let pending = null;
  const replyQ = [], waiters = [];
  const want = {};
  let gone = false;
  const reply = (type, seq, obj) => {
    const f = encodeFrame(type | T.REPLY, seq, obj);
    const w = waiters.shift();
    if (w) w.res(f); else replyQ.push(f);
  };
  const dev = {
    opened: false, configuration: null,
    configurations: [{ interfaces: [{ interfaceNumber: 0, alternates: [{ alternateSetting: 0, interfaceClass: 0xff, interfaceSubclass: 0x50,
      interfaceProtocol: 1, endpoints: [{ type: "bulk", direction: "in", endpointNumber: 2 }, { type: "bulk", direction: "out", endpointNumber: 1 }] }] }] }],
    async open() { this.opened = true; },
    async selectConfiguration() { this.configuration = this.configurations[0]; },
    async claimInterface() {},
    async selectAlternateInterface(i, a) { rec.push({ op: "set-alt", i, a }); },
    async transferOut(ep, data) {
      if (gone) throw new DOMException("gone", "NetworkError");
      const b = new Uint8Array(data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength));
      if (pending) {
        rec.push({ op: "data", name: pending.name, n: b.length });
        pending.got.push(b);
        pending.have += b.length;
        if (pending.have >= pending.size) {
          const all = Buffer.concat(pending.got.map((x) => Buffer.from(x)));
          const s = sha(all), p = pending;
          pending = null;
          if (p.name === "finish.json") {
            const l = JSON.parse(all.toString());
            for (const f of l.files) want[f.name] = f;
            reply(T.PUT_END, p.seq, { ok: s === opt.pin, name: p.name, sha256: s, reason: s === opt.pin ? undefined : "not this slot's own finish list" });
          } else {
            const ok = s === want[p.name].sha256 && !(opt.badEnd === p.name);
            reply(T.PUT_END, p.seq, { ok, name: p.name, sha256: s, reason: ok ? undefined : "sha256 is not the one finish.json names" });
          }
        }
        return { status: "ok", bytesWritten: b.length };
      }
      const f = decodeFrame(b);
      rec.push({ op: "frame", type: f.type, seq: f.seq, body: f.body, len: b.length });
      if (f.type === T.HELLO) reply(T.HELLO, f.seq, { proto: 1, mode: "finish", state: "waiting-finish", pins: [opt.pin] });
      else if (f.type === T.PUT_BEGIN) {
        if (opt.refuse === f.body.name) reply(T.PUT_BEGIN, f.seq, { accept: false, name: f.body.name, reason: "no" });
        else {
          reply(T.PUT_BEGIN, f.seq, { accept: true, name: f.body.name });
          pending = { name: f.body.name, size: f.body.size, have: 0, got: [], seq: f.seq };
        }
      } else if (f.type === T.COMMIT) {
        if (opt.lostCommit) {
          gone = true;
          for (const w of waiters.splice(0)) w.rej(new DOMException("gone", "NetworkError"));
        } else reply(T.COMMIT, f.seq, f.body.auto === false ? { ok: true, auto: false } : { ok: false, auto: true, reason: "auto must be false" });
      }
      return { status: "ok", bytesWritten: b.length };
    },
    async transferIn(ep, length) {
      if (gone) throw new DOMException("gone", "NetworkError");
      const f = replyQ.length ? replyQ.shift() : await new Promise((res, rej) => waiters.push({ res, rej }));
      return { status: "ok", data: new DataView(f.buffer) };
    },
  };
  return { dev, rec };
}

const CFG = { cls: 0xff, sub: 0x50, proto: 1 };

async function session(opt, mut) {
  const s = set("r/uc-pxx-finish");
  const [cand] = findSets(s.files);
  const chk = await checkSet(cand, [{ label: "t", sha256: s.sha }]);
  assert.equal(chk.ok, true);
  const u = fakeUnit({ pin: s.sha, ...opt });
  const link = await Step2Link.open(u.dev, CFG, () => {});
  const h = await link.hello();
  // (a unit that pins another list: the page would stop here; opt.pin tests the unit's own PUT_END refusal)
  if (!opt.pin) assert.deepEqual(checkHello(h, chk.sha, [{ sha256: s.sha }]), { go: "send" });
  if (mut) mut(cand, s);
  const r = await sendSet(link, cand, chk.list, null, () => {});
  return { r, u, s };
}

test("a session: finish.json, the two files in 1 MiB transfers, COMMIT with auto false", async () => {
  const { r, u, s } = await session({});
  assert.deepEqual(r, { ok: true });
  const frames = u.rec.filter((e) => e.op === "frame");
  assert.deepEqual(frames.map((e) => [e.type, e.body.name || null]),
    [[1, null], [2, "finish.json"], [2, "uc-pxx-step2-finish"], [2, "uc-pxx-bootctl-inst"], [5, null]]);
  assert.deepEqual(frames[4].body, { auto: false });
  assert.deepEqual(frames.slice(1, 4).map((e) => e.body.size), [s.list.length, s.b.length, s.c.length]);
  for (const f of frames) assert.notEqual(f.len % 64, 0);
  assert.deepEqual(u.rec.filter((e) => e.op === "data").map((e) => e.n), [s.list.length, DATA_CHUNK, 99, 5000]);
});

test("a refusal or a failed check stops the session; no COMMIT", async () => {
  for (const opt of [{ refuse: "uc-pxx-bootctl-inst" }, { badEnd: "uc-pxx-step2-finish" }, { pin: "0".repeat(64) }]) {
    const { r, u } = await session(opt);
    assert.equal(r.ok, false, JSON.stringify(opt));
    assert.equal(u.rec.filter((e) => e.op === "frame" && e.type === T.COMMIT).length, 0);
  }
  const { r, u } = await session({ refuse: "uc-pxx-bootctl-inst" });
  assert.match(r.why, /refused uc-pxx-bootctl-inst: no/);
  assert.deepEqual(u.rec.filter((e) => e.op === "data").map((e) => e.name), ["finish.json", "uc-pxx-step2-finish", "uc-pxx-step2-finish"]);
});

test("a file that changed size after the check: nothing is sent", async () => {
  const { r, u } = await session({}, (cand) => {
    cand.files["uc-pxx-bootctl-inst"] = file("r/uc-pxx-finish/uc-pxx-bootctl-inst", new Uint8Array(4999));
  });
  assert.equal(r.ok, false);
  assert.match(r.why, /uc-pxx-bootctl-inst has 4999 bytes, finish.json says 5000/);
  assert.equal(u.rec.filter((e) => e.op === "frame" && e.type === T.PUT_BEGIN).length, 0);
});

test("the link ends at the COMMIT: the result is unknown (the status decides), never a resend", async () => {
  const { r, u } = await session({ lostCommit: true });
  assert.equal(r.ok, "unknown");
  assert.match(r.why, /the link ended at the COMMIT/);
  assert.equal(u.rec.filter((e) => e.op === "frame" && e.type === T.COMMIT).length, 1);
});
