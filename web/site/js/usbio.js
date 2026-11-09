// SPDX-License-Identifier: GPL-2.0-or-later
// usbio.js: the only place that calls transferIn and transferOut.
//
// Rules (RULES.md, "never cut a transfer short"):
//   - A transfer is never aborted. There is no timeout that cancels one, and
//     close() refuses while any transfer is in flight.
//   - A short transfer, a stall or a rejected transfer is an error. The caller
//     stops; it never retries by itself.

export class TransferError extends Error {
  // kind: "interrupted" (the transfer was rejected, usually a disconnect),
  //       "stall", "short"
  constructor(kind, message) {
    super(message);
    this.name = "TransferError";
    this.kind = kind;
  }
}

let inflight = 0;

export function transfersInFlight() {
  return inflight;
}

export async function bulkOut(dev, ep, data) {
  if (!(data instanceof Uint8Array)) throw new TypeError("bulkOut needs a Uint8Array");
  inflight++;
  let r;
  try {
    r = await dev.transferOut(ep, data);
  } catch (e) {
    throw new TransferError("interrupted", `USB OUT of ${data.length} bytes failed: ${e && e.name}: ${e && e.message}`);
  } finally {
    inflight--;
  }
  if (r.status !== "ok") throw new TransferError("stall", `USB OUT status ${r.status}`);
  if (r.bytesWritten !== data.length) {
    throw new TransferError("short", `USB OUT wrote ${r.bytesWritten} of ${data.length} bytes`);
  }
}

// bulkIn reads one transfer. With exact = true, anything but `length` bytes is
// an error (as RKU_Read in rkdeveloptool).
export async function bulkIn(dev, ep, length, exact) {
  inflight++;
  let r;
  try {
    r = await dev.transferIn(ep, length);
  } catch (e) {
    throw new TransferError("interrupted", `USB IN of ${length} bytes failed: ${e && e.name}: ${e && e.message}`);
  } finally {
    inflight--;
  }
  if (r.status !== "ok") throw new TransferError("stall", `USB IN status ${r.status}`);
  const d = r.data;
  const out = new Uint8Array(d.buffer.slice(d.byteOffset, d.byteOffset + d.byteLength));
  if (exact && out.length !== length) {
    throw new TransferError("short", `USB IN read ${out.length} of ${length} bytes`);
  }
  return out;
}

// findInterface returns the first interface with the given class triple and
// one bulk IN and one bulk OUT endpoint.
export function findInterface(dev, cls, sub, proto) {
  const cfg = dev.configuration || (dev.configurations && dev.configurations[0]);
  if (!cfg) return null;
  for (const iface of cfg.interfaces) {
    for (const alt of iface.alternates) {
      if (alt.interfaceClass !== cls || alt.interfaceSubclass !== sub || alt.interfaceProtocol !== proto) continue;
      let epIn = 0, epOut = 0;
      for (const ep of alt.endpoints) {
        if (ep.type !== "bulk") continue;
        if (ep.direction === "in" && !epIn) epIn = ep.endpointNumber;
        if (ep.direction === "out" && !epOut) epOut = ep.endpointNumber;
      }
      if (epIn && epOut) return { iface: iface.interfaceNumber, alt: alt.alternateSetting, epIn, epOut };
    }
  }
  return null;
}

export async function openInterface(dev, cls, sub, proto) {
  if (!dev.opened) await dev.open();
  if (!dev.configuration) await dev.selectConfiguration(1);
  const f = findInterface(dev, cls, sub, proto);
  if (!f) throw new Error(`no interface ${cls}/${sub}/${proto} with bulk endpoints`);
  await dev.claimInterface(f.iface);
  return f;
}

// closeIdle closes the device only when no transfer is in flight.
export async function closeIdle(dev) {
  if (inflight !== 0) throw new Error("refusing to close the device during a transfer");
  if (dev.opened) await dev.close();
}
