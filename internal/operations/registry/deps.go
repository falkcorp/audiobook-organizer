// file: internal/operations/registry/deps.go
// version: 2.1.0
// guid: f2a3b4c5-d6e7-8f9a-0b1c-2d3e4f5a6b7c
// last-edited: 2026-09-19

// deps.go implements the UOS M1/M2 requirement evaluator, AllSatisfied aggregator,
// and cycle-detection guard for OperationDef.Requires graphs.
//
// Design constraints:
//   - Pure evaluator: no I/O side-effects, no goroutines, no locks.
//   - DepStore is a narrow interface (5 methods) so the evaluator is testable
//     with a fake without importing PebbleDB.
//   - database.OpSubject is the persisted shape; registry.Subject is the
//     in-process shape. Conversion happens at the call boundary in this file.
//   - ReqFieldSet (M2): load the subject book via GetBookByID and check one of
//     the allow-listed fields is non-empty. Unknown field names return an error
//     so typos in a Requirement are caught immediately rather than silently
//     evaluating to false.

package registry

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// DepStore is the narrow persistence interface the evaluator needs.
// It is satisfied by *database.PebbleStore in production and by fakeDepStore
// in tests. The full database.OpsV2Store interface is not required here.
type DepStore interface {
	// GetDepRev returns the current freshness counter for sub.
	// Returns 0 and no error when no counter exists yet.
	GetDepRev(sub database.OpSubject) (uint64, error)

	// GetOpCompletion returns the dep_rev at which opType last completed for
	// sub at the book level. ok=false when no record exists.
	GetOpCompletion(sub database.OpSubject, opType string) (rev uint64, ok bool, err error)

	// ListFileCompletions returns a map of fileID→depRev for all per-file
	// completion records of opType on sub.
	ListFileCompletions(sub database.OpSubject, opType string) (map[string]uint64, error)

	// BookFiles returns the list of file IDs belonging to bookID.
	// Used when evaluating AllFiles=true requirements.
	// Implementations may return nil, nil when book-file enumeration is not
	// available (e.g. early-startup or M1 enqueue path) — the evaluator
	// treats an empty list as "no files known → unmet".
	BookFiles(bookID string) ([]string, error)

	// GetBookByID returns the book with the given ID, or (nil, nil) when no
	// such book exists. Used by ReqFieldSet evaluation (M2).
	GetBookByID(id string) (*database.Book, error)
}

// OpsV2DepAdapter wraps a database.OpsV2Store to satisfy DepStore.
// BookFiles always returns nil (no file enumeration in the OpsV2Store
// interface). Use this adapter for the enqueue parking path where AllFiles
// evaluation is not needed; wire a real BookFiles provider for Task 5+.
//
// GetBookByID always returns (nil, nil): ReqFieldSet requirements evaluated
// through this adapter are treated as unmet (conservative). The periodic sweep
// via DepsScheduler.SweepTick uses a SchedulerStore that includes a real book
// source, so field_set conditions are eventually satisfied.
type OpsV2DepAdapter struct {
	database.OpsV2Store
}

// BookFiles satisfies DepStore. Returns nil so AllFiles requirements are
// treated as unmet (conservative) when no file source is wired.
func (a OpsV2DepAdapter) BookFiles(_ string) ([]string, error) {
	return nil, nil
}

// GetBookByID satisfies DepStore. Returns (nil, nil) so ReqFieldSet
// requirements are treated as unmet (conservative) at enqueue time when no
// book source is wired. The sweep re-evaluates once the book is available.
func (a OpsV2DepAdapter) GetBookByID(_ string) (*database.Book, error) {
	return nil, nil
}

// bookFieldPredicate is a function that extracts a string value from a Book
// for a named field. Returns ("", false) when the field is absent or empty.
type bookFieldPredicate func(*database.Book) (string, bool)

