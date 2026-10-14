// file: internal/aiscan/keep_test.go
// version: 1.0.0
// guid: 0b7e4d19-6c82-4f35-9a1d-83e5c7f2a604
// last-edited: 2026-10-13

package aiscan

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// keepFake is a KeepStore over a fixed author table. Authors absent from names
// do not exist; countsErr makes the book-count read fail.
type keepFake struct {
	names      map[int]string
	counts     map[int]int
	countsErr  error
	countReads int
}

func (f *keepFake) GetAuthorByID(id int) (*database.Author, error) {
	n, ok := f.names[id]
	if !ok {
		return nil, nil
	}
	return &database.Author{ID: id, Name: n}, nil
}

func (f *keepFake) GetAllAuthorBookCounts() (map[int]int, error) {
	f.countReads++
	return f.counts, f.countsErr
}

func keepOf(t *testing.T, s database.ScanSuggestion, store KeepStore) int {
	t.Helper()
	id, err := chooseKeepID(s, newKeepLookup(store))
	require.NoError(t, err)
	return id
}

func TestChooseKeepID_GroupsScanKeepsTheHeuristicCanonical(t *testing.T) {
	// The canonical is first in a group's ids; the name and the counts must not
	// override it, because the heuristic already chose it with both in view.
	store := &keepFake{
		names:  map[int]string{4: "Variant Name", 9: "Wrong Winner", 12: "Another"},
		counts: map[int]int{4: 1, 9: 500, 12: 3},
	}
	s := database.ScanSuggestion{
		Action: "merge", CanonicalName: "Wrong Winner", Source: "groups_scan",
		AuthorIDs: []int{4, 9, 12},
	}
	require.Equal(t, 4, keepOf(t, s, store))
	require.Zero(t, store.countReads, "a groups row needs no book counts")

	// An agreed row is driven by the groups scan: same rule.
	s.Source = "cross_validate"
	require.Equal(t, 4, keepOf(t, s, store))
}

func TestChooseKeepID_FullScanPrefersTheAuthorNamedCanonical(t *testing.T) {
	store := &keepFake{
		names: map[int]string{
			3: "R.R. Fixture", // normalizes to "R. R. Fixture", the canonical
			5: "Fixture, R. R.",
			8: "Someone Else",
		},
		counts: map[int]int{3: 2, 5: 40, 8: 900},
	}
	s := database.ScanSuggestion{
		Action: "merge", CanonicalName: "R. R. Fixture", Source: "full_scan",
		AuthorIDs: []int{8, 5, 3},
	}
	require.Equal(t, 3, keepOf(t, s, store), "the name match wins over the author with more books")
	require.Zero(t, store.countReads, "one name match is decided without book counts")
}

func TestChooseKeepID_FullScanFallsBackToMostBooksThenLowestID(t *testing.T) {
	store := &keepFake{
		names:  map[int]string{2: "Alpha", 6: "Bravo", 7: "Charlie"},
		counts: map[int]int{2: 5, 6: 9, 7: 9},
	}
	s := database.ScanSuggestion{Action: "merge", CanonicalName: "Nobody Matches", Source: "full_scan", AuthorIDs: []int{7, 2, 6}}
	require.Equal(t, 6, keepOf(t, s, store), "most books, tie broken by the lowest id")

	// Two authors both named canonical_name: the same tie-break applies inside the pool.
	store.names = map[int]string{2: "Same Name", 6: "Same Name", 7: "Other"}
	s.CanonicalName = "Same Name"
	store.counts = map[int]int{2: 1, 6: 1, 7: 99}
	require.Equal(t, 2, keepOf(t, s, store))
}

func TestChooseKeepID_FullScanSkipsAuthorsThatNoLongerExist(t *testing.T) {
	store := &keepFake{names: map[int]string{6: "Bravo"}, counts: map[int]int{6: 1}}
	s := database.ScanSuggestion{Action: "merge", CanonicalName: "Bravo", Source: "full_scan", AuthorIDs: []int{2, 6}}
	require.Equal(t, 6, keepOf(t, s, store))

	s.AuthorIDs = []int{2, 3}
	require.Zero(t, keepOf(t, s, store), "no author exists: no keep")
}

