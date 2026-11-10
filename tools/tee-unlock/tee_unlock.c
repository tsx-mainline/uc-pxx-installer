// SPDX-License-Identifier: GPL-2.0-or-later
/*
 * tee_unlock.c -- UC-P10 (RK3399) bootloader-unlock-bit client (libteec).
 *
 * Copyright (C) 2026 the tsx-mainline contributors
 *
 * This program is free software: you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation, either version 2 of the License, or (at your option)
 * any later version. It comes WITHOUT ANY WARRANTY. See the LICENSE file at
 * the repository root (GPL-2.0-or-later).
 *
 * ---------------------------------------------------------------------------
 * What it is
 *
 *   The authoritative device-unlock bit on this unit is the OP-TEE
 *   secure-storage object "lock_state". It is read/written through the generic
 *   storage pseudo-TA "uboot_storedata_rpmb"
 *   (UUID 1b484ea5-698b-4142-82b8-3acf16e99e2a), which does NO policy check and
 *   NO caller check. U-Boot reaches it from the normal world; rooted Android is
 *   also the normal world, so this client opens the same TA and reads or writes
 *   the object directly. On reboot, the userdebug U-Boot reads
 *   lock_state & 1 == 1 -> AVB "orange" -> boots unsigned, persistently. The
 *   stock "user" U-Boot ignores the object. "fastboot flashing unlock" writes a
 *   different, non-authoritative object and is a no-op for this bit.
 *
 * The contract (full proof: ucp10/docs/unlock-tee-analysis.md)
 *
 *   Pseudo-TA UUID : 1b484ea5-698b-4142-82b8-3acf16e99e2a
 *   OpenSession    : TEEC_LOGIN_PUBLIC, one VALUE_INPUT {a=0,b=0}
 *   command 0      : READ  named object -> 1 byte
 *   command 1      : WRITE named object <- 1 byte
 *   InvokeCommand params:
 *       p0 = MEMREF_TEMP_INPUT  = object name "lock_state" incl NUL (11 bytes)
 *       p1 = MEMREF_TEMP_INOUT  = the 1 data byte
 *   lock_state bit0: 0 = LOCKED, 1 = UNLOCKED.
 *
 * Subcommands:
 *   read    -> cmd 0, print the stored byte
 *   unlock  -> cmd 1 with 0x01, then read back
 *   lock    -> cmd 1 with 0x00, then read back (reversible re-lock)
 *
 * SAFETY: always `read` before and after a write and confirm the byte changed.
 * `lock` is the reverse of `unlock`; it MUST NOT be used on a unit that runs
 * mainline from a slot (a re-lock makes every unsigned slot fail verify). The
 * one-liner scripts and the installer page never offer `lock`.
 * ---------------------------------------------------------------------------
 *
 * BUILD: Android arm64, NDK r26d, linked against a STUB libteec built from the
 * public BSD optee_client headers (stub/ + include/tee_client_api.h). The stub
 * resolves the TEEC_* symbols at link time only; at run time the device's own
 * /vendor/lib64/libteec.so provides them (LD_LIBRARY_PATH=/vendor/lib64). The
 * device libteec is never used to build. See build.sh and SOURCES.md.
 */

#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <tee_client_api.h>

/* UUID 1b484ea5-698b-4142-82b8-3acf16e99e2a. libteec serialises this to the
 * 16 network-order bytes {1b,48,4e,a5,69,8b,41,42,82,b8,3a,cf,16,e9,9e,2a}. */
static const TEEC_UUID storedata_uuid = {
	0x1b484ea5, 0x698b, 0x4142,
	{ 0x82, 0xb8, 0x3a, 0xcf, 0x16, 0xe9, 0x9e, 0x2a }
};

#define CMD_READ  0u
#define CMD_WRITE 1u

/* The object name, NUL included. sizeof() == 11, which is the length U-Boot
 * passes for "lock_state". */
static char obj_name[] = "lock_state";

/*
 * Invoke the storedata PTA on object "lock_state".
 *   cmd == CMD_READ : *val receives the stored byte.
 *   cmd == CMD_WRITE: *val is written to the object.
 * Returns TEEC_SUCCESS or a TEEC_Result; sets *origin.
 */
