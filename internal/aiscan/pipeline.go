// file: internal/aiscan/pipeline.go
// version: 4.3.0
// guid: b8c4d0e2-5f6a-7b8c-9d0e-1f2a3b4c5d6e
// last-edited: 2026-09-19

package aiscan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/lifecycle"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

var plog = logger.New("aiscan")

// Store is the narrow slice of database.Store this service uses.
// Store is what this package actually calls, measured by emptying the
// interface and reading the compiler's enumeration: 4 methods. It was a
// pure pass-through of database.* embeds — 43 methods, none declared here.
//
// It was 7 until 2026-08-22. CreateOperation, UpdateOperationStatus and
// UpdateOperationError went with the v1 operations row; they are NOT replaced
// one-for-one. The registry now owns the row's existence, ProgressSink carries
// progress, and RunScan's return value carries terminal status — so the
// subsystem no longer co-authors its own operation record at all. That
// co-authorship is why six scattered writes could drift into being write-only
// without anyone noticing.
type Store interface {
	GetAllAuthors() ([]database.Author, error)
	GetAuthorByID(id int) (*database.Author, error)
	GetAllAuthorBookCounts() (map[int]int, error)
	GetBooksByAuthorIDWithRoleCore(authorID int) ([]database.BookCore, error)
}

// RealtimeLLM is the synchronous half of the OpenAI client the pipeline uses.
type RealtimeLLM interface {
	ReviewAuthorDuplicates(ctx context.Context, groups []ai.AuthorDedupInput) ([]ai.AuthorDedupSuggestion, error)
	DiscoverAuthorDuplicates(ctx context.Context, inputs []ai.AuthorDiscoveryInput) ([]ai.AuthorDiscoverySuggestion, error)
}

// BatchLLM is the OpenAI Batch API half of the client the pipeline uses.
type BatchLLM interface {
	// owner is the batch metadata naming the scan phase (scanBatchOwner); it is
	// what FindScanBatch matches on after a crash inside CreateBatch.
	CreateBatchAuthorReview(ctx context.Context, groups []ai.AuthorDedupInput, owner map[string]string) (string, error)
	CreateBatchAuthorDedup(ctx context.Context, inputs []ai.AuthorDiscoveryInput, owner map[string]string) (string, error)
	CheckBatchStatus(ctx context.Context, batchID string) (status, outputFileID string, err error)
	CancelBatch(ctx context.Context, batchID string) error
	DownloadBatchGroupsResults(ctx context.Context, outputFileID string) ([]ai.AuthorDedupSuggestion, error)
	DownloadBatchResults(ctx context.Context, outputFileID string) ([]ai.AuthorDiscoverySuggestion, error)
}

// LLM is everything the pipeline calls on its OpenAI client. *ai.OpenAIParser
// satisfies it; tests substitute a fake so LLM calls can be counted across a
// simulated restart.
type LLM interface {
	RealtimeLLM
	BatchLLM
}

// ProgressSink is the narrow slice of registry.Reporter the pipeline publishes
// progress through. Declared here rather than importing the registry so this
// package stays free of the operations layer: the server wires the real
// Reporter in when it runs the op.
type ProgressSink interface {
	UpdateProgress(current, total int, message string) error
}

// PipelineManager coordinates the multi-pass AI author dedup pipeline.
type PipelineManager struct {
	scanStore *database.AIScanStore
	mainStore Store
	parser    LLM
	mu        sync.Mutex
	// cancels tracks cancel functions for active scans, keyed by scan ID.
	cancels map[int]context.CancelFunc
	// sinks tracks the progress sink of the operation currently running each
	// scan, keyed by scan ID. Absent when no op is attached.
	sinks map[int]ProgressSink
	// dones carries each attached scan's terminal outcome to the RunScan call
	// waiting on it, keyed by scan ID. Buffered (size 1) so finishScan never
	// blocks on a caller that has already given up.
	dones map[int]chan error
	// running holds the phases a goroutine in THIS process is executing, keyed
	// "scanID:phaseType". See beginPhase.
	running map[string]bool
}

// NewPipelineManager creates a new pipeline manager.
func NewPipelineManager(scanStore *database.AIScanStore, mainStore Store, parser LLM) *PipelineManager {
	return &PipelineManager{
		scanStore: scanStore,
		mainStore: mainStore,
		parser:    parser,
		cancels:   make(map[int]context.CancelFunc),
		sinks:     make(map[int]ProgressSink),
		dones:     make(map[int]chan error),
		running:   make(map[string]bool),
	}
}

// sinkFor returns the progress sink attached to a scan, or nil when no
// operation is running it (a scan advanced by PollBatchPhases across a restart
// has no sink until its op resumes).
func (pm *PipelineManager) sinkFor(scanID int) ProgressSink {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.sinks[scanID]
}

// report publishes progress if an operation is attached, and is a no-op if not.
// Progress is advisory: a failure to publish must never fail the scan.
func (pm *PipelineManager) report(scanID, current, total int, message string) {
	if sink := pm.sinkFor(scanID); sink != nil {
		if err := sink.UpdateProgress(current, total, message); err != nil {
			slog.Warn("[AI Pipeline] progress report failed", "scanID", scanID, "err", err)
		}
	}
}

// finishScan delivers a scan's terminal outcome to the RunScan call waiting on
// it and drops all per-scan state.
//
// Exactly one caller wins: the done channel is removed under the same lock that
// found it, so the loser returns without sending. That guard is load-bearing —
// groups_scan and full_scan run concurrently and failPhase has 18 call sites,
// so a double finish is the normal case, not an edge case, and closing a
// channel twice would panic the process.
func (pm *PipelineManager) finishScan(scanID int, err error) {
	pm.mu.Lock()
	delete(pm.cancels, scanID)
	delete(pm.sinks, scanID)
	done, attached := pm.dones[scanID]
	if attached {
		delete(pm.dones, scanID)
	}
	pm.mu.Unlock()

	if !attached {
		return
	}
	done <- err
	close(done)
}

// detachScan drops a scan's in-process state without delivering an outcome or
// touching its persisted state — for a scan left to resume after a restart.
func (pm *PipelineManager) detachScan(scanID int) {
	pm.mu.Lock()
	delete(pm.cancels, scanID)
	delete(pm.sinks, scanID)
	delete(pm.dones, scanID)
	pm.mu.Unlock()
}

// CancelScan cancels a running scan by its ID, including any in-flight batch jobs.
func (pm *PipelineManager) CancelScan(scanID int) error {
	pm.mu.Lock()
	cancel, exists := pm.cancels[scanID]
	pm.mu.Unlock()

	if !exists {
		return fmt.Errorf("scan %d not found or already completed", scanID)
	}

	// Cancel the context to stop in-flight realtime API calls
	cancel()

	// Cancel any submitted batch jobs with OpenAI
	phases, _ := pm.scanStore.GetPhases(scanID)
	for _, p := range phases {
		if p.Status == "submitted" && p.BatchID != "" {
			if err := pm.parser.CancelBatch(context.Background(), p.BatchID); err != nil {
				slog.Warn("[AI Pipeline] Scan warning failed to cancel batch", "scanID", scanID, "p", p.BatchID, "err", err)
			} else {
				slog.Info("[AI Pipeline] Scan canceled batch", "scanID", scanID, "p", p.BatchID)
			}
		}
	}

	pm.cleanupScan(scanID, "canceled")
	return nil
}

// cleanupScan marks a scan and its in-progress phases as the given status, and removes the cancel func.
func (pm *PipelineManager) cleanupScan(scanID int, status string) {
	// Mark any in-progress phases as canceled/failed
	phases, _ := pm.scanStore.GetPhases(scanID)
	for _, p := range phases {
		if p.Status == "pending" || p.Status == "processing" || p.Status == "submitting" || p.Status == "submitted" {
			_ = pm.scanStore.UpdatePhaseStatus(scanID, p.PhaseType, status, "")
		}
	}
	_ = pm.scanStore.UpdateScanStatus(scanID, status)

	// Releases the waiting RunScan and drops the cancel func. ErrScanCanceled
	// rather than nil deliberately: CancelScan is reachable straight from
	// POST /ai-scans/:id/cancel without the registry involved, and a nil return
	// there would record the op as having COMPLETED a scan the operator stopped.
	pm.finishScan(scanID, ErrScanCanceled)
}

