// Command goallow checks, with the Go type checker, that the packages that
// must only read (the eMMC readers, the drive reader) and the on-screen flow
// have no write path (FAT-READER-REVIEW.md F5: the regex of
// check-no-writer.py does not see a raw system call, an ioctl, a mount, a
// chmod or a write through a method). ci/check-no-writer.py runs it.
//
//	go run ./ci/goallow [REPO]
//
// Every use of a package-level object of os, syscall, os/exec, unsafe, net
// and every method call on *os.File is resolved by go/types and checked:
//
//   - reader packages: no os, os/exec or syscall object that writes, makes a
//     file, changes a file's mode, owner or times, or starts a process; no
//     write method of *os.File; os.OpenFile and syscall.Open only with a
//     constant flag that has no write bit; syscall only through a short list
//     of read-only names, plus the exceptions named per package (the
//     BLKROSET/BLKROGET ioctl of blk, the read-only mmap of devmem); no
//     unsafe outside blk; no net, plugin, cgo or go:linkname;
//   - the pure flow package (internal/flow): it may import only the
//     packages of its list (no os, no io, no syscall, no exec), and of the
//     reader packages it imports only names that do no I/O;
//   - the FAT writer of the bundle (internal/fatmk): no os, syscall, exec or
//     unsafe at all: it writes only to the io.WriterAt it is given;
//   - the flow runner (internal/flowio): no import of the install side
//     (install, device, devmode, bootctl, fakeemmc); each kind of write
//     (a file write, a process start, a signal, a gadget change) only in the
//     functions named for it, and never a raw system call or an ioctl.
//
// Exit status: 0 clean, 1 a finding, 2 the check could not run.
package main

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Rule is the rule for one package (a directory of the module).
type Rule struct {
	Dir  string
	Kind string // "reader", "pure", "nofile" or "runner"
	// Except: syscall/os/unsafe names this package may use beyond the
	// common list ("syscall.Syscall" for the blk ioctl).
	Except []string
	// Allowed (runner): the functions that may do each kind of write.
	Allowed map[string][]string // kind -> function names
}

// The write kinds of the runner.
const (
	KindFile    = "file"    // a file write, create, rename, remove, truncate
	KindProcess = "process" // a process start
	KindSignal  = "signal"  // a signal to a process
	KindGadget  = "gadget"  // a change of the USB gadget (package gadget)
)

// readNames are the os, syscall and unsafe names that only read or are types
// and constants.
var readNames = map[string]bool{
	"os.Open": true, "os.ReadFile": true, "os.ReadDir": true, "os.Stat": true, "os.Lstat": true, "os.SameFile": true,
	"os.IsNotExist": true, "os.IsExist": true, "os.ErrNotExist": true, "os.ErrExist": true, "os.File": true,
	"os.FileInfo": true, "os.FileMode": true, "os.Getenv": true, "os.LookupEnv": true, "os.Getpid": true,
	"os.Readlink": true, "os.ModeSymlink": true, "os.ModeDevice": true, "os.ModeCharDevice": true, "os.ModeNamedPipe": true,
	"os.ModeDir": true, "os.ModeType": true, "os.PathError": true, "os.DirEntry": true, "os.Getwd": true,
	"os.O_RDONLY": true, "os.Args": true, "os.Stdin": true, "os.ErrClosed": true, "os.Hostname": true,
	"os.Getpagesize":   true,
	"os.OpenFile":      true, // only with a read-only constant flag (checked)
	"syscall.O_RDONLY": true, "syscall.O_NOFOLLOW": true, "syscall.O_NONBLOCK": true, "syscall.O_DIRECT": true,
	"syscall.O_CLOEXEC": true, "syscall.O_SYNC": true, "syscall.Stat_t": true, "syscall.Errno": true,
	"syscall.ENOENT": true, "syscall.EINVAL": true, "syscall.S_IFMT": true, "syscall.S_IFBLK": true,
	"syscall.S_IFREG": true, "syscall.S_IFCHR": true, "syscall.Signal": true, "syscall.WaitStatus": true,
	"syscall.PROT_READ": true, "syscall.MAP_SHARED": true,
	"syscall.O_LARGEFILE": true,
}

// writeBits of an open flag.
const writeBits = int64(os.O_WRONLY | os.O_RDWR | os.O_CREATE | os.O_TRUNC | os.O_APPEND | os.O_EXCL)

// fileWriteMethods are the methods of *os.File that write or change the file.
var fileWriteMethods = map[string]bool{"Write": true, "WriteAt": true, "WriteString": true, "Truncate": true,
	"Chmod": true, "Chown": true, "ReadFrom": true, "SyscallConn": true, "Chdir": true}

