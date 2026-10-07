// file: internal/repairs/owner_test.go
// version: 1.1.0
// guid: 9d4e2b71-6c38-4a05-8f1e-3b7a5c0d2e96
// last-edited: 2026-10-07

package repairs

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// ownerFixer lists "own" as an owner row (skipped, owner-applicable) and
// "manual" as a plain manual row; "fix" is an ordinary applicable row. Its
// Apply of "own" writes only under an owner approval naming the row.
type ownerFixer struct{ s *memStore }

func (f *ownerFixer) ID() string          { return "owner-fx" }
func (f *ownerFixer) Title() string       { return "owner" }
func (f *ownerFixer) Description() string { return "test fixer" }

func (f *ownerFixer) row(id string) Row {
	r := Row{RowID: id, BookIDs: []string{id}, Title: f.s.title(id), Fingerprint: "fp:" + f.s.title(id), Risk: RiskReview}
	switch id {
	case "own":
		r.Skipped, r.SkipReason = SkipITunes, "iTunes tracks this file"
		r.OwnerApplicable, r.OwnerApplyReason, r.OwnerWrites = true, "content proven", []string{"own"}
	case "manual":
		r.Skipped, r.SkipReason = SkipITunes, "iTunes book"
	}
	return r
}

func (f *ownerFixer) Plan(context.Context, json.RawMessage, registry.Reporter) ([]Row, error) {
	return []Row{f.row("own"), f.row("manual"), f.row("fix")}, nil
}

func (f *ownerFixer) Replan(_ context.Context, _ json.RawMessage, planned Row, _ registry.Reporter) (Row, error) {
	return f.row(planned.RowID), nil
}

func (f *ownerFixer) Apply(ctx context.Context, w *Writer, fresh Row) error {
	if !fresh.Applicable() {
		if o, ok := OwnerApplyFrom(ctx); !ok || o.RowID != fresh.RowID {
			return ErrChangedSincePlan
		}
	}
	_, err := w.Modify(fresh.RowID, func(b *database.Book) error {
		b.Title += " (done)"
		return nil
	})
	return err
}

func ownerSetup(t *testing.T) (*memStore, *ownerFixer, *PlanResult, *creditFake, ApplyDeps) {
	t.Helper()
	s := newMemStore()
	for _, id := range []string{"own", "manual", "fix"} {
		s.add(id, id, "/lib/A/"+id+"/x.m4b", nil)
	}
	f := &ownerFixer{s: s}
	plan := planFor(t, s, f)
	j := &creditFake{credits: map[string][]database.BookAuthor{}}
	d := deps(s, &fakeStandDown{renewsLeft: -1})
	d.Writer = NewWriter(s, s, f.ID(), "bulk_update", "rp-").WithJournal(nil, j, "op-apply")
	return s, f, plan, j, d
}

func grantFor(t *testing.T, g *OwnerGrants, rows ...string) string {
	t.Helper()
	tok, err := g.Issue(OwnerGrant{UserID: "owner", AuthMethod: "cf_access", AccessEmail: "owner@example.test", FixerID: "owner-fx", PlanOpID: "op-plan", RowIDs: rows})
	require.NoError(t, err)
	return tok
}

func TestRunPlan_CountsAndPagesOwnerRows(t *testing.T) {
	s := newMemStore()
	for _, id := range []string{"own", "manual", "fix"} {
		s.add(id, id, "/lib/A/"+id+"/x.m4b", nil)
	}
	plan := planFor(t, s, &ownerFixer{s: s})
	require.Equal(t, 1, plan.OwnerApplicable)
	page, err := plan.Page("op-plan", FilterOwnerApplicable, "", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Rows, 1)
	require.Equal(t, "own", page.Rows[0].RowID)
	require.Equal(t, 1, page.OwnerApplicable)
}

// A guard the fixer cannot see (here a Doctor Who path) withdraws the owner's
// Apply at plan time; the row stays skipped as the fixer said.
func TestRunPlan_GuardWithdrawsOwnerApply(t *testing.T) {
	s := newMemStore()
	s.add("own", "own", "/lib/Doctor Who/own/x.m4b", nil)
	s.add("manual", "manual", "/lib/A/manual/x.m4b", nil)
	s.add("fix", "fix", "/lib/A/fix/x.m4b", nil)
	plan := planFor(t, s, &ownerFixer{s: s})
	require.Zero(t, plan.OwnerApplicable)
	for _, r := range plan.Rows {
		if r.RowID == "own" {
			require.False(t, r.OwnerApplicable)
			require.Equal(t, SkipITunes, r.Skipped)
			require.Contains(t, r.SkipReason, "not owner-applicable")
		}
	}
}

