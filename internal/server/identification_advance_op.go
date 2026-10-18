// file: internal/server/identification_advance_op.go
// version: 1.0.3
// guid: 96facb01-98fa-4ebd-b06d-1422569abedd
// last-edited: 2026-10-18
//
// Registers identification.advance (spec 02 PR 14): the v2 ADAPTER around
// the v3-shaped driver body in internal/identification.

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/identification"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// Why this lives in internal/server and not in a plugin package (the spec
// names internal/plugins/identification/): the per-book pipeline FetchOne
// adapts is a *Server method (fetchCandidateForBook, with the server's
// per-book claim and folder memo), and internal/server blank-imports
// internal/plugins, so a plugin cannot import the server back. The body in
// internal/identification does not depend on the server; only this adapter
// does, and the v3 port deletes it.

const identificationAdvanceOpID = "identification.advance"

// identificationAdvanceDependsOn: the ops that spend Audible's token bucket
// with this one. DependsOn is one-directional ("must not be running for THIS
// op to start"), so each of them lists identification.advance in turn.
var identificationAdvanceDependsOn = []string{
	"metadata.candidate-fetch",
	"metafetch.asin-backfill",
	catalogHarvestOpID,
}

// identificationAdvanceParams: dry_run defaults to TRUE (owner rule
// 2026-09-25: every writing op previews unless told otherwise). The dry run
// counts the dirty set and asks no provider; the scheduled tick sends
// dry_run=false.
type identificationAdvanceParams struct {
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
}

var identAdvanceLog = logger.New("identification-advance")

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.RegisterIdentificationAdvanceOp(reg)
	})
}

// RegisterIdentificationAdvanceOp registers identification.advance.
func (s *Server) RegisterIdentificationAdvanceOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:          identificationAdvanceOpID,
		Preview:     opsregistry.PreviewSpec{Params: identificationAdvanceParams{}},
		Liveness:    opsregistry.LivenessRunItems,
		Plugin:      "metadata",
		DisplayName: "Advance Book Identification",
		Description: "Drains the identification dirty set: each book a writer marked (a scan that changed its title or author, a new primary book, a merge, a retitle, a catalog harvest for its author) is run once through the candidate search and scoring, answered from the caches when its question has not changed. Fetch only: never applies a candidate. Dry run by default: counts the marked books.",
		// Low: the driver is continuous background work; anything a person
		// started goes first.
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     true,
		Timeout:         8 * time.Hour,
		// ResumeRestart with no checkpoint of its own: the dirty set IS the
		// resume state. A processed book's mark is gone, so a restarted run
		// pages the set from the start and finds only what is still owed
		// (the source is Unordered, so no cursor is kept).
		ResumePolicy:   opsregistry.ResumeRestart,
		ConcurrencyKey: identificationAdvanceOpID,
		DependsOn:      identificationAdvanceDependsOn,
		// What Item writes: the candidate cache row (through the existing
		// per-book fetch) and the ident: keys (the state index moves with the
		// cache row; the reason and the mark are the driver's own). It does
		// not write the book row, so it does not declare books.
		Writes: []opsregistry.Resource{
			opsregistry.Resource(ops.ResMetadataCache),
			opsregistry.Resource(ops.ResIndexes),
		},
		Permissions:  []auth.Permission{auth.PermSettingsManage},
		Capabilities: []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapNetworkGeneric},
		Run:          s.runIdentificationAdvanceOp,
	})
}

// identificationAdvanceConcurrencyBasis names how the worker count is
// derived (the v3 Network(n, why) form).
const identificationAdvanceConcurrencyBasis = "sum of enabled provider budgets (candidateFetchWorkers, the derivation metadata.candidate-fetch uses)"

// identificationAdvanceWorkers is the driver's worker count. Item is
// network-bound, so the pool is sized exactly as metadata.candidate-fetch's
// is: from the summed rate budget of the enabled sources, or the configured
// metadata_candidate_fetch_workers.
func identificationAdvanceWorkers(b metafetch.SourcesBudget) int {
	return candidateFetchWorkers(b, config.AppConfig.MetadataCandidateFetchWorkers)
}

