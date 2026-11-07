// file: tools/deadcode/surface/main_test.go
// version: 1.0.0
// guid: b84d2f61-0a37-4c95-8e1d-6f9c3a7e5120
// last-edited: 2026-11-07

package main

import (
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsPublic(t *testing.T) {
	cases := map[string]bool{
		"":                         true,
		"app":                      true,
		"storage/keys":             true,
		"metrics/metricstest":      true,
		"coretest":                 true,
		"kernel":                   false,
		"kernel/boot":              false,
		"internal":                 false,
		"internal/jsonschemax":     false,
		"app/internal/x":           false,
		"examples/hello":           false,
		"examples/hello/cmd/hello": false,
		"archtest":                 false,
		"app/testdata/x":           false,
		"kernelx":                  true, // only the kernel tree itself is out
		"x/kernel":                 true,
	}
	for rel, want := range cases {
		if got := isPublic(rel); got != want {
			t.Errorf("isPublic(%q) = %v, want %v", rel, got, want)
		}
	}
}

// writePkg writes files into a fresh dir and returns the pkg for them.
func writePkg(t *testing.T, importPath string, files map[string]string) pkg {
	t.Helper()
	dir := t.TempDir()
	p := pkg{ImportPath: importPath, Name: "p", Dir: dir}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(name, "_test.go") { // go list's GoFiles never holds tests
			p.GoFiles = append(p.GoFiles, name)
		}
	}
	return p
}

func gen(t *testing.T, pkgs ...pkg) (string, []string) {
	t.Helper()
	src, skipped, err := generate(pkgs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := format.Source(src); err != nil {
		t.Fatalf("output does not parse: %v", err)
	}
	return string(src), skipped
}

func TestFuncsAndMethodExpressions(t *testing.T) {
	p := writePkg(t, "example.com/m/app", map[string]string{
		"a.go": `package p

type App struct{}
type hidden struct{}
type Iface interface{ M() }

func New() *App                    { return nil }
func helper()                      {}
func (a *App) Handler()            {}
func (a App) ByValue()             {}
func (a *App) private()            {}
func (h *hidden) Exported()        {}
func init()                        {}
`,
		"b.go": `package p

func Describe() {}
`,
	})
	src, skipped := gen(t, p)
	for _, want := range []string{
		`p0 "example.com/m/app"`,
		"= p0.New\n",
		"= p0.Describe\n",
		"= (*p0.App).Handler\n",
		"= (*p0.App).ByValue\n", // value receivers use the pointer's method set
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in:\n%s", want, src)
		}
	}
	for _, bad := range []string{"helper", "private", "hidden", "init", "Iface", "Exported"} {
		if strings.Contains(src, bad) {
			t.Errorf("%q must not be referenced:\n%s", bad, src)
		}
	}
	if len(skipped) != 0 {
		t.Errorf("skipped = %v", skipped)
	}
	// Each reference is a typed package-level variable, never an []any.
	if strings.Contains(src, "[]any") {
		t.Errorf("surface must not convert to interfaces:\n%s", src)
	}
}

func TestTestFilesAreNotRead(t *testing.T) {
	p := writePkg(t, "example.com/m/x", map[string]string{
		"x.go":      "package p\n\nfunc Real() {}\n",
		"x_test.go": "package p\n\nfunc OnlyInTests() {}\n",
	})
	src, _ := gen(t, p)
	if strings.Contains(src, "OnlyInTests") || !strings.Contains(src, "p0.Real") {
		t.Errorf("unexpected surface:\n%s", src)
	}
}

func TestGenericsInstantiatedOrSkipped(t *testing.T) {
	p := writePkg(t, "example.com/m/httpapi", map[string]string{
		"g.go": `package p

import "fmt"

type Number interface{ ~int | ~float64 }

func Handle[T any](v T)                    {}
func Pair[K comparable, V interface{}]()   {}
func Grouped[A, B any]()                   {}
func Sum[T Number](v T) T                  { return v }
func Show[T fmt.Stringer](v T)             {}

type Box[T any] struct{ v T }

func (b *Box[T]) Get() T { return b.v }

type Num[T Number] struct{ v T }

func (n Num[T]) Value() T { return n.v }
`,
	})
	src, skipped := gen(t, p)
	for _, want := range []string{
		"= p0.Handle[struct{}]\n",
		"= p0.Pair[struct{}, struct{}]\n",
		"= p0.Grouped[struct{}, struct{}]\n",
		"= (*p0.Box[struct{}]).Get\n",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in:\n%s", want, src)
		}
	}
	wantSkipped := []string{
		"example.com/m/httpapi.Num.Value (generic type, non-trivial constraint)",
		"example.com/m/httpapi.Show (generic, non-trivial constraint)",
		"example.com/m/httpapi.Sum (generic, non-trivial constraint)",
	}
	if strings.Join(skipped, "|") != strings.Join(wantSkipped, "|") {
		t.Errorf("skipped = %v, want %v", skipped, wantSkipped)
	}
	// Documented in the generated file's header, not silent.
	for _, s := range wantSkipped {
		if !strings.Contains(src, "//\t"+s) {
			t.Errorf("skip not recorded in the header: %s", s)
		}
	}
	if strings.Contains(src, "p0.Sum") || strings.Contains(src, "p0.Show") || strings.Contains(src, "p0.Num") {
		t.Errorf("skipped members must not be referenced:\n%s", src)
	}
}

func TestPackageWithoutMembersIsStillImported(t *testing.T) {
	p := writePkg(t, "example.com/m/version", map[string]string{"v.go": "package p\n\nvar V = 1\n"})
	src, _ := gen(t, p)
	if !strings.Contains(src, `_ "example.com/m/version"`) {
		t.Errorf("a member-less package must still be loaded:\n%s", src)
	}
}

func TestOutputIsDeterministic(t *testing.T) {
	a := writePkg(t, "example.com/m/a", map[string]string{"a.go": "package p\n\nfunc A() {}\nfunc B() {}\n"})
	b := writePkg(t, "example.com/m/b", map[string]string{"b.go": "package p\n\nfunc C() {}\n"})
	one, _ := gen(t, a, b)
	two, _ := gen(t, b, a)
	if one != two {
		t.Errorf("order of input changed the output:\n%s\n---\n%s", one, two)
	}
}

func TestRunRefusesToShadowAnExistingDir(t *testing.T) {
	out := t.TempDir() // exists
	if err := run(".", out); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Errorf("run over an existing -out dir: err = %v", err)
	}
}
