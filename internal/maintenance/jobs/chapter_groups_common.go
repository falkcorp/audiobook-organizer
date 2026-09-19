// file: internal/maintenance/jobs/chapter_groups_common.go
// version: 1.3.0
// guid: c619d4b3-ba60-4e76-b0ea-a5ff309d39f7
// last-edited: 2026-09-19

package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
)

var chapterLog = logger.New("chapter-groups")

// Defaults the chapter jobs advertise in DefaultParams and apply when a key is
// omitted. They are what both jobs hardcoded before the params were honoured.
const (
	chapterDefaultMinFiles           = 2
	chapterDefaultMaxPerFileDuration = 600
)

// chapterGroupSelection is one reviewed group a real merge is asked to apply:
// exactly the members a dry-run preview returned, and that preview's
// fingerprint of them. A real merge merges ONLY these and never runs detection
// to decide what to merge (the sibling of dedup.BulkSplitBookMergeItem).
type chapterGroupSelection struct {
	PrimaryBookID string   `json:"primary_book_id"`
	BookIDs       []string `json:"book_ids"`
	Fingerprint   string   `json:"fingerprint"`
}

// chapterGroupParams is the operator-facing parameter shape shared by
// scan-chapter-groups and merge-chapter-groups. dry_run is resolved by the
// dispatcher and arrives through Run's dryRun argument; it is decoded here only
// so the result can echo the effective params.
type chapterGroupParams struct {
	DryRun             bool   `json:"dry_run"`
	MinFiles           int    `json:"min_files"`
	MaxPerFileDuration int    `json:"max_per_file_duration"`
	PathPrefix         string `json:"path_prefix"`
	// Groups is the reviewed set a real merge applies. Required when
	// dry_run is false; ignored by a dry run and by the scan.
	Groups []chapterGroupSelection `json:"groups,omitempty"`
}

// decodeChapterGroupParams reads the run's own params. A blob that does not
// decode is an error, never a silent fall-back to defaults.
func decodeChapterGroupParams(ctx context.Context) (chapterGroupParams, error) {
	return parseChapterGroupParams(maintenance.RawParamsFromCtx(ctx))
}

