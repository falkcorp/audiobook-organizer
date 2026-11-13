// file: internal/plugins/maintenance/kinds_t6_g1_live_test.go
// version: 1.0.0
// guid: 5e2a9c71-0d3b-4f68-b1a4-7c8e6f2d9a13
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/ai/resultjournal"
	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/authormerge"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// These tests drive the native shells of group 1 the way the executor does and
// assert on what the shell wired, not on what a core does (the cores have their
// own tests): that the writer a core writes through is the one built over the
// run's gated writer (rc.Writer()), that every write reaches it, and that the
// run takes the stand-down, the pool and the cache hooks its kind declares.
// Conversion rule 3 (05-tasks 5T.3.7).

// t6g1TestStandDownKey is the library scan key the live tests configure.
const t6g1TestStandDownKey = tasks.Key("books.test.library-scan")

// t6g1CountWriter is a union of the nine writer groups over a real store: it
// counts every write that reaches it and then makes it, so a test asserts on
// the writer's calls and on the store's state alike.
type t6g1CountWriter struct {
	t6g1HeadWriter

	mu    sync.Mutex
	calls map[string]int
	// failRecord, when set, is what RecordOperationChange returns instead of
	// writing the row.
	failRecord error
}

func newT6G1CountWriter(ops OpsStore) *t6g1CountWriter {
	return &t6g1CountWriter{t6g1HeadWriter: t6g1HeadWriter{ops}, calls: map[string]int{}}
}

func (w *t6g1CountWriter) note(name string) {
	w.mu.Lock()
	w.calls[name]++
	w.mu.Unlock()
}

func (w *t6g1CountWriter) count(name string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls[name]
}

func (w *t6g1CountWriter) total() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, c := range w.calls {
		n += c
	}
	return n
}

func (w *t6g1CountWriter) AIJournalPruner(kind string) (t6g1JournalPruner, error) {
	w.note("AIJournalPruner")
	return w.t6g1HeadWriter.AIJournalPruner(kind)
}

func (w *t6g1CountWriter) CreateAuthor(name string) (*database.Author, error) {
	w.note("CreateAuthor")
	return w.OpsStore.CreateAuthor(name)
}

func (w *t6g1CountWriter) ModifyBookAuthors(bookID string, fn func([]database.BookAuthor) ([]database.BookAuthor, error)) ([]database.BookAuthor, error) {
	w.note("ModifyBookAuthors")
	return w.OpsStore.ModifyBookAuthors(bookID, fn)
}

func (w *t6g1CountWriter) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	w.note("ModifyBook")
	return w.OpsStore.ModifyBook(id, fn)
}

func (w *t6g1CountWriter) DeleteAuthor(id int) error {
	w.note("DeleteAuthor")
	return w.OpsStore.DeleteAuthor(id)
}

func (w *t6g1CountWriter) SetBookAuthors(bookID string, authors []database.BookAuthor) error {
	w.note("SetBookAuthors")
	return w.OpsStore.SetBookAuthors(bookID, authors)
}

func (w *t6g1CountWriter) RecordOperationChange(change *database.OperationChange) error {
	w.note("RecordOperationChange")
	if w.failRecord != nil {
		return w.failRecord
	}
	return w.t6g1HeadWriter.RecordOperationChange(change)
}

func (w *t6g1CountWriter) JournalStep(opID string, row *database.OperationChange) (func() error, error) {
	w.note("JournalStep")
	return w.t6g1HeadWriter.JournalStep(opID, row)
}

func (w *t6g1CountWriter) SetRaw(key string, value []byte) error {
	w.note("SetRaw")
	return w.OpsStore.SetRaw(key, value)
}

func (w *t6g1CountWriter) DeleteRawBatch(keys []string) error {
	w.note("DeleteRawBatch")
	return w.OpsStore.DeleteRawBatch(keys)
}

func (w *t6g1CountWriter) MoveCredits(opID string, m authormerge.CreditMove) (authormerge.MoveResult, error) {
	w.note("MoveCredits")
	return w.t6g1HeadWriter.MoveCredits(opID, m)
}

func (w *t6g1CountWriter) MergeDeleteAuthor(opID string, from, into database.Author) (authormerge.DeleteResult, error) {
	w.note("MergeDeleteAuthor")
	return w.t6g1HeadWriter.MergeDeleteAuthor(opID, from, into)
}

func (w *t6g1CountWriter) RenameAuthorJournaled(opID string, author database.Author, name string) error {
	w.note("RenameAuthorJournaled")
	return w.t6g1HeadWriter.RenameAuthorJournaled(opID, author, name)
}

// t6g1CountActivity counts the activity-filter writes, then makes them.
type t6g1CountActivity struct {
	t6g1HeadActivity

	mu      sync.Mutex
	windows []int64 // the from bound of each window written
	marks   int
	clears  int
}

func (a *t6g1CountActivity) BackfillFilterIndexWindow(ctx context.Context, from, to int64, openEnded bool) (int, error) {
	a.mu.Lock()
	a.windows = append(a.windows, from)
	a.mu.Unlock()
	return a.t6g1HeadActivity.BackfillFilterIndexWindow(ctx, from, to, openEnded)
}

