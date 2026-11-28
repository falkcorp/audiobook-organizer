// file: core/kernel/health_backup_test.go
// version: 0.1.0
// guid: a5e07c3b-9d14-4f62-8b2e-1c6f93d0a7e8
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/health"
)

// Batman names only: the accounts are Oracle's and Nightwing's.

func bkDegraded(reason string) backupToken {
	return backupToken{Reason: reason, Status: health.StatusDegraded, Detail: "d-" + reason}
}

func bkInfo(reason string) backupToken {
	return backupToken{Reason: reason, Status: health.StatusUp, Detail: "d-" + reason}
}

// TestCombineBackup_NoTokensIsUpWithNoReason: nothing to report is Up with no
// Reason and no Detail.
func TestCombineBackup_NoTokensIsUpWithNoReason(t *testing.T) {
	for _, in := range [][]backupToken{nil, {}} {
		if got := combineBackup(in); got != (health.Result{Status: health.StatusUp}) {
			t.Errorf("combineBackup(%v) = %+v, want Up with no Reason", in, got)
		}
	}
}

// TestCombineBackup_HighestSeverityWins: a degraded token beats every info
// token whatever its rank or the input order (the lowest-ranked degraded
// against the highest-ranked info), and Down beats Degraded.
func TestCombineBackup_HighestSeverityWins(t *testing.T) {
	cases := []struct {
		name     string
		lo, hi   backupToken
		wantStat health.Status
	}{
		{"degraded review over info queued", bkInfo(journal.HealthQueued), bkDegraded(reasonRestoreAdminsReview), health.StatusDegraded},
		{"degraded review over info colocated", bkInfo(journal.HealthColocated), bkDegraded(reasonRestoreAdminsReview), health.StatusDegraded},
		{"down over degraded broken", bkDegraded(journal.HealthBroken), backupToken{Reason: "backup_archive_lost", Status: health.StatusDown, Detail: "x"}, health.StatusDown},
	}
	for _, c := range cases {
		for _, in := range [][]backupToken{{c.lo, c.hi}, {c.hi, c.lo}} {
			got := combineBackup(in)
			if got.Status != c.wantStat || got.Reason != c.hi.Reason {
				t.Errorf("%s: combineBackup(%v) = %s/%s, want %s/%s", c.name, in, got.Status, got.Reason, c.wantStat, c.hi.Reason)
			}
			if !strings.HasPrefix(got.Detail, c.hi.Reason+": ") {
				t.Errorf("%s: Detail %q does not lead with the winning token", c.name, got.Detail)
			}
		}
	}
}

// TestCombineBackup_TiesFollowTheFixedOrder: for every pair of tokens of
// equal severity, in both input orders, the Reason is the one ranked first,
// and Detail lists both in rank order. That includes broken with unwritable,
// which journal.Component.Health can report together.
func TestCombineBackup_TiesFollowTheFixedOrder(t *testing.T) {
	groups := []struct {
		name  string
		order []string
		mk    func(string) backupToken
	}{
		{"degraded", []string{journal.HealthBroken, journal.HealthUnwritable, reasonRestoreLastAdminKept, reasonRestoreAdminsReview}, bkDegraded},
		{"info", []string{journal.HealthQueued, journal.HealthReset, journal.HealthColocated}, bkInfo},
	}
	for _, g := range groups {
		for i := range g.order {
			for j := i + 1; j < len(g.order); j++ {
				first, second := g.mk(g.order[i]), g.mk(g.order[j])
				want := first.Reason + ": " + first.Detail + "; " + second.Reason + ": " + second.Detail
				for _, in := range [][]backupToken{{first, second}, {second, first}} {
					got := combineBackup(in)
					if got.Reason != first.Reason || got.Status != first.Status {
						t.Errorf("%s: combineBackup(%s, %s) Reason = %s, want %s", g.name, in[0].Reason, in[1].Reason, got.Reason, first.Reason)
					}
					if got.Detail != want {
						t.Errorf("%s: Detail = %q, want %q", g.name, got.Detail, want)
					}
				}
			}
		}
	}
}

