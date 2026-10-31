// file: internal/merge/warning_test.go
// version: 1.0.0
// guid: 7d2a94e1-0b36-4c58-9f1d-a83e5c60b472
// last-edited: 2026-10-30

package merge

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A note that names no user serves as the bare string these lists always were;
// one that names a user keeps its account ids through a store round trip (the
// undo's applied record); a bare string written before the field existed
// reads back as a note with no recorded user.
func TestWarning_JSONRoundTrip(t *testing.T) {
	plain, err := json.Marshal(PlainWarning("a credit added since was left as it is"))
	require.NoError(t, err)
	assert.Equal(t, `"a credit added since was left as it is"`, string(plain))

	named := UserWarning("user 01INVENTEDUSER0000000000A progress on survivor b1 changed after the merge; left as is", "01INVENTEDUSER0000000000A")
	data, err := json.Marshal(combineUndoApplied{Warnings: []Warning{named, PlainWarning("x")}})
	require.NoError(t, err)

	var back combineUndoApplied
	require.NoError(t, json.Unmarshal(data, &back))
	require.Len(t, back.Warnings, 2)
	assert.Equal(t, named, back.Warnings[0])
	assert.True(t, back.Warnings[0].NamesUser())
	assert.Equal(t, PlainWarning("x"), back.Warnings[1])
	assert.False(t, back.Warnings[1].NamesUser())

	var legacy combineUndoApplied
	require.NoError(t, json.Unmarshal([]byte(`{"warnings":["old note"]}`), &legacy))
	assert.Equal(t, []Warning{PlainWarning("old note")}, legacy.Warnings)
}

// The producers set UserIDs where they write the account into the text, and a
// note that merely says "user" does not.
func TestWarning_TextsAndConstructors(t *testing.T) {
	assert.Nil(t, PlainWarnings())
	assert.Nil(t, WarningTexts(nil))
	ws := []Warning{PlainWarning("a"), UserWarning("b", "u1", "u2")}
	assert.Equal(t, []string{"a", "b"}, WarningTexts(ws))
	assert.False(t, PlainWarning("if the user listens again the put-back is read as done").NamesUser())
	assert.True(t, ws[1].NamesUser())
}
