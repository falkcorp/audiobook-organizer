// file: internal/applygate/refusal_test.go
// version: 1.0.0
// guid: 1686cc61-ec00-47d3-a8ac-5f54f9190aed
// last-edited: 2026-10-30

package applygate

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// notStructural is every Reason* constant deliberately absent from
// structuralRefusals, with the cause in refusal.go's doc comment. A reason
// must be in exactly one of the two sets.
var notStructural = map[string]bool{
	ReasonIdentityStale:             true,
	ReasonPartialBook:               true,
	ReasonOwnerRejected:             true,
	ReasonOwnerRejectionCheckFailed: true,
	ReasonOwnerManualOnly:           true,
	ReasonOwnerManualCheckFailed:    true,
	ReasonOwnerReviewed:             true,
}

// reasonConstValues parses the package's non-test sources and returns the
// value of every string constant named Reason*.
func reasonConstValues(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		tree, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range tree.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if !strings.HasPrefix(n.Name, "Reason") || i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					v, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					out[n.Name] = v
				}
			}
		}
	}
	return out
}

// A new Reason* constant must be classified on purpose. Without this test a
// new refusal reason is silently "not structural" (safe) but nobody decided it.
func TestStructuralRefusal_EveryReasonClassified(t *testing.T) {
	consts := reasonConstValues(t)
	if len(consts) < 20 {
		t.Fatalf("found only %d Reason* constants; the source scan is broken", len(consts))
	}
	for name, val := range consts {
		structural := StructuralRefusal(val)
		if structural == notStructural[val] {
			t.Errorf("%s (%q): must be in exactly one of structuralRefusals and the notStructural list (structural=%v, listed non-structural=%v)",
				name, val, structural, notStructural[val])
		}
	}
	// Both sets hold only real reasons, so a typo cannot hide a gap.
	known := map[string]bool{}
	for _, v := range consts {
		known[v] = true
	}
	for v := range structuralRefusals {
		if !known[v] {
			t.Errorf("structuralRefusals lists %q, which is not a Reason* constant", v)
		}
	}
	for v := range notStructural {
		if !known[v] {
			t.Errorf("notStructural lists %q, which is not a Reason* constant", v)
		}
	}
}

func TestStructuralRefusal_UnknownAndEmptyAreNotStructural(t *testing.T) {
	for _, r := range []string{"", "something_new", "gate_blocked"} {
		if StructuralRefusal(r) {
			t.Errorf("StructuralRefusal(%q) = true; unknown reasons must default to re-evaluated", r)
		}
	}
}
