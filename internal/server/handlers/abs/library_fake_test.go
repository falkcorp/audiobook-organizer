// file: internal/server/handlers/abs/library_fake_test.go
// version: 1.2.0
// guid: 1d4a67f2-0c85-4f39-9b6e-3a71c5d0e824
// last-edited: 2026-08-12

package abs_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/conformance"
)

// fakeLibrary is an in-memory stand-in for the four capability interfaces the
// browse + playback surface consumes (LibraryStore, IdentityStore, ChapterStore,
// ProgressStore). *database.PebbleStore satisfies all four in production.
//
// It deliberately mints sync ids the same way the real keyspaces do — a 36-char
// UUID for the item, an opaque string for the file — because those two id shapes
// are load-bearing client contract (§1.7.1) and a fake that handed back raw
// ULIDs would let a regression through.
type fakeLibrary struct {
	mu sync.Mutex

	// order preserves seed order, which is what the list endpoints iterate.
	order    []string
	books    map[string]*database.Book
	files    map[string][]database.BookFile
	chapters map[string][]database.Chapter

	authors    []database.Author
	bookAuthor map[string][]database.Author
	narrators  []database.Narrator
	bookNarr   map[string][]database.Narrator
	series     map[int]*database.Series

	syncIDs     map[string]string // bookID -> syncID
	syncItems   map[string]*database.SyncItem
	syncFiles   map[string]string // bookID|fileID -> syncFileID
	syncFileRev map[string]database.SyncFile

	positions map[string]*database.UserPosition  // userID|bookID
	states    map[string]*database.UserBookState // userID|bookID

	nextUUID int
	listErr  error

	// countFiltered counts CountBookSummariesFiltered calls so a test can prove the
	// full-library count scan is cached rather than repeated per request.
	countFiltered int
	authorCounts  int
}

// authorCountCalls reports how many times the full author-count scan ran. The
// jump-to-letter interaction issues up to 93 paged requests in a row, so the value
// that matters is how many of those trigger a rebuild.
func (f *fakeLibrary) authorCountCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authorCounts
}

// countCalls reports how many times the filtered count was actually computed.
func (f *fakeLibrary) countCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.countFiltered
}

// timeNowForSeed is a fixed timestamp for seeded rows. Fixed rather than time.Now so
// ms-epoch assertions stay stable.
func timeNowForSeed() time.Time { return time.UnixMilli(1785370201500) }

func newFakeLibrary() *fakeLibrary {
	return &fakeLibrary{
		books:       map[string]*database.Book{},
		files:       map[string][]database.BookFile{},
		chapters:    map[string][]database.Chapter{},
		bookAuthor:  map[string][]database.Author{},
		bookNarr:    map[string][]database.Narrator{},
		series:      map[int]*database.Series{},
		syncIDs:     map[string]string{},
		syncItems:   map[string]*database.SyncItem{},
		syncFiles:   map[string]string{},
		syncFileRev: map[string]database.SyncFile{},
		positions:   map[string]*database.UserPosition{},
		states:      map[string]*database.UserBookState{},
	}
}

// ── seeding ─────────────────────────────────────────────────────────────────

func (f *fakeLibrary) addBook(b *database.Book, files []database.BookFile, chs []database.Chapter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, b.ID)
	f.books[b.ID] = b
	f.files[b.ID] = files
	f.chapters[b.ID] = chs
}

func (f *fakeLibrary) addAuthor(id int, name string, bookIDs ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := database.Author{ID: id, Name: name}
	f.authors = append(f.authors, a)
	for _, bid := range bookIDs {
		f.bookAuthor[bid] = append(f.bookAuthor[bid], a)
	}
}

// addNarrators seeds narrator rows. The oracle's fixture library has none, so any
// test asserting the narrator ELEMENT shape must seed its own — an empty list is
// exactly how a missing required field shipped unnoticed.
// Narrators are ATTACHED TO A BOOK, not just added to a standalone list: the ABS
// narrator list is derived from the visible books' junction rows, so a narrator with
// no book is correctly invisible. Attaching to the first seeded book makes them
// visible without inventing a new one.
// attachNarrators binds narrators to a specific book's junction rows.
func (f *fakeLibrary) attachNarrators(bookID string, names ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, name := range names {
		n := database.Narrator{ID: len(f.narrators) + i + 1, Name: name}
		f.narrators = append(f.narrators, n)
		f.bookNarr[bookID] = append(f.bookNarr[bookID], n)
	}
}

