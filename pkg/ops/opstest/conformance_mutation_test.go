// file: pkg/ops/opstest/conformance_mutation_test.go
// version: 1.1.0
// guid: 9a4f2c63-1e8b-4d57-b0a9-5c3e7f1d2b86
// last-edited: 2026-10-14

package opstest

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The mutation tests prove each assertion can fail: each runs Conformance
// against a deliberately broken def (or runner, or reporter) through a
// recording tester and asserts the named subtest failed, while a correct twin
// run the same way passes every subtest. A harness whose assertions cannot
// fail would make every later port look green.

// recorder is a tester that records which assertion subtests failed instead
// of failing the test.
type recorder struct {
	t      *testing.T
	name   string
	parent *recorder
	root   *recState
}

type recState struct {
	mu     sync.Mutex
	failed map[string][]string // assertion name -> failure messages (with their subtest path)
	any    bool
}

func newRecorder(t *testing.T) *recorder {
	return &recorder{t: t, root: &recState{failed: map[string][]string{}}}
}

func (r *recorder) Helper() {}

func (r *recorder) Logf(format string, args ...any) {
	r.t.Logf("%s: %s", r.path(), fmt.Sprintf(format, args...))
}

func (r *recorder) Errorf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.root.mu.Lock()
	r.root.any = true
	r.root.failed[r.name] = append(r.root.failed[r.name], r.path()+": "+msg)
	r.root.mu.Unlock()
}

func (r *recorder) Run(name string, f func(tester)) bool {
	child := &recorder{t: r.t, name: name, parent: r, root: r.root}
	f(child)
	return true
}

func (r *recorder) path() string {
	var parts []string
	for n := r; n != nil; n = n.parent {
		if n.name != "" {
			parts = append([]string{n.name}, parts...)
		}
	}
	return strings.Join(parts, "/")
}

// failedSet is the sorted set of assertion names that failed.
func (r *recorder) failedSet() []string {
	r.root.mu.Lock()
	defer r.root.mu.Unlock()
	return slices.Sorted(maps.Keys(r.root.failed))
}

func (r *recorder) messages(name string) []string {
	r.root.mu.Lock()
	defer r.root.mu.Unlock()
	return slices.Clone(r.root.failed[name])
}

// conform runs Conformance against a recorder and returns it.
func conform(t *testing.T, w *World, def ops.Definition, opts ...ConformanceOpt) *recorder {
	t.Helper()
	rec := newRecorder(t)
	conformance(rec, t, def, nil, append([]ConformanceOpt{InWorld(w)}, opts...)...)
	return rec
}

// expectCaught asserts the broken run failed want (and, when exact, nothing
// else) and the correct twin failed nothing.
func expectCaught(t *testing.T, broken, twin *recorder, want string, exact bool) {
	t.Helper()
	if got := twin.failedSet(); len(got) > 0 {
		for _, name := range got {
			for _, m := range twin.messages(name) {
				t.Errorf("correct twin: %s", m)
			}
		}
		t.Fatalf("the correct twin failed %v; it must pass every assertion", got)
	}
	got := broken.failedSet()
	if !slices.Contains(got, want) {
		t.Fatalf("the broken def was not caught by %s (failed: %v)", want, got)
	}
	if exact && !slices.Equal(got, []string{want}) {
		t.Fatalf("the broken def failed %v; it must fail %s and nothing else", got, want)
	}
	msgs := broken.messages(want)
	t.Logf("%s caught it (%d failures), first: %s", want, len(msgs), msgs[0])
}

// TestCatches_WriteAfterCancel: an Item that, when the Writer refuses a
// write because the run is fenced, makes the write straight to the store
// instead. CancelAt and AbandonAt revoke the fence while an item is running,
// so the fallback write commits after the revoke.
func TestCatches_WriteAfterCancel(t *testing.T) {
	t.Parallel()
	w := NewWorld(t)
	books := w.Books()
	broken := ops.Batch("opstest.broken.write-after-cancel", ops.BatchSpec[struct{}, database.Book, titleResult]{
		Common: common("Write around the fence", ops.Writes(ops.ResBooks)),
		Source: bookSource(books),
		Item: func(rc *ops.ItemRC, _ struct{}, b database.Book) error {
			_, err := rc.Writer().ModifyBook(rc.Context(), b.ID, upperTitle)
			if fenced(err) {
				_, err = books.ModifyBook(b.ID, upperTitle) // the bug: around the fence
			}
			return err
		},
		Finish: titleFinish,
	})
	expectCaught(t, conform(t, w, broken), conform(t, w, titleDef(books)), AssertNoWriteAfterFence, false)
}

