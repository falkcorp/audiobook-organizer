// file: internal/reconcile/reconcile_scan_ctx_test.go
// version: 1.1.0
// guid: 3f7a1d92-6c48-4e05-b1a9-8d2e5c70f634
// last-edited: 2026-10-14

package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// The reconcile scan runs on the registry's 5-minute silence watchdog and, since
// 04-P14c, is also the nightly job. These tests pin the two things that made
// that unsafe before: stretches that report nothing (the directory walk, the
// title match) and a Cancel that the body never looked at.

// countingHooks returns hooks whose Heartbeat increments n and, when cancelAt is
// positive, cancels the context on that call.
func countingHooks(n *atomic.Int64, cancelAt int64, cancel context.CancelFunc) ScanHooks {
	return ScanHooks{Heartbeat: func() {
		if got := n.Add(1); cancelAt > 0 && got == cancelAt {
			cancel()
		}
	}}
}

// recordingProgress is a ProgressReporter that discards everything.
type recordingProgress struct{}

func (recordingProgress) UpdateProgress(int, int, string) error { return nil }
func (recordingProgress) Log(string, string, *string) error     { return nil }
func (recordingProgress) IsCanceled() bool                      { return false }

// scanFixture builds a library root holding two untracked audio files and a
// store with two broken records that match neither by hash nor by name, so a
// full scan reaches the title-in-filename phase.
func scanFixture(t *testing.T) *fakeReconcileStore {
	t.Helper()
	root := withEmptyExtensionConfig(t, nil)
	writeAudio(t, root, "zzz-one.m4b", "zzz-two.m4b")

	store := newFakeStore()
	missing := t.TempDir()
	store.books = []database.BookCore{
		{ID: "b1", Title: "alpha", FilePath: filepath.Join(missing, "alpha-gone.m4b")},
		{ID: "b2", Title: "bravo", FilePath: filepath.Join(missing, "bravo-gone.m4b")},
	}
	return store
}

// Scan phases, named after the progress message that opens each one. The
// recorder below attributes every heartbeat to the phase that was open when it
// fired, which is what lets a test cancel on "the first hash beat" or count
// "the title-match beats" instead of inferring both from a total.
const (
	phaseStat  = "stat"
	phaseWalk  = "walk"
	phaseHash  = "hash"
	phaseTitle = "title"
)

type progressEvent struct {
	current, total int
	msg            string
}

// phaseRecorder is the logger a scan reports to and the source of its
// heartbeats. It is safe for concurrent use: the stat and hash pools call both
// from worker goroutines.
type phaseRecorder struct {
	logger.Logger // everything this does not record goes to a real logger

	mu       sync.Mutex
	phase    string
	beats    map[string]int
	progress []progressEvent
	infos    []string

	cancelPhase string
	cancelAt    int
	cancel      context.CancelFunc
}

func newPhaseRecorder() *phaseRecorder {
	return &phaseRecorder{Logger: logger.New("reconcile-test"), beats: map[string]int{}}
}

// cancelOnBeat cancels the scan on the nth heartbeat of phase.
func (r *phaseRecorder) cancelOnBeat(phase string, n int, cancel context.CancelFunc) *phaseRecorder {
	r.cancelPhase, r.cancelAt, r.cancel = phase, n, cancel
	return r
}

func (r *phaseRecorder) hooks() ScanHooks { return ScanHooks{Heartbeat: r.beat} }

func (r *phaseRecorder) beat() {
	r.mu.Lock()
	r.beats[r.phase]++
	fire := r.cancel != nil && r.phase == r.cancelPhase && r.beats[r.phase] == r.cancelAt
	cancel := r.cancel
	r.mu.Unlock()
	if fire {
		cancel()
	}
}

