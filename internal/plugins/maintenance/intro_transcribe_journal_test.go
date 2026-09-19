// file: internal/plugins/maintenance/intro_transcribe_journal_test.go
// version: 1.1.0
// guid: 0b42d884-b844-4201-ba60-046bfb995928
// last-edited: 2026-09-19

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
)

// fakeWhisper is an httptest whisper server speaking the batch protocol of
// scripts/whisper_mlx_server.py: GET /health reports batch_pipeline + model,
// POST /transcribe-batch takes multipart "files" parts and answers
// {"results": {<part filename>: {"text": ...}}}. It counts how many times each
// distinct clip (by bytes) was transcribed. killOnRequest > 0 makes that
// request number the "kill": it cancels the run's context and never answers.
type fakeWhisper struct {
	srv *httptest.Server

	mu            sync.Mutex
	perClip       map[string]int // clip bytes -> times transcribed
	requests      int
	killOnRequest int
	kill          context.CancelFunc
	respond       func(clip string) string // nil = transcriptFor
}

func (fw *fakeWhisper) setRespond(f func(string) string) {
	fw.mu.Lock()
	fw.respond = f
	fw.mu.Unlock()
}

func newFakeWhisper(t *testing.T) *fakeWhisper {
	t.Helper()
	fw := &fakeWhisper{perClip: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "model": "test/whisper-small", "backend": "fake",
			"batch_pipeline": true, "device": "cpu",
		})
	})
	mux.HandleFunc("POST /transcribe-batch", func(w http.ResponseWriter, r *http.Request) {
		fw.mu.Lock()
		fw.requests++
		n, killAt, kill := fw.requests, fw.killOnRequest, fw.kill
		fw.mu.Unlock()
		if killAt > 0 && n == killAt {
			// The kill: cancel the run, never answer. The body is drained so
			// the server notices the client hang up; the timer bounds the wait
			// so srv.Close can never block on this handler.
			kill()
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
			return
		}
		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		results := map[string]map[string]any{}
		for {
			part, perr := mr.NextPart()
			if perr == io.EOF {
				break
			}
			if perr != nil {
				http.Error(w, perr.Error(), http.StatusBadRequest)
				return
			}
			body, _ := io.ReadAll(part)
			fw.mu.Lock()
			fw.perClip[string(body)]++
			respond := fw.respond
			fw.mu.Unlock()
			if respond == nil {
				respond = transcriptFor
			}
			results[part.FileName()] = map[string]any{"text": respond(string(body)), "error": nil}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	})
	fw.srv = httptest.NewServer(mux)
	t.Cleanup(fw.srv.Close)
	return fw
}

func (fw *fakeWhisper) snapshot() (map[string]int, int) {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	out := make(map[string]int, len(fw.perClip))
	for k, v := range fw.perClip {
		out[k] = v
	}
	return out, fw.requests
}

// transcriptFor is non-empty for every clip: empty text would send the book
// down Step 3b's silence-retry ladder (real ffmpeg, two more batch calls).
func transcriptFor(clip string) string {
	return "This is Audible. " + clip + ", written by Some Author."
}

// writeCountingStore is a real PebbleStore that counts, per book, the
// ModifyBook calls that actually wrote (the closure returned nil).
type writeCountingStore struct {
	*database.PebbleStore
	mu     *sync.Mutex
	writes map[string]int
}

func (s writeCountingStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	wrote := false
	b, err := s.PebbleStore.ModifyBook(id, func(cur *database.Book) error {
		ferr := fn(cur)
		wrote = ferr == nil
		return ferr
	})
	if err == nil && b != nil && wrote {
		s.mu.Lock()
		s.writes[id]++
		s.mu.Unlock()
	}
	return b, err
}

// journalTestEnv points the remote whisper path at fw with a small sub-batch
// so one page spans several chunks, and gives books a pre-populated clip
// cache (the cache hit runs before the source-file stat, so no ffmpeg).
func journalTestEnv(t *testing.T, fw *fakeWhisper, batchSize int) string {
	t.Helper()
	cacheDir := t.TempDir()
	orig := config.Snapshot()
	config.Mutate(func(c *config.Config) {
		c.WhisperRemoteURL = fw.srv.URL
		c.WhisperEndpoints = nil
		c.WhisperRequires = nil
		c.WhisperBatchSize = batchSize
		c.WhisperBatchSleepMS = 0
		c.WhisperMaxInFlight = 0
		c.WhisperClipCacheDir = cacheDir
	})
	t.Cleanup(func() {
		config.Mutate(func(c *config.Config) {
			c.WhisperRemoteURL = orig.WhisperRemoteURL
			c.WhisperEndpoints = orig.WhisperEndpoints
			c.WhisperRequires = orig.WhisperRequires
			c.WhisperBatchSize = orig.WhisperBatchSize
			c.WhisperBatchSleepMS = orig.WhisperBatchSleepMS
			c.WhisperMaxInFlight = orig.WhisperMaxInFlight
			c.WhisperClipCacheDir = orig.WhisperClipCacheDir
		})
	})
	return cacheDir
}

