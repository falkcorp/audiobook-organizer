// file: core/archtest/nolowerhashcost_test.go
// version: 1.0.0
// guid: 862dde3e-3019-497a-83ba-e9a9573f96c0
// last-edited: 2026-11-10

package archtest

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoweredHashCostDetection(t *testing.T) {
	cases := []struct {
		name, src string
		want      []string
	}{
		{"time", `package p
import "github.com/falkcorp/mediacore/auth/password"
var h = password.New(password.Options{Time: 1})`, []string{"3: Options.Time"}},
		{"memory and compute through an alias", `package p
import pw "github.com/falkcorp/mediacore/auth/password"
var h = pw.New(pw.Options{Permits: 2, MemoryKiB: 8,
	Compute: nil})`, []string{"3: Options.MemoryKiB", "4: Options.Compute"}},
		{"dot import", `package p
import . "github.com/falkcorp/mediacore/auth/password"
var o = Options{Time: 1}`, []string{"3: Options.Time"}},
		{"positional", `package p
import "github.com/falkcorp/mediacore/auth/password"
var o = password.Options{4, 1, 8, nil}`, []string{"3: Options{positional}"}},
		{"permits only", `package p
import "github.com/falkcorp/mediacore/auth/password"
var h = password.New(password.Options{Permits: 4})`, nil},
		{"another package's Options", `package p
import password "example.com/batcave/password"
var o = password.Options{Time: 1}`, nil},
		{"not imported", `package p
type Options struct{ Time int }
var o = Options{Time: 1}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := loweredHashCost("x.go", tc.src)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("hits = %v, want %v", got, tc.want)
			}
		})
	}
}

// NoLoweredHashCost over a scratch module reads the root main package and a
// file behind a build tag the build excludes, and skips test files and a
// test-support package.
func TestNoLoweredHashCost_ReadsRootAndBuildTaggedFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, src string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/gotham\n\ngo 1.24\n")
	lit := "import \"github.com/falkcorp/mediacore/auth/password\"\nvar o = password.Options{MemoryKiB: 8}\n"
	write("main.go", "package main\n"+lit+"func main() {}\n")
	write("main_test.go", "package main\n"+lit)
	write("tagged.go", "//go:build joker\n\npackage main\n"+lit)
	write("cave/cavetest/suite.go", "package cavetest\n"+lit)

	rec := &bcryptRecorder{TB: t}
	NoLoweredHashCost(rec, dir, CoreTestSupport)
	got := strings.Join(rec.errs, "\n")
	for _, want := range []string{"main.go:3: Options.MemoryKiB", "tagged.go:5: Options.MemoryKiB"} {
		if !strings.Contains(got, want) {
			t.Errorf("findings lack %q:\n%s", want, got)
		}
	}
	for _, bad := range []string{"main_test.go", "cavetest"} {
		if strings.Contains(got, bad) {
			t.Errorf("a test file or test-support package was reported (%s):\n%s", bad, got)
		}
	}
}
