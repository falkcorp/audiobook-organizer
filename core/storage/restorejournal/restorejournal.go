// file: core/storage/restorejournal/restorejournal.go
// version: 1.0.4
// guid: 6c954a92-88a0-4242-b599-92db9a12cae7
// last-edited: 2026-11-11

// Package restorejournal holds the kinds, record types and intent of the one
// journaled directory swap (05-backup's staged restore, revert and checkpoint
// restore; the offline restore, 05-storage §5S.3.12; the factory reset,
// §5S.3.15): an L0 leaf that imports nothing from core.
//
// The declarations are 05-storage §5S.3.15's Go block, Intent to Record.
// core/internal/restoreswap (R1-21) applies an intent; storage's open reads
// the journal. Every writer of the one intent slot calls CreateIntent, so
// the slot has one create.
package restorejournal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// ErrSwapPending: the one intent slot is taken or a swap is in flight;
// storage re-exports it as storage.ErrSwapPending.
var ErrSwapPending = errors.New("storage: a restore or factory reset is already staged or in flight")

// Intent is restore/pending.json for every kind, the one shared type of its
// writers — core's restoreswap and, until R2's backup flip (F8), ABO's
// internal/backup — and of P0a's one rejection rule (COR9-13). Fields a kind
// does not use are empty.
type Intent struct {
	ID            string    `json:"id"`
	Kind          Kind      `json:"kind"`
	Reset         ResetKind `json:"reset,omitempty"`
	DBPath        string    `json:"db_path"`         // absolute
	Targets       []Target  `json:"targets"`         // absolute from/to
	StagedByBuild string    `json:"staged_by_build"` // the binary ID
	ExpiresAt     time.Time `json:"expires_at"`
	RequestedBy   string    `json:"requested_by"`
	RequestedAt   time.Time `json:"requested_at"`
	// Restore kinds (05-backup §5B.3.4 step 8).
	ArchiveID      string `json:"archive_id,omitempty"`
	Staging        string `json:"staging,omitempty"`
	InstallID      string `json:"install_id,omitempty"`
	ManifestSHA256 string `json:"manifest_sha256,omitempty"`
	// KindRewrite (§5S.3.13 "M8"): the step ID of the M8 that staged it, so
	// each M8 reads only its own journals, and the name of the upgrade
	// attempt's checkpoint it ran under (storage.DB.AttemptCheckpoint), so
	// M8 counts only that attempt's swap-backs: a store restored since is a
	// new attempt. Attempt is F3-04's.
	StepID  string `json:"step_id,omitempty"`
	Attempt string `json:"attempt,omitempty"`
	// KindReset (05-storage §5S.3.15).
	DeleteLibraryFiles bool     `json:"delete_library_files,omitempty"`
	Delete             []string `json:"delete,omitempty"`
	HookArgs           []byte   `json:"hook_args,omitempty"`  // opaque app bytes; the kernel never parses them
	HookPaths          []string `json:"hook_paths,omitempty"` // ResetPlan.Paths (R10-6)
}

// Target is one absolute from/to pair of an intent: targets[{from, to}].
type Target struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// The slot's file names under <stateDir>/restore/.
const (
	restoreDir   = "restore"
	pendingName  = "pending.json"
	inflightName = "inflight.json"
)

// CreateIntent writes in into <stateDir>/restore/pending.json atomically:
// temp file + fsync, link(2) (EEXIST → ErrSwapPending, the temp removed),
// remove the temp, directory fsync. It also returns ErrSwapPending while any
// restore/<id>/inflight.json exists. Every writer calls it, so the slot has
// one create.
//
// link(2) is the exclusion between writers, across processes, with no mutex
// (05-storage §5S.3.15 "Stage"): two creates race on one name and exactly one
// wins. The in-flight probe runs before it, as the spec's refusal "while a
// swap of any kind is in flight". The probe and the link are two steps, so a
// swap that renames pending.json to inflight.json between them is not seen:
// stage only after P0a (which resolves any swap in flight) or with the
// service stopped. It is CreateIntentFS over the real filesystem.
func CreateIntent(stateDir string, in Intent) error {
	return CreateIntentFS(vfs.Default, stateDir, in)
}

