// file: internal/plugins/maintenance/duplicate_copies_review_test.go
// version: 1.0.0
// guid: 6e2d9c41-3b7a-4f05-8c1e-a94d2f7b3e58
// last-edited: 2026-10-01

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// The #3645 adversarial review's proving cases, each turned into the
// behaviour it proved missing.

func (d *dcFixture) setRuntime(t *testing.T, id string, min int) {
	t.Helper()
	_, err := d.s.ModifyBook(id, func(b *database.Book) error { b.AudibleRuntimeMin = &min; return nil })
	require.NoError(t, err)
}

func (d *dcFixture) setAuthor(t *testing.T, id, name string) {
	t.Helper()
	a, err := d.s.CreateAuthor(name)
	require.NoError(t, err)
	_, err = d.s.ModifyBook(id, func(b *database.Book) error { b.AuthorID = &a.ID; return nil })
	require.NoError(t, err)
}

func (d *dcFixture) imported(t *testing.T, id string) {
	t.Helper()
	st := "imported"
	_, err := d.s.ModifyBook(id, func(b *database.Book) error { b.LibraryState = &st; return nil })
	require.NoError(t, err)
}

// ---- B1: the fragment iTunes-parent rule needs proven copies -------------

// itunesRule seeds a non-iTunes parent P and an iTunes book I, and a
// fragment matching both on hash; mut shapes them. It returns the plan and
// the ids.
func itunesRule(t *testing.T, pTitle string, pRows []dcRow, iTitle string, iRows []dcRow, fragTitle string, fragDur int, fragHash string,
	mut func(d *dcFixture, p, i string)) (res *repairs.PlanResult, p, frag string) {
	t.Helper()
	d := newDCFixture(t)
	p = d.copyBook(t, "P", pTitle, "lib/P", pRows...)
	i := d.copyBook(t, "I", iTitle, "books/itunes/I", iRows...)
	if mut != nil {
		mut(d, p, i)
	}
	fp := d.file(t, "lib/frag/02.mp3", 777)
	frag = d.book(t, "frag", fragTitle, fp, nil)
	require.NoError(t, d.s.CreateBookFile(&database.BookFile{BookID: frag, FilePath: fp, OriginalFilename: "02.mp3", FileSize: 777,
		Duration: fragDur, FileHash: fragHash}))
	return d.planFor(t, fragFixerID, "op-plan", nil), p, frag
}

func requireAmbiguous(t *testing.T, res *repairs.PlanResult, frag string) {
	t.Helper()
	for _, r := range res.Rows {
		if contains(r.BookIDs, frag) {
			require.False(t, r.Applicable(), "%s: %s", r.RowID, r.Reason)
			require.True(t, strings.HasPrefix(r.RowID, "ambiguous:"), "the fragment stays ambiguous, got %s", r.RowID)
			return
		}
	}
	t.Fatalf("no row names fragment %s", frag)
}

