// file: internal/metadata/eyed3_track_test.go
// version: 1.0.0
// guid: c4a81f3e-6b27-4d95-9e0a-2f7d5b1c8e43
// last-edited: 2026-10-11

package metadata

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/fileops"
)

// eyeD3 -n takes only the track number; "3/12" is rejected with "invalid
// PositiveIntArg value", so the number and the total travel as -n and -N.
func TestMP3TagPlan_SplitsTrackAndTotal(t *testing.T) {
	cases := []struct {
		track    string
		wantArgs []string
		wantTrk  string
	}{
		{"3", []string{"-n", "3"}, "3"},
		{"3/12", []string{"-n", "3", "-N", "12"}, "3/12"},
		{" 03 / 012 ", []string{"-n", "3", "-N", "12"}, "3/12"},
	}
	for _, c := range cases {
		t.Run(c.track, func(t *testing.T) {
			p := mp3TagPlan(map[string]any{"track": c.track})
			require.Equal(t, c.wantArgs, p.args[:len(c.wantArgs)])
			require.NotContains(t, p.args, "3/12", "a slashed value must never reach eyeD3")
			require.Equal(t, []string{c.wantTrk}, p.want["TRACKNUMBER"])
		})
	}
}

// A value eyeD3 would refuse is left out rather than failing every other tag.
func TestMP3TagPlan_DropsUnwritableTrack(t *testing.T) {
	for _, track := range []string{"", "abc", "0", "0/12", "3/0", "3/x", "-1", "3/12/4"} {
		t.Run(track, func(t *testing.T) {
			p := mp3TagPlan(map[string]any{"title": "T", "track": track})
			require.NotContains(t, p.args, "-n")
			require.NotContains(t, p.args, "-N")
			require.NotContains(t, p.want, "TRACKNUMBER")
			require.Equal(t, []string{"T"}, p.want["TITLE"])
		})
	}
}

// The real eyeD3: a "N/M" track must write, and read back as the plan says.
func TestWriteMP3Metadata_RealEyeD3TrackAndTotal(t *testing.T) {
	requireTools(t, "eyeD3", "ffmpeg")
	p := makeAudio(t, filepath.Base("a.mp3"))
	md := map[string]any{"title": "Track Title", "track": "3/12"}
	require.NoError(t, writeMP3Metadata(p, md, fileops.OperationConfig{}))
	out, err := exec.Command("eyeD3", p).CombinedOutput()
	require.NoError(t, err, string(out))
	require.Regexp(t, `track:\s*3/12`, string(out))
	got, err := readTagsWithTaglib(p)
	require.NoError(t, err)
	require.Equal(t, []string{"Track Title"}, got["TITLE"])
	require.Contains(t, got["TRACKNUMBER"][0], "3")
	require.Contains(t, got["TRACKNUMBER"][0], "/12")
}
