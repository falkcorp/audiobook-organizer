// file: internal/plugins/maintenance/fs_regroup_xml.go
// version: 1.1.0
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
// It builds the regroup plan via the pure, tested itunesservice.GroupShatteredBooks.
// Default dry-run reports the plan; dryRun=false applies it: each cohesive shattered
// folder collapses to ONE survivor book (richest enrichment) with a BookFile per chapter,
// and the emptied shells are deleted. Mixed-identity folders are skipped for review.
// ZFS-snapshot-backed; run a dry-run + advisor review before applying on prod.

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

type fsRegroupParams struct {
	// DryRun defaults true (safe). Set false to apply the plan and mutate the library.
	DryRun bool `json:"dryRun"`
	// Limit caps how many shattered books the apply path heals in one run (0 = no cap).
	// Use limit=1 for the first canary apply before batching.
	Limit int `json:"limit"`
}

func (p *Plugin) fsRegroupXMLDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.fs-regroup-xml",
		Plugin:      "maintenance",
		DisplayName: "Heal shattered filesystem books (tag-anchored regroup)",
		Description: "Regroups filesystem-scanner-shattered books (one-book-per-chapter-subdir) back into " +
			"real books: groups single-file books by shared grandparent book-folder + tag identity (ASIN, else " +
			"title+author), attaches each chapter file to one survivor book, and deletes the emptied shells. " +
			"Mixed-identity folders are skipped for review. Default dry-run reports the plan; set dryRun=false to apply.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.fs-regroup-xml",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         120 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
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
			fsb.EnrichScore = enrichScore(b)
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
	targets, st := itunesservice.GroupShatteredBooks(all)

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
		"shattered-books=%d chapter-records=%d cohesive=%d author-mixed=%d with-asin=%d | sizes %v",
		len(targets), chapterRecords, cohesive, flagged, withASIN, hist)
	// Reconciliation: account for EVERY book so the record count ties out vs the inventory.
	recon := fmt.Sprintf(
		"RECONCILE: total=%d non-primary=%d multi-file=%d not-chapter-pattern=%d chapter-candidates=%d "+
			"→ singleton-groups=%d prefix-not-in-parent=%d grouped-records=%d",
		st.TotalBooks, st.NonPrimary, st.MultiFile, st.NotChapterPattern, st.ChapterCandidates,
		st.SingletonGroups, st.PrefixNotInParent, st.GroupedRecords)
	_ = reporter.Log(slog.LevelInfo, recon)

	if params.DryRun {
		_ = reporter.Log(slog.LevelInfo, "DRY RUN PLAN: "+summary)
		_ = reporter.Log(slog.LevelInfo, "DRY RUN examples: "+strings.Join(fsRegroupExamples(targets, 12), " | "))
		_ = reporter.UpdateProgress(3, 3, "DRY RUN — "+summary)
		return nil
	}

	// APPLY: collapse each cohesive shattered book into ONE record. Snapshot-backed.
	if params.Limit > 0 {
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("APPLY (limit=%d) — %s", params.Limit, summary))
	} else {
		_ = reporter.Log(slog.LevelInfo, "APPLY — "+summary)
	}
	return p.applyFSRegroup(ctx, store, targets, params.Limit, reporter)
}

// applyFSRegroup executes the regroup: for each cohesive target, attach a BookFile
// for every member's chapter file to the survivor book, set its canonical title +
// folder path, reassign any external-ids off the deleted shells, then delete the
// emptied non-survivor chapter books. Lean by design — stat-only per file, no hashing
// (the tag-backfill op fills RawTags/hashes after). Recompute aggregates per survivor.
func (p *Plugin) applyFSRegroup(ctx context.Context, store database.Store, targets []itunesservice.FSRegroupTarget, limit int, reporter sdk.Reporter) error {
	var (
		healedBooks, filesAttached, deleted, deleteSkipped, skippedMixed, errCount int
		lastLog                                                                    = time.Now()
	)
	for ti := range targets {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if limit > 0 && healedBooks >= limit {
			break // canary cap: heal only `limit` books this run
		}
		t := targets[ti]
		if !t.Cohesive || t.SurvivorID == "" {
			skippedMixed++
			continue
		}

		survivor, err := store.GetBookByID(t.SurvivorID)
		if err != nil || survivor == nil {
			errCount++
			continue
		}

		// Attach one BookFile per member chapter file (ordered) to the survivor.
		for order, m := range t.Members {
			var size int64
			if fi, serr := os.Stat(m.FilePath); serr == nil {
				size = fi.Size()
			}
			bf := &database.BookFile{
				ID:          ulid.Make().String(),
				BookID:      survivor.ID,
				FilePath:    m.FilePath,
				Format:      strings.TrimPrefix(strings.ToLower(filepath.Ext(m.FilePath)), "."),
				FileSize:    size,
				Duration:    m.DurationSec,
				TrackNumber: order + 1,
			}
			if err := store.UpsertBookFile(bf); err != nil {
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("attach file %s -> %s failed: %v", m.FilePath, survivor.ID, err))
				errCount++
				continue
			}
			filesAttached++
		}

		// Canonical title + folder path on the survivor.
		survivor.Title = t.Title
		survivor.FilePath = t.BookFolder
		if t.ASIN != "" && (survivor.ASIN == nil || *survivor.ASIN == "") {
			asin := t.ASIN
			survivor.ASIN = &asin
		}
		if _, err := store.UpdateBook(survivor.ID, survivor); err != nil {
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("update survivor %s failed: %v", survivor.ID, err))
			errCount++
		}

		// Delete the emptied non-survivor chapter shells (reassign ext-ids first).
		for _, m := range t.Members {
			if m.ID == survivor.ID {
				continue
			}
			if exts, _ := store.GetExternalIDsForBook(m.ID); len(exts) > 0 {
				if rerr := store.ReassignExternalIDs(m.ID, survivor.ID); rerr != nil {
					_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("reassign ext-ids %s->%s failed; skipping delete: %v", m.ID, survivor.ID, rerr))
					deleteSkipped++
					continue
				}
			}
			// Guard: a shattered chapter book has no BookFiles of its own; re-assert.
			if files, _ := store.GetBookFiles(m.ID); len(files) != 0 {
				deleteSkipped++
				continue
			}
			if err := store.DeleteBook(m.ID); err != nil {
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("delete shell %s failed: %v", m.ID, err))
				errCount++
				continue
			}
			deleted++
		}

		if err := store.RecomputeBookAggregates(survivor.ID); err != nil {
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("recompute %s failed: %v", survivor.ID, err))
			errCount++
		}
		healedBooks++

		if time.Since(lastLog) >= 15*time.Second {
			_ = reporter.UpdateProgress(ti+1, len(targets), fmt.Sprintf(
				"healed %d books, %d files attached, %d shells deleted…", healedBooks, filesAttached, deleted))
			lastLog = time.Now()
		}
	}

	result := fmt.Sprintf("APPLIED — healed=%d books, files-attached=%d, shells-deleted=%d, delete-skipped=%d, mixed-skipped=%d, errors=%d",
		healedBooks, filesAttached, deleted, deleteSkipped, skippedMixed, errCount)
	_ = reporter.Log(slog.LevelInfo, result)
	_ = reporter.UpdateProgress(len(targets), len(targets), result)
	if errCount > 0 {
		return fmt.Errorf("%d errors during fs-regroup apply (see op log)", errCount)
	}
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

func asinTag(asin string) string {
	if asin == "" {
		return ""
	}
	return ", asin " + asin
}
