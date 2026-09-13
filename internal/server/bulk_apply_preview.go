// file: internal/server/bulk_apply_preview.go
// version: 1.5.0
// guid: 6a2e9c15-4f70-4b3d-8e21-d5c7a0f9b384
// last-edited: 2026-09-13
//
// The bulk-apply DRY RUN: "metadata.bulk-apply-preview".
//
// For every book in scope it reports what a bulk apply would do — the
// candidate it would take, the certainty gate's verdict (internal/applygate),
// the per-field changes after locked fields are stripped, and whether the
// file sequel would rename and to where — WITHOUT applying anything.
//
// READ-ONLY. It writes no book, author, series, tag, file, field state,
// change history or metadata cache. The single thing it writes is its own
// report: one OperationResult row per book under the preview op's id, which is
// what makes the result pageable and downloadable. Those rows are op
// bookkeeping, not library state, and the dry-run test's fail-on-write store
// exempts nothing else.
//
// The decision it reports comes from planCachedApply / planOpResultApply, the
// same functions the real applies call, so the preview cannot disagree with
// the apply about which candidate is taken or whether the gate lets it through.

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

const bulkApplyPreviewOpID = "metadata.bulk-apply-preview"

// Preview verdicts.
const (
	previewVerdictApply   = "apply"   // the gate passes: a real apply would write this
	previewVerdictBlocked = "blocked" // the gate (or policy) refuses: manual review
	previewVerdictSkipped = "skipped" // nothing to apply (no candidate, book gone, ...)
)

// Preview candidate sources.
const (
	previewSourceCache     = "cache"             // metadata.batch-apply-cached
	previewSourceOpResults = "operation_results" // /metadata/batch-apply-candidates
)

// bulkApplyPreviewParams is the op's params.
type bulkApplyPreviewParams struct {
	BookIDs []string `json:"book_ids,omitempty"`
	// AllCached previews every book that has cached candidates — "everything
	// that would be applied" — instead of a list. Cache source only.
	AllCached bool `json:"all_cached,omitempty"`
	// Source is previewSourceCache (default) or previewSourceOpResults.
	Source string `json:"source,omitempty"`
	// OperationID is the candidate-fetch op whose results are previewed, for
	// previewSourceOpResults. With no BookIDs, every matched book in it.
	OperationID string `json:"operation_id,omitempty"`
	// WriteBack mirrors the apply's write_back (default true): whether the
	// apply would run the file sequel, which is where the rename happens.
	WriteBack *bool `json:"write_back,omitempty"`
}

// previewService is what the preview needs from *metafetch.Service.
type previewService interface {
	cachedApplyService
	PreviewMetadataCandidate(id string, candidate metafetch.MetadataCandidate, writeBack bool) (*metafetch.ApplyPreview, error)
}

// previewBook is the book as it is now.
type previewBook struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Author   string `json:"author"`
	Series   string `json:"series"`
	Position string `json:"position"`
	FilePath string `json:"file_path"`
	// DurationSec is the files' runtime; the gate's runtime check reads it.
	DurationSec int    `json:"duration_sec,omitempty"`
	Narrator    string `json:"narrator,omitempty"`
}

// previewCandidate is the candidate the apply would take.
type previewCandidate struct {
	Title    string  `json:"title"`
	Author   string  `json:"author"`
	Series   string  `json:"series"`
	Position string  `json:"position"`
	Source   string  `json:"source"`
	Score    float64 `json:"score"`
	ASIN     string  `json:"asin,omitempty"`
	Subtitle string  `json:"subtitle,omitempty"`
	Narrator string  `json:"narrator,omitempty"`
	// DurationSec is the source's runtime (0 = the source gave none).
	DurationSec int `json:"duration_sec,omitempty"`
}

// bulkApplyPreviewRow is one book's line in the report.
type bulkApplyPreviewRow struct {
	BookID    string             `json:"book_id"`
	Book      *previewBook       `json:"book,omitempty"`
	Candidate *previewCandidate  `json:"candidate,omitempty"`
	Verdict   string             `json:"verdict"`
	Reason    string             `json:"reason,omitempty"`
	Detail    string             `json:"detail,omitempty"`
	Gate      *applygate.Verdict `json:"gate,omitempty"`
	// Changes is what the apply would write (after locks). Filled for blocked
	// books too, so a reviewer sees what the refusal prevented.
	Changes       []metafetch.FieldChange  `json:"changes,omitempty"`
	SkippedLocked []string                 `json:"skipped_locked,omitempty"`
	Rename        *metafetch.RenamePreview `json:"rename,omitempty"`
}

