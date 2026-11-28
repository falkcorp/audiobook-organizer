// file: internal/usermerge/identity_test.go
// version: 0.1.0
// guid: 6b1d8e47-3a92-4c05-9f7e-d20a5c4b8e19
// last-edited: 2026-11-21

package usermerge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Credential-shaped strings the fixture plants in rows books must not read or
// write once accounts are core's. None of them may reach a preview, a journal
// or a grant's text.
const (
	credHash       = "hash-b"
	credSubject    = "google-67890"
	credCredential = "sess-batman-credential-id"
)

// fakeCore is core's side of the merge: it holds the accounts, answers
// PlanUserMerge and StageUserMerge, and keeps its one write (a marker row) in
// the caller's batch, so whether the merge landed is whether the marker is in
// the books store. It checks what core checks that the service must hand it: a
// principal in ctx, and its refusals by the codes core uses.
type fakeCore struct {
	// Admin is embedded so the fake keeps compiling when core adds methods to
	// auth.Admin; a method this fake does not define would panic (nil), and the
	// ones the tests use (PlanUserMerge, StageUserMerge) are defined below.
	coreauth.Admin
	t     *testing.T
	store *database.PebbleStore

	mu       sync.Mutex
	users    map[string]*coreauth.User
	summary  coreauth.MergeSummary
	planErr  error
	stageErr error
	// onStage runs inside StageUserMerge with the batch it was handed.
	onStage func(b *storage.Batch)

	planned, staged, after atomic.Int32
	lastSrc, lastTgt       string
	seen                   []*coreauth.Principal

	// The unmerge side (undo_identity_test.go): the record the last
	// PlanUserUnmerge was handed, the errors the two calls return, and how many
	// times each ran.
	unmergePlanErr, unmergeStageErr error
	unplanned, unstaged, unafter    atomic.Int32
	lastRec                         coreauth.UnmergeRecord

	// footprint and footprintErr are what AdminAccountFootprint answers
	// (footprint_identity_test.go); footprintSeen is the principal each call saw.
	footprint     coreauth.AccountFootprint
	footprintErr  error
	footprintSeen []*coreauth.Principal
}

func markerKey(src string) string { return "pref:fakecore:merged:" + src }

func (c *fakeCore) user(id string) *coreauth.User {
	c.mu.Lock()
	defer c.mu.Unlock()
	u, ok := c.users[id]
	if !ok {
		return nil
	}
	cp := *u
	cp.Roles = slices.Clone(u.Roles)
	if v, _ := c.store.GetRaw(markerKey(id)); v != nil {
		cp.MergedInto, cp.Status, cp.Version = coreauth.UserID(v), coreauth.StatusDisabled, cp.Version+1
	}
	return &cp
}

func (c *fakeCore) User(_ context.Context, id coreauth.UserID) (*coreauth.User, error) {
	if u := c.user(string(id)); u != nil {
		return u, nil
	}
	return nil, coreauth.ErrNotFound
}

func (c *fakeCore) UserByUsername(context.Context, string) (*coreauth.User, error) {
	return nil, coreauth.ErrNotFound
}

func (c *fakeCore) Users(context.Context, coreauth.UserFilter) iter.Seq2[*coreauth.User, error] {
	return func(func(*coreauth.User, error) bool) {}
}

func (c *fakeCore) PrincipalFrom(ctx context.Context) (*coreauth.Principal, bool) {
	return coreauth.PrincipalFrom(ctx)
}

func (c *fakeCore) PlanUserMerge(ctx context.Context, source, target coreauth.UserID) (coreauth.UserMergePlan, coreauth.MergeSummary, error) {
	c.planned.Add(1)
	p, ok := coreauth.PrincipalFrom(ctx)
	if !ok || p.User == nil {
		return coreauth.UserMergePlan{}, coreauth.MergeSummary{}, fmt.Errorf("%w: no principal", coreauth.ErrDenied)
	}
	c.mu.Lock()
	c.seen = append(c.seen, p)
	c.lastSrc, c.lastTgt = string(source), string(target)
	planErr, sum := c.planErr, c.summary
	c.mu.Unlock()
	if planErr != nil {
		return coreauth.UserMergePlan{}, coreauth.MergeSummary{}, planErr
	}
	if u := c.user(string(source)); u != nil && u.MergedInto != "" {
		return coreauth.UserMergePlan{}, coreauth.MergeSummary{}, &coreauth.MergeRefusal{Code: coreauth.RefusedAlreadyMerged, Detail: "the source was already merged into another account"}
	}
	sum.Source, sum.Target = source, target
	return coreauth.UserMergePlan{}, sum, nil
}

func (c *fakeCore) StageUserMerge(ctx context.Context, b *storage.Batch, _ coreauth.UserMergePlan) (coreauth.AfterCommit, error) {
	c.staged.Add(1)
	p, ok := coreauth.PrincipalFrom(ctx)
	if !ok || p.User == nil {
		return nil, fmt.Errorf("%w: no principal", coreauth.ErrDenied)
	}
	c.mu.Lock()
	c.seen = append(c.seen, p)
	src, tgt, stageErr, onStage := c.lastSrc, c.lastTgt, c.stageErr, c.onStage
	c.mu.Unlock()
	if stageErr != nil {
		return nil, stageErr
	}
	if b != nil {
		if err := b.Set([]byte(markerKey(src)), []byte(tgt)); err != nil {
			return nil, err
		}
		if onStage != nil {
			onStage(b)
		}
	}
	return func(context.Context) { c.after.Add(1) }, nil
}

// idFixture is the HEAD fixture's store and seeded rows with a service wired
// for core identity over a fakeCore.
type idFixture struct {
	*fixture
	core *fakeCore
	pr   *coreauth.Principal
}

