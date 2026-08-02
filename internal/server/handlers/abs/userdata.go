// file: internal/server/handlers/abs/userdata.go
// version: 1.2.0
// guid: 63289143-7fae-47b5-9ed9-888ac3c2034a
// last-edited: 2026-08-02

package abs

import (
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sort"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
	"golang.org/x/sync/errgroup"
)

// This file implements UserDataProvider (see handler.go) — the `mediaProgress[]`
// and `bookmarks[]` arrays carried by GET /api/me, POST /login and
// POST /auth/refresh.
//
// 🔴 READ THIS BEFORE CHANGING ANYTHING HERE.
// §1.8.1: AudioBooth's MediaProgress.syncFromAPI walks the device's LOCAL progress
// rows and DELETES every one whose bookID is absent from the server's list, sparing
// only the currently-playing book. Three consequences shape every decision below:
//
//  1. The list must be COMPLETE. There is no pagination, no limit, no "skip the
//     awkward row" path. Anything we omit, the client destroys.
//  2. A partial read must become an ERROR, never a short list. Every helper here
//     propagates its error up through MediaProgress, which returns a nil slice
//     alongside it so a caller physically cannot serve a truncated body; the
//     handler turns that into a 5xx (me.go). A retry costs nothing.
//  3. When a single field cannot be produced safely we DEGRADE THE FIELD, never
//     drop the row — see the isFinished clamp in progressRow.
//
// Cost: this payload is rebuilt on every login and every home-screen refresh, so
// nothing here may scan the library. The enumeration primitives are both
// USER-KEYED prefix scans (ListUserPositionsSince over "upos:<userID>:" and
// ListBookmarksForUser over "bookmark:<userID>:"), so the work is proportional to
// the books THIS USER has touched, not to the 40,000-book library. The per-book
// follow-up reads then run in a bounded worker pool (CLAUDE.md's concurrency
// rule).

// ProgressListStore is the enumeration + state slice of the listening-progress
// keyspace this provider needs. It is deliberately separate from ProgressStore
// (handler.go), which is the per-book read/write slice the play path uses.
//
// ListUserPositionsSince is the load-bearing method: it prefix-scans
// "upos:<userID>:" across ALL books, which is what makes a complete list
// affordable on a request path. There is no whole-library iteration anywhere in
// this file, and there must never be one.
type ProgressListStore interface {
	ListUserPositionsSince(userID string, since time.Time) ([]database.UserPosition, error)
	GetUserBookState(userID, bookID string) (*database.UserBookState, error)
	// GetUserPosition serves MediaProgressFor. A single-key get rather than a
	// filtered rescan of ListUserPositionsSince: GET /api/me/progress/:id is on
	// the client's reset-progress path and there is no reason to walk every book
	// the user has touched to answer a question about one of them.
	GetUserPosition(userID, bookID string) (*database.UserPosition, error)
}

// BookmarkListStore is the named-bookmark slice. ListBookmarksForUser exists
// specifically so this response can be built with one scan
// (pebble_store_bookmarks.go).
type BookmarkListStore interface {
	ListBookmarksForUser(userID string) ([]progress.Bookmark, error)
}

// SyncIDStore resolves a Book ULID to its durable, client-visible 36-char sync
// UUID. GetSyncIDForBook is the read path; MintOrGetSyncID is the fallback for a
// book that has progress but no sync_item yet — see syncIDFor for why minting on
// a read path is the correct trade.
type SyncIDStore interface {
	GetSyncIDForBook(bookID string) (string, bool, error)
	MintOrGetSyncID(bookID string) (string, error)
}

