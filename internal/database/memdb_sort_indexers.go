// file: internal/database/memdb_sort_indexers.go
// version: 1.0.0
// guid: 5e7d1b94-08c6-4a32-bf51-9d2e6c0a374b
// last-edited: 2026-08-09
//
// Sorted secondary indexes for the library list.
//
// WHY
//
// service_query.go:155 disables pagination whenever the sort is anything but
// title:
//
//	if heavySorting { pdLimit, pdOffset = 0, 0 }   // fetch the FULL set
//
// That is deliberate — a sort applied after pagination only orders the rows
// you already fetched, so it chose slow over wrong. But "slow" here means
// materialising and sorting all 366,916 books to return 50 of them, on every
// page load. memdb_reads.go:585 records the same shape costing 340MB of
// allocations per call at 68K rows; at 366K it is worse.
//
// Title escapes this because it has a sorted index (memIdxTitle) that memdb
// can walk in order, stopping at limit+offset. These indexers give the same
// treatment to the fields the owner selected, converting each from a
// full-set materialise-and-sort into a streaming walk.
//
// WHICH FIELDS, AND WHY NOT THE OTHERS
//
// Nine sort keys over six physical indexes (duration/bitrate/file_size each
// have an alias key naming the same underlying value):
//
//	author, narrator, series      — identity; what a person browsing sorts by
//	created_at, updated_at, year  — chronological
//	duration, file_size, bitrate  — outlier-hunting and quality triage
//
// Deliberately NOT indexed: library_state and quality (low-cardinality —
// sorting 366K books by a handful of distinct values yields a few enormous
// runs in arbitrary internal order, which reads as unsorted; they belong on
// the filter path), and genre/language/publisher/format/codec/edition/
// sample_rate_hz (keep working via the existing materialise-and-sort; adding
// one later is a schema entry plus a sortIndexForField line).
//
// KEY ENCODING — THE PART THAT HAS TO BE RIGHT
//
// memdb is a radix tree: it orders by BYTE comparison, not by value. A key
// must therefore be encoded so that bytewise order equals semantic order.
//
//   - Every key starts with a PRESENCE byte: 0x00 present, 0x01 missing.
//     Missing values thus sort after every present one in ascending order,
//     matching titleSortIndex's intent ("~" sentinel) but without its edge:
//     "~" is 0x7E, so a title beginning with any rune above U+007E would sort
//     after the "no title" sentinel. A dedicated byte cannot collide.
//   - Integers and times are big-endian uint64 in OFFSET-BINARY form
//     (sign bit flipped). Two's-complement negatives have the high bit set,
//     so raw big-endian bytes would sort every negative ABOVE every positive.
//     Flipping the sign bit restores order. Times use UnixNano through the
//     same path.
//   - Strings are lowercased and null-terminated, per the memdb convention
//     already used by titleSortIndex for prefix-iteration correctness.

package database

import (
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Presence markers. A single leading byte, so a missing value can never be
// confused with a present one whatever the payload contains.
const (
	sortKeyPresent byte = 0x00
	sortKeyMissing byte = 0x01
)

// encodeSortableInt64 renders v as an order-preserving big-endian key.
//
// The sign-bit flip is the load-bearing part: as raw two's-complement bytes,
// -1 is 0xFFFF...FF and 1 is 0x0000...01, so a bytewise sort would place
// every negative above every positive. XORing the high bit maps the signed
// range onto the unsigned range monotonically.
func encodeSortableInt64(v int64) []byte {
	out := make([]byte, 1+8)
	out[0] = sortKeyPresent
	binary.BigEndian.PutUint64(out[1:], uint64(v)^(1<<63))
	return out
}

// missingSortKey is the key for a book with no value for the indexed field.
// Sorts after every present value in ascending order.
func missingSortKey() []byte {
	return []byte{sortKeyMissing}
}

// encodeSortableString lowercases and null-terminates, matching the existing
// memdb string-index convention.
func encodeSortableString(s string) []byte {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return missingSortKey()
	}
	out := make([]byte, 0, 1+len(s)+1)
	out = append(out, sortKeyPresent)
	out = append(out, s...)
	return append(out, 0)
}

