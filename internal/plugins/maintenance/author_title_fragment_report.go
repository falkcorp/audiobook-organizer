// file: internal/plugins/maintenance/author_title_fragment_report.go
// version: 1.0.0
// guid: 13220b5a-7a38-4977-b0f3-257781a67fe3
// last-edited: 2026-09-11

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/personname"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- author-title-fragment-scan ---
//
// WHY THIS EXISTS. Author rows whose name begins with '-' ("- Edgedancer",
// "- The Emperor's Soul") are track and chapter titles an importer parsed into
// the author field; 57 of them were counted on the live library. The open question
// (TODO.md, "Check how many other author rows are title fragments") is how many
// OTHER rows have the same origin without the hyphen giveaway. This op answers it
// by enumeration only: count and sample, never fix. The fix is deferred to a
// separate decision.
//
// No existing op covers this:
//   - purge-empty-authors keys on a zero book count, so it cannot see a
//     title-fragment row that HAS books attached — the rows that matter most.
//   - author-strip-merge keys on numeric track prefixes ("001-147 Kevin J Anderson").
//   - author-conjunction-repair keys on a leading '&' / 'and'.
//   - author-split-scan keys on composite delimiters and mutates.
//
// REPORT ONLY. No renames, merges or deletes; the op requests CapLibraryRead and
// nothing else, and has no schedule (it is exploratory, triggered by hand).

// titleFragmentSampleLimit caps the sample kept PER REASON, so the activity log
// shows a reviewable slice rather than every flagged row.
const titleFragmentSampleLimit = 100

// Flag reasons. A row is filed under the FIRST reason that matches, so the two
// counts are disjoint and sum to Flagged.
const (
	titleFragmentReasonLeadingHyphen = "leading_hyphen"
	titleFragmentReasonNotPersonName = "not_person_shape"
)

// titleFragmentRow is one flagged author as it appears in the sample.
type titleFragmentRow struct {
	ID    int
	Name  string
	Books int
	// Reason is one of the titleFragmentReason* constants.
	Reason string
}

// titleFragmentReport is the outcome of one pass.
type titleFragmentReport struct {
	TotalAuthors int
	// Blank counts authors whose name is empty or whitespace. They are NOT
	// flagged: a blank name is a different defect with no title in it to
	// enumerate. Counted separately so they are not silently lost either.
	Blank int
	// LeadingHyphen and NotPersonName are disjoint; Flagged is their sum.
	LeadingHyphen int
	NotPersonName int
	Flagged       int
	// Separate samples per reason. One pool sorted by book count would be filled
	// entirely by shape failures that have books, and the zero-book hyphen rows
	// this op exists for would never make it into the cap.
	HyphenSample    []titleFragmentRow
	NotPersonSample []titleFragmentRow
}

func (r titleFragmentReport) summary() string {
	return fmt.Sprintf(
		"REPORT ONLY (nothing changed) — authors=%d flagged=%d leading-hyphen=%d not-person-shape=%d blank(skipped)=%d",
		r.TotalAuthors, r.Flagged, r.LeadingHyphen, r.NotPersonName, r.Blank)
}

// classifyTitleFragmentAuthor returns the flag reason for name, or "" if the
// name is not flagged. Blank names return "" (the caller counts them).
func classifyTitleFragmentAuthor(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "-") {
		return titleFragmentReasonLeadingHyphen
	}
	// The shape check is applied to the WHOLE name, not as a split-decision
	// component. It also rejects single-word names ("Plato"), which is why the
	// two reasons are reported separately: the hyphen count is the high-signal
	// one, the shape count is a broad net for a human to read.
	if !personname.LooksLikePersonName(trimmed) {
		return titleFragmentReasonNotPersonName
	}
	return ""
}

