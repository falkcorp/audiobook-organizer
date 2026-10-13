// file: internal/plugins/deluge/link_backfill_test.go
// version: 1.4.2
// guid: 5df77016-851d-46b8-b842-bfe9014b4e37
// last-edited: 2026-10-13

package deluge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
)

// All fixtures are synthetic: "Example Book", /downloads/example under a temp
// dir, and 40-hex torrent ids built here.

const linkTestLabel = "audiobooks"

func linkTorrentID(n int) string { return fmt.Sprintf("%040x", 0xabc000+n) }

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// --- fake Deluge ------------------------------------------------------------

type fakeDeluge struct {
	mu       sync.Mutex
	calls    []string
	torrents []delugeclient.TorrentDetail
}

func (f *fakeDeluge) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeDeluge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
		ID     int64             `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.calls = append(f.calls, req.Method)
	torrents := f.torrents
	f.mu.Unlock()

	var result any
	switch req.Method {
	case "auth.login":
		result = true
	case "core.get_torrents_status":
		var filter struct {
			ID []string `json:"id"`
		}
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params[0], &filter)
		}
		if filter.ID == nil { // light listing
			m := map[string]delugeclient.TorrentStatus{}
			for _, t := range torrents {
				m[t.Hash] = t.TorrentStatus
			}
			result = m
		} else { // detail listing
			want := map[string]bool{}
			for _, id := range filter.ID {
				want[id] = true
			}
			m := map[string]delugeclient.TorrentDetail{}
			for _, t := range torrents {
				if want[t.Hash] {
					m[t.Hash] = t
				}
			}
			result = m
		}
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": req.ID, "result": nil,
			"error": map[string]any{"message": "unexpected method " + req.Method, "code": 2},
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"id": req.ID, "result": result, "error": nil})
}

func newFakeDelugeClient(t *testing.T, torrents ...delugeclient.TorrentDetail) (*fakeDeluge, *delugeclient.Client) {
	t.Helper()
	fd := &fakeDeluge{torrents: torrents}
	srv := httptest.NewServer(fd)
	t.Cleanup(srv.Close)
	c, err := delugeclient.New(srv.URL, "test-password")
	if err != nil {
		t.Fatal(err)
	}
	return fd, c
}

// --- fixture ----------------------------------------------------------------

type linkFixture struct {
	root      string
	downloads string
}

func newLinkFixture(t *testing.T) *linkFixture {
	t.Helper()
	tmp := t.TempDir()
	f := &linkFixture{
		root:      filepath.Join(tmp, "library"),
		downloads: filepath.Join(tmp, "downloads", "example"),
	}
	for _, d := range []string{f.root, f.downloads} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	origRoot, origLabel := config.AppConfig.RootDir, config.AppConfig.DelugeDiscoveryLabel
	config.AppConfig.RootDir = f.root
	config.AppConfig.DelugeDiscoveryLabel = linkTestLabel
	t.Cleanup(func() {
		config.AppConfig.RootDir = origRoot
		config.AppConfig.DelugeDiscoveryLabel = origLabel
	})
	return f
}

type tfile struct {
	rel     string
	content []byte
}

// torrent writes each file under savePath and returns the torrent detail.
func (f *linkFixture) torrent(t *testing.T, n int, savePath string, files ...tfile) delugeclient.TorrentDetail {
	t.Helper()
	d := delugeclient.TorrentDetail{
		TorrentStatus: delugeclient.TorrentStatus{
			Hash: linkTorrentID(n), Name: "Example Book", SavePath: savePath, Label: linkTestLabel,
		},
	}
	for i, tf := range files {
		p := filepath.Join(savePath, tf.rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, tf.content, 0o644); err != nil {
			t.Fatal(err)
		}
		d.Files = append(d.Files, delugeclient.TorrentFile{Index: i, Path: tf.rel, Size: int64(len(tf.content))})
		d.TotalSize += int64(len(tf.content))
	}
	return d
}

// libraryRow is a book_file row whose file lives in the library.
func (f *linkFixture) libraryRow(id string, rel string, content []byte) database.BookFile {
	return database.BookFile{
		ID:       id,
		BookID:   "book-" + id,
		FilePath: filepath.Join(f.root, "Example Author", "Example Book", rel),
		FileSize: int64(len(content)),
		FileHash: sha256Hex(content), // below filehash.Threshold the digest is the whole-file SHA-256
		Format:   "mp3",
	}
}

// --- mock store -------------------------------------------------------------

type linkMock struct {
	*database.MockStore
	mu          sync.Mutex
	rows        map[string]database.BookFile
	modifyCalls atomic.Int64
	forbidden   []string
	// beforeFn, when set, mutates the stored row after the op read it and
	// before ModifyBookFile hands it to the re-check.
	beforeFn func(row *database.BookFile)
	// hideHash makes BookFilesWithHash miss a hash (an index that does not
	// hold the row), so rule (d) is the rule that finds it.
	hideHash map[string]bool
	// deletedBooks are soft-deleted; missingBooks do not resolve. Every other
	// book ID GetBooksByIDs is asked for is a live book.
	deletedBooks map[string]bool
	missingBooks map[string]bool
	// versions answers GetBookVersionsByBookID; versionErr fails it for a book.
	versions   map[string][]database.BookVersion
	versionErr map[string]bool
	// onHashLookup, when set, runs at the start of every BookFilesWithHash
	// call, outside the mock's lock.
	onHashLookup func()
}

func newLinkMock(rows ...database.BookFile) *linkMock {
	m := &linkMock{
		MockStore: &database.MockStore{}, rows: map[string]database.BookFile{}, hideHash: map[string]bool{},
		deletedBooks: map[string]bool{}, missingBooks: map[string]bool{}, versions: map[string][]database.BookVersion{},
		versionErr: map[string]bool{},
	}
	for _, r := range rows {
		m.rows[r.ID] = r
	}
	forbid := func(name string) { m.mu.Lock(); m.forbidden = append(m.forbidden, name); m.mu.Unlock() }
	m.GetAllBookFilesCoreCompleteFunc = func() ([]database.BookFileCore, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		out := make([]database.BookFileCore, 0, len(m.rows))
		for _, r := range m.rows {
			out = append(out, r.Core())
		}
		return out, nil
	}
	m.GetBooksByIDsFunc = func(ids []string) ([]database.Book, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		var out []database.Book
		for _, id := range ids {
			if m.missingBooks[id] {
				continue
			}
			b := database.Book{ID: id, Title: "Example Book"}
			if m.deletedBooks[id] {
				b.MarkedForDeletion = new(true)
			}
			out = append(out, b)
		}
		return out, nil
	}
	m.GetBookVersionsByBookIDFunc = func(bookID string) ([]database.BookVersion, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.versionErr[bookID] {
			return nil, fmt.Errorf("example version read failure for %s", bookID)
		}
		return m.versions[bookID], nil
	}
	m.BookFilesWithHashFunc = func(h string) ([]database.BookFile, error) {
		if m.onHashLookup != nil {
			m.onHashLookup()
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.hideHash[h] {
			return nil, nil
		}
		var out []database.BookFile
		for _, r := range m.rows {
			if r.FileHash == h || r.OriginalFileHash == h {
				out = append(out, r)
			}
		}
		return out, nil
	}
	m.BookFilesAtPathFunc = func(p string) ([]database.BookFile, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		var out []database.BookFile
		for _, r := range m.rows {
			if r.FilePath == p {
				out = append(out, r)
			}
		}
		return out, nil
	}
	m.ModifyBookFileFunc = func(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
		m.modifyCalls.Add(1)
		m.mu.Lock()
		defer m.mu.Unlock()
		stored, ok := m.rows[fileID]
		if !ok || stored.BookID != bookID {
			return nil, nil
		}
		if m.beforeFn != nil {
			m.beforeFn(&stored)
			m.rows[fileID] = stored
		}
		row := stored
		if err := fn(&row); err != nil {
			if err == database.ErrSkipBookFileWrite {
				return &stored, nil
			}
			return nil, err
		}
		m.rows[fileID] = row
		return &row, nil
	}
	m.UpdateBookFileFunc = func(string, *database.BookFile) error { forbid("UpdateBookFile"); return nil }
	m.UpdateBookFilesFunc = func(context.Context, []*database.BookFile, func(int, bool)) (int, error) {
		forbid("UpdateBookFiles")
		return 0, nil
	}
	m.UpdateBookFileHashesFunc = func(string, string, string, string) error { forbid("UpdateBookFileHashes"); return nil }
	m.MarkFileImportedFromDelugeFunc = func(context.Context, string, string, string) error {
		forbid("MarkFileImportedFromDeluge")
		return nil
	}
	m.DeleteBookFileFunc = func(string) error { forbid("DeleteBookFile"); return nil }
	m.DeleteBookFilesByIDsFunc = func([]string) error { forbid("DeleteBookFilesByIDs"); return nil }
	m.DeleteBookFilesForBookFunc = func(string) error { forbid("DeleteBookFilesForBook"); return nil }
	m.PatchBookFileFieldsFunc = func(string, string, database.BookFileFieldPatch) (*database.BookFile, *database.BookFile, error) {
		forbid("PatchBookFileFields")
		return nil, nil, nil
	}
	m.MoveBookFilesToBookFunc = func([]string, string, string) error { forbid("MoveBookFilesToBook"); return nil }
	m.MoveBookFilesToBookBulkFunc = func([]database.BookFileMove, string) error { forbid("MoveBookFilesToBookBulk"); return nil }
	return m
}

func (m *linkMock) row(id string) database.BookFile {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rows[id]
}

// provLinkMock adds the file provenance capability to linkMock. The embedded
// nil FileProvenanceStore supplies the methods the op never calls.
type provLinkMock struct {
	*linkMock
	database.FileProvenanceStore
	eventsByHash map[string][]database.FileEvent
	history      map[string][]database.FileEvent
}

func (p *provLinkMock) FindFileEventsByHash(h string) ([]database.FileEvent, error) {
	return p.eventsByHash[h], nil
}

func (p *provLinkMock) GetFileHistory(id string) ([]database.FileEvent, error) {
	return p.history[id], nil
}

// --- reporter ---------------------------------------------------------------

type linkTestReporter struct {
	centralizationTestReporter
	mu     sync.Mutex
	result any
}

func (r *linkTestReporter) SetResult(v any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result = v
	return nil
}

func runLink(t *testing.T, p *Plugin, params string) (linkBackfillResult, error) {
	t.Helper()
	r := &linkTestReporter{}
	err := p.runLinkBackfill(context.Background(), json.RawMessage(params), r)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.result == nil {
		return linkBackfillResult{}, err
	}
	res, ok := r.result.(linkBackfillResult)
	if !ok {
		t.Fatalf("result type %T", r.result)
	}
	return res, err
}

func linkedCount(t *testing.T, res linkBackfillResult) int64 {
	t.Helper()
	switch {
	case res.DryRun && res.WouldLink != nil && res.Linked == nil:
		return *res.WouldLink
	case !res.DryRun && res.Linked != nil && res.WouldLink == nil:
		return *res.Linked
	}
	t.Fatalf("dry_run=%v but linked=%v would_link=%v", res.DryRun, res.Linked, res.WouldLink)
	return 0
}

// --- tests ------------------------------------------------------------------

var exampleAudio = []byte("example audio bytes, chapter one")

func TestLinkBackfill_PreviewWritesNothing(t *testing.T) {
	f := newLinkFixture(t)
	tor := f.torrent(t, 1, f.downloads,
		tfile{"Example Book/01.mp3", exampleAudio},
		tfile{"Example Book/info.nfo", []byte("notes")},
	)
	row := f.libraryRow("bf-1", "01.mp3", exampleAudio)
	store := newLinkMock(row)
	_, client := newFakeDelugeClient(t, tor)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.DryRun {
		t.Fatal("default params must preview")
	}
	if got := store.modifyCalls.Load(); got != 0 {
		t.Fatalf("ModifyBookFile called %d times in preview", got)
	}
	if got := linkedCount(t, res); got != 1 {
		t.Errorf("would_link = %d, want 1", got)
	}
	if res.Torrents != 1 || res.Files != 1 || res.NonAudio != 1 {
		t.Errorf("torrents/files/non_audio = %d/%d/%d, want 1/1/1", res.Torrents, res.Files, res.NonAudio)
	}
	if res.Label != linkTestLabel {
		t.Errorf("label = %q, want the configured discovery label", res.Label)
	}
	if !reflect.DeepEqual(store.row("bf-1"), row) {
		t.Error("preview changed the row")
	}
}

func TestLinkBackfill_LiveSetsOnlyDelugeHash(t *testing.T) {
	f := newLinkFixture(t)
	tor := f.torrent(t, 1, f.downloads, tfile{"Example Book/01.mp3", exampleAudio})
	row := f.libraryRow("bf-1", "01.mp3", exampleAudio)
	store := newLinkMock(row)
	_, client := newFakeDelugeClient(t, tor)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := linkedCount(t, res); got != 1 {
		t.Fatalf("linked = %d, want 1", got)
	}
	want := row
	want.DelugeHash = linkTorrentID(1)
	if got := store.row("bf-1"); !reflect.DeepEqual(got, want) {
		t.Errorf("row differs from the original in more than DelugeHash:\n got %+v\nwant %+v", got, want)
	}
}

func TestLinkBackfill_UnverifiedNotWritten(t *testing.T) {
	f := newLinkFixture(t)
	tor := f.torrent(t, 1, f.downloads, tfile{"Example Book/01.mp3", exampleAudio})
	row := f.libraryRow("bf-1", "01.mp3", exampleAudio)
	// Rule (a) matches on path, but the row holds different content.
	row.DelugeOriginalPath = filepath.Join(f.downloads, "Example Book", "01.mp3")
	row.FileHash = sha256Hex([]byte("other content"))
	store := newLinkMock(row)
	_, client := newFakeDelugeClient(t, tor)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := linkedCount(t, res); got != 0 {
		t.Errorf("linked = %d, want 0", got)
	}
	if res.Unverified != 1 {
		t.Errorf("unverified = %d, want 1", res.Unverified)
	}
	if store.modifyCalls.Load() != 0 || store.row("bf-1").DelugeHash != "" {
		t.Error("an unverified row was written")
	}
}

func TestLinkBackfill_LegacyOriginalHashNotAccepted(t *testing.T) {
	f := newLinkFixture(t)
	tor := f.torrent(t, 1, f.downloads, tfile{"Example Book/01.mp3", exampleAudio})
	row := f.libraryRow("bf-1", "01.mp3", exampleAudio)
	row.FileHash = sha256Hex([]byte("tagged since import"))
	row.OriginalFileHash = sha256Hex(exampleAudio) // kind unknown: not accepted
	store := newLinkMock(row)
	_, client := newFakeDelugeClient(t, tor)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := linkedCount(t, res); got != 0 || res.Unverified != 1 {
		t.Errorf("linked=%d unverified=%d, want 0 and 1", got, res.Unverified)
	}

	// The same digest recorded as sampled is the pristine original: accepted.
	row.OriginalFileHashKind = database.FileHashKindSampled
	store = newLinkMock(row)
	res, err = runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := linkedCount(t, res); got != 1 {
		t.Errorf("sampled original: linked = %d, want 1", got)
	}
}

func TestLinkBackfill_AmbiguousCrossSeed(t *testing.T) {
	f := newLinkFixture(t)
	a := f.torrent(t, 1, filepath.Join(f.downloads, "a"), tfile{"Example Book/01.mp3", exampleAudio})
	b := f.torrent(t, 2, filepath.Join(f.downloads, "b"), tfile{"Example Book/01.mp3", exampleAudio})
	row := f.libraryRow("bf-1", "01.mp3", exampleAudio)
	store := newLinkMock(row)
	_, client := newFakeDelugeClient(t, a, b)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Ambiguous != 1 {
		t.Errorf("ambiguous = %d, want 1", res.Ambiguous)
	}
	if got := linkedCount(t, res); got != 0 {
		t.Errorf("linked = %d, want 0", got)
	}
	if store.modifyCalls.Load() != 0 || store.row("bf-1").DelugeHash != "" {
		t.Error("a cross-seeded row was linked to one of its torrents")
	}
}

// A torrent with a failed file still makes a row it verified ambiguous: B's
// second file is not on disk (errors=1), but B verified the row A also
// verified, so neither links it.
func TestLinkBackfill_AmbiguousCrossSeedWithFailedTorrent(t *testing.T) {
	f := newLinkFixture(t)
	a := f.torrent(t, 1, filepath.Join(f.downloads, "a"), tfile{"Example Book/01.mp3", exampleAudio})
	b := f.torrent(t, 2, filepath.Join(f.downloads, "b"), tfile{"Example Book/01.mp3", exampleAudio})
	b.Files = append(b.Files, delugeclient.TorrentFile{Index: 1, Path: "Example Book/02.mp3", Size: 10})
	row := f.libraryRow("bf-1", "01.mp3", exampleAudio)
	store := newLinkMock(row)
	_, client := newFakeDelugeClient(t, a, b)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Ambiguous != 1 || res.Errors != 1 {
		t.Errorf("ambiguous=%d errors=%d, want 1 and 1", res.Ambiguous, res.Errors)
	}
	if got := linkedCount(t, res); got != 0 || store.modifyCalls.Load() != 0 {
		t.Errorf("linked=%d modify calls=%d, want 0 and 0", got, store.modifyCalls.Load())
	}

	// B alone: its verified row is withheld because B failed, and is not
	// counted again beyond the error.
	_, client = newFakeDelugeClient(t, b)
	store = newLinkMock(row)
	res, err = runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := linkedCount(t, res); got != 0 || res.Errors != 1 || res.Ambiguous != 0 || store.modifyCalls.Load() != 0 {
		t.Errorf("failed torrent alone: linked=%d errors=%d ambiguous=%d modify=%d, want 0/1/0/0",
			got, res.Errors, res.Ambiguous, store.modifyCalls.Load())
	}
}

func TestLinkBackfill_ConflictKept(t *testing.T) {
	f := newLinkFixture(t)
	tor := f.torrent(t, 1, f.downloads, tfile{"Example Book/01.mp3", exampleAudio})
	row := f.libraryRow("bf-1", "01.mp3", exampleAudio)
	row.DelugeHash = linkTorrentID(99)
	store := newLinkMock(row)
	_, client := newFakeDelugeClient(t, tor)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Conflict != 1 {
		t.Errorf("conflict = %d, want 1", res.Conflict)
	}
	if !reflect.DeepEqual(store.row("bf-1"), row) {
		t.Error("a conflicting row was changed")
	}

	// Re-running against a row already holding this torrent's hash is a no-op.
	row.DelugeHash = linkTorrentID(1)
	store = newLinkMock(row)
	res, err = runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.AlreadyLinked != 1 || store.modifyCalls.Load() != 0 {
		t.Errorf("already_linked=%d modify calls=%d, want 1 and 0", res.AlreadyLinked, store.modifyCalls.Load())
	}
}

func TestLinkBackfill_RecheckSkips(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(*database.BookFile)
		check      func(t *testing.T, res linkBackfillResult)
		wantHashIs string
	}{
		{
			name:   "path changed",
			mutate: func(r *database.BookFile) { r.FilePath += ".moved" },
			check: func(t *testing.T, res linkBackfillResult) {
				if res.Unverified != 1 {
					t.Errorf("unverified = %d, want 1", res.Unverified)
				}
			},
		},
		{
			name:       "linked elsewhere meanwhile",
			mutate:     func(r *database.BookFile) { r.DelugeHash = linkTorrentID(77) },
			wantHashIs: linkTorrentID(77),
			check: func(t *testing.T, res linkBackfillResult) {
				if res.Conflict != 1 {
					t.Errorf("conflict = %d, want 1", res.Conflict)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLinkFixture(t)
			tor := f.torrent(t, 1, f.downloads, tfile{"Example Book/01.mp3", exampleAudio})
			row := f.libraryRow("bf-1", "01.mp3", exampleAudio)
			store := newLinkMock(row)
			store.beforeFn = tc.mutate
			_, client := newFakeDelugeClient(t, tor)

			res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if store.modifyCalls.Load() != 1 {
				t.Fatalf("modify calls = %d, want 1", store.modifyCalls.Load())
			}
			if got := linkedCount(t, res); got != 0 {
				t.Errorf("linked = %d, want 0", got)
			}
			if got := store.row("bf-1").DelugeHash; got != tc.wantHashIs {
				t.Errorf("DelugeHash = %q, want %q", got, tc.wantHashIs)
			}
			tc.check(t, res)
		})
	}
}

func TestLinkBackfill_NoDeleteNoMove(t *testing.T) {
	f := newLinkFixture(t)
	other := []byte("example audio bytes, chapter two")
	t1 := f.torrent(t, 1, f.downloads, tfile{"Example Book/01.mp3", exampleAudio}, tfile{"Example Book/cover.jpg", []byte("img")})
	t2 := f.torrent(t, 2, filepath.Join(f.downloads, "b"), tfile{"Example Book/02.mp3", other})
	linked := f.libraryRow("bf-1", "01.mp3", exampleAudio)
	conflict := f.libraryRow("bf-2", "02.mp3", other)
	conflict.DelugeHash = linkTorrentID(50)
	store := newLinkMock(linked, conflict)
	fd, client := newFakeDelugeClient(t, t1, t2)

	if _, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, m := range fd.methods() {
		if m != "auth.login" && m != "core.get_torrents_status" {
			t.Errorf("deluge saw %s; the op may only list torrents", m)
		}
	}
	if len(store.forbidden) > 0 {
		t.Errorf("store saw delete/repoint/whole-row writes: %v", store.forbidden)
	}
	if store.row("bf-1").DelugeHash != linkTorrentID(1) {
		t.Error("expected the verified row to be linked")
	}
}

func TestLinkBackfill_OutsideLibraryNotLinked(t *testing.T) {
	f := newLinkFixture(t)
	tor := f.torrent(t, 1, f.downloads, tfile{"Example Book/01.mp3", exampleAudio})
	// A never-imported row naming the seeding copy matches its own content
	// hash; linking it would make the bulk importers copy it in.
	seeding := database.BookFile{
		ID: "bf-seed", BookID: "book-seed",
		FilePath: filepath.Join(f.downloads, "Example Book", "01.mp3"),
		FileSize: int64(len(exampleAudio)), FileHash: sha256Hex(exampleAudio),
	}
	store := newLinkMock(seeding)
	_, client := newFakeDelugeClient(t, tor)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.OutsideLibrary != 1 || linkedCount(t, res) != 0 || store.modifyCalls.Load() != 0 {
		t.Errorf("outside_library=%d linked=%d modify=%d, want 1/0/0",
			res.OutsideLibrary, linkedCount(t, res), store.modifyCalls.Load())
	}
}

func TestLinkRunOptions_ConcurrencyAtLeastTwo(t *testing.T) {
	tally := &linkTally{}
	if got := linkRunOptions(4, 10, tally).Concurrency; got < 2 {
		t.Errorf("linkRunOptions(4).Concurrency = %d, want >= 2", got)
	}
	got := linkRunOptions(0, 10, tally).Concurrency
	if got < 1 || got != linkDefaultWorkers() {
		t.Errorf("linkRunOptions(0).Concurrency = %d, want the default %d (>= 1)", got, linkDefaultWorkers())
	}
	if got := linkRunOptions(100, 10, tally).Concurrency; got != linkMaxWorkers {
		t.Errorf("linkRunOptions(100).Concurrency = %d, want the cap %d", got, linkMaxWorkers)
	}
}

func TestLinkBackfill_ProvenanceUnavailable(t *testing.T) {
	f := newLinkFixture(t)
	a1, b1, d1 := []byte("rule a audio"), []byte("rule b audio"), []byte("rule d audio")
	ta := f.torrent(t, 1, filepath.Join(f.downloads, "a"), tfile{"Example Book/01.mp3", a1})
	tb := f.torrent(t, 2, filepath.Join(f.downloads, "b"), tfile{"Example Book/02.mp3", b1})
	td := f.torrent(t, 3, filepath.Join(f.downloads, "d"), tfile{"Example Book/03.mp3", d1})

	ra := f.libraryRow("bf-a", "01.mp3", a1)
	ra.DelugeOriginalPath = filepath.Join(f.downloads, "a", "Example Book", "01.mp3")
	rb := f.libraryRow("bf-b", "02.mp3", b1)
	rd := f.libraryRow("bf-d", "03.mp3", d1) // same basename and size as the torrent file
	store := newLinkMock(ra, rb, rd)
	store.hideHash[sha256Hex(d1)] = true // only rule (d) can find it
	if _, ok := database.AsCapability[database.FileProvenanceStore](store); ok {
		t.Fatal("fixture error: the mock exposes file provenance")
	}
	_, client := newFakeDelugeClient(t, ta, tb, td)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run must succeed without provenance: %v", err)
	}
	if res.ProvenanceUnavailable != 1 {
		t.Errorf("provenance_unavailable = %d, want 1", res.ProvenanceUnavailable)
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "provenance_unavailable") {
		t.Errorf("notes do not mention provenance_unavailable: %v", res.Notes)
	}
	if got := linkedCount(t, res); got != 3 {
		t.Errorf("linked = %d, want 3 (rules a, b, d)", got)
	}
	for id, n := range map[string]int{"bf-a": 1, "bf-b": 2, "bf-d": 3} {
		if got := store.row(id).DelugeHash; got != linkTorrentID(n) {
			t.Errorf("%s DelugeHash = %q, want %q", id, got, linkTorrentID(n))
		}
	}
}

func TestLinkBackfill_ProvenanceRuleLinks(t *testing.T) {
	f := newLinkFixture(t)
	tor := f.torrent(t, 1, f.downloads, tfile{"Example Book/01.mp3", exampleAudio})
	h := sha256Hex(exampleAudio)
	// The library copy was tagged since import: its file_hash moved on and no
	// sampled original was kept, but the provenance chain recorded the bytes.
	row := f.libraryRow("bf-1", "renamed-01.mp3", exampleAudio)
	row.FileHash = sha256Hex([]byte("tagged bytes"))
	row.FileSize += 100
	store := &provLinkMock{
		linkMock: newLinkMock(row),
		eventsByHash: map[string][]database.FileEvent{
			h: {{BookFileID: "bf-1", Path: row.FilePath, Digest: database.FileDigest{SHA256Full: h, SizeBytes: int64(len(exampleAudio))}}},
		},
		// The provenance writers record SHA256Full and SizeBytes only
		// (file_provenance_capture.go, write_tags_safe.go): no SHA256Chunk.
		history: map[string][]database.FileEvent{
			"bf-1": {
				{BookFileID: "bf-1", Path: row.FilePath, Digest: database.FileDigest{SHA256Full: h, SizeBytes: int64(len(exampleAudio))}},
				{BookFileID: "bf-1", Path: row.FilePath, Digest: database.FileDigest{SHA256Full: row.FileHash, SizeBytes: row.FileSize}},
			},
		},
	}
	if _, ok := database.AsCapability[database.FileProvenanceStore](store); !ok {
		t.Fatal("fixture error: the wrapper does not expose file provenance")
	}
	_, client := newFakeDelugeClient(t, tor)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.ProvenanceUnavailable != 0 {
		t.Errorf("provenance_unavailable = %d, want 0", res.ProvenanceUnavailable)
	}
	if got := linkedCount(t, res); got != 1 {
		t.Fatalf("linked = %d, want 1 (rule c + earliest provenance digest)", got)
	}
	if store.row("bf-1").DelugeHash != linkTorrentID(1) {
		t.Error("row not linked")
	}

	// An earliest event of another size does not verify.
	store.history["bf-1"][0].Digest.SizeBytes++
	store.linkMock = newLinkMock(row)
	res, err = runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := linkedCount(t, res); got != 0 || res.Unverified != 1 {
		t.Errorf("size mismatch: linked=%d unverified=%d, want 0 and 1", got, res.Unverified)
	}
}

func TestLinkBackfill_DryRunDisagree(t *testing.T) {
	f := newLinkFixture(t)
	tor := f.torrent(t, 1, f.downloads, tfile{"Example Book/01.mp3", exampleAudio})
	store := newLinkMock(f.libraryRow("bf-1", "01.mp3", exampleAudio))
	_, client := newFakeDelugeClient(t, tor)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false,"dryRun":true}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.DryRun || store.modifyCalls.Load() != 0 {
		t.Errorf("dry_run=%v modify calls=%d; a disagreement must preview", res.DryRun, store.modifyCalls.Load())
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "disagree") {
		t.Errorf("notes do not explain the preview: %v", res.Notes)
	}
}

func TestLinkBackfill_DelugeUnavailableFails(t *testing.T) {
	f := newLinkFixture(t)
	store := newLinkMock(f.libraryRow("bf-1", "01.mp3", exampleAudio))

	if _, err := runLink(t, &Plugin{store: store}, `{"dry_run":false}`); err == nil {
		t.Error("nil client: want an error")
	}

	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	client, err := delugeclient.New(url, "test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`); err == nil {
		t.Error("unreachable deluge: want an error")
	}
	if store.modifyCalls.Load() != 0 {
		t.Error("a failed run wrote")
	}
}

