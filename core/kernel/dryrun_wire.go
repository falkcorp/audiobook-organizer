// file: core/kernel/dryrun_wire.go
// version: 1.0.0
// guid: 4c1e7a52-93d8-4f06-b2a1-6d5e0f8c3b97
// last-edited: 2026-11-19

package kernel

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// WireDryRun fills the seams DryRun leaves to the app (F3-07 e1): Migrations
// and Rewrite from the app's manifests, the throw-away keyring's Sealer, the
// scratch store's Bind and the Release that closes what Bind opened. The
// command calls it on the Config it hands DryRun, so the dry run runs the
// steps a live boot's P4b would run, over the scratch copy, through importers
// bound to the scratch copy's stores:
//
//   - Migrations are every manifest's DataMigrations and the steps its Cutover
//     func returns, enabled or not (dataSteps' rule), built over one
//     late-bound importers value; Rewrite is the first manifest's M8
//     (rewriteStep). They replace opt's own.
//   - The dry run mirrors bindCoreStores: Bind
//     builds a layered config store over the operator layers only (no boot
//     record is read, so none can be set aside, and no directory is created),
//     opens the secret store over the scratch copy with its keyring in the
//     scratch directory, builds the native config store over the copy's
//     c/config/, and binds the config and secret importers. Sealer hands the
//     quarantine the late-bound sealer over that secret store, which fails
//     closed until Bind opened it. The state directory's keyring, its install
//     ID and its export record are neither read nor written (05-config §2.4).
//   - With the gate off the live boot has no config or secret store, so none
//     is built and the importers stay unbound; Sealer stays nil, as the live
//     boot's quarantine writer is.
//   - Either way Bind binds the importers' Legacy ranges and AllowKeyRegen
//     (migrateStorage's two bind calls). AllowKeyRegen is
//     core.secrets.allow_key_regen as the operator layers resolve it, false
//     with the gate off, which is what the live boot hands the steps.
//
// The scratch keyring itself is opened with AllowKeyRegen set whatever the
// configuration says: it is new and throw-away, so a copy that already holds
// sealed secrets (a store past the cutover) must not refuse the dry run with
// ErrKeyMissing for want of a keyring that only the live state directory has.
// That switch is the keyring's; Importers.AllowKeyRegen is the step's, and the
// two are never conflated.
//
// Nothing is opened until DryRun calls Bind, so an Options that never reaches
// DryRun leaks nothing. Name coined by F3-07 e1.
func WireDryRun(ctx context.Context, c Config, opt DryRunOptions) (DryRunOptions, error) {
	k := &Kernel{cfg: c, lifecycle: newLifecycle()}
	if err := k.catalog(false); err != nil {
		return opt, err
	}
	w := &dryRunWiring{k: k, imp: k.importers()}
	var migs []datamigrate.DataMigration
	for _, e := range k.entries {
		migs = append(migs, manifestMigrations(e.man, w.imp.values())...)
	}
	opt.Migrations, opt.Rewrite = migs, k.rewriteStep()
	// The live boot seals into the secret store (storageOptions).
	opt.Sealer, opt.Bind, opt.Release = w.sealer, w.bind(ctx), w.release
	return opt, nil
}

// dryRunWiring is one dry run's late-bound state: the importers its
// migration list closes over, and the stores Bind opened over the scratch
// copy. k is only the holder bindCoreStores' helpers and quarantineSealer
// read (cfg, catalog, sec); it opens no store and no log of its own.
type dryRunWiring struct {
	k   *Kernel
	imp *lateImporters

	mu      sync.Mutex
	scratch string         // the dry run's directory, learned from Sealer
	cb      *configBoot    // the scratch config store's layered half
	sec     *kernelSecrets // the throw-away keyring's secret store
	closed  bool
}

// sealer is DryRun's Options.Sealer: it learns the scratch directory, where
// the throw-away keyring will be created, and returns the quarantine's
// sealer. That sealer reads the kernel's secret store when a chunk
// quarantines, so it needs no store now.
func (w *dryRunWiring) sealer(scratch string) (storage.QuarantineSealer, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.scratch = scratch
	return quarantineSealer{w.k}, nil
}

// bind is DryRun's Options.Bind: bindCoreStores over the scratch store, then
// migrateStorage's bind of the importers.
func (w *dryRunWiring) bind(ctx context.Context) func(*storage.DB) error {
	return func(db *storage.DB) error {
		allowRegen, err := w.bindCoreStores(ctx, db)
		if err != nil {
			return err
		}
		w.imp.bindLegacy(db, allowRegen)
		return nil
	}
}

