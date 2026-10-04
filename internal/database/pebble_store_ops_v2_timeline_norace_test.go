// file: internal/database/pebble_store_ops_v2_timeline_norace_test.go
// version: 1.0.0
// guid: b3b7809a-e09a-4ab3-bc43-004c799c7c2c
// last-edited: 2026-10-04

//go:build !race

package database

// opsV2TimelineRaceBuild is false in a build without -race (see the race twin).
const opsV2TimelineRaceBuild = false
