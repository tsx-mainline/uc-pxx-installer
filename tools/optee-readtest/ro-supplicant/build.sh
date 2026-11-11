#!/bin/sh
# build.sh -- build the read-only supplicant (static aarch64, no bionic) and the
# host self-test. READ ONLY + FAIL CLOSED: audited below.
#
# Logging build (OPTEE-READTEST-2-2026-11-11.md). Also:
#  - ref/ holds the reviewed sources; built here with the same compiler they must
#    give the reviewed binary 23142586... (else FAIL), so ref/ IS that binary's source;
#  - rpcequiv: the real service_one_rpc() of ref/ and of the logging build, on a
#    fake TEE device, over the same sweep of every FS op and every RPC shape: the
#    replies, return values and SHM contents must be identical (log off and on);
#  - the log of that sweep must hold no file data byte and no WRITE payload byte;
#  - the self-test checks the log lines, the escaping, the cap and the fd checks;
#  - the aarch64 binary's syscalls: no write-type syscall, nothing new except
#    clock_gettime (the log time stamps).
#
# Round 3 (OPTEE-READTEST-3-2026-11-11.md): an FS OPEN of an EXISTING file is served
# whatever its access-mode bits (RDONLY/WRONLY/RDWR); the handle is read-only (no
# write path exists); OPEN+CREAT/APPEND and every WRITE/TRUNC/UNLINK/RENAME/LINK/
# MKDIR/RMDIR stay refused before any backend access. Also:
#  - ref2/ holds the round-2 sources; built here they must give the round-2 binary
#    9200e2f0... (else FAIL);
#  - only fsdispatch.c/.h, rpclog.c and (comments only) supplicant.c differ from ref2/;
#  - rpcequiv runs ref/, ref2/ and the round-3 sources: ref/ = ref2/ on every line;
#    ref2/ -> round 3 differs ONLY on the predicted OPEN-with-access-mode cases
#    (rpcequiv-check.py enumerates them);
#  - the syscall set is ref2/'s exactly.
#
# Round 4 (the LS supplicant of the install image, MAINLINE-RC2-LS-2026-11-11.md;
# OPTEE-READTEST-3-REVIEW MINOR-2): an FS OPEN is served only with flags EXACTLY
# RDONLY (0x1) or RDWR (0x4), the two values of the sealed round-2/3 RPC logs; every
# other OPEN is refused by the gate before any backend access. Here:
#  - ref3/ holds the round-3 sources; built here they must give the round-3 binary
#    afaf1778... (else FAIL);
#  - only fsdispatch.c/.h and (comments only) supplicant.c differ from ref3/;
#  - rpcequiv (-DCLASS4) runs ref3/ and the round-4 sources: every line identical
#    except the round-4 class (an OPEN whose flags are neither exactly RDONLY nor
#    exactly RDWR, without CREAT/APPEND), and every line of that class is the gate
#    refusal (rpcequiv-check4.py);
#  - the syscall set is ref3/'s exactly.
# (The ref/ -> ref2/ -> ref3/ equivalences are the earlier rounds' builds and reviews;
# here ref/ and ref2/ are only rebuilt to their binaries.)
# Run it in a copy of this folder (it writes its outputs here).
set -eu

CROSS="${CROSS:-aarch64-linux-gnu-}"
HOSTCC="${HOSTCC:-cc}"
CFLAGS_X="-std=gnu11 -Os -static -no-pie -fno-pie -Wall -Wextra"
REF_BIN_SHA=231425862700f93cb596d0421690a8ab676fc677e5ab951abed0a8e387610122
REF2_BIN_SHA=9200e2f085a71dee89e832dfe546c8ea14618a2ad4145ae3cc582650edc5f1b6
REF3_BIN_SHA=afaf1778785c52240cfbb5206a4501ffcf8686d35e9040c40cf541a555530d6a
fail() { echo "BUILD/AUDIT FAIL: $*" >&2; exit 1; }

echo "== static aarch64 supplicant (round 4: OPEN served only with flags exactly RDONLY or RDWR) =="
"${CROSS}gcc" $CFLAGS_X -o tee-supplicant-ro supplicant.c fsdispatch.c rkss_ro.c rpclog.c
file tee-supplicant-ro 2>/dev/null || true
readelf -h tee-supplicant-ro | grep -Ei 'class|machine|type'
sha256sum tee-supplicant-ro

