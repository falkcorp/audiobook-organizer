// file: internal/aiscan/external_results.go
// version: 1.3.0
// guid: 7aa87f93-e965-48b3-9872-35ac6c51a6ea
// last-edited: 2026-09-19

package aiscan

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// externalSourcePrefix marks a scan recorded from a batch submitted outside
// the pipeline. Stored in Scan.OperationID, which is otherwise the id of the
// ai.author-scan op running the scan; the prefix keeps the two apart.
const externalSourcePrefix = "ai-dedup-batch:"

// RecordFullScanResults records a whole-library author dedup batch submitted
// outside the pipeline (maintenance.ai-dedup-batch) as a completed AI scan:
// one complete full_scan phase and its cross-validated results. That puts the
// suggestions in the same review queue ai.author-scan's results are reviewed
// and applied from, instead of the op's result blob nobody reads.
//
// Idempotent per sourceID: a replay finds the scan it already recorded and
// finishes it if an earlier attempt stopped part way, so aijobs re-running
// the callback never produces a second scan or a second set of results.
func RecordFullScanResults(store *database.AIScanStore, sourceID string, suggestions []ai.AuthorDiscoverySuggestion) (int, error) {
	key := externalSourcePrefix + sourceID
	scans, err := store.ListScans()
	if err != nil {
		return 0, fmt.Errorf("list scans: %w", err)
	}
	var scan *database.Scan
	for i := range scans {
		if scans[i].OperationID == key {
			scan = &scans[i]
			break
		}
	}
	// Done already: complete, or superseded by a newer run. A replay must
	// not rewrite a superseded scan's results or mark it complete again.
	if scan != nil && (scan.Status == "complete" || scan.Status == "superseded") {
		return scan.ID, nil
	}
	if scan == nil {
		// Tag and row in one write (see CreateScanTagged).
		scan, err = store.CreateScanTagged("batch", map[string]string{"full": "batch"}, 0, key)
		if err != nil {
			return 0, fmt.Errorf("create scan: %w", err)
		}
	}

	if p, err := store.GetPhase(scan.ID, "full_scan"); err != nil {
		return 0, err
	} else if p == nil {
		if _, err := store.CreatePhase(scan.ID, "full_scan", "batch"); err != nil {
			return 0, fmt.Errorf("create phase: %w", err)
		}
	}
	scanSuggestions := fullSuggestionsToScanSuggestions(suggestions)
	outputJSON, _ := json.Marshal(suggestions)
	suggestionsJSON, _ := json.Marshal(scanSuggestions)
	if err := store.SavePhaseData(scan.ID, "full_scan", nil, outputJSON, suggestionsJSON); err != nil {
		return 0, fmt.Errorf("save phase data: %w", err)
	}
	if err := store.UpdatePhaseStatus(scan.ID, "full_scan", "complete", ""); err != nil {
		return 0, fmt.Errorf("mark phase complete: %w", err)
	}

	// A replay after a crash between writing the results and marking the scan
	// complete may find results a user has ALREADY applied (the scan is
	// visible in the review queue meanwhile). ReplaceScanResults would drop
	// those Applied flags and let the same merge be applied again, so the
	// existing results stand whenever any of them was applied.
	// The applied-check and the replace are one decision under the store's
	// apply lock, so an apply cannot land between them and be wiped.
	if _, err := store.ReplaceScanResultsIfUnapplied(scan.ID, CrossValidate(scan.ID, nil, scanSuggestions)); err != nil {
		return 0, fmt.Errorf("save results: %w", err)
	}
	if err := store.UpdateScanStatus(scan.ID, "complete"); err != nil {
		return 0, fmt.Errorf("mark scan complete: %w", err)
	}
	supersedeUnreviewed(store, scans, scan.ID)
	return scan.ID, nil
}

// supersedeUnreviewed marks earlier external (ai-dedup-batch) scans that
// nobody applied anything from as "superseded", keeping the one just recorded.
//
// The nightly op submits the whole author list every run, so each run records
// a near-identical list. Left alone they pile up in the review queue, and a
// reviewer works through last week's copy of tonight's list. Superseding
// rather than deleting keeps them readable; a scan with ANY applied result is
// a reviewer's work in progress and is never touched. Only "complete" scans
// are considered, so a scan still being recorded by a concurrent replay is
// left alone. Best effort: a failure here leaves an extra scan in the queue,
// never a lost one.
func supersedeUnreviewed(store *database.AIScanStore, scans []database.Scan, keepID int) {
	for _, sc := range scans {
		if sc.ID == keepID || sc.Status != "complete" || !strings.HasPrefix(sc.OperationID, externalSourcePrefix) {
			continue
		}
		// Checked and written under the store's apply lock, so an apply
		// racing this cannot land in a scan that is then hidden.
		if _, err := store.SupersedeIfUnapplied(sc.ID, keepID); err != nil {
			plog.Warn("supersede external scan %d: %v", sc.ID, err)
		}
	}
}
