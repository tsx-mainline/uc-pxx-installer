// SPDX-License-Identifier: GPL-2.0-or-later
// Unit tests: the rockusb CBW layout and guard, and the --plain-entry steps
// against the fake unit (test/fake/fakeusb.js), in Node.
import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash, randomBytes, randomInt } from "node:crypto";
import {
  AB_LBA, BCB_LBA, BCB_SECTORS, buildCbw, deviceMode, FACTORY_LBA, FACTORY_SECTORS, GPT_LBA, GPT_SECTORS, OP, RockUsb,
} from "../../site/js/rockusb.js";
import { Fastboot } from "../../site/js/fastboot.js";
import { bcbPayload } from "../../site/js/bcb.js";
import {
  benignResetReasons, bootInstaller, ENTRY_GETVARS, fastbootState, readGate, readState, restart, sameUnit, writeBootCommand,
} from "../../site/js/enter.js";

await import(new URL("../fake/fakeusb.js", import.meta.url));
const { FakeEnv } = globalThis;
const hex = (b) => Buffer.from(b).toString("hex");
const sha = (b) => createHash("sha256").update(b).digest("hex");

test("CBW: byte layout of READ_LBA 49156 x1 (rkdeveloptool InitializeCBW + RKU_ReadLBA)", () => {
  assert.equal(hex(buildCbw(OP.READ_LBA, AB_LBA, 1, 0x11223344)),
    "55534243" + "44332211" + "00020000" + "80" + "00" + "0a" + "14" + "00" + "0000c004" + "00" + "0001" + "00000000000000");
});

test("CBW: byte layout of the GPT and factory reads, WRITE_LBA 49184 x4 and DEVICE_RESET", () => {
  assert.equal(hex(buildCbw(OP.READ_LBA, GPT_LBA, GPT_SECTORS, 3)),
    "55534243" + "03000000" + "00440000" + "80" + "00" + "0a" + "14" + "00" + "00000000" + "00" + "0022" + "00000000000000");
  assert.equal(hex(buildCbw(OP.READ_LBA, FACTORY_LBA, FACTORY_SECTORS, 3)),
    "55534243" + "03000000" + "00100000" + "80" + "00" + "0a" + "14" + "00" + "0037f400" + "00" + "0008" + "00000000000000");
  assert.equal(hex(buildCbw(OP.WRITE_LBA, BCB_LBA, 4, 1)),
    "55534243" + "01000000" + "00080000" + "00" + "00" + "0a" + "15" + "00" + "0000c020" + "00" + "0004" + "00000000000000");
  assert.equal(hex(buildCbw(OP.DEVICE_RESET, 0, 0, 2)),
    "55534243" + "02000000" + "00000000" + "00" + "00" + "06" + "ff" + "00" + "00000000" + "00" + "0000" + "00000000000000");
});

test("CBW guard: the only commands that build are the six reviewed ones (fuzz); no write but the BCB", () => {
  const ok = new Set([
    `${OP.READ_LBA}:${AB_LBA}:1`, `${OP.READ_LBA}:${BCB_LBA}:4`, `${OP.READ_LBA}:${GPT_LBA}:${GPT_SECTORS}`,
    `${OP.READ_LBA}:${FACTORY_LBA}:${FACTORY_SECTORS}`, `${OP.WRITE_LBA}:${BCB_LBA}:4`, `${OP.DEVICE_RESET}:0:0`,
  ]);
  const ops = [0x00, 0x01, 0x04, 0x05, 0x06, 0x0b, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1e, 0x1f, 0x23, 0x25, 0xaa, 0xff];
  const lbas = [0, 34, 64, 49152, 49155, AB_LBA, 49157, 49183, BCB_LBA, 49185, 49188, FACTORY_LBA, 2 ** 32 - 1];
  let built = 0;
  for (const op of ops) {
    for (const lba of lbas) {
      for (const n of [0, 1, 2, 3, 4, 5, 8, 32, 34, 128]) {
        let r = null;
        try { r = buildCbw(op, lba, n, 0); } catch { /* refused */ }
        assert.equal(r !== null, ok.has(`${op}:${lba}:${n}`), `op ${op} lba ${lba} n ${n}`);
        if (r) built++;
      }
    }
  }
  for (let i = 0; i < 20000; i++) {
    const op = randomInt(256), lba = randomInt(2 ** 32), n = randomInt(65536);
    let r = null;
    try { r = buildCbw(op, lba, n, 0); } catch { /* refused */ }
    assert.equal(r !== null, ok.has(`${op}:${lba}:${n}`));
  }
  assert.equal(built, 6);
  // A write over 49156 never builds, whatever the start and length.
  for (let lba = 49100; lba <= 49160; lba++) {
    for (const n of [1, 4, 8, 16, 64]) assert.throws(() => buildCbw(OP.WRITE_LBA, lba, n, 0));
  }
  // The GPT read overlaps LBA 0..33 but is a READ, never a write.
  assert.throws(() => buildCbw(OP.WRITE_LBA, GPT_LBA, GPT_SECTORS, 0));
});

