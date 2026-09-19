// file: internal/aiscan/pipeline_durable_test.go
// version: 1.0.0
// guid: e3808e03-3f51-4f6d-83b8-9621db7b7f15
// last-edited: 2026-09-19

package aiscan

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// These tests simulate a process restart by abandoning one PipelineManager
// mid-flight (its goroutines stay blocked forever, exactly as if the process
// had died) and running the same scan with a SECOND manager over the same
// on-disk scan store. Nothing in memory crosses the "restart"; only the store
// and the fake OpenAI account (which, like the real one, outlives us) do.

// --- fakes -------------------------------------------------------------------

// fakeMainStore serves a fixed author list with names far enough apart that
// the heuristic grouping finds no duplicate groups, so groups_scan completes
// without an LLM call and every LLM call in a test belongs to full_scan.
type fakeMainStore struct{ authors []database.Author }

func newFakeMainStore(n int) *fakeMainStore {
	names := []string{
		"Aldous Huxley", "Brandon Sanderson", "Cormac McCarthy", "Donna Tartt",
		"Emily Bronte", "Fyodor Dostoevsky", "Gabriel Garcia Marquez", "Haruki Murakami",
		"Isaac Asimov", "Jorge Luis Borges",
	}
	s := &fakeMainStore{}
	for i := range n {
		s.authors = append(s.authors, database.Author{ID: i + 1, Name: names[i]})
	}
	return s
}

func (s *fakeMainStore) GetAllAuthors() ([]database.Author, error) { return s.authors, nil }
func (s *fakeMainStore) GetAuthorByID(id int) (*database.Author, error) {
	for i := range s.authors {
		if s.authors[i].ID == id {
			return &s.authors[i], nil
		}
	}
	return nil, nil
}
func (s *fakeMainStore) GetAllAuthorBookCounts() (map[int]int, error) { return map[int]int{}, nil }
func (s *fakeMainStore) GetBooksByAuthorIDWithRoleCore(int) ([]database.BookCore, error) {
	return nil, nil
}

// fakeAccount is the OpenAI side: batches it holds survive a local restart.
type fakeAccount struct {
	mu      sync.Mutex
	nextID  int
	batches map[string]*fakeBatch
}

type fakeBatch struct {
	id, phase, status string
	owner             map[string]string
	inputs            []ai.AuthorDiscoveryInput
}

func newFakeAccount() *fakeAccount { return &fakeAccount{batches: map[string]*fakeBatch{}} }

func (a *fakeAccount) setAllStatus(status string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, b := range a.batches {
		b.status = status
	}
}

func (a *fakeAccount) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.batches)
}

// fakeLLM is one process's client. Counters are per process.
type fakeLLM struct {
	acct *fakeAccount

	mu            sync.Mutex
	discoverCalls [][]int // author ids sent per realtime call
	createCalls   int
	downloads     int

	// blockDiscoverAt, when >= 0, makes that realtime call (0-based) hang
	// forever — the process "dies" inside it.
	blockDiscoverAt int
	// blockAfterCreate makes CreateBatch* register the batch with OpenAI and
	// then hang — the process "dies" before recording the batch id.
	blockAfterCreate bool
	entered          chan struct{}
}

func newFakeLLM(acct *fakeAccount) *fakeLLM {
	return &fakeLLM{acct: acct, blockDiscoverAt: -1, entered: make(chan struct{}, 1)}
}

// suggestionsFor is the deterministic "model output": one high-confidence
// suggestion per author, so enrichment never needs a second LLM call.
func suggestionsFor(inputs []ai.AuthorDiscoveryInput) []ai.AuthorDiscoverySuggestion {
	out := make([]ai.AuthorDiscoverySuggestion, 0, len(inputs))
	for _, in := range inputs {
		out = append(out, ai.AuthorDiscoverySuggestion{
			AuthorIDs: []int{in.ID}, Action: "rename", CanonicalName: in.Name,
			Reason: "fake", Confidence: "high",
		})
	}
	return out
}

func (f *fakeLLM) ReviewAuthorDuplicates(context.Context, []ai.AuthorDedupInput) ([]ai.AuthorDedupSuggestion, error) {
	return nil, fmt.Errorf("unexpected groups review: the fixture has no duplicate groups")
}

