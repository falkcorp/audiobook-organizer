// file: internal/diagnostics/service_test.go
// version: 1.4.0
// guid: d1a9n0st-1cs0-t3st-s3rv-1c3t3st0001
// last-edited: 2026-08-22

package diagnostics

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupDiagnosticsMocks(t *testing.T) *dbmocks.MockStore {
	store := dbmocks.NewMockStore(t)
	store.EXPECT().GetAllBooksCore(10000, 0).Return([]database.BookCore{
		{ID: "book1", Title: "Test Book"},
	}, nil).Maybe()
	store.EXPECT().GetAllBooksCore(10000, 10000).Return([]database.BookCore{}, nil).Maybe()
	store.EXPECT().GetAllAuthors().Return([]database.Author{}, nil).Maybe()
	store.EXPECT().GetAllSeries().Return([]database.Series{}, nil).Maybe()
	store.EXPECT().GetAllAuthorBookCounts().Return(map[int]int{}, nil).Maybe()
	store.EXPECT().GetAllSeriesBookCounts().Return(map[int]int{}, nil).Maybe()
	store.EXPECT().GetAllAuthorFileCounts().Return(map[int]int{}, nil).Maybe()
	store.EXPECT().GetAllSeriesFileCounts().Return(map[int]int{}, nil).Maybe()
	store.EXPECT().CountPrimaryBooks().Return(1, nil).Maybe()
	store.EXPECT().CountAuthors().Return(0, nil).Maybe()
	store.EXPECT().CountSeries().Return(0, nil).Maybe()
	store.EXPECT().GetSystemActivityLogs("", 10000).Return(nil, nil).Maybe()
	store.EXPECT().GetRecentOperations(100).Return(nil, nil).Maybe()
	return store
}

func readZipFile(t *testing.T, r *zip.ReadCloser, name string) []byte {
	t.Helper()
	for _, f := range r.File {
		if f.Name == name {
			rc, err := f.Open()
			require.NoError(t, err)
			defer rc.Close()
			data, err := io.ReadAll(rc)
			require.NoError(t, err)
			return data
		}
	}
	t.Fatalf("file %s not found in ZIP", name)
	return nil
}

func TestService_GenerateExport_Deduplication(t *testing.T) {
	store := setupDiagnosticsMocks(t)

	svc := NewService(store, nil, "")
	zipPath, err := svc.GenerateExport(context.Background(), "deduplication", "test export", nil)
	require.NoError(t, err)
	defer os.Remove(zipPath)

	r, err := zip.OpenReader(zipPath)
	require.NoError(t, err)
	defer r.Close()

	fileNames := make(map[string]bool)
	for _, f := range r.File {
		fileNames[f.Name] = true
	}

	// Common files always present
	assert.True(t, fileNames["system_info.json"], "missing system_info.json")
	assert.True(t, fileNames["books.json"], "missing books.json")
	assert.True(t, fileNames["authors.json"], "missing authors.json")
	assert.True(t, fileNames["series.json"], "missing series.json")
	assert.True(t, fileNames["batch.jsonl"], "missing batch.jsonl")

	// Dedup-specific files
	assert.True(t, fileNames["version_groups.json"], "missing version_groups.json")
	assert.True(t, fileNames["itunes_albums.json"], "missing itunes_albums.json")

	// Should NOT have error_analysis files
	assert.False(t, fileNames["logs.json"], "should not have logs.json for deduplication")
	assert.False(t, fileNames["operations.json"], "should not have operations.json for deduplication")

	// Verify system_info content
	data := readZipFile(t, r, "system_info.json")
	var sysInfo map[string]any
	require.NoError(t, json.Unmarshal(data, &sysInfo))
	assert.Equal(t, "deduplication", sysInfo["category"])
	assert.Equal(t, "test export", sysInfo["description"])
	assert.Equal(t, float64(1), sysInfo["book_count"])

	// Verify books.json has our test book
	booksData := readZipFile(t, r, "books.json")
	var books []map[string]any
	require.NoError(t, json.Unmarshal(booksData, &books))
	require.Len(t, books, 1)
	assert.Equal(t, "book1", books[0]["id"])
	assert.Equal(t, "Test Book", books[0]["title"])
}

