// file: internal/plugins/dedup/deps_books_engine_test.go
// version: 0.1.0
// guid: 7d1e4a92-3b58-4c06-a9f1-2e8c5b0d6f37
// last-edited: 2026-11-28

package dedup

import (
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/books/writer/writertest"
)

func kindSet(defs []tasks.Definition) map[tasks.Kind]bool {
	out := make(map[tasks.Kind]bool, len(defs))
	for _, d := range defs {
		out[d.Kind()] = true
	}
	return out
}

// With no NewEngine the list is the other 20 kinds and none of the 11 engine
// kinds; with one it is all 31. The schedule hook is asked for exactly the kinds
// returned.
func TestBooksTaskLists_EngineKindsFollowNewEngine(t *testing.T) {
	require.Len(t, EngineKinds(), 11)

	f := newBooksFix(t)
	f.in.NewEngine = nil
	defs, err := BooksTaskLists(f.in)
	require.NoError(t, err)
	require.Len(t, defs, 20)
	got := kindSet(defs)
	for _, k := range EngineKinds() {
		require.False(t, got[k], "%s registered with no NewEngine", k)
	}
	require.Len(t, f.sched, 31, "the schedule hook is asked per built kind, before the engine kinds are dropped")

	f = newBooksFix(t)
	defs, err = BooksTaskLists(f.in)
	require.NoError(t, err)
	require.Len(t, defs, 31)
	got = kindSet(defs)
	for _, k := range EngineKinds() {
		require.True(t, got[k], "%s missing with a NewEngine", k)
	}
}

// EngineKinds returns a copy: a caller cannot widen or empty the list.
func TestEngineKinds_IsACopy(t *testing.T) {
	a := EngineKinds()
	a[0] = "books.dedup.not-a-kind"
	require.NotEqual(t, a[0], EngineKinds()[0])
}

// engineKinds is exactly the set of kinds that need the engine factory: with no
// factory each of the 11 refuses at its first run naming it, and none of the 20
// others does. A kind added to (or dropped from) either side fails here.
func TestEngineKinds_AreExactlyTheKindsThatNeedTheFactory(t *testing.T) {
	f := newBooksFix(t)
	f.in.NewEngine = nil
	d := f.deps(t)
	all := (&Plugin{}).Kinds(d, nil)
	require.Len(t, all, 31)
	engine := map[tasks.Kind]bool{}
	for _, k := range EngineKinds() {
		engine[k] = true
	}
	for _, def := range all {
		t.Run(string(def.Kind()), func(t *testing.T) {
			_, err := runKind(t, def, tasks.ModeLive, writertest.New(def, tasks.ModeLive), "")
			needs := err != nil && strings.Contains(err.Error(), "NewEngine is not set")
			require.Equal(t, engine[def.Kind()], needs, "err=%v", err)
		})
	}
}

// A NewEngine that hands back nothing, or a typed nil, is refused by name at the
// run rather than dereferenced.
func TestBooksTaskLists_ANilEngineFromTheFactoryIsRefusedByName(t *testing.T) {
	for name, mk := range map[string]func(tasks.Writer) WriteEngine{
		"nil":       func(tasks.Writer) WriteEngine { return nil },
		"typed nil": func(tasks.Writer) WriteEngine { return (*booksEngine)(nil) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newBooksFix(t)
			f.in.NewEngine = mk
			defs, err := BooksTaskLists(f.in)
			require.NoError(t, err)
			for _, k := range []tasks.Kind{purgeStaleKind, embedScanKind, llmReviewKind} {
				def := byKind(t, defs, k)
				_, err := runKind(t, def, tasks.ModeLive, writertest.New(def, tasks.ModeLive), "")
				require.ErrorContains(t, err, "the engine factory returned no engine", string(k))
			}
		})
	}
}
