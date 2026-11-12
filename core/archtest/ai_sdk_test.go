// file: core/archtest/ai_sdk_test.go
// version: 1.0.0
// guid: 8c2e4f10-6a73-4d5b-b9e1-0f37a2d65c48
// last-edited: 2026-11-12

package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// writeAIModule lays out a scratch module from rel-path to source pairs.
func writeAIModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files["go.mod"] = "module example.com/gotham\n\ngo 1.24\n"
	for rel, src := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestArchtest_NoAISDKOutsideAdapters is the check's self-test (F7-03): over
// core's own packages it reports nothing, the SDK being imported by
// integrations/ai/openai and nowhere else, and over a synthetic module with one
// importer outside the adapter it reports that importer exactly once.
func TestArchtest_NoAISDKOutsideAdapters(t *testing.T) {
	t.Run("core_packages_no_report", func(t *testing.T) {
		NoAISDKOutsideAdapters(t, coreRoot(t))
	})
	t.Run("synthetic_outside_importer_one_report", func(t *testing.T) {
		imp := "package %s\nimport \"github.com/openai/openai-go/v3\"\nvar _ = openai.F\n"
		dir := writeAIModule(t, map[string]string{
			"integrations/ai/openai/adapter.go": fmt.Sprintf(imp, "openai"),
			"arkham/asylum.go":                  fmt.Sprintf(imp, "arkham"),
		})
		rec := &bcryptRecorder{TB: t}
		NoAISDKOutsideAdapters(rec, dir)
		if len(rec.errs) != 1 {
			t.Fatalf("want exactly one report, got %d: %v", len(rec.errs), rec.errs)
		}
		for _, want := range []string{
			"1 import(s) of the OpenAI SDK outside integrations/ai/openai",
			"arkham/asylum.go:2: imports github.com/openai/openai-go/v3",
		} {
			if !strings.Contains(rec.errs[0], want) {
				t.Errorf("the report lacks %q:\n%s", want, rec.errs[0])
			}
		}
		if strings.Contains(rec.errs[0], "integrations/ai/openai/adapter.go") {
			t.Errorf("the adapter's own import was reported:\n%s", rec.errs[0])
		}
	})
}

// TestAISDKImports_Detection reads one file at a time: the plain, alias, blank
// and dot imports of the SDK and its sub-packages are caught by import path,
// while a look-alike module path and an unrelated import are not.
func TestAISDKImports_Detection(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want int
	}{
		{"plain", "package p\nimport \"github.com/openai/openai-go/v3\"\nvar _ = openai.F\n", 1},
		{"sub-package", "package p\nimport \"github.com/openai/openai-go/v3/option\"\nvar _ = option.WithAPIKey\n", 1},
		{"alias", "package p\nimport oai \"github.com/openai/openai-go/v3\"\nvar _ = oai.F\n", 1},
		{"blank", "package p\nimport _ \"github.com/openai/openai-go/v3/shared\"\n", 1},
		{"dot", "package p\nimport . \"github.com/openai/openai-go/v3/packages/param\"\n", 1},
		{"two imports one file", "package p\nimport (\n\t\"github.com/openai/openai-go/v3\"\n\t\"github.com/openai/openai-go/v3/option\"\n)\n", 2},
		{"bare module path without a version", "package p\nimport \"github.com/openai/openai-go\"\n", 1},
		{"look-alike module", "package p\nimport \"github.com/openai/openai-gox\"\n", 0},
		{"another provider", "package p\nimport \"github.com/anthropics/anthropic-sdk-go\"\n", 0},
		{"no import", "package p\n", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeAIModule(t, map[string]string{"batcave/cave.go": tc.src})
			root, err := resolveRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			inside, outside, err := AISDKImports(root, []string{filepath.Join(root, "batcave", "cave.go")})
			if err != nil {
				t.Fatal(err)
			}
			if len(inside) != 0 {
				t.Fatalf("inside = %v, want none: the file is not in the adapter directory", inside)
			}
			if len(outside) != tc.want {
				t.Fatalf("outside = %v, want %d finding(s)", outside, tc.want)
			}
		})
	}
}

