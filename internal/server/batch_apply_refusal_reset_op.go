// file: internal/server/batch_apply_refusal_reset_op.go
// version: 1.1.0
// guid: 23baacd9-d09b-44cf-a9e7-791961ada5b1
// last-edited: 2026-11-18
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

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
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

// batchApplyRefusalsResetStore is the store reads the reset makes: one key read
// per named book, or a paged scan of the refusal keyspace.
type batchApplyRefusalsResetStore interface {
	GetRaw(key string) ([]byte, error)
	ScanPrefixPage(prefix, after string, limit int) (pairs []database.KVPair, next string, err error)
}

// batchApplyRefusalsResetWriter is the one write the reset makes.
type batchApplyRefusalsResetWriter interface {
	DeleteRawBatch(keys []string) error
}

// resetApplyRefusals counts, and unless dryRun deletes, the refusal records of
// bookIDs (all of them when empty). It returns how many records matched.
func resetApplyRefusals(ctx context.Context, kv database.RawKVStore, bookIDs []string, dryRun bool) (int, error) {
	return resetApplyRefusalsVia(ctx, kv, kv, bookIDs, dryRun)
}

// resetApplyRefusalsVia is resetApplyRefusals with its reads and its one write
// handed in, so the shared core reaches them through its shell (rule R10).
func resetApplyRefusalsVia(ctx context.Context, kv batchApplyRefusalsResetStore, w batchApplyRefusalsResetWriter, bookIDs []string, dryRun bool) (int, error) {
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
			if err := w.DeleteRawBatch(present); err != nil {
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
			if err := w.DeleteRawBatch(keys); err != nil {
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

// batchApplyRefusalsResetEnv is what the shared core takes from its shell
// (rules R2 and R10).
type batchApplyRefusalsResetEnv struct {
	t7Env
	Store  batchApplyRefusalsResetStore
	Writer batchApplyRefusalsResetWriter
}

// batchApplyRefusalsResetCore is the body of the op after the params are
// decoded and the mode resolved (env.Live is !dryRun), shared by HEAD's shell
// and the native kind.
func batchApplyRefusalsResetCore(ctx context.Context, p batchApplyRefusalsResetNativeParams, env batchApplyRefusalsResetEnv) error {
	if env.Store == nil {
		return fmt.Errorf("%s: database not initialized", batchApplyRefusalsResetOpID)
	}
	dryRun := !env.Live
	n, err := resetApplyRefusalsVia(ctx, env.Store, env.Writer, p.BookIDs, dryRun)
	if err != nil {
		return fmt.Errorf("%s: %w", batchApplyRefusalsResetOpID, err)
	}
	verb := "forgot"
	if dryRun {
		verb = "would forget"
	}
	msg := fmt.Sprintf("%s %d remembered apply refusals", verb, n)
	env.Log(slog.LevelInfo, msg, slog.Int("count", n), slog.Bool("dry_run", dryRun))
	env.progress(1, 1, "complete: "+msg)
	return nil
}

// batchApplyRefusalsResetNativeParams are the native kind's params: HEAD's
// without the two dry-run spellings, which the run's mode carries.
type batchApplyRefusalsResetNativeParams struct {
	// BookIDs limits the reset to these books. Empty forgets every record.
	BookIDs []string `json:"book_ids,omitempty"`
}

// headBatchApplyRefusalsResetWriter is HEAD's write: the store's own
// DeleteRawBatch, called exactly as HEAD's body called it.
type headBatchApplyRefusalsResetWriter struct{ kv database.RawKVStore }

func (w headBatchApplyRefusalsResetWriter) DeleteRawBatch(keys []string) error {
	return w.kv.DeleteRawBatch(keys)
}

// batchApplyRefusalsResetDef is HEAD's definition. Its Run is the v2 shell.
func (s *Server) batchApplyRefusalsResetDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
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
			env := batchApplyRefusalsResetEnv{t7Env: t7HeadEnv(reporter, !dryRun)}
			if kv := s.storeForWiring(); kv != nil {
				env.Store = kv
				env.Writer = headBatchApplyRefusalsResetWriter{kv: kv}
			}
			return batchApplyRefusalsResetCore(ctx, batchApplyRefusalsResetNativeParams{BookIDs: p.BookIDs}, env)
		},
	}
}

// RegisterBatchApplyRefusalsResetOp registers metadata.batch-apply-refusals-reset.
// It is a sibling op rather than a param of metadata.batch-apply-cached because
// that op's params travel through MergeQueuedParams and the checkpoint overlay,
// where a "reset" key would survive a resume and change what a restarted batch
// does.
func (s *Server) RegisterBatchApplyRefusalsResetOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(s.batchApplyRefusalsResetDef())
}

// batchApplyRefusalsResetNative is the native books.metadata.batch-apply-refusals-reset
// kind. It deletes the apply_refusal: records (per-book keyed state, resource
// books), and only in Live; Preview counts them.
func (s *Server) batchApplyRefusalsResetNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(batchApplyRefusalsResetOpID)
	c := t7Common(s.batchApplyRefusalsResetDef(), sched)
	c.Effects = t7Writes(ops.ResBooks)
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (struct{}, error) {
			np, err := t7DecodeNative[batchApplyRefusalsResetNativeParams](kind, raw)
			if err != nil {
				return struct{}{}, err
			}
			env := batchApplyRefusalsResetEnv{t7Env: t7NativeEnv(rc)}
			if deps.Store != nil {
				env.Store = deps.Store
			}
			// A Preview run writes nothing, so it builds no writer; a Live run
			// refuses up front when the owner left the factory unset.
			if env.Live {
				w, err := t7NewWriter(kind, deps, rc)
				if err != nil {
					return struct{}{}, err
				}
				env.Writer = w
			}
			return struct{}{}, batchApplyRefusalsResetCore(rc.Context(), np, env)
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterBatchApplyRefusalsResetOp(reg) })
}
