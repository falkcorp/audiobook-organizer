// file: internal/database/change_type_class.go
// version: 1.0.0
// guid: 7e1c4d52-93a8-4f60-b2d7-5a0e6c18f3b4
// last-edited: 2026-10-30

package database

import (
	"errors"
	"fmt"
)

// The journal's change-type vocabulary and its one classification check.
//
// A journal row (OperationChange) is served to callers who may not manage
// users, and the routes that serve it keep the user-data types from them
// (IsUserDataChangeType). A type nobody classified would be served as library
// history by default, so a writer must not record one. The check lives here,
// beside CreateOperationChange, because every journal writer imports this
// package: internal/undo (which declares most of the type constants) imports
// internal/merge, which writes journal rows too, so the table cannot live in
// undo without a cycle. undo.IsUserDataChangeType
// delegates here, and undo's test fails on a ChangeType constant this table
// lacks.
//
// RecordOperationChange is the one way a writer creates a row: it runs the
// check and then CreateOperationChange. internal/arch's journal-writer guard
// fails on a direct CreateOperationChange call anywhere else.

// ErrUnclassifiedChangeType: a writer asked to journal a change type this table
// does not classify, so nobody has decided whether its rows carry one user's
// data. The row is not written. To add a type, give it an entry in
// undoChangeTypeHoldsUserData (a constant internal/undo declares) or
// libraryDataLiteralChangeTypes (a literal no undo constant names), and decide
// true or false deliberately.
var ErrUnclassifiedChangeType = errors.New("change type is not classified as user or library data; the journal row was not written")

// undoChangeTypeHoldsUserData classifies every change type internal/undo
// declares as a ChangeType* constant: true when a row of that type carries one
// user's own data -- listening state and positions, a private playlist's
// contents, an account's identity -- that another user may not read. Each key
// is the value of the constant named in its trailing comment.
// undo.TestUserDataChangeTypes_EveryConstantIsClassified fails on a constant
// whose value is missing here and on a key no constant names.
var undoChangeTypeHoldsUserData = map[string]bool{
	// User data.
	//
	// One user's listening state and positions, before and after.
	"user_book_state_set": true, // ChangeTypeUserBookStateSet
	// Every user's listening state and positions on a retired book and on its
	// survivor, before and after (UserStateFollowRecord).
	"user_state_follow": true, // ChangeTypeUserStateFollow
	// A retire carried user content: rating snapshots, collection membership,
	// and membership of a static playlist, which is private to the user who
	// made it (its contents and the neighbouring book ids). Classified as a
	// whole type, so the rating and collection kinds are kept to the same
	// readers as the playlist kind.
	"user_content_follow": true, // ChangeTypeUserContentFollow
	// Names the user who applied the row and how he signed in.
	"repair_owner_apply": true, // ChangeTypeRepairOwnerApply
	// One read-elsewhere record the master-list import created: the user it is
	// for, the book they have read or own, its status and finish time. That is
	// one user's reading history, so it is kept to callers who may manage users.
	"master_list_elsewhere_create": true, // ChangeTypeMasterListElsewhereCreate

	// Library data: books, files, authors, series, tags and the ledger notes
	// about them.
	"author_rename":                false, // ChangeTypeAuthorRename
	"author_id_repoint":            false, // ChangeTypeAuthorIDRepoint
	"author_merge_credits":         false, // ChangeTypeAuthorMergeCredits
	"author_merge_delete":          false, // ChangeTypeAuthorMergeDelete
	"author_reclassify_credits":    false, // ChangeTypeAuthorReclassifyCredits
	"author_reclassify_delete":     false, // ChangeTypeAuthorReclassifyDelete
	"ai_scan_result_applied":       false, // ChangeTypeAIScanResultApplied
	"book_tag_add":                 false, // ChangeTypeBookTagAdd
	"repair_book_create":           false, // ChangeTypeRepairBookCreate
	"field_lock":                   false, // ChangeTypeFieldLock
	"junk_author_credits":          false, // ChangeTypeJunkAuthorCredits
	"junk_author_create":           false, // ChangeTypeJunkAuthorCreate
	"series_rename":                false, // ChangeTypeSeriesRename
	"book_file_reassign":           false, // ChangeTypeBookFileReassign
	"book_file_track":              false, // ChangeTypeBookFileTrack
	"book_path_update":             false, // ChangeTypeBookPathUpdate
	"book_file_move":               false, // ChangeTypeBookFileMove
	"book_soft_delete":             false, // ChangeTypeBookSoftDelete
	"book_file_create":             false, // ChangeTypeBookFileCreate
	"book_primary_demote":          false, // ChangeTypeBookPrimaryDemote
	"book_primary_handoff":         false, // ChangeTypeBookPrimaryHandoff
	"book_primary_handoff_refused": false, // ChangeTypeBookPrimaryHandoffRefused
	"repair_plan_record":           false, // ChangeTypeRepairPlanRecord
	"external_id_reassign":         false, // ChangeTypeExternalIDReassign
	"book_merged_into":             false, // ChangeTypeBookMergedInto
	"itunes_path_clear":            false, // ChangeTypeITunesPathClear
	"book_file_version_link":       false, // ChangeTypeBookFileVersionLink
	"tag_write":                    false, // ChangeTypeTagWrite
	"file_copy":                    false, // ChangeTypeFileCopy
	"metadata_apply":               false, // ChangeTypeMetadataApply
	"metadata_cache_copy":          false, // ChangeTypeMetadataCacheCopy
	"title_relink_credits":         false, // ChangeTypeTitleRelinkCredits
	"title_relink_author_create":   false, // ChangeTypeTitleRelinkAuthorCreate
	// One book's credit lists and flat author/narrator fields, before and
	// after the credits backfill.
	"credits_backfill": false, // ChangeTypeCreditsBackfill
	// A retire of a combine's shell (retired-versions undo): the version row
	// created, each file row flagged retired and moved under the holder, and a
	// retired version rehomed to another holder. File, version and book ids
	// and paths only; the user content a retire carries is journaled in
	// ChangeTypeUserContentFollow above.
	"book_version_create":    false, // ChangeTypeBookVersionCreate
	"book_file_retire":       false, // ChangeTypeBookFileRetire
	"retired_version_rehome": false, // ChangeTypeRetiredVersionRehome
	// A narrator name the credits backfill is about to create: a library
	// record's name, no one user's data.
	"narrator_create_intent": false, // ChangeTypeNarratorCreateIntent
	// The merge pointers hold only a journal id. The journal they name does
	// hold every user's progress; the routes that serve it strip that for
	// callers who may not manage users (GET /merge/combine-journal and
	// /merge/sibling-journal).
	"merge_combine_journal": false, // ChangeTypeMergeCombineJournal
	"merge_sibling_journal": false, // ChangeTypeMergeSiblingJournal
}

