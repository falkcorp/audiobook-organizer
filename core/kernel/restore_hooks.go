// file: core/kernel/restore_hooks.go
// version: 1.5.0
// guid: b84e1f27-6c93-4d05-8a7e-2f1d9c3b5e60
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"maps"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/authstore"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/rbacstore"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// This file is 06 P4d step 3 (05-backup §5B.3.2 RestoreHook, §5B.3.4 steps
// f-h, §5B.3.6 "Boot re-apply" (4)-(5)): the restore hooks of auth, rbac and
// audit, run in that order with one RestoreContext, then restore_hooked,
// served_boot, backup.location.json and the restore journal's last edges. Like
// restore_reapply.go it is dark (nothing in boot.go, kernel.go or main.go calls
// it), takes every dependency as an input, and is driven by tests over a store
// built in the test; F4-08 wires both after the P4c stores exist.
//
// Everything here is sequential: the hooks are ordered (auth's counts reach
// audit's record), each one is a single pass of its owner's restore, and a
// restore runs once.

// previousView is a read-only view of the pre-swap store
// (storage.Admin.OpenReadOnly's *storage.ReadOnlyDB): the hooks open their
// owner's keyspace through it, and every write through it fails.
type previousView interface {
	Keyspace(owner storage.Owner) (storage.Keyspace, error)
	Close() error
}

// hookEnv is what one run of the hooks hands the builder of the hook list.
type hookEnv struct {
	// Journal receives the lines the hooks append, each stamped with the
	// restore line's seq so a later boot can tell the lines of an aborted
	// restore (§5B.3.6 (3), DATA8-7).
	Journal journal.RevocationJournal
	// RestoreSeq is the seq of the restore line the run belongs to (0 when the
	// journal could not take the line). It is reported, in audit's records and the
	// log, and keys nothing: a journal's seqs start again after a reset or a host
	// loss, so an archive can carry a record made under a seq a later restore also
	// gets.
	RestoreSeq uint64
	// RestoreID is the restore intent's ID when this run belongs to a restore the
	// live host staged: minted by restoreswap.NewID (crypto/rand) and kept in the
	// restore journal under the state directory, never in the store, so no archive
	// can carry it. Empty for a replaced or external store and for any run that has
	// no restore of its own.
	RestoreID string
	// HostRun is the ID of this run when it has no restore intent (RestoreID
	// empty): a replaced or external store. It is minted at P4d step 2 by
	// journal.EnsureHookRun from crypto/rand and kept in the state directory
	// (restore_hook_run.json), never in the store, so no archive can carry it, and
	// removed once the run has finished. Empty for a run with a RestoreID.
	HostRun string
	// Previous is the pre-swap store; nil when there was none (host loss, an
	// external replacement, a HooksOwed run).
	Previous previousView
	// Report collects what the hooks notice beyond their counts.
	Report *hookReport
}

// hookReport is what the hooks noticed: the health tokens
// restore_last_admin_kept and restore_admins_review (registered by F4-08).
type hookReport struct {
	KeptAdmins     []string
	AdminsToReview []string
}

// add records what a hook noticed. The hooks run one after another, so it
// needs no lock.
func (r *hookReport) add(kept, review []string) {
	r.KeptAdmins = append(r.KeptAdmins, kept...)
	r.AdminsToReview = append(r.AdminsToReview, review...)
}

