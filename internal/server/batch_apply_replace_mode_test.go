// file: internal/server/batch_apply_replace_mode_test.go
// version: 1.1.0
// guid: 29f23ca2-e262-4623-9ea4-7a09bd0e7836
// last-edited: 2026-10-07
//
// Owner ruling 2026-09-27: the review page's bulk buttons get a toggle, "Fill
// empty fields" (default) or "Replace existing". The request's mode reaches
// only books carrying a review_bulk pin (hash-checked or the hashless
// marker). Pinless, script and API rows stay fill-only and fully gated; the
// dry-run preview and the op-results apply never see a mode; the checkpoint
// carries it; a queued run never merges across modes.

package server

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/stretchr/testify/require"
)

const replace = metafetch.BulkApplyModeReplace

// passingFixture is refusedByAuthorAndTranscription with the author in the
// path and no transcript, so the certainty gate passes it.
func passingFixture() (fakeBooks, metafetch.MetadataCandidate) {
	books, cand := refusedByAuthorAndTranscription()
	b := *books["b1"]
	b.FilePath = "/lib/Zed Quill/Moon Book/Moon Book.m4b"
	b.TranscribedTitle = nil
	books["b1"] = &b
	return books, cand
}

// applyPlanForReal drives the plan's ApplyOptions through the real metafetch
// apply onto a book whose description is filled, and returns the committed
// description.
func applyPlanForReal(t *testing.T, books fakeBooks, cand metafetch.MetadataCandidate, plan cachedApplyPlan) string {
	t.Helper()
	desc := "Owner's description"
	book := *books["b1"]
	book.Description = &desc
	var updated *database.Book
	store := &database.MockStore{
		GetBookByIDFunc: func(string) (*database.Book, error) { c := book; return &c, nil },
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) {
			c := *b
			updated = &c
			return &c, nil
		},
	}
	_, err := metafetch.NewService(store).ApplyMetadataCandidateWithOptions("b1", cand, nil, plan.applyOptions())
	if err != nil && !errors.Is(err, metafetch.ErrApplyHistoryIncomplete) {
		t.Fatalf("apply: %v", err)
	}
	if updated == nil || updated.Description == nil {
		t.Fatalf("no row committed or description cleared: %+v", updated)
	}
	return *updated.Description
}

// Replace overwrites a filled description for a review_bulk pin and for the
// hashless marker; fill keeps it. Driven through the real metafetch apply.
func TestReplaceMode_BulkPinAndMarkerOverwriteFillKeeps(t *testing.T) {
	marker := func(metafetch.MetadataCandidate) *metafetch.CandidatePin { return ownerMarker() }
	for _, tc := range []struct {
		name       string
		pin        func(metafetch.MetadataCandidate) *metafetch.CandidatePin
		mode       string
		wantDesc   string
		wantUnseen bool
	}{
		{"bulk pin, replace", bulkPin, replace, "Provider description", false},
		{"owner marker, replace", marker, replace, "Provider description", true},
		{"bulk pin, fill", bulkPin, "", "Owner's description", false},
		{"owner marker, fill", marker, "", "Owner's description", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			books, cand := refusedByAuthorAndTranscription()
			cand.Description = "Provider description"
			pin := tc.pin(cand)
			plan := planCachedApply(&fakeApplySvc{candidates: candidateJSON(t, cand)}, books, "b1", nil, pin).withBulkMode(pin, tc.mode)
			if !plan.OwnerReviewed || plan.Reason != "" {
				t.Fatalf("plan reason %q owner %v, want the gate lifted", plan.Reason, plan.OwnerReviewed)
			}
			opts := plan.applyOptions()
			wantReplace := tc.mode == replace
			if opts.FillOnly == wantReplace || opts.OwnerReplace != wantReplace || opts.UnseenCandidate != tc.wantUnseen {
				t.Fatalf("opts %+v, want FillOnly=%v OwnerReplace=%v UnseenCandidate=%v", opts, !wantReplace, wantReplace, tc.wantUnseen)
			}
			if got := applyPlanForReal(t, books, cand, plan); got != tc.wantDesc {
				t.Fatalf("description = %q, want %q", got, tc.wantDesc)
			}
		})
	}
}

