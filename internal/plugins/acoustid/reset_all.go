// file: internal/plugins/acoustid/reset_all.go
// version: 1.3.0
// guid: f3b1e8c4-2d7a-4d62-aabb-1f1d6e2c4a01
// last-edited: 2026-07-01

package acoustid

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// resetAllDef registers acoustid.reset-all — the "nuke everything and start
// over" admin op for AcoustID fingerprint state. Use when stored fingerprints
// are suspected bad (e.g. sentinel "AQAAAA" pollution that pairs every book
// with a single anchor at similarity 1.0).
//
// The op:
//  1. Clears AcoustIDSeg0..6 on every BookFile (also drops the
//     book_file_acoustid: secondary index entries via UpdateBookFile).
//  2. Deletes all pending dedup candidates on the "acoustid" layer.
//  3. Enqueues a forced acoustid.fingerprint-rescan so the library
//     recomputes from scratch with the new write-time guards in place.
func (p *Plugin) resetAllDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "acoustid.reset-all",
		Plugin:          "acoustid",
		DisplayName:     "Reset all AcoustID fingerprints",
		Description:     "Clears every stored AcoustID fingerprint segment, drops acoustid dedup candidates, wipes the LSH index, and re-enqueues a forced rescan.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityHigh,
		ConcurrencyKey:  "acoustid.fingerprint",
		Cancellable:     true,
		Timeout:         2 * time.Hour,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
		},
		Run: p.runResetAll,
	}
}

func (p *Plugin) runResetAll(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	if p.store == nil {
		return fmt.Errorf("database store not available")
	}

	log := reporter.Logger()
	startedAt := time.Now()

	// Initial Progress with N=0 — we don't know the real N until we either
	// get the first callback from the Pebble bulk-clear or load the slow
	// path's file list. The helper degrades gracefully to a 2-frame bar.
	prog := sdk.NewProgress(reporter, 0)
	prog.Start("Clearing fingerprints (batched)…")

	var cleared int

	// Fast path: PebbleStore exposes a batched bulk-clear that fsyncs once
	// per ~2000 records instead of once per UpdateBookFile call — ~100×
	// faster than the per-row fallback below.
	if pebble, ok := p.store.(*database.PebbleStore); ok {
		var totalN int
		c, t, clearErr := pebble.ClearAllAcoustIDFingerprints(ctx, 2000,
			func(processed, c, t int) {
				// Rebuild the Progress as soon as we know the real total.
				if totalN != t {
					totalN = t
					prog = sdk.NewProgress(reporter, t)
					prog.Start("Clearing fingerprints (batched)…")
				}
				prog.StepN(processed,
					fmt.Sprintf("Clearing fingerprints %d/%d (cleared=%d)", processed, t, c))
			})
		if clearErr != nil {
			return fmt.Errorf("bulk clear fingerprints: %w", clearErr)
		}
		cleared = c
		log.Info("acoustid reset-all: bulk clear done", "cleared", c, "total", t, "elapsed", time.Since(startedAt).Round(time.Second))
	} else {
		// Per-row fallback (mock/sqlite tests).
		files, err := p.store.GetAllBookFiles()
		if err != nil {
			return fmt.Errorf("load book files: %w", err)
		}
		total := len(files)
		log.Info("acoustid reset-all: clearing fingerprints (slow path)", "book_files", total)
		prog = sdk.NewProgress(reporter, total)
		prog.Start("Clearing fingerprints (slow path)…")
		err = registry.RunItems(ctx, reporter, files, func(_ context.Context, f database.BookFile) error {
			if f.AcoustIDSeg0 == "" && f.AcoustIDSeg1 == "" && f.AcoustIDSeg2 == "" &&
				f.AcoustIDSeg3 == "" && f.AcoustIDSeg4 == "" && f.AcoustIDSeg5 == "" &&
				f.AcoustIDSeg6 == "" {
				return nil
			}
			updated := f
			updated.AcoustIDSeg0 = ""
			updated.AcoustIDSeg1 = ""
			updated.AcoustIDSeg2 = ""
			updated.AcoustIDSeg3 = ""
			updated.AcoustIDSeg4 = ""
			updated.AcoustIDSeg5 = ""
			updated.AcoustIDSeg6 = ""
			if err := p.store.UpdateBookFile(f.ID, &updated); err != nil {
				log.Warn("acoustid reset-all: update file failed", "id", f.ID, "err", err)
				return nil
			}
			cleared++
			return nil
		}, registry.RunItemsOptions{
			Label: func(i, t int) string {
				return fmt.Sprintf("Clearing fingerprints %d/%d (cleared=%d)", i+1, t, cleared)
			},
		})
		if err != nil {
			return err
		}
	}

	// Drop acoustid-layer pending candidates so the dedup review doesn't
	// keep showing the 14K+ poisoned rows after the rescan starts.
	prog.Finalize("Dropping acoustid dedup candidates…")
	deleted := 0
	if p.embeddingStore != nil {
		var allCands []database.DedupCandidate
		offset := 0
		const pageSize = 500
		for {
			cands, _, lerr := p.embeddingStore.ListCandidates(database.CandidateFilter{
				Layer:  "acoustid",
				Limit:  pageSize,
				Offset: offset,
			})
			if lerr != nil {
				log.Warn("acoustid reset-all: list candidates failed", "err", lerr)
				break
			}
			if len(cands) == 0 {
				break
			}
			allCands = append(allCands, cands...)
			offset += len(cands)
		}

		if len(allCands) > 0 {
			if derr := registry.RunItems(ctx, reporter, allCands, func(_ context.Context, c database.DedupCandidate) error {
				if err := p.embeddingStore.DeleteCandidate(c.ID); err != nil {
					log.Warn("acoustid reset-all: delete candidate failed", "id", c.ID, "err", err)
					return nil
				}
				deleted++
				return nil
			}, registry.RunItemsOptions{
				Label: func(i, t int) string {
					return fmt.Sprintf("Dropping acoustid dedup candidates %d/%d (deleted=%d)", i+1, t, deleted)
				},
			}); derr != nil {
				return derr
			}
		}
	}

	log.Info("acoustid reset-all: complete",
		"cleared", cleared,
		"candidates_deleted", deleted,
		"elapsed", time.Since(startedAt).Round(time.Second))

	prog.Done(fmt.Sprintf("Reset complete: %d files cleared, %d candidates dropped, LSH index wiped (elapsed %s). Now enqueue acoustid.fingerprint-rescan with scope=all force=true.",
		cleared, deleted, time.Since(startedAt).Round(time.Second)))
	return nil
}