func TestFragmentFixer_ITunesParentRuleNeedsAProvenCopy(t *testing.T) {
	dune := []dcRow{{track: 1, dur: 600, hash: "h1"}, {track: 2, dur: 600, hash: "h2"}}
	t.Run("a different work sharing one file", func(t *testing.T) {
		// The review's case: Dune and an iTunes Emma share a 90 s intro.
		res, _, frag := itunesRule(t, "Dune", []dcRow{{track: 1, dur: 600, hash: "h1"}, {track: 2, dur: 90, hash: "intro"}},
			"Emma", []dcRow{{track: 1, dur: 600, hash: "e1"}, {track: 2, dur: 90, hash: "intro"}}, "Emma 02", 90, "intro", nil)
		requireAmbiguous(t, res, frag)
	})
	t.Run("authors differ", func(t *testing.T) {
		res, _, frag := itunesRule(t, "Dune", dune, "Dune", dune, "02", 600, "h2", func(d *dcFixture, p, i string) {
			d.setAuthor(t, p, "Frank Herbert")
			d.setAuthor(t, i, "Brian Herbert")
		})
		requireAmbiguous(t, res, frag)
	})
	t.Run("under 90% of the smaller copy matched", func(t *testing.T) {
		res, _, frag := itunesRule(t, "Dune", dune, "Dune", []dcRow{{track: 1, dur: 600, hash: "x1"}, {track: 2, dur: 600, hash: "h2"}},
			"02", 600, "h2", nil)
		requireAmbiguous(t, res, frag)
	})
	t.Run("a fragment under 60 s", func(t *testing.T) {
		res, _, frag := itunesRule(t, "Dune", dune, "Dune", dune, "02", 59, "h2", nil)
		requireAmbiguous(t, res, frag)
	})
	t.Run("an intro/credits fragment", func(t *testing.T) {
		res, _, frag := itunesRule(t, "Dune", dune, "Dune", dune, "Opening Credits", 600, "h2", nil)
		requireAmbiguous(t, res, frag)
	})
	t.Run("a proven copy still unlocks", func(t *testing.T) {
		res, p, frag := itunesRule(t, "Dune", dune, "44 - Dune", dune, "02", 600, "h2", func(d *dcFixture, p, i string) {
			d.setAuthor(t, p, "Frank Herbert")
			d.setAuthor(t, i, "Frank Herbert")
		})
		r := findRow(t, res, "copy:"+p)
		require.True(t, r.Applicable(), r.SkipReason)
		require.Contains(t, r.BookIDs, frag)
	})
	t.Run("an unreadable verdict store turns the rule off", func(t *testing.T) {
		res, _, frag := itunesRule(t, "Dune", dune, "Dune", dune, "02", 600, "h2", func(d *dcFixture, _, _ string) {
			d.labels.err = errors.New("corrupt label row")
		})
		requireAmbiguous(t, res, frag)
	})
}

// ---- B2 / S5: unknown durations and duplicated junk -------------------------

// TestDuplicateCopies_UnknownDurationsNeverProveOrFold: a copy with rows of
// unknown length (the review's 10 files folded into a 2-file book) is never
// a proven copy.
func TestDuplicateCopies_UnknownDurationsNeverProveOrFold(t *testing.T) {
	d := newDCFixture(t)
	s := d.copyBook(t, "S", "Emma", "lib/Emma", dcRow{track: 1, dur: 600, hash: "e1"}, dcRow{track: 2, dur: 600, hash: "e2"})
	rows := []dcRow{{track: 1, dur: 600, hash: "e1"}, {track: 2, dur: 600, hash: "e2"}, {track: 3, dur: 1, hash: "o3"}}
	for i := 4; i <= 12; i++ {
		rows = append(rows, dcRow{track: i, dur: 0, hash: fmt.Sprintf("o%d", i)})
	}
	l := d.copyBook(t, "L", "Emma", "lib/Emma other", rows...)
	d.imported(t, l)
	r := rowOf(t, d.planFor(t, dcFixerID, "op-plan", nil), s)
	require.False(t, r.Applicable())
	require.Equal(t, dcSkipUnproven, r.Skipped, r.SkipReason)
	require.ElementsMatch(t, []string{s, l}, r.BookIDs)
}

