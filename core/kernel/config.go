// file: core/kernel/config.go
// version: 1.1.6
// guid: 1f6c9e2a-7b40-4d85-9a3e-5c2b8d7f0e14
// last-edited: 2026-11-19

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/internal/configstore"
	"github.com/falkcorp/mediacore/internal/statefs"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// The kernel's config wiring (05-config §1.4, 06 P0/P3/P4b-d), run by boot.go
// behind Config.CoreConfigBoot (config_boot.go: P0 bootCoreConfig, P3
// loadCoreConfig, P4b bindCoreStores, P4d attachCoreConfig). The gate is off
// for ABO, so its boot reads no DB layer through core and writes no c/config/
// row; slice (e) of F3-07 deletes the gate with R1Values (09 §9.2.8). The
// operator layers that reach it are the file, the environment and the
// explicitly set flags (F3-02). CheckConfig, the engine of the offline config
// check, is the one exported entry: it registers the same core sections and
// reads only the operator layers.

const (
	stateDirKey    config.Path = "core.paths.state_dir"
	storagePathKey config.Path = "core.storage.path"
	pathsSection   config.Path = "core.paths"
	modulesSection config.Path = "core.modules"

	// healthUnknownModule is raised on core.config when the boot record
	// names a module this binary does not compile (06 §6.10, COR-14).
	healthUnknownModule = "config_unknown_module_in_boot_record"

	// labelStateDirMkdir is the crash-point label of P0's creation of a
	// state directory candidate (06 §6.5.1's writer table; name coined by
	// F3-07).
	labelStateDirMkdir = "config.statedir.mkdir"
)

// configOwnerSpec is c/config/'s storage owner (05-config §1.3.3).
func configOwnerSpec() storage.OwnerSpec { return configstore.OwnerSpec() }

// configInput is what the config boot takes from app.Options.
type configInput struct {
	AppID, EnvPrefix string
	Environ          []string
	FilePath         string
	Flags            map[config.Path]string
	// StateDirDefault is app.Options.StateDir, the compiled default of
	// core.paths.state_dir.
	StateDirDefault string
	Modules         []module.Module
	Passthrough     func(config.Path) bool
	EnvBindings     []config.EnvName
	Retired         []config.RetiredName
	// OwnerVerifier is true when the app configured an owner-proof verifier
	// (F4's app.Options.Authenticators; nothing sets it before then). OwnerSet
	// is then required: config checks ProtectOwner with Actor.Owner only once an
	// owner is set, and a nil OwnerSet means none is, so ProtectOwner keys would
	// be guarded as ProtectAdmin while the app believes owner proof protects them
	// (F3-01's seam item 2).
	OwnerVerifier bool
	OwnerSet      func() bool
	Logger        *slog.Logger
	Now           func() time.Time
	// Mkdir creates a state directory candidate; nil = MkdirAll(d, 0o700)
	// through the boot's filesystem (statefs), labelled labelStateDirMkdir.
	Mkdir func(string) error
}

// configBoot is the config store after P0, with what P0 resolved.
type configBoot struct {
	store    *config.Store
	view     config.BootView
	stateDir string
	fellBack bool
	// dbPath is core.storage.path as step 0 read it from the pre-boot
	// layers, the file included ("" = unset).
	dbPath   string
	prefix   string
	handles  []any // the core sections' handles, in coreSections order
	unknown  []module.ID
	log      *slog.Logger
	native   *configstore.Store
	instrSet bool
}

