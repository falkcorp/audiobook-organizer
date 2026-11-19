// file: core/kernel/config_keys.go
// version: 1.0.1
// guid: 7c2e4a90-3b1d-4f68-8a15-d09e6b3f2c71
// last-edited: 2026-11-18

package kernel

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"

	"github.com/falkcorp/mediacore/config"
)

// CoreKeys returns every key core's own sections declare, as a store built
// over them reports it: the path, the reload and protection class, the
// schema and whether the key is secret or FailClosed (Effective holds the
// declared default; Source is the default layer). The store has no
// operator layer (no environment, no file) and no database layer, so the
// result is the same on every host.
//
// It exists for the gate tests that compare an app's tables with core's
// registry (ABO's TestRenameTable_*): they live outside this module and
// cannot reach the unexported section list. It builds a store per call and
// closes it; nothing at boot calls it.
func CoreKeys() (map[config.Path]config.KeyState, error) {
	ctx := context.Background()
	st, err := config.NewStore(config.Options{
		AppID:   "corekeys",
		Environ: []string{},
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		return nil, fmt.Errorf("kernel: core keys: %w", err)
	}
	if _, err := config.RegisterSections(st.Registry(), coreSections()...); err != nil {
		return nil, fmt.Errorf("kernel: core keys: register the core sections: %w", err)
	}
	if err := st.Load(ctx); err != nil {
		return nil, fmt.Errorf("kernel: core keys: load: %w", err)
	}
	out := maps.Collect(st.Keys())
	if err := st.Close(ctx); err != nil {
		return nil, fmt.Errorf("kernel: core keys: close: %w", err)
	}
	return out, nil
}
