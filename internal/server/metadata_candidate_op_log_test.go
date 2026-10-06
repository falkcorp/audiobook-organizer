// file: internal/server/metadata_candidate_op_log_test.go
// version: 1.2.1
// guid: 42f7956b-7f83-42cb-adf4-96b00b38786d
// last-edited: 2026-10-06

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// outcomeRecorder is a resumeRecorder that also keeps each log line's level
// and each progress message, which the outcome-logging tests assert on.
type outcomeRecorder struct {
	*resumeRecorder
	mu       sync.Mutex
	lines    []recordedLine
	messages []string
}

type recordedLine struct {
	level slog.Level
	msg   string
}

func newOutcomeRecorder(opID string) *outcomeRecorder {
	return &outcomeRecorder{resumeRecorder: &resumeRecorder{opID: opID}}
}

func (r *outcomeRecorder) Log(level slog.Level, message string, attrs ...slog.Attr) error {
	r.mu.Lock()
	r.lines = append(r.lines, recordedLine{level, message})
	r.mu.Unlock()
	return r.resumeRecorder.Log(level, message, attrs...)
}

func (r *outcomeRecorder) UpdateProgress(current, total int, message string) error {
	r.mu.Lock()
	r.messages = append(r.messages, message)
	r.mu.Unlock()
	return r.resumeRecorder.UpdateProgress(current, total, message)
}

// linesWithPrefix returns the recorded lines whose message starts with prefix.
func (r *outcomeRecorder) linesWithPrefix(prefix string) []recordedLine {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []recordedLine
	for _, l := range r.lines {
		if strings.HasPrefix(l.msg, prefix) {
			out = append(out, l)
		}
	}
	return out
}

func (r *outcomeRecorder) lastMessage() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.messages) == 0 {
		return ""
	}
	return r.messages[len(r.messages)-1]
}

// querySource records every title it is asked for and answers with one
// candidate when the query is in hits, nothing otherwise.
type querySource struct {
	mu      sync.Mutex
	queries []string
	hits    map[string]metadata.BookMetadata
}

func (q *querySource) Name() string { return "QuerySrc" }
func (q *querySource) answer(title string) []metadata.BookMetadata {
	q.mu.Lock()
	q.queries = append(q.queries, title)
	q.mu.Unlock()
	if m, ok := q.hits[title]; ok {
		return []metadata.BookMetadata{m}
	}
	return nil
}
func (q *querySource) SearchByTitle(_ context.Context, title string) ([]metadata.BookMetadata, error) {
	return q.answer(title), nil
}
func (q *querySource) SearchByTitleAndAuthor(_ context.Context, title, _ string) ([]metadata.BookMetadata, error) {
	return q.answer(title), nil
}
func (q *querySource) asked() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.queries...)
}

