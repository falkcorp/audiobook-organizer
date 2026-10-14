// file: pkg/ops/opstest/examples_test.go
// version: 1.0.0
// guid: 4b6d8e21-7c3f-4a95-9e12-0f5a3b7c8d64
// last-edited: 2026-10-14

package opstest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The three worked examples of
// docs/proposals/2026-10-holistic/05-operations-v3/examples.md, written over
// a World instead of the production deps they name (deps.Paths, deps.Files,
// deps.Books, lshChecker, util.NormalizeLetterLOrdinal), which do not exist in
// that form. Example 3 leaves out w.LockFields: the Writer has no such method
// yet.

type sizeResult struct {
	LibraryBytes int64 `json:"library_bytes"`
	ImportBytes  int64 `json:"import_bytes"`
}

// exampleSizeRefresh is Example 1, library.size-refresh: a read-only Task.
// The synthetic "size" of a file is the length of its path.
func exampleSizeRefresh(books *FakeBooks) ops.Definition {
	return ops.Task("library.size-refresh", ops.TaskSpec[ops.NoParams, sizeResult]{
		Common: ops.Common{
			Title:      "Library size refresh",
			Help:       "Walks the library and import trees to refresh the on-disk size cache.",
			Effects:    ops.ReadOnly(ops.ResFiles),
			Permission: auth.PermLibraryEditMetadata,
		},
		Run: func(rc *ops.RC, _ ops.NoParams) (sizeResult, error) {
			files, err := books.AllBookFiles()
			if err != nil {
				return sizeResult{}, err
			}
			var res sizeResult
			for _, f := range files {
				if err := rc.Context().Err(); err != nil { // ctx threaded
					return res, err
				}
				if strings.HasPrefix(f.FilePath, "/library/") {
					res.LibraryBytes += int64(len(f.FilePath))
				} else {
					res.ImportBytes += int64(len(f.FilePath))
				}
			}
			return res, nil
		},
	})
}

type lshResult struct {
	Indexed int64 `json:"indexed"`
	Skipped int64 `json:"skipped"`
	Failed  int64 `json:"failed"`
}

// exampleLSHBackfill is Example 2, acoustid.lsh-backfill: a Snapshot Batch
// over book files. The LSH index is a side value of the file; the re-save is
// ModifyBookFile with a no-op fn, as in the example.
func exampleLSHBackfill(books *FakeBooks) ops.Definition {
	return ops.Batch("acoustid.lsh-backfill", ops.BatchSpec[ops.NoParams, database.BookFile, lshResult]{
		Common: ops.Common{
			Title:      "Backfill LSH fingerprint index",
			Help:       "Re-saves book files that have a whole-file fingerprint but no LSH index entry. Idempotent.",
			Effects:    ops.Writes(ops.ResBookFiles, ops.ResIndexes),
			Exclusive:  "acoustid.fingerprint",
			Permission: auth.PermLibraryEditMetadata,
		},
		Source: func(*ops.RC, ops.NoParams) (ops.Source[database.BookFile], error) {
			files, err := books.AllBookFiles()
			return ops.Source[database.BookFile]{
				Items: files, Key: func(f database.BookFile) string { return f.ID },
				Order: ops.Snapshot, Total: ops.Known(int64(len(files))),
			}, err
		},
		Label: func(f database.BookFile) string { return f.ID },
		Item: func(rc *ops.ItemRC, _ ops.NoParams, f database.BookFile) error {
			sub := ops.Subject{Type: ops.SubjectBookFile, ID: f.ID}
			switch {
			case f.AcoustIDFingerprintDurationSec == 0:
				rc.Skip("no_fingerprint")
				return nil
			case books.Value(sub, "lsh") != "":
				rc.Skip("already_indexed")
				return nil
			}
			w := rc.Writer()
			if _, err := w.ModifyBookFile(rc.Context(), f.BookID, f.ID, func(*database.BookFile) error { return nil }); err != nil {
				return err
			}
			err := w.Effect(rc.Context(), ops.EffectSpec{Name: "acoustid.lsh-index", Subject: sub},
				func() ([]ops.HistoryRow, error) { return nil, books.PutValue(sub, "lsh", "indexed") })
			if err == nil {
				rc.Count("indexed", 1)
			}
			return err
		},
		Finish: func(_ *ops.RC, _ ops.NoParams, s ops.Summary) (lshResult, error) {
			return lshResult{Indexed: s.Counter("indexed"), Skipped: s.Skipped, Failed: s.Failed}, nil
		},
	})
}

