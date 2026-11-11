// SPDX-License-Identifier: GPL-2.0-or-later
// The step-2 USB client: frames, the folder rule, the early checks, and a
// session against a model of the unit's receiver.
import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import {
  checkPayload, DATA_CHUNK, decodeFrame, encodeFrame, findPayloads, FRAME_READ, sendPayload, Step2Error, Step2Link, T,
} from "../../site/js/step2usb.js";

const sha = (b) => createHash("sha256").update(b).digest("hex");

test("encodeFrame: the header, the JSON body, never a multiple of 64 bytes", () => {
  for (let n = 0; n < 2000; n++) {
    const f = encodeFrame(T.HELLO, 7, { x: "a".repeat(n) });
    assert.notEqual(f.length % 64, 0, `body ${n}`);
    const d = decodeFrame(f);
    assert.equal(d.type, T.HELLO);
    assert.equal(d.seq, 7);
    assert.equal(d.body.x.length, n);
  }
  const f = encodeFrame(T.PUT_BEGIN, 1, { name: "rootfs.ext4", size: 5 });
  assert.deepEqual([...f.subarray(0, 4)], [0x50, 0x58, 0x58, 0x31]);
  assert.equal(new DataView(f.buffer).getUint16(6, true), 0);
  assert.throws(() => encodeFrame(1, 1, { x: "a".repeat(70000) }), Step2Error);
});

test("decodeFrame refuses every broken reply", () => {
  const good = encodeFrame(T.HELLO | T.REPLY, 3, { state: "waiting-payload" });
  const bad = (f) => assert.throws(() => decodeFrame(f), Step2Error);
  bad(good.subarray(0, 15));
  const m = good.slice(); m[0] = 0x51; bad(m);
  const fl = good.slice(); fl[6] = 1; bad(fl);
  const ln = good.slice(); new DataView(ln.buffer).setUint32(8, 2, true); bad(ln);
  bad(new Uint8Array([...good, 0x20]));
  bad(encodeFrame(1, 1, [1, 2]));
  bad(encodeFrame(1, 1, "text"));
  const nj = encodeFrame(1, 1, {}); nj[16] = 0x7b; nj[17] = 0x7b; bad(nj);
});

function file(path, bytes) {
  const f = new File([bytes], path.slice(path.lastIndexOf("/") + 1));
  Object.defineProperty(f, "webkitRelativePath", { value: path });
  return f;
}

function payload(dir, rootfsSize = 3 * DATA_CHUNK + 777) {
  const tmpl = new Uint8Array(6144).map((_, i) => (i * 7) & 0xff);
  const root = new Uint8Array(rootfsSize).map((_, i) => (i * 13 + 5) & 0xff);
  const man = new TextEncoder().encode(JSON.stringify({
    kind: "uc-pxx-mainline", version: "v0.0.1-test",
    boot_template: { file: "boot-template.img", size: tmpl.length, sha256: sha(tmpl) },
    rootfs: { file: "rootfs.ext4", size: root.length, sha256: sha(root) },
  }));
  return {
    man, tmpl, root,
    files: [file(`${dir}/manifest.json`, man), file(`${dir}/boot-template.img`, tmpl), file(`${dir}/rootfs.ext4`, root)],
  };
}

test("findPayloads: only folders named uc-pxx-mainline with the three files", () => {
  const a = payload("rel/normal/uc-pxx-mainline");
  const b = payload("rel/step2/uc-pxx-mainline");
  const extra = [file("rel/uc-pxx-mainline-x/manifest.json", a.man), file("rel/other/manifest.json", a.man),
    file("rel/half/uc-pxx-mainline/manifest.json", a.man)];
  const c = findPayloads([...a.files, ...b.files, ...extra]);
  assert.deepEqual(c.map((x) => x.dir).sort(), ["rel/normal/uc-pxx-mainline", "rel/step2/uc-pxx-mainline"]);
  assert.equal(findPayloads([]).length, 0);
});