// bootConfig is 06 P0 for config: step 0 (step0) reads core.paths.state_dir
// (env and flags only) and core.storage.path (every operator layer) before
// anything else and resolves the state directory once; the core sections
// register with core.paths defaulting to that directory and core.modules to
// each manifest's DefaultEnabled; Boot reads the boot record from exactly
// that directory. It touches no storage.
func bootConfig(ctx context.Context, in configInput) (cb *configBoot, err error) {
	if in.OwnerVerifier && in.OwnerSet == nil {
		return nil, errors.New("kernel: config: an owner verifier is configured and config.Options.OwnerSet is nil: ProtectOwner keys would be checked as ProtectAdmin; the app must say whether an owner is set")
	}
	cb = &configBoot{prefix: in.prefix(), log: in.logger()}
	st, handles, err := newStep0Store(in, func(dir string) (config.StateFiles, error) { return cb.bootRoot(ctx, dir) })
	if err != nil {
		return nil, err
	}
	cb.store = st
	defer func() {
		if err != nil {
			err = errors.Join(err, st.Close(ctx))
		}
	}()
	s0, err := step0(ctx, st, in, in.mkdir(ctx))
	if err != nil {
		return nil, err
	}
	cb.stateDir, cb.fellBack, cb.dbPath = s0.stateDir, s0.fellBack, s0.dbPath

	ph, mh, known, err := registerPathsAndModules(st, s0.stateDir, in.Modules)
	if err != nil {
		return nil, err
	}
	cb.handles = append(handles, ph, mh)

	if cb.view, err = st.Boot(ctx); err != nil {
		return nil, fmt.Errorf("kernel: config boot: %w", err)
	}
	if err := cb.checkStoragePath(); err != nil {
		return nil, err
	}
	if err := cb.checkModules(known); err != nil {
		return nil, err
	}
	return cb, nil
}

// prefix is the app's environment prefix, strings.ToUpper(AppID) by default.
func (in configInput) prefix() string {
	if in.EnvPrefix != "" {
		return in.EnvPrefix
	}
	return strings.ToUpper(in.AppID)
}

// logger is in.Logger, or a discarding one.
func (in configInput) logger() *slog.Logger {
	if in.Logger != nil {
		return in.Logger
	}
	return slog.New(slog.DiscardHandler)
}

// mkdir is in.Mkdir, or MkdirAll(d, 0o700) through the boot's filesystem
// (statefs) under labelStateDirMkdir.
func (in configInput) mkdir(ctx context.Context) func(string) error {
	if in.Mkdir != nil {
		return in.Mkdir
	}
	return func(d string) error { return statefs.MkdirAll(ctx, labelStateDirMkdir, d, 0o700) }
}