// TestCandidateFetch_LogsEveryOutcomeAndCounts runs the real op over one book
// per outcome and checks that each gets its own op-log line at the right
// level, and that the final progress message carries the counts by outcome.
//
// It also pins the empty-title fix: a book titled "" is searched by its
// transcribed title (book-level or file-level), a book with neither is
// skipped as "no usable title", and no provider is ever asked for "" or a
// placeholder.
func TestCandidateFetch_LogsEveryOutcomeAndCounts(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	store := s.storeForWiring()

	src := &querySource{hits: map[string]metadata.BookMetadata{
		"Real Title":           {Title: "Real Title", Author: "Real Author"},
		"Marvel's Planet Hulk": {Title: "Marvel's Planet Hulk", Author: "Greg Pak"},
		"File Level Title":     {Title: "File Level Title", Author: "Someone"},
	}}
	mfs := metafetch.NewService(store)
	mfs.SetOverrideSources([]metadata.MetadataSource{src})
	s.metadataFetchService = mfs

	mk := func(b *database.Book) string {
		t.Helper()
		got, err := store.CreateBook(b)
		if err != nil {
			t.Fatalf("CreateBook: %v", err)
		}
		return got.ID
	}
	hulk := "Marvel's Planet Hulk"
	matched := mk(&database.Book{Title: "Real Title", FilePath: "/lib/real/book.m4b"})
	transcribed := mk(&database.Book{Title: "", TranscribedTitle: &hulk, FilePath: "/lib/hulk/book.m4b"})
	// An earlier "" search left junk in the per-provider fetch cache under the
	// row's "" identity. Searching by the transcribed title must not replay it.
	junk, _ := json.Marshal([]metadata.BookMetadata{{Title: "Bad in Bed", Author: "Somebody Else"}})
	if err := database.PutCachedMetadataFetch(store, transcribed, src.Name(),
		database.MetadataSearchIdentity("", "", nil, nil, nil), junk, 0.9); err != nil {
		t.Fatalf("seed junk fetch cache: %v", err)
	}
	placeholder := mk(&database.Book{Title: "Unknown Title", FilePath: "/library/Unknown Author/Unknown Title/book.m4b"})
	fileLevel := mk(&database.Book{Title: "", FilePath: "/lib/fl/book.m4b"})
	fileTitle := "File Level Title"
	if err := store.CreateBookFile(&database.BookFile{
		ID: "fl-1", BookID: fileLevel, FilePath: "/lib/fl/book.m4b", TrackNumber: 1, TranscribedTitle: &fileTitle,
	}); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	noMatch := mk(&database.Book{Title: "Nobody Catalogued This", FilePath: "/lib/nm/book.m4b"})
	const missing = "book-that-does-not-exist"

	ids := []string{matched, transcribed, placeholder, fileLevel, noMatch, missing}
	params, err := json.Marshal(metadataCandidateFetchOpParams{BookIDs: ids})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec := newOutcomeRecorder("op-outcome-log")
	if err := s.runMetadataCandidateFetchOp(context.Background(), params, rec); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Never searched with an empty or placeholder title.
	for _, q := range src.asked() {
		if strings.TrimSpace(q) == "" || q == "Unknown Title" {
			t.Errorf("a provider was asked for %q (all: %q)", q, src.asked())
		}
	}

	rows, err := store.GetOperationResults("op-outcome-log")
	if err != nil {
		t.Fatalf("GetOperationResults: %v", err)
	}
	byID := map[string]CandidateResult{}
	for _, r := range rows {
		var cr CandidateResult
		if err := json.Unmarshal([]byte(r.ResultJSON), &cr); err != nil {
			t.Fatalf("decode: %v", err)
		}
		byID[r.BookID] = cr
	}
	if r := byID[transcribed]; r.Status != "matched" || r.SearchQuery != hulk ||
		r.SearchQuerySource != metabatch.SearchQuerySourceTranscribedTitle {
		t.Errorf("empty-title book: status %q query %q source %q, want matched via transcribed_title %q",
			r.Status, r.SearchQuery, r.SearchQuerySource, hulk)
	}
	if r := byID[fileLevel]; r.Status != "matched" || r.SearchQuerySource != metabatch.SearchQuerySourceFileTranscribedText {
		t.Errorf("file-transcribed book: status %q source %q, want matched via %s",
			r.Status, r.SearchQuerySource, metabatch.SearchQuerySourceFileTranscribedText)
	}
	if r := byID[placeholder]; r.Status != "skipped" || !strings.Contains(r.Error, metabatch.SkipReasonNoUsableTitle) {
		t.Errorf("placeholder-title book: status %q error %q, want skipped: %s", r.Status, r.Error, metabatch.SkipReasonNoUsableTitle)
	}

	// One line per book, at the right level.
	for prefix, want := range map[string]struct {
		n     int
		level slog.Level
	}{
		"matched: ":  {3, slog.LevelInfo},
		"no match: ": {1, slog.LevelInfo},
		"skipped: ":  {1, slog.LevelInfo},
		"error: ":    {1, slog.LevelWarn},
	} {
		got := rec.linesWithPrefix(prefix)
		if len(got) != want.n {
			t.Errorf("%q lines = %d, want %d (%v)", prefix, len(got), want.n, got)
			continue
		}
		for _, l := range got {
			if l.level != want.level {
				t.Errorf("%q line at level %v, want %v: %s", prefix, l.level, want.level, l.msg)
			}
		}
	}
	hulkLine := false
	for _, l := range rec.linesWithPrefix("matched: ") {
		if strings.Contains(l.msg, `searched transcribed_title by "Marvel's Planet Hulk"`) &&
			strings.Contains(l.msg, `→ "Marvel's Planet Hulk" by "Greg Pak" (QuerySrc, score`) {
			hulkLine = true
		}
	}
	if !hulkLine {
		t.Errorf("no matched line names the transcribed query and the candidate: %v", rec.linesWithPrefix("matched: "))
	}
	if got := rec.linesWithPrefix("skipped: "); len(got) == 1 && (!strings.Contains(got[0].msg, "no usable title") || !strings.Contains(got[0].msg, `"/library/Unknown Author/Unknown Title/book.m4b"`)) {
		t.Errorf("skipped line lacks its reason or the book's path: %s", got[0].msg)
	}
	if got := rec.linesWithPrefix("no match: "); len(got) == 1 && !strings.Contains(got[0].msg, `searched title "Nobody Catalogued This"`) {
		t.Errorf("no-match line lacks the query: %s", got[0].msg)
	}

	wantFinal := "completed 6/6 books — matched 3, no match 1, skipped 1, deferred 0, errors 1, from cache 0, known empty 0"
	if got := rec.lastMessage(); got != wantFinal {
		t.Errorf("final progress = %q, want %q", got, wantFinal)
	}
}

