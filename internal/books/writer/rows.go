// file: internal/books/writer/rows.go
// version: 1.0.2
// guid: c4e91a73-0b58-4d26-9f3a-71d5e2a86b04
// last-edited: 2026-11-19

package writer

import (
	"fmt"
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance/resources"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// SharedRows is the stack-wide method-to-resource table
// (internal/maintenance/resources.Table) as Rows: a fresh copy per call.
func SharedRows() Rows { return Rows(resources.Table()) }

// MetafetchApplyRows are the rows of the store writes metafetch's apply makes
// that the shared table has no row for, because no converted kind's typed
// writer ever called them: the apply runs inside metafetch.Service over the
// store, outside the typed writers (seams.d/F5-06.md, the F5-18 obligation).
// Each row follows THE RULE of resources.Table (everything the method can
// write, through its whole call chain):
//
//   - AddBookTagWithSource, RemoveBookTagsByPrefix: the book_tag: row and its
//     tag_idx: reverse index, one batch (books).
//   - CreateSeries: series: and series:name: (series).
//   - UpsertMetadataFieldState, DeleteMetadataFieldState: metadata_state:
//     (field_states).
//   - RecordMetadataChange: metadata_change: history rows, which are book-keyed
//     (books, as path_history: is; coordinator ruling 2026-11-19). A ModifyBook
//     over a legacy stale-series row stages one of them in its own batch, so
//     the ModifyBook row carries it as `books` too.
//   - PutMetadataCache: the metadata_cache: row and the book's ident: state in
//     one batch (metadata_cache, indexes), as DeleteMetadataCache.
//   - RecordPathChange: path_history: (books: a record keyed on a book).
//   - FlagMetadataHashDuplicate, SetLastWrittenAt: a ModifyBook call, so the
//     whole book write.
//   - MarkNeedsRescan: ModifyBook, then the same flag mirrored onto the book's
//     one file row (book_files).
//   - PatchBookFileFields: the book_file row, then notifyBookFileChange ->
//     RecomputeBookAggregates -> ModifyBook (a file-row method).
//   - SetUserPreference, DeleteUserPreference: preference: rows, which the file
//     pipeline's phase checkpoints are (user_state).
//
// The raw key-value writes are qualified by key family (RawFamilies).
//
// These belong in the shared table. They are kept here because the table's file
// is outside F5-18b's scope; TestMetafetchApplyRows_AgreeWithTheSharedTable
// fails if a row ever differs from the shared one, so the day they move there
// is a deletion, not a reconciliation.
func MetafetchApplyRows() Rows {
	bookW := []ops.Resource{ops.ResBooks, ops.ResAuthors, ops.ResMetadataCache, ops.ResIndexes}
	fileRow := resources.Union([]ops.Resource{ops.ResBookFiles}, bookW)
	return Rows{
		"AddBookTagWithSource":      {ops.ResBooks},
		"RemoveBookTagsByPrefix":    {ops.ResBooks},
		"CreateSeries":              {ops.ResSeries},
		"UpsertMetadataFieldState":  {ops.ResFieldStates},
		"DeleteMetadataFieldState":  {ops.ResFieldStates},
		"RecordMetadataChange":      {ops.ResBooks},
		"PutMetadataCache":          {ops.ResMetadataCache, ops.ResIndexes},
		"RecordPathChange":          {ops.ResBooks},
		"FlagMetadataHashDuplicate": slices.Clone(bookW),
		"SetLastWrittenAt":          slices.Clone(bookW),
		"MarkNeedsRescan":           resources.Union([]ops.Resource{ops.ResBookFiles}, bookW),
		"PatchBookFileFields":       fileRow,
		"SetUserPreference":         {ops.ResUserState},
		"DeleteUserPreference":      {ops.ResUserState},
	}
}

// RawFamily is a key family of the raw key-value store and the resource it is.
type RawFamily struct {
	Prefix   string
	Resource ops.Resource
}

// RawFamilies are the raw key families metafetch's apply writes through
// SetRaw, DeleteRaw and DeleteRawBatch, by key prefix: the fetch cache and its
// per-source cache, which the store helper behind the apply writes through the
// store it is handed (metadata_fetch_cache.go). A raw write is stamped by the
// family of its key, qualified as "SetRaw/<prefix without the colon>" (the
// shared table's convention for SetRaw/cover_text and the like); a key in no
// family is refused, so a new raw write fails closed.
//
// Candidate rejections are not in the list on purpose. metafetch's rejection
// helpers raw-write rejected_candidate: (rejected_candidates.go), but only
// server handlers reach them, over the server's own store, never through
// metafetch.Service's store; and metadata_rejection: is written only by the
// store's typed methods. Neither is a raw write the gate sees. If F5-18 routes
// the rejection helpers through the gate it adds rejected_candidate: here with
// the resource it decides on.
func RawFamilies() []RawFamily {
	return []RawFamily{
		{"metadata_fetch_cache:", ops.ResMetadataCache},
		{"pcache:", ops.ResMetadataCache},
	}
}

// RawRows are the qualified rows of RawFamilies, for the three raw writes.
func RawRows() Rows {
	rows := Rows{}
	for _, f := range RawFamilies() {
		for _, m := range []string{"SetRaw", "DeleteRaw", "DeleteRawBatch"} {
			rows[RawMethod(m, f.Prefix)] = []ops.Resource{f.Resource}
		}
	}
	return rows
}

// RawMethod is the qualified row name of a raw write on a key family.
func RawMethod(method, prefix string) string {
	return fmt.Sprintf("%s/%s", method, trimColon(prefix))
}

func trimColon(s string) string {
	if n := len(s); n > 0 && s[n-1] == ':' {
		return s[:n-1]
	}
	return s
}

// Merge unions row tables into one, refusing a method two tables both carry
// with different rows (the rows are compared as sets).
func Merge(tables ...Rows) (Rows, error) {
	out := Rows{}
	for _, t := range tables {
		for m, row := range t {
			if prev, dup := out[m]; dup && !sameSet(prev, row) {
				return nil, fmt.Errorf("books writer: method %s has two different rows: %v and %v", m, prev, row)
			}
			out[m] = row
		}
	}
	return out, nil
}

func sameSet(a, b []ops.Resource) bool {
	return slices.Equal(resources.Union(a), resources.Union(b))
}
