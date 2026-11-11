//go:build install && step2

package step2

// sysSyncfs is syncfs(2) (the syscall package of amd64 has no SYS_SYNCFS).
const sysSyncfs = 306
