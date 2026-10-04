// file: internal/plugins/maintenance/opchange_book_index_test.go
// version: 1.2.0
// guid: 58b6fa0d-49f4-40bf-b1fe-a55af85a5098
// last-edited: 2026-10-03

package maintenance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type fakeOpChangeRebuilder struct {
	res     database.OpChangeByBookBackfillResult
	err     error
	rep     database.OpChangeByBookIndexReport
	rebuilt *bool
}

func (f fakeOpChangeRebuilder) VerifyOpChangeByBookIndex(context.Context, database.OpChangeIndexProgress) (database.OpChangeByBookIndexReport, error) {
	return f.rep, nil
}

func (f fakeOpChangeRebuilder) RebuildOpChangeByBookIndex(context.Context, database.OpChangeIndexProgress) (database.OpChangeByBookBackfillResult, error) {
	if f.rebuilt != nil {
		*f.rebuilt = true
	}
	return f.res, f.err
}

func TestOpchangeBookIndexRebuild_PreviewWritesNothing(t *testing.T) {
	rebuilt := false
	r := fakeOpChangeRebuilder{rep: database.OpChangeByBookIndexReport{Rows: 5, MissingEntries: 2}, rebuilt: &rebuilt}
	if err := previewOpChangeBookIndex(context.Background(), r, &fakeReporter{}); err != nil {
		t.Fatal(err)
	}
	if rebuilt {
		t.Fatal("preview rebuilt the index")
	}
}

func TestOpchangeBookIndexRebuild_CleanRunPasses(t *testing.T) {
	r := fakeOpChangeRebuilder{res: database.OpChangeByBookBackfillResult{Scanned: 10, Indexed: 9, Commits: 2}}
	if err := rebuildOpChangeBookIndex(context.Background(), r, &fakeReporter{}); err != nil {
		t.Fatalf("clean rebuild failed: %v", err)
	}
}

func TestOpchangeBookIndexRebuild_UndecodableFails(t *testing.T) {
	r := fakeOpChangeRebuilder{res: database.OpChangeByBookBackfillResult{Scanned: 10, Indexed: 8, Undecodable: 1}}
	err := rebuildOpChangeBookIndex(context.Background(), r, &fakeReporter{})
	if err == nil || !strings.Contains(err.Error(), "cannot be decoded") {
		t.Fatalf("undecodable rows must fail the op, got %v", err)
	}
}

func TestOpchangeBookIndexRebuild_ErrorWrapped(t *testing.T) {
	boom := errors.New("boom")
	r := fakeOpChangeRebuilder{res: database.OpChangeByBookBackfillResult{Scanned: 4}, err: boom}
	err := rebuildOpChangeBookIndex(context.Background(), r, &fakeReporter{})
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "full scan") {
		t.Fatalf("err = %v, want the wrapped cause and the fallback note", err)
	}
}

// TestOpchangeBookIndexRebuild_RealStore runs the op body against a real
// Pebble store and checks the rebuilt index serves GetBookChanges.
func TestOpchangeBookIndexRebuild_RealStore(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateOperationChange(&database.OperationChange{OperationID: "op1", BookID: "b1"}); err != nil {
		t.Fatal(err)
	}
	r, ok := database.AsCapability[opChangeIndexRebuilder](store)
	if !ok {
		t.Fatal("*PebbleStore does not satisfy opChangeIndexRebuilder")
	}
	if err := rebuildOpChangeBookIndex(context.Background(), r, &fakeReporter{}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetBookChanges("b1")
	if err != nil || len(got) != 1 {
		t.Fatalf("GetBookChanges after rebuild = %v, %v", got, err)
	}
}
