// file: internal/dedup/manual_candidate_protection_test.go
// version: 1.4.0
// guid: 8c81c949-8a0f-4c52-9f6b-839b956d087d
// last-edited: 2026-09-25

package dedup

import (
	"context"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

// Each test pairs a manual candidate with a scanner-layer CONTROL of the same
// shape and asserts the control IS removed. Without the control, a green test
// could only mean the fixture never reached the delete branch.

func mkBookAt(t *testing.T, store *database.PebbleStore, title, path string, group *string) *database.Book {
	t.Helper()
	dur := 3600
	primary := true
	b, err := store.CreateBook(&database.Book{Title: title, FilePath: path, Duration: &dur, VersionGroupID: group, IsPrimaryVersion: &primary})
	if err != nil {
		t.Fatalf("CreateBook %q: %v", title, err)
	}
	return b
}

func candByID(t *testing.T, es *database.EmbeddingStore, id int64) *database.DedupCandidate {
	t.Helper()
	c, err := es.GetCandidateByID(id)
	if err != nil {
		t.Fatalf("GetCandidateByID(%d): %v", id, err)
	}
	return c
}

// The same-directory rule is the prod shape the owner enqueues: a shell book
// sitting in the folder whose audio another book owns.
func TestPurgeStaleCandidates_KeepsManualCandidate(t *testing.T) {
	eng, store := newRescoreTestEngine(t)
	es := eng.embedStore

	shell := mkBookAt(t, store, "Grave Peril", "/lib/Butcher/Grave Peril/shell.m4b", nil)
	owner := mkBookAt(t, store, "Grave Peril", "/lib/Butcher/Grave Peril/01.mp3", nil)
	ctlA := mkBookAt(t, store, "Fracture", "/lib/X/Fracture/a.m4b", nil)
	ctlB := mkBookAt(t, store, "Fracture", "/lib/X/Fracture/b.m4b", nil)

	manual, err := es.EnqueueManualCandidate("book", shell.ID, owner.ID, "shell")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	sim := 0.95
	ctlID, _, err := es.UpsertCandidateNew(database.DedupCandidate{EntityType: "book", EntityAID: ctlA.ID, EntityBID: ctlB.ID, Layer: "embedding", Similarity: &sim})
	if err != nil {
		t.Fatalf("upsert control: %v", err)
	}

	deleted, err := eng.PurgeStaleCandidates(context.Background())
	if err != nil {
		t.Fatalf("PurgeStaleCandidates: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1 (the control only)", deleted)
	}
	if candByID(t, es, ctlID) != nil {
		t.Fatal("control candidate survived: the fixture never reached the same-directory rule")
	}
	got := candByID(t, es, manual.Candidate.ID)
	if got == nil || got.Status != "pending" {
		t.Fatalf("manual candidate purged or moved: %+v", got)
	}
}

// A same-path pair that also shares a version group matched the version-group
// purge rule, which ran before any same-path carve-out. It must survive; a
// different-path pair in one version group (the control) is still purged.
func TestPurgeStaleCandidates_KeepsSamePathPairInOneVersionGroup(t *testing.T) {
	eng, store := newRescoreTestEngine(t)
	es := eng.embedStore
	group := "vg-same"
	ctlGroup := "vg-ctl"
	path := "/lib/Author/Book/32.m4b"
	rowA := mkBookAt(t, store, "Same Path Book", path, &group)
	rowB := mkBookAt(t, store, "Same Path Book", path, &group)
	ctlA := mkBookAt(t, store, "Grouped Book", "/lib/A/g1.m4b", &ctlGroup)
	ctlB := mkBookAt(t, store, "Grouped Book", "/lib/B/g2.m4b", &ctlGroup)

	sim := 1.0
	spID, _, err := es.UpsertCandidateNew(database.DedupCandidate{EntityType: "book", EntityAID: rowA.ID, EntityBID: rowB.ID, Layer: "exact", Similarity: &sim})
	if err != nil {
		t.Fatalf("upsert same-path candidate: %v", err)
	}
	ctlID, _, err := es.UpsertCandidateNew(database.DedupCandidate{EntityType: "book", EntityAID: ctlA.ID, EntityBID: ctlB.ID, Layer: "embedding", Similarity: &sim})
	if err != nil {
		t.Fatalf("upsert control: %v", err)
	}

	if _, err := eng.PurgeStaleCandidates(context.Background()); err != nil {
		t.Fatalf("PurgeStaleCandidates: %v", err)
	}
	if candByID(t, es, ctlID) != nil {
		t.Fatal("control (one version group, different paths) survived: the fixture never reached the version-group rule")
	}
	if got := candByID(t, es, spID); got == nil || got.Status != "pending" {
		t.Fatalf("same-path candidate in one version group purged: %+v", got)
	}
}

// The same-directory rule (`filepath.Dir(a) == filepath.Dir(b)`) is trivially
// true for two rows at the IDENTICAL path, so without an explicit carve-out
// PurgeStaleCandidates purged the very same-path candidates the owner said
// must survive in the review queue (CHAPTER-SUBFOLDER-NN-ROWS, 2026-09-25).
// This pairs a same-path pair (must survive) with a same-DIRECTORY,
// DIFFERENT-path control (must still be purged as a genuine chapter pair).
func TestPurgeStaleCandidates_KeepsSamePathCandidate(t *testing.T) {
	eng, store := newRescoreTestEngine(t)
	es := eng.embedStore

	path := "/lib/Author/Book/Book - NN/32.m4b"
	rowA := mkBookAt(t, store, "Same Path Book", path, nil)
	rowB := mkBookAt(t, store, "Same Path Book", path, nil)
	// Control: genuinely different paths in the SAME directory — the exact
	// chapter-file shape this rule exists to purge.
	chapA := mkBookAt(t, store, "Chapter Book", "/lib/Author/Chapters/01.mp3", nil)
	chapB := mkBookAt(t, store, "Chapter Book", "/lib/Author/Chapters/02.mp3", nil)

	sim := 1.0
	samePathID, _, err := es.UpsertCandidateNew(database.DedupCandidate{EntityType: "book", EntityAID: rowA.ID, EntityBID: rowB.ID, Layer: "exact", Similarity: &sim})
	if err != nil {
		t.Fatalf("upsert same-path candidate: %v", err)
	}
	ctlID, _, err := es.UpsertCandidateNew(database.DedupCandidate{EntityType: "book", EntityAID: chapA.ID, EntityBID: chapB.ID, Layer: "embedding", Similarity: &sim})
	if err != nil {
		t.Fatalf("upsert control: %v", err)
	}

	deleted, err := eng.PurgeStaleCandidates(context.Background())
	if err != nil {
		t.Fatalf("PurgeStaleCandidates: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1 (the same-directory chapter control only)", deleted)
	}
	if candByID(t, es, ctlID) != nil {
		t.Fatal("control (same-directory chapter pair) survived: the fixture never reached the same-directory rule")
	}
	got := candByID(t, es, samePathID)
	if got == nil || got.Status != "pending" {
		t.Fatalf("same-path candidate purged or moved: %+v", got)
	}
}

// Unified scoring DELETES a suppressed pair (here: same version group). A
// manual pair of that shape must be left exactly as filed: not deleted, not
// re-layered, not given a band.
func TestUnifiedScoring_SkipsManualCandidate(t *testing.T) {
	eng, store := newRescoreTestEngine(t)
	es := eng.embedStore

	g1, g2 := "vg-manual", "vg-control"
	a := mkBookAt(t, store, "Shifter's Hoard 4", "/lib/a/one.m4b", &g1)
	b := mkBookAt(t, store, "Shifter's Hoard 4", "/lib/b/two.m4b", &g1)
	c := mkBookAt(t, store, "Other Book", "/lib/c/one.m4b", &g2)
	d := mkBookAt(t, store, "Other Book", "/lib/d/two.m4b", &g2)

	manual, err := es.EnqueueManualCandidate("book", a.ID, b.ID, "")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	sim := 0.97
	ctlID, _, err := es.UpsertCandidateNew(database.DedupCandidate{EntityType: "book", EntityAID: c.ID, EntityBID: d.ID, Layer: "embedding", Similarity: &sim})
	if err != nil {
		t.Fatalf("upsert control: %v", err)
	}

	cfg := unified.DefaultScoreConfig()
	for _, bk := range []*database.Book{a, c} {
		if err := eng.runUnifiedScoringForBook(context.Background(), bk, "", cfg); err != nil {
			t.Fatalf("runUnifiedScoringForBook(%s): %v", bk.ID, err)
		}
	}

	if candByID(t, es, ctlID) != nil {
		t.Fatal("control candidate survived: the fixture never reached the suppression delete")
	}
	got := candByID(t, es, manual.Candidate.ID)
	if got == nil {
		t.Fatal("manual candidate deleted by unified scoring")
	}
	if got.Layer != database.CandidateLayerManual || got.Band != "" || got.ScoreBreakdown != nil {
		t.Fatalf("manual candidate rewritten by unified scoring: layer=%q band=%q breakdown=%v", got.Layer, got.Band, got.ScoreBreakdown)
	}
}

// The pinned row is a scanner row WITH a stored breakdown, so it reaches the
// re-band branch; its unpinned twin (the control) must move HIGH→CERTAIN under
// the lowered ladder. The earlier version of this test used a freshly created
// manual row, which has no breakdown and is skipped as a legacy row whether or
// not the manual guard exists, so it could not fail.
func TestRescore_LeavesManualCandidate(t *testing.T) {
	eng, _ := newRescoreTestEngine(t)
	es := eng.embedStore
	seedRescorableCandidates(t, es, 2) // book-a-0/book-b-0 and book-a-1/book-b-1
	pin, err := es.EnqueueManualCandidate("book", "book-a-0", "book-b-0", "")
	if err != nil || !pin.Pinned {
		t.Fatalf("pin: %v (pinned=%v)", err, pin != nil && pin.Pinned)
	}
	cfg := unified.DefaultScoreConfig()
	cfg.BandCertainMin = 93
	if err := eng.SetScoreConfig(cfg); err != nil {
		t.Fatalf("SetScoreConfig: %v", err)
	}

	res, err := eng.Rescore(context.Background(), true)
	if err != nil {
		t.Fatalf("Rescore: %v", err)
	}
	if res.SkippedManual != 1 || res.Changed != 1 || res.Written != 1 {
		t.Fatalf("want skippedManual 1, changed 1, written 1; got %+v", res)
	}
	got := candByID(t, es, pin.Candidate.ID)
	if got == nil || got.Status != "pending" || got.Band != unified.BandHigh {
		t.Fatalf("manual candidate changed by rescore: %+v", got)
	}
	all, _, err := es.ListCandidates(database.CandidateFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	for _, c := range all {
		if c.ID != pin.Candidate.ID && c.Band != unified.BandCertain {
			t.Fatalf("control row was not re-banded: %+v", c)
		}
	}
}

func TestAutoResolveEligible_RefusesManualCandidate(t *testing.T) {
	eng, _ := newRescoreTestEngine(t)
	c := database.DedupCandidate{
		EntityType: "book", EntityAID: "a", EntityBID: "b",
		Band: unified.BandCertain, ScoreBreakdown: &unified.UnifiedDedupScore{Band: unified.BandCertain},
		Source: database.CandidateSourceManual,
	}
	ok, reason := eng.autoResolveEligible(c, &database.Book{ID: "a"}, &database.Book{ID: "b"})
	if ok {
		t.Fatal("a manual candidate must never be auto-resolved")
	}
	if !strings.Contains(reason, "manual") {
		t.Fatalf("refused for the wrong reason %q: the fixture must be refused by the manual gate, not a later one", reason)
	}
}

// The exact-file-hash auto-merge starts from two books, not a candidate row,
// so it must look the pair up: a pair a human pinned is never merged. The
// same fixture without the pin merges (TestHandleFileHashMatch_AutoMergeWritesJournalEntry).
func TestHandleFileHashMatch_PinnedPairNotAutoMerged(t *testing.T) {
	engine, store, es := setupRealStoreEngine(t)
	engine.AutoMergeEnabled = true
	a := arPlausibleBook("FHA", "Identical Title")
	b := arPlausibleBook("FHB", "Identical Title")
	if _, err := store.CreateBook(a); err != nil {
		t.Fatalf("CreateBook a: %v", err)
	}
	if _, err := store.CreateBook(b); err != nil {
		t.Fatalf("CreateBook b: %v", err)
	}
	pin, err := es.EnqueueManualCandidate("book", a.ID, b.ID, "")
	if err != nil {
		t.Fatalf("pin: %v", err)
	}

	merged, err := engine.handleFileHashMatch(a, b, "")
	if err != nil {
		t.Fatalf("handleFileHashMatch: %v", err)
	}
	if merged {
		t.Fatal("a pinned pair was auto-merged on a file-hash match")
	}
	got := candByID(t, es, pin.Candidate.ID)
	if got == nil || got.Status != "pending" || !database.IsManualCandidate(*got) {
		t.Fatalf("pinned candidate changed: %+v", got)
	}
}
