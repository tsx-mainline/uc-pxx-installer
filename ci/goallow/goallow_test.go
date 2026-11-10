package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// module makes a small module named like the repository in a temporary
// folder: files maps a path to its content.
func module(t *testing.T, files map[string]string) string {
	t.Helper()
	d := t.TempDir()
	files["go.mod"] = "module uc-pxx-installer\n\ngo 1.24\n"
	files["internal/gadget/gadget.go"] = "package gadget\n\nfunc Bind(root, udc string) error { return nil }\n\nfunc Ejected(root string) bool { return false }\n"
	files["internal/device/device.go"] = "package device\n\nconst X = 1\n"
	for p, s := range files {
		f := filepath.Join(d, p)
		os.MkdirAll(filepath.Dir(f), 0o755)
		if err := os.WriteFile(f, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

const head = "package %s\n\nimport (\n\t\"crypto/sha256\"\n\t\"io\"\n\t\"os\"\n\t\"syscall\"\n)\n\nvar _ = sha256.New\nvar _ io.Reader\nvar _ = os.Open\nvar _ = syscall.O_RDONLY\n\n"

func src(pkg, body string) string {
	return strings.Replace(head, "%s", pkg, 1) + body
}

// TestReaderInjections: the review's injections into a reader package
// (FAT-READER-REVIEW.md F5), and more. Each one must be found; the clean
// file must pass.
func TestReaderInjections(t *testing.T) {
	clean := `func read(p string) ([]byte, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, _ := f.Stat()
	b := make([]byte, st.Size())
	_, err = f.ReadAt(b, 0)
	h := sha256.New()
	h.Write(b)
	var r io.ReaderAt = f
	_ = r
	return b, err
}
`
	cases := []struct{ name, body, want string }{
		{"clean", clean, ""},
		{"os.OpenFile O_RDWR", "func x(p string) { os.OpenFile(p, os.O_RDWR, 0) }\n", "opens for writing"},
		{"os.OpenFile with a variable flag", "func x(p string, fl int) { os.OpenFile(p, fl, 0) }\n", "not a constant"},
		{"syscall.Open(path, 2, 0)", "func x(p string) { syscall.Open(p, 2, 0) }\n", "syscall.Open is not a read-only name"},
		{"syscall.Syscall ioctl", "func x() { syscall.Syscall(syscall.SYS_IOCTL, 0, 0, 0) }\n", "syscall.Syscall is not a read-only name"},
		{"syscall.Mount", `func x() { syscall.Mount("a", "b", "vfat", 0, "") }` + "\n", "syscall.Mount"},
		{"os.Chmod", `func x() { os.Chmod("a", 0) }` + "\n", "os.Chmod"},
		{"os.Symlink", `func x() { os.Symlink("a", "b") }` + "\n", "os.Symlink"},
		{"f.Write", "func x(f *os.File) { f.Write(nil) }\n", "(*os.File).Write writes"},
		{"f.WriteAt", "func x(f *os.File) { f.WriteAt(nil, 0) }\n", "(*os.File).WriteAt"},
		{"f.Truncate", "func x(f *os.File) { f.Truncate(0) }\n", "(*os.File).Truncate"},
		{"a file as io.Writer", "func x(f *os.File) { var w io.Writer = f; w.Write(nil) }\n", "given to the writer interface io.Writer"},
		{"a file given to io.Copy", "func x(f *os.File, r io.Reader) { io.Copy(f, r) }\n", "given to the writer interface io.Writer"},
		{"a file returned as io.WriterAt", "func x(f *os.File) io.WriterAt { return f }\n", "io.WriterAt"},
		{"a file in a struct of io.Writer", "type s struct{ w io.Writer }\nfunc x(f *os.File) s { return s{w: f} }\n", "io.Writer"},
		{"a file converted", "func x(f *os.File) { _ = io.Writer(f) }\n", "io.Writer"},
		{"a file as any", "func x(f *os.File) any { return f }\n", "writer interface any"},
		{"os.Create", `func x() { os.Create("a") }` + "\n", "os.Create"},
		{"os.WriteFile", `func x() { os.WriteFile("a", nil, 0) }` + "\n", "os.WriteFile"},
		{"os.Remove", `func x() { os.Remove("a") }` + "\n", "os.Remove"},
		{"os.NewFile", "func x() { os.NewFile(3, \"x\") }\n", "os.NewFile"},
		{"go:linkname", "//go:linkname y runtime.y\nfunc x() {}\n", "go:linkname"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := module(t, map[string]string{"internal/fatfs/a.go": src("fatfs", c.body)})
			f, n, err := Check(d, []Rule{{Dir: "internal/fatfs", Kind: "reader"}})
			if err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Fatalf("%d packages", n)
			}
			all := strings.Join(f, "\n")
			switch {
			case c.want == "" && len(f) > 0:
				t.Fatalf("a clean file has findings:\n%s", all)
			case c.want != "" && !strings.Contains(all, c.want):
				t.Fatalf("want %q in:\n%s", c.want, all)
			}
		})
	}
	t.Run("os.Chtimes", func(t *testing.T) {
		d := module(t, map[string]string{"internal/fatfs/a.go": "package fatfs\n\nimport (\n\t\"os\"\n\t\"time\"\n)\n\nfunc x() { os.Chtimes(\"a\", time.Time{}, time.Time{}) }\n"})
		f, _, err := Check(d, []Rule{{Dir: "internal/fatfs", Kind: "reader"}})
		if err != nil || !strings.Contains(strings.Join(f, "\n"), "os.Chtimes is not a read-only name") {
			t.Fatalf("%v %q", err, f)
		}
	})
	t.Run("imports", func(t *testing.T) {
		for imp, want := range map[string]string{"os/exec": "imports \"os/exec\"", "unsafe": "imports unsafe", "net": "import \"net\""} {
			d := module(t, map[string]string{"internal/fatfs/a.go": "package fatfs\n\nimport _ \"" + imp + "\"\n"})
			f, _, err := Check(d, []Rule{{Dir: "internal/fatfs", Kind: "reader"}})
			if err != nil || !strings.Contains(strings.Join(f, "\n"), want) {
				t.Fatalf("%s: %v %q", imp, err, f)
			}
		}
	})
	t.Run("an exception is per package", func(t *testing.T) {
		body := "func x() { syscall.Syscall(syscall.SYS_IOCTL, 0, 0, 0) }\n"
		d := module(t, map[string]string{"internal/blk/a.go": src("blk", body)})
		f, _, err := Check(d, []Rule{{Dir: "internal/blk", Kind: "reader", Except: []string{"syscall.Syscall", "syscall.SYS_IOCTL"}}})
		if err != nil || len(f) != 0 {
			t.Fatalf("%v %q", err, f)
		}
	})
}

func TestPureFlow(t *testing.T) {
	for _, c := range []struct{ name, file, want string }{
		{"clean", "package flow\n\nimport (\n\t\"fmt\"\n\t\"strings\"\n)\n\nfunc X() string { return fmt.Sprint(strings.ToUpper(\"a\")) }\n", ""},
		{"imports os", "package flow\n\nimport \"os\"\n\nvar _ = os.Getpid\n", `imports "os"`},
		{"imports io", "package flow\n\nimport \"io\"\n\nvar _ io.Reader\n", `imports "io"`},
		{"imports os/exec", "package flow\n\nimport _ \"os/exec\"\n", `imports "os/exec"`},
		{"imports syscall", "package flow\n\nimport _ \"syscall\"\n", `imports "syscall"`},
		{"imports the device mode", "package flow\n\nimport _ \"uc-pxx-installer/internal/device\"\n", "imports"},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := module(t, map[string]string{"internal/flow/a.go": c.file})
			f, _, err := Check(d, []Rule{{Dir: "internal/flow", Kind: "pure"}})
			if err != nil {
				t.Fatal(err)
			}
			all := strings.Join(f, "\n")
			if (c.want == "") != (len(f) == 0) || (c.want != "" && !strings.Contains(all, c.want)) {
				t.Fatalf("want %q, got:\n%s", c.want, all)
			}
		})
	}
	t.Run("status.Collect", func(t *testing.T) {
		d := module(t, map[string]string{
			"internal/status/s.go": "package status\n\ntype Source struct{}\n\ntype Line struct{ Text string }\n\nfunc Collect(s Source) int { return 0 }\n",
			"internal/flow/a.go":   "package flow\n\nimport \"uc-pxx-installer/internal/status\"\n\nvar _ status.Line\n\nfunc X() int { return status.Collect(status.Source{}) }\n",
		})
		f, _, err := Check(d, []Rule{{Dir: "internal/flow", Kind: "pure"}})
		if err != nil || !strings.Contains(strings.Join(f, "\n"), "status.Collect, which reads") {
			t.Fatalf("%v %q", err, f)
		}
	})
	// the planners: the official one and the mainline one; a mainline plan's
	// type and its Render may be used
	for _, c := range []struct{ name, use, want string }{
		{"plan.Compute", "func X() { plan.Compute(plan.Input{}) }\n", "plan.Compute, which reads"},
		{"plan.ComputeMainline", "func X() { plan.ComputeMainline(plan.MainlineInput{}) }\n", "plan.ComputeMainline, which reads"},
		{"plan.ComputeOtherSlot", "func X() { plan.ComputeOtherSlot(plan.OtherSlotInput{}) }\n", "plan.ComputeOtherSlot, which reads"},
		{"plan.VerifyOfficial", "func X() { plan.VerifyOfficial() }\n", "plan.VerifyOfficial, which reads"},
		{"a mainline plan is shown", "func X(p *plan.MainlinePlan) string { return p.Kind + plan.MainlineKind }\n", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := module(t, map[string]string{
				"internal/plan/p.go": "package plan\n\ntype Input struct{}\n\ntype MainlineInput struct{}\n\n" +
					"type MainlinePlan struct{ Kind string }\n\nconst MainlineKind = \"k\"\n\n" +
					"func Compute(Input) int { return 0 }\n\nfunc ComputeMainline(MainlineInput) int { return 0 }\n\nfunc VerifyOfficial() int { return 0 }\n" +
					"\ntype OtherSlotInput struct{}\n\nfunc ComputeOtherSlot(OtherSlotInput) int { return 0 }\n",
				"internal/flow/a.go": "package flow\n\nimport \"uc-pxx-installer/internal/plan\"\n\n" + c.use,
			})
			f, _, err := Check(d, []Rule{{Dir: "internal/flow", Kind: "pure"}})
			all := strings.Join(f, "\n")
			if err != nil || (c.want == "") != (len(f) == 0) || !strings.Contains(all, c.want) {
				t.Fatalf("%v, want %q, got %q", err, c.want, f)
			}
		})
	}
	t.Run("the mainline readers are denied names", func(t *testing.T) {
		for _, n := range []string{"plan.ComputeMainline", "mainline.Verify", "mainline.Reopen", "mainline.DirSource"} {
			if !pureDenied["uc-pxx-installer/internal/"+n] {
				t.Fatalf("%s is not in pureDenied", n)
			}
		}
	})
}

