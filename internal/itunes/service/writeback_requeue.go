// file: internal/itunes/service/writeback_requeue.go
// version: 1.1.0
// guid: 6c3e8a1f-2b94-4d7e-9f05-a8b1c4d2e7f3
// last-edited: 2026-10-07
//
// Requeue planning for the iTunes write-back batcher. Added 2026-10-07 to
// recover the 4,293 book updates and one remove the batcher dropped before
// #3821/#3822 (the drop log had no ids). See
// docs/plans/2026-10-07-itunes-writeback-requeue.md.
//
// PlanRequeue finds every book whose library tracks differ from what the
// flush would write, using the flush's own planner (planBookWrite), and never
// selects adds or removes. PlanRemoveRequeue checks explicit merged-away loser
// book ids for a remove that was tombstoned but never landed.

package itunesservice

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"golang.org/x/sync/errgroup"
)

// Requeue kinds: which differences select a book.
const (
	RequeueKindMetadata = "metadata"
	RequeueKindLocation = "location"
)

// DefaultRequeueSampleLimit caps the books listed in a requeue plan's sample.
const DefaultRequeueSampleLimit = 50

// maxSampleTracksPerBook caps the tracks listed per sampled book.
const maxSampleTracksPerBook = 10

// RequeueStore is what PlanRequeue reads.
type RequeueStore interface {
	bookPlanStore
	GetBookByID(id string) (*database.Book, error)
	GetAllBooksFullFrom(afterID string, limit int) ([]database.Book, error)
}

// RequeueOptions selects what PlanRequeue considers.
type RequeueOptions struct {
	// BookIDs limits the plan to these books. Empty means the whole library.
	BookIDs []string
	// Metadata and Location select books with that kind of difference. They
	// pick books only: once a book is queued the flush writes every
	// difference it finds for that book.
	Metadata bool
	Location bool
	// SampleLimit caps Sample (0 means DefaultRequeueSampleLimit).
	SampleLimit int
}

// RequeueTrackSample is one changed track in a sampled book.
type RequeueTrackSample struct {
	PID      string               `json:"pid"`
	Metadata *RequeueMetadataDiff `json:"metadata,omitempty"`
	Location *RequeueLocationDiff `json:"location,omitempty"`
}

// RequeueMetadataFields are the metadata fields the flush compares.
type RequeueMetadataFields struct {
	Name   string `json:"name"`
	Album  string `json:"album"`
	Artist string `json:"artist"`
	Genre  string `json:"genre"`
}

// RequeueMetadataDiff is a metadata change: the library's value and the DB's.
type RequeueMetadataDiff struct {
	From RequeueMetadataFields `json:"from"`
	To   RequeueMetadataFields `json:"to"`
}

// RequeueLocationDiff is a location change.
type RequeueLocationDiff struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// RequeueBookSample is one selected book and what would change.
type RequeueBookSample struct {
	BookID string               `json:"book_id"`
	Title  string               `json:"title"`
	Tracks []RequeueTrackSample `json:"tracks"`
	// TracksTruncated is true when the book has more changed tracks than
	// listed.
	TracksTruncated bool `json:"tracks_truncated,omitempty"`
}

// RequeuePlan is the outcome of PlanRequeue.
type RequeuePlan struct {
	TracksInLibrary int `json:"tracks_in_library"`
	// BooksScanned counts primary, non-deleted books that were planned.
	BooksScanned int `json:"books_scanned"`
	// BooksWithMetadataChanges / BooksWithLocationChanges count books with at
	// least one such difference on a track that is in the library,
	// regardless of the requested kinds.
	BooksWithMetadataChanges int `json:"books_with_metadata_changes"`
	BooksWithLocationChanges int `json:"books_with_location_changes"`
	// TrackMetadataChanges / TrackLocationChanges count the tracks (in the
	// library) of the SELECTED books that the flush would rewrite.
	TrackMetadataChanges int `json:"track_metadata_changes"`
	TrackLocationChanges int `json:"track_location_changes"`
	// BooksSelected is the number of books the requeue queues.
	BooksSelected int `json:"books_selected"`
	// IgnoredAddTracks counts DB PIDs with no track in the library (a
	// rebuild would add them). Never queued here. A selected book that also
	// has such PIDs still gets updates emitted for them by the flush, as it
	// always has; the contract decides what happens to those.
	IgnoredAddTracks int `json:"ignored_add_tracks"`
	// IgnoredRemoveTracks counts library tracks no scanned book claims (a
	// rebuild would remove them). Never queued here. Nil on a subset plan,
	// where it means nothing.
	IgnoredRemoveTracks *int `json:"ignored_remove_tracks,omitempty"`
	// BookFileLookupErrors counts books whose book files could not be read.
	// They are planned the way the flush plans them (book-level PID only).
	BookFileLookupErrors int `json:"book_file_lookup_errors"`
	// Subset-only outcomes.
	NotFound          []string `json:"not_found,omitempty"`
	SkippedDeleted    []string `json:"skipped_deleted,omitempty"`
	SkippedNonPrimary []string `json:"skipped_non_primary,omitempty"`
	LookupFailed      []string `json:"lookup_failed,omitempty"`

	Sample []RequeueBookSample `json:"sample"`
	// SelectedBookIDs is every selected book, sorted. Not serialized: the
	// handler enqueues it.
	SelectedBookIDs []string `json:"-"`
}

