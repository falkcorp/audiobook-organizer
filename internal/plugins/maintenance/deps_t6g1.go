// file: internal/plugins/maintenance/deps_t6g1.go
// version: 0.1.0
// guid: 6d3a9e41-2b7c-4f58-9a06-e1c47d8b5f32
// last-edited: 2026-11-28

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/ai/resultjournal"
	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/authormerge"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/metafetchgate"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The books module's own t6g1Deps (F5-18 A6d), dark: nothing calls the builder
// until the flip hands its result to t6g1Kinds. It follows the group-2..4
// builders (deps_t6g2.go..deps_t6g4.go): a store whose method set is the group's
// reads, a writer set whose every method is ONE writer.Gate.Do over the run's
// tasks.Writer stamped with the method's whole row, and the plain dependencies
// the shells need. It does not build on t6g1HeadStore/t6g1HeadWriter, which the
// v2 definitions' deletion takes with them.

// AuthorityRunReader is what the authority-build apply reads of the tasks run
// store to find the plan it carries out; tasks.RunStore satisfies it.
type AuthorityRunReader = authorityRunReader

// tasks.RunStore, the store core hands the host, must satisfy AuthorityRunReader:
// a method dropped from either is caught here at compile time, not at the wiring.
var _ AuthorityRunReader = tasks.RunStore(nil)

// G1Inputs is what newT6G1Deps builds group 1's deps from, beside the ops store
// and the stand-down key every group shares.
type G1Inputs struct {
	// Activity is the Pebble activity store's filter-index surface. Nil where no
	// Pebble activity store is wired (the SQLite tier): the backfill kind then
	// reports "no Pebble activity store is wired" as at HEAD.
	Activity database.ActivityFilterIndexBackfiller
	// PlanRuns is the tasks run store an authority-build apply finds its plan in.
	PlanRuns AuthorityRunReader
	// FetchStore is the store the metadata fetch service reads and writes. Its
	// writes never reach the service directly: each run wraps it in
	// metafetchgate.Store over the run's writer before NewFetchService sees it.
	FetchStore metafetch.Store
	// NewFetchService builds the fetch service over the gated store it is handed,
	// configured as the process's own (sources, activity). It must keep no other
	// route to the store.
	NewFetchService func(gated metafetch.Store) *metafetch.Service
	// NewTranscriptionRunners builds the transcription search and apply over the
	// run's fetch service.
	NewTranscriptionRunners func(svc *metafetch.Service) TranscriptionRunners
	// ManualOnly are the owner-manual check's readers.
	ManualOnly applygate.ManualOnlyReaders
	// AIJournalRetentionDays is the configured ai_journal_retention_days.
	AIJournalRetentionDays func() int
	// InvalidateAuthorsCache and InvalidateDedupCache drop the cached author list
	// and the duplicate-author cache after a write.
	InvalidateAuthorsCache func()
	InvalidateDedupCache   func()
}

// errG1NoActivity is the refusal of a write on a store with no activity store.
var errG1NoActivity = errors.New("no Pebble activity store is wired")

// t6g1WriterTypes is every writer group of t6g1Writers, for the row view.
func t6g1WriterTypes() []reflect.Type {
	var types []reflect.Type
	for f := range reflect.TypeFor[t6g1Writers]().Fields() {
		types = append(types, f.Type)
	}
	return types
}

