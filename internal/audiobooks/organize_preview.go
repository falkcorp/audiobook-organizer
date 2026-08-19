// file: internal/audiobooks/organize_preview.go
// version: 2.1.0
// guid: f1a2b3c4-d5e6-7890-abcd-ef1234567890
// last-edited: 2026-08-18
//
// Thin forwarding layer — the real implementation now lives in
// internal/organizer/preview.go. This file provides type aliases and
// constructor wrappers so the rest of the server package can keep using
// the old names.

package audiobooks

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

// Type aliases for backward compatibility.
type OrganizePreviewStep = organizer.PreviewStep
type OrganizePreviewResponse = organizer.PreviewResponse
type OrganizePreviewService = organizer.PreviewService

// NewOrganizePreviewService creates a new organizer.PreviewService and
// wires up server-specific callbacks. IsProtectedPath /
// ResolveAuthorAndSeriesNames bound to db via closures (see rename.go
// for the rationale — SERVER-GLOBAL-STORE-AUDIT phase 6).
func NewOrganizePreviewService(db organizerWrapperStore) *OrganizePreviewService {
	svc := organizer.NewPreviewService(db)
	svc.IsProtectedPath = func(filePath string) bool {
		return isProtectedPath(db, filePath)
	}
	svc.ResolveAuthorAndSeriesNames = func(book *database.Book) (string, string) {
		return resolveAuthorAndSeriesNames(db, book)
	}
	return svc
}
