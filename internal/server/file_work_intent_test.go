// file: internal/server/file_work_intent_test.go
// version: 1.0.0
// guid: 0d7b3c52-9e41-4a68-b1f5-7c2e8a4d6f93
// last-edited: 2026-11-18

// M1, server side. A retired book's files are written by an attended job (the
// single-book dialog and write-back, the Fetch button) and by no unattended
// one (bulk write-back, batch save, batch apply, apply-when-scanned, a
// restart's replay of any of those). The flag travels with the file-work job:
// through the pool's submit, its persisted recovery record, the restart replay
// and the replay's registered handler. Each leg below is pinned on its own,
// because a hop that drops it replays every job as one fixed intent.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// recKV is a pool store that keeps what it is given, so a record the pool
// persists can be read back, and handed to a second pool as the restart.
type recKV struct {
	rawKVWriter
	mu   sync.Mutex
	rows map[string][]byte
}

func newRecKV() *recKV { return &recKV{rows: map[string][]byte{}} }

func (k *recKV) SetRaw(key string, v []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.rows[key] = append([]byte(nil), v...)
	return nil
}

func (k *recKV) DeleteRaw(key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.rows, key)
	return nil
}

func (k *recKV) ScanPrefix(prefix string) ([]database.KVPair, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []database.KVPair
	for key, v := range k.rows {
		if strings.HasPrefix(key, prefix) {
			out = append(out, database.KVPair{Key: key, Value: append([]byte(nil), v...)})
		}
	}
	return out, nil
}

func (k *recKV) snapshot() []database.KVPair {
	pairs, _ := k.ScanPrefix("")
	return pairs
}

// replayProbe registers a recovery handler under opType that records the
// intent each replayed book is handed, and removes it again.
type replayProbe struct {
	mu      sync.Mutex
	intents map[string]metafetch.FetchIntent
}

func registerReplayProbe(t *testing.T, opType string) *replayProbe {
	t.Helper()
	p := &replayProbe{intents: map[string]metafetch.FetchIntent{}}
	t.Cleanup(func() {
		recoveryDispatchMu.Lock()
		delete(recoveryDispatch, opType)
		recoveryDispatchMu.Unlock()
	})
	RegisterFileOpRecovery(opType, func(bookID string, intent metafetch.FetchIntent) {
		p.mu.Lock()
		p.intents[bookID] = intent
		p.mu.Unlock()
	})
	return p
}

func (p *replayProbe) waitFor(t *testing.T, books ...string) map[string]metafetch.FetchIntent {
	t.Helper()
	require.Eventually(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.intents) >= len(books)
	}, 5*time.Second, 5*time.Millisecond, "the replay never ran every job")
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]metafetch.FetchIntent{}
	for k, v := range p.intents {
		out[k] = v
	}
	return out
}

// The pool writes the job's intent into the record a restart replays from.
func TestFileIOPool_PersistsTheJobsIntentWithItsRecord(t *testing.T) {
	kv := newRecKV()
	pool := NewFileIOPool(2)
	pool.SetStore(kv)
	release := make(chan struct{})
	t.Cleanup(func() { close(release); pool.Stop() })

	require.True(t, pool.SubmitTyped("batman", "intent_probe", metafetch.Interactive, func() { <-release }))
	require.True(t, pool.SubmitTyped("robin", "intent_probe", metafetch.Unattended, func() { <-release }))

	got := map[string]metafetch.FetchIntent{}
	for _, kvp := range kv.snapshot() {
		var job FileIOJob
		require.NoError(t, json.Unmarshal(kvp.Value, &job))
		got[job.BookID] = job.Intent
	}
	assert.Equal(t, map[string]metafetch.FetchIntent{"batman": metafetch.Interactive, "robin": metafetch.Unattended}, got)

	// The in-memory view the file-ops page reads carries it too.
	seen := map[string]metafetch.FetchIntent{}
	for _, j := range pool.PendingJobs() {
		seen[j.BookID] = j.Intent
	}
	assert.Equal(t, got, seen)
}

