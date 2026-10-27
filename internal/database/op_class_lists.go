// file: internal/database/op_class_lists.go
// version: 1.0.0
// guid: 3b0e1f5a-6a0c-4d57-9c1e-72d5f1e3a4b8
// last-edited: 2026-10-27

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
)

// Class lists: the id lists of an operation's result classes, kept beside the
// operation instead of inside its result.
//
// WHY. A class-based result (maintenance.credits-backfill: every class with
// every book id, so that every count clicks through) is 5-12 MB at library
// scale. Stored in the operation's own row it was read whole by
// GetOperationV2, by the /system/status poll (ListRecentOperationsV2 decodes
// every row of the last hour), by the boot scan and by the diagnostics bundle,
// and the v3 mirror stored it a second time. The repairs plan moved a 39 MB
// plan out for the same reason (internal/repairs/planstore.go). The result keeps
// the summary and the counts (a ClassListSummary under "class_lists"); the ids
// live here, through OpRowStore, and are read a page at a time.
//
// LAYOUT. One row per CHUNK of ClassListChunk ids, not one per id: a page is a
// few point reads (GetOpRows) at chunk ids computed from the offset, never a
// scan of the whole set, and a library-scale result is a few hundred rows. A
// row's ID is "<class>/<chunk number, 6 digits>", its Data the JSON array of
// that chunk's ids, its Index the class key. The summary records the chunk size
// and every class's count, so a reader checks the rows it reads against it.

const (
	// ClassListsStoreV1 is the ClassListSummary.Store of lists SaveClassLists
	// wrote beside the operation.
	ClassListsStoreV1 = "op_rows.v1"
	// ClassListChunk is the number of ids in one stored row (the last row of a
	// class holds the remainder).
	ClassListChunk = 1000
)

// Errors of reading stored class lists.
var (
	// ErrClassListUnknown: the summary has no such class.
	ErrClassListUnknown = errors.New("database: no such class in the stored class lists")
	// ErrClassListIncomplete: the rows found under the operation's id are not
	// the lists its summary says it has (a partial write, a deleted row set, or a
	// row that does not decode). The remedy is to run the operation again.
	ErrClassListIncomplete = errors.New("database: the stored class lists do not match the result summary; run the operation again")
)

// ClassListSummary is what an operation's result carries in place of its class
// id lists: where they are, how they are cut, and how many ids each class has.
type ClassListSummary struct {
	// Store is ClassListsStoreV1.
	Store string `json:"store"`
	// Chunk is the number of ids per stored row.
	Chunk int `json:"chunk"`
	// Counts is the number of ids stored for each class (classes with none are
	// listed with 0 and have no rows).
	Counts map[string]int `json:"counts"`
}

func classListRowID(class string, chunk int) string { return fmt.Sprintf("%s/%06d", class, chunk) }

// SaveClassLists stores lists (class -> ids, kept in the order given) under the
// operation id, replacing whatever set the operation had (a resumed run writes
// its lists again), and returns the summary the caller puts in the operation's
// result. Rows are written before the caller stores the summary, so a result
// that carries one has all of its rows. On a failure the rows written so far
// are cleared.
func SaveClassLists(rs OpRowStore, opID string, lists map[string][]string) (*ClassListSummary, error) {
	if rs == nil || opID == "" {
		return nil, errors.New("database: save class lists: no row store or operation id")
	}
	sum := &ClassListSummary{Store: ClassListsStoreV1, Chunk: ClassListChunk, Counts: make(map[string]int, len(lists))}
	type ref struct {
		class string
		start int
		end   int
		chunk int
	}
	var refs []ref
	classes := make([]string, 0, len(lists))
	for class := range lists {
		classes = append(classes, class)
	}
	sort.Strings(classes)
	for _, class := range classes {
		ids := lists[class]
		sum.Counts[class] = len(ids)
		for start, chunk := 0, 0; start < len(ids); start, chunk = start+ClassListChunk, chunk+1 {
			refs = append(refs, ref{class: class, start: start, end: min(start+ClassListChunk, len(ids)), chunk: chunk})
		}
	}
	err := rs.ReplaceOpRows(opID, len(refs), func(i int) (OpRow, error) {
		r := refs[i]
		data, err := json.Marshal(lists[r.class][r.start:r.end])
		if err != nil {
			return OpRow{}, fmt.Errorf("encode %s chunk %d: %w", r.class, r.chunk, err)
		}
		return OpRow{ID: classListRowID(r.class, r.chunk), Index: []byte(r.class), Data: data}, nil
	})
	if err != nil {
		_ = ClearClassLists(rs, opID)
		return nil, fmt.Errorf("database: save class lists %s: %w", opID, err)
	}
	return sum, nil
}

// ClearClassLists deletes the class-list rows stored under an operation id.
func ClearClassLists(rs OpRowStore, opID string) error {
	return rs.ReplaceOpRows(opID, 0, nil)
}

// Valid reports whether s describes lists SaveClassLists wrote.
func (s *ClassListSummary) Valid() bool {
	return s != nil && s.Store == ClassListsStoreV1 && s.Chunk > 0 && s.Counts != nil
}

// Page returns up to limit ids of class starting at offset, in stored order.
// An offset at or past the count is an empty page. ErrClassListUnknown names a
// class the summary lacks; ErrClassListIncomplete a row that is missing, does
// not decode or has the wrong length.
func (s *ClassListSummary) Page(rs OpRowStore, opID, class string, offset, limit int) ([]string, error) {
	if !s.Valid() {
		return nil, fmt.Errorf("%w: no valid summary", ErrClassListIncomplete)
	}
	total, ok := s.Counts[class]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrClassListUnknown, class)
	}
	if offset < 0 || limit < 1 {
		return nil, fmt.Errorf("database: class list page: offset %d limit %d", offset, limit)
	}
	if offset >= total {
		return []string{}, nil
	}
	end := min(offset+limit, total)
	first, last := offset/s.Chunk, (end-1)/s.Chunk
	rowIDs := make([]string, 0, last-first+1)
	for c := first; c <= last; c++ {
		rowIDs = append(rowIDs, classListRowID(class, c))
	}
	datas, err := rs.GetOpRows(opID, rowIDs)
	if err != nil {
		return nil, fmt.Errorf("database: read class list %s of %s: %w", class, opID, err)
	}
	out := make([]string, 0, end-offset)
	for i, c := 0, first; c <= last; i, c = i+1, c+1 {
		want := min(s.Chunk, total-c*s.Chunk)
		var ids []string
		if i >= len(datas) || datas[i] == nil {
			return nil, fmt.Errorf("%w: %s has no row for %s", ErrClassListIncomplete, opID, rowIDs[i])
		}
		if err := json.Unmarshal(datas[i], &ids); err != nil || len(ids) != want {
			return nil, fmt.Errorf("%w: %s row %s does not hold the %d ids its summary counts", ErrClassListIncomplete, opID, rowIDs[i], want)
		}
		lo := max(offset-c*s.Chunk, 0)
		hi := min(end-c*s.Chunk, want)
		out = append(out, ids[lo:hi]...)
	}
	return slices.Clone(out), nil
}
