//go:build install && step2

package step2

import "syscall"

// sysSyncfs is syncfs(2).
const sysSyncfs = syscall.SYS_SYNCFS
