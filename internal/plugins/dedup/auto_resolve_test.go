// file: internal/plugins/dedup/auto_resolve_test.go
// version: 1.1.0
// guid: 7e0a5c93-2b18-4f64-9d07-1a6c8f3b0e52
// last-edited: 2026-11-17

// Tests for the dedup.auto-resolve op wrapper (TASK-17). The engine-level
// eligibility, merge, journal, and unmerge behaviour is covered in
// internal/dedup/auto_resolve_test.go; these assert the OperationDef metadata,
// the dry-run contract, and the apply=true kill-switch guard at the op boundary.

package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func autoResolveMockStore() *database.MockStore {
	dur := 3600
	books := map[string]*database.Book{
		"AA": {ID: "AA", Title: "Same Book", Duration: &dur},
		"BB": {ID: "BB", Title: "Same Book", Duration: &dur},
	}
	return &database.MockStore{
		GetBookByIDFunc:  func(id string) (*database.Book, error) { return books[id], nil },
		GetBookFilesFunc: func(string) ([]database.BookFile, error) { return nil, nil },
	}
}

func seedCertainCandidate(t *testing.T, es *database.EmbeddingStore, aID, bID string) {
	t.Helper()
	sb := &unified.UnifiedDedupScore{
		Pair:  [2]string{aID, bID},
		Score: 100,
		Band:  unified.BandCertain,
		Signals: []unified.Signal{
			{Kind: unified.SigExactFile, Confidence: 0.99},
			{Kind: unified.SigISBNASIN, Confidence: 0.99},
		},
	}
	require.NoError(t, es.UpsertCandidate(database.DedupCandidate{
		EntityType:     "book",
		EntityAID:      aID,
		EntityBID:      bID,
		Layer:          "exact",
		Status:         "pending",
		Band:           unified.BandCertain,
		FormulaVersion: "test",
		ScoreBreakdown: sb,
	}))
}

func TestAutoResolveOp_Metadata(t *testing.T) {
	p := &Plugin{}
	def := p.autoResolveDef()
	assert.Equal(t, "dedup.auto-resolve", def.ID)
	assert.Contains(t, def.Capabilities, sdk.CapLibraryRead)
	assert.Contains(t, def.Capabilities, sdk.CapLibraryWrite)

	var params autoResolveParams
	require.NoError(t, json.Unmarshal([]byte(`{}`), &params))
	assert.False(t, params.Apply, "apply must default to false (dry-run)")
}

func TestAutoResolveOp_DryRunProducesReport(t *testing.T) {
	es := newTestEmbeddingStorePurge(t)
	ms := autoResolveMockStore()
	seedCertainCandidate(t, es, "AA", "BB")

	p := buildPluginWithEngine(t, es, ms)
	params, err := json.Marshal(autoResolveParams{Apply: false})
	require.NoError(t, err)
	rep := &resultReporter{}
	require.NoError(t, p.runAutoResolve(context.Background(), params, rep))

	// The dry run's plan is the op's result (GET /operations/:id/result): the
	// pair, its evidence and the guard, with no title.
	raw, err := json.Marshal(rep.result)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "Same Book", "no titles in a stored result")
	var report dedupengine.AutoResolveDryRunReport
	require.NoError(t, json.Unmarshal(raw, &report))
	assert.True(t, report.DryRun)
	assert.Equal(t, dedupengine.AutoResolveRuleID, report.RuleID)
	require.Len(t, report.Plan, 1)
	assert.Equal(t, "AA", report.Plan[0].BookAID)
	assert.Equal(t, "BB", report.Plan[0].BookBID)
	assert.Equal(t, 1, report.Plan[0].Rank)
	assert.Equal(t, "corroboration_signals", report.Plan[0].Guard)
	assert.Len(t, report.Plan[0].Signals, 2)

	// Dry-run leaves the candidate pending (no merge).
	cands, _, err := es.ListCandidates(database.CandidateFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, cands, 1)
	assert.Equal(t, "pending", cands[0].Status, "dry-run must not mutate candidate status")
}

// resultReporter is a reporter that, like the real one, can store the op result.
type resultReporter struct {
	mockReporter
	result any
	err    error
}

func (r *resultReporter) SetResult(v any) error {
	if r.err != nil {
		return r.err
	}
	r.result = v
	return nil
}

// A dry run whose plan cannot be stored must fail the op: the plan is the dry
// run's only product, and a run that reports success while the plan is lost is
// the silent shape this replaces.
func TestAutoResolveOp_DryRunFailsWhenThePlanCannotBeStored(t *testing.T) {
	es := newTestEmbeddingStorePurge(t)
	seedCertainCandidate(t, es, "AA", "BB")
	p := buildPluginWithEngine(t, es, autoResolveMockStore())
	params, err := json.Marshal(autoResolveParams{Apply: false})
	require.NoError(t, err)

	err = p.runAutoResolve(context.Background(), params, &resultReporter{err: errors.New("result store down")})
	require.ErrorContains(t, err, "store the dry-run plan")
	require.ErrorContains(t, err, "result store down")

	// A reporter that cannot store results at all fails loudly too.
	err = p.runAutoResolve(context.Background(), params, &mockReporter{})
	require.ErrorContains(t, err, "cannot persist results")
}

// An apply run stores no plan result: it did its work, and this op adds no write
// to it. (No eligible pair here, so nothing is merged; the point is the result.)
func TestAutoResolveOp_ApplyStoresNoPlan(t *testing.T) {
	es := newTestEmbeddingStorePurge(t)
	seedCertainCandidate(t, es, "AA", "MISSING")
	prev := config.AppConfig.Dedup.AutoResolveEnabled
	config.AppConfig.Dedup.AutoResolveEnabled = true
	t.Cleanup(func() { config.AppConfig.Dedup.AutoResolveEnabled = prev })

	p := buildPluginWithEngine(t, es, autoResolveMockStore())
	params, err := json.Marshal(autoResolveParams{Apply: true})
	require.NoError(t, err)
	rep := &resultReporter{}
	require.NoError(t, p.runAutoResolve(context.Background(), params, rep))
	assert.Nil(t, rep.result, "an apply run stores no dry-run plan")
}

func TestAutoResolveOp_ApplyRefusedWithoutKillSwitch(t *testing.T) {
	es := newTestEmbeddingStorePurge(t)
	ms := autoResolveMockStore()
	seedCertainCandidate(t, es, "AA", "BB")

	prev := config.AppConfig.Dedup.AutoResolveEnabled
	config.AppConfig.Dedup.AutoResolveEnabled = false
	t.Cleanup(func() { config.AppConfig.Dedup.AutoResolveEnabled = prev })

	p := buildPluginWithEngine(t, es, ms)
	params, err := json.Marshal(autoResolveParams{Apply: true})
	require.NoError(t, err)

	err = p.runAutoResolve(context.Background(), params, &mockReporter{})
	require.Error(t, err, "apply=true with kill switch off must error at the op boundary")

	cands, _, err := es.ListCandidates(database.CandidateFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, cands, 1)
	assert.Equal(t, "pending", cands[0].Status, "gated apply must not mutate candidate status")
}