func TestRunner(t *testing.T) {
	rule := Rule{Dir: "internal/flowio", Kind: "runner", Allowed: map[string][]string{
		KindFile: {"writeRunFile", "useImports"}, KindProcess: {"startSupervised", "useImports"},
		KindSignal: {"cancelPlan", "useImports"}, KindGadget: {"ejectDrive", "useImports"}}}
	imp := "package flowio\n\nimport (\n\t\"os\"\n\t\"os/exec\"\n\t\"syscall\"\n\n\t\"uc-pxx-installer/internal/gadget\"\n)\n\n" +
		"func useImports() { _, _, _, _ = os.Open, exec.Command, syscall.Kill, gadget.Ejected }\n\n"
	for _, c := range []struct{ name, body, want string }{
		{"clean", `func writeRunFile(p string, b []byte) error {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	f.Write(b)
	return f.Close()
}
func startSupervised() error { return exec.Command("/usr/bin/p10inst", "install").Start() }
func cancelPlan(pid int) { syscall.Kill(pid, syscall.SIGTERM) }
func ejectDrive() { gadget.Bind("a", "b") }
func reads() bool { return gadget.Ejected("x") }
`, ""},
		{"a file write elsewhere", `func other(p string) { os.WriteFile(p, nil, 0) }` + "\n", "outside the file functions"},
		{"a write open elsewhere", `func other(p string) { os.OpenFile(p, os.O_RDWR, 0) }` + "\n", "opens for writing"},
		{"a process elsewhere", `func other() { exec.Command("dd").Run() }` + "\n", "outside the process functions"},
		{"a signal elsewhere", `func other(pid int) { syscall.Kill(pid, 9) }` + "\n", "outside the signal functions"},
		{"a raw system call", `func writeRunFile() { syscall.Syscall(0, 0, 0, 0) }` + "\n", "syscall.Syscall is not allowed"},
		{"a gadget change elsewhere", `func other() { gadget.Bind("a", "b") }` + "\n", "changes the gadget"},
		{"a file given to a writer elsewhere", "func other(f *os.File) { var w interface{ Write([]byte) (int, error) } = f; _ = w }\n", "writer interface"},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := module(t, map[string]string{"internal/flowio/a.go": imp + c.body})
			f, _, err := Check(d, []Rule{rule})
			if err != nil {
				t.Fatal(err)
			}
			all := strings.Join(f, "\n")
			if (c.want == "") != (len(f) == 0) || (c.want != "" && !strings.Contains(all, c.want)) {
				t.Fatalf("want %q, got:\n%s", c.want, all)
			}
		})
	}
	t.Run("the install side", func(t *testing.T) {
		d := module(t, map[string]string{"internal/flowio/a.go": "package flowio\n\nimport \"uc-pxx-installer/internal/device\"\n\nconst y = device.X\n"})
		f, _, err := Check(d, []Rule{rule})
		if err != nil || !strings.Contains(strings.Join(f, "\n"), "imports the install side") {
			t.Fatalf("%v %q", err, f)
		}
	})
}

// TestRepository: the rules of the repository pass on the repository.
func TestRepository(t *testing.T) {
	f, n, err := Check("../..", DefaultRules())
	if err != nil {
		t.Fatal(err)
	}
	if len(f) > 0 {
		t.Fatalf("findings:\n%s", strings.Join(f, "\n"))
	}
	if n < 15 {
		t.Fatalf("only %d packages checked", n)
	}
}
