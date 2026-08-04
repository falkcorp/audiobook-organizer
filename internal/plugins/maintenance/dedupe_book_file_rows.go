// file: internal/plugins/maintenance/dedupe_book_file_rows.go
// version: 1.0.0
// guid: 1c7f4b93-6a05-42e8-9d31-8b0e5a2f7c46
// last-edited: 2026-08-03

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
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// DedupeBookFileRowsParams are the JSON parameters for the duplicate book_file
// row cleanup.
type DedupeBookFileRowsParams struct {
	// Apply, when true, deletes the redundant rows. Default false — dry run.
	// Mirrors maintenance.title-repair's convention: a destructive op must be
	// asked for explicitly, never reached by forgetting a flag.
	Apply bool `json:"apply"`
	// Limit caps how many BOOKS are processed (0 = all). Useful for a canary.
	Limit int `json:"limit,omitempty"`
}

func (p *Plugin) dedupeBookFileRowsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.dedupe-book-file-rows",
		Plugin:      "maintenance",
		DisplayName: "De-duplicate book_file rows",
		Description: "Finds books holding MORE THAN ONE book_file row for the same file_path and removes the redundant rows, then recomputes the book's aggregates. Duplicated rows inflate a book's total duration and file size by the duplication factor. Dry-run by default — pass {\"apply\": true} to delete.",
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.dedupe-book-file-rows",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runDedupeBookFileRows,
	}
}

// dupGroup is one (book, file_path) that has more than one row.
type dupGroup struct {
	bookID   string
	filePath string
	rowIDs   []string // every row for this path, keeper first after ranking
}