echo
echo "== ref/: the reviewed sources -> the reviewed binary =="
(cd ref && sha256sum -c - <<'EOF'
4051c87d6da989b66fb79d04948afd69722c8218199e3f4f18cd356e7b65caaf  supplicant.c
5433d367514e5b10cfe249e00c2e2ea34272d47301bd0be785600f586737619a  fsdispatch.c
389236d2f6af6be74c10fca8dd41c58251789e39f684814f3572c5d98f9c1d8a  fsdispatch.h
52c39608b2d5ad8d8ea0cb1894e17c03b2263a89e9a02fe8abaab56175536b0e  rkss_ro.c
a9a481d9cdac299d894812070ac1e5a4e9feb14c69521187809d4ae8b48c5b51  rkss_ro.h
EOF
) || fail "ref/ is not the reviewed source"
(cd ref && "${CROSS}gcc" $CFLAGS_X -o ../tee-supplicant-ro.ref supplicant.c fsdispatch.c rkss_ro.c)
h=$(sha256sum tee-supplicant-ro.ref | cut -c1-64)
echo "   ref/ built: $h"
[ "$h" = "$REF_BIN_SHA" ] || fail "ref/ does not build to the reviewed binary $REF_BIN_SHA (another compiler?)"
echo "   = the reviewed tee-supplicant-ro $REF_BIN_SHA"

echo
echo "== ref2/: the round-2 sources -> the round-2 binary =="
(cd ref2 && sha256sum -c - <<'EOF'
9d707aa09e1dde1184f8063fc693b59a832f1df452f9f45a1d6f209ced4fc219  supplicant.c
6ee4396a281c4fcf533cfd0fff953c06caf20545e12b085c0deceb121eaf6b99  fsdispatch.c
8a055faffb062fed1fc5b77d933e83c7111a219daf5d58a6aee2ad00b954e076  fsdispatch.h
d4f291d98d6755f7e901c7af61834424fbcf236cc0d6e89aaaece4cd2d1d87b8  rkss_ro.c
23d5af3b445934e4803630b5fdb6db50525d35ec79ffb094a24484318043bab0  rkss_ro.h
c2ee10302d248714513335ee49867788ec946d2f6a5e5e2cd90e03e3c6bf04ee  rpclog.c
17c659eef49f280176edb8702516475e53579c9b1c5c00e07c2882864e61a165  rpclog.h
EOF
) || fail "ref2/ is not the round-2 source"
(cd ref2 && "${CROSS}gcc" $CFLAGS_X -o ../tee-supplicant-ro.ref2 supplicant.c fsdispatch.c rkss_ro.c rpclog.c)
h=$(sha256sum tee-supplicant-ro.ref2 | cut -c1-64)
echo "   ref2/ built: $h"
[ "$h" = "$REF2_BIN_SHA" ] || fail "ref2/ does not build to the round-2 binary $REF2_BIN_SHA (another compiler?)"
echo "   = the round-2 tee-supplicant-ro $REF2_BIN_SHA"

echo
echo "== ref3/: the round-3 sources -> the round-3 binary =="
(cd ref3 && sha256sum -c - <<'EOF2'
737dc6853f096410d8cceaf5790442d2790b8e0cd54bae29e926703290f2377d  supplicant.c
2d8b45a9b734902d21aaf0c4e5020dce40968ec03e63a1687383677f2e93ff8a  fsdispatch.c
196af993ef0634aaebd65968809bff2e281e44c4a903f2970abfb132b8418443  fsdispatch.h
d4f291d98d6755f7e901c7af61834424fbcf236cc0d6e89aaaece4cd2d1d87b8  rkss_ro.c
23d5af3b445934e4803630b5fdb6db50525d35ec79ffb094a24484318043bab0  rkss_ro.h
580946d092f0c84797a938491fa7c414970b614f34375c24681885d4790d248f  rpclog.c
17c659eef49f280176edb8702516475e53579c9b1c5c00e07c2882864e61a165  rpclog.h
EOF2
) || fail "ref3/ is not the round-3 source"
(cd ref3 && "${CROSS}gcc" $CFLAGS_X -o ../tee-supplicant-ro.ref3 supplicant.c fsdispatch.c rkss_ro.c rpclog.c)
h=$(sha256sum tee-supplicant-ro.ref3 | cut -c1-64)
echo "   ref3/ built: $h"
[ "$h" = "$REF3_BIN_SHA" ] || fail "ref3/ does not build to the round-3 binary $REF3_BIN_SHA (another compiler?)"
echo "   = the round-3 tee-supplicant-ro $REF3_BIN_SHA"

echo
echo "== the source change vs ref3/ (round 4) =="
for f in rkss_ro.c rkss_ro.h rpclog.c rpclog.h; do
	cmp -s "ref3/$f" "$f" || fail "$f differs from ref3/ (round 4 changes only fsdispatch.c/.h and supplicant.c comments)"