test("deviceMode: Maskrom by bcdUSB bit 0 (RKScan.cpp), loader needs bcdDevice 0x9999", () => {
  const d = (bcdUSB, bcdDevice, vid = 0x2207, pid = 0x330c) => ({
    vendorId: vid, productId: pid, usbVersionMajor: bcdUSB >> 8, usbVersionMinor: (bcdUSB >> 4) & 15, usbVersionSubminor: bcdUSB & 15,
    deviceVersionMajor: bcdDevice >> 8, deviceVersionMinor: (bcdDevice >> 4) & 15, deviceVersionSubminor: bcdDevice & 15,
  });
  assert.equal(deviceMode(d(0x0201, 0x9999)).mode, "loader");
  assert.equal(deviceMode(d(0x0200, 0x9999)).mode, "maskrom");
  assert.equal(deviceMode(d(0x0200, 0x0100)).mode, "maskrom");
  assert.equal(deviceMode(d(0x0110, 0x9999)).mode, "maskrom");
  assert.equal(deviceMode(d(0x0201, 0x0100)).mode, "unknown");
  assert.equal(deviceMode(d(0x0201, 0x9999, 0x18d1, 0xd00d)).mode, "other");
});

async function rockusb(sc) {
  FakeEnv.install(Object.assign({ start: "rockusb" }, sc));
  const dev = (await navigator.usb.getDevices())[0] || (await navigator.usb.requestDevice({ filters: [{ vendorId: 0x2207 }] }));
  return { rk: await RockUsb.open(dev), fake: globalThis.__fake };
}

test("RockUsb.open refuses a Maskrom device before it opens it", async () => {
  FakeEnv.install({ start: "maskrom" });
  const dev = await navigator.usb.requestDevice({ filters: [{ vendorId: 0x2207 }] });
  await assert.rejects(RockUsb.open(dev), /maskrom/);
  assert.equal(globalThis.__fake.rec.filter((e) => e.op === "open").length, 0);
});

test("writeBcb refuses any payload but the reviewed one, before any transfer (fuzz)", async () => {
  const { rk, fake } = await rockusb({});
  const before = fake.rec.length;
  for (let i = 0; i < 300; i++) {
    const p = i % 3 === 0 ? new Uint8Array(randomBytes(2048)) : bcbPayload();
    if (i % 3 === 1) p[randomInt(2048)] ^= 1 + randomInt(255);
    if (i % 3 === 2) p[19 + randomInt(2029)] = 1 + randomInt(255);
    await assert.rejects(rk.writeBcb(p), /not the reviewed payload/);
  }
  await assert.rejects(rk.writeBcb(new Uint8Array(512)), /2048 bytes/);
  await assert.rejects(rk.writeBcb(new Uint8Array(4096)), /2048 bytes/);
  await assert.rejects(rk.writeBcb("bootonce-bootloader"), /2048 bytes/);
  assert.equal(fake.rec.length, before, "no transfer");
});

test("RockUsb has only the four reviewed reads/writes/reset (and the two read-only gate reads)", () => {
  const names = Object.getOwnPropertyNames(RockUsb.prototype).sort();
  assert.deepEqual(names, ["constructor", "readAbSector", "readBcb", "readFactory", "readGpt", "resetNormal", "writeBcb"]);
});

