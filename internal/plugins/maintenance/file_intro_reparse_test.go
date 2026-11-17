// file: internal/plugins/maintenance/file_intro_reparse_test.go
// version: 2.0.0
// guid: 2e6b9d14-8a37-4c51-b0f2-d95a1c3e7840
// last-edited: 2026-11-17

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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
	// rpFranchise leads with a publisher that carries a franchise term, so the stored
	// title keeps the term and the clip parse drops it.
	rpFranchise = "Big Finish Productions presents, Hush, written by Lucius Fox, read by Selina Kyle"
)

func rpStr(s string) *string { return &s }

// reparseFakeStore is the op's whole world: the listing, the full rows and the
// per-row writer. ReparseBookFileIntro mirrors the real writer's contract: decide
// sees the row as it is now, a nil transcript is never offered, and the only
// column written is IntroParse.
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

func (s *reparseFakeStore) ReparseBookFileIntro(book, file string, decide func(database.IntroReparseRow) (*database.IntroParse, bool)) (database.IntroReparseOutcome, error) {
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
	parse, ok := decide(cur)
	if !ok {
		return database.IntroReparseSkipped, nil
	}
	if parse == nil {
		return database.IntroReparseSkipped, errors.New("decision without a parse")
	}
	p := *parse
	row.IntroParse = &p // the ONLY column this writer assigns
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

// TestReparseDecide is the table for the glue between ParseClip, the stored shape
// and the identity comparison. Each row names what it pins.
func TestReparseDecide(t *testing.T) {
	cur := func(text string, id database.IntroReparseIdentity, p *database.IntroParse) database.IntroReparseRow {
		return database.IntroReparseRow{Transcript: text, Identity: id, Parse: p}
	}
	title := transcribe.ParseClip(rpTitle)
	titleStored := introParseFromClip(title)
	newer := database.IntroParse{Version: transcribe.ClipParserVersion + 1, Kind: "narration"}
	agreeing := database.IntroReparseIdentity{Title: "The Killing Joke", Author: "Alfred Pennyworth", Narrator: "Selina Kyle"}

	cases := []struct {
		name          string
		row           database.IntroReparseRow
		unreparsable  string
		note          string
		before, after string
		write         bool
		verdict       transcribe.IdentityVerdict
		conflict      transcribe.ConflictKind
		franchiseLost bool
		junk          transcribe.JunkReason
	}{
		{
			name:   "never-classified announcement over an empty identity would fill",
			row:    cur(rpTitle, rpNone, nil),
			before: reparseKindUnclassified, after: "title_announcement", write: true, verdict: transcribe.VerdictFill,
		},
		{
			name:   "up to date row is not written and agrees",
			row:    cur(rpTitle, agreeing, &titleStored),
			before: "title_announcement", after: "title_announcement", verdict: transcribe.VerdictAgree,
		},
		{
			name:   "a chapter marker over a stored identity has nothing to compare",
			row:    cur(rpChapter, database.IntroReparseIdentity{Title: "Hush"}, nil),
			before: reparseKindUnclassified, after: "chapter_marker", write: true, verdict: transcribe.VerdictNotAnnouncement,
		},
		{
			name:   "publisher lead in a stored title is a publisher-prefix conflict, not a repair",
			row:    cur(rpPresent, database.IntroReparseIdentity{Title: "Gotham Audio presents, Year One"}, nil),
			before: reparseKindUnclassified, after: "title_announcement", write: true,
			verdict: transcribe.VerdictConflict, conflict: transcribe.ConflictPublisherPrefix,
		},
		{
			name:   "a lost franchise term is split out of the title conflicts",
			row:    cur(rpFranchise, database.IntroReparseIdentity{Title: "Big Finish Productions presents, Hush"}, nil),
			before: reparseKindUnclassified, after: "title_announcement", write: true,
			verdict: transcribe.VerdictConflict, conflict: transcribe.ConflictPublisherPrefix, franchiseLost: true,
		},
		{
			name:   "a different stored title is a different-title conflict",
			row:    cur(rpTitle, database.IntroReparseIdentity{Title: "Hush"}, nil),
			before: reparseKindUnclassified, after: "title_announcement", write: true,
			verdict: transcribe.VerdictConflict, conflict: transcribe.ConflictDifferentTitle,
		},
		{
			name:         "whitespace transcript cannot be re-parsed",
			row:          cur("  \n ", rpNone, nil),
			unreparsable: reparseBlankTranscript, before: reparseKindUnclassified, after: reparseKindUnclassified,
		},
		{
			name:         "a parse written by a newer grammar is never overwritten",
			row:          cur(rpTitle, rpNone, &newer),
			unreparsable: reparseNewerParser, before: "narration", after: "narration",
		},
		{
			name:   "silence sentinel is a silence verdict with nothing to extract",
			row:    cur(transcribe.SilenceSentinel, rpNone, nil),
			note:   reparseNoteSilence,
			before: reparseKindUnclassified, after: "music_or_silence", write: true, verdict: transcribe.VerdictNotAnnouncement,
		},
		{
			name:   "a parse from an older grammar that no longer matches is rewritten",
			row:    cur(rpChapter, rpNone, &database.IntroParse{Version: 1, Kind: "garbage"}),
			before: "garbage", after: "chapter_marker", write: true, verdict: transcribe.VerdictNotAnnouncement,
		},
		{
			name:   "a stored title the grammar rejects is counted as junk wherever the text goes",
			row:    cur(rpNarr, database.IntroReparseIdentity{Title: "Year One Narrated by Selina Kyle"}, nil),
			before: reparseKindUnclassified, after: "narration", write: true, verdict: transcribe.VerdictNotAnnouncement,
			junk: transcribe.JunkCreditWordingInside,
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
			if got.junk != tc.junk {
				t.Errorf("junk = %q, want %q", got.junk, tc.junk)
			}
			if tc.unreparsable != "" {
				return
			}
			if got.cmp.Verdict != tc.verdict || got.cmp.Conflict != tc.conflict || got.franchiseLost != tc.franchiseLost {
				t.Errorf("comparison = %+v franchiseLost=%v, want %s/%q/%v", got.cmp, got.franchiseLost, tc.verdict, tc.conflict, tc.franchiseLost)
			}
		})
	}
}

