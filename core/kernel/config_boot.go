// file: core/kernel/config_boot.go
// version: 1.0.1
// guid: 0e3db6c6-c5ae-4f54-adaf-6fd7b728c89e
// last-edited: 2026-11-18

package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/secrets"
)

// The config phases of 06 section 6.5.1 as labels on
// <namespace>_kernel_boot_phase_seconds. They exist only when
// Config.CoreConfigBoot is set (06 section 6.9's one label set lists them).
const (
	phaseP0  = "P0"
	phaseP3  = "P3"
	phaseP4d = "P4d"
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

// bindCoreStores is the start of P4b under Config.CoreConfigBoot (06 section
// 6.5.1), before the migration list can run a Cutover step:
//
//  1. secrets.Open (05-config 2.4 step 1): the keyring is read or created beside
//     the state directory and its header carries the store's install marker;
//     ResetBoot is set on the reset boot of a factory reset only (the journal
//     reads set_aside, not applied), so the header is rewritten exactly once;
//  2. that reset boot's journal is marked applied right after, not after P4a:
//     a crash between the fresh install and the keyring leaves set_aside, and
//     the next boot repeats both (DATA7-17; swapAfter skips P4a under the gate);
//  3. the native config store over c/config/ becomes the config importer's
//     target, and the secret store its secret stager, so M2's writes reach both;
//  4. the two Importers P4b hands every Cutover func are bound.
func (k *Kernel) bindCoreStores(ctx context.Context) error {
	cb, db := k.cfgBoot, k.store.db
	keyFile, allowRegen, err := cb.secretsSettings()
	if err != nil {
		return err
	}
	ks, err := openSecrets(ctx, secretsInput{
		DB: db, StateDir: k.cfg.StateDir, AppID: k.cfg.AppID,
		ResetBoot: k.swap != nil && k.swap.out.Boot == restoreswap.BootReset,
		KeyFile:   keyFile, AllowKeyRegen: allowRegen,
		Logger: k.coreLog("secrets"), Now: bootClock(ctx),
	})
	if err != nil {
		return err
	}
	k.sec = ks
	if k.swap != nil && k.swap.out.Boot == restoreswap.BootReset {
		if err := k.swap.s.ResetApplied(ctx, k.swap.out.ID); err != nil {
			return fmt.Errorf("kernel: swap journal %s after secrets.Open: %w", k.swap.out.ID, err)
		}
	}
	if _, err := cb.buildConfigStore(db); err != nil {
		return err
	}
	cb.store.SetImportSecrets(ks.store)
	k.importerCfg, k.importerSec = cb.store.Importer(), ks.store.Importer()
	return nil
}

// secretsSettings is core.secrets.key_file and core.secrets.allow_key_regen as
// the layers resolve them (both reload "restart": read once, here).
func (cb *configBoot) secretsSettings() (keyFile string, allowRegen bool, err error) {
	for p, dst := range map[config.Path]any{"core.secrets.key_file": &keyFile, "core.secrets.allow_key_regen": &allowRegen} {
		ks, ok := cb.store.Lookup(p)
		if !ok {
			return "", false, fmt.Errorf("kernel: config: %s is not declared", p)
		}
		if err := json.Unmarshal(ks.Effective, dst); err != nil {
			return "", false, fmt.Errorf("kernel: config: %s: %w", p, err)
		}
	}
	return keyFile, allowRegen, nil
}

// attachCoreConfig is P4d (06 section 6.5.1) for config: the secret resolver
// binds, then Attach reads the DB layer once. It runs only for a boot that
// opened a store; the audit store the committer records to was built at P4a.
func (k *Kernel) attachCoreConfig(ctx context.Context) error {
	if k.sec == nil {
		return nil
	}
	if _, err := k.cfgBoot.attachConfig(ctx, k.audit, k.sec.store, k.sec.store); err != nil {
		return err
	}
	k.reportConfigHealth()
	return nil
}

// registerCoreStoreHealth is P4c's part of the gate: core.secrets reports the
// store's real state (it starts Up in buildHealth), and core.config registers
// push-only; its tokens are pushed once P4d attached the DB layer.
func (k *Kernel) registerCoreStoreHealth(ctx context.Context) error {
	if k.sec != nil {
		reportSecretsHealth(ctx, k.reporters.Secrets, k.sec.store)
	}
	if k.cfgBoot == nil {
		return nil
	}
	rep, err := k.healthReg.Register(health.Check{ID: componentConfig, Title: "Configuration"})
	if err != nil {
		return fmt.Errorf("kernel: health: %w", err)
	}
	k.reporters.Config = rep
	rep.Report(health.Result{Status: health.StatusUp})
	return nil
}

// reportConfigHealth pushes core.config's tokens (configBoot.HealthTokens: the
// store's and the kernel's): Up with none, else Degraded with the first as the
// Reason and all of them in the Detail (a Result carries one Reason, as
// core.secrets' does).
func (k *Kernel) reportConfigHealth() {
	if k.reporters.Config == nil {
		return
	}
	tokens := k.cfgBoot.HealthTokens()
	if len(tokens) == 0 {
		k.reporters.Config.Report(health.Result{Status: health.StatusUp})
		return
	}
	k.reporters.Config.Report(health.Result{Status: health.StatusDegraded, Reason: tokens[0], Detail: strings.Join(tokens, ", ")})
}

// coreStoreMetrics registers the config and secrets instruments on core's
// scopes once P5 built the Provider (05-config 1.8, 2.8).
func (k *Kernel) coreStoreMetrics() error {
	if k.cfgBoot != nil {
		if _, err := k.cfgBoot.configMetrics(k.provider); err != nil {
			return err
		}
	}
	if k.sec != nil {
		if _, err := secretsMetrics(k.provider, k.sec.store); err != nil {
			return err
		}
	}
	return nil
}

// secretsScope is a module's Deps.Secrets: names under modules.<id>.; the zero
// scope, which refuses every name, where no secret store is open.
func (k *Kernel) secretsScope(id module.ID) secrets.Scope {
	if k.sec == nil {
		return secrets.Scope{}
	}
	return k.sec.store.Scope("modules." + string(id) + ".")
}

// closeSecrets closes the secret store before the store it writes through.
func (k *Kernel) closeSecrets() error {
	if k.sec == nil {
		return nil
	}
	s := k.sec
	k.sec = nil
	if err := s.close(); err != nil {
		return fmt.Errorf("kernel: secrets close: %w", err)
	}
	return nil
}