// TestDuplicateCopies_ZeroDurationRowIsNeverFolded reaches the fold gate
// directly: no plan can, since a 0 s row leaves the pair unproven first.
func TestDuplicateCopies_ZeroDurationRowIsNeverFolded(t *testing.T) {
	d := newDCFixture(t)
	s := d.copyBook(t, "S", "Emma", "lib/Emma", dcRow{track: 1, dur: 600, hash: "e1"}, dcRow{track: 2, dur: 600, hash: "e2"},
		dcRow{track: 3, dur: 600, hash: "e3"}, dcRow{track: 4, dur: 600, hash: "e4"})
	l := d.copyBook(t, "L", "Emma", "lib/Emma other", dcRow{track: 1, dur: 600, hash: "e1"}, dcRow{track: 2, dur: 600, hash: "e2"},
		dcRow{track: 3, dur: 600, hash: "e3"}, dcRow{track: 4, dur: 600, hash: "e4"}, dcRow{track: 5, dur: 600, hash: "e5"})
	d.imported(t, l)
	f := newDuplicateCopiesFixer(d.p)
	lib, err := f.fb.loadFull(d.s)
	require.NoError(t, err)
	run := &dcRun{lib: lib, books: map[string]*dcBook{}, res: repairs.NewPathResolver(), series: map[int]string{}}
	for _, id := range []string{s, l} {
		require.NoError(t, f.detail(d.s, run, run.book(id)))
	}
	edges := map[[2]string]dcVerdict{dcPairKey(s, l): {Kind: dcEdgeProven, Why: "forced"}}
	ok := f.mergeRow(run, edges, []string{s, l}, nil)
	require.True(t, ok.Applicable(), "%s: %s", ok.Skipped, ok.SkipReason)
	for i := range run.books[l].Rows {
		if run.books[l].Rows[i].TrackNumber == 5 {
			run.books[l].Rows[i].Duration = 0
		}
	}
	r := f.mergeRow(run, edges, []string{s, l}, nil)
	require.Equal(t, dcSkipFoldCap, r.Skipped, r.SkipReason)
	require.Contains(t, r.SkipReason, "no known duration")
}

// TestDuplicateCopies_DuplicatedJunkCountsOnce: the review's case, three
// copies of one junk file outweighing the real audio.
func TestDuplicateCopies_DuplicatedJunkCountsOnce(t *testing.T) {
	row := func(id string, dur int, hash string) database.BookFileCore {
		return database.BookFileCore{ID: id, Duration: dur, FileHash: hash, FilePath: "/x/" + id + ".mp3"}
	}
	a := &dcBook{Core: database.BookCore{ID: "a"}, Title: "dune", Rows: []database.BookFileCore{
		row("a1", 3600, "j"), row("a2", 3600, "j"), row("a3", 3600, "j"), row("a4", 1200, "other")}}
	b := &dcBook{Core: database.BookCore{ID: "b"}, Title: "dune", Rows: []database.BookFileCore{
		row("b1", 3600, "j"), row("b2", 30000, "real")}}
	cov, ok := dcCoverageOf(a, b)
	require.True(t, ok)
	require.InDelta(t, 0.75, cov, 0.001, "3600 s of junk once over 4800 s of distinct audio")
	require.Equal(t, dcEdgeUnproven, dcJudge(a, b, nil).Kind)
}

// TestDuplicateCopies_FoldCapCountsMatchedAudioOnce: four survivor rows of
// one audio match once, so a 600 s fold against 1800 s of distinct matched
// audio is over the cap.
func TestDuplicateCopies_FoldCapCountsMatchedAudioOnce(t *testing.T) {
	d := newDCFixture(t)
	rows := []dcRow{{track: 1, dur: 600, hash: "h1"}, {track: 2, dur: 600, hash: "h2"}}
	for i := 3; i <= 6; i++ {
		rows = append(rows, dcRow{track: i, dur: 600, hash: "j"})
	}
	s := d.copyBook(t, "S", "Emma", "lib/Emma", rows...)
	l := d.copyBook(t, "L", "Emma", "lib/Emma other", append(append([]dcRow(nil), rows...), dcRow{track: 7, dur: 600, hash: "h7"})...)
	d.imported(t, l)
	r := rowOf(t, d.planFor(t, dcFixerID, "op-plan", nil), s)
	require.Equal(t, dcSkipFoldCap, r.Skipped, r.SkipReason)
}

// ---- B3: every owner rejection is honoured ---------------------------------

