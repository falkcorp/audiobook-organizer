// file: core/examples/hello/specblocks_test.go
// version: 1.2.0
// guid: e51de200-5290-46a4-a134-5fb0ab239104
// last-edited: 2026-11-10

package hello_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/archtest"
)

const (
	// corePath is the core module's path.
	corePath = "github.com/falkcorp/mediacore"
	// specModule is the temp module the blocks are built in. A block's import
	// of a package the snapshot itself provides is rewritten to it, because
	// under corePath it would resolve to core's own copy of that package.
	specModule = "specblocks.test"
	// bootTimeout bounds one generated boot test.
	bootTimeout = 2 * time.Minute
)

// specBlock is one Go block of the spec: the file it names, relative to the
// core module root, and its text.
type specBlock struct {
	Dest string
	Text string
}

// specGroup is the blocks that make one package directory.
// specImport is one import of a block: its path and the name the file
// refers to it by (the alias, else the last path element).
type specImport struct{ path, name string }

type specGroup struct {
	dir   string            // relative to the core module root: examples/hello
	pkg   string            // package name of its non-test files
	files map[string]string // file name -> text
	core  []string          // core packages it imports, relative to the core module root
	names map[string]bool   // the names its files refer to existing core packages by (import name or alias); never a snapshot package
	deps  []string          // other snapshot directories it imports
	boot  bool              // a non-main package declaring func New() T: booted through coretest.New
}

// TestSpecGoBlocksCompile is 06 §6.10's COR-16 row: the Go blocks of 06 that
// make up the hello example are compiled against core in a temp module, and
// each module package is booted through coretest.New, so a manifest-level refusal
// fails the test as a compile error does. It reads the snapshot regen.py
// wrote under testdata/specblocks, never the spec, so a spec-only edit cannot
// break core CI.
//
// A package whose blocks import a core package that does not exist yet waits
// for it, and so does every package that imports it; the test names each one
// and why. A package whose every compile error names a core identifier that
// does not exist yet (an undefined core name, a missing field or method of a
// core type) waits for those identifiers in the same way, listed one by one
// (waitForCore). Since core/tasks landed (F2-01) every package of 06 compiles
// against existing packages, and hello waits only for identifiers later rows
// add (config.Handle, ui.Bundle, the Manifest and Registrar members), so the
// 06 subtest skips with that list, and the control snapshots below prove the
// harness compiles, boots, catches a compile error, catches a refusal and plans
// both kinds of skip. A package joins the build on its own the day its last
// missing core package and identifier land.
func TestSpecGoBlocksCompile(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := archtest.ModuleRoot(wd)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("06-app-sdk", func(t *testing.T) {
		blocks, err := loadSnapshot(filepath.Join("testdata", "specblocks"))
		if err != nil {
			t.Fatal(err)
		}
		build, waiting, err := planSpec(root, blocks)
		if err != nil {
			t.Fatal(err)
		}
		if build, err = waitForCore(t, root, build, waiting); err != nil {
			t.Fatal(err)
		}
		for _, dir := range slices.Sorted(maps.Keys(waiting)) {
			t.Logf("core/%s waits: %s", dir, waiting[dir])
		}
		// The waiting set is pinned, identifier by identifier, so the skip
		// below cannot hide a block that waits for a new reason (a typo in an
		// import or a name, a package or member the spec renamed). A row that
		// lands one of these identifiers shrinks the list here, in the same
		// PR; the row that lands the last one deletes the entry and the
		// blocks build.
		// Sorted; each line names the row that lands the identifier. The
		// five with no owning row are tracked in TODO.md (todo.d fragment
		// filed with F2-01).
		helloMissing := []string{
			"config.Handle",               // F3-01
			"module.Manifest.Permissions", // no owning row yet
			"module.Manifest.Settings",    // no owning row yet
			"module.Manifest.TaskLedger",  // F2-16/F5-18
			"module.Registrar.Resources",  // F2-16/F5-18
			"module.Registrar.Tasks",      // F2-17
			"module.Setting",              // no owning row yet
			"module.Settings",             // no owning row yet
			"module.SettingsSection",      // no owning row yet
			"ui.Bundle",                   // F9-03
		}
		wantWaiting := map[string]string{
			"examples/hello":           "missing in core: " + strings.Join(helloMissing, ", "),
			"examples/hello/cmd/hello": "imports core/examples/hello, which waits too",
		}
		if !maps.Equal(waiting, wantWaiting) {
			t.Fatalf("06's hello blocks wait for %v, want %v", waiting, wantWaiting)
		}
		if len(build) == 0 {
			t.Skipf("no package of 06's hello blocks compiles at this release: each waits for a core package or identifier (logged above)")
		}
		if err := buildSpec(t, root, build); err != nil {
			t.Fatal(err)
		}
	})

	for _, c := range controlSnapshots() {
		t.Run("control/"+c.name, func(t *testing.T) {
			build, waiting, err := planSpec(root, c.blocks)
			if err != nil {
				t.Fatal(err)
			}
			build, err = waitForCore(t, root, build, waiting)
			switch {
			case err != nil && c.wantErr != "" && strings.Contains(err.Error(), c.wantErr):
				return
			case err != nil:
				t.Fatal(err)
			}
			if !maps.Equal(waiting, c.waiting) {
				t.Fatalf("waiting = %v, want %v", waiting, c.waiting)
			}
			if len(build) == 0 {
				return
			}
			err = buildSpec(t, root, build)
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("the harness refused a good snapshot: %v", err)
			case c.wantErr != "" && err == nil:
				t.Fatalf("the harness passed a snapshot it must refuse (want an error containing %q)", c.wantErr)
			case c.wantErr != "" && !strings.Contains(err.Error(), c.wantErr):
				t.Fatalf("error = %v, want one containing %q", err, c.wantErr)
			}
		})
	}
}

