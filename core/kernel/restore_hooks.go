// file: core/kernel/restore_hooks.go
// version: 1.1.0
// guid: b84e1f27-6c93-4d05-8a7e-2f1d9c3b5e60
// last-edited: 2026-11-11

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
	// journal could not take the line): audit's idempotency key.
	RestoreSeq uint64
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
	if in.Journal == nil || in.Hooks == nil {
		return res, errors.New("kernel: the restore hooks are missing their inputs")
	}
	r := in.Result
	restoring := in.Restore != nil
	owed := r.Owed
	if restoring {
		owed = nil
	}
	if !restoring && len(owed) == 0 && !(r.Class == classReplaced && r.Deferred) {
		return res, nil
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
		// restore_hooked is the record that the hooks completed, whatever the
		// restore journal says: a crash before it re-runs them next boot.
		for _, l := range in.hookedLines() {
			if err := journal.MarkHooked(ctx, in.KS, l, in.now(), res.Counts); err != nil {
				return res, err
			}
		}
		// A replaced store whose restore line could not be journaled held its
		// head back (reapplyResult.Held): the completed hooks are the record that
		// now says it, so the head moves here and never before.
		if r.Held.Seq > 0 {
			if err := journal.CommitHead(ctx, in.KS, r.Held); err != nil {
				return res, fmt.Errorf("kernel: commit the held journal head: %w", err)
			}
		}
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
	in.Journal.SetFrozen(false)
	return res, nil
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
	return writeStoreMarkers(ctx, in.Journal, in.Result.Path, in.Identity, in.Result.Moved, in.log())
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
	hooks, err := in.Hooks(hookEnv{Journal: stampRestore{in.Journal, in.Result.RestoreLine}, RestoreSeq: in.Result.RestoreLine, Previous: prev, Report: report})
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
func restoreAborted(ctx context.Context, comp *journal.Component, keys journal.Keys, restoreID string, at time.Time) error {
	path := comp.Path()
	if path == "" {
		return nil
	}
	chain, err := journal.ReadFile(path, keys)
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
	authDeps := auth.RestoreDeps{Store: s.Auth, NewBatch: s.Auth.NewBatch, Revocations: s.Journal, Clock: s.Clock, Log: s.Log}
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
		RestrictLastAdmin: func(ctx context.Context, id string) error {
			_, err := auth.RestrictLastAdmin(ctx, authDeps, auth.UserID(id))
			return err
		},
		FailClosed: func(ctx context.Context) (map[string]int, error) {
			c, err := auth.PostRestore(ctx, authDeps, journal.RestoreContext{Kind: journal.KindExternal})
			return c, err
		},
	}
}

// restoreHookList is the Hooks builder over live P4c handles: auth's
// PostRestore, then rbac's, then audit's.
func restoreHookList(s hookStores) func(env hookEnv) ([]journal.RestoreHook, error) {
	return func(env hookEnv) ([]journal.RestoreHook, error) {
		if s.Auth == nil || s.Roles == nil || s.Audit == nil {
			return nil, errors.New("the auth, rbac and audit stores are all required")
		}
		authDeps := auth.RestoreDeps{Store: s.Auth, NewBatch: s.Auth.NewBatch, Revocations: env.Journal, Clock: s.Clock, Log: s.Log}
		roleDeps := rbac.RestoreDeps{
			Store: s.Roles, NewBatch: s.Roles.NewBatch, Users: &restoreUsers{store: s.Auth, now: s.Clock},
			Revocations: env.Journal, Clock: s.Clock, Log: s.Log,
			RestrictLastAdmin: func(ctx context.Context, id string) (map[string]int, error) {
				c, err := auth.RestrictLastAdmin(ctx, authDeps, auth.UserID(id))
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
				c, err := auth.PostRestore(ctx, authDeps, rc)
				return c, err
			},
			func(ctx context.Context, rc journal.RestoreContext) (map[string]int, error) {
				rep, err := rbac.PostRestore(ctx, roleDeps, rc)
				if err != nil {
					return nil, err
				}
				env.Report.add(rep.KeptAdmins, rep.AdminsToReview)
				return rep.Counts, nil
			},
			audit.RestoreHook(audit.RestoreDeps{Store: s.Audit, RestoreSeq: env.RestoreSeq, Log: s.Log}),
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
