// file: internal/server/batch_apply_already_applied_test.go
// version: 1.0.1
// guid: 5b2e8c71-4d9a-4f06-a3e1-7c8d2f9b0e46
// last-edited: 2026-10-06

package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// appliedBooks is fakeBooks with b1 already applied under status.
func appliedBooks(status string) fakeBooks {
	dur := 36000
	return fakeBooks{"b1": {ID: "b1", Title: "A Title", FilePath: "/lib/An Author/A Title/A Title.m4b",
		Duration: &dur, MetadataReviewStatus: &status}}
}

// Since 2026-10-05 an apply keeps the book's cached candidates, so an applied
// book still has a top candidate. The cached bulk apply must not apply it
// again -- no apply, no file work, no iTunes write-back -- for any caller
// that is not a single-row approval: no pin, a review-page bulk pin (in
// either mode), the hashless bulk marker.
func TestApplyCachedCandidate_SkipsAlreadyAppliedBook(t *testing.T) {
	var cand metafetch.MetadataCandidate
	raw := oneCandidate(t)
	if err := json.Unmarshal(raw[0], &cand); err != nil {
		t.Fatal(err)
	}
	stale := cand
	stale.Title = "An Older Candidate"
	cases := []struct {
		name string
		pin  *metafetch.CandidatePin
		mode string
	}{
		{name: "no pin"},
		{name: "bulk pin, fill", pin: bulkPin(cand)},
		{name: "bulk pin, replace", pin: bulkPin(cand), mode: metafetch.BulkApplyModeReplace},
		// A bulk pin that no longer matches still reports already_applied:
		// the book is out of scope before staleness matters.
		{name: "stale bulk pin", pin: bulkPin(stale)},
		{name: "hashless bulk marker", pin: ownerMarker(), mode: metafetch.BulkApplyModeReplace},
	}
	for _, status := range []string{"matched", "audio_confirmed"} {
		for _, tc := range cases {
			t.Run(status+"/"+tc.name, func(t *testing.T) {
				svc := &fakeApplySvc{candidates: raw}
				itunes := &fakeITunes{}
				out := applyCachedCandidateForBookTimed(svc, appliedBooks(status), itunes, "b1", true, nil,
					metafetch.NewApplyPhaseTimings(), nil, tc.pin, tc.mode)
				if out.Applied || len(svc.appliedIDs) != 0 || len(svc.finishCalls) != 0 || len(svc.preflightIDs) != 0 || len(itunes.ids) != 0 {
					t.Fatalf("an applied book was applied again: outcome %+v applied %v finish %d preflight %v itunes %v",
						out, svc.appliedIDs, len(svc.finishCalls), svc.preflightIDs, itunes.ids)
				}
				if out.Reason != applySkipAlreadyApplied {
					t.Fatalf("reason = %q, want %q", out.Reason, applySkipAlreadyApplied)
				}
				if out.Err == nil || !strings.Contains(out.Err.Error(), status) {
					t.Errorf("err = %v, want it to name the status %q", out.Err, status)
				}
			})
		}
	}
}

// A single-row approval is the owner choosing to re-apply this candidate to
// an applied book: it applies (and overwrites, as every row approval does).
// A row pin that no longer matches is still refused as stale.
func TestApplyCachedCandidate_RowApprovalReappliesAppliedBook(t *testing.T) {
	raw := oneCandidate(t)
	var cand metafetch.MetadataCandidate
	if err := json.Unmarshal(raw[0], &cand); err != nil {
		t.Fatal(err)
	}
	svc := &fakeApplySvc{candidates: raw}
	out := applyCachedCandidateForBookTimed(svc, appliedBooks("matched"), &fakeITunes{}, "b1", false, nil,
		metafetch.NewApplyPhaseTimings(), nil, rowPin(cand), "")
	if !out.Applied || len(svc.appliedIDs) != 1 {
		t.Fatalf("the owner's row approval of an applied book was refused: outcome %+v", out)
	}
	if svc.applyOpts[0].FillOnly {
		t.Errorf("a row approval must overwrite: %+v", svc.applyOpts[0])
	}

	other := cand
	other.Title = "Some Other Record"
	stale := &fakeApplySvc{candidates: raw}
	out = applyCachedCandidateForBookTimed(stale, appliedBooks("matched"), &fakeITunes{}, "b1", false, nil,
		metafetch.NewApplyPhaseTimings(), nil, rowPin(other), "")
	if out.Applied || out.Reason != applySkipStaleCandidate {
		t.Fatalf("stale row pin on an applied book: outcome %+v, want %s", out, applySkipStaleCandidate)
	}
}