func (a *t6g1CountActivity) MarkFilterIndexBackfillDone() error {
	a.mu.Lock()
	a.marks++
	a.mu.Unlock()
	return a.t6g1HeadActivity.MarkFilterIndexBackfillDone()
}

func (a *t6g1CountActivity) ClearFilterIndexBackfillDone() error {
	a.mu.Lock()
	a.clears++
	a.mu.Unlock()
	return a.t6g1HeadActivity.ClearFilterIndexBackfillDone()
}

// t6g1FakeFetcher is the metadata fetcher the resolve kind runs per book.
type t6g1FakeFetcher struct {
	mu  sync.Mutex
	ids []string
}

func (*t6g1FakeFetcher) HasService() bool { return true }

func (f *t6g1FakeFetcher) FetchByTitle(_ context.Context, bookID string) (*int, error) {
	f.mu.Lock()
	f.ids = append(f.ids, bookID)
	f.mu.Unlock()
	return nil, nil
}

func (f *t6g1FakeFetcher) fetched() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := slices.Clone(f.ids)
	slices.Sort(out)
	return out
}

// t6g1LiveProbe is what a live test hands the shells as deps and reads back.
type t6g1LiveProbe struct {
	mu     sync.Mutex
	given  []tasks.Writer
	caches int
	cw     *t6g1CountWriter
	act    *t6g1CountActivity
}

// noteGiven records a writer a factory was built over.
func (p *t6g1LiveProbe) noteGiven(w tasks.Writer) {
	p.mu.Lock()
	p.given = append(p.given, w)
	p.mu.Unlock()
}

func (p *t6g1LiveProbe) cacheCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.caches
}

// deps wires every hook over ops. ops may be nil for the kinds that do not read
// the library store.
func (p *t6g1LiveProbe) deps(ops OpsStore) t6g1Deps {
	p.cw = newT6G1CountWriter(ops)
	d := t6g1Deps{
		NewWriters: func(w tasks.Writer) t6g1Writers {
			p.noteGiven(w)
			ws := t6g1Writers{
				PruneAIJournal: p.cw, AuthorSplit: p.cw, Merge: p.cw, IDRepair: p.cw,
				PathLink: p.cw, PurgeEmpty: p.cw, AuthorityBuild: p.cw, StripMerge: p.cw,
			}
			if p.act != nil {
				ws.ActivityFilter = p.act
			}
			return ws
		},
		AIJournalRetentionDays: func() int { return 30 },
		StandDownKey:           t6g1TestStandDownKey,
		InvalidateDedupCache:   func() { p.mu.Lock(); p.caches++; p.mu.Unlock() },
		InvalidateAuthorsCache: func() { p.mu.Lock(); p.caches++; p.mu.Unlock() },
	}
	if ops != nil {
		hs := t6g1HeadStore{OpsStore: ops}
		d.Stores = t6g1Stores{
			PurgeEmpty: hs, AuthorityBuild: ops, AuthorSplit: ops, ResolveProduction: ops,
			ConjunctionRepair: hs, DuplicateMerge: hs, IDRepair: hs, PathLink: ops, StripMerge: hs, AutoMatch: ops,
		}
		d.ManualOnly = applygate.ManualOnlyReaders{Files: ops, Series: ops, Authors: ops}
	}
	return d
}

// t6g1LiveCase is one writing kind with a fixture that makes it write.
type t6g1LiveCase struct {
	name   string
	kind   tasks.Kind
	params string
	// preview: the kind has a Preview run, which must write nothing and park
	// nothing.
	preview bool
	// standDown: a live run parks the library scan under t6g1TestStandDownKey.
	standDown bool
	// cpu: the kind's pool is declared tasks.CPU(), so a live run takes CPU
	// slots; every other pool is Fixed and takes none.
	cpu bool
	// setup builds the fixture, the deps over it and the assertions on what a
	// live run did.
	setup func(t *testing.T, p *t6g1LiveProbe) (t6g1Deps, func(t *testing.T, h *t6g1Host, res json.RawMessage))
}