// Submit is the apply_metadata op type's shorthand: it must hand the intent on
// too, not default it.
func TestFileIOPool_SubmitKeepsTheIntent(t *testing.T) {
	kv := newRecKV()
	pool := NewFileIOPool(1)
	pool.SetStore(kv)
	release := make(chan struct{})
	t.Cleanup(func() { close(release); pool.Stop() })

	require.True(t, pool.Submit("batman", metafetch.Interactive, func() { <-release }))

	rows := kv.snapshot()
	require.Len(t, rows, 1)
	var job FileIOJob
	require.NoError(t, json.Unmarshal(rows[0].Value, &job))
	assert.Equal(t, "apply_metadata", job.OpType)
	assert.Equal(t, metafetch.Interactive, job.Intent)
}

// The replay hands each record's intent to its handler: Interactive only for a
// record that says so; a legacy record (no field) and any unknown value replay
// as Unattended, the refusing side.
func TestRecoverInterruptedFileOps_ReplaysEachRecordsIntent(t *testing.T) {
	const opType = "intent_replay_probe"
	probe := registerReplayProbe(t, opType)

	row := func(book, body string) database.KVPair {
		return database.KVPair{Key: pendingFileOpPrefix + book + ":" + opType, Value: []byte(body)}
	}
	rows := []database.KVPair{
		row("attended", fmt.Sprintf(`{"book_id":"attended","op_type":%q,"intent":%d}`, opType, metafetch.Interactive)),
		row("unattended", fmt.Sprintf(`{"book_id":"unattended","op_type":%q,"intent":%d}`, opType, metafetch.Unattended)),
		row("legacy", fmt.Sprintf(`{"book_id":"legacy","op_type":%q}`, opType)),
		row("unknown", fmt.Sprintf(`{"book_id":"unknown","op_type":%q,"intent":99}`, opType)),
	}
	pool := NewFileIOPool(2)
	t.Cleanup(pool.Stop)
	pool.SetStore(&kvFake{rows: rows})

	RecoverInterruptedFileOps(pool, nil)

	assert.Equal(t, map[string]metafetch.FetchIntent{
		"attended":   metafetch.Interactive,
		"unattended": metafetch.Unattended,
		"legacy":     metafetch.Unattended,
		"unknown":    metafetch.Unattended,
	}, probe.waitFor(t, "attended", "unattended", "legacy", "unknown"))
}

// The whole path: submit through one pool, the record it persisted, a restart
// (a second pool over the same store), and the handler's argument.
func TestFileIOPool_SubmitThenRestartReplayKeepsTheIntent(t *testing.T) {
	const opType = "intent_roundtrip_probe"
	probe := registerReplayProbe(t, opType)

	kv := newRecKV()
	first := NewFileIOPool(2)
	first.SetStore(kv)
	release := make(chan struct{})
	require.True(t, first.SubmitTyped("attended", opType, metafetch.Interactive, func() { <-release }))
	require.True(t, first.SubmitTyped("unattended", opType, metafetch.Unattended, func() { <-release }))

	// The process dies with both jobs unfinished: what is left is the rows.
	crashed := newRecKV()
	for _, p := range kv.snapshot() {
		_ = crashed.SetRaw(p.Key, p.Value)
	}
	close(release)
	first.Stop()

	second := NewFileIOPool(2)
	t.Cleanup(second.Stop)
	second.SetStore(crashed)
	RecoverInterruptedFileOps(second, nil)

	assert.Equal(t, map[string]metafetch.FetchIntent{
		"attended":   metafetch.Interactive,
		"unattended": metafetch.Unattended,
	}, probe.waitFor(t, "attended", "unattended"))
}