// TestCatches_HistoryAfterCancel: an Item that, when the Writer refuses its
// write because the run is fenced, journals the change itself anyway. The
// row commits after the revoke, inside the attempt's window.
func TestCatches_HistoryAfterCancel(t *testing.T) {
	t.Parallel()
	w := NewWorld(t)
	books, journal := w.Books(), w.Journal()
	broken := ops.Batch("opstest.broken.history-after-cancel", ops.BatchSpec[struct{}, database.Book, titleResult]{
		Common: common("Journal around the fence", ops.Writes(ops.ResBooks)),
		Source: bookSource(books),
		Item: func(rc *ops.ItemRC, _ struct{}, b database.Book) error {
			_, err := rc.Writer().ModifyBook(rc.Context(), b.ID, upperTitle)
			if fenced(err) { // the bug: the history row goes around the fence
				_ = journal.CreateOperationChange(&database.OperationChange{OperationID: rc.OpID(), BookID: b.ID,
					ChangeType: "title_write", FieldName: "title", NewValue: strings.ToUpper(b.Title)})
			}
			return err
		},
		Finish: titleFinish,
	})
	expectCaught(t, conform(t, w, broken), conform(t, w, titleDef(books)), AssertNoWriteAfterFence, false)
}

// TestCatches_HistoryBeforeWrite: an Effect that journals the change before
// it makes the write, so a crash between the two would leave history for a
// write that never happened.
func TestCatches_HistoryBeforeWrite(t *testing.T) {
	t.Parallel()
	w := NewWorld(t)
	books, journal := w.Books(), w.Journal()
	effectDef := func(id string, historyFirst bool) ops.Definition {
		return ops.Batch(id, ops.BatchSpec[struct{}, database.Book, titleResult]{
			Common: common("Tag every book", ops.Writes(ops.ResBooks)),
			Source: bookSource(books),
			Item: func(rc *ops.ItemRC, _ struct{}, b database.Book) error {
				sub := ops.Subject{Type: ops.SubjectBook, ID: b.ID}
				return rc.Writer().Effect(rc.Context(), ops.EffectSpec{Name: "opstest.tag", Subject: sub},
					func() ([]ops.HistoryRow, error) {
						if historyFirst { // the bug: the journal row commits ahead of the write
							if err := journal.CreateOperationChange(&database.OperationChange{OperationID: rc.OpID(),
								BookID: b.ID, ChangeType: "tag_write", FieldName: "tag", NewValue: "tagged"}); err != nil {
								return nil, err
							}
							return nil, books.PutValue(sub, "tag", "tagged")
						}
						if err := books.PutValue(sub, "tag", "tagged"); err != nil {
							return nil, err
						}
						return []ops.HistoryRow{{BookID: b.ID, Field: "tag", New: "tagged"}}, nil
					})
			},
			Finish: titleFinish,
		})
	}
	expectCaught(t, conform(t, w, effectDef("opstest.broken.history-first", true)),
		conform(t, w, effectDef("opstest.tag", false)), AssertHistoryFollowsWrite, false)
}