// identAdvanceItemStore is the store capability the driver needs beyond
// database.Store: the dirty set and the reason records.
type identAdvanceItemStore interface {
	identification.DirtyPager
	identification.ItemStore
}

func (s *Server) runIdentificationAdvanceOp(ctx context.Context, raw json.RawMessage, reporter opsregistry.Reporter) error {
	var p identificationAdvanceParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("%s: decode params: %w", identificationAdvanceOpID, err)
		}
	}
	dryRun, err := opmode.ResolveDryRun(identificationAdvanceOpID, p.DryRun, p.DryRunCamel)
	if err != nil {
		return err
	}
	store := s.storeForWiring()
	ist, ok := database.AsCapability[identAdvanceItemStore](store)
	if !ok {
		return fmt.Errorf("%s: store has no identification dirty set (store is not Pebble-backed)", identificationAdvanceOpID)
	}
	source := identification.DirtySet{Store: ist}
	if dryRun {
		return identAdvanceDryRun(ctx, reporter, source)
	}
	mfs := s.metadataFetchService
	if mfs == nil {
		return fmt.Errorf("%s: metadata service not initialized", identificationAdvanceOpID)
	}
	opID := opsregistry.ReporterOpID(reporter)
	budget := metafetch.EnabledSourcesBudget()
	limiter := candidateFetchLimiter(budget.RPS, budget.Burst)
	folderMemo := s.newFolderMemo(store)
	workers := identificationAdvanceWorkers(budget)
	identAdvanceLog.Info("start: opID=%s workers=%d (%s) gate_rps=%.1f page_size=%d",
		logger.SanitizeLogValue(opID), workers, identificationAdvanceConcurrencyBasis, budget.RPS, identification.PageSize)

	item := identification.NewItem(identification.ItemDeps{
		LoadBook: store.GetBookByID,
		Store:    ist,
		FetchOne: s.identFetchOne(mfs, store, limiter, opID, folderMemo),
	})
	finish := func(sum identification.Summary) {
		msg := "identification advance: " + sum.String()
		_ = reporter.Log(slog.LevelInfo, msg)
		identAdvanceLog.Info("%s opID=%s", msg, logger.SanitizeLogValue(opID))
	}
	adv := identification.New(source, item, finish, workers, identificationAdvanceConcurrencyBasis)
	return runIdentificationAdvance(ctx, reporter, adv, &identification.Tally{})
}

// identAdvanceDryRun counts the dirty set a page at a time and writes
// nothing.
func identAdvanceDryRun(ctx context.Context, reporter opsregistry.Reporter, source identification.Source) error {
	n, after := 0, ""
	for {
		page, next, err := source.Pages(ctx, after, identification.PageSize)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		n += len(page)
		after = next
	}
	msg := fmt.Sprintf("dry run: %d books marked for identification (nothing fetched; dry_run=false advances them)", n)
	_ = reporter.Log(slog.LevelInfo, msg)
	_ = reporter.UpdateProgress(1, 1, msg)
	return nil
}

// identFetchOne adapts the existing per-book candidate fetch
// (fetchCandidateForBook) to identification.ItemDeps.FetchOne. It takes the
// same per-book claim metadata.candidate-fetch takes, so the driver and a
// concurrent fetch of the same book never fetch it twice at once.
func (s *Server) identFetchOne(mfs *metafetch.Service, store candidateFetchStore, limiter *rate.Limiter,
	opID string, folderMemo *metabatch.FolderMemo) func(context.Context, *database.Book) identification.FetchResult {
	return func(ctx context.Context, book *database.Book) identification.FetchResult {
		release, err := s.candidateFetchClaims.claim(ctx, book.ID)
		if err != nil {
			return identification.FetchResult{Outcome: identification.OutcomeError, Err: err}
		}
		res := s.fetchCandidateForBook(ctx, mfs, store, limiter, opID, book.ID, false, false, folderMemo)
		release()
		return identFetchResultOf(res, func() string {
			_, status := unsearchableQueryKind(metabatch.ResolveCandidateSearchQueryMemo(store, book, folderMemo), book.Title)
			return status
		})
	}
}

