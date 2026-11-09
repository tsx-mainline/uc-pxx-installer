package ui

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// Key is one input of the UI.
type Key int

const (
	KeyNone Key = iota
	KeyPrev     // Vol- on the device, Up or k on a terminal
	KeyNext     // Vol+ on the device, Down or j on a terminal
	KeyOK       // the Mic (mute) key, Enter on a terminal
	KeyBack     // the Speaker key, Backspace or b on a terminal
	KeyYes      // y on a terminal (confirms without a hold)
	KeyQuit     // q on a terminal
)

// Event is a key press (Down) or release.
type Event struct {
	Key  Key
	Down bool
}

// Linux key codes of the T15 touch keys (docs/hardware.md of uc-pxx-linux).
const (
	codeMute    = 113 // Mic (mute), place 6
	codeVolDown = 114 // place 4
	codeVolUp   = 115 // place 5
	codeProg1   = 148 // Teams, place 1
	codeProg2   = 149 // Headset, place 2
	codeProg3   = 202 // Speaker, place 3
)

// MapCode turns a key code of the touch keys into a UI key. The map is the
// choice of the project owner (2026-10-25): Mic (mute) = OK, Speaker = back;
// Vol- (the left one of the two) = previous, Vol+ (the right one) = next
// (owner, 2026-11-02: the other way round before). Teams and Headset have
// no function.
func MapCode(code uint16) Key {
	switch code {
	case codeVolDown:
		return KeyPrev
	case codeVolUp:
		return KeyNext
	case codeMute:
		return KeyOK
	case codeProg3:
		return KeyBack
	}
	return KeyNone
}

// FindTouch returns the event device of the maXTouch controller, or "".
func FindTouch(root string) string {
	names, _ := filepath.Glob(filepath.Join(root, "sys/class/input/event*/device/name"))
	for _, n := range names {
		b, err := os.ReadFile(n)
		if err != nil || !strings.Contains(string(b), "maXTouch") {
			continue
		}
		ev := filepath.Base(filepath.Dir(filepath.Dir(n)))
		return filepath.Join(root, "dev/input", ev)
	}
	return ""
}

const (
	evKey     = 1
	eventSize = 24 // struct input_event on 64-bit Linux
)

// ReadEvdev sends the touch-key events of dev to ch until a read fails.
func ReadEvdev(dev string, ch chan<- Event) error {
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
			typ := binary.LittleEndian.Uint16(e[16:])
			code := binary.LittleEndian.Uint16(e[18:])
			val := int32(binary.LittleEndian.Uint32(e[20:]))
			if typ != evKey || val == 2 {
				continue
			}
			if k := MapCode(code); k != KeyNone {
				ch <- Event{Key: k, Down: val == 1}
			}
		}
	}
}

// WatchTouch waits for the touch controller and reads it. It reports on
// found when the device appears or goes away.
func WatchTouch(root string, ch chan<- Event, found func(dev string)) {
	for {
		dev := FindTouch(root)
		if dev == "" {
			time.Sleep(time.Second)
			continue
		}
		found(dev)
		ReadEvdev(dev, ch)
		found("")
		time.Sleep(time.Second)
	}
}

// Raw puts a terminal into raw mode and returns a function that restores it.
func Raw(fd uintptr) (func(), error) {
	var old syscall.Termios
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&old))); e != 0 {
		return func() {}, e
	}
	t := old
	t.Lflag &^= syscall.ICANON | syscall.ECHO | syscall.ISIG
	t.Iflag &^= syscall.ICRNL | syscall.IXON
	t.Cc[syscall.VMIN] = 1
	t.Cc[syscall.VTIME] = 0
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&t))); e != 0 {
		return func() {}, e
	}
	return func() {
		syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&old)))
	}, nil
}

// ParseTerm turns terminal input into key events. A terminal has no
// release, so each key sends a press and a release.
func ParseTerm(b []byte) []Event {
	var out []Event
	push := func(k Key) { out = append(out, Event{k, true}, Event{k, false}) }
	for i := 0; i < len(b); i++ {
		switch c := b[i]; {
		case c == 0x1b && i+2 < len(b) && b[i+1] == '[':
			switch b[i+2] {
			case 'A', 'D':
				push(KeyPrev)
			case 'B', 'C':
				push(KeyNext)
			}
			i += 2
		case c == 'k' || c == '-':
			push(KeyPrev)
		case c == 'j' || c == '+' || c == ' ':
			push(KeyNext)
		case c == '\r' || c == '\n':
			push(KeyOK)
		case c == 0x7f || c == 0x08 || c == 'b':
			push(KeyBack)
		case c == 'y' || c == 'Y':
			push(KeyYes)
		case c == 'q' || c == 3:
			push(KeyQuit)
		}
	}
	return out
}

// ReadTerm reads key input from a terminal.
func ReadTerm(f *os.File, ch chan<- Event) {
	buf := make([]byte, 64)
	for {
		n, err := f.Read(buf)
		if err != nil {
			ch <- Event{KeyQuit, true}
			return
		}
		for _, e := range ParseTerm(buf[:n]) {
			ch <- e
		}
	}
}
