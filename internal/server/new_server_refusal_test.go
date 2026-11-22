// file: internal/server/new_server_refusal_test.go
// version: 0.1.0
// guid: c4b7e1a9-2d56-4e83-b0f1-8a39d7c5e264
// last-edited: 2026-11-22

package server

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// NewServer used to os.Exit(1) at each of these conditions, from inside a
// library constructor, with no unwind of the kernel. Each cell below drives one
// of them and asserts NewServer returns an error naming the cause and that the
// process lives on to assert it. A cell runs in a child test process: a
// NewServer that exits again would otherwise end this test binary, and the
// failure would be a dead package rather than a named test. The parent reads
// the child's exit status, so a re-added os.Exit fails the parent cell by name.

const refusalChildEnv = "ABO_TEST_NEWSERVER_REFUSAL_CASE"

// refusalCase is one former exit site: run drives the condition in the child.
type refusalCase struct {
	name string
	run  func(t *testing.T)
}

// refusalConfig installs a server config for the cell and restores it after.
func refusalConfig(t *testing.T, edit func(*config.Config)) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	installServerConfigFacade(t)
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig = config.Config{
		DatabaseType: "pebble",
		RootDir:      t.TempDir(),
	}
	if edit != nil {
		edit(&config.AppConfig)
	}
}

func refusalStore(t *testing.T) database.Store {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "refusal.pebble"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// observerCountingStore is a store that hides the PebbleStore beneath it
// (opaqueStore) and reports its change-observer installs and releases.
type observerCountingStore struct {
	opaqueStore
	set, cleared atomic.Int32
}

func (s *observerCountingStore) SetChangeObserver(database.ChangeObserver) { s.set.Add(1) }

func (s *observerCountingStore) ClearChangeObserver(database.ChangeObserver) { s.cleared.Add(1) }

var refusalCases = []refusalCase{
	{"abs jwt secret empty", func(t *testing.T) {
		// wire_abs_routes.go's first exit: the ABS API on with no ABS_JWT_SECRET.
		// books' Register checks this ahead of NewServer (ValidateABSConfig), so
		// this is NewServer's own answer for every other caller.
		refusalConfig(t, func(c *config.Config) { c.ABSAPIEnabled = true; c.ABSJWTSecret = "" })
		store := refusalStore(t)
		srv, err := NewServer(store)
		require.Error(t, err, "an enabled ABS surface with no JWT secret must refuse the boot")
		require.Nil(t, srv, "a refused NewServer hands back no server")
		require.ErrorContains(t, err, "ABS_JWT_SECRET")
		// Released, not leaked: with the surface off the same store builds a server.
		config.AppConfig.ABSAPIEnabled = false
		srv, err = NewServer(store)
		require.NoError(t, err, "a refused NewServer left the store unable to build the next server")
		srv.ReleaseUnstarted()
	}},
	{"abs store lacks the session keyspace", func(t *testing.T) {
		// A valid ABS block over a store that hides the PebbleStore beneath it
		// (opaqueStore has no Unwrap): the session-keyspace assertion refuses.
		refusalConfig(t, func(c *config.Config) {
			c.ABSAPIEnabled = true
			c.ABSJWTSecret = strings.Repeat("batcave-", 6)
		})
		store := &observerCountingStore{opaqueStore: opaqueStore{Store: refusalStore(t)}}
		srv, err := NewServer(store)
		require.Nil(t, srv)
		require.ErrorIs(t, err, errABSNoSessionKeyspace)
		// The refusal came after NewServer had installed its change observer on
		// the store; the half-built server was released, so it is detached.
		require.EqualValues(t, 1, store.set.Load(), "setup: NewServer installs one change observer before the ABS wiring refuses")
		require.EqualValues(t, 1, store.cleared.Load(), "a refused NewServer left its change observer installed on the store")
	}},
	{"nil store", func(t *testing.T) {
		refusalConfig(t, nil)
		srv, err := NewServer(nil)
		require.Nil(t, srv)
		require.ErrorIs(t, err, errNewServerNilStore)
	}},
	{"incomplete identity", func(t *testing.T) {
		refusalConfig(t, nil)
		srv, err := NewServer(refusalStore(t), WithIdentity(&legacyauth.Identity{}))
		require.Nil(t, srv)
		require.ErrorContains(t, err, "identity wiring is incomplete")
		require.ErrorContains(t, err, "no bridge", "the error must name the missing member")
	}},
}

// TestNewServer_RefusalReturnsErrorAndProcessSurvives runs each refusal cell in
// a child test process and requires the child to finish its assertions with a
// zero exit status.
func TestNewServer_RefusalReturnsErrorAndProcessSurvives(t *testing.T) {
	if os.Getenv(refusalChildEnv) != "" {
		t.Skip("the child half runs it (TestNewServer_RefusalChild)")
	}
	for _, tc := range refusalCases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestNewServer_RefusalChild$", "-test.v", "-test.count=1")
			cmd.Env = append(os.Environ(), refusalChildEnv+"="+tc.name)
			out, err := cmd.CombinedOutput()
			if ee, ok := errors.AsType[*exec.ExitError](err); ok {
				t.Fatalf("the child process exited with status %d: NewServer ended the process (os.Exit) or the cell failed:\n%s", ee.ExitCode(), out)
			}
			require.NoError(t, err, "starting the child process: %s", out)
			require.Contains(t, string(out), "--- PASS: TestNewServer_RefusalChild",
				"the child did not run its assertions:\n%s", out)
		})
	}
}

// TestNewServer_RefusalChild is one cell of the test above, chosen by the
// environment. Alone it is skipped.
func TestNewServer_RefusalChild(t *testing.T) {
	name := os.Getenv(refusalChildEnv)
	if name == "" {
		t.Skip("run by TestNewServer_RefusalReturnsErrorAndProcessSurvives")
	}
	for _, tc := range refusalCases {
		if tc.name == name {
			tc.run(t)
			return
		}
	}
	t.Fatalf("no refusal case %q", name)
}
