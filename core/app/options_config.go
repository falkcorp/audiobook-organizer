// file: core/app/options_config.go
// version: 1.0.0
// guid: a795a119-3517-4333-9ffd-c42d723fff59
// last-edited: 2026-11-10

package app

import (
	"log/slog"
	"os"
	"slices"

	"github.com/falkcorp/mediacore/config"
)

// configOptions is the part of config.Options that app.Options carries
// (05-config §1.3.3, 06 §6.4.5): the app's identity and env prefix, the
// environment captured once (Options.Env, else os.Environ), the operator
// file, and the two F3 lists, copied so the caller's slices stay its own:
// RetiredConfigNames into Retired and ConfigEnvBindings into EnvBindings.
// The kernel adds the flags, the boot-record root, metrics and secrets. Core
// holds no app name: the lists arrive only through here (03 R1).
func (o Options) configOptions() config.Options {
	env := o.Env
	if env == nil {
		env = os.Environ()
	}
	log := o.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return config.Options{
		AppID:       o.AppID,
		EnvPrefix:   o.EnvPrefix,
		Environ:     slices.Clone(env),
		FilePath:    o.ConfigFile,
		Retired:     slices.Clone(o.RetiredConfigNames),
		EnvBindings: slices.Clone(o.ConfigEnvBindings),
		Logger:      log,
	}
}
