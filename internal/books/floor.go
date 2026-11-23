// file: internal/books/floor.go
// version: 0.1.0
// guid: 3c7e0a58-91d4-4b26-8f5a-e2b61d94c7a0
// last-edited: 2026-11-22

package books

import (
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/storage"
)

// Books' one upgrade floor (R3-02; 09 section 9.2.2, 05-storage section
// 5S.3.5): R3 deletes the code that only a pre-R2 store needs (books' legacy
// steps 1..LegacyCap, the HEAD-format readers and the cutover steps), so a
// store that skipped R2 must be refused before anything reads it with that
// code. The floor is what refuses it: a non-empty store whose books version is
// below LegacyCap, or on which any of M2-M6 is not done, fails the boot at
// storage lifecycle step 3, before the checkpoint and before any write
// (*storage.ErrUpgradeFloor, exit 78), and restore staging refuses an
// archive below it before extracting anything. An empty store is exempt: its
// one fresh-install batch records books 1..LegacyCap as applied and every
// step named in Requires as done, registered by the build or not.

// The IDs of the cutover data steps M2-M6, as the floor's Requires names them.
// They are copies of internal/books/cutover's stepids.go (ConfigCutoverID and
// the four after it, the one place F3-06 reserved them): R3-02e deletes that
// package, and these constants are all that remain of the steps. The IDs are
// recorded in every cutover store, so they never change.
const (
	floorConfigCutoverID       = "books.config-cutover"
	floorIdentityCutoverID     = "books.identity-cutover"
	floorTasksCutoverID        = "books.tasks-cutover"
	floorLogCutoverID          = "books.log-cutover"
	floorIntegrationsCutoverID = "books.integrations-cutover"
)

// floorRequires is M2, M3, M4, M5 and M6, in that order.
func floorRequires() []storage.StepRef {
	var out []storage.StepRef
	for _, id := range []string{
		floorConfigCutoverID, floorIdentityCutoverID, floorTasksCutoverID,
		floorLogCutoverID, floorIntegrationsCutoverID,
	} {
		out = append(out, storage.StepRef{Owner: database.BooksOwner, DataMigration: id})
	}
	return out
}

// upgradeFloor is books' UpgradeFloor.
//
// InstallFirst is a PLACEHOLDER until OWNER-05 records the R2 production
// release: replace the literal below, and only it, with the tag from
// OWNER-05's tracking-issue record (its kept-decision line, "R2 release <tag>,
// cut from <rc tag>"), never from `gh release list` or the newest RC. (The
// decisions package forbids the literal key prefix anywhere else in the tree,
// comments included, so the line is not quoted with it.)
// It stays a string literal in the declaration because R3-04's acceptance
// greps `InstallFirst: "..."` under internal/books/ and checks that the
// release note names the same tag. TestUpgradeFloorTagIsNotPlaceholder
// (floor_release_test.go, build tag release) fails while the placeholder is
// still here; the R3 release gate runs it with -tags release.
func upgradeFloor() *storage.UpgradeFloor {
	return &storage.UpgradeFloor{
		Owner:        database.BooksOwner,
		Version:      database.LegacyCap,
		Requires:     floorRequires(),
		InstallFirst: "v0.0.0-r2-placeholder",
	}
}

// withUpgradeFloor is spec with books' floor declared. The manifest's storage
// spec and the standalone spec (the openers that plan no manifest: the restore
// stager's verify of an archive, the op-runner child) both take it, so a store
// below the floor is refused by every open of books' store, not only serve's.
func withUpgradeFloor(spec storage.Spec) storage.Spec {
	spec.UpgradeFloor = upgradeFloor()
	return spec
}
