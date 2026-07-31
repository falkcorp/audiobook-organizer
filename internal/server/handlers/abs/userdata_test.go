// file: internal/server/handlers/abs/userdata_test.go
// version: 1.0.0
// guid: 7ac71a7b-e1cb-4416-a393-1fa38af8871f
// last-edited: 2026-07-30

package abs_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
)

// ── fake stores ─────────────────────────────────────────────────────────────
//
// Deliberately named with a `ud` prefix rather than reusing fakeLibrary: this
// provider needs the two ENUMERATION primitives (ListUserPositionsSince,
// ListBookmarksForUser) that the browse fake has no reason to hold, and a
// uniquely-named helper cannot collide with a sibling worktree adding another
// helper to this same package.
type udFake struct {
	mu sync.Mutex

	positions []database.UserPosition
	states    map[string]*database.UserBookState
	bookmarks []progress.Bookmark
	books     map[string]*database.Book
	files     map[string][]database.BookFile
	syncIDs   map[string]string

	positionsErr error
	bookmarksErr error
	stateErr     map[string]error
	filesErr     map[string]error
	syncErr      map[string]error

	mints int
}

func newUDFake() *udFake {
	return &udFake{
		states:   map[string]*database.UserBookState{},
		books:    map[string]*database.Book{},
		files:    map[string][]database.BookFile{},
		syncIDs:  map[string]string{},
		stateErr: map[string]error{},
		filesErr: map[string]error{},
		syncErr:  map[string]error{},
	}
}

// udSyncID mints a 36-char canonical-UUID-shaped id, the same shape the real
// sync_item keyspace mints. A fake that handed back a 26-char Book ULID would
// let the §1.7.1 fixed-offset regression through unnoticed.
func udSyncID(n int) string {
	return fmt.Sprintf("%08d-0000-4000-8000-%012d", n, n)
}

// addBook seeds a book with one BookFile per supplied track duration (seconds).
// With no tracks the book carries only its own Book.Duration fallback.
func (f *udFake) addBook(bookID string, bookDuration *int, trackDurations ...int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.books[bookID] = &database.Book{ID: bookID, Title: "book " + bookID, Duration: bookDuration}
	for i, d := range trackDurations {
		f.files[bookID] = append(f.files[bookID], database.BookFile{
			ID: fmt.Sprintf("%s-f%d", bookID, i), BookID: bookID,
			FilePath: fmt.Sprintf("/lib/%s/%02d.m4b", bookID, i),
			Duration: d, TrackNumber: i + 1,
		})
	}
}

func (f *udFake) presetSyncID(bookID, syncID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.syncIDs[bookID] = syncID
}

func (f *udFake) addPosition(userID, bookID, segment string, seconds float64, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.positions = append(f.positions, database.UserPosition{
		UserID: userID, BookID: bookID, SegmentID: segment,
		PositionSeconds: seconds, UpdatedAt: at,
	})
}

func (f *udFake) setState(s *database.UserBookState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[s.UserID+"|"+s.BookID] = s
}

func (f *udFake) addBookmark(b progress.Bookmark) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bookmarks = append(f.bookmarks, b)
}

// ── ProgressListStore ───────────────────────────────────────────────────────

