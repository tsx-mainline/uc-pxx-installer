// SPDX-License-Identifier: GPL-2.0-or-later
/*
 * supplicant.c -- a READ-ONLY, FAIL-CLOSED OP-TEE supplicant for the legacy
 * Rockchip driver (/dev/opteearmtz00), scoped to exactly what a lock_state READ
 * needs. It NEVER writes a block device, NEVER touches RPMB, and opens the
 * security partition O_RDONLY, so even a bug cannot mutate the store.
 *
 * It replaces the vendor tee-supplicant for the READ milestone: the vendor
 * binary writes 64 KiB to the security partition at startup (rk_fs_init), which
 * makes a vendor-supplicant READ non-write-free and, on a wrong node, a brick.
 * This one issues only pread() on the resolved security partition.
 *
 * Partition resolution: read-only parse of the GPT on the sdhci eMMC disk, find
 * PARTNAME "security" (UTF-16LE), open THAT partition node O_RDONLY, and verify
 * via sysfs it is a partition (has /sys/class/block/<p>/partition) whose start
 * LBA matches the GPT entry -- never a whole disk, never a path guess.
 *
 * Static aarch64, no bionic. Build: build.sh.
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

#include "rkss_ro.h"
#include "fsdispatch.h"

#define OPTEE_DEV "/dev/opteearmtz00"
#define TEEC_MEM_INPUT  0x1u
#define TEEC_MEM_OUTPUT 0x2u

/* ---- GPT (read-only) ---- */
struct gpt_hdr {
	char     sig[8];              /* "EFI PART" */
	uint32_t rev, hdr_size, hdr_crc, reserved;
	uint64_t cur_lba, bak_lba, first_usable, last_usable;
	uint8_t  disk_guid[16];
	uint64_t part_entry_lba;
	uint32_t num_entries, entry_size, part_crc;
} __attribute__((packed));

struct gpt_ent {
	uint8_t  type_guid[16];
	uint8_t  uniq_guid[16];
	uint64_t first_lba, last_lba, attrs;
	uint16_t name[36];            /* UTF-16LE */
} __attribute__((packed));

/* returns the 1-based partition number for PARTNAME==want, and *first_lba; -1 if none */
static int gpt_find_partition(const char *disk, const char *want, uint64_t *first_lba)
{
	int fd = open(disk, O_RDONLY | O_CLOEXEC);          /* READ ONLY */
	if (fd < 0) { fprintf(stderr, "open %s: %s\n", disk, strerror(errno)); return -1; }

	struct gpt_hdr h;
	if (pread(fd, &h, sizeof(h), 512) != (ssize_t)sizeof(h) ||
	    memcmp(h.sig, "EFI PART", 8)) {
		fprintf(stderr, "no GPT on %s\n", disk); close(fd); return -1;
	}
	uint32_t n = h.num_entries, esz = h.entry_size;
	if (esz < sizeof(struct gpt_ent) || esz > 4096 || n == 0 || n > 256) { close(fd); return -1; }

	int found = -1, matches = 0;
	uint8_t *buf = malloc((size_t)esz);
	if (!buf) { close(fd); return -1; }
	for (uint32_t i = 0; i < n; i++) {
		off_t off = (off_t)h.part_entry_lba * 512 + (off_t)i * esz;
		if (pread(fd, buf, esz, off) != (ssize_t)esz) break;
		struct gpt_ent *e = (struct gpt_ent *)buf;
		int empty = 1;
		for (int k = 0; k < 16; k++) if (e->type_guid[k]) { empty = 0; break; }
		if (empty) continue;
		/* decode name as strict ASCII; a non-ASCII unit must not alias "security" */
		char name[37]; int j = 0, bad = 0;
		for (; j < 36 && e->name[j]; j++) {
			if (e->name[j] > 0x7f) { bad = 1; break; }
			name[j] = (char)e->name[j];
		}
		name[j] = 0;
		if (!bad && !strcmp(name, want)) { matches++; found = (int)i + 1; *first_lba = e->first_lba; }
	}
	free(buf); close(fd);
	if (matches > 1) {           /* ambiguous: refuse rather than guess */
		fprintf(stderr, "more than one 'security' partition -- refusing\n");
		return -1;
	}
	return found;
}

