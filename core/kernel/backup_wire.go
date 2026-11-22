// file: core/kernel/backup_wire.go
// version: 0.1.0
// guid: b2e47a91-6d03-4f8c-a5e9-3c70d1f82b64
// last-edited: 2026-11-22

package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/backuphttp"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// The kernel's core.backup service (seams.d/F8-01.md, F8-04a slice G2): the
// backup.Options the kernel binds from handles it already holds, the service
// built over them, and the one function that mounts its routes and returns its
// four task definitions. It is dark: nothing in boot calls newBackup or
// mountBackup, so an app that has not flipped (ABO, today) runs its own backup
// task and routes unchanged. F5-18 and the F8-04 fold register the tasks and
// mount the routes (G3); this file only makes them buildable, and tested.
//
// It opens nothing. In particular it builds no journal component: the one the
// identity boot's P4d made is k.ident.journal, and a second component over the
// same journal file would take its lock against the first.

// Each seam is asserted against the real service the kernel passes, so a
// change to a method fails the build here and not at the fold.
var (
	_ backup.Keyring       = (*secrets.Store)(nil)
	_ backup.Store         = (*storage.Admin)(nil)
	_ backup.CheckedStager = (*restoreswap.Swapper)(nil)
	_ backupJournal        = (*journal.Component)(nil)
	_ backupAuditCut       = (*audit.Store)(nil)
	_ backup.Markers       = (*backupMarkers)(nil)
	_ backuphttp.Rewrapper = (*backup.Svc)(nil)
	_ backuphttp.Catalogue = (*backup.Svc)(nil)
	_ backuphttp.Restorer  = (*backup.Svc)(nil)
	_ backup.Ensurer       = (*backup.Svc)(nil)
	_ audit.Logger         = (*audit.Store)(nil)
	_ backupRecorder       = (*audit.Store)(nil)
	_ func(string)         = (*Lifecycle)(nil).RequestRestart
)

// backupJournal is the part of the identity boot's journal component that
// backup uses: the content marker's writer (the one writer of
// backup.location.json, DATA7-14) and retention's trim.
type backupJournal interface {
	AdvanceServedBoot(ctx context.Context) (journal.ServedBoot, error)
	Trim(ctx context.Context, seq uint64) (journal.TrimResult, error)
}

// backupAuditCut is audit.Store.HighWater: it ends the audit sequence's live
// lease and returns the boundary, so a record written after it is above it.
type backupAuditCut interface {
	HighWater(ctx context.Context) (uint64, error)
}

// backupRecorder is the audit log's synchronous write.
type backupRecorder interface {
	Record(ctx context.Context, e audit.Event) error
}

// backupMarkers is backup.Markers over F4-08's journal handle: Create reads
// the three boundaries before its checkpoint and advances the content marker
// right after it (05-backup §5B.3.5 step 4). head and served read c/backup/
// through the keyspace the journal component was built over.
type backupMarkers struct {
	journal backupJournal
	ks      storage.Keyspace
	audit   backupAuditCut
}

// Read is the three boundaries. The journal head is the committed head seq
// (zero for a store with none); the audit boundary is HighWater, which cuts
// the audit store's own sequence (a stored mark read beside it would let the
// live lease issue records below the boundary); the served-boot pair is nil
// when the store has none.
func (m *backupMarkers) Read(ctx context.Context) (backup.Boundaries, error) {
	head, err := journal.ReadHead(ctx, m.ks)
	if err != nil {
		return backup.Boundaries{}, fmt.Errorf("kernel: backup: read the journal head: %w", err)
	}
	hw, err := m.audit.HighWater(ctx)
	if err != nil {
		return backup.Boundaries{}, fmt.Errorf("kernel: backup: cut the audit sequence: %w", err)
	}
	b := backup.Boundaries{JournalSeq: head.Seq, AuditHighWater: hw}
	pair, err := journal.ReadServedBoot(ctx, m.ks)
	if err != nil {
		return backup.Boundaries{}, fmt.Errorf("kernel: backup: read the content marker: %w", err)
	}
	if pair.N > 0 {
		b.ServedBoot = &pair
	}
	return b, nil
}

// Advance is the component's AdvanceServedBoot.
func (m *backupMarkers) Advance(ctx context.Context) (journal.ServedBoot, error) {
	return m.journal.AdvanceServedBoot(ctx)
}

