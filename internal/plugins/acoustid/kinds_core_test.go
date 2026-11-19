// file: internal/plugins/acoustid/kinds_core_test.go
// version: 1.0.0
// guid: af313943-69ef-47d0-b04c-a436ae653f5e
// last-edited: 2026-11-19

package acoustid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/acoustid"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// F5-05 core tests: HEAD's shell and the native shell over the same core, on the
// same fixture, compared on the writes each made and the result each returned
// (the brief's parity tests), the checkpoint bytes HEAD wrote, and the params
// edge cases.

// ---- shared helpers ----

// coreReporter is HEAD's Reporter: every progress frame, checkpoint and log line
// a run sent it.
type coreReporter struct {
	mu     sync.Mutex
	frames []coreFrame
	ckpts  []json.RawMessage
	logs   bytes.Buffer
}

type coreFrame struct {
	Cur, Total int
	Msg        string
}

func (r *coreReporter) UpdateProgress(c, t int, msg string) error {
	r.mu.Lock()
	r.frames = append(r.frames, coreFrame{c, t, msg})
	r.mu.Unlock()
	return nil
}
func (r *coreReporter) Log(slog.Level, string, ...slog.Attr) error { return nil }
func (r *coreReporter) Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&lockedWriter{mu: &r.mu, w: &r.logs}, nil))
}
func (r *coreReporter) IsCanceled() bool                           { return false }
func (r *coreReporter) Trigger(context.Context, string, any) error { return nil }
func (r *coreReporter) SetCurrentItem(string)                      {}
func (r *coreReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, registry.Reporter) error) error {
	return fn(ctx, r)
}
func (r *coreReporter) Checkpoint(state any) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.ckpts = append(r.ckpts, raw)
	r.mu.Unlock()
	return nil
}

func (r *coreReporter) lastCheckpoint(t *testing.T) json.RawMessage {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.NotEmpty(t, r.ckpts, "HEAD wrote no checkpoint")
	return r.ckpts[len(r.ckpts)-1]
}

// headBarAndNotes is what a native host must show for the frames HEAD's reporter
// got: one bar frame per report (a total <= 0 unknown) and one note per non-empty
// message.
func headBarAndNotes(frames []coreFrame) ([]barFrame, []string) {
	var bars []barFrame
	var notes []string
	for _, f := range frames {
		b := barFrame{Done: int64(f.Cur)}
		if f.Total > 0 {
			b.Total, b.Known = int64(f.Total), true
		}
		bars = append(bars, b)
		if f.Msg != "" {
			notes = append(notes, f.Msg)
		}
	}
	return bars, notes
}

// writeLog is the writes a HEAD run made on its MockStore, in the format recWriter
// logs, so the two compare as sorted lists.
type writeLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *writeLog) add(format string, a ...any) {
	l.mu.Lock()
	l.calls = append(l.calls, fmt.Sprintf(format, a...))
	l.mu.Unlock()
}

func (l *writeLog) sorted() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := slices.Clone(l.calls)
	slices.Sort(out)
	return out
}

func sortedCalls(w *recWriter) []string {
	out := w.logged()
	slices.Sort(out)
	return out
}

// recordWrites points the store's write methods at l (a HEAD run writes through
// its own store).
func recordWrites(s *database.MockStore, l *writeLog) {
	s.UpdateBookFileFunc = func(id string, _ *database.BookFile) error { l.add("UpdateBookFile:%s", id); return nil }
	s.ModifyBookFunc = func(id string, fn func(*database.Book) error) (*database.Book, error) {
		l.add("ModifyBook:%s", id)
		b := &database.Book{ID: id}
		return b, fn(b)
	}
	s.ClearBookSignatureFunc = func(id string) error { l.add("ClearBookSignature:%s", id); return nil }
}

// nativeRun runs the native kind on a Live host over deps with the kit's writer.
func nativeRun(t *testing.T, k *kit, store kindStore, kind tasks.Kind, params string) (*kindHost, any, error) {
	t.Helper()
	d := byKind(t, (&Plugin{}).Kinds(k.deps(store), nil), kind)
	h := newKindHost(context.Background(), d, tasks.ModeLive, &sentinelWriter{})
	res, err := runKindResult(t, d, h, params)
	return h, res, err
}

// ---- params ----

// TestAcoustidCore_ParamsRefuseModeKeys: every kind refuses a mode key in its
// params, in any case, before it touches a store (the mode comes from the start
// request, 05-tasks 5T.3.7). A dropped {"live":false} would turn a dry run into a
// live write.
func TestAcoustidCore_ParamsRefuseModeKeys(t *testing.T) {
	defs := (&Plugin{}).Kinds(fakeKindDeps(t), nil)
	for _, d := range defs {
		for _, key := range []string{"dry_run", "dryRun", "apply", "live", "LIVE", "Dry_Run", "DRYRUN", "Apply"} {
			t.Run(string(d.Kind())+"/"+key, func(t *testing.T) {
				k := newKit()
				plug := (&Plugin{}).Kinds(k.deps(guardedStore(t)), nil)
				def := byKind(t, plug, d.Kind())
				_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, fmt.Sprintf(`{%q:true}`, key))
				require.ErrorContains(t, err, "the run's mode comes from the start request")
				require.ErrorContains(t, err, fmt.Sprintf("%q", key), "the refusal names the offending key as written")
				require.Empty(t, k.handed(), "refused before any writer was built")
				require.Empty(t, k.rec.logged())
			})
		}
	}
}

// TestAcoustidCore_ParamsShapeEdges: null, empty and {} are the zero value, an
// unknown key is ignored as HEAD ignored it, and anything that is not a JSON
// object is refused.
func TestAcoustidCore_ParamsShapeEdges(t *testing.T) {
	for _, ok := range []string{``, `null`, `  null `, `{}`, `{"bogus":1}`} {
		t.Run("accepts/"+ok, func(t *testing.T) {
			k := newKit()
			_, _, err := nativeRun(t, k, guardedStore(t), scanKind, ok)
			require.NoError(t, err)
			require.Contains(t, k.rec.logged(), "AcoustIDScan")
		})
	}
	for _, bad := range []string{`[]`, `"x"`, `12`, `true`, `{`} {
		t.Run("refuses/"+bad, func(t *testing.T) {
			k := newKit()
			_, _, err := nativeRun(t, k, guardedStore(t), scanKind, bad)
			require.ErrorContains(t, err, "decode")
			require.Empty(t, k.rec.logged(), "the scan never started")
		})
	}
	// A typed field of the wrong type is refused by the kinds that take params.
	for _, kind := range []tasks.Kind{fingerprintRescanKind, onlineLookupKind, windowBackfillKind} {
		t.Run("typed/"+string(kind), func(t *testing.T) {
			k := newKit()
			_, _, err := nativeRun(t, k, guardedStore(t), kind, `{"limit":"many","scope":5,"concurrency":"x"}`)
			require.ErrorContains(t, err, "decode params")
		})
	}
}

// ---- scan ----

