// file: internal/plugins/maintenance/file_intro_reparse.go
// version: 1.0.0
// guid: 7d3a5e91-c2b4-4f08-a6e7-91b0d4c8f325
// last-edited: 2026-11-17

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// reparseFileIntrosID is the op id.
const reparseFileIntrosID = "maintenance.reparse-file-intros"

// reparseChunkSize is how many files one work item (one write batch) names.
// Chunks are disjoint sets of files, so no two workers ever touch the same row.
const reparseChunkSize = 64

// reparseCheckpointEvery is how many completed chunks pass between checkpoints
// (about three thousand files).
const reparseCheckpointEvery = 50

// Classes of stored row a re-parse cannot, or may not fully, act on. They are
// reported by name so the owner can see how many rows each one holds.
const (
	// reparseNoTranscript: the row has intro fields but no transcript text (a
	// failure status written by a run that never heard anything). Nothing to parse.
	reparseNoTranscript = "no_transcript"
	// reparseBlankTranscript: the stored text is only whitespace.
	reparseBlankTranscript = "blank_transcript"
	// reparseNewerParser: the stored parse was written by a NEWER grammar than
	// this binary. It is left alone, so rolling the binary back never rewrites
	// newer results with older ones.
	reparseNewerParser = "stored_parse_from_newer_grammar"
	// reparseFrozenByStatus: the row holds text under a failure status
	// (whisper_error, transcribe_timeout, source_file_missing, ...). The parse is
	// written; identity and status are NOT touched (rule 1 of the no-downgrade
	// rule), because the text was not the product of a successful transcription.
	reparseFrozenByStatus = "identity_frozen_by_failure_status"
	// reparseSilenceOnly: the row holds only the silence sentinel. Its kind is
	// derivable (music_or_silence); there are no fields to extract.
	reparseSilenceOnly = "silence_sentinel_only"
)

// reparseKindUnclassified names, in the before-counts, a row with no stored parse.
const reparseKindUnclassified = "unclassified"

// reparseFileIntrosParams: dry_run omitted means PREVIEW (opmode.ResolveDryRun).
type reparseFileIntrosParams struct {
	opmode.DryRunParams
	// ResumeFrom is the contiguous-completion watermark (in chunks of
	// reparseChunkSize files, over the files sorted by book id then file id) a
	// previous live attempt reached. The registry merges the checkpoint back
	// into the params on a restart, so it arrives populated on a resumed run and
	// is zero on a fresh one. It is an optimisation, not a correctness
	// mechanism: every write is idempotent, so a file below a stale watermark
	// that is examined again changes nothing.
	ResumeFrom int `json:"resume_from,omitempty"`
}

// reparseFileIntrosResult is what the op reports, preview or live. Counts are
// per FILE. In a live run every "after" figure is what was actually stored (a
// write that failed counts as unchanged); in a preview it is what a live run
// would store.
type reparseFileIntrosResult struct {
	DryRun bool `json:"dry_run"`
	// Examined is the number of files with any intro field; Reparsed the number
	// whose stored text was parsed (Examined minus Unreparsable).
	Examined   int `json:"examined"`
	Reparsed   int `json:"reparsed"`
	Unreparsed int `json:"unreparsable"`
	// Unreparsable counts, by class, the rows a re-parse could not act on at all
	// (no_transcript, blank_transcript, stored_parse_from_newer_grammar).
	Unreparsable map[string]int `json:"unreparsable_by_class,omitempty"`
	// Notes counts rows that were parsed but have a caveat (see the reparse*
	// class constants).
	Notes map[string]int `json:"notes,omitempty"`

	// KindBefore and KindAfter are the per-kind counts of the parsed rows; a row
	// with no stored parse is "unclassified" before.
	KindBefore map[string]int `json:"kind_before"`
	KindAfter  map[string]int `json:"kind_after"`

	// ParseWrites is the number of files whose stored parse changed (or was
	// absent). IdentityFilled/IdentityRepaired are identity fields filled from
	// empty / replaced because the stored value was defective; StatusPromoted is
	// unparsed -> ok. KindConflicts counts title-announcement-less clips on rows
	// that already carry an identity: the identity is KEPT, the count says how
	// many stored identities the current grammar would not have produced.
	ParseWrites      int `json:"parse_writes"`
	IdentityWrites   int `json:"identity_writes"`
	IdentityFilled   int `json:"identity_fields_filled"`
	IdentityRepaired int `json:"identity_fields_repaired"`
	StatusPromoted   int `json:"status_promoted_to_ok"`
	KindConflicts    int `json:"kind_conflicts_kept"`

	// Gone counts rows deleted between the listing and the read; WriteErrors the
	// writes that failed (the row is left as it was).
	Gone        int `json:"gone"`
	WriteErrors int `json:"write_errors"`
}

