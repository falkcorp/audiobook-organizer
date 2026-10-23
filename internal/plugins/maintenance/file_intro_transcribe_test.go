// file: internal/plugins/maintenance/file_intro_transcribe_test.go
// version: 1.0.4
// guid: 9b4e1c73-d2a6-4f58-8e10-b7c35a9d2e64
// last-edited: 2026-10-23

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// ---- selection (pure) -------------------------------------------------------

func core(id, book, path, hash string, track int) database.BookFileCore {
	return database.BookFileCore{ID: id, BookID: book, FilePath: path, FileHash: hash, TrackNumber: track}
}

func TestSelectFileIntroWork_OrderingGroupingAndSkips(t *testing.T) {
	doneAt := time.Now()
	okStatus := statusOK
	cores := []database.BookFileCore{
		core("b1f2", "b1", "/lib/b1/02.m4b", "h-b1-2", 2),
		core("b1f1", "b1", "/lib/b1/01.m4b", "h-b1-1", 1),
		core("b2f1", "b2", "/lib/b2/01.m4b", "h-shared", 1),
		core("b3f2", "b3", "/lib/b3/02.m4b", "h-shared", 2), // same bytes as b2's first file
		core("b3f1", "b3", "/lib/b3/01.m4b", "h-b3-1", 1),
		core("nohash", "b4", "/lib/b4/01.m4b", "", 1),
		core("cover", "b4", "/lib/b4/cover.jpg", "h-cover", 2),
	}
	missing := core("gone", "b5", "/lib/b5/01.m4b", "h-gone", 1)
	missing.Missing = true
	cores = append(cores, missing)
	done := core("done", "b6", "/lib/b6/01.m4b", "h-done", 1)
	done.IntroSourceHash = "h-done"
	cores = append(cores, done)
	migrated := core("mig", "b7", "/lib/b7/01.m4b", "h-mig", 1)
	migrated.IntroTranscribedAt, migrated.TranscribeStatus = &doneAt, &okStatus
	cores = append(cores, migrated)

	sel := selectFileIntroWork(cores, fileIntroSelectOpts{onlyMissing: true, stat: func(string) bool { return true }})
	if sel.skippedNoHash != 1 || sel.skippedMissing != 1 || sel.skippedUnsupported != 1 {
		t.Fatalf("skips: nohash=%d missing=%d unsupported=%d", sel.skippedNoHash, sel.skippedMissing, sel.skippedUnsupported)
	}
	// eligible: b1x2, b2, b3x2, b6, b7 = 7 ; done at start: b6 and the migrated b7.
	if sel.eligible != 7 || sel.transcribedAtStart != 2 || sel.selected != 5 {
		t.Fatalf("eligible=%d atStart=%d selected=%d", sel.eligible, sel.transcribedAtStart, sel.selected)
	}
	// 5 selected files, one pair shares bytes -> 4 groups.
	if len(sel.groups) != 4 {
		t.Fatalf("groups=%d", len(sel.groups))
	}
	// Every group holding an ordinal-0 file precedes every group that does not.
	seenOther := false
	for _, g := range sel.groups {
		if g.tier() == 1 {
			seenOther = true
		} else if seenOther {
			t.Fatalf("an ordinal-0 group came after a later-track group: %+v", sel.groups)
		}
	}
	last := sel.groups[len(sel.groups)-1]
	if last.Hash != "h-b1-2" {
		t.Fatalf("the only track-2-only group should be last, got %q", last.Hash)
	}
	for _, g := range sel.groups {
		if g.Hash == "h-shared" {
			if len(g.Members) != 2 || g.Members[0].FileID != "b2f1" || g.Members[0].Ordinal != 0 || g.Members[1].Ordinal != 1 {
				t.Fatalf("shared group members: %+v", g.Members)
			}
		}
	}
}

func TestSelectFileIntroWork_ForcedIncludesDoneAndDonorOnlyWhenOnlyMissing(t *testing.T) {
	donor := core("d", "b1", "/lib/b1/01.m4b", "h", 1)
	donor.IntroSourceHash = "h"
	twin := core("t", "b2", "/lib/b2/01.m4b", "h", 1)
	cores := []database.BookFileCore{donor, twin}

	sel := selectFileIntroWork(cores, fileIntroSelectOpts{onlyMissing: true})
	if len(sel.groups) != 1 || sel.groups[0].DonorFileID != "d" || sel.reuseFiles != 1 {
		t.Fatalf("only_missing: want the twin copied from the donor, got %+v reuse=%d", sel.groups, sel.reuseFiles)
	}
	forced := selectFileIntroWork(cores, fileIntroSelectOpts{onlyMissing: false})
	if forced.selected != 2 || forced.reuseFiles != 0 || forced.groups[0].DonorFileID != "" {
		t.Fatalf("forced: want both files sent fresh, got selected=%d reuse=%d %+v", forced.selected, forced.reuseFiles, forced.groups)
	}
}

func TestSelectFileIntroWork_DurableFailures(t *testing.T) {
	mk := func(id, status string) database.BookFileCore {
		c := core(id, "b-"+id, "/lib/"+id+".m4b", "h-"+id, 1)
		c.TranscribeStatus = &status
		return c
	}
	cores := []database.BookFileCore{mk("a", statusFFmpegError), mk("b", statusNoAudio), mk("c", statusSourceMissing), mk("d", statusWhisperError)}
	// source file for c is still gone.
	sel := selectFileIntroWork(cores, fileIntroSelectOpts{onlyMissing: true, stat: func(string) bool { return false }})
	if sel.skippedFailed != 3 || sel.selected != 1 {
		t.Fatalf("still-missing source: skippedFailed=%d selected=%d", sel.skippedFailed, sel.selected)
	}
	// c's file is back: it self-heals; the other durable ones stay held.
	sel = selectFileIntroWork(cores, fileIntroSelectOpts{onlyMissing: true, stat: func(string) bool { return true }})
	if sel.skippedFailed != 2 || sel.selected != 2 {
		t.Fatalf("source back: skippedFailed=%d selected=%d", sel.skippedFailed, sel.selected)
	}
	sel = selectFileIntroWork(cores, fileIntroSelectOpts{onlyMissing: true, retryFailed: true, stat: func(string) bool { return false }})
	if sel.skippedFailed != 0 || sel.selected != 4 {
		t.Fatalf("retry_failed: skippedFailed=%d selected=%d", sel.skippedFailed, sel.selected)
	}
}

// ---- run fixture ------------------------------------------------------------

type introFx struct {
	t     *testing.T
	s     *database.PebbleStore
	p     *Plugin
	dir   string
	whisp *stubWhisper
}

// stubWhisper stands in for the worker: it answers by path and records every
// call, including the highest number of calls in flight at once.
type stubWhisper struct {
	mu          sync.Mutex
	texts       map[string]string // path -> transcript ("" = silence)
	errs        map[string]string // path -> per-file error
	transErr    error             // returned for the whole call
	transErrFor int               // when > 0, transErr is returned for this many calls, then cleared
	dropKeys    bool              // answer with an empty map (a transport that dropped every job)
	calls       [][]fileIntroJob
	inFlight    atomic.Int32
	maxSeen     atomic.Int32
	delay       time.Duration
}

