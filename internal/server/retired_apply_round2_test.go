// file: internal/server/retired_apply_round2_test.go
// version: 1.0.1
// guid: 7b2e94d1-3c58-4a06-b9f7-1d8e0c4a6f35
// last-edited: 2026-11-18

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// Round 2 of the retired-book guard: one rule for who may write a retired book
// (metafetch.PinAllowsRetired, carried to the apply as ApplyOptions.AllowRetired),
// and the backstops read it instead of automatic().

// markerPin is the hashless select-all marker.
func markerPin() *metafetch.CandidatePin { return ownerMarker() }

type pinCase struct {
	name string
	pin  func(metafetch.MetadataCandidate) *metafetch.CandidatePin
	mode string
	// allow is whether the plan may write a retired book.
	allow bool
}

var pinCases = []pinCase{
	{"no_pin", func(metafetch.MetadataCandidate) *metafetch.CandidatePin { return nil }, "", false},
	{"bulk_fill", bulkPin, "", false},
	{"bulk_replace", bulkPin, replace, false},
	{"marker_fill", func(metafetch.MetadataCandidate) *metafetch.CandidatePin { return markerPin() }, "", false},
	{"marker_replace", func(metafetch.MetadataCandidate) *metafetch.CandidatePin { return markerPin() }, replace, false},
	{"row", rowPin, "", true},
	{"row_replace_mode_ignored", rowPin, replace, true},
}

// The apply and its rename preflight are built from the plan: AllowRetired is
// set for a row pin and for nothing else, whatever the bulk mode.
func TestRetiredApply_AllowRetiredFollowsPinOrigin(t *testing.T) {
	for _, pc := range pinCases {
		t.Run(pc.name, func(t *testing.T) {
			raw, cand := retiredCandidateRows(t)
			svc := &fakeApplySvc{candidates: raw}
			out := applyCachedCandidateForBookTimed(context.Background(), svc, retiredBooks(liveShape), "b1", true, nil,
				metafetch.NewApplyPhaseTimings(), nil, pc.pin(cand), pc.mode)
			require.True(t, out.Applied, "control: a live book applies for every pin: %+v", out)
			require.Len(t, svc.applyOpts, 1)
			require.Len(t, svc.preflightOpts, 1)
			assert.Equal(t, pc.allow, svc.applyOpts[0].AllowRetired, "apply options %+v", svc.applyOpts[0])
			assert.Equal(t, pc.allow, svc.preflightOpts[0].AllowRetired, "preflight options %+v", svc.preflightOpts[0])
		})
	}
}

// B1, plan half: the review page's bulk "Replace existing" on a retired book
// is refused as book_retired, for the pin with a hash and the hashless marker.
func TestRetiredApply_BulkReplaceModeRefusesRetiredBookAtPlan(t *testing.T) {
	for _, shape := range retiredShapes {
		for _, pc := range pinCases {
			if pc.allow {
				continue
			}
			t.Run(shape.name+"/"+pc.name, func(t *testing.T) {
				raw, cand := retiredCandidateRows(t)
				svc := &fakeApplySvc{candidates: raw}
				out := applyCachedCandidateForBookTimed(context.Background(), svc, retiredBooks(shape), "b1", true, nil,
					metafetch.NewApplyPhaseTimings(), nil, pc.pin(cand), pc.mode)
				assert.False(t, out.Applied)
				assert.Equal(t, applySkipBookRetired, out.Reason)
				assert.ErrorIs(t, out.Err, metafetch.ErrBookRetired)
				assert.Empty(t, svc.appliedIDs)
			})
		}
	}
}

// retiredStore is the store the REAL metafetch apply sees: the book as the
// shape retires it, with one commit slot.
func retiredStore(shape retiredShape) (*database.MockStore, **database.Book) {
	book := retiredBook("b1", shape)
	var updated *database.Book
	store := &database.MockStore{
		GetBookByIDFunc: func(string) (*database.Book, error) { c := *book; return &c, nil },
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) {
			c := *b
			updated = &c
			return &c, nil
		},
	}
	return store, &updated
}

