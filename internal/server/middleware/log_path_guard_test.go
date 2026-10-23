// file: internal/server/middleware/log_path_guard_test.go
// version: 1.0.0
// guid: ed298646-de05-412a-8292-324e5c6cd42d
// last-edited: 2026-10-22

package middleware

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// rawPathLogAllowed lists the files whose log calls may pass the request path
// as-is, with the reason. A file is only listed when every route it serves is
// outside the invite surface (/invite/<token>, /api/v1/auth/invite/<token>,
// /api/v1/users/invites/<token>[/resend]).
var rawPathLogAllowed = map[string]string{
	"absauthprobe.go": "the probe handler is mounted only on the Audiobookshelf auth routes",
}

// logCallNames are the method names that write a log record.
var logCallNames = map[string]bool{
	"Debug": true, "Info": true, "Warn": true, "Error": true,
	"DebugContext": true, "InfoContext": true, "WarnContext": true, "ErrorContext": true,
	"Printf": true, "Println": true, "Fatalf": true,
}

// isRequestURLPath reports whether e is <x>.URL.Path.
func isRequestURLPath(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Path" {
		return false
	}
	inner, ok := sel.X.(*ast.SelectorExpr)
	return ok && inner.Sel.Name == "URL"
}

// rawPathLogArgs returns the position of every <x>.URL.Path that is a direct
// argument of a log call (or nested in a non-redacting call within one).
func rawPathLogArgs(fset *token.FileSet, f *ast.File) []token.Position {
	var out []token.Position
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !logCallNames[sel.Sel.Name] {
			return true
		}
		for _, arg := range call.Args {
			out = append(out, unredactedPaths(fset, arg)...)
		}
		return true
	})
	return out
}

// unredactedPaths walks one log argument and reports a URL.Path that is not inside
// a call to a redactor (any function whose name contains "Redact", or cfLogPath).
func unredactedPaths(fset *token.FileSet, e ast.Expr) []token.Position {
	var out []token.Position
	var walk func(n ast.Node)
	walk = func(n ast.Node) {
		switch v := n.(type) {
		case *ast.CallExpr:
			name := ""
			switch fn := v.Fun.(type) {
			case *ast.Ident:
				name = fn.Name
			case *ast.SelectorExpr:
				name = fn.Sel.Name
			}
			if strings.Contains(name, "Redact") || name == "cfLogPath" {
				return
			}
			for _, a := range v.Args {
				walk(a)
			}
		case *ast.SelectorExpr:
			if isRequestURLPath(v) {
				out = append(out, fset.Position(v.Pos()))
				return
			}
			walk(v.X)
		}
	}
	walk(e)
	return out
}

// An invite token is a bearer secret in the request path, and /invite/<token> is a
// page route served by the SPA fallback behind this package's middleware. A log
// call that prints the request path must go through the invite redactor
// (logging.RedactInvitePath, directly or through a helper such as cfLogPath). This
// guard catches the next unredacted log site the way cfbounce.go's three were
// caught only on review.
func TestNoLogCallPrintsTheRawRequestPath(t *testing.T) {
	var files []string
	for _, glob := range []string{"*.go", "../../httputil/*.go"} {
		m, err := filepath.Glob(glob)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	if len(files) == 0 {
		t.Fatal("no source files found")
	}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		if _, ok := rawPathLogAllowed[filepath.Base(file)]; ok {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, pos := range rawPathLogArgs(fset, f) {
			t.Errorf("%s: a log call prints the raw request URL.Path; wrap it in logging.RedactInvitePath", pos)
		}
	}
}

// The guard itself: it must flag a bare path and accept a redacted one, or it would
// pass on any file.
func TestNoLogCallPrintsTheRawRequestPath_GuardSelfCheck(t *testing.T) {
	src := `package p
func f() {
	log.Info("a", c.Request.URL.Path)
	log.Warn("b", logger.SanitizeLogValue(c.Request.URL.Path))
	log.Warn("c", logging.RedactInvitePath(c.Request.URL.Path))
	log.Warn("d", logger.SanitizeLogValue(logging.RedactInvitePath(c.Request.URL.Path)))
	log.Warn("e", cfLogPath(c))
	other(c.Request.URL.Path)
}`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := rawPathLogArgs(fset, f)
	if len(got) != 2 || got[0].Line != 3 || got[1].Line != 4 {
		t.Fatalf("guard flagged %v, want exactly lines 3 and 4", got)
	}
}