done
echo "   rkss_ro.c, rkss_ro.h, rpclog.c, rpclog.h: identical to ref3/"
nocomm() { "${CROSS}gcc" -fpreprocessed -dD -E -P "$1" | tr -s ' \t\n' ' '; }
[ "$(nocomm ref3/supplicant.c)" = "$(nocomm supplicant.c)" ] || fail "supplicant.c differs from ref3/ in code (comments only are allowed)"
echo "   supplicant.c: comments only (identical without comments)"
for f in fsdispatch.h fsdispatch.c; do
	echo "   -- ref3/$f -> $f, code without comments (one statement per line), changed lines:"
	nocomm "ref3/$f" | tr ';{}' '\n\n\n' > nocomm.ref3; nocomm "$f" | tr ';{}' '\n\n\n' > nocomm.new
	diff nocomm.ref3 nocomm.new | grep '^[<>]' | sed 's/^/      /' || true
done
rm -f nocomm.ref3 nocomm.new

echo
echo "== host self-test (native) =="
"$HOSTCC" -std=gnu11 -O1 -Wall -Wextra -U_FORTIFY_SOURCE \
	-o rkss_selftest selftest.c fsdispatch.c rkss_ro.c rpclog.c
./rkss_selftest

echo
echo "== rpcequiv (-DCLASS4): round 4 = ref3/ except the round-4 OPEN class, which is the gate refusal (host) =="
"$HOSTCC" -std=gnu11 -O1 -Wall -Wextra -U_FORTIFY_SOURCE -DNEW_BUILD -DCLASS4 -DSUPP_C='"ref3/supplicant.c"' \
	-o rpcequiv-ref3 rpcequiv.c ref3/fsdispatch.c ref3/rkss_ro.c ref3/rpclog.c
"$HOSTCC" -std=gnu11 -O1 -Wall -Wextra -U_FORTIFY_SOURCE -DNEW_BUILD -DCLASS4 -DSUPP_C='"supplicant.c"' \
	-o rpcequiv-new rpcequiv.c fsdispatch.c rkss_ro.c rpclog.c
./rpcequiv-ref3 > rpcequiv-ref3.out 2> /dev/null
./rpcequiv-new > rpcequiv-new.out 2> rpcequiv-new.stderr
./rpcequiv-new --log rpcequiv-new.log > rpcequiv-newlog.out 2> /dev/null
tail -n 1 rpcequiv-ref3.out
cmp rpcequiv-new.out rpcequiv-newlog.out || fail "rpcequiv: round 4 with the log fd differs from round 4 (log on stderr)"
echo "   round 4 (log on stderr) = round 4 (log fd): identical on every line"
if cmp -s rpcequiv-ref3.out rpcequiv-new.out; then fail "rpcequiv: round 4 = ref3/ on every line (the change did nothing?)"; fi
python3 -I rpcequiv-check4.py rpcequiv-ref3.out rpcequiv-new.out || fail "rpcequiv: round 4 vs ref3/ is not exactly the predicted change"
sha256sum rpcequiv-ref3.out rpcequiv-new.out
grep -q 'REFUSED-WRITE-GATE: OPEN with flags other than exactly RDONLY (0x1) or RDWR (0x4)' rpcequiv-new.log || fail "rpcequiv log: no round-4 OPEN refusal"
if grep -q 'OPEN with CREAT/APPEND: a store mutation' rpcequiv-new.log; then fail "rpcequiv log: the round-3 gate text"; fi
if grep -q 'access mode WRONLY asked: the handle is read-only\|access mode none asked: the handle is read-only' rpcequiv-new.log; then
	fail "rpcequiv log: a WRONLY or no-mode OPEN was served"; fi
for m in 'flags=0x1(RDONLY) .* -> SERVED' 'flags=0x4(RDWR) .* -> SERVED'; do
	grep -q -- "$m" rpcequiv-new.log || fail "rpcequiv log: no line '$m'"
done
echo "   round-4 log: OPEN refusals (round-4 text): $(grep -c 'OPEN with flags other than exactly' rpcequiv-new.log); served OPENs: RDONLY $(grep -c 'flags=0x1(RDONLY) .* -> SERVED' rpcequiv-new.log), RDWR $(grep -c 'flags=0x4(RDWR) .* -> SERVED' rpcequiv-new.log), any other $(grep 'FS OPEN(op 1)' rpcequiv-new.log | grep -- '-> SERVED' | grep -vc 'flags=0x1(RDONLY) \|flags=0x4(RDWR) ' || true)"
[ "$(grep 'FS OPEN(op 1)' rpcequiv-new.log | grep -- '-> SERVED' | grep -vc 'flags=0x1(RDONLY) \|flags=0x4(RDWR) ' || true)" = 0 ] ||
	fail "rpcequiv log: an OPEN with another flag value was served"