// realApplySvc plans on its embedded fake (whose books show b1 as live) and
// applies through the REAL metafetch apply over a store where b1 is retired:
// the book was retired after the plan read it and before the commit.
type realApplySvc struct {
	*fakeApplySvc
	real *metafetch.Service
}

func (r realApplySvc) ApplyMetadataCandidateWithOptions(id string, c metafetch.MetadataCandidate, fields []string, opts metafetch.ApplyOptions) (*metafetch.FetchMetadataResponse, error) {
	r.applyOpts = append(r.applyOpts, opts)
	return r.real.ApplyMetadataCandidateWithOptions(id, c, fields, opts)
}

// B1, backstop half: the plan passed (live), the book is retired before the
// commit. Every pin but a row pin is refused under the real apply's own
// checks, with no write, and reported as book_retired.
func TestRetiredApply_RetiredBetweenPlanAndCommitIsRefused(t *testing.T) {
	for _, shape := range retiredShapes {
		for _, pc := range pinCases {
			t.Run(shape.name+"/"+pc.name, func(t *testing.T) {
				raw, cand := retiredCandidateRows(t)
				store, updated := retiredStore(shape)
				svc := realApplySvc{fakeApplySvc: &fakeApplySvc{candidates: raw}, real: metafetch.NewService(store)}
				out := applyCachedCandidateForBookTimed(context.Background(), svc, retiredBooks(liveShape), "b1", false, nil,
					metafetch.NewApplyPhaseTimings(), nil, pc.pin(cand), pc.mode)
				if pc.allow {
					// One human click on one row: it writes, as it always did.
					assert.True(t, out.Applied, "the single-row apply must still write: %+v", out)
					assert.NotNil(t, *updated)
					return
				}
				assert.False(t, out.Applied, "a book retired after the plan was written: %+v", out)
				assert.Equal(t, applySkipBookRetired, out.Reason)
				assert.ErrorIs(t, out.Err, metafetch.ErrBookRetired)
				assert.Nil(t, *updated, "nothing may be committed onto the retired row")
			})
		}
	}
}

// Control for the test above: the same wiring on a book that stays live
// commits, for every pin, so the refusals are the retirement check's.
func TestRetiredApply_ControlLiveBookCommitsThroughRealApply(t *testing.T) {
	for _, pc := range pinCases {
		t.Run(pc.name, func(t *testing.T) {
			raw, cand := retiredCandidateRows(t)
			store, updated := retiredStore(liveShape)
			svc := realApplySvc{fakeApplySvc: &fakeApplySvc{candidates: raw}, real: metafetch.NewService(store)}
			out := applyCachedCandidateForBookTimed(context.Background(), svc, retiredBooks(liveShape), "b1", false, nil,
				metafetch.NewApplyPhaseTimings(), nil, pc.pin(cand), pc.mode)
			require.True(t, out.Applied, "%+v", out)
			assert.NotNil(t, *updated)
		})
	}
}

// An apply that returns ErrBookRetired after the plan passed (the backstop
// refused) is the skip book_retired, not apply_failed.
func TestRetiredApply_ApplyRefusalAfterPlanIsBookRetiredNotFailure(t *testing.T) {
	raw, _ := retiredCandidateRows(t)
	svc := &fakeApplySvc{candidates: raw, applyErr: fmt.Errorf("book b1: %w", metafetch.ErrBookRetired)}
	out := applyCachedCandidateForBookTimed(context.Background(), svc, retiredBooks(liveShape), "b1", false, nil,
		metafetch.NewApplyPhaseTimings(), nil, nil, "")
	assert.False(t, out.Applied)
	assert.Equal(t, applySkipBookRetired, out.Reason)
	assert.NotEqual(t, applySkipApplyFailed, out.Reason)
	assert.ErrorIs(t, out.Err, metafetch.ErrBookRetired)
}

// capturingReporter records the op's progress text (the summary is its last
// "complete: ..." message). RunItems calls it from worker goroutines.
type capturingReporter struct {
	sdReporter
	mu   sync.Mutex
	msgs []string
}

func (r *capturingReporter) UpdateProgress(_, _ int, msg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, msg)
	return nil
}

func (r *capturingReporter) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.msgs) == 0 {
		return ""
	}
	return r.msgs[len(r.msgs)-1]
}

