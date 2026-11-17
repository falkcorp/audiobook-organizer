// file: internal/plugins/maintenance/file_intro_reparse_test.go
// version: 1.0.1
// guid: 2e6b9d14-8a37-4c51-b0f2-d95a1c3e7840
// last-edited: 2026-11-17

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// Invented text only: Batman titles, Batman characters.
const (
	rpNarr    = " Bruce Wayne walked down the long corridor of the old manor while the rain kept falling on the roof of the cave"
	rpTitle   = "The Killing Joke, written by Alfred Pennyworth, read by Selina Kyle"
	rpPresent = "Gotham Audio presents, Year One, written by Alfred Pennyworth, read by Selina Kyle"
	rpChapter = "Chapter 12. The Long Halloween." + rpNarr
)

func rpStr(s string) *string { return &s }

// reparseFakeStore is the op's whole world: the listing, the full rows and the
// per-row writer. ReparseBookFileIntro mirrors the real writer's contract: decide
// sees the row as it is now, a nil transcript is never offered, the patch writes
// only the parse and the columns it names.
type reparseFakeStore struct {
	mu     sync.Mutex
	rows   map[string]*database.BookFile
	writes map[string]int
	// failWrite lists "book/file" keys whose write returns an error.
	failWrite map[string]bool
	// beforeDecide runs once per key, under no lock, before the writer's first
	// decide: the point where a concurrent writer can change the row.
	beforeDecide func(s *reparseFakeStore, key string)
	raced        map[string]bool
	decides      map[string]int
	// rendezvous, when set, makes a write wait (bounded) until that many writes
	// are in flight at once; maxInflight is the most seen.
	rendezvous   int
	rendezvoused atomic.Bool
	inflight     atomic.Int64
	maxInflight  atomic.Int64
	// onWrite sees every committed write (called under the store lock).
	onWrite func()
}

func newReparseFakeStore() *reparseFakeStore {
	return &reparseFakeStore{
		rows: map[string]*database.BookFile{}, writes: map[string]int{},
		failWrite: map[string]bool{}, raced: map[string]bool{}, decides: map[string]int{},
	}
}

func rpKey(book, file string) string { return book + "/" + file }

func (s *reparseFakeStore) put(f *database.BookFile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *f
	s.rows[rpKey(f.BookID, f.ID)] = &cp
}

func (s *reparseFakeStore) get(book, file string) database.BookFile {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.rows[rpKey(book, file)]
}

func (s *reparseFakeStore) totalWrites() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, w := range s.writes {
		n += w
	}
	return n
}

func (s *reparseFakeStore) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]database.BookFileCore, 0, len(s.rows))
	for _, r := range s.rows {
		out = append(out, r.Core())
	}
	return out, nil
}

func (s *reparseFakeStore) GetBookFileByID(book, file string) (*database.BookFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[rpKey(book, file)]
	if !ok {
		return nil, nil
	}
	cp := *r
	return &cp, nil
}

func (s *reparseFakeStore) ReparseBookFileIntro(book, file string, decide func(database.IntroReparseRow) (database.IntroReparsePatch, bool)) (database.IntroReparseOutcome, error) {
	key := rpKey(book, file)
	n := s.inflight.Add(1)
	defer s.inflight.Add(-1)
	for {
		m := s.maxInflight.Load()
		if n <= m || s.maxInflight.CompareAndSwap(m, n) {
			break
		}
	}
	if s.rendezvous > 0 && !s.rendezvoused.Swap(true) { // only the first write waits
		deadline := time.Now().Add(2 * time.Second)
		for s.inflight.Load() < int64(s.rendezvous) && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}
	s.mu.Lock()
	hook := s.beforeDecide
	first := !s.raced[key]
	s.raced[key] = true
	s.mu.Unlock()
	if hook != nil && first {
		hook(s, key)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[key]
	if !ok {
		return database.IntroReparseGone, nil
	}
	if row.IntroTranscription == nil || *row.IntroTranscription == "" {
		return database.IntroReparseNoTranscript, nil
	}
	if s.failWrite[key] {
		return database.IntroReparseSkipped, errors.New("injected write failure")
	}
	s.decides[key]++
	cur := database.IntroReparseRow{Transcript: *row.IntroTranscription, Parse: row.IntroParse}
	if row.TranscribeStatus != nil {
		cur.Status = *row.TranscribeStatus
	}
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	cur.Identity = database.IntroReparseIdentity{
		Title: deref(row.TranscribedTitle), Author: deref(row.TranscribedAuthor), Narrator: deref(row.TranscribedNarrator),
		Translator: deref(row.TranscribedTranslator), CoverArtist: deref(row.TranscribedCoverArtist),
	}
	patch, ok := decide(cur)
	if !ok {
		return database.IntroReparseSkipped, nil
	}
	if patch.Parse == nil {
		return database.IntroReparseSkipped, errors.New("patch without a parse")
	}
	parse := *patch.Parse
	row.IntroParse = &parse
	if id := patch.Identity; id != nil {
		opt := func(v string) *string {
			if v == "" {
				return nil
			}
			return &v
		}
		row.TranscribedTitle, row.TranscribedAuthor, row.TranscribedNarrator = opt(id.Title), opt(id.Author), opt(id.Narrator)
		row.TranscribedTranslator, row.TranscribedCoverArtist = opt(id.Translator), opt(id.CoverArtist)
	}
	if patch.Status != "" {
		st := patch.Status
		row.TranscribeStatus = &st
	}
	s.writes[key]++
	if s.onWrite != nil {
		s.onWrite()
	}
	return database.IntroReparseWrote, nil
}

// reparseReader hides the writer half so a test can hand the env a store that
// can read but not write.
type reparseReader struct{ s *reparseFakeStore }

func (r reparseReader) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return r.s.GetAllBookFilesCore()
}
func (r reparseReader) GetBookFileByID(b, f string) (*database.BookFile, error) {
	return r.s.GetBookFileByID(b, f)
}

