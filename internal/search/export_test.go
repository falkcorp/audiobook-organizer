// file: internal/search/export_test.go
// version: 1.0.0
// guid: 3b6f9d12-47a8-4e05-b1c3-8d2a5e7f0914
// last-edited: 2026-10-26

package search

import "slices"

// PerUserFieldNamesForTest lists the translator's per-user field names, sorted,
// for the external parity test.
func PerUserFieldNamesForTest() []string {
	names := make([]string, 0, len(perUserFieldSet))
	for name := range perUserFieldSet {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
