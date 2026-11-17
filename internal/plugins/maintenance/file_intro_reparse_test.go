// file: internal/plugins/maintenance/file_intro_reparse_test.go
// version: 2.1.0
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
	// failRead lists "book/file" keys whose read returns an error.
	failRead map[string]bool
	// beforeDecide runs once per key, under no lock, before the writer's first
	// decide: the point where a concurrent writer can change the row.
	beforeDecide func(s *reparseFakeStore, key string)
	raced        map[string]bool
	// beforeRead runs once per key, under no lock, before the op's first read of
	// that row: the point where a merge can re-home a row between the listing and
	// the read. afterListing runs after every book file listing, under no lock.
	beforeRead   func(s *reparseFakeStore, key string)
	readRaced    map[string]bool
	afterListing func(s *reparseFakeStore)
	decides      map[string]int
	// rendezvous, when set, makes a write wait (bounded) until that many writes
	// are in flight at once; maxInflight is the most seen.
	rendezvous   int
	rendezvoused atomic.Bool
	inflight     atomic.Int64
	maxInflight  atomic.Int64
	// onWrite sees every committed write (called under the store lock).
	onWrite func()
	// bookText is a book's own (book-level) transcript, as the books listing
	// retains it; a book with rows here but no entry has none.
	bookText map[string]string
	// filesErr / booksErr make the corresponding Complete listing fail, the way a
	// short memdb whose Pebble fallback scan failed would.
	filesErr, booksErr error
	listings           atomic.Int64
	// unconfirmed lists "book/file" keys whose write is applied but reported with an
	// error, the way a commit whose durability is unknown is.
	unconfirmed map[string]bool
	// stamp is the timestamp the fake's SizeWith encodes rows with (the writer's
	// "fresh read" size); the zero value means rpStamp.
	stamp time.Time
}

// rpStamp is the fixed timestamp the fake store and the test envs size rows with, so
// a preview and a live run can be compared exactly.
var rpStamp = time.Date(2026, 11, 17, 12, 0, 0, 123456789, time.UTC)

func rpClock() time.Time { return rpStamp }

