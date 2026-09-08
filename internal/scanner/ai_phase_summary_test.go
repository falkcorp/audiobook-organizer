// file: internal/scanner/ai_phase_summary_test.go
// version: 1.0.0
// guid: 5b1e7c40-9a3f-4d28-8e16-c47f0b93a2d5
// last-edited: 2026-09-08

package scanner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// The exact shape from the report: dozens of consecutive "AI Filename Parsing"
// rows reading "0/5 book(s) parsed in 0/1 batches; 1 batch failure(s), 0 save
// failure(s)" -- every one of them COMPLETED with a full green bar.
//
// One batch, that batch fails, and it fails with something retryable. Neither
// AbortedPermanent (needs isPermanentAIFailure) nor AbortedThreshold (needs 3)
// is set, so Aborted() is false and the operation returned nil.
func TestAIPhaseSummary_SingleFailedBatchIsAFailure(t *testing.T) {
	s := AIPhaseSummary{
		BooksNominated: 5,
		BatchesTotal:   1,
		BatchesOK:      0,
		BatchesFailed:  1,
		BooksParsed:    0,
	}
	if s.Aborted() {
		t.Fatal("Aborted() is true: this test no longer reproduces the reported shape")
	}
	if !s.Failed() {
		t.Error("a run whose only batch failed reported success -- this is the defect: " +
			"0 books parsed because the batch died is not the same as 0 books parsed " +
			"because nothing needed changing")
	}
}

// The converse, and the reason Failed() is not just "BooksParsed == 0": a
// library where every candidate was already filled in by another path
// legitimately parses nothing. Making that red trains everyone to ignore the
// status, which is the failure mode this whole change is trying to undo.
func TestAIPhaseSummary_HealthyNoOpIsNotAFailure(t *testing.T) {
	cases := []struct {
		name string
		s    AIPhaseSummary
	}{
		{"nothing nominated", AIPhaseSummary{BooksNominated: 0, BatchesTotal: 0}},
		{"parsed nothing, no failures", AIPhaseSummary{
			BooksNominated: 5, BatchesTotal: 1, BatchesOK: 1, BooksParsed: 0,
		}},
		{"parsed everything", AIPhaseSummary{
			BooksNominated: 5, BatchesTotal: 1, BatchesOK: 1, BooksParsed: 5,
		}},
		{"AI switched off", AIPhaseSummary{Disabled: true, BooksNominated: 5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.s.Failed() {
				t.Errorf("Failed() on a healthy run: %s", tc.s)
			}
		})
	}
}

// Disabled beats every other field. A run that never happened cannot have
// failed, and the counters are meaningless when the phase was skipped.
func TestAIPhaseSummary_DisabledIsNeverAFailure(t *testing.T) {
	s := AIPhaseSummary{Disabled: true, BooksNominated: 5, BatchesFailed: 3, SavesFailed: 2}
	if s.Failed() {
		t.Error("a run that never ran was reported as failed")
	}
}

func TestAIPhaseSummary_FailureConditions(t *testing.T) {
	cases := []struct {
		name string
		s    AIPhaseSummary
	}{
		{"one failed batch", AIPhaseSummary{BatchesTotal: 1, BatchesFailed: 1}},
		{"partial success", AIPhaseSummary{BatchesTotal: 10, BatchesOK: 9, BatchesFailed: 1, BooksParsed: 180}},
		{"permanent abort", AIPhaseSummary{BatchesTotal: 10, AbortedPermanent: true}},
		{"threshold abort", AIPhaseSummary{BatchesTotal: 10, BatchesFailed: 3, AbortedThreshold: true}},
		// The LLM answered and the answer was thrown away: those books keep
		// their filename-derived metadata exactly as if the batch had failed.
		{"lost save", AIPhaseSummary{BatchesTotal: 1, BatchesOK: 1, BooksParsed: 5, SavesFailed: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.s.Failed() {
				t.Errorf("Failed() false for %q: %s", tc.name, tc.s)
			}
		})
	}
}