// TestLinkedRowBecomesBulkImportCandidate pins the 10-PR2 decision on bulk
// import candidacy: a row this op links has DelugeHash set and
// ImportedFromDelugeAt nil, so GetBookFilesNeedingDelugeImportCore returns it,
// and /discovery/import, deluge.centralize and maintenance.bulk-deluge-import
// (all manual) will see it. That is accepted because the op links rows
// already inside the library root, where all three resolve the destination to
// the file's own path, copy nothing, and mark the row imported in place
// (ImportedFromDelugeAt only), so it leaves the list after one pass. Until
// review round 1 the last two skipped it without marking it and it stayed
// listed forever; the second half of this test runs deluge.centralize on the
// linked row (bulk-deluge-import's twin is
// TestBulkDelugeImportJob_LinkedRowMarkedInPlace in internal/maintenance/jobs).
// If the first half starts failing because the row is no longer a candidate,
// the decision changed: update the brief.
func TestLinkedRowBecomesBulkImportCandidate(t *testing.T) {
	f := newLinkFixture(t)
	store := dbtest.NewStore(t)

	book, err := store.CreateBook(&database.Book{Title: "Example Book", FilePath: filepath.Join(f.root, "Example Author", "Example Book")})
	if err != nil {
		t.Fatal(err)
	}
	row := f.libraryRow("", "01.mp3", exampleAudio)
	row.ID = ""
	row.BookID = book.ID
	if err := store.CreateBookFile(&row); err != nil {
		t.Fatal(err)
	}
	before, err := store.GetBookFilesNeedingDelugeImportCore()
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 0 {
		t.Fatalf("fixture error: %d candidates before the link", len(before))
	}

	tor := f.torrent(t, 1, f.downloads, tfile{"Example Book/01.mp3", exampleAudio})
	_, client := newFakeDelugeClient(t, tor)
	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := linkedCount(t, res); got != 1 {
		t.Fatalf("linked = %d, want 1", got)
	}

	after, err := store.GetBookFilesNeedingDelugeImportCore()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].ID != row.ID {
		t.Fatalf("candidates after link = %+v, want exactly the linked row", after)
	}
	c := after[0]
	if c.DelugeHash != linkTorrentID(1) || c.ImportedFromDelugeAt != nil || c.DelugeOriginalPath != "" || c.DownloadHash != "" {
		t.Errorf("linked row: deluge_hash=%q imported_at=%v original_path=%q download_hash=%q; want only deluge_hash set",
			c.DelugeHash, c.ImportedFromDelugeAt, c.DelugeOriginalPath, c.DownloadHash)
	}
	if !underLibraryRoot(f.root, c.FilePath) {
		t.Errorf("linked row %s is outside the library root", c.FilePath)
	}

	// deluge.centralize on the linked row: nothing copied, the row marked
	// imported in place and off the list, its path and hashes unchanged.
	if err := (&Plugin{store: store}).runCentralization(context.Background(), nil, &centralizationTestReporter{}); err != nil {
		t.Fatalf("centralize: %v", err)
	}
	left, err := store.GetBookFilesNeedingDelugeImportCore()
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("candidates after centralize = %+v, want none", left)
	}
	got, err := store.GetBookFileByID(book.ID, row.ID)
	if err != nil || got == nil {
		t.Fatalf("read row: %+v, %v", got, err)
	}
	if got.ImportedFromDelugeAt == nil || got.FilePath != row.FilePath || got.DelugeOriginalPath != "" ||
		got.DelugeHash != linkTorrentID(1) || got.FileHash != row.FileHash {
		t.Errorf("after centralize: imported_at=%v path=%q original_path=%q deluge_hash=%q file_hash=%q; "+
			"want only imported_at set", got.ImportedFromDelugeAt, got.FilePath, got.DelugeOriginalPath, got.DelugeHash, got.FileHash)
	}
	// The fixture never created the library directory; centralize must not
	// have created it (or a copy in it) either.
	if _, err := os.Stat(filepath.Dir(row.FilePath)); !os.IsNotExist(err) {
		t.Errorf("centralize created the library dir (stat err %v)", err)
	}
}