test("entry steps in Node: gate, read, guarded write, read back, reset, benign-reset validation, boot", async () => {
  const { rk, fake } = await rockusb({});
  const lines = [];
  const log = (m) => lines.push(m);
  await readGate(rk, log, "UC-P10");
  const before = await readState(rk, log);
  assert.equal(before.next, "a");
  assert.equal(before.otherKind, "successful");
  assert.equal(before.bcbCommand.name, "empty (no command set)");
  assert.equal(before.needsWrite, true);
  await writeBootCommand(rk, before, log);
  assert.equal(sha(fake.env.emmc.read(BCB_LBA, BCB_SECTORS)), sha(bcbPayload()));
  await restart(rk);
  await new Promise((r) => setTimeout(r, 120));
  const fbDev = await navigator.usb.requestDevice({ filters: [{ vendorId: 0x18d1 }] });
  const fb = await Fastboot.open(fbDev);
  const st = await fastbootState(fb, before, "evb_rk3399", log);
  assert.equal(st.slot, "a");
  const img = new Uint8Array(600 * 1024);
  img.set(new TextEncoder().encode("ANDROID!"));
  await bootInstaller(fb, { bytes: img, sha256: sha(img) }, st.maxDownload);
  assert.equal(sha(fake.env.downloaded), sha(img));
  // The page never wrote the A/B sector: the post-entry A/B is U-Boot's benign
  // reset (A{15,6,0} B{14,7,0}), not what the page wrote, and no violation fired.
  assert.deepEqual(fake.violations, []);
  const abAfter = fake.env.emmc.read(AB_LBA, 1);
  assert.equal(abAfter[9], 6); // slot A tries
  assert.equal(abAfter[13], 7); // slot B tries
  assert.equal(abAfter[10], 0); // slot A successful = 0
  assert.equal(abAfter[14], 0); // slot B successful = 0
  const cbws = fake.rec.filter((e) => e.op === "cbw").map((e) => `${e.opcode.toString(16)}:${e.lba}:${e.count}`);
  assert.deepEqual(cbws, [
    "14:0:34", "14:3666944:8", "14:49156:1", "14:49184:4", // gate (GPT, factory), A/B, BCB
    "14:49156:1", "14:49184:4", // writeBootCommand re-read
    "15:49184:4", "14:49184:4", "14:49156:1", // write, read back, A/B re-read
    "ff:0:0", // reset
  ]);
  const writes = fake.rec.filter((e) => e.op === "cbw" && e.opcode === 0x15);
  assert.equal(writes.length, 1);
  assert.equal(writes[0].lba, BCB_LBA);
});

test("Fastboot.bootImage itself refuses bytes whose sha is not the pin, and getvar refuses names off the list, with no transfer", async () => {
  FakeEnv.install({ start: "fastboot" });
  const dev = await navigator.usb.requestDevice({ filters: [{ vendorId: 0x18d1 }] });
  const fb = await Fastboot.open(dev);
  const fake = globalThis.__fake;
  const n = fake.rec.length;
  const img = new Uint8Array(4096);
  img.set(new TextEncoder().encode("ANDROID!"));
  const pin = sha(img);
  img[4000] = 1;
  await assert.rejects(fb.bootImage(img, pin, 0x08000000), /image sha256 .* is not/);
  await assert.rejects(fb.bootImage(new Uint8Array(0), pin, 0x08000000), /no image/);
  img[4000] = 0;
  await assert.rejects(fb.bootImage(img, pin, 100), /larger than max-download-size/);
  for (const v of ["all", "serialno", "unlocked", "current-slot ", "", "at-vboot-state", "max-download-size\0all"]) {
    await assert.rejects(fb.getvar(v), /refused/);
  }
  assert.equal(fake.rec.length, n, "no transfer");
});

async function toFastboot(sc) {
  const { rk, fake } = await rockusb(sc);
  await readGate(rk, () => {}, "UC-P10");
  const before = await readState(rk, () => {});
  await writeBootCommand(rk, before, () => {});
  await restart(rk);
  await new Promise((r) => setTimeout(r, 120));
  const fb = await Fastboot.open(await navigator.usb.requestDevice({ filters: [{ vendorId: 0x18d1 }] }));
  return { before, fb, fake };
}

