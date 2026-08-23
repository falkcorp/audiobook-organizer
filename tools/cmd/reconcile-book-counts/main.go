// file: tools/cmd/reconcile-book-counts/main.go
// version: 1.0.0
// guid: 065791f4-9161-4528-8528-4c3f11c765b2
// last-edited: 2026-08-22

// Command reconcile-book-counts is a READ-ONLY diagnostic for TODO.md's
// "3,954-book gap" item: the store's ListBookIDs() (the true live-book-ID
// set) reportedly disagreed with what /api/v1/audiobooks lists by default.
//
// It does three things, in order, and never writes anything:
//
//  1. Prints historical context already on record in TODO.md's "C716
//     resolved" section (2026-08-14 production investigation): the original
//     67,824-live-books figure this item cites was a Bleve DocCount()
//     snapshot, not a true ListBookIDs() count, and it was polluted by 3,953
//     stale soft-deleted docs from an earlier backfill — a leak already
//     fixed on main. That investigation found the residual gap decomposed to
//     2 quarantined books, 0 unexplained. This tool does not re-derive that
//     historical finding (it cannot — the production data that produced it
//     is gone); it states it so a reader is not left assuming a live,
//     unexplained gap exists.
//  2. Re-runs the SAME class of comparison against whatever store (and,
//     optionally, search index) it is pointed at, so the check can be
//     repeated if the gap ever reappears: ListBookIDs() vs. the exact code
//     path /api/v1/audiobooks uses for a library_state-empty,
//     is_primary_version-unset list, called directly (no HTTP layer).
//  3. Optionally cross-checks Bleve's DocCount() against ListBookIDs() —
//     the specific measuring-instrument mix-up from (1) — using the
//     underlying bleve.Open, not this repo's internal/search.Open. That
//     distinction matters: internal/search.Open DELETES AND RECREATES the
//     index on a mapping-version mismatch, which is a write this diagnostic
//     must never perform. The raw bleve.Open() "must exist" and never
//     creates or deletes anything, so it is the read-only-safe choice here.
//
// No DB writes, no index writes, no metadata changes, no row deletions.
// PebbleDB takes an exclusive lock on the directory it opens, so -db (and,
// if given, -bleve) MUST point at a COPY, never the live production
// directory — the server already holds that lock, and this tool has no
// business contending with it even if the open somehow succeeded:
//
//	cp -a --reflink=auto /var/lib/audiobook-organizer/audiobooks.pebble /tmp/abk-snap
//	cp -a --reflink=auto /var/lib/audiobook-organizer/search.bleve /tmp/abk-snap-bleve
//	reconcile-book-counts -db /tmp/abk-snap -bleve /tmp/abk-snap-bleve
//
// Usage:
//
//	reconcile-book-counts -db <pebble-dir-copy> [-bleve <bleve-dir-copy>] [-max-diff N]
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"time"

	"github.com/blevesearch/bleve/v2"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func main() {
	dbPath := flag.String("db", "", "path to the PebbleDB directory (a COPY of prod -- the live DB is locked by the server)")
	blevePath := flag.String("bleve", "", "optional path to the Bleve search index directory (a COPY, not the live index) to cross-check the DocCount()-vs-ListBookIDs() mix-up hypothesis")
	maxDiff := flag.Int("max-diff", 50, "maximum number of set-difference book IDs to print in detail")
	flag.Parse()

	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "usage: reconcile-book-counts -db <pebble-dir-copy> [-bleve <bleve-dir-copy>] [-max-diff N]")
		fmt.Fprintln(os.Stderr, "\nREAD-ONLY diagnostic. Never point -db or -bleve at a live/production directory:")
		fmt.Fprintln(os.Stderr, "Pebble takes an exclusive lock (this tool would contend with the live server),")
		fmt.Fprintln(os.Stderr, "and this tool deliberately does not use internal/search.Open, whose mapping-")
		fmt.Fprintln(os.Stderr, "mismatch branch deletes and recreates the index -- only a COPY is safe here.")
		os.Exit(2)
	}

	printHistoricalContext()

	store, err := database.NewPebbleStore(*dbPath)
	if err != nil {
		log.Fatalf("open pebble store at %s: %v", *dbPath, err)
	}
	defer func() { _ = store.Close() }()

	// Step 1: the true live-book-ID-set. ListBookIDs already excludes
	// MarkedForDeletion (internal/database/pebble_store.go: "the memdb fast
	// path...also filters MarkedForDeletion").
	liveIDs, err := store.ListBookIDs()
	if err != nil {
		log.Fatalf("ListBookIDs: %v", err)
	}
	liveSet := make(map[string]struct{}, len(liveIDs))
	for _, id := range liveIDs {
		liveSet[id] = struct{}{}
	}

	// Step 2: the same code path /api/v1/audiobooks uses for a
	// library_state-empty, is_primary_version-unset list (empty ListFilters
	// take the identical summariesPushdown branch buildAudiobookListResponse
	// does for a plain GET with no query params), called directly rather
	// than over HTTP, with a limit high enough to cover the whole library in
	// one page (GetAudiobooksWithTotal caps the accepted limit at 100000).
	//
	// ExcludeQuarantined mirrors the endpoint's default: show_quarantined
	// unset means false, and buildAudiobookListResponse
	// (internal/server/audiobooks_helpers.go) sets
	// filters.ExcludeQuarantined = true whenever showQuarantined is false.
	svc := audiobooks.NewAudiobookService(store)
	listed, matchTotal, err := svc.GetAudiobooksWithTotal(context.Background(), 100000, 0, "", nil, nil,
		audiobooks.ListFilters{ExcludeQuarantined: true})
	if err != nil {
		log.Fatalf("GetAudiobooksWithTotal: %v", err)
	}
	listedSet := make(map[string]struct{}, len(listed))
	for _, b := range listed {
		listedSet[b.ID] = struct{}{}
	}

	fmt.Println("=== live comparison against the store/index given on the command line ===")
	fmt.Printf("ListBookIDs() (live, MarkedForDeletion excluded):                       %d\n", len(liveSet))
	fmt.Printf("Service list (library_state=\"\", is_primary_version unset, quarantine excluded): %d rows (service-reported match total=%d)\n",
		len(listedSet), matchTotal)

	// Step 3: the actual set difference, not just the count gap.
	var diffIDs []string
	for id := range liveSet {
		if _, ok := listedSet[id]; !ok {
			diffIDs = append(diffIDs, id)
		}
	}
	sort.Strings(diffIDs)
	fmt.Printf("Set difference (live per ListBookIDs, absent from the service list):    %d\n", len(diffIDs))

	shown := diffIDs
	truncated := 0
	if len(shown) > *maxDiff {
		truncated = len(shown) - *maxDiff
		shown = shown[:*maxDiff]
	}
	for _, id := range shown {
		b, gerr := store.GetBookByID(id)
		if gerr != nil || b == nil {
			fmt.Printf("  %-26s <GetBookByID failed: %v>\n", id, gerr)
			continue
		}
		// quarantined_at is what ExcludeQuarantined actually tests
		// (internal/database/pebble_store.go, memdb_summaries.go: "f.ExcludeQuarantined
		// && book.QuarantinedAt != nil") -- QuarantineReason is printed alongside it for
		// human context, but QuarantinedAt is the field that explains inclusion/exclusion.
		fmt.Printf("  %-26s library_state=%-12s marked_for_deletion=%-6s is_primary_version=%-6s quarantined_at=%-6s quarantine_reason=%s\n",
			id, strPtr(b.LibraryState), boolPtr(b.MarkedForDeletion), boolPtr(b.IsPrimaryVersion), setPtr(b.QuarantinedAt), strPtr(b.QuarantineReason))
	}
	if truncated > 0 {
		fmt.Printf("  ... %d more not shown (raise -max-diff to see them)\n", truncated)
	}

	// Step 4: rule in/out the Bleve DocCount() mix-up hypothesis. Optional --
	// skipped entirely without -bleve, since a COPY of the search index is a
	// second thing the caller has to make available and the primary
	// ListBookIDs()-vs-service-list comparison above does not depend on it.
	if *blevePath != "" {
		checkBleveDocCount(*blevePath, len(liveSet))
	} else {
		fmt.Println("\nBleve DocCount() cross-check: skipped (-bleve not given).")
	}

	printVerdict(len(diffIDs), len(liveSet), len(listedSet))
}