func t6g1LiveCases() []t6g1LiveCase {
	return []t6g1LiveCase{
		{
			name: "prune-ai-journal", kind: pruneAIJournalKind,
			setup: func(t *testing.T, p *t6g1LiveProbe) (t6g1Deps, func(*testing.T, *t6g1Host, json.RawMessage)) {
				s := seedJournal(t)
				return p.deps(s), func(t *testing.T, _ *t6g1Host, res json.RawMessage) {
					require.Equal(t, len(resultjournal.AllKinds()), p.cw.count("AIJournalPruner"), "one journal opened per kind, through the writer")
					var r PruneAIJournalResult
					require.NoError(t, json.Unmarshal(res, &r))
					require.Equal(t, 30, r.RetentionDays)
					require.Equal(t, 1, r.Deleted, "the entry past retention went, the fresh one stayed")
				}
			},
		},
		{
			name: "activity-filter-index-backfill", kind: activityFilterIndexBackfillKind, cpu: true,
			setup: func(t *testing.T, p *t6g1LiveProbe) (t6g1Deps, func(*testing.T, *t6g1Host, json.RawMessage)) {
				s, _ := newAfibStore(t, 40)
				p.act = &t6g1CountActivity{t6g1HeadActivity: t6g1HeadActivity{s}}
				d := p.deps(nil)
				d.ActivityFilter = t6g1HeadActivity{s}
				return d, func(t *testing.T, _ *t6g1Host, res json.RawMessage) {
					require.NotEmpty(t, p.act.windows, "windows were indexed through the writer")
					require.Equal(t, 1, p.act.marks, "the sentinel was marked through the writer")
					require.True(t, s.FilterIndexBackfillDone())
					var r ActivityFilterIndexBackfillResult
					require.NoError(t, json.Unmarshal(res, &r))
					require.True(t, r.SentinelMarked)
				}
			},
		},
		{
			name: "author-split-scan", kind: authorSplitScanKind, preview: true, standDown: true,
			setup: func(t *testing.T, p *t6g1LiveProbe) (t6g1Deps, func(*testing.T, *t6g1Host, json.RawMessage)) {
				st := newSplitRelinkStore(t)
				composite, err := st.CreateAuthor("Bruce Wayne & Dick Grayson")
				require.NoError(t, err)
				ids := mkSplitBooks(t, st, composite.ID, 2)
				return p.deps(st), func(t *testing.T, _ *t6g1Host, _ json.RawMessage) {
					require.Equal(t, 1, p.cw.count("DeleteAuthor"), "the composite row went through the writer")
					require.Positive(t, p.cw.count("CreateAuthor"))
					require.Positive(t, p.cw.count("ModifyBookAuthors")+p.cw.count("ModifyBook"))
					for _, id := range ids {
						require.Zero(t, creditCounts(t, st, id)[composite.ID], "book %s still credits the composite", id)
					}
					require.Positive(t, p.cacheCount(), "a live write drops the dedup cache")
				}
			},
		},
		{
			name: "resolve-production-authors", kind: resolveProductionAuthorsKind, preview: true, standDown: true,
			setup: func(t *testing.T, p *t6g1LiveProbe) (t6g1Deps, func(*testing.T, *t6g1Host, json.RawMessage)) {
				s := newRepairPebble(t)
				prod, err := s.CreateAuthor("Gotham Playhouse Theater")
				require.NoError(t, err)
				repairBook(t, s, "b1", &prod.ID, prod.ID)
				repairBook(t, s, "b2", &prod.ID, prod.ID)
				fetch := &t6g1FakeFetcher{}
				d := p.deps(s)
				d.NewMetadataFetcher = func(w tasks.Writer) t6g1MetadataFetcher {
					p.noteGiven(w)
					return fetch
				}
				return d, func(t *testing.T, _ *t6g1Host, _ json.RawMessage) {
					require.Equal(t, []string{"b1", "b2"}, fetch.fetched(), "every book of the production company was fetched")
					require.Positive(t, p.cacheCount(), "a fetch that ran drops the dedup cache")
				}
			},
		},
		{
			name: "author-duplicate-merge", kind: authorDuplicateMergeKind, preview: true, standDown: true,
			params: `{"names":["Alfred Pennyworth"]}`,
			setup: func(t *testing.T, p *t6g1LiveProbe) (t6g1Deps, func(*testing.T, *t6g1Host, json.RawMessage)) {
				s := newRepairPebble(t)
				keep := repairAuthor(t, s, "Alfred Pennyworth")
				dup := repairAuthor(t, s, "ALFRED PENNYWORTH")
				repairBook(t, s, "b1", &keep.ID, keep.ID)
				repairBook(t, s, "b3", &keep.ID, keep.ID)
				repairBook(t, s, "b2", &dup.ID, dup.ID)
				return p.deps(s), func(t *testing.T, _ *t6g1Host, _ json.RawMessage) {
					require.Equal(t, 1, p.cw.count("MergeDeleteAuthor"))
					require.Positive(t, p.cw.count("MoveCredits"))
					ba, err := s.GetBookAuthors("b2")
					require.NoError(t, err)
					require.Len(t, ba, 1)
					require.Equal(t, keep.ID, ba[0].AuthorID, "the duplicate's book now credits the canonical row")
					require.Positive(t, p.cacheCount())
				}
			},
		},
		{
			name: "author-id-repair", kind: authorIDRepairKind, preview: true, standDown: true, cpu: true,
			setup: func(t *testing.T, p *t6g1LiveProbe) (t6g1Deps, func(*testing.T, *t6g1Host, json.RawMessage)) {
				s := newRepairPebble(t)
				x := repairAuthor(t, s, "Bruce Wayne")
				y := repairAuthor(t, s, "bruce twin")
				w := repairAuthor(t, s, "Dick Grayson")
				require.NoError(t, s.UpdateAuthorName(y.ID, "BRUCE WAYNE"))
				if r, _ := s.GetAuthorByName("bruce wayne"); r == nil || r.ID != y.ID {
					t.Fatalf("fixture: index must resolve to y=%d, got %+v", y.ID, r)
				}
				dangling := 99999
				repairBook(t, s, "xonly1", &x.ID, x.ID)
				repairBook(t, s, "dangl", &dangling, dangling, w.ID)
				return p.deps(s), func(t *testing.T, _ *t6g1Host, res json.RawMessage) {
					require.Equal(t, 1, p.cw.count("MergeDeleteAuthor"), "the duplicate was merge-deleted through the writer")
					require.Positive(t, p.cw.count("MoveCredits"))
					require.Positive(t, p.cw.count("ModifyBook"), "the dangling scalar was repaired through the writer")
					require.Equal(t, w.ID, scalarOf(t, s, "dangl"))
					require.Equal(t, y.ID, scalarOf(t, s, "xonly1"))
					var r authorIDRepairResult
					require.NoError(t, json.Unmarshal(res, &r))
					require.Equal(t, 1, r.DuplicateOutcomes[authorIDMergeMerged])
					require.Positive(t, p.cacheCount())
				}
			},
		},
		{
			name: "author-path-link", kind: authorPathLinkKind, preview: true, standDown: true, cpu: true,
			setup: func(t *testing.T, p *t6g1LiveProbe) (t6g1Deps, func(*testing.T, *t6g1Host, json.RawMessage)) {
				s := pathLinkPebble(t)
				a, err := s.CreateAuthor("Bruce Wayne")
				require.NoError(t, err)
				for i := range 3 {
					pathLinkCreateBook(t, s, fmt.Sprintf("fill%d", i), fmt.Sprintf("/mnt/bigdata/books/audiobook-organizer/Filler/f%d/x.m4b", i), &a.ID)
				}
				pathLinkCreateBook(t, s, "link-me", "/mnt/bigdata/books/audiobook-organizer/Bruce Wayne/Year One/x.m4b", nil)
				return p.deps(s), func(t *testing.T, _ *t6g1Host, res json.RawMessage) {
					var r authorPathLinkResult
					require.NoError(t, json.Unmarshal(res, &r))
					require.Equal(t, 1, r.Outcomes[authorPathLinkLinked], "outcomes %v", r.Outcomes)
					require.Positive(t, p.cw.count("ModifyBook"), "the scalar went through the writer")
					require.Positive(t, p.cw.count("RecordOperationChange"), "the undo row went through the writer")
					joins, err := s.GetBookAuthors("link-me")
					require.NoError(t, err)
					require.Len(t, joins, 1)
					require.Equal(t, a.ID, joins[0].AuthorID)
					require.Positive(t, p.cacheCount())
				}
			},
		},
		{
			name: "purge-empty-authors", kind: purgeEmptyAuthorsKind, preview: true, standDown: true,
			setup: func(t *testing.T, p *t6g1LiveProbe) (t6g1Deps, func(*testing.T, *t6g1Host, json.RawMessage)) {
				s, kept, empties := t6g1PurgeFixture(t)
				return p.deps(s), func(t *testing.T, _ *t6g1Host, _ json.RawMessage) {
					require.Equal(t, len(empties), p.cw.count("DeleteAuthor"), "each empty author was deleted through the writer")
					require.Equal(t, len(empties), p.cw.count("RecordOperationChange"), "each delete was journaled first, through the writer")
					for _, id := range empties {
						a, _ := s.GetAuthorByID(id)
						require.Nil(t, a, "author %d should be gone", id)
					}
					a, err := s.GetAuthorByID(kept)
					require.NoError(t, err)
					require.NotNil(t, a, "the author with a book stays")
				}
			},
		},
		{
			name: "author-strip-merge", kind: authorStripMergeKind, preview: true,
			setup: func(t *testing.T, p *t6g1LiveProbe) (t6g1Deps, func(*testing.T, *t6g1Host, json.RawMessage)) {
				s := newRepairPebble(t)
				real := repairAuthor(t, s, "Bruce Wayne")
				numbered := repairAuthor(t, s, "001-147 Bruce Wayne")
				repairBook(t, s, "bk-merge", &numbered.ID, numbered.ID)
				return p.deps(s), func(t *testing.T, _ *t6g1Host, _ json.RawMessage) {
					require.Positive(t, p.cw.count("MergeDeleteAuthor")+p.cw.count("DeleteAuthor"), "the numbered row was removed through the writer")
					ba, err := s.GetBookAuthors("bk-merge")
					require.NoError(t, err)
					require.Len(t, ba, 1)
					require.Equal(t, real.ID, ba[0].AuthorID, "the book now credits the real author")
				}
			},
		},
		{
			name: "auto-match-transcribed", kind: autoMatchTranscribedKind, preview: true,
			setup: func(t *testing.T, p *t6g1LiveProbe) (t6g1Deps, func(*testing.T, *t6g1Host, json.RawMessage)) {
				amd, ops := t6g1AutoMatchFixture(t, []string{"b1"})
				d := p.deps(ops)
				d.NewTranscriptionRunners = func(w tasks.Writer) TranscriptionRunners {
					p.noteGiven(w)
					return amd
				}
				return d, func(t *testing.T, h *t6g1Host, _ json.RawMessage) {
					require.Equal(t, 1, amd.searchCalled)
					require.Equal(t, 1, amd.applyCalled, "the apply ran through the runner built over the run's writer")
					raw, ok := h.CheckpointJSON("")
					require.True(t, ok, "the run checkpoints the last book it visited")
					require.Contains(t, string(raw), `"last_book_id":"b1"`)
				}
			},
		},
	}
}

