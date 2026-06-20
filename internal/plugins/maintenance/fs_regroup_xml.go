// file: internal/plugins/maintenance/fs_regroup_xml.go
// version: 1.0.0
// guid: 7d2a9c14-3e86-4b50-9f71-2c8e0a6d4b95
// last-edited: 2026-06-20

// Package maintenance — op maintenance.fs-regroup-xml.
//
// Heals filesystem-scanner SHATTERED books: real audiobooks that were imported as
// one standalone book per single-file chapter subdir (`<Author>/<Book>/<Book> - N/<file>`),
// shattering ~1,075 books into ~35,577 records (~72% of the library) and feeding the
// 380K exact-layer dedup-candidate explosion. The grouping signal is in the files' own
// tags (already in the DB: Book.Title = album, author, ASIN); the scanner just grouped
// by FOLDER instead of by album. See .claude/notes/shattered-books-inventory.md.
//
// v1 is DRY-RUN ONLY: it builds the regroup plan via the pure, tested
// itunesservice.GroupShatteredBooks and reports it. The apply path (create one unified
// book per shattered folder, move chapter files in, delete emptied shells, backfill PIDs)
// lands in a follow-up once the dry-run is reviewed + advisor-gated.

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

type fsRegroupParams struct {
	// DryRun defaults true. Apply is intentionally NOT wired in v1 — this op only
	// reports. A non-dry-run call returns an explicit not-implemented error so no
	// caller can mutate the library before the plan is reviewed.
	DryRun bool `json:"dryRun"`
}

func (p *Plugin) fsRegroupXMLDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.fs-regroup-xml",
		Plugin:      "maintenance",
		DisplayName: "Heal shattered filesystem books (tag-anchored regroup)",
		Description: "Reports the plan to regroup filesystem-scanner-shattered books (one-book-per-chapter-subdir) " +
			"back into real books, grouping single-file books by shared grandparent book-folder + tag identity " +
			"(ASIN, else title+author). DRY-RUN ONLY in v1 — reports counts + samples, writes nothing.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.fs-regroup-xml",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         60 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead},
		Run:             p.runFSRegroupXML,
	}
}

func (p *Plugin) runFSRegroupXML(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params := fsRegroupParams{DryRun: true}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	if !params.DryRun {
		return fmt.Errorf("maintenance.fs-regroup-xml v1 is dry-run only; apply path not yet implemented")
	}
	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	_ = reporter.UpdateProgress(0, 3, "Phase 1/3: scanning books…")

	// Pass 1: all books → slim FSBook view (id, title, authorID, asin, path, primary, duration).
	bookMeta := make(map[string]*itunesservice.FSBook)
	const page = 1000
	off := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		books, err := store.GetAllBooks(page, off)
		if err != nil {
			return fmt.Errorf("GetAllBooks offset=%d: %w", off, err)
		}
		if len(books) == 0 {
			break
		}
		for i := range books {
			b := &books[i]
			fsb := itunesservice.FSBook{
				ID:        b.ID,
				Title:     b.Title,
				FilePath:  b.FilePath,
				IsPrimary: b.IsPrimaryVersion == nil || *b.IsPrimaryVersion, // nil treated as primary
			}
			if b.AuthorID != nil {
				fsb.AuthorID = *b.AuthorID
			}
			if b.ASIN != nil {
				fsb.ASIN = strings.TrimSpace(*b.ASIN)
			}
			if b.Duration != nil {
				fsb.DurationSec = *b.Duration
			}
			bookMeta[b.ID] = &fsb
		}
		off += len(books)
		_ = reporter.UpdateProgress(0, 3, fmt.Sprintf("Phase 1/3: scanned %d books…", off))
		if len(books) < page {
			break
		}
	}

	// Pass 2: file counts so already-multi-file books are excluded.
	_ = reporter.UpdateProgress(1, 3, "Phase 2/3: counting book files…")
	files, err := store.GetAllBookFiles()
	if err != nil {
		return fmt.Errorf("GetAllBookFiles: %w", err)
	}
	for i := range files {
		if fsb := bookMeta[files[i].BookID]; fsb != nil {
			fsb.FileCount++
		}
	}

	all := make([]itunesservice.FSBook, 0, len(bookMeta))
	for _, fsb := range bookMeta {
		all = append(all, *fsb)
	}

	// Phase 3: group + report.
	_ = reporter.UpdateProgress(2, 3, "Phase 3/3: grouping shattered books…")
	targets := itunesservice.GroupShatteredBooks(all)

	var cohesive, flagged, withASIN, chapterRecords int
	hist := map[string]int{}
	for _, t := range targets {
		n := len(t.Members)
		chapterRecords += n
		if t.Cohesive {
			cohesive++
		} else {
			flagged++
		}
		if t.ASIN != "" {
			withASIN++
		}
		switch {
		case n <= 4:
			hist["2-4"]++
		case n <= 9:
			hist["5-9"]++
		case n <= 49:
			hist["10-49"]++
		default:
			hist["50+"]++
		}
	}

	summary := fmt.Sprintf(
		"shattered-books=%d chapter-records=%d cohesive=%d flagged-mixed=%d with-asin=%d | sizes %v",
		len(targets), chapterRecords, cohesive, flagged, withASIN, hist)
	_ = reporter.Log(slog.LevelInfo, "DRY RUN PLAN: "+summary)
	_ = reporter.Log(slog.LevelInfo, "DRY RUN examples: "+strings.Join(fsRegroupExamples(targets, 12), " | "))
	if flagged > 0 {
		_ = reporter.Log(slog.LevelWarn, "FLAGGED (mixed-identity folders, review before apply): "+
			strings.Join(fsRegroupFlagged(targets, 10), " | "))
	}
	_ = reporter.UpdateProgress(3, 3, "DRY RUN — "+summary)
	return nil
}

// fsRegroupExamples renders the largest few recovered books for the dry-run log.
func fsRegroupExamples(targets []itunesservice.FSRegroupTarget, n int) []string {
	sorted := make([]itunesservice.FSRegroupTarget, len(targets))
	copy(sorted, targets)
	sort.SliceStable(sorted, func(i, j int) bool { return len(sorted[i].Members) > len(sorted[j].Members) })
	out := make([]string, 0, n)
	for _, t := range sorted {
		if len(out) >= n {
			break
		}
		out = append(out, fmt.Sprintf("%q (%d ch%s)", t.Title, len(t.Members), asinTag(t.ASIN)))
	}
	return out
}

func fsRegroupFlagged(targets []itunesservice.FSRegroupTarget, n int) []string {
	out := make([]string, 0, n)
	for _, t := range targets {
		if t.Cohesive || len(out) >= n {
			continue
		}
		out = append(out, fmt.Sprintf("%s → %v", t.BookFolder, t.DistinctTitles))
	}
	return out
}

func asinTag(asin string) string {
	if asin == "" {
		return ""
	}
	return ", asin " + asin
}
