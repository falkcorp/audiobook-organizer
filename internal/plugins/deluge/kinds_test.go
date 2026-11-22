// file: internal/plugins/deluge/kinds_test.go
// version: 1.0.1
// guid: 12a92dd8-393d-4645-83f1-f2143d4d0e31
// last-edited: 2026-11-19

package deluge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// F5-05's shared test kit for the Deluge plugin: the four shared
// TestDelugeKinds_ tests below, over the kindRow literals (rule R8) and the
// fakes this file defines. The per-definition parity and behaviour tests are in
// kinds_cores_test.go, the env tests in kinds_env_test.go. Fixtures are
// synthetic: Batman comic titles and characters, 192.0.2.x addresses.

// kindRow is one definition's expected shape, spelled out literally (rule R8):
// the native kind is compared with this and HEAD's definition is compared with
// the same row, so a drift of either fails. Nothing here is derived from the
// code under test.
type kindRow struct {
	// HeadID is the HEAD def ID; the native kind is "books." + HeadID (rule R3).
	HeadID string
	// Title is HEAD's DisplayName, carried verbatim.
	Title string
	// HeadResume is HEAD's ResumePolicy, Resume the native one (rule R5 and the
	// coordinator's ruling: Restart maps to Continue only if HEAD reads its
	// checkpoint on restart, else FromZero; Requeue FromZero; Drop Drop).
	HeadResume sdk.ResumePolicy
	Resume     tasks.ResumePolicy
	Priority   tasks.Priority
	Timeout    time.Duration
	HeadKey    string
	// HeadWrites is HEAD's declared Writes (empty for three of the four).
	HeadWrites []sdk.Resource
	// Writes is the native write set; ReadOnly means tasks.ReadOnly.
	Writes   []ops.Resource
	ReadOnly bool
	// HeadPerms is HEAD's Permissions (empty: the registry defaults it to
	// settings.manage); Scopes is the native route requirement, literally.
	HeadPerms []auth.Permission
	Scopes    []rbac.Permission
	// Par is the String() of each pool the body runs (rule R4), ParGot the same
	// pools read from the code's own parallelism functions.
	Par    []string
	ParGot []tasks.Parallelism
}

// settingsScopes and integrationsScopes are the two atom rows these
// definitions use (internal/books/atoms.go), spelled out here independently of
// kinds.go's copies.
var (
	settingsScopes = []rbac.Permission{
		"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
		"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage",
	}
	integrationsScopes = []rbac.Permission{"core:integrations.manage", "books:integrations.manage"}
)

func delugeRows() []kindRow {
	return []kindRow{
		{
			HeadID: "deluge.protected-paths-sync", Title: "Sync protected paths from Deluge",
			HeadResume: sdk.ResumeRequeue, Resume: tasks.ResumeFromZero,
			Priority: tasks.PriorityLow, Timeout: 5 * time.Minute,
			HeadKey:  "deluge.protected-paths-sync",
			ReadOnly: true,
			Scopes:   settingsScopes,
		},
		{
			HeadID: "deluge.centralize", Title: "Centralize Deluge books",
			HeadResume: sdk.ResumeRestart, Resume: tasks.ResumeFromZero,
			Priority: tasks.PriorityNormal, Timeout: 24 * time.Hour,
			HeadKey: "deluge.centralize",
			Writes: []ops.Resource{ops.ResAuthors, ops.ResBookFiles, ops.ResBooks, ops.ResFiles,
				ops.ResIndexes, ops.ResMetadataCache},
			Scopes: settingsScopes,
			Par:    []string{centralizeParString},
			ParGot: []tasks.Parallelism{centralizeParallelism()},
		},
		{
			HeadID: "deluge.path-update", Title: "Update Deluge torrent path",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityNormal, Timeout: time.Minute,
			HeadKey: "deluge.path-update",
			Writes:  []ops.Resource{ops.ResFiles},
			Scopes:  settingsScopes,
			Par:     []string{pathUpdateParString},
			ParGot:  []tasks.Parallelism{pathUpdateParallelism()},
		},
		{
			HeadID: "deluge.link-backfill", Title: "Link Deluge torrents to library files",
			HeadResume: sdk.ResumeRestart, Resume: tasks.ResumeFromZero,
			Priority: tasks.PriorityNormal, Timeout: 4 * time.Hour,
			HeadKey:    "deluge.link-backfill",
			HeadWrites: []sdk.Resource{sdk.ResBookFiles},
			Writes:     []ops.Resource{ops.ResBookFiles},
			HeadPerms:  []auth.Permission{auth.PermIntegrationsManage},
			Scopes:     integrationsScopes,
			Par:        []string{linkParString3},
			ParGot:     []tasks.Parallelism{linkParallelism(3)},
		},
	}
}