// previewBulkApplyRow builds one book's row. plan is the same plan the real
// apply acts on. It reads only.
func previewBulkApplyRow(svc previewService, id string, plan cachedApplyPlan, writeBack bool) bulkApplyPreviewRow {
	row := bulkApplyPreviewRow{BookID: id}
	if b := plan.Book; b != nil {
		pb := &previewBook{ID: b.ID, Title: b.Title, FilePath: b.FilePath}
		if b.Duration != nil {
			pb.DurationSec = *b.Duration
		}
		if b.Narrator != nil {
			pb.Narrator = *b.Narrator
		}
		if b.Author != nil {
			pb.Author = b.Author.Name
		}
		if b.Series != nil {
			pb.Series = b.Series.Name
		}
		if b.SeriesPositionRaw != nil {
			pb.Position = *b.SeriesPositionRaw
		} else if b.SeriesSequence != nil {
			pb.Position = strconv.Itoa(*b.SeriesSequence)
		}
		row.Book = pb
	}
	if c := plan.Candidate; c != nil {
		row.Candidate = &previewCandidate{Title: c.Title, Author: c.Author, Series: c.Series,
			Position: c.SeriesPosition, Source: c.Source, Score: c.Score, ASIN: c.ASIN,
			Subtitle: c.Subtitle, Narrator: c.Narrator, DurationSec: c.DurationSec}
	}
	row.Gate = plan.Gate

	switch plan.Reason {
	case "":
		row.Verdict = previewVerdictApply
	case applySkipGateBlocked:
		row.Verdict, row.Reason = previewVerdictBlocked, plan.Gate.Reason
		row.Detail = plan.Gate.Detail
	default:
		row.Verdict, row.Reason = previewVerdictSkipped, plan.Reason
		if plan.Err != nil {
			row.Detail = plan.Err.Error()
		}
		return row
	}

	pv, err := svc.PreviewMetadataCandidate(id, *plan.Candidate, writeBack)
	switch {
	case errors.Is(err, metafetch.ErrApplyPolicyBlocked):
		// ApplyMetadataCandidate would refuse too.
		row.Verdict, row.Reason, row.Detail = previewVerdictBlocked, "policy_no_metadata", err.Error()
	case err != nil:
		// Could not tell what the apply would do; never report that as "apply".
		row.Verdict, row.Reason, row.Detail = previewVerdictBlocked, "preview_failed", err.Error()
	default:
		row.Changes, row.SkippedLocked, row.Rename = pv.Changes, pv.SkippedLocked, &pv.Rename
		// The real apply refuses this book before writing (RenamePreflight,
		// same previewRename), so the dry run must not call it "apply".
		if writeBack && pv.Rename.Blocking != "" {
			row.Verdict, row.Reason, row.Detail = previewVerdictBlocked, applySkipFileWorkWouldFail, pv.Rename.Blocking
		}
	}
	return row
}

// previewResultStore is where the op writes its report and the GET reads it.
type previewResultStore interface {
	CreateOperationResult(result *database.OperationResult) error
	GetOperationResults(operationID string) ([]database.OperationResult, error)
}

// RegisterBulkApplyPreviewOp registers "metadata.bulk-apply-preview".
func (s *Server) RegisterBulkApplyPreviewOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              bulkApplyPreviewOpID,
		Liveness:        opsregistry.LivenessRunItems,
		Plugin:          "metadata",
		DisplayName:     "Preview Bulk Metadata Apply (dry run)",
		Description:     "Report, for each book, the candidate a bulk metadata apply would take, the certainty gate's verdict, the field changes and any rename, without applying anything.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Timeout:         4 * time.Hour,
		// Requeue, not Restart: the preview holds no checkpoint state and writes
		// nothing to the library, so a run cut off by a restart is simply
		// replaced by a fresh one. Unspecified is refused by the registry, and
		// a refused registration stops the server from starting at all.
		ResumePolicy: opsregistry.ResumeRequeue,
		// Its own key: a preview never waits behind a real apply, and never
		// merges into one (it declares no MergeQueuedParams).
		ConcurrencyKey: bulkApplyPreviewOpID,
		Permissions:    []auth.Permission{auth.PermLibraryView},
		// Read-only: no CapLibraryWrite, no CapFilesWrite, and no scan
		// stand-down hold — a preview can run while a scan does.
		Capabilities: []opsregistry.Capability{opsregistry.CapLibraryRead},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p bulkApplyPreviewParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("bulk-apply-preview: decode params: %w", err)
				}
			}
			svc := s.metadataFetchService
			if svc == nil {
				return fmt.Errorf("bulk-apply-preview: metadata fetch service not initialized")
			}
			opID := opsregistry.ReporterOpID(reporter)
			if opID == "" {
				return fmt.Errorf("bulk-apply-preview: reporter has no op id to file results under")
			}
			return runBulkApplyPreview(ctx, reporter, svc, s.store, s.Ops(), opID, p)
		},
	})
}

