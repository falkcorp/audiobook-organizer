// file: internal/plugins/dedup/split_book_scan.go
// version: 1.3.0
// guid: 4c6e8f0b-3a5b-7c9d-1e2f-4a6b8c0d2e3f
// last-edited: 2026-11-18

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// splitBookScanKind is rule R3's native kind for dedup.split-book-scan.
const splitBookScanKind = tasks.Kind("books.dedup.split-book-scan")

// splitBookScanHelp is HEAD's Description, shared by both definitions (rule R5).
const splitBookScanHelp = "Scans the library for one-Book-per-chapter clusters and saves merge candidates."

// splitBookScanDef registers the split-book backfill scan op. It walks
// every book in the store, runs the chapter-cluster detector, and
// persists the resulting candidates to the Pebble split-book keyspace
// so the HTTP list endpoint and the merge-split-books CLI can consume
// them.
func (p *Plugin) splitBookScanDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "dedup.split-book-scan",
		Liveness:        sdk.LivenessManual,
		Plugin:          "dedup",
		DisplayName:     "Split-book backfill scan",
		Description:     splitBookScanHelp,
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "dedup.split-book-scan",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         60 * time.Minute,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
		},
		Run: p.runSplitBookScan,
	}
}

// splitBookScanReads is the one read the scan core makes: the detector over the
// main store (it reads books and authors).
type splitBookScanReads interface {
	DetectSplitBookCandidates(ctx context.Context, progress func(loaded int)) ([]dedupengine.SplitBookCandidate, error)
}

// splitBookScanWriter is the one write the scan core makes: replace every saved
// candidate row with the new list (split_book_candidate: rows).
type splitBookScanWriter interface {
	SaveAll(cands []dedupengine.SplitBookCandidate) error
}

// splitBookScanEnv is what the core takes from its shell. OpenWriter is called
// after detection, as HEAD checked the shared Pebble handle only once the books
// were scanned.
type splitBookScanEnv struct {
	kindEnv
	Reads      splitBookScanReads
	OpenWriter func() (splitBookScanWriter, error)
}

// splitBookScanResult is the native run's result.
type splitBookScanResult struct {
	Candidates int `json:"candidates"`
}

// splitBookScanCore is the body of runSplitBookScan, shared by both shells.
func splitBookScanCore(ctx context.Context, env splitBookScanEnv) (splitBookScanResult, error) {
	scanProg := newProgress(env.kindEnv, 0)
	scanProg.Start("Scanning library for split-book clusters...")
	// Book-loading progress: total is unknown up front, so the callback
	// updates the indeterminate frame's message (and the op log) every
	// 1000 books instead of a percentage.
	cands, err := env.Reads.DetectSplitBookCandidates(ctx, func(loaded int) {
		scanProg.StepN(0, fmt.Sprintf("Loading books: %d loaded...", loaded))
		if loaded%10000 == 0 {
			env.Logger.Info("split-book scan: loading books", "loaded", loaded)
		}
	})
	if err != nil {
		env.Logger.Error("split-book detector error", "error", err)
		return splitBookScanResult{}, fmt.Errorf("split-book scan: %w", err)
	}

	prog := newProgress(env.kindEnv, len(cands))
	prog.Start(fmt.Sprintf("Found %d candidate cluster(s); persisting...", len(cands)))

	store, err := env.OpenWriter()
	if err != nil {
		return splitBookScanResult{}, err
	}
	if err := store.SaveAll(cands); err != nil {
		env.Logger.Error("split-book SaveAll error", "error", err)
		return splitBookScanResult{}, fmt.Errorf("split-book scan persist: %w", err)
	}

	prog.StepN(len(cands), fmt.Sprintf("Persisted %d / %d cluster(s)", len(cands), len(cands)))
	prog.Finalize("writing results...")
	prog.Done(fmt.Sprintf("Split-book scan complete — %d candidate cluster(s) saved", len(cands)))
	env.Logger.Info("split-book scan complete", "candidates", len(cands))
	return splitBookScanResult{Candidates: len(cands)}, nil
}

// headSplitReads is HEAD's reads for the split-book cores: the detector and the
// merge-time recheck over the plugin's main store.
type headSplitReads struct{ store pluginStore }

func (h headSplitReads) DetectSplitBookCandidates(ctx context.Context, progress func(loaded int)) ([]dedupengine.SplitBookCandidate, error) {
	return dedupengine.DetectSplitBookCandidates(ctx, h.store, progress)
}

func (h headSplitReads) RecheckSplitBookMembers(bookIDs []string, chapterMaxSec int) ([]string, error) {
	return dedupengine.RecheckSplitBookMembers(h.store, bookIDs, chapterMaxSec)
}

// headSplitWriter is HEAD's writes for the split-book cores: the candidate rows
// in the shared Pebble DB and the merge over the main store.
type headSplitWriter struct {
	store      pluginStore
	candidates *dedupengine.SplitBookStore
}

func (h headSplitWriter) SaveAll(cands []dedupengine.SplitBookCandidate) error {
	return h.candidates.SaveAll(cands)
}

func (h headSplitWriter) Delete(candidateID string) error { return h.candidates.Delete(candidateID) }

func (h headSplitWriter) MergeSplitBookClusterRechecked(bookIDs []string, keepID, suggestedTitle, opID string) (*dedupengine.SplitBookMergeResult, error) {
	return dedupengine.MergeSplitBookClusterRechecked(h.store, bookIDs, keepID, suggestedTitle,
		dedupengine.SplitMergeOptions{OperationID: opID})
}

func (p *Plugin) runSplitBookScan(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	if p.store == nil {
		return fmt.Errorf("split-book scan: store not available")
	}
	if p.embeddingStore == nil {
		return fmt.Errorf("split-book scan: embedding store not available (needed for shared Pebble DB)")
	}
	_, err := splitBookScanCore(ctx, splitBookScanEnv{
		kindEnv: headEnv(reporter, true),
		Reads:   headSplitReads{store: p.store},
		OpenWriter: func() (splitBookScanWriter, error) {
			db := p.embeddingStore.PebbleDB()
			if db == nil {
				return nil, fmt.Errorf("split-book scan: shared Pebble DB unavailable")
			}
			return headSplitWriter{store: p.store, candidates: dedupengine.NewSplitBookStore(db)}, nil
		},
	})
	return err
}