for f in rpcequiv-new.log rpcequiv-new.stderr; do
	if grep -q 'DATA-MARKER\|WRITE-MARKER\|444154412d\|57524954452d' "$f"; then
		fail "$f holds a file data byte or a WRITE payload byte"
	fi
	echo "   $f: $(wc -l < "$f") lines, $(wc -c < "$f") bytes, no data marker"
done
[ "$(wc -c < rpcequiv-new.stderr)" -le $((1048576 + 100)) ] || fail "the stderr sink passed its cap"
echo "   stderr sink bounded: $(wc -c < rpcequiv-new.stderr) bytes <= 1 MiB + the cap line"
for d in SERVED REFUSED-WRITE-GATE REFUSED-UNSUPPORTED NOT-FOUND DENIED ERROR 'RKSS FORMAT CHECK FAILED' \
	'NOT SERVED: RPMB' 'NOT SERVED: only TEE_RPC_FS' 'TEE_GET_FD_FOR_RPC_SHM failed' 'mmap of the SHM failed' \
	'smaller than the FS header' 'reply write FAILED'; do
	c=$(grep -c -- "$d" rpcequiv-new.log || true)
	printf '   %-36s %s lines\n' "$d" "$c"
	[ "$c" -gt 0 ] || fail "rpcequiv log: no '$d' line"
done
echo "   (rpcequiv-new.log: checked above, now deleted: it has time stamps and is large)"
rm -f rpcequiv-new.log

if command -v qemu-aarch64 > /dev/null 2>&1; then
	echo
	echo "== rpcequiv (-DCLASS4), aarch64 static (the target's code generation), under qemu-aarch64 =="
	"${CROSS}gcc" -std=gnu11 -Os -static -Wall -Wextra -DNEW_BUILD -DCLASS4 -DSUPP_C='"ref3/supplicant.c"' \
		-o rpcequiv-ref3.a64 rpcequiv.c ref3/fsdispatch.c ref3/rkss_ro.c ref3/rpclog.c
	"${CROSS}gcc" -std=gnu11 -Os -static -Wall -Wextra -DNEW_BUILD -DCLASS4 -DSUPP_C='"supplicant.c"' \
		-o rpcequiv-new.a64 rpcequiv.c fsdispatch.c rkss_ro.c rpclog.c
	qemu-aarch64 ./rpcequiv-ref3.a64 > rpcequiv-ref3.a64.out 2> /dev/null
	qemu-aarch64 ./rpcequiv-new.a64 --log rpcequiv-new.a64.log > rpcequiv-new.a64.out 2> /dev/null
	python3 -I rpcequiv-check4.py rpcequiv-ref3.a64.out rpcequiv-new.a64.out ||
		fail "rpcequiv (aarch64): round 4 vs ref3/ is not exactly the predicted change"
	if cmp rpcequiv-new.out rpcequiv-new.a64.out; then echo "   aarch64 round-4 output = host round-4 output"
	else fail "rpcequiv: the aarch64 round-4 output differs from the host's"; fi
	cmp rpcequiv-ref3.out rpcequiv-ref3.a64.out || fail "rpcequiv: the aarch64 ref3/ output differs from the host's"
	if grep -q 'DATA-MARKER\|WRITE-MARKER' rpcequiv-new.a64.log; then fail "aarch64 log holds a data marker"; fi
	rm -f rpcequiv-new.a64.log
	sha256sum rpcequiv-ref3.a64.out rpcequiv-new.a64.out
else
	echo "== rpcequiv aarch64: SKIPPED (no qemu-aarch64)"
fi

echo
echo "== read-only / fail-closed source audit =="
echo "-- RKSS core + dispatcher: no write/create/RPMB tokens at all (must be none)"
if grep -nE '\b(pwrite|writev|O_WRONLY|O_RDWR|O_CREAT|blk_dwrite|rpmb)\b|write[[:space:]]*\(' \
	rkss_ro.c fsdispatch.c; then
	fail "a write/RPMB token in the RKSS/dispatch core"
fi
echo "   (clean)"
echo "-- supplicant.c: the ONLY write() is the RPC reply to the TEE char device:"
grep -nE 'write[[:space:]]*\(' supplicant.c | sed 's/^/   /'
[ "$(grep -cE 'write[[:space:]]*\(' supplicant.c)" = 1 ] && grep -qE 'write\(devfd, &inv, ' supplicant.c ||
	fail "supplicant.c: a write() other than the one RPC reply"
