//go:build install

package device

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/plan"
)

// Medium is the eMMC as an install medium (install.Medium). The executor
// runs the plan on it exactly as on an image file in a dry run.
type Medium struct {
	d      *Disk
	p      *plan.Plan
	target string
	chunk  int
	cut    install.Cutter
	view   *View
}

// Medium makes the install medium. chunk is the size of each data write in
// bytes (0 = 1 MiB).
func (d *Disk) Medium(chunk int) *Medium {
	if chunk == 0 {
		chunk = ioChunk
	}
	return &Medium{d: d, chunk: chunk}
}

// Kind implements install.Medium.
func (m *Medium) Kind() string { return "device" }

// SetStep implements install.Medium.
func (m *Medium) SetStep(n int) { m.cut.Step = n }

// Writes implements install.Medium.
func (m *Medium) Writes() int { return m.cut.N }

// ReadAB implements install.Medium.
func (m *Medium) ReadAB() ([]byte, error) { return m.d.ReadAB() }

// Reader implements install.Medium.
func (m *Medium) Reader() io.ReaderAt {
	if m.view == nil {
		m.view = m.d.View()
	}
	return m.view
}

// HashRange implements install.Medium.
func (m *Medium) HashRange(off, n int64) (string, error) { return m.d.HashRange(off, n) }

// Prepare checks, fresh from the device, that it is the one the plan was
// made for: the node and identity, the size, the GPT (both copies, the
// layout sha256), the full A/B sector, and the bootctl binary (the reviewed
// sha256, and the one the plan names). It needs the A/B lock.
func (m *Medium) Prepare(p *plan.Plan, hooks *install.Hooks) (*gpt.Table, error) {
	d := m.d
	m.cut.Hooks = hooks
	if !d.Locked() {
		return nil, errors.New("the A/B lock is not held")
	}
	if p.Disk.Source != "device:"+EMMCPath || p.Device == nil || *p.Device != d.ID {
		return nil, errors.New("the plan is not for this eMMC")
	}
	if p.Disk.Sectors != d.Sectors {
		return nil, fmt.Errorf("the eMMC has %d sectors, the plan is for %d", d.Sectors, p.Disk.Sectors)
	}
	v := d.View()
	t, err := gpt.Parse(v, d.Bytes())
	v.Close()
	if err != nil {
		return nil, fmt.Errorf("GPT: %w", err)
	}
	if t.LayoutSHA256() != p.Disk.LayoutSHA256 || t.LayoutSHA256() != d.Table.LayoutSHA256() {
		return nil, errors.New("the GPT layout differs from the plan")
	}
	sec, err := d.ReadAB()
	if err != nil {
		return nil, err
	}
	if s := sha256.Sum256(sec); hex.EncodeToString(s[:]) != p.AB.SectorSHA256 {
		return nil, errors.New("the A/B sector differs from the plan")
	}
	_, sha, err := d.e.BootctlBinary()
	if err != nil {
		return nil, err
	}
	if sha != p.Bootctl.SHA256 || p.Bootctl.Path != BootctlPath || p.Bootctl.Exec != "" {
		return nil, errors.New("the bootctl binary is not the one the plan names")
	}
	if len(p.KeeperSHA256) != len(plan.SlotParts) {
		return nil, errors.New("the plan has no keeper hashes")
	}
	m.p, m.target = p, p.Target
	return t, nil
}

// BootctlStatus implements install.Medium: `uc-pxx-bootctl-inst status`.
func (m *Medium) BootctlStatus() (*plan.BootctlStatus, error) {
	return m.d.BootctlStatus()
}

// BootctlStatus runs `status` and reads its output.
func (d *Disk) BootctlStatus() (*plan.BootctlStatus, error) {
	r, err := d.Bootctl([]string{"status"})
	if err != nil {
		return nil, err
	}
	if r.RC != 0 {
		return nil, fmt.Errorf("uc-pxx-bootctl-inst status: exit %d: %s", r.RC, strings.TrimSpace(r.Stderr))
	}
	return plan.ParseBootctlStatus(r.Stderr, r.Stdout)
}

