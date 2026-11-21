// file: core/backup/verify.go
// version: 1.0.5
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
	"time"
	"unicode/utf8"

	"github.com/falkcorp/mediacore/internal/backuparchive"
	"github.com/falkcorp/mediacore/storage"
)

const (
	verifyScratchDir = ".backup-verify"
	// maxDetailBytes bounds VerifyResult.Detail, the failure's text.
	maxDetailBytes = 500
	// staleScratchAge is how old a verification's scratch directory must be
	// before the next verification removes it as a crashed run's leftover. A
	// verification takes hours at most, and the directory's time moves with
	// every file extracted into it.
	staleScratchAge = 24 * time.Hour
)

// truncateUTF8 cuts s to at most n bytes at a rune boundary, so the text a
// sidecar or an API body carries is never an invalid UTF-8 string.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Verify checks an archive at the given level and records the result in
// <archive>.verify.json. A failed check returns the failed result together
// with its error (ErrChecksumMismatch, ErrNoChecksum, ErrManifest, or the
// Inspector's); a canceled one returns the context's error and records
// nothing, because cancellation is not a verdict. Neither is a shortage of disk
// space for the open level's scratch copy: that check (ErrInsufficientSpace) says
// nothing about the archive, and a failed record written for it would take the
// archive out from under the newest-verified floor.
func (s *Svc) Verify(ctx context.Context, id ArchiveID, l VerifyLevel) (VerifyResult, error) {
	f, err := s.find(ctx, id)
	if err != nil {
		return VerifyResult{}, err
	}
	// f.ID is the catalogue's own ID of the archive found, not the caller's text:
	// the logs below name the archive by it.
	return s.verifyArchive(ctx, f.Path, f.ID, l)
}

func (s *Svc) verifyArchive(ctx context.Context, path string, id ArchiveID, l VerifyLevel) (VerifyResult, error) {
	res, _, err := s.verifyWith(ctx, path, id, l, false)
	return res, err
}

// verifyWith is verifyArchive with the checksum sidecar optional: skipSum is
// for a restore of an archive that has none, under AllowWeakVerify. It also
// returns the archive file's SHA-256 the passing verification vouched for (see
// runVerify), "" when it vouched for none.
func (s *Svc) verifyWith(ctx context.Context, path string, id ArchiveID, l VerifyLevel, skipSum bool) (VerifyResult, string, error) {
	if l < VerifyChecksum || l > VerifyOpen {
		return VerifyResult{}, "", fmt.Errorf("backup: unknown verify level %d", l)
	}
	sum, err := s.runVerify(ctx, path, l, skipSum)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrInsufficientSpace) {
		return VerifyResult{}, "", err
	}
	res := VerifyResult{At: s.o.now().UTC().Round(0), Level: l, OK: err == nil}
	if err != nil {
		res.Reason = reasonOf(err)
		res.Detail = truncateUTF8(err.Error(), maxDetailBytes)
		// The text names host paths; it goes to the operator's log and the
		// sidecar, and the reason code is what a caller may show.
		s.o.log().Warn("backup: an archive failed verification", "archive", id, "level", l.String(), "reason", string(res.Reason), "error", err)
	}
	if werr := writeVerify(path+VerifySidecar, res); werr != nil {
		s.o.log().Warn("backup: could not record the verification", "archive", id, "error", werr)
	}
	s.o.Metrics.Verified(ctx, l, res.OK, res.At)
	return res, sum, err
}

// runVerify verifies the archive at path and returns the SHA-256 of the archive
// file it vouched for: at the checksum level the sidecar-checked digest; at the
// entries and open levels the digest of the bytes the entries pass read (a
// mediacore-backup/1 archive's tee, a legacy archive's scan), which must equal
// the sidecar's when both exist. That digest is what binds a restore's staging
// to the bytes verified (StageRestore): a legacy-abo/0 archive has no MAC and no
// per-entry digests, and a foreign mediacore-backup/1 archive's MAC proves
// nothing, so for both it is the only tie. It is "" only when the verification
// read no digest: a checksum-level verification of an archive with no sidecar.
// With any error it is "": a verification that failed vouches for nothing.
func (s *Svc) runVerify(ctx context.Context, path string, l VerifyLevel, skipSum bool) (string, error) {
	sum := ""
	if !skipSum {
		var err error
		if sum, err = s.checkSum(ctx, path); err != nil {
			return "", err
		}
	}
	if l == VerifyChecksum {
		return sum, nil
	}
	a, err := openArchive(path, s.limits())
	if err != nil {
		if !errors.Is(err, ErrManifest) {
			return "", err
		}
		return s.verifyLegacy(ctx, path, l, err, sum)
	}
	defer s.closeRead("the archive being verified", a)
	if side, err := readBounded(path+ManifestSidecar, MaxManifestBytes); err == nil && !bytes.Equal(side, a.ManifestBytes) {
		return "", fmt.Errorf("%w: the manifest sidecar differs from the archive's manifest", ErrManifest)
	}
	if l == VerifyEntries {
		if err := a.consume(ctx, nil, ""); err != nil {
			return "", err
		}
		return vouch(a, sum)
	}
	if s.o.Inspector == nil {
		return "", errors.New("backup: verify at the open level needs an Inspector")
	}
	// The open level extracts the store: its declared bytes must fit beside the
	// database first, and the extractor stops at them.
	declared, err := declaredBytes(a.Manifest.Entries, dbPrefix+"/")
	if err != nil {
		return "", err
	}
	scratch, cleanup, err := s.scratch(declared)
	if err != nil {
		return "", err
	}
	defer cleanup()
	ex, err := backuparchive.NewExtractor(scratch)
	if err != nil {
		return "", err
	}
	ex.SetBudget(budgetOf(declared))
	cerr := a.consume(ctx, ex, dbPrefix)
	if serr := errors.Join(ex.Sync(), ex.Close()); cerr == nil {
		cerr = serr
	}
	if cerr != nil {
		return "", cerr
	}
	read, err := vouch(a, sum)
	if err != nil {
		return "", err
	}
	info, err := s.o.Inspector.Inspect(ctx, scratch)
	if err != nil {
		return "", openRefusal("the archived store does not open", err)
	}
	if err := matchManifest(a.Manifest, info); err != nil {
		return "", err
	}
	return read, nil
}