func (r *phaseRecorder) UpdateProgress(current, total int, msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = append(r.progress, progressEvent{current, total, msg})
	switch {
	case strings.HasPrefix(msg, "Checking file paths"):
		r.phase = phaseStat
	case strings.HasPrefix(msg, "Scanning directories"):
		r.phase = phaseWalk
	case strings.HasPrefix(msg, "Hashing "):
		r.phase = phaseHash
	case strings.Contains(msg, "by title in filename"):
		r.phase = phaseTitle
	}
}

func (r *phaseRecorder) Info(msg string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.infos = append(r.infos, fmt.Sprintf(msg, args...))
}

func (r *phaseRecorder) beatsIn(phase string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.beats[phase]
}

// reportedHashingDone reports whether the scan announced that every untracked
// file had been hashed ("Hashed 2/2 untracked files").
func (r *phaseRecorder) reportedHashingDone() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.progress {
		if strings.HasPrefix(e.msg, "Hashed ") && e.current == e.total {
			return true
		}
	}
	return false
}

func (r *phaseRecorder) logged(substr string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.infos {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}

// manyBrokenFixture is scanFixture with k broken records that match nothing, so
// every one of them reaches the title-in-filename phase.
func manyBrokenFixture(t *testing.T, files, k int) *fakeReconcileStore {
	t.Helper()
	root := withEmptyExtensionConfig(t, nil)
	names := make([]string, files)
	for i := range names {
		names[i] = fmt.Sprintf("zzz-file-%03d.m4b", i)
	}
	writeAudio(t, root, names...)
	store := newFakeStore()
	missing := t.TempDir()
	for i := range k {
		id := fmt.Sprintf("b%d", i)
		store.books = append(store.books, database.BookCore{ID: id, Title: "unrelated " + id, FilePath: filepath.Join(missing, id+"-gone.m4b")})
	}
	return store
}

// The pools and the title loop each stamp the watchdog once per item. Deleting
// any of those beats leaves the scan green and every other test passing, so the
// counts are pinned per phase.
func TestBuildReconcilePreviewCtx_HeartbeatsOncePerItemInEachPhase(t *testing.T) {
	const files, broken = 5, 3
	rec := newPhaseRecorder()
	if _, err := BuildReconcilePreviewCtx(context.Background(), manyBrokenFixture(t, files, broken), rec, rec.hooks()); err != nil {
		t.Fatalf("preview: %v", err)
	}
	for phase, want := range map[string]int{
		phaseStat:  broken, // one per book with a path
		phaseHash:  files,  // one per hashed file
		phaseTitle: broken, // one per broken record
	} {
		if got := rec.beatsIn(phase); got != want {
			t.Errorf("%s phase sent %d heartbeats, want %d", phase, got, want)
		}
	}
	// The walk beats once per entry: at least one per file, plus the directories.
	if got := rec.beatsIn(phaseWalk); got < files {
		t.Errorf("walk phase sent %d heartbeats, want at least %d", got, files)
	}
}

func TestFindUntrackedFiles_HeartbeatsPerWalkedEntry(t *testing.T) {
	root := withEmptyExtensionConfig(t, nil)
	writeAudio(t, root, "a.m4b", "b.m4b", "c.m4b")

	var beats atomic.Int64
	got, err := FindUntrackedFiles(context.Background(), appDirStore{}, map[string]bool{}, countingHooks(&beats, 0, nil))
	if err != nil {
		t.Fatalf("FindUntrackedFiles: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("found %d files, want 3", len(got))
	}
	// 3 files plus the root and the two directories above them: the walk is the
	// longest silent stretch on network storage, so every entry must stamp.
	if beats.Load() < 3 {
		t.Errorf("heartbeats = %d, want at least one per walked file (3)", beats.Load())
	}
}

// Canceling mid-walk must stop the walk at the next entry, not let it run to the
// end of a library-sized tree before the post-walk check notices.
func TestFindUntrackedFiles_CancelMidWalkStopsAtTheNextEntry(t *testing.T) {
	root := withEmptyExtensionConfig(t, nil)
	names := make([]string, 60)
	for i := range names {
		names[i] = fmt.Sprintf("f%02d.m4b", i)
	}
	writeAudio(t, root, names...)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var beats atomic.Int64
	got, err := FindUntrackedFiles(ctx, appDirStore{}, map[string]bool{}, countingHooks(&beats, 3, cancel))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v (files %d)", err, len(got))
	}
	if beats.Load() != 3 {
		t.Errorf("walk visited %d entries after cancel on the 3rd; it must stop at the next entry", beats.Load())
	}
}

func TestFindUntrackedFiles_CanceledContextStopsTheWalk(t *testing.T) {
	root := withEmptyExtensionConfig(t, nil)
	writeAudio(t, root, "a.m4b", "b.m4b")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := FindUntrackedFiles(ctx, appDirStore{}, map[string]bool{}, ScanHooks{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a canceled walk must return context.Canceled, got %v (files %v)", err, got)
	}
	if got != nil {
		t.Errorf("a canceled walk must not hand back a partial list, got %v", got)
	}
}

// A scan that is never canceled must still finish and see the whole fixture:
// the control for the cancel tests below.
func TestBuildReconcilePreviewCtx_UncanceledRunCompletes(t *testing.T) {
	store := scanFixture(t)
	var beats atomic.Int64
	res, err := BuildReconcilePreviewCtx(context.Background(), store, nil, countingHooks(&beats, 0, nil))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(res.BrokenRecords) != 2 || len(res.UntrackedFiles) != 2 || len(res.UnmatchedBooks) != 2 {
		t.Fatalf("fixture drifted: broken=%d untracked=%d unmatched=%d, want 2/2/2",
			len(res.BrokenRecords), len(res.UntrackedFiles), len(res.UnmatchedBooks))
	}
	if beats.Load() == 0 {
		t.Error("a full scan must send heartbeats")
	}
}

// Canceling during the stat pass must fail the scan, and must stop the pool from
// starting further stats. Stats that were skipped leave their broken flag false,
// so without the check after the pool drains the unchecked tail reads as "all
// paths valid" and the scan returns a clean, wrong result.
//
// No library root is configured here, so nothing after the stat pass looks at
// ctx; only the check that follows the pool can notice. The test also bounds how
// many stats ran: that bound, not the returned error, is what pins the
// per-worker ctx check (the check after the pool returns Canceled either way).
func TestBuildReconcilePreviewCtx_CancelDuringStatPassFailsTheScan(t *testing.T) {
	_ = withEmptyExtensionConfig(t, nil)
	config.Mutate(func(c *config.Config) { c.RootDir = "" })
	store := newFakeStore()
	missing := t.TempDir()
	const books = 3000
	for i := range books {
		id := fmt.Sprintf("m%04d", i)
		store.books = append(store.books, database.BookCore{ID: id, Title: id, FilePath: filepath.Join(missing, id+".m4b")})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := newPhaseRecorder().cancelOnBeat(phaseStat, 1, cancel)
	res, err := BuildReconcilePreviewCtx(ctx, store, rec, rec.hooks())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a scan canceled during the stat pass must return context.Canceled, got err=%v res=%v", err, res)
	}
	if res != nil {
		t.Errorf("a canceled scan must not return a result, got %+v", res)
	}
	// Workers already past their ctx check when the cancel lands may still beat
	// (at most the pool size); a pool that ignores ctx beats all 3000.
	if got := rec.beatsIn(phaseStat); got > books/10 {
		t.Errorf("stat pool kept starting stats after cancel: %d of %d ran", got, books)
	}
}

// Canceling inside the title-in-filename phase, the O(broken x untracked) loop
// that reports nothing of its own, must stop it at the next record: the first
// record's beat cancels, so the second record must not beat.
func TestBuildReconcilePreviewCtx_CancelInTitleMatchStopsAtNextRecord(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := newPhaseRecorder().cancelOnBeat(phaseTitle, 1, cancel)
	res, err := BuildReconcilePreviewCtx(ctx, manyBrokenFixture(t, 2, 3), rec, rec.hooks())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a scan canceled inside the title match must return context.Canceled, got err=%v res=%v", err, res)
	}
	if got := rec.beatsIn(phaseTitle); got != 1 {
		t.Errorf("title match sent %d heartbeats; it must stop at the record after the cancel", got)
	}
}

// Canceling while hashing must fail the scan. The hash pool skips files once
// canceled, which would leave a partial index that reads as "nothing matched".
//
// This pins "a cancel during hashing surfaces as Canceled", not where the check
// sits: the title loop's own per-record check would also return Canceled if the
// check after the pool were deleted. TestBuildReconcilePreviewCtx_
// CancelAfterHashingReportsNoHashingResults is the test that isolates it.
func TestBuildReconcilePreviewCtx_CancelWhileHashingFailsTheScan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := newPhaseRecorder().cancelOnBeat(phaseHash, 1, cancel)
	res, err := BuildReconcilePreviewCtx(ctx, manyBrokenFixture(t, 2, 2), rec, rec.hooks())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a scan canceled while hashing must return context.Canceled, got err=%v res=%v", err, res)
	}
}

