// file: internal/plugins/maintenance/intro_transcribe_remote_test.go
// version: 1.3.0
// guid: b21d7c95-3e48-4a06-9f1d-5c8e0a64b7f3
// last-edited: 2026-10-25

package maintenance

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serverdecode"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
)

type pathBatchFn = func(context.Context, map[string]transcribe.PathJob, transcribe.BatchOptions) (map[string]transcribe.BatchResult, error)

// swapPathSeams substitutes the remote-extract seams and, as the standing
// guard of this file, replaces the UPLOAD-path batch call with one that fails
// the test: a remote-mode run must never reach it, because that path needs a
// WAV this host would have had to decode. Not for parallel tests.
func swapPathSeams(t *testing.T, available bool, reason string, fn pathBatchFn) {
	t.Helper()
	origPath, origProbe, origUpload := transcribePathBatchFn, pathPipelineAvailableFn, transcribeBatchFn
	transcribePathBatchFn = fn
	pathPipelineAvailableFn = func(context.Context) (bool, string) { return available, reason }
	transcribeBatchFn = func(context.Context, map[string]string, transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
		t.Error("remote mode reached the upload-path batch call, which needs a locally decoded WAV")
		return nil, errors.New("upload path must not be used in remote mode")
	}
	t.Cleanup(func() {
		transcribePathBatchFn, pathPipelineAvailableFn, transcribeBatchFn = origPath, origProbe, origUpload
	})
}

func boolPtr(b bool) *bool { return &b }

func TestResolveExtractMode(t *testing.T) {
	cases := []struct {
		name        string
		allowDecode bool
		extractOnly bool
		param       *bool
		available   bool
		wantMode    extractMode
		wantErr     string
		wantProbed  bool
	}{
		{"prod default with a path worker goes remote", false, false, nil, true, extractRemote, "", true},
		{"prod default with no path worker stays local (and Check will refuse)", false, false, nil, false, extractLocal, "", true},
		{"dev host that opted in keeps local and never probes", true, false, nil, true, extractLocal, "", false},
		{"explicit false is local without probing", false, false, boolPtr(false), true, extractLocal, "", false},
		{"explicit true goes remote even on a dev host", true, false, boolPtr(true), true, extractRemote, "", true},
		{"explicit true with no path worker is an error, never a silent local", false, false, boolPtr(true), false, extractLocal, "no whisper endpoint can serve path jobs", true},
		{"extract_only is always local", false, true, nil, true, extractLocal, "", false},
		{"extract_only with remote_extract=true is refused", false, true, boolPtr(true), true, extractLocal, "cannot be combined with extract_only", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.allowDecode {
				t.Setenv(serverdecode.EnvVar, "1")
			} else {
				t.Setenv(serverdecode.EnvVar, "")
			}
			probed := false
			orig := pathPipelineAvailableFn
			pathPipelineAvailableFn = func(context.Context) (bool, string) {
				probed = true
				return tc.available, "stub reason"
			}
			t.Cleanup(func() { pathPipelineAvailableFn = orig })

			mode, note, err := resolveExtractMode(context.Background(), tc.param, tc.extractOnly)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if mode != tc.wantMode {
				t.Errorf("mode = %v, want %v", mode, tc.wantMode)
			}
			if probed != tc.wantProbed {
				t.Errorf("probed = %v, want %v", probed, tc.wantProbed)
			}
			if tc.wantMode == extractLocal && tc.wantErr == "" && !tc.allowDecode && !tc.extractOnly && tc.param == nil && note == "" {
				t.Error("prod default that stayed local must say why remote was not used")
			}
		})
	}
}

// remoteBookStore is a MockStore with one book per entry and real source files
// on disk (the remote path stats them). files[bookID] lists the audio paths.
func remoteBookStore(t *testing.T, files map[string][]string) (*database.MockStore, *[]database.Book, []database.Book) {
	t.Helper()
	written := &[]database.Book{}
	var books []database.Book
	for id := range files {
		books = append(books, database.Book{ID: id})
	}
	store := &database.MockStore{
		GetBookFilesFunc: func(id string) ([]database.BookFile, error) {
			var out []database.BookFile
			for i, p := range files[id] {
				out = append(out, database.BookFile{ID: id + "-f", BookID: id, FilePath: p, TrackNumber: i + 1})
			}
			return out, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) { return &database.Book{ID: id}, nil },
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) {
			*written = append(*written, *b)
			return b, nil
		},
	}
	return store, written, books
}

