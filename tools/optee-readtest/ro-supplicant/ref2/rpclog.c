// SPDX-License-Identifier: GPL-2.0-or-later
/*
 * rpclog.c -- the bounded RPC log of the read-only supplicant (see rpclog.h).
 *
 * What it may touch:
 *   - fd 2 and the one inherited log fd (fstat: a regular file or a FIFO, fd >= 3),
 *     through the single call of write in sink_emit(); it opens nothing;
 *   - the request header (a copy), the rkss_ro_diag structure values, the file
 *     table entry's index/size/flags, and -- only for name ops -- the payload as a
 *     NUL-terminated name, at most `payload_cap` bytes, escaped.
 * It never reads a READ's data or a WRITE's payload, and it decides nothing:
 * fs_dispatch_logged() returns exactly what fs_dispatch_why() returned.
 */
#define _GNU_SOURCE
#include <errno.h>
#include <stdarg.h>
#include <stdio.h>
#include <string.h>
#include <time.h>
#include <unistd.h>
#include <sys/stat.h>

#include "rkss_ro.h"
#include "fsdispatch.h"
#include "rpclog.h"

#define LINE_MAX_BYTES 4096

static int           log_fd = -1;          /* the file sink (validated), or -1 */
static size_t        log_cap = RPCLOG_CAP_DEFAULT;
static size_t        sink_used[2];         /* [0] stderr, [1] the file */
static int           sink_full[2];
static unsigned long seq_counter;

static const char cap_note[] =
	"rpc log: the cap of this log is reached -- later lines are not written to it\n";

/* THE ONE call of write in this file: a whole buffer to fd 2 or to the validated log fd */
static void sink_emit(int fd, const char *buf, size_t len)
{
	while (len) {
		ssize_t r = write(fd, buf, len);
		if (r < 0 && errno == EINTR)
			continue;
		if (r <= 0)
			return;
		buf += r; len -= (size_t)r;
	}
}

static void sink_put(int which, const char *buf, size_t len)
{
	int fd = which == 0 ? 2 : log_fd;
	if (fd < 0 || sink_full[which])
		return;
	if (sink_used[which] + len > log_cap) {
		sink_full[which] = 1;
		sink_emit(fd, cap_note, sizeof(cap_note) - 1);
		sink_used[which] += sizeof(cap_note) - 1;
		return;
	}
	sink_emit(fd, buf, len);
	sink_used[which] += len;
}

/* format one line: "[   seconds.micros] text\n"; control characters become '?' */
static size_t format_line(char *out, size_t outsz, const char *fmt, va_list ap)
{
	struct timespec ts = {0, 0};
	clock_gettime(CLOCK_MONOTONIC, &ts);
	int h = snprintf(out, outsz, "[%5ld.%06ld] ", (long)ts.tv_sec, ts.tv_nsec / 1000L);
	if (h < 0) h = 0;
	if ((size_t)h >= outsz) h = (int)outsz - 1;
	int b = vsnprintf(out + h, outsz - (size_t)h, fmt, ap);
	size_t len = (size_t)h + (b < 0 ? 0 : (size_t)b);
	if (len > outsz - 2) {                        /* truncated: mark it */
		len = outsz - 2;
		memcpy(out + len - 12, " [truncated]", 12);
	}
	for (size_t i = (size_t)h; i < len; i++)
		if ((unsigned char)out[i] < 0x20 || out[i] == 0x7f)
			out[i] = '?';
	out[len++] = '\n';
	out[len] = 0;
	return len;
}

static void vline(int to_stderr, const char *fmt, va_list ap)
{
	char buf[LINE_MAX_BYTES];
	size_t len = format_line(buf, sizeof(buf), fmt, ap);
	if (to_stderr)
		sink_put(0, buf, len);
	sink_put(1, buf, len);
}

void rpclog_line(const char *fmt, ...)
{
	int e = errno;
	va_list ap;
	va_start(ap, fmt); vline(1, fmt, ap); va_end(ap);
	errno = e;
}

void rpclog_file_line(const char *fmt, ...)
{
	int e = errno;
	va_list ap;
	va_start(ap, fmt); vline(0, fmt, ap); va_end(ap);
	errno = e;
}