func (f *fakeLLM) DiscoverAuthorDuplicates(_ context.Context, inputs []ai.AuthorDiscoveryInput) ([]ai.AuthorDiscoverySuggestion, error) {
	f.mu.Lock()
	call := len(f.discoverCalls)
	ids := make([]int, 0, len(inputs))
	for _, in := range inputs {
		ids = append(ids, in.ID)
	}
	f.discoverCalls = append(f.discoverCalls, ids)
	f.mu.Unlock()
	if call == f.blockDiscoverAt {
		f.entered <- struct{}{}
		select {} // the process dies here
	}
	return suggestionsFor(inputs), nil
}

func (f *fakeLLM) createBatch(phase string, owner map[string]string, inputs []ai.AuthorDiscoveryInput) (string, error) {
	f.mu.Lock()
	f.createCalls++
	f.mu.Unlock()
	f.acct.mu.Lock()
	f.acct.nextID++
	id := fmt.Sprintf("batch_%d", f.acct.nextID)
	f.acct.batches[id] = &fakeBatch{id: id, phase: phase, owner: owner, status: "in_progress", inputs: inputs}
	f.acct.mu.Unlock()
	if f.blockAfterCreate {
		f.entered <- struct{}{}
		select {} // OpenAI holds the batch; we die before recording its id
	}
	return id, nil
}

func (f *fakeLLM) CreateBatchAuthorReview(context.Context, []ai.AuthorDedupInput, map[string]string) (string, error) {
	return "", fmt.Errorf("unexpected groups batch: the fixture has no duplicate groups")
}

func (f *fakeLLM) CreateBatchAuthorDedup(_ context.Context, inputs []ai.AuthorDiscoveryInput, owner map[string]string) (string, error) {
	return f.createBatch("full_scan", owner, inputs)
}

func (f *fakeLLM) CheckBatchStatus(_ context.Context, batchID string) (string, string, error) {
	f.acct.mu.Lock()
	defer f.acct.mu.Unlock()
	b, ok := f.acct.batches[batchID]
	if !ok {
		return "", "", fmt.Errorf("no batch %s", batchID)
	}
	return b.status, "file_" + batchID, nil
}

func (f *fakeLLM) CancelBatch(context.Context, string) error { return nil }

func (f *fakeLLM) DownloadBatchGroupsResults(context.Context, string) ([]ai.AuthorDedupSuggestion, error) {
	return nil, fmt.Errorf("unexpected groups download")
}

func (f *fakeLLM) DownloadBatchResults(_ context.Context, outputFileID string) ([]ai.AuthorDiscoverySuggestion, error) {
	f.mu.Lock()
	f.downloads++
	f.mu.Unlock()
	f.acct.mu.Lock()
	defer f.acct.mu.Unlock()
	b := f.acct.batches[outputFileID[len("file_"):]]
	return suggestionsFor(b.inputs), nil
}

func (f *fakeLLM) counts() (discover, create, downloads int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.discoverCalls), f.createCalls, f.downloads
}

// finderLLM adds the batch-metadata lookup, matching the scan_id / scan_phase
// owner keys the pipeline wrote at CreateBatch time. A plain *fakeLLM
// deliberately lacks it.
type finderLLM struct{ *fakeLLM }

func (f finderLLM) FindScanBatch(_ context.Context, scanID int, phaseType string) (string, bool, error) {
	f.acct.mu.Lock()
	defer f.acct.mu.Unlock()
	for id, b := range f.acct.batches {
		if b.owner[ai.BatchMetaScanID] == strconv.Itoa(scanID) && b.owner[ai.BatchMetaScanPhase] == phaseType {
			return id, true, nil
		}
	}
	return "", false, nil
}

// --- helpers -----------------------------------------------------------------

func newScanStore(t *testing.T) *database.AIScanStore {
	t.Helper()
	s, err := database.NewAIScanStore(filepath.Join(t.TempDir(), "aiscan.db"))
	require.NoError(t, err)
	// No Close: an abandoned "dead process" manager may still hold goroutines
	// that reference the store. The temp dir is removed after the test.
	return s
}

