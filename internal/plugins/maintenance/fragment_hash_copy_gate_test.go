// file: internal/plugins/maintenance/fragment_hash_copy_gate_test.go
// version: 1.1.0
// guid: 3e8b1c47-9a52-4d06-8f73-c5d2a9e04b16
// last-edited: 2026-10-21

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// The hash-copy gate (hashCopyGate): a file hash retires a book only when it
// can stand as proof. Every fixture here is synthetic.

// hcOnlyRow is the one hash-copy class row of res.
func hcOnlyRow(t *testing.T, res *repairs.PlanResult) repairs.Row {
	t.Helper()
	var out []repairs.Row
	for _, r := range res.Rows {
		if r.Class == fragClassHashCopy {
			out = append(out, r)
		}
	}
	require.Len(t, out, 1, "%+v", res.ByClass)
	return out[0]
}

// requireHeld asserts r is a never-applied hold of kind, that applying it
// writes nothing, and that the child is still live.
func requireHeld(t *testing.T, f *fragFixture, r repairs.Row, kind, child string) {
	t.Helper()
	require.Equal(t, kind, r.Skipped, "%s", r.SkipReason)
	require.False(t, r.Applicable())
	require.Contains(t, r.BookIDs, child)
	require.NotEmpty(t, r.SkipReason)
	out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
	require.Zero(t, out.Applied, "%+v", out.Rows)
	require.True(t, f.live(t, "c3"), "a held row never retires the child")
}

func hcPlanScoped(t *testing.T, f *fragFixture, opID string, bookIDs []string) *repairs.PlanResult {
	t.Helper()
	raw, err := json.Marshal(fragParams{BookIDs: bookIDs})
	require.NoError(t, err)
	params, err := json.Marshal(repairs.PlanParams{FixerID: fragFixerID, Params: raw})
	require.NoError(t, err)
	rep := &repairsOpReporter{id: opID}
	require.NoError(t, f.p.runRepairsPlan(context.Background(), params, rep))
	res, ok := rep.result.(*repairs.PlanResult)
	require.True(t, ok)
	data, err := json.Marshal(res)
	require.NoError(t, err)
	s := string(data)
	f.ops.mu.Lock()
	f.ops.rows[opID] = &database.OperationV2Row{ID: opID, DefID: repairs.PlanOpID, Status: "completed", ResultData: &s}
	f.ops.mu.Unlock()
	return res
}

// B2: a hash shared by every empty file, a size that differs between the two
// files, or a short or boilerplate file is shared between different works, so
// it proves nothing.
func TestFragmentFixer_HashCopyGate_DegenerateHashesAreHeld(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(t *testing.T, f *fragFixture)
		title string
		want  string
	}{
		{name: "the empty-file digest on both rows", want: fragSkipDegenerateHash, setup: func(t *testing.T, f *fragFixture) {
			f.hcSetHash(t, "parent", "p03", fragEmptySHA256)
			f.hcSetHash(t, "c3", "c3", fragEmptySHA256)
		}},
		{name: "zero-length files", want: fragSkipDegenerateHash, setup: func(t *testing.T, f *fragFixture) {
			require.NoError(t, os.WriteFile(f.path("lib/Alpha Omnibus/track-03.mp3"), nil, 0o644))
			require.NoError(t, os.WriteFile(f.path("lib/Stray c3/stray-c3.mp3"), nil, 0o644))
		}},
		{name: "files of different sizes", want: fragSkipDegenerateHash, setup: func(t *testing.T, f *fragFixture) {
			require.NoError(t, os.WriteFile(f.path("lib/Stray c3/stray-c3.mp3"), fragFixtureBytes("longer", 3000), 0o644))
		}},
		{name: "a known duration under a minute", want: fragSkipShortOrBoilerplate, setup: func(t *testing.T, f *fragFixture) {
			f.updateRow(t, f.ids["c3"], f.rowIDs["c3"], func(r *database.BookFile) { r.Duration = 30 })
		}},
		{name: "a boilerplate title", title: "Opening Credits", want: fragSkipShortOrBoilerplate, setup: func(t *testing.T, f *fragFixture) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := hcSeed(t)
			title := tc.title
			if title == "" {
				title = "Stray Recording"
			}
			f.hcChild(t, "c3", title, "hash-p03")
			tc.setup(t, f)
			res := f.plan(t, "op-plan")
			require.Zero(t, res.Applicable, "%+v", res.ByClass)
			r := hcOnlyRow(t, res)
			require.Equal(t, hcRowPrefix(tc.want)+f.ids["c3"], r.RowID)
			require.NotContains(t, r.RowID, "held-held", "the held_ of the kind is not repeated")
			require.NotContains(t, r.RowID, "held-skipped")
			wantLiteral := map[string]string{
				fragSkipDegenerateHash:     "held-degenerate-hash:",
				fragSkipShortOrBoilerplate: "held-short-or-boilerplate:",
			}[tc.want]
			require.Equal(t, wantLiteral+f.ids["c3"], r.RowID, "spelled out, not derived from the kind")
			requireHeld(t, f, r, tc.want, f.ids["c3"])
		})
	}
}

