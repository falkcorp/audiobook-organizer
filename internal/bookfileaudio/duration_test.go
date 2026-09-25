// file: internal/bookfileaudio/duration_test.go
// version: 1.0.0
// guid: 3c0f4a8e-6b1d-4e52-9a77-2d5e8f1b9c04
// last-edited: 2026-09-25

package bookfileaudio

import (
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
)

// fakeProbe records the paths it was asked for and returns a fixed result.
type fakeProbe struct {
	info  *mediainfo.MediaInfo
	err   error
	paths []string
}

func (f *fakeProbe) fn(path string) (*mediainfo.MediaInfo, error) {
	f.paths = append(f.paths, path)
	return f.info, f.err
}

func TestEnsureDuration_Precedence(t *testing.T) {
	cases := []struct {
		name       string
		row        database.BookFile
		known      Known
		probe      fakeProbe
		wantDur    int
		wantCodec  string
		wantSource Source
		wantProbed bool
	}{
		{
			name:       "row already has a duration: untouched, no probe",
			row:        database.BookFile{FilePath: "/a.m4b", Duration: 42},
			known:      Known{SingleFileBook: true, BookDurationSec: 999},
			probe:      fakeProbe{info: &mediainfo.MediaInfo{Duration: 7}},
			wantDur:    42,
			wantSource: SourceAlreadySet,
		},
		{
			name:       "caller value beats book duration and probe",
			row:        database.BookFile{FilePath: "/a.m4b"},
			known:      Known{Info: &mediainfo.MediaInfo{Duration: 100, Codec: "AAC"}, SingleFileBook: true, BookDurationSec: 999},
			probe:      fakeProbe{info: &mediainfo.MediaInfo{Duration: 7}},
			wantDur:    100,
			wantCodec:  "AAC",
			wantSource: SourceCaller,
		},
		{
			name:       "caller value that is an estimate stays 0: no book fall back, no probe",
			row:        database.BookFile{FilePath: "/a.m4b"},
			known:      Known{Info: &mediainfo.MediaInfo{Duration: 100, DurationEstimated: true, Codec: "AAC"}, SingleFileBook: true, BookDurationSec: 100},
			probe:      fakeProbe{info: &mediainfo.MediaInfo{Duration: 7}},
			wantDur:    0,
			wantCodec:  "AAC",
			wantSource: SourceNone,
		},
		{
			name:       "single-file book duration beats probe",
			row:        database.BookFile{FilePath: "/a.m4b"},
			known:      Known{SingleFileBook: true, BookDurationSec: 3600},
			probe:      fakeProbe{info: &mediainfo.MediaInfo{Duration: 7}},
			wantDur:    3600,
			wantSource: SourceBook,
		},
		{
			name:       "multi-file book duration is ignored: probe",
			row:        database.BookFile{FilePath: "/seg1.mp3"},
			known:      Known{SingleFileBook: false, BookDurationSec: 3600},
			probe:      fakeProbe{info: &mediainfo.MediaInfo{Duration: 600, Codec: "MP3"}},
			wantDur:    600,
			wantCodec:  "MP3",
			wantSource: SourceProbe,
			wantProbed: true,
		},
		{
			name:       "probe estimate stays 0 but codec is kept",
			row:        database.BookFile{FilePath: "/a.m4b"},
			probe:      fakeProbe{info: &mediainfo.MediaInfo{Duration: 500, DurationEstimated: true, Codec: "AAC"}},
			wantDur:    0,
			wantCodec:  "AAC",
			wantSource: SourceNone,
			wantProbed: true,
		},
		{
			name:       "probe error leaves 0",
			row:        database.BookFile{FilePath: "/a.m4b"},
			probe:      fakeProbe{err: errors.New("boom")},
			wantDur:    0,
			wantSource: SourceNone,
			wantProbed: true,
		},
		{
			name:       "existing codec is not overwritten",
			row:        database.BookFile{FilePath: "/a.m4b", Codec: "aac"},
			probe:      fakeProbe{info: &mediainfo.MediaInfo{Duration: 50, Codec: "AAC"}},
			wantDur:    50,
			wantCodec:  "aac",
			wantSource: SourceProbe,
			wantProbed: true,
		},
		{
			name:       "no path: nothing to probe",
			row:        database.BookFile{},
			probe:      fakeProbe{info: &mediainfo.MediaInfo{Duration: 50}},
			wantDur:    0,
			wantSource: SourceNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp := tc.probe
			t.Cleanup(SetProbeForTesting(fp.fn))
			row := tc.row
			got := EnsureDuration(&row, tc.known, nil)
			if got != tc.wantSource {
				t.Errorf("source = %v, want %v", got, tc.wantSource)
			}
			if row.Duration != tc.wantDur {
				t.Errorf("Duration = %d, want %d", row.Duration, tc.wantDur)
			}
			if row.Codec != tc.wantCodec {
				t.Errorf("Codec = %q, want %q", row.Codec, tc.wantCodec)
			}
			if probed := len(fp.paths) > 0; probed != tc.wantProbed {
				t.Errorf("probed = %v (%v), want %v", probed, fp.paths, tc.wantProbed)
			}
			if tc.wantProbed && len(fp.paths) > 0 && fp.paths[0] != tc.row.FilePath {
				t.Errorf("probed path %q, want %q", fp.paths[0], tc.row.FilePath)
			}
		})
	}
}

func TestEnsureDuration_ProbeTimeoutLeavesZero(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	t.Cleanup(SetProbeForTesting(func(string) (*mediainfo.MediaInfo, error) {
		<-block
		return &mediainfo.MediaInfo{Duration: 10}, nil
	}))
	prev := probeTimeout
	probeTimeout = 20 * time.Millisecond
	t.Cleanup(func() { probeTimeout = prev })

	row := database.BookFile{FilePath: "/hang.m4b"}
	if got := EnsureDuration(&row, Known{}, nil); got != SourceNone || row.Duration != 0 {
		t.Fatalf("got source %v duration %d, want none/0", got, row.Duration)
	}
}

func TestEnsureDuration_NilRow(t *testing.T) {
	if got := EnsureDuration(nil, Known{}, nil); got != SourceNone {
		t.Fatalf("nil row: source %v", got)
	}
}