// The decision is idempotent: storing its parse and deciding again writes
// nothing. This is what makes a repeat run and a resumed run safe.
func TestReparseDecide_IsIdempotent(t *testing.T) {
	for _, text := range []string{rpTitle, rpPresent, rpChapter, rpNarr, transcribe.SilenceSentinel} {
		cur := database.IntroReparseRow{Transcript: text}
		first := reparseDecide(cur)
		if !first.write {
			t.Fatalf("%q: a never-classified row must be written", text[:min(len(text), 20)])
		}
		p := first.parse
		cur.Parse = &p
		if second := reparseDecide(cur); second.write {
			t.Errorf("%q: second pass still writes", text[:min(len(text), 20)])
		}
	}
}

// seedMixed is one row of every class the op reports, in book b1, plus a file
// with no intro field at all (never selected).
func seedMixed(s *reparseFakeStore) {
	uptodate := introParseFromClip(transcribe.ParseClip(rpTitle))
	newer := database.IntroParse{Version: transcribe.ClipParserVersion + 1, Kind: "narration"}
	agree := database.IntroReparseIdentity{Title: "The Killing Joke", Author: "Alfred Pennyworth", Narrator: "Selina Kyle"}
	s.put(rpIntro("b1", "f01", rpTitle, "unparsed", rpNone, nil))                                                                      // would fill 3
	s.put(rpIntro("b1", "f02", rpChapter, "ok", database.IntroReparseIdentity{Title: "Hush"}, nil))                                    // not an announcement
	s.put(rpIntro("b1", "f03", rpTitle, "ok", agree, &uptodate))                                                                       // up to date, agrees
	s.put(rpIntro("b1", "f04", "", "whisper_error", rpNone, nil))                                                                      // no transcript
	s.put(rpIntro("b1", "f05", "   ", "unparsed", rpNone, nil))                                                                        // blank
	s.put(rpIntro("b1", "f06", rpChapter, "whisper_error", rpNone, nil))                                                               // failure status
	s.put(rpIntro("b1", "f07", rpTitle, "ok", rpNone, &newer))                                                                         // newer grammar
	s.put(rpIntro("b1", "f08", transcribe.SilenceSentinel, "empty", rpNone, nil))                                                      // silence only
	s.put(rpIntro("b1", "f09", rpPresent, "ok", database.IntroReparseIdentity{Title: "Gotham Audio presents, Year One"}, nil))         // publisher prefix
	s.put(&database.BookFile{ID: "f10", BookID: "b1", FilePath: "/lib/b1/f10.m4b"})                                                    // no intro field
	s.put(rpIntro("b1", "f11", rpNarr, "ok", database.IntroReparseIdentity{Title: "Year One Narrated by Selina Kyle"}, nil))           // junk title
	s.put(rpIntro("b1", "f12", rpFranchise, "ok", database.IntroReparseIdentity{Title: "Big Finish Productions presents, Hush"}, nil)) // franchise term lost
}

