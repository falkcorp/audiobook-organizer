// file: internal/books/dirimage_test.go
// version: 0.1.0
// guid: c5466fe1-bc27-41f7-8544-65f99b827b84
// last-edited: 2026-11-22

package books

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Directory images: what a boot that was refused must have left untouched
// (floor_test.go, flip_authgate_test.go). These moved here from
// flip_older_builds_test.go when R3-02e-1 deleted that file's subject.

// dirImage is every file under dir by relative path and content, so a boot that
// wrote nothing can be told from one that rewrote a sidecar.
func dirImage(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		// Pebble's own files (tables, manifest, WAL, lock) are rewritten by any open:
		// the refusal can come after the open, and a compaction replaces tables
		// without changing a row. The rows are compared by rawKVs; this image is for
		// the sidecars and everything else beside the store.
		if strings.Contains(p, ".pebble"+string(filepath.Separator)) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[rel] = string(b)
		return nil
	}))
	return out
}

// changeOf says what a write did to a file whose content went from before to
// after: the bytes it appended (a log line names the boot that wrote it), or,
// for a rewrite, both sizes and the new tail. A bare "changed" names no boot, so
// a failure could not say which boot wrote state/logs/abo.log.
func changeOf(before, after string) string {
	const max = 4096
	clip := func(s string) string {
		if len(s) > max {
			return "..." + s[len(s)-max:]
		}
		return s
	}
	if strings.HasPrefix(after, before) {
		return fmt.Sprintf("appended %q", clip(after[len(before):]))
	}
	return fmt.Sprintf("rewritten, %d bytes before and %d after, ending %q", len(before), len(after), clip(after))
}

// imageDiff names the files that differ between two dirImages (names only: the
// contents are large and a failure message must stay readable).
func imageDiff(before, after map[string]string) []string {
	var out []string
	for k, v := range before {
		if w, ok := after[k]; !ok {
			out = append(out, "removed "+k)
		} else if w != v {
			out = append(out, "changed "+k+": "+changeOf(v, w))
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			out = append(out, "added "+k)
		}
	}
	slices.Sort(out)
	return out
}
