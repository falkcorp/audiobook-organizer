// file: internal/dedup/op_params.go
// version: 1.2.0
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901
// last-edited: 2026-08-23

// Package dedup: op_params.go defines the JSON-unmarshal parameter structs for
// each of the 8 async dedup OperationDef Run functions. They are kept here so
// the extracted logic functions can reference them directly and so server-side
// wrappers can unmarshal into the same types.
//
// None of these carry an operation id. Every dedup op is v2-native: the id it
// logs, keys activity under, and stamps onto OperationChange ledger rows is the
// v2 run's own id, read inside Run via opsregistry.ReporterOpID(reporter). The
// `legacy_op_id` field these structs used to carry was minted by the HTTP
// handler for a v1 operation row that no longer exists.
//
// Four of the eight are empty structs. That is deliberate and they still get
// decoded: the registry contract requires every op to REFUSE malformed params,
// so the json.Unmarshal in each Run body is load-bearing even when there is
// nothing to unmarshal into. Do not delete those decodes.
package dedup

// BookDedupScanParams are the parameters for the "dedup.book-scan" operation.
type BookDedupScanParams struct{}

// BookMergeParams are the parameters for the "dedup.book-merge" operation.
type BookMergeParams struct {
	KeepID   string   `json:"keep_id"`
	MergeIDs []string `json:"merge_ids"`
	Detail   string   `json:"detail"`
}

// AuthorDedupScanParams are the parameters for the "dedup.author-scan" operation.
type AuthorDedupScanParams struct{}

// SeriesDedupScanParams are the parameters for the "dedup.series-scan" operation.
type SeriesDedupScanParams struct{}

// SeriesDedupParams are the parameters for the "dedup.series-dedup" operation.
type SeriesDedupParams struct {
	Detail string `json:"detail"`

	// DryRun reports what WOULD be merged without writing it. Defaults to
	// TRUE when absent (nil), matching
	// maintenance.author-conjunction-repair: the op DELETES series rows, and
	// until TODO.md L3966 it had no preview at all. Pass dry_run=false to
	// apply. Pointer, not bool, so "absent" and "explicitly false" stay
	// distinguishable — a plain bool would make the safe default unreachable.
	DryRun *bool `json:"dry_run,omitempty"`
}

// SeriesPruneParams are the parameters for the "dedup.series-prune" operation.
type SeriesPruneParams struct {
	Detail string `json:"detail"`
}

// SeriesMergeParams are the parameters for the "dedup.series-merge" operation.
type SeriesMergeParams struct {
	KeepID     int    `json:"keep_id"`
	MergeIDs   []int  `json:"merge_ids"`
	CustomName string `json:"custom_name"`
	Detail     string `json:"detail"`
}

// SeriesNormalizeParams are the parameters for the "dedup.series-normalize" operation.
type SeriesNormalizeParams struct{}
