// file: core/rbac/restore_hook_test.go
// version: 1.1.0
// guid: 1ac0d500-ed52-4d8e-8183-8705fc2a5199
// last-edited: 2026-11-12

package rbac_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/internal/rbacstore"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// ---------------------------------------------------------------------------
// Fixtures: a role store and a user store over one booted env, and the fakes
// the hook's seams are tested through.

const (
	bruce   = "bruce_wayne"
	alfred  = "alfred_pennyworth"
	gordon  = "commissioner_gordon"
	harvey  = "harvey_dent"
	trustee = rbac.RoleName("custom.wayne-trustee")
	sleuth  = rbac.RoleName("custom.gotham-detective")
	visitor = rbac.RoleName("custom.arkham-visitor")
)

var restoreNow = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

// fakeUsers is rbac.RestoreUsers over the env's user keyspace. StageRoles
// carries an Expect on the raw record its latest ScanUsers returned, so a user
// changed since the scan makes the batch's Commit fail with ErrConflict, as
// the kernel adapter's must.
type fakeUsers struct {
	t   testing.TB
	e   *env
	ids []string

	scanned map[string][]byte
	scans   int
	// onScan runs after every ScanUsers has yielded its last user, with the
	// scan's number (1-based): the seam that changes the world between the
	// hook's scan and its commit.
	onScan  func(n int)
	scanErr error
}

func (f *fakeUsers) ScanUsers(ctx context.Context) iter.Seq2[rbac.RestoreUser, error] {
	return func(yield func(rbac.RestoreUser, error) bool) {
		f.scans++
		if f.scanErr != nil {
			yield(rbac.RestoreUser{}, f.scanErr)
			return
		}
		f.scanned = map[string][]byte{}
		for _, id := range slices.Sorted(slices.Values(f.ids)) {
			raw, err := f.e.users.Get(ctx, userKey(id))
			if err != nil {
				yield(rbac.RestoreUser{}, err)
				return
			}
			var rec userRec
			if err := json.Unmarshal(raw, &rec); err != nil {
				yield(rbac.RestoreUser{}, err)
				return
			}
			f.scanned[id] = raw
			if !yield(rbac.RestoreUser{ID: id, Active: rec.Active, Roles: rec.Roles}, nil) {
				return
			}
		}
		if f.onScan != nil {
			f.onScan(f.scans)
		}
	}
}

func (f *fakeUsers) StageRoles(_ context.Context, b *storage.Batch, id string, roles []rbac.RoleName) error {
	raw, ok := f.scanned[id]
	if !ok {
		return fmt.Errorf("fakeUsers: %s was not scanned", id)
	}
	var rec userRec
	if err := json.Unmarshal(raw, &rec); err != nil {
		return err
	}
	rec.Roles = slices.Clone(roles)
	out, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	b.Bind(f.e.users)
	if err := b.Expect(f.e.users, userKey(id), raw); err != nil {
		return err
	}
	return b.In(f.e.users).Set(userKey(id), out)
}

// world is one role store plus one user store.
type world struct {
	t     testing.TB
	e     *env
	store *rbacstore.Store
	users *fakeUsers
}

func newWorld(t testing.TB) *world {
	t.Helper()
	e := newEnv(t)
	return &world{t: t, e: e, store: e.store(t, nil), users: &fakeUsers{t: t, e: e}}
}

// seedRole puts a seed role row.
func (w *world) seedRole(name rbac.RoleName, locked bool, perms ...rbac.Permission) {
	w.t.Helper()
	w.put(rbac.Role{Name: name, Title: string(name), Origin: rbac.OriginSeed, SeedPermissions: perms, SeedDigest: "digest-" + string(name), Locked: locked})
}

// customRole puts a custom role row.
func (w *world) customRole(name rbac.RoleName, perms ...rbac.Permission) {
	w.t.Helper()
	w.put(rbac.Role{Name: name, Title: string(name), Origin: rbac.OriginCustom, Added: perms})
}

// put writes a new role at version 1.
func (w *world) put(r rbac.Role) {
	w.t.Helper()
	r.Version = 1
	b := w.store.NewBatch()
	defer func() {
		if err := b.Close(); err != nil {
			w.t.Errorf("close: %v", err)
		}
	}()
	if err := w.store.PutRole(b, &r); err != nil {
		w.t.Fatalf("PutRole(%s): %v", r.Name, err)
	}
	if err := b.Commit(context.Background(), storage.Sync); err != nil {
		w.t.Fatalf("commit role %s: %v", r.Name, err)
	}
}

// update rewrites a stored role the way a concurrent writer would.
func (w *world) update(name rbac.RoleName, f func(r *rbac.Role)) {
	w.t.Helper()
	cur, err := w.store.GetRole(context.Background(), name)
	if err != nil {
		w.t.Fatal(err)
	}
	f(cur)
	cur.Version++
	b := w.store.NewBatch()
	defer func() {
		if err := b.Close(); err != nil {
			w.t.Errorf("close: %v", err)
		}
	}()
	if err := w.store.PutRole(b, cur); err != nil {
		w.t.Fatal(err)
	}
	if err := b.Commit(context.Background(), storage.Sync); err != nil {
		w.t.Fatal(err)
	}
}

// user writes a user record, as auth would.
func (w *world) user(id string, active bool, roles ...rbac.RoleName) {
	w.t.Helper()
	ctx := context.Background()
	if !slices.Contains(w.users.ids, id) {
		w.users.ids = append(w.users.ids, id)
	}
	raw, err := json.Marshal(userRec{Roles: roles, Active: active})
	if err != nil {
		w.t.Fatal(err)
	}
	b := w.e.users.NewBatch()
	defer func() {
		if err := b.Close(); err != nil {
			w.t.Errorf("close: %v", err)
		}
	}()
	if _, err := b.GetForUpdate(ctx, w.e.users, userKey(id)); err != nil && !errors.Is(err, storage.ErrNotFound) {
		w.t.Fatal(err)
	}
	if err := b.In(w.e.users).Set(userKey(id), raw); err != nil {
		w.t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		w.t.Fatalf("write user %s: %v", id, err)
	}
}

// rolesOf is user id's stored role list.
func (w *world) rolesOf(id string) []rbac.RoleName {
	w.t.Helper()
	raw, err := w.e.users.Get(context.Background(), userKey(id))
	if err != nil {
		w.t.Fatalf("read user %s: %v", id, err)
	}
	var rec userRec
	if err := json.Unmarshal(raw, &rec); err != nil {
		w.t.Fatal(err)
	}
	if len(rec.Roles) == 0 {
		return nil
	}
	return rec.Roles
}

// role reads a stored role.
func (w *world) role(name rbac.RoleName) *rbac.Role {
	w.t.Helper()
	r, err := w.store.GetRole(context.Background(), name)
	if err != nil {
		w.t.Fatalf("GetRole(%s): %v", name, err)
	}
	return r
}

// gone reports whether the role is absent.
func (w *world) gone(name rbac.RoleName) bool {
	w.t.Helper()
	_, err := w.store.GetRole(context.Background(), name)
	if errors.Is(err, rbac.ErrUnknownRole) {
		return true
	}
	if err != nil {
		w.t.Fatal(err)
	}
	return false
}

// perms is the role's effective patterns as strings.
func (w *world) perms(name rbac.RoleName) []string {
	w.t.Helper()
	var out []string
	for _, p := range w.role(name).Permissions() {
		out = append(out, string(p))
	}
	return out
}