// bookStringSortIndex indexes a string-valued field extracted by get.
// A nil/empty result becomes the missing key rather than being dropped from
// the index — a book absent from the index vanishes from the library page
// when sorting by that field, which is the bug titleSortIndex's comment
// warns about.
type bookStringSortIndex struct {
	name string
	get  func(*Book) string
}

func (ix bookStringSortIndex) FromObject(obj interface{}) (bool, []byte, error) {
	b, ok := obj.(*Book)
	if !ok {
		return false, nil, fmt.Errorf("%s: expected *Book, got %T", ix.name, obj)
	}
	return true, encodeSortableString(ix.get(b)), nil
}

func (ix bookStringSortIndex) FromArgs(args ...interface{}) ([]byte, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("%s: expected 1 arg, got %d", ix.name, len(args))
	}
	s, ok := args[0].(string)
	if !ok {
		return nil, fmt.Errorf("%s: arg must be string, got %T", ix.name, args[0])
	}
	return encodeSortableString(s), nil
}

// bookIntSortIndex indexes a numeric field. get returns the value and
// whether it is present, so a genuine zero (a 0-second duration) is
// distinguishable from "unset" and sorts with the numbers rather than at the
// end with the unknowns.
type bookIntSortIndex struct {
	name string
	get  func(*Book) (int64, bool)
}

func (ix bookIntSortIndex) FromObject(obj interface{}) (bool, []byte, error) {
	b, ok := obj.(*Book)
	if !ok {
		return false, nil, fmt.Errorf("%s: expected *Book, got %T", ix.name, obj)
	}
	v, present := ix.get(b)
	if !present {
		return true, missingSortKey(), nil
	}
	return true, encodeSortableInt64(v), nil
}

func (ix bookIntSortIndex) FromArgs(args ...interface{}) ([]byte, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("%s: expected 1 arg, got %d", ix.name, len(args))
	}
	switch v := args[0].(type) {
	case int:
		return encodeSortableInt64(int64(v)), nil
	case int64:
		return encodeSortableInt64(v), nil
	case time.Time:
		return encodeSortableInt64(v.UnixNano()), nil
	default:
		return nil, fmt.Errorf("%s: arg must be int, int64 or time.Time, got %T", ix.name, args[0])
	}
}

// Field accessors. Each mirrors the comparator of the same name in
// audiobooks/service_filtering.go, so an indexed sort and a materialised
// sort agree. If a comparator there changes, the accessor here must change
// with it or the two orders silently diverge.

func bookAuthorSortValue(b *Book) string {
	if b.Author != nil {
		return b.Author.Name
	}
	return ""
}

func bookSeriesSortValue(b *Book) string {
	if b.Series != nil {
		return b.Series.Name
	}
	return ""
}

func bookNarratorSortValue(b *Book) string {
	if b.Narrator != nil {
		return *b.Narrator
	}
	return ""
}

// bookYearSortValue mirrors the "year" comparator: AudiobookReleaseYear
// wins, PrintYear is the fallback, and a zero in the first is treated as
// absent rather than as the year 0.
func bookYearSortValue(b *Book) (int64, bool) {
	if b.AudiobookReleaseYear != nil && *b.AudiobookReleaseYear != 0 {
		return int64(*b.AudiobookReleaseYear), true
	}
	if b.PrintYear != nil && *b.PrintYear != 0 {
		return int64(*b.PrintYear), true
	}
	return 0, false
}

func bookDurationSortValue(b *Book) (int64, bool) {
	if b.Duration == nil {
		return 0, false
	}
	return int64(*b.Duration), true
}

func bookBitrateSortValue(b *Book) (int64, bool) {
	if b.Bitrate == nil {
		return 0, false
	}
	return int64(*b.Bitrate), true
}

func bookFileSizeSortValue(b *Book) (int64, bool) {
	if b.FileSize == nil {
		return 0, false
	}
	return *b.FileSize, true
}

func bookCreatedAtSortValue(b *Book) (int64, bool) {
	if b.CreatedAt == nil || b.CreatedAt.IsZero() {
		return 0, false
	}
	return b.CreatedAt.UnixNano(), true
}

func bookUpdatedAtSortValue(b *Book) (int64, bool) {
	if b.UpdatedAt == nil || b.UpdatedAt.IsZero() {
		return 0, false
	}
	return b.UpdatedAt.UnixNano(), true
}

