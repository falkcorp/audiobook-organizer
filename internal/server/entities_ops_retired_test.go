// file: internal/server/entities_ops_retired_test.go
// version: 1.1.0
// guid: 7733bb72-345a-4ee6-8a06-e0268bf4721e
// last-edited: 2026-11-18

package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// entities.resolve-production-author loops over every book credited to a
// production company, unattended, and its writes happen AFTER a fetch that can
// take seconds per book. A book retired in that window used to be written onto:
// the title-only fetch's retired guard only runs at commit, so a fetch that
// finds nothing never reaches it, and the Publisher and author closures read the
// row under the lock but never looked at its state.

const resolveRetiredAuthor = "Alfred Pennyworth"

// retireMeanwhile marks book id merged into another book, the way a merge that
// lands between the op's work list and its write does.
func retireMeanwhile(t *testing.T, store database.Store, id string) {
	t.Helper()
	_, err := store.ModifyBook(id, func(b *database.Book) error {
		into := "survivor-book-id"
		b.MergedIntoBookID = &into
		return nil
	})
	require.NoError(t, err)
}

// retiringSource is a provider that merges book id away while it is being
// searched (the fetch's slow window), then answers with results.
type retiringSource struct {
	countingSource
	retire func()
	fired  atomic.Bool
}

func (r *retiringSource) SearchByTitle(ctx context.Context, q string) ([]metadata.BookMetadata, error) {
	if r.fired.CompareAndSwap(false, true) {
		r.retire()
	}
	return r.countingSource.SearchByTitle(ctx, q)
}

// retireBeforeModifyStore merges the book away right before the op's first
// ModifyBook takes the row lock: the other writer of the lost-update race.
type retireBeforeModifyStore struct {
	database.Store
	retire func()
	fired  atomic.Bool
}

func (s *retireBeforeModifyStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if s.fired.CompareAndSwap(false, true) {
		s.retire()
	}
	return s.Store.ModifyBook(id, fn)
}

// fakeCoverAI answers every cover with one confident author and counts the
// calls: each one is a paid vision request.
type fakeCoverAI struct {
	aiParser
	calls atomic.Int64
}

func (f *fakeCoverAI) IsEnabled() bool { return true }
func (f *fakeCoverAI) ParseCoverArt(context.Context, []byte, string) (*ai.ParsedMetadata, error) {
	f.calls.Add(1)
	return &ai.ParsedMetadata{Author: resolveRetiredAuthor, Confidence: "high"}, nil
}

// writeCoverFile writes a file whose ID3v2.3 tag carries one APIC frame, which
// is all ExtractCoverArtBytes needs.
func writeCoverFile(t *testing.T, dir string) string {
	t.Helper()
	body := append([]byte{0}, []byte("image/jpeg\x00")...)
	body = append(body, 3, 0)
	body = append(body, 0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F')
	frame := append([]byte("APIC"), byte(len(body)>>24), byte(len(body)>>16), byte(len(body)>>8), byte(len(body)), 0, 0)
	frame = append(frame, body...)
	n := len(frame)
	hdr := []byte{'I', 'D', '3', 3, 0, 0, byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f)}
	path := filepath.Join(dir, "cover.mp3")
	require.NoError(t, os.WriteFile(path, append(hdr, frame...), 0o600))
	data, _, err := metadata.ExtractCoverArtBytes(path)
	require.NoError(t, err)
	require.NotEmpty(t, data, "the fixture's cover must be readable or the AI path is never reached")
	return path
}

type resolveRun struct {
	s         *Server
	bookID    string
	prodID    int
	logs      string
	sourceN   int64
	aiCalls   int64
	finalBk   *database.Book
	finalJoin []database.BookAuthor
}

