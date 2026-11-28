// file: internal/plugins/maintenance/deps_t6g1_test.go
// version: 0.1.0
// guid: b4e81d27-5c93-4a06-8f1e-92d7a03c6e58
// last-edited: 2026-11-28

package maintenance

import (
	"context"
	"encoding/json"
	"iter"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/authormerge"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/writertest"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// g1TestKey is the library scan key the group-1 tests give the builder.
const g1TestKey = tasks.Key("books.test.library-scan")

// g1TestInputs are the group's inputs over a store: the service inputs the
// fetch kinds need, and a fetch source that answers fixedly.
func g1TestInputs(s *database.PebbleStore, results []metadata.BookMetadata) G1Inputs {
	return G1Inputs{
		FetchStore: s,
		NewFetchService: func(gated metafetch.Store) *metafetch.Service {
			svc := metafetch.NewService(gated)
			svc.SetOverrideSources([]metadata.MetadataSource{&f518bSource{results: results}})
			return svc
		},
		NewTranscriptionRunners: func(svc *metafetch.Service) TranscriptionRunners {
			return f518bRunners{svc: svc, cand: metafetch.MetadataCandidate{
				Title: "Year One", Author: "Bruce Wayne", Series: "The Long Halloween", Publisher: "Wayne Enterprises", Source: "f518b-fake",
			}}
		},
		ManualOnly:             applygate.ManualOnlyReaders{Files: s, Series: s, Authors: s, Tags: s},
		AIJournalRetentionDays: func() int { return 30 },
		InvalidateAuthorsCache: func() {},
		InvalidateDedupCache:   func() {},
	}
}

// g1NoRuns is a run store with no run in it, shaped like the handle the books
// module holds: it lists and has no point read.
type g1NoRuns struct{}

func (g1NoRuns) List(context.Context, tasks.RunQuery) iter.Seq2[tasks.RunRecord, error] {
	return func(func(tasks.RunRecord, error) bool) {}
}

// g1WriterCalls is one call of every method of every writer group, by method
// name. The two that hand back something that writes later (a journal pruner, a
// void) call it too.
func g1WriterCalls(bookID string) map[string]func(w *booksT6G1Writer) error {
	ctx := context.Background()
	return map[string]func(w *booksT6G1Writer) error{
		"ClearFilterIndexBackfillDone": func(w *booksT6G1Writer) error { return w.ClearFilterIndexBackfillDone() },
		"MarkFilterIndexBackfillDone":  func(w *booksT6G1Writer) error { return w.MarkFilterIndexBackfillDone() },
		"BackfillFilterIndexWindow": func(w *booksT6G1Writer) error {
			_, err := w.BackfillFilterIndexWindow(ctx, 0, 1, false)
			return err
		},
		"ReconcileFilterIndexCoverage": func(w *booksT6G1Writer) error {
			_, err := w.ReconcileFilterIndexCoverage(ctx, nil)
			return err
		},
		"AIJournalPruner": func(w *booksT6G1Writer) error {
			p, err := w.AIJournalPruner("whisper")
			if err != nil {
				return err
			}
			_, err = p.Prune(ctx, time.Hour)
			return err
		},
		"CreateAuthor": func(w *booksT6G1Writer) error {
			_, err := w.CreateAuthor("Selina Kyle")
			return err
		},
		"DeleteAuthor":   func(w *booksT6G1Writer) error { return w.DeleteAuthor(424242) },
		"DeleteRawBatch": func(w *booksT6G1Writer) error { return w.DeleteRawBatch([]string{"ref_src:gotham"}) },
		"SetRaw":         func(w *booksT6G1Writer) error { return w.SetRaw("ref_src:gotham", []byte("{}")) },
		"MergeDeleteAuthor": func(w *booksT6G1Writer) error {
			_, err := w.MergeDeleteAuthor("op", database.Author{ID: 424242}, database.Author{ID: 424243})
			return err
		},
		"ModifyBookAuthors": func(w *booksT6G1Writer) error {
			_, err := w.ModifyBookAuthors(bookID, func(a []database.BookAuthor) ([]database.BookAuthor, error) { return a, nil })
			return err
		},
		"ModifyBookCredits": func(w *booksT6G1Writer) error {
			_, _, err := w.ModifyBookCredits(bookID, func(*database.Book, *database.BookCreditsEdit) error { return nil })
			return err
		},
		"ModifyBook": func(w *booksT6G1Writer) error {
			_, err := w.ModifyBook(bookID, func(*database.Book) error { return nil })
			return err
		},
		"MoveCredits": func(w *booksT6G1Writer) error {
			_, err := w.MoveCredits("op", authormerge.CreditMove{BookID: bookID, FromID: 424242, IntoID: 424243})
			return err
		},
		"RecordOperationChange": func(w *booksT6G1Writer) error {
			return w.RecordOperationChange(&database.OperationChange{OperationID: "op", BookID: bookID, ChangeType: "metadata_update"})
		},
		"RenameAuthorJournaled": func(w *booksT6G1Writer) error {
			return w.RenameAuthorJournaled("op", database.Author{ID: 424242, Name: "Harvey Dent"}, "Two-Face")
		},
		"JournalStep": func(w *booksT6G1Writer) error {
			void, err := w.JournalStep("op", &database.OperationChange{OperationID: "op", BookID: bookID, ChangeType: "metadata_update"})
			if err != nil {
				return err
			}
			return void()
		},
	}
}

// g1StampsPerCall is how many gated calls a method makes when g1WriterCalls
// runs it: the two with a second write behind what they return make two.
var g1StampsPerCall = map[string]int{"AIJournalPruner": 2, "JournalStep": 2}

// g1Writer is the one writer every group of t6g1Writers is, over w.
func g1Writer(t *testing.T, deps t6g1Deps, w tasks.Writer) *booksT6G1Writer {
	t.Helper()
	ws := deps.NewWriters(w)
	bw, ok := ws.Merge.(*booksT6G1Writer)
	require.True(t, ok)
	// Every group is the same writer: a group that was another value would make
	// the row coverage below prove nothing for it.
	for f := range reflect.TypeFor[t6g1Writers]().Fields() {
		require.Same(t, bw, reflect.ValueOf(ws).FieldByName(f.Name).Interface(), f.Name)
	}
	return bw
}

func g1Deps(t *testing.T, s *database.PebbleStore) t6g1Deps {
	t.Helper()
	d, err := newT6G1Deps(s, g1NoRuns{}, g1TestInputs(s, nil), g1TestKey)
	require.NoError(t, err)
	return d
}

func TestBooksT6G1Writer_CoversEveryMethodOfTheInterfaces(t *testing.T) {
	want := t6WriterMethods(t6g1WriterTypes()...)
	calls := g1WriterCalls("x")
	got := make([]string, 0, len(calls))
	for m := range calls {
		got = append(got, m)
	}
	slices.Sort(got)
	require.Equal(t, want, got, "g1WriterCalls must call exactly the methods of the writer groups")

	view := groupRows(map[string]string{"SetRaw": "SetRaw/authority_lists"}, t6g1WriterTypes()...)
	for _, m := range want {
		_, ok := view[m]
		require.True(t, ok, "%s has no row in the shared method table: the gate would refuse it", m)
	}
}

// Each method makes the gated calls named for it, each stamped with the shared
// table's whole row (SetRaw is this group's authority-list write).
func TestBooksT6G1Writer_EveryCallIsStampedWithItsWholeRow(t *testing.T) {
	for method := range g1WriterCalls("x") {
		t.Run(method, func(t *testing.T) {
			s, id := g2Fixture(t)
			rec := &booksGateRecorder{}
			deps := g1Deps(t, s)
			_ = g1WriterCalls(id)[method](g1Writer(t, deps, rec))
			stamps := rec.stamps()
			n := 1
			if m, ok := g1StampsPerCall[method]; ok {
				n = m
			}
			require.Len(t, stamps, n, "%s: gated calls", method)
			key := method
			if method == "SetRaw" {
				key = "SetRaw/authority_lists"
			}
			var want []entity.Ref
			for _, r := range t6WriterResources[key] {
				want = append(want, tasks.Res("books", string(r)))
			}
			for _, st := range stamps {
				require.Equal(t, "books."+method, st.Name)
				require.ElementsMatch(t, want, st.Resources, "the stamp is the method's whole row, not a retyped list")
			}
		})
	}
}

// A refused write (a preview, a fenced run, outside the effects) leaves the
// store untouched.
func TestBooksT6G1Writer_RefusalDoesNothing(t *testing.T) {
	for _, refusal := range []error{tasks.ErrPreview, tasks.ErrFenced, tasks.ErrOutsideEffects} {
		for method := range g1WriterCalls("x") {
			t.Run(method+"/"+refusal.Error(), func(t *testing.T) {
				s, id := g2Fixture(t)
				before := t6g5Snapshot(t, s)
				w := g1Writer(t, g1Deps(t, s), &booksGateRecorder{refuse: refusal})
				require.ErrorIs(t, g1WriterCalls(id)[method](w), refusal)
				require.Equal(t, before, t6g5Snapshot(t, s), "the store changed after a refusal")
			})
		}
	}
}

// core hands a ReadOnly kind no tasks.Writer: every write refuses instead of
// dereferencing nil.
func TestBooksT6G1Writer_NoWriterRefusesEveryWrite(t *testing.T) {
	s, id := g2Fixture(t)
	w := g1Writer(t, g1Deps(t, s), nil)
	for method, call := range g1WriterCalls(id) {
		require.ErrorIs(t, call(w), tasks.ErrOutsideEffects, method)
	}
}

// The pruner and the void outlive the call that handed them out: a run that is
// stopped after it must not delete through them.
func TestBooksT6G1Writer_WhatAWriteHandsBackIsStillGated(t *testing.T) {
	s, id := g2Fixture(t)
	rec := &booksGateRecorder{}
	w := g1Writer(t, g1Deps(t, s), rec)

	p, err := w.AIJournalPruner("whisper")
	require.NoError(t, err)
	void, err := w.JournalStep("op", &database.OperationChange{OperationID: "op", BookID: id, ChangeType: "metadata_update"})
	require.NoError(t, err)
	require.Len(t, rec.stamps(), 2, "the opens")

	rec.refuse = tasks.ErrFenced
	_, err = p.Prune(context.Background(), time.Hour)
	require.ErrorIs(t, err, tasks.ErrFenced)
	require.ErrorIs(t, void(), tasks.ErrFenced)
	require.Len(t, rec.stamps(), 4, "the pruner's delete and the void's write are each a gated call")
}

// Every kind of a writer group has every method of the group admitted by its
// own declared Effects, Live: the builder's stamps are no wider than what the
// kind declares.
func TestBooksT6G1Writer_EveryKindsGroupIsAdmittedByItsOwnEffects(t *testing.T) {
	s, id := g2Fixture(t)
	deps := g1Deps(t, s)
	calls := g1WriterCalls(id)
	fields := t6g1KindWriterFields(t)
	require.NotEmpty(t, fields)
	for _, def := range (&Plugin{}).t6g1Kinds(deps, nil) {
		field, ok := fields[def.Kind()]
		if !ok {
			continue
		}
		ft, _ := reflect.TypeFor[t6g1Writers]().FieldByName(field)
		t.Run(string(def.Kind()), func(t *testing.T) {
			enforcing := writertest.New(def, tasks.ModeLive)
			w := g1Writer(t, deps, enforcing)
			methods := t6WriterMethods(ft.Type)
			for _, m := range methods {
				_ = calls[m](w)
			}
			require.NoError(t, enforcing.RequireClean(), "a stamp is wider than the kind's declared Effects")
			require.Len(t, enforcing.Writes(), g1StampsOf(methods), "every method is stamped")
		})
	}
}

// g1StampsOf is how many gated calls g1WriterCalls makes for methods.
func g1StampsOf(methods []string) int {
	n := 0
	for _, m := range methods {
		if k, ok := g1StampsPerCall[m]; ok {
			n += k
			continue
		}
		n++
	}
	return n
}

func TestBooksT6G1Deps_OptionalInputsStayUnset(t *testing.T) {
	s, _ := g2Fixture(t)
	in := g1TestInputs(s, nil)

	d, err := newT6G1Deps(s, g1NoRuns{}, in, g1TestKey)
	require.NoError(t, err)
	require.Nil(t, d.ActivityFilter, "no activity store, no reads of one")
	require.NotNil(t, d.NewMetadataFetcher)
	require.NotNil(t, d.NewTranscriptionRunners)
	require.Equal(t, g1TestKey, d.StandDownKey)
	require.Equal(t, 30, d.AIJournalRetentionDays())

	// A fetch store with no factory (or the reverse) wires neither fetch hook, as a
	// server with no fetch service does: the kinds then refuse by name at the run.
	for name, mutate := range map[string]func(*G1Inputs){
		"no factory": func(i *G1Inputs) { i.NewFetchService = nil },
		"no store":   func(i *G1Inputs) { i.FetchStore = nil },
	} {
		t.Run(name, func(t *testing.T) {
			in := g1TestInputs(s, nil)
			mutate(&in)
			d, err := newT6G1Deps(s, g1NoRuns{}, in, g1TestKey)
			require.NoError(t, err)
			require.Nil(t, d.NewMetadataFetcher)
			require.Nil(t, d.NewTranscriptionRunners)
		})
	}
}

// The fetch service is built per run over the run's writer: what the factory is
// handed is the gated store, so the service has no other route to the store.
func TestBooksT6G1Deps_FetchServiceGetsTheGatedStoreOverTheRunsWriter(t *testing.T) {
	s := newRepairPebble(t)
	prod, err := s.CreateAuthor("Wayne Enterprises Press")
	require.NoError(t, err)
	repairBook(t, s, "b1", &prod.ID, prod.ID)

	var handed metafetch.Store
	in := g1TestInputs(s, nil)
	factory := in.NewFetchService
	in.NewFetchService = func(gated metafetch.Store) *metafetch.Service { handed = gated; return factory(gated) }
	d, err := newT6G1Deps(s, g1NoRuns{}, in, g1TestKey)
	require.NoError(t, err)

	rec := &booksGateRecorder{refuse: tasks.ErrOutsideEffects}
	f := d.NewMetadataFetcher(rec)
	require.True(t, f.HasService())
	require.NotNil(t, handed)
	require.NotSame(t, metafetch.Store(s), handed, "the service must not be handed the bare store")
	_, err = handed.ModifyBook("b1", func(*database.Book) error { return nil })
	require.ErrorIs(t, err, tasks.ErrOutsideEffects, "a write through the handed store is a gated call")
	require.NotEmpty(t, rec.stamps())
}

// resolve-production-authors and auto-match-transcribed write only through the
// fetch service, so they are run for real over the built deps and the
// Effects-enforcing writer: nothing the service writes is outside the kind's
// declared Effects, and it did write.
func TestBooksT6G1_FetchKindsRunThroughTheBuiltServiceAndTheEnforcingWriter(t *testing.T) {
	t.Run("resolve-production-authors", func(t *testing.T) {
		s := newRepairPebble(t)
		prod, err := s.CreateAuthor("Monarch Theater")
		require.NoError(t, err)
		repairBook(t, s, "b1", &prod.ID, prod.ID)
		_, err = s.UpdateBook("b1", &database.Book{ID: "b1", Title: "Year One", FilePath: "/lib/b1.m4b", AuthorID: &prod.ID})
		require.NoError(t, err)

		in := g1TestInputs(s, []metadata.BookMetadata{{
			Title: "Year One", Author: "Bruce Wayne", Publisher: "Wayne Enterprises", Series: "The Long Halloween", SeriesPosition: "1",
			Description: "A masked vigilante's first year.", PublishYear: 1987, Language: "English",
		}})
		d, err := newT6G1Deps(s, g1NoRuns{}, in, g1TestKey)
		require.NoError(t, err)
		def := t6g1ByKind(t, (&Plugin{}).t6g1Kinds(d, nil), resolveProductionAuthorsKind)
		w := writertest.New(def, tasks.ModeLive)
		before := t6g5Snapshot(t, s)
		_, _, err = runT6G1Full(t, def, tasks.ModeLive, w, "", nil)
		require.NoError(t, err)
		require.NoError(t, w.RequireClean(), "a write of the fetch service is outside the kind's Effects")
		require.NotEmpty(t, w.Writes(), "the fetch wrote nothing: this proves nothing")
		require.NotEqual(t, before, t6g5Snapshot(t, s))
	})
	t.Run("auto-match-transcribed", func(t *testing.T) {
		s := newRepairPebble(t)
		trans, author := "Year One", "Bruce Wayne"
		_, err := s.CreateBook(&database.Book{ID: "b1", Title: "", FilePath: "/lib/b1.m4b", TranscribedTitle: &trans, TranscribedAuthor: &author})
		require.NoError(t, err)

		d, err := newT6G1Deps(s, g1NoRuns{}, g1TestInputs(s, nil), g1TestKey)
		require.NoError(t, err)
		def := t6g1ByKind(t, (&Plugin{}).t6g1Kinds(d, nil), autoMatchTranscribedKind)
		w := writertest.New(def, tasks.ModeLive)
		_, _, err = runT6G1Full(t, def, tasks.ModeLive, w, `{"min_score":0.75}`, nil)
		require.NoError(t, err)
		require.NoError(t, w.RequireClean())
		require.NotEmpty(t, w.Writes())
		got, err := s.GetBookByID("b1")
		require.NoError(t, err)
		require.Equal(t, trans, got.Title, "the apply filled the empty title")
	})
}

// A writing kind runs for real over the built store and writer: the composite
// is gated, the run is clean against its own Effects, and the library changed.
func TestBooksT6G1_AuthorSplitScanRunsThroughTheBuiltDeps(t *testing.T) {
	st := newSplitRelinkStore(t)
	composite, err := st.CreateAuthor("Bruce Wayne & Dick Grayson")
	require.NoError(t, err)
	ids := mkSplitBooks(t, st, composite.ID, 2)

	d, err := newT6G1Deps(st, g1NoRuns{}, g1TestInputs(st, nil), g1TestKey)
	require.NoError(t, err)
	def := t6g1ByKind(t, (&Plugin{}).t6g1Kinds(d, nil), authorSplitScanKind)
	w := writertest.New(def, tasks.ModeLive)
	_, _, err = runT6G1Full(t, def, tasks.ModeLive, w, "", nil)
	require.NoError(t, err)
	require.NoError(t, w.RequireClean())
	require.NotEmpty(t, w.Writes())
	for _, id := range ids {
		require.Zero(t, creditCounts(t, st, id)[composite.ID], "book %s still credits the composite", id)
	}
}

func TestBooksT6G1_PruneAIJournalRunsThroughTheBuiltDeps(t *testing.T) {
	s := seedJournal(t)
	d, err := newT6G1Deps(s, g1NoRuns{}, g1TestInputs(s, nil), g1TestKey)
	require.NoError(t, err)
	def := t6g1ByKind(t, (&Plugin{}).t6g1Kinds(d, nil), pruneAIJournalKind)
	w := writertest.New(def, tasks.ModeLive)
	raw, _, err := runT6G1Full(t, def, tasks.ModeLive, w, "", nil)
	require.NoError(t, err)
	require.NoError(t, w.RequireClean())
	var res PruneAIJournalResult
	require.NoError(t, json.Unmarshal(raw, &res))
	require.Equal(t, 30, res.RetentionDays, "the configured retention reached the kind")
	require.Equal(t, 1, res.Deleted, "the entry past retention went, the fresh one stayed")
	for _, wr := range w.Writes() {
		require.True(t, strings.HasPrefix(wr.Name, "books.AIJournalPruner"), "%s", wr.Name)
	}
}

// A preview run writes nothing: the pruner's deletes are fenced.
func TestBooksT6G1_PruneAIJournalPreviewIsRefusedAndDeletesNothing(t *testing.T) {
	s := seedJournal(t)
	before := t6g5Snapshot(t, s)
	d, err := newT6G1Deps(s, g1NoRuns{}, g1TestInputs(s, nil), g1TestKey)
	require.NoError(t, err)
	def := t6g1ByKind(t, (&Plugin{}).t6g1Kinds(d, nil), pruneAIJournalKind)
	w := writertest.New(def, tasks.ModePreview)
	_, _, _ = runT6G1Full(t, def, tasks.ModePreview, w, "", nil)
	require.Equal(t, before, t6g5Snapshot(t, s), "a preview run changed the journal")
}