// addClipBook creates a book whose only audio is its Book.FilePath (the
// single-file fallback) and writes clip as its cached 90s WAV.
func addClipBook(t *testing.T, s *database.PebbleStore, cacheDir string, i int, clip string) string {
	t.Helper()
	fp := fmt.Sprintf("/nonexistent/library/book-%03d.m4b", i)
	b, err := s.CreateBook(&database.Book{Title: fmt.Sprintf("Book %03d", i), FilePath: fp})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	h := sha256.Sum256([]byte(fp))
	if err := os.WriteFile(cachedClipPath(cacheDir, "path:"+hex.EncodeToString(h[:])), []byte(clip), 0o644); err != nil {
		t.Fatalf("write clip: %v", err)
	}
	return b.ID
}

func openJournalPebble(t *testing.T, dir string) *database.PebbleStore {
	t.Helper()
	s, err := database.NewPebbleStore(dir)
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	s.WaitForWarmup()
	return s
}

func runTranscribeOnce(t *testing.T, s writeCountingStore, ctx context.Context, params string) error {
	t.Helper()
	p := &Plugin{deps: rootDeps{fakeDeps: fakeDeps{store: s}, root: t.TempDir()}}
	return p.runIntroTranscribe(ctx, json.RawMessage(params), &denomReporter{})
}

// TestIntroTranscribe_KillMidPageLosesNothingAndRepeatsNothing simulates a
// kill -9 in the middle of a page: 48 clips go out in 6 chunks of 8, and the
// context is cancelled the moment chunk 3 is sent -- after chunks 1 and 2 came
// back. The store is then closed and reopened from disk (the restart) and a NEW
// plugin runs over it. Then a third run with only_missing=false re-selects every
// book.
//
// Required outcome: every book ends with its own transcript (0 lost), the
// server transcribed each distinct clip exactly once across all runs
// (0 re-transcribed), and every book got exactly one transcript write
// (0 double-applied).
func TestIntroTranscribe_KillMidPageLosesNothingAndRepeatsNothing(t *testing.T) {
	const nBooks, chunk = 48, 8
	fw := newFakeWhisper(t)
	cacheDir := journalTestEnv(t, fw, chunk)
	dir := t.TempDir()

	s1 := openJournalPebble(t, dir)
	clipOf := map[string]string{}
	for i := range nBooks {
		clip := fmt.Sprintf("clip-%03d", i)
		clipOf[addClipBook(t, s1, cacheDir, i, clip)] = clip
	}
	writes := map[string]int{}
	var wmu sync.Mutex

	// Run 1: killed after chunk 2 returned.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fw.mu.Lock()
	fw.killOnRequest, fw.kill = 3, cancel
	fw.mu.Unlock()
	_ = runTranscribeOnce(t, writeCountingStore{s1, &wmu, writes}, ctx, `{}`)
	perClip, reqs := fw.snapshot()
	if got := len(perClip); got != 2*chunk {
		t.Fatalf("setup: run 1 transcribed %d clips before the kill (requests=%d), want %d", got, reqs, 2*chunk)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close (kill): %v", err)
	}

	// Run 2: restart on the same on-disk store.
	s2 := openJournalPebble(t, dir)
	t.Cleanup(func() { _ = s2.Close() })
	fw.mu.Lock()
	fw.killOnRequest = 0
	fw.mu.Unlock()
	if err := runTranscribeOnce(t, writeCountingStore{s2, &wmu, writes}, context.Background(), `{}`); err != nil {
		t.Fatalf("run 2: %v", err)
	}

	// Run 3: re-select every book, transcript or not.
	if err := runTranscribeOnce(t, writeCountingStore{s2, &wmu, writes}, context.Background(), `{"only_missing":false}`); err != nil {
		t.Fatalf("run 3: %v", err)
	}

	perClip, _ = fw.snapshot()
	lost, repeated, doubled := 0, 0, 0
	for id, clip := range clipOf {
		b, err := s2.GetBookByID(id)
		if err != nil || b == nil || b.IntroTranscription == nil || *b.IntroTranscription != transcriptFor(clip) {
			lost++
		}
		if perClip[clip] != 1 {
			repeated++
			t.Logf("clip %s transcribed %d times", clip, perClip[clip])
		}
		wmu.Lock()
		if writes[id] != 1 {
			doubled++
			t.Logf("book %s written %d times", id, writes[id])
		}
		wmu.Unlock()
	}
	if lost != 0 || repeated != 0 || doubled != 0 {
		t.Fatalf("lost=%d re-transcribed=%d double-applied=%d (of %d books); want 0/0/0",
			lost, repeated, doubled, nBooks)
	}
}