func parseChapterGroupParams(raw json.RawMessage) (chapterGroupParams, error) {
	p := chapterGroupParams{}
	if len(raw) > 0 {
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

// chapterMemberSnapshot is what a preview saw of one member; the group
// fingerprint is computed over these, and a real merge recomputes it.
type chapterMemberSnapshot struct {
	BookID    string `json:"book_id"`
	Title     string `json:"title"`
	FileCount int    `json:"file_count"`
	Duration  int    `json:"duration"`
	UpdatedAt string `json:"updated_at"`
	// Files is "id|path" for each of the member's files, sorted: a file
	// swapped, added or moved on disk after the preview changes the
	// fingerprint even when the count does not.
	Files []string `json:"files"`
}

// chapterGroupOutcome is one group as the Maintenance card renders it. The
// merge-only fields are empty on a scan.
type chapterGroupOutcome struct {
	PrimaryBookID string                  `json:"primary_book_id"`
	BookIDs       []string                `json:"book_ids"`
	SourceBookIDs []string                `json:"source_book_ids"`
	CommonTitle   string                  `json:"common_title"`
	TotalDuration float64                 `json:"total_duration"`
	FileCount     int                     `json:"file_count"`
	Directory     string                  `json:"directory"`
	Members       []chapterMemberSnapshot `json:"members,omitempty"`
	// Fingerprint identifies exactly what the preview saw; a real merge of
	// this group must send it back and is refused if the members changed.
	Fingerprint string `json:"fingerprint,omitempty"`

	// Status is would_merge / would_skip / blocked (dry run) or merged /
	// partial / failed / drifted / blocked (real run). Empty on a scan.
	Status string `json:"status,omitempty"`
	// Blockers name user data or metadata on a source that the merge cannot
	// carry onto the primary; a group with any is never merged.
	Blockers []string `json:"blockers,omitempty"`
	// MetadataFills names the fields (asin, narrator, series, author) the
	// merge copies from the sources onto the primary's EMPTY fields.
	MetadataFills []string `json:"metadata_fills,omitempty"`
	// PrimaryTitle is the primary's title before the merge.
	PrimaryTitle string `json:"primary_title,omitempty"`
	// TitleAction: "set" (a filename-derived title replaced by CommonTitle),
	// "kept" (curated title left alone), "kept_locked" (user field lock).
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
	GroupsSkippedDuplicateCopies int                   `json:"groups_skipped_duplicate_copies"`
	BooksExcluded                int                   `json:"books_excluded"`
	BooksMerged                  int                   `json:"books_merged"`
	BooksSkipped                 int                   `json:"books_skipped"`
	GroupsFailed                 int                   `json:"groups_failed"`
	GroupsBlocked                int                   `json:"groups_blocked"`
	GroupsDrifted                int                   `json:"groups_drifted"`
	Groups                       []chapterGroupOutcome `json:"groups"`
}

// chapterExcluder returns the detection Exclude hook: the owner's manual-only
// libraries (Doctor Who / Big Finish / Torchwood, never bulk-applied) and the
// active iTunes library (never mutated). An unreadable iTunes config is an
// error: excluding nothing would be the unsafe direction.
func chapterExcluder() (func(*database.BookCore) bool, error) {
	roots, err := merge.ITunesProtectedRoots(config.Snapshot().ITunes)
	if err != nil {
		return nil, fmt.Errorf("resolve iTunes library roots: %w", err)
	}
	return func(b *database.BookCore) bool {
		if applygate.IsOwnerManualOnly(b.FilePath, "") {
			return true
		}
		p := filepath.Clean(b.FilePath)
		for _, r := range roots {
			if p == r || strings.HasPrefix(p, r+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}, nil
}

func chapterDetectOptions(p chapterGroupParams, exclude func(*database.BookCore) bool) scanner.ChapterDetectOptions {
	return scanner.ChapterDetectOptions{
		MinFiles:           p.MinFiles,
		MaxPerFileDuration: p.MaxPerFileDuration,
		PathPrefix:         p.PathPrefix,
		Exclude:            exclude,
	}
}

// detectChapterGroupsForRun loads every book and runs the detector with the
// run's params and exclusions. Used by the scan and the dry-run preview only:
// a real merge never detects to decide what to merge.
func detectChapterGroupsForRun(ctx context.Context, store maintenance.JobStore, p chapterGroupParams) (scanner.ChapterDetection, error) {
	exclude, err := chapterExcluder()
	if err != nil {
		return scanner.ChapterDetection{}, err
	}
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return scanner.ChapterDetection{}, err
	}
	if err := ctx.Err(); err != nil {
		return scanner.ChapterDetection{}, err
	}
	return scanner.DetectChapterGroupsWithOptions(books, chapterDetectOptions(p, exclude)), nil
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

// chapterGroupState is a fresh read of one group's members.
type chapterGroupState struct {
	books   []*database.Book               // in BookIDs order
	files   map[string][]database.BookFile // by book id
	members []chapterMemberSnapshot
}

// readChapterGroup reads every member and its files. Any read failure fails
// the group: an audit record or fingerprint built on a partial read would
// describe a merge that is not the one about to happen.
func readChapterGroup(store maintenance.JobStore, bookIDs []string) (*chapterGroupState, error) {
	st := &chapterGroupState{files: make(map[string][]database.BookFile, len(bookIDs))}
	for _, id := range bookIDs {
		b, err := store.GetBookByID(id)
		if err != nil || b == nil {
			return nil, fmt.Errorf("member %s not readable: %v", id, err)
		}
		files, err := store.GetBookFiles(id)
		if err != nil {
			return nil, fmt.Errorf("files of %s not readable: %w", id, err)
		}
		st.books = append(st.books, b)
		st.files[id] = files
		m := chapterMemberSnapshot{BookID: id, Title: b.Title, FileCount: len(files)}
		if b.Duration != nil {
			m.Duration = *b.Duration
		}
		if b.UpdatedAt != nil {
			m.UpdatedAt = b.UpdatedAt.UTC().Format(time.RFC3339Nano)
		}
		for _, f := range files {
			m.Files = append(m.Files, f.ID+"|"+f.FilePath)
		}
		sort.Strings(m.Files)
		st.members = append(st.members, m)
	}
	return st, nil
}

// chapterFingerprint hashes the member snapshots in order.
func chapterFingerprint(members []chapterMemberSnapshot) string {
	h := sha256.New()
	for _, m := range members {
		parts := []string{m.BookID, m.Title, strconv.Itoa(m.FileCount), strconv.Itoa(m.Duration), m.UpdatedAt}
		for _, part := range append(parts, m.Files...) {
			h.Write([]byte(part))
			h.Write([]byte{0x1f})
		}
		h.Write([]byte{0x1e})
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// chapterFileOrder is the merged book's play order: members in chapter order,
// each member's files by (disc, track, path).
func chapterFileOrder(st *chapterGroupState) []string {
	var out []string
	for _, b := range st.books {
		files := append([]database.BookFile(nil), st.files[b.ID]...)
		sort.SliceStable(files, func(i, j int) bool {
			if files[i].DiscNumber != files[j].DiscNumber {
				return files[i].DiscNumber < files[j].DiscNumber
			}
			if files[i].TrackNumber != files[j].TrackNumber {
				return files[i].TrackNumber < files[j].TrackNumber
			}
			return files[i].FilePath < files[j].FilePath
		})
		for _, f := range files {
			out = append(out, f.ID)
		}
	}
	return out
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
	FileOrder          []string            `json:"file_order"`
	Fingerprint        string              `json:"fingerprint"`
	BooksMerged        int                 `json:"books_merged"`
	FilesMoved         int                 `json:"files_moved"`
	JournalID          string              `json:"journal_id,omitempty"`
	Errors             []string            `json:"errors,omitempty"`
}