// t6g1PurgeFixture is one author with a book and two with nothing.
func t6g1PurgeFixture(t *testing.T) (s *database.PebbleStore, kept int, empties []int) {
	t.Helper()
	s = newRepairPebble(t)
	keep := repairAuthor(t, s, "Bruce Wayne")
	repairBook(t, s, "bk", &keep.ID, keep.ID)
	a := repairAuthor(t, s, "Joker")
	b := repairAuthor(t, s, "Penguin")
	return s, keep.ID, []int{a.ID, b.ID}
}

// t6g1AutoMatchFixture is eligible books (an empty author, a transcribed title)
// and a runner whose search always finds the transcribed title.
func t6g1AutoMatchFixture(t *testing.T, ids []string) (*autoMatchDeps, OpsStore) {
	t.Helper()
	trans, author := "Year One", "Bruce Wayne"
	var books []database.Book
	for _, id := range ids {
		books = append(books, database.Book{ID: id, Title: "Yr One " + id, TranscribedTitle: &trans, TranscribedAuthor: &author})
	}
	amd := &autoMatchDeps{
		searchFn: func(context.Context, string, string, string) (string, string, float64, bool, error) {
			return trans, author, 1.8, true, nil
		},
	}
	hp := newAutoMatchPlugin(books, amd)
	return amd, hp.deps.OpsStore()
}

