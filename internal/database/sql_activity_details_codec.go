// file: internal/database/sql_activity_details_codec.go
// version: 1.0.0
// guid: 7c2a9e51-3b84-4d06-9f1a-8e5b2c7d4a90
// last-edited: 2026-09-07

// Package database — codec for the SQLite activity `details` column.
//
// WHY: `details` is the only large activity column — a class of `change` rows
// carries multi-MB JSON blobs (e.g. iTunes ApplyITLOperations dumps). Pebble
// stores the activity keyspace block-compressed, but SQLite has no transparent
// text compression, so those blobs land RAW and ballooned activity.sqlite past
// 30 GB on prod (2026-09-07), forcing the SQLite re-enable to be rolled back.
// This compresses `details` at the application layer with pure-Go zstd
// (klauspost/compress — no cgo, matching the modernc.org/sqlite driver choice),
// restoring a Pebble-comparable footprint.
//
// ONLY `details` is compressed. Every other column is queried, indexed, or
// searched (tier/type/level/source/operation_id/book_id/ts in WHERE and indexes;
// summary via instr(); tags via json_each(); src_key is the unique index), and
// each is tiny — compressing them would break querying for no gain.
package database

import (
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// activityDetailsCompressMin is the smallest details payload worth compressing.
// zstd frames carry a small fixed overhead, so tiny rows (the common case) would
// GROW; only the rare fat rows need compression and they dwarf this threshold.
const activityDetailsCompressMin = 512

// Stored `details` BLOBs begin with a 1-byte format tag so a reader never has to
// guess. These values are PERSISTED — never renumber them. A byte that is
// neither tag is treated as a legacy untagged raw-JSON value (see decode), which
// is safe because JSON never begins with 0x00/0x01.
const (
	detailsFmtRaw  byte = 0x00 // remaining bytes are the JSON payload verbatim
	detailsFmtZstd byte = 0x01 // remaining bytes are a zstd frame of the JSON
)

// Shared, concurrency-safe zstd codec. EncodeAll/DecodeAll are safe for
// concurrent use on a single Encoder/Decoder built with a nil io target, so one
// pair serves every activity write/read. Built once, lazily.
var (
	activityZstdOnce sync.Once
	activityZstdEnc  *zstd.Encoder
	activityZstdDec  *zstd.Decoder
)

func activityZstdCodec() (*zstd.Encoder, *zstd.Decoder) {
	activityZstdOnce.Do(func() {
		// SpeedBetterCompression matches internal/backup's default and crushes
		// the repetitive JSON dumps. The options are fixed, so the constructors
		// cannot fail here; the ignored errors are only ever non-nil for invalid
		// options, which these are not.
		activityZstdEnc, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
		activityZstdDec, _ = zstd.NewReader(nil)
	})
	return activityZstdEnc, activityZstdDec
}

// encodeActivityDetails turns marshaled details JSON into the value stored in the
// `details` column: a 1-byte format tag followed by the payload. Empty in → nil
// out, so the column stays NULL. Small payloads are stored raw (never grown);
// large payloads are zstd-compressed, but stored raw anyway if compression fails
// to shrink them, so the stored size never exceeds len(jsonBytes)+1.
func encodeActivityDetails(jsonBytes []byte) []byte {
	if len(jsonBytes) == 0 {
		return nil
	}
	if len(jsonBytes) < activityDetailsCompressMin {
		return append([]byte{detailsFmtRaw}, jsonBytes...)
	}
	enc, _ := activityZstdCodec()
	compressed := enc.EncodeAll(jsonBytes, nil)
	if len(compressed) >= len(jsonBytes) {
		// Incompressible (already-dense payload): keep it raw so we never store
		// more than the original plus the one tag byte.
		return append([]byte{detailsFmtRaw}, jsonBytes...)
	}
	return append([]byte{detailsFmtZstd}, compressed...)
}

// decodeActivityDetails reverses encodeActivityDetails, returning the JSON bytes
// ready to unmarshal. Empty in → nil out. A value whose first byte is neither
// format tag is a LEGACY untagged raw-JSON blob (written before this change);
// JSON never begins with 0x00/0x01, so returning it verbatim is unambiguous and
// lets a pre-compression database still read.
func decodeActivityDetails(stored []byte) ([]byte, error) {
	if len(stored) == 0 {
		return nil, nil
	}
	switch stored[0] {
	case detailsFmtRaw:
		return stored[1:], nil
	case detailsFmtZstd:
		_, dec := activityZstdCodec()
		out, err := dec.DecodeAll(stored[1:], nil)
		if err != nil {
			return nil, fmt.Errorf("sql_activity: decompress details: %w", err)
		}
		return out, nil
	default:
		return stored, nil // legacy untagged raw JSON
	}
}
