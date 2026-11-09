// file: internal/reconcile/itunes_heal_transcript_test.go
// version: 1.0.1
// guid: 5c0f6a52-9a4e-4d57-8f0b-6f0a2b7f1c11
// last-edited: 2026-11-09

package reconcile

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
)

const (
	healYearOneIntro   = "Year One by Bruce Wayne. Read by Alfred Pennyworth."
	healHalloweenIntro = "The Long Halloween by Harvey Dent. Read by Selina Kyle."
)

// healTranscriptStore serves two single-file books, /a.m4b (book A) and
// /b.m4b (book B), with the given stored intro transcripts at book level.
func healTranscriptStore(a, b *string) *database.MockStore {
	m := &database.MockStore{}
	bookOf := func(p string) string {
		if p == "/b.m4b" {
			return "B"
		}
		return "A"
	}
	pathOf := func(id string) string {
		if id == "B" {
			return "/b.m4b"
		}
		return "/a.m4b"
	}
	m.GetBookFileByPathFunc = func(p string) (*database.BookFile, error) {
		return &database.BookFile{ID: "f" + bookOf(p), BookID: bookOf(p), FilePath: p}, nil
	}
	m.GetBookFilesFunc = func(id string) ([]database.BookFile, error) {
		return []database.BookFile{{ID: "f" + id, BookID: id, FilePath: pathOf(id)}}, nil
	}
	m.GetBookByIDFunc = func(id string) (*database.Book, error) {
		bk := &database.Book{ID: id}
		if id == "B" {
			bk.IntroTranscription = b
		} else {
			bk.IntroTranscription = a
		}
		return bk, nil
	}
	return m
}

func healStr(s string) *string { return &s }

func healYearOneTrack() iTunesTrack {
	return iTunesTrack{Album: "Year One", Artist: "Bruce Wayne"}
}

func withClipSeam(t *testing.T, fn func(ctx context.Context, path string, j transcribe.ResultJournal) (string, error)) {
	t.Helper()
	old := transcribeClipFn
	transcribeClipFn = fn
	t.Cleanup(func() { transcribeClipFn = old })
}

func TestHealTranscription_StoredTranscriptUsedWithoutPool(t *testing.T) {
	var calls atomic.Int32
	withClipSeam(t, func(context.Context, string, transcribe.ResultJournal) (string, error) {
		calls.Add(1)
		return "", errors.New("pool must not be called")
	})
	m := healTranscriptStore(healStr(healYearOneIntro), healStr(healHalloweenIntro))
	var f resolverFailureCounters
	got := resolveAmbiguousByTranscription(context.Background(), m, healYearOneTrack(), []string{"/a.m4b", "/b.m4b"}, &f, nil)
	if got != "/a.m4b" {
		t.Fatalf("winner = %q, want /a.m4b", got)
	}
	if calls.Load() != 0 {
		t.Fatalf("pool called %d times despite stored transcripts", calls.Load())
	}
	if f.whisperFailed.Load() != 0 {
		t.Fatalf("whisperFailed = %d, want 0", f.whisperFailed.Load())
	}
}

func TestHealTranscription_SilenceSentinelIsNotTextAndSkipsPool(t *testing.T) {
	var calls atomic.Int32
	withClipSeam(t, func(context.Context, string, transcribe.ResultJournal) (string, error) {
		calls.Add(1)
		return healYearOneIntro, nil
	})
	m := healTranscriptStore(healStr(transcribe.SilenceSentinel), healStr(healHalloweenIntro))
	var f resolverFailureCounters
	got := resolveAmbiguousByTranscription(context.Background(), m, healYearOneTrack(), []string{"/a.m4b", "/b.m4b"}, &f, nil)
	if got != "" {
		t.Fatalf("sentinel candidate must not win; got %q", got)
	}
	if calls.Load() != 0 {
		t.Fatalf("pool called %d times for a known-silent candidate", calls.Load())
	}
	if f.whisperFailed.Load() != 0 {
		t.Fatalf("a known-silent candidate is not a pool failure; whisperFailed = %d", f.whisperFailed.Load())
	}
	if text, silent := storedIntroText(nil, &database.Book{IntroTranscription: healStr(transcribe.SilenceSentinel)}); text != "" || !silent {
		t.Fatalf("storedIntroText(sentinel) = %q, %v", text, silent)
	}
}

func TestHealTranscription_NoStoredTranscriptCallsPool(t *testing.T) {
	var paths []string
	var gotJournal transcribe.ResultJournal
	withClipSeam(t, func(_ context.Context, p string, j transcribe.ResultJournal) (string, error) {
		paths = append(paths, p)
		gotJournal = j
		return healYearOneIntro, nil
	})
	m := healTranscriptStore(nil, healStr(""))
	var f resolverFailureCounters
	got := resolveAmbiguousByTranscription(context.Background(), m, healYearOneTrack(), []string{"/a.m4b"}, &f, nil)
	if got != "/a.m4b" {
		t.Fatalf("winner = %q, want /a.m4b", got)
	}
	if len(paths) != 1 || paths[0] != "/a.m4b" {
		t.Fatalf("pool calls = %v, want exactly /a.m4b", paths)
	}
	if gotJournal != nil {
		t.Fatalf("journal passed through = %v, want the nil we gave", gotJournal)
	}
}

func TestHealTranscription_PoolErrorCountsWhisperFailedAndSkips(t *testing.T) {
	withClipSeam(t, func(context.Context, string, transcribe.ResultJournal) (string, error) {
		return "", &transcribe.TransportError{Err: transcribe.ErrPoolUnavailable}
	})
	m := healTranscriptStore(nil, nil)
	var f resolverFailureCounters
	got := resolveAmbiguousByTranscription(context.Background(), m, healYearOneTrack(), []string{"/a.m4b", "/b.m4b"}, &f, nil)
	if got != "" {
		t.Fatalf("pool outage produced a verdict %q", got)
	}
	if f.whisperFailed.Load() != 2 {
		t.Fatalf("whisperFailed = %d, want 2", f.whisperFailed.Load())
	}
}

func TestStoredIntroText_FileRowNeedsMatchingHash(t *testing.T) {
	row := &database.BookFile{FileHash: "h1", IntroSourceHash: "h0", IntroTranscription: healStr(healHalloweenIntro)}
	if text, _ := storedIntroText(row, nil); text != "" {
		t.Fatalf("stale file transcript used: %q", text)
	}
	row.IntroSourceHash = "h1"
	if text, _ := storedIntroText(row, nil); text != healHalloweenIntro {
		t.Fatalf("fresh file transcript = %q", text)
	}
}