func (f *udFake) ListUserPositionsSince(userID string, since time.Time) ([]database.UserPosition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.positionsErr != nil {
		return nil, f.positionsErr
	}
	var out []database.UserPosition
	for _, p := range f.positions {
		// Mirrors the real store's filter exactly, so a provider that passed a
		// non-zero "since" would visibly drop rows here.
		if p.UserID == userID && p.UpdatedAt.After(since) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *udFake) GetUserBookState(userID, bookID string) (*database.UserBookState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.stateErr[bookID]; err != nil {
		return nil, err
	}
	return f.states[userID+"|"+bookID], nil
}

// ── BookmarkListStore ───────────────────────────────────────────────────────

func (f *udFake) ListBookmarksForUser(userID string) ([]progress.Bookmark, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bookmarksErr != nil {
		return nil, f.bookmarksErr
	}
	var out []progress.Bookmark
	for _, b := range f.bookmarks {
		if b.UserID == userID {
			out = append(out, b)
		}
	}
	return out, nil
}

// ── SyncIDStore ─────────────────────────────────────────────────────────────

func (f *udFake) GetSyncIDForBook(bookID string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.syncErr[bookID]; err != nil {
		return "", false, err
	}
	id, ok := f.syncIDs[bookID]
	return id, ok, nil
}

func (f *udFake) MintOrGetSyncID(bookID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.syncErr[bookID]; err != nil {
		return "", err
	}
	if id, ok := f.syncIDs[bookID]; ok {
		return id, nil
	}
	f.mints++
	id := udSyncID(f.mints)
	f.syncIDs[bookID] = id
	return id, nil
}

// ── UserDataLibraryStore ────────────────────────────────────────────────────

func (f *udFake) GetBookByID(id string) (*database.Book, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.books[id], nil
}

func (f *udFake) GetBookFiles(bookID string) ([]database.BookFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.filesErr[bookID]; err != nil {
		return nil, err
	}
	return f.files[bookID], nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

func udProvider(t *testing.T, f *udFake) abshandler.UserDataProvider {
	t.Helper()
	p, err := abshandler.NewUserData(abshandler.UserDataOptions{
		Progress:  f,
		Bookmarks: f,
		Identity:  f,
		Library:   f,
	})
	if err != nil {
		t.Fatalf("NewUserData: %v", err)
	}
	return p
}

// udRows re-encodes the provider's rows through JSON, because the JSON wire shape
// IS the contract: a Swift/Dart decoder never sees the Go struct. Numbers come
// back as json.Number so a test can assert "integer ms epoch" rather than merely
// "some number".
func udRows(t *testing.T, rows []any) []map[string]json.Number {
	t.Helper()
	out := make([]map[string]json.Number, 0, len(rows))
	for _, row := range rows {
		raw, err := json.Marshal(row)
		if err != nil {
			t.Fatalf("marshal row: %v", err)
		}
		var generic map[string]any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&generic); err != nil {
			t.Fatalf("decode row: %v", err)
		}
		typed := map[string]json.Number{}
		for k, v := range generic {
			if n, ok := v.(json.Number); ok {
				typed[k] = n
			}
		}
		// Keep the non-numeric fields reachable too by stashing them as strings.
		for k, v := range generic {
			if _, ok := v.(json.Number); ok {
				continue
			}
			switch tv := v.(type) {
			case string:
				typed["str:"+k] = json.Number(tv)
			case bool:
				typed["bool:"+k] = json.Number(fmt.Sprintf("%t", tv))
			case nil:
				typed["null:"+k] = "null"
			}
		}
		out = append(out, typed)
	}
	return out
}

func udFloat(t *testing.T, row map[string]json.Number, key string) float64 {
	t.Helper()
	n, ok := row[key]
	if !ok {
		t.Fatalf("field %q missing or not a JSON number: %v", key, row)
	}
	v, err := n.Float64()
	if err != nil {
		t.Fatalf("field %q is not numeric (%q): %v", key, n, err)
	}
	return v
}

func udInt(t *testing.T, row map[string]json.Number, key string) int64 {
	t.Helper()
	n, ok := row[key]
	if !ok {
		t.Fatalf("field %q missing or not a JSON number: %v", key, row)
	}
	// An integer ms epoch must be encoded as an integer literal: AudioBooth decodes
	// dates as Int64 and Dart throws on `42.0 as int?` (§1.7.3 item 5).
	if strings.ContainsAny(string(n), ".eE") {
		t.Fatalf("field %q must be an INTEGER ms epoch, got the literal %q", key, n)
	}
	v, err := n.Int64()
	if err != nil {
		t.Fatalf("field %q is not an integer (%q): %v", key, n, err)
	}
	return v
}

func udStr(t *testing.T, row map[string]json.Number, key string) string {
	t.Helper()
	v, ok := row["str:"+key]
	if !ok {
		t.Fatalf("field %q missing or not a JSON string: %v", key, row)
	}
	return string(v)
}

func udBool(t *testing.T, row map[string]json.Number, key string) bool {
	t.Helper()
	v, ok := row["bool:"+key]
	if !ok {
		t.Fatalf("field %q missing or not a JSON boolean: %v", key, row)
	}
	return v == "true"
}

func udRowByItem(t *testing.T, rows []map[string]json.Number, libraryItemID string) map[string]json.Number {
	t.Helper()
	for _, r := range rows {
		if udStr(t, r, "libraryItemId") == libraryItemID {
			return r
		}
	}
	t.Fatalf("no row for libraryItemId %q in %v", libraryItemID, rows)
	return nil
}

// ── constructor ─────────────────────────────────────────────────────────────

// TestUserDataProviderRefusesMissingDependencies: every dependency is required.
// A provider built without one would answer an EMPTY list where records exist,
// and AudioBooth deletes every local progress row absent from that list (§1.8.1).
// Refusing to build is the only safe outcome; the caller exits on the error.
func TestUserDataProviderRefusesMissingDependencies(t *testing.T) {
	f := newUDFake()
	full := abshandler.UserDataOptions{Progress: f, Bookmarks: f, Identity: f, Library: f}

	cases := map[string]func(o *abshandler.UserDataOptions){
		"progress":  func(o *abshandler.UserDataOptions) { o.Progress = nil },
		"bookmarks": func(o *abshandler.UserDataOptions) { o.Bookmarks = nil },
		"identity":  func(o *abshandler.UserDataOptions) { o.Identity = nil },
		"library":   func(o *abshandler.UserDataOptions) { o.Library = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			o := full
			mutate(&o)
			if _, err := abshandler.NewUserData(o); err == nil {
				t.Fatalf("NewUserData succeeded with a nil %s — it would serve an empty list and destroy progress", name)
			}
		})
	}
	if _, err := abshandler.NewUserData(full); err != nil {
		t.Fatalf("NewUserData with every dependency: %v", err)
	}
}

