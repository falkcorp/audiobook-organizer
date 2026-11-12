// file: core/backup/catalogue.go
// version: 1.0.3
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
	var out []found
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
		for _, suf := range []string{ChecksumSidecar, ManifestSidecar, VerifySidecar, ".tmp"} {
			if base, cut := strings.CutSuffix(name, suf); cut && ext.MatchString(strings.TrimPrefix(strings.TrimSuffix(base, ".tmp"), s.o.Prefix)) && !have[base] {
				s.o.log().Warn("backup: removing an orphaned sidecar", "name", name)
				_ = os.Remove(filepath.Join(s.o.Dir, name))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	s.recordCatalogue(ctx, out)
	return out, nil
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
// Foreign, exactly as for an archive found in the directory. name is a hint:
// it is kept only when it is a plain file name in this app's pattern and free.
func (s *Svc) Import(ctx context.Context, name string, r io.Reader) (_ Archive, err error) {
	if err := mkdirPrivate(s.o.Dir); err != nil {
		return Archive{}, err
	}
	lim := s.limits()
	if lim.MaxArchiveBytes <= 0 {
		lim.MaxArchiveBytes = backuparchive.DefaultMaxArchiveBytes
	}
	tmp := filepath.Join(s.o.Dir, fmt.Sprintf("%simport-%d.partial", s.o.Prefix, s.o.now().UnixNano()))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
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
	h, n, err := copySum(ctxReader{ctx, io.LimitReader(r, lim.MaxArchiveBytes+1)}, f)
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
	if a, oerr := openArchive(tmp, lim); oerr == nil {
		man = a.ManifestBytes
		existing, serr := s.scan(ctx)
		if serr != nil {
			// The duplicate check needs the catalogue: fail closed.
			return Archive{}, errors.Join(serr, a.Close())
		}
		if idTaken(existing, a.Manifest.ArchiveID) {
			return Archive{}, errors.Join(fmt.Errorf("%w: an archive with ID %s is already in the catalogue", ErrManifest, a.Manifest.ArchiveID), a.Close())
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
	final, fname, err := s.importName(name, ext)
	if err != nil {
		return Archive{}, err
	}
	if err := os.Rename(tmp, final); err != nil {
		return Archive{}, err
	}
	done = true
	if err := writeFileAtomic(final+ChecksumSidecar, []byte(h+"  "+fname+"\n")); err != nil {
		removeArchive(final)
		return Archive{}, err
	}
	if man != nil {
		if err := writeFileAtomic(final+ManifestSidecar, man); err != nil {
			removeArchive(final)
			return Archive{}, err
		}
	}
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

// importName picks the stored name: the caller's when it is a plain,
// pattern-conforming and free name, else a fresh timestamped one.
func (s *Svc) importName(hint, ext string) (full, name string, err error) {
	if hint != "" && filepath.Base(hint) == hint && strings.HasPrefix(hint, s.o.Prefix) && archiveExt().MatchString(strings.TrimPrefix(hint, s.o.Prefix)) && strings.HasSuffix(hint, ext) {
		taken := false
		for _, suf := range []string{"", ChecksumSidecar, ManifestSidecar, VerifySidecar} {
			if _, e := os.Lstat(filepath.Join(s.o.Dir, hint+suf)); e == nil {
				taken = true
			}
		}
		if !taken {
			return filepath.Join(s.o.Dir, hint), hint, nil
		}
	}
	name, full, err = s.freshName(s.o.now().UTC(), ext)
	return full, name, err
}

func idTaken(all []found, id ArchiveID) bool {
	for _, f := range all {
		if f.ID == id {
			return true
		}
	}
	return false
}
