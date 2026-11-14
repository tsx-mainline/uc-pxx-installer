// SPDX-License-Identifier: GPL-2.0-or-later
// sshkey.js: the check of the optional ssh public key that the page writes as
// SSH-KEY.PUB onto the installer's drive. The RAM installer applies the same
// rule to that file (the shared test vectors are web/test/unit/
// sshkey-vectors.json). This module is pure: no DOM, no file, no device, no
// network; the one async step is the sha256 of the fingerprint (WebCrypto).
//
// The rule: exactly one line, optionally ended by one LF or one CRLF; at most
// 8192 bytes in all; `TYPE BASE64[ COMMENT]` with single spaces and nothing
// else (no options in front, no other fields). TYPE is ssh-ed25519 (the key
// data is string "ssh-ed25519" + string of 32 bytes, nothing after) or ssh-rsa
// (string "ssh-rsa" + mpint e + mpint n, nothing after; n of 3072 to 16384
// bits; e odd and 3 or more; both mpints canonical: positive, no superfluous
// leading zero byte). BASE64 is canonical standard base64 with padding: the
// decoded bytes encode back to the same text. COMMENT has 1 to 64 characters
// of A-Z a-z 0-9 . _ @ + = : -. The file is the line + one LF.

export const FILE_NAME = "SSH-KEY.PUB";
export const MAX_BYTES = 8192;
export const RSA_MIN_BITS = 3072;
export const RSA_MAX_BITS = 16384;
export const TYPES = Object.freeze(["ssh-ed25519", "ssh-rsa"]);
const COMMENT_RE = /^[A-Za-z0-9._@+=:-]{1,64}$/;
const B64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
const B64_RE = /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/;
const USE = "Use an ssh-ed25519 key, or an ssh-rsa key of 3072 bits or more.";

class KeyError extends Error {}
function refuse(msg) {
  throw new KeyError(msg);
}

// b64encode: standard base64; pad false leaves out the '=' (the fingerprint form).
export function b64encode(b, pad = true) {
  let out = "";
  let i = 0;
  for (; i + 2 < b.length; i += 3) {
    const v = (b[i] << 16) | (b[i + 1] << 8) | b[i + 2];
    out += B64[(v >> 18) & 63] + B64[(v >> 12) & 63] + B64[(v >> 6) & 63] + B64[v & 63];
  }
  if (b.length - i === 1) {
    const v = b[i] << 16;
    out += B64[(v >> 18) & 63] + B64[(v >> 12) & 63] + (pad ? "==" : "");
  } else if (b.length - i === 2) {
    const v = (b[i] << 16) | (b[i + 1] << 8);
    out += B64[(v >> 18) & 63] + B64[(v >> 12) & 63] + B64[(v >> 6) & 63] + (pad ? "=" : "");
  }
  return out;
}

// b64decodeCanonical: the bytes of canonical standard base64 with padding, or
// null for anything else (another alphabet, no or wrong padding, set unused
// bits, whitespace).
export function b64decodeCanonical(s) {
  if (typeof s !== "string" || s === "" || !B64_RE.test(s)) return null;
  const pad = s.endsWith("==") ? 2 : s.endsWith("=") ? 1 : 0;
  const out = new Uint8Array((s.length / 4) * 3 - pad);
  let o = 0;
  for (let i = 0; i < s.length; i += 4) {
    const c = [0, 1, 2, 3].map((k) => (s[i + k] === "=" ? 0 : B64.indexOf(s[i + k])));
    const v = (c[0] << 18) | (c[1] << 12) | (c[2] << 6) | c[3];
    for (const x of [(v >> 16) & 255, (v >> 8) & 255, v & 255]) if (o < out.length) out[o++] = x;
  }
  return b64encode(out) === s ? out : null;
}

// Reader: the SSH wire format (RFC 4251): uint32 big-endian length + bytes.
class Reader {
  constructor(b) {
    this.b = b;
    this.o = 0;
  }
  string() {
    const { b } = this;
    if (b.length - this.o < 4) refuse("The key data is cut short.");
    const n = ((b[this.o] << 24) | (b[this.o + 1] << 16) | (b[this.o + 2] << 8) | b[this.o + 3]) >>> 0;
    this.o += 4;
    if (n > b.length - this.o) refuse("The key data is cut short.");
    const v = b.subarray(this.o, this.o + n);
    this.o += n;
    return v;
  }
  // mpint: a positive, canonical multiple-precision integer (RFC 4251 §5).
  mpint(name) {
    const v = this.string();
    if (v.length === 0) refuse(`The RSA ${name} is zero.`);
    if (v[0] & 0x80) refuse(`The RSA ${name} is negative.`);
    if (v[0] === 0 && (v.length === 1 || !(v[1] & 0x80))) refuse(`The RSA ${name} has a superfluous leading zero byte.`);
    return v[0] === 0 ? v.subarray(1) : v;
  }
  end() {
    if (this.o !== this.b.length) refuse("The key data has extra bytes after the key.");
  }
}

