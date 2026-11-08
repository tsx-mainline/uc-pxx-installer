// SPDX-License-Identifier: GPL-2.0-or-later
// config.js: the reviewed constants of the page. A change here needs a review.
//
// This page is the --plain-entry web front end: it mirrors the reviewed CLI
// `p10-enter rockusb --plain-entry` (uc-pxx-installer branch p10enter-plain
// f631a8a, tool sha256 084394fb..., RULES.md "The BCB bootonce-bootloader route
// into fastboot" and the p10-enter `--plain-entry` entry). The BCB-route entry
// scribbles the A/B block; U-Boot's libavb resets it to a valid, bootable,
// both-slots-trying state. The page tolerates that benign reset (it never
// writes LBA 49156) and validates it before it boots the image.

export const PAGE_VERSION = "web-enter-plain, not reviewed";

// false: the page shows a warning and refuses every device action. Only an
// Opus review of the page plus the user's approval (RULES R-C) may set this to
// true. It stays false until the owner's R-C entry, a new Opus review that
// names this commit's shas, and the R4 hardware proof (FRONTENDS-REVIEW §4).
export const DEVICE_USE_APPROVED = false;

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
// Release 1.2 (keeper-reestablish) install images (RULES.md release-1.2 entry,
// per-release review KEEPER-REESTABLISH-RELEASE-REVIEW-BRIEF.md GO 2026-11-08):
//   UC-P10 install  af3a8e81... token p10inst-fafed7   <- pinned here
//   UC-P8  install  65a95190... token p10inst-4d0395   <- NOT pinned: it is
//     allowed on no unit yet; add it only after the owner's UC-P8 decision.
// The superseded status image b6a14404... (token p10inst-5e15d6) the old page
// pinned is gone: RULES says "superseded ... never publish it" (MF-4).
export const IMAGES = Object.freeze([
  Object.freeze({
    model: "UC-P10",
    label: "UC-P10 installer (release 1.2), token p10inst-fafed7",
    file: "p10inst-install-ucp10-fafed7.img",
    // The exact byte size is filled by the R-D release review from the built
    // image; null here means "check sha256 and the ANDROID! header only".
    size: null,
    token: "p10inst-fafed7",
    sha256: "af3a8e81103159fef1b0ceb34e937a4894d0cbfc62cb559e317248e25d8e3910",
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
