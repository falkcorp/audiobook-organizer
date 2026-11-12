// file: core/outbox/noglobals_test.go
// version: 1.0.0
// guid: 4d9a7e30-5b1c-4f28-8c6e-2a0f93b7d514
// last-edited: 2026-11-12

package outbox_test

import (
	"os"
	"testing"

	"github.com/falkcorp/mediacore/archtest"
	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/outbox"
)

// TestNoGlobals_EventsOutbox is the outbox half of 05-events §5E.12's R5 test:
// no init and no package-level mutable state in outbox or its store (the AST
// scan of archtest.NoInit), and two Outboxes share nothing: a consumer,
// a sequence, an idempotency key or a dead letter of one never reaches the
// other.
func TestNoGlobals_EventsOutbox(t *testing.T) {
	wd, err := os.Getwd()
	must(t, err)
	root, err := archtest.ModuleRoot(wd)
	must(t, err)
	archtest.NoInit(t, root, "./outbox/...")
	archtest.NoInit(t, root, "./internal/outboxstore/...")

	one := newRig(t, rigOpts{noExec: true, noFreeze: true, declared: map[string][]string{"gotham": {consumerC}}})
	two := newRig(t, rigOpts{noExec: true, noFreeze: true, declared: map[string][]string{"wayne": {consumerW}}})
	if one.ob.Declared(consumerW) || two.ob.Declared(consumerC) {
		t.Fatal("a consumer declared on one Outbox is declared on the other")
	}
	// The same registration is free on the second.
	rc := &recorder{}
	for _, r := range []*rig{one, two} {
		must(t, r.ob.Register("core", outbox.Consume(notice(), consumerN, rc.handler(consumerN))))
	}
	one.stage("wayne-manor", Dispatch{Villain: "Joker"})
	if n := two.count(outboxstore.InPrefix(noticeName)); n != 0 {
		t.Fatalf("a message staged on one Outbox is in the other's keyspace (%d rows)", n)
	}
	if one.count(outboxstore.InPrefix(noticeName)) != 1 {
		t.Fatal("the staged message is missing from its own Outbox")
	}
}