// identFetchResultOf maps the candidate fetch's per-book result to the
// driver's outcome and the reason the state derivation cannot see.
// skipStatus is asked only for a book skipped for want of a usable title, and
// returns the ledger status unsearchableQueryKind gives it.
func identFetchResultOf(res CandidateResult, skipStatus func() string) identification.FetchResult {
	switch res.Status {
	case "error":
		return identification.FetchResult{Outcome: identification.OutcomeError, Err: errors.New(res.Error)}
	case candidateStatusDeferred:
		return identification.FetchResult{Outcome: identification.OutcomeDeferred, Reason: database.IdentReasonQuotaDeferred}
	case "skipped":
		if !strings.Contains(res.Error, metabatch.SkipDetailNoUsableTitle) {
			// Marked no match by the owner: held, which the derivation sees.
			return identification.FetchResult{Outcome: identification.OutcomeSkippedDone}
		}
		reason := database.IdentReasonNoUsableTitle
		if skipStatus() == metafetch.FetchStatusSkippedFragment {
			reason = database.IdentReasonFragment
		}
		return identification.FetchResult{Outcome: identification.OutcomeSkippedDone, Reason: reason}
	}
	out := identification.FetchResult{Outcome: identification.OutcomeFetched}
	if res.Candidate != nil && res.Candidate.ManualOnly {
		out.Reason = database.IdentReasonManualOnly
	}
	return out
}

// identAdvanceRunOptions builds the RunItems options for one page.
//
// Concurrency is set explicitly: RunItems defaults to 1. Label reads only the
// item (page[i], which nothing writes while RunItems runs), never a shared
// counter, so it is race-free in the worker goroutines that call it.
// ProgressOffset/ProgressTotal make the bar count the books advanced so far
// in this run; the set's size is unknown until it is drained.
func identAdvanceRunOptions(adv identification.Advance, page []identification.Mark, done int) opsregistry.RunItemsOptions {
	return opsregistry.RunItemsOptions{
		Concurrency:    adv.Concurrency,
		ErrMode:        opsregistry.ErrModeCollect,
		ProgressOffset: done,
		ProgressTotal:  done + len(page),
		Label:          func(i, _ int) string { return adv.Label(page[i]) },
	}
}

// runIdentificationAdvance is the v2 adapter: Pages -> RunItems per page ->
// next page, until a page is empty, then Finish. It owns nothing else: no
// chunk leasing, no ledger, no status of its own (RunItems reports items
// done and the book each worker is on). PartitionBy is the book id and a
// page's keys are unique, so a page's workers never share a book; pages run
// one after another.
func runIdentificationAdvance(ctx context.Context, reporter opsregistry.Reporter, adv identification.Advance, tally *identification.Tally) error {
	after := ""
	var runErr error
	for {
		page, next, err := adv.Source.Pages(ctx, after, identification.PageSize)
		if err != nil {
			if ctx.Err() == nil {
				runErr = err
			}
			break
		}
		if len(page) == 0 {
			break
		}
		err = opsregistry.RunItems(ctx, reporter, page, func(ctx context.Context, m identification.Mark) error {
			o, err := adv.Item(ctx, m)
			tally.Record(o)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				// One book's failure is counted and logged; it never stops
				// the others.
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("book %s: %s", m.BookID, logger.SanitizeLogValue(err.Error())),
					slog.String("book_id", logger.SanitizeLogValue(m.BookID)), slog.String("outcome", string(o)))
			}
			return nil
		}, identAdvanceRunOptions(adv, page, int(tally.Done())))
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			runErr = err
			break
		}
		after = next
	}
	canceled := ctx.Err() != nil
	adv.Finish(tally.Summary(canceled))
	if canceled {
		return ctx.Err()
	}
	return runErr
}
