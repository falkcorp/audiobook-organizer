// file: core/backup/verify.go
// version: 1.0.4
// guid: d41f0c73-7b2e-4e88-a6d5-0c9e3b1f5a27
// last-edited: 2026-11-12

package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/falkcorp/mediacore/internal/backuparchive"
	"github.com/falkcorp/mediacore/storage"
)

const verifyScratchDir = ".backup-verify"

// Verify checks an archive at the given level and records the result in
// <archive>.verify.json. A failed check returns the failed result together
// with its error (ErrChecksumMismatch, ErrNoChecksum, ErrManifest, or the
// Inspector's); a canceled one returns the context's error and records
// nothing, because cancellation is not a verdict.
func (s *Svc) Verify(ctx context.Context, id ArchiveID, l VerifyLevel) (VerifyResult, error) {
	f, err := s.find(ctx, id)
	if err != nil {
		return VerifyResult{}, err
	}
	return s.verifyArchive(ctx, f.Path, id, l)
}

func (s *Svc) verifyArchive(ctx context.Context, path string, id ArchiveID, l VerifyLevel) (VerifyResult, error) {
	return s.verifyWith(ctx, path, id, l, false)
}

// verifyWith is verifyArchive with the checksum sidecar optional: skipSum is
// for a restore of an archive that has none, under AllowWeakVerify.
func (s *Svc) verifyWith(ctx context.Context, path string, id ArchiveID, l VerifyLevel, skipSum bool) (VerifyResult, error) {
	if l < VerifyChecksum || l > VerifyOpen {
		return VerifyResult{}, fmt.Errorf("backup: unknown verify level %d", l)
	}
	err := s.runVerify(ctx, path, l, skipSum)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return VerifyResult{}, err
	}
	res := VerifyResult{At: s.o.now().UTC().Round(0), Level: l, OK: err == nil}
	if err != nil {
		res.Reason = reasonOf(err)
		res.Detail = err.Error()
		if len(res.Detail) > 500 {
			res.Detail = res.Detail[:500]
		}
		// The text names host paths; it goes to the operator's log and the
		// sidecar, and the reason code is what a caller may show.
		s.o.log().Warn("backup: an archive failed verification", "archive", id, "level", l.String(), "reason", string(res.Reason), "error", err)
	}
	if werr := writeVerify(path+VerifySidecar, res); werr != nil {
		s.o.log().Warn("backup: could not record the verification", "archive", id, "error", werr)
	}
	s.o.Metrics.Verified(ctx, l, res.OK, res.At)
	return res, err
}

func (s *Svc) runVerify(ctx context.Context, path string, l VerifyLevel, skipSum bool) error {
	if !skipSum {
		if err := s.checkSum(ctx, path); err != nil {
			return err
		}
	}
	if l == VerifyChecksum {
		return nil
	}
	a, err := openArchive(path, s.limits())
	if err != nil {
		if !errors.Is(err, ErrManifest) {
			return err
		}
		return s.verifyLegacy(ctx, path, l, err)
	}
	defer s.closeRead("the archive being verified", a)
	if side, err := readBounded(path+ManifestSidecar, MaxManifestBytes); err == nil && !bytes.Equal(side, a.ManifestBytes) {
		return fmt.Errorf("%w: the manifest sidecar differs from the archive's manifest", ErrManifest)
	}
	if l == VerifyEntries {
		return a.consume(ctx, nil, "")
	}
	if s.o.Inspector == nil {
		return errors.New("backup: verify at the open level needs an Inspector")
	}
	scratch, cleanup, err := s.scratch()
	if err != nil {
		return err
	}
	defer cleanup()
	ex, err := backuparchive.NewExtractor(scratch)
	if err != nil {
		return err
	}
	cerr := a.consume(ctx, ex, "db")
	if serr := errors.Join(ex.Sync(), ex.Close()); cerr == nil {
		cerr = serr
	}
	if cerr != nil {
		return cerr
	}
	info, err := s.o.Inspector.Inspect(ctx, scratch)
	if err != nil {
		return openRefusal("the archived store does not open", err)
	}
	return matchManifest(a.Manifest, info)
}

