// SPDX-License-Identifier: GPL-2.0-or-later
/*
 * rpcequiv.c -- proof that the logging build serves and refuses exactly what the
 * reviewed build (ref/, binary 23142586...) serves and refuses, for every op.
 *
 * Round 3 (OPTEE-READTEST-3-2026-11-11.md): it also runs the round-2 sources
 * (ref2/, binary 9200e2f0...) and tags every line: 'C' = an FS OPEN whose flags
 * carry an access mode WRONLY/RDWR and neither CREAT nor APPEND (the one class
 * the round-3 policy change may alter), 'R' = a SEEK_SET 0 sent after each 'C'
 * case to put the static seek cursor of both builds in the same state again (a
 * served OPEN sets it to 0, a refused one left it), '-' = everything else.
 * build.sh / rpcequiv-check.py require: ref/ = ref2/ on every line; ref2/ = new
 * on every '-' and 'R' line; each 'C' line that differs is an OPEN of a file that
 * exists, answered "served, fd N" by new where ref2/ answered -1, with every
 * other field equal to the OPEN RDONLY of the same file. FS lines also carry the
 * case key and the header's res/fd after the call. A write() to a store fd from
 * supplicant.c aborts the run; the store files are fingerprinted before/after.
 *
 * It includes a supplicant.c (SUPP_C: "ref/supplicant.c" or "supplicant.c") with
 * read/write/ioctl on the TEE device replaced by a fake device, and drives the
 * real service_one_rpc() -> fs_dispatch...() -> rkss_ro_...() code over a fixed,
 * deterministic sweep of RPCs on synthetic stores:
 *   - FS RPCs: every op 0..16 (+99, -1) x 11 flag values x 9 names x 5 fds x
 *     5 lengths x 2 payload caps, on 4 stores (good; one bad footer; all zero;
 *     the table outside a too-small partition), in one sequence (the static seek
 *     cursor carries over exactly as on the unit);
 *   - RPC shapes: LOAD_TA/RPMB/FS/GET_TIME/0/unknown x nbr_bf x buffer type x the
 *     SHM ioctl failing / giving no fd / an unmappable fd / a working memfd x SHM
 *     sizes x the kernel's preset result x the reply write failing; EOF; EINTR.
 * Round 4 (the LS supplicant, MAINLINE-RC2-LS-2026-11-11.md), built with -DCLASS4:
 * 'C' = an FS OPEN whose flags are neither exactly RDONLY (0x1) nor exactly RDWR
 * (0x4) and carry neither CREAT nor APPEND (the one class the round-4 policy
 * change may alter: round 3 looked such a name up, round 4 refuses it at the
 * gate); 'R' as above; the flag sweep also has 0x11 0x41 0x80 0x81 0x84 0x101
 * 0x104 0x80000004 (RDONLY/RDWR with one more bit). rpcequiv-check4.py requires
 * ref3/ = new on every '-' and 'R' line and every 'C' line of new to be the gate
 * refusal: equal (return, errno, ioctl, reply bytes, FS res/fd) to new's own OPEN
 * with flags CREAT (0x8) of the same case.
 * Per RPC it prints: service_one_rpc's return and errno, the SHM ioctl request,
 * an FNV-1a 64 of the reply bytes written to the device (and their length), and
 * an FNV-1a 64 of the whole SHM afterwards. build.sh runs it for ref/ and for the
 * new sources (log off, log on) and requires the three outputs to be identical.
 * Test code only: never in the image.
 */
#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <errno.h>
#include <fcntl.h>
#include <unistd.h>
#include <sys/ioctl.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <sys/sysmacros.h>
#include <sys/prctl.h>
#include <sys/syscall.h>

#define FAKE_DEVFD 1000

static ssize_t fake_read(int fd, void *buf, size_t n);
static ssize_t fake_write(int fd, const void *buf, size_t n);
static int fake_ioctl(int fd, unsigned long req, void *arg);

#define read fake_read
#define write fake_write
#define ioctl fake_ioctl
#define main supplicant_main
#include SUPP_C
#undef read
#undef write
#undef ioctl
#undef main

#ifdef NEW_BUILD
#include "rpclog.h"
#endif

static int store_fds[4] = { -1, -1, -1, -1 };   /* a write() to one of them aborts the run */

