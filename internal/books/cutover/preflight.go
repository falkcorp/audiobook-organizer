// file: internal/books/cutover/preflight.go
// version: 1.0.0
// guid: 0c386a33-654b-4172-8a8e-b5233793eb25
// last-edited: 2026-11-12

package cutover

import (
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// wirePreflight sets M3's stop-the-upgrade hooks on dm (§5S.3.13 "Cutover
// blockers"; 09 §9.2.3).
//
// Preflight reads books' legacy range through the reader the kernel hands it,
// with HEAD-format decoders, so it needs neither M1 nor any native store: it
// decodes every u:, apikey: and oauthid: row with its idx: rows, runs the
// importers' own record rules over each without staging, and lists every merge
// journal still applying or undoing. Each problem is a datamigrate.Blocker
// with the step, the key, the family and a reason and a fix, never a value.
// It writes nothing: the kernel runs it before the checkpoint and before M7,
// and never on a store whose cutover has started.
//
// FixBlocker and DeleteBlocker are the two hooks `storage cutover-blockers
// fix` and `delete` call, and nothing else does. A fix stages the operator's
// repaired value and is refused unless the row then converts; a delete removes
// the row and, for a user, its keys, identities and index rows, and returns
// the IDs it removed, which Verify later counts in OrphanedRefs where books
// rows still name them. A merge in progress is not fixable here: the report
// says to finish or undo it on the pre-freeze release.
func wirePreflight(dm *datamigrate.DataMigration, m *m3.Migration) {
	dm.Preflight = m.Preflight
	dm.FixBlocker = m.FixBlocker
	dm.DeleteBlocker = m.DeleteBlocker
}
