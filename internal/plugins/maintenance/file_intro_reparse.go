// file: internal/plugins/maintenance/file_intro_reparse.go
// version: 3.1.0
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
	"strings"
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

// reparseMaxPasses is the backstop on a live run's passes (see runReparseFileIntros).
// A run normally ends because a pass wrote nothing and lost nothing; it reaches this
// only when every pass keeps producing parse writes or keeps finding files gone, and
// then it fails, saying so, rather than loop.
const reparseMaxPasses = 5

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

// reparseFileIntrosParams: dry_run omitted means PREVIEW (opmode.ResolveDryRun).
//
// There is no resume position. A restarted live run lists the library again and
// visits every selected file: a file whose stored parse is already current is read,
// found current and skipped without a write (reparseDecide), so a restart costs
// one read-only pass over the finished rows and nothing is rewritten. A stored
// position would skip files that came to sort before it between attempts (re-homed
// to another book, newly created), and the run would still report success; the
// extra read pass is what rules that out, and a keyed resume would need the same
// full pass as a closing sweep on top of the keyed one.
type reparseFileIntrosParams struct {
	opmode.DryRunParams
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
	// NotSelected is the files that are neither marked with an intro column nor
	// the only file of a book that carries a book-level transcript; they are never
	// read. A row that holds only a transcript, with every retained intro column
	// unset, is indistinguishable from them in the listing unless its book carries
	// the transcript too (see selectReparseFiles), so the count is here to make a
	// surprising number visible.
	NotSelected int `json:"files_without_intro_fields"`
	// SelectedByBookTranscript is how many selected files were selected only because
	// they are the sole audio file of a book with a book-level transcript and carry no
	// retained intro column of their own. It is the transcript-only rows PLUS every
	// such file that holds no text of its own (a book whose transcript the book-level
	// op wrote and the single-file migration never copied): those are read in full and
	// land in unreparsable.no_transcript, so that class is inflated by them.
	SelectedByBookTranscript int `json:"selected_by_book_transcript"`
	// Listed is the files the run set out to read, over every pass; Selected is the
	// files a worker actually started on. They are equal on a run that finished. A
	// run that stopped early (cancelled, or a chunk abandoned before its files) has
	// Listed above Selected, which is how the shortfall shows.
	Listed int `json:"listed"`
	// Selected is the files a worker started on; it equals Examined + Gone +
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
	// ParseWriteBytes is the total size, in bytes, of the stored rows those writes
	// replace (each row as it is stored with the new parse): the volume a live run
	// pushes through the database, and what a preview reports so the size is known
	// before anyone starts a live run. Every write re-encodes the WHOLE row,
	// fingerprint included, to add the few hundred bytes of the parse.
	//
	// A live run reports the size of the row the writer re-read under its lock, encoded
	// with the timestamp it stamped: exact. A preview cannot know the timestamp a later
	// write will stamp, and the encoded timestamp's width depends on its fractional
	// digits, so its figure is within 10 bytes per written row (database.IntroParseRowBytes).
	ParseWriteBytes int64 `json:"parse_write_bytes"`
	// WritesUnconfirmed counts parse writes (already in ParseWrites) that were applied
	// but whose durability the database could not confirm. The row holds the new parse
	// in the running process; a repeat run rewrites nothing that did land. It is not an
	// error row, and a run that has any ends in a failure that says so.
	WritesUnconfirmed int `json:"writes_unconfirmed"`

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
	// to write; rowBytes is the size of the row as it would then be stored (set by
	// the caller that has the full row; 0 when nothing is written).
	write    bool
	parse    database.IntroParse
	rowBytes int64
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
	r.ParseWriteBytes += l.ParseWriteBytes
	r.WritesUnconfirmed += l.WritesUnconfirmed
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
		l.ParseWriteBytes += r.rowBytes
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
	// now is the clock a preview uses for the timestamp it assumes a write would
	// stamp when it sizes a row; nil means time.Now. Tests pin it.
	now func() time.Time
}

func (e reparseFileIntrosEnv) stamp() time.Time {
	if e.now != nil {
		return e.now()
	}
	return time.Now()
}

