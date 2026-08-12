// file: internal/server/handlers/abs/library_fake_test.go
// version: 1.4.0
// guid: 1d4a67f2-0c85-4f39-9b6e-3a71c5d0e824
// last-edited: 2026-08-12

package abs_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
	// Named as the oracle names it: the filename reaches clients through
	// metadata.filename AND, via trackTitle's basename fallback, through tracks[].title.
	m4b := filepath.Join(singleDir, "odyssey_complete.m4b")
	// singleFileSize is what the oracle's m4b actually weighs. mapper.go stats the file
	// and lets the on-disk size override the recorded one, so a 4096-byte stand-in made
	// `size` unconformable everywhere this book appears — including two personalized
	// shelves. Written sparse; see writeFakeAudioSized.
	const singleFileSize = 120828875
	writeFakeAudioSized(t, m4b, singleFileSize)
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
		// The oracle reports 9975.480544; BookFile.Duration is an int, so 9975 is the
		// closest the schema can hold. See todo.d/20260812-bookfile-duration-integer-seconds.
		Duration: 9975, FileSize: singleFileSize,
		BitrateKbps: 96, SampleRateHz: 22050, Channels: 1,
		RawTags: map[string]string{
			"album": "The Odyssey", "artist": "Homer", "date": "800BC",
			"encoder": "Lavf62.3.100", "genre": "Audiobook", "title": "The Odyssey",
		},
		CreatedAt: created, UpdatedAt: created,
	}}, sixChapters(t))

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
	// Every per-track value below comes from the ORACLE rather than from constants
	// invented here. Hardcoding them is what made this book diverge from the fixture
	// the conformance tests compare it against — six identical 1662s durations and
	// 2049-2054 byte files against a real recording — and that drift is why twelve
	// assertions could not check values at all until 2026-08-12.
	//
	// It also stops the seed from inventing facts. The old loop gave every track the
	// same six tags and a "%d/24" track number; the real capture has track 4 tagged a
	// bare "4", track 6 carrying no track tag AND no comment/encoder/genre at all.
	// Reading it made one "mapper defect" evaporate: we do not differ from ABS on
	// tagTrack, the seed was manufacturing the difference.
	var mp3s []database.BookFile
	for i, ot := range oracleAudioFiles(t) {
		p := filepath.Join(multiDir, ot.Filename)
		writeFakeAudioSized(t, p, ot.Size)
		mp3s = append(mp3s, database.BookFile{
			ID: fmt.Sprintf("f-mp3-%d", i+1), BookID: multi.ID, FilePath: p,
			Format: "MP2/3 (MPEG audio layer 2/3)", Codec: "mp3",
			// BookFile.Duration is an INT: whole seconds is all the schema can hold, so
			// the oracle's 1386.057143 necessarily becomes 1386. That truncation is a
			// real production limitation, not a test artifact -- see the bounded
			// duration allowances at the assertion sites.
			Duration: int(math.Round(ot.Duration)),
			FileSize: ot.Size,
			// TrackNumber feeds trackNumFromFilename; trackNumFromMeta is derived from
			// RawTags, so track 6's missing "track" tag yields null there on its own.
			TrackNumber: i + 1, TrackCount: 24, Title: ot.Tags["title"],
			BitrateKbps: ot.BitrateBps / 1000, SampleRateHz: 22050, Channels: ot.Channels,
			RawTags:   ot.Tags,
			CreatedAt: created, UpdatedAt: created,
		})
	}
	multi.FilePath = mp3s[0].FilePath
	lib.addBook(multi, mp3s, nil) // nil chapters => synthesized, one per track

	lib.addAuthor(1, "Homer", single.ID)
	lib.addAuthor(2, "transl. Samuel Butler Homer", multi.ID)

	return &oracleSeed{lib: lib, root: root, singleID: single.ID, multiID: multi.ID}
}