// allowedBookFields maps the allow-listed field names for ReqFieldSet to
// accessor predicates over *database.Book. A field is "set" when the accessor
// returns a non-empty string.
//
// Allow-listed fields (all *string on database.Book):
//   - book_sig_v1         — unified per-book audio signature (base64)
//   - metadata_source_hash — sha256 of provider+canonical_id (O(1) dedup key)
//   - asin                — Audible ASIN
//   - isbn13              — ISBN-13
//
// Note: "acoustid_fingerprint" is NOT listed because AcoustIDFingerprint is a
// []byte field on *database.BookFile (per-file), not on *database.Book.
// Fingerprint readiness should be expressed as:
//
//	Requirement{Kind: ReqOpCompleted, OpType: "acoustid.fingerprint-extract", AllFiles: true}
//
// (See spec M4 example.) Adding a Book-level proxy here would be wrong;
// use the AllFiles completion requirement instead.
var allowedBookFields = map[string]bookFieldPredicate{
	"book_sig_v1": func(b *database.Book) (string, bool) {
		// A legacy-era signature (pre-2026-09-19 decoder) is not a usable
		// signature: report it absent so dependents rebuild it.
		if !b.HasCurrentBookSig() {
			return "", false
		}
		return *b.BookSigV1, true
	},
	"metadata_source_hash": func(b *database.Book) (string, bool) {
		if b.MetadataSourceHash == nil || *b.MetadataSourceHash == "" {
			return "", false
		}
		return *b.MetadataSourceHash, true
	},
	"asin": func(b *database.Book) (string, bool) {
		if b.ASIN == nil || *b.ASIN == "" {
			return "", false
		}
		return *b.ASIN, true
	},
	"isbn13": func(b *database.Book) (string, bool) {
		if b.ISBN13 == nil || *b.ISBN13 == "" {
			return "", false
		}
		return *b.ISBN13, true
	},
}

// subjectToOpSubject converts the registry's Subject to the database wire type.
func subjectToOpSubject(sub Subject) database.OpSubject {
	return database.OpSubject{Type: sub.Type, ID: sub.ID}
}

// Satisfied evaluates a single Requirement against the current state in store.
//
// Returns:
//   - (true, "", nil) when the requirement is met
//   - (false, reason, nil) when unmet — reason is human-readable for logs/UI
//   - (false, "", err) when a store error prevents evaluation
func Satisfied(store DepStore, req Requirement, sub Subject) (bool, string, error) {
	switch req.Kind {
	case ReqOpCompleted:
		return evalOpCompleted(store, req, sub)
	case ReqFieldSet:
		return evalFieldSet(store, req, sub)
	default:
		return false, fmt.Sprintf("unknown requirement kind %q", req.Kind), nil
	}
}

// evalFieldSet evaluates a ReqFieldSet requirement.
// It loads the subject book and checks whether the named field is non-empty.
// An unknown field name returns an error (typo guard). A missing book
// (GetBookByID returns nil, nil) is treated as unmet with a clear reason.
func evalFieldSet(store DepStore, req Requirement, sub Subject) (bool, string, error) {
	pred, ok := allowedBookFields[req.Field]
	if !ok {
		return false, "", fmt.Errorf("field_set: unknown field %q (not in allow-list)", req.Field)
	}

	book, err := store.GetBookByID(sub.ID)
	if err != nil {
		return false, "", fmt.Errorf("field_set: GetBookByID(%q): %w", sub.ID, err)
	}
	if book == nil {
		return false, fmt.Sprintf("field_set: book %q not found for subject %s/%s", sub.ID, sub.Type, sub.ID), nil
	}

	val, set := pred(book)
	_ = val // value itself is not used; only presence matters
	if !set {
		return false, fmt.Sprintf("field_set: field %q is empty on book %q", req.Field, sub.ID), nil
	}
	return true, "", nil
}

// AllSatisfied evaluates every requirement in reqs. Returns true only when all
// are met. On first unmet requirement, returns (false, firstUnmet, nil) where
// firstUnmet is the human-readable reason from Satisfied. Returns an error only
// when a store error is encountered.
func AllSatisfied(store DepStore, reqs []Requirement, sub Subject) (bool, string, error) {
	for _, req := range reqs {
		ok, reason, err := Satisfied(store, req, sub)
		if err != nil {
			return false, "", err
		}
		if !ok {
			return false, reason, nil
		}
	}
	return true, "", nil
}