// errBackupNotReady is Options.Restart's refusal while the lifecycle is not
// ready: Lifecycle.RequestRestart ignores such a request (logging it), and a
// restore that asked to restart must say it did not, not report success.
var errBackupNotReady = errors.New("kernel: the app is not ready to restart; restart it to apply the staged restore")

// backupRestartReason is the reason RequestRestart logs for a staged restore.
const backupRestartReason = "a staged restore (core.backup.stage-restore)"

// backupRestart is backup.Options.Restart over Lifecycle.RequestRestart
// (blocker 16): the one path that stops the app through the supervisor, so the
// listener's beforeDrain still sends hub A's system.shutdown frame to the open
// SPA before the process exits 75. It reports an app that is not ready.
func backupRestart(l *Lifecycle) func(context.Context) error {
	if l == nil {
		return nil
	}
	return func(context.Context) error {
		if !l.ready.Load() {
			return errBackupNotReady
		}
		l.RequestRestart(backupRestartReason)
		return nil
	}
}

// backupRecheck is restoreswap.Options.Recheck for a backup-aware swapper
// (05-backup §5B.3.4 step b): the swapper's default (every placement's source
// exists and the rename pre-check passes) and then the archive's manifest
// check (backup.Recheck, which re-hashes when rehash reports true). Setting
// Options.Recheck replaces the default, so both run here, in that order.
func backupRecheck(base func(context.Context, restorejournal.Intent) error, rehash func() bool) func(context.Context, restorejournal.Intent) error {
	archive := backup.Recheck(rehash)
	return func(ctx context.Context, in restorejournal.Intent) error {
		if err := base(ctx, in); err != nil {
			return err
		}
		return archive(ctx, in)
	}
}

// newBackupSwapper is restoreswap.New with Recheck composed by backupRecheck.
// P0a still builds its swapper without it (runP0a, swap.go): the manifest
// check reads a staged manifest that only a core backup stages, so installing
// it before the flip would reject ABO's live restores.
func newBackupSwapper(o restoreswap.Options, rehash func() bool) (*restoreswap.Swapper, error) {
	var sw *restoreswap.Swapper
	o.Recheck = backupRecheck(func(ctx context.Context, in restorejournal.Intent) error {
		return sw.DefaultRecheck(ctx, in)
	}, rehash)
	sw, err := restoreswap.New(o)
	return sw, err
}

// backupAudit is backup.Options.Audit over the audit log: core.restore.stage
// before the intent's create and, when the create refuses, again with outcome
// "rejected". The fields carry no credential (an archive ID and flags).
func backupAudit(rec backupRecorder) func(context.Context, string, map[string]string) error {
	if rec == nil {
		return nil
	}
	return func(ctx context.Context, action string, fields map[string]string) error {
		e := audit.Event{Action: action, Outcome: audit.Success, Target: backup.Resource()}
		details := make(map[string]any, len(fields))
		for k, v := range fields {
			details[k] = v
		}
		if fields["outcome"] == "rejected" {
			e.Outcome, e.Reason = audit.Failure, fields["reason"]
		}
		e.Details = details
		return rec.Record(ctx, e)
	}
}

// backupActor is backup.Options.Actor: the request principal's user ID, "" for
// a context that carries none (the service then names the actor "unknown").
func backupActor(ctx context.Context) string {
	if p, ok := auth.PrincipalFrom(ctx); ok && p != nil && p.User != nil {
		return string(p.User.ID)
	}
	return ""
}

// backupWireIn is everything newBackupSvc binds, each as the narrow seam the
// service takes, so a test can hold any of them and the kernel passes the real
// handle (backupWireInput).
type backupWireIn struct {
	AppID, AppVersion, BuildID, Prefix string
	Dir, StateDir, DBPath              string

	Settings func() backup.Settings
	Owners   []storage.OwnerSpec

	Store     backup.Store
	Keyring   backup.Keyring
	Markers   backup.Markers
	Stager    backup.CheckedStager
	Inspector backup.Inspector

	Probe    restorejournal.Probe
	Audit    func(context.Context, string, map[string]string) error
	Actor    func(context.Context) string
	Restart  func(context.Context) error
	Prune    func(context.Context, uint64) error
	Clock    func() time.Time
	Logger   *slog.Logger
	Modules  map[string]string
	CoreVers string
}