function bitLength(mag) {
  let top = mag[0];
  let n = 0;
  while (top) {
    n++;
    top >>= 1;
  }
  return (mag.length - 1) * 8 + n;
}

function ascii(b) {
  let s = "";
  for (const x of b) s += String.fromCharCode(x);
  return s;
}

function shown(t) {
  return t.length > 40 ? `${t.slice(0, 40)}…` : t;
}

// parse: the checked key, or a KeyError with a plain reason.
function parse(text) {
  if (typeof text !== "string" || text === "") refuse("The key is empty.");
  const bytes = new TextEncoder().encode(text).length;
  if (bytes > MAX_BYTES) refuse(`The key is ${bytes} bytes long; at most ${MAX_BYTES} bytes are allowed.`);
  if (/PRIVATE KEY/.test(text)) {
    refuse("This is a PRIVATE key. Never give it to anyone. Give the one line of the matching .pub file instead.");
  }
  const line = text.endsWith("\r\n") ? text.slice(0, -2) : text.endsWith("\n") ? text.slice(0, -1) : text;
  if (line === "") refuse("The key is empty.");
  if (/[\r\n]/.test(line)) refuse("The key must be one line. Give only the one line of the .pub file.");
  if (/[^\x20-\x7e]/.test(line)) {
    refuse("The key has a character that is not allowed (a tab, a control character or a non-ASCII letter). " +
      "Give the line exactly as it is in the .pub file.");
  }
  const parts = line.split(" ");
  if (parts.some((p) => p === "")) {
    refuse("The parts of the line must be separated by single spaces, with no space at the start or the end.");
  }
  const type = parts[0];
  if (!TYPES.includes(type)) {
    if (parts.slice(1).some((p) => TYPES.includes(p) || /^(ecdsa-sha2-|sk-|ssh-dss)/.test(p))) {
      refuse("The line starts with options (such as restrict, no-pty or command=). Give only the key: its type, " +
        "the key data and an optional comment.");
    }
    refuse(`The key type "${shown(type)}" is not accepted. ${USE}`);
  }
  if (parts.length < 2) refuse("The key data is missing after the type.");
  if (parts.length > 3) refuse("The line has more than three parts. The comment, if any, is one word: no spaces.");
  const blob = b64decodeCanonical(parts[1]);
  if (!blob) refuse("The key data is not valid base64 (the standard alphabet with padding, as ssh-keygen writes it).");
  if (parts.length === 3 && !COMMENT_RE.test(parts[2])) {
    refuse("The comment may have 1 to 64 characters: letters, digits and . _ @ + = : - (no spaces, quotes or " +
      "backslashes).");
  }
  const r = new Reader(blob);
  const bt = ascii(r.string());
  if (bt !== type) {
    const what = /^[\x21-\x7e]{1,64}$/.test(bt) ? `an "${shown(bt)}" key` : "not that type";
    refuse(`The line says ${type}, but the key data is ${what}. ${USE}`);
  }
  let bits;
  if (type === "ssh-ed25519") {
    const pk = r.string();
    if (pk.length !== 32) refuse(`An ssh-ed25519 key has 32 bytes; this one has ${pk.length}.`);
    bits = 256;
  } else {
    const e = r.mpint("exponent");
    const n = r.mpint("modulus");
    if (!(e[e.length - 1] & 1) || (e.length === 1 && e[0] < 3)) refuse("The RSA exponent must be odd and 3 or more.");
    bits = bitLength(n);
    if (bits < RSA_MIN_BITS) {
      refuse(`This RSA key has ${bits} bits; at least ${RSA_MIN_BITS} are needed. Make a new key, for example ` +
        "with ssh-keygen -t ed25519.");
    }
    if (bits > RSA_MAX_BITS) refuse(`This RSA key has ${bits} bits; at most ${RSA_MAX_BITS} are allowed.`);
  }
  r.end();
  return { type, bits, line, blob };
}

// checkKey: { ok: true, type, bits, line, blob } or { ok: false, reason }.
// `line` is the normalised line (no line end); the file is line + LF.
export function checkKey(text) {
  try {
    return { ok: true, ...parse(text) };
  } catch (e) {
    if (e instanceof KeyError) return { ok: false, reason: e.message };
    throw e;
  }
}

// fingerprint: the OpenSSH SHA256 fingerprint of the key data (what
// `ssh-keygen -l` prints): "SHA256:" + base64 without padding of sha256(blob).
export async function fingerprint(blob, subtle = globalThis.crypto.subtle) {
  return `SHA256:${b64encode(new Uint8Array(await subtle.digest("SHA-256", blob)), false)}`;
}

// validate: checkKey plus the fingerprint and the file's text.
export async function validate(text, subtle = globalThis.crypto.subtle) {
  const r = checkKey(text);
  if (!r.ok) return r;
  return { ...r, fingerprint: await fingerprint(r.blob, subtle), file: `${r.line}\n` };
}
