// file: internal/testutil/integration.go
// version: 1.8.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-07-01

package testutil

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/realtime"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/stretchr/testify/require"
)

// IntegrationEnv holds all resources for an integration test.
type IntegrationEnv struct {
	// Store is intentionally the full database.Store surface. Integration
	// tests poke at fixtures across any domain the scenario requires —
	// narrowing here forces churn in every test file for no real benefit
	// (see PR #394 for the regression this deliberate wide type prevents).
	Store     database.Store
	RootDir   string
	ImportDir string
	TempDir   string
	T         *testing.T
}

// SetupIntegration creates a real PebbleDB store, temp directories,
// and configures globals for integration testing.
func SetupIntegration(t *testing.T) (*IntegrationEnv, func()) {
	t.Helper()

	gin.SetMode(gin.TestMode)

	tmpBase := t.TempDir()
	dbPath := filepath.Join(tmpBase, "test.pebble")
	rootDir := filepath.Join(tmpBase, "library")
	importDir := filepath.Join(tmpBase, "import")

	require.NoError(t, os.MkdirAll(rootDir, 0755))
	require.NoError(t, os.MkdirAll(importDir, 0755))

	store, err := database.NewPebbleStore(dbPath)
	require.NoError(t, err)
	// PebbleStore.WaitForWarmup: NewPebbleStore starts the in-memory query
	// layer (memdb) warmup asynchronously. Without waiting here, a scan run
	// immediately after SetupIntegration can create a book via CreateBook
	// before warmup publishes its Pebble snapshot; CreateBook's memdb
	// write-through silently no-ops while mem()==nil (see memSync in
	// memdb_sync.go), and the eventual warmup snapshot can still miss a book
	// that was written mid-snapshot, permanently hiding it from GetAllBooks()
	// for the life of the store. On an idle dev machine warmup of a fresh,
	// near-empty DB finishes before the first write, masking the bug; under
	// CI/full-suite load the warmup goroutine can be scheduled late enough to
	// race a real scan. This was the root cause of the intermittent
	// TestScanService_MultiChapterAudiobook flake (asserted book count of 1,
	// occasionally observed 0 under load). See PebbleStore.WaitForWarmup doc.
	store.WaitForWarmup()

	err = database.RunMigrations(store)
	require.NoError(t, err)

	database.SetGlobalStore(store)
	// Wire the scanner's package-local store to the test store.
	// Previous test runs in the same process may have left a stale
	// pkgStore via scanner.SetStore from NewServer.
	scanner.SetStore(store)

	hub := realtime.NewEventHub()
	realtime.SetGlobalHub(hub)

	origCfg := config.Snapshot()
	// WHY Mutate: test setup writes the global AppConfig before starting any
	// background goroutines; using Mutate ensures the write is locked so that
	// concurrent cleanup from a prior test cannot observe a torn config.
	config.Mutate(func(c *config.Config) {
		*c = config.Config{
			DatabaseType:         "pebble",
			DatabasePath:         dbPath,
			RootDir:              rootDir,
			OrganizationStrategy: "copy",
			FolderNamingPattern:  "{author}/{title}",
			FileNamingPattern:    "{title}",
			SupportedExtensions:  []string{".m4b", ".mp3", ".m4a", ".flac", ".ogg"},
			AutoOrganize:         false,
		}
	})

	env := &IntegrationEnv{
		Store:     store,
		RootDir:   rootDir,
		ImportDir: importDir,
		TempDir:   tmpBase,
		T:         t,
	}

	cleanup := func() {
		// Clear scan hooks before closing the store. NewServer installs
		// hooks that hold a reference to the activity service / PebbleStore.
		// If a scan goroutine calls OnBookScanned after store.Close() it
		// panics ("pebble: closed"). Clearing early means hook calls are
		// no-ops instead of panics; activity loss during teardown is acceptable.
		scanner.SetScanHooks(nil)

		database.SetGlobalStore(nil)
		scanner.SetStore(nil)
		store.Close()
		// WHY Mutate: restore previous config under the write lock; other goroutines
		// may still be reading via Snapshot() as the test tears down.
		config.Mutate(func(c *config.Config) { *c = origCfg })
	}

	return env, cleanup
}

// CreateFakeAudiobook creates a minimal audiobook file in the given directory.
func (env *IntegrationEnv) CreateFakeAudiobook(dir, filename string) string {
	env.T.Helper()
	path := filepath.Join(dir, filename)
	require.NoError(env.T, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(env.T, os.WriteFile(path, []byte("fake-audiobook-data-"+filename), 0644))
	return path
}

// CopyFixture copies a real audio fixture to the target directory.
func (env *IntegrationEnv) CopyFixture(fixtureName, targetDir, targetName string) string {
	env.T.Helper()
	srcPath := filepath.Join(FindRepoRoot(env.T), "testdata", "fixtures", fixtureName)
	dstPath := filepath.Join(targetDir, targetName)
	require.NoError(env.T, os.MkdirAll(filepath.Dir(dstPath), 0755))
	data, err := os.ReadFile(srcPath)
	require.NoError(env.T, err, "fixture %s not found", fixtureName)
	require.NoError(env.T, os.WriteFile(dstPath, data, 0644))
	return dstPath
}

// FindRepoRoot walks up from CWD to find go.mod.
func FindRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.mod)")
		}
		dir = parent
	}
}

// WaitForOp polls until an operation completes or times out.
//
// Checks v2 first (operations enqueued via opRegistry.EnqueueOp land
// in the operations_v2 table) and falls back to v1 for legacy paths.
// Pass a database.Store (which embeds OpsV2Store) so v2 lookups work;
// the parameter type stays OperationStore for source-compat.
func WaitForOp(t *testing.T, store database.OperationStore, opID string, timeout time.Duration) {
	t.Helper()
	require.Eventually(t, func() bool {
		if v2, ok := store.(database.OpsV2Store); ok {
			if row, err := v2.GetOperationV2(opID); err == nil && row != nil {
				switch row.Status {
				case "completed", "failed", "canceled":
					return true
				}
			}
		}
		op, err := store.GetOperationByID(opID)
		return err == nil && op != nil && (op.Status == "completed" || op.Status == "failed")
	}, timeout, 100*time.Millisecond)
}

// CopyFile copies a file from src to dst.
func CopyFile(t *testing.T, src, dst string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0755))
	data, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dst, data, 0644))
}