func newReparseFakeStore() *reparseFakeStore {
	return &reparseFakeStore{
		rows: map[string]*database.BookFile{}, writes: map[string]int{},
		failWrite: map[string]bool{}, failRead: map[string]bool{}, raced: map[string]bool{}, decides: map[string]int{},
		readRaced: map[string]bool{}, unconfirmed: map[string]bool{}, bookText: map[string]string{},
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

func (s *reparseFakeStore) GetAllBookFilesCoreComplete() ([]database.BookFileCore, error) {
	s.listings.Add(1)
	s.mu.Lock()
	if s.filesErr != nil {
		defer s.mu.Unlock()
		return nil, s.filesErr
	}
	out := make([]database.BookFileCore, 0, len(s.rows))
	for _, r := range s.rows {
		out = append(out, r.Core())
	}
	hook := s.afterListing
	s.mu.Unlock()
	if hook != nil {
		hook(s) // the rows it adds show in the NEXT listing, as in a live library
	}
	return out, nil
}

// move re-homes a row the way MoveBookFilesToBook does: same file id, new book.
func (s *reparseFakeStore) move(fromBook, toBook, file string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rows[rpKey(fromBook, file)]
	delete(s.rows, rpKey(fromBook, file))
	r.BookID = toBook
	s.rows[rpKey(toBook, file)] = r
}

// GetAllBooksCoreComplete lists every book a row names, with the book-level
// transcript from bookText. Like the real one it retains IntroTranscription and
// treats limit 0 as all.
func (s *reparseFakeStore) GetAllBooksCoreComplete(limit, offset int) ([]database.BookCore, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.booksErr != nil {
		return nil, s.booksErr
	}
	seen := map[string]bool{}
	var out []database.BookCore
	for _, r := range s.rows {
		if seen[r.BookID] {
			continue
		}
		seen[r.BookID] = true
		b := database.BookCore{ID: r.BookID}
		if t, ok := s.bookText[r.BookID]; ok {
			b.IntroTranscription = rpStr(t)
		}
		out = append(out, b)
	}
	return out, nil
}

func (s *reparseFakeStore) GetBookFileByID(book, file string) (*database.BookFile, error) {
	s.mu.Lock()
	hook := s.beforeRead
	first := !s.readRaced[rpKey(book, file)]
	s.readRaced[rpKey(book, file)] = true
	s.mu.Unlock()
	if hook != nil && first {
		hook(s, rpKey(book, file))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failRead[rpKey(book, file)] {
		return nil, errors.New("read failed (fault injected)")
	}
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
	at := s.stamp
	if at.IsZero() {
		at = rpStamp
	}
	sized := *row
	cur.SizeWith = func(p database.IntroParse) (int, error) { return database.IntroParseRowBytes(&sized, p, at) }
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
	if s.unconfirmed[key] {
		return database.IntroReparseWrote, errors.New("applied, durability unknown (fault injected)")
	}
	return database.IntroReparseWrote, nil
}

// reparseReader hides the writer half so a test can hand the env a store that
// can read but not write.
type reparseReader struct{ s *reparseFakeStore }

func (r reparseReader) GetAllBookFilesCoreComplete() ([]database.BookFileCore, error) {
	return r.s.GetAllBookFilesCoreComplete()
}
func (r reparseReader) GetAllBooksCoreComplete(limit, offset int) ([]database.BookCore, error) {
	return r.s.GetAllBooksCoreComplete(limit, offset)
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

func newReparseEnv(s *reparseFakeStore, rep sdk.Reporter) reparseFileIntrosEnv {
	return reparseFileIntrosEnv{reads: s, writer: s, queue: reparseQueue{}, reporter: rep, workers: 4, now: rpClock}
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
	check("listed", res.Listed, 11)
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

	res, err := runReparseFileIntros(context.Background(), env, true)
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

	res, err := runReparseFileIntros(context.Background(), env, false)
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

	again, err := runReparseFileIntros(context.Background(), env, false)
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

	noWriter := reparseFileIntrosEnv{reads: reparseReader{s}, queue: reparseQueue{}, reporter: &fakeReporter{}}
	if _, err := runReparseFileIntros(ctx, noWriter, false); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Errorf("no writer: err = %v", err)
	}
	noQueue := newReparseEnv(s, &fakeReporter{})
	noQueue.queue = nil
	if _, err := runReparseFileIntros(ctx, noQueue, false); err == nil || !strings.Contains(err.Error(), "cannot verify") {
		t.Errorf("nil queue: err = %v", err)
	}
	// The unreadable-queue and busy cases are bounded: without the start gate the
	// run would go on into the per-chunk scan wait, which retries the queue on its
	// pause interval, so the missing gate would show as a minutes-long wait and
	// not as a failure. A bounded context turns the wait into an error that does
	// not carry the gate's message.
	bctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	broken := newReparseEnv(s, &fakeReporter{})
	broken.queue = reparseQueue{err: errors.New("queue down")}
	if _, err := runReparseFileIntros(bctx, broken, false); err == nil || !strings.Contains(err.Error(), "refusing to run") {
		t.Errorf("unreadable queue: err = %v", err)
	}
	busy := newReparseEnv(s, &fakeReporter{})
	busy.queue = reparseQueue{active: []database.OperationV2Row{{DefID: "library.scan", Status: "running"}}}
	if _, err := runReparseFileIntros(bctx, busy, false); err == nil || !strings.Contains(err.Error(), "library.scan") {
		t.Errorf("scan running: err = %v", err)
	}
	if n := s.totalWrites(); n != 0 {
		t.Fatalf("a refused run wrote %d rows", n)
	}
	// The same busy queue does not stop a preview.
	prev := newReparseEnv(s, &fakeReporter{})
	prev.queue = busy.queue
	if _, err := runReparseFileIntros(ctx, prev, true); err != nil {
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

// m2: a restarted run visits every file again, so a row that landed BEFORE where the
// first attempt stopped is not missed. Attempt 1 fails in its last chunk; between the
// attempts a file arrives that sorts before everything (a re-homed row, a new
// transcript); attempt 2 must write it, and must rewrite none of the finished rows.
// The earlier version of this test asserted the opposite (that a key skipped such
// files), which is the bug.
func TestReparseFileIntros_RestartVisitsRowsThatLandedBeforeTheOldStop(t *testing.T) {
	ctx := context.Background()
	s := newReparseFakeStore()
	n := reparseChunkSize*3 + 5
	seedMany(s, n)
	bad := fid(n - 2) // in the last chunk
	s.failWrite[rpKey("b1", bad)] = true

	res1, err := runReparseFileIntros(ctx, newReparseEnv(s, &fakeReporter{}), false)
	if err == nil || res1.Errors != 1 {
		t.Fatalf("attempt 1: err %v errors %d, want one failure", err, res1.Errors)
	}
	finished := s.totalWrites()
	if finished != n-1 {
		t.Fatalf("attempt 1 wrote %d rows, want %d", finished, n-1)
	}

	// Lands before every file attempt 1 listed.
	s.put(rpIntro("a0", "a0000", rpNarr, "unparsed", rpNone, nil))
	delete(s.failWrite, rpKey("b1", bad))

	res2, err := runReparseFileIntros(ctx, newReparseEnv(s, &fakeReporter{}), false)
	if err != nil {
		t.Fatalf("attempt 2: %v", err)
	}
	if row := s.get("a0", "a0000"); row.IntroParse == nil {
		t.Fatal("a row that landed before the old stop was missed by the restarted run")
	}
	if row := s.get("b1", bad); row.IntroParse == nil {
		t.Fatal("the file that failed was not retried")
	}
	if res2.ParseWrites != 2 || s.totalWrites() != finished+2 {
		t.Errorf("attempt 2 wrote %d rows (store total %d), want exactly the new row and the failed one (%d)", res2.ParseWrites, s.totalWrites(), finished+2)
	}
	// It READ the finished rows and found them current, which is what rules out a gap.
	if res2.Examined != n+1 || res2.Selected != n+1 || res2.Listed != n+1 {
		t.Errorf("attempt 2 listed %d selected %d examined %d, want %d each", res2.Listed, res2.Selected, res2.Examined, n+1)
	}
}

// m2: a run that reports success has visited every file its last listing held. A file that
// appears while the run is working (here: one new row per file, once) is visited by
// a closing pass before the run succeeds, and a library that keeps growing fails the
// run instead of reporting a success it cannot back.
func TestReparseFileIntros_ClosingPassVisitsFilesThatAppearedDuringTheRun(t *testing.T) {
	s := newReparseFakeStore()
	seedMany(s, 3)
	var once sync.Once
	s.beforeDecide = func(st *reparseFakeStore, key string) {
		once.Do(func() { st.put(rpIntro("a0", "late1", rpNarr, "unparsed", rpNone, nil)) })
	}
	res, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), false)
	if err != nil {
		t.Fatal(err)
	}
	if row := s.get("a0", "late1"); row.IntroParse == nil {
		t.Fatal("the run reported success with a selectable file left behind")
	}
	if res.Listed != 4 || res.Selected != 4 || res.ParseWrites != 4 {
		t.Errorf("listed %d selected %d writes %d, want 4 each", res.Listed, res.Selected, res.ParseWrites)
	}
	if got := s.listings.Load(); got < 3 {
		t.Errorf("%d listings, want the first, the closing one that found the new file, and one that found nothing more", got)
	}
}

// R3-m2 (b): rows that keep arriving AND need a parse write keep every pass
// productive, so the run cannot claim to be done: it ends in failure when the
// backstop is reached, and says why.
func TestReparseFileIntros_RowsThatKeepNeedingWritesHitTheBackstopAndFail(t *testing.T) {
	s := newReparseFakeStore()
	seedMany(s, 2)
	var n atomic.Int64
	s.afterListing = func(st *reparseFakeStore) {
		st.put(rpIntro("a0", fmt.Sprintf("late%03d", n.Add(1)), rpNarr, "unparsed", rpNone, nil))
	}
	res, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), false)
	if err == nil || !strings.Contains(err.Error(), "kept producing writes") {
		t.Fatalf("err = %v, want the run to fail and say passes kept producing writes", err)
	}
	if got := s.listings.Load(); got != reparseMaxPasses {
		t.Errorf("%d listings, want exactly the %d passes the backstop allows", got, reparseMaxPasses)
	}
	if res.ParseWrites < 2+reparseMaxPasses-1 {
		t.Errorf("parse writes = %d, want every pass to have written", res.ParseWrites)
	}
}

// R3-m2 (a): while a whole-library transcription run adds files, each one already
// carries a current parse. A pass over such rows writes nothing and loses nothing, so
// the run is done: chasing rows that never needed work would only turn a finished run
// into a failed one. A row added after the last listing is outside the run, as for any
// snapshot.
func TestReparseFileIntros_RowsThatAreAlreadyCurrentDoNotKeepTheRunOpen(t *testing.T) {
	s := newReparseFakeStore()
	seedMany(s, 3)
	current := introParseFromClip(transcribe.ParseClip(rpNarr))
	var n atomic.Int64
	s.afterListing = func(st *reparseFakeStore) {
		st.put(rpIntro("a0", fmt.Sprintf("late%03d", n.Add(1)), rpNarr, "unparsed", rpNone, &current))
	}
	res, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), false)
	if err != nil {
		t.Fatalf("a library that only gained already-current rows failed the run: %v", err)
	}
	if res.ParseWrites != 3 {
		t.Errorf("parse writes = %d, want the 3 seeded rows only", res.ParseWrites)
	}
	if got := s.listings.Load(); got != 2 {
		t.Errorf("%d listings, want 2: the first, and one whose pass wrote nothing and lost nothing", got)
	}
	if res.Examined != 4 || res.Listed != 4 {
		t.Errorf("examined %d listed %d, want the 3 seeded rows and the one row the second listing held", res.Examined, res.Listed)
	}
}

