// file: core/backup/catalogue.go
// version: 1.0.6
// guid: 0e5a7d12-41c8-4b6f-8d33-9c1ab2e07f65
// last-edited: 2026-11-12

package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/internal/backuparchive"
)

// found is one archive of the backup directory and what is known of it without
// hashing it.
type found struct {
	Archive
	Path  string
	Bytes int64 // the archive and its sidecars, for the byte budget
}

const maxSidecarBytes = 64 << 10

// stalePartialAge is how long a .partial or .tmp file of the backup directory
// must go unwritten before the catalogue scan removes it as a crashed run's
// leftover. Every run that owns one keeps its time moving while it writes (an
// archive stream, an upload, a sidecar), and Import does not hold the backup
// resource, so a younger file is never touched.
const stalePartialAge = 6 * time.Hour

// archiveExt matches what follows the catalogue prefix of an archive's name. A
// function, not a variable: core forbids package-level mutable state (03 R5).
// A caller that matches in a loop calls it once, before the loop.
func archiveExt() *regexp.Regexp {
	return regexp.MustCompile(`^\d{8}_\d{6}(\.tar|\.tar\.gz|\.tar\.zst)$`)
}

// stampOf parses a name's timestamp.
func stampOf(rest string) (time.Time, bool) {
	if len(rest) < 15 {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("20060102_150405", rest[:15], time.UTC)
	return t, err == nil
}

// kindEntry caches the first-entry peek of a file without a manifest sidecar,
// keyed by the file's size and time so a replaced file is peeked again.
type kindEntry struct {
	size int64
	mod  time.Time
	core bool
	m    *Manifest
}

// scan reconciles and reads the backup directory (§5B.3.5 "Catalogue"): every
// archive with the app's prefix, its provenance class from its manifest, its
// last verification; orphaned sidecars removed. It never hashes an archive.
// Newest first.
func (s *Svc) scan(ctx context.Context) ([]found, error) {
	ents, err := os.ReadDir(s.o.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("backup: read %s: %w", s.o.Dir, err)
	}
	ext := archiveExt()
	have := make(map[string]bool, len(ents))
	for _, e := range ents {
		have[e.Name()] = true
	}
	var (
		out []found
		now time.Time // read once, and only when there is a temporary file to judge
	)
	for _, e := range ents {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := e.Name()
		rest, ok := strings.CutPrefix(name, s.o.Prefix)
		if !ok {
			continue
		}
		if ext.MatchString(rest) {
			if !e.Type().IsRegular() {
				continue // a link or directory named like an archive is never listed
			}
			f, ok := s.describe(name, rest)
			if ok {
				out = append(out, f)
			}
			continue
		}
		if e.Type().IsRegular() && (strings.HasSuffix(name, ".partial") || strings.HasSuffix(name, ".tmp")) {
			if fi, err := e.Info(); err == nil {
				if now.IsZero() {
					now = s.o.now()
				}
				if now.Sub(fi.ModTime()) > stalePartialAge {
					s.o.log().Warn("backup: removing a stale temporary file", "name", name)
					if err := os.Remove(filepath.Join(s.o.Dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
						s.o.log().Warn("backup: could not remove a stale temporary file", "name", name, "error", err)
					}
				}
			}
			continue
		}
		for _, suf := range []string{ChecksumSidecar, ManifestSidecar, VerifySidecar} {
			if base, cut := strings.CutSuffix(name, suf); cut && ext.MatchString(strings.TrimPrefix(base, s.o.Prefix)) && !have[base] {
				s.o.log().Warn("backup: removing an orphaned sidecar", "name", name)
				_ = os.Remove(filepath.Join(s.o.Dir, name))
			}
		}
	}
	out = s.dropDuplicateIDs(out)
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	s.recordCatalogue(ctx, out)
	return out, nil
}

// dropDuplicateIDs keeps one file per archive ID. Two files can carry one ID
// (an archive copied into the directory by hand, or a legacy .tar and .tar.gz of
// one stem), and every operation addresses an archive by its ID: listing both
// would let find, Delete and Verify act on whichever came first. The file whose
// name sorts first is kept, so the answer is the same on every scan, and each of
// the others is named once in the log. Import refuses an ID already in the
// catalogue (checked again under importMu just before it publishes, so two
// imports of one archive cannot both be stored), so only a file placed outside
// the service gets here.
func (s *Svc) dropDuplicateIDs(in []found) []found {
	sort.Slice(in, func(i, j int) bool { return in[i].Name < in[j].Name })
	kept := make(map[ArchiveID]string, len(in))
	out := in[:0]
	for _, f := range in {
		if first, dup := kept[f.ID]; dup {
			if _, seen := s.warned.LoadOrStore(f.Name, struct{}{}); !seen {
				s.o.log().Warn("backup: an archive has the ID of another file and is not listed", "name", f.Name, "kept", first)
			}
			continue
		}
		kept[f.ID] = f.Name
		out = append(out, f)
	}
	return out
}

func (s *Svc) recordCatalogue(ctx context.Context, all []found) {
	by := map[[2]string]int64{}
	var bytes int64
	for _, f := range all {
		by[[2]string{f.Format, f.Provenance.String()}]++
		bytes += f.Bytes
	}
	s.o.Metrics.Catalogue(ctx, by, bytes)
}

// describe classifies one archive file. ok is false only for a file that
// vanished.
func (s *Svc) describe(name, rest string) (found, bool) {
	p := filepath.Join(s.o.Dir, name)
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return found{}, false
	}
	f := found{Path: p, Bytes: fi.Size()}
	f.Name, f.Size, f.CreatedAt = name, fi.Size(), fi.ModTime().UTC()
	if t, ok := stampOf(rest); ok {
		f.CreatedAt = t
	}
	for _, suf := range []string{ChecksumSidecar, ManifestSidecar, VerifySidecar} {
		if si, err := os.Lstat(p + suf); err == nil && si.Mode().IsRegular() {
			f.Bytes += si.Size()
		}
	}
	// The archive's own first entry is the source of truth: a sidecar can be
	// swapped, replaced or stale, and the catalogue must never list one archive
	// under another's ID or provenance. Verify compares the sidecar with it.
	m := s.peekManifest(p, fi)
	live := ""
	if s.o.Store != nil {
		live = s.o.Store.InstallID()
	}
	if m != nil {
		f.ID, f.Format, f.Manifest, f.CreatedAt = m.ArchiveID, FormatMediacore, m, m.CreatedAt
		f.Provenance = Classify(m, s.o.Keyring, live)
	} else {
		f.ID = legacyIDOf(name)
		f.Format, f.Provenance = FormatLegacyABO, PreProvenance
	}
	f.Verified = readVerify(p + VerifySidecar)
	return f, true
}

func readBounded(p string, max int64) (b []byte, err error) {
	f, fi, err := backuparchive.OpenRegular(p, max)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	b = make([]byte, fi.Size())
	_, err = io.ReadFull(f, b)
	return b, err
}

// peekManifest reads the first entry of an archive: when it is the manifest
// the archive is mediacore-backup/1, else a legacy-abo/0 one (or unreadable,
// which is listed as legacy and fails verification). The result is cached by
// the file's size and time, so a listing reads each archive's head once.
func (s *Svc) peekManifest(p string, fi os.FileInfo) *Manifest {
	if v, ok := s.cache.Load(p); ok {
		if k := v.(kindEntry); k.size == fi.Size() && k.mod.Equal(fi.ModTime()) {
			return k.m
		}
	}
	var m *Manifest
	if a, err := openArchive(p, s.limits()); err == nil {
		m = a.Manifest
		if cerr := a.Close(); cerr != nil {
			s.o.log().Warn("backup: closing an archive after reading its manifest", "name", filepath.Base(p), "error", cerr)
		}
	}
	s.cache.Store(p, kindEntry{size: fi.Size(), mod: fi.ModTime(), core: m != nil, m: m})
	return m
}

func (s *Svc) limits() backuparchive.Limits { return s.o.Limits }

type verifyFile struct {
	At     time.Time `json:"at"`
	Level  string    `json:"level"`
	OK     bool      `json:"ok"`
	Detail string    `json:"detail"`
	Reason string    `json:"reason,omitempty"`
}

func readVerify(p string) *VerifyResult {
	b, err := readBounded(p, maxSidecarBytes)
	if err != nil {
		return nil
	}
	var v verifyFile
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	l := VerifyInvalid
	for _, c := range []VerifyLevel{VerifyChecksum, VerifyEntries, VerifyOpen} {
		if c.String() == v.Level {
			l = c
		}
	}
	// The sidecar sits in a directory an operator or a network share may write
	// to, so its reason is read as untrusted: only a code this build knows
	// survives, and a sidecar from before reason codes (or one with a made-up
	// code) reads as the generic failure.
	return &VerifyResult{At: v.At, Level: l, OK: v.OK, Detail: v.Detail, Reason: VerifyReason(v.Reason).Normalize(v.OK)}
}

func writeVerify(p string, r VerifyResult) error {
	b, err := json.Marshal(verifyFile{At: r.At.UTC(), Level: r.Level.String(), OK: r.OK, Detail: r.Detail, Reason: string(r.Reason)})
	if err != nil {
		return err
	}
	return writeFileAtomic(p, b)
}

// List is the catalogue, newest first.
func (s *Svc) List(ctx context.Context) ([]Archive, error) {
	all, err := s.scan(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Archive, len(all))
	for i, f := range all {
		out[i] = f.Archive
	}
	return out, nil
}

// find is the archive of id.
func (s *Svc) find(ctx context.Context, id ArchiveID) (found, error) {
	if !ValidArchiveID(string(id)) {
		return found{}, ErrNotFound
	}
	all, err := s.scan(ctx)
	if err != nil {
		return found{}, err
	}
	for _, f := range all {
		if f.ID == id {
			return f, nil
		}
	}
	return found{}, ErrNotFound
}

// Download opens the archive as stored. The file is the archive's own bytes:
// it is credential-equivalent (B1), which is why the route that serves it is
// gated like a restore.
func (s *Svc) Download(ctx context.Context, id ArchiveID) (io.ReadCloser, error) {
	f, err := s.find(ctx, id)
	if err != nil {
		return nil, err
	}
	file, _, err := backuparchive.OpenRegular(f.Path, 0)
	return file, err
}

// Delete removes an archive and its sidecars, refusing one a retention floor
// protects (the newest verified archive, one a pending or undo-able restore
// names).
func (s *Svc) Delete(ctx context.Context, id ArchiveID) error {
	release, err := s.acquire()
	if err != nil {
		return err
	}
	defer release()
	all, err := s.scan(ctx)
	if err != nil {
		return err
	}
	for _, f := range all {
		if f.ID != id {
			continue
		}
		if why, prot := s.floors(all)[f.ID]; prot {
			return fmt.Errorf("%w: %s", ErrProtectedByFloor, why)
		}
		removeArchive(f.Path)
		s.cache.Delete(f.Path)
		return syncDir(s.o.Dir)
	}
	return ErrNotFound
}

// Import stores an uploaded archive in the backup directory. The upload is
// bounded by the archive size limit, written to a .partial created O_EXCL
// 0600, and accepted only when it is a well-formed archive: a
// mediacore-backup/1 archive must pass VerifyEntries and a legacy-abo/0
// archive must be one store directory. Its provenance then decides Own or
// Foreign, exactly as for an archive found in the directory.
//
// The name is the caller's suggestion and is ignored (it is in the signature
// because the Catalogue's is): the archive is stored under a fresh name of this
// app's pattern, <prefix><timestamp><codec extension>. A name that arrived with
// a request is text a stranger chose, and keeping it would make it a path under
// the backup directory and a string in every log line that names the file.
func (s *Svc) Import(ctx context.Context, _ string, r io.Reader) (_ Archive, err error) {
	if err := mkdirPrivate(s.o.Dir); err != nil {
		return Archive{}, err
	}
	lim := s.limits()
	if lim.MaxArchiveBytes <= 0 {
		lim.MaxArchiveBytes = backuparchive.DefaultMaxArchiveBytes
	}
	// The upload's length is not known until it ends, and it is bounded only by
	// MaxArchiveBytes: ask for room for its first interval before a file is made,
	// and for each next one as it is written (spaceWriter), so an upload stops
	// near the margin instead of at a full volume (near: see uploadCheckBytes).
	claim := func(n uint64) error {
		return requireRoom(s.o.Disk, s.o.settings().margin(), s.o.Dir, n)
	}
	if err := claim(uploadCheckBytes); err != nil {
		return Archive{}, err
	}
	f, tmp, err := createTemp(filepath.Join(s.o.Dir, s.o.Prefix+"import-upload"))
	if err != nil {
		return Archive{}, err
	}
	done, fClosed := false, false
	defer func() {
		if done {
			return
		}
		if !fClosed {
			err = errors.Join(err, f.Close())
		}
		if rerr := os.Remove(tmp); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			err = errors.Join(err, rerr)
		}
	}()
	h, n, err := copySum(ctxReader{ctx, io.LimitReader(r, lim.MaxArchiveBytes+1)}, newSpaceWriter(f, uploadCheckBytes, claim))
	if err != nil {
		return Archive{}, err
	}
	if n > lim.MaxArchiveBytes {
		return Archive{}, fmt.Errorf("%w: the upload is over %d bytes", backuparchive.ErrLimit, lim.MaxArchiveBytes)
	}
	if err := f.Sync(); err != nil {
		return Archive{}, err
	}
	fClosed = true
	if err := f.Close(); err != nil {
		return Archive{}, err
	}
	var ext string
	var man []byte
	var id ArchiveID // a mediacore archive's own ID; "" for a legacy one, which is named by its file
	if a, oerr := openArchive(tmp, lim); oerr == nil {
		man, id = a.ManifestBytes, a.Manifest.ArchiveID
		// The fast refusal: a duplicate is turned away before the upload is read
		// through. It is not the guard, since another import of the same archive
		// can be at this very point and neither has a name yet; install checks
		// again under the lock that publishes.
		existing, serr := s.scan(ctx)
		if serr != nil {
			// The duplicate check needs the catalogue: fail closed.
			return Archive{}, errors.Join(serr, a.Close())
		}
		if idTaken(existing, id) {
			return Archive{}, errors.Join(errDuplicateID(id), a.Close())
		}
		if cerr := errors.Join(a.consume(ctx, nil, ""), a.Close()); cerr != nil {
			return Archive{}, cerr
		}
	} else if errors.Is(oerr, ErrManifest) {
		// A tar that does not open as a mediacore archive may be a legacy one.
		if _, lerr := backuparchive.ScanLegacy(ctx, tmp, lim); lerr != nil {
			return Archive{}, errors.Join(oerr, lerr)
		}
	} else if _, lerr := backuparchive.ScanLegacy(ctx, tmp, lim); lerr != nil {
		return Archive{}, errors.Join(oerr, lerr)
	}
	ext, err = extensionOf(tmp)
	if err != nil {
		return Archive{}, err
	}
	fname, err := s.install(ctx, tmp, ext, h, man, id)
	if err != nil {
		return Archive{}, err // tmp is gone or is removed by the deferred cleanup
	}
	done = true
	if err := syncDir(s.o.Dir); err != nil {
		return Archive{}, err
	}
	rest := strings.TrimPrefix(fname, s.o.Prefix)
	got, ok := s.describe(fname, rest)
	if !ok {
		return Archive{}, ErrNotFound
	}
	return got.Archive, nil
}