// reparseFileIntrosReads are the reads the op makes: the two listings it selects
// from and the full row (transcript included) of each file. Both listings are the
// Complete variants: they never answer from a memdb known to be missing rows (they
// fall through to the authoritative Pebble scan, and fail when that fails), so a
// short memdb cannot silently shrink the selection.
type reparseFileIntrosReads interface {
	GetAllBookFilesCoreComplete() ([]database.BookFileCore, error)
	GetAllBooksCoreComplete(limit, offset int) ([]database.BookCore, error)
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
}

func (p *Plugin) reparseFileIntrosDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          reparseFileIntrosID,
		Preview:     sdk.PreviewSpec{Params: reparseFileIntrosParams{}},
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "maintenance",
		DisplayName: "Re-parse stored file intro transcripts",
		Description: "Classifies the stored per-file intro transcripts (title announcement, credits, chapter marker, part marker, front/back matter, narration, music or silence, garbage) and reads the fields in it (chapter and part number, chapter title, publisher, \"books N through M\", title, author, narrator) from the stored text, with no Whisper call and no change to which files get transcribed. " +
			"The classification is stored on the file as one new additive field (IntroParse) and NOTHING else is written: the status, the title, author, narrator, translator and cover artist columns, the transcript and its provenance keep exactly the values the transcription run stored, so no guard, search query or apply gate sees a different value. " +
			"Reports before and after counts per kind, and a disagreement report that changes nothing: for rows whose clip is a title announcement, how many agree with the stored identity, how many would fill an empty field and how many conflict (publisher prefix, truncation, different title, franchise term lost, another field), plus the stored titles the grammar itself rejects, with up to 10 sample file ids per class (file ids only). " +
			"Rows with no transcript cannot be re-parsed and are counted by class. dry_run defaults to true and reports the number of rows a live run would rewrite and their total size in bytes, within 10 bytes per row (every write re-encodes the whole stored row, fingerprint included). A live run is NOT recommended until something reads the field: nothing does today, so a live run only spends that write volume. A restart begins again from the first file, reads the rows already done and rewrites none of them, and a repeat run changes nothing.",
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  reparseFileIntrosID,
		// NO Writes declaration, for the reason on fileIntroTranscribeDef: the op
		// writes only the intro group of one row at a time (ReparseBookFileIntro,
		// under the row's lock, compare-and-swap on the stored bytes), and a
		// whole-row writer that reads under that lock cannot revert it because the
		// shared book_file merge classes keep the group (bookFileFieldClasses). The
		// scanner's batch upsert takes the same file stripes from its lookup to its
		// commit, so it cannot interleave with a write here. The start gate and the
		// wait before every chunk's writes are kept all the same: the other ops in
		// fileIntroBusyOps (import, folder auto-scan, bulk metadata apply) and the
		// batch paths that take no file stripe (moves, deletes) have not been checked
		// against this field, so the op does not run alongside them.
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
	res, err := runReparseFileIntros(ctx, env, dryRun)
	if serr := registry.ReporterSetResult(reporter, res); serr != nil {
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("could not persist result data: %v", serr))
	}
	return err
}

type reparseRef struct{ bookID, fileID string }

// hasIntroFields reports whether a listed file carries any retained intro column.
// The transcript itself is stripped from the listing, so this reads what is
// retained: the status, time, done marker and run id the transcription run and the
// single-file migration normally write alongside a transcript, and the parsed
// identity columns (which imply one). It is NOT a proof that a row holds no
// transcript when it answers false: a row can hold only a transcript (a silence
// sentinel written without a marker, a migration that copied a book's transcript
// with a nil status and time). selectReparseFiles covers those through the book.
func hasIntroFields(c *database.BookFileCore) bool {
	return c.TranscribeStatus != nil || c.IntroTranscribedAt != nil || c.IntroSourceHash != "" || c.IntroRunOpID != "" ||
		c.TranscribedTitle != nil || c.TranscribedAuthor != nil || c.TranscribedNarrator != nil ||
		c.TranscribedTranslator != nil || c.TranscribedCoverArtist != nil
}

