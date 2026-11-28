// file: core/rbac/adminreview_test.go
// version: 0.1.0
// guid: 3c9f7e12-5b8a-4d60-a1f4-8e27c0b95d36
// last-edited: 2026-11-21

package rbac_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Batman names only. F4-08 B10i: acknowledging the core:admin holders a restore
// listed for review (05-rbac 7.1).

// openBackupKS boots a store holding c/backup/, where the restore report lives.
func openBackupKS(t testing.TB) storage.Keyspace {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:            filepath.Join(t.TempDir(), "main.pebble"),
		Owners:          []storage.OwnerSpec{{Owner: journal.Owner, Spec: journal.StorageSpec(), Enabled: true}},
		Build:           "coretest+fixture",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		t.Fatal(errors.Join(err, o.Close()))
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	ks, ok := db.Keyspace(journal.Owner)
	if !ok {
		t.Fatal("no c/backup/ keyspace")
	}
	return ks
}

// reviewFx is an AdminReview over a real c/backup/ keyspace holding a report,
// with the fixture's checkers for the actors.
type reviewFx struct {
	*fixture
	ks     storage.Keyspace
	review *rbac.AdminReview
}

func newReviewFx(t *testing.T, rep journal.RestoreReport) *reviewFx {
	t.Helper()
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	ks := openBackupKS(t)
	if err := journal.PutRestoreReport(t.Context(), ks, rep, time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	r, err := rbac.NewAdminReview(journal.NewReviewAcks(ks), f.audit)
	if err != nil {
		t.Fatal(err)
	}
	return &reviewFx{fixture: f, ks: ks, review: r}
}

// reviewed is the audit records of acknowledgements ("<action> <subject>"), not
// the seed roles' that booting the fixture wrote.
func (fx *reviewFx) reviewed() []string {
	return slices.DeleteFunc(fx.audit.actions(), func(a string) bool { return !strings.HasPrefix(a, "core.role.review_admin ") })
}

func (fx *reviewFx) stored(t *testing.T) journal.RestoreReport {
	t.Helper()
	got, err := journal.ReadRestoreReport(t.Context(), fx.ks)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestAdminReview_AcknowledgesOneOfTwoAndAuditsIt: an interactive core:admin
// acknowledges one of two listed holders; the other stays listed, and the one
// audit record names the acknowledged account (the actor is the audit store's to
// fill from the context, which the kernel's end-to-end test checks).
func TestAdminReview_AcknowledgesOneOfTwoAndAuditsIt(t *testing.T) {
	fx := newReviewFx(t, journal.RestoreReport{Review: []string{"u-nightwing", "u-robin"}})
	if err := fx.review.AcknowledgeAdminReview(t.Context(), fx.admin(), "u-robin"); err != nil {
		t.Fatal(err)
	}
	if got := fx.stored(t); !slices.Equal(got.Review, []string{"u-nightwing"}) {
		t.Fatalf("review list = %v, want only u-nightwing", got.Review)
	}
	if got := fx.reviewed(); !slices.Equal(got, []string{"core.role.review_admin u-robin"}) {
		t.Fatalf("acknowledgement audit records = %v, want exactly one, for u-robin", got)
	}
	fx.audit.mu.Lock()
	defer fx.audit.mu.Unlock()
	ev := fx.audit.recorded[len(fx.audit.recorded)-1]
	if ev.Action != "core.role.review_admin" || ev.Outcome != audit.Success ||
		ev.Target.Module != "core" || ev.Target.Kind != "user" || ev.Target.ID != "u-robin" {
		t.Errorf("audit event = %+v, want core.role.review_admin success on core user u-robin", ev)
	}
}

// TestAdminReview_TheLastAcknowledgementDeletesTheRecord: acknowledging the last
// listed holder deletes the record (an empty report is deleted), and the report a
// reader sees is empty; with a Kept account still on the report the record stays.
func TestAdminReview_TheLastAcknowledgementDeletesTheRecord(t *testing.T) {
	fx := newReviewFx(t, journal.RestoreReport{Review: []string{"u-nightwing", "u-robin"}})
	for _, id := range []string{"u-robin", "u-nightwing"} {
		if err := fx.review.AcknowledgeAdminReview(t.Context(), fx.admin(), id); err != nil {
			t.Fatalf("acknowledge %s: %v", id, err)
		}
	}
	if _, err := fx.ks.Get(t.Context(), []byte("restore_report")); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("the report record after the last acknowledgement: %v, want ErrNotFound (deleted)", err)
	}
	if got := fx.stored(t); !got.Empty() {
		t.Errorf("report = %+v, want empty", got)
	}

	kept := newReviewFx(t, journal.RestoreReport{Kept: []string{"u-oracle"}, Review: []string{"u-robin"}})
	if err := kept.review.AcknowledgeAdminReview(t.Context(), kept.admin(), "u-robin"); err != nil {
		t.Fatal(err)
	}
	if got := kept.stored(t); len(got.Review) != 0 || !slices.Equal(got.Kept, []string{"u-oracle"}) {
		t.Errorf("report = %+v, want review empty and kept [u-oracle]: acknowledging a review does not clear restore_last_admin_kept", got)
	}
}

// TestAdminReview_NotListedIsRefusedAndRecordsNothing: an unknown account, an
// account acknowledged a moment ago and an account listed only as Kept are
// ErrNotInAdminReview; none writes an audit record, and the second
// acknowledgement of one account is not audited twice.
func TestAdminReview_NotListedIsRefusedAndRecordsNothing(t *testing.T) {
	fx := newReviewFx(t, journal.RestoreReport{Kept: []string{"u-oracle"}, Review: []string{"u-robin", "u-nightwing"}})
	for _, id := range []string{"u-joker", "u-oracle"} {
		if err := fx.review.AcknowledgeAdminReview(t.Context(), fx.admin(), id); !errors.Is(err, rbac.ErrNotInAdminReview) {
			t.Errorf("acknowledge %s: %v, want ErrNotInAdminReview", id, err)
		}
	}
	if err := fx.review.AcknowledgeAdminReview(t.Context(), fx.admin(), "u-robin"); err != nil {
		t.Fatal(err)
	}
	if err := fx.review.AcknowledgeAdminReview(t.Context(), fx.admin(), "u-robin"); !errors.Is(err, rbac.ErrNotInAdminReview) {
		t.Errorf("a second acknowledgement of u-robin: %v, want ErrNotInAdminReview", err)
	}
	if got := fx.reviewed(); !slices.Equal(got, []string{"core.role.review_admin u-robin"}) {
		t.Errorf("audit records = %v, want exactly one, for the one acknowledgement that happened", got)
	}
	if got := fx.stored(t); !slices.Equal(got.Review, []string{"u-nightwing"}) || !slices.Equal(got.Kept, []string{"u-oracle"}) {
		t.Errorf("report = %+v, want review [u-nightwing] kept [u-oracle]", got)
	}
}

// TestAdminReview_OnlyACoreAdminMayAcknowledge: an actor without core:admin, a
// nil actor and a holder of only narrower scopes are ErrDenied, and nothing is
// removed or audited. The account is looked up in the report alone, so a listed
// account that no longer exists as a user (nothing here has users at all) is
// acknowledgeable.
func TestAdminReview_OnlyACoreAdminMayAcknowledge(t *testing.T) {
	fx := newReviewFx(t, journal.RestoreReport{Review: []string{"u-robin"}})
	for name, actor := range map[string]rbac.Checker{
		"nil actor":      nil,
		"a module scope": fx.as("gotham:patrol"),
		"roles manager":  fx.as("core:roles.manage"),
		"users manager":  fx.as("core:users.manage"),
	} {
		if err := fx.review.AcknowledgeAdminReview(t.Context(), actor, "u-robin"); !errors.Is(err, rbac.ErrDenied) {
			t.Errorf("%s: %v, want ErrDenied", name, err)
		}
	}
	if got := fx.stored(t); !slices.Equal(got.Review, []string{"u-robin"}) {
		t.Errorf("a refused acknowledgement changed the report: %+v", got)
	}
	if got := fx.reviewed(); len(got) != 0 {
		t.Errorf("a refused acknowledgement was audited: %v", got)
	}
	// The same listed account, now by a core:admin, with no user store anywhere.
	if err := fx.review.AcknowledgeAdminReview(t.Context(), fx.admin(), "u-robin"); err != nil {
		t.Errorf("a listed account that is not a user: %v", err)
	}
}

// TestAdminReview_AFailedAuditRecordLeavesTheAccountListed: the audit record is
// staged into the removal's batch, so one that cannot be staged discards the
// removal; the error is not ErrNotInAdminReview.
func TestAdminReview_AFailedAuditRecordLeavesTheAccountListed(t *testing.T) {
	fx := newReviewFx(t, journal.RestoreReport{Review: []string{"u-robin"}})
	boom := errors.New("the audit store is full")
	fx.audit.recordErr = boom
	err := fx.review.AcknowledgeAdminReview(t.Context(), fx.admin(), "u-robin")
	if !errors.Is(err, boom) || errors.Is(err, rbac.ErrNotInAdminReview) {
		t.Fatalf("err = %v, want the audit error", err)
	}
	if got := fx.stored(t); !slices.Equal(got.Review, []string{"u-robin"}) {
		t.Fatalf("the removal committed without its audit record: %+v", got)
	}
}

// TestAdminReview_AnUnreadableReportIsNotNotListed: a report that does not decode
// is not "no such account": the error is not ErrNotInAdminReview.
func TestAdminReview_AnUnreadableReportIsNotNotListed(t *testing.T) {
	fx := newReviewFx(t, journal.RestoreReport{Review: []string{"u-robin"}})
	if err := fx.ks.Set(t.Context(), []byte("restore_report"), []byte("not json")); err != nil {
		t.Fatal(err)
	}
	err := fx.review.AcknowledgeAdminReview(t.Context(), fx.admin(), "u-robin")
	if err == nil || errors.Is(err, rbac.ErrNotInAdminReview) {
		t.Fatalf("err = %v, want a read error that is not ErrNotInAdminReview", err)
	}
}

// TestNewAdminReview_NeedsItsDependencies: a nil store or audit logger is
// refused, not a nil dereference at the first acknowledgement.
func TestNewAdminReview_NeedsItsDependencies(t *testing.T) {
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	if _, err := rbac.NewAdminReview(nil, f.audit); err == nil {
		t.Error("NewAdminReview without a store succeeded")
	}
	if _, err := rbac.NewAdminReview(journal.NewReviewAcks(storage.Keyspace{}), nil); err == nil {
		t.Error("NewAdminReview without an audit logger succeeded")
	}
}