// tempLeft reports a temp intent file the create could not remove, nil when
// it was removed.
func tempLeft(tmp string, rmErr error) error {
	if rmErr == nil || errors.Is(rmErr, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("restorejournal: remove temp intent %s: %w", filepath.Base(tmp), rmErr)
}

// Kind is the swap's kind, the intent's "kind" field and the "staged"
// record's Note. 05-backup's journal.RestoreKind is the hooks' view of the
// first three; a reset runs no restore hook.
type Kind string

const (
	KindArchive    Kind = "archive"
	KindRevert     Kind = "revert"
	KindCheckpoint Kind = "checkpoint"
	KindReset      Kind = "reset"
	// KindRewrite is M8's swap of the store's fresh copy for its old
	// directory (§5S.3.13): it keeps the store generation, and its set-aside
	// old directory is deleted at once, never kept as previous/.
	KindRewrite Kind = "rewrite"
)

// ResetKind is a KindReset swap's reset kind, the intent's "reset" field
// (COR8-1).
type ResetKind string

const (
	ResetStore   ResetKind = "store"   // store and settings, no file effect
	ResetFactory ResetKind = "factory" // also aux DBs and the reset hook
)

// Record is one line of restore/<id>/journal.json, for every kind. Restore
// steps are 05-backup §5B.3.4's (and "applied_offline", §5S.3.12); KindReset
// steps are "staged", "set_aside_started", "moved", "set_aside", "applied",
// "hook_done", "rejected". In every kind restore/<id>/inflight.json is
// created by renaming pending.json before the first live item is renamed
// (R9-7).
//
// The file is JSON Lines, one Record per line, each line fsynced before
// its step is acted on (05-storage §5S.3.15). A last line without its
// newline, or one that does not decode, is a torn append, never
// acknowledged: a reader drops it, and the next append rewrites the file
// without it (temp, fsync, rename, directory fsync), so stale bytes past a
// shorter line never survive. An undecodable line before the last is
// corruption, and a reader stops (restoreswap.ErrJournalCorrupt).
type Record struct {
	At    time.Time `json:"at"`
	Step  string    `json:"step"`
	Seq   uint64    `json:"seq,omitempty"` // restore "staged": the outgoing store's journal head seq (DATA2-25)
	Actor string    `json:"actor"`
	Note  string    `json:"note,omitempty"`
	// From and To (KindReset "moved" only): the target and its set-aside
	// destination, so resume, move-back and manual undo know where it went.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Failed ("hook_done" only): the entries the reset hook could not remove
	// (COR8-4).
	Failed []string `json:"failed,omitempty"`
	// Kept ("hook_done" only): the delete entries skipped because removing
	// them would remove a kept path (rule (4), Kept): a skip, not a failure.
	Kept []string `json:"kept,omitempty"`
	// InstallID ("set_aside_started" only): the old install's ID, for manual
	// undo, also after a crash mid-set-aside (DATA9-15).
	InstallID string `json:"install_id,omitempty"`
	// HooksOwed (restore "marker_written" and "applied_offline"): a build
	// without P4d steps 2-3 (R1) applied this restore with no hooks, or an
	// offline restore of a checkpoint whose record has hooks_owed (the
	// escape from a failed journal included, DATA-7); the next journaling
	// boot runs them with the source's boundary (05-backup §5B.3.6).
	HooksOwed bool `json:"hooks_owed,omitempty"`
	// StepID, Attempt and DBPath (KindRewrite "staged" only): the staging
	// M8's step ID, its attempt checkpoint and the store's path, copied from
	// the intent, so M8 finds its own journals of this store after
	// inflight.json is gone and counts its attempt's swap-backs (§5S.3.13
	// "M8"). Attempt and DBPath are F3-04's.
	StepID  string `json:"step_id,omitempty"`
	Attempt string `json:"attempt,omitempty"`
	DBPath  string `json:"db_path,omitempty"`
}