// snapshot is every role row and user record, to compare whole stores.
func (w *world) snapshot() string {
	w.t.Helper()
	ctx := context.Background()
	var sb strings.Builder
	for r, err := range w.store.ScanRoles(ctx) {
		if err != nil {
			w.t.Fatal(err)
		}
		raw, err := json.Marshal(r)
		if err != nil {
			w.t.Fatal(err)
		}
		sb.Write(raw)
		sb.WriteByte('\n')
	}
	for _, id := range slices.Sorted(slices.Values(w.users.ids)) {
		raw, err := w.e.users.Get(ctx, userKey(id))
		if err != nil {
			w.t.Fatal(err)
		}
		sb.WriteString(id + " " + string(raw) + "\n")
	}
	return sb.String()
}

// restrictor records RestrictLastAdmins calls: calls is every user it was asked
// to restrict, flat and in order; batches is one entry per call.
type restrictor struct {
	mu      sync.Mutex
	calls   []string
	batches [][]string
	err     error
	// failures is how many calls fail (after being recorded) before the rest
	// succeed, when err is nil: a restriction that errors once and works on the
	// re-run.
	failures int
}

func (r *restrictor) fn(_ context.Context, ids []string) (map[string]int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, ids...)
	r.batches = append(r.batches, slices.Clone(ids))
	if r.err != nil {
		return nil, r.err
	}
	if r.failures > 0 {
		r.failures--
		return nil, errors.New("auth store unavailable once")
	}
	return map[string]int{"users_restricted": len(ids)}, nil
}

func (r *restrictor) called() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// batchesOf is the ids of each call, in order.
func (r *restrictor) batchesOf() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.batches)
}

// deps wires the hook over w with a fresh journal and restrictor.
func (w *world) deps(j journal.RevocationJournal, r *restrictor) rbac.RestoreDeps {
	return rbac.RestoreDeps{
		Store: w.store, NewBatch: w.store.NewBatch, Users: w.users,
		Revocations: j, RestrictLastAdmins: r.fn,
		Clock: func() time.Time { return restoreNow },
	}
}

// withPrevious sets prev as the read-only pre-swap state.
func withPrevious(d rbac.RestoreDeps, prev *world) rbac.RestoreDeps {
	d.Previous, d.PreviousUsers = prev.store, prev.users
	return d
}

// seq is a coverage-intact journal tail; nil lines are nil coverage (a gap).
func seq(lines []journal.Revocation) iter.Seq[journal.Revocation] {
	if lines == nil {
		return nil
	}
	return func(yield func(journal.Revocation) bool) {
		for _, l := range lines {
			if !yield(l) {
				return
			}
		}
	}
}

func rcOf(lines []journal.Revocation) journal.RestoreContext {
	return journal.RestoreContext{ArchiveID: "arkham-2026-11-01", Kind: journal.KindArchive, Revocations: seq(lines)}
}

func userRefOf(id string) entity.Ref { return entity.Ref{Module: "core", Kind: "user", ID: id} }
func roleRefOf(n rbac.RoleName) entity.Ref {
	return entity.Ref{Module: "core", Kind: "role", ID: string(n)}
}

func unassigned(id string, role rbac.RoleName) journal.Revocation {
	return journal.Revocation{Kind: journal.RoleUnassigned, Subject: userRefOf(id), Object: roleRefOf(role)}
}

func narrowed(role rbac.RoleName, removed ...string) journal.Revocation {
	return journal.Revocation{Kind: journal.RoleNarrowed, Subject: roleRefOf(role), Detail: removed}
}

func deleted(role rbac.RoleName) journal.Revocation {
	return journal.Revocation{Kind: journal.RoleDeleted, Subject: roleRefOf(role)}
}

// describe renders a journal line for comparison.
func describe(r journal.Revocation) string {
	s := string(r.Kind) + " " + r.Subject.Kind + ":" + r.Subject.ID
	if r.Object.ID != "" {
		s += " " + r.Object.Kind + ":" + r.Object.ID
	}
	if len(r.Detail) > 0 {
		s += " " + strings.Join(r.Detail, ",")
	}
	return s
}

// written is the journal's lines, described and sorted.
func written(j *revJournal) []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []string
	for _, l := range j.lines {
		out = append(out, describe(l))
	}
	slices.Sort(out)
	return out
}

func wantEqual[T any](t *testing.T, what string, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %#v, want %#v", what, got, want)
	}
}

func mustHook(t *testing.T, d rbac.RestoreDeps, lines []journal.Revocation) rbac.RestoreReport {
	t.Helper()
	rep, err := rbac.PostRestore(context.Background(), d, rcOf(lines))
	if err != nil {
		t.Fatalf("PostRestore: %v", err)
	}
	return rep
}

// gothamWorld is the restored state most tests start from: gordon is an active
// admin through the locked admin role, so a demotion elsewhere never trips
// the last-admin rule.
func gothamWorld(t *testing.T) *world {
	w := newWorld(t)
	w.seedRole(rbac.RoleAdmin, true, rbac.CoreAdmin)
	w.seedRole(rbac.RoleViewer, false, "gotham:patrol")
	w.customRole(trustee, rbac.CoreAdmin, "gotham:patrol")
	w.customRole(sleuth, "gotham:patrol", "gotham:signal", "gotham:cave.manage")
	w.user(bruce, true, trustee, sleuth)
	w.user(alfred, true, trustee, rbac.RoleViewer)
	w.user(gordon, true, rbac.RoleAdmin)
	w.user(harvey, true, rbac.RoleViewer)
	return w
}

// ---------------------------------------------------------------------------
// Journal re-apply.

