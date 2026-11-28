// file: internal/books/relocation_arms_test.go
// version: 0.1.0
// guid: 7d3b9e14-2a60-4c85-b1f7-5e8c0a46d932
// last-edited: 2026-11-28

package books

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/storage"
	skeys "github.com/falkcorp/mediacore/storage/keys"
	"github.com/gin-gonic/gin"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// The organize copy's two Deluge arms (F6-03, option b). Before the events flip
// the single-book organize request called deluge.NotifyDelugeAfterOrganize after
// its copy. The flip moves that onto the outbox (the copy stages
// books.book.relocated.v1, books' consumer starts books.deluge.path-update), and
// stages it only while the tasks switch is on (relocationFollowUpOn), which is
// off in production until the F5-18 switch commit. With the switch off the
// request keeps the direct call, so an organize copy behaves as it did before.
// Both arms follow ONE fact, the store's relocation outbox, which
// installRelocationOutbox (Register's install) sets only under that predicate.
//
// These cells run the real handler over the real store, outbox and organizer
// service, with a stand-in Deluge web UI that counts core.move_storage calls.

// movesStub is a stand-in Deluge web UI (JSON-RPC) counting core.move_storage.
type movesStub struct{ moves atomic.Int32 }

func newMovesStub(t *testing.T) *movesStub {
	t.Helper()
	m := &movesStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			ID     int64  `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "auth.login":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": req.ID, "result": true})
		case "core.move_storage":
			m.moves.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": req.ID, "result": nil})
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	client, err := deluge.New(srv.URL, "deluge")
	if err != nil {
		t.Fatalf("deluge client: %v", err)
	}
	restore := deluge.SetGlobalClientForTest(client)
	t.Cleanup(restore)
	return m
}

// arms is one organize rig: the books store over a core DB that also holds the
// outbox keyspace, an outbox whose catalog declares books.book.relocated.v1, the
// organizer service wired as audiobooks wires it, and the real handler.
type arms struct {
	store *database.PebbleStore
	out   outbox.Writer
	outKS storage.Keyspace
	h     *handlers.OrganizeHandler
	moves *movesStub
	root  string
}

func newArms(t *testing.T) *arms {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	const outboxOwner storage.Owner = "core/outbox"
	ser := []string{}
	for _, f := range []string{"seq", "cur", "dead", "deadn"} {
		ser = append(ser, string(skeys.Prefix(skeys.Str(f))))
	}
	op, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(t.TempDir(), "gotham.pebble"), FS: storage.MemFS(), Build: "batcomputer-1",
		Owners: []storage.OwnerSpec{
			{Owner: database.BooksOwner, Enabled: true, Spec: storage.Spec{Claims: database.BooksClaims()}},
			{Owner: outboxOwner, Enabled: true, Spec: storage.Spec{Keyspace: true, Serialized: ser}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	db, err := op.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	lr, ok := db.Legacy(database.BooksOwner)
	if !ok {
		t.Fatal("no books LegacyRange")
	}
	outKS, ok := db.Keyspace(outboxOwner)
	if !ok {
		t.Fatal("no outbox keyspace")
	}
	store, err := database.NewBooksStore(lr, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("store close: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Errorf("core DB close: %v", err)
		}
	})
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Declare("books", database.BookRelocatedTopicSpec()); err != nil {
		t.Fatal(err)
	}
	bus.Freeze()
	ob, err := outbox.New(outbox.Options{Keyspace: outKS, Catalog: bus.Catalog()})
	if err != nil {
		t.Fatal(err)
	}

	// What the boot's repairs hook does: start the memdb warmup the organizer's
	// book_file ownership check waits for.
	if err := store.RunBootRepairs(ctx); err != nil {
		t.Fatal(err)
	}
	store.WaitForWarmup()
	store.UseMemDB = true

	prev := config.Snapshot()
	t.Cleanup(func() { config.ReplaceForTest(prev) })
	root := t.TempDir()
	config.ReplaceForTest(config.Config{
		RootDir:              root,
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
		OrganizationStrategy: "copy",
		DelugeMoveEnabled:    true,
	})

	orgSvc := organizer.NewService(store)
	orgSvc.SetRelocatingCopyCreator(store) // what the audiobooks KeyOrganize ServiceDef does
	h := handlers.NewOrganizeHandler(store, organizer.NewPreviewService(store), orgSvc, nil, false)
	return &arms{store: store, out: ob.Writer("books"), outKS: outKS, h: h, moves: newMovesStub(t), root: root}
}

// staged counts the intake rows of books.book.relocated.v1.
func (a *arms) staged(t *testing.T) int {
	t.Helper()
	n := 0
	pre := skeys.Prefix(skeys.Str("in"), skeys.Str(database.BookRelocatedTopicName))
	for _, err := range a.outKS.ScanPrefix(context.Background(), pre) {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	return n
}

// book makes a book with one file at path (content is the same for every call:
// the organizer's same-hash check only looks at other books) and an Active
// torrent version, so a Deluge move has something to send.
func (a *arms) book(t *testing.T, title, path string) *database.Book {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o775); err != nil {
		t.Fatal(err)
	}
	content := []byte("the batcave " + title)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	author, err := a.store.CreateAuthor("Harmon Example")
	if err != nil {
		t.Fatal(err)
	}
	imported := "imported"
	b, err := a.store.CreateBook(&database.Book{Title: title, FilePath: path, AuthorID: &author.ID, LibraryState: &imported})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.CreateBookFile(&database.BookFile{ID: b.ID + "-f", BookID: b.ID, FilePath: path, FileSize: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.CreateBookVersion(&database.BookVersion{
		BookID: b.ID, Status: database.BookVersionStatusActive, Format: "mp3", Source: "deluge",
		TorrentHash: "0123456789abcdef0123456789abcdef01234567",
	}); err != nil {
		t.Fatal(err)
	}
	return b
}

// organize POSTs the single-book organize for id and returns its decoded body.
func (a *arms) organize(t *testing.T, id string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/audiobooks/"+id+"/organize", nil)
	c.Params = gin.Params{{Key: "id", Value: id}}
	a.h.OrganizeBook(c)
	if w.Code != http.StatusOK {
		t.Fatalf("organize %s: HTTP %d: %s", id, w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if data, ok := body["data"].(map[string]any); ok {
		return data
	}
	return body
}

// installLikeRegister runs Register's install of the relocation outbox with the
// tasks switch in the given state.
func (a *arms) installLikeRegister(t *testing.T, tasksOn bool) {
	t.Helper()
	t.Cleanup(SetTasksFlipForTest(tasksOn))
	installRelocationOutbox(a.store, a.out)
}

// TestOrganizeCopy_ExactlyOneDelugeArm: for one single-book organize copy, in
// both switch states, exactly one arm moves the original's torrents. Off: the
// direct call sends the one Active torrent to Deluge and nothing is staged (no
// relay and no executor are involved). On: one message is staged, keyed by the
// original and naming the copy, and the direct call sends nothing. "Not both"
// and "not neither" are asserted separately, so a failure names which broke.
func TestOrganizeCopy_ExactlyOneDelugeArm(t *testing.T) {
	for _, tc := range []struct {
		name       string
		tasksOn    bool
		wantStaged int
		wantDirect int
	}{
		{"tasks switch off: the direct call, nothing staged", false, 0, 1},
		{"tasks switch on: one staged message, no direct call", true, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newArms(t)
			a.installLikeRegister(t, tc.tasksOn)
			orig := a.book(t, "Knightfall", filepath.Join(t.TempDir(), "newbooks", "Knightfall.mp3"))

			body := a.organize(t, orig.ID)
			if body["original_book_id"] != orig.ID || body["book_id"] == orig.ID {
				t.Fatalf("this was not an organize copy of %s: %v", orig.ID, body)
			}

			staged, direct := a.staged(t), int(a.moves.moves.Load())
			if staged+direct > 1 {
				t.Errorf("BOTH arms ran for one organize copy (staged %d, direct %d): the original's torrents move twice", staged, direct)
			}
			if staged+direct == 0 {
				t.Errorf("NEITHER arm ran for one organize copy: the original's torrents never move")
			}
			if staged != tc.wantStaged {
				t.Errorf("staged books.book.relocated.v1 = %d, want %d", staged, tc.wantStaged)
			}
			if direct != tc.wantDirect {
				t.Errorf("direct NotifyDelugeAfterOrganize move_storage calls = %d, want %d", direct, tc.wantDirect)
			}
		})
	}
}

// TestOrganizeInPlace_MovesNoTorrentInEitherState: an in-place organize keeps
// its pre-flip behaviour, no Deluge call and nothing staged, whatever the
// switch says (todo 9351f44a5 asks whether it should move torrents; it does
// not).
func TestOrganizeInPlace_MovesNoTorrentInEitherState(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tasksOn bool
	}{{"tasks switch off", false}, {"tasks switch on", true}} {
		t.Run(tc.name, func(t *testing.T) {
			a := newArms(t)
			a.installLikeRegister(t, tc.tasksOn)
			// Inside RootDir but not at the computed target: the organizer renames it
			// where it stands.
			orig := a.book(t, "Year One", filepath.Join(a.root, "misfiled", "Year One.mp3"))

			body := a.organize(t, orig.ID)
			if _, copied := body["original_book_id"]; copied {
				t.Fatalf("this was an organize copy, not an in-place organize: %v", body)
			}
			if staged, direct := a.staged(t), int(a.moves.moves.Load()); staged != 0 || direct != 0 {
				t.Errorf("in-place organize moved torrents: staged %d, direct %d, want 0 and 0", staged, direct)
			}
		})
	}
}

// recordingSetter is a store that records SetRelocationOutbox.
type recordingSetter struct{ installs int }

func (s *recordingSetter) SetRelocationOutbox(outbox.Writer) { s.installs++ }

// TestInstallRelocationOutbox_FollowsTheRelocationPredicate: the install is the
// follow-up's one predicate (relocationFollowUpOn) and a core outbox. Off, or
// with no outbox, nothing is installed, so the organize copy's direct arm runs;
// on, it is installed and the staged arm replaces it.
func TestInstallRelocationOutbox_FollowsTheRelocationPredicate(t *testing.T) {
	someWriter := (*outbox.Outbox)(nil).Writer("books") // non-zero; never used by the recorder
	for _, tc := range []struct {
		name      string
		tasksOn   bool
		out       outbox.Writer
		installed bool
	}{
		{"tasks switch off", false, someWriter, false},
		{"tasks switch on, no core outbox", true, outbox.Writer{}, false},
		{"tasks switch off, no core outbox", false, outbox.Writer{}, false},
		{"tasks switch on", true, someWriter, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(SetTasksFlipForTest(tc.tasksOn))
			rec := &recordingSetter{}
			if got := installRelocationOutbox(rec, tc.out); got != tc.installed || (rec.installs == 1) != tc.installed || rec.installs > 1 {
				t.Fatalf("installRelocationOutbox = %v with %d install(s), want installed=%v", got, rec.installs, tc.installed)
			}
		})
	}
}

// TestRelocationFollowUp_RegisterInstallsOnlyThroughThePredicate: Register's body
// cannot run without a booted kernel (see TestRelocationFollowUp_RegisterRunsTheWiringCheck),
// so the call sites are pinned from the source. Register calls
// installRelocationOutbox, and no other non-test code in this package calls
// SetRelocationOutbox (which would install the outbox outside the predicate, and
// make the organize copy stage while the tasks switch is off).
func TestRelocationFollowUp_RegisterInstallsOnlyThroughThePredicate(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var viaHelper bool
	var strays []string
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				c, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := c.Fun.(type) {
				case *ast.Ident:
					if fun.Name == "installRelocationOutbox" && name == "module.go" && fn.Name.Name == "Register" && fn.Recv != nil {
						viaHelper = true
					}
				case *ast.SelectorExpr:
					if fun.Sel.Name == "SetRelocationOutbox" && !(name == "tasks_flip.go" && fn.Name.Name == "installRelocationOutbox") {
						strays = append(strays, fmt.Sprintf("%s:%d (%s)", name, fset.Position(c.Pos()).Line, fn.Name.Name))
					}
				}
				return true
			})
		}
	}
	if !viaHelper {
		t.Fatal("booksModule.Register does not call installRelocationOutbox: the relocation outbox would never be installed, and the organize copy's staged arm would never run")
	}
	if len(strays) > 0 {
		t.Fatalf("SetRelocationOutbox is called outside installRelocationOutbox, so the tasks switch does not gate it: %s", strings.Join(strays, ", "))
	}
}
