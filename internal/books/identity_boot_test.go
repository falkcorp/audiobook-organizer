// file: internal/books/identity_boot_test.go
// version: 0.1.0
// guid: 5d9a2e74-1c83-4b06-a7f2-8e3b6c01d957
// last-edited: 2026-11-21

package books

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// bootIdentity is a real composed boot (core's kernel, books' Register, the
// settings boot) with the identity predicate on and sign-in required.
func bootIdentity(t *testing.T, edit func(*config.Config)) *composed {
	t.Helper()
	t.Cleanup(SetIdentityFlipForTest(true))
	return bootComposedWith(t, false, "", func(c *config.Config) {
		c.EnableAuth = true
		if edit != nil {
			edit(c)
		}
	}, nil, nil)
}

func TestIdentityBoot_Boots(t *testing.T) {
	c := bootIdentity(t, nil)
	require.True(t, c.srv.UnderCoreIdentity())
}