// TestRestore_DemotedAdminStaysDemoted: an archive taken before a demotion,
// restored with the journal tail that recorded it, keeps the demotion
// (05-rbac 12; SEC2-10). Each kind separately.
func TestRestore_DemotedAdminStaysDemoted(t *testing.T) {
	t.Run("unassigned", func(t *testing.T) {
		w := gothamWorld(t)
		j, r := &revJournal{}, &restrictor{}
		rep := mustHook(t, w.deps(j, r), []journal.Revocation{unassigned(bruce, trustee)})
		wantEqual(t, "bruce", w.rolesOf(bruce), []rbac.RoleName{sleuth})
		wantEqual(t, "alfred keeps the role", w.rolesOf(alfred), []rbac.RoleName{trustee, rbac.RoleViewer})
		wantEqual(t, "counts", rep.Counts, map[string]int{rbac.CountRolesUnassigned: 1})
		wantEqual(t, "journal", written(j), []string(nil))
		wantEqual(t, "restricted", r.called(), []string(nil))
		if len(rep.KeptAdmins) != 0 {
			t.Errorf("KeptAdmins = %v", rep.KeptAdmins)
		}
	})
	t.Run("narrowed_custom", func(t *testing.T) {
		w := gothamWorld(t)
		j, r := &revJournal{}, &restrictor{}
		rep := mustHook(t, w.deps(j, r), []journal.Revocation{narrowed(trustee, "core:admin")})
		wantEqual(t, "perms", w.perms(trustee), []string{"gotham:patrol"})
		if v := w.role(trustee).Version; v != 2 {
			t.Errorf("Version = %d, want 2", v)
		}
		if !w.role(trustee).UpdatedAt.Equal(restoreNow) {
			t.Errorf("UpdatedAt = %v, want the hook clock", w.role(trustee).UpdatedAt)
		}
		wantEqual(t, "bruce still holds it", w.rolesOf(bruce), []rbac.RoleName{trustee, sleuth})
		wantEqual(t, "counts", rep.Counts, map[string]int{rbac.CountRolesNarrowed: 1})
		wantEqual(t, "journal", written(j), []string(nil))
	})
	t.Run("narrowed_seed", func(t *testing.T) {
		w := gothamWorld(t)
		w.seedRole("gotham.detective", false, "gotham:patrol", "gotham:patrol.night")
		before := w.role("gotham.detective")
		mustHook(t, w.deps(&revJournal{}, &restrictor{}), []journal.Revocation{narrowed("gotham.detective", "gotham:patrol.night")})
		got := w.role("gotham.detective")
		wantEqual(t, "seed perms", got.SeedPermissions, []rbac.Permission{"gotham:patrol"})
		if len(got.Added) != 0 {
			t.Errorf("Added = %v: a seed role is narrowed in SeedPermissions", got.Added)
		}
		if got.SeedDigest == before.SeedDigest || got.SeedDigest == "" {
			t.Errorf("SeedDigest %q was not recomputed from %q", got.SeedDigest, before.SeedDigest)
		}
		if got.Version != before.Version+1 {
			t.Errorf("Version = %d, want %d", got.Version, before.Version+1)
		}
	})
	t.Run("narrowed_removes_only_listed", func(t *testing.T) {
		w := gothamWorld(t)
		mustHook(t, w.deps(&revJournal{}, &restrictor{}), []journal.Revocation{narrowed(sleuth, "gotham:cave.manage", "gotham:never-held")})
		wantEqual(t, "perms", w.perms(sleuth), []string{"gotham:patrol", "gotham:signal"})
	})
	t.Run("deleted_custom", func(t *testing.T) {
		w := gothamWorld(t)
		j, r := &revJournal{}, &restrictor{}
		rep := mustHook(t, w.deps(j, r), []journal.Revocation{deleted(trustee)})
		if !w.gone(trustee) {
			t.Error("the deleted role is back")
		}
		wantEqual(t, "bruce", w.rolesOf(bruce), []rbac.RoleName{sleuth})
		wantEqual(t, "alfred", w.rolesOf(alfred), []rbac.RoleName{rbac.RoleViewer})
		wantEqual(t, "counts", rep.Counts, map[string]int{rbac.CountRolesDeleted: 1, rbac.CountRolesUnassigned: 2})
		wantEqual(t, "journal", written(j), []string(nil))
	})
	t.Run("deleted_seed", func(t *testing.T) {
		w := gothamWorld(t)
		w.seedRole("gotham.detective", false, "gotham:patrol")
		w.user(harvey, true, rbac.RoleViewer, "gotham.detective")
		mustHook(t, w.deps(&revJournal{}, &restrictor{}), []journal.Revocation{deleted("gotham.detective")})
		if !w.gone("gotham.detective") {
			t.Error("the deleted seed role is back")
		}
		wantEqual(t, "harvey", w.rolesOf(harvey), []rbac.RoleName{rbac.RoleViewer})
	})
	t.Run("deleted_role_already_absent_still_unassigned", func(t *testing.T) {
		// A dangling assignment would be handed back by the next Freeze's
		// re-seed of a seed role.
		w := gothamWorld(t)
		w.user(harvey, true, rbac.RoleViewer, "gotham.detective")
		mustHook(t, w.deps(&revJournal{}, &restrictor{}), []journal.Revocation{deleted("gotham.detective")})
		wantEqual(t, "harvey", w.rolesOf(harvey), []rbac.RoleName{rbac.RoleViewer})
	})
	t.Run("lines_apply_in_order_once_each", func(t *testing.T) {
		w := gothamWorld(t)
		rep := mustHook(t, w.deps(&revJournal{}, &restrictor{}), []journal.Revocation{
			unassigned(alfred, rbac.RoleViewer), narrowed(sleuth, "gotham:signal"), unassigned(bruce, sleuth),
		})
		wantEqual(t, "alfred", w.rolesOf(alfred), []rbac.RoleName{trustee})
		wantEqual(t, "bruce", w.rolesOf(bruce), []rbac.RoleName{trustee})
		wantEqual(t, "sleuth", w.perms(sleuth), []string{"gotham:patrol", "gotham:cave.manage"})
		wantEqual(t, "counts", rep.Counts, map[string]int{rbac.CountRolesUnassigned: 2, rbac.CountRolesNarrowed: 1})
	})
}

// TestRestoreHook_UnknownTargetsSkipped: a line naming a user or a role the
// restored store does not hold, or a malformed reference, revokes nothing
// and is no error.
func TestRestoreHook_UnknownTargetsSkipped(t *testing.T) {
	w := gothamWorld(t)
	before := w.snapshot()
	rep := mustHook(t, w.deps(&revJournal{}, &restrictor{}), []journal.Revocation{
		unassigned("selina_kyle", trustee),
		unassigned(bruce, "custom.never-existed"),
		narrowed("custom.never-existed", "gotham:patrol"),
		deleted("custom.never-existed"),
		{Kind: journal.RoleUnassigned, Subject: roleRefOf(trustee), Object: userRefOf(bruce)}, // swapped kinds
		{Kind: journal.RoleNarrowed, Subject: entity.Ref{Module: "gotham", Kind: "role", ID: "custom.gotham-detective"}, Detail: []string{"gotham:patrol"}}, // foreign module
		{Kind: journal.RoleDeleted, Subject: userRefOf("custom.gotham-detective")},
		{Kind: journal.UserDisabled, Subject: userRefOf(bruce)},
	})
	if got := w.snapshot(); got != before {
		t.Errorf("the store changed:\n%s\nwas:\n%s", got, before)
	}
	if len(rep.Counts) != 0 {
		t.Errorf("Counts = %v, want none", rep.Counts)
	}
}

// ---------------------------------------------------------------------------
// The pre-swap pass.

// preSwapWorld is the live state after a demotion that no journal line holds:
// the detective role lost cave.manage, the visitor role was deleted, bruce
// lost the detective role and alfred the visitor role.
func preSwapWorld(t *testing.T) (restored, pre *world) {
	restored = newWorld(t)
	restored.seedRole(rbac.RoleAdmin, true, rbac.CoreAdmin)
	restored.customRole(sleuth, "gotham:patrol", "gotham:signal", "gotham:cave.manage")
	restored.customRole(visitor, "gotham:patrol")
	restored.customRole(trustee, "gotham:patrol")
	restored.user(bruce, true, rbac.RoleAdmin, sleuth)
	restored.user(alfred, true, sleuth, visitor)
	restored.user(gordon, true, sleuth)
	restored.user(harvey, true, trustee) // absent from the pre-swap store

	pre = newWorld(t)
	pre.seedRole(rbac.RoleAdmin, true, rbac.CoreAdmin)
	pre.customRole(sleuth, "gotham:patrol", "gotham:signal", "gotham:patrol.night") // narrower, and wider by patrol.night
	pre.customRole(trustee, "gotham:patrol")
	pre.user(bruce, true, rbac.RoleAdmin)
	pre.user(alfred, true, sleuth)
	pre.user(gordon, true, sleuth, trustee) // holds more there than in the archive
	return restored, pre
}

// TestRestore_UnjournaledDemotionHeldByPreSwapStore: with a pre-swap store
// and no journal coverage, a demotion no line holds is applied from the
// pre-swap state, and what that derived is journaled (05-rbac 12 cell;
// SEC5-10, SEC6-4).
func TestRestore_UnjournaledDemotionHeldByPreSwapStore(t *testing.T) {
	restored, pre := preSwapWorld(t)
	j, r := &revJournal{}, &restrictor{}
	d := withPrevious(restored.deps(j, r), pre)
	rep := mustHook(t, d, nil)

	wantEqual(t, "detective perms are the intersection", restored.perms(sleuth), []string{"gotham:patrol", "gotham:signal"})
	if !restored.gone(visitor) {
		t.Error("the role deleted pre-swap came back")
	}
	if restored.gone(trustee) {
		t.Error("a role both stores hold was deleted")
	}
	wantEqual(t, "bruce loses the assignment absent pre-swap", restored.rolesOf(bruce), []rbac.RoleName{rbac.RoleAdmin})
	wantEqual(t, "alfred", restored.rolesOf(alfred), []rbac.RoleName{sleuth})
	wantEqual(t, "gordon is not given the pre-swap extra role", restored.rolesOf(gordon), []rbac.RoleName{sleuth})
	wantEqual(t, "harvey is absent pre-swap and left alone", restored.rolesOf(harvey), []rbac.RoleName{trustee})
	wantEqual(t, "journal", written(j), []string{
		"role.deleted role:custom.arkham-visitor",
		"role.narrowed role:custom.gotham-detective gotham:cave.manage",
		"role.unassigned user:alfred_pennyworth role:custom.arkham-visitor",
		"role.unassigned user:bruce_wayne role:custom.gotham-detective",
	})
	for _, l := range j.lines {
		if !l.At.Equal(restoreNow) {
			t.Errorf("%s: At = %v, want the hook clock", describe(l), l.At)
		}
	}
	wantEqual(t, "counts", rep.Counts, map[string]int{rbac.CountRolesDeleted: 1, rbac.CountRolesNarrowed: 1, rbac.CountRolesUnassigned: 2})
	if len(rep.AdminsToReview) != 0 {
		t.Errorf("AdminsToReview = %v with a pre-swap store", rep.AdminsToReview)
	}
}

