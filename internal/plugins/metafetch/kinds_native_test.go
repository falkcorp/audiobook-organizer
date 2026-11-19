// file: internal/plugins/metafetch/kinds_native_test.go
// version: 1.0.0
// guid: d7a40c58-31be-4e29-8f6a-b2c5917e03d4
// last-edited: 2026-11-19

package metafetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metadata/providerhttp"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// F5-05 behaviour tests for the native metafetch kinds. HEAD's shell is the
// oracle: where HEAD's observable output exists (the result document, the store
// after the run) both shells run on the same fixture and are compared.

// nilCandidates is a candidate-cache read that has nothing cached.
type nilCandidates struct{}

func (nilCandidates) GetCachedCandidates(string) (*database.MetadataCandidateCache, bool, error) {
	return nil, false, nil
}

const (
	isbnLongHalloween = "9780000000026"
	isbnHush          = "9780000000033"
)

// asinFixture is a library of three Batman books and an Audible fake that
// answers for them: one match to write, one no-match to remember, one book that
// already has an ASIN but no ISBN.
type asinFixture struct {
	store   *database.PebbleStore
	audible *fakeAudible
	ids     []string
}

const (
	bookLongHalloween = "bk-01-long-halloween"
	bookYearOne       = "bk-02-year-one"
	bookHush          = "bk-03-hush"
)

func newBatmanASINFixture(t *testing.T) *asinFixture {
	t.Helper()
	withSharedProviderCache(t)
	prodLH := metadata.AudibleIdentity{ASIN: "B0BATMAN01", Title: "The Long Halloween", Authors: []string{"Selina Kyle"}, ISBN: isbnLongHalloween, RuntimeMin: 600}
	prodHush := metadata.AudibleIdentity{ASIN: "B0BATMAN03", Title: "Hush", Authors: []string{"Barbara Gordon"}, ISBN: isbnHush, RuntimeMin: 480}
	fa := &fakeAudible{
		byTitle: map[string][]metadata.AudibleIdentity{"the long halloween": {prodLH}},
		byKW:    map[string][]metadata.AudibleIdentity{isbnLongHalloween: {prodLH}},
		byASIN:  map[string]*metadata.AudibleIdentity{"B0BATMAN03": &prodHush},
	}
	_, s := newASINTestPlugin(t, fa)
	selina := mkAuthor(t, s, "Selina Kyle")
	harvey := mkAuthor(t, s, "Harvey Dent")
	barbara := mkAuthor(t, s, "Barbara Gordon")
	mkBook(t, s, &database.Book{ID: bookLongHalloween, Title: "The Long Halloween", AuthorID: &selina, ISBN13: new(isbnLongHalloween), FilePath: "/synthetic/long-halloween.m4b"})
	mkBook(t, s, &database.Book{ID: bookYearOne, Title: "Year One", AuthorID: &harvey, FilePath: "/synthetic/year-one.m4b"})
	mkBook(t, s, &database.Book{ID: bookHush, Title: "Hush", AuthorID: &barbara, ASIN: new("B0BATMAN03"), FilePath: "/synthetic/hush.m4b"})
	return &asinFixture{store: s, audible: fa, ids: []string{bookLongHalloween, bookYearOne, bookHush}}
}

// asinNativeDeps is the deps a native asin-backfill run takes over the fixture.
func (fx *asinFixture) deps(t *testing.T, w kindWriter) (KindDeps, *guardStore) {
	t.Helper()
	g := &guardStore{kindStore: fx.store, t: t}
	return KindDeps{
		Store:      g,
		NewWriter:  func(tasks.Writer) kindWriter { return w },
		NewAudible: func() audibleIdentitySearcher { return fx.audible },
	}, g
}

// asinState is the observable store state of the fixture, comparable across two
// stores built by the same fixture function.
type asinState struct {
	ASIN    map[string]string
	ISBN13  map[string]string
	History map[string][]string
	Markers []string
	PCache  []string
}

func snapshotASIN(t *testing.T, fx *asinFixture) asinState {
	t.Helper()
	st := asinState{ASIN: map[string]string{}, ISBN13: map[string]string{}, History: map[string][]string{}}
	for _, id := range fx.ids {
		st.ASIN[id] = asinOf(t, fx.store, id)
		st.ISBN13[id], _ = isbnOf(t, fx.store, id)
		hist, err := fx.store.GetBookChangeHistory(id, 100)
		require.NoError(t, err)
		for _, h := range hist {
			require.NotEmpty(t, h.BatchID, "a history row has a batch id")
			st.History[id] = append(st.History[id], h.Field+"|"+h.ChangeType+"|"+h.Source)
		}
		sort.Strings(st.History[id])
	}
	st.Markers = pcacheKeys(t, fx.store, "asinbackfill:")
	st.PCache = pcacheKeys(t, fx.store, database.PCachePrefix)
	return st
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// nativeASIN runs the native kind over fx.
func nativeASIN(t *testing.T, fx *asinFixture, mode tasks.Mode, params string) (*kindHost, *recWriter, *guardStore, asinBackfillResult, error) {
	t.Helper()
	w := &recWriter{store: fx.store}
	deps, g := fx.deps(t, w)
	def := byKind(t, (&Plugin{}).Kinds(deps, nil), asinBackfillKind)
	h, out, err := runKind(t, def, mode, &sentinelWriter{}, params)
	var res asinBackfillResult
	if err == nil {
		require.NoError(t, json.Unmarshal(out, &res))
	}
	return h, w, g, res, err
}

// headASIN runs HEAD's v2 shell over fx.
func headASIN(t *testing.T, fx *asinFixture, params string) asinBackfillResult {
	t.Helper()
	p := New(fx.store, nil)
	p.newAudible = func() audibleIdentitySearcher { return fx.audible }
	return runASIN(t, p, params).res(t)
}

// Both shells, same fixture, every mode: the same result document and the same
// store afterwards (the HEAD shell is the oracle).
func TestMetafetchNative_ASINBackfillMatchesHEAD(t *testing.T) {
	for _, tc := range []struct {
		name       string
		headParams string
		mode       tasks.Mode
	}{
		{"dry run", `{}`, tasks.ModePreview},
		{"live", `{"dry_run":false}`, tasks.ModeLive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hf, nf := newBatmanASINFixture(t), newBatmanASINFixture(t)
			want := headASIN(t, hf, tc.headParams)
			_, _, g, got, err := nativeASIN(t, nf, tc.mode, "")
			require.NoError(t, err)
			require.Zero(t, g.writes.Load())
			// Proposals are collected by concurrent workers at HEAD too: compare
			// them as a set.
			sort.Slice(want.Proposals, func(i, j int) bool { return want.Proposals[i].BookID < want.Proposals[j].BookID })
			sort.Slice(got.Proposals, func(i, j int) bool { return got.Proposals[i].BookID < got.Proposals[j].BookID })
			require.Equal(t, jsonOf(t, want), jsonOf(t, got), "result document")
			require.Equal(t, snapshotASIN(t, hf), snapshotASIN(t, nf), "store afterwards")
			require.Equal(t, hf.audible.calls.Load(), nf.audible.calls.Load(), "Audible searches")
			require.Equal(t, hf.audible.lookups.Load(), nf.audible.lookups.Load(), "Audible ASIN lookups")
			if tc.mode == tasks.ModeLive {
				require.Equal(t, int64(1), got.MatchedWritten)
				require.Equal(t, int64(1), got.ISBNWritten)
				require.Equal(t, "B0BATMAN01", snapshotASIN(t, nf).ASIN[bookLongHalloween])
				require.Equal(t, isbnHush, snapshotASIN(t, nf).ISBN13[bookHush])
				require.Len(t, snapshotASIN(t, nf).Markers, 1, "the no-match book is remembered")
			}
		})
	}
}