/* ---- the fake device ---- */
static struct tee_rpc_invoke cur_inv;
static ssize_t cur_n;          /* what read returns */
static int     cur_n_errno;
static int     cur_ioctl;      /* 0 memfd, 1 fail EINVAL, 2 ok but fd -1, 3 a pipe (mmap fails) */
static size_t  cur_shm_size;
static uint8_t cur_shm_init[80000];
static int     cur_memfd = -1;
static int     cur_wmode;      /* 0 ok, 1 fail EIO, 2 short */
static uint64_t w_hash; static ssize_t w_len; static int w_calls;
static struct tee_shm_io io_req; static int io_calls;

static uint64_t fnv(uint64_t h, const void *p, size_t n)
{
	const uint8_t *b = p;
	for (size_t i = 0; i < n; i++) { h ^= b[i]; h *= 1099511628211ULL; }
	return h;
}
#define FNV0 1469598103934665603ULL

static ssize_t fake_read(int fd, void *buf, size_t n)
{
	if (fd != FAKE_DEVFD)
		return syscall(SYS_read, fd, buf, n);
	if (cur_n < 0) { errno = cur_n_errno; return -1; }
	if (cur_n == 0) return 0;
	memcpy(buf, &cur_inv, (size_t)cur_n < n ? (size_t)cur_n : n);
	return cur_n;
}

static ssize_t fake_write(int fd, const void *buf, size_t n)
{
	for (int i = 0; i < 4; i++)
		if (fd >= 0 && fd == store_fds[i]) {
			fprintf(stderr, "rpcequiv: a write() to a store fd -- ABORT\n");
			exit(3);
		}
	if (fd != FAKE_DEVFD)
		return syscall(SYS_write, fd, buf, n);
	w_calls++;
	w_hash = fnv(FNV0, buf, n); w_len = (ssize_t)n;
	if (cur_wmode == 1) { errno = EIO; return -1; }
	if (cur_wmode == 2) return (ssize_t)n - 1;
	return (ssize_t)n;
}

static int fake_ioctl(int fd, unsigned long req, void *arg)
{
	if (fd != FAKE_DEVFD)
		return (int)syscall(SYS_ioctl, fd, req, arg);
	io_calls++;
	if (req != TEE_GET_FD_FOR_RPC_SHM_IOC) { errno = ENOTTY; return -1; }
	struct tee_shm_io *s = arg;
	io_req = *s;
	switch (cur_ioctl) {
	case 1: errno = EINVAL; return -1;
	case 2: s->fd_shm = -1; return 0;
	case 3: {
		int p[2];
		if (pipe(p) != 0) { perror("pipe"); exit(2); }
		close(p[1]);
		s->fd_shm = p[0];
		return 0;
	}
	default:
		cur_memfd = memfd_create("shm", MFD_CLOEXEC);
		if (cur_memfd < 0 || ftruncate(cur_memfd, (off_t)cur_shm_size) != 0) { perror("memfd"); exit(2); }
		if (cur_shm_size && pwrite(cur_memfd, cur_shm_init, cur_shm_size, 0) != (ssize_t)cur_shm_size) {
			perror("pwrite memfd"); exit(2);
		}
		s->fd_shm = dup(cur_memfd);
		return 0;
	}
}

/* ---- synthetic stores ---- */
#define IMG_SECT (RKSS_DATA_SECTION_COUNT + 256)
#define MARK_DATA  "DATA-MARKER-must-never-be-logged-"
#define MARK_WRITE "WRITE-MARKER-must-never-be-logged-"

static void put_verif(uint8_t *sec, uint32_t ver, uint32_t chk)
{
	struct rkss_file_verification v = { ver, chk };
	memcpy(sec + RKSS_SECTION_LEN - sizeof(v), &v, sizeof(v));
}

