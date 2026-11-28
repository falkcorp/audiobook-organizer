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
	s, err := newCoreStore(lr, nil, warmNever, nil)
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

// copyOf organizes a copy of original at path the way the single-book organize
// request does: one CreateBookWithRelocation.
func (r *relocationRig) copyOf(t *testing.T, original *Book, title, path string) *Book {
	t.Helper()
	c, err := r.store.CreateBookWithRelocation(&Book{Title: title, FilePath: path}, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	return c
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
	raw, err := json.Marshal(BookRelocatedV1{BookID: "b1", TargetBookID: "b2"})
	if err != nil {
		t.Fatal(err)
	}
	// Ids, never paths: a consumer reads the target's path as stored when it runs.
	if string(raw) != `{"book_id":"b1","target_book_id":"b2"}` {
		t.Fatalf("payload wire form = %s", raw)
	}
}

// The copy-organize is the one write that stages: one message keyed by the
// original, naming the copy, in the copy row's own batch.
func TestBookRelocated_CopyOrganizeStagesOneMessage(t *testing.T) {
	r := newRelocationRig(t, false)
	r.store.SetRelocationOutbox(r.ob.Writer("books"))
	orig := r.book(t, "The Long Halloween", "/downloads/long-halloween")
	if got := r.staged(t); len(got) != 0 {
		t.Fatalf("a plain create staged %d relocations, want 0", len(got))
	}
	c := r.copyOf(t, orig, "The Long Halloween", "/gotham/library/batman/long-halloween")
	got := r.staged(t)
	want := []stagedRelocation{{Key: orig.ID, Producer: "books", Payload: BookRelocatedV1{BookID: orig.ID, TargetBookID: c.ID}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("staged = %+v, want %+v", got, want)
	}
	if p := r.pathOf(t, c.ID); p != "/gotham/library/batman/long-halloween" {
		t.Fatalf("copy path = %q", p)
	}
	st, err := r.ob.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Topics) != 1 || st.Topics[0].Topic != BookRelocatedTopicName || st.Topics[0].IntakePending != 1 {
		t.Fatalf("outbox status topics = %+v, want one pending intake row on %s", st.Topics, BookRelocatedTopicName)
	}
}

// No other book write stages. The first block is the exact shapes the writers
// of Book.FilePath use at base (the inventory in the PR body): UpdateBook and
// ModifyBook are the funnel's only callers that can change the path, so each
// path change below stands for its whole class of writers, the version swap
// (versions.RunVersionSwap's ModifyBook) included. They moved no torrent before
// the events flip and must move none after it.
func TestBookRelocated_EveryOtherPathWriterStagesNothing(t *testing.T) {
	r := newRelocationRig(t, false)
	r.store.SetRelocationOutbox(r.ob.Writer("books"))
	steps := []struct {
		name string
		do   func(b *Book) error
	}{
		{"UpdateBook moves the path (manual edit, revert, merge restore)", func(b *Book) error {
			_, err := r.store.UpdateBook(b.ID, &Book{Title: b.Title, FilePath: "/gotham/library/batman/a"})
			return err
		}},
		{"ModifyBook moves the path (organize in place, repairs, scanner, quarantine, reconcile)", func(b *Book) error {
			return r.move(b.ID, "/gotham/library/batman/b")
		}},
		{"version swap: ModifyBook with the swap's skip-when-equal shape", func(b *Book) error {
			_, err := r.store.ModifyBook(b.ID, func(bk *Book) error {
				if bk.FilePath == "/gotham/library/batman/.versions/v2/part1.m4b" {
					return ErrSkipBookWrite
				}
				bk.FilePath = "/gotham/library/batman/.versions/v2/part1.m4b"
				return nil
			})
			return err
		}},
		{"ModifyBookCredits moves the path", func(b *Book) error {
			_, _, err := r.store.ModifyBookCredits(b.ID, func(bk *Book, _ *BookCreditsEdit) error {
				bk.FilePath = "/gotham/library/batman/c"
				return nil
			})
			return err
		}},
		{"retire clears the path", func(b *Book) error { return r.move(b.ID, "") }},
		{"restore gives a cleared book its path back", func(b *Book) error {
			_, err := r.store.UpdateBook(b.ID, &Book{Title: b.Title, FilePath: "/gotham/library/batman/d"})
			return err
		}},
		{"a write that keeps the path", func(b *Book) error {
			_, err := r.store.ModifyBook(b.ID, func(bk *Book) error { bk.Title += "!"; return nil })
			return err
		}},
	}
	b := r.book(t, "Year One", "/downloads/year-one")
	for _, st := range steps {
		if err := st.do(b); err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		if got := r.staged(t); len(got) != 0 {
			t.Fatalf("%s staged %d relocations, want 0: %+v", st.name, len(got), got)
		}
	}
}

// A copy-organize with no original named is a plain create, and a store with no
// outbox creates the copy as before.
func TestBookRelocated_NothingStagedWithoutAnOutboxOrAnOriginal(t *testing.T) {
	r := newRelocationRig(t, false)
	orig := r.book(t, "Hush", "/downloads/hush")
	c := r.copyOf(t, orig, "Hush", "/gotham/library/batman/hush")
	if p := r.pathOf(t, c.ID); p != "/gotham/library/batman/hush" {
		t.Fatalf("a dark store must create the copy as before: path %q", p)
	}
	r.store.SetRelocationOutbox(r.ob.Writer("books"))
	if _, err := r.store.CreateBookWithRelocation(&Book{Title: "Hush", FilePath: "/gotham/library/batman/hush-2"}, ""); err != nil {
		t.Fatal(err)
	}
	if got := r.staged(t); len(got) != 0 {
		t.Fatalf("staged %d relocations with no outbox or no original, want 0: %+v", len(got), got)
	}
}

// The copy and its message commit together or not at all.
func TestBookRelocated_FailedCommitStagesNothingAndCreatesNoCopy(t *testing.T) {
	r := newRelocationRig(t, false)
	r.store.SetRelocationOutbox(r.ob.Writer("books"))
	orig := r.book(t, "Arkham Asylum", "/downloads/arkham-asylum")
	boom := errors.New("the batcomputer lost power")
	fault := func(p *PebbleStore) error {
		if p == r.store {
			return boom
		}
		return nil
	}
	identCommitFault.Store(&fault)
	_, err := r.store.CreateBookWithRelocation(&Book{ID: "arkham-copy", Title: "Arkham Asylum", FilePath: "/gotham/library/arkham-asylum"}, orig.ID)
	identCommitFault.Store(nil)
	if !errors.Is(err, boom) {
		t.Fatalf("create = %v, want the commit fault", err)
	}
	if b, _ := r.store.GetBookByID("arkham-copy"); b != nil {
		t.Fatalf("a failed commit left the copy row: %+v", b)
	}
	if got := r.staged(t); len(got) != 0 {
		t.Fatalf("a failed commit left %d staged relocations: %+v", len(got), got)
	}
	if _, err := r.store.CreateBookWithRelocation(&Book{ID: "arkham-copy", Title: "Arkham Asylum", FilePath: "/gotham/library/arkham-asylum"}, orig.ID); err != nil {
		t.Fatal(err)
	}
	got := r.staged(t)
	want := []stagedRelocation{{Key: orig.ID, Producer: "books", Payload: BookRelocatedV1{BookID: orig.ID, TargetBookID: "arkham-copy"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after the retry staged = %+v, want %+v", got, want)
	}
}

func TestBookRelocated_StageRefusalRefusesTheCopy(t *testing.T) {
	// The catalog does not declare the topic: Stage refuses, and the copy must
	// not commit without its follow-up (05-events 5E.7).
	r := newRelocationRig(t, true)
	r.store.SetRelocationOutbox(r.ob.Writer("books"))
	orig := r.book(t, "Knightfall", "/downloads/knightfall")
	_, err := r.store.CreateBookWithRelocation(&Book{ID: "knightfall-copy", Title: "Knightfall", FilePath: "/gotham/library/knightfall"}, orig.ID)
	if !errors.Is(err, outbox.ErrUnknownTopic) {
		t.Fatalf("create = %v, want outbox.ErrUnknownTopic", err)
	}
	if b, _ := r.store.GetBookByID("knightfall-copy"); b != nil {
		t.Fatalf("a refused stage left the copy row")
	}
	if got := r.staged(t); len(got) != 0 {
		t.Fatalf("staged = %+v", got)
	}
	// A writer for another module is refused the same way.
	r2 := newRelocationRig(t, false)
	r2.store.SetRelocationOutbox(r2.ob.Writer("joker"))
	o2 := r2.book(t, "Death of the Family", "/downloads/death-of-the-family")
	_, err = r2.store.CreateBookWithRelocation(&Book{ID: "dotf-copy", Title: "Death of the Family", FilePath: "/gotham/library/dotf"}, o2.ID)
	if !errors.Is(err, outbox.ErrNotOwner) {
		t.Fatalf("create through a foreign writer = %v, want outbox.ErrNotOwner", err)
	}
	if b, _ := r2.store.GetBookByID("dotf-copy"); b != nil {
		t.Fatalf("a foreign writer's refusal left the copy row")
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
	if err := p.stageBookRelocated(b, "b1", "b2"); err != nil {
		t.Fatalf("no outbox installed = %v, want nil", err)
	}
	r := newRelocationRig(t, false)
	p.SetRelocationOutbox(r.ob.Writer("books"))
	if err := p.stageBookRelocated(b, "b1", "b2"); !errors.Is(err, errRelocationNeedsCoreBatch) {
		t.Fatalf("raw batch with an outbox = %v, want errRelocationNeedsCoreBatch", err)
	}
	// The filter runs first: a write that is not a copy-organize on a raw batch is still nothing.
	if err := p.stageBookRelocated(b, "", "b2"); err != nil {
		t.Fatalf("no original = %v", err)
	}
}

// TestBookRelocated_ConcurrentCopiesOfOneOriginal runs copy-organizes of one
// original beside path writers on it. Each copy stages exactly one message
// naming its own row; the path writers stage none.
func TestBookRelocated_ConcurrentCopiesOfOneOriginal(t *testing.T) {
	r := newRelocationRig(t, false)
	r.store.SetRelocationOutbox(r.ob.Writer("books"))
	orig := r.book(t, "No Man's Land", "/downloads/no-mans-land")
	const copiers, movers, each = 6, 3, 8
	var wg sync.WaitGroup
	errs := make(chan error, (copiers+movers)*each)
	copies := make(chan string, copiers*each)
	for c := range copiers {
		wg.Go(func() {
			for i := range each {
				cb, err := r.store.CreateBookWithRelocation(&Book{Title: "No Man's Land", FilePath: fmt.Sprintf("/gotham/library/no-mans-land-%d-%d", c, i)}, orig.ID)
				errs <- err
				if err == nil {
					copies <- cb.ID
				}
			}
		})
	}
	for m := range movers {
		wg.Go(func() {
			for i := range each {
				errs <- r.move(orig.ID, fmt.Sprintf("/downloads/no-mans-land-%d-%d", m, i))
			}
		})
	}
	wg.Wait()
	close(errs)
	close(copies)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]bool{}
	for id := range copies {
		want[id] = true
	}
	got := r.staged(t)
	if len(got) != copiers*each {
		t.Fatalf("staged %d relocations, want exactly %d (one per copy, none per path write)", len(got), copiers*each)
	}
	for i, s := range got {
		if s.Key != orig.ID || s.Payload.BookID != orig.ID {
			t.Fatalf("row %d keyed %q for book %q, want the original %q", i, s.Key, s.Payload.BookID, orig.ID)
		}
		if !want[s.Payload.TargetBookID] {
			t.Fatalf("row %d names target %q, which is no copy this test made", i, s.Payload.TargetBookID)
		}
		delete(want, s.Payload.TargetBookID)
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

// RelocationOutboxInstalled is what the boot check (audiobooks.RequireRelocationOutbox)
// reads: false until SetRelocationOutbox runs, true after.
func TestBookRelocated_InstalledReportsTheOutbox(t *testing.T) {
	r := newRelocationRig(t, false)
	if r.store.RelocationOutboxInstalled() {
		t.Fatal("a store reports a relocation outbox before SetRelocationOutbox")
	}
	r.store.SetRelocationOutbox(r.ob.Writer("books"))
	if !r.store.RelocationOutboxInstalled() {
		t.Fatal("a store reports no relocation outbox after SetRelocationOutbox")
	}
}