// The expected parallelism strings, spelled out.
const (
	centralizeParString = "fixed(4): each item is a connected group of pending files doing disk copies, store writes and Deluge RPCs, bounded by what the Deluge daemon and the library disk tolerate"
	pathUpdateParString = "sequential: one book's versions (a handful); sequential by design"
	linkParString3      = "fixed(3): each worker hashes at most 20 MB of a torrent file at a time, so the count bounds disk pressure on the download volume (max 8)"
)

// kindHeadPriority maps HEAD's three priorities onto the native ones.
func kindHeadPriority(p registry.Priority) tasks.Priority {
	switch p {
	case registry.PriorityHigh:
		return tasks.PriorityHigh
	case registry.PriorityNormal:
		return tasks.PriorityNormal
	default:
		return tasks.PriorityLow
	}
}

func refStrings(rs []ops.Resource) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, "books/"+string(r))
	}
	sort.Strings(out)
	return out
}

func refStrings2(refs []entity.Ref) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, tasks.FormatRef(r))
	}
	sort.Strings(out)
	return out
}

func parStrings(ps []tasks.Parallelism) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.String())
	}
	return out
}

// byKind finds the definition of a kind in defs.
func byKind(t *testing.T, defs []tasks.Definition, kind tasks.Kind) tasks.Definition {
	t.Helper()
	for _, d := range defs {
		if d.Kind() == kind {
			return d
		}
	}
	t.Fatalf("no definition of kind %s", kind)
	return nil
}

// --- host --------------------------------------------------------------------

// kindHost is a taskstest.FakeRun with the mode, writer, context and
// observation a test chooses (FakeRun is pinned to ModeLive with no writer).
type kindHost struct {
	*taskstest.FakeRun
	mode tasks.Mode
	w    tasks.Writer

	mu      sync.Mutex
	notes   []string
	bar     []barFrame
	logs    bytes.Buffer
	touches atomic.Int64
	// capObserver sees every worker count a run's pool declared.
	capObserver func(declared int)
}

// barFrame is one progress-bar report: a run's rc.Progress().Set.
type barFrame struct {
	Done, Total int64
	Known       bool
}

func (h *kindHost) Mode() tasks.Mode     { return h.mode }
func (h *kindHost) Writer() tasks.Writer { return h.w }
func (h *kindHost) Touch()               { h.touches.Add(1) }

func (h *kindHost) ReportProgress(done int64, total tasks.Total) {
	n, known := total.Value()
	h.mu.Lock()
	h.bar = append(h.bar, barFrame{Done: done, Total: n, Known: known})
	h.mu.Unlock()
}

func (h *kindHost) Note(s string) {
	h.mu.Lock()
	h.notes = append(h.notes, s)
	h.mu.Unlock()
}

func (h *kindHost) CapWorkers(declared int) int {
	if h.capObserver != nil {
		h.capObserver(declared)
	}
	return h.FakeRun.CapWorkers(declared)
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// Log is a logger over the host's own buffer (FakeRun discards).
func (h *kindHost) Log() *slog.Logger {
	return slog.New(slog.NewTextHandler(&lockedWriter{mu: &h.mu, w: &h.logs}, nil))
}

func (h *kindHost) logged() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.logs.String()
}

func (h *kindHost) noteList() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.notes)
}

