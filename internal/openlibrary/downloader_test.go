// file: internal/openlibrary/downloader_test.go
// version: 1.1.1
// guid: a7b8c9d0-e1f2-3a4b-5c6d-7e8f9a0b1c2d

package openlibrary

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDumpFilename(t *testing.T) {
	assert.Equal(t, "ol_dump_editions_latest.txt.gz", DumpFilename("editions"))
	assert.Equal(t, "ol_dump_authors_latest.txt.gz", DumpFilename("authors"))
	assert.Equal(t, "ol_dump_works_latest.txt.gz", DumpFilename("works"))
}

func TestDownloadFromHTTPServer(t *testing.T) {
	content := []byte("fake dump content for testing")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer server.Close()

	dir := t.TempDir()
	targetPath := filepath.Join(dir, "test_dump.txt.gz")

	resp, err := http.Get(server.URL)
	require.NoError(t, err)

	f, err := os.Create(targetPath)
	require.NoError(t, err)
	_, err = f.ReadFrom(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	f.Close()

	data, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	assert.Equal(t, content, data)
}

func TestDownloadTracker(t *testing.T) {
	tracker := NewDownloadTracker()

	// Initial state is idle
	p := tracker.Get("editions")
	assert.Equal(t, "idle", p.Status)
	assert.Equal(t, int64(-1), p.TotalSize)

	// Set progress
	tracker.set("editions", &DownloadProgress{
		DumpType: "editions", Status: "downloading",
		Downloaded: 1024, TotalSize: 2048,
	})

	p = tracker.Get("editions")
	assert.Equal(t, "downloading", p.Status)
	assert.Equal(t, int64(1024), p.Downloaded)
	assert.Equal(t, int64(2048), p.TotalSize)

	// GetAll
	all := tracker.GetAll()
	assert.Len(t, all, 1)
	assert.Equal(t, "downloading", all["editions"].Status)
}

func TestDumpSources(t *testing.T) {
	assert.GreaterOrEqual(t, len(DumpSources), 2, "should have at least 2 download sources")
	assert.Contains(t, DumpSources[0], "openlibrary.org")
	assert.Contains(t, DumpSources[1], "archive.org")
}