type reparseQueue struct {
	active []database.OperationV2Row
	err    error
}

func (q reparseQueue) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	return q.active, q.err
}

// reparseCkptReporter records every Checkpoint state, safely for the pool.
type reparseCkptReporter struct {
	fakeReporter
	pmu    sync.Mutex
	states []reparseFileIntrosParams
}

func (r *reparseCkptReporter) Checkpoint(state any) error {
	r.pmu.Lock()
	defer r.pmu.Unlock()
	p, ok := state.(reparseFileIntrosParams)
	if !ok {
		return fmt.Errorf("checkpoint state is %T, want reparseFileIntrosParams", state)
	}
	r.states = append(r.states, p)
	return nil
}

func (r *reparseCkptReporter) snapshot() []reparseFileIntrosParams {
	r.pmu.Lock()
	defer r.pmu.Unlock()
	return append([]reparseFileIntrosParams(nil), r.states...)
}

func newReparseEnv(s *reparseFakeStore, rep sdk.Reporter) reparseFileIntrosEnv {
	return reparseFileIntrosEnv{reads: s, writer: s, queue: reparseQueue{}, reporter: rep, workers: 4}
}

// intro is one stored per-file intro row.
func rpIntro(book, file, text, status string, id database.IntroReparseIdentity, parse *database.IntroParse) *database.BookFile {
	f := &database.BookFile{
		ID: file, BookID: book, FilePath: "/lib/" + book + "/" + file + ".m4b",
		IntroSourceHash: "hash-" + file, IntroParse: parse,
	}
	if text != "" {
		f.IntroTranscription = rpStr(text)
	}
	if status != "" {
		f.TranscribeStatus = rpStr(status)
	}
	set := func(dst **string, v string) {
		if v != "" {
			*dst = rpStr(v)
		}
	}
	set(&f.TranscribedTitle, id.Title)
	set(&f.TranscribedAuthor, id.Author)
	set(&f.TranscribedNarrator, id.Narrator)
	return f
}

var rpNone = database.IntroReparseIdentity{}

