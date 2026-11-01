package ui

import (
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"time"
)

// KeyRow are the touch keys from the left, with the code that each one must
// give (the stock key maps of both models: UC-P10 "linux,gpio-keymap_p10",
// UC-P8 "linux,gpio-keymap", in the order of the keys under the glass;
// uc-pxx-linux docs/hardware.md, UC-P8.md §1.2 F2).
var KeyRow = []struct {
	Code uint16
	Name string
}{
	{codeProg1, "Teams"}, {codeProg2, "Headset"}, {codeProg3, "Speaker"},
	{codeVolDown, "Vol-"}, {codeVolUp, "Vol+"}, {codeMute, "Mic"},
}

// CodeLabel names a key code: "148 Teams (place 1)", or "N (not a key of
// the row)".
func CodeLabel(code uint16) string {
	for i, k := range KeyRow {
		if k.Code == code {
			return fmt.Sprintf("%d %s (place %d)", code, k.Name, i+1)
		}
	}
	return fmt.Sprintf("%d (not a key of the row)", code)
}

// ReadPresses sends the code of each key press (value 1) of dev to ch until
// a read fails. It opens dev read-only.
func ReadPresses(dev string, ch chan<- uint16) error {
	f, err := os.Open(dev)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, eventSize*16)
	for {
		n, err := f.Read(buf)
		if err != nil {
			return err
		}
		for off := 0; off+eventSize <= n; off += eventSize {
			e := buf[off : off+eventSize]
			if binary.LittleEndian.Uint16(e[16:]) == evKey && binary.LittleEndian.Uint32(e[20:]) == 1 {
				ch <- binary.LittleEndian.Uint16(e[18:])
			}
		}
	}
}

// KeyTest is the key test of a status run (UC-P8.md §7, step S2): the
// person presses each touch key once, from the left. It reports every
// press and whether the six codes came in the order of KeyRow. It only
// reads the input device.
func KeyTest(presses <-chan uint16, timeout time.Duration, say func(string)) bool {
	var names []string
	for _, k := range KeyRow {
		names = append(names, k.Name)
	}
	say("keytest: press each touch key once, from the left: " + strings.Join(names, ", "))
	var got []uint16
	end := time.After(timeout)
	for len(got) < len(KeyRow) {
		select {
		case c := <-presses:
			got = append(got, c)
			say(fmt.Sprintf("keytest: press %d: code %s", len(got), CodeLabel(c)))
		case <-end:
			say(fmt.Sprintf("keytest: %d of %d presses in %s", len(got), len(KeyRow), timeout))
			say("RESULT: keytest not complete")
			return false
		}
	}
	ok := true
	for i, c := range got {
		ok = ok && c == KeyRow[i].Code
	}
	if ok {
		say("RESULT: keytest: the six keys give the expected codes, in order")
	} else {
		var g []string
		for _, c := range got {
			g = append(g, CodeLabel(c))
		}
		say("RESULT: keytest: NOT the expected order: " + strings.Join(g, ", "))
	}
	return ok
}