// TestRestoreHook_FullCoverageWithPreSwapDerivesNothingNew: the journal holds
// every demotion and the pre-swap store holds the same ones: the result is
// the same and nothing is journaled again.
func TestRestoreHook_FullCoverageWithPreSwapDerivesNothingNew(t *testing.T) {
	restored, pre := preSwapWorld(t)
	j := &revJournal{}
	d := withPrevious(restored.deps(j, &restrictor{}), pre)
	mustHook(t, d, []journal.Revocation{
		narrowed(sleuth, "gotham:cave.manage"), unassigned(bruce, sleuth), unassigned(alfred, visitor), deleted(visitor),
	})
	wantEqual(t, "journal", written(j), []string(nil))
	wantEqual(t, "detective", restored.perms(sleuth), []string{"gotham:patrol", "gotham:signal"})
	if !restored.gone(visitor) {
		t.Error("visitor survived")
	}
}

// TestRestoreHook_PreSwapPassPartlyJournaled: a line the journal holds is
// applied by pass 1; pass 2 journals only what remains.
func TestRestoreHook_PreSwapPassPartlyJournaled(t *testing.T) {
	restored, pre := preSwapWorld(t)
	j := &revJournal{}
	d := withPrevious(restored.deps(j, &restrictor{}), pre)
	mustHook(t, d, []journal.Revocation{narrowed(sleuth, "gotham:cave.manage"), deleted(visitor)})
	wantEqual(t, "journal", written(j), []string{"role.unassigned user:bruce_wayne role:custom.gotham-detective"})
	wantEqual(t, "alfred", restored.rolesOf(alfred), []rbac.RoleName{sleuth})
}

// TestRestoreHook_PreSwapIntersectionPatterns: a role's permissions are the
// restored patterns some pre-swap pattern covers, wildcards included.
func TestRestoreHook_PreSwapIntersectionPatterns(t *testing.T) {
	for _, tc := range []struct {
		name     string
		restored []rbac.Permission
		pre      []rbac.Permission
		want     []string
	}{
		{"identical wildcard survives", []rbac.Permission{"gotham:*", "gotham:signal"}, []rbac.Permission{"gotham:*", "gotham:signal"}, []string{"gotham:*", "gotham:signal"}},
		{"identical action wildcard survives", []rbac.Permission{"gotham:patrol.*"}, []rbac.Permission{"gotham:patrol.*"}, []string{"gotham:patrol.*"}},
		{"narrower pre-swap drops the wildcard", []rbac.Permission{"gotham:*", "gotham:signal"}, []rbac.Permission{"gotham:signal"}, []string{"gotham:signal"}},
		{"namespace wildcard covers concrete and action wildcard", []rbac.Permission{"gotham:signal", "gotham:patrol.*", "gotham:patrol.night"}, []rbac.Permission{"gotham:*"}, []string{"gotham:signal", "gotham:patrol.*", "gotham:patrol.night"}},
		{"action wildcard covers its sub", []rbac.Permission{"gotham:patrol.night", "gotham:patrol", "gotham:signal"}, []rbac.Permission{"gotham:patrol.*"}, []string{"gotham:patrol.night", "gotham:patrol"}},
		{"action wildcard does not cover the namespace wildcard", []rbac.Permission{"gotham:*"}, []rbac.Permission{"gotham:patrol.*"}, nil},
		{"another namespace covers nothing", []rbac.Permission{"gotham:signal"}, []rbac.Permission{"arkham:*"}, nil},
		{"never widens", []rbac.Permission{"gotham:signal"}, []rbac.Permission{"gotham:signal", "gotham:cave.manage"}, []string{"gotham:signal"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restored, pre := newWorld(t), newWorld(t)
			restored.customRole(sleuth, tc.restored...)
			pre.customRole(sleuth, tc.pre...)
			mustHook(t, withPrevious(restored.deps(&revJournal{}, &restrictor{}), pre), nil)
			wantEqual(t, "perms", restored.perms(sleuth), tc.want)
		})
	}
}

// TestRestoreHook_PreSwapNarrowsSeedRoles: the intersection reads and writes
// a seed role's SeedPermissions.
func TestRestoreHook_PreSwapNarrowsSeedRoles(t *testing.T) {
	restored, pre := newWorld(t), newWorld(t)
	restored.seedRole("gotham.detective", false, "gotham:patrol", "gotham:signal")
	pre.seedRole("gotham.detective", false, "gotham:patrol")
	restored.seedRole("gotham.newcomer", false, "gotham:patrol") // a seed role the pre-swap store never had
	mustHook(t, withPrevious(restored.deps(&revJournal{}, &restrictor{}), pre), nil)
	wantEqual(t, "narrowed seed", restored.role("gotham.detective").SeedPermissions, []rbac.Permission{"gotham:patrol"})
	if restored.gone("gotham.newcomer") {
		t.Error("a seed role the pre-swap store lacks must be left for the re-seed")
	}
}

// ---------------------------------------------------------------------------
// The last-admin rule.

// lastAdminWorld has bruce as the one active holder of trustee (core:admin);
// alfred holds it inactive.
func lastAdminWorld(t *testing.T) *world {
	w := newWorld(t)
	w.customRole(trustee, rbac.CoreAdmin, "gotham:patrol")
	w.seedRole(rbac.RoleViewer, false, "gotham:patrol")
	w.user(bruce, true, trustee)
	w.user(alfred, false, trustee) // inactive: never counts as an admin
	w.user(gordon, true, rbac.RoleViewer)
	return w
}

