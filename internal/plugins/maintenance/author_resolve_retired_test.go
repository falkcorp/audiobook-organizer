// file: internal/plugins/maintenance/author_resolve_retired_test.go
// version: 1.1.0
// guid: 7a3d91c5-2b84-4e06-a1f7-d5c08e6b3f29
// last-edited: 2026-11-17

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// maintenance.resolve-production-authors fetches every book credited to a
// production company and nobody picks one, so the server's fetch refuses a
// retired book (metafetch.ErrBookRetired). The op reports that as a skip with
// a count, separate from a failed fetch, and keeps going.
func TestResolveProductionAuthors_RetiredBookIsASkipNotAFailure(t *testing.T) {
	d := newResolveFixture()
	d.fetch = func(_ context.Context, id string) (*int, error) {
		switch id {
		case "b1":
			realID := 30
			return &realID, nil // resolved
		case "b2":
			// The shape Server.FetchMetadataByTitle returns for a retired book.
			return nil, fmt.Errorf("book %q: %w", "Synthetic Title", metafetch.ErrBookRetired)
		default:
			return nil, errors.New("no match") // b3: a real failed fetch
		}
	}
	p := &Plugin{deps: d}
	rep := &opIDReporter{id: "op-retired"}
	require.NoError(t, runGuarded(t, p, context.Background(), json.RawMessage(`{"dry_run":false}`), rep))

	fetched, _, _, _ := d.snapshot()
	require.ElementsMatch(t, []string{"b1", "b2", "b3"}, fetched, "a retired book must not stop the pass")
	require.Contains(t, strings.Join(rep.logs, "\n"),
		"Resolved 1 books across 2 production companies (1 fetches failed, 0 companies' book lists unreadable, 1 retired books skipped)",
		"b2 is a skip, b3 the only failed fetch")
}

// The preview must not promise fetches the apply refuses: a retired book
// (trashed, or merged into another book) is left out of the count, and named
// in the message so the number is not a mystery.
func TestResolveProductionAuthors_DryRunDoesNotCountRetiredBooks(t *testing.T) {
	d := newResolveFixture()
	// b1 live; b2 (co-credited to both companies) merged away; b3 trashed.
	d.guard.books = map[int][]database.BookCore{
		1: {{ID: "b1"}, {ID: "b2", MergedIntoBookID: new("survivor")}},
		2: {{ID: "b2", MergedIntoBookID: new("survivor")}, {ID: "b3", MarkedForDeletion: new(true)}},
	}
	p := &Plugin{deps: d}
	rep := &opIDReporter{id: "op-retired-dry"}
	require.NoError(t, runGuarded(t, p, context.Background(), json.RawMessage(`{"dry_run":true}`), rep))

	logs := strings.Join(rep.logs, "\n")
	require.Contains(t, logs, "DRY RUN: would re-fetch metadata for 1 books across 2 production companies",
		"only the live book is counted")
	require.Contains(t, logs, "2 retired books left out", "a book credited to two companies is counted once")
	fetched, _, _, _ := d.snapshot()
	require.Empty(t, fetched, "a dry run fetches nothing")
}

// The apply does not even ask for a retired book: no provider call, and the
// retired books are reported as skipped.
func TestResolveProductionAuthors_ApplyLeavesRetiredBooksOutOfTheWorkList(t *testing.T) {
	d := newResolveFixture()
	d.guard.books = map[int][]database.BookCore{
		1: {{ID: "b1"}, {ID: "b2", MergedIntoBookID: new("survivor")}},
		2: {{ID: "b2", MergedIntoBookID: new("survivor")}, {ID: "b3", MarkedForDeletion: new(true)}},
	}
	p := &Plugin{deps: d}
	rep := &opIDReporter{id: "op-retired-apply"}
	require.NoError(t, runGuarded(t, p, context.Background(), json.RawMessage(`{"dry_run":false}`), rep))

	fetched, _, _, _ := d.snapshot()
	require.Equal(t, []string{"b1"}, fetched, "no fetch for a retired book")
	require.Contains(t, strings.Join(rep.logs, "\n"), "2 retired books skipped")
}
