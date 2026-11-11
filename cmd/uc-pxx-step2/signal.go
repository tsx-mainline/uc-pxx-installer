//go:build install && step2

package main

import (
	"os"
	ossignal "os/signal"
)

func ignore(s []os.Signal) { ossignal.Ignore(s...) }
func reset(s []os.Signal)  { ossignal.Reset(s...) }