// A scan canceled while hashing must stop right there. Reporting "Hashed 2/2"
// and "Computed hashes for N files" afterwards tells the operator, and the
// activity log, that hashing finished when part of it was skipped.
func TestBuildReconcilePreviewCtx_CancelAfterHashingReportsNoHashingResults(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := newPhaseRecorder().cancelOnBeat(phaseHash, 1, cancel)
	if _, err := BuildReconcilePreviewCtx(ctx, manyBrokenFixture(t, 2, 2), rec, rec.hooks()); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if rec.reportedHashingDone() {
		t.Error("a canceled scan reported 100% hashed")
	}
	for _, line := range []string{"Computed hashes", "Hash matching found"} {
		if rec.logged(line) {
			t.Errorf("a canceled scan logged %q", line)
		}
	}
	if got := rec.beatsIn(phaseTitle); got != 0 {
		t.Errorf("a scan canceled while hashing went on to the title match (%d beats)", got)
	}
}

// hashPoolFiles is enough work that a pool ignoring ctx is unmistakable: well
// over any plausible worker count, so the bound below cannot be met by the
// workers already in flight when the cancel lands.
func hashPoolFiles() []string {
	files := make([]string, max(200, 50*runtime.NumCPU()))
	for i := range files {
		files[i] = fmt.Sprintf("/nonexistent/synthetic/file-%04d.m4b", i) // hash errors are skipped
	}
	return files
}

