// SPDX-License-Identifier: GPL-2.0-or-later
// LBA 49156 is never written: the invariants after every test check the fake
// unit's record; these tests also attack the rockusb module from inside the
// page and run the full entry over many unit states.
import { connectFastboot, cbws, expect, open, phase, rec, test, toChecked, writeAndRestart } from "./helpers.mjs";

test("inside the page: no call of the rockusb module can write LBA 49156 or anything but the BCB", async ({ page }) => {
  await open(page, { start: "rockusb" });
  const out = await page.evaluate(async () => {
    const m = await import("/js/rockusb.js");
    const bcb = await import("/js/bcb.js");
    const res = { built: [], frozen: Object.isFrozen(m.OP), errors: 0 };
    for (const op of [0x14, 0x15, 0xff, 0x05, 0x25, 0x16]) {
      for (let lba = 49140; lba <= 49200; lba++) {
        for (const n of [0, 1, 2, 4, 8, 64]) {
          try {
            m.buildCbw(op, lba, n, 0);
            res.built.push(`${op}:${lba}:${n}`);
          } catch {
            res.errors++;
          }
        }
      }
    }
    res.opAssign = Reflect.set(m.OP, "WRITE_LBA", 0x14);
    res.opAfter = m.OP.WRITE_LBA;
    const [dev] = await navigator.usb.getDevices().then((d) => (d.length ? d : navigator.usb.requestDevice({ filters: [{ vendorId: 0x2207 }] }).then((x) => [x])));
    const rk = await m.RockUsb.open(dev);
    res.methods = Object.getOwnPropertyNames(Object.getPrototypeOf(rk)).sort();
    res.privateVisible = typeof rk["#transact"] !== "undefined" || typeof rk.transact !== "undefined";
    // A changed payload, a payload for the A/B sector, a zero payload: all refused.
    const bad = [new Uint8Array(2048), new Uint8Array(512), new Uint8Array(2048).fill(0xff)];
    const ab = await rk.readAbSector();
    const abAs2048 = new Uint8Array(2048);
    abAs2048.set(ab);
    bad.push(abAs2048);
    const p = bcb.bcbPayload();
    p[2047] = 1;
    bad.push(p);
    res.refused = 0;
    for (const b of bad) {
      try {
        await rk.writeBcb(b);
      } catch {
        res.refused++;
      }
    }
    // The reviewed payload goes to 49184 and only there.
    await rk.writeBcb(bcb.bcbPayload());
    return res;
  });
  expect(out.built.sort()).toEqual(["20:49156:1", "20:49184:4", "21:49184:4"].sort());
  expect(out.frozen).toBe(true);
  expect(out.opAssign).toBe(false);
  expect(out.opAfter).toBe(0x15);
  expect(out.methods).toEqual(["constructor", "readAbSector", "readBcb", "readFactory", "readGpt", "resetNormal", "writeBcb"]);
  expect(out.privateVisible).toBe(false);
  expect(out.refused).toBe(5);
  const writes = cbws(await rec(page)).filter((c) => c.startsWith("15:"));
  expect(writes).toEqual(["15:49184:4"]);
  const hostWrites = await page.evaluate(() => globalThis.__fake.env.hostWrites);
  expect(hostWrites).toEqual([{ lba: 49184, sectors: 4 }]);
});

// The A/B states the page writes from: both slots successful, or the next slot
// a keeper and the other never tried (the factory-stock state). The page never
// writes LBA 49156; U-Boot's benign reset after the entry is the only change.
const STATES = [
  ["S0", [15, 0, 1], [14, 0, 1], "zero"],
  ["S0, payload already set", [15, 0, 1], [14, 0, 1], "payload"],
  ["D3 (B active)", [14, 0, 1], [15, 0, 1], "zero"],
  ["A 1, B 15", [1, 0, 1], [15, 0, 1], "zero"],
  ["A 15, B 1", [15, 0, 1], [1, 0, 1], "payload"],
  ["factory never-tried (A keeper, B 14/7/0)", [15, 0, 1], [14, 7, 0], "zero"],
];
for (const [name, a, b, bcb] of STATES) {
  test(`full entry with ${name}: 49156 is read, never written by the page; only U-Boot's benign reset changes it`, async ({ page }) => {
    await open(page, { start: "rockusb", a, b, bcb });
    await toChecked(page);
    await writeAndRestart(page);
    await phase(page, "restarted");
    await connectFastboot(page);
    await phase(page, "fastboot-checked");
    await page.click("#boot");
    await phase(page, "booted");
    // The page NEVER wrote LBA 49156; U-Boot's benign reset changed it to the
    // both-trying state (neither slot successful). abOwnChange marks it as
    // U-Boot's, not the page's.
    const succ = await page.evaluate(() => { const s = globalThis.__fake.env.emmc.read(49156, 1); return [s[10], s[14]]; });
    expect(succ).toEqual([0, 0]); // both slots' successful byte -> 0 (both-trying reset)
    expect(await page.evaluate(() => globalThis.__fake.env.abOwnChange)).toBe(true);
    const r = await rec(page);
    expect(r.filter((e) => e.op === "cbw" && e.opcode === 0x15).every((e) => e.lba === 49184 && e.count === 4)).toBe(true);
    expect(r.filter((e) => e.op === "cbw" && e.lba === 49156).every((e) => e.opcode === 0x14 && e.count === 1)).toBe(true);
    // The rest of misc around the two places was never touched.
    const misc = await page.evaluate(() => {
      const f = globalThis.__fake;
      const out = [];
      for (let l = 49152; l < 49192; l++) {
        if (l === 49156 || (l >= 49184 && l <= 49187)) continue;
        const s = f.env.emmc.read(l, 1);
        if (!s.every((x) => x === (l & 0xff))) out.push(l);
      }
      return out;
    });
    expect(misc).toEqual([]);
  });
}
