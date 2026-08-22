// file: internal/database/mock_store_override_test.go
// version: 1.0.0
// guid: 1a8e0840-e3e1-4dd5-9e0f-38b2181b5090
// last-edited: 2026-08-22

package database

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestMockStore_EveryMethodIsOverridable is the regression guard for TASK-034.
//
// MockStore is a hand-maintained double, not mockery-generated, so nothing
// regenerates it and nothing but this test stops the next hand-written method
// from landing with a hardwired return and no override field. That is exactly
// how the 89 methods this task fixed accumulated: 313 of 399 followed the
// override pattern, so the pattern read as universal and the exceptions were
// invisible until a test author tried to override one and could not.
//
// It parses the source rather than grepping it because the acceptance criterion
// is structural — "every method body checks its own override" — and a text grep
// counts occurrences of a string, not methods that satisfy a shape. A method
// could mention "Func != nil" in a comment, or check a *different* method's
// override field, and a grep would score both as passing.
func TestMockStore_EveryMethodIsOverridable(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "mock_store.go", nil, 0)
	if err != nil {
		t.Fatalf("parse mock_store.go: %v", err)
	}

	var methods, missing []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Body == nil {
			continue
		}
		if !isMockStoreReceiver(fn.Recv) {
			continue
		}
		methods = append(methods, fn.Name.Name)
		if !hasOverrideGuard(fn.Body) {
			missing = append(missing, fn.Name.Name)
		}
	}

	// Positive control: without this, a parser that matched zero methods — a
	// renamed receiver type, a moved file, a silent parse of the wrong package —
	// would report "0 missing" and pass while checking nothing at all.
	if len(methods) < 300 {
		t.Fatalf("found only %d *MockStore methods; the scan is broken, not the mock "+
			"(expected ~399). Check the receiver-type match and the file path.", len(methods))
	}

	if len(missing) > 0 {
		t.Errorf("%d of %d *MockStore methods have no `if m.XFunc != nil` override guard, "+
			"so a test cannot override them:\n\t%s",
			len(missing), len(methods), strings.Join(missing, "\n\t"))
	}
}

// isMockStoreReceiver reports whether a method's receiver is *MockStore.
func isMockStoreReceiver(recv *ast.FieldList) bool {
	if len(recv.List) != 1 {
		return false
	}
	star, ok := recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == "MockStore"
}

// hasOverrideGuard reports whether a method body contains an `if <recv>.XFunc != nil`
// check anywhere in it. It deliberately accepts the guard at any depth rather than
// only as the first statement: a few methods legitimately validate an argument
// before consulting the override.
func hasOverrideGuard(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		bin, ok := ifStmt.Cond.(*ast.BinaryExpr)
		if !ok || bin.Op != token.NEQ {
			return true
		}
		nilIdent, ok := bin.Y.(*ast.Ident)
		if !ok || nilIdent.Name != "nil" {
			return true
		}
		sel, ok := bin.X.(*ast.SelectorExpr)
		if !ok || !strings.HasSuffix(sel.Sel.Name, "Func") {
			return true
		}
		found = true
		return false
	})
	return found
}

// TestMockStore_NewOverridesAreHonored exercises a sample of the fields this task
// added, chosen for the shapes that were most likely to be mis-transcribed across
// 89 near-identical edits rather than for coverage breadth: a plain zero-literal
// return, a return that reads a pre-existing error field, a computed return, and
// two whose blank `_` parameters had to be named before they could be forwarded.
//
// TestMockStore_EveryMethodIsOverridable is what covers all 89 structurally; this
// test is what proves the structure actually routes a value through.
func TestMockStore_NewOverridesAreHonored(t *testing.T) {
	t.Run("plain zero-literal return", func(t *testing.T) {
		m := &MockStore{
			GetAllWorkBookCountsFunc: func() (map[string]int, error) {
				return map[string]int{"work-1": 3}, nil
			},
		}
		got, err := m.GetAllWorkBookCounts()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got["work-1"] != 3 {
			t.Errorf("override not honored: got %v, want work-1=3", got)
		}
	})

	t.Run("computed fallback", func(t *testing.T) {
		m := &MockStore{
			MarkITunesSyncedFunc: func([]string) (int64, error) {
				return 99, nil
			},
		}
		// Three IDs, so the un-overridden fallback would return 3. A return of 99
		// can only have come from the override.
		got, err := m.MarkITunesSynced([]string{"a", "b", "c"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != 99 {
			t.Errorf("override not honored: got %d, want 99", got)
		}
	})

	t.Run("renamed blank params are forwarded", func(t *testing.T) {
		var gotPrimary, gotTitle string
		var gotSrcs []string
		var gotDuration float64
		m := &MockStore{
			MergeChapterBooksFunc: func(primaryID string, srcIDs []string, commonTitle string, totalDuration float64) error {
				gotPrimary, gotSrcs, gotTitle, gotDuration = primaryID, srcIDs, commonTitle, totalDuration
				return nil
			},
		}
		if err := m.MergeChapterBooks("book-1", []string{"book-2"}, "Some Title", 42.5); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Renaming `_` to a real name is what makes forwarding possible at all; if
		// any argument were dropped or transposed in that edit, it shows up here.
		if gotPrimary != "book-1" || gotTitle != "Some Title" || gotDuration != 42.5 ||
			len(gotSrcs) != 1 || gotSrcs[0] != "book-2" {
			t.Errorf("arguments not forwarded intact: primary=%q srcs=%v title=%q duration=%v",
				gotPrimary, gotSrcs, gotTitle, gotDuration)
		}
	})
}

// TestMockStore_NilOverridePreservesFallback is the other half of the change's
// central claim: it is purely additive, so every existing call site — all of which
// leave the override nil — keeps the exact behavior it had before. A fallback
// quietly replaced by a bare zero during the sweep would break callers that depend
// on it and would not be caught by any override test.
func TestMockStore_NilOverridePreservesFallback(t *testing.T) {
	t.Run("computed fallback still computes", func(t *testing.T) {
		m := &MockStore{}
		got, err := m.MarkITunesSynced([]string{"a", "b", "c"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != 3 {
			t.Errorf("fallback should return len(bookIDs)=3, got %d; the sweep "+
				"replaced a computed return with a literal", got)
		}
	})

	t.Run("error-field fallback still reads the field", func(t *testing.T) {
		sentinel := errRatingFallback
		m := &MockStore{UpdateBookRatingError: sentinel}
		if err := m.UpdateBookRating("book-1", UpdateBookRatingRequest{}); err != sentinel {
			t.Errorf("fallback should return UpdateBookRatingError, got %v; the sweep "+
				"dropped the pre-existing error field", err)
		}
	})

	t.Run("zero-literal fallback is unchanged", func(t *testing.T) {
		m := &MockStore{}
		got, err := m.GetAllWorkBookCounts()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || len(got) != 0 {
			t.Errorf("fallback should return an empty non-nil map, got %v", got)
		}
	})
}

// errRatingFallback is a distinct sentinel so the assertion cannot pass on a
// coincidental non-nil error from somewhere else in the mock.
var errRatingFallback = &mockOverrideTestError{}

type mockOverrideTestError struct{}

func (*mockOverrideTestError) Error() string { return "mock override test sentinel" }