int rpclog_init(int file_fd, size_t cap)
{
	int e = errno;
	struct stat st;
	log_fd = -1;
	log_cap = cap ? cap : RPCLOG_CAP_DEFAULT;
	sink_used[0] = sink_used[1] = 0;
	sink_full[0] = sink_full[1] = 0;
	seq_counter = 0;
	if (file_fd >= 0) {
		if (file_fd < 3)
			rpclog_line("rpc log: fd %d refused (must be >= 3): stderr only", file_fd);
		else if (fstat(file_fd, &st) != 0)
			rpclog_line("rpc log: fd %d refused (not open: %s): stderr only", file_fd, strerror(errno));
		else if (!S_ISREG(st.st_mode) && !S_ISFIFO(st.st_mode))
			rpclog_line("rpc log: fd %d refused (mode 0%o: not a regular file or FIFO): stderr only",
				    file_fd, (unsigned)st.st_mode);
		else
			log_fd = file_fd;
	}
	rpclog_line("rpc log: tee-supplicant-ro logging build; sinks: stderr%s; cap %zu bytes per sink; "
		    "data bytes are never logged", log_fd >= 0 ? " + the log fd" : " only", log_cap);
	errno = e;
	return log_fd >= 0;
}

int rpclog_file_on(void) { return log_fd >= 0; }

unsigned long rpclog_seq_next(void) { return ++seq_counter; }

const char *rpclog_cmd_name(uint32_t cmd)
{
	switch (cmd) {
	case TEE_RPC_LOAD_TA:  return "LOAD_TA";
	case TEE_RPC_RPMB_CMD: return "RPMB_CMD";
	case TEE_RPC_FS:       return "FS";
	case TEE_RPC_GET_TIME: return "GET_TIME";
	default:               return "unknown";
	}
}

const char *rpclog_fsop_name(int op)
{
	static const char *const n[] = { "op0", "OPEN", "CLOSE", "READ", "WRITE", "SEEK", "UNLINK",
		"RENAME", "TRUNC", "MKDIR", "OPENDIR", "CLOSEDIR", "READDIR", "RMDIR", "ACCESS", "LINK" };
	if (op >= 0 && op < (int)(sizeof(n) / sizeof(n[0])))
		return n[op];
	return "unknown";
}

const char *rpclog_decision_name(int d)
{
	switch (d) {
	case FSD_SERVED:              return "SERVED";
	case FSD_REFUSED_WRITE_GATE:  return "REFUSED-WRITE-GATE";
	case FSD_REFUSED_UNSUPPORTED: return "REFUSED-UNSUPPORTED";
	case FSD_NOT_FOUND:           return "NOT-FOUND";
	case FSD_DENIED:              return "DENIED";
	case FSD_ERROR:               return "ERROR";
	default:                      return "?";
	}
}

int rpclog_fsop_names(int op)
{
	switch (op) {
	case TEE_FS_OPEN: case TEE_FS_ACCESS: case TEE_FS_UNLINK: case TEE_FS_MKDIR:
	case TEE_FS_RMDIR: case TEE_FS_OPENDIR:
		return 1;
	case TEE_FS_RENAME: case TEE_FS_LINK:
		return 2;
	default:              /* READ, WRITE, TRUNC, SEEK, CLOSE, READDIR, CLOSEDIR, unknown */
		return 0;
	}
}

void rpclog_sanitize(char *dst, size_t dstsz, const void *src, size_t srcmax)
{
	const unsigned char *p = src;
	size_t o = 0, i = 0;
	if (dstsz == 0)
		return;
	dst[0] = 0;
	if (srcmax == 0) {
		snprintf(dst, dstsz, "<no payload>");
		return;
	}
	for (; i < srcmax && p[i] && i < RPCLOG_NAME_MAX; i++) {
		unsigned char c = p[i];
		if (o + 5 >= dstsz)
			break;
		if (c >= 0x20 && c < 0x7f && c != '"' && c != '\\')
			dst[o++] = (char)c;
		else
			o += (size_t)snprintf(dst + o, dstsz - o, "\\x%02x", c);
	}
	dst[o] = 0;
	if (i < srcmax && p[i] && o + 1 < dstsz) {    /* longer than shown */
		size_t more = strnlen((const char *)p + i, srcmax - i);
		snprintf(dst + o, dstsz - o, "...(+%zu%s)", more, i + more >= srcmax ? ", no NUL" : "");
	} else if (i >= srcmax && o + 1 < dstsz) {     /* no NUL within the payload */
		snprintf(dst + o, dstsz - o, "<no NUL in %zu bytes>", srcmax);
	}
}

