// SPDX-License-Identifier: GPL-2.0-or-later
// progress.js: the one progress bar of the page (DESIGN §4). Every transfer
// uses it: the installer download, the file checks, the fastboot send, the
// step-1 sends, part 2, the finish set, and the drive copies of the fallback.
//
// Line 3 of a bar: "<done> MB of <total> MB · about <t> left", at the end
// "<total> MB · done"; KB for small files; MB = 10^6 bytes. The time left is
// COMPUTED from the mean speed of the last 10 s (owner 2026-11-15: keep it, it
// is calculated, not a guess); under 90 s "about 40 s left", else "about 3 min
// left". No other text of the page gives a time (static test). MB/s goes to
// the log only (the caller logs rate()).

export const WINDOW_MS = 10000;

function unitOf(total) {
  return total >= 1e6 ? { div: 1e6, name: "MB" } : { div: 1e3, name: "KB" };
}

function num(n) {
  return Math.round(n).toLocaleString("en-US");
}

// leftText: the computed time left, or "" while there is no speed yet.
export function leftText(seconds) {
  if (!Number.isFinite(seconds) || seconds < 0) return "";
  if (seconds < 90) return `about ${Math.max(1, Math.round(seconds))} s left`;
  return `about ${Math.round(seconds / 60)} min left`;
}

// amountText: the third line of a bar. left: seconds (NaN = not known yet).
export function amountText(done, total, left) {
  const u = unitOf(total);
  if (total > 0 && done >= total) return `${num(total / u.div)} ${u.name} · done`;
  if (done <= 0) return "starting …";
  const t = leftText(left);
  return `${num(done / u.div)} ${u.name} of ${num(total / u.div)} ${u.name}` + (t ? ` · ${t}` : "");
}

// countText: a bar of things, not bytes (the packages the unit installs,
// DESIGN-R23 §7.4.1): "<n> of <N>".
export function countText(done, total) {
  if (total <= 0) return "starting …";
  return `${num(Math.min(done, total))} of ${num(total)}`;
}

// Rate keeps the samples of the last WINDOW_MS and gives the mean speed.
export class Rate {
  #s = [];
  constructor(now = () => Date.now()) {
    this.now = now;
  }
  add(done) {
    const t = this.now();
    this.#s.push([t, done]);
    while (this.#s.length > 2 && t - this.#s[1][0] >= WINDOW_MS) this.#s.shift();
  }
  // bytes per second over the window, or NaN
  speed() {
    if (this.#s.length < 2) return NaN;
    const [t0, d0] = this.#s[0];
    const [t1, d1] = this.#s[this.#s.length - 1];
    if (t1 <= t0 || d1 < d0) return NaN;
    return ((d1 - d0) * 1000) / (t1 - t0);
  }
  left(done, total) {
    const v = this.speed();
    return v > 0 ? (total - done) / v : NaN;
  }
}

// Bar is the DOM of one bar: label, bar, amount. el is the <div class="prog">.
export class Bar {
  constructor(doc, label, count = false) {
    this.count = count;
    this.el = doc.createElement("div");
    this.el.className = "prog";
    const l = doc.createElement("div");
    l.className = "label";
    l.textContent = label;
    const b = doc.createElement("div");
    b.className = "bar";
    b.setAttribute("role", "progressbar");
    b.setAttribute("aria-label", label);
    b.setAttribute("aria-valuemin", "0");
    b.setAttribute("aria-valuemax", "100");
    this.fill = doc.createElement("div");
    b.append(this.fill);
    this.amount = doc.createElement("div");
    this.amount.className = "amount";
    this.el.append(l, b, this.amount);
    this.bar = b;
    this.label = label;
    this.clear();
  }
  clear() {
    this.rate = new Rate();
    this.t0 = Date.now();
    this.done = 0;
    this.total = 0;
    this.fill.style.width = "0%";
    this.el.classList.remove("complete");
    this.amount.textContent = "starting …";
    this.bar.setAttribute("aria-valuenow", "0");
  }
  update(done, total) {
    this.done = done;
    this.total = total;
    this.rate.add(done);
    const pct = total > 0 ? Math.min(100, (100 * done) / total) : 0;
    this.fill.style.width = pct.toFixed(2) + "%";
    this.el.classList.toggle("complete", total > 0 && done >= total);
    this.amount.textContent = this.count ? countText(done, total) : amountText(done, total, this.rate.left(done, total));
    this.bar.setAttribute("aria-valuenow", String(Math.floor(pct)));
    this.bar.setAttribute("aria-valuetext", this.amount.textContent);
  }
  // mbps: the mean speed of the whole transfer (for the log)
  mbps() {
    const s = Math.max((Date.now() - this.t0) / 1000, 0.001);
    return (this.done / 1e6 / s).toFixed(1);
  }
}