// pureImports are the only imports of the pure flow package.
var pureImports = map[string]bool{"bytes": true, "crypto/sha256": true, "encoding/hex": true, "encoding/json": true,
	"errors": true, "fmt": true, "regexp": true, "sort": true, "strconv": true, "strings": true, "time": true,
	"math": true, "unicode": true, "unicode/utf8": true, "uc-pxx-installer/internal/avbab": true, "uc-pxx-installer/internal/plan": true,
	"uc-pxx-installer/internal/status": true}

// pureDenied are the names of the allowed imports that read files or a disk.
var pureDenied = map[string]bool{"uc-pxx-installer/internal/status.Collect": true,
	"uc-pxx-installer/internal/status.Source": true, "uc-pxx-installer/internal/plan.Compute": true,
	"uc-pxx-installer/internal/plan.VerifyOfficial": true}

// runnerBanned are the module packages the runner must not import: the
// write side lives in the child process only.
var runnerBanned = []string{"internal/install", "internal/device", "internal/devmode", "internal/bootctl",
	"internal/fakeemmc", "internal/pcdfw/pcdfwtest", "internal/nvram/nvramtest"}

// DefaultRules is the rule set of the repository.
func DefaultRules() []Rule {
	var r []Rule
	for _, d := range []string{"internal/status", "internal/ext4", "internal/bootimg", "internal/vendorstore",
		"internal/avbab", "internal/gpt", "internal/vbmeta", "internal/nvram", "internal/plan", "internal/fatfs",
		"internal/fatfs/fattest", "internal/drivestate"} {
		r = append(r, Rule{Dir: d, Kind: "reader"})
	}
	r = append(r,
		Rule{Dir: "internal/blk", Kind: "reader", Except: []string{"syscall.Syscall", "syscall.SYS_IOCTL", "unsafe.Pointer"}},
		Rule{Dir: "internal/devmem", Kind: "reader", Except: []string{"syscall.Mmap", "syscall.Munmap"}},
		Rule{Dir: "internal/flow", Kind: "pure"},
		Rule{Dir: "internal/fatmk", Kind: "nofile"},
		Rule{Dir: "internal/flowio", Kind: "runner", Allowed: map[string][]string{
			KindFile:    {"writeRunFile", "makeDriveFile", "removeRunFile", "superviseMain", "ejectDrive", "attachDrive", "takeUILock", "appendRemote"},
			KindProcess: {"startSupervised", "runReadonly", "superviseMain", "readDmesg"},
			KindSignal:  {"cancelPlan"},
			KindGadget:  {"ejectDrive", "attachDrive"},
		}},
	)
	return r
}

type checker struct {
	fset     *token.FileSet
	imp      types.Importer
	module   string
	findings []string
}

func (c *checker) add(pos token.Pos, f string, a ...any) {
	p := c.fset.Position(pos)
	c.findings = append(c.findings, fmt.Sprintf("%s:%d: %s", p.Filename, p.Line, fmt.Sprintf(f, a...)))
}

// Check runs the rules on the module in root. It returns the findings and
// the number of packages checked.
func Check(root string, rules []Rule) ([]string, int, error) {
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, 0, err
	}
	module := ""
	for _, l := range strings.Split(string(mod), "\n") {
		if strings.HasPrefix(l, "module ") {
			module = strings.TrimSpace(strings.TrimPrefix(l, "module "))
		}
	}
	c := &checker{fset: token.NewFileSet(), module: module}
	// the source importer resolves the imports with go/build (and go list)
	// from build.Default: it must look in this module
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, 0, err
	}
	build.Default.Dir = abs
	build.Default.CgoEnabled = false // as the release builds (CGO_ENABLED=0); no C compiler is needed
	c.imp = importer.ForCompiler(c.fset, "source", nil)
	n := 0
	for _, r := range rules {
		dir := filepath.Join(root, r.Dir)
		if _, err := os.Stat(dir); err != nil {
			if r.Kind == "runner" || r.Kind == "pure" {
				continue // not there yet
			}
			return nil, n, fmt.Errorf("%s: %v", r.Dir, err)
		}
		if err := c.checkPkg(root, r); err != nil {
			return nil, n, err
		}
		n++
	}
	sort.Strings(c.findings)
	return c.findings, n, nil
}