// ── mediaProgress: completeness ─────────────────────────────────────────────

// TestUserDataMediaProgressIsComplete is the §1.8.1 data-loss guard at the
// provider level: every book the user has a position for must appear, with no
// pagination and no cap.
func TestUserDataMediaProgressIsComplete(t *testing.T) {
	f := newUDFake()
	const n = 120
	base := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		bookID := fmt.Sprintf("bk%03d", i)
		f.addBook(bookID, nil, 1800, 1800)
		f.presetSyncID(bookID, udSyncID(1000+i))
		f.addPosition("u1", bookID, "abs", float64(60+i), base.Add(time.Duration(i)*time.Minute))
	}
	// A second user's rows must never leak into u1's list.
	f.addBook("other", nil, 100)
	f.addPosition("u2", "other", "abs", 50, base)

	rows, err := udProvider(t, f).MediaProgress("u1")
	if err != nil {
		t.Fatalf("MediaProgress: %v", err)
	}
	if len(rows) != n {
		t.Fatalf("got %d rows want %d — a truncated list DELETES the user's positions", len(rows), n)
	}
	decoded := udRows(t, rows)
	seen := map[string]bool{}
	for i := range decoded {
		seen[udStr(t, decoded[i], "libraryItemId")] = true
	}
	for i := 0; i < n; i++ {
		if !seen[udSyncID(1000+i)] {
			t.Fatalf("book bk%03d is missing from the list", i)
		}
	}
	if seen[udSyncID(9999)] {
		t.Fatal("another user's row leaked into the list")
	}
}

// TestUserDataMediaProgressEmptyIsEmptyNotNil: a user with no records gets a
// non-nil empty slice, which /api/me renders as `[]` rather than `null`.
func TestUserDataMediaProgressEmptyIsEmptyNotNil(t *testing.T) {
	rows, err := udProvider(t, newUDFake()).MediaProgress("u1")
	if err != nil {
		t.Fatalf("MediaProgress: %v", err)
	}
	if rows == nil {
		t.Fatal("rows must be a non-nil empty slice")
	}
	if len(rows) != 0 {
		t.Fatalf("got %d rows want 0", len(rows))
	}
}

// ── mediaProgress: partial failure MUST be an error ─────────────────────────

// TestUserDataMediaProgressPartialFailureIsAnError is the single most important
// test in this file. When one book's data cannot be read, the provider must
// return an ERROR (so /api/me answers 5xx) and NOT the rows it did manage to
// build. A truncated 200 makes the client delete the missing books' progress.
func TestUserDataMediaProgressPartialFailureIsAnError(t *testing.T) {
	base := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

	seed := func() *udFake {
		f := newUDFake()
		for i, id := range []string{"good1", "bad", "good2"} {
			f.addBook(id, nil, 3600)
			f.presetSyncID(id, udSyncID(10+i))
			f.addPosition("u1", id, "abs", float64(100+i), base)
		}
		return f
	}

	cases := map[string]func(f *udFake){
		"book files unreadable": func(f *udFake) { f.filesErr["bad"] = errors.New("pebble: files down") },
		"sync identity failure": func(f *udFake) { f.syncErr["bad"] = errors.New("pebble: sync_item down") },
		"book state unreadable": func(f *udFake) { f.stateErr["bad"] = errors.New("pebble: ubs down") },
		"position scan failure": func(f *udFake) { f.positionsErr = errors.New("pebble: iterator failed") },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			f := seed()
			breakIt(f)
			rows, err := udProvider(t, f).MediaProgress("u1")
			if err == nil {
				t.Fatalf("MediaProgress returned %d rows and no error — a partial list destroys progress", len(rows))
			}
			if rows != nil {
				t.Fatalf("MediaProgress returned a %d-row list alongside its error; callers must get nothing to serve", len(rows))
			}
		})
	}
}