// TestReparseDecide is the table for the glue between ParseClip, the
// no-downgrade rule and the stored shape. Each row names what it pins.
func TestReparseDecide(t *testing.T) {
	cur := func(text, status string, id database.IntroReparseIdentity, p *database.IntroParse) database.IntroReparseRow {
		return database.IntroReparseRow{Transcript: text, Status: status, Identity: id, Parse: p}
	}
	title := transcribe.ParseClip(rpTitle)
	titleStored := introParseFromClip(title)
	newer := database.IntroParse{Version: transcribe.ClipParserVersion + 1, Kind: "narration"}

	cases := []struct {
		name         string
		row          database.IntroReparseRow
		unreparsable string
		note         string
		before       string
		after        string
		write        bool
		wantStatus   string
		wantTitle    string
		kindConflict bool
		identityNil  bool
	}{
		{
			name: "unparsed row with an announcement fills identity and becomes ok",
			row:  cur(rpTitle, "unparsed", rpNone, nil),
			// the stored kind before is "unclassified" for a row never classified
			before: reparseKindUnclassified, after: "title_announcement", write: true,
			wantStatus: "ok", wantTitle: "The Killing Joke",
		},
		{
			name:   "up to date row is not written",
			row:    cur(rpTitle, "ok", database.IntroReparseIdentity{Title: "The Killing Joke", Author: "Alfred Pennyworth", Narrator: "Selina Kyle"}, &titleStored),
			before: "title_announcement", after: "title_announcement", write: false, identityNil: true,
		},
		{
			name:   "identity present and the clip is a chapter marker keeps identity",
			row:    cur(rpChapter, "ok", database.IntroReparseIdentity{Title: "Hush"}, nil),
			before: reparseKindUnclassified, after: "chapter_marker", write: true, kindConflict: true,
			identityNil: true,
		},
		{
			name:   "publisher lead in a stored title is repaired",
			row:    cur(rpPresent, "ok", database.IntroReparseIdentity{Title: "Gotham Audio presents, Year One"}, nil),
			before: reparseKindUnclassified, after: "title_announcement", write: true, wantTitle: "Year One",
			wantStatus: "",
		},
		{
			name:         "whitespace transcript cannot be re-parsed",
			row:          cur("  \n ", "unparsed", rpNone, nil),
			unreparsable: reparseBlankTranscript, before: reparseKindUnclassified, after: reparseKindUnclassified,
		},
		{
			name:         "a parse written by a newer grammar is never overwritten",
			row:          cur(rpTitle, "ok", rpNone, &newer),
			unreparsable: reparseNewerParser, before: "narration", after: "narration",
		},
		{
			name:   "failure status keeps identity and status, parse is written",
			row:    cur(rpTitle, "whisper_error", database.IntroReparseIdentity{Title: "Hush"}, nil),
			note:   reparseFrozenByStatus,
			before: reparseKindUnclassified, after: "title_announcement", write: true, identityNil: true,
		},
		{
			name:   "silence sentinel is a silence verdict with nothing to extract",
			row:    cur(transcribe.SilenceSentinel, "empty", rpNone, nil),
			note:   reparseSilenceOnly,
			before: reparseKindUnclassified, after: "music_or_silence", write: true, identityNil: true,
		},
		{
			name:   "a parse from an older grammar that no longer matches is rewritten",
			row:    cur(rpChapter, "ok", rpNone, &database.IntroParse{Version: 1, Kind: "garbage"}),
			before: "garbage", after: "chapter_marker", write: true, identityNil: true,
		},
		{
			name:   "narration on an unparsed row stays unparsed",
			row:    cur(rpNarr, "unparsed", rpNone, nil),
			before: reparseKindUnclassified, after: "narration", write: true, identityNil: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reparseDecide(tc.row)
			if got.unreparsable != tc.unreparsable || got.note != tc.note {
				t.Fatalf("unreparsable=%q note=%q, want %q / %q", got.unreparsable, got.note, tc.unreparsable, tc.note)
			}
			if got.before != tc.before || got.after != tc.after {
				t.Fatalf("kind %s -> %s, want %s -> %s", got.before, got.after, tc.before, tc.after)
			}
			if got.write != tc.write {
				t.Fatalf("write = %v, want %v", got.write, tc.write)
			}
			if got.dec.KindConflict != tc.kindConflict {
				t.Errorf("kindConflict = %v, want %v", got.dec.KindConflict, tc.kindConflict)
			}
			if !got.write {
				if got.patch.Parse != nil {
					t.Errorf("a row that is not written carries a patch: %+v", got.patch)
				}
				return
			}
			if got.patch.Parse == nil {
				t.Fatal("a written row has no parse in its patch")
			}
			if tc.identityNil && got.patch.Identity != nil {
				t.Errorf("identity patch = %+v, want none", got.patch.Identity)
			}
			if tc.wantTitle != "" && (got.patch.Identity == nil || got.patch.Identity.Title != tc.wantTitle) {
				t.Errorf("identity patch = %+v, want title %q", got.patch.Identity, tc.wantTitle)
			}
			if got.patch.Status != tc.wantStatus {
				t.Errorf("status patch = %q, want %q", got.patch.Status, tc.wantStatus)
			}
		})
	}
}

// The decision is idempotent: applying its patch and deciding again writes
// nothing. This is what makes a repeat run and a resumed run safe.
func TestReparseDecide_IsIdempotent(t *testing.T) {
	for _, text := range []string{rpTitle, rpPresent, rpChapter, rpNarr, transcribe.SilenceSentinel} {
		for _, status := range []string{"ok", "unparsed", "empty", "whisper_error"} {
			cur := database.IntroReparseRow{Transcript: text, Status: status}
			first := reparseDecide(cur)
			if !first.write {
				t.Fatalf("%q/%s: a never-classified row must be written", text[:min(len(text), 20)], status)
			}
			cur.Parse = first.patch.Parse
			if id := first.patch.Identity; id != nil {
				cur.Identity = *id
			}
			if first.patch.Status != "" {
				cur.Status = first.patch.Status
			}
			if second := reparseDecide(cur); second.write {
				t.Errorf("%q/%s: second pass still writes: %+v", text[:min(len(text), 20)], status, second.patch)
			}
		}
	}
}

// The status constants this package compares with are the ones the grammar's
// rule uses; a drift would make "ok" mean two things.
func TestReparseStatusVocabularyAgrees(t *testing.T) {
	if statusOK != transcribe.StatusOK || statusUnparsed != transcribe.StatusUnparsed {
		t.Fatalf("maintenance statuses %q/%q, transcribe %q/%q", statusOK, statusUnparsed, transcribe.StatusOK, transcribe.StatusUnparsed)
	}
	if database.IntroStatusOK != transcribe.StatusOK || database.IntroStatusUnparsed != transcribe.StatusUnparsed {
		t.Fatalf("store statuses %q/%q, transcribe %q/%q", database.IntroStatusOK, database.IntroStatusUnparsed, transcribe.StatusOK, transcribe.StatusUnparsed)
	}
}