// sortIndexForField maps a sort_by key to the memdb index that can serve it
// as an ordered walk. Keys absent from this map fall back to the existing
// materialise-and-sort path, so adding an entry is the only step needed to
// promote a field once its index exists.
//
// Alias keys (duration_seconds, bitrate_kbps, file_size_bytes) point at the
// same index as their short form — one physical index, two spellings.
// enabledSortIndexes holds the sort fields whose indexes are registered in
// the schema. Empty by default, which reproduces pre-2026-08-09 behaviour
// exactly: only title streams.
//
// A setter rather than reading config.AppConfig directly, because
// internal/config already imports internal/database — reading config here
// would be an import cycle. The dependency has to point this way.
var (
	enabledSortIndexesMu sync.RWMutex
	enabledSortIndexes   = map[string]bool{}
)

// SetEnabledSortIndexes selects which sort fields get a memdb sorted index.
// Returns any names that were not recognised, so the caller can warn rather
// than silently ignoring a typo in config.
//
// ⚠️ MUST be called BEFORE the store opens. memdbSchema() reads this set
// once, when NewMemStore builds the schema; calling it afterwards changes
// which sorts claim to be pushdownable without changing which indexes exist,
// and the query path would then ask memdb for an index that is not there.
//
// Each enabled field costs real memory — see EnabledSortIndexes in
// internal/config for the measured figures (~146 MB per key at prod scale).
func SetEnabledSortIndexes(fields []string) (unknown []string) {
	enabledSortIndexesMu.Lock()
	defer enabledSortIndexesMu.Unlock()

	enabledSortIndexes = make(map[string]bool, len(fields))
	for _, f := range fields {
		f = strings.ToLower(strings.TrimSpace(f))
		if f == "" {
			continue
		}
		if _, ok := sortIndexForField[f]; !ok {
			unknown = append(unknown, f)
			continue
		}
		enabledSortIndexes[f] = true
	}
	return unknown
}

// sortIndexEnabled reports whether field's index is registered.
func sortIndexEnabled(field string) bool {
	enabledSortIndexesMu.RLock()
	defer enabledSortIndexesMu.RUnlock()
	return enabledSortIndexes[field]
}

// enabledSortIndexNames returns the distinct memdb index names to register.
// Alias keys collapse: enabling both "duration" and "duration_seconds"
// yields one index, not two.
func enabledSortIndexNames() map[string]bool {
	enabledSortIndexesMu.RLock()
	defer enabledSortIndexesMu.RUnlock()

	out := make(map[string]bool, len(enabledSortIndexes))
	for field := range enabledSortIndexes {
		if name, ok := sortIndexForField[field]; ok {
			out[name] = true
		}
	}
	return out
}

// CanPushDownSort reports whether sort_by=field can be served as an ordered
// walk over a memdb index rather than by materialising the whole filtered set.
//
// Exported because the decision belongs to whoever owns the indexes, not to
// the query service: audiobooks/service_query.go used to hardcode
// `f.SortBy == "title"`, so adding an index here without editing that string
// there would have built the index and never used it. Callers ask; they do
// not maintain their own list.
//
// "title" is included even though it predates this map, so callers have one
// question to ask instead of two.
func CanPushDownSort(field string) bool {
	if field == memIdxTitle {
		return true
	}
	// Must consult the ENABLED set, not merely the known set: a field whose
	// index was not registered has no index to walk, and claiming otherwise
	// would send the query path to txn.Get with an unknown index name.
	return sortIndexEnabled(field)
}

var sortIndexForField = map[string]string{
	"author":           memIdxSortAuthor,
	"narrator":         memIdxSortNarrator,
	"series":           memIdxSortSeries,
	"year":             memIdxSortYear,
	"created_at":       memIdxSortCreatedAt,
	"updated_at":       memIdxSortUpdatedAt,
	"duration":         memIdxSortDuration,
	"duration_seconds": memIdxSortDuration,
	"bitrate":          memIdxSortBitrate,
	"bitrate_kbps":     memIdxSortBitrate,
	"file_size":        memIdxSortFileSize,
	"file_size_bytes":  memIdxSortFileSize,
}
