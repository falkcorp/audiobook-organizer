// file: internal/dedup/series_journal_record.go
// version: 1.0.0
// guid: 4a7d19c3-8e52-4f06-b3a8-62d1f5e9c70b
// last-edited: 2026-10-30

package dedup

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

var seriesJournalLog = logger.New("dedup.series-journal")

// recordSeriesChange writes one series-merge ledger row through the
// classification check. The merge has already committed the write the row
// describes, so a refusal does not undo it and is not returned, but it is
// logged at Warn with the change type and the operation id.
func recordSeriesChange(store database.OperationChangeCreator, c *database.OperationChange) {
	if err := database.RecordOperationChange(store, c); err != nil {
		seriesJournalLog.Warn("series merge: journal row not recorded: op_id=%s change_type=%s err=%s",
			logger.SanitizeLogValue(c.OperationID), logger.SanitizeLogValue(c.ChangeType),
			logger.SanitizeLogValue(err.Error()))
	}
}