// Every write of a live run goes through the writer, each under the method that
// names it: a no-match marker is SetRawASINMiss, a shared answer-cache row is
// SetRawPCache, and each family writes only under its own prefix.
func TestMetafetchNative_ASINBackfillWriterMethodsPerKeyFamily(t *testing.T) {
	fx := newBatmanASINFixture(t)
	_, w, g, _, err := nativeASIN(t, fx, tasks.ModeLive, "")
	require.NoError(t, err)
	require.Zero(t, g.writes.Load())
	require.Equal(t, []string{"asinbackfill:miss:" + bookYearOne}, w.keysOf("SetRawASINMiss"))
	pc := w.keysOf("SetRawPCache")
	require.NotEmpty(t, pc)
	for _, k := range pc {
		require.True(t, strings.HasPrefix(k, database.PCachePrefix), "SetRawPCache wrote %q", k)
	}
	require.Equal(t, []string{bookLongHalloween, bookHush}, w.keysOf("ModifyBook"), "the matched ASIN and the looked-up ISBN")
	require.Equal(t, []string{bookLongHalloween, bookHush}, w.keysOf("RecordMetadataChange"), "history rows ride the writer")
}

// A field the owner locked is skipped before any write: no ModifyBook reaches the
// writer for it and the book keeps its empty ASIN.
func TestMetafetchNative_ASINBackfillLockedFieldIsNotWritten(t *testing.T) {
	fx := newBatmanASINFixture(t)
	require.NoError(t, fx.store.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: bookLongHalloween, Field: database.FieldKeyASIN, OverrideValue: new(`""`), OverrideLocked: true}))
	_, w, _, res, err := nativeASIN(t, fx, tasks.ModeLive, "")
	require.NoError(t, err)
	require.Equal(t, int64(1), res.SkippedLocked)
	require.NotContains(t, w.keysOf("ModifyBook"), bookLongHalloween)
	require.Empty(t, snapshotASIN(t, fx).ASIN[bookLongHalloween])
}

// Params: the mode keys are refused, a bad document is refused, null and empty
// are the zero params; the other keys are HEAD's.
func TestMetafetchNative_ParamsRefuseModeKeys(t *testing.T) {
	cases := []struct {
		kind   tasks.Kind
		params string
		want   string
	}{
		{asinBackfillKind, `{"dry_run":true}`, "dry_run"},
		{asinBackfillKind, `{"dryRun":false}`, "dryRun"},
		{asinBackfillKind, `{"DRY_RUN":false}`, "DRY_RUN"},
		{asinBackfillKind, `{"apply":true}`, "apply"},
		{asinBackfillKind, `[1]`, "decode params"},
		{calibrateScoringKind, `{"dry_run":false}`, "dry_run"},
		{calibrateScoringKind, `{"sample_limit":"x"}`, "decode params"},
		{pcacheSweepKind, `{"dry_run":false}`, "dry_run"},
		{pcacheSweepKind, `{"apply":true}`, "apply"},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind)+tc.params, func(t *testing.T) {
			fx := newBatmanASINFixture(t)
			cf := newCalibrateFixture(t)
			sf := newSweepFixture(t)
			w := &recWriter{store: fx.store}
			deps := KindDeps{
				Store: &guardStore{kindStore: fx.store, t: t}, NewWriter: func(tasks.Writer) kindWriter { return w },
				NewAudible: func() audibleIdentitySearcher { return fx.audible },
				Candidates: cf.candidates, Scoring: cf.scoring,
			}
			if tc.kind == pcacheSweepKind {
				deps.Store = &guardStore{kindStore: sf.wrap, t: t}
			}
			def := byKind(t, (&Plugin{}).Kinds(deps, nil), tc.kind)
			_, _, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, tc.params)
			require.ErrorContains(t, err, tc.want)
			require.Empty(t, w.log(), "a refused run wrote nothing")
			require.Zero(t, fx.audible.calls.Load(), "a refused run searched nothing")
		})
	}
	for _, params := range []string{"", "null", " null ", "{}"} {
		fx := newBatmanASINFixture(t)
		_, _, _, _, err := nativeASIN(t, fx, tasks.ModePreview, params)
		require.NoError(t, err, "params %q", params)
	}
}