// sixChapters returns the single-file book's chapters as the ORACLE reported them.
//
// They used to be six flat 1662.5s spans, which is nothing like the real recording's
// boundaries — the first chapter ends at 1386.057. Unlike the durations, this one is
// fully fixable: database.Chapter holds StartSec/EndSec as float64, so the oracle's
// values fit exactly and no allowance is needed. It was the BOUNDED duration allowance
// that exposed this: a blanket "chapter bounds may differ" would have absorbed a 276s
// gap as though it were the known sub-second truncation.
func sixChapters(t *testing.T) []database.Chapter {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixturesDir(), "get_api_libraries_id_search.json"))
	if err != nil {
		t.Fatalf("read search oracle: %v", err)
	}
	var doc struct {
		Response struct {
			Body struct {
				Book []struct {
					LibraryItem struct {
						Media struct {
							Chapters []struct {
								ID    int     `json:"id"`
								Start float64 `json:"start"`
								End   float64 `json:"end"`
								Title string  `json:"title"`
							} `json:"chapters"`
						} `json:"media"`
					} `json:"libraryItem"`
				} `json:"book"`
			} `json:"body"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode search oracle: %v", err)
	}
	if len(doc.Response.Body.Book) == 0 {
		t.Fatal("search oracle has no book result; the single-file book would be seeded chapterless")
	}
	chs := doc.Response.Body.Book[0].LibraryItem.Media.Chapters
	if len(chs) != 6 {
		t.Fatalf("search oracle must yield 6 chapters, got %d", len(chs))
	}
	out := make([]database.Chapter, 0, len(chs))
	for _, c := range chs {
		out = append(out, database.Chapter{ID: c.ID, StartSec: c.Start, EndSec: c.End, Title: c.Title})
	}
	return out
}

// oracleTrack is one audioFile exactly as the reference server reported it.
type oracleTrack struct {
	Filename   string
	Duration   float64
	Size       int64
	BitrateBps int
	Channels   int
	// Tags is metaTags translated back to the RawTags key space the importer uses
	// (tagAlbum -> album, ...). A tag the oracle did not carry is ABSENT here, not
	// empty: the difference is load-bearing, because trackNumFromMeta is derived
	// from the presence of "track".
	Tags map[string]string
}

// oracleAudioFiles reads the six tracks out of the item fixture so the fake library
// is seeded from the same source the tests assert against. Anything hardcoded here
// instead is free to drift, and did.
func oracleAudioFiles(t *testing.T) []oracleTrack {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixturesDir(), "get_api_items_id.json"))
	if err != nil {
		t.Fatalf("read item oracle: %v", err)
	}
	var doc struct {
		Response struct {
			Body struct {
				Media struct {
					AudioFiles []struct {
						BitRate  int            `json:"bitRate"`
						Channels int            `json:"channels"`
						Duration float64        `json:"duration"`
						MetaTags map[string]any `json:"metaTags"`
						Metadata struct {
							Filename string `json:"filename"`
							Size     int64  `json:"size"`
						} `json:"metadata"`
					} `json:"audioFiles"`
				} `json:"media"`
			} `json:"body"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode item oracle: %v", err)
	}
	files := doc.Response.Body.Media.AudioFiles
	// Guard against a vacuous seed: an empty or restructured fixture would otherwise
	// produce a book with no tracks, and every multi-file assertion would pass by
	// having nothing to disagree about.
	if len(files) != 6 {
		t.Fatalf("item oracle must yield 6 audioFiles, got %d — the fixture changed shape "+
			"and the fake library would be seeded empty", len(files))
	}
	tagKey := map[string]string{
		"tagAlbum": "album", "tagArtist": "artist", "tagComment": "comment",
		"tagDate": "date", "tagEncoder": "encoder", "tagGenre": "genre",
		"tagTitle": "title", "tagTrack": "track",
	}
	out := make([]oracleTrack, 0, len(files))
	for _, f := range files {
		tags := map[string]string{}
		for k, v := range f.MetaTags {
			s, ok := v.(string)
			if !ok || strings.TrimSpace(s) == "" {
				continue // absent or null: must stay absent, see oracleTrack.Tags
			}
			if key, ok := tagKey[k]; ok {
				tags[key] = s
			}
		}
		out = append(out, oracleTrack{
			Filename: f.Metadata.Filename, Duration: f.Duration, Size: f.Metadata.Size,
			BitrateBps: f.BitRate, Channels: f.Channels, Tags: tags,
		})
	}
	return out
}