// newStep0Store is the config store step 0 reads: the operator layers, the
// boot record through bootRecord (nil = none: an offline resolve never reads
// it), and every core section but core.paths and core.modules, so
// core.storage.path is read canonically.
func newStep0Store(in configInput, bootRecord func(string) (config.StateFiles, error)) (*config.Store, []any, error) {
	st, err := config.NewStore(config.Options{
		AppID: in.AppID, EnvPrefix: in.EnvPrefix, Environ: in.Environ, FilePath: in.FilePath, Flags: in.Flags,
		Passthrough: in.Passthrough, EnvBindings: in.EnvBindings, Retired: in.Retired,
		Logger: in.logger(), Now: in.Now, OwnerSet: in.OwnerSet,
		BootRecordRoot: bootRecord,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("kernel: config store: %w", err)
	}
	rest, err := coreSectionsButPathsAndModules()
	if err == nil {
		var handles []any
		if handles, err = config.RegisterSections(st.Registry(), rest...); err == nil {
			return st, handles, nil
		}
		err = fmt.Errorf("kernel: config: register the core sections: %w", err)
	}
	return nil, nil, errors.Join(err, st.Close(context.Background()))
}

// step0Result is what step 0 resolved.
type step0Result struct {
	stateDir string // absolute and clean
	fellBack bool   // beside the database, the default being uncreatable
	dbPath   string // core.storage.path from the operator layers, "" = unset
}

// step0 is 05-config §1.7's step 0 (OPS5-13) over st, a store holding the
// operator layers: a retired name that refuses boot is judged first, so a
// refused boot creates nothing (§1.3.6); then core.paths.state_dir (env and
// flags) and core.storage.path (file, env and flags) are read and
// config.ResolveStateDir picks the state directory, creating it with mkdir,
// made absolute. bootConfig runs it before Boot reads the boot record, and
// ResolveStorage runs it alone.
func step0(ctx context.Context, st *config.Store, in configInput, mkdir func(string) error) (step0Result, error) {
	// The judgement runs on a scratch store over the same operator layers
	// (st cannot judge core.paths before step 0 registers it); the real Boot
	// judges again and warns.
	if _, err := CheckConfig(ctx, ConfigCheck{
		Config: config.Options{
			AppID: in.AppID, EnvPrefix: in.EnvPrefix, Environ: in.Environ, FilePath: in.FilePath, Flags: in.Flags,
			Passthrough: in.Passthrough, EnvBindings: in.EnvBindings, Retired: in.Retired,
			Logger: slog.New(slog.DiscardHandler), Now: in.Now, OwnerSet: in.OwnerSet,
		},
		StateDirDefault: in.StateDirDefault, Modules: in.Modules,
	}); err != nil {
		if rerr, refused := errors.AsType[*config.RetiredNameError](err); refused {
			return step0Result{}, fmt.Errorf("kernel: config boot: %w", rerr)
		}
		// Any other operator-layer error is the real store's to report, in
		// its own words.
	}
	explicit, err := operatorString(st, stateDirKey)
	if err != nil {
		return step0Result{}, err
	}
	var r step0Result
	if r.dbPath, err = operatorString(st, storagePathKey); err != nil {
		return step0Result{}, err
	}
	stateVar := in.prefix() + "_CORE_PATHS_STATE_DIR"
	dir, fellBack, err := config.ResolveStateDir(explicit, in.StateDirDefault, r.dbPath, mkdir)
	if err != nil {
		if strings.TrimSpace(explicit) != "" {
			return step0Result{}, fmt.Errorf("kernel: config: the state directory %q (%s) cannot be used: %w", dir, stateVar, err)
		}
		// With no default, config's error already says so; it is not repeated.
		what := fmt.Errorf("the default state directory %q cannot be created (%w)", dir, err)
		if strings.TrimSpace(in.StateDirDefault) == "" {
			what = err
		}
		return step0Result{}, fmt.Errorf("kernel: config: %w, and core.storage.path %q gives no absolute directory that can be created to keep state beside; set %s to a directory that can be created",
			what, r.dbPath, stateVar)
	}
	// Absolute, as app.storagePath makes the state directory with the gate
	// off: a relative compiled default (hello's "hello-state") would
	// otherwise reach P0a, the swap journal and the keyring relative to
	// whatever the working directory is at each use.
	if r.stateDir, err = filepath.Abs(dir); err != nil {
		return step0Result{}, fmt.Errorf("kernel: config: the state directory: %w", err)
	}
	r.fellBack = fellBack
	if fellBack {
		in.logger().Warn("kernel: the default state directory cannot be created; keeping state files beside the database",
			"default", in.StateDirDefault, "using", r.stateDir, "choose_with", stateVar)
	}
	return r, nil
}

// coreSectionsButPathsAndModules is coreSections without core.paths and
// core.modules, which register with defaults the boot computes
// (registerPathsAndModules).
func coreSectionsButPathsAndModules() ([]any, error) {
	var paths, mods any
	rest := make([]any, 0, len(coreSections()))
	for _, sec := range coreSections() {
		switch pathOf(sec) {
		case pathsSection:
			paths = sec
		case modulesSection:
			mods = sec
		default:
			rest = append(rest, sec)
		}
	}
	if paths == nil || mods == nil {
		return nil, errors.New("kernel: config: coreSections lacks core.paths or core.modules")
	}
	if _, ok := mods.(config.Section[map[string]moduleSettings]); !ok {
		return nil, fmt.Errorf("kernel: config: core.modules is %T, want a map of moduleSettings", mods)
	}
	return rest, nil
}

// registerPathsAndModules registers core.paths with stateDir as
// core.paths.state_dir's default and core.modules with each manifest's
// DefaultEnabled, and returns the compiled module IDs.
func registerPathsAndModules(st *config.Store, stateDir string, mods []module.Module) (ph, mh any, known []module.ID, err error) {
	ph, err = config.Register(st.Registry(), config.Define(pathsSection, pathsSettings{StateDir: stateDir}))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("kernel: config: register core.paths: %w", err)
	}
	enabled := map[string]moduleSettings{}
	known = make([]module.ID, 0, len(mods))
	for _, m := range mods {
		man := m.Manifest()
		enabled[string(man.ID)] = moduleSettings{Enabled: man.DefaultEnabled}
		known = append(known, man.ID)
	}
	mh, err = config.Register(st.Registry(), config.Define(modulesSection, enabled))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("kernel: config: register core.modules: %w", err)
	}
	return ph, mh, known, nil
}