func (h *kindHost) bars() []barFrame {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.bar)
}

func newKindHost(ctx context.Context, d tasks.Definition, mode tasks.Mode, w tasks.Writer) *kindHost {
	return &kindHost{FakeRun: taskstest.NewFakeRun(ctx, d.Kind()), mode: mode, w: w}
}

// runKindResult runs d's own Run, the function the executor would call, on a
// fresh host and returns the host, the JSON result and the error.
func runKindResult(t *testing.T, d tasks.Definition, mode tasks.Mode, w tasks.Writer, params string) (*kindHost, json.RawMessage, error) {
	t.Helper()
	return runKindCtx(t, context.Background(), d, mode, w, params)
}

func runKindCtx(t *testing.T, ctx context.Context, d tasks.Definition, mode tasks.Mode, w tasks.Writer, params string) (*kindHost, json.RawMessage, error) {
	t.Helper()
	h := newKindHost(ctx, d, mode, w)
	r, ok := d.(interface {
		ExecutorRunners() (func(json.RawMessage) (any, error), *tasks.TaskSpec[any, json.RawMessage], *tasks.BatchSpec[any, any, json.RawMessage], *tasks.FixerSpec[any, any])
	})
	require.True(t, ok, "%T has no ExecutorRunners", d)
	decode, task, _, _ := r.ExecutorRunners()
	require.NotNil(t, task, "%s is not a Task", d.Kind())
	var raw json.RawMessage
	if params != "" {
		raw = json.RawMessage(params)
	}
	p, err := decode(raw)
	if err != nil {
		return h, nil, err
	}
	out, err := task.Run(tasks.NewRC(h), p)
	return h, out, err
}

// runKind is runKindResult without the result.
func runKind(t *testing.T, d tasks.Definition, mode tasks.Mode, w tasks.Writer, params string) (*kindHost, error) {
	t.Helper()
	h, _, err := runKindResult(t, d, mode, w, params)
	return h, err
}

// sentinelWriter is a tasks.Writer only its identity matters for: a probe
// asserts that the writer the deps factory saw is the very writer the run's
// rc.Writer() returned.
type sentinelWriter struct{ tasks.Writer }

// --- store and writer fakes ----------------------------------------------------

// guardedStore is the read store a native test hands KindDeps.Store: every read
// of the embedded mock, and a t.Errorf for every write-ish method a core could
// reach around env.Writer (the brief's "store fake whose write-ish methods
// t.Fatal"; Errorf keeps the pool goroutines safe).
type guardedStore struct {
	*database.MockStore
	t *testing.T
}

func (g guardedStore) ModifyBookFile(string, string, func(*database.BookFile) error) (*database.BookFile, error) {
	g.t.Errorf("store.ModifyBookFile called: a write must go through env.Writer")
	return nil, errors.New("store written around the writer")
}

func (g guardedStore) UpdateBookFile(string, *database.BookFile) error {
	g.t.Errorf("store.UpdateBookFile called: a write must go through env.Writer")
	return errors.New("store written around the writer")
}

// guardedStoreOf wraps m, a MockStore.
func guardedStoreOf(t *testing.T, m *database.MockStore) guardedStore {
	return guardedStore{MockStore: m, t: t}
}

// recWriter is the writer a native test hands KindDeps.NewWriter: it records the
// kernel writer each run built it over and every write, and forwards both
// methods to the backing functions.
type recWriter struct {
	mu    sync.Mutex
	seen  []tasks.Writer
	calls []string

	modify func(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
	update func(id string, file *database.BookFile) error
}

func (w *recWriter) rec(call string) {
	w.mu.Lock()
	w.calls = append(w.calls, call)
	w.mu.Unlock()
}

func (w *recWriter) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	w.rec("ModifyBookFile " + bookID + " " + fileID)
	if w.modify == nil {
		return &database.BookFile{ID: fileID, BookID: bookID}, nil
	}
	return w.modify(bookID, fileID, fn)
}

