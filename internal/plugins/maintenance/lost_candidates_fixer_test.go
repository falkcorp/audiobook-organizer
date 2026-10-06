// file: internal/plugins/maintenance/lost_candidates_fixer_test.go
// version: 1.1.0
// guid: cde35f5b-18f6-420d-bbdf-ba221050c4b8
// last-edited: 2026-10-05

package maintenance

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

const lcTestOpID = "op-lost-candidates-apply"

// lcDeps serves the lost-candidates fixer: canned latest outcomes, and a
// refetch that stores `found` candidates for the book (as the real batch
// fetch writes the cache) and records every call.
type lcDeps struct {
	fakeDeps
	st       *database.PebbleStore
	outcomes map[string]CandidateFetchOutcome
	found    map[string]int

	mu        sync.Mutex
	refetched []string
}

func (d *lcDeps) LatestCandidateFetchOutcomes() (map[string]CandidateFetchOutcome, error) {
	return d.outcomes, nil
}

func (d *lcDeps) LatestCandidateFetchOutcome(id string) (CandidateFetchOutcome, bool, error) {
	o, ok := d.outcomes[id]
	return o, ok, nil
}

func (d *lcDeps) RefetchMetadataCandidates(_ context.Context, id string) (CandidateRefetchResult, error) {
	d.mu.Lock()
	d.refetched = append(d.refetched, id)
	d.mu.Unlock()
	n := d.found[id]
	cands := make([]json.RawMessage, 0, n)
	for range n {
		cands = append(cands, json.RawMessage(`{"title":"Found","author":"Someone","score":0.9}`))
	}
	if err := d.st.PutMetadataCache(&database.MetadataCandidateCache{BookID: id, FetchedAt: time.Now(), Candidates: cands}); err != nil {
		return CandidateRefetchResult{}, err
	}
	if n == 0 {
		return CandidateRefetchResult{Status: "no_match", Detail: "no candidates found"}, nil
	}
	return CandidateRefetchResult{Status: "matched", Candidates: n}, nil
}

type lcLib struct {
	t     *testing.T
	st    *database.PebbleStore
	deps  *lcDeps
	p     *Plugin
	fixer *lostCandidatesFixer
	ids   map[string]string
}

func newLCLib(t *testing.T) *lcLib {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	d := &lcDeps{fakeDeps: fakeDeps{store: st}, st: st, outcomes: map[string]CandidateFetchOutcome{}, found: map[string]int{}}
	p := &Plugin{deps: d, standDownWait: noWait}
	return &lcLib{t: t, st: st, deps: d, p: p, fixer: newLostCandidatesFixer(p), ids: map[string]string{}}
}

var lcMatchedAt = time.Date(2026, 10, 2, 22, 43, 0, 0, time.UTC)

// book creates a book whose latest fetch outcome is status ("" for none).
func (l *lcLib) book(key, path, status string, edit func(*database.Book)) string {
	l.t.Helper()
	b := &database.Book{Title: "Book " + key, Format: "m4b", FilePath: path}
	if edit != nil {
		edit(b)
	}
	created, err := l.st.CreateBook(b)
	require.NoError(l.t, err)
	require.NoError(l.t, l.st.CreateBookFile(&database.BookFile{BookID: created.ID, FilePath: path + "/01.m4b", Format: "m4b"}))
	if status != "" {
		l.deps.outcomes[created.ID] = CandidateFetchOutcome{Status: status, At: lcMatchedAt}
	}
	l.ids[key] = created.ID
	return created.ID
}

func (l *lcLib) plan() (*repairs.PlanResult, map[string]repairs.Row) {
	l.t.Helper()
	series, err := l.st.GetAllSeries()
	require.NoError(l.t, err)
	res, err := repairs.RunPlan(context.Background(), l.fixer, nil, l.p.repairsPlanDeps(l.st, series), &fakeReporter{})
	require.NoError(l.t, err)
	raw, err := json.Marshal(res)
	require.NoError(l.t, err)
	var stored repairs.PlanResult
	require.NoError(l.t, json.Unmarshal(raw, &stored))
	byKey := map[string]repairs.Row{}
	for key, id := range l.ids {
		for _, r := range stored.Rows {
			if r.RowID == id {
				byKey[key] = r
			}
		}
	}
	return &stored, byKey
}

