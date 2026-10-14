// file: internal/aiscan/keep.go
// version: 1.1.1
// guid: 5e1c8a42-93d7-4b60-a8f1-2c7d4e90b315
// last-edited: 2026-10-14

package aiscan

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
)

// KeepStore is what choosing a keep author reads: an author's name, and the
// book count of every author in one call. aiscan.Store satisfies it.
type KeepStore interface {
	GetAuthorByID(id int) (*database.Author, error)
	GetAllAuthorBookCounts() (map[int]int, error)
}

// keepLookup resolves names and book counts for chooseKeepID, reading each
// author at most once and the book counts at most once (only a full-scan row
// that cannot be decided by name needs them).
type keepLookup struct {
	store  KeepStore
	names  map[int]string
	absent map[int]bool
	counts map[int]int
	// countsErr is a failed book-count read, kept so every row that needed the
	// counts sees the same failure instead of silently choosing without them.
	countsErr error
	countsOK  bool
}

func newKeepLookup(store KeepStore) *keepLookup {
	return &keepLookup{store: store, names: map[int]string{}, absent: map[int]bool{}}
}

// name returns the author's name, or false when the author does not exist (a
// merged-away or deleted author cannot be kept).
func (l *keepLookup) name(id int) (string, bool) {
	if n, ok := l.names[id]; ok {
		return n, true
	}
	if l.absent[id] {
		return "", false
	}
	// GetAuthorByID follows the author tombstone: for a merged-away id it answers
	// with the author it was merged into, whose ID differs. That is "gone", not a
	// candidate to keep under the wrong id.
	a, err := l.store.GetAuthorByID(id)
	if err != nil || a == nil || a.ID != id {
		l.absent[id] = true
		return "", false
	}
	l.names[id] = a.Name
	return a.Name, true
}

func (l *keepLookup) bookCount(id int) (int, error) {
	if !l.countsOK {
		l.counts, l.countsErr = l.store.GetAllAuthorBookCounts()
		l.countsOK = true
	}
	if l.countsErr != nil {
		return 0, l.countsErr
	}
	return l.counts[id], nil
}

// groupsKeepID is the keep author of a suggestion the heuristic grouping
// produced: its first author id. groupAuthorIDs puts the group's Canonical
// first, and CrossValidate keeps the groups suggestion's ids first when it
// merges them with the full scan's, so for a groups_scan row and for an agreed
// (cross_validate) row, AuthorIDs[0] IS the heuristic canonical.
func groupsKeepID(s database.ScanSuggestion) int {
	if len(s.AuthorIDs) == 0 {
		return 0
	}
	return s.AuthorIDs[0]
}

// chooseKeepID picks the author a merge suggestion keeps, or 0 when none of its
// authors can be resolved.
//
//   - A groups-scan row, and an agreed row (which the groups scan drives), keep
//     the heuristic canonical: AuthorIDs[0].
//   - A full-scan row has ids in no defined order. It keeps the author whose
//     name equals canonical_name once both pass dedup.NormalizeAuthorName;
//     failing that the author with the most books. Ties go to the lowest id.
//
// The same function runs when the pipeline builds a suggestion and when an
// older scan's results are read, so a scan gets the same answer either way.
func chooseKeepID(s database.ScanSuggestion, lookup *keepLookup) (int, error) {
	if s.Source == "groups_scan" || s.Source == "cross_validate" {
		// The heuristic canonical is only a keep while it still exists: a scan read
		// after that author was merged away must not hand the apply a keep that is
		// gone. It then falls through to the rule a full-scan row uses.
		if id := groupsKeepID(s); id > 0 {
			if _, ok := lookup.name(id); ok {
				return id, nil
			}
		}
	}
	want := dedup.NormalizeAuthorName(s.CanonicalName)
	var named []int // authors whose normalized name is canonical_name
	var exist []int // every author that still exists
	for _, id := range s.AuthorIDs {
		n, ok := lookup.name(id)
		if !ok {
			continue
		}
		exist = append(exist, id)
		if want != "" && dedup.NormalizeAuthorName(n) == want {
			named = append(named, id)
		}
	}
	pool := exist
	if len(named) > 0 {
		pool = named
	}
	if len(pool) == 0 {
		return 0, nil
	}
	best, bestCount := 0, -1
	for _, id := range pool {
		// One named author needs no book count; do not read the whole library's
		// counts to confirm a choice that was already unambiguous.
		if len(pool) == 1 {
			return id, nil
		}
		c, err := lookup.bookCount(id)
		if err != nil {
			return 0, fmt.Errorf("book counts: %w", err)
		}
		if c > bestCount || (c == bestCount && id < best) {
			best, bestCount = id, c
		}
	}
	return best, nil
}

// AssignKeepIDs fills KeepID on every merge suggestion that has none.
// A suggestion whose keep cannot be chosen keeps KeepID 0 (the apply refuses it
// by name). It returns the first error from a failed read; suggestions it had
// already decided keep their ids.
func AssignKeepIDs(store KeepStore, sugs []database.ScanSuggestion) error {
	lookup := newKeepLookup(store)
	var firstErr error
	for i := range sugs {
		if sugs[i].KeepID != 0 || sugs[i].Action != "merge" {
			continue
		}
		id, err := chooseKeepID(sugs[i], lookup)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		sugs[i].KeepID = id
	}
	return firstErr
}

// EnsureKeepIDs fills KeepID on a scan's stored merge results that lack one (a
// scan stored before the field existed, or recorded by a path that had no
// author store) and persists the choices, so the next read finds them. It is
// idempotent and safe to call on every read.
func (pm *PipelineManager) EnsureKeepIDs(scanID int) error {
	results, err := pm.scanStore.GetScanResults(scanID)
	if err != nil {
		return fmt.Errorf("get results: %w", err)
	}
	var todo []int // indexes into results
	for i, r := range results {
		if r.Suggestion.Action == "merge" && r.Suggestion.KeepID == 0 {
			todo = append(todo, i)
		}
	}
	if len(todo) == 0 {
		return nil
	}
	lookup := newKeepLookup(pm.mainStore)
	keep := make(map[int]int, len(todo))
	var firstErr error
	for _, i := range todo {
		id, cerr := chooseKeepID(results[i].Suggestion, lookup)
		if cerr != nil {
			if firstErr == nil {
				firstErr = cerr
			}
			continue
		}
		if id > 0 {
			keep[results[i].ID] = id
		}
	}
	if err := pm.scanStore.SetResultKeepIDs(scanID, keep); err != nil {
		return fmt.Errorf("save keep ids: %w", err)
	}
	return firstErr
}