// vouch is the digest a passing entries or open level vouches for: the SHA-256
// of the bytes its entries pass read (a's tee, whole once consume finished).
// When the sidecar check ran (checked != ""), that pass must have read the same
// bytes: an archive replaced between the two reads would otherwise vouch for a
// checksum whose file nobody checked the entries of, so it is refused, as
// verifyLegacy refuses a scan of other bytes.
func vouch(a *openedArchive, checked string) (string, error) {
	read, err := a.fileSHA256()
	if err != nil {
		return "", err
	}
	if checked != "" && !strings.EqualFold(read, checked) {
		return "", fmt.Errorf("%w: %w: the entries pass read other bytes than the checksum check", ErrChecksumMismatch, backuparchive.ErrChanged)
	}
	return read, nil
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
// and the open level extracts the store and opens it. checked is the digest the
// sidecar check accepted ("" when none ran): the scan must read the same bytes,
// or the archive changed between the two and backuparchive.ErrChanged refuses
// it. It returns the scan's digest, the one the verification vouches for.
func (s *Svc) verifyLegacy(ctx context.Context, path string, l VerifyLevel, why error, checked string) (string, error) {
	info, err := backuparchive.ScanLegacy(ctx, path, s.limits())
	if err != nil {
		return "", errors.Join(err, why)
	}
	if checked != "" && !strings.EqualFold(info.SHA256, checked) {
		return "", fmt.Errorf("%w: the scan read other bytes than the checksum check", backuparchive.ErrChanged)
	}
	if l == VerifyEntries {
		return info.SHA256, nil
	}
	if s.o.Inspector == nil {
		return "", errors.New("backup: verify at the open level needs an Inspector")
	}
	scratch, cleanup, err := s.scratch(uint64(info.Bytes))
	if err != nil {
		return "", err
	}
	defer cleanup()
	if _, err := backuparchive.ExtractLegacy(ctx, path, info, scratch, s.limits()); err != nil {
		return "", err
	}
	if _, err := s.o.Inspector.Inspect(ctx, scratch); err != nil {
		return "", openRefusal("the archived store does not open", err)
	}
	return info.SHA256, nil
}

// scratch is a private empty directory on the database's filesystem, for an
// extraction of declared bytes. It first removes what a crashed verification
// left (a v-* directory untouched for staleScratchAge), then checks that the
// declared bytes fit the filesystem, margin included, so a verification of an
// archive too large for the disk is refused (ErrInsufficientSpace) before it
// writes a byte rather than filling the volume the database lives on.
func (s *Svc) scratch(declared uint64) (dir string, cleanup func(), err error) {
	root := filepath.Join(filepath.Dir(s.o.DBPath), verifyScratchDir)
	if err := mkdirPrivate(root); err != nil {
		return "", nil, err
	}
	s.sweepScratch(root)
	if err := s.requireSpace(spaceClaim{dir: root, bytes: declared}); err != nil {
		return "", nil, err
	}
	dir, err = os.MkdirTemp(root, "v-")
	if err != nil {
		return "", nil, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// sweepScratch removes the v-* directories of root that nothing has touched for
// staleScratchAge. A verification that is still running keeps its directory's
// time moving (every file it extracts into the top level is a change), and
// verifications of other archives run beside each other, so a younger directory
// is never touched.
func (s *Svc) sweepScratch(root string) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return
	}
	var now time.Time // read once, and only when there is a directory to judge
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "v-") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if now.IsZero() {
			now = s.o.now()
		}
		if now.Sub(fi.ModTime()) <= staleScratchAge {
			continue
		}
		s.o.log().Warn("backup: removing a stale verification directory", "name", e.Name())
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			s.o.log().Warn("backup: could not remove a stale verification directory", "name", e.Name(), "error", err)
		}
	}
}

// checkSum hashes the archive and compares it with its .sha256 sidecar
// (sha256sum format: "<hex>  <name>"). It returns the digest it computed, in
// lower-case hex, when the two match.
func (s *Svc) checkSum(ctx context.Context, path string) (string, error) {
	return checkSumFile(ctx, path)
}

// checkSumFile is checkSum without a service: the offline extractor has none.
func checkSumFile(ctx context.Context, path string) (string, error) {
	b, err := readBounded(path+ChecksumSidecar, maxSidecarBytes)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNoChecksum
	}
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNoChecksum, err)
	}
	want, _, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	if _, err := hex.DecodeString(want); err != nil || len(want) != 64 {
		return "", fmt.Errorf("%w: the sidecar is not a SHA-256", ErrNoChecksum)
	}
	f, _, err := backuparchive.OpenRegular(path, 0)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(h, ctxReader{ctx, f})
	if err := errors.Join(err, f.Close()); err != nil {
		return "", err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return "", ErrChecksumMismatch
	}
	return got, nil
}

// openRefusal wraps what storage.Open refused. A Breaking step above the build
// is a build mismatch, "too new", not a damaged archive.
func openRefusal(what string, err error) error {
	if errors.Is(err, storage.ErrKeyspaceTooNew) {
		return fmt.Errorf("%w: %w", ErrBackupTooNew, err)
	}
	return &openFailure{what: what, err: err}
}
