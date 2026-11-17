// file: internal/plugins/maintenance/file_intro_reparse.go
// version: 2.0.1
// guid: 7d3a5e91-c2b4-4f08-a6e7-91b0d4c8f325
// last-edited: 2026-11-17

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/franchise"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// reparseFileIntrosID is the op id.
const reparseFileIntrosID = "maintenance.reparse-file-intros"

// This op stores a CLASSIFICATION next to each per-file intro transcript and
// writes nothing else. BookFile.IntroParse is the only column it touches: the
// stored title, author, narrator, translator, cover artist, status, transcript
// and provenance are exactly as the transcription run left them, because guards,
// the metadata search query and the apply gate read those columns and a changed
// grammar must not change what they see. What the op does report, on a preview
// and a live run alike, is how the new parse compares with the stored identity
// (identityReport), so a later change can decide from counts whether any repair
// is safe. It decides nothing itself.

// reparseChunkSize is how many FILES one work item (one write batch) names.
// Chunks are disjoint sets of files, so no two workers ever touch the same row.
const reparseChunkSize = 64

// reparseCheckpointEvery is how many completed chunks pass between checkpoints
// (64 files per chunk, so about three thousand files).
const reparseCheckpointEvery = 50

// reparseSampleCap bounds each class's sample list. Samples are file ids only (no
// titles, no transcript text), and the lowest ids are kept so two runs over the
// same data report the same samples.
const reparseSampleCap = 10

// Classes of stored row a re-parse cannot act on. They are reported by name so
// the owner can see how many rows each one holds.
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
)

// reparseNoteSilence marks a parsed row whose whole text is the silence sentinel:
// its kind is derivable and there are no fields to extract.
const reparseNoteSilence = "silence_sentinel_only"

// reparseKindUnclassified names, in the before-counts, a row with no stored parse.
const reparseKindUnclassified = "unclassified"

// Conflict classes reported beyond transcribe.ConflictKind: the one a repair
// would most likely get wrong, so it is counted before the others.
const reparseConflictFranchiseLost = "franchise_term_lost"

// reparseResumeKey is the last file a previous live attempt finished: every file
// ordered at or before (book id, file id) is done. A key, not a position, so a
// resume over a library that gained or lost files in between neither skips a
// file nor repeats a long prefix.
type reparseResumeKey struct{ bookID, fileID string }

func (k reparseResumeKey) zero() bool { return k.bookID == "" && k.fileID == "" }

// reparseFileIntrosParams: dry_run omitted means PREVIEW (opmode.ResolveDryRun).
type reparseFileIntrosParams struct {
	opmode.DryRunParams
	// ResumeAfterBookID and ResumeAfterFileID are the key of the last file of the
	// contiguous run of chunks a previous live attempt completed. The registry
	// merges the checkpoint back into the params on a restart, so they arrive
	// populated on a resumed run and empty on a fresh one. An optimisation, not a
	// correctness mechanism: every write is idempotent, so a file at or before the
	// key that is examined again changes nothing.
	ResumeAfterBookID string `json:"resume_after_book_id,omitempty"`
	ResumeAfterFileID string `json:"resume_after_file_id,omitempty"`
}

// identityReport is how the clip parse compares with the identity already stored,
// over the rows where the parse is a title announcement. It is measurement only.
type identityReport struct {
	// Announcements is the rows whose parse is a title announcement.
	Announcements int `json:"title_announcements"`
	// Agree: every field the parse names equals the stored one.
	Agree int `json:"agree"`
	// WouldFill: nothing conflicts and at least one stored-empty field would be
	// filled; WouldFillFields is the number of such fields.
	WouldFill       int `json:"would_fill"`
	WouldFillFields int `json:"would_fill_fields"`
	// Conflict: a stored field and the parse's value for it differ, split by how
	// (publisher_prefix, truncation, different_title, other_field, and
	// franchise_term_lost, which takes precedence over the title classes).
	Conflict   int            `json:"conflict"`
	ConflictBy map[string]int `json:"conflict_by_kind"`
	// StoredTitleJunk counts stored titles the grammar itself rejects as a title
	// (credit wording inside, a bare credit verb, ending in one), over every
	// examined row with a stored title, and splits them by reason.
	StoredTitleJunk   int            `json:"stored_title_junk"`
	StoredTitleJunkBy map[string]int `json:"stored_title_junk_by_reason"`
}

