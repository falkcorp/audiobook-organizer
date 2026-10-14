// file: pkg/ops/opstest/conformance.go
// version: 1.1.0
// guid: 2d8e4b17-5f3a-4c69-a1b8-6e9d2c7f4a35
// last-edited: 2026-10-14

package opstest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The assertion names: one subtest each, so a failure points at the rule.
const (
	AssertPreviewWritesNothing      = "preview_writes_nothing"
	AssertHistoryFollowsWrite       = "history_follows_write"
	AssertNoWriteAfterFence         = "no_write_after_fence"
	AssertCrashResumeAtLeastOnce    = "crash_resume_at_least_once"
	AssertCrashMidChunkNoRerun      = "crash_mid_chunk_no_completed_chunk_twice"
	AssertPartitionOneWorkerInOrder = "partition_one_worker_in_order"
	AssertAppendOnlyClaim           = "append_only_claim"
	AssertProgressMonotone          = "progress_monotone"
	AssertIdempotentItem            = "idempotent_item"
)

// ConformanceOpt configures Conformance.
type ConformanceOpt func(*confCfg)

type confCfg struct {
	world   *World
	runOpts []RunOpt
	below   any
}

// InWorld runs every case in w (reset before each run). A def whose Source
// or Load reads the World's Books must be built over w and passed this
// option; without it Conformance builds a default World (NewWorld's 12
// synthetic books).
func InWorld(w *World) ConformanceOpt { return func(c *confCfg) { c.world = w } }

// WithRunOpts adds opts to every run Conformance makes (after its own
// Live/ChunkSize/fault options). The mutation tests inject runner and
// reporter mutations this way.
func WithRunOpts(opts ...RunOpt) ConformanceOpt {
	return func(c *confCfg) { c.runOpts = append(c.runOpts, opts...) }
}

// BelowCursor is the item append_only_claim inserts below the cursor (see
// InsertBelowCursor); nil, the default, generates one.
func BelowCursor(item any) ConformanceOpt { return func(c *confCfg) { c.below = item } }

// Conformance proves def keeps the runner's invariants. Every op gets these
// assertions for free, one subtest each, for ChunkSize 1, 3 and the def's
// default, with Workers = max(4, GOMAXPROCS):
//
//   - preview_writes_nothing: a Preview run commits no write and no history
//     row, and deletes no DirtySet entry;
//   - history_follows_write: every history row follows a write of its book,
//     made inside the same Writer call when the row was made inside one;
//   - no_write_after_fence: no write or history row commits after the fence
//     is revoked (CancelAt, AbandonAt, LoseLeaseAt), and none outside every
//     attempt (a goroutine left writing after its run ended, reported at the
//     World's next Reset); a canceled run's Finish sees Canceled and the
//     ledger's folded counts; an abandoned run keeps its key;
//   - crash_resume_at_least_once: CrashAt at 3 positions and ShutdownAt (a
//     graceful stop for resume) in the middle, then Resume: every item
//     completed with its completion committed, no chunk the ledger recorded
//     ran again, and the resumed Finish counts every item once;
//   - crash_mid_chunk_no_completed_chunk_twice: CrashMidChunk in the first, a
//     middle and the last chunk: the resume re-runs that chunk from its first
//     item and nothing the ledger recorded;
//   - partition_one_worker_in_order (PartitionBy only): every partition ran in
//     one chunk, on one worker, in source order;
//   - append_only_claim (AppendOnly only): InsertBelowCursor fails the claim
//     when the item is never processed;
//   - progress_monotone: Done, Failed and every counter never decrease,
//     across a resume that continues too (a resume from zero restarts by
//     design), and end at Total (and Chunks.Done at Chunks.Total);
//   - idempotent_item: a second run over the first one's end state changes
//     nothing, and every crash plus resume leaves the same World as a clean
//     run.
//
// A Fixer is planned in Preview and its plan applied in Live; a Task gets
// the assertions that do not need items.
//
// Each case starts from a World.Reset, and runs are compared by item key, so
// a def's keys must follow from the World's data alone (an id, a path), never
// from when a row happened to be written.
func Conformance(t *testing.T, def ops.Definition, params any, opts ...ConformanceOpt) {
	t.Helper()
	conformance(tAdapter{t}, t, def, params, opts...)
}

