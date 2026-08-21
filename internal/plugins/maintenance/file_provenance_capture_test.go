// file: internal/plugins/maintenance/file_provenance_capture_test.go
// version: 1.0.0
// guid: 6b40f9d2-7c15-4e83-a09b-2d5e1f847c36
// last-edited: 2026-08-21

package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// provDeps supplies a real provenance store to the op under test.
type provDeps struct {
	fakeDeps
	prov database.FileProvenanceStore
}

func (d provDeps) FileProvenanceStore() database.FileProvenanceStore { return d.prov }

func newCaptureFixture(t *testing.T, files map[string]string) (*Plugin, *database.PebbleStore, string) {
	t.Helper()

	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	root := t.TempDir()
	for name, content := range files {
		full := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}

	return New(provDeps{prov: store}), store, root
}

func runCapture(t *testing.T, p *Plugin, params map[string]any) fileProvCaptureResult {
	t.Helper()
	raw, err := json.Marshal(params)
	require.NoError(t, err)

	res, err := p.captureFileProvenance(context.Background(), raw)
	require.NoError(t, err)
	return res
}

func TestFileProvenanceCaptureRequiresRoots(t *testing.T) {
	p, _, _ := newCaptureFixture(t, nil)
	_, err := p.captureFileProvenance(context.Background(), json.RawMessage(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "root")
}

// A dry run must not hash. Hashing is the entire cost of the op; a dry run as
// expensive as the real thing would never be run first.
func TestFileProvenanceCaptureDryRunHashesNothingAndWritesNothing(t *testing.T) {
	p, store, root := newCaptureFixture(t, map[string]string{
		"a.m4b":      "aaa",
		"sub/b.mp3":  "bbb",
		"notes.txt":  "ignored",
		"cover.jpeg": "ignored",
	})

	res := runCapture(t, p, map[string]any{"roots": []string{root}})

	assert.Equal(t, 2, res.Walked, "only audio files should be walked")
	assert.Zero(t, res.Hashed, "a dry run must not hash")
	assert.Zero(t, res.Recorded)
	for _, s := range res.Samples {
		assert.Equal(t, "would-capture", s.Outcome)
		assert.Empty(t, s.SHA256, "a dry run must not report a hash it did not compute")
	}

	found, err := store.FindFileEventsByHash("anything")
	require.NoError(t, err)
	assert.Empty(t, found)
}

func TestFileProvenanceCaptureApplyRecordsOrphanEvents(t *testing.T) {
	p, store, root := newCaptureFixture(t, map[string]string{"a.m4b": "pristine-bytes"})

	res := runCapture(t, p, map[string]any{"roots": []string{root}, "apply": true})

	assert.Equal(t, 1, res.Walked)
	assert.Equal(t, 1, res.Hashed)
	assert.Equal(t, 1, res.Recorded)
	require.Len(t, res.Samples, 1)
	require.NotEmpty(t, res.Samples[0].SHA256)

	// The event is an orphan — no book_file row exists yet — and is reachable
	// by the hash that was captured.
	found, err := store.FindFileEventsByHash(res.Samples[0].SHA256)
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Empty(t, found[0].BookFileID, "a pre-import capture must be an orphan")
	assert.Equal(t, database.FileEventObserved, found[0].Kind)
	assert.Equal(t, filepath.Join(root, "a.m4b"), found[0].Path)
	assert.EqualValues(t, len("pristine-bytes"), found[0].Digest.SizeBytes)
}

// Re-running must not pile duplicate rows into an append-only store.
func TestFileProvenanceCaptureIsIdempotent(t *testing.T) {
	p, store, root := newCaptureFixture(t, map[string]string{"a.m4b": "pristine-bytes"})

	first := runCapture(t, p, map[string]any{"roots": []string{root}, "apply": true})
	require.Equal(t, 1, first.Recorded)

	second := runCapture(t, p, map[string]any{"roots": []string{root}, "apply": true})
	assert.Zero(t, second.Recorded, "the second sweep re-recorded a file it already knew")
	assert.Equal(t, 1, second.AlreadyKnown)

	found, err := store.FindFileEventsByHash(first.Samples[0].SHA256)
	require.NoError(t, err)
	assert.Len(t, found, 1, "a duplicate event was appended")
}

// A changed file is a genuinely new observation and must be recorded, even
// though the path is one the sweep has seen before.
func TestFileProvenanceCaptureRecordsAFileWhoseContentChanged(t *testing.T) {
	p, store, root := newCaptureFixture(t, map[string]string{"a.m4b": "before"})

	first := runCapture(t, p, map[string]any{"roots": []string{root}, "apply": true})
	require.Equal(t, 1, first.Recorded)

	require.NoError(t, os.WriteFile(filepath.Join(root, "a.m4b"), []byte("after"), 0o644))

	second := runCapture(t, p, map[string]any{"roots": []string{root}, "apply": true})
	assert.Equal(t, 1, second.Recorded, "a changed file must produce a new observation")
	assert.Zero(t, second.AlreadyKnown)

	// Both digests remain resolvable — that is the point of the ledger.
	for _, sha := range []string{first.Samples[0].SHA256, second.Samples[0].SHA256} {
		found, err := store.FindFileEventsByHash(sha)
		require.NoError(t, err)
		assert.Len(t, found, 1, "digest %s stopped resolving", sha)
	}
}

// The cap must be reported, never silently applied.
func TestFileProvenanceCaptureReportsWhatTheCapLeftOut(t *testing.T) {
	p, _, root := newCaptureFixture(t, map[string]string{
		"a.m4b": "one", "b.m4b": "two", "c.m4b": "three",
	})

	res := runCapture(t, p, map[string]any{"roots": []string{root}, "apply": true, "max": 2})

	assert.Equal(t, 3, res.Walked)
	assert.Equal(t, 2, res.Hashed)
	assert.Equal(t, 1, res.Capped, "the uncaptured file must be counted, not silently dropped")
}

func TestFileProvenanceCaptureHonoursCustomExtensions(t *testing.T) {
	p, _, root := newCaptureFixture(t, map[string]string{"a.m4b": "x", "b.weird": "y"})

	res := runCapture(t, p, map[string]any{"roots": []string{root}, "extensions": []string{".weird"}})

	assert.Equal(t, 1, res.Walked)
	require.Len(t, res.Samples, 1)
	assert.Equal(t, filepath.Join(root, "b.weird"), res.Samples[0].Path)
}

// An unreadable root is counted, not fatal — one bad mount must not abort a
// sweep across several.
func TestFileProvenanceCaptureSurvivesAMissingRoot(t *testing.T) {
	p, _, root := newCaptureFixture(t, map[string]string{"a.m4b": "x"})

	res := runCapture(t, p, map[string]any{
		"roots": []string{filepath.Join(root, "nope"), root},
		"apply": true,
	})

	assert.Equal(t, 1, res.Recorded, "the good root was not swept")
	assert.Positive(t, res.Errors, "the missing root was not counted as an error")
}

func TestFileProvenanceCaptureFailsWithoutAProvenanceStore(t *testing.T) {
	p := New(provDeps{prov: nil})
	_, err := p.captureFileProvenance(context.Background(),
		json.RawMessage(`{"roots":["/tmp"]}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not initialized")
}

// An unreadable directory INSIDE a root must not abandon the rest of that root.
//
// This is a distinct case from a missing root, which the per-root loop already
// tolerates: here the failure happens partway through a single WalkDir, and
// returning the error would silently skip every file after it. A capture sweep
// that quietly stops early is the exact failure this op exists to prevent.
func TestFileProvenanceCaptureContinuesPastAnUnreadableSubdirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a 0000 directory, so the error cannot be provoked")
	}

	p, _, root := newCaptureFixture(t, map[string]string{
		"aaa-first.m4b":     "one",
		"mmm-blocked/x.m4b": "two",
		"zzz-last.m4b":      "three",
	})

	// Sorts between the two readable files, so a walk that aborts here loses
	// zzz-last.m4b.
	blocked := filepath.Join(root, "mmm-blocked")
	require.NoError(t, os.Chmod(blocked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	res := runCapture(t, p, map[string]any{"roots": []string{root}, "apply": true})

	assert.Positive(t, res.Errors, "the unreadable directory was not counted")
	assert.Equal(t, 2, res.Recorded,
		"the sweep stopped at the unreadable directory instead of continuing past it")
}