static int make_store_ro(int variant, char *path_out)
{
	strcpy(path_out, "/tmp/rpcequiv_XXXXXX");
	int fd = mkstemp(path_out);
	if (fd < 0) { perror("mkstemp"); exit(2); }
	close(fd);
	uint8_t *img = calloc(IMG_SECT, RKSS_SECTION_LEN);
	if (variant != 2) {
		for (int i = 0; i < RKSS_PARTITION_TABLE_COUNT; i++)
			put_verif(img + (size_t)i * RKSS_SECTION_LEN, RKSS_VERSION_V1, RKSS_CHECK_STR);
		rkss_file_info *f = (rkss_file_info *)img;
		f[0].used = 1; strcpy(f[0].name, "testobj"); f[0].index = 130; f[0].size = 180; f[0].flags = RK_FS_R | RK_FS_W;
		f[1].used = 1; strcpy(f[1].name, "markobj"); f[1].index = 140; f[1].size = 300; f[1].flags = RK_FS_R;
		f[2].used = 2; strcpy(f[2].name, "odd");      f[2].index = 160; f[2].size = 10;  f[2].flags = RK_FS_R;
		f[3].used = 1; strcpy(f[3].name, "dir/file"); f[3].index = 150; f[3].size = 600; f[3].flags = RK_FS_W;
		for (size_t o = 0; o + strlen(MARK_DATA) <= 1200; o += strlen(MARK_DATA))
			memcpy(img + (size_t)140 * RKSS_SECTION_LEN + o, MARK_DATA, strlen(MARK_DATA));
		for (int i = 0; i < 180; i++) img[(size_t)130 * RKSS_SECTION_LEN + i] = (uint8_t)(0xA0 + (i & 0x1f));
	}
	if (variant == 1)
		put_verif(img + 5 * RKSS_SECTION_LEN, 0, RKSS_CHECK_STR);
	FILE *f = fopen(path_out, "wb");
	if (!f || fwrite(img, RKSS_SECTION_LEN, IMG_SECT, f) != IMG_SECT || fclose(f)) { perror("store"); exit(2); }
	free(img);
	int ro = open(path_out, O_RDONLY);
	if (ro < 0) { perror("open store"); exit(2); }
	return ro;
}

static unsigned long ncase;
static char cur_tag = '-';          /* 'C' / 'R' / '-' (see the head of this file) */
static char cur_key[96];            /* FS cases: the sweep indices; else empty */

static void run_case(struct rkss_ro *s)
{
	w_calls = 0; w_hash = 0; w_len = -2; io_calls = 0; memset(&io_req, 0, sizeof(io_req));
	cur_memfd = -1;
	errno = 0;
	int r = service_one_rpc(FAKE_DEVFD, s);
	int e = r < 0 ? errno : 0;
	uint64_t sh = 0;
	char fsr[48] = "fs=-";
	if (cur_memfd >= 0) {
		static uint8_t back[80000];
		if (cur_shm_size && pread(cur_memfd, back, cur_shm_size, 0) != (ssize_t)cur_shm_size) {
			perror("pread memfd"); exit(2);
		}
		sh = fnv(FNV0, back, cur_shm_size);
		if (cur_shm_size >= sizeof(struct tee_fs_rpc)) {
			struct tee_fs_rpc h;
			memcpy(&h, back, sizeof(h));
			snprintf(fsr, sizeof(fsr), "fs=%d/%d", h.res, h.fd);
		}
		close(cur_memfd);
		cur_memfd = -1;
	}
	printf("%c %lu r=%d e=%d io=%d{%u,%u,%llx} w=%d/%zd/%016llx shm=%016llx %s%s%s\n", cur_tag, ncase++, r, e,
	       io_calls, io_req.size, io_req.flags, (unsigned long long)io_req.padding_buf, w_calls, w_len,
	       (unsigned long long)w_hash, (unsigned long long)sh, fsr, cur_key[0] ? " key=" : "", cur_key);
	cur_tag = '-';
	cur_key[0] = 0;
}

static void fs_case(struct rkss_ro *s, int op, int flags, int arg, int fd, uint32_t len,
		    const char *name, size_t namelen, size_t cap)
{
	struct tee_fs_rpc h = { .op = op, .flags = flags, .arg = arg, .fd = fd, .len = len, .res = -999 };
	memset(cur_shm_init, 0, sizeof(h) + cap + 64);
	memcpy(cur_shm_init, &h, sizeof(h));
	uint8_t *p = cur_shm_init + sizeof(h);
	if (name) {                               /* the name, then a second name, then the marker */
		memcpy(p, name, namelen);
		size_t o = namelen;
		if (name[namelen - 1] == 0) {
			if (op == TEE_FS_RENAME || op == TEE_FS_LINK) { memcpy(p + o, "new/name", 9); o += 9; }
			memcpy(p + o, MARK_WRITE, strlen(MARK_WRITE));
		}
	}
	memset(&cur_inv, 0, sizeof(cur_inv));
	cur_inv.cmd = TEE_RPC_FS; cur_inv.res = 0xffff0009u; cur_inv.nbr_bf = 1;
	cur_inv.cmds[0].type = TEE_RPC_BUFFER; cur_inv.cmds[0].size = (uint32_t)(sizeof(h) + cap);
	cur_inv.cmds[0].padding_buf = 0x9e0b000ULL + ncase;
	cur_n = sizeof(cur_inv); cur_ioctl = 0; cur_wmode = 0;
	cur_shm_size = sizeof(h) + cap;
	run_case(s);
}