func TestT6G1Kinds_LiveRunsWriteThroughTheRunsWriter(t *testing.T) {
	for _, tc := range t6g1LiveCases() {
		t.Run(tc.name, func(t *testing.T) {
			probe := &t6g1LiveProbe{}
			deps, check := tc.setup(t, probe)
			def := t6g1ByKind(t, (&Plugin{}).t6g1Kinds(deps, nil), tc.kind)
			gate := &t6g1GateWriter{name: tc.name}

			if tc.preview {
				_, h, err := runT6G1Full(t, def, tasks.ModePreview, gate, tc.params, nil)
				require.NoError(t, err, "preview")
				require.Zero(t, probe.cw.total(), "a preview wrote")
				require.Empty(t, h.standDowns(), "a preview parked the library scan")
				require.Zero(t, probe.cacheCount(), "a preview dropped a cache")
			}

			res, h, err := runT6G1Full(t, def, tasks.ModeLive, gate, tc.params, nil)
			require.NoError(t, err, "live")
			require.NotEmpty(t, probe.given, "no writer factory was called")
			for _, w := range probe.given {
				require.Same(t, gate, w, "the factory must be given the run's writer, rc.Writer()")
			}
			check(t, h, res)

			if tc.standDown {
				require.Equal(t, []tasks.Key{t6g1TestStandDownKey}, h.standDowns(), "the write phase parks the library scan")
			}
			acquires, _ := h.CPUStats()
			if tc.cpu {
				require.Positive(t, acquires, "a tasks.CPU() pool takes CPU slots")
			} else {
				require.Zero(t, acquires, "a Fixed pool takes none")
			}
		})
	}
}

// The conjunction repair's live run is covered by WritesThroughShell (a rename
// through the recorder over the run's writer); the table above covers the other
// writing kinds, so the 12 are all asserted.
func TestT6G1Kinds_LiveCasesCoverEveryWritingKind(t *testing.T) {
	covered := map[tasks.Kind]bool{authorConjunctionRepairKind: true, authorityBuildKind: true}
	for _, tc := range t6g1LiveCases() {
		covered[tc.kind] = true
	}
	for _, d := range (&Plugin{}).t6g1Kinds(t6g1Deps{}, nil) {
		c := tasks.CommonOf(d)
		if c.Effects.IsReadOnly() {
			continue
		}
		require.True(t, covered[d.Kind()], "%s writes and has no live test", d.Kind())
	}
}