// TestCombineBackup_AllSevenAtOnce: with every listed token raised, the
// Reason is revocation_journal_broken and Detail lists all seven, degraded
// first, each group in rank order.
func TestCombineBackup_AllSevenAtOnce(t *testing.T) {
	in := []backupToken{
		bkInfo(journal.HealthColocated), bkDegraded(reasonRestoreAdminsReview), bkInfo(journal.HealthReset),
		bkDegraded(journal.HealthUnwritable), bkInfo(journal.HealthQueued), bkDegraded(reasonRestoreLastAdminKept),
		bkDegraded(journal.HealthBroken),
	}
	got := combineBackup(in)
	if got.Status != health.StatusDegraded || got.Reason != journal.HealthBroken {
		t.Fatalf("combineBackup = %s/%s, want degraded/%s", got.Status, got.Reason, journal.HealthBroken)
	}
	var reasons []string
	for part := range strings.SplitSeq(got.Detail, "; ") {
		r, _, _ := strings.Cut(part, ": ")
		reasons = append(reasons, r)
	}
	want := []string{journal.HealthBroken, journal.HealthUnwritable, reasonRestoreLastAdminKept, reasonRestoreAdminsReview,
		journal.HealthQueued, journal.HealthReset, journal.HealthColocated}
	if !slices.Equal(reasons, want) {
		t.Errorf("Detail order = %v, want %v", reasons, want)
	}
	if in[0].Reason != journal.HealthColocated {
		t.Error("combineBackup reordered its input slice")
	}
}

// TestCombineBackup_UnlistedReason: a reason the order does not list is never
// dropped and never masks a listed token of its own severity. It loses a tie
// to a listed token, wins over a lower severity, is the Reason when alone,
// and two unlisted reasons still have a fixed order.
func TestCombineBackup_UnlistedReason(t *testing.T) {
	const unlisted, unlisted2 = "backup_archive_stale", "backup_archive_missing"
	cases := []struct {
		name string
		in   []backupToken
		want string
	}{
		{"loses a degraded tie to the lowest listed", []backupToken{bkDegraded(unlisted), bkDegraded(reasonRestoreAdminsReview)}, reasonRestoreAdminsReview},
		{"loses an info tie to colocated", []backupToken{bkInfo(unlisted), bkInfo(journal.HealthColocated)}, journal.HealthColocated},
		{"degraded beats a listed info", []backupToken{bkInfo(journal.HealthQueued), bkDegraded(unlisted)}, unlisted},
		{"alone", []backupToken{bkDegraded(unlisted)}, unlisted},
		{"two unlisted", []backupToken{bkDegraded(unlisted), bkDegraded(unlisted2)}, unlisted2},
		{"two unlisted, swapped", []backupToken{bkDegraded(unlisted2), bkDegraded(unlisted)}, unlisted2},
	}
	for _, c := range cases {
		got := combineBackup(c.in)
		if got.Reason != c.want {
			t.Errorf("%s: Reason = %s, want %s", c.name, got.Reason, c.want)
		}
		for _, tok := range c.in {
			if !strings.Contains(got.Detail, tok.Reason+": "+tok.Detail) {
				t.Errorf("%s: Detail %q drops %s", c.name, got.Detail, tok.Reason)
			}
		}
	}
}

// TestBackupReasonRank_EverySource: every token a core.backup source can emit
// is listed, each at the rank the documented order gives it, so a token added
// to a source without a place in the order fails here.
func TestBackupReasonRank_EverySource(t *testing.T) {
	want := []string{journal.HealthBroken, journal.HealthUnwritable, reasonRestoreLastAdminKept, reasonRestoreAdminsReview,
		journal.HealthQueued, journal.HealthReset, journal.HealthColocated}
	for i, r := range want {
		if got := backupReasonRank(r); got != i {
			t.Errorf("backupReasonRank(%s) = %d, want %d", r, got, i)
		}
	}
	if backupRankUnknown <= len(want)-1 {
		t.Errorf("backupRankUnknown = %d, want above every listed rank", backupRankUnknown)
	}
	// The journal's tokens as its Health emits them, and the restore tokens as
	// restoreTokens emits them.
	emitted := []string{journal.HealthBroken, journal.HealthUnwritable, journal.HealthReset, journal.HealthColocated, journal.HealthQueued}
	for _, tok := range restoreTokens(hookReport{KeptAdmins: []string{"u-oracle"}, AdminsToReview: []string{"u-nightwing"}}) {
		emitted = append(emitted, tok.Reason)
	}
	for _, r := range emitted {
		if backupReasonRank(r) == backupRankUnknown {
			t.Errorf("%s is emitted by a core.backup source but has no place in backupReasonRank", r)
		}
	}
}