// TestRestoreHook_LastAdminKeptAndRestricted: a demotion that would leave no
// active admin keeps the role, restricts the account exactly once and
// reports it; with a second active admin the same demotion applies.
func TestRestoreHook_LastAdminKeptAndRestricted(t *testing.T) {
	t.Run("unassign_of_sole_admin", func(t *testing.T) {
		w := lastAdminWorld(t)
		j, r := &revJournal{}, &restrictor{}
		rep := mustHook(t, w.deps(j, r), []journal.Revocation{unassigned(bruce, trustee)})
		wantEqual(t, "bruce keeps the role", w.rolesOf(bruce), []rbac.RoleName{trustee})
		wantEqual(t, "restricted", r.called(), []string{bruce})
		wantEqual(t, "KeptAdmins", rep.KeptAdmins, []string{bruce})
		wantEqual(t, "counts", rep.Counts, map[string]int{rbac.CountLastAdminKept: 1, "users_restricted": 1})
		wantEqual(t, "journal", written(j), []string(nil))
	})
	t.Run("narrowing_of_sole_admin_role", func(t *testing.T) {
		w := lastAdminWorld(t)
		r := &restrictor{}
		rep := mustHook(t, w.deps(&revJournal{}, r), []journal.Revocation{narrowed(trustee, "core:admin", "gotham:patrol")})
		// The role stays admin-granting; the part of the line that does not
		// touch admin still applies.
		wantEqual(t, "perms", w.perms(trustee), []string{"core:admin"})
		wantEqual(t, "restricted", r.called(), []string{bruce})
		wantEqual(t, "KeptAdmins", rep.KeptAdmins, []string{bruce})
		wantEqual(t, "counts", rep.Counts, map[string]int{rbac.CountRolesNarrowed: 1, rbac.CountLastAdminKept: 1, "users_restricted": 1})
	})
	t.Run("deleting_the_sole_admin_role", func(t *testing.T) {
		w := lastAdminWorld(t)
		r := &restrictor{}
		rep := mustHook(t, w.deps(&revJournal{}, r), []journal.Revocation{deleted(trustee), unassigned(gordon, rbac.RoleViewer)})
		if w.gone(trustee) {
			t.Error("the admin-granting role was deleted")
		}
		wantEqual(t, "bruce keeps the role", w.rolesOf(bruce), []rbac.RoleName{trustee})
		wantEqual(t, "an unrelated change still applies", w.rolesOf(gordon), []rbac.RoleName(nil))
		// Only an active holder is protected: the inactive alfred loses it.
		wantEqual(t, "inactive holder is unassigned", w.rolesOf(alfred), []rbac.RoleName(nil))
		wantEqual(t, "restricted", r.called(), []string{bruce})
		wantEqual(t, "KeptAdmins", rep.KeptAdmins, []string{bruce})
	})
	t.Run("every_holder_of_a_demoted_pair_is_restricted_once", func(t *testing.T) {
		w := lastAdminWorld(t)
		w.user(harvey, true, trustee)
		r := &restrictor{}
		rep := mustHook(t, w.deps(&revJournal{}, r), []journal.Revocation{unassigned(harvey, trustee), unassigned(bruce, trustee)})
		wantEqual(t, "bruce", w.rolesOf(bruce), []rbac.RoleName{trustee})
		wantEqual(t, "harvey", w.rolesOf(harvey), []rbac.RoleName{trustee})
		wantEqual(t, "restricted", r.called(), []string{bruce, harvey})
		wantEqual(t, "one call for both holders", r.batchesOf(), [][]string{{bruce, harvey}})
		wantEqual(t, "KeptAdmins", rep.KeptAdmins, []string{bruce, harvey})
		wantEqual(t, "last_admin_kept", rep.Counts[rbac.CountLastAdminKept], 2)
		wantEqual(t, "users_restricted", rep.Counts["users_restricted"], 2)
	})
	t.Run("a_failed_restriction_of_a_pair_is_redone_for_both_on_the_re_run", func(t *testing.T) {
		w := lastAdminWorld(t)
		w.user(harvey, true, trustee)
		r := &restrictor{failures: 1}
		lines := []journal.Revocation{unassigned(harvey, trustee), unassigned(bruce, trustee)}
		if _, err := rbac.PostRestore(context.Background(), w.deps(&revJournal{}, r), rcOf(lines)); err == nil {
			t.Fatal("the failed restriction must fail the hook")
		}
		// The role state was committed before the restriction: both holders keep
		// the role, so the re-run (the hook is re-run until the restore is hooked)
		// finds the same two holders and restricts them again, together.
		wantEqual(t, "bruce keeps the role", w.rolesOf(bruce), []rbac.RoleName{trustee})
		wantEqual(t, "harvey keeps the role", w.rolesOf(harvey), []rbac.RoleName{trustee})
		rep := mustHook(t, w.deps(&revJournal{}, r), lines)
		wantEqual(t, "KeptAdmins on the re-run", rep.KeptAdmins, []string{bruce, harvey})
		wantEqual(t, "one call per run, both holders each time", r.batchesOf(), [][]string{{bruce, harvey}, {bruce, harvey}})
		wantEqual(t, "users_restricted", rep.Counts["users_restricted"], 2)
	})
	t.Run("second_active_admin_lets_the_demotion_apply", func(t *testing.T) {
		w := lastAdminWorld(t)
		w.user(harvey, true, trustee)
		r := &restrictor{}
		rep := mustHook(t, w.deps(&revJournal{}, r), []journal.Revocation{unassigned(bruce, trustee)})
		wantEqual(t, "bruce is demoted", w.rolesOf(bruce), []rbac.RoleName(nil))
		wantEqual(t, "restricted", r.called(), []string(nil))
		if len(rep.KeptAdmins) != 0 || rep.Counts[rbac.CountLastAdminKept] != 0 {
			t.Errorf("KeptAdmins = %v, counts = %v", rep.KeptAdmins, rep.Counts)
		}
	})
	t.Run("pre_swap_demotion_of_sole_admin", func(t *testing.T) {
		w := lastAdminWorld(t)
		pre := newWorld(t)
		pre.customRole(trustee, rbac.CoreAdmin, "gotham:patrol")
		pre.seedRole(rbac.RoleViewer, false, "gotham:patrol")
		pre.user(bruce, true) // demoted after the archive, no journal line
		pre.user(alfred, false, trustee)
		pre.user(gordon, true, rbac.RoleViewer)
		j, r := &revJournal{}, &restrictor{}
		rep := mustHook(t, withPrevious(w.deps(j, r), pre), nil)
		wantEqual(t, "bruce keeps the role", w.rolesOf(bruce), []rbac.RoleName{trustee})
		wantEqual(t, "restricted", r.called(), []string{bruce})
		wantEqual(t, "KeptAdmins", rep.KeptAdmins, []string{bruce})
		wantEqual(t, "journal", written(j), []string(nil))
	})
	t.Run("restricting_failure_fails_the_hook", func(t *testing.T) {
		w := lastAdminWorld(t)
		r := &restrictor{err: errors.New("auth store unavailable")}
		_, err := rbac.PostRestore(context.Background(), w.deps(&revJournal{}, r), rcOf([]journal.Revocation{unassigned(bruce, trustee)}))
		if err == nil {
			t.Fatal("a failed restriction must fail the hook")
		}
	})
}

// TestRestoreHook_NoAdminBeforeMeansNothingToKeep: a restored store with no
// active admin holder at all is not a lockout this hook caused.
func TestRestoreHook_NoAdminBeforeMeansNothingToKeep(t *testing.T) {
	w := newWorld(t)
	w.customRole(trustee, rbac.CoreAdmin)
	w.user(bruce, false, trustee)
	r := &restrictor{}
	rep := mustHook(t, w.deps(&revJournal{}, r), []journal.Revocation{unassigned(bruce, trustee)})
	wantEqual(t, "bruce", w.rolesOf(bruce), []rbac.RoleName(nil))
	wantEqual(t, "restricted", r.called(), []string(nil))
	if len(rep.KeptAdmins) != 0 {
		t.Errorf("KeptAdmins = %v", rep.KeptAdmins)
	}
}

// ---------------------------------------------------------------------------
// Idempotency, replaced stores, locked roles, journal failure, conflicts.

