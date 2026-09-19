// file: internal/aiscan/pipeline_durable_test.go
// version: 1.3.0
// guid: e3808e03-3f51-4f6d-83b8-9621db7b7f15
// last-edited: 2026-09-19

package aiscan

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
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
type fakeMainStore struct {
	mu      sync.Mutex
	authors []database.Author
}

func newNamedMainStore(names ...string) *fakeMainStore {
	s := &fakeMainStore{}
	for i, n := range names {
		s.authors = append(s.authors, database.Author{ID: i + 1, Name: n})
	}
	return s
}

func (s *fakeMainStore) setAuthors(a []database.Author) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authors = a
}

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

func (s *fakeMainStore) GetAllAuthors() ([]database.Author, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]database.Author(nil), s.authors...), nil
}
func (s *fakeMainStore) GetAuthorByID(id int) (*database.Author, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	noOutput          bool // an expired/cancelled/failed batch with no output file
	inputs            []ai.AuthorDiscoveryInput
	groups            []ai.AuthorDedupInput
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
	// uncertainFirst makes author 1's first-pass answer "medium", and the
	// enrichment re-ask (a call carrying only author 1) answer "high", so
	// enriched and un-enriched results differ.
	uncertainFirst bool

	// blockDiscoverUntilCtx makes that realtime call (0-based) wait for its
	// context and return ctx.Err(), as the real client does on cancel.
	blockDiscoverUntilCtx int
	// createErr makes CreateBatch* return an error; createErrAfterAccept
	// first registers the batch with OpenAI (an ambiguous failure: the
	// request reached OpenAI but the response was lost).
	createErr, createErrAfterAccept bool
	// downloadFails makes that many downloads fail before one succeeds.
	downloadFails int
	cancels       int
	// enrichGate, when set, holds the enrichment re-ask (a call carrying only
	// author 1) until it is closed or the call's context ends.
	enrichGate chan struct{}
}

