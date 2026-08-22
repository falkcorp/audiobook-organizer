// file: internal/plugins/acoustid/fingerprint_rescan_dedupe_test.go
// version: 1.0.0
// guid: 716787ee-e16c-4fd5-b762-06f6cf8ce256
// last-edited: 2026-08-22

package acoustid

import "testing"

// TestFingerprintRescanDef_DedupeDecisionIsExplicit is the acoustid half of the
// ENQ-DEDUP-1 per-def table test.
//
// acoustid.fingerprint-rescan carries an explicit book selection
// (fingerprintRescanParams.BookIDs, fingerprint_rescan.go:29), so a second
// request naming DIFFERENT books must queue a second run rather than be
// deduped onto the running one — the 2026-08-21 prod incident, where approving
// more books during a running metadata.batch-apply-cached applied nothing.
// EnqueueOp dedupes on def id alone for a def that sets DedupeQueuedRuns or
// carries a Schedule; this def must do neither.
//
// The main table test lives in internal/server (op_dedupe_decision_test.go) and
// enumerates the registry, but the acoustid plugin registers zero ops unless the
// service container can supply a dedup engine and an embedding store
// (register.go:28-36 plus the nil-engine guard at plugin.go:42-44), which a
// NewServer over a mock store cannot. The def is constructible here, so the
// assertion lives here rather than being dropped.
func TestFingerprintRescanDef_DedupeDecisionIsExplicit(t *testing.T) {
	// fingerprintRescanDef builds a literal; the engine is only touched inside
	// the Run closure, which this test never invokes.
	def := (&Plugin{}).fingerprintRescanDef()

	if def.ID != "acoustid.fingerprint-rescan" {
		t.Fatalf("def ID = %q, want %q — the table test in internal/server keys off this id",
			def.ID, "acoustid.fingerprint-rescan")
	}
	if def.ConcurrencyKey == "" {
		t.Error("ConcurrencyKey is empty: a second queued run would not be serialized by dispatcher Gate 3")
	}
	if def.DedupeQueuedRuns {
		t.Error("DedupeQueuedRuns is true: a request naming a different set of books would be " +
			"silently dropped in favour of the running op (prod, 2026-08-21)")
	}
	if def.Schedule != nil {
		t.Error("Schedule is non-nil: EnqueueOp's cron clause would dedupe this def on def id " +
			"alone and drop a differing book selection")
	}
}
