// file: internal/plugins/maintenance/fragment_copy_claimants_test.go
// version: 1.3.0
// guid: 3f8b2c61-7d4e-4a19-9c05-e2b6a8d17f43
// last-edited: 2026-10-08

package maintenance

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// copyClaimantsFixture seeds the owner's 2026-10-06 shape, synthetically: a
// two-row parent whose files are both on disk, and three single-file
// fragments that each claim the parent's row 02 by its original name and
// size: two in the library folder (libA, libB) and one under the iTunes
// library (itm). hashed puts the parent row's hash on every claimant
// (proven copies); otherwise the claims rest on the name and size only.
func copyClaimantsFixture(t *testing.T, hashed bool) *fragFixture {
	t.Helper()
	f := newFragFixture(t)
	p1 := f.file(t, "lib/Many Parts/01.mp3", 2001)
	p2 := f.file(t, "lib/Many Parts/02.mp3", 2002)
	parent := f.book(t, "parent", "Many Parts", f.path("lib/Many Parts"), nil)
	f.row(t, "p01", parent, p1, "01.mp3", 2001, 600, 1)
	f.row(t, "p02", parent, p2, "02.mp3", 2002, 600, 2)
	claim := func(role, rel string) string {
		p := f.file(t, rel, 2002)
		id := f.book(t, role, "02", p, nil)
		f.row(t, role, id, p, "02.mp3", 2002, 590, 0)
		f.organized(t, id)
		return id
	}
	claim("libA", "lib/Many Parts copy A/02.mp3")
	claim("libB", "lib/Many Parts copy B/02.mp3")
	claim("itm", "books/itunes/iTunes Media/Music/Many Parts/02.mp3")
	if hashed {
		for row, book := range map[string]string{"p02": "parent", "libA": "libA", "libB": "libB", "itm": "itm"} {
			f.updateRow(t, f.ids[book], f.rowIDs[row], func(r *database.BookFile) { r.FileHash = "hash-02" })
		}
	}
	return f
}

func (f *fragFixture) updateRow(t *testing.T, bookID, rowID string, mod func(*database.BookFile)) {
	t.Helper()
	r, err := f.s.GetBookFileByID(bookID, rowID)
	require.NoError(t, err)
	require.NotNil(t, r)
	mod(r)
	require.NoError(t, f.s.UpdateBookFile(r.ID, r))
}

// linkParentToITunes gives every parent row an iTunes path: the parent is
// then an iTunes-linked book (itunesCopyWhy's "row iTunes path").
func linkParentToITunes(t *testing.T, f *fragFixture) {
	t.Helper()
	rows, err := f.s.GetBookFiles(f.ids["parent"])
	require.NoError(t, err)
	for i := range rows {
		f.updateRow(t, f.ids["parent"], rows[i].ID, func(r *database.BookFile) {
			r.ITunesPath = "file://localhost/Music/iTunes Media/Many Parts/" + r.OriginalFilename
		})
	}
}