func (w *stubWhisper) fn(ctx context.Context, jobs map[string]fileIntroJob, _ func(int, int)) (map[string]fileIntroWhisperResult, error) {
	cur := w.inFlight.Add(1)
	defer w.inFlight.Add(-1)
	for {
		m := w.maxSeen.Load()
		if cur <= m || w.maxSeen.CompareAndSwap(m, cur) {
			break
		}
	}
	if w.delay > 0 {
		select {
		case <-time.After(w.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	batch := make([]fileIntroJob, 0, len(jobs))
	for _, j := range jobs {
		batch = append(batch, j)
	}
	w.calls = append(w.calls, batch)
	if w.transErr != nil {
		err := w.transErr
		if w.transErrFor > 0 {
			if w.transErrFor--; w.transErrFor == 0 {
				w.transErr = nil
			}
		}
		return nil, err
	}
	if w.dropKeys {
		return map[string]fileIntroWhisperResult{}, nil
	}
	out := make(map[string]fileIntroWhisperResult, len(jobs))
	for k, j := range jobs {
		if e, ok := w.errs[j.Path]; ok {
			out[k] = fileIntroWhisperResult{Error: e}
			continue
		}
		out[k] = fileIntroWhisperResult{Text: w.texts[j.Path], Model: "model-x", Language: "en"}
	}
	return out, nil
}

func (w *stubWhisper) sentPaths() map[string]int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := map[string]int{}
	for _, b := range w.calls {
		for _, j := range b {
			n[j.Path]++
			if j.Seconds != fileIntroClipSeconds {
				panic(fmt.Sprintf("clip length %d, want %d", j.Seconds, fileIntroClipSeconds))
			}
		}
	}
	return n
}

const goodIntro = "The Example Book by Example Author. Narrated by Example Reader."

func newIntroFx(t *testing.T) *introFx {
	t.Helper()
	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	fx := &introFx{t: t, s: s, p: New(fakeDeps{store: s}), dir: t.TempDir(), whisp: &stubWhisper{texts: map[string]string{}, errs: map[string]string{}}}

	origFn, origSlots, origPause, origPre, origOutage := fileIntroWhisperFn, fileIntroSlots, fileIntroPauseInterval, fileIntroPreflight, fileIntroOutageInterval
	fileIntroWhisperFn = fx.whisp.fn
	fileIntroOutageInterval = time.Millisecond
	fileIntroPreflight = func(context.Context) error { return nil }
	fileIntroSlots = func() int { return 3 }
	fileIntroPauseInterval = 5 * time.Millisecond
	t.Cleanup(func() {
		fileIntroWhisperFn, fileIntroSlots, fileIntroPauseInterval, fileIntroPreflight, fileIntroOutageInterval = origFn, origSlots, origPause, origPre, origOutage
	})
	return fx
}

func (fx *introFx) book(title string) string {
	fx.t.Helper()
	b, err := fx.s.CreateBook(&database.Book{Title: title})
	if err != nil {
		fx.t.Fatal(err)
	}
	return b.ID
}

// file creates a book_file whose path exists on disk (unless absent) and, when
// text is non-nil, scripts the worker's answer for it.
func (fx *introFx) file(bookID, name string, track int, hash string, absent bool) (id, path string) {
	fx.t.Helper()
	path = filepath.Join(fx.dir, bookID, name)
	if !absent {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fx.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			fx.t.Fatal(err)
		}
	}
	f := &database.BookFile{BookID: bookID, FilePath: path, Format: "m4b", Duration: 600, TrackNumber: track, FileHash: hash}
	if err := fx.s.CreateBookFile(f); err != nil {
		fx.t.Fatal(err)
	}
	rows, err := fx.s.GetBookFiles(bookID)
	if err != nil {
		fx.t.Fatal(err)
	}
	for _, r := range rows {
		if r.FilePath == path {
			return r.ID, path
		}
	}
	fx.t.Fatal("file not found after create")
	return "", ""
}

type fileIntroRep struct {
	lastProgressReporter
	id string
}

func (r *fileIntroRep) OpID() string { return r.id }

func (fx *introFx) run(params string) (*fileIntroRep, error) {
	fx.t.Helper()
	rep := &fileIntroRep{id: "run-test-1"}
	return fx.runAs(rep, params)
}

func (fx *introFx) runAs(rep *fileIntroRep, params string) (*fileIntroRep, error) {
	fx.t.Helper()
	err := fx.p.runFileIntroTranscribe(context.Background(), liveTranscribeParams(params), rep)
	return rep, err
}

func (fx *introFx) row(bookID, fileID string) *database.BookFile {
	fx.t.Helper()
	r, err := fx.s.GetBookFileByID(bookID, fileID)
	if err != nil || r == nil {
		fx.t.Fatalf("GetBookFileByID: %v %v", r, err)
	}
	return r
}

func (fx *introFx) bookRow(id string) *database.Book {
	fx.t.Helper()
	b, err := fx.s.GetBookByID(id)
	if err != nil || b == nil {
		fx.t.Fatalf("GetBookByID: %v %v", b, err)
	}
	return b
}

func sp(s string) *string { return &s }

// ---- run behaviour ----------------------------------------------------------

func TestFileIntroRun_PreviewIsTheDefaultAndWritesNothing(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("Preview")
	_, path := fx.file(b, "01.m4b", 1, "h1", false)
	fx.whisp.texts[path] = goodIntro

	rep := &fileIntroRep{id: "run-p"}
	if err := fx.p.runFileIntroTranscribe(context.Background(), json.RawMessage(`{}`), rep); err != nil {
		t.Fatal(err)
	}
	msg := rep.lastMessage()
	if !strings.HasPrefix(msg, introPreviewMarker) || !strings.Contains(msg, "UNMEASURED") || !strings.Contains(msg, "would handle 1 files") {
		t.Fatalf("preview message: %q", msg)
	}
	if len(fx.whisp.calls) != 0 {
		t.Fatal("a preview called whisper")
	}
	if st, _ := fx.s.GetFileIntroStats(); st != nil {
		t.Fatalf("a preview wrote the stats key: %+v", st)
	}
	if fx.bookRow(b).IntroTranscription != nil {
		t.Fatal("a preview wrote a book")
	}
}

func TestFileIntroRun_FillsFilesAndOnlyEmptyBookTranscripts(t *testing.T) {
	fx := newIntroFx(t)

	// Book A: empty transcript, two files. Book B: already has a transcript.
	a := fx.book("Book A")
	a1, a1p := fx.file(a, "01.m4b", 1, "ha1", false)
	a2, a2p := fx.file(a, "02.m4b", 2, "ha2", false)
	b := fx.book("Book B")
	b1, b1p := fx.file(b, "01.m4b", 1, "hb1", false)
	if _, err := fx.s.ModifyBook(b, func(cur *database.Book) error { cur.IntroTranscription = sp("curated words"); return nil }); err != nil {
		t.Fatal(err)
	}
	fx.whisp.texts[a1p], fx.whisp.texts[a2p], fx.whisp.texts[b1p] = goodIntro, "second track words", "b words"

	rep, err := fx.run("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rep.lastMessage(), introLiveMarker) {
		t.Fatalf("message: %q", rep.lastMessage())
	}
	for _, id := range []struct{ book, file string }{{a, a1}, {a, a2}, {b, b1}} {
		r := fx.row(id.book, id.file)
		if r.IntroTranscription == nil || r.IntroSourceHash != r.FileHash || r.IntroRunOpID != "run-test-1" {
			t.Fatalf("file %s not done: %+v", id.file, r)
		}
		if r.IntroMeta == nil || r.IntroMeta.Model != "model-x" || r.IntroMeta.ClipLengthSec != 90 {
			t.Fatalf("file %s meta: %+v", id.file, r.IntroMeta)
		}
	}
	// Book A is filled from its FIRST file only.
	ba := fx.bookRow(a)
	if ba.IntroTranscription == nil || *ba.IntroTranscription != goodIntro {
		t.Fatalf("book A transcript: %v", ba.IntroTranscription)
	}
	if ba.IntroTranscribedAt == nil || !ba.IntroTranscribedAt.Equal(*fx.row(a, a1).IntroTranscribedAt) {
		t.Fatal("book fill and file result should carry the same timestamp")
	}
	// Book B's existing text is never overwritten.
	if got := fx.bookRow(b).IntroTranscription; got == nil || *got != "curated words" {
		t.Fatalf("book B transcript was overwritten: %v", got)
	}
	st, _ := fx.s.GetFileIntroStats()
	if st == nil || !st.Done || st.RunOpID != "run-test-1" || st.Eligible != 3 || st.TranscribedNow != 3 || st.BookFills != 1 {
		t.Fatalf("stats: %+v", st)
	}
	if st.OK+st.Unparsed != 3 || st.Attempted != 3 {
		t.Fatalf("outcome counts: %+v", st)
	}
}

func TestFileIntroRun_ResumesFromTheMarkerAndNeverResendsDoneFiles(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("Resume")
	_, p1 := fx.file(b, "01.m4b", 1, "r1", false)
	_, p2 := fx.file(b, "02.m4b", 2, "r2", false)
	fx.whisp.texts[p1], fx.whisp.texts[p2] = goodIntro, "words"

	// An outage that outlasts every retry stops the run (it does not drain the
	// library as deferred) and writes nothing for the batch.
	fx.whisp.transErr = &transcribe.TransportError{Endpoints: []string{"http://192.0.2.10:8000"}, Err: errors.New("connection refused")}
	_, err := fx.run("")
	if !errors.Is(err, errFileIntroOutage) {
		t.Fatalf("a dead pool should fail the run with errFileIntroOutage, got %v", err)
	}
	if n := len(fx.whisp.calls); n != fileIntroOutageMaxTries {
		t.Fatalf("one batch should be tried %d times, was %d", fileIntroOutageMaxTries, n)
	}
	st, _ := fx.s.GetFileIntroStats()
	if st.Attempted != 0 {
		t.Fatalf("an outage must not count as an attempt: %+v", st)
	}
	for _, r := range mustFiles(t, fx.s, b) {
		if r.IntroSourceHash != "" || r.TranscribeStatus != nil || r.IntroTranscription != nil {
			t.Fatalf("an outage wrote to %s: %+v", r.ID, r)
		}
	}

	fx.whisp.transErr = nil
	fx.whisp.calls = nil
	if _, err := fx.run(""); err != nil {
		t.Fatal(err)
	}
	if got := fx.whisp.sentPaths(); got[p1] != 1 || got[p2] != 1 {
		t.Fatalf("second run sent %v", got)
	}

	// A third run finds nothing left.
	fx.whisp.calls = nil
	rep, err := fx.run("")
	if err != nil {
		t.Fatal(err)
	}
	if len(fx.whisp.calls) != 0 || !strings.Contains(rep.lastMessage(), "nothing to transcribe") {
		t.Fatalf("third run: calls=%d msg=%q", len(fx.whisp.calls), rep.lastMessage())
	}
}

func mustFiles(t *testing.T, s *database.PebbleStore, bookID string) []database.BookFile {
	t.Helper()
	rows, err := s.GetBookFiles(bookID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestFileIntroRun_IdenticalFilesAreTranscribedOnce(t *testing.T) {
	fx := newIntroFx(t)
	a, b := fx.book("Twin A"), fx.book("Twin B")
	a1, ap := fx.file(a, "01.m4b", 1, "same-bytes", false)
	b1, bp := fx.file(b, "01.m4b", 1, "same-bytes", false)
	fx.whisp.texts[ap], fx.whisp.texts[bp] = goodIntro, goodIntro

	if _, err := fx.run(""); err != nil {
		t.Fatal(err)
	}
	sent := fx.whisp.sentPaths()
	if sent[ap]+sent[bp] != 1 {
		t.Fatalf("identical files should be sent once, sent %v", sent)
	}
	for _, id := range []struct{ book, file string }{{a, a1}, {b, b1}} {
		if r := fx.row(id.book, id.file); r.IntroTranscription == nil || *r.IntroTranscription != goodIntro || r.IntroSourceHash != "same-bytes" {
			t.Fatalf("file %s: %+v", id.file, r)
		}
		if fx.bookRow(id.book).IntroTranscription == nil {
			t.Fatalf("book %s not filled", id.book)
		}
	}
}

func TestFileIntroRun_ReusesAnEarlierRunsResultAcrossRuns(t *testing.T) {
	fx := newIntroFx(t)
	a := fx.book("Donor")
	_, ap := fx.file(a, "01.m4b", 1, "dup", false)
	fx.whisp.texts[ap] = goodIntro
	if _, err := fx.run(""); err != nil {
		t.Fatal(err)
	}
	// A second copy of the same bytes appears later.
	b := fx.book("Copy")
	b1, bp := fx.file(b, "01.m4b", 1, "dup", false)
	fx.whisp.calls = nil
	if _, err := fx.runAs(&fileIntroRep{id: "run-test-2"}, ""); err != nil {
		t.Fatal(err)
	}
	if len(fx.whisp.calls) != 0 {
		t.Fatalf("the copy should have been filled from the donor, whisper saw %v", fx.whisp.sentPaths())
	}
	_ = bp
	r := fx.row(b, b1)
	if r.IntroTranscription == nil || *r.IntroTranscription != goodIntro || r.IntroRunOpID != "run-test-2" {
		t.Fatalf("copy: %+v", r)
	}
	if st, _ := fx.s.GetFileIntroStats(); st.Reused != 1 || st.Attempted != 0 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestFileIntroRun_FailuresSilenceAndMissingSource(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("Mixed")
	okID, okP := fx.file(b, "01.m4b", 1, "m1", false)
	errID, errP := fx.file(b, "02.m4b", 2, "m2", false)
	silID, silP := fx.file(b, "03.m4b", 3, "m3", false)
	gone, _ := fx.file(b, "04.m4b", 4, "m4", true)
	fx.whisp.texts[okP], fx.whisp.texts[silP] = goodIntro, ""
	fx.whisp.errs[errP] = "could not decode"

	if _, err := fx.run(""); err != nil {
		t.Fatal(err)
	}
	if r := fx.row(b, okID); r.IntroSourceHash == "" {
		t.Fatal("ok file has no marker")
	}
	r := fx.row(b, errID)
	if r.IntroSourceHash != "" || r.TranscribeStatus == nil || *r.TranscribeStatus != statusWhisperError || r.IntroTranscription != nil {
		t.Fatalf("whisper_error must be retryable (no marker, no transcript): %+v", r)
	}
	r = fx.row(b, silID)
	if r.IntroSourceHash != "m3" || r.IntroTranscription == nil || *r.IntroTranscription != transcribe.SilenceSentinel ||
		r.TranscribeStatus == nil || *r.TranscribeStatus != statusEmpty {
		t.Fatalf("silence should be a done file marker: %+v", r)
	}
	r = fx.row(b, gone)
	if r.TranscribeStatus == nil || *r.TranscribeStatus != statusSourceMissing || r.IntroSourceHash != "" {
		t.Fatalf("absent file: %+v", r)
	}

	// The book was filled from track 1 (a real transcript), never from silence.
	if got := fx.bookRow(b).IntroTranscription; got == nil || *got != goodIntro {
		t.Fatalf("book transcript: %v", got)
	}

	// A book whose FIRST file is silent must stay empty so the book op's longer
	// retries still apply.
	s := fx.book("Silent first")
	sID, sP := fx.file(s, "01.m4b", 1, "s1", false)
	fx.whisp.texts[sP] = ""
	if _, err := fx.runAs(&fileIntroRep{id: "run-test-2"}, ""); err != nil {
		t.Fatal(err)
	}
	if fx.row(s, sID).IntroTranscription == nil {
		t.Fatal("silent file should carry the sentinel")
	}
	if got := fx.bookRow(s).IntroTranscription; got != nil {
		t.Fatalf("the silence sentinel filled a book: %q", *got)
	}

	// Next run: the whisper_error file is retried, nothing else is.
	fx.whisp.calls = nil
	delete(fx.whisp.errs, errP)
	fx.whisp.texts[errP] = "now fine"
	if _, err := fx.runAs(&fileIntroRep{id: "run-test-3"}, ""); err != nil {
		t.Fatal(err)
	}
	sent := fx.whisp.sentPaths()
	if len(sent) != 1 || sent[errP] != 1 {
		t.Fatalf("only the whisper_error file should be retried, sent %v", sent)
	}
}

func TestFileIntroRun_PreflightFailureStopsTheRunBeforeAnyWork(t *testing.T) {
	fx := newIntroFx(t)
	fileIntroPreflight = func(context.Context) error {
		return errors.New("no whisper worker can take path jobs: http://192.0.2.10:8000: does not advertise path_pipeline")
	}
	b := fx.book("No path pipeline")
	id, _ := fx.file(b, "01.m4b", 1, "t1", false)
	if _, err := fx.run(""); err == nil || !strings.Contains(err.Error(), "path_pipeline") {
		t.Fatalf("err = %v", err)
	}
	if len(fx.whisp.calls) != 0 {
		t.Fatal("whisper was called after a failed preflight")
	}
	if r := fx.row(b, id); r.TranscribeStatus != nil {
		t.Fatalf("a failed preflight recorded a per-file failure: %+v", r)
	}
}

// The worker prefixes a per-file error with a code; the code decides whether the
// verdict is about the file (written, and how durably) or about the mount (not
// written).
func TestFileIntroRun_WorkerErrorCodesDecideWhatIsWritten(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("Codes")
	type c struct {
		name, errText, wantStatus string
		wantWritten               bool
	}
	cases := []c{
		{"not_found", "not_found: no such file", statusSourceMissing, true},
		{"ffmpeg_error", "ffmpeg_error: invalid data", statusFFmpegError, true},
		{"path_unmapped", "path_unmapped: root unreadable", "", false},
		{"ffmpeg_timeout", "ffmpeg_timeout: nfs stall", "", false},
		{"worker_misconfigured", "worker_misconfigured: no audio root", "", false},
		{"container_rejected", "container_rejected: unsupported container", "", false},
		{"whisper_error", "whisper_error: boom", statusWhisperError, true},
		{"uncoded", "something odd", statusWhisperError, true},
	}
	ids := map[string]string{}
	for i, tc := range cases {
		id, p := fx.file(b, fmt.Sprintf("%02d.m4b", i+1), i+1, "code-"+tc.name, false)
		ids[tc.name] = id
		fx.whisp.errs[p] = tc.errText
	}
	if _, err := fx.run(""); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		r := fx.row(b, ids[tc.name])
		if tc.wantWritten {
			if r.TranscribeStatus == nil || *r.TranscribeStatus != tc.wantStatus || r.IntroSourceHash != "" {
				t.Fatalf("%s: want status %q and no marker, got %+v", tc.name, tc.wantStatus, r)
			}
		} else if r.TranscribeStatus != nil {
			t.Fatalf("%s: an infrastructure error was written as a file verdict: %v", tc.name, *r.TranscribeStatus)
		}
	}
	st, _ := fx.s.GetFileIntroStats()
	if st.FFmpegError != 1 || st.SourceMissing != 1 || st.WhisperError != 2 || st.Deferred != 4 {
		t.Fatalf("stats: %+v", st)
	}
	// An ffmpeg_error file is a held verdict: the next run does not re-send it,
	// but retry_failed does.
	fx.whisp.calls = nil
	delete(fx.whisp.errs, filepath.Join(fx.dir, b, "02.m4b"))
	fx.whisp.texts[filepath.Join(fx.dir, b, "02.m4b")] = goodIntro
	if _, err := fx.runAs(&fileIntroRep{id: "run-test-2"}, ""); err != nil {
		t.Fatal(err)
	}
	if got := fx.whisp.sentPaths(); got[filepath.Join(fx.dir, b, "02.m4b")] != 0 {
		t.Fatalf("a held ffmpeg_error file was re-sent without retry_failed: %v", got)
	}
	fx.whisp.calls = nil
	if _, err := fx.runAs(&fileIntroRep{id: "run-test-3"}, `{"retry_failed":true}`); err != nil {
		t.Fatal(err)
	}
	if got := fx.whisp.sentPaths(); got[filepath.Join(fx.dir, b, "02.m4b")] != 1 {
		t.Fatalf("retry_failed should re-send the ffmpeg_error file: %v", got)
	}
}

func TestFileIntroRun_RefusesALiveRunWithNoEndpoint(t *testing.T) {
	fx := newIntroFx(t)
	fileIntroSlots = func() int { return 0 }
	b := fx.book("No endpoint")
	fx.file(b, "01.m4b", 1, "e1", false)
	if _, err := fx.run(""); err == nil || !strings.Contains(err.Error(), "no whisper endpoint") {
		t.Fatalf("err = %v", err)
	}
}

func TestFileIntroRun_BoundedConcurrencyAndRace(t *testing.T) {
	fx := newIntroFx(t)
	fx.whisp.delay = 3 * time.Millisecond
	fileIntroSlots = func() int { return 3 }
	const books = 40
	for i := 0; i < books; i++ {
		b := fx.book(fmt.Sprintf("Book %02d", i))
		_, p1 := fx.file(b, "01.m4b", 1, fmt.Sprintf("c%02d-1", i), false)
		_, p2 := fx.file(b, "02.m4b", 2, fmt.Sprintf("c%02d-2", i), false)
		fx.whisp.texts[p1], fx.whisp.texts[p2] = goodIntro, "words"
	}
	if _, err := fx.run(""); err != nil {
		t.Fatal(err)
	}
	if got := fx.whisp.maxSeen.Load(); got > 3 || got < 2 {
		t.Fatalf("max whisper calls in flight = %d, want 2..3 (slots=3)", got)
	}
	st, _ := fx.s.GetFileIntroStats()
	if st.Selected != books*2 || st.TranscribedNow != books*2 || st.BookFills != books {
		t.Fatalf("stats: %+v", st)
	}
	// Ordinal 0 first, library-wide: every first-track file was sent before any
	// second-track file.
	order := []string{}
	fx.whisp.mu.Lock()
	for _, c := range fx.whisp.calls {
		for _, j := range c {
			order = append(order, filepath.Base(j.Path))
		}
	}
	fx.whisp.mu.Unlock()
	sawSecond := false
	for _, n := range order {
		if n == "02.m4b" {
			sawSecond = true
		} else if sawSecond && n == "01.m4b" {
			// Batches complete concurrently, so allow only the batches already in
			// flight (slots) to straddle the boundary.
			continue
		}
	}
	firstSecond := -1
	for i, n := range order {
		if n == "02.m4b" {
			firstSecond = i
			break
		}
	}
	for i, n := range order {
		if n == "01.m4b" && firstSecond >= 0 && i > firstSecond+3*fileIntroBatchGroups {
			t.Fatalf("a first-track file was sent at position %d after track 2 started at %d", i, firstSecond)
		}
	}
}

// ---- pause ------------------------------------------------------------------

type fakeQueue struct {
	mu     sync.Mutex
	calls  int
	busyN  int // report a running scan for the first busyN calls
	failN  int // return an error for the first failN calls
	defID  string
	status string
}

func (q *fakeQueue) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls++
	if q.calls <= q.failN {
		return nil, errors.New("queue unavailable")
	}
	if q.calls <= q.failN+q.busyN {
		return []database.OperationV2Row{{ID: "op-1", DefID: q.defID, Status: q.status}}, nil
	}
	return nil, nil
}

func newWorkerForPause(q OpQueueReader) *fileIntroWorker {
	return &fileIntroWorker{queue: q, reporter: &lastProgressReporter{}}
}

func TestFileIntroPause_WaitsWhileScanOrBatchApplyRuns(t *testing.T) {
	orig := fileIntroPauseInterval
	fileIntroPauseInterval = time.Millisecond
	t.Cleanup(func() { fileIntroPauseInterval = orig })

	for _, def := range []string{"library.scan", "library.import", "library.folder-auto-scan", "metadata.batch-apply-cached"} {
		for _, status := range []string{"running", "queued"} {
			q := &fakeQueue{busyN: 4, defID: def, status: status}
			if err := newWorkerForPause(q).waitWhileBusy(context.Background()); err != nil {
				t.Fatalf("%s/%s: %v", def, status, err)
			}
			if q.calls != 5 {
				t.Fatalf("%s/%s: polled %d times, want 5 (4 busy then free)", def, status, q.calls)
			}
		}
	}
	// An unrelated op does not pause the run, and neither does itunes.import,
	// which edits rows under their lock instead of through the scanner's
	// unlocked upsert.
	for _, def := range []string{"maintenance.something-else", "itunes.import"} {
		q := &fakeQueue{busyN: 3, defID: def, status: "running"}
		if err := newWorkerForPause(q).waitWhileBusy(context.Background()); err != nil || q.calls != 1 {
			t.Fatalf("%s paused the run: err=%v calls=%d", def, err, q.calls)
		}
	}
}

func TestFileIntroPause_CancelsWhilePausedAndFailsClosedOnListErrors(t *testing.T) {
	orig := fileIntroPauseInterval
	fileIntroPauseInterval = time.Hour // only ctx.Done can end this wait
	t.Cleanup(func() { fileIntroPauseInterval = orig })

	q := &fakeQueue{busyN: 1 << 20, defID: "library.scan", status: "running"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- newWorkerForPause(q).waitWhileBusy(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a paused run did not honour ctx.Done")
	}

	fileIntroPauseInterval = time.Millisecond
	bad := &fakeQueue{failN: 1 << 20}
	if err := newWorkerForPause(bad).waitWhileBusy(context.Background()); err == nil || bad.calls != fileIntroPauseMaxListErrors {
		t.Fatalf("persistent list failure must stop the run after %d tries: err=%v calls=%d", fileIntroPauseMaxListErrors, err, bad.calls)
	}
	if err := newWorkerForPause(nil).waitWhileBusy(context.Background()); err == nil {
		t.Fatal("no queue must fail closed")
	}
	// A transient failure recovers.
	flaky := &fakeQueue{failN: 3}
	if err := newWorkerForPause(flaky).waitWhileBusy(context.Background()); err != nil {
		t.Fatalf("transient list failure: %v", err)
	}
}

// ---- scan gate on writes ----------------------------------------------------

// scanQueue reports a running library.scan while busy is set. onBusyPoll runs
// on every poll that reports it, so a test can assert what has (not) been
// written at that moment. After clearAfter busy polls the scan "finishes".
type scanQueue struct {
	mu         sync.Mutex
	busy       bool
	busyPolls  int
	clearAfter int
	onBusyPoll func()
}

func (q *scanQueue) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	q.mu.Lock()
	if !q.busy {
		q.mu.Unlock()
		return nil, nil
	}
	q.busyPolls++
	if q.clearAfter > 0 && q.busyPolls >= q.clearAfter {
		q.busy = false
	}
	hook := q.onBusyPoll
	q.mu.Unlock()
	if hook != nil {
		hook()
	}
	return []database.OperationV2Row{{ID: "scan-1", DefID: "library.scan", Status: "running"}}, nil
}

func (q *scanQueue) setBusy(v bool) {
	q.mu.Lock()
	q.busy = v
	q.mu.Unlock()
}

// setBusyOnce marks the scan running unless it already ran and finished.
func (q *scanQueue) setBusyOnce() {
	q.mu.Lock()
	if q.busyPolls == 0 {
		q.busy = true
	}
	q.mu.Unlock()
}

func (q *scanQueue) polls() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.busyPolls
}

// queueDeps is fakeDeps with the op queue replaced.
type queueDeps struct {
	fakeDeps
	q OpQueueReader
}

func (d queueDeps) OperationQueueStore() OpQueueReader { return d.q }

func TestFileIntro_ScanStartingMidTranscriptionHoldsTheWrites(t *testing.T) {
	fx := newIntroFx(t)
	q := &scanQueue{clearAfter: 3}
	fx.p = New(queueDeps{fakeDeps: fakeDeps{store: fx.s}, q: q})
	b := fx.book("Scan mid-run")
	f1, p1 := fx.file(b, "01.m4b", 1, "sm1", false)
	f2, p2 := fx.file(b, "02.m4b", 2, "sm2", false)
	fx.whisp.texts[p1], fx.whisp.texts[p2] = goodIntro, "second"

	// A scan starts while the batch is out at the whisper workers: the queue was
	// idle at the start gate and at the top of the batch.
	inner := fx.whisp.fn
	fileIntroWhisperFn = func(ctx context.Context, jobs map[string]fileIntroJob, cb func(int, int)) (map[string]fileIntroWhisperResult, error) {
		out, err := inner(ctx, jobs, cb)
		q.setBusy(true)
		return out, err
	}

	var mu sync.Mutex
	var early []string
	q.onBusyPoll = func() {
		for _, id := range []string{f1, f2} {
			if r := fx.row(b, id); r.IntroSourceHash != "" || r.IntroTranscription != nil {
				mu.Lock()
				early = append(early, id)
				mu.Unlock()
			}
		}
	}

	if _, err := fx.run(""); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(early) != 0 {
		t.Fatalf("results were written while library.scan was running: %v", early)
	}
	if q.polls() < 3 {
		t.Fatalf("the write gate polled the busy queue %d times, want >= 3", q.polls())
	}
	for _, id := range []string{f1, f2} {
		if r := fx.row(b, id); r.IntroSourceHash == "" || r.IntroTranscription == nil {
			t.Fatalf("file %s was never written after the scan finished: %+v", id, r)
		}
	}
}

func TestFileIntro_RefusesToStartWhileAScanRuns(t *testing.T) {
	fx := newIntroFx(t)
	q := &scanQueue{busy: true}
	fx.p = New(queueDeps{fakeDeps: fakeDeps{store: fx.s}, q: q})
	b := fx.book("Scan at start")
	f1, p1 := fx.file(b, "01.m4b", 1, "ss1", false)
	fx.whisp.texts[p1] = goodIntro

	_, err := fx.run("")
	if err == nil || !strings.Contains(err.Error(), "library.scan") || !strings.Contains(err.Error(), "refusing to start") {
		t.Fatalf("a live run during a scan must refuse to start, got %v", err)
	}
	if n := len(fx.whisp.sentPaths()); n != 0 {
		t.Fatalf("%d files were sent to whisper by a refused run", n)
	}
	if r := fx.row(b, f1); r.IntroSourceHash != "" || r.IntroTranscription != nil {
		t.Fatalf("a refused run wrote: %+v", r)
	}

	// The preview is read-only and is never refused.
	rep := &fileIntroRep{id: "preview-1"}
	if err := fx.p.runFileIntroTranscribe(context.Background(), json.RawMessage(`{}`), rep); err != nil {
		t.Fatalf("preview during a scan: %v", err)
	}
	if !strings.HasPrefix(rep.lastMessage(), introPreviewMarker) {
		t.Fatalf("preview message: %q", rep.lastMessage())
	}

	// And the queue being unreadable is not an idle queue.
	fx.p = New(queueDeps{fakeDeps: fakeDeps{store: fx.s}, q: nil})
	if _, err := fx.run(""); err == nil {
		t.Fatal("a nil queue must fail closed")
	}
}

func TestFileIntroClear_RefusesWhileAScanRunsAndGatesEachChunk(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("Clear vs scan")
	f1, p1 := fx.file(b, "01.m4b", 1, "cs1", false)
	fx.whisp.texts[p1] = goodIntro
	if _, err := fx.run(""); err != nil {
		t.Fatal(err)
	}

	q := &scanQueue{busy: true}
	fx.p = New(queueDeps{fakeDeps: fakeDeps{store: fx.s}, q: q})
	err := fx.p.runFileIntroTranscribe(context.Background(), liveTranscribeParams(`{"clear_run_id":"run-test-1"}`), &fileIntroRep{id: "clear-x"})
	if err == nil || !strings.Contains(err.Error(), "refusing to start") {
		t.Fatalf("a live clear during a scan must refuse to start, got %v", err)
	}
	if r := fx.row(b, f1); r.IntroRunOpID != "run-test-1" || r.IntroTranscription == nil {
		t.Fatalf("a refused clear wrote: %+v", r)
	}

	// Idle at the start gate, scan appears before the chunk's writes: the clear
	// waits and clears only after the scan is gone.
	q2 := &scanQueue{clearAfter: 3}
	fx.p = New(queueDeps{fakeDeps: fakeDeps{store: fx.s}, q: &flipAfterFirstPoll{inner: q2}})
	var early bool
	q2.onBusyPoll = func() {
		if r := fx.row(b, f1); r.IntroRunOpID == "" {
			early = true
		}
	}
	if err := fx.p.runFileIntroTranscribe(context.Background(), liveTranscribeParams(`{"clear_run_id":"run-test-1"}`), &fileIntroRep{id: "clear-y"}); err != nil {
		t.Fatal(err)
	}
	if early {
		t.Fatal("the clear wrote while library.scan was running")
	}
	if q2.polls() < 3 {
		t.Fatalf("the clear's chunk gate polled the busy queue %d times, want >= 3", q2.polls())
	}
	if r := fx.row(b, f1); r.IntroRunOpID != "" || r.IntroTranscription != nil {
		t.Fatalf("clear never landed after the scan finished: %+v", r)
	}
}

// flipAfterFirstPoll answers the first poll idle (the start gate) and flips the
// wrapped scanQueue to busy for every later one.
type flipAfterFirstPoll struct {
	inner *scanQueue
	n     atomic.Int32
}

func (q *flipAfterFirstPoll) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	if q.n.Add(1) == 1 {
		return nil, nil
	}
	q.inner.setBusyOnce()
	return q.inner.ListActiveOperationsV2()
}