func wantMixed(t *testing.T, res reparseFileIntrosResult) {
	t.Helper()
	check := func(name string, got, want int) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %d, want %d", name, got, want)
		}
	}
	check("files_without_intro_fields", res.NotSelected, 1)
	check("selected", res.Selected, 11)
	check("examined", res.Examined, 11)
	check("reparsed", res.Reparsed, 8)
	check("unreparsable", res.Unreparsed, 3)
	check("unreparsable no_transcript", res.Unreparsable[reparseNoTranscript], 1)
	check("unreparsable blank", res.Unreparsable[reparseBlankTranscript], 1)
	check("unreparsable newer", res.Unreparsable[reparseNewerParser], 1)
	check("note silence", res.Notes[reparseNoteSilence], 1)
	// f01, f02, f06, f08, f09, f11, f12 are written; f03 is up to date.
	check("parse_writes", res.ParseWrites, 7)
	check("kind_before unclassified", res.KindBefore[reparseKindUnclassified], 7)
	check("kind_before title", res.KindBefore["title_announcement"], 1)
	check("kind_after title", res.KindAfter["title_announcement"], 4) // f01, f03, f09, f12
	check("kind_after chapter", res.KindAfter["chapter_marker"], 2)   // f02, f06
	check("kind_after silence", res.KindAfter["music_or_silence"], 1) // f08
	check("kind_after narration", res.KindAfter["narration"], 1)      // f11
	check("gone", res.Gone, 0)
	check("errors", res.Errors, 0)
	check("selected = examined + gone + errors", res.Selected, res.Examined+res.Gone+res.Errors)

	id := res.Identity
	check("announcements", id.Announcements, 4)
	check("agree", id.Agree, 1)
	check("would_fill", id.WouldFill, 1)
	check("would_fill_fields", id.WouldFillFields, 3)
	check("conflict", id.Conflict, 2)
	check("conflict publisher_prefix", id.ConflictBy["publisher_prefix"], 1)
	check("conflict franchise_term_lost", id.ConflictBy[reparseConflictFranchiseLost], 1)
	check("announcements = agree + fill + conflict", id.Announcements, id.Agree+id.WouldFill+id.Conflict)
	check("stored_title_junk", id.StoredTitleJunk, 1)
	check("stored_title_junk credit_wording_inside", id.StoredTitleJunkBy["credit_wording_inside"], 1)

	for class, want := range map[string]string{
		"would_fill":                                   "f01",
		"conflict.publisher_prefix":                    "f09",
		"conflict.franchise_term_lost":                 "f12",
		"stored_title_junk.credit_wording_inside":      "f11",
		"unreparsable.no_transcript":                   "f04",
		"unreparsable.blank_transcript":                "f05",
		"unreparsable.stored_parse_from_newer_grammar": "f07",
	} {
		if got := res.Samples[class]; len(got) != 1 || got[0] != want {
			t.Errorf("samples[%s] = %v, want [%s]", class, got, want)
		}
	}
}

// A preview writes nothing, and reports what a live run would store.
func TestReparseFileIntros_PreviewWritesNothingAndReports(t *testing.T) {
	s := newReparseFakeStore()
	seedMixed(s)
	// A preview needs neither a writer nor a quiet queue.
	env := reparseFileIntrosEnv{reads: reparseReader{s}, reporter: &fakeReporter{}, workers: 4}

	res, err := runReparseFileIntros(context.Background(), env, reparseResumeKey{}, true)
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
	if got := s.get("b1", "f01"); got.IntroParse != nil {
		t.Errorf("preview changed f01: %+v", got)
	}
}

// storedColumns is every column a live run must leave exactly as stored: all but
// IntroParse, compared as a whole row.
func storedColumns(f database.BookFile) database.BookFile {
	f.IntroParse = nil
	return f
}