func TestLinkBackfill_Race(t *testing.T) {
	f := newLinkFixture(t)
	const n = 50
	var torrents []delugeclient.TorrentDetail
	var rows []database.BookFile
	for i := range n {
		c1 := fmt.Appendf(nil, "example audio %d part 1", i)
		c2 := fmt.Appendf(nil, "example audio %d part 2", i)
		save := filepath.Join(f.downloads, fmt.Sprintf("t%02d", i))
		torrents = append(torrents, f.torrent(t, i, save,
			tfile{"Example Book/01.mp3", c1}, tfile{"Example Book/02.mp3", c2}, tfile{"Example Book/info.nfo", []byte("x")}))
		r1 := f.libraryRow(fmt.Sprintf("bf-%02d-1", i), fmt.Sprintf("%02d-01.mp3", i), c1)
		r2 := f.libraryRow(fmt.Sprintf("bf-%02d-2", i), fmt.Sprintf("%02d-02.mp3", i), c2)
		rows = append(rows, r1, r2)
	}
	store := newLinkMock(rows...)
	_, client := newFakeDelugeClient(t, torrents...)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false,"workers":4}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := linkedCount(t, res); got != 2*n {
		t.Errorf("linked = %d, want %d", got, 2*n)
	}
	if res.Torrents != n || res.Files != 2*n || res.NonAudio != n || res.Errors != 0 {
		t.Errorf("torrents/files/non_audio/errors = %d/%d/%d/%d", res.Torrents, res.Files, res.NonAudio, res.Errors)
	}
	var ids []string
	for id, r := range store.rows {
		if r.DelugeHash == "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > 0 {
		t.Errorf("unlinked rows: %v", ids)
	}
}

