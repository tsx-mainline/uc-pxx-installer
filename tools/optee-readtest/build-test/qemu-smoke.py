#!/usr/bin/env python3
"""qemu-smoke.py IMAGE_KERNEL OUTDIR [--insmod] [--disk DISK] [--token p10inst-XXXXXX]

Host smoke test of the OP-TEE read-test RAM image under qemu-system-aarch64 (virt).
The Image carries the initramfs. qemu virt has no eMMC and no secure world, so:
  - the image boots through /init and rcS to busybox init (the console log shows it);
  - a root shell comes up on ttyS2 (the debug-UART getty of the inittab): a
    pci-serial-4x card gives ttyS0..ttyS3, its port 3 is driven over a socket;
  - optee-readtest runs and must REFUSE cleanly (no eMMC) before any module load,
    with no hang and nothing written; result.txt is read back;
  - with --insmod: insmod optee_legacy.ko by hand (no OP-TEE in qemu) to record how
    the module fails without a secure world (informational).
Nothing here touches a device. The disk-less VM has no block device to write.
"""
import os, socket, subprocess, sys, time

img, out = sys.argv[1], sys.argv[2]
do_insmod = "--insmod" in sys.argv
disk = sys.argv[sys.argv.index("--disk") + 1] if "--disk" in sys.argv else None
os.makedirs(out, exist_ok=True)
socks = [os.path.join(out, "s%d.sock" % i) for i in range(1, 5)]
for s in socks:
    if os.path.exists(s):
        os.unlink(s)
token = sys.argv[sys.argv.index("--token") + 1] if "--token" in sys.argv else "p10inst-5c95a8"
append = ("console=ttyAMA0 rdinit=/init loglevel=6 panic=-1 p10inst.drive_mb=64 "
          "p10inst.ram=1 uc-pxx.id=" + token)
cmd = ["qemu-system-aarch64", "-M", "virt", "-cpu", "cortex-a72", "-smp", "2", "-m", "2048",
       "-nographic", "-no-reboot", "-kernel", img, "-append", append,
       "-netdev", "user,id=n0,restrict=on", "-device", "virtio-net-pci,netdev=n0"]
if disk:
    # read-only at the qemu level too: a write would fail in qemu, and the host compares the file sha
    cmd += ["-drive", "file=%s,if=none,format=raw,id=d0,readonly=on" % disk, "-device", "virtio-blk-pci,drive=d0"]
for i, s in enumerate(socks, 1):
    cmd += ["-chardev", "socket,id=c%d,path=%s,server=on,wait=off" % (i, s)]
cmd += ["-device", "pci-serial-4x,chardev1=c1,chardev2=c2,chardev3=c3,chardev4=c4"]
con = open(os.path.join(out, "console.log"), "wb")
q = subprocess.Popen(cmd, stdin=subprocess.DEVNULL, stdout=con, stderr=subprocess.STDOUT)
t0 = time.time()
while not all(os.path.exists(s) for s in socks):
    if time.time() - t0 > 30:
        q.kill(); sys.exit("qemu sockets did not appear")
    time.sleep(0.2)
conns = []
for s in socks:
    c = socket.socket(socket.AF_UNIX); c.connect(s); c.setblocking(False); conns.append(c)
log = open(os.path.join(out, "shell.log"), "wb")
buf = {i: b"" for i in range(4)}

def pump(secs):
    end = time.time() + secs
    while time.time() < end:
        for i, c in enumerate(conns):
            try:
                d = c.recv(65536)
                if d:
                    buf[i] += d; log.write(b"[ttyS%d] " % i + d); log.flush()
            except BlockingIOError:
                pass
        time.sleep(0.05)

def run(port, line, marker, limit):
    buf[port] = b""
    # the marker is typed as M""END so the terminal echo of the command never matches it
    conns[port].sendall(line.encode() + b"; echo " + marker.encode() + b'""END=$?\n')
    end = time.time() + limit
    while time.time() < end:
        pump(0.5)
        if (marker + "END=").encode() in buf[port]:
            pump(0.5)
            return buf[port].decode("utf-8", "replace")
    return None

# wait for the boot: up to 240 s for a shell on one of the ports
shell = None
end = time.time() + 240
while time.time() < end and shell is None:
    pump(3)
    for i, c in enumerate(conns):
        try:
            c.sendall(b"echo SHELL-ON-$(tty)\n")
        except OSError:
            pass
    pump(2)
    for i in range(4):
        if b"SHELL-ON-/dev/ttyS" in buf[i]:
            shell = i
            break
if shell is None:
    q.kill(); sys.exit("FAIL: no shell on ttyS0..3 within 240 s")
print("shell on port index %d: %s" % (shell, buf[shell].decode(errors="replace").strip().splitlines()[-1]))
steps = [
    ("uname -r; cat /proc/cmdline; ls -l /NOT-FOR-DEVICE.txt /usr/sbin/optee-readtest /usr/lib/optee-readtest", "M1", 30),
    ("cat /usr/lib/optee-readtest/SHA256SUMS; ps | grep -c '[t]ee-supplicant'; lsmod | grep -c optee_legacy", "M2", 30),
    ("time /usr/sbin/optee-readtest", "M3", 600),
    ("cat /run/optee-readtest/result.txt; ls /run/optee-readtest; lsmod | grep -c optee_legacy; ls /dev/opteearmtz00", "M4", 30),
]
if do_insmod:
    steps.append(("/usr/sbin/optee-readtest; echo second-run-rc=$?", "M6", 600))
    steps.insert(len(steps) - 1, ("insmod /usr/lib/optee-readtest/optee_legacy.ko; echo insmod-rc=$?; sleep 2; ls -l /dev/opteearmtz00; dmesg | tail -n 40", "M5", 90))
if disk:
    # the same script with the eMMC names mapped to the virtio disk (vda, vda16); the TEE part runs for real
    T = "/run/t-readtest"
    steps = [
        ("ls /dev/vda*; grep PARTNAME /sys/class/block/vda16/uevent; cat /sys/block/vda/ro", "D1", 30),
        ("sed -e 's/mmcblk0p/vda/g' -e 's/mmcblk0/vda/g' /usr/sbin/optee-readtest > %s; chmod 755 %s; "
         "grep -n 'DISK=\\|SECNODE=' %s" % (T, T, T), "D2", 30),
        (T + "; echo t-rc=$?", "D3", 300),
        ("for d in /dev/vda /dev/vda[0-9]*; do blockdev --setro $d; done; cat /sys/block/vda/ro /sys/block/vda/vda16/ro", "D4", 30),
        (T + "; echo t-rc=$?", "D5", 600),
        ("cat /run/optee-readtest/result.txt; cat /run/optee-readtest/supplicant.log; "
         "ls -l /run/optee-readtest; cat /run/optee-readtest/supplicant-rpc.log; lsmod; ls -l /dev/opteearmtz00; "
         "dmesg | tail -n 60", "D6", 60),
    ]
res = {}
for line, mk, lim in steps:
    r = run(shell, line, mk, lim)
    res[mk] = r
    print("==== %s: %s" % (mk, line))
    print(r if r is not None else "TIMEOUT (no marker in %d s)" % lim)
pump(2)
q.terminate()
try:
    q.wait(10)
except subprocess.TimeoutExpired:
    q.kill()
print("qemu exit", q.returncode)
