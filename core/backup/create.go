// file: core/backup/create.go
// version: 1.0.3
// guid: 6e7c41d8-9a30-4d1b-a2c5-17b0f3d9e6a4
// last-edited: 2026-11-12

package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/internal/backuparchive"
)

const stagingDirName = ".backup-staging"

// Create is §5B.3.5's creation, in the order the spec fixes: codec before any
// work, retention before the space check, the journal boundaries before the
// checkpoint and the new content marker right after it, the archive streamed
// to a .partial and renamed, the sidecars, then the optional verification.
func (s *Svc) Create(ctx context.Context, o CreateOptions) (a Archive, err error) {
	reason := o.Reason
	if reason == "" {
		reason = "manual"
	}
	// The reason is signed into the manifest and read back by ParseManifest with
	// a bound; a reason the read side would refuse is refused here, before the
	// resource is taken and before any file is made, so no archive is written
	// that its own catalogue cannot list. A refusal is a caller's mistake, not a
	// run, and records no run (a configuration error found later, in create,
	// does).
	if err := ValidReason(reason); err != nil {
		return Archive{}, err
	}
	release, err := s.acquire()
	if err != nil {
		return Archive{}, err
	}
	defer release()
	defer func() {
		out := "ok"
		switch {
		case errors.Is(err, context.Canceled):
			out = "canceled"
		case err != nil && a.ID == "":
			out = "error"
		case err != nil:
			out = "unverified"
		}
		s.o.Metrics.Run(context.WithoutCancel(ctx), runReasonLabel(reason), out)
	}()
	return s.create(ctx, o, reason)
}

// runReasonLabel is the reason label of the runs counter: one of a fixed set,
// never the reason itself. A reason is free text the caller chose (a task's
// parameter, a module's "pre:" tag, a route's body), and a label value that
// follows it would grow the series without bound. The reason itself is recorded
// in the manifest.
func runReasonLabel(reason string) string {
	switch reason {
	case "schedule", "manual":
		return reason
	}
	return "other"
}