func (w *recWriter) UpdateBookFile(id string, file *database.BookFile) error {
	w.rec("UpdateBookFile " + id)
	if w.update == nil {
		return nil
	}
	return w.update(id, file)
}

func (w *recWriter) factory(tw tasks.Writer) kindWriter {
	w.mu.Lock()
	w.seen = append(w.seen, tw)
	w.mu.Unlock()
	return w
}

func (w *recWriter) writers() []tasks.Writer {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.seen)
}

// recorded is the writes in a stable order (the pools run in any order).
func (w *recWriter) recorded() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := slices.Clone(w.calls)
	sort.Strings(out)
	return out
}

// fakeClient is the Deluge client a native test hands KindDeps.Client: it lists
// the torrents it is given, records every move_storage, and imports through the
// real ImportToLibraryWith with no Deluge client (move enabled or not is the
// config's: with a nil *Client the importer never moves storage).
type fakeClient struct {
	mu       sync.Mutex
	torrents []delugeclient.TorrentDetail
	listErr  error
	moveErr  error
	labels   []string
	moves    []string
	imported []string
	// importFn, when set, replaces the real import: it sees the options the core
	// passed and answers in its place.
	importFn func(bf *database.BookFile, opts delugeclient.ImportOptions) (string, error)
}

func (c *fakeClient) ListTorrentDetailsByLabel(label string) ([]delugeclient.TorrentDetail, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.labels = append(c.labels, label)
	if c.listErr != nil {
		return nil, c.listErr
	}
	return slices.Clone(c.torrents), nil
}

func (c *fakeClient) MoveStorage(ids []string, dest string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range ids {
		c.moves = append(c.moves, id+" -> "+dest)
	}
	return c.moveErr
}

func (c *fakeClient) ImportToLibrary(cfg *config.Config, store delugeclient.Store, bf *database.BookFile, opts delugeclient.ImportOptions) (string, error) {
	c.mu.Lock()
	c.imported = append(c.imported, bf.ID)
	fn := c.importFn
	c.mu.Unlock()
	if fn != nil {
		return fn(bf, opts)
	}
	return delugeclient.ImportToLibraryWith(cfg, nil, store, bf, opts)
}

func (c *fakeClient) moveList() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := slices.Clone(c.moves)
	sort.Strings(out)
	return out
}

// fakeCache is the protected-path cache a native test hands KindDeps.Cache.
type fakeCache struct {
	mu        sync.Mutex
	refreshes int
	err       error
	protected map[string]bool
}

func (c *fakeCache) Refresh() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshes++
	return c.err
}

func (c *fakeCache) IsProtected(path string) bool { return c.protected[path] }

func (c *fakeCache) ProtectedBy(path string) string {
	if c.protected[path] {
		return "fake"
	}
	return ""
}

func (c *fakeCache) refreshCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refreshes
}

// setGlobals sets the global config the HEAD shells read and restores it at the
// end of the test. A native run never reads it (rule R10), so a native test
// calls poisonGlobals and hands the run its own config.
func setGlobals(t *testing.T, root, label string, moveEnabled bool, protected []string) {
	t.Helper()
	orig := config.Snapshot()
	config.SetForTest(func(c *config.Config) {
		c.RootDir = root
		c.DelugeDiscoveryLabel = label
		c.DelugeMoveEnabled = moveEnabled
		c.ProtectedPaths = protected
	})
	t.Cleanup(func() {
		config.SetForTest(func(c *config.Config) {
			c.RootDir = orig.RootDir
			c.DelugeDiscoveryLabel = orig.DelugeDiscoveryLabel
			c.DelugeMoveEnabled = orig.DelugeMoveEnabled
			c.ProtectedPaths = orig.ProtectedPaths
		})
	})
}

// poisonGlobals makes the global config unusable, so a native core that read it
// instead of its env would fail.
func poisonGlobals(t *testing.T) {
	t.Helper()
	setGlobals(t, "/poison/global/root", "poison-label", false, []string{"/poison/global/protected"})
}

// --- the fake deps shared by the four shared tests -------------------------------

