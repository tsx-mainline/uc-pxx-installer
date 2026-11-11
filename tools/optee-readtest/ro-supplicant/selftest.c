// SPDX-License-Identifier: GPL-2.0-or-later
/*
 * selftest.c -- offline proof of the read-only supplicant's FS dispatcher.
 * Builds a synthetic RKSS v1 partition image (we have no access to the unit's
 * real secret bytes), opens it O_RDONLY exactly as the real supplicant opens the
 * security partition, and drives fs_dispatch() for every op. Asserts:
 *   - READ/OPEN(no-create)/ACCESS/SEEK/CLOSE work and return the right data,
 *   - every write-type op (WRITE, TRUNC, UNLINK, RENAME, LINK, MKDIR, RMDIR,
 *     OPEN+O_CREAT, OPEN+O_APPEND) is REFUSED,
 *   - round 4 (the LS supplicant; round 3 served every access mode): an OPEN of
 *     an existing file is served only with flags exactly RDONLY (0x1) or RDWR
 *     (0x4); every other flag value (WRONLY, combinations, no mode bit, CREAT,
 *     APPEND, 0x10, 0x40, 0x80, high bits) is refused by the gate before any
 *     backend access, for an existing and a missing name, on a live and a dead
 *     backend; a WRITE/TRUNC on a served handle is refused before any backend
 *     access; OPEN RDONLY/RDWR of a missing file "not found"; a lock_state-like
 *     sequence (ACCESS meta.0 missing, ACCESS meta.1,
 *     OPEN RDWR meta.0/meta.1, READs, OPEN RDWR of a block file, CLOSE) gives the
 *     expected bytes and leaves the store unchanged (round3_tests),
 *   - the image's sha-equivalent (byte compare of a copy) is unchanged, i.e. the
 *     dispatcher issued no write at all (reinforced by O_RDONLY: a write would
 *     EBADF anyway).
 * Logging build (log_tests): every case also through fs_dispatch_logged() with
 * the same result as fs_dispatch(); the log lines (op, flags, name, decision,
 * the store's diagnosis, the FORMAT CHECK line), no file data byte and no WRITE
 * payload byte in the log, names escaped (one line per record), the cap (4 KiB
 * test cap, both sinks), the log-fd checks, errno kept.
 * Exit 0 = all pass.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <fcntl.h>
#include <unistd.h>
#include <errno.h>
#include "rkss_ro.h"
#include "fsdispatch.h"
#include "rpclog.h"

#define IMG_SECT (RKSS_DATA_SECTION_COUNT + 256)   /* ptable+flags+data+backup */
#define TESTNAME "testobj"
#define TESTIDX  130
#define TESTSIZE 180

static int fails;
#define CHECK(c, msg) do { if (!(c)) { printf("FAIL: %s\n", msg); fails++; } \
			   else printf("ok  : %s\n", msg); } while (0)

static void put_verif(uint8_t *sec)
{
	struct rkss_file_verification v = { RKSS_VERSION_V1, RKSS_CHECK_STR };
	memcpy(sec + RKSS_SECTION_LEN - sizeof(v), &v, sizeof(v));
}

static char *build_image(void)
{
	static char path[] = "/tmp/rkss_selftest_XXXXXX";
	strcpy(path, "/tmp/rkss_selftest_XXXXXX");   /* round 3: called twice (main, round3_tests) */
	int fd = mkstemp(path);
	if (fd < 0) { perror("mkstemp"); exit(2); }
	uint8_t *img = calloc(IMG_SECT, RKSS_SECTION_LEN);

	/* 128 partition-table sections, each with a valid verification footer */
	for (int i = 0; i < RKSS_PARTITION_TABLE_COUNT; i++)
		put_verif(img + (size_t)i * RKSS_SECTION_LEN);

	/* one file "testobj" in table section 0, slot 0 */
	rkss_file_info *f = (rkss_file_info *)img;
	f->used = 1; strcpy(f->name, TESTNAME);
	f->index = TESTIDX; f->size = TESTSIZE; f->flags = RK_FS_R | RK_FS_W;

	/* usedflags section [128]: every nibble = 1 */
	memset(img + (size_t)RKSS_USED_FLAGS_INDEX * RKSS_SECTION_LEN, 0x11, RKSS_SECTION_LEN);

	/* data at section TESTIDX: a recognizable pattern */
	uint8_t *data = img + (size_t)TESTIDX * RKSS_SECTION_LEN;
	for (int i = 0; i < TESTSIZE; i++) data[i] = (uint8_t)(0xA0 + (i & 0x1f));

	if (write(fd, img, (size_t)IMG_SECT * RKSS_SECTION_LEN) < 0) { perror("write"); exit(2); }
	free(img); close(fd);
	return path;
}

static unsigned long fnv1a(const char *path)   /* cheap content fingerprint */
{
	FILE *f = fopen(path, "rb"); if (!f) return 0;
	unsigned long h = 1469598103934665603UL; int c;
	while ((c = fgetc(f)) != EOF) { h ^= (unsigned char)c; h *= 1099511628211UL; }
	fclose(f); return h;
}

/* drive one fsrpc through a fixed-size SHM-like scratch */
static void mk(struct tee_fs_rpc *r, int op, int flags, int fd, uint32_t len)
{ memset(r, 0, sizeof(*r)); r->op = op; r->flags = flags; r->fd = fd; r->len = len; r->res = -999; }

/* ===================== logging build: the RPC log ===================== */
#define MARK_DATA  "DATA-MARKER-must-never-be-logged-"   /* the file's data */
#define MARK_WRITE "WRITE-MARKER-must-never-be-logged-"  /* a WRITE's payload */

/* variant 0: testobj + markobj (ASCII marker data); 1: section 5 footer version 0;
 * 2: all zero (an unformatted store) */
static char *build_image2(int variant)
{
	static char path[3][40];
	snprintf(path[variant], sizeof(path[variant]), "/tmp/rkss_logtest%d_XXXXXX", variant);
	int fd = mkstemp(path[variant]);
	if (fd < 0) { perror("mkstemp"); exit(2); }
	uint8_t *img = calloc(IMG_SECT, RKSS_SECTION_LEN);
	if (variant != 2) {
		for (int i = 0; i < RKSS_PARTITION_TABLE_COUNT; i++)
			put_verif(img + (size_t)i * RKSS_SECTION_LEN);
		rkss_file_info *f = (rkss_file_info *)img;
		f[0].used = 1; strcpy(f[0].name, TESTNAME);
		f[0].index = TESTIDX; f[0].size = TESTSIZE; f[0].flags = RK_FS_R | RK_FS_W;
		f[1].used = 1; strcpy(f[1].name, "markobj");
		f[1].index = 140; f[1].size = 300; f[1].flags = RK_FS_R;
		uint8_t *d = img + (size_t)140 * RKSS_SECTION_LEN;
		for (size_t o = 0; o + strlen(MARK_DATA) <= 300; o += strlen(MARK_DATA))
			memcpy(d + o, MARK_DATA, strlen(MARK_DATA));
	}
	if (variant == 1) {
		struct rkss_file_verification v = { 0, RKSS_CHECK_STR };
		memcpy(img + 5 * RKSS_SECTION_LEN + RKSS_SECTION_LEN - sizeof(v), &v, sizeof(v));
	}
	if (write(fd, img, (size_t)IMG_SECT * RKSS_SECTION_LEN) < 0) { perror("write"); exit(2); }
	free(img); close(fd);
	return path[variant];
}