func newIDFixture(t *testing.T) *idFixture {
	t.Helper()
	f := newFixture(t)
	core := &fakeCore{t: t, store: f.s, users: map[string]*coreauth.User{
		f.a.ID:     {ID: coreauth.UserID(f.a.ID), Username: "keeper", Status: coreauth.StatusActive, Roles: []rbac.RoleName{rbac.RoleAdmin}, Version: 3, CredentialEpoch: 1},
		f.b.ID:     {ID: coreauth.UserID(f.b.ID), Username: "absorbed", Status: coreauth.StatusActive, Roles: []rbac.RoleName{rbac.RoleAdmin, rbac.RoleEditor}, Version: 5, CredentialEpoch: 2, Email: "absorbed@admins.example.com"},
		"u-alfred": {ID: "u-alfred", Username: "alfred", Status: coreauth.StatusActive, Roles: []rbac.RoleName{rbac.RoleAdmin}, Version: 1},
	}, summary: coreauth.MergeSummary{
		RolesTransferred: []rbac.RoleName{rbac.RoleEditor}, TargetHoldsAdmin: true, Email: coreauth.MergeEmailKept,
		APIKeys: []string{"key-robin"}, IdentityProviders: []string{"gotham-sso"}, AllowListRows: 1,
	}}
	refresh := func(_ context.Context, p *coreauth.Principal) (*coreauth.Principal, error) {
		np := *p
		return &np, nil
	}
	svc, err := New(f.s,
		WithIdentityFlipped(func() bool { return true }),
		WithCoreIdentity(CoreIdentity{Admin: core, Users: core, Refresh: refresh}))
	if err != nil {
		t.Fatal(err)
	}
	svc.ownerEmail = func() string { return ownerAddr }
	svc.backfillUserID = func() string { return "" }
	f.svc = svc
	pr := &coreauth.Principal{User: core.user(f.a.ID), CredentialID: credCredential}
	return &idFixture{fixture: f, core: core, pr: pr}
}

func (f *idFixture) ctx() context.Context {
	return coreauth.WithPrincipal(context.Background(), f.pr)
}

func (f *idFixture) plan() *Plan {
	f.t.Helper()
	p, err := f.svc.BuildPlan(f.ctx(), f.planReq())
	if err != nil {
		f.t.Fatalf("BuildPlan: %v", err)
	}
	return p
}

func (f *idFixture) applyReq(p *Plan) ApplyRequest {
	return ApplyRequest{ActorID: f.a.ID, SurvivorID: f.a.ID, AbsorbedID: f.b.ID, Digest: p.Digest,
		AcknowledgeOwnerIdentity: true, Principal: f.pr}
}

func (f *idFixture) journalRows() []database.KVPair {
	f.t.Helper()
	kv, err := f.s.ScanPrefix(database.UserMergeJournalPrefix)
	if err != nil {
		f.t.Fatal(err)
	}
	return kv
}

// unchanged fails the test when any key but the journal (and the fake core's
// marker) differs from before.
func (f *idFixture) unchanged(before map[string]string) {
	f.t.Helper()
	got := f.snapshot()
	for k, v := range got {
		if old, ok := before[k]; !ok || old != v {
			f.t.Errorf("a refused or failed merge changed %q", k)
		}
	}
	for k := range before {
		if _, ok := got[k]; !ok {
			f.t.Errorf("a refused or failed merge deleted %q", k)
		}
	}
}