func TestAcoustidCore_ScanWiring(t *testing.T) {
	t.Run("pending count is the result", func(t *testing.T) {
		k := newKit()
		_, res, err := nativeRun(t, k, guardedStore(t), scanKind, ``)
		require.NoError(t, err)
		requireResult(t, scanResult{PendingCandidates: 2}, res)
	})
	t.Run("engine error is wrapped and skips the summary", func(t *testing.T) {
		k := newKit()
		k.eng.failAt = fmt.Errorf("the Riddler hid the index")
		h, _, err := nativeRun(t, k, guardedStore(t), scanKind, ``)
		require.EqualError(t, err, "acoustid scan: the Riddler hid the index")
		require.Contains(t, h.logged(), "AcoustIDScan error")
	})
	t.Run("no NewEngine is refused", func(t *testing.T) {
		k := newKit()
		d := k.deps(guardedStore(t))
		d.NewEngine = nil
		def := byKind(t, (&Plugin{}).Kinds(d, nil), scanKind)
		_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, ``)
		require.EqualError(t, err, string(scanKind)+": KindDeps.NewEngine is not set")
	})
	t.Run("a factory that builds nothing is HEAD's refusal", func(t *testing.T) {
		k := newKit()
		d := k.deps(guardedStore(t))
		d.NewEngine = func(tasks.Writer) scanEngine { return nil }
		def := byKind(t, (&Plugin{}).Kinds(d, nil), scanKind)
		_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, ``)
		require.EqualError(t, err, "dedup engine not available")
	})
	t.Run("no candidate store skips the count", func(t *testing.T) {
		k := newKit()
		d := k.deps(guardedStore(t))
		d.Candidates = nil
		def := byKind(t, (&Plugin{}).Kinds(d, nil), scanKind)
		h := newKindHost(context.Background(), def, tasks.ModeLive, &sentinelWriter{})
		res, err := runKindResult(t, def, h, ``)
		require.NoError(t, err)
		requireResult(t, scanResult{}, res)
	})
}

func TestAcoustidCore_ScanFramesMatchHEAD(t *testing.T) {
	k := newKit()
	rep := &coreReporter{}
	res, err := scanCore(context.Background(), scanEnv{kindEnv: headEnv(rep, true), Engine: k.eng, Candidates: fakeCandidates{pending: 2}})
	require.NoError(t, err)

	h, got, nerr := nativeRun(t, newKit(), guardedStore(t), scanKind, ``)
	require.NoError(t, nerr)
	requireResult(t, res, got)
	bars, notes := headBarAndNotes(rep.frames)
	require.Equal(t, bars, h.bars())
	require.Equal(t, notes, h.allNotes())
	require.Equal(t, []string{"AcoustIDScan"}, k.rec.logged())
}

// ---- backfill ----

// backfillFixture is n books with one unfingerprinted audio file each.
func backfillFixture(t *testing.T, n int) *pagedFixture {
	t.Helper()
	stubFingerprint(t)
	return newPagedFixture(n, 1, eligibleFiles(t))
}

func headBackfillEnvFor(p *Plugin, rep *coreReporter) backfillEnv {
	env := p.headBackfillEnv(rep)
	env.HasEngine = true
	env.Host = fakeFingerprintDeps().hooks()
	env.Workers = func() int { return 2 }
	return env
}

func TestAcoustidCore_BackfillMatchesHEAD(t *testing.T) {
	const n = 5
	// HEAD: its shell over a store it writes through.
	hf := backfillFixture(t, n)
	var hl writeLog
	recordWrites(hf.store, &hl)
	hres, err := backfillCore(context.Background(), nil, headBackfillEnvFor(&Plugin{store: hf.store}, &coreReporter{}))
	require.NoError(t, err)

	// Native: the same fixture shape, writes through the writer only.
	nf := backfillFixture(t, n)
	nf.store.UpdateBookFileFunc = func(id string, _ *database.BookFile) error {
		t.Errorf("store.UpdateBookFile(%s) bypassed the writer", id)
		return nil
	}
	k := newKit()
	_, nres, nerr := nativeRun(t, k, nf.store, backfillKind, ``)
	require.NoError(t, nerr)

	requireResult(t, hres, nres)
	requireResult(t, backfillResult{Books: n, Fingerprinted: n}, nres)
	require.Len(t, hl.sorted(), 2*n, "each file is a signature clear plus a file update")
	require.Equal(t, hl.sorted(), sortedCalls(k.rec), "the same files, written through the writer")
}

func TestAcoustidCore_BackfillRefusalsMatchHEAD(t *testing.T) {
	// The engine gate comes first, then the fingerprint backend, then the store.
	fx := backfillFixture(t, 1)
	env := headBackfillEnvFor(&Plugin{store: fx.store}, &coreReporter{})
	env.HasEngine = false
	_, err := backfillCore(context.Background(), nil, env)
	require.EqualError(t, err, "dedup engine not available")

	env = headBackfillEnvFor(&Plugin{store: fx.store}, &coreReporter{})
	env.Host.FingerprintAvailable = func() bool { return false }
	res, err := backfillCore(context.Background(), nil, env)
	require.NoError(t, err)
	requireResult(t, backfillResult{}, res, "no fpcalc/ffmpeg is a logged skip, not a failure")

	env = headBackfillEnvFor(&Plugin{}, &coreReporter{})
	_, err = backfillCore(context.Background(), nil, env)
	require.EqualError(t, err, "database store not available")

	// Native: no store or no writer factory is refused before the core runs.
	k := newKit()
	d := k.deps(nil)
	d.Store = nil
	def := byKind(t, (&Plugin{}).Kinds(d, nil), backfillKind)
	_, err = runKind(t, def, tasks.ModeLive, &sentinelWriter{}, ``)
	require.EqualError(t, err, string(backfillKind)+": KindDeps.Store is not set")
	d = k.deps(fx.store)
	d.NewWriter = nil
	def = byKind(t, (&Plugin{}).Kinds(d, nil), backfillKind)
	_, err = runKind(t, def, tasks.ModeLive, &sentinelWriter{}, ``)
	require.EqualError(t, err, string(backfillKind)+": KindDeps.NewWriter is not set")
	d = k.deps(fx.store)
	d.NewWriter = func(tasks.Writer) kindWriter { return nil }
	def = byKind(t, (&Plugin{}).Kinds(d, nil), backfillKind)
	_, err = runKind(t, def, tasks.ModeLive, &sentinelWriter{}, ``)
	require.EqualError(t, err, string(backfillKind)+": the writer factory returned no writer")
}

