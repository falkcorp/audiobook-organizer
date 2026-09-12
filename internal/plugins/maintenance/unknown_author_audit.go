// file: internal/plugins/maintenance/unknown_author_audit.go
// version: 1.0.0
// guid: 730dab79-4f62-426a-9ab9-ac727bf31a58
// last-edited: 2026-09-12

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- unknown-author-audit ---
//
// 🔴 WHY THIS EXISTS. The organizer's HasResolvedAuthor gate (TODO.md "Organize
// renames with placeholder metadata", parts 1-3) stops NEW target paths from
// being built with the "Unknown Author" fallback. It does nothing for books that
// were already organized under that directory before the gate landed -- the
// 2026-08-11 mass-reorganize alone did it 23,622 times (see
// docs/audits/2026-08-13-mass-reorganize-duplicated-14tb-under-unknown-author.md).
// The same TODO item asks to "audit how many books already have 'Unknown
// Author' baked into their organizer-tree paths"; this op is that census.
//
// 🔴 NOT COVERED BY MIGRATION 61. migration061Up (internal/database/
// migrations.go) flags books whose FilePath contains a literal '{' -- an
// UNRESOLVED {author} token. A path the organizer already expanded to
// ".../Unknown Author/..." carries no '{', so migration 61 never sees it.
//
// 🔴 REPORT-ONLY, DELIBERATELY. The op requests CapLibraryRead only, and its
// store surface (unknownAuthorAuditStore) is narrowed to GetAllBooksCore, so a
// write is not merely avoided but uncompilable. Output is a count and a capped,
// sorted sample (book ID, title, path) for a human to review. It is not
// scheduled; an operator starts it by def_id.
//
// Not in scope here (surfaced, not built): splitting the flagged population
// into "resolvable metadata" vs "genuinely unknown". BookCore already carries
// Title, Narrator, ISBN10/ISBN13 and ASIN, so that split could be done in memory
// later without per-book store reads. Organizer.HasResolvedAuthor was not used:
// it needs a hydrated *database.Book plus book_authors join reads per book.

// unknownAuthorAuditSampleLimit bounds how many flagged books are listed, so
// the report stays readable when tens of thousands of paths match.
const unknownAuthorAuditSampleLimit = 100

// unknownAuthorAuditParams are the JSON parameters accepted by the op.
type unknownAuthorAuditParams struct {
	// SampleLimit overrides how many flagged books are listed (0 = default).
	SampleLimit int `json:"sample_limit"`
}

// unknownAuthorAuditSample is one flagged book.
type unknownAuthorAuditSample struct {
	BookID   string `json:"book_id"`
	Title    string `json:"title"`
	FilePath string `json:"file_path"`
}

// unknownAuthorAuditReport is the outcome of one sweep.
type unknownAuthorAuditReport struct {
	// TotalBooks is every row GetAllBooksCore returned, before any exclusion.
	TotalBooks int

	// SoftDeleted counts trashed rows skipped by the explicit IsSoftDeleted
	// filter. GetAllBooksCore is expected to exclude them already (both of its
	// implementations are held to one rule -- see book_visibility.go and
	// TestGetAllBooksCore_MemDBAndPebbleAgree), so a non-zero value here means
	// that contract slipped. It is reported rather than silently absorbed.
	SoftDeleted int

	// EmptyFilePath counts live rows with a blank FilePath (never organized).
	// They cannot carry a baked placeholder and are never flagged.
	EmptyFilePath int

	// PlaceholderInPath counts live books whose FilePath has a DIRECTORY
	// segment equal to the placeholder author name.
	PlaceholderInPath int

	// Sample holds up to SampleLimit flagged books, sorted by FilePath then
	// book ID so the report is deterministic and diffable across runs.
	Sample []unknownAuthorAuditSample
}

func (r unknownAuthorAuditReport) summary() string {
	return fmt.Sprintf("total_books=%d soft_deleted=%d empty_file_path=%d placeholder_in_path=%d",
		r.TotalBooks, r.SoftDeleted, r.EmptyFilePath, r.PlaceholderInPath)
}

// unknownAuthorAuditStore is the narrow read surface this op needs, per the
// store_slices.go convention of listing methods explicitly.
type unknownAuthorAuditStore interface {
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
}

func (p *Plugin) unknownAuthorAuditDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.unknown-author-audit",
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Unknown Author path census",
		Description: "Counts books whose organized FilePath already has the \"" + authorname.Placeholder +
			"\" placeholder baked in as a directory segment, and lists a capped sample (book ID, " +
			"title, path) for review. REPORT-ONLY: takes no action and modifies nothing.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.unknown-author-audit",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		// 🔴 READ ONLY. No CapLibraryWrite is ever requested.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead},
		Run:          p.runUnknownAuthorAudit,
	}
}

