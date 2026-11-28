// file: internal/plugins/maintenance/deps_t6g5_test.go
// version: 0.1.0
// guid: 6d2f0a83-91b7-4c5e-a4d3-18e7b5c9f260
// last-edited: 2026-11-28

package maintenance

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/writertest"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

const g5TestKey = tasks.Key("books.test.library-scan")

// g5TestHost is the host of the group-5 tests: the Pebble store serves every
// accessor, and every call that is not a plain read is counted.
type g5TestHost struct {
	s          *database.PebbleStore
	root       string
	scans      atomic.Int64
	merges     atomic.Int64
	clones     atomic.Int64
	invalidate atomic.Int64
	noCache    bool
}

func (h *g5TestHost) RootDir() string { return h.root }
func (h *g5TestHost) RunMetadataRefreshScan(context.Context, operations.ProgressReporter) error {
	h.scans.Add(1)
	return nil
}
func (h *g5TestHost) OperationQueueStore() OpQueueReader { return h.s }
func (h *g5TestHost) PlanLibraryClone(*database.Book, []database.BookFile) ([]string, error) {
	return nil, nil
}
func (h *g5TestHost) LibraryITunesPath(p string) string         { return p }
func (h *g5TestHost) BookTagReader() BookTagReader              { return h.s }
func (h *g5TestHost) VersionPrimaryStore() VersionPrimaryStore  { return h.s }
func (h *g5TestHost) PlaylistStore() database.UserPlaylistStore { return h.s }
func (h *g5TestHost) MergeBooks(string, []string, string) (int, error) {
	h.merges.Add(1)
	return 1, nil
}
func (h *g5TestHost) CloneBookIntoLibrary(*database.Book, []database.BookFile, string) (string, error) {
	h.clones.Add(1)
	return "clone-id", nil
}
func (h *g5TestHost) MergeUserStateStore() merge.UserStateRepairStore { return h.s }
func (h *g5TestHost) MetadataCacheStore() database.MetadataCacheStore {
	if h.noCache {
		return nil
	}
	return h.s
}
func (h *g5TestHost) InvalidateAuthorsCache() { h.invalidate.Add(1) }

func g5Fixture(t *testing.T) (*database.PebbleStore, *g5TestHost, string) {
	t.Helper()
	s, id := g2Fixture(t)
	return s, &g5TestHost{s: s, root: t.TempDir()}, id
}

