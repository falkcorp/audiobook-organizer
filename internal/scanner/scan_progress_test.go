// file: internal/scanner/scan_progress_test.go
// version: 1.5.0
// guid: 5f2a9c14-8e63-4b07-a5d9-1c4e7b0f6a38
// last-edited: 2026-09-12

package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

// progressEntry is one recorded UpdateProgress call, kept structured so tests
// can assert on the numeric denominator, not just the count of checkpoints.
type progressEntry struct {
	current int
	total   int
	message string
}

// progressSpy records UpdateProgress calls. It embeds logger.Logger so the
// rest of the (large) interface comes from a real logger, and overrides With
// to return itself -- callers wrap the logger before handing it down, and a
// spy that lost its identity on With would silently observe nothing.
type progressSpy struct {
	logger.Logger
	mu      sync.Mutex
	calls   []string
	entries []progressEntry
}

func (p *progressSpy) UpdateProgress(current, total int, message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, fmt.Sprintf("%d/%d %s", current, total, message))
	p.entries = append(p.entries, progressEntry{current: current, total: total, message: message})
}

func (p *progressSpy) With(string) logger.Logger { return p }

func (p *progressSpy) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *progressSpy) snapshot() []progressEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]progressEntry(nil), p.entries...)
}

// TestScanDirectoryParallel_ChecksInLongEnoughToSurviveTheWatchdog pins the
// 2026-08-16 fix.
//
// Both phases of ScanDirectoryParallel -- the WalkDir that discovers
// directories and the parallel pass that reads each one -- used to run without
// a single UpdateProgress call. The stuck-op watchdog kills an operation after
// 5 minutes of silence, so on a large import root the scan was killed while
// the process was demonstrably busy. That is precisely how the 2026-08-16
// rescan died, mid-walk of a folder holding 17,469 books.
//
// The assertion is deliberately about the COUNT of checkpoints, not merely
// that one happened: a single call at the start or the end would satisfy
// "reports progress" while leaving an arbitrarily long silent stretch in the
// middle, which is the actual defect.
func TestScanDirectoryParallel_ChecksInLongEnoughToSurviveTheWatchdog(t *testing.T) {
	root := t.TempDir()

	// scanProgressEvery*3 directories, so a correct implementation must check
	// in several times in each phase rather than once at a boundary.
	const dirCount = scanProgressEvery * 3
	for i := range dirCount {
		d := filepath.Join(root, fmt.Sprintf("book-%03d", i))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// A supported audio file so the directory is real work, not skipped.
		if err := os.WriteFile(filepath.Join(d, "track.mp3"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	prev := config.AppConfig.SupportedExtensions
	config.AppConfig.SupportedExtensions = []string{".mp3"}
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = prev })

	spy := &progressSpy{Logger: logger.New("test")}

	if _, err := ScanDirectoryParallel(context.Background(), root, 4, spy); err != nil {
		t.Fatalf("ScanDirectoryParallel: %v", err)
	}

	got := spy.count()
	// dirCount directories in the scan phase alone yields dirCount/every
	// checkpoints; the discovery walk adds its own. Require at least the scan
	// phase's share so the test fails if either phase goes silent.
	want := dirCount / scanProgressEvery
	if got < want {
		t.Errorf("got %d progress checkpoints for %d directories, want >= %d — "+
			"a phase is running silently and the stuck-op watchdog will kill long scans",
			got, dirCount, want)
	}
}

// TestScanDirectoryParallel_ReportsInsideOneHugeDirectory covers the shape
// that per-directory checkpoints cannot see.
//
// The first version of this fix counted directories only. That is fine for a
// library organized as a folder per book, but a library kept as ONE flat
// folder of tens of thousands of files has exactly one directory: the
// discovery walk reports nothing (1 % 20 != 0) and the scan phase reports once,
// after all the work is already done. The whole scan is a single silent stretch
// and the watchdog kills it -- the same failure, just reached differently.
//
// So this asserts checkpoints happen with dirCount == 1, which is false for
// any directory-counting implementation no matter how small its interval.
func TestScanDirectoryParallel_ReportsInsideOneHugeDirectory(t *testing.T) {
	root := t.TempDir()

	const fileCount = scanProgressEvery * 3
	for i := range fileCount {
		f := filepath.Join(root, fmt.Sprintf("track-%03d.mp3", i))
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	prev := config.AppConfig.SupportedExtensions
	config.AppConfig.SupportedExtensions = []string{".mp3"}
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = prev })

	spy := &progressSpy{Logger: logger.New("test")}

	if _, err := ScanDirectoryParallel(context.Background(), root, 4, spy); err != nil {
		t.Fatalf("ScanDirectoryParallel: %v", err)
	}

	got := spy.count()
	want := fileCount / scanProgressEvery
	if got < want {
		t.Errorf("got %d checkpoints scanning %d files in a SINGLE directory, want >= %d — "+
			"a flat library reports only per-directory and will be killed mid-scan",
			got, fileCount, want)
	}
}

