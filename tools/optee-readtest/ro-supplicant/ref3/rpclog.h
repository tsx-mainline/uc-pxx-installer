/* SPDX-License-Identifier: GPL-2.0-or-later */
/*
 * rpclog.h -- the RPC log of the read-only supplicant (logging build).
 *
 * One line per RPC (its type, the decision, the reply length) and, for an FS
 * RPC, one more line (the FS op, flags, the name for name ops, fd/arg/len, the
 * decision, the result). NEVER a data byte: the payload is read only as a name,
 * and only for the ops whose payload is a name (OPEN, ACCESS, UNLINK, RENAME,
 * LINK, MKDIR, RMDIR, OPENDIR); a READ's data and a WRITE's payload are never
 * looked at.
 *
 * Sinks: stderr (fd 2) and, if given, one inherited file descriptor that is a
 * regular file or a FIFO (the caller's shell opens it; this code opens no file
 * at all). Each sink is bounded: at most `cap` bytes plus one "cap reached"
 * line. Every function keeps errno as it found it.
 */
#ifndef RPCLOG_H
#define RPCLOG_H
#include <stddef.h>
#include <stdint.h>
#include <sys/types.h>
#include "rkss_ro.h"

#define RPCLOG_CAP_DEFAULT (1024u * 1024u)   /* bytes per sink */
#define RPCLOG_NAME_MAX    256u              /* name bytes shown (then "...(+N)") */
#define RPCLOG_NAMEBUF     (RPCLOG_NAME_MAX * 4u + 64u)

/* fd < 0: stderr only. Returns 1 if the file sink is on, 0 if not (refused or none). */
int  rpclog_init(int file_fd, size_t cap);
int  rpclog_file_on(void);
unsigned long rpclog_seq_next(void);

/* a line to both sinks / to the file sink only (printf format, one line) */
void rpclog_line(const char *fmt, ...) __attribute__((format(printf, 1, 2)));
void rpclog_file_line(const char *fmt, ...) __attribute__((format(printf, 1, 2)));

/* fs_dispatch_why() (unchanged decisions) plus the FS log line; returns its rc */
int  fs_dispatch_logged(unsigned long seq, const struct rkss_ro *s,
			struct tee_fs_rpc *fsrpc, void *payload, size_t payload_cap);

/* the RPC line, after the reply was written */
void rpclog_rpc_done(unsigned long seq, const struct tee_rpc_invoke *inv, ssize_t n,
		     uint32_t res_in, int serviced, const char *why_not, int why_errno,
		     ssize_t wr, int wr_errno);

/* helpers (also for the self-test) */
const char *rpclog_cmd_name(uint32_t cmd);
const char *rpclog_fsop_name(int op);
const char *rpclog_decision_name(int decision);
/* number of names in the payload of `op` (0, 1 or 2): only these are ever read */
int  rpclog_fsop_names(int op);
/* escape at most `srcmax` bytes of src up to its NUL into dst (always NUL-terminated) */
void rpclog_sanitize(char *dst, size_t dstsz, const void *src, size_t srcmax);

#endif