// newT6G1Deps is the books module's t6g1Deps over the ops store src.
//
// It returns an error when the shared row table has no row for the fetch service
// (metafetchgate.Rows), which no input can fix.
func newT6G1Deps(src OpsStore, in G1Inputs, standDownKey tasks.Key) (t6g1Deps, error) {
	fetchRows, err := metafetchgate.Rows()
	if err != nil {
		return t6g1Deps{}, fmt.Errorf("group 1 fetch rows: %w", err)
	}
	rows := groupRows(map[string]string{"SetRaw": "SetRaw/authority_lists"}, t6g1WriterTypes()...)
	store := booksT6G1Store{src: src}
	d := t6g1Deps{
		Stores: t6g1Stores{
			TitleFragment: store, WhitespaceCollision: store, PurgeEmpty: store,
			AuthorityBuild: store, AuthorSplit: store, ResolveProduction: store,
			ConjunctionRepair: store, DuplicateMerge: store, IDRepair: store,
			PathLink: store, StripMerge: store, AutoMatch: store,
		},
		NewWriters: func(w tasks.Writer) t6g1Writers {
			bw := &booksT6G1Writer{src: src, activity: in.Activity, gate: newBooksGate(w, rows)}
			return t6g1Writers{
				PruneAIJournal: bw, ActivityFilter: bw, AuthorSplit: bw, Merge: bw, IDRepair: bw,
				PathLink: bw, PurgeEmpty: bw, AuthorityBuild: bw, StripMerge: bw,
			}
		},
		AIJournalRetentionDays: in.AIJournalRetentionDays,
		PlanRuns:               in.PlanRuns,
		StandDownKey:           standDownKey,
		InvalidateDedupCache:   in.InvalidateDedupCache,
		InvalidateAuthorsCache: in.InvalidateAuthorsCache,
		ManualOnly:             in.ManualOnly,
	}
	if in.Activity != nil {
		d.ActivityFilter = booksT6G1Activity{s: in.Activity}
	}
	if in.FetchStore != nil && in.NewFetchService != nil {
		fetchOf := func(w tasks.Writer) *metafetch.Service {
			gate := newBooksGate(w, fetchRows).g
			return in.NewFetchService(metafetchgate.Wrap(runless(), in.FetchStore, gate))
		}
		d.NewMetadataFetcher = func(w tasks.Writer) t6g1MetadataFetcher {
			return booksT6G1Fetcher{svc: fetchOf(w)}
		}
		if in.NewTranscriptionRunners != nil {
			d.NewTranscriptionRunners = func(w tasks.Writer) TranscriptionRunners {
				return in.NewTranscriptionRunners(fetchOf(w))
			}
		}
	}
	return d, nil
}

// booksT6G1Fetcher is what the server's FetchMetadataByTitle does over a
// service: the title-only fetch-and-apply the production-author resolvers loop
// over, handing back only the author id the book ends with.
type booksT6G1Fetcher struct{ svc *metafetch.Service }

func (booksT6G1Fetcher) HasService() bool { return true }

func (f booksT6G1Fetcher) FetchByTitle(ctx context.Context, bookID string) (*int, error) {
	// Unattended: a retired book comes back as metafetch.ErrBookRetired for the
	// caller to count as a skip.
	resp, err := f.svc.FetchMetadataForBookByTitle(ctx, bookID, metafetch.Unattended)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Book == nil {
		return nil, nil
	}
	return resp.Book.AuthorID, nil
}

// booksT6G1Activity is the activity store's reads (activityFilterStore).
type booksT6G1Activity struct {
	s database.ActivityFilterIndexBackfiller
}

func (a booksT6G1Activity) FilterIndexBackfillDone() bool { return a.s.FilterIndexBackfillDone() }

func (a booksT6G1Activity) FilterIndexEarliestNanos(ctx context.Context) (int64, bool, error) {
	return a.s.FilterIndexEarliestNanos(ctx)
}

// ReconcileSupport is nil when the store has ReconcileFilterIndexCoverage.
func (a booksT6G1Activity) ReconcileSupport() error {
	if _, ok := a.s.(activityFilterIndexReconciler); !ok {
		return fmt.Errorf("activity-filter-index-backfill: cannot reconcile: the activity store is %T, which has no "+
			"ReconcileFilterIndexCoverage", a.s)
	}
	return nil
}

// booksT6G1Store is the group's reads: the union of the methods of every
// store interface of t6g1Stores, none of which writes.
type booksT6G1Store struct{ src OpsStore }

func (s booksT6G1Store) AuthorFileRefCounts() (map[int]int, error) {
	return database.AuthorFileRefCounts(s.src)
}

func (s booksT6G1Store) AuthorRefBucketCounts() (map[int]database.AuthorRefBuckets, error) {
	return database.AuthorRefBucketCounts(s.src)
}

