// file: core/kernel/identity_p4d.go
// version: 0.1.0
// guid: 3d8a51f2-6c07-4b9e-a1d4-7e20c5b98f63
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/internal/journalfs"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/internal/statefs"
	"github.com/falkcorp/mediacore/rbac"
)

// This file is the identity boot's P4d (06 §6.5.1 P4d steps 2-4): it builds the
// revocation journal's component over the boot's journal filesystem, hands
// runP4dSteps the live P4c handles, and keeps the result for the health probe
// and for P5. It runs only when the app declared the flip
// (identityBootDeclared), right after attachCoreConfig attached the DB layer
// (step 1), because the effective core.backup.dir is read from it.
//
// It is the one file of the boot that imports core/internal/journalfs, so the
// crash-matrix kit sees every journal file write (TestJournalRowsAreUnboundUntilF408).

// LabelBackupDirMkdir is the crash-matrix label of the kernel's creation of
// the effective backup directory (identity_p4d.go ensureBackupDir): the
// journal file is created inside it, so the directory must exist first. It is
// a no-operation when the directory is there, as every boot after the first
// finds it.
const LabelBackupDirMkdir = "kernel.backup.dir.mkdir"

// backupDirMode is the mode of a backup directory the kernel creates: the
// journal and the archives it holds are credentials-adjacent (revocation
// lines, key hashes in an archive), so owner-only.
const backupDirMode = fs.FileMode(0o700)

// resolveBackupDir is core.backup.dir's effective directory: an absolute value
// is itself, an empty one is "backups", and a relative one resolves against
// the store's parent directory, never the working directory (05-backup §5B.7;
// the same rule as HEAD's internal/backup.ResolveDir, which core cannot
// import).
func resolveBackupDir(configured, dbPath string) string {
	if filepath.IsAbs(configured) {
		return configured
	}
	dir := configured
	if dir == "" {
		dir = "backups"
	}
	if dbPath == "" {
		return dir
	}
	return filepath.Join(filepath.Dir(dbPath), dir)
}

// archivePrefix is the app's archive prefix (05-backup §5B.3.6 row "Where"):
// Config.ArchivePrefix, or <AppID>_ (the archive name's default,
// `<AppID>_<YYYYMMDD_HHMMSS><ext>`). The journal's file name and header both
// carry it, so two apps sharing a backup directory never share a journal.
func (k *Kernel) archivePrefix() string {
	if k.cfg.ArchivePrefix != "" {
		return k.cfg.ArchivePrefix
	}
	return k.cfg.AppID + "_"
}

// handleOf is the live handle of section type T among the core sections'
// handles, or false.
func handleOf[T any](handles []any) (*config.Handle[T], bool) {
	for _, h := range handles {
		if th, ok := h.(*config.Handle[T]); ok {
			return th, true
		}
	}
	return nil, false
}

// authSettingsOver is auth.Deps.Settings over the live core.auth section.
func (k *Kernel) authSettingsOver() (func() auth.Settings, error) {
	h, ok := handleOf[authSettings](k.cfgBoot.handles)
	if !ok {
		return nil, errors.New("kernel: identity: the core.auth section is not registered")
	}
	return authSettingsReader(h.Get, configExplicit(k.cfgBoot.store), k.cfg.AppID)
}

// stateFiles is auth.StateFiles over the labelled state directory (statefs):
// a write is a temporary file, fsync, rename and a directory fsync, every step
// under the caller's label; a removal is followed by a directory fsync.
type stateFiles struct{ dir *statefs.Dir }

// WriteFile writes name atomically, so a reader never sees half a credential.
func (s stateFiles) WriteFile(label, name string, data []byte, perm fs.FileMode) error {
	tmp := name + ".tmp"
	if err := s.dir.Remove(label, tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("kernel: clear %s: %w", tmp, err)
	}
	if err := s.dir.WriteNew(label, tmp, data, perm); err != nil {
		return errors.Join(fmt.Errorf("kernel: write %s: %w", tmp, err), s.removeQuiet(label, tmp))
	}
	if err := s.dir.Rename(label, tmp, name); err != nil {
		return errors.Join(fmt.Errorf("kernel: rename %s: %w", tmp, err), s.removeQuiet(label, tmp))
	}
	return s.dir.SyncDir(label, ".")
}

