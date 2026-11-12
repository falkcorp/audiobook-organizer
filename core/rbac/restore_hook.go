// file: core/rbac/restore_hook.go
// version: 1.1.0
// guid: 29379f80-62fb-4914-a76a-8b10fd95585b
// last-edited: 2026-11-12

package rbac

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
)

// rbac's post-restore hook and the boot's journal tail re-apply (05-rbac
// §7.1 "Restore hooks (SEC-17)", 05-backup §5B.3.6 "Coverage at restore" and
// "Boot re-apply" (3)). Coined by F4-11. Nothing calls this yet (dark): the
// kernel's P4d step wraps PostRestore in a journal.RestoreHook and its boot
// re-apply calls StageReapply.
//
// The security property: a role assignment that was revoked, a role that was
// narrowed or deleted and an admin that was demoted never come back through a
// restore. Both entry points only ever remove authority, with one deliberate
// exception: when removing it would leave no active core:admin holder, the
// admin-granting state is kept and the accounts that hold it are restricted
// (SEC3-15, SEC4-12), because a lockout is worse than a stale admin.
//
// Concurrency (CLAUDE.md "ordered / exclusive access"): the whole pass is
// sequential on purpose and must not be parallelised. The journal lines are
// applied in sequence order, each once, and the last-admin rule judges the
// plan as a whole; two workers applying lines to the same role or user would
// race on the plan and could each decide the other still leaves an admin. The
// work is one scan of the roles and one of the users, in memory, committed in
// one batch, so there is no per-item I/O to spread over cores.

// RestoreUser is one user as the hook sees it.
type RestoreUser struct {
	ID     string
	Active bool // status active: the user can sign in
	Roles  []RoleName
}

// RestoreUsers is the hook's view of auth's user records (rbac cannot import
// auth). The kernel implements it over auth's store.
type RestoreUsers interface {
	// ScanUsers yields every user. An undecodable user record is the
	// implementation's to skip (auth refuses such a record anyway); an error
	// it yields ends the scan and fails the hook.
	ScanUsers(ctx context.Context) iter.Seq2[RestoreUser, error]
	// StageRoles stages id's role list (replacing it) into b with an Expect
	// on the user version ScanUsers returned for id, so b.Commit fails with
	// storage.ErrConflict when the user changed since it was scanned. The
	// version is the one observed at scan time, not one read again here: a
	// re-read would let a change between scan and stage be overwritten
	// unseen. Every attempt of the hook scans again.
	StageRoles(ctx context.Context, b *storage.Batch, id string, roles []RoleName) error
}

// RestoreDeps are the collaborators of PostRestore and StageReapply (coined
// by F4-11).
type RestoreDeps struct {
	Store         Store                     // the live (restored) role store
	NewBatch      func() *storage.Batch     // a batch over the role keyspace, as Deps.NewBatch
	Users         RestoreUsers              // live users
	Previous      Store                     // read-only pre-swap role store (writes fail ErrReadOnly); nil when none
	PreviousUsers RestoreUsers              // read-only pre-swap users; nil exactly when Previous is nil
	Revocations   journal.RevocationJournal // derived restrictions are journaled, as auth does
	// RestrictLastAdmins restricts every user in userIDs as 05-authn §4.14's
	// restricted last admin, in one batch (the kernel closes it over
	// auth.RestrictLastAdmins; rbac cannot import auth). It is idempotent and
	// returns auth's counts. One call for every holder a kept line reports, never
	// one per holder: a commit each leaves a point at which the first is restricted
	// and the rest are not.
	RestrictLastAdmins func(ctx context.Context, userIDs []string) (map[string]int, error)
	Clock              func() time.Time // nil = time.Now
	Log                *slog.Logger     // nil discards
}

// The count families (the keys of RestoreReport.Counts). A family with a zero
// count is absent from the map. The counts RestrictLastAdmins returns are
// added under their own keys.
const (
	CountRolesDeleted    = "roles_deleted"
	CountRolesNarrowed   = "roles_narrowed"
	CountRolesUnassigned = "roles_unassigned" // user-role assignments removed
	CountLastAdminKept   = "last_admin_kept"  // admin-granting state kept to avoid a lockout
)