func (s booksT6G1Store) AuthorRefCounts() (map[int]int, error) {
	return database.AuthorRefCounts(s.src)
}

func (s booksT6G1Store) CanMergeDelete() bool {
	_, ok := database.AsCapability[database.AuthorMergeDeleter](s.src)
	return ok
}

// CanModifyBookCredits answers author-split-scan's preflight probe. It is the
// capability's presence only: the store hands out no ModifyBookCredits of its own,
// so the write stays the writer's (gated) one.
func (s booksT6G1Store) CanModifyBookCredits() bool {
	_, ok := database.AsCapability[database.BookCreditsModifier](s.src)
	return ok
}

func (s booksT6G1Store) CountPrefix(prefix string) (int64, error) {
	return s.src.CountPrefix(prefix)
}

func (s booksT6G1Store) GetAllAuthorBookCounts() (map[int]int, error) {
	return s.src.GetAllAuthorBookCounts()
}

func (s booksT6G1Store) GetAllAuthors() ([]database.Author, error) {
	return s.src.GetAllAuthors()
}

func (s booksT6G1Store) GetAllBooksCore(limit int, offset int) ([]database.BookCore, error) {
	return s.src.GetAllBooksCore(limit, offset)
}

func (s booksT6G1Store) GetAllBooksCoreComplete(limit int, offset int) ([]database.BookCore, error) {
	return s.src.GetAllBooksCoreComplete(limit, offset)
}

func (s booksT6G1Store) GetAllSeries() ([]database.Series, error) {
	return s.src.GetAllSeries()
}

func (s booksT6G1Store) GetAuthorByID(id int) (*database.Author, error) {
	return s.src.GetAuthorByID(id)
}

func (s booksT6G1Store) GetAuthorByName(name string) (*database.Author, error) {
	return s.src.GetAuthorByName(name)
}

func (s booksT6G1Store) GetBookAuthors(bookID string) ([]database.BookAuthor, error) {
	return s.src.GetBookAuthors(bookID)
}

func (s booksT6G1Store) GetBookByID(id string) (*database.Book, error) {
	return s.src.GetBookByID(id)
}

func (s booksT6G1Store) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return s.src.GetBookFiles(bookID)
}

func (s booksT6G1Store) GetBookIDsCreditingAuthorDurable(authorID int) ([]string, error) {
	return s.src.GetBookIDsCreditingAuthorDurable(authorID)
}

func (s booksT6G1Store) GetBooksByAuthorIDForRelinkCore(authorID int) ([]database.BookCore, error) {
	return s.src.GetBooksByAuthorIDForRelinkCore(authorID)
}

func (s booksT6G1Store) GetBooksByAuthorIDWithRoleCore(authorID int) ([]database.BookCore, error) {
	return s.src.GetBooksByAuthorIDWithRoleCore(authorID)
}

func (s booksT6G1Store) GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error) {
	return s.src.GetMetadataFieldStates(bookID)
}

func (s booksT6G1Store) GetRaw(key string) ([]byte, error) {
	return s.src.GetRaw(key)
}

func (s booksT6G1Store) GetUserPreference(key string) (*database.UserPreference, error) {
	return s.src.GetUserPreference(key)
}

func (s booksT6G1Store) ListBookIDs() ([]string, error) {
	return s.src.ListBookIDs()
}

func (s booksT6G1Store) ScanPrefixPage(prefix string, after string, limit int) ([]database.KVPair, string, error) {
	return s.src.ScanPrefixPage(prefix, after, limit)
}

func (s booksT6G1Store) VerifyAuthorUnlinked(authorID int) error {
	return database.VerifyAuthorUnlinked(s.src, authorID)
}

// gatedValue is one gated store call of a method with a value and an error.
func gatedValue[T any](b booksGate, method string, subject entity.Ref, call func() (T, error)) (out T, err error) {
	err = b.do(runless(), method, subject, func(context.Context) error {
		var e error
		out, e = call()
		return e
	})
	return out, err
}

