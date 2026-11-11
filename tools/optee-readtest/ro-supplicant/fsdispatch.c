// SPDX-License-Identifier: GPL-2.0-or-later
/*
 * fsdispatch.c -- the read-only, FAIL-CLOSED FS-RPC dispatcher. This is the one
 * place that interprets a tee_fs_rpc. It services ONLY the operations a
 * lock_state READ needs (OPEN of an existing file without create/append, CLOSE,
 * READ, SEEK, ACCESS); every write-type op (WRITE, TRUNC, UNLINK, RENAME, LINK,
 * MKDIR, RMDIR, create-open, append-open) and every unknown op returns an error
 * and touches no block device. It is driven identically by the real device loop (supplicant.c) and
 * by the offline self-test (selftest.c), so the test exercises the exact code
 * that would run on the unit.
 *
 * Returns 0 if the RPC was serviced (fsrpc->res carries the FS result, which may
 * itself be -1), or -1 if the op is refused outright. Either way: no writes.
 *
 * Logging build: fs_dispatch_why() is the reviewed dispatcher (ref/fsdispatch.c)
 * with one WHY() before each return, which fills the caller's `why` (if any)
 * with the decision and a fixed text.
 *
 * Round 3 (OPTEE-READTEST-3-2026-11-11.md): the one policy change vs round 2
 * (ref2/, binary 9200e2f0...). OP-TEE opens an object's meta file with
 * TEE_FS_O_RDWR even for a READ (unit-2 run 2312). An OPEN of an EXISTING file is
 * now served whatever its access-mode bits (RDONLY/WRONLY/RDWR): the handle is
 * only a table index, this supplicant has no write path at all (the backend fd is
 * O_RDONLY and the only backend call is pread), and every WRITE/TRUNC/... on the
 * handle is still refused by the gate below before any backend access. OPEN with
 * CREAT or APPEND stays refused; an OPEN of a missing file stays "not found" (never
 * created). build.sh's rpcequiv proves every other RPC identical to ref2/ and ref/.
 *
 * Round 4 (the LS supplicant, MAINLINE-RC2-LS-2026-11-11.md; OPTEE-READTEST-3-REVIEW
 * MINOR-2): the one policy change vs round 3 (ref3/, binary afaf1778...). An OPEN is
 * served only when its flags are exactly RDONLY (0x1) or RDWR (0x4), the two values the
 * sealed round-2/3 RPC logs show this OP-TEE using; every other flag value is refused
 * by the gate before any backend access. build.sh's rpcequiv proves every other RPC
 * identical to ref3/.
 */
#include <string.h>
#include "rkss_ro.h"
#include "fsdispatch.h"

/* mirrors the vendor's single static seek cursor */
static int fs_file_seek;

#define WHY(d, t) do { if (why) { why->decision = (d); why->detail = (t); } } while (0)
#define WHY_INFO(pp) do { if (why) { why->info = (pp); why->have_info = 1; } } while (0)

/* the decision for a failed name/fd lookup, from the store's own diagnosis */
static int lookup_decision(void)
{
	switch (rkss_ro_diag.last) {
	case RKSS_E_NOTFOUND: case RKSS_E_FOLDER: case RKSS_E_UNUSED: case RKSS_E_BADFD:
		return FSD_NOT_FOUND;
	default:                 /* PTABLE_FORMAT, NAMELEN, IO, BOUNDS */
		return FSD_ERROR;
	}
}

int fs_is_write_op(int op, int flags)
{
	switch (op) {
	case TEE_FS_WRITE: case TEE_FS_TRUNC: case TEE_FS_UNLINK:
	case TEE_FS_RENAME: case TEE_FS_LINK: case TEE_FS_MKDIR:
	case TEE_FS_RMDIR:
		return 1;
	case TEE_FS_OPEN:
		/* Round 4 (OPTEE-READTEST-3-REVIEW MINOR-2): an OPEN is served
		 * only with flags EXACTLY TEE_FS_O_RDONLY (0x1) or TEE_FS_O_RDWR
		 * (0x4), the only two values this OP-TEE sent for the lock_state
		 * READ (the sealed supplicant-rpc.log of unit-2 rounds 2 and 3:
		 * RDWR for the meta file, RDONLY for the block file). Every other
		 * value is refused here, before any backend access: CREAT and
		 * APPEND (a store mutation), WRONLY, any combination of access-mode
		 * bits, no mode bit, and every other bit (0x10, 0x40, 0x80, high
		 * bits), which this read-only supplicant does not emulate. The
		 * served handle is read-only as in round 3. */
		return (flags == TEE_FS_O_RDONLY || flags == TEE_FS_O_RDWR) ? 0 : 1;
	default:
		return 0;
	}
}

int fs_dispatch(const struct rkss_ro *s, struct tee_fs_rpc *fsrpc,
		void *payload, size_t payload_cap)
{
	return fs_dispatch_why(s, fsrpc, payload, payload_cap, NULL);
}

