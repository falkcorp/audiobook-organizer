// file: internal/activity/user_data_test.go
// version: 1.0.0
// guid: 70dbd475-4218-4a5d-ae68-86d5bd16ad4f
// last-edited: 2026-10-25

package activity

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// WithoutOthersUserData drops the trash purge's per-user summary and leaves
// every other detail, every other entry type, and the input entry alone.
func TestWithoutOthersUserData(t *testing.T) {
	in := database.ActivityEntry{Type: TypeTrashDiscardProgressPurge, Details: map[string]any{
		DetailProgressSummary: "invented-reader: finished", "users_cleared": 1,
	}}
	out := WithoutOthersUserData(in)
	assert.NotContains(t, out.Details, DetailProgressSummary)
	assert.Equal(t, 1, out.Details["users_cleared"])
	assert.Equal(t, "invented-reader: finished", in.Details[DetailProgressSummary], "the input is not modified")

	other := database.ActivityEntry{Type: "library.scan", Details: map[string]any{DetailProgressSummary: "kept"}}
	assert.Equal(t, other, WithoutOthersUserData(other))
	assert.Equal(t, database.ActivityEntry{Type: TypeTrashDiscardProgressPurge}, WithoutOthersUserData(database.ActivityEntry{Type: TypeTrashDiscardProgressPurge}))
}