// nextPhases determines which phases should start based on current state.
func (pm *PipelineManager) nextPhases(completedPhase, status string, phaseStates map[string]string) []string {
	if status != "complete" {
		return nil
	}

	var next []string

	switch completedPhase {
	case "groups_scan":
		next = append(next, "groups_enrich")
	case "full_scan":
		next = append(next, "full_enrich")
	case "groups_enrich", "full_enrich":
		// Cross-validate only when BOTH enrichment phases exist and are
		// complete. This used to also count an enrichment as done when its row
		// was absent and its source was complete — but runEnrichment is
		// launched with `go` and creates its row a moment later, so whichever
		// enrichment finished first saw the other as "done", ran
		// cross-validation on the UN-enriched suggestions, and the second
		// finish ran it again. Every scan wrote two sets of results.
		groupsDone := phaseStates["groups_enrich"] == "complete" || completedPhase == "groups_enrich"
		fullDone := phaseStates["full_enrich"] == "complete" || completedPhase == "full_enrich"
		if groupsDone && fullDone {
			next = append(next, "cross_validate")
		}
	}

	return next
}

// ErrScanCanceled is the terminal outcome of a scan stopped by an operator.
var ErrScanCanceled = errors.New("ai scan canceled")

// ErrBatchSubmitUnknown is the terminal outcome of a batch phase found in
// "submitting" after a restart with no way to look its batch up.
var ErrBatchSubmitUnknown = errors.New("batch phase was mid-submit at restart; OpenAI may hold a batch that cannot be located")

// errBatchLookup marks a batch lookup that failed or could not see the whole
// window: not a verdict. The phase stays "submitting" and is looked up again.
var errBatchLookup = errors.New("batch lookup inconclusive")

// maxSubmitAttempts bounds how many times a phase whose CreateBatch failed
// (confirmed: no batch exists) is submitted again before it fails.
const maxSubmitAttempts = 3

// maxDownloadAttempts bounds how many failed downloads of a completed batch's
// output are retried before the phase fails.
const maxDownloadAttempts = 5

// fullScanChunkSize is how many authors one realtime full_scan LLM call
// carries. A var only so tests can make a small library span several chunks.
var fullScanChunkSize = 500

// heartbeatInterval is how often an attached scan republishes progress while
// waiting, and how often it polls its own submitted batches. The registry
// watchdog cancels an op that reports nothing for ProgressTimeout (default
// 5m); a submitted batch phase can legitimately sit silent for hours, so the
// wait loop must keep reporting on its behalf.
const heartbeatInterval = 60 * time.Second

// BatchFinder looks up an OpenAI batch by the owner metadata written at
// CreateBatch time (scan id, phase and a per-scan nonce). It is what lets a
// phase that died between CreateBatch and recording the batch id — or whose
// CreateBatch call errored after OpenAI may have accepted it — re-attach
// instead of paying for a second batch. found=false must mean a CONFIRMED
// absence; a lookup that could not see the whole window returns an error.
// *ai.OpenAIParser implements it; the client passed to NewPipelineManager is
// checked for it (test fakes may omit it).
type BatchFinder interface {
	FindBatchByMetadata(ctx context.Context, match map[string]string, since time.Time) (batchID string, found bool, err error)
}

// The production client must implement BatchFinder. It is checked by type
// assertion at run time, so without this line dropping the method would
// silently turn every crash-mid-submit re-attach into ErrBatchSubmitUnknown.
var _ BatchFinder = (*ai.OpenAIParser)(nil)

// sourcePhases are the two phases a scan starts with, each paired with the
// enrichment phase that follows it.
var sourcePhases = [...]struct{ scan, enrich string }{
	{"groups_scan", "groups_enrich"},
	{"full_scan", "full_enrich"},
}

// isTerminalPhase reports whether a phase row has reached a state nothing will
// move it out of.
func isTerminalPhase(status string) bool {
	return status == "complete" || status == "failed" || status == "canceled"
}

// CreateScan creates a scan and its phase records WITHOUT starting any work.
//
// Split out from the old StartScan so the HTTP handler can return the scan id
// synchronously — DedupAIReviewTab.tsx:47-48 calls startAIScan() and then
// immediately getAIScan(newScan.id), so the id cannot wait on an async op.
// The caller enqueues an ai.author-scan operation whose Run calls RunScan.
func (pm *PipelineManager) CreateScan(mode string) (*database.Scan, error) {
	models := map[string]string{"groups": "gpt-5-mini", "full": "o4-mini"}

	authors, err := pm.mainStore.GetAllAuthors()
	if err != nil {
		return nil, fmt.Errorf("get authors: %w", err)
	}

	scan, err := pm.scanStore.CreateScan(mode, models, len(authors))
	if err != nil {
		return nil, fmt.Errorf("create scan: %w", err)
	}

	if _, err := pm.scanStore.CreatePhase(scan.ID, "groups_scan", models["groups"]); err != nil {
		return nil, fmt.Errorf("create groups phase: %w", err)
	}
	if _, err := pm.scanStore.CreatePhase(scan.ID, "full_scan", models["full"]); err != nil {
		return nil, fmt.Errorf("create full phase: %w", err)
	}

	return scan, nil
}

// LinkOperation records which operation is running a scan.
//
// This field is the ONLY link between the two records, and CancelOperationV2
// (handlers/operations_v2.go:289) matches an incoming operation id against it to
// route a cancel through the pipeline. Before 2026-08-22 it held a v1 ULID that
// was registered with nothing and appeared in no timeline, so that branch could
// never match and the cancel silently did nothing. Storing the v2 op id here is
// what makes it reachable.
func (pm *PipelineManager) LinkOperation(scanID int, opID string) error {
	return pm.scanStore.UpdateScanOperationID(scanID, opID)
}

// RunScan drives a scan created by CreateScan to a terminal state and blocks
// until it gets there. It is the body of the ai.author-scan operation.
//
// It decides what to run from the scan's PERSISTED phase state — never from
// pm's in-process maps — because the op declares ResumePolicy=ResumeRestart:
// on a restart Run is re-entered with the same params and nothing in memory.
// drive does the per-phase decision; a fresh scan is just the case where every
// phase is pending.
func (pm *PipelineManager) RunScan(ctx context.Context, scanID int, sink ProgressSink) error {
	scan, err := pm.scanStore.GetScan(scanID)
	if err != nil {
		return fmt.Errorf("get scan %d: %w", scanID, err)
	}
	if scan == nil {
		return fmt.Errorf("scan %d not found", scanID)
	}
	// A scan already finished must not be waited on: nothing would ever
	// deliver its outcome and the op would sit until its 24h timeout.
	switch scan.Status {
	case "complete":
		return nil
	case "failed":
		return fmt.Errorf("scan %d already failed", scanID)
	case "canceled":
		return ErrScanCanceled
	}

	phases, err := pm.scanStore.GetPhases(scanID)
	if err != nil {
		return fmt.Errorf("get phases for scan %d: %w", scanID, err)
	}
	fresh := true
	for _, p := range phases {
		if p.Status != "pending" {
			fresh = false
			break
		}
	}

	done := make(chan error, 1)
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	pm.mu.Lock()
	if _, running := pm.dones[scanID]; running {
		pm.mu.Unlock()
		return fmt.Errorf("scan %d is already attached to a running operation", scanID)
	}
	pm.dones[scanID] = done
	pm.cancels[scanID] = cancel
	pm.sinks[scanID] = sink
	pm.mu.Unlock()

	if fresh {
		if err := pm.scanStore.UpdateScanStatus(scanID, "scanning"); err != nil {
			pm.finishScan(scanID, nil)
			return fmt.Errorf("update scan status: %w", err)
		}
		pm.report(scanID, 0, 100, "Starting AI scan pipeline...")
	} else {
		plog.Info("scan %d: resuming from persisted phase state", scanID)
		pm.report(scanID, 0, 100, "Resuming AI scan from its last persisted state")
	}

	if err := pm.drive(scanCtx, scanID, scan.Mode); err != nil {
		pm.finishScan(scanID, nil)
		return err
	}
	return pm.waitForScan(ctx, scanID, scan.Mode, done)
}

