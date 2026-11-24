// file: internal/books/register_server_refusal_test.go
// version: 0.1.0
// guid: 7e2d9b4c-5a18-4c63-9f07-d1b8a3e6c452
// last-edited: 2026-11-22

package books

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/mediacore/app"
)

// errServerRefused is the refusal the stand-in constructor returns, standing for
// any of the conditions NewServer used to os.Exit on.
var errServerRefused = errors.New("Two-Face flipped the coin: the server refuses to build")

// TestRegister_ServerRefusalUnwinds is the production caller's half of
// "internal/server never exits": when NewServer refuses, Register returns an
// error that wraps the cause, app.New hands back no App (so nothing ran and the
// process lives on to assert it), the module holds no server, and Release
// closes the store Register had already opened.
//
// With a real store no server refusal is reachable through Register (the ABS
// ones are answered first by ValidateABSConfig), so the constructor is the
// package's newServer variable, as the settings boot's steps are variables.
func TestRegister_ServerRefusalUnwinds(t *testing.T) {
	for _, tc := range []struct {
		name string
		// build is the stand-in constructor. The second case returns a server
		// WITH its error, which NewServer's contract forbids: Register must not
		// adopt it either way, or Release would treat a refused server as live.
		build func(database.Store, ...server.ServerOption) (*server.Server, error)
	}{
		{"nil server and an error", func(database.Store, ...server.ServerOption) (*server.Server, error) {
			return nil, errServerRefused
		}},
		{"a server returned with its error is not adopted", func(database.Store, ...server.ServerOption) (*server.Server, error) {
			return &server.Server{}, errServerRefused
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mod, opts, _ := composedOptions(t, false, "", nil, nil)
			orig := newServer
			t.Cleanup(func() { newServer = orig })
			newServer = tc.build

			a, err := app.New(t.Context(), opts, mod)
			require.Error(t, err, "app.New booted over a server that refused to build")
			require.Nil(t, a, "a refused server must not leave an App to run")
			require.ErrorIs(t, err, errServerRefused, "the refusal's cause must reach the caller")
			assert.ErrorContains(t, err, "books:")
			assert.Nil(t, mod.srv.Load(), "Register kept a server whose constructor refused")

			// Register opened the store and published it as the process-global store
			// before NewServer ran. The kernel closes the core DB under it when app.New
			// fails, so the store's own writes are refused either way; what Release
			// adds, and what a leak would miss, is closeStore: it closes books' reader
			// side and unpublishes the global store.
			require.NotNil(t, mod.store.Load(), "setup: Register had not opened the store before NewServer ran")
			require.NotNil(t, database.GetGlobalStore(), "setup: the store was not published before NewServer ran")
			Release(mod)
			assert.Nil(t, database.GetGlobalStore(), "Release left the refused boot's store published as the global store")
			assert.Nil(t, mod.srv.Load())
		})
	}
}