// R3-m1: a merge re-homes a row (same file id, new book) after pass 1 listed it and
// before its chunk read it. The read under the old key finds nothing (Gone); the row
// must still be classified, under its new book, by a further pass. Keyed by file id
// alone, the old key marked the file visited and the run reported success with the
// row unclassified.
func TestReparseFileIntros_AFileRehomedBetweenListingAndReadIsClassifiedUnderItsNewBook(t *testing.T) {
	s := newReparseFakeStore()
	seedMany(s, 3)
	moved := fid(1)
	s.beforeRead = func(st *reparseFakeStore, key string) {
		if key == rpKey("b1", moved) {
			st.move("b1", "b2", moved)
		}
	}
	res, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), false)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if row := s.get("b2", moved); row.IntroParse == nil {
		t.Fatal("the run reported success with the re-homed row unclassified")
	}
	if res.Gone != 1 || res.ParseWrites != 3 || res.Listed != 4 {
		t.Errorf("gone %d writes %d listed %d, want 1 / 3 / 4 (the old key, then the row under its new book)", res.Gone, res.ParseWrites, res.Listed)
	}
}

// A preview is one pass and one listing, whatever happens to the library after it.
func TestReparseFileIntros_PreviewListsOnce(t *testing.T) {
	s := newReparseFakeStore()
	seedMany(s, reparseChunkSize+1)
	if _, err := runReparseFileIntros(context.Background(), reparseFileIntrosEnv{reads: s, reporter: &fakeReporter{}, workers: 2}, true); err != nil {
		t.Fatal(err)
	}
	if got := s.listings.Load(); got != 1 {
		t.Errorf("a preview listed the library %d times, want 1", got)
	}
}

