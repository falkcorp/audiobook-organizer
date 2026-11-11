// file: internal/books/cutover/stepids.go
// version: 1.0.0
// guid: 359f1b3d-0f5d-41f1-b089-851054b4b811
// last-edited: 2026-11-11

package cutover

import (
	"github.com/falkcorp/mediacore/storage"
)

// The step IDs of the R2 cutover (05-storage §5S.3.13 "The list"; 03 §3.8's
// order M1, M7, M2, M3, M4, M5, M6, M8). This file is the one place that
// reserves them: F4-06 (M3), F5-16 (M4), F5-15 (M5), F7-08 (M6) and R3-02's
// UpgradeFloor.Requires take their ID from here and never coin another, and
// nothing in this package spells an ID as a literal. Every ID is
// "<owner>.<name>" with <name> matching [a-z][a-z0-9-]*
// (TestStepIDs_UniqueAndWellFormed). The names are coined by F3-06; the
// spec names none of them.
//
// A flip that adds a step adds its own level constant and level-step ID
// beside the ones below, never editing an earlier level's constant or ID and
// never the M8 ID (D15, F3-04's "Flip mechanism").

// BooksOwner is the storage owner of every step below: books' module ID.
const BooksOwner storage.Owner = "books"

// BooksCutoverLevel is the books reader level M7 raises books to in F3's flip
// (05-storage §5S.3.7): the level M2's RaisesLevel names, the one
// books.level-<n> step raises books to, and the Spec.Level F3-07 declares.
// An earlier build's level is 0.
const BooksCutoverLevel = 1

// BooksCutoverLevelStepID is the ID of books' half of M7 for F3's level
// (03 §3.8 row M7): storage.LevelStepName(BooksOwner, BooksCutoverLevel).
// It names a versioned step, not a data migration, so the step a later one
// runs After is the StepRef {Owner: BooksOwner, Version: <its version>}
// (BooksLevelStepRef); the version is the owner's next contiguous one, which
// F3-07 knows.
const BooksCutoverLevelStepID = "books.level-1"

// IDs of the cutover data steps.
const (
	// ConfigCutoverID is M2, config and secrets (03 §3.8 row M2; 05-config
	// §1.12.2): the settings blob's fields into core's config store under
	// canonical keys, the plaintext blob secrets and HEAD's four encrypted
	// rows into core's secrets store.
	ConfigCutoverID = "books.config-cutover"

	// IdentityCutoverID is M3, Identity (03 §3.8 row M3; F4-06): users, OAuth
	// identities, the invite allow-list, API keys and roles into core's auth
	// and rbac stores.
	IdentityCutoverID = "books.identity-cutover"

	// TasksCutoverID is M4, tasks (03 §3.8 row M4; F5-16): runs, schedule
	// clocks, markers and the Requires stamps into core's task store.
	TasksCutoverID = "books.tasks-cutover"

	// LogCutoverID is M5, the system log (03 §3.8 row M5; F5-15): the syslog:
	// rows into core's log journal.
	LogCutoverID = "books.log-cutover"

	// IntegrationsCutoverID is M6, integrations (03 §3.8 row M6; F7-08): the
	// blob's instance fields with their secret refs into core's integrations
	// store, then the blob deleted.
	IntegrationsCutoverID = "books.integrations-cutover"
)

// RewriteID is M8, the store rewrite (03 §3.8 row M8; 05-storage §5S.3.13
// "M8: the rewrite"): the one fixed ID for all of R2 (D15). A later flip
// inserts its steps before M8 and never renames, replaces or removes it.
const RewriteID = "books.rewrite"

// StepIDs is every ID this file reserves, in 03 §3.8's order: M7's books
// level step for F3's level, M2, M3, M4, M5, M6 and M8.
func StepIDs() []string {
	return []string{
		BooksCutoverLevelStepID,
		ConfigCutoverID,
		IdentityCutoverID,
		TasksCutoverID,
		LogCutoverID,
		IntegrationsCutoverID,
		RewriteID,
	}
}
