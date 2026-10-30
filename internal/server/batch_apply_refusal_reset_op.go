// file: internal/server/batch_apply_refusal_reset_op.go
// version: 1.0.0
// guid: 23baacd9-d09b-44cf-a9e7-791961ada5b1
// last-edited: 2026-10-30
//
// metadata.batch-apply-refusals-reset forgets the structural refusals that
// metadata.batch-apply-cached remembers (batch_apply_refusal.go), so the next
// batch evaluates those books again.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// batchApplyRefusalsResetOpID is the op that clears the apply_refusal: records.
const batchApplyRefusalsResetOpID = "metadata.batch-apply-refusals-reset"

// batchApplyRefusalsResetPage bounds the memory of one scan page and the size
// of one delete batch (the keyspace grows with the number of refused books).
const batchApplyRefusalsResetPage = 500

// batchApplyRefusalsResetParams is the params of the reset op.
type batchApplyRefusalsResetParams struct {
	// DryRun / DryRunCamel are the two spellings of the mode; omitted is a
	// preview (opmode.ResolveDryRun).
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
	// BookIDs limits the reset to these books. Empty forgets every record.
	BookIDs []string `json:"book_ids,omitempty"`
}

// resetApplyRefusals counts, and unless dryRun deletes, the refusal records of
// bookIDs (all of them when empty). It returns how many records matched.
func resetApplyRefusals(ctx context.Context, kv database.RawKVStore, bookIDs []string, dryRun bool) (int, error) {
	if len(bookIDs) > 0 {
		keys := make([]string, 0, len(bookIDs))
		for _, id := range bookIDs {
			if id = strings.TrimSpace(id); id != "" {
				keys = append(keys, applyRefusalKey(id))
			}
		}
		// Count only keys that exist, so a dry run and a live run agree.
		present := keys[:0]
		for _, k := range keys {
			if raw, err := kv.GetRaw(k); err != nil {
				return 0, err
			} else if len(raw) > 0 {
				present = append(present, k)
			}
		}
		if !dryRun && len(present) > 0 {
			if err := kv.DeleteRawBatch(present); err != nil {
				return 0, err
			}
		}
		return len(present), nil
	}
	n, after := 0, ""
	for {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		pairs, next, err := kv.ScanPrefixPage(applyRefusalKeyPrefix, after, batchApplyRefusalsResetPage)
		if err != nil {
			return n, err
		}
		keys := make([]string, 0, len(pairs))
		for _, kvp := range pairs {
			keys = append(keys, kvp.Key)
		}
		if !dryRun && len(keys) > 0 {
			if err := kv.DeleteRawBatch(keys); err != nil {
				return n, err
			}
		}
		n += len(keys)
		if next == "" {
			return n, nil
		}
		after = next
	}
}

// RegisterBatchApplyRefusalsResetOp registers metadata.batch-apply-refusals-reset.
// It is a sibling op rather than a param of metadata.batch-apply-cached because
// that op's params travel through MergeQueuedParams and the checkpoint overlay,
// where a "reset" key would survive a resume and change what a restarted batch
// does.
func (s *Server) RegisterBatchApplyRefusalsResetOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              batchApplyRefusalsResetOpID,
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "metadata",
		DisplayName:     "Forget Remembered Apply Refusals",
		Description:     "Forget the structural refusals the cached-metadata batch apply remembers, so those books are evaluated again. dry_run (default) only counts them; book_ids limits the reset.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  batchApplyRefusalsResetOpID,
		Preview:         opsregistry.PreviewSpec{Params: batchApplyRefusalsResetParams{}},
		Permissions:     []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, raw json.RawMessage, reporter opsregistry.Reporter) error {
			var p batchApplyRefusalsResetParams
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &p); err != nil {
					return fmt.Errorf("%s: invalid params: %w", batchApplyRefusalsResetOpID, err)
				}
			}
			dryRun, err := opmode.ResolveDryRun(batchApplyRefusalsResetOpID, p.DryRun, p.DryRunCamel)
			if err != nil {
				return err
			}
			if s.store == nil {
				return fmt.Errorf("%s: database not initialized", batchApplyRefusalsResetOpID)
			}
			n, err := resetApplyRefusals(ctx, s.store, p.BookIDs, dryRun)
			if err != nil {
				return fmt.Errorf("%s: %w", batchApplyRefusalsResetOpID, err)
			}
			verb := "forgot"
			if dryRun {
				verb = "would forget"
			}
			msg := fmt.Sprintf("%s %d remembered apply refusals", verb, n)
			_ = reporter.Log(slog.LevelInfo, msg, slog.Int("count", n), slog.Bool("dry_run", dryRun))
			return registryProgressAdapter{r: reporter}.UpdateProgress(1, 1, "complete: "+msg)
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterBatchApplyRefusalsResetOp(reg) })
}