// A book nobody applied, or marked no match, keeps its old path: the skip
// keys on the applied statuses only.
func TestPlanCachedApply_AlreadyAppliedOnlyForAppliedStatuses(t *testing.T) {
	for _, status := range []string{"", "pending", "no_match"} {
		books := appliedBooks(status)
		if status == "" {
			books["b1"].MetadataReviewStatus = nil
		}
		p := planCachedApply(&fakeApplySvc{candidates: oneCandidate(t)}, books, "b1", nil, nil)
		if p.Reason == applySkipAlreadyApplied {
			t.Errorf("status %q: skipped as already applied", status)
		}
	}
}

// The dry run reports an applied book as skipped / already_applied -- the
// choice the apply makes -- never as "would apply", and counts it in the
// summary message.
func TestBulkApplyPreview_ReportsAlreadyApplied(t *testing.T) {
	svc := fakePreviewSvc{&fakeApplySvc{candidates: oneCandidate(t)}}
	plan := planCachedApply(svc, appliedBooks("matched"), "b1", nil, nil)
	if excludedFromPreview(plan) {
		t.Fatalf("an applied book must get a row, not be left out: %+v", plan)
	}
	row := previewBulkApplyRow(svc, "b1", plan, true, false)
	if row.Verdict != previewVerdictSkipped || row.Reason != applySkipAlreadyApplied {
		t.Fatalf("row verdict=%q reason=%q, want skipped/%s", row.Verdict, row.Reason, applySkipAlreadyApplied)
	}
	if len(svc.previewOpts) != 0 {
		t.Fatalf("a skipped row must not be previewed as an apply: %+v", svc.previewOpts)
	}
}

// perBookSvc gives each book its own cached candidates.
type perBookSvc struct {
	listingSvc
	byBook map[string][]json.RawMessage
}

func (s perBookSvc) GetCachedCandidates(id string) (*metafetch.MetadataCandidateCache, bool, error) {
	raw := s.byBook[id]
	if len(raw) == 0 {
		return nil, false, nil
	}
	return &metafetch.MetadataCandidateCache{BookID: id, Candidates: raw}, true, nil
}

// An applied book claims the ASIN it holds, not its top cached candidate: it
// owns that record, and the apply skips it, so its top candidate is never
// taken. b2 is CD2 of the book in b1's folder, applied, holding the book's
// ASIN; its top cached candidate is some other record. b1 taking the
// whole-book candidate with that ASIN must still be refused as partial_book.
func TestClaimIndex_AppliedBookClaimsItsOwnASIN(t *testing.T) {
	moon := moondust()
	otherRec := metafetch.MetadataCandidate{Title: "Earthlight", Author: "Arthur C. Clarke", ASIN: "B0TESTEARTH", Score: 0.99}
	cd2 := func(status, asin string) database.Book {
		b := database.Book{ID: "b2", Title: "A Fall of Moondust CD2", FilePath: "/lib/Arthur C. Clarke/A Fall of Moondust/CD2"}
		if status != "" {
			b.MetadataReviewStatus = &status
		}
		if asin != "" {
			b.ASIN = &asin
		}
		return b
	}
	cases := []struct {
		name    string
		sibling database.Book
		blocks  bool
	}{
		{name: "applied, holds the ASIN", sibling: cd2("matched", "B0TESTMOON"), blocks: true},
		{name: "audio-confirmed, holds the ASIN", sibling: cd2("audio_confirmed", "b0testmoon "), blocks: true},
		// Controls: the same sibling claims its top candidate (another
		// record) when it is not applied, or applied with no ASIN to own.
		{name: "not applied, holds the ASIN", sibling: cd2("", "B0TESTMOON")},
		{name: "applied, no ASIN", sibling: cd2("matched", "")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			books := moondustBooks(tc.sibling)
			svc := perBookSvc{
				listingSvc: listingSvc{fakeApplySvc: &fakeApplySvc{candidates: candidateJSON(t, moon)}, ids: []string{"b1", "b2"}},
				byBook:     map[string][]json.RawMessage{"b1": candidateJSON(t, moon), "b2": candidateJSON(t, otherRec)},
			}
			claims, err := cachedClaimIndex(context.Background(), svc, books)
			if err != nil {
				t.Fatal(err)
			}
			p := planCachedApply(svc, books, "b1", claims, nil)
			blocked := p.Gate != nil && p.Gate.Evidence.Reason == applygate.ReasonPartialBook
			if blocked != tc.blocks {
				t.Fatalf("partial_book blocked=%v, want %v (reason=%q gate=%+v)", blocked, tc.blocks, p.Reason, p.Gate)
			}
		})
	}
}

