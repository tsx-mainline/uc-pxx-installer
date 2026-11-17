// SPDX-License-Identifier: GPL-2.0-or-later
// config.js: the reviewed constants of the page. A change here needs a review.
//
// This page is the --plain-entry web front end: it mirrors the reviewed CLI
// `p10-enter rockusb --plain-entry` (uc-pxx-installer branch
// p10enter-plain-cmdfield 4934986, tool sha256 6fb3acd7..., RULES.md "The BCB
// bootonce-bootloader route into fastboot" and the p10-enter `--plain-entry`
// entry; the dynamic BCB command-field guard, bcb.js 9c95ab53). The BCB-route entry
// scribbles the A/B block; U-Boot's libavb resets it to a valid, bootable,
// both-slots-trying state. The page tolerates that benign reset (it never
// writes LBA 49156) and validates it before it boots the image.

// RC-10c (local test): the pins of the RC-10c integration build
// (RC10C-INTEGRATION-2026-11-17). This page is served from the owner's computer
// for the RC-10c end-to-end run; it is not the public page (main is 1.3.1).
export const PAGE_VERSION = "1.4.0-rc10c-local (2026-11-17): mainline RC-10c (local test), install image pxx-inst-804423";

// true: the page may drive the device (BCB plain-entry + fastboot boot of the
// pinned image). Set true for the end-user release (owner 2026-11-08): the R4
// hardware proof ran the full browser WebUSB page -> BCB entry -> fastboot ->
// image -> on-screen flow install on a unit (HANDOFF 2026-11-08 ~16:20), and the
// install image/engine (pin below, p10inst-f9ce0a) was re-proven to the Done
// screen on hardware (HANDOFF 2026-11-08 ~19:40). The page's own guards are the
// end-user safety (BCB command-field guard, GPT/model gate, plain-entry benign-
// reset validation, image sha pin, EXPECT_FASTBOOT_PRODUCT). RULES R-C is the
// project-internal record of this and is not part of the shipped page. A change
// to this file or the write surface needs a fresh review before it ships.
export const DEVICE_USE_APPROVED = true;

// The model this page is built for. The factory-data model gate and the image
// pin are both bound to it. Unit 2 and unit 3 are UC-P10.
export const MODEL = Object.freeze({
  // The base string the unit's factory data must name (internal/factory rule,
  // mirrored in factory.js: "UC-P8" in the first 5 bytes, else "UC-P10" in the
  // first 6). On this unit the U-Boot download mode does not serve the factory
  // LBA (it returns all 0xCC): that is logged "not readable in the download
  // mode" and is not a stop (owner 2026-11-07). The model is then gated only by
  // the pinned image (its device tree names the model) and by the RAM image's
  // own model gate.
  base: "UC-P10",
  label: "UC-P10",
});

// The RAM images the page may start. The page computes the sha256 of the file
// and starts it only when it equals one of these, and only for a goal that the
// pin names (`goals`). The sha256 is the authority; `size` (when a positive
// integer) and the "ANDROID!" header are a cheap pre-check. Each pin is bound
// to a model: the page boots only its MODEL's image.
//
// RC-10 (DESIGN v3 §10, owner Q2 2026-11-15: "use the NEW image for both
// goals"): ONE install image for both goals. It runs the installer from RAM
// and takes the step-1 files over the USB cable (I-6). For the userdebug goal
// the page sends only the firmware file (COMMIT mode "official"); for the
// mainline goal also part 1 (COMMIT mode "mainline").
//
// RC-10c (local test): the RC-10c install image (uc-pxx-installer
// mainline-rc-10c, images of eafc199, token pxx-inst-804423; record
// RC10C-INTEGRATION-2026-11-17). Release 1.3.0 pinned the RC-10b image
// (token pxx-inst-d876e5). The release asset name is `file`.
//
// The image is NOT committed to the git repo: it is a GitHub release asset, and
// the Pages deploy workflow copies it into this site's images/ at build time, so
// the page fetches it same-origin (images/<file>) with no cross-origin request
// (GitHub release downloads are not CORS-fetchable from a browser). If the
// image is not on the site, the person chooses the file by hand (the same
// sha256 gate).
export const IMAGES = Object.freeze([
  Object.freeze({
    model: "UC-P10",
    goals: Object.freeze(["userdebug", "mainline"]),
    label: "UC-P10 installer 1.4.0 (mainline RC-10c, local test)",
    file: "uc-pxx-inst-ucp10-804423.img",
    size: 34480128,
    token: "pxx-inst-804423",
    sha256: "ba28664fe9c88f159b9308f1be34b1e9d8bba3d23cd4d84e9e2fd8752acf018a",
  }),
]);