// ---- clear ------------------------------------------------------------------

func TestFileIntroClear_DryRunCountsThenLiveRevertsOnlyItsOwnFills(t *testing.T) {
	fx := newIntroFx(t)
	a := fx.book("Clear A") // filled by the run, untouched afterwards -> reverted
	a1, ap := fx.file(a, "01.m4b", 1, "ca1", false)
	a2, a2p := fx.file(a, "02.m4b", 2, "ca2", false)
	b := fx.book("Clear B") // filled by the run, then rewritten by someone else -> kept
	b1, bp := fx.file(b, "01.m4b", 1, "cb1", false)
	fx.whisp.texts[ap], fx.whisp.texts[a2p], fx.whisp.texts[bp] = goodIntro, "t2", "b first"
	if _, err := fx.run(""); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.s.ModifyBook(b, func(cur *database.Book) error { cur.IntroTranscription = sp("owner edit"); return nil }); err != nil {
		t.Fatal(err)
	}
	// A second run's file must survive clearing the first run.
	other := fx.book("Other run")
	o1, op := fx.file(other, "01.m4b", 1, "co1", false)
	fx.whisp.texts[op] = "other words"
	if _, err := fx.runAs(&fileIntroRep{id: "run-keep"}, ""); err != nil {
		t.Fatal(err)
	}

	// Dry run: counts, writes nothing.
	rep := &fileIntroRep{id: "clear-1"}
	if err := fx.p.runFileIntroTranscribe(context.Background(), json.RawMessage(`{"clear_run_id":"run-test-1"}`), rep); err != nil {
		t.Fatal(err)
	}
	if m := rep.lastMessage(); !strings.HasPrefix(m, introPreviewMarker) || !strings.Contains(m, "would clear per-file results on 3 files and 1 book transcripts") {
		t.Fatalf("clear preview: %q", m)
	}
	if fx.row(a, a1).IntroTranscription == nil || fx.bookRow(a).IntroTranscription == nil {
		t.Fatal("clear dry run wrote")
	}

	// Live.
	rep = &fileIntroRep{id: "clear-2"}
	if err := fx.p.runFileIntroTranscribe(context.Background(), liveTranscribeParams(`{"clear_run_id":"run-test-1"}`), rep); err != nil {
		t.Fatal(err)
	}
	if m := rep.lastMessage(); !strings.Contains(m, "cleared per-file results on 3 files and 1 book transcripts") {
		t.Fatalf("clear message: %q", m)
	}
	for _, id := range []struct{ book, file string }{{a, a1}, {a, a2}, {b, b1}} {
		r := fx.row(id.book, id.file)
		if r.IntroTranscription != nil || r.IntroSourceHash != "" || r.IntroRunOpID != "" || r.TranscribeStatus != nil || r.IntroMeta != nil {
			t.Fatalf("file %s not cleared: %+v", id.file, r)
		}
	}
	if fx.bookRow(a).IntroTranscription != nil || fx.bookRow(a).TranscribeStatus != nil {
		t.Fatal("run's own book fill was not reverted")
	}
	if got := fx.bookRow(b).IntroTranscription; got == nil || *got != "owner edit" {
		t.Fatalf("a transcript written after the fill was erased: %v", got)
	}
	if r := fx.row(other, o1); r.IntroTranscription == nil || r.IntroRunOpID != "run-keep" {
		t.Fatalf("another run's result was cleared: %+v", r)
	}
	// Cleared files are work again.
	fx.whisp.calls = nil
	if _, err := fx.runAs(&fileIntroRep{id: "run-test-4"}, ""); err != nil {
		t.Fatal(err)
	}
	if got := fx.whisp.sentPaths(); got[ap] != 1 || got[a2p] != 1 || got[bp] != 1 || len(got) != 3 {
		t.Fatalf("after clearing, the three files should be redone: %v", got)
	}
}