// reparseRow is the pure outcome of re-parsing one stored row.
type reparseRow struct {
	// unreparsable is the class of a row nothing could be done with ("" when
	// the text was parsed).
	unreparsable string
	// note is a caveat of a parsed row ("" for none).
	note string

	before, after string
	parse         transcribe.ClipParse
	dec           transcribe.ReparseDecision

	// write is true when anything differs from what is stored; patch is what to
	// write.
	write bool
	patch database.IntroReparsePatch
}

// introParseFromClip is the stored form of a parse.
func introParseFromClip(p transcribe.ClipParse) database.IntroParse {
	return database.IntroParse{
		Version:          transcribe.ClipParserVersion,
		Kind:             string(p.Kind),
		ChapterNumber:    p.ChapterNumber,
		HasChapterNumber: p.HasChapterNumber,
		ChapterTitle:     p.ChapterTitle,
		PartNumber:       p.PartNumber,
		HasPartNumber:    p.HasPartNumber,
		Marker:           p.Marker,
		Title:            p.Title,
		Author:           p.Author,
		Narrator:         p.Narrator,
		Translator:       p.Translator,
		CoverArtist:      p.CoverArtist,
		Publisher:        p.Publisher,
		CollectionFrom:   p.CollectionFrom,
		CollectionTo:     p.CollectionTo,
	}
}

// silenceParse is the stored parse of the silence sentinel.
func silenceParse() *database.IntroParse {
	p := introParseFromClip(transcribe.ParseClip(transcribe.SilenceSentinel))
	return &p
}

// cloneIntroParse copies a stored parse (nil stays nil).
func cloneIntroParse(p *database.IntroParse) *database.IntroParse {
	if p == nil {
		return nil
	}
	c := *p
	return &c
}

// reparseDecide re-derives one row's parse from its stored text and applies the
// no-downgrade rule (transcribe.Reconcile). It is a pure function of the row, so
// a lost compare-and-swap race simply calls it again on the fresh row.
func reparseDecide(cur database.IntroReparseRow) reparseRow {
	before := reparseKindUnclassified
	if cur.Parse != nil {
		before = cur.Parse.Kind
	}
	out := reparseRow{before: before, after: before}

	if cur.Parse != nil && cur.Parse.Version > transcribe.ClipParserVersion {
		out.unreparsable = reparseNewerParser
		return out
	}
	p := transcribe.ParseClip(cur.Transcript)
	if p.Kind == transcribe.ClipKindNone {
		out.unreparsable = reparseBlankTranscript
		return out
	}
	out.parse = p
	out.after = string(p.Kind)

	stored := transcribe.StoredIntro{Status: cur.Status, Identity: transcribe.Identity{
		Title: cur.Identity.Title, Author: cur.Identity.Author, Narrator: cur.Identity.Narrator,
		Translator: cur.Identity.Translator, CoverArtist: cur.Identity.CoverArtist,
	}}
	out.dec = transcribe.Reconcile(stored, p)

	switch {
	case cur.Status != transcribe.StatusOK && cur.Status != transcribe.StatusUnparsed && cur.Status != database.IntroStatusEmpty:
		out.note = reparseFrozenByStatus
	case p.Kind == transcribe.ClipKindSilence && cur.Transcript == transcribe.SilenceSentinel:
		out.note = reparseSilenceOnly
	}

	np := introParseFromClip(p)
	out.write = cur.Parse == nil || *cur.Parse != np || out.dec.IdentityWrite || out.dec.StatusWrite
	if out.write {
		out.patch.Parse = &np
		if out.dec.IdentityWrite {
			out.patch.Identity = &database.IntroReparseIdentity{
				Title: out.dec.Identity.Title, Author: out.dec.Identity.Author, Narrator: out.dec.Identity.Narrator,
				Translator: out.dec.Identity.Translator, CoverArtist: out.dec.Identity.CoverArtist,
			}
		}
		if out.dec.StatusWrite {
			out.patch.Status = out.dec.Status
		}
	}
	return out
}