// restoreHooksInput is everything runRestoreHooks reads.
type restoreHooksInput struct {
	Journal  *journal.Component
	KS       storage.Keyspace // c/backup/
	Install  string
	Identity storage.StoreIdentity
	// StateDir is the state directory: where restore_hook_run.json, the ID of a
	// run with no restore intent, lives. Required.
	StateDir string
	// Result is reapplyRevocations' result for this boot.
	Result reapplyResult
	// Restore is the restore boot; nil for a replaced store, whose hooks run
	// with journal.KindExternal.
	Restore     *restoreBoot
	SwappedBack func(restoreID string) bool
	// OpenPrevious opens the pre-swap store directory read-only.
	OpenPrevious func(ctx context.Context, dir string) (previousView, error)
	// Hooks builds the hook list, in the order auth, rbac, audit.
	Hooks func(env hookEnv) ([]journal.RestoreHook, error)
	Clock func() time.Time
	Log   *slog.Logger
	// FS is the filesystem of the journal's files (reapplyInput.FS, which step 3
	// takes from step 2's input); nil is the real one.
	FS journal.FS
}

func (in restoreHooksInput) now() time.Time {
	if in.Clock != nil {
		return in.Clock().UTC()
	}
	return time.Now().UTC()
}

func (in restoreHooksInput) log() *slog.Logger {
	if in.Log != nil {
		return in.Log
	}
	return slog.Default()
}

// restoreHooksResult is what step 3 did.
type restoreHooksResult struct {
	// Ran is true when the hooks ran this boot.
	Ran    bool
	Counts map[string]int
	Report hookReport
}

// runRestoreHooks is P4d step 3. A plain boot of the same store, or of a
// relocated one, has nothing to run and returns at once; a restore boot, a
// replaced store, and a boot that finds the restore line of an earlier boot
// without its c/backup/restore_hooked/<seq> record run the hooks, journal that
// they completed, and write the markers.
func runRestoreHooks(ctx context.Context, in restoreHooksInput) (res restoreHooksResult, err error) {
	if in.Journal == nil || in.Hooks == nil || in.StateDir == "" {
		return res, errors.New("kernel: the restore hooks are missing their inputs")
	}
	r := in.Result
	restoring := in.Restore != nil
	owed := r.Owed
	if restoring {
		owed = nil
	}
	if !restoring && len(owed) == 0 && !(r.Class == classReplaced && r.Deferred) {
		// No run is owed, so a run ID a crash or a build that does not know it left
		// behind belongs to none: it goes before any later run could find it.
		return res, in.clearHostRun(ctx)
	}
	if !restoring && r.HostRun == "" {
		return res, errors.New("kernel: the restore hooks run with no restore of their own and no run ID (step 2 mints it)")
	}

	done := false
	if r.RestoreLine != 0 {
		if done, err = journal.Hooked(ctx, in.KS, r.RestoreLine); err != nil {
			return res, fmt.Errorf("kernel: read restore_hooked: %w", err)
		}
	}
	if !done {
		if res.Counts, err = in.run(ctx, &res.Report); err != nil {
			return res, err
		}
		res.Ran = true
		// The report is the source of the restore health tokens, which outlive the
		// boot: it is recorded before the record that says the hooks completed, so
		// a crash between the two re-runs the hooks and records it again.
		rep := journal.RestoreReport{Kept: res.Report.KeptAdmins, Review: res.Report.AdminsToReview}
		if err := bootseam.Labelled(bootseam.From(ctx), labelHooksReport, func() error { return journal.PutRestoreReport(ctx, in.KS, rep, in.now()) }); err != nil {
			return res, fmt.Errorf("kernel: record the restore hooks' report: %w", err)
		}
		// restore_hooked is the record that the hooks completed, whatever the
		// restore journal says: a crash before it re-runs them next boot.
		for _, l := range in.hookedLines() {
			if err := bootseam.Labelled(bootseam.From(ctx), labelHooksMarkHooked, func() error { return journal.MarkHooked(ctx, in.KS, l, in.now(), res.Counts) }); err != nil {
				return res, err
			}
		}
		// A replaced store whose restore line could not be journaled held its
		// head back (reapplyResult.Held): the completed hooks are the record that
		// now says it, so the head moves here and never before.
		if r.Held.Seq > 0 {
			if err := bootseam.Labelled(bootseam.From(ctx), labelHooksHeldHead, func() error { return journal.CommitHead(ctx, in.KS, r.Held) }); err != nil {
				return res, fmt.Errorf("kernel: commit the held journal head: %w", err)
			}
		}
		// The hold the boot put on the journal (reapplyRevocations) ends with the
		// hooks: a line appended from here on stages a head that no longer passes
		// anything left unapplied. A line this build cannot apply is not one of
		// those: it is queued, and the journal takes lines regardless.
		in.Journal.Resume()
	}

	if restoring && in.Restore.Steps != nil {
		if err := in.Restore.Steps.HooksDone(ctx, in.Restore.ID); err != nil {
			return res, fmt.Errorf("kernel: journal hooks_done: %w", err)
		}
	}
	if r.Deferred {
		if err := in.writeMarkers(ctx); err != nil {
			return res, err
		}
	}
	if restoring && in.Restore.Steps != nil {
		if err := in.Restore.Steps.MarkerWritten(ctx, in.Restore.ID); err != nil {
			return res, fmt.Errorf("kernel: journal marker_written: %w", err)
		}
		if err := in.Restore.Steps.HooksApplied(ctx, in.Restore.ID); err != nil {
			return res, fmt.Errorf("kernel: journal applied: %w", err)
		}
	}
	// The run is over, so its ID goes: the next run with no restore intent is a
	// different restore. This is last on purpose; cleared before the markers, a
	// crash would re-run the hooks under a new ID and write audit's records twice.
	// A crash between the markers and here leaves the file behind, and nothing
	// takes it for a key: the next boot is a plain one that clears it
	// (clearHostRun, at the top), and a store swapped in before any boot finds the
	// markers changed what the file was minted for (journal.EnsureHookRun's
	// binding).
	if err := in.clearHostRun(ctx); err != nil {
		return res, err
	}
	in.Journal.SetFrozen(false)
	return res, nil
}