// drive launches whatever each phase of a scan still needs, judged from its
// persisted row alone. It is the resume, and a fresh start is the case where
// every phase is pending:
//
//   - pending / processing source phase: (re)run it. A realtime full_scan
//     continues from the chunks it already persisted; nothing is re-requested.
//   - submitting: the process died inside CreateBatch. Re-attach by metadata,
//     or fail visibly — never blindly re-submit a batch that may exist.
//   - submitted: OpenAI holds it; the collector (heartbeat or poller) finishes it.
//   - complete: make sure its enrichment ran, and cross-validation after both.
//
// Every phase function claims its phase and re-checks the row before doing
// work (beginPhase), so drive racing OnPhaseComplete cannot run a phase twice.
func (pm *PipelineManager) drive(ctx context.Context, scanID int, mode string) error {
	phases, err := pm.scanStore.GetPhases(scanID)
	if err != nil {
		return fmt.Errorf("get phases for scan %d: %w", scanID, err)
	}
	byType := make(map[string]database.ScanPhase, len(phases))
	for _, p := range phases {
		byType[p.PhaseType] = p
	}

	var authors []database.Author
	loadAuthors := func() ([]database.Author, error) {
		if authors != nil {
			return authors, nil
		}
		a, err := pm.mainStore.GetAllAuthors()
		if err != nil {
			return nil, fmt.Errorf("get authors: %w", err)
		}
		authors = a
		return authors, nil
	}

	enrichDone := 0
	for _, sp := range sourcePhases {
		p, ok := byType[sp.scan]
		if !ok {
			continue
		}
		switch p.Status {
		case "pending", "processing":
			a, err := loadAuthors()
			if err != nil {
				return err
			}
			pm.launchSource(ctx, scanID, mode, sp.scan, a)
		case "submitting":
			pm.resolveSubmitting(ctx, scanID, sp.scan)
		case "complete":
			e, ok := byType[sp.enrich]
			switch {
			case ok && e.Status == "complete":
				enrichDone++
			case !ok || !isTerminalPhase(e.Status):
				go pm.runEnrichment(ctx, scanID, sp.scan, sp.enrich)
			}
		}
	}

	if enrichDone == len(sourcePhases) {
		if cv, ok := byType["cross_validate"]; ok && cv.Status == "complete" {
			// Died between finishing cross-validation and marking the scan.
			pm.completeScan(scanID)
		} else {
			go pm.runCrossValidation(ctx, scanID)
		}
	}
	return nil
}

// launchSource starts one source phase in the scan's mode.
func (pm *PipelineManager) launchSource(ctx context.Context, scanID int, mode, phaseType string, authors []database.Author) {
	switch {
	case mode == "batch" && phaseType == "groups_scan":
		go pm.runGroupsScanBatch(ctx, scanID, authors)
	case mode == "batch":
		go pm.runFullScanBatch(ctx, scanID, authors)
	case phaseType == "groups_scan":
		go pm.runGroupsScanRealtime(ctx, scanID, authors)
	default:
		go pm.runFullScanRealtime(ctx, scanID, authors)
	}
}

// resolveSubmitting settles a batch phase left "submitting": its row was
// written before CreateBatch, and CreateBatch either never returned (the
// process died) or returned an error that does not prove OpenAI rejected it.
//
//   - found by metadata: record its id; the collector finishes it.
//   - confirmed absent: no batch reached OpenAI, so submitting now is the only
//     submission (bounded by maxSubmitAttempts).
//   - inconclusive lookup: leave it "submitting"; the next heartbeat or poll
//     looks again. Failing here would orphan a batch OpenAI may be billing.
//   - no finder at all: fail visibly (ErrBatchSubmitUnknown) — nothing could
//     ever settle it, and guessing either way pays twice or hangs.
func (pm *PipelineManager) resolveSubmitting(ctx context.Context, scanID int, phaseType string) {
	batchID, found, err := pm.findScanBatch(ctx, scanID, phaseType)
	switch {
	case errors.Is(err, ErrBatchSubmitUnknown):
		pm.failPhase(scanID, phaseType, err)
	case err != nil:
		plog.Warn("scan %d: %s is mid-submit and its batch lookup was inconclusive; will look again: %v", scanID, phaseType, err)
	case found:
		plog.Info("scan %d: re-attached %s to batch %s found by metadata", scanID, phaseType, batchID)
		if err := pm.scanStore.UpdatePhaseStatus(scanID, phaseType, "submitted", batchID); err != nil {
			plog.Error("scan %d: record re-attached batch %s for %s (will retry): %v", scanID, batchID, phaseType, err)
		}
	default:
		if n := pm.attempts(scanID, phaseType, "submit_attempts"); n >= maxSubmitAttempts {
			pm.failPhase(scanID, phaseType, fmt.Errorf("batch create failed %d times with no batch reaching OpenAI", n))
			return
		}
		authors, err := pm.mainStore.GetAllAuthors()
		if err != nil {
			plog.Warn("scan %d: cannot resubmit %s yet: %v", scanID, phaseType, err)
			return
		}
		plog.Info("scan %d: confirmed no batch exists for %s; submitting it now", scanID, phaseType)
		pm.launchSource(ctx, scanID, "batch", phaseType, authors)
	}
}

// scanBatchOwner is the batch metadata naming the scan phase that owns a
// batch. The phase row learns its batch id only after CreateBatch returns; if
// the process dies in between, or CreateBatch errors ambiguously, these keys
// are the only link from the paid OpenAI batch back to its phase. The nonce
// (the scan's creation time) keeps a reused scan id from matching.
func scanBatchOwner(scan *database.Scan, phaseType string) map[string]string {
	return map[string]string{
		ai.BatchMetaScanID:    strconv.Itoa(scan.ID),
		ai.BatchMetaScanPhase: phaseType,
		ai.BatchMetaScanNonce: strconv.FormatInt(scan.CreatedAt.UnixNano(), 10),
	}
}

// findScanBatch looks up a phase's batch through the client's BatchFinder.
// ErrBatchSubmitUnknown: no finder. errBatchLookup: inconclusive.
func (pm *PipelineManager) findScanBatch(ctx context.Context, scanID int, phaseType string) (string, bool, error) {
	finder, ok := pm.parser.(BatchFinder)
	if !ok {
		return "", false, ErrBatchSubmitUnknown
	}
	scan, err := pm.scanStore.GetScan(scanID)
	if err != nil || scan == nil {
		return "", false, fmt.Errorf("%w: read scan %d: %v", errBatchLookup, scanID, err)
	}
	// An hour of slack under the scan's creation absorbs clock skew between
	// this host and OpenAI; the batch cannot predate its scan.
	since := scan.CreatedAt.Add(-time.Hour)
	batchID, found, err := finder.FindBatchByMetadata(ctx, scanBatchOwner(scan, phaseType), since)
	if err != nil {
		return "", false, fmt.Errorf("%w: %w", errBatchLookup, err)
	}
	return batchID, found, nil
}

// attempts reads a phase's persisted attempt counter.
func (pm *PipelineManager) attempts(scanID int, phaseType, name string) int {
	arts, err := pm.scanStore.GetPhaseArtifacts(scanID, phaseType)
	if err != nil {
		return 0
	}
	var n int
	_ = json.Unmarshal(arts[name], &n)
	return n
}

// bumpAttempts increments and persists a phase's attempt counter.
func (pm *PipelineManager) bumpAttempts(scanID int, phaseType, name string) int {
	n := pm.attempts(scanID, phaseType, name) + 1
	raw, _ := json.Marshal(n)
	if err := pm.scanStore.SavePhaseArtifact(scanID, phaseType, name, raw); err != nil {
		plog.Warn("scan %d: persist %s for %s: %v", scanID, name, phaseType, err)
	}
	return n
}

// abandonedForShutdown reports whether ctx ended because the server is
// shutting down. A phase that sees it must return WITHOUT writing a terminal
// state: the op resumes after the restart and drive continues the phase from
// what is persisted. Failing it would throw that work away.
func abandonedForShutdown(ctx context.Context, scanID int, phaseType string) bool {
	if !lifecycle.IsShutdown(ctx) {
		return false
	}
	plog.Info("scan %d: %s interrupted by server shutdown; it resumes after the restart", scanID, phaseType)
	return true
}

// beginPhase claims a phase for this process and reports whether the caller
// should run it. It returns false when another goroutine here already holds
// the phase, or when the persisted row is already terminal — the second is what
// makes a repeated trigger (both enrichments finishing at once, a completed
// batch dispatched by two pollers, drive racing OnPhaseComplete) a no-op
// instead of a second paid call and a second set of results. A true return
// must be paired with endPhase.
func (pm *PipelineManager) beginPhase(scanID int, phaseType string) bool {
	key := fmt.Sprintf("%d:%s", scanID, phaseType)
	pm.mu.Lock()
	if pm.running == nil {
		pm.running = make(map[string]bool)
	}
	if pm.running[key] {
		pm.mu.Unlock()
		return false
	}
	pm.running[key] = true
	pm.mu.Unlock()

	if p, err := pm.scanStore.GetPhase(scanID, phaseType); err == nil && p != nil && isTerminalPhase(p.Status) {
		pm.endPhase(scanID, phaseType)
		return false
	}
	return true
}

