// file: core/backup/rewrap.go
// version: 1.0.0
// guid: 1c7e9a64-5b3f-4d28-a0e1-8d4c6f2b9e17
// last-edited: 2026-11-12

package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/falkcorp/mediacore/internal/backuparchive"
)

// Rewrap re-creates an Own archive under the active derivation key: the same
// entries, a new manifest signed by the key that is active now (re-MAC;
// re-encryption is a deferred-build member, §5B.3.9), under a new archive ID so
// the two never share one. The original is deleted only when no floor protects
// it, and the new archive is verified at the entries level first, so it takes
// the newest-verified floor over from the original. An archive that is not Own,
// or is already under the active key, is returned unchanged.
func (s *Svc) Rewrap(ctx context.Context, id ArchiveID) (Archive, error) {
	release, err := s.acquire()
	if err != nil {
		return Archive{}, err
	}
	defer release()
	f, err := s.find(ctx, id)
	if err != nil {
		return Archive{}, err
	}
	if f.Format != FormatMediacore {
		return Archive{}, fmt.Errorf("%w: only a mediacore-backup/1 archive carries provenance", ErrForeignArchive)
	}
	src, err := openArchive(f.Path, s.limits())
	if err != nil {
		return Archive{}, err
	}
	defer s.closeRead("the archive being re-signed", src)
	live := ""
	if s.o.Store != nil {
		live = s.o.Store.InstallID()
	}
	if Classify(src.Manifest, s.o.Keyring, live) != Own {
		return Archive{}, fmt.Errorf("%w: an archive that is not Own is not re-signed", ErrForeignArchive)
	}
	if src.Manifest.Provenance.KeyID == s.o.Keyring.ActiveDeriveID() {
		return f.Archive, nil
	}
	set := s.o.settings()
	codec, err := backuparchive.Resolve(set.Compression)
	if err != nil {
		return Archive{}, err
	}
	now := s.o.now().UTC().Round(0)
	stagingRoot := filepath.Join(filepath.Dir(s.o.DBPath), stagingDirName)
	if err := mkdirPrivate(stagingRoot); err != nil {
		return Archive{}, err
	}
	if err := s.sameDevice(stagingRoot); err != nil {
		return Archive{}, err
	}
	newID, err := NewArchiveID(now)
	if err != nil {
		return Archive{}, err
	}
	run, err := newRunDir(filepath.Join(stagingRoot, "rewrap-"+string(newID)))
	if err != nil {
		return Archive{}, err
	}
	defer func() {
		if cerr := errors.Join(run.Close(), os.RemoveAll(run.path)); cerr != nil {
			s.o.log().Warn("backup: removing the run directory", "run", filepath.Base(run.path), "error", cerr)
		}
	}()
	ex, err := backuparchive.NewExtractor(run.path)
	if err != nil {
		return Archive{}, err
	}
	cerr := src.consume(ctx, ex, "")
	if serr := errors.Join(ex.Sync(), ex.Close()); cerr == nil {
		cerr = serr
	}
	if cerr != nil {
		return Archive{}, cerr
	}
	m := *src.Manifest
	m.ArchiveID = newID
	if err := Sign(&m, s.o.Keyring); err != nil {
		return Archive{}, err
	}
	mb, err := MarshalManifest(&m)
	if err != nil {
		return Archive{}, err
	}
	name, final, err := s.freshName(now, codec.Extension())
	if err != nil {
		return Archive{}, err
	}
	sum, size, err := writeArchive(ctx, archiveSpec{
		Partial: final + ".partial", Codec: codec, Level: set.CompressionLevel,
		Manifest: mb, Created: m.CreatedAt, Entries: m.Entries, RunDir: run.path,
	})
	if err != nil {
		return Archive{}, err
	}
	if err := os.Rename(final+".partial", final); err != nil {
		_ = os.Remove(final + ".partial")
		return Archive{}, err
	}
	if err := s.writeSidecars(final, name, sum, mb); err != nil {
		removeArchive(final)
		return Archive{}, err
	}
	res, verr := s.verifyArchive(ctx, final, newID, VerifyEntries)
	if verr != nil {
		removeArchive(final)
		return Archive{}, fmt.Errorf("backup: the re-signed archive did not verify: %w", verr)
	}
	out := Archive{ID: newID, Name: name, Size: size, CreatedAt: m.CreatedAt, Format: FormatMediacore, Provenance: Own, Manifest: &m, Verified: &res}
	// The original goes only when no floor protects it, now that the new one is
	// the newest verified.
	all, err := s.scan(ctx)
	if err != nil {
		return out, err
	}
	if _, prot := s.floors(all)[f.ID]; !prot {
		if err := s.deleteFiles(f); err != nil {
			s.o.log().Warn("backup: the original archive was not deleted after a rewrap", "archive", f.Name, "error", err)
		}
	}
	return out, nil
}