// A kind whose deps are unset is refused before it runs, naming the field.
func TestMetafetchNative_UnsetDepsAreRefused(t *testing.T) {
	cf := newCalibrateFixture(t)
	fx := newBatmanASINFixture(t)
	run := func(deps KindDeps, kind tasks.Kind, mode tasks.Mode) error {
		_, _, err := runKind(t, byKind(t, (&Plugin{}).Kinds(deps, nil), kind), mode, &sentinelWriter{}, "")
		return err
	}
	good := KindDeps{
		Store: &guardStore{kindStore: cf.store, t: t}, Candidates: cf.candidates, Scoring: cf.scoring,
		NewAudible: func() audibleIdentitySearcher { return fx.audible },
		NewWriter:  func(tasks.Writer) kindWriter { return &recWriter{store: fx.store} },
	}
	for _, kind := range []tasks.Kind{calibrateScoringKind, asinBackfillKind, pcacheSweepKind} {
		d := good
		d.Store = nil
		require.ErrorContains(t, run(d, kind, tasks.ModePreview), "KindDeps.Store is not set", kind)
	}
	d := good
	d.Candidates = nil
	require.ErrorContains(t, run(d, calibrateScoringKind, tasks.ModePreview), "KindDeps.Candidates is not set")
	d = good
	d.Scoring = nil
	require.ErrorContains(t, run(d, calibrateScoringKind, tasks.ModePreview), "KindDeps.Scoring is not set")
	d = good
	d.NewAudible = nil
	require.ErrorContains(t, run(d, asinBackfillKind, tasks.ModePreview), "KindDeps.NewAudible is not set")
	for _, kind := range []tasks.Kind{asinBackfillKind, pcacheSweepKind} {
		d = good
		d.NewWriter = nil
		require.ErrorContains(t, run(d, kind, tasks.ModeLive), "KindDeps.NewWriter is not set", kind)
		d.NewWriter = func(tasks.Writer) kindWriter { return nil }
		require.ErrorContains(t, run(d, kind, tasks.ModeLive), "returned no writer", kind)
		// A Preview run needs no writer factory.
		d.NewWriter = nil
		if kind == pcacheSweepKind {
			d.Store = &guardStore{kindStore: newSweepFixture(t).wrap, t: t}
		}
		require.NoError(t, run(d, kind, tasks.ModePreview), kind)
	}
}

// BuildSourceChain runs once, before any Audible client is built, and only when
// the run reaches the books (HEAD built the chain right before the client).
func TestMetafetchNative_ASINBackfillBuildsSourceChainBeforeTheClient(t *testing.T) {
	fx := newBatmanASINFixture(t)
	var order []string
	var mu sync.Mutex
	add := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }
	deps, _ := fx.deps(t, &recWriter{store: fx.store})
	deps.BuildSourceChain = func() { add("chain") }
	deps.NewAudible = func() audibleIdentitySearcher { add("client"); return fx.audible }
	_, _, err := runKind(t, byKind(t, (&Plugin{}).Kinds(deps, nil), asinBackfillKind), tasks.ModePreview, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Equal(t, "chain", order[0])
	require.Equal(t, 1, strings.Count(strings.Join(order, ","), "chain"))
	require.Contains(t, order, "client")
}

// The pool's declared worker count is the workers param, default 4, clamped 1..8.
func TestMetafetchNative_ASINBackfillWorkersParam(t *testing.T) {
	for _, tc := range []struct {
		params string
		want   int
	}{{``, 4}, {`{"workers":0}`, 4}, {`{"workers":1}`, 1}, {`{"workers":3}`, 3}, {`{"workers":8}`, 8}, {`{"workers":99}`, 8}} {
		fx := newBatmanASINFixture(t)
		h, _, _, _, err := nativeASIN(t, fx, tasks.ModePreview, tc.params)
		require.NoError(t, err)
		require.NotEmpty(t, h.declaredWorkers(), "params %q", tc.params)
		for _, d := range h.declaredWorkers() {
			require.Equal(t, tc.want, d, "params %q", tc.params)
		}
	}
}

// A page is checkpointed with HEAD's struct after it drains, and a resume reads
// HEAD's own bytes: books at or before the cursor are not searched again.
func TestMetafetchNative_ASINBackfillCheckpointAndResume(t *testing.T) {
	fx := newBatmanASINFixture(t)
	h, _, _, _, err := nativeASIN(t, fx, tasks.ModePreview, "")
	require.NoError(t, err)
	raw, ok := h.CheckpointJSON("")
	require.True(t, ok, "the run saved a checkpoint")
	require.JSONEq(t, `{"after_book_id":"`+bookHush+`"}`, string(raw), "the last book of the page, HEAD's key")
	_ = h

	// Resume from HEAD's literal bytes: after "bk-02-year-one" only the Hush
	// book is walked (it has an ASIN, so nothing is searched).
	fx2 := newBatmanASINFixture(t)
	w := &recWriter{store: fx2.store}
	deps, _ := fx2.deps(t, w)
	def := byKind(t, (&Plugin{}).Kinds(deps, nil), asinBackfillKind)
	host := newKindHost(context.Background(), def, tasks.ModePreview, &sentinelWriter{})
	require.NoError(t, host.SetCheckpoint("", json.RawMessage(`{"after_book_id":"`+bookLongHalloween+`"}`)))
	out, err := runKindOn(t, def, host, "")
	require.NoError(t, err)
	var res asinBackfillResult
	require.NoError(t, json.Unmarshal(out, &res))
	require.Equal(t, int64(2), res.Scanned, "the first book is behind the cursor")
	require.Equal(t, int64(1), fx2.audible.calls.Load(), "only the Year One search ran (the Long Halloween is behind the cursor)")
}