// ConfigCheck is what CheckConfig reads (name coined by F3-02): the app's
// config.Options (its AppID, EnvPrefix, Environ, FilePath, Flags,
// Passthrough, EnvBindings, Retired and Logger; BootRecordRoot, Metrics and
// Secrets are ignored), the compiled default of core.paths.state_dir, the
// app's modules, and the config.Define values of the modules' sections.
// Until F3-07 hands module sections to the kernel nothing lists them, so a
// caller passes none and a retired name of a module key is Unchecked (the
// seam F3-07 fills, from app's config check).
type ConfigCheck struct {
	Config          config.Options
	StateDirDefault string
	Modules         []module.Module
	ModuleSections  []any
}

// CheckConfig is the engine of the offline command config check (05-config
// §1.3.6, 06 §6.4.5): it registers the core sections as the boot does and
// runs the store's Boot and Load over the operator layers only. It opens no
// store, reads no boot record and creates no directory. It returns every
// retired name set; err is a *config.RetiredNameError when one would refuse
// boot, or the operator layers' own error (a value that does not decode, an
// unreadable file). A retired name of a key of a module with no section in
// c.ModuleSections is reported Unchecked (its class is unknown); one of a
// module with sections there is judged as a start judges it. bootConfig also
// runs it, before step 0, so a refused boot creates no directory.
func CheckConfig(ctx context.Context, c ConfigCheck) (uses []config.RetiredUse, err error) {
	o := c.Config
	o.BootRecordRoot = nil
	o.Metrics, o.Secrets = nil, nil
	st, err := config.NewStore(o)
	if err != nil {
		return nil, fmt.Errorf("kernel: config check: %w", err)
	}
	defer func() { err = errors.Join(err, st.Close(ctx)) }()
	rest, err := coreSectionsButPathsAndModules()
	if err != nil {
		return nil, err
	}
	if _, err := config.RegisterSections(st.Registry(), rest...); err != nil {
		return nil, fmt.Errorf("kernel: config check: register the core sections: %w", err)
	}
	if _, _, _, err := registerPathsAndModules(st, c.StateDirDefault, c.Modules); err != nil {
		return nil, err
	}
	if _, err := config.RegisterSections(st.Registry(), c.ModuleSections...); err != nil {
		return nil, fmt.Errorf("kernel: config check: register the module sections: %w", err)
	}
	lerr := st.Load(ctx)
	return st.RetiredSet(), lerr
}

// bootRoot is Options.BootRecordRoot. Boot hands back core.paths.state_dir
// as the layers resolve it; it must be the directory step 0 chose, or two
// resolvers disagree about where state lives and the boot record would land
// in a third place. The directory is opened through statefs, so under the
// crash matrix's seam the record is read from, and set aside on, the boot's
// filesystem.
func (cb *configBoot) bootRoot(ctx context.Context, dir string) (config.StateFiles, error) {
	got, err := filepath.Abs(strings.TrimSpace(dir))
	if err != nil || got != cb.stateDir {
		return nil, errors.Join(fmt.Errorf("kernel: config: Boot resolved the state directory %q, the pre-boot resolver %q", got, cb.stateDir), err)
	}
	return statefs.Open(ctx, cb.stateDir)
}

// pathOf is a config.Section's path, whatever its type parameter.
func pathOf(sec any) config.Path {
	if p, ok := sec.(interface{ Path() config.Path }); ok {
		return p.Path()
	}
	return ""
}

// operatorString reads one string key from the pre-boot operator layers.
func operatorString(st *config.Store, p config.Path) (string, error) {
	v, ok, err := st.Operator(p)
	if err != nil {
		return "", fmt.Errorf("kernel: config: %s: %w", p, err)
	}
	if !ok {
		return "", nil
	}
	return operatorValueString(p, v)
}