func (l *lcLib) apply(plan *repairs.PlanResult, dry bool, keys ...string) *repairs.ApplyResult {
	l.t.Helper()
	series, err := l.st.GetAllSeries()
	require.NoError(l.t, err)
	var rowIDs []string
	for _, k := range keys {
		rowIDs = append(rowIDs, l.ids[k])
	}
	deps := repairs.ApplyDeps{Guard: l.st, Tags: l.p.repairsGuardTags(), Series: repairs.SeriesNamesFrom(series), OpID: lcTestOpID}
	if !dry {
		deps.Writer = repairs.NewWriter(l.st, l.st, l.fixer.ID(), "bulk_update", "repairs-").WithJournal(l.st, l.st, lcTestOpID)
	}
	res, err := repairs.RunApply(context.Background(), l.fixer, plan, "op-lost-candidates-plan", rowIDs, dry, deps, &fakeReporter{})
	require.NoError(l.t, err)
	return res
}

func lcRowResult(res *repairs.ApplyResult, id string) repairs.RowResult {
	for _, r := range res.Rows {
		if r.RowID == id {
			return r
		}
	}
	return repairs.RowResult{}
}

func (l *lcLib) putCache(key string, n int, at time.Time) {
	l.t.Helper()
	cands := make([]json.RawMessage, 0, n)
	for range n {
		cands = append(cands, json.RawMessage(`{"title":"Kept","score":0.9}`))
	}
	require.NoError(l.t, l.st.PutMetadataCache(&database.MetadataCandidateCache{BookID: l.ids[key], FetchedAt: at, Candidates: cands}))
}

