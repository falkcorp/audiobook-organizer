// file: internal/plugins/maintenance/intro_transcribe_transport_test.go
// version: 1.0.0
// guid: 7f2b4a8d-9c3e-4d15-b6a2-0e8f1c5d7a93
// last-edited: 2026-08-07

package maintenance

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
)

// transportTestFixture wires up everything processTranscribePage needs to reach
// the TranscribeBatch call without ffmpeg or a live endpoint:
//   - a pre-populated WAV clip cache so the extraction step is a cache hit
//     (the cache-hit path runs before the source-file stat, so the source
//     path never needs to exist),
//   - a MockStore whose GetBookFiles returns one audio file with a FileHash
//     matching the cached clip, and whose UpdateBook captures every write.
func transportTestFixture(t *testing.T, bookID, fileHash string) (*database.MockStore, *[]database.Book, []database.Book, string) {
	t.Helper()

	cacheDir := t.TempDir()
	t.Setenv("WHISPER_CLIP_CACHE_DIR", cacheDir)
	clip := cachedClipPath(cacheDir, fileHash)
	if err := os.WriteFile(clip, []byte("RIFF fake wav"), 0o644); err != nil {
		t.Fatalf("write cached clip: %v", err)
	}

	written := &[]database.Book{}
	store := &database.MockStore{
		GetBookFilesFunc: func(id string) ([]database.BookFile, error) {
			return []database.BookFile{{
				ID:       "f-" + id,
				BookID:   id,
				FilePath: filepath.Join(t.TempDir(), "does-not-exist.m4b"),
				FileHash: fileHash,
			}}, nil
		},
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) {
			*written = append(*written, *b)
			return b, nil
		},
	}
	books := []database.Book{{ID: bookID}}
	return store, written, books, t.TempDir() // last value = rootDir (cache env overrides it)
}

// swapTranscribeBatchFn substitutes the transcribeBatchFn test seam and
// restores it on cleanup. Tests using it must not run in parallel.
func swapTranscribeBatchFn(t *testing.T, fn func(context.Context, map[string]string, transcribe.ProgressFunc) (map[string]transcribe.BatchResult, error)) {
	t.Helper()
	orig := transcribeBatchFn
	transcribeBatchFn = fn
	t.Cleanup(func() { transcribeBatchFn = orig })
}

// TestProcessTranscribePage_TransportErrorWritesNoStatus is the regression test
// for the 2026-07-01 outage: when TranscribeBatch fails with a
// *transcribe.TransportError (endpoint dead, request never reached a model),
// processTranscribePage must write ZERO TranscribeStatus rows — the page is
// deferred and retried next run, never recorded as ~200 false whisper_errors.
func TestProcessTranscribePage_TransportErrorWritesNoStatus(t *testing.T) {
	store, written, books, rootDir := transportTestFixture(t, "b-transport", "hash-transport")
	var calls int
	swapTranscribeBatchFn(t, func(_ context.Context, jobs map[string]string, _ transcribe.ProgressFunc) (map[string]transcribe.BatchResult, error) {
		calls++
		if len(jobs) != 1 {
			t.Errorf("expected 1 job to reach the batch call, got %d", len(jobs))
		}
		return nil, &transcribe.TransportError{
			Endpoints:  []string{"http://whisper-a.test", "http://whisper-b.test"},
			Err:        errors.New("dial tcp: connection refused"),
			Recognized: true,
		}
	})

	p := New(fakeDeps{store: store})
	accum := newTranscribeStatsAccum(nil, "op", 0, time.Now())
	processed := p.processTranscribePage(context.Background(), store, slog.Default(),
		books, rootDir, nil, accum, false)

	if calls != 1 {
		t.Fatalf("TranscribeBatch stub called %d times, want 1", calls)
	}
	if processed != 0 {
		t.Errorf("processed = %d, want 0", processed)
	}
	if len(*written) != 0 {
		t.Fatalf("transport error must write ZERO TranscribeStatus rows, got %d UpdateBook calls; first: %+v",
			len(*written), (*written)[0])
	}
}

// TestProcessTranscribePage_PerFileErrorStillWritesWhisperError is the
// no-over-correction guard: a per-file failure reported INSIDE a successful
// batch (BatchResult{Error: "boom"}) is a genuine per-file verdict and must
// still be written as whisper_error for that book.
func TestProcessTranscribePage_PerFileErrorStillWritesWhisperError(t *testing.T) {
	store, written, books, rootDir := transportTestFixture(t, "b-perfile", "hash-perfile")
	swapTranscribeBatchFn(t, func(_ context.Context, jobs map[string]string, _ transcribe.ProgressFunc) (map[string]transcribe.BatchResult, error) {
		out := make(map[string]transcribe.BatchResult, len(jobs))
		for id := range jobs {
			out[id] = transcribe.BatchResult{Error: "boom"}
		}
		return out, nil
	})

	p := New(fakeDeps{store: store})
	accum := newTranscribeStatsAccum(nil, "op", 0, time.Now())
	processed := p.processTranscribePage(context.Background(), store, slog.Default(),
		books, rootDir, nil, accum, false)

	if processed != 0 {
		t.Errorf("processed = %d, want 0 (whisper_error does not count as processed)", processed)
	}
	if len(*written) != 1 {
		t.Fatalf("expected exactly 1 UpdateBook for the per-file error, got %d", len(*written))
	}
	w := (*written)[0]
	if w.TranscribeStatus == nil || *w.TranscribeStatus != statusWhisperError {
		t.Errorf("TranscribeStatus = %v, want %q", w.TranscribeStatus, statusWhisperError)
	}
	if w.TranscribeError == nil || *w.TranscribeError != "boom" {
		t.Errorf("TranscribeError = %v, want \"boom\"", w.TranscribeError)
	}
}
