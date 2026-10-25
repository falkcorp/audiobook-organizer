// file: internal/activity/user_data.go
// version: 1.1.0
// guid: 8f75db40-9f7a-4cb3-ad7f-757dd64ea827
// last-edited: 2026-10-25

package activity

import (
	"maps"
	"regexp"
	"strings"

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

// TagUserData is the tag Writer puts on a server log line that names a user or
// carries one user's own data (NamesUserText). It is written with the row, so a
// row that has it stays withheld from a caller who may not manage users
// whatever its source or wording; NamesOthersUserData also recognises the same
// lines by their text, which is what covers rows stored before the tag existed.
const TagUserData = "user-data"

// userTextPattern finds the ways a log line names a user: a key=value or key:
// pair whose key is a user, account or e-mail field (user=, user_id=, username=,
// owner_user_id=, access_email=, email=), and a sentence that says "user <id>"
// where the id is id-shaped (it holds a digit, a hyphen or an underscore, so
// "user should" and "user interface" do not match). The abs progress handler
// logs "user=<id> ... currentTime=...", the merge undo warns "user <id> progress
// on ...", and the owner guard logs "user=... access_email=...".
//
// It is a deny-list on purpose: it errs towards withholding a line, and a
// line that names a user with no id-shaped token and none of these keys passes.
var userTextPattern = regexp.MustCompile(
	`(?i)(?:^|[^a-z0-9])(?:(?:owner_)?user(?:_?id|_?name)?|access_?email|e-?mail)\s*[=:]` +
		`|(?:^|[^a-z0-9])user\s+[0-9a-z_-]*[0-9_-][0-9a-z_-]*`)

// NamesUserText reports whether s, a log line or an entry summary, names a user
// (userTextPattern).
func NamesUserText(s string) bool {
	return s != "" && userTextPattern.MatchString(s)
}

// userDataSource reports whether a server log source writes lines about users:
// the Audiobookshelf compatibility layer ("abs", "abs-cache", ...) logs every
// user's progress, and the merge service logs the progress it moved.
func userDataSource(src string) bool {
	return src == "abs" || strings.HasPrefix(src, "abs-") || src == "merge"
}

// NamesOthersUserData reports whether entry e carries other users' own data, so
// only a caller who may manage users reads it (owner decision, 2026-10-24). The
// server's own log is mirrored into the activity log (Writer), and the abs
// progress handler, the merge service and the owner guard write user ids,
// positions and e-mail addresses into it, which GET /activity serves at
// library.view; so an entry is withheld when it
//
//   - has the TagUserData tag (Writer stamped it), or
//   - is a "system" entry of the abs or merge source (its tag source:<name>
//     says which when a digest item has no Source), or
//   - has a summary that names a user (NamesUserText), of any type: this is what
//     covers rows stored before the tag, and the engine's owner-apply line
//     ("owner apply by user <id>"), which is logged under its Repairs operation.
//
// It reads only the fields a store filter reads (never Details), so it can be
// the activity filter's ExcludeEntry predicate.
func NamesOthersUserData(e database.ActivityEntry) bool {
	for _, t := range e.Tags {
		if t == TagUserData {
			return true
		}
	}
	if e.Type == "system" {
		if userDataSource(e.Source) {
			return true
		}
		for _, t := range e.Tags {
			if src, ok := strings.CutPrefix(t, "source:"); ok && userDataSource(src) {
				return true
			}
		}
	}
	return NamesUserText(e.Summary)
}