// TestScanDirectoryParallel_IndeterminatePhasesReportZeroTotal pins the
// 2026-09-09 fix for the "total counts up" symptom.
//
// The discovery walk and the tag-reading pass have no bounded denominator while
// they run, so they must report an INDETERMINATE total (0) — which the UI renders
// as an animated bar plus the count. Until this fix they reported total==current
// (both climbing), which the UI rendered as a determinate bar pinned at 100%
// while a number raced upward: the user-visible "total counts up" bug. The
// per-directory "Scanning folders" phase, by contrast, DOES know its denominator
// (len(dirs)) and must keep reporting a real, positive total.
func TestScanDirectoryParallel_IndeterminatePhasesReportZeroTotal(t *testing.T) {
	root := t.TempDir()
	const dirCount = scanProgressEvery * 3
	for i := range dirCount {
		d := filepath.Join(root, fmt.Sprintf("book-%03d", i))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(d, "track.mp3"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	prev := config.AppConfig.SupportedExtensions
	config.AppConfig.SupportedExtensions = []string{".mp3"}
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = prev })

	spy := &progressSpy{Logger: logger.New("test")}
	if _, err := ScanDirectoryParallel(context.Background(), root, 4, spy); err != nil {
		t.Fatalf("ScanDirectoryParallel: %v", err)
	}

	sawScanningFolders := false
	for _, e := range spy.snapshot() {
		switch {
		case strings.HasPrefix(e.message, "Discovering folders:"),
			strings.HasPrefix(e.message, "Reading tags:"):
			if e.total != 0 {
				t.Errorf("indeterminate phase %q reported total=%d, want 0 "+
					"(a determinate bar pinned at 100%% is the bug this fixes)",
					e.message, e.total)
			}
		case strings.HasPrefix(e.message, "Scanning folders:"):
			sawScanningFolders = true
			if e.total <= 0 {
				t.Errorf("bounded phase %q reported total=%d, want the real dir count (>0)",
					e.message, e.total)
			}
		}
	}
	if !sawScanningFolders {
		t.Fatal("expected at least one 'Scanning folders' checkpoint with a real denominator")
	}
}

// TestScanFolder_ProgressStaysWithinDenominatorOnResume pins the 2026-09-09
// per-book denominator change on the one path where "current <= total" is not
// obvious: a resumed folder.
//
// scanFolder seeds processedFiles with the resume offset (the books a previous
// run already finished) and only then runs the per-book callback for the
// REMAINING books. If the seed and the denominator were ever computed from
// different bases — the exact bug the old max()-patched, file-unit estimate
// produced — the bar could report current > total, i.e. more than 100%. This
// drives the real scanFolder with a nonzero itemOffset and asserts every
// reported checkpoint keeps current <= total, current > 0, total > 0, and that
// the run ends exactly at current == total == the discovered book count.
//
// saveBook is stubbed to a no-op so the test needs no store: ProcessBooksParallel
// guards every getStore() call for nil, and saveBook is the only unconditional
// persistence hook.
func TestScanFolder_ProgressStaysWithinDenominatorOnResume(t *testing.T) {
	root := t.TempDir()
	const bookCount = 12
	for i := range bookCount {
		d := filepath.Join(root, fmt.Sprintf("book-%03d", i))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(d, "track.mp3"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	prevExt := config.AppConfig.SupportedExtensions
	config.AppConfig.SupportedExtensions = []string{".mp3"}
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = prevExt })

	prevSave := saveBook
	saveBook = func(context.Context, *Book) error { return nil }
	t.Cleanup(func() { saveBook = prevSave })

	const resumeOffset = 5 // pretend a prior run finished the first 5 books
	spy := &progressSpy{Logger: logger.New("test")}
	ss := &ScanService{db: &database.MockStore{}} // satisfies updateImportPathBookCount

	var discoveredBooks atomic.Int64
	var processedFiles atomic.Int32
	stats := &ScanStats{}

	err := ss.scanFolder(context.Background(), 0, root, []string{root},
		&discoveredBooks, &processedFiles, stats, "", resumeOffset, nil, nil, spy)
	if err != nil {
		t.Fatalf("scanFolder: %v", err)
	}

	sawProcessed := false
	for _, e := range spy.snapshot() {
		if !strings.HasPrefix(e.message, "Processed:") {
			continue
		}
		sawProcessed = true
		if e.total <= 0 {
			t.Errorf("processed checkpoint %q reported total=%d, want the book count (>0)", e.message, e.total)
		}
		if e.current <= 0 {
			t.Errorf("processed checkpoint %q reported current=%d, want >0", e.message, e.current)
		}
		if e.current > e.total {
			t.Errorf("processed checkpoint %q reported current=%d > total=%d — "+
				"the resume seed and the denominator disagree (the >100%% bug this fix removes)",
				e.message, e.current, e.total)
		}
	}
	if !sawProcessed {
		t.Fatal("expected at least one 'Processed:' checkpoint")
	}
	if got := int(discoveredBooks.Load()); got != bookCount {
		t.Errorf("discoveredBooks=%d, want %d (the real book count)", got, bookCount)
	}
	if got := int(processedFiles.Load()); got != bookCount {
		t.Errorf("processedFiles=%d, want %d — resume seed (%d) plus %d processed books must total the folder count",
			got, bookCount, resumeOffset, bookCount-resumeOffset)
	}
}

