// file: internal/server/audiobooks_compat.go
// version: 2.0.0
// guid: b1c2d3e4-f5a6-7890-bcde-f01234560020
// last-edited: 2026-08-18
//
// Type aliases and function variables that let the rest of internal/server/
// continue using the old unqualified names after the seven service files
// were moved to internal/audiobooks/. This file is the only place that
// needs updating if a moved symbol is later renamed.

package server

import (
	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
)

// --- AudiobookService -------------------------------------------------------

type (
	// AudiobookService is a type alias for the moved service.
	AudiobookService = audiobookspkg.AudiobookService

	// AudiobooksListResponse is re-exported from the audiobooks package.
	AudiobooksListResponse = audiobookspkg.AudiobooksListResponse

	// AudiobookDetail is re-exported from the audiobooks package.
	AudiobookDetail = audiobookspkg.AudiobookDetail

	// DuplicatesResult is re-exported from the audiobooks package.
	DuplicatesResult = audiobookspkg.DuplicatesResult

	// SoftDeletedBooksResponse is re-exported from the audiobooks package.
	SoftDeletedBooksResponse = audiobookspkg.SoftDeletedBooksResponse

	// PurgeResult is re-exported from the audiobooks package.
	PurgeResult = audiobookspkg.PurgeResult

	// AudiobookUpdate is re-exported from the audiobooks package.
	AudiobookUpdate = audiobookspkg.AudiobookUpdate

	// OverridePayload is re-exported from the audiobooks package.
	OverridePayload = audiobookspkg.OverridePayload

	// FieldFilter is re-exported from the audiobooks package.
	FieldFilter = audiobookspkg.FieldFilter

	// ListFilters is re-exported from the audiobooks package.
	ListFilters = audiobookspkg.ListFilters

	// UpdateAudiobookRequest is re-exported from the audiobooks package.
	UpdateAudiobookRequest = audiobookspkg.UpdateAudiobookRequest

	// DeleteAudiobookOptions is re-exported from the audiobooks package.
	DeleteAudiobookOptions = audiobookspkg.DeleteAudiobookOptions
)

// PerUserFieldNames is re-exported from the audiobooks package.
var PerUserFieldNames = audiobookspkg.PerUserFieldNames

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
	// AuthorSeriesService is re-exported from the audiobooks package.
	AuthorSeriesService = audiobookspkg.AuthorSeriesService

	// AuthorWithCount is re-exported from the audiobooks package.
	AuthorWithCount = audiobookspkg.AuthorWithCount

	// AuthorListResponse is re-exported from the audiobooks package.
	AuthorListResponse = audiobookspkg.AuthorListResponse

	// AuthorWithCountListResponse is re-exported from the audiobooks package.
	AuthorWithCountListResponse = audiobookspkg.AuthorWithCountListResponse

	// SeriesWithCount is re-exported from the audiobooks package.
	SeriesWithCount = audiobookspkg.SeriesWithCount

	// SeriesListResponse is re-exported from the audiobooks package.
	SeriesListResponse = audiobookspkg.SeriesListResponse

	// SeriesWithCountsResponse is re-exported from the audiobooks package.
	SeriesWithCountsResponse = audiobookspkg.SeriesWithCountsResponse
)

// NewAuthorSeriesService is the audiobooks constructor under its pre-move name.
var NewAuthorSeriesService = audiobookspkg.NewAuthorSeriesService

// --- OrganizeService --------------------------------------------------------

type (
	// OrganizeService is re-exported from the audiobooks package.
	OrganizeService = audiobookspkg.OrganizeService

	// OrganizeRequest is re-exported from the audiobooks package.
	OrganizeRequest = audiobookspkg.OrganizeRequest

	// OrganizeStats is re-exported from the audiobooks package.
	OrganizeStats = audiobookspkg.OrganizeStats
)

// NewOrganizeService is the audiobooks constructor under its pre-move name.
var NewOrganizeService = audiobookspkg.NewOrganizeService

// --- OrganizePreviewService -------------------------------------------------

type (
	// OrganizePreviewStep is re-exported from the audiobooks package.
	OrganizePreviewStep = audiobookspkg.OrganizePreviewStep

	// OrganizePreviewResponse is re-exported from the audiobooks package.
	OrganizePreviewResponse = audiobookspkg.OrganizePreviewResponse

	// OrganizePreviewService is re-exported from the audiobooks package.
	OrganizePreviewService = audiobookspkg.OrganizePreviewService
)

// NewOrganizePreviewService is the audiobooks constructor under its pre-move name.
var NewOrganizePreviewService = audiobookspkg.NewOrganizePreviewService

// --- RevertService ----------------------------------------------------------

type (
	// RevertService is re-exported from the audiobooks package.
	RevertService = audiobookspkg.RevertService
)

// NewRevertService is the audiobooks constructor under its pre-move name.
var NewRevertService = audiobookspkg.NewRevertService

// --- RenameService ----------------------------------------------------------

type (
	// RenameService is re-exported from the audiobooks package.
	RenameService = audiobookspkg.RenameService

	// TagChange is re-exported from the audiobooks package.
	TagChange = audiobookspkg.TagChange

	// RenamePreview is re-exported from the audiobooks package.
	RenamePreview = audiobookspkg.RenamePreview

	// RenameApplyResult is re-exported from the audiobooks package.
	RenameApplyResult = audiobookspkg.RenameApplyResult
)

// NewRenameService is the audiobooks constructor under its pre-move name.
var NewRenameService = audiobookspkg.NewRenameService

// applyOverrideToPayload delegates to the audiobooks package.
// Kept unexported so server-package whitebox tests can reference it directly.
var applyOverrideToPayload = audiobookspkg.ApplyOverrideToPayload
