// file: internal/plugins/maintenance/kinds_f5_18b_blindspots_test.go
// version: 1.2.0
// guid: d2a8f419-6c70-4e35-b1a2-0f94e7c3a561
// last-edited: 2026-11-19

package maintenance

import (
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/metafetchgate"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/writertest"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The F5-10 store-key diff (t6g5DiffStore) proves a write HAPPENED by comparing
// the store's keys before and after. It has three blind spots, each a write the
// diff cannot see and so cannot check against a kind's Effects (F5-18b O27).
// The gate stamps the resource of every write at the call, so each is refused
// when it is attempted. Each test shows both halves on a real store: the diff
// is empty, and the gate refuses.

// f518bGated is a metafetch.Store over s whose writes are fenced against the
// declared resources (books module), with the writer to read back.
func f518bGated(t *testing.T, s metafetch.Store, declared ...ops.Resource) (*metafetchgate.Store, *writertest.Writer) {
	t.Helper()
	var set []entity.Ref
	for _, r := range declared {
		set = append(set, tasks.Res(writer.Module, string(r)))
	}
	w := writertest.ForSet("books.f518b", set, tasks.ModeLive)
	rows, err := metafetchgate.Rows()
	require.NoError(t, err)
	return metafetchgate.Wrap(t.Context(), s, writer.New(w, rows)), w
}

const f518bCacheKey = "metadata_fetch_cache:f518b-key"

// Blind spot 1: a write that leaves a key's bytes as they were.
func TestF518b_BlindSpot_WriteThatChangesNoKey(t *testing.T) {
	s := newRepairPebble(t)
	require.NoError(t, s.SetRaw(f518bCacheKey, []byte("same")))

	before := t6g5Snapshot(t, s)
	require.NoError(t, s.SetRaw(f518bCacheKey, []byte("same"))) // the ungated write
	require.Zero(t, t6g5DiffStore(before, t6g5Snapshot(t, s)).changed, "the key diff sees nothing: it cannot be what catches this")

	g, w := f518bGated(t, s, ops.ResBooks) // declares no metadata_cache
	err := g.SetRaw(f518bCacheKey, []byte("same"))
	require.ErrorIs(t, err, tasks.ErrOutsideEffects)
	require.Contains(t, err.Error(), "books/metadata_cache")
	require.Empty(t, w.Writes())
	v, gerr := s.GetRaw(f518bCacheKey)
	require.NoError(t, gerr)
	require.Equal(t, "same", string(v))

	g, w = f518bGated(t, s, ops.ResMetadataCache)
	require.NoError(t, g.SetRaw(f518bCacheKey, []byte("same")))
	require.Equal(t, map[string]bool{"books/metadata_cache": true}, w.Stamped(), "declared, the same write is admitted and recorded")
}

// Blind spot 2: a delete of a key that is already absent.
func TestF518b_BlindSpot_DeleteOfAnAbsentKey(t *testing.T) {
	s := newRepairPebble(t)
	before := t6g5Snapshot(t, s)
	require.NoError(t, s.DeleteMetadataCache("bk-absent")) // the ungated write
	require.Zero(t, t6g5DiffStore(before, t6g5Snapshot(t, s)).changed, "deleting nothing changes no key")

	g, w := f518bGated(t, s, ops.ResBooks)
	err := g.DeleteMetadataCache("bk-absent")
	require.ErrorIs(t, err, tasks.ErrOutsideEffects)
	require.Contains(t, err.Error(), "books/metadata_cache")
	require.Empty(t, w.Writes())

	g, w = f518bGated(t, s, ops.ResMetadataCache, ops.ResIndexes)
	require.NoError(t, g.DeleteMetadataCache("bk-absent"))
	require.Equal(t, map[string]bool{"books/metadata_cache": true, "books/indexes": true}, w.Stamped())
}

// Blind spot 3: a key created and deleted inside one diffed window.
func TestF518b_BlindSpot_KeyCreatedAndDeletedInOneWindow(t *testing.T) {
	s := newRepairPebble(t)
	before := t6g5Snapshot(t, s)
	require.NoError(t, s.SetRaw(f518bCacheKey, []byte("scratch"))) // the ungated writes
	require.NoError(t, s.DeleteRaw(f518bCacheKey))
	require.Zero(t, t6g5DiffStore(before, t6g5Snapshot(t, s)).changed, "the window shows no key changed")

	g, w := f518bGated(t, s, ops.ResBooks)
	err := g.SetRaw(f518bCacheKey, []byte("scratch"))
	require.ErrorIs(t, err, tasks.ErrOutsideEffects)
	require.Empty(t, w.Writes())
	after := t6g5Snapshot(t, s)
	require.Equal(t, before, after, "a refused write never reached the store")

	g, w = f518bGated(t, s, ops.ResMetadataCache)
	before = t6g5Snapshot(t, s)
	require.NoError(t, g.SetRaw(f518bCacheKey, []byte("scratch")))
	require.NoError(t, g.DeleteRaw(f518bCacheKey))
	require.Zero(t, t6g5DiffStore(before, t6g5Snapshot(t, s)).changed)
	require.Len(t, w.Writes(), 2, "both writes are recorded though the diff sees neither")
	require.Equal(t, map[string]bool{"books/metadata_cache": true}, w.Stamped())
}

// The single-resource stamp the gate replaces: one tag per write admits a call
// whose row is wider than the tag. ModifyBook writes books, indexes and
// metadata_cache (its shared row; F5-18j narrowed it, authors left it when the
// store stopped staging author_books_legacy rows); stamped with books alone a
// kind that declares only books is let through.
func TestF518b_OneTagPerWriteAdmitsWhatTheWholeRowRefuses(t *testing.T) {
	declared := []entity.Ref{tasks.Res(writer.Module, string(ops.ResBooks))}
	legacy := tasks.Write{Name: "books.ModifyBook", Resource: declared[0], Subject: tasks.ResID(writer.Module, "books", "bk")}
	require.NoError(t, tasks.CheckWrite("books.f518b", declared, legacy), "the old single-tag stamp admits it")

	s := newRepairPebble(t)
	repairBook(t, s, "bk", nil)
	g, w := f518bGated(t, s, ops.ResBooks)
	_, err := g.ModifyBook("bk", nil)
	require.ErrorIs(t, err, tasks.ErrOutsideEffects)
	for _, missing := range []string{"indexes", "metadata_cache"} {
		require.Contains(t, err.Error(), "books/"+missing)
	}
	require.NotContains(t, err.Error(), "books/authors", "F5-18j took authors out of the ModifyBook row")
	require.Empty(t, w.Writes())
}

// TestF518b_LocalRowsCoverWhatTheStoreWrites: THE RULE of the shared table is
// that a row lists everything the method CAN write. The rows this PR keeps
// beside the table (writer.MetafetchApplyRows) are traced by hand, so each is
// checked against the real store: call the method directly, diff the store's
// keys, and require every resource that changed to be in the row.
func TestF518b_LocalRowsCoverWhatTheStoreWrites(t *testing.T) {
	now := time.Now()
	type call struct {
		method string
		setup  func(t *testing.T, s *database.PebbleStore)
		do     func(s *database.PebbleStore) error
		// mustWrite names resources the call has to change (a row that
		// over-claims is allowed, one that under-claims is not).
		mustWrite []string
	}
	tag := func(s *database.PebbleStore) error { return s.AddBookTagWithSource("bk", "mood:dark", "system") }
	fieldState := func(s *database.PebbleStore) error {
		v := `"Year One"`
		return s.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: "bk", Field: "title", FetchedValue: &v, UpdatedAt: now})
	}
	// withFile gives the book one file row at the book's own path (what
	// repairBook sets), the shape MarkNeedsRescan mirrors its flag onto.
	withFile := func(t *testing.T, s *database.PebbleStore) {
		require.NoError(t, s.CreateBookFile(&database.BookFile{ID: "bf1", BookID: "bk", FilePath: "/lib/bk.m4b", FileSize: 1000}))
	}
	// withStaleAggregate is withFile with the book's size aggregate knocked out
	// of step with its file, so the patch's recompute has a book write to make.
	withStaleAggregate := func(t *testing.T, s *database.PebbleStore) {
		withFile(t, s)
		_, err := s.ModifyBook("bk", func(b *database.Book) error { b.FileSize = nil; return nil })
		require.NoError(t, err)
	}
	cases := []call{
		{"AddBookTagWithSource", nil, tag, []string{"books"}},
		{"RemoveBookTagsByPrefix", func(t *testing.T, s *database.PebbleStore) { require.NoError(t, tag(s)) },
			func(s *database.PebbleStore) error { return s.RemoveBookTagsByPrefix("bk", "mood:", "system") }, []string{"books"}},
		{"CreateSeries", nil, func(s *database.PebbleStore) error { _, err := s.CreateSeries("The Long Halloween", nil); return err }, []string{"series"}},
		{"UpsertMetadataFieldState", nil, fieldState, []string{"field_states"}},
		{"DeleteMetadataFieldState", func(t *testing.T, s *database.PebbleStore) { require.NoError(t, fieldState(s)) },
			func(s *database.PebbleStore) error { return s.DeleteMetadataFieldState("bk", "title") }, []string{"field_states"}},
		{"RecordMetadataChange", nil, func(s *database.PebbleStore) error {
			return s.RecordMetadataChange(&database.MetadataChangeRecord{BookID: "bk", Field: "title", ChangeType: "fetched", ChangedAt: now})
		}, []string{"books"}},
		{"PutMetadataCache", nil, func(s *database.PebbleStore) error {
			return s.PutMetadataCache(&database.MetadataCandidateCache{BookID: "bk", FetchedAt: now, SourceHash: "h"})
		}, []string{"metadata_cache"}},
		{"RecordPathChange", nil, func(s *database.PebbleStore) error {
			return s.RecordPathChange(&database.BookPathChange{BookID: "bk", OldPath: "/lib/a", NewPath: "/lib/b", ChangeType: "rename", CreatedAt: now})
		}, []string{"books"}},
		{"FlagMetadataHashDuplicate", func(t *testing.T, s *database.PebbleStore) { repairBook(t, s, "bk2", nil) },
			func(s *database.PebbleStore) error { return s.FlagMetadataHashDuplicate("bk", "bk2") }, nil},
		{"SetLastWrittenAt", nil, func(s *database.PebbleStore) error { return s.SetLastWrittenAt("bk", now) }, []string{"books"}},
		{"MarkNeedsRescan", withFile, func(s *database.PebbleStore) error { return s.MarkNeedsRescan("bk") }, []string{"books", "book_files"}},
		{"PatchBookFileFields", withStaleAggregate, func(s *database.PebbleStore) error {
			n := 3
			_, _, err := s.PatchBookFileFields("bk", "bf1", database.BookFileFieldPatch{TrackNumber: &n})
			return err
		}, []string{"book_files", "books"}},
		{"SetUserPreference", nil, func(s *database.PebbleStore) error { return s.SetUserPreference("f518b_pref", "v") }, []string{"user_state"}},
		{"DeleteUserPreference", func(t *testing.T, s *database.PebbleStore) {
			require.NoError(t, s.SetUserPreference("f518b_pref", "v"))
		},
			func(s *database.PebbleStore) error { return s.DeleteUserPreference("f518b_pref") }, []string{"user_state"}},
	}
	local := writer.MetafetchApplyRows()
	require.Len(t, cases, len(local), "every local row has a case: add the new row's method here")
	for _, c := range cases {
		t.Run(c.method, func(t *testing.T) {
			row, ok := local[c.method]
			require.True(t, ok, "no local row %s", c.method)
			s := newRepairPebble(t)
			repairBook(t, s, "bk", nil)
			if c.setup != nil {
				c.setup(t, s)
			}
			before := t6g5Snapshot(t, s)
			require.NoError(t, c.do(s))
			changed := f518bChangedResources(t, before, t6g5Snapshot(t, s))
			inRow := map[string]bool{}
			for _, r := range row {
				inRow[string(r)] = true
			}
			for r, key := range changed {
				require.True(t, inRow[r], "%s changed %s (key %s) but its row %v does not list it", c.method, r, key, row)
			}
			for _, r := range c.mustWrite {
				require.Contains(t, changed, r, "%s was expected to write %s: the case does not exercise the row", c.method, r)
			}
		})
	}
}

