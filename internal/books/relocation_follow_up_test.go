// file: internal/books/relocation_follow_up_test.go
// version: 0.1.0
// guid: 41c6d9e0-7a2b-4835-9f1d-b0e85a3c62f7
// last-edited: 2026-11-28

package books

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/plugins/deluge"
)

// TestRelocationFollowUp_FollowsTheTasksFlip (F6-03): the durable relocation
// follow-up (the consumer name the manifest lists, the consumer, the store's
// staging) is one unit behind one predicate, identity AND the tasks flip. The
// consumer starts a task run: wired without books.deluge.path-update registered
// (the tasks flip's registration), core refuses the boot, and staged for no
// consumer a message waits for one.
func TestRelocationFollowUp_FollowsTheTasksFlip(t *testing.T) {
	listed := func() bool {
		return slices.Contains(flipOn().Manifest().OutboxConsumers, deluge.RelocationConsumerName)
	}
	if relocationFollowUpOn() || listed() {
		t.Fatalf("the follow-up is wired with the tasks flip off (predicate %v, consumer listed %v)", relocationFollowUpOn(), listed())
	}
	t.Cleanup(SetTasksFlipForTest(true))
	if !relocationFollowUpOn() || !listed() {
		t.Fatalf("the follow-up is not wired with the tasks flip on (predicate %v, consumer listed %v)", relocationFollowUpOn(), listed())
	}
}

// noRelocationOutbox is a store that reports no relocation outbox installed.
type noRelocationOutbox struct{}

func (noRelocationOutbox) RelocationOutboxInstalled() bool { return false }

// TestRelocationFollowUp_WiredWithoutAnOutboxFailsTheBoot (F6-03 item 8):
// Register's check refuses a store with no relocation outbox exactly when the
// follow-up is wired, and asks nothing when it is not.
func TestRelocationFollowUp_WiredWithoutAnOutboxFailsTheBoot(t *testing.T) {
	if err := requireRelocationWiring(noRelocationOutbox{}); err != nil {
		t.Fatalf("the follow-up is not wired and the check refused: %v", err)
	}
	t.Cleanup(SetTasksFlipForTest(true))
	if err := requireRelocationWiring(noRelocationOutbox{}); err == nil {
		t.Fatal("the follow-up is wired, the store has no relocation outbox, and the boot check passed")
	}
}

// TestRelocationFollowUp_RegisterRunsTheWiringCheck: the check above is useless
// if Register never calls it, and Register's body cannot run without a booted
// kernel (its store is bound inside the boot, and the booted flip-on build is not
// bootable until F5-18 registers books.deluge.path-update), so the call site is
// pinned from the source: Register, with a closure inside it, calls
// requireRelocationWiring.
func TestRelocationFollowUp_RegisterRunsTheWiringCheck(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "module.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var calls bool
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Register" || fn.Recv == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "requireRelocationWiring" {
					calls = true
				}
			}
			return !calls
		})
	}
	if !calls {
		t.Fatal("booksModule.Register does not call requireRelocationWiring: a wired follow-up over a store with no outbox would boot")
	}
}
