// SPDX-License-Identifier: GPL-2.0-or-later
// texts.js: every text the page puts on the screen by script (DESIGN v3 §2,
// the mock-up v3 wording). The fixed texts of the steps are in index.html.
// Rules (owner R4, R14, R17): say only what the person must know or do; sector
// numbers, slot letters, A/B states, boot-command contents and the safety
// reasoning go to the log, never here; no time estimate (the bars' computed
// time left is progress.js). A static test reads this file and index.html.

export const TEXT = Object.freeze({
  // Files
  imageOk: "Installer: OK",
  imageMissing: "This site does not have the installer. Download it from the Releases page, then choose it.",
  imageWrong: "This is not the installer of this page. Choose the installer file from the Releases page.",
  downloadStopped: "The download stopped. Check the internet connection, then press Try again.",
  fwOk: "pcdfw-1.0.0.94-userdebug.bin: OK",
  fwWrong: "This is not the Crestron userdebug firmware file. Choose pcdfw-1.0.0.94-userdebug.bin.",
  baseOk: "Mainline Linux: OK",
  baseFolderOk: "Using the folder uc-pxx-mainline from this computer: OK",
  baseWrong: "This folder is not Mainline Linux. Choose the folder uc-pxx-mainline.",
  baseMissing: "This site does not have Mainline Linux. Choose the folder uc-pxx-mainline.",
  keyNeeded: "Console only needs an SSH key.",
  installs: (s) => `The unit installs ${s.installs}. Keep the USB-C cable connected.`,
  bundleOk: (b) => `${b.file}: OK`,
  bundleWrong: (b) => `This is not the package file. Choose ${b.file}.`,
  keyGood: (k) => `Key: ${k.type}, ${k.bits} bits, fingerprint ${k.fingerprint}`,
  keyConfirm: (k) => `This is my key (fingerprint ${k.fingerprint})`,
  keyBad: (reason) => `This key is refused: ${reason} Correct it, or empty the field to go on without a key.`,
  keyChecking: "Checking the key …",
  // what to do after the finish (RC-10d, owner 2026-11-17: say what to do next)
  nextConsole: "Next: log in to the unit as root over ssh, with your key.",
  nextKiosk: "Next: set up the unit on its own screen.",
  keyFileBig: "This file is too large for a public key. Choose the .pub file.",
  keyFilePrivate: "This is a private key. The page did not read it into the field. Choose the .pub file (the same name with .pub at the end).",
  keyFileUnread: "The page could not read this file. Choose the .pub file again, or paste its line.",
  keyFileNotPub: "This file is not an SSH public key of a type the unit takes (ssh-ed25519 or ssh-rsa). Choose the .pub file.",
  // the reason next to a greyed Next (owner R25): one line for each thing that is missing
  whyGoal: "Choose what to install.",
  whySystem: "Choose a system.",
  whyBoxes: "Tick each box that does not say Optional.",
  whyImage: "Get the installer: press Try again, or choose the file.",
  whyImageHand: "Get the installer: download it from GitHub, then choose the file.",
  whyFw: "Choose the Crestron firmware file to go on.",
  whyFwChecking: "Wait while the page checks the firmware file.",
  whyFwBad: "Choose the right Crestron firmware file (pcdfw-1.0.0.94-userdebug.bin).",
  whyBase: "Get Mainline Linux: press Try again, or use a folder on this computer.",
  whyBaseHand: "Get Mainline Linux: use a folder on this computer.",
  whyKeyNeeded: "Paste your SSH key. Console only needs one.",
  whyKeyChecking: "Wait while the page checks the SSH key.",
  whyKeyBad: "Correct the SSH key.",
  whyKeyBadOptional: "Correct the SSH key, or empty the field.",
  whyKeyConfirm: "Tick the box under the key to confirm that it is your key.",
  whyImageLoading: "Wait while the page downloads the installer.",
  whyBaseLoading: "Wait while the page downloads Mainline Linux.",
  // Connect
  unitFound: "Unit found: UC-P10.",
  // the System card: the bar of step 2's copy (R32)
  copyBar: "Copying the system",
  // Start
  transferStopped: "The transfer stopped. Nothing was written to the unit. Check the cable, then press Send again.",
  // Install
  stopHold: "KEEP THE UNIT POWERED. Do not restart it. Save the log and ask for help.",
  // the drive fallback
  ejectWin: "In File Explorer, right-click UC-PXX-INST and choose Eject.",
  ejectMac: "In Finder, press the eject button next to UC-PXX-INST.",
  ejectLinux: "In the file manager, press the eject button next to UC-PXX-INST.",
  driveNotEmpty: "This is not an empty drive UC-PXX-INST. Choose the drive UC-PXX-INST.",
  copyFailed: "The copy failed. Press Copy again.",
  driveHandMainline: "This browser cannot write to the drive. Mainline Linux needs the USB cable here: restart the unit in download mode, then load this page again.",
  // notices (not a stop)
  openFailed: "The page cannot open the unit.",
  openWin: "Windows needs a USB driver for the unit: see Windows: the first time on this computer.",
  openLinux: "Your user needs access to the unit (a udev rule): see Linux: the first time on this computer. Then press the button again.",
  openOther: "Close other programs that use the unit. Then press the button again.",
  twoUnits: "Connect only one unit. Unplug the others, then load this page again.",
  notThisMode: "Choose the unit in the list.",
});