// Once canceled, the hash pool must stop starting hashes. Cancel on the first
// beat of a few hundred files and fewer than half may have started.
func TestHashFilesConcurrent_CancelStopsStartingHashes(t *testing.T) {
	files := hashPoolFiles()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var beats atomic.Int64
	hashFilesConcurrent(ctx, files, nil, countingHooks(&beats, 1, cancel))
	if got := beats.Load(); got == 0 || got > int64(len(files)/2) {
		t.Errorf("%d of %d files started after the cancel on the first beat; want at least 1 and under half", got, len(files))
	}
}

// And every file stamps the watchdog once: a pool that hashes without beating
// leaves the longest I/O phase of the scan silent.
func TestHashFilesConcurrent_HeartbeatsOncePerFile(t *testing.T) {
	files := hashPoolFiles()
	var beats atomic.Int64
	hashFilesConcurrent(context.Background(), files, nil, countingHooks(&beats, 0, nil))
	if got := beats.Load(); got != int64(len(files)) {
		t.Errorf("hash pool sent %d heartbeats for %d files", got, len(files))
	}
}

func TestRunReconcileScan_CanceledScanSavesNothing(t *testing.T) {
	store := scanFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	saved := 0
	err := RunReconcileScan(ctx, store, func(any) error { saved++; return nil }, recordingProgress{}, ScanHooks{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if saved != 0 {
		t.Errorf("a canceled scan saved %d results; a partial preview must never be stored", saved)
	}
}

func TestRunReconcileScan_CompletedScanSavesOnceAndHeartbeats(t *testing.T) {
	store := scanFixture(t)
	var beats atomic.Int64
	saved := 0
	var payload any
	err := RunReconcileScan(context.Background(), store, func(p any) error {
		saved++
		payload = p
		return nil
	}, recordingProgress{}, countingHooks(&beats, 0, nil))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if saved != 1 {
		t.Errorf("saved %d results, want 1", saved)
	}
	if beats.Load() == 0 {
		t.Error("the scan must send heartbeats")
	}
	// The payload shape is the /latest endpoint's contract (the same type the
	// retired nightly op stored), so nightly and manual rows decode identically.
	if _, ok := payload.(json.RawMessage); !ok {
		t.Errorf("payload is %T, want json.RawMessage of a ReconcilePreviewResult", payload)
	}
}

// The title-in-filename phase normalizes every untracked basename once and
// reuses it for the filename index and the title loop. This pins the result of
// the whole name-matching ladder on a fixture that exercises each rule, so the
// precomputation cannot change which file a record takes:
//
//   - b5 matches by normalized filename (step 4) and takes exact_match.m4b;
//   - b6's title would match that same file, but it is taken, so b6 stays unmatched;
//   - b1 ("dragon rider") is contained in two filenames and the first in walk
//     order wins;
//   - b2 ("dragon") then gets the next free containing file;
//   - b3's title contains a filename (the reverse direction);
//   - b4 matches nothing.
func TestBuildReconcilePreviewCtx_TitleMatchResultsAreStable(t *testing.T) {
	root := withEmptyExtensionConfig(t, nil)
	writeAudio(t, root, "dragon rider part 1.m4b", "dragon rider part 2.m4b", "dragon rider.m4b", "exact_match.m4b")

	store := newFakeStore()
	missing := t.TempDir()
	gone := func(name string) string { return filepath.Join(missing, name) }
	store.books = []database.BookCore{
		{ID: "b1", Title: "Dragon Rider", FilePath: gone("b1-gone.m4b")},
		{ID: "b2", Title: "Dragon", FilePath: gone("b2-gone.m4b")},
		{ID: "b3", Title: "Dragon Rider Returns Complete Edition", FilePath: gone("b3-gone.m4b")},
		{ID: "b4", Title: "Wholly Absent", FilePath: gone("b4-gone.m4b")},
		{ID: "b5", Title: "Some Other Title", FilePath: gone("exact-match.m4b")},
		{ID: "b6", Title: "Exact Match", FilePath: gone("b6-gone.m4b")},
	}

	res, err := BuildReconcilePreviewCtx(context.Background(), store, nil, ScanHooks{})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}

	type got struct {
		book, file, kind string
		score            float64
	}
	var have []got
	for _, m := range res.Matches {
		have = append(have, got{m.BookID, filepath.Base(m.NewPath), m.MatchType, m.Score})
	}
	want := []got{
		{"b5", "exact_match.m4b", "filename", 0.5},
		{"b1", "dragon rider part 1.m4b", "filename", 0.3},
		{"b2", "dragon rider part 2.m4b", "filename", 0.3},
		{"b3", "dragon rider.m4b", "filename", 0.3},
	}
	if fmt.Sprint(have) != fmt.Sprint(want) {
		t.Errorf("matches changed:\n got %v\nwant %v", have, want)
	}
	var unmatched []string
	for _, b := range res.UnmatchedBooks {
		unmatched = append(unmatched, b.BookID)
	}
	if fmt.Sprint(unmatched) != "[b4 b6]" {
		t.Errorf("unmatched = %v, want [b4 b6]", unmatched)
	}
}
