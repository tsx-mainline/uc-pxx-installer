# uc-pxx-webdrive: the installer page's step-2 and finish sections, driven headless over WebUSB

A host tool for the supervised unit-2 end-user e2e run (U2-5; owner decision 2026-11-13 "Headless browser on
the build host"). It runs the page's own step-2 and finish code in a headless Chromium on the build host, against the
unit's USB transfer interface (uc-pxx-xfer, 1d6b:0105), with no device chooser and no person at a browser. It is a
host tool only: never in a RAM image, a payload or a package. It does not change the page: it serves a non-served LAN
test copy (`web/test/lan-site.sh`) and clicks the page's own buttons.

## How it runs

`uc-pxx-webdrive` (python3, stdlib) checks everything on the host, then starts the pinned Playwright container
(`mcr.microsoft.com/playwright@sha256:47b510dd…`, Chrome for Testing 156.0.8078.4, node 24) by digest with:

- `--network none --read-only --cap-drop ALL --security-opt no-new-privileges`, as the calling user (uid of `d`);
- read-only binds of this folder, `node_modules` (playwright-core 1.64.1 only, `npm ci` from the sha512 lockfile), the
  page copy, and the one set of the mode (the payload folder `uc-pxx-mainline` or the finish folder `uc-pxx-finish`);
- the policy file `policy.json` at `/etc/opt/chrome_for_testing/policies/managed/`: `WebUsbAllowDevicesForUrls` gives the
  origin `http://127.0.0.1:8137` the device 1d6b:0105, so the page's `navigator.usb.getDevices()` has it and the
  page never asks for the chooser; a `URLBlocklist` entry is the positive control that the file is active;
- in a real run (not `--dry-run`) exactly one device node: `--device /dev/bus/usb/BBB/DDD`, the usbfs node of the one
  1d6b:0105 device of the host (found in sysfs; no node is opened on the host), with its group.

Inside, `driver.mjs` checks every file against `pins.json` again, serves the page copy and the payload on
127.0.0.1:8137 (inside the container: it has no network), checks the policy is active, loads the page with
`inpage.js` (a WebUSB log and a transport guard, below), and drives one mode.

## Modes

    uc-pxx-webdrive setup                                  # once: node_modules and the page copy (npm needs the network)
    uc-pxx-webdrive selfcheck --payload P --finish F       # every local check; prints the lines a GO file names
    uc-pxx-webdrive status [--dry-run] --run DIR           # the page's "Read the unit's status" (HELLO only)
    uc-pxx-webdrive step2 --payload P --go-file G [--dry-run] --run DIR
    uc-pxx-webdrive finish --finish F --go-file G [--dry-run] --run DIR
    uc-pxx-webdrive watch [--every 10] [--max-minutes 60] --run DIR

- **status**: the page's read-only status view: one HELLO. It prints the page's table (model, slot, token, mode,
  state, step-2 code, plan id), the unit's lines and pins.
- **step2**: first the status view, which must show exactly the pinned state (UC-P10, slot B, token `ucpxx-5a454728`,
  mode `payload`, `waiting-payload`, the pins name 62acc9ff… and only manifests of this release, room for the rootfs);
  then the page's "Download the payload from the release site" (same origin: the manifest's sha256 is checked by the
  page before the two files are fetched), "Continue by itself" ticked (AUTO-STEP2: the unit's 30 s countdown that Back
  cancels), "Connect and send to the unit". The run ends at the unit's COMMIT answer (`ok`, `auto: true`), with the
  unit's own sha256 of each file equal to the pin.
- **finish**: the status view must show UC-P10, slot A, mode `finish`, `waiting-finish`, `finish: waiting` and exactly
  the pinned finish list; then the page's finish folder pick and "Connect and send the finish to the unit"; it ends at
  COMMIT (`ok`, `auto: false`).
- **watch**: status reads, each in a new container with the node of the moment (the unit re-enumerates after each
  COMMIT and at its restart), until a final code: `restarting` and then no device (exit 0), `finish: done` (0), a
  stop or hold (7), a refusal (5), or any code it does not know (6).