// Bulk / "apply all": an owner row named in row_ids is not applicable.
func TestRunApply_BulkNeverAppliesOwnerRows(t *testing.T) {
	s, f, plan, _, d := ownerSetup(t)
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"own", "manual", "fix"}, false, d, nopReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, res.Applied)
	require.Equal(t, "own", s.title("own"))
	for _, r := range res.Rows {
		if r.RowID == "own" {
			require.Equal(t, OutcomeNotApplicable, r.Outcome)
		}
	}
}

// With a consumed grant the owner row is applied and the audit note is
// journaled before the write, naming the user; the grant is single-use.
func TestRunApply_OwnerGrantAppliesOwnerRowWithAudit(t *testing.T) {
	s, f, plan, j, d := ownerSetup(t)
	g := NewOwnerGrants()
	p := ApplyParams{FixerID: f.ID(), PlanOpID: "op-plan", OwnerApplyRowIDs: []string{"own"}, OwnerGrant: grantFor(t, g, "own")}
	d.Owner = ResolveOwnerApproval(g, p)
	require.Empty(t, d.Owner.Refused)
	res, err := RunApply(context.Background(), f, plan, "op-plan", nil, false, d, nopReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, res.Applied, "%+v", res.Rows)
	require.Equal(t, "own (done)", s.title("own"))
	require.Equal(t, "owner", res.OwnerUserID)
	require.Equal(t, "owner", res.Rows[0].OwnerUserID)
	var notes int
	for _, c := range j.journal {
		if c.ChangeType == undo.ChangeTypeRepairOwnerApply {
			notes++
			require.Equal(t, "own", c.BookID)
			require.Contains(t, c.NewValue, `"user_id":"owner"`)
			require.True(t, undo.IsLedgerOnly(&c) && undo.IsRestorable(&c))
		}
	}
	require.Equal(t, 1, notes)
	require.Equal(t, "journal:own", j.order[0], "the audit note precedes the write")

	// The same token again (a retry, a copied param) finds nothing.
	again := ResolveOwnerApproval(g, p)
	require.NotEmpty(t, again.Refused)
}

// Params alone (POST /operations/v2 written by hand, a forged token, a
// grant for another row) never apply an owner row.
func TestRunApply_OwnerRowsRefusedWithoutValidGrant(t *testing.T) {
	g := NewOwnerGrants()
	other := grantFor(t, g, "manual")
	for name, p := range map[string]ApplyParams{
		"no grant":       {FixerID: "owner-fx", PlanOpID: "op-plan", OwnerApplyRowIDs: []string{"own"}},
		"forged token":   {FixerID: "owner-fx", PlanOpID: "op-plan", OwnerApplyRowIDs: []string{"own"}, OwnerGrant: "deadbeef"},
		"other rows":     {FixerID: "owner-fx", PlanOpID: "op-plan", OwnerApplyRowIDs: []string{"own"}, OwnerGrant: other},
		"other plan":     {FixerID: "owner-fx", PlanOpID: "op-other", OwnerApplyRowIDs: []string{"own"}, OwnerGrant: grantFor(t, g, "own")},
		"resumed run":    {FixerID: "owner-fx", PlanOpID: "op-plan", OwnerApplyRowIDs: []string{"own"}, OwnerGrant: grantFor(t, g, "own"), Resume: &ApplyCheckpoint{}},
		"expired":        {FixerID: "owner-fx", PlanOpID: "op-plan", OwnerApplyRowIDs: []string{"own"}, OwnerGrant: expiredGrant(t, g)},
		"manual row too": {FixerID: "owner-fx", PlanOpID: "op-plan", OwnerApplyRowIDs: []string{"own", "manual"}, OwnerGrant: grantFor(t, g, "own")},
	} {
		t.Run(name, func(t *testing.T) {
			s, f, plan, _, d := ownerSetup(t)
			d.Owner = ResolveOwnerApproval(g, p)
			if p.Resume != nil {
				d.Resumed = p.Resume
			}
			res, err := RunApply(context.Background(), f, plan, "op-plan", nil, false, d, nopReporter{})
			require.NoError(t, err)
			require.Zero(t, res.Applied)
			require.Equal(t, "own", s.title("own"))
			for _, r := range res.Rows {
				require.Equal(t, OutcomeOwnerRefused, r.Outcome, "%+v", r)
			}
		})
	}
}

