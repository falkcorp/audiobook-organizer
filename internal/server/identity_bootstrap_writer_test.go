// file: internal/server/identity_bootstrap_writer_test.go
// version: 0.1.0
// guid: d2f7a1c8-3b64-4e95-a0d7-81c5e9f3b246
// last-edited: 2026-11-21

package server

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// TestSeedRolesAndTokens_HEADWritesTheBootstrapTokenOnlyOffCoreIdentity pins the
// gate on HEAD's bootstrap-token writer. Under core identity the token file is
// core's (auth.BootWrites, Options.Auth.BootstrapTokenFile, the same name in the
// same directory); a second writer at server start would replace it on every
// restart with a token only the retired /auth/bootstrap handler can redeem, so the
// operator would hold a token nothing accepts. Off core identity HEAD's writer is
// the only one and must still run.
func TestSeedRolesAndTokens_HEADWritesTheBootstrapTokenOnlyOffCoreIdentity(t *testing.T) {
	for name, tc := range map[string]struct {
		opts      func(t *testing.T) []ServerOption
		wantToken bool
	}{
		"off core identity HEAD writes it": {
			opts:      func(*testing.T) []ServerOption { return nil },
			wantToken: true,
		},
		"under core identity only core does": {
			opts: func(t *testing.T) []ServerOption {
				return []ServerOption{WithIdentity(adoptIdentity(t, []string{"library.view"}, nil, nil, &database.User{ID: "u-bruce", Username: "bruce", Status: "active"}))}
			},
			wantToken: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			config.ResetStateDirForTest()
			t.Cleanup(config.ResetStateDirForTest)
			stateDir := t.TempDir()
			t.Setenv(config.SecureStateDirEnv, stateDir)

			store, err := database.NewPebbleStoreInMemory(t.TempDir() + "/bootstrap-writer.pebble")
			require.NoError(t, err)
			t.Cleanup(func() { store.Close() })
			s := newTestServerWith(t, store, tc.opts(t)...)

			s.seedRolesAndTokens()

			_, statErr := os.Stat(BootstrapTokenPath(stateDir))
			if tc.wantToken {
				require.NoError(t, statErr, "HEAD's writer did not write the token")
			} else {
				require.True(t, os.IsNotExist(statErr), "HEAD's writer wrote a token file under core identity (stat err = %v)", statErr)
			}
		})
	}
}