// ── mediaProgress: field contract ───────────────────────────────────────────

// TestUserDataMediaProgressFieldContract checks every client-verified field of a
// single row: the 36-char sync id, ms-epoch lastUpdate read from
// UserBookState.LastActivityAt, seconds currentTime, an always-present duration,
// and a 0.0-1.0 progress FRACTION derived from currentTime/duration rather than
// the store's lossy integer ProgressPct.
func TestUserDataMediaProgressFieldContract(t *testing.T) {
	f := newUDFake()
	// 3 tracks summing to 9975.48s — the oracle fixture's duration.
	f.addBook("bk1", nil, 3325, 3325, 3325)
	f.presetSyncID("bk1", udSyncID(7))
	posAt := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	activity := time.Date(2026, 7, 30, 12, 5, 30, 123_000_000, time.UTC)
	f.addPosition("u1", "bk1", "abs", 1234.5, posAt)
	f.setState(&database.UserBookState{
		UserID: "u1", BookID: "bk1", Status: database.UserBookStatusInProgress,
		LastActivityAt: activity,
		// 12% is what the store cached; the DTO must NOT be derived from it.
		ProgressPct: 12, TotalListenedSeconds: 1234.5,
	})

	rows, err := udProvider(t, f).MediaProgress("u1")
	if err != nil {
		t.Fatalf("MediaProgress: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows want 1", len(rows))
	}
	row := udRows(t, rows)[0]

	if got := udStr(t, row, "libraryItemId"); got != udSyncID(7) {
		t.Fatalf("libraryItemId = %q, want the sync UUID %q", got, udSyncID(7))
	}
	if got := udStr(t, row, "libraryItemId"); len(got) != 36 {
		t.Fatalf("libraryItemId is %d chars (%q) — Absorb splits ids at byte offset 36", len(got), got)
	}
	if got := udStr(t, row, "mediaItemId"); len(got) != 36 {
		t.Fatalf("mediaItemId is %d chars — same fixed-offset contract", len(got))
	}
	if got := udStr(t, row, "mediaItemType"); got != "book" {
		t.Fatalf("mediaItemType = %q want \"book\"", got)
	}
	if got := udStr(t, row, "userId"); got != "u1" {
		t.Fatalf("userId = %q want u1", got)
	}
	if got := udStr(t, row, "id"); got == "" {
		t.Fatal("id must be stable and non-empty")
	}

	// lastUpdate: integer ms epoch, sourced from UserBookState.LastActivityAt.
	if got, want := udInt(t, row, "lastUpdate"), activity.UnixMilli(); got != want {
		t.Fatalf("lastUpdate = %d, want %d (UserBookState.LastActivityAt in ms)", got, want)
	}
	if got := udInt(t, row, "startedAt"); got <= 0 {
		t.Fatalf("startedAt = %d, want a positive ms epoch", got)
	}

	// currentTime: float SECONDS, never ms.
	if got := udFloat(t, row, "currentTime"); got != 1234.5 {
		t.Fatalf("currentTime = %v want 1234.5 seconds", got)
	}
	// duration: always emitted, sum of the track durations.
	if got := udFloat(t, row, "duration"); got != 9975 {
		t.Fatalf("duration = %v want 9975 (sum of tracks)", got)
	}
	// progress: 0.0-1.0 fraction from currentTime/duration, NOT ProgressPct/100.
	wantFraction := 1234.5 / 9975.0
	if got := udFloat(t, row, "progress"); got != wantFraction {
		t.Fatalf("progress = %v want %v (currentTime/duration, not the lossy ProgressPct)", got, wantFraction)
	}
	if got := udFloat(t, row, "progress"); got > 1 || got < 0 {
		t.Fatalf("progress = %v must be a 0.0-1.0 fraction", got)
	}
	if udBool(t, row, "isFinished") {
		t.Fatal("isFinished must be false at 12% through the book")
	}
	if udBool(t, row, "hideFromContinueListening") {
		t.Fatal("hideFromContinueListening must be false")
	}
	if _, isNull := row["null:finishedAt"]; !isNull {
		t.Fatal("finishedAt must be null for an unfinished book")
	}
	if _, isNull := row["null:episodeId"]; !isNull {
		t.Fatal("episodeId must be null for a book")
	}
	if _, isNull := row["null:ebookLocation"]; !isNull {
		t.Fatal("ebookLocation must be null")
	}
}

