// file: internal/plugins/dedup/kinds_index_test.go
// version: 1.0.0
// guid: 37db46af-5d7a-4d7a-9acd-1cd87ae9dd07
// last-edited: 2026-11-18

package dedup

import "testing"

// F5-04 stub: the index group's worker replaces this file.

func indexFakeDeps(t *testing.T) IndexKindDeps {
	t.Helper()
	return IndexKindDeps{}
}

func indexRows() []kindRow { return nil }

func indexWriteProbes(t *testing.T) []writeProbe { return nil }