// runResolve builds a server with one live book credited to the production
// company, runs the op over it, and returns what is left. src is the metadata
// provider (a retiringSource is bound to merge the book away mid-search), and
// wrap, when set, replaces the store the op writes through so a test can land
// the other writer right before a ModifyBook takes the row lock. withCover gives
// the book a file whose tag carries a cover, which is what opens the AI pass.
func runResolve(t *testing.T, src metadata.MetadataSource, withCover bool, wrap func(s *Server, id string) database.Store) resolveRun {
	t.Helper()
	s, cleanup := setupTestServer(t)
	t.Cleanup(cleanup)
	store := s.storeForWiring()
	mfs := metafetch.NewService(store)
	mfs.SetOverrideSources([]metadata.MetadataSource{src})
	s.metadataFetchService = mfs

	prod, err := store.CreateAuthor(prodCompanyName)
	require.NoError(t, err)
	path := "/lib/Batman Year One/b.m4b"
	if withCover {
		path = writeCoverFile(t, t.TempDir())
	}
	created, err := store.CreateBook(&database.Book{Title: "Batman Year One", FilePath: path})
	require.NoError(t, err)
	id := created.ID
	require.NoError(t, store.SetBookAuthors(id, []database.BookAuthor{{BookID: id, AuthorID: prod.ID, Role: "author", Position: 0}}))
	if cs, ok := src.(interface{ bind(func()) }); ok {
		cs.bind(func() { retireMeanwhile(t, store, id) })
	}
	if wrap != nil {
		s.store = wrap(s, id)
	}

	fake := &fakeCoverAI{}
	prevAI, prevKey, prevEnable := newAIParser, config.AppConfig.OpenAIAPIKey, config.AppConfig.EnableAIParsing
	newAIParser = func(string, bool) aiParser { return fake }
	config.AppConfig.EnableAIParsing = true
	t.Cleanup(func() {
		newAIParser, config.AppConfig.OpenAIAPIKey, config.AppConfig.EnableAIParsing = prevAI, prevKey, prevEnable
	})

	reg := opsregistry.New(store, slog.New(slog.DiscardHandler), 1, nil)
	require.NoError(t, s.RegisterResolveProductionAuthorOp(reg))
	def, ok := reg.Def("entities.resolve-production-author")
	require.True(t, ok)
	params, err := json.Marshal(resolveProductionAuthorOpParams{AuthorID: prod.ID, ProdAuthorName: prodCompanyName})
	require.NoError(t, err)
	rec := &resumeRecorder{opID: "op-resolve-prod-retired"}
	require.NoError(t, def.Run(context.Background(), params, rec))

	final, err := store.GetBookByID(id)
	require.NoError(t, err)
	join, err := store.GetBookAuthors(id)
	require.NoError(t, err)
	r := resolveRun{s: s, bookID: id, prodID: prod.ID, logs: strings.Join(rec.logLines(), "\n"), aiCalls: fake.calls.Load(), finalBk: final, finalJoin: join}
	if cs, ok := src.(interface{ n() int64 }); ok {
		r.sourceN = cs.n()
	}
	return r
}

func (r *retiringSource) bind(f func()) { r.retire = f }
func (r *retiringSource) n() int64      { return r.calls.Load() }

// A book merged away while its title-only search is in flight: the fetch finds
// nothing, so its own retired guard never runs, and the cover pass used to buy a
// vision answer and write the author onto the retired row.
func TestResolveProductionAuthorOp_BookRetiredMidRunOnNoResultPathIsNotWritten(t *testing.T) {
	src := &retiringSource{countingSource: countingSource{name: "SrcA"}}
	r := runResolve(t, src, true, nil)

	require.Positive(t, r.sourceN, "the book was live at the start, so the search ran")
	require.Zero(t, r.aiCalls, "a book retired during the search must not buy a cover analysis")
	require.Nil(t, r.finalBk.AuthorID, "the retired book's author must stay as it was")
	require.Len(t, r.finalJoin, 1)
	require.Equal(t, r.prodID, r.finalJoin[0].AuthorID, "the retired book's author join must stay as it was")
	require.Contains(t, r.logs, "1 retired books skipped")
	require.Contains(t, r.logs, "0 unresolved", "a retired book is a skip, not an unresolved failure")
}

// The race after the cover pass's own re-read: the book is merged away between
// the re-read and the author write's row lock, so only the closure can see it.
func TestResolveProductionAuthorOp_BookRetiredBeforeAuthorWriteIsNotWritten(t *testing.T) {
	src := &countingSource{name: "SrcA"}
	r := runResolve(t, src, true, func(s *Server, id string) database.Store {
		inner := s.storeForWiring()
		return &retireBeforeModifyStore{Store: inner, retire: func() { retireMeanwhile(t, inner, id) }}
	})

	require.Equal(t, int64(1), r.aiCalls, "the book was live when the cover pass re-read it")
	require.Nil(t, r.finalBk.AuthorID, "the author write must not land on a retired book")
	require.Len(t, r.finalJoin, 1)
	require.Equal(t, r.prodID, r.finalJoin[0].AuthorID, "the join rewrite must be skipped with the refused author write")
	require.Contains(t, r.logs, "1 retired books skipped")
	require.Contains(t, r.logs, "0 unresolved")
}

// The Publisher path: the fetch found and applied an author, then the book was
// merged away before the Publisher reclassification took the row lock.
func TestResolveProductionAuthorOp_BookRetiredBeforePublisherWriteIsNotWritten(t *testing.T) {
	src := &countingSource{name: "Finds", results: []metadata.BookMetadata{{Title: "Batman Year One", Author: resolveRetiredAuthor}}}
	r := runResolve(t, src, false, func(s *Server, id string) database.Store {
		inner := s.storeForWiring()
		return &retireBeforeModifyStore{Store: inner, retire: func() { retireMeanwhile(t, inner, id) }}
	})

	require.Nil(t, r.finalBk.Publisher, "the Publisher write must not land on a retired book")
	require.Contains(t, r.logs, "1 retired books skipped")
	require.Contains(t, r.logs, "0 skipped on hydrate error")
}