// bindCoreStores is the dry run's bindCoreStores (config_boot.go) over the
// scratch store db: the layered store the live boot would have loaded at P3,
// the secret store with its keyring in the scratch directory, the native
// config store over db's c/config/ as the import target and the secret store
// as the secret stager, and the two importers bound. It returns the
// resolved core.secrets.allow_key_regen.
func (w *dryRunWiring) bindCoreStores(ctx context.Context, db *storage.DB) (allowRegen bool, err error) {
	w.mu.Lock()
	scratch := w.scratch
	w.mu.Unlock()
	if scratch == "" {
		return false, errors.New("kernel: dry run: the scratch directory is unknown (DryRunOptions.Sealer runs before Bind and names it)")
	}
	k := w.k
	cb, err := k.scratchConfig(ctx)
	if err != nil {
		return false, err
	}
	w.mu.Lock()
	w.cb = cb
	w.mu.Unlock()
	if _, allowRegen, err = cb.secretsSettings(); err != nil {
		return false, err
	}
	ks, err := openSecrets(ctx, secretsInput{
		DB: db, StateDir: scratch, AppID: k.cfg.AppID,
		// KeyFile "": the default name, inside the scratch directory the
		// secret store is rooted at; AllowKeyRegen: see WireDryRun.
		KeyFile: "", AllowKeyRegen: true,
		Logger: k.coreLog("secrets"), Now: bootClock(ctx),
	})
	if err != nil {
		return false, err
	}
	w.mu.Lock()
	w.sec = ks
	w.mu.Unlock()
	k.sec = ks
	if _, err := cb.buildConfigStore(db); err != nil {
		return false, err
	}
	cb.store.SetImportSecrets(ks.store)
	w.imp.bind(cb.store.Importer(), ks.store.Importer())
	w.imp.bindLookup(cb.store.Lookup)
	return allowRegen, nil
}

// release is DryRun's Options.Release: the secret store (wiping its key
// material, closing its root in the scratch directory), then the layered
// config store, once; a Bind that never ran left nothing to close.
func (w *dryRunWiring) release() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	var errs []error
	if w.sec != nil {
		if err := w.sec.close(); err != nil {
			errs = append(errs, fmt.Errorf("kernel: dry run: close the throw-away keyring: %w", err))
		}
	}
	if w.cb != nil {
		if err := w.cb.close(context.Background()); err != nil {
			errs = append(errs, fmt.Errorf("kernel: dry run: close the scratch config store: %w", err))
		}
	}
	return errors.Join(errs...)
}

// scratchConfig is the layered config store a live boot has loaded by P4b,
// built for a dry run: the operator layers (environment, flags, file) and the
// core and enabled modules' sections, Loaded, with no boot record. It is
// bootConfig and loadCoreConfig without what they write: bootConfig reads the
// boot record and may rename an unparsable one to .bad, and step 0 creates
// the state directory, neither of which a dry run may do to the live state
// directory (the same reason ResolveStorage runs step 0 alone with no record).
func (k *Kernel) scratchConfig(ctx context.Context) (*configBoot, error) {
	in := k.configInput(ctx)
	cb := &configBoot{prefix: in.prefix(), log: in.logger(), stateDir: k.cfg.StateDir}
	st, _, err := newStep0Store(in, nil)
	if err != nil {
		return nil, err
	}
	cb.store = st
	fail := func(err error) (*configBoot, error) {
		return nil, errors.Join(err, st.Close(context.WithoutCancel(ctx)))
	}
	if _, _, _, err := registerPathsAndModules(st, k.cfg.StateDir, in.Modules); err != nil {
		return fail(err)
	}
	var secs []module.SettingsSection
	for _, e := range k.entries {
		if e.enabled {
			secs = append(secs, e.man.Settings...)
		}
	}
	reg := st.Registry()
	if err := config.Batch(reg, func() error {
		for _, s := range secs {
			if _, err := s.Register(reg); err != nil {
				return fmt.Errorf("kernel: config: register the module section %s: %w", s.Path(), err)
			}
		}
		return nil
	}); err != nil {
		return fail(err)
	}
	if err := st.Load(ctx); err != nil {
		return fail(fmt.Errorf("kernel: config load: %w", err))
	}
	return cb, nil
}