// A checkpoint that cannot be read starts from the params, as a failed merge did;
// one that cannot be saved is a warning, not a failure; with book_ids the cursor
// is not consulted.
func TestMetafetchNative_ASINBackfillCheckpointFailures(t *testing.T) {
	fx := newBatmanASINFixture(t)
	deps, _ := fx.deps(t, &recWriter{store: fx.store})
	def := byKind(t, (&Plugin{}).Kinds(deps, nil), asinBackfillKind)

	host := newKindHost(context.Background(), def, tasks.ModePreview, &sentinelWriter{})
	require.NoError(t, host.SetCheckpoint("", json.RawMessage(`"not an object"`)))
	out, err := runKindOn(t, def, host, "")
	require.NoError(t, err, "an unreadable checkpoint does not fail the run")
	var res asinBackfillResult
	require.NoError(t, json.Unmarshal(out, &res))
	require.Equal(t, int64(3), res.Scanned, "it started from the params' cursor (none)")

	host = newKindHost(context.Background(), def, tasks.ModePreview, &sentinelWriter{})
	host.FailCheckpoints(errors.New("disk full"))
	_, err = runKindOn(t, def, host, "")
	require.NoError(t, err, "a failed checkpoint save is a warning")
	_, saved := host.CheckpointJSON("")
	require.False(t, saved, "the failing save stored nothing")

	// The params' own cursor is honoured when there is no checkpoint, and the
	// checkpoint overlays it when there is one (HEAD's merge: the overlay wins).
	host = newKindHost(context.Background(), def, tasks.ModePreview, &sentinelWriter{})
	out, err = runKindOn(t, def, host, `{"after_book_id":"`+bookYearOne+`"}`)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(out, &res))
	require.Equal(t, int64(1), res.Scanned)
	host = newKindHost(context.Background(), def, tasks.ModePreview, &sentinelWriter{})
	require.NoError(t, host.SetCheckpoint("", json.RawMessage(`{"after_book_id":"`+bookLongHalloween+`"}`)))
	out, err = runKindOn(t, def, host, `{"after_book_id":"`+bookYearOne+`"}`)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(out, &res))
	require.Equal(t, int64(2), res.Scanned, "the checkpoint, not the params, decides")

	// book_ids walk exactly those books and never read the checkpoint.
	host = newKindHost(context.Background(), def, tasks.ModePreview, &sentinelWriter{})
	require.NoError(t, host.SetCheckpoint("", json.RawMessage(`{"after_book_id":"`+bookHush+`"}`)))
	out, err = runKindOn(t, def, host, `{"book_ids":["`+bookLongHalloween+`"," `+bookYearOne+`","bk-missing"]}`)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(out, &res))
	require.Equal(t, int64(2), res.Scanned)
	raw, saved := host.CheckpointJSON("")
	require.True(t, saved)
	require.JSONEq(t, `{"after_book_id":"`+bookHush+`"}`, string(raw), "the pre-seeded checkpoint is untouched: a book_ids run saves none")
}

// A cancelled run stops before it touches a book, in either shell.
func TestMetafetchNative_ASINBackfillCancel(t *testing.T) {
	fx := newBatmanASINFixture(t)
	w := &recWriter{store: fx.store}
	deps, g := fx.deps(t, w)
	def := byKind(t, (&Plugin{}).Kinds(deps, nil), asinBackfillKind)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runKindOn(t, def, newKindHost(ctx, def, tasks.ModeLive, &sentinelWriter{}), "")
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, w.log())
	require.Zero(t, g.writes.Load())
	require.Zero(t, fx.audible.calls.Load())
}

// The core returns what it counted even when the walk failed: HEAD persisted the
// result either way (ReporterSetResult), and the error is the walk's.
func TestMetafetchNative_ASINBackfillCoreReturnsResultWithError(t *testing.T) {
	fx := newBatmanASINFixture(t)
	bad := &failingWalkStore{kindStore: fx.store}
	rep := &asinTestReporter{}
	p := New(fx.store, nil)
	p.newAudible = func() audibleIdentitySearcher { return fx.audible }
	// HEAD's shell, with the walk failing: the error comes back and the result is stored.
	res, err := asinBackfillCore(context.Background(), asinBackfillParams{}, asinBackfillEnv{
		kindEnv: headEnv(rep, true), Store: bad, Writer: headASINBackfillWriter{store: fx.store}, NewClient: p.newAudible,
	})
	require.ErrorContains(t, err, "load books after")
	require.True(t, !res.DryRun, "live")
	require.Zero(t, res.Scanned)
}

type failingWalkStore struct{ kindStore }

func (failingWalkStore) GetAllBooksFullFrom(string, int) ([]database.Book, error) {
	return nil, errors.New("synthetic walk failure")
}

// pcache-sweep: both shells on the same fixture, every mode, same result and the
// same rows left.
func TestMetafetchNative_PCacheSweepMatchesHEAD(t *testing.T) {
	for _, tc := range []struct {
		name       string
		headParams string
		mode       tasks.Mode
	}{{"dry run", `{}`, tasks.ModePreview}, {"live", `{"dry_run":false}`, tasks.ModeLive}} {
		t.Run(tc.name, func(t *testing.T) {
			hf, nf := newSweepFixture(t), newSweepFixture(t)
			want := runSweep(t, hf.plugin, tc.headParams)
			g := &guardStore{kindStore: nf.wrap, t: t}
			w := &recWriter{store: nf.store}
			def := byKind(t, (&Plugin{}).Kinds(KindDeps{Store: g, NewWriter: func(tasks.Writer) kindWriter { return w }}, nil), pcacheSweepKind)
			h, out, err := runKind(t, def, tc.mode, &sentinelWriter{}, "")
			require.NoError(t, err)
			var got pcacheSweepResult
			require.NoError(t, json.Unmarshal(out, &got))
			require.Equal(t, jsonOf(t, want), jsonOf(t, got), "result document")
			// The two fixtures mint their own book ids, so rows are compared by
			// position in the fixtures' keep/dead lists, not by key text.
			require.Len(t, nf.keep, len(hf.keep))
			require.Len(t, nf.dead, len(hf.dead))
			for i := range hf.keep {
				require.Equal(t, hf.present(t, hf.keep[i]), nf.present(t, nf.keep[i]), "kept row %d", i)
			}
			for i := range hf.dead {
				require.Equal(t, hf.present(t, hf.dead[i]), nf.present(t, nf.dead[i]), "dead row %d", i)
			}
			require.Zero(t, g.writes.Load())
			require.Equal(t, []int{pcacheSweepResolveConcurrency}, dedupInts(h.declaredWorkers()), "pointer lookups run 8 wide")
			if tc.mode == tasks.ModeLive {
				require.NotEmpty(t, w.log())
			} else {
				require.Empty(t, w.log())
			}
		})
	}
}