// m3: the selection reads the COMPLETE listings and a failure of either is an error,
// never a shorter list: a short memdb must not silently shrink the set. Nothing is
// written and nothing is reported as examined.
func TestReparseFileIntros_AFailedListingIsAnError(t *testing.T) {
	for name, set := range map[string]func(s *reparseFakeStore){
		"book files": func(s *reparseFakeStore) { s.filesErr = errors.New("memdb incomplete: book_files") },
		"books":      func(s *reparseFakeStore) { s.booksErr = errors.New("memdb incomplete: books") },
	} {
		for _, dry := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s dry=%v", name, dry), func(t *testing.T) {
				s := newReparseFakeStore()
				seedMany(s, 10)
				set(s)
				env := newReparseEnv(s, &fakeReporter{})
				res, err := runReparseFileIntros(context.Background(), env, dry)
				if err == nil || !strings.Contains(err.Error(), "memdb incomplete") {
					t.Fatalf("err = %v, want the listing failure", err)
				}
				if res.Examined != 0 || res.Selected != 0 || s.totalWrites() != 0 {
					t.Errorf("a failed listing still examined %d selected %d wrote %d", res.Examined, res.Selected, s.totalWrites())
				}
			})
		}
	}
}

// m4: a row that holds ONLY a transcript (no status, time, hash, run id or identity)
// is selected when it is the single file of a book that carries a book-level
// transcript, which is where the silence sentinel and the single-file migration put
// one. The two shapes the listing cannot reveal are counted as not selected and
// said so in the comment; this test pins that boundary so a change to it is seen.
func TestReparseFileIntros_SelectsTranscriptOnlyRowsThroughTheBook(t *testing.T) {
	s := newReparseFakeStore()
	only := func(book, file, text string) {
		s.put(&database.BookFile{ID: file, BookID: book, FilePath: "/lib/" + book + "/" + file + ".m4b", IntroTranscription: rpStr(text)})
	}
	only("single", "s1", rpTitle) // single-file book, book has the transcript: selected
	s.bookText["single"] = rpTitle
	only("sentinel", "n1", transcribe.SilenceSentinel) // the silence sentinel the same way
	s.bookText["sentinel"] = transcribe.SilenceSentinel
	only("multi", "m1", rpTitle) // two files: cannot tell which holds the text
	only("multi", "m2", rpNarr)
	s.bookText["multi"] = rpTitle
	only("nobook", "b1", rpTitle) // the book has no transcript of its own
	s.put(&database.BookFile{ID: "plain", BookID: "plainbook", FilePath: "/lib/plain.m4b"})
	s.bookText["blank"] = "   " // a blank book transcript selects nothing
	s.put(&database.BookFile{ID: "bl1", BookID: "blank", FilePath: "/lib/blank.m4b"})

	res, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Selected != 2 || res.SelectedByBookTranscript != 2 || res.NotSelected != 5 {
		t.Errorf("selected %d (by book transcript %d) not selected %d, want 2 / 2 / 5", res.Selected, res.SelectedByBookTranscript, res.NotSelected)
	}
	if got := s.get("single", "s1").IntroParse; got == nil || got.Kind != "title_announcement" {
		t.Errorf("transcript-only row parse = %+v, want a title announcement", got)
	}
	if got := s.get("sentinel", "n1").IntroParse; got == nil || got.Kind != "music_or_silence" {
		t.Errorf("sentinel row parse = %+v, want silence", got)
	}
	for _, id := range [][2]string{{"multi", "m1"}, {"multi", "m2"}, {"nobook", "b1"}, {"plainbook", "plain"}, {"blank", "bl1"}} {
		if s.get(id[0], id[1]).IntroParse != nil {
			t.Errorf("%s/%s was classified; it is outside the documented selection", id[0], id[1])
		}
	}
}

// A file that fails to write leaves its row as stored, makes the run fail, and a
// repeat run finishes it. Chunks after it still run.
func TestReparseFileIntros_FailedWriteIsRetriedByARepeatRun(t *testing.T) {
	s := newReparseFakeStore()
	n := reparseChunkSize*4 + 1
	seedMany(s, n)
	bad := fid(reparseChunkSize + 3) // inside chunk 1
	s.failWrite[rpKey("b1", bad)] = true

	res, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), false)
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
	// n2: the failed file is an error, not an examined row, and the books balance.
	if res.Selected != res.Examined+res.Gone+res.Errors || res.Examined != n-1 {
		t.Errorf("selected %d examined %d gone %d errors %d", res.Selected, res.Examined, res.Gone, res.Errors)
	}

	// A repeat run with the fault cleared finishes it.
	delete(s.failWrite, rpKey("b1", bad))
	res2, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), false)
	if err != nil || res2.ParseWrites != 1 {
		t.Errorf("repeat run: writes %d err %v, want exactly the one that failed", res2.ParseWrites, err)
	}
}