// TracksByPID indexes a parsed library by lowercase PID hex, the key the
// flush uses.
func TracksByPID(lib *itunes.ITLLibrary) map[string]*itunes.ITLTrack {
	out := make(map[string]*itunes.ITLTrack, len(lib.Tracks))
	for i := range lib.Tracks {
		t := &lib.Tracks[i]
		out[strings.ToLower(hex.EncodeToString(t.PersistentID[:]))] = t
	}
	return out
}

// bookOutcome is one book's planning result, merged under a mutex.
type bookOutcome struct {
	book      *database.Book
	plan      bookWritePlan
	metaBook  bool
	locBook   bool
	metaCount int
	locCount  int
	absent    int
}

func classify(book *database.Book, plan bookWritePlan) bookOutcome {
	o := bookOutcome{book: book, plan: plan}
	for _, ch := range plan.Changes {
		if ch.Current == nil {
			o.absent++
			continue
		}
		if ch.Metadata != nil {
			o.metaBook = true
			o.metaCount++
		}
		if ch.Location != nil {
			o.locBook = true
			o.locCount++
		}
	}
	return o
}

// PlanRequeue plans which books to put back on the write-back queue. lib is
// the parsed write target (the file the flush writes). Selection uses
// planBookWrite, so a book is selected exactly when a flush of it would write
// at least one track that is already in the library.
//
// The per-book work reads book files and authors, so books are planned on a
// worker pool sized to runtime.NumCPU(). Workers only read; results are merged
// under a mutex and sorted by book id.
func PlanRequeue(ctx context.Context, store RequeueStore, lib *itunes.ITLLibrary, opts RequeueOptions) (*RequeuePlan, error) {
	if store == nil || lib == nil {
		return nil, errors.New("requeue plan needs a store and a parsed library")
	}
	if !opts.Metadata && !opts.Location {
		return nil, errors.New("requeue plan needs at least one kind")
	}
	sampleLimit := opts.SampleLimit
	if sampleLimit <= 0 {
		sampleLimit = DefaultRequeueSampleLimit
	}

	tracks := TracksByPID(lib)
	plan := &RequeuePlan{TracksInLibrary: len(tracks)}

	var mu sync.Mutex
	var outcomes []bookOutcome
	claimed := make(map[string]bool)
	record := func(o bookOutcome) {
		mu.Lock()
		defer mu.Unlock()
		outcomes = append(outcomes, o)
		for _, pid := range o.plan.PIDs {
			claimed[pid] = true
		}
	}
	note := func(list *[]string, id string) {
		mu.Lock()
		*list = append(*list, id)
		mu.Unlock()
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())

	if len(opts.BookIDs) > 0 {
		seen := make(map[string]bool, len(opts.BookIDs))
		for _, id := range opts.BookIDs {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			g.Go(func() error {
				if err := gctx.Err(); err != nil {
					return err
				}
				book, err := store.GetBookByID(id)
				switch {
				case err != nil:
					note(&plan.LookupFailed, id)
				case book == nil:
					note(&plan.NotFound, id)
				case book.IsSoftDeleted():
					note(&plan.SkippedDeleted, id)
				case book.IsPrimaryVersion != nil && !*book.IsPrimaryVersion:
					note(&plan.SkippedNonPrimary, id)
				default:
					record(classify(book, planBookWrite(store, book, tracks)))
				}
				return nil
			})
		}
	} else {
		const pageSize = 500
		afterID := ""
		for {
			if err := gctx.Err(); err != nil {
				break
			}
			books, err := store.GetAllBooksFullFrom(afterID, pageSize)
			if err != nil {
				_ = g.Wait()
				return nil, fmt.Errorf("get books after %q: %w", afterID, err)
			}
			if len(books) == 0 {
				break
			}
			for i := range books {
				book := &books[i]
				// Same filter as the flush: only primary versions are
				// written. Soft-deleted books are not in this listing, but
				// the check is cheap and explicit.
				if book.IsSoftDeleted() || (book.IsPrimaryVersion != nil && !*book.IsPrimaryVersion) {
					continue
				}
				g.Go(func() error {
					if err := gctx.Err(); err != nil {
						return err
					}
					record(classify(book, planBookWrite(store, book, tracks)))
					return nil
				})
			}
			afterID = books[len(books)-1].ID
			if len(books) < pageSize {
				break
			}
		}
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	sort.Slice(outcomes, func(i, j int) bool { return outcomes[i].book.ID < outcomes[j].book.ID })
	for _, l := range [][]string{plan.NotFound, plan.SkippedDeleted, plan.SkippedNonPrimary, plan.LookupFailed} {
		sort.Strings(l)
	}

	for _, o := range outcomes {
		plan.BooksScanned++
		plan.IgnoredAddTracks += o.absent
		if o.plan.FilesErr != nil {
			plan.BookFileLookupErrors++
		}
		if o.metaBook {
			plan.BooksWithMetadataChanges++
		}
		if o.locBook {
			plan.BooksWithLocationChanges++
		}
		if !(opts.Metadata && o.metaBook) && !(opts.Location && o.locBook) {
			continue
		}
		plan.BooksSelected++
		plan.TrackMetadataChanges += o.metaCount
		plan.TrackLocationChanges += o.locCount
		plan.SelectedBookIDs = append(plan.SelectedBookIDs, o.book.ID)
		if len(plan.Sample) < sampleLimit {
			plan.Sample = append(plan.Sample, sampleBook(o))
		}
	}

	if len(opts.BookIDs) == 0 {
		unclaimed := 0
		for pid := range tracks {
			if !claimed[pid] {
				unclaimed++
			}
		}
		plan.IgnoredRemoveTracks = &unclaimed
	}
	if plan.Sample == nil {
		plan.Sample = []RequeueBookSample{}
	}
	return plan, nil
}

func sampleBook(o bookOutcome) RequeueBookSample {
	s := RequeueBookSample{BookID: o.book.ID, Title: o.book.Title, Tracks: []RequeueTrackSample{}}
	for _, ch := range o.plan.Changes {
		if ch.Current == nil {
			continue
		}
		if len(s.Tracks) == maxSampleTracksPerBook {
			s.TracksTruncated = true
			break
		}
		ts := RequeueTrackSample{PID: strings.ToLower(ch.PID)}
		if ch.Metadata != nil {
			ts.Metadata = &RequeueMetadataDiff{
				From: RequeueMetadataFields{Name: ch.Current.Name, Album: ch.Current.Album, Artist: ch.Current.Artist, Genre: ch.Current.Genre},
				To:   RequeueMetadataFields{Name: ch.Metadata.Name, Album: ch.Metadata.Album, Artist: ch.Metadata.Artist, Genre: ch.Metadata.Genre},
			}
		}
		if ch.Location != nil {
			ts.Location = &RequeueLocationDiff{From: ch.Current.Location, To: ch.Location.NewLocation}
		}
		s.Tracks = append(s.Tracks, ts)
	}
	return s
}

// MaxRemoveRequeueBooks caps the loser book ids one remove-requeue request
// may name. Removes are destructive and nothing withdraws a queued one.
const MaxRemoveRequeueBooks = 5

// ErrRemoveRequeueRequest is returned for a request PlanRemoveRequeue refuses
// outright (no ids, too many ids, too many PIDs).
var ErrRemoveRequeueRequest = errors.New("invalid remove requeue request")

// RemoveRequeueStore is what PlanRemoveRequeue reads.
type RemoveRequeueStore interface {
	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	IsExternalIDTombstoned(source, externalID string) (bool, error)
	// GetAllBookFilesCore finds EVERY book_file row holding a PID. The
	// book_file_pid index (GetBookFileByPID) keeps one row per PID, and
	// duplicate PIDs across rows are a known condition (/itunes/pid-integrity),
	// so the index could return the loser's row and hide a live duplicate.
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	ListBooksByITunesPID(limit, offset int) ([]database.Book, error)
}

// RemoveRequeuePID is one PID found on a loser book and whether it may be
// queued for removal.
type RemoveRequeuePID struct {
	PID       string `json:"pid"`
	InLibrary bool   `json:"in_library"`
	TrackName string `json:"track_name,omitempty"`
	Eligible  bool   `json:"eligible"`
	// Reason says why an ineligible PID is refused, or notes an eligible
	// one ("already queued").
	Reason string `json:"reason,omitempty"`
}

// RemoveRequeueBook is one requested loser book.
type RemoveRequeueBook struct {
	BookID string `json:"book_id"`
	Title  string `json:"title,omitempty"`
	// Refused says why the whole book was refused (not found, live primary).
	Refused string             `json:"refused,omitempty"`
	PIDs    []RemoveRequeuePID `json:"pids"`
}

// RemoveRequeuePlan is the outcome of PlanRemoveRequeue.
type RemoveRequeuePlan struct {
	Books []RemoveRequeueBook `json:"books"`
	// EligiblePIDs is every eligible PID not already queued, lowercase.
	EligiblePIDs []string `json:"eligible_pids"`
}

// PlanRemoveRequeue checks explicit merged-away loser book ids for iTunes
// removes that were tombstoned but never landed (the pre-v6 batcher tombstoned
// at enqueue, then dropped the batch).
//
// A merge reassigns the loser's external-id rows to the winner before it
// queues the remove, so the PIDs are read from the loser's own book row and
// book_file rows, not its external-id rows. A PID is eligible only when:
//   - the loser is soft-deleted or explicitly non-primary;
//   - its external-id row exists and is tombstoned;
//   - no other live book holds it (any book_file row, or a book-level PID),
//     so the remove cannot take a survivor's track;
//   - the track is still in the library;
//   - it is not on the held list.
func (b *WriteBackBatcher) PlanRemoveRequeue(store RemoveRequeueStore, lib *itunes.ITLLibrary, bookIDs []string) (*RemoveRequeuePlan, error) {
	if store == nil || lib == nil {
		return nil, errors.New("remove requeue plan needs a store and a parsed library")
	}
	ids := dedupeNonEmpty(bookIDs)
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: book_ids is required", ErrRemoveRequeueRequest)
	}
	if len(ids) > MaxRemoveRequeueBooks {
		return nil, fmt.Errorf("%w: at most %d book_ids per request, got %d", ErrRemoveRequeueRequest, MaxRemoveRequeueBooks, len(ids))
	}
	tracks := TracksByPID(lib)

	// Pass 1: the requested books and the PIDs on them.
	type loserPIDs struct {
		rb   RemoveRequeueBook
		pids []string // as stored, first-seen order, deduplicated by case
	}
	losers := make([]loserPIDs, 0, len(ids))
	wanted := make(map[string]bool)
	for _, id := range ids {
		lp := loserPIDs{rb: RemoveRequeueBook{BookID: id, PIDs: []RemoveRequeuePID{}}}
		book, err := store.GetBookByID(id)
		switch {
		case err != nil:
			lp.rb.Refused = fmt.Sprintf("book lookup failed: %v", err)
		case book == nil:
			lp.rb.Refused = "book not found"
		case !book.IsSoftDeleted() && (book.IsPrimaryVersion == nil || *book.IsPrimaryVersion):
			lp.rb.Title = book.Title
			lp.rb.Refused = "book is live and not marked non-primary; only a merged-away or deleted book's remove can be re-queued here"
		}
		if lp.rb.Refused != "" {
			losers = append(losers, lp)
			continue
		}
		lp.rb.Title = book.Title
		var raw []string
		if book.ITunesPersistentID != nil && *book.ITunesPersistentID != "" {
			raw = append(raw, *book.ITunesPersistentID)
		}
		files, ferr := store.GetBookFiles(id)
		if ferr != nil {
			lp.rb.Refused = fmt.Sprintf("book file lookup failed: %v", ferr)
			losers = append(losers, lp)
			continue
		}
		for _, f := range files {
			if f.ITunesPersistentID != "" {
				raw = append(raw, f.ITunesPersistentID)
			}
		}
		seen := make(map[string]bool)
		for _, pid := range raw {
			key := strings.ToLower(pid)
			if seen[key] {
				continue
			}
			seen[key] = true
			lp.pids = append(lp.pids, pid)
			wanted[key] = true
		}
		if len(lp.pids) == 0 {
			lp.rb.Refused = "no iTunes PID on the book or its files"
		}
		losers = append(losers, lp)
	}

	// Pass 2: every holder of those PIDs, at book level and on any book_file
	// row. One listing each per request; this endpoint takes a handful of
	// explicit ids and runs rarely.
	holders := make(map[string]map[string]bool) // lowercase PID -> book ids
	addHolder := func(pid, bookID string) {
		k := strings.ToLower(pid)
		if !wanted[k] {
			return
		}
		if holders[k] == nil {
			holders[k] = make(map[string]bool)
		}
		holders[k][bookID] = true
	}
	if len(wanted) > 0 {
		liveBooks, err := store.ListBooksByITunesPID(0, 0)
		if err != nil {
			return nil, fmt.Errorf("list books by iTunes PID: %w", err)
		}
		for i := range liveBooks {
			if lb := &liveBooks[i]; lb.ITunesPersistentID != nil && !lb.IsSoftDeleted() {
				addHolder(*lb.ITunesPersistentID, lb.ID)
			}
		}
		allFiles, err := store.GetAllBookFilesCore()
		if err != nil {
			return nil, fmt.Errorf("list book files: %w", err)
		}
		for i := range allFiles {
			if f := &allFiles[i]; f.ITunesPersistentID != "" {
				addHolder(f.ITunesPersistentID, f.BookID)
			}
		}
	}

	// Pass 3: eligibility.
	plan := &RemoveRequeuePlan{Books: []RemoveRequeueBook{}, EligiblePIDs: []string{}}
	queued := make(map[string]bool)
	for _, lp := range losers {
		rb := lp.rb
		if rb.Refused == "" {
			for _, pid := range lp.pids {
				key := strings.ToLower(pid)
				c := RemoveRequeuePID{PID: key}
				if t, ok := tracks[key]; ok {
					c.InLibrary = true
					c.TrackName = t.Name
				}
				c.Reason = b.removeIneligibleReason(store, rb.BookID, pid, c.InLibrary, holders[key])
				if c.Reason == "" {
					c.Eligible = true
					if b.IsRemovePending(key) {
						c.Reason = "already queued"
					} else if !queued[key] {
						queued[key] = true
						plan.EligiblePIDs = append(plan.EligiblePIDs, key)
					}
				}
				rb.PIDs = append(rb.PIDs, c)
			}
		}
		plan.Books = append(plan.Books, rb)
	}
	if len(plan.EligiblePIDs) > MaxRemovesPerFlush {
		return nil, fmt.Errorf("%w: %d eligible PIDs exceed MaxRemovesPerFlush=%d; name fewer books", ErrRemoveRequeueRequest, len(plan.EligiblePIDs), MaxRemovesPerFlush)
	}
	return plan, nil
}

