# Installing with the UC-P10 installer

This guide installs a new system onto a **UC-P10** with the web installer. It is for the UC-P10 only. It is **not**
for the UC-P8: do not use it on a UC-P8.

The page offers two goals:

- **Android userdebug:** Crestron's debug Android (official firmware 1.0.0.94 userdebug) in slot B. The Crestron
  firmware in slot A stays on the unit as the backup.
- **Mainline Linux:** Linux in place of the Crestron firmware, in both slots. All settings and data on the unit are
  erased. You choose one system: **Kiosk and Home Assistant**, **Kiosk only**, or **Console only** (for your own
  software).

The installer page is at **https://tsx-mainline.github.io/uc-pxx-installer/** (served by GitHub Pages from
`github.com/tsx-mainline/uc-pxx-installer`). The page loads the installer image and Mainline Linux from its own site.
These files are copies of the GitHub release assets: the workflow that publishes the site puts them next to the page
and checks each one against its pin. The page starts the installer only after its sha256 matches. If a download
does not work, download the file from the project's **Releases** page
(https://github.com/tsx-mainline/uc-pxx-installer/releases) and choose it on the page.

## What you need

- A desktop computer (Windows, macOS or Linux) with **Google Chrome, Microsoft Edge or Chromium**. The page uses
  WebUSB. Firefox and Safari do not work, and the page refuses other browsers (for example Brave or Opera).
- A **USB-C cable** from the computer to the unit's USB-C port.
- The **Crestron userdebug firmware file** `pcdfw-1.0.0.94-userdebug.bin` on the computer, for both goals. The page
  links to Crestron's own download. Crestron can remove that link at any time, so keep a copy of the file. The page
  and the installer accept only this file. They refuse every other file before anything is written.
- **Internet access** on the computer, so that the page can load its files.
- For Mainline Linux with a Kiosk system: **internet access on the unit** (its network cable) while it installs the
  system. The unit downloads its packages by itself. If it cannot, the page sends them over the USB cable.
- The unit on stable power (PoE) that will not be cut during the install.

## Drivers and permissions

The page talks to the unit in three USB modes. Each mode is a different USB device:

| Mode | USB ID (vendor:product) |
|---|---|
| Download mode (Rockchip) | `2207:330c` |
| Fastboot | `18d1:d00d` |
| The installer and the new system (the page's link to the unit) | `1d6b:0105` |

**Linux.** Your user needs access to the three devices. Add a udev rule, for example the file
`/etc/udev/rules.d/51-uc-p10.rules` with these three lines:

```
SUBSYSTEM=="usb", ATTR{idVendor}=="2207", ATTR{idProduct}=="330c", MODE="0660", TAG+="uaccess"
SUBSYSTEM=="usb", ATTR{idVendor}=="18d1", ATTR{idProduct}=="d00d", MODE="0660", TAG+="uaccess"
SUBSYSTEM=="usb", ATTR{idVendor}=="1d6b", ATTR{idProduct}=="0105", ENV{ID_MM_DEVICE_IGNORE}="1", MODE="0660", TAG+="uaccess"
```

Then run `sudo udevadm control --reload-rules`, and disconnect and connect the cable again. Put the third line in
place **before** you start the installer. It also stops ModemManager from using the unit's serial port. Without it,
ModemManager can probe the unit while the page sends a file, and the unit's link can stop. On Linux the page shows
these lines at the steps that need them.

**Windows.** The browser needs the **WinUSB** driver for download mode and for fastboot. Install it with **Zadig**
(https://zadig.akeo.ie). The page shows these steps at the step that needs them:

1. Put the unit in the mode (download mode first; the fastboot device shows only after the page restarts the unit).
2. In Zadig, select **Options > List All Devices**, then the device with the USB ID above.
3. Select **WinUSB** and press **Install Driver** (or **Replace Driver**, if a different driver is installed, for
   example the Rockchip driver).

If the page says that it cannot open the unit in fastboot, do not disconnect or restart the unit. Install WinUSB
for the fastboot device (`18d1:d00d`) with Zadig, then press **Connect** again.

The device `1d6b:0105` carries Microsoft OS descriptors, so Windows 10 and 11 should install WinUSB for it by
themselves. If the page cannot open it, install WinUSB for `1d6b:0105` with Zadig as above.

**macOS.** No driver is necessary.

On all systems, close other programs that use the unit (for example `fastboot` or `rkdeveloptool`).

## Before you start

The page shows these items as a checklist. Tick each one before you go on.

For the **Android userdebug** goal, do these steps in the Crestron web UI of the unit:

1. **Crestron auto-update off.** Turn off the automatic firmware update (Settings, Auto Update). A firmware update
   that the unit starts by itself could write the slot that the installer writes. The installer reads this setting.
   It does not install while the setting is on or cannot be read.
2. **XiO Cloud off.** In XiO Cloud, turn **XiO Cloud Connection** off. (The installer does not check this setting.)
3. **Optional: update server.** Change the update server URL to an address that does not answer, so that the unit
   cannot load a firmware by itself.

The **Mainline Linux** goal does not need these steps: Android does not start again during a mainline install.

For both goals:

1. **Optional: block the internet** for this unit at your router. (Mainline Linux with a Kiosk system then gets its
   packages over the USB cable.)
2. **Stable power.** Make sure that the power (PoE) is not cut or switched during the install.
3. **The USB-C cable** connects the unit to this computer.
4. **Keep a copy of the firmware file** on the computer.

## How the installer uses the two slots

- The unit has two system slots, A and B. In the first part of the install (step 1), the installer **always keeps
  slot A** and installs over **slot B**. It does not write slot A, and slot A stays the fallback.
- When the page restarts the unit into fastboot, the unit resets its slot table to "both slots trying". This is
  normal. The page checks that the unit shows exactly this normal reset before it starts the installer. If it
  does not, the page stops and keeps the unit in fastboot.
- Before it writes slot B, the installer marks slot A as the working fallback again, so that slot A starts if slot
  B does not.
- If the unit runs slot B now (for example after a Crestron update), the system in slot B, the newer one, is
  replaced. The unit's screen shows the version in each slot before you start the install.
- The page refuses a unit that runs slot B while its slot A is not marked as successful. It writes nothing to such
  a unit.
- **Mainline Linux only:** the installer also unlocks the bootloader in step 1, so that the unit starts a system
  that Crestron did not sign. After step 1, slot B starts Mainline Linux, installs the system that you chose, and
  then copies itself into slot A (step 2). After the finish, slot A starts first and slot B is the fallback. The
  Crestron firmware is then gone from the unit.

## Install, step by step

The page walks you through the same steps. Follow it on screen; this is the summary. When a **Next** button is
grey, the page says next to it what is still missing.

1. **Prepare.** Open the installer page in Chrome, Edge or Chromium. Choose **Android userdebug** or **Mainline
   Linux**. For Mainline Linux, also choose the system. Tick the boxes of the checklist, then press **Next**.

2. **Get the files.** The page downloads the installer (and Mainline Linux) from its own site, each with a progress
   bar. Press **Download it from Crestron**, then **Choose the file** and choose `pcdfw-1.0.0.94-userdebug.bin`. The
   page checks its size and sha256.
   - For Mainline Linux you can paste your SSH public key (one line of your `.pub` file, never a private key) and
     confirm its fingerprint. With a key, you can log in to the new system as root over your network. Without
     one, ssh stays off. You can add a key later on the unit's setup page (Access). Console only needs a key.

3. **Put the unit in download mode and connect it.**
   - Connect the unit's USB-C port to the computer.
   - Power on the unit. The LED cycles **red, green, blue, white**, then after a few seconds **blinks green**.
   - **When the LED blinks green,** press and hold **Vol−** and **Mic Mute** (the Mic key, not the speaker key).
     The unit restarts: the LED cycles the colors again, then the screen stays on the **Crestron logo**. That logo
     screen is download mode.
   - Press **Connect to the unit** and choose the unit in the browser's list.
   - In this step the page only **reads** the unit (storage layout, model, slot table, boot command). Nothing is
     written yet.

4. **Start the installer.**
   - Press **Start the installer**. The page writes one boot command, reads it back and restarts the unit into
     fastboot. This is the only write of the page itself. It never writes the slot table.
   - When the page asks, press **Connect** and choose the unit again. The page checks the unit's state and sends the
     installer into the unit's memory. The unit starts it from RAM.
   - The page sends the firmware file over the USB cable into the unit's memory, for Mainline Linux also your key
     and Mainline Linux. Each send has a progress bar. **Do not press keys on the unit while the page sends a
     file.** The unit checks every byte again. Nothing is written to the unit's storage yet.
   - If a transfer stops twice, the page offers **Use the drive instead**. The unit then shows a drive UC-PXX-INST
     on the computer, and the page copies the files to it.

5. **Install.** The unit's screen shows the plan: which slot it installs to, and which slot it keeps as the
   fallback.
   - **Hold OK** (the Mic key) for 3 seconds to start. This is the only confirmation on the unit.
   - The installer marks slot A as the fallback. For Mainline Linux it then unlocks the bootloader: the page shows
     "Unlocking the bootloader", then "Bootloader unlocked". Then the installer writes slot B, reads it all back to
     verify, and makes it the next boot. Slot A stays bootable the whole time.
   - When the screen says that the install is done, the unit restarts by itself after 10 seconds.
   - **Android userdebug:** the unit starts Android userdebug. You can disconnect the cable. The install is done.

6. **Install the system (Mainline Linux).** Slot B starts Mainline Linux. Keep the cable connected.
   - The unit installs the system that you chose. For a Kiosk system it downloads the pinned packages itself. If
     the unit cannot download them, the page offers **Send the packages over the cable instead**: download the file
     from GitHub and choose it. The page checks the file and sends it.
   - The unit's own screen shows the progress. With the RC-10c base (in work), the kiosk's setup screen does not show
     during the install, and step 2's question takes the unit's screen without a flash.
   - Then step 2 starts on the unit by itself after a 30-second countdown on its screen. To stop it, press Back on
     the unit.
   - Step 2 copies the system of slot B into slot A. The page and the unit's screen show the copy with a progress
     bar. Then the unit checks the copy, moves the data, makes slot A the next boot and restarts.

7. **Finish (Mainline Linux).** Slot A starts. The page sends the last files (the finish) over the cable into the
   unit's memory. The unit takes them only when they are the ones that its system names, and runs the finish. When
   the page says **Done. Mainline Linux is installed.**, you can disconnect the cable. Slot A starts first, slot B
   is the fallback.

## Good to know

- While the installer runs, its USB serial console (the page's **Open the shell** in Details, or any serial
  terminal) gives a **root shell without a password**. The installer runs from RAM, so this shell ends when the unit
  restarts.
- The page's **Read the unit's status** in Details reads the unit's link (model, slot, build, step 2). That read
  changes nothing. If a unit waits for the finish (for example after you loaded the page again), the page then
  offers **Send the finish set**.
- Updates (the design of RC-10c, in work, pending the change of the project's rules): with the RC-10c base (mode
  `auto`), a new Mainline Linux install updates itself: its updater (tsx-autoupdate) starts in mode `auto`. Step 2
  and the finish run in mode `auto` as well as in mode `manual`.

## If something goes wrong

- **Keep the unit powered.** If the unit's screen or the page says "KEEP THE UNIT POWERED", do not cut power or
  restart. The kept slot is still bootable. The message is about the slot that was written.
- **Save the log.** The page can save its log to the computer (it sends nothing to any server). Keep the log if you
  need help.
- **The unit's link does not come back.** If the page sees no new device a few seconds after a disconnect, do not
  keep pressing keys on the unit. Before the hold on the plan screen, the installer has written nothing. Put the
  unit in download mode again and start again on the page.
- **Nothing is sent anywhere.** The page uses no cookies, no analytics, and makes no requests to other sites.

## License

The installer and this page are free software under the GNU General Public License, version 2 or later. The source
is at `github.com/tsx-mainline/uc-pxx-installer`; the page serves its own `LICENSE.txt`.
