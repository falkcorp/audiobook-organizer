// file: internal/tools/downloader_test.go
// version: 1.0.0
// guid: c9d0e1f2-a3b4-5678-cdef-678901234567
// last-edited: 2026-06-15

package tools_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownload_Success(t *testing.T) {
	content := []byte("fake-binary-content")
	sum := sha256.Sum256(content)
	hexSum := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	def := tools.ToolDef{
		Name: "fpcalc",
		Release: tools.ToolRelease{
			Version:     "1.5.1",
			URLTemplate: srv.URL + "/fpcalc.tar.gz",
			SHA256:      map[string]string{"linux/amd64": hexSum, "darwin/arm64": hexSum},
		},
	}

	destDir := t.TempDir()
	path, err := tools.Download(t.Context(), def, destDir, nil)
	require.NoError(t, err)
	assert.FileExists(t, path)
	got, _ := os.ReadFile(path)
	assert.Equal(t, content, got)
}

func TestDownload_ChecksumMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("corrupted"))
	}))
	defer srv.Close()

	def := tools.ToolDef{
		Name: "fpcalc",
		Release: tools.ToolRelease{
			Version:     "1.5.1",
			URLTemplate: srv.URL + "/fpcalc.tar.gz",
			SHA256:      map[string]string{"linux/amd64": "0000000000000000000000000000000000000000000000000000000000000000", "darwin/arm64": "0000000000000000000000000000000000000000000000000000000000000000"},
		},
	}

	_, err := tools.Download(t.Context(), def, t.TempDir(), nil)
	assert.ErrorContains(t, err, "checksum mismatch")
}
