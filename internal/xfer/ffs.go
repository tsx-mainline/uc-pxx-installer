package xfer

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// FunctionFS events (struct usb_functionfs_event: 8 bytes of SETUP, the
// type, 3 bytes of padding).
const (
	evBind    = 0
	evUnbind  = 1
	evEnable  = 2
	evDisable = 3
	evSetup   = 4
	evSuspend = 5
	evResume  = 6
	evSize    = 12
)

// ffsConn is the link of one session: ep1 (bulk OUT) and ep2 (bulk IN) of the
// FunctionFS mount. A read is one read(2): FunctionFS ends it at the end of
// the host's transfer.
type ffsConn struct {
	dir     string
	out, in *os.File
}

func openConn(dir string) (*ffsConn, error) {
	out, err := os.OpenFile(filepath.Join(dir, "ep1"), os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	in, err := os.OpenFile(filepath.Join(dir, "ep2"), os.O_WRONLY, 0)
	if err != nil {
		out.Close()
		return nil, err
	}
	return &ffsConn{dir: dir, out: out, in: in}, nil
}

func (c *ffsConn) Read(p []byte) (int, error) {
	n, err := c.out.Read(p)
	if err == io.EOF {
		err = nil // a zero-length transfer
	}
	return n, err
}

func (c *ffsConn) Write(p []byte) (int, error) { return c.in.Write(p) }

func (c *ffsConn) Close() error {
	c.out.Close()
	return c.in.Close()
}

// Halt stalls both endpoints: in FunctionFS an I/O in the wrong direction of
// an endpoint halts it. The two non-blocking opens never wait for an endpoint
// that is not enabled (EAGAIN then: nothing to halt).
func (c *ffsConn) Halt() {
	one := []byte{0}
	if fd, err := syscall.Open(filepath.Join(c.dir, "ep1"), syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0); err == nil {
		syscall.Write(fd, one) // a write on the OUT endpoint: halt
		syscall.Close(fd)
	}
	if fd, err := syscall.Open(filepath.Join(c.dir, "ep2"), syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0); err == nil {
		syscall.Read(fd, one) // a read on the IN endpoint: halt
		syscall.Close(fd)
	}
}

// Daemon runs the FunctionFS function of the receiver.
type Daemon struct {
	Dir   string // the FunctionFS mount
	R     *Receiver
	Ready func() // called once the descriptors and strings are written
	Logf  func(string, ...any)
}

// Run writes the descriptors, then serves: one session per ENABLE, never two
// at a time (a new session starts only after the former one ended), until
// stop is closed. Every SETUP that reaches the function is stalled (the MS OS
// and WebUSB requests are answered by the kernel's composite layer).
func (d *Daemon) Run(stop <-chan struct{}) error {
	logf := d.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	ep0, err := os.OpenFile(filepath.Join(d.Dir, "ep0"), os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer ep0.Close()
	if _, err := ep0.Write(Descriptors()); err != nil {
		return fmt.Errorf("descriptors: %w", err)
	}
	if _, err := ep0.Write(Strings()); err != nil {
		return fmt.Errorf("strings: %w", err)
	}
	if d.Ready != nil {
		d.Ready()
	}
	logf("descriptors written; waiting for the host")
	enable := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-enable:
			}
			c, err := openConn(d.Dir)
			if err != nil {
				logf("endpoints: %v", err)
				continue
			}
			logf("session start")
			d.R.Serve(c)
			logf("session end: %s", d.R.Snapshot().Detail)
		}
	}()
	go d.R.Watch(stop)
	go func() {
		<-stop
		ep0.Close() // ends the read below
	}()
	ev := make([]byte, 4*evSize)
	for {
		n, err := ep0.Read(ev)
		if err != nil {
			select {
			case <-stop:
				return nil
			default:
			}
			if errors.Is(err, io.EOF) || errors.Is(err, syscall.EINTR) || errors.Is(err, syscall.EAGAIN) {
				continue
			}
			return fmt.Errorf("ep0: %w", err)
		}
		for i := 0; i+evSize <= n; i += evSize {
			e := ev[i : i+evSize]
			switch e[8] {
			case evBind:
				logf("event BIND")
			case evUnbind:
				logf("event UNBIND")
			case evEnable:
				logf("event ENABLE")
				select {
				case enable <- struct{}{}:
				default:
				}
			case evDisable:
				logf("event DISABLE")
			case evSuspend, evResume:
			case evSetup:
				logf("event SETUP %x: stalled", e[:8])
				if e[0]&0x80 != 0 {
					ep0.Read(nil) // an IN request: a read stalls it
				} else {
					ep0.Write(nil) // an OUT request: a write stalls it
				}
			default:
				logf("event %d", e[8])
			}
		}
	}
}