// tester is the part of *testing.T Conformance reports through, so the
// mutation tests can run it against a recorder and assert which subtest
// failed.
type tester interface {
	Helper()
	Logf(format string, args ...any)
	Errorf(format string, args ...any)
	Run(name string, f func(tester)) bool
}

type tAdapter struct{ t *testing.T }

func (a tAdapter) Helper()                           { a.t.Helper() }
func (a tAdapter) Logf(format string, args ...any)   { a.t.Helper(); a.t.Logf(format, args...) }
func (a tAdapter) Errorf(format string, args ...any) { a.t.Helper(); a.t.Errorf(format, args...) }
func (a tAdapter) Run(name string, f func(tester)) bool {
	return a.t.Run(name, func(t *testing.T) { f(tAdapter{t}) })
}

// conformance reports to tt; tb owns the World (its cleanup and its fatal
// harness errors).
func conformance(tt tester, tb testing.TB, def ops.Definition, params any, opts ...ConformanceOpt) {
	tt.Helper()
	var cfg confCfg
	for _, o := range opts {
		o(&cfg)
	}
	c := ops.Compile(def)
	if len(c.Problems) > 0 {
		tt.Errorf("%s does not compile: %v", c.ID, c.Problems)
		return
	}
	w := cfg.world
	if w == nil {
		w = NewWorld(tb)
	}
	raw, err := encodeParams(params)
	if err != nil {
		tt.Errorf("params do not encode: %v", err)
		return
	}
	switch c.Kind {
	case ops.KindTask:
		taskConformance(tt, w, def, raw, cfg)
	case ops.KindBatch, ops.KindFixer:
		w.Reset()
		probe := w.Run(def, raw, cfg.runOpts...)
		if probe.Err != nil {
			tt.Errorf("%s: the preview run failed: %v", c.ID, probe.Err)
			return
		}
		sizes := []int{1, 3}
		if !slices.Contains(sizes, probe.DefChunkSize) {
			sizes = append(sizes, probe.DefChunkSize)
		}
		for _, size := range sizes {
			tt.Run(fmt.Sprintf("chunk_size_%d", size), func(tt tester) {
				s := &suite{w: w, def: def, kind: c.Kind, params: raw, cfg: cfg, size: size}
				s.run(tt)
			})
		}
	default:
		tt.Errorf("%s: Conformance does not run %s definitions", c.ID, c.Kind)
	}
}

// taskConformance is the subset that applies without items.
func taskConformance(tt tester, w *World, def ops.Definition, raw json.RawMessage, cfg confCfg) {
	w.Reset()
	preview := w.Run(def, raw, cfg.runOpts...)
	w.Reset()
	live := w.Run(def, raw, append(slices.Clone(cfg.runOpts), Live())...)
	second := w.Run(def, raw, append(slices.Clone(cfg.runOpts), Live())...)
	tt.Run(AssertPreviewWritesNothing, func(tt tester) { checkPreview(tt, preview) })
	tt.Run(AssertHistoryFollowsWrite, func(tt tester) { checkHistory(tt, live) })
	tt.Run(AssertIdempotentItem, func(tt tester) {
		checkOK(tt, "the first live run", live)
		checkOK(tt, "the second live run", second)
		checkUnchanged(tt, second)
	})
	for _, name := range []string{AssertNoWriteAfterFence, AssertCrashResumeAtLeastOnce, AssertCrashMidChunkNoRerun,
		AssertPartitionOneWorkerInOrder, AssertAppendOnlyClaim, AssertProgressMonotone} {
		tt.Run(name, func(tt tester) { tt.Logf("not applicable to a Task: it has no items, chunks or ledger") })
	}
}

// crashPair is a crashed attempt and its resume, with the World each left.
type crashPair struct {
	label     string
	chunk     int
	pre, post *Outcome
	state     string
}

