// file: internal/database/ai_scan_store.go
// version: 2.6.0
// last-edited: 2026-09-19
// guid: a7b3c9d1-4e5f-6a7b-8c9d-0e1f2a3b4c5d

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
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
//   - scan_artifact:<scanID>:<phaseType>:<name> -> raw JSON a phase persisted
//     mid-flight so a restart can continue it (finished realtime chunks, the
//     group->author-id map a batch was submitted with)
type AIScanStore struct {
	db     *pebble.DB
	prefix string // "aiscan:" when shared, "" when standalone
	owned  bool   // if false, Close and Optimize are no-ops
	// applyMu makes "apply a result" and "supersede the scan" one decision
	// each (MarkResultApplied, SupersedeIfUnapplied): without it a supersede
	// could read "nothing applied", an apply land, and the scan then be hidden
	// with an applied result in it.
	applyMu sync.Mutex
	// stateMu serializes every read-modify-write of a phase row or a scan's
	// status, so TransitionPhase and CompleteScanIfActive are true
	// compare-and-set operations against CancelScan's writes.
	stateMu sync.Mutex
}

// Scan represents a full pipeline run.
type Scan struct {
	ID          int               `json:"id"`
	Status      string            `json:"status"` // pending, scanning, enriching, cross_validating, complete, failed, canceled, superseded (an unreviewed ai-dedup-batch scan replaced by a newer run)
	Mode        string            `json:"mode"`   // batch, realtime
	Models      map[string]string `json:"models"` // {groups: "gpt-5-mini", full: "o4-mini"}
	AuthorCount int               `json:"author_count"`
	OperationID string            `json:"operation_id,omitempty"` // links to main operations store for visibility/cancel
	// SupersededBy is the scan that replaced this one when Status is
	// "superseded".
	SupersededBy int        `json:"superseded_by,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
}

// ScanPhase represents one phase of the pipeline.
type ScanPhase struct {
	ScanID      int             `json:"scan_id"`
	PhaseType   string          `json:"phase_type"` // groups_scan, full_scan, groups_enrich, full_enrich, cross_validate
	Status      string          `json:"status"`     // pending, submitting, submitted, processing, complete, failed, canceled
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
	return s.CreateScanTagged(mode, models, authorCount, "")
}

// CreateScanTagged is CreateScan with OperationID set in the SAME write as the
// scan row. A caller that finds its scan again by that tag (a replayed batch
// apply) must never see the scan exist untagged: a crash between a create and
// a separate tag write leaves a scan no replay can find, and each replay then
// creates another.
func (s *AIScanStore) CreateScanTagged(mode string, models map[string]string, authorCount int, operationID string) (*Scan, error) {
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
		OperationID: operationID,
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
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.updateScanStatusLocked(id, status)
}

func (s *AIScanStore) updateScanStatusLocked(id int, status string) error {
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
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
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

	// Delete all phase artifacts for this scan.
	if err := s.deletePrefix(batch, s.k("scan_artifact:%d:", id)); err != nil {
		return err
	}

	return batch.Commit(pebble.Sync)
}

// deletePrefix queues a delete of every key under prefix onto batch.
func (s *AIScanStore) deletePrefix(batch *pebble.Batch, prefix []byte) error {
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(append([]byte{}, prefix...), 0xff),
	})
	if err != nil {
		return fmt.Errorf("failed to create iterator: %w", err)
	}
	defer iter.Close()
	for iter.First(); iter.Valid(); iter.Next() {
		if err := batch.Delete(append([]byte{}, iter.Key()...), pebble.Sync); err != nil {
			return fmt.Errorf("queue delete: %w", err)
		}
	}
	return nil
}

// ScanSupersededError is returned when applying a result from a superseded
// scan. By names the newer scan the reviewer should apply from instead.
type ScanSupersededError struct {
	ScanID, By int
}

func (e *ScanSupersededError) Error() string {
	return fmt.Sprintf("scan %d was superseded by scan %d; apply from scan %d instead", e.ScanID, e.By, e.By)
}

// isTerminalScanStatus reports whether a scan status admits no more phase
// progress.
func isTerminalScanStatus(status string) bool {
	return status == "complete" || status == "failed" || status == "canceled" || status == "superseded"
}

// TransitionPhase moves a phase to status `to` (recording batchID when non-
// empty) only if its current status is one of from AND its scan is not
// terminal, and reports whether it did. It is the only safe way to record a
// batch id: a blind write can land on a phase CancelScan just marked
// canceled, attaching a live paid batch to a dead scan that nothing will ever
// cancel or collect. On false the caller owns cleaning up that batch.
func (s *AIScanStore) TransitionPhase(scanID int, phaseType string, from []string, to, batchID string) (bool, error) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	scan, err := s.GetScan(scanID)
	if err != nil {
		return false, err
	}
	if scan == nil || isTerminalScanStatus(scan.Status) {
		return false, nil
	}
	phase, err := s.GetPhase(scanID, phaseType)
	if err != nil || phase == nil {
		return false, err
	}
	allowed := false
	for _, f := range from {
		if phase.Status == f {
			allowed = true
			break
		}
	}
	if !allowed {
		return false, nil
	}
	return true, s.updatePhaseStatusLocked(scanID, phaseType, to, batchID)
}

// CompleteScanIfActive marks a scan complete unless it already reached a
// terminal status (an operator cancel, a failure) — so work that finishes
// after a cancel never overwrites it — and reports whether it did.
func (s *AIScanStore) CompleteScanIfActive(scanID int) (bool, error) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	scan, err := s.GetScan(scanID)
	if err != nil {
		return false, err
	}
	if scan == nil || (isTerminalScanStatus(scan.Status) && scan.Status != "complete") {
		return false, nil
	}
	return true, s.updateScanStatusLocked(scanID, "complete")
}

// ReplaceScanResultsIfUnapplied replaces a scan's results unless any of them
// has been applied, under the same lock as MarkResultApplied, and reports
// whether it replaced them. A replay must never wipe a result a user applied.
func (s *AIScanStore) ReplaceScanResultsIfUnapplied(scanID int, results []ScanResult) (bool, error) {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	existing, err := s.GetScanResults(scanID)
	if err != nil {
		return false, err
	}
	for _, r := range existing {
		if r.Applied {
			return false, nil
		}
	}
	return true, s.ReplaceScanResults(scanID, results)
}

// SupersedeIfUnapplied marks scanID superseded by byID unless any of its
// results has been applied, and reports whether it did. It holds applyMu, so
// it cannot interleave with MarkResultApplied.
func (s *AIScanStore) SupersedeIfUnapplied(scanID, byID int) (bool, error) {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	rs, err := s.GetScanResults(scanID)
	if err != nil {
		return false, err
	}
	for _, r := range rs {
		if r.Applied {
			return false, nil
		}
	}
	s.stateMu.Lock() // lock order: applyMu, then stateMu
	defer s.stateMu.Unlock()
	scan, err := s.GetScan(scanID)
	if err != nil || scan == nil {
		return false, fmt.Errorf("read scan %d: %v", scanID, err)
	}
	scan.Status = "superseded"
	scan.SupersededBy = byID
	data, err := json.Marshal(scan)
	if err != nil {
		return false, fmt.Errorf("marshal scan: %w", err)
	}
	if err := s.db.Set(s.k("scan:%d", scanID), data, pebble.Sync); err != nil {
		return false, fmt.Errorf("save scan: %w", err)
	}
	return true, nil
}

// SavePhaseArtifact durably records one named piece of a phase's in-flight
// work. It exists so a restart can continue a phase instead of redoing it: a
// realtime full_scan writes each finished chunk here the moment its LLM call
// returns, and a resumed run skips every chunk already present. Writing the
// same name twice overwrites, so a re-run chunk cannot be counted twice.
func (s *AIScanStore) SavePhaseArtifact(scanID int, phaseType, name string, data json.RawMessage) error {
	if err := s.db.Set(s.k("scan_artifact:%d:%s:%s", scanID, phaseType, name), data, pebble.Sync); err != nil {
		return fmt.Errorf("save artifact %s/%s for scan %d: %w", phaseType, name, scanID, err)
	}
	return nil
}

// GetPhaseArtifacts returns every artifact saved for one phase of a scan,
// keyed by name. An empty map, not an error, when there are none.
func (s *AIScanStore) GetPhaseArtifacts(scanID int, phaseType string) (map[string]json.RawMessage, error) {
	prefix := s.k("scan_artifact:%d:%s:", scanID, phaseType)
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(append([]byte{}, prefix...), 0xff),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create iterator: %w", err)
	}
	defer iter.Close()

	out := make(map[string]json.RawMessage)
	for iter.First(); iter.Valid(); iter.Next() {
		name := string(iter.Key()[len(prefix):])
		out[name] = append(json.RawMessage{}, iter.Value()...)
	}
	return out, nil
}

// ReplaceScanResults atomically replaces every result of a scan with results,
// assigning fresh IDs. Cross-validation writes through this rather than one
// SaveScanResult per row so that running it again — after a restart, or when
// both enrichment phases finish at once and each triggers it — leaves exactly
// one set of results instead of appending a duplicate of every suggestion.
//
// It discards Applied / AppliedAt. That is safe only because the pipeline
// calls it solely while its cross_validate phase is not yet complete, and
// marks the phase complete right after; results are applied by a user after
// that. Do not call it on a scan whose results may already have been applied.
func (s *AIScanStore) ReplaceScanResults(scanID int, results []ScanResult) error {
	batch := s.db.NewBatch()
	defer batch.Close()

	if err := s.deletePrefix(batch, s.k("scan_result:%d:", scanID)); err != nil {
		return err
	}
	for i := range results {
		id, err := s.nextID("scan_result")
		if err != nil {
			return fmt.Errorf("failed to generate result ID: %w", err)
		}
		results[i].ID = id
		results[i].ScanID = scanID
		data, err := json.Marshal(&results[i])
		if err != nil {
			return fmt.Errorf("failed to marshal result: %w", err)
		}
		if err := batch.Set(s.k("scan_result:%d:%06d", scanID, id), data, pebble.Sync); err != nil {
			return fmt.Errorf("queue result: %w", err)
		}
	}
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
// Sets StartedAt on "submitting", "submitted" or "processing", CompletedAt on "complete" or "failed".
func (s *AIScanStore) UpdatePhaseStatus(scanID int, phaseType, status, batchID string) error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.updatePhaseStatusLocked(scanID, phaseType, status, batchID)
}

func (s *AIScanStore) updatePhaseStatusLocked(scanID int, phaseType, status, batchID string) error {
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
	if status == "submitting" || status == "submitted" || status == "processing" {
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
	s.stateMu.Lock() // it rewrites the whole row, status included
	defer s.stateMu.Unlock()
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
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	// Refuse applying from a superseded scan: its list was replaced by a
	// newer run, and the reviewer should work from that one.
	if scan, err := s.GetScan(scanID); err == nil && scan != nil && scan.Status == "superseded" {
		return &ScanSupersededError{ScanID: scanID, By: scan.SupersededBy}
	}
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
