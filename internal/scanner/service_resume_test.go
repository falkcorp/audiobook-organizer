// file: internal/scanner/service_resume_test.go
// version: 1.0.0
// guid: 2b7c9e14-6a83-4f52-b0d7-8e41c6a9d375
// last-edited: 2026-08-17

package scanner

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func booksN(n int) []Book {
	out := make([]Book, n)
	for i := range out {
		out[i] = Book{FilePath: fmt.Sprintf("/lib/%06d.m4b", i)}
	}
	return out
}

// TestProcessBookChunks_CheckpointsAbsoluteOffsets is the contract the resume
// path depends on: each checkpoint must name the absolute position in the folder,
// because that value is fed straight back as ResumeItemOffset next run.
func TestProcessBookChunks_CheckpointsAbsoluteOffsets(t *testing.T) {
	ss := &ScanService{}
	books := booksN(scanChunkSize*2 + 37)

	var marks []int
	err := ss.processBookChunks(context.Background(), books, 0, 3,
		func(_ int, off int) { marks = append(marks, off) },
		func(_ context.Context, _ []Book) error { return nil },
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []int{scanChunkSize, scanChunkSize * 2, len(books)}
	if len(marks) != len(want) {
		t.Fatalf("expected %d checkpoints, got %d (%v)", len(want), len(marks), marks)
	}
	for i := range want {
		if marks[i] != want[i] {
			t.Fatalf("checkpoint %d: got %d, want %d (all: %v)", i, marks[i], want[i], marks)
		}
	}
}

// TestProcessBookChunks_ResumeSkipsPrefix verifies the offset actually skips work
// rather than merely relabelling it.
func TestProcessBookChunks_ResumeSkipsPrefix(t *testing.T) {
	ss := &ScanService{}
	books := booksN(scanChunkSize * 4)
	start := scanChunkSize * 2

	var processed int
	var firstSeen string
	err := ss.processBookChunks(context.Background(), books, start, 0, nil,
		func(_ context.Context, chunk []Book) error {
			if processed == 0 && len(chunk) > 0 {
				firstSeen = chunk[0].FilePath
			}
			processed += len(chunk)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed != len(books)-start {
		t.Fatalf("expected %d books processed after resuming at %d, got %d",
			len(books)-start, start, processed)
	}
	if want := books[start].FilePath; firstSeen != want {
		t.Fatalf("resume started at %q, want %q", firstSeen, want)
	}
}

// TestProcessBookChunks_FailedChunkIsNotCheckpointed is the safety property. If a
// chunk errors and we checkpointed past it anyway, a resume would step over books
// that were never processed and nothing would ever revisit them.
func TestProcessBookChunks_FailedChunkIsNotCheckpointed(t *testing.T) {
	ss := &ScanService{}
	books := booksN(scanChunkSize * 3)

	var marks []int
	calls := 0
	err := ss.processBookChunks(context.Background(), books, 0, 0,
		func(_ int, off int) { marks = append(marks, off) },
		func(_ context.Context, _ []Book) error {
			calls++
			if calls == 2 {
				return errors.New("chunk 2 exploded")
			}
			return nil
		},
	)
	if err == nil {
		t.Fatal("expected the failing chunk to surface an error")
	}
	if len(marks) != 1 || marks[0] != scanChunkSize {
		t.Fatalf("only the FIRST chunk should be checkpointed; got %v", marks)
	}
}

// TestProcessBookChunks_ResumeBeyondEndDoesNothing covers a stale checkpoint
// pointing past the current book count (files removed since the previous run).
func TestProcessBookChunks_ResumeBeyondEndDoesNothing(t *testing.T) {
	ss := &ScanService{}
	books := booksN(10)
	ran := 0
	err := ss.processBookChunks(context.Background(), books, 999, 0, nil,
		func(_ context.Context, chunk []Book) error { ran += len(chunk); return nil },
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ran != 0 {
		t.Fatalf("a stale offset past the end must process nothing, processed %d", ran)
	}
}

// TestProcessBookChunks_CancelStopsBetweenChunks proves cancellation is honoured
// at a chunk boundary rather than running the whole folder to completion.
func TestProcessBookChunks_CancelStopsBetweenChunks(t *testing.T) {
	ss := &ScanService{}
	books := booksN(scanChunkSize * 5)

	ctx, cancel := context.WithCancel(context.Background())
	chunks := 0
	err := ss.processBookChunks(ctx, books, 0, 0, nil,
		func(_ context.Context, _ []Book) error {
			chunks++
			if chunks == 2 {
				cancel()
			}
			return nil
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if chunks != 2 {
		t.Fatalf("expected to stop after the cancelling chunk, ran %d chunks", chunks)
	}
}

// TestProcessBookChunks_NoCheckpointFnIsSafe pins that the feature is optional.
func TestProcessBookChunks_NoCheckpointFnIsSafe(t *testing.T) {
	ss := &ScanService{}
	books := booksN(scanChunkSize + 1)
	processed := 0
	err := ss.processBookChunks(context.Background(), books, 0, 0, nil,
		func(_ context.Context, chunk []Book) error { processed += len(chunk); return nil },
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed != len(books) {
		t.Fatalf("expected all %d books processed, got %d", len(books), processed)
	}
}