func newFakeLLM(acct *fakeAccount) *fakeLLM {
	return &fakeLLM{acct: acct, blockDiscoverAt: -1, blockDiscoverUntilCtx: -1, entered: make(chan struct{}, 1)}
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

func (f *fakeLLM) DiscoverAuthorDuplicates(ctx context.Context, inputs []ai.AuthorDiscoveryInput) ([]ai.AuthorDiscoverySuggestion, error) {
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
	if call == f.blockDiscoverUntilCtx {
		f.entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.enrichGate != nil && len(inputs) == 1 && inputs[0].ID == 1 {
		f.entered <- struct{}{}
		select {
		case <-f.enrichGate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	out := suggestionsFor(inputs)
	if f.uncertainFirst {
		enrichment := len(inputs) == 1 && inputs[0].ID == 1
		for i := range out {
			if out[i].AuthorIDs[0] == 1 && !enrichment {
				out[i].Confidence = "medium"
			}
		}
	}
	return out, nil
}

func (f *fakeLLM) createBatch(phase string, owner map[string]string, inputs []ai.AuthorDiscoveryInput) (string, error) {
	f.mu.Lock()
	f.createCalls++
	createErr, afterAccept := f.createErr, f.createErrAfterAccept
	f.createErr, f.createErrAfterAccept = false, false // one-shot
	f.mu.Unlock()
	if createErr && !afterAccept {
		return "", fmt.Errorf("create batch: connection refused")
	}
	f.acct.mu.Lock()
	f.acct.nextID++
	id := fmt.Sprintf("batch_%d", f.acct.nextID)
	f.acct.batches[id] = &fakeBatch{id: id, phase: phase, owner: owner, status: "in_progress", inputs: inputs}
	f.acct.mu.Unlock()
	if f.blockAfterCreate {
		f.entered <- struct{}{}
		select {} // OpenAI holds the batch; we die before recording its id
	}
	if createErr {
		return "", fmt.Errorf("create batch: read tcp: connection reset by peer")
	}
	return id, nil
}

func (f *fakeLLM) CreateBatchAuthorReview(_ context.Context, groups []ai.AuthorDedupInput, owner map[string]string) (string, error) {
	id, err := f.createBatch("groups_scan", owner, nil)
	if id == "" {
		return "", err
	}
	f.acct.mu.Lock()
	f.acct.batches[id].groups = groups
	f.acct.mu.Unlock()
	return id, err
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
	if b.noOutput {
		return b.status, "", nil
	}
	return b.status, "file_" + batchID, nil
}

func (f *fakeLLM) CancelBatch(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels++
	return nil
}

// failDownload consumes one configured download failure.
func (f *fakeLLM) failDownload() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downloads++
	if f.downloadFails > 0 {
		f.downloadFails--
		return fmt.Errorf("download: 502 bad gateway")
	}
	return nil
}

// DownloadBatchGroupsResults answers "merge" for every group it was asked
// about, naming the group only by index — as the real model does.
func (f *fakeLLM) DownloadBatchGroupsResults(_ context.Context, outputFileID string) ([]ai.AuthorDedupSuggestion, error) {
	if err := f.failDownload(); err != nil {
		return nil, err
	}
	f.acct.mu.Lock()
	defer f.acct.mu.Unlock()
	b := f.acct.batches[outputFileID[len("file_"):]]
	out := make([]ai.AuthorDedupSuggestion, 0, len(b.groups))
	for _, g := range b.groups {
		out = append(out, ai.AuthorDedupSuggestion{
			GroupIndex: g.Index, Action: "merge", CanonicalName: g.CanonicalName,
			Reason: "fake", Confidence: "high",
		})
	}
	return out, nil
}

func (f *fakeLLM) DownloadBatchResults(ctx context.Context, outputFileID string) ([]ai.AuthorDiscoverySuggestion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := f.failDownload(); err != nil {
		return nil, err
	}
	f.acct.mu.Lock()
	defer f.acct.mu.Unlock()
	b := f.acct.batches[outputFileID[len("file_"):]]
	out := suggestionsFor(b.inputs)
	if f.uncertainFirst {
		for i := range out {
			if out[i].AuthorIDs[0] == 1 {
				out[i].Confidence = "medium"
			}
		}
	}
	return out, nil
}

func (f *fakeLLM) counts() (discover, create, downloads int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.discoverCalls), f.createCalls, f.downloads
}

// finderLLM adds the batch-metadata lookup, matching every owner key the
// pipeline wrote at CreateBatch time (scan id, phase and nonce). A plain
// *fakeLLM deliberately lacks it. err, when set, is returned instead (a failed
// or truncated listing).
type finderLLM struct {
	*fakeLLM
	err *error
	// hide, while true, makes every batch invisible (a listing that has not
	// caught up with a batch OpenAI just accepted).
	hide *atomic.Bool
	// staleFirst, when set, makes the FIRST lookup see an empty listing and
	// then wait until the channel is closed before returning it: a resolver
	// holding a stale view while another one acts.
	staleFirst chan struct{}
	lookups    *atomic.Int32
}

func (f finderLLM) FindBatchByMetadata(_ context.Context, match map[string]string, _ time.Time) (string, bool, error) {
	if f.err != nil && *f.err != nil {
		return "", false, *f.err
	}
	n := int32(0)
	if f.lookups != nil {
		n = f.lookups.Add(1)
	}
	if f.staleFirst != nil && n == 1 {
		f.entered <- struct{}{}
		<-f.staleFirst
		return "", false, nil
	}
	if f.hide != nil && f.hide.Load() {
		return "", false, nil
	}
	f.acct.mu.Lock()
	defer f.acct.mu.Unlock()
	for id, b := range f.acct.batches {
		ok := len(match) > 0
		for k, v := range match {
			if b.owner[k] != v {
				ok = false
			}
		}
		if ok {
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

// pollUntilDone plays the collector (the poller handler / heartbeat) until the
// scan's RunScan returns. Repeated polls are the production shape: a poll that
// lands while the submitting goroutine still holds its phase claim is a no-op
// and the next tick collects.
func pollUntilDone(t *testing.T, pm *PipelineManager, run <-chan error) error {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case err := <-run:
			return err
		case <-deadline:
			t.Fatal("RunScan never returned")
			return nil
		case <-time.After(5 * time.Millisecond):
			pm.PollBatchPhases(context.Background())
		}
	}
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
	require.NoError(t, pollUntilDone(t, pm2, run))
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
	pm2 := NewPipelineManager(store, main, finderLLM{fakeLLM: llm2})
	run := runAsync(pm2, scan.ID)
	waitPhase(t, store, scan.ID, "full_scan", "submitted")
	p, err := store.GetPhase(scan.ID, "full_scan")
	require.NoError(t, err)
	require.Equal(t, "batch_1", p.BatchID, "re-attached to the batch OpenAI already holds")

	acct.setAllStatus("completed")
	require.NoError(t, pollUntilDone(t, pm2, run))

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

// --- enrichment, groups mapping, submit-confirmed-absent ----------------------

// TestCrossValidationUsesEnrichedSuggestions: author 1 comes back "medium" and
// enrichment upgrades it to "high". Cross-validation must run once, AFTER both
// enrichments, so the stored result carries the enriched confidence. Before
// the nextPhases fix the first enrichment to finish treated the other's
// not-yet-created row as done and cross-validated un-enriched suggestions.
func TestCrossValidationUsesEnrichedSuggestions(t *testing.T) {
	for range 20 { // the old race needed only a scheduling window
		store := newScanStore(t)
		llm := newFakeLLM(newFakeAccount())
		llm.uncertainFirst = true
		pm := NewPipelineManager(store, newFakeMainStore(3), llm)
		scan, err := pm.CreateScan("realtime")
		require.NoError(t, err)
		require.NoError(t, awaitRun(t, runAsync(pm, scan.ID)))

		rs, err := store.GetScanResults(scan.ID)
		require.NoError(t, err)
		require.Len(t, rs, 3, "one result per suggestion")
		for _, r := range rs {
			require.Equal(t, "high", r.Suggestion.Confidence, "author %v kept its un-enriched confidence", r.Suggestion.AuthorIDs)
		}
	}
}

// TestGroupsBatchDecodedAgainstSubmittedGrouping: the model names a group only
// by index. Between submit and collection the author table changes so that a
// rebuilt grouping would put a DIFFERENT pair at index 0; the collected
// suggestion must still name the pair the batch was asked about.
func TestGroupsBatchDecodedAgainstSubmittedGrouping(t *testing.T) {
	main := newNamedMainStore("Brandon Sanderson", "Brandon Sandersen", "Isaac Asimov")
	store := newScanStore(t)
	acct := newFakeAccount()
	llm := newFakeLLM(acct)
	pm := NewPipelineManager(store, main, llm)
	scan, err := pm.CreateScan("batch")
	require.NoError(t, err)
	run := runAsync(pm, scan.ID)
	waitPhase(t, store, scan.ID, "groups_scan", "submitted")
	waitPhase(t, store, scan.ID, "full_scan", "submitted")

	main.setAuthors([]database.Author{
		{ID: 10, Name: "Aaron Abernathy"}, {ID: 11, Name: "Aaron Abernathey"},
		{ID: 1, Name: "Brandon Sanderson"}, {ID: 2, Name: "Brandon Sandersen"},
		{ID: 3, Name: "Isaac Asimov"},
	})
	acct.setAllStatus("completed")
	require.NoError(t, pollUntilDone(t, pm, run))

	p, err := store.GetPhase(scan.ID, "groups_scan")
	require.NoError(t, err)
	var got []database.ScanSuggestion
	require.NoError(t, json.Unmarshal(p.Suggestions, &got))
	require.Len(t, got, 1)
	require.ElementsMatch(t, []int{1, 2}, got[0].AuthorIDs)
	_, _, downloads := llm.counts()
	require.Equal(t, 2, downloads, "each of the two batches downloaded once")
}

// TestBatchSubmittingWithNoBatchSubmitsOnce: a phase left "submitting" whose
// batch the finder confirms does NOT exist (the process died before
// CreateBatch reached OpenAI) is submitted exactly once on resume.
func TestBatchSubmittingWithNoBatchSubmitsOnce(t *testing.T) {
	main := newFakeMainStore(4)
	store := newScanStore(t)
	acct := newFakeAccount()
	pm0 := NewPipelineManager(store, main, newFakeLLM(acct))
	scan, err := pm0.CreateScan("batch")
	require.NoError(t, err)
	require.NoError(t, store.UpdateScanStatus(scan.ID, "scanning"))
	require.NoError(t, store.UpdatePhaseStatus(scan.ID, "full_scan", "submitting", ""))

	llm := newFakeLLM(acct)
	pm := NewPipelineManager(store, main, finderLLM{fakeLLM: llm})
	// The pre-seeded "submitting" row is past the resubmit grace.
	pm.now = func() time.Time { return time.Now().Add(2 * submitGrace) }
	run := runAsync(pm, scan.ID)
	waitPhase(t, store, scan.ID, "full_scan", "submitted")
	_, creates, _ := llm.counts()
	require.Equal(t, 1, creates)
	require.Equal(t, 1, acct.count())

	acct.setAllStatus("completed")
	require.NoError(t, pollUntilDone(t, pm, run))
	require.Len(t, resultKeys(t, store, scan.ID), 4)
}
