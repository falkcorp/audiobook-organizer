// file: internal/plugins/deluge/kinds_parity_test.go
// version: 1.0.0
// guid: 1b722993-2dfa-4f07-990a-6f812ee14726
// last-edited: 2026-11-19

package deluge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
)

// The parity tests run HEAD's v2 shell and the native kind over the same shared
// core on the same fixture and compare what an operator could see: the write
// log, the error, the final progress line and the result. HEAD's shell is the
// oracle (its own tests pass unchanged); the native run is handed its own
// config and a poisoned global one, so a core that read config.AppConfig fails.

// frame is one progress report of HEAD's reporter.
type frame struct {
	Current, Total int
	Msg            string
}

// parityReporter is HEAD's Reporter over recorders: progress frames, every
// checkpoint's JSON, the persisted result, the log, and a cancel switch.
type parityReporter struct {
	centralizationTestReporter
	mu       sync.Mutex
	frames   []frame
	ckpts    []json.RawMessage
	result   any
	items    []string
	logs     bytes.Buffer
	canceled atomic.Bool
	touches  atomic.Int64
}

func (r *parityReporter) UpdateProgress(cur, total int, msg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, frame{cur, total, msg})
	return nil
}

func (r *parityReporter) Checkpoint(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.ckpts = append(r.ckpts, raw)
	r.mu.Unlock()
	return nil
}

func (r *parityReporter) SetResult(v any) error {
	r.mu.Lock()
	r.result = v
	r.mu.Unlock()
	return nil
}

func (r *parityReporter) SetCurrentItem(s string) {
	r.mu.Lock()
	r.items = append(r.items, s)
	r.mu.Unlock()
}

func (r *parityReporter) TouchLiveness() { r.touches.Add(1) }

func (r *parityReporter) IsCanceled() bool { return r.canceled.Load() }

func (r *parityReporter) Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&lockedWriter{mu: &r.mu, w: &r.logs}, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func (r *parityReporter) logged() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.logs.String()
}

func (r *parityReporter) frameList() []frame {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]frame(nil), r.frames...)
}

func (r *parityReporter) lastMsg() string {
	fs := r.frameList()
	if len(fs) == 0 {
		return ""
	}
	return fs[len(fs)-1].Msg
}

func (r *parityReporter) lastCheckpoint() json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.ckpts) == 0 {
		return nil
	}
	return r.ckpts[len(r.ckpts)-1]
}