// normalizeLetterL is the synthetic util.NormalizeLetterLOrdinal: a leading
// "l " becomes "1 ".
func normalizeLetterL(title string) (string, bool) {
	rest, ok := strings.CutPrefix(title, "l ")
	if !ok {
		return title, false
	}
	return "1 " + rest, true
}

// exampleLetterLOrdinals is Example 3, the trial -> approve -> apply Fixer.
func exampleLetterLOrdinals(books *FakeBooks) ops.Definition {
	return ops.Fixer("maintenance.normalize-letter-l-ordinals", ops.FixerSpec[ops.NoParams, database.Book]{
		Common: ops.Common{
			Title:      `Letter-l ordinals ("l Volume" -> "1 Volume")`,
			Help:       "Rewrites a leading letter-l ordinal to digits.",
			Effects:    ops.Writes(ops.ResBooks),
			Permission: auth.PermLibraryEditMetadata,
		},
		Candidates: func(*ops.RC, ops.NoParams) (ops.Source[database.Book], error) {
			all, err := books.AllBooks()
			var out []database.Book
			for _, b := range all {
				if _, ok := normalizeLetterL(b.Title); ok && !b.IsSoftDeleted() {
					out = append(out, b)
				}
			}
			return ops.Source[database.Book]{Items: out, Key: func(b database.Book) string { return b.ID },
				Order: ops.Snapshot}, err
		},
		Load: func(_ *ops.RC, _ ops.NoParams, id string) (database.Book, bool, error) {
			b, err := books.GetBookByID(id)
			if err != nil || b == nil || b.IsSoftDeleted() {
				return database.Book{}, false, err
			}
			return *b, true, nil
		},
		Evaluate: func(_ *ops.ItemRC, _ ops.NoParams, b database.Book) (ops.Row, error) {
			row := ops.Row{ID: b.ID, Subjects: ops.Books(b.ID), Title: b.Title, Risk: ops.RiskLow,
				Current: map[string]string{"title": b.Title}, Inputs: map[string]string{"title": b.Title}}
			fixed, ok := normalizeLetterL(b.Title)
			if !ok {
				row.Hold = "the title no longer uses a letter-l ordinal"
				return row, nil
			}
			row.Proposed = map[string]string{"title": fixed}
			row.Reason = "the ordinal is the letter l, not the digit 1 / roman I"
			return row, nil
		},
		Apply: func(rc *ops.ItemRC, w ops.Writer, _ ops.NoParams, row ops.Row) error {
			_, err := w.ModifyBook(rc.Context(), row.ID, func(b *database.Book) error {
				if b.Title != row.Current["title"] {
					return ops.ErrChangedSincePlan // compare-and-set under the write lock
				}
				b.Title = row.Proposed["title"]
				return nil
			})
			return err
		},
	})
}

// exampleWorld seeds 14 synthetic books: odd ones titled "l Volume NNN" (the
// Fixer's candidates), even ones not; every book has one file, and files of
// books divisible by 3 have no fingerprint (Example 2's no_fingerprint skip).
func exampleWorld(t *testing.T) *World {
	var bs []database.Book
	var fs []database.BookFile
	for i := 1; i <= 14; i++ {
		id := fmt.Sprintf("book-%03d", i)
		title := fmt.Sprintf("Synthetic Title %03d", i)
		if i%2 == 1 {
			title = fmt.Sprintf("l Volume %03d", i)
		}
		bs = append(bs, database.Book{ID: id, Title: title, FilePath: "/library/synthetic/" + id, Format: "m4b"})
		f := database.BookFile{ID: fmt.Sprintf("file-%03d", i), BookID: id,
			FilePath: "/library/synthetic/" + id + "/part-1.m4b", Format: "m4b"}
		if i%3 != 0 {
			f.AcoustIDFingerprintDurationSec = float64(600 + i)
		}
		fs = append(fs, f)
	}
	return NewWorld(t, WithBooks(bs...), WithBookFiles(fs...))
}

// TestExamplesConform: the three example defs pass Conformance at ChunkSize
// 1, 3 and their default.
func TestExamplesConform(t *testing.T) {
	t.Parallel()
	w := exampleWorld(t)
	books := w.Books()
	t.Run("size_refresh", func(t *testing.T) { Conformance(t, exampleSizeRefresh(books), nil, InWorld(w)) })
	t.Run("lsh_backfill", func(t *testing.T) { Conformance(t, exampleLSHBackfill(books), nil, InWorld(w)) })
	t.Run("letter_l_ordinals", func(t *testing.T) { Conformance(t, exampleLetterLOrdinals(books), nil, InWorld(w)) })
}