func (p *Plugin) runUnknownAuthorAudit(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	var params unknownAuthorAuditParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}

	report, err := buildUnknownAuthorAudit(ctx, store, params, reporter)
	if err != nil {
		return err
	}

	log := reporter.Logger()
	for _, s := range report.Sample {
		log.Info("unknown-author-audit: placeholder baked into path",
			"book_id", s.BookID, "title", s.Title, "file_path", s.FilePath)
	}
	log.Info("unknown-author-audit complete",
		"total_books", report.TotalBooks, "soft_deleted", report.SoftDeleted,
		"empty_file_path", report.EmptyFilePath, "placeholder_in_path", report.PlaceholderInPath,
		"sampled", len(report.Sample))
	return nil
}

// pathHasPlaceholderAuthorDir reports whether any DIRECTORY segment of path is
// the placeholder author name.
//
// Segment equality, not substring: a title directory such as "The Unknown
// Author's Diary" must not match. The comparison goes through
// authorname.IsPlaceholder (TrimSpace + case-insensitive), the repo's canonical
// predicate, rather than a case-sensitive literal, so "unknown author" written
// by an older build or a case-insensitive filesystem is counted too.
//
// The final segment (the filename) is excluded on purpose. The organizer can
// also write the placeholder INTO a filename ("<title> - Unknown Author - read
// by ....m4b"), but whether that happens depends on the configured file
// pattern, and a real-author directory holding such a file is a different
// problem from a book filed under the placeholder tree. Such books are left
// uncounted here rather than folded silently into this number.
//
// Both '/' and '\' are treated as separators so the result does not depend on
// the host OS the report runs on.
func pathHasPlaceholderAuthorDir(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	segments := strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' })
	if len(segments) < 2 {
		return false // a bare filename has no directory segment
	}
	for _, seg := range segments[:len(segments)-1] {
		if authorname.IsPlaceholder(seg) {
			return true
		}
	}
	return false
}

// buildUnknownAuthorAudit performs the sweep and RETURNS the report, so the
// counts can be asserted as values rather than scraped from a log line.
//
// 🔴 PLAIN LOOP, ON PURPOSE. CLAUDE.md's concurrency rule targets whole-library
// loops that do meaningful per-item work (a DB read/write, network call,
// hashing, fuzzy compare, subprocess). This loop does none: after the single
// GetAllBooksCore snapshot, each row costs one string split and a few
// case-insensitive compares over an in-memory slice -- milliseconds for 100k+
// rows. migration061Up makes the same call over the same full-library scan
// ("the per-row check is a strings.Contains and is not worth a worker pool").
// The sibling filepath_collision_report.go does shard a similar in-memory pass,
// but its reduce step builds a path -> IDs map across the whole library; this
// one only increments counters, so there is nothing for extra cores to win.
// If a future edit adds a per-book store read (e.g. hydrating for
// HasResolvedAuthor), it must move to registry.RunItems WITH Concurrency set.
func buildUnknownAuthorAudit(ctx context.Context, store unknownAuthorAuditStore, params unknownAuthorAuditParams, reporter sdk.Reporter) (unknownAuthorAuditReport, error) {
	sampleLimit := params.SampleLimit
	if sampleLimit <= 0 {
		sampleLimit = unknownAuthorAuditSampleLimit
	}
	reporter.Logger().Info("unknown-author-audit start", "sample_limit", sampleLimit)

	// One limit-0 call = one consistent snapshot. Paging with offset across
	// multiple calls can skip or repeat rows if the memdb snapshot swaps
	// between pages (reconcile #2443).
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return unknownAuthorAuditReport{}, fmt.Errorf("GetAllBooksCore: %w", err)
	}
	_ = reporter.UpdateProgress(0, len(books),
		fmt.Sprintf("Scanning %d book(s) for a baked-in %q path segment…", len(books), authorname.Placeholder))

	report := unknownAuthorAuditReport{TotalBooks: len(books)}
	var flagged []unknownAuthorAuditSample
	for i := range books {
		if i%10000 == 0 {
			if err := ctx.Err(); err != nil {
				return unknownAuthorAuditReport{}, err
			}
		}
		b := &books[i]
		if b.IsSoftDeleted() {
			report.SoftDeleted++
			continue
		}
		if strings.TrimSpace(b.FilePath) == "" {
			report.EmptyFilePath++
			continue
		}
		if !pathHasPlaceholderAuthorDir(b.FilePath) {
			continue
		}
		report.PlaceholderInPath++
		flagged = append(flagged, unknownAuthorAuditSample{BookID: b.ID, Title: b.Title, FilePath: b.FilePath})
	}

	// Sorted before the cap: GetAllBooksCore ordering is not guaranteed
	// stable, and an unsorted cap would make the sample vary run to run.
	sort.Slice(flagged, func(i, j int) bool {
		if flagged[i].FilePath != flagged[j].FilePath {
			return flagged[i].FilePath < flagged[j].FilePath
		}
		return flagged[i].BookID < flagged[j].BookID
	})
	if len(flagged) > sampleLimit {
		flagged = flagged[:sampleLimit]
	}
	report.Sample = flagged

	_ = reporter.UpdateProgress(len(books), len(books), "REPORT ONLY (nothing modified) — "+report.summary())
	return report, nil
}
