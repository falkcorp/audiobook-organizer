// file: internal/server/openlibrary_status_race_test.go
// version: 1.0.0
// guid: 7c2d9e41-5b3a-4806-a1f7-e0d4c8b26935
// last-edited: 2026-10-11

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/openlibrary"
)

// getOLStatus and OptimizeOLStore read the service's store while deleteOLData
// closes and nils it and EnsureStore reopens it. They used to read the
// Mu-guarded field without Mu (and the maintenance one read it twice, check
// then call), which -race flags and which can dereference nil. They now load
// the published pointer once, so the only outcomes are a store that answers or
// one that refuses: never a data race and never a panic.
func TestOLStoreReaders_RaceWithDeleteAndReopen(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dumpDir := filepath.Join(t.TempDir(), "ol-dumps")
	prev := config.AppConfig.OpenLibraryDumpDir
	config.AppConfig.OpenLibraryDumpDir = dumpDir
	t.Cleanup(func() { config.AppConfig.OpenLibraryDumpDir = prev })

	svc := &metafetch.OpenLibraryService{
		Tracker:   openlibrary.NewDownloadTracker(),
		Importing: map[string]bool{},
	}
	t.Cleanup(svc.Close)
	if err := svc.EnsureStore(dumpDir); err != nil {
		t.Fatal(err)
	}
	s := &Server{olService: svc}

	const cycles = 8
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 2 {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/openlibrary/status", nil)
				s.getOLStatus(c)
				if w.Code != http.StatusOK && w.Code != http.StatusInternalServerError {
					t.Errorf("getOLStatus returned %d", w.Code)
					return
				}
			}
		})
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				// An error from a store that is closing is expected; a panic or a
				// race is the failure.
				_ = s.OptimizeOLStore(context.Background())
			}
		})
	}

	for range cycles {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodDelete, "/api/v1/openlibrary/data", nil)
		s.deleteOLData(c)
		if w.Code != http.StatusOK {
			t.Errorf("deleteOLData returned %d: %s", w.Code, w.Body.String())
		}
		if err := svc.EnsureStore(dumpDir); err != nil {
			t.Errorf("reopening the store: %v", err)
		}
	}
	close(stop)
	readers.Wait()
}