// reparseFileIntrosResult is what the op reports, preview or live. Counts are
// per FILE. In a live run every "after" figure is what was actually stored (a
// write that failed counts as unchanged); in a preview it is what a live run
// would store.
type reparseFileIntrosResult struct {
	DryRun bool `json:"dry_run"`
	// Resumed is true when a live run continued from a checkpoint (so the counts
	// cover only the files after it).
	Resumed bool `json:"resumed,omitempty"`
	// NotSelected is the files with no intro field at all; they are never read.
	// Every writer of a transcript also writes a status, a time and a done
	// marker, so a transcript cannot hide among them; the count is here so a
	// surprising number is visible.
	NotSelected int `json:"files_without_intro_fields"`
	// Selected is the files the op set out to read; it equals Examined + Gone +
	// Errors.
	Selected int `json:"selected"`
	// Examined is the files read that still existed; Reparsed the number whose
	// stored text was parsed (Examined minus Unreparsed).
	Examined   int `json:"examined"`
	Reparsed   int `json:"reparsed"`
	Unreparsed int `json:"unreparsable"`
	// Unreparsable counts, by class, the rows a re-parse could not act on at all
	// (no_transcript, blank_transcript, stored_parse_from_newer_grammar).
	Unreparsable map[string]int `json:"unreparsable_by_class,omitempty"`
	// Notes counts rows that were parsed but have a caveat.
	Notes map[string]int `json:"notes,omitempty"`

	// KindBefore and KindAfter are the per-kind counts of the parsed rows; a row
	// with no stored parse is "unclassified" before.
	KindBefore map[string]int `json:"kind_before"`
	KindAfter  map[string]int `json:"kind_after"`

	// ParseWrites is the number of files whose stored parse changed (or was
	// absent). It is the only thing this op writes.
	ParseWrites int `json:"parse_writes"`

	// Identity is the disagreement report; Samples lists up to reparseSampleCap
	// file ids per class (keys like "conflict.publisher_prefix").
	Identity identityReport      `json:"identity_disagreement"`
	Samples  map[string][]string `json:"sample_file_ids,omitempty"`

	// Gone counts files deleted between the listing and the read (or the write);
	// Errors the files that could not be read or written (the row is left as it was).
	Gone   int `json:"gone"`
	Errors int `json:"errors"`
}

// reparseRow is the pure outcome of re-parsing one stored row.
type reparseRow struct {
	// unreparsable is the class of a row nothing could be done with ("" when
	// the text was parsed).
	unreparsable string
	// note is a caveat of a parsed row ("" for none).
	note string

	before, after string

	// junk is the grammar's verdict on the stored title.
	junk transcribe.JunkReason
	// cmp compares the parse with the stored identity (title announcements only);
	// franchiseLost is set when a conflicting title dropped a franchise term.
	cmp           transcribe.IdentityComparison
	franchiseLost bool

	// write is true when the stored parse differs from the new one; parse is what
	// to write.
	write bool
	parse database.IntroParse
}

// introParseFromClip is the stored form of a parse. IntroParse holds the RAW
// parse: whatever the clip grammar read, including a title announcement's
// fields, whether or not they agree with the identity stored on the row. No
// reader trusts it yet; the disagreement report is how its quality is measured.
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

