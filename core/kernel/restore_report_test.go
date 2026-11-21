// file: core/kernel/restore_report_test.go
// version: 0.1.0
// guid: 6a2d9f81-c4e3-4b70-a5d8-1f93e7b0c256
// last-edited: 2026-11-21

package kernel

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// TestRestoreReport_AHostLossRestoreFlagsItsAdminsAndTheTokenSurvivesARestart: a
// restore whose live store is gone has no pre-swap store, so the hooks list every
// admin holder for review (restore_admins_review). The report is recorded in the
// store, so the token is still raised by the boot after it, which runs no hooks;
// and a restore with nothing to report leaves none.
func TestRestoreReport_AHostLossRestoreFlagsItsAdminsAndTheTokenSurvivesARestart(t *testing.T) {
	h, c, first := restoreHost(t)
	actor := &auth.Principal{Method: auth.MethodSession, Perms: holdsAll{}, User: &auth.User{ID: "u-alfred", Username: "alfred"}}
	oracle, err := first.ident.svc.Auth.CreateUser(t.Context(), actor, auth.NewUser{Username: "oracle", Password: fixturePassword, Roles: []rbac.RoleName{rbac.RoleAdmin}})
	if err != nil {
		t.Fatal(err)
	}
	src := newArchiveSource()
	src.intentInstall = "live"
	stageArchive(t, h, first, "r-hostloss", src)
	h.close(first)
	if err := os.RemoveAll(h.db); err != nil {
		t.Fatal(err)
	}

	k := h.mustBoot(c)
	if !k.ident.p4d.Hooks.Ran {
		t.Fatal("the restore hooks did not run")
	}
	want := []string{string(oracle.ID)}
	if got := k.ident.p4d.Hooks.Report.AdminsToReview; !slices.Equal(got, want) {
		t.Fatalf("admins to review = %v, want %v (no pre-swap store: every admin holder)", got, want)
	}
	stored, err := journal.ReadRestoreReport(t.Context(), k.ident.backupKS)
	if err != nil || !slices.Equal(stored.Review, want) {
		t.Fatalf("stored report = %+v, %v, want review %v", stored, err, want)
	}
	h.close(k)

	again := h.mustBoot(c) // a plain restart: no restore, no hooks
	if again.ident.p4d.Hooks.Ran {
		t.Fatal("a plain restart ran the restore hooks")
	}
	res := again.ident.backupProbe()(t.Context())
	if res.Status != health.StatusDegraded || res.Reason != reasonRestoreAdminsReview || !strings.Contains(res.Detail, string(oracle.ID)) {
		t.Fatalf("core.backup after a restart = %v %q (%s), want degraded %q naming %s", res.Status, res.Reason, res.Detail, reasonRestoreAdminsReview, oracle.ID)
	}
	if got := health.PublicTokens([]string{string(componentBackup) + ":" + res.Reason}); !slices.Equal(got, []string{string(componentBackup) + ":down"}) {
		t.Errorf("PublicTokens names the restore token to an unauthenticated caller: %v", got)
	}
}

// TestRestoreReport_AnUnreadableReportIsNotNothingToReview: a stored report that
// does not decode raises restore_report_unreadable on core.backup; it does not
// read as an empty report.
func TestRestoreReport_AnUnreadableReportIsNotNothingToReview(t *testing.T) {
	h, c := identityHost(t)
	k := h.mustBoot(c)
	b := k.ident.backupKS.NewBatch()
	if err := b.In(k.ident.backupKS).Set([]byte("restore_report"), []byte("not json")); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(t.Context(), storage.Sync); err != nil {
		t.Fatal(err)
	}
	res := k.ident.backupProbe()(t.Context())
	if res.Status != health.StatusDegraded || res.Reason != reasonRestoreReportUnreadable {
		t.Fatalf("core.backup = %v %q (%s), want %q", res.Status, res.Reason, res.Detail, reasonRestoreReportUnreadable)
	}
}
