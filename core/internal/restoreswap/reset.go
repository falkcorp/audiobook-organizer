// file: core/internal/restoreswap/reset.go
// version: 1.0.0
// guid: 5f0c9b27-3e81-4a6d-b2f4-7d18e6a0c953
// last-edited: 2026-11-08

package restoreswap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// intentTTL is how long a staged intent stays applicable: P0a rejects it
// after expires_at (05-backup §5B.3.4 step a), so a reset staged and then
// forgotten never resets a store days later.
const intentTTL = 24 * time.Hour

// configBootRecord is <state_dir>/config.boot.json (05-config §1.3.5), a
// reset target: settings start at core's defaults after the reset.
const configBootRecord = "config.boot.json"

// NewID is a fresh intent ID: the UTC time and 8 random hex digits, one
// path segment.
func NewID(now time.Time) string {
	var b [4]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails (Go 1.24)
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}

// ResetSpec is what StageReset stages (05-storage §5S.3.15 "Stage"): the
// kernel fills it once the request passed Validate, the hook's Plan and the
// confirmation check.
type ResetSpec struct {
	ID                 string // "" = NewID
	DBPath             string // core.storage.path, absolute
	Reset              restorejournal.ResetKind
	DeleteLibraryFiles bool
	Plan               storage.ResetPlan // ResetStore: zero
	InstallID          string            // the live store's (set_aside_started's Record.InstallID, DATA9-15)
	RequestedBy        string
	// AuxDBs are the auxiliary DBs the kernel opened (ResetFactory only):
	// set aside with the store when on its filesystem, else listed in the
	// intent's delete (a Recreatable one). The R1 kernel opens none.
	AuxDBs []string
}

// ResetTargets is a reset's set-aside list for a store at dbPath: the DB
// directory, its sidecars (the core suffixes and each legacy suffix), the
// config boot record and each auxiliary DB, each to its SetAsideDest. Not
// the checkpoints, which stay under the sweeper's clock (§5S.3.15 "Stage",
// V9).
func ResetTargets(stateDir, dbPath, id string, legacySuffixes, aux []string) []restorejournal.Target {
	db := filepath.Clean(dbPath)
	paths := []string{db}
	for _, s := range append(restorejournal.CoreSidecarSuffixes(), legacySuffixes...) {
		paths = append(paths, db+s)
	}
	paths = append(paths, filepath.Join(stateDir, configBootRecord))
	paths = append(paths, aux...)
	var out []restorejournal.Target
	seen := map[string]bool{}
	for _, p := range paths {
		p = filepath.Clean(p)
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, restorejournal.Target{From: p, To: restorejournal.SetAsideDest(stateDir, db, id, p)})
	}
	return out
}

// PlanKept is rule (4) of the reset hook (restorejournal.Kept) for a reset
// of the store at dbPath that is not staged yet: Kept over the targets
// StageReset records for it (ResetTargets with the same auxiliary DBs). The
// set-aside destinations differ only by the intent's ID and lie inside the
// .mediacore-restore trees Kept holds anyway, so a path PlanKept reports is
// one the confined filesystem refuses at Run (ErrKeptPath). The reset
// hook's Plan counts its wipe with it (module.ResetPlanKept), so the count
// the operator confirms is what Run can remove.
func (s *Swapper) PlanKept(dbPath string, aux []string) func(path string) bool {
	stateDir, legacy := s.o.StateDir, s.legacySuffixes()
	in := restorejournal.Intent{DBPath: filepath.Clean(dbPath), Targets: ResetTargets(stateDir, dbPath, "", legacy, aux)}
	return func(p string) bool { return restorejournal.Kept(stateDir, in, legacy, p) }
}