func TestDuplicateCopies_OwnerRejections(t *testing.T) {
	pair := func(t *testing.T) (*dcFixture, string, string) {
		d := newDCFixture(t)
		a := d.copyBook(t, "a", "Ubik", "lib/Ubik", dcRow{track: 1, dur: 600, hash: "u1"}, dcRow{track: 2, dur: 600, hash: "u2"})
		b := d.copyBook(t, "b", "Ubik", "lib/Ubik 2", dcRow{track: 1, dur: 600, hash: "u1"}, dcRow{track: 2, dur: 600, hash: "u2"})
		return d, a, b
	}
	for _, status := range []string{"dismissed", "merged"} {
		t.Run("candidate "+status, func(t *testing.T) {
			d, a, b := pair(t)
			d.labels.cands = append(d.labels.cands, database.DedupCandidate{ID: 9, EntityType: "book", EntityAID: b, EntityBID: a, Status: status})
			r := rowOf(t, d.planFor(t, dcFixerID, "op-plan", nil), a)
			require.Equal(t, dcSkipNotDup, r.Skipped, r.SkipReason)
			require.Contains(t, r.SkipReason, status)
		})
	}
	t.Run("a pending candidate is no verdict", func(t *testing.T) {
		d, a, b := pair(t)
		d.labels.cands = append(d.labels.cands, database.DedupCandidate{ID: 9, EntityType: "book", EntityAID: a, EntityBID: b, Status: "pending"})
		r := rowOf(t, d.planFor(t, dcFixerID, "op-plan", nil), a)
		require.True(t, r.Applicable(), r.SkipReason)
	})
	t.Run("dismissed duplicate group", func(t *testing.T) {
		d, a, b := pair(t)
		keys, err := json.Marshal([]string{"x+" + b + "+" + a})
		require.NoError(t, err)
		require.NoError(t, d.s.SetUserPreference(dcDismissedGroupsPref, string(keys)))
		r := rowOf(t, d.planFor(t, dcFixerID, "op-plan", nil), a)
		require.Equal(t, dcSkipNotDup, r.Skipped, r.SkipReason)
		require.Contains(t, r.SkipReason, "dismissed duplicate group")
	})
	t.Run("unreadable dismissed groups fail the plan", func(t *testing.T) {
		d, _, _ := pair(t)
		require.NoError(t, d.s.SetUserPreference(dcDismissedGroupsPref, "{not a list"))
		_, err := newDuplicateCopiesFixer(d.p).Plan(context.Background(), nil, nil)
		require.ErrorContains(t, err, "dismissed duplicate groups")
	})
	t.Run("an unreadable verdict store fails the plan", func(t *testing.T) {
		d, _, _ := pair(t)
		d.labels.err = errors.New("corrupt row")
		_, err := newDuplicateCopiesFixer(d.p).Plan(context.Background(), nil, nil)
		require.ErrorContains(t, err, "corrupt row")
	})
}

// ---- B4: box set vs book 1 (owner: outside-match + runtime) ---------------

func ubik(t *testing.T) (*dcFixture, string, string) {
	d := newDCFixture(t)
	a := d.copyBook(t, "A", "Ubik", "lib/Ubik A", dcRow{track: 1, dur: 600, hash: "u1"}, dcRow{track: 2, dur: 600, hash: "u2"})
	b := d.copyBook(t, "B", "Ubik", "lib/Ubik Box", dcRow{track: 1, dur: 600, hash: "u1"}, dcRow{track: 2, dur: 600, hash: "u2"},
		dcRow{track: 3, dur: 600, hash: "x3"}, dcRow{track: 4, dur: 600, hash: "x4"}, dcRow{track: 5, dur: 600, hash: "x5"},
		dcRow{track: 6, dur: 600, hash: "x6"}, dcRow{track: 7, dur: 600, hash: "x7"}, dcRow{track: 8, dur: 600, hash: "x8"})
	return d, a, b
}

func TestDuplicateCopies_BoxSet(t *testing.T) {
	t.Run("runtime: the set is 4x book 1's Audible runtime", func(t *testing.T) {
		d, a, _ := ubik(t)
		d.setRuntime(t, a, 20)
		r := rowOf(t, d.planFor(t, dcFixerID, "op-plan", nil), a)
		require.Equal(t, dcSkipBoxSet, r.Skipped, r.SkipReason)
		require.Contains(t, r.SkipReason, "Audible runtime")
	})
	t.Run("outside match: book 2 is its own book", func(t *testing.T) {
		d, a, _ := ubik(t)
		c := d.copyBook(t, "C", "Ubik 2", "lib/Ubik 2", dcRow{track: 1, dur: 600, hash: "x3"}, dcRow{track: 2, dur: 600, hash: "x4"})
		r := rowOf(t, d.planFor(t, dcFixerID, "op-plan", nil), a)
		require.Equal(t, dcSkipBoxSet, r.Skipped, r.SkipReason)
		require.Contains(t, r.SkipReason, c)
	})
	t.Run("neither: applicable, and says no runtime is on file", func(t *testing.T) {
		d, a, b := ubik(t)
		r := rowOf(t, d.planFor(t, dcFixerID, "op-plan", nil), a)
		require.True(t, r.Applicable(), r.SkipReason)
		require.Equal(t, b, r.Proposed["survivor"])
		require.Contains(t, r.Evidence, "box-set check: no runtime on file")
	})
}

