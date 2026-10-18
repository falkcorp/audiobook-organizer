// file: internal/audiobooks/revert_combine_undo_crash_test.go
// version: 1.0.0
// guid: 63945e51-9b1a-4083-92cc-94923d03df26
// last-edited: 2026-10-18

package audiobooks

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// A revert of an APPLIED combine that crashes part-way must be finishable: a
// re-run of the same revert puts every row, file and listening position back.
// Before the fix the journal stayed Applied over a half-undone library (the
// undo wrote undo_failed only on an error return, never on a crash), so every
// re-run refused on the rows the dead attempt had already put back.
//
// The revert's writes are traced once, then the revert is crashed before and
// after each of them in turn (UpdateBook included: applyUndo step 1 releases
// its group stripes with a defer, so a crash there cannot leak them and wedge
// the re-run).
func TestRevertCombine_CrashMidUndoThenRetry(t *testing.T) {
	override := &merge.CombineOverride{Title: "OVERRIDDEN TITLE", Author: "Jane Example"}
	type seeded struct {
		opID   string
		f      crashFixture
		before crashSnap
		meta   keepMeta
		store  *database.PebbleStore
	}
	seed := func(t *testing.T) seeded {
		store := dbtest.NewStore(t)
		f := seedCrashFixture(t, store)
		seedPriorTitleLock(t, store, f.keep)
		seedPriorAuthor(t, store, f.keep)
		before := snapCrash(t, store, f)
		meta := metaOf(t, store, f.keep)
		opID := "op-" + ulid.Make().String()
		_, err := merge.NewService(store).CombineBooksWithOptions([]string{f.keep, f.a, f.b}, f.keep, override, merge.CombineOptions{OperationID: opID})
		require.NoError(t, err)
		return seeded{opID: opID, f: f, before: before, meta: meta, store: store}
	}

	var writes []string
	{
		s := seed(t)
		_, err := NewRevertService(crashStore{s.store, func(m, arg string) bool { writes = append(writes, m+" "+arg); return false }, false}).RevertOperation(s.opID)
		require.NoError(t, err)
	}
	require.NotEmpty(t, writes)
	sawUpdateBook := false
	for _, w := range writes {
		sawUpdateBook = sawUpdateBook || strings.HasPrefix(w, "UpdateBook ")
	}
	require.True(t, sawUpdateBook, "the revert restores the absorbed rows with UpdateBook; the trace must include it")

	crashed := 0
	for i := 1; i <= len(writes); i++ {
		for _, post := range []bool{false, true} {
			name := fmt.Sprintf("%02d %s %s", i, strings.Fields(writes[i-1])[0], map[bool]string{false: "before", true: "after"}[post])
			t.Run(name, func(t *testing.T) {
				s := seed(t)
				n := 0
				didCrash := false
				func() {
					defer func() { didCrash = recover() != nil }()
					_, _ = NewRevertService(crashStore{s.store, func(string, string) bool { n++; return n == i }, post}).RevertOperation(s.opID)
				}()
				if !didCrash {
					t.Skip("crash point not reached in this run")
				}
				crashed++
				_, err := NewRevertService(s.store).RevertOperation(s.opID)
				require.NoError(t, err, "re-run after a crash at %s", writes[i-1])
				after := snapCrash(t, s.store, s.f)
				require.Equal(t, fmt.Sprint(s.before.Rows), fmt.Sprint(after.Rows), "book rows back")
				require.Equal(t, s.before.AProgress, after.AProgress, "listening progress back")
				for path, owner := range after.Owner {
					require.Equal(t, s.f.paths[path], owner, "file %s on its owner", path)
				}
				for path := range s.before.Owner {
					require.NotEmpty(t, after.Owner[path], "file %s still has a row", path)
				}
				if m := metaOf(t, s.store, s.f.keep); !reflect.DeepEqual(m, s.meta) {
					t.Errorf("metadata not back: before=%+v after=%+v", s.meta, m)
				}
			})
		}
	}
	require.Positive(t, crashed, "no crash point was reached")
}
