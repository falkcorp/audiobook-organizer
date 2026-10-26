// file: internal/server/handlers/opaccess/user_text_test.go
// version: 1.0.0
// guid: 91e4b7d2-6c38-4a15-b0f9-5d2a8c13e764
// last-edited: 2026-10-26

package opaccess

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
)

// UserText leaves free text alone for a caller with users.manage, and for any
// caller when the text names no user; otherwise it replaces the text whole. An
// unwired check holds nothing, so such text is withheld.
func TestReader_UserText(t *testing.T) {
	const naming = "merge failed: user_id=01INVENTEDOWNER0000000000A could not be moved"
	const plain = "scan of Year One failed: disk full"
	view := []auth.Permission{auth.PermLibraryView}
	users := []auth.Permission{auth.PermLibraryView, auth.PermUsersManage}

	c, _ := ctxHolding()
	r := NewRepairs(fixers{}, noOps, holdsOnly(view...)).For(c)
	require.Equal(t, activity.WithheldUserText, r.UserText(naming))
	require.Equal(t, plain, r.UserText(plain))
	require.Empty(t, r.UserText(""))

	c, _ = ctxHolding()
	r = NewRepairs(fixers{}, noOps, holdsOnly(users...)).For(c)
	require.Equal(t, naming, r.UserText(naming), "users.manage reads it whole")

	c, _ = ctxHolding()
	require.Equal(t, activity.WithheldUserText, NewRepairs(fixers{}, noOps, nil).For(c).UserText(naming), "nil holds")
	require.Equal(t, activity.WithheldUserText, (*Repairs)(nil).For(c).UserText(naming), "nil access")
}