// evalOpCompleted evaluates a ReqOpCompleted requirement.
// A completion is fresh when its stored dep_rev equals the subject's current dep_rev.
// If AllFiles is true, every file of the book must have its own fresh completion record.
func evalOpCompleted(store DepStore, req Requirement, sub Subject) (bool, string, error) {
	dbSub := subjectToOpSubject(sub)
	curRev, err := store.GetDepRev(dbSub)
	if err != nil {
		return false, "", fmt.Errorf("GetDepRev(%v): %w", sub, err)
	}

	if req.AllFiles {
		return evalAllFilesCompleted(store, req, sub, dbSub, curRev)
	}

	// Book-level completion check.
	completionRev, ok, err := store.GetOpCompletion(dbSub, req.OpType)
	if err != nil {
		return false, "", fmt.Errorf("GetOpCompletion(%v, %q): %w", sub, req.OpType, err)
	}
	if !ok {
		return false, fmt.Sprintf("op %q has never completed for %s/%s", req.OpType, sub.Type, sub.ID), nil
	}
	if completionRev < curRev {
		return false, fmt.Sprintf("op %q completion at rev %d is stale (current dep_rev=%d) for %s/%s",
			req.OpType, completionRev, curRev, sub.Type, sub.ID), nil
	}
	return true, "", nil
}

// evalAllFilesCompleted checks that every file of the book has a per-file
// completion record at the current dep_rev.
func evalAllFilesCompleted(store DepStore, req Requirement, sub Subject, dbSub database.OpSubject, curRev uint64) (bool, string, error) {
	if sub.Type != "book" {
		return false, fmt.Sprintf("AllFiles=true is only supported for book subjects, got %q", sub.Type), nil
	}

	files, err := store.BookFiles(sub.ID)
	if err != nil {
		return false, "", fmt.Errorf("BookFiles(%q): %w", sub.ID, err)
	}
	if len(files) == 0 {
		// No files on record → treat as unmet: the book may not be scanned yet.
		return false, fmt.Sprintf("op %q AllFiles=true: book %q has no files on record", req.OpType, sub.ID), nil
	}

	fileRevs, err := store.ListFileCompletions(dbSub, req.OpType)
	if err != nil {
		return false, "", fmt.Errorf("ListFileCompletions(%v, %q): %w", sub, req.OpType, err)
	}

	for _, fileID := range files {
		rev, ok := fileRevs[fileID]
		if !ok {
			return false, fmt.Sprintf("op %q AllFiles=true: file %q has no completion record for %s/%s",
				req.OpType, fileID, sub.Type, sub.ID), nil
		}
		if rev < curRev {
			return false, fmt.Sprintf("op %q AllFiles=true: file %q completion at rev %d is stale (current dep_rev=%d)",
				req.OpType, fileID, rev, curRev), nil
		}
	}
	return true, "", nil
}

// CheckRequirementCycle checks whether the requirement graph defined by
// defReqsByOpType contains any cycle. The map keys are op-type IDs and the
// values are their Requires slices (only ReqOpCompleted edges are traversed;
// ReqFieldSet has no graph edge).
//
// Returns a non-nil error naming the cycle when one exists.
// This is intended to run once at registry startup, not per-enqueue.
func CheckRequirementCycle(defReqsByOpType map[string][]Requirement) error {
	// Standard DFS with three-color marking:
	//   white (0) = unvisited
	//   grey  (1) = in current DFS stack (back-edge = cycle)
	//   black (2) = fully processed (cross/forward edges are fine)
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := make(map[string]int, len(defReqsByOpType))

	var dfs func(node string) error
	dfs = func(node string) error {
		color[node] = grey
		for _, req := range defReqsByOpType[node] {
			if req.Kind != ReqOpCompleted {
				continue // only op-completed edges form graph edges
			}
			neighbor := req.OpType
			switch color[neighbor] {
			case grey:
				return fmt.Errorf("requirement cycle detected: %q → %q forms a cycle", node, neighbor)
			case white:
				if err := dfs(neighbor); err != nil {
					return err
				}
			}
			// black = already fully processed, skip
		}
		color[node] = black
		return nil
	}

	for node := range defReqsByOpType {
		if color[node] == white {
			if err := dfs(node); err != nil {
				return err
			}
		}
	}
	return nil
}
