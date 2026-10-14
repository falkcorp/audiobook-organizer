// file: internal/operations/registry/fixer_resume_real_writer_test.go
// version: 1.1.0
// guid: 53c15d37-1fc7-4e53-ac78-538932952997
// last-edited: 2026-10-14

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// realRow is one row of the real-Writer resume test: set field of book to
// target. field "format" is the book's book_file "f-<book>" (ModifyBookFile,
// an operation journal row); the others are book fields (ModifyBook, a
// metadata history batch).
type realRow struct{ book, field, target string }

func realFileID(book string) string { return "f-" + book }

func realGet(t *testing.T, p *database.PebbleStore, r realRow) string {
	t.Helper()
	if r.field == "format" {
		f, err := p.GetBookFileByID(r.book, realFileID(r.book))
		if err != nil || f == nil {
			t.Fatalf("book_file of %s: %v", r.book, err)
		}
		return f.Format
	}
	b, err := p.GetBookByID(r.book)
	if err != nil || b == nil {
		t.Fatalf("book %s: %v", r.book, err)
	}
	switch r.field {
	case "title":
		return b.Title
	case "publisher":
		if b.Publisher == nil {
			return ""
		}
		return *b.Publisher
	}
	t.Fatalf("field %q", r.field)
	return ""
}

func realSetBook(b *database.Book, field, v string) {
	switch field {
	case "title":
		b.Title = v
	case "publisher":
		b.Publisher = &v
	default:
		panic(field)
	}
}

// foreignSet changes a book field the way someone outside the run would: a
// plain store write, no history and no marker under the run.
func foreignSet(t *testing.T, p *database.PebbleStore, book, field, v string) {
	t.Helper()
	b, err := p.GetBookByID(book)
	if err != nil || b == nil {
		t.Fatalf("book %s: %v", book, err)
	}
	realSetBook(b, field, v)
	if _, err := p.UpdateBook(book, b); err != nil {
		t.Fatal(err)
	}
}