// runBulkApplyPreview is the op body, separated from Server so a test can run
// it against fakes.
func runBulkApplyPreview(
	ctx context.Context,
	reporter opsregistry.Reporter,
	svc interface {
		previewService
		ListCachedSummaries(ctx context.Context) ([]metafetch.MetadataCacheSummary, error)
	},
	books claimBookReader,
	results previewResultStore,
	opID string,
	p bulkApplyPreviewParams,
) error {
	writeBack := p.WriteBack == nil || *p.WriteBack
	source := p.Source
	if source == "" {
		source = previewSourceCache
	}

	ids := p.BookIDs
	var opResults map[string]CandidateResult
	switch source {
	case previewSourceCache:
		if p.AllCached {
			sums, err := svc.ListCachedSummaries(ctx)
			if err != nil {
				return fmt.Errorf("bulk-apply-preview: list cached books: %w", err)
			}
			ids = ids[:0:0]
			for _, sm := range sums {
				if sm.CandidateCount > 0 {
					ids = append(ids, sm.BookID)
				}
			}
		}
	case previewSourceOpResults:
		if p.OperationID == "" {
			return fmt.Errorf("bulk-apply-preview: source %q needs operation_id", source)
		}
		rows, err := results.GetOperationResults(p.OperationID)
		if err != nil {
			return fmt.Errorf("bulk-apply-preview: load results of %s: %w", p.OperationID, err)
		}
		opResults = make(map[string]CandidateResult, len(rows))
		var matched []string
		for _, r := range rows {
			var cr CandidateResult
			if json.Unmarshal([]byte(r.ResultJSON), &cr) != nil {
				continue
			}
			opResults[r.BookID] = cr
			if cr.Status == "matched" && cr.Candidate != nil {
				matched = append(matched, r.BookID)
			}
		}
		if len(ids) == 0 {
			ids = matched
		}
	default:
		return fmt.Errorf("bulk-apply-preview: unknown source %q", source)
	}

	// The sibling-part index covers the whole source (every row of the
	// operation, or every cached book), not the preview's id list: the apply
	// builds it the same way, so both see the same siblings for any subset.
	var claims *applygate.ClaimIndex
	var claimErr error
	if source == previewSourceOpResults {
		claims, claimErr = buildClaimIndex(ctx, keysOf(opResults), opResultClaimLoader(books, func(id string) (CandidateResult, bool, error) {
			cr, ok := opResults[id]
			return cr, ok, nil
		}))
	} else {
		claims, claimErr = cachedClaimIndex(ctx, svc, books)
	}
	if claimErr != nil {
		return fmt.Errorf("bulk-apply-preview: %w", claimErr)
	}

	var nApply, nBlocked, nSkipped, nWriteErr atomic.Int64
	previewOne := func(_ context.Context, id string) error {
		var plan cachedApplyPlan
		if source == previewSourceOpResults {
			cr, ok := opResults[id]
			switch {
			case !ok:
				plan = cachedApplyPlan{Reason: "not_in_operation"}
			case cr.Status != "matched" || cr.Candidate == nil:
				plan = cachedApplyPlan{Reason: "status_" + cr.Status}
			default:
				plan = planOpResultApply(books, id, cr, claims)
			}
		} else {
			plan = planCachedApply(svc, books, id, claims)
		}
		row := previewBulkApplyRow(svc, id, plan, writeBack)
		switch row.Verdict {
		case previewVerdictApply:
			nApply.Add(1)
		case previewVerdictBlocked:
			nBlocked.Add(1)
		default:
			nSkipped.Add(1)
		}
		raw, err := json.Marshal(row)
		if err == nil {
			err = results.CreateOperationResult(&database.OperationResult{
				OperationID: opID, BookID: id, ResultJSON: string(raw), Status: row.Verdict,
			})
		}
		if err != nil {
			// A report row that failed to save must be visible, not a silent gap.
			nWriteErr.Add(1)
			_ = reporter.Log(slog.LevelWarn, "preview row not saved", slog.String("book_id", id), slog.String("error", err.Error()))
		}
		return nil
	}

	// CPU/DB-bound per book (cache read, lock read, organizer path build), so
	// NumCPU workers; the rows are disjoint by book id.
	runErr := opsregistry.RunItems(ctx, reporter, ids, previewOne, opsregistry.RunItemsOptions{
		Concurrency:    runtime.NumCPU(),
		PerItemTimeout: time.Minute,
		ErrMode:        opsregistry.ErrModeCollect,
		Label:          func(int, int) string { return "previewing bulk metadata apply" },
	})
	if runErr != nil {
		return runErr
	}
	msg := fmt.Sprintf("dry run complete: %d would apply, %d blocked, %d skipped of %d (report rows not saved: %d); read /api/v1/metadata/bulk-apply-preview/%s",
		nApply.Load(), nBlocked.Load(), nSkipped.Load(), len(ids), nWriteErr.Load(), opID)
	_ = registryProgressAdapter{r: reporter}.UpdateProgress(len(ids), len(ids), msg)
	return nil
}