// TestJournalTokens_Levels: degraded is StatusDegraded, info is StatusUp with
// its Reason, and a severity the mapping does not know is StatusDegraded (shown,
// not hidden). Token and Detail are kept.
func TestJournalTokens_Levels(t *testing.T) {
	got := journalTokens([]journal.Health{
		{Token: journal.HealthBroken, Severity: journal.SeverityDegraded, Detail: "chain"},
		{Token: journal.HealthReset, Severity: journal.SeverityInfo, Detail: "set aside"},
		{Token: journal.HealthQueued, Severity: journal.Severity("critical"), Detail: "2 lines"},
	})
	want := []backupToken{
		{journal.HealthBroken, health.StatusDegraded, "chain"},
		{journal.HealthReset, health.StatusUp, "set aside"},
		{journal.HealthQueued, health.StatusDegraded, "2 lines"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("journalTokens = %+v, want %+v", got, want)
	}
}

// TestRestoreTokens: no report raises nothing; a kept admin raises
// restore_last_admin_kept (Degraded) naming the account and the recovery
// path; a review list raises restore_admins_review (Degraded) naming every
// holder.
func TestRestoreTokens(t *testing.T) {
	if got := restoreTokens(hookReport{}); len(got) != 0 {
		t.Errorf("restoreTokens(empty) = %+v, want none", got)
	}
	got := restoreTokens(hookReport{KeptAdmins: []string{"u-oracle"}, AdminsToReview: []string{"u-oracle", "u-nightwing"}})
	if len(got) != 2 {
		t.Fatalf("restoreTokens = %+v, want two tokens", got)
	}
	kept, review := got[0], got[1]
	if kept.Reason != reasonRestoreLastAdminKept || kept.Status != health.StatusDegraded ||
		!strings.Contains(kept.Detail, "u-oracle") || !strings.Contains(kept.Detail, "reset-password") || !strings.Contains(kept.Detail, "reset link") {
		t.Errorf("kept = %+v, want Degraded %s naming u-oracle and both recovery paths", kept, reasonRestoreLastAdminKept)
	}
	if review.Reason != reasonRestoreAdminsReview || review.Status != health.StatusDegraded ||
		!strings.Contains(review.Detail, "u-oracle") || !strings.Contains(review.Detail, "u-nightwing") {
		t.Errorf("review = %+v, want Degraded %s naming both holders", review, reasonRestoreAdminsReview)
	}
	if only := restoreTokens(hookReport{AdminsToReview: []string{"u-nightwing"}}); len(only) != 1 || only[0].Reason != reasonRestoreAdminsReview {
		t.Errorf("restoreTokens(review only) = %+v, want restore_admins_review alone", only)
	}
}

// TestBackupProbe_OnTheRegistry: registered as core.backup's source, the
// probe combines the journal's and the restore hook's tokens into the one
// Result the registry stores (an info Reason survives on StatusUp); nil
// sources report Up.
func TestBackupProbe_OnTheRegistry(t *testing.T) {
	jh := []journal.Health{
		{Token: journal.HealthColocated, Severity: journal.SeverityInfo, Detail: "one device"},
		{Token: journal.HealthQueued, Severity: journal.SeverityInfo, Detail: "1 line"},
	}
	var rep hookReport
	probe := backupProbe(func() []journal.Health { return jh }, func(context.Context) (hookReport, error) { return rep, nil })
	reg := health.NewRegistry()
	if _, err := registerCoreComponents(reg, coreComponentSources{Backup: probe}); err != nil {
		t.Fatal(err)
	}
	res := backupResult(t, reg)
	if res.Status != health.StatusUp || res.Reason != journal.HealthQueued {
		t.Errorf("core.backup = %s/%s, want up/%s (info, queued ranked over colocated)", res.Status, res.Reason, journal.HealthQueued)
	}
	if !strings.Contains(res.Detail, journal.HealthColocated) {
		t.Errorf("core.backup Detail %q drops %s", res.Detail, journal.HealthColocated)
	}

	rep = hookReport{KeptAdmins: []string{"u-oracle"}}
	if got := probe(context.Background()); got.Status != health.StatusDegraded || got.Reason != reasonRestoreLastAdminKept {
		t.Errorf("with a kept admin, probe = %s/%s, want degraded/%s", got.Status, got.Reason, reasonRestoreLastAdminKept)
	}

	if got := backupProbe(nil, nil)(context.Background()); got != (health.Result{Status: health.StatusUp}) {
		t.Errorf("backupProbe(nil, nil) = %+v, want Up with no Reason", got)
	}
}

func backupResult(t *testing.T, reg *health.Registry) health.Result {
	t.Helper()
	for _, c := range reg.Snapshot().Components {
		if c.ID == componentBackup {
			return c.Result
		}
	}
	t.Fatal("core.backup is not registered")
	return health.Result{}
}