// TestDuplicateCopies_BoxSetOwnerCasesStillMerge: the owner's two short
// copies (HWFWM 3, 3h40m in a 24h39m copy, Audible ~24h; Cobra, 55m in 10h)
// pass the runtime leg. A live stray chapter of the full copy holds HWFWM
// back until fragment consolidation retires it.
func TestDuplicateCopies_BoxSetOwnerCasesStillMerge(t *testing.T) {
	hwfwm := func(t *testing.T) (*dcFixture, string, string) {
		d := newDCFixture(t)
		short := []dcRow{{track: 1, dur: 3300, hash: "w1"}, {track: 2, dur: 3300, hash: "w2"}, {track: 3, dur: 3300, hash: "w3"}, {track: 4, dur: 3300, hash: "w4"}}
		full := append(append([]dcRow(nil), short...), dcRow{track: 5, dur: 37740, hash: "w5"}, dcRow{track: 6, dur: 37800, hash: "w6"})
		s := d.copyBook(t, "short", "He Who Fights with Monsters 3", "lib/HWFWM 3 short", short...)
		f := d.copyBook(t, "full", "He Who Fights with Monsters 3", "lib/HWFWM 3", full...)
		d.setRuntime(t, s, 1440)
		return d, s, f
	}
	t.Run("HWFWM 3", func(t *testing.T) {
		d, s, f := hwfwm(t)
		r := rowOf(t, d.planFor(t, dcFixerID, "op-plan", nil), s)
		require.True(t, r.Applicable(), r.SkipReason)
		require.Equal(t, f, r.Proposed["survivor"])
	})
	t.Run("HWFWM 3 with a live stray chapter", func(t *testing.T) {
		d, s, _ := hwfwm(t)
		fp := d.file(t, "lib/strays/Chapter 40.mp3", 900)
		stray := d.book(t, "stray", "Chapter 40", fp, nil)
		require.NoError(t, d.s.CreateBookFile(&database.BookFile{BookID: stray, FilePath: fp, FileSize: 900, Duration: 37740, FileHash: "w5"}))
		r := rowOf(t, d.planFor(t, dcFixerID, "op-plan", nil), s)
		require.Equal(t, dcSkipBoxSet, r.Skipped, r.SkipReason)
		require.Contains(t, r.SkipReason, "fragment consolidation")
	})
	t.Run("Cobra", func(t *testing.T) {
		d := newDCFixture(t)
		// Two files: a one-file short copy is a fragment, fragment
		// consolidation's case, and never seeds a copy pair.
		s := d.copyBook(t, "short", "Cobra", "lib/Cobra short", dcRow{track: 1, dur: 1650, hash: "c1"}, dcRow{track: 2, dur: 1650, hash: "c2"})
		f := d.copyBook(t, "full", "Cobra", "lib/Cobra", dcRow{track: 1, dur: 1650, hash: "c1"}, dcRow{track: 2, dur: 1650, hash: "c2"},
			dcRow{track: 3, dur: 32700, hash: "c3"})
		d.setRuntime(t, s, 600)
		r := rowOf(t, d.planFor(t, dcFixerID, "op-plan", nil), s)
		require.True(t, r.Applicable(), r.SkipReason)
		require.Equal(t, f, r.Proposed["survivor"])
	})
}

// ---- S6: credits make a book manual-only --------------------------------------

