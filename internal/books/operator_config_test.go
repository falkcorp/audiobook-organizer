// file: internal/books/operator_config_test.go
// version: 0.1.0
// guid: 9e2c5a17-4d83-4f6b-b1a0-3c7e8d2f5b94
// last-edited: 2026-11-20

package books

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// operatorEnv isolates OperatorAppConfig from the host: a temporary HOME and
// state directory.
func operatorEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv(config.SecureStateDirEnv, state)
	return state
}

// TestOperatorAppConfig_HEADFormWhisperEndpoints (F3-07 rehearsal finding 2):
// a WHISPER_ENDPOINTS value in HEAD's form (url alone, no label) is accepted
// under its canonical name, as HEAD accepted it, and nothing is created.
func TestOperatorAppConfig_HEADFormWhisperEndpoints(t *testing.T) {
	state := operatorEnv(t)
	t.Setenv("ABO_BOOKS_WHISPER_ENDPOINTS", `[{"url":"http://192.0.2.10:9000"},{"url":"http://192.0.2.11:9000","concurrency":2,"label":"batcave"}]`)
	cfg, err := OperatorAppConfig(t.Context(), Module(), BootInputs{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.WhisperEndpoints) != 2 || cfg.WhisperEndpoints[0].URL != "http://192.0.2.10:9000" || cfg.WhisperEndpoints[1].Label != "batcave" {
		t.Errorf("WhisperEndpoints = %+v", cfg.WhisperEndpoints)
	}
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("state dir: stat err = %v, want it never created", err)
	}
}

// TestOperatorAppConfig_RefusesRetiredAndBase: a boot-refusing retired name
// refuses; with none, the base fields (no row stores them) hold their values.
func TestOperatorAppConfig_RefusesRetiredAndBase(t *testing.T) {
	operatorEnv(t)
	cfg, err := OperatorAppConfig(t.Context(), Module(), BootInputs{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseType != "pebble" || !cfg.AIEndpointsRouting {
		t.Errorf("base fields: DatabaseType %q, AIEndpointsRouting %v", cfg.DatabaseType, cfg.AIEndpointsRouting)
	}
	t.Setenv("PLAYLIST_DIR", "/srv/gotham/playlists")
	if _, err := OperatorAppConfig(t.Context(), Module(), BootInputs{}); err == nil {
		t.Fatal("a boot-refusing retired name was accepted")
	}
}
