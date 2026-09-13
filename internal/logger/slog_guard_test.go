// file: internal/logger/slog_guard_test.go
// version: 1.0.0
// guid: 5c0e7a3b-9d41-4f6e-8b2a-71e4c9d0f3a8
// last-edited: 2026-09-13

package logger

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestGuard_NoDirectSlogCalls is the CI guard for go/log-injection. It fails
// when non-test Go code under internal/ or cmd/ calls a package-level log/slog
// logging function (slog.Info, slog.Warn, slog.Error, slog.Debug, slog.Log,
// slog.LogAttrs, the *Context variants) or slog.Default(), outside the
// ratchet in slog_guard_ratchet_test.go.
//
// Why: this package's Logger, OperationLogger and the std-logger bridge run
// every line through sanitizeLogLine. A direct slog call gets no barrier at
// all, which is how main came to hold 307 open go/log-injection alerts on
// 2026-09-13 (docs/audits/2026-09-13-log-injection-sweep.md).
//
// Two gates, deliberately split:
//   - This test is a CLASS gate. No new file may call slog directly, and a
//     ratcheted file may not gain calls: slogRatchet records the per-file call
//     count on the day the guard landed, and a file above its count fails.
//     A file leaves the ratchet only when every direct call in it is gone.
//   - CodeQL is the PER-ALERT gate. Wrapping a tainted value with
//     SanitizeLogValue closes the alert but leaves the slog call in place, so
//     a file fixed that way stays on the ratchet at its old count.
//
// Known gap: method calls on a *slog.Logger value (r.logger.Warn(...) in
// internal/operations/registry) are NOT matched. Without go/types a selector
// like x.logger.Info cannot be told apart from a call on this package's
// sanitizing Logger, so a name-based rule would flood the ratchet with false
// positives. Those sinks are left to CodeQL; see the audit doc.
var slogGuardFuncs = map[string]bool{
	"Debug": true, "Info": true, "Warn": true, "Error": true,
	"DebugContext": true, "InfoContext": true, "WarnContext": true, "ErrorContext": true,
	"Log": true, "LogAttrs": true, "Default": true,
}

// slogGuardExemptPrefixes are permanent exemptions, not ratchet entries.
// internal/logger is the sanitizing layer itself.
var slogGuardExemptPrefixes = []string{"internal/logger/"}

var slogGuardSkipDirs = map[string]bool{
	"testdata": true, "vendor": true, "node_modules": true,
}