func TestChooseKeepID_ABookCountFailureIsAnErrorNotAGuess(t *testing.T) {
	store := &keepFake{names: map[int]string{1: "A", 2: "B"}, countsErr: errors.New("db down")}
	s := database.ScanSuggestion{Action: "merge", CanonicalName: "Z", Source: "full_scan", AuthorIDs: []int{1, 2}}
	_, err := chooseKeepID(s, newKeepLookup(store))
	require.Error(t, err)
}

func TestAssignKeepIDs_OnlyFillsMergeRowsThatHaveNone(t *testing.T) {
	store := &keepFake{names: map[int]string{1: "A", 2: "B"}, counts: map[int]int{1: 1, 2: 5}}
	sugs := []database.ScanSuggestion{
		{Action: "merge", CanonicalName: "Q", Source: "full_scan", AuthorIDs: []int{1, 2}},
		{Action: "merge", CanonicalName: "Q", Source: "full_scan", AuthorIDs: []int{1, 2}, KeepID: 1},
		{Action: "alias", CanonicalName: "Q", Source: "full_scan", AuthorIDs: []int{1, 2}},
	}
	require.NoError(t, AssignKeepIDs(store, sugs))
	require.Equal(t, 2, sugs[0].KeepID)
	require.Equal(t, 1, sugs[1].KeepID, "an existing keep is never recomputed")
	require.Zero(t, sugs[2].KeepID, "only merge rows have a keep")
}

func TestGroupsSuggestionsCarryTheCanonicalAsKeep(t *testing.T) {
	groupIDs := [][]int{{10, 11, 12}, {20, 21}}
	got := groupsSuggestionsToScanSuggestions([]ai.AuthorDedupSuggestion{
		{GroupIndex: 1, Action: "merge", CanonicalName: "Second"},
		{GroupIndex: 0, Action: "alias", CanonicalName: "First"},
	}, groupIDs)
	require.Equal(t, 20, got[0].KeepID)
	require.Zero(t, got[1].KeepID, "an alias row has no merge, so no keep")
}

func TestCrossValidateCarriesKeepIDIntoEveryRowShape(t *testing.T) {
	groups := []database.ScanSuggestion{
		{Action: "merge", CanonicalName: "A", AuthorIDs: []int{1, 2}, KeepID: 1, Source: "groups_scan"},
		{Action: "merge", CanonicalName: "B", AuthorIDs: []int{3, 4}, KeepID: 3, Source: "groups_scan"},
		{Action: "merge", CanonicalName: "C", AuthorIDs: []int{5, 6}, KeepID: 5, Source: "groups_scan"},
	}
	full := []database.ScanSuggestion{
		{Action: "merge", CanonicalName: "A", AuthorIDs: []int{2, 1, 9}, KeepID: 2, Source: "full_scan"}, // agreed with A
		{Action: "alias", CanonicalName: "B", AuthorIDs: []int{4, 3}, KeepID: 0, Source: "full_scan"},    // disagrees with B
		{Action: "merge", CanonicalName: "D", AuthorIDs: []int{7, 8}, KeepID: 8, Source: "full_scan"},    // full only
	}
	byKey := map[string]database.ScanResult{}
	for _, r := range CrossValidate(1, groups, full) {
		byKey[r.Agreement+"/"+r.Suggestion.Source+"/"+r.Suggestion.CanonicalName] = r
	}
	require.Equal(t, 1, byKey["agreed/cross_validate/A"].Suggestion.KeepID, "agreed rows keep the groups scan's choice")
	require.Equal(t, 3, byKey["disagreed/groups_scan/B"].Suggestion.KeepID)
	require.Equal(t, 5, byKey["groups_only/groups_scan/C"].Suggestion.KeepID)
	require.Equal(t, 8, byKey["full_only/full_scan/D"].Suggestion.KeepID)
}

