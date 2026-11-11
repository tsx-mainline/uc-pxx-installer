// SPDX-License-Identifier: GPL-2.0-or-later
/*
 * fsdispatch.c -- the read-only, FAIL-CLOSED FS-RPC dispatcher. This is the one
 * place that interprets a tee_fs_rpc. It services ONLY the operations a
 * lock_state READ needs (OPEN without create, CLOSE, READ, SEEK, ACCESS); every
 * write-type op (WRITE, TRUNC, UNLINK, RENAME, LINK, MKDIR, RMDIR, create-open,
 * append-open) and every unknown op returns an error and touches no block
 * device. It is driven identically by the real device loop (supplicant.c) and
 * by the offline self-test (selftest.c), so the test exercises the exact code
 * that would run on the unit.
 *
 * Returns 0 if the RPC was serviced (fsrpc->res carries the FS result, which may
 * itself be -1), or -1 if the op is refused outright. Either way: no writes.
 */
#include <string.h>
#include "rkss_ro.h"
#include "fsdispatch.h"

/* mirrors the vendor's single static seek cursor */
static int fs_file_seek;

int fs_is_write_op(int op, int flags)
{
	switch (op) {
	case TEE_FS_WRITE: case TEE_FS_TRUNC: case TEE_FS_UNLINK:
	case TEE_FS_RENAME: case TEE_FS_LINK: case TEE_FS_MKDIR:
	case TEE_FS_RMDIR:
		return 1;
	case TEE_FS_OPEN:
		/* create / write / append all imply a store mutation */
		return (flags & (TEE_FS_O_CREAT | TEE_FS_O_WRONLY |
				 TEE_FS_O_RDWR | TEE_FS_O_APPEND)) ? 1 : 0;
	default:
		return 0;
	}
}

int fs_dispatch(const struct rkss_ro *s, struct tee_fs_rpc *fsrpc,
		void *payload, size_t payload_cap)
{
	/* Hard gate: any write-type op is refused before any backend access. */
	if (fs_is_write_op(fsrpc->op, fsrpc->flags)) {
		fsrpc->res = -1;
		return -1;
	}

	switch (fsrpc->op) {
	case TEE_FS_OPEN: {
		rkss_file_info p;
		char *name = (char *)payload;
		int fd;
		if (strnlen(name, payload_cap) >= payload_cap) { fsrpc->res = -1; return 0; }
		fd = rkss_ro_fileinfo_by_name(s, name, &p);
		if (fd < 0) { fsrpc->res = -1; return 0; }   /* not found; never create */
		fsrpc->fd = fd;
		fs_file_seek = 0;
		fsrpc->res = fd;
		return 0;
	}
	case TEE_FS_CLOSE:
		fsrpc->res = 0;
		return 0;
	case TEE_FS_READ: {
		/*
		 * The shared buffer is header + len, so payload_cap == len. The
		 * vendor reads the whole section span (num = len/512 + 1) into a
		 * scratch, then copies di = min(len, file_size) into the caller's
		 * buffer. Reading num*512 straight into payload (cap == len) would
		 * always overflow, so use a bounded scratch. (C1 fix.)
		 */
		static uint8_t scratch[128 * RKSS_SECTION_LEN];   /* >= any observed read */
		rkss_file_info p;
		unsigned int num, di;
		if (rkss_ro_fileinfo_by_index(s, fsrpc->fd, &p) < 0) { fsrpc->res = -1; return 0; }
		if (fsrpc->len > payload_cap) { fsrpc->res = -1; return 0; }
		num = fsrpc->len / RKSS_SECTION_LEN + 1;
		if ((size_t)num * RKSS_SECTION_LEN > sizeof(scratch)) { fsrpc->res = -1; return 0; }
		if (rkss_ro_read_sections(s, p.index, num, scratch) < 0) { fsrpc->res = -1; return 0; }
		di = fsrpc->len > p.size ? p.size : fsrpc->len;
		memcpy(payload, scratch, di);
		fsrpc->res = (int)di;
		return 0;
	}
	case TEE_FS_SEEK: {
		if (fsrpc->flags == TEE_FS_SEEK_CUR) {
			fsrpc->res = fs_file_seek + fsrpc->arg;
		} else if (fsrpc->flags == TEE_FS_SEEK_SET) {
			fs_file_seek = fsrpc->arg; fsrpc->res = fs_file_seek;
		} else if (fsrpc->flags == TEE_FS_SEEK_END) {
			rkss_file_info p;
			if (rkss_ro_fileinfo_by_index(s, fsrpc->fd, &p) < 0) { fsrpc->res = -1; return 0; }
			fs_file_seek = p.size + fsrpc->arg; fsrpc->res = fs_file_seek;
		} else { fsrpc->res = -1; }
		return 0;
	}
	case TEE_FS_ACCESS: {
		rkss_file_info p = {0};          /* zero: folder match (-100) leaves p unset */
		char *name = (char *)payload;
		if (strnlen(name, payload_cap) >= payload_cap) { fsrpc->res = -1; return 0; }
		int r = rkss_ro_fileinfo_by_name(s, name, &p);
		if (r < 0 && r != -100) { fsrpc->res = -1; return 0; }
		if ((fsrpc->flags & TEE_FS_R_OK) && !(p.flags & RK_FS_R)) { fsrpc->res = -1; return 0; }
		/* W_OK is a query only -- we answer it, we never make it writable */
		if ((fsrpc->flags & TEE_FS_W_OK) && !(p.flags & RK_FS_W)) { fsrpc->res = -1; return 0; }
		fsrpc->res = 0;
		return 0;
	}
	default:
		/* OPENDIR/READDIR/CLOSEDIR and anything else: refuse (fail closed) */
		fsrpc->res = -1;
		return -1;
	}
}
