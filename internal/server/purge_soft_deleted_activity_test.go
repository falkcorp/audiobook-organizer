// file: internal/server/purge_soft_deleted_activity_test.go
// version: 1.0.0
// guid: 3b8f1d27-6c40-4e95-a7d2-0e5c91b84f36
// last-edited: 2026-11-19

package server

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
)

// purgeActivityEntries are the activity rows the purge op queued on w for opID.
func purgeActivityEntries(w *activity.Writer, opID string) []string {
	var out []string
	for _, e := range drainActivity(w) {
		if e.OperationID == opID && e.Type == purgeSoftDeletedOpID {
			out = append(out, e.Summary)
		}
	}
	return out
}

// HEAD wrote the purge's activity row on a dry run as well as a real run, and
// dry run is the default, so it is the common run. The shell must keep doing so.
func TestPurgeSoftDeletedOp_HeadPreviewWritesItsActivityRow(t *testing.T) {
	s, _ := purgeOpWorld(t, "a", "b")
	s.activityWriter = activity.NewWriter(nil, 16)

	rep, err := runPurgeOp(t, s, `{}`)
	require.NoError(t, err)
	res := purgeResultOf(t, rep)
	require.True(t, res.DryRun)

	rows := purgeActivityEntries(s.activityWriter, rep.OpID())
	require.NotEmpty(t, rows, "the preview must emit its summary row, as HEAD did")
	assert.Contains(t, rows, res.Summary())
}

// recordingPurgeWriter is a live-only writer that records the activity it is
// asked to write.
type recordingPurgeWriter struct {
	emitted []string
	flushed int
}

func (w *recordingPurgeWriter) RunPurge(context.Context, audiobookspkg.PurgeOptions) (*audiobookspkg.PurgeResult, error) {
	return &audiobookspkg.PurgeResult{}, nil
}
func (w *recordingPurgeWriter) FlushActivityOp(string) { w.flushed++ }
func (w *recordingPurgeWriter) EmitActivityInfo(_, _, _, summary string, _ ...string) {
	w.emitted = append(w.emitted, summary)
}
func (w *recordingPurgeWriter) LogActivityBatch(string, string, string, activity.BatchItem) {}

type previewOnlyPurgeStore struct{}

func (previewOnlyPurgeStore) RunPurgePreview(context.Context, audiobookspkg.PurgeOptions) (*audiobookspkg.PurgeResult, error) {
	return &audiobookspkg.PurgeResult{DryRun: true}, nil
}

// The native Preview builds no writer, so it writes no activity row; the
// native Live run, which has one, does. The gate is the writer, not the mode.
func TestPurgeSoftDeletedCore_ActivityFollowsTheWriter(t *testing.T) {
	base := t7Env{Log: func(slog.Level, string, ...slog.Attr) {}, OpID: "op-1"}

	preview := purgeSoftDeletedEnv{t7Env: base, Store: previewOnlyPurgeStore{}}
	_, err := purgeSoftDeletedCore(context.Background(), purgeSoftDeletedNativeParams{}, preview)
	require.NoError(t, err, "a preview with no writer must not touch one")

	w := &recordingPurgeWriter{}
	live := base
	live.Live = true
	_, err = purgeSoftDeletedCore(context.Background(), purgeSoftDeletedNativeParams{}, purgeSoftDeletedEnv{t7Env: live, Writer: w})
	require.NoError(t, err)
	assert.Len(t, w.emitted, 1)
	assert.Equal(t, 1, w.flushed)

	// A HEAD-shaped preview carries the writer too: it emits.
	hw := &recordingPurgeWriter{}
	_, err = purgeSoftDeletedCore(context.Background(), purgeSoftDeletedNativeParams{}, purgeSoftDeletedEnv{t7Env: base, Store: previewOnlyPurgeStore{}, Writer: hw})
	require.NoError(t, err)
	assert.Len(t, hw.emitted, 1, "HEAD's preview wrote its row")

	// No op id, no row.
	noID := base
	noID.OpID = ""
	nw := &recordingPurgeWriter{}
	_, err = purgeSoftDeletedCore(context.Background(), purgeSoftDeletedNativeParams{}, purgeSoftDeletedEnv{t7Env: noID, Store: previewOnlyPurgeStore{}, Writer: nw})
	require.NoError(t, err)
	assert.Empty(t, nw.emitted)
}