// errDuplicateID is the refusal of an import whose archive ID the catalogue
// already lists.
func errDuplicateID(id ArchiveID) error {
	return fmt.Errorf("%w: an archive with ID %s is already in the catalogue", ErrManifest, id)
}

// install is the end of an Import, one import at a time: it checks that the
// archive's ID (id, "" for a legacy archive) is not in the catalogue, gives the
// finished upload its name and writes its sidecars. The check at the top of
// Import is a fast refusal and cannot be the guard: two imports of one archive
// both pass it, because neither has a name yet, and both would then be stored
// under one ID. The catalogue lists one of the two files, and deleting that one
// brings the other back. So the check is made again here, under importMu, and
// the lock is held through publish and the sidecars: a second import's check
// then finds the first's archive whole, or (when its sidecars failed and it was
// removed) not at all.
//
// The lock is not held while the upload is read or verified, which can take
// hours and would queue every import behind one, and it is not the backup
// resource, which Import never takes. Taking it is not cancelled by ctx; what
// waits behind it is a scan, a link and two small files. A legacy archive needs
// no check: its ID is its file's name, and publish never gives one name twice.
func (s *Svc) install(ctx context.Context, tmp, ext, sum string, man []byte, id ArchiveID) (string, error) {
	s.importMu.Lock()
	defer s.importMu.Unlock()
	if id != "" {
		existing, err := s.scan(ctx)
		if err != nil {
			return "", err // fail closed, as at the first check
		}
		if idTaken(existing, id) {
			return "", errDuplicateID(id)
		}
	}
	fname, final, err := s.publish(tmp, s.o.now().UTC(), ext)
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(final+ChecksumSidecar, []byte(sum+"  "+fname+"\n")); err != nil {
		removeArchive(final)
		return "", err
	}
	if man != nil {
		if err := writeFileAtomic(final+ManifestSidecar, man); err != nil {
			removeArchive(final)
			return "", err
		}
	}
	return fname, nil
}

// extensionOf is the codec extension of the archive at p, from its magic bytes.
func extensionOf(p string) (ext string, err error) {
	f, _, err := backuparchive.OpenRegular(p, 0)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	var magic [8]byte
	n, rerr := io.ReadFull(f, magic[:])
	if rerr != nil && !errors.Is(rerr, io.EOF) && !errors.Is(rerr, io.ErrUnexpectedEOF) {
		return "", rerr // a short file is not a tar; a failing read is not "not a tar"
	}
	c := backuparchive.Sniff(magic[:n])
	if c == nil {
		return "", fmt.Errorf("%w: not a tar archive", ErrManifest)
	}
	return c.Extension(), nil
}

func idTaken(all []found, id ArchiveID) bool {
	for _, f := range all {
		if f.ID == id {
			return true
		}
	}
	return false
}
