// file: internal/aiscan/pipeline.go
// version: 3.2.0
// guid: b8c4d0e2-5f6a-7b8c-9d0e-1f2a3b4c5d6e
// last-edited: 2026-07-05

package aiscan

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	ulid "github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
)

// Store is the narrow slice of database.Store this service uses.
type Store interface {
	database.AuthorReader
	database.OperationStore
}

// PipelineManager coordinates the multi-pass AI author dedup pipeline.
type PipelineManager struct {
	scanStore *database.AIScanStore
	mainStore Store
	parser    *ai.OpenAIParser
	mu        sync.Mutex
	// cancels tracks cancel functions for active scans, keyed by scan ID.
	cancels map[int]context.CancelFunc
}

// NewPipelineManager creates a new pipeline manager.
func NewPipelineManager(scanStore *database.AIScanStore, mainStore Store, parser *ai.OpenAIParser) *PipelineManager {
	return &PipelineManager{
		scanStore: scanStore,
		mainStore: mainStore,
		parser:    parser,
		cancels:   make(map[int]context.CancelFunc),
	}
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
	pm.mu.Lock()
	delete(pm.cancels, scanID)
	pm.mu.Unlock()

	// Mark any in-progress phases as canceled/failed
	phases, _ := pm.scanStore.GetPhases(scanID)
	for _, p := range phases {
		if p.Status == "pending" || p.Status == "processing" || p.Status == "submitted" {
			_ = pm.scanStore.UpdatePhaseStatus(scanID, p.PhaseType, status, "")
		}
	}
	_ = pm.scanStore.UpdateScanStatus(scanID, status)

	// Update the operation record too
	scan, _ := pm.scanStore.GetScan(scanID)
	if scan != nil && scan.OperationID != "" {
		_ = pm.mainStore.UpdateOperationStatus(scan.OperationID, status, 0, 0, "scan "+status)
	}
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

// StartScan creates a new scan, registers it as an operation, and kicks off Phase 1.
func (pm *PipelineManager) StartScan(ctx context.Context, mode string) (*database.Scan, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	models := map[string]string{"groups": "gpt-5-mini", "full": "o4-mini"}

	authors, err := pm.mainStore.GetAllAuthors()
	if err != nil {
		return nil, fmt.Errorf("get authors: %w", err)
	}

	scan, err := pm.scanStore.CreateScan(mode, models, len(authors))
	if err != nil {
		return nil, fmt.Errorf("create scan: %w", err)
	}

	// Create an operation record so the scan appears in the operations list
	opID := ulid.Make().String()
	detail := fmt.Sprintf("AI scan #%d (%s mode, %d authors)", scan.ID, mode, len(authors))
	if _, err := pm.mainStore.CreateOperation(opID, "ai-author-scan", &detail); err != nil {
		slog.Warn("[AI Pipeline] Warning failed to create operation record", "err", err)
	} else {
		scan.OperationID = opID
		// Re-save scan with operation ID
		pm.scanStore.UpdateScanOperationID(scan.ID, opID)
		_ = pm.mainStore.UpdateOperationStatus(opID, "running", 0, 100, "Starting AI scan pipeline...")
	}

	// Create phase records
	if _, err := pm.scanStore.CreatePhase(scan.ID, "groups_scan", models["groups"]); err != nil {
		return nil, fmt.Errorf("create groups phase: %w", err)
	}
	if _, err := pm.scanStore.CreatePhase(scan.ID, "full_scan", models["full"]); err != nil {
		return nil, fmt.Errorf("create full phase: %w", err)
	}

	if err := pm.scanStore.UpdateScanStatus(scan.ID, "scanning"); err != nil {
		return nil, fmt.Errorf("update scan status: %w", err)
	}

	// Create a cancellable background context for the goroutines
	scanCtx, cancel := context.WithCancel(context.Background())
	pm.cancels[scan.ID] = cancel

	if mode == "batch" {
		go pm.runGroupsScanBatch(scanCtx, scan.ID, authors)
		go pm.runFullScanBatch(scanCtx, scan.ID, authors)
	} else {
		go pm.runGroupsScanRealtime(scanCtx, scan.ID, authors)
		go pm.runFullScanRealtime(scanCtx, scan.ID, authors)
	}

	return scan, nil
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

	// Update operation progress (rough: each phase ~20% of total pipeline)
	scan, _ := pm.scanStore.GetScan(scanID)
	if scan != nil && scan.OperationID != "" {
		pct := completedCount * 20
		if pct > 90 {
			pct = 90
		}
		_ = pm.mainStore.UpdateOperationStatus(scan.OperationID, "running", pct, 100,
			fmt.Sprintf("Phase %s complete", completedPhase))
	}

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

	// Update operation record
	scan, _ := pm.scanStore.GetScan(scanID)
	if scan != nil && scan.OperationID != "" {
		errMsg := err.Error()
		_ = pm.mainStore.UpdateOperationError(scan.OperationID, errMsg)
	}

	// Clean up cancel func
	pm.mu.Lock()
	delete(pm.cancels, scanID)
	pm.mu.Unlock()
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
		end := start + chunkSize
		if end > len(inputs) {
			end = len(inputs)
		}
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
	if _, err := pm.scanStore.CreatePhase(scanID, "cross_validate", "local"); err != nil {
		slog.Error("[AI Pipeline] Scan error creating cross_validate phase", "scanID", scanID, "err", err)
		return
	}
	if err := pm.scanStore.UpdatePhaseStatus(scanID, "cross_validate", "processing", ""); err != nil {
		slog.Error("[AI Pipeline] Scan error updating cross_validate status", "scanID", scanID, "err", err)
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
		slog.Error("[AI Pipeline] Scan error updating cross_validate status", "scanID", scanID, "err", err)
		return
	}
	if err := pm.scanStore.UpdateScanStatus(scanID, "complete"); err != nil {
		slog.Error("[AI Pipeline] Scan error updating scan status", "scanID", scanID, "err", err)
	}

	// Update operation record
	scan, _ := pm.scanStore.GetScan(scanID)
	if scan != nil && scan.OperationID != "" {
		_ = pm.mainStore.UpdateOperationStatus(scan.OperationID, "completed", 100, 100,
			fmt.Sprintf("AI scan complete — %d results", len(results)))
	}

	// Clean up cancel func
	pm.mu.Lock()
	delete(pm.cancels, scanID)
	pm.mu.Unlock()
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
