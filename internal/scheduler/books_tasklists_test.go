// file: internal/scheduler/books_tasklists_test.go
// version: 0.1.0
// guid: 17b3f6c9-5e82-4a04-b9d1-c40e7a62f385
// last-edited: 2026-11-22

package scheduler

import (
	"context"
	"reflect"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/books/writer/writertest"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

type countingUpgrader struct{ runs int }

func (u *countingUpgrader) RunUpgrade(context.Context, int, operations.ProgressReporter) (*metabatch.UpgradeResult, error) {
	u.runs++
	return &metabatch.UpgradeResult{}, nil
}

func booksT7Inputs(u MetadataUpgrader) BooksTaskInputs {
	return BooksTaskInputs{
		Upgrader:     u,
		StandDownKey: tasks.Key("books.library.scan"),
		Schedules:    func(tasks.Kind) *tasks.Schedule { return nil },
	}
}

func TestBooksT7Writer_SweepIsOneGatedWriteAdmittedByTheKindsEffects(t *testing.T) {
	kind := t7KindFor(metadataUpgradeOpID)
	for _, mode := range []tasks.Mode{tasks.ModeLive, tasks.ModePreview} {
		t.Run(mode.String(), func(t *testing.T) {
			up := &countingUpgrader{}
			defs, err := BooksTaskLists(booksT7Inputs(up))
			require.NoError(t, err)
			require.Len(t, defs, 1)
			require.Equal(t, kind, defs[0].Kind())

			deps, err := newBooksT7Deps(booksT7Inputs(up))
			require.NoError(t, err)
			enforcing := writertest.New(defs[0], mode)
			res, err := deps.NewWriter(enforcing).RunUpgrade(context.Background(), 10, nil)
			require.NoError(t, enforcing.RequireClean(), "the sweep's stamp is wider than the kind's declared Effects")
			require.Len(t, enforcing.Writes(), 1, "the sweep is one stamped write")
			if mode == tasks.ModeLive {
				require.NoError(t, err)
				require.NotNil(t, res)
				require.Equal(t, 1, up.runs)
			} else {
				require.ErrorIs(t, err, tasks.ErrPreview)
				require.Zero(t, up.runs, "a Preview run swept")
			}
		})
	}
}

// A run with no writer (nil) refuses the sweep instead of dereferencing nil; so
// does a writer fenced to nothing.
func TestBooksT7Writer_NoWriterAndOutsideEffectsRefuse(t *testing.T) {
	up := &countingUpgrader{}
	deps, err := newBooksT7Deps(booksT7Inputs(up))
	require.NoError(t, err)
	_, err = deps.NewWriter(nil).RunUpgrade(context.Background(), 10, nil)
	require.ErrorIs(t, err, tasks.ErrOutsideEffects)
	_, err = deps.NewWriter(writertest.ForSet(t7KindFor(metadataUpgradeOpID), nil, tasks.ModeLive)).RunUpgrade(context.Background(), 10, nil)
	require.ErrorIs(t, err, tasks.ErrOutsideEffects)
	require.Zero(t, up.runs)
}

func TestBooksT7Deps_EveryFieldIsSet(t *testing.T) {
	d, err := newBooksT7Deps(booksT7Inputs(&countingUpgrader{}))
	require.NoError(t, err)
	v := reflect.ValueOf(d)
	for i := 0; i < v.NumField(); i++ {
		require.False(t, v.Field(i).IsZero(), "t7Deps.%s is unset", v.Type().Field(i).Name)
	}
}

func TestBooksTaskLists_MissingInputsRefuse(t *testing.T) {
	full := booksT7Inputs(&countingUpgrader{})
	for name, mutate := range map[string]func(*BooksTaskInputs){
		"Upgrader":     func(in *BooksTaskInputs) { in.Upgrader = nil },
		"StandDownKey": func(in *BooksTaskInputs) { in.StandDownKey = "" },
		"Schedules":    func(in *BooksTaskInputs) { in.Schedules = nil },
	} {
		in := full
		mutate(&in)
		defs, err := BooksTaskLists(in)
		require.ErrorContains(t, err, name)
		require.Nil(t, defs)
	}
	defs, err := BooksTaskLists(full)
	require.NoError(t, err)
	for _, d := range defs {
		require.Empty(t, tasks.Problems(d), d.Kind())
	}
}
