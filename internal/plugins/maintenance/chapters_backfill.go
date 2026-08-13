// file: internal/plugins/maintenance/chapters_backfill.go
// version: 1.2.0
// guid: 5d3b7e14-9c62-4a8f-b0d7-2e6194af8c35
// last-edited: 2026-08-13

// Package maintenance — op maintenance.chapters-backfill.
//
// 🔴 THE PROBLEM THIS EXISTS TO CLOSE. Chapter extraction is a WRITE-PATH-ONLY
// feature. database.SaveChaptersForBook has exactly one non-test caller —
// scanner.PersistChaptersForBook (internal/scanner/process_file.go:259) — and
// that is reached only from the saveBook success branch (scanner.go:851,
// scanner.go:1035). The feature landed 2026-07-30; the library predates it, and
// an incremental scan skips unchanged files via the scan cache. So no book that
// already existed has ever been probed, and none ever will be.
//
// The failure is SILENT because the read path has a fallback. When no chapters
// are persisted, handlers/abs/mapper.go:243 calls audioutil.SynthesizeChapters
// over the track list. For a multi-file book that is one chapter per file, which
// looks plausible. For a SINGLE-FILE book it is ONE CHAPTER SPANNING THE WHOLE
// CONTAINER — a 24-hour audiobook served as one unnavigable block, titled with
// the book's own name. A missing chapter list would have rendered blank and been
// reported; a plausible wrong one was not.
//
// Measured on production 2026-08-13, before this op existed:
//
//	500 ABS items sampled  → numChapters == numAudioFiles in 500/500
//	213 of them single-file containers over 10,000s → ALL reported exactly 1 chapter
//	40 random .m4b/.m4a on disk → 19 carry real embedded markers (16–118 each)
//	14 days of logs → 0 PersistChaptersForBook warnings (it is not failing; it never runs)
//
// SINGLE-FILE ONLY, DELIBERATELY. Multi-file books are out of scope and skipped.
// Their persisted form (one chapter per file, synthesized from BookFile
// durations) is byte-identical to what the mapper already computes at request
// time, so persisting it changes no API response — while introducing a staleness
// hazard the read path cannot detect: len(stored) > 0 short-circuits the live
// synthesis, so a persisted list would silently win over the correct one after a
// book gains or loses a file. There is no invalidation hook for that today. The
// bug is entirely in the single-file population, and that is what this op fixes.
//
// 🔴 "PROBED, FOUND NONE" IS NOT REPRESENTABLE, SO THIS OP RE-PROBES.
// SaveChaptersForBook deletes the key on an empty slice
// (pebble_store_chapters.go:63), so a book with no embedded markers is
// byte-identical to a book that was never examined. Every run therefore
// re-ffprobes that population — roughly half of single-file containers, on the
// 40-file sample. That is accepted rather than fixed here: the only durable
// marker available is internal/operations/freshness, which today has ZERO
// non-test callers and would need a new ServerDeps accessor plus server wiring
// to reach an op. That plumbing is a wider change than this op's benefit
// justifies while the op is MANUAL-TRIGGER ONLY. `-show_chapters` reads the
// container header, so a re-probe is cheap. If this op is ever given a Schedule,
// wire the freshness stamp FIRST — a nightly re-probe of every markerless
// container is a different cost profile than an occasional manual run.
//
// DATA-LOSS SAFETY. There is no UpdateBook call in this file, and that is
// structural rather than a promise: this repo's dominant incident class is the
// write-back wipe, where a bare whole-row UpdateBook clears AcoustIDFingerprint
// or Author. The only write here is SaveChaptersForBook, which touches one
// dedicated Pebble keyspace (chapters:<bookID>) and no book row. Reverting a
// book is DeleteChaptersForBook, which restores exactly the state production is
// in today.
//
// DRY RUN BY DEFAULT, matching probe_directory_books.go: nothing is written
// unless the caller passes {"apply": true}.
package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/audioutil"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// chaptersBackfillParams configures the op.
type chaptersBackfillParams struct {
	// Apply gates every write. False (the default) makes this a pure reporter
	// that measures what WOULD be persisted.
	Apply bool `json:"apply"`
	// Limit caps how many books are WRITTEN this run (0 = no cap). Probing
	// always covers the whole library, mirroring probe-directory-books: a
	// capped run must never look like a clean bill of health, so the cap
	// applies to the writes and never to the measurement.
	Limit int `json:"limit"`
	// BookIDs restricts the run to an explicit set. Used to exercise the op
	// against a bounded cohort before a whole-library pass.
	BookIDs []string `json:"bookIds,omitempty"`
}

// chapterProbeTimeout bounds one ffprobe invocation. Mirrors
// probe_directory_books.go's probeFileTimeout: ffprobe reads only the container
// header, so this exists for a wedged mount, not a slow file.
const chaptersBackfillProbeTimeout = 20 * time.Second