func writeSource(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("not audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// noClipWrites points the clip cache at an empty dir and returns a check that
// nothing was written there or under rootDir: remote mode cuts no clip here.
func noClipWrites(t *testing.T) (rootDir string, check func()) {
	t.Helper()
	cacheDir := t.TempDir()
	rootDir = t.TempDir()
	orig := config.AppConfig.WhisperClipCacheDir
	config.Mutate(func(c *config.Config) { c.WhisperClipCacheDir = cacheDir })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { c.WhisperClipCacheDir = orig }) })
	// ffmpeg is unreachable: any exec of it fails, so a stray local decode
	// would surface as a failed outcome instead of passing quietly.
	t.Setenv("PATH", "")
	return rootDir, func() {
		t.Helper()
		for _, d := range []string{cacheDir, rootDir} {
			if ents, _ := os.ReadDir(d); len(ents) != 0 {
				t.Errorf("remote mode wrote into %s: %v", d, ents)
			}
		}
	}
}

func runRemotePage(t *testing.T, store *database.MockStore, books []database.Book, rootDir string) (int, *transcribeStatsAccum) {
	t.Helper()
	p := New(fakeDeps{store: store})
	accum := newTranscribeStatsAccum(nil, "op", 0, time.Now())
	n := p.processTranscribePage(context.Background(), store, slog.Default(), books, rootDir, nil, accum, false, false, pageJournal{}, extractRemote)
	return n, accum
}

func TestRemotePage_SendsSourcePathAnd90sAndWritesTranscript(t *testing.T) {
	src := writeSource(t, "one.m4b")
	store, written, books := remoteBookStore(t, map[string][]string{"b1": {src}})
	rootDir, noWrites := noClipWrites(t)
	var got map[string]transcribe.PathJob
	swapPathSeams(t, true, "", func(_ context.Context, jobs map[string]transcribe.PathJob, _ transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
		got = jobs
		return map[string]transcribe.BatchResult{"b1": {Text: "This is The Title by An Author, read by A Narrator"}}, nil
	})
	n, accum := runRemotePage(t, store, books, rootDir)
	if want := (transcribe.PathJob{Path: src, Seconds: 90}); got["b1"] != want {
		t.Errorf("worker job = %+v, want %+v", got["b1"], want)
	}
	if n != 1 || len(*written) != 1 || (*written)[0].IntroTranscription == nil {
		t.Fatalf("processed=%d writes=%d", n, len(*written))
	}
	if st := accum.snapshot(); st.CacheHits != 0 || st.Attempted != 1 {
		t.Errorf("stats = %+v (remote mode has no clip cache; one attempt)", st)
	}
	noWrites()
}

func statusOf(t *testing.T, written *[]database.Book) string {
	t.Helper()
	if len(*written) != 1 || (*written)[0].TranscribeStatus == nil {
		t.Fatalf("expected one status write, got %d", len(*written))
	}
	return *(*written)[0].TranscribeStatus
}

func TestRemotePage_WorkerErrorsMapToTheRightStatus(t *testing.T) {
	cases := []struct {
		name       string
		workerErr  string
		deleteSrc  bool // the server loses the file before the verdict is mapped
		wantStatus string
		durable    bool
	}{
		{"decode failure is ffmpeg_error", "ffmpeg_error: Invalid data found when processing input", false, statusFFmpegError, true},
		{"ffmpeg timeout is not a verdict on the file", "ffmpeg_timeout: ffmpeg exceeded 120s", false, statusWhisperError, false},
		{"bad request is retried", "bad_request: seconds must be an integer", false, statusWhisperError, false},
		{"whisper failure", "whisper_error: model fell over", false, statusWhisperError, false},
		{"old worker free text", "Failed to load audio", false, statusWhisperError, false},
		{"path refused while the server CAN stat it is path_unmapped", "path_unmapped: mapped root for /mnt/lib is not readable (mount lost?)", false, statusPathUnmapped, false},
		{"not_found while the server CAN stat it is path_unmapped", "not_found: file not found under the mapped root", false, statusPathUnmapped, false},
		{"path refused and the server cannot stat it is source_file_missing", "path_unmapped: path is not under a mapped prefix", true, statusSourceMissing, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := writeSource(t, "one.m4b")
			store, written, books := remoteBookStore(t, map[string][]string{"b1": {src}})
			rootDir, noWrites := noClipWrites(t)
			swapPathSeams(t, true, "", func(_ context.Context, _ map[string]transcribe.PathJob, _ transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
				if tc.deleteSrc {
					_ = os.Remove(src)
				}
				return map[string]transcribe.BatchResult{"b1": {Error: tc.workerErr}}, nil
			})
			n, accum := runRemotePage(t, store, books, rootDir)
			if n != 0 {
				t.Errorf("processed = %d, want 0", n)
			}
			if got := statusOf(t, written); got != tc.wantStatus {
				t.Errorf("status = %q, want %q", got, tc.wantStatus)
			}
			if isDurableTranscribeFailure(tc.wantStatus) != tc.durable {
				t.Errorf("isDurableTranscribeFailure(%q) = %v, want %v", tc.wantStatus, !tc.durable, tc.durable)
			}
			st := accum.snapshot()
			bucket := map[string]int{
				statusFFmpegError: st.FFmpegError, statusWhisperError: st.WhisperError,
				statusPathUnmapped: st.PathUnmapped, statusSourceMissing: st.SourceMissing,
			}
			if bucket[tc.wantStatus] != 1 || st.Attempted != 1 {
				t.Errorf("stats %+v: bucket for %q should be 1 and Attempted 1", st, tc.wantStatus)
			}
			noWrites()
		})
	}
}

