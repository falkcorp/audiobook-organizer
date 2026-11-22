// file: internal/books/store/book_relocated_outbox_test.go
// version: 0.1.0
// guid: 5233e967-782c-457f-a18c-cf401b5e221b
// last-edited: 2026-11-20

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
	skeys "github.com/falkcorp/mediacore/storage/keys"
)

// outboxOwner and outboxSpec mirror core/internal/outboxstore's Owner and
// StorageSpec, which the root module cannot import (Go's internal rule): the
// kernel registers the same owner and spec (core/kernel/outbox.go).
const outboxOwner storage.Owner = "core/outbox"

func outboxSpec() storage.Spec {
	ser := []string{}
	for _, f := range []string{"seq", "cur", "dead", "deadn"} {
		ser = append(ser, string(skeys.Prefix(skeys.Str(f))))
	}
	return storage.Spec{Keyspace: true, Serialized: ser}
}

// relocationRig is one core DB holding books' claims and the outbox keyspace,
// the books store over its LegacyRange, and an outbox over its keyspace whose
// catalog declares books.book.relocated.v1 (unless undeclared is set).
type relocationRig struct {
	store *PebbleStore
	ob    *outbox.Outbox
	outKS storage.Keyspace
}

func newRelocationRig(t *testing.T, undeclared bool) *relocationRig {
	t.Helper()
	ctx := context.Background()
	op, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(t.TempDir(), "gotham.pebble"), FS: storage.MemFS(), Build: "batcomputer-1",
		Owners: []storage.OwnerSpec{
			{Owner: BooksOwner, Enabled: true, Spec: storage.Spec{Claims: BooksClaims()}},
			{Owner: outboxOwner, Enabled: true, Spec: outboxSpec()},
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
	lr, ok := db.Legacy(BooksOwner)
	if !ok {
		t.Fatal("no books LegacyRange")
	}
	outKS, ok := db.Keyspace(outboxOwner)
	if !ok {
		t.Fatal("no outbox keyspace")
	}
	s, err := newCoreStore(lr, nil, warmNever)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
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
	if !undeclared {
		if err := bus.Declare("books", BookRelocatedTopicSpec()); err != nil {
			t.Fatal(err)
		}
	}
	bus.Freeze()
	ob, err := outbox.New(outbox.Options{Keyspace: outKS, Catalog: bus.Catalog()})
	if err != nil {
		t.Fatal(err)
	}
	return &relocationRig{store: s, ob: ob, outKS: outKS}
}

// stagedRelocation is one intake row of the topic, in stage (ULID) order.
type stagedRelocation struct {
	Key      string          `json:"key"`
	Producer string          `json:"producer"`
	Payload  BookRelocatedV1 `json:"payload"`
}

// staged reads every intake row of books.book.relocated.v1 back from the
// outbox keyspace, in key order (the stage order, 05-events §5E.5.2).
func (r *relocationRig) staged(t *testing.T) []stagedRelocation {
	t.Helper()
	pre := skeys.Prefix(skeys.Str("in"), skeys.Str(BookRelocatedTopicName))
	var out []stagedRelocation
	for kv, err := range r.outKS.ScanPrefix(context.Background(), pre) {
		if err != nil {
			t.Fatal(err)
		}
		row, err := codec.JSON[stagedRelocation](1).Decode(kv.Value)
		if err != nil {
			t.Fatalf("intake row %q: %v", kv.Key, err)
		}
		out = append(out, row)
	}
	return out
}

func (r *relocationRig) book(t *testing.T, title, path string) *Book {
	t.Helper()
	b, err := r.store.CreateBook(&Book{Title: title, FilePath: path})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (r *relocationRig) move(id, path string) error {
	_, err := r.store.ModifyBook(id, func(b *Book) error {
		b.FilePath = path
		return nil
	})
	return err
}

func (r *relocationRig) pathOf(t *testing.T, id string) string {
	t.Helper()
	b, err := r.store.GetBookByID(id)
	if err != nil || b == nil {
		t.Fatalf("GetBookByID(%s) = %v, %v", id, b, err)
	}
	return b.FilePath
}

func TestBookRelocatedTopicSpec(t *testing.T) {
	spec := BookRelocatedTopicSpec()
	if spec.Name != "books.book.relocated.v1" {
		t.Fatalf("Name = %q", spec.Name)
	}
	if spec.Durable == nil || spec.Durable.Partitions != 16 {
		t.Fatalf("Durable = %+v, want 16 partitions (fixed for the topic's lifetime)", spec.Durable)
	}
	if !reflect.DeepEqual(spec.Permission, rbac.Need("books:view")) {
		t.Fatalf("Permission = %+v, want books:view (anyone who may view the library)", spec.Permission)
	}
	if spec.Realtime != nil || spec.Notify != nil {
		t.Fatalf("a durable relocation topic carries no Realtime or Notify metadata: %+v", spec)
	}
	if got := spec.PayloadType(); got != reflect.TypeFor[BookRelocatedV1]() {
		t.Fatalf("PayloadType = %v", got)
	}
	if len(spec.Schema) == 0 {
		t.Fatal("no payload schema reflected")
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Declare("books", spec); err != nil {
		t.Fatalf("books may not declare its own topic: %v", err)
	}
	raw, err := json.Marshal(BookRelocatedV1{BookID: "b1", FromPath: "/gotham/a", ToPath: "/gotham/b"})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"book_id":"b1","from_path":"/gotham/a","to_path":"/gotham/b"}` {
		t.Fatalf("payload wire form = %s", raw)
	}
}

func TestBookRelocated_StagedInThePathWrite(t *testing.T) {
	r := newRelocationRig(t, false)
	r.store.SetRelocationOutbox(r.ob.Writer("books"))
	b := r.book(t, "The Long Halloween", "/gotham/library/long-halloween")
	if got := r.staged(t); len(got) != 0 {
		t.Fatalf("a create staged %d relocations, want 0 (only a path change of a stored book is a relocation)", len(got))
	}
	if _, err := r.store.UpdateBook(b.ID, &Book{Title: b.Title, FilePath: "/gotham/library/batman/long-halloween"}); err != nil {
		t.Fatal(err)
	}
	got := r.staged(t)
	want := []stagedRelocation{{Key: b.ID, Producer: "books", Payload: BookRelocatedV1{
		BookID: b.ID, FromPath: "/gotham/library/long-halloween", ToPath: "/gotham/library/batman/long-halloween"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("staged = %+v, want %+v", got, want)
	}
	st, err := r.ob.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Topics) != 1 || st.Topics[0].Topic != BookRelocatedTopicName || st.Topics[0].IntakePending != 1 {
		t.Fatalf("outbox status topics = %+v, want one pending intake row on %s", st.Topics, BookRelocatedTopicName)
	}
}

func TestBookRelocated_NothingStagedWithoutAnOutbox(t *testing.T) {
	r := newRelocationRig(t, false)
	b := r.book(t, "Year One", "/gotham/library/year-one")
	if err := r.move(b.ID, "/gotham/library/batman/year-one"); err != nil {
		t.Fatalf("a dark store must move books as before: %v", err)
	}
	if got := r.pathOf(t, b.ID); got != "/gotham/library/batman/year-one" {
		t.Fatalf("path = %q", got)
	}
	if got := r.staged(t); len(got) != 0 {
		t.Fatalf("staged %d relocations with no outbox installed, want 0", len(got))
	}
}

func TestBookRelocated_NoEventWhenThePathDoesNotChange(t *testing.T) {
	r := newRelocationRig(t, false)
	r.store.SetRelocationOutbox(r.ob.Writer("books"))
	b := r.book(t, "Hush", "/gotham/library/hush")
	// A full-row update that keeps the path.
	if _, err := r.store.UpdateBook(b.ID, &Book{Title: "Hush (Absolute Edition)", FilePath: "/gotham/library/hush"}); err != nil {
		t.Fatal(err)
	}
	// A read-modify-write that touches another field.
	if _, err := r.store.ModifyBook(b.ID, func(bk *Book) error { bk.Title = "Hush"; return nil }); err != nil {
		t.Fatal(err)
	}
	// A move to the path it is already at.
	if err := r.move(b.ID, "/gotham/library/hush"); err != nil {
		t.Fatal(err)
	}
	if got := r.staged(t); len(got) != 0 {
		t.Fatalf("staged %d relocations for writes that kept the path: %+v", len(got), got)
	}
}

func TestBookRelocated_EmptyNewPathIsNoRelocation(t *testing.T) {
	r := newRelocationRig(t, false)
	r.store.SetRelocationOutbox(r.ob.Writer("books"))
	b := r.book(t, "The Killing Joke", "/gotham/library/killing-joke")
	if err := r.move(b.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := r.staged(t); len(got) != 0 {
		t.Fatalf("clearing the path staged %+v, want nothing (no path for a follow-up to point at)", got)
	}
	if got := r.pathOf(t, b.ID); got != "" {
		t.Fatalf("path = %q, want the cleared path committed", got)
	}
}

// A book getting its first path is a create, not a relocation: nothing sits at
// an old path for a follow-up to move. The path write still commits, and the
// book's next move is a relocation again.
func TestBookRelocated_EmptyOldPathIsNoRelocation(t *testing.T) {
	r := newRelocationRig(t, false)
	r.store.SetRelocationOutbox(r.ob.Writer("books"))
	b := r.book(t, "Dark Victory", "")
	if err := r.move(b.ID, "/gotham/library/dark-victory"); err != nil {
		t.Fatal(err)
	}
	if got := r.pathOf(t, b.ID); got != "/gotham/library/dark-victory" {
		t.Fatalf("path = %q, want the first path committed", got)
	}
	// The full-row update funnel too, from a cleared path.
	c := r.book(t, "The Killing Joke", "/gotham/library/killing-joke")
	if err := r.move(c.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.UpdateBook(c.ID, &Book{Title: c.Title, FilePath: "/gotham/library/batman/killing-joke"}); err != nil {
		t.Fatal(err)
	}
	if got := r.pathOf(t, c.ID); got != "/gotham/library/batman/killing-joke" {
		t.Fatalf("path = %q, want the restored path committed", got)
	}
	if got := r.staged(t); len(got) != 0 {
		t.Fatalf("giving a book a path from none staged %+v, want nothing (a create is not a relocation)", got)
	}
	if err := r.move(b.ID, "/gotham/library/batman/dark-victory"); err != nil {
		t.Fatal(err)
	}
	got := r.staged(t)
	want := []stagedRelocation{{Key: b.ID, Producer: "books", Payload: BookRelocatedV1{
		BookID: b.ID, FromPath: "/gotham/library/dark-victory", ToPath: "/gotham/library/batman/dark-victory"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("staged = %+v, want %+v (the move after the first path is a relocation)", got, want)
	}
}

func TestBookRelocated_FailedCommitStagesNothing(t *testing.T) {
	r := newRelocationRig(t, false)
	r.store.SetRelocationOutbox(r.ob.Writer("books"))
	b := r.book(t, "Arkham Asylum", "/gotham/library/arkham-asylum")
	boom := errors.New("the batcomputer lost power")
	fault := func(p *PebbleStore) error {
		if p == r.store {
			return boom
		}
		return nil
	}
	identCommitFault.Store(&fault)
	err := r.move(b.ID, "/gotham/library/batman/arkham-asylum")
	identCommitFault.Store(nil)
	if !errors.Is(err, boom) {
		t.Fatalf("move = %v, want the commit fault", err)
	}
	if got := r.pathOf(t, b.ID); got != "/gotham/library/arkham-asylum" {
		t.Fatalf("path after a failed commit = %q", got)
	}
	if got := r.staged(t); len(got) != 0 {
		t.Fatalf("a failed commit left %d staged relocations: %+v", len(got), got)
	}
	// The same move then commits with exactly its one message.
	if err := r.move(b.ID, "/gotham/library/batman/arkham-asylum"); err != nil {
		t.Fatal(err)
	}
	if got := r.staged(t); len(got) != 1 {
		t.Fatalf("after the retry staged = %+v, want 1", got)
	}
}

func TestBookRelocated_StageRefusalRefusesTheMove(t *testing.T) {
	// The catalog does not declare the topic: Stage refuses, and the move must
	// not commit without its follow-up (05-events §5E.7).
	r := newRelocationRig(t, true)
	r.store.SetRelocationOutbox(r.ob.Writer("books"))
	b := r.book(t, "Knightfall", "/gotham/library/knightfall")
	err := r.move(b.ID, "/gotham/library/batman/knightfall")
	if !errors.Is(err, outbox.ErrUnknownTopic) {
		t.Fatalf("move = %v, want outbox.ErrUnknownTopic", err)
	}
	if got := r.pathOf(t, b.ID); got != "/gotham/library/knightfall" {
		t.Fatalf("path after a refused stage = %q, want the old path", got)
	}
	if got := r.staged(t); len(got) != 0 {
		t.Fatalf("staged = %+v", got)
	}
	// A writer for another module is refused the same way.
	r2 := newRelocationRig(t, false)
	r2.store.SetRelocationOutbox(r2.ob.Writer("joker"))
	b2 := r2.book(t, "Death of the Family", "/gotham/library/death-of-the-family")
	if err := r2.move(b2.ID, "/gotham/library/batman/death-of-the-family"); !errors.Is(err, outbox.ErrNotOwner) {
		t.Fatalf("move through a foreign writer = %v, want outbox.ErrNotOwner", err)
	}
	if got := r2.pathOf(t, b2.ID); got != "/gotham/library/death-of-the-family" {
		t.Fatalf("path = %q", got)
	}
}

func TestBookRelocated_RawBatchIsRefused(t *testing.T) {
	raw, err := pebble.Open("", &pebble.Options{FS: vfs.NewMem()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	b := NewGatedDB(raw).NewBatch()
	t.Cleanup(func() { _ = b.Close() })
	p := &PebbleStore{}
	// No outbox: the raw batch is fine, nothing is staged.
	if err := p.stageBookRelocated(b, "b1", "/gotham/a", "/gotham/b"); err != nil {
		t.Fatalf("no outbox installed = %v, want nil", err)
	}
	r := newRelocationRig(t, false)
	p.SetRelocationOutbox(r.ob.Writer("books"))
	if err := p.stageBookRelocated(b, "b1", "/gotham/a", "/gotham/b"); !errors.Is(err, errRelocationNeedsCoreBatch) {
		t.Fatalf("raw batch with an outbox = %v, want errRelocationNeedsCoreBatch", err)
	}
	// The filter runs first: a non-move on a raw batch is still nothing.
	if err := p.stageBookRelocated(b, "b1", "/gotham/a", "/gotham/a"); err != nil {
		t.Fatalf("same path = %v", err)
	}
	if err := p.stageBookRelocated(b, "b1", "/gotham/a", ""); err != nil {
		t.Fatalf("empty new path = %v", err)
	}
}

// TestBookRelocated_ConcurrentMovesOfOneBook runs movers and non-path writers
// on one book at once. Every move goes to a path no other write uses, so each
// is a real change; the per-book stripe serializes the writes, so the staged
// messages, in stage order, form one unbroken chain from the first path to the
// stored one, and the non-path writes stage nothing.
func TestBookRelocated_ConcurrentMovesOfOneBook(t *testing.T) {
	r := newRelocationRig(t, false)
	r.store.SetRelocationOutbox(r.ob.Writer("books"))
	const start = "/gotham/library/no-mans-land"
	b := r.book(t, "No Man's Land", start)
	const movers, moves, editors = 6, 8, 3
	var wg sync.WaitGroup
	errs := make(chan error, movers*moves+editors*moves)
	for m := range movers {
		wg.Go(func() {
			for i := range moves {
				errs <- r.move(b.ID, fmt.Sprintf("/gotham/library/batman/no-mans-land-%d-%d", m, i))
			}
		})
	}
	for e := range editors {
		wg.Go(func() {
			for i := range moves {
				_, err := r.store.ModifyBook(b.ID, func(bk *Book) error {
					bk.Title = fmt.Sprintf("No Man's Land vol %d.%d", e, i)
					return nil
				})
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got := r.staged(t)
	if len(got) != movers*moves {
		t.Fatalf("staged %d relocations, want exactly %d (one per move, none per edit)", len(got), movers*moves)
	}
	prev := start
	seen := map[string]bool{}
	for i, s := range got {
		if s.Key != b.ID || s.Payload.BookID != b.ID {
			t.Fatalf("row %d keyed %q for book %q, want %q", i, s.Key, s.Payload.BookID, b.ID)
		}
		if s.Payload.FromPath != prev {
			t.Fatalf("row %d moves from %q, want %q (the chain broke: a stale old path)", i, s.Payload.FromPath, prev)
		}
		if seen[s.Payload.ToPath] {
			t.Fatalf("row %d repeats the move to %q", i, s.Payload.ToPath)
		}
		seen[s.Payload.ToPath] = true
		prev = s.Payload.ToPath
	}
	if stored := r.pathOf(t, b.ID); stored != prev {
		t.Fatalf("stored path %q, the last staged move went to %q", stored, prev)
	}
}

// TestBookRelocated_ImportMoveStagesNothing pins the Deluge-moves ruling for the
// import move (F6-03): ImportToLibrary re-points the book's FILE rows
// (UpdateBookFile) and never writes the book's own file_path, so a moved file
// is not a book relocation and no path-update run follows it; the in-process
// import call that moved the torrent is the only mover.
func TestBookRelocated_ImportMoveStagesNothing(t *testing.T) {
	r := newRelocationRig(t, false)
	r.store.SetRelocationOutbox(r.ob.Writer("books"))
	b := r.book(t, "The Cult", "/gotham/library/the-cult")
	f := &BookFile{BookID: b.ID, FilePath: "/downloads/the-cult/part1.mp3", Format: "mp3"}
	if err := r.store.CreateBookFile(f); err != nil {
		t.Fatal(err)
	}
	f.FilePath = "/gotham/library/the-cult/part1.mp3"
	if err := r.store.UpdateBookFile(f.ID, f); err != nil {
		t.Fatal(err)
	}
	if got := r.staged(t); len(got) != 0 {
		t.Fatalf("an import's file re-point staged %d relocations, want 0: %+v", len(got), got)
	}
}
