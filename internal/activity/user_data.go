// file: internal/activity/user_data.go
// version: 1.0.0
// guid: 8f75db40-9f7a-4cb3-ad7f-757dd64ea827
// last-edited: 2026-10-25

package activity

import (
	"maps"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TypeTrashDiscardProgressPurge is the audit entry
// audiobooks.DiscardProgressAndPurge records when the owner drops every
// user's listening state on a book in the trash and purges it.
const TypeTrashDiscardProgressPurge = "trash_discard_progress_purge"

// DetailProgressSummary is the Details key of a TypeTrashDiscardProgressPurge
// entry that holds the discarded state: one line per user, with the user's
// name and progress ("reader: 42%, at 1:02:03").
const DetailProgressSummary = "progress_summary"

// userDataDetails names, per entry type, the Details keys that carry other
// users' own data (listening state and positions). The activity log routes are
// library.view reads; WithoutOthersUserData removes these keys for a caller
// who may not manage users, as the trash listing narrows the same summary
// (audiobooks.TrashProgressInfo.ForViewer).
var userDataDetails = map[string][]string{
	TypeTrashDiscardProgressPurge: {DetailProgressSummary},
}

// WithoutOthersUserData returns e without the Details that carry other users'
// own data (userDataDetails), for a caller who may not manage users. e is not
// modified: a Details map that loses a key is copied first.
func WithoutOthersUserData(e database.ActivityEntry) database.ActivityEntry {
	keys := userDataDetails[e.Type]
	if len(keys) == 0 || len(e.Details) == 0 {
		return e
	}
	var details map[string]any
	for _, k := range keys {
		if _, ok := e.Details[k]; !ok {
			continue
		}
		if details == nil {
			details = maps.Clone(e.Details)
		}
		delete(details, k)
	}
	if details != nil {
		e.Details = details
	}
	return e
}