// TestCatches_HistoryInsideEffectBeforeWrite: an item that writes the
// book's title through the Writer, then runs an Effect that journals its tag
// before it puts the tag. A match by book alone passes it (the title write
// precedes the row); the row must pair with a write of its book made inside
// the call that recorded it.
func TestCatches_HistoryInsideEffectBeforeWrite(t *testing.T) {
	t.Parallel()
	w := NewWorld(t)
	books, journal := w.Books(), w.Journal()
	tagDef := func(id string, historyFirst bool) ops.Definition {
		return ops.Batch(id, ops.BatchSpec[struct{}, database.Book, titleResult]{
			Common: common("Title and tag every book", ops.Writes(ops.ResBooks)),
			Source: bookSource(books),
			Item: func(rc *ops.ItemRC, _ struct{}, b database.Book) error {
				if _, err := rc.Writer().ModifyBook(rc.Context(), b.ID, upperTitle); err != nil {
					return err
				}
				sub := ops.Subject{Type: ops.SubjectBook, ID: b.ID}
				return rc.Writer().Effect(rc.Context(), ops.EffectSpec{Name: "opstest.tag", Subject: sub},
					func() ([]ops.HistoryRow, error) {
						if historyFirst { // the bug: the tag's row commits ahead of the tag
							if err := journal.CreateOperationChange(&database.OperationChange{OperationID: rc.OpID(),
								BookID: b.ID, ChangeType: "tag_write", FieldName: "tag", NewValue: "tagged"}); err != nil {
								return nil, err
							}
							return nil, books.PutValue(sub, "tag", "tagged")
						}
						if err := books.PutValue(sub, "tag", "tagged"); err != nil {
							return nil, err
						}
						return []ops.HistoryRow{{BookID: b.ID, Field: "tag", New: "tagged"}}, nil
					})
			},
			Finish: titleFinish,
		})
	}
	expectCaught(t, conform(t, w, tagDef("opstest.broken.title-then-history-first", true)),
		conform(t, w, tagDef("opstest.title-then-tag", false)), AssertHistoryFollowsWrite, true)
}

// tbRecorder is a testing.TB that records Errorf instead of failing, so a
// test can assert what a World reports through its TB (checkOrphans).
type tbRecorder struct {
	*testing.T
	mu   sync.Mutex
	errs []string
}

func (r *tbRecorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
	r.mu.Unlock()
}

func (r *tbRecorder) errors() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.errs)
}

// TestCatches_WriteAfterRunEnds is TestCatches_WriteAfterCancel's async
// twin: the fenced item hands its write (a book write, or a history row) to
// a goroutine that makes it straight to the store once the run is over. No
// attempt's window holds it, so the per-attempt assertion cannot see it; the
// World reports it, at its next Reset, as committed outside every attempt.
func TestCatches_WriteAfterRunEnds(t *testing.T) {
	t.Parallel()
	run := func(t *testing.T, leak string) (*Outcome, []string) {
		rec := &tbRecorder{T: t}
		w := NewWorld(rec)
		books, journal := w.Books(), w.Journal()
		release := make(chan struct{})
		var wg sync.WaitGroup
		def := ops.Batch("opstest.write-after-run", ops.BatchSpec[struct{}, database.Book, titleResult]{
			Common: common("Write after the run", ops.Writes(ops.ResBooks)),
			Source: bookSource(books),
			Item: func(rc *ops.ItemRC, _ struct{}, b database.Book) error {
				_, err := rc.Writer().ModifyBook(rc.Context(), b.ID, upperTitle)
				if fenced(err) && leak != "" { // the bug: the write outlives the run
					opID := rc.OpID()
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-release
						if leak == "journal" {
							_ = journal.CreateOperationChange(&database.OperationChange{OperationID: opID,
								BookID: b.ID, ChangeType: "tag_write", FieldName: "tag", NewValue: "late"})
							return
						}
						_, _ = books.ModifyBook(b.ID, upperTitle)
					}()
				}
				return err
			},
			Finish: titleFinish,
		})
		o := w.Run(def, nil, Live(), ChunkSize(2), CancelAt(5))
		close(release)
		wg.Wait()
		w.Reset()
		return o, rec.errors()
	}
	for leak, want := range map[string]string{
		"write":   "a write committed outside every run attempt",
		"journal": "a history row was recorded outside every run attempt",
	} {
		t.Run(leak, func(t *testing.T) {
			t.Parallel()
			o, errs := run(t, leak)
			if o.FenceRevokedAt == 0 {
				t.Fatalf("CancelAt(5) never revoked the fence: %v", o.Notes)
			}
			for _, wr := range o.Writes {
				if wr.Seq > o.FenceRevokedAt {
					t.Fatalf("the leaked write landed inside the attempt's window (seq %d, revoke %d, end %d); "+
						"the test no longer exercises a write after the run", wr.Seq, o.FenceRevokedAt, o.EndSeq)
				}
			}
			for _, h := range o.History {
				if h.Seq > o.FenceRevokedAt {
					t.Fatalf("the leaked history row landed inside the attempt's window (seq %d, revoke %d); "+
						"the test no longer exercises a row after the run", h.Seq, o.FenceRevokedAt)
				}
			}
			if len(errs) == 0 || !strings.Contains(errs[0], AssertNoWriteAfterFence) || !strings.Contains(errs[0], want) {
				t.Fatalf("what the leaked goroutine made after the run was not reported as %q: %v", want, errs)
			}
			t.Logf("%s caught it (%d failures), first: %s", AssertNoWriteAfterFence, len(errs), errs[0])
		})
	}
	t.Run("twin", func(t *testing.T) {
		t.Parallel()
		if _, twin := run(t, ""); len(twin) > 0 {
			t.Fatalf("the correct twin was reported: %v", twin)
		}
	})
}

