// file: internal/operations/registry/v3_writer_evidence_test.go
// version: 2.1.1
// guid: 3ebbf9a7-49e9-4226-b8b5-535c23956b47
// last-edited: 2026-10-14

package registry

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger/logtest"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// noMarkerStore is a v3-capable store whose marker methods are shadowed by
// fields of the same name, so opswriter.RowMarkStore does not resolve.
type noMarkerStore struct {
	*database.PebbleStore
	PutRowMarkV3 struct{}
	GetRowMarkV3 struct{}
}

// 05-PR5b: the Writer newV3Writer builds records a row's apply marker in a
// real store and reads it back from a later attempt's Writer -- its own
// op's marker, never another op's. A store without the marker methods
// still gets a Live Writer (no error); no row is ever recognized, so a
// resumed Fixer skips such a row as changed since plan instead of claiming
// it, and the Registry says so in one warning, however many Writers it
// builds (round 3 N3). A store with every read says nothing.
func TestV3WriterRowMarkers(t *testing.T) {
	logs := logtest.Capture(t, slog.LevelWarn)
	const warning = "lacks the Fixer resume evidence reads"
	p := newPebbleV3(t)
	ctx := context.Background()
	for _, op := range []string{"op-a", "op-b"} {
		if err := p.InsertRunV3(database.RunRecord{ID: op, DefID: "test.fixer", Priority: 5}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.CreateBook(&database.Book{ID: "book-001", Title: "old", FilePath: "/x/book-001"}); err != nil {
		t.Fatal(err)
	}
	r := NewWithOptions(p, slog.Default(), 1, Options{WatchdogInterval: time.Hour, SweepInterval: time.Hour})
	attempt := func(op string) *opswriter.Writer {
		t.Helper()
		w, err := r.newV3Writer(ctx, op, "test.fixer", ops.Live)
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	first := attempt("op-a")
	irc := ops.NewItemRC(ops.NewRC(ops.RunEnv{Context: ctx}), ctx, &ops.Tally{}, nil)
	if _, err := first.ModifyBook(irc.Context(), "book-001", func(b *database.Book) error { b.Title = "new"; return nil }); err != nil {
		t.Fatal(err)
	}
	first.MarkRowApplied(irc.Context(), "row-1")
	attempt("op-b").MarkRowApplied(ctx, "row-2")

	resumed := attempt("op-a")
	for _, c := range []struct {
		row  string
		want bool
	}{{"row-1", true}, {"row-2", false}, {"row-3", false}} {
		if got, err := resumed.RowApplied(ctx, c.row); err != nil || got != c.want {
			t.Errorf("RowApplied(%s) = %v, %v; want %v", c.row, got, err, c.want)
		}
	}

	if n := len(logs.Lines(warning)); n != 0 {
		t.Fatalf("a store with every evidence read logged %d warnings", n)
	}
	bare := NewWithOptions(noMarkerStore{PebbleStore: p}, slog.Default(), 1,
		Options{WatchdogInterval: time.Hour, SweepInterval: time.Hour})
	if _, err := bare.newV3Writer(ctx, "op-b", "test.fixer", ops.Live); err != nil {
		t.Fatal(err)
	}
	w2, err := bare.newV3Writer(ctx, "op-a", "test.fixer", ops.Live)
	if err != nil {
		t.Fatalf("a store without the marker methods must still back a Live Writer: %v", err)
	}
	if got, err := w2.RowApplied(ctx, "row-1"); err != nil || got {
		t.Fatalf("RowApplied without the marker store = %v, %v; want false, nil", got, err)
	}
	if n := len(logs.Lines(warning)); n != 1 {
		t.Fatalf("two Writers without the marker store logged %d warnings; want 1", n)
	}
}
