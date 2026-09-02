// file: internal/organizer/resolve_organized_file_path_test.go
// version: 1.1.0
// guid: 6a1f0c8d-4b52-4f7e-9d31-2c8ea45b7f60
// last-edited: 2026-09-02

package organizer

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveOrganizedFilePath pins the decision that writes the organized
// book's per-file rows.
//
// CreateOrganizedVersion used to derive these by GUESSING --
// filepath.Join(newPath, filepath.Base(bf.FilePath)) -- a fourth independent
// answer to "where does this file go", never checked against the disk. The
// 2026-08-15 fix replaced the guess with a recomputed PLAN plus an os.Stat
// tiebreaker: "if a file exists at the planned target, the copy landed". That
// was wrong in the one case that matters: two books planning the same target.
// The loser's copy was skipped, the winner's file sat at the planned path, and
// the tiebreaker pointed the loser's row at the winner's audio.
//
// The rule now: the row follows Landing.Files, the source->landed map the
// organize that just ran produced, and nothing else. A file in the map landed
// (created or adopted, decided by hash inside organizeBookDirectory); a file
// not in the map keeps its source path. There is no disk lookup here, and the
// cases below include the one where a file DOES exist at a plausible target
// and must still be ignored.
func TestResolveOrganizedFilePath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.m4b")
	dst := filepath.Join(dir, "Organized - 01.m4b")
	strangers := filepath.Join(dir, "Another Book - 01.m4b")

	for _, p := range []string{src, dst, strangers} {
		if err := os.WriteFile(p, []byte("audio"), 0644); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}

	log := &noopLogger{}

	cases := []struct {
		name    string
		srcPath string
		landed  map[string]string
		want    string
		why     string
	}{
		{
			name:    "landed — take the landed path",
			srcPath: src,
			landed:  map[string]string{src: dst},
			want:    dst,
			why:     "the normal path: organize copied (or adopted) the file, so the row must name that copy",
		},
		{
			name:    "not in the landing, though a file sits at the would-be target — keep the source",
			srcPath: src,
			landed:  map[string]string{},
			want:    src,
			why: "this is the two-books-one-target case: the file at the target is the OTHER book's. " +
				"A disk lookup here is exactly what pointed the loser's row at the winner's audio",
		},
		{
			name:    "landing maps other files only — keep the source",
			srcPath: src,
			landed:  map[string]string{"/elsewhere/ch02.m4b": strangers},
			want:    src,
			why:     "a row the organize did not land must not inherit some other file's target",
		},
		{
			name:    "landed in place — the same path",
			srcPath: src,
			landed:  map[string]string{src: src},
			want:    src,
			why:     "already in place; the map says so and the row keeps it",
		},
		{
			name:    "empty target — keep the source",
			srcPath: src,
			landed:  map[string]string{src: ""},
			want:    src,
			why:     "an empty string is not a path; never write one into a row",
		},
		{
			name:    "nil landing — keep the source",
			srcPath: src,
			landed:  nil,
			want:    src,
			why:     "a single-file Landing has no Files map; rows must survive that unchanged",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveOrganizedFilePath(tc.srcPath, tc.landed, log)
			if got != tc.want {
				t.Errorf("resolveOrganizedFilePath(%q) = %q, want %q\n  why: %s",
					tc.srcPath, got, tc.want, tc.why)
			}
		})
	}
}
