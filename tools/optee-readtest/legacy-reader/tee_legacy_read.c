/*
 * tee_legacy_read.c -- READ-ONLY legacy-ABI client for /dev/opteearmtz00.
 *
 * Reads the secure-storage object "lock_state" through the generic storage
 * pseudo-TA uboot_storedata_rpmb (UUID 1b484ea5-698b-4142-82b8-3acf16e99e2a),
 * command 0 (READ) ONLY. There is NO write path compiled into this binary:
 * command 1 (WRITE) is never referenced, so this tool cannot change lock_state.
 *
 * Unlike ucp10/tools/tee-unlock/tee_unlock_raw.c (which drives the *upstream*
 * /dev/tee0 uABI, magic 0xa4), this talks the *legacy* Rockchip ST ABI directly
 * via tee_ioc.h ('t'/0x74 ioctls): TEE_OPEN_SESSION_IOC (_IOWR('t',161)=0xa1)
 * and TEE_INVOKE_COMMAND_IOC (_IOWR('t',163)=0xa3). It is the drop-in reader
 * for the forward-ported drivers/tee/optee_legacy driver. The ioctl sequence it
 * emits (open /dev/opteearmtz00, 0xa1 on the device fd, 0xa3 on the session fd)
 * matches the vendor client traced on unit 3 (plan/uc-pxx/optee-m5-2026-11-10/
 * cli.trace) byte-for-byte; no TEE_ALLOC_SHM_IOC is needed because the legacy
 * kernel driver allocates the tmp SHM for TEMP memrefs itself.
 *
 * Build: static aarch64 ELF, see build.sh. Run as (or via) a user that can
 * open /dev/opteearmtz00 (world-rw on stock; root in the RAM installer).
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <errno.h>
#include <fcntl.h>
#include <unistd.h>
#include <sys/ioctl.h>

#include "tee_ioc.h"

#define TEE_DEV      "/dev/opteearmtz00"
#define CMD_READ     0u              /* uboot_storedata_rpmb READ. WRITE (1) is NOT used. */

/* UUID 1b484ea5-698b-4142-82b8-3acf16e99e2a */
static const TEEC_UUID storedata_uuid = {
	0x1b484ea5, 0x698b, 0x4142,
	{ 0x82, 0xb8, 0x3a, 0xcf, 0x16, 0xe9, 0x9e, 0x2a }
};

/* Object name incl. terminating NUL -> 11 bytes, exactly as U-Boot/tee_unlock. */
static char obj_name[] = "lock_state";

int main(void)
{
	int fd, sessfd, rc;
	struct tee_cmd_io cmd;
	TEEC_Operation op;
	uint8_t val = 0;

	fd = open(TEE_DEV, O_RDWR);
	if (fd < 0) {
		fprintf(stderr, "open %s: %s\n", TEE_DEV, strerror(errno));
		return 2;
	}

	/*
	 * OpenSession: public login + one VALUE_INPUT {a=0,b=0}, as U-Boot and
	 * tee_unlock.c do. The legacy kernel requires op != NULL and uuid != NULL
	 * for TEE_OPEN_SESSION_IOC; data must be NULL / data_size 0.
	 */
	memset(&op, 0, sizeof(op));
	op.paramTypes = TEEC_PARAM_TYPES(TEEC_VALUE_INPUT, TEEC_NONE,
					 TEEC_NONE, TEEC_NONE);
	/* op.params[0].value.{a,b} already 0 from memset */

	memset(&cmd, 0, sizeof(cmd));
	cmd.cmd     = 0;
	cmd.uuid    = (TEEC_UUID *)&storedata_uuid;
	cmd.op      = &op;
	cmd.fd_sess = 0;            /* <= 0 means "create a new session" */
	cmd.data    = NULL;
	cmd.data_size = 0;

	rc = ioctl(fd, TEE_OPEN_SESSION_IOC, &cmd);
	if (rc != 0 || cmd.err != TEEC_SUCCESS) {
		fprintf(stderr, "OPEN_SESSION: rc=%d err=0x%08x origin=%u errno=%s\n",
			rc, cmd.err, cmd.origin, strerror(errno));
		close(fd);
		return 3;
	}
	sessfd = cmd.fd_sess;       /* the anon-inode session fd the kernel returned */

	/*
	 * InvokeCommand READ (cmd 0) on the SESSION fd:
	 *   p0 = MEMREF_TEMP_INPUT  : object name incl NUL (11 bytes)
	 *   p1 = MEMREF_TEMP_INOUT  : the single data byte (receives lock_state)
	 * The kernel copies the tmprefs in/out; no SHM ioctl is issued here.
	 */
	memset(&op, 0, sizeof(op));
	op.paramTypes = TEEC_PARAM_TYPES(TEEC_MEMREF_TEMP_INPUT,
					 TEEC_MEMREF_TEMP_INOUT,
					 TEEC_NONE, TEEC_NONE);
	op.params[0].tmpref.buffer = obj_name;
	op.params[0].tmpref.size   = sizeof(obj_name);  /* 11 */
	op.params[1].tmpref.buffer = &val;
	op.params[1].tmpref.size   = 1;

	memset(&cmd, 0, sizeof(cmd));
	cmd.cmd     = CMD_READ;
	cmd.uuid    = NULL;                 /* invoke carries no UUID */
	cmd.op      = &op;
	cmd.fd_sess = sessfd;

	rc = ioctl(sessfd, TEE_INVOKE_COMMAND_IOC, &cmd);
	if (rc != 0 || cmd.err != TEEC_SUCCESS) {
		fprintf(stderr, "INVOKE(READ): rc=%d err=0x%08x origin=%u errno=%s\n",
			rc, cmd.err, cmd.origin, strerror(errno));
		close(sessfd);
		close(fd);
		return 4;
	}

	printf("lock_state = 0x%02x (%s)\n",
	       val, (val & 1) ? "UNLOCKED, bit0" : "LOCKED");

	close(sessfd);
	close(fd);
	return 0;
}
