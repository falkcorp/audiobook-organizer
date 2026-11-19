// file: internal/plugins/dedup/kinds_labels_test.go
// version: 1.0.0
// guid: 89f35ea7-0555-45e0-aeeb-68bb008a75bd
// last-edited: 2026-11-18

package dedup

import "testing"

// F5-04 stub: the labels group's worker replaces this file.

func labelFakeDeps(t *testing.T) LabelKindDeps {
	t.Helper()
	return LabelKindDeps{}
}

func labelRows() []kindRow { return nil }

func labelWriteProbes(t *testing.T) []writeProbe { return nil }