// n7: a run that stops early leaves Selected below Listed, so the books-balance
// identity (selected = examined + gone + errors) cannot hide a shortfall.
func TestReparseFileIntros_ACancelledRunShowsItsShortfall(t *testing.T) {
	s := newReparseFakeStore()
	n := reparseChunkSize * 4
	seedMany(s, n)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var writes atomic.Int64
	s.onWrite = func() {
		if writes.Add(1) == int64(reparseChunkSize+10) {
			cancel()
		}
	}
	env := newReparseEnv(s, &fakeReporter{})
	env.workers = 1
	res, err := runReparseFileIntros(ctx, env, false)
	if err == nil {
		t.Fatal("a cancelled run reported success")
	}
	if res.Listed != n {
		t.Errorf("listed = %d, want the %d files the run set out to read", res.Listed, n)
	}
	if res.Selected >= res.Listed {
		t.Errorf("selected %d is not below listed %d on a run that stopped early", res.Selected, res.Listed)
	}
	if res.Selected != res.Examined+res.Gone+res.Errors {
		t.Errorf("selected %d != examined %d + gone %d + errors %d", res.Selected, res.Examined, res.Gone, res.Errors)
	}
}

// MJ1: the report says how much a live run would write. Each rewritten row is
// counted at its full stored size (the whole row is re-encoded, fingerprint and
// all), computed here from the row itself with the pinned timestamp, and a preview, a
// live run and a repeat run agree.
func TestReparseFileIntros_ReportsTheWriteVolume(t *testing.T) {
	s := newReparseFakeStore()
	seedMixed(s)
	// A fingerprint makes the row size depend on the whole record, not the parse.
	fp := rpIntro("b2", "fp", rpTitle, "unparsed", rpNone, nil)
	fp.AcoustIDFingerprint = make([]byte, 3000)
	s.put(fp)

	want := int64(0)
	s.mu.Lock()
	for _, row := range s.rows {
		if row.IntroTranscription == nil || strings.TrimSpace(*row.IntroTranscription) == "" {
			continue
		}
		p := transcribe.ParseClip(*row.IntroTranscription)
		np := introParseFromClip(p)
		if row.IntroParse != nil && (*row.IntroParse == np || row.IntroParse.Version > transcribe.ClipParserVersion) {
			continue
		}
		c := *row
		c.IntroParse = &np
		c.UpdatedAt = rpStamp
		b, err := json.Marshal(&c)
		if err != nil {
			s.mu.Unlock()
			t.Fatal(err)
		}
		want += int64(len(b))
	}
	s.mu.Unlock()
	if want < 3000 {
		t.Fatalf("test setup: expected volume %d does not include the fingerprint row", want)
	}

	prev, err := runReparseFileIntros(context.Background(), reparseFileIntrosEnv{reads: reparseReader{s}, reporter: &fakeReporter{}, workers: 3, now: rpClock}, true)
	if err != nil {
		t.Fatal(err)
	}
	if prev.ParseWriteBytes != want || prev.ParseWrites != 8 {
		t.Errorf("preview reports %d rows / %d bytes, want 8 / %d", prev.ParseWrites, prev.ParseWriteBytes, want)
	}
	live, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), false)
	if err != nil {
		t.Fatal(err)
	}
	if live.ParseWriteBytes != want || live.ParseWrites != prev.ParseWrites {
		t.Errorf("live reports %d rows / %d bytes, preview %d / %d", live.ParseWrites, live.ParseWriteBytes, prev.ParseWrites, want)
	}
	again, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), false)
	if err != nil {
		t.Fatal(err)
	}
	if again.ParseWrites != 0 || again.ParseWriteBytes != 0 {
		t.Errorf("a repeat run reports %d rows / %d bytes, want none", again.ParseWrites, again.ParseWriteBytes)
	}
	raw, _ := json.Marshal(prev)
	if !strings.Contains(string(raw), `"parse_write_bytes"`) {
		t.Errorf("the result does not carry parse_write_bytes: %s", raw)
	}
}

