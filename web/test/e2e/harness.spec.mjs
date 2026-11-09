// SPDX-License-Identifier: GPL-2.0-or-later
// The test harness catches what it must catch: a write to 49156, getvar all,
// a close during a transfer, a third-party request.
import { checkInvariants, expect, open, test } from "./helpers.mjs";
import { test as plain } from "@playwright/test";

const clean = { rec: [], violations: [], hostWrites: [], abOwnChange: false, abBefore: "00", abNow: "00" };
const cbw = (opcode, lba, count) => ({ op: "cbw", opcode, lba, count });

plain("checkInvariants fails on each broken rule", () => {
  expect(() => checkInvariants(clean)).not.toThrow();
  // the read-only GPT and factory reads are allowed
  expect(() => checkInvariants({ ...clean, rec: [cbw(0x14, 0, 34), cbw(0x14, 3666944, 8)] })).not.toThrow();
  const bad = [
    { ...clean, rec: [cbw(0x15, 49156, 1)] },
    { ...clean, rec: [cbw(0x15, 49152, 8)] },
    { ...clean, rec: [cbw(0x15, 49184, 8)] },
    { ...clean, rec: [cbw(0x14, 0, 64)] },
    { ...clean, rec: [cbw(0x0b, 0, 0)] },
    { ...clean, hostWrites: [{ lba: 49156, sectors: 1 }] },
    { ...clean, abNow: "01" },
    { ...clean, rec: [{ op: "cmd", cmd: "getvar:all" }] },
    { ...clean, rec: [{ op: "cmd", cmd: "flash:boot_a" }] },
    { ...clean, rec: [{ op: "close", inflight: 1 }] },
    { ...clean, rec: [{ op: "usb-reset" }] },
    { ...clean, violations: ["x"] },
  ];
  for (const f of bad) expect(() => checkInvariants(f), JSON.stringify(f)).toThrow();
});

test("the fake unit records getvar all and a write over 49156 sent around the page's modules", async ({ page }) => {
  await open(page, { start: "fastboot" });
  const v = await page.evaluate(async () => {
    const [d] = await navigator.usb.requestDevice({ filters: [{ vendorId: 0x18d1 }] }).then((x) => [x]);
    await d.open();
    await d.selectConfiguration(1);
    await d.claimInterface(0);
    await d.transferOut(2, new TextEncoder().encode("getvar:all"));
    const v = globalThis.__fake.violations.slice();
    globalThis.__fake.violations.length = 0; // so the fixture's own check passes
    globalThis.__fake.rec.length = 0;
    return v;
  });
  expect(v).toEqual(["fastboot: getvar all"]);
});

test("the page's CSP blocks a request to another site before it leaves the browser", async ({ page }) => {
  const seen = [];
  page.on("request", (r) => seen.push(r.url()));
  await open(page, { start: "rockusb" });
  const r = await page.evaluate(() => fetch("https://example.invalid/x").then(() => "sent", (e) => e.name));
  expect(r).toBe("TypeError");
  expect(seen.filter((u) => !u.startsWith("http://127.0.0.1:8137/"))).toEqual([]);
});