// TestAcoustidCore_BackfillRestoresHEADCheckpointBytes: the checkpoint HEAD wrote
// (its BackfillParams, re-dispatched as the params on ResumeRestart) is what a
// native Restore reads, and the native run writes the same bytes.
func TestAcoustidCore_BackfillRestoresHEADCheckpointBytes(t *testing.T) {
	const n = 4
	cursor := BackfillParams{AfterBookID: "book-00001"}
	headBytes, err := json.Marshal(cursor)
	require.NoError(t, err)

	visited := func(fx *pagedFixture) []string {
		fx.mu.Lock()
		defer fx.mu.Unlock()
		var out []string
		for id := range fx.visits {
			out = append(out, id)
		}
		slices.Sort(out)
		return out
	}

	t.Run("HEAD params and native Restore resume at the same book", func(t *testing.T) {
		hf := backfillFixture(t, n)
		var hl writeLog
		recordWrites(hf.store, &hl)
		rep := &coreReporter{}
		_, err := backfillCore(context.Background(), headBytes, headBackfillEnvFor(&Plugin{store: hf.store}, rep))
		require.NoError(t, err)

		nf := backfillFixture(t, n)
		nf.store.UpdateBookFileFunc = func(string, *database.BookFile) error { return nil }
		k := newKit()
		d := byKind(t, (&Plugin{}).Kinds(k.deps(nf.store), nil), backfillKind)
		h := newKindHost(context.Background(), d, tasks.ModeLive, &sentinelWriter{})
		require.NoError(t, h.SetCheckpoint("", cursor))
		_, nerr := runKindResult(t, d, h, ``)
		require.NoError(t, nerr)

		require.Equal(t, []string{"book-00002", "book-00003"}, visited(hf), "HEAD resumed after the cursor")
		require.Equal(t, visited(hf), visited(nf), "native resumed at the same book")

		// The checkpoint the native run wrote is the one HEAD's reporter got.
		nraw, ok := h.CheckpointJSON("")
		require.True(t, ok)
		var hcp, ncp BackfillParams
		require.NoError(t, json.Unmarshal(rep.lastCheckpoint(t), &hcp))
		require.NoError(t, json.Unmarshal(nraw, &ncp))
		require.Equal(t, hcp, ncp)
		require.Equal(t, "book-00003", ncp.AfterBookID)
		require.JSONEq(t, string(rep.lastCheckpoint(t)), string(nraw), "byte-compatible with what HEAD wrote")
	})

	t.Run("a garbled checkpoint is a fresh start in both shells", func(t *testing.T) {
		garbled := json.RawMessage(`{"after_book_id": 5}`)

		hf := backfillFixture(t, n)
		hrep := &coreReporter{}
		_, err := backfillCore(context.Background(), garbled, headBackfillEnvFor(&Plugin{store: hf.store}, hrep))
		require.NoError(t, err)
		require.Contains(t, hrep.logs.String(), "failed to unmarshal checkpoint")

		nf := backfillFixture(t, n)
		nf.store.UpdateBookFileFunc = func(string, *database.BookFile) error { return nil }
		k := newKit()
		d := byKind(t, (&Plugin{}).Kinds(k.deps(nf.store), nil), backfillKind)
		h := newKindHost(context.Background(), d, tasks.ModeLive, &sentinelWriter{})
		require.NoError(t, h.SetCheckpoint("", map[string]any{"after_book_id": 5}))
		_, nerr := runKindResult(t, d, h, ``)
		require.NoError(t, nerr)
		require.Contains(t, h.logged(), "failed to restore checkpoint")

		require.Len(t, visited(hf), n)
		require.Equal(t, visited(hf), visited(nf))
	})

	t.Run("no checkpoint is a fresh start", func(t *testing.T) {
		nf := backfillFixture(t, n)
		nf.store.UpdateBookFileFunc = func(string, *database.BookFile) error { return nil }
		_, _, err := nativeRun(t, newKit(), nf.store, backfillKind, ``)
		require.NoError(t, err)
		require.Len(t, visited(nf), n)
	})
}

// ---- fingerprint-rescan ----

func rescanFixture(t *testing.T) *database.MockStore {
	t.Helper()
	stubFingerprint(t)
	good := audioFile(t, "The Court of Owls")
	s := guardedStore(t)
	s.GetBookByIDFunc = func(id string) (*database.Book, error) {
		return &database.Book{ID: id, Title: "The Court of Owls"}, nil
	}
	s.GetBookFilesFunc = func(string) ([]database.BookFile, error) {
		return []database.BookFile{
			{ID: "f-owls", BookID: "b-owls", FilePath: good, Format: "m4b"},
			{ID: "f-cover", BookID: "b-owls", FilePath: "/library/Court of Owls/cover.jpg", Format: "jpg"},
			{ID: "f-skip", BookID: "b-owls", FilePath: good, Format: "m4b", SkipScan: true},
		}, nil
	}
	return s
}

func TestAcoustidCore_RescanMatchesHEAD(t *testing.T) {
	const params = `{"scope":"books","book_ids":["b-owls"],"force":true}`

	var hreported []ineligibleEntry
	hooks := fakeFingerprintDeps()
	hooks.WriteIneligibleReport = func(_ string, e []ineligibleEntry) (string, error) {
		hreported = append(hreported, e...)
		return "", nil
	}

	hs := rescanFixture(t)
	var hl writeLog
	recordWrites(hs, &hl)
	henv := (&Plugin{store: hs}).headFingerprintRescanEnv(&coreReporter{})
	henv.Host = hooks.hooks()
	hres, err := fingerprintRescanCore(context.Background(), json.RawMessage(params), henv)
	require.NoError(t, err)

	var nreported []ineligibleEntry
	nhooks := fakeFingerprintDeps()
	nhooks.WriteIneligibleReport = func(_ string, e []ineligibleEntry) (string, error) {
		nreported = append(nreported, e...)
		return "", nil
	}
	k := newKit()
	d := k.deps(rescanFixture(t))
	d.Fingerprint = nhooks
	def := byKind(t, (&Plugin{}).Kinds(d, nil), fingerprintRescanKind)
	nh := newKindHost(context.Background(), def, tasks.ModeLive, &sentinelWriter{})
	nres, nerr := runKindResult(t, def, nh, params)
	require.NoError(t, nerr)

	requireResult(t, hres, nres)
	require.Equal(t, hl.sorted(), sortedCalls(k.rec))
	require.Equal(t, []string{"UpdateBookFile:f-owls"}, sortedCalls(k.rec))
	// The two runs write the report from different temp dirs, so compare the
	// entries by everything but the directory.
	flat := func(es []ineligibleEntry) []string {
		out := make([]string, 0, len(es))
		for _, e := range es {
			out = append(out, fmt.Sprintf("%s|%s|%s|%s", e.BookID, e.BookTitle, e.Reason, filepath.Base(e.FilePath)))
		}
		return out
	}
	require.ElementsMatch(t, flat(hreported), flat(nreported))
	require.Len(t, nreported, 2, "the cover and the skip_scan file are ineligible")
}