func TestDuplicateCopies_BigFinishPublisherIsManualOnly(t *testing.T) {
	d := newDCFixture(t)
	a := d.copyBook(t, "a", "Michael Fenton Stevens/The Ultimate Foe", "lib/MFS/Ultimate Foe",
		dcRow{track: 1, dur: 600, hash: "m1"}, dcRow{track: 2, dur: 600, hash: "m2"})
	d.copyBook(t, "b", "Michael Fenton Stevens/The Ultimate Foe", "lib/MFS/Ultimate Foe 2",
		dcRow{track: 1, dur: 600, hash: "m1"}, dcRow{track: 2, dur: 600, hash: "m2"})
	pub := "Big Finish Productions"
	_, err := d.s.ModifyBook(a, func(b *database.Book) error { b.Publisher = &pub; return nil })
	require.NoError(t, err)
	r := rowOf(t, d.planFor(t, dcFixerID, "op-plan", nil), a)
	require.Equal(t, dcClassManual, r.Class)
	require.Equal(t, repairs.SkipOwnerManual, r.Skipped, r.SkipReason)
	require.Contains(t, r.SkipReason, "publisher")
}

// ---- NITs ---------------------------------------------------------------------

func TestDuplicateCopies_DanglingAuthorIsUnknownNotAWildcard(t *testing.T) {
	five := 5
	require.Equal(t, "?author#5", dcAuthorKey(map[int]string{}, &five))
	require.Equal(t, "", dcAuthorKey(map[int]string{5: "Unknown Author"}, &five))
	row := database.BookFileCore{ID: "r", Duration: 600, FileHash: "h", FilePath: "/x/r.mp3"}
	a := &dcBook{Core: database.BookCore{ID: "a"}, Title: "dune", Author: dcAuthorKey(map[int]string{}, &five), Rows: []database.BookFileCore{row}}
	b := &dcBook{Core: database.BookCore{ID: "b"}, Title: "dune", Author: "frankherbert", Rows: []database.BookFileCore{row}}
	require.Empty(t, dcJudge(a, b, nil).Kind, "a dangling author never matches a known one")
	b.Author = ""
	require.Equal(t, dcEdgeProven, dcJudge(a, b, nil).Kind, "no author on the other side still matches")
}