func (f *fakeLibrary) addNarrators(names ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var target string
	if len(f.order) > 0 {
		target = f.order[0]
	}
	for i, name := range names {
		n := database.Narrator{ID: len(f.narrators) + i + 1, Name: name}
		f.narrators = append(f.narrators, n)
		if target != "" {
			f.bookNarr[target] = append(f.bookNarr[target], n)
		}
	}
}

// ── LibraryStore ────────────────────────────────────────────────────────────

func (f *fakeLibrary) GetBookByID(id string) (*database.Book, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.books[id], nil
}

func (f *fakeLibrary) GetBooksByIDs(ids []string) ([]database.Book, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]database.Book, 0, len(ids))
	for _, id := range ids {
		if b, ok := f.books[id]; ok {
			out = append(out, *b)
		}
	}
	return out, nil
}

func (f *fakeLibrary) GetAllBookSummaries(limit, offset int) ([]database.BookSummary, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []database.BookSummary{}
	for i, id := range f.order {
		if i < offset {
			continue
		}
		if limit > 0 && len(out) >= limit {
			break
		}
		b := f.books[id]
		out = append(out, database.BookSummary{ID: b.ID, Title: b.Title, CreatedAt: b.CreatedAt})
	}
	return out, nil
}

// matchesFilter applies the same predicates the real store applies, so a test that
// seeds a non-primary or unorganized book actually sees it excluded rather than
// trusting the handler to have asked for the right thing.
func (f *fakeLibrary) matchesFilter(b *database.Book, sum database.BookSummary, fl database.BookSummaryFilter) bool {
	if fl.IsPrimaryVersion != nil {
		isPrimary := b.IsPrimaryVersion == nil || *b.IsPrimaryVersion
		if isPrimary != *fl.IsPrimaryVersion {
			return false
		}
	}
	if fl.LibraryState != "" {
		state := ""
		if b.LibraryState != nil {
			state = *b.LibraryState
		}
		if state != fl.LibraryState {
			return false
		}
	}
	if fl.ExcludeQuarantined && b.QuarantinedAt != nil {
		return false
	}
	_ = sum
	return true
}

// filteredSummaries returns the seeded books matching fl, in the requested order.
func (f *fakeLibrary) filteredSummaries(fl database.BookSummaryFilter) []database.BookSummary {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []database.BookSummary{}
	for _, id := range f.order {
		b := f.books[id]
		if b == nil {
			continue
		}
		// Mirror the REAL summary projection for the narrator tiers. Without
		// these two fields the fake silently cannot represent a book whose
		// narrator lives outside the junction table, and any test asserting
		// three-tier resolution would pass vacuously.
		sum := database.BookSummary{
			ID:            b.ID,
			Title:         b.Title,
			Narrator:      b.Narrator,
			NarratorsJSON: b.NarratorsJSON,
		}
		if f.matchesFilter(b, sum, fl) {
			out = append(out, sum)
		}
	}
	if fl.SortBy == "title" {
		sort.SliceStable(out, func(i, j int) bool {
			if fl.SortAscending {
				return out[i].Title < out[j].Title
			}
			return out[i].Title > out[j].Title
		})
	}
	return out
}

func (f *fakeLibrary) CountBookSummariesFiltered(fl database.BookSummaryFilter) (int, error) {
	if f.listErr != nil {
		return 0, f.listErr
	}
	n := len(f.filteredSummaries(fl))
	f.mu.Lock()
	f.countFiltered++
	f.mu.Unlock()
	return n, nil
}

func (f *fakeLibrary) GetAllBookSummariesFiltered(limit, offset int, fl database.BookSummaryFilter) ([]database.BookSummary, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	all := f.filteredSummaries(fl)
	if offset >= len(all) {
		return []database.BookSummary{}, nil
	}
	end := offset + limit
	if limit <= 0 || end > len(all) {
		end = len(all)
	}
	return all[offset:end], nil
}

