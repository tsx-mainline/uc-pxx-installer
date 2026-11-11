# Installing with the UC-P10 installer

This guide installs official Crestron firmware onto a **UC-P10** with the web installer. It is for the UC-P10
only. It is **not** for the UC-P8: do not use it on a UC-P8.

The installer runs from RAM. It always keeps slot A of the unit's A/B pair and installs over slot B. Slot A stays
bootable the whole time, so the unit always has a firmware it can start.

The installer page is at **https://tsx-mainline.github.io/uc-pxx-installer/** (served by GitHub Pages from
`github.com/tsx-mainline/uc-pxx-installer`). The page loads the installer image from its own site. That file is a
copy of the GitHub release asset: the workflow that publishes the site puts it next to the page. The page starts
the image only after its sha256 matches. If the download does not work, download the image from the project's
**Releases** page (https://github.com/tsx-mainline/uc-pxx-installer/releases) and choose the file on the page.

## What you need

- A desktop computer (Windows, macOS or Linux) with **Google Chrome, Microsoft Edge or Chromium**. The page uses
  WebUSB. Firefox and Safari do not work, and the page refuses other browsers (for example Brave or Opera).
- A **USB-C cable** from the computer to the unit's USB-C port.
- The **Crestron firmware file** (`.bin`) on the computer. The installer accepts only **1.0.0.94 userdebug**. It
  refuses every other firmware file before it writes anything.
- **Internet access** on the computer, so that the page can load the installer image (or the image file, downloaded
  before from the project's Releases page).
- The unit on stable power (PoE) that will not be cut during the install.

## Drivers and permissions

The page talks to the unit in two USB modes. Each mode is a different USB device:

| Mode | USB ID (vendor:product) |
|---|---|
| Download mode (Rockchip) | `2207:330c` |
| Fastboot | `18d1:d00d` |

**Linux.** Your user needs access to both devices. Add a udev rule, for example the file
`/etc/udev/rules.d/51-uc-p10.rules` with these two lines:

```
SUBSYSTEM=="usb", ATTR{idVendor}=="2207", ATTR{idProduct}=="330c", MODE="0660", TAG+="uaccess"
SUBSYSTEM=="usb", ATTR{idVendor}=="18d1", ATTR{idProduct}=="d00d", MODE="0660", TAG+="uaccess"
```

Then run `sudo udevadm control --reload-rules`, and disconnect and connect the cable again.

**Windows.** The browser needs the **WinUSB** driver for both devices. Install it with **Zadig**
(https://zadig.akeo.ie):

1. Put the unit in the mode (download mode first; the fastboot device shows only after the page restarts the unit).
2. In Zadig, select **Options > List All Devices**, then the device with the USB ID above.
3. Select **WinUSB** and press **Install Driver** (or **Replace Driver**, if a different driver is installed, for
   example the Rockchip driver).

If the page says that it cannot open the unit in fastboot, do not disconnect or restart the unit. Install WinUSB
for the fastboot device (`18d1:d00d`) with Zadig, then press **Connect to fastboot** again.

**macOS.** No driver is necessary.

On all systems, close other programs that use the unit (for example `fastboot` or `rkdeveloptool`).

## Before you start

Do these steps in the Crestron web UI of the unit **before** you start the installer. The installer cannot change
them: it only reads the auto-update setting, and it does not install while that setting is on or cannot be read.
Choosing **Install** on the installer's screen means that you did these steps.

1. **Crestron auto-update off.** In the Crestron web UI, turn off the automatic firmware update (Settings, Auto
   Update). A firmware update that the unit starts by itself could write the slot that the installer writes.
2. **XiO Cloud off.** In the Crestron web UI, XIO Cloud: turn **XIO Cloud Connection** off. (The installer does not
   check this setting.)
3. **Update server.** In the Crestron web UI, change the update server URL to an address that does not answer, so
   that the unit cannot load a firmware by itself.
4. **Optional: block the Internet** for this unit at your router, as a second guard against an update.
5. **Stable power.** Make sure that the power (PoE) is not cut or switched during the install. Keep the unit
   powered until the screen says that the install ended.
6. **Keep a copy of the firmware file** on the computer. If the installer stops with "KEEP THE UNIT POWERED" after
   a switch or a repair, it writes its log folder to the drive UC-PXX-INST. This replaces all files on the drive,
   also a firmware file that you copied there.

## How the installer uses the two slots

- The unit has two system slots, A and B. The installer **always keeps slot A** and installs over **slot B**.
  It does not touch slot A, and slot A stays the fallback.
- If the unit runs slot B now (for example after a Crestron update), the system in slot B, the newer one, is
  replaced. The unit's screen shows the version in each slot before you start the install.
- The page refuses a unit that runs slot B while its slot A is not marked as successful. It writes nothing to such
  a unit.
- When the page restarts the unit into fastboot, the unit resets its slot table to "both slots trying". This is
  normal. The page checks that the unit shows exactly this normal reset before it starts the installer. If it
  does not, the page stops and keeps the unit in fastboot. The installer sets the slots again during the install.

## Install, step by step

The page walks you through the same steps. Follow it on screen; this is the summary.

1. **Open the installer page** in Chrome, Edge or Chromium (the URL above).

2. **Installer image.** Press the button that uses the image from this site. The page loads the image and checks
   it; it starts only an image it recognizes. If the download does not work, download the image from the
   project's **Releases** page and choose the file on the page.

3. **Put the unit in download mode and connect it.**
   - Connect the unit's USB-C port to the computer.
   - Power on the unit. The LED cycles **red, green, blue, white**, then after a few seconds **blinks green**.
   - **When the LED blinks green,** press and hold **Vol−** and **Mic Mute** (the Mic key, not the speaker key).
     The unit restarts: the LED cycles the colors again, then the screen stays on the **Crestron logo**. That logo
     screen is download mode.
   - Press **Connect to the unit** and choose the unit in the browser's list.
   - In this step the page only **reads** the unit (storage layout, model, A/B state, boot command). Nothing is
     written yet.

4. **Write the boot command and restart.** When you confirm, the page writes one boot command (4 sectors) and
   restarts the unit into fastboot. The page itself never writes the slot table; the unit resets it to "both
   slots trying" (see above). Keep the unit connected and powered.

5. **Start the installer.** Press **Connect to fastboot**, then **Start the installer**. The unit starts the
   installer image from RAM; its screen now shows the installer.

6. **On the unit's screen**, use the touch keys under the screen
   (Speaker = Back, Vol− = Previous, Vol+ = Next, Mic = OK):
   - Choose **Install**.
   - When it asks, copy the firmware `.bin` onto the **UC-PXX-INST** drive (**P10-INSTALL** on installers before the rename) that the unit now shows on the
     computer, eject the drive, and continue.
   - Confirm that you can read the screen on the unit's own panel.
   - The installer can show the screen **"Install to which slot?"**. If it lets you choose slot B, install to
     **slot B** (the screen selects it for you) and press OK (**Install here**). Slot A is not touched. If it does
     not let you choose slot B, press Back (**Cancel**; nothing is written) and ask for help.
   - The installer checks the firmware and the unit and shows the **plan**: which slot it installs to, and which
     slot it keeps as the fallback.
   - **Hold OK** (Mic) to start. The installer writes slot B, reads it all back to verify, and makes it the next
     boot. Slot A stays bootable the whole time.
   - When the screen says **Install done**, **hold OK** (Mic, "Hold: restart") to restart. The unit starts the
     newly installed slot. If that slot fails to start, slot A starts again after a few restarts.

## Good to know

- While the installer runs, its USB serial console (the page's **Open the shell**, or any serial terminal) gives a
  **root shell without a password**. The installer runs from RAM, so this shell ends when the unit restarts.

## If something goes wrong

- **Keep the unit powered.** If the screen says "KEEP THE UNIT POWERED", do not cut power or restart. The kept
  slot is still bootable; the message is about the slot being written.
- **Save the log.** The page can save its log to the computer (it sends nothing to any server). The installer also
  writes a log folder to the **UC-PXX-INST** drive. Keep the log if you need help.
- **Nothing is sent anywhere.** The page uses no cookies, no analytics, and makes no requests to other sites.

## License

The installer and this page are free software under the GNU General Public License, version 2 or later. The source
is at `github.com/tsx-mainline/uc-pxx-installer`; the page serves its own `LICENSE.txt`.