// scanTitleFragmentAuthors builds the report from an already-loaded author list.
//
// A plain loop, not registry.RunItems or a worker pool: the per-author work is
// two pure in-memory string predicates over a slice that is already loaded, with
// no DB, network or subprocess call per item. A pool would add scheduling and
// contention on the shared report without adding throughput.
func scanTitleFragmentAuthors(authors []database.Author, bookCounts map[int]int, sampleLimit int) titleFragmentReport {
	report := titleFragmentReport{TotalAuthors: len(authors)}
	var hyphen, notPerson []titleFragmentRow
	for _, a := range authors {
		if strings.TrimSpace(a.Name) == "" {
			report.Blank++
			continue
		}
		reason := classifyTitleFragmentAuthor(a.Name)
		if reason == "" {
			continue
		}
		row := titleFragmentRow{ID: a.ID, Name: a.Name, Books: bookCounts[a.ID], Reason: reason}
		if reason == titleFragmentReasonLeadingHyphen {
			report.LeadingHyphen++
			hyphen = append(hyphen, row)
		} else {
			report.NotPersonName++
			notPerson = append(notPerson, row)
		}
	}
	report.Flagged = report.LeadingHyphen + report.NotPersonName
	report.HyphenSample = topTitleFragmentRows(hyphen, sampleLimit)
	report.NotPersonSample = topTitleFragmentRows(notPerson, sampleLimit)
	return report
}

// topTitleFragmentRows orders rows by book count descending (the rows with the
// most books attached are the most consequential), then by ID for a deterministic
// order two runs can be diffed on, and returns at most limit of them.
func topTitleFragmentRows(rows []titleFragmentRow, limit int) []titleFragmentRow {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Books != rows[j].Books {
			return rows[i].Books > rows[j].Books
		}
		return rows[i].ID < rows[j].ID
	})
	if limit >= 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

func (p *Plugin) authorTitleFragmentScanDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.author-title-fragment-scan",
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Author title-fragment scan",
		Description: "REPORT ONLY — changes nothing. Walks every author and flags names that begin " +
			"with '-' (track/chapter titles parsed into the author field, e.g. '- Edgedancer') or that " +
			"fail the person-name shape check (which also catches single-word names, so read that count " +
			"as a net, not a verdict). Logs counts plus up to 100 sample rows per reason (author ID, " +
			"name, book count). The book count is the DISPLAY counter: it omits trashed books, " +
			"non-primary versions and junction-only co-author credits, so '0 books' here does NOT mean " +
			"the author is safe to delete.",
		// ResumeDrop: a read-only report is cheap to re-trigger, and a resumed run
		// would only repeat the same reads.
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.author-title-fragment-scan",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         10 * time.Minute,
		// No Schedule: manual trigger only.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead},
		Run:          p.runAuthorTitleFragmentScan,
	}
}

func (p *Plugin) runAuthorTitleFragmentScan(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	_ = reporter.Log(slog.LevelInfo, "Starting author title-fragment scan (report only)")
	_ = reporter.UpdateProgress(0, 3, "Listing authors…")

	authors, err := store.GetAllAuthors()
	if err != nil {
		return fmt.Errorf("list authors: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	_ = reporter.UpdateProgress(1, 3, "Counting books per author…")
	// GetAllAuthorBookCounts is the DISPLAY counter (skips trashed books,
	// non-primary versions and junction-only co-author credits — see the long
	// comment in author_purge_empty.go). Acceptable for a report that only shows
	// how many books a flagged row touches, and it is labelled as such in the
	// op description. A read failure FAILS the op rather than rendering every row
	// as "0 books": a report of zeros is exactly the artifact someone would later
	// cite to justify a delete.
	bookCounts, err := store.GetAllAuthorBookCounts()
	if err != nil {
		return fmt.Errorf("author book counts: %w", err)
	}

	_ = reporter.UpdateProgress(2, 3, fmt.Sprintf("Checking %d author names…", len(authors)))
	report := scanTitleFragmentAuthors(authors, bookCounts, titleFragmentSampleLimit)

	summary := report.summary()
	_ = reporter.Log(slog.LevelInfo, summary)
	for _, rows := range [][]titleFragmentRow{report.HyphenSample, report.NotPersonSample} {
		for _, r := range rows {
			_ = reporter.Log(slog.LevelInfo,
				fmt.Sprintf("flagged author reason=%s author_id=%d books=%d name=%q", r.Reason, r.ID, r.Books, r.Name),
				slog.Int("author_id", r.ID), slog.Int("books", r.Books),
				slog.String("reason", r.Reason), slog.String("name", r.Name))
		}
	}
	_ = reporter.UpdateProgress(3, 3, summary)
	return nil
}