static char *slurp(int fd, size_t *len)
{
	off_t sz = lseek(fd, 0, SEEK_END);
	char *b = calloc(1, (size_t)sz + 1);
	if (pread(fd, b, (size_t)sz, 0) != sz) { perror("pread"); exit(2); }
	*len = (size_t)sz;
	return b;
}

static size_t count_str(const char *hay, const char *needle)
{
	size_t n = 0;
	for (const char *p = hay; (p = strstr(p, needle)); p++) n++;
	return n;
}

/* one request through BOTH fs_dispatch (the reviewed path) and fs_dispatch_logged,
 * each on its own copy: same rc, same header, same payload afterwards */
static int lines_expected;
static void both(const struct rkss_ro *s, const struct tee_fs_rpc *in, const void *pay, size_t cap,
		 const char *what)
{
	struct tee_fs_rpc a = *in, b = *in;
	uint8_t *pa = calloc(1, cap + 1), *pb = calloc(1, cap + 1);
	memcpy(pa, pay, cap); memcpy(pb, pay, cap);
	int ra = fs_dispatch(s, &a, pa, cap);
	int rb = fs_dispatch_logged(7, s, &b, pb, cap);
	lines_expected += 1 + (rkss_ro_diag.last == RKSS_E_PTABLE_FORMAT);
	char msg[160];
	snprintf(msg, sizeof(msg), "logged == unlogged (rc, header, payload): %s", what);
	CHECK(ra == rb && !memcmp(&a, &b, sizeof(a)) && !memcmp(pa, pb, cap), msg);
	free(pa); free(pb);
}

