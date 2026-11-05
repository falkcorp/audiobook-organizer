// file: internal/scanner/replaced_file_probe_outcome_test.go
// version: 1.0.0
// guid: 14d81b12-fa12-4c33-a945-a94795dd61f0
// last-edited: 2026-11-04

package scanner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/testutil/durtest"
)

// stubFFprobeOnPath puts a stub "ffprobe" script on PATH (and only there) and
// disables mediainfo. An empty script leaves no ffprobe at all. Nothing decodes.
func stubFFprobeOnPath(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if script != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "ffprobe"), []byte("#!/bin/sh\n"+script+"\n"), 0o700))
		t.Setenv("PATH", dir+":/bin:/usr/bin")
	} else {
		t.Setenv("PATH", dir)
	}
	t.Setenv("ABK_DISABLE_MEDIAINFO", "1")
}

func junkMP3(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "Year One.mp3")
	require.NoError(t, os.WriteFile(p, make([]byte, 480_000), 0o600))
	return p
}

// The replaced-file probe writes its outcome to bf.Scan.DurationOK like every
// other probe writer.
func TestProbeReplacedFileAudio_RecordsOutcome(t *testing.T) {
	log := logger.New("test")

	t.Run("measured: Known and Done", func(t *testing.T) {
		stubFFprobeOnPath(t, "echo 5400.2")
		bf := &database.BookFile{FilePath: junkMP3(t)}
		probeReplacedFileAudio(bf, bf.FilePath, log)
		require.Equal(t, 5400, durtest.Sec(bf.Duration))
		require.Equal(t, database.StageStatus{Done: true}, bf.Scan.DurationOK)
	})
	t.Run("ffprobe fails on a present file: an attempt, never the estimate", func(t *testing.T) {
		stubFFprobeOnPath(t, "exit 1")
		bf := &database.BookFile{FilePath: junkMP3(t)}
		probeReplacedFileAudio(bf, bf.FilePath, log)
		require.Nil(t, bf.Duration)
		require.Equal(t, 1, bf.Scan.DurationOK.Attempts)
		require.NotEmpty(t, bf.Scan.DurationOK.LastError)
		require.Equal(t, database.DurationProbeFailed, bf.DurationState())
	})
	t.Run("ffprobe reports 0s: an attempt", func(t *testing.T) {
		stubFFprobeOnPath(t, "echo 0")
		bf := &database.BookFile{FilePath: junkMP3(t)}
		probeReplacedFileAudio(bf, bf.FilePath, log)
		require.Equal(t, 1, bf.Scan.DurationOK.Attempts)
		require.Equal(t, "probe returned 0s", bf.Scan.DurationOK.LastError)
	})
	t.Run("ffprobe not installed: no attempt", func(t *testing.T) {
		stubFFprobeOnPath(t, "")
		bf := &database.BookFile{FilePath: junkMP3(t)}
		probeReplacedFileAudio(bf, bf.FilePath, log)
		require.Nil(t, bf.Duration)
		require.Equal(t, database.StageStatus{}, bf.Scan.DurationOK)
	})
	t.Run("file absent: no attempt", func(t *testing.T) {
		stubFFprobeOnPath(t, "exit 1")
		bf := &database.BookFile{FilePath: filepath.Join(t.TempDir(), "gone.mp3")}
		probeReplacedFileAudio(bf, bf.FilePath, log)
		require.Equal(t, database.StageStatus{}, bf.Scan.DurationOK)
	})
}

func TestSeedDurationStage(t *testing.T) {
	stored := &database.BookFile{FileHash: "h1", Scan: database.ScanState{
		NeedsDeep:  true,
		DurationOK: database.StageStatus{Attempts: 2, LastError: "exit status 1"},
	}}
	cases := []struct {
		name     string
		bfHash   string
		wantSeed bool
	}{
		{"same bytes carry the stored count", "h1", true},
		{"replaced file starts over", "h2", false},
		{"unknown incoming hash is not a change", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bf := &database.BookFile{FileHash: tc.bfHash}
			seedDurationStage(bf, stored)
			if tc.wantSeed {
				require.Equal(t, stored.Scan.DurationOK, bf.Scan.DurationOK)
				bf.RecordDurationProbeFailure("exit status 1")
				require.Equal(t, 3, bf.Scan.DurationOK.Attempts)
				require.Equal(t, database.DurationUnreadable, bf.DurationState())
			} else {
				require.Equal(t, database.StageStatus{}, bf.Scan.DurationOK)
			}
			require.False(t, bf.Scan.NeedsDeep, "only the duration stage is carried; the merge restores the rest")
		})
	}
	seedDurationStage(nil, stored)
	seedDurationStage(&database.BookFile{}, nil)
}