// endPhase releases a claim taken by beginPhase.
func (pm *PipelineManager) endPhase(scanID int, phaseType string) {
	pm.mu.Lock()
	delete(pm.running, fmt.Sprintf("%d:%s", scanID, phaseType))
	pm.mu.Unlock()
}

// phaseClaimed reports whether a goroutine in this process holds a phase.
func (pm *PipelineManager) phaseClaimed(scanID int, phaseType string) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.running[fmt.Sprintf("%d:%s", scanID, phaseType)]
}

// waitForScan blocks until the scan reaches a terminal state, the operation's
// context is canceled, and heartbeats progress in the meantime so the registry
// watchdog does not mistake a legitimately quiet batch wait for a stuck op.
//
// For a batch scan each heartbeat also polls the scan's own batches. The
// batch poller's author_review / author_dedup handlers collect them too, but
// only for a batch that is "completed" AND in the first page of the project's
// batch listing; a failed or expired batch, or one pushed off that page by
// other traffic, is only ever seen here.
func (pm *PipelineManager) waitForScan(ctx context.Context, scanID int, mode string, done <-chan error) error {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case err := <-done:
			return err

		case <-ctx.Done():
			// A server restart is not a cancel. The registry cancels every
			// running op on shutdown with lifecycle.ErrShutdown as the cause;
			// this op is ResumeRestart, so leave the batches running and the
			// phases where they are for the resumed run to pick up.
			if lifecycle.IsShutdown(ctx) {
				plog.Info("scan %d: server shutting down; leaving the scan to resume after the restart", scanID)
				pm.detachScan(scanID)
				return ctx.Err()
			}
			// Operator cancel or timeout: route through CancelScan, not a bare
			// context cancel. A submitted batch is held by OpenAI and keeps
			// costing money until the batch job itself is canceled, which only
			// CancelScan does.
			if cerr := pm.CancelScan(scanID); cerr != nil {
				slog.Warn("[AI Pipeline] cancel on context done failed", "scanID", scanID, "err", cerr)
			}
			return ctx.Err()

		case <-ticker.C:
			if mode == "batch" {
				pm.pollScanBatches(ctx, scanID)
			}
			phases, err := pm.scanStore.GetPhases(scanID)
			if err != nil {
				continue
			}
			complete := 0
			for _, p := range phases {
				if p.Status == "complete" {
					complete++
				}
			}
			pm.report(scanID, phaseProgressPct(complete), 100, "AI scan in progress")
		}
	}
}

// phaseProgressPct maps completed phase count to a percentage, capped below 100
// so the bar never claims completion before the scan actually finishes.
func phaseProgressPct(completed int) int {
	pct := min(completed*20, 90)
	return pct
}

// OnPhaseComplete is called when a phase finishes. Updates operation progress and triggers next phases.
func (pm *PipelineManager) OnPhaseComplete(ctx context.Context, scanID int, completedPhase string) {
	phases, err := pm.scanStore.GetPhases(scanID)
	if err != nil {
		slog.Error("[AI Pipeline] Error getting phases for scan", "scanID", scanID, "err", err)
		return
	}
	phaseStates := map[string]string{}
	completedCount := 0
	for _, p := range phases {
		phaseStates[p.PhaseType] = p.Status
		if p.Status == "complete" {
			completedCount++
		}
	}

	// Report progress (rough: each phase ~20% of total pipeline). A no-op when
	// no operation is attached.
	pm.report(scanID, phaseProgressPct(completedCount), 100,
		fmt.Sprintf("Phase %s complete", completedPhase))

	next := pm.nextPhases(completedPhase, "complete", phaseStates)
	for _, phaseType := range next {
		switch phaseType {
		case "groups_enrich":
			go pm.runEnrichment(ctx, scanID, "groups_scan", "groups_enrich")
		case "full_enrich":
			go pm.runEnrichment(ctx, scanID, "full_scan", "full_enrich")
		case "cross_validate":
			go pm.runCrossValidation(ctx, scanID)
		}
	}
}

// failPhase marks a phase and the overall scan as failed, and updates the operation record.
func (pm *PipelineManager) failPhase(scanID int, phaseType string, err error) {
	slog.Info("[AI Pipeline] Scan failed", "scanID", scanID, "phaseType", phaseType, "err", err)
	if updateErr := pm.scanStore.UpdatePhaseStatus(scanID, phaseType, "failed", ""); updateErr != nil {
		slog.Error("[AI Pipeline] Scan error updating status", "scanID", scanID, "phaseType", phaseType, "updateErr", updateErr)
	}
	if updateErr := pm.scanStore.UpdateScanStatus(scanID, "failed"); updateErr != nil {
		slog.Error("[AI Pipeline] Scan error updating scan status", "scanID", scanID, "updateErr", updateErr)
	}

	// Carry the failure to the waiting operation. finishScan drops the cancel
	// func, and its delete-under-mutex guard means only the FIRST failing phase
	// decides the op's outcome — groups_scan and full_scan run concurrently, so
	// a second failure arriving here is normal and must not double-send.
	pm.finishScan(scanID, fmt.Errorf("phase %s: %w", phaseType, err))
}

// buildGroupsInput builds AuthorDedupInput from heuristic groups, replicating the logic from server.go.
func (pm *PipelineManager) buildGroupsInput(authors []database.Author) ([]ai.AuthorDedupInput, []dedup.AuthorDedupGroup, error) {
	bookCounts, err := pm.mainStore.GetAllAuthorBookCounts()
	if err != nil {
		return nil, nil, fmt.Errorf("get book counts: %w", err)
	}
	bookCountFn := func(authorID int) int { return bookCounts[authorID] }

	groups := dedup.FindDuplicateAuthors(authors, 0.9, bookCountFn)

	var inputs []ai.AuthorDedupInput
	for i, group := range groups {
		var variantNames []string
		for _, v := range group.Variants {
			variantNames = append(variantNames, v.Name)
		}
		var sampleTitles []string
		if group.Canonical.ID > 0 {
			books, bErr := pm.mainStore.GetBooksByAuthorIDWithRoleCore(group.Canonical.ID)
			if bErr == nil {
				for j, b := range books {
					if j >= 3 {
						break
					}
					sampleTitles = append(sampleTitles, b.Title)
				}
			}
		}
		inputs = append(inputs, ai.AuthorDedupInput{
			Index:         i,
			CanonicalName: group.Canonical.Name,
			VariantNames:  variantNames,
			BookCount:     group.BookCount,
			SampleTitles:  sampleTitles,
		})
	}

	return inputs, groups, nil
}

// buildFullInput builds AuthorDiscoveryInput from all authors, replicating the logic from server.go.
func (pm *PipelineManager) buildFullInput(authors []database.Author) []ai.AuthorDiscoveryInput {
	var inputs []ai.AuthorDiscoveryInput
	for _, author := range authors {
		var sampleTitles []string
		books, err := pm.mainStore.GetBooksByAuthorIDWithRoleCore(author.ID)
		if err == nil {
			for j, b := range books {
				if j >= 3 {
					break
				}
				sampleTitles = append(sampleTitles, b.Title)
			}
		}
		inputs = append(inputs, ai.AuthorDiscoveryInput{
			ID:           author.ID,
			Name:         author.Name,
			BookCount:    len(books),
			SampleTitles: sampleTitles,
		})
	}
	return inputs
}

// groupsSuggestionsToScanSuggestions converts AI groups suggestions to normalized ScanSuggestions.
func groupsSuggestionsToScanSuggestions(suggestions []ai.AuthorDedupSuggestion, groupIDs [][]int) []database.ScanSuggestion {
	var result []database.ScanSuggestion
	for _, s := range suggestions {
		// Normalize initials formatting
		canonicalName := dedup.NormalizeAuthorName(s.CanonicalName)

		// Author IDs of the group the model was asked about
		var authorIDs []int
		if s.GroupIndex >= 0 && s.GroupIndex < len(groupIDs) {
			authorIDs = append(authorIDs, groupIDs[s.GroupIndex]...)
		}

		var rolesJSON json.RawMessage
		if s.Roles != nil {
			rolesJSON, _ = json.Marshal(s.Roles)
		}

		result = append(result, database.ScanSuggestion{
			Action:        s.Action,
			CanonicalName: canonicalName,
			Reason:        s.Reason,
			Confidence:    s.Confidence,
			AuthorIDs:     authorIDs,
			GroupIndex:    s.GroupIndex,
			Roles:         rolesJSON,
			Source:        "groups_scan",
		})
	}
	return result
}

