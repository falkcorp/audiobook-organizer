// file: core/auth/merge_atomic_test.go
// version: 0.1.0
// guid: 108f5076-8b6e-45a8-9b16-984d4f54a4ca
// last-edited: 2026-11-21

package auth_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/internal/authstore"
	"github.com/falkcorp/mediacore/internal/rbacstore"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// F4-08 B10l atomicity spike: one batch carries a module's own rows (here a
// stand-in for books, a module with legacy claims, the batch taken from its
// LegacyRange) and core's user merge (Admin.StageUserMerge), so one Commit
// applies all of it or none of it. books' real merge writes its rows through
// LegacyRange.NewBatch (GatedDB's legacy backend), the same call as below.

// gothamBooks is the stand-in books owner: one Serialized read-write family,
// the shape of books' own user-keyed families (storage/booksclaims.go).
const gothamBooks storage.Owner = "gothambooks"

func gothamBooksSpec() storage.OwnerSpec {
	return storage.OwnerSpec{Owner: gothamBooks, Enabled: true, Spec: storage.Spec{Claims: []storage.Claim{
		{Prefix: "readstate:", Mode: storage.ClaimReadWrite, Serialized: true, Description: "per-user read state"},
		// books' user-keyed families (ubs:, upos:, playp:, upl:, ...) are plain
		// read-write claims today: only u:, role:, apikey:, invite:,
		// inviteallow:, oauthid: and sess: are Serialized (booksclaims.go).
		{Prefix: "unserialized:", Mode: storage.ClaimReadWrite, Description: "a family that is not Serialized"},
	}}}
}

// booksRow is one module row the merge re-owns: key, the bytes the preview
// read, and the bytes the merge writes.
type booksRow struct{ key, seen, next string }

type atomicFx struct {
	*fx
	lr storage.LegacyRange
}

func newAtomicFx(t *testing.T) *atomicFx {
	t.Helper()
	f, db := newFxOwners(t, nil, gothamBooksSpec())
	lr, ok := db.Legacy(gothamBooks)
	if !ok {
		t.Fatal("the stand-in books owner has no legacy range")
	}
	return &atomicFx{fx: f, lr: lr}
}

func (f *atomicFx) put(key, value string) {
	f.t.Helper()
	if err := f.lr.Set(context.Background(), []byte(key), []byte(value)); err != nil {
		f.t.Fatalf("seed %s: %v", key, err)
	}
}

func (f *atomicFx) get(key string) string {
	f.t.Helper()
	v, err := f.lr.Get(context.Background(), []byte(key))
	if err != nil {
		f.t.Fatalf("read %s: %v", key, err)
	}
	return string(v)
}

// merge stages rows and plan into one batch taken from the module's legacy
// range, then commits it once. It returns the merge's AfterCommit (nil when
// staging failed) and the first staging or commit error.
func (f *atomicFx) merge(actor *auth.Principal, plan auth.UserMergePlan, rows []booksRow) (auth.AfterCommit, error) {
	f.t.Helper()
	ctx := auth.WithPrincipal(context.Background(), actor)
	b := f.lr.NewBatch()
	defer func() { _ = b.Close() }()
	// The batch the module's rows are staged into: the merge's own.
	booksBatch := b
	for _, r := range rows {
		if err := booksBatch.Expect(f.lr, []byte(r.key), []byte(r.seen)); err != nil {
			return nil, err
		}
		if err := booksBatch.In(f.lr).Set([]byte(r.key), []byte(r.next)); err != nil {
			return nil, err
		}
	}
	after, err := f.svc.StageUserMerge(ctx, b, plan)
	if err != nil {
		return nil, err
	}
	if booksBatch != b {
		if err := booksBatch.Commit(ctx, storage.Sync); err != nil {
			return after, err
		}
	}
	return after, b.Commit(ctx, storage.Sync)
}

// mergeScene is Bruce Wayne merging Jason Todd into Dick Grayson: Jason owns
// one API key and two read-state rows.
type mergeScene struct {
	bruce, jason, dick *auth.User
	key                *auth.APIKey
	plan               auth.UserMergePlan
	rows               []booksRow
	epoch              uint64
}