func (c *checker) checkPkg(root string, r Rule) error {
	dir := filepath.Join(root, r.Dir)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var files []*ast.File
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		// the files of the build (the default build tags; none of these
		// packages has files for one build only)
		if ok, err := build.Default.MatchFile(dir, n); err != nil || !ok {
			c.findings = append(c.findings, fmt.Sprintf("%s/%s: a file with a build constraint in a checked package", r.Dir, n))
			continue
		}
		f, err := parser.ParseFile(c.fset, filepath.Join(dir, n), nil, parser.ParseComments)
		if err != nil {
			return err
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return fmt.Errorf("%s: no Go files", r.Dir)
	}
	info := &types.Info{Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{},
		Types: map[ast.Expr]types.TypeAndValue{}}
	conf := types.Config{Importer: importerAt{c.imp, dir}, Error: func(error) {}}
	path := c.module + "/" + filepath.ToSlash(r.Dir)
	if _, err := conf.Check(path, c.fset, files, info); err != nil {
		return fmt.Errorf("%s: type check: %v", r.Dir, err)
	}
	except := map[string]bool{}
	for _, e := range r.Except {
		except[e] = true
	}
	for _, f := range files {
		c.checkFile(r, f, info, except)
	}
	return nil
}

// importerAt resolves imports from the package's own directory (module mode).
type importerAt struct {
	imp types.Importer
	dir string
}

func (i importerAt) Import(path string) (*types.Package, error) {
	if fi, ok := i.imp.(types.ImporterFrom); ok {
		return fi.ImportFrom(path, i.dir, 0)
	}
	return i.imp.Import(path)
}

func (c *checker) checkFile(r Rule, f *ast.File, info *types.Info, except map[string]bool) {
	for _, cg := range f.Comments {
		for _, cm := range cg.List {
			if strings.HasPrefix(cm.Text, "//go:linkname") {
				c.add(cm.Pos(), "go:linkname is not allowed")
			}
		}
	}
	for _, im := range f.Imports {
		p := strings.Trim(im.Path.Value, `"`)
		switch {
		case p == "C" || p == "plugin" || p == "net" || strings.HasPrefix(p, "net/"):
			c.add(im.Pos(), "import %q is not allowed", p)
		case r.Kind == "pure" && !pureImports[p]:
			c.add(im.Pos(), "the pure flow package imports %q (only %s)", p, keys(pureImports))
		case r.Kind == "nofile" && (p == "os" || p == "syscall" || p == "os/exec" || p == "unsafe" || p == "io/ioutil" || p == "io/fs"):
			c.add(im.Pos(), "a package that writes only to the writer it is given imports %q", p)
		case r.Kind == "reader" && (p == "os/exec" || p == "io/ioutil"):
			c.add(im.Pos(), "a reader package imports %q", p)
		case r.Kind == "reader" && p == "unsafe" && !except["unsafe.Pointer"]:
			c.add(im.Pos(), "a reader package imports unsafe")
		case r.Kind == "runner":
			for _, b := range runnerBanned {
				if p == c.module+"/"+b {
					c.add(im.Pos(), "the flow runner imports the install side %q: only the child process writes", p)
				}
			}
			if p == "unsafe" {
				c.add(im.Pos(), "the flow runner imports unsafe")
			}
		}
	}
	// each top-level function, so the runner rule knows where a call is
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		name := ""
		if ok {
			name = fn.Name.Name
		}
		ast.Inspect(d, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				c.selector(r, name, x, info, except)
			case *ast.CallExpr:
				c.call(r, name, x, info)
				c.callEscapes(r, name, x, info)
			case *ast.AssignStmt:
				if len(x.Lhs) == len(x.Rhs) {
					for i := range x.Lhs {
						c.escape(r, name, info.Types[x.Lhs[i]].Type, x.Rhs[i], info)
					}
				}
			case *ast.ValueSpec:
				if x.Type != nil && len(x.Values) == len(x.Names) {
					for _, v := range x.Values {
						c.escape(r, name, info.Types[x.Type].Type, v, info)
					}
				}
			case *ast.CompositeLit:
				c.litEscapes(r, name, x, info)
			}
			return true
		})
		if ok && fn.Type.Results != nil {
			var res []types.Type
			for _, f := range fn.Type.Results.List {
				t := info.Types[f.Type].Type
				for range max(1, len(f.Names)) {
					res = append(res, t)
				}
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				if _, lit := n.(*ast.FuncLit); lit {
					return false
				}
				if rs, isRet := n.(*ast.ReturnStmt); isRet && len(rs.Results) == len(res) {
					for i, e := range rs.Results {
						c.escape(r, name, res[i], e, info)
					}
				}
				return true
			})
		}
	}
}

func keys(m map[string]bool) string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return strings.Join(k, ", ")
}