// The scheduler metafetch's auto-fetch uses queues the job with the fetch's
// intent, so the record it persists replays with it.
func TestAutoFetchScheduler_QueuesWithTheFetchsIntent(t *testing.T) {
	kv := newRecKV()
	pool := NewFileIOPool(2)
	pool.SetStore(kv)
	release := make(chan struct{})
	t.Cleanup(func() { close(release); pool.Stop() })

	sched := newAutoFetchScheduler(func() *FileIOPool { return pool })
	sched("attended", metafetch.Interactive, func() { <-release })
	sched("unattended", metafetch.Unattended, func() { <-release })

	got := map[string]metafetch.FetchIntent{}
	for _, kvp := range kv.snapshot() {
		var job FileIOJob
		require.NoError(t, json.Unmarshal(kvp.Value, &job))
		require.Equal(t, autoFetchFileOpType, job.OpType)
		got[job.BookID] = job.Intent
	}
	assert.Equal(t, map[string]metafetch.FetchIntent{"attended": metafetch.Interactive, "unattended": metafetch.Unattended}, got)
}

// intentSvc is a metadata service that records the intent each replayed job
// reaches the file work with.
type intentSvc struct {
	mu    sync.Mutex
	calls []string
}

func (s *intentSvc) FinishApplyFileWork(_ context.Context, id, _ string, _, _ bool, intent metafetch.FetchIntent, _ func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, fmt.Sprintf("apply:%s:%s", id, intent))
	return nil
}

func (s *intentSvc) FinishAutoFetchFileWork(_ context.Context, id, _ string, _ bool, intent metafetch.FetchIntent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, fmt.Sprintf("autofetch:%s:%s", id, intent))
	return nil
}

// The two handlers the server registers pass the replayed intent to the file
// work (a mutant that hard-codes either side fails one of the four rows).
func TestRegisteredFileOpRecoveries_HandTheReplayedIntentToTheFileWork(t *testing.T) {
	recoveryDispatchMu.Lock()
	prev := map[string]FileOpRecoveryFunc{}
	for _, op := range []string{"apply_metadata", autoFetchFileOpType} {
		prev[op] = recoveryDispatch[op]
	}
	recoveryDispatchMu.Unlock()
	t.Cleanup(func() {
		recoveryDispatchMu.Lock()
		for op, fn := range prev {
			if fn == nil {
				delete(recoveryDispatch, op)
			} else {
				recoveryDispatch[op] = fn
			}
		}
		recoveryDispatchMu.Unlock()
	})

	svc := &intentSvc{}
	registerFileOpRecoveries(func() fileWorkRecoverer { return svc })

	for _, tc := range []struct {
		op     string
		intent metafetch.FetchIntent
		want   string
	}{
		{"apply_metadata", metafetch.Interactive, "apply:b1:interactive"},
		{"apply_metadata", metafetch.Unattended, "apply:b2:unattended"},
		{autoFetchFileOpType, metafetch.Interactive, "autofetch:b3:interactive"},
		{autoFetchFileOpType, metafetch.Unattended, "autofetch:b4:unattended"},
	} {
		fn, ok := lookupFileOpRecovery(tc.op)
		require.True(t, ok, tc.op)
		book := strings.Split(tc.want, ":")[1]
		fn(book, tc.intent)
		svc.mu.Lock()
		got := svc.calls[len(svc.calls)-1]
		svc.mu.Unlock()
		assert.Equal(t, tc.want, got)
	}

	// No metadata service: the handler logs and does nothing, instead of
	// calling through a nil.
	registerFileOpRecoveries(func() fileWorkRecoverer { return nil })
	fn, _ := lookupFileOpRecovery("apply_metadata")
	svc.mu.Lock()
	before := len(svc.calls)
	svc.mu.Unlock()
	fn("b5", metafetch.Interactive)
	svc.mu.Lock()
	defer svc.mu.Unlock()
	assert.Len(t, svc.calls, before)
}

// Each caller class in this package states its intent, seen through the seams
// and the fake service the callers take.

// apply-when-scanned and batch-apply-candidates share submitOpResultFileWork.
func TestUnattendedCallers_ApplyWhenScannedFileWorkIsUnattended(t *testing.T) {
	got := make(chan metafetch.FetchIntent, 1)
	prev := finishApplyFileWork
	finishApplyFileWork = func(_ *metafetch.Service, _ context.Context, _, _ string, _, _ bool, intent metafetch.FetchIntent, _ func() error) error {
		got <- intent
		return nil
	}
	t.Cleanup(func() { finishApplyFileWork = prev })

	kv := newRecKV()
	pool := NewFileIOPool(1)
	pool.SetStore(kv)
	defer pool.Stop()
	s := &Server{fileIOPool: pool}
	s.submitOpResultFileWork(context.Background(), "b1", "")

	select {
	case intent := <-got:
		assert.Equal(t, metafetch.Unattended, intent, "the job's file work")
	case <-time.After(10 * time.Second):
		t.Fatal("the file job never ran")
	}
}

