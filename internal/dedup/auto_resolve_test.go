// file: internal/dedup/auto_resolve_test.go
// version: 1.1.0
// guid: 2f4b8c19-7a03-4d56-9e18-5c0d7f2a6b91
// last-edited: 2026-07-13

// Tests for the Tier-1 (Band CERTAIN) auto-resolution pass:
// Engine.AutoResolveCertain, autoResolveEligible, and UnmergeAuto.
//
// Eligibility tests run against the MockStore-backed engine (no CoW needed).
// Apply / journal / unmerge round-trip tests require a real PebbleStore because
// the reversibility path depends on book_ver copy-on-write snapshots, which the
// MockStore does not produce.

package dedup

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// certainBreakdown builds a CERTAIN UnifiedDedupScore with the given primary
// signal kinds (each at confidence 0.99) and no suppressors.
func certainBreakdown(aID, bID string, kinds ...unified.SignalKind) *unified.UnifiedDedupScore {
	sigs := make([]unified.Signal, 0, len(kinds))
	for _, k := range kinds {
		sigs = append(sigs, unified.Signal{Kind: k, Confidence: 0.99, Raw: 1})
	}
	return &unified.UnifiedDedupScore{
		Pair:    [2]string{aID, bID},
		Score:   100,
		Band:    unified.BandCertain,
		Signals: sigs,
	}
}

func arPlausibleBook(id, title string) *database.Book {
	d := 3600
	return &database.Book{ID: id, Title: title, Duration: &d}
}

// --- Eligibility unit tests (MockStore) ---

func TestAutoResolveEligible(t *testing.T) {
	engine, _, es := setupTestEngine(t)

	bookA := arPlausibleBook("A", "Same Book")
	bookB := arPlausibleBook("B", "Same Book")

	base := func() database.DedupCandidate {
		return database.DedupCandidate{
			ID:             1,
			EntityType:     "book",
			EntityAID:      "A",
			EntityBID:      "B",
			Band:           unified.BandCertain,
			Status:         "pending",
			ScoreBreakdown: certainBreakdown("A", "B", unified.SigExactFile, unified.SigISBNASIN),
		}
	}

	t.Run("accepts CERTAIN with 2 primary signals", func(t *testing.T) {
		ok, reason := engine.autoResolveEligible(base(), bookA, bookB)
		if !ok {
			t.Fatalf("expected eligible, got not eligible: %s", reason)
		}
	})

	t.Run("rejects band != CERTAIN", func(t *testing.T) {
		c := base()
		c.Band = unified.BandHigh
		c.ScoreBreakdown.Band = unified.BandHigh
		if ok, _ := engine.autoResolveEligible(c, bookA, bookB); ok {
			t.Fatal("expected not eligible for HIGH band")
		}
	})

	t.Run("rejects non-empty suppressors", func(t *testing.T) {
		c := base()
		c.ScoreBreakdown.Suppressors = []string{"series_volume_differs"}
		if ok, _ := engine.autoResolveEligible(c, bookA, bookB); ok {
			t.Fatal("expected not eligible with suppressors")
		}
	})

	t.Run("rejects nil ScoreBreakdown", func(t *testing.T) {
		c := base()
		c.ScoreBreakdown = nil
		if ok, _ := engine.autoResolveEligible(c, bookA, bookB); ok {
			t.Fatal("expected not eligible with nil breakdown")
		}
	})

	t.Run("rejects implausible audio on one side", func(t *testing.T) {
		c := base()
		bad := &database.Book{ID: "B", Title: "Same Book"} // no duration/size
		if ok, _ := engine.autoResolveEligible(c, bookA, bad); ok {
			t.Fatal("expected not eligible when a side lacks plausible audio")
		}
	})

	t.Run("rejects identifier conflict", func(t *testing.T) {
		c := base()
		a := arPlausibleBook("A", "Same Book")
		b := arPlausibleBook("B", "Same Book")
		a.ISBN13 = strPtr("9780000000001")
		b.ISBN13 = strPtr("9780000000002")
		if ok, _ := engine.autoResolveEligible(c, a, b); ok {
			t.Fatal("expected not eligible with conflicting ISBNs")
		}
	})

	t.Run("rejects single primary signal without label", func(t *testing.T) {
		c := base()
		c.ScoreBreakdown = certainBreakdown("A", "B", unified.SigExactFile)
		if ok, _ := engine.autoResolveEligible(c, bookA, bookB); ok {
			t.Fatal("expected not eligible with only 1 primary signal and no label")
		}
	})

	t.Run("accepts single primary signal WITH whole-book-signature label", func(t *testing.T) {
		c := base()
		c.ID = 42
		c.ScoreBreakdown = certainBreakdown("A", "B", unified.SigExactFile)
		if err := es.UpsertLabeledExample(database.LabeledExample{
			CandidateID: 42,
			Label:       "true_dup",
			LabelReason: "whole-book signatures match",
		}); err != nil {
			t.Fatalf("UpsertLabeledExample: %v", err)
		}
		ok, reason := engine.autoResolveEligible(c, bookA, bookB)
		if !ok {
			t.Fatalf("expected eligible via label fallback, got: %s", reason)
		}
	})

	t.Run("supporting signals do not count as primary", func(t *testing.T) {
		c := base()
		// Two signals but both SUPPORTING kinds — must not qualify.
		c.ScoreBreakdown = certainBreakdown("A", "B", unified.SigDuration, unified.SigFolderPath)
		if ok, _ := engine.autoResolveEligible(c, bookA, bookB); ok {
			t.Fatal("expected not eligible with only supporting signals")
		}
	})
}

