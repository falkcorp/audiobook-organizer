// file: internal/plugins/maintenance/merge_cleanup_verdict_inputs_plan_test.go
// version: 1.0.0
// guid: 8d2f4a60-1c93-4b7e-a5d8-60e1f7c93a24
// last-edited: 2026-11-18

package maintenance

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// The same facts as merge_cleanup_verdict_inputs_test.go, read through the
// loader and the plan: a store's rows in, a held row out. Titles and names are
// invented (Batman comics and characters).

// mcHeldWith is the standard wrong-work pair with one fact edited.
func mcWrongPair(x *mcFixture, loser, target mcSpec) {
	x.mk("target", target)
	x.mk("loser", loser)
	x.point("loser", "target")
}

func TestMergeCleanupPlanReadsACompositeCreditAsItsNames(t *testing.T) {
	x := newMCFixture(t)
	// Different titles, and credits that share Bruce Wayne: one wrong-work signal.
	mcWrongPair(x,
		mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, files: one(40000)},
		mcSpec{title: "Hush", authors: []string{"Bruce Wayne, Selina Kyle"}, files: one(30000)})
	r := wantRow(t, x.plan(), "loser", mcClassNoVerdict, mcSkipSuspicious)
	require.NotContains(t, r.Current["signals"], "AUTH_DIFF", "the credits share a name")
}

func TestMergeCleanupPlanHoldsPlaceholderAndDanglingMetadata(t *testing.T) {
	loserSpec := func() mcSpec {
		return mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, files: one(40000)}
	}
	targetSpec := func() mcSpec {
		return mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)}
	}
	t.Run("premise: the clean pair is an unmerge", func(t *testing.T) {
		x := newMCFixture(t)
		mcWrongPair(x, loserSpec(), targetSpec())
		wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
	})
	t.Run("a placeholder author on the target", func(t *testing.T) {
		x := newMCFixture(t)
		mcWrongPair(x, loserSpec(), targetSpec())
		// The store no longer creates a placeholder author, but legacy rows
		// exist: report Selina Kyle's row under the placeholder name.
		x.f = newMergeCleanupFixer(&Plugin{deps: fakeDeps{store: mcAuthorNameStore{Store: x.st,
			names: map[int]string{x.authors["Selina Kyle"]: "Unknown"}}, root: x.root, vetoes: x.vetoes}})
		x.f.vgProbe = func(context.Context, string) (int, error) { return 1, nil }
		r := wantRow(t, x.plan(), "loser", mcClassNoVerdict, mcSkipPlaceholder)
		require.Equal(t, "true", r.Current["target_credits_placeholder"])
	})
	t.Run("a placeholder title on the loser", func(t *testing.T) {
		x := newMCFixture(t)
		l := loserSpec()
		l.title = "Unknown Title"
		mcWrongPair(x, l, targetSpec())
		wantRow(t, x.plan(), "loser", mcClassNoVerdict, mcSkipPlaceholder)
	})
	t.Run("a credit with no author row is held, not dropped", func(t *testing.T) {
		x := newMCFixture(t)
		l := loserSpec()
		l.authors, l.dangling = nil, 1
		mcWrongPair(x, l, targetSpec())
		r := wantRow(t, x.plan(), "loser", mcClassNoVerdict, mcSkipDanglingAuthor)
		require.Equal(t, "1", r.Current["loser_credits_dangling"])
	})
	t.Run("a dangling credit beside a real one is still held", func(t *testing.T) {
		x := newMCFixture(t)
		l := loserSpec()
		l.dangling = 1
		mcWrongPair(x, l, targetSpec())
		wantRow(t, x.plan(), "loser", mcClassNoVerdict, mcSkipDanglingAuthor)
	})
}

func TestMergeCleanupPlanHoldsATitleAuthorSwappedPairForTheSwapFixer(t *testing.T) {
	x := newMCFixture(t)
	x.mk("target", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(30000)})
	x.mk("loser", mcSpec{title: "Bruce Wayne", authors: []string{"Year One"}, files: one(40000)})
	x.point("loser", "target")
	r := wantRow(t, x.plan(), "loser", mcClassNoVerdict, mcSkipTitleAuthorSwapped)
	require.Contains(t, r.SkipReason, "Fix swapped title and author")
	require.Contains(t, r.Current["signals"], "TRANSPOSED")
}