- **--dry-run**: every step except the ones that open the device: no device node in the container, `inpage.js`
  refuses `open()` and every transfer, no status read, no send; the step-2 dry run downloads and checks the payload.

A step2 or finish real run needs `--go-file` (the session's rule-9 GO, written by the verifier) with exactly one each
of `verdict: GO`, `webdrive-sha256:` (this wrapper's sha256), `a-payload-manifest-sha256:`, `a-rootfs-sha256:`,
`finish-list-sha256:` (the pins). A GO file is used once per mode (`used-go.txt` in the home folder).

## What it refuses or never does

- It refuses unless exactly one 1d6b:0105 device is on the host with the serial `UC-PXX-STEP2`, the product `UC-P10
  mainline step 2` and the maker `uc-pxx`, and no other UC-P10 id (2207:*, 18d1:*, 1d6b:0104) is there; the page must
  see exactly that one device too. It refuses with `uc-pxx-send` or another run of itself active (a lock).
- It refuses any file, page-copy file, policy, image id or node_modules tree that is not the pinned one (regular files
  only, no link followed), before the server starts.
- It stops on any page state, text or answer it does not expect, and then presses nothing more.
- `inpage.js` lets through only the frames of the mode (status: HELLO; step2: HELLO, STATUS, the three PUTs of the
  pinned names and sizes in order, one COMMIT `{"auto":true}` after the unit's sha256 of each file equals the pin;
  finish: the same with the finish set and `{"auto":false}`); it refuses a frame before it is sent, never during a
  transfer, and never refuses file data. It refuses `requestDevice`, `reset`, `forget`, control transfers.
- A send is never stopped: no timeout while the page sends, and the browser is closed only when no OUT transfer is in
  flight. A signal that reaches the container (Ctrl-C or a hangup in the tool's terminal: the whole process group;
  docker forwards it) during a send lets the rest of the set run to the end (up to about 1.1 GB) and then refuses the
  COMMIT (a frame not yet sent): the unit drops the uncommitted set and starts nothing (exit 130). Exit 130 is not
  proof that nothing was committed (a COMMIT already in flight whose answer is lost): read `status` after any end. A
  SIGTERM or SIGHUP sent only to the wrapper's PID is NOT passed to the container: the send and its COMMIT go on.
  Run the tool in tmux or screen with its output not piped (an output error kills the wrapper; the container goes
  on). Never `kill` the wrapper's PID, never `docker kill` a running container (SIGKILL cannot be caught); a
  container that ends with any other code gives exit 6.
- One step-2 send per home: after a real `step2` run that sent a PUT (`step2-sent.txt`), a second real `step2` is
  refused (U2-5 runs AUTO-STEP2 once; another one is the owner's). `watch` treats `finish: switched` (K = a, not the
  U2-5 shape) as unexpected.

## Exit codes

0 done; 2 usage; 3 a local check failed (nothing opened or sent); 4 the USB link failed, no answer or no device (read
the status before any second try); 5 the unit refused (nothing written; read the status); 6 STOP: unexpected (nothing
more pressed); 7 KEEP THE UNIT POWERED; 130 an operator stop (between steps, or during a send: the COMMIT refused).
Any other code (1, 120): STOP; read `result.json` and `status`.

## Logs (the run folder)

`wrapper.json` (every host check, the device, the docker argv), `console.log`, `driver.log` (human), `usb.jsonl`
(every WebUSB call; frames decoded, file data as counts), `page-states.jsonl` (every page state and text change),
`page.log` (the page's own log lines), `server.log` (every request, bytes served), `result.json`, `files.sha256`.

## Tests (no device)

`test/test_webdrive.py`: unit tests of the host checks; end-to-end runs of the wrapper with a fake sysfs, test pins and
`test/fakexfer.js` (a fake navigator.usb modelled on internal/xfer) in the real container and Chromium, with the
refusal, cut, lost-answer, lying-unit, operator-stop and watch paths; `WD_REAL=1` dry runs with the production pins
and the real RC-7 set. `test/guest/run.sh`: the real uc-pxx-xfer on dummy_hcd in a KVM guest (no host USB device)
with the driver run as the container runs it (no udevd, one usbfs node, loopback only, uid 1000).