// TestNoAISDKOutsideAdapters_ScratchModule: the adapter directory may import
// the SDK, any other non-test file may not (one behind a build tag included),
// a test file and a nested module are left alone, and the finding names the
// file and line.
func TestNoAISDKOutsideAdapters_ScratchModule(t *testing.T) {
	imp := "package %s\nimport \"github.com/openai/openai-go/v3/option\"\nvar _ = option.WithAPIKey\n"
	dir := writeAIModule(t, map[string]string{
		"integrations/ai/openai/adapter.go":      fmt.Sprintf(imp, "openai"),
		"integrations/ai/openai/adapter_test.go": fmt.Sprintf(imp, "openai"),
		"integrations/ai/router.go":              "package ai\n",
		"arkham/asylum.go":                       fmt.Sprintf(imp, "arkham"),
		"arkham/tagged.go":                       "//go:build joker\n\n" + fmt.Sprintf(imp, "arkham"),
		"arkham/asylum_test.go":                  fmt.Sprintf(imp, "arkham"),
		"blackgate/go.mod":                       "module example.com/blackgate\n\ngo 1.24\n",
		"blackgate/b.go":                         fmt.Sprintf(imp, "blackgate"),
	})
	rec := &bcryptRecorder{TB: t}
	NoAISDKOutsideAdapters(rec, dir)
	got := strings.Join(rec.errs, "\n")
	for _, want := range []string{
		"arkham/asylum.go:2: imports github.com/openai/openai-go/v3/option",
		"arkham/tagged.go:4: imports github.com/openai/openai-go/v3/option",
		"2 import(s) of the OpenAI SDK outside integrations/ai/openai",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("findings lack %q:\n%s", want, got)
		}
	}
	for _, bad := range []string{"integrations/ai/openai/adapter.go", "asylum_test.go", "blackgate", "does not see the import"} {
		if strings.Contains(got, bad) {
			t.Errorf("findings name %q, which is allowed, a test file, a nested module or not a finding:\n%s", bad, got)
		}
	}
}

// TestNoAISDKOutsideAdapters_AdapterThatNoLongerImports: an adapter directory
// with Go files and no SDK import means the scan has stopped seeing the import
// it guards, so it fails rather than pass vacuously.
func TestNoAISDKOutsideAdapters_AdapterThatNoLongerImports(t *testing.T) {
	dir := writeAIModule(t, map[string]string{
		"integrations/ai/openai/adapter.go": "package openai\n",
		"integrations/ai/router.go":         "package ai\n",
	})
	rec := &bcryptRecorder{TB: t}
	NoAISDKOutsideAdapters(rec, dir)
	if got := strings.Join(rec.errs, "\n"); !strings.Contains(got, "none imports the SDK") {
		t.Fatalf("an adapter directory without the SDK import was not reported:\n%s", got)
	}

	// Without an adapter directory at all the check has nothing to guard and
	// passes: the adapter lands in a later change than the check.
	dir = writeAIModule(t, map[string]string{"integrations/ai/router.go": "package ai\n"})
	rec = &bcryptRecorder{TB: t}
	NoAISDKOutsideAdapters(rec, dir)
	if len(rec.errs) != 0 {
		t.Fatalf("a module without an adapter directory failed: %v", rec.errs)
	}
}

// fatalRecorder is a testing.TB that records Errorf and Fatalf without marking
// the real test failed; Fatalf ends only the goroutine it runs on.
type fatalRecorder struct {
	testing.TB
	msgs []string
}

func (r *fatalRecorder) Helper() {}
func (r *fatalRecorder) Errorf(f string, a ...any) {
	r.msgs = append(r.msgs, strings.TrimSpace(fmt.Sprintf(f, a...)))
}
func (r *fatalRecorder) Fatalf(f string, a ...any) {
	r.msgs = append(r.msgs, "FATAL "+strings.TrimSpace(fmt.Sprintf(f, a...)))
	runtime.Goexit()
}

// TestNoAISDKOutsideAdapters_VacuousScan: a module with no non-test Go file is
// a failed scan, not a passing one.
func TestNoAISDKOutsideAdapters_VacuousScan(t *testing.T) {
	dir := writeAIModule(t, map[string]string{"gotham/gotham_test.go": "package gotham\n"})
	rec := &fatalRecorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		NoAISDKOutsideAdapters(rec, dir)
	}()
	<-done
	if got := strings.Join(rec.msgs, "\n"); !strings.Contains(got, "FATAL") || !strings.Contains(got, "the check would be vacuous") {
		t.Fatalf("a scan over no files was not reported as a vacuous fatal:\n%s", got)
	}
}

// TestAISDKImports_SortedAndSplit: the findings are sorted and split by
// directory, so the output does not depend on map or walk order.
func TestAISDKImports_SortedAndSplit(t *testing.T) {
	imp := "package x\nimport \"github.com/openai/openai-go/v3\"\nvar _ = openai.F\n"
	dir := writeAIModule(t, map[string]string{
		"integrations/ai/openai/b.go": imp,
		"integrations/ai/openai/a.go": imp,
		"zubat/z.go":                  imp,
		"alfred/a.go":                 imp,
	})
	root, err := resolveRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	files, err := moduleSourceFiles(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	inside, outside, err := AISDKImports(root, files)
	if err != nil {
		t.Fatal(err)
	}
	wantIn := []string{
		"integrations/ai/openai/a.go:2: imports github.com/openai/openai-go/v3",
		"integrations/ai/openai/b.go:2: imports github.com/openai/openai-go/v3",
	}
	wantOut := []string{
		"alfred/a.go:2: imports github.com/openai/openai-go/v3",
		"zubat/z.go:2: imports github.com/openai/openai-go/v3",
	}
	if !reflect.DeepEqual(inside, wantIn) || !reflect.DeepEqual(outside, wantOut) {
		t.Fatalf("inside = %v, outside = %v\nwant %v and %v", inside, outside, wantIn, wantOut)
	}
}