// Through the per-book apply the op runs: replace reaches the ApplyOptions
// and is reported on the outcome.
func TestReplaceMode_OutcomeReportsOwnerReplace(t *testing.T) {
	books, cand := passingFixture()
	svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
	out := applyCachedCandidateForBookTimed(svc, books, "b1", false, nil, metafetch.NewApplyPhaseTimings(), nil, bulkPin(cand), replace)
	if !out.Applied || !out.OwnerReplace || out.OwnerReviewed {
		t.Fatalf("outcome %+v, want applied as owner replace (gate passed, nothing overridden)", out)
	}
	if len(svc.applyOpts) != 1 || svc.applyOpts[0].FillOnly || !svc.applyOpts[0].OwnerReplace {
		t.Fatalf("apply opts %+v, want overwrite with OwnerReplace", svc.applyOpts)
	}
}

// Replace has no effect on a book without an owner-review pin: pinless, a
// script's pin (no origin) or a pin of another origin stays fill-only when the
// gate passes it and hard-gated when the gate refuses it. A row pin already
// overwrites and is not labelled an owner replace.
func TestReplaceMode_IgnoredWithoutReviewBulkPin(t *testing.T) {
	scriptPin := func(c metafetch.MetadataCandidate) *metafetch.CandidatePin { p := metafetch.PinOf(c); return &p }
	otherOrigin := func(c metafetch.MetadataCandidate) *metafetch.CandidatePin {
		p := metafetch.PinOf(c)
		p.Origin = "bulk"
		return &p
	}
	none := func(metafetch.MetadataCandidate) *metafetch.CandidatePin { return nil }
	for name, pin := range map[string]func(metafetch.MetadataCandidate) *metafetch.CandidatePin{
		"pinless": none, "script pin": scriptPin, "other origin": otherOrigin,
	} {
		t.Run(name+", gate passes", func(t *testing.T) {
			books, cand := passingFixture()
			svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
			out := applyCachedCandidateForBookTimed(svc, books, "b1", false, nil, metafetch.NewApplyPhaseTimings(), nil, pin(cand), replace)
			if !out.Applied || out.OwnerReplace || out.OwnerReviewed {
				t.Fatalf("outcome %+v, want an ordinary applied book", out)
			}
			if len(svc.applyOpts) != 1 || !svc.applyOpts[0].FillOnly || svc.applyOpts[0].OwnerReplace {
				t.Fatalf("apply opts %+v, want fill-only", svc.applyOpts)
			}
		})
		t.Run(name+", gate refuses", func(t *testing.T) {
			books, cand := refusedByAuthorAndTranscription()
			svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
			out := applyCachedCandidateForBookTimed(svc, books, "b1", false, nil, metafetch.NewApplyPhaseTimings(), nil, pin(cand), replace)
			if out.Applied || out.OwnerReplace || out.Reason != applySkipGateBlocked || len(svc.applyOpts) != 0 {
				t.Fatalf("outcome %+v opts %+v, want gate_blocked and nothing applied", out, svc.applyOpts)
			}
		})
	}

	books, cand := passingFixture()
	svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
	out := applyCachedCandidateForBookTimed(svc, books, "b1", false, nil, metafetch.NewApplyPhaseTimings(), nil, rowPin(cand), replace)
	if !out.Applied || out.OwnerReplace || len(svc.applyOpts) != 1 || svc.applyOpts[0].FillOnly || svc.applyOpts[0].OwnerReplace {
		t.Fatalf("row pin: outcome %+v opts %+v, want an overwrite without the owner-replace label", out, svc.applyOpts)
	}
}

// A plan that will not apply (stale pin) is left alone by the mode.
func TestReplaceMode_SkippedPlanUnchanged(t *testing.T) {
	books, cand := passingFixture()
	shown := bulkPin(cand)
	cand.Title = "Moon Book (refetched)"
	plan := planCachedApply(&fakeApplySvc{candidates: candidateJSON(t, cand)}, books, "b1", nil, shown).withBulkMode(shown, replace)
	if plan.Reason != applySkipStaleCandidate || plan.BulkReplace {
		t.Fatalf("plan %+v, want stale_candidate without BulkReplace", plan)
	}
}

// The dry-run preview (bulk_apply_preview.go) plans with planCachedApply,
// pinless and with no mode parameter to pass, and the op-results apply takes
// neither, so neither ever plans an owner replace.
func TestReplaceMode_PreviewAndOpResultNeverReplace(t *testing.T) {
	books, cand := passingFixture()
	svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
	plan := planCachedApply(svc, books, "b1", nil, nil)
	if plan.BulkReplace || plan.applyOptions().OwnerReplace || !plan.applyOptions().FillOnly {
		t.Fatalf("preview plan %+v, want fill-only", plan)
	}
	op := planOpResultApply(books, "b1", CandidateResult{Candidate: &cand, Book: CandidateBookInfo{ID: "b1", Title: "Moon Book", Author: "Zed Quill"}}, nil)
	if op.BulkReplace || op.applyOptions().OwnerReplace {
		t.Fatalf("op-results plan %+v, want no owner replace", op)
	}
}