func TestMergeCleanupPlanAnAlternativeTitleVetoesAnUnmerge(t *testing.T) {
	x := newMCFixture(t)
	x.wrongWork("loser", "target")
	wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
	byID := x.withAlts(map[string][]string{"loser": {"Hush"}})
	r := wantRow(t, x.plan(), "loser", mcClassNoVerdict, mcSkipSuspicious)
	require.Equal(t, "Hush", r.Current["loser_alternative_titles"])
	require.Contains(t, r.Current["signals"], "TITLE_ALT_MATCH")

	t.Run("the fingerprint moves with an alternative title", func(t *testing.T) {
		before := r.Fingerprint
		byID[x.ids["loser"]] = []string{"Hush", "Hush Returns"}
		after, err := x.f.Replan(context.Background(), nil, r, &fakeReporter{})
		require.NoError(t, err)
		require.NotEqual(t, before, after.Fingerprint)
	})
}

func TestMergeCleanupPlanHoldsExactRuntimesWhoseSizesAreIncomplete(t *testing.T) {
	x := newMCFixture(t)
	// Both runtimes are 30000 s; the loser's file has no recorded size.
	x.mk("target", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.mk("loser", mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, files: []mcFile{{secs: 30000, size: 0}}})
	x.point("loser", "target")
	wantRow(t, x.plan(), "loser", mcKindUnmerge, mcSkipSizeIncomplete)
}

func TestMergeCleanupPlanTheOriginalHashVetoesAnUnmerge(t *testing.T) {
	x := newMCFixture(t)
	// The target is the loser's organized copy: it kept the loser's hash as its
	// original hash. Titles and credits differ because the metadata is wrong.
	x.mk("target", mcSpec{title: "Hush", authors: []string{"Selina Kyle"},
		files: []mcFile{{secs: 30000, size: 30000 * 8000, hash: "tagged", orig: "source-hash"}}})
	x.mk("loser", mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"},
		files: []mcFile{{secs: 40000, size: 40000 * 8000, hash: "source-hash"}}})
	x.point("loser", "target")
	r := wantRow(t, x.plan(), "loser", mcClassNoVerdict, mcSkipSuspicious)
	require.Contains(t, r.Current["signals"], "HASH_EQ")
}

func TestMergeCleanupPlanSwapsOnTheAudioOnDisk(t *testing.T) {
	x := newMCFixture(t)
	// Four chapters of an hour on disk and six more recorded but off disk: ten
	// hours with them, four without. The target is one seven-hour file.
	var chapters []mcFile
	for range 4 {
		chapters = append(chapters, mcFile{secs: 3600, size: 3600 * 8000})
	}
	for range 6 {
		chapters = append(chapters, mcFile{secs: 3600, size: 3600 * 8000, missing: true})
	}
	x.mk("target", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(25200)})
	x.mk("loser", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: chapters})
	x.point("loser", "target")
	r := wantRow(t, x.plan(), "loser", mcKindSwap, mcSkipRuntimeIncludesMissing)
	require.Equal(t, "14400", r.Current["loser_runtime_seconds"], "the verdict reads what is on disk")
	require.Equal(t, "36000", r.Current["loser_runtime_with_missing_seconds"])
	require.Equal(t, "6", r.Current["loser_missing_chapter_rows"])
	require.Equal(t, "4", r.Current["loser_files"])
	require.Len(t, r.Holds, 2, "the on-disk verdict is listed beside the hold")
	require.Equal(t, mcSkipMergeLooksCorrect, r.Holds[1].Kind)
}