// TestRestoreHook_Idempotent: the hook re-runs after a crash before the
// restore journal reaches applied; the second run changes and journals
// nothing.
func TestRestoreHook_Idempotent(t *testing.T) {
	t.Run("journal_and_pre_swap", func(t *testing.T) {
		restored, pre := preSwapWorld(t)
		j := &revJournal{}
		d := withPrevious(restored.deps(j, &restrictor{}), pre)
		lines := []journal.Revocation{narrowed(sleuth, "gotham:cave.manage"), unassigned(bruce, sleuth)}
		mustHook(t, d, lines)
		after1, journaled1 := restored.snapshot(), written(j)
		if len(journaled1) == 0 {
			t.Fatal("the first run derived nothing; the test proves nothing")
		}
		rep := mustHook(t, d, lines)
		if got := restored.snapshot(); got != after1 {
			t.Errorf("the second run changed the store:\n%s\nwas:\n%s", got, after1)
		}
		wantEqual(t, "journal after the second run", written(j), journaled1)
		if len(rep.Counts) != 0 {
			t.Errorf("second run Counts = %v, want none", rep.Counts)
		}
	})
	t.Run("deleted_role_and_seed", func(t *testing.T) {
		w := gothamWorld(t)
		j := &revJournal{}
		lines := []journal.Revocation{deleted(trustee), narrowed(sleuth, "gotham:signal")}
		mustHook(t, w.deps(j, &restrictor{}), lines)
		after1 := w.snapshot()
		mustHook(t, w.deps(j, &restrictor{}), lines)
		if got := w.snapshot(); got != after1 {
			t.Errorf("the second run changed the store:\n%s\nwas:\n%s", got, after1)
		}
		if len(j.lines) != 0 {
			t.Errorf("journaled %v", written(j))
		}
	})
	t.Run("kept_admin_is_restricted_again_and_nothing_else_moves", func(t *testing.T) {
		w := lastAdminWorld(t)
		r := &restrictor{}
		lines := []journal.Revocation{unassigned(bruce, trustee)}
		mustHook(t, w.deps(&revJournal{}, r), lines)
		after1 := w.snapshot()
		rep := mustHook(t, w.deps(&revJournal{}, r), lines)
		if got := w.snapshot(); got != after1 {
			t.Errorf("the second run changed the store")
		}
		wantEqual(t, "KeptAdmins", rep.KeptAdmins, []string{bruce})
		wantEqual(t, "RestrictLastAdmins runs again (it is idempotent)", r.called(), []string{bruce, bruce})
	})
}

// TestRestoreHook_ReplacedStoreListsAdmins: a replaced store (no pre-swap
// store, no coverage) changes nothing and lists every active admin holder.
func TestRestoreHook_ReplacedStoreListsAdmins(t *testing.T) {
	w := gothamWorld(t)
	w.customRole("custom.arkham-warden", "core:*")
	w.customRole("custom.gotham-clerk", "gotham:patrol")
	w.user(harvey, true, "custom.arkham-warden", rbac.RoleAdmin) // two admin roles: listed once
	w.user("selina_kyle", true, "custom.gotham-clerk")           // not an admin
	w.user("oswald_cobblepot", false, trustee)                   // inactive
	before := w.snapshot()
	j, r := &revJournal{}, &restrictor{}
	rep := mustHook(t, w.deps(j, r), nil)
	if got := w.snapshot(); got != before {
		t.Errorf("a replaced store with no coverage must change nothing:\n%s", got)
	}
	wantEqual(t, "AdminsToReview", rep.AdminsToReview, []string{alfred, bruce, gordon, harvey})
	if len(rep.KeptAdmins) != 0 || len(rep.Counts) != 0 || len(j.lines) != 0 || len(r.called()) != 0 {
		t.Errorf("report %+v, journal %v, restricted %v", rep, written(j), r.called())
	}
}

// TestRestoreHook_ReplacedStoreWithJournalListsPostHookAdmins: when lines are
// re-applied on a store with no pre-swap copy, the review list is the state
// after the hook.
func TestRestoreHook_ReplacedStoreWithJournalListsPostHookAdmins(t *testing.T) {
	w := gothamWorld(t)
	rep := mustHook(t, w.deps(&revJournal{}, &restrictor{}), []journal.Revocation{unassigned(bruce, trustee), unassigned(bruce, sleuth)})
	wantEqual(t, "AdminsToReview", rep.AdminsToReview, []string{alfred, gordon})
}

// TestRestoreHook_LockedRoleNeverTouched: neither a line nor the pre-swap
// comparison narrows or deletes a Locked role; its assignments can still be
// taken from a user (that is a demotion).
func TestRestoreHook_LockedRoleNeverTouched(t *testing.T) {
	w := gothamWorld(t)
	w.put(rbac.Role{Name: "custom.arkham-cell", Origin: rbac.OriginCustom, Added: []rbac.Permission{"gotham:patrol"}, Locked: true})
	w.user(harvey, true, "custom.arkham-cell")
	pre := newWorld(t)
	pre.seedRole(rbac.RoleAdmin, true) // the pre-swap admin role is empty
	pre.customRole(trustee, "gotham:patrol")
	pre.customRole(sleuth, "gotham:patrol")
	pre.user(bruce, true, trustee, sleuth)
	pre.user(alfred, true, trustee, rbac.RoleViewer)
	pre.user(gordon, true, rbac.RoleAdmin)
	pre.user(harvey, true, "custom.arkham-cell")
	adminBefore, cellBefore := w.role(rbac.RoleAdmin), w.role("custom.arkham-cell")

	d := withPrevious(w.deps(&revJournal{}, &restrictor{}), pre)
	mustHook(t, d, []journal.Revocation{
		narrowed(rbac.RoleAdmin, "core:admin"), deleted(rbac.RoleAdmin),
		narrowed("custom.arkham-cell", "gotham:patrol"), deleted("custom.arkham-cell"),
	})
	wantEqual(t, "admin role row", w.role(rbac.RoleAdmin), adminBefore)
	wantEqual(t, "locked custom role row", w.role("custom.arkham-cell"), cellBefore)
	wantEqual(t, "gordon still holds admin", w.rolesOf(gordon), []rbac.RoleName{rbac.RoleAdmin})
	wantEqual(t, "harvey still holds the locked role", w.rolesOf(harvey), []rbac.RoleName{"custom.arkham-cell"})

	// Taking the admin role from a user is a demotion, not a change to the role.
	w.user(bruce, true, trustee, rbac.RoleAdmin)
	mustHook(t, w.deps(&revJournal{}, &restrictor{}), []journal.Revocation{unassigned(bruce, rbac.RoleAdmin)})
	wantEqual(t, "bruce", w.rolesOf(bruce), []rbac.RoleName{trustee})
}

// TestRestoreHook_AdminRoleProtectedByNameWithoutLockedFlag: the admin role is
// untouchable by name, even if its row lost the Locked flag or is absent.
func TestRestoreHook_AdminRoleProtectedByNameWithoutLockedFlag(t *testing.T) {
	w := newWorld(t)
	w.seedRole(rbac.RoleAdmin, false, rbac.CoreAdmin) // not flagged Locked
	w.customRole(trustee, rbac.CoreAdmin)
	w.user(harvey, true, trustee) // a second admin source, so no lockout guard can hide a missing name check
	w.user(bruce, true, rbac.RoleAdmin, "custom.ghost")
	w.user(gordon, true, rbac.RoleAdmin)
	before := w.role(rbac.RoleAdmin)
	mustHook(t, w.deps(&revJournal{}, &restrictor{}), []journal.Revocation{narrowed(rbac.RoleAdmin, "core:admin"), deleted(rbac.RoleAdmin)})
	wantEqual(t, "admin row", w.role(rbac.RoleAdmin), before)
	wantEqual(t, "bruce", w.rolesOf(bruce), []rbac.RoleName{rbac.RoleAdmin, "custom.ghost"})
	wantEqual(t, "gordon", w.rolesOf(gordon), []rbac.RoleName{rbac.RoleAdmin})
}