// removeIneligibleReason returns why pid (found on loser book loserID) must
// not be queued for removal, or "" when it may be. holders is every book that
// holds pid at book level (live books only) or on a book_file row.
func (b *WriteBackBatcher) removeIneligibleReason(store RemoveRequeueStore, loserID, pid string, inLibrary bool, holders map[string]bool) string {
	others := make([]string, 0, len(holders))
	for id := range holders {
		if id != loserID {
			others = append(others, id)
		}
	}
	sort.Strings(others)
	for _, other := range others {
		owner, err := store.GetBookByID(other)
		if err != nil {
			return fmt.Sprintf("lookup of PID holder %s failed: %v", other, err)
		}
		if owner != nil && !owner.IsSoftDeleted() {
			return fmt.Sprintf("PID is also held by live book %s", other)
		}
	}
	tombstoned := false
	for _, v := range pidCaseVariants(pid) {
		t, err := store.IsExternalIDTombstoned("itunes", v)
		if err != nil {
			return fmt.Sprintf("external-id lookup failed: %v", err)
		}
		if t {
			tombstoned = true
			break
		}
	}
	if !tombstoned {
		return "external-id row is not tombstoned (or does not exist)"
	}
	if !inLibrary {
		return "track is not in the library; nothing to remove"
	}
	if b.IsRemoveHeld(pid) {
		return "PID is on the held list; release it via /itunes/writeback/held/release"
	}
	return ""
}

// pidCaseVariants returns pid as given plus its upper and lower case forms
// (deduplicated): PIDs are stored in either case depending on the writer.
func pidCaseVariants(pid string) []string {
	out := []string{pid}
	for _, v := range []string{strings.ToUpper(pid), strings.ToLower(pid)} {
		dup := false
		for _, o := range out {
			if o == v {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, v)
		}
	}
	return out
}

func dedupeNonEmpty(in []string) []string {
	seen := make(map[string]bool, len(in))
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
