// file: internal/server/handlers/system/handler_factoryreset_ol_test.go
// version: 1.0.0
// guid: 5e1b7a94-2d6c-4f03-9a8e-c4b0d7e3f125
// last-edited: 2026-10-11

package system_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/openlibrary"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/system"
	systemmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/system/mocks"
)

// newFactoryResetHandler builds a handler around a real Open Library service,
// opened on a pebble engine under a temp dump dir. It returns the service, the
// dump dir and a straggler: an iterator the caller holds to keep the engine's
// close from draining.
func newFactoryResetHandler(t *testing.T) (*system.Handler, *metafetch.OpenLibraryService, string, *database.GatedIter) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dumpDir := filepath.Join(t.TempDir(), "ol-dumps")
	prevDir, prevRoot := config.AppConfig.OpenLibraryDumpDir, config.AppConfig.RootDir
	config.AppConfig.OpenLibraryDumpDir = dumpDir
	config.AppConfig.RootDir = "" // skip the library-folder clear branch
	t.Cleanup(func() {
		config.AppConfig.OpenLibraryDumpDir = prevDir
		config.AppConfig.RootDir = prevRoot
	})

	db, err := pebble.Open(filepath.Join(dumpDir, "oldb"), &pebble.Options{FormatMajorVersion: pebble.FormatNewest})
	require.NoError(t, err)
	gated, closeDB := database.NewOwnedGatedDB(db)
	it, err := gated.NewIter(&pebble.IterOptions{})
	require.NoError(t, err)
	it.First()

	svc := &metafetch.OpenLibraryService{
		OLStore:      openlibrary.NewOLStoreOnEngine(gated, closeDB),
		Tracker:      openlibrary.NewDownloadTracker(),
		Importing:    map[string]bool{},
		CloseTimeout: 50 * time.Millisecond,
	}

	store := systemmocks.NewMockSystemStore(t)
	store.EXPECT().Reset().Return(nil)
	store.EXPECT().InvalidateLibraryStats().Return().Maybe()
	store.EXPECT().SetSetting(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	store.EXPECT().GetSetting(mock.Anything).Return(nil, nil).Maybe()
	store.EXPECT().DeleteSetting(mock.Anything).Return(nil).Maybe()

	h := system.New(
		func() system.SystemStore { return store },
		systemmocks.NewMockSystemService(t),
		systemmocks.NewMockConfigUpdateService(t),
		systemmocks.NewMockPluginHealthChecker(t),
		func() system.EventStreamer { return systemmocks.NewMockEventStreamer(t) },
		systemmocks.NewMockOperationLogsProvider(t),
		svc,
		func(string) (uint64, uint64, error) { return 1000, 400, nil },
		func() {},
		func() string { return "test-version" },
		func(g []dedup.AuthorDedupGroup) []dedup.AuthorDedupGroup { return g },
		nil,
	)
	return h, svc, dumpDir, it
}

func postFactoryReset(h *system.Handler) int {
	w := run(http.MethodPost, "/system/factory-reset", "/system/factory-reset", []byte(`{"confirm":"RESET"}`), func(r *gin.Engine) {
		r.POST("/system/factory-reset", h.FactoryReset)
	})
	return w.Code
}

// Factory reset must never RemoveAll the Open Library directory while its
// engine is still open: a lookup or import that outlives the close timeout
// leaves the engine open, and deleting the directory then pulls files out
// from under it. The rest of the reset still goes on (200), the data is kept,
// and the service keeps the half-closed store so the next import finishes the
// close once the straggler lets go.
func TestFactoryReset_KeepsOLDataWhileTheEngineIsStillOpen(t *testing.T) {
	h, svc, dumpDir, it := newFactoryResetHandler(t)

	for attempt := 1; attempt <= 2; attempt++ {
		assert.Equal(t, http.StatusOK, postFactoryReset(h), "reset #%d: the rest of the reset must still go on", attempt)
		_, err := os.Stat(filepath.Join(dumpDir, "oldb"))
		require.NoErrorf(t, err, "reset #%d removed the Open Library data while its engine was open", attempt)
	}
	assert.Nil(t, svc.LiveStore(), "a store whose close began must not be served to lookups")

	// The straggler lets go: the next EnsureStore finishes the pending close
	// and reopens, which only works if the engine's lock was released rather
	// than the directory having been pulled out from under it.
	require.NoError(t, it.Close())
	svc.Mu.Lock()
	svc.CloseTimeout = 0
	svc.Mu.Unlock()
	require.NoError(t, svc.EnsureStore(dumpDir))
	require.NotNil(t, svc.LiveStore())
	svc.Close()
}

// With nothing holding the engine, factory reset closes it and deletes the
// Open Library data.
func TestFactoryReset_DeletesOLDataAfterACleanClose(t *testing.T) {
	h, svc, dumpDir, it := newFactoryResetHandler(t)
	require.NoError(t, it.Close())
	svc.Mu.Lock()
	svc.CloseTimeout = 0
	svc.Mu.Unlock()

	assert.Equal(t, http.StatusOK, postFactoryReset(h))
	_, err := os.Stat(dumpDir)
	assert.True(t, os.IsNotExist(err), "data not removed after a clean close (stat error %v)", err)
	assert.Nil(t, svc.LiveStore())
}