/* verify /dev/<base> is a PARTITION (not a whole disk) whose start == first_lba */
static int sysfs_is_partition(const char *devbase, uint64_t first_lba)
{
	char path[384]; char val[64]; int ok = 0;
	snprintf(path, sizeof(path), "/sys/class/block/%s/partition", devbase);
	int fd = open(path, O_RDONLY | O_CLOEXEC);
	if (fd < 0) return 0;                       /* no "partition" attr => whole disk: refuse */
	close(fd);
	snprintf(path, sizeof(path), "/sys/class/block/%s/start", devbase);
	fd = open(path, O_RDONLY | O_CLOEXEC);
	if (fd >= 0) {
		ssize_t r = read(fd, val, sizeof(val) - 1); close(fd);
		if (r > 0) { val[r] = 0; ok = (strtoull(val, NULL, 10) == first_lba); }
	}
	return ok;
}

/* Resolve and O_RDONLY-open the security partition. Returns fd or -1. */
static int open_security_ro(const char *disk, struct rkss_ro *s)
{
	uint64_t first_lba = 0;
	int partnum = gpt_find_partition(disk, "security", &first_lba);
	if (partnum < 0) { fprintf(stderr, "no 'security' partition in GPT of %s\n", disk); return -1; }

	/* partition node: <disk>p<N> for mmcblk/nvme, <disk><N> otherwise */
	const char *d = disk + strlen(disk);
	int need_p = (d > disk && (d[-1] >= '0' && d[-1] <= '9'));
	char part[320], base[320];
	snprintf(part, sizeof(part), "%s%s%d", disk, need_p ? "p" : "", partnum);
	const char *b = strrchr(part, '/'); snprintf(base, sizeof(base), "%s", b ? b + 1 : part);

	if (!sysfs_is_partition(base, first_lba)) {
		fprintf(stderr, "%s is not a partition at LBA %llu -- refusing (never whole disk)\n",
			part, (unsigned long long)first_lba);
		return -1;
	}
	int fd = open(part, O_RDONLY | O_CLOEXEC);          /* READ ONLY */
	if (fd < 0) { fprintf(stderr, "open %s: %s\n", part, strerror(errno)); return -1; }

	struct stat st; uint64_t nbytes = 0;
	if (fstat(fd, &st) == 0 && S_ISBLK(st.st_mode)) {
		unsigned long sz = 0;
		if (ioctl(fd, 0x80081272 /*BLKGETSIZE64*/, &nbytes) != 0) nbytes = 0;
		(void)sz;
	}
	s->fd = fd; s->base = 0; s->nsect = nbytes ? nbytes / 512 : RKSS_DATA_SECTION_COUNT + 256;
	fprintf(stderr, "security = %s (partition %d, start LBA %llu), O_RDONLY\n",
		part, partnum, (unsigned long long)first_lba);
	return fd;
}