static TEEC_Result invoke_obj(TEEC_Session *sess, uint32_t cmd,
			      uint8_t *val, uint32_t *origin)
{
	TEEC_Operation op;

	memset(&op, 0, sizeof(op));
	op.paramTypes = TEEC_PARAM_TYPES(TEEC_MEMREF_TEMP_INPUT,
					 TEEC_MEMREF_TEMP_INOUT,
					 TEEC_NONE, TEEC_NONE);
	/* p0: object name incl NUL */
	op.params[0].tmpref.buffer = obj_name;
	op.params[0].tmpref.size   = sizeof(obj_name); /* 11 */
	/* p1: the 1 data byte (in for WRITE, out for READ) */
	op.params[1].tmpref.buffer = val;
	op.params[1].tmpref.size   = 1;

	return TEEC_InvokeCommand(sess, cmd, &op, origin);
}

static void print_state(uint8_t v)
{
	printf("lock_state = 0x%02x (%s)\n",
	       v, (v & 1) ? "UNLOCKED, bit0" : "LOCKED");
}

int main(int argc, char **argv)
{
	TEEC_Context   ctx;
	TEEC_Session   sess;
	TEEC_Operation op;
	TEEC_Result    res;
	uint32_t       origin = 0;
	int            mode;     /* 0 read, 1 unlock, 2 lock */
	int            rc = 1;

	if (argc < 2) {
		fprintf(stderr, "usage: %s <read|unlock|lock>\n", argv[0]);
		return 2;
	}
	if      (!strcmp(argv[1], "read"))   mode = 0;
	else if (!strcmp(argv[1], "unlock")) mode = 1;
	else if (!strcmp(argv[1], "lock"))   mode = 2;
	else {
		fprintf(stderr, "usage: %s <read|unlock|lock>\n", argv[0]);
		return 2;
	}

	res = TEEC_InitializeContext(NULL, &ctx);
	if (res != TEEC_SUCCESS) {
		fprintf(stderr, "ERROR: TEEC_InitializeContext: 0x%08x\n", res);
		return 1;
	}

	/* OpenSession: public login + one VALUE_INPUT {0,0} (as U-Boot does). */
	memset(&op, 0, sizeof(op));
	op.paramTypes = TEEC_PARAM_TYPES(TEEC_VALUE_INPUT, TEEC_NONE,
					 TEEC_NONE, TEEC_NONE);
	op.params[0].value.a = 0;
	op.params[0].value.b = 0;

	res = TEEC_OpenSession(&ctx, &sess, &storedata_uuid,
			       TEEC_LOGIN_PUBLIC, NULL, &op, &origin);
	if (res != TEEC_SUCCESS) {
		fprintf(stderr, "ERROR: TEEC_OpenSession: 0x%08x origin=%u\n",
			res, origin);
		goto out_ctx;
	}

	if (mode != 0) {
		uint8_t wv = (mode == 1) ? 0x01 : 0x00;
		res = invoke_obj(&sess, CMD_WRITE, &wv, &origin);
		if (res != TEEC_SUCCESS) {
			fprintf(stderr, "ERROR: WRITE: 0x%08x origin=%u\n",
				res, origin);
			goto out_sess;
		}
		printf("wrote lock_state = 0x%02x (%s)\n",
		       wv, (mode == 1) ? "unlock" : "lock");
	}

	/* READ (always; it is the read-back after a write). 0xaa sentinel so a
	 * PTA that silently writes nothing is detectable. */
	{
		uint8_t rv = 0xaa;
		res = invoke_obj(&sess, CMD_READ, &rv, &origin);
		if (res != TEEC_SUCCESS) {
			fprintf(stderr, "ERROR: READ: 0x%08x origin=%u\n",
				res, origin);
			goto out_sess;
		}
		print_state(rv);
		rc = 0;
	}

out_sess:
	TEEC_CloseSession(&sess);
out_ctx:
	TEEC_FinalizeContext(&ctx);
	return rc;
}