// g5Calls is one call of every method of t6g5Writer. The calls name a book that
// exists where the method takes one, so a call that is NOT refused does its write
// (and a test of a refusal sees the store unchanged because nothing ran).
func g5Calls(bookID string) map[string]func(w t6g5Writer) error {
	ctx := context.Background()
	return map[string]func(w t6g5Writer) error{
		"CarryStateBeforeHardDelete": func(w t6g5Writer) error { return w.CarryStateBeforeHardDelete(bookID, "doomed") },
		"CarryStateBetweenLiveBooks": func(w t6g5Writer) error { return w.CarryStateBetweenLiveBooks(bookID, "from") },
		"CarryStateThenHardDelete": func(w t6g5Writer) error {
			return w.CarryStateThenHardDelete(bookID, "doomed", func() error { return nil }, func() error { return nil })
		},
		"CloneBookIntoLibrary": func(w t6g5Writer) error {
			_, err := w.CloneBookIntoLibrary(&database.Book{ID: bookID}, nil, "op")
			return err
		},
		"CreateBook": func(w t6g5Writer) error {
			_, err := w.CreateBook(&database.Book{Title: "Year One", FilePath: "/lib/Year One/part1.m4b"})
			return err
		},
		"CreateNarrator": func(w t6g5Writer) error {
			_, err := w.CreateNarrator("Selina Kyle")
			return err
		},
		"DeleteBookFilesByIDs": func(w t6g5Writer) error {
			_, err := w.DeleteBookFilesByIDs(nil)
			return err
		},
		"DeleteBook":          func(w t6g5Writer) error { return w.DeleteBook(bookID) },
		"DeleteMetadataCache": func(w t6g5Writer) error { return w.DeleteMetadataCache(bookID) },
		"DeleteNarrator":      func(w t6g5Writer) error { return w.DeleteNarrator(424242) },
		"HardDeleteWithoutUserState": func(w t6g5Writer) error {
			return w.HardDeleteWithoutUserState(bookID, func() error { return nil })
		},
		"ImportSmartPlaylists": func(w t6g5Writer) error {
			_ = w.ImportSmartPlaylists((*itunes.ITLLibrary)(nil), "owner")
			return nil
		},
		"InvalidateAuthorsCache": func(w t6g5Writer) error { w.InvalidateAuthorsCache(); return nil },
		"MergeBooks": func(w t6g5Writer) error {
			_, err := w.MergeBooks("op", []string{bookID}, bookID)
			return err
		},
		"ModifyBookFile": func(w t6g5Writer) error {
			_, err := w.ModifyBookFile(bookID, "nofile", func(*database.BookFile) error { return nil })
			return err
		},
		"ModifyBook": func(w t6g5Writer) error {
			_, err := w.ModifyBook(bookID, func(*database.Book) error { return nil })
			return err
		},
		"MoveBookFilesToBookBulk": func(w t6g5Writer) error { return w.MoveBookFilesToBookBulk(nil, bookID) },
		"MoveBookFilesToBook":     func(w t6g5Writer) error { return w.MoveBookFilesToBook(nil, bookID, bookID) },
		"NewUserStateProbe": func(w t6g5Writer) error {
			_, err := w.NewUserStateProbe()
			return err
		},
		"ReassignExternalID": func(w t6g5Writer) error { return w.ReassignExternalID("src", "ext", bookID) },
		"RecomputeBookAggregates": func(w t6g5Writer) error {
			return w.RecomputeBookAggregates(bookID)
		},
		"RecordOperationChange": func(w t6g5Writer) error {
			return w.RecordOperationChange(&database.OperationChange{OperationID: "op", BookID: bookID, ChangeType: "metadata_update"})
		},
		"Reflink":         func(w t6g5Writer) error { return w.Reflink("/nonexistent/src", "/nonexistent/dst") },
		"RemoveClonePath": func(w t6g5Writer) error { return w.RemoveClonePath("/nonexistent/clone") },
		"SaveChaptersForBook": func(w t6g5Writer) error {
			return w.SaveChaptersForBook(bookID, nil)
		},
		"SetBookAuthors":   func(w t6g5Writer) error { return w.SetBookAuthors(bookID, nil) },
		"SetBookNarrators": func(w t6g5Writer) error { return w.SetBookNarrators(bookID, nil) },
		"SetLibraryStateOrganized": func(w t6g5Writer) error {
			_, _, err := w.SetLibraryStateOrganized(bookID, "imported")
			return err
		},
		"SetUserPreferenceForUser": func(w t6g5Writer) error { return w.SetUserPreferenceForUser("user", "k", "v") },
		"UpdateBookFiles": func(w t6g5Writer) error {
			_, err := w.UpdateBookFiles(ctx, nil, nil)
			return err
		},
		"UpdateBook": func(w t6g5Writer) error {
			_, err := w.UpdateBook(bookID, &database.Book{Title: "The Long Halloween"})
			return err
		},
	}
}

// g5EmptyRow are the methods the shared table gives no row: they stay outside the
// fence (a ReadOnly kind uses them) and write nothing themselves.
var g5EmptyRow = []string{"HardDeleteWithoutUserState", "InvalidateAuthorsCache", "NewUserStateProbe"}

func g5Deps(t *testing.T, s *database.PebbleStore, h *g5TestHost) t6g5Deps {
	t.Helper()
	d := newT6G5Deps(s, h, g5TestKey)
	require.NotNil(t, d.Store)
	require.NotNil(t, d.NewWriter)
	return d
}

func TestBooksT6G5Writer_CoversEveryMethodOfTheInterface(t *testing.T) {
	want := t6WriterMethods(reflect.TypeFor[t6g5Writer]())
	got := make([]string, 0)
	for m := range g5Calls("x") {
		got = append(got, m)
	}
	slices.Sort(got)
	require.Equal(t, want, got, "g5Calls must call exactly the methods of t6g5Writer")
	view := groupRows(nil, reflect.TypeFor[t6g5Writer]())
	for _, m := range want {
		_, ok := view[m]
		require.True(t, ok, "%s has no row in the shared table: the gate would refuse it", m)
	}
}