// A book whose source the SERVER cannot stat never costs a worker request, and
// gets exactly the verdict local mode gives it.
func TestRemotePage_MissingSourceAndNoAudioNeverReachTheWorker(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone.m4b")
	store, written, books := remoteBookStore(t, map[string][]string{"b1": {missing}, "b2": {}})
	rootDir, noWrites := noClipWrites(t)
	swapPathSeams(t, true, "", func(_ context.Context, jobs map[string]transcribe.PathJob, _ transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
		t.Errorf("worker was asked about %v", jobs)
		return nil, nil
	})
	runRemotePage(t, store, books, rootDir)
	got := map[string]string{}
	for _, b := range *written {
		got[b.ID] = *b.TranscribeStatus
	}
	if len(got) != 2 || got["b2"] != statusNoAudio {
		t.Fatalf("statuses = %v", got)
	}
	if got["b1"] != statusSourceMissing {
		t.Errorf("b1 = %q, want %q", got["b1"], statusSourceMissing)
	}
	noWrites()
}

// A page the pool cannot place (no endpoint advertises the capability, or a
// root is unreadable) writes NOTHING: it is deferred, never a per-book verdict.
func TestRemotePage_TransportErrorWritesNoStatus(t *testing.T) {
	src := writeSource(t, "one.m4b")
	store, written, books := remoteBookStore(t, map[string][]string{"b1": {src}})
	rootDir, noWrites := noClipWrites(t)
	swapPathSeams(t, true, "", func(context.Context, map[string]transcribe.PathJob, transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
		return nil, &transcribe.TransportError{
			Endpoints: []string{"http://whisper-a.test"},
			Err:       errors.New("all 1 whisper endpoint(s) refused: mapped root(s) [/mnt/lib] not readable on the worker (NFS mount lost?)"),
		}
	})
	n, accum := runRemotePage(t, store, books, rootDir)
	if n != 0 || len(*written) != 0 {
		t.Fatalf("processed=%d writes=%d, want 0 and 0", n, len(*written))
	}
	if st := accum.snapshot(); st.Deferred != 1 || st.Attempted != 0 {
		t.Errorf("stats = %+v, want Deferred=1 Attempted=0", st)
	}
	noWrites()
}

