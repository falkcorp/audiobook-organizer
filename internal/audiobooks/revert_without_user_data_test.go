// file: internal/audiobooks/revert_without_user_data_test.go
// version: 1.1.0
// guid: 8c2e6a14-5d07-4b9f-a3e1-7f40d9b2c815
// last-edited: 2026-10-30

package audiobooks

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WithoutUserData strips the notes that name a user from every free-text list
// and from Summary (built from the combine notes), says so where it dropped
// some, leaves the counts and the input alone, and is nil-safe.
func TestRevertResult_WithoutUserData(t *testing.T) {
	const userID = "01INVENTEDUSER0000000000A"
	const userNote = "user " + userID + " progress on The Long Halloween kept at 40%"
	named := RevertNote{Text: "change c1: " + userNote, UserIDs: []string{userID}}
	plain := RevertNote{Text: "change c2: a lock was left as it is"}
	in := &RevertResult{
		OperationID: "op-1", Total: 3, Restored: 3,
		CombinesUndoneInPart: 1, combineKept: []RevertNote{{Text: userNote, UserIDs: []string{userID}}},
		PartiallyRestored: 1, Kept: []RevertNote{named, plain},
	}
	before := in.Summary()
	require := assert.New(t)

	out := in.WithoutUserData()
	require.NotSame(in, out)
	require.Equal(before, in.Summary(), "the input is not modified")
	require.Equal([]RevertNote{named, plain}, in.Kept)

	require.Equal(3, out.Restored)
	require.Equal(1, out.PartiallyRestored)
	require.Equal([]RevertNote{plain, {Text: "1 note(s) that name a user withheld (users.manage shows them)"}}, out.Kept)
	require.NotContains(out.Summary(), userID)
	require.Contains(out.Summary(), "1 note(s) that name a user withheld", "the combine list is not left empty")
	require.NotContains(out.Summary(), "left as they are: ;")

	clean := &RevertResult{Total: 1, Restored: 1, Kept: []RevertNote{{Text: "a lock was left as it is"}}}
	require.Equal(clean.Kept, clean.WithoutUserData().Kept, "nothing withheld, nothing added")
	var none *RevertResult
	require.Nil(none.WithoutUserData())
}

// What is withheld is decided by the note's UserIDs, not by its words: a note
// that says "user" for another reason is served, and a note that names an
// account without the word is withheld.
func TestRevertResult_WithoutUserData_WithholdsByFieldNotByWord(t *testing.T) {
	const userID = "01INVENTEDUSER0000000000A"
	wordOnly := RevertNote{Text: "change c3: the user-facing lock on The Dark Knight Returns was left as it is"}
	idOnly := RevertNote{Text: "change c4: 01INVENTEDUSER0000000000A kept at 91% on The Killing Joke", UserIDs: []string{userID}}
	in := &RevertResult{
		Total: 2, Restored: 2, PartiallyRestored: 2, CombinesUndoneInPart: 2,
		Kept:        []RevertNote{wordOnly, idOnly},
		combineKept: []RevertNote{wordOnly, idOnly},
	}
	out := in.WithoutUserData()
	assert.Equal(t, []RevertNote{wordOnly, {Text: "1 note(s) that name a user withheld (users.manage shows them)"}}, out.Kept)
	assert.Equal(t, out.Kept, out.combineKept)
	assert.Contains(t, out.Summary(), "the user-facing lock")
	assert.NotContains(t, out.Summary(), userID)

	// The ids stay server side: a note serves as its bare text.
	body, err := json.Marshal(in)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"kept":["change c3: the user-facing lock`)
	var back RevertResult
	require.NoError(t, json.Unmarshal(body, &back))
	assert.Equal(t, wordOnly.Text, back.Kept[0].Text)
}
