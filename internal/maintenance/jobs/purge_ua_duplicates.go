// file: internal/maintenance/jobs/purge_ua_duplicates.go
// version: 1.3.0
// guid: 7a4d1e58-9c26-4b73-b0f2-5e8c3a6d9f41
// last-edited: 2026-08-25

package jobs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"golang.org/x/sync/errgroup"
)

func init() { maintenance.Register(&purgeUADuplicatesJob{}) }

// purgeUADuplicatesJob soft-deletes "Unknown Author/" books that are verified
// duplicates of a real-author copy. Rules come from the 2026-08-13 audit
// (docs/audits/2026-08-13-mass-reorganize-duplicated-14tb-under-unknown-author.md)
// and are NON-NEGOTIABLE:
//
//   - Identity is measured on INTERIOR content (probes at 25/50/75% of the
//     file), never head/tail — tag blocks differ between an Unknown Author
//     copy and its correctly-tagged twin even when the audio is identical.
//   - The twin must exist ON DISK right now, outside the Unknown Author tree,
//     and belong to a live (non-soft-deleted) book.
//   - EVERY active file of the UA book must have a verified twin, or the book
//     is left alone. A size match alone proves nothing (fixed-bitrate rips
//     collide); the ~314 UA-only survivors must never match.
//   - Purge = soft-delete (MarkedForDeletion), the same reversible state every
//     other deletion path uses. Files are not touched here; the normal trash
//     lifecycle owns them (blocks are ZFS-cloned — there is no space to win,
//     this is library hygiene).
type purgeUADuplicatesJob struct{}

func (j *purgeUADuplicatesJob) ID() string       { return "purge-unknown-author-duplicates" }
func (j *purgeUADuplicatesJob) Name() string     { return "Purge verified Unknown Author duplicates" }
func (j *purgeUADuplicatesJob) Category() string { return "maintenance" }
func (j *purgeUADuplicatesJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: true}
}
func (j *purgeUADuplicatesJob) Description() string {
	return "Soft-delete Unknown Author books whose every file has an interior-content-verified twin outside the Unknown Author tree"
}
func (j *purgeUADuplicatesJob) CanResume() bool { return false } // idempotent

// uaProbeSize is how many bytes each interior probe compares. Three probes ×
// 64 KiB reads both files at 25/50/75% — small enough to sweep hundreds of
// thousands of files, large enough that fixed-bitrate size collisions cannot
// pass by accident.
const uaProbeSize = 64 * 1024

