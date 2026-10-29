// file: internal/decisions/rowcache_test.go
// version: 1.1.1
// guid: 40be71d2-95a3-4c08-b6e1-7f2d83a9c514
// last-edited: 2026-10-28

package decisions

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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

// The cache holds the veto rows alone, so only a commit that writes (or
// deletes) one of them may invalidate it. A label-only decision (applied,
// undone, approve, skip, auto_applied, a reopen) writes a record, a verdict and
// lookup indexes and must leave the cache warm: the retired-versions apply
// writes one applied record per row, and a cache emptied by each defeats the
// point of sharing the scan across the run.
func TestRowCache_OnlyVetoRowWritesInvalidate(t *testing.T) {
	// A member-shaped reject needs more entities than MaxEdgeSet.
	big := make([]string, MaxEdgeSet+1)
	for i := range big {
		big[i] = fmt.Sprintf("big%02d", i)
	}
	// stage is one write and whether it must invalidate the cache.
	type stage struct {
		name       string
		do         func(*testing.T, *Store)
		invalidate bool
	}
	// The cache is filled over an edge reject, a single-entity (active row)
	// reject and a member reject, so each family has rows to be stale about.
	setup := func(t *testing.T, s *Store) {
		mustRecord(t, s, humanReject("p1", "p2"))
		mustRecord(t, s, rewriteReject(hashOld))
		mustRecord(t, s, humanReject(big...))
	}
	stages := []stage{
		{"approve", func(t *testing.T, s *Store) { mustRecord(t, s, labelInput(DecisionApprove, "a1", "a2")) }, false},
		{"skip", func(t *testing.T, s *Store) { mustRecord(t, s, labelInput(DecisionSkip, "a1", "a2")) }, false},
		{"applied", func(t *testing.T, s *Store) { mustRecord(t, s, labelInput(DecisionApplied, "a1", "a2")) }, false},
		{"undone", func(t *testing.T, s *Store) { mustRecord(t, s, labelInput(DecisionUndone, "a1", "a2")) }, false},
		{"auto_applied", func(t *testing.T, s *Store) { mustRecord(t, s, labelInput(DecisionAutoApplied, "a1", "a2")) }, false},
		{"reopened", func(t *testing.T, s *Store) { mustRecord(t, s, reopenOf(rewriteReject(hashOld), hashNew)) }, false},
		{"label on a rejected pair", func(t *testing.T, s *Store) { mustRecord(t, s, labelInput(DecisionApplied, "p1", "p2")) }, false},
		{"edge reject", func(t *testing.T, s *Store) { mustRecord(t, s, humanReject("e1", "e2")) }, true},
		{"single-entity reject (active row)", func(t *testing.T, s *Store) {
			in := rewriteReject(hashOld)
			in.Scope = "row-2"
			mustRecord(t, s, in)
		}, true},
		{"member reject", func(t *testing.T, s *Store) {
			ids := make([]string, len(big))
			for i := range big {
				ids[i] = "m" + big[i]
			}
			mustRecord(t, s, humanReject(ids...))
		}, true},
		{"unreject (edges and active row deleted)", func(t *testing.T, s *Store) {
			mustRecord(t, s, labelInput(DecisionUnreject, "p1", "p2"))
		}, true},
	}
	for _, st := range stages {
		t.Run(st.name, func(t *testing.T) {
			kv := &closeTrackKV{KV: newKV(t)}
			cache := NewRowCache()
			s := New(kv, WithRowCache(cache))
			setup(t, s)
			load := func() {
				_, err := New(kv, WithRowCache(cache)).Load(context.Background(), nil)
				require.NoError(t, err)
			}
			load()
			warm := kv.opened.Load()
			load()
			require.Equal(t, warm, kv.opened.Load(), "the cache is warm before the stage")

			st.do(t, s)
			load()
			if st.invalidate {
				require.Equal(t, warm+1, kv.opened.Load(), "a write of a veto row must invalidate the cache")
			} else {
				require.Equal(t, warm, kv.opened.Load(), "a label-only commit must keep the cache warm")
			}
		})
	}
}

// writesVetoRows watches vetoRowPrefixes, the list scanRows reads, and nothing
// else: every veto family invalidates on its own (a batch with only that key),
// every label family does not, and every listed family has a reader in
// loadedRows.add.
func TestWritesVetoRows_WatchesTheScannedPrefixes(t *testing.T) {
	require.ElementsMatch(t, []string{prefixActive, prefixEdge, prefixMember}, vetoRowPrefixes)
	for _, prefix := range vetoRowPrefixes {
		require.True(t, writesVetoRows([]database.RawWrite{{Key: prefix + "x"}}), prefix)
		require.True(t, writesVetoRows([]database.RawWrite{{Key: prefix + "x", Delete: true}}), prefix+" delete")
		// A reader exists for the family: a malformed key is refused as corrupt,
		// never as an unknown family.
		err := (&loadedRows{subjects: map[string]rejected{}}).add(prefix, database.KVPair{Key: prefix + "malformed"})
		require.ErrorIs(t, err, ErrCorrupt, prefix)
		require.NotContains(t, err.Error(), "has no reader", prefix)
	}
	require.ErrorContains(t, (&loadedRows{}).add("decision:zz:", database.KVPair{}), "has no reader")
	for _, prefix := range []string{prefixRecord, prefixVerdict, prefixEntity, prefixOp} {
		require.False(t, writesVetoRows([]database.RawWrite{{Key: prefix + "x"}}), prefix)
	}
	require.False(t, writesVetoRows(nil))
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