// booksT6G1Writer is one run's gated writes: the union of the methods of every
// writer interface of t6g1Writers. Each is ONE gated call stamped with the
// method's whole row (the shared table, through groupRows).
type booksT6G1Writer struct {
	src      OpsStore
	activity database.ActivityFilterIndexBackfiller
	gate     booksGate
}

// activityStore is the activity store the filter-index writes go to.
func (w *booksT6G1Writer) activityStore() (database.ActivityFilterIndexBackfiller, error) {
	if w.activity == nil {
		return nil, errG1NoActivity
	}
	return w.activity, nil
}

func (w *booksT6G1Writer) ClearFilterIndexBackfillDone() error {
	return w.gate.do(runless(), "ClearFilterIndexBackfillDone", kindRef(ops.ResActivity), func(context.Context) error {
		a, err := w.activityStore()
		if err != nil {
			return err
		}
		return a.ClearFilterIndexBackfillDone()
	})
}

func (w *booksT6G1Writer) MarkFilterIndexBackfillDone() error {
	return w.gate.do(runless(), "MarkFilterIndexBackfillDone", kindRef(ops.ResActivity), func(context.Context) error {
		a, err := w.activityStore()
		if err != nil {
			return err
		}
		return a.MarkFilterIndexBackfillDone()
	})
}

func (w *booksT6G1Writer) BackfillFilterIndexWindow(ctx context.Context, from, to int64, openEnded bool) (int, error) {
	return gatedValue(w.gate, "BackfillFilterIndexWindow", kindRef(ops.ResActivity), func() (int, error) {
		a, err := w.activityStore()
		if err != nil {
			return 0, err
		}
		return a.BackfillFilterIndexWindow(ctx, from, to, openEnded)
	})
}

func (w *booksT6G1Writer) ReconcileFilterIndexCoverage(ctx context.Context, progress database.FilterIndexProgress) (database.FilterIndexReconcileResult, error) {
	return gatedValue(w.gate, "ReconcileFilterIndexCoverage", kindRef(ops.ResActivity), func() (database.FilterIndexReconcileResult, error) {
		a, err := w.activityStore()
		if err != nil {
			return database.FilterIndexReconcileResult{}, err
		}
		rec, ok := a.(activityFilterIndexReconciler)
		if !ok {
			return database.FilterIndexReconcileResult{}, booksT6G1Activity{s: a}.ReconcileSupport()
		}
		return rec.ReconcileFilterIndexCoverage(ctx, progress)
	})
}

// AIJournalPruner stamps the open, which fences a preview and a stopped run, and
// hands back the pruner with its deletions stamped too: the pruner outlives the
// call, so a bare one would delete after the run was stopped (gatedPruner).
func (w *booksT6G1Writer) AIJournalPruner(kind string) (t6g1JournalPruner, error) {
	return gatedValue(w.gate, "AIJournalPruner", kindRef(ops.ResAIJournal), func() (t6g1JournalPruner, error) {
		j, err := resultjournal.New(w.src, kind)
		if err != nil {
			return nil, err
		}
		return gatedPruner{p: j, gate: w.gate}, nil
	})
}

// gatedPruner is a journal pruner whose Prune is stamped under the row of the
// call that opened it (the shared table has no row for the journal's own
// methods), which is ops.ResAIJournal.
type gatedPruner struct {
	p    t6g1JournalPruner
	gate booksGate
}

func (g gatedPruner) Prune(ctx context.Context, maxAge time.Duration) (int, error) {
	return gatedValue(g.gate, "AIJournalPruner", kindRef(ops.ResAIJournal), func() (int, error) {
		return g.p.Prune(ctx, maxAge)
	})
}

func (w *booksT6G1Writer) CreateAuthor(name string) (*database.Author, error) {
	return gatedValue(w.gate, "CreateAuthor", kindRef(ops.ResAuthors), func() (*database.Author, error) {
		return w.src.CreateAuthor(name)
	})
}

func (w *booksT6G1Writer) DeleteAuthor(id int) error {
	return w.gate.do(runless(), "DeleteAuthor", kindRef(ops.ResAuthors), func(context.Context) error {
		return w.src.DeleteAuthor(id)
	})
}