// A live run reports exactly what the preview reported, stores the parse and
// NOTHING else on any row, and a second live run changes nothing.
func TestReparseFileIntros_LiveMatchesPreviewWritesOnlyTheParseAndIsIdempotent(t *testing.T) {
	s := newReparseFakeStore()
	seedMixed(s)
	env := newReparseEnv(s, &fakeReporter{})
	before := map[string]database.BookFile{}
	for _, id := range []string{"f01", "f02", "f03", "f04", "f05", "f06", "f07", "f08", "f09", "f10", "f11", "f12"} {
		before[id] = storedColumns(s.get("b1", id))
	}

	res, err := runReparseFileIntros(context.Background(), env, reparseResumeKey{}, false)
	if err != nil {
		t.Fatalf("live: %v", err)
	}
	if res.DryRun {
		t.Error("live result claims dry run")
	}
	wantMixed(t, res)
	if n := s.totalWrites(); n != 7 {
		t.Fatalf("live wrote %d rows, want 7", n)
	}
	for id, want := range before {
		if got := storedColumns(s.get("b1", id)); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: a live run changed a column other than IntroParse:\nbefore %+v\nafter  %+v", id, want, got)
		}
	}

	if f01 := s.get("b1", "f01"); f01.IntroParse == nil || f01.IntroParse.Kind != "title_announcement" || f01.IntroParse.Version != transcribe.ClipParserVersion ||
		f01.IntroParse.Title != "The Killing Joke" {
		t.Errorf("f01 parse = %+v", f01.IntroParse)
	}
	// The status stays what the run stored: a clean announcement does not promote it.
	if st := s.get("b1", "f01").TranscribeStatus; st == nil || *st != "unparsed" {
		t.Errorf("f01 status = %v, want it still unparsed", st)
	}
	if f02 := s.get("b1", "f02"); f02.IntroParse.Kind != "chapter_marker" || f02.IntroParse.ChapterNumber != 12 {
		t.Errorf("f02 parse = %+v", f02.IntroParse)
	}
	// A newer parse is untouched, a row with no transcript still has none.
	if f07 := s.get("b1", "f07"); f07.IntroParse.Version != transcribe.ClipParserVersion+1 {
		t.Errorf("f07 parse overwritten: %+v", f07.IntroParse)
	}
	if f04 := s.get("b1", "f04"); f04.IntroParse != nil || f04.IntroTranscription != nil {
		t.Errorf("f04 changed: %+v", f04)
	}

	again, err := runReparseFileIntros(context.Background(), env, reparseResumeKey{}, false)
	if err != nil {
		t.Fatalf("second live run: %v", err)
	}
	if again.ParseWrites != 0 || s.totalWrites() != 7 {
		t.Errorf("second run still writes: parse_writes %d, total writes %d", again.ParseWrites, s.totalWrites())
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
	none := reparseResumeKey{}

	noWriter := reparseFileIntrosEnv{reads: reparseReader{s}, queue: reparseQueue{}, reporter: &fakeReporter{}}
	if _, err := runReparseFileIntros(ctx, noWriter, none, false); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Errorf("no writer: err = %v", err)
	}
	noQueue := newReparseEnv(s, &fakeReporter{})
	noQueue.queue = nil
	if _, err := runReparseFileIntros(ctx, noQueue, none, false); err == nil || !strings.Contains(err.Error(), "cannot verify") {
		t.Errorf("nil queue: err = %v", err)
	}
	broken := newReparseEnv(s, &fakeReporter{})
	broken.queue = reparseQueue{err: errors.New("queue down")}
	if _, err := runReparseFileIntros(ctx, broken, none, false); err == nil || !strings.Contains(err.Error(), "refusing to run") {
		t.Errorf("unreadable queue: err = %v", err)
	}
	busy := newReparseEnv(s, &fakeReporter{})
	busy.queue = reparseQueue{active: []database.OperationV2Row{{DefID: "library.scan", Status: "running"}}}
	if _, err := runReparseFileIntros(ctx, busy, none, false); err == nil || !strings.Contains(err.Error(), "library.scan") {
		t.Errorf("scan running: err = %v", err)
	}
	if n := s.totalWrites(); n != 0 {
		t.Fatalf("a refused run wrote %d rows", n)
	}
	// The same busy queue does not stop a preview.
	prev := newReparseEnv(s, &fakeReporter{})
	prev.queue = busy.queue
	if _, err := runReparseFileIntros(ctx, prev, none, true); err != nil {
		t.Errorf("preview refused: %v", err)
	}
}

// seedMany stores n narration rows in book b1, ids sorted by their number.
func seedMany(s *reparseFakeStore, n int) {
	for i := range n {
		s.put(rpIntro("b1", fmt.Sprintf("f%05d", i), rpNarr, "unparsed", rpNone, nil))
	}
}

func fid(i int) string { return fmt.Sprintf("f%05d", i) }