// --- Dry-run path (real EmbeddingStore + MockStore for book lookups) ---

func TestAutoResolveCertain_DryRun(t *testing.T) {
	engine, mock, es := setupTestEngine(t)

	mergeCalled := false
	mock.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		mergeCalled = true // any UpdateBook implies a merge happened
		return b, nil
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		return arPlausibleBook(id, "Same Book"), nil
	}

	// Two eligible CERTAIN candidates + one HIGH (must be ignored by the Band filter).
	mustSeed(t, es, "A1", "B1", unified.BandCertain, unified.SigExactFile, unified.SigISBNASIN)
	mustSeed(t, es, "A2", "B2", unified.BandCertain, unified.SigExactAcoustID, unified.SigMetaSrcHash)
	mustSeed(t, es, "A3", "B3", unified.BandHigh, unified.SigExactFile, unified.SigISBNASIN)

	res, err := engine.AutoResolveCertain(context.Background(), false, 200, 50)
	if err != nil {
		t.Fatalf("AutoResolveCertain: %v", err)
	}
	if !res.DryRun {
		t.Fatal("expected DryRun=true")
	}
	if res.Checked != 2 {
		t.Fatalf("expected Checked=2 (CERTAIN band only), got %d", res.Checked)
	}
	if res.Eligible != 2 {
		t.Fatalf("expected Eligible=2, got %d", res.Eligible)
	}
	if res.Merged != 0 {
		t.Fatalf("expected Merged=0 in dry-run, got %d", res.Merged)
	}
	if len(res.Samples) != 2 {
		t.Fatalf("expected 2 samples, got %d", len(res.Samples))
	}
	for _, s := range res.Samples {
		if s.Reason == "" {
			t.Fatalf("expected non-empty eligibility reason in sample %d", s.CandidateID)
		}
		if s.Merged {
			t.Fatal("dry-run samples must not be marked Merged")
		}
	}
	if mergeCalled {
		t.Fatal("dry-run must not call MergeBooks/UpdateBook")
	}
}

func TestAutoResolveCertain_ApplyRequiresKillSwitch(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		return arPlausibleBook(id, "Same Book"), nil
	}
	mustSeed(t, es, "A1", "B1", unified.BandCertain, unified.SigExactFile, unified.SigISBNASIN)

	prev := config.AppConfig.Dedup.AutoResolveEnabled
	config.AppConfig.Dedup.AutoResolveEnabled = false
	t.Cleanup(func() { config.AppConfig.Dedup.AutoResolveEnabled = prev })

	res, err := engine.AutoResolveCertain(context.Background(), true, 200, 50)
	if err == nil {
		t.Fatal("expected error when apply=true with kill switch off")
	}
	if res.Merged != 0 {
		t.Fatalf("expected zero merges on gated apply, got %d", res.Merged)
	}
}

// --- Apply path + journal + unmerge round-trip (real PebbleStore) ---

func setupRealStoreEngine(t *testing.T) (*Engine, database.Store, *database.EmbeddingStore) {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "pebble"))
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)

	edb, err := pebble.Open(t.TempDir(), &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open embed: %v", err)
	}
	es := database.NewEmbeddingStore(edb)
	t.Cleanup(func() {
		database.SetGlobalStore(origStore)
		_ = edb.Close()
		_ = store.Close()
	})

	ms := merge.NewService(store)
	engine := NewEngine(es, store, nil, nil, ms)
	return engine, store, es
}