// seedMixed is one row of every class the op reports, in book b1, plus a file
// with no intro field at all (never selected).
func seedMixed(s *reparseFakeStore) {
	uptodate := introParseFromClip(transcribe.ParseClip(rpTitle))
	newer := database.IntroParse{Version: transcribe.ClipParserVersion + 1, Kind: "narration"}
	s.put(rpIntro("b1", "f01", rpTitle, "unparsed", rpNone, nil))                                                                                                         // fills 3, unparsed->ok
	s.put(rpIntro("b1", "f02", rpChapter, "ok", database.IntroReparseIdentity{Title: "Hush"}, nil))                                                                       // kind conflict kept
	s.put(rpIntro("b1", "f03", rpTitle, "ok", database.IntroReparseIdentity{Title: "The Killing Joke", Author: "Alfred Pennyworth", Narrator: "Selina Kyle"}, &uptodate)) // no write
	s.put(rpIntro("b1", "f04", "", "whisper_error", rpNone, nil))                                                                                                         // no transcript
	s.put(rpIntro("b1", "f05", "   ", "unparsed", rpNone, nil))                                                                                                           // blank
	s.put(rpIntro("b1", "f06", rpChapter, "whisper_error", rpNone, nil))                                                                                                  // frozen by status
	s.put(rpIntro("b1", "f07", rpTitle, "ok", rpNone, &newer))                                                                                                            // newer grammar
	s.put(rpIntro("b1", "f08", transcribe.SilenceSentinel, "empty", rpNone, nil))                                                                                         // silence only
	s.put(rpIntro("b1", "f09", rpPresent, "ok", database.IntroReparseIdentity{Title: "Gotham Audio presents, Year One"}, nil))                                            // repaired
	s.put(&database.BookFile{ID: "f10", BookID: "b1", FilePath: "/lib/b1/f10.m4b"})                                                                                       // no intro field
}

func wantMixed(t *testing.T, res reparseFileIntrosResult) {
	t.Helper()
	check := func(name string, got, want int) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %d, want %d", name, got, want)
		}
	}
	check("examined", res.Examined, 9)
	check("reparsed", res.Reparsed, 6)
	check("unreparsable", res.Unreparsed, 3)
	check("unreparsable no_transcript", res.Unreparsable[reparseNoTranscript], 1)
	check("unreparsable blank", res.Unreparsable[reparseBlankTranscript], 1)
	check("unreparsable newer", res.Unreparsable[reparseNewerParser], 1)
	check("note frozen", res.Notes[reparseFrozenByStatus], 1)
	check("note silence", res.Notes[reparseSilenceOnly], 1)
	// f01, f02, f06, f08, f09 are written; f03 is up to date.
	check("parse_writes", res.ParseWrites, 5)
	check("identity_writes", res.IdentityWrites, 2) // f01 (fill), f09 (repair + fill)
	check("status promoted", res.StatusPromoted, 1)
	check("kind conflicts", res.KindConflicts, 1)
	check("kind_before unclassified", res.KindBefore[reparseKindUnclassified], 5)
	check("kind_before title", res.KindBefore["title_announcement"], 1)
	check("kind_after title", res.KindAfter["title_announcement"], 3) // f01, f03, f09
	check("kind_after chapter", res.KindAfter["chapter_marker"], 2)   // f02, f06
	check("kind_after silence", res.KindAfter["music_or_silence"], 1) // f08
	check("write_errors", res.WriteErrors, 0)
}

// A preview writes nothing, and reports what a live run would store.
func TestReparseFileIntros_PreviewWritesNothingAndReports(t *testing.T) {
	s := newReparseFakeStore()
	seedMixed(s)
	// A preview needs neither a writer nor a quiet queue.
	env := reparseFileIntrosEnv{reads: reparseReader{s}, reporter: &fakeReporter{}, workers: 4}

	res, err := runReparseFileIntros(context.Background(), env, 0, true)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !res.DryRun {
		t.Error("result does not say it was a dry run")
	}
	wantMixed(t, res)
	if n := s.totalWrites(); n != 0 {
		t.Fatalf("preview wrote %d rows", n)
	}
	if got := s.get("b1", "f01"); got.IntroParse != nil || *got.TranscribeStatus != "unparsed" {
		t.Errorf("preview changed f01: %+v", got)
	}
}