func (w *booksT6G1Writer) DeleteRawBatch(keys []string) error {
	return w.gate.do(runless(), "DeleteRawBatch", kindRef(ops.ResAuthorityLists), func(context.Context) error {
		return w.src.DeleteRawBatch(keys)
	})
}

func (w *booksT6G1Writer) SetRaw(key string, value []byte) error {
	return w.gate.do(runless(), "SetRaw", kindRef(ops.ResAuthorityLists), func(context.Context) error {
		return w.src.SetRaw(key, value)
	})
}

// JournalStep writes one undo-ledger row BEFORE the write it describes and
// returns the function that voids it. Both the row and the void are writes, so
// both are stamped: a void that ran after the run was stopped would write past
// the fence.
func (w *booksT6G1Writer) JournalStep(opID string, row *database.OperationChange) (func() error, error) {
	return gatedValue(w.gate, "JournalStep", kindRef(ops.ResOperations), func() (func() error, error) {
		void, err := authormerge.Step(w.src, opID, row)
		if err != nil || void == nil {
			return void, err
		}
		return func() error {
			return w.gate.do(runless(), "JournalStep", kindRef(ops.ResOperations), func(context.Context) error { return void() })
		}, nil
	})
}

func (w *booksT6G1Writer) MergeDeleteAuthor(opID string, from, into database.Author) (authormerge.DeleteResult, error) {
	return gatedValue(w.gate, "MergeDeleteAuthor", kindRef(ops.ResAuthors), func() (authormerge.DeleteResult, error) {
		return authormerge.MergeDelete(w.src, opID, from, into)
	})
}

func (w *booksT6G1Writer) ModifyBookAuthors(bookID string, fn func([]database.BookAuthor) ([]database.BookAuthor, error)) ([]database.BookAuthor, error) {
	return gatedValue(w.gate, "ModifyBookAuthors", kindRef(ops.ResAuthors), func() ([]database.BookAuthor, error) {
		return w.src.ModifyBookAuthors(bookID, fn)
	})
}

// ModifyBookCredits writes a book's row and its credit lists in one commit, or
// refuses with database.ErrBookCreditsUnsupported: a store that cannot make them
// one write gets no fallback to two.
func (w *booksT6G1Writer) ModifyBookCredits(bookID string, fn func(book *database.Book, credits *database.BookCreditsEdit) error) (b *database.Book, edit database.BookCreditsEdit, err error) {
	err = w.gate.do(runless(), "ModifyBookCredits", bookRef(bookID), func(context.Context) error {
		m, ok := database.AsCapability[database.BookCreditsModifier](w.src)
		if !ok {
			return fmt.Errorf("%w: store is %T", database.ErrBookCreditsUnsupported, w.src)
		}
		var e error
		b, edit, e = m.ModifyBookCredits(bookID, fn)
		return e
	})
	return b, edit, err
}

func (w *booksT6G1Writer) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	return gatedValue(w.gate, "ModifyBook", bookRef(id), func() (*database.Book, error) {
		return w.src.ModifyBook(id, fn)
	})
}

func (w *booksT6G1Writer) MoveCredits(opID string, m authormerge.CreditMove) (authormerge.MoveResult, error) {
	return gatedValue(w.gate, "MoveCredits", bookRef(m.BookID), func() (authormerge.MoveResult, error) {
		return authormerge.MoveCredits(w.src, opID, m)
	})
}

// RecordOperationChange journals one change on the undo ledger, through the
// classification check.
func (w *booksT6G1Writer) RecordOperationChange(change *database.OperationChange) error {
	return w.gate.do(runless(), "RecordOperationChange", kindRef(ops.ResOperations), func(context.Context) error {
		return database.RecordOperationChange(w.src, change)
	})
}

func (w *booksT6G1Writer) RenameAuthorJournaled(opID string, author database.Author, name string) error {
	return w.gate.do(runless(), "RenameAuthorJournaled", kindRef(ops.ResAuthors), func(context.Context) error {
		return renameAuthorJournaled(w.src, opID, author, name)
	})
}