static void log_tests(void)
{
	printf("\n-- logging build: the RPC log --\n");
	char *img = build_image2(0);
	unsigned long before = fnv1a(img);
	struct rkss_ro s = { .fd = open(img, O_RDONLY), .base = 0, .nsect = IMG_SECT };
	CHECK(s.fd >= 0, "open the log-test image O_RDONLY");

	/* sinks: the file (mkstemp) and stderr (redirected to a second temp file) */
	char lp[] = "/tmp/rkss_rpclog_XXXXXX", ep[] = "/tmp/rkss_stderr_XXXXXX";
	int lfd = mkstemp(lp), efd = mkstemp(ep), saved2 = dup(2);
	unlink(lp); unlink(ep);
	dup2(efd, 2);
	lines_expected = 1;                      /* the rpclog_init line */
	CHECK(rpclog_init(lfd, RPCLOG_CAP_DEFAULT) == 1, "rpclog_init: a regular file fd is taken");

	struct tee_fs_rpc r;
	uint8_t pay[4096];
	memset(pay, 0, sizeof(pay));

	mk(&r, TEE_FS_ACCESS, TEE_FS_R_OK, 0, 0); strcpy((char *)pay, "markobj");
	both(&s, &r, pay, 64, "ACCESS markobj R_OK");
	mk(&r, TEE_FS_ACCESS, TEE_FS_W_OK, 0, 0);
	both(&s, &r, pay, 64, "ACCESS markobj W_OK (denied)");
	mk(&r, TEE_FS_OPEN, TEE_FS_O_RDONLY, 0, 0);
	both(&s, &r, pay, 64, "OPEN markobj RDONLY");
	/* the fd of markobj is 1 (section 0, slot 1) */
	mk(&r, TEE_FS_READ, 0, 1, 300); memset(pay, 0, sizeof(pay));
	both(&s, &r, pay, 300, "READ markobj 300 (the data holds the marker)");
	mk(&r, TEE_FS_READ, 0, 1, 64);
	both(&s, &r, pay, 64, "READ markobj 64");
	mk(&r, TEE_FS_SEEK, TEE_FS_SEEK_SET, 1, 0); r.arg = 5;
	both(&s, &r, pay, 16, "SEEK SET 5");
	mk(&r, TEE_FS_SEEK, TEE_FS_SEEK_END, 1, 0); r.arg = -1;
	both(&s, &r, pay, 16, "SEEK END -1");
	mk(&r, TEE_FS_SEEK, 0x40, 1, 0);
	both(&s, &r, pay, 16, "SEEK unknown whence");
	mk(&r, TEE_FS_CLOSE, 0, 1, 0);
	both(&s, &r, pay, 16, "CLOSE");
	mk(&r, TEE_FS_OPEN, TEE_FS_O_RDONLY, 0, 0); strcpy((char *)pay, "nope");
	both(&s, &r, pay, 64, "OPEN nope (not found)");
	mk(&r, TEE_FS_OPEN, TEE_FS_O_RDWR, 0, 0); strcpy((char *)pay, "markobj");
	both(&s, &r, pay, 64, "OPEN markobj RDWR (round 3: served read-only)");
	mk(&r, TEE_FS_OPEN, TEE_FS_O_RDONLY | TEE_FS_O_CREAT, 0, 0);
	both(&s, &r, pay, 64, "OPEN markobj RDONLY|CREAT (write gate)");
	mk(&r, TEE_FS_WRITE, 0, 1, 40); memset(pay, 0, sizeof(pay)); strcpy((char *)pay, MARK_WRITE);
	both(&s, &r, pay, 64, "WRITE with a marker payload (write gate)");
	mk(&r, TEE_FS_TRUNC, 0, 1, 0);
	both(&s, &r, pay, 64, "TRUNC (write gate)");
	memset(pay, 0, sizeof(pay)); memcpy(pay, "old/name\0new/name", 18);
	mk(&r, TEE_FS_RENAME, 0, 0, 0);
	both(&s, &r, pay, 64, "RENAME old/name new/name (write gate)");
	mk(&r, TEE_FS_OPENDIR, 0, 0, 0); strcpy((char *)pay, "dir");
	both(&s, &r, pay, 64, "OPENDIR (unsupported)");
	mk(&r, 99, 0, 0, 0);
	both(&s, &r, pay, 64, "unknown op 99 (unsupported)");
	memset(pay, 0, sizeof(pay)); strcpy((char *)pay, "evil\nname\x01\"q\\");
	mk(&r, TEE_FS_OPEN, TEE_FS_O_RDONLY, 0, 0);
	both(&s, &r, pay, 64, "OPEN a name with a newline/control/quote (escaped)");
	memset(pay, 'n', 300); pay[300] = 0;
	mk(&r, TEE_FS_OPEN, TEE_FS_O_RDONLY, 0, 0);
	both(&s, &r, pay, 400, "OPEN a 300-byte name (RKSS name limit, log shows 256)");
	memset(pay, 'u', 64);
	mk(&r, TEE_FS_ACCESS, 0, 0, 0);
	both(&s, &r, pay, 64, "ACCESS an unterminated name");
	mk(&r, TEE_FS_READ, 0, 4 * 128, 16);
	both(&s, &r, pay, 16, "READ an fd outside the table");
	mk(&r, TEE_FS_READ, 0, 2, 16);
	both(&s, &r, pay, 16, "READ an unused entry");
	mk(&r, TEE_FS_READ, 0, 0, 100);
	both(&s, &r, pay, 50, "READ len > payload cap");

	/* the format check: one bad footer, and an unformatted (all-zero) store */
	char *bad = build_image2(1), *zero = build_image2(2);
	struct rkss_ro sb = { .fd = open(bad, O_RDONLY), .base = 0, .nsect = IMG_SECT };
	struct rkss_ro sz = { .fd = open(zero, O_RDONLY), .base = 0, .nsect = IMG_SECT };
	strcpy((char *)pay, TESTNAME);
	mk(&r, TEE_FS_OPEN, TEE_FS_O_RDONLY, 0, 0);
	both(&sb, &r, pay, 64, "OPEN on a store with one bad footer");
	mk(&r, TEE_FS_ACCESS, TEE_FS_R_OK, 0, 0);
	both(&sz, &r, pay, 64, "ACCESS on an unformatted store");
	struct rkss_ro sshort = { .fd = s.fd, .base = 0, .nsect = 100 };
	mk(&r, TEE_FS_OPEN, TEE_FS_O_RDONLY, 0, 0);
	both(&sshort, &r, pay, 64, "OPEN with the table outside a 100-section partition");

	int e = errno = 4321;
	rpclog_line("errno test");
	lines_expected++;
	CHECK(errno == e, "rpclog_line keeps errno");
	errno = 1234;
	mk(&r, TEE_FS_OPEN, TEE_FS_O_RDONLY, 0, 0);
	fs_dispatch_logged(8, &s, &r, pay, 64);
	lines_expected++;
	CHECK(errno == 1234, "fs_dispatch_logged keeps errno (for a successful lookup)");

	fflush(stderr);
	size_t ll, el;
	char *log = slurp(lfd, &ll), *err = slurp(efd, &el);
	CHECK(ll > 0 && ll == el && !memcmp(log, err, ll), "the file sink and stderr hold the same lines");
	size_t nl = count_str(log, "\n");
	char msg[160];
	snprintf(msg, sizeof(msg), "one line per record, no raw newline from a name (%zu lines, want %d)", nl,
		 lines_expected);
	CHECK(nl == (size_t)lines_expected, msg);
	CHECK(!strstr(log, "DATA-MARKER") && !strstr(log, "444154412d4d41524b4552") &&
	      !strstr(log, "44 41 54 41"), "no file data byte in the log (marker, hex)");
	CHECK(!strstr(log, "WRITE-MARKER"), "no WRITE payload byte in the log");
	CHECK(strstr(log, "FS ACCESS(op 14) flags=0x1(R_OK)") && strstr(log, "name=\"markobj\" -> SERVED"),
	      "log: ACCESS name, flags, SERVED");
	CHECK(strstr(log, "FS ACCESS(op 14) flags=0x2(W_OK) arg=0 fd=0 len=0 cap=64 name=\"markobj\" -> DENIED"),
	      "log: ACCESS W_OK -> DENIED");
	CHECK(strstr(log, "FS OPEN(op 1) flags=0x1(RDONLY) arg=0 fd=0 len=0 cap=64 name=\"markobj\" -> SERVED: opened read-only (any access mode; the handle never writes); res=1 rc=0; file: entry 1 index 140 size 300 flags 0x1; access mode RDONLY asked: the handle is read-only (a WRITE/TRUNC on it is refused)"),
	      "log: OPEN RDONLY -> SERVED with the entry and the mode");
	CHECK(strstr(log, "FS READ(op 3) flags=0x0(-) arg=0 fd=1 len=300 cap=300 -> SERVED") &&
	      strstr(log, "data reply 300 bytes (1 sections read; bytes not logged)"), "log: READ length, no data");
	CHECK(strstr(log, "name=\"nope\" -> NOT-FOUND") && strstr(log, "(2 used entries in the table)"),
	      "log: OPEN missing -> NOT-FOUND, used-entry count");
	CHECK(strstr(log, "FS OPEN(op 1) flags=0x4(RDWR) arg=0 fd=0 len=0 cap=64 name=\"markobj\" -> SERVED: opened read-only (any access mode; the handle never writes); res=1 rc=0; file: entry 1 index 140 size 300 flags 0x1; access mode RDWR asked: the handle is read-only (a WRITE/TRUNC on it is refused)"),
	      "log: OPEN RDWR -> SERVED, the mode named, the handle read-only (round 3)");
	CHECK(strstr(log, "flags=0x9(RDONLY|CREAT) arg=0 fd=0 len=0 cap=64 name=\"markobj\" -> REFUSED-WRITE-GATE: OPEN with flags other than exactly RDONLY (0x1) or RDWR (0x4); res=-1 rc=-1; access mode RDONLY asked"),
	      "log: OPEN RDONLY|CREAT -> REFUSED-WRITE-GATE, the mode named");
	CHECK(!strstr(log, "CREAT/WRONLY/RDWR/APPEND"), "log: the round-2 gate text is gone");
	CHECK(strstr(log, "flags=0x9(RDONLY|CREAT)"), "log: OPEN flags decoded");
	CHECK(strstr(log, "FS WRITE(op 4) flags=0x0(-) arg=0 fd=1 len=40 cap=64 -> REFUSED-WRITE-GATE"),
	      "log: WRITE -> REFUSED-WRITE-GATE, no name read");
	CHECK(strstr(log, "FS RENAME(op 7)") && strstr(log, "name=\"old/name\" new=\"new/name\" -> REFUSED-WRITE-GATE"),
	      "log: RENAME both names -> REFUSED-WRITE-GATE");
	CHECK(strstr(log, "FS OPENDIR(op 10)") && strstr(log, "-> REFUSED-UNSUPPORTED") &&
	      strstr(log, "FS unknown(op 99)"), "log: OPENDIR / unknown -> REFUSED-UNSUPPORTED");
	CHECK(strstr(log, "name=\"evil\\x0aname\\x01\\x22q\\x5c\""), "log: a name is escaped");
	CHECK(strstr(log, "...(+44)") && strstr(log, "name length 300 > 116"), "log: a long name is cut at 256, NAMELEN shown");
	CHECK(strstr(log, "<no NUL in 64 bytes>") && strstr(log, "not NUL-terminated"), "log: unterminated name");
	CHECK(strstr(log, "fd outside the 128-section table") && strstr(log, "entry is not in use (used=0)") &&
	      strstr(log, "len is larger than the payload"), "log: READ errors named");
	CHECK(strstr(log, "RKSS FORMAT CHECK FAILED: partition-table section 5 of 128 has footer version=0x00000000 (want 0x00000001) checkstr=0x12345678 (want 0x12345678); 1 of 128 sections fail"),
	      "log: FORMAT CHECK line (one bad footer: section, version, checkstr, count)");
	CHECK(strstr(log, "partition-table section 0 of 128 has footer version=0x00000000 (want 0x00000001) checkstr=0x00000000 (want 0x12345678); 128 of 128 sections fail"),
	      "log: FORMAT CHECK line (unformatted store)");
	CHECK(strstr(log, "span of sections 0+128 outside the partition (100 sections)"), "log: bounds failure named");
	CHECK(strstr(log, "FS SEEK(op 5) flags=0x40(unknown whence)") && strstr(log, "SEEK_END"), "log: SEEK whence");
	free(log); free(err);

	/* bounded: a 4 KiB cap and 500 records -> at most cap + the one cap line, on both sinks */
	char cp[] = "/tmp/rkss_rpccap_XXXXXX", cpe[] = "/tmp/rkss_rpccape_XXXXXX";
	int cfd = mkstemp(cp), cefd = mkstemp(cpe);
	unlink(cp); unlink(cpe);
	dup2(cefd, 2);
	rpclog_init(cfd, 4096);
	strcpy((char *)pay, "nope");
	for (int i = 0; i < 500; i++) {
		mk(&r, TEE_FS_OPEN, TEE_FS_O_RDONLY, 0, 0);
		fs_dispatch_logged((unsigned long)i, &s, &r, pay, 64);
	}
	size_t cl, cel;
	char *cap = slurp(cfd, &cl), *cape = slurp(cefd, &cel);
	snprintf(msg, sizeof(msg), "bounded: file %zu B, stderr %zu B <= 4096 + one cap line", cl, cel);
	CHECK(cl <= 4096 + 100 && cel <= 4096 + 100, msg);
	CHECK(count_str(cap, "cap of this log is reached") == 1 && count_str(cape, "cap of this log is reached") == 1,
	      "bounded: the cap line is written once per sink");
	free(cap); free(cape);

	/* the log fd must be a regular file or a FIFO, >= 3 */
	int pfd[2];
	if (pipe(pfd) != 0) { perror("pipe"); exit(2); }
	int devnull = open("/dev/null", O_RDONLY);
	CHECK(rpclog_init(1, 0) == 0, "rpclog_init refuses fd 1");
	CHECK(rpclog_init(250, 0) == 0, "rpclog_init refuses a closed fd");
	CHECK(rpclog_init(devnull, 0) == 0, "rpclog_init refuses a character device");
	CHECK(rpclog_init(pfd[1], 0) == 1, "rpclog_init takes a FIFO");
	rpclog_init(-1, 0);
	close(pfd[0]); close(pfd[1]); close(devnull);

	dup2(saved2, 2); close(saved2);
	close(lfd); close(efd); close(cfd); close(cefd);
	close(sb.fd); close(sz.fd); close(s.fd);
	CHECK(fnv1a(img) == before, "log-test image unchanged (no write issued)");
	unlink(img); unlink(bad); unlink(zero);
}

