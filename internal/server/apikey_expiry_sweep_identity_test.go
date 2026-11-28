// file: internal/server/apikey_expiry_sweep_identity_test.go
// version: 1.0.0
// guid: da737141-20fa-49f6-9944-d5e30f0d761b
// last-edited: 2026-11-28

package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAPIKeyExpirySweep_DoesNotRunUnderCoreIdentity: the sweep lists books'
// k: rows, which are drained under core identity (keys are core's, M3), so it
// must not start its loop there. HEAD's server over the same store runs it until
// its context ends, which proves the fixture reaches the loop.
func TestAPIKeyExpirySweep_DoesNotRunUnderCoreIdentity(t *testing.T) {
	w := newSiteWorld(t, false, true)
	returned := func(s *Server) bool {
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.warnExpiringAPIKeys()
		}()
		select {
		case <-done:
			return true
		case <-time.After(500 * time.Millisecond):
			s.bgCancel() // end the loop so the goroutine does not outlive the test
			<-done
			return false
		}
	}
	require.False(t, returned(w.head), "precondition: HEAD's sweep runs until its context ends")
	assert.True(t, returned(w.core), "the key-expiry sweep started its loop under core identity")
}