// The pool record apply-when-scanned's job leaves is Unattended too, so its
// replay refuses a retired book's files like the live job did.
func TestUnattendedCallers_ApplyWhenScannedRecordReplaysUnattended(t *testing.T) {
	release := make(chan struct{})
	prev := finishApplyFileWork
	finishApplyFileWork = func(*metafetch.Service, context.Context, string, string, bool, bool, metafetch.FetchIntent, func() error) error {
		<-release
		return nil
	}
	t.Cleanup(func() { finishApplyFileWork = prev })

	kv := newRecKV()
	pool := NewFileIOPool(1)
	pool.SetStore(kv)
	t.Cleanup(func() { close(release); pool.Stop() })
	s := &Server{fileIOPool: pool}
	s.submitOpResultFileWork(context.Background(), "b1", "")

	rows := kv.snapshot()
	require.Len(t, rows, 1)
	var job FileIOJob
	require.NoError(t, json.Unmarshal(rows[0].Value, &job))
	assert.Equal(t, metafetch.Unattended, job.Intent)
}

// The batch-apply op's per-book file work, both branches.
func TestUnattendedCallers_BatchApplyFileWorkIsUnattended(t *testing.T) {
	for _, writeBack := range []bool{true, false} {
		t.Run(fmt.Sprintf("writeBack=%v", writeBack), func(t *testing.T) {
			svc := &fakeApplySvc{candidates: oneCandidate(t)}
			out := applyCachedCandidateForBookTimed(context.Background(), svc, fakeBooks{}, "b1", writeBack, nil,
				metafetch.NewApplyPhaseTimings(), nil, nil, "")
			require.True(t, out.Applied, "outcome: %+v", out)
			require.NotEmpty(t, svc.finishCalls)
			for _, c := range svc.finishCalls {
				assert.Equal(t, metafetch.Unattended, c.intent)
			}
		})
	}
}

// metadata.batch-save's write-back.
func TestUnattendedCallers_BatchSaveWriteBackIsUnattended(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	intents := captureWriteBackIntents(t)
	id := writeBackBook(t, s)

	reg := capOpReg(t)
	require.NoError(t, s.RegisterBatchSaveToFilesOp(reg))
	def, ok := reg.Def("metadata.batch-save")
	require.True(t, ok)
	params, err := json.Marshal(batchSaveOpParams{BookIDs: []string{id}, Force: true})
	require.NoError(t, err)
	require.NoError(t, def.Run(context.Background(), params, &resumeRecorder{opID: "op-intent-save"}))

	assert.Equal(t, []metafetch.FetchIntent{metafetch.Unattended}, intents.all())
}

// captureWriteBackIntents replaces the write-back seam with a recorder of the
// intent each call carries.
type intentRecorder struct {
	mu sync.Mutex
	v  []metafetch.FetchIntent
}

func (r *intentRecorder) add(i metafetch.FetchIntent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.v = append(r.v, i)
}

func (r *intentRecorder) all() []metafetch.FetchIntent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]metafetch.FetchIntent(nil), r.v...)
}

func captureWriteBackIntents(t *testing.T) *intentRecorder {
	t.Helper()
	rec := &intentRecorder{}
	prev := writeBackBookFiles
	writeBackBookFiles = func(_ *metafetch.Service, _ context.Context, _ string, intent metafetch.FetchIntent) (int, error) {
		rec.add(intent)
		return 0, nil
	}
	t.Cleanup(func() { writeBackBookFiles = prev })
	return rec
}

