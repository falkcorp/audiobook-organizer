// file: internal/organizer/resolve_organized_file_path_test.go
// version: 1.0.0
// guid: 6a1f0c8d-4b52-4f7e-9d31-2c8ea45b7f60
// last-edited: 2026-08-15

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
// answer to "where does this file go", never checked against the disk. Now that
// the file naming pattern actually renames the files, that guess is simply
// wrong, and wrong here is SILENT: a row that names a plausible path nobody ever
// wrote is indistinguishable from a correct one until someone opens the book.
//
// The plan says where organize INTENDED to put each file, not that the copy
// happened, so disk is the tiebreaker. This is the case-by-case statement of
// that rule.
func TestResolveOrganizedFilePath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.m4b")
	dst := filepath.Join(dir, "Organized - 01.m4b")
	gone := filepath.Join(dir, "vanished.m4b")
	unwritten := filepath.Join(dir, "never-copied.m4b")

	for _, p := range []string{src, dst} {
		if err := os.WriteFile(p, []byte("audio"), 0644); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}

	log := &noopLogger{}

	cases := []struct {
		name    string
		srcPath string
		planned map[string]string
		want    string
		why     string
	}{
		{
			name:    "target exists — the copy landed, take it",
			srcPath: src,
			planned: map[string]string{src: dst},
			want:    dst,
			why:     "the normal path: organize copied the file, so the row must name the copy",
		},
		{
			name:    "target planned but absent, source still there — keep the source",
			srcPath: src,
			planned: map[string]string{src: unwritten},
			want:    src,
			why: "organize skips files whose destination is unsafe or occupied by a stranger. " +
				"Writing the plan on faith here is exactly the silent defect: a row pointing at nothing",
		},
		{
			name:    "file not in the plan at all — keep the source",
			srcPath: src,
			planned: map[string]string{dst: unwritten},
			want:    src,
			why:     "a row the planner dropped (no path, unreadable) must not inherit some other file's target",
		},
		{
			name:    "plan is a no-op — keep the source",
			srcPath: src,
			planned: map[string]string{src: src},
			want:    src,
			why:     "already in place; nothing moved",
		},
		{
			name:    "neither exists — take the plan",
			srcPath: gone,
			planned: map[string]string{gone: unwritten},
			want:    unwritten,
			why:     "the file is missing either way, and the planned path is where a restore should put it",
		},
		{
			name:    "empty target — keep the source",
			srcPath: src,
			planned: map[string]string{src: ""},
			want:    src,
			why:     "an empty string is not a path; never write one into a row",
		},
		{
			name:    "nil plan — keep the source",
			srcPath: src,
			planned: nil,
			want:    src,
			why:     "PlanFilePaths returning an error leaves the map nil; rows must survive that unchanged",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveOrganizedFilePath(tc.srcPath, tc.planned, log)
			if got != tc.want {
				t.Errorf("resolveOrganizedFilePath(%q) = %q, want %q\n  why: %s",
					tc.srcPath, got, tc.want, tc.why)
			}
		})
	}
}
