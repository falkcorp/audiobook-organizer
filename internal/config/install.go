// file: internal/config/install.go
// version: 1.0.1
// guid: 0c7e9b52-3d61-4a8f-b1e4-6f2a9d4c8e07
// last-edited: 2026-11-19

package config

import "sync/atomic"

// The installed Facade (F3-07 e2). Until the core config boot is on nothing is
// installed and every name in this package behaves as it always has: the
// process-wide Config, filled by InitConfig and the DB loader. With a Facade
// installed, AppConfig is its projection (Project replaces it on every rebuild)
// and the write and lock names delegate to it.

// installed holds the Facade behind the package-level names; the zero value
// means none.
var installed atomic.Pointer[Facade]

// Install binds f behind AppConfig, Snapshot and Mutate, and returns the Facade it replaced
// (nil if none) so a test can restore it. The books module installs its Facade
// from Register; nothing else should. Install(nil) unbinds.
func Install(f Facade) (prev Facade) {
	var old *Facade
	if f == nil {
		old = installed.Swap(nil)
	} else {
		old = installed.Swap(&f)
	}
	if old == nil {
		return nil
	}
	return *old
}

// Current is the installed Facade, or nil while none is: the settings routes,
// the Basic gate and telemetry take it from here.
func Current() Facade {
	if f := installed.Load(); f != nil {
		return *f
	}
	return nil
}

// UpdateHookRegistrar is a Facade that runs UpdateHooks after each committed
// Update (the books module's appconfig.Engine). A service whose live state
// follows a setting registers its hook on Current() while the server is built,
// before the Facade serves writes.
type UpdateHookRegistrar interface {
	OnUpdate(name string, fn UpdateHook)
}

// Project replaces AppConfig with cfg under the write lock and moves
// Generation. It is the Facade's rebuild step, called on every core generation
// change; no other caller should assign the whole Config.
func Project(cfg Config) {
	// Publish the normalised config, as HEAD's boot Validate and PUT did: the
	// rebuild hands over the raw rows, whose zeros mean "the default".
	cfg.Normalize()
	mutateLocal(func(c *Config) { *c = cfg })
}

// mutateRefused reports a Mutate the installed Facade refused. Callers of
// Mutate hold no error today, so the refusal is logged here, once, and the
// projection is left as it was. A variable so a test can count the reports.
var mutateRefused = func(err error) {
	configLog.Error("config: Mutate refused by the Facade; the change was not applied: %s", err)
}