// Control: the same two paths on a live book write.
func TestResolveProductionAuthorOp_ControlLiveBookIsWrittenOnBothPaths(t *testing.T) {
	t.Run("author", func(t *testing.T) {
		r := runResolve(t, &countingSource{name: "SrcA"}, true, nil)
		require.Equal(t, int64(1), r.aiCalls)
		require.NotNil(t, r.finalBk.AuthorID)
		require.NotEqual(t, r.prodID, *r.finalBk.AuthorID, "the live book is re-attributed")
		require.Contains(t, r.logs, "0 retired books skipped")
	})
	t.Run("publisher", func(t *testing.T) {
		src := &countingSource{name: "Finds", results: []metadata.BookMetadata{{Title: "Batman Year One", Author: resolveRetiredAuthor}}}
		r := runResolve(t, src, false, nil)
		require.NotNil(t, r.finalBk.Publisher)
		require.Equal(t, prodCompanyName, *r.finalBk.Publisher)
		require.Contains(t, r.logs, "0 retired books skipped")
	})
}

// The closures themselves, with the stateful store: the sentinel comes back and
// neither the row nor the join is touched.
func TestAssignResolvedAuthorPreservingRecord_RefusesRetiredRowAndSkipsTheJoin(t *testing.T) {
	prod := 7
	store := newLostUpdateBookStore(&database.Book{ID: "b1", Title: "Book", AuthorID: &prod})
	store.concurrent = func(b *database.Book) { into := "survivor"; b.MergedIntoBookID = &into }
	var joinWrites int
	store.SetBookAuthorsFunc = func(string, []database.BookAuthor) error { joinWrites++; return nil }

	err := assignResolvedAuthorPreservingRecord(store, "b1", 42, prod)
	require.ErrorIs(t, err, errResolveBookRetired)
	require.Zero(t, joinWrites, "the join must not be rewritten when the row write was refused")
	require.Equal(t, prod, *store.stored("b1").AuthorID)
}

func TestAssignPublisherPreservingRecord_RefusesRetiredRow(t *testing.T) {
	for _, shape := range retiredShapes {
		t.Run(shape.name, func(t *testing.T) {
			store := newLostUpdateBookStore(&database.Book{ID: "b1", Title: "Book"})
			store.concurrent = func(b *database.Book) { shape.retire(b) }

			err := assignPublisherPreservingRecord(store, "b1", "SomeProduction LLC")
			require.ErrorIs(t, err, errResolveBookRetired)
			require.Nil(t, store.stored("b1").Publisher)
		})
	}
}

// B4. The author write goes through ModifyBook (row lock), and the join rewrite
// used to run after that lock was released: a book merged away in between had
// its credits rewritten. The join is now rewritten in a second locked pass that
// rechecks the row. The merge lands once the AuthorID column is already the
// resolved author's, which is when only the second pass can see it.
func TestAssignResolvedAuthorPreservingRecord_DoesNotRewriteTheJoinOfABookRetiredAfterTheAuthorWrite(t *testing.T) {
	prod, resolved := 7, 42
	store := newLostUpdateBookStore(&database.Book{ID: "b1", Title: "Book", AuthorID: &prod})
	store.concurrent = func(b *database.Book) {
		if b.AuthorID != nil && *b.AuthorID == resolved {
			into := "survivor"
			b.MergedIntoBookID = &into
		}
	}
	var joinWrites int
	store.GetBookAuthorsFunc = func(string) ([]database.BookAuthor, error) {
		return []database.BookAuthor{{BookID: "b1", AuthorID: prod, Role: "author", Position: 0}}, nil
	}
	store.SetBookAuthorsFunc = func(string, []database.BookAuthor) error { joinWrites++; return nil }

	err := assignResolvedAuthorPreservingRecord(store, "b1", resolved, prod)

	require.ErrorIs(t, err, errResolveBookRetired, "a book retired before the join pass is a retired skip")
	require.Zero(t, joinWrites, "the join of a retired book must not be rewritten")
	require.Equal(t, resolved, *store.stored("b1").AuthorID, "the column was written while the book was live")
}

// The live control: with no merge in between the join is rewritten, once, onto
// the resolved author in the production company's slot.
func TestAssignResolvedAuthorPreservingRecord_LiveBookGetsItsJoinRewrittenOnce(t *testing.T) {
	prod, resolved := 7, 42
	store := newLostUpdateBookStore(&database.Book{ID: "b1", Title: "Book", AuthorID: &prod})
	var written [][]database.BookAuthor
	store.GetBookAuthorsFunc = func(string) ([]database.BookAuthor, error) {
		return []database.BookAuthor{{BookID: "b1", AuthorID: prod, Role: "author", Position: 0}}, nil
	}
	store.SetBookAuthorsFunc = func(_ string, a []database.BookAuthor) error { written = append(written, a); return nil }

	require.NoError(t, assignResolvedAuthorPreservingRecord(store, "b1", resolved, prod))

	require.Len(t, written, 1)
	require.Len(t, written[0], 1)
	require.Equal(t, resolved, written[0][0].AuthorID)
	require.Equal(t, 0, written[0][0].Position, "the resolved author takes the production company's slot")
}
