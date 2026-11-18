// file: internal/decisions/merge_cleanup_reasons_test.go
// version: 1.0.0
// guid: 3d8a5e17-9c40-4b62-a1f3-7e0c5b9d2a46
// last-edited: 2026-11-18

package decisions

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A reject of a merge-cleanup row (is this existing merge really wrong?) takes
// the split-books reasons, not the field-rewrite ones a plain `repairs:` kind
// gets, and the codes are the same list every other merge-shaped fixer offers.
func TestReasons_MergeCleanupIsMergeShaped(t *testing.T) {
	const kind = "repairs:maintenance.merge-cleanup"
	got := reasonCodes(Reasons(kind))
	require.Equal(t, reasonCodes(Reasons("repairs:duplicate-copies")), got)
	require.Contains(t, got, "distinct_titles")
	require.NotContains(t, got, "current_value_correct", "not the field-rewrite taxonomy")
	require.True(t, ReasonKnown(kind, "distinct_titles"))
}
