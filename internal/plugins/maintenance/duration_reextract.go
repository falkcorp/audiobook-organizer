// file: internal/plugins/maintenance/duration_reextract.go
// version: 3.1.0
// guid: 9c2f7a14-6d83-4e51-b0a9-2f5c8e1d4b67
// last-edited: 2026-06-21

// Package maintenance — op maintenance.duration-reextract.
//
// PR #1555 fixed internal/mediainfo to read the TRUE audio-stream duration via
// ffprobe instead of the old fileSize÷assumed-bitrate estimate. The old estimator
// assumed 128 kbps for m4b/m4a, so those durations were routinely ~2× too short.
// Every Book row imported before that fix still carries the wrong duration, which
// poisons dedup duration-matching (checkDurationMatch) and metadata scoring.
//
// This op re-derives the real duration and corrects durations when the new value
// differs meaningfully from the stored one (see "Source priority (v3)" below for
// where the real value comes from). It NEVER overwrites a stored value with an
// ffprobe-fallback ESTIMATE (DurationEstimated==true) and skips books with any
// unreadable segment. Dry-run by default: previews counts; set dryRun=false to
// apply.
//
// Scope (v2): handles BOTH book layouts.
//   - Multi-file books (audio across BookFiles; Book.FilePath may be a directory):
//     re-extract EVERY segment, correct each BookFile.Duration that drifted, then
//     RecomputeBookAggregates to sum the corrected segments into Book.Duration.
//     Segment writes use UpdateBookFile on pebble-direct rows, so the AcoustID
//     fingerprint is preserved (PR #1552) and memdb is refreshed (PR #1560).
//   - Virtual single-file books (no BookFile rows): probe Book.FilePath and write
//     Book.Duration directly.
// A book is corrected only when ALL present segments yield a real (non-estimated)
// duration — a single missing/unreadable/estimated segment makes the total
// untrustworthy, so the whole book is skipped (counted) rather than half-written.
// Book.Duration is what dedup's checkDurationMatch consumes.
//
// Source priority (v3): fingerprinting already measured and stored the real
// decode duration in BookFile.AcoustIDFingerprintDurationSec for ~275K files. v3
// reads that stored value FIRST and treats it as authoritative — no stat, no
// ffprobe — so the fingerprinted majority of the backlog is a fast pure-DB pass.
// ffprobe is the fallback only for never-fingerprinted segments (and virtual
// single-file books, which have no BookFile row to carry the value). The summary
// reports from-fingerprint vs from-ffprobe so a dry-run reveals the fast/slow
// split. The FingerprintFailedAt tombstone does NOT gate the ffprobe fallback:
// ffprobe can still read a container header even when full-decode fingerprinting
// failed, and the worst case is simply skipping the book.
//
// Idempotent: a re-run finds already-corrected rows within tolerance and skips
// them. The ffprobe tail is slow by design — it shells out once per
// non-fingerprinted segment — so the op heartbeats progress every ~15s and is
// cancellable.

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

type durationReextractParams struct {
	DryRun bool `json:"dryRun"`
	// Limit caps the number of books examined in one run (0 = no cap). Useful for
	// a bounded first pass over a large library.
	Limit int `json:"limit"`
}

// durationChangeThresholds: a book is corrected only when the freshly extracted
// real duration differs from the stored value by more than BOTH a relative and an
// absolute floor, so we never churn rows over sub-second rounding noise.
const (
	durationRelTolerance  = 0.02 // 2%
	durationAbsToleranceS = 5    // seconds
)

// durationDiffMeaningful reports whether newDur differs from oldDur by enough to
// warrant a write: >2% AND >5s. Both floors must be exceeded.
func durationDiffMeaningful(oldDur, newDur int) bool {
	if oldDur <= 0 {
		return newDur > 0 // no usable stored value — any real value is an improvement
	}
	delta := int(math.Abs(float64(newDur - oldDur)))
	if delta <= durationAbsToleranceS {
		return false
	}
	rel := float64(delta) / float64(oldDur)
	return rel > durationRelTolerance
}

// extractTimeout is the per-file wall-clock cap for mediainfo.Extract. The call
// chain (os.Open → tag.ReadFrom → ffprobe) contains several blocking syscalls
// that do not respect Go context cancellation and can hang indefinitely on a
// slow or unresponsive filesystem. The goroutine is intentionally leaked on
// timeout — it will unblock whenever the kernel recovers the I/O.
const extractTimeout = 30 * time.Second

