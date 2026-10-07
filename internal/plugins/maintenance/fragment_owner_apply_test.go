// file: internal/plugins/maintenance/fragment_owner_apply_test.go
// version: 1.3.0
// guid: 2b7e9d40-1c56-4a83-b9f2-8e0d4a6c3f17
// last-edited: 2026-10-07

package maintenance

import (
	"context"
	"encoding/json"
	"os"
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
		tok, err = repairs.DefaultOwnerGrants.Issue(repairs.OwnerGrant{UserID: "owner-user", AuthMethod: "cf_access", AccessEmail: "owner@example.test",
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
			m := findRow(t, res, fragRowOwner+":"+f.ids["parent"])
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
					require.Contains(t, c.NewValue, `"auth_method":"cf_access"`)
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
		m := findRow(t, f.plan(t, "op-plan"), fragRowOwner+":"+f.ids["parent"])
		require.True(t, m.OwnerApplicable)

		out := f.ownerApply(t, "op-plan", "op-forged", []string{m.RowID}, "not-a-grant", nil)
		require.Equal(t, repairs.OutcomeOwnerRefused, out.Rows[0].Outcome)
		require.True(t, f.live(t, "libA"))

		tok, err := repairs.DefaultOwnerGrants.Issue(repairs.OwnerGrant{UserID: "owner-user", AuthMethod: "cf_access", AccessEmail: "owner@example.test",
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

	// A run that fails before it reaches the rows (here: a plan op id that
	// does not exist) still consumes the grant, so retrying it (POST
	// /operations/v2/:id/retry copies the params, token included, for any
	// caller) never applies the owner's row.
	t.Run("a run that fails early consumes the grant; its retry is refused", func(t *testing.T) {
		t.Parallel()
		f := ownerFixture(t, true)
		m := findRow(t, f.plan(t, "op-plan"), fragRowOwner+":"+f.ids["parent"])
		require.True(t, m.OwnerApplicable)
		tok, err := repairs.DefaultOwnerGrants.Issue(repairs.OwnerGrant{UserID: "owner-user", AuthMethod: "cf_access", AccessEmail: "owner@example.test",
			FixerID: fragFixerID, PlanOpID: "op-plan", RowIDs: []string{m.RowID}})
		require.NoError(t, err)
		f.applyOp("op-fails", fragFixerID)
		no := false
		bad, err := json.Marshal(repairs.ApplyParams{FixerID: fragFixerID, PlanOpID: "op-no-such-plan", DryRun: &no,
			OwnerApplyRowIDs: []string{m.RowID}, OwnerGrant: tok})
		require.NoError(t, err)
		require.Error(t, f.p.runRepairsApply(context.Background(), bad, &repairsOpReporter{id: "op-fails"}))
		require.True(t, f.live(t, "libA"))

		out := f.ownerApply(t, "op-plan", "op-retry", []string{m.RowID}, tok, nil)
		require.Equal(t, repairs.OutcomeOwnerRefused, out.Rows[0].Outcome, "%+v", out.Rows)
		require.Zero(t, out.Applied)
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
		m := findRow(t, f.plan(t, "op-plan"), fragRowOwner+":"+f.ids["parent"])
		require.True(t, m.OwnerApplicable)
		u, err := f.s.CreateUser("reader", "reader@example.com", "bcrypt", "x", []string{"user"}, "active")
		require.NoError(t, err)
		require.NoError(t, f.s.SetUserPosition(u.ID, f.ids["libA"], f.rowIDs["libA"], 100))
		out := f.ownerApply(t, "op-plan", "op-apply", []string{m.RowID}, "", nil)
		require.Zero(t, out.Applied, "%+v", out.Rows)
		require.True(t, f.live(t, "libA"))
	})

	// The content proof is re-verified at apply time, not trusted from the
	// plan: a fragment or parent file rewritten after the plan (same size,
	// its mtime put back, so only ctime betrays it) refuses the owner's
	// apply, and nothing is written.
	for _, side := range []string{"fragment", "parent"} {
		t.Run("a "+side+" file rewritten since the plan (mtime restored) is refused at apply time", func(t *testing.T) {
			t.Parallel()
			f := ownerFixture(t, false)
			m := findRow(t, f.plan(t, "op-plan"), fragRowOwner+":"+f.ids["parent"])
			require.True(t, m.OwnerApplicable, "%s", m.SkipReason)
			rel := hpLibA
			if side == "parent" {
				rel = hpParent
			}
			p := f.path(rel)
			fi, err := os.Stat(p)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(p, fragFixtureBytes("different", 2002), 0o644))
			require.NoError(t, os.Chtimes(p, fi.ModTime(), fi.ModTime()))
			out := f.ownerApply(t, "op-plan", "op-apply", []string{m.RowID}, "", nil)
			require.Zero(t, out.Applied, "%+v", out.Rows)
			require.Equal(t, repairs.OutcomeChangedSincePlan, out.Rows[0].Outcome, "%+v", out.Rows)
			require.Contains(t, out.Rows[0].Error, "changed since the plan compared its content")
			require.True(t, f.live(t, "libA"))
			changes, err := f.s.GetOperationChanges("op-apply")
			require.NoError(t, err)
			require.Empty(t, changes, "no audit note and no write for a refused row")
		})
	}

	t.Run("a fragment file moved under the iTunes library since the plan is refused", func(t *testing.T) {
		t.Parallel()
		f := ownerFixture(t, false)
		m := findRow(t, f.plan(t, "op-plan"), fragRowOwner+":"+f.ids["parent"])
		require.True(t, m.OwnerApplicable)
		f.updateRow(t, f.ids["libA"], f.rowIDs["libA"], func(r *database.BookFile) {
			r.ITunesPath = "file://localhost/W:/itunes/iTunes Media/Many Parts/02.mp3"
		})
		out := f.ownerApply(t, "op-plan", "op-apply", []string{m.RowID}, "", nil)
		require.Zero(t, out.Applied, "%+v", out.Rows)
		require.True(t, f.live(t, "libA"))
	})
}

// ownerFixtureAll is the prod shape of 2026-10-07 (synthetic): an
// iTunes-linked parent whose library copies are ALL content-proven and each
// iTunes-tracked by its own row's iTunes path, written as a percent-encoded
// file:// URL, each fragment the primary of a version group of its own.
func ownerFixtureAll(t *testing.T) *fragFixture {
	t.Helper()
	f := copyClaimantsFixture(t, false)
	linkParentToITunes(t, f)
	f.writeSame(t, "same", hpParent, hpLibA, hpLibB)
	yes := true
	for role, dir := range map[string]string{"libA": "Many%20Parts%20copy%20A", "libB": "Many%20Parts%20copy%20B"} {
		f.updateRow(t, f.ids[role], f.rowIDs[role], func(r *database.BookFile) {
			r.ITunesPath = "file://localhost/W:/audiobook-organizer/" + dir + "/0%32.mp3"
		})
		g := "vg-" + role
		_, err := f.s.ModifyBook(f.ids[role], func(b *database.Book) error { b.VersionGroupID = &g; b.IsPrimaryVersion = &yes; return nil })
		require.NoError(t, err)
	}
	return f
}

func TestFragmentFixer_OwnerApplyWholeParentRow(t *testing.T) {
	t.Parallel()

	t.Run("every fragment on one owner row; one apply retires them all, the iTunes parent untouched; revert restores them", func(t *testing.T) {
		t.Parallel()
		f := ownerFixtureAll(t)
		parent := f.ids["parent"]
		b0, err := f.s.GetBookByID(parent)
		require.NoError(t, err)
		rows0, err := f.s.GetBookFiles(parent)
		require.NoError(t, err)

		res := f.plan(t, "op-plan")
		m := findRow(t, res, fragRowOwner+":"+parent)
		require.True(t, m.OwnerApplicable, "%s", m.SkipReason)
		require.False(t, m.Applicable())
		require.ElementsMatch(t, []string{f.ids["libA"], f.ids["libB"]}, m.OwnerWrites)
		require.ElementsMatch(t, []string{parent, f.ids["libA"], f.ids["libB"]}, m.BookIDs)
		require.Contains(t, m.OwnerApplyReason, "2 byte-identical copies")
		require.Contains(t, m.OwnerApplyReason, "iTunes-linked parent")
		require.Equal(t, 1, res.OwnerApplicable)
		for _, r := range res.Rows {
			require.NotEqual(t, "copy:"+parent, r.RowID, "no copy row is left for these fragments")
		}

		// A bulk apply naming the owner row writes nothing.
		out := f.apply(t, "op-plan", "op-bulk", []string{m.RowID}, nil)
		require.Equal(t, repairs.OutcomeNotApplicable, out.Rows[0].Outcome)
		require.True(t, f.live(t, "libA"))
		require.True(t, f.live(t, "libB"))

		out = f.ownerApply(t, "op-plan", "op-apply", []string{m.RowID}, "", nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
		for _, role := range []string{"libA", "libB"} {
			b, err := f.s.GetBookByID(f.ids[role])
			require.NoError(t, err)
			require.True(t, b.IsSoftDeleted(), role)
			require.NotNil(t, b.MergedIntoBookID)
			require.Equal(t, parent, *b.MergedIntoBookID)
			require.True(t, b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion, "%s demoted", role)
			rows, err := f.s.GetBookFiles(f.ids[role])
			require.NoError(t, err)
			require.Len(t, rows, 1, "%s keeps its row", role)
			require.Contains(t, rows[0].ITunesPath, "/0%32.mp3", "%s: the iTunes path is not touched", role)
		}
		b1, err := f.s.GetBookByID(parent)
		require.NoError(t, err)
		require.Equal(t, *b0, *b1, "the iTunes-linked parent book is not written")
		rows1, err := f.s.GetBookFiles(parent)
		require.NoError(t, err)
		require.Equal(t, rows0, rows1, "the parent's rows are not written")

		changes, err := f.s.GetOperationChanges("op-apply")
		require.NoError(t, err)
		notes := map[string]int{}
		for _, c := range changes {
			require.NotEqual(t, parent, c.BookID, "nothing is journaled on the parent: %+v", c)
			if c.ChangeType == undo.ChangeTypeRepairOwnerApply {
				notes[c.BookID]++
			}
		}
		require.Equal(t, map[string]int{f.ids["libA"]: 1, f.ids["libB"]: 1}, notes, "one owner audit note per fragment")

		_, err = audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
		require.NoError(t, err)
		require.True(t, f.live(t, "libA"))
		require.True(t, f.live(t, "libB"))
	})

	t.Run("one fragment file rewritten since the plan refuses the whole row; nothing is written", func(t *testing.T) {
		t.Parallel()
		f := ownerFixtureAll(t)
		m := findRow(t, f.plan(t, "op-plan"), fragRowOwner+":"+f.ids["parent"])
		require.True(t, m.OwnerApplicable, "%s", m.SkipReason)
		p := f.path(hpLibB)
		fi, err := os.Stat(p)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(p, fragFixtureBytes("different", 2002), 0o644))
		require.NoError(t, os.Chtimes(p, fi.ModTime(), fi.ModTime()))
		out := f.ownerApply(t, "op-plan", "op-apply", []string{m.RowID}, "", nil)
		require.Zero(t, out.Applied, "%+v", out.Rows)
		require.Equal(t, repairs.OutcomeChangedSincePlan, out.Rows[0].Outcome, "%+v", out.Rows)
		require.True(t, f.live(t, "libA"))
		require.True(t, f.live(t, "libB"))
		changes, err := f.s.GetOperationChanges("op-apply")
		require.NoError(t, err)
		require.Empty(t, changes)
	})

	t.Run("a refusal found under the lock for one fragment refuses the row before any write", func(t *testing.T) {
		t.Parallel()
		f := ownerFixtureAll(t)
		m := findRow(t, f.plan(t, "op-plan"), fragRowOwner+":"+f.ids["parent"])
		require.True(t, m.OwnerApplicable, "%s", m.SkipReason)
		// Listening state lands on the second fragment (by id) after the plan.
		u, err := f.s.CreateUser("reader", "reader@example.com", "bcrypt", "x", []string{"user"}, "active")
		require.NoError(t, err)
		last := m.OwnerWrites[len(m.OwnerWrites)-1]
		role := "libA"
		if last == f.ids["libB"] {
			role = "libB"
		}
		require.NoError(t, f.s.SetUserPosition(u.ID, last, f.rowIDs[role], 100))
		out := f.ownerApply(t, "op-plan", "op-apply", []string{m.RowID}, "", nil)
		require.Zero(t, out.Applied, "%+v", out.Rows)
		require.True(t, f.live(t, "libA"))
		require.True(t, f.live(t, "libB"))
		changes, err := f.s.GetOperationChanges("op-apply")
		require.NoError(t, err)
		for _, c := range changes {
			require.Equal(t, undo.ChangeTypeRepairOwnerApply, c.ChangeType, "only the audit notes, no write: %+v", c)
		}
	})

	t.Run("a fragment that is primary of a group with a plain member: the plain member is crowned, the parent untouched", func(t *testing.T) {
		t.Parallel()
		f := ownerFixtureAll(t)
		g := "vg-libA"
		no := false
		sp := f.file(t, "lib/Other/x.m4b", 77)
		sib := f.book(t, "sib", "02 (other edition)", sp, nil)
		f.row(t, "sib", sib, sp, "x.m4b", 77, 600, 0)
		f.organized(t, sib)
		_, err := f.s.ModifyBook(sib, func(b *database.Book) error { b.VersionGroupID = &g; b.IsPrimaryVersion = &no; return nil })
		require.NoError(t, err)
		b0, err := f.s.GetBookByID(f.ids["parent"])
		require.NoError(t, err)
		m := findRow(t, f.plan(t, "op-plan"), fragRowOwner+":"+f.ids["parent"])
		require.True(t, m.OwnerApplicable, "%s", m.SkipReason)
		out := f.ownerApply(t, "op-plan", "op-apply", []string{m.RowID}, "", nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
		s, err := f.s.GetBookByID(sib)
		require.NoError(t, err)
		require.True(t, s.IsPrimaryVersion != nil && *s.IsPrimaryVersion, "the plain member is crowned")
		a, err := f.s.GetBookByID(f.ids["libA"])
		require.NoError(t, err)
		require.True(t, a.IsSoftDeleted())
		require.True(t, a.IsPrimaryVersion != nil && !*a.IsPrimaryVersion)
		b1, err := f.s.GetBookByID(f.ids["parent"])
		require.NoError(t, err)
		require.Equal(t, *b0, *b1)
	})

	t.Run("two fragments in one version group are not owner-applicable (a hand-off could crown the other)", func(t *testing.T) {
		t.Parallel()
		f := ownerFixtureAll(t)
		g := "vg-shared"
		for _, role := range []string{"libA", "libB"} {
			_, err := f.s.ModifyBook(f.ids[role], func(b *database.Book) error { b.VersionGroupID = &g; return nil })
			require.NoError(t, err)
		}
		res := f.plan(t, "op-plan")
		for _, r := range res.Rows {
			require.False(t, r.OwnerApplicable, "%s: %s", r.RowID, r.SkipReason)
		}
		require.Zero(t, res.OwnerApplicable)
	})

	t.Run("mixed parent: owner-eligible, plain and hands-off fragments land on their own rows", func(t *testing.T) {
		t.Parallel()
		f := ownerFixture(t, true) // libA iTunes-tracked by its own path, libB plain
		res := f.plan(t, "op-plan")
		own := findRow(t, res, fragRowOwner+":"+f.ids["parent"])
		require.True(t, own.OwnerApplicable, "%s", own.SkipReason)
		require.Equal(t, []string{f.ids["libA"]}, own.OwnerWrites)
		cp := findRow(t, res, "copy:"+f.ids["parent"])
		require.True(t, cp.Applicable(), "%s", cp.SkipReason)
		require.ElementsMatch(t, []string{f.ids["parent"], f.ids["libB"]}, cp.BookIDs)
		itm := rowWithBook(t, res.Rows, f.ids["itm"])
		require.False(t, itm.Applicable())
		require.False(t, itm.OwnerApplicable)
	})
}

func TestItunesPathNamesFile_DecodesURLEscapes(t *testing.T) {
	t.Parallel()
	require.True(t, itunesPathNamesFile("file://localhost/W:/abo/Book/002%20of%20301/002%20of%20301.m4b", "/lib/Book/002 of 301/002 of 301.m4b"))
	require.True(t, itunesPathNamesFile(`W:\abo\Book\002 of 301.m4b`, "/lib/Book/002 of 301.m4b"))
	require.True(t, itunesPathNamesFile("file://localhost/W:/abo/100%.m4b", "/lib/100%.m4b"), "an undecodable name compares as written")
	require.False(t, itunesPathNamesFile("file://localhost/W:/abo/003%20of%20301.m4b", "/lib/002 of 301.m4b"))
}
