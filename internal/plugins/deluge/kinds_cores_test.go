// file: internal/plugins/deluge/kinds_cores_test.go
// version: 1.0.0
// guid: 5d0c1f64-7a3b-4e29-9a54-c8b6e2f47d10
// last-edited: 2026-11-19

package deluge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

// --- deluge.centralize ------------------------------------------------------------

// cenFixture is one centralize scenario's rows and directories, built twice (one
// per shell) so the two runs never see each other's writes.
type cenFixture struct {
	root    string
	srcDir  string
	rows    []database.BookFile
	missing map[string]bool // ids GetBookFileByID cannot find
	hydrate error
	updErr  error
}

func newCenFixture(t *testing.T, names ...string) *cenFixture {
	t.Helper()
	f := &cenFixture{root: t.TempDir(), srcDir: t.TempDir(), missing: map[string]bool{}}
	for i, n := range names {
		p := filepath.Join(f.srcDir, n)
		require.NoError(t, os.WriteFile(p, []byte("bytes of "+n), 0o644))
		f.rows = append(f.rows, database.BookFile{ID: "bf-" + n, BookID: "book-" + n[:1], FilePath: p, DelugeHash: linkTorrentID(100 + i)})
	}
	return f
}

// cenOutcome is what one shell of centralize did.
type cenOutcome struct {
	err     string
	final   string
	updated map[string]string // row id -> new path
	marked  []string
	dest    map[string]string // library file -> content
	totals  []int             // positive progress denominators reported
}

type cenStore struct {
	f       *cenFixture
	mu      sync.Mutex
	updated map[string]string
	marked  []string
}

func (s *cenStore) mock() *database.MockStore {
	return &database.MockStore{
		GetBookFilesNeedingDelugeImportCoreFunc: func() ([]database.BookFileCore, error) {
			out := make([]database.BookFileCore, 0, len(s.f.rows))
			for _, r := range s.f.rows {
				out = append(out, r.Core())
			}
			return out, nil
		},
		GetBookFileByIDFunc: func(_, id string) (*database.BookFile, error) {
			if s.f.hydrate != nil {
				return nil, s.f.hydrate
			}
			for _, r := range s.f.rows {
				if r.ID == id && !s.f.missing[id] {
					cp := r
					return &cp, nil
				}
			}
			return nil, nil
		},
		UpdateBookFileFunc: func(id string, file *database.BookFile) error {
			if s.f.updErr != nil {
				return s.f.updErr
			}
			s.mu.Lock()
			s.updated[id] = file.FilePath
			s.mu.Unlock()
			return nil
		},
		ModifyBookFileFunc: func(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
			for _, r := range s.f.rows {
				if r.ID == fileID {
					cp := r
					if err := fn(&cp); err != nil {
						return &r, nil
					}
					s.mu.Lock()
					s.marked = append(s.marked, fileID)
					s.mu.Unlock()
					return &cp, nil
				}
			}
			return nil, errors.New("no such row")
		},
	}
}

func newCenStore(f *cenFixture) *cenStore { return &cenStore{f: f, updated: map[string]string{}} }

