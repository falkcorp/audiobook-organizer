// file: core/backup/stage.go
// version: 1.0.0
// guid: a9c2e5f1-3d70-4b84-8f6a-2e1d9b7c4a03
// last-edited: 2026-11-12

package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/backuparchive"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// ErrNoUndoPoint: no applied restore has a kept previous/ to go back to.
var ErrNoUndoPoint = errors.New("backup: there is no restore to undo")

// WarnGapFill is what a staged legacy-abo/0 restore warns (05-config §1.12.2,
// DATA-15): HEAD filled stored-empty fields from the config.yaml beside the
// database, which the migration reads from the restored host.
const WarnGapFill = "this archive predates the config cutover: every setting HEAD filled from the config.yaml beside the database migrates empty unless that file is installed beside the restored database before the next start"

const intentTTLDefault = 24 * time.Hour

func newRestoreID(now time.Time) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}

func (s *Svc) actor(ctx context.Context) string {
	if s.o.Actor != nil {
		if a := s.o.Actor(ctx); a != "" {
			return a
		}
	}
	return "unknown"
}

func (s *Svc) probe() restorejournal.Probe {
	if s.o.Probe != nil {
		return s.o.Probe
	}
	return restorejournal.OSProbe()
}

// slot reads the one swap slot without acting on it: the pending intent, if
// any, and the in-flight ids. An unreadable pending file counts as taken.
func (s *Svc) slot() (pending *restorejournal.Intent, taken bool, inflight []string, err error) {
	pp := restorejournal.PendingPath(s.o.StateDir)
	if _, err := os.Lstat(pp); err == nil {
		taken = true
		if b, err := readBounded(pp, 1<<20); err == nil {
			var in restorejournal.Intent
			if json.Unmarshal(b, &in) == nil {
				pending = &in
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, nil, err
	}
	ents, err := os.ReadDir(restorejournal.Dir(s.o.StateDir))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, false, nil, err
	}
	for _, e := range ents {
		if e.IsDir() {
			if _, err := os.Lstat(filepath.Join(restorejournal.Dir(s.o.StateDir), e.Name(), "inflight.json")); err == nil {
				inflight = append(inflight, e.Name())
				taken = true
			}
		}
	}
	return pending, taken, inflight, nil
}

// StageRestore is the API phase of a restore, §5B.3.4 steps 1-9. It writes
// only under <db parent>/.mediacore-restore/<id>/ and <state_dir>/restore/;
// the live store is never opened for writing (TestRestoreNeverWritesLiveStore).
func (s *Svc) StageRestore(ctx context.Context, id ArchiveID, o RestoreOptions) (pr PendingRestore, err error) {
	release, err := s.acquire()
	if err != nil {
		return PendingRestore{}, err
	}
	defer release()
	// 1. The early refusal; Stage's atomic create is the one that decides.
	if _, taken, _, err := s.slot(); err != nil {
		return PendingRestore{}, err
	} else if taken {
		return PendingRestore{}, restorejournal.ErrSwapPending
	}
	f, err := s.find(ctx, id)
	if err != nil {
		return PendingRestore{}, err
	}
	// 2. Verify.
	if err := s.verifyForRestore(ctx, f, o); err != nil {
		s.o.Metrics.Restore(ctx, "rejected", o.Foreign)
		return PendingRestore{}, err
	}
	// 3. Provenance, from the archive's own manifest, never a sidecar.
	var (
		man  *Manifest
		open *openedArchive
	)
	if f.Format == FormatMediacore {
		if open, err = openArchive(f.Path, s.limits()); err != nil {
			return PendingRestore{}, err
		}
		defer open.Close()
		man = open.Manifest
	}
	class := PreProvenance
	if man != nil {
		live := ""
		if s.o.Store != nil {
			live = s.o.Store.InstallID()
		}
		class = Classify(man, s.o.Keyring, live)
	}
	if class != Own && !o.Foreign {
		s.o.Metrics.Restore(ctx, "rejected", false)
		return PendingRestore{}, fmt.Errorf("%w (%s)", ErrForeignArchive, class)
	}
	foreign := class != Own
	if s.o.Audit != nil {
		if err := s.o.Audit(ctx, "core.restore.stage", map[string]string{"foreign": fmt.Sprint(foreign), "archive": string(id)}); err != nil {
			return PendingRestore{}, fmt.Errorf("backup: audit: %w", err)
		}
	}
	// 4. Compatibility, before anything is extracted.
	var warnings []string
	if man != nil {
		if warnings, err = s.compat(man); err != nil {
			s.o.Metrics.Restore(ctx, "rejected", foreign)
			return PendingRestore{}, err
		}
	} else {
		warnings = append(warnings, WarnGapFill)
	}
	// 6/7. Extract into staging on the database's filesystem.
	now := s.o.now().UTC()
	rid := newRestoreID(now)
	staging := restorejournal.StagingDir(s.o.DBPath, rid)
	cleanup := func() {
		_ = os.RemoveAll(filepath.Dir(staging))
		_ = os.RemoveAll(restorejournal.JournalDir(s.o.StateDir, rid))
	}
	staged := false
	defer func() {
		if !staged {
			cleanup()
		}
	}()
	if err := mkdirPrivate(staging); err != nil {
		return PendingRestore{}, err
	}
	if err := s.sameDevice(filepath.Dir(staging)); err != nil {
		return PendingRestore{}, err
	}
	var manifestSHA string
	if man != nil {
		if manifestSHA, err = s.extractCore(ctx, open, staging); err != nil {
			return PendingRestore{}, err
		}
	} else if manifestSHA, err = s.extractLegacy(ctx, f.Path, staging); err != nil {
		return PendingRestore{}, err
	}
	// 5. Secrets: open the staged checkpoint read-only.
	if s.o.Inspector == nil {
		return PendingRestore{}, errors.New("backup: restore needs an Inspector")
	}
	info, err := s.o.Inspector.Inspect(ctx, filepath.Join(staging, "db"))
	if err != nil {
		return PendingRestore{}, fmt.Errorf("backup: the staged store does not open: %w", err)
	}
	w, err := s.checkSecrets(info, o)
	if err != nil {
		return PendingRestore{}, err
	}
	warnings = append(warnings, w...)
	// 6. The config file and boot record: placed only when asked, whole and
	// passing the rename pre-check; otherwise left for the operator.
	targets := []restorejournal.Target{{From: filepath.Join(staging, "db"), To: s.o.DBPath}}
	cfgTargets, w, err := s.configTargets(staging, rid, man, o)
	if err != nil {
		return PendingRestore{}, err
	}
	targets, warnings = append(targets, cfgTargets...), append(warnings, w...)
	// 8. Create the intent in the one slot.
	ttl := s.o.settings().IntentTTL
	if ttl <= 0 {
		ttl = intentTTLDefault
	}
	install := ""
	if s.o.Store != nil {
		install = s.o.Store.InstallID()
	}
	actor := s.actor(ctx)
	in := restorejournal.Intent{
		ID: rid, Kind: restorejournal.KindArchive, DBPath: s.o.DBPath, Targets: targets,
		StagedByBuild: s.o.BuildID, ExpiresAt: now.Add(ttl), RequestedBy: actor, RequestedAt: now,
		ArchiveID: string(id), Staging: staging, InstallID: install, ManifestSHA256: manifestSHA,
	}
	if s.o.Stager == nil {
		return PendingRestore{}, errors.New("backup: restore needs a Stager")
	}
	if err := s.o.Stager.Stage(ctx, in); err != nil {
		s.o.Metrics.Restore(ctx, "rejected", foreign)
		return PendingRestore{}, err
	}
	staged = true
	if err := s.writeStageNote(rid, stageNote{ArchiveID: id, Foreign: foreign, Warnings: warnings}); err != nil {
		s.o.log().Warn("backup: could not write the stage note; the archive is not floor-protected", "error", err)
		warnings = append(warnings, "the archive is not protected from retention while this restore is pending")
	}
	s.o.Metrics.Restore(ctx, "staged", foreign)
	pr = pendingOf(in, warnings)
	// 9. Restart, when asked. The restore is staged either way.
	if o.Restart && s.o.Restart != nil {
		if err := s.o.Restart(ctx); err != nil {
			pr.Warnings = append(pr.Warnings, "the restart request failed: "+err.Error())
		}
	}
	return pr, nil
}

func pendingOf(in restorejournal.Intent, warnings []string) PendingRestore {
	k := journal.KindArchive
	if in.Kind == restorejournal.KindRevert {
		k = journal.KindRevert
	}
	return PendingRestore{
		ArchiveID: ArchiveID(in.ArchiveID), Kind: k, StagedAt: in.RequestedAt, StagedByBuild: in.StagedByBuild,
		ExpiresAt: in.ExpiresAt, RequestedBy: in.RequestedBy, Warnings: warnings,
	}
}

// verifyForRestore is step 2: at the requested level (default open). A level
// below open, and an archive with no checksum sidecar, need AllowWeakVerify.
func (s *Svc) verifyForRestore(ctx context.Context, f found, o RestoreOptions) error {
	level := o.Verify
	if level == VerifyInvalid {
		level = VerifyOpen
	}
	if level < VerifyOpen && !o.AllowWeakVerify {
		return fmt.Errorf("backup: verification at the %s level needs AllowWeakVerify", level)
	}
	_, err := s.verifyWith(ctx, f.Path, f.ID, level, false)
	if errors.Is(err, ErrNoChecksum) && o.AllowWeakVerify {
		_, err = s.verifyWith(ctx, f.Path, f.ID, level, true)
	}
	return err
}

// compat is step 4 for a mediacore-backup/1 archive: the app, the levels, the
// Pebble format and the build's upgrade floors, all from the manifest. Owners
// the build lacks are restored dormant, with a warning. A floor that requires
// done data migrations cannot be proven from the manifest; the Inspector's open
// of the staged store applies it (a *storage.ErrUpgradeFloor either way).
func (s *Svc) compat(m *Manifest) (warnings []string, err error) {
	if m.App != s.o.AppID {
		return nil, fmt.Errorf("%w: the archive is of %q, this is %q", ErrWrongApp, m.App, s.o.AppID)
	}
	supported := storage.Supported(s.o.Owners)
	owners := make([]string, 0, len(m.Levels))
	for o := range m.Levels {
		owners = append(owners, o)
	}
	sort.Strings(owners)
	for _, o := range owners {
		have, ok := supported[storage.Owner(o)]
		switch {
		case !ok:
			warnings = append(warnings, fmt.Sprintf("module %s in the backup is not in this build: its data is restored and dormant", o))
		case m.Levels[o] > have:
			return nil, fmt.Errorf("%w: %s is at level %d, this build serves %d", ErrBackupTooNew, o, m.Levels[o], have)
		}
	}
	if s.o.PebbleFormat != 0 && m.PebbleFormat > s.o.PebbleFormat {
		return nil, fmt.Errorf("%w: Pebble format %d, this build's is %d", ErrBackupTooNew, m.PebbleFormat, s.o.PebbleFormat)
	}
	for _, own := range s.o.Owners {
		f := own.Spec.UpgradeFloor
		if f == nil {
			continue
		}
		have := m.Keyspaces[string(f.Owner)].Version
		if have < f.Version {
			return nil, &storage.ErrUpgradeFloor{Owner: f.Owner, Have: have, Floor: f.Version, InstallFirst: f.InstallFirst}
		}
	}
	for mod := range m.Modules {
		if _, ok := s.o.Modules[mod]; !ok {
			warnings = append(warnings, fmt.Sprintf("module %s in the backup is not in this build: its data is restored and dormant", mod))
		}
	}
	sort.Strings(warnings)
	return warnings, nil
}

// extractCore writes every entry of the archive into staging, each checked
// against the manifest as it streams, then the manifest itself beside them (step
// b's anchor), everything fsynced. It returns the manifest's SHA-256.
func (s *Svc) extractCore(ctx context.Context, a *openedArchive, staging string) (string, error) {
	ex, err := backuparchive.NewExtractor(staging)
	if err != nil {
		return "", err
	}
	cerr := a.consume(ctx, ex, "")
	if cerr == nil {
		_, cerr = ex.WriteFile(ManifestName, bytes.NewReader(a.ManifestBytes), nil)
	}
	if serr := errors.Join(ex.Sync(), ex.Close()); cerr == nil {
		cerr = serr
	}
	if cerr != nil {
		return "", cerr
	}
	return SumManifest(a.ManifestBytes), nil
}

// legacyStaged is what a staged legacy-abo/0 restore keeps beside the store: the
// archive's outer SHA-256 and the extracted files' names and sizes, the anchor
// step b re-checks (a legacy archive has no per-entry digests).
type legacyStaged struct {
	Format  string  `json:"format"`
	Archive string  `json:"archive_sha256"`
	Codec   string  `json:"codec"`
	Entries []Entry `json:"entries"`
}

const legacyManifestName = "legacy-manifest.json"

func (s *Svc) extractLegacy(ctx context.Context, archive, staging string) (string, error) {
	info, err := backuparchive.ScanLegacy(ctx, archive, s.limits())
	if err != nil {
		return "", err
	}
	db := filepath.Join(staging, "db")
	if err := os.Mkdir(db, 0o700); err != nil {
		return "", err
	}
	if err := backuparchive.ExtractLegacy(ctx, archive, info, db, s.limits()); err != nil {
		return "", err
	}
	var ents []Entry
	err = filepath.WalkDir(db, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(staging, p)
		ents = append(ents, Entry{Path: filepath.ToSlash(rel), Size: fi.Size(), Class: SystemOfRecord})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Path < ents[j].Path })
	b, err := json.Marshal(legacyStaged{Format: FormatLegacyABO, Archive: info.SHA256, Codec: info.Codec, Entries: ents})
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(filepath.Join(staging, legacyManifestName), b); err != nil {
		return "", err
	}
	if err := syncDir(staging); err != nil {
		return "", err
	}
	return SumManifest(b), nil
}

