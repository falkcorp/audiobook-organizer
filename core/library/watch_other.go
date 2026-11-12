// file: core/library/watch_other.go
// version: 1.0.0
// guid: f2def277-7f18-4c90-8a31-dfae002acdb1
// last-edited: 2026-11-11

//go:build !linux && !darwin

package library

// isNetworkFS is "no" where library has no statfs type to ask: the root is
// watched, and the overflow and add-failure paths still fall back to polling.
func isNetworkFS(string) bool { return false }
