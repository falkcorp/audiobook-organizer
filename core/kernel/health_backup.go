// file: core/kernel/health_backup.go
// version: 0.1.0
// guid: 7d2c9e41-6b05-4a38-9f17-c3e8a50d2b64
// last-edited: 2026-11-21

package kernel

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/health"
)

// core.backup carries several owners' reasons on one component: the
// revocation journal's five tokens (05-backup §5B.3.6, journal.Component.Health)
// and rbac's two restore tokens (05-health §5H.3.1 "Restore tokens owned by
// 05-rbac"), and later F8-08's. A health.Result has one Status and one Reason,
// and a component has one owner of its Result: once core.backup has a Probe,
// the probe loop overwrites anything pushed through its Reporter. So every
// source of a core.backup reason is an input of backupProbe, never a second
// Reporter; F8-08 adds its tokens here as another input and adds each token to
// backupReasonRank.
//
// The rule that combines them (F4-08 A11b, coordinator ruling 2026-11-21):
//
//   - the highest severity wins: Down above Degraded above info;
//   - among equal severities, backupReasonRank's fixed order picks the
//     Reason;
//   - Detail lists every token present, each as "<reason>: <detail>", in the
//     same order, so the authenticated endpoint shows all of them and the
//     token-only route shows the one that matters most.
//
// "info" is 05-health's advisory level: a Reason on a StatusUp result, with no
// probe or readiness effect. No token at all is StatusUp with no Reason.

// The restore tokens rbac's post-restore hook raises on core.backup (05-health
// §5H.3.1; 05-rbac §7.1). Both are Degraded.
const (
	reasonRestoreLastAdminKept = "restore_last_admin_kept"
	reasonRestoreAdminsReview  = "restore_admins_review"
)

// backupToken is one reason core.backup has to report.
type backupToken struct {
	Reason string
	Status health.Status // StatusUp for info, StatusDegraded or StatusDown otherwise
	Detail string
}

// backupRankUnknown is the rank of a Reason backupReasonRank does not list. It
// sorts after every listed Reason of its own severity, so a token a source
// added without adding it to the order is neither dropped (it is in Detail and
// is the Reason when it is alone, or when its severity is the highest) nor able
// to mask a listed token of equal severity. TestBackupReasonRank_EverySource
// fails when a source emits a token missing here.
const backupRankUnknown = 7

// backupReasonRank is the fixed order that picks core.backup's Reason among
// tokens of equal severity: lower ranks first. A function with a switch, not a
// package-level table (no mutable package state, R5). The order, and why:
//
//  0. revocation_journal_broken: the journal does not verify, is cut short or
//     belongs to another install or app, so a restore can no longer re-apply
//     the revocations made since the backup: every account is exposed.
//  1. revocation_journal_unwritable: the journal is not taking lines now (or a
//     gap is open), with the same exposure for the revocations of this window.
//  2. restore_last_admin_kept: one account kept its last core:admin entry and
//     its sign-in stays restricted until an operator recovers it.
//  3. restore_admins_review: advisory; every core:admin holder after a restore
//     with no pre-swap store is listed for review.
//  4. revocation_journal_queued: revocation lines this build cannot apply wait
//     in the unapplied queue, so some revocations are not in force yet.
//  5. revocation_journal_reset: another install's journal was set aside at
//     this boot, a one-time event.
//  6. revocation_journal_colocated: standing configuration (the journal shares
//     the store's device). It is last so that, on a host where it is always
//     raised, it never hides a queued or reset token from the token-only route
//     or the soak gate (it is on the soak allow-list, 05-health §5H.3.1).
func backupReasonRank(reason string) int {
	switch reason {
	case journal.HealthBroken:
		return 0
	case journal.HealthUnwritable:
		return 1
	case reasonRestoreLastAdminKept:
		return 2
	case reasonRestoreAdminsReview:
		return 3
	case journal.HealthQueued:
		return 4
	case journal.HealthReset:
		return 5
	case journal.HealthColocated:
		return 6
	}
	return backupRankUnknown
}

// combineBackup is core.backup's Result for tokens: see the rule above. It
// does not modify tokens.
func combineBackup(tokens []backupToken) health.Result {
	if len(tokens) == 0 {
		return health.Result{Status: health.StatusUp}
	}
	sorted := slices.Clone(tokens)
	slices.SortStableFunc(sorted, func(a, b backupToken) int {
		if c := cmp.Compare(b.Status, a.Status); c != 0 {
			return c // higher severity first
		}
		if c := cmp.Compare(backupReasonRank(a.Reason), backupReasonRank(b.Reason)); c != 0 {
			return c
		}
		return strings.Compare(a.Reason, b.Reason) // two unlisted reasons: a fixed order still
	})
	parts := make([]string, 0, len(sorted))
	for _, t := range sorted {
		parts = append(parts, t.Reason+": "+t.Detail)
	}
	return health.Result{Status: sorted[0].Status, Reason: sorted[0].Reason, Detail: strings.Join(parts, "; ")}
}

// journalTokens maps the journal's tokens onto core.backup's levels: degraded
// is StatusDegraded and info is StatusUp. A severity this mapping does not
// know is StatusDegraded, the louder of the two, so a new journal level is
// shown rather than hidden as info.
func journalTokens(hs []journal.Health) []backupToken {
	out := make([]backupToken, 0, len(hs))
	for _, h := range hs {
		st := health.StatusDegraded
		if h.Severity == journal.SeverityInfo {
			st = health.StatusUp
		}
		out = append(out, backupToken{Reason: h.Token, Status: st, Detail: h.Detail})
	}
	return out
}

// restoreTokens are the two restore tokens rbac's hook reported. Each Detail
// names the accounts (user IDs, not names or e-mail); restore_last_admin_kept's
// also names the recovery path (05-health §5H.3.1).
func restoreTokens(r hookReport) []backupToken {
	var out []backupToken
	if len(r.KeptAdmins) > 0 {
		out = append(out, backupToken{Reason: reasonRestoreLastAdminKept, Status: health.StatusDegraded, Detail: fmt.Sprintf(
			"%d account(s) kept their last core:admin entry and stay restricted (%s); recover with an owner-verified reset link or the offline users reset-password command",
			len(r.KeptAdmins), strings.Join(r.KeptAdmins, ", "))})
	}
	if len(r.AdminsToReview) > 0 {
		out = append(out, backupToken{Reason: reasonRestoreAdminsReview, Status: health.StatusDegraded, Detail: fmt.Sprintf(
			"the restore had no pre-swap store: review the %d core:admin holder(s) (%s)",
			len(r.AdminsToReview), strings.Join(r.AdminsToReview, ", "))})
	}
	return out
}

// backupProbe is core.backup's probe (coreComponentSources.Backup): the
// journal's tokens and the restore tokens, combined. Either source may be nil
// (no journal bound, no restore hooks this build). The journal's Health reads
// no store and writes nothing, so the probe is cheap. Nothing calls this until
// F4-08's B3 wires the boot.
func backupProbe(journalHealth func() []journal.Health, restore func() hookReport) func(context.Context) health.Result {
	return func(context.Context) health.Result {
		var tokens []backupToken
		if journalHealth != nil {
			tokens = append(tokens, journalTokens(journalHealth())...)
		}
		if restore != nil {
			tokens = append(tokens, restoreTokens(restore())...)
		}
		return combineBackup(tokens)
	}
}
