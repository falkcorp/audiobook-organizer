// file: internal/repairs/owner_ctx_test.go
// version: 1.0.1
// guid: 3f0c9d52-7b1e-4a68-9c24-d5e8a1b6f073
// last-edited: 2026-11-03

package repairs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// The marker names exactly one row: an unmarked ctx, an empty row id and the
// marker of another row all read as no approval.
func TestOwnerApplyFor_NamesExactlyOneRow(t *testing.T) {
	require.False(t, OwnerApplyFor(context.Background(), "folder:year-one"))
	//lint:ignore SA1012 a nil ctx must read as no approval, not panic
	require.False(t, OwnerApplyFor(nil, "folder:year-one"))
	marked := WithOwnerApply(context.Background(), "folder:year-one")
	require.True(t, OwnerApplyFor(marked, "folder:year-one"))
	require.False(t, OwnerApplyFor(marked, "folder:hush"))
	require.False(t, OwnerApplyFor(marked, ""))
	require.False(t, OwnerApplyFor(WithOwnerApply(context.Background(), ""), ""))
}
