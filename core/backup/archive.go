// file: core/backup/archive.go
// version: 1.0.1
// guid: 5d1b3c8e-24a7-4f0c-b1d9-3b6a0c94d7e2
// last-edited: 2026-11-12

package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/internal/backuparchive"
)

// ctxReader fails a read once its context is done, so every long copy stops
// between chunks (§5B.6).
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// archiveSpec is what writeArchive streams.
type archiveSpec struct {
	Partial  string // created O_EXCL, 0600: never an existing file, never a link
	Codec    backuparchive.Codec
	Level    int
	Manifest []byte
	Created  time.Time
	Entries  []Entry // in archive order, after the manifest
	RunDir   string  // the files, by archive path
}

// writeArchive is §5B.3.5 step 5's stream: tar over the codec into a .partial
// file, manifest first, re-hashing every input while it streams and failing on
// any difference from the manifest's record (an input that changed since the
// hash pass). It returns the archive's outer SHA-256 and size. On any error
// the partial file is removed; it never leaves one behind, cancellation
// included. The file is fsynced; renaming it is the caller's step.
func writeArchive(ctx context.Context, sp archiveSpec) (sum string, size int64, err error) {
	f, err := os.OpenFile(sp.Partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, fmt.Errorf("backup: create %s: %w", sp.Partial, err)
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(sp.Partial)
		}
	}()
	outer := sha256.New()
	cw := &countWriter{w: io.MultiWriter(f, outer)}
	zw, err := sp.Codec.NewWriter(cw, sp.Level)
	if err != nil {
		return "", 0, err
	}
	tw := tar.NewWriter(zw)
	put := func(name string, n int64, r io.Reader) error {
		hdr := &tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o600, Size: n, ModTime: sp.Created}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		got, err := io.Copy(tw, ctxReader{ctx, r})
		if err != nil {
			return err
		}
		if got != n {
			return fmt.Errorf("backup: %s: wrote %d of %d bytes", name, got, n)
		}
		return nil
	}
	if err = put(ManifestName, int64(len(sp.Manifest)), bytes.NewReader(sp.Manifest)); err != nil {
		return "", 0, err
	}
	root, err := os.OpenRoot(sp.RunDir)
	if err != nil {
		return "", 0, err
	}
	defer root.Close()
	for _, e := range sp.Entries {
		if err = ctx.Err(); err != nil {
			return "", 0, err
		}
		if err = streamEntry(root, e, put); err != nil {
			return "", 0, err
		}
	}
	if err = tw.Close(); err != nil {
		return "", 0, err
	}
	if err = zw.Close(); err != nil {
		return "", 0, err
	}
	if err = f.Sync(); err != nil {
		return "", 0, err
	}
	if err = f.Close(); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(outer.Sum(nil)), cw.n, nil
}

// streamEntry copies one run-directory file into the tar, checking it is the
// regular file the hash pass saw and that its bytes still hash to the manifest.
func streamEntry(root *os.Root, e Entry, put func(string, int64, io.Reader) error) error {
	fi, err := root.Lstat(e.Path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file", backuparchive.ErrNotRegularFile, e.Path)
	}
	fh, err := root.Open(e.Path)
	if err != nil {
		return err
	}
	defer fh.Close()
	if st, err := fh.Stat(); err != nil || !os.SameFile(fi, st) {
		return fmt.Errorf("backup: %s was replaced while it was being archived", e.Path)
	}
	h := sha256.New()
	if err := put(e.Path, e.Size, io.TeeReader(io.LimitReader(fh, e.Size), h)); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != e.SHA256 {
		return fmt.Errorf("%w: %s changed while it was being archived", ErrManifest, e.Path)
	}
	return nil
}

// openedArchive is a mediacore-backup/1 archive open for reading, its
// manifest read and parsed (the first entry, by rule).
type openedArchive struct {
	f             *os.File
	rd            *backuparchive.Reader
	Manifest      *Manifest
	ManifestBytes []byte
}