int fs_dispatch_why(const struct rkss_ro *s, struct tee_fs_rpc *fsrpc,
		    void *payload, size_t payload_cap, struct fs_why *why)
{
	if (why)
		memset(why, 0, sizeof(*why));
	rkss_ro_diag_reset();

	/* Hard gate: any write-type op is refused before any backend access. */
	if (fs_is_write_op(fsrpc->op, fsrpc->flags)) {
		fsrpc->res = -1;
		WHY(FSD_REFUSED_WRITE_GATE, fsrpc->op == TEE_FS_OPEN ?
		    "OPEN with flags other than exactly RDONLY (0x1) or RDWR (0x4)" :
		    "a write-type op (WRITE/TRUNC/UNLINK/RENAME/LINK/MKDIR/RMDIR)");
		return -1;
	}

	switch (fsrpc->op) {
	case TEE_FS_OPEN: {
		rkss_file_info p;
		char *name = (char *)payload;
		int fd;
		if (strnlen(name, payload_cap) >= payload_cap) {
			fsrpc->res = -1;
			WHY(FSD_ERROR, "the name is not NUL-terminated within the payload");
			return 0;
		}
		fd = rkss_ro_fileinfo_by_name(s, name, &p);
		if (fd < 0) {    /* not found; never create */
			fsrpc->res = -1;
			WHY(lookup_decision(), "no openable file of that name (never created)");
			return 0;
		}
		fsrpc->fd = fd;
		fs_file_seek = 0;
		fsrpc->res = fd;
		WHY(FSD_SERVED, "opened read-only (any access mode; the handle never writes)");
		WHY_INFO(p);
		return 0;
	}
	case TEE_FS_CLOSE:
		fsrpc->res = 0;
		WHY(FSD_SERVED, "closed (nothing to release)");
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
		if (rkss_ro_fileinfo_by_index(s, fsrpc->fd, &p) < 0) {
			fsrpc->res = -1;
			WHY(lookup_decision(), "the fd names no file in use");
			return 0;
		}
		WHY_INFO(p);
		if (fsrpc->len > payload_cap) {
			fsrpc->res = -1;
			WHY(FSD_ERROR, "len is larger than the payload");
			return 0;
		}
		num = fsrpc->len / RKSS_SECTION_LEN + 1;
		if ((size_t)num * RKSS_SECTION_LEN > sizeof(scratch)) {
			fsrpc->res = -1;
			WHY(FSD_ERROR, "the section span is larger than the 64 KiB scratch");
			return 0;
		}
		if (rkss_ro_read_sections(s, p.index, num, scratch) < 0) {
			fsrpc->res = -1;
			WHY(FSD_ERROR, "the section read failed");
			return 0;
		}
		di = fsrpc->len > p.size ? p.size : fsrpc->len;
		memcpy(payload, scratch, di);
		fsrpc->res = (int)di;
		WHY(FSD_SERVED, "read min(len, file size) bytes");
		if (why) why->sections = num;
		return 0;
	}
	case TEE_FS_SEEK: {
		if (fsrpc->flags == TEE_FS_SEEK_CUR) {
			fsrpc->res = fs_file_seek + fsrpc->arg;
			WHY(FSD_SERVED, "SEEK_CUR (the cursor is not moved, as the vendor)");
		} else if (fsrpc->flags == TEE_FS_SEEK_SET) {
			fs_file_seek = fsrpc->arg; fsrpc->res = fs_file_seek;
			WHY(FSD_SERVED, "SEEK_SET");
		} else if (fsrpc->flags == TEE_FS_SEEK_END) {
			rkss_file_info p;
			if (rkss_ro_fileinfo_by_index(s, fsrpc->fd, &p) < 0) {
				fsrpc->res = -1;
				WHY(lookup_decision(), "SEEK_END: the fd names no file in use");
				return 0;
			}
			fs_file_seek = p.size + fsrpc->arg; fsrpc->res = fs_file_seek;
			WHY(FSD_SERVED, "SEEK_END");
			WHY_INFO(p);
		} else {
			fsrpc->res = -1;
			WHY(FSD_ERROR, "an unknown seek whence");
		}
		return 0;
	}
	case TEE_FS_ACCESS: {
		rkss_file_info p = {0};          /* zero: folder match (-100) leaves p unset */
		char *name = (char *)payload;
		if (strnlen(name, payload_cap) >= payload_cap) {
			fsrpc->res = -1;
			WHY(FSD_ERROR, "the name is not NUL-terminated within the payload");
			return 0;
		}
		int r = rkss_ro_fileinfo_by_name(s, name, &p);
		if (r < 0 && r != -100) {
			fsrpc->res = -1;
			WHY(lookup_decision(), "no file or folder of that name");
			return 0;
		}
		if (r >= 0)
			WHY_INFO(p);
		if ((fsrpc->flags & TEE_FS_R_OK) && !(p.flags & RK_FS_R)) {
			fsrpc->res = -1;
			WHY(FSD_DENIED, r == -100 ? "R_OK asked: a folder match has no R flag" :
			    "R_OK asked: the file has no R flag");
			return 0;
		}
		/* W_OK is a query only -- we answer it, we never make it writable */
		if ((fsrpc->flags & TEE_FS_W_OK) && !(p.flags & RK_FS_W)) {
			fsrpc->res = -1;
			WHY(FSD_DENIED, r == -100 ? "W_OK asked: a folder match has no W flag" :
			    "W_OK asked: the file has no W flag");
			return 0;
		}
		fsrpc->res = 0;
		WHY(FSD_SERVED, r == -100 ? "exists (a folder match)" : "exists, asked permissions present");
		return 0;
	}
	default:
		/* OPENDIR/READDIR/CLOSEDIR and anything else: refuse (fail closed) */
		fsrpc->res = -1;
		WHY(FSD_REFUSED_UNSUPPORTED, "not served by this read-only supplicant (OPENDIR/READDIR/CLOSEDIR/unknown)");
		return -1;
	}
}
