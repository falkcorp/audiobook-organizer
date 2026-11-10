// file: core/backup/journal/journal.go
// version: 1.0.0
// guid: 2a78173c-7f1b-4b86-8611-64a3be499999
// last-edited: 2026-11-10

// Package journal is the leaf of backup that auth and rbac import (COR2-9):
// github.com/falkcorp/mediacore/backup/journal, L4, imports entity and storage
// (with its L0 leaf storage/restorejournal) only, lands in R2 with auth and rbac. It holds the out-of-DB revocation journal (§5B.3.6),
// its file-backed implementation, the restore-hook contract and StorageSpec,
// the c/backup/ storage.Spec the kernel registers from the identity flip (COR5-6).
//
// The types below are 05-backup §5B.3.2's package block. The file-backed
// implementation (file.go) is the plain append of F4-04; the journal's
// authenticated format (the header's fields, every line's kid and mac, the
// install check), backup.location.json and c/backup/served_boot are F4-11's.
package journal

import (
	"context"
	"iter"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// Kind is what one journal line records (§5B.3.2).
type Kind string

const (
	APIKeyRevoked         Kind = "apikey.revoked"
	APIKeyDisabled        Kind = "apikey.disabled" // any status change away from active, needs_confirmation included
	UserDisabled          Kind = "user.disabled"
	UserDeleted           Kind = "user.deleted"
	CredentialEpochBumped Kind = "credential_epoch.bumped"
	PasswordChanged       Kind = "password.changed"  // change or reset (SEC2-11; DATA2-7's credential.changed)
	IdentityUnlinked      Kind = "identity.unlinked" // Detail = [provider]
	SessionRevoked        Kind = "session.revoked"
	RoleUnassigned        Kind = "role.unassigned" // Subject user, Object role
	RoleNarrowed          Kind = "role.narrowed"   // Subject role, Detail = removed permissions
	RoleDeleted           Kind = "role.deleted"    // Subject role
	Restore               Kind = "restore"         // the store was replaced; Detail = [kind, archive or source id, boundary seq]
	Gap                   Kind = "gap"             // an interval with revocations the journal may lack; Detail = [since, reason: "append_failed"]
	RestoreAborted        Kind = "restore_aborted" // a restore whose boot swapped back; Detail = [restore line seq]: that restore line is neither resumed nor evidence (DATA6-3)
)

// Revocation is one line of the revocation journal (§5B.3.6). SEC2-10: one
// Subject cannot say what a role removal removed, hence Object and Detail.
type Revocation struct {
	Seq     uint64    // assigned by Append: header line is 0, then 1, 2, ... without gaps
	At      time.Time // informational; no coverage rule reads it
	Kind    Kind
	Subject entity.Ref // e.g. {core, user, <id>}, {core, apikey, <id>}, {core, role, <id>}
	Object  entity.Ref // role.unassigned: the role taken away
	Detail  []string
	Epoch   int64 // the new credential epoch, when Kind bumps it
	// RestoreSeq is set on a line a post-restore hook appends: the seq of the
	// restore line whose hooks wrote it (0 otherwise; JSON "restore_seq"). Boot
	// re-apply skips such a line when that restore was aborted (DATA8-7).
	RestoreSeq uint64 `json:"restore_seq,omitempty"`
	Source     string // writing binary: its binary ID (app.BinaryID; R5-7, FE5-3)
}

// Head identifies the last line a store has seen: c/backup/journal_head.
type Head struct {
	Seq uint64
	MAC string
}

// ServedBoot is the content marker c/backup/served_boot = {n, nonce}
// (§5B.3.6 row "Content marker").
type ServedBoot struct {
	N     uint64 `json:"n"`
	Nonce string `json:"nonce"`
}

// RevocationJournal is what core/auth and core/rbac receive from the kernel.
type RevocationJournal interface {
	// Append writes r as one fsynced line BEFORE the caller commits the
	// revocation, and stages c/backup/journal_head = max(head, r.Seq) into b by
	// participant binding (05-storage §5S.3.2), reading the head with
	// GetForUpdate (c/backup/journal_head is Serialized), so two revocations
	// committing out of order never move the head backwards; a conflict
	// retries the revoking batch, and a duplicate line is harmless (DATA4-7).
	// On error nothing was recorded and nothing staged: the caller still
	// commits the revocation and calls Gap(b, at) — a disk problem never
	// blocks a security action.
	Append(ctx context.Context, b *storage.Batch, r Revocation) error
	Gap(b *storage.Batch, since time.Time) error
}

// RestoreKind is the hooks' view of a swap. The swap's own kind is
// restorejournal.Kind ("archive", "revert", "checkpoint", "reset", "rewrite";
// KindReset and KindRewrite, 05-storage §5S.3.15 and §5S.3.13), the intent's
// "kind" field; a reset and M8's rewrite run no restore hook, so RestoreKind
// has no reset or rewrite value (R8-1; §5B.3.4 "M8's store rewrite").
type RestoreKind uint8

const (
	RestoreKindInvalid RestoreKind = iota
	KindArchive                    // an archive
	KindRevert                     // the undo point of the last restore
	KindCheckpoint                 // a pre-migration checkpoint (05-storage §5S.3.12)
	KindExternal                   // replaced outside the staged and offline restore (a file copy, a hand-copied checkpoint), detected at boot (§5B.3.6)
)

// RestoreContext is what the kernel hands every restore hook at 06 P4d step 3.
// Hooks are idempotent: a crash before the journal reaches "applied" re-runs
// them at the next boot with the same context (DATA3-6).
type RestoreContext struct {
	ArchiveID string // backup.ArchiveID (journal cannot import backup); "" for revert and checkpoint restores
	Kind      RestoreKind
	Foreign   bool
	// Boundary is the journal seq the swapped-in store had seen (§5B.3.6):
	// min(the source's recorded journal seq, the swapped-in store's
	// journal_head); for KindExternal, the store's journal_head. AuditHighWater
	// is the manifest's (0 for revert, checkpoint and external).
	Boundary       uint64
	AuditHighWater uint64
	// Previous opens a read-only view of the pre-swap store (restore/<id>/previous/),
	// limited to the calling owner's own keyspace (audit's copy-forward is its
	// consumer); nil when there was no live store and for KindExternal. The
	// kernel opens the pre-swap directory with storage.Admin.OpenReadOnly
	// (05-storage §5S.3.10; COR7-8). For auth and rbac the kernel also builds
	// the owner's native store over the read-only DB, whose handles fail every
	// write with storage.ErrReadOnly, and hands it to the owner's hook function (auth:
	// RestoreDeps.Previous, a read-only auth.Store; rbac: a read-only
	// rbac.Store), so the nil-coverage fallback keeps every restriction the
	// pre-swap store holds (SEC5-10).
	Previous func(owner storage.Owner) (storage.Reader, error)
	// Revocations yields the verified journal lines after Boundary; nil when
	// coverage fails (§5B.3.6): foreign, host-loss, external, broken chain,
	// gap, other install.
	Revocations iter.Seq[Revocation]
	// Journal is the out-of-DB restore journal (staged, swapped, ... records).
	Journal []restorejournal.Record
	// Counts carries what earlier hooks returned (auth runs first), so audit's
	// hook can record core.restore.revoke with them.
	Counts map[string]int
}

// RestoreHook is the kernel's adapter over an owner's package function (auth's
// PostRestore, rbac's, audit's), bound to that owner's P4c handles. Hooks run
// in the order auth, rbac, audit; each returns per-family counts.
type RestoreHook func(ctx context.Context, rc RestoreContext) (counts map[string]int, err error)

// The out-of-DB restore journal restore/<id>/journal.json holds
// restorejournal.Record lines (05-storage §5S.3.15, used under its own names, SIMP8-8).
// A restore's steps: "staged", "rechecked", "previous_moved",
// "staged_moved", "sidecars", "swapped", "opened", "restore_line",
// "hooks_done", "marker_written", "applied", "failed", "swapped_back",
// "restore_aborted", "rejected", "applied_offline" (DATA7-12). Seq (staged)
// is the outgoing store's journal head seq, the recorded boundary of a later
// revert (DATA2-25); HooksOwed (step "marker_written") marks a restore a
// build without hooks (R1) applied, whose hooks the next journaling boot runs
// (§5B.3.6); the Note of step "failed" names the
// binary whose boot failed (DATA8-6).
