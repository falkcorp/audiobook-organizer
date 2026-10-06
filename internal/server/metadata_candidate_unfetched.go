// file: internal/server/metadata_candidate_unfetched.go
// version: 1.3.1
// guid: 6bf34beb-7e2f-40a9-b7a7-c5755a52c7fb
// last-edited: 2026-10-06
//
// Selects the books the scheduled candidate fetch asks the providers about:
// books never fetched, books whose candidates were invalidated, and books
// whose empty answer was recorded for questions a search no longer asks, and
// books with no usable candidate that a fallback provider still owes an
// answer (candidate_fallback.go).

package server

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

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
	// The fallback gate's owner-manual-only check (fallbackGates).
	fallbackGateStore
	// The owner's rejected candidates (metabatch.NoUsableCandidate).
	database.RawKVStore
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
	// FallbackPending: books whose current row holds no usable candidate
	// (metabatch.NoUsableCandidate: none, all owner-rejected, all refused by the ASIN
	// checks, or below the apply floor) and that a fallback provider (Open
	// Library, Google Books) still owes an answer: the fallback was deferred
	// or never ran.
	FallbackPending int
	// FallbackUnusable: the FallbackPending books whose row HOLDS candidates,
	// none usable -- the rows that were stuck before 2026-10-06 (served from
	// the cache forever, never selected).
	FallbackUnusable int
	// FallbackCapped: pending books Google Books owes whose Google step
	// today's background share of the shared Google Books budget cannot
	// cover; a later quota day asks Google about them, oldest attempt first.
	// One Google alone owes is left out; one Open Library owes too is still
	// selected for that free step (GoogleCapped).
	FallbackCapped int
	// ChainCapped: selected books the chain has not answered yet (NoRow,
	// StaleEmpty) whose possible Google step today's background share
	// cannot cover after the FallbackPending books are funded. They are
	// still selected -- the chain and Open Library cost no Google budget --
	// with their Google step put off (GoogleCapped).
	ChainCapped int
	// GoogleCapped: the selected books whose Google step is capped (asked
	// of the chain and Open Library only;
	// metabatch.FetchOpParams.GoogleCappedBookIDs).
	GoogleCapped []string
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
// A stale row is never selected for its staleness alone: that is the
// stale-refetch's to re-ask (POST .../batch-fetch-candidates with stale).
// Neither is an empty row that is merely old: an empty answer for the same
// questions ages out on its own (database.MetadataKnownEmptyTTL), and
// counting it here would turn every tick into a library-wide refetch.
//
// A third kind qualifies too: a current row (this ladder version, current
// fingerprint) with NO USABLE candidate (metabatch.NoUsableCandidate: none, all
// owner-rejected, all refused by the ASIN checks, or the best below the
// apply floor) that an enabled fallback provider still owes an answer
// (fallbackOwed) -- the fallback was deferred (Google's budget spent, a
// throttle hold) or never ran. The fetch decides with the same two functions,
// so a book selected here is one the fetch asks a fallback provider about or
// defers -- never one it serves from the cache and that comes back every
// tick. Google Books is not owed by an owner-manual-only book (it is still
// selected for Open Library).
//
// A book Google Books owes has its Google step funded only while
// googleRemaining (today's background share of the shared Google Books
// budget, which counts requests) covers the requests one lookup sends
// (googleRequestsPerBook), oldest Google attempt first (never attempted
// first, then the least recently attempted; FallbackAttempts), so a book
// whose lookup keeps being deferred or failing does not hold a capped day's
// place while others never get one. An unfunded book is left out when only
// Google owes it; one Open Library owes too is still selected for that free
// step, its Google step put off (GoogleCapped). Either way it comes back on a
// later quota day.
//
// A book the chain has not answered yet (no row, a stale empty row) may end
// in a Google step too: the fetch asks the fallback when the chain finds
// nothing usable. Its Google step is funded the same way, AFTER every
// FallbackPending book (those are known to need Google; these only may), so
// a never-fetched library cannot spend the background share the selection
// gave the rotation. An unfunded one is still selected -- the chain and Open
// Library cost no Google budget -- with its Google step put off
// (GoogleCapped, ChainCapped). Funding is reserved pessimistically: a book
// whose chain then answers spends nothing, and the share it held is left for
// a later tick.
//
// A book whose search query is not usable (metabatch.ResolveCandidateSearchQuery:
// a part row, no usable title) is left out too: the fetch would only skip it,
// and it would come back every tick.
//
// The walk is whole-library; the per-book resolve and cache read run on a
// bounded pool (runtime.NumCPU workers, errgroup.SetLimit). Each worker
// writes only its own slot, so the result needs no lock beyond the counts.
func unfetchedCandidateBookIDs(ctx context.Context, store unfetchedSelectStore, mfs *metafetch.Service,
	memo *metabatch.FolderMemo, busy map[string]bool, googleRemaining int) (unfetchedSelection, error) {
	var sel unfetchedSelection
	plan := activeFallbackPlan(candidateFallbackPlan(mfs.ActiveSourceNamesByID()))
	googleName := ""
	for _, fb := range plan {
		if fb.isGoogle() {
			googleName = fb.name
		}
	}
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
			// A row with candidates matters only to the fallback (its
			// candidates may all be unusable); with no fallback provider
			// enabled it was fetched and is left alone.
			if hasRow && n > 0 && len(plan) == 0 {
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
	// fallback marks a book selected because a fallback provider owes it an
	// answer; unusable, one whose row holds candidates; googleOwed, one
	// Google Books owes (capped below), with its last Google attempt.
	fallback := make([]bool, len(picks))
	unusable := make([]bool, len(picks))
	googleOwed := make([]bool, len(picks))
	// chainGoogle: googleOwed for a book the chain has not answered yet
	// (no row, a vanished row, a stale empty row): Google is enabled and the
	// book is not owner-manual-only, so the fallback could reach Google.
	chainGoogle := func(i int, b *database.Book, query string) {
		if googleName == "" {
			return
		}
		googleOwed[i] = fallbackGates(store, b, query).ManualOnly == ""
	}
	otherOwed := make([]bool, len(picks))
	googleTried := make([]time.Time, len(picks))
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
				chainGoogle(i, b, q.Title)
				return nil
			}
			entry, err := store.GetMetadataCache(b.ID)
			if err != nil || entry == nil {
				// The row vanished or cannot be read: ask again, the safe
				// direction for a fetch that never writes the book.
				keep[i] = true
				chainGoogle(i, b, q.Title)
				return nil
			}
			// A version "1" (legacy) row answered the old ladder's questions
			// and is re-asked by the batch fetch's own verdict
			// (metafetch.CachedBatchVerdict) when a fetch reaches it --
			// counting it here would put every pre-fan-out row into the first
			// scheduled run.
			if !strings.HasPrefix(entry.SearchFingerprint, metafetch.FingerprintPrefix) {
				return nil
			}
			current := mfs.SearchFingerprintCurrent(entry.SearchFingerprint, b, q.Title, liveAuthorHint(store, b))
			if !current {
				// Stale questions: an EMPTY row is re-asked; a row with
				// candidates is the stale-refetch's.
				if len(entry.Candidates) == 0 {
					keep[i], stale[i] = true, true
					chainGoogle(i, b, q.Title)
				}
				return nil
			}
			if metabatch.NoUsableCandidate(store, b, entry).Usable {
				return nil
			}
			gate := fallbackGates(store, b, q.Title)
			if gate.Applied {
				return nil
			}
			owed := fallbackOwed(entry, plan)
			if gate.ManualOnly != "" {
				owed = slices.DeleteFunc(owed, func(fb fallbackProvider) bool { return fb.isGoogle() })
			}
			if len(owed) == 0 {
				return nil
			}
			keep[i], fallback[i], unusable[i] = true, true, len(entry.Candidates) > 0
			otherOwed[i] = slices.ContainsFunc(owed, func(fb fallbackProvider) bool { return !fb.isGoogle() })
			if slices.ContainsFunc(owed, func(fb fallbackProvider) bool { return fb.isGoogle() }) {
				googleOwed[i] = true
				googleTried[i] = entry.FallbackAttempts[googleName].At
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return sel, err
	}
	// FallbackPending books first (they are known to need their fallback
	// step; a book the chain has not answered only may), then Google-owed
	// books oldest attempt first (never attempted first), then by id, so the
	// budget cap rotates through them deterministically.
	order := make([]int, len(picks))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ia, ib := order[a], order[b]
		if fallback[ia] != fallback[ib] {
			return fallback[ia]
		}
		if !googleTried[ia].Equal(googleTried[ib]) {
			return googleTried[ia].Before(googleTried[ib])
		}
		return picks[ia].book.ID < picks[ib].book.ID
	})
	perBook := googleRequestsPerBook()
	for _, i := range order {
		if !keep[i] {
			continue
		}
		switch {
		case !fallback[i]:
			// The chain has not answered (stale empty, no row): selected
			// whatever the budget; only its Google step may be put off.
			if googleOwed[i] {
				if googleRemaining < perBook {
					sel.ChainCapped++
					sel.GoogleCapped = append(sel.GoogleCapped, picks[i].book.ID)
				} else {
					googleRemaining -= perBook
				}
			}
			if stale[i] {
				sel.StaleEmpty++
			} else {
				sel.NoRow++
			}
		case fallback[i]:
			if googleOwed[i] {
				if googleRemaining < perBook {
					sel.FallbackCapped++
					if !otherOwed[i] {
						continue
					}
					sel.GoogleCapped = append(sel.GoogleCapped, picks[i].book.ID)
				} else {
					googleRemaining -= perBook
				}
			}
			sel.FallbackPending++
			if unusable[i] {
				sel.FallbackUnusable++
			}
		}
		sel.IDs = append(sel.IDs, picks[i].book.ID)
	}
	sort.Strings(sel.IDs)
	sort.Strings(sel.GoogleCapped)
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