// checkSecrets is step 5: every key ID the staged store's secret records
// reference must be in the keyring, unless the caller accepts the loss.
func (s *Svc) checkSecrets(info StoreInfo, o RestoreOptions) (warnings []string, err error) {
	have := map[string]bool{}
	for _, id := range s.o.Keyring.KeyIDs() {
		have[id] = true
	}
	var missing []string
	for _, id := range info.SecretKeyIDs {
		if !have[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	sort.Strings(missing)
	if !o.AcceptUnreadableSecrets {
		return nil, fmt.Errorf("%w: key %s", ErrUnreadableSecrets, strings.Join(missing, ", "))
	}
	return []string{"secrets sealed under keys this install does not hold (" + strings.Join(missing, ", ") + ") are unreadable and must be re-entered"}, nil
}

// configTargets places the archive's config file and boot record when the
// caller asked (ReplaceConfig), the archive holds the files themselves and the
// rename pre-check passes; otherwise it copies them to
// <state_dir>/restore/<id>/config.restored/ for the operator to merge.
func (s *Svc) configTargets(staging, rid string, m *Manifest, o RestoreOptions) ([]restorejournal.Target, []string, error) {
	if m == nil || s.o.ConfigPath == "" {
		return nil, nil, nil
	}
	cfgDir := filepath.Join(staging, "config")
	ents, err := os.ReadDir(cfgDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	live := map[string]string{filepath.Base(s.o.ConfigPath): s.o.ConfigPath}
	if s.o.BootRecordPath != "" {
		live[filepath.Base(s.o.BootRecordPath)] = s.o.BootRecordPath
	}
	var moves []restorejournal.Target
	for _, e := range ents {
		if to, ok := live[e.Name()]; ok && e.Type().IsRegular() {
			moves = append(moves, restorejournal.Target{From: filepath.Join(cfgDir, e.Name()), To: to})
		}
	}
	var warnings []string
	if m.ConfigRedacted {
		warnings = append(warnings, "the archive holds a redacted copy of the config file (it carried secrets); the live file is kept and the copy is staged for you to merge")
	}
	if o.ReplaceConfig && !m.ConfigRedacted {
		if err := restorejournal.Validate(s.probe(), moves); err == nil {
			return moves, warnings, nil
		} else {
			warnings = append(warnings, "the config file or boot record cannot be swapped by rename here ("+err.Error()+"); they are staged for you to merge")
		}
	}
	dst := filepath.Join(restorejournal.JournalDir(s.o.StateDir, rid), "config.restored")
	if err := mkdirPrivate(dst); err != nil {
		return nil, nil, err
	}
	for _, mv := range moves {
		b, err := os.ReadFile(mv.From)
		if err != nil {
			return nil, nil, err
		}
		if err := writeFileAtomic(filepath.Join(dst, filepath.Base(mv.From)), b); err != nil {
			return nil, nil, err
		}
	}
	warnings = append(warnings, "the archive's config files are in "+dst)
	return nil, warnings, nil
}

// Pending reports the staged restore, nil when the slot holds none.
func (s *Svc) Pending(ctx context.Context) (*PendingRestore, error) {
	pending, taken, _, err := s.slot()
	if err != nil {
		return nil, err
	}
	if !taken {
		return nil, nil
	}
	if pending == nil {
		return &PendingRestore{Warnings: []string{"a restore is in flight or its intent does not decode"}}, nil
	}
	var w []string
	if n, ok := s.readStageNote(pending.ID); ok {
		w = n.Warnings
	}
	pr := pendingOf(*pending, w)
	return &pr, nil
}

// CancelRestore removes a staged restore that has not started: its intent and
// its staging directory. It refuses while a swap is in flight, which only a
// boot finishes.
func (s *Svc) CancelRestore(ctx context.Context) error {
	release, err := s.acquire()
	if err != nil {
		return err
	}
	defer release()
	pending, taken, inflight, err := s.slot()
	if err != nil {
		return err
	}
	if len(inflight) > 0 {
		return fmt.Errorf("%w: restore %s is in flight; boot this build once to finish it", restorejournal.ErrSwapPending, inflight[0])
	}
	if !taken {
		return nil
	}
	if err := os.Remove(restorejournal.PendingPath(s.o.StateDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := syncDir(restorejournal.Dir(s.o.StateDir)); err != nil {
		return err
	}
	if pending != nil && restorejournal.ValidID(pending.ID) {
		_ = os.RemoveAll(filepath.Dir(restorejournal.StagingDir(s.o.DBPath, pending.ID)))
	}
	return nil
}

// RevertLastRestore stages the undo point of the newest applied restore: its
// previous/ directory placed back as the live store, through the same swap,
// with kind revert (§5B.3.4 "Undo").
func (s *Svc) RevertLastRestore(ctx context.Context, o RestoreOptions) (PendingRestore, error) {
	release, err := s.acquire()
	if err != nil {
		return PendingRestore{}, err
	}
	defer release()
	if _, taken, _, err := s.slot(); err != nil {
		return PendingRestore{}, err
	} else if taken {
		return PendingRestore{}, restorejournal.ErrSwapPending
	}
	prevID, prevDB, err := s.undoPoint()
	if err != nil {
		return PendingRestore{}, err
	}
	now := s.o.now().UTC()
	rid := newRestoreID(now)
	targets := []restorejournal.Target{{From: prevDB, To: s.o.DBPath}}
	var warnings []string
	if o.ReplaceConfig {
		for _, p := range []string{s.o.ConfigPath, s.o.BootRecordPath} {
			if p == "" {
				continue
			}
			from := restorejournal.SetAsideDest(s.o.StateDir, s.o.DBPath, prevID, p)
			if _, err := os.Lstat(from); err == nil {
				targets = append(targets, restorejournal.Target{From: from, To: p})
			}
		}
	}
	ttl := s.o.settings().IntentTTL
	if ttl <= 0 {
		ttl = intentTTLDefault
	}
	install := ""
	if s.o.Store != nil {
		install = s.o.Store.InstallID()
	}
	in := restorejournal.Intent{
		ID: rid, Kind: restorejournal.KindRevert, DBPath: s.o.DBPath, Targets: targets,
		StagedByBuild: s.o.BuildID, ExpiresAt: now.Add(ttl), RequestedBy: s.actor(ctx), RequestedAt: now,
		InstallID: install,
	}
	if s.o.Audit != nil {
		if err := s.o.Audit(ctx, "core.restore.stage", map[string]string{"kind": "revert", "undo": prevID}); err != nil {
			return PendingRestore{}, fmt.Errorf("backup: audit: %w", err)
		}
	}
	if s.o.Stager == nil {
		return PendingRestore{}, errors.New("backup: restore needs a Stager")
	}
	if err := s.o.Stager.Stage(ctx, in); err != nil {
		_ = os.RemoveAll(restorejournal.JournalDir(s.o.StateDir, rid))
		return PendingRestore{}, err
	}
	s.o.Metrics.Restore(ctx, "staged", o.Foreign)
	pr := pendingOf(in, warnings)
	if o.Restart && s.o.Restart != nil {
		if err := s.o.Restart(ctx); err != nil {
			pr.Warnings = append(pr.Warnings, "the restart request failed: "+err.Error())
		}
	}
	return pr, nil
}

// undoPoint is the newest applied restore that still has its set-aside store.
func (s *Svc) undoPoint() (id, db string, err error) {
	ents, err := os.ReadDir(restorejournal.Dir(s.o.StateDir))
	if err != nil {
		return "", "", ErrNoUndoPoint
	}
	for _, e := range ents {
		if !e.IsDir() || !restorejournal.ValidID(e.Name()) {
			continue
		}
		switch lastJournalStep(filepath.Join(restorejournal.JournalDir(s.o.StateDir, e.Name()), restorejournal.JournalName)) {
		case "applied", "applied_offline":
		default:
			continue
		}
		cand := restorejournal.SetAsideDest(s.o.StateDir, s.o.DBPath, e.Name(), s.o.DBPath)
		if fi, err := os.Lstat(cand); err == nil && fi.IsDir() && e.Name() > id {
			id, db = e.Name(), cand
		}
	}
	if id == "" {
		return "", "", ErrNoUndoPoint
	}
	return id, db, nil
}

// lastJournalStep is the step of the swap journal's last record ("" when the
// journal is unreadable).
func lastJournalStep(p string) string {
	b, err := readBounded(p, 16<<20)
	if err != nil {
		return ""
	}
	lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
	var r restorejournal.Record
	if len(lines) == 0 || json.Unmarshal(lines[len(lines)-1], &r) != nil {
		return ""
	}
	return r.Step
}

// Recheck is step b's re-check of a staged archive restore (decision D4), for
// restoreswap.Options.Recheck: the manifest in staging must hash to the
// intent's manifest_sha256, and every listed file must exist with its recorded
// size (and, when rehash reports true, its digest: core.backup.restore.rehash_on_boot).
// A revert has no archive: only its sources are checked. The kernel composes
// it with the swapper's default (the placements' rename pre-check) at F8-04.
func Recheck(rehash func() bool) func(ctx context.Context, in restorejournal.Intent) error {
	return func(ctx context.Context, in restorejournal.Intent) error {
		for _, t := range in.Targets {
			if _, err := os.Lstat(t.From); err != nil {
				return fmt.Errorf("the staged item %s is gone", t.From)
			}
		}
		if in.Kind != restorejournal.KindArchive {
			return nil
		}
		if in.Staging == "" || in.ManifestSHA256 == "" {
			return errors.New("the intent names no staging directory or manifest hash")
		}
		var (
			raw     []byte
			entries []Entry
			err     error
		)
		if raw, err = readBounded(filepath.Join(in.Staging, ManifestName), MaxManifestBytes); err == nil {
			if SumManifest(raw) != in.ManifestSHA256 {
				return errors.New("the staged manifest does not match the intent")
			}
			m, perr := ParseManifest(raw)
			if perr != nil {
				return perr
			}
			entries = m.Entries
		} else if raw, lerr := readBounded(filepath.Join(in.Staging, legacyManifestName), MaxManifestBytes); lerr == nil {
			if SumManifest(raw) != in.ManifestSHA256 {
				return errors.New("the staged manifest does not match the intent")
			}
			var ls legacyStaged
			if json.Unmarshal(raw, &ls) != nil {
				return errors.New("the staged legacy manifest does not decode")
			}
			entries = ls.Entries
		} else {
			return fmt.Errorf("no staged manifest: %w", err)
		}
		full := rehash != nil && rehash()
		for _, e := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			p := filepath.Join(in.Staging, filepath.FromSlash(e.Path))
			fi, err := os.Lstat(p)
			if err != nil || !fi.Mode().IsRegular() || fi.Size() != e.Size {
				return fmt.Errorf("the staged file %s is missing or its size changed", e.Path)
			}
			if full && e.SHA256 != "" {
				f, _, err := backuparchive.OpenRegular(p, 0)
				if err != nil {
					return err
				}
				herr := hashEntry(ctxReader{ctx, f}, e)
				_ = f.Close()
				if herr != nil {
					return herr
				}
			}
		}
		return nil
	}
}
