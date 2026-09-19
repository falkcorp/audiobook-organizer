// file: internal/fingerprint/window_integration_test.go
// version: 1.0.0
// guid: e5d2f0a1-52cf-4c86-8571-9ffd04be4087
// last-edited: 2026-09-19

package fingerprint_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/tools"
)

// realWindowTools resolves the real binaries through a ToolRegistry, the
// way the server will, or skips when either is missing.
func realWindowTools(t *testing.T) fingerprint.WindowTools {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "fpcalc"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
	reg := tools.NewToolRegistry(&tools.ToolsConfig{Fpcalc: tools.ToolConfig{Mode: tools.ToolModeSystem}})
	reg.Register(tools.ToolDef{Name: "fpcalc"})
	reg.Register(tools.ToolDef{Name: "ffmpeg"})
	wt, err := fingerprint.ResolveWindowTools(context.Background(), reg)
	if err != nil {
		t.Fatalf("ResolveWindowTools: %v", err)
	}
	if wt.Versions.Fpcalc == "" || wt.Versions.FFmpeg == "" {
		t.Fatalf("versions not recorded: %+v", wt.Versions)
	}
	t.Logf("fpcalc %s, ffmpeg %s", wt.Versions.Fpcalc, wt.Versions.FFmpeg)
	return wt
}

// makeToneFile renders a deterministic, time-varying signal (two slow FM
// sweeps plus seeded noise) of durSec seconds, preceded by delaySec of
// silence, as an m4a (a real container with a moov atom to seek through).
func makeToneFile(t *testing.T, path string, durSec int, seed int, delaySec float64) {
	t.Helper()
	// seed picks both the sweep parameters and the noise, so two seeds are
	// different "recordings" (a noise-only difference would leave the tonal
	// content, which dominates the print, identical).
	f1, f2 := 300+97*seed, 800+151*seed
	p1, p2 := 7+2*seed, 13+3*seed
	src := fmt.Sprintf("aevalsrc='0.5*sin(2*PI*(%d+300*sin(2*PI*t/%d))*t)+0.3*sin(2*PI*(%d+500*sin(2*PI*t/%d))*t)':s=22050:d=%d",
		f1, p1, f2, p2, durSec)
	noise := "anoisesrc=d=" + strconv.Itoa(durSec) + ":c=pink:r=22050:a=0.3:seed=" + strconv.Itoa(seed)
	filter := "[0:a][1:a]amix=inputs=2:normalize=0"
	if delaySec > 0 {
		filter += ",adelay=" + strconv.FormatFloat(delaySec*1000, 'f', 0, 64) + ":all=1"
	}
	out, err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-y",
		"-f", "lavfi", "-i", src, "-f", "lavfi", "-i", noise,
		"-filter_complex", filter, "-ac", "1", "-c:a", "aac", "-b:a", "64k", path).CombinedOutput()
	if err != nil {
		t.Fatalf("render %s: %v\n%s", path, err, out)
	}
}

func windowsFor(t *testing.T, wt fingerprint.WindowTools, path string, dur float64) []fingerprint.WindowPrint {
	t.Helper()
	plan, err := fingerprint.PlanWindows(fingerprint.DurationUsed{Sec: dur, Source: fingerprint.DurationSourceFFprobe}, fingerprint.WindowSetWS1)
	if err != nil {
		t.Fatal(err)
	}
	var out []fingerprint.WindowPrint
	for _, spec := range plan {
		w, err := wt.FileWindow(context.Background(), path, spec)
		if err != nil {
			t.Fatalf("FileWindow %s slot %d: %v", filepath.Base(path), spec.SlotBP, err)
		}
		out = append(out, *w)
	}
	return out
}

