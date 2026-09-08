// file: internal/database/ai_scan_store.go
// version: 2.1.0
// last-edited: 2026-09-08
// guid: a7b3c9d1-4e5f-6a7b-8c9d-0e1f2a3b4c5d

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// AIScanStore persists AI scan data (scan history, phases, results) inside a
// PebbleDB instance. It can operate in two modes:
//
//   - Standalone (NewAIScanStore): opens and owns its own Pebble file at the
//     given path. Keys have no prefix.
//   - Shared (NewAIScanStoreFromDB): reuses the caller's *pebble.DB. All keys
//     are namespaced under "aiscan:" to avoid collisions with the host store.
//     Close and Optimize are no-ops in this mode.
//
// Key Schema (relative to prefix):
//   - counter:scan              -> next scan ID
//   - counter:scan_result       -> next scan result ID
//   - scan:<id>                 -> Scan JSON
//   - scan_phase:<scanID>:<phaseType> -> ScanPhase JSON
//   - scan_result:<scanID>:<resultID> -> ScanResult JSON
type AIScanStore struct {
	db     *pebble.DB
	prefix string // "aiscan:" when shared, "" when standalone
	owned  bool   // if false, Close and Optimize are no-ops
}

// Scan represents a full pipeline run.
type Scan struct {
	ID          int               `json:"id"`
	Status      string            `json:"status"` // pending, scanning, enriching, cross_validating, complete, failed, canceled
	Mode        string            `json:"mode"`   // batch, realtime
	Models      map[string]string `json:"models"` // {groups: "gpt-5-mini", full: "o4-mini"}
	AuthorCount int               `json:"author_count"`
	OperationID string            `json:"operation_id,omitempty"` // links to main operations store for visibility/cancel
	CreatedAt   time.Time         `json:"created_at"`
	CompletedAt *time.Time        `json:"completed_at,omitempty"`
}