// The checkpoint carries every param, dry_run included, and its resume key is the
// last file of the last completed chunk. Without dry_run it comes back as the
// PREVIEW default on a resumed run.
func TestReparseFileIntros_CheckpointCarriesDryRunFalseAndTheLastKey(t *testing.T) {
	s := newReparseFakeStore()
	n := reparseChunkSize*3 + 5 // 4 chunks
	seedMany(s, n)
	rep := &reparseCkptReporter{}

	res, err := runReparseFileIntros(context.Background(), newReparseEnv(s, rep), reparseResumeKey{}, false)
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
	if last.ResumeAfterBookID != "b1" || last.ResumeAfterFileID != fid(n-1) {
		t.Errorf("final resume key = %s/%s, want b1/%s (the last file)", last.ResumeAfterBookID, last.ResumeAfterFileID, fid(n-1))
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
	if err != nil || dry || back.ResumeAfterFileID != fid(n-1) {
		t.Errorf("a resumed run reads dry_run=%v key=%q err=%v from %s, want false and the last file", dry, back.ResumeAfterFileID, err, raw)
	}
}

// A preview writes no checkpoint at all.
func TestReparseFileIntros_PreviewWritesNoCheckpoint(t *testing.T) {
	s := newReparseFakeStore()
	seedMany(s, reparseChunkSize+1)
	rep := &reparseCkptReporter{}
	if _, err := runReparseFileIntros(context.Background(), reparseFileIntrosEnv{reads: s, reporter: rep, workers: 2}, reparseResumeKey{}, true); err != nil {
		t.Fatal(err)
	}
	if got := rep.snapshot(); len(got) != 0 {
		t.Errorf("preview wrote checkpoints: %+v", got)
	}
}

// A live resume skips every file at or before the key; a preview counts them all
// (a partial count would be a wrong report); a key past the end finds nothing.
func TestReparseFileIntros_ResumeAfterKey(t *testing.T) {
	ctx := context.Background()
	n := reparseChunkSize*3 + 5
	key := reparseResumeKey{"b1", fid(2*reparseChunkSize - 1)} // the end of chunk 1

	s := newReparseFakeStore()
	seedMany(s, n)
	res, err := runReparseFileIntros(ctx, newReparseEnv(s, &fakeReporter{}), key, false)
	if err != nil {
		t.Fatal(err)
	}
	skipped := 2 * reparseChunkSize
	if !res.Resumed || res.Examined != n-skipped || s.totalWrites() != n-skipped {
		t.Errorf("resume: resumed=%v examined %d writes %d, want %d", res.Resumed, res.Examined, s.totalWrites(), n-skipped)
	}
	if first := s.get("b1", fid(0)); first.IntroParse != nil {
		t.Error("a file at or before the key was written")
	}
	if edge := s.get("b1", key.fileID); edge.IntroParse != nil {
		t.Error("the key's own file was written again")
	}
	if last := s.get("b1", fid(n-1)); last.IntroParse == nil {
		t.Error("a file after the key was not written")
	}

	p := newReparseFakeStore()
	seedMany(p, n)
	pres, err := runReparseFileIntros(ctx, reparseFileIntrosEnv{reads: p, reporter: &fakeReporter{}, workers: 2}, key, true)
	if err != nil || pres.Examined != n || pres.Resumed {
		t.Errorf("preview with a key: examined %d resumed=%v err %v, want all %d and not resumed", pres.Examined, pres.Resumed, err, n)
	}

	c := newReparseFakeStore()
	seedMany(c, 10)
	cres, err := runReparseFileIntros(ctx, newReparseEnv(c, &fakeReporter{}), reparseResumeKey{"b9", "zzz"}, false)
	if err != nil || cres.Examined != 0 || c.totalWrites() != 0 {
		t.Errorf("key past the end: examined %d writes %d err %v", cres.Examined, c.totalWrites(), err)
	}
}

// m2: a resume over a changed file set. The key means the same thing whatever was
// added or removed since the checkpoint, where a position in a rebuilt list would
// skip files (after removals before it) or redo a long prefix (after additions).
func TestReparseFileIntros_ResumeOverAChangedFileSet(t *testing.T) {
	ctx := context.Background()
	// Attempt 1 had finished everything up to and including f00099.
	key := reparseResumeKey{"b1", fid(99)}

	s := newReparseFakeStore()
	seedMany(s, 200)
	// Between the attempts: 40 files before the key were deleted (a position-based
	// watermark of 100 would now skip 40 files that were never done), 10 new files
	// arrived before the key (the old position would redo them), and 15 files after
	// it were deleted and 20 added.
	for i := 0; i < 40; i++ {
		s.mu.Lock()
		delete(s.rows, rpKey("b1", fid(i)))
		s.mu.Unlock()
	}
	for i := 0; i < 10; i++ {
		s.put(rpIntro("b1", fmt.Sprintf("f%05da", 50+i), rpNarr, "unparsed", rpNone, nil)) // sorts before f00099
	}
	for i := 120; i < 135; i++ {
		s.mu.Lock()
		delete(s.rows, rpKey("b1", fid(i)))
		s.mu.Unlock()
	}
	for i := 0; i < 20; i++ {
		s.put(rpIntro("b1", fmt.Sprintf("g%05d", i), rpNarr, "unparsed", rpNone, nil))
	}

	wantAfter := 0
	for _, c := range func() []database.BookFileCore { c, _ := s.GetAllBookFilesCore(); return c }() {
		if c.ID > key.fileID {
			wantAfter++
		}
	}
	res, err := runReparseFileIntros(ctx, newReparseEnv(s, &fakeReporter{}), key, false)
	if err != nil {
		t.Fatal(err)
	}
	// 100 original files after the key, minus the 15 deleted, plus 20 new.
	if wantAfter != 105 || res.Examined != wantAfter || s.totalWrites() != wantAfter {
		t.Fatalf("examined %d writes %d, want exactly the %d files after the key (expected 105)", res.Examined, s.totalWrites(), wantAfter)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, row := range s.rows {
		after := row.ID > key.fileID
		if after != (row.IntroParse != nil) {
			t.Errorf("%s: written=%v, want %v (after the key: %v)", k, row.IntroParse != nil, after, after)
		}
	}
}

// A file that fails to write leaves its row as stored, makes the run fail, and
// holds the resume key below its chunk so a resumed attempt retries it. Chunks
// after it still run.
func TestReparseFileIntros_FailedWriteHoldsTheKey(t *testing.T) {
	s := newReparseFakeStore()
	n := reparseChunkSize*4 + 1
	seedMany(s, n)
	bad := fid(reparseChunkSize + 3) // inside chunk 1
	s.failWrite[rpKey("b1", bad)] = true
	rep := &reparseCkptReporter{}

	res, err := runReparseFileIntros(context.Background(), newReparseEnv(s, rep), reparseResumeKey{}, false)
	if err == nil {
		t.Fatal("a failed write did not fail the run")
	}
	if res.Errors != 1 {
		t.Errorf("errors = %d, want 1", res.Errors)
	}
	if row := s.get("b1", bad); row.IntroParse != nil {
		t.Error("the failing row was written")
	}
	if want := n - 1; s.totalWrites() != want {
		t.Errorf("writes = %d, want every other file (%d)", s.totalWrites(), want)
	}
	for _, st := range rep.snapshot() {
		if st.ResumeAfterFileID >= fid(reparseChunkSize) {
			t.Fatalf("resume key %s passed the failed chunk 1 (starts at %s)", st.ResumeAfterFileID, fid(reparseChunkSize))
		}
	}
	// n2: the failed file is an error, not an examined row, and the books balance.
	if res.Selected != res.Examined+res.Gone+res.Errors || res.Examined != n-1 {
		t.Errorf("selected %d examined %d gone %d errors %d", res.Selected, res.Examined, res.Gone, res.Errors)
	}

	// A repeat run with the fault cleared finishes it.
	delete(s.failWrite, rpKey("b1", bad))
	res2, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), reparseResumeKey{}, false)
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

	res, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), reparseResumeKey{}, false)
	if err != nil {
		t.Fatal(err)
	}
	row := s.get("b1", "f01")
	if row.IntroParse == nil || row.IntroParse.Kind != "title_announcement" {
		t.Fatalf("row parse = %+v, want a verdict about the NEW text", row.IntroParse)
	}
	if row.TranscribedTitle != nil || *row.TranscribeStatus != "unparsed" {
		t.Errorf("identity/status touched: title %v status %v", row.TranscribedTitle, row.TranscribeStatus)
	}
	if res.KindAfter["title_announcement"] != 1 || res.KindAfter["narration"] != 0 || res.Identity.WouldFill != 1 {
		t.Errorf("counts follow the stale read: after=%v identity=%+v", res.KindAfter, res.Identity)
	}
}

