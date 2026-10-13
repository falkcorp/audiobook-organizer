// file: internal/catalog/alias_backfill.go
// version: 1.0.0
// guid: c27d8f40-1a6e-4b93-85d2-e9f0b3a7c514
// last-edited: 2026-10-12

package catalog

import (
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
//     under two spellings names one person ("Jonathan Brazee" / "Jonathan P.
//     Brazee"). When exactly ONE of the spellings has a block, each other
//     spelling is aliased to it; with none, or with two, nothing is filed.
//     Spellings are re-folded with authorjunk.FoldKey: the authority's own
//     fold keeps accents, a catalog key does not.
//
// The pairs are written in batches, serialised by the store.
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
	var g errgroup.Group
	g.SetLimit(runtime.NumCPU())
	for chunk := range slices.Chunk(ids, backfillChunk) {
		g.Go(func() error {
			if err := ctx.Err(); err != nil {
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
// (paged) and returns the aliases they justify.
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
		for _, p := range page {
			var ref authority.ASINRef
			if err := json.Unmarshal(p.Value, &ref); err != nil {
				return nil, fmt.Errorf("alias backfill: decode %s: %w", p.Key, err)
			}
			if len(ref.Folds) < 2 {
				continue
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
				continue
			}
			for _, f := range folds {
				if f != withBlock[0] && len(f) >= minAliasFold {
					out = append(out, database.CatalogAuthorAlias{Alias: f, Target: withBlock[0]})
				}
			}
		}
		if next == "" {
			return out, nil
		}
		after = next
	}
}