// The plan lists exactly the books that had a matched candidate and lost it;
// apply refetches only the selected ids, never applies metadata, and a dry
// run fetches nothing.
func TestLostCandidatesFixer_PlansAndAppliesByIDWithoutApplyingMetadata(t *testing.T) {
	l := newLCLib(t)
	asin := "B00LOSTAS1"
	matched, noMatch := "matched", "no_match"
	l.book("lost-asin", "/lib/A/Lost Asin", "matched", func(b *database.Book) { b.ASIN = &asin })
	l.book("lost-plain", "/lib/A/Lost Plain", "matched", nil)
	l.book("lost-empty", "/lib/A/Lost Empty", "matched", nil)
	l.book("lost-itunes", "/media/books/itunes/A/Lost iTunes", "matched", nil)
	l.book("has-cands", "/lib/A/Has Cands", "matched", nil)
	l.putCache("has-cands", 2, lcMatchedAt)
	l.book("applied", "/lib/A/Applied", "matched", func(b *database.Book) { b.MetadataReviewStatus = &matched })
	l.book("ruled-no-match", "/lib/A/Ruled", "matched", func(b *database.Book) { b.MetadataReviewStatus = &noMatch })
	l.book("never-matched", "/lib/A/Never", "no_match", nil)
	l.book("never-fetched", "/lib/A/Unfetched", "", nil)
	l.book("searched-since", "/lib/A/Searched Since", "matched", nil)
	l.putCache("searched-since", 0, lcMatchedAt.Add(time.Hour))
	l.deps.found[l.ids["lost-asin"]] = 3
	l.deps.found[l.ids["lost-plain"]] = 1

	plan, rows := l.plan()
	var keys []string
	for k := range rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	require.Equal(t, []string{"lost-asin", "lost-empty", "lost-itunes", "lost-plain", "searched-since"}, keys,
		"only books that lost a matched candidate are rows")
	require.True(t, rows["lost-asin"].Applicable())
	require.Equal(t, lostClassASIN, rows["lost-asin"].Class)
	require.Equal(t, lostClassNoASIN, rows["lost-plain"].Class)
	require.True(t, rows["lost-itunes"].Applicable(), "a fetch-only fixer is cleared for iTunes books")
	require.Equal(t, lostSkipSearchedSince, rows["searched-since"].Skipped)

	before := map[string]*database.Book{}
	for k, id := range l.ids {
		b, err := l.st.GetBookByID(id)
		require.NoError(t, err)
		before[k] = b
	}

	// Dry run: would_apply, and no provider is asked.
	dry := l.apply(plan, true, "lost-asin")
	require.Equal(t, repairs.OutcomeWouldApply, lcRowResult(dry, l.ids["lost-asin"]).Outcome)
	require.Empty(t, l.deps.refetched, "a dry run fetches nothing")

	res := l.apply(plan, false, "lost-asin", "lost-empty", "searched-since")
	require.Equal(t, repairs.OutcomeApplied, lcRowResult(res, l.ids["lost-asin"]).Outcome)
	empty := lcRowResult(res, l.ids["lost-empty"])
	require.Equal(t, repairs.OutcomeFailed, empty.Outcome)
	require.True(t, strings.Contains(empty.Error, errRefetchFoundNothing.Error()), empty.Error)
	require.Equal(t, repairs.OutcomeNotApplicable, lcRowResult(res, l.ids["searched-since"]).Outcome)
	sort.Strings(l.deps.refetched)
	want := []string{l.ids["lost-asin"], l.ids["lost-empty"]}
	sort.Strings(want)
	require.Equal(t, want, l.deps.refetched, "only the selected applicable ids are refetched")

	entry, err := l.st.GetMetadataCache(l.ids["lost-asin"])
	require.NoError(t, err)
	require.Len(t, entry.Candidates, 3)
	entry, err = l.st.GetMetadataCache(l.ids["lost-plain"])
	require.NoError(t, err)
	require.Nil(t, entry, "an unselected row is not refetched")

	// Nothing about any book changed: the fixer applies no metadata.
	for k, id := range l.ids {
		after, err := l.st.GetBookByID(id)
		require.NoError(t, err)
		require.Equal(t, before[k].Title, after.Title, k)
		require.Equal(t, before[k].ASIN, after.ASIN, k)
		require.Equal(t, before[k].MetadataReviewStatus, after.MetadataReviewStatus, k)
		require.Equal(t, before[k].AuthorID, after.AuthorID, k)
		hist, err := l.st.GetBookChangeHistory(id, 10)
		require.NoError(t, err)
		require.Empty(t, hist, "no metadata history for %s", k)
	}

	// A re-plan of a refetched book: it holds candidates, so it is no row.
	_, rows = l.plan()
	_, still := rows["lost-asin"]
	require.False(t, still)
	require.True(t, rows["lost-plain"].Applicable())
}

// A planned row whose book gained candidates (or was applied) before the
// apply is refused as changed_since_plan, not refetched.
func TestLostCandidatesFixer_ChangedSincePlanIsNotRefetched(t *testing.T) {
	l := newLCLib(t)
	l.book("lost", "/lib/B/Lost", "matched", nil)
	l.book("applied-later", "/lib/B/Applied Later", "matched", nil)
	plan, rows := l.plan()
	require.True(t, rows["lost"].Applicable())
	require.True(t, rows["applied-later"].Applicable())

	l.putCache("lost", 1, time.Now())
	matched := "matched"
	_, err := l.st.ModifyBook(l.ids["applied-later"], func(b *database.Book) error { b.MetadataReviewStatus = &matched; return nil })
	require.NoError(t, err)

	res := l.apply(plan, false, "lost", "applied-later")
	require.Equal(t, repairs.OutcomeChangedSincePlan, lcRowResult(res, l.ids["lost"]).Outcome)
	require.Equal(t, repairs.OutcomeChangedSincePlan, lcRowResult(res, l.ids["applied-later"]).Outcome)
	require.Empty(t, l.deps.refetched)
}

// The fixer is registered in the Repairs lane.
func TestLostCandidatesFixer_Registered(t *testing.T) {
	p := &Plugin{deps: fakeDeps{}}
	f, ok := p.Repairs().Get(lostCandidatesFixerID)
	require.True(t, ok)
	require.True(t, repairs.AllowsITunesDatabaseOnly(f))
	// It writes only the candidate cache, so its long refetch run does not
	// park the library scan (the engine takes no stand-down for it).
	require.True(t, repairs.SkipsScanStandDown(f))
}
