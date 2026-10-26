// file: internal/plugins/acoustid/fingerprint_resetter_capability_test.go
// version: 1.2.0
// guid: 3a7f5e91-8b26-4c04-97d1-5e8a2b4f6c73
// last-edited: 2026-10-26

package acoustid

import (
	"context"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type resetCapableStore struct {
	database.Store
}

func (resetCapableStore) ClearAllAcoustIDFingerprints(context.Context, int, func(int, int, int)) (int, int, error) {
	return 3, 3, nil
}

type resetDecorator struct {
	database.Store
	inner database.Store
}

func (d resetDecorator) Unwrap() database.Store { return d.inner }

// TestResolveFingerprintResetterThroughDecorator pins the batched wipe.
// ClearAllAcoustIDFingerprints is not on database.Store (compile-probed), so a
// bare assertion fails through the production decorator — which is how this op
// degraded to the one-row-at-a-time path.
func TestResolveFingerprintResetterThroughDecorator(t *testing.T) {
	got := resolveFingerprintResetter(resetDecorator{inner: resetCapableStore{}})
	if got == nil {
		t.Fatal("resolveFingerprintResetter returned nil through the decorator; the reset would fall back to per-row deletes")
	}
	c, total, err := got.ClearAllAcoustIDFingerprints(context.Background(), 10, nil)
	if err != nil || c != 3 || total != 3 {
		t.Fatalf("ClearAllAcoustIDFingerprints = (%d, %d, %v), want (3, 3, nil)", c, total, err)
	}
}

func TestResolveFingerprintResetterOnUncapableBackend(t *testing.T) {
	type plain struct{ database.Store }
	if got := resolveFingerprintResetter(plain{}); got != nil {
		t.Fatalf("resolveFingerprintResetter = %v without the batched wipe, want nil", got)
	}
}

// busyResetStore reports a bulk clear that finished except for two rows other
// writers kept changing.
type busyResetStore struct{ database.Store }

func (busyResetStore) ClearAllAcoustIDFingerprints(context.Context, int, func(int, int, int)) (int, int, error) {
	return 7, 9, &database.BookFileSweepConflictError{Op: "ClearAllAcoustIDFingerprints", Rows: 2}
}

// TestResetAll_BusyRowsAreReportedNotFatal pins N4: a bulk clear that left rows
// busy (and so kept the LSH index) must not fail the op after it did almost all
// of its work. The run reports it as incomplete, tells the operator to run the
// reset again, and does not claim the rescan was withheld: the reset endpoint
// queues the rescan behind the reset either way.
func TestResetAll_BusyRowsAreReportedNotFatal(t *testing.T) {
	p := &Plugin{store: busyResetStore{}}
	r := &resetAllTestReporter{}
	if err := p.runResetAll(context.Background(), nil, r); err != nil {
		t.Fatalf("runResetAll = %v, want nil: busy rows are reported, not fatal", err)
	}
	var last string
	for _, f := range r.frames {
		last = f.message
	}
	if strings.Contains(last, "do not enqueue") || strings.Contains(last, "not queued") {
		t.Errorf("final message = %q, must not tell the operator the rescan has not been queued: the reset endpoint queues it behind the reset either way", last)
	}
	if !strings.Contains(last, "INCOMPLETE") || !strings.Contains(last, "2 files kept changing") {
		t.Errorf("final message = %q, want an INCOMPLETE summary naming the 2 busy files", last)
	}
	if strings.Contains(last, "Now enqueue") {
		t.Errorf("final message = %q: must not tell the operator to enqueue the rescan while rows are uncleared", last)
	}
}