// The project's Releases page: the hand fallback for the installer image and,
// while a part has no download link of its own yet, for the mainline parts.
export const RELEASES_URL = "https://github.com/tsx-mainline/uc-pxx-installer/releases";

// The fastboot "product" of this U-Boot (review F9/N-3). Measured on unit 2's
// U-Boot (P10-ENTER-M1.md: product evb_rk3399, variant userdebug). It is a
// generic RK3399 board name, so it is weak identity: the page logs it and warns
// if it differs, but does NOT stop on it. This mirrors the reviewed CLI
// check_plain_entry, which reads product (INFO_VARS) but never gates on it. The
// A/B validation (the benign both-trying reset) and the USB serial-string match
// across the reset are the gates. R4 must confirm the product a stock unit's
// U-Boot reports on this route (unit 3). null = no expected value recorded.
export const EXPECT_FASTBOOT_PRODUCT = "evb_rk3399";

// Optional: the sha256 of the first 32 bytes of the A/B sector that every
// session of this page is bound to (the PRE-entry stock A/B first-32 sha, like
// the CLI's --expect-ab-sha256). null = none. A session can also be bound with
// the URL parameter ?expect-ab=<sha256>; when both are set, both must match.
// This is an extra refusal only; it can never allow anything.
export const EXPECT_AB_FIRST32_SHA256 = null;

// The installer's USB serial shell (CDC-ACM, Web Serial). RC-10 (I-6, DESIGN
// §9.6.3): the RAM image's gadget is 1d6b:0105 (the vendor interface first,
// then ACM and NCM); installers before RC-10 are 1d6b:0104. The shell is read
// by the person only (Details); it sends nothing by itself.
export const GADGETS = Object.freeze([
  Object.freeze({ usbVendorId: 0x1d6b, usbProductId: 0x0105 }),
  Object.freeze({ usbVendorId: 0x1d6b, usbProductId: 0x0104 }),
]);
export const SERIAL_BAUD = 115200;

// ---------------------------------------------------------------------------
// The goals (DESIGN v3 M1). "userdebug": Crestron's debug Android into the
// second slot, the Crestron firmware stays as the backup. "mainline": Linux in
// place of the Crestron firmware (step 1 into slot B, step 2 into slot A, the
// finish). Both use the one RC-10 image (IMAGES[].goals); the goal goes to the
// unit in the step-1 COMMIT ({ mode: "official" } or { mode: "mainline" }), so
// the userdebug button never starts a mainline install.
//
// MAINLINE_INSTALL_APPROVED gates the mainline button. RULES.md item G of the
// RC-5 entry: MAINLINE_INSTALL_APPROVED, STEP2_USB_ENABLED and
// FINISH_USB_ENABLED stay false in every served page until a reviewed page
// change pins the payload, the finish list and an image, and an owner line
// names the served files. RC-10c (local test) pins the RC-10c image, base,
// bundles and finish list and turns the three flags on in this one change, for
// the owner's end-to-end run from a local copy of the page (not the public page).
export const MAINLINE_INSTALL_APPROVED = true;

// ---------------------------------------------------------------------------
// Step 1 over the USB cable (I-6, DESIGN v3 §9.6; owner 2026-11-15 "yes"). The
// RAM installer presents the same USB identity as the step-2 transport
// (STEP2_USB: 1d6b:0105, the vendor interface ff/50/01, serial string
// UC-PXX-STEP2, the PXX1 frames), with HELLO mode "step1". The page sends the
// firmware file (and for mainline the ssh key, then part 1) into the unit's
// RAM. The unit checks every byte itself; nothing is written to the eMMC
// before the one hold on the unit's plan screen. The drive (UC-PXX-INST) is
// the fallback (DESIGN §6). STATUS_EVERY_S: seconds between two STATUS reads.
// CONNECT_HINT_S: the page shows "Press Connect and choose the unit again."
// only when it did not see the installer's link by itself for this long.
export const STEP1 = Object.freeze({ STATUS_EVERY_S: 1, CONNECT_HINT_S: 20 });

// ---------------------------------------------------------------------------
// Step 2 (DESIGN-R23 §4): after the system is installed in slot B, slot A is
// made as a clone of B on the unit (no part 2). The page gives the consent of
// AUTO-STEP2 (the COMMIT with auto true: the unit's 30 s countdown, Back on
// the unit stops it) over B's USB link and follows the unit's status.
//
// ON in RC-10c (local test) (see MAINLINE_INSTALL_APPROVED above).
export const STEP2_USB_ENABLED = true;

// The USB identity of the transfer device: 1d6b:0105, the vendor interface
// ff/50/01. The same values as internal/xfer/proto.go (one constant there).
// Step 1 (RC-10), step 2 and the finish use this one identity, so the
// browser's permission from step 1 also covers step 2 and the finish.
export const STEP2_USB = Object.freeze({ vendorId: 0x1d6b, productId: 0x0105, cls: 0xff, sub: 0x50, proto: 0x01 });