// TestLinkBackfill_MemDBNotServingFailsClosed pins what the op does while the
// store's memdb is not serving (the ~130s of boot warmup in production):
// BookFilesWithHash refuses rather than answering from a possibly short list,
// so the torrent is counted as an error, links nothing, and writes nothing.
// It is the deterministic form of the state tests used to hit by accident when
// they opened a store with database.NewPebbleStore and ran the op before the
// async warmup finished (TestLinkedRowBecomesBulkImportCandidate flaked as
// "linked = 0, want 1" under the full -race package run). Every test that
// needs the memdb-backed lookup must build its store with dbtest.NewStore,
// which waits for warmup.
func TestLinkBackfill_MemDBNotServingFailsClosed(t *testing.T) {
	f := newLinkFixture(t)
	store := dbtest.NewStore(t, dbtest.WithoutMemDB())
	book, err := store.CreateBook(&database.Book{Title: "Example Book", FilePath: filepath.Join(f.root, "Example Author", "Example Book")})
	if err != nil {
		t.Fatal(err)
	}
	row := f.libraryRow("", "01.mp3", exampleAudio)
	row.ID = ""
	row.BookID = book.ID
	if err := store.CreateBookFile(&row); err != nil {
		t.Fatal(err)
	}

	tor := f.torrent(t, 1, f.downloads, tfile{"Example Book/01.mp3", exampleAudio})
	_, client := newFakeDelugeClient(t, tor)
	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := linkedCount(t, res); got != 0 || res.Errors != 1 {
		t.Fatalf("linked = %d errors = %d, want 0 and 1 (fail closed, reported)", got, res.Errors)
	}
	after, err := store.GetBookFilesNeedingDelugeImportCore()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Errorf("a failed lookup linked %d row(s): %+v", len(after), after)
	}
}