// renameableBook creates a book under root whose naming pattern puts its two
// files somewhere else, so a rename that runs is visible on disk.
func renameableBook(t *testing.T, s *Server, root, title string, retire bool) (id string, files []string) {
	t.Helper()
	author, err := s.store.CreateAuthor("Harmon Example")
	if err != nil {
		author, err = s.store.GetAuthorByName("Harmon Example")
	}
	require.NoError(t, err)
	dir := filepath.Join(root, "incoming", title)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	book := &database.Book{Title: title, Format: "mp3", AuthorID: &author.ID, FilePath: dir}
	if retire {
		yes := true
		book.MarkedForDeletion = &yes
	}
	created, err := s.store.CreateBook(book)
	require.NoError(t, err)
	for i, body := range []string{"part one", "part two, longer"} {
		p := filepath.Join(dir, fmt.Sprintf("%02d.mp3", i+1))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
		require.NoError(t, s.store.CreateBookFile(&database.BookFile{
			BookID: created.ID, FilePath: p, Format: "mp3", TrackNumber: i + 1, FileSize: int64(len(body)),
		}))
		files = append(files, p)
	}
	return created.ID, files
}

func withRenameConfig(t *testing.T, root string) {
	t.Helper()
	config.Mutate(func(c *config.Config) {
		c.RootDir = root
		c.AutoRenameOnApply = true
		c.FolderNamingPattern = "{author}/{title}"
		c.FileNamingPattern = "{title} - {track:02d}"
	})
}

func anyMissing(paths []string) bool {
	for _, p := range paths {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return true
		}
	}
	return false
}

// A bulk write-back with rename on leaves a retired book alone BEFORE its
// rename and counts it skipped, not failed; a live book beside it is renamed
// and written. The retired book's files are untouched on disk.
func TestBulkWriteBack_SkipsARetiredBookBeforeItsRename(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	root := t.TempDir()
	withRenameConfig(t, root)
	intents := captureWriteBackIntents(t)

	retiredID, retiredFiles := renameableBook(t, s, root, "Year One", true)
	liveID, liveFiles := renameableBook(t, s, root, "Year Two", false)

	rec := &resumeRecorder{opID: "op-bulk-retired"}
	progress := registryProgressAdapter{r: rec}
	require.NoError(t, s.runBulkWriteBack(context.Background(), "op-bulk-retired", []string{retiredID, liveID}, true, 0, progress, nil))

	assert.False(t, anyMissing(retiredFiles), "no file of the retired book may be renamed by an unattended bulk job")
	assert.True(t, anyMissing(liveFiles), "control: the live book was renamed, so this fixture can see a rename")
	assert.Equal(t, []metafetch.FetchIntent{metafetch.Unattended}, intents.all(), "only the live book reached the tag write, and unattended")

	rec.mu.Lock()
	defer rec.mu.Unlock()
	var summary string
	for _, l := range rec.logs {
		if strings.HasPrefix(l, "bulk write-back complete") {
			summary = l
		}
	}
	assert.Equal(t, "bulk write-back complete: 1 written, 0 failed, 1 skipped out of 2", summary)
}

// A book retired while the job waited for the file-I/O gate is skipped too:
// the row is read again after the wait.
func TestBulkWriteBack_RereadsTheBookAfterTheFileGate(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	root := t.TempDir()
	withRenameConfig(t, root)
	intents := captureWriteBackIntents(t)
	id, files := renameableBook(t, s, root, "Year One", false)

	// Hold every gate slot so the worker parks after its first read.
	var releases []func()
	for range maxWriteBackWorkers {
		rel, err := writeBackFileGate.acquire(context.Background())
		require.NoError(t, err)
		releases = append(releases, rel)
	}
	release := func() {
		for _, r := range releases {
			r()
		}
		releases = nil
	}
	t.Cleanup(release)

	rec := &resumeRecorder{opID: "op-bulk-gate"}
	done := make(chan error, 1)
	go func() {
		done <- s.runBulkWriteBack(context.Background(), "op-bulk-gate", []string{id}, true, 0, registryProgressAdapter{r: rec}, nil)
	}()

	// The book is trashed while the job waits.
	time.Sleep(200 * time.Millisecond)
	b, err := s.store.GetBookByID(id)
	require.NoError(t, err)
	yes := true
	b.MarkedForDeletion = &yes
	_, err = s.store.UpdateBook(id, b)
	require.NoError(t, err)
	release()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("the bulk write-back never finished")
	}
	assert.False(t, anyMissing(files), "a book trashed while the job queued must not be renamed")
	assert.Empty(t, intents.all(), "and not tagged")
	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.Contains(t, strings.Join(rec.logs, "\n"), "0 written, 0 failed, 1 skipped")
}

