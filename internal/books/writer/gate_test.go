// file: internal/books/writer/gate_test.go
// version: 1.1.0
// guid: 0d6e48b2-7a95-4c13-b8f0-52e1c9a7d340
// last-edited: 2026-11-19

package writer_test

import (
	"context"
	"errors"
	"slices"
	"sort"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/books/writer"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/writertest"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// allRows is every row the gated writers read: the shared table, metafetch's
// own rows and the qualified raw rows.
func allRows(t testing.TB) writer.Rows {
	t.Helper()
	rows, err := writer.Merge(writer.SharedRows(), writer.MetafetchApplyRows(), writer.RawRows())
	require.NoError(t, err)
	return rows
}

func refsOf(row []ops.Resource) []entity.Ref {
	out := make([]entity.Ref, 0, len(row))
	for _, r := range row {
		out = append(out, tasks.Res(writer.Module, string(r)))
	}
	return out
}

func sortedNames(row []ops.Resource) []string {
	out := make([]string, len(row))
	for i, r := range row {
		out[i] = string(r)
	}
	sort.Strings(out)
	return slices.Compact(out)
}

var subject = tasks.ResID(writer.Module, string(ops.ResBooks), "bk-1")

// TestGate_EveryRowIsFencedOnEveryResource (F5-18b O17, proves 1): for EVERY
// row of the stack's method-to-resource tables, a kind that declares the whole
// row is admitted, and a kind that declares the row minus any one resource is
// refused with ErrOutsideEffects naming that resource, with fn not called and
// nothing recorded as written. One Do per call: an admitted call is exactly
// one write carrying the whole row.
func TestGate_EveryRowIsFencedOnEveryResource(t *testing.T) {
	rows := allRows(t)
	checked, withResources := 0, 0
	for method, row := range rows {
		if len(row) == 0 {
			continue
		}
		withResources++
		t.Run(method, func(t *testing.T) {
			full := refsOf(row)
			// The whole row declared: one admitted write stamped with all of it.
			w := writertest.ForSet("books.k", full, tasks.ModeLive)
			called := 0
			err := writer.New(w, rows).Do(t.Context(), method, subject, func(context.Context) ([]tasks.Change, error) { called++; return nil, nil })
			require.NoError(t, err)
			require.Equal(t, 1, called)
			require.Len(t, w.Writes(), 1, "one store call is one Do, never one per resource")
			var stamped []string
			for _, s := range w.Writes()[0].Stamps() {
				require.Equal(t, writer.Module, s.Module)
				stamped = append(stamped, s.Kind)
			}
			require.ElementsMatch(t, sortedNames(row), stamped, "the write is stamped with the whole row")
			// The row minus each resource in turn.
			for _, missing := range sortedNames(row) {
				var declared []entity.Ref
				for _, r := range full {
					if r.Kind != missing {
						declared = append(declared, r)
					}
				}
				w := writertest.ForSet("books.k", declared, tasks.ModeLive)
				called := 0
				err := writer.New(w, rows).Do(t.Context(), method, subject, func(context.Context) ([]tasks.Change, error) { called++; return nil, nil })
				require.ErrorIs(t, err, tasks.ErrOutsideEffects, "%s declared without %s", method, missing)
				require.Contains(t, err.Error(), "books/"+missing)
				require.Zero(t, called, "a refused write never reaches fn")
				require.Empty(t, w.Writes())
				checked++
			}
		})
	}
	require.Greater(t, withResources, 100, "the tables lost rows: %d methods with resources", withResources)
	require.Greater(t, checked, 200)
}

// A method with no row is refused (fail closed), as both ErrNoRow and
// ErrOutsideEffects; a method whose row is empty writes nothing itself and runs
// fn with no fence; a Preview records the intent and never calls fn.
func TestGate_UnknownEmptyAndPreview(t *testing.T) {
	rows := writer.Rows{"Known": {ops.ResBooks}, "Quiet": {}}
	t.Run("unknown method", func(t *testing.T) {
		w := writertest.ForSet("books.k", refsOf([]ops.Resource{ops.ResBooks}), tasks.ModeLive)
		called := false
		err := writer.New(w, rows).Do(t.Context(), "Mystery", subject, func(context.Context) ([]tasks.Change, error) { called = true; return nil, nil })
		require.ErrorIs(t, err, writer.ErrNoRow)
		require.ErrorIs(t, err, tasks.ErrOutsideEffects)
		require.False(t, called)
		require.Empty(t, w.Writes())
	})
	t.Run("empty row passes through unfenced", func(t *testing.T) {
		w := writertest.ForSet("books.k", nil, tasks.ModeLive) // declares nothing
		called := false
		err := writer.New(w, rows).Do(t.Context(), "Quiet", subject, func(context.Context) ([]tasks.Change, error) { called = true; return nil, nil })
		require.NoError(t, err)
		require.True(t, called)
		require.Empty(t, w.Writes())
		require.Empty(t, w.Refusals())
	})
	t.Run("empty row returns fn's error", func(t *testing.T) {
		boom := errors.New("boom")
		err := writer.New(writertest.ForSet("books.k", nil, tasks.ModeLive), rows).Do(t.Context(), "Quiet", subject, func(context.Context) ([]tasks.Change, error) { return nil, boom })
		require.ErrorIs(t, err, boom)
	})
	t.Run("preview records and does not call fn", func(t *testing.T) {
		w := writertest.ForSet("books.k", refsOf([]ops.Resource{ops.ResBooks}), tasks.ModePreview)
		called := false
		err := writer.New(w, rows).Do(t.Context(), "Known", subject, func(context.Context) ([]tasks.Change, error) { called = true; return nil, nil })
		require.ErrorIs(t, err, tasks.ErrPreview)
		require.False(t, called)
		require.Len(t, w.Writes(), 1)
	})
	t.Run("preview still fences", func(t *testing.T) {
		w := writertest.ForSet("books.k", nil, tasks.ModePreview)
		err := writer.New(w, rows).Do(t.Context(), "Known", subject, func(context.Context) ([]tasks.Change, error) { return nil, nil })
		require.ErrorIs(t, err, tasks.ErrOutsideEffects)
	})
}

// The stamp is the row sorted and without repeats, named for the method, so the
// recorded intent does not depend on a row's literal order.
func TestGate_StampIsSortedWholeRowNamedForTheMethod(t *testing.T) {
	g := writer.New(nil, writer.Rows{"M": {ops.ResIndexes, ops.ResBooks, ops.ResIndexes, ops.ResAuthors}})
	w, ok := g.Stamp("M", subject)
	require.True(t, ok)
	require.Equal(t, "books.M", w.Name)
	require.Equal(t, subject, w.Subject)
	require.Zero(t, w.Resource, "the stamp set is Resources; the legacy single field stays unset")
	require.Equal(t, []entity.Ref{tasks.Res("books", "authors"), tasks.Res("books", "books"), tasks.Res("books", "indexes")}, w.Resources)
	_, ok = g.Stamp("Nope", subject)
	require.False(t, ok)
}

// Rows from two tables merge when they agree and fail when a method has two
// different rows; the shared table and metafetch's rows must agree today.
func TestMerge_And_MetafetchApplyRows_AgreeWithTheSharedTable(t *testing.T) {
	_, err := writer.Merge(writer.Rows{"X": {ops.ResBooks}}, writer.Rows{"X": {ops.ResBooks}})
	require.NoError(t, err)
	_, err = writer.Merge(writer.Rows{"X": {ops.ResBooks, ops.ResIndexes}}, writer.Rows{"X": {ops.ResIndexes, ops.ResBooks}})
	require.NoError(t, err, "rows are compared as sets")
	_, err = writer.Merge(writer.Rows{"X": {ops.ResBooks}}, writer.Rows{"X": {ops.ResBooks, ops.ResIndexes}})
	require.Error(t, err)

	shared := writer.SharedRows()
	for m, row := range writer.MetafetchApplyRows() {
		if s, dup := shared[m]; dup {
			require.ElementsMatch(t, s, row, "%s: metafetch's row differs from the shared table's", m)
		}
	}
	for m := range writer.RawRows() {
		require.NotContains(t, shared, m)
	}
	// Merging them all is the production table: it must succeed.
	_, err = writer.Merge(shared, writer.MetafetchApplyRows(), writer.RawRows())
	require.NoError(t, err)
}

// Raw rows are qualified by the key family, with the colon dropped.
func TestRawRows_AreQualifiedByFamily(t *testing.T) {
	require.Equal(t, "SetRaw/metadata_fetch_cache", writer.RawMethod("SetRaw", "metadata_fetch_cache:"))
	rows := writer.RawRows()
	require.Equal(t, []ops.Resource{ops.ResMetadataCache}, rows["DeleteRaw/pcache"])
	require.Equal(t, []ops.Resource{ops.ResBooks}, rows["SetRaw/metadata_rejection"])
	require.Len(t, rows, 3*len(writer.RawFamilies()))
}

// Concurrent calls (a run's worker pool) are safe and each is admitted or
// refused on its own row; run with -race.
func TestGate_ConcurrentCalls(t *testing.T) {
	rows := writer.Rows{"A": {ops.ResBooks, ops.ResIndexes}, "B": {ops.ResSeries}}
	declared := refsOf([]ops.Resource{ops.ResBooks, ops.ResIndexes}) // not series
	w := writertest.ForSet("books.k", declared, tasks.ModeLive)
	g := writer.New(w, rows)
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted, refused := 0, 0
	for i := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := "A"
			if i%2 == 1 {
				m = "B"
			}
			err := g.Do(t.Context(), m, subject, func(context.Context) ([]tasks.Change, error) { return nil, nil })
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && m == "A":
				admitted++
			case errors.Is(err, tasks.ErrOutsideEffects) && m == "B":
				refused++
			default:
				t.Errorf("%s: %v", m, err)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 32, admitted)
	require.Equal(t, 32, refused)
	require.Len(t, w.Writes(), 32)
	require.Len(t, w.Refusals(), 32)
}

// TestMetafetchApplyRows_ModifyBookRulePinned: THE RULE of the shared table is
// that a row lists everything its method CAN write through its whole call
// chain, not what one closure happens to touch. The local rows of the methods
// that are a ModifyBook call say so in their doc comment; the real store cannot
// tell a narrowed row apart (SetLastWrittenAt's closure sets one field, and ModifyBook writes
// the index and cache keys only when the fields they derive from change), so this pins
// the rule itself: each is at least ModifyBook's row, and the two that touch a
// file row add book_files and nothing else.
func TestMetafetchApplyRows_ModifyBookRulePinned(t *testing.T) {
	shared := writer.SharedRows()
	local := writer.MetafetchApplyRows()
	modify := shared["ModifyBook"]
	require.NotEmpty(t, modify)
	for _, m := range []string{"FlagMetadataHashDuplicate", "SetLastWrittenAt"} {
		require.ElementsMatch(t, modify, local[m], "%s is a ModifyBook call", m)
	}
	withFile := append([]ops.Resource{ops.ResBookFiles}, modify...)
	for _, m := range []string{"MarkNeedsRescan", "PatchBookFileFields"} {
		require.ElementsMatch(t, withFile, local[m], "%s is a ModifyBook call and a file-row write", m)
	}
}