// reparseTally is the run's counters. The atomics are what the progress label
// reads (it runs inside the worker goroutines); the maps and the rest are folded
// in under mu once per chunk.
type reparseTally struct {
	examined atomic.Int64
	changed  atomic.Int64

	mu  sync.Mutex
	res reparseFileIntrosResult
}

func newReparseTally(dryRun bool) *reparseTally {
	return &reparseTally{res: reparseFileIntrosResult{
		DryRun:       dryRun,
		Unreparsable: map[string]int{},
		Notes:        map[string]int{},
		KindBefore:   map[string]int{},
		KindAfter:    map[string]int{},
	}}
}

// add folds one chunk's local tally in.
func (t *reparseTally) add(l *reparseFileIntrosResult) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r := &t.res
	r.Examined += l.Examined
	r.Reparsed += l.Reparsed
	r.Unreparsed += l.Unreparsed
	r.ParseWrites += l.ParseWrites
	r.IdentityWrites += l.IdentityWrites
	r.IdentityFilled += l.IdentityFilled
	r.IdentityRepaired += l.IdentityRepaired
	r.StatusPromoted += l.StatusPromoted
	r.KindConflicts += l.KindConflicts
	r.Gone += l.Gone
	r.WriteErrors += l.WriteErrors
	for k, n := range l.Unreparsable {
		r.Unreparsable[k] += n
	}
	for k, n := range l.Notes {
		r.Notes[k] += n
	}
	for k, n := range l.KindBefore {
		r.KindBefore[k] += n
	}
	for k, n := range l.KindAfter {
		r.KindAfter[k] += n
	}
}

func (t *reparseTally) snapshot() reparseFileIntrosResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.res
}

func newReparseLocal() *reparseFileIntrosResult {
	return &reparseFileIntrosResult{
		Unreparsable: map[string]int{}, Notes: map[string]int{},
		KindBefore: map[string]int{}, KindAfter: map[string]int{},
	}
}

// count folds one row's outcome into a chunk-local tally. wrote says the patch
// was committed (always true for a would-write row in a preview); a write that
// failed is counted by the caller as an error and the row keeps its stored kind.
func (l *reparseFileIntrosResult) count(r reparseRow, wrote bool) {
	l.Examined++
	if r.unreparsable != "" {
		l.Unreparsed++
		l.Unreparsable[r.unreparsable]++
		return
	}
	l.Reparsed++
	if r.note != "" {
		l.Notes[r.note]++
	}
	l.KindBefore[r.before]++
	if !wrote && r.write {
		l.KindAfter[r.before]++
		return
	}
	l.KindAfter[r.after]++
	if r.dec.KindConflict {
		l.KindConflicts++
	}
	if !r.write {
		return
	}
	l.ParseWrites++
	if r.dec.IdentityWrite {
		l.IdentityWrites++
		l.IdentityFilled += r.dec.Filled
		l.IdentityRepaired += r.dec.Repaired
	}
	if r.dec.StatusWrite {
		l.StatusPromoted++
	}
}

// reparseFileIntrosEnv is everything the core reaches the world through.
type reparseFileIntrosEnv struct {
	reads    reparseFileIntrosReads
	writer   database.BookFileIntroReparser // nil when the store cannot write
	queue    OpQueueReader
	reporter sdk.Reporter
	// workers is the pool size; 0 means runtime.NumCPU().
	workers int
}

// reparseFileIntrosReads are the two reads the op makes: the listing it selects
// from and the full row (transcript included) of each file.
type reparseFileIntrosReads interface {
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
}

