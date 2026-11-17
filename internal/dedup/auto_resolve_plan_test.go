// file: internal/dedup/auto_resolve_plan_test.go
// version: 1.0.0
// guid: 5a1e8c37-d204-4b69-93f0-7e6b2c4a9d18
// last-edited: 2026-11-17

// Tests for the dry-run plan of the Tier-1 auto-resolution pass: what it stores,
// that it is bounded, that it carries no titles, that it decides exactly as the
// apply path does, and that a dry run writes nothing but its own result.

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// planEngine is an engine over a real store whose embedding store shares the
// book store's DB, as in production, so a dump of that one DB sees every write
// the pass could make.
func planEngine(t *testing.T) (*Engine, *database.PebbleStore, *database.EmbeddingStore) {
	t.Helper()
	s, err := database.NewPebbleStoreInMemory("plan_" + t.Name())
	require.NoError(t, err)
	s.WaitForWarmup()
	t.Cleanup(func() { _ = s.Close() })
	es := database.NewEmbeddingStoreFromStore(s)
	require.NotNil(t, es)
	engine, err := NewEngine(es, s, nil, nil, merge.NewService(s), unified.DefaultScoreConfig())
	require.NoError(t, err)
	return engine, s, es
}

func planBooks(t *testing.T, s *database.PebbleStore, ids ...string) {
	t.Helper()
	for _, id := range ids {
		_, err := s.CreateBook(arPlausibleBook(id, "The Dark Knight Returns "+id))
		require.NoError(t, err)
	}
}

// The plan lists the eligible pairs in the order apply visits them, with the
// evidence and the deciding guard for each, and lists the refused pair with the
// guard that stopped it.
func TestAutoResolveDryRun_PlanListsEligibleAndRefusedPairs(t *testing.T) {
	engine, s, es := planEngine(t)
	planBooks(t, s, "A1", "B1", "A2", "B2", "A3", "B3", "C1")
	mustSeed(t, es, "A1", "B1", unified.BandCertain, unified.SigExactFile, unified.SigISBNASIN)
	mustSeed(t, es, "A2", "B2", unified.BandCertain, unified.SigExactAcoustID, unified.SigMetaSrcHash)
	mustSeed(t, es, "A3", "B3", unified.BandCertain, unified.SigExactFile) // one kind: refused
	mustSeed(t, es, "A1", "C1", unified.BandCertain, unified.SigExactFile, unified.SigISBNASIN)

	res, err := engine.AutoResolveCertain(context.Background(), false, 0, 0)
	require.NoError(t, err)
	require.True(t, res.DryRun)
	require.Equal(t, 4, res.Checked)
	require.Equal(t, 3, res.Eligible)
	require.Zero(t, res.Merged)
	require.Equal(t, autoResolveMaxMergesDefault, res.MaxMerges, "the cap apply would use")

	require.Len(t, res.Plan, 3)
	for i, e := range res.Plan {
		require.Equal(t, i+1, e.Rank, "rank is the order apply visits")
		require.Equal(t, AutoResolveRuleID, e.Rule)
		require.Equal(t, guardCorroborationSignals, e.Guard)
		require.Equal(t, unified.BandCertain, e.Band)
		require.EqualValues(t, 100, e.Score)
		require.NotEmpty(t, e.Reason)
		require.Len(t, e.PrimaryKinds, 2)
		require.Len(t, e.Signals, 2)
		require.NotZero(t, e.CandidateID)
	}
	require.Less(t, res.Plan[0].CandidateID, res.Plan[1].CandidateID, "ascending candidate id, as apply visits them")
	require.Equal(t, [2]string{"A1", "B1"}, [2]string{res.Plan[0].BookAID, res.Plan[0].BookBID})
	require.Zero(t, res.Plan[0].SharesBookWithRank)
	require.Zero(t, res.Plan[1].SharesBookWithRank)
	require.Equal(t, 1, res.Plan[2].SharesBookWithRank, "A1 is also in entry 1: apply would merge A1 away first")

	require.Len(t, res.Refused, 1)
	require.Equal(t, guardCorroborationInsufficient, res.Refused[0].Guard)
	require.Zero(t, res.Refused[0].Rank, "a refused pair has no merge rank")
	require.Equal(t, map[string]int{guardCorroborationInsufficient: 1}, res.RefusedByGuard)
	require.Zero(t, res.PlanTruncated)
	require.Zero(t, res.RefusedTruncated)
}

