// file: internal/plugins/dedup/kinds_chain_test.go
// version: 1.0.0
// guid: 6d01f54f-d1e9-4cf7-8def-037dd38e3e04
// last-edited: 2026-11-18

package dedup

import "testing"

// F5-04 stub: the chain group's worker replaces this file.

func chainFakeDeps(t *testing.T) ChainKindDeps {
	t.Helper()
	return ChainKindDeps{}
}

func chainRows() []kindRow { return nil }

func chainWriteProbes(t *testing.T) []writeProbe { return nil }

// chainChildWaitChecks is TestDedupKinds_ChildWaitsUseAwait's body for the
// chain group (run-all and label-refinement).
func chainChildWaitChecks(t *testing.T) {
	t.Helper()
}
