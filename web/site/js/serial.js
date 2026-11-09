// SPDX-License-Identifier: GPL-2.0-or-later
// serial.js: the installer's USB serial shell over Web Serial, with a
// transcript for the log file.
import { GADGET, SERIAL_BAUD } from "./config.js";

// Remove terminal escape sequences for the plain text view.
export function plain(s) {
  // eslint-disable-next-line no-control-regex
  return s.replace(/\x1b\[[0-9;?]*[A-Za-z]/g, "").replace(/\x1b[()][A-Z0-9]/g, "").replace(/\r(?!\n)/g, "");
}

export class Shell {
  #port;
  #writer = null;
  #reading = false;
  transcript = "";

  constructor(port, onText) {
    this.#port = port;
    this.onText = onText;
  }

  static async request(serial, onText) {
    const port = await serial.requestPort({ filters: [GADGET] });
    await port.open({ baudRate: SERIAL_BAUD });
    const sh = new Shell(port, onText);
    sh.#readLoop();
    return sh;
  }

  async #readLoop() {
    if (this.#reading) return;
    this.#reading = true;
    const dec = new TextDecoder();
    let ended = false;
    while (this.#port.readable && !ended) {
      const reader = this.#port.readable.getReader();
      try {
        for (;;) {
          const { value, done } = await reader.read();
          if (done) {
            ended = true;
            break;
          }
          const t = dec.decode(value, { stream: true });
          this.transcript += t;
          this.onText(plain(t));
        }
      } catch (e) {
        this.onText(`\n[serial: ${e && e.message}]\n`);
        break;
      } finally {
        reader.releaseLock();
      }
    }
    this.#reading = false;
  }

  async send(line) {
    if (!this.#writer) this.#writer = this.#port.writable.getWriter();
    await this.#writer.write(new TextEncoder().encode(line + "\r"));
  }
}
