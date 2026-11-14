#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""Make web/test/unit/sshkey-vectors.json: the test vectors of the SSH-KEY.PUB rule.

  python3 -I web/test/unit/sshkey-vectors.py > web/test/unit/sshkey-vectors.json

The page (web/site/js/sshkey.js) and the installer (Go) apply the same rule to
the optional ssh public key; both test suites read this one file. Each run
makes NEW throwaway keys with ssh-keygen in a temporary folder (the private
halves are deleted with it), so the output differs from run to run: the
committed JSON is the reference, not this script's next output.

The expected fingerprint and size of every accepted key come from
`ssh-keygen -l -E sha256 -f` (the script refuses to write the file when its own
computation disagrees). For a refused input, `ssh_keygen_l` records what
ssh-keygen prints for it (or null when ssh-keygen refuses it too); it is
information only: the rule is stricter than ssh-keygen.

The rule (the installer's, owner 2026-11-13): exactly one line, optionally
ended by one LF or one CRLF; at most 8192 bytes in all; `TYPE BASE64[ COMMENT]`
with single spaces and nothing else (no options); TYPE ssh-ed25519 (blob =
string "ssh-ed25519" + string of 32 bytes, nothing after) or ssh-rsa (blob =
string "ssh-rsa" + mpint e + mpint n, nothing after; n of 3072..16384 bits; e
odd and >= 3; both mpints canonical and positive); BASE64 canonical standard
base64 with padding; COMMENT 1..64 characters of A-Z a-z 0-9 . _ @ + = : -.
The file the page writes is the line + one LF.
"""
import base64
import hashlib
import json
import os
import secrets
import struct
import subprocess
import sys
import tempfile


def s(b):
    return struct.pack(">I", len(b)) + b


def mpint(x):
    if x == 0:
        return s(b"")
    b = x.to_bytes((x.bit_length() + 7) // 8, "big")
    if b[0] & 0x80:
        b = b"\0" + b
    return s(b)


def b64(b):
    return base64.b64encode(b).decode()


def fp(blob):
    return "SHA256:" + base64.b64encode(hashlib.sha256(blob).digest()).decode().rstrip("=")


def odd_n(bits):
    """A random odd number of exactly `bits` bits (not a real modulus: the rule does not check primes)."""
    return secrets.randbits(bits) | (1 << (bits - 1)) | 1


class Gen:
    def __init__(self, tmp):
        self.tmp = tmp
        self.n = 0

    def path(self):
        self.n += 1
        return os.path.join(self.tmp, "k%d" % self.n)

    def keygen(self, t, comment, bits=None):
        p = self.path()
        cmd = ["ssh-keygen", "-q", "-t", t, "-N", "", "-C", comment, "-f", p]
        if bits:
            cmd[2:2] = ["-b", str(bits)]
        subprocess.run(cmd, check=True, stdin=subprocess.DEVNULL)
        with open(p + ".pub") as f:
            pub = f.read()
        return p, pub

    def cert(self, keypath, comment):
        ca, _ = self.keygen("ed25519", "test-ca")
        subprocess.run(["ssh-keygen", "-q", "-s", ca, "-I", "test-cert", "-n", "test", keypath + ".pub"],
                       check=True, stdin=subprocess.DEVNULL, capture_output=True)
        with open(keypath + "-cert.pub") as f:
            t, k, _ = f.read().split(" ", 2)
        return "%s %s %s" % (t, k, comment)

    def l(self, text):
        p = self.path()
        with open(p, "w", newline="") as f:
            f.write(text)
        r = subprocess.run(["ssh-keygen", "-l", "-E", "sha256", "-f", p], capture_output=True, text=True,
                           stdin=subprocess.DEVNULL)
        if r.returncode != 0:
            return None
        return [x for x in r.stdout.split("\n") if x]


def main():
    out = []
    with tempfile.TemporaryDirectory() as tmp:
        g = Gen(tmp)
        _, ed = g.keygen("ed25519", "test-ed25519")
        ed = ed.rstrip("\n")
        edt, edk, _ = ed.split(" ")
        edblob = base64.b64decode(edk)
        edpk = edblob[-32:]
        _, r3072 = g.keygen("rsa", "test-rsa3072", 3072)
        _, r4096 = g.keygen("rsa", "test-rsa4096", 4096)
        _, r2048 = g.keygen("rsa", "test-rsa2048", 2048)
        r3072, r4096, r2048 = (x.rstrip("\n") for x in (r3072, r4096, r2048))
        _, ec = g.keygen("ecdsa", "test-ecdsa", 256)
        ec = ec.rstrip("\n")
        edkp, _ = g.keygen("ed25519", "test-for-cert")
        cert = g.cert(edkp, "test-cert")
        ecblob = base64.b64decode(ec.split(" ")[1])
        # ecdsa blob: string type, string curve, string Q
        o = 0
        parts = []
        while o < len(ecblob):
            (n,) = struct.unpack(">I", ecblob[o:o + 4])
            parts.append(ecblob[o + 4:o + 4 + n])
            o += 4 + n
        ecq = parts[2]

        def rsa_blob(e, n_bytes_or_int):
            nb = n_bytes_or_int if isinstance(n_bytes_or_int, bytes) else mpint(n_bytes_or_int)
            eb = e if isinstance(e, bytes) else mpint(e)
            return s(b"ssh-rsa") + eb + nb

        def ed_blob(pk):
            return s(b"ssh-ed25519") + s(pk)

        r3072k = r3072.split(" ")[1]
        r3072blob = base64.b64decode(r3072k)

        V = []

        def add(name, text, valid, why, typ=None, bits=None):
            V.append((name, text, valid, why, typ, bits))

        # --- accepted -------------------------------------------------------
        add("ed25519", ed, True, "a real ed25519 key (ssh-keygen)", "ssh-ed25519", 256)
        add("ed25519-lf", ed + "\n", True, "one trailing LF", "ssh-ed25519", 256)
        add("ed25519-crlf", ed + "\r\n", True, "one trailing CRLF", "ssh-ed25519", 256)
        add("ed25519-no-comment", "%s %s" % (edt, edk), True, "no comment", "ssh-ed25519", 256)
        add("ed25519-comment-64", "%s %s %s" % (edt, edk, ("Az09._@+=:-" * 6)[:64]), True,
            "a comment of 64 characters of the whole allowed set", "ssh-ed25519", 256)
        add("ed25519-comment-1", "%s %s x" % (edt, edk), True, "a comment of 1 character", "ssh-ed25519", 256)
        add("rsa-3072", r3072, True, "a real 3072-bit rsa key (the lower bound)", "ssh-rsa", 3072)
        add("rsa-4096", r4096 + "\n", True, "a real 4096-bit rsa key, trailing LF", "ssh-rsa", 4096)
        add("rsa-16384", "ssh-rsa %s test-rsa16384" % b64(rsa_blob(65537, odd_n(16384))), True,
            "n of 16384 bits (the upper bound; a synthetic n)", "ssh-rsa", 16384)
        add("rsa-e-3", "ssh-rsa %s test-e3" % b64(rsa_blob(3, odd_n(3072))), True, "e = 3", "ssh-rsa", 3072)
        add("rsa-4001", "ssh-rsa %s" % b64(rsa_blob(65537, odd_n(4001))), True,
            "n of 4001 bits: its top byte is 0x01, so its mpint has no leading 0x00 (synthetic n)", "ssh-rsa", 4001)

        # --- refused: size, lines -------------------------------------------
        add("empty", "", False, "empty")
        add("lf-only", "\n", False, "only a newline")
        add("two-lines", ed + "\n" + r3072 + "\n", False, "two lines")
        add("two-lf", ed + "\n\n", False, "two trailing LFs (a second, empty line)")
        add("crlf-crlf", ed + "\r\n\r\n", False, "two trailing CRLFs")
        add("cr-only", ed + "\r", False, "a trailing CR without LF")
        add("lf-cr", ed + "\n\r", False, "LF then CR")
        add("leading-lf", "\n" + ed, False, "an empty first line")
        add("cr-inside", "%s\r%s" % (edt, edk), False, "a CR in the line")
        big = "ssh-rsa " + b64(rsa_blob(65537, odd_n(16384)) + s(secrets.token_bytes(5000)))
        assert len(big) > 8193
        add("oversize", big[:8193], False, "8193 bytes")
        add("oversize-lf", big[:8192] + "\n", False, "8192 bytes + LF = 8193 bytes")
        # --- refused: form --------------------------------------------------
        add("leading-space", " " + ed, False, "a space at the start")
        add("trailing-space", ed + " ", False, "a space at the end")
        add("trailing-space-lf", ed + " \n", False, "a space before the LF")
        add("double-space", "%s  %s" % (edt, edk), False, "two spaces between type and key")
        add("tab", "%s\t%s" % (edt, edk), False, "a tab as separator")
        add("type-only", edt, False, "only the type")
        add("type-uppercase", "SSH-ED25519 %s" % edk, False, "the type in capitals")
        add("options-restrict", "restrict " + ed, False, "an options prefix (restrict)")
        add("options-no-pty", "no-pty,no-agent-forwarding " + ed, False, "an options prefix (no-pty,...)")
        add("options-command", 'command="true" ' + ed, False, "an options prefix (command=)")
        add("options-command-space", 'command="echo hi" ' + ed, False, "an options prefix with a quoted space")
        add("key-only", edk, False, "the base64 only, no type")
        # --- refused: comment -----------------------------------------------
        add("comment-65", "%s %s %s" % (edt, edk, "c" * 65), False, "a comment of 65 characters")
        add("comment-space", "%s %s two words" % (edt, edk), False, "a comment with a space")
        add("comment-quote", '%s %s it"s' % (edt, edk), False, "a comment with a double quote")
        add("comment-squote", "%s %s it's" % (edt, edk), False, "a comment with a single quote")
        add("comment-backslash", "%s %s a\\b" % (edt, edk), False, "a comment with a backslash")
        add("comment-slash", "%s %s a/b" % (edt, edk), False, "a comment with a slash")
        add("comment-comma", "%s %s a,b" % (edt, edk), False, "a comment with a comma")
        add("comment-hash", "%s %s a#b" % (edt, edk), False, "a comment with a hash")
        add("comment-non-ascii", "%s %s café" % (edt, edk), False, "a comment with a non-ASCII letter")
        add("comment-nul", "%s %s a\u0000b" % (edt, edk), False, "a comment with a NUL")
        add("comment-del", "%s %s a\u007fb" % (edt, edk), False, "a comment with DEL")
        # --- refused: base64 ------------------------------------------------
        # ed25519's blob is 51 bytes: its base64 has no padding. The 3072-bit rsa blob is 407 bytes: one '='.
        rt = "ssh-rsa"
        assert len(r3072blob) % 3 == 2 and r3072k.endswith("=") and not r3072k.endswith("==")
        add("b64-no-padding", "%s %s" % (rt, r3072k[:-1]), False, "the base64 without its padding")
        # the character before the '=' holds 2 unused bits: set one, the bytes stay the same
        alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
        last = r3072k[-2]
        nc = r3072k[:-2] + alpha[alpha.index(last) ^ 1] + "="
        assert nc != r3072k and base64.b64decode(nc) == r3072blob
        add("b64-noncanonical", "%s %s" % (rt, nc), False, "non-zero unused bits before the padding (decodes to the same bytes)")
        add("b64-padding-group", "%s %s====" % (edt, edk), False, "a group of four '='")
        while True:
            u = b64(ed_blob(secrets.token_bytes(32)))
            if "+" in u or "/" in u:
                break
        add("b64-urlsafe", "%s %s" % (edt, u.replace("+", "-").replace("/", "_")), False, "the URL-safe alphabet")
        add("b64-bad-char", "%s %s" % (edt, edk[:-4] + "*" + edk[-3:]), False, "a '*' in the base64")
        add("b64-inner-padding", "%s %s" % (edt, edk[:8] + "=" + edk[9:]), False, "a '=' inside the base64")
        add("b64-length", "%s %s" % (edt, edk[:-1]), False, "a length that is not a multiple of 4")
        # --- refused: ed25519 blob ------------------------------------------
        add("ed25519-trailing-garbage", "%s %s" % (edt, b64(edblob + b"\0")), False, "one byte after the ed25519 blob")
        add("ed25519-trailing-string", "%s %s" % (edt, b64(edblob + s(b"x"))), False, "a string after the ed25519 blob")
        add("ed25519-31", "%s %s" % (edt, b64(ed_blob(edpk[:31]))), False, "a 31-byte ed25519 key")
        add("ed25519-33", "%s %s" % (edt, b64(ed_blob(edpk + b"\0"))), False, "a 33-byte ed25519 key")
        add("ed25519-truncated", "%s %s" % (edt, b64(edblob[:-1])), False, "the blob cut short by one byte")
        add("ed25519-len-overflow", "%s %s" % (edt, b64(s(b"ssh-ed25519") + struct.pack(">I", 0xffffffff) + edpk)),
            False, "a length field larger than the blob")
        add("type-mismatch-rsa-ed", "ssh-rsa %s" % edk, False, "line says ssh-rsa, blob is ssh-ed25519")
        add("type-mismatch-ed-rsa", "ssh-ed25519 %s" % r3072k, False, "line says ssh-ed25519, blob is ssh-rsa")
        add("blob-type-nul", "%s %s" % (edt, b64(s(b"ssh-ed25519\0") + s(edpk))), False, "the blob's type has a NUL")
        # --- refused: rsa ---------------------------------------------------
        add("rsa-2048", r2048, False, "a real 2048-bit rsa key")
        add("rsa-3071", "ssh-rsa %s" % b64(rsa_blob(65537, odd_n(3071))), False, "n of 3071 bits")
        add("rsa-16385", "ssh-rsa %s" % b64(rsa_blob(65537, odd_n(16385))), False, "n of 16385 bits")
        n3 = odd_n(3072)
        add("rsa-e-even", "ssh-rsa %s" % b64(rsa_blob(65536, n3)), False, "e even")
        add("rsa-e-1", "ssh-rsa %s" % b64(rsa_blob(1, n3)), False, "e = 1")
        add("rsa-e-zero", "ssh-rsa %s" % b64(rsa_blob(s(b""), n3)), False, "e = 0 (empty mpint)")
        add("rsa-e-leading-zero", "ssh-rsa %s" % b64(rsa_blob(s(b"\0\x01\0\x01"), n3)), False, "e with a superfluous 0x00")
        add("rsa-e-zero-byte", "ssh-rsa %s" % b64(rsa_blob(s(b"\0"), n3)), False, "e as the one byte 0x00")
        add("rsa-e-negative", "ssh-rsa %s" % b64(rsa_blob(s(b"\x81"), n3)), False, "e negative (0x81 without 0x00)")
        nb = n3.to_bytes(384, "big")
        add("rsa-n-leading-zero", "ssh-rsa %s" % b64(rsa_blob(65537, s(b"\0" + (nb[0] & 0x7f).to_bytes(1, "big") + nb[1:]))),
            False, "n with a superfluous 0x00 (top bit of the next byte clear)")
        add("rsa-n-two-zeros", "ssh-rsa %s" % b64(rsa_blob(65537, s(b"\0\0" + nb))), False, "n with two leading 0x00")
        add("rsa-n-negative", "ssh-rsa %s" % b64(rsa_blob(65537, s(nb))), False, "n with the top bit set and no 0x00 (negative)")
        add("rsa-n-zero", "ssh-rsa %s" % b64(rsa_blob(65537, s(b""))), False, "n = 0")
        add("rsa-no-n", "ssh-rsa %s" % b64(s(b"ssh-rsa") + mpint(65537)), False, "no n")
        add("rsa-trailing-garbage", "ssh-rsa %s" % b64(r3072blob + b"\0\0\0\0"), False, "an empty string after the rsa blob")
        add("rsa-trailing-byte", "ssh-rsa %s" % b64(r3072blob + b"\x01"), False, "one byte after the rsa blob")
        # --- refused: other types -------------------------------------------
        add("ecdsa-p256", ec, False, "an ecdsa key (ssh-keygen)")
        dss = s(b"ssh-dss") + mpint(odd_n(1024)) + mpint(odd_n(160)) + mpint(odd_n(1023)) + mpint(odd_n(1023))
        add("ssh-dss", "ssh-dss %s test-dss" % b64(dss), False, "a dss key (synthetic)")
        sked = s(b"sk-ssh-ed25519@openssh.com") + s(edpk) + s(b"ssh:")
        add("sk-ed25519", "sk-ssh-ed25519@openssh.com %s test-sk" % b64(sked), False, "an ed25519-sk key")
        skec = s(b"sk-ecdsa-sha2-nistp256@openssh.com") + s(b"nistp256") + s(ecq) + s(b"ssh:")
        add("sk-ecdsa", "sk-ecdsa-sha2-nistp256@openssh.com %s test-sk" % b64(skec), False, "an ecdsa-sk key")
        add("ed25519-cert", cert, False, "an ed25519 certificate")
        add("rsa-sha2-512-type", "rsa-sha2-512 %s" % r3072k, False, "a signature name as the type")
        add("private-key", "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----\n",
            False, "a private key (a fake, not a key)")

        for name, text, valid, why, typ, bits in V:
            v = {"name": name, "input": text, "valid": valid, "why": why}
            lines = g.l(text)
            v["ssh_keygen_l"] = lines
            if valid:
                line = text[:-2] if text.endswith("\r\n") else text[:-1] if text.endswith("\n") else text
                t, k = line.split(" ")[:2]
                blob = base64.b64decode(k)
                want = fp(blob)
                if not lines or len(lines) != 1:
                    sys.exit("%s: ssh-keygen -l did not read the key: %r" % (name, lines))
                kbits, kfp = lines[0].split(" ")[:2]
                if kfp != want or int(kbits) != bits:
                    sys.exit("%s: ssh-keygen says %s %s, the script %s %s" % (name, kbits, kfp, bits, want))
                v.update({"type": typ, "bits": bits, "fingerprint": kfp, "line": line, "file": line + "\n"})
            out.append(v)
    names = [v["name"] for v in out]
    assert len(names) == len(set(names))
    doc = {
        "about": "Test vectors of the optional ssh public key (SSH-KEY.PUB) rule, shared by the page "
                 "(web/site/js/sshkey.js, web/test/unit/sshkey.test.mjs) and the installer's Go tests. Made by "
                 "web/test/unit/sshkey-vectors.py with throwaway keys (ssh-keygen); every key here is a test key. "
                 "valid: the rule accepts the input. For a valid input: type, bits and fingerprint (from "
                 "ssh-keygen -l -E sha256), line (the normalised line) and file (the bytes SSH-KEY.PUB gets: line + LF). "
                 "ssh_keygen_l: what ssh-keygen -l prints for the input as a file (null: ssh-keygen refused it); "
                 "information only, the rule is stricter. why: what the vector tests.",
        "rule": {"max_bytes": 8192, "types": ["ssh-ed25519", "ssh-rsa"], "rsa_min_bits": 3072, "rsa_max_bits": 16384,
                 "comment": "^[A-Za-z0-9._@+=:-]{1,64}$", "file_name": "SSH-KEY.PUB"},
        "vectors": out,
    }
    json.dump(doc, sys.stdout, indent=1, ensure_ascii=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