// The stop box (DESIGN §2.8): the title by kind, one "Why:" line by code. The
// full message and advice of the Refusal go to the log.
export const STOP_TITLE = Object.freeze({
  nothing: "The page stopped. Nothing was written to the unit.",
  keep: "The page stopped. Keep the unit connected and powered, and do not restart it.",
  maskrom: "Stop. The unit is in Maskrom mode. Do nothing more with it.",
});
export const STOP_SAVE = "Save the log, then ask for help and give the log.";

// code -> [kind, why]. kind "auto": "nothing" before the boot command was
// written, else "keep".
export const STOP_WHY = Object.freeze({
  "maskrom": ["maskrom", ""],
  "not-loader": ["nothing", "Why: the unit is not in download mode."],
  "other-device": ["keep", "Why: the unit in fastboot is not the same unit."],
  "layout": ["nothing", "Why: the unit's storage is not the one this page knows."],
  "model": ["nothing", "Why: this unit is not a UC-P10."],
  "ab-refused": ["nothing", "Why: the unit's systems are not in a state this page accepts."],
  "ab-not-bootable-pair": ["nothing", "Why: the unit's systems are not in a state this page accepts."],
  "ab-runs-slot-b": ["nothing", "Why: the unit's backup system is not marked as working."],
  "ab-not-expected": ["nothing", "Why: this is not the unit this session is bound to."],
  "bcb-unserved": ["nothing", "Why: the page could not read the unit's boot command."],
  "bcb-command-pending": ["nothing", "Why: a recovery or update is pending on the unit."],
  "state-changed": ["nothing", "Why: the unit changed since the page read it."],
  "write-interrupted": ["keep", "Why: the unit went away while the page wrote the boot command."],
  "readback-mismatch": ["keep", "Why: the boot command did not read back correctly."],
  "ab-changed": ["keep", "Why: the boot command did not read back correctly."],
  "reset-interrupted": ["keep", "Why: the restart did not reach the unit."],
  "getvar-unreadable": ["keep", "Why: the unit's state after the restart is not the expected one."],
  "entry-not-benign": ["keep", "Why: the unit's state after the restart is not the expected one."],
  "max-download": ["keep", "Why: the unit cannot take the installer."],
  "image-sha": ["keep", "Why: the installer in the page's memory changed."],
  "download-interrupted": ["keep", "Why: sending the installer stopped."],
  "fastboot-fail": ["keep", "Why: sending the installer stopped."],
  "no-pre-entry": ["nothing", "Why: the unit is already in fastboot. Power it off and on, then start again in download mode."],
  "read-interrupted": ["auto", "Why: the unit went away."],
  "step1-other": ["keep", "Why: the unit runs another installer than the one the page started."],
  "unexpected": ["auto", "Why: an error the page did not expect."],
  "unexpected-after-write": ["keep", "Why: an error the page did not expect."],
});

// stopView: { title, why } for a fatal Refusal. written: the boot command
// write started (S.before.writeStarted).
export function stopView(code, written) {
  const [kind, why] = STOP_WHY[code] || ["auto", "Why: an error the page did not expect."];
  const k = kind === "auto" ? (written ? "keep" : "nothing") : kind;
  return { title: STOP_TITLE[k], why, kind: k };
}

// ejectLine: the eject text for the platform (userAgentData.platform).
export function ejectLine(platform) {
  return platform === "Windows" ? TEXT.ejectWin : platform === "macOS" ? TEXT.ejectMac : TEXT.ejectLinux;
}
