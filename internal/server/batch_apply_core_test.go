// file: internal/server/batch_apply_core_test.go
// version: 1.0.0
// guid: e8c54d4d-e19d-4e7f-9c58-2017c7edfe5c
// last-edited: 2026-11-18

package server

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

// batchApplyNotReady is a writer whose service check refuses and which records
// whether the apply state was opened.
type batchApplyNotReady struct{ opened bool }

func (w *batchApplyNotReady) Ready() error { return errors.New("not ready") }

func (w *batchApplyNotReady) OpenBatchApply(context.Context) (batchApplyRun, error) {
	w.opened = true
	return nil, errors.New("must not open")
}

// HEAD refused a server with no metadata fetch service right after the scan
// stand-down and before it logged or reported anything: nothing is emitted, the
// apply state is never opened, and the hold is released with the refusal.
func TestBatchApplyCore_RefusesAnUnreadyServiceBeforeReporting(t *testing.T) {
	w := &batchApplyNotReady{}
	var emitted []string
	var finished error
	env := batchApplyEnv{
		t7Env: t7Env{
			Log:      func(slog.Level, string, ...slog.Attr) { emitted = append(emitted, "log") },
			Progress: func(int, int, string) { emitted = append(emitted, "progress") },
		},
		Writer: w,
		StandDown: func(context.Context, string) (t7Hold, error) {
			return t7Hold{Finish: func(runErr error) error { finished = runErr; return runErr }}, nil
		},
	}
	err := batchApplyCore(context.Background(), batchApplyOpParams{}, env)
	require.EqualError(t, err, "not ready")
	require.Empty(t, emitted, "nothing is reported before the service check")
	require.False(t, w.opened)
	require.EqualError(t, finished, "not ready")
}
