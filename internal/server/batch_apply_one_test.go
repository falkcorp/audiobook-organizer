// file: internal/server/batch_apply_one_test.go
// version: 1.3.0
// guid: 9d2b71fa-30c8-4e57-a614-8b5e0c7f2d93
// last-edited: 2026-09-12
//
// Regression tests for applying ONE book's cached metadata candidate.
//
// These moved here from internal/server/handlers/metadata_cache_test.go when
// the batch apply became a background op. They pin the same three defects the
// handler tests pinned, against the same code — the logic was EXTRACTED rather
// than reimplemented, so these still cover the path production runs. Testing
// the handler instead would now only prove that an op was enqueued.

package server

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// finishCall is one FinishApplyFileWork invocation.
type finishCall struct {
	id        string
	cover     string
	fileIO    bool
	writeTags bool
}

// fakeApplySvc records which of the file-side calls were made.
type fakeApplySvc struct {
	candidates    []json.RawMessage
	getErr        error
	applyErr      error
	finishErr     error
	pendingCover  string
	skippedLocked []string
	appliedIDs    []string
	invalidatedID []string
	finishCalls   []finishCall
}

func (f *fakeApplySvc) GetCachedCandidates(bookID string) (*metafetch.MetadataCandidateCache, bool, error) {
	if f.getErr != nil {
		return nil, false, f.getErr
	}
	if len(f.candidates) == 0 {
		return nil, false, nil
	}
	return &metafetch.MetadataCandidateCache{Candidates: f.candidates}, true, nil
}

func (f *fakeApplySvc) ApplyMetadataCandidate(id string, _ metafetch.MetadataCandidate, _ []string) (*metafetch.FetchMetadataResponse, error) {
	if f.applyErr != nil {
		return nil, f.applyErr
	}
	f.appliedIDs = append(f.appliedIDs, id)
	return &metafetch.FetchMetadataResponse{SkippedLockedFields: f.skippedLocked, PendingCoverURL: f.pendingCover}, nil
}

func (f *fakeApplySvc) InvalidateCachedCandidates(bookID string) error {
	f.invalidatedID = append(f.invalidatedID, bookID)
	return nil
}

func (f *fakeApplySvc) FinishApplyFileWork(id, pendingCoverURL string, fileIO, writeTags bool) error {
	f.finishCalls = append(f.finishCalls, finishCall{id: id, cover: pendingCoverURL, fileIO: fileIO, writeTags: writeTags})
	return f.finishErr
}

type fakeBookReader struct {
	book *database.Book
	err  error
}

func (f *fakeBookReader) GetBookByID(id string) (*database.Book, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.book, nil
}

type fakeITunes struct{ ids []string }

func (f *fakeITunes) Enqueue(bookID string) { f.ids = append(f.ids, bookID) }

func oneCandidate(t *testing.T) []json.RawMessage {
	t.Helper()
	blob, err := json.Marshal(metafetch.MetadataCandidate{Title: "A Title", Author: "An Author"})
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}
	return []json.RawMessage{blob}
}

// TestApplyCachedCandidate_WritesFilesForAppliedBook is the regression test for
// the original defect: metadata landed in the database and the iTunes batcher
// was enqueued, but no audio file was ever written and nothing logged a
// failure. It looked like success.
func TestApplyCachedCandidate_WritesFilesForAppliedBook(t *testing.T) {
	svc := &fakeApplySvc{candidates: oneCandidate(t)}
	store := &fakeBookReader{book: &database.Book{ID: "b1", Title: "A Title", FilePath: "/lib/a.m4b"}}
	itunes := &fakeITunes{}

	out := applyCachedCandidateForBook(svc, store, itunes, "b1", true, nil)

	if !out.Applied || out.WriteBackFailed {
		t.Fatalf("expected clean apply, got %+v", out)
	}
	// Exactly ONE file-side sequel, asking for file I/O and tags. The core
	// itself guarantees the tags are written once (TestFinishApplyFileWork_*
	// in internal/metafetch); this path used to call ApplyMetadataFileIO and
	// then WriteBackMetadataForBook, which tagged twice.
	want := []finishCall{{id: "b1", fileIO: true, writeTags: true}}
	if !reflect.DeepEqual(svc.finishCalls, want) {
		t.Errorf("file work = %+v, want %+v", svc.finishCalls, want)
	}
	if len(itunes.ids) != 1 {
		t.Errorf("iTunes batcher not enqueued: %v", itunes.ids)
	}
	if len(svc.invalidatedID) != 1 {
		t.Errorf("cache not invalidated: %v", svc.invalidatedID)
	}
}