func dedupInts(in []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// The delete-time re-check and the pointer-row lookup failure behave natively as
// they do at HEAD (both are in the fixture; HEAD's own tests pin them).
func TestMetafetchNative_PCacheSweepRecheckKeepsARefreshedRow(t *testing.T) {
	f := newSweepFixture(t)
	const k = "pcache:q:audible:search_title:fresh-positive"
	stale, err := json.Marshal(database.PCacheEntry{Provider: "audible", AskedAt: time.Now().Add(-400 * 24 * time.Hour),
		Results: json.RawMessage(`[{"title":"Title 000123"}]`)})
	require.NoError(t, err)
	f.wrap.staleKey, f.wrap.staleValue = k, stale
	w := &recWriter{store: f.store}
	def := byKind(t, (&Plugin{}).Kinds(KindDeps{Store: &guardStore{kindStore: f.wrap, t: t}, NewWriter: func(tasks.Writer) kindWriter { return w }}, nil), pcacheSweepKind)
	_, out, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	var res pcacheSweepResult
	require.NoError(t, json.Unmarshal(out, &res))
	require.Equal(t, int64(1), res.ExpiredKept)
	require.True(t, f.present(t, k))
	require.NotContains(t, w.keysOf("DeleteRawBatch"), k)
}

// A failing batch delete is counted, not the run's error (as at HEAD), and the
// rows it did not delete stay.
func TestMetafetchNative_PCacheSweepDeleteErrorIsCounted(t *testing.T) {
	f := newSweepFixture(t)
	w := &recWriter{store: f.store, failDelete: errors.New("synthetic delete failure")}
	def := byKind(t, (&Plugin{}).Kinds(KindDeps{Store: &guardStore{kindStore: f.wrap, t: t}, NewWriter: func(tasks.Writer) kindWriter { return w }}, nil), pcacheSweepKind)
	_, out, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	var res pcacheSweepResult
	require.NoError(t, json.Unmarshal(out, &res))
	require.Positive(t, res.DeleteErrors)
	require.Zero(t, res.ExpiredDeleted)
	for _, k := range f.dead {
		require.True(t, f.present(t, k), "%s was deleted although the delete failed", k)
	}
}

// A cancelled sweep deletes nothing.
func TestMetafetchNative_PCacheSweepCancel(t *testing.T) {
	f := newSweepFixture(t)
	w := &recWriter{store: f.store}
	def := byKind(t, (&Plugin{}).Kinds(KindDeps{Store: &guardStore{kindStore: f.wrap, t: t}, NewWriter: func(tasks.Writer) kindWriter { return w }}, nil), pcacheSweepKind)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runKindOn(t, def, newKindHost(ctx, def, tasks.ModeLive, &sentinelWriter{}), "")
	require.Error(t, err)
	require.Empty(t, w.log())
}

// calibrateFixture is the applied-book library of HEAD's race test, with Batman
// data: a matched manual-origin book, a matched auto-origin one, an empty cache,
// an unmatchable one and a book that was never applied.
type calibrateFixture struct {
	store      *database.MockStore
	candidates calibrateCandidates
	scoring    func() config.MetadataScoringConfig
}

func newCalibrateFixture(t *testing.T) *calibrateFixture {
	t.Helper()
	applied := metafetch.MetadataCandidate{Title: "Year One", Source: "audible", ASIN: "B0BATMAN01", DurationSec: 3000}
	distract := metafetch.MetadataCandidate{Title: "Something Else", Source: "google", ISBN: "111"}
	appliedHash := candidateSourceHash(applied)
	books := []database.Book{
		{ID: "m1", Title: "Year One", Duration: new(3000), MetadataSourceHash: &appliedHash},
		{ID: "m2", Title: "Year One", Duration: new(3000), MetadataSourceHash: &appliedHash},
		{ID: "e3", Title: "Empty", MetadataSourceHash: new("someotherhash")},
		{ID: "u4", Title: "Unmatch", MetadataSourceHash: new("nomatchhash")},
		{ID: "x5", Title: "NoHash"},
		{ID: "n6", Title: "No cache row", MetadataSourceHash: new("hashn6")},
		{ID: "r7", Title: "Cache read fails", MetadataSourceHash: new("hashr7")},
		{ID: "p8", Title: "Garbled candidate", MetadataSourceHash: new("hashp8")},
	}
	caches := map[string]*database.MetadataCandidateCache{
		"m1": {BookID: "m1", Candidates: mustCands(t, applied, distract)},
		"m2": {BookID: "m2", Candidates: mustCands(t, applied, distract)},
		"e3": {BookID: "e3"},
		"u4": {BookID: "u4", Candidates: mustCands(t, distract)},
		"p8": {BookID: "p8", Candidates: []json.RawMessage{json.RawMessage(`"not a candidate"`)}},
	}
	store := &database.MockStore{}
	store.GetAllBooksFullFromFunc = func(afterID string, _ int) ([]database.Book, error) {
		if afterID == "" {
			return books, nil
		}
		return nil, nil
	}
	store.GetMetadataCacheFunc = func(bookID string) (*database.MetadataCandidateCache, error) {
		if bookID == "r7" {
			return nil, errors.New("synthetic cache read failure")
		}
		return caches[bookID], nil
	}
	store.GetMetadataFieldStatesFunc = func(bookID string) ([]database.MetadataFieldState, error) {
		if bookID == "m1" {
			return []database.MetadataFieldState{{Field: "title", OverrideValue: new(`"x"`)}}, nil
		}
		return nil, nil
	}
	return &calibrateFixture{store: store, candidates: metafetch.NewService(store), scoring: func() config.MetadataScoringConfig { return config.MetadataScoringConfig{} }}
}

// captureReporter is HEAD's Reporter that keeps the log and the progress text.
type captureReporter struct {
	stubReporter
	mu   sync.Mutex
	logs strings.Builder
	msgs []string
}

func (r *captureReporter) Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&syncWriter{mu: &r.mu, b: &r.logs}, nil))
}

func (r *captureReporter) UpdateProgress(_, _ int, msg string) error {
	r.mu.Lock()
	r.msgs = append(r.msgs, msg)
	r.mu.Unlock()
	return nil
}

