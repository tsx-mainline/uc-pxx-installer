// SPDX-License-Identifier: GPL-2.0-or-later
// step1usb.js (W13, I-6): the step-1 send against a fake unit built from the
// protocol (the PXX1 frames of step 2, HELLO mode "step1", the states of P):
// the phase map, the HELLO check, each send only in the state that asks for
// it and only when the unit pins the file, the order and the COMMIT bodies,
// refusals before any byte, cuts, and the DRIVE frame.
import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash, randomBytes } from "node:crypto";
import { classify, checkHello, P, PHASE_WORDS, phaseOf, requestDrive, sendBase, sendFirmware, Step2Link, T1 } from "../../site/js/step1usb.js";
import { DATA_CHUNK, decodeFrame, encodeFrame, T } from "../../site/js/step2usb.js";

const sha = (b) => createHash("sha256").update(b).digest("hex");
const CFG = { cls: 0xff, sub: 0x50, proto: 1 };

// fakeUnit: the step-1 receiver (uc-pxx-installer 85f2b22 internal/xfer kind
// step1): HELLO fw_pins in the firmware stage, pins (manifest sha256s) in the
// bundle stage
function fakeUnit(o) {
  const rec = { frames: [], data: [], puts: [], commits: [] };
  let state = o.state || P.WAITING_FIRMWARE;
  let pending = null;
  const q = [], waiters = [];
  const reply = (type, seq, obj) => {
    const f = encodeFrame(type | T.REPLY, seq, obj);
    const w = waiters.shift();
    if (w) w(f); else q.push(f);
  };
  const hello = () => ({ proto: 1, usb: "1d6b:0105", mode: o.mode || "step1", state, stage: state === P.WAITING_FIRMWARE ? "firmware" : "bundle",
    fw_pins: o.fwPins || [], pins: o.pins || [], info: { token: o.token, model: "UC-P10" } });
  const dev = {
    opened: false, configuration: null,
    configurations: [{ interfaces: [{ interfaceNumber: 0, alternates: [{ alternateSetting: 0, interfaceClass: 0xff, interfaceSubclass: 0x50,
      interfaceProtocol: 1, endpoints: [{ type: "bulk", direction: "in", endpointNumber: 1 }, { type: "bulk", direction: "out", endpointNumber: 2 }] }] }] }],
    async open() { this.opened = true; },
    async selectConfiguration() { this.configuration = this.configurations[0]; },
    async claimInterface() {},
    async selectAlternateInterface() { rec.frames.push({ setAlt: true }); },
    async transferOut(ep, data) {
      const b = Buffer.from(data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength));
      if (pending) {
        rec.data.push(b.length);
        assert.ok(b.length <= DATA_CHUNK);
        pending.got.push(b);
        pending.have += b.length;
        if (o.cutAfter && pending.have >= o.cutAfter) throw new DOMException("cut", "NetworkError");
        if (pending.have >= pending.size) {
          const all = Buffer.concat(pending.got), p = pending;
          pending = null;
          const ok = p.name === P.KEY_NAME || (o.fwPins || []).some((x) => x.sha256 === sha(all)) || (o.pins || []).includes(sha(all)) ||
            (o.files || []).includes(sha(all));
          if (ok) rec.puts.push({ name: p.name, sha256: sha(all), text: p.name === P.KEY_NAME ? all.toString() : undefined });
          reply(T.PUT_END, p.seq, { ok, name: p.name, sha256: sha(all), reason: ok ? undefined : "not pinned" });
        }
        return { status: "ok", bytesWritten: b.length };
      }
      const f = decodeFrame(b);
      rec.frames.push({ type: f.type, body: f.body });
      if (f.type === T.HELLO) reply(T.HELLO, f.seq, hello());
      else if (f.type === T.STATUS) reply(T.STATUS, f.seq, { state, mode: "step1", info: { phase: o.phase || "" } });
      else if (f.type === T.PUT_BEGIN) {
        const why = o.refuse && o.refuse(f.body) ? "refused by the unit" : "";
        if (why) reply(T.PUT_BEGIN, f.seq, { accept: false, name: f.body.name, reason: why });
        else {
          reply(T.PUT_BEGIN, f.seq, { accept: true, name: f.body.name });
          pending = { name: f.body.name, size: f.body.size, have: 0, got: [], seq: f.seq };
        }
      } else if (f.type === T.COMMIT) {
        rec.commits.push(f.body);
        reply(T.COMMIT, f.seq, { ok: !o.refuseCommit, reason: o.refuseCommit ? "incomplete" : undefined });
      } else if (f.type === T1.DRIVE) reply(T1.DRIVE, f.seq, { ok: true });
      return { status: "ok", bytesWritten: b.length };
    },
    async transferIn() {
      const f = q.length ? q.shift() : await new Promise((r) => waiters.push(r));
      return { status: "ok", data: new DataView(f.buffer) };
    },
  };
  return { dev, rec, setState: (s) => { state = s; } };
}