// StageReset is 05-storage §5S.3.15 "Stage": it resolves the targets
// (ResetTargets), records the plan's bytes and paths (hook_args,
// hook_paths) and the live install ID, and stages the intent through
// Stage, which pre-validates every rename (ErrRenameRefused: another
// filesystem, read-only, a mount point or a single-file bind mount), the
// target shapes (ErrTargetsRefused) and the delete entries, and creates the
// one intent in the slot (ErrSwapPending while any swap is staged or in
// flight). Nothing is staged when any check fails.
func (s *Swapper) StageReset(ctx context.Context, r ResetSpec) (restorejournal.Intent, error) {
	if r.Reset != restorejournal.ResetFactory && r.Reset != restorejournal.ResetStore {
		return restorejournal.Intent{}, fmt.Errorf("restoreswap: reset kind %q is neither store nor factory", r.Reset)
	}
	if r.Reset == restorejournal.ResetStore && (r.DeleteLibraryFiles || len(r.Plan.Hook) > 0 || len(r.Plan.Paths) > 0) {
		return restorejournal.Intent{}, errors.New("restoreswap: a store reset carries no hook plan and deletes no file")
	}
	for _, p := range r.Plan.Paths {
		if !filepath.IsAbs(p) {
			return restorejournal.Intent{}, fmt.Errorf("restoreswap: hook path %q is not absolute", p)
		}
	}
	now := s.now()
	id := r.ID
	if id == "" {
		id = NewID(now)
	}
	var aux []string
	if r.Reset == restorejournal.ResetFactory {
		aux = r.AuxDBs
	}
	in := restorejournal.Intent{
		ID: id, Kind: restorejournal.KindReset, Reset: r.Reset, DeleteLibraryFiles: r.DeleteLibraryFiles,
		DBPath: filepath.Clean(r.DBPath), Targets: ResetTargets(s.o.StateDir, r.DBPath, id, s.legacySuffixes(), aux),
		StagedByBuild: s.o.BuildID, ExpiresAt: now.Add(intentTTL), RequestedBy: r.RequestedBy, RequestedAt: now,
		InstallID: r.InstallID, HookArgs: r.Plan.Hook, HookPaths: slices.Clone(r.Plan.Paths),
	}
	return in, s.Stage(ctx, in)
}

// HookIntent is the intent of the applied reset id whose reset hook runs
// (05-storage §5S.3.15 "The app's reset hook"): its in-flight file, which
// must name a KindReset swap. The kernel serializes the hook's Done and
// collects its kept paths; this package takes no lock (the slot is decided
// by link(2) alone).
func (s *Swapper) HookIntent(id string) (restorejournal.Intent, error) {
	in, err := readIntent(s.fs, s.inflightPath(id))
	if err != nil {
		return restorejournal.Intent{}, err
	}
	if in.Kind != restorejournal.KindReset {
		return restorejournal.Intent{}, fmt.Errorf("restoreswap: journal %s is a %s swap, not a reset", id, in.Kind)
	}
	return in, nil
}

// HookDone is ResetRun.Done's journal side: hook_done with the hook's
// failures and the kept paths its filesystem refused (unless the journal
// already reads hook_done), then the in-flight file deleted. Owed only
// after applied. Calling it again after it succeeded is a no-op.
func (s *Swapper) HookDone(ctx context.Context, id string, failed, kept []string) error {
	return s.hookDone(ctx, id, failed, kept)
}

// hookDone journals hook_done (unless the journal already reads it) and
// deletes the in-flight file. Owed only after applied.
func (s *Swapper) hookDone(ctx context.Context, id string, failed, kept []string) error {
	last, err := s.last(id)
	if err != nil {
		return err
	}
	switch last {
	case StepApplied:
		if err := s.appendRec(ctx, id, restorejournal.Record{Step: StepHookDone, Failed: failed, Kept: kept}); err != nil {
			return err
		}
	case StepHookDone:
		if ok, err := exists(s.fs, s.inflightPath(id)); err != nil || !ok {
			return err // done before: nothing left to delete
		}
	default:
		return fmt.Errorf("restoreswap: reset %s owes hook_done only after applied; its journal reads %q", id, last)
	}
	return s.removeInflight(ctx, id)
}