// TestScanFolder_SubStepsNeverReplaceTheScanCounters pins the 2026-09-12 fix
// for "why does the library scan randomly think it's scanning 0/1 and then
// back to 60,000?".
//
// One op has one (current, total) row. Before the fix, the sub-steps scanFolder
// hands its logger to wrote their OWN denominators into it: ScanDirectoryParallel
// wrote "Scanning folders: 1/1" for an import folder holding one book, and the
// auto-organize hook wrote (0, 1) for its backup and (n, len(this folder's
// books)) for its loop. Each write replaced the scan's cumulative count, so the
// bar flipped between 0/1, 1/1 and the real total.
//
// This drives the real scanFolder across two folders -- three books, then one
// -- with an AutoOrganizeFn that reports exactly what the organize pipeline
// reports (including through a With child, as the organizer does). It asserts
// the recorded sequence never shows a total below the running cumulative total
// or a current below the running processed count, and that the auto-organize
// step is still visible in the message.
func TestScanFolder_SubStepsNeverReplaceTheScanCounters(t *testing.T) {
	mkFolder := func(books int) string {
		root := t.TempDir()
		for i := range books {
			d := filepath.Join(root, fmt.Sprintf("book-%03d", i))
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(d, "track.mp3"), []byte("x"), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
		}
		return root
	}
	folders := []string{mkFolder(3), mkFolder(1)}

	prevExt := config.AppConfig.SupportedExtensions
	config.AppConfig.SupportedExtensions = []string{".mp3"}
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = prevExt })

	prevSave := saveBook
	saveBook = func(context.Context, *Book) error { return nil }
	t.Cleanup(func() { saveBook = prevSave })

	spy := &progressSpy{Logger: logger.New("test")}
	ss := &ScanService{db: &database.MockStore{}}
	var organizeCalls atomic.Int32
	ss.AutoOrganizeFn = func(_ context.Context, books []Book, l logger.Logger) {
		organizeCalls.Add(1)
		// organizer/service.go's pre-organize backup and its organize loop.
		l.UpdateProgress(0, 1, "Backing up database before organize")
		l.With("organizer").UpdateProgress(0, len(books), fmt.Sprintf("Organizing 0/%d books", len(books)))
	}

	var discoveredBooks atomic.Int64
	var processedFiles atomic.Int32
	stats := &ScanStats{}
	for i, folder := range folders {
		if err := ss.scanFolder(context.Background(), i, folder, folders,
			&discoveredBooks, &processedFiles, stats, "", 0, nil, nil, spy); err != nil {
			t.Fatalf("scanFolder(%s): %v", folder, err)
		}
	}

	if got := organizeCalls.Load(); got != 2 {
		t.Fatalf("AutoOrganizeFn ran %d times, want 2 (once per folder)", got)
	}

	entries := spy.snapshot()
	maxCurrent, maxTotal := 0, 0
	sawBackup, sawOrganize := false, false
	for i, e := range entries {
		if e.total < maxTotal {
			t.Errorf("entry %d %d/%d %q: total %d is below the cumulative total %d already reported -- "+
				"a sub-step replaced the scan's denominator with its own", i, e.current, e.total, e.message, e.total, maxTotal)
		}
		if e.current < maxCurrent {
			t.Errorf("entry %d %d/%d %q: current %d went backwards from %d",
				i, e.current, e.total, e.message, e.current, maxCurrent)
		}
		maxCurrent = max(maxCurrent, e.current)
		maxTotal = max(maxTotal, e.total)
		if strings.HasPrefix(e.message, "Auto-organize: ") {
			switch {
			case strings.Contains(e.message, "Backing up database"):
				sawBackup = true
			case strings.Contains(e.message, "Organizing"):
				sawOrganize = true
			}
		}
	}
	if !sawBackup || !sawOrganize {
		t.Errorf("auto-organize step not visible in progress messages (backup=%v organize=%v); "+
			"the sub-step must keep its message, only its numbers are replaced", sawBackup, sawOrganize)
	}
	if last := entries[len(entries)-1]; last.current != 4 || last.total != 4 {
		t.Errorf("last entry %d/%d %q, want 4/4 (both folders' books)", last.current, last.total, last.message)
	}
}