func TestAcoustidCore_RescanScopeRefusalsMatchHEAD(t *testing.T) {
	for _, tc := range []struct{ params, want string }{
		{`{"scope":"books"}`, `book_ids is required when scope is "books"`},
		{`{"scope":"nonsense"}`, `scope must be one of: missing, all, books`},
	} {
		t.Run(tc.params, func(t *testing.T) {
			henv := (&Plugin{store: guardedStore(t)}).headFingerprintRescanEnv(&coreReporter{})
			henv.Host = fakeFingerprintDeps().hooks()
			_, herr := fingerprintRescanCore(context.Background(), json.RawMessage(tc.params), henv)
			require.EqualError(t, herr, tc.want)
			_, _, nerr := nativeRun(t, newKit(), guardedStore(t), fingerprintRescanKind, tc.params)
			require.EqualError(t, nerr, tc.want)
		})
	}

	t.Run("HEAD params JSON is accepted by native", func(t *testing.T) {
		raw, err := json.Marshal(FingerprintRescanParams{Scope: "books", BookIDs: []string{"b-owls"}, Force: true})
		require.NoError(t, err)
		k := newKit()
		_, _, nerr := nativeRun(t, k, rescanFixture(t), fingerprintRescanKind, string(raw))
		require.NoError(t, nerr)
		require.Equal(t, []string{"UpdateBookFile:f-owls"}, sortedCalls(k.rec))
	})

	t.Run("malformed params: HEAD is lenient, native refuses", func(t *testing.T) {
		empty := newPagedFixture(0, 0, nil).store
		henv := (&Plugin{store: empty}).headFingerprintRescanEnv(&coreReporter{})
		henv.Host = fakeFingerprintDeps().hooks()
		_, herr := fingerprintRescanCore(context.Background(), json.RawMessage(`{"scope":5}`), henv)
		require.NoError(t, herr, "HEAD logs it and runs the default scope")
		_, _, nerr := nativeRun(t, newKit(), empty, fingerprintRescanKind, `{"scope":5}`)
		require.ErrorContains(t, nerr, "decode params")
	})

	t.Run("host refusals come first", func(t *testing.T) {
		k := newKit()
		d := k.deps(rescanFixture(t))
		d.Fingerprint.ServerDecodeCheck = func(op string) error { return fmt.Errorf("no decoding on the server: %s", op) }
		def := byKind(t, (&Plugin{}).Kinds(d, nil), fingerprintRescanKind)
		_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, `{"scope":"all"}`)
		require.EqualError(t, err, "no decoding on the server: acoustid.fingerprint-rescan")

		d = k.deps(rescanFixture(t))
		d.Fingerprint.FingerprintAvailable = func() bool { return false }
		def = byKind(t, (&Plugin{}).Kinds(d, nil), fingerprintRescanKind)
		_, err = runKind(t, def, tasks.ModeLive, &sentinelWriter{}, `{"scope":"all"}`)
		require.EqualError(t, err, "no fingerprint backend (fpcalc / ffmpeg) found")
		require.Empty(t, k.rec.logged())
	})
}

// ---- reset-all ----

func resetFixture() *database.MockStore {
	return &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			return []database.BookFileCore{{ID: "f-1", BookID: "b-1"}, {ID: "f-2", BookID: "b-1"}, {ID: "f-3", BookID: "b-2"}}, nil
		},
		GetBookFileByIDFunc: func(_, id string) (*database.BookFile, error) {
			if id == "f-3" {
				return &database.BookFile{ID: id, BookID: "b-2"}, nil // nothing to clear
			}
			return &database.BookFile{ID: id, BookID: "b-1", AcoustIDSeg0: "seg-0", AcoustIDSeg3: "seg-3"}, nil
		},
	}
}

// pageCands serves candidates one page and then an empty one, and records deletes.
type pageCands struct {
	mu      sync.Mutex
	served  bool
	cands   []database.DedupCandidate
	deleted *writeLog
}

func (c *pageCands) ListCandidates(database.CandidateFilter) ([]database.DedupCandidate, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.served {
		return nil, 0, nil
	}
	c.served = true
	return c.cands, len(c.cands), nil
}

func (c *pageCands) DeleteCandidates(ids []int64) (int, error) {
	if c.deleted != nil {
		c.deleted.add("DeleteCandidates:%d", len(ids))
	}
	return len(ids), nil
}

func resetCands(l *writeLog) *pageCands {
	return &pageCands{deleted: l, cands: []database.DedupCandidate{
		{ID: 1}, {ID: 2}, {ID: 3, Source: database.CandidateSourceManual},
	}}
}

func TestAcoustidCore_ResetSlowPathMatchesHEAD(t *testing.T) {
	var hl writeLog
	hs := resetFixture()
	recordWrites(hs, &hl)
	henv := (&Plugin{store: hs, resetCandidates: resetCands(&hl)}).headResetAllEnv(&coreReporter{})
	hres, err := resetAllCore(context.Background(), henv)
	require.NoError(t, err)

	k := newKit()
	k.rec.resetter = nil // a backend without the batched wipe: the per-row fallback
	d := k.deps(resetFixture())
	d.Candidates = resetCands(nil)
	def := byKind(t, (&Plugin{}).Kinds(d, nil), resetAllKind)
	h := newKindHost(context.Background(), def, tasks.ModeLive, &sentinelWriter{})
	nres, nerr := runKindResult(t, def, h, ``)
	require.NoError(t, nerr)

	requireResult(t, hres, nres)
	requireResult(t, resetAllResult{Cleared: 2, CandidatesDeleted: 2}, nres, "the manual candidate is kept")
	require.Equal(t, []string{"DeleteCandidates:2", "UpdateBookFile:f-1", "UpdateBookFile:f-2"}, hl.sorted())
	require.Equal(t, hl.sorted(), sortedCalls(k.rec))
}

func TestAcoustidCore_ResetFastPathUsesTheWritersResetter(t *testing.T) {
	k := newKit()
	d := k.deps(resetFixture()) // the read store has rows, but the fast path never reads them
	d.Candidates = resetCands(nil)
	def := byKind(t, (&Plugin{}).Kinds(d, nil), resetAllKind)
	h := newKindHost(context.Background(), def, tasks.ModeLive, &sentinelWriter{})
	res, err := runKindResult(t, def, h, ``)
	require.NoError(t, err)
	requireResult(t, resetAllResult{Cleared: 3, CandidatesDeleted: 2}, res)
	require.Equal(t, []string{"ClearAllAcoustIDFingerprints", "DeleteCandidates:2"}, k.rec.logged())
	bars := h.bars()
	require.NotEmpty(t, bars)
	// 3 rows cleared + 2 candidates deleted: the bar is rebuilt with that real
	// total once the wipe has reported it.
	require.True(t, slices.ContainsFunc(bars, func(b barFrame) bool { return b.Known && b.Total == 5 }),
		"the bar was rebuilt with the real total once the wipe reported it: %v", bars)
}

// ---- lsh-backfill ----

func lshFixture() *database.MockStore {
	s := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			return []database.BookFileCore{
				{ID: "f-a", BookID: "b-a", AcoustIDFingerprintDurationSec: 600},
				{ID: "f-b", BookID: "b-b"}, // no whole-file print
				{ID: "f-c", BookID: "b-c", AcoustIDFingerprintDurationSec: 900},
				{ID: "f-gone", BookID: "b-d", AcoustIDFingerprintDurationSec: 900},
			}, nil
		},
		GetBookFileByIDFunc: func(_, id string) (*database.BookFile, error) {
			if id == "f-gone" {
				return nil, nil
			}
			return &database.BookFile{ID: id, AcoustIDFingerprint: []byte{1, 2, 3, 4}}, nil
		},
	}
	return s
}

