// file: internal/pathutil/commondir.go
// version: 1.0.0
// guid: 7c0b3f49-5a26-4d18-9e72-0b8d4a6c2f51
// last-edited: 2026-06-19

package pathutil

import (
	"path/filepath"
	"strings"
)

// CommonDir returns the deepest directory that contains all of the given file
// paths. With a single file it returns that file's directory; with no shared
// ancestor it returns "/". Returns "" for empty input. This is the shared-prefix
// fallback used when a path matches no named library root.
//
// (Folded in from the previously-unused server.filesCommonDir helper so the
// logic lives in one place instead of being hardcoded at call sites.)
func CommonDir(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	common := filepath.Dir(paths[0])
	for _, p := range paths[1:] {
		dir := filepath.Dir(p)
		for common != dir && !strings.HasPrefix(dir, common+string(filepath.Separator)) {
			common = filepath.Dir(common)
			if common == "/" || common == "." {
				return common
			}
		}
	}
	return common
}