func waitPhase(t *testing.T, s *database.AIScanStore, scanID int, phase, status string) {
	t.Helper()
	require.Eventually(t, func() bool {
		p, err := s.GetPhase(scanID, phase)
		return err == nil && p != nil && p.Status == status
	}, 5*time.Second, 5*time.Millisecond, "phase %s never reached %s", phase, status)
}

func runAsync(pm *PipelineManager, scanID int) <-chan error {
	out := make(chan error, 1)
	go func() { out <- pm.RunScan(context.Background(), scanID, nil) }()
	return out
}

func awaitRun(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("RunScan never returned")
		return nil
	}
}

// resultKeys is a scan's results reduced to what a user would apply.
func resultKeys(t *testing.T, s *database.AIScanStore, scanID int) []string {
	t.Helper()
	rs, err := s.GetScanResults(scanID)
	require.NoError(t, err)
	keys := make([]string, 0, len(rs))
	for _, r := range rs {
		keys = append(keys, fmt.Sprintf("%s|%s|%s|%v", r.Agreement, r.Suggestion.Action, r.Suggestion.CanonicalName, r.Suggestion.AuthorIDs))
	}
	sort.Strings(keys)
	return keys
}

func withChunkSize(t *testing.T, n int) {
	t.Helper()
	old := fullScanChunkSize
	fullScanChunkSize = n
	t.Cleanup(func() { fullScanChunkSize = old })
}

// --- realtime ----------------------------------------------------------------

// TestRealtimeScanResumesFromLastFinishedChunk: 7 authors in chunks of 2 is 4
// LLM calls. The first process dies inside call 3 (index 2), after chunks 0
// and 1 returned. The resumed run must request ONLY chunks 2 and 3 and end with
// exactly the results a clean run produces. Before the fix the resume was
// resumeImpossible: the scan failed and chunks 0-1 were thrown away.
func TestRealtimeScanResumesFromLastFinishedChunk(t *testing.T) {
	withChunkSize(t, 2)
	main := newFakeMainStore(7)

	// Reference: a clean, uninterrupted run.
	cleanStore := newScanStore(t)
	cleanLLM := newFakeLLM(newFakeAccount())
	cleanPM := NewPipelineManager(cleanStore, main, cleanLLM)
	cleanScan, err := cleanPM.CreateScan("realtime")
	require.NoError(t, err)
	require.NoError(t, awaitRun(t, runAsync(cleanPM, cleanScan.ID)))
	want := resultKeys(t, cleanStore, cleanScan.ID)
	require.Len(t, want, 7)

	// Process 1 dies inside chunk 2.
	store := newScanStore(t)
	llm1 := newFakeLLM(newFakeAccount())
	llm1.blockDiscoverAt = 2
	pm1 := NewPipelineManager(store, main, llm1)
	scan, err := pm1.CreateScan("realtime")
	require.NoError(t, err)
	_ = runAsync(pm1, scan.ID)
	<-llm1.entered
	waitPhase(t, store, scan.ID, "groups_enrich", "complete")

	// Process 2 resumes.
	llm2 := newFakeLLM(newFakeAccount())
	pm2 := NewPipelineManager(store, main, llm2)
	require.NoError(t, awaitRun(t, runAsync(pm2, scan.ID)), "an interrupted realtime scan must resume, not fail")

	llm2.mu.Lock()
	calls := llm2.discoverCalls
	llm2.mu.Unlock()
	require.Equal(t, [][]int{{5, 6}, {7}}, calls, "only the chunks the dead process never finished are re-requested")
	require.Equal(t, want, resultKeys(t, store, scan.ID), "resumed results must equal a clean run's")
}

// --- batch -------------------------------------------------------------------