test("checkPayload: the page's pin, the manifest's files and sizes", async () => {
  const a = payload("r/uc-pxx-mainline");
  const [cand] = findPayloads(a.files);
  const pin = { label: "test", manifest_sha256: sha(a.man) };
  const ok = await checkPayload(cand, [pin]);
  assert.equal(ok.ok, true);
  assert.equal(ok.sha, sha(a.man));
  assert.equal(ok.total, a.man.length + a.tmpl.length + a.root.length);
  const no = await checkPayload(cand, [{ label: "x", manifest_sha256: "0".repeat(64) }]);
  assert.equal(no.ok, false);
  assert.match(no.why, /is not a step-2 payload of this page/);
  const short = findPayloads([a.files[0], a.files[1], file("r/uc-pxx-mainline/rootfs.ext4", a.root.subarray(1))])[0];
  assert.match((await checkPayload(short, [pin])).why, /sizes are not the manifest's/);
  const big = findPayloads([file("r/uc-pxx-mainline/manifest.json", new Uint8Array(70000)), a.files[1], a.files[2]])[0];
  assert.match((await checkPayload(big, [pin])).why, /70000 bytes/);
});

// A model of the unit's receiver behind a fake WebUSB device (the Go
// receiver is tested by itself; this checks the client's side of the
// protocol: the order, the chunk sizes, the seq, the answers).
function fakeUnit(opt = {}) {
  const rec = [];
  let pending = null, replyQ = [], waiters = [];
  const want = {};
  const reply = (type, seq, obj) => {
    const f = encodeFrame(type | T.REPLY, seq, obj);
    const w = waiters.shift();
    if (w) w(f); else replyQ.push(f);
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
      const b = new Uint8Array(data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength));
      if (pending) {
        rec.push({ op: "data", n: b.length });
        pending.got.push(b);
        pending.have += b.length;
        if (opt.cutAt && pending.have >= opt.cutAt) throw new DOMException("cut", "NetworkError");
        if (pending.have >= pending.size) {
          const all = Buffer.concat(pending.got.map((x) => Buffer.from(x)));
          const s = sha(all), p = pending;
          pending = null;
          if (p.name === "manifest.json") {
            const m = JSON.parse(all.toString());
            want["boot-template.img"] = m.boot_template; want["rootfs.ext4"] = m.rootfs;
            reply(T.PUT_END, p.seq, { ok: true, name: p.name, sha256: s });
          } else reply(T.PUT_END, p.seq, { ok: s === want[p.name].sha256, name: p.name, sha256: s, reason: s === want[p.name].sha256 ? undefined : "sha" });
        }
        return { status: "ok", bytesWritten: b.length };
      }
      const f = decodeFrame(b);
      rec.push({ op: "frame", type: f.type, seq: f.seq, body: f.body, len: b.length });
      if (f.type === T.HELLO) reply(T.HELLO, f.seq, { proto: 1, state: "waiting-payload", pins: opt.pins || [] });
      else if (f.type === T.PUT_BEGIN) {
        if (opt.refuse === f.body.name) reply(T.PUT_BEGIN, f.seq, { accept: false, name: f.body.name, reason: "no" });
        else {
          reply(T.PUT_BEGIN, f.seq, { accept: true, name: f.body.name });
          pending = { name: f.body.name, size: f.body.size, have: 0, got: [], seq: f.seq };
        }
      } else if (f.type === T.COMMIT) reply(T.COMMIT, f.seq, { ok: true, auto: f.body.auto });
      else if (f.type === T.STATUS) reply(T.STATUS, f.seq, { state: "payload-complete" });
      return { status: "ok", bytesWritten: b.length };
    },
    async transferIn(ep, length) {
      rec.push({ op: "in", length });
      const f = replyQ.length ? replyQ.shift() : await new Promise((r) => waiters.push(r));
      return { status: "ok", data: new DataView(f.buffer) };
    },
  };
  return { dev, rec };
}

const CFG = { cls: 0xff, sub: 0x50, proto: 1 };

test("a session: SET_INTERFACE first, HELLO, the three PUTs in 1 MiB transfers, COMMIT with the person's choice", async () => {
  const a = payload("r/uc-pxx-mainline");
  const [cand] = findPayloads(a.files);
  const u = fakeUnit({ pins: [sha(a.man)] });
  const link = await Step2Link.open(u.dev, CFG, () => {});
  assert.equal(u.rec[0].op, "set-alt");
  const h = await link.hello();
  assert.equal(h.state, "waiting-payload");
  const r = await sendPayload(link, cand, true, null, () => {});
  assert.deepEqual(r, { ok: true });
  const frames = u.rec.filter((e) => e.op === "frame");
  assert.deepEqual(frames.map((e) => [e.type, e.body.name]), [[1, undefined], [2, "manifest.json"], [2, "boot-template.img"], [2, "rootfs.ext4"], [5, undefined]]);
  assert.equal(frames[4].body.auto, true);
  for (const f of frames) assert.notEqual(f.len % 64, 0);
  assert.deepEqual(frames.map((f) => f.seq), [1, 2, 3, 4, 5]);
  const data = u.rec.filter((e) => e.op === "data").map((e) => e.n);
  assert.deepEqual(data, [a.man.length, a.tmpl.length, DATA_CHUNK, DATA_CHUNK, DATA_CHUNK, 777]);
  for (const e of u.rec.filter((x) => x.op === "in")) assert.equal(e.length, FRAME_READ);
});

test("a refusal sends no byte of the file", async () => {
  const a = payload("r/uc-pxx-mainline");
  const [cand] = findPayloads(a.files);
  const u = fakeUnit({ refuse: "rootfs.ext4" });
  const link = await Step2Link.open(u.dev, CFG, () => {});
  const r = await sendPayload(link, cand, false, null, () => {});
  assert.equal(r.ok, false);
  assert.match(r.why, /refused rootfs\.ext4: no/);
  const data = u.rec.filter((e) => e.op === "data").map((e) => e.n);
  assert.deepEqual(data, [a.man.length, a.tmpl.length]);
  assert.equal(u.rec.filter((e) => e.op === "frame" && e.type === T.COMMIT).length, 0);
});

test("a cut during a file is an error (TransferError), never a retry", async () => {
  const a = payload("r/uc-pxx-mainline");
  const [cand] = findPayloads(a.files);
  const u = fakeUnit({ cutAt: 2 * DATA_CHUNK });
  const link = await Step2Link.open(u.dev, CFG, () => {});
  await assert.rejects(sendPayload(link, cand, false, null, () => {}), (e) => e.name === "TransferError" && e.kind === "interrupted");
  const sent = u.rec.filter((e) => e.op === "data").map((e) => e.n);
  assert.deepEqual(sent.slice(-2), [DATA_CHUNK, DATA_CHUNK]);
});