// TestFragmentFixer_CopyClaimants (owner decision 2026-10-06): several
// fragments claiming a parent row whose file is still on disk are each a
// copy of it, not a contradiction.
func TestFragmentFixer_CopyClaimants(t *testing.T) {
	t.Parallel()

	t.Run("unproven copies are listed as copies, the iTunes Media one too (owner 2026-10-08)", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, false)
		// Content not compared (unreadable here): the name-and-size rules.
		f.noContentReads(t)
		res := f.plan(t, "op-plan")
		r := findRow(t, res, fragRowCopyUnproven+":"+f.ids["parent"])
		require.Equal(t, fragClassCopy, r.Class)
		require.Equal(t, fragSkipCopyUnproven, r.Skipped, r.SkipReason)
		require.ElementsMatch(t, []string{f.ids["parent"], f.ids["libA"], f.ids["libB"], f.ids["itm"]}, r.BookIDs)
		for _, row := range res.Rows {
			require.NotEqual(t, fragClassAmbiguous, row.Class, "%s: %s", row.RowID, row.SkipReason)
		}
	})

	t.Run("a gone parent row with two present claimants stays ambiguous", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, false)
		require.NoError(t, os.Remove(f.path("lib/Many Parts/02.mp3")))
		res := f.plan(t, "op-plan")
		for _, role := range []string{"libA", "libB"} {
			r := findRow(t, res, "ambiguous:"+f.ids[role])
			require.Equal(t, fragSkipAmbiguous, r.Skipped)
			require.Contains(t, r.SkipReason, "parent row "+f.rowIDs["p02"]+" is claimed by 3 fragments")
		}
	})

	t.Run("claimants whose sizes on disk conflict stay ambiguous", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, false)
		// Content not compared (unreadable here): the name-and-size rules.
		f.noContentReads(t)
		require.NoError(t, os.WriteFile(f.path("lib/Many Parts copy B/02.mp3"), make([]byte, 2100), 0o644))
		res := f.plan(t, "op-plan")
		for _, role := range []string{"libA", "libB"} {
			r := findRow(t, res, "ambiguous:"+f.ids[role])
			require.Equal(t, fragSkipAmbiguous, r.Skipped)
			require.Contains(t, r.SkipReason, "is claimed by 3 fragments")
		}
		for _, row := range res.Rows {
			require.False(t, strings.HasPrefix(row.RowID, fragRowCopyUnproven+":"), "no copy row: %s", row.RowID)
		}
	})

	t.Run("claimants whose hashes disagree stay ambiguous", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, false)
		// Content not compared (unreadable here): the name-and-size rules.
		f.noContentReads(t)
		// The parent row has no hash, so each still matches it by name and
		// size; the claimants' own hashes say they are different audio.
		f.updateRow(t, f.ids["libA"], f.rowIDs["libA"], func(r *database.BookFile) { r.FileHash = "hash-a" })
		f.updateRow(t, f.ids["libB"], f.rowIDs["libB"], func(r *database.BookFile) { r.FileHash = "hash-b" })
		res := f.plan(t, "op-plan")
		for _, role := range []string{"libA", "libB"} {
			r := findRow(t, res, "ambiguous:"+f.ids[role])
			require.Equal(t, fragSkipAmbiguous, r.Skipped)
			require.Contains(t, r.SkipReason, "is claimed by 3 fragments")
		}
	})

	t.Run("a library copy whose own row has an iTunes path retires like any other", func(t *testing.T) {
		// Review round 1, B1 held these manual-only; owner 2026-10-08
		// ("combine them too"): iTunes is import-only, so the copy retires,
		// database rows only, its row keeping its iTunes path.
		t.Parallel()
		for _, itParent := range []bool{false, true} {
			f := copyClaimantsFixture(t, true)
			if itParent {
				linkParentToITunes(t, f)
			}
			f.updateRow(t, f.ids["libA"], f.rowIDs["libA"], func(r *database.BookFile) {
				r.ITunesPath = "file://localhost/Music/iTunes Media/Many Parts/02.mp3"
			})
			res := f.plan(t, "op-plan")
			r := findRow(t, res, "copy:"+f.ids["parent"])
			require.ElementsMatch(t, []string{f.ids["parent"], f.ids["libA"], f.ids["libB"], f.ids["itm"]}, r.BookIDs)
			require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
			out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
			require.Equal(t, 1, out.Applied, "%+v", out.Rows)
			require.False(t, f.live(t, "libA"))
			require.False(t, f.live(t, "libB"))
			row := f.fileRow(t, "libA", "libA")
			require.Equal(t, "file://localhost/Music/iTunes Media/Many Parts/02.mp3", row.ITunesPath, "the row keeps its iTunes path")
		}
	})

	t.Run("a donor whose path twin is hands-off is held with it", func(t *testing.T) {
		// Review round 1, S2: the twin is split off (manual: a Doctor Who
		// series), so its donor, which shares its file, must not retire
		// beside a live co-owner.
		t.Parallel()
		f := copyClaimantsFixture(t, true)
		aPath := f.path("lib/Many Parts copy A/02.mp3")
		dw, err := f.s.CreateSeries("Doctor Who", nil)
		require.NoError(t, err)
		twin := f.book(t, "twin", "Many Parts - 02", aPath, &dw.ID)
		f.row(t, "twin", twin, aPath, "", 0, 0, 0)
		res := f.plan(t, "op-plan")
		require.Equal(t, fragClassManual, findRow(t, res, "manual:"+twin).Class)
		d := findRow(t, res, "held:"+f.ids["libA"])
		require.Contains(t, d.SkipReason, "shares its file with fragment "+twin)
		r := findRow(t, res, "copy:"+f.ids["parent"])
		require.ElementsMatch(t, []string{f.ids["parent"], f.ids["libB"], f.ids["itm"]}, r.BookIDs)
	})

	for _, tc := range []struct {
		name string
		link func(t *testing.T, f *fragFixture)
	}{
		{"book iTunes id", func(t *testing.T, f *fragFixture) { setBookPID(t, f, f.ids["parent"], "PARENTPID") }},
		{"row iTunes id", func(t *testing.T, f *fragFixture) {
			f.updateRow(t, f.ids["parent"], f.rowIDs["p01"], func(r *database.BookFile) { r.ITunesPersistentID = "ROWPID" })
		}},
		{"itunes external id", func(t *testing.T, f *fragFixture) {
			require.NoError(t, f.s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: "EXTPID", BookID: f.ids["parent"]}))
		}},
	} {
		t.Run("parent linked by "+tc.name+": the copies retire like any other (owner 2026-10-08)", func(t *testing.T) {
			t.Parallel()
			f := copyClaimantsFixture(t, true)
			tc.link(t, f)
			r := findRow(t, f.plan(t, "op-plan"), "copy:"+f.ids["parent"])
			require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
			require.Empty(t, r.Current["itunes_parent"], "no fragment-only mode any more")
			out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
			require.Equal(t, 1, out.Applied, "%+v", out.Rows)
			require.False(t, f.live(t, "libA"))
			require.False(t, f.live(t, "libB"))
		})
	}
}