// Remove removes name; a name that is not there is fs.ErrNotExist, which auth
// treats as done.
func (s stateFiles) Remove(label, name string) error {
	if err := s.dir.Remove(label, name); err != nil {
		return err
	}
	return s.dir.SyncDir(label, ".")
}

func (s stateFiles) removeQuiet(label, name string) error {
	if err := s.dir.Remove(label, name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// swappedBackFor reports whether restore journal id reads swapped_back
// (reapplyInput.SwappedBack): such a restore's line is neither resumed nor
// evidence. A boot with no swap, or a journal that cannot be read, is false:
// a restore that cannot be shown to have been swapped back is treated as live.
func (k *Kernel) swappedBackFor(id string) bool {
	if k.swap == nil || k.swap.s == nil {
		return false
	}
	recs, err := k.swap.s.Journal(id)
	if err != nil {
		return false
	}
	for _, r := range recs {
		if r.Step == restoreswap.StepSwappedBack {
			return true
		}
	}
	return false
}

// ensureBackupDir creates the effective backup directory, in the journal
// filesystem's region LabelBackupDirMkdir, so the crash-matrix kit can kill
// or fail it like any boot writer.
func ensureBackupDir(fsys journal.FS, dir string) error {
	exit := fsys.Region(LabelBackupDirMkdir)
	defer exit()
	if err := fsys.MkdirAll(dir, backupDirMode); err != nil {
		return fmt.Errorf("kernel: create the backup directory: %w", err)
	}
	return nil
}

// errRestoreBootNeedsDerivation refuses an identity boot that is also a restore
// boot. P4d step 2 and 3 take a *restoreBoot derived from the P0a outcome (the
// source's boundary, the pre-swap store's directory, the restore journal's
// edges), and that derivation is not built: running them as a plain boot would
// misread the restore (its line is never appended, so every revocation journaled
// since the backup is lost to the hooks). Refusing is fail-closed, and no app
// declares identity before F4-08's later slices.
var errRestoreBootNeedsDerivation = errors.New("kernel: a restore boot cannot run the identity restore steps yet: the restore boot derivation is not built")

// runIdentityP4d is the identity boot's P4d: steps 2 (the revocation re-apply),
// 3 (the restore hooks) and 4 (auth's boot writes), through runP4dSteps. Any
// error wraps errP4dRefuse and the boot fails: nothing is served.
//
// The journal component is built once per boot over journalfs.From(ctx), kept
// in k.ident before the steps run so a failed step still closes it at
// closeCore (the component holds the journal file's lock), and started after
// they succeeded: the anchor ticks only for a journal that is bound.
func (k *Kernel) runIdentityP4d(ctx context.Context) error {
	id := k.ident
	if id == nil || k.store == nil || k.store.db == nil || k.sec == nil || k.cfgBoot == nil || k.audit == nil {
		return errors.New("kernel: identity: P4d needs the P4c stores, the audit store, the secret store and the config boot")
	}
	if k.swap != nil && k.swap.out.Boot == restoreswap.BootRestore {
		return fmt.Errorf("%w: %w", errP4dRefuse, errRestoreBootNeedsDerivation)
	}
	db := k.store.db
	jfs, err := journalfs.From(ctx)
	if err != nil {
		return fmt.Errorf("kernel: identity: the journal filesystem: %w", err)
	}
	bh, ok := handleOf[backupSettings](k.cfgBoot.handles)
	if !ok {
		return errors.New("kernel: identity: the core.backup section is not registered")
	}
	backupDir := resolveBackupDir(bh.Get().Dir, k.cfg.StoragePath)
	if err := ensureBackupDir(jfs, backupDir); err != nil {
		return err
	}
	bks, ok := db.Keyspace(journal.Owner)
	if !ok {
		return fmt.Errorf("kernel: identity: no %s keyspace in the store", journal.Owner)
	}
	binary, err := k.binaryID(ctx)
	if err != nil {
		return err
	}
	install := db.Admin().InstallID()
	clock := bootClock(ctx)
	log := k.coreLog("journal")
	prefix := k.archivePrefix()
	comp, err := journal.NewComponent(journal.ComponentOptions{
		StateDir: k.cfg.StateDir, DBPath: k.cfg.StoragePath, Keyspace: bks, Keys: k.sec.store,
		InstallID: install, App: prefix, Source: binary, Clock: clock, Log: log, FS: jfs,
	})
	if err != nil {
		return fmt.Errorf("kernel: identity: the journal component: %w", err)
	}
	id.journal, id.jfs = comp, jfs
	settings, err := k.authSettingsOver()
	if err != nil {
		return err
	}
	id.settings = settings
	id.stateFiles = stateFiles{dir: k.sec.root}

	stores := hookStores{Auth: id.users, Roles: id.roles, Audit: k.audit, Journal: comp, Clock: clock, Log: log}
	res, err := runP4dSteps(ctx, p4dStepsInput{
		Reapply: reapplyInput{
			Journal: comp, KS: bks, Keys: k.sec.store, Overrides: true, Install: install,
			Identity: db.Admin().StoreIdentity(), DBPath: k.cfg.StoragePath, StateDir: k.cfg.StateDir,
			BackupDir: backupDir, JournalPath: filepath.Join(backupDir, prefix+"revocations.journal"),
			SwappedBack: k.swappedBackFor, Apply: reapplyApplyFor(stores), Clock: clock, Log: log, FS: jfs,
		},
		OpenPrevious: func(ctx context.Context, dir string) (previousView, error) {
			return db.Admin().OpenReadOnly(ctx, dir)
		},
		Hooks: restoreHookList(stores),
		BootWrites: func(ctx context.Context) error {
			return auth.BootWrites(ctx, auth.RestoreDeps{
				Store: id.users, NewBatch: id.users.NewBatch, Revocations: comp, StateFiles: id.stateFiles,
				Clock: clock, Settings: settings, RoleGrantsAdmin: id.roleGrantsAdmin, Log: log,
			})
		},
	})
	id.p4d = res
	if err != nil {
		return err
	}
	comp.Start()
	return k.registerBackupHealth()
}

// roleGrantsAdmin is auth.RestoreDeps.RoleGrantsAdmin over the role store: an
// unknown role grants nothing, so BootWrites picks no owner for the start-up
// key from it.
func (id *identityBoot) roleGrantsAdmin(ctx context.Context, name rbac.RoleName) (bool, error) {
	r, err := id.roles.GetRole(ctx, name)
	switch {
	case errors.Is(err, rbac.ErrUnknownRole):
		return false, nil
	case err != nil:
		return false, err
	}
	return rbac.GrantsAdmin(r.Permissions()), nil
}

// registerBackupHealth registers core.backup with the probe over the journal's
// tokens and the restore hooks' (backupProbe): the one owner of that
// component's Result. The tokens are posture reasons, so they reach only the
// authenticated degraded field of GET /api/v1/system/status (health's
// isPostureReason), never the unauthenticated surfaces. The probe reads the
// hook report this boot produced, which is empty on a boot whose hooks did not
// run.
func (k *Kernel) registerBackupHealth() error {
	id := k.ident
	if k.healthReg == nil {
		return errors.New("kernel: identity: no health registry is built")
	}
	_, err := k.healthReg.Register(health.Check{
		ID: componentBackup, Title: "Backup and restore",
		Probe: backupProbe(id.journal.Health, func() hookReport { return id.p4d.Hooks.Report }),
	})
	if err != nil {
		return fmt.Errorf("kernel: identity: health: %w", err)
	}
	return nil
}

// closeIdentity closes the journal component (it stops the anchor, writes the
// last anchor and releases the journal file's lock); it must run before the
// store closes, which the anchor reads.
func (k *Kernel) closeIdentity(ctx context.Context) error {
	if k.ident == nil || k.ident.journal == nil {
		return nil
	}
	if err := k.ident.journal.Close(ctx); err != nil {
		return fmt.Errorf("kernel: close the revocation journal: %w", err)
	}
	return nil
}
