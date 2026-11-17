// file: internal/metafetch/fetch_intent_test.go
// version: 1.0.0
// guid: 5e0b7c1a-83d4-4f26-9a7e-c2d6b1f48a93
// last-edited: 2026-11-17

package metafetch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// FetchMetadataForBook and FetchMetadataForBookByTitle search AND apply with
// nobody picking the candidate, so a retired book (trash, or merged into
// another) is refused unless the caller states it is a person's own action on
// this one book (Interactive). Synthetic fixtures only.

type fetchFn struct {
	label string
	run   func(*Service, string, FetchIntent) (*FetchMetadataResponse, error)
}

var fetchFns = []fetchFn{
	{"FetchMetadataForBook", func(s *Service, id string, in FetchIntent) (*FetchMetadataResponse, error) {
		return s.FetchMetadataForBook(context.Background(), id, in)
	}},
	{"FetchMetadataForBookByTitle", func(s *Service, id string, in FetchIntent) (*FetchMetadataResponse, error) {
		return s.FetchMetadataForBookByTitle(context.Background(), id, in)
	}},
}

func retireBook(t *testing.T, f *verdictFixture, id string, rc retiredFieldCase) {
	t.Helper()
	_, err := f.store.ModifyBook(id, func(b *database.Book) error { rc.retire(b); return nil })
	require.NoError(t, err)
}

func TestFetchIntent_UnattendedRefusesRetiredBookBeforeSearching(t *testing.T) {
	for _, fn := range fetchFns {
		for _, rc := range retiredFieldCases {
			t.Run(fn.label+"/"+rc.name, func(t *testing.T) {
				src := &verdictSource{name: "Audible", results: autoFetchRecord("audible description")}
				f, id := autoFetchFixture(t, src)
				retireBook(t, f, id, rc)

				resp, err := fn.run(f.mfs, id, Unattended)
				require.ErrorIs(t, err, ErrBookRetired)
				require.Nil(t, resp)
				require.Zero(t, src.calls.Load(), "a retired book must not spend a provider call")
				require.Nil(t, f.book(id).Description, "nothing may be written to a retired book")
			})
		}
	}
}

// Control: the same Unattended fetch on a live book applies, so the refusals
// above are the retirement and not a broken fixture.
func TestFetchIntent_ControlUnattendedFetchOnLiveBookApplies(t *testing.T) {
	for _, fn := range fetchFns {
		t.Run(fn.label, func(t *testing.T) {
			src := &verdictSource{name: "Audible", results: autoFetchRecord("audible description")}
			f, id := autoFetchFixture(t, src)
			resp, err := fn.run(f.mfs, id, Unattended)
			require.NoError(t, err)
			require.Equal(t, "Audible", resp.Source)
			require.Positive(t, src.calls.Load())
			d := f.book(id).Description
			require.NotNil(t, d)
			require.Equal(t, "audible description", *d)
		})
	}
}

// A person fetching a retired book by hand (the Fetch button) is not refused.
func TestFetchIntent_InteractiveFetchOnRetiredBookIsUnchanged(t *testing.T) {
	for _, fn := range fetchFns {
		for _, rc := range retiredFieldCases {
			t.Run(fn.label+"/"+rc.name, func(t *testing.T) {
				src := &verdictSource{name: "Audible", results: autoFetchRecord("audible description")}
				f, id := autoFetchFixture(t, src)
				retireBook(t, f, id, rc)

				_, err := fn.run(f.mfs, id, Interactive)
				require.NoError(t, err)
				d := f.book(id).Description
				require.NotNil(t, d, "the owner's own fetch applied")
			})
		}
	}
}

// An intent that is neither constant is an error, not a guess in either
// direction: a caller that did not state its intent gets neither behaviour.
func TestFetchIntent_UnstatedIntentIsRefused(t *testing.T) {
	for _, fn := range fetchFns {
		t.Run(fn.label, func(t *testing.T) {
			src := &verdictSource{name: "Audible", results: autoFetchRecord("audible description")}
			f, id := autoFetchFixture(t, src)
			_, err := fn.run(f.mfs, id, FetchIntent(0))
			require.Error(t, err)
			require.NotErrorIs(t, err, ErrBookRetired)
			require.Zero(t, src.calls.Load())
			require.Nil(t, f.book(id).Description)
		})
	}
}

// retireWhileSearching retires the book from inside the provider search, the
// window between the fetch's first read of the row and its write.
type retireWhileSearching struct {
	*verdictSource
	once   func()
	didRun bool
}

func (r *retireWhileSearching) retire() {
	if !r.didRun {
		r.didRun = true
		r.once()
	}
}

func (r *retireWhileSearching) SearchByTitle(ctx context.Context, t string) ([]metadata.BookMetadata, error) {
	r.retire()
	return r.verdictSource.SearchByTitle(ctx, t)
}

func (r *retireWhileSearching) SearchByTitleAndAuthor(ctx context.Context, t, a string) ([]metadata.BookMetadata, error) {
	r.retire()
	return r.verdictSource.SearchByTitleAndAuthor(ctx, t, a)
}

// A book trashed or merged while the provider search ran is not written to:
// the guard re-checks the row under the write lock.
func TestFetchIntent_UnattendedFetchRetiredDuringSearchIsNotWritten(t *testing.T) {
	for _, fn := range fetchFns {
		for _, rc := range retiredFieldCases {
			t.Run(fn.label+"/"+rc.name, func(t *testing.T) {
				src := &retireWhileSearching{verdictSource: &verdictSource{name: "Audible", results: autoFetchRecord("audible description")}}
				f, id := autoFetchFixture(t, src)
				src.once = func() { retireBook(t, f, id, rc) }

				_, err := fn.run(f.mfs, id, Unattended)
				require.True(t, src.didRun, "the search must have run for this to test the in-lock check")
				require.ErrorIs(t, err, ErrBookRetired, "the under-lock refusal is typed")
				require.Nil(t, f.book(id).Description, "nothing was committed onto the retired row")
			})
		}
	}
}