// TestRestoreHook_AppendFailureStillCommitsWithGap: a journal that cannot
// Append records a gap instead and never blocks the restriction.
func TestRestoreHook_AppendFailureStillCommitsWithGap(t *testing.T) {
	restored, pre := preSwapWorld(t)
	j := &revJournal{appendErr: errors.New("the backup volume is full")}
	rep := mustHook(t, withPrevious(restored.deps(j, &restrictor{}), pre), nil)
	wantEqual(t, "perms", restored.perms(sleuth), []string{"gotham:patrol", "gotham:signal"})
	if !restored.gone(visitor) {
		t.Error("the change was blocked by the journal")
	}
	wantEqual(t, "bruce", restored.rolesOf(bruce), []rbac.RoleName{rbac.RoleAdmin})
	if j.gaps != 4 || len(j.lines) != 0 {
		t.Errorf("gaps = %d, lines = %v, want one gap per derived restriction (4) and no line", j.gaps, written(j))
	}
	wantEqual(t, "counts", rep.Counts[rbac.CountRolesDeleted], 1)

	// A gap that cannot be staged fails the hook and commits nothing.
	restored2, pre2 := preSwapWorld(t)
	before := restored2.snapshot()
	j2 := &revJournal{appendErr: errors.New("full"), gapErr: errors.New("journal head unreadable")}
	if _, err := rbac.PostRestore(context.Background(), withPrevious(restored2.deps(j2, &restrictor{}), pre2), rcOf(nil)); err == nil {
		t.Error("a gap that cannot be staged must fail the hook")
	}
	if restored2.snapshot() != before {
		t.Error("a failed hook committed a change")
	}
}

// orderJournal runs check inside Append, before the batch commits.
type orderJournal struct {
	revJournal
	check func(r journal.Revocation)
}

func (o *orderJournal) Append(ctx context.Context, b *storage.Batch, r journal.Revocation) error {
	o.check(r)
	return o.revJournal.Append(ctx, b, r)
}

// TestRestoreHook_DerivedLinesJournaledBeforeCommit: every derived line is
// appended into the change's batch while the store still holds the old state.
func TestRestoreHook_DerivedLinesJournaledBeforeCommit(t *testing.T) {
	restored, pre := preSwapWorld(t)
	oj := &orderJournal{}
	oj.check = func(r journal.Revocation) {
		if restored.gone(sleuth) || restored.gone(visitor) || len(restored.perms(sleuth)) != 3 {
			t.Errorf("%s was journaled after the change committed", describe(r))
		}
	}
	mustHook(t, withPrevious(restored.deps(oj, &restrictor{}), pre), nil)
	if len(oj.lines) != 4 {
		t.Errorf("lines = %v", written(&oj.revJournal))
	}
}

// TestRestoreHook_ConflictRetried: a user changed between the hook's scan and
// its commit makes the commit conflict; the retry reads again and does not
// overwrite the concurrent change.
func TestRestoreHook_ConflictRetried(t *testing.T) {
	t.Run("user_changed", func(t *testing.T) {
		w := gothamWorld(t)
		w.users.onScan = func(n int) {
			if n == 1 {
				w.user(alfred, true, trustee, rbac.RoleViewer, "editor")
			}
		}
		j := &revJournal{}
		rep := mustHook(t, w.deps(j, &restrictor{}), []journal.Revocation{unassigned(alfred, trustee)})
		if w.users.scans != 2 {
			t.Errorf("scans = %d, want 2 (the retry reads again)", w.users.scans)
		}
		wantEqual(t, "alfred keeps the concurrent grant", w.rolesOf(alfred), []rbac.RoleName{rbac.RoleViewer, "editor"})
		wantEqual(t, "counts", rep.Counts, map[string]int{rbac.CountRolesUnassigned: 1})
	})
	t.Run("role_changed", func(t *testing.T) {
		w := gothamWorld(t)
		w.users.onScan = func(n int) {
			if n == 1 {
				w.update(sleuth, func(r *rbac.Role) { r.Title = "Detective, revised" })
			}
		}
		mustHook(t, w.deps(&revJournal{}, &restrictor{}), []journal.Revocation{narrowed(sleuth, "gotham:signal")})
		if w.users.scans != 2 {
			t.Errorf("scans = %d, want 2", w.users.scans)
		}
		got := w.role(sleuth)
		if got.Title != "Detective, revised" || got.Version != 3 {
			t.Errorf("title %q version %d: the retry must build on the concurrent write (version 3)", got.Title, got.Version)
		}
		wantEqual(t, "perms", w.perms(sleuth), []string{"gotham:patrol", "gotham:cave.manage"})
	})
	t.Run("exhausted", func(t *testing.T) {
		w := gothamWorld(t)
		before := w.perms(trustee)
		w.users.onScan = func(n int) {
			w.user(alfred, true, trustee, rbac.RoleViewer, rbac.RoleName(fmt.Sprintf("custom.scan-%d", n)))
		}
		_, err := rbac.PostRestore(context.Background(), w.deps(&revJournal{}, &restrictor{}), rcOf([]journal.Revocation{unassigned(alfred, trustee), narrowed(trustee, "gotham:patrol")}))
		if !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("err = %v, want storage.ErrConflict", err)
		}
		wantEqual(t, "perms", w.perms(trustee), before)
	})
}