// ScanPhase represents one phase of the pipeline.
type ScanPhase struct {
	ScanID      int             `json:"scan_id"`
	PhaseType   string          `json:"phase_type"` // groups_scan, full_scan, groups_enrich, full_enrich, cross_validate
	Status      string          `json:"status"`     // pending, submitted, processing, complete, failed
	BatchID     string          `json:"batch_id,omitempty"`
	Model       string          `json:"model"`
	InputData   json.RawMessage `json:"input_data,omitempty"`
	OutputData  json.RawMessage `json:"output_data,omitempty"`
	Suggestions json.RawMessage `json:"suggestions,omitempty"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
}

// ScanSuggestion is the normalized suggestion from any phase.
// Note: Roles uses json.RawMessage to avoid importing the ai package (the actual SuggestionRoles struct is in internal/ai).
type ScanSuggestion struct {
	Action        string          `json:"action"`
	CanonicalName string          `json:"canonical_name"`
	Reason        string          `json:"reason"`
	Confidence    string          `json:"confidence"`
	AuthorIDs     []int           `json:"author_ids,omitempty"`
	GroupIndex    int             `json:"group_index,omitempty"`
	Roles         json.RawMessage `json:"roles,omitempty"`
	Source        string          `json:"source"` // groups_scan, full_scan, groups_enrich, full_enrich
}

// ScanResult is the final cross-validated output.
type ScanResult struct {
	ID         int            `json:"id"`
	ScanID     int            `json:"scan_id"`
	Agreement  string         `json:"agreement"` // agreed, groups_only, full_only, disagreed
	Suggestion ScanSuggestion `json:"suggestion"`
	Applied    bool           `json:"applied"`
	AppliedAt  *time.Time     `json:"applied_at,omitempty"`
}

// NewAIScanStore creates a standalone AIScanStore that owns its own PebbleDB at path.
func NewAIScanStore(path string) (*AIScanStore, error) {
	db, err := pebble.Open(path, &pebble.Options{
		FormatMajorVersion: pebble.FormatNewest,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open AI scan DB: %w", err)
	}

	store := &AIScanStore{db: db, prefix: "", owned: true}
	if err := store.initCounters(); err != nil {
		db.Close()
		return nil, err
	}

	slog.Info("AI Scan DB opened at", "path", path)
	return store, nil
}

// NewAIScanStoreFromDB creates an AIScanStore that shares an existing *pebble.DB.
// All keys are namespaced under "aiscan:" to avoid collisions. Close and Optimize
// are no-ops — the caller owns the DB lifecycle.
func NewAIScanStoreFromDB(db *pebble.DB) (*AIScanStore, error) {
	store := &AIScanStore{db: db, prefix: "aiscan:", owned: false}
	if err := store.initCounters(); err != nil {
		return nil, err
	}
	return store, nil
}

// initCounters ensures counter keys exist (value "1") if not already present.
func (s *AIScanStore) initCounters() error {
	for _, name := range []string{"scan", "scan_result"} {
		k := s.k("counter:%s", name)
		if _, closer, err := s.db.Get(k); err == pebble.ErrNotFound {
			if err := s.db.Set(k, []byte("1"), pebble.Sync); err != nil {
				return fmt.Errorf("failed to initialize counter %s: %w", name, err)
			}
		} else if err == nil {
			closer.Close()
		} else {
			return fmt.Errorf("failed to check counter %s: %w", name, err)
		}
	}
	return nil
}

// k builds a prefixed key. Use like fmt.Sprintf but the prefix is prepended.
func (s *AIScanStore) k(format string, args ...any) []byte {
	if len(args) == 0 {
		return []byte(s.prefix + format)
	}
	return []byte(s.prefix + fmt.Sprintf(format, args...))
}

// Close closes the underlying PebbleDB. No-op when sharing an external DB.
func (s *AIScanStore) Close() error {
	if !s.owned {
		return nil
	}
	return s.db.Close()
}

// Optimize compacts the PebbleDB. No-op when sharing an external DB (compaction
// is the host store's responsibility).
func (s *AIScanStore) Optimize(ctx context.Context) error {
	if !s.owned {
		return nil
	}
	return s.db.Compact(ctx, nil, []byte{0xff}, false)
}

// nextID atomically reads and increments the counter for the given entity type.
func (s *AIScanStore) nextID(counter string) (int, error) {
	key := s.k("counter:%s", counter)

	value, closer, err := s.db.Get(key)
	if err != nil {
		return 0, err
	}
	defer closer.Close()

	id, err := strconv.Atoi(string(value))
	if err != nil {
		return 0, err
	}

	nextID := id + 1
	if err := s.db.Set(key, []byte(strconv.Itoa(nextID)), pebble.Sync); err != nil {
		return 0, err
	}

	return id, nil
}

// CreateScan creates a new Scan with pending status.
func (s *AIScanStore) CreateScan(mode string, models map[string]string, authorCount int) (*Scan, error) {
	id, err := s.nextID("scan")
	if err != nil {
		return nil, fmt.Errorf("failed to generate scan ID: %w", err)
	}

	scan := &Scan{
		ID:          id,
		Status:      "pending",
		Mode:        mode,
		Models:      models,
		AuthorCount: authorCount,
		CreatedAt:   time.Now(),
	}

	data, err := json.Marshal(scan)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal scan: %w", err)
	}

	if err := s.db.Set(s.k("scan:%d", id), data, pebble.Sync); err != nil {
		return nil, fmt.Errorf("failed to save scan: %w", err)
	}

	return scan, nil
}

// GetScan retrieves a scan by ID. Returns nil, nil if not found.
func (s *AIScanStore) GetScan(id int) (*Scan, error) {
	value, closer, err := s.db.Get(s.k("scan:%d", id))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get scan: %w", err)
	}
	defer closer.Close()

	var scan Scan
	if err := json.Unmarshal(value, &scan); err != nil {
		return nil, fmt.Errorf("failed to unmarshal scan: %w", err)
	}

	return &scan, nil
}

// UpdateScanStatus updates the status of a scan. Sets CompletedAt if status is "complete" or "failed".
func (s *AIScanStore) UpdateScanStatus(id int, status string) error {
	scan, err := s.GetScan(id)
	if err != nil {
		return err
	}
	if scan == nil {
		return fmt.Errorf("scan %d not found", id)
	}

	scan.Status = status
	if status == "complete" || status == "failed" || status == "canceled" {
		now := time.Now()
		scan.CompletedAt = &now
	}

	data, err := json.Marshal(scan)
	if err != nil {
		return fmt.Errorf("failed to marshal scan: %w", err)
	}

	return s.db.Set(s.k("scan:%d", id), data, pebble.Sync)
}

// UpdateScanOperationID sets the operation ID on an existing scan.
func (s *AIScanStore) UpdateScanOperationID(id int, operationID string) error {
	scan, err := s.GetScan(id)
	if err != nil {
		return err
	}
	if scan == nil {
		return fmt.Errorf("scan %d not found", id)
	}
	scan.OperationID = operationID
	data, err := json.Marshal(scan)
	if err != nil {
		return fmt.Errorf("failed to marshal scan: %w", err)
	}
	return s.db.Set(s.k("scan:%d", id), data, pebble.Sync)
}

// ListScans returns all scans, iterating keys from "scan:0" to "scan:;".
// It skips keys containing "_" to avoid scan_phase and scan_result keys.
func (s *AIScanStore) ListScans() ([]Scan, error) {
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: s.k("scan:0"),
		UpperBound: s.k("scan:;"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create iterator: %w", err)
	}
	defer iter.Close()

	var scans []Scan
	for iter.First(); iter.Valid(); iter.Next() {
		// Strip the prefix before checking for "_" in the bare key portion.
		bare := string(iter.Key()[len(s.prefix):])
		if strings.Contains(bare, "_") {
			continue
		}

		var scan Scan
		if err := json.Unmarshal(iter.Value(), &scan); err != nil {
			return nil, fmt.Errorf("failed to unmarshal scan at key %s: %w", string(iter.Key()), err)
		}
		scans = append(scans, scan)
	}

	return scans, nil
}

// DeleteScan deletes a scan and all its associated phases and results.
func (s *AIScanStore) DeleteScan(id int) error {
	batch := s.db.NewBatch()
	defer batch.Close()

	batch.Delete(s.k("scan:%d", id), pebble.Sync)

	// Delete all phases for this scan.
	phasePrefix := s.k("scan_phase:%d:", id)
	phaseIter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: phasePrefix,
		UpperBound: append(append([]byte{}, phasePrefix...), 0xff),
	})
	if err != nil {
		return fmt.Errorf("failed to create phase iterator: %w", err)
	}
	for phaseIter.First(); phaseIter.Valid(); phaseIter.Next() {
		batch.Delete(phaseIter.Key(), pebble.Sync)
	}
	phaseIter.Close()

	// Delete all results for this scan.
	resultPrefix := s.k("scan_result:%d:", id)
	resultIter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: resultPrefix,
		UpperBound: append(append([]byte{}, resultPrefix...), 0xff),
	})
	if err != nil {
		return fmt.Errorf("failed to create result iterator: %w", err)
	}
	for resultIter.First(); resultIter.Valid(); resultIter.Next() {
		batch.Delete(resultIter.Key(), pebble.Sync)
	}
	resultIter.Close()

	return batch.Commit(pebble.Sync)
}

// CreatePhase creates a new ScanPhase with pending status.
func (s *AIScanStore) CreatePhase(scanID int, phaseType, model string) (*ScanPhase, error) {
	phase := &ScanPhase{
		ScanID:    scanID,
		PhaseType: phaseType,
		Status:    "pending",
		Model:     model,
	}

	data, err := json.Marshal(phase)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal phase: %w", err)
	}

	if err := s.db.Set(s.k("scan_phase:%d:%s", scanID, phaseType), data, pebble.Sync); err != nil {
		return nil, fmt.Errorf("failed to save phase: %w", err)
	}

	return phase, nil
}

// GetPhase retrieves a phase by scan ID and phase type. Returns nil, nil if not found.
func (s *AIScanStore) GetPhase(scanID int, phaseType string) (*ScanPhase, error) {
	value, closer, err := s.db.Get(s.k("scan_phase:%d:%s", scanID, phaseType))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get phase: %w", err)
	}
	defer closer.Close()

	var phase ScanPhase
	if err := json.Unmarshal(value, &phase); err != nil {
		return nil, fmt.Errorf("failed to unmarshal phase: %w", err)
	}

	return &phase, nil
}

// UpdatePhaseStatus updates the status and optionally the batch ID of a phase.
// Sets StartedAt on "submitted" or "processing", CompletedAt on "complete" or "failed".
func (s *AIScanStore) UpdatePhaseStatus(scanID int, phaseType, status, batchID string) error {
	phase, err := s.GetPhase(scanID, phaseType)
	if err != nil {
		return err
	}
	if phase == nil {
		return fmt.Errorf("phase %s for scan %d not found", phaseType, scanID)
	}

	phase.Status = status
	if batchID != "" {
		phase.BatchID = batchID
	}

	now := time.Now()
	if status == "submitted" || status == "processing" {
		if phase.StartedAt == nil {
			phase.StartedAt = &now
		}
	}
	if status == "complete" || status == "failed" {
		phase.CompletedAt = &now
	}

	data, err := json.Marshal(phase)
	if err != nil {
		return fmt.Errorf("failed to marshal phase: %w", err)
	}

	return s.db.Set(s.k("scan_phase:%d:%s", scanID, phaseType), data, pebble.Sync)
}

// SavePhaseData saves input, output, and suggestions data for a phase.
func (s *AIScanStore) SavePhaseData(scanID int, phaseType string, input, output, suggestions json.RawMessage) error {
	phase, err := s.GetPhase(scanID, phaseType)
	if err != nil {
		return err
	}
	if phase == nil {
		return fmt.Errorf("phase %s for scan %d not found", phaseType, scanID)
	}

	phase.InputData = input
	phase.OutputData = output
	phase.Suggestions = suggestions

	data, err := json.Marshal(phase)
	if err != nil {
		return fmt.Errorf("failed to marshal phase: %w", err)
	}

	return s.db.Set(s.k("scan_phase:%d:%s", scanID, phaseType), data, pebble.Sync)
}

// GetPhases returns all phases for a given scan ID.
func (s *AIScanStore) GetPhases(scanID int) ([]ScanPhase, error) {
	prefix := s.k("scan_phase:%d:", scanID)
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(append([]byte{}, prefix...), 0xff),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create iterator: %w", err)
	}
	defer iter.Close()

	var phases []ScanPhase
	for iter.First(); iter.Valid(); iter.Next() {
		var phase ScanPhase
		if err := json.Unmarshal(iter.Value(), &phase); err != nil {
			return nil, fmt.Errorf("failed to unmarshal phase: %w", err)
		}
		phases = append(phases, phase)
	}

	return phases, nil
}

// SaveScanResult saves a scan result, auto-assigning an ID.
func (s *AIScanStore) SaveScanResult(result *ScanResult) error {
	id, err := s.nextID("scan_result")
	if err != nil {
		return fmt.Errorf("failed to generate result ID: %w", err)
	}

	result.ID = id

	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("failed to marshal result: %w", err)
	}

	return s.db.Set(s.k("scan_result:%d:%06d", result.ScanID, id), data, pebble.Sync)
}

// GetScanResults returns all results for a given scan ID.
func (s *AIScanStore) GetScanResults(scanID int) ([]ScanResult, error) {
	prefix := s.k("scan_result:%d:", scanID)
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(append([]byte{}, prefix...), 0xff),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create iterator: %w", err)
	}
	defer iter.Close()

	var results []ScanResult
	for iter.First(); iter.Valid(); iter.Next() {
		var result ScanResult
		if err := json.Unmarshal(iter.Value(), &result); err != nil {
			return nil, fmt.Errorf("failed to unmarshal result: %w", err)
		}
		results = append(results, result)
	}

	return results, nil
}

// MarkResultApplied marks a scan result as applied with the current timestamp.
func (s *AIScanStore) MarkResultApplied(scanID, resultID int) error {
	key := s.k("scan_result:%d:%06d", scanID, resultID)
	value, closer, err := s.db.Get(key)
	if err == pebble.ErrNotFound {
		return fmt.Errorf("result %d for scan %d not found", resultID, scanID)
	}
	if err != nil {
		return fmt.Errorf("failed to get result: %w", err)
	}
	defer closer.Close()

	var result ScanResult
	if err := json.Unmarshal(value, &result); err != nil {
		return fmt.Errorf("failed to unmarshal result: %w", err)
	}

	result.Applied = true
	now := time.Now()
	result.AppliedAt = &now

	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("failed to marshal result: %w", err)
	}

	return s.db.Set(key, data, pebble.Sync)
}

// GetAllAppliedResults returns all applied scan results across all scans.
// Used to filter heuristic dedup results by excluding author groups already reviewed.
func (s *AIScanStore) GetAllAppliedResults() ([]ScanResult, error) {
	prefix := s.k("scan_result:")
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(append([]byte{}, prefix...), 0xff),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create iterator: %w", err)
	}
	defer iter.Close()

	var results []ScanResult
	for iter.First(); iter.Valid(); iter.Next() {
		var result ScanResult
		if err := json.Unmarshal(iter.Value(), &result); err != nil {
			continue
		}
		if result.Applied {
			results = append(results, result)
		}
	}
	return results, nil
}

// AIScanHealthStats contains diagnostic counts for the AI scan store.
type AIScanHealthStats struct {
	JobCount     int    `json:"job_count"`
	PendingCount int    `json:"pending_count"`
	SizeBytes    uint64 `json:"size_bytes"`
}

// HealthStats returns diagnostic counts and disk usage for the AI scan store.
// SizeBytes reflects the entire shared DB when not in standalone mode.
func (s *AIScanStore) HealthStats() (AIScanHealthStats, error) {
	scans, err := s.ListScans()
	if err != nil {
		return AIScanHealthStats{}, err
	}
	var pending int
	for _, sc := range scans {
		if sc.Status == "pending" || sc.Status == "scanning" || sc.Status == "enriching" || sc.Status == "cross_validating" {
			pending++
		}
	}
	sizeBytes := s.db.Metrics().DiskSpaceUsage()
	return AIScanHealthStats{
		JobCount:     len(scans),
		PendingCount: pending,
		SizeBytes:    sizeBytes,
	}, nil
}