// reparseDecide re-derives one row's parse from its stored text and measures how
// it compares with the stored identity. It is a pure function of the row, so a
// lost compare-and-swap race simply calls it again on the fresh row.
func reparseDecide(cur database.IntroReparseRow) reparseRow {
	before := reparseKindUnclassified
	if cur.Parse != nil {
		before = cur.Parse.Kind
	}
	out := reparseRow{before: before, after: before, junk: transcribe.StoredTitleJunk(cur.Identity.Title)}

	if cur.Parse != nil && cur.Parse.Version > transcribe.ClipParserVersion {
		out.unreparsable = reparseNewerParser
		return out
	}
	p := transcribe.ParseClip(cur.Transcript)
	if p.Kind == transcribe.ClipKindNone {
		out.unreparsable = reparseBlankTranscript
		return out
	}
	out.after = string(p.Kind)
	if p.Kind == transcribe.ClipKindSilence && cur.Transcript == transcribe.SilenceSentinel {
		out.note = reparseNoteSilence
	}

	stored := transcribe.Identity{
		Title: cur.Identity.Title, Author: cur.Identity.Author, Narrator: cur.Identity.Narrator,
		Translator: cur.Identity.Translator, CoverArtist: cur.Identity.CoverArtist,
	}
	out.cmp = transcribe.CompareIdentity(stored, p)
	// A title that differs AND loses a franchise term the stored one carried is the
	// disagreement most likely to change a guard's verdict, so it is split out.
	if out.cmp.Verdict == transcribe.VerdictConflict && out.cmp.Conflict != transcribe.ConflictOtherField &&
		franchise.Matches(stored.Title) && !franchise.Matches(p.Title) {
		out.franchiseLost = true
	}

	np := introParseFromClip(p)
	out.write = cur.Parse == nil || *cur.Parse != np
	out.parse = np
	return out
}

// reparseTally is the run's counters. The atomics are what the progress label
// reads (it runs inside the worker goroutines); the maps and the rest are folded
// in under mu once per chunk.
type reparseTally struct {
	// processed is files handled, whatever the outcome (read error, gone, parsed);
	// changed is parse writes decided (preview) or committed (live).
	processed atomic.Int64
	changed   atomic.Int64

	mu  sync.Mutex
	res reparseFileIntrosResult
}

func newReparseResult(dryRun bool) reparseFileIntrosResult {
	return reparseFileIntrosResult{
		DryRun:       dryRun,
		Unreparsable: map[string]int{},
		Notes:        map[string]int{},
		KindBefore:   map[string]int{},
		KindAfter:    map[string]int{},
		Samples:      map[string][]string{},
		Identity: identityReport{
			ConflictBy:        map[string]int{},
			StoredTitleJunkBy: map[string]int{},
		},
	}
}

func newReparseTally(dryRun bool) *reparseTally {
	return &reparseTally{res: newReparseResult(dryRun)}
}

// addSample keeps the reparseSampleCap lowest distinct ids per class, sorted.
func addSample(m map[string][]string, class, id string) {
	ids := m[class]
	i := sort.SearchStrings(ids, id)
	if i < len(ids) && ids[i] == id {
		return
	}
	if i >= reparseSampleCap {
		return
	}
	ids = append(ids, "")
	copy(ids[i+1:], ids[i:])
	ids[i] = id
	if len(ids) > reparseSampleCap {
		ids = ids[:reparseSampleCap]
	}
	m[class] = ids
}

func addCounts(dst, src map[string]int) {
	for k, n := range src {
		dst[k] += n
	}
}

// add folds one chunk's local tally in.
func (t *reparseTally) add(l *reparseFileIntrosResult) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r := &t.res
	r.Selected += l.Selected
	r.Examined += l.Examined
	r.Reparsed += l.Reparsed
	r.Unreparsed += l.Unreparsed
	r.ParseWrites += l.ParseWrites
	r.Gone += l.Gone
	r.Errors += l.Errors
	addCounts(r.Unreparsable, l.Unreparsable)
	addCounts(r.Notes, l.Notes)
	addCounts(r.KindBefore, l.KindBefore)
	addCounts(r.KindAfter, l.KindAfter)
	ri, li := &r.Identity, &l.Identity
	ri.Announcements += li.Announcements
	ri.Agree += li.Agree
	ri.WouldFill += li.WouldFill
	ri.WouldFillFields += li.WouldFillFields
	ri.Conflict += li.Conflict
	ri.StoredTitleJunk += li.StoredTitleJunk
	addCounts(ri.ConflictBy, li.ConflictBy)
	addCounts(ri.StoredTitleJunkBy, li.StoredTitleJunkBy)
	for class, ids := range l.Samples {
		for _, id := range ids {
			addSample(r.Samples, class, id)
		}
	}
}

func (t *reparseTally) snapshot() reparseFileIntrosResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.res
}