/* ===================== round 3: OPEN of an existing file, any access mode ===================== */
#define LS_DIR   "A54E481B8B69424182B83ACF16E99E2A/.01000000/lock_state/"
#define LS_META0 LS_DIR "meta.0"
#define LS_META1 LS_DIR "meta.1"
#define LS_BLK   LS_DIR "block0.0"
#define LS_META_IDX 130
#define LS_META_SIZE 180
#define LS_BLK_IDX 135
#define LS_BLK_SIZE 64
static uint8_t ls_meta_byte(int i) { return (uint8_t)(i * 7 + 3); }
static uint8_t ls_blk_byte(int i)  { return (uint8_t)(0x5a ^ (i * 13)); }

/* a store shaped like unit 2's lock_state object (names of the round-2 RPC log;
 * the data is synthetic): meta.1 + one block file, meta.0 absent, 3 unrelated files */
static char *build_lockstate_store(void)
{
	static char path[] = "/tmp/rkss_lockstate_XXXXXX";
	int fd = mkstemp(path);
	if (fd < 0) { perror("mkstemp"); exit(2); }
	uint8_t *img = calloc(IMG_SECT, RKSS_SECTION_LEN);
	for (int i = 0; i < RKSS_PARTITION_TABLE_COUNT; i++)
		put_verif(img + (size_t)i * RKSS_SECTION_LEN);
	rkss_file_info *f = (rkss_file_info *)img;
	f[0].used = 1; strcpy(f[0].name, "other/obj.a"); f[0].index = 129; f[0].size = 10; f[0].flags = RK_FS_R | RK_FS_W;
	f[1].used = 1; strcpy(f[1].name, LS_META1); f[1].index = LS_META_IDX; f[1].size = LS_META_SIZE;
	f[1].flags = RK_FS_R | RK_FS_W;
	f[2].used = 1; strcpy(f[2].name, "other/obj.b"); f[2].index = 131; f[2].size = 10; f[2].flags = RK_FS_R;
	rkss_file_info *g = (rkss_file_info *)(img + RKSS_SECTION_LEN);   /* table section 1 */
	g[1].used = 1; strcpy(g[1].name, LS_BLK); g[1].index = LS_BLK_IDX; g[1].size = LS_BLK_SIZE;
	g[1].flags = RK_FS_R | RK_FS_W;
	g[2].used = 1; strcpy(g[2].name, "other/obj.c"); g[2].index = 140; g[2].size = 10; g[2].flags = RK_FS_R;
	memset(img + (size_t)RKSS_USED_FLAGS_INDEX * RKSS_SECTION_LEN, 0x11, RKSS_SECTION_LEN);
	for (int i = 0; i < 2 * RKSS_SECTION_LEN; i++)   /* past the file size too: min(len, size) is checked */
		img[(size_t)LS_META_IDX * RKSS_SECTION_LEN + i] = ls_meta_byte(i);
	for (int i = 0; i < RKSS_SECTION_LEN; i++)
		img[(size_t)LS_BLK_IDX * RKSS_SECTION_LEN + i] = ls_blk_byte(i);
	if (write(fd, img, (size_t)IMG_SECT * RKSS_SECTION_LEN) < 0) { perror("write"); exit(2); }
	free(img); close(fd);
	return path;
}

