// file: internal/server/scan_cache_backfill.go
// version: 1.0.0
// guid: 3b7f21c4-6a58-4e19-9d02-8c4f5e1a77b3
// last-edited: 2026-08-25

package server

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
)

// ScanCacheBackfiller is the single method the backfill endpoint needs.
//
// Declared as a NAMED interface rather than an inline `s.store.(interface{...})`
// assertion on purpose: an anonymous interface embedded in an expression is
// invisible to a grep for the type name, so the requirement cannot be found by
// anyone auditing what this package demands of the store. Naming it also keeps
// database.Store from having to grow a 399th method to satisfy one endpoint.
type ScanCacheBackfiller interface {
	BackfillBookFileScanCache(dryRun bool) (*database.BackfillBookFileScanCacheResult, error)
}

// backfillScanCacheHandler seeds the per-file scan cache from the existing
// book-level stamps, and gives single-file books the book_file row the scan never
// creates for them.
//
// WHY THIS ENDPOINT EXISTS AT ALL. The scan cache is now keyed on book_file rows.
// Until this has been run once, every one of those rows reads as "never scanned",
// so the first scan after deploying that reader re-reads and re-hashes the entire
// library -- 4-6 hours on this library, and the exact opposite of what a scan
// cache is for. Landing the reader without a way to invoke this left that
// protection theoretical; this makes it a thing an operator can actually do.
//
// It is deliberately an API endpoint rather than a maintenance.* op. The
// maintenance plugin is gated on RootDir: started without --dir it registers 0 of
// 105 ops and would silently not appear, which is the wrong failure mode for a
// migration you must run before a deploy. It is deliberately NOT a startup
// migration either -- a pass that stats tens of thousands of files across a NAS
// should not fire unbidden on boot.
//
// Defaults to a dry run, matching electMissingPrimariesHandler: a mutating apply
// must be opted into with ?dry_run=false, so an accidental POST previews rather
// than writes. The dry run reports exactly what an apply would do, including how
// many book_file rows it would create.
//
// The apply is idempotent, so re-running it after a partial or timed-out request
// is safe: seeding skips rows already stamped, and creation skips books that
// already own a row. A client timeout does not cancel it -- the work is not bound
// to the request context -- so a disconnect means "result unseen", never "write
// abandoned half way".
func (s *Server) backfillScanCacheHandler(c *gin.Context) {
	backfiller, ok := s.storeForWiring().(ScanCacheBackfiller)
	if !ok {
		// Not an error the caller can fix, and not a 500 either: this store simply
		// does not implement the migration. Say which store, so the message is
		// actionable rather than mysterious.
		httputil.RespondWithError(c, http.StatusNotImplemented,
			"this store does not implement the per-file scan-cache backfill", "NOT_IMPLEMENTED")
		return
	}

	dryRun := c.DefaultQuery("dry_run", "true") != "false"
	result, err := backfiller.BackfillBookFileScanCache(dryRun)
	if err != nil {
		httputil.InternalError(c, "failed to backfill the per-file scan cache", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"dry_run": dryRun, "result": result})
}