// clearHostRun removes the run ID of a run with no restore intent, and the row of
// the same name that a build before F4-08 B10m kept in the store: no run is
// using either. A boot with neither writes nothing.
func (in restoreHooksInput) clearHostRun(ctx context.Context) error {
	if err := journal.ClearHookRun(in.StateDir, journal.WithFS(in.FS)); err != nil {
		return fmt.Errorf("kernel: clear the restore hooks' run ID: %w", err)
	}
	if err := bootseam.Labelled(bootseam.From(ctx), labelHooksRunClear, func() error { return journal.ClearLegacyHookRun(ctx, in.KS) }); err != nil {
		return fmt.Errorf("kernel: clear the restore hooks' legacy run row: %w", err)
	}
	return nil
}

// hookedLines are the restore lines whose restore_hooked record the run writes:
// this boot's restore line, and every unfinished external line of an earlier
// boot (one run of the hooks covers them all).
func (in restoreHooksInput) hookedLines() []uint64 {
	var out []uint64
	if in.Result.RestoreLine != 0 {
		out = append(out, in.Result.RestoreLine)
	}
	if in.Restore == nil {
		for _, l := range in.Result.Owed {
			if !slices.Contains(out, l.Seq) {
				out = append(out, l.Seq)
			}
		}
	}
	return out
}

// writeMarkers is step h's marker write.
func (in restoreHooksInput) writeMarkers(ctx context.Context) error {
	return writeStoreMarkers(ctx, in.Journal, in.FS, in.Result.Path, in.Identity, in.Result.Moved, in.log())
}