// A live run stores exactly what the preview reported, and a second live run
// changes nothing.
func TestReparseFileIntros_LiveMatchesPreviewAndIsIdempotent(t *testing.T) {
	s := newReparseFakeStore()
	seedMixed(s)
	env := newReparseEnv(s, &fakeReporter{})

	res, err := runReparseFileIntros(context.Background(), env, 0, false)
	if err != nil {
		t.Fatalf("live: %v", err)
	}
	if res.DryRun {
		t.Error("live result claims dry run")
	}
	wantMixed(t, res)
	if n := s.totalWrites(); n != 5 {
		t.Fatalf("live wrote %d rows, want 5", n)
	}

	f01 := s.get("b1", "f01")
	if f01.TranscribeStatus == nil || *f01.TranscribeStatus != "ok" || f01.TranscribedTitle == nil || *f01.TranscribedTitle != "The Killing Joke" ||
		f01.TranscribedAuthor == nil || *f01.TranscribedAuthor != "Alfred Pennyworth" || f01.TranscribedNarrator == nil || *f01.TranscribedNarrator != "Selina Kyle" {
		t.Errorf("f01 identity/status after live run: %+v", f01)
	}
	if f01.IntroParse == nil || f01.IntroParse.Kind != "title_announcement" || f01.IntroParse.Version != transcribe.ClipParserVersion {
		t.Errorf("f01 parse = %+v", f01.IntroParse)
	}
	// The no-downgrade rule: f02's identity survives a chapter-marker clip.
	if f02 := s.get("b1", "f02"); f02.TranscribedTitle == nil || *f02.TranscribedTitle != "Hush" || f02.IntroParse.Kind != "chapter_marker" || f02.IntroParse.ChapterNumber != 12 {
		t.Errorf("f02 = title %v parse %+v", f02.TranscribedTitle, f02.IntroParse)
	}
	// A failure status is frozen: parse yes, identity and status no.
	if f06 := s.get("b1", "f06"); *f06.TranscribeStatus != "whisper_error" || f06.TranscribedTitle != nil || f06.IntroParse == nil {
		t.Errorf("f06 = status %v title %v parse %+v", f06.TranscribeStatus, f06.TranscribedTitle, f06.IntroParse)
	}
	// A newer parse is untouched, a row with no transcript still has none.
	if f07 := s.get("b1", "f07"); f07.IntroParse.Version != transcribe.ClipParserVersion+1 {
		t.Errorf("f07 parse overwritten: %+v", f07.IntroParse)
	}
	if f04 := s.get("b1", "f04"); f04.IntroParse != nil || f04.IntroTranscription != nil {
		t.Errorf("f04 changed: %+v", f04)
	}
	// Provenance is not the op's to change.
	if f01.IntroSourceHash != "hash-f01" {
		t.Errorf("f01 done marker changed: %q", f01.IntroSourceHash)
	}

	again, err := runReparseFileIntros(context.Background(), env, 0, false)
	if err != nil {
		t.Fatalf("second live run: %v", err)
	}
	if again.ParseWrites != 0 || again.IdentityWrites != 0 || again.StatusPromoted != 0 {
		t.Errorf("second run still writes: %+v", again)
	}
	if n := s.totalWrites(); n != 5 {
		t.Errorf("second run wrote %d more rows", n-5)
	}
	if again.KindBefore["chapter_marker"] != 2 || again.KindBefore[reparseKindUnclassified] != 0 {
		t.Errorf("second run's before-counts = %v", again.KindBefore)
	}
}

// A live run refuses to start unless it can write and the queue is clear. A
// preview never needs either.
func TestReparseFileIntros_LiveStartGates(t *testing.T) {
	s := newReparseFakeStore()
	seedMixed(s)
	ctx := context.Background()

	noWriter := reparseFileIntrosEnv{reads: reparseReader{s}, queue: reparseQueue{}, reporter: &fakeReporter{}}
	if _, err := runReparseFileIntros(ctx, noWriter, 0, false); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Errorf("no writer: err = %v", err)
	}
	noQueue := newReparseEnv(s, &fakeReporter{})
	noQueue.queue = nil
	if _, err := runReparseFileIntros(ctx, noQueue, 0, false); err == nil || !strings.Contains(err.Error(), "cannot verify") {
		t.Errorf("nil queue: err = %v", err)
	}
	broken := newReparseEnv(s, &fakeReporter{})
	broken.queue = reparseQueue{err: errors.New("queue down")}
	if _, err := runReparseFileIntros(ctx, broken, 0, false); err == nil || !strings.Contains(err.Error(), "refusing to run") {
		t.Errorf("unreadable queue: err = %v", err)
	}
	busy := newReparseEnv(s, &fakeReporter{})
	busy.queue = reparseQueue{active: []database.OperationV2Row{{DefID: "library.scan", Status: "running"}}}
	if _, err := runReparseFileIntros(ctx, busy, 0, false); err == nil || !strings.Contains(err.Error(), "library.scan") {
		t.Errorf("scan running: err = %v", err)
	}
	if n := s.totalWrites(); n != 0 {
		t.Fatalf("a refused run wrote %d rows", n)
	}
	// The same busy queue does not stop a preview.
	prev := newReparseEnv(s, &fakeReporter{})
	prev.queue = busy.queue
	if _, err := runReparseFileIntros(ctx, prev, 0, true); err != nil {
		t.Errorf("preview refused: %v", err)
	}
}

// seedMany stores n narration rows in book b1, ids sorted by their number.
func seedMany(s *reparseFakeStore, n int) {
	for i := range n {
		s.put(rpIntro("b1", fmt.Sprintf("f%05d", i), rpNarr, "unparsed", rpNone, nil))
	}
}

// The checkpoint carries every param, dry_run included, and its watermark is the
// absolute chunk count. Without dry_run it comes back as the PREVIEW default on
// a resumed run.
func TestReparseFileIntros_CheckpointCarriesDryRunFalse(t *testing.T) {
	s := newReparseFakeStore()
	n := reparseChunkSize*3 + 5 // 4 chunks
	seedMany(s, n)
	rep := &reparseCkptReporter{}

	res, err := runReparseFileIntros(context.Background(), newReparseEnv(s, rep), 0, false)
	if err != nil {
		t.Fatalf("live: %v", err)
	}
	if res.Examined != n || res.ParseWrites != n {
		t.Fatalf("examined %d, writes %d, want %d / %d", res.Examined, res.ParseWrites, n, n)
	}
	states := rep.snapshot()
	if len(states) == 0 {
		t.Fatal("a live ResumeRestart op wrote no checkpoint")
	}
	last := states[len(states)-1]
	if last.ResumeFrom != 4 {
		t.Errorf("final watermark = %d, want 4 chunks", last.ResumeFrom)
	}
	for _, st := range states {
		if st.DryRun == nil || *st.DryRun {
			t.Fatalf("checkpoint %+v does not carry dry_run=false", st)
		}
	}
	// And it round-trips the way the registry merges it back into the params.
	raw, err := json.Marshal(last)
	if err != nil {
		t.Fatal(err)
	}
	var back reparseFileIntrosParams
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	dry, err := opmode.ResolveDryRun(reparseFileIntrosID, back.DryRun, back.DryRunCamel)
	if err != nil || dry {
		t.Errorf("a resumed run reads dry_run=%v err=%v from %s, want false", dry, err, raw)
	}
}

