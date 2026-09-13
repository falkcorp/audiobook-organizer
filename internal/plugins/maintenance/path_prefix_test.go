// file: internal/plugins/maintenance/path_prefix_test.go
// version: 1.0.0
// guid: e41b6d09-2f7a-4c83-9b15-6a0d8e3c47f2
// last-edited: 2026-09-13

package maintenance

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

func TestPathPrefixMatches(t *testing.T) {
	for _, c := range []struct {
		path, prefix string
		want         bool
	}{
		{"/lib/a.m4b", "", true},       // empty prefix = no filter
		{"/lib", "/lib", true},         // equal to the prefix
		{"/lib/a.m4b", "/lib", true},   // under it
		{"/lib/a.m4b", "/lib/", true},  // trailing separator tolerated
		{"/lib", "/lib/", false},       // the folder itself is not under "/lib/" (same as HasPrefix)
		{"/lib2/a.m4b", "/lib", false}, // the sibling-folder bug this fixes
		{"/lib2/a.m4b", "/lib/", false},
		{"/lib2", "/lib", false},
	} {
		require.Equal(t, c.want, pathPrefixMatches(c.path, c.prefix), "path=%q prefix=%q", c.path, c.prefix)
	}
}

// TestPathPrefix_OpsMatchOnFolderBoundary drives every op that takes a
// PathPrefix through its REAL plan function, so an op whose call site still uses
// strings.HasPrefix fails here even though TestPathPrefixMatches stays green.
//
// Every fixture path is absent from disk, so each row is "missing" and each op's
// in-scope counter equals the number of rows the prefix admitted.
func TestPathPrefix_OpsMatchOnFolderBoundary(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "lib")
	paths := []string{
		filepath.Join(root, "a.m4b"),
		filepath.Join(root, "sub", "b.m4b"),
		filepath.Join(root+"2", "a.m4b"), // sibling folder: "/lib" must NOT match it
	}
	cores := func() []database.BookFileCore {
		out := make([]database.BookFileCore, len(paths))
		for i, p := range paths {
			id := string(rune('1' + i))
			out[i] = database.BookFileCore{ID: "f" + id, BookID: "b" + id, FilePath: p}
		}
		return out
	}
	ctx := context.Background()

	ops := map[string]func(t *testing.T, prefix string) int{
		"mark-missing-files": func(t *testing.T, prefix string) int {
			plan, err := planMarkMissingFiles(ctx, &markFakeStore{cores: cores()}, nil,
				markMissingParams{PathPrefix: prefix}, &fakeReporter{})
			require.NoError(t, err)
			return plan.ScannedRows
		},
		"missing-file-repoint": func(t *testing.T, prefix string) int {
			// ScannedRows is len(files) before the filter; MissingRows is the in-scope count.
			plan, err := planMissingFileRepoint(ctx, &repointFakeStore{cores: cores()}, nil,
				missingFileRepointParams{PathPrefix: prefix}, &fakeReporter{})
			require.NoError(t, err)
			return plan.MissingRows
		},
		"missing-file-repair": func(t *testing.T, prefix string) int {
			return planRepair(t, cores(), missingFileRepairParams{PathPrefix: prefix}).BooksExamined
		},
		"missing-file-audit": func(t *testing.T, prefix string) int {
			return runAudit(t, cores(), missingFileAuditParams{PathPrefix: prefix}).TotalRows
		},
		"merge-same-path-dupes": func(t *testing.T, prefix string) int {
			// Two records per path, so each in-scope path is one shared-path group.
			store := &mergeFakeStore{}
			for i, p := range paths {
				id := string(rune('1' + i))
				store.books = append(store.books,
					database.BookCore{ID: "x" + id, FilePath: p}, database.BookCore{ID: "y" + id, FilePath: p})
			}
			m := &recordingMerge{}
			plan, err := planMergeSamePathDupes(ctx, store, m.fn,
				mergeSamePathParams{PathPrefix: prefix}, &fakeReporter{})
			require.NoError(t, err)
			return plan.SharedPaths
		},
	}

	cases := []struct {
		name, prefix string
		want         int
	}{
		{"empty prefix is no filter", "", 3},
		{"folder excludes sibling", root, 2},
		{"trailing separator", root + string(filepath.Separator), 2},
		{"path equal to prefix", paths[0], 1},
		{"sibling folder itself", root + "2", 1},
	}
	for name, run := range ops {
		for _, c := range cases {
			t.Run(name+"/"+c.name, func(t *testing.T) {
				require.Equal(t, c.want, run(t, c.prefix), "prefix=%q", c.prefix)
			})
		}
	}
}