// The authority build applies a reviewed plan: a Preview run produces it, a
// Live run carries it out through the writer.
func TestT6G1Kinds_AuthorityBuildAppliesThroughTheRunsWriter(t *testing.T) {
	ps := authorityTestStore(t)
	probe := &t6g1LiveProbe{}
	deps := probe.deps(ps)
	deps.PlanReader = ps
	def := t6g1ByKind(t, (&Plugin{}).t6g1Kinds(deps, nil), authorityBuildKind)
	gate := &t6g1GateWriter{name: "authority"}

	export := writeExport(t, false)
	res, _, err := runT6G1Full(t, def, tasks.ModePreview, gate, fmt.Sprintf(`{"library_export_path":%q}`, export), nil)
	require.NoError(t, err)
	require.Zero(t, probe.cw.total(), "a Preview run wrote")
	var plan authorityPlanResult
	require.NoError(t, json.Unmarshal(res, &plan))
	require.True(t, plan.DryRun)
	require.Positive(t, plan.Writes)
	storePlanRow(t, ps, "plan1", res)

	res, h, err := runT6G1Full(t, def, tasks.ModeLive, gate, `{"plan_op_id":"plan1"}`, nil)
	require.NoError(t, err)
	var applied authorityPlanResult
	require.NoError(t, json.Unmarshal(res, &applied))
	require.NotNil(t, applied.Apply)
	require.Equal(t, plan.Writes, applied.Apply.Written)
	require.Positive(t, probe.cw.count("SetRaw"), "the ref_* keys were written through the writer")
	require.NotEmpty(t, probe.given)
	for _, w := range probe.given {
		require.Same(t, gate, w)
	}
	acquires, _ := h.CPUStats()
	require.Positive(t, acquires, "no concurrency param: the pool is tasks.CPU()")

	// An explicit concurrency is a Fixed pool: no CPU slots.
	_, h2, err := runT6G1Full(t, def, tasks.ModePreview, gate, fmt.Sprintf(`{"library_export_path":%q,"concurrency":2}`, export), nil)
	require.NoError(t, err)
	acquires2, _ := h2.CPUStats()
	require.Zero(t, acquires2, "a concurrency param declares Fixed(n), which takes no CPU slot")
}

// A resumed activity backfill picks up at the window its checkpoint names, from
// the bytes HEAD wrote (activityFilterIndexBackfillParams).
func TestT6G1Kinds_ActivityBackfillRestoresHEADCheckpoint(t *testing.T) {
	s, _ := newAfibStore(t, 40)
	probe := &t6g1LiveProbe{}
	probe.act = &t6g1CountActivity{t6g1HeadActivity: t6g1HeadActivity{s}}
	deps := probe.deps(nil)
	deps.ActivityFilter = t6g1HeadActivity{s}
	def := t6g1ByKind(t, (&Plugin{}).t6g1Kinds(deps, nil), activityFilterIndexBackfillKind)

	w := int64(activityFilterIndexWindow)
	start := time.Now().UnixNano() - 4*w
	cp := fmt.Sprintf(`{"planStartNanos":%d,"planWindows":5,"resumeFrom":3,"indexedBefore":7}`, start)
	res, _, err := runT6G1Full(t, def, tasks.ModeLive, &t6g1GateWriter{}, "", func(f *taskstest.FakeRun) {
		require.NoError(t, f.SetCheckpoint("", json.RawMessage(cp)))
	})
	require.NoError(t, err)

	got := slices.Clone(probe.act.windows)
	slices.Sort(got)
	require.Equal(t, []int64{start + 3*w, start + 4*w}, got, "only the windows from the checkpoint's watermark were indexed")
	var r ActivityFilterIndexBackfillResult
	require.NoError(t, json.Unmarshal(res, &r))
	require.Equal(t, 5, r.Windows, "the plan is the checkpoint's, not a fresh one")
	require.GreaterOrEqual(t, r.RowsIndexed, int64(7), "the earlier attempts' rows carry into the result")
	require.True(t, r.SentinelMarked)
}

// A resumed auto-match skips every book up to and including the checkpoint's
// last book, from the bytes HEAD wrote (autoMatchTranscribedParams).
func TestT6G1Kinds_AutoMatchRestoresHEADCheckpoint(t *testing.T) {
	amd, ops := t6g1AutoMatchFixture(t, []string{"b1", "b2", "b3"})
	var applied []string
	amd.applyFn = func(_ context.Context, id, _, _ string) error {
		applied = append(applied, id)
		return nil
	}
	probe := &t6g1LiveProbe{}
	deps := probe.deps(ops)
	deps.NewTranscriptionRunners = func(w tasks.Writer) TranscriptionRunners { probe.noteGiven(w); return amd }
	def := t6g1ByKind(t, (&Plugin{}).t6g1Kinds(deps, nil), autoMatchTranscribedKind)

	_, h, err := runT6G1Full(t, def, tasks.ModeLive, &t6g1GateWriter{}, "", func(f *taskstest.FakeRun) {
		require.NoError(t, f.SetCheckpoint("", json.RawMessage(`{"last_book_id":"b1","dry_run":false,"min_score":0.75}`)))
	})
	require.NoError(t, err)
	require.Equal(t, []string{"b2", "b3"}, applied, "b1 was finished before the interruption")
	raw, ok := h.CheckpointJSON("")
	require.True(t, ok)
	require.Contains(t, string(raw), `"last_book_id":"b3"`)
}