// selectReparseFiles lists the files a pass will read, sorted by (book id, file
// id), from the Complete listings (a short memdb falls through to the Pebble scan
// and a failed scan is an error, never a shorter list).
//
// A file is selected when it has any retained intro column (hasIntroFields), or
// when it is the only AUDIO file of a book whose book-level transcript is non-blank.
// The second rule is how a transcript-only row is reached: the listing cannot say
// whether a file holds text, but the two writers that leave a file with text and no
// retained column both start from a book-level transcript (the silence sentinel and
// the single-file migration, which copies a book's transcript onto the book's single
// audio file and counts audio files only, so a cover-art row beside it does not
// make the book multi-file here either; isTranscribable is the shared test).
// A transcript-only row whose book has no transcript, and a transcript-only file of
// a multi-file book, cannot be told from a file with no transcript without reading
// every book_file row in the library (fingerprints included), which this op does not
// do; NotSelected counts them with everything else not selected.
//
// The second rule also selects an audio file that holds no text at all (a book whose
// transcript only the book-level op wrote and the migration never copied): the row is
// read in full and counted as unreparsable.no_transcript.
func selectReparseFiles(reads reparseFileIntrosReads) (refs []reparseRef, notSelected, byBookTranscript int, err error) {
	files, err := reads.GetAllBookFilesCoreComplete()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("read book files: %w", err)
	}
	books, err := reads.GetAllBooksCoreComplete(0, 0)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("read books: %w", err)
	}
	hasBookText := make(map[string]bool, len(books))
	for i := range books {
		if t := books[i].IntroTranscription; t != nil && strings.TrimSpace(*t) != "" {
			hasBookText[books[i].ID] = true
		}
	}
	perBook := make(map[string]int, len(books))
	for i := range files {
		if isTranscribable(&files[i]) {
			perBook[files[i].BookID]++
		}
	}
	for i := range files {
		c := &files[i]
		switch {
		case hasIntroFields(c):
			refs = append(refs, reparseRef{c.BookID, c.ID})
		case isTranscribable(c) && perBook[c.BookID] == 1 && hasBookText[c.BookID]:
			refs = append(refs, reparseRef{c.BookID, c.ID})
			byBookTranscript++
		default:
			notSelected++
		}
	}
	// Sorted so chunks hold neighbouring files of a book and two runs over the same
	// data cut the same chunks, not because any position is kept between runs.
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].bookID != refs[j].bookID {
			return refs[i].bookID < refs[j].bookID
		}
		return refs[i].fileID < refs[j].fileID
	})
	return refs, notSelected, byBookTranscript, nil
}

