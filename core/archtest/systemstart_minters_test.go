// file: core/archtest/systemstart_minters_test.go
// version: 1.0.0
// guid: d6a2f850-7c19-4e3b-8b04-5f1e9c3a7d28
// last-edited: 2026-11-28

package archtest

import (
	"maps"
	"strings"
	"testing"
)

// minterTree is a fixture core module: the executor with the three minters, the
// kernel calling them, a plain package, and a test file that uses one. extra adds
// or replaces files.
func minterTree(t *testing.T, extra map[string]string) string {
	t.Helper()
	files := map[string]string{
		"go.mod": "module github.com/falkcorp/mediacore\n\ngo 1.27.0\n",
		"internal/executor/executor.go": `package executor

func ScheduleStager(x any) (any, error)   { return x, nil }
func OutboxRuns(x any) (any, error)       { return x, nil }
func CheckSystemStart(x any, m string) error { return nil }
func New() any                              { return nil }
`,
		"kernel/tasks.go": `package kernel

import "github.com/falkcorp/mediacore/internal/executor"

func Wire() { _, _ = executor.ScheduleStager(nil); _, _ = executor.OutboxRuns(nil); _ = executor.CheckSystemStart(nil, "") }
`,
		"gotham/gotham.go": `package gotham

import "github.com/falkcorp/mediacore/internal/executor"

func Signal() any { return executor.New() }
`,
		"gotham/gotham_test.go": `package gotham

import (
	"testing"

	"github.com/falkcorp/mediacore/internal/executor"
)

func TestMint(t *testing.T) { _, _ = executor.ScheduleStager(nil) }
`,
	}
	maps.Copy(files, extra)
	return writeModule(t, files)
}

// TestArchtest_SystemStartMinters: the kernel, the executor and test files are
// not reported; any other non-test file that names a minter, through a plain,
// renamed or dot import or a linkname, is; the guard refuses a tree without the
// executor; and real core passes.
func TestArchtest_SystemStartMinters(t *testing.T) {
	one := func(t *testing.T, extra map[string]string, wantFile, wantName string) {
		t.Helper()
		got, err := SystemStartMinterFindings(minterTree(t, extra))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || !strings.Contains(got[0], wantFile) || !strings.Contains(got[0], wantName) {
			t.Fatalf("findings = %q, want one naming %s and %s", got, wantFile, wantName)
		}
	}
	t.Run("the fixture is clean", func(t *testing.T) {
		got, err := SystemStartMinterFindings(minterTree(t, nil))
		if err != nil || len(got) != 0 {
			t.Fatalf("findings = %q, %v, want none", got, err)
		}
	})
	t.Run("a plain import call is reported", func(t *testing.T) {
		one(t, map[string]string{"gotham/gotham.go": `package gotham

import "github.com/falkcorp/mediacore/internal/executor"

func Signal() any { s, _ := executor.ScheduleStager(nil); return s }
`}, "gotham/gotham.go", "ScheduleStager")
	})
	t.Run("a renamed import is reported", func(t *testing.T) {
		one(t, map[string]string{"gotham/gotham.go": `package gotham

import ex "github.com/falkcorp/mediacore/internal/executor"

func Signal() any { r, _ := ex.OutboxRuns(nil); return r }
`}, "gotham/gotham.go", "OutboxRuns")
	})
	t.Run("a dot import is reported", func(t *testing.T) {
		one(t, map[string]string{"gotham/gotham.go": `package gotham

import . "github.com/falkcorp/mediacore/internal/executor"

func Signal() error { return CheckSystemStart(nil, "") }
`}, "gotham/gotham.go", "CheckSystemStart")
	})
	t.Run("a method value is reported", func(t *testing.T) {
		one(t, map[string]string{"gotham/gotham.go": `package gotham

import "github.com/falkcorp/mediacore/internal/executor"

var mint = executor.ScheduleStager
`}, "gotham/gotham.go", "ScheduleStager")
	})
	t.Run("a linkname is reported", func(t *testing.T) {
		one(t, map[string]string{"gotham/gotham.go": `package gotham

import _ "unsafe"

//go:linkname mint github.com/falkcorp/mediacore/internal/executor.OutboxRuns
func mint(x any) (any, error)
`}, "gotham/gotham.go", "OutboxRuns")
	})
	t.Run("a tree without the executor is refused", func(t *testing.T) {
		dir := writeModule(t, map[string]string{
			"go.mod":      "module github.com/falkcorp/mediacore\n\ngo 1.27.0\n",
			"gotham/g.go": "package gotham\n",
			"kernel/k.go": "package kernel\n",
		})
		if _, err := SystemStartMinterFindings(dir); err == nil || !strings.Contains(err.Error(), "vacuous") {
			t.Fatalf("err = %v, want the vacuous-guard refusal", err)
		}
	})
	t.Run("real core", func(t *testing.T) { SystemStartMinterCallers(t) })
}
