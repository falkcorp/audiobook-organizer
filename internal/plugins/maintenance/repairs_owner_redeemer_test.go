// file: internal/plugins/maintenance/repairs_owner_redeemer_test.go
// version: 1.0.0
// guid: 5b0d83e6-4c19-47aa-9f52-e1a7c60d3b84
// last-edited: 2026-10-26

package maintenance

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// ownerOpsDeps serves the op rows ownerRedeemer reads.
type ownerOpsDeps struct {
	fakeDeps
	ops *planOps
}

func (d ownerOpsDeps) OperationQueueStore() OpQueueReader { return d.ops }

// An owner grant is redeemed only by the op the Repairs owner-apply handler
// marked (registry.WithOwnerApply). The generic start and retry routes record
// the actor and how it signed in for every caller, so the owner's own Cloudflare
// Access session on one of them stores the same actor and the same cf_access
// method as the handler's op; only the mark tells them apart. A grant token is
// readable from GET /operations, so params prove nothing either: each row below
// carries a valid grant for the very same user, fixer, plan and rows.
func TestOwnerRedeemer_OnlyTheOwnerApplyHandlersOpRedeemsAGrant(t *testing.T) {
	const (
		owner   = "01JOWNERBRUCEWAYNE0000000"
		email   = "bruce.wayne@example.invalid"
		fixerID = "version-group-primary"
		planOp  = "op-plan-year-one"
	)
	str := func(s string) *string { return &s }
	rows := map[string]*database.OperationV2Row{
		"handler":      {ID: "handler", ActorUserID: str(owner), ActorAuthMethod: repairs.OwnerAuthMethod, OwnerApply: true},
		"generic":      {ID: "generic", ActorUserID: str(owner), ActorAuthMethod: repairs.OwnerAuthMethod},
		"handler-key":  {ID: "handler-key", ActorUserID: str(owner), ActorAuthMethod: "api_key", OwnerApply: true},
		"handler-retr": {ID: "handler-retr", ActorUserID: str(owner), ActorAuthMethod: repairs.OwnerAuthMethod, OwnerApply: true, ManualRetryCount: 1},
		"handler-noac": {ID: "handler-noac", ActorAuthMethod: repairs.OwnerAuthMethod, OwnerApply: true},
	}
	cases := []struct {
		opID       string
		wantRedeem bool
	}{
		{"handler", true},
		{"generic", false},
		{"handler-key", false},
		{"handler-retr", false},
		{"handler-noac", false},
	}
	for _, tc := range cases {
		t.Run(tc.opID, func(t *testing.T) {
			p := &Plugin{
				deps:       ownerOpsDeps{ops: &planOps{MemOpRows: dbtest.NewMemOpRows(), rows: rows}},
				ownerEmail: func() string { return email },
			}
			grants := repairs.NewOwnerGrants()
			tok, err := grants.Issue(repairs.OwnerGrant{UserID: owner, AuthMethod: repairs.OwnerAuthMethod,
				AccessEmail: email, FixerID: fixerID, PlanOpID: planOp, RowIDs: []string{"r1"}})
			require.NoError(t, err)
			params := repairs.ApplyParams{FixerID: fixerID, PlanOpID: planOp,
				OwnerApplyRowIDs: []string{"r1"}, OwnerGrant: tok}

			got := repairs.ResolveOwnerApproval(grants, params, p.ownerRedeemer(&repairsOpReporter{id: tc.opID}))
			require.NotNil(t, got)
			if tc.wantRedeem {
				require.Empty(t, got.Refused)
				require.Equal(t, owner, got.UserID)
				return
			}
			require.NotEmpty(t, got.Refused, "the grant must not be redeemed by an op the handler did not mark")
		})
	}
}