func (j *purgeUADuplicatesJob) Run(ctx context.Context, store maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) error {
	root := config.AppConfig.RootDir
	if root == "" {
		return fmt.Errorf("purge-unknown-author-duplicates: RootDir not configured")
	}
	uaPrefix := filepath.Join(root, authorname.Placeholder) + string(filepath.Separator)

	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return fmt.Errorf("list books: %w", err)
	}
	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return fmt.Errorf("list book files: %w", err)
	}

	liveBook := make(map[string]*database.BookCore, len(books))
	for i := range books {
		if !books[i].IsSoftDeleted() {
			liveBook[books[i].ID] = &books[i]
		}
	}

	// Candidate twins: files OUTSIDE the UA tree owned by live books, indexed
	// by size. UA-side files grouped per owning book.
	twinBySize := make(map[int64][]string)
	uaFilesByBook := make(map[string][]database.BookFileCore)
	for i := range files {
		f := &files[i]
		if f.FilePath == "" || f.Missing {
			continue
		}
		if _, live := liveBook[f.BookID]; !live {
			continue
		}
		if strings.HasPrefix(f.FilePath, uaPrefix) {
			uaFilesByBook[f.BookID] = append(uaFilesByBook[f.BookID], *f)
		} else if f.FileSize > 0 {
			twinBySize[f.FileSize] = append(twinBySize[f.FileSize], f.FilePath)
		}
	}

	// A UA book qualifies for the sweep only if ALL its active files sit in
	// the UA tree — a book with files on both sides is not "the Unknown copy".
	var uaBooks []string
	for id, uaFiles := range uaFilesByBook {
		b := liveBook[id]
		if b == nil || !strings.HasPrefix(b.FilePath, uaPrefix) {
			continue
		}
		total := 0
		for i := range files {
			if files[i].BookID == id && files[i].FilePath != "" && !files[i].Missing {
				total++
			}
		}
		if total == len(uaFiles) {
			uaBooks = append(uaBooks, id)
		}
	}

	slog.Info("purge-ua-duplicates: census",
		"ua_books", len(uaBooks), "twin_candidate_files", len(files)-len(uaFilesByBook), "dry_run", dryRun)

	var verified, purged, skippedNoTwin, skippedMismatch, errCount int64
	reporter.SetTotal(len(uaBooks))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(runtime.NumCPU(), 1))
	var mu sync.Mutex
	samples := []string{}
	for _, id := range uaBooks {
		g.Go(func() error {
			if gctx.Err() != nil {
				return gctx.Err()
			}
			defer reporter.Increment()
			ok, reason := uaBookFullyTwinned(uaFilesByBook[id], twinBySize)
			switch {
			case ok:
				atomic.AddInt64(&verified, 1)
				mu.Lock()
				if len(samples) < 20 {
					samples = append(samples, liveBook[id].FilePath)
				}
				mu.Unlock()
				if dryRun {
					return nil
				}
				full, herr := store.GetBookByID(id)
				if herr != nil || full == nil {
					atomic.AddInt64(&errCount, 1)
					return nil
				}
				t := true
				now := time.Now()
				full.MarkedForDeletion = &t
				full.MarkedForDeletionAt = &now
				if _, uerr := store.UpdateBook(full.ID, full); uerr != nil {
					slog.Warn("purge-ua-duplicates: soft-delete failed", "book", id, "err", uerr)
					atomic.AddInt64(&errCount, 1)
					return nil
				}
				atomic.AddInt64(&purged, 1)
			case reason == "no-twin":
				atomic.AddInt64(&skippedNoTwin, 1)
			default:
				atomic.AddInt64(&skippedMismatch, 1)
			}
			return nil
		})
	}
	if werr := g.Wait(); werr != nil {
		return werr
	}

	for _, s := range samples {
		slog.Info("purge-ua-duplicates: verified duplicate", "path", s, "dry_run", dryRun)
	}
	slog.Info("purge-ua-duplicates: complete",
		"ua_books", len(uaBooks), "verified_duplicates", verified, "purged", purged,
		"kept_no_twin", skippedNoTwin, "kept_content_differs", skippedMismatch,
		"errors", errCount, "dry_run", dryRun)
	if errCount > 0 {
		return fmt.Errorf("purge-ua-duplicates: %d books failed", errCount)
	}
	return nil
}

// uaBookFullyTwinned reports whether EVERY file has an interior-verified twin.
// reason is "no-twin" when some file had no size candidate at all, "mismatch"
// when candidates existed but none matched content.
func uaBookFullyTwinned(uaFiles []database.BookFileCore, twinBySize map[int64][]string) (bool, string) {
	for _, f := range uaFiles {
		cands := twinBySize[f.FileSize]
		if len(cands) == 0 {
			return false, "no-twin"
		}
		matched := false
		for _, cand := range cands {
			same, err := interiorContentEqual(f.FilePath, cand, f.FileSize)
			if err == nil && same {
				matched = true
				break
			}
		}
		if !matched {
			return false, "mismatch"
		}
	}
	return true, ""
}

// interiorContentEqual compares two files at 25/50/75% offsets (uaProbeSize
// bytes each). Head/tail are deliberately NOT compared: tag blocks live there
// and differ between a correctly-tagged twin and an Unknown Author copy even
// when the audio is byte-identical (audit finding).
func interiorContentEqual(a, b string, size int64) (bool, error) {
	fa, err := os.Open(a)
	if err != nil {
		return false, err
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false, err
	}
	defer fb.Close()

	// The twin must actually be size-identical on disk right now — the index
	// was built from DB rows, which can be stale.
	sa, err := fa.Stat()
	if err != nil {
		return false, err
	}
	sb, err := fb.Stat()
	if err != nil {
		return false, err
	}
	if sa.Size() != sb.Size() || sa.Size() != size {
		return false, nil
	}

	bufA := make([]byte, uaProbeSize)
	bufB := make([]byte, uaProbeSize)
	for _, frac := range []int64{25, 50, 75} {
		off := size * frac / 100
		if off+uaProbeSize > size {
			off = max64(0, size-uaProbeSize)
		}
		na, err := fa.ReadAt(bufA, off)
		if err != nil && err != io.EOF {
			return false, err
		}
		nb, err := fb.ReadAt(bufB, off)
		if err != nil && err != io.EOF {
			return false, err
		}
		if na != nb || !bytes.Equal(bufA[:na], bufB[:nb]) {
			return false, nil
		}
	}
	return true, nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// Policy declares the bridge's existing behaviour verbatim: see DefaultPolicy.
func (j *purgeUADuplicatesJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}