/* one request through fs_dispatch_logged() (the device path) and, on a copy, through
 * fs_dispatch() (equal rc/header/payload); returns the rc, fills *why from fs_dispatch_why() */
static int r3(const struct rkss_ro *s, struct tee_fs_rpc *r, uint8_t *pay, size_t cap, struct fs_why *why)
{
	struct tee_fs_rpc a = *r, c = *r;
	uint8_t *pa = calloc(1, cap + 1), *pc = calloc(1, cap + 1);
	memcpy(pa, pay, cap); memcpy(pc, pay, cap);
	int ra = fs_dispatch(s, &a, pa, cap);
	int rw = fs_dispatch_why(s, &c, pc, cap, why);
	int rl = fs_dispatch_logged(3, s, r, pay, cap);
	if (!(ra == rl && rw == rl && !memcmp(&a, r, sizeof(a)) && !memcmp(&c, r, sizeof(c)) &&
	      !memcmp(pa, pay, cap) && !memcmp(pc, pay, cap))) {
		printf("FAIL: fs_dispatch / _why / _logged disagree (op %d flags 0x%x)\n", r->op, r->flags);
		fails++;
	}
	free(pa); free(pc);
	return rl;
}

static void round3_tests(void)
{
	printf("\n-- round 4: OPEN served only with flags exactly RDONLY or RDWR; the handle is read-only --\n");
	char *img = build_image();
	unsigned long before = fnv1a(img);
	struct rkss_ro s = { .fd = open(img, O_RDONLY), .base = 0, .nsect = IMG_SECT };
	/* a backend that cannot be read at all: any backend access would show as RKSS_E_IO / BADFD */
	struct rkss_ro dead = { .fd = -1, .base = 0, .nsect = IMG_SECT };
	CHECK(s.fd >= 0, "open synthetic security image O_RDONLY");
	char lp[] = "/tmp/rkss_r3log_XXXXXX", ep[] = "/tmp/rkss_r3err_XXXXXX";
	int lfd = mkstemp(lp), efd = mkstemp(ep), saved2 = dup(2);
	unlink(lp); unlink(ep);
	dup2(efd, 2);
	rpclog_init(lfd, RPCLOG_CAP_DEFAULT);

	uint8_t pay[4096];
	struct tee_fs_rpc r;
	struct fs_why w;
	char msg[200];
	/* round 4: the two flag values of the sealed round-2/3 RPC logs */
	static const struct { int flags; const char *t; } modes[] = {
		{ TEE_FS_O_RDONLY, "RDONLY" }, { TEE_FS_O_RDWR, "RDWR" },
	};
	for (size_t m = 0; m < sizeof(modes) / sizeof(modes[0]); m++) {
		memset(pay, 0, sizeof(pay)); strcpy((char *)pay, TESTNAME);
		mk(&r, TEE_FS_OPEN, modes[m].flags, 0, 0);
		int rc = r3(&s, &r, pay, 64, &w);
		snprintf(msg, sizeof(msg), "OPEN testobj %s (existing) -> served, fd 0 (rc 0, res 0, decision SERVED)", modes[m].t);
		CHECK(rc == 0 && r.res == 0 && r.fd == 0 && w.decision == FSD_SERVED && w.have_info &&
		      w.info.index == TESTIDX, msg);
		int fd = r.res;
		/* a READ on that handle: the file's bytes */
		mk(&r, TEE_FS_READ, 0, fd, TESTSIZE); memset(pay, 0, sizeof(pay));
		rc = r3(&s, &r, pay, TESTSIZE, &w);
		int ok = rc == 0 && r.res == TESTSIZE;
		for (int i = 0; i < TESTSIZE && ok; i++) ok = (pay[i] == (uint8_t)(0xA0 + (i & 0x1f)));
		snprintf(msg, sizeof(msg), "READ on the %s handle -> 180 bytes, content matches", modes[m].t);
		CHECK(ok, msg);
		/* every write-type op on that handle: refused by the gate, before any backend access */
		static const struct { int op; const char *t; } wo[] = {
			{ TEE_FS_WRITE, "WRITE" }, { TEE_FS_TRUNC, "TRUNC" }, { TEE_FS_UNLINK, "UNLINK" },
			{ TEE_FS_RENAME, "RENAME" }, { TEE_FS_LINK, "LINK" }, { TEE_FS_MKDIR, "MKDIR" },
			{ TEE_FS_RMDIR, "RMDIR" },
		};
		for (size_t k = 0; k < sizeof(wo) / sizeof(wo[0]); k++) {
			/* WRITE/TRUNC: a data payload (the marker, never logged); the name ops: a name */
			memset(pay, 0, sizeof(pay));
			strcpy((char *)pay, wo[k].op == TEE_FS_WRITE || wo[k].op == TEE_FS_TRUNC ? MARK_WRITE : TESTNAME);
			mk(&r, wo[k].op, 0, fd, 40); r.arg = 0;
			rc = r3(&s, &r, pay, 64, &w);
			int g1 = rc == -1 && r.res == -1 && w.decision == FSD_REFUSED_WRITE_GATE &&
				 rkss_ro_diag.last == RKSS_E_NONE;
			/* the same on a backend that cannot be read: the gate answers first */
			mk(&r, wo[k].op, 0, fd, 40);
			rc = r3(&dead, &r, pay, 64, &w);
			int g2 = rc == -1 && r.res == -1 && w.decision == FSD_REFUSED_WRITE_GATE &&
				 rkss_ro_diag.last == RKSS_E_NONE;
			snprintf(msg, sizeof(msg), "%s on the %s handle -> REFUSED-WRITE-GATE, no backend access", wo[k].t,
				 modes[m].t);
			CHECK(g1 && g2, msg);
		}
		mk(&r, TEE_FS_CLOSE, 0, fd, 0);
		rc = r3(&s, &r, pay, 16, &w);
		CHECK(rc == 0 && r.res == 0, "CLOSE the handle -> 0");
		CHECK(fnv1a(img) == before, "store fingerprint unchanged after the handle's WRITE/TRUNC/... attempts");
	}

	/* every other flag value (round 4: also WRONLY, the combinations, no mode bit and every
	 * other bit; round 3 served those) and CREAT / APPEND: refused by the gate before any
	 * backend access, for an existing and a missing file */
	static const struct { int flags; const char *t; } cr[] = {
		{ TEE_FS_O_WRONLY, "WRONLY" }, { TEE_FS_O_RDONLY | TEE_FS_O_WRONLY, "RDONLY|WRONLY" },
		{ TEE_FS_O_RDONLY | TEE_FS_O_RDWR, "RDONLY|RDWR" }, { TEE_FS_O_WRONLY | TEE_FS_O_RDWR, "WRONLY|RDWR" },
		{ TEE_FS_O_RDONLY | TEE_FS_O_WRONLY | TEE_FS_O_RDWR, "RDONLY|WRONLY|RDWR" }, { 0, "no mode bit" },
		{ 0x10, "0x10" }, { 0x11, "RDONLY|0x10" }, { 0x14, "RDWR|0x10" }, { 0x40, "0x40" },
		{ 0x41, "RDONLY|0x40" }, { 0x44, "RDWR|0x40" }, { 0x80, "0x80" }, { 0x81, "RDONLY|0x80" },
		{ 0x84, "RDWR|0x80" }, { 0x101, "RDONLY|0x100" }, { 0x104, "RDWR|0x100" }, { 0x10001, "RDONLY|0x10000" },
		{ 0x40000004, "RDWR|0x40000000" }, { (int)0x80000001u, "RDONLY|0x80000000" },
		{ (int)0x80000004u, "RDWR|0x80000000" }, { 0x7ffffffb, "all but RDWR" },
		{ TEE_FS_O_CREAT, "CREAT" }, { TEE_FS_O_APPEND, "APPEND" },
		{ TEE_FS_O_RDWR | TEE_FS_O_CREAT, "RDWR|CREAT" }, { TEE_FS_O_WRONLY | TEE_FS_O_CREAT, "WRONLY|CREAT" },
		{ TEE_FS_O_RDONLY | TEE_FS_O_CREAT, "RDONLY|CREAT" }, { TEE_FS_O_RDWR | TEE_FS_O_APPEND, "RDWR|APPEND" },
		{ TEE_FS_O_WRONLY | TEE_FS_O_APPEND, "WRONLY|APPEND" }, { TEE_FS_O_CREAT | TEE_FS_O_APPEND, "CREAT|APPEND" },
		{ -1, "all bits" },
	};
	static const char *const nm[] = { TESTNAME, "nope" };
	for (size_t c = 0; c < sizeof(cr) / sizeof(cr[0]); c++)
		for (int n = 0; n < 2; n++) {
			memset(pay, 0, sizeof(pay)); strcpy((char *)pay, nm[n]);
			mk(&r, TEE_FS_OPEN, cr[c].flags, 0, 0);
			int rc = r3(&s, &r, pay, 64, &w);
			int g1 = rc == -1 && r.res == -1 && w.decision == FSD_REFUSED_WRITE_GATE &&
				 rkss_ro_diag.last == RKSS_E_NONE;
			mk(&r, TEE_FS_OPEN, cr[c].flags, 0, 0);
			rc = r3(&dead, &r, pay, 64, &w);
			int g2 = rc == -1 && r.res == -1 && w.decision == FSD_REFUSED_WRITE_GATE &&
				 rkss_ro_diag.last == RKSS_E_NONE;
			snprintf(msg, sizeof(msg), "OPEN %s %s -> REFUSED-WRITE-GATE, no backend access", cr[c].t, nm[n]);
			CHECK(g1 && g2, msg);
		}

	/* OPEN of a missing file, any access mode: "not found" (never created) */
	for (size_t m = 0; m < sizeof(modes) / sizeof(modes[0]); m++) {
		memset(pay, 0, sizeof(pay)); strcpy((char *)pay, "nope");
		mk(&r, TEE_FS_OPEN, modes[m].flags, 0, 0);
		int rc = r3(&s, &r, pay, 64, &w);
		snprintf(msg, sizeof(msg), "OPEN nope %s (missing) -> NOT-FOUND (rc 0, res -1)", modes[m].t);
		CHECK(rc == 0 && r.res == -1 && r.fd == 0 && w.decision == FSD_NOT_FOUND &&
		      rkss_ro_diag.last == RKSS_E_NOTFOUND, msg);
	}
	/* a folder match and an unformatted backend: as before, never served */
	memset(pay, 0, sizeof(pay)); strcpy((char *)pay, "testobj/x");
	mk(&r, TEE_FS_OPEN, TEE_FS_O_RDWR, 0, 0);
	CHECK(r3(&s, &r, pay, 64, &w) == 0 && r.res == -1 && w.decision == FSD_NOT_FOUND,
	      "OPEN RDWR of a name below a file -> NOT-FOUND");
	memset(pay, 0, sizeof(pay)); strcpy((char *)pay, TESTNAME);
	mk(&r, TEE_FS_OPEN, TEE_FS_O_RDWR, 0, 0);
	CHECK(r3(&dead, &r, pay, 64, &w) == 0 && r.res == -1 && w.decision == FSD_ERROR &&
	      rkss_ro_diag.last == RKSS_E_IO, "OPEN RDWR on an unreadable backend -> ERROR (the read fails), not served");
	close(s.fd);
	CHECK(fnv1a(img) == before, "round-3 image unchanged (no write issued)");
	unlink(img);

	/* the lock_state-like sequence of unit 2 (round-2 RPC log), now served */
	char *ls = build_lockstate_store();
	unsigned long lbefore = fnv1a(ls);
	struct rkss_ro t = { .fd = open(ls, O_RDONLY), .base = 0, .nsect = IMG_SECT };
	CHECK(t.fd >= 0, "open the lock_state-like store O_RDONLY");
	const size_t NCAP = 61;                 /* the name's SHM payload on the unit: cap=61 */
	memset(pay, 0, sizeof(pay)); strcpy((char *)pay, LS_META0);
	mk(&r, TEE_FS_ACCESS, 0x4, 0, 0);
	CHECK(r3(&t, &r, pay, NCAP, &w) == 0 && r.res == -1 && w.decision == FSD_NOT_FOUND,
	      "seq 1: ACCESS meta.0 (flags 0x4) -> NOT-FOUND");
	memset(pay, 0, sizeof(pay)); strcpy((char *)pay, LS_META1);
	mk(&r, TEE_FS_ACCESS, 0x4, 0, 0);
	CHECK(r3(&t, &r, pay, NCAP, &w) == 0 && r.res == 0 && w.decision == FSD_SERVED && w.info.index == LS_META_IDX &&
	      w.info.size == LS_META_SIZE, "seq 2: ACCESS meta.1 (flags 0x4) -> exists (index 130, size 180)");
	memset(pay, 0, sizeof(pay)); strcpy((char *)pay, LS_META0);
	mk(&r, TEE_FS_OPEN, TEE_FS_O_RDWR, 0, 0);
	CHECK(r3(&t, &r, pay, NCAP, &w) == 0 && r.res == -1 && w.decision == FSD_NOT_FOUND,
	      "seq 3: OPEN RDWR meta.0 -> NOT-FOUND (was REFUSED-WRITE-GATE in round 2)");
	memset(pay, 0, sizeof(pay)); strcpy((char *)pay, LS_META1);
	mk(&r, TEE_FS_OPEN, TEE_FS_O_RDWR, 0, 0);
	int mfd = -9;
	CHECK(r3(&t, &r, pay, NCAP, &w) == 0 && (mfd = r.res) == 1 && r.fd == 1 && w.decision == FSD_SERVED,
	      "seq 4: OPEN RDWR meta.1 -> served, fd 1 (was REFUSED-WRITE-GATE in round 2)");
	mk(&r, TEE_FS_SEEK, TEE_FS_SEEK_SET, mfd, 0); r.arg = 0;
	CHECK(r3(&t, &r, pay, 16, &w) == 0 && r.res == 0, "seq 5: SEEK_SET 0 on meta.1 -> 0");
	static uint8_t rb[4096];
	for (int pass = 0; pass < 2; pass++) {
		uint32_t len = pass ? 4000 : LS_META_SIZE;    /* exact, then more than the file */
		memset(rb, 0xEE, sizeof(rb));
		mk(&r, TEE_FS_READ, 0, mfd, len);
		int rc = r3(&t, &r, rb, len, &w);
		int ok = rc == 0 && r.res == LS_META_SIZE;
		for (int i = 0; i < LS_META_SIZE && ok; i++) ok = rb[i] == ls_meta_byte(i);
		for (uint32_t i = LS_META_SIZE; i < len && ok; i++) ok = rb[i] == 0xEE;   /* nothing past the size */
		snprintf(msg, sizeof(msg), "seq 6.%d: READ meta.1 len %u -> 180 bytes, the expected bytes, nothing past them",
			 pass, len);
		CHECK(ok, msg);
	}
	memset(pay, 0, sizeof(pay)); strcpy((char *)pay, LS_BLK);
	mk(&r, TEE_FS_OPEN, TEE_FS_O_RDWR, 0, 0);
	int bfd = -9;
	CHECK(r3(&t, &r, pay, sizeof(LS_BLK), &w) == 0 && (bfd = r.res) == 5 && w.decision == FSD_SERVED,
	      "seq 7: OPEN RDWR block0.0 -> served, fd 5");
	memset(rb, 0xEE, sizeof(rb));
	mk(&r, TEE_FS_READ, 0, bfd, LS_BLK_SIZE);
	{
		int rc = r3(&t, &r, rb, LS_BLK_SIZE, &w);
		int ok = rc == 0 && r.res == LS_BLK_SIZE;
		for (int i = 0; i < LS_BLK_SIZE && ok; i++) ok = rb[i] == ls_blk_byte(i);
		CHECK(ok, "seq 8: READ block0.0 64 -> the expected 64 bytes");
	}
	/* if OP-TEE tried to write back through the RDWR handles: refused, nothing done */
	memset(pay, 0, sizeof(pay)); strcpy((char *)pay, MARK_WRITE);
	mk(&r, TEE_FS_WRITE, 0, mfd, 40);
	CHECK(r3(&t, &r, pay, 64, &w) == -1 && r.res == -1 && w.decision == FSD_REFUSED_WRITE_GATE,
	      "seq 9: WRITE on the meta.1 RDWR handle -> REFUSED-WRITE-GATE");
	mk(&r, TEE_FS_TRUNC, 0, bfd, 0); r.arg = 0;
	CHECK(r3(&t, &r, pay, 64, &w) == -1 && r.res == -1 && w.decision == FSD_REFUSED_WRITE_GATE,
	      "seq 10: TRUNC on the block0.0 RDWR handle -> REFUSED-WRITE-GATE");
	mk(&r, TEE_FS_CLOSE, 0, bfd, 0);
	CHECK(r3(&t, &r, pay, 16, &w) == 0 && r.res == 0, "seq 11: CLOSE block0.0 -> 0");
	mk(&r, TEE_FS_CLOSE, 0, mfd, 0);
	CHECK(r3(&t, &r, pay, 16, &w) == 0 && r.res == 0, "seq 12: CLOSE meta.1 -> 0");
	close(t.fd);
	CHECK(fnv1a(ls) == lbefore, "lock_state-like store unchanged (no write issued)");
	unlink(ls);

	fflush(stderr);
	size_t ll, el;
	char *log = slurp(lfd, &ll), *err = slurp(efd, &el);
	CHECK(ll > 0 && ll == el && !memcmp(log, err, ll), "round-3 log: the file sink and stderr hold the same lines");
	CHECK(strstr(log, "FS OPEN(op 1) flags=0x4(RDWR) arg=0 fd=0 len=0 cap=61 name=\"" LS_META1 "\" -> SERVED: "
		      "opened read-only (any access mode; the handle never writes); res=1 rc=0; file: entry 1 index 130 "
		      "size 180 flags 0x3; access mode RDWR asked: the handle is read-only (a WRITE/TRUNC on it is refused)") != NULL,
	      "round-3 log: OPEN RDWR meta.1 -> SERVED, mode RDWR, handle read-only");
	CHECK(strstr(log, "FS OPEN(op 1) flags=0x4(RDWR) arg=0 fd=0 len=0 cap=61 name=\"" LS_META0 "\" -> NOT-FOUND") != NULL,
	      "round-3 log: OPEN RDWR meta.0 -> NOT-FOUND");
	CHECK(strstr(log, "FS OPEN(op 1) flags=0x2(WRONLY) arg=0 fd=0 len=0 cap=64 name=\"testobj\" -> REFUSED-WRITE-GATE: "
		      "OPEN with flags other than exactly RDONLY (0x1) or RDWR (0x4); res=-1 rc=-1; access mode WRONLY asked") &&
	      strstr(log, "flags=0x6(WRONLY|RDWR)") && strstr(log, "flags=0x0(none)") &&
	      strstr(log, "flags=0x14(RDWR|other 0x10)") && strstr(log, "flags=0x41(RDONLY|other 0x40)") &&
	      !strstr(log, "access mode WRONLY asked: the handle is read-only") &&
	      !strstr(log, "access mode none asked: the handle is read-only"),
	      "round-4 log: every other OPEN flag value refused at the gate, its mode named; no WRONLY/none handle");
	CHECK(strstr(log, "FS WRITE(op 4) flags=0x0(-) arg=0 fd=1 len=40 cap=64 -> REFUSED-WRITE-GATE") &&
	      strstr(log, "FS TRUNC(op 8) flags=0x0(-) arg=0 fd=5 len=0 cap=64 -> REFUSED-WRITE-GATE"),
	      "round-3 log: WRITE/TRUNC on the RDWR handles -> REFUSED-WRITE-GATE");
	CHECK(strstr(log, "flags=0xc(RDWR|CREAT)") &&
	      strstr(log, "-> REFUSED-WRITE-GATE: OPEN with flags other than exactly RDONLY (0x1) or RDWR (0x4)") &&
	      !strstr(log, "OPEN with CREAT/APPEND: a store mutation"),
	      "round-4 log: OPEN RDWR|CREAT -> REFUSED-WRITE-GATE (the round-4 text; the round-3 text is gone)");
	CHECK(strstr(log, "data reply 180 bytes (1 sections read; bytes not logged)") &&
	      strstr(log, "data reply 64 bytes (1 sections read; bytes not logged)"), "round-3 log: READ lengths");
	CHECK(!strstr(log, "WRITE-MARKER"), "round-3 log: no WRITE payload byte");
	free(log); free(err);
	dup2(saved2, 2); close(saved2);
	close(lfd); close(efd);
	rpclog_init(-1, 0);
}

