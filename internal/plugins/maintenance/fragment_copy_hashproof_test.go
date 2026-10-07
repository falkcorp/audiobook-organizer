// file: internal/plugins/maintenance/fragment_copy_hashproof_test.go
// version: 1.2.0
// guid: 9a4e7c13-5b2d-4f86-a0c1-7e3d9b6f2a58
// last-edited: 2026-10-06

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// Paths of copyClaimantsFixture's files, relative to the fixture root.
const (
	hpParent = "lib/Many Parts/02.mp3"
	hpLibA   = "lib/Many Parts copy A/02.mp3"
	hpLibB   = "lib/Many Parts copy B/02.mp3"
	hpITM    = "books/itunes/iTunes Media/Music/Many Parts/02.mp3"
)

// writeSame overwrites each rel with the same 2002 bytes (seeded by seed):
// byte-identical copies, at the size the fixture's rows record.
func (f *fragFixture) writeSame(t *testing.T, seed string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		require.NoError(t, os.WriteFile(f.path(rel), fragFixtureBytes(seed, 2002), 0o644))
	}
}

// registeredFragFixer is the fragment fixer the plugin's plan and apply ops
// run (f.plan, f.apply), so a test can swap its file reads.
func (f *fragFixture) registeredFragFixer(t *testing.T) *fragmentFixer {
	t.Helper()
	fx, ok := f.p.Repairs().Get(fragFixerID)
	require.True(t, ok)
	ff, ok := fx.(*fragmentFixer)
	require.True(t, ok, "%T", fx)
	return ff
}

// noContentReads makes every content read of the plugin's fragment fixer
// fail, so name-and-size claims stay unproven ("content not compared"), as
// they were before the plan read files: the tests of those rules use it.
func (f *fragFixture) noContentReads(t *testing.T) {
	t.Helper()
	f.registeredFragFixer(t).hashFn = func(string) (fragFileSig, string, error) {
		return fragFileSig{}, "", errors.New("content reads are off in this test")
	}
}

// rowWithBook is the one row whose books include id.
func rowWithBook(t *testing.T, rows []repairs.Row, id string) repairs.Row {
	t.Helper()
	var out []repairs.Row
	for _, r := range rows {
		if slices.Contains(r.BookIDs, id) {
			out = append(out, r)
		}
	}
	require.Len(t, out, 1, "rows holding %s", id)
	return out[0]
}