// benignResetReasons is the port of check_plain_entry's A/B rule.
test("benignResetReasons: the both-trying reset passes; zero-bootable, any-successful, no-try fail", () => {
  const ok = { "current-slot": "a", "slot-successful:a": "no", "slot-successful:b": "no", "slot-unbootable:a": "no",
    "slot-unbootable:b": "no", "slot-retry-count:a": "6", "slot-retry-count:b": "7" };
  assert.deepEqual(benignResetReasons(ok).why, []);
  assert.equal(benignResetReasons(ok).cur, "a");
  // U-Boot prints the current slot as _a
  assert.deepEqual(benignResetReasons({ ...ok, "current-slot": "_a" }).why, []);
  // a slot still successful -> a stale keeper, refuse
  assert.match(benignResetReasons({ ...ok, "slot-successful:a": "yes" }).why.join(";"), /still successful/);
  // the current slot is unbootable -> zero bootable slots (via that slot)
  assert.match(benignResetReasons({ ...ok, "slot-unbootable:a": "yes" }).why.join(";"), /no bootable slot/);
  // current-slot is not a real slot
  assert.match(benignResetReasons({ ...ok, "current-slot": "x" }).why.join(";"), /not a real slot/);
  // the current slot did not take a try (6 is the reset shape; 7 is not)
  assert.match(benignResetReasons({ ...ok, "current-slot": "b", "slot-retry-count:b": "7", "slot-retry-count:a": "0" }).why.join(";"),
    /did not take a try/);
  // a retry count out of range
  assert.match(benignResetReasons({ ...ok, "slot-retry-count:a": "9" }).why.join(";"), /not in 0\.\.7/);
});

test("fastbootState: the default benign reset passes; a stale-keeper, zero-bootable or no-try post-entry state is refused", async () => {
  // default plain reset -> OK
  let { before, fb } = await toFastboot({});
  assert.equal((await fastbootState(fb, before, "evb_rk3399", () => {})).slot, "a");
  // no pre-entry read
  ({ before, fb } = await toFastboot({}));
  await assert.rejects(fastbootState(fb, null, "evb_rk3399", () => {}), (e) => e.code === "no-pre-entry");
  // a slot stayed successful after the entry (U-Boot did NOT scribble/reset)
  ({ before, fb } = await toFastboot({ entryAb: [[15, 0, 1], [14, 7, 0]] }));
  await assert.rejects(fastbootState(fb, before, "evb_rk3399", () => {}), (e) => e.code === "entry-not-benign" && /still successful/.test(e.message));
  // zero bootable slots after entry: the unit cannot name a bootable current
  // slot, so the getvar is unreadable (kept in fastboot, asked for help)
  ({ before, fb } = await toFastboot({ entryAb: [[0, 0, 0], [0, 0, 0]] }));
  await assert.rejects(fastbootState(fb, before, "evb_rk3399", () => {}),
    (e) => e.code === "entry-not-benign" || e.code === "getvar-unreadable");
  // the current slot took no try (A unbootable prio>0, B trying 7)
  ({ before, fb } = await toFastboot({ entryAb: [[15, 0, 0], [14, 7, 0]] }));
  await assert.rejects(fastbootState(fb, before, "evb_rk3399", () => {}), (e) => e.code === "entry-not-benign" && /did not take a try/.test(e.message));
});

test("fastbootState: product is informational (a mismatch is logged, not a stop), and an unreadable slot getvar stops", async () => {
  // a different product: still boots (the benign reset is the gate), logged
  let { before, fb } = await toFastboot({ product: "rk3399_box" });
  const logs = [];
  assert.equal((await fastbootState(fb, before, "evb_rk3399", (m) => logs.push(m))).slot, "a");
  assert.match(logs.join("\n"), /product "rk3399_box" is not the expected "evb_rk3399" \(logged, not a stop\)/);
  // an unreadable required slot getvar stops
  for (const k of ["current-slot", "slot-successful:a", "slot-retry-count:b"]) {
    ({ before, fb } = await toFastboot({ getvarFail: [k] }));
    await assert.rejects(fastbootState(fb, before, "evb_rk3399", () => {}), (e) => e.code === "getvar-unreadable" && e.message.includes(k), k);
  }
  // max-download-size unreadable stops (needed to send the image)
  ({ before, fb } = await toFastboot({ getvarFail: ["max-download-size"] }));
  await assert.rejects(fastbootState(fb, before, "evb_rk3399", () => {}), (e) => e.code === "max-download");
  // product is in the read list but not required
  assert.ok(ENTRY_GETVARS.includes("product"));
  assert.ok(!ENTRY_GETVARS.includes("serialno"));
});