var _ opsregistry.Reporter = (*capturingReporter)(nil)

// The cached batch apply op, end to end on a real store: a retired book among
// the ids is counted "retired 1" in the summary (not "apply failed"), and
// nothing is written to it.
func TestRetiredApply_BatchApplyOpCountsRetiredBooks(t *testing.T) {
	for _, shape := range retiredShapes {
		t.Run(shape.name, func(t *testing.T) {
			s, cleanup := setupTestServer(t)
			defer cleanup()
			store := s.storeForWiring()
			s.metadataFetchService = metafetch.NewService(store)
			s.scanStandDownGateOverride = &sdGate{}
			b := database.Book{Title: fixtureTitle, FilePath: "/lib/" + fixtureTitle + "/b.m4b"}
			shape.retire(&b)
			got, err := store.CreateBook(&b)
			require.NoError(t, err)
			_, cand := retiredCandidateRows(t)
			raw, err := json.Marshal(cand)
			require.NoError(t, err)
			require.NoError(t, store.PutMetadataCache(&database.MetadataCandidateCache{
				BookID: got.ID, Candidates: []json.RawMessage{raw}, SourceHash: "h", SearchFingerprint: "f"}))

			reg := capOpReg(t)
			require.NoError(t, s.RegisterBatchApplyFromCacheOp(reg))
			def, ok := reg.Def("metadata.batch-apply-cached")
			require.True(t, ok)
			params, err := json.Marshal(batchApplyOpParams{BookIDs: []string{got.ID}, Mode: replace})
			require.NoError(t, err)
			rep := &capturingReporter{sdReporter: sdReporter{id: "op-retired-count"}}
			require.NoError(t, def.Run(context.Background(), params, rep))

			summary := rep.last()
			assert.Contains(t, summary, "applied 0 of 1")
			assert.Contains(t, summary, "retired 1,", summary)
			assert.Contains(t, summary, "apply failed 0", summary)
			after, err := store.GetBookByID(got.ID)
			require.NoError(t, err)
			assert.Equal(t, fixtureTitle, after.Title)
			assert.Nil(t, after.MetadataReviewStatus, "nothing was recorded on the retired book")
		})
	}
}

// The op-result applies (batch-apply-candidates, metadata.apply-when-scanned)
// map a retired book to "skipped": at the plan, and when the backstop refuses
// after the plan passed.
func TestRetiredApply_OpResultApplyMapsRetiredToSkipped(t *testing.T) {
	for _, shape := range retiredShapes {
		t.Run(shape.name+"/refused_at_plan", func(t *testing.T) {
			books, cr := retiredOpResultFixture(shape.retire)
			store, updated := retiredStore(shape)
			s := &Server{metadataFetchService: metafetch.NewService(store)}
			o := s.applyOpResultCandidateLocked(context.Background(), books, "op-1", "b1", opResultRows(t, cr), nil)
			assert.True(t, o.skipped, "outcome %+v", o)
			assert.False(t, o.applied)
			assert.Empty(t, o.errMsg)
			assert.Nil(t, *updated)
		})
		t.Run(shape.name+"/refused_after_plan", func(t *testing.T) {
			books, cr := retiredOpResultFixture(nil) // live for the plan
			store, updated := retiredStore(shape)
			s := &Server{metadataFetchService: metafetch.NewService(store)}
			o := s.applyOpResultCandidateLocked(context.Background(), books, "op-1", "b1", opResultRows(t, cr), nil)
			assert.True(t, o.skipped, "outcome %+v", o)
			assert.False(t, o.applied)
			assert.Empty(t, o.errMsg, "a retired book is a skip, not an apply failure")
			assert.Nil(t, *updated, "nothing may be committed onto the retired row")
		})
	}
}

// opResultRows is the candidate-fetch op's stored result for b1.
func opResultRows(t *testing.T, cr CandidateResult) map[string]database.OperationResult {
	t.Helper()
	blob, err := json.Marshal(cr)
	require.NoError(t, err)
	return map[string]database.OperationResult{"b1": {BookID: "b1", ResultJSON: string(blob), Status: "matched"}}
}
