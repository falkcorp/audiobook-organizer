// file: internal/metafetch/apply_timings.go
// version: 1.1.0
// guid: 8d4c2a61-9f3e-4b07-a5d8-1e6b7c0f29a3
// last-edited: 2026-09-14

package metafetch

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
	"golang.org/x/sync/errgroup"
)

// Phase names reported in the per-book "apply phase durations" line. The
// order here is the order they are printed in.
const (
	PhaseGateWait = "gate_wait" // waiting for the process-wide write-back gate (server)
	PhaseApplyDB  = "apply_db"  // ApplyMetadataCandidate: DB apply, MATCH-4 dup check, provenance
	PhaseLockWait = "lock_wait" // waiting for book / path locks
	PhaseCover    = "cover"     // cover download (network) + cover_url update
	PhaseCopyLock = "copy_lock" // library-copy lookup/creation under the version-group lock
	PhaseEmbed    = "embed"     // cover embed into the audio files
	PhaseRename   = "rename"    // rename planning, moves, and their book_file row updates
	PhaseTagPrep  = "tag_prep"  // author/narrator resolution, file listing before tag writes
	PhaseTags     = "tags"      // per-file tag reads + safe writes
	PhaseDBPost   = "db_post"   // history, last_written_at, needs_rescan, sibling tags, checkpoints
)

var phaseOrder = []string{
	PhaseGateWait, PhaseApplyDB, PhaseLockWait, PhaseCover, PhaseCopyLock,
	PhaseEmbed, PhaseRename, PhaseTagPrep, PhaseTags, PhaseDBPost,
}

// ApplyPhaseTimings accumulates how long one book's apply spent in each phase,
// so production logs say where the time went. Before 2026-09-13 a one-book
// apply took 87s with two ~35s stretches that logged nothing at all.
//
// A nil *ApplyPhaseTimings is valid and records nothing. Safe for concurrent
// use.
type ApplyPhaseTimings struct {
	start time.Time
	mu    sync.Mutex
	d     map[string]time.Duration
	files atomic.Int64
}

// NewApplyPhaseTimings starts a timer; its total runs from now.
func NewApplyPhaseTimings() *ApplyPhaseTimings {
	return &ApplyPhaseTimings{start: time.Now(), d: make(map[string]time.Duration)}
}

// Add adds d to phase.
func (t *ApplyPhaseTimings) Add(phase string, d time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.d[phase] += d
	t.mu.Unlock()
}

// Since adds the time elapsed since start to phase.
func (t *ApplyPhaseTimings) Since(phase string, start time.Time) {
	t.Add(phase, time.Since(start))
}

// AddFiles counts n audio files the tag write considered.
func (t *ApplyPhaseTimings) AddFiles(n int) {
	if t == nil {
		return
	}
	t.files.Add(int64(n))
}

// Get returns the time recorded for phase.
func (t *ApplyPhaseTimings) Get(phase string) time.Duration {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.d[phase]
}

// String renders "gate_wait=0ms apply_db=812ms ... total=4210ms files=58".
//
// Only phases that were actually MEASURED appear. A phase this timer never
// saw is left out rather than printed as 0ms: the single-book apply handler
// runs its DB apply before the file work starts its timer, and a line saying
// "apply_db=0ms" there would claim a measurement that was never made.
func (t *ApplyPhaseTimings) String() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var b strings.Builder
	for _, p := range phaseOrder {
		if d, ok := t.d[p]; ok {
			fmt.Fprintf(&b, "%s=%dms ", p, d.Milliseconds())
		}
	}
	fmt.Fprintf(&b, "total=%dms files=%d", time.Since(t.start).Milliseconds(), t.files.Load())
	return b.String()
}

var applyTimingLog = logger.New("metafetch")

// applyTimingSink receives the rendered per-book line. A package variable so a
// test can capture it; production logs it at Info.
var applyTimingSink = func(id, line string) {
	applyTimingLog.Info("apply phase durations book_id=%s %s", logger.SanitizeLogValue(id), line)
}

