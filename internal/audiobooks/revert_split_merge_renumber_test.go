// file: internal/audiobooks/revert_split_merge_renumber_test.go
// version: 1.0.0
// guid: 9c3082b9-b916-4ba1-9f7c-2cd4c0d34e4b
// last-edited: 2026-10-18

package audiobooks

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
)

// A split merge that crashes in step 1 (after a's soft-delete lands) never
// reaches the play-order stamp, so its journal records the survivor's
// numbering as untouched (journal.SurvivorNumbering =
// merge.SurvivorNumberingUntouched, set before the first write). A renumber
// of keep's OWN row made after the crash is the user's, and the revert must
// leave it alone. Without that marker the journal reads "may change" and the
// revert puts the row's old disc/track back over the user's edit.
func TestRevertSplitMerge_CrashInStep1_KeepOwnRenumberSurvives(t *testing.T) {
	store := dbtest.NewStore(t)
	f := seedCombineFixture(t, store)
	opID := "op-p2"
	crashed := false
	func() {
		defer func() { crashed = recover() != nil }()
		_, _ = dedup.MergeSplitBookClusterWithOptions(crashStore{store, func(m, arg string) bool { return m == "ModifyBook" && arg == f.a }, true},
			f.keep, []string{f.a, f.b}, "", dedup.SplitMergeOptions{OperationID: opID, OrderByBooks: true})
	}()
	require.True(t, crashed)
	kf, err := store.GetBookFiles(f.keep)
	require.NoError(t, err)
	var own string
	for _, x := range kf {
		if x.FilePath == "/tmp/revert-paths/keep/01.mp3" {
			own = x.ID
			x.DiscNumber, x.TrackNumber = 5, 99
			require.NoError(t, store.UpdateBookFile(x.ID, &x))
		}
	}
	require.NotEmpty(t, own)
	_, err = NewRevertService(store).RevertOperation(opID)
	require.NoError(t, err)
	got, err := store.GetBookFileByID(f.keep, own)
	require.NoError(t, err)
	require.Equal(t, [2]int{5, 99}, [2]int{got.DiscNumber, got.TrackNumber}, "the user's renumber of keep's own row survives")
}
