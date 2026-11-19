// file: internal/plugins/maintenance/kinds_f5_18b_blindspots_test.go
// version: 1.0.0
// guid: d2a8f419-6c70-4e35-b1a2-0f94e7c3a561
// last-edited: 2026-11-19

package maintenance

import (
	"testing"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

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
// whose row is wider than the tag. ModifyBook writes books, authors, indexes and
// metadata_cache (its shared row); stamped with books alone a kind that
// declares only books is let through.
func TestF518b_OneTagPerWriteAdmitsWhatTheWholeRowRefuses(t *testing.T) {
	declared := []entity.Ref{tasks.Res(writer.Module, string(ops.ResBooks))}
	legacy := tasks.Write{Name: "books.ModifyBook", Resource: declared[0], Subject: tasks.ResID(writer.Module, "books", "bk")}
	require.NoError(t, tasks.CheckWrite("books.f518b", declared, legacy), "the old single-tag stamp admits it")

	s := newRepairPebble(t)
	repairBook(t, s, "bk", nil)
	g, w := f518bGated(t, s, ops.ResBooks)
	_, err := g.ModifyBook("bk", nil)
	require.ErrorIs(t, err, tasks.ErrOutsideEffects)
	for _, missing := range []string{"authors", "indexes", "metadata_cache"} {
		require.Contains(t, err.Error(), "books/"+missing)
	}
	require.Empty(t, w.Writes())
}