// UserDataLibraryStore is the two-method duration slice. LibraryStore already
// satisfies it structurally, so the caller passes the same value.
type UserDataLibraryStore interface {
	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

// UserDataOptions are the provider's dependencies. Every one is REQUIRED:
// NewUserData refuses to build without any of them, because a provider missing a
// dependency would answer an empty list where records exist, and an empty list is
// only safe when it is genuinely the complete list.
type UserDataOptions struct {
	Progress  ProgressListStore
	Bookmarks BookmarkListStore
	Identity  SyncIDStore
	Library   UserDataLibraryStore

	// Concurrency bounds the per-book follow-up reads. Zero means
	// runtime.NumCPU(). Never unbounded: the collection is user-scoped but a
	// heavy listener can still have thousands of rows.
	Concurrency int
}

// bookmarkDTO is one ABS named bookmark, matching the oracle capture in
// testdata/abs-fixtures/get_api_me.json exactly: four fields, no id, no userId,
// no updatedAt.
//
// Time is SECONDS as a JSON number (int or float both encode as a number, which
// is what clients accept — AudioBooth sends an Int on some paths and a Double on
// others). CreatedAt is an integer ms epoch like every other date on this
// surface (§1.7.3 item 5).
type bookmarkDTO struct {
	CreatedAt     int64   `json:"createdAt"`
	LibraryItemID string  `json:"libraryItemId"`
	Time          float64 `json:"time"`
	Title         string  `json:"title"`
}

type userDataProvider struct {
	progress    ProgressListStore
	bookmarks   BookmarkListStore
	identity    SyncIDStore
	library     UserDataLibraryStore
	concurrency int
}

var _ UserDataProvider = (*userDataProvider)(nil)

// NewUserData builds the Phase 6 progress/bookmark provider.
//
// It returns an error rather than a degraded provider when a dependency is
// missing. The caller (wireABSRoutes) exits on that error: booting with a
// provider that reports an empty mediaProgress while the store holds real
// records would delete the owner's listening positions on the next home-screen
// refresh, which is strictly worse than not booting.
func NewUserData(o UserDataOptions) (UserDataProvider, error) {
	if o.Progress == nil {
		return nil, errors.New("abs: userdata: a progress store is required (an empty list deletes the user's positions)")
	}
	if o.Bookmarks == nil {
		return nil, errors.New("abs: userdata: a bookmark store is required (an empty list deletes the user's bookmarks)")
	}
	if o.Identity == nil {
		return nil, errors.New("abs: userdata: a sync-identity store is required (libraryItemId must be the 36-char sync UUID)")
	}
	if o.Library == nil {
		return nil, errors.New("abs: userdata: a library store is required (isFinished with a zero duration zeroes the client's position)")
	}
	limit := o.Concurrency
	if limit <= 0 {
		limit = runtime.NumCPU()
	}
	return &userDataProvider{
		progress:    o.Progress,
		bookmarks:   o.Bookmarks,
		identity:    o.Identity,
		library:     o.Library,
		concurrency: limit,
	}, nil
}

// MediaProgress returns the user's COMPLETE progress list, or an error.
//
// Shape of the work:
//
//  1. ONE user-keyed prefix scan enumerates every stored position row. The
//     "since" argument is the zero time, i.e. "since the beginning of time" —
//     the store filters with UpdatedAt.After(since), so the zero value keeps
//     every row. Passing anything else here would silently truncate the list.
//  2. Rows collapse to one per book. The UserPosition keyspace is per
//     (user, book, SEGMENT) because the app's own reader tracks a position per
//     segment; ABS has exactly one whole-book position per item.
//  3. The per-book follow-ups (sync id, duration, derived state) run in a bounded
//     pool, writing into a pre-sized slice by index so concurrency cannot reorder
//     or short the list.
func (p *userDataProvider) MediaProgress(userID string) ([]any, error) {
	if userID == "" {
		// An empty id would prefix-scan "upos::" and answer a confidently wrong
		// list for nobody. Refuse.
		return nil, errors.New("abs: userdata: userID is required")
	}

	positions, err := p.progress.ListUserPositionsSince(userID, time.Time{})
	if err != nil {
		return nil, fmt.Errorf("abs: userdata: list positions for user %s: %w", userID, err)
	}

	latest := make(map[string]database.UserPosition, len(positions))
	for _, pos := range positions {
		if pos.BookID == "" {
			// A row with no book cannot be named with a libraryItemId, so no client
			// can have it either — skipping it drops nothing the client could
			// delete. Logged because it means a corrupt write happened somewhere.
			slog.Warn("abs: userdata: skipping a stored position with no book id", "user_id", userID, "segment_id", pos.SegmentID)
			continue
		}
		cur, seen := latest[pos.BookID]
		if !seen || betterPosition(pos, cur) {
			latest[pos.BookID] = pos
		}
	}
	if len(latest) == 0 {
		// Non-nil: `mediaProgress` must marshal as [] and never as null.
		return []any{}, nil
	}

	bookIDs := make([]string, 0, len(latest))
	for id := range latest {
		bookIDs = append(bookIDs, id)
	}
	// Sorted so the body is byte-stable across refreshes rather than reshuffling
	// with Go's map iteration order.
	sort.Strings(bookIDs)

	rows := make([]any, len(bookIDs))
	g := new(errgroup.Group)
	g.SetLimit(p.concurrency)
	for i := range bookIDs {
		i := i
		g.Go(func() error {
			row, err := p.progressRow(userID, latest[bookIDs[i]])
			if err != nil {
				return err
			}
			rows[i] = row
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		// The partially-filled slice is DISCARDED on purpose. Handing it back would
		// let a caller serve a 200 with holes in it, and every hole is a book whose
		// progress the client then deletes (§1.8.1).
		return nil, err
	}
	return rows, nil
}

// MediaProgressFor renders the single row GET /api/me/progress/:id serves.
//
// It routes through the SAME progressRow renderer the list uses, which is the
// whole point of the method existing: the client compares the single-item body
// against the copy of the same book inside /api/me's list, resolving conflicts on
// `lastUpdate` with a strict `>` after truncating both sides to whole seconds. Two
// renderers that disagree by one field — or by one rounding step in the finished
// tolerance — produce a book that re-syncs forever.
//
// ok=false means the user has never started this book, which the handler turns
// into the 404 real ABS answers there. An error means we could not tell, which
// becomes a 5xx: a 404 we are not sure about reads to the client as "no progress",
// and that is the one answer that can cost a position.
func (p *userDataProvider) MediaProgressFor(userID, bookID string) (any, bool, error) {
	if userID == "" || bookID == "" {
		return nil, false, errors.New("abs: userdata: userID and bookID are required")
	}
	pos, err := p.progress.GetUserPosition(userID, bookID)
	if err != nil {
		return nil, false, fmt.Errorf("abs: userdata: load position for %s/%s: %w", userID, bookID, err)
	}
	if pos == nil {
		return nil, false, nil
	}
	// GetUserPosition answers for the (user, book) without naming a segment, so
	// normalize the keys the renderer relies on rather than trusting whichever
	// segment row the store happened to return.
	pos.UserID, pos.BookID = userID, bookID

	row, err := p.progressRow(userID, *pos)
	if err != nil {
		return nil, false, err
	}
	return row, true, nil
}

// betterPosition decides which of two position rows for the SAME book wins.
//
// Newest write wins, and a timestamp TIE resolves to the further position rather
// than to whichever row the iterator happened to reach first. That tie-break is
// deliberately forward-only: rewinding the listener is the failure this whole
// surface exists to prevent, and the resulting position can only ever be >= the
// one GetUserPosition (pebble_store_playback.go) picks for the same book, so
// /api/items and /api/me can never disagree in the direction that loses a place
// in a book.
func betterPosition(candidate, current database.UserPosition) bool {
	if candidate.UpdatedAt.After(current.UpdatedAt) {
		return true
	}
	if current.UpdatedAt.After(candidate.UpdatedAt) {
		return false
	}
	return candidate.PositionSeconds > current.PositionSeconds
}

// progressRow renders one mediaProgress entry.
//
// Every field is a verified client requirement, not a nicety — the field-by-field
// reasoning is in item.go's mediaProgress (the /api/items/:id twin), and the two
// must stay consistent because a client compares them for the same book. In
// short: lastUpdate is an integer ms epoch (omit it and the server loses every
// future conflict, because clients compare it against their own wall clock and
// ties go to local); duration is always emitted alongside isFinished; progress is
// a 0.0–1.0 FRACTION, never a percentage.
func (p *userDataProvider) progressRow(userID string, pos database.UserPosition) (mediaProgressDTO, error) {
	syncID, err := p.syncIDFor(pos.BookID)
	if err != nil {
		return mediaProgressDTO{}, err
	}
	duration, err := p.durationFor(pos.BookID)
	if err != nil {
		return mediaProgressDTO{}, err
	}
	state, err := p.progress.GetUserBookState(userID, pos.BookID)
	if err != nil {
		return mediaProgressDTO{}, fmt.Errorf("abs: userdata: load book state for %s/%s: %w", userID, pos.BookID, err)
	}

	// §5b's >=2s tolerance, never a tight compare: a book's three legitimate
	// durations disagree by ~52ms (container vs chapter marks vs track sum), so an
	// exact comparison leaves a fully-listened book stuck at 99% forever.
	finished := progress.IsWithinFinishedTolerance(pos.PositionSeconds, duration)
	if state != nil && state.Status == database.UserBookStatusFinished {
		finished = true
	}

	fraction := 0.0
	if duration > 0 {
		fraction = pos.PositionSeconds / duration
		if fraction > 1 || finished {
			// A finished book reads 1.0 exactly. Reporting 0.99998 next to
			// isFinished:true is the "sits at 99% forever" symptom of §5b showing up
			// in the progress bar instead of in the finished flag.
			fraction = 1
		}
	}

	lastUpdate := lastUpdateMs(pos, state)
	// The user's own "remove from Continue Listening" choice, persisted on the
	// book state. Never derived: a false here because the row could not be read
	// would silently put a book the user hid back on their home screen.
	hidden := state != nil && state.HideFromContinueListening

	row := mediaProgressDTO{
		CurrentTime: pos.PositionSeconds,
		Duration:    duration,
		// ebook fields: this surface serves audiobooks only. Null/zero, never absent.
		EbookProgress:             0,
		HideFromContinueListening: hidden,
		// Derived from (user, item) rather than random so a client that stores the id
		// keeps matching the same row across restarts. Same formula as item.go.
		ID:            userID + "-" + syncID,
		IsFinished:    finished,
		LastUpdate:    lastUpdate,
		LibraryItemID: syncID,
		MediaItemID:   syncID,
		MediaItemType: "book",
		Progress:      fraction,
		StartedAt:     lastUpdate,
		UserID:        userID,
	}
	if finished {
		at := lastUpdate
		row.FinishedAt = &at
	}

	// §1.8.7's null-duration trap: `isFinished:true` with a zero duration sets the
	// client's currentTime to 0 — it DESTROYS the very position this row exists to
	// report. When we cannot prove a duration (a fileless book, a book whose row is
	// gone), the safe answer is to clear isFinished and keep the position, NOT to
	// drop the row: dropping it is what makes the client delete its own copy.
	if err := progress.ValidateFinishedDuration(progress.Progress{
		CurrentTime: row.CurrentTime, Duration: row.Duration,
		IsFinished: row.IsFinished, UpdatedAtMs: row.LastUpdate,
	}); err != nil {
		slog.Warn("abs: userdata: clearing isFinished for a book with no known duration — "+
			"reporting it would zero the client's saved position",
			"user_id", userID, "book_id", pos.BookID, "library_item_id", syncID, "err", err)
		row.IsFinished = false
		row.FinishedAt = nil
		row.Progress = 0
	}
	return row, nil
}

// lastUpdateMs picks the millisecond epoch clients conflict-resolve against.
//
// §1.7.3 item 1 makes this the single highest-value field in the protocol: omit
// it (or emit a 0) and the server permanently loses every conflict, because
// AudioBooth compares it against its own wall clock — truncating BOTH sides via
// integer /1000 and comparing with strict > — so a tie goes to the client's
// cached value.
//
// UserBookState.LastActivityAt is the authoritative source: UserPosition.UpdatedAt
// is stamped time.Now() by the store on every write and so cannot carry a
// caller-supplied timestamp. The fallbacks exist only so a row written before its
// state existed still gets a usable value instead of a 0; a 0 would hand every
// future conflict to the client, which at least keeps the client's own position
// rather than losing it.
func lastUpdateMs(pos database.UserPosition, state *database.UserBookState) int64 {
	if state != nil {
		if ms := msEpoch(state.LastActivityAt); ms > 0 {
			return ms
		}
		if ms := msEpoch(state.UpdatedAt); ms > 0 {
			return ms
		}
	}
	return msEpoch(pos.UpdatedAt)
}

// syncIDFor resolves the client-visible 36-char sync UUID for a book.
//
// 🔴 libraryItemId MUST be the sync UUID and never the raw 26-char Book ULID:
// Absorb splits compound ids by FIXED BYTE OFFSET substring(0,36) at 4+ call
// sites (§1.7.1), so a ULID mis-truncates into the wrong /api/me/progress path.
//
// A book with progress but no sync_item yet is MINTED here rather than skipped.
// Minting is a write on a read path, which is not free — but the alternative is
// omitting a row the client holds, and the client deletes what we omit. The mint
// is idempotent, happens at most once per book ever, and after the identity
// backfill it happens for nothing at all.
func (p *userDataProvider) syncIDFor(bookID string) (string, error) {
	id, ok, err := p.identity.GetSyncIDForBook(bookID)
	if err != nil {
		return "", fmt.Errorf("abs: userdata: resolve sync id for book %s: %w", bookID, err)
	}
	if ok && id != "" {
		return id, nil
	}
	id, err = p.identity.MintOrGetSyncID(bookID)
	if err != nil {
		return "", fmt.Errorf("abs: userdata: mint sync id for book %s: %w", bookID, err)
	}
	if id == "" {
		return "", fmt.Errorf("abs: userdata: minted an empty sync id for book %s", bookID)
	}
	return id, nil
}

// durationFor returns the book's authoritative duration in seconds.
//
// §5b requires ONE duration per book used consistently across media.duration, the
// play session and the progress math, because the three legitimate sources
// disagree by ~52ms and mixing them leaves a finished book stuck at 99%. This
// reproduces the mapper's rule exactly (loadOneItemView in mapper.go): the sum of
// the per-file durations, with Book.Duration used ONLY for a book that has no
// BookFile rows at all. Do not "improve" one of the two in isolation.
//
// A book that resolves to nothing at all returns 0, which is honest: 0 makes
// IsWithinFinishedTolerance false, so no row can claim isFinished without a
// duration to back it.
func (p *userDataProvider) durationFor(bookID string) (float64, error) {
	files, err := p.library.GetBookFiles(bookID)
	if err != nil {
		return 0, fmt.Errorf("abs: userdata: load files for book %s: %w", bookID, err)
	}
	if len(files) > 0 {
		total := 0.0
		for i := range files {
			total += float64(files[i].Duration)
		}
		return total, nil
	}
	book, err := p.library.GetBookByID(bookID)
	if err != nil {
		return 0, fmt.Errorf("abs: userdata: load book %s: %w", bookID, err)
	}
	if book != nil && book.Duration != nil {
		return float64(*book.Duration), nil
	}
	return 0, nil
}

// ListenedSeconds totals the user's listened time across every book they have
// touched, from the per-book UserBookState the playback sync maintains.
//
// One user-keyed prefix scan for the book list, then bounded per-book state reads —
// the same shape and the same cost as MediaProgress, and for the same reason: this
// is a request path and nothing here may scan the library.
//
// A per-book state that cannot be read contributes 0 rather than failing the whole
// total. That is the opposite of MediaProgress's discipline, and deliberately so: an
// under-reported statistic is cosmetic, while an under-reported PROGRESS LIST makes
// the client delete books (§1.8.1). Failing this call instead would turn a cosmetic
// gap into an orange "connection error" on the client's home screen.
func (p *userDataProvider) ListenedSeconds(userID string) (float64, error) {
	if userID == "" {
		return 0, errors.New("abs: userdata: userID is required")
	}
	positions, err := p.progress.ListUserPositionsSince(userID, time.Time{})
	if err != nil {
		return 0, fmt.Errorf("abs: userdata: list positions for user %s: %w", userID, err)
	}
	seen := make(map[string]struct{}, len(positions))
	bookIDs := make([]string, 0, len(positions))
	for _, pos := range positions {
		if pos.BookID == "" {
			continue
		}
		if _, dup := seen[pos.BookID]; dup {
			continue
		}
		seen[pos.BookID] = struct{}{}
		bookIDs = append(bookIDs, pos.BookID)
	}
	if len(bookIDs) == 0 {
		return 0, nil
	}

	totals := make([]float64, len(bookIDs))
	g := new(errgroup.Group)
	g.SetLimit(p.concurrency)
	for i := range bookIDs {
		g.Go(func() error {
			state, serr := p.progress.GetUserBookState(userID, bookIDs[i])
			if serr == nil && state != nil && state.TotalListenedSeconds > 0 {
				totals[i] = state.TotalListenedSeconds
			}
			return nil
		})
	}
	_ = g.Wait() // no goroutine returns an error; see the note above
	sum := 0.0
	for _, t := range totals {
		sum += t
	}
	return sum, nil
}

// Bookmarks returns the user's COMPLETE bookmark list, or an error.
//
// One user-keyed prefix scan, no per-item work, no library access — which is why
// ListBookmarksForUser exists. Same error discipline as MediaProgress: a read
// failure is an error, never a short list.
func (p *userDataProvider) Bookmarks(userID string) ([]any, error) {
	if userID == "" {
		return nil, errors.New("abs: userdata: userID is required")
	}
	stored, err := p.bookmarks.ListBookmarksForUser(userID)
	if err != nil {
		return nil, fmt.Errorf("abs: userdata: list bookmarks for user %s: %w", userID, err)
	}

	out := make([]any, 0, len(stored))
	for _, b := range stored {
		if b.ItemID == "" {
			// Unnameable, exactly like a position row with no book: no client can
			// hold it, so skipping it deletes nothing.
			slog.Warn("abs: userdata: skipping a stored bookmark with no item id", "user_id", userID, "time_sec", b.TimeSec)
			continue
		}
		// ItemID is ALREADY the client-visible libraryItemId — the bookmark keyspace
		// is keyed by (userID, libraryItemId, time), mirroring real ABS, whose
		// create/delete surface addresses a bookmark by the item id and the time
		// value in the URL path. It is not translated here.
		out = append(out, bookmarkDTO{
			CreatedAt:     b.CreatedAt,
			LibraryItemID: b.ItemID,
			Time:          b.TimeSec,
			Title:         b.Title,
		})
	}
	// Deterministic ordering (item, then time) so the body is byte-stable across
	// refreshes: the store's scan order is already sorted per item, but the slice
	// crosses items and a client that diffs bodies should see no churn.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].(bookmarkDTO), out[j].(bookmarkDTO)
		if a.LibraryItemID != b.LibraryItemID {
			return a.LibraryItemID < b.LibraryItemID
		}
		return a.Time < b.Time
	})
	return out, nil
}