export const STEP2 = Object.freeze({
  // seconds between two STATUS requests while step 2 runs on the unit
  STATUS_EVERY_S: 2,
});

// ---------------------------------------------------------------------------
// The mainline base and the systems (owner R21-R23 2026-11-15, DESIGN-R23.md).
// Step 1 writes a small BASE into slot B (the console system, a schema-3
// payload folder: manifest.json, boot-template.img, rootfs.ext4.zst). The page
// downloads it by itself from this site (BASE.dir, same origin; the Pages
// workflow copies it from the release and checks it, W12), or the person
// chooses the folder uc-pxx-mainline. The manifest comes first and its sha256
// must be BASE.manifest_sha256 before any other byte is read; each other file
// must have the manifest's size and sha256 (the unit checks every byte again,
// unpacks rootfs.ext4.zst on arrival and checks the unpacked file).
//
// RC-10c (local test): the RC-10c base 0.2.0-rc10c-base (manifest 288d132b;
// release 1.3.0 pinned the RC-10b base, manifest 04f4f064). PAYLOAD_URL: the owner's hosting item: another host for the base
// (a same-origin or CORS URL ending in "/"); null = this site's BASE.dir. With
// a URL, index.html's CSP connect-src must name its origin (a reviewed change).
export const BASE = Object.freeze({
  label: "Mainline Linux",
  dir: "base/uc-pxx-mainline/",
  manifest_sha256: "288d132bf3b3bad058177c3cf9164e95b57c1eaba528bca8ccf48e8da28d78e7",
  PAYLOAD_URL: null,
  // the release assets the Pages workflow copies into BASE.dir (W12)
  assets: Object.freeze({ "manifest.json": "uc-pxx-base-manifest.json", "boot-template.img": "uc-pxx-base-boot-template.img",
    "rootfs.ext4.zst": "uc-pxx-base-rootfs.ext4.zst" }),
});

// The systems of the mainline goal (DESIGN-R23 M1). After step 1, slot B
// installs the chosen system by itself (uc-pxx-provision): it downloads the
// pinned package bundle (the GitHub release asset; PROVISION_URL null = the
// unit's own pinned default), or the page sends the bundle over the cable
// (cable mode: "Download from GitHub", "Choose the file", the sha256 pin
// checked by the page first). Console only has no bundle; it has no setup
// page and a locked root password, so the ssh key is required.
// The bundle file names are bundle.py's form (uc-pxx-linux r23-base,
// r23-refs.sh, profiles.json): uc-pxx-profile-<profile>-<release>.tar.
// RC-10c (local test): the bundles of release 0.2.0-rc10c on the pre-release
// mainline-rc-10c (release 1.3.0 pinned the RC-10 bundles of 0.2.0-rc10). `url`
// is only the "Download from GitHub" link of the cable mode (a link, not a
// fetch): the release asset URL that the base's profiles.json names.
export const SYSTEMS = Object.freeze([
  Object.freeze({ id: "kiosk-ha", label: "Kiosk and Home Assistant", installs: "Kiosk and Home Assistant", keyRequired: false,
    bundle: Object.freeze({ file: "uc-pxx-profile-kiosk-ha-0.2.0-rc10c.tar", size: 374302720, sha256: "bd24cdcbc8eac3b71802d3379be7a8ab080526765c113b2d1168b0db6c6bfe18",
      url: "https://github.com/tsx-mainline/uc-pxx-installer/releases/download/mainline-rc-10c/uc-pxx-profile-kiosk-ha-0.2.0-rc10c.tar" }) }),
  Object.freeze({ id: "kiosk", label: "Kiosk only", installs: "the kiosk", keyRequired: false,
    bundle: Object.freeze({ file: "uc-pxx-profile-kiosk-0.2.0-rc10c.tar", size: 321085440, sha256: "0a1b9da838bb6ccd7a474795e2f589d56da500294f6f44cccaead84175168ef9",
      url: "https://github.com/tsx-mainline/uc-pxx-installer/releases/download/mainline-rc-10c/uc-pxx-profile-kiosk-0.2.0-rc10c.tar" }) }),
  Object.freeze({ id: "console", label: "Console only (for your own software)", installs: "the console system", keyRequired: true,
    bundle: null }),
]);
export const PROVISION = Object.freeze({ PROVISION_URL: null, STATUS_EVERY_S: 1 });