// previewTestReporter records the progress messages a preview run writes.
type previewTestReporter struct {
	mu   sync.Mutex
	msgs []string
}

func (r *previewTestReporter) UpdateProgress(_, _ int, m string) error {
	r.mu.Lock()
	r.msgs = append(r.msgs, m)
	r.mu.Unlock()
	return nil
}
func (r *previewTestReporter) Log(slog.Level, string, ...slog.Attr) error { return nil }
func (r *previewTestReporter) Logger() *slog.Logger                       { return slog.Default() }
func (r *previewTestReporter) Checkpoint(any) error                       { return nil }
func (r *previewTestReporter) IsCanceled() bool                           { return false }
func (r *previewTestReporter) Trigger(context.Context, string, any) error { return nil }
func (r *previewTestReporter) SetCurrentItem(string)                      {}
func (r *previewTestReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, opsregistry.Reporter) error) error {
	return fn(ctx, r)
}

// previewResults is an in-memory previewResultStore.
type previewResults struct {
	mu   sync.Mutex
	rows []database.OperationResult
}

func (p *previewResults) CreateOperationResult(r *database.OperationResult) error {
	p.mu.Lock()
	p.rows = append(p.rows, *r)
	p.mu.Unlock()
	return nil
}
func (p *previewResults) GetOperationResults(string) ([]database.OperationResult, error) {
	return p.rows, nil
}

// listingPreviewSvc is fakePreviewSvc that lists ids as the cached books.
type listingPreviewSvc struct {
	fakePreviewSvc
	ids []string
}

func (l listingPreviewSvc) ListCachedSummaries(context.Context) ([]metafetch.MetadataCacheSummary, error) {
	out := make([]metafetch.MetadataCacheSummary, 0, len(l.ids))
	for _, id := range l.ids {
		out = append(out, metafetch.MetadataCacheSummary{BookID: id, CandidateCount: 1})
	}
	return out, nil
}

// The all_cached dry run -- "everything that would be applied" -- now lists
// every applied book too, since an apply keeps its candidates. Each comes back
// as a skipped / already_applied row and is counted in the run's message;
// it is never counted as would-apply.
func TestRunBulkApplyPreview_AllCachedReportsAlreadyApplied(t *testing.T) {
	books := appliedBooks("matched")
	dur := 36000
	books["b2"] = &database.Book{ID: "b2", Title: "A Title", FilePath: "/lib/An Author/Other/A Title.m4b", Duration: &dur}
	svc := listingPreviewSvc{fakePreviewSvc: fakePreviewSvc{&fakeApplySvc{candidates: oneCandidate(t)}}, ids: []string{"b1", "b2"}}
	rep := &previewTestReporter{}
	res := &previewResults{}
	if err := runBulkApplyPreview(context.Background(), rep, svc, books, res, "op-preview", bulkApplyPreviewParams{AllCached: true}); err != nil {
		t.Fatalf("runBulkApplyPreview: %v", err)
	}
	verdicts := map[string]bulkApplyPreviewRow{}
	for _, r := range res.rows {
		if r.BookID == previewIndexRowID {
			continue
		}
		var row bulkApplyPreviewRow
		if err := json.Unmarshal([]byte(r.ResultJSON), &row); err != nil {
			t.Fatal(err)
		}
		verdicts[r.BookID] = row
	}
	if v := verdicts["b1"]; v.Verdict != previewVerdictSkipped || v.Reason != applySkipAlreadyApplied {
		t.Fatalf("applied book row = %s/%s, want skipped/%s", v.Verdict, v.Reason, applySkipAlreadyApplied)
	}
	if v := verdicts["b2"]; v.Verdict != previewVerdictApply {
		t.Fatalf("plain book row = %s/%s (%s), want apply", v.Verdict, v.Reason, v.Detail)
	}
	last := rep.msgs[len(rep.msgs)-1]
	if !strings.Contains(last, "1 would apply") || !strings.Contains(last, "skipped as already applied: 1") {
		t.Fatalf("summary %q must count the applied book as skipped, not would-apply", last)
	}
}