// matchManifest is VerifyOpen's comparison: the store read from the archive
// must carry the levels, the install marker and the exact per-owner key counts
// the manifest records.
func matchManifest(m *Manifest, info StoreInfo) error {
	if len(info.Levels) != len(m.Levels) {
		return fmt.Errorf("%w: the store's levels %v differ from the manifest's %v", ErrManifest, info.Levels, m.Levels)
	}
	for o, v := range m.Levels {
		if got, ok := info.Levels[o]; !ok || got != v {
			return fmt.Errorf("%w: level of %s is %d, the manifest says %d", ErrManifest, o, got, v)
		}
	}
	if info.InstallID != "" && info.InstallID != m.InstallID {
		return fmt.Errorf("%w: the store's install marker is not the manifest's", ErrManifest)
	}
	if info.Keys == nil {
		return nil // an inspector with no live admin cannot count; the store opened and its levels match
	}
	for o, ks := range m.Keyspaces {
		if got := info.Keys[o]; got != ks.Keys {
			return fmt.Errorf("%w: %s holds %d keys, the manifest says %d", ErrManifest, o, got, ks.Keys)
		}
	}
	for o := range info.Keys {
		if _, ok := m.Keyspaces[o]; !ok && info.Keys[o] != 0 {
			return fmt.Errorf("%w: %s holds keys the manifest does not list", ErrManifest, o)
		}
	}
	return nil
}

// verifyLegacy verifies a legacy-abo/0 archive: it has no entry hashes, so the
// entries level is a structural scan (one store directory, safe names, bounded)
// and the open level extracts the store and opens it.
func (s *Svc) verifyLegacy(ctx context.Context, path string, l VerifyLevel, why error) error {
	info, err := backuparchive.ScanLegacy(ctx, path, s.limits())
	if err != nil {
		return errors.Join(err, why)
	}
	if l == VerifyEntries {
		return nil
	}
	if s.o.Inspector == nil {
		return errors.New("backup: verify at the open level needs an Inspector")
	}
	scratch, cleanup, err := s.scratch()
	if err != nil {
		return err
	}
	defer cleanup()
	if err := backuparchive.ExtractLegacy(ctx, path, info, scratch, s.limits()); err != nil {
		return err
	}
	if _, err := s.o.Inspector.Inspect(ctx, scratch); err != nil {
		return openRefusal("the archived store does not open", err)
	}
	return nil
}

// scratch is a private empty directory on the database's filesystem.
func (s *Svc) scratch() (dir string, cleanup func(), err error) {
	root := filepath.Join(filepath.Dir(s.o.DBPath), verifyScratchDir)
	if err := mkdirPrivate(root); err != nil {
		return "", nil, err
	}
	dir, err = os.MkdirTemp(root, "v-")
	if err != nil {
		return "", nil, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// checkSum hashes the archive and compares it with its .sha256 sidecar
// (sha256sum format: "<hex>  <name>").
func (s *Svc) checkSum(ctx context.Context, path string) error { return checkSumFile(ctx, path) }

// checkSumFile is checkSum without a service: the offline extractor has none.
func checkSumFile(ctx context.Context, path string) error {
	b, err := readBounded(path+ChecksumSidecar, maxSidecarBytes)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNoChecksum
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNoChecksum, err)
	}
	want, _, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	if _, err := hex.DecodeString(want); err != nil || len(want) != 64 {
		return fmt.Errorf("%w: the sidecar is not a SHA-256", ErrNoChecksum)
	}
	f, _, err := backuparchive.OpenRegular(path, 0)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(h, ctxReader{ctx, f})
	if err := errors.Join(err, f.Close()); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want) {
		return ErrChecksumMismatch
	}
	return nil
}

// openRefusal wraps what storage.Open refused. A Breaking step above the build
// is a build mismatch, "too new", not a damaged archive.
func openRefusal(what string, err error) error {
	if errors.Is(err, storage.ErrKeyspaceTooNew) {
		return fmt.Errorf("%w: %w", ErrBackupTooNew, err)
	}
	return &openFailure{what: what, err: err}
}
