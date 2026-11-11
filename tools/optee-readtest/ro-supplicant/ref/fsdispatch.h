/* SPDX-License-Identifier: GPL-2.0-or-later */
#ifndef FSDISPATCH_H
#define FSDISPATCH_H
#include <stddef.h>
#include "rkss_ro.h"

/* 1 if this op would mutate the store (so the supplicant must refuse it). */
int fs_is_write_op(int op, int flags);

/* Service one tee_fs_rpc, read-only. Returns 0 if serviced (fsrpc->res set),
 * -1 if refused. Never writes the backend. `payload` is the SHM region after
 * the tee_fs_rpc header; `payload_cap` its length. */
int fs_dispatch(const struct rkss_ro *s, struct tee_fs_rpc *fsrpc,
		void *payload, size_t payload_cap);

#endif