const FW = randomBytes(3 * DATA_CHUNK + 4321);
const FWPIN = [{ size: FW.length, sha256: sha(FW) }];
const TMPL = randomBytes(50000);
const ZST = randomBytes(2 * DATA_CHUNK + 9);
const MAN = Buffer.from(JSON.stringify({ schema: 3, boot_template: { file: "boot-template.img", size: TMPL.length, sha256: sha(TMPL) },
  rootfs_zst: { file: "rootfs.ext4.zst", size: ZST.length, sha256: sha(ZST) } }));
const BASE = { sha: sha(MAN), manifest: JSON.parse(MAN), files: { manifest: new Blob([MAN]), template: new Blob([TMPL]), rootfsZst: new Blob([ZST]) } };
const KEY = new Blob(["ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBfakeKeyForTheTestOnly1234567890abcdefgh test\n"]);

// the installer's closed set: flow.PhaseWords of uc-pxx-installer
// mainline-rc-10 (internal/flow/usb.go at 4af28d1 / 85015e6; record §6.6), copied here,
// with "unlock" of mainline-rc-10c (R26: the install's lock-state write)
const INSTALLER_PHASE_WORDS = [
  "stash", "check", "install", "unlock", "switch-running", "repair-running",
  "waiting-firmware", "waiting-bundle", "drive: waiting-firmware", "drive: waiting-bundle", "drive: reading",
  "status", "canceled", "question", "planning", "plan", "progress", "done", "stopped", "hold", "refused",
  "log", "restart", "repair", "safe", "switch", "switching", "switch-confirm",
];

test("the phase words are the installer's closed set, each mapped once; a word outside it is unknown in every state", () => {
  assert.deepEqual([...PHASE_WORDS].sort(), [...INSTALLER_PHASE_WORDS].sort());
  assert.equal(new Set(PHASE_WORDS).size, PHASE_WORDS.length);
  for (const w of INSTALLER_PHASE_WORDS) {
    // the files screen's words: the state decides (a status-only link takes nothing)
    for (const state of (P.FILES.includes(w) ? ["drive", "waiting-firmware", "waiting-bundle"] : ["busy", "drive", "waiting-firmware", "waiting-bundle"])) {
      assert.notEqual(classify({ state, info: { phase: w } }).phase, "unknown", `${state} ${w}`);
    }
  }
  // the record's mapping (§6.6)
  const c = (phase, state = "busy") => classify({ state, info: { phase } }).phase;
  for (const w of ["install", "progress"]) assert.equal(c(w), "installing");
  assert.equal(c("unlock"), "unlocking"); // R26
  assert.equal(c("unlock", "drive"), "unlocking"); // also after a drive-mode step 1
  for (const w of ["switch-running", "repair-running", "switching"]) assert.equal(c(w), "writing");
  for (const w of ["switch", "switch-confirm", "repair", "safe", "status", "log", "restart"]) {
    assert.equal(c(w), "other");
    assert.equal(c(w, "waiting-firmware"), "other"); // another screen: the page sends nothing
  }
  for (const w of ["check", "planning"]) assert.equal(c(w), "checking");
  for (const w of ["stopped", "hold"]) assert.equal(c(w), "stop");
  assert.equal(c("waiting-firmware", "waiting-firmware"), "send-firmware");
  assert.equal(c("waiting-bundle", "waiting-bundle"), "send-base");
  assert.equal(c("waiting-firmware", "busy"), "unknown"); // the files screen, a status-only link: nothing to send
  for (const w of ["drive: checking", "drive:", "installing", "xyz", "Done", "done ", "Unlock", "unlock ", "unlocking", "unlocked"]) {
    for (const state of ["busy", "drive", "waiting-firmware", "waiting-bundle", "receiving"]) assert.equal(c(w, state), "unknown", `${state} ${w}`);
  }
});