// run builds the RestoreContext and runs the hooks in order.
func (in restoreHooksInput) run(ctx context.Context, report *hookReport) (map[string]int, error) {
	var prev previousView
	if rb := in.Restore; rb != nil && rb.PreSwapDir != "" {
		if in.OpenPrevious == nil {
			return nil, errors.New("kernel: the restore hooks cannot open the pre-swap store")
		}
		p, err := in.OpenPrevious(ctx, rb.PreSwapDir)
		if err != nil {
			return nil, fmt.Errorf("kernel: open the pre-swap store: %w", err)
		}
		defer func() {
			// The pre-swap store was only read: a failed close loses nothing, but is not silent.
			if err := p.Close(); err != nil {
				in.log().Warn("kernel: the pre-swap store did not close cleanly", "err", err)
			}
		}()
		prev = p
	}
	rc, err := in.context(ctx, prev)
	if err != nil {
		return nil, err
	}
	hooks, err := in.Hooks(hookEnv{Journal: stampRestore{in.Journal, in.Result.RestoreLine}, RestoreSeq: in.Result.RestoreLine, RestoreID: in.restoreID(), HostRun: in.hostRun(), Previous: prev, Report: report})
	if err != nil {
		return nil, fmt.Errorf("kernel: build the restore hooks: %w", err)
	}
	counts := map[string]int{}
	for i, h := range hooks {
		rc.Counts = maps.Clone(counts)
		got, err := h(ctx, rc)
		if err != nil {
			return nil, fmt.Errorf("kernel: restore hook %d of %d: %w", i+1, len(hooks), err)
		}
		maps.Copy(counts, got)
	}
	return counts, nil
}

// context is the one RestoreContext every hook receives.
func (in restoreHooksInput) context(ctx context.Context, prev previousView) (journal.RestoreContext, error) {
	r := in.Result
	rc := journal.RestoreContext{Kind: journal.KindExternal, Boundary: r.RestoreBoundary}
	if rb := in.Restore; rb != nil {
		rc.Kind, rc.ArchiveID, rc.Foreign = rb.Kind, rb.ArchiveID, rb.Foreign
		rc.AuditHighWater, rc.Journal = rb.AuditHighWater, rb.Records
	}
	if prev != nil {
		rc.Previous = func(owner storage.Owner) (storage.Reader, error) { return prev.Keyspace(owner) }
		revs, err := in.coverage(ctx, prev)
		if err != nil {
			return rc, err
		}
		rc.Revocations = revs
	}
	return rc, nil
}

// coverage is row "Coverage at restore": the verified journal lines after the
// boundary when the journal covers the pre-swap store, nil otherwise (foreign,
// host loss, external, broken chain, gap, other install: the hooks then fail
// closed). The lines a hook of an aborted restore appended are left out
// (DATA8-7): the store they narrowed was swapped away.
func (in restoreHooksInput) coverage(ctx context.Context, prev previousView) (iter.Seq[journal.Revocation], error) {
	rb := in.Restore
	if rb == nil || rb.Foreign || in.Result.Reset {
		return nil, nil
	}
	ks, err := prev.Keyspace(journal.Owner)
	if err != nil {
		in.log().Warn("kernel: the pre-swap store has no c/backup/: the restore gets no journal coverage", "err", err)
		return nil, nil
	}
	head, err := journal.ReadHead(ctx, ks)
	if err != nil {
		return nil, fmt.Errorf("kernel: read the pre-swap journal_head: %w", err)
	}
	gap, err := journal.OpenGaps(ctx, ks)
	if err != nil {
		return nil, fmt.Errorf("kernel: read the pre-swap journal_gap records: %w", err)
	}
	cov := in.Result.Chain.Cover(in.Install, in.Result.RestoreBoundary, head, gap)
	if !cov.OK {
		in.log().Warn("kernel: the revocation journal does not cover this restore", "why", cov.Why)
		return nil, nil
	}
	aborted := in.Result.Chain.AbortedRestores(in.SwappedBack)
	var lines []journal.Revocation
	for _, l := range cov.Lines {
		if l.RestoreSeq != 0 && aborted[l.RestoreSeq] {
			continue
		}
		lines = append(lines, l.Revocation)
	}
	return slices.Values(lines), nil
}

// stampRestore sets RestoreSeq on a line a hook appends when the hook left it
// zero; Gap passes through.
type stampRestore struct {
	journal.RevocationJournal
	seq uint64
}