// openArchive opens path (a regular file, never followed through a link, at
// most lim.MaxArchiveBytes), reads its first entry and parses it as the
// manifest. A first entry that is anything else, or is over MaxManifestBytes,
// is ErrManifest; so is a manifest that does not parse.
func openArchive(path string, lim backuparchive.Limits) (a *openedArchive, err error) {
	f, _, err := backuparchive.OpenRegular(path, lim.MaxArchiveBytes)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
		}
	}()
	rd, err := backuparchive.NewReader(f, lim, false)
	if err != nil {
		return nil, err
	}
	h, err := rd.Next()
	if err != nil {
		_ = rd.Close()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: the archive is empty", ErrManifest)
		}
		// HEAD's archives open with the store directory's own entry: a first entry
		// of another type is "not a mediacore-backup/1 archive", which the callers
		// treat as a candidate legacy-abo/0 archive and scan as one.
		if errors.Is(err, backuparchive.ErrEntryType) {
			return nil, fmt.Errorf("%w: the first entry is not the manifest: %v", ErrManifest, err)
		}
		return nil, err
	}
	if h.Name != ManifestName || h.Size > MaxManifestBytes {
		_ = rd.Close()
		return nil, fmt.Errorf("%w: the first entry is %q, not %s", ErrManifest, h.Name, ManifestName)
	}
	raw := make([]byte, h.Size)
	if _, err = io.ReadFull(rd, raw); err != nil {
		_ = rd.Close()
		return nil, fmt.Errorf("%w: %v", ErrManifest, err)
	}
	m, err := ParseManifest(raw)
	if err != nil {
		_ = rd.Close()
		return nil, err
	}
	return &openedArchive{f: f, rd: rd, Manifest: m, ManifestBytes: raw}, nil
}

func (a *openedArchive) Close() error { return errors.Join(a.rd.Close(), a.f.Close()) }

// consume reads the rest of the archive against its manifest: every entry must
// be listed (an unlisted one is ErrManifest), match its size and digest, and
// every listed entry must appear. With ex non-nil, entries under strip+"/"
// (every entry when strip is "") are written through it as they stream, named
// without the prefix, each checked while it is written; the others are only
// hashed. A mismatch removes the file just written and fails the call.
func (a *openedArchive) consume(ctx context.Context, ex *backuparchive.Extractor, strip string) error {
	want := make(map[string]Entry, len(a.Manifest.Entries))
	for _, e := range a.Manifest.Entries {
		want[e.Path] = e
	}
	seen := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := a.rd.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		e, ok := want[h.Name]
		if !ok {
			return fmt.Errorf("%w: entry %q is not in the manifest", ErrManifest, h.Name)
		}
		if h.Size != e.Size {
			return fmt.Errorf("%w: entry %q is %d bytes, the manifest says %d", ErrManifest, h.Name, h.Size, e.Size)
		}
		r := ctxReader{ctx, a.rd}
		if name, in := stripped(h.Name, strip); ex != nil && in {
			if _, err := ex.WriteFile(name, r, &backuparchive.Expect{Size: e.Size, SHA256: e.SHA256}); err != nil {
				return mismatch(err, h.Name)
			}
		} else if err := hashEntry(r, e); err != nil {
			return err
		}
		seen++
	}
	if err := a.rd.Finish(); err != nil {
		return err
	}
	if seen != len(want) {
		return fmt.Errorf("%w: the archive holds %d of the manifest's %d entries", ErrManifest, seen, len(want))
	}
	return nil
}

func stripped(name, strip string) (string, bool) {
	if strip == "" {
		return name, true
	}
	if rest, ok := strings.CutPrefix(name, strip+"/"); ok && rest != "" {
		return rest, true
	}
	return "", false
}

func mismatch(err error, name string) error {
	if errors.Is(err, backuparchive.ErrEntryMismatch) {
		return fmt.Errorf("%w: entry %q does not match its recorded digest", ErrManifest, name)
	}
	return err
}

func hashEntry(r io.Reader, e Entry) error {
	var h hash.Hash = sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return err
	}
	if n != e.Size || hex.EncodeToString(h.Sum(nil)) != e.SHA256 {
		return fmt.Errorf("%w: entry %q does not match its recorded digest", ErrManifest, e.Path)
	}
	return nil
}