static void flags_text(char *b, size_t bs, int op, int flags)
{
	b[0] = 0;
	size_t o = 0;
#define ADD(bit, txt) do { if (flags & (bit)) o += (size_t)snprintf(b + o, o < bs ? bs - o : 0, "%s%s", o ? "|" : "", txt); } while (0)
	if (op == TEE_FS_OPEN) {
		ADD(TEE_FS_O_RDONLY, "RDONLY"); ADD(TEE_FS_O_WRONLY, "WRONLY"); ADD(TEE_FS_O_RDWR, "RDWR");
		ADD(TEE_FS_O_CREAT, "CREAT"); ADD(TEE_FS_O_APPEND, "APPEND");
		int other = flags & ~(TEE_FS_O_RDONLY | TEE_FS_O_WRONLY | TEE_FS_O_RDWR | TEE_FS_O_CREAT | TEE_FS_O_APPEND);
		if (other)
			o += (size_t)snprintf(b + o, o < bs ? bs - o : 0, "%sother 0x%x", o ? "|" : "", (unsigned)other);
		if (!flags)
			snprintf(b, bs, "none");
	} else if (op == TEE_FS_SEEK) {
		snprintf(b, bs, "%s", flags == TEE_FS_SEEK_SET ? "SEEK_SET" : flags == TEE_FS_SEEK_END ? "SEEK_END" :
			 flags == TEE_FS_SEEK_CUR ? "SEEK_CUR" : "unknown whence");
	} else if (op == TEE_FS_ACCESS) {
		ADD(TEE_FS_R_OK, "R_OK"); ADD(TEE_FS_W_OK, "W_OK");
		int other = flags & ~(TEE_FS_R_OK | TEE_FS_W_OK);
		if (other)
			o += (size_t)snprintf(b + o, o < bs ? bs - o : 0, "%sother 0x%x", o ? "|" : "", (unsigned)other);
		if (!flags)
			snprintf(b, bs, "F_OK");
	} else {
		snprintf(b, bs, "-");
	}
#undef ADD
}

/* the store's own diagnosis of the last lookup or read, structure values only */
static void store_text(char *b, size_t bs, const struct rkss_ro *s)
{
	const struct rkss_ro_diag *d = &rkss_ro_diag;
	switch (d->last) {
	case RKSS_E_BOUNDS:
		snprintf(b, bs, "; store: span of sections %lu+%u outside the partition (%llu sections): not read",
			 d->io_index, d->io_num, (unsigned long long)s->nsect);
		break;
	case RKSS_E_IO:
		snprintf(b, bs, "; store: pread of sections %lu+%u failed: %s", d->io_index, d->io_num,
			 d->io_errno ? strerror(d->io_errno) : "end of device");
		break;
	case RKSS_E_PTABLE_FORMAT:
		snprintf(b, bs, "; store: FORMAT CHECK FAILED (see the line above)");
		break;
	case RKSS_E_NAMELEN:
		snprintf(b, bs, "; store: name length %zu > %d (RKSS limit)", d->name_len, RKSS_MAX_NAME_LENGTH - 1);
		break;
	case RKSS_E_NOTFOUND:
		snprintf(b, bs, "; store: no used entry of that name (%d used entries in the table)", d->pt_used);
		break;
	case RKSS_E_FOLDER:
		snprintf(b, bs, "; store: a folder prefix of a used entry (lookup -100)");
		break;
	case RKSS_E_BADFD:
		snprintf(b, bs, "; store: fd outside the 128-section table");
		break;
	case RKSS_E_UNUSED:
		snprintf(b, bs, "; store: the fd's entry is not in use (used=%d)", d->entry_used);
		break;
	default:
		b[0] = 0;
	}
}

int fs_dispatch_logged(unsigned long seq, const struct rkss_ro *s,
		       struct tee_fs_rpc *fsrpc, void *payload, size_t payload_cap)
{
	int e0 = errno;
	const struct tee_fs_rpc req = *fsrpc;          /* the request as it came */
	char n1[RPCLOG_NAMEBUF], n2[RPCLOG_NAMEBUF];
	int names = rpclog_fsop_names(req.op);
	n1[0] = n2[0] = 0;
	if (names >= 1)
		rpclog_sanitize(n1, sizeof(n1), payload, payload_cap);
	if (names == 2) {
		size_t l = strnlen((const char *)payload, payload_cap);
		if (l + 1 < payload_cap)
			rpclog_sanitize(n2, sizeof(n2), (const char *)payload + l + 1, payload_cap - l - 1);
		else
			snprintf(n2, sizeof(n2), "<none>");
	}
	errno = e0;