func TestAcoustidCore_LSHMatchesHEAD(t *testing.T) {
	var hl writeLog
	hs := lshFixture()
	recordWrites(hs, &hl)
	rep := &coreReporter{}
	hres, err := lshBackfillCore(context.Background(), (&Plugin{store: hs}).headLSHBackfillEnv(rep))
	require.NoError(t, err)

	k := newKit()
	h, nres, nerr := nativeRun(t, k, lshFixture(), lshBackfillKind, ``)
	require.NoError(t, nerr)

	requireResult(t, hres, nres)
	requireResult(t, lshBackfillResult{Total: 4, Indexed: 2, SkippedNoFingerprint: 1, Failed: 1}, nres)
	require.Equal(t, hl.sorted(), sortedCalls(k.rec))

	// The opening and closing frames HEAD's sdk.Progress sent are replicated
	// through the sink: loading with an unknown total, then the real total.
	bars := h.bars()
	require.NotEmpty(t, bars)
	hb, _ := headBarAndNotes(rep.frames)
	require.NotEmpty(t, hb)
	require.Equal(t, hb[0], bars[0], "the opening frame is the one HEAD's sdk.Progress sent")
	require.Equal(t, hb[len(hb)-1], bars[len(bars)-1], "and so is the closing frame")
	// The per-item frames (done/4) are the kernel's own item progress; a FakeRun
	// does not route them, so only the sdk.Progress frames are compared here.
	notes := h.allNotes()
	// HEAD's pool also sends one counter label per item ("LSH backfill 2/4
	// (indexed=...)"); the kernel's label is pure, so those are HEAD-only
	// (seams.d/F5-05.md). Every other status line is shared, percent suffix included.
	var headShared []string
	for _, n := range headNotes(rep.frames) {
		if !strings.HasPrefix(n, "LSH backfill ") || strings.HasPrefix(n, "LSH backfill complete") {
			headShared = append(headShared, n)
		}
	}
	require.Equal(t, headShared, notes)
	require.Contains(t, strings.Join(notes, "\n"), "LSH backfill complete: indexed=2 skipped_no_fp=1 skipped_existing=0 failed=1")
}

func headNotes(frames []coreFrame) []string {
	_, notes := headBarAndNotes(frames)
	return notes
}

type lshChecked struct {
	*database.MockStore
	have map[string]bool
}

func (s lshChecked) HasLSHIndex(id string) bool { return s.have[id] }

func TestAcoustidCore_LSHCapabilityAndCancelGuards(t *testing.T) {
	t.Run("an already indexed row is skipped through the capability", func(t *testing.T) {
		k := newKit()
		store := lshChecked{MockStore: lshFixture(), have: map[string]bool{"f-a": true}}
		_, res, err := nativeRun(t, k, store, lshBackfillKind, ``)
		require.NoError(t, err)
		requireResult(t, lshBackfillResult{Total: 4, Indexed: 1, SkippedNoFingerprint: 1, SkippedAlreadyIndexed: 1, Failed: 1}, res)
		require.Equal(t, []string{"UpdateBookFile:f-c"}, k.rec.logged())
	})

	t.Run("a canceled run writes nothing and reports the cancel", func(t *testing.T) {
		k := newKit()
		d := byKind(t, (&Plugin{}).Kinds(k.deps(lshFixture()), nil), lshBackfillKind)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		h := newKindHost(ctx, d, tasks.ModeLive, &sentinelWriter{})
		_, err := runKindResult(t, d, h, ``)
		require.ErrorIs(t, err, context.Canceled)
		require.Empty(t, k.rec.logged(), "no item started a write on a dead ctx")
	})

	t.Run("a cancel during the last item is still reported", func(t *testing.T) {
		// A pool can finish its last item without re-checking ctx; the core's
		// post-loop check is what surfaces the cancel.
		ctx, cancel := context.WithCancel(context.Background())
		var done atomic.Bool
		s := &database.MockStore{
			GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
				return []database.BookFileCore{{ID: "f-only", BookID: "b", AcoustIDFingerprintDurationSec: 600}}, nil
			},
			GetBookFileByIDFunc: func(_, id string) (*database.BookFile, error) {
				return &database.BookFile{ID: id}, nil
			},
		}
		env := lshBackfillEnv{kindEnv: headEnv(&coreReporter{}, true), Store: s, Writer: cancelingWriter{cancel: cancel, done: &done}}
		_, err := lshBackfillCore(ctx, env)
		require.True(t, done.Load())
		require.ErrorIs(t, err, context.Canceled)
	})
}

// cancelingWriter cancels the run from inside its one write.
type cancelingWriter struct {
	headKindWriter
	cancel func()
	done   *atomic.Bool
}

func (w cancelingWriter) UpdateBookFile(string, *database.BookFile) error {
	w.done.Store(true)
	w.cancel()
	return nil
}

// ---- lookup-online ----

func onlineStore(t *testing.T, log *writeLog) *database.MockStore {
	t.Helper()
	s := guardedStore(t)
	if log != nil {
		recordWrites(s, log)
	}
	s.GetAllBookFilesCoreFunc = func() ([]database.BookFileCore, error) {
		cur := fingerprint.PrintEncodingVersion
		stamped := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
		return []database.BookFileCore{
			{ID: "f-hush", BookID: "b-hush", AcoustIDFingerprintDurationSec: 1800, AcoustIDFPVersion: cur},
			{ID: "f-done", BookID: "b-done", AcoustIDFingerprintDurationSec: 1800, AcoustIDFPVersion: cur, AcoustIDOnlineLookedUpAt: &stamped},
			{ID: "f-short", BookID: "b-short", AcoustIDFingerprintDurationSec: 10, AcoustIDFPVersion: cur}, // under 30s
			{ID: "f-none", BookID: "b-none"},                                           // no print
			{ID: "f-legacy", BookID: "b-legacy", AcoustIDFingerprintDurationSec: 1800}, // legacy era
		}, nil
	}
	s.GetBookFilesFunc = func(string) ([]database.BookFile, error) {
		return []database.BookFile{{ID: "f-hush", BookID: "b-hush", AcoustIDFingerprint: []byte{1, 2, 3, 4, 5, 6, 7, 8}}}, nil
	}
	return s
}

func TestAcoustidCore_OnlineLookupMatchesHEAD(t *testing.T) {
	var hl writeLog
	rep := &coreReporter{}
	henv := (&Plugin{store: onlineStore(t, &hl)}).headOnlineLookupEnv(rep)
	henv.Lookup = newKit().deps(nil).Lookup.hooks()
	hres, err := onlineLookupCore(context.Background(), nil, henv)
	require.NoError(t, err)

	k := newKit()
	h, nres, nerr := nativeRun(t, k, onlineStore(t, nil), onlineLookupKind, ``)
	require.NoError(t, nerr)

	requireResult(t, hres, nres)
	requireResult(t, onlineLookupResult{Total: 1, Matched: 1, NeedsRefingerprint: 1}, nres)
	require.Equal(t, hl.sorted(), sortedCalls(k.rec))
	bars, notes := headBarAndNotes(rep.frames)
	require.Equal(t, bars, h.bars())
	require.Equal(t, notes, h.allNotes())
}

