#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""msos.py VID:PID: read what a Windows host reads before it binds a driver (STEP2-USB-TRANSPORT §3.2), by usbfs control
transfers as root: the device descriptor, string 0xEE (MSFT100 + the vendor code), the extended compat id (device,
wIndex 4), the extended properties (interface 0, wIndex 5), the BOS. Prints JSON. Test only."""
import fcntl
import glob
import json
import os
import struct
import sys
import ctypes

USBDEVFS_CONTROL = 0xC0185500  # _IOWR('U', 0, struct usbdevfs_ctrltransfer), 24 bytes on 64-bit


def find(vid, pid):
    for d in glob.glob("/sys/bus/usb/devices/*"):
        try:
            v = open(d + "/idVendor").read().strip()
            p = open(d + "/idProduct").read().strip()
        except OSError:
            continue
        if v == vid and p == pid:
            bus = int(open(d + "/busnum").read())
            dev = int(open(d + "/devnum").read())
            return "/dev/bus/usb/%03d/%03d" % (bus, dev)
    return None


def ctrl(fd, rt, req, val, idx, n):
    buf = ctypes.create_string_buffer(n)
    arg = struct.pack("=BBHHHI4xQ", rt, req, val, idx, n, 2000, ctypes.addressof(buf))
    try:
        r = fcntl.ioctl(fd, USBDEVFS_CONTROL, bytearray(arg))
    except OSError as e:
        return {"error": str(e)}
    got = r if isinstance(r, int) else 0
    return {"hex": buf.raw[:got].hex(), "len": got}


def main():
    vid, pid = sys.argv[1].split(":")
    path = find(vid, pid)
    if not path:
        print(json.dumps({"error": "no device %s" % sys.argv[1]}))
        return 1
    fd = os.open(path, os.O_RDWR)
    out = {"node": path,
           "device": ctrl(fd, 0x80, 6, 0x0100, 0, 18),
           "string_ee": ctrl(fd, 0x80, 6, 0x03EE, 0, 255),
           "ext_compat": ctrl(fd, 0xC0, 0xCD, 0x0000, 4, 255),
           "ext_prop": ctrl(fd, 0xC1, 0xCD, 0x0000, 5, 1024),
           "bos": ctrl(fd, 0x80, 6, 0x0F00, 0, 255)}
    os.close(fd)
    s = out["string_ee"].get("hex", "")
    if s:
        b = bytes.fromhex(s)
        out["string_ee_text"] = b[2:16].decode("utf-16-le", "replace")
        out["string_ee_vendor_code"] = "0x%02x" % b[16] if len(b) > 16 else None
    e = out["ext_compat"].get("hex", "")
    if e:
        out["ext_compat_id"] = bytes.fromhex(e)[18:26].rstrip(b"\0").decode()
    p = out["ext_prop"].get("hex", "")
    if p:
        out["ext_prop_text"] = bytes.fromhex(p).decode("utf-16-le", "replace").replace("\0", "|")
    print(json.dumps(out))
    return 0


if __name__ == "__main__":
    sys.exit(main())