test("classify: the receiver's states and the flow's phases; anything else is unknown", () => {
  const c = (state, phase) => classify({ state, info: { phase } }).phase;
  assert.equal(c("waiting-firmware", "waiting-firmware"), "send-firmware");
  assert.equal(c("receiving"), "receiving");
  assert.equal(c("firmware-complete"), "sealing");
  assert.equal(c("bundle-complete"), "sealing");
  assert.equal(c("busy", "stash"), "reading");
  assert.equal(c("waiting-bundle", "waiting-bundle"), "send-base");
  assert.equal(c("busy", "check"), "checking");
  assert.equal(c("busy", "planning"), "checking");
  assert.equal(c("busy", "plan"), "plan");
  assert.equal(c("busy", "install"), "installing");
  assert.equal(c("busy", "progress"), "installing");
  assert.equal(c("busy", "done"), "done");
  assert.equal(c("busy", "canceled"), "canceled");
  assert.equal(c("busy", "refused"), "refused");
  assert.equal(c("busy", "stopped"), "stop");
  assert.equal(c("busy", "hold"), "stop");
  assert.deepEqual(classify({ state: "drive", info: { phase: "drive: waiting-bundle" } }), { phase: "drive", drive: "waiting-bundle" });
  // review MAJOR-2: in drive mode the state stays "drive" while the phase goes on
  assert.equal(c("drive", "check"), "checking");
  assert.equal(c("drive", "plan"), "plan");
  assert.equal(c("drive", "install"), "installing");
  assert.equal(c("drive", "done"), "done");
  assert.equal(c("drive", "hold"), "stop");
  assert.equal(c("drive", ""), "drive");
  assert.equal(c("drive", "status"), "other");
  assert.equal(c("drive", "stash"), "reading");
  assert.equal(c("drive", "waiting-bundle"), "drive");
  assert.deepEqual(classify({ state: "drive", info: { phase: "drive: reading" } }), { phase: "drive", drive: "reading" });
  assert.equal(c("busy", "restart"), "other"); // a restart from the status menu is not an install's Done
  assert.equal(c("busy", "question"), "question");
  assert.equal(c("busy", "status"), "other");
  assert.equal(c(undefined), "unknown");
  assert.equal(phaseOf({}), "");
});

test("checkHello: only a step-1 unit with the token of the image the page started", () => {
  assert.equal(checkHello({ mode: "step1", info: { token: "pxx-inst-a" } }, { token: "pxx-inst-a" }).go, "ok");
  assert.equal(checkHello({ mode: "payload", info: { token: "pxx-inst-a" } }, { token: "pxx-inst-a" }).go, "stop");
  assert.equal(checkHello({ mode: "step1", info: { token: "pxx-inst-b" } }, { token: "pxx-inst-a" }).go, "stop");
  // review MAJOR-1: no token yet (it comes with the status report): wait, not stop
  assert.equal(checkHello({ mode: "step1" }, { token: "pxx-inst-a" }).go, "wait");
  assert.equal(checkHello({ mode: "step1", info: { token: "" } }, { token: "pxx-inst-a" }).go, "wait");
  assert.equal(checkHello(null, { token: "x" }).go, "stop");
});

async function link(u) {
  const l = await Step2Link.open(u.dev, CFG, () => {});
  return { l, h: await l.hello() };
}

test("userdebug: firmware.bin in 1 MiB transfers, no key, COMMIT {mode: official}", async () => {
  const u = fakeUnit({ fwPins: FWPIN, token: "t" });
  const { l, h } = await link(u);
  const seen = [];
  const r = await sendFirmware(l, h, new Blob([FW]), sha(FW), null, "official", (n, s) => seen.push(s), () => {});
  assert.deepEqual(r, { ok: true });
  assert.deepEqual(u.rec.puts.map((p) => p.name), ["firmware.bin"]);
  assert.deepEqual(u.rec.commits, [{ mode: "official" }]);
  assert.deepEqual(u.rec.data, [DATA_CHUNK, DATA_CHUNK, DATA_CHUNK, 4321]);
  assert.equal(seen.at(-1), FW.length);
});