// A native writing kind refuses the keys the run's mode replaces, whatever
// their case, and accepts an empty or null body: a dropped {"dry_run":true}
// would turn a start meant as a dry run into a live write.
func TestT6G1Kinds_RefuseModeKeysInParams(t *testing.T) {
	defs := (&Plugin{}).t6g1Kinds(t6g1Deps{}, nil)
	dryRun := []string{`{"dry_run":true}`, `{"dryRun":false}`, `{"DRY_RUN":true}`, `{"Dry_Run":false}`}
	apply := []string{`{"apply":false}`, `{"Apply":true}`}
	cases := []struct {
		kind tasks.Kind
		bad  []string
	}{
		{activityFilterIndexBackfillKind, slices.Concat(dryRun, apply)},
		{pruneAIJournalKind, slices.Concat(dryRun, apply)},
		{authorSplitScanKind, dryRun},
		{resolveProductionAuthorsKind, dryRun},
		{authorConjunctionRepairKind, dryRun},
		{authorDuplicateMergeKind, dryRun},
		{authorIDRepairKind, dryRun},
		{authorPathLinkKind, dryRun},
		{purgeEmptyAuthorsKind, slices.Concat(dryRun, apply)},
		{authorStripMergeKind, slices.Concat(dryRun, apply)},
		{authorityBuildKind, dryRun},
		{autoMatchTranscribedKind, dryRun},
	}
	for _, tc := range cases {
		decode, _ := t6g1Runner(t, t6g1ByKind(t, defs, tc.kind))
		for _, body := range tc.bad {
			_, err := decode(json.RawMessage(body))
			require.Error(t, err, "%s accepted %s", tc.kind, body)
			require.Contains(t, err.Error(), "mode comes from the start request", "%s %s", tc.kind, body)
		}
		for _, body := range []string{"", "null", "{}"} {
			var raw json.RawMessage
			if body != "" {
				raw = json.RawMessage(body)
			}
			_, err := decode(raw)
			require.NoError(t, err, "%s refused the body %q", tc.kind, body)
		}
	}
}

// A live run with a cache hook unwired is refused before it writes: a nil hook
// would panic after the writes. A Preview run never calls the hooks.
func TestT6G1Kinds_LiveRefusesAnUnwiredHookBeforeWriting(t *testing.T) {
	cases := []struct {
		kind    tasks.Kind
		drop    func(*t6g1Deps)
		wantMsg string
	}{
		{authorSplitScanKind, func(d *t6g1Deps) { d.InvalidateDedupCache = nil }, "InvalidateDedupCache"},
		{resolveProductionAuthorsKind, func(d *t6g1Deps) { d.InvalidateDedupCache = nil }, "InvalidateDedupCache"},
		{authorConjunctionRepairKind, func(d *t6g1Deps) { d.InvalidateDedupCache = nil }, "InvalidateDedupCache"},
		{authorConjunctionRepairKind, func(d *t6g1Deps) { d.InvalidateAuthorsCache = nil }, "InvalidateAuthorsCache"},
		{authorDuplicateMergeKind, func(d *t6g1Deps) { d.InvalidateDedupCache = nil }, "InvalidateDedupCache"},
		{authorDuplicateMergeKind, func(d *t6g1Deps) { d.InvalidateAuthorsCache = nil }, "InvalidateAuthorsCache"},
		{authorIDRepairKind, func(d *t6g1Deps) { d.InvalidateDedupCache = nil }, "InvalidateDedupCache"},
		{authorIDRepairKind, func(d *t6g1Deps) { d.InvalidateAuthorsCache = nil }, "InvalidateAuthorsCache"},
		{authorPathLinkKind, func(d *t6g1Deps) { d.InvalidateDedupCache = nil }, "InvalidateDedupCache"},
		{authorPathLinkKind, func(d *t6g1Deps) { d.InvalidateAuthorsCache = nil }, "InvalidateAuthorsCache"},
		{pruneAIJournalKind, func(d *t6g1Deps) { d.AIJournalRetentionDays = nil }, "AIJournalRetentionDays"},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind)+"/"+tc.wantMsg, func(t *testing.T) {
			probe := &t6g1LiveProbe{}
			deps := probe.deps(nil)
			tc.drop(&deps)
			def := t6g1ByKind(t, (&Plugin{}).t6g1Kinds(deps, nil), tc.kind)
			err := runT6G1Kind(t, def, tasks.ModeLive, &t6g1GateWriter{}, "")
			require.Error(t, err)
			require.Contains(t, err.Error(), "is not wired")
			require.Contains(t, err.Error(), tc.wantMsg)
			require.Empty(t, probe.given, "the run built a writer before refusing")
		})
	}
}