type syncWriter struct {
	mu *sync.Mutex
	b  *strings.Builder
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

// calibrate-scoring: HEAD's shell logs the report as JSON; the native kind returns
// the same document, the same progress text goes to the run's notes, the same
// pool width is declared, and nothing is written in either mode.
func TestMetafetchNative_CalibrateScoringMatchesHEAD(t *testing.T) {
	hf := newCalibrateFixture(t)
	rep := &captureReporter{}
	p := New(hf.store, metafetch.NewService(hf.store))
	require.NoError(t, p.runCalibrateScoring(context.Background(), nil, rep))

	for _, mode := range []tasks.Mode{tasks.ModePreview, tasks.ModeLive} {
		nf := newCalibrateFixture(t)
		g := &guardStore{kindStore: nf.store, t: t}
		def := byKind(t, (&Plugin{}).Kinds(KindDeps{Store: g, Candidates: nf.candidates, Scoring: nf.scoring}, nil), calibrateScoringKind)
		h, out, err := runKind(t, def, mode, &sentinelWriter{}, "")
		require.NoError(t, err)
		var got calibrationReport
		require.NoError(t, json.Unmarshal(out, &got))
		require.Equal(t, 3, got.SampleSize, "three applied books with a usable cache (the unusable ones are skipped before the sample)")
		require.Equal(t, 2, got.Evaluated)
		// The apply origin comes from the field-state store the core was handed: m1
		// carries a manual override, m2 does not.
		require.True(t, got.ManualSegmentDetermined)
		require.False(t, got.CircularBiased)
		require.Equal(t, 1, got.Segments["manual"].N)
		require.Equal(t, 1, got.Segments["auto"].N)
		require.Equal(t, map[string]int{"empty_cache": 2, "no_cache": 1, "cache_read_error": 1, "unparseable_candidate": 1, "unmatchable": 1}, got.SkipCounts, "skips")

		rep.mu.Lock()
		logs, msgs := rep.logs.String(), append([]string(nil), rep.msgs...)
		rep.mu.Unlock()
		require.Contains(t, logs, "report_json="+quote(jsonOf(t, got)), "HEAD logged the very document the kind returned")
		for _, m := range msgs {
			if strings.HasPrefix(m, "Books ") {
				continue // the pool's own frames, below
			}
			require.Contains(t, h.noted(), m, "HEAD's progress text %q is a note of the native run", m)
		}
		require.Equal(t, []int{runtime.NumCPU()}, dedupInts(h.declaredWorkers()), "the replay pool is NumCPU wide")
		require.Zero(t, g.writes.Load())
		var sawBooks bool
		for _, ev := range h.Progress() {
			sawBooks = sawBooks || strings.HasPrefix(ev.Current, "Books ")
		}
		require.True(t, sawBooks, "the pool's pure label reaches the native run")
	}
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }

// params: sample_limit caps the evaluation set, sweep_steps defaults and clamps.
func TestMetafetchNative_CalibrateScoringParams(t *testing.T) {
	nf := newCalibrateFixture(t)
	def := byKind(t, (&Plugin{}).Kinds(KindDeps{Store: &guardStore{kindStore: nf.store, t: t}, Candidates: nf.candidates, Scoring: nf.scoring}, nil), calibrateScoringKind)
	_, out, err := runKind(t, def, tasks.ModePreview, &sentinelWriter{}, `{"sample_limit":1,"sweep_steps":99}`)
	require.NoError(t, err)
	var got calibrationReport
	require.NoError(t, json.Unmarshal(out, &got))
	require.Equal(t, 1, got.SampleSize)
	require.Equal(t, maxSweepSteps, got.SweepSteps)

	for _, tc := range []struct {
		in      calibrateScoringParams
		limit   int
		steps   int
		comment string
	}{
		{calibrateScoringParams{}, defaultSampleLimit, defaultSweepSteps, "defaults"},
		{calibrateScoringParams{SampleLimit: -5, SweepSteps: -1}, defaultSampleLimit, defaultSweepSteps, "negatives are defaults"},
		{calibrateScoringParams{SampleLimit: 7, SweepSteps: 3}, 7, 3, "as given"},
		{calibrateScoringParams{SweepSteps: maxSweepSteps + 1}, defaultSampleLimit, maxSweepSteps, "steps clamp"},
		{calibrateScoringParams{SweepSteps: maxSweepSteps}, defaultSampleLimit, maxSweepSteps, "max is allowed"},
	} {
		limit, steps := calibrateScoringSample(tc.in)
		require.Equal(t, tc.limit, limit, tc.comment)
		require.Equal(t, tc.steps, steps, tc.comment)
	}
}

// With nothing applied the report is the empty one and the run says so.
func TestMetafetchNative_CalibrateScoringNoApplied(t *testing.T) {
	store := &database.MockStore{}
	store.GetAllBooksFullFromFunc = func(string, int) ([]database.Book, error) { return nil, nil }
	def := byKind(t, (&Plugin{}).Kinds(KindDeps{Store: &guardStore{kindStore: store, t: t}, Candidates: nilCandidates{}, Scoring: func() config.MetadataScoringConfig { return config.MetadataScoringConfig{} }}, nil), calibrateScoringKind)
	h, out, err := runKind(t, def, tasks.ModePreview, &sentinelWriter{}, "")
	require.NoError(t, err)
	var got calibrationReport
	require.NoError(t, json.Unmarshal(out, &got))
	require.Zero(t, got.SampleSize)
	require.Zero(t, got.Evaluated)
	require.Contains(t, strings.Join(h.noted(), "\n"), "no applied books with usable caches found")
	require.Empty(t, h.declaredWorkers(), "no pool ran")
}

// The walk's error is wrapped as HEAD wrapped it; a cancelled run stops at the
// first page.
func TestMetafetchNative_CalibrateScoringErrorsAndCancel(t *testing.T) {
	store := &database.MockStore{}
	store.GetAllBooksFullFromFunc = func(string, int) ([]database.Book, error) { return nil, errors.New("synthetic walk failure") }
	deps := KindDeps{Store: &guardStore{kindStore: store, t: t}, Candidates: nilCandidates{}, Scoring: func() config.MetadataScoringConfig { return config.MetadataScoringConfig{} }}
	def := byKind(t, (&Plugin{}).Kinds(deps, nil), calibrateScoringKind)
	_, _, err := runKind(t, def, tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "enumerate books: synthetic walk failure")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = runKindOn(t, def, newKindHost(ctx, def, tasks.ModePreview, &sentinelWriter{}), "")
	require.ErrorIs(t, err, context.Canceled)
}

// applyOriginVia is applyOrigin over the store the core was handed.
func TestMetafetchNative_ApplyOriginVia(t *testing.T) {
	cf := newCalibrateFixture(t)
	p := New(cf.store, nil)
	for _, id := range []string{"m1", "m2", "e3"} {
		b := &database.Book{ID: id}
		require.Equal(t, p.applyOrigin(b), applyOriginVia(cf.store, b), id)
	}
	require.Equal(t, "manual", applyOriginVia(cf.store, &database.Book{ID: "m1"}))
	require.Equal(t, "auto", applyOriginVia(cf.store, &database.Book{ID: "m2"}))
}

// HEAD's shell checkpoints the page cursor with HEAD's own struct and labels the
// pool's items with the tally; HEAD's cancel signal (the reporter's) stops the
// calibration before it reads a book.
func TestMetafetchEnv_HEADShellsKeepTheirReporterContract(t *testing.T) {
	fx := newBatmanASINFixture(t)
	rep := &captureReporter{}
	cp := &checkpointRecorder{captureReporter: rep}
	p := New(fx.store, nil)
	p.newAudible = func() audibleIdentitySearcher { return fx.audible }
	require.NoError(t, p.runASINBackfill(context.Background(), nil, cp))
	require.Equal(t, []any{asinBackfillCheckpoint{AfterBookID: bookHush}}, cp.saved(), "one page, one checkpoint, HEAD's struct")
	re := regexp.MustCompile(`^Books \d/3 \(scanned \d+, asin would write \d+,`)
	rep.mu.Lock()
	defer rep.mu.Unlock()
	var labelled bool
	for _, m := range rep.msgs {
		labelled = labelled || re.MatchString(m)
	}
	require.True(t, labelled, "the counter label reaches HEAD's pool: %v", rep.msgs)
}

type checkpointRecorder struct {
	*captureReporter
	cmu sync.Mutex
	cps []any
}

func (c *checkpointRecorder) Checkpoint(v any) error {
	c.cmu.Lock()
	c.cps = append(c.cps, v)
	c.cmu.Unlock()
	return nil
}

func (c *checkpointRecorder) saved() []any {
	c.cmu.Lock()
	defer c.cmu.Unlock()
	return append([]any(nil), c.cps...)
}

type canceledReporter struct{ captureReporter }

func (*canceledReporter) IsCanceled() bool { return true }

func TestMetafetchEnv_HEADCalibrateStopsOnTheReportersCancel(t *testing.T) {
	cf := newCalibrateFixture(t)
	reads := 0
	cf.store.GetAllBooksFullFromFunc = func(string, int) ([]database.Book, error) { reads++; return nil, nil }
	p := New(cf.store, metafetch.NewService(cf.store))
	err := p.runCalibrateScoring(context.Background(), nil, &canceledReporter{})
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, reads, "no book was read after the cancel")
}