// TestUserDataMediaProgressLastUpdateFallsBackWhenStateMissing: a position with
// no UserBookState row still needs a usable ms epoch, or the server loses every
// future conflict for that book.
func TestUserDataMediaProgressLastUpdateFallsBackWhenStateMissing(t *testing.T) {
	f := newUDFake()
	f.addBook("bk1", nil, 3600)
	f.presetSyncID("bk1", udSyncID(3))
	posAt := time.Date(2026, 7, 30, 9, 30, 0, 0, time.UTC)
	f.addPosition("u1", "bk1", "abs", 60, posAt)

	rows, err := udProvider(t, f).MediaProgress("u1")
	if err != nil {
		t.Fatalf("MediaProgress: %v", err)
	}
	row := udRows(t, rows)[0]
	if got, want := udInt(t, row, "lastUpdate"), posAt.UnixMilli(); got != want {
		t.Fatalf("lastUpdate = %d, want the position's own timestamp %d as the fallback", got, want)
	}
}

// ── mediaProgress: finished detection ───────────────────────────────────────

// TestUserDataMediaProgressFinishedTolerance: §5b's >=2s tolerance, not a tight
// compare. Three legitimate durations of the same book disagree by ~52ms, so a
// tight epsilon leaves a fully-listened book stuck at 99% forever.
func TestUserDataMediaProgressFinishedTolerance(t *testing.T) {
	cases := []struct {
		name        string
		position    float64
		wantFinishd bool
	}{
		{"52ms short of the end is finished", 3599.948, true},
		{"1.9s short of the end is finished", 3598.1, true},
		{"2.5s short of the end is not finished", 3597.5, false},
		{"exactly at the end is finished", 3600, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUDFake()
			f.addBook("bk1", nil, 3600)
			f.presetSyncID("bk1", udSyncID(4))
			f.addPosition("u1", "bk1", "abs", tc.position, time.Now())

			rows, err := udProvider(t, f).MediaProgress("u1")
			if err != nil {
				t.Fatalf("MediaProgress: %v", err)
			}
			row := udRows(t, rows)[0]
			if got := udBool(t, row, "isFinished"); got != tc.wantFinishd {
				t.Fatalf("isFinished = %v want %v at position %v of 3600", got, tc.wantFinishd, tc.position)
			}
			if got := udFloat(t, row, "duration"); got <= 0 {
				t.Fatalf("duration = %v — isFinished with a zero duration sets the client's currentTime to 0", got)
			}
			if tc.wantFinishd {
				if got := udInt(t, row, "finishedAt"); got <= 0 {
					t.Fatalf("finishedAt = %d, want a positive ms epoch on a finished book", got)
				}
				if got := udFloat(t, row, "progress"); got != 1 {
					t.Fatalf("progress = %v, a finished book must clamp to exactly 1.0", got)
				}
			}
		})
	}
}

// TestUserDataMediaProgressFinishedStateWins: a manually-finished book reports
// isFinished even when the stored position is nowhere near the end.
func TestUserDataMediaProgressFinishedStateWins(t *testing.T) {
	f := newUDFake()
	f.addBook("bk1", nil, 3600)
	f.presetSyncID("bk1", udSyncID(5))
	f.addPosition("u1", "bk1", "abs", 10, time.Now())
	f.setState(&database.UserBookState{
		UserID: "u1", BookID: "bk1", Status: database.UserBookStatusFinished,
		LastActivityAt: time.Now(),
	})

	rows, err := udProvider(t, f).MediaProgress("u1")
	if err != nil {
		t.Fatalf("MediaProgress: %v", err)
	}
	row := udRows(t, rows)[0]
	if !udBool(t, row, "isFinished") {
		t.Fatal("a book whose state says finished must report isFinished")
	}
	if got := udFloat(t, row, "duration"); got <= 0 {
		t.Fatalf("duration = %v must stay positive alongside isFinished", got)
	}
}

