// SPDX-License-Identifier: GPL-2.0-or-later
/*
 * libteec_stub.c -- a link-only STUB of the GlobalPlatform TEE Client API.
 *
 * Copyright (C) 2026 the tsx-mainline contributors. GPL-2.0-or-later.
 *
 * This file exists ONLY to satisfy the dynamic linker at build time: it is
 * compiled into a shared object with SONAME "libteec.so" that exports the few
 * TEEC_* symbols tee_unlock.c imports. The built client is linked against this
 * stub and NEVER against the device's /vendor/lib64/libteec.so. At run time on
 * the unit the device's real libteec.so (reached through
 * LD_LIBRARY_PATH=/vendor/lib64) provides these symbols; this stub is not
 * shipped and is not loaded on the device.
 *
 * Because the client imports the TEEC_* symbols by name with no symbol version
 * (the device libteec exports them unversioned), the stub exports them the same
 * way, so the client's import table is identical to a build linked against the
 * device library -- without ever reading or shipping that library.
 *
 * The bodies are deliberately inert: if this .so were ever loaded (it must not
 * be), every call fails closed with TEEC_ERROR_NOT_IMPLEMENTED and the client
 * stops. Never run the client against this stub.
 */

#include <tee_client_api.h>

TEEC_Result TEEC_InitializeContext(const char *name, TEEC_Context *ctx)
{
	(void)name;
	(void)ctx;
	return TEEC_ERROR_NOT_IMPLEMENTED;
}

void TEEC_FinalizeContext(TEEC_Context *ctx)
{
	(void)ctx;
}

TEEC_Result TEEC_OpenSession(TEEC_Context *ctx, TEEC_Session *session,
			     const TEEC_UUID *destination,
			     uint32_t connectionMethod,
			     const void *connectionData,
			     TEEC_Operation *operation,
			     uint32_t *returnOrigin)
{
	(void)ctx;
	(void)session;
	(void)destination;
	(void)connectionMethod;
	(void)connectionData;
	(void)operation;
	if (returnOrigin)
		*returnOrigin = TEEC_ORIGIN_API;
	return TEEC_ERROR_NOT_IMPLEMENTED;
}

void TEEC_CloseSession(TEEC_Session *session)
{
	(void)session;
}

TEEC_Result TEEC_InvokeCommand(TEEC_Session *session, uint32_t commandID,
			       TEEC_Operation *operation,
			       uint32_t *returnOrigin)
{
	(void)session;
	(void)commandID;
	(void)operation;
	if (returnOrigin)
		*returnOrigin = TEEC_ORIGIN_API;
	return TEEC_ERROR_NOT_IMPLEMENTED;
}