// retry_after_days and limit are HEAD's params, carried by the native kind: a
// no-match marker is retried at once with retry_after_days 0, and limit caps the
// searches.
func TestMetafetchNative_ASINBackfillRetryAndLimitParams(t *testing.T) {
	fx := newBatmanASINFixture(t)
	_, _, _, _, err := nativeASIN(t, fx, tasks.ModeLive, "")
	require.NoError(t, err)
	_, _, _, res, err := nativeASIN(t, fx, tasks.ModePreview, "")
	require.NoError(t, err)
	require.Equal(t, int64(1), res.SkippedRecentMiss, "the no-match marker holds for the default 30 days")
	_, _, _, res, err = nativeASIN(t, fx, tasks.ModePreview, `{"retry_after_days":0}`)
	require.NoError(t, err)
	require.Zero(t, res.SkippedRecentMiss, "retry_after_days 0 searches the no-match book again")

	fx2 := newBatmanASINFixture(t)
	_, _, _, res, err = nativeASIN(t, fx2, tasks.ModePreview, `{"limit":1,"workers":1}`)
	require.NoError(t, err)
	require.Positive(t, res.SkippedLimit, "the limit stopped the walk before every book was searched")
	full := newBatmanASINFixture(t)
	_, _, _, fullRes, err := nativeASIN(t, full, tasks.ModePreview, "")
	require.NoError(t, err)
	require.Less(t, res.Searched, fullRes.Searched)
	require.Less(t, fx2.audible.calls.Load(), full.audible.calls.Load())
}

// The second page of books continues the bar where the first stopped (the pool
// is handed the page's offset), so the bar ends at the library's count.
func TestMetafetchNative_ASINBackfillSecondPageContinuesTheBar(t *testing.T) {
	withSharedProviderCache(t)
	fa := &fakeAudible{}
	_, s := newASINTestPlugin(t, fa)
	aid := mkAuthor(t, s, "Selina Kyle")
	n := asinBackfillPageSize + 5
	for i := range n {
		mkBook(t, s, &database.Book{ID: fmt.Sprintf("bk-%03d", i), Title: fmt.Sprintf("Gotham Case %03d", i), AuthorID: &aid, FilePath: fmt.Sprintf("/synthetic/gotham-%03d.m4b", i)})
	}
	h, _, _, _, err := nativeASIN(t, &asinFixture{store: s, audible: fa}, tasks.ModePreview, "")
	require.NoError(t, err)
	var maxDone int64
	for _, ev := range h.Progress() {
		maxDone = max(maxDone, ev.Done)
	}
	require.EqualValues(t, n, maxDone, "the second page's frames continue after the first page's")
}