// previewSummary counts a report by verdict and by reason.
type previewSummary struct {
	Total     int                       `json:"total"`
	ByVerdict map[string]int            `json:"by_verdict"`
	ByReason  map[string]map[string]int `json:"by_reason"` // verdict -> reason -> count
}

// handleStartBulkApplyPreview handles POST /api/v1/metadata/bulk-apply-preview.
// Body: bulkApplyPreviewParams. Returns 202 with the preview op id.
func (s *Server) handleStartBulkApplyPreview(c *gin.Context) {
	var p bulkApplyPreviewParams
	if err := c.ShouldBindJSON(&p); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body")
		return
	}
	if len(p.BookIDs) == 0 && !p.AllCached && p.OperationID == "" {
		httputil.RespondWithBadRequest(c, "give book_ids, all_cached:true, or operation_id")
		return
	}
	if p.OperationID != "" && p.Source == "" {
		p.Source = previewSourceOpResults
	}
	s.enqueueBulkApplyPreview(c, p)
}

// enqueueBulkApplyPreview enqueues the preview op and answers 202. Shared by
// the preview endpoint and by both bulk-apply endpoints' dry-run default.
func (s *Server) enqueueBulkApplyPreview(c *gin.Context, p bulkApplyPreviewParams) {
	if s.opRegistry == nil {
		httputil.RespondWithInternalError(c, "operations registry not initialized")
		return
	}
	opID, err := s.opRegistry.EnqueueOp(c.Request.Context(), bulkApplyPreviewOpID, p)
	if err != nil {
		httputil.InternalError(c, "failed to enqueue bulk apply preview", err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"data": gin.H{
		"op_id":       opID,
		"dry_run":     true,
		"results_url": "/api/v1/metadata/bulk-apply-preview/" + opID,
		"note":        "dry run: nothing was applied. Send dry_run:false to apply.",
	}})
}

// handleGetBulkApplyPreview handles GET /api/v1/metadata/bulk-apply-preview/:id.
//
// Query: limit (default 100, 0 = all), offset, verdict, reason (filters), and
// download=1 for the whole filtered report as an attachment. The summary always
// covers the WHOLE report, not the page. Rows appear while the op runs; poll
// /api/v1/operations/v2/:id for its status.
func (s *Server) handleGetBulkApplyPreview(c *gin.Context) {
	opID := c.Param("id")
	all, err := s.Ops().GetOperationResults(opID)
	if err != nil {
		httputil.InternalError(c, "failed to load preview results", err)
		return
	}
	rows := make([]bulkApplyPreviewRow, 0, len(all))
	sum := previewSummary{ByVerdict: map[string]int{}, ByReason: map[string]map[string]int{}}
	wantVerdict, wantReason := c.Query("verdict"), c.Query("reason")
	for _, r := range all {
		var row bulkApplyPreviewRow
		if json.Unmarshal([]byte(r.ResultJSON), &row) != nil || row.Verdict == "" {
			continue // not a preview row
		}
		sum.Total++
		sum.ByVerdict[row.Verdict]++
		if sum.ByReason[row.Verdict] == nil {
			sum.ByReason[row.Verdict] = map[string]int{}
		}
		sum.ByReason[row.Verdict][row.Reason]++
		if (wantVerdict == "" || row.Verdict == wantVerdict) && (wantReason == "" || row.Reason == wantReason) {
			rows = append(rows, row)
		}
	}
	if sum.Total == 0 {
		httputil.RespondWithNotFound(c, "bulk apply preview", opID)
		return
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].BookID < rows[j].BookID })

	if c.Query("download") == "1" {
		c.Header("Content-Disposition", `attachment; filename="bulk-apply-preview-`+sanitizeFilenamePart(opID)+`.json"`)
		c.JSON(http.StatusOK, gin.H{"op_id": opID, "summary": sum, "rows": rows})
		return
	}
	limit, offset := 100, 0
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v >= 0 {
		limit = v
	}
	if v, err := strconv.Atoi(c.Query("offset")); err == nil && v >= 0 {
		offset = v
	}
	filtered := len(rows)
	page := rows[min(offset, filtered):]
	if limit > 0 && len(page) > limit {
		page = page[:limit]
	}
	httputil.RespondWithOK(c, gin.H{
		"op_id":          opID,
		"summary":        sum,
		"filtered_count": filtered,
		"limit":          limit,
		"offset":         offset,
		"rows":           page,
	})
}

// sanitizeFilenamePart keeps an op id safe inside a Content-Disposition value.
func sanitizeFilenamePart(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '-' || r == '_' || r == '.' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			out = append(out, r)
		}
	}
	return string(out)
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterBulkApplyPreviewOp(reg) })
}