// TestCrashResumeCountsCommittedItems: crash_resume_at_least_once asks for
// every item's completion to be committed across the crash and the resume,
// not merely started. Here a runner recorded item book-002's chunk complete
// at lease: the item started before the crash, never returned, and the
// resume skipped its chunk. A count of starts sees it run once; the
// committed count catches it, even though the resume's progress ends
// complete, so the checkEnd backstop does not fire.
func TestCrashResumeCountsCommittedItems(t *testing.T) {
	t.Parallel()
	total := int64(2)
	rec := newRecorder(t)
	s := &suite{clean: &Outcome{Items: []ItemEvent{{Key: "book-001"}, {Key: "book-002"}}}}
	pre := &Outcome{Crashed: true, Order: ops.Snapshot, ResumePolicy: ops.ResumeContinue, Items: []ItemEvent{
		{Key: "book-001", Chunk: 0, Done: true, Committed: true},
		{Key: "book-002", Chunk: 1}, // started; its chunk recorded at lease; never returned
	}}
	post := &Outcome{State: state.V3Succeeded, Order: ops.Snapshot, ResumePolicy: ops.ResumeContinue,
		Progress: []ops.ProgressSnapshot{{Done: total, Total: &total}}}
	rec.Run(AssertCrashResumeAtLeastOnce, func(tt tester) {
		s.checkCrash(tt, crashPair{label: "CrashAt(0)", chunk: -1, pre: pre, post: post}, false)
	})
	msgs := rec.messages(AssertCrashResumeAtLeastOnce)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "book-002 never completed") {
		t.Fatalf("crash_resume_at_least_once reported %v; want exactly book-002 never completed", msgs)
	}
	pre.Items[1].Done, pre.Items[1].Committed = true, true
	ok := newRecorder(t)
	ok.Run(AssertCrashResumeAtLeastOnce, func(tt tester) {
		s.checkCrash(tt, crashPair{label: "CrashAt(0)", chunk: -1, pre: pre, post: post}, false)
	})
	if got := ok.failedSet(); len(got) > 0 {
		t.Fatalf("with book-002 committed before the crash, the assertion still failed: %v",
			ok.messages(AssertCrashResumeAtLeastOnce))
	}
}

// TestCatches_AppendOnlyLie: a paged source over book ids claims AppendOnly,
// but a book inserted with a smaller id lands below the cursor, where the
// resumed run never looks. The Snapshot twin makes no such claim.
func TestCatches_AppendOnlyLie(t *testing.T) {
	t.Parallel()
	w := NewWorld(t)
	expectCaught(t, conform(t, w, streamDef(w.Books(), ops.AppendOnly)),
		conform(t, w, streamDef(w.Books(), ops.Snapshot)), AssertAppendOnlyClaim, true)
}

// TestCatches_NonIdempotentItem: an Item that appends to a side-effect log
// each time it runs. The runner's at-least-once contract lets a crashed
// chunk's items run twice, so the crash + resume World holds more log
// entries than a clean run's (the log is World state, so its Finish, which
// counts the log, would report a different number too), and a second run
// appends again. This fails idempotent_item and no other assertion: the
// runner kept every one of its own invariants, the def broke its contract.
func TestCatches_NonIdempotentItem(t *testing.T) {
	t.Parallel()
	w := NewWorld(t)
	books := w.Books()
	logDef := func(id string, appendEach bool) ops.Definition {
		return ops.Batch(id, ops.BatchSpec[struct{}, database.Book, titleResult]{
			Common: common("Log every book", ops.Writes(ops.ResBooks)),
			Source: bookSource(books),
			Item: func(rc *ops.ItemRC, _ struct{}, b database.Book) error {
				sub := ops.Subject{Type: ops.SubjectBook, ID: b.ID}
				return rc.Writer().Effect(rc.Context(), ops.EffectSpec{Name: "opstest.log", Subject: sub},
					func() ([]ops.HistoryRow, error) {
						if appendEach { // the bug: every run appends again
							return nil, books.AppendLog("seen", b.ID)
						}
						return nil, books.PutValue(sub, "seen", "yes")
					})
			},
			Finish: func(*ops.RC, struct{}, ops.Summary) (titleResult, error) {
				return titleResult{Done: int64(len(books.Log("seen")))}, nil
			},
		})
	}
	expectCaught(t, conform(t, w, logDef("opstest.broken.append-log", true)),
		conform(t, w, logDef("opstest.put-value", false)), AssertIdempotentItem, true)
}