func TestAcoustidCore_OnlineLookupParamsAndRefusals(t *testing.T) {
	t.Run("limit and force are HEAD's params", func(t *testing.T) {
		k := newKit()
		_, res, err := nativeRun(t, k, onlineStore(t, nil), onlineLookupKind, `{"limit":0,"force":true}`)
		require.NoError(t, err)
		require.Equal(t, 2, resultOf[onlineLookupResult](t, res).Total, "force re-queries the stamped file too; limit 0 is no limit")
	})
	t.Run("no api key is HEAD's refusal", func(t *testing.T) {
		k := newKit()
		d := k.deps(onlineStore(t, nil))
		d.Lookup.APIKey = func() string { return "" }
		def := byKind(t, (&Plugin{}).Kinds(d, nil), onlineLookupKind)
		_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, ``)
		require.ErrorContains(t, err, "AcoustID API key is not configured")
		require.Empty(t, k.rec.logged())
	})
	t.Run("a rate limit widens the throttle and counts", func(t *testing.T) {
		k := newKit()
		d := k.deps(onlineStore(t, nil))
		var waits []int64
		d.Lookup.NewLookup = func(string) LookupFunc {
			return func(context.Context, string, int) (acoustid.LookupResult, error) {
				return acoustid.LookupResult{}, acoustid.ErrRateLimited
			}
		}
		d.Lookup.Wait = func(_ context.Context, w time.Duration) error { waits = append(waits, w.Milliseconds()); return nil }
		def := byKind(t, (&Plugin{}).Kinds(d, nil), onlineLookupKind)
		h := newKindHost(context.Background(), def, tasks.ModeLive, &sentinelWriter{})
		res, err := runKindResult(t, def, h, ``)
		require.NoError(t, err)
		requireResult(t, onlineLookupResult{Total: 1, Failed: 1, RateLimited: 1, NeedsRefingerprint: 1}, res)
		require.Equal(t, []int64{onlineLookupThrottleMax.Milliseconds()}, waits)
		require.Empty(t, k.rec.logged(), "a failed lookup writes nothing")
	})
}

// ---- duration-repair ----

func durationFixture(t *testing.T) (*database.MockStore, string) {
	t.Helper()
	stubFingerprint(t)
	path := audioFile(t, "Batman Year Two")
	s := guardedStore(t)
	s.GetFilesWithZeroDurationFingerprintFunc = func(int, int) ([]database.BookFile, int64, error) {
		return []database.BookFile{{ID: "f-y2", BookID: "b-y2", FilePath: path, Format: "m4b", AcoustIDFingerprint: []byte{1, 2, 3, 4}}}, 1, nil
	}
	return s, path
}

// TestAcoustidCore_DurationRepairLiveInversion: HEAD's `live` param (default
// false: a dry run) is the native mode (Preview is the dry run), and neither mode
// takes `live` from params any more.
func TestAcoustidCore_DurationRepairLiveInversion(t *testing.T) {
	for _, tc := range []struct {
		name       string
		headParams string
		mode       tasks.Mode
		want       durationRepairResult
		writes     []string
	}{
		{"dry run", ``, tasks.ModePreview, durationRepairResult{Affected: 1}, nil},
		{"explicit live:false", `{"live":false}`, tasks.ModePreview, durationRepairResult{Affected: 1}, nil},
		{"live", `{"live":true}`, tasks.ModeLive, durationRepairResult{Affected: 1, Fixed: 1}, []string{"UpdateBookFile:f-y2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hs, _ := durationFixture(t)
			var hl writeLog
			recordWrites(hs, &hl)
			var raw json.RawMessage
			if tc.headParams != "" {
				raw = json.RawMessage(tc.headParams)
			}
			p := &Plugin{store: hs}
			require.NoError(t, p.runDurationBackfill(context.Background(), raw, &coreReporter{}))
			require.Equal(t, tc.writes, hl.sorted())

			ns, _ := durationFixture(t)
			k := newKit()
			d := byKind(t, (&Plugin{}).Kinds(k.deps(ns), nil), durationRepairKind)
			h := newKindHost(context.Background(), d, tc.mode, &sentinelWriter{})
			res, err := runKindResult(t, d, h, ``)
			require.NoError(t, err)
			requireResult(t, tc.want, res)
			require.Equal(t, tc.writes, sortedCalls(k.rec))
			if tc.mode == tasks.ModePreview {
				require.Empty(t, k.handed(), "a dry run builds no writer")
			}
		})
	}
}

func TestAcoustidCore_DurationRepairReferenceToolsRefusal(t *testing.T) {
	const want = "fingerprint_window_reference_tools is set, so audio is decoded by remote workers only"
	hooks := fakeFingerprintDeps()
	hooks.ReferenceToolsSet = func() bool { return true }

	// HEAD's core, live: refused.
	hs, _ := durationFixture(t)
	env := durationRepairEnv{kindEnv: headEnv(&coreReporter{}, true), Store: hs, Host: hooks.hooks(), Writer: headKindWriter{store: hs}}
	_, herr := durationRepairCore(context.Background(), env)
	require.ErrorContains(t, herr, want)

	// Native Live: the same refusal, before any write.
	ns, _ := durationFixture(t)
	k := newKit()
	d := k.deps(ns)
	d.Fingerprint = hooks
	def := byKind(t, (&Plugin{}).Kinds(d, nil), durationRepairKind)
	_, nerr := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, ``)
	require.ErrorContains(t, nerr, want)
	require.Empty(t, k.rec.logged())

	// A dry run decodes nothing, so it survives the reference pair (HEAD's too).
	henv := durationRepairEnv{kindEnv: headEnv(&coreReporter{}, false), Store: hs, Host: hooks.hooks()}
	hres, derr := durationRepairCore(context.Background(), henv)
	require.NoError(t, derr)
	_, pres, perr := func() (*kindHost, any, error) {
		h := newKindHost(context.Background(), def, tasks.ModePreview, &sentinelWriter{})
		r, e := runKindResult(t, def, h, ``)
		return h, r, e
	}()
	require.NoError(t, perr)
	requireResult(t, hres, pres)
}

// ---- window-backfill ----

// windowKit is a kit over a wbEnv: the native kind reads the same store and
// resolves tools through the same fake wiring HEAD's shell does.
func windowKit(e *wbEnv) (*kit, tasks.Definition) {
	k := newKit()
	d := byKind(e.t, e.plugin.Kinds(k.deps(e.store), nil), windowBackfillKind)
	return k, d
}

