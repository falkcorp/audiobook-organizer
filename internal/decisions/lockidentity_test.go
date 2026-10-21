// file: internal/decisions/lockidentity_test.go
// version: 1.0.0
// guid: 6f2a9d41-8b07-4e53-a1c6-3d95e0b74f28
// last-edited: 2026-10-21

package decisions

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A wrapper of a DIFFERENT store has a different identity: the two do not
// serialise against each other (they write different keys).
func TestLockIdentity_DistinctStoresDiffer(t *testing.T) {
	a, b := newKV(t), newKV(t)
	require.NotEqual(t, a.LockIdentity(), b.LockIdentity())
	require.Equal(t, a.LockIdentity(), (&slowKV{a}).LockIdentity(), "a wrapper inherits the store's identity")
	require.Equal(t, lockScope(a), lockScope(&slowKV{a}))
	require.NotEqual(t, lockScope(a), lockScope(b))
}