// RestoreReport is what the hook did.
type RestoreReport struct {
	Counts         map[string]int
	KeptAdmins     []string // user IDs restricted because re-applying a demotion would have removed the last active core:admin holder (health: restore_last_admin_kept)
	AdminsToReview []string // when Previous == nil: every active holder of an admin-granting role after the hook (health: restore_admins_review)
}

// restoreRetries bounds the retries of one commit that failed on
// storage.ErrConflict, as auth's hook does.
const restoreRetries = 4

func (d RestoreDeps) now() time.Time {
	if d.Clock != nil {
		return d.Clock().UTC()
	}
	return time.Now().UTC()
}

func (d RestoreDeps) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.New(slog.DiscardHandler)
}

// missingDeps names the collaborators StageReapply and PostRestore both need.
func (d RestoreDeps) missingDeps() []string {
	var missing []string
	if d.Store == nil {
		missing = append(missing, "Store")
	}
	if d.Users == nil {
		missing = append(missing, "Users")
	}
	return missing
}

// checkReapply reports the missing collaborators StageReapply needs.
func (d RestoreDeps) checkReapply() error {
	if missing := d.missingDeps(); len(missing) > 0 {
		return fmt.Errorf("rbac: restore: missing deps %v", missing)
	}
	return nil
}

// checkHook reports the missing or inconsistent collaborators PostRestore
// needs.
func (d RestoreDeps) checkHook() error {
	missing := d.missingDeps()
	if d.NewBatch == nil {
		missing = append(missing, "NewBatch")
	}
	if d.Revocations == nil {
		missing = append(missing, "Revocations")
	}
	if d.RestrictLastAdmins == nil {
		missing = append(missing, "RestrictLastAdmins")
	}
	if len(missing) > 0 {
		return fmt.Errorf("rbac: restore: missing deps %v", missing)
	}
	if (d.Previous == nil) != (d.PreviousUsers == nil) {
		return errors.New("rbac: restore: Previous and PreviousUsers must both be set or both be nil")
	}
	return nil
}

// ---------------------------------------------------------------------------
// The plan: the roles and users as read, and the removals decided on them.
// Nothing is written until stage.

// planUser is one user's planned role list.
type planUser struct {
	active bool
	orig   []RoleName
	roles  []RoleName
}

// guard is the last-admin keep (SEC3-15, SEC4-12): while set, no change
// removes admin from a role that grants it, and no admin-granting role is
// taken from a user who held one while active.
type guard struct {
	adminRoles map[RoleName]bool // roles that granted admin before the plan
	protected  map[string]bool   // users that were active admin holders before the plan
}

// plan is the live state with the removals decided so far. orig is the state
// as read and is never changed; roles holds the planned rows (a role absent
// from it is deleted) and users the planned role lists.
type plan struct {
	orig  map[RoleName]*Role
	roles map[RoleName]*Role
	users map[string]*planUser
	ids   []string // sorted user IDs
	guard *guard   // nil: no keep
}

// load reads every role and every user of the live stores.
func (d RestoreDeps) load(ctx context.Context) (*plan, error) {
	p := &plan{orig: map[RoleName]*Role{}, roles: map[RoleName]*Role{}, users: map[string]*planUser{}}
	for r, err := range d.Store.ScanRoles(ctx) {
		if err != nil {
			if errors.Is(err, ErrUndecodableRole) {
				// rbac grants nothing for a record it cannot read, so there is
				// nothing to restrict in it.
				d.log().Warn("rbac: restore: skipping an undecodable role", "error", err)
				continue
			}
			return nil, fmt.Errorf("rbac: restore: scan roles: %w", err)
		}
		p.orig[r.Name] = r.clone()
		p.roles[r.Name] = r.clone()
	}
	users, err := scanUsers(ctx, d.Users, "")
	if err != nil {
		return nil, err
	}
	for id, u := range users {
		p.users[id] = &planUser{active: u.Active, orig: slices.Clone(u.Roles), roles: slices.Clone(u.Roles)}
	}
	p.ids = slices.Sorted(maps.Keys(p.users))
	return p, nil
}

