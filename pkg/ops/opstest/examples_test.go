// file: pkg/ops/opstest/examples_test.go
// version: 1.1.0
// guid: 4b6d8e21-7c3f-4a95-9e12-0f5a3b7c8d64
// last-edited: 2026-10-14

package opstest

import (
	"bytes"
	"context"
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
// order on the same workers; and the seed really drives it, since another
// seed gives another interleaving.
func TestSeedDeterminism(t *testing.T) {
	t.Parallel()
	run := func(seed int64) (writes, steps []byte) {
		w := NewWorld(t)
		o := w.Run(titleDef(w.Books()), nil, Live(), ChunkSize(2), Seed(seed))
		if o.Err != nil {
			t.Fatalf("run: %v", o.Err)
		}
		type step struct {
			Key    string
			Worker int
			Chunk  int
		}
		var ss []step
		for _, it := range o.Items {
			ss = append(ss, step{it.Key, it.Worker, it.Chunk})
		}
		wr, err := json.Marshal(o.Writes)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		st, err := json.Marshal(ss)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		return wr, st
	}
	w1, s1 := run(7)
	w2, s2 := run(7)
	if !bytes.Equal(w1, w2) || !bytes.Equal(s1, s2) {
		t.Fatalf("Seed(7) gave two different runs:\n%s\n%s\n%s\n%s", w1, s1, w2, s2)
	}
	if _, s3 := run(8); bytes.Equal(s1, s3) {
		t.Errorf("Seed(7) and Seed(8) gave the same interleaving %s: the seed does not drive the schedule", s1)
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

// dirtyPrefix is the DirtySet the dirty-title example drains.
const dirtyPrefix = "opstest-dirty:"

// dirtyTitleDef upper-cases the title of every book marked dirty: a DirtySet
// source, drained a page per chunk, each entry deleted once its item
// succeeds.
func dirtyTitleDef(books *FakeBooks) ops.Definition {
	return ops.Batch("opstest.dirty-title", ops.BatchSpec[struct{}, database.Book, titleResult]{
		Common: common("Upper-case dirty titles", ops.Writes(ops.ResBooks)),
		Source: func(*ops.RC, struct{}) (ops.Source[database.Book], error) {
			return ops.DirtySet(dirtyPrefix, func(_ context.Context, ids []string) ([]database.Book, error) {
				out := make([]database.Book, 0, len(ids))
				for _, id := range ids {
					b, err := books.GetBookByID(id)
					if err != nil {
						return nil, err
					}
					if b == nil {
						return nil, fmt.Errorf("dirty book %s does not exist", id)
					}
					out = append(out, *b)
				}
				return out, nil
			}), nil
		},
		Item: func(rc *ops.ItemRC, _ struct{}, b database.Book) error {
			_, err := rc.Writer().ModifyBook(rc.Context(), b.ID, upperTitle)
			return err
		},
		Finish: titleFinish,
	})
}

// TestDirtySetConforms: a DirtySet def runs through the production registry
// and passes Conformance (preview leaves the set alone; a crash resumes from
// zero over the entries still dirty), and a live run drains the set.
func TestDirtySetConforms(t *testing.T) {
	t.Parallel()
	var ids []string
	for i := 1; i <= 12; i++ {
		ids = append(ids, fmt.Sprintf("book-%03d", i))
	}
	w := NewWorld(t, SyntheticBooks(12), WithDirty(dirtyPrefix, ids...))
	def := dirtyTitleDef(w.Books())
	Conformance(t, def, nil, InWorld(w))
	w.Reset()
	o := w.Run(def, nil, Live())
	if o.Err != nil || o.State != state.V3Succeeded {
		t.Fatalf("live run: %s %v", o.State, o.Err)
	}
	if left := w.Dirty(dirtyPrefix); len(left) > 0 {
		t.Errorf("the live run left %v dirty", left)
	}
	if len(o.DirtyCleared) != len(ids) {
		t.Errorf("the live run cleared %d entries, want %d", len(o.DirtyCleared), len(ids))
	}
	for _, id := range ids {
		if b, err := w.Books().GetBookByID(id); err != nil || b == nil || b.Title != strings.ToUpper(b.Title) {
			t.Errorf("book %s after the live run = %+v (%v), want its title upper-cased", id, b, err)
		}
	}
}

// TestCrashWithUncommittedCompletionsResumes: under the production commit
// cadence (the runner commits at most every 2s of its clock, which the
// harness steps 1ms a reading) the crash lands with chunk completions folded
// into the ledger but not committed. Those chunks are lost with the crash;
// the resume runs them again and the World ends as a clean run leaves it.
func TestCrashWithUncommittedCompletionsResumes(t *testing.T) {
	t.Parallel()
	w := NewWorld(t)
	pre := w.Run(titleDef(w.Books()), nil, Live(), ChunkSize(2), Seed(7), CrashAt(11))
	if !pre.Crashed {
		t.Fatalf("CrashAt(11) did not crash: %v", pre.Notes)
	}
	if pre.UncommittedAtCrash == 0 {
		t.Fatalf("the crash left no folded but uncommitted completion; the production commit cadence did not apply")
	}
	recorded := recordedChunks(pre.Ledger)
	post := pre.Resume()
	if post.Err != nil || post.State != state.V3Succeeded {
		t.Fatalf("resume: %s %v", post.State, post.Err)
	}
	rerun := map[int]bool{}
	for _, c := range post.Chunks {
		if recorded[c.Chunk] {
			t.Errorf("the resume leased chunk %d, which the crash's ledger recorded", c.Chunk)
		}
		rerun[c.Chunk] = true
	}
	lost := 0
	for _, it := range pre.Items {
		if it.Done && !it.Committed && !rerun[it.Chunk] {
			t.Errorf("item %s finished before the crash without its completion committed, and the resume "+
				"did not run its chunk %d again", it.Key, it.Chunk)
		}
		if it.Done && !it.Committed {
			lost++
		}
	}
	clean := NewWorld(t)
	if o := clean.Run(titleDef(clean.Books()), nil, Live(), ChunkSize(2)); o.Err != nil {
		t.Fatalf("clean run: %v", o.Err)
	}
	if got, want := w.State(), clean.State(); got != want {
		t.Errorf("crash + resume left a different World than a clean run\n%s", stateDiff(want, got))
	}
	t.Logf("CrashAt(11): %d chunk completion(s) uncommitted at the crash, %d finished item(s) re-run",
		pre.UncommittedAtCrash, lost)
}

// TestShutdownStopsForResume: a registry shutdown mid-run (a server restart)
// stops the run for resume. The stopped attempt does not run Finish, the
// resumed one runs it exactly once with every item counted, and the World
// ends as a clean run leaves it.
func TestShutdownStopsForResume(t *testing.T) {
	t.Parallel()
	w := NewWorld(t)
	books := w.Books()
	def := finishLogDef(books)
	pre := w.Run(def, nil, Live(), ChunkSize(2), ShutdownAt(5))
	if !pre.Crashed {
		t.Fatalf("ShutdownAt(5) did not stop the run: %s %v %v", pre.State, pre.Err, pre.Notes)
	}
	if n := len(books.Log("finish")); n != 0 {
		t.Fatalf("the attempt stopped for resume ran Finish %d time(s); the resumed attempt runs it", n)
	}
	t.Logf("ShutdownAt(5): the stopped attempt ended %s with %d item(s) started", pre.State, len(pre.Items))
	post := pre.Resume()
	if post.Err != nil || post.State != state.V3Succeeded {
		t.Fatalf("resume: %s %v", post.State, post.Err)
	}
	if n := len(books.Log("finish")); n != 1 {
		t.Errorf("Finish ran %d time(s) across the stop and the resume, want exactly 1", n)
	}
	if got := post.Summary.Done + post.Summary.Failed + post.Summary.Skipped; got != 12 {
		t.Errorf("the resumed Finish saw %d items, want all 12 across both attempts", got)
	}
	for _, it := range post.Items {
		for _, p := range pre.Items {
			if p.Key == it.Key && p.Committed {
				t.Errorf("the resume ran %s again, which the stopped attempt committed", it.Key)
			}
		}
	}
	for i := 1; i <= 12; i++ {
		id := fmt.Sprintf("book-%03d", i)
		if b, err := books.GetBookByID(id); err != nil || b == nil || b.Title != strings.ToUpper(b.Title) {
			t.Errorf("book %s after the resume = %+v (%v), want its title upper-cased", id, b, err)
		}
	}
}

// finishLogDef is titleDef whose Finish appends the op id to the World's
// "finish" log, so a test can count how many times Finish ran.
func finishLogDef(books *FakeBooks) ops.Definition {
	return ops.Batch("opstest.upper-title-finish-log", ops.BatchSpec[struct{}, database.Book, titleResult]{
		Common: common("Upper-case titles, logging Finish", ops.Writes(ops.ResBooks)),
		Source: bookSource(books),
		Item: func(rc *ops.ItemRC, _ struct{}, b database.Book) error {
			_, err := rc.Writer().ModifyBook(rc.Context(), b.ID, upperTitle)
			return err
		},
		Finish: func(rc *ops.RC, _ struct{}, s ops.Summary) (titleResult, error) {
			if err := books.AppendLog("finish", rc.OpID()); err != nil {
				return titleResult{}, err
			}
			return titleResult{Done: s.Done}, nil
		},
	})
}

// TestCancelRunsFinish: a canceled run is not resumed, so its own attempt
// runs Finish, once, with Canceled set.
func TestCancelRunsFinish(t *testing.T) {
	t.Parallel()
	w := NewWorld(t)
	books := w.Books()
	o := w.Run(finishLogDef(books), nil, Live(), ChunkSize(2), CancelAt(5))
	if o.State != state.V3Canceled {
		t.Fatalf("CancelAt(5): the run ended %s, not canceled (%v)", o.State, o.Notes)
	}
	if n := len(books.Log("finish")); n != 1 {
		t.Errorf("a canceled run ran Finish %d time(s), want exactly 1", n)
	}
	if !o.Summary.Canceled {
		t.Errorf("Finish's Summary does not say Canceled")
	}
}
