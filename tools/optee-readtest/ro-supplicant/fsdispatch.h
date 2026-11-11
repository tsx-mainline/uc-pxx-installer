/* SPDX-License-Identifier: GPL-2.0-or-later */
#ifndef FSDISPATCH_H
#define FSDISPATCH_H
#include <stddef.h>
#include "rkss_ro.h"

/* 1 if the supplicant must refuse this op at the gate, before any backend access:
 * WRITE, TRUNC, UNLINK, RENAME, LINK, MKDIR, RMDIR, and every OPEN whose flags are
 * not exactly RDONLY (0x1) or RDWR (0x4) (round 4; round 3 refused only OPEN with
 * CREAT or APPEND). The served handle is read-only. */
int fs_is_write_op(int op, int flags);

/* Service one tee_fs_rpc, read-only. Returns 0 if serviced (fsrpc->res set),
 * -1 if refused. Never writes the backend. `payload` is the SHM region after
 * the tee_fs_rpc header; `payload_cap` its length. */
int fs_dispatch(const struct rkss_ro *s, struct tee_fs_rpc *fsrpc,
		void *payload, size_t payload_cap);

/*
 * Logging build: the same dispatcher, which also says why it returned what it
 * returned. fs_dispatch(...) == fs_dispatch_why(..., NULL). `why` is only
 * written, never read, by the dispatcher: it cannot change a result.
 */
enum fs_decision {
	FSD_SERVED = 0,          /* the op ran; fsrpc->res is its answer (>= 0)        */
	FSD_REFUSED_WRITE_GATE,  /* fs_is_write_op(): refused before any backend access
	                          * (WRITE/TRUNC/UNLINK/RENAME/LINK/MKDIR/RMDIR, an OPEN whose flags
	                          * are not exactly RDONLY or RDWR) */
	FSD_REFUSED_UNSUPPORTED, /* OPENDIR/READDIR/CLOSEDIR/unknown op: refused        */
	FSD_NOT_FOUND,           /* the name or fd names no file (never created)        */
	FSD_DENIED,              /* ACCESS: the file lacks the asked R/W permission      */
	FSD_ERROR,               /* bad request (payload/len/seek) or a store/read error */
};

struct fs_why {
	int            decision;     /* enum fs_decision                              */
	const char    *detail;       /* a fixed text (never request or file data)     */
	int            have_info;    /* 1: `info` holds the file's table entry        */
	rkss_file_info info;         /* index/size/flags of the file (name not logged from here) */
	unsigned int   sections;     /* READ: sections read                           */
};

int fs_dispatch_why(const struct rkss_ro *s, struct tee_fs_rpc *fsrpc,
		    void *payload, size_t payload_cap, struct fs_why *why);

#endif