func TestAutoResolveCertain_ApplyMergesAndJournals(t *testing.T) {
	engine, store, es := setupRealStoreEngine(t)

	prev := config.AppConfig.Dedup.AutoResolveEnabled
	config.AppConfig.Dedup.AutoResolveEnabled = true
	t.Cleanup(func() { config.AppConfig.Dedup.AutoResolveEnabled = prev })

	// Create two eligible pairs (4 books) with plausible audio.
	for _, id := range []string{"BA", "BB", "BC", "BD"} {
		if _, err := store.CreateBook(arPlausibleBook(id, "Dup Title "+id)); err != nil {
			t.Fatalf("CreateBook %s: %v", id, err)
		}
	}
	mustSeed(t, es, "BA", "BB", unified.BandCertain, unified.SigExactFile, unified.SigISBNASIN)
	mustSeed(t, es, "BC", "BD", unified.BandCertain, unified.SigExactAcoustID, unified.SigMetaSrcHash)

	// max_merges=1 so we can prove the cap stops the second eligible pair.
	res, err := engine.AutoResolveCertain(context.Background(), true, 1, 50)
	if err != nil {
		t.Fatalf("AutoResolveCertain apply: %v", err)
	}
	if res.Merged != 1 {
		t.Fatalf("expected Merged=1 (cap), got %d", res.Merged)
	}
	if res.Eligible != 2 {
		t.Fatalf("expected Eligible=2, got %d", res.Eligible)
	}
	if res.SkippedCap != 1 {
		t.Fatalf("expected SkippedCap=1, got %d", res.SkippedCap)
	}

	// Journal written for the one merge.
	entries, err := es.ListAutoMergeJournalEntries(0)
	if err != nil {
		t.Fatalf("ListAutoMergeJournalEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 journal entry, got %d", len(entries))
	}
	entry := entries[0]

	// Survivor tagged :auto-certain.
	tags, err := store.GetBookTags(entry.WinnerID)
	if err != nil {
		t.Fatalf("GetBookTags: %v", err)
	}
	if !arHasTag(tags, "dedup:merge-survivor:auto-certain") {
		t.Fatalf("expected survivor tag :auto-certain, got %v", tags)
	}

	// Loser soft-deleted (MarkedForDeletion=true) post-merge.
	loser, err := store.GetBookByID(entry.LoserID)
	if err != nil || loser == nil {
		t.Fatalf("GetBookByID loser: %v", err)
	}
	if loser.MarkedForDeletion == nil || !*loser.MarkedForDeletion {
		t.Fatal("expected loser MarkedForDeletion=true after merge")
	}

	// --- Unmerge round-trip: both books restored to pre-merge state. ---
	if err := engine.UnmergeAuto(entry.Key); err != nil {
		t.Fatalf("UnmergeAuto: %v", err)
	}
	loserAfter, err := store.GetBookByID(entry.LoserID)
	if err != nil || loserAfter == nil {
		t.Fatalf("GetBookByID loser after unmerge: %v", err)
	}
	if loserAfter.MarkedForDeletion != nil && *loserAfter.MarkedForDeletion {
		t.Fatal("expected loser MarkedForDeletion cleared after unmerge")
	}
	if loserAfter.VersionGroupID != nil {
		t.Fatalf("expected loser VersionGroupID nil after unmerge, got %v", *loserAfter.VersionGroupID)
	}
	winnerAfter, err := store.GetBookByID(entry.WinnerID)
	if err != nil || winnerAfter == nil {
		t.Fatalf("GetBookByID winner after unmerge: %v", err)
	}
	if winnerAfter.VersionGroupID != nil {
		t.Fatalf("expected winner VersionGroupID nil after unmerge, got %v", *winnerAfter.VersionGroupID)
	}
}