// objName is "pkgpath.Name" of a package-level object, or "".
func objName(o types.Object) string {
	if o == nil || o.Pkg() == nil || o.Parent() != o.Pkg().Scope() {
		return ""
	}
	return o.Pkg().Path() + "." + o.Name()
}

func (c *checker) runnerAllowed(r Rule, fn, kind string) bool {
	for _, a := range r.Allowed[kind] {
		if a == fn {
			return true
		}
	}
	return false
}

func (c *checker) selector(r Rule, fn string, x *ast.SelectorExpr, info *types.Info, except map[string]bool) {
	// a method of *os.File
	if sel, ok := info.Selections[x]; ok {
		if sel.Kind() == types.MethodVal {
			recv := types.TypeString(sel.Recv(), nil)
			if (recv == "*os.File" || recv == "os.File") && fileWriteMethods[x.Sel.Name] {
				switch {
				case r.Kind == "runner" && c.runnerAllowed(r, fn, KindFile):
				default:
					c.add(x.Pos(), "(*os.File).%s writes (in %s)", x.Sel.Name, fnName(fn))
				}
			}
		}
		return
	}
	o := info.Uses[x.Sel]
	full := objName(o)
	if full == "" {
		return
	}
	pkg := o.Pkg().Path()
	short := o.Pkg().Name() + "." + o.Name()
	switch r.Kind {
	case "pure":
		if pureDenied[full] {
			c.add(x.Pos(), "the pure flow package uses %s, which reads files or a disk", short)
		}
		return
	case "reader":
		switch pkg {
		case "os", "syscall", "unsafe":
			if !readNames[short] && !except[short] {
				c.add(x.Pos(), "%s is not a read-only name (in %s)", short, fnName(fn))
			}
		}
	case "runner":
		switch pkg {
		case "syscall":
			switch {
			case strings.HasPrefix(short, "syscall.SIG") && o.Name() != "SIGKILL":
				// signal numbers (to ignore them, to read an end); only Kill sends one
			case short == "syscall.Kill":
				if !c.runnerAllowed(r, fn, KindSignal) {
					c.add(x.Pos(), "%s outside the signal functions %v (in %s)", short, r.Allowed[KindSignal], fnName(fn))
				}
			case short == "syscall.Flock" || short == "syscall.LOCK_EX" || short == "syscall.LOCK_NB" ||
				short == "syscall.LOCK_UN" || short == "syscall.EWOULDBLOCK" || short == "syscall.SysProcAttr" || short == "syscall.Sync":
				// the A/B lock is only taken to wait for the end (an O_RDONLY
				// open), the lock of the flow UI (its file is written only in
				// takeUILock), the supervisor's own session, a sync of the RAM drive
			case !readNames[short]:
				c.add(x.Pos(), "%s is not allowed in the flow runner (in %s)", short, fnName(fn))
			}
		case "os":
			switch {
			case readNames[short] || short == "os.Exit" || short == "os.Stdout" || short == "os.Stderr" ||
				short == "os.Signal" || short == "os.Getppid" || short == "os.Process" || short == "os.FindProcess":
			case short == "os.StartProcess":
				if !c.runnerAllowed(r, fn, KindProcess) {
					c.add(x.Pos(), "%s outside the process functions (in %s)", short, fnName(fn))
				}
			default:
				if !c.runnerAllowed(r, fn, KindFile) {
					c.add(x.Pos(), "%s writes or changes a file outside the file functions %v (in %s)", short, r.Allowed[KindFile], fnName(fn))
				}
			}
		case "os/exec":
			if !c.runnerAllowed(r, fn, KindProcess) {
				c.add(x.Pos(), "%s outside the process functions %v (in %s)", short, r.Allowed[KindProcess], fnName(fn))
			}
		case c.module + "/internal/gadget":
			switch o.Name() {
			case "Ejected", "Bound", "UDCState", "FirstUDC", "Config", "LunFile":
			default:
				if !c.runnerAllowed(r, fn, KindGadget) {
					c.add(x.Pos(), "gadget.%s changes the gadget outside %v (in %s)", o.Name(), r.Allowed[KindGadget], fnName(fn))
				}
			}
		}
	}
}

// isFile is an expression of static type *os.File.
func isFile(t types.Type) bool { return t != nil && types.TypeString(t, nil) == "*os.File" }