// scanUsers reads every user of src by ID; which labels the store in errors.
func scanUsers(ctx context.Context, src RestoreUsers, which string) (map[string]RestoreUser, error) {
	out := map[string]RestoreUser{}
	for u, err := range src.ScanUsers(ctx) {
		if err != nil {
			return nil, fmt.Errorf("rbac: restore: scan %susers: %w", which, err)
		}
		if u.ID == "" {
			continue
		}
		u.Roles = slices.Clone(u.Roles)
		out[u.ID] = u
	}
	return out, nil
}

// clone returns a deep copy of the planned state; orig is shared, it never
// changes.
func (p *plan) clone() *plan {
	c := &plan{orig: p.orig, roles: make(map[RoleName]*Role, len(p.roles)), users: make(map[string]*planUser, len(p.users)), ids: p.ids, guard: p.guard}
	for n, r := range p.roles {
		c.roles[n] = r.clone()
	}
	for id, u := range p.users {
		c.users[id] = &planUser{active: u.active, orig: u.orig, roles: slices.Clone(u.roles)}
	}
	return c
}

// isAdminRole reports whether the planned role n grants core:admin.
func (p *plan) isAdminRole(n RoleName) bool {
	r := p.roles[n]
	return r != nil && GrantsAdmin(r.Permissions())
}

// activeAdmins lists the active users holding an admin-granting role, sorted.
func (p *plan) activeAdmins() []string {
	var out []string
	for _, id := range p.ids {
		u := p.users[id]
		if u.active && slices.ContainsFunc(u.roles, p.isAdminRole) {
			out = append(out, id)
		}
	}
	return out
}

// lockedRole reports a role no line or pre-swap comparison may narrow or
// delete: admin, by name even when its row is absent.
func (p *plan) lockedRole(n RoleName) bool {
	if n == RoleAdmin {
		return true
	}
	r := p.roles[n]
	return r != nil && r.Locked
}

// unassign takes role from user id; an unknown user or a role the user does
// not hold changes nothing.
func (p *plan) unassign(id string, role RoleName) {
	u := p.users[id]
	if u == nil {
		return
	}
	if g := p.guard; g != nil && g.protected[id] && g.adminRoles[role] {
		return
	}
	u.roles = slices.DeleteFunc(u.roles, func(n RoleName) bool { return n == role })
}

// deleteRole deletes role n and unassigns it from every user that holds it,
// the unassignment made even when the row is already absent, since a seed
// role is re-seeded at the next Freeze and would hand a dangling assignment
// back. A locked role is untouched.
func (p *plan) deleteRole(n RoleName) {
	if p.lockedRole(n) {
		return
	}
	if p.guard == nil || !p.guard.adminRoles[n] {
		delete(p.roles, n)
	}
	for _, id := range p.ids {
		p.unassign(id, n)
	}
}

// narrow removes from role n's effective permissions every pattern remove
// reports. A locked role and an absent one are untouched; under the guard a
// pattern that covers core:admin is never removed from a role that granted it.
func (p *plan) narrow(n RoleName, remove func(Permission) bool) {
	r := p.roles[n]
	if r == nil || p.lockedRole(n) {
		return
	}
	guarded := p.guard != nil && p.guard.adminRoles[n]
	next := slices.DeleteFunc(r.Permissions(), func(q Permission) bool {
		return remove(q) && !(guarded && Covers(q, CoreAdmin))
	})
	if slices.Equal(next, r.Permissions()) {
		return
	}
	switch r.Origin {
	case OriginSeed:
		r.SeedPermissions, r.SeedDigest = next, seedDigest(next)
	case OriginCustom:
		r.Added = next
	}
}