test("readState accepts the never-tried factory state and refuses non-bootable-pair states, binds to an expected A/B sha", async () => {
  // factory-stock never-tried state (unit 3): accepted, either slot the keeper
  let { rk } = await rockusb({ a: [15, 0, 1], b: [14, 7, 0] });
  await readGate(rk, () => {}, "UC-P10");
  assert.equal((await readState(rk, () => {})).otherKind, "never successful");
  // B runs (B keeper) while A was never successful: REFUSED before any write.
  // The entry's A/B reset makes A the installer's keeper whatever ran before,
  // so A must be a proven (successful) slot.
  for (const [a, b] of [[[2, 7, 0], [15, 0, 1]], [[14, 7, 0], [15, 0, 1]]]) {
    const r = await rockusb({ a, b });
    await readGate(r.rk, () => {}, "UC-P10");
    await assert.rejects(readState(r.rk, () => {}), (e) => e.code === "ab-runs-slot-b");
    assert.equal(r.fake.rec.filter((e) => e.op === "cbw" && e.opcode === 0x15).length, 0, "no write");
  }
  // B runs and A is successful too (D3, the state after an update into B): accepted.
  for (const [a, b] of [[[14, 0, 1], [15, 0, 1]], [[1, 0, 1], [15, 0, 1]]]) {
    const r = await rockusb({ a, b });
    await readGate(r.rk, () => {}, "UC-P10");
    const st = await readState(r.rk, () => {});
    assert.equal(st.next, "b");
    assert.equal(st.otherKind, "successful");
  }
  // refused: next slot a keeper but other slot neither keeper nor never-tried
  for (const [a, b] of [[[15, 0, 1], [0, 0, 0]], [[15, 0, 1], [14, 3, 0]], [[15, 0, 1], [9, 0, 0]]]) {
    const r = await rockusb({ a, b });
    await readGate(r.rk, () => {}, "UC-P10");
    await assert.rejects(readState(r.rk, () => {}), (e) => e.code === "ab-not-bootable-pair" || e.code === "ab-refused");
    assert.equal(r.fake.rec.filter((e) => e.op === "cbw" && e.opcode === 0x15).length, 0, "no write");
  }
  const S0sha = "1d2e3769805d32c2065ac3ff7e204ebf60921c0d2d3f66f0d47612d0889d7d2d";
  ({ rk } = await rockusb({}));
  await readGate(rk, () => {}, "UC-P10");
  assert.equal((await readState(rk, () => {}, [S0sha])).ab32, S0sha);
  ({ rk } = await rockusb({}));
  await readGate(rk, () => {}, "UC-P10");
  await assert.rejects(readState(rk, () => {}, [S0sha, "0".repeat(64)]), (e) => e.code === "ab-not-expected");
});

test("readState's BCB gate is the command field, not a whole-block sha", async () => {
  // empty command field -> write (even when the rest of the block is a leftover)
  for (const bcb of [undefined, "leftover"]) {
    const { rk } = await rockusb(bcb ? { bcb } : {});
    await readGate(rk, () => {}, "UC-P10");
    const before = await readState(rk, () => {});
    assert.equal(before.needsWrite, true, `empty command field (${bcb || "all zero"})`);
    assert.equal(before.bcbCommand.reason, "empty");
  }
  // an unrecognised leftover command U-Boot ignores -> write, no stop, no write yet
  for (const bcb of ["random", "bq-leftover"]) {
    const { rk, fake } = await rockusb({ bcb });
    await readGate(rk, () => {}, "UC-P10");
    const before = await readState(rk, () => {});
    assert.equal(before.needsWrite, true, bcb);
    assert.equal(before.bcbCommand.reason, "leftover", bcb);
    assert.equal(fake.rec.filter((e) => e.op === "cbw" && e.opcode === 0x15).length, 0, "no write in readState");
  }
  // bootonce-bootloader already set -> skip
  {
    const { rk } = await rockusb({ bcb: "payload" });
    await readGate(rk, () => {}, "UC-P10");
    const before = await readState(rk, () => {});
    assert.equal(before.needsWrite, false);
    assert.equal(before.bcbCommand.reason, "already-set");
  }
  // a recovery/OTA command U-Boot acts on -> STOP before any write
  for (const bcb of ["recovery", "boot_recovery"]) {
    const { rk, fake } = await rockusb({ bcb });
    await readGate(rk, () => {}, "UC-P10");
    await assert.rejects(readState(rk, () => {}), (e) => e.code === "bcb-command-pending", bcb);
    assert.equal(fake.rec.filter((e) => e.op === "cbw" && e.opcode === 0x15).length, 0, "no write");
  }
});