// hcRowPrefix is the row id prefix of a held kind: the kind's own held_ or
// skipped_ is not repeated ("held-file-absent:<child>", not
// "held-held-file-absent:<child>").
func hcRowPrefix(skip string) string {
	skip = strings.TrimPrefix(strings.TrimPrefix(skip, "held_"), "skipped_")
	return "held-" + strings.ReplaceAll(skip, "_", "-") + ":"
}

// An unknown duration (the real children record none) is not "short": the
// rule stays applicable.
func TestFragmentFixer_HashCopyGate_UnknownDurationIsNotShort(t *testing.T) {
	t.Parallel()
	f := hcSeed(t)
	child := f.hcChild(t, "c3", "Stray Recording", "hash-p03")
	f.updateRow(t, child, f.rowIDs["c3"], func(r *database.BookFile) { r.Duration = 0 })
	r := hcOnlyRow(t, f.plan(t, "op-plan"))
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, 1, f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil).Applied)
}

// S1: a child is retired only into a parent track that is really on disk, and
// only when its own file is too.
func TestFragmentFixer_HashCopyGate_AbsentFilesAreHeld(t *testing.T) {
	t.Parallel()
	for _, side := range []string{"parent", "child"} {
		t.Run(side+" file absent at plan", func(t *testing.T) {
			t.Parallel()
			f := hcSeed(t)
			f.hcChild(t, "c3", "Stray Recording", "hash-p03")
			if side == "parent" {
				require.NoError(t, os.Remove(f.path("lib/Alpha Omnibus/track-03.mp3")))
			} else {
				require.NoError(t, os.Remove(f.path("lib/Stray c3/stray-c3.mp3")))
			}
			res := f.plan(t, "op-plan")
			require.Zero(t, res.Applicable)
			r := hcOnlyRow(t, res)
			require.Equal(t, fragClassHashCopy, r.Class, "held on the hash-copy class, not a ghost or moved row")
			requireHeld(t, f, r, fragSkipFileAbsent, f.ids["c3"])
		})
	}

	t.Run("parent file removed between plan and apply", func(t *testing.T) {
		t.Parallel()
		f := hcSeed(t)
		child := f.hcChild(t, "c3", "Stray Recording", "hash-p03")
		r := hcOnlyRow(t, f.plan(t, "op-plan"))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, hcRowID(f.ids["parent"], child), r.RowID)
		require.NoError(t, os.Remove(f.path("lib/Alpha Omnibus/track-03.mp3")))
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Zero(t, out.Applied, "%+v", out.Rows)
		require.True(t, f.live(t, "c3"), "the only copy of the track stays live")
	})
}

