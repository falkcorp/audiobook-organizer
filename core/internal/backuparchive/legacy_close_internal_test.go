// file: core/internal/backuparchive/legacy_close_internal_test.go
// version: 0.1.0
// guid: 7f3b46cc-b2f8-4849-b4d5-ddffa669f17e
// last-edited: 2026-11-20

package backuparchive

import (
	"archive/tar"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// failClose is an archive whose Close reports err after closing the file.
type failClose struct {
	*os.File
	err error
}

func (f failClose) Close() error { return errors.Join(f.File.Close(), f.err) }

// TestExtractLegacyReportsAFailedClose: a Close error from the deferred closes
// reaches the caller, and with it no written files. The deferred closes join
// into the named result; with unnamed results they would join into a local and
// the extraction would report success.
func TestExtractLegacyReportsAFailedClose(t *testing.T) {
	p := filepath.Join(t.TempDir(), "arkham.tar")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	body := "Oswald Cobblepot"
	if err := errors.Join(
		tw.WriteHeader(&tar.Header{Name: "audiobooks.pebble/", Typeflag: tar.TypeDir, Mode: 0o700}),
		tw.WriteHeader(&tar.Header{Name: "audiobooks.pebble/000001.sst", Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(body))}),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tw, body); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(tw.Close(), f.Close()); err != nil {
		t.Fatal(err)
	}
	lim := Limits{}.withDefaults()
	info, err := ScanLegacy(t.Context(), p, lim)
	if err != nil {
		t.Fatal(err)
	}
	open := func(closeErr error) func() (io.ReadCloser, int64, error) {
		return func() (io.ReadCloser, int64, error) {
			f, st, err := OpenRegular(p, lim.MaxArchiveBytes)
			if err != nil {
				return nil, 0, err
			}
			return failClose{f, closeErr}, st.Size(), nil
		}
	}
	got, err := extractLegacy(t.Context(), info, t.TempDir(), lim, open(nil))
	if err != nil || len(got) != 1 {
		t.Fatalf("control: %v, %v", got, err)
	}
	closeErr := errors.New("the Penguin's umbrella jammed")
	got, err = extractLegacy(t.Context(), info, t.TempDir(), lim, open(closeErr))
	if !errors.Is(err, closeErr) || got != nil {
		t.Fatalf("a failed Close: %v, %v; want the Close error and no files", got, err)
	}
}