// n3 and n2. Selection takes any retained intro column, not just status, time or
// hash: a row with only a run id or only a parsed title is read too. A file with
// none is counted, never read. A row deleted between the listing and the read, or
// between the read and the write, is Gone and not Examined, so selected always
// equals examined + gone + errors.
func TestReparseFileIntros_SelectionAndVanishedRows(t *testing.T) {
	s := newReparseFakeStore()
	s.put(&database.BookFile{ID: "plain", BookID: "b1"})
	runOnly := &database.BookFile{ID: "r1", BookID: "b1", IntroRunOpID: "op-batman", IntroTranscription: rpStr(rpNarr)}
	s.put(runOnly)
	titleOnly := &database.BookFile{ID: "r2", BookID: "b1", TranscribedTitle: rpStr("Hush"), IntroTranscription: rpStr(rpNarr)}
	s.put(titleOnly)
	s.put(rpIntro("b1", "f01", rpNarr, "ok", rpNone, nil))
	s.put(rpIntro("b1", "f02", rpNarr, "ok", rpNone, nil)) // vanishes at the read
	s.put(rpIntro("b1", "f03", rpNarr, "ok", rpNone, nil)) // vanishes at the write
	s.beforeDecide = func(st *reparseFakeStore, key string) {
		if key == rpKey("b1", "f03") {
			st.mu.Lock()
			delete(st.rows, key)
			st.mu.Unlock()
		}
	}

	gone := &vanishingReads{reparseFakeStore: s, vanish: rpKey("b1", "f02")}
	env := reparseFileIntrosEnv{reads: gone, writer: s, queue: reparseQueue{}, reporter: &fakeReporter{}, workers: 2}
	res, err := runReparseFileIntros(context.Background(), env, reparseResumeKey{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.NotSelected != 1 || res.Selected != 5 || res.Examined != 3 || res.Gone != 2 || res.Errors != 0 {
		t.Errorf("not_selected %d selected %d examined %d gone %d errors %d, want 1 / 5 / 3 / 2 / 0", res.NotSelected, res.Selected, res.Examined, res.Gone, res.Errors)
	}
	if res.Selected != res.Examined+res.Gone+res.Errors {
		t.Errorf("selected %d != examined %d + gone %d + errors %d", res.Selected, res.Examined, res.Gone, res.Errors)
	}
	for _, id := range []string{"r1", "r2", "f01"} {
		if s.get("b1", id).IntroParse == nil {
			t.Errorf("%s was selected but not classified", id)
		}
	}
	if s.get("b1", "plain").IntroParse != nil {
		t.Error("a file with no intro field was written")
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

// The disagreement report keeps the lowest ids per class (so two runs agree), is
// bounded, and never contains a title, a transcript or any name: counts and file
// ids only.
func TestReparseFileIntros_SamplesAreBoundedDeterministicAndFileIdsOnly(t *testing.T) {
	run := func(workers int) reparseFileIntrosResult {
		s := newReparseFakeStore()
		for i := range 40 {
			s.put(rpIntro("b1", fid(i), rpNarr, "ok", database.IntroReparseIdentity{Title: "Gotham Nights Narrated by Selina Kyle"}, nil))
		}
		env := reparseFileIntrosEnv{reads: s, reporter: &fakeReporter{}, workers: workers}
		res, err := runReparseFileIntros(context.Background(), env, reparseResumeKey{}, true)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	a, b := run(1), run(6)
	class := "stored_title_junk.credit_wording_inside"
	if a.Identity.StoredTitleJunk != 40 {
		t.Fatalf("junk = %d, want 40", a.Identity.StoredTitleJunk)
	}
	if len(a.Samples[class]) != reparseSampleCap {
		t.Fatalf("%d samples, want the cap %d", len(a.Samples[class]), reparseSampleCap)
	}
	for i, id := range a.Samples[class] {
		if id != fid(i) {
			t.Errorf("sample %d = %s, want the lowest ids in order (%s)", i, id, fid(i))
		}
	}
	if !reflect.DeepEqual(a.Samples, b.Samples) {
		t.Errorf("samples differ between 1 and 6 workers:\n%v\n%v", a.Samples, b.Samples)
	}
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"Gotham Nights", "Selina Kyle", "Bruce Wayne", "corridor"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("the op result contains %q: %s", leak, raw)
		}
	}
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
	// The description says what the op does NOT do.
	for _, want := range []string{"NOTHING else is written", "IntroParse", "dry_run defaults to true"} {
		if !strings.Contains(def.Description, want) {
			t.Errorf("description does not say %q", want)
		}
	}
	// dry_run omitted means preview, spelled either way.
	var p reparseFileIntrosParams
	if err := json.Unmarshal([]byte(`{}`), &p); err != nil {
		t.Fatal(err)
	}
	if dry, err := opmode.ResolveDryRun(reparseFileIntrosID, p.DryRun, p.DryRunCamel); err != nil || !dry {
		t.Errorf("omitted dry_run resolves to %v (err %v), want preview", dry, err)
	}
	if err := json.Unmarshal([]byte(`{"dry_run": false, "resume_after_book_id": "b1", "resume_after_file_id": "f7"}`), &p); err != nil {
		t.Fatal(err)
	}
	if dry, _ := opmode.ResolveDryRun(reparseFileIntrosID, p.DryRun, p.DryRunCamel); dry || p.ResumeAfterBookID != "b1" || p.ResumeAfterFileID != "f7" {
		t.Errorf("dry_run=false and a key parsed as dry=%v key=%s/%s", dry, p.ResumeAfterBookID, p.ResumeAfterFileID)
	}
}

// A row the transcription op writes is a fixed point of the re-parse: the same
// clip grammar produced its classification, so re-parsing a new row changes
// nothing. If these two drift, every new transcription would show up as work for
// the next re-parse.
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
				Transcript: *r.Transcript, Parse: r.Parse,
				Identity: database.IntroReparseIdentity{
					Title: r.Title, Author: r.Author, Narrator: r.Narrator, Translator: r.Translator, CoverArtist: r.CoverArtist,
				},
			}
			if got := reparseDecide(cur); got.write {
				t.Errorf("re-parsing a freshly written row would write: %+v", got.parse)
			}
		})
	}
}

