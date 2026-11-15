// file: internal/plugins/maintenance/kinds_t6_g4_loops_test.go
// version: 1.0.0
// guid: 633f4bb6-49d8-463f-9e52-e1bb347a81d6
// last-edited: 2026-11-14

package maintenance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"runtime"
	"strings"
	"testing"
)

// TestT6G4WorkersFor (review NIT 16): the width a loop of n items runs with is
// its declared Parallelism, capped at n, and never a clamp of a bad value to
// the CPU count.
func TestT6G4WorkersFor(t *testing.T) {
	cpu := max(runtime.NumCPU(), 1)
	for _, c := range []struct {
		name string
		o    t6g4EachOpts
		n    int
		want int
	}{
		{"cpu, more items than cores", t6g4EachOpts{Concurrency: t6g4CPU}, cpu + 50, cpu},
		{"cpu, fewer items than cores", t6g4EachOpts{Concurrency: t6g4CPU}, 1, 1},
		{"fixed(4), 10 items", t6g4EachOpts{Concurrency: 4}, 10, 4},
		{"fixed(4), 2 items", t6g4EachOpts{Concurrency: 4, Reason: "four at a time"}, 2, 2},
		{"sequential", t6g4EachOpts{Concurrency: 1}, 9, 1},
		{"zero is sequential, as at HEAD", t6g4EachOpts{}, 9, 1},
		{"no items", t6g4EachOpts{Concurrency: t6g4CPU}, 0, 0},
	} {
		if got := t6g4WorkersFor(c.o, c.n); got != c.want {
			t.Errorf("%s: t6g4WorkersFor = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestT6G4Loops_EveryFixedOrSequentialLoopNamesItsReason (review NIT 18): a
// Fixed or Sequential loop's Parallelism carries a reason, and the generic
// fallback names no cause. Every t6g4EachOpts literal in the package's
// production files whose Concurrency is not t6g4CPU must set Reason.
func TestT6G4Loops_EveryFixedOrSequentialLoopNamesItsReason(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	seen, withReason := 0, 0
	for _, e := range entries {
		fname := e.Name()
		if e.IsDir() || !strings.HasSuffix(fname, ".go") || strings.HasSuffix(fname, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, fname, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if id, ok := lit.Type.(*ast.Ident); !ok || id.Name != "t6g4EachOpts" {
				return true
			}
			seen++
			var conc ast.Expr
			hasReason := false
			for _, el := range lit.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				switch kv.Key.(*ast.Ident).Name {
				case "Concurrency":
					conc = kv.Value
				case "Reason":
					hasReason = true
				}
			}
			if id, ok := conc.(*ast.Ident); ok && id.Name == "t6g4CPU" {
				return true // CPU() takes no reason
			}
			if !hasReason {
				t.Errorf("%s: a t6g4EachOpts literal at %s has no Reason; a Fixed or Sequential loop must say what bounds it", fname, fset.Position(lit.Pos()))
				return true
			}
			withReason++
			return true
		})
	}
	if seen == 0 || withReason < 6 {
		t.Fatalf("scanned %d loop literals, %d with a reason: the scan no longer finds the group's loops", seen, withReason)
	}
}