// TestUserDataMediaProgressFinishedIsClampedWithoutDuration is §1.8.7's
// null-duration trap: `isFinished:true` with a zero duration sets the client's
// currentTime to 0 — it DESTROYS the position it was meant to report. When the
// duration is unknown, isFinished must be cleared rather than the row dropped.
func TestUserDataMediaProgressFinishedIsClampedWithoutDuration(t *testing.T) {
	f := newUDFake()
	f.addBook("bk1", nil) // no files, no Book.Duration -> duration unknown
	f.presetSyncID("bk1", udSyncID(6))
	f.addPosition("u1", "bk1", "abs", 500, time.Now())
	f.setState(&database.UserBookState{
		UserID: "u1", BookID: "bk1", Status: database.UserBookStatusFinished,
		LastActivityAt: time.Now(),
	})

	rows, err := udProvider(t, f).MediaProgress("u1")
	if err != nil {
		t.Fatalf("MediaProgress: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows want 1 — the row must survive, only isFinished is unsafe", len(rows))
	}
	row := udRows(t, rows)[0]
	if udBool(t, row, "isFinished") {
		t.Fatal("isFinished:true with a zero duration zeroes the client's currentTime — it must be cleared")
	}
	if got := udFloat(t, row, "currentTime"); got != 500 {
		t.Fatalf("currentTime = %v want 500 — the position must survive intact", got)
	}
}

// ── mediaProgress: duration source ──────────────────────────────────────────

// TestUserDataMediaProgressDurationFallsBackToBookDuration mirrors the mapper's
// rule (§5b: ONE authoritative duration per book): sum of the track durations,
// with Book.Duration used only for a book with no BookFile rows at all.
func TestUserDataMediaProgressDurationFallsBackToBookDuration(t *testing.T) {
	fileless := 4242
	f := newUDFake()
	f.addBook("bk1", &fileless)
	f.presetSyncID("bk1", udSyncID(8))
	f.addPosition("u1", "bk1", "abs", 100, time.Now())

	rows, err := udProvider(t, f).MediaProgress("u1")
	if err != nil {
		t.Fatalf("MediaProgress: %v", err)
	}
	row := udRows(t, rows)[0]
	if got := udFloat(t, row, "duration"); got != 4242 {
		t.Fatalf("duration = %v want 4242 (Book.Duration fallback for a fileless book)", got)
	}
}

// ── mediaProgress: identity ─────────────────────────────────────────────────

// TestUserDataMediaProgressMintsMissingSyncID: a book with progress but no
// sync_item yet must still appear. Dropping it because it has no client-visible
// id would make the client delete that book's progress.
func TestUserDataMediaProgressMintsMissingSyncID(t *testing.T) {
	f := newUDFake()
	f.addBook("bk1", nil, 3600)
	f.addPosition("u1", "bk1", "abs", 100, time.Now())
	// deliberately NO presetSyncID

	rows, err := udProvider(t, f).MediaProgress("u1")
	if err != nil {
		t.Fatalf("MediaProgress: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows want 1 — a book with no sync id must still be reported", len(rows))
	}
	row := udRows(t, rows)[0]
	if got := udStr(t, row, "libraryItemId"); len(got) != 36 {
		t.Fatalf("libraryItemId is %d chars (%q), want a freshly minted 36-char UUID", len(got), got)
	}
}

// TestUserDataMediaProgressSurvivesMissingBook: progress for a book that no
// longer resolves (a merge loser, a deleted row) is still reported. Silence
// there is indistinguishable from "delete it" to the client.
func TestUserDataMediaProgressSurvivesMissingBook(t *testing.T) {
	f := newUDFake()
	f.presetSyncID("ghost", udSyncID(9))
	f.addPosition("u1", "ghost", "abs", 321, time.Now())

	rows, err := udProvider(t, f).MediaProgress("u1")
	if err != nil {
		t.Fatalf("MediaProgress: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows want 1", len(rows))
	}
	row := udRows(t, rows)[0]
	if got := udFloat(t, row, "currentTime"); got != 321 {
		t.Fatalf("currentTime = %v want 321", got)
	}
	if udBool(t, row, "isFinished") {
		t.Fatal("an unknown-duration book must not claim isFinished")
	}
}

// ── mediaProgress: one row per book ─────────────────────────────────────────