func TestFileIntroClear_DonorCopyDoesNotLetItsClearUndoTheDonorsBookFill(t *testing.T) {
	fx := newIntroFx(t)
	a := fx.book("Donor book")
	_, ap := fx.file(a, "01.m4b", 1, "dd", false)
	fx.whisp.texts[ap] = goodIntro
	if _, err := fx.runAs(&fileIntroRep{id: "run-donor"}, ""); err != nil {
		t.Fatal(err)
	}
	b := fx.book("Copy book")
	fx.file(b, "01.m4b", 1, "dd", false)
	if _, err := fx.runAs(&fileIntroRep{id: "run-copy"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := fx.p.runFileIntroTranscribe(context.Background(), liveTranscribeParams(`{"clear_run_id":"run-copy"}`), &fileIntroRep{id: "c"}); err != nil {
		t.Fatal(err)
	}
	if got := fx.bookRow(a).IntroTranscription; got == nil || *got != goodIntro {
		t.Fatal("clearing the copy run undid the donor run's book fill")
	}
	if fx.bookRow(b).IntroTranscription != nil {
		t.Fatal("the copy run's own book fill was not reverted")
	}
}

// ---- definition ---------------------------------------------------------------

func TestFileIntroDef_Shape(t *testing.T) {
	def := New(fakeDeps{}).fileIntroTranscribeDef()
	if def.ID != "maintenance.transcribe-file-intros" || !def.Cancellable {
		t.Fatalf("def: %+v", def)
	}
	// A multi-day op must not hold the dispatcher's write-set gate (it would keep
	// acoustid.backfill queued for the whole run); it still has to be a writer.
	if len(def.Writes) != 0 {
		t.Fatalf("op declares Writes %v; see the comment on the def", def.Writes)
	}
	var writes bool
	for _, c := range def.Capabilities {
		if c == sdk.CapLibraryWrite {
			writes = true
		}
	}
	if !writes {
		t.Fatal("op must declare library.write so v3 treats it as a writer")
	}
}

// ---- review-round behaviour ---------------------------------------------------

// A transport outage that recovers inside the retry budget writes the batch
// once, with nothing deferred and no run failure.
func TestFileIntroRun_OutageThatRecoversRetriesTheSameBatch(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("Recovers")
	id1, p1 := fx.file(b, "01.m4b", 1, "o1", false)
	fx.whisp.texts[p1] = goodIntro
	fx.whisp.transErr = &transcribe.TransportError{Endpoints: []string{"http://192.0.2.10:8000"}, Err: errors.New("connection refused")}
	fx.whisp.transErrFor = 2

	rep, err := fx.run("")
	if err != nil {
		t.Fatalf("a recovering outage must not fail the run: %v", err)
	}
	if n := len(fx.whisp.calls); n != 3 {
		t.Fatalf("expected 2 failed tries + 1 success, calls=%d", n)
	}
	if r := fx.row(b, id1); r.IntroSourceHash != "o1" || r.IntroTranscription == nil || *r.IntroTranscription != goodIntro {
		t.Fatalf("file not written after recovery: %+v", r)
	}
	st, _ := fx.s.GetFileIntroStats()
	if st.Deferred != 0 || st.OK != 1 {
		t.Fatalf("stats after recovery: %+v", st)
	}
	_ = rep
}

// A worker that answers without a key for a sent job did not say "silence": the
// file stays undone and the next run sends it again.
func TestFileIntroRun_AMissingAnswerIsDeferredNotSilence(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("Dropped")
	id1, p1 := fx.file(b, "01.m4b", 1, "d1", false)
	fx.whisp.texts[p1] = goodIntro
	fx.whisp.dropKeys = true
	if _, err := fx.run(""); err != nil {
		t.Fatal(err)
	}
	if r := fx.row(b, id1); r.IntroSourceHash != "" || r.IntroTranscription != nil || r.TranscribeStatus != nil {
		t.Fatalf("a dropped job was written: %+v", r)
	}
	if st, _ := fx.s.GetFileIntroStats(); st.Deferred != 1 || st.Silence != 0 {
		t.Fatalf("stats: %+v", st)
	}
	fx.whisp.dropKeys = false
	fx.whisp.calls = nil
	if _, err := fx.runAs(&fileIntroRep{id: "run-test-2"}, ""); err != nil {
		t.Fatal(err)
	}
	if got := fx.whisp.sentPaths(); got[p1] != 1 {
		t.Fatalf("the dropped file was not re-sent: %v", got)
	}
}

// A forced run that hears silence over a file that already holds text keeps the
// text, and a forced replacement is not counted as new coverage.
func TestFileIntroRun_ForcedSilenceNeverReplacesTextAndReplacementIsNotNewCoverage(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("Forced")
	id1, p1 := fx.file(b, "01.m4b", 1, "f1", false)
	id2, p2 := fx.file(b, "02.m4b", 2, "f2", false)
	fx.whisp.texts[p1], fx.whisp.texts[p2] = goodIntro, "second file words"
	if _, err := fx.run(""); err != nil {
		t.Fatal(err)
	}
	st, _ := fx.s.GetFileIntroStats()
	if st.TranscribedNow != 2 {
		t.Fatalf("first run coverage: %+v", st)
	}

	fx.whisp.texts[p1] = "" // silence this time
	fx.whisp.texts[p2] = "replacement words"
	if _, err := fx.runAs(&fileIntroRep{id: "run-test-2"}, `{"only_missing":false}`); err != nil {
		t.Fatal(err)
	}
	r1 := fx.row(b, id1)
	if r1.IntroTranscription == nil || *r1.IntroTranscription != goodIntro || r1.IntroRunOpID != "run-test-1" {
		t.Fatalf("forced silence replaced real text: %+v", r1)
	}
	r2 := fx.row(b, id2)
	if r2.IntroTranscription == nil || *r2.IntroTranscription != "replacement words" || r2.IntroRunOpID != "run-test-2" {
		t.Fatalf("forced run did not replace the transcript: %+v", r2)
	}
	st, _ = fx.s.GetFileIntroStats()
	if st.TranscribedNow != 2 {
		t.Fatalf("a replacement counted as new coverage: %+v", st)
	}
}

// A failure verdict never lands on a row that holds a result, so it can neither
// relabel a transcript as an error nor claim a migrated sentinel for its run id
// (which clear_run_id would then erase).
func TestFileIntroRun_FailureNeverTouchesARowHoldingAResult(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("Migrated")
	id1, p1 := fx.file(b, "01.m4b", 1, "m1", false)
	sentinel := transcribe.SilenceSentinel
	if _, err := fx.s.SetBookFileIntro(b, id1, database.BookFileIntroResult{
		SourceHash: "m1", Status: statusEmpty, Transcript: &sentinel, Silence: true,
		At: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Done: true,
	}); err != nil {
		t.Fatal(err)
	}
	fx.whisp.errs[p1] = "whisper_error: boom"
	if _, err := fx.run(`{"only_missing":false}`); err != nil {
		t.Fatal(err)
	}
	r := fx.row(b, id1)
	if r.IntroRunOpID != "" || r.TranscribeStatus == nil || *r.TranscribeStatus != statusEmpty ||
		r.IntroTranscription == nil || *r.IntroTranscription != sentinel {
		t.Fatalf("a failure touched a row holding a result: %+v", r)
	}
	// Clearing the run that tried it leaves the migrated sentinel alone.
	if err := fx.p.runFileIntroTranscribe(context.Background(), liveTranscribeParams(`{"clear_run_id":"run-test-1"}`), &fileIntroRep{id: "clear-z"}); err != nil {
		t.Fatal(err)
	}
	if r := fx.row(b, id1); r.IntroTranscription == nil || r.IntroSourceHash != "m1" {
		t.Fatalf("clear_run_id erased a result the run never wrote: %+v", r)
	}
}

// recordingReporter keeps every progress message.
type recordingReporter struct {
	lastProgressReporter
	mu2  sync.Mutex
	msgs []string
}

func (r *recordingReporter) UpdateProgress(c, t int, msg string) error {
	r.mu2.Lock()
	r.msgs = append(r.msgs, msg)
	r.mu2.Unlock()
	return r.lastProgressReporter.UpdateProgress(c, t, msg)
}

func (r *recordingReporter) sawPause() bool {
	r.mu2.Lock()
	defer r.mu2.Unlock()
	for _, m := range r.msgs {
		if strings.Contains(m, "paused") {
			return true
		}
	}
	return false
}

// While a clear waits out a busy op it keeps announcing itself, so the
// watchdog sees a live op instead of a silent one.
func TestFileIntroClear_AnnouncesItsPauseInProgress(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("Clear pause")
	_, p1 := fx.file(b, "01.m4b", 1, "cp1", false)
	fx.whisp.texts[p1] = goodIntro
	if _, err := fx.run(""); err != nil {
		t.Fatal(err)
	}
	q2 := &scanQueue{clearAfter: 3}
	fx.p = New(queueDeps{fakeDeps: fakeDeps{store: fx.s}, q: &flipAfterFirstPoll{inner: q2}})
	rep := &recordingReporter{}
	if err := fx.p.runFileIntroTranscribe(context.Background(), liveTranscribeParams(`{"clear_run_id":"run-test-1"}`), &fileIntroClearRep{recordingReporter: rep, id: "clear-p"}); err != nil {
		t.Fatal(err)
	}
	if !rep.sawPause() {
		t.Fatalf("clear never reported its pause, messages: %v", rep.msgs)
	}
}

type fileIntroClearRep struct {
	*recordingReporter
	id string
}

func (r *fileIntroClearRep) OpID() string { return r.id }

// The database package cannot import this one, so the status strings it
// compares against are duplicated there; this keeps the two in step.
func TestFileIntro_StatusConstantsMatchTheDatabasePackage(t *testing.T) {
	for _, c := range [][2]string{{statusOK, database.IntroStatusOK}, {statusUnparsed, database.IntroStatusUnparsed}, {statusEmpty, database.IntroStatusEmpty}} {
		if c[0] != c[1] {
			t.Fatalf("maintenance status %q != database status %q", c[0], c[1])
		}
	}
}