func (f *atomicFx) scene() mergeScene {
	f.t.Helper()
	s := mergeScene{}
	s.bruce = f.user("bruce-wayne", rbac.RoleAdmin)
	s.jason = f.user("jason-todd", roleCommissioner)
	s.dick = f.user("dick-grayson", "gotham.detective")
	_, s.key = f.mint(f.session(s.jason), s.jason.ID, "gotham:patrol")
	s.epoch = uint64(f.reload(s.jason.ID).CredentialEpoch)
	s.plan = auth.UserMergePlan{Source: s.jason.ID, Target: s.dick.ID, Rows: []auth.MergeRow{f.keyRow(s.key.ID)}}
	s.rows = []booksRow{
		{key: "readstate:jason:b1", seen: "jason:page-12", next: "dick:page-12"},
		{key: "readstate:jason:b2", seen: "jason:page-40", next: "dick:page-40"},
	}
	for _, r := range s.rows {
		f.put(r.key, r.seen)
	}
	f.inval.take()
	return s
}

// assertNothingLanded is the none half of all-or-nothing: no credential of
// Jason's re-owned, Jason still active at his epoch, no module row moved.
func (f *atomicFx) assertNothingLanded(s mergeScene) {
	f.t.Helper()
	if got := f.key(s.key.ID).Owner; got != s.jason.ID {
		f.t.Errorf("key %s owner = %s after a failed merge, want %s", s.key.ID, got, s.jason.ID)
	}
	src := f.reload(s.jason.ID)
	if src.Status != auth.StatusActive || uint64(src.CredentialEpoch) != s.epoch {
		f.t.Errorf("source after a failed merge: status %s epoch %d, want active at %d", src.Status, src.CredentialEpoch, s.epoch)
	}
	for _, r := range s.rows {
		if got := f.get(r.key); got != r.seen {
			f.t.Errorf("%s = %q after a failed merge, want %q", r.key, got, r.seen)
		}
	}
}

// TestMergeAtomic_HappyPath: the legacy-range batch accepts the merge's
// staging (Bind admits core/auth's keyspace of the same store), and one Commit
// lands the module's rows, the re-owned key, the disabled source, and an
// AfterCommit that publishes.
func TestMergeAtomic_HappyPath(t *testing.T) {
	f := newAtomicFx(t)
	s := f.scene()

	after, err := f.merge(f.signedIn(s.bruce), s.plan, s.rows)
	if err != nil {
		t.Fatalf("one batch over the module's legacy range and the merge: %v", err)
	}
	after(context.Background())

	for _, r := range s.rows {
		if got := f.get(r.key); got != r.next {
			t.Errorf("%s = %q, want %q", r.key, got, r.next)
		}
	}
	if got := f.key(s.key.ID).Owner; got != s.dick.ID {
		t.Errorf("key %s owner = %s, want %s", s.key.ID, got, s.dick.ID)
	}
	src := f.reload(s.jason.ID)
	if src.Status != auth.StatusDisabled || uint64(src.CredentialEpoch) != s.epoch+1 {
		t.Errorf("source after the merge: status %s epoch %d, want disabled at %d", src.Status, src.CredentialEpoch, s.epoch+1)
	}
	got := f.inval.take()
	if len(got) != 1 || got[0].User != string(s.jason.ID) {
		t.Errorf("published %v, want one user-wide invalidation of %s", got, s.jason.ID)
	}
}

// TestMergeAtomic_ParticipantsAreTheModuleAuthAndRBAC: the one batch is bound
// to the module's range, to core/auth's keyspace and to core/rbac's (the
// last-admin check guards the role-set generation, GuardRoleSet), and to
// nothing else.
func TestMergeAtomic_ParticipantsAreTheModuleAuthAndRBAC(t *testing.T) {
	f := newAtomicFx(t)
	s := f.scene()
	ctx := auth.WithPrincipal(context.Background(), f.signedIn(s.bruce))
	b := f.lr.NewBatch()
	defer func() { _ = b.Close() }()
	if _, err := f.svc.StageUserMerge(ctx, b, s.plan); err != nil {
		t.Fatalf("StageUserMerge into the legacy-range batch: %v", err)
	}
	got := b.Participants()
	slices.Sort(got)
	want := []storage.Owner{authstore.Owner, rbacstore.Owner, gothamBooks}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("participants = %v, want %v", got, want)
	}
}