// operatorValueString decodes one operator value of a string key. A
// core.paths.state_dir from the operator file is refused: the state directory
// is resolved before any config file is read (05-config §1.7).
func operatorValueString(p config.Path, v config.OperatorValue) (string, error) {
	if p == stateDirKey && v.Src == config.SourceFile {
		return "", fmt.Errorf("kernel: config: %s sets %s: it is a pre-boot key, read from the environment and flags only (05-config §1.7)", v.By, p)
	}
	var s string
	if err := json.Unmarshal(v.Value, &s); err != nil {
		return "", fmt.Errorf("kernel: config: %s from %s: %w", p, v.By, err)
	}
	return s, nil
}

// checkStoragePath fails boot when core.storage.path as the store resolves
// it is not what step 0 read: the state directory, the store and the boot
// record would then disagree about where state lives (05-config §1.11).
func (cb *configBoot) checkStoragePath() error {
	ks, ok := cb.store.Lookup(storagePathKey)
	if !ok {
		return nil
	}
	var now string
	if err := json.Unmarshal(ks.Effective, &now); err != nil {
		return fmt.Errorf("kernel: config: %s: %w", storagePathKey, err)
	}
	if strings.TrimSpace(now) == strings.TrimSpace(cb.dbPath) {
		return nil
	}
	msg := fmt.Sprintf("kernel: config: %s is %q, but the state directory was resolved with %q", storagePathKey, now, cb.dbPath)
	if cb.fellBack {
		msg += fmt.Sprintf(": the state directory %s sits beside the old path; set %s_CORE_PATHS_STATE_DIR explicitly, or give %s from the environment or a flag so it is known before boot",
			cb.stateDir, cb.prefix, storagePathKey)
	}
	return errors.New(msg)
}

// checkModules refuses core.modules.<id>.enabled naming a module this
// binary does not compile when an operator layer sets it, and warns (and
// retains the entry) when only the UI-written boot record does (06 §6.10,
// COR-14).
func (cb *configBoot) checkModules(known []module.ID) error {
	var operator []string
	cb.unknown = nil
	for p, ks := range cb.store.Keys() {
		rest, ok := strings.CutPrefix(string(p), string(modulesSection)+".")
		if !ok {
			continue
		}
		id, field, _ := strings.Cut(rest, ".")
		if field != "enabled" || slices.Contains(known, module.ID(id)) {
			continue
		}
		switch {
		case ks.LockedBy != "":
			operator = append(operator, fmt.Sprintf("%s (%s)", id, ks.LockedBy))
		case ks.Source == config.SourceBootRecord:
			cb.unknown = append(cb.unknown, module.ID(id))
		}
	}
	ids := make([]string, len(known))
	for i, id := range known {
		ids[i] = string(id)
	}
	slices.Sort(ids)
	if len(operator) > 0 {
		return &module.ValidationError{Problems: []module.Problem{{
			Kind: module.ProblemUnknownModuleInConfig,
			Detail: fmt.Sprintf("core.modules names %s, which this binary does not compile; known modules: %s; remove the setting",
				strings.Join(operator, ", "), strings.Join(ids, ", ")),
		}}}
	}
	if len(cb.unknown) > 0 {
		cb.log.Warn("kernel: the boot record enables or disables modules this binary does not compile; the entries are kept",
			"modules", cb.unknown, "known", ids)
	}
	return nil
}

// HealthTokens is core.config's health: the store's tokens plus the
// kernel's.
func (cb *configBoot) HealthTokens() []string {
	set := map[string]bool{}
	for _, t := range cb.store.HealthTokens() {
		set[t] = true
	}
	if len(cb.unknown) > 0 {
		set[healthUnknownModule] = true
	}
	return slices.Sorted(maps.Keys(set))
}