func TestAcoustidCore_WindowMatchesHEAD(t *testing.T) {
	seed := func(e *wbEnv) {
		e.addFileWith("", "arkham/Batman Year One.m4b", fakeAudio(1800, 4096), 1800)
		e.addFileWith("", "gotham/The Long Halloween.m4b", fakeAudio(1200, 4096), 1200)
		e.addFile("", "gotham/missing.m4b", false, 600)
	}
	he := newWBEnv(t)
	seed(he)
	hres, err := he.run(context.Background(), nil, WindowBackfillParams{Live: true, Concurrency: 2})
	require.NoError(t, err)

	ne := newWBEnv(t)
	seed(ne)
	k, def := windowKit(ne)
	h := newKindHost(context.Background(), def, tasks.ModeLive, &sentinelWriter{})
	res, nerr := runKindResult(t, def, h, `{"concurrency":2}`)
	require.NoError(t, nerr)
	nres := resultOf[windowBackfillResult](t, res)

	require.Equal(t, hres.written, nres.Written)
	require.Equal(t, hres.failed, nres.Failed)
	require.Equal(t, hres.skipped, nres.Skipped)
	require.Equal(t, hres.writeErrors, nres.WriteErrors)
	require.EqualValues(t, 2, nres.Written)

	// HEAD wrote its windows to the store; native sent the same writes to the
	// writer and none to the store (the pebble store stays empty).
	var headFiles, nativeFiles int
	for _, c := range sortedCalls(k.rec) {
		if strings.HasPrefix(c, "ReplaceFingerprintWindows:") {
			nativeFiles++
		}
	}
	files, err := he.store.GetAllBookFilesCore()
	require.NoError(t, err)
	for _, f := range files {
		if len(he.windows(f.ID)) > 0 {
			headFiles++
		}
	}
	require.Equal(t, headFiles, nativeFiles)
	nfiles, err := ne.store.GetAllBookFilesCore()
	require.NoError(t, err)
	for _, f := range nfiles {
		require.Empty(t, ne.windows(f.ID), "the native run wrote through the writer, not the store")
	}
}

func TestAcoustidCore_WindowPreviewIsADryRun(t *testing.T) {
	e := newWBEnv(t)
	e.addFileWith("", "arkham/Batman Year One.m4b", fakeAudio(1800, 4096), 1800)
	k, def := windowKit(e)
	h := newKindHost(context.Background(), def, tasks.ModePreview, &sentinelWriter{})
	res, err := runKindResult(t, def, h, ``)
	require.NoError(t, err)
	require.NotEmpty(t, resultOf[windowBackfillResult](t, res).Plan, "the plan summary is the dry run's report")
	require.Zero(t, resultOf[windowBackfillResult](t, res).Written)
	require.Empty(t, k.handed())
	require.Empty(t, e.invoked(), "a dry run decodes nothing")
}

// TestAcoustidCore_WindowRestoresHEADCheckpointBytes: HEAD's checkpoint is its
// WindowBackfillParams with a Resume cursor; the native Restore takes only the
// cursor and the start request supplies the settings.
func TestAcoustidCore_WindowRestoresHEADCheckpointBytes(t *testing.T) {
	e := newWBEnv(t)
	var ids []string
	for i := range 3 {
		_, id := e.addFileWith("", fmt.Sprintf("gotham/Year-%d.m4b", i), fakeAudio(600, 4096), 600)
		ids = append(ids, id)
	}
	slices.Sort(ids)

	written := func(k *kit) []string {
		var out []string
		for _, c := range k.rec.logged() {
			for _, id := range ids {
				if strings.HasPrefix(c, "ReplaceFingerprintWindows:") && strings.Contains(c, id) {
					out = append(out, id)
				}
			}
		}
		slices.Sort(out)
		return out
	}

	t.Run("cursor inside the tier", func(t *testing.T) {
		k, def := windowKit(e)
		h := newKindHost(context.Background(), def, tasks.ModeLive, &sentinelWriter{})
		// The bytes HEAD wrote: the caller's params (live, concurrency) plus the cursor.
		require.NoError(t, h.SetCheckpoint("", WindowBackfillParams{
			Live: true, Concurrency: 7, Resume: &WindowBackfillCursor{Tier: windowTierPresent, AfterFileID: ids[1]},
		}))
		_, err := runKindResult(t, def, h, ``)
		require.NoError(t, err)
		require.Equal(t, []string{ids[2]}, written(k), "only files after the cursor are cut")

		raw, ok := h.CheckpointJSON("")
		require.True(t, ok)
		var cp WindowBackfillParams
		require.NoError(t, json.Unmarshal(raw, &cp))
		require.True(t, cp.Live, "the checkpoint carries live:true as HEAD's did")
		require.NotNil(t, cp.Resume)
		require.Equal(t, windowTierCount, cp.Resume.Tier, "the last checkpoint is past the final tier")
	})
	t.Run("cursor past every tier", func(t *testing.T) {
		k, def := windowKit(e)
		h := newKindHost(context.Background(), def, tasks.ModeLive, &sentinelWriter{})
		require.NoError(t, h.SetCheckpoint("", WindowBackfillParams{Live: true, Resume: &WindowBackfillCursor{Tier: windowTierCount}}))
		_, err := runKindResult(t, def, h, ``)
		require.NoError(t, err)
		require.Empty(t, written(k))
	})
	t.Run("no checkpoint cuts everything", func(t *testing.T) {
		k, def := windowKit(e)
		h := newKindHost(context.Background(), def, tasks.ModeLive, &sentinelWriter{})
		_, err := runKindResult(t, def, h, ``)
		require.NoError(t, err)
		require.Equal(t, ids, written(k))
	})
	t.Run("a garbled checkpoint fails closed, as HEAD's params decode does", func(t *testing.T) {
		k, def := windowKit(e)
		h := newKindHost(context.Background(), def, tasks.ModeLive, &sentinelWriter{})
		require.NoError(t, h.SetCheckpoint("", map[string]any{"resume": "not a cursor"}))
		_, err := runKindResult(t, def, h, ``)
		require.ErrorContains(t, err, "window-backfill params")
		require.Empty(t, k.handed())
	})
}

func TestAcoustidCore_WindowHooksFillHEADDefaults(t *testing.T) {
	p := &Plugin{}
	h := p.windowHooks(WindowKindDeps{})
	require.NotNil(t, h.Hub)
	require.NotNil(t, h.Probe)
	require.NotNil(t, h.Tools)
	require.NotNil(t, h.Workers)
	require.NotNil(t, h.ReferenceToolsSet)
	require.NotNil(t, h.SetToolEquivalence)
	require.NotNil(t, h.Roots)
	require.NotNil(t, h.ProbeDuration)
	require.NotNil(t, h.Hostname)

	var seen []string
	over := p.windowHooks(WindowKindDeps{
		Workers:           func(n int) int { seen = append(seen, "workers"); return n + 100 },
		ReferenceToolsSet: func() bool { seen = append(seen, "ref"); return true },
		Hostname:          func() (string, error) { return "wayne-manor", nil },
	})
	require.Equal(t, 104, over.Workers(4))
	require.True(t, over.ReferenceToolsSet())
	name, err := over.Hostname()
	require.NoError(t, err)
	require.Equal(t, "wayne-manor", name)
	require.Equal(t, []string{"workers", "ref"}, seen)
}

func TestAcoustidCore_WindowReferenceRefusalReachesThroughHooks(t *testing.T) {
	e := newWBEnv(t)
	e.addFileWith("", "arkham/Batman Year One.m4b", fakeAudio(1800, 4096), 1800)
	k := newKit()
	d := k.deps(e.store)
	d.Window.ReferenceToolsSet = func() bool { return true }
	def := byKind(t, e.plugin.Kinds(d, nil), windowBackfillKind)
	_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, ``)
	require.Error(t, err, "a live run with a reference pair and no remote_only/allow_server_decode is refused")
	require.Empty(t, k.rec.logged())
	require.Empty(t, e.invoked())
}