func TestGuard_NoDirectSlogCalls(t *testing.T) {
	root := slogGuardRepoRoot(t)

	total := 0
	for _, n := range slogRatchet {
		total += n
	}
	if len(slogRatchet) > slogRatchetFileCeiling {
		t.Fatalf("slogRatchet has %d files, ceiling %d: the list may only shrink", len(slogRatchet), slogRatchetFileCeiling)
	}
	if total > slogRatchetCallCeiling {
		t.Fatalf("slogRatchet allows %d calls, ceiling %d: the counts may only go down", total, slogRatchetCallCeiling)
	}

	scanned := 0
	found := map[string]int{}
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, de fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if de.IsDir() {
				if slogGuardSkipDirs[de.Name()] || strings.HasPrefix(de.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			scanned++
			f, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if perr != nil {
				t.Errorf("parse %s: %v", rel, perr)
				return nil
			}
			if n := len(slogGuardViolations(f)); n > 0 {
				found[rel] = n
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", top, err)
		}
	}

	// Positive control: a walker that scanned nothing would pass forever.
	if scanned < 500 {
		t.Fatalf("scanned only %d Go files under %s; the walker is broken, not the code", scanned, root)
	}

	if os.Getenv("SLOG_GUARD_PRINT") != "" {
		keys := make([]string, 0, len(found))
		sum := 0
		for k, n := range found {
			if !slogGuardIsExempt(k) {
				keys = append(keys, k)
				sum += n
			}
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			fmt.Fprintf(&b, "\t%q: %d,\n", k, found[k])
		}
		t.Logf("files=%d calls=%d\n%s", len(keys), sum, b.String())
	}

	var bad []string
	for rel, n := range found {
		if slogGuardIsExempt(rel) {
			continue
		}
		allowed, ok := slogRatchet[rel]
		switch {
		case !ok:
			bad = append(bad, fmt.Sprintf("%s: %d direct slog call(s), file is not on the ratchet", rel, n))
		case n > allowed:
			bad = append(bad, fmt.Sprintf("%s: %d direct slog call(s), ratchet allows %d", rel, n, allowed))
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("%d file(s) call log/slog directly, which bypasses the log-injection barrier. "+
			"Log through internal/logger (logger.New / OperationLogger), or wrap every "+
			"user-controlled value with logger.SanitizeLogValue AND route the call through "+
			"this package. Do NOT add to slogRatchet or raise a count:\n\t%s",
			len(bad), strings.Join(bad, "\n\t"))
	}

	// Ratchet: entries must track reality downward, or the list stops shrinking.
	var stale []string
	for rel, allowed := range slogRatchet {
		if n := found[rel]; n < allowed {
			stale = append(stale, fmt.Sprintf("%s: ratchet allows %d, file now has %d", rel, allowed, n))
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("slogRatchet entries above the file's real count (or for files with none left); "+
			"lower or delete them and lower the ceilings:\n\t%s", strings.Join(stale, "\n\t"))
	}
}

// The rule itself, against synthetic source, so a rule that silently stops
// matching cannot hide behind the ratchet.
func TestGuard_SlogRuleMatches(t *testing.T) {
	cases := map[string]string{
		"info": `package p
import "log/slog"
func f(v string) { slog.Info("m", "k", v) }`,
		"alias": `package p
import stdlog "log/slog"
func f(v string) { stdlog.Warn("m", "k", v) }`,
		"default": `package p
import "log/slog"
func f() { slog.Default().Error("m") }`,
		"context": `package p
import "log/slog"
func f() { slog.ErrorContext(nil, "m") }`,
	}
	for name, src := range cases {
		f, err := parser.ParseFile(token.NewFileSet(), name+".go", src, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(slogGuardViolations(f)) == 0 {
			t.Errorf("%s: rule did not fire on:\n%s", name, src)
		}
	}
	clean := `package p
import (
	"log/slog"
	"example.com/other/slogx"
)
var h = slog.NewTextHandler(nil, nil)
func f(l *slog.Logger) { slog.SetDefault(slog.New(h)); slogx.Info("m"); _ = slog.String("k", "v") }`
	f, err := parser.ParseFile(token.NewFileSet(), "clean.go", clean, 0)
	if err != nil {
		t.Fatal(err)
	}
	if v := slogGuardViolations(f); len(v) > 0 {
		t.Errorf("clean source flagged: %v", v)
	}
}

// slogGuardViolations returns one entry per direct call, resolving the name
// log/slog was imported under (it is aliased as stdlog in places).
func slogGuardViolations(f *ast.File) []string {
	names := map[string]bool{}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if p != "log/slog" {
			continue
		}
		name := "slog"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name != "_" && name != "." {
			names[name] = true
		}
	}
	if len(names) == 0 {
		return nil
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && names[id.Name] && slogGuardFuncs[sel.Sel.Name] {
			out = append(out, id.Name+"."+sel.Sel.Name)
		}
		return true
	})
	return out
}

func slogGuardIsExempt(rel string) bool {
	for _, p := range slogGuardExemptPrefixes {
		if strings.HasPrefix(rel, p) {
			return true
		}
	}
	return false
}

// slogGuardRepoRoot walks up to the directory holding go.mod. Failing to find
// it is fatal: scanning the wrong tree would pass.
func slogGuardRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the package directory; cannot locate the repo root")
		}
		dir = parent
	}
}
