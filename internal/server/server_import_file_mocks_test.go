// file: internal/server/server_import_file_mocks_test.go
// version: 1.0.1
// guid: 1a2b3c4d-5e6f-7081-92a3-b4c5d6e7f8a9
// last-edited: 2026-01-24

//go:build mocks

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	metamocks "github.com/falkcorp/audiobook-organizer/internal/metadata/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	queuemocks "github.com/falkcorp/audiobook-organizer/internal/operations/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestImportFile_WithMockMetadata_CreateAuthorAndBook(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Supported extensions for the test
	config.AppConfig.SupportedExtensions = []string{".m4b", ".mp3"}

	// Temp file to satisfy os.Stat
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "import.m4b")
	require.NoError(t, os.WriteFile(testFile, []byte("audio"), 0o644))

	// Mocks
	mockStore := dbmocks.NewMockStore(t)
	database.SetGlobalStore(mockStore)
	t.Cleanup(func() { database.SetGlobalStore(nil) })

	mockMeta := metamocks.NewMockMetadataExtractor(t)
	metadata.SetMetadataExtractor(mockMeta)
	t.Cleanup(func() { metadata.SetMetadataExtractor(nil) })

	// Expectations: metadata returns title and artist; author missing -> create
	mockMeta.EXPECT().ExtractMetadata(testFile).Return(metadata.Metadata{
		Title:  "Meta Title",
		Artist: "Author Name",
	}, nil).Once()

	mockStore.EXPECT().GetAuthorByName("Author Name").Return(nil, assert.AnError).Once()
	mockStore.EXPECT().CreateAuthor("Author Name").Return(&database.Author{ID: 42, Name: "Author Name"}, nil).Once()

	mockStore.EXPECT().CreateBook(mock.Anything).Return(&database.Book{
		ID:       "B1",
		Title:    "Meta Title",
		FilePath: testFile,
	}, nil).Once()

	// Server
	srv := &Server{router: gin.New()}
	srv.setupRoutes()

	body := map[string]interface{}{"file_path": testFile}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/import/file", bytes.NewBuffer(buf))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "file imported successfully", resp["message"])
}

func TestImportFile_WithOrganize_QueuesOperation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	config.AppConfig.SupportedExtensions = []string{".m4b"}
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "import.m4b")
	require.NoError(t, os.WriteFile(testFile, []byte("audio"), 0o644))

	mockStore := dbmocks.NewMockStore(t)
	database.SetGlobalStore(mockStore)
	t.Cleanup(func() { database.SetGlobalStore(nil) })
	mockMeta := metamocks.NewMockMetadataExtractor(t)
	metadata.SetMetadataExtractor(mockMeta)
	t.Cleanup(func() { metadata.SetMetadataExtractor(nil) })
	mockQueue := queuemocks.NewMockQueue(t)
	server.queue = mockQueue
	t.Cleanup(func() { server.queue = nil })

	mockMeta.EXPECT().ExtractMetadata(testFile).Return(metadata.Metadata{Title: "Meta Title"}, nil).Once()
	mockStore.EXPECT().CreateBook(mock.Anything).Return(&database.Book{ID: "B2", Title: "Meta Title", FilePath: testFile}, nil).Once()

	// Organize true triggers CreateOperation + Enqueue
	mockStore.EXPECT().CreateOperation(mock.Anything, "organize", (*string)(nil)).Return(&database.Operation{ID: "op-org-import", Type: "organize"}, nil).Once()
	mockQueue.EXPECT().Enqueue(mock.Anything, "organize", operations.PriorityNormal, mock.AnythingOfType("operations.OperationFunc")).Return(nil).Once()

	srv := &Server{router: gin.New()}
	srv.setupRoutes()

	body := map[string]interface{}{"file_path": testFile, "organize": true}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/import/file", bytes.NewBuffer(buf))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "file imported successfully", resp["message"])
	// Ensure operation_id is present in response
	assert.NotNil(t, resp["operation_id"])
}

func TestImportFile_MetadataExtractError_Returns500(t *testing.T) {
	gin.SetMode(gin.TestMode)

	config.AppConfig.SupportedExtensions = []string{".m4b"}
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "import.m4b")
	require.NoError(t, os.WriteFile(testFile, []byte("audio"), 0o644))

	mockStore := dbmocks.NewMockStore(t)
	database.SetGlobalStore(mockStore)
	t.Cleanup(func() { database.SetGlobalStore(nil) })
	mockMeta := metamocks.NewMockMetadataExtractor(t)
	metadata.SetMetadataExtractor(mockMeta)
	t.Cleanup(func() { metadata.SetMetadataExtractor(nil) })

	mockMeta.EXPECT().ExtractMetadata(testFile).Return(metadata.Metadata{}, assert.AnError).Once()

	srv := &Server{router: gin.New()}
	srv.setupRoutes()

	body := map[string]interface{}{"file_path": testFile}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/import/file", bytes.NewBuffer(buf))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "failed to extract metadata")
}

func TestImportFile_CreateBookError_Returns500(t *testing.T) {
	gin.SetMode(gin.TestMode)

	config.AppConfig.SupportedExtensions = []string{".m4b"}
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "import.m4b")
	require.NoError(t, os.WriteFile(testFile, []byte("audio"), 0o644))

	mockStore := dbmocks.NewMockStore(t)
	database.SetGlobalStore(mockStore)
	t.Cleanup(func() { database.SetGlobalStore(nil) })
	mockMeta := metamocks.NewMockMetadataExtractor(t)
	metadata.SetMetadataExtractor(mockMeta)
	t.Cleanup(func() { metadata.SetMetadataExtractor(nil) })

	mockMeta.EXPECT().ExtractMetadata(testFile).Return(metadata.Metadata{Title: "Meta Title"}, nil).Once()
	mockStore.EXPECT().CreateBook(mock.Anything).Return((*database.Book)(nil), assert.AnError).Once()

	srv := &Server{router: gin.New()}
	srv.setupRoutes()

	body := map[string]interface{}{"file_path": testFile}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/import/file", bytes.NewBuffer(buf))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "failed to create book")
}

func TestImportFile_UnsupportedExtension_Returns400(t *testing.T) {
	gin.SetMode(gin.TestMode)

	config.AppConfig.SupportedExtensions = []string{".m4b"}
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "import.wav")
	require.NoError(t, os.WriteFile(testFile, []byte("audio"), 0o644))

	srv := &Server{router: gin.New()}
	srv.setupRoutes()

	body := map[string]interface{}{"file_path": testFile}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/import/file", bytes.NewBuffer(buf))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "unsupported file type")
}