func printHistoricalContext() {
	fmt.Println("=== historical context (TODO.md, \"C716 resolved\" section, 2026-08-14 investigation) ===")
	fmt.Println("The 67,824-live-books figure this diagnostic was written to check was NOT a true")
	fmt.Println("ListBookIDs() count. It was a Bleve DocCount() snapshot polluted by 3,953 stale")
	fmt.Println("soft-deleted docs indexed by an earlier backfill -- a leak already fixed on main.")
	fmt.Println("Post-fix, ListBookIDs() reported 63,871 live books against the API's 63,870: a")
	fmt.Println("residual 1-book difference that decomposed to 2 quarantined books (store-visible,")
	fmt.Println("excluded from the default list by ExcludeQuarantined) with 0 unexplained. This tool")
	fmt.Println("cannot re-derive that historical finding -- the production data it was measured")
	fmt.Println("against is gone -- so it is stated here rather than re-claimed as newly verified.")
	fmt.Println("What follows below IS freshly computed, against whatever -db/-bleve were given.")
	fmt.Println()
}

// checkBleveDocCount opens the Bleve index at path with the raw bleve.Open
// (NOT internal/search.Open, whose mapping-mismatch branch deletes and
// recreates the index -- a write this tool must never perform). bleve.Open
// "must exist" and never creates or mutates anything, which is why it is
// used here instead.
func checkBleveDocCount(path string, liveCount int) {
	idx, err := bleve.Open(path)
	if err != nil {
		fmt.Printf("\nBleve DocCount() cross-check: could not open %s: %v (skipping)\n", path, err)
		return
	}
	defer func() { _ = idx.Close() }()

	docCount, err := idx.DocCount()
	if err != nil {
		fmt.Printf("\nBleve DocCount() cross-check: DocCount() failed: %v\n", err)
		return
	}

	fmt.Printf("\nBleve DocCount() cross-check: index reports %d docs vs %d live books (delta %d)\n",
		docCount, liveCount, int64(docCount)-int64(liveCount))
	if int64(docCount) > int64(liveCount) {
		fmt.Println("  DocCount() EXCEEDS ListBookIDs() -- consistent with the same measuring-instrument")
		fmt.Println("  mix-up already diagnosed and fixed (see internal/server/search_coverage.go): the")
		fmt.Println("  index holds documents for books that are no longer live. Before treating any gap")
		fmt.Println("  reported above as a genuine third population, confirm it is not this.")
	} else {
		fmt.Println("  DocCount() does not exceed ListBookIDs() on this dataset -- the Bleve-doc-count")
		fmt.Println("  mix-up does not explain any gap seen here.")
	}
}

func printVerdict(diffCount, liveCount, listedCount int) {
	fmt.Println()
	if diffCount == 0 {
		fmt.Printf("VERDICT: no real gap on this dataset -- ListBookIDs() (%d) and the default service "+
			"list (%d) agree exactly.\n", liveCount, listedCount)
		return
	}
	fmt.Printf("VERDICT: %d book(s) are live per ListBookIDs() but absent from the default service list "+
		"on this dataset. See the per-book fields printed above. In the 2026-08-14 production "+
		"investigation this shape (store-visible, list-invisible) was fully explained by quarantine "+
		"exclusion (quarantined_at non-nil, ExcludeQuarantined drops them from the default list) "+
		"with 0 books left unexplained -- check quarantined_at on the rows above first. If any "+
		"remain unexplained by a field printed here, that is a genuine third population and should be "+
		"filed as its own todo.d fragment with this tool's output attached, not fixed inline.\n", diffCount)
}

func strPtr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	if *s == "" {
		return "<empty>"
	}
	return *s
}

func boolPtr(b *bool) string {
	if b == nil {
		return "<nil>"
	}
	if *b {
		return "true"
	}
	return "false"
}

func setPtr(t *time.Time) string {
	if t == nil {
		return "<nil>"
	}
	return "set"
}