func (p *Plugin) reparseFileIntrosDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          reparseFileIntrosID,
		Preview:     sdk.PreviewSpec{Params: reparseFileIntrosParams{}},
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "maintenance",
		DisplayName: "Re-parse stored file intro transcripts",
		Description: "Re-derives what each stored per-file intro transcript IS (title announcement, credits, chapter marker, part marker, front/back matter, narration, music or silence, garbage) and the fields in it (chapter and part number, chapter title, publisher, \"books N through M\", title, author, narrator) from the stored text, with no Whisper call and no change to which files get transcribed. " +
			"The classification is stored on the file as a new additive field; the existing status and Transcribed* columns keep their meaning. " +
			"It never downgrades identity: identity is written only by a title-announcement parse, a field is never emptied, and a stored value is replaced only when it is defective (leaks credit wording or a publisher lead, or overruns the length a clean value has) and the new one is not; an unparsed row becomes ok only when a title is parsed. " +
			"Rows with no transcript cannot be re-parsed and are counted by class. Reports before and after counts per kind. dry_run defaults to true. Resumable: a restart continues from the last checkpoint, and a repeat run changes nothing.",
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  reparseFileIntrosID,
		// NO Writes declaration, for the reason on fileIntroTranscribeDef: the op
		// writes only the intro group of one row at a time (ReparseBookFileIntro,
		// under the row's lock, compare-and-swap on the stored bytes), and a
		// whole-row writer that reads under that lock cannot revert it because the
		// shared book_file merge classes keep the group (bookFileFieldClasses). The
		// scanner's batch upsert reads without the row lock, which is why a live run
		// refuses to start while a scan runs and waits before every chunk's writes.
		Cancellable:     true,
		Isolate:         false,
		Timeout:         6 * time.Hour,
		ProgressTimeout: 10 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runReparseFileIntros,
	}
}

func (p *Plugin) runReparseFileIntros(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	var params reparseFileIntrosParams
	if len(rawParams) > 0 && string(rawParams) != "null" {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("%s: decode params: %w", reparseFileIntrosID, err)
		}
	}
	dryRun, err := opmode.ResolveDryRun(reparseFileIntrosID, params.DryRun, params.DryRunCamel)
	if err != nil {
		return err
	}
	env := reparseFileIntrosEnv{reads: store, queue: p.deps.OperationQueueStore(), reporter: reporter}
	if w, ok := database.AsCapability[database.BookFileIntroReparser](store); ok {
		env.writer = w
	}
	res, err := runReparseFileIntros(ctx, env, params.ResumeFrom, dryRun)
	if serr := registry.ReporterSetResult(reporter, res); serr != nil {
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("could not persist result data: %v", serr))
	}
	return err
}

type reparseRef struct{ bookID, fileID string }