// count folds one examined row's outcome into a chunk-local tally. A write that
// failed is counted by the caller as an error and never reaches here.
func (l *reparseFileIntrosResult) count(r reparseRow, fileID string) {
	l.Examined++
	if r.junk != transcribe.JunkNone {
		l.Identity.StoredTitleJunk++
		l.Identity.StoredTitleJunkBy[string(r.junk)]++
		addSample(l.Samples, "stored_title_junk."+string(r.junk), fileID)
	}
	if r.unreparsable != "" {
		l.Unreparsed++
		l.Unreparsable[r.unreparsable]++
		addSample(l.Samples, "unreparsable."+r.unreparsable, fileID)
		return
	}
	l.Reparsed++
	if r.note != "" {
		l.Notes[r.note]++
	}
	l.KindBefore[r.before]++
	l.countIdentity(r, fileID)
	l.KindAfter[r.after]++
	if r.write {
		l.ParseWrites++
	}
}

// countIdentity folds the disagreement measurement of a title announcement in.
func (l *reparseFileIntrosResult) countIdentity(r reparseRow, fileID string) {
	id := &l.Identity
	switch r.cmp.Verdict {
	case transcribe.VerdictNotAnnouncement, "":
		return
	case transcribe.VerdictAgree:
		id.Agree++
	case transcribe.VerdictFill:
		id.WouldFill++
		id.WouldFillFields += r.cmp.Fillable
		addSample(l.Samples, "would_fill", fileID)
	case transcribe.VerdictConflict:
		id.Conflict++
		kind := string(r.cmp.Conflict)
		if r.franchiseLost {
			kind = reparseConflictFranchiseLost
		}
		id.ConflictBy[kind]++
		addSample(l.Samples, "conflict."+kind, fileID)
	}
	id.Announcements++
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
		Description: "Classifies each stored per-file intro transcript (title announcement, credits, chapter marker, part marker, front/back matter, narration, music or silence, garbage) and reads the fields in it (chapter and part number, chapter title, publisher, \"books N through M\", title, author, narrator) from the stored text, with no Whisper call and no change to which files get transcribed. " +
			"The classification is stored on the file as one new additive field (IntroParse) and NOTHING else is written: the status, the title, author, narrator, translator and cover artist columns, the transcript and its provenance keep exactly the values the transcription run stored, so no guard, search query or apply gate sees a different value. " +
			"Reports before and after counts per kind, and a disagreement report that changes nothing: for rows whose clip is a title announcement, how many agree with the stored identity, how many would fill an empty field and how many conflict (publisher prefix, truncation, different title, franchise term lost, another field), plus the stored titles the grammar itself rejects, with up to 10 sample file ids per class (file ids only). " +
			"Rows with no transcript cannot be re-parsed and are counted by class. dry_run defaults to true. Resumable: a restart continues after the last file of the last completed chunk, and a repeat run changes nothing.",
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
	res, err := runReparseFileIntros(ctx, env, reparseResumeKey{params.ResumeAfterBookID, params.ResumeAfterFileID}, dryRun)
	if serr := registry.ReporterSetResult(reporter, res); serr != nil {
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("could not persist result data: %v", serr))
	}
	return err
}

type reparseRef struct{ bookID, fileID string }

// reparseRefAfter reports whether ref orders strictly after key.
func reparseRefAfter(ref reparseRef, key reparseResumeKey) bool {
	if ref.bookID != key.bookID {
		return ref.bookID > key.bookID
	}
	return ref.fileID > key.fileID
}

// hasIntroFields reports whether a listed file carries any retained intro column.
// The transcript itself is stripped from the listing, so selection reads what is
// retained: every writer of a transcript (SetBookFileIntro, the single-file
// migration) also writes a status, a time, a done marker and a run id, and a
// parsed identity column implies a transcript, so a row holding text cannot have
// none of these.
func hasIntroFields(c *database.BookFileCore) bool {
	return c.TranscribeStatus != nil || c.IntroTranscribedAt != nil || c.IntroSourceHash != "" || c.IntroRunOpID != "" ||
		c.TranscribedTitle != nil || c.TranscribedAuthor != nil || c.TranscribedNarrator != nil ||
		c.TranscribedTranslator != nil || c.TranscribedCoverArtist != nil
}

