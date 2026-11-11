// SPDX-License-Identifier: GPL-2.0-or-later
/*
 * rkss_ro.c -- read-only RKSS v1 primitives. Faithful to U-Boot's
 * OpteeClientRkFs_v1.c read path (rkss_read_multi_sections,
 * rkss_read_patition_tables, rkss_get_fileinfo_by_name/by_index), with EVERY
 * write/backup/clean branch removed. The only syscall these make on the backend
 * is pread(). If the partition table fails verification, this does NOT "clean"
 * it (the vendor rewrites it -- we refuse and return an error: fail closed).
 */
#include <string.h>
#include <unistd.h>
#include "rkss_ro.h"

int rkss_ro_read_sections(const struct rkss_ro *s, unsigned long index,
			  unsigned int num, void *out)
{
	/* bounds: never read outside the partition (fail closed) */
	if (num == 0 || index + num > s->nsect || index + num < index)
		return -1;
	off_t off = s->base + (off_t)index * RKSS_SECTION_LEN;
	size_t len = (size_t)num * RKSS_SECTION_LEN;
	uint8_t *p = out;
	while (len) {
		ssize_t r = pread(s->fd, p, len, off);   /* READ ONLY */
		if (r <= 0)
			return -1;
		p += r; off += r; len -= (size_t)r;
	}
	return 0;
}

/* Read the 128 partition-table sections and verify each. READ ONLY; on a bad
 * section we FAIL (the vendor would rewrite -- we must not write). */
static int rkss_ro_read_ptables(const struct rkss_ro *s, uint8_t *table_data)
{
	if (rkss_ro_read_sections(s, 0, RKSS_PARTITION_TABLE_COUNT, table_data) < 0)
		return -1;
	for (int i = 0; i < RKSS_PARTITION_TABLE_COUNT; i++) {
		uint8_t *cp = table_data + (i * RKSS_SECTION_LEN);
		struct rkss_file_verification *v =
			(struct rkss_file_verification *)
			(cp + RKSS_SECTION_LEN - sizeof(struct rkss_file_verification));
		if (v->version != RKSS_VERSION_V1 || v->checkstr != RKSS_CHECK_STR)
			return -1;     /* fail closed: do NOT clean/rewrite */
	}
	return 0;
}

int rkss_ro_fileinfo_by_name(const struct rkss_ro *s, const char *name,
			     rkss_file_info *out)
{
	static uint8_t table_data[RKSS_SECTION_LEN * RKSS_PARTITION_TABLE_COUNT];
	size_t len = strlen(name);

	if (len > RKSS_MAX_NAME_LENGTH - 1)
		return -1;
	if (rkss_ro_read_ptables(s, table_data) < 0)
		return -1;

	for (int i = 0; i < RKSS_PARTITION_TABLE_COUNT; i++) {
		uint8_t *sec = table_data + (size_t)i * RKSS_SECTION_LEN;
		for (int n = 0; n < RKSS_EACH_SECTION_FILECOUNT; n++) {
			rkss_file_info *p = (rkss_file_info *)sec + n;
			if (p->used == 0)
				continue;
			if (!strcmp(p->name, name)) {
				memcpy(out, p, sizeof(*out));
				return i * RKSS_EACH_SECTION_FILECOUNT + n;
			}
			/* folder-prefix match, exactly as the vendor (read-only) */
			const char *split = "/";
			const char *li = name, *ls = p->name, *ci, *cs;
			int folder = 1;
			do {
				ci = strstr(li, split);
				cs = strstr(ls, split);
				int si = ci ? (int)(ci - li) : (int)strlen(li);
				int ss = cs ? (int)(cs - ls) : (int)strlen(ls);
				if (si != ss || memcmp(li, ls, si)) { folder = 0; break; }
				li = ci ? ci + 1 : li;
				ls = cs ? cs + 1 : ls;
			} while (ci && cs);
			if (folder)
				return -100;
		}
	}
	return -1;
}

int rkss_ro_fileinfo_by_index(const struct rkss_ro *s, int fd,
			      rkss_file_info *out)
{
	uint8_t sec[RKSS_SECTION_LEN];
	int i = fd / RKSS_EACH_SECTION_FILECOUNT;
	int n = fd - RKSS_EACH_SECTION_FILECOUNT * i;

	if (fd < 0 || i < 0 || i >= RKSS_PARTITION_TABLE_COUNT)
		return -1;
	if (rkss_ro_read_sections(s, i, 1, sec) < 0)
		return -1;
	rkss_file_info *p = (rkss_file_info *)sec + n;
	if (p->used != 1)
		return -1;
	memcpy(out, p, sizeof(*out));
	return 0;
}