// TestCatches_PartitionStraddle: a chunker that groups by partition but cuts
// every size keys regardless, so a partition straddles two chunks (and two
// workers). The runner's own chunker is the twin.
func TestCatches_PartitionStraddle(t *testing.T) {
	t.Parallel()
	w := NewWorld(t)
	straddle := WithChunker(func(keys, parts []string, size int) ([]int, []int) {
		order := make([]int, len(keys))
		for i := range order {
			order[i] = i
		}
		slices.SortStableFunc(order, func(a, b int) int { return strings.Compare(parts[a], parts[b]) })
		var starts []int
		for i := 0; i < len(order); i += max(size, 1) {
			starts = append(starts, i)
		}
		return order, starts
	})
	def := partitionDef(w.Books())
	expectCaught(t, conform(t, w, def, WithRunOpts(straddle)), conform(t, w, def),
		AssertPartitionOneWorkerInOrder, false)
}

// TestCatches_CompletedChunkRerun: a lease policy that hands out a chunk the
// ledger already records, so a resume re-runs completed work.
func TestCatches_CompletedChunkRerun(t *testing.T) {
	t.Parallel()
	w := NewWorld(t)
	rerun := WithLeasePolicy(func(_ func(int) bool, chunk int) int { return chunk })
	def := titleDef(w.Books())
	expectCaught(t, conform(t, w, def, WithRunOpts(rerun)), conform(t, w, def), AssertCrashMidChunkNoRerun, false)
}

// TestCatches_PreviewWrites: an Item that, in Preview, writes straight to
// the store to "show" its result.
func TestCatches_PreviewWrites(t *testing.T) {
	t.Parallel()
	w := NewWorld(t)
	books := w.Books()
	broken := ops.Batch("opstest.broken.preview-writes", ops.BatchSpec[struct{}, database.Book, titleResult]{
		Common: common("Write in preview", ops.Writes(ops.ResBooks)),
		Source: bookSource(books),
		Item: func(rc *ops.ItemRC, _ struct{}, b database.Book) error {
			if rc.Mode() == ops.Preview { // the bug: a write the Writer never saw
				if err := books.PutValue(ops.Subject{Type: ops.SubjectBook, ID: b.ID}, "previewed", "yes"); err != nil {
					return err
				}
			}
			_, err := rc.Writer().ModifyBook(rc.Context(), b.ID, upperTitle)
			return err
		},
		Finish: titleFinish,
	})
	expectCaught(t, conform(t, w, broken), conform(t, w, titleDef(books)), AssertPreviewWritesNothing, false)
}

// restartSink is a progress reporter that forgets the resumed base: it
// reports Done as the items this attempt started, so a resume's progress
// starts again from zero.
type restartSink struct {
	mu      sync.Mutex
	started int64
	snaps   []ops.ProgressSnapshot
}

func (s *restartSink) Published(p ops.ProgressSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.Done = s.started
	s.snaps = append(s.snaps, p)
}

func (s *restartSink) ItemStarted(int, int) {
	s.mu.Lock()
	s.started++
	s.mu.Unlock()
}

func (s *restartSink) Snapshots() []ops.ProgressSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.snaps)
}

// TestCatches_NonMonotoneProgress: progress Done that restarts from zero on
// resume.
func TestCatches_NonMonotoneProgress(t *testing.T) {
	t.Parallel()
	w := NewWorld(t)
	sink := WithProgressSink(func() ProgressSink { return &restartSink{} })
	def := titleDef(w.Books())
	expectCaught(t, conform(t, w, def, WithRunOpts(sink)), conform(t, w, def), AssertProgressMonotone, false)
}