// The op's description tells the operator a live run is not recommended yet.
func TestReparseFileIntrosDef_DescriptionWarnsAboutALiveRun(t *testing.T) {
	d := (&Plugin{}).reparseFileIntrosDef().Description
	for _, want := range []string{"NOT recommended", "something reads the field", "bytes"} {
		if !strings.Contains(d, want) {
			t.Errorf("description lacks %q: %s", want, d)
		}
	}
	if strings.Contains(strings.ToLower(d), "resumable") || strings.Contains(d, "continues after") {
		t.Errorf("description still claims a resume position: %s", d)
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

	res, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), false)
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
	res, err := runReparseFileIntros(context.Background(), env, false)
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
		res, err := runReparseFileIntros(context.Background(), env, true)
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
	// A checkpoint or job record written by the keyed round of this op still carries
	// resume_after_* fields; they are ignored, and dry_run=false still means live.
	p = reparseFileIntrosParams{}
	if err := json.Unmarshal([]byte(`{"dry_run": false, "resume_after_book_id": "b1", "resume_after_file_id": "f7"}`), &p); err != nil {
		t.Fatal(err)
	}
	if dry, err := opmode.ResolveDryRun(reparseFileIntrosID, p.DryRun, p.DryRunCamel); err != nil || dry {
		t.Errorf("dry_run=false parsed as dry=%v err=%v", dry, err)
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
	// The loop above compares with the same ParseAudiobookIntro the code calls, so it
	// catches only a switch to the clip fields. These are written out: if the legacy
	// grammar's answer for them ever changes, this fails even though both sides
	// moved together.
	for text, want := range map[string][3]string{
		rpTitle:   {"The Killing Joke", "Alfred Pennyworth", "Selina Kyle"},
		rpPresent: {"Gotham Audio presents, Year One", "Alfred Pennyworth", "Selina Kyle"},
	} {
		r := whisperGroupResult(fileIntroWhisperResult{Text: text}, now).res
		if got := [3]string{r.Title, r.Author, r.Narrator}; got != want {
			t.Errorf("identity of %q = %q, want %q", text, got, want)
		}
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

	if _, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), false); err != nil {
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

	if _, err := runReparseFileIntros(context.Background(), env, false); err != nil {
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
		&database.IntroParse{Version: transcribe.ClipParserVersion, Kind: "title_announcement", Title: "The Killing Joke"})
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

// m5: a donor with no parse, or with one from an older grammar, does not hand that
// on. The new row's parse is derived from its text, so it is already current and
// the re-parse op has nothing to find. A donor at the current version is copied,
// and a donor from a NEWER grammar is left as that parser wrote it.
func TestDonorResult_DerivesTheParseWhenTheDonorsIsMissingOrOld(t *testing.T) {
	want := introParseFromClip(transcribe.ParseClip(rpTitle))
	if want.Kind != "title_announcement" {
		t.Fatalf("fixture: %q parses as %q", rpTitle, want.Kind)
	}
	cases := []struct {
		name  string
		donor *database.IntroParse
		want  database.IntroParse
	}{
		{"nil parse", nil, want},
		// A version below the current one, with content that would be wrong to keep.
		{"older grammar", &database.IntroParse{Version: transcribe.ClipParserVersion - 1, Kind: "narration"}, want},
		{"current grammar is copied", &database.IntroParse{Version: transcribe.ClipParserVersion, Kind: "narration"}, database.IntroParse{Version: transcribe.ClipParserVersion, Kind: "narration"}},
		{"newer grammar is kept", &database.IntroParse{Version: transcribe.ClipParserVersion + 1, Kind: "narration"}, database.IntroParse{Version: transcribe.ClipParserVersion + 1, Kind: "narration"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			donor := rpIntro("b1", "donor", rpTitle, "ok", database.IntroReparseIdentity{Title: "The Killing Joke"}, tc.donor)
			s := newReparseFakeStore()
			s.put(donor)
			w := &fileIntroWorker{store: s}
			gr := w.donorResult(fileIntroGroup{DonorBookID: "b1", DonorFileID: "donor", Hash: donor.IntroSourceHash})
			if !gr.hasResult || gr.res.Parse == nil {
				t.Fatalf("donor result = %+v, want a parse", gr.res)
			}
			if *gr.res.Parse != tc.want {
				t.Errorf("parse = %+v, want %+v", *gr.res.Parse, tc.want)
			}
			if tc.donor != nil && gr.res.Parse == tc.donor {
				t.Error("the result aliases the donor's parse")
			}
		})
	}
	// The derived parse is a fixed point of the re-parse, like a fresh transcription's.
	donor := rpIntro("b1", "donor", rpTitle, "ok", rpNone, nil)
	s := newReparseFakeStore()
	s.put(donor)
	gr := (&fileIntroWorker{store: s}).donorResult(fileIntroGroup{DonorBookID: "b1", DonorFileID: "donor", Hash: donor.IntroSourceHash})
	if got := reparseDecide(database.IntroReparseRow{Transcript: *gr.res.Transcript, Parse: gr.res.Parse}); got.write {
		t.Errorf("re-parsing a row filled from a stale donor would still write: %+v", got.parse)
	}
}

// The silence sentinel reused from a donor with no parse gets the silence parse.
func TestDonorResult_SentinelWithoutAParseIsSilence(t *testing.T) {
	donor := rpIntro("b1", "donor", transcribe.SilenceSentinel, "empty", rpNone, nil)
	s := newReparseFakeStore()
	s.put(donor)
	gr := (&fileIntroWorker{store: s}).donorResult(fileIntroGroup{DonorBookID: "b1", DonorFileID: "donor", Hash: donor.IntroSourceHash})
	if gr.res.Parse == nil || gr.res.Parse.Kind != "music_or_silence" || gr.res.Parse.Version != transcribe.ClipParserVersion {
		t.Fatalf("parse = %+v, want the current silence parse", gr.res.Parse)
	}
}

// addSample keeps the lowest reparseSampleCap distinct ids per class whatever
// order they arrive in (chunks fold in completion order, so the order is not the
// id order). The op test above feeds ascending ids from one chunk only.
func TestAddSample_KeepsTheLowestDistinctIdsInAnyOrder(t *testing.T) {
	ids := make([]string, 0, 30)
	for i := range 30 {
		ids = append(ids, fid(i))
	}
	orders := map[string][]string{
		"ascending":  ids,
		"descending": append([]string(nil), ids...),
		"doubled":    append(append([]string(nil), ids...), ids...),
	}
	for i, j := 0, len(orders["descending"])-1; i < j; i, j = i+1, j-1 {
		orders["descending"][i], orders["descending"][j] = orders["descending"][j], orders["descending"][i]
	}
	// Interleave: high, low, high, low.
	var mixed []string
	for i := range 15 {
		mixed = append(mixed, ids[29-i], ids[i])
	}
	orders["interleaved"] = mixed
	want := ids[:reparseSampleCap]
	for name, in := range orders {
		m := map[string][]string{}
		for _, id := range in {
			addSample(m, "c", id)
		}
		if !reflect.DeepEqual(m["c"], want) {
			t.Errorf("%s: samples = %v, want %v", name, m["c"], want)
		}
	}
	// One id offered twice below the cap is kept once.
	m := map[string][]string{}
	addSample(m, "c", "f00001")
	addSample(m, "c", "f00001")
	if len(m["c"]) != 1 {
		t.Errorf("a repeated id was kept %d times", len(m["c"]))
	}
}

// Selection reads the retained intro columns because the transcript itself is
// stripped from the listing. Each one alone must select a row, and a row with
// none must not.
func TestHasIntroFields_EveryRetainedColumnSelects(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	cases := map[string]database.BookFileCore{
		"status":       {TranscribeStatus: rpStr("ok")},
		"time":         {IntroTranscribedAt: &now},
		"hash":         {IntroSourceHash: "hash-batman"},
		"run id":       {IntroRunOpID: "op-batman"},
		"title":        {TranscribedTitle: rpStr("Hush")},
		"author":       {TranscribedAuthor: rpStr("Selina Kyle")},
		"narrator":     {TranscribedNarrator: rpStr("Alfred Pennyworth")},
		"translator":   {TranscribedTranslator: rpStr("Lucius Fox")},
		"cover artist": {TranscribedCoverArtist: rpStr("Barbara Gordon")},
	}
	for name, c := range cases {
		if !hasIntroFields(&c) {
			t.Errorf("a row with only its %s is not selected", name)
		}
	}
	if hasIntroFields(&database.BookFileCore{ID: "plain", BookID: "b1"}) {
		t.Error("a row with no intro column was selected")
	}
}

// ErrModeCollect: a chunk that fails does not stop the chunks after it. One
// worker makes the order fixed, so a fail-fast pool would never reach them.
func TestReparseFileIntros_AFailedChunkDoesNotStopTheOthers(t *testing.T) {
	s := newReparseFakeStore()
	n := reparseChunkSize*3 + 5
	seedMany(s, n)
	s.failWrite[rpKey("b1", fid(1))] = true // chunk 0
	env := newReparseEnv(s, &fakeReporter{})
	env.workers = 1

	res, err := runReparseFileIntros(context.Background(), env, false)
	if err == nil || res.Errors != 1 {
		t.Fatalf("err = %v, errors = %d, want the one failure reported", err, res.Errors)
	}
	if want := n - 1; s.totalWrites() != want {
		t.Errorf("writes = %d, want %d: the chunks after the failed one were skipped", s.totalWrites(), want)
	}
}

// When the writer re-reads the row and finds nothing left to change (another
// writer stored the same classification between the op's read and the write),
// the file is examined and counted as no write.
func TestReparseFileIntros_AnotherWriterGotThereFirst(t *testing.T) {
	s := newReparseFakeStore()
	s.put(rpIntro("b1", "f01", rpNarr, "ok", rpNone, nil))
	theirs := introParseFromClip(transcribe.ParseClip(rpNarr))
	s.beforeDecide = func(st *reparseFakeStore, key string) {
		st.mu.Lock()
		defer st.mu.Unlock()
		p := theirs
		st.rows[key].IntroParse = &p
	}

	res, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), false)
	if err != nil {
		t.Fatal(err)
	}
	if res.ParseWrites != 0 || s.totalWrites() != 0 {
		t.Errorf("parse writes = %d, store writes = %d, want 0: the other writer's row was already current", res.ParseWrites, s.totalWrites())
	}
	if res.Examined != 1 || res.Reparsed != 1 || res.Selected != 1 {
		t.Errorf("selected %d examined %d reparsed %d, want 1 each", res.Selected, res.Examined, res.Reparsed)
	}
}