// libraryDataLiteralChangeTypes are the change types a journaling writer
// records that no constant of internal/undo declares: the shared edit-history
// vocabulary (metadata_update), the series-prune ledger's, the book_file
// location repoint, and the operation ledgers of the organize, the narrator and
// author maintenance ops and the repoint/dedupe ops. Their rows are library
// history, never one user's data. A writer that needs a new literal adds it
// here, deliberately, or declares an undo constant and classifies it above.
var libraryDataLiteralChangeTypes = map[string]bool{
	"metadata_update":        true,
	"series_merge":           true,
	"series_delete":          true,
	"series_prune_summary":   true,
	"author_link":            true,
	"author_delete":          true,
	"narrator_split":         true,
	"narrator_orphan_unlink": true,
	"narrator_delete":        true,
	// repoint-book-file-rows journals the whole stored row; the location
	// repoint below is the opswriter's narrower row.
	"book_file_repoint": true,
	"book_file_delete":  true,
	"file_path_update":  true,
	"book_create":       true,
	// The organize ledger: a file placed, skipped, or failed, and the run's
	// summary line.
	"organize":         true,
	"organize_summary": true,
	"organize_skipped": true,
	"organize_rename":  true,
	"organize_failed":  true,

	ChangeTypeBookFileRepoint: true,
}

// ClassifyChangeType reports whether changeType is classified, and if so
// whether its rows carry one user's own data. known is false for a type that
// is neither an internal/undo ChangeType constant nor one of
// libraryDataLiteralChangeTypes.
func ClassifyChangeType(changeType string) (holdsUserData, known bool) {
	if v, ok := undoChangeTypeHoldsUserData[changeType]; ok {
		return v, true
	}
	return false, libraryDataLiteralChangeTypes[changeType]
}

// IsUserDataChangeType reports whether a journal row of changeType carries one
// user's own data that other users may not read. A type that is not classified
// reports false; RecordOperationChange refuses to write one.
func IsUserDataChangeType(changeType string) bool {
	return undoChangeTypeHoldsUserData[changeType]
}

// CheckChangeTypeClassified refuses a change type ClassifyChangeType does not
// know. RecordOperationChange(s) call it; a writer that must refuse before it
// does anything else (repairs.Writer, before its lease beat) calls it too, so
// there is one implementation of the check.
func CheckChangeTypeClassified(changeType string) error {
	if _, known := ClassifyChangeType(changeType); !known {
		return fmt.Errorf("%w: %q", ErrUnclassifiedChangeType, changeType)
	}
	return nil
}

// OperationChangeCreator is the one journal method RecordOperationChange needs.
type OperationChangeCreator interface {
	CreateOperationChange(change *OperationChange) error
}

// OperationChangesCreator is the batch form (PebbleStore.CreateOperationChanges):
// every row lands in one commit or none does.
type OperationChangesCreator interface {
	CreateOperationChanges(changes []*OperationChange) error
}

// RecordOperationChange is the checked way to create a journal row: it refuses
// an unclassified change type, then stores the row with CreateOperationChange.
// Every journal writer outside this package goes through it (or through
// repairs.Writer, which does).
func RecordOperationChange(store OperationChangeCreator, change *OperationChange) error {
	if err := CheckChangeTypeClassified(change.ChangeType); err != nil {
		return err
	}
	return store.CreateOperationChange(change)
}

// RecordOperationChanges is RecordOperationChange for several rows committed
// together. Every row is checked before any is written.
func RecordOperationChanges(store OperationChangesCreator, changes []*OperationChange) error {
	for _, c := range changes {
		if err := CheckChangeTypeClassified(c.ChangeType); err != nil {
			return err
		}
	}
	return store.CreateOperationChanges(changes)
}
