// file: internal/database/store_consumers_test.go
// version: 1.0.0
// guid: b4e92a07-6c15-4d3e-a8f1-92d07e5c3b61
// last-edited: 2026-10-10

package database

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// storeReferenceBaseline is the number of non-test, non-mock, non-comment
// source lines under internal/ and cmd/ that name the wide database.Store
// type. This is a ratchet: it may only be lowered, by hand, in the PR that
// lowers the count. The test fails only on a rise. 07-S3 and 07-M14 are the
// PRs expected to lower it. Companion of storeMethodBaseline in
// store_width_test.go.
const storeReferenceBaseline = 31

// storeReferenceKinds classifies the files that carry a reference, so the
// figures quoted in the 07 design decisions (D2) stay checkable. It is an aid,
// not a gate: the gate is the total. A file with mixed kinds is listed under
// its dominant kind. Paths only; no library data.
//
//	assertion  - compile-time conformance (var _ X = database.Store(nil))
//	wiring     - struct field, decorator embed, unwrap or constructor plumbing
//	testhelper - exported helper signature used by tests
//	consumer   - a function that takes the wide type as a parameter
var storeReferenceKinds = map[string]string{
	"internal/database/dbtest/invariants.go":                          "testhelper",
	"internal/testutil/integration.go":                                "testhelper",
	"internal/plugins/metafetch/asin_backfill.go":                     "assertion",
	"internal/plugins/maintenance/repairs_ops.go":                     "assertion",
	"internal/plugins/maintenance/move_book_file_rows.go":             "assertion",
	"internal/plugins/maintenance/repoint_book_file_rows.go":          "assertion",
	"internal/plugins/maintenance/fs_regroup_xml.go":                  "assertion",
	"internal/plugins/maintenance/repoint_missing_to_folder_audio.go": "assertion",
	"internal/plugins/maintenance/build_folder_book_files.go":         "assertion",
	"internal/plugins/maintenance/relink_stale_series_fixer.go":       "assertion",
	"internal/server/search_reconciler.go":                            "assertion",
	"internal/maintenance/jobs/merge_chapter_groups.go":               "assertion",
	"internal/plugins/maintenance/deps.go":                            "consumer",
	"internal/server/provider_throttle_wire.go":                       "consumer",
	"internal/server/catalog_harvest_op.go":                           "consumer",
	"cmd/root.go":                                                     "consumer",
	"internal/server/indexed_store.go":                                "wiring",
	"internal/server/server.go":                                       "wiring",
	"internal/operations/registry/register.go":                        "wiring",
	"internal/scanner/scanner.go":                                     "wiring",
}

var storeReferenceRe = regexp.MustCompile(`\bdatabase\.Store\b`)

var storeReferenceSkipDirs = map[string]bool{
	"testdata": true, "vendor": true, "node_modules": true, "mocks": true,
}

func storeReferenceRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the package directory; cannot locate the repo root")
		}
		dir = parent
	}
}

// countStoreReferences returns per-file reference counts (keyed by slash
// path relative to root) and the number of .go files visited.
func countStoreReferences(t *testing.T, root string) (map[string]int, int) {
	t.Helper()
	found := map[string]int{}
	scanned := 0
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, de fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if de.IsDir() {
				if storeReferenceSkipDirs[de.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			name := de.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			if strings.HasPrefix(name, "mock_store") {
				return nil
			}
			scanned++
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
			for sc.Scan() {
				line := sc.Text()
				if strings.HasPrefix(strings.TrimSpace(line), "//") {
					continue
				}
				if storeReferenceRe.MatchString(line) {
					found[rel]++
				}
			}
			return sc.Err()
		})
		if err != nil {
			t.Fatalf("walk %s: %v", top, err)
		}
	}
	return found, scanned
}

func TestStoreReferenceCount(t *testing.T) {
	found, scanned := countStoreReferences(t, storeReferenceRepoRoot(t))
	if scanned < 500 {
		t.Fatalf("scanned only %d .go files, expected at least 500: the walker is not finding the tree", scanned)
	}
	total := 0
	var files []string
	for f, n := range found {
		total += n
		files = append(files, f)
	}
	sort.Strings(files)

	kindTotals := map[string]int{}
	for _, f := range files {
		kind, ok := storeReferenceKinds[f]
		if !ok {
			t.Logf("unclassified reference file (add to storeReferenceKinds): %s (%d)", f, found[f])
			kind = "unclassified"
		}
		kindTotals[kind] += found[f]
	}
	t.Logf("database.Store references: %d in %d files (%d .go files scanned); by kind: %v",
		total, len(files), scanned, kindTotals)

	if total > storeReferenceBaseline {
		t.Fatalf("non-test references to database.Store: %d, baseline is %d. "+
			"Depend on a small role interface instead of the wide type. Offending files:\n  %s\n"+
			"The baseline may only be lowered, never raised.",
			total, storeReferenceBaseline, strings.Join(files, "\n  "))
	}
	if total < storeReferenceBaseline {
		t.Logf("NOTE (not a failure): references are now %d; lower storeReferenceBaseline in this PR", total)
	}
}
