// file: core/app/operator_config.go
// version: 0.1.0
// guid: e6a2f9c4-3b17-4d58-9c0e-71d4b8a5f2c3
// last-edited: 2026-11-20

package app

import (
	"context"
	"slices"

	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/module"
)

// OperatorConfigAccess is the configuration OperatorConfig loads: module.ConfigAccess
// over the operator layers alone, closed by the caller. Its writes return
// kernel.ErrOperatorConfigReadOnly.
type OperatorConfigAccess interface {
	module.ConfigAccess
	Close(ctx context.Context) error
}

// OperatorConfig is mods' configuration as opts' operator layers alone give
// it (kernel.OperatorConfig): the compiled defaults (opts.CoreDefaults and
// opts.StateDir among them), the operator file, the environment and
// opts.ConfigFlags, judged and decoded as a boot over opts judges them, with
// no stored value. It opens no store, reads no boot record and creates no
// directory, so an app can read its settings before New, or in a command that
// never boots. A retired name that would refuse the boot refuses here, as the
// *config.RetiredNameError.
func OperatorConfig(ctx context.Context, opts Options, mods []module.Module) (OperatorConfigAccess, error) {
	if opts.EnvPrefix != "" {
		if _, err := envPrefix(opts); err != nil {
			return nil, err
		}
	}
	acc, err := kernel.OperatorConfig(ctx, kernel.ConfigCheck{
		Config:          opts.configOptions(),
		StateDirDefault: opts.StateDir,
		Modules:         slices.Clone(mods),
		Defaults:        kernel.CoreDefaults(opts.CoreDefaults),
	})
	if err != nil {
		return nil, err
	}
	return acc, nil
}