func TestEnsureKeepIDs_BackfillsOldResultsOncePreservingAppliedAndExistingKeeps(t *testing.T) {
	scanStore := newScanStore(t)
	main := newNamedMainStore("Alpha One", "Alpha Won", "Brand New")
	pm := NewPipelineManager(scanStore, main, nil)

	scan, err := scanStore.CreateScan("realtime", nil, 3)
	require.NoError(t, err)
	save := func(r database.ScanResult) int {
		r.ScanID = scan.ID
		require.NoError(t, scanStore.SaveScanResult(&r))
		return r.ID
	}
	old := save(database.ScanResult{Agreement: "groups_only", Suggestion: database.ScanSuggestion{
		Action: "merge", CanonicalName: "Alpha One", Source: "groups_scan", AuthorIDs: []int{1, 2}}})
	full := save(database.ScanResult{Agreement: "full_only", Suggestion: database.ScanSuggestion{
		Action: "merge", CanonicalName: "Alpha Won", Source: "full_scan", AuthorIDs: []int{1, 2}}})
	kept := save(database.ScanResult{Agreement: "full_only", Suggestion: database.ScanSuggestion{
		Action: "merge", CanonicalName: "Alpha Won", Source: "full_scan", AuthorIDs: []int{1, 2}, KeepID: 1}})
	alias := save(database.ScanResult{Agreement: "full_only", Suggestion: database.ScanSuggestion{
		Action: "alias", CanonicalName: "Alpha Won", Source: "full_scan", AuthorIDs: []int{1, 2}}})
	require.NoError(t, scanStore.MarkResultApplied(scan.ID, old))

	require.NoError(t, pm.EnsureKeepIDs(scan.ID))
	require.NoError(t, pm.EnsureKeepIDs(scan.ID), "idempotent")

	got, err := scanStore.GetScanResults(scan.ID)
	require.NoError(t, err)
	byID := map[int]database.ScanResult{}
	for _, r := range got {
		byID[r.ID] = r
	}
	require.Equal(t, 1, byID[old].Suggestion.KeepID, "a groups row keeps its first id")
	require.True(t, byID[old].Applied, "the backfill must not clear an applied flag")
	require.Equal(t, 2, byID[full].Suggestion.KeepID, "a full row keeps the author named canonical_name")
	require.Equal(t, 1, byID[kept].Suggestion.KeepID, "an existing keep is never recomputed")
	require.Zero(t, byID[alias].Suggestion.KeepID)
}

func TestRecordFullScanResults_GivesMergeRowsTheirKeepWhenAnAuthorStoreIsPassed(t *testing.T) {
	store := newScanStore(t)
	authors := &keepFake{names: map[int]string{1: "Dana Fixture", 2: "Fixture, Dana"}, counts: map[int]int{1: 1, 2: 3}}
	sugg := []ai.AuthorDiscoverySuggestion{{AuthorIDs: []int{2, 1}, Action: "merge", CanonicalName: "Dana Fixture", Confidence: "high"}}

	id, err := RecordFullScanResults(store, "night-keep", sugg, authors)
	require.NoError(t, err)
	got, err := store.GetScanResults(id)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, 1, got[0].Suggestion.KeepID)

	// Without an author store the row is left for the read path.
	id, err = RecordFullScanResults(store, "night-nokeep", sugg, nil)
	require.NoError(t, err)
	got, _ = store.GetScanResults(id)
	require.Zero(t, got[0].Suggestion.KeepID)
}

func TestCreateScanRefusesWhileAnotherScanRuns(t *testing.T) {
	store := newScanStore(t)
	pm := NewPipelineManager(store, newFakeMainStore(2), nil)
	first, err := pm.CreateScan("realtime")
	require.NoError(t, err)

	_, err = pm.CreateScan("realtime")
	var running *database.ScanAlreadyRunningError
	require.ErrorAs(t, err, &running)
	require.Equal(t, first.ID, running.ScanID)

	require.NoError(t, store.UpdateScanStatus(first.ID, "complete"))
	_, err = pm.CreateScan("realtime")
	require.NoError(t, err)
}