// TestMergeAtomic_ModuleRowConflictLandsNothing: a module row changed after
// the preview read it fails the one Commit with ErrConflict, and then no
// credential is re-owned, the source stays active, no other module row moves,
// and AfterCommit run anyway publishes nothing.
func TestMergeAtomic_ModuleRowConflictLandsNothing(t *testing.T) {
	f := newAtomicFx(t)
	s := f.scene()
	f.put(s.rows[1].key, "jason:page-41") // a write behind the preview's back

	after, err := f.merge(f.signedIn(s.bruce), s.plan, s.rows)
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("merge with a changed module row = %v, want storage.ErrConflict", err)
	}
	if after == nil {
		t.Fatal("staging succeeded, so the merge must have handed back its AfterCommit")
	}
	after(context.Background())

	s.rows[1].seen = "jason:page-41" // the value the writer left
	f.assertNothingLanded(s)
	if got := f.inval.take(); len(got) != 0 {
		t.Errorf("AfterCommit after a failed commit published %v, want nothing", got)
	}
}

// TestMergeAtomic_AuthRowConflictLandsNothing: a credential row changed after
// the preview fails the one Commit, and no module row moves.
func TestMergeAtomic_AuthRowConflictLandsNothing(t *testing.T) {
	f := newAtomicFx(t)
	s := f.scene()
	renamed := f.key(s.key.ID)
	renamed.Name, renamed.Version = "grappling hook", renamed.Version+1
	f.commit(func(b *storage.Batch) error { return f.st.PutAPIKey(b, renamed) })

	after, err := f.merge(f.signedIn(s.bruce), s.plan, s.rows)
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("merge with a changed credential row = %v, want storage.ErrConflict", err)
	}
	if after == nil {
		t.Fatal("staging succeeded, so the merge must have handed back its AfterCommit")
	}
	after(context.Background())

	if got := f.key(s.key.ID); got.Owner != s.jason.ID || !bytes.Equal([]byte(got.Name), []byte("grappling hook")) {
		t.Errorf("key after a failed merge: owner %s name %q, want %s and the writer's rename", got.Owner, got.Name, s.jason.ID)
	}
	src := f.reload(s.jason.ID)
	if src.Status != auth.StatusActive || uint64(src.CredentialEpoch) != s.epoch {
		t.Errorf("source after a failed merge: status %s epoch %d, want active at %d", src.Status, src.CredentialEpoch, s.epoch)
	}
	for _, r := range s.rows {
		if got := f.get(r.key); got != r.seen {
			t.Errorf("%s = %q after a failed merge, want %q", r.key, got, r.seen)
		}
	}
	if got := f.inval.take(); len(got) != 0 {
		t.Errorf("AfterCommit after a failed commit published %v, want nothing", got)
	}
}

// TestMergeAtomic_ExpectNeedsASerializedFamily pins the limit the port meets:
// a module row can be guarded by Batch.Expect only in a Serialized claim. In a
// plain read-write claim (books' ubs:, upos:, playp:, upl:, col: today) Expect
// is refused with storage.ErrNotSerialized and poisons the batch, so the
// merge's Commit fails whole instead of landing unguarded.
func TestMergeAtomic_ExpectNeedsASerializedFamily(t *testing.T) {
	f := newAtomicFx(t)
	s := f.scene()
	f.put("unserialized:jason:b1", "jason:page-12")
	ctx := auth.WithPrincipal(context.Background(), f.signedIn(s.bruce))
	b := f.lr.NewBatch()
	defer func() { _ = b.Close() }()

	if err := b.Expect(f.lr, []byte("unserialized:jason:b1"), []byte("jason:page-12")); !errors.Is(err, storage.ErrNotSerialized) {
		t.Fatalf("Expect in a family that is not Serialized = %v, want storage.ErrNotSerialized", err)
	}
	if _, err := f.svc.StageUserMerge(ctx, b, s.plan); err == nil {
		if err := b.Commit(ctx, storage.Sync); err == nil {
			t.Fatal("a batch poisoned by the refused Expect committed")
		}
	}
	if got := f.key(s.key.ID).Owner; got != s.jason.ID {
		t.Errorf("key %s owner = %s after the poisoned batch, want %s", s.key.ID, got, s.jason.ID)
	}
}
