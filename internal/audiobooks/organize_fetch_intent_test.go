// file: internal/audiobooks/organize_fetch_intent_test.go
// version: 1.0.1
// guid: 0d6a8b3e-47c1-4e92-b5f0-93a7c2e1d864
// last-edited: 2026-11-17

package audiobooks

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// The fetch-first callback newOrganizeService wires into the organizer is an
// unattended caller of metafetch's FetchMetadataForBook: a retired book (in the
// trash, or merged into another) is refused before any provider is searched,
// and comes back as the organizer's own skip sentinel (its package cannot
// import metafetch) that still wraps metafetch.ErrBookRetired.
func TestNewOrganizeService_FetchFirstCallbackIsUnattended(t *testing.T) {
	yes := true
	into := "survivor-book-id"
	for name, retire := range map[string]func(*database.Book){
		"soft_deleted": func(b *database.Book) { b.MarkedForDeletion = &yes },
		"merged_into":  func(b *database.Book) { b.MergedIntoBookID = &into },
	} {
		t.Run(name, func(t *testing.T) {
			store, err := database.NewPebbleStore(t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			book := &database.Book{Title: "The Long Halloween", FilePath: "/lib/tlh/book.m4b"}
			retire(book)
			created, err := store.CreateBook(book)
			require.NoError(t, err)

			svc := newOrganizeService(store, store)
			resp, err := svc.FetchMetadataForBook(context.Background(), created.ID)
			require.ErrorIs(t, err, organizer.ErrFetchSkippedRetired)
			require.ErrorIs(t, err, metafetch.ErrBookRetired)
			require.Nil(t, resp)
		})
	}
}

// relocationSpy records the copy the organize service creates through
// the relocating writer, over a real store.
type relocationSpy struct {
	*database.PebbleStore
	originals []string
}

func (s *relocationSpy) CreateBookWithRelocation(b *database.Book, originalID string) (*database.Book, error) {
	s.originals = append(s.originals, originalID)
	return s.PebbleStore.CreateBookWithRelocation(b, originalID)
}

// newOrganizeService hands its creator to the organizer as the writer of the
// single-book organize's copy (F6-03): without that wiring the copy is refused
// (ErrRelocatingCopyCreatorNotWired) and no torrent follow-up could be staged.
func TestNewOrganizeServiceWiresTheRelocatingCopyCreator(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	ps.WaitForWarmup()
	t.Cleanup(func() { _ = ps.Close() })
	spy := &relocationSpy{PebbleStore: ps}
	imported := "imported"
	src, err := ps.CreateBook(&database.Book{Title: "Year One", FilePath: filepath.Join(t.TempDir(), "year-one.m4b"), LibraryState: &imported})
	require.NoError(t, err)

	svc := newOrganizeService(spy, spy)
	copyBook, err := svc.CreateOrganizedVersionFollowTorrents(src, &organizer.Landing{Path: filepath.Join(t.TempDir(), "organized-year-one.m4b")}, "", logger.New("test"))
	require.NoError(t, err)
	require.NotNil(t, copyBook)
	require.Equal(t, []string{src.ID}, spy.originals)
}

// RequireRelocatingCopyCreator is books' boot check of the production store: the
// real store passes; a store without CreateBookWithRelocation (a wrapper that
// embeds database.Store, a generated mock) is refused, naming the interface.
func TestRequireRelocatingCopyCreator(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	ps.WaitForWarmup()
	t.Cleanup(func() { _ = ps.Close() })
	require.NoError(t, RequireRelocatingCopyCreator(ps))

	type wrapped struct{ database.Store }
	err = RequireRelocatingCopyCreator(wrapped{ps})
	require.Error(t, err)
	require.Contains(t, err.Error(), "organizer.RelocatingCopyCreator")
	require.Contains(t, err.Error(), "wrapped")
}

// The container builds the organize service over whatever store the "store"
// slot holds. A store with the method wires it (the copy stages its follow-up);
// one without wires none, and the single-book copy refuses rather than creating a
// copy that moves no torrent (the silent loss the relocation exists to prevent).
func TestServiceDefs_OrganizeCopyNeedsTheCreatorTheStoreHas(t *testing.T) {
	for name, wrap := range map[string]func(*database.PebbleStore) database.Store{
		"store_has_the_method": func(ps *database.PebbleStore) database.Store { return ps },
		"wrapped_store":        func(ps *database.PebbleStore) database.Store { return struct{ database.Store }{ps} },
	} {
		t.Run(name, func(t *testing.T) {
			ps, err := database.NewPebbleStore(t.TempDir())
			require.NoError(t, err)
			ps.WaitForWarmup()
			t.Cleanup(func() { _ = ps.Close() })
			imported := "imported"
			src, err := ps.CreateBook(&database.Book{Title: "Year One", FilePath: filepath.Join(t.TempDir(), "year-one.m4b"), LibraryState: &imported})
			require.NoError(t, err)

			c := serviceregistry.NewContainerFrom(ServiceDefs()...).Override(serviceregistry.KeyStore, wrap(ps))
			require.NoError(t, c.Resolve())
			require.NoError(t, c.Build(t.Context()))
			svc := serviceregistry.Get[*OrganizeService](c, serviceregistry.KeyOrganize)

			_, err = svc.CreateOrganizedVersionFollowTorrents(src, &organizer.Landing{Path: filepath.Join(t.TempDir(), "organized-year-one.m4b")}, "", logger.New("test"))
			if name == "store_has_the_method" {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, organizer.ErrRelocatingCopyCreatorNotWired)
		})
	}
}