// TestF518b_LegacyStaleSeriesRowWriteIsCoveredByTheBookRow (round 2, M1): a
// book write over a legacy stale-series row (Series set, SeriesID nil: what
// older builds left behind) drops the stored object and stages a
// metadata_change: history row in the book's own batch
// (stageSeriesObjectDrop). The row of every method that reaches ModifyBook has
// to carry that write. metadata_change: is book-keyed history, so it is
// `books`, like path_history:; the test shows the row was staged and that the
// row of the call covers everything that changed.
func TestF518b_LegacyStaleSeriesRowWriteIsCoveredByTheBookRow(t *testing.T) {
	now := time.Now()
	shared := writer.SharedRows()
	local := writer.MetafetchApplyRows()
	for name, c := range map[string]struct {
		row []ops.Resource
		do  func(s *database.PebbleStore) error
	}{
		"ModifyBook (shared row)": {shared["ModifyBook"], func(s *database.PebbleStore) error {
			_, err := s.ModifyBook("bk", func(b *database.Book) error { b.Title = "Hush"; return nil })
			return err
		}},
		"SetLastWrittenAt": {local["SetLastWrittenAt"], func(s *database.PebbleStore) error { return s.SetLastWrittenAt("bk", now) }},
	} {
		t.Run(name, func(t *testing.T) {
			s := newRepairPebble(t)
			repairBook(t, s, "bk", nil)
			_, err := s.SeedLegacyBookRowForTest("bk", func(b *database.Book) error {
				b.Series = &database.Series{ID: 7, Name: "Hush"}
				b.SeriesID = nil
				return nil
			})
			require.NoError(t, err)
			before := t6g5Snapshot(t, s)
			require.NoError(t, c.do(s))
			after := t6g5Snapshot(t, s)

			var staged bool
			for k := range after {
				if _, had := before[k]; !had && strings.HasPrefix(k, "metadata_change:bk:") {
					staged = true
				}
			}
			require.True(t, staged, "the write did not stage the series-object-drop history row: the case does not exercise the row")

			inRow := map[string]bool{}
			for _, r := range c.row {
				inRow[string(r)] = true
			}
			for r, key := range f518bChangedResources(t, before, after) {
				require.True(t, inRow[r], "%s changed %s (key %s) over a legacy stale-series row but its row %v does not list it", name, r, key, c.row)
			}
		})
	}
}