	struct fs_why why;
	int rc = fs_dispatch_why(s, fsrpc, payload, payload_cap, &why);   /* the decision */
	int e1 = errno;

	char fl[96], st[256], info[160], rd[96], nm[2 * RPCLOG_NAMEBUF + 32];
	flags_text(fl, sizeof(fl), req.op, req.flags);
	store_text(st, sizeof(st), s);
	info[0] = rd[0] = nm[0] = 0;
	if (names == 1)
		snprintf(nm, sizeof(nm), " name=\"%s\"", n1);
	else if (names == 2)
		snprintf(nm, sizeof(nm), " name=\"%s\" new=\"%s\"", n1, n2);
	if (why.have_info && req.op == TEE_FS_ACCESS)
		snprintf(info, sizeof(info), "; file: index %u size %u flags 0x%x",
			 why.info.index, why.info.size, why.info.flags);
	else if (why.have_info)
		snprintf(info, sizeof(info), "; file: entry %d index %u size %u flags 0x%x",
			 req.op == TEE_FS_OPEN ? fsrpc->res : req.fd, why.info.index, why.info.size, why.info.flags);
	if (req.op == TEE_FS_READ && why.decision == FSD_SERVED)
		snprintf(rd, sizeof(rd), "; data reply %d bytes (%u sections read; bytes not logged)",
			 fsrpc->res, why.sections);

	if (rkss_ro_diag.last == RKSS_E_PTABLE_FORMAT)
		rpclog_line("#%lu RKSS FORMAT CHECK FAILED: partition-table section %d of %d has footer "
			    "version=0x%08x (want 0x%08x) checkstr=0x%08x (want 0x%08x); %d of %d sections fail; "
			    "the store is not cleaned (read only): every name lookup fails",
			    seq, rkss_ro_diag.pt_first_bad, RKSS_PARTITION_TABLE_COUNT, rkss_ro_diag.pt_version,
			    RKSS_VERSION_V1, rkss_ro_diag.pt_checkstr, RKSS_CHECK_STR, rkss_ro_diag.pt_bad,
			    RKSS_PARTITION_TABLE_COUNT);
	rpclog_line("#%lu FS %s(op %d) flags=0x%x(%s) arg=%d fd=%d len=%u cap=%zu%s -> %s: %s; res=%d rc=%d%s%s%s",
		    seq, rpclog_fsop_name(req.op), req.op, (unsigned)req.flags, fl, req.arg, req.fd, req.len,
		    payload_cap, nm, rpclog_decision_name(why.decision), why.detail ? why.detail : "-",
		    fsrpc->res, rc, info, rd, st);
	errno = e1;
	return rc;
}

void rpclog_rpc_done(unsigned long seq, const struct tee_rpc_invoke *inv, ssize_t n,
		     uint32_t res_in, int serviced, const char *why_not, int why_errno,
		     ssize_t wr, int wr_errno)
{
	int e = errno;
	char bufs[5 * 48 + 8];
	size_t o = 0;
	bufs[0] = 0;
	for (uint32_t i = 0; i < inv->nbr_bf && i < TEE_RPC_BUFFER_NUMBER && o < sizeof(bufs); i++)
		o += (size_t)snprintf(bufs + o, sizeof(bufs) - o, " buf%u{type %u size %u}", i,
				      inv->cmds[i].type, inv->cmds[i].size);
	char wn[192];
	if (serviced)
		snprintf(wn, sizeof(wn), "SERVED (the FS dispatcher ran)");
	else if (why_errno)
		snprintf(wn, sizeof(wn), "NOT SERVED: %s (%s)", why_not ? why_not : "-", strerror(why_errno));
	else
		snprintf(wn, sizeof(wn), "NOT SERVED: %s", why_not ? why_not : "-");
	char ws[96];
	if (wr == n)
		snprintf(ws, sizeof(ws), "reply %zd bytes", wr);
	else
		snprintf(ws, sizeof(ws), "reply write FAILED (%zd of %zd bytes: %s)", wr, n,
			 wr < 0 ? strerror(wr_errno) : "short");
	rpclog_line("#%lu RPC cmd=0x%08x(%s) nbr_bf=%u%s%s res_in=0x%08x -> %s; %s, res=0x%08x",
		    seq, inv->cmd, rpclog_cmd_name(inv->cmd), inv->nbr_bf, bufs,
		    n < (ssize_t)sizeof(*inv) ? " (short read)" : "", res_in, wn, ws, inv->res);
	errno = e;
}
