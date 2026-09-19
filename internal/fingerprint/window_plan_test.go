// file: internal/fingerprint/window_plan_test.go
// version: 1.0.0
// guid: c734fdb9-96b4-44eb-b09f-d5b450aaf333
// last-edited: 2026-09-19

package fingerprint

import (
	"errors"
	"math"
	"testing"
)

func TestPlanWindows_Table(t *testing.T) {
	src := DurationSourceBookFile
	type want struct {
		slot   int
		off    float64
		length float64
		whole  bool
	}
	cases := []struct {
		name string
		dur  float64
		want []want
	}{
		{"tiny clip covers whole", 12.5, []want{{0, 0, 12.5, true}}},
		{"exactly 150s covers whole", 150, []want{{0, 0, 150, true}}},
		{"sub-ms duration floors length", 100.0006, []want{{0, 0, 100, true}}},
		{"just over 150s clamps 50% to dur-len", 151, []want{{5000, 31, 120, false}}},
		{"200s chapter clamps", 200, []want{{5000, 80, 120, false}}},
		{"240s chapter exact half", 240, []want{{5000, 120, 120, false}}},
		{"599.9s still one slot", 599.9, []want{{5000, 299.95, 120, false}}},
		{"exactly 600s three slots, 90% clamps", 600, []want{
			{1000, 60, 120, false}, {5000, 300, 120, false}, {9000, 480, 120, false}}},
		{"10h book", 36000, []want{
			{1000, 3600, 120, false}, {5000, 18000, 120, false}, {9000, 32400, 120, false}}},
		{"fractional duration rounds to ms", 3601.23456, []want{
			{1000, 360.123, 120, false}, {5000, 1800.617, 120, false}, {9000, 3241.111, 120, false}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PlanWindows(DurationUsed{Sec: tc.dur, Source: src}, WindowSetWS1)
			if err != nil {
				t.Fatalf("PlanWindows(%v): %v", tc.dur, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d windows, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				g := got[i]
				if g.SlotBP != w.slot || math.Abs(g.OffsetSec-w.off) > 1e-9 ||
					math.Abs(g.LengthSec-w.length) > 1e-9 || g.CoversWhole != w.whole {
					t.Errorf("window %d = %+v, want slot=%d off=%v len=%v whole=%v", i, g, w.slot, w.off, w.length, w.whole)
				}
				if g.Kind != WindowKindWindow || g.WindowSet != WindowSetWS1 ||
					g.Duration.Sec != tc.dur || g.Duration.Source != src {
					t.Errorf("window %d provenance = %+v", i, g)
				}
				if g.OffsetSec < 0 || g.OffsetSec+g.LengthSec > tc.dur+1e-9 {
					t.Errorf("window %d [%v,+%v] leaves [0,%v]", i, g.OffsetSec, g.LengthSec, tc.dur)
				}
			}
		})
	}
}

func TestPlanWindows_Refuses(t *testing.T) {
	for _, d := range []DurationUsed{
		{Sec: 0, Source: DurationSourceBookFile},
		{Sec: -5, Source: DurationSourceBookFile},
		{Sec: math.NaN(), Source: DurationSourceBookFile},
		{Sec: math.Inf(1), Source: DurationSourceBookFile},
		{Sec: 3600, Source: ""},
	} {
		if _, err := PlanWindows(d, WindowSetWS1); !errors.Is(err, ErrUnknownDuration) {
			t.Errorf("PlanWindows(%+v) err = %v, want ErrUnknownDuration", d, err)
		}
	}
	if _, err := PlanWindows(DurationUsed{Sec: 3600, Source: DurationSourceBookFile}, "ws2"); !errors.Is(err, ErrUnknownWindowSet) {
		t.Errorf("unknown window set err = %v", err)
	}
}

func TestChooseDuration_Priority(t *testing.T) {
	probeCalls := 0
	probe := func(v float64, err error) func() (float64, error) {
		return func() (float64, error) { probeCalls++; return v, err }
	}
	cases := []struct {
		name     string
		fp, bf   float64
		probe    func() (float64, error)
		wantSec  float64
		wantSrc  DurationSource
		wantErr  bool
		wantCall int
	}{
		{"fingerprint wins", 3600, 3500, probe(3400, nil), 3600, DurationSourceFingerprint, false, 0},
		{"book file second", 0, 3500, probe(3400, nil), 3500, DurationSourceBookFile, false, 0},
		{"ffprobe last", 0, 0, probe(3400, nil), 3400, DurationSourceFFprobe, false, 1},
		{"NaN fingerprint skipped", math.NaN(), 3500, nil, 3500, DurationSourceBookFile, false, 0},
		{"probe error refuses", 0, 0, probe(0, errors.New("no ffprobe")), 0, "", true, 1},
		{"probe zero refuses", 0, 0, probe(0, nil), 0, "", true, 1},
		{"nothing refuses", 0, -1, nil, 0, "", true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probeCalls = 0
			got, err := ChooseDuration(tc.fp, tc.bf, tc.probe)
			if tc.wantErr {
				if !errors.Is(err, ErrUnknownDuration) {
					t.Fatalf("err = %v, want ErrUnknownDuration", err)
				}
			} else if err != nil || got.Sec != tc.wantSec || got.Source != tc.wantSrc {
				t.Fatalf("got %+v, %v; want %v from %s", got, err, tc.wantSec, tc.wantSrc)
			}
			if probeCalls != tc.wantCall {
				t.Errorf("probe called %d times, want %d", probeCalls, tc.wantCall)
			}
		})
	}
}

func TestParseToolVersion(t *testing.T) {
	cases := map[string]struct{ out, name, want string }{
		"fpcalc":          {"fpcalc version 1.6.1 (FFmpeg Lavc63.1.101 Lavf63.1.101 SwR7.1.101)\n", "fpcalc", "1.6.1"},
		"ffmpeg":          {"ffmpeg version 8.0.1 Copyright (c) 2000-2025 the FFmpeg developers\nbuilt with gcc\n", "ffmpeg", "8.0.1"},
		"ffmpeg distro":   {"ffmpeg version 6.1.1-3ubuntu5 Copyright\n", "ffmpeg", "6.1.1-3ubuntu5"},
		"banner first":    {"warning: something\nfpcalc version 1.5.1\n", "fpcalc", "1.5.1"},
		"no version":      {"usage: fpcalc [OPTIONS]\n", "fpcalc", ""},
		"wrong tool name": {"ffmpeg version 8.0.1\n", "fpcalc", ""},
	}
	for name, tc := range cases {
		if got := parseToolVersion(tc.out, tc.name); got != tc.want {
			t.Errorf("%s: parseToolVersion = %q, want %q", name, got, tc.want)
		}
	}
}