test("writeBootCommand overwrites an unrecognised leftover command field and logs it (raw bytes never logged)", async () => {
  const { rk, fake } = await rockusb({ bcb: "bq-leftover" });
  await readGate(rk, () => {}, "UC-P10");
  const lines = [];
  const before = await readState(rk, (m) => lines.push(m));
  await writeBootCommand(rk, before, (m) => lines.push(m));
  assert.match(lines.join("\n"), /overwriting an unrecognised leftover BCB command field: "b\?"/);
  assert.doesNotMatch(lines.join("\n"), /[0-9a-f]{4096}/); // the raw 2048-byte BCB is never logged
  const w = fake.rec.filter((e) => e.op === "cbw" && e.opcode === 0x15);
  assert.equal(w.length, 1);
  assert.equal(w[0].lba, BCB_LBA);
  assert.equal(sha(fake.env.emmc.read(BCB_LBA, BCB_SECTORS)), sha(bcbPayload())); // write surface unchanged: the reviewed payload
});

test("readGate refuses a bad layout and a wrong model, before any write", async () => {
  // bad GPT (no signature) -> layout stop
  let { rk, fake } = await rockusb({ gpt: "nosig" });
  await assert.rejects(readGate(rk, () => {}, "UC-P10"), (e) => e.code === "layout");
  assert.equal(fake.rec.filter((e) => e.op === "cbw" && e.opcode === 0x15).length, 0);
  // misc at the wrong place -> layout stop
  ({ rk } = await rockusb({ gpt: "badmisc" }));
  await assert.rejects(readGate(rk, () => {}, "UC-P10"), (e) => e.code === "layout" && /misc/.test(e.message));
  // the unit is a UC-P8 but this page is UC-P10 -> model stop
  ({ rk } = await rockusb({ factory: "ucp8" }));
  await assert.rejects(readGate(rk, () => {}, "UC-P10"), (e) => e.code === "model");
  // a usable UC-P10 factory block -> passes
  ({ rk } = await rockusb({ factory: "ucp10" }));
  await readGate(rk, () => {}, "UC-P10");
  // a 0xCC-only factory block (the real download mode): not readable, not a stop
  const logs = [];
  ({ rk } = await rockusb({})); // default factory = unserved
  await readGate(rk, (m) => logs.push(m), "UC-P10");
  assert.match(logs.join("\n"), /not readable in the download mode/);
  // a corrupt (bad-checksum) factory block that is NOT all 0xCC -> model stop
  ({ rk } = await rockusb({ factory: "bad" }));
  await assert.rejects(readGate(rk, () => {}, "UC-P10"), (e) => e.code === "model");
});

test("the factory read's sha is never logged (it holds the MAC)", async () => {
  const { rk } = await rockusb({ factory: "ucp10" });
  const logs = [];
  await readGate(rk, (m) => logs.push(m), "UC-P10");
  // the model is shown, the factory block's sha256 is not in any log line
  assert.match(logs.join("\n"), /model UC-P10/);
  assert.doesNotMatch(logs.join("\n"), /factory block.*[0-9a-f]{64}/);
});

test("sameUnit: compares USB serial strings only when both exist, and never puts them in its text", () => {
  assert.match(sameUnit("X1", { serialNumber: "X1" }), /the same/);
  assert.match(sameUnit("", { serialNumber: "X1" }), /not compared/);
  assert.match(sameUnit("X1", { serialNumber: "" }), /not compared/);
  assert.match(sameUnit("X1", {}), /not compared/);
  assert.throws(() => sameUnit("X1", { serialNumber: "X2" }), (e) => e.code === "other-device" && !e.message.includes("X1") && !e.message.includes("X2"));
});