// fullSuggestionsToScanSuggestions converts AI full/discovery suggestions to normalized ScanSuggestions.
func fullSuggestionsToScanSuggestions(suggestions []ai.AuthorDiscoverySuggestion) []database.ScanSuggestion {
	var result []database.ScanSuggestion
	for _, s := range suggestions {
		canonicalName := dedup.NormalizeAuthorName(s.CanonicalName)

		var rolesJSON json.RawMessage
		if s.Roles != nil {
			rolesJSON, _ = json.Marshal(s.Roles)
		}

		result = append(result, database.ScanSuggestion{
			Action:        s.Action,
			CanonicalName: canonicalName,
			Reason:        s.Reason,
			Confidence:    s.Confidence,
			AuthorIDs:     s.AuthorIDs,
			Roles:         rolesJSON,
			Source:        "full_scan",
		})
	}
	return result
}

// Phase implementations

// groupAuthorIDsArtifact names the persisted group -> author-id map a groups
// phase was built from. The model answers with a group_index, so the answer
// can only be mapped back to authors through the grouping it was ASKED about;
// a batch that completes hours later must not be decoded against a grouping
// rebuilt from whatever the author table holds by then.
const groupAuthorIDsArtifact = "group_author_ids"

// chunkArtifact names one finished realtime full_scan chunk.
func chunkArtifact(i int) string { return fmt.Sprintf("chunk:%06d", i) }

// groupAuthorIDs flattens heuristic groups to the author ids of each, in order.
func groupAuthorIDs(groups []dedup.AuthorDedupGroup) [][]int {
	out := make([][]int, len(groups))
	for i, g := range groups {
		ids := []int{g.Canonical.ID}
		for _, v := range g.Variants {
			ids = append(ids, v.ID)
		}
		out[i] = ids
	}
	return out
}

// saveGroupAuthorIDs persists the grouping a groups phase is about to ask about.
func (pm *PipelineManager) saveGroupAuthorIDs(scanID int, groups []dedup.AuthorDedupGroup) error {
	raw, err := json.Marshal(groupAuthorIDs(groups))
	if err != nil {
		return fmt.Errorf("marshal group author ids: %w", err)
	}
	return pm.scanStore.SavePhaseArtifact(scanID, "groups_scan", groupAuthorIDsArtifact, raw)
}

// completeEmptySource finishes a source phase that has nothing to send.
func (pm *PipelineManager) completeEmptySource(ctx context.Context, scanID int, phaseType string) {
	emptySuggestions, _ := json.Marshal([]database.ScanSuggestion{})
	if err := pm.scanStore.SavePhaseData(scanID, phaseType, nil, nil, emptySuggestions); err != nil {
		pm.failPhase(scanID, phaseType, fmt.Errorf("save phase data: %w", err))
		return
	}
	if err := pm.scanStore.UpdatePhaseStatus(scanID, phaseType, "complete", ""); err != nil {
		pm.failPhase(scanID, phaseType, fmt.Errorf("mark %s complete: %w", phaseType, err))
		return
	}
	pm.OnPhaseComplete(ctx, scanID, phaseType)
}

// finishSource persists a source phase's results, marks it complete and
// triggers what follows.
func (pm *PipelineManager) finishSource(ctx context.Context, scanID int, phaseType string, input json.RawMessage, output any, suggestions []database.ScanSuggestion) {
	outputJSON, _ := json.Marshal(output)
	suggestionsJSON, _ := json.Marshal(suggestions)
	if err := pm.scanStore.SavePhaseData(scanID, phaseType, input, outputJSON, suggestionsJSON); err != nil {
		pm.failPhase(scanID, phaseType, fmt.Errorf("save phase data: %w", err))
		return
	}
	plog.Info("scan %d: %s complete with %d suggestions", scanID, phaseType, len(suggestions))
	if err := pm.scanStore.UpdatePhaseStatus(scanID, phaseType, "complete", ""); err != nil {
		pm.failPhase(scanID, phaseType, fmt.Errorf("mark %s complete: %w", phaseType, err))
		return
	}
	pm.OnPhaseComplete(ctx, scanID, phaseType)
}

func (pm *PipelineManager) runGroupsScanRealtime(ctx context.Context, scanID int, authors []database.Author) {
	if !pm.beginPhase(scanID, "groups_scan") {
		return
	}
	defer pm.endPhase(scanID, "groups_scan")
	plog.Info("scan %d: starting groups scan (realtime)", scanID)
	if err := pm.scanStore.UpdatePhaseStatus(scanID, "groups_scan", "processing", ""); err != nil {
		pm.failPhase(scanID, "groups_scan", fmt.Errorf("mark processing: %w", err))
		return
	}

	inputs, groups, err := pm.buildGroupsInput(authors)
	if err != nil {
		pm.failPhase(scanID, "groups_scan", err)
		return
	}
	if len(inputs) == 0 {
		plog.Info("scan %d: no duplicate groups found, skipping groups scan", scanID)
		pm.completeEmptySource(ctx, scanID, "groups_scan")
		return
	}

	inputJSON, _ := json.Marshal(inputs)
	if err := pm.scanStore.SavePhaseData(scanID, "groups_scan", inputJSON, nil, nil); err != nil {
		pm.failPhase(scanID, "groups_scan", fmt.Errorf("save phase input: %w", err))
		return
	}

	// One LLM call: a restart during it re-runs it. There is no partial result
	// to keep.
	suggestions, err := pm.parser.ReviewAuthorDuplicates(ctx, inputs)
	if err != nil {
		if abandonedForShutdown(ctx, scanID, "groups_scan") {
			return
		}
		pm.failPhase(scanID, "groups_scan", fmt.Errorf("AI review failed: %w", err))
		return
	}
	pm.finishSource(ctx, scanID, "groups_scan", inputJSON, suggestions,
		groupsSuggestionsToScanSuggestions(suggestions, groupAuthorIDs(groups)))
}

// fullScanInputs returns the inputs a full_scan run must use. A phase resumed
// after a restart reuses the input it persisted before its first LLM call, NOT
// a rebuild from the live author table: chunk k of a rebuilt list is a
// different set of authors once anything was added or merged, and the chunks
// already finished would then no longer line up with the ones still to run.
func (pm *PipelineManager) fullScanInputs(scanID int, authors []database.Author) ([]ai.AuthorDiscoveryInput, json.RawMessage, error) {
	if p, err := pm.scanStore.GetPhase(scanID, "full_scan"); err == nil && p != nil && len(p.InputData) > 0 {
		var inputs []ai.AuthorDiscoveryInput
		if err := json.Unmarshal(p.InputData, &inputs); err == nil && len(inputs) > 0 {
			return inputs, p.InputData, nil
		}
	}
	inputs := pm.buildFullInput(authors)
	if len(inputs) == 0 {
		return nil, nil, nil
	}
	inputJSON, _ := json.Marshal(inputs)
	if err := pm.scanStore.SavePhaseData(scanID, "full_scan", inputJSON, nil, nil); err != nil {
		return nil, nil, fmt.Errorf("save phase input: %w", err)
	}
	return inputs, inputJSON, nil
}

