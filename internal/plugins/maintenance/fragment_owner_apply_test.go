// file: internal/plugins/maintenance/fragment_owner_apply_test.go
// version: 1.0.0
// guid: 2b7e9d40-1c56-4a83-b9f2-8e0d4a6c3f17
// last-edited: 2026-10-06

package maintenance

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// ownLibAITunesPath is an iTunes location naming libA's own library file.
const ownLibAITunesPath = "file://localhost/W:/audiobook-organizer/Many Parts copy A/02.mp3"

// ownerFixture is the owner's 2026-10-06 shape: libA and libB are
// byte-identical copies of the parent's row 02, and libA's own row carries
// an iTunes path naming its own file (iTunes tracks it).
func ownerFixture(t *testing.T, itunesParent bool) *fragFixture {
	t.Helper()
	f := copyClaimantsFixture(t, false)
	if itunesParent {
		linkParentToITunes(t, f)
	}
	f.writeSame(t, "same", hpParent, hpLibA, hpLibB)
	f.updateRow(t, f.ids["libA"], f.rowIDs["libA"], func(r *database.BookFile) { r.ITunesPath = ownLibAITunesPath })
	return f
}

// ownerApply runs repairs.apply for owner rows under a grant minted for
// them (as the owner's click does), or under tok when it is not "".
func (f *fragFixture) ownerApply(t *testing.T, planOpID, opID string, rows []string, tok string, resume *repairs.ApplyCheckpoint) *repairs.ApplyResult {
	t.Helper()
	if tok == "" {
		var err error
		tok, err = repairs.DefaultOwnerGrants.Issue(repairs.OwnerGrant{UserID: "owner-user", AuthMethod: "session",
			FixerID: fragFixerID, PlanOpID: planOpID, RowIDs: rows})
		require.NoError(t, err)
	}
	f.applyOp(opID, fragFixerID)
	no := false
	params, err := json.Marshal(repairs.ApplyParams{FixerID: fragFixerID, PlanOpID: planOpID, DryRun: &no,
		OwnerApplyRowIDs: rows, OwnerGrant: tok, Resume: resume})
	require.NoError(t, err)
	rep := &repairsOpReporter{id: opID}
	require.NoError(t, f.p.runRepairsApply(context.Background(), params, rep))
	res, ok := rep.result.(*repairs.ApplyResult)
	require.True(t, ok)
	return res
}