// retireOnRead wraps a store so the book is retired at the Nth read of it,
// AFTER that read returned the live row: the window between the bulk job's own
// retired check and the file work's, under the book's lock.
type retireOnRead struct {
	database.Store
	mu     sync.Mutex
	reads  int
	at     int
	retire func()
}

func (r *retireOnRead) GetBookByID(id string) (*database.Book, error) {
	b, err := r.Store.GetBookByID(id)
	r.mu.Lock()
	r.reads++
	fire := r.reads == r.at
	r.mu.Unlock()
	if fire {
		r.retire()
	}
	return b, err
}

// A book that is retired after the bulk job's last read is refused by the file
// work itself (fileWorkTarget), and the job counts that skipped, not failed.
func TestBulkWriteBack_RetiredInTheWindowIsSkippedNotFailed(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	root := t.TempDir()
	withRenameConfig(t, root)
	intents := captureWriteBackIntents(t)
	id, files := renameableBook(t, s, root, "Year One", false)

	real := s.store
	// Reads 1 (top of processOne) and 2 (after the gate) see the live row; the
	// book is trashed right after read 2, so the rename's own check refuses it.
	s.store = &retireOnRead{Store: real, at: 2, retire: func() {
		b, err := real.GetBookByID(id)
		require.NoError(t, err)
		yes := true
		b.MarkedForDeletion = &yes
		_, err = real.UpdateBook(id, b)
		require.NoError(t, err)
	}}
	t.Cleanup(func() { s.store = real })

	rec := &resumeRecorder{opID: "op-bulk-window"}
	require.NoError(t, s.runBulkWriteBack(context.Background(), "op-bulk-window", []string{id}, true, 0, registryProgressAdapter{r: rec}, nil))

	assert.False(t, anyMissing(files), "the refused rename moved nothing")
	assert.Empty(t, intents.all(), "and the tags were not written")
	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.Contains(t, strings.Join(rec.logs, "\n"), "0 written, 0 failed, 1 skipped")
}

// The write-back's own refusal (the book retired after the rename step) is a
// skip as well.
func TestBulkWriteBack_RetiredRefusalFromTheTagWriteIsSkippedNotFailed(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	id := writeBackBook(t, s)
	prev := writeBackBookFiles
	writeBackBookFiles = func(*metafetch.Service, context.Context, string, metafetch.FetchIntent) (int, error) {
		return 0, fmt.Errorf("book %s is retired: %w", id, metafetch.ErrBookRetired)
	}
	t.Cleanup(func() { writeBackBookFiles = prev })

	rec := &resumeRecorder{opID: "op-bulk-writeref"}
	require.NoError(t, s.runBulkWriteBack(context.Background(), "op-bulk-writeref", []string{id}, false, 0, registryProgressAdapter{r: rec}, nil))

	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.Contains(t, strings.Join(rec.logs, "\n"), "0 written, 0 failed, 1 skipped")
}

// A non-retired failure from the tag write is still a failure.
func TestBulkWriteBack_OtherWriteErrorsStillFail(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	id := writeBackBook(t, s)
	prev := writeBackBookFiles
	writeBackBookFiles = func(*metafetch.Service, context.Context, string, metafetch.FetchIntent) (int, error) {
		return 0, fmt.Errorf("disk on fire")
	}
	t.Cleanup(func() { writeBackBookFiles = prev })

	rec := &resumeRecorder{opID: "op-bulk-otherfail"}
	require.NoError(t, s.runBulkWriteBack(context.Background(), "op-bulk-otherfail", []string{id}, false, 0, registryProgressAdapter{r: rec}, nil))

	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.Contains(t, strings.Join(rec.logs, "\n"), "0 written, 1 failed, 0 skipped")
}