// runReparseFileIntros is the op body.
//
// A live run works in passes. Each pass lists the library, visits the selected
// files the run has not visited yet, and the run ends successfully when a pass
// makes no parse write and finds no file Gone: everything that pass read was
// already current and nothing it listed had vanished or moved, so another pass
// would only read more current rows. A pass that wrote, or found a file Gone, is
// followed by a fresh listing: a row a merge re-homed (same file id, new book)
// reappears under its new book, which is a new (book id, file id) key and is
// visited, and a row the pass itself made selectable is seen. reparseMaxPasses is
// only a backstop; a run that reaches it fails and says passes kept producing writes.
//
// What a success does and does not say: every file the LAST pass's listing held was
// visited, and that pass found nothing to write or lose. It does not say the library
// holds no unclassified row. A file that appears after the last pass's listing (a
// transcription run finishing one, a new row) is outside this run, as it would be
// for any snapshot of a library that is being written to; a repeat run picks it up.
// Rows that are already current when they appear never prolong the run, which is
// what keeps it from failing while a whole-library transcription run adds files.
// A preview is a single pass: it reports one listing.
func runReparseFileIntros(ctx context.Context, env reparseFileIntrosEnv, dryRun bool) (reparseFileIntrosResult, error) {
	mark := introModeMarker(dryRun)
	reporter := env.reporter
	log := reporter.Logger()

	if !dryRun {
		if env.writer == nil {
			return reparseFileIntrosResult{DryRun: dryRun}, fmt.Errorf("%s: the store does not support per-file intro re-parse writes", reparseFileIntrosID)
		}
		// A live run does not write alongside a scan, import, folder auto-scan or
		// bulk metadata apply (fileIntroBusyOps): no start while one is active, and a
		// wait before every chunk. See the note on the op's definition for what is
		// and is not known about each of those writers.
		if err := reparseRefuseIfBusy(env.queue); err != nil {
			return reparseFileIntrosResult{DryRun: dryRun}, err
		}
	}

	tally := newReparseTally(dryRun)
	// Keyed by (book id, file id): a merge re-homes a row under a new book with the
	// same file id, and that row has not been visited at its new place.
	visited := map[reparseRef]struct{}{}
	for pass := 1; ; pass++ {
		_ = reporter.UpdateProgress(0, 1, mark+"Selecting files — reading the book file listing")
		listed, notSelected, byBook, err := selectReparseFiles(env.reads)
		if err != nil {
			res := tally.snapshot()
			return res, fmt.Errorf("%s: %w", reparseFileIntrosID, err)
		}
		if pass == 1 {
			tally.mu.Lock()
			tally.res.NotSelected = notSelected
			tally.res.SelectedByBookTranscript = byBook
			tally.mu.Unlock()
		}
		refs := listed[:0:0]
		for _, r := range listed {
			if _, seen := visited[r]; !seen {
				refs = append(refs, r)
			}
		}
		if len(refs) == 0 {
			break
		}
		if pass > 1 {
			log.Info(reparseFileIntrosID+": further files to visit; running another pass", "pass", pass, "files", len(refs))
		}
		// Counted here, before any worker starts, so a chunk that is abandoned
		// (cancelled run, a scan wait that ended in an error) leaves Selected below
		// Listed instead of silently shrinking both.
		tally.mu.Lock()
		tally.res.Listed += len(refs)
		tally.mu.Unlock()

		before := tally.snapshot()
		err = runReparsePass(ctx, env, refs, dryRun, tally)
		for _, r := range refs {
			visited[r] = struct{}{}
		}
		if err != nil || dryRun {
			return finishReparse(env, tally, dryRun, err)
		}
		after := tally.snapshot()
		if after.ParseWrites == before.ParseWrites && after.Gone == before.Gone {
			break
		}
		if pass >= reparseMaxPasses {
			return finishReparse(env, tally, dryRun, fmt.Errorf("%s: passes kept producing writes: after %d passes the library still held rows that needed a parse write or had moved; run it again", reparseFileIntrosID, pass))
		}
	}
	return finishReparse(env, tally, dryRun, nil)
}

// finishReparse reports the run's summary and shapes its error.
func finishReparse(env reparseFileIntrosEnv, tally *reparseTally, dryRun bool, err error) (reparseFileIntrosResult, error) {
	mark := introModeMarker(dryRun)
	reporter := env.reporter
	log := reporter.Logger()
	res := tally.snapshot()
	id := res.Identity
	summary := fmt.Sprintf("%s%d files listed, %d selected, %d examined, %d re-parsed, %d could not be re-parsed; %d parse writes (%d bytes of rows, %d not confirmed durable); title announcements: %d agree, %d would fill, %d conflict; %d stored titles the grammar rejects; %d gone, %d errors",
		mark, res.Listed, res.Selected, res.Examined, res.Reparsed, res.Unreparsed, res.ParseWrites, res.ParseWriteBytes, res.WritesUnconfirmed, id.Agree, id.WouldFill, id.Conflict, id.StoredTitleJunk, res.Gone, res.Errors)
	log.Info(reparseFileIntrosID+": finished", "summary", summary, "err", err)
	_ = reporter.UpdateProgress(1, 1, summary)
	if res.Errors > 0 {
		// err is non-nil here: a chunk with failures returns an error.
		return res, fmt.Errorf("%s: %d files could not be read or written; they keep their stored values and a repeat run retries them: %w", reparseFileIntrosID, res.Errors, errors.Join(err))
	}
	if res.WritesUnconfirmed > 0 && err == nil {
		// Not an error row: the writes were applied. The database could not say they
		// are durable, which an operator should hear, so the run still ends failed.
		return res, fmt.Errorf("%s: %d parse writes were applied but the database did not confirm them durable; a repeat run rewrites nothing that did land", reparseFileIntrosID, res.WritesUnconfirmed)
	}
	return res, err
}

