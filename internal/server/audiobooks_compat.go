// file: internal/server/audiobooks_compat.go
// version: 3.0.0
// guid: b1c2d3e4-f5a6-7890-bcde-f01234560020
// last-edited: 2026-10-09
//
// Type aliases and function variables that let the rest of internal/server/
// continue using the old unqualified names after the seven service files
// were moved to internal/audiobooks/. Only names that still have a use in
// package server are kept; new code calls the audiobooks package directly.

package server

import (
	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
)

// --- AudiobookService -------------------------------------------------------

type (
	// AudiobookService is a type alias for the moved service.
	AudiobookService = audiobookspkg.AudiobookService

	// AudiobookUpdate is re-exported from the audiobooks package.
	AudiobookUpdate = audiobookspkg.AudiobookUpdate

	// FieldFilter is re-exported from the audiobooks package.
	FieldFilter = audiobookspkg.FieldFilter

	// ListFilters is re-exported from the audiobooks package.
	ListFilters = audiobookspkg.ListFilters

	// UpdateAudiobookRequest is re-exported from the audiobooks package.
	UpdateAudiobookRequest = audiobookspkg.UpdateAudiobookRequest
)

// IsPerUserField delegates to the audiobooks package.
func IsPerUserField(field string) bool {
	return audiobookspkg.IsPerUserField(field)
}

// NewAudiobookService is the audiobooks constructor under its pre-move name.
var NewAudiobookService = audiobookspkg.NewAudiobookService

// --- AudiobookUpdateService -------------------------------------------------

type (
	// AudiobookUpdateService is re-exported from the audiobooks package.
	AudiobookUpdateService = audiobookspkg.AudiobookUpdateService
)

// NewAudiobookUpdateService is the audiobooks constructor under its pre-move name.
var NewAudiobookUpdateService = audiobookspkg.NewAudiobookUpdateService

// --- AuthorSeriesService ----------------------------------------------------

type (
	// AuthorListResponse is re-exported from the audiobooks package.
	AuthorListResponse = audiobookspkg.AuthorListResponse

	// AuthorWithCountListResponse is re-exported from the audiobooks package.
	AuthorWithCountListResponse = audiobookspkg.AuthorWithCountListResponse

	// SeriesListResponse is re-exported from the audiobooks package.
	SeriesListResponse = audiobookspkg.SeriesListResponse
)

// NewAuthorSeriesService is the audiobooks constructor under its pre-move name.
var NewAuthorSeriesService = audiobookspkg.NewAuthorSeriesService

// --- OrganizeService --------------------------------------------------------

type (
	// OrganizeService is re-exported from the audiobooks package.
	OrganizeService = audiobookspkg.OrganizeService

	// OrganizeRequest is re-exported from the audiobooks package.
	OrganizeRequest = audiobookspkg.OrganizeRequest
)

// applyOverrideToPayload delegates to the audiobooks package.
// Kept unexported so server-package whitebox tests can reference it directly.
var applyOverrideToPayload = audiobookspkg.ApplyOverrideToPayload
