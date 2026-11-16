package provision

import (
	"context"
	"io"
	"path/filepath"
	"time"

	"uc-pxx-installer/internal/clone"
	"uc-pxx-installer/internal/xfer/handover"
)

// Env is what the service needs of the unit (LiveEnv on a unit; the tests
// give their own).
type Env struct {
	// Root is "/" on a unit (a test root otherwise): every path of the
	// service is under it.
	Root  string
	Now   func() time.Time
	Sleep func(time.Duration)
	Logf  func(string, ...any)
	// Marked: the running slot is successful and the slot that boots (its
	// own mark hook ran); slot is "a" or "b".
	Marked func() (ok bool, slot string, err error)
	// Locks takes tsx-autoupdate's run lock and its apk lock (one try each,
	// non-blocking; the run lock first, given back when the apk lock is
	// busy) and returns their release.
	Locks func() (func(), error)
	// Guard sets the kernel read-only flag of the eMMC and of every
	// partition but the running root (slot's system_<slot>) and returns the
	// restore.
	Guard func(slot string) (func() error, error)
	// Exec runs argv (a pinned program of the base) as root with a fixed
	// environment; each line of its output goes to line.
	Exec func(argv []string, line func(string)) error
	// Fetch is the network (nil: none).
	Fetch Fetcher
	// USB is the slot's USB link (nil: none).
	USB USB
	// Panel shows the lines on the unit's screen (tty1).
	Panel func(lines []string)
	// Free is the free bytes of the file system of a path.
	Free func(path string) int64
	// Mem is MemAvailable in bytes.
	Mem func() int64
	// ClockSynced: the kernel clock is synchronised (chrony; a TLS fetch
	// waits for it, bounded).
	ClockSynced func() bool
	// MountStage mounts the stage tmpfs of size bytes (root, 0711);
	// UnmountStage takes it off. nil: a test folder.
	MountStage   func(dir string, size int64) error
	UnmountStage func(dir string) error
}

func (e *Env) path(p string) string { return filepath.Join(e.Root, p) }

// Options of Run.
type Options struct {
	// Gate and Release: the clone gate and the release data (nil: this
	// program's own, clone.EmbeddedGate and clone.Release).
	Gate    *clone.Gate
	Release *clone.ReleaseData
	// Retries, RetryWaits, MarkWait, ChoiceWait, LockWait, ClockWait,
	// DoneLinger: 0 = the defaults (tests shorten them).
	Retries    int
	RetryWaits []time.Duration
	MarkWait   time.Duration
	ChoiceWait time.Duration
	LockWait   time.Duration
	ClockWait  time.Duration
	DoneLinger time.Duration
}

// The defaults.
const (
	// MarkWait: the wait for the running slot's mark (as step 2).
	MarkWait = 10 * time.Minute
	// ChoiceWait: the wait for the page's choice (as step 2's ProvisionWait).
	ChoiceWait = 6 * time.Hour
	// LockWait: tsx-autoupdate's locks (as step 2's LockWait).
	LockWait = 15 * time.Minute
	// ClockWait: a TLS fetch waits this long for the clock's sync, then tries.
	ClockWait = 3 * time.Minute
	// DoneLinger: the page reads "done" before the link goes to step 2.
	DoneLinger = 5 * time.Second
	// Retries: the retries of the download after the first try (owner
	// 2026-11-15: 5 retries; then failed: network and the cable).
	Retries = 5
	// MemReserve is the RAM kept free (as the receiver's).
	MemReserve = 256 << 20
	// StageSlack is the tmpfs room beyond the largest bundle.
	StageSlack = 64 << 20
	// MaxStage is the largest stage (the receiver's limit).
	MaxStage = 1536 << 20
	// FreeMargin: the root file system needs the installed size + 10 %.
	FreeMarginPct = 10
)

// RetryWaits are the waits before the retries (10 s, 30 s, 1 min, 2 min,
// 5 min; DESIGN-R23 §3.6).
var RetryWaits = []time.Duration{10 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute}

func (o *Options) defaults() {
	if o.Retries == 0 {
		o.Retries = Retries
	}
	if o.RetryWaits == nil {
		o.RetryWaits = RetryWaits
	}
	if o.MarkWait == 0 {
		o.MarkWait = MarkWait
	}
	if o.ChoiceWait == 0 {
		o.ChoiceWait = ChoiceWait
	}
	if o.LockWait == 0 {
		o.LockWait = LockWait
	}
	if o.ClockWait == 0 {
		o.ClockWait = ClockWait
	}
	if o.DoneLinger == 0 {
		o.DoneLinger = DoneLinger
	}
	if o.Gate == nil {
		o.Gate = clone.EmbeddedGate()
	}
	if o.Release == nil {
		o.Release = clone.Release()
	}
}

// XferArgs are the arguments of the receiver (uc-pxx-xfer run --kind
// provision).
type XferArgs struct {
	Profiles   []handover.ProvisionProfile
	Fixed      string
	ConsoleKey bool
	Seq        int
	StatusOnly bool
}

// Proc is a running receiver.
type Proc interface {
	Stop() error
	Done() <-chan struct{}
}

// USB is the slot's USB link (the gadget of uc-pxx-usb, taken for the
// provisioning; the receiver uc-pxx-xfer of the kind provision).
type USB interface {
	// Take stops uc-pxx-usb (the gadget is the service's); Give starts it
	// again.
	Take() error
	Give() error
	// Cleanup takes apart what a killed receiver left (never a ready folder).
	Cleanup() error
	// Start starts the receiver (uc-pxx-xfer run --kind provision).
	Start(a XferArgs) (Proc, error)
	// UID is the receiver's user; NoProcessOf kills what of it is left
	// (before the seal of a cable bundle).
	UID() (int, error)
	NoProcessOf(uid int) error
}

// ctxFor is a context that ends when the stall watch cancels it.
func ctxFor() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }

var _ io.Writer = (*countWriter)(nil)

// countWriter counts the bytes it passes on.
type countWriter struct {
	w io.Writer
	n *int64
}

func (c *countWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	*c.n += int64(n)
	return n, err
}