// A candidate the pass cannot judge is recorded with the reason, not dropped.
func TestAutoResolveDryRun_UnjudgeablePairsAreRecordedNotDropped(t *testing.T) {
	engine, s, es := planEngine(t)
	planBooks(t, s, "A1", "B1", "A2")
	trashed, err := s.GetBookByID("A2")
	require.NoError(t, err)
	trashed.MarkedForDeletion = new(true)
	_, err = s.UpdateBook("A2", trashed)
	require.NoError(t, err)
	mustSeed(t, es, "A1", "GONE", unified.BandCertain, unified.SigExactFile, unified.SigISBNASIN)
	mustSeed(t, es, "A2", "B1", unified.BandCertain, unified.SigExactFile, unified.SigISBNASIN)

	res, err := engine.AutoResolveCertain(context.Background(), false, 0, 0)
	require.NoError(t, err)
	require.Equal(t, 2, res.Checked)
	require.Zero(t, res.Eligible)
	require.Equal(t, map[string]int{guardBookUnreadable: 1, guardBookSoftDeleted: 1}, res.RefusedByGuard)
	require.Len(t, res.Refused, 2)
}

// The cap holds the stored list, and the pairs past it are counted.
func TestAutoResolvePlan_CapsEachListAndCountsTheRest(t *testing.T) {
	p := newAutoResolvePlan(true, 3, 2)
	for i := range 5 {
		c := database.DedupCandidate{ID: int64(i + 1), EntityAID: fmt.Sprintf("a%d", i), EntityBID: fmt.Sprintf("b%d", i), Band: unified.BandCertain}
		p.add(c, autoResolveDecision{Eligible: true, Guard: guardCorroborationSignals})
		p.refuse(c, autoResolveDecision{Guard: guardBand})
	}
	var res AutoResolveResult
	p.finish(&res, 200)
	require.Len(t, res.Plan, 3)
	require.Equal(t, 2, res.PlanTruncated)
	require.Len(t, res.Refused, 2)
	require.Equal(t, 3, res.RefusedTruncated)
	require.Equal(t, map[string]int{guardBand: 5}, res.RefusedByGuard, "every refusal is counted, listed or not")
	require.Equal(t, map[string]int{guardCorroborationSignals: 5}, res.PlannedByGuard, "every admitted pair is counted by guard, listed or not")
	require.Equal(t, int64(3), res.Plan[2].CandidateID, "the first entries are the ones kept, in order")
	require.Equal(t, 3, res.Plan[2].Rank)
}

// A disabled plan (the apply path) records and allocates nothing.
func TestAutoResolvePlan_DisabledPlanRecordsNothing(t *testing.T) {
	p := newAutoResolvePlan(false, 10, 10)
	c := database.DedupCandidate{ID: 1, EntityAID: "a", EntityBID: "b"}
	p.add(c, autoResolveDecision{Eligible: true})
	p.refuse(c, autoResolveDecision{Guard: guardBand})
	res := AutoResolveResult{DryRun: false}
	p.finish(&res, 200)
	require.Nil(t, res.Plan)
	require.Nil(t, res.Refused)
	require.Nil(t, res.RefusedByGuard)
	require.Zero(t, res.MaxMerges)
}

