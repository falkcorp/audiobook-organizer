// file: internal/server/scheduler_maintenance_window_update_test.go
// version: 1.0.0
// guid: 90753d6a-5bc6-4f4b-9015-67a0e8184581
// last-edited: 2026-11-05

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/updater"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mwLogReporter is a Reporter that records every log line of the run.
type mwLogReporter struct {
	mu   sync.Mutex
	logs []string
}

func (r *mwLogReporter) UpdateProgress(int, int, string) error { return nil }
func (r *mwLogReporter) Log(_ slog.Level, msg string, _ ...slog.Attr) error {
	r.mu.Lock()
	r.logs = append(r.logs, msg)
	r.mu.Unlock()
	return nil
}
func (r *mwLogReporter) Logger() *slog.Logger                       { return slog.New(slog.DiscardHandler) }
func (r *mwLogReporter) Checkpoint(any) error                       { return nil }
func (r *mwLogReporter) IsCanceled() bool                           { return false }
func (r *mwLogReporter) Trigger(context.Context, string, any) error { return nil }
func (r *mwLogReporter) SetCurrentItem(string)                      {}
func (r *mwLogReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, opsregistry.Reporter) error) error {
	return fn(ctx, r)
}

func (r *mwLogReporter) joined() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.logs, "\n")
}

// TestMaintenanceWindowStep1_CheckOnly: with automatic update enabled and a
// newer release published, step 1 of the maintenance window checks, records
// the release as the updater's last check (AutoApplyPaused set), logs it, and
// still writes maintenance_window_update_completed; it never downloads the
// asset, swaps the binary or exits.
func TestMaintenanceWindowStep1_CheckOnly(t *testing.T) {
	srv := setupMaintenanceTestServer(t)

	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.AutoUpdate.Enabled = true
	config.AppConfig.AutoUpdate.Channel = "stable"
	// Window closed: the task loop after step 1 stops at its first task, so
	// only step 1 runs.
	config.AppConfig.Maintenance.Enabled = false

	asset := fmt.Sprintf("audiobook-organizer-%s-%s", runtime.GOOS, runtime.GOARCH)
	var latestHits, assetHits atomic.Int32
	var rel *httptest.Server
	rel = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"), strings.HasSuffix(r.URL.Path, "/releases/tags/v0.224.0"):
			latestHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v0.224.0",
				"html_url": "https://example.invalid/releases/v0.224.0",
				"body":     "Hush",
				"assets": []map[string]string{{
					"name":                 asset,
					"browser_download_url": rel.URL + "/assets/" + asset,
				}},
			})
		case strings.HasPrefix(r.URL.Path, "/assets/"):
			assetHits.Add(1)
			_, _ = w.Write([]byte("new binary"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(rel.Close)

	exe := filepath.Join(t.TempDir(), "audiobook-organizer")
	require.NoError(t, os.WriteFile(exe, []byte("running binary"), 0o755))
	var exits atomic.Int32
	srv.updater = updater.NewUpdater("v0.223.0",
		updater.WithAPIBaseForTest(rel.URL),
		updater.WithHTTPClientForTest(rel.Client()),
		updater.WithExecutableForTest(func() (string, error) { return exe, nil }),
		updater.WithExitForTest(func(int) { exits.Add(1) }),
	)

	reg := capOpReg(t)
	require.NoError(t, srv.RegisterMaintenanceWindowOp(reg))
	def, ok := reg.Def("maintenance.window")
	require.True(t, ok)

	rep := &mwLogReporter{}
	require.NoError(t, def.Run(context.Background(), json.RawMessage(`{"ignore_window":false}`), rep))

	assert.Positive(t, latestHits.Load(), "step 1 must check for an update")
	last := srv.updater.LastCheck()
	require.NotNil(t, last, "step 1 must record the check")
	assert.True(t, last.UpdateAvailable)
	assert.Equal(t, "0.224.0", last.LatestVersion)
	assert.True(t, last.AutoApplyPaused, "an unattended check reports the paused auto-apply")
	assert.Contains(t, rep.joined(), "apply it from Settings")

	assert.Zero(t, assetHits.Load(), "step 1 must not download the asset")
	got, err := os.ReadFile(exe)
	require.NoError(t, err)
	assert.Equal(t, "running binary", string(got), "step 1 must not swap the binary")
	for _, suffix := range []string{".new", ".prev", ".old"} {
		_, statErr := os.Stat(exe + suffix)
		assert.True(t, os.IsNotExist(statErr), "%s must not exist", suffix)
	}
	// RestartSelf would run in a goroutine; give a stray one time to land.
	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, exits.Load(), "step 1 must never exit the process")

	done, err := srv.Ops().GetSetting("maintenance_window_update_completed")
	require.NoError(t, err)
	require.NotNil(t, done)
	assert.Equal(t, time.Now().Format("2006-01-02"), done.Value)
}