func (f *fakeLibrary) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []database.BookCore{}
	for i, id := range f.order {
		if i < offset {
			continue
		}
		if limit > 0 && len(out) >= limit {
			break
		}
		b := f.books[id]
		out = append(out, database.BookCore{
			ID: b.ID, Title: b.Title, PrintYear: b.PrintYear,
			AudiobookReleaseYear: b.AudiobookReleaseYear,
		})
	}
	return out, nil
}

func (f *fakeLibrary) CountAllBooks() (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.order), nil
}

func (f *fakeLibrary) SearchBooks(query string, limit, offset int) ([]database.Book, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := strings.ToLower(query)
	out := []database.Book{}
	for i, id := range f.order {
		b := f.books[id]
		if !strings.Contains(strings.ToLower(b.Title), q) {
			continue
		}
		if i < offset {
			continue
		}
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, *b)
	}
	return out, nil
}

func (f *fakeLibrary) GetBookFiles(bookID string) ([]database.BookFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.files[bookID], nil
}

func (f *fakeLibrary) GetAuthorsByBookIDs(_ context.Context, ids []string) (map[string][]database.Author, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string][]database.Author{}
	for _, id := range ids {
		if a, ok := f.bookAuthor[id]; ok {
			out[id] = a
		}
	}
	return out, nil
}

func (f *fakeLibrary) GetNarratorsByBookIDs(_ context.Context, ids []string) (map[string][]database.Narrator, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string][]database.Narrator{}
	for _, id := range ids {
		if n, ok := f.bookNarr[id]; ok {
			out[id] = n
		}
	}
	return out, nil
}

func (f *fakeLibrary) GetSeriesByIDs(ids []int) (map[int]*database.Series, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[int]*database.Series{}
	for _, id := range ids {
		if s, ok := f.series[id]; ok {
			out[id] = s
		}
	}
	return out, nil
}

func (f *fakeLibrary) GetAllAuthors() ([]database.Author, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]database.Author(nil), f.authors...), nil
}

func (f *fakeLibrary) GetAllAuthorBookCounts() (map[int]int, error) {
	f.mu.Lock()
	f.authorCounts++
	f.mu.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[int]int{}
	for _, list := range f.bookAuthor {
		for _, a := range list {
			out[a.ID]++
		}
	}
	return out, nil
}

func (f *fakeLibrary) GetAllSeries() ([]database.Series, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []database.Series{}
	for _, s := range f.series {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeLibrary) GetAllSeriesBookCounts() (map[int]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[int]int{}
	for _, b := range f.books {
		if b.SeriesID != nil {
			out[*b.SeriesID]++
		}
	}
	return out, nil
}

func (f *fakeLibrary) ListNarrators() ([]database.Narrator, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]database.Narrator(nil), f.narrators...), nil
}

