// file: internal/repairs/books_tasklists_test.go
// version: 0.1.0
// guid: 3e9a6d41-8c27-4f5b-b0d3-7a1c52e8f964
// last-edited: 2026-11-22

package repairs

import (
	"context"
	"reflect"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
)

// booksStores is a WriterStores over the package's in-memory store: the
// capabilities the memory store does not carry come from their own nil-able
// embeds, which the tests never call.
type booksStores struct {
	*memStore
	BookFileWriter
	ChangeJournal
	CreditStore
	FieldStateStore
}

func booksInputs(t *testing.T) BooksTaskInputs {
	t.Helper()
	s := newMemStore()
	return BooksTaskInputs{
		Fixers:       []Fixer{&trimFixer{s: s}},
		Guard:        s,
		Series:       seriesNamer,
		Stores:       booksStores{memStore: s},
		PlanRows:     &fakePlanRows{rows: map[string]tasks.Row{}},
		BindStores:   func(ctx context.Context) context.Context { return WithStores(ctx, "shell") },
		Scopes:       testScopes,
		StandDownKey: "books.gotham-watch",
		Concurrency:  2,
	}
}

func TestBooksTaskLists_OneKindPerFixer(t *testing.T) {
	in := booksInputs(t)
	defs, err := BooksTaskLists(in)
	require.NoError(t, err)
	require.Len(t, defs, len(in.Fixers))
	for i, d := range defs {
		require.Equal(t, tasks.Kind(KindPrefix+in.Fixers[i].ID()), d.Kind())
		require.Empty(t, tasks.Problems(d))
	}
}

func TestBooksTaskLists_MissingInputsRefuse(t *testing.T) {
	for name, mutate := range map[string]func(*BooksTaskInputs){
		"Fixers":       func(in *BooksTaskInputs) { in.Fixers = nil },
		"Guard":        func(in *BooksTaskInputs) { in.Guard = nil },
		"Stores":       func(in *BooksTaskInputs) { in.Stores = nil },
		"PlanRows":     func(in *BooksTaskInputs) { in.PlanRows = nil },
		"BindStores":   func(in *BooksTaskInputs) { in.BindStores = nil },
		"Scopes":       func(in *BooksTaskInputs) { in.Scopes = nil },
		"StandDownKey": func(in *BooksTaskInputs) { in.StandDownKey = "" },
	} {
		t.Run(name, func(t *testing.T) {
			in := booksInputs(t)
			mutate(&in)
			defs, err := BooksTaskLists(in)
			require.ErrorContains(t, err, name)
			require.Nil(t, defs)
		})
	}
}

// A fixer whose permission atom books' table does not know has no Start
// requirement; registering it would leave a kind nobody can start.
func TestBooksTaskLists_UnresolvedAtomRefuses(t *testing.T) {
	in := booksInputs(t)
	in.Scopes = func(auth.Permission) ([]rbac.Permission, bool) { return nil, false }
	defs, err := BooksTaskLists(in)
	require.Error(t, err)
	require.ErrorContains(t, err, in.Fixers[0].ID())
	require.Nil(t, defs)
}

// Each run's Writer journals under the run's id, so the run's revert undoes the
// apply; the optional capabilities are wired only when the caller has them.
func TestBooksWriter_JournalsUnderTheRunID(t *testing.T) {
	in := booksInputs(t)
	mk := newBooksWriter(in)
	w, err := mk(context.Background(), tasks.RunID("run-robin"), in.Fixers[0])
	require.NoError(t, err)
	require.Equal(t, "run-robin", w.OpID())
	other, err := mk(context.Background(), tasks.RunID("run-nightwing"), in.Fixers[0])
	require.NoError(t, err)
	require.Equal(t, "run-nightwing", other.OpID(), "one writer per run")
	require.NotSame(t, w, other)
}

func TestBooksTaskInputs_EveryRequiredFieldIsCoveredByTheRefusal(t *testing.T) {
	// A field added to BooksTaskInputs must be classified: required (and refused
	// when missing, above) or optional (listed here).
	optional := map[string]bool{"Tags": true, "Series": true, "TagWriter": true, "UserState": true, "Concurrency": true}
	required := map[string]bool{
		"Fixers": true, "Guard": true, "Stores": true, "PlanRows": true,
		"BindStores": true, "Scopes": true, "StandDownKey": true,
	}
	ty := reflect.TypeFor[BooksTaskInputs]()
	for i := 0; i < ty.NumField(); i++ {
		n := ty.Field(i).Name
		require.True(t, optional[n] || required[n], "BooksTaskInputs.%s is neither required nor optional here", n)
	}
}
