// file: internal/operations/progress_attrs_test.go
// version: 1.0.0
// guid: 2b8a9374-5359-44ec-8ad4-3f7462e9a53c
// last-edited: 2026-09-12

package operations

import (
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

type attrsTestPlainLine struct {
	level, message string
	details        *string
}

type attrsTestPlainReporter struct{ lines []attrsTestPlainLine }

func (r *attrsTestPlainReporter) UpdateProgress(int, int, string) error { return nil }
func (r *attrsTestPlainReporter) Log(level, message string, details *string) error {
	r.lines = append(r.lines, attrsTestPlainLine{level: level, message: message, details: details})
	return nil
}
func (r *attrsTestPlainReporter) IsCanceled() bool { return false }

type attrsTestAttrLine struct {
	level   slog.Level
	message string
	attrs   []slog.Attr
}

type attrsTestAttrReporter struct {
	attrsTestPlainReporter
	attrLines []attrsTestAttrLine
}

func (r *attrsTestAttrReporter) LogAttrs(level slog.Level, message string, attrs ...slog.Attr) error {
	r.attrLines = append(r.attrLines, attrsTestAttrLine{level: level, message: message, attrs: attrs})
	return nil
}

// TestLoggerFromReporterForwardsStructuredAttrs: the scanner's per-file failure
// lines must reach the op log as attrs, including through With(), which is how
// the scan service hands its logger to ProcessBooksParallel.
func TestLoggerFromReporterForwardsStructuredAttrs(t *testing.T) {
	rep := &attrsTestAttrReporter{}
	al, ok := LoggerFromReporter(rep).With("scanner").(logger.AttrLogger)
	require.True(t, ok, "LoggerFromReporter's logger must implement logger.AttrLogger")

	al.LogAttrs(slog.LevelWarn, "scan: file failed",
		slog.String("file_path", "library/a.mp3"), slog.String("reason", "permission denied"))

	require.Len(t, rep.attrLines, 1)
	assert.Empty(t, rep.lines, "an AttrReporter must not also receive a flattened copy")
	got := rep.attrLines[0]
	assert.Equal(t, slog.LevelWarn, got.level)
	assert.Equal(t, "scan: file failed", got.message)
	require.Len(t, got.attrs, 2)
	assert.Equal(t, "file_path", got.attrs[0].Key)
	assert.Equal(t, "library/a.mp3", got.attrs[0].Value.String())
}

// TestLoggerFromReporterFallsBackToDetailsJSON: a reporter without LogAttrs
// still gets the attrs, as a JSON details payload, rather than losing them.
func TestLoggerFromReporterFallsBackToDetailsJSON(t *testing.T) {
	rep := &attrsTestPlainReporter{}
	al, ok := LoggerFromReporter(rep).(logger.AttrLogger)
	require.True(t, ok)

	al.LogAttrs(slog.LevelError, "scan: file failed",
		slog.String("file_path", "library/b.mp3"), slog.Int("n", 3))

	require.Len(t, rep.lines, 1)
	assert.Equal(t, "error", rep.lines[0].level)
	require.NotNil(t, rep.lines[0].details)
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(*rep.lines[0].details), &m))
	assert.Equal(t, "library/b.mp3", m["file_path"])
	assert.EqualValues(t, 3, m["n"])
}