// writerIface is an interface type with a method that writes (io.Writer,
// io.WriterAt, io.StringWriter, io.ReadWriter, ...).
func writerIface(t types.Type) bool {
	if t == nil {
		return false
	}
	it, ok := t.Underlying().(*types.Interface)
	if !ok {
		return false
	}
	for i := 0; i < it.NumMethods(); i++ {
		if fileWriteMethods[it.Method(i).Name()] {
			return true
		}
	}
	return it.NumMethods() == 0 // any: a file in an "any" can be asserted back to a writer
}

// escape: an *os.File given to a writer interface outside the allowed file
// functions (a write through the interface could not be seen otherwise).
func (c *checker) escape(r Rule, fn string, dst types.Type, src ast.Expr, info *types.Info) {
	if !isFile(info.Types[src].Type) || !writerIface(dst) {
		return
	}
	if s, ok := src.(*ast.SelectorExpr); ok {
		if n := objName(info.Uses[s.Sel]); n == "os.Stdout" || n == "os.Stderr" {
			return // a message on the terminal
		}
	}
	if r.Kind == "runner" && c.runnerAllowed(r, fn, KindFile) {
		return
	}
	c.add(src.Pos(), "an *os.File is given to the writer interface %s (in %s)", types.TypeString(dst, nil), fnName(fn))
}

func (c *checker) callEscapes(r Rule, fn string, x *ast.CallExpr, info *types.Info) {
	ft := info.Types[x.Fun]
	if ft.IsType() {
		if len(x.Args) == 1 {
			c.escape(r, fn, ft.Type, x.Args[0], info)
		}
		return
	}
	sig, ok := ft.Type.(*types.Signature)
	if !ok {
		return
	}
	ps := sig.Params()
	for i, a := range x.Args {
		var pt types.Type
		switch {
		case sig.Variadic() && i >= ps.Len()-1:
			pt = ps.At(ps.Len() - 1).Type()
			if s, ok := pt.(*types.Slice); ok && !x.Ellipsis.IsValid() {
				pt = s.Elem()
			}
		case i < ps.Len():
			pt = ps.At(i).Type()
		}
		c.escape(r, fn, pt, a, info)
	}
}

func (c *checker) litEscapes(r Rule, fn string, x *ast.CompositeLit, info *types.Info) {
	t := info.Types[x].Type
	if t == nil {
		return
	}
	switch u := t.Underlying().(type) {
	case *types.Struct:
		for i, e := range x.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				if id, ok := kv.Key.(*ast.Ident); ok {
					for j := 0; j < u.NumFields(); j++ {
						if u.Field(j).Name() == id.Name {
							c.escape(r, fn, u.Field(j).Type(), kv.Value, info)
						}
					}
				}
			} else if i < u.NumFields() {
				c.escape(r, fn, u.Field(i).Type(), e, info)
			}
		}
	case *types.Slice:
		for _, e := range x.Elts {
			c.escape(r, fn, u.Elem(), e, info)
		}
	case *types.Map:
		for _, e := range x.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				c.escape(r, fn, u.Elem(), kv.Value, info)
			}
		}
	}
}

func fnName(fn string) string {
	if fn == "" {
		return "a top-level declaration"
	}
	return fn
}

// call checks the flag of os.OpenFile and syscall.Open.
func (c *checker) call(r Rule, fn string, x *ast.CallExpr, info *types.Info) {
	s, ok := x.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	full := objName(info.Uses[s.Sel])
	idx := -1
	switch full {
	case "os.OpenFile", "syscall.Open":
		idx = 1
	case "syscall.Openat":
		idx = 2
	}
	if idx < 0 || len(x.Args) <= idx {
		return
	}
	tv := info.Types[x.Args[idx]]
	if tv.Value == nil {
		if r.Kind == "runner" && c.runnerAllowed(r, fn, KindFile) {
			return
		}
		c.add(x.Pos(), "%s with a flag that is not a constant (in %s)", full, fnName(fn))
		return
	}
	v, ok := constant.Int64Val(tv.Value)
	if !ok {
		c.add(x.Pos(), "%s: the flag is not an integer", full)
		return
	}
	if v&writeBits != 0 {
		if r.Kind == "runner" && c.runnerAllowed(r, fn, KindFile) {
			return
		}
		c.add(x.Pos(), "%s opens for writing (flag %#x, in %s)", full, v, fnName(fn))
	}
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	f, n, err := Check(root, DefaultRules())
	if err != nil {
		fmt.Fprintln(os.Stderr, "goallow:", err)
		os.Exit(2)
	}
	for _, x := range f {
		fmt.Println("FAIL:", x)
	}
	if len(f) > 0 {
		os.Exit(1)
	}
	fmt.Printf("ok: no write path in %d packages (readers, the pure flow, the flow runner), by go/types\n", n)
}