// backupOptions is the backup.Options over in. The Stager is the swapper
// itself: backup.New wraps a CheckedStager with BeforeStage, so an app's
// per-request BeforeStageFunc (books' vector-index clean-stop discard) runs
// after the swapper's own refusals and before its create.
func backupOptions(in backupWireIn) backup.Options {
	var stager backup.Stager
	if in.Stager != nil {
		stager = in.Stager
	}
	return backup.Options{
		AppID: in.AppID, AppVersion: in.AppVersion, CoreVersion: in.CoreVers, BuildID: in.BuildID,
		Prefix: in.Prefix, Dir: in.Dir, StateDir: in.StateDir, DBPath: in.DBPath,
		Settings: in.Settings, Owners: in.Owners, Modules: in.Modules,
		Store: in.Store, Keyring: in.Keyring, Markers: in.Markers,
		Stager: stager, Inspector: in.Inspector,
		Actor: in.Actor, Probe: in.Probe, Restart: in.Restart, PruneJournal: in.Prune,
		Audit: in.Audit, Clock: in.Clock, Logger: in.Logger,
	}
}

// newBackupSvc builds the core backup service over in. A nil Stager is an
// error: a service with none builds, and refuses every restore at the last
// step ("restore needs a Stager"), after the archive was extracted.
func newBackupSvc(in backupWireIn) (*backup.Svc, error) {
	if in.Stager == nil {
		return nil, errors.New("kernel: backup: no swapper to stage restores through")
	}
	return backup.New(backupOptions(in))
}

// backupSettingsOf maps the live core.backup section to the service's
// Settings. RehashOnBoot and IntentTTL are read here only when the section
// carries them; the swap's intent TTL (core.storage.swap.intent_ttl) is not in
// this section, so the service's 24 h default applies until the fold binds it.
func backupSettingsOf(get func() *backupSettings) func() backup.Settings {
	return func() backup.Settings {
		s := *get()
		return backup.Settings{
			Enabled: s.Enabled, Compression: s.Compression, CompressionLevel: s.CompressionLevel,
			IncludeDerived: s.IncludeDerived, IncludeRecreatable: s.IncludeRecreatable,
			VerifyAfterCreate: s.VerifyAfterCreate, VerifyKeep: s.VerifyKeep,
			MaxCount: s.Retention.MaxCount, MaxTotalBytes: s.Retention.MaxTotalBytes, MaxAge: s.Retention.MaxAge.Std(),
			HashWorkers: s.HashWorkers, RehashOnBoot: s.Restore.RehashOnBoot,
		}
	}
}

// backupMarkers is Markers over F4-08's one journal handle (k.ident.journal
// and the c/backup/ keyspace it was built over) and the audit store. It builds
// no component: a second one over the journal file would take the journal's
// lock against the first.
func (k *Kernel) backupMarkers() *backupMarkers {
	return &backupMarkers{journal: k.ident.journal, ks: k.ident.backupKS, audit: k.audit}
}

// backupPrune is backup.Options.PruneJournal over the journal component's
// Trim, which refuses (journal.ErrTrimRefused) while a restore is held or
// frozen. The trim's report is the component's own to log.
func backupPrune(j backupJournal) func(context.Context, uint64) error {
	return func(ctx context.Context, seq uint64) error {
		_, err := j.Trim(ctx, seq)
		return err
	}
}