// runReparseFileIntros is the op body.
func runReparseFileIntros(ctx context.Context, env reparseFileIntrosEnv, resume reparseResumeKey, dryRun bool) (reparseFileIntrosResult, error) {
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
	notSelected := 0
	for i := range cores {
		c := &cores[i]
		if hasIntroFields(c) {
			refs = append(refs, reparseRef{c.BookID, c.ID})
		} else {
			notSelected++
		}
	}
	cores = nil
	// The resume key is an ordering, so the order must mean the same thing on the
	// next attempt: sort rather than trust the store's enumeration order.
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].bookID != refs[j].bookID {
			return refs[i].bookID < refs[j].bookID
		}
		return refs[i].fileID < refs[j].fileID
	})

	resumed := false
	if dryRun {
		// A preview writes nothing and is cheap to repeat, so it always counts the
		// whole set: a partial count would be a wrong report.
		resume = reparseResumeKey{}
	}
	if !resume.zero() {
		// Find the files after the key in the list as it is NOW: a file added or
		// removed since the checkpoint moves nothing that was already done.
		first := sort.Search(len(refs), func(i int) bool { return reparseRefAfter(refs[i], resume) })
		refs = refs[first:]
		resumed = true
		log.Info(reparseFileIntrosID+": resuming", "after_file_id", resume.fileID, "files_left", len(refs))
	}

	chunks := make([][]reparseRef, 0, (len(refs)+reparseChunkSize-1)/reparseChunkSize)
	for i := 0; i < len(refs); i += reparseChunkSize {
		chunks = append(chunks, refs[i:min(i+reparseChunkSize, len(refs))])
	}

	tally := newReparseTally(dryRun)
	tally.res.NotSelected = notSelected
	tally.res.Resumed = resumed
	var chunksDone atomic.Int64

	workers := env.workers
	if workers < 1 {
		workers = runtime.NumCPU()
	}
	live := !dryRun
	opts := registry.RunItemsOptions{
		// Chunks are disjoint sets of files and each write is a per-row
		// compare-and-swap, so workers cannot touch the same row.
		Concurrency: workers,
		// One failed chunk must not stop the others: every chunk that can be
		// written is, and the failures are reported together.
		ErrMode:       registry.ErrModeCollect,
		ProgressTotal: len(chunks),
		// Label runs inside the worker goroutines (registry.RunItems), so it reads
		// only atomics.
		Label: func(i, total int) string {
			return mark + fmt.Sprintf("Re-parsing — chunk %d/%d, %d files processed, %d parse writes", i+1, total, tally.processed.Load(), tally.changed.Load())
		},
	}
	if live {
		opts.CheckpointEvery = reparseCheckpointEvery
		// Every param is carried: a checkpoint is merged into the resumed run's
		// params, and a dry_run dropped here would come back as the PREVIEW
		// default and silently turn the unfinished half of a live run into a
		// preview that reports success. The watermark is a count of chunks of THIS
		// attempt's list; it is stored as the key of the last file of the last of
		// them, which means the same thing on any later attempt.
		f := false
		opts.CheckpointStateFn = func(_ context.Context, watermark int) error {
			if watermark < 1 || watermark > len(chunks) {
				return nil
			}
			last := chunks[watermark-1]
			key := last[len(last)-1]
			return reporter.Checkpoint(reparseFileIntrosParams{
				DryRunParams:      opmode.DryRunParams{DryRun: &f},
				ResumeAfterBookID: key.bookID,
				ResumeAfterFileID: key.fileID,
			})
		}
	}

	err = registry.RunItems(ctx, reporter, chunks, func(ctx context.Context, refs []reparseRef) error {
		local := newReparseResult(dryRun)
		defer func() { tally.add(&local) }()
		if live {
			// Immediately before this chunk's writes, not only at start: a run over
			// many chunks outlives a scan that begins mid-run. The production
			// reporter serialises its own updates (RunItems itself calls it from
			// these goroutines), so no extra lock is taken around this call.
			if err := waitWhileScanBusy(ctx, reparseFileIntrosID, env.queue, func(def string) {
				_ = reporter.UpdateProgress(int(chunksDone.Load()), max(len(chunks), 1), mark+"Re-parse paused — "+def+" is running; resumes when it finishes")
			}); err != nil {
				return err
			}
		}
		for _, ref := range refs {
			if err := ctx.Err(); err != nil {
				return err
			}
			local.Selected++
			reparseOne(env, ref, live, &local, log, tally)
			tally.processed.Add(1)
		}
		// A chunk with a failed read or write is reported as failed, so the
		// watermark stays below it (RunItems never advances it past a failed item)
		// and a resumed attempt retries the chunk instead of skipping it.
		if local.Errors > 0 {
			return fmt.Errorf("%d of %d files in this chunk could not be read or written", local.Errors, len(refs))
		}
		chunksDone.Add(1)
		return nil
	}, opts)

	res := tally.snapshot()
	id := res.Identity
	summary := fmt.Sprintf("%s%d files selected, %d examined, %d re-parsed, %d could not be re-parsed; %d parse writes; title announcements: %d agree, %d would fill, %d conflict; %d stored titles the grammar rejects; %d gone, %d errors",
		mark, res.Selected, res.Examined, res.Reparsed, res.Unreparsed, res.ParseWrites, id.Agree, id.WouldFill, id.Conflict, id.StoredTitleJunk, res.Gone, res.Errors)
	log.Info(reparseFileIntrosID+": finished", "summary", summary, "err", err)
	_ = reporter.UpdateProgress(len(chunks), max(len(chunks), 1), summary)
	if res.Errors > 0 {
		// err is non-nil here: a chunk with failures returns an error.
		return res, fmt.Errorf("%s: %d files could not be read or written; they keep their stored values and a resumed or repeat run retries them: %w", reparseFileIntrosID, res.Errors, errors.Join(err))
	}
	return res, err
}