// The silence retries are the other place this host used to decode: the 300 s
// clip and the second audio file. In remote mode both go to the worker as paths.
func TestRemotePage_SilenceRetriesGoToTheWorkerNotToFFmpeg(t *testing.T) {
	first := writeSource(t, "disc1.m4b")
	second := writeSource(t, "disc2.m4b")
	store, written, books := remoteBookStore(t, map[string][]string{"b1": {first, second}})
	rootDir, noWrites := noClipWrites(t)

	var mu sync.Mutex
	var calls []transcribe.PathJob
	swapPathSeams(t, true, "", func(_ context.Context, jobs map[string]transcribe.PathJob, _ transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
		mu.Lock()
		defer mu.Unlock()
		j := jobs["b1"]
		calls = append(calls, j)
		// First pass and the 300 s retry hear silence; the second file speaks.
		if j.Path == second {
			return map[string]transcribe.BatchResult{"b1": {Text: "This is The Title by An Author"}}, nil
		}
		return map[string]transcribe.BatchResult{"b1": {Text: ""}}, nil
	})
	n, _ := runRemotePage(t, store, books, rootDir)
	want := []transcribe.PathJob{{Path: first, Seconds: 90}, {Path: first, Seconds: 300}, {Path: second, Seconds: 90}}
	if len(calls) != len(want) {
		t.Fatalf("worker calls = %+v, want %+v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("call %d = %+v, want %+v", i, calls[i], want[i])
		}
	}
	if n != 1 || len(*written) == 0 {
		t.Errorf("processed=%d writes=%d, want the retry's transcript applied", n, len(*written))
	}
	noWrites()
}

// A worker whose ffmpeg is missing says so with worker_misconfigured. That is a
// fault of the worker, not a verdict about the book: nothing is written and the
// book is deferred (writing it as a per-book status is the 2026-08-31 shape).
func TestRemotePage_WorkerMisconfiguredWritesNothingAndDefers(t *testing.T) {
	src := writeSource(t, "one.m4b")
	store, written, books := remoteBookStore(t, map[string][]string{"b1": {src}})
	rootDir, noWrites := noClipWrites(t)
	swapPathSeams(t, true, "", func(context.Context, map[string]transcribe.PathJob, transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
		return map[string]transcribe.BatchResult{"b1": {Error: "worker_misconfigured: ffmpeg is not on PATH on this worker"}}, nil
	})
	n, accum := runRemotePage(t, store, books, rootDir)
	if n != 0 || len(*written) != 0 {
		t.Fatalf("processed=%d writes=%d, want 0 and 0", n, len(*written))
	}
	if st := accum.snapshot(); st.Deferred != 1 {
		t.Errorf("stats = %+v, want Deferred=1", st)
	}
	noWrites()
}

// A silence retry that FAILED (transport error, per-job error) proves nothing
// about the book: the [SILENCE] sentinel makes only_missing skip a book forever,
// so it must be written only when the retries came back as successful empty
// results. A retry that failed to say anything about the book (transport
// error, no result, misconfigured worker) writes nothing and the book is
// counted deferred.
func TestRemotePage_FailedSilenceRetryNeverWritesTheSentinel(t *testing.T) {
	transport := &transcribe.TransportError{
		Endpoints: []string{"http://whisper-a.test"},
		Err:       errors.New("all 1 whisper endpoint(s) refused: mapped root(s) [/mnt/lib] not readable on the worker (NFS mount lost?)"),
	}
	// A constructor, not a shared map: the op stores retry results into the map
	// the first pass returned, so a shared one would leak between cases.
	silent := func() map[string]transcribe.BatchResult {
		return map[string]transcribe.BatchResult{"b1": {Text: ""}}
	}
	cases := []struct {
		name         string
		twoFiles     bool
		retry        func(call int) (map[string]transcribe.BatchResult, error) // call 2 = retry1, 3 = retry2
		wantSentinel bool
		wantDeferred int
	}{
		{"single file, retry1 transport error", false, func(int) (map[string]transcribe.BatchResult, error) { return nil, transport }, false, 1},
		{"single file, retry1 worker_misconfigured", false, func(int) (map[string]transcribe.BatchResult, error) {
			return map[string]transcribe.BatchResult{"b1": {Error: "worker_misconfigured: ffmpeg is not on PATH on this worker"}}, nil
		}, false, 1},
		{"single file, retry1 returns no result for the book", false, func(int) (map[string]transcribe.BatchResult, error) {
			return map[string]transcribe.BatchResult{}, nil
		}, false, 1},
		{"two files, retry1 empty then retry2 transport error", true, func(call int) (map[string]transcribe.BatchResult, error) {
			if call == 2 {
				return silent(), nil
			}
			return nil, transport
		}, false, 1},
		{"two files, retry1 empty then retry2 worker_misconfigured", true, func(call int) (map[string]transcribe.BatchResult, error) {
			if call == 2 {
				return silent(), nil
			}
			return map[string]transcribe.BatchResult{"b1": {Error: "worker_misconfigured: x"}}, nil
		}, false, 1},
		// Positive control: every retry succeeds and is empty -> sentinel.
		{"single file, retry1 successfully empty", false, func(int) (map[string]transcribe.BatchResult, error) { return silent(), nil }, true, 0},
		{"two files, both retries successfully empty", true, func(int) (map[string]transcribe.BatchResult, error) { return silent(), nil }, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := []string{writeSource(t, "disc1.m4b")}
			if tc.twoFiles {
				files = append(files, writeSource(t, "disc2.m4b"))
			}
			store, written, books := remoteBookStore(t, map[string][]string{"b1": files})
			rootDir, noWrites := noClipWrites(t)
			var calls int
			swapPathSeams(t, true, "", func(context.Context, map[string]transcribe.PathJob, transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
				calls++
				if calls == 1 {
					return silent(), nil // first pass: the 90 s clip is silent
				}
				return tc.retry(calls)
			})
			n, accum := runRemotePage(t, store, books, rootDir)
			if n != 0 {
				t.Errorf("processed = %d, want 0", n)
			}
			sentinel := false
			for _, b := range *written {
				if b.IntroTranscription != nil && *b.IntroTranscription == silenceSentinel {
					sentinel = true
				}
			}
			if sentinel != tc.wantSentinel {
				t.Errorf("sentinel written = %v, want %v (writes: %d)", sentinel, tc.wantSentinel, len(*written))
			}
			if !tc.wantSentinel && len(*written) != 0 {
				t.Errorf("a failed silence retry wrote %d row(s); it must write nothing", len(*written))
			}
			if st := accum.snapshot(); st.Deferred != tc.wantDeferred {
				t.Errorf("Deferred = %d, want %d", st.Deferred, tc.wantDeferred)
			}
			noWrites()
		})
	}
}