// TestAutoResolveCertain_SkipsAlreadyMergedBook proves the apply loop does not
// re-merge a book that was soft-deleted as a loser in a prior merge (the
// shared-book-across-two-pairs case). Candidates are snapshotted once up front,
// so the guard must re-check the live book state.
func TestAutoResolveCertain_SkipsAlreadyMergedBook(t *testing.T) {
	engine, store, es := setupRealStoreEngine(t)

	prev := config.AppConfig.Dedup.AutoResolveEnabled
	config.AppConfig.Dedup.AutoResolveEnabled = true
	t.Cleanup(func() { config.AppConfig.Dedup.AutoResolveEnabled = prev })

	for _, id := range []string{"PA", "PB", "PE"} {
		if _, err := store.CreateBook(arPlausibleBook(id, "Dup Title "+id)); err != nil {
			t.Fatalf("CreateBook %s: %v", id, err)
		}
	}
	mustSeed(t, es, "PA", "PB", unified.BandCertain, unified.SigExactFile, unified.SigISBNASIN)

	res, err := engine.AutoResolveCertain(context.Background(), true, 10, 50)
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if res.Merged != 1 {
		t.Fatalf("expected Merged=1, got %d", res.Merged)
	}
	entries, err := es.ListAutoMergeJournalEntries(0)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected 1 journal entry, got %d (err=%v)", len(entries), err)
	}
	loserID := entries[0].LoserID

	// Seed a fresh CERTAIN candidate pairing the now-soft-deleted loser with PE.
	mustSeed(t, es, loserID, "PE", unified.BandCertain, unified.SigExactFile, unified.SigISBNASIN)

	res2, err := engine.AutoResolveCertain(context.Background(), true, 10, 50)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if res2.Merged != 0 {
		t.Fatalf("expected Merged=0 (soft-deleted book must be skipped), got %d", res2.Merged)
	}
	if res2.Eligible != 0 {
		t.Fatalf("expected Eligible=0 (skipped before eligibility), got %d", res2.Eligible)
	}
}

// TestAutoMergeCertain_ProvisionalJournalFailureSkipsMerge proves the
// reversibility rail: when the PROVISIONAL journal write fails, autoMergeCertain
// returns an error and does NOT perform the merge — so we never leave a
// completed, irreversible merge with no journal key. Failure is injected by
// closing the EmbeddingStore, which makes PutAutoMergeJournalEntry error.
func TestAutoMergeCertain_ProvisionalJournalFailureSkipsMerge(t *testing.T) {
	// Build the engine with a READ-ONLY embed DB so the provisional journal
	// write (a Pebble Set, BEFORE MergeBooks) fails with an error — modelling a
	// real journal-write failure. Book reads/writes use `store` (writable).
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "pebble"))
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)

	edbDir := t.TempDir()
	edb0, err := pebble.Open(edbDir, &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open embed (init): %v", err)
	}
	_ = edb0.Close() // create the store, then reopen it read-only.
	edb, err := pebble.Open(edbDir, &pebble.Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("pebble.Open embed (read-only): %v", err)
	}
	es := database.NewEmbeddingStore(edb)
	t.Cleanup(func() {
		database.SetGlobalStore(origStore)
		_ = edb.Close()
		_ = store.Close()
	})
	engine := NewEngine(es, store, nil, nil, merge.NewService(store))

	for _, id := range []string{"JA", "JB"} {
		if _, err := store.CreateBook(arPlausibleBook(id, "Dup Title "+id)); err != nil {
			t.Fatalf("CreateBook %s: %v", id, err)
		}
	}

	c := database.DedupCandidate{ID: 4242, EntityType: "book", EntityAID: "JA", EntityBID: "JB"}
	winner, err := engine.autoMergeCertain(c)
	if err == nil {
		t.Fatalf("expected error when provisional journal write fails, got winner=%q nil err", winner)
	}

	// Neither book may have been merged/soft-deleted — the destructive act must
	// be skipped when reversibility could not be recorded first.
	for _, id := range []string{"JA", "JB"} {
		b, gerr := store.GetBookByID(id)
		if gerr != nil || b == nil {
			t.Fatalf("GetBookByID %s: %v", id, gerr)
		}
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			t.Fatalf("book %s was soft-deleted despite provisional journal failure (merge not skipped)", id)
		}
		if b.VersionGroupID != nil {
			t.Fatalf("book %s got a version group despite provisional journal failure", id)
		}
	}
}

// --- helpers ---

func mustSeed(t *testing.T, es *database.EmbeddingStore, aID, bID, band string, kinds ...unified.SignalKind) {
	t.Helper()
	sb := certainBreakdown(aID, bID, kinds...)
	sb.Band = band
	if err := es.UpsertCandidate(database.DedupCandidate{
		EntityType:     "book",
		EntityAID:      aID,
		EntityBID:      bID,
		Layer:          "exact",
		Status:         "pending",
		Band:           band,
		FormulaVersion: "test",
		ScoreBreakdown: sb,
	}); err != nil {
		t.Fatalf("UpsertCandidate(%s,%s): %v", aID, bID, err)
	}
}

func arHasTag(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