func (s stampRestore) Append(ctx context.Context, b *storage.Batch, r journal.Revocation) error {
	if r.RestoreSeq == 0 {
		r.RestoreSeq = s.seq
	}
	return s.RevocationJournal.Append(ctx, b, r)
}

// restoreAborted appends the restore_aborted line of the restore whose boot
// swapped back (05-backup §5B.3.4 step x): the line names the restore line the
// restore journal ID restoreID owns, so no later boot resumes it or reads it
// as evidence. No restore line, or none this journal can take, is nothing to
// close and is not an error: the swap back already restored the store and the
// location file.
func restoreAborted(ctx context.Context, comp *journal.Component, keys journal.Keys, restoreID string, at time.Time, fsOpts ...journal.FSOption) error {
	path := comp.Path()
	if path == "" {
		return nil
	}
	chain, err := journal.ReadFile(path, keys, fsOpts...)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil // no journal, so no restore line to close
	case err != nil:
		// The caller (swapFailed) learns that the line could not be closed; its
		// restore journal's swapped_back record is then the only evidence.
		return fmt.Errorf("kernel: read the revocation journal to close restore %s: %w", restoreID, err)
	}
	aborted := chain.AbortedRestores(nil)
	for _, l := range chain.Lines {
		info, ok := l.RestoreInfo()
		if !ok || info.RestoreID != restoreID || aborted[l.Seq] {
			continue
		}
		if _, err := comp.AppendLine(ctx, journal.AbortedLine(l.Seq, at)); err != nil {
			return fmt.Errorf("kernel: append restore_aborted for seq %d: %w", l.Seq, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// The adapters over the owners' package functions.

// hookStores are the live P4c handles the adapters bind.
type hookStores struct {
	Auth  *authstore.Store
	Roles *rbacstore.Store
	Audit *audit.Store
	// Journal is the journal component: where the fail-closed fallback and the
	// last-admin restriction journal what they derive.
	Journal journal.RevocationJournal
	Clock   func() time.Time
	Log     *slog.Logger
}

// reapplyApplyFor binds step 2's appliers to the live P4c handles: the tail
// re-apply is auth's and rbac's StageReapply into one batch with the head, and
// the fail-closed fallback is auth's PostRestore with no coverage and no
// pre-swap store, built from the same auth.RestoreDeps the hooks use so the two
// paths cannot drift.
func reapplyApplyFor(s hookStores) reapplyApply {
	authDeps := auth.RestoreDeps{Store: s.Auth, NewBatch: s.Auth.NewBatch, Revocations: s.Journal, Journaled: journaledOf(s.Journal), Clock: s.Clock, Log: s.Log}
	roleDeps := rbac.RestoreDeps{Store: s.Roles, NewBatch: s.Roles.NewBatch, Users: &restoreUsers{store: s.Auth, now: s.Clock},
		Revocations: s.Journal, Clock: s.Clock, Log: s.Log}
	return reapplyApply{
		NewBatch: s.Auth.NewBatch,
		Auth: func(ctx context.Context, b *storage.Batch, lines []journal.Revocation) error {
			_, err := auth.StageReapply(ctx, authDeps, b, lines)
			return err
		},
		RBAC: func(ctx context.Context, b *storage.Batch, lines []journal.Revocation) ([]string, error) {
			rep, err := rbac.StageReapply(ctx, roleDeps, b, lines)
			return rep.KeptAdmins, err
		},
		RestrictLastAdmins: func(ctx context.Context, ids []string, runKey string) error {
			d := authDeps
			d.RunKey = runKey
			_, err := auth.RestrictLastAdmins(ctx, d, userIDs(ids))
			return err
		},
		FailClosed: func(ctx context.Context) (map[string]int, error) {
			c, err := auth.PostRestore(ctx, authDeps, journal.RestoreContext{Kind: journal.KindExternal})
			return c, err
		},
	}
}

// journaledOf is how auth asks the journal whether it already holds a line
// (journal.Component.Journaled); nil for a journal that cannot say, which
// leaves auth appending every line it owes.
func journaledOf(j journal.RevocationJournal) func(journal.Revocation) (bool, error) {
	if c, ok := j.(interface {
		Journaled(journal.Revocation) (bool, error)
	}); ok {
		return c.Journaled
	}
	return nil
}

// restoreID is the ID of the restore the live host staged for this run, "" when
// there is none (a replaced or external store).
func (in restoreHooksInput) restoreID() string {
	if in.Restore == nil {
		return ""
	}
	return in.Restore.ID
}

// hostRun is the ID of this run when it has no restore intent: step 2's, minted
// before anything of the run was written. Empty for a restore boot.
func (in restoreHooksInput) hostRun() string {
	if in.Restore != nil {
		return ""
	}
	return in.Result.HostRun
}

// hookRunKey is the key every hook stamps on its restore run: auth's lines of the
// last-admin restriction, auth's marker that makes the credential-epoch bump
// idempotent, and audit's records. A run that matches a stored key SKIPS work (the
// bump, a journal line, a record), so the key must be something a restored archive
// cannot forge. Neither the restore line's seq (journal seqs start again after a
// journal reset or a host loss, so an archive can carry a marker for a seq a later
// restore also gets) nor an ID stored in the restorable keyspace (an archive taken
// mid-run hands it back) is that. Two things are: the restore intent's ID, and the
// run ID of a run with no intent. Each is minted on this host from crypto/rand and
// kept under the state directory outside the store.
//
// "" is a run with neither. Auth takes it as "never skip" (it bumps and journals
// every time, and a double bump only narrows what a session may do); audit has no
// such fallback, so its adapter refuses to run on it.
func hookRunKey(env hookEnv) string {
	switch {
	case env.RestoreID != "":
		return fmt.Sprintf("restore:%s:seq:%d", env.RestoreID, env.RestoreSeq)
	case env.HostRun != "":
		return env.HostRun
	}
	return ""
}

// userIDs is ids as auth's user IDs.
func userIDs(ids []string) []auth.UserID {
	users := make([]auth.UserID, len(ids))
	for i, id := range ids {
		users[i] = auth.UserID(id)
	}
	return users
}

// restoreHookList is the Hooks builder over live P4c handles: auth's
// PostRestore, then rbac's, then audit's.
func restoreHookList(s hookStores) func(env hookEnv) ([]journal.RestoreHook, error) {
	return func(env hookEnv) ([]journal.RestoreHook, error) {
		if s.Auth == nil || s.Roles == nil || s.Audit == nil {
			return nil, errors.New("the auth, rbac and audit stores are all required")
		}
		authDeps := auth.RestoreDeps{Store: s.Auth, NewBatch: s.Auth.NewBatch, Revocations: env.Journal, Journaled: journaledOf(s.Journal),
			RunKey: hookRunKey(env), Clock: s.Clock, Log: s.Log}
		roleDeps := rbac.RestoreDeps{
			Store: s.Roles, NewBatch: s.Roles.NewBatch, Users: &restoreUsers{store: s.Auth, now: s.Clock},
			Revocations: env.Journal, Clock: s.Clock, Log: s.Log,
			RestrictLastAdmins: func(ctx context.Context, ids []string) (map[string]int, error) {
				c, err := auth.RestrictLastAdmins(ctx, authDeps, userIDs(ids))
				return c, err
			},
		}
		if env.Previous != nil {
			aks, err := env.Previous.Keyspace(authstore.Owner)
			if err != nil {
				return nil, fmt.Errorf("the pre-swap identity keyspace: %w", err)
			}
			pa, err := authstore.New(aks, authstore.Options{})
			if err != nil {
				return nil, err
			}
			rks, err := env.Previous.Keyspace(rbacstore.Owner)
			if err != nil {
				return nil, fmt.Errorf("the pre-swap role keyspace: %w", err)
			}
			pr, err := rbacstore.New(rks, rbacstore.Options{})
			if err != nil {
				return nil, err
			}
			authDeps.Previous = pa
			roleDeps.Previous, roleDeps.PreviousUsers = pr, &restoreUsers{store: pa, now: s.Clock, readOnly: true}
		}
		return []journal.RestoreHook{
			func(ctx context.Context, rc journal.RestoreContext) (map[string]int, error) {
				defer bootseam.Region(ctx, labelHookAuth)()
				c, err := auth.PostRestore(ctx, authDeps, rc)
				return c, err
			},
			func(ctx context.Context, rc journal.RestoreContext) (map[string]int, error) {
				defer bootseam.Region(ctx, labelHookRBAC)()
				rep, err := rbac.PostRestore(ctx, roleDeps, rc)
				if err != nil {
					return nil, err
				}
				env.Report.add(rep.KeptAdmins, rep.AdminsToReview)
				return rep.Counts, nil
			},
			func(ctx context.Context, rc journal.RestoreContext) (map[string]int, error) {
				defer bootseam.Region(ctx, labelHookAudit)()
				// Audit keys its records on the run key and falls back to the restore
				// line's seq without one, which a restored archive can repeat: so a
				// run with no live-host key writes nothing.
				key := hookRunKey(env)
				if key == "" {
					return nil, errors.New("kernel: the audit restore hook has no live-host run key (no restore ID and no run ID)")
				}
				return audit.RestoreHook(audit.RestoreDeps{Store: s.Audit, RestoreSeq: env.RestoreSeq, RunKey: key, Log: s.Log})(ctx, rc)
			},
		}, nil
	}
}

// restoreUsers is rbac.RestoreUsers over auth's user records: rbac cannot
// import auth. ScanUsers remembers the version each user had when scanned, and
// StageRoles stages the new role list with that version, so a user who changed
// in between makes the batch fail with storage.ErrConflict (the hook scans
// again on each attempt).
type restoreUsers struct {
	store    auth.Store
	now      func() time.Time
	readOnly bool

	mu   sync.Mutex
	seen map[string]*auth.User
}

var _ rbac.RestoreUsers = (*restoreUsers)(nil)

func (u *restoreUsers) ScanUsers(ctx context.Context) iter.Seq2[rbac.RestoreUser, error] {
	return func(yield func(rbac.RestoreUser, error) bool) {
		seen := map[string]*auth.User{}
		defer func() {
			u.mu.Lock()
			u.seen = seen
			u.mu.Unlock()
		}()
		for usr, err := range u.store.ScanUsers(ctx) {
			if err != nil {
				if errors.Is(err, auth.ErrUndecodableRecord) {
					continue // auth refuses such a record anyway; there is nothing to restrict in it
				}
				yield(rbac.RestoreUser{}, err)
				return
			}
			seen[string(usr.ID)] = usr
			ru := rbac.RestoreUser{ID: string(usr.ID), Active: usr.Status == auth.StatusActive, Roles: slices.Clone(usr.Roles)}
			if !yield(ru, nil) {
				return
			}
		}
	}
}

func (u *restoreUsers) StageRoles(_ context.Context, b *storage.Batch, id string, roles []rbac.RoleName) error {
	if u.readOnly {
		return storage.ErrReadOnly
	}
	u.mu.Lock()
	seen := u.seen[id]
	u.mu.Unlock()
	if seen == nil {
		return fmt.Errorf("kernel: user %s was not scanned", id)
	}
	up := *seen
	up.Roles = slices.Clone(roles)
	up.Version = seen.Version + 1
	if u.now != nil {
		up.UpdatedAt = u.now().UTC()
	} else {
		up.UpdatedAt = time.Now().UTC()
	}
	return u.store.PutUser(b, &up)
}