// TestFragmentFixer_CopyContentProof (owner decision 2026-10-06, "hash both,
// read-only"): a copy claimant matched by its original name and size only is
// proven by reading both files at plan time; byte-identical content makes it
// a proven copy, which retires into an iTunes-linked parent writing the
// fragments alone.
func TestFragmentFixer_CopyContentProof(t *testing.T) {
	t.Parallel()

	t.Run("equal bytes: the copies are proven and retire; nothing on the parent is written", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, false)
		linkParentToITunes(t, f)
		f.writeSame(t, "same", hpParent, hpLibA, hpLibB, hpITM)
		parent := f.ids["parent"]
		b0, err := f.s.GetBookByID(parent)
		require.NoError(t, err)
		rows0, err := f.s.GetBookFiles(parent)
		require.NoError(t, err)

		res := f.plan(t, "op-plan")
		r := findRow(t, res, "copy:"+parent)
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.ElementsMatch(t, []string{parent, f.ids["libA"], f.ids["libB"]}, r.BookIDs)
		require.Contains(t, r.Current["itunes_parent"], "row iTunes path")
		for _, ev := range r.Evidence {
			require.Contains(t, ev, "content hash equal at plan time: sha256:", ev)
		}
		itm := rowWithBook(t, res.Rows, f.ids["itm"])
		require.Equal(t, fragClassManual, itm.Class, itm.RowID)
		require.False(t, itm.Applicable())

		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
		for _, role := range []string{"libA", "libB"} {
			b, err := f.s.GetBookByID(f.ids[role])
			require.NoError(t, err)
			require.True(t, b.IsSoftDeleted(), role)
			require.NotNil(t, b.MergedIntoBookID)
			require.Equal(t, parent, *b.MergedIntoBookID)
			// Read-only: no hash is stored on the fragment's row either.
			rows, err := f.s.GetBookFiles(f.ids[role])
			require.NoError(t, err)
			for _, row := range rows {
				require.Empty(t, row.FileHash, role)
			}
		}
		require.True(t, f.live(t, "itm"), "the iTunes Media copy is never written")
		b1, err := f.s.GetBookByID(parent)
		require.NoError(t, err)
		require.Equal(t, *b0, *b1, "the parent book is not written")
		rows1, err := f.s.GetBookFiles(parent)
		require.NoError(t, err)
		require.Equal(t, rows0, rows1, "the parent's rows are not written (no file_hash stored)")
		changes, err := f.s.GetOperationChanges("op-apply")
		require.NoError(t, err)
		require.NotEmpty(t, changes)
		for _, c := range changes {
			require.Contains(t, []string{f.ids["libA"], f.ids["libB"]}, c.BookID, "only the fragments are written: %+v", c)
			require.NotContains(t, []string{undo.ChangeTypeUserStateFollow, undo.ChangeTypeExternalIDReassign}, c.ChangeType, "%+v", c)
		}
	})

	t.Run("same size, other bytes: each claimant is held, content differs", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, false)
		linkParentToITunes(t, f)
		f.writeSame(t, "parent bytes", hpParent)
		f.writeSame(t, "other bytes", hpLibA, hpLibB)
		res := f.plan(t, "op-plan")
		for _, role := range []string{"libA", "libB"} {
			r := findRow(t, res, fragClassHeld+":"+f.ids[role])
			require.Equal(t, fragClassHeld, r.Class)
			require.Equal(t, fragSkipCopyUnproven, r.Skipped)
			require.False(t, r.Applicable())
			require.Contains(t, r.SkipReason, "content differs")
			require.Contains(t, r.Evidence[0], fragEvContentDiffers)
		}
		for _, row := range res.Rows {
			require.NotEqual(t, "copy:"+f.ids["parent"], row.RowID, "nothing is proven")
		}
	})

	t.Run("one claimant equal, one different: the equal one retires, the other is held", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, false)
		f.writeSame(t, "same", hpParent, hpLibA)
		f.writeSame(t, "other bytes", hpLibB)
		res := f.plan(t, "op-plan")
		r := findRow(t, res, "copy:"+f.ids["parent"])
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.ElementsMatch(t, []string{f.ids["parent"], f.ids["libA"]}, r.BookIDs)
		require.Equal(t, "1", r.Current["content_proven"])
		b := findRow(t, res, fragClassHeld+":"+f.ids["libB"])
		require.Contains(t, b.SkipReason, "content differs")
	})

	for _, tc := range []struct{ name, rel string }{
		{"the fragment's file", hpLibA},
		{"the parent row's file", hpParent},
	} {
		t.Run(tc.name+" modified between plan and apply: changed since plan, nothing written", func(t *testing.T) {
			t.Parallel()
			f := copyClaimantsFixture(t, false)
			linkParentToITunes(t, f)
			f.writeSame(t, "same", hpParent, hpLibA, hpLibB)
			r := findRow(t, f.plan(t, "op-plan"), "copy:"+f.ids["parent"])
			require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
			// Rewritten in place: same size, and a distinct mtime so the
			// filesystem's timestamp granularity cannot hide it.
			p := f.path(tc.rel)
			require.NoError(t, os.WriteFile(p, fragFixtureBytes("rewritten", 2002), 0o644))
			later := time.Now().Add(time.Hour)
			require.NoError(t, os.Chtimes(p, later, later))
			out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
			require.Zero(t, out.Applied, "%+v", out.Rows)
			require.Equal(t, repairs.OutcomeChangedSincePlan, out.Rows[0].Outcome, "%+v", out.Rows)
			require.True(t, f.live(t, "libA"))
			require.True(t, f.live(t, "libB"))
		})
	}

	t.Run("a stored proof is refused by the locked re-plan once a file changes", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, false)
		f.writeSame(t, "same", hpParent, hpLibA, hpLibB)
		planned := findRow(t, f.plan(t, "op-plan"), "copy:"+f.ids["parent"])
		fx := newFragmentFixer(f.p)
		var reads atomic.Int64
		fx.hashFn = func(p string) (fragFileSig, string, error) { reads.Add(1); return fragHashFile(p) }
		fresh, err := fx.Replan(context.Background(), nil, planned, nil)
		require.NoError(t, err)
		require.Equal(t, planned.Fingerprint, fresh.Fingerprint, fresh.Reason)
		later := time.Now().Add(2 * time.Hour)
		require.NoError(t, os.Chtimes(f.path(hpLibB), later, later))
		changed, err := fx.Replan(context.Background(), nil, planned, nil)
		require.NoError(t, err)
		require.NotEqual(t, planned.Fingerprint, changed.Fingerprint)
		require.Contains(t, changed.Reason, "changed since the plan compared its content")
		require.Zero(t, reads.Load(), "a re-plan never re-reads a file")
	})

	t.Run("an unreadable file leaves its claimant unproven, with the reason", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, false)
		f.writeSame(t, "same", hpParent, hpLibA, hpLibB)
		fx := f.registeredFragFixer(t)
		bad := f.path(hpLibA)
		fx.hashFn = func(p string) (fragFileSig, string, error) {
			if p == bad {
				return fragFileSig{}, "", fmt.Errorf("open %s: permission denied", p)
			}
			return fragHashFile(p)
		}
		res := f.plan(t, "op-plan")
		proven := findRow(t, res, "copy:"+f.ids["parent"])
		require.ElementsMatch(t, []string{f.ids["parent"], f.ids["libB"]}, proven.BookIDs)
		r := findRow(t, res, fragRowCopyUnproven+":"+f.ids["parent"])
		require.Equal(t, fragSkipCopyUnproven, r.Skipped)
		require.ElementsMatch(t, []string{f.ids["parent"], f.ids["libA"]}, r.BookIDs)
		require.Contains(t, r.SkipReason, "content not compared")
		require.Contains(t, r.SkipReason, "permission denied")
	})

	t.Run("the iTunes Media claimant is never read; the parent's file is read once", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, false)
		f.writeSame(t, "same", hpParent, hpLibA, hpLibB, hpITM)
		fx := f.registeredFragFixer(t)
		var mu sync.Mutex
		read := map[string]int{}
		fx.hashFn = func(p string) (fragFileSig, string, error) {
			mu.Lock()
			read[p]++
			mu.Unlock()
			return fragHashFile(p)
		}
		res := f.plan(t, "op-plan")
		require.True(t, findRow(t, res, "copy:"+f.ids["parent"]).Applicable())
		mu.Lock()
		defer mu.Unlock()
		for p := range read {
			require.NotContains(t, p, "iTunes Media", "a hands-off claimant is not read")
		}
		require.Equal(t, map[string]int{f.path(hpParent): 1, f.path(hpLibA): 1, f.path(hpLibB): 1}, read)
	})

	t.Run("hashing honours cancellation", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, false)
		f.writeSame(t, "same", hpParent, hpLibA, hpLibB)
		const extra = 12
		for i := range extra {
			rel := fmt.Sprintf("lib/Many Parts copy %02d/02.mp3", i)
			p := f.file(t, rel, 2002)
			f.writeSame(t, "same", rel)
			id := f.book(t, fmt.Sprintf("x%02d", i), "02", p, nil)
			f.row(t, fmt.Sprintf("x%02d", i), id, p, "02.mp3", 2002, 590, 0)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		fx := newFragmentFixer(f.p)
		var calls atomic.Int64
		fx.hashFn = func(p string) (fragFileSig, string, error) {
			calls.Add(1)
			cancel()
			return fragHashFile(p)
		}
		_, err := fx.Plan(ctx, nil, &repairsOpReporter{id: "op-plan"})
		require.ErrorIs(t, err, context.Canceled)
		require.Positive(t, calls.Load(), "the plan reached the hashing pool")
		require.LessOrEqual(t, calls.Load(), int64(fragHashLimit), "no read starts after the cancel")
		require.Less(t, calls.Load(), int64(extra+3))
	})

	t.Run("an iTunes-tracked claimant in the library folder is compared and listed manual-only with the proof", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, false)
		linkParentToITunes(t, f)
		f.writeSame(t, "same", hpParent, hpLibA, hpLibB, hpITM)
		setBookPID(t, f, f.ids["libA"], "PIDLIBA0000000001")
		fx := f.registeredFragFixer(t)
		var mu sync.Mutex
		read := map[string]bool{}
		fx.hashFn = func(p string) (fragFileSig, string, error) {
			mu.Lock()
			read[p] = true
			mu.Unlock()
			return fragHashFile(p)
		}
		res := f.plan(t, "op-plan")
		m := findRow(t, res, "manual:"+f.ids["libA"])
		require.Equal(t, fragClassManual, m.Class)
		require.False(t, m.Applicable())
		require.Contains(t, m.Evidence[0], fragEvContentHashPrefix, "the proof is shown for the owner")
		r := findRow(t, res, "copy:"+f.ids["parent"])
		require.ElementsMatch(t, []string{f.ids["parent"], f.ids["libB"]}, r.BookIDs)
		mu.Lock()
		require.True(t, read[f.path(hpLibA)], "the library-folder claimant is read")
		require.False(t, read[f.path(hpITM)], "the iTunes Media claimant is not")
		mu.Unlock()
		// No apply path writes it: a manual row asked for by id is not applicable.
		out := f.apply(t, "op-plan", "op-apply", []string{m.RowID}, nil)
		require.Zero(t, out.Applied, "%+v", out.Rows)
		require.Equal(t, repairs.OutcomeNotApplicable, out.Rows[0].Outcome)
		require.True(t, f.live(t, "libA"))
	})

	t.Run("a claimant whose own row carries an iTunes path shows the proof on its manual-only row", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, false)
		linkParentToITunes(t, f)
		f.writeSame(t, "same", hpParent, hpLibA, hpLibB)
		f.updateRow(t, f.ids["libA"], f.rowIDs["libA"], func(r *database.BookFile) {
			r.ITunesPath = "file://localhost/W:/itunes/iTunes Media/Many Parts/02.mp3"
		})
		res := f.plan(t, "op-plan")
		m := rowWithBook(t, res.Rows, f.ids["libA"])
		require.False(t, m.Applicable(), "%s", m.RowID)
		require.Contains(t, strings.Join(m.Evidence, "\n"), fragEvContentHashPrefix, "%s: %s", m.RowID, m.SkipReason)
		out := f.apply(t, "op-plan", "op-apply", []string{m.RowID}, nil)
		require.Zero(t, out.Applied, "%+v", out.Rows)
		require.True(t, f.live(t, "libA"))
	})

	t.Run("a content-proven copy that turns iTunes after the locked re-plan is refused before any write", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, false)
		linkParentToITunes(t, f)
		f.writeSame(t, "same", hpParent, hpLibA, hpLibB)
		plan := f.plan(t, "op-plan")
		require.Contains(t, strings.Join(findRow(t, plan, "copy:"+f.ids["parent"]).Evidence, "\n"), fragEvContentHashPrefix)
		fx := newFragmentFixer(f.p)
		fx.afterLockedReplan = func() { setBookPID(t, f, f.ids["libB"], "PIDLATE000000001") }
		err := applyRowInRun(t, f, fx, context.Background(), plan, "copy:"+f.ids["parent"])
		require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
		require.True(t, f.live(t, "libA"), "refused whole: no sibling retired first")
		require.True(t, f.live(t, "libB"))
	})

	t.Run("same size, other bytes renamed over the path with the mtime set back: changed since plan", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, false)
		linkParentToITunes(t, f)
		f.writeSame(t, "same", hpParent, hpLibA, hpLibB)
		p := f.path(hpLibA)
		fi, err := os.Stat(p)
		require.NoError(t, err)
		r := findRow(t, f.plan(t, "op-plan"), "copy:"+f.ids["parent"])
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		// Other bytes, same size, the old mtime restored, renamed over: size
		// and mtime both match the plan's; the inode and ctime do not.
		tmp := p + ".tmp"
		require.NoError(t, os.WriteFile(tmp, fragFixtureBytes("swapped", 2002), 0o644))
		require.NoError(t, os.Chtimes(tmp, fi.ModTime(), fi.ModTime()))
		require.NoError(t, os.Rename(tmp, p))
		now, err := os.Stat(p)
		require.NoError(t, err)
		require.Equal(t, fi.Size(), now.Size())
		require.True(t, fi.ModTime().Equal(now.ModTime()), "the probe keeps size and mtime")
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Zero(t, out.Applied, "%+v", out.Rows)
		require.Equal(t, repairs.OutcomeChangedSincePlan, out.Rows[0].Outcome, "%+v", out.Rows)
		require.True(t, f.live(t, "libA"))
		require.True(t, f.live(t, "libB"))
	})

	for _, tc := range []struct {
		name string
		link func(oldname, newname string) error
	}{
		{"hardlink", os.Link},
		{"symlink", os.Symlink},
	} {
		t.Run("a "+tc.name+" to the parent's file is the same file, not a copy: held", func(t *testing.T) {
			t.Parallel()
			f := copyClaimantsFixture(t, false)
			f.writeSame(t, "same", hpParent, hpLibA, hpLibB)
			p := f.path(hpLibA)
			require.NoError(t, os.Remove(p))
			require.NoError(t, tc.link(f.path(hpParent), p))
			res := f.plan(t, "op-plan")
			a := findRow(t, res, fragClassHeld+":"+f.ids["libA"])
			require.Equal(t, fragSkipCopyUnproven, a.Skipped)
			require.False(t, a.Applicable())
			require.Contains(t, a.SkipReason, "path alias")
			require.Contains(t, a.Evidence[0], fragEvContentAlias)
			r := findRow(t, res, "copy:"+f.ids["parent"])
			require.ElementsMatch(t, []string{f.ids["parent"], f.ids["libB"]}, r.BookIDs, "the real copy is still proven")
		})
	}

	t.Run("files over filehash.Threshold are never read and stay unproven, naming the cap", func(t *testing.T) {
		t.Parallel()
		f := newFragFixture(t)
		big := int64(filehash.Threshold) + 1
		sparse := func(rel string) string {
			p := f.file(t, rel, 1)
			require.NoError(t, os.Truncate(p, big))
			return p
		}
		p1 := f.file(t, "lib/Big/01.mp3", 3001)
		p2 := sparse("lib/Big/02.mp3")
		parent := f.book(t, "parent", "Big", f.path("lib/Big"), nil)
		f.row(t, "p01", parent, p1, "01.mp3", 3001, 600, 1)
		f.row(t, "p02", parent, p2, "02.mp3", big, 600, 2)
		c := sparse("lib/Big copy/02.mp3")
		id := f.book(t, "copy", "02", c, nil)
		f.row(t, "copy", id, c, "02.mp3", big, 590, 0)
		var reads atomic.Int64
		f.registeredFragFixer(t).hashFn = func(p string) (fragFileSig, string, error) { reads.Add(1); return fragHashFile(p) }
		r := findRow(t, f.plan(t, "op-plan"), fragRowCopyUnproven+":"+parent)
		require.Equal(t, fragSkipCopyUnproven, r.Skipped)
		require.ElementsMatch(t, []string{parent, id}, r.BookIDs)
		require.Contains(t, r.SkipReason, "content not compared")
		require.Contains(t, r.SkipReason, "over 100 MB")
		require.Zero(t, reads.Load(), "neither file is read")
	})
}

// TestFragHashFile_RefusesOverThreshold: a file over filehash.Threshold is
// refused by the hash itself, so no sampled digest can become a proof.
func TestFragHashFile_RefusesOverThreshold(t *testing.T) {
	t.Parallel()
	p := t.TempDir() + "/big.mp3"
	require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
	require.NoError(t, os.Truncate(p, int64(filehash.Threshold)+1))
	_, sum, err := fragHashFile(p)
	require.Error(t, err)
	require.Contains(t, err.Error(), "over filehash.Threshold")
	require.Empty(t, sum)
	small := t.TempDir() + "/small.mp3"
	require.NoError(t, os.WriteFile(small, []byte("abc"), 0o644))
	sig, sum, err := fragHashFile(small)
	require.NoError(t, err)
	require.NotEmpty(t, sum)
	require.NotZero(t, sig.Ino)
	require.NotZero(t, sig.CtimeNS)
}