func mergeParams(t *testing.T, a, b batchApplyOpParams) (batchApplyOpParams, bool) {
	t.Helper()
	ea, _ := json.Marshal(a)
	eb, _ := json.Marshal(b)
	raw, ok, err := mergeBatchApplyQueuedParams(ea, eb)
	require.NoError(t, err)
	if !ok {
		return batchApplyOpParams{}, false
	}
	var out batchApplyOpParams
	require.NoError(t, json.Unmarshal(raw, &out))
	return out, true
}

// Fill and replace never merge (either direction); replace+replace merges and
// stays replace; "" and "fill" are the same mode.
func TestReplaceMode_QueuedMergeKeepsModesApart(t *testing.T) {
	withBulkApplyCapServer(t, 100)
	pins := map[string]metafetch.CandidatePin{"a": *ownerMarker()}
	fill := batchApplyOpParams{BookIDs: []string{"a"}, WriteBack: true, Pins: pins}
	repl := batchApplyOpParams{BookIDs: []string{"b"}, WriteBack: true, Pins: map[string]metafetch.CandidatePin{"b": *ownerMarker()}, Mode: replace}

	if _, ok := mergeParams(t, fill, repl); ok {
		t.Fatal("a replace request merged into a queued fill run")
	}
	if _, ok := mergeParams(t, repl, fill); ok {
		t.Fatal("a fill request merged into a queued replace run")
	}
	repl2 := batchApplyOpParams{BookIDs: []string{"c"}, WriteBack: true, Mode: replace}
	m, ok := mergeParams(t, repl, repl2)
	require.True(t, ok)
	require.Equal(t, replace, m.Mode)
	require.Equal(t, []string{"b", "c"}, m.BookIDs)
	require.Contains(t, m.Pins, "b")

	named := batchApplyOpParams{BookIDs: []string{"d"}, WriteBack: true, Mode: metafetch.BulkApplyModeFill}
	m, ok = mergeParams(t, fill, named)
	require.True(t, ok, `"" and "fill" must merge`)
	require.Empty(t, m.Mode)

	bad := batchApplyOpParams{BookIDs: []string{"e"}, WriteBack: true, Mode: "overwrite"}
	if _, ok := mergeParams(t, fill, bad); ok {
		t.Fatal("an unknown mode merged")
	}
}

// The checkpoint carries the mode and the owed books' pins, through the real
// payload builder and a JSON round trip.
func TestReplaceMode_CheckpointCarriesModeAndPins(t *testing.T) {
	pins := map[string]metafetch.CandidatePin{"a": *ownerMarker(), "c": *ownerMarker()}
	st := batchApplyCheckpointState([]string{"a", "b", "c"}, true, 3, 1, []string{"a"}, pins, replace)
	raw, err := json.Marshal(st)
	require.NoError(t, err)
	var back batchApplyOpParams
	require.NoError(t, json.Unmarshal(raw, &back))
	require.Equal(t, replace, back.Mode)
	require.Equal(t, []string{"b", "c", "a"}, back.BookIDs)
	require.Len(t, back.Pins, 2, "deferred a and owed c keep their pins")

	fill := batchApplyCheckpointState([]string{"a"}, true, 1, 0, nil, nil, "")
	raw, _ = json.Marshal(fill)
	require.NotContains(t, string(raw), `"mode"`, "a fill checkpoint names no mode")
}

// Run refuses an unknown mode (a hand-written /operations/v2 call) before any
// dependency or write; replace passes the check.
func TestReplaceMode_RunRefusesUnknownMode(t *testing.T) {
	withBulkApplyCapServer(t, 10)
	err := capRunOp(t, "metadata.batch-apply-cached", (*Server).RegisterBatchApplyFromCacheOp,
		batchApplyOpParams{BookIDs: []string{"b1"}, Mode: "overwrite"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown mode")

	err = capRunOp(t, "metadata.batch-apply-cached", (*Server).RegisterBatchApplyFromCacheOp,
		batchApplyOpParams{BookIDs: []string{"b1"}, Mode: replace})
	require.Error(t, err, "a zero Server fails later, at its dependency check")
	require.False(t, strings.Contains(err.Error(), "unknown mode"), "replace is a known mode: %v", err)
}
