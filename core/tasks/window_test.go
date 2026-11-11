// file: core/tasks/window_test.go
// version: 1.0.0
// guid: 5d83459b-b9ee-450d-91e9-94da56e2134e
// last-edited: 2026-11-11

package tasks_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/tasks"
)

// TestWindowNightly_DeclaredOnceInTasks (name coined by F2-04): the window
// kind core.window.nightly, and every core.window. kind name, is spelled only
// in core/tasks/window.go (05-tasks 5T.8.2: core/tasks is its one owner). An
// AST scan of every non-test Go file under core/ finds no other string
// literal naming it.
func TestWindowNightly_DeclaredOnceInTasks(t *testing.T) {
	wd, err := os.Getwd() // not runtime.Caller: CI builds with -trimpath
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(wd) // core/
	var scanned int
	var hits []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "testdata", "node_modules", "web", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		scanned++
		rel, _ := filepath.Rel(root, path)
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err == nil && strings.HasPrefix(v, "core.window.") {
				hits = append(hits, rel+": "+v)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 100 {
		t.Fatalf("scanned %d files under %s; the scan would be vacuous", scanned, root)
	}
	want := []string{filepath.Join("tasks", "window.go") + ": core.window.nightly", filepath.Join("tasks", "window.go") + ": core.window."}
	if len(hits) != len(want) {
		t.Fatalf("core.window. literals: %v, want only %v", hits, want)
	}
	for i := range want {
		if hits[i] != want[i] {
			t.Fatalf("core.window. literals: %v, want only %v", hits, want)
		}
	}
}

func TestWindowKindAndName(t *testing.T) {
	if got := tasks.WindowKind("nightly"); got != tasks.WindowNightly {
		t.Fatalf("WindowKind(nightly) = %q, want %q", got, tasks.WindowNightly)
	}
	if _, err := tasks.ParseKind(string(tasks.WindowNightly)); err != nil {
		t.Fatalf("WindowNightly is not a kind: %v", err)
	}
	if tasks.WindowNightly.Module() != "core" {
		t.Fatal("the window kind is not core's")
	}
	if got := tasks.WindowKind("bat_signal"); got != "core.window.bat-signal" {
		t.Fatalf("WindowKind(bat_signal) = %q", got)
	}
	for _, bad := range []string{"", "Nightly", "night-ly", "9pm", "a.b"} {
		if got := tasks.WindowKind(bad); got != "" {
			t.Errorf("WindowKind(%q) = %q, want none", bad, got)
		}
	}
	for k, want := range map[tasks.Kind]string{tasks.WindowNightly: "nightly", "core.window.bat-signal": "bat_signal"} {
		if got, ok := tasks.WindowName(k); !ok || got != want {
			t.Errorf("WindowName(%s) = %q %v, want %q", k, got, ok, want)
		}
	}
	for _, k := range []tasks.Kind{"core.windows.nightly", "core.window.", "core.window.a.b", "gotham.window.nightly", "core.window.Nightly"} {
		if got, ok := tasks.WindowName(k); ok {
			t.Errorf("WindowName(%s) = %q, want not a window", k, got)
		}
	}
	if w := tasks.NightlyWindow(); w != (tasks.Window{Name: "nightly", Start: 1, End: 4}) {
		t.Fatalf("NightlyWindow() = %+v", w)
	}
}

// TestScheduleConfig_OneJSONValue (F2-04): ScheduleConfig encodes itself as
// its plain object, so config treats a schedule key as one key, and decodes
// the same object back (every field, the duration as a string).
func TestScheduleConfig_OneJSONValue(t *testing.T) {
	for _, c := range []tasks.ScheduleConfig{
		{Enabled: true, Every: config.Duration(24 * time.Hour)},
		{Enabled: true, DailyAt: "03:30"},
		{Enabled: true, Cron: "*/15 1-4 * * 1-5"},
		{Enabled: true, Window: "nightly", Order: 3},
		{},
	} {
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		var back tasks.ScheduleConfig
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatalf("%s: %v", b, err)
		}
		if back != c {
			t.Fatalf("round trip of %+v through %s = %+v", c, b, back)
		}
	}
	b, _ := json.Marshal(tasks.ScheduleConfig{Enabled: true, Every: config.Duration(time.Hour)})
	if string(b) != `{"enabled":true,"every":"1h"}` {
		t.Fatalf("encoding = %s", b)
	}
	var c tasks.ScheduleConfig
	if err := json.Unmarshal([]byte(`{"enabled":true,"every":"5m","order":2}`), &c); err != nil ||
		c != (tasks.ScheduleConfig{Enabled: true, Every: config.Duration(5 * time.Minute), Order: 2}) {
		t.Fatalf("decode = %+v, %v", c, err)
	}
	if err := json.Unmarshal([]byte(`{"enabled":"yes"}`), &c); err == nil {
		t.Fatal("a mistyped field decoded")
	}
	var _ json.Marshaler = tasks.ScheduleConfig{}
	var _ json.Unmarshaler = &c
}