// logApplyPhaseTimings emits the one per-book phase-duration line.
func logApplyPhaseTimings(id string, t *ApplyPhaseTimings) {
	if t == nil {
		return
	}
	applyTimingSink(id, t.String())
}

// maxFileWritersPerBook caps one book's concurrent per-file tag writers. The
// process-wide write-back gate (8 slots) is the real bound; this only stops
// one book from taking every free slot at once.
const maxFileWritersPerBook = 4

// runFileWrites calls fn(i) exactly once for every i in [0, n), using up to
// maxFileWritersPerBook goroutines.
//
// The FIRST writer always runs, on the caller's goroutine, with no gate slot:
// it is the writer the caller already accounts for (batch apply and bulk
// write-back hold one gate slot per book around this), i.e. exactly the one
// sequential writer that existed before. Every EXTRA writer needs its own
// slot, taken with fileWriteSlot, which never blocks: if no slot is free
// right now the book simply gets fewer writers. So:
//   - concurrent file writers across the process never exceed the gate's
//     limit (plus, as before, the untokened single writer of a caller that
//     holds no slot);
//   - a book can never wait on a slot while holding one, so no deadlock;
//   - with no gate wired (tests, organize's per-call service) it is the old
//     sequential loop.
//
// Workers pull indices from a shared counter, so each file is written by
// exactly one writer, once.
func (mfs *Service) runFileWrites(n int, fn func(i int)) {
	if n <= 0 {
		return
	}
	var next atomic.Int64
	work := func() {
		for {
			i := int(next.Add(1) - 1)
			if i >= n {
				return
			}
			fn(i)
		}
	}
	var g errgroup.Group
	g.SetLimit(maxFileWritersPerBook)
	if mfs.fileWriteSlot != nil {
		for extra := 1; extra < maxFileWritersPerBook && extra < n; extra++ {
			release, ok := mfs.fileWriteSlot()
			if !ok {
				break
			}
			g.Go(func() error {
				defer release()
				work()
				return nil
			})
		}
	}
	work()
	_ = g.Wait() // workers never return an error
}

// writeFileTagsSafe is the one per-file tag write of writeBackForBook: an
// optional backup, then an atomic temp-copy write. bookFileID/store, when set,
// let WriteTagsSafe persist the file's before/after hashes.
//
// The write goes through the same protected-path guard as every other tag
// write (tagger.ResolvePathForWrite with the service's safe-write deps): a
// Deluge-protected file is imported to the library and the copy is written,
// or, when there is no copy to write, the call returns an error wrapping
// tagger.ErrProtectedPathWrite and the file is not touched. Until 2026-09-14
// this path wrote the file it was given, so write-back rewrote files a torrent
// client was seeding whenever mfs.isProtectedPath (import roots and the iTunes
// library only) did not cover them.
func (mfs *Service) writeFileTagsSafe(path string, tagMap map[string]any, opts fileops.WriteTagsSafeOptions, opConfig fileops.OperationConfig) error {
	target, err := tagger.ResolvePathForWrite(context.Background(), path, mfs.safeWriteDeps)
	if err != nil {
		return err
	}
	if target != path && opts.Store != nil && mfs.db != nil {
		// The write went to a library copy: record on the row of the file
		// actually written (tagger.SafeWriteDeps.hashOptions, same rule).
		opts = fileops.HashOptionsForPath(mfs.db, target)
	}
	if mfs.fileTagWrite != nil {
		return mfs.fileTagWrite(target, tagMap)
	}
	backupFileBeforeWrite(target)
	_, _, err = fileops.WriteTagsSafe(target, func(tmpPath string) error {
		return metadata.WriteMetadataToFileInPlace(tmpPath, tagMap, opConfig)
	}, opts)
	return err
}

// bookFileWriteOpts returns the WriteTagsSafe options that persist hashes
// against f's book_file row, or none when f has no row. A lookup error is
// logged at Warn (it was dropped silently) and the write goes ahead without
// recording.
func (mfs *Service) bookFileWriteOpts(f string) fileops.WriteTagsSafeOptions {
	return fileops.HashOptionsForPath(mfs.db, f)
}