// TestUserDataMediaProgressCollapsesSegmentsToOneRowPerBook: the UserPosition
// keyspace is per (user, book, SEGMENT) because the app's own reader tracks a
// position per segment. ABS has exactly one whole-book position per item, so the
// segments must collapse to a single row — and the winner must never be BEHIND
// the newest position, or the client is rewound.
func TestUserDataMediaProgressCollapsesSegmentsToOneRowPerBook(t *testing.T) {
	f := newUDFake()
	f.addBook("bk1", nil, 7200)
	f.presetSyncID("bk1", udSyncID(11))
	base := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	f.addPosition("u1", "bk1", "chapter-1", 100, base)
	f.addPosition("u1", "bk1", "abs", 4000, base.Add(2*time.Hour))
	f.addPosition("u1", "bk1", "chapter-2", 250, base.Add(time.Hour))

	rows, err := udProvider(t, f).MediaProgress("u1")
	if err != nil {
		t.Fatalf("MediaProgress: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows want exactly 1 per book", len(rows))
	}
	row := udRows(t, rows)[0]
	if got := udFloat(t, row, "currentTime"); got != 4000 {
		t.Fatalf("currentTime = %v want 4000 (the newest segment position)", got)
	}
}

// TestUserDataMediaProgressTieBreaksForward: two segments written inside the same
// instant must resolve to the FURTHEST position. Picking the smaller one would
// rewind the listener, which is the exact failure this whole surface exists to
// prevent.
func TestUserDataMediaProgressTieBreaksForward(t *testing.T) {
	at := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	for _, order := range [][]float64{{900, 100}, {100, 900}} {
		f := newUDFake()
		f.addBook("bk1", nil, 7200)
		f.presetSyncID("bk1", udSyncID(12))
		f.addPosition("u1", "bk1", "s1", order[0], at)
		f.addPosition("u1", "bk1", "s2", order[1], at)

		rows, err := udProvider(t, f).MediaProgress("u1")
		if err != nil {
			t.Fatalf("MediaProgress: %v", err)
		}
		row := udRows(t, rows)[0]
		if got := udFloat(t, row, "currentTime"); got != 900 {
			t.Fatalf("currentTime = %v want 900 — a timestamp tie must never rewind the listener", got)
		}
	}
}

// TestUserDataMediaProgressIsDeterministic: the same state must serialize in the
// same order every time, so a client (or a conformance diff) never sees the list
// reshuffle between refreshes.
func TestUserDataMediaProgressIsDeterministic(t *testing.T) {
	f := newUDFake()
	for i := 0; i < 25; i++ {
		id := fmt.Sprintf("bk%02d", i)
		f.addBook(id, nil, 600)
		f.presetSyncID(id, udSyncID(200+i))
		f.addPosition("u1", id, "abs", float64(i), time.Now())
	}
	p := udProvider(t, f)
	first, err := p.MediaProgress("u1")
	if err != nil {
		t.Fatalf("MediaProgress: %v", err)
	}
	firstJSON, _ := json.Marshal(first)
	for i := 0; i < 5; i++ {
		again, err := p.MediaProgress("u1")
		if err != nil {
			t.Fatalf("MediaProgress: %v", err)
		}
		againJSON, _ := json.Marshal(again)
		if !bytes.Equal(firstJSON, againJSON) {
			t.Fatal("mediaProgress ordering is not deterministic across calls")
		}
	}
}

// ── bookmarks ───────────────────────────────────────────────────────────────

// TestUserDataBookmarksShape checks the ABS bookmark contract captured in
// testdata/abs-fixtures/get_api_me.json: {createdAt, libraryItemId, time, title}
// with createdAt an integer ms epoch and time a JSON number in SECONDS.
func TestUserDataBookmarksShape(t *testing.T) {
	f := newUDFake()
	f.addBookmark(progress.Bookmark{
		UserID: "u1", ItemID: udSyncID(21), TimeSec: 100, Title: "conformance bookmark",
		CreatedAt: 1785370279374, UpdatedAt: 1785370279374,
	})
	f.addBookmark(progress.Bookmark{
		UserID: "u1", ItemID: udSyncID(21), TimeSec: 4212.5, Title: "half way",
		CreatedAt: 1785370299374, UpdatedAt: 1785370299374,
	})
	f.addBookmark(progress.Bookmark{
		UserID: "u2", ItemID: udSyncID(22), TimeSec: 7, Title: "someone else",
		CreatedAt: 1785370299374,
	})

	rows, err := udProvider(t, f).Bookmarks("u1")
	if err != nil {
		t.Fatalf("Bookmarks: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d bookmarks want 2 (and none of another user's)", len(rows))
	}
	decoded := udRows(t, rows)
	for _, row := range decoded {
		if got := udStr(t, row, "libraryItemId"); len(got) != 36 {
			t.Fatalf("bookmark libraryItemId is %d chars (%q), want the 36-char sync id", len(got), got)
		}
		if got := udStr(t, row, "title"); got == "" {
			t.Fatal("bookmark title must be present")
		}
		if got := udInt(t, row, "createdAt"); got <= 0 {
			t.Fatalf("createdAt = %d, want an integer ms epoch", got)
		}
	}
	// An int-valued time stays an integer literal; a fractional one survives as a
	// float. Both are JSON numbers, never strings.
	if got := udFloat(t, decoded[0], "time"); got != 100 {
		t.Fatalf("time = %v want 100 seconds", got)
	}
	if got := udFloat(t, decoded[1], "time"); got != 4212.5 {
		t.Fatalf("time = %v want 4212.5 seconds", got)
	}
}

