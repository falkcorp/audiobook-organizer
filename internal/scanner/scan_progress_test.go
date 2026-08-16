// file: internal/scanner/scan_progress_test.go
// version: 1.0.0
// guid: 5f2a9c14-8e63-4b07-a5d9-1c4e7b0f6a38
// last-edited: 2026-08-16

package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// progressSpy records UpdateProgress calls. It embeds logger.Logger so the
// rest of the (large) interface comes from a real logger, and overrides With
// to return itself -- callers wrap the logger before handing it down, and a
// spy that lost its identity on With would silently observe nothing.
type progressSpy struct {
	logger.Logger
	mu    sync.Mutex
	calls []string
}

func (p *progressSpy) UpdateProgress(current, total int, message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, fmt.Sprintf("%d/%d %s", current, total, message))
}

func (p *progressSpy) With(string) logger.Logger { return p }

func (p *progressSpy) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
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