// TestIntroTranscribe_IdenticalClipsBothWritten: two books whose clips are
// byte-identical (distinct source files, so the WAV cache does NOT collapse
// them -- the shared-Audible-intro case). The server must transcribe the
// content once, and BOTH books must receive the transcript: the journal's
// result sharing must never stand in for a per-book apply.
func TestIntroTranscribe_IdenticalClipsBothWritten(t *testing.T) {
	fw := newFakeWhisper(t)
	cacheDir := journalTestEnv(t, fw, 8)
	s := openJournalPebble(t, t.TempDir())
	t.Cleanup(func() { _ = s.Close() })
	const clip = "shared-audible-intro"
	a := addClipBook(t, s, cacheDir, 1, clip)
	b := addClipBook(t, s, cacheDir, 2, clip)
	writes := map[string]int{}
	var wmu sync.Mutex

	if err := runTranscribeOnce(t, writeCountingStore{s, &wmu, writes}, context.Background(), `{}`); err != nil {
		t.Fatalf("run: %v", err)
	}
	perClip, _ := fw.snapshot()
	if perClip[clip] != 1 {
		t.Errorf("identical clip transcribed %d times, want 1", perClip[clip])
	}
	for _, id := range []string{a, b} {
		got, _ := s.GetBookByID(id)
		if got == nil || got.IntroTranscription == nil || *got.IntroTranscription != transcriptFor(clip) {
			t.Errorf("book %s has no transcript", id)
		}
		if writes[id] != 1 {
			t.Errorf("book %s written %d times, want 1", id, writes[id])
		}
	}
}

// retry_silence exists to get a different answer for the same audio after VAD
// or decoder tuning, so the run must bypass journal lookups. A book whose
// first run came back silent is transcribed again, reaches the server, and
// takes the new text over its [SILENCE] sentinel.
func TestIntroTranscribe_RetrySilenceReachesServer(t *testing.T) {
	fw := newFakeWhisper(t)
	cacheDir := journalTestEnv(t, fw, 8)
	s := openJournalPebble(t, t.TempDir())
	t.Cleanup(func() { _ = s.Close() })
	const clip = "quiet-intro"
	id := addClipBook(t, s, cacheDir, 1, clip)
	writes := map[string]int{}
	var wmu sync.Mutex
	st := writeCountingStore{s, &wmu, writes}

	fw.setRespond(func(string) string { return "" }) // before tuning: silent
	if err := runTranscribeOnce(t, st, context.Background(), `{}`); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if b, _ := s.GetBookByID(id); b == nil || b.IntroTranscription == nil || *b.IntroTranscription != silenceSentinel {
		t.Fatalf("setup: run 1 did not mark the book silent")
	}
	fw.setRespond(transcriptFor) // after tuning: speech found
	if err := runTranscribeOnce(t, st, context.Background(), `{"retry_silence":true}`); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	perClip, _ := fw.snapshot()
	if perClip[clip] != 2 {
		t.Errorf("clip reached the server %d times across both runs, want 2", perClip[clip])
	}
	if b, _ := s.GetBookByID(id); b == nil || b.IntroTranscription == nil || *b.IntroTranscription != transcriptFor(clip) {
		t.Errorf("book still lacks the retuned transcript (has %q)", derefStr(b.IntroTranscription))
	}
}

