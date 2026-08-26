// file: internal/aiscan/pipeline.go
// version: 4.0.0
// guid: b8c4d0e2-5f6a-7b8c-9d0e-1f2a3b4c5d6e
// last-edited: 2026-08-22

package aiscan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
)

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
	parser    *ai.OpenAIParser
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
}

// NewPipelineManager creates a new pipeline manager.
func NewPipelineManager(scanStore *database.AIScanStore, mainStore Store, parser *ai.OpenAIParser) *PipelineManager {
	return &PipelineManager{
		scanStore: scanStore,
		mainStore: mainStore,
		parser:    parser,
		cancels:   make(map[int]context.CancelFunc),
		sinks:     make(map[int]ProgressSink),
		dones:     make(map[int]chan error),
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
		if p.Status == "pending" || p.Status == "processing" || p.Status == "submitted" {
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
		// Cross-validate when both enrichments are done
		groupsDone := phaseStates["groups_enrich"] == "complete" || (phaseStates["groups_scan"] == "complete" && phaseStates["groups_enrich"] == "")
		fullDone := phaseStates["full_enrich"] == "complete" || (phaseStates["full_scan"] == "complete" && phaseStates["full_enrich"] == "")
		if completedPhase == "groups_enrich" {
			groupsDone = true
		}
		if completedPhase == "full_enrich" {
			fullDone = true
		}
		if groupsDone && fullDone {
			next = append(next, "cross_validate")
		}
	}

	return next
}

// ErrScanCanceled is the terminal outcome of a scan stopped by an operator.
var ErrScanCanceled = errors.New("ai scan canceled")

// ErrRealtimeNotResumable is the terminal outcome of a realtime scan whose
// process died mid-flight. Unlike a batch scan — which OpenAI is still holding
// and PollBatchPhases will still collect — a realtime scan's in-flight requests
// died with the process, so there is nothing left to attach to.
var ErrRealtimeNotResumable = errors.New("realtime ai scan interrupted and cannot be resumed")

// heartbeatInterval is how often an attached scan republishes progress while
// waiting. The registry watchdog cancels an op that reports nothing for
// ProgressTimeout (default 5m); a submitted batch phase can legitimately sit
// silent for hours, so the wait loop must keep reporting on its behalf.
const heartbeatInterval = 60 * time.Second

// resumeAction is what RunScan should do with a scan, given the state its
// phases were left in.
type resumeAction int

const (
	// resumeLaunch: no phase has started. Launch the phase goroutines.
	resumeLaunch resumeAction = iota
	// resumeAttach: work is in flight and something outside this process will
	// finish it. Wait, do not re-launch.
	resumeAttach
	// resumeImpossible: work started but nothing will ever finish it.
	resumeImpossible
)

// decideResume is the launch-vs-attach decision, split out from RunScan so it
// can be tested without a store — it is the part that must be right, because
// getting it wrong either hangs an operation forever or re-runs a paid
// whole-library LLM pass against OpenAI.
//
// It reads ONLY persisted phase state. The op declares ResumePolicy=ResumeRestart,
// so Run is re-entered after a crash with nothing in memory to consult.
//
// The mode split is the crux. A batch scan's work is held by OpenAI and
// PollBatchPhases (batch_poller.go:181) will collect it whenever it completes,
// with or without this process — so re-attaching is correct. A realtime scan's
// work was in-flight HTTP requests issued by the process that died, so nothing
// will ever advance its phases; attaching would wait until the op's 24h timeout.
func decideResume(mode string, phases []database.ScanPhase) resumeAction {
	started := false
	for _, p := range phases {
		if p.Status != "pending" {
			started = true
			break
		}
	}
	if !started {
		return resumeLaunch
	}
	if mode == "batch" {
		return resumeAttach
	}
	return resumeImpossible
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
// It either LAUNCHES the phase goroutines or ATTACHES to work already in
// flight, and decides which from the scan's PERSISTED phase state — never from
// pm's in-process maps. That distinction is the whole point: the op declares
// ResumePolicy=ResumeRestart, so on a restart Run is re-entered with the same
// params, and a process-local guard cannot survive the very restart it exists
// to handle. Deciding from memory would re-launch the phases and re-run a paid
// whole-library LLM pass against OpenAI.
func (pm *PipelineManager) RunScan(ctx context.Context, scanID int, sink ProgressSink) error {
	scan, err := pm.scanStore.GetScan(scanID)
	if err != nil {
		return fmt.Errorf("get scan %d: %w", scanID, err)
	}
	if scan == nil {
		return fmt.Errorf("scan %d not found", scanID)
	}

	phases, err := pm.scanStore.GetPhases(scanID)
	if err != nil {
		return fmt.Errorf("get phases for scan %d: %w", scanID, err)
	}
	action := decideResume(scan.Mode, phases)
	if action == resumeImpossible {
		pm.cleanupScan(scanID, "failed")
		return ErrRealtimeNotResumable
	}
	started := action == resumeAttach

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

	if started {
		slog.Info("[AI Pipeline] re-attaching to in-flight batch scan", "scanID", scanID)
		pm.report(scanID, 0, 100, "Re-attached to in-flight batch scan")
	} else {
		if err := pm.scanStore.UpdateScanStatus(scanID, "scanning"); err != nil {
			pm.finishScan(scanID, nil)
			return fmt.Errorf("update scan status: %w", err)
		}
		authors, err := pm.mainStore.GetAllAuthors()
		if err != nil {
			pm.finishScan(scanID, nil)
			return fmt.Errorf("get authors: %w", err)
		}
		pm.report(scanID, 0, 100, "Starting AI scan pipeline...")
		if scan.Mode == "batch" {
			go pm.runGroupsScanBatch(scanCtx, scanID, authors)
			go pm.runFullScanBatch(scanCtx, scanID, authors)
		} else {
			go pm.runGroupsScanRealtime(scanCtx, scanID, authors)
			go pm.runFullScanRealtime(scanCtx, scanID, authors)
		}
	}

	return pm.waitForScan(ctx, scanID, done)
}

// waitForScan blocks until the scan reaches a terminal state, the operation's
// context is canceled, and heartbeats progress in the meantime so the registry
// watchdog does not mistake a legitimately quiet batch wait for a stuck op.
func (pm *PipelineManager) waitForScan(ctx context.Context, scanID int, done <-chan error) error {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case err := <-done:
			return err

		case <-ctx.Done():
			// Route through CancelScan, not a bare context cancel: a submitted
			// batch is held by OpenAI and keeps costing money until the batch
			// job itself is canceled, which only CancelScan does.
			if cerr := pm.CancelScan(scanID); cerr != nil {
				slog.Warn("[AI Pipeline] cancel on context done failed", "scanID", scanID, "err", cerr)
			}
			return ctx.Err()

		case <-ticker.C:
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
func groupsSuggestionsToScanSuggestions(suggestions []ai.AuthorDedupSuggestion, groups []dedup.AuthorDedupGroup) []database.ScanSuggestion {
	var result []database.ScanSuggestion
	for _, s := range suggestions {
		// Normalize initials formatting
		canonicalName := dedup.NormalizeAuthorName(s.CanonicalName)

		// Build author IDs from the group
		var authorIDs []int
		if s.GroupIndex >= 0 && s.GroupIndex < len(groups) {
			g := groups[s.GroupIndex]
			authorIDs = append(authorIDs, g.Canonical.ID)
			for _, v := range g.Variants {
				authorIDs = append(authorIDs, v.ID)
			}
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

func (pm *PipelineManager) runGroupsScanRealtime(ctx context.Context, scanID int, authors []database.Author) {
	slog.Info("[AI Pipeline] Scan starting groups scan (realtime)", "scanID", scanID)
	if err := pm.scanStore.UpdatePhaseStatus(scanID, "groups_scan", "processing", ""); err != nil {
		slog.Error("[AI Pipeline] Scan error updating groups_scan status", "scanID", scanID, "err", err)
		return
	}

	// Build heuristic groups using Jaro-Winkler logic
	inputs, groups, err := pm.buildGroupsInput(authors)
	if err != nil {
		pm.failPhase(scanID, "groups_scan", err)
		return
	}

	if len(inputs) == 0 {
		slog.Info("[AI Pipeline] Scan no duplicate groups found, skipping groups scan", "scanID", scanID)
		// Save empty results
		emptySuggestions, _ := json.Marshal([]database.ScanSuggestion{})
		_ = pm.scanStore.SavePhaseData(scanID, "groups_scan", nil, nil, emptySuggestions)
		if err := pm.scanStore.UpdatePhaseStatus(scanID, "groups_scan", "complete", ""); err != nil {
			slog.Error("[AI Pipeline] Scan error updating groups_scan status", "scanID", scanID, "err", err)
			return
		}
		pm.OnPhaseComplete(ctx, scanID, "groups_scan")
		return
	}

	// Save input data
	inputJSON, _ := json.Marshal(inputs)
	_ = pm.scanStore.SavePhaseData(scanID, "groups_scan", inputJSON, nil, nil)

	// Call AI
	suggestions, err := pm.parser.ReviewAuthorDuplicates(ctx, inputs)
	if err != nil {
		pm.failPhase(scanID, "groups_scan", fmt.Errorf("AI review failed: %w", err))
		return
	}

	// Save raw output
	outputJSON, _ := json.Marshal(suggestions)

	// Convert to normalized ScanSuggestions
	scanSuggestions := groupsSuggestionsToScanSuggestions(suggestions, groups)
	suggestionsJSON, _ := json.Marshal(scanSuggestions)

	if err := pm.scanStore.SavePhaseData(scanID, "groups_scan", inputJSON, outputJSON, suggestionsJSON); err != nil {
		pm.failPhase(scanID, "groups_scan", fmt.Errorf("save phase data: %w", err))
		return
	}

	slog.Info("[AI Pipeline] Scan groups scan complete — suggestions from groups", "scanID", scanID, "scanSuggestions_count", len(scanSuggestions), "inputs_count", len(inputs))
	if err := pm.scanStore.UpdatePhaseStatus(scanID, "groups_scan", "complete", ""); err != nil {
		slog.Error("[AI Pipeline] Scan error updating groups_scan status", "scanID", scanID, "err", err)
		return
	}
	pm.OnPhaseComplete(ctx, scanID, "groups_scan")
}

func (pm *PipelineManager) runFullScanRealtime(ctx context.Context, scanID int, authors []database.Author) {
	slog.Info("[AI Pipeline] Scan starting full scan (realtime)", "scanID", scanID)
	if err := pm.scanStore.UpdatePhaseStatus(scanID, "full_scan", "processing", ""); err != nil {
		slog.Error("[AI Pipeline] Scan error updating full_scan status", "scanID", scanID, "err", err)
		return
	}

	inputs := pm.buildFullInput(authors)
	if len(inputs) == 0 {
		slog.Info("[AI Pipeline] Scan no authors found, skipping full scan", "scanID", scanID)
		emptySuggestions, _ := json.Marshal([]database.ScanSuggestion{})
		_ = pm.scanStore.SavePhaseData(scanID, "full_scan", nil, nil, emptySuggestions)
		if err := pm.scanStore.UpdatePhaseStatus(scanID, "full_scan", "complete", ""); err != nil {
			slog.Error("[AI Pipeline] Scan error updating full_scan status", "scanID", scanID, "err", err)
			return
		}
		pm.OnPhaseComplete(ctx, scanID, "full_scan")
		return
	}

	// Save input data
	inputJSON, _ := json.Marshal(inputs)
	_ = pm.scanStore.SavePhaseData(scanID, "full_scan", inputJSON, nil, nil)

	// Chunk if >500 authors to manage token limits
	const chunkSize = 500
	var allDiscoveries []ai.AuthorDiscoverySuggestion

	for start := 0; start < len(inputs); start += chunkSize {
		end := min(start+chunkSize, len(inputs))
		chunk := inputs[start:end]

		discoveries, err := pm.parser.DiscoverAuthorDuplicates(ctx, chunk)
		if err != nil {
			pm.failPhase(scanID, "full_scan", fmt.Errorf("AI discovery failed (chunk %d-%d): %w", start, end, err))
			return
		}
		allDiscoveries = append(allDiscoveries, discoveries...)
	}

	// Save raw output
	outputJSON, _ := json.Marshal(allDiscoveries)

	// Convert to normalized ScanSuggestions
	scanSuggestions := fullSuggestionsToScanSuggestions(allDiscoveries)
	suggestionsJSON, _ := json.Marshal(scanSuggestions)

	if err := pm.scanStore.SavePhaseData(scanID, "full_scan", inputJSON, outputJSON, suggestionsJSON); err != nil {
		pm.failPhase(scanID, "full_scan", fmt.Errorf("save phase data: %w", err))
		return
	}

	slog.Info("[AI Pipeline] Scan full scan complete — suggestions from authors", "scanID", scanID, "scanSuggestions_count", len(scanSuggestions), "inputs_count", len(inputs))
	if err := pm.scanStore.UpdatePhaseStatus(scanID, "full_scan", "complete", ""); err != nil {
		slog.Error("[AI Pipeline] Scan error updating full_scan status", "scanID", scanID, "err", err)
		return
	}
	pm.OnPhaseComplete(ctx, scanID, "full_scan")
}

func (pm *PipelineManager) runGroupsScanBatch(ctx context.Context, scanID int, authors []database.Author) {
	slog.Info("[AI Pipeline] Scan starting groups scan (batch)", "scanID", scanID)

	inputs, _, err := pm.buildGroupsInput(authors)
	if err != nil {
		pm.failPhase(scanID, "groups_scan", err)
		return
	}

	if len(inputs) == 0 {
		slog.Info("[AI Pipeline] Scan no duplicate groups found, marking groups scan complete", "scanID", scanID)
		emptySuggestions, _ := json.Marshal([]database.ScanSuggestion{})
		_ = pm.scanStore.SavePhaseData(scanID, "groups_scan", nil, nil, emptySuggestions)
		if err := pm.scanStore.UpdatePhaseStatus(scanID, "groups_scan", "complete", ""); err != nil {
			slog.Error("[AI Pipeline] Scan error updating groups_scan status", "scanID", scanID, "err", err)
		}
		pm.OnPhaseComplete(ctx, scanID, "groups_scan")
		return
	}

	// Save input data
	inputJSON, _ := json.Marshal(inputs)
	_ = pm.scanStore.SavePhaseData(scanID, "groups_scan", inputJSON, nil, nil)

	// Create the batch job
	batchID, err := pm.parser.CreateBatchAuthorReview(ctx, inputs)
	if err != nil {
		pm.failPhase(scanID, "groups_scan", fmt.Errorf("create batch: %w", err))
		return
	}

	slog.Info("[AI Pipeline] Scan groups batch submitted — batch_id", "scanID", scanID, "batchID", batchID)
	if err := pm.scanStore.UpdatePhaseStatus(scanID, "groups_scan", "submitted", batchID); err != nil {
		slog.Error("[AI Pipeline] Scan error updating groups_scan status", "scanID", scanID, "err", err)
	}
	// Scheduler will poll for completion
}

func (pm *PipelineManager) runFullScanBatch(ctx context.Context, scanID int, authors []database.Author) {
	slog.Info("[AI Pipeline] Scan starting full scan (batch)", "scanID", scanID)

	inputs := pm.buildFullInput(authors)
	if len(inputs) == 0 {
		slog.Info("[AI Pipeline] Scan no authors found, marking full scan complete", "scanID", scanID)
		emptySuggestions, _ := json.Marshal([]database.ScanSuggestion{})
		_ = pm.scanStore.SavePhaseData(scanID, "full_scan", nil, nil, emptySuggestions)
		if err := pm.scanStore.UpdatePhaseStatus(scanID, "full_scan", "complete", ""); err != nil {
			slog.Error("[AI Pipeline] Scan error updating full_scan status", "scanID", scanID, "err", err)
		}
		pm.OnPhaseComplete(ctx, scanID, "full_scan")
		return
	}

	// Save input data
	inputJSON, _ := json.Marshal(inputs)
	_ = pm.scanStore.SavePhaseData(scanID, "full_scan", inputJSON, nil, nil)

	// Create the batch job
	batchID, err := pm.parser.CreateBatchAuthorDedup(ctx, inputs)
	if err != nil {
		pm.failPhase(scanID, "full_scan", fmt.Errorf("create batch: %w", err))
		return
	}

	slog.Info("[AI Pipeline] Scan full batch submitted — batch_id", "scanID", scanID, "batchID", batchID)
	if err := pm.scanStore.UpdatePhaseStatus(scanID, "full_scan", "submitted", batchID); err != nil {
		slog.Error("[AI Pipeline] Scan error updating full_scan status", "scanID", scanID, "err", err)
	}
	// Scheduler will poll for completion
}

func (pm *PipelineManager) runEnrichment(ctx context.Context, scanID int, sourcePhase, enrichPhase string) {
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
	slog.Info("[AI Pipeline] Scan starting cross-validation", "scanID", scanID)
	// Each of these three bailouts used to `return` bare, which left the scan
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

	// Save results
	for i := range results {
		if err := pm.scanStore.SaveScanResult(&results[i]); err != nil {
			slog.Error("[AI Pipeline] Scan error saving result", "scanID", scanID, "err", err)
		}
	}

	slog.Info("[AI Pipeline] Scan cross-validation complete — results", "scanID", scanID, "results_count", len(results))
	if err := pm.scanStore.UpdatePhaseStatus(scanID, "cross_validate", "complete", ""); err != nil {
		pm.failPhase(scanID, "cross_validate", fmt.Errorf("mark cross_validate complete: %w", err))
		return
	}
	if err := pm.scanStore.UpdateScanStatus(scanID, "complete"); err != nil {
		slog.Error("[AI Pipeline] Scan error updating scan status", "scanID", scanID, "err", err)
	}

	pm.report(scanID, 100, 100, fmt.Sprintf("AI scan complete — %d results", len(results)))

	// The scan's only success path. A nil outcome is what marks the operation
	// completed; there is no separate "write completed status" call any more.
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

// PollBatchPhases checks all "submitted" batch phases and completes them if the batch is done.
// Called periodically by the scheduler.
func (pm *PipelineManager) PollBatchPhases(ctx context.Context) {
	// Get all scans that are in "scanning" status
	scans, err := pm.scanStore.ListScans()
	if err != nil {
		slog.Error("[AI Pipeline] Error listing scans for batch polling", "err", err)
		return
	}

	for _, scan := range scans {
		if scan.Status != "scanning" {
			continue
		}
		if scan.Mode != "batch" {
			continue
		}

		phases, err := pm.scanStore.GetPhases(scan.ID)
		if err != nil {
			continue
		}

		for _, phase := range phases {
			if phase.Status != "submitted" || phase.BatchID == "" {
				continue
			}

			status, outputFileID, err := pm.parser.CheckBatchStatus(ctx, phase.BatchID)
			if err != nil {
				slog.Error("[AI Pipeline] Scan error polling batch", "scan", scan.ID, "phase", phase.BatchID, "err", err)
				continue
			}

			switch status {
			case "completed":
				pm.handleBatchComplete(ctx, scan.ID, phase, outputFileID)
			case "failed", "expired", "cancelled":
				pm.failPhase(scan.ID, phase.PhaseType, fmt.Errorf("batch %s: %s", phase.BatchID, status))
			default:
				// Still processing — do nothing
				slog.Info("[AI Pipeline] Scan batch status", "scan", scan.ID, "phase", phase.BatchID, "status", status)
			}
		}
	}
}

// handleBatchComplete downloads and processes results for a completed batch phase.
func (pm *PipelineManager) handleBatchComplete(ctx context.Context, scanID int, phase database.ScanPhase, outputFileID string) {
	slog.Info("[AI Pipeline] Scan batch completed, downloading results", "scanID", scanID, "phase", phase.BatchID)

	switch phase.PhaseType {
	case "groups_scan":
		suggestions, err := pm.parser.DownloadBatchGroupsResults(ctx, outputFileID)
		if err != nil {
			pm.failPhase(scanID, phase.PhaseType, fmt.Errorf("download batch results: %w", err))
			return
		}

		// Rebuild groups to map group_index to author IDs
		authors, err := pm.mainStore.GetAllAuthors()
		if err != nil {
			pm.failPhase(scanID, phase.PhaseType, fmt.Errorf("get authors for mapping: %w", err))
			return
		}
		_, groups, err := pm.buildGroupsInput(authors)
		if err != nil {
			pm.failPhase(scanID, phase.PhaseType, fmt.Errorf("build groups for mapping: %w", err))
			return
		}

		outputJSON, _ := json.Marshal(suggestions)
		scanSuggestions := groupsSuggestionsToScanSuggestions(suggestions, groups)
		suggestionsJSON, _ := json.Marshal(scanSuggestions)

		_ = pm.scanStore.SavePhaseData(scanID, phase.PhaseType, phase.InputData, outputJSON, suggestionsJSON)

	case "full_scan":
		discoveries, err := pm.parser.DownloadBatchResults(ctx, outputFileID)
		if err != nil {
			pm.failPhase(scanID, phase.PhaseType, fmt.Errorf("download batch results: %w", err))
			return
		}

		outputJSON, _ := json.Marshal(discoveries)
		scanSuggestions := fullSuggestionsToScanSuggestions(discoveries)
		suggestionsJSON, _ := json.Marshal(scanSuggestions)

		_ = pm.scanStore.SavePhaseData(scanID, phase.PhaseType, phase.InputData, outputJSON, suggestionsJSON)
	}

	slog.Info("[AI Pipeline] Scan batch results processed", "scanID", scanID, "phase", phase.PhaseType)
	if err := pm.scanStore.UpdatePhaseStatus(scanID, phase.PhaseType, "complete", ""); err != nil {
		slog.Error("[AI Pipeline] Scan error updating status", "scanID", scanID, "phase", phase.PhaseType, "err", err)
		return
	}
	pm.OnPhaseComplete(ctx, scanID, phase.PhaseType)
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
