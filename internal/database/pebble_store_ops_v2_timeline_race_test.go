// file: internal/database/pebble_store_ops_v2_timeline_race_test.go
// version: 1.0.0
// guid: 1d3ffa4a-aba1-4a81-b176-d57a4efa6799
// last-edited: 2026-10-04

//go:build race

package database

// opsV2TimelineRaceBuild is true when the test binary is built with -race.
// TestOpsV2Timeline_EquivalenceRandomHistories caps its seed count with it:
// 200 seeds under the race detector take ~9 min on a loaded machine, too
// close to go test's 10 min default.
const opsV2TimelineRaceBuild = true