// runRealResume runs a Fixer over rows on a real Pebble store with the real
// opswriter Writer (newV3Writer) and the real batch runner, twice. The first
// attempt commits chunk 0 (rows 0-9, ChunkSize 10, one worker), applies
// chunk 1's rows up to blockRow, parks on blockRow and is stopped, so chunk
// 1 never completes. between runs next (the foreign actor); then a resumed
// attempt -- a fresh Writer, as after a deploy -- finishes. It returns
// FixerResult.Applied and what AfterApply was handed.
func runRealResume(t *testing.T, rows map[string]realRow, blockRow string,
	between func(p *database.PebbleStore)) (applied, afterApply []string) {
	t.Helper()
	p := newPebbleV3(t)
	for _, r := range rows {
		if b, _ := p.GetBookByID(r.book); b != nil {
			continue
		}
		if _, err := p.CreateBook(&database.Book{ID: r.book, Title: "orig " + r.book, FilePath: "/x/" + r.book}); err != nil {
			t.Fatal(err)
		}
		if err := p.CreateBookFile(&database.BookFile{ID: realFileID(r.book), BookID: r.book,
			FilePath: "/x/" + r.book + ".m4b", Format: "mp3"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.InsertRunV3(database.RunRecord{ID: "op", DefID: "test.fixer", Priority: 5}); err != nil {
		t.Fatal(err)
	}
	reg := NewWithOptions(p, slog.Default(), 1, Options{WatchdogInterval: time.Hour, SweepInterval: time.Hour})
	evaluate := func(id string) (ops.Row, error) {
		r, ok := rows[id]
		if !ok {
			return ops.Row{}, errors.New("no row " + id)
		}
		cur := realGet(t, p, r)
		subjects := ops.Books(r.book)
		if r.field == "format" {
			subjects = append(subjects, ops.BookFiles(realFileID(r.book))...)
		}
		return ops.Row{ID: id, Subjects: subjects, Inputs: map[string]string{r.field: cur},
			Current: map[string]string{r.field: cur}, Proposed: map[string]string{r.field: r.target}}, nil
	}
	var mu sync.Mutex
	block := true
	blocked := make(chan struct{})
	def := ops.Fixer("test.fixer", ops.FixerSpec[ops.NoParams, string]{
		Common:     ops.Common{Title: "f", Effects: ops.Writes(ops.ResBooks), Permission: auth.PermSettingsManage},
		Candidates: func(*ops.RC, ops.NoParams) (ops.Source[string], error) { return ops.Source[string]{}, nil },
		Load: func(_ *ops.RC, _ ops.NoParams, id string) (string, bool, error) {
			_, ok := rows[id]
			return id, ok, nil
		},
		Evaluate: func(_ *ops.ItemRC, _ ops.NoParams, id string) (ops.Row, error) { return evaluate(id) },
		Apply: func(irc *ops.ItemRC, w ops.Writer, _ ops.NoParams, row ops.Row) error {
			mu.Lock()
			parked := block && row.ID == blockRow
			mu.Unlock()
			if parked {
				close(blocked)
				<-irc.Context().Done()
				return irc.Context().Err()
			}
			r := rows[row.ID]
			if r.field == "format" {
				_, err := w.ModifyBookFile(irc.Context(), r.book, realFileID(r.book), func(f *database.BookFile) error {
					f.Format = r.target
					return nil
				})
				return err
			}
			_, err := w.ModifyBook(irc.Context(), r.book, func(b *database.Book) error {
				realSetBook(b, r.field, r.target)
				return nil
			})
			return err
		},
		AfterApply: func(_ *ops.RC, ids []string) error {
			afterApply = slices.Clone(ids)
			return nil
		},
	})
	keys := make([]string, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	planned := make([]ops.Row, 0, len(keys))
	for _, k := range keys {
		r, err := evaluate(k)
		if err != nil {
			t.Fatal(err)
		}
		planned = append(planned, r)
	}
	params, err := json.Marshal(ops.FixerApply{Rows: planned})
	if err != nil {
		t.Fatal(err)
	}
	open := func(resumed bool) (*ops.BatchJob, *ops.RC, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		w, err := reg.newV3Writer(ctx, "op", "test.fixer", ops.Live)
		if err != nil {
			t.Fatal(err)
		}
		rc := ops.NewRC(ops.RunEnv{Context: ctx, OpID: "op", DefID: "test.fixer", Mode: ops.Live, Writer: w, Resumed: resumed})
		job, err := ops.Compile(def).OpenBatch(rc, params)
		if err != nil {
			t.Fatal(err)
		}
		job.ChunkSize, job.Workers = 10, 1
		return job, rc, cancel
	}
	store := newMemLedgerStore(t)
	job, rc, cancel := open(false)
	defer cancel()
	_, _, err = runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1,
		afterCommit: func(l database.Ledger, _ database.ProgressSnapshot) bool {
			if len(bitsSet(l)) != 1 {
				return false
			}
			<-blocked
			return true
		}})
	if !errors.Is(err, errWorldStopped) {
		t.Fatalf("first attempt: %v", err)
	}
	mu.Lock()
	block = false
	mu.Unlock()
	if between != nil {
		between(p)
	}
	job2, rc2, cancel2 := open(true)
	defer cancel2()
	_, raw, err := runBatch(rc2.Context(), batchConfig{opID: "op", job: job2, rc: rc2, store: store, commitEvery: -1})
	if err != nil {
		t.Fatal(err)
	}
	var res ops.FixerResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	return res.Applied, afterApply
}

// twentyTitleRows is rows 0-19, each setting the title of its own book.
func twentyTitleRows() map[string]realRow {
	rows := map[string]realRow{}
	for i := range 20 {
		rows[itemKey(i)] = realRow{book: "bk-" + itemKey(i), field: "title", target: "new " + itemKey(i)}
	}
	return rows
}

// TestFixerResumeRealWriter (05-PR5b acceptance, through the real opswriter
// Writer on Pebble and the real batch runner): a row the first attempt wrote
// in a chunk that never completed is reported applied on resume, from its
// own apply marker; a row it never wrote, which someone else set to its
// Proposed values between the attempts, is not -- even when the run wrote
// another row of the same book (a book field, or the book's book_file) --
// and neither is a written row whose history was undone before a foreign
// actor set it back to Proposed. Applied and AfterApply agree.
func TestFixerResumeRealWriter(t *testing.T) {
	all := func(except ...int) []string {
		var out []string
		for i := range 20 {
			if !slices.Contains(except, i) {
				out = append(out, itemKey(i))
			}
		}
		return out
	}
	cases := []struct {
		name    string
		rows    func() map[string]realRow
		block   int
		between func(t *testing.T, p *database.PebbleStore)
		want    []string
	}{
		{"foreign_row_between_attempts", twentyTitleRows, 12,
			func(t *testing.T, p *database.PebbleStore) {
				foreignSet(t, p, "bk-"+itemKey(13), "title", "new "+itemKey(13))
			}, all(13)},
		{"shared_book_two_fields", func() map[string]realRow {
			rows := twentyTitleRows()
			rows[itemKey(10)] = realRow{book: "bk-shared", field: "title", target: "new shared"}
			rows[itemKey(11)] = realRow{book: "bk-shared", field: "publisher", target: "Foreign Pub"}
			return rows
		}, 11, func(t *testing.T, p *database.PebbleStore) {
			foreignSet(t, p, "bk-shared", "publisher", "Foreign Pub")
		}, all(11)},
		{"book_file_and_book_of_one_book", func() map[string]realRow {
			rows := twentyTitleRows()
			rows[itemKey(10)] = realRow{book: "bk-f", field: "format", target: "m4b"}
			rows[itemKey(11)] = realRow{book: "bk-f", field: "title", target: "new f"}
			return rows
		}, 11, func(t *testing.T, p *database.PebbleStore) {
			foreignSet(t, p, "bk-f", "title", "new f")
		}, all(11)},
		{"undone_then_set_back_by_someone_else", twentyTitleRows, 12,
			func(t *testing.T, p *database.PebbleStore) {
				// Row 10's batch is undone (undo-last-apply writes undo rows
				// under the batch id); someone then sets the title back to
				// row 10's Proposed. Row 11 is left alone.
				m, err := p.GetRowMarkV3("op", itemKey(10))
				if err != nil || m == nil || len(m.BookBatches) != 1 {
					t.Fatalf("row 10's marker = %+v, %v; want one batch", m, err)
				}
				old, applied := `"orig bk-`+itemKey(10)+`"`, `"new `+itemKey(10)+`"`
				if err := p.RecordMetadataChange(&database.MetadataChangeRecord{BookID: m.BookBatches[0].BookID,
					Field: "title", PreviousValue: &applied, NewValue: &old, ChangeType: "undo", // metafetch.ChangeTypeApplyUndo
					Source: "undo-last-apply", ChangedAt: time.Now(), BatchID: m.BookBatches[0].BatchID}); err != nil {
					t.Fatal(err)
				}
			}, all(10)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			applied, afterApply := runRealResume(t, c.rows(), itemKey(c.block),
				func(p *database.PebbleStore) { c.between(t, p) })
			if !slices.Equal(applied, c.want) || !slices.Equal(afterApply, c.want) {
				t.Fatalf("Applied = %v\nAfterApply = %v\nwant %v", applied, afterApply, c.want)
			}
		})
	}
}
