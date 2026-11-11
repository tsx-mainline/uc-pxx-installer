//go:build install && step2

package step2

// The live panel of the service: the kiosk stopped, the text on tty1 (the
// LCD), the touch keys grabbed (EVIOCGRAB) so the kiosk under it never sees
// them. The keys come from the evdev of the maXTouch controller as
// ui.ReadEvdev reads them (autorepeat values dropped), mapped as the RAM
// flow maps them: Mic = OK, Speaker = Back, Vol-/Vol+.

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"uc-pxx-installer/internal/step2ui"
	"uc-pxx-installer/internal/ui"
)

const (
	evSyn      = 0
	synDropped = 3
	evKey      = 1
	eventSize  = 24         // struct input_event on 64-bit Linux
	eviocgrab  = 0x40044590 // _IOW('E', 0x90, int)
	panelTTY   = "/dev/tty1"
	rcService  = "/sbin/rc-service"
	kioskName  = "kiosk"
	chvtBinary = "/usr/bin/chvt"
)

type livePanel struct {
	tty  *os.File
	ev   *os.File
	keys chan step2ui.Key
}

// LivePanel stops the kiosk and takes tty1 and the touch keys.
func LivePanel() (Panel, error) {
	dev := ui.FindTouch("/")
	if dev == "" {
		return nil, fmt.Errorf("no maXTouch event device")
	}
	ev, err := os.Open(dev)
	if err != nil {
		return nil, err
	}
	one := 1
	if _, _, en := syscall.Syscall(syscall.SYS_IOCTL, ev.Fd(), eviocgrab, uintptr(one)); en != 0 {
		ev.Close()
		return nil, fmt.Errorf("EVIOCGRAB %s: %v", dev, en)
	}
	run(rcService, kioskName, "stop")
	run(chvtBinary, "1")
	tty, err := os.OpenFile(panelTTY, os.O_WRONLY|syscall.O_NOCTTY, 0)
	if err != nil {
		ev.Close()
		run(rcService, kioskName, "start")
		return nil, err
	}
	p := &livePanel{tty: tty, ev: ev, keys: make(chan step2ui.Key, 16)}
	go p.read()
	return p, nil
}

func run(name string, args ...string) {
	c := exec.Command(name, args...)
	c.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	c.Run()
}

func (p *livePanel) read() {
	buf := make([]byte, eventSize*16)
	for {
		n, err := p.ev.Read(buf)
		if err != nil {
			return
		}
		for _, k := range KeysOf(buf[:n]) {
			select {
			case p.keys <- k:
			default:
			}
		}
	}
}

// KeysOf turns evdev events into keys of the confirm model: key presses and
// releases of the touch keys (autorepeat values dropped). After SYN_DROPPED
// (the kernel lost events, perhaps the release of OK) it gives KeyOKUp: any
// hold ends, and only a new press of OK starts one (review T4-2 NIT-1: a
// lost key-up never lets one short tap start the run later).
func KeysOf(b []byte) []step2ui.Key {
	var out []step2ui.Key
	for off := 0; off+eventSize <= len(b); off += eventSize {
		e := b[off : off+eventSize]
		typ := binary.LittleEndian.Uint16(e[16:])
		code := binary.LittleEndian.Uint16(e[18:])
		val := int32(binary.LittleEndian.Uint32(e[20:]))
		switch {
		case typ == evSyn && code == synDropped:
			out = append(out, step2ui.KeyOKUp)
		case typ != evKey || val == 2:
			// autorepeat: never a key of the model
		default:
			if k := keyOf(ui.MapCode(code), val == 1); k != step2ui.KeyNone {
				out = append(out, k)
			}
		}
	}
	return out
}

// keyOf maps a UI key press or release to a key of the confirm model.
func keyOf(k ui.Key, down bool) step2ui.Key {
	switch {
	case k == ui.KeyOK && down:
		return step2ui.KeyOKDown
	case k == ui.KeyOK:
		return step2ui.KeyOKUp
	case k == ui.KeyBack && down:
		return step2ui.KeyBack
	case k == ui.KeyPrev && down:
		return step2ui.KeyVolDown
	case k == ui.KeyNext && down:
		return step2ui.KeyVolUp
	}
	return step2ui.KeyNone
}

func (p *livePanel) Show(lines []string) {
	w, _ := ui.Size(p.tty.Fd())
	if w <= 0 {
		w = 80
	}
	var b strings.Builder
	b.WriteString("\033[H\033[2J")
	for _, l := range lines {
		for len(l) > w {
			b.WriteString(l[:w] + "\r\n")
			l = l[w:]
		}
		b.WriteString(l + "\r\n")
	}
	p.tty.WriteString(b.String())
}

func (p *livePanel) Keys() <-chan step2ui.Key { return p.keys }

func (p *livePanel) Close(restart bool) {
	p.ev.Close() // the grab ends with the file
	p.tty.Close()
	if restart {
		run(rcService, kioskName, "start")
	}
}
