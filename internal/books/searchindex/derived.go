// file: internal/books/searchindex/derived.go
// version: 0.1.0
// guid: c633544f-c659-4b7e-a73f-c5c4bf192078
// last-edited: 2026-11-20

package searchindex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/falkcorp/mediacore/storage"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// Derived index names under the books module (each satisfies keys.ValidName).
// "books" is the full-text index the engine builds from NewSpec; the other three
// are app-level indexes books keeps itself (spec 05-storage 5S.3.8).
const (
	DerivedBooks   = "books"
	DerivedMemDB   = "memdb"
	DerivedHNSW    = "hnsw"
	DerivedChromem = "chromem"
)

// Derived is one declared derived index with the hooks that warm-start it.
type Derived struct {
	Index storage.DerivedIndex
	Hooks storage.DerivedHooks
}

// HNSWArtifact is the part of the HNSW embedding store the artifact hooks use;
// *store.HNSWEmbeddingStore satisfies it.
type HNSWArtifact interface {
	ExportWithTruth(dir string, truth func(entityType string) (int, error)) error
	ImportCountChecked(dir string, truth func(entityType string) (int, error)) error
	NeedsReconcile() bool
	DiscardForRebuild()
	CountByType(ctx context.Context, entityType string) (int, error)
}

var _ HNSWArtifact = (*database.HNSWEmbeddingStore)(nil)

// HNSWConfig wires the hnsw hooks. Truth is the row count per entity type the
// graph is judged against (HEAD's server_lifecycle.go importHNSWSnapshot reads
// it from the store); a snapshot exported with a count the store no longer
// matches is refused.
type HNSWConfig struct {
	Store HNSWArtifact
	Truth func(entityType string) (int, error)
}

// Catalog is books' Spec.Derived declarations and their hooks, in the order the
// fold registers them.
//
// Only hnsw has an artifact: the memdb is rebuilt from the store's rows at every
// boot (its warmup) and chromem persists itself as it is written, so neither has
// a snapshot to stamp or load. Their zero Hooks mean "no warm start": the kernel
// rebuilds them through their build task (<module>.derived.<name>.build, which
// the fold supplies from the existing warmup and hydrate code).
//
// Blocking is a decision the fold confirms (seams.d/F7-09-T6.md): memdb answers
// every list read, so readiness waits for it; the three searches degrade to
// their fallbacks meanwhile and do not.
func Catalog(h HNSWConfig) []Derived {
	return []Derived{
		{Index: storage.DerivedIndex{Name: DerivedBooks, Version: MappingVersion}},
		{Index: storage.DerivedIndex{Name: DerivedMemDB, Version: "1", Blocking: true}},
		{Index: storage.DerivedIndex{Name: DerivedHNSW, Version: "1"}, Hooks: HNSWHooks(h)},
		{Index: storage.DerivedIndex{Name: DerivedChromem, Version: "1"}},
	}
}

// DerivedIndexes is Catalog's declarations alone: the value of the books
// manifest's Spec.Derived.
func DerivedIndexes() []storage.DerivedIndex {
	cat := Catalog(HNSWConfig{})
	out := make([]storage.DerivedIndex, len(cat))
	for i, d := range cat {
		out[i] = d.Index
	}
	return out
}

// HNSWHooks are the hnsw warm-start hooks.
//
// Snapshot follows the artifact rule (storage.StampArtifact): gen was read
// before the call; the old stamp is removed first so a crash mid-export leaves
// an artifact nothing vouches for; the graph is exported with its truth counts;
// and only then is gen stamped. An incomplete graph exports nothing and stamps
// nothing.
//
// Load refuses unless the artifact's stamp equals current AND the store's own
// count check passes: the import must find every entity type's recorded row count
// unchanged, install the graph complete (not pending a reconcile) and leave it
// non-empty. Whenever it refuses after importing, the graph is discarded so a
// half-trusted snapshot never serves.
func HNSWHooks(c HNSWConfig) storage.DerivedHooks {
	return storage.DerivedHooks{
		Snapshot: func(ctx context.Context, dir string, gen storage.Generation) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if c.Store == nil {
				return errors.New("searchindex: hnsw snapshot: no store")
			}
			if gen.IsZero() {
				return errors.New("searchindex: hnsw snapshot: zero generation")
			}
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return fmt.Errorf("searchindex: hnsw snapshot: %w", err)
			}
			if err := os.Remove(filepath.Join(dir, storage.ArtifactGenerationFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("searchindex: hnsw snapshot: clear old stamp: %w", err)
			}
			if err := c.Store.ExportWithTruth(dir, c.Truth); err != nil {
				return fmt.Errorf("searchindex: hnsw snapshot: %w", err)
			}
			return storage.StampArtifact(dir, gen)
		},
		Load: func(ctx context.Context, dir string, current storage.Generation) (bool, error) {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			if c.Store == nil {
				return false, errors.New("searchindex: hnsw load: no store")
			}
			ok, err := storage.ArtifactAccepted(dir, current)
			if err != nil || !ok {
				return false, err
			}
			var consulted []string
			truth := func(entityType string) (int, error) {
				consulted = append(consulted, entityType)
				if c.Truth == nil {
					return 0, errors.New("no truth source")
				}
				return c.Truth(entityType)
			}
			if err := c.Store.ImportCountChecked(dir, truth); err != nil {
				if errors.Is(err, database.ErrNoHNSWSnapshot) {
					return false, nil
				}
				c.Store.DiscardForRebuild()
				return false, fmt.Errorf("searchindex: hnsw load: %w", err)
			}
			if c.Store.NeedsReconcile() || !nonEmpty(ctx, c.Store, consulted) {
				c.Store.DiscardForRebuild()
				return false, nil
			}
			return true, nil
		},
	}
}

// nonEmpty reports whether the imported graph holds anything for the entity
// types the import consulted. The store discards an inconsistent stale snapshot
// by installing an empty graph and returning nil, which only this check tells
// apart from a good import.
func nonEmpty(ctx context.Context, s HNSWArtifact, types []string) bool {
	for _, t := range types {
		if n, err := s.CountByType(ctx, t); err == nil && n > 0 {
			return true
		}
	}
	return false
}
