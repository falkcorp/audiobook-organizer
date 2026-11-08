// file: core/internal/restoreswap/journal_test.go
// version: 1.0.1
// guid: f7abe2b1-cca9-4e96-a595-efc4ea75e0cc
// last-edited: 2026-11-08

package restoreswap

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// TestJournalAppendDropsTheTornTail (R1-21 review m6): an append after a
// torn tail leaves exactly the acknowledged lines and its own, so a later
// append torn so that only its newline reached the disk is one torn last
// line again, never an undecodable line before the last. Written in place
// over a longer tail, the stale bytes past the new line and that newline
// made two lines, and the journal read as ErrJournalCorrupt.
func TestJournalAppendDropsTheTornTail(t *testing.T) {
	mem := vfs.NewMem()
	s := newSwapper(t, mem, testBuild, testNow)
	ctx := context.Background()
	if err := mkdirAllSynced(mem, s.journalDir("j")); err != nil {
		t.Fatal(err)
	}
	path := journalPath(stateDir, "j")
	content := func() []byte {
		t.Helper()
		f, err := mem.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(f)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	staged := `{"step":"staged"}` + "\n"
	// A torn last line whose newline reached the disk, longer than the
	// record appended after it.
	torn := `{"step":"previous_moved","note":"` + strings.Repeat("joker", 60) + "\n"
	f, err := mem.Create(path, vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(staged + torn)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if err := s.appendRec(ctx, "j", restorejournal.Record{Step: StepRechecked}); err != nil {
		t.Fatal(err)
	}
	b := content()
	lines := bytes.SplitAfter(b, []byte("\n"))
	if len(lines) != 3 || len(lines[2]) != 0 || string(lines[0]) != staged {
		t.Errorf("journal after the append = %q, want the staged line and the rechecked line only", b)
	}
	acked := len(lines[0]) + len(lines[1])
	if recs, err := ReadJournal(mem, stateDir, "j"); err != nil || !slices.Equal(steps(recs), []string{StepStaged, StepRechecked}) {
		t.Fatalf("after the append: %v %v", steps(recs), err)
	}

	// The next append, torn: only its last byte, the newline, reached the
	// disk at its place, just past the acknowledged lines.
	next := len(`{"step":"previous_moved","at":"1939-05-01T12:00:00Z"}` + "\n")
	rw, err := mem.OpenReadWrite(path, vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rw.WriteAt([]byte("\n"), int64(acked+next-1)); err != nil {
		t.Fatal(err)
	}
	if err := rw.Close(); err != nil {
		t.Fatal(err)
	}
	if recs, err := ReadJournal(mem, stateDir, "j"); err != nil || !slices.Equal(steps(recs), []string{StepStaged, StepRechecked}) {
		t.Fatalf("after a torn append: %v %v; want the torn line dropped, not the journal corrupt", steps(recs), err)
	}
	if err := s.appendRec(ctx, "j", restorejournal.Record{Step: StepPreviousMoved}); err != nil {
		t.Fatal(err)
	}
	if recs, err := ReadJournal(mem, stateDir, "j"); err != nil || !slices.Equal(steps(recs), []string{StepStaged, StepRechecked, StepPreviousMoved}) {
		t.Fatalf("after the retried append: %v %v", steps(recs), err)
	}
	if names, err := mem.List(s.journalDir("j")); err != nil || slices.ContainsFunc(names, func(n string) bool { return strings.HasSuffix(n, ".tmp") }) {
		t.Fatalf("journal directory %v (%v): the rewrite's temp is left", names, err)
	}
}
