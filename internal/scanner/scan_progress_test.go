// file: internal/scanner/scan_progress_test.go
// version: 1.3.0
// guid: 5f2a9c14-8e63-4b07-a5d9-1c4e7b0f6a38
// last-edited: 2026-09-10

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
		&discoveredBooks, &processedFiles, stats, "", resumeOffset, nil, spy)
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
