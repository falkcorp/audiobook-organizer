// file: internal/plugins/maintenance/repoint_probe_outcome_test.go
// version: 1.0.0
// guid: d3a16d65-e084-4a0a-8b8d-3eba7a91104c
// last-edited: 2026-11-04

package maintenance

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/bookfileaudio"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
	"github.com/falkcorp/audiobook-organizer/internal/testutil/durtest"
)

// The plan-time probe is replayed at apply time without re-reading the file, and
// what it decided survives: a real duration is written, a failed read of the
// present file is one attempt, and a read that could not run (no ffprobe,
// absent file) is nothing. The last case is the one a bare
// MediaInfo{Duration, Codec} used to get wrong, recording a counted "0s".
func TestRfApplyProbe(t *testing.T) {
	cases := []struct {
		name         string
		probe        rfProbe
		wantDur      int
		wantAttempts int
		wantCodec    string
	}{
		{"measured", rfProbe{Info: mediainfo.MediaInfo{Duration: 5400, Codec: "AAC"}, Source: bookfileaudio.SourceCaller}, 5400, 0, "AAC"},
		{"failed on a present file", rfProbe{Info: mediainfo.MediaInfo{Codec: "AAC"}, Source: bookfileaudio.SourceProbeFailed, Failure: "exit status 1"}, 0, 1, "AAC"},
		{"could not run", rfProbe{Info: mediainfo.MediaInfo{Codec: "AAC"}, Source: bookfileaudio.SourceProbeUnavailable}, 0, 0, "AAC"},
		{"nothing tried", rfProbe{}, 0, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &database.BookFile{ID: "f1", FilePath: "/lib/Year One/01.m4b"}
			rfApplyProbe(f, tc.probe)
			require.Equal(t, tc.wantDur, durtest.Sec(f.Duration))
			require.Equal(t, tc.wantAttempts, f.Scan.DurationOK.Attempts)
			require.Equal(t, tc.wantCodec, f.Codec)
			if tc.wantAttempts > 0 {
				require.Equal(t, "exit status 1", f.Scan.DurationOK.LastError)
			}
		})
	}
}
