// file: internal/server/server_maintenance_deps_triage_test.go
// version: 1.0.0
// guid: 858930a2-ded4-48a8-9404-abc75c5bd103
// last-edited: 2026-07-18

package server

import (
	"context"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	maintenanceplugin "github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
)

// newTriageTestEmbeddingStore creates a temp-dir-backed EmbeddingStore for
// server-level DedupTriageExactPending tests. Unlike internal/database's
// unexported newTestEmbeddingStore helper, this uses the exported
// database.NewEmbeddingStore constructor since this test lives outside the
// database package.
func newTriageTestEmbeddingStore(t *testing.T) *database.EmbeddingStore {
	t.Helper()
	dir := t.TempDir()
	db, err := pebble.Open(dir, &pebble.Options{})
	require.NoError(t, err)
	store := database.NewEmbeddingStore(db)
	t.Cleanup(func() { _ = db.Close() })
	return store
}

// t03TriageBook builds a *database.Book with the fields ClassifyCandidate
// inspects. fileSize < 0 or duration < 0 leaves the corresponding pointer nil.
func t03TriageBook(id, title string, fileSize int64, duration int, itunesPID string) database.Book {
	b := database.Book{ID: id, Title: title}
	if fileSize >= 0 {
		b.FileSize = &fileSize
	}
	if duration >= 0 {
		b.Duration = &duration
	}
	if itunesPID != "" {
		b.ITunesPersistentID = &itunesPID
	}
	return b
}

// t03HardSignalBreakdown returns a ScoreBreakdown carrying a hard signal
// (ISBN/ASIN), which ClassifyCandidate treats as TriageClassGenuine.
func t03HardSignalBreakdown() *unified.UnifiedDedupScore {
	return &unified.UnifiedDedupScore{Signals: []unified.Signal{{Kind: unified.SigISBNASIN, Confidence: 0.99}}}
}

// t03SoftSignalBreakdown returns a ScoreBreakdown with only a soft signal —
// insufficient on its own to classify genuine.
func t03SoftSignalBreakdown() *unified.UnifiedDedupScore {
	return &unified.UnifiedDedupScore{Signals: []unified.Signal{{Kind: unified.SigMetaFuzzy, Confidence: 0.5}}}
}

// t03TriageFixture wires an embeddingStore + MockStore with one candidate of
// each of the five triage classes, and returns the *Server plus the
// candidate IDs keyed by class for post-run status assertions.
func t03TriageFixture(t *testing.T) (srv *Server, ids map[maintenanceplugin.TriageClass]int64) {
	t.Helper()
	embStore := newTriageTestEmbeddingStore(t)

	books := map[string]database.Book{
		// genuine: hard ISBN signal, both real audio.
		"genuine-a": t03TriageBook("genuine-a", "Genuine Book", 10*1024*1024, 3600, ""),
		"genuine-b": t03TriageBook("genuine-b", "Genuine Book", 10*1024*1024, 3600, ""),
		// stub: book B is a byte-empty stub.
		"stub-a": t03TriageBook("stub-a", "Stub Pair A", 10*1024*1024, 3600, ""),
		"stub-b": t03TriageBook("stub-b", "Stub Pair B", 100, 1, ""),
		// fragment: duration ratio well under 5%.
		"frag-a": t03TriageBook("frag-a", "Fragment Chapter", 5*1024*1024, 120, ""),
		"frag-b": t03TriageBook("frag-b", "Fragment Full Book", 100*1024*1024, 5400, ""),
		// title_leak: both iTunes imports, exact layer, only a soft signal.
		"leak-a": t03TriageBook("leak-a", "Leaked Title", 5*1024*1024, 3600, "itunes-a"),
		"leak-b": t03TriageBook("leak-b", "Leaked Title", 5*1024*1024, 3500, "itunes-b"),
		// unknown: exact layer, soft signal only, non-iTunes, different titles.
		"unk-a": t03TriageBook("unk-a", "Unknown Book A", 5*1024*1024, 3600, ""),
		"unk-b": t03TriageBook("unk-b", "Unknown Book B", 5*1024*1024, 3600, ""),
	}

	store := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			b, ok := books[id]
			if !ok {
				return nil, nil
			}
			return &b, nil
		},
	}

	candidates := map[maintenanceplugin.TriageClass]database.DedupCandidate{
		maintenanceplugin.TriageClassGenuine: {
			EntityType: "book", EntityAID: "genuine-a", EntityBID: "genuine-b",
			Layer: "exact", ScoreBreakdown: t03HardSignalBreakdown(),
		},
		maintenanceplugin.TriageClassStub: {
			EntityType: "book", EntityAID: "stub-a", EntityBID: "stub-b",
			Layer: "exact", ScoreBreakdown: t03SoftSignalBreakdown(),
		},
		maintenanceplugin.TriageClassFragment: {
			EntityType: "book", EntityAID: "frag-a", EntityBID: "frag-b",
			Layer: "exact",
		},
		maintenanceplugin.TriageClassTitleLeak: {
			EntityType: "book", EntityAID: "leak-a", EntityBID: "leak-b",
			Layer: "exact", ScoreBreakdown: t03SoftSignalBreakdown(),
		},
		maintenanceplugin.TriageClassUnknown: {
			EntityType: "book", EntityAID: "unk-a", EntityBID: "unk-b",
			Layer: "exact", ScoreBreakdown: t03SoftSignalBreakdown(),
		},
	}

	ids = make(map[maintenanceplugin.TriageClass]int64, len(candidates))
	for cls, c := range candidates {
		id, _, err := embStore.UpsertCandidateNew(c)
		require.NoError(t, err)
		ids[cls] = id
	}

	srv = &Server{store: store, embeddingStore: embStore}
	return srv, ids
}