// A preview writes no checkpoint at all.
func TestReparseFileIntros_PreviewWritesNoCheckpoint(t *testing.T) {
	s := newReparseFakeStore()
	seedMany(s, reparseChunkSize+1)
	rep := &reparseCkptReporter{}
	if _, err := runReparseFileIntros(context.Background(), reparseFileIntrosEnv{reads: s, reporter: rep, workers: 2}, 0, true); err != nil {
		t.Fatal(err)
	}
	if got := rep.snapshot(); len(got) != 0 {
		t.Errorf("preview wrote checkpoints: %+v", got)
	}
}

// A live resume skips the chunks below the watermark; a preview counts them all
// (a partial count would be a wrong report); a watermark past the end is clamped.
func TestReparseFileIntros_ResumeFromWatermark(t *testing.T) {
	ctx := context.Background()
	n := reparseChunkSize*3 + 5

	s := newReparseFakeStore()
	seedMany(s, n)
	res, err := runReparseFileIntros(ctx, newReparseEnv(s, &fakeReporter{}), 2, false)
	if err != nil {
		t.Fatal(err)
	}
	skipped := 2 * reparseChunkSize
	if res.Examined != n-skipped || s.totalWrites() != n-skipped {
		t.Errorf("resume from 2: examined %d writes %d, want %d", res.Examined, s.totalWrites(), n-skipped)
	}
	if first := s.get("b1", "f00000"); first.IntroParse != nil {
		t.Error("a file below the watermark was written")
	}
	if last := s.get("b1", fmt.Sprintf("f%05d", n-1)); last.IntroParse == nil {
		t.Error("a file above the watermark was not written")
	}

	p := newReparseFakeStore()
	seedMany(p, n)
	pres, err := runReparseFileIntros(ctx, reparseFileIntrosEnv{reads: p, reporter: &fakeReporter{}, workers: 2}, 2, true)
	if err != nil || pres.Examined != n {
		t.Errorf("preview with a watermark: examined %d err %v, want all %d", pres.Examined, err, n)
	}

	c := newReparseFakeStore()
	seedMany(c, 10)
	cres, err := runReparseFileIntros(ctx, newReparseEnv(c, &fakeReporter{}), 99, false)
	if err != nil || cres.Examined != 0 || c.totalWrites() != 0 {
		t.Errorf("watermark past the end: examined %d writes %d err %v", cres.Examined, c.totalWrites(), err)
	}
}

// A file that fails to write leaves its row as stored, makes the run fail, and
// holds the watermark below its chunk so a resumed attempt retries it. Chunks
// after it still run.
func TestReparseFileIntros_FailedWriteHoldsTheWatermark(t *testing.T) {
	s := newReparseFakeStore()
	n := reparseChunkSize*4 + 1
	seedMany(s, n)
	bad := fmt.Sprintf("f%05d", reparseChunkSize+3) // inside chunk 1
	s.failWrite[rpKey("b1", bad)] = true
	rep := &reparseCkptReporter{}

	res, err := runReparseFileIntros(context.Background(), newReparseEnv(s, rep), 0, false)
	if err == nil {
		t.Fatal("a failed write did not fail the run")
	}
	if res.WriteErrors != 1 {
		t.Errorf("write_errors = %d, want 1", res.WriteErrors)
	}
	if row := s.get("b1", bad); row.IntroParse != nil {
		t.Error("the failing row was written")
	}
	if want := n - 1; s.totalWrites() != want {
		t.Errorf("writes = %d, want every other file (%d)", s.totalWrites(), want)
	}
	for _, st := range rep.snapshot() {
		if st.ResumeFrom > 1 {
			t.Fatalf("watermark %d passed the failed chunk 1", st.ResumeFrom)
		}
	}
	// The failed row counts as unchanged in the after-counts.
	if res.KindAfter["unclassified"] != 1 {
		t.Errorf("kind_after = %v, want the failed row still unclassified", res.KindAfter)
	}

	// A repeat run with the fault cleared finishes it.
	delete(s.failWrite, rpKey("b1", bad))
	res2, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), 0, false)
	if err != nil || res2.ParseWrites != 1 {
		t.Errorf("repeat run: writes %d err %v, want exactly the one that failed", res2.ParseWrites, err)
	}
}