func (s *Svc) create(ctx context.Context, co CreateOptions, reason string) (Archive, error) {
	set := s.o.settings()
	// 1. Configuration errors before any work.
	codec, err := backuparchive.Resolve(set.Compression)
	if err != nil {
		return Archive{}, err
	}
	if err := backuparchive.ValidateLevel(codec, set.CompressionLevel); err != nil {
		return Archive{}, err
	}
	if s.o.Store == nil || s.o.Inspector == nil {
		return Archive{}, errors.New("backup: create needs a Store and an Inspector")
	}
	if err := mkdirPrivate(s.o.Dir); err != nil {
		return Archive{}, err
	}
	// 2. Retention before the space check: a full disk is the case it exists for.
	if err := s.prune(ctx, set); err != nil {
		return Archive{}, err
	}
	// 3. Space check, then the same-device rule.
	est, err := dirSize(s.o.DBPath)
	if err != nil {
		return Archive{}, fmt.Errorf("backup: size of %s: %w", s.o.DBPath, err)
	}
	margin := set.margin()
	avail, err := s.o.Disk.avail(s.o.Dir)
	if err != nil {
		return Archive{}, fmt.Errorf("backup: free space of %s: %w", s.o.Dir, err)
	}
	if avail < uint64(est)+margin {
		return Archive{}, fmt.Errorf("%w: %d bytes free, %d needed", ErrInsufficientSpace, avail, uint64(est)+margin)
	}
	now := s.o.now().UTC().Round(0)
	id, err := NewArchiveID(now)
	if err != nil {
		return Archive{}, err
	}
	stagingRoot := filepath.Join(filepath.Dir(s.o.DBPath), stagingDirName)
	if err := mkdirPrivate(stagingRoot); err != nil {
		return Archive{}, err
	}
	if err := s.sameDevice(stagingRoot); err != nil {
		return Archive{}, err
	}
	s.sweepStaging(stagingRoot)
	run, err := newRunDir(filepath.Join(stagingRoot, string(id)))
	if err != nil {
		return Archive{}, err
	}
	defer func() {
		// The archive is published (or the failure already reported) by now; a
		// leftover run directory is swept by the next run's sweepStaging.
		if cerr := errors.Join(run.Close(), os.RemoveAll(run.path)); cerr != nil {
			s.o.log().Warn("backup: removing the run directory", "run", filepath.Base(run.path), "error", cerr)
		}
	}()

	// 4. Boundaries first, the checkpoint, then the new content marker.
	b, err := s.o.Markers.Read(ctx)
	if err != nil {
		return Archive{}, fmt.Errorf("backup: read journal boundaries: %w", err)
	}
	endPhase := s.phase(ctx, "checkpoint")
	if err := s.o.Store.Checkpoint(ctx, filepath.Join(run.path, "db")); err != nil {
		return Archive{}, fmt.Errorf("backup: checkpoint: %w", err)
	}
	if _, err := s.o.Markers.Advance(ctx); err != nil {
		return Archive{}, fmt.Errorf("backup: advance the content marker: %w", err)
	}
	info, err := s.o.Inspector.Inspect(ctx, filepath.Join(run.path, "db"))
	if err != nil {
		return Archive{}, fmt.Errorf("backup: read the checkpoint: %w", err)
	}

	// 5. Capture, hash, manifest, provenance. The capture belongs to the
	// checkpoint phase: every input is read from the one consistent point.
	caps, redacted, err := s.capture(ctx, run, co, set)
	if err != nil {
		return Archive{}, err
	}
	endPhase()
	endPhase = s.phase(ctx, "checksum")
	entries, err := run.hashTree(ctx, set.HashWorkers)
	if err != nil {
		return Archive{}, err
	}
	for i := range entries {
		entries[i].Class = classOf(caps, entries[i].Path)
		if entries[i].Class == Secret || entries[i].Class == ClassInvalid {
			return Archive{}, fmt.Errorf("%w: %s", ErrUnencryptedSecrets, entries[i].Path)
		}
	}
	m := &Manifest{
		Format: FormatMediacore, ArchiveID: id, App: s.o.AppID, AppVersion: s.o.AppVersion,
		CoreVersion: s.o.CoreVersion, InstallID: s.o.Store.InstallID(), CreatedAt: now, Reason: reason,
		JournalSeq: b.JournalSeq, AuditHighWater: b.AuditHighWater, ServedBoot: b.ServedBoot,
		Levels: info.Levels, LegacyStorageFormat: info.LegacyFormat, PebbleFormat: info.PebbleFormat,
		Keyspaces: keyspaces(info), Modules: s.o.Modules, Entries: entries, ConfigRedacted: redacted,
	}
	if m.Derived, err = s.derived(ctx, caps, info); err != nil {
		return Archive{}, err
	}
	if err := Sign(m, s.o.Keyring); err != nil {
		return Archive{}, fmt.Errorf("backup: sign the manifest: %w", err)
	}
	mb, err := MarshalManifest(m)
	if err != nil {
		return Archive{}, err
	}
	if err := checkReadsBack(mb); err != nil {
		return Archive{}, err
	}
	endPhase()

	// 5b/6. Stream, publish under a free name, sidecars.
	endPhase = s.phase(ctx, "archive")
	partial := s.partialPath("create", string(id))
	sum, size, err := writeArchive(ctx, archiveSpec{
		Partial: partial, Codec: codec, Level: set.CompressionLevel,
		Manifest: mb, Created: now, Entries: entries, RunDir: run.path,
	})
	if err != nil {
		return Archive{}, err
	}
	name, final, err := s.publish(partial, now, codec.Extension())
	if err != nil {
		_ = os.Remove(partial)
		return Archive{}, err
	}
	if err := s.writeSidecars(final, name, sum, mb); err != nil {
		removeArchive(final)
		return Archive{}, err
	}
	endPhase()
	a := Archive{
		ID: id, Name: name, Size: size, CreatedAt: now, Format: FormatMediacore, Provenance: Own, Manifest: m,
	}
	s.o.Metrics.Created(ctx, size, now)

	// 7. Verification after create.
	if set.VerifyAfterCreate {
		endVerify := s.phase(ctx, "verify")
		res, verr := s.verifyArchive(ctx, final, a.ID, VerifyOpen)
		endVerify()
		a.Verified = &res
		if verr != nil {
			return a, fmt.Errorf("backup: the new archive %s failed verification: %w", a.ID, verr)
		}
	}
	return a, nil
}