// recordingReporter is an operations.ProgressReporter that records every
// UpdateProgress. Wrapped by operations.LoggerFromReporter it gives the scan
// the same logger production does, whose With keeps the reporter -- unlike
// progressSpy, whose With returns itself and so cannot tell whether
// subStepLogger.With kept the substitution on a child logger.
type recordingReporter struct {
	mu      sync.Mutex
	entries []progressEntry
}

func (r *recordingReporter) UpdateProgress(current, total int, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, progressEntry{current: current, total: total, message: message})
	return nil
}
func (r *recordingReporter) Log(string, string, *string) error { return nil }
func (r *recordingReporter) IsCanceled() bool                  { return false }

func (r *recordingReporter) snapshot() []progressEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]progressEntry(nil), r.entries...)
}

// assertCumulative fails on any entry whose total drops below the largest
// total already published or whose current steps backwards.
func assertCumulative(t *testing.T, entries []progressEntry) {
	t.Helper()
	maxCurrent, maxTotal := 0, 0
	for i, e := range entries {
		if e.total < maxTotal {
			t.Errorf("entry %d %d/%d %q: total below cumulative %d -- a sub-step replaced the scan's denominator",
				i, e.current, e.total, e.message, maxTotal)
		}
		if e.current < maxCurrent {
			t.Errorf("entry %d %d/%d %q: current went backwards from %d", i, e.current, e.total, e.message, maxCurrent)
		}
		maxCurrent = max(maxCurrent, e.current)
		maxTotal = max(maxTotal, e.total)
	}
}

// TestScanFolder_SubStepsKeepCountersThroughTheRealReporterLogger runs the
// same two-folder scan as TestScanFolder_SubStepsNeverReplaceTheScanCounters
// but through operations.LoggerFromReporter, the logger library.scan really
// gets. Every former writer is driven for real: ScanDirectoryParallel's
// "Scanning folders: n/len(dirs)" (the second folder holds one book, so the
// old write was 1/1 after a total of 3), and the organize pipeline's (0, 1)
// backup and per-folder organize count, reported through a With child exactly
// as organizer.PerformOrganizeStats does. Deleting subStepLogger.With makes
// the With-child writes reach the reporter with their own numbers and fails
// this test.
func TestScanFolder_SubStepsKeepCountersThroughTheRealReporterLogger(t *testing.T) {
	mkFolder := func(books int) string {
		root := t.TempDir()
		for i := range books {
			d := filepath.Join(root, fmt.Sprintf("book-%03d", i))
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(d, "track.mp3"), []byte("x"), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
		}
		return root
	}
	folders := []string{mkFolder(3), mkFolder(1)}

	prevExt := config.AppConfig.SupportedExtensions
	config.AppConfig.SupportedExtensions = []string{".mp3"}
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = prevExt })

	prevSave := saveBook
	saveBook = func(context.Context, *Book) error { return nil }
	t.Cleanup(func() { saveBook = prevSave })

	rep := &recordingReporter{}
	opLog := operations.LoggerFromReporter(rep)
	ss := &ScanService{db: &database.MockStore{}}
	ss.AutoOrganizeFn = func(_ context.Context, books []Book, l logger.Logger) {
		l.UpdateProgress(0, 1, "Backing up database before organize")
		child := l.With("organizer")
		child.UpdateProgress(0, len(books), fmt.Sprintf("Scanning: 0/%d books", len(books)))
		child.UpdateProgress(len(books), len(books), fmt.Sprintf("Organized %d/%d", len(books), len(books)))
	}

	var discoveredBooks atomic.Int64
	var processedFiles atomic.Int32
	stats := &ScanStats{}
	for i, folder := range folders {
		if err := ss.scanFolder(context.Background(), i, folder, folders,
			&discoveredBooks, &processedFiles, stats, "", 0, nil, nil, opLog); err != nil {
			t.Fatalf("scanFolder(%s): %v", folder, err)
		}
	}

	entries := rep.snapshot()
	assertCumulative(t, entries)

	var sawFolderScan2, sawOrganized bool
	for _, e := range entries {
		// The walk counts the folder root and its one book dir, so the old
		// write here was 2/2 -- below the 3 books the scan had already found.
		if strings.HasPrefix(e.message, "Folder 2/2: Scanning folders: ") {
			sawFolderScan2 = true
			if e.total != 3 && e.total != 4 {
				t.Errorf("%q published total %d, want the scan's cumulative total (3 or 4)", e.message, e.total)
			}
		}
		if strings.HasPrefix(e.message, "Auto-organize: Organized") {
			sawOrganized = true
		}
	}
	if !sawFolderScan2 {
		t.Errorf("ScanDirectoryParallel's folder-scan message for folder 2 never reached the reporter; entries: %v", entries)
	}
	if !sawOrganized {
		t.Errorf("organize count written through a With child never reached the reporter as an Auto-organize message; entries: %v", entries)
	}
	if last := entries[len(entries)-1]; last.current != 4 || last.total != 4 {
		t.Errorf("last entry %d/%d %q, want 4/4", last.current, last.total, last.message)
	}
}