// A live write phase with no scan stand-down key configured never runs
// unparked: it refuses before the first write.
func TestT6G1Kinds_LiveWithoutAStandDownKeyWritesNothing(t *testing.T) {
	t.Run("purge-empty-authors", func(t *testing.T) {
		s, _, _ := t6g1PurgeFixture(t)
		probe := &t6g1LiveProbe{}
		deps := probe.deps(s)
		deps.StandDownKey = ""
		def := t6g1ByKind(t, (&Plugin{}).t6g1Kinds(deps, nil), purgeEmptyAuthorsKind)
		_, h, err := runT6G1Full(t, def, tasks.ModeLive, &t6g1GateWriter{}, "", nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "no scan stand-down key configured")
		require.Zero(t, probe.cw.total(), "a refused run wrote")
		require.Empty(t, h.standDowns())
	})
	t.Run("author-split-scan", func(t *testing.T) {
		st := newSplitRelinkStore(t)
		composite, err := st.CreateAuthor("Bruce Wayne & Dick Grayson")
		require.NoError(t, err)
		mkSplitBooks(t, st, composite.ID, 1)
		probe := &t6g1LiveProbe{}
		deps := probe.deps(st)
		deps.StandDownKey = ""
		def := t6g1ByKind(t, (&Plugin{}).t6g1Kinds(deps, nil), authorSplitScanKind)
		_, h, err := runT6G1Full(t, def, tasks.ModeLive, &t6g1GateWriter{}, "", nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "no scan stand-down key configured")
		require.Zero(t, probe.cw.total())
		require.Empty(t, h.standDowns())
	})
}

// A journal row refused because the run was fenced aborts the purge before the
// delete it describes, as a refused delete does.
func TestT6G1Kinds_PurgeAbortsWhenTheJournalWriteIsFenced(t *testing.T) {
	s, _, empties := t6g1PurgeFixture(t)
	probe := &t6g1LiveProbe{}
	deps := probe.deps(s)
	probe.cw.failRecord = fmt.Errorf("journal row refused: %w", tasks.ErrFenced)
	def := t6g1ByKind(t, (&Plugin{}).t6g1Kinds(deps, nil), purgeEmptyAuthorsKind)

	err := runT6G1Kind(t, def, tasks.ModeLive, &t6g1GateWriter{}, "")
	require.Error(t, err)
	require.True(t, errors.Is(err, tasks.ErrFenced), "the abort wraps the fence: %v", err)
	require.Contains(t, err.Error(), "run fenced")
	require.Equal(t, 1, probe.cw.count("RecordOperationChange"), "the run stopped at the first refused row")
	require.Zero(t, probe.cw.count("DeleteAuthor"), "nothing was deleted past a fenced journal write")
	for _, id := range empties {
		a, _ := s.GetAuthorByID(id)
		require.NotNil(t, a, "author %d must still exist", id)
	}
}

// The native pool runs the parallelism the kind declares: tasks.CPU() takes
// the executor's CPU slots, Fixed(n) runs n workers and takes none, and the
// pool never runs wider than the declaration.
func TestT6G1Kinds_NativePoolRunsTheDeclaredParallelism(t *testing.T) {
	run := func(t *testing.T, par tasks.Parallelism, items int, fn func(cur *atomic.Int32, peak *atomic.Int32)) (acquires, peak int) {
		t.Helper()
		h := &t6g1Host{FakeRun: taskstest.NewFakeRun(context.Background(), "books.test.pool"), mode: tasks.ModeLive}
		rc := tasks.NewRC(h)
		var cur, pk atomic.Int32
		err := t6g1NativeItems(rc, par)(context.Background(), items, t6g1ItemsOptions{}, func(context.Context, int) error {
			fn(&cur, &pk)
			return nil
		})
		require.NoError(t, err)
		a, _ := h.CPUStats()
		return a, int(pk.Load())
	}
	// hold keeps `want` items in flight at once (or gives up after a timeout, so
	// a pool that runs narrower fails the peak assertion rather than hanging).
	hold := func(want int32) func(cur, peak *atomic.Int32) {
		return func(cur, peak *atomic.Int32) {
			c := cur.Add(1)
			for {
				p := peak.Load()
				if c <= p || peak.CompareAndSwap(p, c) {
					break
				}
			}
			deadline := time.Now().Add(2 * time.Second)
			for peak.Load() < want && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			time.Sleep(2 * time.Millisecond)
			cur.Add(-1)
		}
	}

	t.Run("cpu", func(t *testing.T) {
		acquires, _ := run(t, tasks.CPU(), 16, hold(1))
		require.Positive(t, acquires, "a CPU pool takes CPU slots")
	})
	t.Run("fixed", func(t *testing.T) {
		acquires, peak := run(t, tasks.Fixed(3, "test"), 24, hold(3))
		require.Zero(t, acquires, "a Fixed pool takes no CPU slot")
		require.Equal(t, 3, peak, "a Fixed(3) pool runs three workers, no more and no fewer")
	})
	t.Run("fixed one", func(t *testing.T) {
		acquires, peak := run(t, tasks.Fixed(1, "test"), 6, hold(1))
		require.Zero(t, acquires)
		require.Equal(t, 1, peak)
	})
	t.Run("sequential", func(t *testing.T) {
		acquires, peak := run(t, tasks.Sequential("test"), 6, hold(1))
		require.Zero(t, acquires)
		require.Equal(t, 1, peak)
	})
}