// B1: over the sampling threshold the stored hash covers head, tail and size
// only, so the rule proves the pair by reading both files whole.
func TestFragmentFixer_HashCopyGate_SampledHashNeedsAWholeFileProof(t *testing.T) {
	t.Parallel()

	// seed: the parent's third track and the child are 1003 bytes, over a
	// 1000 byte threshold; copyBytes makes them byte-identical.
	seed := func(t *testing.T, copyBytes bool) (*fragFixture, *atomic.Int32, string) {
		t.Helper()
		f := hcSeed(t)
		child := f.hcChild(t, "c3", "Stray Recording", "hash-p03")
		fx := f.registeredFragFixer(t)
		fx.partialHashAbove = 1000
		reads := &atomic.Int32{}
		fx.wholeHashFn = func(ctx context.Context, p string) (fragFileSig, string, error) {
			reads.Add(1)
			return fragWholeHashFile(ctx, p)
		}
		if copyBytes {
			b, err := os.ReadFile(f.path("lib/Alpha Omnibus/track-03.mp3"))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(f.path("lib/Stray c3/stray-c3.mp3"), b, 0o644))
		}
		return f, reads, child
	}

	t.Run("identical bytes: proven, applied from the stored proof, undoable", func(t *testing.T) {
		t.Parallel()
		f, reads, _ := seed(t, true)
		res := f.plan(t, "op-plan")
		r := hcOnlyRow(t, res)
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, int32(2), reads.Load(), "both files read whole, once each")
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
		require.Equal(t, int32(2), reads.Load(), "the apply re-stats the proven files, it does not read them again")
		require.False(t, f.live(t, "c3"))
		rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
		require.NoError(t, err)
		require.Zero(t, rr.Failed, "%+v", rr)
		require.True(t, f.live(t, "c3"))
	})

	t.Run("same size, different bytes: held as a mismatch", func(t *testing.T) {
		t.Parallel()
		f, _, child := seed(t, false)
		res := f.plan(t, "op-plan")
		require.Zero(t, res.Applicable)
		requireHeld(t, f, hcOnlyRow(t, res), fragSkipPartialMismatch, child)
	})

	t.Run("a failed whole-file read: held unproven", func(t *testing.T) {
		t.Parallel()
		f, _, child := seed(t, true)
		f.registeredFragFixer(t).wholeHashFn = func(context.Context, string) (fragFileSig, string, error) {
			return fragFileSig{}, "", errors.New("read error")
		}
		res := f.plan(t, "op-plan")
		require.Zero(t, res.Applicable)
		requireHeld(t, f, hcOnlyRow(t, res), fragSkipPartialUnproven, child)
	})

	t.Run("child rewritten after the plan: the apply refuses", func(t *testing.T) {
		t.Parallel()
		f, _, _ := seed(t, true)
		r := hcOnlyRow(t, f.plan(t, "op-plan"))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.NoError(t, os.WriteFile(f.path("lib/Stray c3/stray-c3.mp3"), fragFixtureBytes("rewritten", 1003), 0o644))
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Zero(t, out.Applied, "%+v", out.Rows)
		require.True(t, f.live(t, "c3"))
	})

	t.Run("under the threshold nothing is read whole", func(t *testing.T) {
		t.Parallel()
		f, reads, _ := seed(t, false)
		f.registeredFragFixer(t).partialHashAbove = 0 // the real threshold, far above 1003 bytes
		r := hcOnlyRow(t, f.plan(t, "op-plan"))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Zero(t, reads.Load())
	})
}