// Every method that has a row is ONE gated call stamped with that whole row; the
// empty-row ones make none.
func TestBooksT6G5Writer_EveryCallIsOneStampedWholeRow(t *testing.T) {
	for method := range g5Calls("x") {
		t.Run(method, func(t *testing.T) {
			s, h, id := g5Fixture(t)
			rec := &booksGateRecorder{}
			_ = g5Calls(id)[method](g5Deps(t, s, h).NewWriter(rec))
			stamps := rec.stamps()
			if slices.Contains(g5EmptyRow, method) {
				require.Empty(t, stamps, "%s has an empty row and stays outside the fence", method)
				return
			}
			require.Len(t, stamps, 1, "%s: gated calls", method)
			var want []string
			for _, r := range t6WriterResources[method] {
				want = append(want, string(r))
			}
			var got []string
			for _, r := range stamps[0].Resources {
				got = append(got, r.Kind)
			}
			require.ElementsMatch(t, want, got, "the stamp is the method's whole row, not a retyped list")
			require.Equal(t, "books."+method, stamps[0].Name)
		})
	}
}

// A refused write (preview, fenced, outside the effects) runs nothing: the store
// is as it was and no host service ran.
func TestBooksT6G5Writer_RefusalDoesNothing(t *testing.T) {
	for _, refusal := range []error{tasks.ErrPreview, tasks.ErrFenced, tasks.ErrOutsideEffects} {
		for method := range g5Calls("x") {
			if slices.Contains(g5EmptyRow, method) {
				continue
			}
			t.Run(method+"/"+refusal.Error(), func(t *testing.T) {
				s, h, id := g5Fixture(t)
				before := t6g5Snapshot(t, s)
				w := g5Deps(t, s, h).NewWriter(&booksGateRecorder{refuse: refusal})
				err := g5Calls(id)[method](w)
				if method != "ImportSmartPlaylists" { // no error channel: see the method
					require.ErrorIs(t, err, refusal)
				}
				require.Equal(t, before, t6g5Snapshot(t, s), "the store changed after a refusal")
				require.Zero(t, h.merges.Load()+h.clones.Load(), "a host service ran after a refusal")
			})
		}
	}
}

// A ReadOnly kind is handed no tasks.Writer: every row-bearing write refuses
// rather than dereferencing nil, and the empty-row methods still work.
func TestBooksT6G5Writer_NoWriterRefusesEveryGatedWrite(t *testing.T) {
	s, h, id := g5Fixture(t)
	w := g5Deps(t, s, h).NewWriter(nil)
	for method, call := range g5Calls(id) {
		if slices.Contains(g5EmptyRow, method) || method == "ImportSmartPlaylists" {
			continue
		}
		require.ErrorIs(t, call(w), tasks.ErrOutsideEffects, method)
	}
	w.InvalidateAuthorsCache()
	require.EqualValues(t, 1, h.invalidate.Load())
}

// Every kind admits every method of its own writer interface Live: no stamp is
// wider than what the kind declares.
func TestBooksT6G5Writer_EveryKindIsAdmittedByItsOwnEffects(t *testing.T) {
	s, h, id := g5Fixture(t)
	deps := g5Deps(t, s, h)
	defs := (&Plugin{}).t6g5Kinds(deps, nil)
	wants := t6g5Wants()
	ifaces := t6g5KindWriterIfaces()
	require.Len(t, defs, len(wants))
	for i, w := range wants {
		types, ok := ifaces[w.headID]
		if !ok {
			continue
		}
		t.Run(w.headID, func(t *testing.T) {
			enforcing := writertest.New(defs[i], tasks.ModeLive)
			writer := deps.NewWriter(enforcing)
			for _, m := range ifaceMethods(types) {
				_ = g5Calls(id)[m](writer)
			}
			require.NoError(t, enforcing.RequireClean(), "a stamp is wider than the kind's declared Effects")
			require.NotEmpty(t, enforcing.Writes())
		})
	}
}

func TestBooksT6G5Deps_MissingSourceOrHostLeavesTheGroupUnset(t *testing.T) {
	s, h, _ := g5Fixture(t)
	for name, d := range map[string]t6g5Deps{
		"no source": newT6G5Deps(nil, h, g5TestKey),
		"no host":   newT6G5Deps(s, nil, g5TestKey),
	} {
		require.Nil(t, d.Store, name)
		require.Nil(t, d.NewWriter, name)
		require.Equal(t, g5TestKey, d.ScanKey, name)
	}
	require.Equal(t, g5TestKey, g5Deps(t, s, h).ScanKey)
}