// A silence retry that returns a real per-book verdict is not "failed": the
// verdict is kept and classified exactly like a first-pass worker error, so a
// book whose 300 s clip cannot be decoded gets ffmpeg_error instead of being
// deferred forever, and neither case writes the [SILENCE] marker.
func TestRemotePage_SilenceRetryVerdictIsClassifiedNotDeferred(t *testing.T) {
	// A constructor, not a shared map: the op stores retry results into the map
	// the first pass returned, so a shared one would leak between cases.
	silent := func() map[string]transcribe.BatchResult {
		return map[string]transcribe.BatchResult{"b1": {Text: ""}}
	}
	cases := []struct {
		name       string
		twoFiles   bool
		deleteSrc  bool // the server loses the file, so its own stat confirms not_found
		retry      func(call int) map[string]transcribe.BatchResult
		wantStatus string
	}{
		{"retry1 ffmpeg_error on the 300 s clip", false, false, func(int) map[string]transcribe.BatchResult {
			return map[string]transcribe.BatchResult{"b1": {Error: "ffmpeg_error: Invalid data found when processing input"}}
		}, statusFFmpegError},
		{"retry1 not_found confirmed by the server's own stat", false, true, func(int) map[string]transcribe.BatchResult {
			return map[string]transcribe.BatchResult{"b1": {Error: "not_found: file not found under the mapped root"}}
		}, statusSourceMissing},
		{"retry1 path_unmapped while the server can stat the file", false, false, func(int) map[string]transcribe.BatchResult {
			return map[string]transcribe.BatchResult{"b1": {Error: "path_unmapped: mapped root is not readable (mount lost?)"}}
		}, statusPathUnmapped},
		{"retry2 ffmpeg_timeout after an empty retry1", true, false, func(call int) map[string]transcribe.BatchResult {
			if call == 2 {
				return silent()
			}
			return map[string]transcribe.BatchResult{"b1": {Error: "ffmpeg_timeout: ffmpeg exceeded 120s"}}
		}, statusWhisperError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := []string{writeSource(t, "disc1.m4b")}
			if tc.twoFiles {
				files = append(files, writeSource(t, "disc2.m4b"))
			}
			store, written, books := remoteBookStore(t, map[string][]string{"b1": files})
			rootDir, noWrites := noClipWrites(t)
			var calls int
			swapPathSeams(t, true, "", func(context.Context, map[string]transcribe.PathJob, transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
				calls++
				if calls == 1 {
					return silent(), nil
				}
				if tc.deleteSrc {
					_ = os.Remove(files[0])
				}
				return tc.retry(calls), nil
			})
			n, accum := runRemotePage(t, store, books, rootDir)
			if n != 0 {
				t.Errorf("processed = %d, want 0", n)
			}
			if got := statusOf(t, written); got != tc.wantStatus {
				t.Errorf("status = %q, want %q", got, tc.wantStatus)
			}
			if b := (*written)[0]; b.IntroTranscription != nil && *b.IntroTranscription == silenceSentinel {
				t.Error("a retry verdict must not write the [SILENCE] sentinel")
			}
			if st := accum.snapshot(); st.Deferred != 0 {
				t.Errorf("Deferred = %d, want 0: the retry returned a verdict, nothing to defer", st.Deferred)
			}
			noWrites()
		})
	}
}

