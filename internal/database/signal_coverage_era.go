// file: internal/database/signal_coverage_era.go
// version: 1.0.0
// guid: be27cd9f-64d3-43d8-86f2-ebe054adf7f5
// last-edited: 2026-09-19

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"golang.org/x/sync/errgroup"
)

// BookSignatureEraCoverage counts live (not soft-deleted) books by the era of
// their book signature: CurrentEra is Book.HasCurrentBookSig (non-empty
// BookSigV1 at BookSigVersion >= fingerprint.BookSignatureVersion); LegacyEra
// is any other non-empty signature. CurrentEra + LegacyEra == WithSignature
// and WithSignature + NoSignature == LiveBooks.
//
// InlineUnmigrated counts live books whose signature was read off the book:
// row because no book_sig: sidecar exists yet (hydrateBookSig is
// fallback-first, so those books still use it).
type BookSignatureEraCoverage struct {
	SignatureVersion int   `json:"signature_version"`
	LiveBooks        int64 `json:"live_books"`
	WithSignature    int64 `json:"with_signature"`
	CurrentEra       int64 `json:"current_era"`
	LegacyEra        int64 `json:"legacy_era"`
	NoSignature      int64 `json:"no_signature"`
	InlineUnmigrated int64 `json:"inline_unmigrated"`
}

// sigEra is one book's signature state: none, legacy or current.
type sigEra uint8

const (
	sigNone sigEra = iota
	sigLegacy
	sigCurrent
)

func sigEraOf(v1 presence, version *int) sigEra {
	if !v1 {
		return sigNone
	}
	if version != nil && *version >= fingerprint.BookSignatureVersion {
		return sigCurrent
	}
	return sigLegacy
}

type sigRowJob struct {
	id  string
	val []byte
}

// decodeRowsParallel runs decode over rows produced by walk, across a bounded
// pool of workers. walk runs on one goroutine and hands over value COPIES in
// batches, so at most about (workers+2)*batch values are in flight.
func decodeRowsParallel(ctx context.Context, workers int, walk func(emit func(id string, val []byte) error) error, decode func(worker int, id string, val []byte) error) error {
	const batchSize = 128
	batches := make(chan []sigRowJob, 1)
	g, gctx := errgroup.WithContext(ctx)
	for w := range workers {
		g.Go(func() error {
			for batch := range batches {
				for _, j := range batch {
					if err := decode(w, j.id, j.val); err != nil {
						return err
					}
				}
			}
			return nil
		})
	}
	g.Go(func() error {
		defer close(batches)
		var batch []sigRowJob
		flush := func() error {
			select {
			case batches <- batch:
				batch = nil
				return nil
			case <-gctx.Done():
				return gctx.Err()
			}
		}
		if err := walk(func(id string, val []byte) error {
			batch = append(batch, sigRowJob{id: id, val: append([]byte(nil), val...)})
			if len(batch) == batchSize {
				return flush()
			}
			return nil
		}); err != nil {
			return err
		}
		if len(batch) > 0 {
			return flush()
		}
		return nil
	})
	return g.Wait()
}

// CountBookSignatureEras is a census of every book row and every book_sig:
// sidecar, read from Pebble (memdb strips all five signature fields). The
// sidecar is authoritative where it exists, exactly as hydrateBookSig reads
// it. Decoding runs on a bounded pool; the two key walks are one iterator each.
func (p *PebbleStore) CountBookSignatureEras(ctx context.Context, workers int) (*BookSignatureEraCoverage, error) {
	if workers < 1 {
		workers = runtime.NumCPU()
	}
	// A full book-row read: it shares the single deep-coverage slot.
	if !p.TryAcquireDeepCoverageScan() {
		return nil, ErrDeepCoverageBusy
	}
	defer p.ReleaseDeepCoverageScan()

	// Pass 1: sidecars -> era per book ID. Per-worker maps, merged after.
	sideParts := make([]map[string]sigEra, workers)
	for i := range sideParts {
		sideParts[i] = make(map[string]sigEra)
	}
	err := decodeRowsParallel(ctx, workers,
		func(emit func(string, []byte) error) error {
			return forEachKeyInRange(p.db, []byte(bookSigKeyPrefix), prefixEnd([]byte(bookSigKeyPrefix)), func(k, v []byte) error {
				return emit(strings.TrimPrefix(string(k), bookSigKeyPrefix), v)
			})
		},
		func(w int, id string, val []byte) error {
			var row struct {
				V1      presence `json:"v1"`
				Version *int     `json:"version"`
			}
			if err := json.Unmarshal(val, &row); err != nil {
				return fmt.Errorf("decode book_sig %s: %w", id, err)
			}
			sideParts[w][id] = sigEraOf(row.V1, row.Version)
			return nil
		})
	if err != nil {
		return nil, err
	}
	sidecars := make(map[string]sigEra)
	for _, m := range sideParts {
		for id, e := range m {
			sidecars[id] = e
		}
	}

	// Pass 2: live book rows; the sidecar map is read-only from here on.
	accs := make([]BookSignatureEraCoverage, workers)
	err = decodeRowsParallel(ctx, workers,
		func(emit func(string, []byte) error) error {
			return forEachBookRow(p.db, func(id string, v []byte) error { return emit(id, v) })
		},
		func(w int, id string, val []byte) error {
			var row struct {
				MarkedForDeletion *bool    `json:"marked_for_deletion"`
				V1                presence `json:"book_sig_v1"`
				Version           *int     `json:"book_sig_version"`
			}
			if err := json.Unmarshal(val, &row); err != nil {
				return fmt.Errorf("decode book %s: %w", id, err)
			}
			if markedForDeletionFlag(row.MarkedForDeletion) {
				return nil
			}
			a := &accs[w]
			a.LiveBooks++
			era, ok := sidecars[id]
			if !ok {
				era = sigEraOf(row.V1, row.Version)
				if era != sigNone {
					a.InlineUnmigrated++
				}
			}
			switch era {
			case sigCurrent:
				a.CurrentEra++
			case sigLegacy:
				a.LegacyEra++
			default:
				a.NoSignature++
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	out := &BookSignatureEraCoverage{SignatureVersion: fingerprint.BookSignatureVersion}
	for _, a := range accs {
		out.LiveBooks += a.LiveBooks
		out.CurrentEra += a.CurrentEra
		out.LegacyEra += a.LegacyEra
		out.NoSignature += a.NoSignature
		out.InlineUnmigrated += a.InlineUnmigrated
	}
	out.WithSignature = out.CurrentEra + out.LegacyEra
	return out, nil
}
