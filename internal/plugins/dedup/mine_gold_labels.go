// file: internal/plugins/dedup/mine_gold_labels.go
// version: 1.3.0
// guid: 8f4c2a16-5d70-4e93-bc28-1a6e9d3b7c52
// last-edited: 2026-11-18

// Package dedup — op dedup.mine-gold-labels (dedup tuning dataset, positive miner).
//
// Iterates pending candidates and labels the high-confidence DUPLICATES that the
// rule Classify() does not catch as positives — pairs that share a file hash, an
// AcoustID recording id, or an ASIN/ISBN (with audio on both sides). Each firing
// candidate gets a LabeledExample written with label_source="auto_high_conf" and
// label="true_dup", reusing the candidate's own id (no synthetic rows).
//
// This complements dedup.dataset-backfill (which mines rule-based NEGATIVES) and
// the live human-capture path (merge/dismiss → label_source="human"): together they
// seed the tuning dataset. These auto-mined labels are high precision but NOT human
// gold — the classifier should treat them as weak/strong supervision and validate
// only on human labels.
//
// Dry-run by default: reports counts, writes nothing. apply=true is idempotent
// (UpsertLabeledExample overwrites), so re-running is safe.
package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/dataset"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

type mineGoldLabelsParams struct {
	// Apply, if true, writes the mined true_dup labels. Default false (dry-run).
	Apply bool `json:"apply"`
}

func (p *Plugin) mineGoldLabelsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "dedup.mine-gold-labels",
		Preview:         sdk.PreviewSpec{Params: mineGoldLabelsParams{}},
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "dedup",
		DisplayName:     "Mine high-confidence dedup labels",
		Description:     mineGoldLabelsHelp,
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "dedup.mine-gold-labels",
		Cancellable:     true,
		Timeout:         60 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runMineGoldLabels,
	}
}

// mineGoldLabelsKind is rule R3's native kind for dedup.mine-gold-labels.
const mineGoldLabelsKind = tasks.Kind("books.dedup.mine-gold-labels")

// mineGoldLabelsHelp is HEAD's Description and the native Help (rule R5).
const mineGoldLabelsHelp = "Labels pending candidates that share a file hash, AcoustID recording id, or " +
	"ASIN/ISBN as true_dup (label_source=auto_high_conf), seeding the tuning dataset with " +
	"in-house positive ground truth. Dry-run by default."

// mineGoldLabelsStore is the reads the core takes.
type mineGoldLabelsStore interface {
	ListCandidates(f database.CandidateFilter) ([]database.DedupCandidate, int, error)
	labelBookReader
}

// mineGoldLabelsWriter is the one write the core makes.
type mineGoldLabelsWriter interface {
	UpsertLabeledExample(ex database.LabeledExample) error
}

// mineGoldLabelsEnv is what the core takes from its shell. Live is HEAD's
// apply=true; a dry run never calls Writer, which may then be nil.
type mineGoldLabelsEnv struct {
	kindEnv
	Store  mineGoldLabelsStore
	Writer mineGoldLabelsWriter
}

// mineGoldLabelsResult is the native run's result. HEAD stored none.
type mineGoldLabelsResult struct {
	Examined int `json:"examined"`
	Mined    int `json:"mined"`
	Written  int `json:"written"`
	Errors   int `json:"errors"`
}

// headMineGoldLabels is HEAD's reads and writes for the core: the main store for
// candidates' books and the embedding store for the candidates and the upsert.
type headMineGoldLabels struct {
	emb   *database.EmbeddingStore
	store pluginStore
}

func (h headMineGoldLabels) ListCandidates(f database.CandidateFilter) ([]database.DedupCandidate, int, error) {
	return h.emb.ListCandidates(f)
}
func (h headMineGoldLabels) GetBookByID(id string) (*database.Book, error) {
	return h.store.GetBookByID(id)
}
func (h headMineGoldLabels) GetBookFiles(id string) ([]database.BookFile, error) {
	return h.store.GetBookFiles(id)
}
func (h headMineGoldLabels) UpsertLabeledExample(ex database.LabeledExample) error {
	return h.emb.UpsertLabeledExample(ex)
}