// TestApplyCachedCandidate_WriteBackFalseSuppressesFileIO pins the opt-out:
// write_back=false must change the database and touch NO file.
func TestApplyCachedCandidate_WriteBackFalseSuppressesFileIO(t *testing.T) {
	svc := &fakeApplySvc{candidates: oneCandidate(t)}
	store := &fakeBookReader{book: &database.Book{ID: "b1", FilePath: "/lib/a.m4b"}}
	itunes := &fakeITunes{}

	out := applyCachedCandidateForBook(svc, store, itunes, "b1", false, nil)

	if !out.Applied {
		t.Fatalf("expected applied, got %+v", out)
	}
	// The only file-side call allowed is the cover download: no file I/O,
	// no tags.
	want := []finishCall{{id: "b1", fileIO: false, writeTags: false}}
	if !reflect.DeepEqual(svc.finishCalls, want) {
		t.Errorf("file work ran despite write_back=false: %+v, want %+v", svc.finishCalls, want)
	}
	if len(itunes.ids) != 0 {
		t.Errorf("iTunes enqueued despite write_back=false: %v", itunes.ids)
	}
}

// TestApplyCachedCandidate_ReportsSkipReasons covers the second defect: a book
// with nothing cached must report WHY rather than being silently counted as
// applied.
func TestApplyCachedCandidate_ReportsSkipReasons(t *testing.T) {
	tests := []struct {
		name       string
		svc        *fakeApplySvc
		wantReason string
	}{
		{"no cached candidates", &fakeApplySvc{}, applySkipNoCachedCandidates},
		{"cache read failed", &fakeApplySvc{getErr: errors.New("boom")}, applySkipNoCachedCandidates},
		{"undecodable candidate", &fakeApplySvc{candidates: []json.RawMessage{[]byte("{not json")}}, applySkipDecodeFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := applyCachedCandidateForBook(tt.svc, &fakeBookReader{}, &fakeITunes{}, "b1", true, nil)
			if out.Applied {
				t.Fatalf("expected not applied, got %+v", out)
			}
			if out.Reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", out.Reason, tt.wantReason)
			}
			if len(tt.svc.finishCalls) != 0 {
				t.Errorf("file work ran for a skipped book: %+v", tt.svc.finishCalls)
			}
		})
	}
}

// TestApplyCachedCandidate_ApplyFailureIsNotReportedAsApplied guards the
// distinction the response vocabulary depends on.
func TestApplyCachedCandidate_ApplyFailureIsNotReportedAsApplied(t *testing.T) {
	svc := &fakeApplySvc{candidates: oneCandidate(t), applyErr: errors.New("apply exploded")}

	out := applyCachedCandidateForBook(svc, &fakeBookReader{}, &fakeITunes{}, "b1", true, nil)

	if out.Applied {
		t.Fatalf("apply failed but was reported applied: %+v", out)
	}
	if out.Reason != applySkipApplyFailed {
		t.Errorf("reason = %q, want %q", out.Reason, applySkipApplyFailed)
	}
	if len(svc.finishCalls) != 0 {
		t.Errorf("wrote files for a book whose apply failed: %+v", svc.finishCalls)
	}
}

// TestApplyCachedCandidate_WriteBackFailureStaysApplied is the honesty check.
// The database change is real and durable even when writing the audio files
// fails, so the book must NOT be reported as unapplied — that would send
// someone re-applying work that already succeeded.
func TestApplyCachedCandidate_WriteBackFailureStaysApplied(t *testing.T) {
	svc := &fakeApplySvc{candidates: oneCandidate(t), finishErr: errors.New("disk full")}
	store := &fakeBookReader{book: &database.Book{ID: "b1", FilePath: "/lib/a.m4b"}}

	out := applyCachedCandidateForBook(svc, store, &fakeITunes{}, "b1", true, nil)

	if !out.Applied {
		t.Fatalf("write-back failure must not unset Applied: %+v", out)
	}
	if !out.WriteBackFailed {
		t.Errorf("WriteBackFailed not set despite a write-back error")
	}
}

// TestApplyCachedCandidate_FileIOFailureIsReported is the regression this whole
// change exists for. ApplyMetadataFileIO — which is where the RENAME happens —
// used to return nothing, so a failed rename was unreachable to this function
// and the outcome said Applied:true with WriteBackFailed:false. The API reported
// a clean apply while the files had never moved.
//
// Applied must stay true (the database row really was written) and
// WriteBackFailed must now be set, so the batch op counts and logs it.
func TestApplyCachedCandidate_FileIOFailureIsReported(t *testing.T) {
	svc := &fakeApplySvc{candidates: oneCandidate(t), finishErr: errors.New("rename files: cross-device link")}
	store := &fakeBookReader{book: &database.Book{ID: "b1", FilePath: "/lib/a.m4b"}}

	out := applyCachedCandidateForBook(svc, store, &fakeITunes{}, "b1", true, nil)

	if !out.Applied {
		t.Fatalf("a file-I/O failure must not unset Applied: %+v", out)
	}
	if !out.WriteBackFailed {
		t.Fatalf("WriteBackFailed not set despite ApplyMetadataFileIO failing: %+v", out)
	}
	if out.Err == nil || !strings.Contains(out.Err.Error(), "cross-device link") {
		t.Errorf("Err = %v, want the file-I/O error surfaced", out.Err)
	}
}

