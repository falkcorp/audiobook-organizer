// file: internal/decisions/rowcache_test.go
// version: 1.0.0
// guid: 40be71d2-95a3-4c08-b6e1-7f2d83a9c514
// last-edited: 2026-10-24

package decisions

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// With a RowCache the log is scanned once while nothing is written; a record
// makes the next Load scan again, so a reject made after a plan is seen.
func TestRowCache_ScansOnceUntilTheLogChanges(t *testing.T) {
	kv := &closeTrackKV{KV: newKV(t)}
	cache := NewRowCache()
	w := New(kv, WithRowCache(cache))
	mustRecord(t, w, humanReject("b1", "b2"))

	load := func() *Vetoes {
		// A fresh Store each time, as a fresh reader is built for every run.
		v, err := New(kv, WithRowCache(cache)).Load(context.Background(), nil)
		require.NoError(t, err)
		return v
	}
	v := load()
	require.True(t, snapVetoed(t, v, testType, "b1", "b2", FamilySameWork))
	before := kv.opened.Load()
	v = load()
	require.True(t, snapVetoed(t, v, testType, "b1", "b2", FamilySameWork))
	require.Equal(t, before, kv.opened.Load(), "no record was written: the second Load reads no log")

	// A reject written through another Store over the same engine (the
	// handler's) is seen by the very next Load.
	mustRecord(t, New(kv), humanReject("b3", "b4"))
	v = load()
	require.True(t, snapVetoed(t, v, testType, "b3", "b4", FamilySameWork), "a reject recorded after the cache was filled")
	require.Equal(t, before+1, kv.opened.Load())

	// A different engine never reads this one's rows.
	other := New(newKV(t), WithRowCache(cache))
	ov, err := other.Load(context.Background(), nil)
	require.NoError(t, err)
	require.False(t, snapVetoed(t, ov, testType, "b1", "b2", FamilySameWork))
}

// Only the scan is cached: ids are resolved afresh by every Load, so a book
// merged or retired between two Loads is seen by the second.
func TestRowCache_StoredIDsAreResolvedAfreshEveryLoad(t *testing.T) {
	kv := newKV(t)
	cache := NewRowCache()
	s := New(kv, WithRowCache(cache))
	rejectPair(t, s, "S", "Y")

	v1, err := s.LoadPaths(context.Background(), pathMap(nil, nil))
	require.NoError(t, err)
	require.False(t, snapVetoed(t, v1, testType, "S2", "Y", FamilySameWork), "S2 is not S yet")

	// S is merged into S2 between the two Loads: no record was written.
	v2, err := s.LoadPaths(context.Background(), pathMap(map[string][]string{"S": {"S", "S2"}}, nil))
	require.NoError(t, err)
	require.True(t, snapVetoed(t, v2, testType, "S2", "Y", FamilySameWork), "the cached rows resolve through today's book state")
}

// Every write of the decision keys goes through Store.commit, which counts it
// for the RowCache. A second writer would leave a cache stale (a dropped
// owner reject), so the package has exactly one call to ApplyRawBatch, and no
// other package writes a decision: key.
func TestDecisionKeysHaveOneWriter(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)

	apply := regexp.MustCompile(`\.ApplyRawBatch\(`)
	var inPackage int
	require.NoError(t, filepath.WalkDir(filepath.Join(root, "internal", "decisions"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, line := range strings.Split(string(src), "\n") {
			if trimmed := strings.TrimSpace(line); apply.MatchString(line) && !strings.HasPrefix(trimmed, "//") {
				inPackage++
				require.Equal(t, "store.go", filepath.Base(path), "ApplyRawBatch outside store.go")
			}
		}
		return nil
	}))
	require.Equal(t, 1, inPackage, "decisions writes its keys in Store.commit and nowhere else")

	// Outside the package, no production code names a decision: key but the
	// key-family registry.
	allowed := map[string]bool{
		filepath.Join("internal", "decisions", "keys.go"):       true,
		filepath.Join("internal", "database", "keyfamilies.go"): true,
	}
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "web", ".worktrees", ".standards", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || allowed[rel] {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		require.NotContains(t, string(src), `"decision:`, "%s writes or reads a decision: key outside internal/decisions", rel)
		return nil
	}))
}
