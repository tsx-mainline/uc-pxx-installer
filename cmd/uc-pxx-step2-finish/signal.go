//go:build install && step2 && finish

package main

import (
	"os"
	ossignal "os/signal"
)

func ignore(s []os.Signal) { ossignal.Ignore(s...) }