// TestBatchScanCollectedOnceAcrossRestart: the process that submitted the batch
// dies; a second one re-attaches, and the batch is then collected by several
// pollers at once (the poller's author_dedup handler, the attached op's
// heartbeat, a second completed-batch dispatch). Results must be downloaded and
// applied exactly once.
func TestBatchScanCollectedOnceAcrossRestart(t *testing.T) {
	main := newFakeMainStore(7)
	store := newScanStore(t)
	acct := newFakeAccount()

	pm1 := NewPipelineManager(store, main, newFakeLLM(acct))
	scan, err := pm1.CreateScan("batch")
	require.NoError(t, err)
	_ = runAsync(pm1, scan.ID)
	waitPhase(t, store, scan.ID, "full_scan", "submitted")
	// pm1 is abandoned here: the "process" is gone.

	llm2 := newFakeLLM(acct)
	pm2 := NewPipelineManager(store, main, llm2)
	run := runAsync(pm2, scan.ID)

	acct.setAllStatus("completed")
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pm2.PollBatchPhases(context.Background())
		}()
	}
	wg.Wait()
	require.NoError(t, awaitRun(t, run))
	pm2.PollBatchPhases(context.Background()) // a late re-dispatch after completion

	_, creates, downloads := llm2.counts()
	require.Equal(t, 0, creates, "the restarted process must not submit a second batch")
	require.Equal(t, 1, downloads, "a completed batch is downloaded and applied once")
	require.Equal(t, 1, acct.count())
	require.Len(t, resultKeys(t, store, scan.ID), 7, "one result per suggestion, no duplicates")
}

// TestBatchCrashAfterCreateReattaches: the process dies after OpenAI accepted
// the batch but before the phase row learned its id. The phase must have been
// marked "submitting" BEFORE CreateBatch, so the resume looks the batch up and
// re-attaches instead of launching (and paying for) a second one.
func TestBatchCrashAfterCreateReattaches(t *testing.T) {
	main := newFakeMainStore(7)
	store := newScanStore(t)
	acct := newFakeAccount()

	llm1 := newFakeLLM(acct)
	llm1.blockAfterCreate = true
	pm1 := NewPipelineManager(store, main, llm1)
	scan, err := pm1.CreateScan("batch")
	require.NoError(t, err)
	_ = runAsync(pm1, scan.ID)
	<-llm1.entered
	waitPhase(t, store, scan.ID, "full_scan", "submitting")

	llm2 := newFakeLLM(acct)
	pm2 := NewPipelineManager(store, main, finderLLM{llm2})
	run := runAsync(pm2, scan.ID)
	waitPhase(t, store, scan.ID, "full_scan", "submitted")
	p, err := store.GetPhase(scan.ID, "full_scan")
	require.NoError(t, err)
	require.Equal(t, "batch_1", p.BatchID, "re-attached to the batch OpenAI already holds")

	acct.setAllStatus("completed")
	pm2.PollBatchPhases(context.Background())
	require.NoError(t, awaitRun(t, run))

	_, creates, downloads := llm2.counts()
	require.Equal(t, 0, creates, "no second CreateBatch")
	require.Equal(t, 1, acct.count())
	require.Equal(t, 1, downloads)
	require.Len(t, resultKeys(t, store, scan.ID), 7)
}

// TestBatchCrashAfterCreateWithoutFinderFailsVisibly: without a way to look
// the batch up by metadata, a "submitting" phase is ambiguous — OpenAI may
// hold a paid batch nobody can find. Re-submitting could pay twice and waiting
// would hang for the op's 24h timeout, so the phase fails with an error that
// says so.
func TestBatchCrashAfterCreateWithoutFinderFailsVisibly(t *testing.T) {
	main := newFakeMainStore(7)
	store := newScanStore(t)
	acct := newFakeAccount()

	llm1 := newFakeLLM(acct)
	llm1.blockAfterCreate = true
	pm1 := NewPipelineManager(store, main, llm1)
	scan, err := pm1.CreateScan("batch")
	require.NoError(t, err)
	_ = runAsync(pm1, scan.ID)
	<-llm1.entered
	waitPhase(t, store, scan.ID, "full_scan", "submitting")

	llm2 := newFakeLLM(acct)
	pm2 := NewPipelineManager(store, main, llm2)
	err = awaitRun(t, runAsync(pm2, scan.ID))
	require.ErrorIs(t, err, ErrBatchSubmitUnknown)

	_, creates, _ := llm2.counts()
	require.Equal(t, 0, creates, "never re-submit a batch that may already exist")
	require.Equal(t, 1, acct.count())
}