// runFullScanRealtime sends the author list in chunks and persists each
// chunk's answer the moment it returns. A run resumed after a restart skips
// every chunk already persisted, so a crash costs at most the one chunk that
// was in flight — before this, finished chunks lived only in memory until the
// whole phase ended and a restart discarded all of them.
func (pm *PipelineManager) runFullScanRealtime(ctx context.Context, scanID int, authors []database.Author) {
	if !pm.beginPhase(scanID, "full_scan") {
		return
	}
	defer pm.endPhase(scanID, "full_scan")
	plog.Info("scan %d: starting full scan (realtime)", scanID)
	if err := pm.scanStore.UpdatePhaseStatus(scanID, "full_scan", "processing", ""); err != nil {
		pm.failPhase(scanID, "full_scan", fmt.Errorf("mark processing: %w", err))
		return
	}

	inputs, inputJSON, err := pm.fullScanInputs(scanID, authors)
	if err != nil {
		pm.failPhase(scanID, "full_scan", err)
		return
	}
	if len(inputs) == 0 {
		plog.Info("scan %d: no authors found, skipping full scan", scanID)
		pm.completeEmptySource(ctx, scanID, "full_scan")
		return
	}

	finished, err := pm.scanStore.GetPhaseArtifacts(scanID, "full_scan")
	if err != nil {
		pm.failPhase(scanID, "full_scan", fmt.Errorf("load finished chunks: %w", err))
		return
	}

	chunkSize := fullScanChunkSize
	var allDiscoveries []ai.AuthorDiscoverySuggestion
	resumed := 0
	for i, start := 0, 0; start < len(inputs); i, start = i+1, start+chunkSize {
		end := min(start+chunkSize, len(inputs))
		name := chunkArtifact(i)

		if raw, ok := finished[name]; ok {
			var saved []ai.AuthorDiscoverySuggestion
			if err := json.Unmarshal(raw, &saved); err == nil {
				allDiscoveries = append(allDiscoveries, saved...)
				resumed++
				continue
			}
			plog.Warn("scan %d: persisted %s is unreadable; requesting it again", scanID, name)
		}

		discoveries, err := pm.parser.DiscoverAuthorDuplicates(ctx, inputs[start:end])
		if err != nil {
			if abandonedForShutdown(ctx, scanID, "full_scan") {
				return
			}
			pm.failPhase(scanID, "full_scan", fmt.Errorf("AI discovery failed (chunk %d-%d): %w", start, end, err))
			return
		}
		raw, _ := json.Marshal(discoveries)
		if err := pm.scanStore.SavePhaseArtifact(scanID, "full_scan", name, raw); err != nil {
			// The answer is still in hand, so this run loses nothing; only a
			// restart before the phase ends would have to ask for it again.
			plog.Warn("scan %d: could not persist %s, a restart would re-request it: %v", scanID, name, err)
		}
		allDiscoveries = append(allDiscoveries, discoveries...)
	}
	if resumed > 0 {
		plog.Info("scan %d: full scan reused %d persisted chunks", scanID, resumed)
	}

	pm.finishSource(ctx, scanID, "full_scan", inputJSON, allDiscoveries,
		fullSuggestionsToScanSuggestions(allDiscoveries))
}

// submitBatch records "submitting" BEFORE CreateBatch, then the batch id
// after. The pre-submit write is what makes a crash inside CreateBatch
// recoverable: without it the phase still reads "pending" after a restart and
// the resume launches — and pays for — a second batch. With it, the phase is
// settled by resolveSubmitting, which looks the batch up by its owner metadata.
//
// A CreateBatch ERROR is handled the same way, not as a failure: a timeout or
// reset connection does not prove OpenAI rejected the request, and failing the
// phase there would orphan a batch it may already be billing.
func (pm *PipelineManager) submitBatch(scanID int, phaseType string, create func(owner map[string]string) (string, error)) {
	scan, err := pm.scanStore.GetScan(scanID)
	if err != nil || scan == nil {
		pm.failPhase(scanID, phaseType, fmt.Errorf("read scan for batch owner: %v", err))
		return
	}
	if err := pm.scanStore.UpdatePhaseStatus(scanID, phaseType, "submitting", ""); err != nil {
		// Never create a batch this process could not account for after a crash.
		pm.failPhase(scanID, phaseType, fmt.Errorf("mark submitting: %w", err))
		return
	}
	pm.bumpAttempts(scanID, phaseType, "submit_attempts")
	batchID, err := create(scanBatchOwner(scan, phaseType))
	if err != nil {
		plog.Warn("scan %d: %s CreateBatch errored; left submitting for a metadata lookup to settle: %v", scanID, phaseType, err)
		return
	}
	plog.Info("scan %d: %s batch submitted as %s", scanID, phaseType, batchID)
	if err := pm.scanStore.UpdatePhaseStatus(scanID, phaseType, "submitted", batchID); err != nil {
		// The row stays "submitting"; resolveSubmitting re-attaches by metadata.
		plog.Error("scan %d: could not record batch %s for %s: %v", scanID, batchID, phaseType, err)
	}
	// The collector (heartbeat or batch poller) finishes it.
}

func (pm *PipelineManager) runGroupsScanBatch(ctx context.Context, scanID int, authors []database.Author) {
	if !pm.beginPhase(scanID, "groups_scan") {
		return
	}
	defer pm.endPhase(scanID, "groups_scan")
	plog.Info("scan %d: starting groups scan (batch)", scanID)

	inputs, groups, err := pm.buildGroupsInput(authors)
	if err != nil {
		pm.failPhase(scanID, "groups_scan", err)
		return
	}
	if len(inputs) == 0 {
		plog.Info("scan %d: no duplicate groups found, marking groups scan complete", scanID)
		pm.completeEmptySource(ctx, scanID, "groups_scan")
		return
	}

	inputJSON, _ := json.Marshal(inputs)
	if err := pm.scanStore.SavePhaseData(scanID, "groups_scan", inputJSON, nil, nil); err != nil {
		pm.failPhase(scanID, "groups_scan", fmt.Errorf("save phase input: %w", err))
		return
	}
	if err := pm.saveGroupAuthorIDs(scanID, groups); err != nil {
		pm.failPhase(scanID, "groups_scan", err)
		return
	}
	pm.submitBatch(scanID, "groups_scan", func(owner map[string]string) (string, error) {
		return pm.parser.CreateBatchAuthorReview(ctx, inputs, owner)
	})
}

func (pm *PipelineManager) runFullScanBatch(ctx context.Context, scanID int, authors []database.Author) {
	if !pm.beginPhase(scanID, "full_scan") {
		return
	}
	defer pm.endPhase(scanID, "full_scan")
	plog.Info("scan %d: starting full scan (batch)", scanID)

	inputs, _, err := pm.fullScanInputs(scanID, authors)
	if err != nil {
		pm.failPhase(scanID, "full_scan", err)
		return
	}
	if len(inputs) == 0 {
		plog.Info("scan %d: no authors found, marking full scan complete", scanID)
		pm.completeEmptySource(ctx, scanID, "full_scan")
		return
	}
	pm.submitBatch(scanID, "full_scan", func(owner map[string]string) (string, error) {
		return pm.parser.CreateBatchAuthorDedup(ctx, inputs, owner)
	})
}

