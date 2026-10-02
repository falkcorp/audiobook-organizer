// file: internal/compactprogress/stats.go
// version: 2.0.0
// guid: 6b2e4f0a-8c1d-4e7a-9f35-2d6c8a1b7e40
// last-edited: 2026-10-02

// Package compactprogress reports what a long Pebble compaction is doing
// while it runs.
//
// A full-keyspace `db.Compact` is one blocking call with no callback. On
// production it takes ~28 minutes, and until this package existed the
// db-optimize ops showed "Optimizing main database (0/3)" for that whole time
// and nothing else. The engine does expose live counters through
// `(*pebble.DB).Metrics()` (sampled by database.CollectCompactionStats); this
// package samples them on a ticker while the compaction runs and turns them
// into operator-readable progress lines.
//
// The dependency points one way: this package imports internal/database,
// and nothing under internal/database imports this package. internal/database
// is in the plugin SDK's dependency closure (make sdkguard); the sampler is not.
package compactprogress

import "github.com/falkcorp/audiobook-organizer/internal/database"

// Stats is the store-side counter snapshot. An alias, so a store's
// CompactionStats method can be handed straight to Step.Stats.
type Stats = database.CompactionStats