// runReparsePass visits refs in disjoint chunks on a bounded pool and folds the
// outcome into tally. Chunks are disjoint sets of files and each write is a
// per-row compare-and-swap, so workers cannot touch the same row.
func runReparsePass(ctx context.Context, env reparseFileIntrosEnv, refs []reparseRef, dryRun bool, tally *reparseTally) error {
	mark := introModeMarker(dryRun)
	reporter := env.reporter
	log := reporter.Logger()
	live := !dryRun

	chunks := make([][]reparseRef, 0, (len(refs)+reparseChunkSize-1)/reparseChunkSize)
	for i := 0; i < len(refs); i += reparseChunkSize {
		chunks = append(chunks, refs[i:min(i+reparseChunkSize, len(refs))])
	}
	var chunksDone atomic.Int64

	workers := env.workers
	if workers < 1 {
		workers = runtime.NumCPU()
	}
	opts := registry.RunItemsOptions{
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
	return registry.RunItems(ctx, reporter, chunks, func(ctx context.Context, refs []reparseRef) error {
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
		// A chunk with a failed read or write is reported as failed, so the run
		// ends with an error and a repeat run retries the file.
		if local.Errors > 0 {
			return fmt.Errorf("%d of %d files in this chunk could not be read or written", local.Errors, len(refs))
		}
		chunksDone.Add(1)
		return nil
	}, opts)
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
	if out.write && !live {
		// A preview's size of the row as a write would store it, within 10 bytes (the
		// timestamp the write will stamp is not known yet). A live run sizes the row
		// the writer re-reads under its lock instead, below, and does not encode a
		// row (fingerprint and all) twice.
		n, merr := database.IntroParseRowBytes(row, out.parse, env.stamp())
		if merr != nil {
			log.Warn(reparseFileIntrosID+": could not size the row", "file_id", ref.fileID, "err", merr)
			local.Errors++
			return
		}
		out.rowBytes = int64(n)
	}
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
	// The size reported for a write is the fresh row's, encoded as the store will
	// write it (SizeWith), so it is the row that was actually stored.
	var last reparseRow
	ran := false
	outcome, werr := env.writer.ReparseBookFileIntro(ref.bookID, ref.fileID, func(fresh database.IntroReparseRow) (*database.IntroParse, bool) {
		last, ran = reparseDecide(fresh), true
		if !last.write {
			return nil, false
		}
		p := last.parse
		last.rowBytes = 0
		if fresh.SizeWith != nil {
			n, serr := fresh.SizeWith(p)
			if serr != nil {
				log.Warn(reparseFileIntrosID+": could not size the row", "file_id", ref.fileID, "err", serr)
			}
			last.rowBytes = int64(n)
		} else if n, serr := database.IntroParseRowBytes(row, p, env.stamp()); serr == nil {
			// A store that cannot size the fresh row: the pre-lock row is the best
			// there is, within 10 bytes.
			last.rowBytes = int64(n)
		}
		return &p, true
	})
	switch {
	case outcome == database.IntroReparseWrote && werr != nil:
		// Applied, durability unknown (the store says so with the outcome): the row
		// carries the new parse, so it is counted as written and separately as
		// unconfirmed, and is not an error row.
		log.Warn(reparseFileIntrosID+": write applied but not confirmed durable", "file_id", ref.fileID, "err", werr)
		tally.changed.Add(1)
		local.WritesUnconfirmed++
		local.count(last, ref.fileID)
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
		last.rowBytes = 0
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
// a scan, import, folder auto-scan or bulk apply is queued or running, and refuses
// when the queue cannot be read. A point-in-time check; the wait before every chunk
// covers one that starts later.
func reparseRefuseIfBusy(queue OpQueueReader) error {
	if queue == nil {
		return fmt.Errorf("%s: cannot verify no library scan is active; refusing to run", reparseFileIntrosID)
	}
	active, err := queue.ListActiveOperationsV2()
	if err != nil {
		return fmt.Errorf("%s: cannot list active operations; refusing to run: %w", reparseFileIntrosID, err)
	}
	if def := fileIntroBusyDef(active); def != "" {
		return fmt.Errorf("%s: %s is queued or running; refusing to start: this op does not write alongside an op that rewrites book_file rows in bulk, whose writers have not been checked against the intro-parse field. Run it when that op has finished", reparseFileIntrosID, def)
	}
	return nil
}