// The "tags still written after a rename failure" and "rename error wins over
// the tag error" guarantees moved with the logic into the shared core: see
// TestFinishApplyFileWork_RenameFailureStillWritesTags in internal/metafetch.

// TestApplyCachedCandidate_DownloadsPendingCover is the regression test for the
// batch path never fetching the new cover. ApplyMetadataCandidate keeps serving
// the previous cover until the image is on disk, and only the single-book
// handler ever downloaded it, so a batch-applied book kept its old cover
// forever. The cover must reach the core with and without write-back.
func TestApplyCachedCandidate_DownloadsPendingCover(t *testing.T) {
	const cover = "https://covers.example.test/new.jpg"
	store := &fakeBookReader{book: &database.Book{ID: "b1", FilePath: "/lib/a.m4b"}}
	for _, writeBack := range []bool{true, false} {
		svc := &fakeApplySvc{candidates: oneCandidate(t), pendingCover: cover}
		_ = applyCachedCandidateForBook(svc, store, &fakeITunes{}, "b1", writeBack, nil)
		if len(svc.finishCalls) != 1 || svc.finishCalls[0].cover != cover {
			t.Errorf("writeBack=%v: file work = %+v, want one call carrying the pending cover %q",
				writeBack, svc.finishCalls, cover)
		}
	}
}

// TestApplyCachedCandidate_TakesPathLockOnPostApplyPath pins that the lock is
// taken on the path read AFTER the apply, not a stale one. ApplyMetadataCandidate
// can rewrite the book's row, and locking the pre-apply path would serialize
// against the wrong key.
func TestApplyCachedCandidate_TakesPathLockOnPostApplyPath(t *testing.T) {
	svc := &fakeApplySvc{candidates: oneCandidate(t)}
	store := &fakeBookReader{book: &database.Book{ID: "b1", FilePath: "/lib/new-location.m4b"}}

	var locked []string
	lock := func(p string) func() {
		locked = append(locked, p)
		return func() {}
	}

	out := applyCachedCandidateForBook(svc, store, &fakeITunes{}, "b1", true, lock)

	if !out.Applied {
		t.Fatalf("expected applied, got %+v", out)
	}
	if len(locked) != 1 || locked[0] != "/lib/new-location.m4b" {
		t.Errorf("locked %v, want the post-apply path", locked)
	}
}

// TestApplyCachedCandidate_ReportsSkippedLockedFields pins that the lock keys
// the apply refused to overwrite reach the outcome on EVERY applied return --
// the plain one, and the write-back-failure ones that used to be built from
// scratch and would have dropped the list. The op summary counts these; an
// outcome that lost them would read "applied" over a silently kept title.
func TestApplyCachedCandidate_ReportsSkippedLockedFields(t *testing.T) {
	locked := []string{database.FieldKeyTitle, database.FieldKeyAuthorName}
	store := &fakeBookReader{book: &database.Book{ID: "b1", FilePath: "/lib/a.m4b"}}

	t.Run("no write-back", func(t *testing.T) {
		svc := &fakeApplySvc{candidates: oneCandidate(t), skippedLocked: locked}
		out := applyCachedCandidateForBook(svc, store, nil, "b1", false, nil)
		if !out.Applied {
			t.Fatalf("expected Applied: %+v", out)
		}
		if !reflect.DeepEqual(out.SkippedLocked, locked) {
			t.Errorf("SkippedLocked = %v, want %v", out.SkippedLocked, locked)
		}
	})

	t.Run("write-back succeeds", func(t *testing.T) {
		svc := &fakeApplySvc{candidates: oneCandidate(t), skippedLocked: locked}
		out := applyCachedCandidateForBook(svc, store, &fakeITunes{}, "b1", true, nil)
		if !out.Applied || out.WriteBackFailed {
			t.Fatalf("expected a clean apply: %+v", out)
		}
		if !reflect.DeepEqual(out.SkippedLocked, locked) {
			t.Errorf("SkippedLocked = %v, want %v", out.SkippedLocked, locked)
		}
	})

	t.Run("write-back fails", func(t *testing.T) {
		svc := &fakeApplySvc{candidates: oneCandidate(t), skippedLocked: locked, finishErr: errors.New("disk full")}
		out := applyCachedCandidateForBook(svc, store, &fakeITunes{}, "b1", true, nil)
		if !out.Applied || !out.WriteBackFailed {
			t.Fatalf("expected applied with write-back failure: %+v", out)
		}
		if !reflect.DeepEqual(out.SkippedLocked, locked) {
			t.Errorf("a write-back failure dropped the skipped locks: %v, want %v", out.SkippedLocked, locked)
		}
	})

	t.Run("nothing locked", func(t *testing.T) {
		svc := &fakeApplySvc{candidates: oneCandidate(t)}
		out := applyCachedCandidateForBook(svc, store, nil, "b1", false, nil)
		if len(out.SkippedLocked) != 0 {
			t.Errorf("SkippedLocked = %v, want none", out.SkippedLocked)
		}
	})
}