// TestCandidateFetch_TranscribedEmptyBookIsNotRefetched: a blank-title book
// searched by its transcribed title that no provider answers must get the
// same "known empty" short-circuit as any other book. The cache row is hashed
// with the query actually asked, so a verdict that only recognised rows hashed
// with the book's own "" title re-asked every provider on every run.
func TestCandidateFetch_TranscribedEmptyBookIsNotRefetched(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	store := s.storeForWiring()

	tt := "A Transcribed Title Nobody Has"
	book, err := store.CreateBook(&database.Book{Title: "", TranscribedTitle: &tt, FilePath: "/lib/tt/book.m4b"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	a := &countingSource{name: "SrcA"}
	mfs := metafetch.NewService(store)
	mfs.SetOverrideSources([]metadata.MetadataSource{a})
	s.metadataFetchService = mfs

	first := runCandidateFetch(t, s, "op-tt-1", []string{book.ID}, false)
	if first[book.ID].Status != "no_match" || first[book.ID].SearchQuery != tt {
		t.Fatalf("first run = %+v, want no_match searched by %q", first[book.ID], tt)
	}
	before := a.calls.Load()
	if before == 0 {
		t.Fatal("first run made no provider call")
	}
	second := runCandidateFetch(t, s, "op-tt-2", []string{book.ID}, false)
	if got := a.calls.Load() - before; got != 0 {
		t.Fatalf("second run re-asked the provider %d times, want 0", got)
	}
	if r := second[book.ID]; r.Status != "no_match" || r.Cached != candidateCachedKnownEmpty {
		t.Fatalf("second run = {status %q, cached %q}, want {no_match, %q}", r.Status, r.Cached, candidateCachedKnownEmpty)
	}
}

// A chapter-number or chapter-fragment title is searched by its transcribed
// title, then its folder name, never verbatim; the book's own title is left
// alone, and each path's outcome line names the query it used.
func TestCandidateFetch_ChapterTitleFallbacks(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	store := s.storeForWiring()

	src := &querySource{hits: map[string]metadata.BookMetadata{
		"Eldest":         {Title: "Eldest", Author: "Christopher Paolini"},
		"World War Hulk": {Title: "World War Hulk", Author: "Greg Pak"},
	}}
	mfs := metafetch.NewService(store)
	mfs.SetOverrideSources([]metadata.MetadataSource{src})
	s.metadataFetchService = mfs

	wwh := "World War Hulk"
	cases := []struct {
		name, title, path string
		transcribed       *string
		wantStatus        string
		wantSource        string
		wantQuery         string
		wantLine          string
	}{
		{name: "chapter number via transcription", title: "Chapter 3", path: "/library/x/y/03.mp3", transcribed: &wwh,
			wantStatus: "matched", wantSource: metabatch.SearchQuerySourceTranscribedTitle, wantQuery: wwh,
			wantLine: `[searched transcribed_title by "World War Hulk"]`},
		{name: "bare number via folder", title: "98", path: "/library/Paolini/Eldest/98.mp3",
			wantStatus: "matched", wantSource: metabatch.SearchQuerySourceFolderTitle, wantQuery: "Eldest",
			wantLine: `[searched folder_title by "Eldest"]`},
		{name: "fragment with nothing usable", title: "06 Chapter 6", path: "/library/06 Chapter 6.mp3",
			wantStatus: "skipped", wantLine: "chapter fragment, no usable title"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := store.CreateBook(&database.Book{Title: tc.title, FilePath: tc.path, TranscribedTitle: tc.transcribed})
			if err != nil {
				t.Fatalf("CreateBook: %v", err)
			}
			rec := newOutcomeRecorder(fmt.Sprintf("op-chapter-%d", i))
			params, _ := json.Marshal(metadataCandidateFetchOpParams{BookIDs: []string{b.ID}})
			if err := s.runMetadataCandidateFetchOp(context.Background(), params, rec); err != nil {
				t.Fatalf("run: %v", err)
			}
			rows, _ := store.GetOperationResults(fmt.Sprintf("op-chapter-%d", i))
			if len(rows) != 1 {
				t.Fatalf("rows = %d, want 1", len(rows))
			}
			var cr CandidateResult
			_ = json.Unmarshal([]byte(rows[0].ResultJSON), &cr)
			if cr.Status != tc.wantStatus || cr.SearchQuerySource != tc.wantSource || cr.SearchQuery != tc.wantQuery {
				t.Errorf("result = {%s %s %q}, want {%s %s %q}", cr.Status, cr.SearchQuerySource, cr.SearchQuery,
					tc.wantStatus, tc.wantSource, tc.wantQuery)
			}
			found := false
			for _, l := range rec.linesWithPrefix("") {
				found = found || strings.Contains(l.msg, tc.wantLine)
			}
			if !found {
				t.Errorf("no op log line contains %q: %v", tc.wantLine, rec.linesWithPrefix(""))
			}
			got, _ := store.GetBookByID(b.ID)
			if got == nil || got.Title != tc.title {
				t.Errorf("book title changed to %v; the fallback is only a query", got)
			}
		})
	}
	for _, q := range src.asked() {
		if q == "Chapter 3" || q == "98" || q == "06 Chapter 6" {
			t.Errorf("a provider was asked for the raw chapter title %q (all: %q)", q, src.asked())
		}
	}
}

// TestCandidateFetchTally_ProgressLineCadence pins the progress message: it
// carries the counts, and it is rebuilt only every candidateFetchProgressEvery
// books (and on the last), so the op log gets one progress row per cadence
// rather than one per book.
func TestCandidateFetchTally_ProgressLineCadence(t *testing.T) {
	var tally candidateFetchTally
	total := 2*candidateFetchProgressEvery + 3
	distinct := map[string]bool{}
	var last string
	for i := 1; i <= total; i++ {
		switch {
		case i%10 == 0:
			tally.record(CandidateResult{Status: "error"})
		case i%7 == 0:
			tally.record(CandidateResult{Status: "no_match", Cached: candidateCachedKnownEmpty})
		case i%5 == 0:
			tally.record(CandidateResult{Status: "skipped"})
		default:
			tally.record(CandidateResult{Status: "matched", Cached: candidateCachedCandidates})
		}
		last = tally.progressLine(int64(i), total)
		distinct[last] = true
	}
	// First book, two cadence points, and the last book.
	if len(distinct) != 4 {
		t.Errorf("progress message changed %d times over %d books, want 4: %v", len(distinct), total, distinct)
	}
	want := "fetched 53/53 — matched 37, no match 7, skipped 4, deferred 0, errors 5, from cache 37, known empty 7"
	if last != want {
		t.Errorf("last progress line = %q, want %q", last, want)
	}
}

// TestCandidateOutcomeLine_SanitizesUserValues: a title carrying a newline
// must not forge a second op-log line.
func TestCandidateOutcomeLine_SanitizesUserValues(t *testing.T) {
	_, msg, _ := candidateOutcomeLine(CandidateResult{
		Book:   CandidateBookInfo{ID: "b1", Title: "Evil\nINFO fake line", Author: "A"},
		Status: "skipped",
		Error:  "skipped: chapter fragment",
	})
	if strings.Contains(msg, "\n") {
		t.Fatalf("outcome line contains a raw newline: %q", msg)
	}
	if !strings.HasPrefix(msg, "skipped: ") || !strings.Contains(msg, "chapter fragment") {
		t.Errorf("unexpected skipped line: %q", msg)
	}
}

// A no-match line names the author the search actually asked with, never a
// placeholder that was not sent.
func TestCandidateOutcomeLine_NoMatchNamesSearchedAuthor(t *testing.T) {
	cases := []struct {
		name         string
		bookAuthor   string
		searchAuthor string
		want         string
		notWant      string
	}{
		{name: "real author sent", bookAuthor: "Greg Pak", searchAuthor: "Greg Pak", want: `author "Greg Pak"`},
		{name: "placeholder not sent", bookAuthor: "Unknown Author",
			want: `no author hint (placeholder "Unknown Author" not sent)`, notWant: `author "Unknown Author":`},
		{name: "narrator placeholder not sent", bookAuthor: "read by narrator",
			want: `no author hint (placeholder "read by narrator" not sent)`},
		{name: "no author at all", want: "no author hint"},
		{name: "resolved author differs from snapshot", searchAuthor: "Andrzej Sapkowski", want: `author "Andrzej Sapkowski"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, msg, _ := candidateOutcomeLine(CandidateResult{
				Book:         CandidateBookInfo{ID: "b1", Title: "Planet Hulk", Author: tc.bookAuthor},
				Status:       "no_match",
				SearchQuery:  "Planet Hulk",
				SearchAuthor: tc.searchAuthor,
			})
			if !strings.Contains(msg, tc.want) {
				t.Errorf("line %q lacks %q", msg, tc.want)
			}
			if tc.notWant != "" && strings.Contains(msg, tc.notWant) {
				t.Errorf("line %q claims %q", msg, tc.notWant)
			}
		})
	}
}