// rankKeeper orders rows so the BEST-EVIDENCED row sorts first and therefore
// survives.
//
// 🔴 THE ORDER HERE IS DATA-LOSS-CRITICAL. This repo has already shipped bugs
// that wiped AcoustIDFingerprint (see the UpdateBookFile fingerprint-wipe
// incident). Deleting the wrong twin would destroy a fingerprint that took a
// full-file decode to produce, and nothing downstream would report it — the book
// would simply stop matching in dedup.
//
// Preference order, most to least important:
//  1. has an AcoustID fingerprint  — expensive to recompute, impossible to guess
//  2. has a non-zero duration      — the field this whole cleanup exists to fix
//  3. has a file hash              — used by integrity checks
//  4. lexicographically smallest ID — arbitrary but STABLE, so a dry run and the
//     apply that follows it choose the same keeper
func rankKeeper(files []database.BookFile) []database.BookFile {
	out := append([]database.BookFile(nil), files...)
	score := func(f database.BookFile) (int, int, int) {
		fp, dur, hash := 0, 0, 0
		if len(f.AcoustIDFingerprint) > 0 {
			fp = 1
		}
		if f.Duration > 0 {
			dur = 1
		}
		if strings.TrimSpace(f.FileHash) != "" {
			hash = 1
		}
		return fp, dur, hash
	}
	sort.SliceStable(out, func(i, j int) bool {
		fi, di, hi := score(out[i])
		fj, dj, hj := score(out[j])
		if fi != fj {
			return fi > fj
		}
		if di != dj {
			return di > dj
		}
		if hi != hj {
			return hi > hj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// mergeMissingFields fills fields the keeper lacks from the twins about to be
// deleted, and reports whether anything was salvaged.
//
// Strictly additive: a field the keeper already has is never touched, so the
// merge can only ever recover data, never degrade it. The twins are scanned in
// ranked order, so the best-evidenced donor is consulted first.
func mergeMissingFields(keeper database.BookFile, twins []database.BookFile) (database.BookFile, bool) {
	changed := false
	for i := range twins {
		t := twins[i]
		if keeper.Duration <= 0 && t.Duration > 0 {
			keeper.Duration = t.Duration
			changed = true
		}
		if len(keeper.AcoustIDFingerprint) == 0 && len(t.AcoustIDFingerprint) > 0 {
			keeper.AcoustIDFingerprint = t.AcoustIDFingerprint
			changed = true
		}
		if strings.TrimSpace(keeper.FileHash) == "" && strings.TrimSpace(t.FileHash) != "" {
			keeper.FileHash = t.FileHash
			changed = true
		}
		if keeper.FileSize <= 0 && t.FileSize > 0 {
			keeper.FileSize = t.FileSize
			changed = true
		}
		if keeper.AcoustIDFingerprintDurationSec <= 0 && t.AcoustIDFingerprintDurationSec > 0 {
			keeper.AcoustIDFingerprintDurationSec = t.AcoustIDFingerprintDurationSec
			changed = true
		}
	}
	return keeper, changed
}

func (p *Plugin) runDedupeBookFileRows(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params DedupeBookFileRowsParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	log := reporter.Logger()
	log.Info("dedupe-book-file-rows: starting", "apply", params.Apply, "limit", params.Limit)

	// PASS 1 — cheap. The Core projection is a memdb read, so it is fast enough
	// to sweep the whole library, and file_path is all we need to FIND duplicates.
	//
	// It is deliberately NOT used to DECIDE anything: the Core projection does not
	// carry AcoustIDFingerprint, so choosing a keeper from it would be choosing
	// blind on the one field we must not lose.
	cores, err := store.GetAllBookFilesCore()
	if err != nil {
		return fmt.Errorf("scan book_files: %w", err)
	}
	byBookPath := map[string][]string{}
	for i := range cores {
		c := cores[i]
		if c.BookID == "" || strings.TrimSpace(c.FilePath) == "" {
			continue // orphan / pathless rows belong to orphan-book-files-cleanup
		}
		key := c.BookID + "\x00" + c.FilePath
		byBookPath[key] = append(byBookPath[key], c.ID)
	}

	affected := map[string][]dupGroup{} // bookID -> duplicate groups
	dupRows := 0
	for key, ids := range byBookPath {
		if len(ids) < 2 {
			continue
		}
		parts := strings.SplitN(key, "\x00", 2)
		g := dupGroup{bookID: parts[0], filePath: parts[1], rowIDs: ids}
		affected[g.bookID] = append(affected[g.bookID], g)
		dupRows += len(ids) - 1
	}

	bookIDs := make([]string, 0, len(affected))
	for id := range affected {
		bookIDs = append(bookIDs, id)
	}
	sort.Strings(bookIDs) // deterministic order so runs are comparable
	if params.Limit > 0 && len(bookIDs) > params.Limit {
		bookIDs = bookIDs[:params.Limit]
	}

	log.Info("dedupe-book-file-rows: scan complete",
		"total_rows", len(cores),
		"books_affected", len(affected),
		"redundant_rows", dupRows)

	prog := sdk.NewProgress(reporter, len(bookIDs))
	prog.Start(fmt.Sprintf("%d book(s) hold duplicate book_file rows (%d redundant rows)",
		len(affected), dupRows))

	var deleted, wouldDelete, failed, recomputed, salvaged int
	var examples []string

	// PASS 2 — per affected book only, so the expensive full-fidelity read is paid
	// for the handful of books that need it rather than the whole library.
	for i, bookID := range bookIDs {
		if ctx.Err() != nil {
			log.Warn("dedupe-book-file-rows: cancelled",
				"deleted", deleted, "remaining", len(bookIDs)-i)
			return ctx.Err()
		}

		// GetBookFiles reads Pebble directly (raw prefix iteration), NOT memdb, so
		// AcoustIDFingerprint is present and un-stripped here. That is exactly why
		// the keeper decision happens in this pass and not the previous one.
		files, ferr := store.GetBookFiles(bookID)
		if ferr != nil {
			failed++
			log.Warn("dedupe-book-file-rows: GetBookFiles failed", "book_id", bookID, "err", ferr)
			continue
		}
		byPath := map[string][]database.BookFile{}
		for fi := range files {
			f := files[fi]
			if strings.TrimSpace(f.FilePath) == "" {
				continue
			}
			byPath[f.FilePath] = append(byPath[f.FilePath], f)
		}

		changedThisBook := false
		for path, rows := range byPath {
			if len(rows) < 2 {
				continue
			}
			ranked := rankKeeper(rows)
			keeper, redundant := ranked[0], ranked[1:]

			// 🔴 MERGE, DON'T JUST PICK. Ranking alone chooses a whole ROW, so a
			// keeper that carries a fingerprint but no duration silently loses the
			// duration held by one of its twins.
			//
			// That is not hypothetical: the first canary run left "The Trapped Mind
			// Project" (130 rows for one file) reading 0.00h, because the
			// fingerprinted row it kept had Duration == 0 while a discarded twin had
			// the real value.
			//
			// So salvage every field the keeper is missing before its twins are
			// deleted. Only ever FILLS empty fields — a value the keeper already has
			// always wins, so this can never overwrite good data with worse.
			merged, changed := mergeMissingFields(keeper, redundant)
			keeper = merged

			if len(examples) < 10 {
				examples = append(examples, fmt.Sprintf("%s: %d rows for %q (keeping %s)",
					bookID, len(rows), shortPath(path), keeper.ID))
			}
			if !params.Apply {
				wouldDelete += len(redundant)
				if changed {
					salvaged++
				}
				continue
			}

			// Persist the salvaged fields BEFORE deleting the donors. If the write
			// fails we skip this group entirely rather than delete rows whose data
			// was never rescued — losing a duration is recoverable, losing it while
			// also deleting the only copy is not.
			if changed {
				if uerr := store.UpdateBookFile(keeper.ID, &keeper); uerr != nil {
					failed++
					log.Warn("dedupe-book-file-rows: could not persist salvaged fields; leaving this group intact",
						"book_id", bookID, "keeper", keeper.ID, "err", uerr)
					continue
				}
				salvaged++
			}

			for ri := range redundant {
				if derr := store.DeleteBookFile(redundant[ri].ID); derr != nil {
					failed++
					log.Warn("dedupe-book-file-rows: delete failed",
						"book_id", bookID, "row_id", redundant[ri].ID, "err", derr)
					continue
				}
				deleted++
				changedThisBook = true
			}
		}

		// Totals are a plain sum over the surviving rows, so they are only correct
		// once the redundant rows are gone — recompute AFTER deleting, per book.
		if params.Apply && changedThisBook {
			if rerr := store.RecomputeBookAggregates(bookID); rerr != nil {
				failed++
				log.Warn("dedupe-book-file-rows: RecomputeBookAggregates failed",
					"book_id", bookID, "err", rerr)
			} else {
				recomputed++
			}
		}
		prog.StepN(i+1, fmt.Sprintf("Processed %d/%d affected books", i+1, len(bookIDs)))
	}

	verb := fmt.Sprintf("would delete %d (would salvage fields on %d keepers)", wouldDelete, salvaged)
	if params.Apply {
		verb = fmt.Sprintf("deleted %d (salvaged fields on %d keepers, recomputed %d books)",
			deleted, salvaged, recomputed)
	}
	// ⚠️ Operational note, learned the hard way on the first canary: the corrected
	// totals are NOT visible until memdb catches up. Immediately after an apply the
	// list projection still reported the pre-delete duration and file count, and a
	// service restart was what surfaced the (already correct) values. Say so, or
	// the next operator concludes the op did nothing.
	summary := fmt.Sprintf(
		"dedupe-book-file-rows: %d rows scanned, %d books affected, %d redundant rows, %s, failed %d "+
			"| NOTE: corrected totals may not appear until memdb refreshes (restart) | e.g. %s",
		len(cores), len(affected), dupRows, verb, failed, strings.Join(examples, "; "))
	_ = reporter.Log(slog.LevelInfo, summary)
	prog.Done(summary)
	return nil
}

// shortPath trims a long path to its last two segments for log readability.
func shortPath(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) <= 2 {
		return p
	}
	return ".../" + strings.Join(parts[len(parts)-2:], "/")
}
