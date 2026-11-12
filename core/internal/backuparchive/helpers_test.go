// file: core/internal/backuparchive/helpers_test.go
// version: 1.0.0
// guid: 456cb7e7-3a76-4c1d-90a1-d868a4529e47
// last-edited: 2026-11-12

package backuparchive_test

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/mediacore/internal/backuparchive"
)

// ent is one tar entry of a test archive.
type ent struct {
	name string
	body string
	typ  byte // 0 = regular
	link string
	size int64 // header size when it should differ from the body's (a lie)
}

// buildTar writes entries into one tar archive through codec and returns its
// path in a fresh directory.
func buildTar(t *testing.T, codec string, entries ...ent) string {
	t.Helper()
	c, err := backuparchive.Resolve(codec)
	if err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	zw, err := c.NewWriter(&raw, backuparchive.LevelDefault)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		hdr := &tar.Header{Name: e.name, Typeflag: typ, Mode: 0o644, Linkname: e.link}
		switch typ {
		case tar.TypeReg:
			hdr.Size = int64(len(e.body))
			if e.size != 0 {
				hdr.Size = e.size
			}
		case tar.TypeDir:
			hdr.Mode = 0o755
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := io.WriteString(tw, e.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "archive"+c.Extension())
	if err := os.WriteFile(p, raw.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// readAll opens path and returns the entries Reader yields, or the first error.
func readAll(t *testing.T, path string, lim backuparchive.Limits, dirs bool) (names []string, err error) {
	t.Helper()
	f, _, err := backuparchive.OpenRegular(path, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rd, err := backuparchive.NewReader(f, lim, dirs)
	if err != nil {
		return nil, err
	}
	defer rd.Close()
	for {
		h, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return names, err
		}
		if _, err := io.Copy(io.Discard, rd); err != nil {
			return names, err
		}
		names = append(names, h.Name)
	}
	return names, rd.Finish()
}
