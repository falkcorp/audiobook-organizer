// file: internal/dedup/merge_journaled_paths_test.go
// version: 1.1.0
// guid: 8a4b1b59-2071-4042-bc11-03f0520916dd
// last-edited: 2026-09-10

// Regression tests for DA-02: the two UNATTENDED auto-merge triggers inside the
// engine (exact-file-hash auto-merge on the FullScan Layer-1 pass, and
// ApplyVerdicts' LLM high-confidence auto-merge) used to call
// mergeService.MergeBooks directly, so neither wrote an undo-ledger entry.
// A merge no human reviewed was exactly the merge with no way back.
//
// These require a real PebbleStore: the journal records book_ver copy-on-write
// snapshot timestamps, which the MockStore does not produce.

package dedup

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestHandleFileHashMatch_AutoMergeWritesJournalEntry pins the exact-file-hash
// auto-merge (engine.go handleFileHashMatch) to the journaled merge path. This
// path fires with zero human review whenever Dedup.AutoMergeEnabled is on.
func TestHandleFileHashMatch_AutoMergeWritesJournalEntry(t *testing.T) {
	engine, store, es := setupRealStoreEngine(t)
	engine.AutoMergeEnabled = true

	// Same normalized title and both AuthorID-less (so the author names compare
	// equal) — the two conditions handleFileHashMatch auto-merges on.
	a := arPlausibleBook("FHA", "Identical Title")
	b := arPlausibleBook("FHB", "Identical Title")
	if _, err := store.CreateBook(a); err != nil {
		t.Fatalf("CreateBook a: %v", err)
	}
	if _, err := store.CreateBook(b); err != nil {
		t.Fatalf("CreateBook b: %v", err)
	}

	merged, err := engine.handleFileHashMatch(a, b, "")
	if err != nil {
		t.Fatalf("handleFileHashMatch: %v", err)
	}
	if !merged {
		t.Fatal("expected the file-hash auto-merge to fire")
	}

	entries, err := es.ListAutoMergeJournalEntries(0)
	if err != nil {
		t.Fatalf("ListAutoMergeJournalEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("exact-file-hash auto-merge must write exactly 1 undo journal entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.WinnerID == "" || entry.LoserID == "" || entry.WinnerID == entry.LoserID {
		t.Fatalf("journal entry must name a distinct winner and loser, got winner=%q loser=%q", entry.WinnerID, entry.LoserID)
	}
	if entry.LoserPreMergeTS == 0 {
		t.Fatal("journal entry must record the loser's pre-merge snapshot, otherwise UnmergeAuto has nothing to revert to")
	}
}

// TestApplyVerdicts_LLMAutoMergeWritesJournalEntry pins ApplyVerdicts' LLM
// high-confidence auto-merge to the journaled merge path. Like the file-hash
// path it merges without a human ever seeing the pair.
func TestApplyVerdicts_LLMAutoMergeWritesJournalEntry(t *testing.T) {
	engine, store, es := setupRealStoreEngine(t)

	prev := config.AppConfig.Dedup.LLMAutoMergeHighConfidence
	config.AppConfig.Dedup.LLMAutoMergeHighConfidence = true
	t.Cleanup(func() { config.AppConfig.Dedup.LLMAutoMergeHighConfidence = prev })

	for _, id := range []string{"LLA", "LLB"} {
		if _, err := store.CreateBook(arPlausibleBook(id, "LLM Dup "+id)); err != nil {
			t.Fatalf("CreateBook %s: %v", id, err)
		}
	}
	if err := es.UpsertCandidate(database.DedupCandidate{
		EntityType:     "book",
		EntityAID:      "LLA",
		EntityBID:      "LLB",
		Layer:          "llm",
		Status:         "pending",
		FormulaVersion: "test",
	}); err != nil {
		t.Fatalf("UpsertCandidate: %v", err)
	}
	cands, _, err := es.ListCandidates(database.CandidateFilter{EntityType: "book", Status: "pending", Limit: 10})
	if err != nil || len(cands) != 1 {
		t.Fatalf("ListCandidates: got %d candidates, err=%v", len(cands), err)
	}

	applied := engine.ApplyVerdicts(
		[]ai.DedupPairVerdict{{Index: 0, IsDuplicate: true, Confidence: "high", Reason: "same book"}},
		map[int]database.DedupCandidate{0: cands[0]},
	)
	if applied != 1 {
		t.Fatalf("expected 1 verdict applied, got %d", applied)
	}

	entries, err := es.ListAutoMergeJournalEntries(0)
	if err != nil {
		t.Fatalf("ListAutoMergeJournalEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("LLM high-confidence auto-merge must write exactly 1 undo journal entry, got %d", len(entries))
	}
	if entries[0].CandidateID != cands[0].ID {
		t.Fatalf("journal entry must name the candidate that triggered the merge: want %d, got %d", cands[0].ID, entries[0].CandidateID)
	}
}

// TestMergeBooksJournaled_ClusterWritesOneEntryPerLoser covers the N-ary core
// the HTTP cluster-merge handlers need: AutoMergeJournalEntry is pairwise, so a
// cluster of N books must leave N-1 entries, one per loser, each naming the same
// surviving winner.
func TestMergeBooksJournaled_ClusterWritesOneEntryPerLoser(t *testing.T) {
	engine, store, es := setupRealStoreEngine(t)

	ids := []string{"CLA", "CLB", "CLC"}
	for _, id := range ids {
		if _, err := store.CreateBook(arPlausibleBook(id, "Cluster "+id)); err != nil {
			t.Fatalf("CreateBook %s: %v", id, err)
		}
	}

	result, keys, err := engine.MergeBooksJournaled(0, ids, "CLA", "dedup:merge-source:test")
	if err != nil {
		t.Fatalf("MergeBooksJournaled: %v", err)
	}
	if result.PrimaryID != "CLA" {
		t.Fatalf("keepID must win: want CLA, got %q", result.PrimaryID)
	}
	if len(keys) != 2 {
		t.Fatalf("a 3-book cluster must leave 2 journal keys, got %d", len(keys))
	}
	if keys[0] == keys[1] {
		t.Fatalf("journal keys must be distinct, both were %q", keys[0])
	}

	entries, err := es.ListAutoMergeJournalEntries(0)
	if err != nil {
		t.Fatalf("ListAutoMergeJournalEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 journal entries (one per loser), got %d", len(entries))
	}
	losers := map[string]bool{}
	for _, e := range entries {
		if e.WinnerID != "CLA" {
			t.Fatalf("every entry must name the surviving winner CLA, got %q", e.WinnerID)
		}
		if e.LoserPreMergeTS == 0 {
			t.Fatalf("entry for loser %s has no pre-merge snapshot", e.LoserID)
		}
		losers[e.LoserID] = true
	}
	if !losers["CLB"] || !losers["CLC"] {
		t.Fatalf("both losers must be journaled, got %v", losers)
	}
}

// TestHandleFileHashMatch_SkipsAutoMergeWithoutJournalStore covers the scan
// path's degraded branch. The three HTTP endpoints answer 503 when the undo
// journal is unreachable, but a scan-wide abort is worse than not merging one
// pair, so this path must skip quietly instead. The journal and the candidate
// table share embedStore, so with no embedStore there is no candidate row to
// fall back to either — falling through to upsertExactCandidate here would
// dereference the nil store and panic inside a scan worker.
func TestHandleFileHashMatch_SkipsAutoMergeWithoutJournalStore(t *testing.T) {
	engine, store, _ := setupRealStoreEngine(t)
	engine.AutoMergeEnabled = true
	engine.embedStore = nil

	a := arPlausibleBook("NJA", "No Journal Title")
	b := arPlausibleBook("NJB", "No Journal Title")
	for _, book := range []*database.Book{a, b} {
		if _, err := store.CreateBook(book); err != nil {
			t.Fatalf("CreateBook %s: %v", book.ID, err)
		}
	}

	merged, err := engine.handleFileHashMatch(a, b, "")
	if err != nil {
		t.Fatalf("a missing undo journal must not abort the scan, got err: %v", err)
	}
	if merged {
		t.Fatal("the pair must not be merged when the undo journal is unavailable")
	}

	after, err := store.GetBookByID("NJB")
	if err != nil || after == nil {
		t.Fatalf("GetBookByID NJB: book=%v err=%v", after, err)
	}
	if after.IsSoftDeleted() {
		t.Fatal("NJB was soft-deleted, so the merge happened unjournaled")
	}
}

// TestMergeBooksJournaled_RefusesWithoutJournalStore proves the fail-closed
// contract holds for the N-ary form too: with no embedding store there is
// nowhere to write an undo key, so the merge must not happen.
func TestMergeBooksJournaled_RefusesWithoutJournalStore(t *testing.T) {
	engine, _, _ := setupRealStoreEngine(t)
	engine.embedStore = nil

	if _, _, err := engine.MergeBooksJournaled(0, []string{"X", "Y"}, "", "t"); err == nil {
		t.Fatal("expected a refusal when the undo journal cannot be written")
	}
}