test("mainline: firmware.bin, SSH-KEY.PUB, COMMIT {mode: mainline}; then the base (manifest first) and COMMIT {}", async () => {
  const u = fakeUnit({ fwPins: FWPIN, token: "t", files: [sha(TMPL), sha(ZST)], pins: [BASE.sha] });
  const { l, h } = await link(u);
  assert.deepEqual(await sendFirmware(l, h, new Blob([FW]), sha(FW), KEY, "mainline", null, null), { ok: true });
  u.setState(P.WAITING_BUNDLE);
  const h2 = await l.hello();
  const seen = [];
  assert.deepEqual(await sendBase(l, h2, BASE, (n, s, size, done, total) => seen.push([done, total]), null), { ok: true });
  assert.deepEqual(u.rec.puts.map((p) => p.name), ["firmware.bin", "SSH-KEY.PUB", "manifest.json", "boot-template.img", "rootfs.ext4.zst"]);
  assert.equal(u.rec.puts[1].text, await KEY.text());
  assert.deepEqual(u.rec.commits, [{ mode: "mainline" }, {}]);
  assert.deepEqual(seen.at(-1), [MAN.length + TMPL.length + ZST.length, MAN.length + TMPL.length + ZST.length]);
});

test("nothing is sent in the wrong state, without the unit's pin, with a size not the manifest's, or a key for userdebug", async () => {
  const u = fakeUnit({ fwPins: FWPIN, token: "t", state: P.BUSY, pins: [BASE.sha] });
  const { l, h } = await link(u);
  const before = u.rec.frames.length;
  assert.equal((await sendFirmware(l, h, new Blob([FW]), sha(FW), null, "official")).ok, false);
  u.setState(P.WAITING_FIRMWARE);
  const h2 = await l.hello();
  assert.match((await sendFirmware(l, h2, new Blob([FW]), "0".repeat(64), null, "official")).why, /does not take this firmware/);
  assert.match((await sendFirmware(l, h2, new Blob([FW.subarray(1)]), sha(FW), null, "official")).why, /does not take this firmware/);
  await assert.rejects(sendFirmware(l, h2, new Blob([FW]), sha(FW), KEY, "official"), /a key only with the mainline goal/);
  await assert.rejects(sendFirmware(l, h2, new Blob([FW]), sha(FW), null, "other"), /mode/);
  assert.equal((await sendBase(l, h2, BASE)).ok, false); // not the bundle state
  u.setState(P.WAITING_BUNDLE);
  const h3 = { ...(await l.hello()), pins: ["0".repeat(64)] };
  assert.match((await sendBase(l, h3, BASE)).why, /does not take this base/);
  const h4 = await l.hello();
  const short = { ...BASE, files: { ...BASE.files, rootfsZst: new Blob([ZST.subarray(1)]) } };
  assert.match((await sendBase(l, h4, short)).why, /the manifest says/);
  assert.deepEqual(u.rec.frames.slice(before).filter((f) => f.type !== T.HELLO), []);
  assert.deepEqual(u.rec.data, []);
});

test("a refusal at PUT_BEGIN sends no byte; a refused COMMIT is a failure", async () => {
  const u = fakeUnit({ fwPins: FWPIN, token: "t", refuse: (b) => b.name === "firmware.bin" });
  const { l, h } = await link(u);
  const r = await sendFirmware(l, h, new Blob([FW]), sha(FW), null, "official");
  assert.equal(r.ok, false);
  assert.match(r.why, /refused the firmware file/);
  assert.deepEqual(u.rec.data, []);
  assert.deepEqual(u.rec.commits, []);
  const u2 = fakeUnit({ fwPins: FWPIN, token: "t", refuseCommit: true });
  const y = await link(u2);
  assert.match((await sendFirmware(y.l, y.h, new Blob([FW]), sha(FW), null, "official")).why, /did not take the files/);
});

test("a cut in the firmware file is an error (the page sends again from HELLO); no COMMIT", async () => {
  const u = fakeUnit({ fwPins: FWPIN, token: "t", cutAfter: DATA_CHUNK + 1 });
  const { l, h } = await link(u);
  await assert.rejects(sendFirmware(l, h, new Blob([FW]), sha(FW), null, "official"), (e) => e.name === "TransferError");
  assert.deepEqual(u.rec.commits, []);
});

test("DRIVE: the frame of the drive fallback", async () => {
  const u = fakeUnit({ token: "t" });
  const { l } = await link(u);
  assert.equal(await requestDrive(l), true);
  assert.equal(u.rec.frames.at(-1).type, 7);
});
