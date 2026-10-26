// file: internal/activity/user_data.go
// version: 1.2.0
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

// The audit-tier entry types that name another account or an administrator by
// name. TypesNamingAccounts is the deny-list NamesOthersUserData holds them to;
// the writers record them under these constants so a renamed type cannot drift
// out of it.
const (
	// TypeAuthEmailDisplaced: a verified sign-in or an accepted invitation took
	// an email back from unverified accounts (summary and details name the new
	// and the displaced account ids).
	TypeAuthEmailDisplaced = "auth_email_displaced"
	// TypeGuestServiceTokenPrefix starts the four guest-token audit types
	// (minted, mint_failed, revoked, revoke_failed): each names the guest's
	// account id and the Cloudflare token id, and the failures carry the
	// Cloudflare error text.
	TypeGuestServiceTokenPrefix = "guest_service_token_"
	// TypeUserMerge: an admin merged one account into another (or undid it).
	TypeUserMerge = "user_merge"
	// TypeAdminDebugEdit and TypeAdminDebugUndo: an admin edited or undid an
	// edit of a book or book file; Details["actor"] is "username (ULID)".
	TypeAdminDebugEdit = "admin_debug_edit"
	TypeAdminDebugUndo = "admin_debug_undo"
)

// DetailActor is the Details key of the audit entries that name the person who
// acted ("username (ULID)"): the trash discard-and-purge and the admin debug
// edit and undo. The actor is the owner or an admin, but the username is still
// an account fact the library.view activity routes do not serve.
const DetailActor = "actor"

// userDataDetails names, per entry type, the Details keys that carry other
// users' own data (listening state and positions) or an account's name. The
// activity log routes are library.view reads; WithoutOthersUserData removes
// these keys for a caller who may not manage users, as the trash listing
// narrows the same summary (audiobooks.TrashProgressInfo.ForViewer). The rows
// themselves are about a book and stay visible.
var userDataDetails = map[string][]string{
	TypeTrashDiscardProgressPurge: {DetailProgressSummary, DetailActor},
	TypeAdminDebugEdit:            {DetailActor},
	TypeAdminDebugUndo:            {DetailActor},
}

// typeNamesAccounts reports whether an entry of type t is an account or
// identity event: its summary or details carry account ids and, for some,
// provider error text, so the whole row is withheld from a caller who may not
// manage users. It is by type, not by wording: "account <id>" is not a pattern
// userTextPattern can safely own.
func typeNamesAccounts(t string) bool {
	switch t {
	case TypeAuthEmailDisplaced, TypeUserMerge:
		return true
	}
	return strings.HasPrefix(t, TypeGuestServiceTokenPrefix)
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

// userTextPattern finds the ways a log line names a user:
//
//   - a key=value or key: pair whose key is a user, account or e-mail field
//     (user=, user_id=, username=, owner_user_id=, adminUser=, access_email=,
//     email=);
//   - a sentence that says "user <id>" where the id is id-shaped (it holds a
//     digit, a hyphen or an underscore, so "user should" and "user interface"
//     do not match). The abs progress handler logs "user=<id> ...
//     currentTime=...", the merge undo warns "user <id> progress on ...", and
//     the owner guard logs "user=... access_email=...";
//   - an account, owner, admin or user word followed by a ULID-shaped id, with
//     an optional "(s)", bracket, parenthesis or quote between them: "account
//     <id>", "account(s) [<id> <id>]", `owner "<id>"`, "first admin <id>",
//     "more than one user (<id>, <id>)";
//   - the phrase "user listening state" (a store that cannot restore it names
//     the user and the book after it);
//   - an e-mail address.
//
// It is a deny-list on purpose: it errs towards withholding a line, and a
// line that names a user with none of these shapes passes (whole subsystems
// that write such lines are held back by their name instead:
// userDataSubsystems).
var userTextPattern = regexp.MustCompile(
	`(?i)(?:^|[^a-z0-9])(?:(?:owner_|admin_?)?user(?:_?id|_?name)?|access_?email|e-?mail)\s*[=:]` +
		`|(?:^|[^a-z0-9])user\s+[0-9a-z_-]*[0-9_-][0-9a-z_-]*` +
		`|(?:^|[^a-z0-9])(?:account|owner|admin|user)(?:\(s\))?s?\s*[\["'(]*(?-i:[0-9A-Z]{20,})` +
		`|(?:^|[^a-z0-9])user\s+listening\s+state` +
		`|[a-z0-9._%+-]+@[a-z0-9-]+(?:\.[a-z0-9-]+)*\.[a-z]{2,}`)

// NamesUserText reports whether s, a log line or an entry summary, names a user
// (userTextPattern).
func NamesUserText(s string) bool {
	return s != "" && userTextPattern.MatchString(s)
}

// userDataSubsystems are the logger subsystems (logger.New("<name>")) whose
// lines are about accounts or one user's listening state, so every line of
// theirs is withheld: the abs compatibility layer logs every user's progress,
// the merge service the progress it moved, the invite, OAuth and auth layers
// the accounts they create, displace and look up, and the playback store the
// (user, book) rows it reads and writes. A name ending in "-" is a prefix
// ("abs-" covers "abs-cache").
var userDataSubsystems = []string{
	"abs", "abs-", "merge",
	"server.invites", "handlers.invites", "oauth.resolve",
	"database.auth", "database.playback",
}

// userDataSource reports whether a server log source or logger subsystem name
// writes lines about users (userDataSubsystems).
func userDataSource(src string) bool {
	for _, n := range userDataSubsystems {
		if src == n || (strings.HasSuffix(n, "-") && strings.HasPrefix(src, n)) {
			return true
		}
	}
	return false
}

// summarySubsystem returns the logger subsystem a server log summary starts
// with ("abs" for "abs: progress patch ..."), or "" when it has none.
//
// The writer cannot tell a subsystem from the line itself: the server installs
// slog's text handler as the default, so every logger.New line arrives as
// `msg="<subsystem>: <text>"`, which parses to Source "server" and keeps the
// subsystem only as the summary's prefix. (A line in the older "[info] abs:
// ..." layout carries it as the Source instead.)
func summarySubsystem(summary string) string {
	name, _, ok := strings.Cut(summary, ": ")
	if !ok || name == "" || strings.ContainsAny(name, " \t=\"") {
		return ""
	}
	return name
}

// NamesOthersUserData reports whether entry e carries other users' own data, so
// only a caller who may manage users reads it (owner decision, 2026-10-24). The
// server's own log is mirrored into the activity log (Writer), and the abs
// progress handler, the merge service, the invite and auth layers and the owner
// guard write user ids, positions and e-mail addresses into it, which GET
// /activity serves at library.view; so an entry is withheld when it
//
//   - has the TagUserData tag (Writer stamped it), or
//   - is of an account or identity type (typeNamesAccounts): the email-
//     displacement, guest-token and user-merge audit rows, which name accounts
//     in their summary and details, or
//   - is a "system" entry of a user-data source or subsystem (userDataSource,
//     matched on its Source, on its source:<name> tag when a digest item has no
//     Source, and on the "<subsystem>: " prefix of its summary), or
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
	if typeNamesAccounts(e.Type) {
		return true
	}
	if e.Type == "system" {
		if userDataSource(e.Source) || userDataSource(summarySubsystem(e.Summary)) {
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