// loadSnapshot reads dir's blocks.json and the .go.txt file of each block,
// and checks the snapshot is what regen.py wrote: every file listed, every
// sha256 matching.
func loadSnapshot(dir string) ([]specBlock, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "blocks.json"))
	if err != nil {
		return nil, err
	}
	var index struct {
		Blocks []struct {
			Dest     string `json:"dest"`
			Sha256   string `json:"sha256"`
			Snapshot string `json:"snapshot"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(raw, &index); err != nil {
		return nil, fmt.Errorf("blocks.json: %w", err)
	}
	listed := make(map[string]bool)
	var blocks []specBlock
	for _, b := range index.Blocks {
		text, err := os.ReadFile(filepath.Join(dir, b.Snapshot))
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(text)
		if got := hex.EncodeToString(sum[:]); got != b.Sha256 {
			return nil, fmt.Errorf("%s: sha256 %s, blocks.json records %s; rerun regen.py rather than edit a snapshot by hand", b.Snapshot, got, b.Sha256)
		}
		listed[b.Snapshot] = true
		blocks = append(blocks, specBlock{Dest: b.Dest, Text: string(text)})
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.go.txt"))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if !listed[filepath.Base(f)] {
			return nil, fmt.Errorf("%s is not in blocks.json; rerun regen.py", filepath.Base(f))
		}
	}
	if len(blocks) == 0 {
		return nil, errors.New("blocks.json lists no block; the test would be vacuous")
	}
	return blocks, nil
}

// planSpec groups blocks by package directory and splits the groups into the
// ones to build and the ones waiting (directory -> reason): a group waits when
// it imports a core package with no Go file yet, or a group that waits. The
// groups to build come back sorted by directory. A block that does not parse
// is an error, never a wait.
func planSpec(root string, blocks []specBlock) ([]*specGroup, map[string]string, error) {
	groups := make(map[string]*specGroup)
	imports := make(map[string][]specImport) // dir -> every import of its files
	for _, b := range blocks {
		dir, name := path.Split(b.Dest)
		dir = strings.TrimSuffix(dir, "/")
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, b.Dest, b.Text, parser.SkipObjectResolution)
		if err != nil {
			return nil, nil, fmt.Errorf("block %s does not parse: %w", b.Dest, err)
		}
		g := groups[dir]
		if g == nil {
			g = &specGroup{dir: dir, files: make(map[string]string)}
			groups[dir] = g
		}
		if _, dup := g.files[name]; dup {
			return nil, nil, fmt.Errorf("two blocks name %s", b.Dest)
		}
		g.files[name] = b.Text
		if !strings.HasSuffix(name, "_test.go") {
			g.pkg = f.Name.Name
			g.boot = g.boot || (f.Name.Name != "main" && declaresNew(f))
		}
		for _, is := range f.Imports {
			p, err := strconv.Unquote(is.Path.Value)
			if err != nil {
				return nil, nil, fmt.Errorf("block %s: import %s: %w", b.Dest, is.Path.Value, err)
			}
			name := path.Base(p)
			if is.Name != nil {
				name = is.Name.Name
			}
			imports[dir] = append(imports[dir], specImport{path: p, name: name})
		}
	}

	waiting := make(map[string]string)
	for dir, g := range groups {
		var missing []string
		for _, is := range imports[dir] {
			rel, ok := strings.CutPrefix(is.path, corePath+"/")
			if !ok {
				continue
			}
			switch _, inSnapshot := groups[rel]; {
			case rel == dir:
				// A package's external test imports the package itself.
			case inSnapshot:
				// A snapshot package is never "core": a name it lacks is
				// a typo in the blocks, so it stays out of names and
				// fails the compile.
				g.deps = append(g.deps, rel)
			case !hasGoFiles(filepath.Join(root, filepath.FromSlash(rel))):
				missing = append(missing, "core/"+rel)
			default:
				g.core = append(g.core, rel)
				if g.names == nil {
					g.names = make(map[string]bool)
				}
				g.names[is.name] = true
			}
		}
		if len(missing) > 0 {
			slices.Sort(missing)
			waiting[dir] = "no Go file yet in " + strings.Join(slices.Compact(missing), ", ")
		}
	}
	all := make([]*specGroup, 0, len(groups))
	for _, dir := range slices.Sorted(maps.Keys(groups)) {
		all = append(all, groups[dir])
	}
	return propagateWaits(all, waiting), waiting, nil
}

// propagateWaits marks every group that imports a waiting group as waiting
// too, and returns the groups that do not wait, in their given order.
func propagateWaits(groups []*specGroup, waiting map[string]string) []*specGroup {
	for changed := true; changed; {
		changed = false
		for _, g := range groups {
			if _, ok := waiting[g.dir]; ok {
				continue
			}
			for _, d := range g.deps {
				if _, ok := waiting[d]; ok {
					waiting[g.dir] = "imports core/" + d + ", which waits too"
					changed = true
					break
				}
			}
		}
	}
	var build []*specGroup
	for _, g := range groups {
		if _, ok := waiting[g.dir]; !ok {
			build = append(build, g)
		}
	}
	return build
}

// waitForCore compiles the groups planSpec would build, reporting every
// error (-gcflags=-e). A group whose every error names a core identifier that
// does not exist yet (missingCoreIdent) waits for those identifiers, sorted
// and de-duplicated, and so does every group that imports it; it is added to
// waiting and left out of the returned groups. Any other error, or an error
// line the classifier cannot read, is returned: a compile error is never a
// wait. A missing identifier never hides an independent compile error; an
// error downstream of a missing type (the type checker drops operands of an
// invalid type) surfaces in the row that lands the type.
func waitForCore(t *testing.T, root string, build []*specGroup, waiting map[string]string) ([]*specGroup, error) {
	t.Helper()
	if len(build) == 0 {
		return build, nil
	}
	tmp, _, _, err := writeSpecModule(t, root, build)
	if err != nil {
		return nil, err
	}
	out, err := goCmd(t, tmp, 0, "build", "-gcflags=-e", "./...")
	if err == nil {
		return build, nil
	}
	byDir := make(map[string]*specGroup, len(build))
	for _, g := range build {
		byDir[g.dir] = g
	}
	missing := make(map[string][]string)
	for line := range strings.Lines(string(out)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "# ") {
			continue
		}
		file, msg, ok := splitCompileError(line)
		g := byDir[path.Dir(file)]
		if !ok || g == nil {
			return nil, fmt.Errorf("compile: %w\n%s", err, out)
		}
		ident, ok := missingCoreIdent(msg, g.names)
		if !ok {
			return nil, fmt.Errorf("compile: %w\n%s", err, out)
		}
		missing[g.dir] = append(missing[g.dir], ident)
	}
	if len(missing) == 0 {
		return nil, fmt.Errorf("compile: %w\n%s", err, out)
	}
	for dir, idents := range missing {
		slices.Sort(idents)
		waiting[dir] = "missing in core: " + strings.Join(slices.Compact(idents), ", ")
	}
	return propagateWaits(build, waiting), nil
}

// splitCompileError splits "<file>:<line>:<col>: <message>".
func splitCompileError(line string) (file, msg string, ok bool) {
	m := compileErrorPattern().FindStringSubmatch(line)
	if m == nil {
		return "", "", false
	}
	return filepath.ToSlash(m[1]), m[2], true
}

func compileErrorPattern() *regexp.Regexp {
	return regexp.MustCompile(`^(\S+\.go):\d+:\d+: (.+)$`)
}

// missingCoreIdent reads one compiler message as a core identifier that does
// not exist yet, qualified by the core package name the block uses (names):
// "undefined: config.Handle" -> config.Handle; "unknown field TaskLedger in
// struct literal of type module.Manifest" -> module.Manifest.TaskLedger;
// "r.Tasks undefined (type *module.Registrar has no field or method Tasks)"
// -> module.Registrar.Tasks. Anything else, or a name that is not a core
// package's, is not a missing core identifier.
func missingCoreIdent(msg string, names map[string]bool) (string, bool) {
	for _, re := range missingIdentPatterns() {
		m := re.FindStringSubmatch(msg)
		if m == nil {
			continue
		}
		pkg, ident := m[re.SubexpIndex("pkg")], m[re.SubexpIndex("ident")]
		if !names[pkg] {
			return "", false
		}
		if i := re.SubexpIndex("member"); i > 0 {
			ident += "." + m[i]
		}
		return pkg + "." + ident, true
	}
	return "", false
}

func missingIdentPatterns() []*regexp.Regexp {
	return []*regexp.Regexp{
		regexp.MustCompile(`^undefined: (?P<pkg>[a-z][a-z0-9]*)\.(?P<ident>[A-Z]\w*)$`),
		regexp.MustCompile(`^unknown field (?P<member>[A-Z]\w*) in struct literal of type (?P<pkg>[a-z][a-z0-9]*)\.(?P<ident>[A-Z]\w*)$`),
		regexp.MustCompile(`^\S+ undefined \(type \*?(?P<pkg>[a-z][a-z0-9]*)\.(?P<ident>[A-Z]\w*) has no field or method (?P<member>[A-Z]\w*)\)$`),
	}
}

// declaresNew reports a top-level func New() with no parameters and one
// result: the constructor 06 §6.4.1 gives every module package.
func declaresNew(f *ast.File) bool {
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == "New" &&
			fd.Type.Params.NumFields() == 0 && fd.Type.Results.NumFields() == 1 {
			return true
		}
	}
	return false
}

// hasGoFiles reports a directory holding a non-test .go file: a package. A
// directory that holds only subpackages is not one.
func hasGoFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if n := e.Name(); !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			return true
		}
	}
	return false
}

// buildSpec writes the groups into a temp module that requires core through a
// replace of the core module root, builds every package, compiles every test
// file, and runs one generated test per module package that boots that
// package's New() through coretest.New, which fails the test on a boot error.
func buildSpec(t *testing.T, root string, build []*specGroup) error {
	t.Helper()
	tmp, boots, hasTests, err := writeSpecModule(t, root, build)
	if err != nil {
		return err
	}
	if out, err := goCmd(t, tmp, 0, "build", "./..."); err != nil {
		return fmt.Errorf("compile: %w\n%s", err, out)
	}
	if hasTests {
		if out, err := goCmd(t, tmp, 0, "test", "-count=1", "-run", "^$", "./..."); err != nil {
			return fmt.Errorf("compile tests: %w\n%s", err, out)
		}
	}
	for _, pkg := range boots {
		if out, err := goCmd(t, tmp, bootTimeout, "test", "-count=1", "-run", "^TestBoot$", "./"+pkg); err != nil {
			return fmt.Errorf("boot %s: %w\n%s", pkg, err, out)
		}
	}
	return nil
}

// writeSpecModule writes the groups into a fresh temp module (go.mod, core's
// go.sum, every block with its snapshot imports rewritten, and one boot test
// package per module package) and returns its directory, the boot packages
// and whether any test file was written.
func writeSpecModule(t *testing.T, root string, build []*specGroup) (tmp string, boots []string, hasTests bool, err error) {
	t.Helper()
	tmp = t.TempDir()
	gomod, err := specGoMod(root)
	if err != nil {
		return "", nil, false, err
	}
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), gomod, 0o644); err != nil {
		return "", nil, false, err
	}
	sum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		return "", nil, false, err
	}
	if err := os.WriteFile(filepath.Join(tmp, "go.sum"), sum, 0o644); err != nil {
		return "", nil, false, err
	}

	for _, g := range build {
		dir := filepath.Join(tmp, filepath.FromSlash(g.dir))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", nil, false, err
		}
		for name, text := range g.files {
			for _, d := range append([]string{g.dir}, g.deps...) {
				text = strings.ReplaceAll(text, strconv.Quote(corePath+"/"+d), strconv.Quote(specModule+"/"+d))
			}
			if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
				return "", nil, false, err
			}
			hasTests = hasTests || strings.HasSuffix(name, "_test.go")
		}
		if g.boot {
			pkg := "specboot/" + strings.ReplaceAll(g.dir, "/", "-")
			if err := os.MkdirAll(filepath.Join(tmp, filepath.FromSlash(pkg)), 0o755); err != nil {
				return "", nil, false, err
			}
			if err := os.WriteFile(filepath.Join(tmp, filepath.FromSlash(pkg), "boot_test.go"), bootTest(g), 0o644); err != nil {
				return "", nil, false, err
			}
			boots = append(boots, pkg)
		}
	}

	return tmp, boots, hasTests, nil
}

// specGoMod is core's go.mod renamed to specModule, without its ignore
// directives (their directories are core's), plus a require and a replace of
// core by its module root. Keeping core's requirements and exclude keeps the
// temp build's module graph core's own, so it needs no network.
func specGoMod(root string) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	renamed := false
	for line := range strings.Lines(string(raw)) {
		switch f := strings.Fields(line); {
		case len(f) == 2 && f[0] == "module":
			b.WriteString("module " + specModule + "\n")
			renamed = true
		case len(f) > 0 && f[0] == "ignore":
		default:
			b.WriteString(line)
		}
	}
	if !renamed {
		return nil, errors.New("core's go.mod has no module line")
	}
	fmt.Fprintf(&b, "\nrequire %s v0.0.0\n\nreplace %s => %s\n", corePath, corePath, strconv.Quote(root))
	return []byte(b.String()), nil
}

// bootTest is the test that boots g's module through coretest.New (06
// §6.4.8), the harness every later row's tests boot core through.
func bootTest(g *specGroup) []byte {
	return fmt.Appendf(nil, `package boot_test

import (
	"testing"

	"%s/coretest"
	m %q
)

func TestBoot(t *testing.T) { coretest.New(t, coretest.WithModules(m.New())) }
`, corePath, specModule+"/"+g.dir)
}

// goCmd runs the go command of the toolchain running this test in dir, with
// no workspace and no network, and returns its combined output. timeout 0 is
// the test's own deadline.
func goCmd(t *testing.T, dir string, timeout time.Duration, args ...string) ([]byte, error) {
	t.Helper()
	ctx := t.Context()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	bin := "go"
	if goroot := os.Getenv("GOROOT"); goroot != "" { // go test sets it to the running toolchain
		bin = filepath.Join(goroot, "bin", "go")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "GOPROXY=off")
	return cmd.CombinedOutput()
}

// controlSnapshot is a snapshot written for the harness, with what it must
// make of it.
type controlSnapshot struct {
	name    string
	blocks  []specBlock
	waiting map[string]string // directory -> the exact reason it waits; nil = none waits
	wantErr string            // "" = compiles and boots
}

// controlSnapshots prove the harness is not blind: a good module boots (and
// its main, importing it by its core path, compiles only if the import was
// rewritten into the temp module); a compile error, a reserved module ID, a
// missing core package and missing core identifiers are each caught, a
// missing identifier never hides an independent compile error, and a name a
// snapshot package lacks is a compile error, not a wait. Every control pins
// the exact waiting set. They are written here, not taken from a spec, so the 06 subtest's skip at R1 leaves the harness tested.
func controlSnapshots() []controlSnapshot {
	const batcave = `// core/examples/batcave/batcave.go
package batcave

import (
	"context"
	"net/http"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
)

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Manifest() module.Manifest {
	return module.Manifest{
		ID:             "batcave",
		Version:        "1.0.0",
		MinCoreVersion: "0.1.0",
		DefaultEnabled: true,
		PublicRoutes:   []string{"getSignal"},
	}
}

type Signal struct{}

func (*Module) Register(r *module.Registrar) error {
	httpapi.Handle(r.HTTP(), httpapi.Route{
		Method: http.MethodGet, Path: "/signal", Name: "getSignal", Perm: rbac.Public, Summary: "Read the signal",
	}, func(context.Context, *httpapi.In[struct{}]) (Signal, error) { return Signal{}, nil })
	return r.Err()
}
`
	const batcaveMain = `// core/examples/batcave/cmd/batcave/main.go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/examples/batcave"
)

func main() {
	if _, err := app.Describe(context.Background(), app.Options{AppID: "batcave"}, batcave.New()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
`
	const arkham = `// core/examples/arkham/arkham.go
package arkham

import "github.com/falkcorp/mediacore/module"

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Manifest() module.Manifest {
	return module.Manifest{ID: "api", Version: "1.0.0", MinCoreVersion: "0.1.0", DefaultEnabled: true}
}

func (*Module) Register(r *module.Registrar) error { return r.Err() }
`
	const joker = `// core/examples/joker/joker.go
package joker

type Module struct{}

func New() *Module { return laughingGas }
`
	const riddler = `// core/examples/riddler/riddler.go
package riddler

import (
	"github.com/falkcorp/mediacore/examples"
	"github.com/falkcorp/mediacore/nosuchpackage"
)

func New() any { return nil }
`
	const riddlerMain = `// core/examples/riddler/cmd/riddler/main.go
package main

import "github.com/falkcorp/mediacore/examples/riddler"

func main() { _ = riddler.New() }
`
	const penguin = `// core/examples/penguin/penguin.go
package penguin

import "github.com/falkcorp/mediacore/module"

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Manifest() module.Manifest {
	return module.Manifest{ID: "penguin", Version: "1.0.0", MinCoreVersion: "0.1.0", Umbrellas: 3}
}

func (*Module) Register(r *module.Registrar) error {
	r.Iceberg()
	var lounge module.Lounge
	r.Iceberg(lounge)
	return r.Err()
}
`
	const penguinMain = `// core/examples/penguin/cmd/penguin/main.go
package main

import "github.com/falkcorp/mediacore/examples/penguin"

func main() { penguin.New() }
`
	const twoFace = `// core/examples/twoface/twoface.go
package twoface

import "github.com/falkcorp/mediacore/module"

type Module struct{}

func New() *Module { return coinFlip }

func (*Module) Register(r *module.Registrar) error { r.Scar(); return r.Err() }
`
	return []controlSnapshot{
		{name: "batcave-boots", blocks: []specBlock{
			{Dest: "examples/batcave/batcave.go", Text: batcave},
			{Dest: "examples/batcave/cmd/batcave/main.go", Text: batcaveMain},
		}},
		{name: "arkham-reserved-id-refused", blocks: []specBlock{
			{Dest: "examples/arkham/arkham.go", Text: arkham},
		}, wantErr: "ReservedID"},
		{name: "joker-compile-error", blocks: []specBlock{
			{Dest: "examples/joker/joker.go", Text: joker},
		}, wantErr: "undefined: laughingGas"},
		{name: "riddler-waits", blocks: []specBlock{
			{Dest: "examples/riddler/riddler.go", Text: riddler},
			{Dest: "examples/riddler/cmd/riddler/main.go", Text: riddlerMain},
		}, waiting: map[string]string{
			"examples/riddler":             "no Go file yet in core/examples, core/nosuchpackage",
			"examples/riddler/cmd/riddler": "imports core/examples/riddler, which waits too",
		}},
		{name: "penguin-waits-on-identifiers", blocks: []specBlock{
			{Dest: "examples/penguin/penguin.go", Text: penguin},
			{Dest: "examples/penguin/cmd/penguin/main.go", Text: penguinMain},
		}, waiting: map[string]string{
			"examples/penguin":             "missing in core: module.Lounge, module.Manifest.Umbrellas, module.Registrar.Iceberg",
			"examples/penguin/cmd/penguin": "imports core/examples/penguin, which waits too",
		}},
		{name: "two-face-missing-identifier-does-not-hide-compile-error", blocks: []specBlock{
			{Dest: "examples/twoface/twoface.go", Text: twoFace},
		}, wantErr: "undefined: coinFlip"},
		{name: "batcave-typo-in-a-snapshot-package-is-a-compile-error", blocks: []specBlock{
			{Dest: "examples/batcave/batcave.go", Text: batcave},
			{Dest: "examples/batcave/cmd/batcave/main.go", Text: strings.Replace(batcaveMain, "batcave.New()", "batcave.Nwe()", 1)},
		}, wantErr: "undefined: batcave.Nwe"},
	}
}