// B: the message has to say what happened. Asserting on the RENDERED string
// rather than on the struct, because the string is what a person reads on the
// Activity page -- a summary that carries the detail in a field nothing prints
// is the same defect in a new place.
func TestAIPhaseSummary_StringNamesTheCause(t *testing.T) {
	s := AIPhaseSummary{
		BooksNominated: 5,
		BatchesTotal:   1,
		BatchesFailed:  1,
		BatchFailures: []AIBatchFailure{{
			Batch:     1,
			Total:     1,
			Filenames: []string{"a.m4b", "b.m4b"},
			Err:       "context deadline exceeded",
		}},
	}
	got := s.String()
	if !strings.Contains(got, "context deadline exceeded") {
		t.Errorf("summary does not say WHY the batch failed:\n%s", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("summary must stay one line -- it is the operation's progress message:\n%s", got)
	}
}

func TestAIPhaseSummary_FailureDetailsNameTheBooks(t *testing.T) {
	s := AIPhaseSummary{
		BooksNominated: 5,
		BatchesTotal:   1,
		BatchesFailed:  1,
		BatchFailures: []AIBatchFailure{{
			Batch:     1,
			Total:     1,
			Filenames: []string{"Dune - Herbert.m4b", "Neuromancer.m4b"},
			Err:       "429 rate limited",
		}},
		SavesFailed:  1,
		SaveFailures: []AISaveFailure{{Path: "/books/x.m4b", Err: "row not found"}},
	}
	lines := strings.Join(s.FailureDetails(), "\n")
	for _, want := range []string{"Dune - Herbert.m4b", "Neuromancer.m4b", "429 rate limited", "/books/x.m4b", "row not found"} {
		if !strings.Contains(lines, want) {
			t.Errorf("failure details omit %q:\n%s", want, lines)
		}
	}
}

// A capped sample must not read as the total. The counters are the census.
func TestAIPhaseSummary_FailureDetailsAdmitTruncation(t *testing.T) {
	s := AIPhaseSummary{
		BatchesTotal:  30,
		BatchesFailed: 12,
		BatchFailures: make([]AIBatchFailure, maxRecordedBatchFailures),
		SavesFailed:   25,
		SaveFailures:  make([]AISaveFailure, maxRecordedSaveFailures),
	}
	lines := strings.Join(s.FailureDetails(), "\n")
	if !strings.Contains(lines, fmt.Sprintf("%d further batch failure(s)", 12-maxRecordedBatchFailures)) {
		t.Errorf("recorded batch failures read as the total:\n%s", lines)
	}
	if !strings.Contains(lines, fmt.Sprintf("%d further save failure(s)", 25-maxRecordedSaveFailures)) {
		t.Errorf("recorded save failures read as the total:\n%s", lines)
	}
}

func TestAIPhaseSummary_FailureDetailsEmptyOnAHealthyRun(t *testing.T) {
	s := AIPhaseSummary{BooksNominated: 5, BatchesTotal: 1, BatchesOK: 1, BooksParsed: 5}
	if got := s.FailureDetails(); len(got) != 0 {
		t.Errorf("healthy run produced failure lines: %v", got)
	}
}

// The counter defect found while fixing A: failures.Add sat BELOW the
// isPermanentAIFailure early return, so the worst failure there is -- a revoked
// key, an exhausted quota, every batch dead -- returned BatchesFailed == 0 and
// the summary printed "0 batch failure(s)".
//
// One batch, one permanent error, so the run cannot reach the threshold by any
// other path.
func TestRunAIBatchPhase_PermanentFailureIsCounted(t *testing.T) {
	books, cands := makeCandidates(5)
	f := &fakeAIParser{err: errors.New("insufficient_quota: credit balance exhausted")}

	s := runAIBatchPhase(context.Background(), f, books, cands, logger.New("test"), saveBookAndReportPath)

	if !s.AbortedPermanent {
		t.Fatal("the permanent-failure path did not fire; this test asserts nothing")
	}
	if s.BatchesFailed != 1 {
		t.Errorf("BatchesFailed = %d, want 1: a run that failed every batch reported "+
			"zero batch failures because the counter sat below the permanent-failure "+
			"early return", s.BatchesFailed)
	}
	if !s.Failed() {
		t.Error("a permanently aborted run did not report as failed")
	}
	if strings.Contains(s.String(), "0 batch failure(s)") {
		t.Errorf("summary still claims zero failures:\n%s", s)
	}
}

// The abort policy must be UNCHANGED by moving the counter. Three transient
// failures still trip the threshold, and the count is exact -- an increment
// that fired twice per error would trip it after two.
func TestRunAIBatchPhase_ThresholdPolicyUnchanged(t *testing.T) {
	books, cands := makeCandidates(20 * 40)
	f := &fakeAIParser{err: errors.New("connection reset by peer")}

	s := runAIBatchPhase(context.Background(), f, books, cands, logger.New("test"), saveBookAndReportPath)

	if !s.AbortedThreshold {
		t.Fatalf("threshold abort did not fire: %s", s)
	}
	// aiBatchWorkers batches can be in flight when the third failure trips the
	// abort, so the count lands in [maxTotalFailures, maxTotalFailures+workers].
	// The assertion that matters is that it is not BELOW the threshold, which
	// is what a double increment would produce.
	if s.BatchesFailed < maxTotalFailures {
		t.Errorf("BatchesFailed = %d, below the threshold of %d: the counter is being "+
			"incremented more than once per failed batch", s.BatchesFailed, maxTotalFailures)
	}
	if len(s.BatchFailures) == 0 {
		t.Error("no failure detail captured for an aborted run")
	}
}

// The detail slices are written from worker goroutines while the counters are
// atomics. -race only proves anything if at least two batches fail
// CONCURRENTLY, so this fails every batch with aiBatchWorkers > 1 and holds
// each call open long enough that the workers overlap. Run under `go test
// -race`; without it this is only an assertion about the caps.
func TestRunAIBatchPhase_ConcurrentFailureCaptureIsRaceFree(t *testing.T) {
	if aiBatchWorkers < 2 {
		t.Skipf("aiBatchWorkers is %d: this test cannot exercise concurrent capture", aiBatchWorkers)
	}
	books, cands := makeCandidates(20 * 40)
	f := newBlockingFailParser(aiBatchWorkers)

	s := runAIBatchPhase(context.Background(), f, books, cands, logger.New("test"), saveBookAndReportPath)

	if f.maxInFlight() < 2 {
		t.Fatalf("max concurrent failing batches was %d: the failure paths never "+
			"overlapped, so -race proved nothing here", f.maxInFlight())
	}

	if len(s.BatchFailures) > maxRecordedBatchFailures {
		t.Errorf("captured %d batch failures, cap is %d", len(s.BatchFailures), maxRecordedBatchFailures)
	}
	if len(s.BatchFailures) == 0 {
		t.Fatal("no failures captured from a backend that failed every call")
	}
	for _, bf := range s.BatchFailures {
		if len(bf.Filenames) == 0 {
			t.Error("a captured failure names no books, which is the defect B fixes")
		}
		if len(bf.Filenames) > maxRecordedFilenames {
			t.Errorf("captured %d filenames, cap is %d", len(bf.Filenames), maxRecordedFilenames)
		}
	}
}

// blockingFailParser fails every call, and holds the first `hold` callers
// inside ParseBatch until all of them have arrived. That is what makes the
// failure paths provably concurrent: without the barrier the workers can
// serialize by luck and -race observes nothing.
type blockingFailParser struct {
	mu       sync.Mutex
	inFlight int
	maxSeen  int
	waiting  int
	hold     int
	released bool
	cond     *sync.Cond
}

func newBlockingFailParser(hold int) *blockingFailParser {
	p := &blockingFailParser{hold: hold}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *blockingFailParser) maxInFlight() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxSeen
}

func (p *blockingFailParser) ParseBatch(_ context.Context, _ []string) ([]*ai.ParsedMetadata, error) {
	p.mu.Lock()
	p.inFlight++
	if p.inFlight > p.maxSeen {
		p.maxSeen = p.inFlight
	}
	if !p.released {
		p.waiting++
		if p.waiting >= p.hold {
			p.released = true
			p.cond.Broadcast()
		}
		for !p.released {
			p.cond.Wait()
		}
	}
	p.inFlight--
	p.mu.Unlock()
	return nil, errors.New("connection reset by peer")
}