// The writer's decision is made on the row as it is when the write happens. A
// concurrent change of the text between the op's read and the write is judged
// afresh and counted from the fresh decision.
func TestReparseFileIntros_DecidesOnTheFreshRow(t *testing.T) {
	s := newReparseFakeStore()
	s.put(rpIntro("b1", "f01", rpNarr, "unparsed", rpNone, nil))
	s.beforeDecide = func(st *reparseFakeStore, key string) {
		st.mu.Lock()
		defer st.mu.Unlock()
		st.rows[key].IntroTranscription = rpStr(rpTitle) // the text was replaced
	}

	res, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), 0, false)
	if err != nil {
		t.Fatal(err)
	}
	row := s.get("b1", "f01")
	if row.IntroParse == nil || row.IntroParse.Kind != "title_announcement" || row.TranscribedTitle == nil || *row.TranscribedTitle != "The Killing Joke" {
		t.Fatalf("row = parse %+v title %v, want a verdict about the NEW text", row.IntroParse, row.TranscribedTitle)
	}
	if res.KindAfter["title_announcement"] != 1 || res.KindAfter["narration"] != 0 || res.IdentityWrites != 1 {
		t.Errorf("counts follow the stale read: after=%v identity_writes=%d", res.KindAfter, res.IdentityWrites)
	}
}

// Files with no intro field are not selected, and a row deleted between the
// listing and the read is counted, not failed.
func TestReparseFileIntros_SelectionAndVanishedRows(t *testing.T) {
	s := newReparseFakeStore()
	s.put(&database.BookFile{ID: "plain", BookID: "b1"})
	s.put(rpIntro("b1", "f01", rpNarr, "ok", rpNone, nil))
	s.put(rpIntro("b1", "f02", rpNarr, "ok", rpNone, nil))

	// Vanish f02 after the listing.
	gone := &vanishingReads{reparseFakeStore: s, vanish: rpKey("b1", "f02")}
	env := reparseFileIntrosEnv{reads: gone, writer: s, queue: reparseQueue{}, reporter: &fakeReporter{}, workers: 2}
	res, err := runReparseFileIntros(context.Background(), env, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Examined != 1 || res.Gone != 1 {
		t.Errorf("examined %d gone %d, want 1 / 1 (the plain file is never selected)", res.Examined, res.Gone)
	}
}

// vanishingReads lists a row and then reports it deleted.
type vanishingReads struct {
	*reparseFakeStore
	vanish string
}

func (v *vanishingReads) GetBookFileByID(book, file string) (*database.BookFile, error) {
	if rpKey(book, file) == v.vanish {
		return nil, nil
	}
	return v.reparseFakeStore.GetBookFileByID(book, file)
}

// The op declares what the registry and the architecture guards read.
func TestReparseFileIntrosDef(t *testing.T) {
	def := (&Plugin{}).reparseFileIntrosDef()
	if def.ID != "maintenance.reparse-file-intros" {
		t.Errorf("ID = %q", def.ID)
	}
	if def.ResumePolicy != sdk.ResumeRestart || def.Run == nil || !def.Cancellable {
		t.Errorf("resume=%v run-nil=%v cancellable=%v", def.ResumePolicy, def.Run == nil, def.Cancellable)
	}
	caps := map[sdk.Capability]bool{}
	for _, c := range def.Capabilities {
		caps[c] = true
	}
	if !caps[sdk.CapLibraryRead] || !caps[sdk.CapLibraryWrite] {
		t.Errorf("capabilities = %v", def.Capabilities)
	}
	// dry_run omitted means preview, spelled either way.
	var p reparseFileIntrosParams
	if err := json.Unmarshal([]byte(`{}`), &p); err != nil {
		t.Fatal(err)
	}
	if dry, err := opmode.ResolveDryRun(reparseFileIntrosID, p.DryRun, p.DryRunCamel); err != nil || !dry {
		t.Errorf("omitted dry_run resolves to %v (err %v), want preview", dry, err)
	}
	if err := json.Unmarshal([]byte(`{"dry_run": false, "resume_from": 7}`), &p); err != nil {
		t.Fatal(err)
	}
	if dry, _ := opmode.ResolveDryRun(reparseFileIntrosID, p.DryRun, p.DryRunCamel); dry || p.ResumeFrom != 7 {
		t.Errorf("dry_run=false resume_from=7 parsed as dry=%v resume=%d", dry, p.ResumeFrom)
	}
}

// A row the transcription op writes is a fixed point of the re-parse: the same
// grammar produced both, so re-parsing a new row changes nothing. If these two
// drift, every new transcription would show up as work for the next re-parse.
func TestWhisperGroupResult_IsAFixedPointOfTheReparse(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	texts := map[string]string{
		"announcement":     rpTitle,
		"publisher lead":   rpPresent,
		"chapter":          rpChapter,
		"narration":        rpNarr,
		"silence":          "",
		"music tag":        "[Music]",
		"hallucination":    "Thank you for watching!",
		"credits only":     "Narrated by Selina Kyle. Copyright 2019 Wayne Enterprises. All rights reserved.",
		"sentinel by hand": transcribe.SilenceSentinel,
	}
	for name, text := range texts {
		t.Run(name, func(t *testing.T) {
			gr := whisperGroupResult(fileIntroWhisperResult{Text: text, Model: "model-x", Language: "en"}, now)
			r := gr.res
			if r.Parse == nil {
				t.Fatal("a stored transcription carries no classification")
			}
			if r.Parse.Version != transcribe.ClipParserVersion || r.Parse.Kind == "" {
				t.Fatalf("parse = %+v", r.Parse)
			}
			if r.Meta == nil || r.Meta.Model != "model-x" || r.Meta.Language != "en" {
				t.Errorf("meta = %+v, want the worker's model and language carried", r.Meta)
			}
			if r.Transcript == nil {
				t.Fatal("no transcript stored")
			}
			cur := database.IntroReparseRow{
				Transcript: *r.Transcript, Status: r.Status, Parse: r.Parse,
				Identity: database.IntroReparseIdentity{
					Title: r.Title, Author: r.Author, Narrator: r.Narrator, Translator: r.Translator, CoverArtist: r.CoverArtist,
				},
			}
			if got := reparseDecide(cur); got.write {
				t.Errorf("re-parsing a freshly written row would write: %+v", got.patch)
			}
		})
	}
}

// Only a title announcement writes identity; a clip of any other kind stores its
// classification and no identity, and is stored as unparsed.
func TestWhisperGroupResult_IdentityOnlyFromAnAnnouncement(t *testing.T) {
	now := time.Now()
	ann := whisperGroupResult(fileIntroWhisperResult{Text: rpTitle}, now).res
	if ann.Status != statusOK || ann.Title != "The Killing Joke" || ann.Author != "Alfred Pennyworth" || ann.Parse.Kind != "title_announcement" {
		t.Errorf("announcement = status %q title %q author %q kind %q", ann.Status, ann.Title, ann.Author, ann.Parse.Kind)
	}
	ch := whisperGroupResult(fileIntroWhisperResult{Text: rpChapter}, now).res
	if ch.Status != statusUnparsed || ch.Title != "" || ch.Parse.Kind != "chapter_marker" || ch.Parse.ChapterNumber != 12 {
		t.Errorf("chapter = status %q title %q parse %+v", ch.Status, ch.Title, ch.Parse)
	}
}

// The chunks really run in parallel: with a pool of four, at least two writes are
// in flight at once. (A rendezvous in the fake holds each write until a second
// one arrives or two seconds pass, so a sequential run fails rather than hangs.)
func TestReparseFileIntros_ChunksRunInParallel(t *testing.T) {
	s := newReparseFakeStore()
	seedMany(s, reparseChunkSize*4)
	s.rendezvous = 2

	if _, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), 0, false); err != nil {
		t.Fatal(err)
	}
	if got := s.maxInflight.Load(); got < 2 {
		t.Errorf("at most %d writes in flight at once with 4 workers and 4 chunks, want at least 2", got)
	}
}

