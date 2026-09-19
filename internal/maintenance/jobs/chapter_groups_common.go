// file: internal/maintenance/jobs/chapter_groups_common.go
// version: 1.0.0
// guid: c619d4b3-ba60-4e76-b0ea-a5ff309d39f7
// last-edited: 2026-09-19

package jobs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
)

var chapterLog = logger.New("chapter-groups")

// Defaults the chapter jobs advertise in DefaultParams and apply when a key is
// omitted. They are what both jobs hardcoded before the params were honoured.
const (
	chapterDefaultMinFiles           = 2
	chapterDefaultMaxPerFileDuration = 600
)

// chapterGroupParams is the operator-facing parameter shape shared by
// scan-chapter-groups and merge-chapter-groups. dry_run is resolved by the
// dispatcher and arrives through Run's dryRun argument; it is decoded here only
// so the result can echo the effective params.
type chapterGroupParams struct {
	DryRun             bool   `json:"dry_run"`
	MinFiles           int    `json:"min_files"`
	MaxPerFileDuration int    `json:"max_per_file_duration"`
	PathPrefix         string `json:"path_prefix"`
}

// decodeChapterGroupParams reads the run's own params. A blob that does not
// decode is an error, never a silent fall-back to defaults: path_prefix is what
// scopes a merge, and losing it would widen a merge to the whole library.
func decodeChapterGroupParams(ctx context.Context) (chapterGroupParams, error) {
	p := chapterGroupParams{}
	if raw := maintenance.RawParamsFromCtx(ctx); len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return p, fmt.Errorf("decode chapter-group params: %w", err)
		}
	}
	if p.MinFiles <= 0 {
		p.MinFiles = chapterDefaultMinFiles
	}
	if p.MaxPerFileDuration <= 0 {
		p.MaxPerFileDuration = chapterDefaultMaxPerFileDuration
	}
	return p, nil
}

// chapterGroupOutcome is one group as the Maintenance card renders it. The
// merge-only fields are empty on a scan.
type chapterGroupOutcome struct {
	PrimaryBookID string   `json:"primary_book_id"`
	BookIDs       []string `json:"book_ids"`
	SourceBookIDs []string `json:"source_book_ids"`
	CommonTitle   string   `json:"common_title"`
	TotalDuration float64  `json:"total_duration"`
	FileCount     int      `json:"file_count"`
	Directory     string   `json:"directory"`

	// Status is would_merge / would_skip (dry run) or merged / partial /
	// failed (real run). Empty on a scan.
	Status string `json:"status,omitempty"`
	// PrimaryTitle is the primary's title before the merge.
	PrimaryTitle string `json:"primary_title,omitempty"`
	// TitleAction says what the merge does to the primary's title:
	// "set" (replaced a filename-derived title with CommonTitle), "kept"
	// (curated title left alone), "kept_locked" (user field lock).
	TitleAction string   `json:"title_action,omitempty"`
	BooksMerged int      `json:"books_merged,omitempty"`
	FilesMoved  int      `json:"files_moved,omitempty"`
	JournalID   string   `json:"journal_id,omitempty"`
	Errors      []string `json:"errors,omitempty"`
}

// chapterGroupsResult is the structured payload both jobs persist on their
// operation (GET /operations/:id/result).
type chapterGroupsResult struct {
	Job                          string                `json:"job"`
	DryRun                       bool                  `json:"dry_run"`
	Params                       chapterGroupParams    `json:"params"`
	GroupsFound                  int                   `json:"groups_found"`
	TotalBooksAffected           int                   `json:"total_books_affected"`
	GroupsSkippedUnknownDuration int                   `json:"groups_skipped_unknown_duration"`
	BooksMerged                  int                   `json:"books_merged"`
	BooksSkipped                 int                   `json:"books_skipped"`
	GroupsFailed                 int                   `json:"groups_failed"`
	Groups                       []chapterGroupOutcome `json:"groups"`
}

// detectChapterGroupsForRun loads every book and runs the detector with the
// run's params. The returned map indexes the loaded rows by ID for callers that
// need a member's snapshot (titles for the audit record).
func detectChapterGroupsForRun(ctx context.Context, store maintenance.JobStore, p chapterGroupParams) (scanner.ChapterDetection, map[string]database.BookCore, error) {
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return scanner.ChapterDetection{}, nil, err
	}
	if err := ctx.Err(); err != nil {
		return scanner.ChapterDetection{}, nil, err
	}
	det := scanner.DetectChapterGroupsWithOptions(books, scanner.ChapterDetectOptions{
		MinFiles:           p.MinFiles,
		MaxPerFileDuration: p.MaxPerFileDuration,
		PathPrefix:         p.PathPrefix,
	})
	byID := make(map[string]database.BookCore)
	for _, g := range det.Groups {
		for _, id := range g.BookIDs {
			byID[id] = database.BookCore{}
		}
	}
	for i := range books {
		if _, ok := byID[books[i].ID]; ok {
			byID[books[i].ID] = books[i]
		}
	}
	return det, byID, nil
}

func newChapterGroupOutcome(g scanner.ChapterGroup) chapterGroupOutcome {
	return chapterGroupOutcome{
		PrimaryBookID: g.PrimaryBookID,
		BookIDs:       g.BookIDs,
		SourceBookIDs: append([]string(nil), g.BookIDs[1:]...),
		CommonTitle:   g.CommonTitle,
		TotalDuration: g.TotalDuration,
		FileCount:     g.FileCount,
		Directory:     g.Directory,
	}
}

// chapterMergeAudit is the per-group audit record a real merge writes as an
// OperationResult row (GET /operations/:id/results), keyed by the primary. The
// undo itself is the combine journal JournalID names (merge.Service.UndoCombine);
// this row is what lets an operator review a merge without reading the journal.
type chapterMergeAudit struct {
	PrimaryBookID      string              `json:"primary_book_id"`
	PrimaryTitleBefore string              `json:"primary_title_before"`
	NewTitle           string              `json:"new_title,omitempty"`
	TitleAction        string              `json:"title_action"`
	SourceBookIDs      []string            `json:"source_book_ids"`
	SourceTitles       map[string]string   `json:"source_titles"`
	MovedFileIDs       map[string][]string `json:"moved_file_ids"`
	BooksMerged        int                 `json:"books_merged"`
	FilesMoved         int                 `json:"files_moved"`
	JournalID          string              `json:"journal_id,omitempty"`
	Errors             []string            `json:"errors,omitempty"`
}