func TestService_GenerateExport_ErrorAnalysis(t *testing.T) {
	store := setupDiagnosticsMocks(t)

	now := time.Now()
	store.EXPECT().GetSystemActivityLogs("", 10000).Unset()
	store.EXPECT().GetSystemActivityLogs("", 10000).Return([]database.SystemActivityLog{
		{ID: 1, Source: "scanner", Level: "error", Message: "scan failed", CreatedAt: now},
		{ID: 2, Source: "scanner", Level: "info", Message: "old log", CreatedAt: now.Add(-48 * time.Hour)},
	}, nil).Maybe()

	svc := NewService(store, nil, "")
	zipPath, err := svc.GenerateExport(context.Background(), "error_analysis", "debug errors", nil)
	require.NoError(t, err)
	defer os.Remove(zipPath)

	r, err := zip.OpenReader(zipPath)
	require.NoError(t, err)
	defer r.Close()

	fileNames := make(map[string]bool)
	for _, f := range r.File {
		fileNames[f.Name] = true
	}

	// Error analysis specific files
	assert.True(t, fileNames["logs.json"], "missing logs.json")
	assert.True(t, fileNames["operations.json"], "missing operations.json")

	// Should NOT have dedup files
	assert.False(t, fileNames["version_groups.json"], "should not have version_groups.json for error_analysis")
	assert.False(t, fileNames["itunes_albums.json"], "should not have itunes_albums.json for error_analysis")

	// Verify logs are filtered to last 24h
	logsData := readZipFile(t, r, "logs.json")
	var logs []database.SystemActivityLog
	require.NoError(t, json.Unmarshal(logsData, &logs))
	assert.Len(t, logs, 1, "should only include logs from last 24h")
	assert.Equal(t, "scan failed", logs[0].Message)
}

func TestService_GenerateExport_MetadataQuality(t *testing.T) {
	store := setupDiagnosticsMocks(t)

	authorID := 1
	seriesID := 1
	store.EXPECT().GetAllBooksCore(10000, 0).Unset()
	store.EXPECT().GetAllBooksCore(10000, 0).Return([]database.BookCore{
		{ID: "book1", Title: "Complete Book", AuthorID: &authorID, SeriesID: &seriesID},
		{ID: "book2", Title: "", AuthorID: nil},          // missing title, author, series
		{ID: "book3", Title: "No Author", AuthorID: nil}, // missing author, series
	}, nil).Maybe()
	store.EXPECT().GetAllBooksCore(10000, 10000).Unset()
	store.EXPECT().GetAllBooksCore(10000, 10000).Return([]database.BookCore{}, nil).Maybe()
	store.EXPECT().CountPrimaryBooks().Unset()
	store.EXPECT().CountPrimaryBooks().Return(3, nil).Maybe()

	svc := NewService(store, nil, "")
	zipPath, err := svc.GenerateExport(context.Background(), "metadata_quality", "check quality", nil)
	require.NoError(t, err)
	defer os.Remove(zipPath)

	r, err := zip.OpenReader(zipPath)
	require.NoError(t, err)
	defer r.Close()

	fileNames := make(map[string]bool)
	for _, f := range r.File {
		fileNames[f.Name] = true
	}

	assert.True(t, fileNames["missing_fields.json"], "missing missing_fields.json")

	// Verify missing_fields content
	data := readZipFile(t, r, "missing_fields.json")
	var missingFields []map[string]any
	require.NoError(t, json.Unmarshal(data, &missingFields))
	// book2 missing title+author+series, book3 missing author+series
	assert.Len(t, missingFields, 2, "should have 2 books with missing fields")
}

func TestService_GenerateExport_General(t *testing.T) {
	store := setupDiagnosticsMocks(t)

	svc := NewService(store, nil, "")
	zipPath, err := svc.GenerateExport(context.Background(), "general", "full export", nil)
	require.NoError(t, err)
	defer os.Remove(zipPath)

	r, err := zip.OpenReader(zipPath)
	require.NoError(t, err)
	defer r.Close()

	fileNames := make(map[string]bool)
	for _, f := range r.File {
		fileNames[f.Name] = true
	}

	// General includes everything
	assert.True(t, fileNames["system_info.json"])
	assert.True(t, fileNames["books.json"])
	assert.True(t, fileNames["authors.json"])
	assert.True(t, fileNames["series.json"])
	assert.True(t, fileNames["batch.jsonl"])
	assert.True(t, fileNames["logs.json"], "general should include logs.json")
	assert.True(t, fileNames["operations.json"], "general should include operations.json")
	assert.True(t, fileNames["version_groups.json"], "general should include version_groups.json")
	assert.True(t, fileNames["itunes_albums.json"], "general should include itunes_albums.json")
	assert.True(t, fileNames["missing_fields.json"], "general should include missing_fields.json")
}