// A file that cannot be READ is an error, not a vanished row: it keeps its stored
// values, fails its chunk (so the watermark holds), and the books still balance.
func TestReparseFileIntros_ReadErrorIsAnErrorNotGone(t *testing.T) {
	s := newReparseFakeStore()
	seedMany(s, 5)
	s.failRead[rpKey("b1", fid(2))] = true

	res, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), false)
	if err == nil {
		t.Fatal("a failed read did not fail the run")
	}
	if res.Errors != 1 || res.Gone != 0 {
		t.Errorf("errors = %d, gone = %d, want 1 and 0", res.Errors, res.Gone)
	}
	if res.Selected != res.Examined+res.Gone+res.Errors || res.Examined != 4 {
		t.Errorf("selected %d examined %d gone %d errors %d", res.Selected, res.Examined, res.Gone, res.Errors)
	}
}

// R3-n3: a live write reports the size of the row the writer re-read under its lock,
// not the pre-lock read. Another writer grows the row (a fingerprint lands) between
// the op's read and the writer's decision; the live figure carries the fingerprint
// and the preview's, taken before it landed, does not.
func TestReparseFileIntros_LiveReportsTheSizeOfTheFreshRow(t *testing.T) {
	s := newReparseFakeStore()
	s.put(rpIntro("b1", "f1", rpTitle, "unparsed", rpNone, nil))
	prev, err := runReparseFileIntros(context.Background(), reparseFileIntrosEnv{reads: reparseReader{s}, reporter: &fakeReporter{}, workers: 1, now: rpClock}, true)
	if err != nil || prev.ParseWrites != 1 {
		t.Fatalf("preview: writes %d err %v", prev.ParseWrites, err)
	}
	s.beforeDecide = func(st *reparseFakeStore, key string) {
		st.mu.Lock()
		st.rows[key].AcoustIDFingerprint = make([]byte, 3000)
		st.mu.Unlock()
	}
	live, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), false)
	if err != nil || live.ParseWrites != 1 {
		t.Fatalf("live: writes %d err %v", live.ParseWrites, err)
	}
	stored := s.get("b1", "f1")
	want, err := database.IntroParseRowBytes(&stored, *stored.IntroParse, rpStamp)
	if err != nil {
		t.Fatal(err)
	}
	if live.ParseWriteBytes != int64(want) || live.ParseWriteBytes < prev.ParseWriteBytes+3000 {
		t.Errorf("live reported %d bytes, want the fresh row's %d (the pre-lock row was %d)", live.ParseWriteBytes, want, prev.ParseWriteBytes)
	}
}