/* ---- RPC loop ---- */
static int service_one_rpc(int devfd, struct rkss_ro *s)
{
	struct tee_rpc_invoke inv;
	ssize_t n = read(devfd, &inv, sizeof(inv));
	if (n <= 0) return (int)n;              /* 0/-1 => caller decides */

	int serviced = 0;
	/* Only TEE_RPC_FS is serviced. RPMB/LOAD_TA/GET_TIME/unknown: leave the
	 * kernel's preset error (NOT_IMPLEMENTED) in inv.res -> fail closed. */
	if (inv.cmd == TEE_RPC_FS && inv.nbr_bf >= 1 && inv.cmds[0].type == TEE_RPC_BUFFER) {
		struct tee_shm_io shm = {0};
		shm.buffer = inv.cmds[0].buffer;
		shm.size   = inv.cmds[0].size;
		shm.flags  = TEEC_MEM_INPUT | TEEC_MEM_OUTPUT;
		if (ioctl(devfd, TEE_GET_FD_FOR_RPC_SHM_IOC, &shm) == 0 && shm.fd_shm >= 0) {
			void *map = mmap(NULL, shm.size, PROT_READ | PROT_WRITE,
					 MAP_SHARED, shm.fd_shm, 0);
			if (map != MAP_FAILED) {
				if (shm.size >= sizeof(struct tee_fs_rpc)) {
					struct tee_fs_rpc *fsrpc = map;
					void *payload = (uint8_t *)map + sizeof(*fsrpc);
					size_t cap = shm.size - sizeof(*fsrpc);
					fs_dispatch(s, fsrpc, payload, cap);  /* read-only */
					serviced = 1;
				}
				munmap(map, shm.size);
			}
			close(shm.fd_shm);
		}
	}
	/* C3: only claim RPC_OK if we actually ran the dispatcher; else leave the
	 * kernel's NOT_IMPLEMENTED so OP-TEE sees a clean failure. */
	if (serviced)
		inv.res = 0;
	/* C2: zero EVERY buffer pointer in the reply. The kernel's tee_supp_write
	 * does find_vma() on each cmds[n].buffer; a leftover TEE paddr there hits a
	 * BUG_ON under the insync mutex and hangs the TEE thread. The vendor zeroes
	 * cmds[0]; we zero all nbr_bf. */
	for (uint32_t i = 0; i < inv.nbr_bf && i < TEE_RPC_BUFFER_NUMBER; i++)
		inv.cmds[i].buffer = 0;
	/* Always reply, and with exactly the length the kernel handed us (< 136, so
	 * tee_supp_write accepts it). Never exit without replying -> no TEE hang. */
	if (write(devfd, &inv, (size_t)n) != n)
		return -1;
	return 1;
}

int main(int argc, char **argv)
{
	const char *disk = "/dev/mmcblk0";       /* mainline eMMC (sdhci); stock=mmcblk1 */
	for (int i = 1; i < argc; i++)
		if (!strcmp(argv[i], "--disk") && i + 1 < argc) disk = argv[++i];

	/* the legacy driver only lets a process named "tee-supplicant" register */
	prctl(PR_SET_NAME, "tee-supplicant", 0, 0, 0);

	struct rkss_ro s;
	if (open_security_ro(disk, &s) < 0)
		return 2;

	int devfd = open(OPTEE_DEV, O_RDWR | O_CLOEXEC);
	if (devfd < 0) { fprintf(stderr, "open %s: %s\n", OPTEE_DEV, strerror(errno)); return 3; }
	/* Defense in depth: refuse unless this is the TEE CHAR device, whose minor
	 * is dynamic. If a block node were named /dev/opteearmtz00, our O_RDWR write
	 * loop could corrupt it; require S_ISCHR and match /sys/class/misc/.../dev. */
	{
		struct stat ds; char line[32]; unsigned maj = 0, min = 0;
		int mf = open("/sys/class/misc/opteearmtz00/dev", O_RDONLY | O_CLOEXEC);
		if (fstat(devfd, &ds) != 0 || !S_ISCHR(ds.st_mode) || mf < 0) {
			fprintf(stderr, "%s is not the TEE char device -- refusing\n", OPTEE_DEV);
			close(devfd); close(s.fd); return 3;
		}
		ssize_t r = read(mf, line, sizeof(line) - 1); close(mf);
		if (r > 0) { line[r] = 0; sscanf(line, "%u:%u", &maj, &min); }
		if (makedev(maj, min) != ds.st_rdev) {
			fprintf(stderr, "%s rdev mismatch vs sysfs -- refusing\n", OPTEE_DEV);
			close(devfd); close(s.fd); return 3;
		}
	}

	for (;;) {
		int r = service_one_rpc(devfd, &s);
		if (r < 0) { if (errno == EINTR) continue; break; }
		/* r==0: EOF on the device; keep waiting is also valid. Exit on EOF. */
		if (r == 0) break;
	}
	close(devfd); close(s.fd);
	return 0;
}