// runReparseFileIntros is the op body.
func runReparseFileIntros(ctx context.Context, env reparseFileIntrosEnv, resumeFrom int, dryRun bool) (reparseFileIntrosResult, error) {
	mark := introModeMarker(dryRun)
	reporter := env.reporter
	log := reporter.Logger()

	if !dryRun {
		if env.writer == nil {
			return reparseFileIntrosResult{DryRun: dryRun}, fmt.Errorf("%s: the store does not support per-file intro re-parse writes", reparseFileIntrosID)
		}
		// A scan's unlocked read-modify-write can bring a stale intro group back
		// over a re-parse, so a live run obeys the same gate as a transcription
		// run: no start while one is active, and a wait before every chunk.
		if err := reparseRefuseIfBusy(env.queue); err != nil {
			return reparseFileIntrosResult{DryRun: dryRun}, err
		}
	}

	_ = reporter.UpdateProgress(0, 1, mark+"Selecting files — reading the book file listing")
	cores, err := env.reads.GetAllBookFilesCore()
	if err != nil {
		return reparseFileIntrosResult{DryRun: dryRun}, fmt.Errorf("%s: read book files: %w", reparseFileIntrosID, err)
	}
	var refs []reparseRef
	for i := range cores {
		c := &cores[i]
		if c.TranscribeStatus != nil || c.IntroTranscribedAt != nil || c.IntroSourceHash != "" {
			refs = append(refs, reparseRef{c.BookID, c.ID})
		}
	}
	cores = nil
	// A watermark counts positions, so the order must mean the same thing on the
	// next attempt: sort rather than trust the store's enumeration order.
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].bookID != refs[j].bookID {
			return refs[i].bookID < refs[j].bookID
		}
		return refs[i].fileID < refs[j].fileID
	})

	chunks := make([][]reparseRef, 0, (len(refs)+reparseChunkSize-1)/reparseChunkSize)
	for i := 0; i < len(refs); i += reparseChunkSize {
		chunks = append(chunks, refs[i:min(i+reparseChunkSize, len(refs))])
	}
	// Clamp rather than trust: a checkpoint can name a watermark past the end
	// once files were removed.
	resume := min(max(resumeFrom, 0), len(chunks))
	if dryRun {
		// A preview writes nothing and is cheap to repeat, so it always counts
		// the whole set: a partial count would be a wrong report.
		resume = 0
	}
	if resume > 0 {
		log.Info(reparseFileIntrosID+": resuming", "chunk", resume, "chunks", len(chunks))
	}

	tally := newReparseTally(dryRun)
	var chunksDone atomic.Int64
	chunksDone.Store(int64(resume))
	var progressMu sync.Mutex // the reporter is not required to be goroutine-safe

	workers := env.workers
	if workers < 1 {
		workers = runtime.NumCPU()
	}
	live := false
	opts := registry.RunItemsOptions{
		// Chunks are disjoint sets of files and each write is a per-row
		// compare-and-swap, so workers cannot touch the same row.
		Concurrency:   workers,
		ProgressTotal: len(chunks),
		ResumeFrom:    resume,
		Label: func(i, total int) string {
			return mark + fmt.Sprintf("Re-parsing — chunk %d/%d, %d files examined, %d to change", resume+i+1, total, tally.examined.Load(), tally.changed.Load())
		},
	}
	if !dryRun {
		live = true
		opts.CheckpointEvery = reparseCheckpointEvery
		// Every param is carried: a checkpoint is merged into the resumed run's
		// params, and a dry_run dropped here would come back as the PREVIEW
		// default and silently turn the unfinished half of a live run into a
		// preview that reports success.
		f := false
		opts.CheckpointStateFn = func(_ context.Context, watermark int) error {
			return reporter.Checkpoint(reparseFileIntrosParams{DryRunParams: opmode.DryRunParams{DryRun: &f}, ResumeFrom: watermark})
		}
	}

	err = registry.RunItems(ctx, reporter, chunks, func(ctx context.Context, refs []reparseRef) error {
		local := newReparseLocal()
		defer func() { tally.add(local) }()
		if live {
			// Immediately before this chunk's writes, not only at start: a run over
			// many chunks outlives a scan that begins mid-run.
			if err := waitWhileScanBusy(ctx, reparseFileIntrosID, env.queue, func(def string) {
				progressMu.Lock()
				defer progressMu.Unlock()
				_ = reporter.UpdateProgress(int(chunksDone.Load()), max(len(chunks), 1), mark+"Re-parse paused — "+def+" is running; resumes when it finishes")
			}); err != nil {
				return err
			}
		}
		for _, ref := range refs {
			if err := ctx.Err(); err != nil {
				return err
			}
			reparseOne(env, ref, live, local, log, tally)
		}
		chunksDone.Add(1)
		return nil
	}, opts)

	res := tally.snapshot()
	summary := fmt.Sprintf("%s%d files examined, %d re-parsed, %d could not be re-parsed; %d parse writes, %d identity writes (%d fields filled, %d repaired), %d unparsed->ok, %d identities kept despite a non-announcement clip, %d write errors",
		mark, res.Examined, res.Reparsed, res.Unreparsed, res.ParseWrites, res.IdentityWrites, res.IdentityFilled, res.IdentityRepaired, res.StatusPromoted, res.KindConflicts, res.WriteErrors)
	log.Info(reparseFileIntrosID+": finished", "summary", summary, "err", err)
	_ = reporter.UpdateProgress(len(chunks), max(len(chunks), 1), summary)
	if err != nil {
		return res, err
	}
	if res.WriteErrors > 0 {
		return res, fmt.Errorf("%s: %d writes failed; the rows keep their stored values and a repeat run retries them", reparseFileIntrosID, res.WriteErrors)
	}
	return res, nil
}