// sharedProbeEnv is everything a probe needs to observe a run.
type sharedProbeEnv struct {
	deps   KindDeps
	writer *recWriter
	client *fakeClient
	store  *database.MockStore
}

// newSharedDeps builds KindDeps whose store holds one in-place pending book
// file of Bruce Wayne's (centralize), a book with one Deluge version
// (path-update) and one torrent matching one library row (link-backfill).
func newSharedDeps(t *testing.T) (sharedProbeEnv, string) {
	t.Helper()
	fx := newLinkFixture(t) // sets the globals; poisoned below
	cfg := &config.Config{
		RootDir: fx.root, DelugeDiscoveryLabel: linkTestLabel, DelugeMoveEnabled: true,
	}
	poisonGlobals(t)

	// link-backfill: one torrent file, one library row with the same bytes.
	tor := fx.torrent(t, 1, fx.downloads, tfile{rel: "01.mp3", content: exampleAudio})
	row := fx.libraryRow("bf-year-one", "01.mp3", exampleAudio)
	lm := newLinkMock(row)

	// centralize: a pending row that already sits at its library path.
	inPlace := database.BookFile{ID: "bf-hush", BookID: "book-hush", FilePath: row.FilePath + ".hush.mp3"}
	// path-update: a book with one active version carrying a torrent hash.
	book := &database.Book{ID: "book-long-halloween", FilePath: fx.root + "/Long Halloween/01.mp3"}
	versions := []database.BookVersion{{ID: "v1", BookID: book.ID, Status: database.BookVersionStatusActive, TorrentHash: linkTorrentID(7)}}

	m := lm.MockStore
	m.GetBookFilesNeedingDelugeImportCoreFunc = func() ([]database.BookFileCore, error) {
		return []database.BookFileCore{inPlace.Core()}, nil
	}
	m.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if id == book.ID {
			return book, nil
		}
		return nil, nil
	}
	m.GetBookVersionsByBookIDFunc = func(id string) ([]database.BookVersion, error) {
		if id == book.ID {
			return versions, nil
		}
		return nil, nil
	}
	w := &recWriter{modify: lm.MockStore.ModifyBookFileFunc}
	client := &fakeClient{torrents: []delugeclient.TorrentDetail{tor}}
	cache := &fakeCache{}
	deps := KindDeps{
		Store:     guardedStoreOf(t, m),
		NewWriter: w.factory,
		Client:    client,
		Cache:     cache,
		Config:    cfg,
	}
	return sharedProbeEnv{deps: deps, writer: w, client: client, store: m}, book.ID
}

func fakeKindDeps(t *testing.T) KindDeps {
	t.Helper()
	e, _ := newSharedDeps(t)
	return e.deps
}

// TestDelugeKinds_Conformance: taskstest.Conformance over every native kind
// (05-tasks 5T.9 last row).
func TestDelugeKinds_Conformance(t *testing.T) {
	taskstest.Conformance(t, (&Plugin{}).Kinds(fakeKindDeps(t), nil)...)
}

