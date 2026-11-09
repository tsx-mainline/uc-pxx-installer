# Installing with the UC-P10 installer

This guide installs official Crestron firmware onto a UC-P10 (or UC-P8) with the web installer. The installer
runs from RAM, writes one slot of the unit's A/B pair, and keeps the other slot bootable the whole time, so the
unit always has a firmware it can start.

The installer page is at **https://tsx-mainline.github.io/uc-pxx-installer/** (served by GitHub Pages from
`github.com/tsx-mainline/uc-pxx-installer`). The install image is **not** in the repo: it is a **GitHub release
asset** that the page downloads (the latest release) and starts only after its sha256 matches. If the download
does not work, download the image from the repo's **Releases** page and choose it on the page.

## What you need

- A computer with **Chrome or Edge** (the page uses WebUSB; Firefox and Safari do not work). Chromium works too.
- A **USB-C cable** from the computer to the unit's USB-C port.
- The **Crestron firmware file** (`.bin`) for your unit, on the computer.
- **Internet access** on the computer so the page can download the install image from GitHub (or the image file
  downloaded ahead of time from the project's Releases page).
- The unit on stable power (PoE) that will not be cut during the install.

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
   a switch or a repair, it writes its log folder to the drive P10-INSTALL. This replaces all files on the drive,
   also a firmware file that you copied there.

## Install, step by step

The page walks you through the same steps. Follow it on screen; this is the summary.

1. **Open the installer page** in Chrome or Edge (the URL above).

2. **Installer image.** The page downloads the install image from the project's GitHub release and checks it; it
   starts only an image it recognizes. If the download does not work, download the image from the project's
   **Releases** page and choose the file on the page.

3. **Put the unit in download mode and connect it.**
   - Connect the unit's USB-C port to the computer.
   - Power on the unit. The LED cycles **red, green, blue, white**, then after a few seconds **blinks green**.
   - **When the LED blinks green,** press and hold **Vol−** and **Mic Mute** (the Mic key — not the speaker key).
     The unit restarts: the LED cycles the colors again, then the screen stops on the **Crestron logo**. That logo
     screen is download mode. (You can release the keys once it restarts.)
   - Press **Connect to the unit** and choose the unit in the browser's list.
   - In this step the page only **reads** the unit (storage layout, model, A/B state, boot command). Nothing is
     written yet.

4. **Write the boot command and restart.** When you confirm, the page writes one boot command (4 sectors) and
   restarts the unit into fastboot. The page itself never changes the A/B slots. Keep the unit connected and
   powered.

5. **Start the installer.** Press **Connect to fastboot**, then **Start the installer**. The unit starts the
   installer image from RAM; its screen now shows the installer.

6. **On the unit's screen**, drive the on-screen flow with the touch keys under the screen
   (Speaker = Back, Vol− = Previous, Vol+ = Next, Mic = OK):
   - Choose **Install**.
   - When it asks, copy the firmware `.bin` onto the **P10-INSTALL** drive that the unit now shows on the
     computer, eject the drive, and continue.
   - Confirm you can read the screen on the unit's own panel.
   - The installer checks the firmware and the unit and shows the **plan**: which slot it installs to, and which
     slot it keeps as the fallback (it never touches the kept slot).
   - **Hold OK** (Mic) to start. The installer writes the slot, reads it all back to verify, and makes it the next
     boot. The kept slot stays bootable the whole time.
   - When it says **Done**, restart. The unit starts the newly installed slot; if that slot ever fails to start,
     the kept slot starts again after a few restarts.

## The UC-P8

The installer works on the UC-P8 in the same way. Do the same steps of "Before you start".

- Use the UC-P8 installer image. The installer reads the model from the unit's factory data. If the image and the
  unit are not the same model, it refuses before it writes anything ("This installer image is for the …").
- The screen and the keys are the same as on the UC-P10, only smaller: the same screens, and the same touch keys
  under the screen (Speaker = Back, Vol− = Previous, Vol+ = Next, Mic = OK). Some key labels are shorter, for
  example "Restart" in place of "Hold: restart".

## If something goes wrong

- **Keep the unit powered.** If the screen says "KEEP THE UNIT POWERED", do not cut power or restart. The kept
  slot is still bootable; the message is about the slot being written.
- **Save the log.** The page can save its log to the computer (it sends nothing to any server). The installer also
  writes a log folder to the **P10-INSTALL** drive. Keep the log if you need help.
- **Nothing is sent anywhere.** The page uses no cookies, no analytics, and makes no requests to other sites.

## License

The installer and this page are free software under the GNU General Public License, version 2 or later. The source
is at `github.com/tsx-mainline/uc-pxx-installer`; the page serves its own `LICENSE.txt`.