int main(void)
{
	char *img = build_image();
	unsigned long before = fnv1a(img);

	struct rkss_ro s = { .fd = open(img, O_RDONLY), .base = 0, .nsect = IMG_SECT };
	CHECK(s.fd >= 0, "open synthetic security image O_RDONLY");

	uint8_t pay[4096];
	struct tee_fs_rpc r;

	/* ACCESS testobj (R_OK) */
	mk(&r, TEE_FS_ACCESS, TEE_FS_R_OK, 0, 0);
	strcpy((char *)pay, TESTNAME);
	fs_dispatch(&s, &r, pay, sizeof(pay));
	CHECK(r.res == 0, "ACCESS testobj R_OK -> 0");

	/* OPEN testobj read-only -> fd */
	mk(&r, TEE_FS_OPEN, TEE_FS_O_RDONLY, 0, 0);
	strcpy((char *)pay, TESTNAME);
	fs_dispatch(&s, &r, pay, sizeof(pay));
	int fd = r.res;
	CHECK(fd >= 0, "OPEN testobj O_RDONLY -> fd");

	/* READ it back */
	mk(&r, TEE_FS_READ, 0, fd, TESTSIZE);
	memset(pay, 0, sizeof(pay));
	fs_dispatch(&s, &r, pay, sizeof(pay));
	int ok = (r.res == TESTSIZE);
	for (int i = 0; i < TESTSIZE && ok; i++) ok = (pay[i] == (uint8_t)(0xA0 + (i & 0x1f)));
	CHECK(ok, "READ testobj -> 180 bytes, content matches");

	/* C1 regression: the real SHM payload cap == len (not oversized). The READ
	 * must still succeed when payload_cap is exactly len, as on the unit. */
	mk(&r, TEE_FS_READ, 0, fd, TESTSIZE);
	memset(pay, 0, sizeof(pay));
	fs_dispatch(&s, &r, pay, TESTSIZE);      /* cap == len */
	ok = (r.res == TESTSIZE);
	for (int i = 0; i < TESTSIZE && ok; i++) ok = (pay[i] == (uint8_t)(0xA0 + (i & 0x1f)));
	CHECK(ok, "READ with payload_cap==len -> 180 bytes (C1)");

	/* OPEN missing, no create -> -1 */
	mk(&r, TEE_FS_OPEN, TEE_FS_O_RDONLY, 0, 0);
	strcpy((char *)pay, "nope");
	fs_dispatch(&s, &r, pay, sizeof(pay));
	CHECK(r.res == -1, "OPEN missing O_RDONLY -> -1 (never create)");

	/* SEEK SET */
	mk(&r, TEE_FS_SEEK, TEE_FS_SEEK_SET, fd, 0); r.arg = 7;
	fs_dispatch(&s, &r, pay, sizeof(pay));
	CHECK(r.res == 7, "SEEK SET 7 -> 7");

	/* ---- every write-type op must be REFUSED ---- */
	struct { int op, flags; const char *name; } w[] = {
		{ TEE_FS_WRITE, 0, "WRITE" },
		{ TEE_FS_TRUNC, 0, "TRUNC" },
		{ TEE_FS_UNLINK, 0, "UNLINK" },
		{ TEE_FS_RENAME, 0, "RENAME" },
		{ TEE_FS_LINK, 0, "LINK" },
		{ TEE_FS_MKDIR, 0, "MKDIR" },
		{ TEE_FS_RMDIR, 0, "RMDIR" },
		{ TEE_FS_OPEN, TEE_FS_O_CREAT, "OPEN+O_CREAT" },
		{ TEE_FS_OPEN, TEE_FS_O_APPEND, "OPEN+O_APPEND" },
		/* round 3: OPEN+O_RDWR (an existing file) is served read-only: round3_tests() */
		{ TEE_FS_OPEN, TEE_FS_O_RDWR | TEE_FS_O_CREAT, "OPEN+O_RDWR|O_CREAT" },
		{ TEE_FS_OPEN, TEE_FS_O_RDWR | TEE_FS_O_APPEND, "OPEN+O_RDWR|O_APPEND" },
	};
	for (size_t i = 0; i < sizeof(w)/sizeof(w[0]); i++) {
		mk(&r, w[i].op, w[i].flags, fd, 16);
		strcpy((char *)pay, TESTNAME);
		int rc = fs_dispatch(&s, &r, pay, sizeof(pay));
		char msg[64]; snprintf(msg, sizeof(msg), "%s refused (res=-1)", w[i].name);
		CHECK(rc == -1 && r.res == -1, msg);
	}

	/* unknown op refused */
	mk(&r, 999, 0, 0, 0);
	CHECK(fs_dispatch(&s, &r, pay, sizeof(pay)) == -1, "unknown op refused");

	close(s.fd);
	unsigned long after = fnv1a(img);
	CHECK(before == after, "image fingerprint unchanged (no write issued)");
	unlink(img);

	round3_tests();
	log_tests();

	printf(fails ? "\n%d FAILS\n" : "\nALL PASS\n", fails);
	return fails ? 1 : 0;
}