func TestRetireInto_LeavesTombstonedExternalIDsBehind(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	require.NoError(t, d.s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: "DEADPID", BookID: l, Tombstoned: true}))
	require.NoError(t, d.s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "audible", ExternalID: "B0LIVE", BookID: l}))
	res := d.planFor(t, dcFixerID, "op-plan", nil)
	out := d.applyFor(t, dcFixerID, "op-plan", "op-apply", []string{rowOf(t, res, s).RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	got, err := d.s.GetExternalIDsForBook(s)
	require.NoError(t, err)
	var ids []string
	for _, e := range got {
		ids = append(ids, e.ExternalID)
	}
	require.Contains(t, ids, "B0LIVE")
	require.NotContains(t, ids, "DEADPID")
}

// ---- S7: the revert hands the path key back to its journaled owner --------

// keyOwnerFixture: owner A and live B both at path (A wrote the key last),
// X elsewhere. A's book is retired since, so the old "a live book's row
// first" pick would hand the key to B.
func keyOwnerFixture(t *testing.T) (d *dcFixture, path, aRow, bRow, xBook, xRow string) {
	d = newDCFixture(t)
	path = d.file(t, "lib/shared/01.mp3", 500)
	other := d.file(t, "lib/x/01.mp3", 501)
	bBook := d.book(t, "B", "B", path, nil)
	bf := &database.BookFile{BookID: bBook, FilePath: path, FileSize: 500, Duration: 600, FileHash: "k"}
	require.NoError(t, d.s.CreateBookFile(bf))
	aBook := d.book(t, "A", "A", path, nil)
	af := &database.BookFile{BookID: aBook, FilePath: path, FileSize: 500, Duration: 600, FileHash: "k"}
	require.NoError(t, d.s.CreateBookFile(af))
	xBook = d.book(t, "X", "X", other, nil)
	xf := &database.BookFile{BookID: xBook, FilePath: other, FileSize: 501, Duration: 600, FileHash: "k"}
	require.NoError(t, d.s.CreateBookFile(xf))
	owner, err := d.s.GetBookFileByPath(path)
	require.NoError(t, err)
	require.NotNil(t, owner)
	require.Equal(t, af.ID, owner.ID, "precondition: A holds the key")
	yes := true
	_, err = d.s.ModifyBook(aBook, func(b *database.Book) error { b.MarkedForDeletion = &yes; return nil })
	require.NoError(t, err)
	return d, path, af.ID, bf.ID, xBook, xf.ID
}

func repointAndRevert(t *testing.T, d *dcFixture, xBook, xRow, path string, legacy bool) {
	t.Helper()
	w := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-rp")
	cur, err := d.s.GetBookFileByID(xBook, xRow)
	require.NoError(t, err)
	was := undo.LocationOf(cur)
	to := undo.BookFileLocation{Path: path, Hash: "k", Size: 500}
	if legacy {
		// A repoint journaled before the key owner was recorded.
		require.NoError(t, w.Step(xBook, undo.ChangeTypeBookFileRepoint, "book_file:"+xRow, was.Encode(), to.Encode(), func() error {
			_, err := d.s.ModifyBookFile(xBook, xRow, func(f *database.BookFile) error { to.Apply(f); return nil })
			return err
		}))
	} else {
		require.NoError(t, w.RepointBookFile(xBook, xRow, was, to))
	}
	_, err = audiobooks.NewRevertService(d.s).RevertOperation("op-rp")
	require.NoError(t, err)
	back, err := d.s.GetBookFileByID(xBook, xRow)
	require.NoError(t, err)
	require.Equal(t, was.Path, back.FilePath, "the repoint is undone")
}

func TestRevertRepoint_KeyGoesBackToItsJournaledOwner(t *testing.T) {
	d, path, aRow, _, xBook, xRow := keyOwnerFixture(t)
	repointAndRevert(t, d, xBook, xRow, path, false)
	got, err := d.s.GetBookFileByPath(path)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, aRow, got.ID, "exactly the row that held the key before the repoint")
}

func TestRevertRepoint_LegacyEntryFallsBackToALiveRow(t *testing.T) {
	d, path, _, bRow, xBook, xRow := keyOwnerFixture(t)
	repointAndRevert(t, d, xBook, xRow, path, true)
	got, err := d.s.GetBookFileByPath(path)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, bRow, got.ID, "no owner journaled: a live book's row")
}

func TestRevertRepoint_NeverRewritesAnITunesRow(t *testing.T) {
	d := newDCFixture(t)
	path := d.file(t, "lib/shared/01.mp3", 500)
	other := d.file(t, "lib/x/01.mp3", 501)
	it := d.book(t, "I", "I", path, nil)
	pid := "PIDSHARED"
	_, err := d.s.ModifyBook(it, func(b *database.Book) error { b.ITunesPersistentID = &pid; return nil })
	require.NoError(t, err)
	require.NoError(t, d.s.CreateBookFile(&database.BookFile{BookID: it, FilePath: path, FileSize: 500, Duration: 600, FileHash: "k"}))
	xBook := d.book(t, "X", "X", other, nil)
	xf := &database.BookFile{BookID: xBook, FilePath: other, FileSize: 501, Duration: 600, FileHash: "k"}
	require.NoError(t, d.s.CreateBookFile(xf))
	before, err := d.s.GetBookFiles(it)
	require.NoError(t, err)
	beforeBook, err := d.s.GetBookByID(it)
	require.NoError(t, err)
	repointAndRevert(t, d, xBook, xf.ID, path, false)
	after, err := d.s.GetBookFiles(it)
	require.NoError(t, err)
	require.Equal(t, before, after, "the iTunes row was not re-written")
	afterBook, err := d.s.GetBookByID(it)
	require.NoError(t, err)
	require.Equal(t, beforeBook.UpdatedAt, afterBook.UpdatedAt, "nor its book")
}