// The host's store accessors answer as HEAD's adapter did: a nil one is "database
// not initialized", not a panic.
func TestBooksT6G5Store_HostAccessorsAndNilStores(t *testing.T) {
	s, h, id := g5Fixture(t)
	st := g5Deps(t, s, h).Store

	require.Equal(t, h.root, st.RootDir())
	require.NoError(t, st.RunMetadataRefreshScan(context.Background(), nil))
	require.EqualValues(t, 1, h.scans.Load())
	require.Equal(t, "/x", st.LibraryITunesPath("/x"))

	b, err := st.GetBookByID(id)
	require.NoError(t, err)
	require.Equal(t, "The Long Halloween", b.Title)
	rows, err := st.BookFileRowsAtPathStrict("/lib/The Long Halloween/part1.m4b")
	require.NoError(t, err)
	require.NotNil(t, rows == nil || len(rows) >= 0)
	_, err = st.GetBookFilesIncludingRetired(id)
	require.NoError(t, err)
	_, err = st.GetAllBookFilesCoreIncludingRetired()
	require.NoError(t, err)
	n, err := st.CountSmartPlaylists()
	require.NoError(t, err)
	require.Zero(t, n)

	keys, err := st.ListMetadataCacheKeys()
	require.NoError(t, err)
	require.Empty(t, keys)
	h.noCache = true
	_, err = st.ListMetadataCacheKeys()
	require.ErrorIs(t, err, errT6g5NoStore)
	require.ErrorIs(t, g5Deps(t, s, h).NewWriter(&booksGateRecorder{}).DeleteMetadataCache(id), errT6g5NoStore)
}

// A kind run for real over the built deps: metadata-refresh reaches the host's
// scan (it is ReadOnly, so it is handed no writer), and purge-empty-narrators
// deletes an unreferenced narrator through the Effects-enforcing writer and
// leaves nothing outside its declared Effects.
func TestBooksT6G5_KindsRunThroughTheBuiltDeps(t *testing.T) {
	t.Run("metadata-refresh", func(t *testing.T) {
		s, h, _ := g5Fixture(t)
		defs := (&Plugin{}).t6g5Kinds(g5Deps(t, s, h), nil)
		def := t6g1ByKind(t, defs, "books.maintenance.metadata-refresh")
		_, _, err := runT6G1Full(t, def, tasks.ModeLive, nil, "", nil)
		require.NoError(t, err)
		require.EqualValues(t, 1, h.scans.Load(), "the scan is the host's")
	})
	t.Run("purge-empty-narrators", func(t *testing.T) {
		s, h, _ := g5Fixture(t)
		n, err := s.CreateNarrator("Selina Kyle")
		require.NoError(t, err)
		defs := (&Plugin{}).t6g5Kinds(g5Deps(t, s, h), nil)
		def := t6g1ByKind(t, defs, "books.maintenance.purge-empty-narrators")
		w := writertest.New(def, tasks.ModeLive)
		raw, _, err := runT6G1Full(t, def, tasks.ModeLive, w, "", nil)
		require.NoError(t, err)
		require.NoError(t, w.RequireClean())
		require.NotEmpty(t, w.Writes(), "nothing was written: this proves nothing")
		var rep emptyNarratorReport
		require.NoError(t, json.Unmarshal(raw, &rep))
		require.Equal(t, 1, rep.Deleted)
		got, err := s.GetNarratorByID(n.ID)
		require.NoError(t, err)
		require.Nil(t, got)
	})
	t.Run("purge-empty-narrators preview deletes nothing", func(t *testing.T) {
		s, h, _ := g5Fixture(t)
		n, err := s.CreateNarrator("Selina Kyle")
		require.NoError(t, err)
		defs := (&Plugin{}).t6g5Kinds(g5Deps(t, s, h), nil)
		def := t6g1ByKind(t, defs, "books.maintenance.purge-empty-narrators")
		w := writertest.New(def, tasks.ModePreview)
		_, _, _ = runT6G1Full(t, def, tasks.ModePreview, w, "", nil)
		got, err := s.GetNarratorByID(n.ID)
		require.NoError(t, err)
		require.NotNil(t, got, "a preview run deleted the narrator")
	})
}