func (pm *PipelineManager) runEnrichment(ctx context.Context, scanID int, sourcePhase, enrichPhase string) {
	if !pm.beginPhase(scanID, enrichPhase) {
		return
	}
	defer pm.endPhase(scanID, enrichPhase)
	slog.Info("[AI Pipeline] Scan starting enrichment for", "scanID", scanID, "sourcePhase", sourcePhase)
	if _, err := pm.scanStore.CreatePhase(scanID, enrichPhase, ""); err != nil {
		slog.Error("[AI Pipeline] Scan error creating phase", "scanID", scanID, "enrichPhase", enrichPhase, "err", err)
		return
	}
	if err := pm.scanStore.UpdatePhaseStatus(scanID, enrichPhase, "processing", ""); err != nil {
		slog.Error("[AI Pipeline] Scan error updating status", "scanID", scanID, "enrichPhase", enrichPhase, "err", err)
		return
	}

	// Load suggestions from source phase
	sourcePhaseData, err := pm.scanStore.GetPhase(scanID, sourcePhase)
	if err != nil || sourcePhaseData == nil {
		pm.failPhase(scanID, enrichPhase, fmt.Errorf("get source phase %s: %w", sourcePhase, err))
		return
	}

	var suggestions []database.ScanSuggestion
	if len(sourcePhaseData.Suggestions) > 0 {
		if err := json.Unmarshal(sourcePhaseData.Suggestions, &suggestions); err != nil {
			pm.failPhase(scanID, enrichPhase, fmt.Errorf("parse suggestions from %s: %w", sourcePhase, err))
			return
		}
	}

	// Filter to medium/low confidence only — these are candidates for enrichment
	var uncertain []database.ScanSuggestion
	for _, s := range suggestions {
		if s.Confidence == "medium" || s.Confidence == "low" {
			uncertain = append(uncertain, s)
		}
	}

	if len(uncertain) == 0 {
		slog.Info("[AI Pipeline] Scan no uncertain suggestions in , skipping enrichment", "scanID", scanID, "sourcePhase", sourcePhase)
		// Save original suggestions as enriched (unchanged)
		suggestionsJSON, _ := json.Marshal(suggestions)
		_ = pm.scanStore.SavePhaseData(scanID, enrichPhase, nil, nil, suggestionsJSON)
		if err := pm.scanStore.UpdatePhaseStatus(scanID, enrichPhase, "complete", ""); err != nil {
			slog.Error("[AI Pipeline] Scan error updating status", "scanID", scanID, "enrichPhase", enrichPhase, "err", err)
			return
		}
		pm.OnPhaseComplete(ctx, scanID, enrichPhase)
		return
	}

	// For each uncertain suggestion, fetch book titles to enrich the context
	type enrichedInput struct {
		Suggestion  database.ScanSuggestion `json:"suggestion"`
		BookTitles  map[int][]string        `json:"book_titles"`
		OriginalIdx int                     `json:"original_idx"`
	}

	var enrichInputs []enrichedInput
	for _, s := range uncertain {
		bookTitles := make(map[int][]string)
		for _, authorID := range s.AuthorIDs {
			books, bErr := pm.mainStore.GetBooksByAuthorIDWithRoleCore(authorID)
			if bErr == nil {
				var titles []string
				for j, b := range books {
					if j >= 5 { // up to 5 titles for enrichment
						break
					}
					titles = append(titles, b.Title)
				}
				bookTitles[authorID] = titles
			}
		}
		enrichInputs = append(enrichInputs, enrichedInput{
			Suggestion: s,
			BookTitles: bookTitles,
		})
	}

	// Build enriched AuthorDiscoveryInput for re-submission
	var resubmitInputs []ai.AuthorDiscoveryInput
	for _, ei := range enrichInputs {
		for _, authorID := range ei.Suggestion.AuthorIDs {
			titles := ei.BookTitles[authorID]
			// Find the author name — look up from store
			author, aErr := pm.mainStore.GetAuthorByID(authorID)
			name := fmt.Sprintf("Author #%d", authorID)
			if aErr == nil && author != nil {
				name = author.Name
			}
			resubmitInputs = append(resubmitInputs, ai.AuthorDiscoveryInput{
				ID:           authorID,
				Name:         name,
				BookCount:    len(titles),
				SampleTitles: titles,
			})
		}
	}

	// Deduplicate by author ID
	seen := make(map[int]bool)
	var deduped []ai.AuthorDiscoveryInput
	for _, input := range resubmitInputs {
		if !seen[input.ID] {
			seen[input.ID] = true
			deduped = append(deduped, input)
		}
	}

	// Save enrichment input
	enrichInputJSON, _ := json.Marshal(deduped)

	if len(deduped) > 0 {
		// Re-submit to AI with enriched context
		discoveries, err := pm.parser.DiscoverAuthorDuplicates(ctx, deduped)
		if err != nil {
			// A shutdown is not an enrichment failure: completing with the
			// un-enriched suggestions would lose the enrichment for good.
			if abandonedForShutdown(ctx, scanID, enrichPhase) {
				return
			}
			// Enrichment failure is non-fatal — use original suggestions
			slog.Info("[AI Pipeline] Scan enrichment AI call failed for — using original suggestions", "scanID", scanID, "enrichPhase", enrichPhase, "err", err)
			suggestionsJSON, _ := json.Marshal(suggestions)
			_ = pm.scanStore.SavePhaseData(scanID, enrichPhase, enrichInputJSON, nil, suggestionsJSON)
			if err := pm.scanStore.UpdatePhaseStatus(scanID, enrichPhase, "complete", ""); err != nil {
				slog.Error("[AI Pipeline] Scan error updating status", "scanID", scanID, "enrichPhase", enrichPhase, "err", err)
				return
			}
			pm.OnPhaseComplete(ctx, scanID, enrichPhase)
			return
		}

		// Save raw enrichment output
		enrichOutputJSON, _ := json.Marshal(discoveries)

		// Merge: if enriched result upgrades confidence, replace in suggestions
		enrichedSuggestions := fullSuggestionsToScanSuggestions(discoveries)
		enrichedByIDs := make(map[string]database.ScanSuggestion)
		for _, es := range enrichedSuggestions {
			key := idsKey(es.AuthorIDs)
			enrichedByIDs[key] = es
		}

		// Build final merged suggestions list
		merged := make([]database.ScanSuggestion, len(suggestions))
		copy(merged, suggestions)
		for i, s := range merged {
			if s.Confidence != "medium" && s.Confidence != "low" {
				continue
			}
			key := idsKey(s.AuthorIDs)
			if enriched, ok := enrichedByIDs[key]; ok {
				// Only upgrade if enriched confidence is higher
				if confidenceRank(enriched.Confidence) > confidenceRank(s.Confidence) {
					merged[i].Confidence = enriched.Confidence
					merged[i].Reason = s.Reason + " [enriched: " + enriched.Reason + "]"
				}
			}
		}

		mergedJSON, _ := json.Marshal(merged)
		_ = pm.scanStore.SavePhaseData(scanID, enrichPhase, enrichInputJSON, enrichOutputJSON, mergedJSON)
	} else {
		// No inputs to enrich — pass through
		suggestionsJSON, _ := json.Marshal(suggestions)
		_ = pm.scanStore.SavePhaseData(scanID, enrichPhase, nil, nil, suggestionsJSON)
	}

	slog.Info("[AI Pipeline] Scan enrichment complete for", "scanID", scanID, "enrichPhase", enrichPhase)
	if err := pm.scanStore.UpdatePhaseStatus(scanID, enrichPhase, "complete", ""); err != nil {
		slog.Error("[AI Pipeline] Scan error updating status", "scanID", scanID, "enrichPhase", enrichPhase, "err", err)
		return
	}
	pm.OnPhaseComplete(ctx, scanID, enrichPhase)
}

func (pm *PipelineManager) runCrossValidation(ctx context.Context, scanID int) {
	// Both enrichment phases trigger this, and they can finish at the same
	// moment; drive can trigger it again after a restart. beginPhase makes all
	// but one of those a no-op.
	if !pm.beginPhase(scanID, "cross_validate") {
		return
	}
	defer pm.endPhase(scanID, "cross_validate")
	plog.Info("scan %d: starting cross-validation", scanID)
	// Each of these bailouts used to `return` bare, which left the scan
	// sitting at "scanning" forever and leaked its cancel func — so CancelScan
	// still believed the scan was live. Routing them through failPhase marks the
	// scan failed AND releases the waiting operation; a bare return here would
	// now hang RunScan for the op's whole timeout.
	if _, err := pm.scanStore.CreatePhase(scanID, "cross_validate", "local"); err != nil {
		pm.failPhase(scanID, "cross_validate", fmt.Errorf("create cross_validate phase: %w", err))
		return
	}
	if err := pm.scanStore.UpdatePhaseStatus(scanID, "cross_validate", "processing", ""); err != nil {
		pm.failPhase(scanID, "cross_validate", fmt.Errorf("mark cross_validate processing: %w", err))
		return
	}

	// Load groups and full phase suggestions
	// Check for enriched versions first, fall back to original
	groupsSuggestions := pm.loadBestSuggestions(scanID, "groups_enrich", "groups_scan")
	fullSuggestions := pm.loadBestSuggestions(scanID, "full_enrich", "full_scan")

	results := CrossValidate(scanID, groupsSuggestions, fullSuggestions)

	// Replace, never append: a re-run (restart mid-phase, or a duplicate
	// trigger) must leave one set of results, not a second copy of each.
	if err := pm.scanStore.ReplaceScanResults(scanID, results); err != nil {
		pm.failPhase(scanID, "cross_validate", fmt.Errorf("save results: %w", err))
		return
	}

	plog.Info("scan %d: cross-validation complete with %d results", scanID, len(results))
	if err := pm.scanStore.UpdatePhaseStatus(scanID, "cross_validate", "complete", ""); err != nil {
		pm.failPhase(scanID, "cross_validate", fmt.Errorf("mark cross_validate complete: %w", err))
		return
	}
	pm.completeScan(scanID)
}

// completeScan marks a scan whose cross-validation finished as complete and
// releases the operation waiting on it — the scan's only success path. A nil
// outcome is what marks the operation completed.
func (pm *PipelineManager) completeScan(scanID int) {
	if err := pm.scanStore.UpdateScanStatus(scanID, "complete"); err != nil {
		plog.Error("scan %d: could not mark scan complete: %v", scanID, err)
	}
	pm.report(scanID, 100, 100, "AI scan complete")
	pm.finishScan(scanID, nil)
}

