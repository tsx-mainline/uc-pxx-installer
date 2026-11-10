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

export const PAGE_VERSION = "release 1.2.1 (page 2026-11-09), image p10inst-f9ce0a";

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
// and starts it only when it equals one of these. The sha256 is the authority;
// `size` (when a positive integer) and the "ANDROID!" header are a cheap
// pre-check. Each pin is bound to a model: the page boots only its MODEL's
// image.
//
// Release 1.2 (keeper-reestablish) install images, flow exit-3 fix + UX build
// (RULES.md release-1.2 entry, UPDATED 2026-11-08; per-release review
// KEEPER-REESTABLISH-RELEASE-REVIEW-BRIEF.md GO 2026-11-08 on this build):
//   UC-P10 install  3c522be8... token p10inst-f9ce0a   <- pinned here
//   UC-P8  install  c9f0ca57... token p10inst-697a85   <- NOT pinned: it is
//     allowed on no unit yet; add it only after the owner's UC-P8 decision.
// Supersedes the prior build's af3a8e81 (p10inst-fafed7) that the page pinned.
//
// The image is NOT committed to the git repo: it is a GitHub release asset, and
// the Pages deploy workflow copies it into this site's images/ at build time, so
// the page fetches it same-origin (images/<file>) with no cross-origin request
// (GitHub release downloads are not CORS-fetchable from a browser). The page
// starts it only after its sha256 matches the pin; if the image is not on the
// site, the user picks the file by hand (the sha256 gate still applies).
export const IMAGES = Object.freeze([
  Object.freeze({
    model: "UC-P10",
    label: "UC-P10 installer (release 1.2, keeper-reestablish), token p10inst-f9ce0a",
    file: "p10inst-install-ucp10-f9ce0a.img",
    // Exact byte size of the R-D build (reproducible, ia1==ia2); the sha256 is
    // the authority, size + "ANDROID!" header are a cheap pre-check.
    size: 32382976,
    token: "p10inst-f9ce0a",
    sha256: "3c522be84ebb8bd59a7cac7b94cf638030937a4bef960b3b6583a1a5c8febb12",
  }),
]);

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

// The installer's USB gadget (internal/gadget): CDC-ACM root shell.
export const GADGET = Object.freeze({ usbVendorId: 0x1d6b, usbProductId: 0x0104 });
export const SERIAL_BAUD = 115200;

// ---------------------------------------------------------------------------
// Start-screen goal and the (gated) mainline path.
//
// The page offers a first choice: "Android userdebug" (today's v1.2.1 flow,
// unchanged — the write surface, the image pin and the guards above are its
// only device behaviour) or "Mainline Linux" (the direct/fallback mainline
// install, PAGE-DRIVEN-INSTALL-DESIGN.md). The mainline path is NOT shipped: it
// has no reviewed RULES line, no first-hand mainline-in-B boot record and no
// pinned mainline image, so its button is shown but DISABLED ("coming soon")
// until MAINLINE_INSTALL_APPROVED is set true by a reviewed change. This flag
// gates ONLY the start-screen button; it does not change the userdebug flow.
export const MAINLINE_INSTALL_APPROVED = false;

// The mainline install RAM image(s). EMPTY until a mainline image is built,
// reviewed and pinned (a separate image from the userdebug one; the shipped
// 3c522be8 install image has no mainline mode). Same shape as IMAGES.
export const MAINLINE_IMAGES = Object.freeze([]);

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
export const UNLOCK_GUIDE_ENABLED = true;
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
  // The one-liner scripts, served same-origin (tools/). unlock.js embeds the
  // same text; a unit test checks they are identical.
  scripts: Object.freeze([
    Object.freeze({ os: "Linux / macOS", file: "unlock-linux-macos.sh", shell: "sh" }),
    Object.freeze({ os: "Windows", file: "unlock-windows.ps1", shell: "PowerShell" }),
  ]),
});

// The userdebug firmware .bin the drive-copy helper (drivecopy.js) writes onto
// P10-INSTALL and verifies in the browser. The sha is the 1.0.0.94 userdebug
// firmware the installer's firmware gate accepts (c9766c0a...). The page never
// ships this file; it is the user's own file, written to the user-chosen drive.
export const FIRMWARE = Object.freeze({
  label: "Crestron 1.0.0.94 userdebug firmware",
  file: "pcdfw-1.0.0.94-userdebug.bin",
  // Byte size and sha256 of pcdfw-1.0.0.94-userdebug.bin, from the installer's
  // manifest (manifests/manifest-1.0.0.94-userdebug.json firmware.sha256).
  size: 972963471,
  sha256: "c9766c0a96ca4a199c405143cc3639c2457c060c1535517f252f7c0d3f579718",
});