func TestMergeCleanupOneMeasuredChapterOnDiskIsNotTheWholeBook(t *testing.T) {
	x := newMCFixture(t)
	// The loser's one present file has no measured duration, nine more chapters
	// are missing, and Book.Duration holds some earlier writer's total. That
	// total is not the on-disk runtime: ComputeBookRuntime's single-file
	// fallback must not apply to the present rows alone.
	chapters := []mcFile{{secs: -1, size: 3600 * 8000}}
	for range 9 {
		chapters = append(chapters, mcFile{secs: 3600, size: 3600 * 8000, missing: true})
	}
	x.mk("target", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(9000)})
	loser := x.mk("loser", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: chapters})
	_, err := x.st.ModifyBook(loser, func(b *database.Book) error {
		d := 36000
		b.Duration = &d
		return nil
	})
	require.NoError(t, err)
	x.point("loser", "target")
	r := x.plan()["loser"]
	require.Equal(t, "", r.Current["loser_runtime_seconds"], "an unmeasured present file has no on-disk runtime")
	require.Equal(t, "false", r.Current["loser_runtime_complete"])
	require.NotEqual(t, mcKindSwap+"/", r.Class+"/"+r.Skipped, "never an unheld swap")
	require.NotEmpty(t, r.Skipped)
}

func TestMergeCleanupPlanShowsTheCreditsSeriesAndASINItReadsFrom(t *testing.T) {
	x := newMCFixture(t)
	one1 := 1
	x.mk("target", mcSpec{title: "Hush", authors: []string{"Selina Kyle", "Bruce Wayne"}, series: "Batman Hush", seq: &one1,
		asin: "B000TARGET", files: one(30000)})
	x.mk("loser", mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, asin: "B000LOSER", files: one(40000)})
	x.point("loser", "target")
	r := x.plan()["loser"]
	require.Equal(t, "Bruce Wayne", r.Current["loser_credits"])
	require.Equal(t, "Selina Kyle; Bruce Wayne", r.Current["target_credits"])
	require.Equal(t, "B000LOSER", r.Current["loser_asin"])
	require.Equal(t, "B000TARGET", r.Current["target_asin"])
	require.Equal(t, "1", r.Current["target_series_position"])
	require.NotEmpty(t, r.Current["target_series_id"])
	require.NotContains(t, r.Current, "loser_series_position")
}

func TestMergeCleanupShortMemoryIndexRefusesTheCensus(t *testing.T) {
	require.NoError(t, mcListingShort(10, 10))
	require.NoError(t, mcListingShort(10, 12), "an index longer than the listing is a race, not a short index")
	err := mcListingShort(10, 7)
	require.ErrorContains(t, err, "holds 7 books")
	require.ErrorContains(t, err, "listing has 10")
	require.ErrorContains(t, err, "3 short")

	x := newMCFixture(t)
	x.wrongWork("loser", "target")
	rep := &fakeReporter{}
	x.f = newMergeCleanupFixer(&Plugin{deps: fakeDeps{store: mcShortIndexStore{Store: x.st, short: 1}, root: x.root, vetoes: x.vetoes}})
	_, err = x.f.Plan(context.Background(), nil, rep)
	require.ErrorContains(t, err, "short")
	require.NotEmpty(t, rep.logs, "the refusal is logged with its counts")
	require.Contains(t, rep.logs[len(rep.logs)-1], "authoritative listing has 2")
}

// mcShortIndexStore counts fewer books than the listing holds, as a memdb that
// lost rows does.
type mcShortIndexStore struct {
	database.Store
	short int
}

func (s mcShortIndexStore) Unwrap() database.Store { return s.Store }
func (s mcShortIndexStore) CountAllBooks() (int, error) {
	n, err := s.Store.CountAllBooks()
	return n - s.short, err
}

// mcAuthorNameStore reports chosen author rows under another name, the way a
// legacy placeholder author row reads.
type mcAuthorNameStore struct {
	database.Store
	names map[int]string
}

func (s mcAuthorNameStore) Unwrap() database.Store { return s.Store }
func (s mcAuthorNameStore) GetAuthorByID(id int) (*database.Author, error) {
	a, err := s.Store.GetAuthorByID(id)
	if err != nil || a == nil {
		return a, err
	}
	if n, ok := s.names[id]; ok {
		c := *a
		c.Name = n
		return &c, nil
	}
	return a, nil
}