// ── run level ────────────────────────────────────────────────────────────────

func newRemoteRun(t *testing.T, srcs int) (*Plugin, *database.PebbleStore, []string) {
	t.Helper()
	s := openJournalPebble(t, t.TempDir())
	t.Cleanup(func() { _ = s.Close() })
	var ids []string
	for i := range srcs {
		b, err := s.CreateBook(&database.Book{Title: "Book " + string(rune('A'+i)), FilePath: writeSource(t, "book.m4b")})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, b.ID)
	}
	rootDir, _ := noClipWrites(t)
	return &Plugin{deps: rootDeps{fakeDeps: fakeDeps{store: s}, root: rootDir}}, s, ids
}

// The headline: on a host that may not decode (ALLOW_SERVER_DECODE unset, as in
// production) a live run succeeds through the worker instead of failing in
// 16 ms with "audio decoding is not allowed on this server".
func TestIntroTranscribe_LiveRunOnANoDecodeHostUsesTheWorker(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	p, s, ids := newRemoteRun(t, 2)
	var sent int
	swapPathSeams(t, true, "", func(_ context.Context, jobs map[string]transcribe.PathJob, _ transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
		out := map[string]transcribe.BatchResult{}
		for id := range jobs {
			sent++
			out[id] = transcribe.BatchResult{Text: "This is The Title by An Author"}
		}
		return out, nil
	})
	rep := &denomReporter{}
	if err := p.runIntroTranscribe(context.Background(), liveTranscribeParams(`{}`), rep); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if sent != 2 {
		t.Errorf("worker received %d jobs, want 2", sent)
	}
	for _, id := range ids {
		if b, _ := s.GetBookByID(id); b == nil || b.IntroTranscription == nil || *b.IntroTranscription == "" {
			t.Errorf("book %s has no transcript", id)
		}
	}
}

func TestIntroTranscribe_NoDecodeHostWithoutAPathWorkerStillRefuses(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	p, _, _ := newRemoteRun(t, 1)
	swapPathSeams(t, false, "no whisper endpoints configured", func(context.Context, map[string]transcribe.PathJob, transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
		t.Error("worker called although none advertises path_pipeline")
		return nil, nil
	})
	err := p.runIntroTranscribe(context.Background(), liveTranscribeParams(`{}`), &denomReporter{})
	if !errors.Is(err, serverdecode.ErrRefused) {
		t.Fatalf("err = %v, want serverdecode.ErrRefused", err)
	}
	if !strings.Contains(err.Error(), "no whisper endpoints configured") {
		t.Errorf("refusal should say why remote was not used: %v", err)
	}
}

func TestIntroTranscribe_ForcedRemoteWithNoPathWorkerFailsClosed(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "1") // decoding allowed: forced remote must still not fall back to it
	p, _, _ := newRemoteRun(t, 1)
	swapPathSeams(t, false, "all refused", func(context.Context, map[string]transcribe.PathJob, transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
		t.Error("worker called")
		return nil, nil
	})
	err := p.runIntroTranscribe(context.Background(), liveTranscribeParams(`{"remote_extract":true}`), &denomReporter{})
	if err == nil || !strings.Contains(err.Error(), "no whisper endpoint can serve path jobs") {
		t.Fatalf("err = %v", err)
	}
}

func TestIntroTranscribe_PreviewNamesTheExtractMode(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	p, _, _ := newRemoteRun(t, 1)
	swapPathSeams(t, true, "", func(context.Context, map[string]transcribe.PathJob, transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
		t.Error("a preview must not transcribe")
		return nil, nil
	})
	rep := &denomReporter{}
	if err := p.runIntroTranscribe(context.Background(), []byte(`{}`), rep); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.last, "Whisper worker") || !strings.HasPrefix(rep.last, introPreviewMarker) {
		t.Errorf("preview message = %q", rep.last)
	}
}