// loadConfig is 06 P3 for config: the enabled modules' sections register,
// then Load seals the registry and builds every section. core.storage.path is
// checked again: the store must open where step 0 said.
func (cb *configBoot) loadConfig(ctx context.Context, moduleSections ...any) ([]any, error) {
	hs, err := config.RegisterSections(cb.store.Registry(), moduleSections...)
	if err != nil {
		return nil, fmt.Errorf("kernel: config: register the module sections: %w", err)
	}
	if err := cb.seal(ctx); err != nil {
		return nil, err
	}
	return hs, nil
}

// loadSettings is loadConfig for the sections modules declare in
// Manifest.Settings (module.SettingsSection): each registers with the store,
// and the live handles come back by path.
func (cb *configBoot) loadSettings(ctx context.Context, secs []module.SettingsSection) (map[config.Path]any, error) {
	live := make(map[config.Path]any, len(secs))
	for _, s := range secs {
		h, err := s.Register(cb.store.Registry())
		if err != nil {
			return nil, fmt.Errorf("kernel: config: register the module section %s: %w", s.Path(), err)
		}
		live[s.Path()] = h
	}
	if err := cb.seal(ctx); err != nil {
		return nil, err
	}
	return live, nil
}

// seal loads the registered sections and checks core.storage.path once more.
func (cb *configBoot) seal(ctx context.Context) error {
	if err := cb.store.Load(ctx); err != nil {
		return fmt.Errorf("kernel: config load: %w", err)
	}
	return cb.checkStoragePath()
}

// buildConfigStore is 06 P4b for config: the native store over c/config/,
// handed to the Importer for the cutover step that runs before Attach.
func (cb *configBoot) buildConfigStore(db *storage.DB) (*configstore.Store, error) {
	if db == nil {
		return nil, errors.New("kernel: config: no storage handle (build the config store after P4a)")
	}
	ks, ok := db.Keyspace(configstore.Owner)
	if !ok {
		return nil, fmt.Errorf("kernel: config: owner %s has no keyspace: append configOwnerSpec() to storageOwners (F3-07)", configstore.Owner)
	}
	cb.native = configstore.New(ks)
	cb.store.SetImportTarget(cb.native)
	return cb.native, nil
}

// attachConfig is 06 P4d step 1 for config: the secrets resolver binds (when
// the secrets store is open), then Attach reads the DB layer once, writing
// nothing; the committer stages rows, secret writes and the
// core.settings.update audit record in one batch.
func (cb *configBoot) attachConfig(ctx context.Context, auditStore *audit.Store, resolver secrets.Resolver, stager configstore.SecretStager) (config.AttachReport, error) {
	if cb.native == nil {
		return config.AttachReport{}, errors.New("kernel: config: Attach before buildConfigStore")
	}
	if resolver != nil {
		cb.store.SetSecrets(resolver)
	}
	committer, err := configstore.NewCommitter(configstore.CommitterOptions{
		Store: cb.native, Audit: auditStore, Keys: cb.store, Secrets: stager,
	})
	if err != nil {
		return config.AttachReport{}, fmt.Errorf("kernel: config committer: %w", err)
	}
	rep, err := cb.store.Attach(ctx, cb.native, committer)
	if err != nil {
		return config.AttachReport{}, fmt.Errorf("kernel: config attach: %w", err)
	}
	if len(rep.Unknown) > 0 || len(rep.Quarantined) > 0 {
		cb.log.Warn("kernel: stored settings were kept and ignored",
			"unknown", rep.Unknown, "quarantined", slices.Sorted(maps.Keys(rep.Quarantined)))
	}
	return rep, nil
}

// configMetrics registers the store's instruments on core's "config" scope
// (05-config §1.8).
func (cb *configBoot) configMetrics(p *metrics.Provider) (*metrics.Scope, error) {
	if cb.instrSet {
		return nil, errors.New("kernel: config metrics registered twice")
	}
	sc, err := p.Core("config", "outcome", "section")
	if err != nil {
		return nil, fmt.Errorf("kernel: config metrics scope: %w", err)
	}
	if err := cb.store.Instrument(sc); err != nil {
		return nil, fmt.Errorf("kernel: config metrics: %w", err)
	}
	cb.instrSet = true
	return sc, nil
}