// suite is one chunk size's set of runs and assertions.
type suite struct {
	w      *World
	def    ops.Definition
	kind   ops.Kind
	params json.RawMessage
	cfg    confCfg
	size   int

	preview, clean, second *Outcome
	cleanState             string
	fence                  []*Outcome
	crashAt, crashMid      []crashPair
	midNote                string
	appendRun              []*Outcome // the faulted attempt, its resume, a fresh run
}

func (s *suite) opts(extra ...RunOpt) []RunOpt {
	o := []RunOpt{ChunkSize(s.size), Workers(max(minWorkers, runtime.GOMAXPROCS(0)))}
	o = append(o, extra...)
	return append(o, s.cfg.runOpts...)
}

func (s *suite) live(extra ...RunOpt) []RunOpt { return s.opts(append([]RunOpt{Live()}, extra...)...) }

// run makes every run first, then asserts, so each assertion subtest has its
// data whichever subtests a -run pattern selects.
func (s *suite) run(tt tester) {
	tt.Helper()
	w := s.w
	w.Reset()
	s.preview = w.Run(s.def, s.params, s.opts()...)
	liveParams := s.params
	if s.kind == ops.KindFixer {
		var plan ops.FixerPlan
		if err := json.Unmarshal(s.preview.Result, &plan); err != nil {
			tt.Errorf("the Fixer's plan does not decode: %v", err)
			return
		}
		raw, err := json.Marshal(ops.FixerApply{Rows: plan.Rows, Params: s.params})
		if err != nil {
			tt.Errorf("the Fixer's apply params do not encode: %v", err)
			return
		}
		liveParams = raw
	}
	w.Reset()
	s.clean = w.Run(s.def, liveParams, s.live()...)
	s.cleanState = w.State()
	if s.clean.Err != nil || s.clean.State != state.V3Succeeded {
		tt.Errorf("the clean live run did not succeed (%s): %v", s.clean.State, s.clean.Err)
		return
	}
	s.second = w.Run(s.def, liveParams, s.live()...)
	n := len(s.clean.Items)
	mid := n / 2
	for _, f := range []RunOpt{CancelAt(mid), AbandonAt(mid), LoseLeaseAt(mid)} {
		w.Reset()
		s.fence = append(s.fence, w.Run(s.def, liveParams, s.live(f)...))
	}
	for _, p := range uniq(0, mid, max(n-1, 0)) {
		w.Reset()
		pre := w.Run(s.def, liveParams, s.live(CrashAt(p))...)
		pair := crashPair{label: fmt.Sprintf("CrashAt(%d)", p), chunk: -1, pre: pre}
		if pre.Crashed {
			pair.post = pre.Resume()
			pair.state = w.State()
		}
		s.crashAt = append(s.crashAt, pair)
	}
	// A graceful stop (a server restart) between the crashes: the run stops
	// for resume, skipping Finish, and the next boot resumes it.
	w.Reset()
	if pre := w.Run(s.def, liveParams, s.live(ShutdownAt(mid))...); true {
		pair := crashPair{label: fmt.Sprintf("ShutdownAt(%d)", mid), chunk: -1, pre: pre}
		if pre.Crashed {
			pair.post = pre.Resume()
			pair.state = w.State()
		}
		s.crashAt = append(s.crashAt, pair)
	}
	for _, cand := range midChunkCandidates(s.clean) {
		w.Reset()
		pre := w.Run(s.def, liveParams, s.live(CrashMidChunk(cand[0], cand[1]))...)
		pair := crashPair{label: fmt.Sprintf("CrashMidChunk(%d, %d)", cand[0], cand[1]), chunk: cand[0], pre: pre}
		if pre.Crashed {
			pair.post = pre.Resume()
			pair.state = w.State()
		}
		s.crashMid = append(s.crashMid, pair)
	}
	if len(s.crashMid) == 0 {
		s.midNote = fmt.Sprintf("CrashMidChunk not applicable at ChunkSize %d: no chunk holds 2 or more items", s.size)
	}
	if s.clean.Order == ops.AppendOnly {
		w.Reset()
		pre := w.Run(s.def, liveParams, s.live(InsertBelowCursor(s.cfg.below))...)
		s.appendRun = append(s.appendRun, pre)
		if pre.Crashed {
			s.appendRun = append(s.appendRun, pre.Resume())
		}
		s.appendRun = append(s.appendRun, w.Run(s.def, liveParams, s.live()...))
	}

	tt.Run(AssertPreviewWritesNothing, func(tt tester) { checkPreview(tt, s.preview) })
	tt.Run(AssertHistoryFollowsWrite, func(tt tester) {
		for _, o := range s.liveOutcomes() {
			checkHistory(tt, o)
		}
	})
	tt.Run(AssertNoWriteAfterFence, func(tt tester) { s.checkFence(tt) })
	tt.Run(AssertCrashResumeAtLeastOnce, func(tt tester) {
		for _, p := range s.crashAt {
			s.checkCrash(tt, p, false)
		}
	})
	tt.Run(AssertCrashMidChunkNoRerun, func(tt tester) {
		if s.midNote != "" {
			tt.Logf("%s", s.midNote)
		}
		for _, p := range s.crashMid {
			s.checkCrash(tt, p, true)
		}
	})
	tt.Run(AssertPartitionOneWorkerInOrder, func(tt tester) { s.checkPartitions(tt) })
	tt.Run(AssertAppendOnlyClaim, func(tt tester) { s.checkAppendOnly(tt) })
	tt.Run(AssertProgressMonotone, func(tt tester) {
		checkMonotone(tt, "the clean run", s.clean.Progress)
		if p, ok := s.resumePair(); ok {
			if p.post.ResumePolicy == ops.ResumeContinue {
				checkMonotone(tt, p.label+" + Resume", append(slices.Clone(p.pre.Progress), p.post.Progress...))
			} else {
				tt.Logf("%s: the job resumes from zero, so its progress restarts by design; the resume alone "+
					"is checked", p.label)
				checkMonotone(tt, p.label+"'s Resume", p.post.Progress)
			}
		}
	})
	tt.Run(AssertIdempotentItem, func(tt tester) {
		checkOK(tt, "the second live run", s.second)
		checkUnchanged(tt, s.second)
		for _, set := range [][]crashPair{s.crashAt, s.crashMid} {
			for _, p := range set {
				if p.post != nil && p.state != s.cleanState {
					tt.Errorf("%s + Resume left a different World than a clean run: an item that ran twice did "+
						"not leave the effect of running once\n%s", p.label, stateDiff(s.cleanState, p.state))
				}
			}
		}
	})
}