func TestAcoustidCore_WindowParallelismIsOneToOne(t *testing.T) {
	require.Equal(t, 6, windowParallelism(6).Workers())
	require.Equal(t, 32, windowWorkers(500), "the param is clamped to 1..32 by HEAD's own resolver")
	require.Equal(t, 32, windowParallelism(windowWorkers(500)).Workers())
	require.Equal(t, 1, windowParallelism(0).Workers())
	require.Equal(t, windowPlanWorkers, windowPlanParallelism().Workers())

	o := registryItemsOptions(windowParallelism(6), kindItemsOptions{})
	require.Equal(t, 6, o.Concurrency)
}

// ---- additions: gates, hooks and guards the parity runs do not reach ----

func TestAcoustidCore_BackfillNeedsTheEngineFactory(t *testing.T) {
	fx := backfillFixture(t, 1)
	k := newKit()
	d := k.deps(fx.store)
	d.NewEngine = nil
	def := byKind(t, (&Plugin{}).Kinds(d, nil), backfillKind)
	_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, ``)
	require.EqualError(t, err, "dedup engine not available", "HEAD's gate: the backfill needs a dedup engine to exist")
	require.Empty(t, k.rec.logged())
}

func TestAcoustidCore_HostWorkersHookSizesTheFingerprintPools(t *testing.T) {
	var asked atomic.Int64
	workers := func() int { asked.Add(1); return 3 }

	bf := backfillFixture(t, 1)
	bf.store.UpdateBookFileFunc = func(string, *database.BookFile) error { return nil }
	k := newKit()
	d := k.deps(bf.store)
	d.Fingerprint.Workers = workers
	_, err := runKind(t, byKind(t, (&Plugin{}).Kinds(d, nil), backfillKind), tasks.ModeLive, &sentinelWriter{}, ``)
	require.NoError(t, err)
	require.Positive(t, asked.Load(), "backfill sizes its pool from the hook")

	asked.Store(0)
	d = newKit().deps(rescanFixture(t))
	d.Fingerprint.Workers = workers
	_, err = runKind(t, byKind(t, (&Plugin{}).Kinds(d, nil), fingerprintRescanKind), tasks.ModeLive, &sentinelWriter{}, `{"scope":"books","book_ids":["b-owls"]}`)
	require.NoError(t, err)
	require.Positive(t, asked.Load(), "rescan sizes its pool from the hook")

	asked.Store(0)
	ds, _ := durationFixture(t)
	d = newKit().deps(ds)
	d.Fingerprint.Workers = workers
	_, err = runKind(t, byKind(t, (&Plugin{}).Kinds(d, nil), durationRepairKind), tasks.ModeLive, &sentinelWriter{}, ``)
	require.NoError(t, err)
	require.Positive(t, asked.Load(), "duration repair sizes its pool from the hook")
}

// failWindowWriter fails the window writes the way a store failure would.
type failWindowWriter struct{ *recWriter }

func (failWindowWriter) ReplaceFingerprintWindows(database.FingerprintWindowRef, []database.FingerprintWindow) error {
	return fmt.Errorf("pebble: disk is full")
}

func (failWindowWriter) RecordFingerprintWindowFailure(*database.FingerprintWindowFailure) error {
	return fmt.Errorf("pebble: disk is full")
}

func TestAcoustidCore_WindowWriteErrorsFailTheRun(t *testing.T) {
	e := newWBEnv(t)
	e.addFileWith("", "arkham/Batman Year One.m4b", fakeAudio(1800, 4096), 1800)
	k := newKit()
	d := k.deps(e.store)
	d.NewWriter = func(tasks.Writer) kindWriter { return failWindowWriter{k.rec} }
	def := byKind(t, e.plugin.Kinds(d, nil), windowBackfillKind)
	_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, ``)
	require.ErrorContains(t, err, "window-backfill: 1 file(s) could not be written; see log")
}

// TestAcoustidCore_LSHPerItemGuard: HEAD's pool can hand an item a dead ctx (its
// dispatcher picks at random when a slot is free and ctx is done), so the core
// refuses to start a write on one and reports the cancel after the pool returns.
func TestAcoustidCore_LSHPerItemGuard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var writes atomic.Int64
	env := lshBackfillEnv{
		kindEnv: headEnv(&coreReporter{}, true),
		Store:   lshFixture(),
		Writer:  countingWriter{writes: &writes},
	}
	// A pool that runs every item regardless of ctx, as HEAD's can.
	env.Items = func(ctx context.Context, _ tasks.Parallelism, n int, _ kindItemsOptions, fn func(context.Context, int) error) error {
		for i := range n {
			_ = fn(ctx, i)
		}
		return nil
	}
	_, err := lshBackfillCore(ctx, env)
	require.ErrorIs(t, err, context.Canceled, "the cancel is reported once, after the pool returns")
	require.Zero(t, writes.Load(), "no item started a write on a dead ctx")
}

type countingWriter struct {
	headKindWriter
	writes *atomic.Int64
}

func (w countingWriter) UpdateBookFile(string, *database.BookFile) error {
	w.writes.Add(1)
	return nil
}

func TestAcoustidCore_OnlineLookupForceAndLimit(t *testing.T) {
	run := func(params string) onlineLookupResult {
		k := newKit()
		_, res, err := nativeRun(t, k, onlineStore(t, nil), onlineLookupKind, params)
		require.NoError(t, err)
		return resultOf[onlineLookupResult](t, res)
	}
	require.Equal(t, onlineLookupResult{Total: 1, Matched: 1, NeedsRefingerprint: 1}, run(``), "an already looked-up file is skipped")
	forced := run(`{"force":true}`)
	require.Equal(t, 2, forced.Total, "force re-queries the stamped file too")
	require.Equal(t, 1, forced.Matched)
	require.Equal(t, 1, forced.Failed, "the stamped file has no raw bytes in this fixture: a hydrate drift, counted as failed")
	require.Equal(t, 1, run(`{"force":true,"limit":1}`).Total, "limit caps the candidates")
	require.Equal(t, 2, run(`{"force":true,"limit":5}`).Total, "a limit above the count changes nothing")
}

// ---- native results ----

// resultBytes is the JSON a native run returned. The executor stores a task's
// result as raw JSON, so a kind's Run hands back bytes, not its result struct.
func resultBytes(t *testing.T, res any) []byte {
	t.Helper()
	if v := reflect.ValueOf(res); v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
		return v.Bytes()
	}
	b, err := json.Marshal(res)
	require.NoError(t, err)
	return b
}

// requireResult asserts a native run's result carries exactly want's JSON.
func requireResult(t *testing.T, want, got any, msg ...any) {
	t.Helper()
	w, err := json.Marshal(want)
	require.NoError(t, err)
	require.JSONEq(t, string(w), string(resultBytes(t, got)), msg...)
}

// resultOf decodes a native run's result into T.
func resultOf[T any](t *testing.T, res any) T {
	t.Helper()
	var out T
	require.NoError(t, json.Unmarshal(resultBytes(t, res), &out))
	return out
}
