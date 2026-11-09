// file: internal/backup/prepare_restore_test.go
// version: 1.0.5
// guid: 29ed21ba-737f-4de2-844d-5681aec413d7
// last-edited: 2026-11-09

package backup

import (
	"archive/tar"
	"os"
	"strings"
	"testing"
)

// writePlainTar writes an uncompressed tar archive of regular files, and of
// directories for names that end in a slash. The tests of HEAD's in-place
// restore split (PrepareRestoreIn, then Extract) that lived here went with
// it in R1-25: the live restore stages through StageRestore and
// ExtractStore, whose refusals TestStageRestore and the ExtractStore tests
// cover.
func writePlainTar(t *testing.T, path string, entries [][2]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	for _, e := range entries {
		hdr := &tar.Header{Name: e[0], Mode: 0o644, Size: int64(len(e[1])), Typeflag: tar.TypeReg}
		if strings.HasSuffix(e[0], "/") {
			hdr = &tar.Header{Name: e[0], Mode: 0o755, Typeflag: tar.TypeDir}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