// S2: an iTunes copy of the parent that the iTunes-parent rule sets aside is
// not a "second parent" at apply either.
func TestFragmentFixer_HashCopyGate_ITunesCopyOfTheParentIsNotASecondParent(t *testing.T) {
	setup := func(t *testing.T) (*dcFixture, string, string, string) {
		d := newDCFixture(t)
		parent := d.copyBook(t, "P", "Dune", "lib/Dune", dcRow{track: 1, dur: 600, hash: "h1"}, dcRow{track: 2, dur: 600, hash: "h2"})
		it := d.copyBook(t, "I", "Dune", "books/itunes/Dune", dcRow{track: 1, dur: 600, hash: "h1"}, dcRow{track: 2, dur: 600, hash: "h2"})
		// Parent track 02 is 101 bytes; the child is a plain-named copy of it.
		fp := d.file(t, "lib/Stray/stray.mp3", 101)
		c := d.book(t, "c3", "Stray Recording", fp, nil)
		require.NoError(t, d.s.CreateBookFile(&database.BookFile{BookID: c, FilePath: fp, OriginalFilename: "stray.mp3", FileSize: 101, Duration: 600, FileHash: "h2"}))
		d.organized(t, c)
		return d, parent, it, c
	}

	t.Run("the plan is applicable and the apply retires into the non-iTunes parent", func(t *testing.T) {
		d, parent, it, c := setup(t)
		beforeI, err := d.s.GetBookByID(it)
		require.NoError(t, err)
		res := d.planFor(t, fragFixerID, "op-plan", nil)
		r := findRow(t, res, hcRowID(parent, c))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		out := d.applyFor(t, fragFixerID, "op-plan", "op-apply", []string{r.RowID})
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
		require.False(t, d.live(t, "c3"))
		b, err := d.s.GetBookByID(c)
		require.NoError(t, err)
		require.NotNil(t, b.MergedIntoBookID)
		require.Equal(t, parent, *b.MergedIntoBookID)
		afterI, err := d.s.GetBookByID(it)
		require.NoError(t, err)
		require.Equal(t, beforeI.UpdatedAt, afterI.UpdatedAt, "the iTunes copy is never written")
	})

	t.Run("a second non-iTunes parent that appears after the plan still refuses", func(t *testing.T) {
		d, parent, _, c := setup(t)
		res := d.planFor(t, fragFixerID, "op-plan", nil)
		r := findRow(t, res, hcRowID(parent, c))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		d.copyBook(t, "P2", "Dune", "lib/Dune again", dcRow{track: 1, dur: 600, hash: "h1"}, dcRow{track: 2, dur: 600, hash: "h2"})
		out := d.applyFor(t, fragFixerID, "op-plan", "op-apply", []string{r.RowID})
		require.Zero(t, out.Applied, "%+v", out.Rows)
		require.True(t, d.live(t, "c3"))
	})
}

// S4: a child with a missing hash is held as skipped_no_hash although its
// recorded size is 0 (the real children record none): the size gate reads the
// files.
func TestFragmentFixer_HashCopyGate_NoHashIsReachableWithoutRecordedSizes(t *testing.T) {
	t.Parallel()
	f := hcSeed(t)
	np := f.file(t, "lib/Alpha Omnibus/opening-theme.mp3", 1003)
	f.updateRow(t, f.ids["parent"], f.rowIDs["p03"], func(r *database.BookFile) {
		r.FilePath, r.OriginalFilename, r.FileSize, r.FileHash = np, "opening-theme.mp3", 0, ""
	})
	p := f.file(t, "lib/Stray/opening-theme.mp3", 1003)
	id := f.book(t, "c3", "Stray Recording", p, nil)
	f.row(t, "c3", id, p, "opening-theme.mp3", 0, 600, 0)
	f.hcSetHash(t, "c3", "c3", "hash-p03")

	res := f.plan(t, "op-plan")
	require.Equal(t, 1, res.SkippedByKind[fragSkipNoHash], "%+v", res.SkippedByKind)
}

// N1: a book_ids scope selects the CHILDREN to consider; the parent index
// stays the whole library's, so the scoped plan decides as the full one.
func TestFragmentFixer_HashCopyGate_BookIDsScopeOnlyTheChildren(t *testing.T) {
	t.Parallel()
	f := hcSeed(t)
	child := f.hcChild(t, "c3", "Stray Recording", "hash-p03")
	parent := f.ids["parent"]

	res := hcPlanScoped(t, f, "op-plan", []string{child})
	r := findRow(t, res, hcRowID(parent, child))
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)

	// A second parent outside the scope is still seen: held, not retired.
	other := f.book(t, "other", "Beta Omnibus", f.path("lib/Beta Omnibus"), nil)
	for i := 1; i <= 2; i++ {
		role := "o0" + string(rune('0'+i))
		p := f.file(t, "lib/Beta Omnibus/track-0"+string(rune('0'+i))+".mp3", 2000+i)
		f.row(t, role, other, p, "track-0"+string(rune('0'+i))+".mp3", int64(2000+i), 600, i)
	}
	f.hcSetHash(t, "other", "o01", "hash-p03")
	res = hcPlanScoped(t, f, "op-plan-2", []string{child})
	require.Zero(t, res.Applicable, "%+v", res.ByClass)
	require.Equal(t, 1, res.SkippedByKind[fragSkipAmbiguousParent], "%+v", res.SkippedByKind)
	requireHeld(t, f, hcOnlyRow(t, res), fragSkipAmbiguousParent, child)
}