// writeFakeAudioSized produces a file whose LENGTH matches the oracle without
// writing the oracle's bytes. mapper.go stats every track and lets the on-disk size
// override the recorded one, so `metadata.size` can only conform if the file really
// is ~11-21 MB — 66 MB per run across six tracks if written out in full.
//
// The first 4 KiB carry the usual pattern and the rest is a hole, which costs no
// disk. Safe for the Range assertions because they are self-referential: play_test.go
// compares a suffix range against the full response's own tail rather than against a
// hardcoded pattern, so zeroes satisfy them exactly as patterned bytes do.
func writeFakeAudioSized(t *testing.T, path string, size int64) {
	t.Helper()
	const patterned = 4096
	n := int64(patterned)
	if size < n {
		n = size
	}
	writeFakeAudio(t, path, int(n))
	if size > n {
		if err := os.Truncate(path, size); err != nil {
			t.Fatalf("extend %s to %d: %v", path, size, err)
		}
	}
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

// assertConformantPending was the debt list for the strictness flip of 2026-08-12: a
// shape-only check that each exempt call site had to justify in prose. It is gone
// because the list is EMPTY — every one of the twelve now holds assertConformant or
// assertConformantExcept with named allowances. A helper kept alive for a debt nobody
// owes is just a softer gate waiting to be reached for.
//
// ── the standing divergences ────────────────────────────────────────────────
//
// These are the same four wherever a book body appears, so they are written once and
// merged per call site rather than restated. Three were owner decisions on 2026-08-12;
// the fourth is a schema limit filed for separate work.

const durationReason = "BookFile.Duration is an INT: the store holds whole seconds, so " +
	"the oracle's 1386.057143 can only come back as 1386. Bounded, not blanket — a " +
	"duration wrong by more than the truncation is a different bug and still fails. " +
	"Filed: todo.d/20260812-bookfile-duration-integer-seconds.md"

const timeBaseReason = "timeBase is hardcoded 1/1000 (mapper.go:645) where ffprobe " +
	"reports 1/14112000. We do not capture stream time_base at import, so there is " +
	"nothing to map from. Owner decision 2026-08-12: allow rather than add an ingest " +
	"field and backfill for a value no client is known to divide by"

const trackTitleReason = "we send the embedded tag title where ABS sends the filename. " +
	"Owner decision 2026-08-12: keep ours deliberately — the tag title is the useful " +
	"string and the filename is also available via metadata.filename"

// bookBodyAllowances covers any response embedding a book's media block.
//
// The duration bounds are the arithmetic, not round numbers: a per-file duration is
// rounded to whole seconds so it cannot be off by more than 0.5, and startOffset and
// chapter bounds accumulate those roundings across six tracks, worst case 3.0.
func bookBodyAllowances() map[string]allowance {
	return map[string]allowance{
		// A single track is rounded to whole seconds, so it cannot be off by more
		// than 0.5. Anything wider is not this bug.
		"*audioFiles[].duration": {Reason: durationReason, Within: 0.5},
		"*tracks[].duration":     {Reason: durationReason, Within: 0.5},
		// Aggregates aggregate the error: six roundings, worst case 3.0. Keeping this
		// separate from the per-track bound is the point — one loose bound covering
		// both would let a per-track duration be wrong by 3s and call it expected.
		"*media.duration":   {Reason: durationReason + " (summed over six tracks)", Within: 3.0},
		"*startOffset":      {Reason: durationReason + " (accumulated across tracks)", Within: 3.0},
		"*chapters[].start": {Reason: durationReason + " (synthesized chapter bound)", Within: 3.0},
		"*chapters[].end":   {Reason: durationReason + " (synthesized chapter bound)", Within: 3.0},
		"*timeBase":         {Reason: timeBaseReason},
		// Scoped to TRACK titles. A bare "*title" would also swallow
		// media.metadata.title — the book's own title, which must stay compared.
		"*tracks[].title": {Reason: trackTitleReason},
	}
}

// identityAllowances covers the user object served by /login, /auth/refresh and /me.
// Source is NOT included: /me does not carry it, and an allowance that cannot fire is
// a test failure by design.
func identityAllowances() map[string]allowance {
	const roleReason = "our role model is not ABS's: the oracle captured its own root " +
		"account, and upload/createEreader describe ABS features we do not implement. " +
		"Reporting true would be a claim we cannot honour"
	return map[string]allowance{
		"*type":                      {Reason: roleReason},
		"*permissions.upload":        {Reason: roleReason},
		"*permissions.createEreader": {Reason: roleReason},
	}
}

// sourceAllowance is separate because only the auth bodies carry Source.
func sourceAllowance() map[string]allowance {
	return map[string]allowance{
		"Source": {Reason: "we identify as audiobook-organizer; the oracle was a docker install"},
	}
}

// progressReason covers a progress fraction that reflects where the CAPTURE happened to
// be paused. currentTime/duration at capture time is not something a test can align to,
// and the normalizer deliberately does not treat progress as volatile because its value
// does matter — so it is named here rather than hidden there.
const progressReason = "progress is currentTime/duration at CAPTURE time; the oracle was " +
	"recorded at a different playback position than this test sets"

// bitrateReason is the duration truncation's twin one field over: BookFile.BitrateKbps
// is an int in KILObits, so the oracle's 96208 bps can only come back as 96 * 1000.
const bitrateReason = "BookFile.BitrateKbps is an int in kbps, so ffprobe's 96208 bps " +
	"round-trips as 96000. Bounded below 1 kbps — a bitrate wrong by more than the " +
	"rounding is a different bug"

// publishedYearReason is the same shape of loss as the duration truncation: a typed
// column cannot hold what the raw tag said. Book.PrintYear is an int, so the oracle's
// "800BC" comes back "800" — ABS passes the raw date tag straight through.
const publishedYearReason = "we render Book.PrintYear (an int) where ABS passes the raw " +
	"date tag through, so the oracle's \"800BC\" loses its era and becomes \"800\""

// mergeAllowances combines allowance sets, and refuses to let one silently replace a
// key from another — a collision would mean two different reasons for one path, and
// the loser's reason would be a lie in the source.
func mergeAllowances(t *testing.T, sets ...map[string]allowance) map[string]allowance {
	t.Helper()
	out := map[string]allowance{}
	for _, s := range sets {
		for k, v := range s {
			if prev, dup := out[k]; dup {
				t.Fatalf("allowance %q declared twice (%q vs %q)", k, prev.Reason, v.Reason)
			}
			out[k] = v
		}
	}
	return out
}

// allowance is a divergence from the oracle that we accept.
//
// Where the divergence is numeric and its cause is known and bounded — integer-second
// duration truncation, say — Within states the widest gap that cause can produce, and
// anything wider still fails. That distinction matters: a blanket allowance at
// media.duration accepts ANY duration there forever, so the day we start reporting
// half the book's length the suite says nothing. Within keeps the field checked while
// admitting the part we cannot fix.
//
// Within == 0 means blanket, which is only honest where the divergence is not numeric.
type allowance struct {
	Reason string
	Within float64
}

// arrayIndex normalizes concrete element indices so one allowance can cover a whole
// list. An allowance almost never applies to element 3 alone.
var arrayIndex = regexp.MustCompile(`\[\d+\]`)

// allowedAt resolves a finding's path against the allowance keys. Keys use [] for "any
// index"; a leading * makes the remainder a suffix match, so a single entry covers
// media.x, libraryItem.media.x and a bare x — the play body carries all three.
// Selection MUST be deterministic. This originally returned the first matching pattern
// found while ranging the map, and Go randomizes map iteration — so if two patterns could
// match one path, WHICH BOUND APPLIED would vary between runs. A run that happened to pick
// a 3.0 bound where 0.5 was intended would accept a real divergence and report green,
// which is a worse failure than the one it hides because it is invisible.
//
// An exact key wins outright. Otherwise an ambiguous path is reported as an ERROR rather
// than resolved by a tie-break: two patterns claiming one field is an authoring mistake,
// and quietly guessing which the author meant is exactly the guarantee this design exists
// to provide. Returning the sorted match list keeps the failure message stable too.
func allowedAt(allowed map[string]allowance, path string) (key string, a allowance, ok bool, ambiguous []string) {
	norm := arrayIndex.ReplaceAllString(path, "[]")
	if exact, found := allowed[norm]; found {
		return norm, exact, true, nil
	}
	var matches []string
	for k := range allowed {
		suffix, isSuffix := strings.CutPrefix(k, "*")
		if !isSuffix {
			continue
		}
		if norm == suffix || strings.HasSuffix(norm, "."+suffix) {
			matches = append(matches, k)
		}
	}
	sort.Strings(matches)
	switch len(matches) {
	case 0:
		return "", allowance{}, false, nil
	case 1:
		return matches[0], allowed[matches[0]], true, nil
	default:
		return "", allowance{}, false, matches
	}
}

// TestAllowedAt_AmbiguousPatternsAreAnErrorNotACoinFlip is the regression guard for the
// defect the matcher shipped with: it ranged the allowance map and returned the first
// match, so with two patterns able to claim one path the applied bound depended on Go's
// randomised map iteration.
//
// The loop matters. A single call could pass a hundred times and fail the hundred-and-
// first, which is precisely the shape of bug that gets closed as "could not reproduce".
func TestAllowedAt_AmbiguousPatternsAreAnErrorNotACoinFlip(t *testing.T) {
	allowed := map[string]allowance{
		"*duration":       {Reason: "loose", Within: 3.0},
		"*media.duration": {Reason: "tight", Within: 0.5},
	}
	for i := 0; i < 200; i++ {
		key, _, ok, ambiguous := allowedAt(allowed, "libraryItem.media.duration")
		if ok {
			t.Fatalf("iteration %d: resolved to %q — two patterns claim this path, so any "+
				"single winner is iteration-order luck", i, key)
		}
		if len(ambiguous) != 2 || ambiguous[0] != "*duration" || ambiguous[1] != "*media.duration" {
			t.Fatalf("iteration %d: ambiguity must be reported sorted and complete, got %v", i, ambiguous)
		}
	}
}

// TestAllowedAt_ExactKeyBeatsPattern pins the one precedence rule there is, so a call site
// can always name a specific path to override a shared pattern.
func TestAllowedAt_ExactKeyBeatsPattern(t *testing.T) {
	allowed := map[string]allowance{
		"*duration":               {Reason: "pattern", Within: 3.0},
		"media.tracks[].duration": {Reason: "exact", Within: 0.5},
	}
	for i := 0; i < 50; i++ {
		key, a, ok, ambiguous := allowedAt(allowed, "media.tracks[3].duration")
		if !ok || ambiguous != nil || key != "media.tracks[].duration" || a.Within != 0.5 {
			t.Fatalf("iteration %d: exact key must win outright, got key=%q within=%v ok=%v ambiguous=%v",
				i, key, a.Within, ok, ambiguous)
		}
	}
}

// TestAllowedAt_SuiteAllowancesAreUnambiguous checks the patterns the suite actually
// declares, so adding a broad key later fails here rather than silently widening a bound
// on whichever run picks it.
func TestAllowedAt_SuiteAllowancesAreUnambiguous(t *testing.T) {
	paths := []string{
		"media.duration", "libraryItem.media.duration", "duration",
		"media.audioFiles[0].duration", "media.tracks[0].duration", "audioTracks[0].duration",
		"media.tracks[0].startOffset", "media.chapters[0].start", "media.chapters[0].end",
		"media.audioFiles[0].timeBase", "media.tracks[0].title", "media.metadata.title",
	}
	sets := map[string]map[string]allowance{
		"bookBody": bookBodyAllowances(),
		"identity": identityAllowances(),
	}
	for name, set := range sets {
		for _, p := range paths {
			if _, _, _, ambiguous := allowedAt(set, p); len(ambiguous) > 0 {
				t.Errorf("%s: %q is claimed by %v — narrow one of them", name, p, ambiguous)
			}
		}
	}
}

// numericGap reports |want-got| when both sides parse as numbers.
func numericGap(fi conformance.Finding) (float64, bool) {
	want, err1 := strconv.ParseFloat(strings.TrimSpace(fi.Want), 64)
	got, err2 := strconv.ParseFloat(strings.TrimSpace(fi.Got), 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return math.Abs(want - got), true
}

// assertConformantExcept is assertConformant with an explicit, named allowance list.
//
// It exists for one situation and must not grow past it: a field where real ABS reports
// something our pipeline genuinely cannot produce, so no amount of mapping work closes
// the gap. Each allowance names the path AND the reason, so an allowance can never
// quietly become a shape bug — a finding at any other path still fails.
//
// Two guards keep the list from rotting, because an allowance that protects nothing
// reads exactly like one that protects something:
//
//   - An allowance that never fires FAILS the test. The old doc comment asked readers
//     to notice and delete dead weight themselves; nobody was going to. A stale entry
//     is either a divergence we already fixed or a path we typo'd, and the second one
//     is a hole shaped like a safeguard.
//   - A bounded allowance that fires OUTSIDE its bound fails too, reported as its own
//     kind of finding rather than silently absorbed.
func assertConformantExcept(t *testing.T, fixture string, got any, allowed map[string]allowance) {
	t.Helper()
	f := mustLoadFixture(t, fixture)
	findings := f.CompareBody(got, conformance.Options{IgnoreExtra: true, CompareValues: true})
	fired := make(map[string]bool, len(allowed))
	var unexpected []string
	for _, fi := range findings {
		key, a, ok, ambiguous := allowedAt(allowed, fi.Path)
		if len(ambiguous) > 0 {
			unexpected = append(unexpected, fmt.Sprintf(
				"%s — %d allowances match this path (%s). Which bound applies would depend "+
					"on map iteration order, so this is an authoring error, not a divergence: "+
					"narrow the patterns until exactly one claims the field",
				fi, len(ambiguous), strings.Join(ambiguous, ", ")))
			continue
		}
		if !ok {
			unexpected = append(unexpected, fi.String())
			continue
		}
		if a.Within > 0 {
			gap, numeric := numericGap(fi)
			if !numeric {
				unexpected = append(unexpected, fmt.Sprintf(
					"%s — allowance %q is bounded (within %v) but this finding is not numeric",
					fi, key, a.Within))
				continue
			}
			if gap > a.Within {
				unexpected = append(unexpected, fmt.Sprintf(
					"%s — gap %v EXCEEDS the %v allowed by %q (%s). A known divergence got "+
						"bigger, which makes it a different divergence",
					fi, gap, a.Within, key, a.Reason))
				continue
			}
		}
		fired[key] = true
	}
	for _, msg := range unexpected {
		t.Errorf("%s: %s", fixture, msg)
	}
	for key, a := range allowed {
		if !fired[key] {
			t.Errorf("%s: allowance %q never fired — it is either a divergence that has "+
				"been fixed (delete it) or a path that does not exist (a hole). Reason given: %s",
				fixture, key, a.Reason)
		}
	}
	if t.Failed() {
		t.Fatalf("%s: %d unallowed findings; see above", fixture, len(unexpected))
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