// The plugin must ask TranscribeBatchOpts to bypass lookups on retry_silence
// and only then.
func TestProcessTranscribePage_RetrySilenceRefreshesJournal(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		store, _, books, rootDir := transportTestFixture(t, "b-refresh", "hash-refresh")
		var got []bool
		swapTranscribeBatchFn(t, func(_ context.Context, jobs map[string]string, o transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
			got = append(got, o.RefreshJournal)
			out := map[string]transcribe.BatchResult{}
			for id := range jobs {
				out[id] = transcribe.BatchResult{Text: "hello"}
			}
			return out, nil
		})
		p := New(fakeDeps{store: store})
		accum := newTranscribeStatsAccum(nil, "op", 0, time.Now())
		p.processTranscribePage(context.Background(), store, slog.Default(), books, rootDir, nil, accum, false,
			pageJournal{refresh: refresh})
		if len(got) != 1 || got[0] != refresh {
			t.Errorf("refresh=%v: RefreshJournal passed = %v", refresh, got)
		}
	}
}

// failingJournalStore fails every journal write; everything else is Pebble.
type failingJournalStore struct{ writeCountingStore }

func (failingJournalStore) SetRaw(key string, _ []byte) error {
	return fmt.Errorf("disk full writing %s", key)
}

// A journal write failure does not fail the book (the transcript still lands)
// but is counted in stats:transcribe and named in the final progress message.
func TestIntroTranscribe_JournalWriteFailuresAreCounted(t *testing.T) {
	fw := newFakeWhisper(t)
	cacheDir := journalTestEnv(t, fw, 8)
	s := openJournalPebble(t, t.TempDir())
	t.Cleanup(func() { _ = s.Close() })
	for i := range 3 {
		addClipBook(t, s, cacheDir, i, fmt.Sprintf("clip-%d", i))
	}
	var wmu sync.Mutex
	st := failingJournalStore{writeCountingStore{s, &wmu, map[string]int{}}}
	p := &Plugin{deps: rootDeps{fakeDeps: fakeDeps{store: st}, root: t.TempDir()}}
	rep := &denomReporter{}
	if err := p.runIntroTranscribe(context.Background(), json.RawMessage(`{}`), rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	stats, err := s.GetTranscribeStats()
	if err != nil || stats == nil {
		t.Fatalf("stats: %v %v", stats, err)
	}
	if stats.JournalWriteFailures != 3 || stats.OK+stats.Unparsed != 3 {
		t.Errorf("journal_write_failures=%d transcribed=%d, want 3/3", stats.JournalWriteFailures, stats.OK+stats.Unparsed)
	}
	if !strings.Contains(rep.last, "3 journal-write failures") {
		t.Errorf("final message %q does not name the journal write failures", rep.last)
	}
}

// extractClip must never expose a partially written WAV at the cache path:
// another page may stat, hash and upload that path at any moment. A fake
// ffmpeg writes half a clip, pauses, then the rest; the destination must be
// absent or complete at every observation, and a failed ffmpeg must leave
// neither the destination nor a temp file behind.
func TestExtractClip_AtomicRename(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
for a in "$@"; do out="$a"; done
if [ "$FAKE_FFMPEG_FAIL" = 1 ]; then printf 'half' > "$out"; exit 1; fi
printf 'half' > "$out"; sleep 0.3; printf '%s' '-and-rest' >> "$out"
`
	if err := os.WriteFile(filepath.Join(bin, "ffmpeg"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	dir := t.TempDir()
	dst := filepath.Join(dir, "clip.wav")

	stop := make(chan struct{})
	var bad []string
	var watch sync.WaitGroup
	watch.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if b, err := os.ReadFile(dst); err == nil && string(b) != "half-and-rest" {
				bad = append(bad, string(b))
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	_, err := extractClip(context.Background(), "/src.m4b", dst, 90)
	close(stop)
	watch.Wait()
	if err != nil {
		t.Fatalf("extractClip: %v", err)
	}
	if len(bad) > 0 {
		t.Fatalf("partial clip visible at the cache path: %q", bad[0])
	}
	if b, _ := os.ReadFile(dst); string(b) != "half-and-rest" {
		t.Fatalf("final clip = %q", b)
	}

	t.Setenv("FAKE_FFMPEG_FAIL", "1")
	dst2 := filepath.Join(dir, "failed.wav")
	if _, err := extractClip(context.Background(), "/src.m4b", dst2, 90); err == nil {
		t.Fatal("failing ffmpeg returned nil error")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "clip.wav" {
			t.Errorf("failed extraction left %s behind", e.Name())
		}
	}
}

func derefStr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}
