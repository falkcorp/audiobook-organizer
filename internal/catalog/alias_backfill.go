// file: internal/catalog/alias_backfill.go
// version: 1.1.0
// guid: c27d8f40-1a6e-4b93-85d2-e9f0b3a7c514
// last-edited: 2026-10-12

package catalog

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/authority"
	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

const (
	// backfillChunk is how many entries one worker decodes per task.
	backfillChunk = 512
	// aliasWriteBatch is how many alias pairs go to one PutAuthorAliases call.
	aliasWriteBatch = 5000
	// authorityPageSize is the page of the ref_asin: scan.
	authorityPageSize = 1000
)

// AliasBackfill is what one BackfillAuthorAliases pass did.
type AliasBackfill struct {
	// Entries and Names: the entries decoded and the distinct author spellings
	// they credit.
	Entries int
	Names   int
	// SpellingPairs and AuthorityPairs: alias pairs derived from the catalog's
	// own author spellings and from the authority lists' ASIN spellings.
	SpellingPairs  int
	AuthorityPairs int
	// Written: pairs the store wrote or demoted to ambiguous (the rest were
	// already filed).
	Written int
}

// BackfillAuthorAliases files the alias rows of every entry already in the
// catalog (the harvest writes them for the entries it stores from now on) and
// the aliases the authority lists add. It is idempotent and writes only what
// is missing, so the end of every harvest run can call it: it is not an op.
//
//  1. Every entry is decoded from a bounded worker pool (one per CPU) and its
//     author spellings collected; the pairs come from AuthorAliasFolds.
//  2. kv (nil skips this step): a contributor ASIN the authority lists saw
//     under two spellings names one person ("Jane Author" / "Jane Q.
//     Author"). When exactly ONE of the spellings has a block, each other
//     spelling is aliased to it; with none, or with two, nothing is filed.
//     Spellings are re-folded with authorjunk.FoldKey: the authority's own
//     fold keeps accents, a catalog key does not.
//
// The pairs are written in batches, serialised by the store.
//
// It reads the whole catalog, so it is cheap only because it is bounded and
// rare (the end of a harvest run, 30 days apart by default). It runs whether
// or not metadata_fetch.catalog_stage is on, though only the stage reads the
// aliases: the stage can be turned on between two harvests, and would then find
// an empty alias table for every entry stored before this version.
func BackfillAuthorAliases(ctx context.Context, st *database.CatalogStore, kv authority.Scanner) (AliasBackfill, error) {
	var out AliasBackfill
	if st == nil {
		return out, nil
	}
	ids, err := st.AllEntryIDs()
	if err != nil {
		return out, fmt.Errorf("alias backfill: list entries: %w", err)
	}
	out.Entries = len(ids)

	var mu sync.Mutex
	names := map[string]struct{}{}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	for chunk := range slices.Chunk(ids, backfillChunk) {
		g.Go(func() error {
			// A failed or cancelled sibling stops the rest of the pool.
			if err := gctx.Err(); err != nil {
				return err
			}
			local := map[string]struct{}{}
			for _, id := range chunk {
				e, err := st.GetEntry(id)
				if err != nil {
					if errors.Is(err, database.ErrCatalogEntryNotFound) {
						continue
					}
					return err
				}
				for _, a := range e.Authors {
					if strings.TrimSpace(a.Name) != "" {
						local[a.Name] = struct{}{}
					}
				}
			}
			mu.Lock()
			for n := range local {
				names[n] = struct{}{}
			}
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return out, fmt.Errorf("alias backfill: read entries: %w", err)
	}
	out.Names = len(names)

	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	slices.Sort(sorted)
	pairs := aliasPairsForNames(sorted)
	out.SpellingPairs = len(pairs)
	w, err := putAliasBatches(ctx, st, pairs)
	out.Written += w
	if err != nil {
		return out, err
	}

	if kv != nil {
		ap, err := authorityAliasPairs(ctx, st, kv)
		if err != nil {
			return out, err
		}
		out.AuthorityPairs = len(ap)
		w, err := putAliasBatches(ctx, st, ap)
		out.Written += w
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func putAliasBatches(ctx context.Context, st *database.CatalogStore, pairs []database.CatalogAuthorAlias) (int, error) {
	written := 0
	for batch := range slices.Chunk(pairs, aliasWriteBatch) {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n, err := st.PutAuthorAliases(batch)
		written += n
		if err != nil {
			return written, fmt.Errorf("alias backfill: write: %w", err)
		}
	}
	return written, nil
}

// authorityAliasPairs reads the authority lists' contributor-ASIN rows once
// (paged) and returns the aliases they justify. Each page's rows are judged
// from a bounded worker pool (one per CPU): a row costs one exact-key seek
// per spelling, and a library-scale authority list has tens of thousands of
// rows. The result is sorted, so a run's output does not depend on which
// worker finished first.
func authorityAliasPairs(ctx context.Context, st *database.CatalogStore, kv authority.Scanner) ([]database.CatalogAuthorAlias, error) {
	prefix := authority.ASINPrefix + "person:"
	var out []database.CatalogAuthorAlias
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, next, err := kv.ScanPrefixPage(prefix, after, authorityPageSize)
		if err != nil {
			return nil, fmt.Errorf("alias backfill: scan %s: %w", prefix, err)
		}
		var mu sync.Mutex
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(runtime.NumCPU())
		for _, p := range page {
			g.Go(func() error {
				if err := gctx.Err(); err != nil {
					return err
				}
				var ref authority.ASINRef
				if err := json.Unmarshal(p.Value, &ref); err != nil {
					return fmt.Errorf("alias backfill: decode %s: %w", p.Key, err)
				}
				pairs, err := asinRefAliasPairs(st, ref)
				if err != nil {
					return err
				}
				if len(pairs) > 0 {
					mu.Lock()
					out = append(out, pairs...)
					mu.Unlock()
				}
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return nil, err
		}
		if next == "" {
			slices.SortFunc(out, func(a, b database.CatalogAuthorAlias) int {
				return cmp.Or(cmp.Compare(a.Alias, b.Alias), cmp.Compare(a.Target, b.Target))
			})
			return out, nil
		}
		after = next
	}
}

// asinRefAliasPairs judges one contributor-ASIN row: when it names two or
// more spellings and exactly ONE of them has a block, each other spelling is
// aliased to it.
func asinRefAliasPairs(st *database.CatalogStore, ref authority.ASINRef) ([]database.CatalogAuthorAlias, error) {
	if len(ref.Folds) < 2 {
		return nil, nil
	}
	var folds []string
	for _, f := range ref.Folds {
		if k := authorjunk.FoldKey(f); k != "" && !slices.Contains(folds, k) {
			folds = append(folds, k)
		}
	}
	var withBlock []string
	for _, f := range folds {
		ok, err := st.HasAuthorKey(f)
		if err != nil {
			return nil, err
		}
		if ok {
			withBlock = append(withBlock, f)
		}
	}
	if len(withBlock) != 1 {
		return nil, nil
	}
	var out []database.CatalogAuthorAlias
	for _, f := range folds {
		if f != withBlock[0] && len(f) >= minAliasFold {
			out = append(out, database.CatalogAuthorAlias{Alias: f, Target: withBlock[0]})
		}
	}
	return out, nil
}
