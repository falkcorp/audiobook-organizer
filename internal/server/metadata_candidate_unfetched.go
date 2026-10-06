// file: internal/server/metadata_candidate_unfetched.go
// version: 1.0.0
// guid: 6bf34beb-7e2f-40a9-b7a7-c5755a52c7fb
// last-edited: 2026-10-05
//
// Selects the books the scheduled candidate fetch asks the providers about:
// books never fetched, books whose candidates were invalidated, and books
// whose empty answer was recorded for questions a search no longer asks.

package server

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// unfetchedSelectStore is what unfetchedCandidateBookIDs reads: every live
// book (paged), the metadata-cache summaries and rows, and what the search
// query resolver reads.
type unfetchedSelectStore interface {
	GetAllBooksFullFrom(afterID string, limit int) ([]database.Book, error)
	ListMetadataCacheKeys() ([]database.MetadataCacheSummary, error)
	GetMetadataCache(bookID string) (*database.MetadataCandidateCache, error)
	metabatch.SearchQueryReader
}

// unfetchedPageSize is how many books one page of the selection's book walk
// reads.
const unfetchedPageSize = 1000

// unfetchedSelection is what unfetchedCandidateBookIDs found, with the counts
// the op logs so a run that selects nothing says why.
type unfetchedSelection struct {
	IDs []string
	// NoRow: books with no metadata_cache row (never fetched, or invalidated:
	// a book write that changes its search identity deletes the row).
	NoRow int
	// StaleEmpty: books whose 0-candidate row answered other questions than
	// a search would ask now (metafetch.SearchFingerprintCurrent).
	StaleEmpty int
	// Unsearchable: candidates the fetch would only skip (no usable query).
	Unsearchable int
	// Scanned: live books read.
	Scanned int
}

// unfetchedCandidateBookIDs returns the books the scheduled candidate fetch
// should ask the providers about. A book qualifies when it is a live primary
// book (database.EffectiveIsPrimaryVersion: nil is primary), its metadata is
// not applied (audiobooks.BookMetadataApplied), the owner has not marked it
// "no match", no fetch already running holds it (busy), and either
//
//   - it has no metadata_cache row: never fetched, or its candidates were
//     invalidated (a write that changes its search identity deletes the row);
//   - or its row holds 0 candidates recorded, by this ladder version
//     (metafetch.FingerprintPrefix), for questions a search no longer asks
//     (metafetch.SearchFingerprintCurrent false): its title, author or the
//     query parser changed since the providers answered "nothing". A legacy
//     (version "1") empty row is left out.
//
// A row with candidates is never selected: it was fetched, and a stale one is
// the stale-refetch's to re-ask (POST .../batch-fetch-candidates with stale).
// Neither is an empty row that is merely old: an empty answer for the same
// questions ages out on its own (database.MetadataKnownEmptyTTL), and
// counting it here would turn every tick into a library-wide refetch.
//
// A book whose search query is not usable (metabatch.ResolveCandidateSearchQuery:
// a part row, no usable title) is left out too: the fetch would only skip it,
// and it would come back every tick.
//
// The walk is whole-library; the per-book resolve and cache read run on a
// bounded pool (runtime.NumCPU workers, errgroup.SetLimit). Each worker
// writes only its own slot, so the result needs no lock beyond the counts.
func unfetchedCandidateBookIDs(ctx context.Context, store unfetchedSelectStore, mfs *metafetch.Service,
	memo *metabatch.FolderMemo, busy map[string]bool) (unfetchedSelection, error) {
	var sel unfetchedSelection
	summaries, err := store.ListMetadataCacheKeys()
	if err != nil {
		return sel, fmt.Errorf("list metadata cache rows: %w", err)
	}
	candidates := make(map[string]int, len(summaries))
	for _, s := range summaries {
		candidates[s.BookID] = s.CandidateCount
	}

	type pick struct {
		book   database.Book
		hasRow bool
	}
	var picks []pick
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return sel, err
		}
		page, err := store.GetAllBooksFullFrom(after, unfetchedPageSize)
		if err != nil {
			return sel, fmt.Errorf("list books after %q: %w", after, err)
		}
		if len(page) == 0 {
			break
		}
		for i := range page {
			b := page[i]
			sel.Scanned++
			if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
				continue
			}
			if !database.EffectiveIsPrimaryVersion(b.IsPrimaryVersion) || audiobooks.BookMetadataApplied(&b) ||
				metafetch.IsMarkedNoMatch(b.MetadataReviewStatus) || busy[b.ID] {
				continue
			}
			n, hasRow := candidates[b.ID]
			if hasRow && n > 0 {
				continue
			}
			picks = append(picks, pick{book: b, hasRow: hasRow})
		}
		after = page[len(page)-1].ID
		if len(page) < unfetchedPageSize {
			break
		}
	}

	keep := make([]bool, len(picks))
	stale := make([]bool, len(picks))
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	for i := range picks {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			b := &picks[i].book
			q := metabatch.ResolveCandidateSearchQueryMemo(store, b, memo)
			if !q.Usable {
				mu.Lock()
				sel.Unsearchable++
				mu.Unlock()
				return nil
			}
			if !picks[i].hasRow {
				keep[i] = true
				return nil
			}
			entry, err := store.GetMetadataCache(b.ID)
			if err != nil || entry == nil {
				// The row vanished or cannot be read: ask again, the safe
				// direction for a fetch that never writes the book.
				keep[i] = true
				return nil
			}
			// A row with candidates is fetched; a version "1" (legacy) empty
			// row answered the old ladder's questions and is re-asked by the
			// batch fetch's own verdict (metafetch.CachedBatchVerdict) when a
			// fetch reaches it -- counting it here would put every pre-fan-out
			// empty row into the first scheduled run.
			if len(entry.Candidates) > 0 || !strings.HasPrefix(entry.SearchFingerprint, metafetch.FingerprintPrefix) {
				return nil
			}
			if !mfs.SearchFingerprintCurrent(entry.SearchFingerprint, b, q.Title, liveAuthorHint(store, b)) {
				keep[i], stale[i] = true, true
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return sel, err
	}
	for i, p := range picks {
		if !keep[i] {
			continue
		}
		sel.IDs = append(sel.IDs, p.book.ID)
		if stale[i] {
			sel.StaleEmpty++
		} else {
			sel.NoRow++
		}
	}
	sort.Strings(sel.IDs)
	return sel, nil
}

// liveAuthorHint is the author hint fetchCandidateForBook searches and hashes
// a book with: its live primary author (database.LiveBookAuthorNames), else
// the snapshot, with a placeholder dropped (metafetch.SearchAuthorHint). A
// read fault answers "" -- the fingerprint then differs and the book is
// re-asked, the safe direction.
func liveAuthorHint(store database.BookAuthorReader, book *database.Book) string {
	author := ""
	if book.Author != nil {
		author = book.Author.Name
	}
	if live, err := database.LiveBookAuthorNames(store, book); err == nil && len(live) > 0 {
		author = live[0]
	}
	return metafetch.SearchAuthorHint(author)
}