// A history row that cannot be recorded is counted in the result, not lost.
func TestMetafetchNative_ASINBackfillCountsFailedHistoryRows(t *testing.T) {
	fx := newBatmanASINFixture(t)
	w := &recWriter{store: fx.store, failRecord: errors.New("synthetic history failure")}
	deps, _ := fx.deps(t, w)
	def := byKind(t, (&Plugin{}).Kinds(deps, nil), asinBackfillKind)
	_, out, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	var res asinBackfillResult
	require.NoError(t, json.Unmarshal(out, &res))
	// The writer tries a history row twice per modified book before it gives up and
	// counts that book's row failed.
	modified := len(w.keysOf("ModifyBook"))
	require.Positive(t, modified)
	require.Equal(t, 2*modified, len(w.keysOf("RecordMetadataChange")), "each history row was retried once")
	require.Equal(t, modified, res.HistoryFailed, "every unrecorded history row is counted")
}

// HEAD's shell builds the configured source chain before it builds the Audible
// client (that is what installs the configured budget into providerhttp): an
// enabled source in the config has its budget applied by the run.
func TestMetafetchEnv_HEADASINShellBuildsTheSourceChainFirst(t *testing.T) {
	id := fmt.Sprintf("gotham-probe-%d", time.Now().UnixNano())
	saved := config.AppConfig.MetadataSources
	config.AppConfig.MetadataSources = []config.MetadataSource{{ID: id, Enabled: true, Priority: 1}}
	t.Cleanup(func() { config.AppConfig.MetadataSources = saved })
	_, s := newASINTestPlugin(t, &fakeAudible{})
	p := New(s, metafetch.NewService(s)) // no newAudible seam: the shell builds the chain
	require.NoError(t, p.runASINBackfill(context.Background(), nil, &asinTestReporter{}))
	require.True(t, providerhttp.HasOverride(id), "the run applied the configured source's budget")
}

// calibrateRankFixture is one applied book whose runtime (3000 s) matches the rival
// candidate and not the applied one (9000 s): the duration tiers rank the rival
// first when the runtime is read, and tie them when it is not.
func calibrateRankRun(t *testing.T, scoring func() config.MetadataScoringConfig) calibrationReport {
	t.Helper()
	applied := metafetch.MetadataCandidate{Title: "Year One", Source: "audible", ASIN: "B0BATMAN01", DurationSec: 9000}
	rival := metafetch.MetadataCandidate{Title: "Year One", Source: "audible", ASIN: "B0BATMAN02", DurationSec: 3000}
	hash := candidateSourceHash(applied)
	require.NotEqual(t, hash, candidateSourceHash(rival))
	store := &database.MockStore{}
	store.GetAllBooksFullFromFunc = func(afterID string, _ int) ([]database.Book, error) {
		if afterID == "" {
			return []database.Book{{ID: "rt1", Title: "Year One", Duration: new(3000), MetadataSourceHash: &hash}}, nil
		}
		return nil, nil
	}
	store.GetMetadataCacheFunc = func(string) (*database.MetadataCandidateCache, error) {
		return &database.MetadataCandidateCache{BookID: "rt1", Candidates: mustCands(t, applied, rival)}, nil
	}
	def := byKind(t, (&Plugin{}).Kinds(KindDeps{Store: &guardStore{kindStore: store, t: t}, Candidates: metafetch.NewService(store), Scoring: scoring}, nil), calibrateScoringKind)
	_, out, err := runKind(t, def, tasks.ModePreview, &sentinelWriter{}, "")
	require.NoError(t, err)
	var got calibrationReport
	require.NoError(t, json.Unmarshal(out, &got))
	require.Equal(t, 1, got.Evaluated)
	return got
}

// The replay reads the book's runtime through the store the core was handed, and
// the current knobs come from the scoring config the shell supplied.
func TestMetafetchNative_CalibrateScoringReadsRuntimeAndConfig(t *testing.T) {
	def := calibrateRankRun(t, func() config.MetadataScoringConfig { return config.MetadataScoringConfig{} })
	require.EqualValues(t, 2, def.CurrentKnobMeanRank, "the rival's duration match outranks the applied candidate")

	flat := calibrateRankRun(t, func() config.MetadataScoringConfig {
		return config.MetadataScoringConfig{
			DurationTierMultipliers: []float64{1, 1, 1, 1, 1, 1},
			DurationTierScores:      []float64{0, 0, 0, 0, 0, 0},
		}
	})
	require.EqualValues(t, 1, flat.CurrentKnobMeanRank, "a config with flat duration tiers ties them: the config reached the knobs")
}

// An empty sample still logs its report (HEAD's shell logs through the reporter's logger).
func TestMetafetchEnv_HEADCalibrateLogsTheEmptyReport(t *testing.T) {
	store := &database.MockStore{}
	store.GetAllBooksFullFromFunc = func(string, int) ([]database.Book, error) { return nil, nil }
	rep := &captureReporter{}
	require.NoError(t, New(store, metafetch.NewService(store)).runCalibrateScoring(context.Background(), nil, rep))
	rep.mu.Lock()
	defer rep.mu.Unlock()
	require.Contains(t, rep.logs.String(), "calibrate-scoring report")
}

// HEAD's pcache-sweep shell runs the pointer pages on the silent errgroup, not on
// registry.RunItems: its progress frames are exactly the core's own.
func TestMetafetchEnv_HEADPCacheShellKeepsThePointerPagesSilent(t *testing.T) {
	frames := func(rep *envRecorder) []string {
		var out []string
		for _, f := range rep.progressFrames() {
			out = append(out, fmt.Sprint(f))
		}
		sort.Strings(out)
		return out
	}
	headRep := &envRecorder{}
	f1 := newSweepFixture(t)
	require.NoError(t, f1.plugin.runPCacheSweep(context.Background(), nil, headRep))

	refRep := &envRecorder{}
	f2 := newSweepFixture(t)
	env := pcacheSweepEnv{kindEnv: headEnv(refRep, false), Store: f2.wrap, Writer: headPCacheSweepWriter{store: f2.wrap}}
	env.Items = headErrgroupItems()
	_, err := pcacheSweepCore(context.Background(), env)
	require.NoError(t, err)
	require.NotEmpty(t, frames(refRep))
	require.Equal(t, frames(refRep), frames(headRep))
}