// R3-n3: a write that was applied but whose durability is unknown is not an error
// row. It is counted as written and as unconfirmed, the books still balance, and the
// run ends failed with a message that says what happened.
func TestReparseFileIntros_AnUnconfirmedWriteIsCountedApartFromErrors(t *testing.T) {
	s := newReparseFakeStore()
	seedMany(s, 4)
	s.unconfirmed[rpKey("b1", fid(2))] = true
	res, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), false)
	if err == nil || !strings.Contains(err.Error(), "did not confirm them durable") || strings.Contains(err.Error(), "could not be read or written") {
		t.Fatalf("err = %v, want the unconfirmed-durability failure and not the read/write-failure one", err)
	}
	if res.WritesUnconfirmed != 1 || res.Errors != 0 || res.ParseWrites != 4 {
		t.Errorf("unconfirmed %d errors %d parse writes %d, want 1 / 0 / 4", res.WritesUnconfirmed, res.Errors, res.ParseWrites)
	}
	if res.Selected != res.Examined+res.Gone+res.Errors || res.Examined != 4 {
		t.Errorf("selected %d examined %d gone %d errors %d", res.Selected, res.Examined, res.Gone, res.Errors)
	}
	if row := s.get("b1", fid(2)); row.IntroParse == nil {
		t.Error("the unconfirmed row does not hold the parse it was applied with")
	}
	raw, _ := json.Marshal(res)
	if !strings.Contains(string(raw), `"writes_unconfirmed":1`) {
		t.Errorf("the result does not carry writes_unconfirmed: %s", raw)
	}
}

// R3-n2: the selection counts AUDIO files per book, as the single-file migration does:
// a cover-art row beside the one audio file does not make the book multi-file, and a
// book whose only row is not audio selects nothing.
func TestReparseFileIntros_TheSoleAudioFileIsSelectedBesideACoverArtRow(t *testing.T) {
	s := newReparseFakeStore()
	row := func(book, file, path string) {
		s.put(&database.BookFile{ID: file, BookID: book, FilePath: path, IntroTranscription: rpStr(rpTitle)})
	}
	row("cover", "a1", "/lib/cover/01.m4b")
	row("cover", "c1", "/lib/cover/cover.jpg")
	s.bookText["cover"] = rpTitle
	row("art", "c2", "/lib/art/cover.jpg")
	s.bookText["art"] = rpTitle
	row("two", "t1", "/lib/two/01.m4b")
	row("two", "t2", "/lib/two/02.m4b")
	s.bookText["two"] = rpTitle

	res, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Selected != 1 || res.SelectedByBookTranscript != 1 || res.NotSelected != 4 {
		t.Errorf("selected %d (by book transcript %d) not selected %d, want 1 / 1 / 4", res.Selected, res.SelectedByBookTranscript, res.NotSelected)
	}
	if s.get("cover", "a1").IntroParse == nil {
		t.Error("the sole audio file of a book with a cover-art row was not classified")
	}
	for _, id := range [][2]string{{"cover", "c1"}, {"art", "c2"}, {"two", "t1"}, {"two", "t2"}} {
		if s.get(id[0], id[1]).IntroParse != nil {
			t.Errorf("%s/%s was classified outside the documented selection", id[0], id[1])
		}
	}
}

// R3-n2: a selected sole audio file that holds no text of its own (the book-level op
// wrote the book's transcript, the migration never copied it) is read in full and
// counted as no_transcript, which is what SelectedByBookTranscript's doc says.
func TestReparseFileIntros_AnAudioFileWithoutTextSelectedThroughTheBookIsNoTranscript(t *testing.T) {
	s := newReparseFakeStore()
	s.put(&database.BookFile{ID: "n1", BookID: "nomigrate", FilePath: "/lib/nomigrate/01.m4b"})
	s.bookText["nomigrate"] = rpTitle
	res, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), false)
	if err != nil {
		t.Fatal(err)
	}
	if res.SelectedByBookTranscript != 1 || res.Unreparsable[reparseNoTranscript] != 1 || res.ParseWrites != 0 {
		t.Errorf("by book transcript %d, no_transcript %d, writes %d, want 1 / 1 / 0", res.SelectedByBookTranscript, res.Unreparsable[reparseNoTranscript], res.ParseWrites)
	}
}