// minPersistableChapters is the threshold below which a probe result is NOT
// written.
//
// One chapter is exactly what the mapper already synthesizes for a single-file
// book, so persisting it changes no response — it only converts "we never
// looked" into "we looked and found nothing", which the store cannot represent
// anyway (an empty save is a delete). Writing it would fabricate a row that is
// indistinguishable from a measured one. Two is the smallest count that makes a
// book navigable.
const minPersistableChapters = 2

// probeChaptersFn is the injection seam for tests, wired to the real
// implementation in production. Tests that swap it must not run in parallel and
// must restore via t.Cleanup — it is process-global. Mirrors the seam
// probe_directory_books.go establishes for ProbeDurationSeconds.
var probeChaptersFn = audioutil.ProbeChapters

// chapterPersister is the narrow slice of the store this op needs beyond the
// Store interface. Declared as an assertion target rather than requiring
// *database.PebbleStore so a future backend that implements the same two
// methods works unchanged — and so a backend that does NOT gets a clear refusal
// instead of a nil-pointer panic.
//
// 🔴 RESOLVE IT WITH database.AsCapability, NEVER A BARE ASSERTION. deps.Store()
// returns Server.store, which is REPLACED by the *server.indexedStore decorator
// at server_lifecycle.go:290 once the search index opens. indexedStore embeds
// database.Store, so only methods on THAT interface are promoted — and the
// chapter methods are not on it. A bare `store.(chapterPersister)` therefore
// fails in production while passing every test that hands the op a raw
// *PebbleStore.
//
// That is not hypothetical here: this op shipped with the bare assertion and its
// first production run refused with "store is *server.indexedStore, which does
// not persist chapters". It is also the third recorded instance of this exact
// bug — see AsPebbleStore's doc comment for the two prod jobs that were silently
// degraded for weeks by the same decorator. Those degraded SILENTLY because they
// had non-Pebble fallbacks; this one refused loudly, which is the only reason it
// was caught in one run.
type chapterPersister interface {
	GetChaptersForBook(bookID string) ([]database.Chapter, error)
	SaveChaptersForBook(bookID string, chapters []database.Chapter) error
}

func (p *Plugin) chaptersBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.chapters-backfill",
		Plugin:      "maintenance",
		DisplayName: "Backfill embedded chapters (single-file books)",
		Description: "Extracts the embedded chapter timeline from single-file audiobook containers that have none " +
			"persisted, so the ABS API stops falling back to a one-chapter-spans-the-whole-book synthesis. Chapter " +
			"extraction only ever ran on the scanner's new-book save path, so every book predating 2026-07-30 was " +
			"never probed. Multi-file books are skipped deliberately: their persisted form is identical to the " +
			"live synthesis and would go stale undetectably. Refuses to run when ffprobe is unavailable. " +
			"DRY RUN unless {\"apply\": true}.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		// Shares the ffprobe lane with the other whole-library probe op so the
		// two cannot saturate every core at once.
		ConcurrencyKey: "maintenance.probe-directory-books",
		Cancellable:    true,
		Isolate:        false,
		Timeout:        24 * time.Hour,
		// No Writes resources declared: this op writes only the chapters:
		// keyspace, never a Book or BookFile row, so the dispatcher's write-set
		// gate has nothing to serialize it against. Declaring ResBooks here
		// would misdescribe it and needlessly block it behind every book writer.
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
			sdk.CapFilesRead,
			sdk.CapFilesExecute,
			sdk.CapSubprocessSpawn,
		},
		Run: p.runChaptersBackfill,
	}
}

// chaptersBackfillCounters is the run tally. Every field is touched from
// multiple RunItems workers, so all of them are atomic rather than
// mutex-guarded — there is no invariant spanning two counters that would need
// them to move together.
type chaptersBackfillCounters struct {
	examined       atomic.Int64
	skipMultiFile  atomic.Int64
	skipHasStored  atomic.Int64
	skipNoPath     atomic.Int64
	probeFailed    atomic.Int64
	noChapters     atomic.Int64
	wouldPersist   atomic.Int64
	persisted      atomic.Int64
	persistFailed  atomic.Int64
	chaptersWorked atomic.Int64
}

func (c *chaptersBackfillCounters) summary(apply bool) string {
	verb := "would persist"
	n := c.wouldPersist.Load()
	if apply {
		verb = "persisted"
		n = c.persisted.Load()
	}
	return fmt.Sprintf(
		"examined=%d %s=%d (%d chapters) | skipped: multi-file=%d already-had=%d no-path=%d | "+
			"no-markers=%d probe-failed=%d persist-failed=%d",
		c.examined.Load(), verb, n, c.chaptersWorked.Load(),
		c.skipMultiFile.Load(), c.skipHasStored.Load(), c.skipNoPath.Load(),
		c.noChapters.Load(), c.probeFailed.Load(), c.persistFailed.Load(),
	)
}