// loadBestSuggestions loads suggestions from the enriched phase if available, otherwise the original.
func (pm *PipelineManager) loadBestSuggestions(scanID int, enrichPhase, originalPhase string) []database.ScanSuggestion {
	// Try enriched first
	phase, err := pm.scanStore.GetPhase(scanID, enrichPhase)
	if err == nil && phase != nil && phase.Status == "complete" && len(phase.Suggestions) > 0 {
		var suggestions []database.ScanSuggestion
		if err := json.Unmarshal(phase.Suggestions, &suggestions); err == nil {
			return suggestions
		}
	}

	// Fall back to original
	phase, err = pm.scanStore.GetPhase(scanID, originalPhase)
	if err == nil && phase != nil && len(phase.Suggestions) > 0 {
		var suggestions []database.ScanSuggestion
		if err := json.Unmarshal(phase.Suggestions, &suggestions); err == nil {
			return suggestions
		}
	}

	return nil
}

// PollBatchPhases advances every batch scan that is still scanning: it
// re-attaches phases left "submitting" whose batch can be found by metadata,
// and collects submitted batches that finished. The batch poller calls it from
// its author_review / author_dedup handlers, and each attached RunScan calls
// pollScanBatches for its own scan on every heartbeat. Running it more than
// once, or concurrently, is safe: collection claims the phase and re-checks
// the persisted row, so a batch is downloaded and applied once.
func (pm *PipelineManager) PollBatchPhases(ctx context.Context) {
	scans, err := pm.scanStore.ListScans()
	if err != nil {
		plog.Error("listing scans for batch polling: %v", err)
		return
	}
	for _, scan := range scans {
		if scan.Status != "scanning" || scan.Mode != "batch" {
			continue
		}
		pm.pollScanBatches(ctx, scan.ID)
	}
}

// pollScanBatches advances the batch phases of one scan.
func (pm *PipelineManager) pollScanBatches(ctx context.Context, scanID int) {
	phases, err := pm.scanStore.GetPhases(scanID)
	if err != nil {
		plog.Error("scan %d: reading phases for batch polling: %v", scanID, err)
		return
	}
	for _, phase := range phases {
		switch {
		case phase.Status == "submitting" && !pm.phaseClaimed(scanID, phase.PhaseType):
			// Left by a process that died inside CreateBatch, or by a
			// CreateBatch that errored. Claimed means a submit is running in
			// this process right now, and its outcome is not known yet.
			pm.resolveSubmitting(ctx, scanID, phase.PhaseType)
		case phase.Status == "submitted" && phase.BatchID != "":
			status, outputFileID, err := pm.parser.CheckBatchStatus(ctx, phase.BatchID)
			if err != nil {
				plog.Error("scan %d: polling batch %s: %v", scanID, phase.BatchID, err)
				continue
			}
			switch {
			case status == "completed":
				pm.handleBatchComplete(ctx, scanID, phase.PhaseType, outputFileID)
			case (status == "expired" || status == "cancelled" || status == "failed") && outputFileID != "":
				// OpenAI ended the batch, but the requests that finished have
				// output. Collect it: those answers are paid for.
				plog.Warn("scan %d: batch %s %s with partial output; collecting it", scanID, phase.BatchID, status)
				pm.handleBatchComplete(ctx, scanID, phase.PhaseType, outputFileID)
			case status == "expired" || status == "cancelled" || status == "failed":
				pm.failPhase(scanID, phase.PhaseType, fmt.Errorf("batch %s: %s with no output", phase.BatchID, status))
			default:
				plog.Debug("scan %d: batch %s status %s", scanID, phase.BatchID, status)
			}
		}
	}
}

// CollectBatch is the batch poller's handler body for one listed batch. It
// polls, then reports whether that batch is DONE with: nil when the phase
// owning it is terminal, or when no scan could own it (not ours); an error
// while it is still owed, so the poller does not journal it as handled and
// dispatches it again. Journaling a batch nothing collected would drop it
// from the poller's view for good.
func (pm *PipelineManager) CollectBatch(ctx context.Context, batchID string) error {
	pm.PollBatchPhases(ctx)
	scans, err := pm.scanStore.ListScans()
	if err != nil {
		return fmt.Errorf("list scans: %w", err)
	}
	awaitingAttach := false
	for _, scan := range scans {
		phases, err := pm.scanStore.GetPhases(scan.ID)
		if err != nil {
			return fmt.Errorf("read phases of scan %d: %w", scan.ID, err)
		}
		for _, p := range phases {
			if p.BatchID == batchID {
				if isTerminalPhase(p.Status) {
					return nil
				}
				return fmt.Errorf("batch %s not yet collected (scan %d %s is %s)", batchID, scan.ID, p.PhaseType, p.Status)
			}
			if p.Status == "submitting" && scan.Status == "scanning" {
				awaitingAttach = true
			}
		}
	}
	if awaitingAttach {
		return fmt.Errorf("batch %s may belong to a scan phase still awaiting re-attachment", batchID)
	}
	return nil
}

// batchGroupAuthorIDs returns the grouping a groups batch was submitted with.
// Scans submitted before that grouping was persisted fall back to rebuilding
// it from the current author table, which is what every scan did until then.
func (pm *PipelineManager) batchGroupAuthorIDs(scanID int) ([][]int, error) {
	arts, err := pm.scanStore.GetPhaseArtifacts(scanID, "groups_scan")
	if err != nil {
		return nil, fmt.Errorf("load group author ids: %w", err)
	}
	if raw, ok := arts[groupAuthorIDsArtifact]; ok {
		var ids [][]int
		if err := json.Unmarshal(raw, &ids); err != nil {
			return nil, fmt.Errorf("decode group author ids: %w", err)
		}
		return ids, nil
	}
	authors, err := pm.mainStore.GetAllAuthors()
	if err != nil {
		return nil, fmt.Errorf("get authors for mapping: %w", err)
	}
	_, groups, err := pm.buildGroupsInput(authors)
	if err != nil {
		return nil, fmt.Errorf("build groups for mapping: %w", err)
	}
	return groupAuthorIDs(groups), nil
}

// handleBatchComplete downloads and applies a completed batch phase, exactly
// once: it claims the phase and proceeds only while the persisted row still
// reads "submitted", so a second poller (or the same batch dispatched again
// after a restart) finds it complete and does nothing.
func (pm *PipelineManager) handleBatchComplete(ctx context.Context, scanID int, phaseType, outputFileID string) {
	if !pm.beginPhase(scanID, phaseType) {
		return
	}
	defer pm.endPhase(scanID, phaseType)
	phase, err := pm.scanStore.GetPhase(scanID, phaseType)
	if err != nil || phase == nil || phase.Status != "submitted" {
		return
	}
	plog.Info("scan %d: batch %s completed, downloading results", scanID, phase.BatchID)

	switch phaseType {
	case "groups_scan":
		suggestions, err := pm.parser.DownloadBatchGroupsResults(ctx, outputFileID)
		if err != nil {
			pm.downloadFailed(scanID, phaseType, err)
			return
		}
		groupIDs, err := pm.batchGroupAuthorIDs(scanID)
		if err != nil {
			pm.failPhase(scanID, phaseType, err)
			return
		}
		pm.finishSource(ctx, scanID, phaseType, phase.InputData, suggestions,
			groupsSuggestionsToScanSuggestions(suggestions, groupIDs))

	case "full_scan":
		discoveries, err := pm.parser.DownloadBatchResults(ctx, outputFileID)
		if err != nil {
			pm.downloadFailed(scanID, phaseType, err)
			return
		}
		pm.finishSource(ctx, scanID, phaseType, phase.InputData, discoveries,
			fullSuggestionsToScanSuggestions(discoveries))
	}
}

// downloadFailed handles a failed download of a finished batch's output. It
// is usually transient (a 5xx, a reset connection), so the phase stays
// "submitted" and the next poll downloads again; only maxDownloadAttempts
// consecutive failures fail the phase.
func (pm *PipelineManager) downloadFailed(scanID int, phaseType string, err error) {
	n := pm.bumpAttempts(scanID, phaseType, "download_attempts")
	if n >= maxDownloadAttempts {
		pm.failPhase(scanID, phaseType, fmt.Errorf("download batch results failed %d times: %w", n, err))
		return
	}
	plog.Warn("scan %d: download of %s results failed (attempt %d/%d), will retry: %v", scanID, phaseType, n, maxDownloadAttempts, err)
}

// idsKey creates a string key from a sorted list of IDs for map lookup.
func idsKey(ids []int) string {
	if len(ids) == 0 {
		return ""
	}
	b, _ := json.Marshal(ids)
	return string(b)
}

// confidenceRank returns a numeric rank for confidence levels.
func confidenceRank(c string) int {
	switch c {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}