// mineGoldLabelsCore is the body of runMineGoldLabels, shared by both shells.
func mineGoldLabelsCore(ctx context.Context, env mineGoldLabelsEnv) (mineGoldLabelsResult, error) {
	env.Logger.Info("mine-gold-labels start", "apply", env.Live)

	env.Progress(0, 2, "Loading pending candidates…")
	cands, _, err := env.Store.ListCandidates(database.CandidateFilter{Status: "pending", Limit: 1_000_000})
	if err != nil {
		return mineGoldLabelsResult{}, fmt.Errorf("list candidates: %w", err)
	}
	env.Logger.Info("mine-gold-labels: candidates loaded", "count", len(cands))

	// Book-lookup memoization: a book referenced by many candidate rows was
	// re-read from the store on every occurrence (up to 4 reads/candidate with
	// no reuse). Mirrors the bookCache pattern in drain_stale.go /
	// server_maintenance_deps.go, wrapped in a mutex since the cache is shared
	// across the pool below (CONC-8).
	adapter := newLabelBookMemo(env.Store)

	// examined/mined/written/errs are incremented from every worker goroutine
	// below, so they're guarded by statsMu (the candidate processing itself --
	// the store reads/writes -- is independent per candidate and needs no lock).
	var (
		statsMu                        sync.Mutex
		examined, mined, written, errs int
	)

	env.Progress(1, 2, fmt.Sprintf("Mining %d candidates…", len(cands)))
	err = env.Items(ctx, tasks.CPU(), len(cands), kindItemsOptions{
		PureLabel: func(i, total int) string { return fmt.Sprintf("Mined %d/%d…", i+1, total) },
	}, func(ctx context.Context, i int) error {
		c := cands[i]
		if env.Canceled() {
			return context.Canceled
		}

		statsMu.Lock()
		examined++
		statsMu.Unlock()

		a, aErr := adapter.GetBook(c.EntityAID)
		b, bErr := adapter.GetBook(c.EntityBID)
		if aErr != nil || bErr != nil || a == nil || b == nil {
			statsMu.Lock()
			errs++
			statsMu.Unlock()
			return nil
		}
		aFiles, afErr := adapter.GetBookFiles(c.EntityAID)
		bFiles, bfErr := adapter.GetBookFiles(c.EntityBID)
		if afErr != nil || bfErr != nil {
			statsMu.Lock()
			errs++
			statsMu.Unlock()
			return nil
		}

		label, reason, fires := dataset.MineHighConfidenceDup(a, b, aFiles, bFiles)
		if !fires {
			return nil
		}
		statsMu.Lock()
		mined++
		statsMu.Unlock()

		if env.Live {
			ex, buildErr := dataset.BuildExample(adapter, c)
			if buildErr != nil {
				statsMu.Lock()
				errs++
				statsMu.Unlock()
				env.Logger.Warn("mine-gold-labels: build example failed", "candidate_id", c.ID, "error", buildErr)
				return nil
			}
			ex.Label = label
			ex.LabelSource = "auto_high_conf"
			ex.LabelReason = reason
			ex.DecidedAt = time.Now().UTC().Format(time.RFC3339)
			if err := env.Writer.UpsertLabeledExample(ex); err != nil {
				statsMu.Lock()
				errs++
				statsMu.Unlock()
				env.Logger.Error("mine-gold-labels: upsert error", "candidate_id", c.ID, "error", err)
				return nil
			}
			statsMu.Lock()
			written++
			statsMu.Unlock()
		}
		return nil
	})
	if err != nil {
		return mineGoldLabelsResult{}, err
	}

	summary := fmt.Sprintf("examined=%d mined=%d written=%d errs=%d (apply=%v)", examined, mined, written, errs, env.Live)
	env.Logger.Info("mine-gold-labels complete", "summary", summary)
	if !env.Live {
		env.Progress(2, 2, fmt.Sprintf("Dry-run — %d of %d candidates are high-confidence true_dup. Pass apply=true to write.", mined, examined))
	} else {
		env.Progress(2, 2, fmt.Sprintf("Complete — wrote %d true_dup labels. %s", written, summary))
	}
	return mineGoldLabelsResult{Examined: examined, Mined: mined, Written: written, Errors: errs}, nil
}

// runMineGoldLabels is the v2 shell: HEAD's Run (nil checks, params) over
// mineGoldLabelsCore.
func (p *Plugin) runMineGoldLabels(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	if p.embeddingStore == nil {
		return fmt.Errorf("embedding store not available")
	}
	if p.store == nil {
		return fmt.Errorf("main store not available")
	}

	var params mineGoldLabelsParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	h := headMineGoldLabels{emb: p.embeddingStore, store: p.store}
	_, err := mineGoldLabelsCore(ctx, mineGoldLabelsEnv{kindEnv: headEnv(reporter, params.Apply), Store: h, Writer: h})
	return err
}
