// file: internal/undo/user_data.go
// version: 1.4.1
// guid: b7545a0f-8cbd-4cda-b35c-382ea405714b
// last-edited: 2026-10-28

package undo

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// changeTypeHoldsUserData classifies every change type this package declares:
// true when a row of that type carries one user's own data -- listening state
// and positions, a private playlist's contents, an account's identity -- that
// another user may not read. Readers that serve journal rows to other users
// (the operation and per-book journal routes, opaccess.Reader.MayReadChange)
// ask IsUserDataChangeType and keep such rows to callers who may manage users.
//
// Every ChangeType constant declared in this package must have an entry, true
// or false: TestUserDataChangeTypes_EveryConstantIsClassified fails the build
// on one that is missing, so a new type cannot reach the journal routes
// unclassified.
var changeTypeHoldsUserData = map[string]bool{
	// User data.
	//
	// One user's listening state and positions, before and after.
	ChangeTypeUserBookStateSet: true,
	// Every user's listening state and positions on a retired book and on its
	// survivor, before and after (UserStateFollowRecord).
	ChangeTypeUserStateFollow: true,
	// A retire carried user content: rating snapshots, collection membership,
	// and membership of a static playlist, which is private to the user who
	// made it (its contents and the neighbouring book ids). Classified as a
	// whole type, so the rating and collection kinds are kept to the same
	// readers as the playlist kind.
	ChangeTypeUserContentFollow: true,
	// Names the user who applied the row and how he signed in.
	ChangeTypeRepairOwnerApply: true,
	// One read-elsewhere record the master-list import created: the user it is
	// for, the book they have read or own, its status and finish time. That is
	// one user's reading history, so it is kept to callers who may manage users.
	ChangeTypeMasterListElsewhereCreate: true,

	// Library data: books, files, authors, series, tags and the ledger notes
	// about them.
	ChangeTypeAuthorRename:              false,
	ChangeTypeAuthorIDRepoint:           false,
	ChangeTypeAuthorMergeCredits:        false,
	ChangeTypeAuthorMergeDelete:         false,
	ChangeTypeAuthorReclassifyCredits:   false,
	ChangeTypeAuthorReclassifyDelete:    false,
	ChangeTypeAIScanResultApplied:       false,
	ChangeTypeBookTagAdd:                false,
	ChangeTypeRepairBookCreate:          false,
	ChangeTypeFieldLock:                 false,
	ChangeTypeJunkAuthorCredits:         false,
	ChangeTypeJunkAuthorCreate:          false,
	ChangeTypeSeriesRename:              false,
	ChangeTypeBookFileReassign:          false,
	ChangeTypeBookFileTrack:             false,
	ChangeTypeBookPathUpdate:            false,
	ChangeTypeBookFileMove:              false,
	ChangeTypeBookSoftDelete:            false,
	ChangeTypeBookFileCreate:            false,
	ChangeTypeBookPrimaryDemote:         false,
	ChangeTypeBookPrimaryHandoff:        false,
	ChangeTypeBookPrimaryHandoffRefused: false,
	ChangeTypeRepairPlanRecord:          false,
	ChangeTypeExternalIDReassign:        false,
	ChangeTypeBookMergedInto:            false,
	ChangeTypeITunesPathClear:           false,
	ChangeTypeBookFileVersionLink:       false,
	ChangeTypeTagWrite:                  false,
	ChangeTypeFileCopy:                  false,
	ChangeTypeMetadataApply:             false,
	ChangeTypeMetadataCacheCopy:         false,
	ChangeTypeTitleRelinkCredits:        false,
	ChangeTypeTitleRelinkAuthorCreate:   false,
	// One book's credit lists and flat author/narrator fields, before and
	// after the credits backfill.
	ChangeTypeCreditsBackfill: false,
	// A narrator name the credits backfill is about to create: a library
	// record's name, no one user's data.
	ChangeTypeNarratorCreateIntent: false,
	// The merge pointers hold only a journal id. The journal they name does
	// hold every user's progress; the routes that serve it strip that for
	// callers who may not manage users (GET /merge/combine-journal and
	// /merge/sibling-journal).
	ChangeTypeMergeCombineJournal: false,
	ChangeTypeMergeSiblingJournal: false,
}

// libraryDataLiteralChangeTypes are the change types a journaling writer
// records that no constant of this package declares: the shared edit-history
// vocabulary (metadata_update), the series-prune ledger's (series_merge,
// series_delete) and the book_file location repoint, whose constant lives in
// the database package beside the location it encodes. Their rows are library history, never
// one user's data. They are the only literals ClassifyChangeType knows besides
// the constants above; a writer that must refuse an unclassified type
// (repairs.Writer) adds a new literal here, deliberately, or declares a constant
// and classifies it in changeTypeHoldsUserData.
var libraryDataLiteralChangeTypes = map[string]bool{
	"metadata_update": true,
	"series_merge":    true,
	"series_delete":   true,

	database.ChangeTypeBookFileRepoint: true,
}

// ClassifyChangeType reports whether changeType is one this package has
// classified, and if so whether its rows carry one user's own data. known is
// false for a type that is neither a ChangeType constant of this package nor
// one of libraryDataLiteralChangeTypes: nobody has decided whether it is user
// data, and a writer whose rows reach callers who may not manage users should
// not record it (repairs.Writer refuses it).
func ClassifyChangeType(changeType string) (holdsUserData, known bool) {
	if v, ok := changeTypeHoldsUserData[changeType]; ok {
		return v, true
	}
	return false, libraryDataLiteralChangeTypes[changeType]
}

// IsUserDataChangeType reports whether a journal row of changeType carries
// one user's own data that other users may not read (see
// changeTypeHoldsUserData). A type this package does not declare reports
// false: the change types other packages write (metadata_update,
// book_file_repoint_location, the opswriter types, ...) are library history.
func IsUserDataChangeType(changeType string) bool {
	return changeTypeHoldsUserData[changeType]
}

// NamesUser reports whether s, a free-text note a journal or an undo carries
// (a merge undo's warnings, a repair's error), mentions a user. The merge
// writes a user's id only into its progress warnings ("user <id> progress on
// ...", "user=<id>"), and the refusals on a user's state name the account, so a
// note that says "user" is withheld from a caller who may not manage users. It
// is a deny-list on purpose: it errs towards withholding, and a note that says
// "user" for another reason goes too.
func NamesUser(s string) bool {
	return strings.Contains(strings.ToLower(s), "user")
}