// Bootctl implements install.Medium. Before the write: bootctl's own status
// must agree with the installer's decoder on the sector cur. Then the tool
// runs. Exit 0: the sector must be the prediction. Exit 1 or 2: the tool says
// that the sector did not change; this is checked. Any other end (exit 3, a
// signal, a run error): p10inst reads the sector itself. When it is valid and
// is exactly cur, or exactly the prediction with the tail unchanged, the state
// is known and the install stops with it (exit 1). Only otherwise it is
// ErrABUnknown (exit 3).
func (m *Medium) Bootctl(b *plan.BootctlStep, cur []byte) (*install.BootctlRun, error) {
	st, err := m.d.BootctlStatus()
	if err != nil {
		return nil, err
	}
	a, _ := avbab.Parse(cur)
	if err := st.Agrees(a); err != nil {
		return nil, fmt.Errorf("before %s: bootctl status and the installer's decoder disagree: %w", strings.Join(b.Args, " "), err)
	}
	if err := m.cut.Before(nil); err != nil {
		return nil, err
	}
	want, _ := b.After.Raw()
	args := strings.Join(b.Args, " ")
	wrote := func() error {
		if b.NoOp {
			return nil
		}
		return m.cut.After(install.Event{Op: "ab", Partition: "misc", Off: plan.ABLBA * gpt.Sector, Len: gpt.Sector})
	}
	r, rerr := m.d.Bootctl(b.Args)
	run := &install.BootctlRun{RC: -1}
	how := ""
	switch {
	case rerr != nil:
		how = "did not run to its end (" + rerr.Error() + ")"
		run.Stderr = rerr.Error()
	default:
		run.RC, run.Stderr = r.RC, r.Stderr
		how = fmt.Sprintf("ended with exit %d", r.RC)
		if r.Signal != "" {
			how = "was ended by the signal " + r.Signal
		}
	}
	back, berr := m.d.ReadAB()
	valid := berr == nil
	if valid {
		ab, _ := avbab.Parse(back)
		valid = ab.Validate() == nil
	}
	isCur := valid && bytes.Equal(back, cur)
	isNext := valid && bytes.Equal(back[:32], want[:]) && bytes.Equal(back[32:], cur[32:])
	if rerr == nil && r.Signal == "" {
		switch r.RC {
		case 0:
			if err := wrote(); err != nil {
				return run, err
			}
			if berr != nil {
				return run, fmt.Errorf("%w: the A/B sector cannot be read back: %v", ErrABUnknown, berr)
			}
			if !valid {
				return run, fmt.Errorf("%w: the A/B sector is not valid after uc-pxx-bootctl-inst", ErrABUnknown)
			}
			if !isNext {
				return run, fmt.Errorf("%w: after uc-pxx-bootctl-inst %s the A/B sector is %x, the plan predicts %x (tail same: %v)",
					ErrABUnknown, args, back[:32], want, bytes.Equal(back[32:], cur[32:]))
			}
			return run, nil
		case 1, 2:
			if !isCur {
				return run, fmt.Errorf("%w: uc-pxx-bootctl-inst exit %d says unchanged, but the sector differs", ErrABUnknown, r.RC)
			}
			what := map[int]string{1: "refused", 2: "I/O error"}[r.RC]
			return run, fmt.Errorf("uc-pxx-bootctl-inst %s: exit %d (%s, the A/B sector is verified unchanged): %s",
				args, r.RC, what, strings.TrimSpace(r.Stderr))
		}
	}
	switch {
	case isCur:
		return run, fmt.Errorf("uc-pxx-bootctl-inst %s %s; the installer read the A/B sector back: it is unchanged (%s)",
			args, how, plan.State(first32(back)))
	case isNext && !b.NoOp:
		if err := wrote(); err != nil {
			return run, err
		}
		return run, fmt.Errorf("uc-pxx-bootctl-inst %s %s; the installer read the A/B sector back: it is the plan's next state (%s); stopped there",
			args, how, plan.State(first32(back)))
	}
	return run, fmt.Errorf("%w: uc-pxx-bootctl-inst %s %s, and the A/B sector is neither the state before nor the predicted one: %s",
		ErrABUnknown, args, how, strings.TrimSpace(run.Stderr))
}

func first32(b []byte) [32]byte {
	var r [32]byte
	copy(r[:], b)
	return r
}

// OpenWrite implements install.Medium.
func (m *Medium) OpenWrite(w *plan.WriteStep) (install.PartWriter, error) {
	if m.p == nil {
		return nil, errors.New("internal error: the medium is not bound to a plan")
	}
	return m.d.openWrite(m.target, w, m.chunk, &m.cut)
}