// TestLinkBackfill_PreviewSandboxPebble is the brief's sandbox preview: a real
// Pebble store with linkable, conflicting and outside-library rows. The preview
// reports its counts and changes no row (row count and every row compared
// before and after).
func TestLinkBackfill_PreviewSandboxPebble(t *testing.T) {
	f := newLinkFixture(t)
	store := dbtest.NewStore(t)
	book, err := store.CreateBook(&database.Book{Title: "Example Book", FilePath: filepath.Join(f.root, "Example Author", "Example Book")})
	if err != nil {
		t.Fatal(err)
	}

	c1, c2, c3 := []byte("sandbox chapter 1"), []byte("sandbox chapter 2"), []byte("sandbox chapter 3")
	tor := f.torrent(t, 1, f.downloads,
		tfile{"Example Book/01.mp3", c1}, tfile{"Example Book/02.mp3", c2},
		tfile{"Example Book/03.mp3", c3}, tfile{"Example Book/cover.jpg", []byte("img")})
	linkable := f.libraryRow("", "01.mp3", c1)
	conflicting := f.libraryRow("", "02.mp3", c2)
	conflicting.DelugeHash = linkTorrentID(42)
	seeding := database.BookFile{FilePath: filepath.Join(f.downloads, "Example Book", "03.mp3"), FileSize: int64(len(c3)), FileHash: sha256Hex(c3)}
	for _, r := range []*database.BookFile{&linkable, &conflicting, &seeding} {
		r.ID, r.BookID = "", book.ID
		if err := store.CreateBookFile(r); err != nil {
			t.Fatal(err)
		}
	}
	before, err := store.GetAllBookFilesCore()
	if err != nil {
		t.Fatal(err)
	}

	_, client := newFakeDelugeClient(t, tor)
	res, err := runLink(t, &Plugin{client: client, store: store}, `{}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	t.Logf("sandbox preview: torrents=%d files=%d would_link=%d already_linked=%d unverified=%d ambiguous=%d conflict=%d non_audio=%d errors=%d provenance_unavailable=%d unmatched=%d outside_library=%d",
		res.Torrents, res.Files, linkedCount(t, res), res.AlreadyLinked, res.Unverified, res.Ambiguous,
		res.Conflict, res.NonAudio, res.Errors, res.ProvenanceUnavailable, res.Unmatched, res.OutsideLibrary)
	if !res.DryRun || linkedCount(t, res) != 1 || res.Conflict != 1 || res.OutsideLibrary != 1 || res.NonAudio != 1 || res.ProvenanceUnavailable != 0 {
		t.Errorf("unexpected preview counts: %+v", res)
	}

	after, err := store.GetAllBookFilesCore()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("row count %d -> %d", len(before), len(after))
	}
	byID := map[string]database.BookFileCore{}
	for _, r := range before {
		byID[r.ID] = r
	}
	for _, r := range after {
		if !reflect.DeepEqual(byID[r.ID], r) {
			t.Errorf("preview changed row %s", r.ID)
		}
	}
	got, err := store.GetBookFileByID(book.ID, linkable.ID)
	if err != nil || got == nil || got.DelugeHash != "" {
		t.Errorf("sampled row after preview: %+v, %v", got, err)
	}
}

// --- review round 1 (B1, S1, S4-S7, nits) -----------------------------------

// B1: an empty torrent file has the digest of every empty file, so it would
// "verify" against empty library rows of unrelated books. It is skipped: only
// the torrent's own row is linked, and the unrelated rows are not ambiguous,
// just never considered.
func TestLinkBackfill_ZeroByteFileSkipped(t *testing.T) {
	f := newLinkFixture(t)
	tor := f.torrent(t, 1, f.downloads,
		tfile{"Example Book/01.mp3", exampleAudio},
		tfile{"Example Book/00-intro.mp3", []byte{}})
	own := f.libraryRow("bf-own", "01.mp3", exampleAudio)
	other1 := f.libraryRow("bf-x", "x.mp3", []byte{})
	other1.BookID = "book-unrelated-1"
	other2 := f.libraryRow("bf-y", "y.mp3", []byte{})
	other2.BookID = "book-unrelated-2"
	store := newLinkMock(own, other1, other2)
	_, client := newFakeDelugeClient(t, tor)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := linkedCount(t, res); got != 1 || res.Ambiguous != 0 || res.EmptyFiles != 1 || res.Files != 1 {
		t.Errorf("linked=%d ambiguous=%d empty_files=%d files=%d, want 1/0/1/1", got, res.Ambiguous, res.EmptyFiles, res.Files)
	}
	if store.row("bf-own").DelugeHash != linkTorrentID(1) {
		t.Error("the torrent's own row was not linked")
	}
	for _, id := range []string{"bf-x", "bf-y"} {
		if got := store.row(id).DelugeHash; got != "" {
			t.Errorf("%s (an unrelated book's empty file) was linked: %q", id, got)
		}
	}
}

// B1: one torrent file whose verified rows belong to two live books links
// neither; rows of one book that hold the same bytes are still linked.
func TestLinkBackfill_FileSpanningBooksAmbiguous(t *testing.T) {
	f := newLinkFixture(t)
	shared := []byte("example shared intro bytes")
	tor := f.torrent(t, 1, f.downloads,
		tfile{"Example Book/00-intro.mp3", shared}, tfile{"Example Book/01.mp3", exampleAudio})
	a := f.libraryRow("bf-a", "intro-a.mp3", shared)
	b := f.libraryRow("bf-b", "intro-b.mp3", shared) // another book, same bytes
	own := f.libraryRow("bf-own", "01.mp3", exampleAudio)
	dup := f.libraryRow("bf-own-dup", "01-copy.mp3", exampleAudio)
	dup.BookID = own.BookID // same book: not ambiguous
	store := newLinkMock(a, b, own, dup)
	_, client := newFakeDelugeClient(t, tor)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Ambiguous != 2 {
		t.Errorf("ambiguous = %d, want 2 (both rows of the two-book file)", res.Ambiguous)
	}
	if got := linkedCount(t, res); got != 2 {
		t.Errorf("linked = %d, want 2 (the one-book file's two rows)", got)
	}
	for id, want := range map[string]string{"bf-a": "", "bf-b": "", "bf-own": linkTorrentID(1), "bf-own-dup": linkTorrentID(1)} {
		if got := store.row(id).DelugeHash; got != want {
			t.Errorf("%s DelugeHash = %q, want %q", id, got, want)
		}
	}
}

// S4: rows of a soft-deleted or missing book are dropped before the
// one-book-per-file check, so they neither link nor make the live row
// ambiguous. The books are read in one batch.
func TestLinkBackfill_DeletedBookRowsDropped(t *testing.T) {
	f := newLinkFixture(t)
	tor := f.torrent(t, 1, f.downloads, tfile{"Example Book/01.mp3", exampleAudio})
	live := f.libraryRow("bf-live", "01.mp3", exampleAudio)
	trashed := f.libraryRow("bf-trashed", "01-old.mp3", exampleAudio)
	gone := f.libraryRow("bf-gone", "01-gone.mp3", exampleAudio)
	store := newLinkMock(live, trashed, gone)
	store.deletedBooks[trashed.BookID] = true
	store.missingBooks[gone.BookID] = true
	var batches atomic.Int64
	inner := store.GetBooksByIDsFunc
	store.GetBooksByIDsFunc = func(ids []string) ([]database.Book, error) {
		batches.Add(1)
		return inner(ids)
	}
	_, client := newFakeDelugeClient(t, tor)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := linkedCount(t, res); got != 1 || res.BookDeleted != 2 || res.Ambiguous != 0 {
		t.Errorf("linked=%d book_deleted=%d ambiguous=%d, want 1/2/0", got, res.BookDeleted, res.Ambiguous)
	}
	if store.row("bf-live").DelugeHash != linkTorrentID(1) || store.row("bf-trashed").DelugeHash != "" || store.row("bf-gone").DelugeHash != "" {
		t.Error("want only the live book's row linked")
	}
	if batches.Load() != 1 {
		t.Errorf("GetBooksByIDs calls = %d, want 1 batch", batches.Load())
	}
}

// S1: limit is applied after the cross-seed check, so a twin that would fall
// past the limit still makes the row ambiguous.
func TestLinkBackfill_LimitAfterCrossSeedCheck(t *testing.T) {
	f := newLinkFixture(t)
	a := f.torrent(t, 1, filepath.Join(f.downloads, "a"), tfile{"Example Book/01.mp3", exampleAudio})
	b := f.torrent(t, 2, filepath.Join(f.downloads, "b"), tfile{"Example Book/01.mp3", exampleAudio})
	store := newLinkMock(f.libraryRow("bf-1", "01.mp3", exampleAudio))
	_, client := newFakeDelugeClient(t, a, b)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false,"limit":1}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := linkedCount(t, res); got != 0 || res.Ambiguous != 1 || res.Torrents != 2 {
		t.Errorf("linked=%d ambiguous=%d torrents=%d, want 0/1/2", got, res.Ambiguous, res.Torrents)
	}
	if store.modifyCalls.Load() != 0 || store.row("bf-1").DelugeHash != "" {
		t.Error("a cross-seeded row was linked under limit=1")
	}
}

// limit caps the torrents written; the torrents left are counted in
// deferred_by_limit (torrents, not files), not dropped silently.
func TestLinkBackfill_LimitCapsWrites(t *testing.T) {
	f := newLinkFixture(t)
	var torrents []delugeclient.TorrentDetail
	var rows []database.BookFile
	for i := 1; i <= 3; i++ {
		c := fmt.Appendf(nil, "example audio for limit %d", i)
		torrents = append(torrents, f.torrent(t, i, filepath.Join(f.downloads, fmt.Sprintf("t%d", i)), tfile{"Example Book/01.mp3", c}))
		rows = append(rows, f.libraryRow(fmt.Sprintf("bf-%d", i), fmt.Sprintf("%d.mp3", i), c))
	}
	store := newLinkMock(rows...)
	_, client := newFakeDelugeClient(t, torrents...)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false,"limit":1}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := linkedCount(t, res); got != 1 || res.DeferredByLimit != 2 || res.Torrents != 3 {
		t.Errorf("linked=%d deferred_by_limit=%d torrents=%d, want 1/2/3", got, res.DeferredByLimit, res.Torrents)
	}
	// The plan is sorted by torrent hash: the lowest one is written.
	if store.row("bf-1").DelugeHash != linkTorrentID(1) || store.row("bf-2").DelugeHash != "" || store.row("bf-3").DelugeHash != "" {
		t.Error("want exactly the first planned torrent's row linked")
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "limit") {
		t.Errorf("notes do not mention the limit: %v", res.Notes)
	}
}

// The label param selects the torrents; the default is the discovery label.
func TestLinkBackfill_LabelParam(t *testing.T) {
	f := newLinkFixture(t)
	other := []byte("example audio under another label")
	def := f.torrent(t, 1, filepath.Join(f.downloads, "a"), tfile{"Example Book/01.mp3", exampleAudio})
	custom := f.torrent(t, 2, filepath.Join(f.downloads, "b"), tfile{"Example Book/02.mp3", other})
	custom.Label = "example-custom"
	store := newLinkMock(f.libraryRow("bf-1", "01.mp3", exampleAudio), f.libraryRow("bf-2", "02.mp3", other))
	_, client := newFakeDelugeClient(t, def, custom)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false,"label":" example-custom "}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Label != "example-custom" || res.Torrents != 1 || linkedCount(t, res) != 1 {
		t.Errorf("label=%q torrents=%d linked=%d, want example-custom/1/1", res.Label, res.Torrents, linkedCount(t, res))
	}
	if store.row("bf-2").DelugeHash != linkTorrentID(2) || store.row("bf-1").DelugeHash != "" {
		t.Error("want only the custom-label torrent's row linked")
	}
}

// The workers param reaches the resolve pool: two torrents resolve at once.
// Each BookFilesWithHash call (rule b) waits until a second one is in flight,
// so a pool of one would time out here.
func TestLinkBackfill_WorkersParamReachesPool(t *testing.T) {
	f := newLinkFixture(t)
	c2 := []byte("example audio, second torrent")
	a := f.torrent(t, 1, filepath.Join(f.downloads, "a"), tfile{"Example Book/01.mp3", exampleAudio})
	b := f.torrent(t, 2, filepath.Join(f.downloads, "b"), tfile{"Example Book/02.mp3", c2})
	store := newLinkMock(f.libraryRow("bf-1", "01.mp3", exampleAudio), f.libraryRow("bf-2", "02.mp3", c2))
	var inFlight, maxInFlight atomic.Int64
	both := make(chan struct{})
	var once sync.Once
	store.onHashLookup = func() {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			m := maxInFlight.Load()
			if n <= m || maxInFlight.CompareAndSwap(m, n) {
				break
			}
		}
		if n >= 2 {
			once.Do(func() { close(both) })
		}
		select {
		case <-both:
		case <-time.After(3 * time.Second):
		}
	}
	_, client := newFakeDelugeClient(t, a, b)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false,"workers":2}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if maxInFlight.Load() < 2 {
		t.Errorf("max concurrent hash lookups = %d, want 2: workers=2 did not reach the resolve pool", maxInFlight.Load())
	}
	if got := linkedCount(t, res); got != 2 {
		t.Errorf("linked = %d, want 2", got)
	}
}

// A stored hash in upper case is the same torrent: already_linked, no write.
func TestLinkBackfill_AlreadyLinkedCaseInsensitive(t *testing.T) {
	f := newLinkFixture(t)
	tor := f.torrent(t, 1, f.downloads, tfile{"Example Book/01.mp3", exampleAudio})
	row := f.libraryRow("bf-1", "01.mp3", exampleAudio)
	row.DelugeHash = strings.ToUpper(linkTorrentID(1))
	store := newLinkMock(row)
	_, client := newFakeDelugeClient(t, tor)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.AlreadyLinked != 1 || res.Conflict != 0 || store.modifyCalls.Load() != 0 {
		t.Errorf("already_linked=%d conflict=%d modify=%d, want 1/0/0", res.AlreadyLinked, res.Conflict, store.modifyCalls.Load())
	}
}

// S6: rule (a) matches on DelugeOriginalPath, but a row whose file is outside
// the library root is still refused.
func TestLinkBackfill_RuleAOutsideLibraryRefused(t *testing.T) {
	f := newLinkFixture(t)
	tor := f.torrent(t, 1, f.downloads, tfile{"Example Book/01.mp3", exampleAudio})
	torrentPath := filepath.Join(f.downloads, "Example Book", "01.mp3")
	row := database.BookFile{
		ID: "bf-out", BookID: "book-out",
		FilePath:           filepath.Join(filepath.Dir(f.root), "elsewhere", "01.mp3"),
		DelugeOriginalPath: torrentPath,
		FileSize:           int64(len(exampleAudio)), FileHash: sha256Hex(exampleAudio),
	}
	store := newLinkMock(row)
	_, client := newFakeDelugeClient(t, tor)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := linkedCount(t, res); got != 0 || res.OutsideLibrary != 1 || store.modifyCalls.Load() != 0 {
		t.Errorf("linked=%d outside_library=%d modify=%d, want 0/1/0", got, res.OutsideLibrary, store.modifyCalls.Load())
	}
}

// A library directory named "..Extras" is inside the root, as it is for the
// importers (pathutil.RelEscapesBase), so its row is linked.
func TestLinkBackfill_DotDotNamedLibraryDirLinked(t *testing.T) {
	f := newLinkFixture(t)
	tor := f.torrent(t, 1, f.downloads, tfile{"Example Book/01.mp3", exampleAudio})
	row := f.libraryRow("bf-1", "01.mp3", exampleAudio)
	row.FilePath = filepath.Join(f.root, "..Extras", "01.mp3")
	store := newLinkMock(row)
	_, client := newFakeDelugeClient(t, tor)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := linkedCount(t, res); got != 1 || res.OutsideLibrary != 0 {
		t.Errorf("linked=%d outside_library=%d, want 1/0", got, res.OutsideLibrary)
	}
}

// S7: the under-lock re-check also refuses a row whose content hashes moved
// since verification.
func TestLinkBackfill_RecheckHashes(t *testing.T) {
	for name, mutate := range map[string]func(*database.BookFile){
		"file hash changed":               func(r *database.BookFile) { r.FileHash = sha256Hex([]byte("retagged")) },
		"original file hash changed":      func(r *database.BookFile) { r.OriginalFileHash = sha256Hex([]byte("another original")) },
		"original file hash kind changed": func(r *database.BookFile) { r.OriginalFileHashKind = database.FileHashKindSampled },
	} {
		t.Run(name, func(t *testing.T) {
			f := newLinkFixture(t)
			tor := f.torrent(t, 1, f.downloads, tfile{"Example Book/01.mp3", exampleAudio})
			store := newLinkMock(f.libraryRow("bf-1", "01.mp3", exampleAudio))
			store.beforeFn = mutate
			_, client := newFakeDelugeClient(t, tor)

			res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if store.modifyCalls.Load() != 1 || linkedCount(t, res) != 0 || res.Unverified != 1 || store.row("bf-1").DelugeHash != "" {
				t.Errorf("modify=%d linked=%d unverified=%d hash=%q, want 1/0/1/empty",
					store.modifyCalls.Load(), linkedCount(t, res), res.Unverified, store.row("bf-1").DelugeHash)
			}
		})
	}
}

// S5: the earliest provenance digest verifies by SHA256Full only at or below
// filehash.Threshold, where BookFileHash is the whole-file SHA-256; above it
// only SHA256Chunk can match. Called directly: no 100 MB fixture.
func TestLinkVerify_ProvenanceDigestArms(t *testing.T) {
	const h = "example-digest"
	big := int64(filehash.Threshold) + 1
	cases := []struct {
		name   string
		digest database.FileDigest
		size   int64
		want   bool
	}{
		{"full, small file", database.FileDigest{SHA256Full: h, SizeBytes: 10}, 10, true},
		{"full, at threshold", database.FileDigest{SHA256Full: h, SizeBytes: filehash.Threshold}, filehash.Threshold, true},
		{"full, above threshold", database.FileDigest{SHA256Full: h, SizeBytes: big}, big, false},
		{"chunk, above threshold", database.FileDigest{SHA256Chunk: h, SizeBytes: big}, big, true},
		{"full, size differs", database.FileDigest{SHA256Full: h, SizeBytes: 11}, 10, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prov := &provLinkMock{linkMock: newLinkMock(), history: map[string][]database.FileEvent{
				"bf-1": {{BookFileID: "bf-1", Digest: tc.digest}},
			}}
			r := &linkRun{prov: prov}
			got, err := r.verify(linkCandidate{ID: "bf-1", FileHash: "other"}, h, tc.size)
			if err != nil || got != tc.want {
				t.Errorf("verify = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

// A deselected (priority 0) file is skipped, not a reason to refuse the
// torrent: it is usually absent from disk.
func TestLinkBackfill_DeselectedFileSkipped(t *testing.T) {
	f := newLinkFixture(t)
	tor := f.torrent(t, 1, f.downloads, tfile{"Example Book/01.mp3", exampleAudio})
	tor.Files = append(tor.Files, delugeclient.TorrentFile{Index: 1, Path: "Example Book/02.mp3", Size: 10}) // not on disk
	tor.FilePriorities = []int{1, 0}
	store := newLinkMock(f.libraryRow("bf-1", "01.mp3", exampleAudio))
	_, client := newFakeDelugeClient(t, tor)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := linkedCount(t, res); got != 1 || res.Deselected != 1 || res.Errors != 0 {
		t.Errorf("linked=%d deselected=%d errors=%d, want 1/1/0", got, res.Deselected, res.Errors)
	}
}

// When no torrent file is found on disk the paths Deluge reports are not the
// paths this server sees; the result says so.
func TestLinkBackfill_PathViewsDifferNote(t *testing.T) {
	f := newLinkFixture(t)
	tor := f.torrent(t, 1, f.downloads, tfile{"Example Book/01.mp3", exampleAudio})
	tor.SavePath = filepath.Join(f.downloads, "as-deluge-sees-it")
	store := newLinkMock(f.libraryRow("bf-1", "01.mp3", exampleAudio))
	_, client := newFakeDelugeClient(t, tor)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Errors != 1 || !strings.Contains(strings.Join(res.Notes, "\n"), "path_views_differ") {
		t.Errorf("errors=%d notes=%v, want 1 and a path_views_differ note", res.Errors, res.Notes)
	}

	// One file found is enough to drop the note.
	ok := f.torrent(t, 2, filepath.Join(f.downloads, "b"), tfile{"Example Book/02.mp3", []byte("example found audio")})
	_, client = newFakeDelugeClient(t, tor, ok)
	res, err = runLink(t, &Plugin{client: client, store: newLinkMock()}, `{}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(strings.Join(res.Notes, "\n"), "path_views_differ") {
		t.Errorf("note present although a file was found: %v", res.Notes)
	}
}

// The relink hold-back. A planned row that names no version is held
// (held_for_relink, nothing written) when its book has an active or alt
// version with no torrent hash that no row names, or its versions cannot be
// read; include_unsettled_relink=true writes it, and relink_fixer_would_link
// then counts the rows maintenance.relink-pre-fix-imports plans as would_link
// (exactly one such version, at most one hashless unlinked row), with a note
// naming the order. The other cases are the fixer's other classes.
func TestLinkBackfill_RelinkHoldBack(t *testing.T) {
	stranded := database.BookVersion{ID: "ver-1", Status: database.BookVersionStatusActive}
	cases := []struct {
		name       string
		versions   []database.BookVersion
		readErr    bool
		extra      func(f *linkFixture, row database.BookFile) []database.BookFile
		wantHeld   int64
		wantFixer  int64 // with include_unsettled_relink
		wantErrors int64
	}{
		{name: "one unnamed hashless version", versions: []database.BookVersion{stranded}, wantHeld: 1, wantFixer: 1},
		{name: "version carries a torrent hash",
			versions: []database.BookVersion{{ID: "ver-1", Status: database.BookVersionStatusActive, TorrentHash: linkTorrentID(9)}}},
		{name: "version is trashed",
			versions: []database.BookVersion{{ID: "ver-1", Status: database.BookVersionStatusTrash}}},
		{name: "two unnamed hashless versions", wantHeld: 1,
			versions: []database.BookVersion{stranded, {ID: "ver-2", Status: database.BookVersionStatusAlt}}},
		{name: "version already named by another row", versions: []database.BookVersion{stranded},
			extra: func(f *linkFixture, row database.BookFile) []database.BookFile {
				o := f.libraryRow("bf-named", "named.mp3", []byte("example other bytes"))
				o.BookID, o.VersionID = row.BookID, "ver-1"
				return []database.BookFile{o}
			}},
		{name: "two hashless unlinked rows", versions: []database.BookVersion{stranded}, wantHeld: 1,
			extra: func(f *linkFixture, row database.BookFile) []database.BookFile {
				o := f.libraryRow("bf-peer", "peer.mp3", []byte("example peer bytes"))
				o.BookID = row.BookID
				return []database.BookFile{o}
			}},
		{name: "version read fails", readErr: true, wantHeld: 1, wantErrors: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLinkFixture(t)
			tor := f.torrent(t, 1, f.downloads, tfile{"Example Book/01.mp3", exampleAudio})
			row := f.libraryRow("bf-1", "01.mp3", exampleAudio)
			rows := []database.BookFile{row}
			if tc.extra != nil {
				rows = append(rows, tc.extra(f, row)...)
			}
			_, client := newFakeDelugeClient(t, tor)
			newStore := func() *linkMock {
				m := newLinkMock(rows...)
				m.versions[row.BookID] = tc.versions
				m.versionErr[row.BookID] = tc.readErr
				return m
			}

			for _, dry := range []bool{true, false} {
				store := newStore()
				res, err := runLink(t, &Plugin{client: client, store: store}, fmt.Sprintf(`{"dry_run":%v}`, dry))
				if err != nil {
					t.Fatalf("dry_run=%v: run: %v", dry, err)
				}
				if res.HeldForRelink != tc.wantHeld || linkedCount(t, res) != 1-tc.wantHeld || res.Errors != tc.wantErrors {
					t.Errorf("dry_run=%v: held_for_relink=%d linked=%d errors=%d, want %d/%d/%d",
						dry, res.HeldForRelink, linkedCount(t, res), res.Errors, tc.wantHeld, 1-tc.wantHeld, tc.wantErrors)
				}
				if res.RelinkFixerWouldLink != 0 {
					t.Errorf("dry_run=%v: relink_fixer_would_link = %d without the override; held rows are not written", dry, res.RelinkFixerWouldLink)
				}
				if held := store.row("bf-1").DelugeHash == ""; !dry && held != (tc.wantHeld == 1) {
					t.Errorf("row left unlinked = %v, want %v", held, tc.wantHeld == 1)
				}
				hasNote := strings.Contains(strings.Join(res.Notes, "\n"), "held_for_relink")
				if hasNote != (tc.wantHeld > 0) {
					t.Errorf("dry_run=%v: held note present = %v, want %v: %v", dry, hasNote, tc.wantHeld > 0, res.Notes)
				}
			}

			store := newStore()
			res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false,"include_unsettled_relink":true}`)
			if err != nil {
				t.Fatalf("override: run: %v", err)
			}
			if res.HeldForRelink != 0 || linkedCount(t, res) != 1 || store.row("bf-1").DelugeHash != linkTorrentID(1) {
				t.Errorf("override: held_for_relink=%d linked=%d deluge_hash=%q, want 0/1/the torrent",
					res.HeldForRelink, linkedCount(t, res), store.row("bf-1").DelugeHash)
			}
			if res.RelinkFixerWouldLink != tc.wantFixer {
				t.Errorf("override: relink_fixer_would_link = %d, want %d", res.RelinkFixerWouldLink, tc.wantFixer)
			}
			hasNote := strings.Contains(strings.Join(res.Notes, "\n"), "relink-pre-fix-imports first")
			if hasNote != (tc.wantFixer > 0) {
				t.Errorf("override: order note present = %v, want %v: %v", hasNote, tc.wantFixer > 0, res.Notes)
			}
		})
	}
}

// The reviewer's flip case: book B has one hashless version V no row names
// and two hashless unlinked rows r1 and r2, both verified by one torrent. The
// fixer classes the book hashless-ambiguous today; once its owner removes one
// row it would link the other to V. Linking either first rules that out, so
// both are held -- in the preview and the live run -- and nothing is linked.
// include_unsettled_relink links both.
func TestLinkBackfill_RelinkHoldsAmbiguousBook(t *testing.T) {
	f := newLinkFixture(t)
	c2 := []byte("example audio bytes, chapter two")
	tor := f.torrent(t, 1, f.downloads, tfile{"Example Book/01.mp3", exampleAudio}, tfile{"Example Book/02.mp3", c2})
	r1, r2 := f.libraryRow("bf-r1", "01.mp3", exampleAudio), f.libraryRow("bf-r2", "02.mp3", c2)
	r2.BookID = r1.BookID
	_, client := newFakeDelugeClient(t, tor)
	newStore := func() *linkMock {
		m := newLinkMock(r1, r2)
		m.versions[r1.BookID] = []database.BookVersion{{ID: "ver-v", Status: database.BookVersionStatusActive}}
		return m
	}

	preview, err := runLink(t, &Plugin{client: client, store: newStore()}, `{}`)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.HeldForRelink != 2 || linkedCount(t, preview) != 0 {
		t.Errorf("preview: held_for_relink=%d would_link=%d, want 2 and 0", preview.HeldForRelink, linkedCount(t, preview))
	}

	store := newStore()
	live, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("live: %v", err)
	}
	if live.HeldForRelink != 2 || linkedCount(t, live) != 0 || store.modifyCalls.Load() != 0 {
		t.Errorf("live: held_for_relink=%d linked=%d modify calls=%d, want 2/0/0",
			live.HeldForRelink, linkedCount(t, live), store.modifyCalls.Load())
	}

	store = newStore()
	over, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false,"include_unsettled_relink":true}`)
	if err != nil {
		t.Fatalf("override: %v", err)
	}
	if over.HeldForRelink != 0 || linkedCount(t, over) != 2 ||
		store.row("bf-r1").DelugeHash != linkTorrentID(1) || store.row("bf-r2").DelugeHash != linkTorrentID(1) {
		t.Errorf("override: held_for_relink=%d linked=%d, want 0 and both rows linked", over.HeldForRelink, linkedCount(t, over))
	}
}

// limit counts real writes: rows the hold-back holds are dropped before
// limit is applied, so a held torrent does not use up the limit.
func TestLinkBackfill_LimitCountsAfterHoldBack(t *testing.T) {
	f := newLinkFixture(t)
	held := []byte("example audio held for relink")
	free := []byte("example audio free to link,..")
	free2 := []byte("example audio free to link, a second and longer file")
	a := f.torrent(t, 1, filepath.Join(f.downloads, "a"), tfile{"Example Book/01.mp3", held})
	b := f.torrent(t, 2, filepath.Join(f.downloads, "b"), tfile{"Example Book/01.mp3", free}, tfile{"Example Book/02.mp3", free2})
	ra, rb, rb2 := f.libraryRow("bf-a", "a.mp3", held), f.libraryRow("bf-b", "b.mp3", free), f.libraryRow("bf-b2", "b2.mp3", free2)
	rb2.BookID = rb.BookID
	store := newLinkMock(ra, rb, rb2)
	store.versions[ra.BookID] = []database.BookVersion{{ID: "ver-a", Status: database.BookVersionStatusActive}}
	_, client := newFakeDelugeClient(t, a, b)
	hc := &hashCounter{}

	// Two rounds: torrent 1 alone has nothing to write once held, so the
	// window grows to torrent 2. Its first file (same size as torrent 1's)
	// was already hashed in round 1 for the cross-seed check and is not
	// hashed again; its second (no size match) is hashed only in round 2.
	res, err := runLink(t, &Plugin{client: client, store: store, linkHash: hc.hash}, `{"dry_run":false,"limit":1}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if linkedCount(t, res) != 2 || res.HeldForRelink != 1 || res.DeferredByLimit != 0 {
		t.Errorf("linked=%d held_for_relink=%d deferred_by_limit=%d, want 2/1/0", linkedCount(t, res), res.HeldForRelink, res.DeferredByLimit)
	}
	if store.row("bf-a").DelugeHash != "" || store.row("bf-b").DelugeHash != linkTorrentID(2) || store.row("bf-b2").DelugeHash != linkTorrentID(2) {
		t.Error("want the held row unlinked and the other torrent's rows linked")
	}
	if na, nb, nb2 := hc.hashed(d1(a)), hc.hashed(d1(b)), hc.hashed(d2(b)); na != 1 || nb != 1 || nb2 != 1 {
		t.Errorf("hashed torrent 1's file %d time(s) and torrent 2's %d and %d, want once each", na, nb, nb2)
	}
}

// would_link counts rows, not torrents, and matches what the live run links.
func TestLinkBackfill_PreviewCountsRowsLikeLive(t *testing.T) {
	f := newLinkFixture(t)
	c2 := []byte("example audio bytes, chapter two")
	tor := f.torrent(t, 1, f.downloads, tfile{"Example Book/01.mp3", exampleAudio}, tfile{"Example Book/02.mp3", c2})
	rows := []database.BookFile{f.libraryRow("bf-1", "01.mp3", exampleAudio), f.libraryRow("bf-2", "02.mp3", c2)}
	rows[1].BookID = rows[0].BookID
	_, client := newFakeDelugeClient(t, tor)

	preview, err := runLink(t, &Plugin{client: client, store: newLinkMock(rows...)}, `{}`)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	live, err := runLink(t, &Plugin{client: client, store: newLinkMock(rows...)}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("live: %v", err)
	}
	if p, l := linkedCount(t, preview), linkedCount(t, live); p != 2 || l != 2 {
		t.Errorf("would_link=%d linked=%d, want 2 and 2", p, l)
	}
}

// hashCounter wraps filehash.BookFileHash and records every path hashed.
type hashCounter struct {
	mu    sync.Mutex
	paths []string
}

func (h *hashCounter) hash(path string) (string, error) {
	h.mu.Lock()
	h.paths = append(h.paths, path)
	h.mu.Unlock()
	return filehash.BookFileHash(path)
}

func (h *hashCounter) hashed(path string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, p := range h.paths {
		if p == path {
			n++
		}
	}
	return n
}

// limit bounds the hashing: a torrent past the limit is hashed only for the
// files whose size equals a size inside the window. So a cross-seed (C, the
// same bytes as A's first file) is still found and makes that row ambiguous,
// a same-size file of another book (B) is hashed but never written, and a
// torrent with no matching size (D) is not hashed at all. E cross-seeds B:
// a row only torrents past the limit reach is a later run's, so it is not
// counted ambiguous here.
func TestLinkBackfill_LimitBoundsHashing(t *testing.T) {
	f := newLinkFixture(t)
	x2 := []byte("example audio bytes, chapter two, a longer one") // a size no other file has
	y := []byte("example audio bytes, another book!")[:len(exampleAudio)]
	z := []byte("example audio bytes of a book past the limit, with its own size")
	if len(x2) == len(exampleAudio) || len(z) == len(exampleAudio) || len(z) == len(x2) {
		t.Fatal("fixture: sizes must differ")
	}
	a := f.torrent(t, 1, filepath.Join(f.downloads, "a"), tfile{"Example Book/01.mp3", exampleAudio}, tfile{"Example Book/02.mp3", x2})
	b := f.torrent(t, 2, filepath.Join(f.downloads, "b"), tfile{"Other Book/01.mp3", y})
	c := f.torrent(t, 3, filepath.Join(f.downloads, "c"), tfile{"Example Book/01.mp3", exampleAudio})
	d := f.torrent(t, 4, filepath.Join(f.downloads, "d"), tfile{"Late Book/01.mp3", z})
	e := f.torrent(t, 5, filepath.Join(f.downloads, "e"), tfile{"Other Book/01.mp3", y})
	r1, r2 := f.libraryRow("bf-1", "01.mp3", exampleAudio), f.libraryRow("bf-2", "02.mp3", x2)
	r2.BookID = r1.BookID
	rb, rd := f.libraryRow("bf-b", "other.mp3", y), f.libraryRow("bf-d", "late.mp3", z)
	store := newLinkMock(r1, r2, rb, rd)
	_, client := newFakeDelugeClient(t, a, b, c, d, e)
	hc := &hashCounter{}

	res, err := runLink(t, &Plugin{client: client, store: store, linkHash: hc.hash}, `{"dry_run":false,"limit":1}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if linkedCount(t, res) != 1 || res.Ambiguous != 1 || res.DeferredByLimit != 4 || res.BeyondLimitHashed != 3 || res.Files != 2 {
		t.Errorf("linked=%d ambiguous=%d deferred_by_limit=%d beyond_limit_hashed=%d files=%d, want 1/1/4/3/2",
			linkedCount(t, res), res.Ambiguous, res.DeferredByLimit, res.BeyondLimitHashed, res.Files)
	}
	if store.row("bf-2").DelugeHash != linkTorrentID(1) || store.row("bf-1").DelugeHash != "" ||
		store.row("bf-b").DelugeHash != "" || store.row("bf-d").DelugeHash != "" {
		t.Error("want only bf-2 linked: bf-1 is cross-seeded, bf-b and bf-d belong to torrents past the limit")
	}
	for name, path := range map[string]string{
		"a/01": d1(a), "a/02": d2(a), "b/01": d1(b), "c/01": d1(c), "e/01": d1(e),
	} {
		if n := hc.hashed(path); n != 1 {
			t.Errorf("%s hashed %d time(s), want exactly 1", name, n)
		}
	}
	if n := hc.hashed(d1(d)); n != 0 {
		t.Errorf("a torrent past the limit with no matching size was hashed %d time(s)", n)
	}
}

func d1(d delugeclient.TorrentDetail) string { return filepath.Clean(d.AbsPath(d.Files[0])) }
func d2(d delugeclient.TorrentDetail) string { return filepath.Clean(d.AbsPath(d.Files[1])) }

// A limited run reaches new torrents the next time: torrents some row
// already carries go to the back of the window order, so the second run
// hashes only the torrent the first one did not write.
func TestLinkBackfill_LimitNextRunReachesNewTorrents(t *testing.T) {
	f := newLinkFixture(t)
	ca := []byte("example audio bytes for the first torrent")
	cb := []byte("example audio bytes for the second torrent, longer")
	a := f.torrent(t, 1, filepath.Join(f.downloads, "a"), tfile{"Example Book/01.mp3", ca})
	b := f.torrent(t, 2, filepath.Join(f.downloads, "b"), tfile{"Example Book/01.mp3", cb})
	store := newLinkMock(f.libraryRow("bf-a", "a.mp3", ca), f.libraryRow("bf-b", "b.mp3", cb))
	_, client := newFakeDelugeClient(t, a, b)

	first := &hashCounter{}
	if res, err := runLink(t, &Plugin{client: client, store: store, linkHash: first.hash}, `{"dry_run":false,"limit":1}`); err != nil || linkedCount(t, res) != 1 {
		t.Fatalf("first run: linked=%d err=%v, want 1", linkedCount(t, res), err)
	}
	if store.row("bf-a").DelugeHash != linkTorrentID(1) || first.hashed(d1(b)) != 0 {
		t.Fatalf("first run: want torrent 1 linked and torrent 2 unhashed")
	}
	second := &hashCounter{}
	res, err := runLink(t, &Plugin{client: client, store: store, linkHash: second.hash}, `{"dry_run":false,"limit":1}`)
	if err != nil || linkedCount(t, res) != 1 || store.row("bf-b").DelugeHash != linkTorrentID(2) {
		t.Fatalf("second run: linked=%d err=%v, want torrent 2 linked", linkedCount(t, res), err)
	}
	if n := second.hashed(d1(a)); n != 0 {
		t.Errorf("second run re-hashed the torrent the first run linked (%d time(s))", n)
	}
}

// Only rows that name no version are held: a row of the same book that names
// a version is linked while its hashless sibling waits for the relink fixer.
func TestLinkBackfill_RelinkHoldsOnlyUnversionedRows(t *testing.T) {
	f := newLinkFixture(t)
	c2 := []byte("example audio bytes, chapter two")
	tor := f.torrent(t, 1, f.downloads, tfile{"Example Book/01.mp3", exampleAudio}, tfile{"Example Book/02.mp3", c2})
	r1, r2 := f.libraryRow("bf-1", "01.mp3", exampleAudio), f.libraryRow("bf-2", "02.mp3", c2)
	r2.BookID, r2.VersionID = r1.BookID, "ver-2"
	store := newLinkMock(r1, r2)
	store.versions[r1.BookID] = []database.BookVersion{
		{ID: "ver-1", Status: database.BookVersionStatusActive},
		{ID: "ver-2", Status: database.BookVersionStatusAlt},
	}
	_, client := newFakeDelugeClient(t, tor)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.HeldForRelink != 1 || linkedCount(t, res) != 1 {
		t.Errorf("held_for_relink=%d linked=%d, want 1 and 1", res.HeldForRelink, linkedCount(t, res))
	}
	if store.row("bf-1").DelugeHash != "" || store.row("bf-2").DelugeHash != linkTorrentID(1) {
		t.Error("want the unversioned row held and the versioned row linked")
	}
}
