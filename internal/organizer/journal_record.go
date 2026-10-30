// file: internal/organizer/journal_record.go
// version: 1.0.0
// guid: 91c4e7a2-5d38-4b6f-a1e9-3f8d20c7b564
// last-edited: 2026-10-30

package organizer

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

var journalLog = logger.New("organizer.journal")

// recordChange writes one organize ledger row through the classification
// check. The organize run does not stop for a row that was not recorded (the
// file move it describes has happened or been skipped already), so a refusal
// is not returned, but it is not silent either: it is logged at Warn with the
// change type, the operation id and the book.
func (orgSvc *Service) recordChange(c *database.OperationChange) {
	if err := database.RecordOperationChange(orgSvc.db, c); err != nil {
		journalLog.Warn("organize: journal row not recorded: op_id=%s change_type=%s book_id=%s err=%s",
			logger.SanitizeLogValue(c.OperationID), logger.SanitizeLogValue(c.ChangeType),
			logger.SanitizeLogValue(c.BookID), logger.SanitizeLogValue(err.Error()))
	}
}