// TestScanProgress_SubStepSubstitutesEveryWriterShape drives each progress
// shape a sub-step writes -- discovery (n, 0), per-folder (n, len(dirs)), the
// AI parse phase's (batch, totalBatches), and the organize pipeline's (0, 1)
// -- through a subStep logger over the real reporter logger, with and without
// a With child. runAIBatchPhase needs a live AI parser to reach its write, so
// this is where that shape is pinned.
func TestScanProgress_SubStepSubstitutesEveryWriterShape(t *testing.T) {
	rep := &recordingReporter{}
	var processed atomic.Int32
	var discovered atomic.Int64
	processed.Store(40)
	discovered.Store(61000)
	p := newScanProgress(operations.LoggerFromReporter(rep), &processed, &discovered)
	sub := p.subStep(operations.LoggerFromReporter(rep).With("scanner"), "Folder 3/9")

	writers := []struct {
		current, total int
		message        string
	}{
		{7, 0, "Discovering folders: 7 found (x)"},
		{1, 1, "Scanning folders: 1/1 (x)"},
		{2, 5, "AI parsing batch 2/5 (50 books)"},
		{0, 1, "Backing up database before organize"},
	}
	for _, w := range writers {
		sub.UpdateProgress(w.current, w.total, w.message)
		sub.With("child").UpdateProgress(w.current, w.total, w.message)
	}

	entries := rep.snapshot()
	if len(entries) != 2*len(writers) {
		t.Fatalf("got %d progress writes, want %d (a sub-step write must be substituted, never dropped -- "+
			"it is the watchdog's liveness stamp)", len(entries), 2*len(writers))
	}
	for i, e := range entries {
		w := writers[i/2]
		if e.current != 40 || e.total != 61000 {
			t.Errorf("write %q published %d/%d, want the scan's 40/61000", w.message, e.current, e.total)
		}
		if want := "Folder 3/9: " + w.message; e.message != want {
			t.Errorf("message %q, want %q", e.message, want)
		}
	}
}

// TestScanProgressEveryIsATightEnoughBound guards the constant itself. It is
// the only thing standing between a directory-heavy scan and a watchdog kill,
// so a well-meaning bump to a large value would silently reintroduce the bug.
func TestScanProgressEveryIsATightEnoughBound(t *testing.T) {
	if scanProgressEvery <= 0 {
		t.Fatalf("scanProgressEvery must be positive, got %d", scanProgressEvery)
	}
	// 5m watchdog budget; even a pathologically slow directory (1s) must leave
	// a wide margin.
	if scanProgressEvery > 100 {
		t.Errorf("scanProgressEvery=%d is too coarse: at ~1s per slow directory "+
			"that is %ds between checkpoints against a 5m ProgressTimeout",
			scanProgressEvery, scanProgressEvery)
	}
}