// TestDedupTriageExactPending_DryRun_DismissesNothing proves the preserved
// report-only contract: apply=false must classify candidates but leave every
// status at "pending" and DismissedCount at 0.
func TestDedupTriageExactPending_DryRun_DismissesNothing(t *testing.T) {
	srv, ids := t03TriageFixture(t)

	report, err := srv.DedupTriageExactPending(context.Background(), false)
	require.NoError(t, err)
	require.Equal(t, 5, report.TotalScanned)
	require.Equal(t, 0, report.DismissedCount)

	// Purgeable classes (stub, title_leak) show up as purgeable, not dismissed.
	require.Equal(t, 2, report.PurgeableCount)

	for cls, id := range ids {
		got, err := srv.embeddingStore.GetCandidateByID(id)
		require.NoError(t, err)
		require.Equalf(t, "pending", got.Status, "class %s must remain pending on dry-run", cls)
	}
}

// TestDedupTriageExactPending_Apply_DismissesOnlyPurgeable proves apply=true
// dismisses stub + title_leak candidates only, leaves genuine/fragment/unknown
// pending, and DismissedCount matches the number of statuses actually flipped.
func TestDedupTriageExactPending_Apply_DismissesOnlyPurgeable(t *testing.T) {
	srv, ids := t03TriageFixture(t)

	report, err := srv.DedupTriageExactPending(context.Background(), true)
	require.NoError(t, err)
	require.Equal(t, 5, report.TotalScanned)
	require.Equal(t, 2, report.DismissedCount, "stub + title_leak = 2 purgeable candidates")
	require.Equal(t, 2, report.PurgeableCount)

	wantStatus := map[maintenanceplugin.TriageClass]string{
		maintenanceplugin.TriageClassGenuine:   "pending",
		maintenanceplugin.TriageClassStub:      "dismissed",
		maintenanceplugin.TriageClassFragment:  "pending",
		maintenanceplugin.TriageClassTitleLeak: "dismissed",
		maintenanceplugin.TriageClassUnknown:   "pending",
	}
	for cls, id := range ids {
		got, err := srv.embeddingStore.GetCandidateByID(id)
		require.NoError(t, err)
		require.Equalf(t, wantStatus[cls], got.Status, "class %s status after apply=true", cls)
	}
}

// TestDedupTriageExactPending_Apply_SecondRunDoesNotRecountDismissed proves a
// dismissed candidate does not resurrect as pending on a later run: the
// second pass (regardless of apply) must scan strictly fewer candidates and
// must not double-count the already-dismissed pair.
func TestDedupTriageExactPending_Apply_SecondRunDoesNotRecountDismissed(t *testing.T) {
	srv, ids := t03TriageFixture(t)

	first, err := srv.DedupTriageExactPending(context.Background(), true)
	require.NoError(t, err)
	require.Equal(t, 2, first.DismissedCount)

	second, err := srv.DedupTriageExactPending(context.Background(), true)
	require.NoError(t, err)
	require.Equal(t, 3, second.TotalScanned, "the 2 dismissed candidates must not reappear as pending")
	require.Equal(t, 0, second.DismissedCount, "nothing new to dismiss — already-dismissed rows are excluded from the pending scan")

	// Statuses from the first run remain untouched (dismissed stays dismissed,
	// pending classes stay pending — no reclassification flip on rescan).
	for cls, id := range ids {
		got, err := srv.embeddingStore.GetCandidateByID(id)
		require.NoError(t, err)
		if cls == maintenanceplugin.TriageClassStub || cls == maintenanceplugin.TriageClassTitleLeak {
			require.Equal(t, "dismissed", got.Status)
		} else {
			require.Equal(t, "pending", got.Status)
		}
	}
}