func (p *Plugin) runChaptersBackfill(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params := chaptersBackfillParams{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}

	// ── refuse to run without ffprobe ────────────────────────────────────────
	//
	// First, before anything else. Every probe would fail and the op would
	// report the same "no markers found" summary it reports against a library
	// that genuinely has none. Those two states must not look alike.
	ffprobePath, err := lookupFFprobeFn()
	if err != nil {
		return fmt.Errorf("cannot run: this op extracts real chapter markers and has no fallback — %w", err)
	}
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("using ffprobe at %s", ffprobePath))

	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	persister, ok := database.AsCapability[chapterPersister](store)
	if !ok {
		return fmt.Errorf("cannot run: store is %T, which does not persist chapters "+
			"(and no store in its decorator chain does)", store)
	}
	if !params.Apply {
		_ = reporter.Log(slog.LevelInfo, "DRY RUN — no chapters will be written")
	}

	ids := params.BookIDs
	if len(ids) == 0 {
		_ = reporter.UpdateProgress(0, 0, "Enumerating library…")
		ids, err = store.ListBookIDs()
		if err != nil {
			return fmt.Errorf("ListBookIDs: %w", err)
		}
	} else {
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("restricted to %d explicit book IDs", len(ids)))
	}

	var c chaptersBackfillCounters

	// CONCURRENCY (CLAUDE.md mandate): a whole-library loop doing one subprocess
	// call per item is exactly the shape that hung dedup.full-scan for 3+ hours
	// on a single core. Every worker touches a disjoint book, and the only
	// shared state is the atomic counter block, so the pool needs no lock.
	runErr := registry.RunItems(ctx, reporter, ids, func(ctx context.Context, id string) error {
		c.examined.Add(1)

		// Already extracted — idempotent, the same rule the scanner applies.
		// Checked BEFORE the file read so a re-run over a mostly-done library
		// costs one Pebble get per book and nothing more.
		if stored, err := persister.GetChaptersForBook(id); err == nil && len(stored) > 0 {
			c.skipHasStored.Add(1)
			return nil
		}

		files, ferr := store.GetBookFiles(id)
		if ferr != nil {
			c.skipNoPath.Add(1)
			return nil // a book that vanished mid-run is not this op's problem
		}
		if len(files) > 1 {
			c.skipMultiFile.Add(1)
			return nil
		}

		path := ""
		if len(files) == 1 {
			path = strings.TrimSpace(files[0].FilePath)
		}
		if path == "" {
			b, berr := store.GetBookByID(id)
			if berr != nil || b == nil {
				c.skipNoPath.Add(1)
				return nil
			}
			path = strings.TrimSpace(b.FilePath)
		}
		if path == "" {
			c.skipNoPath.Add(1)
			return nil
		}

		probeCtx, cancel := context.WithTimeout(ctx, chaptersBackfillProbeTimeout)
		defer cancel()
		chs, perr := probeChaptersFn(probeCtx, ffprobePath, path)
		if perr != nil {
			// Swallowed per file, never fatal to the run — but COUNTED, so a
			// run where every probe failed cannot be mistaken for a library
			// with no markers.
			c.probeFailed.Add(1)
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("ProbeChapters(%s): %v", path, perr))
			return nil
		}
		if len(chs) < minPersistableChapters {
			c.noChapters.Add(1)
			return nil
		}

		c.chaptersWorked.Add(int64(len(chs)))
		if !params.Apply {
			c.wouldPersist.Add(1)
			return nil
		}
		if params.Limit > 0 && c.persisted.Load() >= int64(params.Limit) {
			c.wouldPersist.Add(1)
			return nil
		}

		dbChapters := make([]database.Chapter, len(chs))
		for i, ch := range chs {
			dbChapters[i] = database.Chapter{
				ID: ch.ID, StartSec: ch.StartSec, EndSec: ch.EndSec, Title: ch.Title,
			}
		}
		if serr := persister.SaveChaptersForBook(id, dbChapters); serr != nil {
			c.persistFailed.Add(1)
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("SaveChaptersForBook(%s): %v", id, serr))
			return nil
		}
		c.persisted.Add(1)
		return nil
	}, registry.RunItemsOptions{
		Concurrency:   runtime.NumCPU(),
		ProgressTotal: len(ids),
		ErrMode:       registry.ErrModeCollect,
		// Reports ELIGIBLE (= persisted + wouldPersist), not persisted. A dry
		// run never increments persisted, so a label reading it sat at zero for
		// the whole run and was indistinguishable from a run finding nothing —
		// the opposite of the truth on the first cohort pass, which found 33.
		Label: func(i, total int) string {
			return fmt.Sprintf("Books %d/%d (eligible=%d chapters=%d)",
				i+1, total, c.persisted.Load()+c.wouldPersist.Load(), c.chaptersWorked.Load())
		},
	})

	summary := c.summary(params.Apply)
	if params.Limit > 0 && c.wouldPersist.Load() > 0 && params.Apply {
		summary += fmt.Sprintf(" | ⚠️ write cap %d reached — %d eligible books left unwritten",
			params.Limit, c.wouldPersist.Load())
	}
	_ = reporter.Log(slog.LevelInfo, summary)
	_ = reporter.UpdateProgress(len(ids), len(ids), summary)
	return runErr
}