// The stored report carries ids, numbers and guard names, never a title and never
// a signal's free-text evidence, and it round-trips through JSON.
func TestAutoResolveDryRunReport_HasNoTitlesAndRoundTrips(t *testing.T) {
	engine, s, es := planEngine(t)
	planBooks(t, s, "A1", "B1")
	sb := certainBreakdown("A1", "B1", unified.SigExactFile, unified.SigISBNASIN)
	sb.Signals[0].Evidence = "Batman Begins.m4b at /library/Bruce Wayne/Batman Begins.m4b"
	require.NoError(t, es.UpsertCandidate(database.DedupCandidate{
		EntityType: "book", EntityAID: "A1", EntityBID: "B1", Layer: "exact", Status: "pending",
		Band: unified.BandCertain, FormulaVersion: "test", ScoreBreakdown: sb,
	}))

	res, err := engine.AutoResolveCertain(context.Background(), false, 0, 0)
	require.NoError(t, err)
	raw, err := json.Marshal(NewAutoResolveDryRunReport(res))
	require.NoError(t, err)
	for _, forbidden := range []string{"The Dark Knight Returns", "Batman Begins", "Bruce Wayne", "/library", "title"} {
		require.NotContains(t, string(raw), forbidden)
	}

	var back AutoResolveDryRunReport
	require.NoError(t, json.Unmarshal(raw, &back))
	require.Equal(t, AutoResolveRuleID, back.RuleID)
	require.True(t, back.DryRun)
	require.Equal(t, autoResolvePlanCap, back.PlanCap)
	require.Equal(t, autoResolveRefusedCap, back.RefusedCap)
	require.Equal(t, 1, back.Eligible)
	require.Len(t, back.Plan, 1)
	require.Equal(t, "A1", back.Plan[0].BookAID)
	require.NotEmpty(t, back.NotEvaluated)
	require.NotNil(t, back.Refused, "an empty list is [], not null")
}

// The proof the brief asks for: a dry run performs no write. Every key and value
// of the one DB the book store and the embedding store share is identical before
// and after it. (Its own result is stored by the op wrapper, not by the engine.)
func TestAutoResolveDryRun_WritesNothing(t *testing.T) {
	engine, s, es := planEngine(t)
	planBooks(t, s, "A1", "B1", "A2", "B2", "A3", "B3")
	mustSeed(t, es, "A1", "B1", unified.BandCertain, unified.SigExactFile, unified.SigISBNASIN)
	mustSeed(t, es, "A2", "B2", unified.BandCertain, unified.SigExactAcoustID, unified.SigMetaSrcHash)
	mustSeed(t, es, "A3", "B3", unified.BandCertain, unified.SigExactFile)
	// A labelled example takes the one read-through path in the rule.
	require.NoError(t, es.UpsertLabeledExample(database.LabeledExample{CandidateID: 3, Label: "true_dup", LabelReason: "whole-book signatures match"}))

	before := dumpDB(t, s)
	require.NotEmpty(t, before)
	res, err := engine.AutoResolveCertain(context.Background(), false, 0, 0)
	require.NoError(t, err)
	require.Equal(t, 3, res.Eligible, "the labelled pair is eligible too, so the label path ran")
	require.Equal(t, map[string]int{guardCorroborationSignals: 2, guardCorroborationLabel: 1}, res.PlannedByGuard,
		"the pair that rests on the free-text label is counted apart from the signal-corroborated ones")
	var labelled []AutoResolvePlanEntry
	for _, e := range res.Plan {
		if e.Guard == guardCorroborationLabel {
			labelled = append(labelled, e)
		}
	}
	require.Len(t, labelled, 1, "and its plan entry names the label guard")
	require.Equal(t, int64(3), labelled[0].CandidateID)
	require.Equal(t, before, dumpDB(t, s), "a dry run must write nothing")
}

// The apply path is unchanged by the plan: it records no plan and still merges
// and journals exactly as before (the existing apply tests cover the behaviour;
// this pins that the plan does not leak into an apply result).
func TestAutoResolveApply_RecordsNoPlan(t *testing.T) {
	engine, store, es := setupRealStoreEngine(t)
	prev := config.AppConfig.Dedup.AutoResolveEnabled
	config.AppConfig.Dedup.AutoResolveEnabled = true
	t.Cleanup(func() { config.AppConfig.Dedup.AutoResolveEnabled = prev })
	for _, id := range []string{"PA", "PB", "PC", "PD"} {
		_, err := store.CreateBook(arPlausibleBook(id, "Batman Year One "+id))
		require.NoError(t, err)
	}
	mustSeed(t, es, "PA", "PB", unified.BandCertain, unified.SigExactFile, unified.SigISBNASIN)
	mustSeed(t, es, "PC", "PD", unified.BandCertain, unified.SigExactFile)

	res, err := engine.AutoResolveCertain(context.Background(), true, 5, 50)
	require.NoError(t, err)
	require.Equal(t, 1, res.Merged)
	require.Nil(t, res.Plan)
	require.Nil(t, res.Refused)
	require.Nil(t, res.RefusedByGuard)
	require.Zero(t, res.MaxMerges)
	require.Zero(t, res.PlanTruncated)
}