// TestExamplesDoTheirWork guards the guard: the example defs Conformance
// passes really make their writes, so a pass is not a def that does nothing.
func TestExamplesDoTheirWork(t *testing.T) {
	t.Parallel()
	w := exampleWorld(t)
	books := w.Books()
	if o := w.Run(exampleLSHBackfill(books), nil, Live()); o.Err != nil || o.State != state.V3Succeeded {
		t.Fatalf("lsh backfill: %s %v", o.State, o.Err)
	} else if got := string(o.Result); got != `{"indexed":10,"skipped":4,"failed":0}` {
		t.Errorf("lsh backfill result = %s, want 10 indexed and 4 no_fingerprint skips", got)
	}
	plan := w.Run(exampleLetterLOrdinals(books), nil)
	var p ops.FixerPlan
	if err := json.Unmarshal(plan.Result, &p); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	if len(p.Rows) != 7 {
		t.Fatalf("plan rows = %d, want the 7 letter-l titles", len(p.Rows))
	}
	apply := w.Run(exampleLetterLOrdinals(books), ops.FixerApply{Rows: p.Rows}, Live())
	if apply.Err != nil || apply.State != state.V3Succeeded {
		t.Fatalf("apply: %s %v", apply.State, apply.Err)
	}
	b, err := books.GetBookByID("book-001")
	if err != nil || b == nil || b.Title != "1 Volume 001" {
		t.Errorf("book-001 after apply = %+v (%v), want title 1 Volume 001", b, err)
	}
}

// TestSeedDeterminism: the same Seed gives the same interleaving, so two
// Worlds running one def with one seed commit the same writes in the same
// order.
func TestSeedDeterminism(t *testing.T) {
	t.Parallel()
	writes := func() []byte {
		w := NewWorld(t)
		o := w.Run(titleDef(w.Books()), nil, Live(), ChunkSize(2), Seed(7))
		if o.Err != nil {
			t.Fatalf("run: %v", o.Err)
		}
		type step struct {
			Key    string
			Worker int
			Chunk  int
		}
		var steps []step
		for _, it := range o.Items {
			steps = append(steps, step{it.Key, it.Worker, it.Chunk})
		}
		raw, err := json.Marshal(struct {
			Writes []WriteRecord
			Steps  []step
		}{o.Writes, steps})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		return raw
	}
	a, b := writes(), writes()
	if !bytes.Equal(a, b) {
		t.Fatalf("Seed(7) gave two different runs:\n%s\n%s", a, b)
	}
}

// TestLoseLeaseAt: a def that holds the scan stand-down loses it mid-run;
// the fence is revoked with no cancel behind it, no write commits after the
// revoke, and the run does not succeed.
func TestLoseLeaseAt(t *testing.T) {
	t.Parallel()
	w := NewWorld(t)
	books := w.Books()
	def := ops.Batch("opstest.stand-down", ops.BatchSpec[struct{}, database.Book, titleResult]{
		Common: common("Upper-case titles under the stand-down", ops.Writes(ops.ResBooks)),
		Source: func(rc *ops.RC, p struct{}) (ops.Source[database.Book], error) {
			if _, err := rc.StandDown(); err != nil {
				return ops.Source[database.Book]{}, err
			}
			return bookSource(books)(rc, p)
		},
		Item: func(rc *ops.ItemRC, _ struct{}, b database.Book) error {
			_, err := rc.Writer().ModifyBook(rc.Context(), b.ID, upperTitle)
			return err
		},
		Finish: titleFinish,
	})
	o := w.Run(def, nil, Live(), ChunkSize(3), LoseLeaseAt(5))
	if o.FenceRevokedAt == 0 {
		t.Fatalf("the lease was never lost: %v", o.Notes)
	}
	for _, wr := range o.Writes {
		if wr.Seq > o.FenceRevokedAt {
			t.Errorf("write after the lost lease: %+v (revoke at %d)", wr, o.FenceRevokedAt)
		}
	}
	if o.State == state.V3Succeeded {
		t.Errorf("the run succeeded after losing its stand-down lease")
	}
	t.Logf("LoseLeaseAt(5): state %s, %d writes before the revoke, err %v", o.State, len(o.Writes), o.Err)
	r := newRecorder(t)
	conformance(r, t, def, nil, InWorld(w))
	if got := r.failedSet(); len(got) > 0 {
		t.Errorf("Conformance failed %v for a stand-down def", got)
	}
}