// ---------------------------------------------------------------------------
// The finish of step 2 over the USB cable (mainline-rc-4; owner 2026-11-11,
// Q-T8 = option 5). After step 2 the unit runs the new mainline slot A. On its
// first boot that slot's interface waits for the PINNED finish set:
// finish.json (kind uc-pxx-finish, version 1) and the two files it names,
// uc-pxx-step2-finish and uc-pxx-bootctl-inst. The page fetches them from its
// own site (SITE_DIR; the Pages workflow copies them from the release and
// checks them against LISTS, W12), checks them, and sends them into the unit's
// RAM; the unit takes finish.json only when its sha256 is the one of the
// slot's own list, checks the two files itself, and runs the finish from RAM.
//
// ON in RC-10c (local test) (see MAINLINE_INSTALL_APPROVED above).
export const FINISH_USB_ENABLED = true;

// LISTS: the sha256 of each finish.json this page sends ({ label, sha256 }).
// RC-10c (local test): the RC-10c base's /usr/share/uc-pxx-usb/finish.json
// a1e2d88c (uc-pxx-step2-finish 71b7a019 and uc-pxx-bootctl-inst r2 20aa6bc5;
// release 1.3.0 pinned the RC-10b finish set 66dfb112). The unit's own pin
// (HELLO) must also be this sha256.
// STATUS_EVERY_S: seconds between two STATUS requests after the send.
export const FINISH = Object.freeze({
  LISTS: Object.freeze([
    Object.freeze({ label: "the RC-10c finish set", sha256: "a1e2d88c8f7d46b25ecb17f1a699e9207cbe7ec7c5f5562abc9dbb9929f5a009" }),
  ]),
  SITE_DIR: "finish/",
  STATUS_EVERY_S: 2,
});

// ---------------------------------------------------------------------------
// Optional unlock guide (tier A, opt-in, AFTER a userdebug install).
//
// This is a GUIDE only: it shows one-liner scripts the user runs on THEIR OWN
// computer over LAN adb (it drives no device over WebUSB from the browser). The
// client is a reproducible, public-source Android arm64 build (tools/tee-unlock,
// SOURCES.md): linked against a stub libteec, never the device's libteec. It is
// bundled same-origin (the Pages workflow copies it from the latest release into
// web/site/tools/, like the install image), so the one-liner downloads it from
// this page's own origin (CSP connect-src 'self').
//
// OFF by default. An end-user TEE unlock is a NEW KIND OF WRITE, which RULES.md
// makes the owner's decision, and the client sha below is not yet named in
// RULES. The guide ships only when an owner line names the client sha and the
// scripts (same gate as the mainline button). Flip to true in that reviewed
// change; a test may flip it locally.
export const UNLOCK_GUIDE_ENABLED = false;
export const UNLOCK = Object.freeze({
  // The reproducible client (tools/tee-unlock built with NDK r26d, stub libteec).
  // This sha is byte-different from the hardware-proven fc6eea50... client
  // (which was linked against the device's libteec and cannot be reproduced from
  // public inputs); same source, same imports. OWNER DECISION before end-user
  // unlock: a one-time read-only `tee_unlock read` proof of this binary on a
  // unit, or keep the proven client. The scripts and the page pin this sha.
  client: Object.freeze({
    file: "tee_unlock.android-arm64",
    size: 6304,
    sha256: "6ba5d28c0854f9ca110f22fe5a3a108a1ba068cab40755256061bb83292fc205",
  }),
  // The unlock marker the one-liner writes (unencrypted /cache; the installer
  // reads it from the 'cache' partition by name). A claim, not the authority.
  markerPath: "/cache/uc-pxx/unlock-marker",
  // The one-liner scripts, served same-origin (tools/). unlock.js fetches this
  // text from tools/ and shows it; a unit test checks the pinned client sha is
  // present in each script.
  scripts: Object.freeze([
    Object.freeze({ os: "Linux / macOS", file: "unlock-linux-macos.sh", shell: "sh" }),
    Object.freeze({ os: "Windows", file: "unlock-windows.ps1", shell: "PowerShell" }),
  ]),
});

// The Crestron userdebug firmware file. The person downloads it from
// Crestron's own URL (a normal browser download: Crestron's site sends no CORS
// header, so the page never fetches it, and the project never re-hosts it;
// owner R19) and chooses it; the page checks its size and sha256 (the
// streaming hash) and sends it to the RAM installer (step 1, I-6) or copies it
// to the drive (the fallback). The sha is the 1.0.0.94 userdebug firmware that
// the installer's firmware gate accepts (manifests/manifest-1.0.0.94-userdebug.json
// firmware.sha256).
export const FIRMWARE = Object.freeze({
  label: "Crestron userdebug firmware file",
  file: "pcdfw-1.0.0.94-userdebug.bin",
  url: "https://crestrondevicefiles.blob.core.windows.net/firmware/pcdfw-1.0.0.94-userdebug.bin",
  size: 972963471,
  sha256: "c9766c0a96ca4a199c405143cc3639c2457c060c1535517f252f7c0d3f579718",
});