// sameDevice refuses a staging root that is not on the database's filesystem:
// the checkpoint hard-links its files, so across devices it would silently copy
// gigabytes (§5B.5).
func (s *Svc) sameDevice(stagingRoot string) error {
	db, err := s.o.Disk.device(s.o.DBPath)
	if err != nil {
		return fmt.Errorf("backup: device of %s: %w", s.o.DBPath, err)
	}
	st, err := s.o.Disk.device(stagingRoot)
	if err != nil {
		return fmt.Errorf("backup: device of %s: %w", stagingRoot, err)
	}
	if db != st {
		return fmt.Errorf("%w: the database is on device %d and %s on device %d", ErrCrossDevice, db, stagingRoot, st)
	}
	return nil
}

// sweepStaging removes what an earlier crashed run left in the staging root.
// The caller holds the backup resource, so nothing in it is live.
func (s *Svc) sweepStaging(root string) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range ents {
		s.o.log().Warn("backup: removing a stale staging directory", "name", e.Name())
		_ = os.RemoveAll(filepath.Join(root, e.Name()))
	}
}

// checkReadsBack is the write side's copy of the read side's test: the manifest
// bytes about to be signed into an archive must parse as ParseManifest parses
// them. Every string of a manifest that does not come from the caller of Create
// (the install ID, the journal's nonce, the inspector's level and keyspace
// names, the options' module names, the keyring's key ID) and the manifest's
// total size are bounded on the way in by ParseManifest and nowhere on the way
// out; without this an archive could be written, reported as made and listed
// by nothing. ErrManifest, because that is what the manifest is.
func checkReadsBack(manifest []byte) error {
	if _, err := ParseManifest(manifest); err != nil {
		return fmt.Errorf("backup: the manifest this build wrote does not read back: %w", err)
	}
	return nil
}

// partialPath is where an archive of this run is streamed before it has a name:
// <prefix><kind>-<id>.partial in the backup directory. The ID is the run's own
// and unique, so the name needs no check, and it is not an archive's name, so
// nothing listed can be mistaken for it or collide with it.
func (s *Svc) partialPath(kind, id string) string {
	return filepath.Join(s.o.Dir, s.o.Prefix+kind+"-"+id+".partial")
}

// errNoFreeName is publish running out of candidate names.
var errNoFreeName = errors.New("backup: no free archive name")

// publish gives the finished file src the name of an archive: <prefix>
// <YYYYMMDD_HHMMSS><ext>, the first such name at or after at (a second apart)
// that nothing holds, and returns it. src is gone on success, and the archive
// is whole at its name from the first moment it has one.
//
// It never replaces a file. The name is taken by hard-linking src to it, which
// fails with fs.ErrExist if anything, a finished archive included, is there
// already, and that is the one test: a lookup first and a rename after leaves a
// window in which two writers (two imports in the same second, an import and a
// create) both find the name free, and the second rename replaces an archive
// that was already reported made. A name whose sidecar is present while its
// archive is not (a crashed run's, or another writer between its two steps) is
// skipped too, since the sidecars written next would overwrite those.
func (s *Svc) publish(src string, at time.Time, ext string) (name, full string, err error) {
	for i := range 3600 {
		t := at.Add(time.Duration(i) * time.Second)
		name = s.o.Prefix + t.Format("20060102_150405") + ext
		full = filepath.Join(s.o.Dir, name)
		if sidecarHeld(full) {
			continue
		}
		switch err := s.o.Disk.publish(src, full); {
		case err == nil:
			if rerr := os.Remove(src); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
				// The archive is published; the leftover is a second name for the
				// same file, swept as a stale temporary file later.
				s.o.log().Warn("backup: removing the temporary name of a published archive", "name", filepath.Base(src), "error", rerr)
			}
			return name, full, nil
		case errors.Is(err, fs.ErrExist):
			continue
		default:
			return "", "", err
		}
	}
	return "", "", errNoFreeName
}

