package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"uc-pxx-installer/internal/ui"
)

// cmdKeytest is the read-only key test of a status run: it reads the touch
// keys from the input device of the maXTouch controller and prints the code
// of each press. It opens nothing for writing.
func cmdKeytest(args []string) {
	fs := flag.NewFlagSet("keytest", flag.ExitOnError)
	root := fs.String("root", "/", "the root of the sysfs and /dev to look in")
	secs := fs.Int("seconds", 60, "how long to wait for the six presses")
	fs.Parse(args)
	dev := ui.FindTouch(*root)
	if dev == "" {
		fail(2, "keytest: no maXTouch input device")
	}
	fmt.Printf("keytest: %s\n", dev)
	ch := make(chan uint16, 16)
	errc := make(chan error, 1)
	go func() { errc <- ui.ReadPresses(dev, ch) }()
	done := make(chan bool, 1)
	go func() {
		done <- ui.KeyTest(ch, time.Duration(*secs)*time.Second, func(s string) { fmt.Println(s) })
	}()
	select {
	case ok := <-done:
		if !ok {
			os.Exit(1)
		}
	case err := <-errc:
		fail(2, "keytest: %s: %v", dev, err)
	}
}