// TestDelugeKinds_MatchHEADDefs: every row compares one native kind and the HEAD
// definition it came from with the same literal row; the rows and the list are
// the same set (rule R8).
func TestDelugeKinds_MatchHEADDefs(t *testing.T) {
	p := &Plugin{}
	defs := p.Kinds(fakeKindDeps(t), nil)
	heads := map[string]sdk.OperationDef{}
	for _, h := range p.OperationDefs() {
		heads[h.ID] = h
	}
	rows := delugeRows()

	seen := map[tasks.Kind]bool{}
	for _, r := range rows {
		t.Run(r.HeadID, func(t *testing.T) {
			kind := tasks.Kind("books." + r.HeadID)
			require.False(t, seen[kind], "two rows for %s", kind)
			seen[kind] = true
			head, ok := heads[r.HeadID]
			require.True(t, ok, "HEAD has no definition %s", r.HeadID)
			c := tasks.CommonOf(byKind(t, defs, kind))
			_, err := tasks.ParseKind(string(kind))
			require.NoError(t, err)

			require.Equal(t, head.DisplayName, r.Title, "row title is HEAD's DisplayName")
			require.Equal(t, head.Description, c.Help, "Help is HEAD's Description verbatim")
			require.Equal(t, r.Title, c.Title)
			require.Equal(t, r.HeadResume, head.ResumePolicy, "row's HEAD resume is HEAD's")
			require.Equal(t, r.Resume, c.Resume)
			require.Equal(t, r.Priority, c.Priority)
			require.Equal(t, kindHeadPriority(head.DefaultPriority), c.Priority, "HEAD's priority maps to the native one")
			require.Equal(t, head.Timeout, c.Timeout, "timeout is carried unchanged")
			require.Equal(t, r.Timeout, c.Timeout)
			require.False(t, head.NoWarmupWait, "all four wait for the memdb")
			require.Equal(t, []tasks.ReadySignal{"books.memdb"}, c.Ready)
			require.Equal(t, r.HeadKey, head.ConcurrencyKey)
			require.Equal(t, tasks.Key("books."+r.HeadKey), c.ConcurrencyKey)
			require.Empty(t, c.NotWhile, "no HEAD def has DependsOn")
			require.Equal(t, r.HeadPerms, head.Permissions)
			require.Equal(t, rbac.KindAllOf, c.Perms.Start.Kind())
			require.Equal(t, rbac.Requirement(rbac.AllOf(r.Scopes...)), c.Perms.Start)
			require.Empty(t, c.Perms.StartableByViewers)
			require.Nil(t, c.Schedule, "sched is nil in the test; no conversion declares a schedule itself")

			require.Equal(t, r.ReadOnly, c.Effects.IsReadOnly())
			require.Equal(t, refStrings(r.Writes), refStrings2(c.Effects.WriteSet()), "write set")
			require.Empty(t, c.Effects.ReadSet(), "no declared reads")
			require.Empty(t, c.Effects.DeleteSet(), "no AlsoDeletes: tasklint wants an owner review for each")
			// HEAD's declared Writes, where it declared any, are the native set exactly.
			var headWrites []string
			for _, w := range head.Writes {
				headWrites = append(headWrites, "books/"+string(w))
			}
			sort.Strings(headWrites)
			if len(r.HeadWrites) > 0 {
				require.Equal(t, headWrites, refStrings2(c.Effects.WriteSet()), "HEAD-declared Writes are kept exactly (rule R7)")
			} else {
				require.Empty(t, headWrites, "row says HEAD declares no Writes")
			}

			require.Equal(t, r.Par, parStrings(r.ParGot), "parallelism of the body's pools (rule R4)")
		})
	}
	var got, want []string
	for _, d := range defs {
		got = append(got, string(d.Kind()))
	}
	for k := range seen {
		want = append(want, string(k))
	}
	sort.Strings(got)
	sort.Strings(want)
	require.Equal(t, want, got, "every native kind has a row and every row a kind")
	require.Len(t, defs, len(rows))
	// The list is HEAD's OperationDefs list, in order.
	var headOrder, nativeOrder []string
	for _, h := range p.OperationDefs() {
		headOrder = append(headOrder, "books."+h.ID)
	}
	for _, d := range defs {
		nativeOrder = append(nativeOrder, string(d.Kind()))
	}
	require.Equal(t, headOrder, nativeOrder)
}