// reparseOne re-parses one file and folds the outcome into local.
func reparseOne(env reparseFileIntrosEnv, ref reparseRef, live bool, local *reparseFileIntrosResult, log *slog.Logger, tally *reparseTally) {
	row, err := env.reads.GetBookFileByID(ref.bookID, ref.fileID)
	if err != nil {
		log.Warn(reparseFileIntrosID+": read failed", "file_id", ref.fileID, "err", err)
		local.WriteErrors++
		return
	}
	if row == nil {
		local.Gone++
		return
	}
	tally.examined.Add(1)
	if row.IntroTranscription == nil || *row.IntroTranscription == "" {
		local.count(reparseRow{unreparsable: reparseNoTranscript}, false)
		return
	}
	cur := database.IntroReparseRow{Transcript: *row.IntroTranscription, Parse: row.IntroParse}
	if row.TranscribeStatus != nil {
		cur.Status = *row.TranscribeStatus
	}
	cur.Identity = database.IntroReparseIdentity{
		Title: reparseDeref(row.TranscribedTitle), Author: reparseDeref(row.TranscribedAuthor), Narrator: reparseDeref(row.TranscribedNarrator),
		Translator: reparseDeref(row.TranscribedTranslator), CoverArtist: reparseDeref(row.TranscribedCoverArtist),
	}
	out := reparseDecide(cur)
	if out.write {
		tally.changed.Add(1)
	}
	if !live || !out.write || out.unreparsable != "" {
		local.count(out, true)
		return
	}

	// The decision is re-made on the row the store re-reads under its lock, so a
	// writer that changed the row since the read above is never overwritten with
	// a verdict about text it no longer holds. last is the outcome of the final
	// run of decide: the store may run it more than once.
	var last reparseRow
	outcome, werr := env.writer.ReparseBookFileIntro(ref.bookID, ref.fileID, func(fresh database.IntroReparseRow) (database.IntroReparsePatch, bool) {
		last = reparseDecide(fresh)
		return last.patch, last.write && last.unreparsable == ""
	})
	switch {
	case werr != nil:
		log.Warn(reparseFileIntrosID+": write failed", "file_id", ref.fileID, "err", werr)
		local.WriteErrors++
		local.count(out, false)
	case outcome == database.IntroReparseGone:
		local.Gone++
	case outcome == database.IntroReparseNoTranscript:
		local.count(reparseRow{unreparsable: reparseNoTranscript}, false)
	case outcome == database.IntroReparseWrote:
		local.count(last, true)
	default:
		// Skipped: on the fresh row there was nothing left to change (another
		// writer, or an earlier attempt, got there first).
		last.write = false
		local.count(last, true)
	}
}

func reparseDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// reparseRefuseIfBusy is the START gate of a live run: it refuses to begin while
// a scan or bulk apply is queued or running, and refuses when the queue cannot
// be read. A point-in-time check; the wait before every chunk covers a scan that
// starts later.
func reparseRefuseIfBusy(queue OpQueueReader) error {
	if queue == nil {
		return fmt.Errorf("%s: cannot verify no library scan is active; refusing to run", reparseFileIntrosID)
	}
	active, err := queue.ListActiveOperationsV2()
	if err != nil {
		return fmt.Errorf("%s: cannot list active operations; refusing to run: %w", reparseFileIntrosID, err)
	}
	if def := fileIntroBusyDef(active); def != "" {
		return fmt.Errorf("%s: %s is queued or running; refusing to start: a scan rewrites book_file rows with a read-modify-write that can erase an intro group written between its read and its commit. Run this op when the scan has finished", reparseFileIntrosID, def)
	}
	return nil
}