// resumePair is the crash + resume progress_monotone uses: the middle
// CrashMidChunk, or the middle CrashAt when no chunk can crash mid-way.
func (s *suite) resumePair() (crashPair, bool) {
	for _, set := range [][]crashPair{s.crashMid, s.crashAt} {
		var ok []crashPair
		for _, p := range set {
			if p.post != nil {
				ok = append(ok, p)
			}
		}
		if len(ok) > 0 {
			return ok[len(ok)/2], true
		}
	}
	return crashPair{}, false
}

// liveOutcomes are the attempts history_follows_write reads.
func (s *suite) liveOutcomes() []*Outcome {
	out := []*Outcome{s.clean, s.second}
	for _, set := range [][]crashPair{s.crashAt, s.crashMid} {
		for _, p := range set {
			out = append(out, p.pre)
			if p.post != nil {
				out = append(out, p.post)
			}
		}
	}
	return out
}

func uniq(xs ...int) []int {
	var out []int
	for _, x := range xs {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// midChunkCandidates picks the first, a middle and the last chunk that hold
// two or more items, each with the item in its middle (never its last).
func midChunkCandidates(clean *Outcome) [][2]int {
	sizes := map[int]int{}
	for _, c := range clean.Chunks {
		sizes[c.Chunk] = len(c.Keys)
	}
	var eligible []int
	for _, c := range slices.Sorted(maps.Keys(sizes)) {
		if sizes[c] >= 2 {
			eligible = append(eligible, c)
		}
	}
	if len(eligible) == 0 {
		return nil
	}
	var out [][2]int
	for _, c := range uniq(eligible[0], eligible[len(eligible)/2], eligible[len(eligible)-1]) {
		out = append(out, [2]int{c, (sizes[c] - 1) / 2})
	}
	return out
}

// ── assertions ──────────────────────────────────────────────────────────────

func checkOK(tt tester, what string, o *Outcome) {
	tt.Helper()
	if o.Err != nil || o.State != state.V3Succeeded {
		tt.Errorf("%s did not succeed (%s): %v", what, o.State, o.Err)
	}
}

func checkPreview(tt tester, o *Outcome) {
	tt.Helper()
	if o.Err != nil {
		tt.Errorf("the preview run failed: %v", o.Err)
	}
	for _, wr := range o.Writes {
		tt.Errorf("preview committed a write: %s %s:%s (seq %d)", wr.Op, wr.Subject.Type, wr.Subject.ID, wr.Seq)
	}
	for _, h := range o.History {
		tt.Errorf("preview recorded a history row: book %s field %s (seq %d)", h.BookID, h.Field, h.Seq)
	}
	for _, k := range o.DirtyCleared {
		tt.Errorf("preview deleted the DirtySet entry %s: the live run that follows would find it gone", k)
	}
}

// checkHistory: every history row follows, in commit order, the write it
// records. A row made inside a Writer call on its book (ModifyBook's own
// history, an Effect's rows or a journal entry its fn makes) pairs with that
// call: a write of the book must commit after the call starts and before the
// row, so a write made earlier in the item (another field of the same book)
// does not count. A row made outside every Writer call falls back to a write
// of its book anywhere earlier in the attempt.
func checkHistory(tt tester, o *Outcome) {
	tt.Helper()
	for _, h := range o.History {
		from, call := int64(0), ""
		for _, sp := range o.spans {
			if sp.Book == h.BookID && sp.Start < h.Seq && h.Seq < sp.End && sp.Start >= from {
				from, call = sp.Start, sp.Call
			}
		}
		ok := false
		for _, wr := range o.Writes {
			if from < wr.Seq && wr.Seq < h.Seq && wr.BookID != "" && wr.BookID == h.BookID {
				ok = true
				break
			}
		}
		switch {
		case ok:
		case call != "":
			tt.Errorf("op %s attempt %d: history row (book %s, field %s, seq %d) was made inside %s (from seq "+
				"%d) before that call wrote its book: history was recorded ahead of the write", o.OpID, o.Attempt,
				h.BookID, h.Field, h.Seq, call, from)
		default:
			tt.Errorf("op %s attempt %d: history row (book %s, field %s, seq %d) has no write of its book "+
				"committed before it: history was recorded ahead of the write", o.OpID, o.Attempt, h.BookID, h.Field, h.Seq)
		}
	}
}

// checkUnchanged: a run over its own end state changes nothing.
func checkUnchanged(tt tester, o *Outcome) {
	tt.Helper()
	for _, wr := range o.Writes {
		if len(wr.Changed) > 0 {
			tt.Errorf("a second run over the first run's end state changed %s:%s (%s %v): the item is not "+
				"idempotent", wr.Subject.Type, wr.Subject.ID, wr.Op, wr.Changed)
		}
	}
}

func (s *suite) checkFence(tt tester) {
	tt.Helper()
	names := []string{"CancelAt", "AbandonAt", "LoseLeaseAt"}
	for i, o := range s.fence {
		name := names[i]
		if o.FenceRevokedAt == 0 {
			if len(o.Notes) > 0 {
				tt.Logf("%s: %s", name, strings.Join(o.Notes, "; "))
			} else {
				tt.Errorf("%s: the fault never fired (the fence was not revoked)", name)
			}
			continue
		}
		for _, wr := range o.Writes {
			if wr.Seq > o.FenceRevokedAt {
				tt.Errorf("%s: a write committed after the fence was revoked: %s %s:%s (seq %d > revoke %d)",
					name, wr.Op, wr.Subject.Type, wr.Subject.ID, wr.Seq, o.FenceRevokedAt)
			}
		}
		for _, h := range o.History {
			if h.Seq > o.FenceRevokedAt {
				tt.Errorf("%s: a history row was recorded after the fence was revoked: book %s field %s (seq %d > "+
					"revoke %d)", name, h.BookID, h.Field, h.Seq, o.FenceRevokedAt)
			}
		}
		for _, l := range o.LateWrites {
			tt.Errorf("%s: the Writer accepted a call that started after the fence was revoked: %s", name, l)
		}
		switch name {
		case "CancelAt":
			if o.State != state.V3Canceled {
				tt.Errorf("CancelAt: the run ended %s, not canceled", o.State)
			}
			if !o.Summary.Canceled {
				tt.Errorf("CancelAt: Finish did not see Canceled")
			}
			checkSummaryIsLedger(tt, o)
		case "AbandonAt":
			a := o.Abandon
			switch {
			case a == nil:
				tt.Errorf("AbandonAt: no second run was started")
			case !a.SecondQueued || a.SecondStartedWhileHeld:
				tt.Errorf("AbandonAt: the second run did not wait for the abandoned run's key (queued %v, "+
					"started while held %v)", a.SecondQueued, a.SecondStartedWhileHeld)
			case a.Second == nil || a.Second.StartSeq < o.EndSeq:
				tt.Errorf("AbandonAt: the second run started before the abandoned goroutine returned")
			default:
				checkOK(tt, "AbandonAt's second run", a.Second)
			}
			if o.State != state.V3Canceled {
				tt.Errorf("AbandonAt: the run ended %s, not canceled", o.State)
			}
		case "LoseLeaseAt":
			if o.State == state.V3Succeeded {
				tt.Errorf("LoseLeaseAt: the run succeeded after losing its stand-down lease")
			}
		}
	}
}

// checkSummaryIsLedger: the Summary Finish got is the ledger's folded counts.
func checkSummaryIsLedger(tt tester, o *Outcome) {
	tt.Helper()
	l := o.Ledger
	if l == nil {
		tt.Errorf("op %s: no ledger was stored", o.OpID)
		return
	}
	s := o.Summary
	if s.Done != l.Items.Done || s.Failed != l.Items.Failed || s.Skipped != l.Items.Skipped || s.ChunksDone != l.ChunksDone {
		tt.Errorf("op %s: Finish's Summary {done %d failed %d skipped %d chunks %d} is not the ledger's folded "+
			"counts {done %d failed %d skipped %d chunks %d}", o.OpID, s.Done, s.Failed, s.Skipped, s.ChunksDone,
			l.Items.Done, l.Items.Failed, l.Items.Skipped, l.ChunksDone)
	}
}

// recordedChunks is the set of chunks a Snapshot ledger records complete.
func recordedChunks(l *database.Ledger) map[int]bool {
	out := map[int]bool{}
	if l == nil {
		return out
	}
	for c := range l.ChunkCount {
		if b := c / 8; b < len(l.Done) && l.Done[b]&(1<<(c%8)) != 0 {
			out[c] = true
		}
	}
	return out
}

func inRange(rs []database.KeyRange, key string) bool {
	for _, r := range rs {
		if r.First <= key && key <= r.Last {
			return true
		}
	}
	return false
}

// checkCrash asserts one crash + resume. mid adds CrashMidChunk's stronger
// expectations about the crashed chunk.
func (s *suite) checkCrash(tt tester, p crashPair, mid bool) {
	tt.Helper()
	pre, post := p.pre, p.post
	if !pre.Crashed {
		if len(pre.Notes) > 0 {
			tt.Logf("%s: %s", p.label, strings.Join(pre.Notes, "; "))
		} else {
			tt.Errorf("%s: the fault never fired", p.label)
		}
		return
	}
	checkOK(tt, p.label+" + Resume", post)
	runs := map[string]int{}
	committed := map[string]bool{}
	for _, o := range []*Outcome{pre, post} {
		for _, it := range o.Items {
			runs[it.Key]++
			if it.Committed {
				committed[it.Key] = true
			}
		}
	}
	if pre.UncommittedAtCrash > 0 {
		tt.Logf("%s: the crash lost %d folded but uncommitted chunk completion(s)", p.label, pre.UncommittedAtCrash)
	}
	for _, it := range s.clean.Items {
		if !committed[it.Key] {
			tt.Errorf("%s: item %s never completed with its completion committed across the crash and the "+
				"resume (it started %d time(s))", p.label, it.Key, runs[it.Key])
		}
	}
	continued := post.ResumePolicy == ops.ResumeContinue
	if !continued {
		tt.Logf("%s: the job resumes from zero (%s source): every item whose completion was not durable runs "+
			"again by design", p.label, post.Order)
	}
	recorded := recordedChunks(pre.Ledger)
	if continued {
		for _, c := range post.Chunks {
			if post.Order == ops.Snapshot && recorded[c.Chunk] {
				tt.Errorf("%s: the resume leased chunk %d, which the ledger recorded complete before the crash",
					p.label, c.Chunk)
			}
		}
		if post.Order == ops.AppendOnly && pre.Ledger != nil {
			for _, it := range post.Items {
				if inRange(pre.Ledger.Ranges, it.Key) {
					tt.Errorf("%s: the resume ran %s, inside a key range the ledger recorded complete", p.label, it.Key)
				}
			}
		}
	}
	if mid && continued {
		var crashed []string
		for _, c := range pre.Chunks {
			if c.Chunk == p.chunk {
				crashed = c.Keys
			}
		}
		inChunk := map[string]bool{}
		for _, k := range crashed {
			inChunk[k] = true
		}
		var rerun []string
		for _, it := range post.Items {
			rerun = append(rerun, it.Key)
		}
		if len(crashed) > 0 && (len(post.Items) == 0 || post.Items[0].Key != crashed[0]) && post.Order == ops.Snapshot {
			tt.Errorf("%s: the resume did not re-run chunk %d from its first item %s", p.label, p.chunk, crashed[0])
		}
		if slices.Sort(rerun); !slices.Equal(rerun, sortedCopy(crashed)) {
			tt.Errorf("%s: the resume ran %v; with every other chunk recorded it must run exactly chunk %d %v",
				p.label, rerun, p.chunk, crashed)
		}
		for k, n := range runs {
			switch {
			case inChunk[k] && n > 2:
				tt.Errorf("%s: item %s of the crashed chunk ran %d times (at most 2)", p.label, k, n)
			case !inChunk[k] && n != 1:
				tt.Errorf("%s: item %s outside the crashed chunk ran %d times (exactly 1)", p.label, k, n)
			}
		}
		if len(post.Progress) > 0 && pre.Ledger != nil && post.Progress[0].Done != pre.Ledger.Items.Done {
			tt.Errorf("%s: the resume's progress starts at %d, not at the ledger's folded %d", p.label,
				post.Progress[0].Done, pre.Ledger.Items.Done)
		}
	}
	// Done counts each settled item once, whatever its outcome; an item run
	// again after the crash may settle differently (an idempotent re-run
	// often skips), so only Done is compared.
	if got, want := post.Summary.Done, s.clean.Summary.Done; continued && got != want {
		tt.Errorf("%s: the resumed attempt's Finish got Done %d, a clean run's %d: it must see every attempt's "+
			"ledger, each item once", p.label, got, want)
	}
	checkEnd(tt, p.label+" + Resume", post.Progress)
}

func sortedCopy(xs []string) []string {
	out := slices.Clone(xs)
	slices.Sort(out)
	return out
}

// checkEnd: the last snapshot has Done == Total (when known) and every chunk
// done.
func checkEnd(tt tester, what string, snaps []ops.ProgressSnapshot) {
	tt.Helper()
	if len(snaps) == 0 {
		tt.Errorf("%s: no progress was published", what)
		return
	}
	last := snaps[len(snaps)-1]
	if last.Total != nil && last.Done != *last.Total {
		tt.Errorf("%s: progress ends at Done %d, Total %d", what, last.Done, *last.Total)
	}
	if last.Chunks != nil && last.Chunks.Done != last.Chunks.Total {
		tt.Errorf("%s: progress ends at chunks %d/%d", what, last.Chunks.Done, last.Chunks.Total)
	}
}

// checkMonotone: Done, Failed and every counter never decrease; the end is
// complete.
func checkMonotone(tt tester, what string, snaps []ops.ProgressSnapshot) {
	tt.Helper()
	for i := 1; i < len(snaps); i++ {
		a, b := snaps[i-1], snaps[i]
		if b.Done < a.Done {
			tt.Errorf("%s: progress Done went from %d to %d (snapshot %d)", what, a.Done, b.Done, i)
		}
		if b.Failed < a.Failed {
			tt.Errorf("%s: progress Failed went from %d to %d (snapshot %d)", what, a.Failed, b.Failed, i)
		}
		for k, v := range a.Counters {
			if b.Counters[k] < v {
				tt.Errorf("%s: counter %s went from %d to %d (snapshot %d)", what, k, v, b.Counters[k], i)
			}
		}
	}
	checkEnd(tt, what, snaps)
}

func (s *suite) checkPartitions(tt tester) {
	tt.Helper()
	if !s.clean.Partitioned {
		tt.Logf("not applicable: the def does not partition")
		return
	}
	pos := map[string]int{}
	for i, k := range s.clean.SourceKeys {
		pos[k] = i
	}
	outs := []*Outcome{s.clean}
	for _, p := range s.crashMid {
		if p.post != nil {
			outs = append(outs, p.post)
		}
	}
	for _, o := range outs {
		byPart := map[string][]ItemEvent{}
		for _, it := range o.Items {
			byPart[it.Partition] = append(byPart[it.Partition], it)
		}
		for _, part := range slices.Sorted(maps.Keys(byPart)) {
			evs := byPart[part]
			for _, e := range evs[1:] {
				if e.Chunk != evs[0].Chunk {
					tt.Errorf("op %s: partition %q straddles chunks %d and %d", o.OpID, part, evs[0].Chunk, e.Chunk)
					break
				}
			}
			for _, e := range evs[1:] {
				if e.Worker != evs[0].Worker {
					tt.Errorf("op %s: partition %q ran on workers %d and %d", o.OpID, part, evs[0].Worker, e.Worker)
					break
				}
			}
			if len(pos) == 0 {
				continue
			}
			for i := 1; i < len(evs); i++ {
				if pos[evs[i].Key] < pos[evs[i-1].Key] {
					tt.Errorf("op %s: partition %q ran %s before %s, against source order", o.OpID, part,
						evs[i-1].Key, evs[i].Key)
				}
			}
		}
	}
}

func (s *suite) checkAppendOnly(tt tester) {
	tt.Helper()
	if s.clean.Order != ops.AppendOnly {
		tt.Logf("not tested: the source claims %s, not append_only", s.clean.Order)
		return
	}
	faulted := s.appendRun[:len(s.appendRun)-1]
	fresh := s.appendRun[len(s.appendRun)-1]
	if faulted[0].Inserted == "" {
		tt.Logf("not tested: %s", strings.Join(faulted[0].Notes, "; "))
		return
	}
	seen := map[string]bool{}
	for _, it := range s.clean.Items {
		seen[it.Key] = true
	}
	var added []string
	for _, it := range fresh.Items {
		if !seen[it.Key] {
			added = append(added, it.Key)
		}
	}
	if len(added) == 0 {
		tt.Logf("not tested: the source never returned the inserted item %s, even to a fresh run", faulted[0].Inserted)
		return
	}
	processed := map[string]bool{}
	for _, o := range faulted {
		for _, it := range o.Items {
			processed[it.Key] = true
		}
	}
	for _, k := range added {
		if !processed[k] {
			tt.Errorf("AppendOnly claim violated: item inserted below the cursor was never processed (key %s; a "+
				"fresh run of the same source returns it)", k)
		} else {
			tt.Logf("item %s, inserted mid-run, was processed under key %s", faulted[0].Inserted, k)
		}
	}
}

// stateDiff is the first lines where two World dumps differ.
func stateDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	var out bytes.Buffer
	shown := 0
	for i := 0; i < max(len(al), len(bl)) && shown < 6; i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			fmt.Fprintf(&out, "  clean:   %s\n  resumed: %s\n", x, y)
			shown++
		}
	}
	return out.String()
}