func TestBuildBatchJSONL(t *testing.T) {
	books := []SlimBook{
		{ID: "b1", Title: "Book One", Format: "mp3"},
	}
	data, err := BuildBatchJSONL("deduplication", "test", books, nil, nil, nil)
	require.NoError(t, err)
	assert.Greater(t, len(data), 0)

	// Verify it's valid JSONL
	lines := bytes.SplitSeq(bytes.TrimSpace(data), []byte("\n"))
	for line := range lines {
		var req map[string]any
		require.NoError(t, json.Unmarshal(line, &req))
		assert.Equal(t, "POST", req["method"])
		assert.Equal(t, "/v1/chat/completions", req["url"])
		assert.NotEmpty(t, req["custom_id"])

		body, ok := req["body"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, BatchModel, body["model"])

		messages, ok := body["messages"].([]any)
		require.True(t, ok)
		assert.GreaterOrEqual(t, len(messages), 2)
	}
}

func TestBuildBatchJSONL_Categories(t *testing.T) {
	books := []SlimBook{
		{ID: "b1", Title: "Book One"},
	}

	for _, category := range []string{"deduplication", "error_analysis", "metadata_quality", "general"} {
		t.Run(category, func(t *testing.T) {
			data, err := BuildBatchJSONL(category, "test", books, nil, nil, nil)
			require.NoError(t, err)
			assert.Greater(t, len(data), 0)

			lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
			require.GreaterOrEqual(t, len(lines), 1)

			var req map[string]any
			require.NoError(t, json.Unmarshal(lines[0], &req))
			assert.Equal(t, "chunk-000", req["custom_id"])
		})
	}
}

func TestBuildBatchJSONL_Chunking(t *testing.T) {
	// Create 600 books to test chunking at 500
	books := make([]SlimBook, 600)
	for i := range 600 {
		books[i] = SlimBook{ID: "b" + strings.Repeat("x", 5), Title: "Book"}
	}

	data, err := BuildBatchJSONL("deduplication", "test", books, nil, nil, nil)
	require.NoError(t, err)

	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	assert.Equal(t, 2, len(lines), "600 books should produce 2 chunks at 500 per chunk")
}

func TestBuildBatchJSONL_EmptyBooks(t *testing.T) {
	data, err := BuildBatchJSONL("deduplication", "test", []SlimBook{}, nil, nil, nil)
	require.NoError(t, err)
	assert.Greater(t, len(data), 0, "should still produce at least one request line")
}

// TestService_GenerateExport_ReportsProgressEveryPhase is the regression test
// for a silent export.
//
// diagnostics.export runs under the registry watchdog, which cancels an op that
// reports nothing for ProgressTimeout. This function used to make the entire
// export — a full library walk plus up to ten file writes — in one silent call,
// so any export slower than the watchdog's default was killed while a zip
// nothing would serve went on being built.
//
// Asserting "more than one frame" is the point: a single Start-then-Done pair
// satisfies any total-based assertion while still being silent for the whole
// run, which is exactly what shipped.
func TestService_GenerateExport_ReportsProgressEveryPhase(t *testing.T) {
	store := setupDiagnosticsMocks(t)
	svc := NewService(store, nil, "")

	type frame struct {
		cur, total int
		msg        string
	}
	var frames []frame

	zipPath, err := svc.GenerateExport(context.Background(), "general", "progress test",
		func(cur, total int, msg string) { frames = append(frames, frame{cur, total, msg}) })
	require.NoError(t, err)
	defer os.Remove(zipPath)

	require.Greater(t, len(frames), 2,
		"a silent export is what the watchdog kills; one frame per phase is the fix")

	// "general" is the widest category: 10 writes plus the book collect.
	require.Equal(t, 11, frames[0].total, "total must be known before work starts")
	assert.Equal(t, 0, frames[0].cur)
	assert.Contains(t, frames[0].msg, "Collecting")

	last := frames[len(frames)-1]
	assert.Equal(t, last.total, last.cur, "the final frame must read 100%")

	for i := 1; i < len(frames); i++ {
		assert.GreaterOrEqual(t, frames[i].cur, frames[i-1].cur, "progress must not go backwards")
		assert.Equal(t, frames[0].total, frames[i].total, "total must not move mid-run")
	}
}

// TestService_GenerateExport_HonorsCanceledContext pins the other half: the
// watchdog's cancel has to actually stop the work. Before ctx was threaded
// through, a cancelled export kept running to completion, unobserved.
func TestService_GenerateExport_HonorsCanceledContext(t *testing.T) {
	store := setupDiagnosticsMocks(t)
	svc := NewService(store, nil, "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	zipPath, err := svc.GenerateExport(ctx, "general", "canceled", nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, zipPath, "a canceled export must not hand back a path")
}