// reparseOne re-parses one file and folds the outcome into local. A file that
// vanished is Gone and never Examined; a file that could not be read or written
// is Errors and never Examined; every other selected file is Examined exactly
// once.
func reparseOne(env reparseFileIntrosEnv, ref reparseRef, live bool, local *reparseFileIntrosResult, log *slog.Logger, tally *reparseTally) {
	row, err := env.reads.GetBookFileByID(ref.bookID, ref.fileID)
	if err != nil {
		log.Warn(reparseFileIntrosID+": read failed", "file_id", ref.fileID, "err", err)
		local.Errors++
		return
	}
	if row == nil {
		local.Gone++
		return
	}
	cur := database.IntroReparseRow{Parse: row.IntroParse}
	if row.IntroTranscription != nil {
		cur.Transcript = *row.IntroTranscription
	}
	cur.Identity = database.IntroReparseIdentity{
		Title: reparseDeref(row.TranscribedTitle), Author: reparseDeref(row.TranscribedAuthor), Narrator: reparseDeref(row.TranscribedNarrator),
		Translator: reparseDeref(row.TranscribedTranslator), CoverArtist: reparseDeref(row.TranscribedCoverArtist),
	}
	if cur.Transcript == "" {
		local.count(reparseRow{unreparsable: reparseNoTranscript, junk: transcribe.StoredTitleJunk(cur.Identity.Title)}, ref.fileID)
		return
	}
	out := reparseDecide(cur)
	if !live || !out.write {
		if out.write {
			tally.changed.Add(1)
		}
		local.count(out, ref.fileID)
		return
	}

	// The decision is re-made on the row the store re-reads under its lock, so a
	// writer that changed the row since the read above is never overwritten with
	// a verdict about text it no longer holds. last is the outcome of the final
	// run of decide (the store may run it more than once); ran says it ran at all.
	var last reparseRow
	ran := false
	outcome, werr := env.writer.ReparseBookFileIntro(ref.bookID, ref.fileID, func(fresh database.IntroReparseRow) (*database.IntroParse, bool) {
		last, ran = reparseDecide(fresh), true
		if !last.write {
			return nil, false
		}
		p := last.parse
		return &p, true
	})
	switch {
	case werr != nil:
		log.Warn(reparseFileIntrosID+": write failed", "file_id", ref.fileID, "err", werr)
		local.Errors++
	case outcome == database.IntroReparseGone:
		local.Gone++
	case outcome == database.IntroReparseNoTranscript:
		local.count(reparseRow{unreparsable: reparseNoTranscript, junk: out.junk}, ref.fileID)
	case outcome == database.IntroReparseWrote:
		tally.changed.Add(1)
		local.count(last, ref.fileID)
	default:
		// Skipped: on the fresh row there was nothing left to change (another
		// writer, or an earlier attempt, got there first).
		if !ran {
			last = out
		}
		last.write = false
		local.count(last, ref.fileID)
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