int main(int argc, char **argv)
{
#ifdef NEW_BUILD
	if (argc > 2 && !strcmp(argv[1], "--log")) {
		int lfd = open(argv[2], O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0644);   /* test code only */
		if (lfd < 3) { perror("log"); return 2; }
		rpclog_init(lfd, (size_t)1 << 30);
	}
#else
	(void)argc; (void)argv;
#endif
	char paths[4][32];
	int fds[4];
	for (int v = 0; v < 3; v++) fds[v] = make_store_ro(v, paths[v]);
	fds[3] = fds[0];
	for (int v = 0; v < 4; v++) store_fds[v] = fds[v];
	uint64_t fp_before[3];
	for (int v = 0; v < 3; v++) {
		static uint8_t img[(size_t)IMG_SECT * RKSS_SECTION_LEN];
		if (pread(fds[v], img, sizeof(img), 0) != (ssize_t)sizeof(img)) { perror("pread store"); exit(2); }
		fp_before[v] = fnv(FNV0, img, sizeof(img));
	}
	struct rkss_ro st[4] = {
		{ .fd = fds[0], .base = 0, .nsect = IMG_SECT },
		{ .fd = fds[1], .base = 0, .nsect = IMG_SECT },
		{ .fd = fds[2], .base = 0, .nsect = IMG_SECT },
		{ .fd = fds[3], .base = 0, .nsect = 100 },     /* the table outside the partition */
	};

	static const int ops[] = { 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 99, -1 };
	/* round 3: + 0x5 0x6 0x7 0x14 0x44 (more access-mode combos) and 0xc 0x24 (RDWR|CREAT, RDWR|APPEND) */
	static const int flags[] = { 0, 1, 2, 4, 8, 0x10, 0x20, 0x9, 0x3, 0x40, -1,
				     0x5, 0x6, 0x7, 0x14, 0x44, 0xc, 0x24
#ifdef CLASS4
				     , 0x11, 0x41, 0x80, 0x81, 0x84, 0x101, 0x104, (int)0x80000004u
#endif
	};
	static const int fdv[] = { 0, 1, 2, -1, 512 };
	static const uint32_t lens[] = { 0, 16, 180, 600, 70000 };
	char longname[201]; memset(longname, 'n', 200); longname[200] = 0;
	static char unterm[80000]; memset(unterm, 'u', sizeof(unterm));
	struct { const char *n; size_t l; } names[] = {
		{ "testobj", 8 }, { "markobj", 8 }, { "nope", 5 }, { "dir", 4 }, { "dir/file", 9 },
		{ "odd", 4 }, { "", 1 }, { longname, 201 }, { unterm, 0 /* = the whole cap */ },
	};
	const int NN = (int)(sizeof(names) / sizeof(names[0]));

	for (int v = 0; v < 4; v++)
		for (size_t oi = 0; oi < sizeof(ops) / sizeof(ops[0]); oi++)
			for (size_t fi = 0; fi < sizeof(flags) / sizeof(flags[0]); fi++)
				for (int ni = 0; ni < NN; ni++)
					for (size_t di = 0; di < sizeof(fdv) / sizeof(fdv[0]); di++)
						for (size_t li = 0; li < sizeof(lens) / sizeof(lens[0]); li++)
							for (int ci = 0; ci < 2; ci++) {
								uint32_t len = lens[li];
								size_t cap = ci == 0 ? len : 4072;
								if (cap > sizeof(cur_shm_init) - 64) cap = sizeof(cur_shm_init) - 64;
								size_t nl = names[ni].l ? names[ni].l : cap;
								if (nl > cap) nl = cap;
								int arg = (int)di * 3 - 2 + (int)li;
								int op = ops[oi], fl = flags[fi];
#ifdef CLASS4
								int cls = op == TEE_FS_OPEN &&
									  fl != TEE_FS_O_RDONLY && fl != TEE_FS_O_RDWR &&
									  !(fl & (TEE_FS_O_CREAT | TEE_FS_O_APPEND));
#else
								int cls = op == TEE_FS_OPEN &&
									  (fl & (TEE_FS_O_WRONLY | TEE_FS_O_RDWR)) &&
									  !(fl & (TEE_FS_O_CREAT | TEE_FS_O_APPEND));
#endif
								cur_tag = cls ? 'C' : '-';
								snprintf(cur_key, sizeof(cur_key), "v%d,op%d,f%x,n%d,d%zu,l%zu,c%d",
									 v, op, (unsigned)fl, ni, di, li, ci);
								fs_case(&st[v], op, fl, arg, fdv[di], len,
									nl ? names[ni].n : NULL, nl, cap);
								if (cls) {   /* resync the static seek cursor */
									cur_tag = 'R';
									fs_case(&st[v], TEE_FS_SEEK, TEE_FS_SEEK_SET, 0,
										fdv[di], 0, NULL, 0, 16);
								}
							}

	/* RPC shapes */
	static const uint32_t cmds[] = { TEE_RPC_LOAD_TA, TEE_RPC_RPMB_CMD, TEE_RPC_FS, TEE_RPC_GET_TIME, 0, 0xdeadbeefu };
	static const uint32_t nbrs[] = { 0, 1, 2, 5, 7 };
	static const uint32_t types[] = { 0, TEE_RPC_BUFFER, TEE_RPC_VALUE };
	static const size_t sizes[] = { 0, 8, 24, 64 };
	static const uint32_t resin[] = { 0xffff0009u, 0 };
	for (size_t a = 0; a < 6; a++)
		for (size_t b = 0; b < 5; b++)
			for (size_t c = 0; c < 3; c++)
				for (int io = 0; io < 4; io++)
					for (size_t z = 0; z < 4; z++)
						for (size_t q = 0; q < 2; q++)
							for (int wm = 0; wm < 3; wm++) {
								memset(&cur_inv, 0, sizeof(cur_inv));
								cur_inv.cmd = cmds[a]; cur_inv.res = resin[q]; cur_inv.nbr_bf = nbrs[b];
								for (int k = 0; k < TEE_RPC_BUFFER_NUMBER; k++) {
									cur_inv.cmds[k].padding_buf = 0x9e0b000ULL + (uint64_t)k * 0x1000;
									cur_inv.cmds[k].type = k ? TEE_RPC_VALUE : types[c];
									cur_inv.cmds[k].size = (uint32_t)sizes[z];
									cur_inv.cmds[k].fd = k; cur_inv.cmds[k].reserved = 7;
								}
								memset(cur_shm_init, 0, sizeof(cur_shm_init));
								struct tee_fs_rpc h = { .op = TEE_FS_OPEN, .flags = TEE_FS_O_RDONLY, .res = -999 };
								memcpy(cur_shm_init, &h, sizeof(h));
								memcpy(cur_shm_init + sizeof(h), "testobj", 8);
								cur_n = sizeof(cur_inv); cur_ioctl = io; cur_wmode = wm;
								cur_shm_size = sizes[z];
								run_case(&st[(a + b + z) % 4]);
							}
	/* EOF and an interrupted read */
	cur_n = 0; run_case(&st[0]);
	cur_n = -1; cur_n_errno = EINTR; run_case(&st[0]);
	cur_n = -1; cur_n_errno = EIO; run_case(&st[0]);

	for (int v = 0; v < 3; v++) {
		static uint8_t img[(size_t)IMG_SECT * RKSS_SECTION_LEN];
		if (pread(fds[v], img, sizeof(img), 0) != (ssize_t)sizeof(img)) { perror("pread store"); exit(2); }
		if (fnv(FNV0, img, sizeof(img)) != fp_before[v]) {
			fprintf(stderr, "rpcequiv: store %d CHANGED -- ABORT\n", v);
			exit(3);
		}
	}
	for (int v = 0; v < 3; v++) { close(fds[v]); unlink(paths[v]); }
	fprintf(stdout, "END %lu cases; the 3 store files unchanged (fingerprint)\n", ncase);
	return 0;
}
