// file: core/coretest/metrics_test.go
// version: 0.1.0
// guid: 6c1e9a47-2d8b-4f35-a0c6-3b7e5d914f28
// last-edited: 2026-11-20

package coretest_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/coretest"
)

// Every boot keeps a keyring and boot record in a state directory (F3-07):
// MetricsProvider gives an unset one t.TempDir(), so a caller that names only
// its metric identity still boots.
func TestMetricsProvider_DefaultsTheStateDir(t *testing.T) {
	p := coretest.MetricsProvider(t, app.Options{MetricNamespace: "gotham"})
	if _, err := p.Gatherer().Gather(); err != nil {
		t.Fatalf("Gather: %v", err)
	}
}

// NewMetricsProvider has no testing.TB to make and remove a directory, so an
// unset StateDir is refused naming the field, and a named one is the state
// directory the boot creates.
func TestNewMetricsProvider_StateDir(t *testing.T) {
	t.Run("unset is refused", func(t *testing.T) {
		p, err := coretest.NewMetricsProvider(context.Background(), app.Options{MetricNamespace: "gotham"})
		if err == nil {
			_ = p.Shutdown(context.Background())
			t.Fatal("NewMetricsProvider booted with no state directory")
		}
		if !strings.Contains(err.Error(), "StateDir") {
			t.Fatalf("error %q does not name StateDir", err)
		}
	})
	t.Run("a named directory is the boot's", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "arkham")
		p, err := coretest.NewMetricsProvider(context.Background(), app.Options{MetricNamespace: "gotham", StateDir: dir})
		if err != nil {
			t.Fatalf("NewMetricsProvider: %v", err)
		}
		t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			t.Fatalf("state directory %s was not created: %v", dir, err)
		}
	})
}
