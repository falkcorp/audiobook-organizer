// file: core/kernel/config_boot.go
// version: 1.0.0
// guid: 0e3db6c6-c5ae-4f54-adaf-6fd7b728c89e
// last-edited: 2026-11-18

package kernel

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/module"
)

// The config phases of 06 section 6.5.1 as labels on
// <namespace>_kernel_boot_phase_seconds. They exist only when
// Config.CoreConfigBoot is set (06 section 6.9's one label set lists them).
const (
	phaseP0 = "P0"
	phaseP3 = "P3"
)

// disabledByConfig is ModuleState.Disabled for a module that a layer other
// than the manifest default turned off: core.modules.<id>.enabled from the
// environment, a flag, the file or the boot record.
const disabledByConfig = "config"

// bootCoreConfig is P0 (06 section 6.5.1), run when Config.CoreConfigBoot is
// set and not for Describe, which performs no I/O: bootConfig's step 0 resolves
// the state directory and core.storage.path from the pre-boot layers, the
// store reads the boot record from that directory, and what it found replaces
// the app's own resolution (app.storagePath reads the environment only):
//
//   - Config.StateDir is the directory step 0 chose; an app that resolved a
//     different one fails the boot, because two resolvers disagreeing about
//     where state lives is the one thing step 0 exists to prevent;
//   - Config.StoragePath is core.storage.path as the layers give it. It is
//     the compiled default only when no layer names one, and an app whose
//     compiled path and a layer's path name two stores fails the boot (the
//     rule app.storagePath applies to the environment and R1StoragePath);
//   - core.modules.<id>.enabled from the boot view decides which modules are
//     enabled, so P3 registers the sections of the modules that run.
//
// An app that declares no storage keeps Config.StoragePath and StateDir as
// they are: with no store there is nothing to swap and no keyring to keep, and
// the config store is the operator layers and the boot record.
func (k *Kernel) bootCoreConfig(ctx context.Context) error {
	o := k.cfg.ConfigOptions
	cb, err := bootConfig(ctx, configInput{
		AppID: k.cfg.AppID, EnvPrefix: o.EnvPrefix, Environ: o.Environ, FilePath: o.FilePath, Flags: o.Flags,
		StateDirDefault: k.cfg.StateDirDefault, Modules: k.cfg.Modules, Passthrough: o.Passthrough,
		EnvBindings: o.EnvBindings, Retired: o.Retired, OwnerSet: o.OwnerSet,
		Logger: k.coreLog("config"), Now: bootClock(ctx),
	})
	if err != nil {
		return err
	}
	k.cfgBoot = cb
	if k.declaresStorage() {
		path, err := k.gatedStoragePath(cb)
		if err != nil {
			return err
		}
		if sd := k.cfg.StateDir; sd != "" && filepath.Clean(sd) != cb.stateDir {
			return fmt.Errorf("kernel: config: the app resolved the state directory %q, step 0 of the config boot %q: one resolver must own it", sd, cb.stateDir)
		}
		k.cfg.StoragePath, k.cfg.StateDir = path, cb.stateDir
	}
	k.applyBootEnabled(cb)
	return nil
}

// declaresStorage reports whether any module of the app declares storage,
// enabled or not (the rule app.storagePath and storageWanted apply).
func (k *Kernel) declaresStorage() bool {
	for _, e := range k.entries {
		if DeclaresStorage(e.man) {
			return true
		}
	}
	return false
}

// gatedStoragePath is the store's path under the config boot: core.storage.path
// from the pre-boot layers, else the compiled default (Config.StoragePath),
// else <state dir>/storage/main.pebble.
func (k *Kernel) gatedStoragePath(cb *configBoot) (string, error) {
	layer, compiled := cb.dbPath, k.cfg.StoragePath
	for what, p := range map[string]string{"core.storage.path": layer, "the app's compiled database path": compiled} {
		if p != "" && !filepath.IsAbs(p) {
			return "", fmt.Errorf("kernel: config: %s %q is not an absolute path", what, p)
		}
	}
	switch {
	case layer != "" && compiled != "" && filepath.Clean(layer) != filepath.Clean(compiled):
		return "", fmt.Errorf("kernel: config: core.storage.path %q and the app's database path %q name different stores; set one, or set both to the same path", layer, compiled)
	case layer != "":
		return filepath.Clean(layer), nil
	case compiled != "":
		return filepath.Clean(compiled), nil
	}
	return filepath.Join(cb.stateDir, "storage", "main.pebble"), nil
}

// applyBootEnabled sets each module's enablement from core.modules.<id>.enabled
// as the boot view resolves it (operator layers, then the boot record, then
// the manifest's DefaultEnabled).
func (k *Kernel) applyBootEnabled(cb *configBoot) {
	for _, e := range k.entries {
		v, src, ok := cb.view.Bool(config.Path(string(modulesSection) + "." + string(e.man.ID) + ".enabled"))
		if !ok {
			continue
		}
		e.enabled = v
		if !v && src != config.SourceDefault {
			e.disabledBy = disabledByConfig
		}
	}
}

// loadCoreConfig is P3 (06 section 6.5.1): every enabled module's declared
// sections register with the store, in ID order, then Load seals the registry
// and builds every section. The live handles are what a module's
// module.Setting reads in Register (P6).
func (k *Kernel) loadCoreConfig(ctx context.Context) error {
	cb := k.cfgBoot
	var secs []module.SettingsSection
	for _, e := range k.entries {
		if e.enabled {
			secs = append(secs, e.man.Settings...)
		}
	}
	live, err := cb.loadSettings(ctx, secs)
	if err != nil {
		return err
	}
	k.settingHandles = live
	return nil
}

// closeConfig closes the layered store, once the core config that shares it
// has been released (closeCore).
func (cb *configBoot) close(ctx context.Context) error {
	if cb == nil {
		return nil
	}
	return cb.store.Close(ctx)
}