// backupWireInput gathers newBackupSvc's inputs from the kernel's handles. It
// reads k.ident.journal and k.ident.backupKS, the journal F4-08's P4d built,
// and never constructs another; every handle it needs is a P4d-or-later one,
// so it errors, rather than building one, before they exist.
func (k *Kernel) backupWireInput(ctx context.Context) (backupWireIn, error) {
	id := k.ident
	switch {
	case id == nil || id.journal == nil:
		return backupWireIn{}, errors.New("kernel: backup: the identity boot's journal component is not built (P4d has not run)")
	case k.swap == nil || k.swap.s == nil:
		return backupWireIn{}, errors.New("kernel: backup: no swapper (P0a has not run)")
	case k.sec == nil || k.sec.store == nil:
		return backupWireIn{}, errors.New("kernel: backup: the secret store is not open")
	case k.store == nil || k.store.db == nil:
		return backupWireIn{}, errors.New("kernel: backup: the store is not open")
	case k.audit == nil:
		return backupWireIn{}, errors.New("kernel: backup: the audit store is not built")
	case k.cfgBoot == nil:
		return backupWireIn{}, errors.New("kernel: backup: the config store is not built")
	}
	bh, ok := handleOf[backupSettings](k.cfgBoot.handles)
	if !ok {
		return backupWireIn{}, errors.New("kernel: backup: the core.backup section is not registered")
	}
	build, err := k.binaryID(ctx)
	if err != nil {
		return backupWireIn{}, err
	}
	// The Inspector's base is rebuilt from the owners and the binary ID, not
	// from storageOptions: that one calls dataSteps again, which reassigns the
	// data-migration runner P4a built.
	base := storage.OpenOptions{
		FS: k.cfg.StorageFS, Owners: k.storageOwners(), Build: build, Logger: k.coreLog("storage"),
		Clock: bootClock(ctx), FromRelease: k.cfg.FromRelease, ClaimsShadow: k.cfg.StorageClaimsShadow,
	}
	admin := k.store.db.Admin()
	jh := id.journal
	return backupWireIn{
		AppID: k.cfg.AppID, AppVersion: k.cfg.Version, BuildID: build, Prefix: k.archivePrefix(),
		Dir: resolveBackupDir(bh.Get().Dir, k.cfg.StoragePath), StateDir: k.cfg.StateDir, DBPath: k.cfg.StoragePath,
		Settings: backupSettingsOf(bh.Get), Owners: base.Owners,
		Store: admin, Keyring: k.sec.store,
		Markers:   k.backupMarkers(),
		Stager:    k.swap.s,
		Inspector: &backup.StorageInspector{Base: base, Admin: admin, Secrets: k.sec.store},
		Probe:     bootProbe(ctx, bootseam.FS(ctx)),
		Audit:     backupAudit(k.audit), Actor: backupActor, Restart: backupRestart(k.lifecycle),
		Prune: backupPrune(jh),
		Clock: bootClock(ctx), Logger: k.coreLog("backup"),
	}, nil
}

// newBackup is the kernel's core backup service. It is called by nothing yet.
func (k *Kernel) newBackup(ctx context.Context) (*backup.Svc, error) {
	in, err := k.backupWireInput(ctx)
	if err != nil {
		return nil, err
	}
	return newBackupSvc(in)
}

// backupMount is what mountBackup needs besides the service.
type backupMount struct {
	Svc *backup.Svc
	// Enqueuer starts core.backup.create for POST /backups; F5-18's executor.
	Enqueuer backuphttp.Enqueuer
	// Audit records create, import and download before the action.
	Audit backuphttp.Recorder
	// OwnerRequired is core.auth.owner.required; nil means the owner is
	// required, the stricter reading.
	OwnerRequired  func() bool
	MaxImportBytes int64
	Log            *slog.Logger
}

// mountBackup mounts 05-backup §5B.3.8's routes on g. It is not called at boot
// today: a nil Svc mounts nothing, so a caller that has no service fails
// closed. The four task kinds are registerBackupTasks'.
func mountBackup(g *httpapi.Group, m backupMount) {
	if m.Svc == nil {
		return
	}
	backuphttp.Register(g, backuphttp.Deps{
		Catalogue: m.Svc, Restorer: m.Svc, Rewrapper: m.Svc, Enqueuer: m.Enqueuer, Audit: m.Audit,
		OwnerRequired: m.OwnerRequired, MaxImportBytes: m.MaxImportBytes, Log: m.Log,
	})
}

// registerBackupTasks records the four core.backup.* kinds (create, verify,
// prune, stage-restore) on r, the one place the kernel builds them. A nil Svc
// records nothing: the kinds would fail closed anyway (tasks.Problems reports
// a definition with no Run), and an unwired service must not look registered.
// Nothing calls it at boot: registering core's own kinds is G3, after F5-18,
// which also settles whose Registrar this is.
func registerBackupTasks(r *module.Registrar, svc *backup.Svc) {
	if svc == nil {
		return
	}
	r.Tasks(backup.CreateTask(svc), backup.VerifyTask(svc), backup.PruneTask(svc), backup.StageRestoreTask(svc))
}