// applyLine applies one journal line: a line of another kind, an unknown user
// or role and a malformed reference change nothing.
func (p *plan) applyLine(l journal.Revocation) {
	switch l.Kind {
	case journal.RoleUnassigned:
		if isRef(l.Subject, "user") && isRef(l.Object, "role") {
			p.unassign(l.Subject.ID, RoleName(l.Object.ID))
		}
	case journal.RoleNarrowed:
		if isRef(l.Subject, "role") {
			gone := map[Permission]bool{}
			for _, s := range l.Detail {
				gone[Permission(s)] = true
			}
			p.narrow(RoleName(l.Subject.ID), func(q Permission) bool { return gone[q] })
		}
	case journal.RoleDeleted:
		if isRef(l.Subject, "role") {
			p.deleteRole(RoleName(l.Subject.ID))
		}
	}
}

// isRef reports a core reference of the given kind with an ID.
func isRef(r entity.Ref, kind string) bool {
	return r.Module == "core" && r.Kind == kind && r.ID != ""
}

func userRef(id string) entity.Ref { return entity.Ref{Module: "core", Kind: "user", ID: id} }

// isRoleLine reports the three kinds rbac re-applies.
func isRoleLine(k journal.Kind) bool {
	return k == journal.RoleUnassigned || k == journal.RoleNarrowed || k == journal.RoleDeleted
}

// ---------------------------------------------------------------------------
// The pre-swap pass (05-rbac §7.1: the most restrictive of both states).

// previousState is the pre-swap role rows and user role sets.
type previousState struct {
	roles map[RoleName]*Role
	users map[string]map[RoleName]bool
}

// loadPrevious reads the pre-swap stores once; they are read-only, so no
// attempt needs them again. Nil when there is no pre-swap store.
func (d RestoreDeps) loadPrevious(ctx context.Context) (*previousState, error) {
	if d.Previous == nil {
		return nil, nil
	}
	prev := &previousState{roles: map[RoleName]*Role{}, users: map[string]map[RoleName]bool{}}
	for r, err := range d.Previous.ScanRoles(ctx) {
		if err != nil {
			if errors.Is(err, ErrUndecodableRole) {
				// It cannot be named, so a live role of that name reads as
				// absent there: the more restrictive reading.
				d.log().Warn("rbac: restore: skipping an undecodable pre-swap role", "error", err)
				continue
			}
			return nil, fmt.Errorf("rbac: restore: scan pre-swap roles: %w", err)
		}
		prev.roles[r.Name] = r
	}
	users, err := scanUsers(ctx, d.PreviousUsers, "pre-swap ")
	if err != nil {
		return nil, err
	}
	for id, u := range users {
		prev.users[id] = map[RoleName]bool{}
		for _, n := range u.Roles {
			prev.users[id][n] = true
		}
	}
	return prev, nil
}

// patternCovers reports whether pattern q grants everything pattern p grants.
// Covers answers for a concrete scope only, and a role holds patterns, so a
// wildcard p needs this: "<ns>:*" covers every pattern of ns, "<ns>:<a>.*"
// covers itself and its concrete subs, a concrete pattern covers only itself.
func patternCovers(q, p Permission) bool {
	if q == p {
		return true
	}
	qp, err := ParsePattern(string(q))
	if err != nil || !qp.IsWildcard() {
		return false
	}
	pp, err := ParsePattern(string(p))
	if err != nil {
		return false
	}
	if !pp.IsWildcard() {
		return Covers(qp, pp)
	}
	return strings.HasSuffix(string(qp), ":*") && qp.Namespace() == pp.Namespace()
}

