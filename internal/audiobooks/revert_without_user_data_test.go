// file: internal/audiobooks/revert_without_user_data_test.go
// version: 1.0.0
// guid: 8c2e6a14-5d07-4b9f-a3e1-7f40d9b2c815
// last-edited: 2026-10-25

package audiobooks

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// WithoutUserData strips the notes that name a user from every free-text list
// and from Summary (built from the combine notes), says so where it dropped
// some, leaves the counts and the input alone, and is nil-safe.
func TestRevertResult_WithoutUserData(t *testing.T) {
	const userNote = "user 01INVENTEDUSER0000000000A progress on a Plimmet title kept at 40%"
	in := &RevertResult{
		OperationID: "op-1", Total: 3, Restored: 3,
		CombinesUndoneInPart: 1, combineKept: []string{userNote},
		PartiallyRestored: 1, Kept: []string{"change c1: " + userNote, "change c2: a lock was left as it is"},
	}
	before := in.Summary()
	require := assert.New(t)

	out := in.WithoutUserData()
	require.NotSame(in, out)
	require.Equal(before, in.Summary(), "the input is not modified")
	require.Equal([]string{"change c1: " + userNote, "change c2: a lock was left as it is"}, in.Kept)

	require.Equal(3, out.Restored)
	require.Equal(1, out.PartiallyRestored)
	require.Equal([]string{"change c2: a lock was left as it is", "1 note(s) that name a user withheld (users.manage shows them)"}, out.Kept)
	require.NotContains(out.Summary(), "01INVENTEDUSER0000000000A")
	require.Contains(out.Summary(), "1 note(s) that name a user withheld", "the combine list is not left empty")
	require.NotContains(out.Summary(), "left as they are: ;")

	clean := &RevertResult{Total: 1, Restored: 1, Kept: []string{"a lock was left as it is"}}
	require.Equal(clean.Kept, clean.WithoutUserData().Kept, "nothing withheld, nothing added")
	var none *RevertResult
	require.Nil(none.WithoutUserData())
}
