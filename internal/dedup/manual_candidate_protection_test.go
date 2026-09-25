// file: internal/dedup/manual_candidate_protection_test.go
// version: 1.0.0
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

func TestRescore_LeavesManualCandidate(t *testing.T) {
	eng, store := newRescoreTestEngine(t)
	es := eng.embedStore
	a := mkBookAt(t, store, "A", "/lib/a/a.m4b", nil)
	b := mkBookAt(t, store, "B", "/lib/b/b.m4b", nil)
	manual, err := es.EnqueueManualCandidate("book", a.ID, b.ID, "")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := eng.Rescore(context.Background(), true); err != nil {
		t.Fatalf("Rescore: %v", err)
	}
	got := candByID(t, es, manual.Candidate.ID)
	if got == nil || got.Status != "pending" || got.Band != "" {
		t.Fatalf("manual candidate changed by rescore: %+v", got)
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