// The classification is stored from the clip grammar and the identity from the
// LEGACY one: on every transcript the new row's identity columns and status are
// exactly what ParseAudiobookIntro gave before the clip grammar existed, even
// where the two grammars disagree.
func TestWhisperGroupResult_IdentityIsTheLegacyParse(t *testing.T) {
	now := time.Now()
	for name, text := range map[string]string{
		"announcement":   rpTitle,
		"publisher lead": rpPresent,
		"franchise lead": rpFranchise,
		"chapter":        rpChapter,
		"narration":      rpNarr,
		"B1 presents":    "The Joker's Christmas Presents, written by Edward Nygma",
		"B1 colon":       "Christmas Presents: A Gotham Anthology, edited by Alfred Pennyworth, read by Selina Kyle",
		"M3 marker lead": "Part 1. Gotham Audio presents Knightfall, written by Lucius Fox",
	} {
		t.Run(name, func(t *testing.T) {
			legacy := transcribe.ParseAudiobookIntro(text)
			r := whisperGroupResult(fileIntroWhisperResult{Text: text}, now).res
			if r.Title != legacy.Title || r.Author != legacy.Author || r.Narrator != legacy.Narrator ||
				r.Translator != legacy.Translator || r.CoverArtist != legacy.CoverArtist {
				t.Errorf("identity = %q/%q/%q/%q/%q, legacy %q/%q/%q/%q/%q", r.Title, r.Author, r.Narrator, r.Translator, r.CoverArtist,
					legacy.Title, legacy.Author, legacy.Narrator, legacy.Translator, legacy.CoverArtist)
			}
			wantStatus := statusOK
			if legacy.Title == "" {
				wantStatus = statusUnparsed
			}
			if r.Status != wantStatus {
				t.Errorf("status = %q, want %q", r.Status, wantStatus)
			}
		})
	}
	// And the classification beside it is the clip grammar's, with a kind of its own.
	ch := whisperGroupResult(fileIntroWhisperResult{Text: rpChapter}, now).res
	if ch.Parse.Kind != "chapter_marker" || ch.Parse.ChapterNumber != 12 {
		t.Errorf("chapter parse = %+v", ch.Parse)
	}
}

// The chunks really run in parallel: with a pool of four, at least two writes are
// in flight at once. (A rendezvous in the fake holds each write until a second
// one arrives or two seconds pass, so a sequential run fails rather than hangs.)
func TestReparseFileIntros_ChunksRunInParallel(t *testing.T) {
	s := newReparseFakeStore()
	seedMany(s, reparseChunkSize*4)
	s.rendezvous = 2

	if _, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), reparseResumeKey{}, false); err != nil {
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

	if _, err := runReparseFileIntros(context.Background(), env, reparseResumeKey{}, false); err != nil {
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