// totals is every positive progress denominator HEAD reported.
func (r *parityReporter) totals() []int {
	seen := map[int]bool{}
	for _, f := range r.frameList() {
		if f.Total > 0 {
			seen[f.Total] = true
		}
	}
	var out []int
	for k := range seen {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// nativeTotals is the same set from a native host's bar frames.
func nativeTotals(h *kindHost) []int {
	seen := map[int]bool{}
	for _, b := range h.bars() {
		if b.Known && b.Total > 0 {
			seen[int(b.Total)] = true
		}
	}
	// The pool's own deliveries (tasks.RunItems reports through ItemsProgress,
	// not the bar HEAD's sdk.Progress writes).
	for _, e := range h.FakeRun.Progress() {
		if e.Total > 0 {
			seen[int(e.Total)] = true
		}
	}
	var out []int
	for k := range seen {
		out = append(out, int(k))
	}
	sort.Ints(out)
	return out
}

func lastNote(h *kindHost) string {
	n := h.noteList()
	if len(n) == 0 {
		return ""
	}
	return n[len(n)-1]
}

// errText is err's text, or "" for nil.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// --- deluge.protected-paths-sync -------------------------------------------------

// Both shells run the same body: Start, Step, Finalize, Done, in that order, with
// total n+2 = 3 (sdk.NewProgress's frames), and one refresh of the cache.
func TestDelugeCores_ProtectedPathsSyncParity(t *testing.T) {
	// HEAD.
	cache := &fakeCache{}
	_ = cache
	fake := &fakeTorrentServer{savePath: "/seeding/Gotham"}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	client, err := delugeclient.New(srv.URL, "test-password")
	require.NoError(t, err)
	realCache := delugeclient.NewProtectedPathCache(client, nil)
	hr := &parityReporter{}
	require.NoError(t, (&Plugin{client: client, cache: realCache}).runProtectedPathsSync(context.Background(), nil, hr))
	require.Equal(t, int64(1), fake.fetches.Load(), "HEAD's shell refreshed once")

	// Native.
	nc := &fakeCache{}
	def := byKind(t, (&Plugin{}).Kinds(KindDeps{Cache: nc}, nil), protectedPathsSyncKind)
	h, out, nerr := runKindResult(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, nerr)
	require.Equal(t, 1, nc.refreshCount(), "the native run refreshed once")
	require.JSONEq(t, `{}`, string(out))

	wantMsgs := []string{"Refreshing protected paths from Deluge... (0%)", "Cache refreshed (33%)", "Writing results... (67%)", "Protected paths refreshed (100%)"}
	var headMsgs []string
	var headFrames []frame
	for _, f := range hr.frameList() {
		headMsgs = append(headMsgs, f.Msg)
		headFrames = append(headFrames, f)
	}
	require.Equal(t, wantMsgs, headMsgs, "HEAD's frames")
	require.Equal(t, wantMsgs, h.noteList(), "the native frames carry the same messages in the same order")
	// Same bar: current/total of each frame.
	var native []barFrame
	for _, f := range headFrames {
		native = append(native, barFrame{Done: int64(f.Current), Total: int64(f.Total), Known: f.Total > 0})
	}
	require.Equal(t, native, h.bars(), "the native bar frames equal HEAD's")
	require.Equal(t, []int{3}, hr.totals())
}

// A refresh that fails fails both shells with the same error and no Done frame.
func TestDelugeCores_ProtectedPathsSyncRefreshErrorParity(t *testing.T) {
	boom := errors.New("deluge is down")
	hr := &parityReporter{}
	headErr := (&Plugin{cache: delugeclient.NewProtectedPathCache(downClient(t), nil)}).runProtectedPathsSync(context.Background(), nil, hr)
	require.Error(t, headErr)

	nc := &fakeCache{err: boom}
	def := byKind(t, (&Plugin{}).Kinds(KindDeps{Cache: nc}, nil), protectedPathsSyncKind)
	h, _, nerr := runKindResult(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorIs(t, nerr, boom, "the cache's own error comes back")
	require.NotContains(t, h.noteList(), "Protected paths refreshed")
	require.NotContains(t, hr.lastMsg(), "Protected paths refreshed")
}

// downClient is a Deluge client whose server answers 503.
func downClient(t *testing.T) *delugeclient.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	c, err := delugeclient.New(srv.URL, "test-password")
	require.NoError(t, err)
	return c
}

// An unset cache is a refusal in both shells, never a nil dereference: HEAD's
// shell leaves the env's cache an untyped nil for a plugin with none, and the
// native kind for deps with none.
func TestDelugeCores_ProtectedPathsSyncNoCache(t *testing.T) {
	err := (&Plugin{}).runProtectedPathsSync(context.Background(), nil, &parityReporter{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cache is not configured")

	def := byKind(t, (&Plugin{}).Kinds(KindDeps{}, nil), protectedPathsSyncKind)
	_, _, nerr := runKindResult(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.Error(t, nerr)
	require.Contains(t, nerr.Error(), "cache is not configured")
}

// ReadOnly: a Preview and a Live run both refresh (there is nothing to preview).
func TestDelugeCores_ProtectedPathsSyncRunsInEveryMode(t *testing.T) {
	for _, mode := range []tasks.Mode{tasks.ModeLive, tasks.ModePreview} {
		nc := &fakeCache{}
		def := byKind(t, (&Plugin{}).Kinds(KindDeps{Cache: nc}, nil), protectedPathsSyncKind)
		_, _, err := runKindResult(t, def, mode, &sentinelWriter{}, "")
		require.NoError(t, err)
		require.Equal(t, 1, nc.refreshCount(), "mode %v", mode)
	}
}

// --- deluge.path-update ----------------------------------------------------------

// recordingDeluge is a Deluge Web UI that records every core.move_storage call
// as "hash -> dest" and fails the ones for the hashes in fail.
type recordingDeluge struct {
	mu    sync.Mutex
	moves []string
	fail  map[string]bool
}

func (d *recordingDeluge) moveList() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	// The real client retries a failed call once, so a failing hash shows up
	// twice; the set of moves is what the core decided, not the retry count.
	seen := map[string]bool{}
	var out []string
	for _, m := range d.moves {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}

func (d *recordingDeluge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string `json:"method"`
		ID     int64  `json:"id"`
		Params []any  `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var result any = true
	var rpcErr any
	if req.Method == "core.move_storage" {
		ids, _ := req.Params[0].([]any)
		dest, _ := req.Params[1].(string)
		for _, id := range ids {
			hash, _ := id.(string)
			d.mu.Lock()
			d.moves = append(d.moves, hash+" -> "+dest)
			failing := d.fail[hash]
			d.mu.Unlock()
			if failing {
				rpcErr = map[string]any{"message": "move failed", "code": 1}
			}
		}
		result = nil
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"id": req.ID, "result": result, "error": rpcErr})
}

// puFixture is a book with versions and the config both shells run against.
type puFixture struct {
	book     *database.Book
	versions []database.BookVersion
	bookErr  error
	versErr  error
	missing  bool
}

func (f *puFixture) store() *database.MockStore {
	return &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			if f.bookErr != nil {
				return nil, f.bookErr
			}
			if f.missing {
				return nil, nil
			}
			return f.book, nil
		},
		GetBookVersionsByBookIDFunc: func(string) ([]database.BookVersion, error) {
			return f.versions, f.versErr
		},
	}
}

func puHashes() (string, string, string) {
	return linkTorrentID(11), linkTorrentID(12), linkTorrentID(13)
}

func newPUFixture() *puFixture {
	h1, h2, h3 := puHashes()
	return &puFixture{
		book: &database.Book{ID: "book-dark-knight-returns", FilePath: "/library/Frank Miller/Dark Knight Returns/part1.mp3"},
		versions: []database.BookVersion{
			{ID: "v-active-1", Status: database.BookVersionStatusActive, TorrentHash: h1},
			{ID: "v-active-2", Status: database.BookVersionStatusActive, TorrentHash: h2},
			{ID: "v-inactive", Status: "inactive", TorrentHash: h3},
			{ID: "v-no-torrent", Status: database.BookVersionStatusActive},
		},
	}
}

// pathUpdateOutcome is what one shell of path-update did.
type pathUpdateOutcome struct {
	err   string
	moves []string
	final string
	log   string
}

func runPathUpdateHEAD(t *testing.T, f *puFixture, client bool, moveEnabled bool, dryRun bool, bookID string, failing map[string]bool) pathUpdateOutcome {
	t.Helper()
	setGlobals(t, "/lib", "lbl", moveEnabled, nil)
	p := &Plugin{store: f.store()}
	d := &recordingDeluge{fail: failing}
	if client {
		srv := httptest.NewServer(d)
		t.Cleanup(srv.Close)
		c, err := delugeclient.New(srv.URL, "test-password")
		require.NoError(t, err)
		p.client = c
	}
	rep := &parityReporter{}
	params := fmt.Sprintf(`{"book_id":%q,"dry_run":%v}`, bookID, dryRun)
	err := p.runPathUpdate(context.Background(), json.RawMessage(params), rep)
	return pathUpdateOutcome{err: errText(err), moves: d.moveList(), final: rep.lastMsg(), log: rep.logged()}
}

func runPathUpdateNative(t *testing.T, f *puFixture, client bool, moveEnabled bool, dryRun bool, bookID string, failing map[string]bool) (pathUpdateOutcome, json.RawMessage) {
	t.Helper()
	poisonGlobals(t)
	fc := &fakeClient{}
	deps := KindDeps{Store: guardedStoreOf(t, f.store()), Config: &config.Config{DelugeMoveEnabled: moveEnabled}}
	if client {
		deps.Client = &failingMover{fakeClient: fc, fail: failing}
	}
	def := byKind(t, (&Plugin{}).Kinds(deps, nil), pathUpdateKind)
	mode := tasks.ModeLive
	if dryRun {
		mode = tasks.ModePreview
	}
	h, out, err := runKindResult(t, def, mode, &sentinelWriter{}, fmt.Sprintf(`{"book_id":%q}`, bookID))
	return pathUpdateOutcome{err: errText(err), moves: fc.moveList(), final: lastNote(h), log: h.logged()}, out
}

// failingMover makes MoveStorage fail for the hashes in fail, as the Deluge
// server of the HEAD shell does.
type failingMover struct {
	*fakeClient
	fail map[string]bool
}

func (m *failingMover) MoveStorage(ids []string, dest string) error {
	err := m.fakeClient.MoveStorage(ids, dest)
	for _, id := range ids {
		if m.fail[id] {
			return errors.New("move failed")
		}
	}
	return err
}

func TestDelugeCores_PathUpdateParity(t *testing.T) {
	h1, h2, _ := puHashes()
	one := func() *puFixture { // a single active version: it moves to the book's own directory
		f := newPUFixture()
		f.versions = f.versions[:1]
		return f
	}
	cases := []struct {
		name        string
		fx          func() *puFixture
		client      bool
		moveEnabled bool
		dryRun      bool
		bookID      string
		failing     map[string]bool
		wantErr     string
		wantMoves   []string
		wantFinal   string
	}{
		{name: "live moves each active version to its own folder", fx: newPUFixture, client: true, moveEnabled: true, bookID: "b1",
			wantMoves: []string{
				h1 + " -> /library/Frank Miller/Dark Knight Returns/.versions/v-active-1",
				h2 + " -> /library/Frank Miller/Dark Knight Returns/.versions/v-active-2",
			},
			wantFinal: "APPLY: 0 move_storage planned, 2 updated (100%)"},
		{name: "live single active version uses the book folder", fx: one, client: true, moveEnabled: true, bookID: "b1",
			wantMoves: []string{h1 + " -> /library/Frank Miller/Dark Knight Returns"},
			wantFinal: "APPLY: 0 move_storage planned, 1 updated (100%)"},
		{name: "dry run plans and moves nothing", fx: newPUFixture, client: true, moveEnabled: true, dryRun: true, bookID: "b1",
			wantFinal: "DRY RUN: 2 move_storage planned, 0 updated (100%)"},
		{name: "a failed move is non-fatal and not counted", fx: newPUFixture, client: true, moveEnabled: true, bookID: "b1",
			failing: map[string]bool{h1: true},
			wantMoves: []string{
				h1 + " -> /library/Frank Miller/Dark Knight Returns/.versions/v-active-1",
				h2 + " -> /library/Frank Miller/Dark Knight Returns/.versions/v-active-2",
			},
			wantFinal: "APPLY: 0 move_storage planned, 1 updated (100%)"},
		{name: "deluge_move_enabled off does nothing", fx: newPUFixture, client: true, moveEnabled: false, bookID: "b1",
			wantFinal: "APPLY: 0 move_storage planned, 0 updated (100%)"},
		{name: "no deluge client does nothing", fx: newPUFixture, client: false, moveEnabled: true, bookID: "b1",
			wantFinal: "APPLY: 0 move_storage planned, 0 updated (100%)"},
		{name: "dry run with no client plans nothing", fx: newPUFixture, client: false, moveEnabled: true, dryRun: true, bookID: "b1",
			wantFinal: "DRY RUN: 0 move_storage planned, 0 updated (100%)"},
		{name: "book_id is required", fx: newPUFixture, client: true, moveEnabled: true, bookID: "", wantErr: "book_id is required"},
		{name: "book not found", fx: func() *puFixture { f := newPUFixture(); f.missing = true; return f }, client: true, moveEnabled: true,
			bookID: "b1", wantErr: "book not found", wantFinal: "Loading book b1... (0%)"},
		{name: "book read fails", fx: func() *puFixture { f := newPUFixture(); f.bookErr = errors.New("read failed"); return f }, client: true,
			moveEnabled: true, bookID: "b1", wantErr: "load book: read failed", wantFinal: "Loading book b1... (0%)"},
		{name: "versions read fails", fx: func() *puFixture { f := newPUFixture(); f.versErr = errors.New("versions failed"); return f }, client: true,
			moveEnabled: true, bookID: "b1", wantErr: "load versions: versions failed", wantFinal: "Loading book b1... (0%)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			head := runPathUpdateHEAD(t, tc.fx(), tc.client, tc.moveEnabled, tc.dryRun, tc.bookID, tc.failing)
			nat, out := runPathUpdateNative(t, tc.fx(), tc.client, tc.moveEnabled, tc.dryRun, tc.bookID, tc.failing)

			require.Equal(t, tc.wantErr, head.err, "HEAD's error")
			require.Equal(t, head.err, nat.err, "the native error equals HEAD's")
			require.Equal(t, tc.wantMoves, head.moves, "HEAD's moves")
			require.Equal(t, head.moves, nat.moves, "the native moves equal HEAD's")
			require.Equal(t, tc.wantFinal, head.final, "HEAD's final line")
			require.Equal(t, head.final, nat.final, "the native final line equals HEAD's")
			if tc.wantErr == "" {
				var res pathUpdateResult
				require.NoError(t, json.Unmarshal(out, &res))
				require.Equal(t, tc.dryRun, res.DryRun)
				require.Equal(t, tc.bookID, res.BookID)
			}
		})
	}
}

// A typed-nil *Client in the plugin is no Deluge: HEAD's shell leaves the env's
// mover an untyped nil, so the core's "is Deluge configured" check stays true to
// HEAD's `p.client == nil`.
func TestDelugeCores_PathUpdateTypedNilClient(t *testing.T) {
	setGlobals(t, "/lib", "lbl", true, nil)
	f := newPUFixture()
	rep := &parityReporter{}
	var nilClient *delugeclient.Client
	err := (&Plugin{client: nilClient, store: f.store()}).runPathUpdate(context.Background(), json.RawMessage(`{"book_id":"b1","dry_run":false}`), rep)
	require.NoError(t, err)
	require.Equal(t, "APPLY: 0 move_storage planned, 0 updated (100%)", rep.lastMsg())
}

// HEAD's mode switch: the two spellings disagreeing is an error before any work.
func TestDelugeCores_PathUpdateHEADModeConflict(t *testing.T) {
	setGlobals(t, "/lib", "lbl", true, nil)
	rep := &parityReporter{}
	err := (&Plugin{store: newPUFixture().store()}).runPathUpdate(context.Background(),
		json.RawMessage(`{"book_id":"b1","dry_run":true,"dryRun":false}`), rep)
	require.Error(t, err)
	require.Empty(t, rep.frameList(), "nothing ran")
	err = (&Plugin{}).runPathUpdate(context.Background(), json.RawMessage(`{`), rep)
	require.ErrorContains(t, err, "unmarshal params")
}

// The native Task's result carries the counts; a Preview reports planned.
func TestDelugeCores_PathUpdateNativeResult(t *testing.T) {
	nat, out := runPathUpdateNative(t, newPUFixture(), true, true, true, "b1", nil)
	require.Empty(t, nat.err)
	var res pathUpdateResult
	require.NoError(t, json.Unmarshal(out, &res))
	require.Equal(t, pathUpdateResult{BookID: "b1", DryRun: true, Planned: 2, Updated: 0}, res)
	nat, out = runPathUpdateNative(t, newPUFixture(), true, true, false, "b1", nil)
	require.Empty(t, nat.err)
	require.NoError(t, json.Unmarshal(out, &res))
	require.Equal(t, pathUpdateResult{BookID: "b1", DryRun: false, Planned: 0, Updated: 2}, res)
}

// A native run whose owner left Store or Config unset is refused, naming the field.
func TestDelugeCores_NativeRefusesUnsetDeps(t *testing.T) {
	for _, tc := range []struct {
		kind   tasks.Kind
		params string
		deps   KindDeps
		mode   tasks.Mode
		want   string
	}{
		{pathUpdateKind, `{"book_id":"b"}`, KindDeps{Config: &config.Config{}}, tasks.ModeLive, "KindDeps.Store is not set"},
		{pathUpdateKind, `{"book_id":"b"}`, KindDeps{Store: guardedStoreOf(t, &database.MockStore{})}, tasks.ModeLive, "KindDeps.Config is not set"},
		{centralizeKind, "", KindDeps{Config: &config.Config{}}, tasks.ModeLive, "KindDeps.Store is not set"},
		{centralizeKind, "", KindDeps{Store: guardedStoreOf(t, &database.MockStore{})}, tasks.ModeLive, "KindDeps.Config is not set"},
		{centralizeKind, "", KindDeps{Store: guardedStoreOf(t, &database.MockStore{}), Config: &config.Config{}}, tasks.ModeLive, "KindDeps.NewWriter is not set"},
		{centralizeKind, "", KindDeps{Store: guardedStoreOf(t, &database.MockStore{}), Config: &config.Config{},
			NewWriter: func(tasks.Writer) kindWriter { return nil }}, tasks.ModeLive, "returned no writer"},
		{linkBackfillKind, "", KindDeps{Config: &config.Config{}}, tasks.ModePreview, "KindDeps.Store is not set"},
		{linkBackfillKind, "", KindDeps{Store: guardedStoreOf(t, &database.MockStore{})}, tasks.ModePreview, "KindDeps.Config is not set"},
		{linkBackfillKind, "", KindDeps{Store: guardedStoreOf(t, &database.MockStore{}), Config: &config.Config{RootDir: "/r"},
			Client: &fakeClient{}}, tasks.ModeLive, "KindDeps.NewWriter is not set"},
		{linkBackfillKind, "", KindDeps{Store: guardedStoreOf(t, &database.MockStore{}), Config: &config.Config{RootDir: "/r"},
			Client: &fakeClient{}, NewWriter: func(tasks.Writer) kindWriter { return nil }}, tasks.ModeLive, "returned no writer"},
	} {
		t.Run(string(tc.kind)+"/"+tc.want, func(t *testing.T) {
			def := byKind(t, (&Plugin{}).Kinds(tc.deps, nil), tc.kind)
			_, _, err := runKindResult(t, def, tc.mode, &sentinelWriter{}, tc.params)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

var _ = strings.Contains