// coreOwned are the books keys of the families core owns once accounts are its
// own: no identity-era merge reads or writes them.
func coreOwned(key string) bool {
	for _, p := range []string{"u:", "oauthid:", "idx:oauth:", "apikey:", "idx:apikey:", "invite:", "inviteallow:", "role:", "sess:"} {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// decorated is the real committer with a hook around ApplyUserMergeIdentity.
type decorated struct {
	database.IdentityUserMerger
	apply func(ops []database.UserMergeOp, key string, journal []byte, stage func(*storage.Batch) error) error
}

func (d decorated) ApplyUserMergeIdentity(ops []database.UserMergeOp, key string, journal []byte, stage func(*storage.Batch) error) error {
	if d.apply != nil {
		return d.apply(ops, key, journal, stage)
	}
	return d.IdentityUserMerger.ApplyUserMergeIdentity(ops, key, journal, stage)
}

func TestMergeIdentityPlan_ShowsCoreSummaryAndNoCredentialBytes(t *testing.T) {
	f := newIDFixture(t)
	before := f.snapshot()
	p := f.plan()
	if !p.Applicable() {
		t.Fatalf("refusals: %v", p.Refusals)
	}
	if p.Core == nil || !slices.Equal(p.Core.RolesTransferred, []string{"editor"}) || p.Core.Email != "kept" ||
		!slices.Equal(p.Core.APIKeyIDs, []string{"key-robin"}) || !slices.Equal(p.Core.IdentityProviders, []string{"gotham-sso"}) || p.Core.AllowListRows != 1 {
		t.Fatalf("core summary = %+v", p.Core)
	}
	if !p.OwnerIdentityAckRequired {
		t.Errorf("a sign-in moves and the plan cannot tell whether it is the owner's, so the acknowledgement must be required")
	}
	if p.Digest == "" || f.core.planned.Load() != 1 {
		t.Errorf("digest %q, core planned %d times", p.Digest, f.core.planned.Load())
	}
	// No credential reaches the preview: books reads none of the rows it no
	// longer owns, so none of the planted credential material is in the JSON, and
	// the families core owns are not in the counts.
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{credHash, credSubject, "gh-12345", ownerAddr, "kB", "idB", "tokB", "friend@example.com"} {
		if bytes.Contains(raw, []byte(needle)) {
			t.Errorf("the preview carries %q: %s", needle, raw)
		}
	}
	for _, fam := range []string{FamOAuthIdentities, FamAPIKeys, FamInvites, FamInviteGrants} {
		if _, ok := p.Counts[fam]; ok {
			t.Errorf("the identity preview counts the core-owned family %s", fam)
		}
	}
	if len(p.AbsorbedIdentities) != 0 || len(p.SurvivorIdentities) != 0 {
		t.Errorf("sign-ins listed from books' stale copy: %+v %+v", p.SurvivorIdentities, p.AbsorbedIdentities)
	}
	f.unchanged(before)
	if len(f.journalRows()) != 0 || f.core.staged.Load() != 0 {
		t.Errorf("a preview wrote a journal or staged core's half")
	}
}

func TestMergeIdentityApply_OneBatchMovesBooksCoreAndJournal(t *testing.T) {
	f := newIDFixture(t)
	before := f.snapshot()
	p := f.plan()
	j, err := f.svc.Apply(context.Background(), f.applyReq(p), nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if j.Status != StatusApplied || !j.Retired || j.Core == nil || j.Core.Era != "identity" || j.Cursor != len(j.Ops) {
		t.Fatalf("journal = %+v", j.Summary())
	}
	if got := f.get(markerKey(f.b.ID)); got != f.a.ID {
		t.Errorf("core's half did not land with the books rows: marker %q", got)
	}
	if f.core.staged.Load() != 1 || f.core.after.Load() != 1 {
		t.Errorf("StageUserMerge ran %d times and AfterCommit %d times, want 1 and 1", f.core.staged.Load(), f.core.after.Load())
	}
	// The journal on disk is the batch's own record.
	rows := f.journalRows()
	if len(rows) != 1 {
		t.Fatalf("journals = %d, want 1", len(rows))
	}
	var disk Journal
	if err := json.Unmarshal(rows[0].Value, &disk); err != nil || disk.ID != j.ID || disk.Core == nil || disk.Status != StatusApplied {
		t.Fatalf("journal on disk = %+v (%v)", disk, err)
	}
	// Books moved its own rows.
	if f.get("pref:"+f.b.ID+":speed") != "" || f.get("pref:"+f.a.ID+":speed") == "" {
		t.Errorf("the absorbed account's preference row did not move")
	}
	// No row of a core-owned family was read into a write.
	for k, v := range f.snapshot() {
		if coreOwned(k) && before[k] != v {
			t.Errorf("an identity merge wrote the core-owned key %q", k)
		}
	}
	for _, op := range j.Ops {
		if coreOwned(op.Key) {
			t.Errorf("the journal holds an op on the core-owned key %q", op.Key)
		}
	}
}

func TestMergeIdentityApply_StagingFailureLeavesBooksAndCoreUntouched(t *testing.T) {
	f := newIDFixture(t)
	p := f.plan()
	before := f.snapshot()
	f.core.stageErr = errors.New("gotham: stage refused")
	if _, err := f.svc.Apply(context.Background(), f.applyReq(p), nil); err == nil || !strings.Contains(err.Error(), "stage refused") {
		t.Fatalf("Apply = %v", err)
	}
	f.unchanged(before)
	if len(f.journalRows()) != 0 || f.core.after.Load() != 0 {
		t.Errorf("a failed staging left a journal (%d) or ran AfterCommit (%d)", len(f.journalRows()), f.core.after.Load())
	}
}

// The commit itself fails (the batch is closed under it after staging): none of
// books' rows, the journal or core's marker lands, and AfterCommit never runs.
func TestMergeIdentityApply_CommitFailureLandsNothingAndRunsNoAfterCommit(t *testing.T) {
	f := newIDFixture(t)
	p := f.plan()
	before := f.snapshot()
	f.core.onStage = func(b *storage.Batch) { _ = b.Close() }
	_, err := f.svc.Apply(context.Background(), f.applyReq(p), nil)
	if err == nil {
		t.Fatal("Apply succeeded over a failed commit")
	}
	f.unchanged(before)
	if f.core.staged.Load() != 1 {
		t.Errorf("staged %d times", f.core.staged.Load())
	}
	if f.core.after.Load() != 0 {
		t.Errorf("AfterCommit ran %d time(s) for a commit that failed", f.core.after.Load())
	}
	if len(f.journalRows()) != 0 {
		t.Errorf("a failed commit left a journal")
	}
}

func TestMergeIdentityApply_BooksRowChangedAfterThePlanIsAConflictAndWritesNothing(t *testing.T) {
	f := newIDFixture(t)
	p := f.plan()
	// A writer changes a row the plan will write, after the plan was read under
	// the locks and before the compare: the books CAS must catch it.
	real := f.svc.idMerger
	f.svc.idMerger = decorated{IdentityUserMerger: real, apply: func(ops []database.UserMergeOp, key string, journal []byte, stage func(*storage.Batch) error) error {
		f.put("pref:"+f.a.ID+":theme", "{\"key\":\"theme\",\"value\":\"changed-by-a-writer\"}")
		f.put("bookmark:"+f.b.ID+":item1:10", map[string]any{"UserID": f.b.ID, "ItemID": "item1", "TimeSec": 10, "Title": "edited"})
		return real.ApplyUserMergeIdentity(ops, key, journal, stage)
	}}
	before := f.snapshot()
	_, err := f.svc.Apply(context.Background(), f.applyReq(p), nil)
	if !errors.Is(err, ErrDigestMismatch) || !errors.Is(err, database.ErrUserMergeConflict) {
		t.Fatalf("Apply = %v, want ErrDigestMismatch wrapping the storage conflict", err)
	}
	if f.get("pref:"+f.a.ID+":theme") != "{\"key\":\"theme\",\"value\":\"changed-by-a-writer\"}" {
		t.Errorf("the writer's row was overwritten")
	}
	if f.core.staged.Load() != 0 || f.core.after.Load() != 0 || len(f.journalRows()) != 0 {
		t.Errorf("staged %d, after %d, journals %d after a conflict", f.core.staged.Load(), f.core.after.Load(), len(f.journalRows()))
	}
	_ = before
	if f.get(markerKey(f.b.ID)) != "" {
		t.Errorf("core's half landed")
	}
}

func TestMergeIdentityApply_CoreSideChangeIsAConflictAndWritesNothing(t *testing.T) {
	t.Run("core changed after the preview", func(t *testing.T) {
		f := newIDFixture(t)
		p := f.plan()
		f.core.summary.APIKeys = []string{"key-robin", "key-created-after-the-preview"}
		before := f.snapshot()
		if _, err := f.svc.Apply(context.Background(), f.applyReq(p), nil); !errors.Is(err, ErrDigestMismatch) {
			t.Fatalf("Apply = %v, want ErrDigestMismatch", err)
		}
		f.unchanged(before)
		if f.core.staged.Load() != 0 {
			t.Errorf("staged core's half over a stale preview")
		}
	})
	t.Run("core's own pin fails at staging", func(t *testing.T) {
		f := newIDFixture(t)
		p := f.plan()
		f.core.stageErr = fmt.Errorf("%w: a row changed", coreauth.ErrMergeConflict)
		before := f.snapshot()
		if _, err := f.svc.Apply(context.Background(), f.applyReq(p), nil); !errors.Is(err, ErrDigestMismatch) || !errors.Is(err, coreauth.ErrMergeConflict) {
			t.Fatalf("Apply = %v, want ErrDigestMismatch wrapping ErrMergeConflict", err)
		}
		f.unchanged(before)
		if f.core.after.Load() != 0 {
			t.Errorf("AfterCommit ran for a refused merge")
		}
	})
}

// A plan asked for with no principal in the context (the operation's dry-run
// preview, which has none) is denied by core and writes nothing: the preview
// fails closed, as the maintenance op's comment says.
func TestMergeIdentityPlan_WithoutAPrincipalCoreDeniesAndNothingIsWritten(t *testing.T) {
	f := newIDFixture(t)
	_, err := f.svc.BuildPlan(context.Background(), f.planReq())
	if !errors.Is(err, coreauth.ErrDenied) {
		t.Fatalf("BuildPlan without a principal = %v, want core's denial", err)
	}
	if len(f.journalRows()) != 0 || f.core.staged.Load() != 0 {
		t.Error("a denied preview wrote something")
	}
}

func TestMergeIdentityPlan_SurvivorMustBeAnAdmin(t *testing.T) {
	f := newIDFixture(t)
	f.core.users[f.a.ID].Roles = []rbac.RoleName{rbac.RoleEditor}
	p := f.plan()
	if p.Applicable() || !slices.Contains(p.Refusals, "the surviving account is not an admin") {
		t.Fatalf("a survivor that is not an admin must be refused by books itself: %v", p.Refusals)
	}
	if f.core.planned.Load() != 0 {
		t.Errorf("a merge books refuses reached core's planner")
	}
	if _, err := f.svc.Apply(context.Background(), ApplyRequest{ActorID: f.a.ID, SurvivorID: f.a.ID, AbsorbedID: f.b.ID, Digest: p.Digest, Principal: f.pr}, nil); !errors.Is(err, ErrRefused) {
		t.Errorf("Apply = %v, want a refusal", err)
	}
}

func TestMergeIdentityPlan_EveryCoreRefusalIsAPlanRefusalWithItsCode(t *testing.T) {
	codes := map[string]error{
		coreauth.RefusedAlreadyMerged: &coreauth.MergeRefusal{Code: coreauth.RefusedAlreadyMerged, Detail: "already merged"},
		coreauth.RefusedNoSignIn:      &coreauth.MergeRefusal{Code: coreauth.RefusedNoSignIn, Detail: "no sign-in"},
		coreauth.RefusedEmailLinkable: &coreauth.MergeRefusal{Code: coreauth.RefusedEmailLinkable, Detail: "linkable"},
		CodeLastAdmin:                 coreauth.ErrLastAdmin,
		CodeEmailInUse:                coreauth.ErrEmailInUse,
		CodeInactiveUser:              coreauth.ErrInactiveUser,
	}
	for code, planErr := range codes {
		t.Run(code, func(t *testing.T) {
			f := newIDFixture(t)
			f.core.planErr = planErr
			p := f.plan()
			if p.Applicable() || !slices.Equal(p.RefusalCodes, []string{code}) {
				t.Fatalf("refusals %v codes %v", p.Refusals, p.RefusalCodes)
			}
			if _, err := f.svc.Apply(context.Background(), ApplyRequest{ActorID: f.a.ID, SurvivorID: f.a.ID, AbsorbedID: f.b.ID, Digest: "x", Principal: f.pr}, nil); !errors.Is(err, ErrRefused) {
				t.Errorf("Apply = %v", err)
			}
			// The same refusal decided at staging (after a plan that was fine)
			// is a refusal carrying the code, not a bare error.
			g := newIDFixture(t)
			gp := g.plan()
			g.core.stageErr = planErr
			_, err := g.svc.Apply(context.Background(), g.applyReq(gp), nil)
			var re *RefusedError
			if !errors.As(err, &re) || !slices.Equal(re.Plan.RefusalCodes, []string{code}) {
				t.Errorf("a staging-time refusal = %v", err)
			}
			if g.core.after.Load() != 0 || len(g.journalRows()) != 0 {
				t.Errorf("a refused merge left a journal or ran AfterCommit")
			}
		})
	}
}

func TestMergeIdentity_TooLargeAnswersBeforeAnythingIsStagedOrWritten(t *testing.T) {
	f := newIDFixture(t)
	p := f.plan()
	if MergeBatchCap != 256<<20 {
		t.Fatalf("MergeBatchCap = %d", MergeBatchCap)
	}
	f.svc.batchCap = 1 // lowered through the test hook: no 256 MiB fixture
	before := f.snapshot()
	if _, err := f.svc.BuildPlan(f.ctx(), f.planReq()); !errors.Is(err, ErrMergeTooLarge) {
		t.Errorf("BuildPlan = %v, want ErrMergeTooLarge", err)
	}
	if _, err := f.svc.Apply(context.Background(), f.applyReq(p), nil); !errors.Is(err, ErrMergeTooLarge) {
		t.Fatalf("Apply = %v, want ErrMergeTooLarge", err)
	}
	f.unchanged(before)
	if f.core.staged.Load() != 0 || f.core.after.Load() != 0 || len(f.journalRows()) != 0 {
		t.Errorf("staged %d, after %d, journals %d for a merge over the cap", f.core.staged.Load(), f.core.after.Load(), len(f.journalRows()))
	}
}

// Two applies of the same source into two survivors: runMu serializes them and
// exactly one lands; the other is refused. Run under -race.
func TestMergeIdentityApply_TwoSurvivorsRaceExactlyOneLands(t *testing.T) {
	f := newIDFixture(t)
	survivors := []string{f.a.ID, "u-alfred"}
	reqs := make([]ApplyRequest, 2)
	for i, sv := range survivors {
		pr := &coreauth.Principal{User: f.core.user(sv)}
		ctx := coreauth.WithPrincipal(context.Background(), pr)
		plan, err := f.svc.BuildPlan(ctx, PlanRequest{ActorID: sv, SurvivorID: sv, AbsorbedID: f.b.ID})
		if err != nil || !plan.Applicable() {
			t.Fatalf("plan for %s: %v %v", sv, err, plan)
		}
		reqs[i] = ApplyRequest{ActorID: sv, SurvivorID: sv, AbsorbedID: f.b.ID, Digest: plan.Digest, AcknowledgeOwnerIdentity: true, Principal: pr}
	}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range reqs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.svc.Apply(context.Background(), reqs[i], nil)
		}()
	}
	wg.Wait()
	landed := 0
	for _, err := range errs {
		switch {
		case err == nil:
			landed++
		case errors.Is(err, ErrRefused), errors.Is(err, ErrDigestMismatch):
		default:
			t.Errorf("the loser answered %v, want a refusal or a conflict", err)
		}
	}
	if landed != 1 || len(f.journalRows()) != 1 || f.core.after.Load() != 1 {
		t.Errorf("landed %d, journals %d, AfterCommit %d, want 1 each", landed, len(f.journalRows()), f.core.after.Load())
	}
	if got := f.get(markerKey(f.b.ID)); got != survivors[0] && got != survivors[1] {
		t.Errorf("marker %q", got)
	}
}

func TestMergeIdentityApply_ActsAsTheRefreshedPrincipalOrNotAtAll(t *testing.T) {
	t.Run("ctx carries what Refresh returned, never the grant's copy", func(t *testing.T) {
		f := newIDFixture(t)
		p := f.plan()
		f.core.mu.Lock()
		f.core.seen = nil
		f.core.mu.Unlock()
		refreshed := &coreauth.Principal{User: f.core.user(f.a.ID), CredentialID: "refreshed"}
		f.svc.core.Refresh = func(context.Context, *coreauth.Principal) (*coreauth.Principal, error) { return refreshed, nil }
		if _, err := f.svc.Apply(context.Background(), f.applyReq(p), nil); err != nil {
			t.Fatal(err)
		}
		if len(f.core.seen) == 0 {
			t.Fatal("core saw no principal")
		}
		for _, seen := range f.core.seen {
			if seen != refreshed {
				t.Errorf("core was handed a principal that Refresh did not return")
			}
		}
	})
	t.Run("a session revoked or an epoch moved between enqueue and redeem refuses", func(t *testing.T) {
		f := newIDFixture(t)
		p := f.plan()
		f.svc.core.Refresh = func(context.Context, *coreauth.Principal) (*coreauth.Principal, error) {
			return nil, coreauth.ErrRevoked
		}
		before := f.snapshot()
		_, err := f.svc.Apply(context.Background(), f.applyReq(p), nil)
		if !errors.Is(err, ErrNoPrincipal) || !errors.Is(err, coreauth.ErrDenied) {
			t.Fatalf("Apply = %v, want ErrNoPrincipal (a denial)", err)
		}
		f.unchanged(before)
		if f.core.planned.Load() != 1 || f.core.staged.Load() != 0 {
			t.Errorf("core planned %d / staged %d: a refused principal reached core", f.core.planned.Load(), f.core.staged.Load())
		}
	})
	t.Run("a principal with a tampered field is refused and the grant's copy is never used", func(t *testing.T) {
		// Core's Refresh believes a principal's exported fields only while they
		// are what core sealed (checkSeal; core/auth/refresh_forgery_test.go pins
		// that). This Refresh stands in for the seal: it refuses a principal whose
		// credential id is not the one it issued. What is pinned here is books'
		// side: a refusal is final, with no fallback to the principal the grant
		// carries, and core is never asked.
		f := newIDFixture(t)
		p := f.plan()
		issued := f.pr.CredentialID
		f.svc.core.Refresh = func(_ context.Context, pr *coreauth.Principal) (*coreauth.Principal, error) {
			if pr.CredentialID != issued {
				return nil, fmt.Errorf("%w: principal does not match what auth sealed", coreauth.ErrInvalidCredentials)
			}
			return pr, nil
		}
		req := f.applyReq(p)
		forged := *f.pr
		forged.CredentialID = "sess-forged"
		req.Principal = &forged
		before := f.snapshot()
		planned := f.core.planned.Load()
		if _, err := f.svc.Apply(context.Background(), req, nil); !errors.Is(err, ErrNoPrincipal) {
			t.Fatalf("Apply = %v, want ErrNoPrincipal", err)
		}
		f.unchanged(before)
		if f.core.planned.Load() != planned || f.core.staged.Load() != 0 {
			t.Errorf("core was asked on behalf of a principal its seal refused")
		}
	})
	t.Run("a store that cannot answer is not a denial", func(t *testing.T) {
		f := newIDFixture(t)
		p := f.plan()
		f.svc.core.Refresh = func(context.Context, *coreauth.Principal) (*coreauth.Principal, error) {
			return nil, coreauth.ErrStoreUnavailable
		}
		_, err := f.svc.Apply(context.Background(), f.applyReq(p), nil)
		if !errors.Is(err, coreauth.ErrStoreUnavailable) || errors.Is(err, ErrNoPrincipal) || errors.Is(err, coreauth.ErrDenied) {
			t.Fatalf("Apply = %v, want the store failure kept apart from a denial", err)
		}
	})
	t.Run("an actor demoted between enqueue and redeem is refused by core", func(t *testing.T) {
		f := newIDFixture(t)
		p := f.plan()
		f.core.planErr = fmt.Errorf("%w: users.manage is gone", coreauth.ErrDenied)
		before := f.snapshot()
		if _, err := f.svc.Apply(context.Background(), f.applyReq(p), nil); !errors.Is(err, coreauth.ErrDenied) {
			t.Fatalf("Apply = %v, want core's denial", err)
		}
		f.unchanged(before)
	})
	t.Run("no principal, or one for another user, is refused before core is asked", func(t *testing.T) {
		f := newIDFixture(t)
		p := f.plan()
		planned := f.core.planned.Load()
		other := &coreauth.Principal{User: f.core.user(f.b.ID)}
		refreshCalls := 0
		f.svc.core.Refresh = func(_ context.Context, pr *coreauth.Principal) (*coreauth.Principal, error) {
			refreshCalls++
			return pr, nil
		}
		for name, pr := range map[string]*coreauth.Principal{"none": nil, "another user": other, "no user": {}} {
			req := f.applyReq(p)
			req.Principal = pr
			if _, err := f.svc.Apply(context.Background(), req, nil); !errors.Is(err, ErrNoPrincipal) {
				t.Errorf("%s: Apply = %v, want ErrNoPrincipal", name, err)
			}
		}
		if f.core.planned.Load() != planned || refreshCalls != 0 {
			t.Errorf("core was asked (%d plans) or Refresh called (%d) for a principal that is not the actor's", f.core.planned.Load()-planned, refreshCalls)
		}
		// Refresh answering with another user's principal is refused too.
		f.svc.core.Refresh = func(context.Context, *coreauth.Principal) (*coreauth.Principal, error) { return other, nil }
		if _, err := f.svc.Apply(context.Background(), f.applyReq(p), nil); !errors.Is(err, ErrNoPrincipal) {
			t.Errorf("Refresh for another user: Apply = %v, want ErrNoPrincipal", err)
		}
	})
}

func TestMergeIdentityApply_AfterCommitRunsOnce(t *testing.T) {
	f := newIDFixture(t)
	p := f.plan()
	if _, err := f.svc.Apply(context.Background(), f.applyReq(p), nil); err != nil {
		t.Fatal(err)
	}
	if n := f.core.after.Load(); n != 1 {
		t.Fatalf("AfterCommit ran %d times on success, want exactly 1", n)
	}
}

// The identity journal holds what a later unmerge needs of both halves and no
// credential.
func TestMergeIdentityApply_JournalRecordsBothHalvesWithoutCredentials(t *testing.T) {
	f := newIDFixture(t)
	p := f.plan()
	j, err := f.svc.Apply(context.Background(), f.applyReq(p), nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(j)
	rows := f.journalRows()
	for _, blob := range [][]byte{raw, rows[0].Value} {
		for _, needle := range []string{credHash, credSubject, "gh-12345", credCredential, "tokB"} {
			if bytes.Contains(blob, []byte(needle)) {
				t.Errorf("the journal carries %q", needle)
			}
		}
	}
	c := j.Core
	if c.SourceStatusBefore != "active" || c.SourceEmailBefore != "absorbed@admins.example.com" ||
		!slices.Equal(c.SourceRolesBefore, []string{"admin", "editor"}) || c.SourceVersionBefore != 5 || c.SourceEpochBefore != 2 ||
		!slices.Equal(c.TargetRolesBefore, []string{"admin"}) || c.TargetVersionBefore != 3 ||
		!slices.Equal(c.RolesAdded, []string{"editor"}) || c.EmailOutcome != "kept" ||
		!slices.Equal(c.APIKeyIDs, []string{"key-robin"}) || !slices.Equal(c.IdentityProviders, []string{"gotham-sso"}) || c.AllowListRows != 1 {
		t.Errorf("core half = %+v", c)
	}
	if len(j.Ops) == 0 {
		t.Fatal("no books ops")
	}
	for _, op := range j.Ops {
		if !op.IsOwnerOp() && !op.BeforeAbsent && len(op.Before) == 0 {
			t.Errorf("op %s has no before-image", op.Family)
		}
	}
	if j.AbsorbedStatusBefore != "active" || j.ActorID != f.a.ID {
		t.Errorf("journal = %+v", j.Summary())
	}
}

// A books row the absorbed account's in-flight request writes after the plan is
// moved by the sweep, and the merge stays applied.
func TestMergeIdentityApply_SweepsBooksRowsWrittenAfterThePlan(t *testing.T) {
	f := newIDFixture(t)
	p := f.plan()
	f.core.onStage = nil
	real := f.svc.idMerger
	f.svc.idMerger = decorated{IdentityUserMerger: real, apply: func(ops []database.UserMergeOp, key string, journal []byte, stage func(*storage.Batch) error) error {
		if err := real.ApplyUserMergeIdentity(ops, key, journal, stage); err != nil {
			return err
		}
		f.put("pref:"+f.b.ID+":late", "{\"key\":\"late\",\"value\":\"1\"}")
		return nil
	}}
	j, err := f.svc.Apply(context.Background(), f.applyReq(p), nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.get("pref:"+f.b.ID+":late") != "" || f.get("pref:"+f.a.ID+":late") == "" {
		t.Errorf("the straggler row was not moved")
	}
	if j.Sweeps != 1 {
		t.Errorf("sweeps = %d, want 1", j.Sweeps)
	}
	if j.Status != StatusApplied {
		t.Errorf("status = %s", j.Status)
	}
}

// journalOnDisk is the journal the store holds for the one merge of f.
func (f *idFixture) journalOnDisk() *Journal {
	f.t.Helper()
	rows := f.journalRows()
	if len(rows) != 1 {
		f.t.Fatalf("journal rows = %d, want 1", len(rows))
	}
	var j Journal
	if err := json.Unmarshal(rows[0].Value, &j); err != nil {
		f.t.Fatal(err)
	}
	return &j
}

// failingStore refuses to save a journal: the post-commit sweep's own re-saves.
type failingStore struct {
	Store
	fail *atomic.Bool
}

func (s failingStore) SetRaw(key string, v []byte) error {
	if s.fail.Load() && strings.HasPrefix(key, database.UserMergeJournalPrefix) {
		return errors.New("disk full")
	}
	return s.Store.SetRaw(key, v)
}

// failingMerger refuses the sweep's write (rows and journal together).
type failingMerger struct {
	database.UserMerger
	fail *atomic.Bool
}

func (m failingMerger) ApplyUserMergeOps(ops []database.UserMergeOp, key string, journal []byte) error {
	if m.fail.Load() {
		return errors.New("the books store went away")
	}
	return m.UserMerger.ApplyUserMergeOps(ops, key, journal)
}

// The journal in the committed batch says the sweep is still to run
// (Core.SweepPending), so a crash between the commit and the sweep is visible
// without anything having been written after it; a clean sweep clears it.
func TestMergeIdentityApply_JournalSaysTheSweepIsPendingUntilItRanClean(t *testing.T) {
	f := newIDFixture(t)
	p := f.plan()
	var committed Journal
	real := f.svc.idMerger
	f.svc.idMerger = decorated{IdentityUserMerger: real, apply: func(ops []database.UserMergeOp, key string, journal []byte, stage func(*storage.Batch) error) error {
		if err := json.Unmarshal(journal, &committed); err != nil {
			return err
		}
		return real.ApplyUserMergeIdentity(ops, key, journal, stage)
	}}
	j, err := f.svc.Apply(context.Background(), f.applyReq(p), nil)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Core == nil || !committed.Core.SweepPending {
		t.Errorf("the journal in the batch does not say the sweep is pending: %+v", committed.Core)
	}
	if j.Core.SweepPending || f.journalOnDisk().Core.SweepPending {
		t.Errorf("a clean sweep left the journal pending (memory %v, disk %v)", j.Core.SweepPending, f.journalOnDisk().Core.SweepPending)
	}
}

// A failed save of the journal after a clean sweep is loud: the merge stands,
// the run reports ErrSweepIncomplete with the merge id, and the journal on disk
// still says sweep_pending.
func TestMergeIdentityApply_FailedJournalResaveIsLoudAndLeavesTheMarker(t *testing.T) {
	f := newIDFixture(t)
	p := f.plan()
	fail := &atomic.Bool{}
	fail.Store(true)
	f.svc.store = failingStore{Store: f.svc.store, fail: fail}
	j, err := f.svc.Apply(context.Background(), f.applyReq(p), nil)
	var se *SweepError
	if !errors.As(err, &se) || !errors.Is(err, ErrSweepIncomplete) {
		t.Fatalf("Apply error = %v, want a *SweepError", err)
	}
	if j == nil || se.JournalID != j.ID {
		t.Errorf("the error names merge %q, the journal is %v", se.JournalID, j)
	}
	if got := f.journalOnDisk(); got.Core == nil || !got.Core.SweepPending || got.Status != StatusApplied {
		t.Errorf("journal on disk = status %s, core %+v", got.Status, got.Core)
	}
	if f.core.user(f.b.ID).MergedInto != coreauth.UserID(f.a.ID) {
		t.Error("the merge itself did not stand")
	}
	if f.core.after.Load() != 1 {
		t.Errorf("AfterCommit ran %d times, want 1", f.core.after.Load())
	}
}

// A straggler pass that cannot write is loud too, and the journal keeps to what
// was written: the planned-but-unwritten straggler ops are not in it.
func TestMergeIdentityApply_FailedSweepWriteIsLoudAndTheJournalKeepsToWhatHappened(t *testing.T) {
	f := newIDFixture(t)
	p := f.plan()
	fail := &atomic.Bool{}
	real := f.svc.idMerger
	f.svc.idMerger = decorated{IdentityUserMerger: real, apply: func(ops []database.UserMergeOp, key string, journal []byte, stage func(*storage.Batch) error) error {
		if err := real.ApplyUserMergeIdentity(ops, key, journal, stage); err != nil {
			return err
		}
		f.put("pref:"+f.b.ID+":late", "{\"key\":\"late\",\"value\":\"1\"}")
		fail.Store(true)
		return nil
	}}
	f.svc.merger = failingMerger{UserMerger: f.svc.merger, fail: fail}
	j, err := f.svc.Apply(context.Background(), f.applyReq(p), nil)
	if !errors.Is(err, ErrSweepIncomplete) {
		t.Fatalf("Apply error = %v, want ErrSweepIncomplete", err)
	}
	got := f.journalOnDisk()
	if got.Core == nil || !got.Core.SweepPending {
		t.Errorf("journal core = %+v", got.Core)
	}
	if len(got.Ops) != got.Cursor || len(got.Ops) != len(p.ops) {
		t.Errorf("journal ops = %d, cursor = %d, committed ops = %d", len(got.Ops), got.Cursor, len(p.ops))
	}
	if len(got.Warnings) == 0 || j == nil {
		t.Errorf("warnings = %v, journal = %v", got.Warnings, j)
	}
	if f.get("pref:"+f.b.ID+":late") == "" {
		t.Error("the straggler row moved although its write failed")
	}
}

// A resume under identity of a journal with no core half (written before the
// flip) is permanent: the one read decides it and nothing is written. The
// identity-era journals' own answers are in undo_identity_test.go.
func TestMergeIdentityResume_IsPermanentWithoutTouchingTheStore(t *testing.T) {
	f := newIDFixture(t)
	store := &touchedStore{journalRows: journalRows{rows: journalRow("j1", StatusApplying)}}
	svc := &Service{store: store, identityFlipped: f.svc.identityFlipped, core: f.svc.core, idMerger: f.svc.idMerger}
	if _, err := svc.Resume(context.Background(), "j1", nil); !errors.Is(err, ErrMergePermanent) {
		t.Fatalf("Resume = %v", err)
	}
	if n := store.calls.Load(); n != 1 {
		t.Errorf("Resume touched the store %d times, want the one journal read", n)
	}
	// A clean identity-era journal has nothing to resume; its undo is core's.
	p := f.plan()
	j, err := f.svc.Apply(context.Background(), f.applyReq(p), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.CheckAction(ActionResume, j.ID); !errors.Is(err, ErrMergePermanent) {
		t.Errorf("CheckAction(resume) = %v", err)
	}
	if err := f.svc.CheckAction(ActionUndo, j.ID); err != nil {
		t.Errorf("CheckAction(undo) = %v, want nil (core decides an undo)", err)
	}
}

// Off identity the service is HEAD's: it never asks core and never uses the
// batch committer, though both are wired.
func TestMergeIdentityWiring_OffIdentityIsHeads(t *testing.T) {
	f := newIDFixture(t)
	f.svc.identityFlipped = nil
	f.svc.idMerger = decorated{IdentityUserMerger: f.svc.idMerger, apply: func([]database.UserMergeOp, string, []byte, func(*storage.Batch) error) error {
		t.Error("HEAD's apply used the identity committer")
		return nil
	}}
	j, err := f.apply(true)
	if err != nil {
		t.Fatal(err)
	}
	if j.Core != nil || j.Retirement.AbsorbedID == "" {
		t.Errorf("a HEAD journal = %+v", j.Summary())
	}
	if f.core.planned.Load() != 0 || f.core.staged.Load() != 0 {
		t.Errorf("HEAD's merge asked core")
	}
}

func TestMergeIdentityWiring_FailsClosedWithoutCoreHandlesOrCommitter(t *testing.T) {
	f := newIDFixture(t)
	p := f.plan()
	for name, mutate := range map[string]func(*Service){
		"no core":      func(s *Service) { s.core = nil },
		"no admin":     func(s *Service) { s.core = &CoreIdentity{Users: f.core, Refresh: s.core.Refresh} },
		"no users":     func(s *Service) { s.core = &CoreIdentity{Admin: f.core, Refresh: s.core.Refresh} },
		"no refresh":   func(s *Service) { s.core = &CoreIdentity{Admin: f.core, Users: f.core} },
		"no committer": func(s *Service) { s.idMerger = nil },
	} {
		t.Run(name, func(t *testing.T) {
			svc := *f.svc
			mutate(&svc)
			if err := svc.CheckAvailable(); !errors.Is(err, ErrUnavailableUnderIdentity) {
				t.Errorf("CheckAvailable = %v", err)
			}
			if _, err := svc.BuildPlan(f.ctx(), f.planReq()); !errors.Is(err, ErrUnavailableUnderIdentity) {
				t.Errorf("BuildPlan = %v", err)
			}
			if _, err := svc.Apply(context.Background(), f.applyReq(p), nil); !errors.Is(err, ErrUnavailableUnderIdentity) {
				t.Errorf("Apply = %v", err)
			}
		})
	}
}

func TestMergeGrant_NeverFormatsOrLogsItsPrincipal(t *testing.T) {
	pr := &coreauth.Principal{User: &coreauth.User{ID: "u-bruce"}, CredentialID: credCredential}
	g := Grant{ActorID: "u-bruce", Action: ActionApply, SurvivorID: "a", AbsorbedID: "b", Digest: "d", Principal: pr}
	for name, text := range map[string]string{
		//lint:ignore S1025 the test pins what each verb prints, %s included
		"%v": fmt.Sprintf("%v", g), "%+v": fmt.Sprintf("%+v", g), "%#v": fmt.Sprintf("%#v", g), "%s": fmt.Sprintf("%s", g),
		"pointer %+v": fmt.Sprintf("%+v", &g),
	} {
		if strings.Contains(text, credCredential) {
			t.Errorf("%s of a Grant carries the principal's credential id: %s", name, text)
		}
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("grant", "grant", g)
	if strings.Contains(buf.String(), credCredential) || !strings.Contains(buf.String(), "u-bruce") {
		t.Errorf("slog record of a Grant: %s", buf.String())
	}
}

func TestMergeGrants_PrincipalMustBeTheActorsOwn(t *testing.T) {
	g := NewGrants()
	own := &coreauth.Principal{User: &coreauth.User{ID: "u-bruce"}}
	if _, err := g.Issue(Grant{ActorID: "u-dick", Action: ActionApply, SurvivorID: "a", AbsorbedID: "b", Digest: "d", Principal: own}); err == nil {
		t.Error("a grant for u-dick was minted with u-bruce's principal")
	}
	if _, err := g.Issue(Grant{ActorID: "u-bruce", Action: ActionApply, SurvivorID: "a", AbsorbedID: "b", Digest: "d", Principal: &coreauth.Principal{}}); err == nil {
		t.Error("a grant was minted with a principal that names no user")
	}
	tok, err := g.Issue(Grant{ActorID: "u-bruce", Action: ActionApply, SurvivorID: "a", AbsorbedID: "b", Digest: "d", Principal: own})
	if err != nil {
		t.Fatal(err)
	}
	// An operation enqueued for another actor cannot redeem it and cannot read
	// the principal out.
	if _, err := g.Redeem(tok, "u-dick", Grant{Action: ActionApply, SurvivorID: "a", AbsorbedID: "b", Digest: "d"}); err == nil {
		t.Fatal("redeemed for another actor")
	}
	tok, _ = g.Issue(Grant{ActorID: "u-bruce", Action: ActionApply, SurvivorID: "a", AbsorbedID: "b", Digest: "d", Principal: own})
	gr, err := g.Redeem(tok, "u-bruce", Grant{Action: ActionApply, SurvivorID: "a", AbsorbedID: "b", Digest: "d"})
	if err != nil || gr.Principal != own {
		t.Fatalf("Redeem = %v %v", gr.Principal, err)
	}
}

func (c *fakeCore) PlanUserUnmerge(ctx context.Context, rec coreauth.UnmergeRecord) (coreauth.UserUnmergePlan, coreauth.UnmergeSummary, error) {
	c.unplanned.Add(1)
	p, ok := coreauth.PrincipalFrom(ctx)
	if !ok || p.User == nil {
		return coreauth.UserUnmergePlan{}, coreauth.UnmergeSummary{}, fmt.Errorf("%w: no principal", coreauth.ErrDenied)
	}
	c.mu.Lock()
	c.lastRec = rec
	err := c.unmergePlanErr
	c.mu.Unlock()
	if err != nil {
		return coreauth.UserUnmergePlan{}, coreauth.UnmergeSummary{}, err
	}
	return coreauth.UserUnmergePlan{}, coreauth.UnmergeSummary{Source: rec.Source, Target: rec.Target}, nil
}

// StageUserUnmerge undoes the fake's one write: the merged-into marker, in the
// caller's batch, so whether the unmerge landed is whether the marker is gone.
func (c *fakeCore) StageUserUnmerge(ctx context.Context, b *storage.Batch, _ coreauth.UserUnmergePlan) (coreauth.AfterCommit, error) {
	c.unstaged.Add(1)
	if p, ok := coreauth.PrincipalFrom(ctx); !ok || p.User == nil {
		return nil, fmt.Errorf("%w: no principal", coreauth.ErrDenied)
	}
	c.mu.Lock()
	rec, err := c.lastRec, c.unmergeStageErr
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if b != nil {
		if err := b.Delete([]byte(markerKey(string(rec.Source)))); err != nil {
			return nil, err
		}
	}
	return func(context.Context) { c.unafter.Add(1) }, nil
}
