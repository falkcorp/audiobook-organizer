// file: internal/plugins/maintenance/author_ops_dry_run_test.go
// version: 1.0.0
// guid: 5c1e4571-8bcf-4190-ac2d-ae141026d139
// last-edited: 2026-09-25

package maintenance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// author-split-scan and resolve-production-authors ignored their params and
// wrote on every run until 2026-09-25. Both now default to a dry run.

func newDryRunSplitStore(t *testing.T) (*database.PebbleStore, int, string) {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	st.WaitForWarmup()
	t.Cleanup(func() { _ = st.Close() })
	composite, err := st.CreateAuthor("Alice Smith & Bob Jones")
	require.NoError(t, err)
	b, err := st.CreateBook(&database.Book{
		Title:            "dry-run-split",
		FilePath:         "/dry-run-split/book.m4b",
		AuthorID:         new(composite.ID),
		IsPrimaryVersion: new(true),
	})
	require.NoError(t, err)
	require.NoError(t, st.SetBookAuthors(b.ID, []database.BookAuthor{{BookID: b.ID, AuthorID: composite.ID, Role: "author"}}))
	return st, composite.ID, b.ID
}

func TestAuthorSplitScan_DefaultIsDryRun(t *testing.T) {
	for _, params := range []string{"", `{}`, `{"dry_run":true}`, `{"dryRun":true}`} {
		t.Run("params="+params, func(t *testing.T) {
			st, compositeID, bookID := newDryRunSplitStore(t)
			p := &Plugin{deps: &fakeDeps{store: st}}
			rep := &fakeReporter{}
			var raw json.RawMessage
			if params != "" {
				raw = json.RawMessage(params)
			}
			require.NoError(t, p.runAuthorSplitScan(context.Background(), raw, rep))

			still, err := st.GetAuthorByID(compositeID)
			require.NoError(t, err)
			require.NotNil(t, still, "dry run deleted the composite author")
			alice, err := st.GetAuthorByName("Alice Smith")
			require.NoError(t, err)
			require.Nil(t, alice, "dry run created a split author")
			credits, err := st.GetBookAuthors(bookID)
			require.NoError(t, err)
			require.Len(t, credits, 1)
			require.Equal(t, compositeID, credits[0].AuthorID, "dry run relinked the book")
			require.Contains(t, strings.Join(rep.logs, "\n"), "DRY RUN: would split 1 composite authors, create 2 authors, relink 1 books")
		})
	}
}

// The camelCase alias applies too, so an operator using either spelling gets
// the write they asked for.
func TestAuthorSplitScan_ExplicitApplyWrites(t *testing.T) {
	st, compositeID, _ := newDryRunSplitStore(t)
	p := &Plugin{deps: &fakeDeps{store: st}}
	require.NoError(t, p.runAuthorSplitScan(context.Background(), json.RawMessage(`{"dryRun":false}`), &fakeReporter{}))
	gone, err := st.GetAuthorByID(compositeID)
	require.NoError(t, err)
	require.Nil(t, gone, "apply should delete the composite")
}

func TestAuthorOpDryRun_DisagreeingKeysAreAnError(t *testing.T) {
	st, compositeID, _ := newDryRunSplitStore(t)
	p := &Plugin{deps: &fakeDeps{store: st}}
	err := p.runAuthorSplitScan(context.Background(), json.RawMessage(`{"dry_run":false,"dryRun":true}`), &fakeReporter{})
	require.ErrorContains(t, err, "disagree")
	still, gerr := st.GetAuthorByID(compositeID)
	require.NoError(t, gerr)
	require.NotNil(t, still)
	err = p.runResolveProductionAuthors(context.Background(), json.RawMessage(`{"dry_run":true,"dryRun":false}`), &fakeReporter{})
	require.ErrorContains(t, err, "disagree")
}

// countingDeps records the one write resolve-production-authors makes today:
// the dedup cache invalidation.
type countingDeps struct {
	fakeDeps
	invalidations *int
}

func (d countingDeps) InvalidateDedupCache() { *d.invalidations++ }

func TestResolveProductionAuthors_DefaultIsDryRun(t *testing.T) {
	store := &database.MockStore{
		GetAllAuthorsFunc: func() ([]database.Author, error) {
			return []database.Author{{ID: 1, Name: "Tantor Media"}, {ID: 2, Name: "Jane Roe"}}, nil
		},
	}
	n := 0
	p := &Plugin{deps: countingDeps{fakeDeps: fakeDeps{store: store}, invalidations: &n}}

	rep := &fakeReporter{}
	require.NoError(t, p.runResolveProductionAuthors(context.Background(), nil, rep))
	require.Equal(t, 0, n, "dry run invalidated the dedup cache")
	require.Contains(t, strings.Join(rep.logs, "\n"), "DRY RUN: 1 production company authors found")

	require.NoError(t, p.runResolveProductionAuthors(context.Background(), json.RawMessage(`{"dry_run":false}`), &fakeReporter{}))
	require.Equal(t, 1, n, "apply should invalidate the dedup cache")
}
