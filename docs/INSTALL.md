# Installing with the UC-P10 installer (draft)

Draft for the install guide and the web page. The project owner decides the final words.

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

## The UC-P8

The installer works on the UC-P8 in the same way. Do the same steps of "Before you start".

- Use the UC-P8 installer image. The installer reads the model from the unit's factory data. If the image and the
  unit are not the same model, it refuses before it writes anything ("This installer image is for the …").
- The screen and the keys are the same as on the UC-P10, only smaller: the same screens, and the same touch keys
  under the screen (Speaker = Back, Vol- = Previous, Vol+ = Next, Mic = OK). Some key labels are shorter, for
  example "Restart" in place of "Hold: restart".