// A live run waits before every chunk's writes while a scan runs, and writes
// nothing while the queue reports it. The start gate sees a clear queue first.
func TestReparseFileIntros_WaitsForAScanBeforeEveryChunk(t *testing.T) {
	old := fileIntroPauseInterval
	fileIntroPauseInterval = time.Millisecond
	t.Cleanup(func() { fileIntroPauseInterval = old })

	s := newReparseFakeStore()
	seedMany(s, reparseChunkSize*3)
	q := &flippingQueue{}
	s.onWrite = func() {
		if q.busyNow() {
			q.sawWriteWhileBusy.Store(true)
		}
	}
	env := newReparseEnv(s, &fakeReporter{})
	env.queue = q
	env.workers = 3

	if _, err := runReparseFileIntros(context.Background(), env, 0, false); err != nil {
		t.Fatal(err)
	}
	if q.sawWriteWhileBusy.Load() {
		t.Error("a write was committed while a scan was reported running")
	}
	if q.busyAnswers.Load() == 0 {
		t.Fatal("the queue never reported busy: the test did not exercise the wait")
	}
	if s.totalWrites() != reparseChunkSize*3 {
		t.Errorf("writes = %d, want every file once the scan finished", s.totalWrites())
	}
}

// flippingQueue answers clear to the start gate, then busy for a while, then clear
// again, and says whether it is currently reporting busy.
type flippingQueue struct {
	mu                sync.Mutex
	calls             int
	busy              bool
	busyAnswers       atomic.Int64
	sawWriteWhileBusy atomic.Bool
}

func (q *flippingQueue) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls++
	// call 1 is the start gate; calls 2..6 report a running scan.
	q.busy = q.calls >= 2 && q.calls <= 6
	if q.busy {
		q.busyAnswers.Add(1)
		return []database.OperationV2Row{{DefID: "library.scan", Status: "running"}}, nil
	}
	return nil, nil
}

func (q *flippingQueue) busyNow() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.busy
}

// A group that reuses a donor's result copies the donor's classification, as a
// separate value.
func TestDonorResult_CopiesTheClassification(t *testing.T) {
	donor := rpIntro("b1", "donor", rpTitle, "ok", database.IntroReparseIdentity{Title: "The Killing Joke"},
		&database.IntroParse{Version: 1, Kind: "title_announcement", Title: "The Killing Joke"})
	s := newReparseFakeStore()
	s.put(donor)
	w := &fileIntroWorker{store: s}

	gr := w.donorResult(fileIntroGroup{DonorBookID: "b1", DonorFileID: "donor", Hash: donor.IntroSourceHash})
	if !gr.hasResult || gr.res.Parse == nil || gr.res.Parse.Kind != "title_announcement" || gr.res.Parse.Title != "The Killing Joke" {
		t.Fatalf("donor result = %+v", gr.res)
	}
	stored := s.get("b1", "donor")
	if gr.res.Parse == stored.IntroParse {
		t.Error("the copy aliases the donor's stored parse")
	}
}