// TestUserDataBookmarksEmptyIsEmptyNotNil keeps `bookmarks` a JSON array.
func TestUserDataBookmarksEmptyIsEmptyNotNil(t *testing.T) {
	rows, err := udProvider(t, newUDFake()).Bookmarks("u1")
	if err != nil {
		t.Fatalf("Bookmarks: %v", err)
	}
	if rows == nil || len(rows) != 0 {
		t.Fatalf("got %v, want a non-nil empty slice", rows)
	}
}

// TestUserDataBookmarksErrorIsAnError: same rule as mediaProgress — never a
// silently short list.
func TestUserDataBookmarksErrorIsAnError(t *testing.T) {
	f := newUDFake()
	f.addBookmark(progress.Bookmark{UserID: "u1", ItemID: udSyncID(23), TimeSec: 1, Title: "x", CreatedAt: 1})
	f.bookmarksErr = errors.New("pebble: bookmark scan failed")

	rows, err := udProvider(t, f).Bookmarks("u1")
	if err == nil {
		t.Fatal("Bookmarks must return an error when it cannot read the complete list")
	}
	if rows != nil {
		t.Fatalf("Bookmarks returned %d rows alongside its error", len(rows))
	}
}

// TestUserDataRejectsEmptyUserID: an empty user id would prefix-scan the whole
// keyspace, so it is rejected rather than answered.
func TestUserDataRejectsEmptyUserID(t *testing.T) {
	p := udProvider(t, newUDFake())
	if _, err := p.MediaProgress(""); err == nil {
		t.Fatal("MediaProgress(\"\") must error")
	}
	if _, err := p.Bookmarks(""); err == nil {
		t.Fatal("Bookmarks(\"\") must error")
	}
}

// ── end to end through /api/me ──────────────────────────────────────────────

// TestUserDataProviderServesAPIMe wires the REAL provider into the handler and
// checks the rendered /api/me body, which is what the client actually decodes.
func TestUserDataProviderServesAPIMe(t *testing.T) {
	f := newUDFake()
	f.addBook("bk1", nil, 3600, 3600)
	f.presetSyncID("bk1", udSyncID(31))
	f.addPosition("u1", "bk1", "abs", 1800, time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC))
	f.setState(&database.UserBookState{
		UserID: "u1", BookID: "bk1", Status: database.UserBookStatusInProgress,
		LastActivityAt: time.Date(2026, 7, 30, 10, 0, 5, 0, time.UTC),
	})
	f.addBookmark(progress.Bookmark{
		UserID: "u1", ItemID: udSyncID(31), TimeSec: 100, Title: "conformance bookmark",
		CreatedAt: 1785370279374,
	})

	h := newHarness(t, "cf,jwt", nil, withUserData(udProvider(t, f)))
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	access := str(t, userObj(t, h.login(t, "owner", "pw-pw-pw-pw")), "accessToken")

	w, body := h.do(t, request{method: http.MethodGet, path: "/api/me",
		headers: map[string]string{"Authorization": "Bearer " + access}})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	rows, ok := body["mediaProgress"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("mediaProgress = %v, want exactly 1 row", body["mediaProgress"])
	}
	first, _ := rows[0].(map[string]any)
	if got, _ := first["libraryItemId"].(string); got != udSyncID(31) {
		t.Fatalf("libraryItemId = %v want %s", first["libraryItemId"], udSyncID(31))
	}
	if got, _ := first["currentTime"].(float64); got != 1800 {
		t.Fatalf("currentTime = %v want 1800", got)
	}
	if got, _ := first["progress"].(float64); got != 0.25 {
		t.Fatalf("progress = %v want 0.25", got)
	}
	marks, ok := body["bookmarks"].([]any)
	if !ok || len(marks) != 1 {
		t.Fatalf("bookmarks = %v, want exactly 1", body["bookmarks"])
	}
}