func TestFileWindow_Integration_ToneFile(t *testing.T) {
	wt := realWindowTools(t)
	dir := t.TempDir()
	const dur = 660 // 11 min: three windows
	a := filepath.Join(dir, "a.m4a")
	shifted := filepath.Join(dir, "a_shifted:3s.m4a") // ':' exercises -i file:
	other := filepath.Join(dir, "other.m4a")
	makeToneFile(t, a, dur, 1, 0)
	makeToneFile(t, shifted, dur, 1, 3)
	makeToneFile(t, other, dur, 2, 0)

	winA := windowsFor(t, wt, a, dur)
	if len(winA) != 3 {
		t.Fatalf("got %d windows, want 3", len(winA))
	}
	for _, w := range winA {
		if w.Frames < 900 || w.Frames > 1000 {
			t.Errorf("slot %d: %d frames for a 120 s window", w.SlotBP, w.Frames)
		}
		if w.DecodedSec < 119.9 || w.DecodedSec > 120.1 {
			t.Errorf("slot %d: decoded %.3f s", w.SlotBP, w.DecodedSec)
		}
		if w.Pipeline != fingerprint.WindowPipelineID || w.FpcalcVersion != wt.Versions.Fpcalc {
			t.Errorf("slot %d provenance %+v", w.SlotBP, w)
		}
	}

	// Parity: the pipeline's frames equal fpcalc -raw reading a file that
	// was cut to the same window, so the pipe adds nothing of its own.
	mid := winA[1]
	cut := filepath.Join(dir, "cut.wav")
	if out, err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-y", "-ss",
		strconv.FormatFloat(mid.OffsetSec, 'f', 3, 64), "-i", a, "-t", "120",
		"-ac", "1", "-ar", "11025", cut).CombinedOutput(); err != nil {
		t.Fatalf("cut: %v\n%s", err, out)
	}
	out, err := exec.Command("fpcalc", "-raw", "-json", "-length", "120", cut).Output()
	if err != nil {
		t.Fatalf("fpcalc direct: %v", err)
	}
	var direct struct {
		Fingerprint []uint32 `json:"fingerprint"`
	}
	if err := json.Unmarshal(out, &direct); err != nil {
		t.Fatal(err)
	}
	if len(direct.Fingerprint) != mid.Frames {
		t.Errorf("parity: direct %d frames, pipeline %d", len(direct.Fingerprint), mid.Frames)
	} else if !bytes.Equal(mid.Raw, packLE(direct.Fingerprint)) {
		t.Error("parity: pipeline frames differ from fpcalc -raw on the pre-cut file")
	}

	winShifted := windowsFor(t, wt, shifted, dur+3)
	winOther := windowsFor(t, wt, other, dur)

	same, err := fingerprint.WindowSetSimilarity(winA, winShifted)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := fingerprint.WindowSetSimilarity(winA, winOther)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("shifted copy: %.3f %+v", same.Score, same.Slots)
	t.Logf("different content: %.3f %+v", diff.Score, diff.Slots)
	if same.Score < 0.85 {
		t.Errorf("3 s-shifted copy scored %.3f, want >= 0.85", same.Score)
	}
	if diff.Score > same.Score-0.15 {
		t.Errorf("different content %.3f is not clearly below the shifted copy %.3f", diff.Score, same.Score)
	}
}

func TestFileWindow_Integration_ShortFileCoversWhole(t *testing.T) {
	wt := realWindowTools(t)
	p := filepath.Join(t.TempDir(), "clip.m4a")
	makeToneFile(t, p, 40, 5, 0)
	wins := windowsFor(t, wt, p, 40)
	if len(wins) != 1 || !wins[0].CoversWhole || wins[0].OffsetSec != 0 {
		t.Fatalf("windows %+v, want one covers-whole window", wins)
	}
}

func TestFileWindow_Integration_MissingFile(t *testing.T) {
	wt := realWindowTools(t)
	spec := fingerprint.WindowSpec{Kind: fingerprint.WindowKindWindow, SlotBP: 5000, OffsetSec: 10, LengthSec: 120,
		WindowSet: fingerprint.WindowSetWS1, Duration: fingerprint.DurationUsed{Sec: 300, Source: fingerprint.DurationSourceFFprobe}}
	_, err := wt.FileWindow(context.Background(), filepath.Join(t.TempDir(), "nope.m4b"), spec)
	if !errors.Is(err, fingerprint.ErrWindowFFmpeg) {
		t.Fatalf("err = %v, want ErrWindowFFmpeg", err)
	}
	t.Logf("missing file error: %v", err)
}

func packLE(frames []uint32) []byte {
	b := make([]byte, len(frames)*4)
	for i, f := range frames {
		binary.LittleEndian.PutUint32(b[i*4:], f)
	}
	return b
}
