// file: internal/undo/metadata_apply.go
// version: 1.0.0
// guid: 9b4e2c71-5d08-4a3f-8e61-c7f02a9d3b54
// last-edited: 2026-10-06

package undo

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ChangeTypeMetadataApply: a Repairs fixer applied a metadata candidate to
// BookID through metafetch's apply, which recorded the fields it changed as
// one change-history batch. FieldName is MetadataApplyField, NewValue the
// batch id, OldValue "". Written AFTER the apply committed (ledger after
// write), by maintenance.version-twin-metadata.
//
// Restorable: the revert undoes exactly that history batch
// (metafetch.Service.UndoApplyBatch), field by field, each only while it
// still holds what the apply wrote. A batch already undone (undo last apply,
// or an earlier revert) counts restored with nothing written.
const ChangeTypeMetadataApply = "metadata_apply"

// MetadataApplyField is the FieldName of a ChangeTypeMetadataApply row.
const MetadataApplyField = "batch"

// ChangeTypeMetadataCacheCopy: a Repairs fixer wrote BookID's candidate cache
// row from another book's (metafetch.Service.CopyCandidateCache). FieldName
// is MetadataCacheField. OldValue is MetadataCacheAbsent when the book had no
// cache row, else the prior row as JSON (the copy only ever replaces a row
// with no candidates, so it is small). NewValue is MetadataCacheStamp of the
// row written. Journaled after the copy, by maintenance.version-twin-metadata.
//
// Restorable: while the book's cache row still carries the stamp, it is
// deleted (OldValue absent) or put back (OldValue a row). A row already back
// to OldValue counts restored; any other row (a fetch since) is refused.
const ChangeTypeMetadataCacheCopy = "metadata_cache_copy"

// MetadataCacheField is the FieldName of a ChangeTypeMetadataCacheCopy row.
const MetadataCacheField = "candidates"

// MetadataCacheAbsent is the OldValue of a ChangeTypeMetadataCacheCopy row
// whose book had no cache row before the copy.
const MetadataCacheAbsent = "absent"

// MetadataCacheStamp identifies one cache row's content for the revert's
// compare-and-set: the identity hash, the fetch time, the candidate count,
// the search fingerprint, the ASIN fetched for and the last empty fetch. A fetch that replaces the row changes at least
// the time.
func MetadataCacheStamp(e *database.MetadataCandidateCache) string {
	if e == nil {
		return MetadataCacheAbsent
	}
	empty := ""
	if e.LastEmptyFetchAt != nil {
		empty = e.LastEmptyFetchAt.UTC().Format(time.RFC3339Nano)
	}
	return strings.Join([]string{e.SourceHash, e.FetchedAt.UTC().Format(time.RFC3339Nano),
		strconv.Itoa(len(e.Candidates)), e.SearchFingerprint, e.FetchedForASIN, empty}, "|")
}

// EncodeMetadataCacheOld is the OldValue for a copy over prior (nil: none).
func EncodeMetadataCacheOld(prior *database.MetadataCandidateCache) (string, error) {
	if prior == nil {
		return MetadataCacheAbsent, nil
	}
	b, err := json.Marshal(prior)
	if err != nil {
		return "", fmt.Errorf("encode the prior cache row of %s: %w", prior.BookID, err)
	}
	return string(b), nil
}

// DecodeMetadataCacheOld parses a ChangeTypeMetadataCacheCopy OldValue: nil
// for MetadataCacheAbsent.
func DecodeMetadataCacheOld(v string) (*database.MetadataCandidateCache, error) {
	if v == MetadataCacheAbsent {
		return nil, nil
	}
	var e database.MetadataCandidateCache
	if err := json.Unmarshal([]byte(v), &e); err != nil {
		return nil, fmt.Errorf("metadata_cache_copy old value: %w", err)
	}
	return &e, nil
}

// CheckMetadataCacheCopy is the compare-and-set of a
// ChangeTypeMetadataCacheCopy row, shared by the revert and the preflight:
// nil while cur (the book's cache row now, nil when none) carries the row's
// stamp (restore it), ErrAlreadyRestored when cur is already OldValue, and a
// ReasonChangedSince refusal otherwise.
func CheckMetadataCacheCopy(cur *database.MetadataCandidateCache, c *database.OperationChange) error {
	if MetadataCacheStamp(cur) == c.NewValue && cur != nil {
		return nil
	}
	old, err := DecodeMetadataCacheOld(c.OldValue)
	if err != nil {
		return refuse(ReasonOldValueUnparsable, "%v", err)
	}
	if MetadataCacheStamp(cur) == MetadataCacheStamp(old) {
		return ErrAlreadyRestored
	}
	return refuse(ReasonChangedSince, "book %s's candidate cache was replaced since the copy (a fetch ran); not restoring it", c.BookID)
}

func validMetadataApplyRow(c *database.OperationChange) bool {
	return c.FieldName == MetadataApplyField && c.NewValue != ""
}

func validMetadataCacheCopyRow(c *database.OperationChange) bool {
	if c.FieldName != MetadataCacheField || c.NewValue == "" || c.NewValue == MetadataCacheAbsent {
		return false
	}
	_, err := DecodeMetadataCacheOld(c.OldValue)
	return err == nil
}

// MetadataCacheReader is the optional store surface the preflight and the
// revert read a book's candidate cache row through.
type MetadataCacheReader interface {
	GetMetadataCache(bookID string) (*database.MetadataCandidateCache, error)
}
