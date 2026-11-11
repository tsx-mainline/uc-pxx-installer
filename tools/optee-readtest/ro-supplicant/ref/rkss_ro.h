/* SPDX-License-Identifier: GPL-2.0-or-later */
/*
 * rkss_ro.h -- shared ABI + read-only RKSS primitives for the read-only legacy
 * OP-TEE supplicant. The protocol numbers are the legacy Rockchip 4.4 driver's
 * (drivers/tee/optee_legacy, core/tee_supp_com.h) and U-Boot's RkFs v1
 * (rockchip-linux/u-boot lib/optee_clientApi/OpteeClientRkFs_v1.c, GPL-2.0+).
 * This file carries NO write/create/truncate/unlink/RPMB code at all.
 */
#ifndef RKSS_RO_H
#define RKSS_RO_H

#include <stdint.h>
#include <stddef.h>
#include <sys/types.h>

/* ---- device ioctl ABI (legacy, magic 't'/0x74) ---- */
#define TEE_GET_FD_FOR_RPC_SHM_IOC  0xc01874a7u   /* _IOWR('t',167,struct tee_shm_io) */

struct tee_shm_io {            /* must match optee_legacy include/linux/tee_ioc.h */
	union { void *buffer; uint64_t padding_buf; };
	uint32_t size;
	uint32_t flags;
	int      fd_shm;
	uint32_t registered;
};

/* ---- RPC message (tee_supp_com.h: struct tee_rpc_invoke, one buffer) ---- */
#define TEE_RPC_BUFFER_NUMBER 5
struct tee_rpc_cmd {
	union { void *buffer; uint64_t padding_buf; };
	uint32_t size;
	uint32_t type;        /* TEE_RPC_BUFFER=1 / TEE_RPC_VALUE=2 */
	int      fd;
	int      reserved;
};
struct tee_rpc_invoke {
	uint32_t cmd;         /* TEE_RPC_FS / TEE_RPC_RPMB_CMD / ... */
	uint32_t res;
	uint32_t nbr_bf;
	uint32_t reserved;
	struct tee_rpc_cmd cmds[TEE_RPC_BUFFER_NUMBER];
};
#define TEE_RPC_BUFFER 0x00000001u
#define TEE_RPC_VALUE  0x00000002u

/* top-level RPC command ids (tee_rpc.h). We ONLY ever service TEE_RPC_FS. */
#define TEE_RPC_LOAD_TA  0x10000001u
#define TEE_RPC_RPMB_CMD 0x1000000Au
#define TEE_RPC_FS       0x10000010u
#define TEE_RPC_GET_TIME 0x10000011u

/* ---- FS sub-protocol (OpteeClientRkFs.h) ---- */
struct tee_fs_rpc {           /* followed by a payload (name or data) in the SHM */
	int      op;
	int      flags;
	int      arg;
	int      fd;
	uint32_t len;
	int      res;
};
#define TEE_FS_OPEN     1
#define TEE_FS_CLOSE    2
#define TEE_FS_READ     3
#define TEE_FS_WRITE    4
#define TEE_FS_SEEK     5
#define TEE_FS_UNLINK   6
#define TEE_FS_RENAME   7
#define TEE_FS_TRUNC    8
#define TEE_FS_MKDIR    9
#define TEE_FS_OPENDIR  10
#define TEE_FS_CLOSEDIR 11
#define TEE_FS_READDIR  12
#define TEE_FS_RMDIR    13
#define TEE_FS_ACCESS   14
#define TEE_FS_LINK     15

#define TEE_FS_O_RDONLY 0x1
#define TEE_FS_O_WRONLY 0x2
#define TEE_FS_O_RDWR   0x4
#define TEE_FS_O_CREAT  0x8
#define TEE_FS_O_APPEND 0x20
#define TEE_FS_SEEK_SET 0x1
#define TEE_FS_SEEK_END 0x2
#define TEE_FS_SEEK_CUR 0x4
#define TEE_FS_R_OK     0x1
#define TEE_FS_W_OK     0x2

/* ---- RKSS v1 on-disk layout (OpteeClientRkFs_v1.c) ---- */
#define RKSS_SECTION_LEN          512
#define RKSS_PARTITION_TABLE_COUNT 128     /* sections [0..127]            */
#define RKSS_USED_FLAGS_INDEX     128      /* section [128]                */
#define RKSS_DATA_FIRST           129      /* data sections [129..1023]    */
#define RKSS_DATA_SECTION_COUNT   1024
#define RKSS_EACH_SECTION_FILECOUNT 4
#define RKSS_MAX_NAME_LENGTH      117
#define RKSS_VERSION_V1           1
#define RKSS_CHECK_STR            0x12345678u
#define RK_FS_R 0x1
#define RK_FS_W 0x2
#define RK_FS_D 0x8

typedef struct rkss_file_info {   /* 126 bytes */
	uint8_t  used;
	char     name[RKSS_MAX_NAME_LENGTH];
	uint16_t index;
	uint16_t size;
	uint16_t father;
	uint8_t  id;
	uint8_t  flags;
} rkss_file_info;

struct rkss_file_verification {   /* last 8 bytes of each ptable section */
	uint32_t version;
	uint32_t checkstr;
};

/*
 * The backend: a thing we can only READ from, section-addressed, partition
 * relative. In the real supplicant it is a pread() on the O_RDONLY security
 * partition fd; in the self-test it is a pread() on a partition image file.
 * There is deliberately no write entry point.
 */
struct rkss_ro {
	int   fd;            /* O_RDONLY */
	off_t base;          /* partition start byte (0 for a partition node)  */
	uint64_t nsect;      /* partition size in 512-B sections               */
};

/* Read `num` sections starting at `index` into `out`. Returns 0 / -1. READ ONLY. */
int rkss_ro_read_sections(const struct rkss_ro *s, unsigned long index,
			  unsigned int num, void *out);

/* Find a file info by name by scanning the 128-section partition table. READ ONLY.
 * Returns the fd (>=0), -100 for a folder match, -1 if not found. */
int rkss_ro_fileinfo_by_name(const struct rkss_ro *s, const char *name,
			     rkss_file_info *out);

/* Find a file info by fd (= section*4 + slot). READ ONLY. 0 / -1. */
int rkss_ro_fileinfo_by_index(const struct rkss_ro *s, int fd,
			      rkss_file_info *out);

#endif /* RKSS_RO_H */