// extractWithTimeout runs mediainfo.Extract in a goroutine and returns an error
// if it does not complete within extractTimeout. It also respects ctx so the op
// can be cancelled between files.
func extractWithTimeout(ctx context.Context, filePath string) (*mediainfo.MediaInfo, error) {
	type result struct {
		info *mediainfo.MediaInfo
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		info, err := mediainfo.Extract(filePath)
		ch <- result{info, err}
	}()
	timer := time.NewTimer(extractTimeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.info, r.err
	case <-timer.C:
		return nil, fmt.Errorf("extract timed out after %v", extractTimeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *Plugin) durationReextractDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.duration-reextract",
		Plugin:      "maintenance",
		DisplayName: "Re-extract real book durations (fingerprint-first)",
		Description: "Reads the real per-file duration already stored from fingerprinting (AcoustIDFingerprintDurationSec) first — a fast DB pass — " +
			"and falls back to ffprobe only for never-fingerprinted files. Handles both multi-file and virtual single-file books. " +
			"Corrects Book.Duration where the old fileSize÷bitrate estimate was wrong (PR #1555; m4b/m4a were ~2× too short). " +
			"Never overwrites a real duration with an estimate, and skips books with any unreadable segment. " +
			"Default dry-run previews counts (incl. fingerprint vs ffprobe split); set dryRun=false to apply.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.duration-reextract",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         120 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runDurationReextract,
	}
}