// sidecarHeld reports whether any sidecar of the archive at full exists.
func sidecarHeld(full string) bool {
	for _, suf := range []string{ChecksumSidecar, ManifestSidecar, VerifySidecar} {
		if _, err := os.Lstat(full + suf); err == nil {
			return true
		}
	}
	return false
}

func (s *Svc) writeSidecars(final, name, sum string, manifest []byte) error {
	if err := writeFileAtomic(final+ChecksumSidecar, []byte(sum+"  "+name+"\n")); err != nil {
		return err
	}
	if err := writeFileAtomic(final+ManifestSidecar, manifest); err != nil {
		return err
	}
	return syncDir(s.o.Dir)
}

// removeArchive deletes an archive and its sidecars.
func removeArchive(final string) {
	for _, suf := range []string{"", ChecksumSidecar, ManifestSidecar, VerifySidecar} {
		_ = os.Remove(final + suf)
	}
}

// captured is one capture root of the run directory and its class.
type captured struct {
	prefix string // archive path prefix, "db/" style
	class  Class
	name   string // the artifact's name, for derived items
}

func classOf(caps []captured, rel string) Class {
	for _, c := range caps {
		if rel == strings.TrimSuffix(c.prefix, "/") || strings.HasPrefix(rel, c.prefix) {
			return c.class
		}
	}
	return ClassInvalid
}

// capture gathers everything but the checkpoint into the run directory:
// the config file (redacted when it holds secrets) and boot record, then each
// registered artifact. A secret-class artifact is skipped (an archive is
// plaintext); a system-of-record artifact that fails aborts the archive; a
// derived or recreatable one that fails is omitted.
func (s *Svc) capture(ctx context.Context, run *runDir, co CreateOptions, set Settings) (caps []captured, redacted bool, err error) {
	caps = append(caps, captured{prefix: "db/", class: SystemOfRecord})
	if s.o.ConfigPath != "" {
		caps = append(caps, captured{prefix: "config/", class: SystemOfRecord})
		base := filepath.Base(s.o.ConfigPath)
		switch {
		case s.o.Config.ConfigFileHasSecrets():
			b, err := s.o.Config.RedactedConfigFile()
			if err != nil {
				return nil, false, fmt.Errorf("backup: redact the config file: %w", err)
			}
			if err := run.putBytes("config/"+base, b); err != nil {
				return nil, false, err
			}
			redacted = true
		default:
			if err := run.copyFile(ctx, s.o.ConfigPath, "config/"+base); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, false, fmt.Errorf("backup: capture the config file: %w", err)
			}
		}
		if s.o.BootRecordPath != "" {
			if err := run.copyFile(ctx, s.o.BootRecordPath, "config/"+filepath.Base(s.o.BootRecordPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, false, fmt.Errorf("backup: capture the boot record: %w", err)
			}
		}
	}
	if s.o.Artifacts == nil {
		return caps, redacted, nil
	}
	for _, a := range s.o.Artifacts() {
		prefix, ok := artifactPrefix(a)
		if !ok {
			return nil, false, fmt.Errorf("backup: artifact %q has an unusable name or owner", a.Name)
		}
		switch a.Class {
		case Secret, Cache:
			s.o.log().Info("backup: artifact not archived", "artifact", a.Name, "class", classString(a.Class))
			continue
		case Derived:
			if !(co.IncludeDerived || set.IncludeDerived) {
				continue
			}
		case Recreatable:
			if !(co.IncludeRecreatable || set.IncludeRecreatable) {
				continue
			}
		case SystemOfRecord:
			if a.Consistency == ConsistencyInvalid {
				return nil, false, fmt.Errorf("backup: system-of-record artifact %q declares no consistency", a.Name)
			}
		default:
			return nil, false, fmt.Errorf("backup: artifact %q has no valid class", a.Name)
		}
		if err := s.captureArtifact(ctx, run, a, prefix); err != nil {
			if a.Class == SystemOfRecord {
				return nil, false, fmt.Errorf("backup: artifact %s: %w", a.Name, err)
			}
			s.o.log().Warn("backup: derived item omitted", "artifact", a.Name, "error", err)
			if rerr := run.root.RemoveAll(strings.TrimSuffix(prefix, "/")); rerr != nil {
				// The omitted item is not in the manifest, so nothing of it is
				// archived; the run directory goes whole when the run ends.
				s.o.log().Warn("backup: removing an omitted derived item's files", "artifact", a.Name, "error", rerr)
			}
			continue
		}
		caps = append(caps, captured{prefix: prefix, class: a.Class, name: a.Name})
	}
	return caps, redacted, nil
}