// TestRestoreHook_ErrorsFailTheHook: a store or scan failure is returned, never
// swallowed, and so is a misconfigured RestoreDeps.
func TestRestoreHook_ErrorsFailTheHook(t *testing.T) {
	w := gothamWorld(t)
	w.users.scanErr = errors.New("user store unavailable")
	if _, err := rbac.PostRestore(context.Background(), w.deps(&revJournal{}, &restrictor{}), rcOf(nil)); err == nil || !strings.Contains(err.Error(), "user store unavailable") {
		t.Errorf("err = %v", err)
	}
	w.users.scanErr = nil

	for name, mut := range map[string]func(d *rbac.RestoreDeps){
		"no store":                  func(d *rbac.RestoreDeps) { d.Store = nil },
		"no users":                  func(d *rbac.RestoreDeps) { d.Users = nil },
		"no batch":                  func(d *rbac.RestoreDeps) { d.NewBatch = nil },
		"no journal":                func(d *rbac.RestoreDeps) { d.Revocations = nil },
		"no restrict":               func(d *rbac.RestoreDeps) { d.RestrictLastAdmins = nil },
		"previous without users":    func(d *rbac.RestoreDeps) { d.Previous = w.store },
		"previous users without it": func(d *rbac.RestoreDeps) { d.PreviousUsers = w.users },
	} {
		d := w.deps(&revJournal{}, &restrictor{})
		mut(&d)
		if _, err := rbac.PostRestore(context.Background(), d, rcOf(nil)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// ---------------------------------------------------------------------------
// StageReapply: the boot's tail re-apply on the same store.

// stageAndCommit runs StageReapply into a fresh batch and commits it.
func stageAndCommit(t *testing.T, w *world, d rbac.RestoreDeps, lines []journal.Revocation) (rbac.RestoreReport, error) {
	t.Helper()
	b := w.store.NewBatch()
	defer func() {
		if err := b.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	rep, err := rbac.StageReapply(context.Background(), d, b, lines)
	if err != nil {
		t.Fatalf("StageReapply: %v", err)
	}
	return rep, b.Commit(context.Background(), storage.Sync)
}

// reapplyDeps are the deps StageReapply needs: no journal, no restrictor.
func reapplyDeps(w *world) rbac.RestoreDeps {
	return rbac.RestoreDeps{Store: w.store, Users: w.users, Clock: func() time.Time { return restoreNow }}
}

// TestStageReapply_AppliesRoleLinesIntoBatch: the lines are staged into the
// caller's batch, applied only when it commits, and journal nothing.
func TestStageReapply_AppliesRoleLinesIntoBatch(t *testing.T) {
	w := gothamWorld(t)
	before := w.snapshot()
	b := w.store.NewBatch()
	defer func() {
		if err := b.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	rep, err := rbac.StageReapply(context.Background(), reapplyDeps(w), b, []journal.Revocation{
		unassigned(alfred, rbac.RoleViewer), narrowed(sleuth, "gotham:cave.manage"), deleted(trustee),
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.snapshot() != before {
		t.Error("StageReapply wrote before the caller committed")
	}
	if b.Count() == 0 {
		t.Error("nothing was staged into the caller's batch")
	}
	if err := b.Commit(context.Background(), storage.Sync); err != nil {
		t.Fatal(err)
	}
	wantEqual(t, "sleuth", w.perms(sleuth), []string{"gotham:patrol", "gotham:signal"})
	if !w.gone(trustee) {
		t.Error("trustee survived")
	}
	wantEqual(t, "alfred", w.rolesOf(alfred), []rbac.RoleName(nil))
	wantEqual(t, "bruce", w.rolesOf(bruce), []rbac.RoleName{sleuth})
	wantEqual(t, "counts", rep.Counts, map[string]int{rbac.CountRolesDeleted: 1, rbac.CountRolesNarrowed: 1, rbac.CountRolesUnassigned: 3})
	if len(rep.KeptAdmins) != 0 {
		t.Errorf("KeptAdmins = %v", rep.KeptAdmins)
	}
}

// TestStageReapply_SkipsLastAdminRemovalAndReportsIt: a line whose
// application would remove the last active admin holder is skipped and its
// holder reported; nobody is restricted here.
func TestStageReapply_SkipsLastAdminRemovalAndReportsIt(t *testing.T) {
	w := lastAdminWorld(t)
	w.user(harvey, true, trustee) // a second active admin, until a line demotes him
	r := &restrictor{}
	d := reapplyDeps(w)
	d.RestrictLastAdmins = r.fn
	rep, err := stageAndCommit(t, w, d, []journal.Revocation{
		unassigned(harvey, trustee),         // fine: bruce is still an admin
		unassigned(bruce, trustee),          // would remove the last one: skipped
		unassigned(gordon, rbac.RoleViewer), // unrelated: applies after the skip
	})
	if err != nil {
		t.Fatal(err)
	}
	wantEqual(t, "harvey", w.rolesOf(harvey), []rbac.RoleName(nil))
	wantEqual(t, "bruce keeps the role", w.rolesOf(bruce), []rbac.RoleName{trustee})
	wantEqual(t, "gordon", w.rolesOf(gordon), []rbac.RoleName(nil))
	wantEqual(t, "KeptAdmins", rep.KeptAdmins, []string{bruce})
	wantEqual(t, "restricted", r.called(), []string(nil))
	wantEqual(t, "counts", rep.Counts, map[string]int{rbac.CountRolesUnassigned: 2, rbac.CountLastAdminKept: 1})

	// A narrowing and a deletion that would remove the last admin are skipped too.
	for _, l := range []journal.Revocation{narrowed(trustee, "core:admin"), deleted(trustee)} {
		before := w.snapshot()
		rep, err := stageAndCommit(t, w, reapplyDeps(w), []journal.Revocation{l})
		if err != nil {
			t.Fatal(err)
		}
		if w.snapshot() != before {
			t.Errorf("%s: the store changed", describe(l))
		}
		wantEqual(t, describe(l)+" KeptAdmins", rep.KeptAdmins, []string{bruce})
	}
}

// TestStageReapply_IgnoresNonRoleKinds: other journal kinds change nothing.
func TestStageReapply_IgnoresNonRoleKinds(t *testing.T) {
	w := gothamWorld(t)
	before := w.snapshot()
	b := w.store.NewBatch()
	defer func() {
		if err := b.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	rep, err := rbac.StageReapply(context.Background(), reapplyDeps(w), b, []journal.Revocation{
		{Kind: journal.UserDisabled, Subject: userRefOf(bruce)},
		{Kind: journal.UserDeleted, Subject: userRefOf(alfred)},
		{Kind: journal.APIKeyRevoked, Subject: entity.Ref{Module: "core", Kind: "apikey", ID: "batphone"}},
		{Kind: journal.PasswordChanged, Subject: userRefOf(gordon)},
		{Kind: journal.Gap, Detail: []string{"2026-11-01", "append_failed"}},
		{Kind: journal.Restore, Detail: []string{"archive", "arkham", "7"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.Count() != 0 {
		t.Errorf("staged %d writes for non-role kinds", b.Count())
	}
	if err := b.Commit(context.Background(), storage.Sync); err != nil {
		t.Fatal(err)
	}
	if w.snapshot() != before || len(rep.Counts) != 0 {
		t.Errorf("non-role kinds changed the store: %v", rep.Counts)
	}
}

// TestStageReapply_FreshBatchesGiveTheSameResult: the caller retries on
// conflict with a fresh batch; two calls over unchanged state agree, and a
// call after a conflict reads the concurrent change.
func TestStageReapply_FreshBatchesGiveTheSameResult(t *testing.T) {
	lines := []journal.Revocation{unassigned(alfred, trustee), narrowed(sleuth, "gotham:signal")}
	t.Run("unchanged_state", func(t *testing.T) {
		w := gothamWorld(t)
		b1, b2 := w.store.NewBatch(), w.store.NewBatch()
		defer func() {
			for _, b := range []*storage.Batch{b1, b2} {
				if err := b.Close(); err != nil {
					t.Errorf("close: %v", err)
				}
			}
		}()
		r1, err := rbac.StageReapply(context.Background(), reapplyDeps(w), b1, lines)
		if err != nil {
			t.Fatal(err)
		}
		r2, err := rbac.StageReapply(context.Background(), reapplyDeps(w), b2, lines)
		if err != nil {
			t.Fatal(err)
		}
		wantEqual(t, "report", r2, r1)
		if b1.Count() != b2.Count() || b1.Count() == 0 {
			t.Errorf("staged %d and %d writes", b1.Count(), b2.Count())
		}
	})
	t.Run("after_conflict", func(t *testing.T) {
		w := gothamWorld(t)
		b1 := w.store.NewBatch()
		if _, err := rbac.StageReapply(context.Background(), reapplyDeps(w), b1, lines); err != nil {
			t.Fatal(err)
		}
		w.user(alfred, true, trustee, rbac.RoleViewer, "editor") // concurrent change after the read
		err := b1.Commit(context.Background(), storage.Sync)
		if cerr := b1.Close(); cerr != nil {
			t.Error(cerr)
		}
		if !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("commit = %v, want storage.ErrConflict", err)
		}
		if _, err := stageAndCommit(t, w, reapplyDeps(w), lines); err != nil {
			t.Fatalf("retry: %v", err)
		}
		wantEqual(t, "alfred", w.rolesOf(alfred), []rbac.RoleName{rbac.RoleViewer, "editor"})
		wantEqual(t, "sleuth", w.perms(sleuth), []string{"gotham:patrol", "gotham:cave.manage"})
	})
	t.Run("role_changed_between_read_and_stage_is_a_conflict", func(t *testing.T) {
		w := gothamWorld(t)
		w.users.onScan = func(n int) { w.update(sleuth, func(r *rbac.Role) { r.Title = "revised" }) }
		b := w.store.NewBatch()
		defer func() {
			if err := b.Close(); err != nil {
				t.Error(err)
			}
		}()
		_, err := rbac.StageReapply(context.Background(), reapplyDeps(w), b, lines)
		if !errors.Is(err, storage.ErrConflict) {
			t.Errorf("err = %v, want storage.ErrConflict so the caller retries", err)
		}
	})
}

// TestStageReapply_Misuse: a missing batch or collaborator is an error.
func TestStageReapply_Misuse(t *testing.T) {
	w := gothamWorld(t)
	if _, err := rbac.StageReapply(context.Background(), reapplyDeps(w), nil, nil); err == nil {
		t.Error("nil batch: want an error")
	}
	b := w.store.NewBatch()
	defer func() {
		if err := b.Close(); err != nil {
			t.Error(err)
		}
	}()
	d := reapplyDeps(w)
	d.Users = nil
	if _, err := rbac.StageReapply(context.Background(), d, b, nil); err == nil {
		t.Error("no users: want an error")
	}
}
