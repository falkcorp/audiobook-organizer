// file: internal/server/batch_apply_one.go
// version: 1.1.0
// guid: 4e91c082-77a3-4d16-b5f8-2c0a9e3d4671
// last-edited: 2026-08-16

package server

import (
	"encoding/json"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// cachedApplyService is the narrow slice of *metafetch.Service that applying one
// cached candidate needs. It exists so the per-book logic can be tested against
// fakes without standing up a Server — the four regression tests that used to
// drive this through the HTTP handler now drive applyCachedCandidateForBook
// directly, so the behaviour they pin is still the behaviour production runs.
//
// That equivalence is the whole point of the interface. Before this extraction
// the logic lived inline in the gin handler; moving it to a background op
// without extracting it would have left the tests exercising a code path
// production no longer used.
type cachedApplyService interface {
	GetCachedCandidates(bookID string) (*metafetch.MetadataCandidateCache, bool, error)
	ApplyMetadataCandidate(id string, candidate metafetch.MetadataCandidate, fields []string) (*metafetch.FetchMetadataResponse, error)
	InvalidateCachedCandidates(bookID string) error
	ApplyMetadataFileIO(id string) error
	WriteBackMetadataForBook(id string, segmentFilter ...[]string) (int, error)
}

// applyBookReader is the single store read the per-book path needs.
type applyBookReader interface {
	GetBookByID(id string) (*database.Book, error)
}

// itunesEnqueuer mirrors handlers.WriteBackEnqueuer: the iTunes library sync
// batcher, which does NOT touch audio tags. Named explicitly here because
// confusing it for the tag writer is exactly the defect this path once had —
// metadata landed in the database, the iTunes batcher was enqueued, and no audio
// file was ever written.
type itunesEnqueuer interface {
	Enqueue(bookID string)
}

// applyOutcome is the result of applying one book's cached candidate.
type applyOutcome struct {
	Applied bool
	// Reason is set when Applied is false, using the batchSkip* vocabulary.
	Reason string
	Err    error
	// WriteBackFailed is true when the metadata WAS applied to the database but
	// writing it into the audio files failed. Deliberately separate from
	// !Applied: the database change is real and durable, and reporting the book
	// as "not applied" would send someone re-applying work that succeeded.
	WriteBackFailed bool
}

// Skip reason vocabulary, shared with the HTTP response shape in
// internal/server/handlers/metadata_cache.go.
const (
	applySkipNoCachedCandidates = "no_cached_candidates"
	applySkipDecodeFailed       = "decode_failed"
	applySkipApplyFailed        = "apply_failed"
)

// applyCachedCandidateForBook applies the highest-scored cached candidate for
// one book and, when writeBack is true, writes the result into the audio files.
//
// FILE I/O — KEEP IN STEP WITH THE SINGLE-BOOK PATH. The sibling is
// applyAudiobookMetadataImpl in internal/server/handlers/metadata/handler.go.
// The two drifted apart once: the sibling wrote tags and embedded cover art
// while this path only updated the database and enqueued the iTunes batcher, so
// applied metadata never reached the files and nothing logged a failure. If you
// add file-side work to either path, add it to both.
//
// The caller supplies concurrency and path locking; this function does the work
// for exactly one book and never spawns goroutines.
func applyCachedCandidateForBook(
	svc cachedApplyService,
	store applyBookReader,
	itunes itunesEnqueuer,
	id string,
	writeBack bool,
	lockPath func(path string) func(),
) applyOutcome {
	entry, _, err := svc.GetCachedCandidates(id)
	if err != nil || entry == nil || len(entry.Candidates) == 0 {
		return applyOutcome{Reason: applySkipNoCachedCandidates, Err: err}
	}

	var cand metafetch.MetadataCandidate
	if derr := json.Unmarshal(entry.Candidates[0], &cand); derr != nil {
		return applyOutcome{Reason: applySkipDecodeFailed, Err: derr}
	}

	if _, aerr := svc.ApplyMetadataCandidate(id, cand, nil); aerr != nil {
		return applyOutcome{Reason: applySkipApplyFailed, Err: aerr}
	}
	_ = svc.InvalidateCachedCandidates(id)

	if !writeBack {
		return applyOutcome{Applied: true}
	}

	// iTunes library sync is enqueued BEFORE the file work (matching the
	// single-book path) so a failure writing tags cannot lose it.
	if itunes != nil {
		itunes.Enqueue(id)
	}

	// Re-read the book: ApplyMetadataCandidate just rewrote its row, and the
	// file path we lock and write must be the post-apply one.
	book, berr := store.GetBookByID(id)
	if berr != nil || book == nil {
		return applyOutcome{Applied: true, WriteBackFailed: true, Err: berr}
	}

	if lockPath != nil {
		release := lockPath(book.FilePath)
		defer release()
	}
	// The rename lives in here, and its failure used to be unreachable: this
	// call returned nothing, so the outcome below said Applied:true whether or
	// not a single file had moved. Applied stays true — the database change is
	// real and durable — but the file side is now flagged, which is exactly why
	// WriteBackFailed is separate from !Applied.
	//
	// The write-back below still runs on a file-I/O failure. Skipping it would
	// be a behaviour change beyond reporting: tag writing is independent of the
	// rename (correct tags in a file that did not move are still correct), and
	// it ran unconditionally before this call could report anything. Only the
	// error we surface changes — fileErr wins because "rename failed" localises
	// the fault better than the write-back error it would otherwise cause.
	fileErr := svc.ApplyMetadataFileIO(id)
	_, wberr := svc.WriteBackMetadataForBook(id)
	if fileErr != nil {
		return applyOutcome{Applied: true, WriteBackFailed: true, Err: fileErr}
	}
	if wberr != nil {
		return applyOutcome{Applied: true, WriteBackFailed: true, Err: wberr}
	}
	return applyOutcome{Applied: true}
}
