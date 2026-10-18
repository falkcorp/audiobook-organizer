// file: internal/server/transcode_integration_test.go
// version: 1.1.0
// guid: cc5127fa-27ba-490a-b347-bbac0a472878
// last-edited: 2026-10-18

//go:build integration

package server

import (
	"context"
	"fmt"
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

// TestLibraryTranscodeOp_RegisteredOpCreatesVersion runs the REAL registered
// library.transcode op (Server.RegisterLibraryTranscodeOp) through a started v2
// operations registry, against real ffmpeg and a real Pebble store. Inputs are
// generated tones. It enqueues with EnqueueOp, the call the production path
// (POST /api/v1/operations/v2 {def_id:"library.transcode"} -> TriggerOperationV2
// -> EnqueueOp) ends in; it does not go through the HTTP handler, the
// defPermissionsHeld check, or the production registry hooks
// (registry_wire.go). It replaces the old internal/transcode integration test
// that re-implemented the op inline against the deleted v1 queue.
//
// The two cases pin the version-primary hand-off, which depends on the op
// passing config.AppConfig.RootDir into recordTranscodedVersion: an organized
// original under the library root hands the primary to the M4B; an unorganized
// one leaves the group held with no primary.
func TestLibraryTranscodeOp_RegisteredOpCreatesVersion(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found", bin)
		}
	}

	for _, tc := range []struct {
		name      string
		organized bool
	}{
		{"organized original under library root", true},
		{"unorganized original outside library root", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, cleanup := testutil.SetupIntegration(t)
			t.Cleanup(cleanup)

			// A multi-file book's FilePath is its directory, as the scanner sets it.
			bookDir := filepath.Join(t.TempDir(), "tone-book")
			var state *string
			if tc.organized {
				bookDir = filepath.Join(env.RootDir, "Synthetic Author", "tone-book")
				organized := "organized"
				state = &organized
			}
			paths := testutil.SynthesizeToneBook(t, bookDir)

			created, err := env.Store.CreateBook(&database.Book{
				Title:        "Synthetic Tone Book",
				FilePath:     bookDir,
				Format:       "mp3",
				LibraryState: state,
			})
			require.NoError(t, err)
			for i, p := range paths {
				fi, err := os.Stat(p)
				require.NoError(t, err)
				require.NoError(t, env.Store.CreateBookFile(&database.BookFile{
					ID:               fmt.Sprintf("tone-file-%d", i+1),
					BookID:           created.ID,
					FilePath:         p,
					OriginalFilename: filepath.Base(p),
					Format:           "mp3",
					Codec:            "mp3",
					TrackNumber:      i + 1,
					TrackCount:       len(paths),
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
				if err := reg.Shutdown(shutdownCtx); err != nil {
					t.Logf("registry shutdown: %v", err)
				}
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

			isPrimary := func(b *database.Book) bool { return b.IsPrimaryVersion != nil && *b.IsPrimaryVersion }
			primaries := 0
			for i := range members {
				if isPrimary(&members[i]) {
					primaries++
				}
			}
			if tc.organized {
				// The output lies under RootDir, keeps the organized state, and is
				// crowned: the original is demoted.
				require.NotNil(t, m4b.LibraryState)
				assert.Equal(t, "organized", *m4b.LibraryState)
				assert.True(t, isPrimary(m4b), "organized M4B should be the primary")
				require.NotNil(t, original.IsPrimaryVersion, "original should carry an explicit flag")
				assert.False(t, *original.IsPrimaryVersion, "original should be demoted")
				assert.Equal(t, 1, primaries)
			} else {
				// Nothing is organized under the library root, so the hand-off holds
				// the group (needs_organize_or_restore) and crowns no one.
				assert.Equal(t, 0, primaries, "an unorganized group is held with no primary")
			}

			// The output exists, is valid, has one chapter per input, and the
			// original tracks were kept.
			require.FileExists(t, m4b.FilePath)
			assert.Equal(t, len(paths), testutil.FFprobeValidate(t, m4b.FilePath))
			for _, p := range paths {
				assert.FileExists(t, p, "KeepOriginal=true must leave the source tracks")
			}
			files, err := env.Store.GetBookFiles(m4b.ID)
			require.NoError(t, err)
			require.Len(t, files, 1, "M4B version should carry its own book_file row")
			assert.Equal(t, m4b.FilePath, files[0].FilePath)
		})
	}
}