if grep -nE '\b(pwrite|writev|blk_dwrite|rpmb)\b|O_WRONLY|O_CREAT|O_TRUNC|O_APPEND' supplicant.c; then
	fail "a write/create/RPMB token in supplicant.c"
fi
echo "-- the backend is opened O_RDONLY only:"
grep -nE 'open[^;]*O_RDONLY' supplicant.c | sed 's/^/   /'
echo "-- the ONLY block-device opens in supplicant.c (GPT read + security, both O_RDONLY):"
grep -nE 'open\(' supplicant.c | grep -v opteearmtz00 | sed 's/^/   /'
echo "-- rpclog.c: opens nothing, no ioctl/mmap/pwrite/RPMB, ONE write() (sink_emit: fd 2 or the checked log fd):"
if grep -nE '\b(open|openat|creat|fopen|freopen|ioctl|mmap|pwrite|pwritev|writev|dup|dup2|dup3|rpmb|pread)[[:space:]]*\(|\bO_(WRONLY|RDWR|CREAT|TRUNC|APPEND)\b' rpclog.c; then
	fail "rpclog.c: an open/ioctl/mmap/pwrite/dup/RPMB/pread token"
fi
grep -nE 'write[[:space:]]*\(' rpclog.c | sed 's/^/   /'
[ "$(grep -cE '[^_]write[[:space:]]*\(' rpclog.c)" = 1 ] || fail "rpclog.c: more than one write()"
echo "-- rpclog.c: the only payload accesses (a name, only for the name ops of rpclog_fsop_names):"
grep -nE 'payload' rpclog.c | grep -vE '^\s*[0-9]+:\s*(\*|//)' | sed 's/^/   /'
echo "-- rpclog.c: the log fd is taken only when fstat says regular file or FIFO, fd >= 3:"
grep -nE 'S_ISREG|S_ISFIFO|file_fd < 3' rpclog.c | sed 's/^/   /'

if command -v "${CROSS}objdump" > /dev/null 2>&1; then
	echo
	echo "== aarch64 binaries: syscall numbers (immediates into w8/x8 before svc) =="
	sc() { "${CROSS}objdump" -d "$1" | awk '
		/mov[ \t]+[wx]8, #0x[0-9a-f]+/ { match($0, /#0x[0-9a-f]+/); last = substr($0, RSTART + 1, RLENGTH - 1); have = 1; next }
		/svc[ \t]+#0x0/ { if (have) print last; have = 0; next }' |
		while read -r x; do printf '%d\n' "$x"; done | sort -n | uniq | tr '\n' ' '; }
	SNEW=$(sc tee-supplicant-ro); SREF=$(sc tee-supplicant-ro.ref); SREF3=$(sc tee-supplicant-ro.ref3)
	echo "   ref/: $SREF"
	echo "   ref3: $SREF3"
	echo "   new : $SNEW"
	[ "$SNEW" = "$SREF3" ] || fail "the round-4 syscall set differs from ref3/'s (round 3)"
	echo "   round 4 = ref3/ (round 3): the same syscall set"
	# write-type: truncate 45, ftruncate 46, fallocate 47, mkdirat 34, symlinkat 36, linkat 37, renameat 38,
	# unlinkat 35, sync 81, fsync 82, fdatasync 83, sync_file_range 84, pwrite64 68, pwritev 70, syncfs 267,
	# renameat2 276, pwritev2 287, mount 40
	for n in 34 35 36 37 38 40 45 46 47 68 70 81 82 83 84 267 276 287; do
		case " $SNEW " in *" $n "*) fail "the logging binary has write-type syscall $n";; esac
	done
	extra=
	for n in $SNEW; do case " $SREF " in *" $n "*) ;; *) extra="$extra $n";; esac; done
	echo "   new syscalls vs ref/:${extra:- none}"
	for n in $extra; do [ "$n" = 113 ] || fail "a new syscall $n (only 113 clock_gettime is expected)"; done
	echo "   no write-type syscall; new only:${extra:- none} (113 = clock_gettime, the log time stamp)"
fi
echo
echo "built: tee-supplicant-ro (aarch64), rkss_selftest, rpcequiv (host). ALL PASS above = read-only proven;"
echo "served/refused identical to round 3 ($REF3_BIN_SHA) except an OPEN whose flags are not exactly"
echo "RDONLY or RDWR: now refused at the gate, before any backend access."