func (s *Svc) captureArtifact(ctx context.Context, run *runDir, a Artifact, prefix string) error {
	switch {
	case a.Snapshot != nil:
		w := &prefixWriter{run: run, prefix: prefix}
		return a.Snapshot(ctx, w)
	case a.Dir != "":
		if err := run.root.MkdirAll(strings.TrimSuffix(prefix, "/"), 0o700); err != nil {
			return err
		}
		return run.copyTree(ctx, a.Dir, strings.TrimSuffix(prefix, "/"))
	}
	return errors.New("neither a Dir nor a Snapshot hook")
}

type prefixWriter struct {
	run    *runDir
	prefix string
}

func (w *prefixWriter) Create(rel string, mode os.FileMode) (io.WriteCloser, error) {
	return w.run.Create(path.Join(w.prefix, rel), mode)
}

func artifactPrefix(a Artifact) (string, bool) {
	for _, seg := range append(strings.Split(string(a.Owner), "/"), a.Name) {
		if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, `\`) || strings.Contains(seg, "..") {
			return "", false
		}
	}
	switch a.Class {
	case Derived:
		return "derived/" + a.Name + "/", true
	case Recreatable:
		return "aux/" + string(a.Owner) + "/" + a.Name + "/", true
	}
	return "artifacts/" + string(a.Owner) + "/" + a.Name + "/", true
}

func classString(c Class) string {
	switch c {
	case SystemOfRecord:
		return "system_of_record"
	case Secret:
		return "secret"
	case Derived:
		return "derived"
	case Cache:
		return "cache"
	case Recreatable:
		return "recreatable"
	}
	return "invalid"
}

func keyspaces(info StoreInfo) map[string]KSInfo {
	out := make(map[string]KSInfo, len(info.Levels)+len(info.Keys))
	add := func(o string) {
		out[o] = KSInfo{Version: info.Versions[o], Keys: info.Keys[o], Bytes: info.Bytes[o]}
	}
	for o := range info.Versions {
		add(o)
	}
	for o := range info.Keys {
		add(o)
	}
	return out
}

// derived records each captured derived item's checkpoint generation and
// whether its live source moved since (stale: never offered on restore).
func (s *Svc) derived(ctx context.Context, caps []captured, info StoreInfo) (map[string]DerivedInfo, error) {
	var out map[string]DerivedInfo
	for _, c := range caps {
		if c.class != Derived {
			continue
		}
		cp := info.Generations[c.name]
		stale := cp == ""
		if !stale && s.o.DerivedGeneration != nil {
			live, err := s.o.DerivedGeneration(ctx, c.name)
			stale = err != nil || live != cp
		} else if s.o.DerivedGeneration == nil {
			stale = true
		}
		if out == nil {
			out = map[string]DerivedInfo{}
		}
		out[c.name] = DerivedInfo{Generation: cp, Stale: stale}
	}
	return out, nil
}
