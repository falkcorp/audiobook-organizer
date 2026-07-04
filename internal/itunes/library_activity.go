// file: internal/itunes/library_activity.go
// version: 1.0.0
// guid: 2d6f8a1b-4c3e-4f7a-9b0d-5e8c1a2b3c4d
// last-edited: 2026-07-03
//
// FileActivityLibraryCheck — a concrete "is iTunes using the library?" signal
// (SPEC 2 §3 step 8 / SPEC 3 §4). iTunes on the Windows box accesses the
// library over the network share, so process-level checks (lsof etc.) cannot
// see it from the server. What IS visible is write activity: while iTunes has
// the library open it touches the .itl and its journal siblings ("Temp File
// N.tmp", "iT N.tmp", "sentinel") continuously. Recent mtime on any of them
// is treated as "in use"; the batcher then defers the flush and re-enqueues,
// so a false positive costs one debounce cycle, never data.

package itunes

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// libraryActivityGlobs are the sibling files iTunes touches while it has the
// library open (journal/temp files and the sentinel), checked alongside the
// library file itself.
var libraryActivityGlobs = []string{"Temp File*.tmp", "iT*.tmp", "sentinel"}

// FileActivityLibraryCheck returns a precondition function for
// SetLibraryNotInUse / WithLibraryNotInUse: it errors when the library at
// path — or any iTunes journal sibling in its directory — was modified within
// `window` of the call. A missing library file yields nil (no signal: first
// run or not yet provisioned); only observed recent activity blocks a write.
func FileActivityLibraryCheck(path string, window time.Duration) func() error {
	if window <= 0 {
		window = 2 * time.Minute
	}
	return func() error {
		cutoff := time.Now().Add(-window)

		if fi, err := os.Stat(path); err == nil && fi.ModTime().After(cutoff) {
			return fmt.Errorf("library %s modified %s ago (< %s window): iTunes may have it open",
				filepath.Base(path), time.Since(fi.ModTime()).Round(time.Second), window)
		}

		dir := filepath.Dir(path)
		for _, pattern := range libraryActivityGlobs {
			matches, err := filepath.Glob(filepath.Join(dir, pattern))
			if err != nil {
				continue // malformed pattern cannot happen with the fixed globs above
			}
			for _, m := range matches {
				fi, err := os.Stat(m)
				if err != nil {
					continue
				}
				if fi.ModTime().After(cutoff) {
					return fmt.Errorf("iTunes journal file %s modified %s ago (< %s window): library may be open",
						filepath.Base(m), time.Since(fi.ModTime()).Round(time.Second), window)
				}
			}
		}
		return nil
	}
}