// Verify implements install.Medium. It reads the range twice with two
// implementations: once in Go with O_DIRECT, once (after BLKFLSBUF drops the
// page cache of the node) with `head -c N NODE | sha256sum` of busybox. Both
// must give the same sha256.
func (m *Medium) Verify(w *plan.WriteStep) (string, error) {
	p, ok := m.d.byName[w.Partition]
	if !ok || p.Start != w.StartLBA {
		return "", fmt.Errorf("verify: no partition %s at %d", w.Partition, w.StartLBA)
	}
	if w.Offset != 0 {
		return "", errors.New("verify: an image must start at offset 0 of its partition")
	}
	h1, err := m.d.HashRange(p.Start*gpt.Sector, w.Length)
	if err != nil {
		return "", err
	}
	node := m.d.partNode(p)
	if err := m.d.e.flushBuf(node, p.Major, p.Minor); err != nil {
		return "", err
	}
	h2, err := shellHash(node, w.Length)
	if err != nil {
		return "", fmt.Errorf("verify %s with sha256sum: %w", w.Partition, err)
	}
	if h1 != h2 {
		return "", fmt.Errorf("verify %s: the two reads differ: %s (O_DIRECT) and %s (sha256sum)", w.Partition, h1, h2)
	}
	return h1, nil
}

// tool finds a busybox (or coreutils) tool in the fixed system directories,
// never through $PATH.
func tool(name string) (string, error) {
	for _, d := range []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"} {
		p := d + "/" + name
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("no %s in /usr/bin, /bin, /usr/sbin or /sbin", name)
}

// shellHash runs head -c n NODE | sha256sum, with no shell.
func shellHash(node string, n int64) (string, error) {
	env := []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	headPath, err := tool("head")
	if err != nil {
		return "", err
	}
	shaPath, err := tool("sha256sum")
	if err != nil {
		return "", err
	}
	head := exec.Command(headPath, "-c", strconv.FormatInt(n, 10), node)
	sum := exec.Command(shaPath)
	head.Env, sum.Env = env, env
	pr, pw, err := os.Pipe()
	if err != nil {
		return "", err
	}
	head.Stdout = pw
	sum.Stdin = pr
	var out, e1, e2 bytes.Buffer
	sum.Stdout, head.Stderr, sum.Stderr = &out, &e1, &e2
	if err := head.Start(); err != nil {
		pr.Close()
		pw.Close()
		return "", err
	}
	pw.Close()
	if err := sum.Start(); err != nil {
		pr.Close()
		head.Wait()
		return "", err
	}
	pr.Close()
	herr, serr := head.Wait(), sum.Wait()
	if herr != nil || serr != nil {
		return "", fmt.Errorf("head: %v %s; sha256sum: %v %s", herr, strings.TrimSpace(e1.String()), serr, strings.TrimSpace(e2.String()))
	}
	f := strings.Fields(out.String())
	if len(f) == 0 || len(f[0]) != 64 {
		return "", fmt.Errorf("sha256sum printed %q", out.String())
	}
	return f[0], nil
}

// ReadKeySector implements install.Medium: one sector of backup, O_DIRECT.
func (m *Medium) ReadKeySector(lba int64) ([]byte, error) {
	p, ok := m.d.byName["backup"]
	if !ok || lba < p.Start || lba >= p.Start+p.Size {
		return nil, fmt.Errorf("LBA %d is not in backup", lba)
	}
	b := make([]byte, gpt.Sector)
	if err := m.d.ReadPart(p, b, (lba-p.Start)*gpt.Sector); err != nil {
		return nil, err
	}
	return b, nil
}

// WriteKeySector implements install.Medium (writer.go).
func (m *Medium) WriteKeySector(lba int64, rec []byte) error {
	if m.p == nil {
		return errors.New("internal error: the medium is not bound to a plan")
	}
	return m.d.writeKeySector(lba, rec, &m.cut)
}

// Sync implements install.Medium: fsync of every target partition node and
// of the disk node. Each fsync of a block device sends a cache flush to the
// eMMC, so after this the data is on the flash before the target is armed.
func (m *Medium) Sync(names []string) error {
	for _, n := range names {
		p, ok := m.d.byName[n]
		if !ok {
			return fmt.Errorf("sync: no partition %s", n)
		}
		if err := m.d.e.fsyncNode(m.d.partNode(p), p.Major, p.Minor); err != nil {
			return err
		}
	}
	return m.d.e.fsyncNode(m.d.diskNode(), m.d.Major, m.d.Minor)
}

// Finish implements install.Medium: the read-only flag on the disk and on
// every partition again, as the RAM image had it at its start.
func (m *Medium) Finish() error {
	if m.view != nil {
		m.view.Close()
		m.view = nil
	}
	return m.d.ReadOnlyAll()
}