func (s *cenStore) outcome(err error, final string) cenOutcome {
	dest := map[string]string{}
	entries, _ := os.ReadDir(s.f.root)
	for _, e := range entries {
		if b, rerr := os.ReadFile(filepath.Join(s.f.root, e.Name())); rerr == nil {
			dest[e.Name()] = string(b)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	upd := map[string]string{}
	for k, v := range s.updated {
		upd[k] = filepath.Base(v)
	}
	m := append([]string(nil), s.marked...)
	return cenOutcome{err: errText(err), final: final, updated: upd, marked: m, dest: dest}
}

func runCentralizeHEAD(t *testing.T, f *cenFixture, protected []string) cenOutcome {
	t.Helper()
	setGlobals(t, f.root, "lbl", false, protected)
	st := newCenStore(f)
	rep := &parityReporter{}
	err := (&Plugin{store: st.mock()}).runCentralization(context.Background(), nil, rep)
	out := st.outcome(err, rep.lastMsg())
	out.totals = rep.totals()
	return out
}

func runCentralizeNative(t *testing.T, f *cenFixture, protected []string, mode tasks.Mode) (cenOutcome, *kindHost, *recWriter) {
	t.Helper()
	poisonGlobals(t)
	st := newCenStore(f)
	m := st.mock()
	w := &recWriter{modify: m.ModifyBookFileFunc, update: m.UpdateBookFileFunc}
	deps := KindDeps{Store: guardedStoreOf(t, m), NewWriter: w.factory, Config: &config.Config{RootDir: f.root, ProtectedPaths: protected}}
	def := byKind(t, (&Plugin{}).Kinds(deps, nil), centralizeKind)
	h, _, err := runKindResult(t, def, mode, &sentinelWriter{}, "")
	return st.outcome(err, lastNote(h)), h, w
}

func TestDelugeCores_CentralizeParity(t *testing.T) {
	cases := []struct {
		name      string
		setup     func(t *testing.T) *cenFixture
		protected func(f *cenFixture) []string
		wantTail  string
		// wantTotals is the literal set of positive progress denominators: 2 is
		// the opening "Loading" frame (before the groups are known), then the
		// pool's unit is the group and sdk.Progress adds a start and a done step
		// around it, so N groups report against N+2. Both shells share the core,
		// so only a literal catches a drift of both.
		wantTotals []int
	}{
		{name: "copies a pending file into the library", setup: func(t *testing.T) *cenFixture { return newCenFixture(t, "Year One.mp3") },
			wantTail: "1 succeeded, 0 already in library (marked imported), 0 protected (left pending), 0 left pending (Deluge protected list not loaded), 0 skipped, 0 errors", wantTotals: []int{2, 3}},
		{name: "files of one book are one group", setup: func(t *testing.T) *cenFixture {
			f := newCenFixture(t, "Year One 1.mp3", "Year One 2.mp3")
			f.rows[1].BookID = f.rows[0].BookID
			return f
		}, wantTail: "2 succeeded", wantTotals: []int{2, 3}},
		{name: "two books are two groups", setup: func(t *testing.T) *cenFixture { return newCenFixture(t, "Year One.mp3", "Hush.mp3") },
			wantTail: "2 succeeded", wantTotals: []int{2, 4}},
		{name: "a hydrate failure is counted and leaves no copy", setup: func(t *testing.T) *cenFixture {
			f := newCenFixture(t, "Year One.mp3")
			f.hydrate = errors.New("read failed")
			return f
		}, wantTail: "0 succeeded, 0 already in library (marked imported), 0 protected (left pending), 0 left pending (Deluge protected list not loaded), 0 skipped, 1 errors"},
		{name: "a missing row is counted as an error", setup: func(t *testing.T) *cenFixture {
			f := newCenFixture(t, "Year One.mp3")
			f.missing["bf-Year One.mp3"] = true
			return f
		}, wantTail: "1 errors"},
		{name: "a failed row update leaves no copy", setup: func(t *testing.T) *cenFixture {
			f := newCenFixture(t, "Year One.mp3")
			f.updErr = errors.New("write failed")
			return f
		}, wantTail: "1 errors"},
		{name: "an empty path is skipped", setup: func(t *testing.T) *cenFixture {
			f := newCenFixture(t, "Year One.mp3")
			f.rows[0].FilePath = ""
			return f
		}, wantTail: "1 skipped"},
		{name: "a static protected source is left pending", setup: func(t *testing.T) *cenFixture { return newCenFixture(t, "Year One.mp3") },
			protected: func(f *cenFixture) []string { return []string{f.srcDir} },
			wantTail:  "1 protected (left pending)"},
		{name: "a file already in the library is marked in place", setup: func(t *testing.T) *cenFixture {
			f := newCenFixture(t, "Year One.mp3")
			dst := filepath.Join(f.root, "Year One.mp3")
			require.NoError(t, os.WriteFile(dst, []byte("bytes"), 0o644))
			f.rows[0].FilePath = dst
			return f
		}, wantTail: "1 already in library (marked imported)"},
		{name: "an existing destination is never overwritten", setup: func(t *testing.T) *cenFixture {
			f := newCenFixture(t, "Year One.mp3")
			require.NoError(t, os.WriteFile(filepath.Join(f.root, "Year One.mp3"), []byte("another book's bytes"), 0o644))
			return f
		}, wantTail: "1 errors"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Each shell gets its own fixture, built the same way.
			var protectedHEAD, protectedNative []string
			fh := tc.setup(t)
			fn := tc.setup(t)
			if tc.protected != nil {
				protectedHEAD, protectedNative = tc.protected(fh), tc.protected(fn)
			}
			head := runCentralizeHEAD(t, fh, protectedHEAD)
			nat, h, _ := runCentralizeNative(t, fn, protectedNative, tasks.ModeLive)

			require.Contains(t, head.final, tc.wantTail, "HEAD's summary")
			require.Equal(t, head.err, nat.err)
			require.Equal(t, head.final, nat.final, "the native summary equals HEAD's")
			require.Equal(t, head.updated, nat.updated, "rows repointed")
			require.Equal(t, head.marked, nat.marked, "rows marked in place")
			require.Equal(t, head.dest, nat.dest, "library contents")
			// The pool reports against the same denominators in both.
			require.Equal(t, head.totals, nativeTotals(h), "progress denominators")
			if tc.wantTotals != nil {
				require.Equal(t, tc.wantTotals, head.totals, "literal denominators (groups+2)")
			}
		})
	}
}

// Nothing pending: both shells finish with the same message and no pool.
func TestDelugeCores_CentralizeNothingPending(t *testing.T) {
	f := &cenFixture{root: t.TempDir()}
	head := runCentralizeHEAD(t, f, nil)
	nat, _, w := runCentralizeNative(t, &cenFixture{root: t.TempDir()}, nil, tasks.ModeLive)
	require.Equal(t, "No files to centralize (100%)", head.final)
	require.Equal(t, head.final, nat.final)
	require.Empty(t, w.recorded())
}

// The pending-list read failing is the run's error, wrapped the same way.
func TestDelugeCores_CentralizeListError(t *testing.T) {
	setGlobals(t, t.TempDir(), "lbl", false, nil)
	m := &database.MockStore{GetBookFilesNeedingDelugeImportCoreFunc: func() ([]database.BookFileCore, error) { return nil, errors.New("scan failed") }}
	headErr := (&Plugin{store: m}).runCentralization(context.Background(), nil, &parityReporter{})
	require.EqualError(t, headErr, "load deluge-pending book files: scan failed")

	poisonGlobals(t)
	deps := KindDeps{Store: guardedStoreOf(t, m), NewWriter: (&recWriter{}).factory, Config: &config.Config{RootDir: t.TempDir()}}
	def := byKind(t, (&Plugin{}).Kinds(deps, nil), centralizeKind)
	_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.EqualError(t, err, headErr.Error())
}

// A native Preview run of centralize is refused before any read or write.
func TestDelugeCores_CentralizeRefusesPreview(t *testing.T) {
	f := newCenFixture(t, "Year One.mp3")
	nat, _, w := runCentralizeNative(t, f, nil, tasks.ModePreview)
	require.Contains(t, nat.err, "preview not supported")
	require.Empty(t, w.writers())
	require.Empty(t, nat.dest)
}

// The checkpoint is counts-only liveness: both shells write the same snapshots
// and decode as centralizationCheckpoint, and the final one covers every file.
func TestDelugeCores_CentralizeCheckpointParity(t *testing.T) {
	fh := newCenFixture(t, "Year One.mp3", "Hush.mp3", "Long Halloween.mp3")
	setGlobals(t, fh.root, "lbl", false, nil)
	hr := &parityReporter{}
	require.NoError(t, (&Plugin{store: newCenStore(fh).mock()}).runCentralization(context.Background(), nil, hr))
	var head centralizationCheckpoint
	require.NoError(t, json.Unmarshal(hr.lastCheckpoint(), &head))
	require.Equal(t, centralizationCheckpoint{Completed: 3, Total: 3}, head)
	require.Equal(t, int64(3), hr.touches.Load(), "one liveness touch per file")

	fn := newCenFixture(t, "Year One.mp3", "Hush.mp3", "Long Halloween.mp3")
	_, h, _ := runCentralizeNative(t, fn, nil, tasks.ModeLive)
	var nat centralizationCheckpoint
	raw, ok := h.FakeRun.CheckpointJSON("")
	require.True(t, ok, "the native run wrote a checkpoint")
	require.NoError(t, json.Unmarshal(raw, &nat), "the checkpoint decodes")
	require.Equal(t, head, nat)
	require.Equal(t, int64(3), h.touches.Load(), "the native run touches once per file too")
}

// The Timeout's deadline reaches the core: a context already canceled stops the
// pool before any group runs, and the error is the context's.
func TestDelugeCores_CentralizeCanceledContext(t *testing.T) {
	f := newCenFixture(t, "Year One.mp3", "Hush.mp3")
	poisonGlobals(t)
	st := newCenStore(f)
	m := st.mock()
	w := &recWriter{modify: m.ModifyBookFileFunc, update: m.UpdateBookFileFunc}
	deps := KindDeps{Store: guardedStoreOf(t, m), NewWriter: w.factory, Config: &config.Config{RootDir: f.root}}
	def := byKind(t, (&Plugin{}).Kinds(deps, nil), centralizeKind)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runKindCtxDiscard(t, ctx, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, w.recorded(), "no file was written after cancel")
	require.Empty(t, st.outcome(nil, "").dest, "no copy was made after cancel")
}

func runKindCtxDiscard(t *testing.T, ctx context.Context, d tasks.Definition, mode tasks.Mode, w tasks.Writer, params string) (*kindHost, error) {
	t.Helper()
	h, _, err := runKindCtx(t, ctx, d, mode, w, params)
	return h, err
}

// Retired-reader capability. A store that offers it lets the import see retired
// rows; a store with none fails closed (database.ErrRetiredReaderUnavailable),
// which the core counts as an error, never as a clean copy. Both shells.
type noRetiredStore struct{ pluginStore }

func TestDelugeCores_CentralizeRetiredReaderUnavailableFailsClosed(t *testing.T) {
	f := newCenFixture(t, "Year One.mp3")
	setGlobals(t, f.root, "lbl", false, nil)
	st := newCenStore(f)
	rep := &parityReporter{}
	err := (&Plugin{store: noRetiredStore{st.mock()}}).runCentralization(context.Background(), nil, rep)
	require.NoError(t, err)
	require.Contains(t, rep.lastMsg(), "0 succeeded")
	require.Contains(t, rep.lastMsg(), "1 errors")
	require.Empty(t, st.outcome(nil, "").dest, "no copy is left when the retired reader is unavailable")
	require.Empty(t, st.updated)

	// With the capability (a plain MockStore has it) the same row imports.
	f2 := newCenFixture(t, "Year One.mp3")
	head := runCentralizeHEAD(t, f2, nil)
	require.Contains(t, head.final, "1 succeeded")
}

// path-update, centralize and link-backfill never read the global config in a native run.
func TestDelugeCores_NativeNeverReadsGlobalConfig(t *testing.T) {
	f := newCenFixture(t, "Year One.mp3")
	nat, _, _ := runCentralizeNative(t, f, nil, tasks.ModeLive)
	require.Contains(t, nat.final, "1 succeeded")
	require.Contains(t, nat.dest, "Year One.mp3", "copied under the deps' RootDir, not the poisoned global one")
}

// keep the compiler honest about the time import used by later additions.
var _ = time.Second

// The pool runs groups concurrently on both shells, bounded by centralizeConcurrency
// (4): each hydrate read waits until a peer is in flight, so a pool of one would
// never overlap, and a pool of more than four would exceed the cap.
func TestDelugeCores_CentralizeGroupsRunConcurrently(t *testing.T) {
	names := []string{"Year One.mp3", "Hush.mp3", "Long Halloween.mp3", "Dark Victory.mp3", "Knightfall.mp3", "Bane.mp3", "Azrael.mp3", "Robin.mp3"}
	measure := func(t *testing.T, native bool) int64 {
		f := newCenFixture(t, names...)
		var inFlight, maxInFlight atomic.Int64
		st := newCenStore(f)
		m := st.mock()
		base := m.GetBookFileByIDFunc
		release := make(chan struct{})
		var once sync.Once
		m.GetBookFileByIDFunc = func(bookID, id string) (*database.BookFile, error) {
			n := inFlight.Add(1)
			defer inFlight.Add(-1)
			for {
				cur := maxInFlight.Load()
				if n <= cur || maxInFlight.CompareAndSwap(cur, n) {
					break
				}
			}
			if n >= 4 {
				once.Do(func() { close(release) })
			}
			select {
			case <-release:
			case <-time.After(2 * time.Second):
			}
			return base(bookID, id)
		}
		if native {
			poisonGlobals(t)
			w := &recWriter{modify: m.ModifyBookFileFunc, update: m.UpdateBookFileFunc}
			deps := KindDeps{Store: guardedStoreOf(t, m), NewWriter: w.factory, Config: &config.Config{RootDir: f.root}}
			def := byKind(t, (&Plugin{}).Kinds(deps, nil), centralizeKind)
			_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
			require.NoError(t, err)
		} else {
			setGlobals(t, f.root, "lbl", false, nil)
			require.NoError(t, (&Plugin{store: m}).runCentralization(context.Background(), nil, &parityReporter{}))
		}
		return maxInFlight.Load()
	}
	require.Equal(t, int64(4), measure(t, false), "HEAD pool: exactly the fixed 4 workers overlapped")
	require.Equal(t, int64(4), measure(t, true), "native pool: exactly the fixed 4 workers overlapped")
}

// The native kind hands the core its deps' protected-path cache and Deluge client,
// as the HEAD shell hands its plugin's: a path the cache protects stays pending
// and the importer reached is the client's.
func TestDelugeCores_CentralizeNativeCacheAndClient(t *testing.T) {
	// A file that is its own library destination, with the cache protecting it.
	f := newCenFixture(t, "Year One.mp3")
	dst := filepath.Join(f.root, "Year One.mp3")
	require.NoError(t, os.WriteFile(dst, []byte("bytes"), 0o644))
	f.rows[0].FilePath = dst
	poisonGlobals(t)
	st := newCenStore(f)
	m := st.mock()
	w := &recWriter{modify: m.ModifyBookFileFunc, update: m.UpdateBookFileFunc}
	cache := &fakeCache{protected: map[string]bool{dst: true}}
	deps := KindDeps{Store: guardedStoreOf(t, m), NewWriter: w.factory, Cache: cache, Config: &config.Config{RootDir: f.root}}
	def := byKind(t, (&Plugin{}).Kinds(deps, nil), centralizeKind)
	h, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Contains(t, lastNote(h), "1 protected (left pending)")
	require.Empty(t, st.marked, "a protected in-place row is not marked")

	// A pending copy goes through the client's importer.
	f2 := newCenFixture(t, "Hush.mp3")
	st2 := newCenStore(f2)
	m2 := st2.mock()
	fc := &fakeClient{}
	w2 := &recWriter{modify: m2.ModifyBookFileFunc, update: m2.UpdateBookFileFunc}
	deps2 := KindDeps{Store: guardedStoreOf(t, m2), NewWriter: w2.factory, Client: fc, Config: &config.Config{RootDir: f2.root}}
	def2 := byKind(t, (&Plugin{}).Kinds(deps2, nil), centralizeKind)
	_, err = runKind(t, def2, tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Equal(t, []string{"bf-Hush.mp3"}, fc.imported, "the deps' client imported the file")
}

// The importer contract the core owes ImportToLibrary: the options carry the row's
// own ID as ExpectedBookFileID (the guard against repointing another row) and the
// protected set, and the two refusals the importer can answer are counted apart:
// a list that has not loaded wraps ErrProtectedPathWrite too, so it must be tested
// first, or it would be counted as protected.
func TestDelugeCores_CentralizeImporterContract(t *testing.T) {
	f := newCenFixture(t, "Year One.mp3", "Hush.mp3", "Long Halloween.mp3")
	poisonGlobals(t)
	st := newCenStore(f)
	m := st.mock()
	var mu sync.Mutex
	got := map[string]delugeclient.ImportOptions{}
	fc := &fakeClient{importFn: func(bf *database.BookFile, opts delugeclient.ImportOptions) (string, error) {
		mu.Lock()
		got[bf.ID] = opts
		mu.Unlock()
		switch bf.ID {
		case "bf-Year One.mp3":
			return "", fmt.Errorf("source under a protected prefix: %w", tagger.ErrProtectedListNotLoaded)
		case "bf-Hush.mp3":
			return "", fmt.Errorf("source under a protected prefix: %w", tagger.ErrProtectedPathWrite)
		}
		return filepath.Join(f.root, "Long Halloween.mp3"), nil
	}}
	cache := &fakeCache{}
	w := &recWriter{modify: m.ModifyBookFileFunc, update: m.UpdateBookFileFunc}
	deps := KindDeps{Store: guardedStoreOf(t, m), NewWriter: w.factory, Cache: cache, Client: fc, Config: &config.Config{RootDir: f.root}}
	def := byKind(t, (&Plugin{}).Kinds(deps, nil), centralizeKind)
	_, out, err := runKindResult(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	var res centralizeResult
	require.NoError(t, json.Unmarshal(out, &res))
	require.Equal(t, int64(1), res.ProtectedListNotLoaded, "list not loaded")
	require.Equal(t, int64(1), res.Protected, "protected")
	require.Equal(t, int64(1), res.Succeeded, "succeeded")
	require.Zero(t, res.Errors)
	for id, opts := range got {
		require.Equal(t, id, opts.ExpectedBookFileID, "the guard names the row's own ID")
		require.NotNil(t, opts.Protected, "the protected set reaches the importer")
	}
	require.Len(t, got, 3)
}

// A cancel in the middle of a group stops that group before its next file, and
// the pool collects every group's cancellation (Collect), so the error names all
// four workers' groups and not just the first.
func TestDelugeCores_CentralizeCancelMidRun(t *testing.T) {
	names := []string{"Year One.mp3", "Year Two.mp3", "Hush One.mp3", "Hush Two.mp3",
		"Long One.mp3", "Long Two.mp3", "Dark One.mp3", "Dark Two.mp3"}
	measure := func(t *testing.T, native bool) (int64, string) {
		f := newCenFixture(t, names...)
		st := newCenStore(f)
		m := st.mock()
		base := m.GetBookFileByIDFunc
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var started, calls atomic.Int64
		release := make(chan struct{})
		m.GetBookFileByIDFunc = func(bookID, id string) (*database.BookFile, error) {
			calls.Add(1)
			// Every group's first file waits for the four to be in flight, then
			// the run is canceled and they go on to their second file.
			if n := started.Add(1); n <= 4 {
				if n == 4 {
					cancel()
					close(release)
				}
				select {
				case <-release:
				case <-time.After(3 * time.Second):
				}
			}
			return base(bookID, id)
		}
		var err error
		if native {
			poisonGlobals(t)
			w := &recWriter{modify: m.ModifyBookFileFunc, update: m.UpdateBookFileFunc}
			deps := KindDeps{Store: guardedStoreOf(t, m), NewWriter: w.factory, Config: &config.Config{RootDir: f.root}}
			def := byKind(t, (&Plugin{}).Kinds(deps, nil), centralizeKind)
			_, _, err = runKindCtx(t, ctx, def, tasks.ModeLive, &sentinelWriter{}, "")
		} else {
			setGlobals(t, f.root, "lbl", false, nil)
			err = (&Plugin{store: m}).runCentralization(ctx, nil, &parityReporter{})
		}
		require.ErrorIs(t, err, context.Canceled)
		return calls.Load(), err.Error()
	}
	for _, native := range []bool{false, true} {
		calls, text := measure(t, native)
		require.Equal(t, int64(4), calls, "native=%v: each group read its first file only, the second was stopped by the cancel", native)
		require.Equal(t, 4, strings.Count(text, "context canceled"), "native=%v: every group's cancellation is collected: %q", native, text)
	}
}

// A snapshot taken earlier that arrives after a later one is dropped, an equal one
// too, and a failed write still advances the mark (a later snapshot supersedes it).
func TestOrderedCheckpoint_OnlyMovesForward(t *testing.T) {
	var got []int
	var fail bool
	o := &orderedCheckpoint{write: func(v any) error {
		got = append(got, v.(centralizationCheckpoint).Completed)
		if fail {
			return errors.New("store down")
		}
		return nil
	}}
	o.put(centralizationCheckpoint{Completed: 2})
	o.put(centralizationCheckpoint{Completed: 1}) // taken before 2, arrives after
	o.put(centralizationCheckpoint{Completed: 2}) // a duplicate
	o.put(centralizationCheckpoint{Completed: 3})
	fail = true
	o.put(centralizationCheckpoint{Completed: 4})
	o.put(centralizationCheckpoint{Completed: 4})
	require.Equal(t, []int{2, 3, 4}, got)
}

// The run's logger is the Reporter's (HEAD) and the run's own (native): the
// core's closing line reaches it on both shells.
func TestDelugeCores_CentralizeLogsReachTheRun(t *testing.T) {
	f := newCenFixture(t, "Year One.mp3")
	setGlobals(t, f.root, "lbl", false, nil)
	rep := &parityReporter{}
	require.NoError(t, (&Plugin{store: newCenStore(f).mock()}).runCentralization(context.Background(), nil, rep))
	require.Contains(t, rep.logged(), "deluge centralization complete")

	f2 := newCenFixture(t, "Year One.mp3")
	poisonGlobals(t)
	m := newCenStore(f2).mock()
	w := &recWriter{modify: m.ModifyBookFileFunc, update: m.UpdateBookFileFunc}
	deps := KindDeps{Store: guardedStoreOf(t, m), NewWriter: w.factory, Config: &config.Config{RootDir: f2.root}}
	def := byKind(t, (&Plugin{}).Kinds(deps, nil), centralizeKind)
	h, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Contains(t, h.logged(), "deluge centralization complete")
}