// TestAppendOnlyClaimHolds is AppendOnlyLie's other half: a source that
// really is append-only passes append_only_claim. It pages the seeded books
// first and every book created later after them (the way a source keyed by
// a creation sequence would), so the inserted book, whose id sorts below
// every seeded one, still lands above the cursor.
func TestAppendOnlyClaimHolds(t *testing.T) {
	t.Parallel()
	w := NewWorld(t)
	books := w.Books()
	seeded := map[string]bool{}
	all, err := books.AllBooks()
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range all {
		seeded[b.ID] = true
	}
	keyOf := func(b database.Book) string {
		if seeded[b.ID] {
			return "0/" + b.ID
		}
		return "1/" + b.ID
	}
	def := ops.Batch("opstest.upper-title-by-creation", ops.BatchSpec[struct{}, database.Book, titleResult]{
		Common: common("Upper-case titles in creation order", ops.Writes(ops.ResBooks)),
		Source: func(*ops.RC, struct{}) (ops.Source[database.Book], error) {
			return ops.Source[database.Book]{
				Pages: func(_ context.Context, after string, limit int) ([]database.Book, string, error) {
					all, err := books.AllBooks()
					if err != nil {
						return nil, "", err
					}
					slices.SortFunc(all, func(a, b database.Book) int { return strings.Compare(keyOf(a), keyOf(b)) })
					i := slices.IndexFunc(all, func(b database.Book) bool { return keyOf(b) > after })
					if i < 0 {
						return nil, "", nil
					}
					page := all[i:min(i+max(limit, 1), len(all))]
					if i+len(page) >= len(all) {
						return page, "", nil
					}
					return page, keyOf(page[len(page)-1]), nil
				},
				Key: keyOf, Order: ops.AppendOnly, Total: ops.Unknown(),
			}, nil
		},
		Item: func(rc *ops.ItemRC, _ struct{}, b database.Book) error {
			_, err := rc.Writer().ModifyBook(rc.Context(), b.ID, upperTitle)
			return err
		},
		Finish: titleFinish,
	})
	inserted := database.Book{ID: "book-000", Title: "Inserted mid-run", FilePath: "/library/synthetic/book-000",
		Format: "m4b"}
	r := conform(t, w, def, BelowCursor(inserted))
	if got := r.failedSet(); len(got) > 0 {
		for _, name := range got {
			for _, m := range r.messages(name) {
				t.Errorf("%s", m)
			}
		}
	}
}

// TestFileEffectHistoryConforms: an Effect on a book_file subject whose
// history row names the file's book passes history_follows_write; the write
// is matched to the row through the file's book, not missed for want of a
// book id on a file subject.
func TestFileEffectHistoryConforms(t *testing.T) {
	t.Parallel()
	w := NewWorld(t)
	books := w.Books()
	def := ops.Batch("opstest.tag-files", ops.BatchSpec[struct{}, database.BookFile, titleResult]{
		Common: common("Tag every book file", ops.Writes(ops.ResBookFiles)),
		Source: func(*ops.RC, struct{}) (ops.Source[database.BookFile], error) {
			files, err := books.AllBookFiles()
			return ops.Source[database.BookFile]{Items: files, Key: func(f database.BookFile) string { return f.ID },
				Order: ops.Snapshot}, err
		},
		Item: func(rc *ops.ItemRC, _ struct{}, f database.BookFile) error {
			sub := ops.Subject{Type: ops.SubjectBookFile, ID: f.ID}
			return rc.Writer().Effect(rc.Context(), ops.EffectSpec{Name: "opstest.tag-file", Subject: sub},
				func() ([]ops.HistoryRow, error) {
					if err := books.PutValue(sub, "tag", "tagged"); err != nil {
						return nil, err
					}
					return []ops.HistoryRow{{BookID: f.BookID, Field: "file.tag:" + f.ID, New: "tagged"}}, nil
				})
		},
		Finish: titleFinish,
	})
	r := conform(t, w, def)
	for _, name := range r.failedSet() {
		for _, m := range r.messages(name) {
			t.Errorf("%s", m)
		}
	}
}