func (f *fakeLibrary) GetDistinctGenres() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[string]bool{}
	out := []string{}
	for _, b := range f.books {
		if b.Genre == nil {
			continue
		}
		for _, g := range strings.Split(*b.Genre, ",") {
			g = strings.TrimSpace(g)
			if g != "" && !seen[g] {
				seen[g] = true
				out = append(out, g)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func (f *fakeLibrary) GetDistinctLanguages() ([]string, error) { return []string{}, nil }

// ── IdentityStore ───────────────────────────────────────────────────────────

// nextSyncID mints a deterministic 36-char canonical UUID so tests can assert
// the length invariant of §1.7.1 without depending on randomness.
func (f *fakeLibrary) nextSyncID() string {
	f.nextUUID++
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", f.nextUUID, f.nextUUID)
}

func (f *fakeLibrary) MintOrGetSyncID(bookID string) (string, error) {
	if bookID == "" {
		return "", fmt.Errorf("bookID required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.syncIDs[bookID]; ok {
		return id, nil
	}
	id := f.nextSyncID()
	f.syncIDs[bookID] = id
	f.syncItems[id] = &database.SyncItem{SyncID: id, CurrentBookID: bookID, CreatedAt: time.Now()}
	return id, nil
}

func (f *fakeLibrary) ResolveSyncItem(syncID string) (*database.SyncItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.syncItems[syncID]
	if !ok {
		return nil, nil
	}
	cp := *it
	return &cp, nil
}

func (f *fakeLibrary) MintOrGetSyncFileID(bookID, fileID string) (string, error) {
	if bookID == "" || fileID == "" {
		return "", fmt.Errorf("bookID and fileID required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := bookID + "|" + fileID
	if id, ok := f.syncFiles[key]; ok {
		return id, nil
	}
	id := fmt.Sprintf("sf%03d", len(f.syncFiles)+1)
	f.syncFiles[key] = id
	f.syncFileRev[id] = database.SyncFile{SyncFileID: id, BookID: bookID, CurrentFileID: fileID}
	return id, nil
}

func (f *fakeLibrary) GetSyncFileID(bookID, fileID string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.syncFiles[bookID+"|"+fileID]
	return id, ok, nil
}

func (f *fakeLibrary) ListSyncFilesForBook(bookID string) ([]database.SyncFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []database.SyncFile{}
	for _, sf := range f.syncFileRev {
		if sf.BookID == bookID {
			out = append(out, sf)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SyncFileID < out[j].SyncFileID })
	return out, nil
}

// ── ChapterStore ────────────────────────────────────────────────────────────

func (f *fakeLibrary) GetChaptersForBook(bookID string) ([]database.Chapter, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.chapters[bookID], nil
}

// ── ProgressStore ───────────────────────────────────────────────────────────

func (f *fakeLibrary) GetUserPosition(userID, bookID string) (*database.UserPosition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.positions[userID+"|"+bookID], nil
}

func (f *fakeLibrary) SetUserPosition(userID, bookID, segmentID string, pos float64) error {
	if userID == "" || bookID == "" || segmentID == "" {
		return fmt.Errorf("user/book/segment required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.positions[userID+"|"+bookID] = &database.UserPosition{
		UserID: userID, BookID: bookID, SegmentID: segmentID,
		PositionSeconds: pos, UpdatedAt: time.Now(),
	}
	return nil
}

// ClearUserPositions backs DELETE /api/me/progress/:id. It drops the (user, book)
// entry entirely rather than zeroing it, matching PebbleStore's batch delete —
// GetUserPosition must answer nil afterwards, not a row reading 0.
func (f *fakeLibrary) ClearUserPositions(userID, bookID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.positions, userID+"|"+bookID)
	return nil
}

func (f *fakeLibrary) GetUserBookState(userID, bookID string) (*database.UserBookState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states[userID+"|"+bookID], nil
}

func (f *fakeLibrary) SetUserBookState(s *database.UserBookState) error {
	if s == nil || s.UserID == "" || s.BookID == "" {
		return fmt.Errorf("user and book required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *s
	f.states[s.UserID+"|"+s.BookID] = &cp
	return nil
}

// ── oracle seed ─────────────────────────────────────────────────────────────

// oracleLibrary reproduces the exact library the real ABS 2.36.0 server held when
// testdata/abs-fixtures/ was captured: two copies of The Odyssey, one a single m4b
// with six embedded chapters, one six mp3 tracks with six synthesized chapters.
//
// The counts matter: the conformance differ compares array LENGTHS as well as
// element types, so a seed that differs in cardinality fails for a reason that has
// nothing to do with the code under test.
type oracleSeed struct {
	lib      *fakeLibrary
	root     string
	singleID string
	multiID  string
}

func seedOracleLibrary(t *testing.T) *oracleSeed {
	t.Helper()
	root := t.TempDir()
	lib := newFakeLibrary()

	// Distinct creation times, matching the oracle's own addedAt values. The
	// multi-file copy was added LAST, which is why the Recently Added shelf lists it
	// first — a shared timestamp would make that shelf's order arbitrary.
	created := time.UnixMilli(1785370201391)
	createdMulti := time.UnixMilli(1785370201438)
	strp := func(s string) *string { return &s }
	boolp := func(b bool) *bool { return &b }
	// The ABS item list serves ONLY primary + organized books (see absItemFilter), so
	// the fixture library must look like a normal organized library or every browse
	// test would assert against an empty list.
	organized, primary := strp("organized"), boolp(true)
	intp := func(i int) *int { return &i }

	// ── book 1: single-file m4b, 6 embedded chapters ─────────────────────────
	singleDir := filepath.Join(root, "Homer", "The Odyssey (Single File)")
	if err := os.MkdirAll(singleDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	m4b := filepath.Join(singleDir, "odyssey.m4b")
	writeFakeAudio(t, m4b, 4096)
	single := &database.Book{
		ID: "01SINGLEFILEODYSSEYBOOK00", Title: "The Odyssey",
		FilePath: m4b, Format: "QuickTime / MOV", Duration: intp(9975),
		Genre: strp("Audiobook"), PrintYear: intp(800),
		LibraryState: organized, IsPrimaryVersion: primary,
		CreatedAt: &created, UpdatedAt: &created,
	}
	// The oracle's m4b carries NO track tag and no track number in its filename, so
	// both trackNumFrom* are null there. Seeded that way deliberately: an earlier
	// version of the mapper fabricated an index, which the conformance diff caught.
	lib.addBook(single, []database.BookFile{{
		ID: "f-m4b", BookID: single.ID, FilePath: m4b, Format: "QuickTime / MOV", Codec: "aac",
		Duration: 9975, FileSize: 4096,
		BitrateKbps: 96, SampleRateHz: 22050, Channels: 1,
		RawTags: map[string]string{
			"album": "The Odyssey", "artist": "Homer", "date": "800BC",
			"encoder": "Lavf62.3.100", "genre": "Audiobook", "title": "The Odyssey",
		},
		CreatedAt: created, UpdatedAt: created,
	}}, sixChapters())

	// ── book 2: six mp3 tracks, 6 synthesized chapters ──────────────────────
	multiDir := filepath.Join(root, "Homer", "The Odyssey")
	if err := os.MkdirAll(multiDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	multi := &database.Book{
		ID: "01MULTIFILEODYSSEYBOOK000", Title: "The Odyssey",
		Format: "mp3", Duration: intp(9975),
		Genre:        strp("Speech"),
		Description:  strp("http://archive.org/details/odyssey_butler_librivox"),
		LibraryState: organized, IsPrimaryVersion: primary,
		CreatedAt: &createdMulti, UpdatedAt: &createdMulti,
	}
	var mp3s []database.BookFile
	for i := 1; i <= 6; i++ {
		p := filepath.Join(multiDir, fmt.Sprintf("odyssey_%02d_homer_butler_64kb.mp3", i))
		writeFakeAudio(t, p, 2048+i)
		// Track 6 deliberately carries no "track" tag, mirroring the oracle: its
		// trackNumFromFilename is 6 while its trackNumFromMeta is null. That asymmetry
		// is the whole reason the two fields are not interchangeable.
		rawTags := map[string]string{
			"album": "The Odyssey", "artist": "Homer, transl. Samuel Butler",
			"comment": "http://archive.org/details/odyssey_butler_librivox",
			"encoder": "LAME 64bits version 3.98.4 (http://www.mp3dev.org/)",
			"genre":   "Speech", "title": fmt.Sprintf("The Odyssey: Book %02d", i),
		}
		if i < 6 {
			rawTags["track"] = fmt.Sprintf("%d/24", i)
		}
		mp3s = append(mp3s, database.BookFile{
			ID: fmt.Sprintf("f-mp3-%d", i), BookID: multi.ID, FilePath: p,
			Format: "MP2/3 (MPEG audio layer 2/3)", Codec: "mp3", Duration: 1662, FileSize: int64(2048 + i),
			TrackNumber: i, TrackCount: 24, Title: fmt.Sprintf("The Odyssey: Book %02d", i),
			BitrateKbps: 64, SampleRateHz: 22050, Channels: 1,
			RawTags:   rawTags,
			CreatedAt: created, UpdatedAt: created,
		})
	}
	multi.FilePath = mp3s[0].FilePath
	lib.addBook(multi, mp3s, nil) // nil chapters => synthesized, one per track

	lib.addAuthor(1, "Homer", single.ID)
	lib.addAuthor(2, "transl. Samuel Butler Homer", multi.ID)

	return &oracleSeed{lib: lib, root: root, singleID: single.ID, multiID: multi.ID}
}

func sixChapters() []database.Chapter {
	out := make([]database.Chapter, 6)
	start := 0.0
	for i := range out {
		end := start + 1662.5
		out[i] = database.Chapter{ID: i, StartSec: start, EndSec: end, Title: fmt.Sprintf("Book %02d", i+1)}
		start = end
	}
	return out
}

// writeFakeAudio writes deterministic bytes so Range assertions can name exact
// offsets. The content is not real audio: nothing under test decodes it.
func writeFakeAudio(t *testing.T, path string, size int) {
	t.Helper()
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = byte(i % 251)
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// ── harness plumbing ────────────────────────────────────────────────────────

func withLibrary(s *oracleSeed) harnessOpt {
	return func(o *abshandler.Options) {
		o.Library = s.lib
		o.Identity = s.lib
		o.Chapters = s.lib
		o.Progress = s.lib
		o.CoverRoot = s.root
	}
}

// mustLoadFixture loads a golden ABS fixture or fails the test.
func mustLoadFixture(t *testing.T, name string) *conformance.Fixture {
	t.Helper()
	f, err := conformance.LoadFixture(filepath.Join(fixturesDir(), name))
	if err != nil {
		t.Fatalf("LoadFixture(%s): %v", name, err)
	}
	return f
}

// assertConformantPending is a SHAPE-ONLY check for a call site that cannot meet the
// oracle's values yet. It is the debt list for the strictness flip of 2026-08-12.
//
// Every use is a promise to come back, so every use must say why in prose — an empty
// reason fails the test rather than silently buying another release of weak checking.
// Delete a call site from this helper the moment it can hold assertConformant; the
// count of these IS the remaining N-2 work, so it must not be able to drift upward
// quietly.
func assertConformantPending(t *testing.T, fixture string, got any, reason string) {
	t.Helper()
	if strings.TrimSpace(reason) == "" {
		t.Fatalf("%s: assertConformantPending requires a reason — a call site that cannot say "+
			"why it is exempt has no business being exempt", fixture)
	}
	f := mustLoadFixture(t, fixture)
	t.Logf("%s: PENDING strict conformance (shape only) — %s", fixture, reason)
	findings := f.CompareBody(got, conformance.Options{IgnoreExtra: true})
	if len(findings) > 0 {
		for _, fi := range findings {
			t.Errorf("%s: %s", fixture, fi)
		}
		t.Fatalf("%s: %d SHAPE findings against the real ABS 2.36.0 response — these are not "+
			"covered by the pending exemption, which is for values only", fixture, len(findings))
	}
}

// assertConformantExcept is assertConformant with an explicit, named allowance list.
//
// It exists for exactly ONE situation and must not grow past it: a field where real
// ABS reports data our pipeline genuinely does not collect, so no amount of mapping
// work would close the gap. Each allowance names the JSON path AND the reason, so an
// allowance can never quietly become a shape bug — a finding at any other path still
// fails, and an allowance that stops firing is dead weight a reader can delete.
//
// NOTE (2026-08-12): still shape-only while assertConformant went strict. Its one call
// site (search) is in the pending-12 and its allowances are calibrated against
// shape-level findings, so flipping CompareValues here without retriaging those
// allowances would just relocate the failure. It goes strict with the rest in N-2.
func assertConformantExcept(t *testing.T, fixture string, got any, allowed map[string]string) {
	t.Helper()
	f := mustLoadFixture(t, fixture)
	findings := f.CompareBody(got, conformance.Options{IgnoreExtra: true})
	var unexpected []conformance.Finding
	for _, fi := range findings {
		if reason, ok := allowed[fi.Path]; ok {
			t.Logf("%s: ALLOWED deviation at %s (%s) — %s", fixture, fi.Path, fi.Kind, reason)
			continue
		}
		unexpected = append(unexpected, fi)
	}
	for _, fi := range unexpected {
		t.Errorf("%s: %s", fixture, fi)
	}
	if len(unexpected) > 0 {
		t.Fatalf("%s: %d unallowed conformance findings against the real ABS 2.36.0 response", fixture, len(unexpected))
	}
}

// doAny is do() for endpoints whose top-level body is not an object — most
// importantly /personalized, which is a BARE ARRAY (§1.8.6: `{}` there throws).
func (h *harness) doAny(t *testing.T, req request) (int, any) {
	t.Helper()
	w, _ := h.do(t, req)
	var decoded any
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("%s %s: body is not JSON (%v): %s", req.method, req.path, err, w.Body.String())
		}
	}
	return w.Code, decoded
}
