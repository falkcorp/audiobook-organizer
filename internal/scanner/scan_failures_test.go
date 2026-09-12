// file: internal/scanner/scan_failures_test.go
// version: 1.0.0
// guid: 4c1d90e5-d60a-429e-b3c4-0a6908457406
// last-edited: 2026-09-12

package scanner

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

type capturedAttrLine struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

// captureAttrLogger is a logger.Logger that also implements logger.AttrLogger,
// standing in for operations.LoggerFromReporter's logger: what it captures is
// what would reach the operation's own log.
type captureAttrLogger struct {
	*logger.StandardLogger
	mu    sync.Mutex
	lines []capturedAttrLine
}

func newCaptureAttrLogger() *captureAttrLogger {
	return &captureAttrLogger{StandardLogger: logger.New("test")}
}

func (c *captureAttrLogger) LogAttrs(level slog.Level, msg string, attrs ...slog.Attr) {
	m := make(map[string]any, len(attrs))
	for _, a := range attrs {
		m[a.Key] = a.Value.Any()
	}
	c.mu.Lock()
	c.lines = append(c.lines, capturedAttrLine{level: level, msg: msg, attrs: m})
	c.mu.Unlock()
}

func (c *captureAttrLogger) snapshot() []capturedAttrLine {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]capturedAttrLine(nil), c.lines...)
}

func linesWithAttr(lines []capturedAttrLine, key string) []capturedAttrLine {
	var out []capturedAttrLine
	for _, l := range lines {
		if _, ok := l.attrs[key]; ok {
			out = append(out, l)
		}
	}
	return out
}

// TestFileFailuresBoundsSampleButCountsAll: a large failure set must not flood
// the op log, and must not be under-reported either.
func TestFileFailuresBoundsSampleButCountsAll(t *testing.T) {
	const n = 60
	c := &FileFailures{}
	log := newCaptureAttrLogger()
	for i := range n {
		c.Record(log, FileFailure{Path: fmt.Sprintf("library/f%02d.mp3", i), Stage: FileFailureStageRead, Reason: "boom"})
	}
	c.ReportSummary(log)

	assert.Equal(t, n, c.Total())
	assert.Len(t, c.Samples(), MaxFileFailureSamples)

	lines := log.snapshot()
	perFile := linesWithAttr(lines, "file_path")
	require.Len(t, perFile, MaxFileFailureSamples, "only the bounded sample is listed")
	assert.Equal(t, "library/f00.mp3", perFile[0].attrs["file_path"])
	for _, l := range perFile {
		assert.Equal(t, slog.LevelWarn, l.level)
	}

	summary := linesWithAttr(lines, "files_failed")
	require.Len(t, summary, 1)
	assert.EqualValues(t, n, summary[0].attrs["files_failed"])
	assert.EqualValues(t, MaxFileFailureSamples, summary[0].attrs["files_listed"])
	assert.EqualValues(t, n-MaxFileFailureSamples, summary[0].attrs["files_omitted"])

	// One notice at the cap, not one per unlisted failure.
	assert.Len(t, lines, MaxFileFailureSamples+2)
}

func TestFileFailuresNoFailuresWritesNothing(t *testing.T) {
	c := &FileFailures{}
	log := newCaptureAttrLogger()
	c.ReportSummary(log)
	assert.Empty(t, log.snapshot())
}

// TestProcessBooksParallelSurfacesUnreadableFile runs the real per-book
// pipeline over a temp dir holding one readable file and one the process
// cannot open, and asserts the unreadable one is surfaced -- path, stage and
// reason as structured attrs -- and counted exactly once.
func TestProcessBooksParallelSurfacesUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open a mode-000 file, so the unreadable fixture would not fail")
	}
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })

	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	oldExts := config.AppConfig.SupportedExtensions
	oldMin := config.AppConfig.MinBookSizeBytes
	t.Cleanup(func() {
		config.AppConfig.SupportedExtensions = oldExts
		config.AppConfig.MinBookSizeBytes = oldMin
	})
	config.AppConfig.SupportedExtensions = []string{".mp3"}
	config.AppConfig.MinBookSizeBytes = 0 // the suspicious-file guard would short-circuit tiny fixtures

	dir := t.TempDir()
	good := filepath.Join(dir, "good.mp3")
	require.NoError(t, os.WriteFile(good, []byte("audio-good"), 0o644))
	bad := filepath.Join(dir, "unreadable.mp3")
	require.NoError(t, os.WriteFile(bad, []byte("audio-bad"), 0o644))
	require.NoError(t, os.Chmod(bad, 0))
	t.Cleanup(func() { _ = os.Chmod(bad, 0o644) })

	failures := &FileFailures{}
	ctx := withFileFailures(context.Background(), failures)
	log := newCaptureAttrLogger()
	books := []Book{{FilePath: good, Title: "Good"}, {FilePath: bad, Title: "Unreadable"}}
	require.NoError(t, ProcessBooksParallel(ctx, books, 2, nil, log))

	require.Equal(t, 1, failures.Total(), "exactly the unreadable file fails")
	samples := failures.Samples()
	require.Len(t, samples, 1)
	assert.Equal(t, bad, samples[0].Path)
	assert.Equal(t, FileFailureStageRead, samples[0].Stage)
	assert.Contains(t, samples[0].Reason, "permission denied")

	lines := log.snapshot()
	perFile := linesWithAttr(lines, "file_path")
	require.Len(t, perFile, 1)
	assert.Equal(t, slog.LevelWarn, perFile[0].level)
	assert.Equal(t, bad, perFile[0].attrs["file_path"])
	assert.Equal(t, FileFailureStageRead, perFile[0].attrs["stage"])
	assert.Contains(t, perFile[0].attrs["reason"], "permission denied")

	// The collector came from ctx, so the summary belongs to its owner
	// (PerformScan), not to this chunk.
	assert.Empty(t, linesWithAttr(lines, "files_failed"))
	failures.ReportSummary(log)
	summary := linesWithAttr(log.snapshot(), "files_failed")
	require.Len(t, summary, 1)
	assert.EqualValues(t, 1, summary[0].attrs["files_failed"])
}