func expiredGrant(t *testing.T, g *OwnerGrants) string {
	t.Helper()
	tok := grantFor(t, g, "own")
	g.mu.Lock()
	gr := g.m[tok]
	gr.ExpiresAt = time.Now().Add(-time.Minute)
	g.m[tok] = gr
	g.mu.Unlock()
	return tok
}

// A grant naming a row the plan did not mark owner-applicable is refused for
// that row even when the grant itself is valid.
func TestRunApply_OwnerGrantForNonOwnerRowRefused(t *testing.T) {
	s, f, plan, _, d := ownerSetup(t)
	g := NewOwnerGrants()
	d.Owner = ResolveOwnerApproval(g, ApplyParams{FixerID: f.ID(), PlanOpID: "op-plan",
		OwnerApplyRowIDs: []string{"manual"}, OwnerGrant: grantFor(t, g, "manual")})
	require.Empty(t, d.Owner.Refused)
	res, err := RunApply(context.Background(), f, plan, "op-plan", nil, false, d, nopReporter{})
	require.NoError(t, err)
	require.Equal(t, OutcomeOwnerRefused, res.Rows[0].Outcome)
	require.True(t, strings.Contains(res.Rows[0].Error, "does not list"))
	require.Equal(t, "manual", s.title("manual"))
}

// A resumed run keeps an owner row's settled result and never re-applies an
// unsettled one, even when handed a live approval.
func TestRunApply_ResumeNeverAppliesOwnerRows(t *testing.T) {
	s, f, plan, _, d := ownerSetup(t)
	d.Resumed = &ApplyCheckpoint{Settled: []RowResult{{RowID: "fix", Outcome: OutcomeApplied}}}
	d.Owner = &OwnerApproval{UserID: "owner", AuthMethod: "cf_access", RowIDs: []string{"own"}}
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"fix"}, false, d, nopReporter{})
	require.NoError(t, err)
	require.Equal(t, "own", s.title("own"))
	for _, r := range res.Rows {
		if r.RowID == "own" {
			require.Equal(t, OutcomeOwnerRefused, r.Outcome)
		}
	}
	require.Empty(t, res.OwnerUserID)
}

func TestOwnerApplyFrom_RequiresRowAndUser(t *testing.T) {
	_, ok := OwnerApplyFrom(context.Background())
	require.False(t, ok)
	_, ok = OwnerApplyFrom(WithOwnerApply(context.Background(), OwnerApplyContext{RowID: "r"}))
	require.False(t, ok)
	o, ok := OwnerApplyFrom(WithOwnerApply(context.Background(), OwnerApplyContext{RowID: "r", UserID: "u"}))
	require.True(t, ok)
	require.Equal(t, "r", o.RowID)
}

// An owner grant is minted only for a verified Cloudflare Access owner
// sign-in (owner decision 2026-10-07), whoever calls Issue: a session, an
// API key or an Access grant with no email is refused, so the exceptions a
// grant unlocks (the owner row and its OwnerITunesDatabaseOnly books) cannot
// be reached any other way.
func TestOwnerGrants_IssueOnlyForAccessOwner(t *testing.T) {
	g := NewOwnerGrants()
	base := OwnerGrant{UserID: "owner", FixerID: "fx", PlanOpID: "op", RowIDs: []string{"own"}}
	for name, mut := range map[string]func(*OwnerGrant){
		"password session": func(gr *OwnerGrant) { gr.AuthMethod = "session"; gr.AccessEmail = "owner@example.test" },
		"api key":          func(gr *OwnerGrant) { gr.AuthMethod = "api_key"; gr.AccessEmail = "owner@example.test" },
		"access, no email": func(gr *OwnerGrant) { gr.AuthMethod = OwnerAuthMethod },
		"no method":        func(gr *OwnerGrant) { gr.AccessEmail = "owner@example.test" },
	} {
		gr := base
		mut(&gr)
		if _, err := g.Issue(gr); err == nil {
			t.Errorf("%s: Issue minted a grant", name)
		}
	}
	gr := base
	gr.AuthMethod, gr.AccessEmail = OwnerAuthMethod, "owner@example.test"
	if _, err := g.Issue(gr); err != nil {
		t.Fatalf("access owner grant refused: %v", err)
	}
}
