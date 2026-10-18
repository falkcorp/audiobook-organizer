// file: internal/server/transcode_integration_test.go
// version: 1.0.0
// guid: cc5127fa-27ba-490a-b347-bbac0a472878
// last-edited: 2026-10-18

//go:build integration

package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/testutil"
)

// ffprobeChapters returns the chapter count of an audio file and fails the test
// when ffprobe cannot read it or finds no audio stream.
func ffprobeChapters(t *testing.T, filePath string) int {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-print_format", "json",
		"-show_format", "-show_streams", "-show_chapters", filePath).CombinedOutput()
	require.NoError(t, err, "ffprobe failed for %s: %s", filePath, out)
	var res struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
		} `json:"streams"`
		Chapters []json.RawMessage `json:"chapters"`
	}
	require.NoError(t, json.Unmarshal(out, &res))
	hasAudio := false
	for _, s := range res.Streams {
		hasAudio = hasAudio || s.CodecType == "audio"
	}
	assert.True(t, hasAudio, "%s should contain an audio stream", filePath)
	return len(res.Chapters)
}

// TestLibraryTranscodeOp_RegisteredOpCreatesVersion runs the REAL registered
// library.transcode op (Server.RegisterLibraryTranscodeOp) through a started v2
// operations registry, exactly as POST /api/v1/operations/transcode enqueues
// it, against real ffmpeg and a real Pebble store. Inputs are generated tones.
// It replaces the old internal/transcode integration test that re-implemented
// the op inline against the deleted v1 queue.
func TestLibraryTranscodeOp_RegisteredOpCreatesVersion(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found", bin)
		}
	}

	env, cleanup := testutil.SetupIntegration(t)
	t.Cleanup(cleanup)

	bookDir := filepath.Join(t.TempDir(), "tone-book")
	const tracks = 3
	paths := make([]string, tracks)
	for i := range paths {
		paths[i] = filepath.Join(bookDir, "track"+string(rune('1'+i))+".mp3")
		testutil.SynthesizeMP3(t, paths[i], 2, 440+100*i)
	}

	// FilePath is the book's base path: Transcode names the output after it.
	created, err := env.Store.CreateBook(&database.Book{
		Title:    "Synthetic Tone Book",
		FilePath: filepath.Join(bookDir, "tone-book.mp3"),
		Format:   "mp3",
	})
	require.NoError(t, err)
	for i, p := range paths {
		fi, err := os.Stat(p)
		require.NoError(t, err)
		require.NoError(t, env.Store.CreateBookFile(&database.BookFile{
			ID:               "tone-file-" + string(rune('1'+i)),
			BookID:           created.ID,
			FilePath:         p,
			OriginalFilename: filepath.Base(p),
			Format:           "mp3",
			Codec:            "mp3",
			TrackNumber:      i + 1,
			TrackCount:       tracks,
			FileSize:         fi.Size(),
		}))
	}

	reg := opsregistry.New(env.Store, slog.New(slog.DiscardHandler), 1, nil)
	s := &Server{store: env.Store, opRegistry: reg}
	require.NoError(t, s.RegisterLibraryTranscodeOp(reg))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	reg.Start(ctx)
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()
		_ = reg.Shutdown(shutdownCtx)
	})

	opID, err := reg.EnqueueOp(ctx, "library.transcode", libraryTranscodeParams{
		BookID:       created.ID,
		OutputFormat: "m4b",
		Bitrate:      64,
		KeepOriginal: true,
	})
	require.NoError(t, err, "EnqueueOp should accept library.transcode")

	testutil.WaitForOp(t, env.Store, opID, 5*time.Minute)
	row, err := env.Store.GetOperationV2(opID)
	require.NoError(t, err)
	require.NotNil(t, row)
	errMsg := ""
	if row.ErrorMessage != nil {
		errMsg = *row.ErrorMessage
	}
	require.Equal(t, "completed", row.Status, "op should complete; error: %s", errMsg)

	// Original: now a member of a version group, noted as the original format.
	original, err := env.Store.GetBookByID(created.ID)
	require.NoError(t, err)
	require.NotNil(t, original.VersionGroupID, "original should be put in a version group")
	groupID := *original.VersionGroupID
	require.NotEmpty(t, groupID)
	require.NotNil(t, original.VersionNotes)
	assert.Equal(t, "Original format", *original.VersionNotes)

	// The M4B is a new record in the same group.
	members, err := env.Store.GetBooksByVersionGroup(groupID)
	require.NoError(t, err)
	require.Len(t, members, 2, "group should hold the original and the M4B")
	var m4b *database.Book
	for i := range members {
		if members[i].ID != created.ID {
			m4b = &members[i]
		}
	}
	require.NotNil(t, m4b, "M4B version record should exist")
	assert.Equal(t, "m4b", m4b.Format)
	require.NotNil(t, m4b.VersionNotes)
	assert.Equal(t, "Transcoded to M4B", *m4b.VersionNotes)
	require.NotNil(t, m4b.VersionGroupID)
	assert.Equal(t, groupID, *m4b.VersionGroupID)

	// Neither book is organized under the library root, so the version-primary
	// hand-off holds the group (needs_organize_or_restore) rather than
	// crowning the M4B: it must not be primary, and the group never ends up
	// with more than one primary.
	primaries := 0
	for _, m := range members {
		if m.IsPrimaryVersion != nil && *m.IsPrimaryVersion {
			primaries++
		}
	}
	assert.LessOrEqual(t, primaries, 1, "version group must never hold two primaries")
	assert.True(t, m4b.IsPrimaryVersion == nil || !*m4b.IsPrimaryVersion,
		"an unorganized M4B must not be crowned")

	// The output exists, is valid, has one chapter per input, and the
	// original tracks were kept.
	require.FileExists(t, m4b.FilePath)
	assert.Equal(t, tracks, ffprobeChapters(t, m4b.FilePath))
	for _, p := range paths {
		assert.FileExists(t, p, "KeepOriginal=true must leave the source tracks")
	}
	files, err := env.Store.GetBookFiles(m4b.ID)
	require.NoError(t, err)
	require.Len(t, files, 1, "M4B version should carry its own book_file row")
	assert.Equal(t, m4b.FilePath, files[0].FilePath)
}