func (p *Plugin) runDurationReextract(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params := durationReextractParams{DryRun: true} // safe default
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}

	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	if params.DryRun {
		_ = reporter.Log(slog.LevelInfo, "DRY RUN — no changes will be written")
	}

	totalBooks, countErr := store.CountBooks()
	if countErr != nil || totalBooks <= 0 {
		totalBooks = 0
	}
	_ = reporter.UpdateProgress(0, totalBooks, "Correcting real durations (fingerprint-first, ffprobe fallback)…")

	const (
		pageSize    = 500
		logInterval = 15 * time.Second
		exampleCap  = 5
	)
	var (
		examined      int // books inspected
		eligible      int // books for which every segment yielded a real duration (fingerprint or ffprobe)
		wouldChange   int // books whose real duration differs meaningfully
		roughlyDouble int // subset where new ≈ 2× old (the m4b/m4a estimate bug signature)
		missing       int // file path set but not on disk
		estimated     int // ffprobe failed → estimate returned; never trusted/written
		readErr       int // mediainfo.Extract returned an error
		noPath        int // book has no single-file FilePath (multi-file; out of v1 scope)
		fpBooks       int // eligible books fully sourced from stored fingerprint durations (fast path)
		ffprobeBooks  int // eligible books that needed ≥1 ffprobe call (slow tail)
		written       int // actual UpdateBook calls (apply mode)
		examples      = make([]string, 0, exampleCap)
		lastLog       = time.Now()
		offset        int
	)

	heartbeat := func(force bool) {
		if !force && time.Since(lastLog) < logInterval {
			return
		}
		total := totalBooks
		if total == 0 {
			total = examined
		}
		_ = reporter.UpdateProgress(examined, total, fmt.Sprintf(
			"examined=%d eligible=%d (fp=%d ffprobe=%d) would-change=%d (~2x=%d) missing=%d est-skip=%d read-err=%d",
			examined, eligible, fpBooks, ffprobeBooks, wouldChange, roughlyDouble, missing, estimated, readErr))
		lastLog = time.Now()
	}

	for {
		// Fire a progress update BEFORE GetAllBooks so the stuck-op detector
		// (5-minute no-progress threshold) does not cancel us during a slow
		// PebbleDB compaction or memdb-lock contention between pages.
		heartbeat(true)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		books, err := store.GetAllBooks(pageSize, offset)
		if err != nil {
			return fmt.Errorf("GetAllBooks offset=%d: %w", offset, err)
		}
		if len(books) == 0 {
			break
		}

		for i := range books {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if params.Limit > 0 && examined >= params.Limit {
				break
			}
			book := books[i]
			examined++

			// Compute the book's REAL duration. Multi-file books store audio across
			// BookFiles (Book.FilePath may even be a directory); virtual single-file
			// books carry it on Book.FilePath. We require EVERY present segment to
			// yield a real (non-estimated) ffprobe duration, else the book total
			// can't be trusted and we skip it.
			segs, _ := store.GetBookFiles(book.ID)

			var (
				newDur      int
				changedBFs  []database.BookFile
				skip        bool
				usedFfprobe bool // this book needed ≥1 ffprobe call (slow tail)
			)

			if len(segs) > 0 {
				for si := range segs {
					f := segs[si]
					if f.FilePath == "" {
						continue // pathless segment contributes nothing; tolerate
					}
					// v3 fast path: fingerprinting already measured and stored the real
					// decode duration. Trust it as authoritative — no stat, no ffprobe.
					// Only segments never fingerprinted fall through to the slow ffprobe
					// path below.
					var segDur int
					if f.AcoustIDFingerprintDurationSec > 0 {
						segDur = int(math.Round(f.AcoustIDFingerprintDurationSec))
					} else {
						usedFfprobe = true
						info, mErr := extractWithTimeout(ctx, f.FilePath)
						if mErr != nil || info == nil || info.Duration <= 0 {
							readErr++
							skip = true
							break
						}
						if info.DurationEstimated {
							estimated++
							skip = true
							break
						}
						segDur = info.Duration
					}
					newDur += segDur
					if durationDiffMeaningful(f.Duration, segDur) {
						nf := f
						nf.Duration = segDur
						changedBFs = append(changedBFs, nf)
					}
				}
			} else {
				// Virtual single-file book: no BookFile rows means no stored fingerprint
				// duration, so always probe Book.FilePath directly.
				usedFfprobe = true
				if book.FilePath == "" {
					noPath++
					continue
				}
				info, mErr := extractWithTimeout(ctx, book.FilePath)
				if mErr != nil || info == nil || info.Duration <= 0 {
					readErr++
					continue
				}
				if info.DurationEstimated {
					estimated++
					continue
				}
				newDur = info.Duration
			}
			if skip || newDur <= 0 {
				continue
			}
			eligible++
			if usedFfprobe {
				ffprobeBooks++
			} else {
				fpBooks++
			}

			oldDur := 0
			if book.Duration != nil {
				oldDur = *book.Duration
			}
			if !durationDiffMeaningful(oldDur, newDur) && len(changedBFs) == 0 {
				continue // book total + every segment already correct — idempotent skip
			}
			wouldChange++
			if oldDur > 0 {
				ratio := float64(newDur) / float64(oldDur)
				if ratio >= 1.8 && ratio <= 2.2 {
					roughlyDouble++
				}
			}
			if len(examples) < exampleCap {
				examples = append(examples, fmt.Sprintf("%s %ds→%ds (%d seg)", book.ID, oldDur, newDur, len(segs)))
			}

			if !params.DryRun {
				// Correct each changed segment first. UpdateBookFile is full-replace,
				// but segs came from pebble-direct GetBookFiles so every field incl.
				// the fingerprint is intact; it also refreshes memdb.
				for ci := range changedBFs {
					cf := changedBFs[ci]
					if uErr := store.UpdateBookFile(cf.ID, &cf); uErr != nil {
						_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
							"book %s seg %s: UpdateBookFile failed: %v", book.ID, cf.ID, uErr))
						readErr++
					}
				}
				if len(segs) > 0 {
					// Canonical aggregator: sums the (now-corrected) segment durations
					// into Book.Duration.
					if rErr := store.RecomputeBookAggregates(book.ID); rErr != nil {
						_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
							"book %s: RecomputeBookAggregates failed: %v", book.ID, rErr))
						readErr++
						continue
					}
				} else {
					full, gErr := store.GetBookByID(book.ID)
					if gErr != nil || full == nil {
						_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
							"book %s: GetBookByID failed: %v", book.ID, gErr))
						readErr++
						continue
					}
					nd := newDur
					full.Duration = &nd
					if _, uErr := store.UpdateBook(book.ID, full); uErr != nil {
						_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
							"book %s: UpdateBook failed: %v", book.ID, uErr))
						readErr++
						continue
					}
				}
				written++
			}

			heartbeat(false)
		}

		offset += len(books)
		if params.Limit > 0 && examined >= params.Limit {
			break
		}
		if len(books) < pageSize {
			break
		}
		heartbeat(false)
	}

	verb := "would correct"
	if !params.DryRun {
		verb = fmt.Sprintf("corrected %d;", written)
	}
	summary := fmt.Sprintf(
		"examined=%d eligible=%d (from-fingerprint=%d from-ffprobe=%d) %s would-change=%d (~2x=%d) missing-on-disk=%d estimated-skipped=%d read-errors=%d no-filepath=%d | e.g. %s",
		examined, eligible, fpBooks, ffprobeBooks, verb, wouldChange, roughlyDouble, missing, estimated, readErr, noPath,
		strings.Join(examples, ", "))
	_ = reporter.Log(slog.LevelInfo, summary)
	total := totalBooks
	if total == 0 {
		total = examined
	}
	_ = reporter.UpdateProgress(total, total, summary)
	return nil
}