func TestFragmentFixer_OwnerApply(t *testing.T) {
	t.Parallel()

	for _, itunesParent := range []bool{true, false} {
		name := "plain parent"
		if itunesParent {
			name = "iTunes-linked parent"
		}
		t.Run(name+": listed for the owner; bulk refuses; the owner's grant retires the fragment alone; revert restores it", func(t *testing.T) {
			t.Parallel()
			f := ownerFixture(t, itunesParent)
			parent := f.ids["parent"]
			b0, err := f.s.GetBookByID(parent)
			require.NoError(t, err)
			rows0, err := f.s.GetBookFiles(parent)
			require.NoError(t, err)

			res := f.plan(t, "op-plan")
			m := findRow(t, res, "manual:"+f.ids["libA"])
			require.Equal(t, fragClassManual, m.Class)
			require.False(t, m.Applicable())
			require.True(t, m.OwnerApplicable, "%s", m.SkipReason)
			require.Contains(t, m.OwnerApplyReason, "byte-identical")
			require.Contains(t, m.OwnerApplyReason, "only database rows change")
			require.Equal(t, []string{f.ids["libA"]}, m.OwnerWrites)
			require.Contains(t, m.Evidence[0], fragEvContentHashPrefix)
			require.Equal(t, 1, res.OwnerApplicable)

			// Bulk / "apply all": not applicable, nothing written.
			out := f.apply(t, "op-plan", "op-bulk", []string{m.RowID}, nil)
			require.Equal(t, repairs.OutcomeNotApplicable, out.Rows[0].Outcome)
			require.True(t, f.live(t, "libA"))

			// The owner's grant: applied.
			out = f.ownerApply(t, "op-plan", "op-apply", []string{m.RowID}, "", nil)
			require.Equal(t, 1, out.Applied, "%+v", out.Rows)
			require.Equal(t, "owner-user", out.OwnerUserID)
			a, err := f.s.GetBookByID(f.ids["libA"])
			require.NoError(t, err)
			require.True(t, a.IsSoftDeleted())
			require.NotNil(t, a.MergedIntoBookID)
			require.Equal(t, parent, *a.MergedIntoBookID)
			b1, err := f.s.GetBookByID(parent)
			require.NoError(t, err)
			require.Equal(t, *b0, *b1, "the parent book is not written")
			rows1, err := f.s.GetBookFiles(parent)
			require.NoError(t, err)
			require.Equal(t, rows0, rows1, "the parent's rows are not written")
			arows, err := f.s.GetBookFiles(f.ids["libA"])
			require.NoError(t, err)
			require.Len(t, arows, 1, "the fragment keeps its row")
			require.Equal(t, ownLibAITunesPath, arows[0].ITunesPath, "the iTunes path is not touched")

			changes, err := f.s.GetOperationChanges("op-apply")
			require.NoError(t, err)
			notes := 0
			for _, c := range changes {
				require.Equal(t, f.ids["libA"], c.BookID, "only the fragment is written: %+v", c)
				require.NotContains(t, []string{undo.ChangeTypeUserStateFollow, undo.ChangeTypeExternalIDReassign}, c.ChangeType)
				if c.ChangeType == undo.ChangeTypeRepairOwnerApply {
					notes++
					require.Contains(t, c.NewValue, `"user_id":"owner-user"`)
					require.Contains(t, c.NewValue, `"auth_method":"session"`)
				}
			}
			require.Equal(t, 1, notes, "one owner audit note")

			// libB, the plain library copy, is still its own applicable row.
			require.True(t, f.live(t, "libB"))

			_, err = audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
			require.NoError(t, err)
			require.True(t, f.live(t, "libA"), "the op revert restores the fragment")
			a, err = f.s.GetBookByID(f.ids["libA"])
			require.NoError(t, err)
			require.True(t, a.MergedIntoBookID == nil || *a.MergedIntoBookID == "")
		})
	}

	t.Run("params without a valid grant, a reused grant, or a resume never apply it", func(t *testing.T) {
		t.Parallel()
		f := ownerFixture(t, true)
		m := findRow(t, f.plan(t, "op-plan"), "manual:"+f.ids["libA"])
		require.True(t, m.OwnerApplicable)

		out := f.ownerApply(t, "op-plan", "op-forged", []string{m.RowID}, "not-a-grant", nil)
		require.Equal(t, repairs.OutcomeOwnerRefused, out.Rows[0].Outcome)
		require.True(t, f.live(t, "libA"))

		tok, err := repairs.DefaultOwnerGrants.Issue(repairs.OwnerGrant{UserID: "owner-user", AuthMethod: "session",
			FixerID: fragFixerID, PlanOpID: "op-plan", RowIDs: []string{m.RowID}})
		require.NoError(t, err)
		out = f.ownerApply(t, "op-plan", "op-resumed", []string{m.RowID}, tok, &repairs.ApplyCheckpoint{})
		require.Equal(t, repairs.OutcomeOwnerRefused, out.Rows[0].Outcome)
		require.True(t, f.live(t, "libA"))
		// The resume consumed it: the same token again finds nothing.
		out = f.ownerApply(t, "op-plan", "op-reuse", []string{m.RowID}, tok, nil)
		require.Equal(t, repairs.OutcomeOwnerRefused, out.Rows[0].Outcome)
		require.True(t, f.live(t, "libA"))
	})

	t.Run("ineligible: no content proof, a PID, an iTunes Media path, listening state, an external id, the parent in its group", func(t *testing.T) {
		t.Parallel()
		cases := map[string]func(t *testing.T, f *fragFixture){
			"no content proof": func(t *testing.T, f *fragFixture) { f.noContentReads(t) },
			"book PID":         func(t *testing.T, f *fragFixture) { setBookPID(t, f, f.ids["libA"], "PIDLIBA") },
			"iTunes Media path": func(t *testing.T, f *fragFixture) {
				f.updateRow(t, f.ids["libA"], f.rowIDs["libA"], func(r *database.BookFile) {
					r.ITunesPath = "file://localhost/W:/itunes/iTunes Media/Many Parts/02.mp3"
				})
			},
			"listening state": func(t *testing.T, f *fragFixture) {
				u, err := f.s.CreateUser("reader", "reader@example.com", "bcrypt", "x", []string{"user"}, "active")
				require.NoError(t, err)
				require.NoError(t, f.s.SetUserPosition(u.ID, f.ids["libA"], f.rowIDs["libA"], 100))
			},
			"external id": func(t *testing.T, f *fragFixture) {
				require.NoError(t, f.s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "audible", ExternalID: "B0LIBA", BookID: f.ids["libA"]}))
			},
			"parent in its version group": func(t *testing.T, f *fragFixture) {
				g := "vg-owner"
				for _, id := range []string{f.ids["libA"], f.ids["parent"]} {
					_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.VersionGroupID = &g; return nil })
					require.NoError(t, err)
				}
			},
		}
		for name, mutate := range cases {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				f := ownerFixture(t, true)
				mutate(t, f)
				res := f.plan(t, "op-plan")
				m := rowWithBook(t, res.Rows, f.ids["libA"])
				if m.RowID == "copy:"+f.ids["parent"] {
					// Not split off at all (nothing iTunes about it any more
					// would make it applicable; here it must stay skipped).
					require.False(t, m.OwnerApplicable)
					return
				}
				require.False(t, m.Applicable(), "%s", m.RowID)
				require.False(t, m.OwnerApplicable, "%s: %s", m.RowID, m.SkipReason)
				out := f.ownerApply(t, "op-plan", "op-apply", []string{m.RowID}, "", nil)
				require.Equal(t, repairs.OutcomeOwnerRefused, out.Rows[0].Outcome, "%+v", out.Rows)
				require.True(t, f.live(t, "libA"))
			})
		}
	})

	t.Run("listening state landing after the plan is refused under the lock", func(t *testing.T) {
		t.Parallel()
		f := ownerFixture(t, true)
		m := findRow(t, f.plan(t, "op-plan"), "manual:"+f.ids["libA"])
		require.True(t, m.OwnerApplicable)
		u, err := f.s.CreateUser("reader", "reader@example.com", "bcrypt", "x", []string{"user"}, "active")
		require.NoError(t, err)
		require.NoError(t, f.s.SetUserPosition(u.ID, f.ids["libA"], f.rowIDs["libA"], 100))
		out := f.ownerApply(t, "op-plan", "op-apply", []string{m.RowID}, "", nil)
		require.Zero(t, out.Applied, "%+v", out.Rows)
		require.True(t, f.live(t, "libA"))
	})

	t.Run("a fragment file moved under the iTunes library since the plan is refused", func(t *testing.T) {
		t.Parallel()
		f := ownerFixture(t, false)
		m := findRow(t, f.plan(t, "op-plan"), "manual:"+f.ids["libA"])
		require.True(t, m.OwnerApplicable)
		f.updateRow(t, f.ids["libA"], f.rowIDs["libA"], func(r *database.BookFile) {
			r.ITunesPath = "file://localhost/W:/itunes/iTunes Media/Many Parts/02.mp3"
		})
		out := f.ownerApply(t, "op-plan", "op-apply", []string{m.RowID}, "", nil)
		require.Zero(t, out.Applied, "%+v", out.Rows)
		require.True(t, f.live(t, "libA"))
	})
}