// TestDelugeKinds_WritesThroughShell: for every kind whose Effects declare
// writes, one ModeLive run on a fake host shows the deps factory called with that
// run's rc.Writer() and a write reaching the recording writer (rule R10); the
// store fake is never written; a Preview run (where the kind allows one) makes no
// write at all.
func TestDelugeKinds_WritesThroughShell(t *testing.T) {
	type probe struct {
		kind    tasks.Kind
		params  func(bookID string) string
		preview bool // the kind has a preview
		// viaClient: the write is Deluge's (path-update), so no store writer is
		// built and the client records it.
		viaClient bool
	}
	probes := []probe{
		{kind: kindFor("deluge.centralize"), params: func(string) string { return "" }},
		{kind: kindFor("deluge.link-backfill"), params: func(string) string { return "" }, preview: true},
		{kind: kindFor("deluge.path-update"), params: func(id string) string { return `{"book_id":"` + id + `"}` }, preview: true, viaClient: true},
	}
	probed := map[tasks.Kind]bool{}
	for _, pr := range probes {
		t.Run(string(pr.kind), func(t *testing.T) {
			probed[pr.kind] = true
			e, bookID := newSharedDeps(t)
			def := byKind(t, (&Plugin{}).Kinds(e.deps, nil), pr.kind)

			w := &sentinelWriter{}
			_, err := runKind(t, def, tasks.ModeLive, w, pr.params(bookID))
			require.NoError(t, err)
			if pr.viaClient {
				require.Empty(t, e.writer.writers(), "path-update writes through Deluge: no store writer is built")
				require.Equal(t, []string{linkTorrentID(7) + " -> " + e.deps.Config.RootDir + "/Long Halloween"}, e.client.moveList(),
					"the move reached the client")
			} else {
				ws := e.writer.writers()
				require.NotEmpty(t, ws, "the deps factory was never called with the run's writer")
				for _, got := range ws {
					require.Same(t, w, got, "the factory must be handed this run's rc.Writer()")
				}
				require.NotEmpty(t, e.writer.recorded(), "no write reached the recording writer")
			}

			// Preview: nothing is written, no writer is built, Deluge is not asked to move.
			e2, bookID2 := newSharedDeps(t)
			def2 := byKind(t, (&Plugin{}).Kinds(e2.deps, nil), pr.kind)
			_, perr := runKind(t, def2, tasks.ModePreview, &sentinelWriter{}, pr.params(bookID2))
			if !pr.preview {
				require.Error(t, perr, "a live-only kind refuses a Preview run")
				require.Contains(t, perr.Error(), "preview not supported")
			} else {
				require.NoError(t, perr)
			}
			require.Empty(t, e2.writer.writers(), "a Preview run builds no writer")
			require.Empty(t, e2.writer.recorded(), "a Preview run writes nothing")
			require.Empty(t, e2.client.moveList(), "a Preview run moves no torrent")
		})
	}
	// Every kind that declares writes has a probe; a read-only kind has none.
	for _, d := range (&Plugin{}).Kinds(fakeKindDeps(t), nil) {
		c := tasks.CommonOf(d)
		if c.Effects.IsReadOnly() {
			require.False(t, probed[d.Kind()], "%s is ReadOnly but has a write probe", d.Kind())
			continue
		}
		require.True(t, probed[d.Kind()], "%s declares writes but has no write probe", d.Kind())
	}
}

// TestDelugeKinds_ParamsRefuseModeKeys: a native shell refuses dry_run, dryRun and
// apply (any case) instead of dropping them: a dropped {"dry_run":true} would turn
// a dry-run start into a live write. Params that are not an object are refused,
// and null/empty params decode to the zero value.
func TestDelugeKinds_ParamsRefuseModeKeys(t *testing.T) {
	for _, d := range (&Plugin{}).Kinds(fakeKindDeps(t), nil) {
		for _, key := range []string{"dry_run", "dryRun", "apply", "DRY_RUN", "Apply"} {
			t.Run(string(d.Kind())+"/"+key, func(t *testing.T) {
				e, _ := newSharedDeps(t)
				def := byKind(t, (&Plugin{}).Kinds(e.deps, nil), d.Kind())
				_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, `{"`+key+`":true,"book_id":"x"}`)
				require.Error(t, err)
				require.Contains(t, err.Error(), "the run's mode comes from the start request")
				require.Empty(t, e.writer.recorded())
				require.Empty(t, e.client.moveList())
			})
		}
		t.Run(string(d.Kind())+"/not-an-object", func(t *testing.T) {
			e, _ := newSharedDeps(t)
			def := byKind(t, (&Plugin{}).Kinds(e.deps, nil), d.Kind())
			_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, `[1,2]`)
			require.Error(t, err)
		})
	}
}