// restrictToPrevious makes the plan the most restrictive of the restored and
// the pre-swap state: a custom role the pre-swap store lacks is deleted, a
// role's permissions become those a pre-swap pattern covers, and a user
// present in both keeps only the roles the pre-swap user held. Roles first,
// users last, so a deleted role is already gone from every list. A user
// absent from the pre-swap store is left alone (auth's hook handles deleted
// users).
func (p *plan) restrictToPrevious(prev *previousState) {
	names := slices.Sorted(maps.Keys(p.roles))
	for _, n := range names {
		if _, ok := prev.roles[n]; !ok && p.roles[n].Origin == OriginCustom {
			p.deleteRole(n)
		}
	}
	for _, n := range names {
		q := prev.roles[n]
		if q == nil || p.roles[n] == nil {
			continue
		}
		held := q.Permissions()
		p.narrow(n, func(perm Permission) bool {
			return !slices.ContainsFunc(held, func(h Permission) bool { return patternCovers(h, perm) })
		})
	}
	for _, id := range p.ids {
		pu, ok := prev.users[id]
		if !ok {
			continue
		}
		for _, n := range slices.Clone(p.users[id].roles) {
			if !pu[n] {
				p.unassign(id, n)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// The diff between two plans: counts, derived journal lines and writes.

// removedAssignments are the assignments in from that to no longer has, per user.
func removedAssignments(from, to *plan, id string) []RoleName {
	var out []RoleName
	for _, n := range from.users[id].roles {
		if !slices.Contains(to.users[id].roles, n) {
			out = append(out, n)
		}
	}
	return out
}

// counts is the report's count map for the change from base to final.
func counts(base, final *plan) map[string]int {
	c := map[string]int{}
	add := func(k string, n int) {
		if n > 0 {
			c[k] += n
		}
	}
	for n, o := range base.orig {
		r, ok := final.roles[n]
		switch {
		case !ok:
			add(CountRolesDeleted, 1)
		case !slices.Equal(r.Permissions(), o.Permissions()):
			add(CountRolesNarrowed, 1)
		}
	}
	for _, id := range base.ids {
		add(CountRolesUnassigned, len(removedAssignments(base, final, id)))
	}
	return c
}

// derivedLines are the restrictions from → to made that the journal did not
// already hold: what pass 2 derived. A deleted role journals one
// role.unassigned per holder it lost, then its role.deleted.
func derivedLines(from, to *plan) []journal.Revocation {
	var out []journal.Revocation
	for _, n := range slices.Sorted(maps.Keys(from.roles)) {
		if r, ok := to.roles[n]; ok && !slices.Equal(r.Permissions(), from.roles[n].Permissions()) {
			out = append(out, journal.Revocation{
				Kind: journal.RoleNarrowed, Subject: roleRef(n),
				Detail: permStrings(minus(from.roles[n].Permissions(), r.Permissions())),
			})
		}
	}
	for _, id := range from.ids {
		for _, n := range removedAssignments(from, to, id) {
			out = append(out, journal.Revocation{Kind: journal.RoleUnassigned, Subject: userRef(id), Object: roleRef(n)})
		}
	}
	for _, n := range slices.Sorted(maps.Keys(from.roles)) {
		if _, ok := to.roles[n]; !ok {
			out = append(out, journal.Revocation{Kind: journal.RoleDeleted, Subject: roleRef(n)})
		}
	}
	return out
}

// dirty reports whether the plan changes any row.
func (p *plan) dirty() bool {
	for n, o := range p.orig {
		if r, ok := p.roles[n]; !ok || !slices.Equal(r.Permissions(), o.Permissions()) {
			return true
		}
	}
	return slices.ContainsFunc(p.ids, func(id string) bool { return !slices.Equal(p.users[id].orig, p.users[id].roles) })
}

// stageWrites stages the plan's role and user writes into b, in name order.
// A role or user that changed since it was read refuses the staging with
// ErrVersionMismatch or ErrUnknownRole; that is a conflict, so it is
// reported wrapping storage.ErrConflict and the caller's retry re-reads.
func (p *plan) stageWrites(ctx context.Context, d RestoreDeps, b *storage.Batch, now time.Time) error {
	for _, n := range slices.Sorted(maps.Keys(p.orig)) {
		o := p.orig[n]
		r, ok := p.roles[n]
		var err error
		switch {
		case !ok:
			err = d.Store.DeleteRole(b, o)
		case !slices.Equal(r.Permissions(), o.Permissions()):
			next := r.clone()
			next.Version, next.UpdatedAt = o.Version+1, now
			err = d.Store.PutRole(b, next)
		default:
			continue
		}
		if errors.Is(err, ErrVersionMismatch) || errors.Is(err, ErrUnknownRole) {
			return fmt.Errorf("%w: role %s changed since it was read: %w", storage.ErrConflict, n, err)
		}
		if err != nil {
			return fmt.Errorf("rbac: restore: stage role %s: %w", n, err)
		}
	}
	for _, id := range p.ids {
		u := p.users[id]
		if slices.Equal(u.orig, u.roles) {
			continue
		}
		if err := d.Users.StageRoles(ctx, b, id, slices.Clone(u.roles)); err != nil {
			return fmt.Errorf("rbac: restore: stage the roles of user %s: %w", id, err)
		}
	}
	return nil
}

// journalLine appends r into b before the batch commits: a failed Append
// records a gap instead and never blocks the restriction, as rbac.Service
// and auth's hook do. A gap that cannot be staged fails the hook, since the
// journal would then claim coverage it does not have.
func (d RestoreDeps) journalLine(ctx context.Context, b *storage.Batch, r journal.Revocation) error {
	r.At = d.now()
	if err := d.Revocations.Append(ctx, b, r); err != nil {
		d.log().Warn("rbac: restore: revocation journal append failed; recording a gap", "kind", string(r.Kind), "error", err)
		if gerr := d.Revocations.Gap(b, r.At); gerr != nil {
			return fmt.Errorf("rbac: restore: journal %s: append failed (%v) and the gap could not be staged: %w", r.Kind, err, gerr)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// The entry points.

// roleLines keeps the lines of the kinds rbac re-applies, in order.
func roleLines(lines iter.Seq[journal.Revocation]) []journal.Revocation {
	var out []journal.Revocation
	if lines == nil {
		return nil
	}
	for l := range lines {
		if isRoleLine(l.Kind) {
			out = append(out, l)
		}
	}
	return out
}

// PostRestore is rbac's journal.RestoreHook body (05-rbac §7.1). It
// re-applies rc.Revocations, then, when there is a pre-swap store, takes the
// most restrictive of the restored and pre-swap state and journals what that
// derived. The result is committed in one batch, retried on conflict with the
// state read again. When it would leave no active core:admin holder, the
// admin-granting state is kept and every active holder is restricted through
// one RestrictLastAdmins call after the commit (reported in KeptAdmins). It is
// idempotent: the kernel re-runs it after a crash before the restore journal
// reaches "applied", and a second run over the same inputs changes and
// journals nothing.
func PostRestore(ctx context.Context, d RestoreDeps, rc journal.RestoreContext) (RestoreReport, error) {
	if err := d.checkHook(); err != nil {
		return RestoreReport{}, err
	}
	lines := roleLines(rc.Revocations)
	prev, err := d.loadPrevious(ctx)
	if err != nil {
		return RestoreReport{}, err
	}

	var final *plan
	var rep RestoreReport
	err = storage.RetryOnConflict(ctx, restoreRetries, func(ctx context.Context) (err error) {
		base, err := d.load(ctx)
		if err != nil {
			return err
		}
		p, snap := base.run(lines, prev)
		var kept []string
		if had := base.activeAdmins(); len(had) > 0 && len(p.activeAdmins()) == 0 {
			// SEC3-15, SEC4-12: the plan would lock everyone out. Plan again
			// with the admin-granting aspects kept; every active holder of
			// the pre-plan state is restricted after the commit.
			base.guard = newGuard(base, had)
			p, snap = base.run(lines, prev)
			kept = had
		}
		b := d.NewBatch()
		if b == nil {
			return errors.New("rbac: restore: NewBatch returned no batch")
		}
		defer func() {
			if cerr := b.Close(); cerr != nil && err == nil {
				err = fmt.Errorf("rbac: restore: close batch: %w", cerr)
			}
		}()
		if p.dirty() {
			if snap != nil {
				for _, l := range derivedLines(snap, p) {
					if err := d.journalLine(ctx, b, l); err != nil {
						return err
					}
				}
			}
			if err := p.stageWrites(ctx, d, b, d.now()); err != nil {
				return err
			}
			if err := b.Commit(ctx, storage.Sync); err != nil {
				return err
			}
		}
		final = p
		rep = RestoreReport{Counts: counts(base, p), KeptAdmins: kept}
		return nil
	})
	if err != nil {
		return RestoreReport{}, err
	}

	if n := len(rep.KeptAdmins); n > 0 {
		got, err := d.RestrictLastAdmins(ctx, rep.KeptAdmins)
		if err != nil {
			return rep, fmt.Errorf("rbac: restore: restrict the kept admins %v: %w", rep.KeptAdmins, err)
		}
		for k, c := range got {
			rep.Counts[k] += c
		}
		for _, id := range rep.KeptAdmins {
			d.log().Warn("rbac: restore: an admin-granting role was kept to avoid a lockout; the account is restricted", "user", id)
		}
		rep.Counts[CountLastAdminKept] = n
	}
	if prev == nil {
		rep.AdminsToReview = final.activeAdmins()
	}
	return rep, nil
}

// run plans the journal lines, then the pre-swap pass (when prev is set) over
// a copy of p. snap is the plan between the two passes (nil without a
// pre-swap pass): what pass 2 changed is what it derived.
func (p *plan) run(lines []journal.Revocation, prev *previousState) (final, snap *plan) {
	final = p.clone()
	for _, l := range lines {
		final.applyLine(l)
	}
	if prev == nil {
		return final, nil
	}
	snap = final.clone()
	final.restrictToPrevious(prev)
	return final, snap
}

// newGuard keeps the admin-granting aspects of base for its active holders.
func newGuard(base *plan, holders []string) *guard {
	g := &guard{adminRoles: map[RoleName]bool{}, protected: map[string]bool{}}
	for n := range base.roles {
		if base.isAdminRole(n) {
			g.adminRoles[n] = true
		}
	}
	for _, id := range holders {
		g.protected[id] = true
	}
	return g
}

// StageReapply is the boot's tail re-apply on the SAME store (05-backup
// §5B.3.6 "Boot re-apply" (3), a lagging head): it stages the role kinds
// among lines into the caller's batch b. The caller owns the retry on
// storage.ErrConflict and commits b, so every call reads the state again. It
// runs no pre-swap pass, journals nothing (the lines are already journaled)
// and restricts nobody: a line whose application would remove the last active
// admin holder is skipped and its holders reported in KeptAdmins, for the
// caller to restrict. Kinds other than role.unassigned, role.narrowed and
// role.deleted are ignored. Lines apply in order, once each; nothing here may
// run concurrently (see the note at the top of this file).
func StageReapply(ctx context.Context, d RestoreDeps, b *storage.Batch, lines []journal.Revocation) (RestoreReport, error) {
	if err := d.checkReapply(); err != nil {
		return RestoreReport{}, err
	}
	if b == nil {
		return RestoreReport{}, errors.New("rbac: restore: StageReapply needs the caller's batch")
	}
	base, err := d.load(ctx)
	if err != nil {
		return RestoreReport{}, err
	}
	p := base.clone()
	kept := map[string]bool{}
	for _, l := range lines {
		if !isRoleLine(l.Kind) {
			continue
		}
		trial := p.clone()
		trial.applyLine(l)
		if had := p.activeAdmins(); len(had) > 0 && len(trial.activeAdmins()) == 0 {
			for _, id := range had {
				kept[id] = true
			}
			continue
		}
		p = trial
	}
	if p.dirty() {
		if err := p.stageWrites(ctx, d, b, d.now()); err != nil {
			return RestoreReport{}, err
		}
	}
	rep := RestoreReport{Counts: counts(base, p), KeptAdmins: slices.Sorted(maps.Keys(kept))}
	if n := len(rep.KeptAdmins); n > 0 {
		rep.Counts[CountLastAdminKept] = n
	}
	return rep, nil
}
