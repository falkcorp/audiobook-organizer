// file: internal/database/listing_ordering.go
// version: 1.2.0
// guid: 5b2e9c14-7a63-4f08-9d5b-8e1c6a30f472
// last-edited: 2026-11-07

package database

import (
	"cmp"
	"slices"
	"sort"
	"strings"
)

// This file holds the comparators for listings that have two backing
// implementations, for the same reason series_ordering.go does: MemStore sorted
// and the PebbleStore scan did not sort at all, so each of these listings came
// back in a different order depending only on whether memdb had finished
// warming up. Pebble iterates "<kind>:<id>" keys, and because those keys are
// strings the raw order is neither alphabetical nor numeric — author:10 precedes
// author:2 — so it is not a defensible order in its own right either.
//
// Keeping one comparator per listing, called from both paths, is what stops the
// two from drifting apart again. Asserting membership (require.ElementsMatch)
// cannot catch this class; the conformance tests assert the SEQUENCE.

// sortByLowerName orders any listing by a case-insensitive name projection,
// ties broken by ID. Used for authors, series and import paths, all three of
// which MemStore returns sorted by name.
//
// The ID tie-break is what makes the order total. Without it, two rows whose
// names differ only in case ("Torchwood" / "torchwood") compared equal, and
// sort.Slice is not stable, so memdb (fed in ID order) and Pebble (fed in
// "series:N" key order) returned the pair in different orders — the very drift
// this file exists to prevent. The series-prune fixer fingerprints duplicate
// groups in listing order, so a warmup finishing between its plan and re-plan
// changed the fingerprint and refused the apply as "changed since preview".
func sortByLowerName[T any](items []T, name func(T) string, id func(T) int) {
	if len(items) < 2 {
		return
	}
	slices.SortFunc(items, func(a, b T) int {
		if c := strings.Compare(strings.ToLower(name(a)), strings.ToLower(name(b))); c != 0 {
			return c
		}
		return cmp.Compare(id(a), id(b))
	})
}

// sortAuthorAliases groups aliases by author, then orders them by alias name
// (case-insensitive), matching MemStore.GetAllAuthorAliases.
func sortAuthorAliases(aliases []AuthorAlias) {
	if len(aliases) < 2 {
		return
	}
	sort.Slice(aliases, func(i, j int) bool {
		if aliases[i].AuthorID != aliases[j].AuthorID {
			return aliases[i].AuthorID < aliases[j].AuthorID
		}
		return strings.ToLower(aliases[i].AliasName) < strings.ToLower(aliases[j].AliasName)
	})
}

// sortBooksByDeletedAtDesc orders the trash the way the UI promises to show it:
// most recently deleted first, rows carrying no deletion timestamp last, ties
// broken by ID so the order is total and stable.
//
// The Pebble implementation of ListSoftDeletedBooks used to apply limit/offset
// *during* iteration, which meant it could not sort at all — a page was cut
// before the full matching set existed. Callers paging through the trash while
// warmup completed would see the ordering change underneath them, skipping or
// repeating rows. Sorting forces collect-then-paginate, which is affordable
// precisely because the soft-deleted set is tiny relative to the library; that
// is the same assumption the memdb implementation already documents.
func sortBooksByDeletedAtDesc(books []Book) {
	if len(books) < 2 {
		return
	}
	sort.SliceStable(books, func(i, j int) bool { return deletedAtDescLess(&books[i], &books[j]) })
}

// sortBookPtrsByDeletedAtDesc is sortBooksByDeletedAtDesc over pointers, for
// the memdb path, which sorts the trash without copying every Book (only the
// requested page is copied out). Same comparator, so the two paths agree.
func sortBookPtrsByDeletedAtDesc(books []*Book) {
	if len(books) < 2 {
		return
	}
	sort.SliceStable(books, func(i, j int) bool { return deletedAtDescLess(books[i], books[j]) })
}

// deletedAtDescLess is the trash order: most recently deleted first, rows
// with no deletion timestamp last, ties broken by ID.
func deletedAtDescLess(a, b *Book) bool {
	ai, aj := a.MarkedForDeletionAt, b.MarkedForDeletionAt
	switch {
	case ai == nil && aj == nil:
		return a.ID < b.ID
	case ai == nil:
		// Rows with no timestamp sort after rows that have one.
		return false
	case aj == nil:
		return true
	default:
		return ai.After(*aj)
	}
}
