// file: internal/server/fetch_intent_callers_test.go
// version: 1.0.2
// guid: ba95b95d-5a6d-4c26-bb9e-9f78520ebbc9
// last-edited: 2026-11-17

package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// Two server-side callers of the title-only metadata fetch are unattended: the
// maintenance plugin's resolver (through Server.FetchMetadataByTitle) and the
// entities.resolve-production-author op. A retired book must cost neither a
// provider call nor a write, and neither caller may count it as a failure.

// prodCompanyName is a fictional Gotham production company. The detector
// (personname.IsProductionCompany) matches a lowercased known-name list and also
// any name ending in " theater" or " theatre", so this fires it without a real
// publisher's name in the fixtures.
const prodCompanyName = "Gotham Theater"

func retiredFetchBook(t *testing.T, s *Server, title string, retire func(*database.Book)) string {
	t.Helper()
	b := database.Book{Title: title, FilePath: "/lib/" + title + "/b.m4b"}
	if retire != nil {
		retire(&b)
	}
	got, err := s.storeForWiring().CreateBook(&b)
	require.NoError(t, err)
	return got.ID
}

func TestServerFetchMetadataByTitle_IsUnattended(t *testing.T) {
	for _, shape := range retiredShapes {
		t.Run(shape.name, func(t *testing.T) {
			s, cleanup := setupTestServer(t)
			defer cleanup()
			src := &countingSource{name: "SrcA"}
			mfs := metafetch.NewService(s.storeForWiring())
			mfs.SetOverrideSources([]metadata.MetadataSource{src})
			s.metadataFetchService = mfs
			id := retiredFetchBook(t, s, "Batman "+shape.name, shape.retire)

			_, err := s.FetchMetadataByTitle(context.Background(), id)
			require.ErrorIs(t, err, metafetch.ErrBookRetired)
			require.Zero(t, sourceCalls(src), "a retired book must not spend a provider call")
		})
	}
}

// Control: a live book reaches the provider, so the zero above is the refusal.
func TestServerFetchMetadataByTitle_ControlLiveBookSearches(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	src := &countingSource{name: "SrcA"}
	mfs := metafetch.NewService(s.storeForWiring())
	mfs.SetOverrideSources([]metadata.MetadataSource{src})
	s.metadataFetchService = mfs
	id := retiredFetchBook(t, s, "Batman Year One", nil)

	_, err := s.FetchMetadataByTitle(context.Background(), id)
	require.NotErrorIs(t, err, metafetch.ErrBookRetired)
	require.Positive(t, sourceCalls(src))
}

// runResolveProductionAuthor runs entities.resolve-production-author over one
// book credited to a production company and returns the source's call count
// and the op's log lines.
func runResolveProductionAuthor(t *testing.T, retire func(*database.Book)) (int64, []string) {
	t.Helper()
	s, cleanup := setupTestServer(t)
	t.Cleanup(cleanup)
	store := s.storeForWiring()
	src := &countingSource{name: "SrcA"}
	mfs := metafetch.NewService(store)
	mfs.SetOverrideSources([]metadata.MetadataSource{src})
	s.metadataFetchService = mfs

	prod, err := store.CreateAuthor(prodCompanyName)
	require.NoError(t, err)
	id := retiredFetchBook(t, s, "Batman Year One", retire)
	require.NoError(t, store.SetBookAuthors(id, []database.BookAuthor{{BookID: id, AuthorID: prod.ID, Role: "author", Position: 0}}))

	reg := opsregistry.New(store, slog.New(slog.DiscardHandler), 1, nil)
	require.NoError(t, s.RegisterResolveProductionAuthorOp(reg))
	def, ok := reg.Def("entities.resolve-production-author")
	require.True(t, ok)
	params, err := json.Marshal(resolveProductionAuthorOpParams{AuthorID: prod.ID, ProdAuthorName: prodCompanyName})
	require.NoError(t, err)
	rec := &resumeRecorder{opID: "op-resolve-prod"}
	require.NoError(t, def.Run(context.Background(), params, rec))
	return sourceCalls(src), rec.logLines()
}

func TestResolveProductionAuthorOp_SkipsRetiredBookWithoutASearch(t *testing.T) {
	for _, shape := range retiredShapes {
		t.Run(shape.name, func(t *testing.T) {
			calls, logs := runResolveProductionAuthor(t, shape.retire)
			joined := strings.Join(logs, "\n")
			require.Zero(t, calls, "a retired book must not spend a provider call")
			if shape.name == "soft_deleted" {
				// The store's per-author list already leaves trashed books out,
				// so the op never reaches one; the guard is the second line.
				require.Contains(t, joined, "Resolving 0 books")
				return
			}
			require.Contains(t, joined, "Resolving 1 books", "the merged book is in the op's work list, so the skip is what keeps it out of a search")
			require.Contains(t, joined, "0 unresolved", "a retired book is a skip, not an unresolved failure")
			require.Contains(t, joined, "1 retired books skipped")
		})
	}
}

// Control: the same book, live, is searched and counted unresolved.
func TestResolveProductionAuthorOp_ControlLiveBookIsSearched(t *testing.T) {
	calls, logs := runResolveProductionAuthor(t, nil)
	joined := strings.Join(logs, "\n")
	require.Positive(t, calls)
	require.Contains(t, joined, "1 unresolved")
	require.Contains(t, joined, "0 retired books skipped")
}