// Each refusal names the guard that decided it.
func TestAutoResolveEligible_NamesTheDecidingGuard(t *testing.T) {
	engine, _, _ := setupTestEngine(t)
	good := func() (database.DedupCandidate, *database.Book, *database.Book) {
		return database.DedupCandidate{
			ID: 1, EntityType: "book", EntityAID: "A", EntityBID: "B", Band: unified.BandCertain, Status: "pending",
			ScoreBreakdown: certainBreakdown("A", "B", unified.SigExactFile, unified.SigISBNASIN),
		}, arPlausibleBook("A", "Same Book"), arPlausibleBook("B", "Same Book")
	}
	cases := []struct {
		name  string
		edit  func(c *database.DedupCandidate, a, b *database.Book)
		guard string
		ok    bool
	}{
		{"eligible", func(*database.DedupCandidate, *database.Book, *database.Book) {}, guardCorroborationSignals, true},
		{"band", func(c *database.DedupCandidate, _, _ *database.Book) { c.Band = unified.BandHigh }, guardBand, false},
		{"no breakdown", func(c *database.DedupCandidate, _, _ *database.Book) { c.ScoreBreakdown = nil }, guardNoBreakdown, false},
		{"stored suppressors", func(c *database.DedupCandidate, _, _ *database.Book) {
			c.ScoreBreakdown.Suppressors = []string{"series_volume_differs"}
		}, guardSuppressors, false},
		{"live suppression", func(_ *database.DedupCandidate, a, b *database.Book) {
			vg := "vg"
			a.VersionGroupID, b.VersionGroupID = &vg, &vg
		}, guardPairEligibility, false},
		{"no audio", func(_ *database.DedupCandidate, _, b *database.Book) { b.Duration = nil }, guardPlausibleAudio, false},
		{"identifier conflict", func(_ *database.DedupCandidate, a, b *database.Book) {
			a.ISBN13, b.ISBN13 = new("9780000000001"), new("9780000000002")
		}, guardIdentifiersConflict, false},
		{"one signal", func(c *database.DedupCandidate, _, _ *database.Book) {
			c.ScoreBreakdown = certainBreakdown("A", "B", unified.SigExactFile)
		}, guardCorroborationInsufficient, false},
		{"same path", func(_ *database.DedupCandidate, a, b *database.Book) {
			a.FilePath, b.FilePath = "/lib/Batman/Book.m4b", "/lib/Batman//Book.m4b"
		}, guardAutomatedRefusal, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, a, b := good()
			tc.edit(&c, a, b)
			d := engine.autoResolveEligible(c, a, b)
			require.Equal(t, tc.ok, d.Eligible, d.Reason)
			require.Equal(t, tc.guard, d.Guard, d.Reason)
			require.NotEmpty(t, d.Reason)
		})
	}

	t.Run("label fallback", func(t *testing.T) {
		engine, _, es := setupTestEngine(t)
		c, a, b := good()
		c.ID = 42
		c.ScoreBreakdown = certainBreakdown("A", "B", unified.SigExactFile)
		require.NoError(t, es.UpsertLabeledExample(database.LabeledExample{CandidateID: 42, Label: "true_dup", LabelReason: "whole-book signatures match"}))
		d := engine.autoResolveEligible(c, a, b)
		require.True(t, d.Eligible)
		require.Equal(t, guardCorroborationLabel, d.Guard)
	})
}

// dumpDB is every key and value in the store's DB.
func dumpDB(t *testing.T, s *database.PebbleStore) map[string]string {
	t.Helper()
	iter, err := s.DB().NewIter(&pebble.IterOptions{})
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()
	out := map[string]string{}
	for iter.First(); iter.Valid(); iter.Next() {
		out[string(iter.Key())] = string(iter.Value())
	}
	require.NoError(t, iter.Error())
	return out
}