// ConfinedFiles is the reset hook's storage.StateFiles (05-storage
// §5S.3.15 "Hook confinement", R10-6, COR10-4): it follows a path only
// inside the state directory, <db parent>, the intent's delete entries and
// its hook_paths, never parsing hook_args; anything else is
// storage.ErrConfined. Removing a kept path (rule (4),
// restorejournal.Kept: <db parent>, the state directory, restore/, a swap's
// private directory, the fresh store) is storage.ErrKeptPath, a skip
// reported to onKept, which the hook_done record lists as kept. On the real
// filesystem a path whose directory resolves through a symbolic link to
// outside the confinement is refused too. It holds no state of its own, so
// a hook may call it from several goroutines; onKept must be safe for that.
type ConfinedFiles struct {
	fs       vfs.FS
	stateDir string
	in       restorejournal.Intent
	legacy   []string
	roots    []string
	real     bool // the OS filesystem: symbolic links resolve
	onKept   func(path string)
}

var _ storage.StateFiles = (*ConfinedFiles)(nil)

// Confined is the reset hook's confined filesystem for the applied reset
// in; onKept (nil = none) receives each kept path RemoveAll refused.
func (s *Swapper) Confined(in restorejournal.Intent, onKept func(path string)) *ConfinedFiles {
	roots := []string{filepath.Clean(s.o.StateDir), filepath.Dir(filepath.Clean(in.DBPath))}
	for _, p := range append(slices.Clone(in.Delete), in.HookPaths...) {
		if filepath.IsAbs(p) {
			roots = append(roots, filepath.Clean(p))
		}
	}
	if onKept == nil {
		onKept = func(string) {}
	}
	return &ConfinedFiles{fs: s.fs, stateDir: s.o.StateDir, in: in, legacy: s.legacySuffixes(), roots: roots,
		real: vfs.Root(s.fs) == vfs.Default, onKept: onKept}
}

// check refuses a path outside the confinement.
func (c *ConfinedFiles) check(p string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("%w: %q is not absolute", storage.ErrConfined, p)
	}
	p = filepath.Clean(p)
	if !c.within(p, c.roots) {
		return fmt.Errorf("%w: %s", storage.ErrConfined, p)
	}
	if !c.real {
		return nil
	}
	// The directory that holds p, resolved: a link inside a root that names
	// a directory outside every root is no way out.
	dir, err := filepath.EvalSymlinks(filepath.Dir(p))
	if errors.Is(err, os.ErrNotExist) {
		return nil // nothing there to follow
	}
	if err != nil {
		return fmt.Errorf("%w: resolving %s: %w", storage.ErrConfined, filepath.Dir(p), err)
	}
	var resolved []string
	for _, r := range c.roots {
		if rr, err := filepath.EvalSymlinks(r); err == nil {
			resolved = append(resolved, rr)
		}
	}
	if !c.within(filepath.Join(dir, filepath.Base(p)), resolved) {
		return fmt.Errorf("%w: %s resolves to %s", storage.ErrConfined, p, dir)
	}
	return nil
}

func (c *ConfinedFiles) within(p string, roots []string) bool {
	for _, r := range roots {
		if restorejournal.InTree(p, r) {
			return true
		}
	}
	return false
}

// lstat is os.Lstat on the real filesystem (a link is an entry of its own,
// removed as one), Stat on any other (no links).
func (c *ConfinedFiles) lstat(p string) (os.FileInfo, error) {
	if c.real {
		return os.Lstat(p)
	}
	return c.fs.Stat(p)
}

// ReadDir lists the directory at path, sorted by name.
func (c *ConfinedFiles) ReadDir(path string) ([]iofs.DirEntry, error) {
	if err := c.check(path); err != nil {
		return nil, err
	}
	names, err := c.fs.List(path)
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	out := make([]iofs.DirEntry, 0, len(names))
	for _, n := range names {
		fi, err := c.lstat(filepath.Join(path, n))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, iofs.FileInfoToDirEntry(fi))
	}
	return out, nil
}

// RemoveAll removes path and everything under it, then syncs its
// directory; removing what is gone is success.
func (c *ConfinedFiles) RemoveAll(path string) error {
	if err := c.check(path); err != nil {
		return err
	}
	path = filepath.Clean(path)
	if restorejournal.Kept(c.stateDir, c.in, c.legacy, path) {
		c.onKept(path)
		return fmt.Errorf("%w: %s", storage.ErrKeptPath, path)
	}
	if _, err := c.lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := c.fs.RemoveAll(path); err != nil {
		return err
	}
	return syncDir(c.fs, filepath.Dir(path))
}
