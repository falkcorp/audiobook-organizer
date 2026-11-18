// file: core/updater/selftest_test.go
// version: 1.0.0
// guid: 6c0e92b5-d814-4a37-8f2a-b3e5719c40d8
// last-edited: 2026-11-18

//go:build unix

package updater

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestSelfTest_VersionJSONFieldList: the self-test accepts exactly the five
// fields FE6-4 fixed for --version-json, all strings, and holds the binary to
// the app and the version it was fetched for.
func TestSelfTest_VersionJSONFieldList(t *testing.T) {
	if got := versionFields(); !slices.Equal(got, []string{"app", "version", "commit", "ui_build_id", "binary_id"}) {
		t.Fatalf("versionFields = %v", got)
	}
	run := func(t *testing.T, stdout string) error {
		t.Helper()
		exe := filepath.Join(t.TempDir(), "gotham-hq")
		if err := os.WriteFile(exe, scriptBinary("printf '%s\\n' '"+stdout+"'"), 0o755); err != nil {
			t.Fatal(err)
		}
		return selfTest(t.Context(), exe, testApp, "v0.224.0")
	}
	const ok = `{"app":"gotham","version":"v0.224.0","commit":"","ui_build_id":"dev","binary_id":"b1"}`

	if err := run(t, ok); err != nil {
		t.Fatalf("the exact field list was refused: %v", err)
	}
	t.Run("the leading v is optional on the binary's side", func(t *testing.T) {
		if err := run(t, strings.Replace(ok, `"v0.224.0"`, `"0.224.0"`, 1)); err != nil {
			t.Fatal(err)
		}
	})
	for name, body := range map[string]string{
		"an extra field":          `{"app":"gotham","version":"v0.224.0","commit":"","ui_build_id":"dev","binary_id":"b1","extra":"x"}`,
		"a missing field":         `{"app":"gotham","version":"v0.224.0","commit":"","ui_build_id":"dev"}`,
		"a renamed field":         `{"app":"gotham","version":"v0.224.0","commit":"","ui_build":"dev","binary_id":"b1"}`,
		"a non-string field":      `{"app":"gotham","version":"v0.224.0","commit":"","ui_build_id":"dev","binary_id":1}`,
		"another app":             strings.Replace(ok, `"gotham"`, `"metropolis"`, 1),
		"another version":         strings.Replace(ok, `"v0.224.0"`, `"v0.223.0"`, 1),
		"an empty version":        strings.Replace(ok, `"v0.224.0"`, `""`, 1),
		"not an object":           `["gotham"]`,
		"not JSON":                `gotham v0.224.0`,
		"two objects":             ok + "\n" + ok,
		"an app that only starts": strings.Replace(ok, `"gotham"`, `"gotham-x"`, 1),
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			if err := run(t, body); !errors.Is(err, ErrSelfTest) {
				t.Fatalf("err = %v, want ErrSelfTest", err)
			}
		})
	}
	t.Run("the error carries the two compared values and never the binary's output", func(t *testing.T) {
		err := run(t, strings.Replace(ok, `"gotham"`, `"metropolis"`, 1))
		if err == nil || !strings.Contains(err.Error(), "metropolis") || !strings.Contains(err.Error(), "gotham") {
			t.Fatalf("err = %v", err)
		}
		long := strings.Repeat("x", 500)
		err = run(t, strings.Replace(ok, `"gotham"`, `"`+long+`"`, 1))
		if err == nil || strings.Contains(err.Error(), long) {
			t.Errorf("a 500-byte app name went into the error whole: %v", err)
		}
	})
}

func TestSelfTest_BoundsTheRun(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "gotham-hq")
	big := scriptBinary(`yes aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa | head -c 2000000`)
	if err := os.WriteFile(exe, big, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := selfTest(t.Context(), exe, testApp, "v0.224.0"); !errors.Is(err, ErrSelfTest) {
		t.Fatalf("a binary printing 2 MB: %v, want ErrSelfTest", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the probe took too long on a chatty binary")
	}
}

// TestApply_OnlyPathIsManualTask: the only way into the download-and-swap
// pipeline is the core.updater.apply task body, and the apply definition
// carries no schedule (5U.4: nothing applies unattended).
func TestApply_OnlyPathIsManualTask(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg := pkgs["updater"]
	if pkg == nil {
		t.Fatal("package updater not parsed")
	}
	// callers[name] is every function that calls a method or function so named.
	callers := map[string][]string{}
	var applyTaskBody *ast.FuncDecl
	for _, f := range pkg.Files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			if fd.Name.Name == "ApplyTask" {
				applyTaskBody = fd
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fn := call.Fun.(type) {
				case *ast.SelectorExpr:
					// Calls on the updater itself (u.Apply), not on a field of it
					// (u.metrics.Apply counts an outcome).
					if _, direct := fn.X.(*ast.Ident); direct {
						callers[fn.Sel.Name] = append(callers[fn.Sel.Name], fd.Name.Name)
					}
				case *ast.Ident:
					callers[fn.Name] = append(callers[fn.Name], fd.Name.Name)
				}
				return true
			})
		}
	}
	only := func(callee string, allowed ...string) {
		t.Helper()
		for _, c := range callers[callee] {
			if !slices.Contains(allowed, c) {
				t.Errorf("%s is called from %s; only %v may call it", callee, c, allowed)
			}
		}
		if len(callers[callee]) == 0 {
			t.Errorf("%s has no caller in the package: the scan is looking at the wrong name", callee)
		}
	}
	only("Apply", "taskApply")
	only("install", "Apply", "Install")
	only("swap", "install")
	only("Rollback", "taskRollback")
	only("writeExecutable", "download")
	only("download", "install", "FetchRelease")
	// Install is the R0 gin path's entry (deprecated, deleted with that path by
	// F8-04): nothing in this package may call it, so the task path stays the
	// only way in from here.
	if got := callers["Install"]; len(got) != 0 {
		t.Errorf("Install is called from %v inside the package", got)
	}
	// A check, scheduled or not, never reaches the pipeline.
	for _, banned := range []string{"Apply", "Install", "install", "download", "swap", "FetchRelease", "Rollback"} {
		for _, c := range callers[banned] {
			if c == "runCheck" || c == "check" || c == "taskCheck" || c == "announce" {
				t.Errorf("%s calls %s: a check must never install", c, banned)
			}
		}
	}

	if applyTaskBody == nil {
		t.Fatal("ApplyTask not found")
	}
	ast.Inspect(applyTaskBody.Body, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && (id.Name == "Schedule" || id.Name == "ScheduleFromConfig") {
			t.Errorf("ApplyTask names %s at %s: the apply task must have no schedule", id.Name, fset.Position(id.Pos()))
		}
		return true
	})
}
