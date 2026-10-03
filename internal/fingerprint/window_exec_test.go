// file: internal/fingerprint/window_exec_test.go
// version: 1.1.0
// guid: 6cf5cc21-cb7b-42bc-9d2f-c6838d6a621d
// last-edited: 2026-10-03

package fingerprint

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// writeScript writes an executable /bin/sh script into dir.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// pcmSeconds is the byte count of n seconds of the window pipeline's PCM.
func pcmSeconds(n int) int { return n * 2 * WindowSampleRate }

// fakeFFmpegOK emits sec seconds of zero PCM, like a successful cut.
func fakeFFmpegOK(sec int) string {
	return fmt.Sprintf("head -c %d /dev/zero", pcmSeconds(sec))
}

// fakeFpcalcRaw drains stdin then prints a -raw -json result of n frames.
func fakeFpcalcRaw(n int) string {
	vals := make([]string, n)
	for i := range vals {
		vals[i] = fmt.Sprint(4000000000 - i) // above MaxInt32: must parse as unsigned
	}
	return fmt.Sprintf(`cat >/dev/null
printf '{"duration": 0.00, "fingerprint": [%s]}\n'`, strings.Join(vals, ","))
}

func skipOnWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake binaries are /bin/sh scripts")
	}
}

var testSpec = WindowSpec{
	Kind: WindowKindWindow, SlotBP: 5000, OffsetSec: 1800, LengthSec: 120,
	WindowSet: WindowSetWS1, Duration: DurationUsed{Sec: 3600, Source: DurationSourceBookFile},
}

func TestFileWindow_FakeSuccess(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	tools := WindowTools{
		FFmpegPath: writeScript(t, dir, "ffmpeg", fakeFFmpegOK(120)),
		FpcalcPath: writeScript(t, dir, "fpcalc", fakeFpcalcRaw(900)),
		Versions:   ToolVersionInfo{Fpcalc: "1.6.1", FFmpeg: "8.0.1"},
	}
	w, err := tools.FileWindow(context.Background(), "/lib/a.m4b", testSpec)
	if err != nil {
		t.Fatalf("FileWindow: %v", err)
	}
	if w.Frames != 900 || len(w.Raw) != 3600 {
		t.Fatalf("frames=%d raw=%d", w.Frames, len(w.Raw))
	}
	if got := binary.LittleEndian.Uint32(w.Raw); got != 4000000000 {
		t.Errorf("first frame = %d, want 4000000000", got)
	}
	if w.DecodedSec != 120 {
		t.Errorf("DecodedSec = %v, want 120 (counted from PCM bytes)", w.DecodedSec)
	}
	if w.Pipeline != WindowPipelineID || w.Algorithm != WindowAlgorithm ||
		w.FpcalcVersion != "1.6.1" || w.FFmpegVersion != "8.0.1" ||
		w.SlotBP != 5000 || w.OffsetSec != 1800 || w.DurationUsedSec != 3600 ||
		w.DurationSource != DurationSourceBookFile || w.WindowSet != WindowSetWS1 {
		t.Errorf("provenance not stamped: %+v", *w)
	}
}

// TestFileWindow_ArgVectors pins the pipeline id to its argument vectors.
func TestFileWindow_ArgVectors(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	ffLog := filepath.Join(dir, "ff.args")
	fpLog := filepath.Join(dir, "fp.args")
	tools := WindowTools{
		FFmpegPath: writeScript(t, dir, "ffmpeg", fmt.Sprintf(`printf '%%s\n' "$@" > %q
%s`, ffLog, fakeFFmpegOK(120))),
		FpcalcPath: writeScript(t, dir, "fpcalc", fmt.Sprintf(`printf '%%s\n' "$@" > %q
%s`, fpLog, fakeFpcalcRaw(900))),
	}
	if _, err := tools.FileWindow(context.Background(), "/lib/a:b.m4b", testSpec); err != nil {
		t.Fatal(err)
	}
	ff, _ := os.ReadFile(ffLog)
	fp, _ := os.ReadFile(fpLog)
	wantFF := "-nostdin -hide_banner -v error -ss 1800.000 -i file:/lib/a:b.m4b -t 120.000 -map 0:a:0 -vn -ac 1 -ar 11025 -f s16le pipe:1"
	wantFP := "-format s16le -rate 11025 -channels 1 -length 120 -algorithm 2 -raw -json -"
	if got := strings.Join(strings.Fields(string(ff)), " "); got != wantFF {
		t.Errorf("ffmpeg args\n got %s\nwant %s", got, wantFF)
	}
	if got := strings.Join(strings.Fields(string(fp)), " "); got != wantFP {
		t.Errorf("fpcalc args\n got %s\nwant %s", got, wantFP)
	}
}

func TestFileWindow_FakeFailures(t *testing.T) {
	skipOnWindows(t)
	cases := []struct {
		name       string
		ffmpeg     string
		fpcalc     string
		wantErr    error
		wantSubstr string
	}{
		{
			name:       "ffmpeg exits non-zero",
			ffmpeg:     "echo 'moov atom not found' >&2; exit 1",
			fpcalc:     "cat >/dev/null; echo 'ERROR: Not enough audio data' >&2; exit 2",
			wantErr:    ErrWindowFFmpeg,
			wantSubstr: "moov atom not found",
		},
		{
			// PCM was produced in full, then ffmpeg failed: the exit status
			// must still fail the window (the old fpcalcAt discarded it).
			name:       "ffmpeg fails after full output",
			ffmpeg:     fakeFFmpegOK(120) + "; echo 'Error while decoding stream' >&2; exit 69",
			fpcalc:     fakeFpcalcRaw(900),
			wantErr:    ErrWindowFFmpeg,
			wantSubstr: "Error while decoding stream",
		},
		{
			name:       "fpcalc fails",
			ffmpeg:     fakeFFmpegOK(120),
			fpcalc:     "cat >/dev/null; echo 'ERROR: Error decoding audio frame' >&2; exit 2",
			wantErr:    ErrWindowFpcalc,
			wantSubstr: "Error decoding audio frame",
		},
		{
			name:   "fpcalc exits early without reading",
			ffmpeg: fakeFFmpegOK(120),
			fpcalc: "echo 'ERROR: bad option' >&2; exit 2",
			// ffmpeg must not be blamed for fpcalc closing the pipe.
			wantErr: ErrWindowFpcalc,
		},
		{
			name:    "fpcalc prints garbage",
			ffmpeg:  fakeFFmpegOK(120),
			fpcalc:  "cat >/dev/null; echo 'not json'",
			wantErr: ErrWindowParse,
		},
		{
			name:    "fpcalc compressed (non-raw) output rejected",
			ffmpeg:  fakeFFmpegOK(120),
			fpcalc:  `cat >/dev/null; echo '{"duration":0,"fingerprint":"AQAAAA"}'`,
			wantErr: ErrWindowParse,
		},
		{
			name:    "fewer than 80 frames",
			ffmpeg:  fakeFFmpegOK(120),
			fpcalc:  fakeFpcalcRaw(79),
			wantErr: ErrFingerprintTooShort,
		},
		{
			name:    "decoded audio short of the window",
			ffmpeg:  fakeFFmpegOK(100),
			fpcalc:  fakeFpcalcRaw(900),
			wantErr: ErrWindowShortDecode,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tools := WindowTools{
				FFmpegPath: writeScript(t, dir, "ffmpeg", tc.ffmpeg),
				FpcalcPath: writeScript(t, dir, "fpcalc", tc.fpcalc),
			}
			w, err := tools.FileWindow(context.Background(), "/lib/a.m4b", testSpec)
			if w != nil || !errors.Is(err, tc.wantErr) {
				t.Fatalf("got (%v, %v), want error %v", w, err, tc.wantErr)
			}
			if tc.wantSubstr != "" && !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error %q lacks tool stderr %q", err, tc.wantSubstr)
			}
		})
	}
}

// waitForFile blocks until path exists and returns its trimmed contents. The
// fake tools write their ready file with write-then-rename, so a file that
// exists is complete. The deadline only bounds a broken test; the result never
// depends on how fast anything runs. It reports through t.Errorf, so it may run
// on a goroutine other than the test's.
func waitForFile(t *testing.T, path string) (string, bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if b, err := os.ReadFile(path); err == nil {
			return strings.TrimSpace(string(b)), true
		}
		if time.Now().After(deadline) {
			t.Errorf("%s never appeared; the fake tool did not start", path)
			return "", false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// readyThen is a fake-tool body that records pidExpr in ready (atomically) and
// then runs rest.
func readyThen(ready, pidExpr, rest string) string {
	return fmt.Sprintf("echo %s > '%s.tmp' && mv '%s.tmp' '%s'\n%s", pidExpr, ready, ready, ready, rest)
}

// Every case cancels only after the fake ffmpeg is known to be running, so the
// kill path is what is exercised in every run. Until 2026-10-03 the cases
// raced a 200-300ms timer against process start-up and asserted a 5s
// wall-clock bound: under load the timer could fire before ffmpeg started
// (Start then returned the context error, which FileWindow flattened into a
// transient ffmpeg failure; fixed in startFailure) and a run that waited out
// the 2s WaitDelay twice could pass 5s.
func TestFileWindow_ContextKill(t *testing.T) {
	skipOnWindows(t)

	t.Run("single process", func(t *testing.T) {
		// exec: the sleep IS the ffmpeg process. If cancellation did not
		// kill it, FileWindow would wait out the hour and the test would hang.
		dir := t.TempDir()
		ready := filepath.Join(dir, "ready")
		tools := WindowTools{
			FFmpegPath: writeScript(t, dir, "ffmpeg", readyThen(ready, "$$", "exec sleep 3600")),
			FpcalcPath: writeScript(t, dir, "fpcalc", fakeFpcalcRaw(900)),
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			_, _ = waitForFile(t, ready)
			cancel()
		}()
		_, err := tools.FileWindow(ctx, "/lib/a.m4b", testSpec)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want Canceled", err)
		}
	})

	t.Run("orphaned grandchild holds pipe", func(t *testing.T) {
		// The grandchild inherits the pipe's write end and survives the kill
		// of its parent; the read end must still be closed on cancel. Its
		// stderr goes to /dev/null so the run does not also wait out
		// ffmpeg's 2s WaitDelay on the stderr pipe, which is not under test.
		dir := t.TempDir()
		ready := filepath.Join(dir, "ready")
		tools := WindowTools{
			FFmpegPath: writeScript(t, dir, "ffmpeg",
				"sleep 3600 2>/dev/null &\n"+readyThen(ready, "$!", "wait\nexit 0")),
			FpcalcPath: writeScript(t, dir, "fpcalc", fakeFpcalcRaw(900)),
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		pidCh := make(chan int, 1)
		go func() {
			var pid int
			if got, ok := waitForFile(t, ready); ok {
				_, _ = fmt.Sscan(got, &pid)
			}
			pidCh <- pid
			cancel()
		}()
		_, err := tools.FileWindow(ctx, "/lib/a.m4b", testSpec)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want Canceled", err)
		}
		pid := <-pidCh
		if pid <= 0 {
			t.Fatalf("bad grandchild pid %d", pid)
		}
		proc, findErr := os.FindProcess(pid)
		if findErr != nil {
			t.Fatalf("find grandchild %d: %v", pid, findErr)
		}
		t.Cleanup(func() { _ = proc.Kill() })
		// The grandchild is still alive, still holding the write end: so
		// FileWindow returned because it closed the read end, not because
		// the pipe's last writer went away.
		if sigErr := proc.Signal(syscall.Signal(0)); sigErr != nil {
			t.Fatalf("grandchild %d is gone (%v); the test no longer proves FileWindow returns while it holds the pipe", pid, sigErr)
		}
	})

	t.Run("per-window timeout", func(t *testing.T) {
		// The timeout can expire before ffmpeg starts or while it runs; both
		// must report DeadlineExceeded (see TestFileWindow_DoneContextBeforeStart).
		dir := t.TempDir()
		tools := WindowTools{
			FFmpegPath: writeScript(t, dir, "ffmpeg", "exec sleep 3600"),
			FpcalcPath: writeScript(t, dir, "fpcalc", fakeFpcalcRaw(900)),
			Timeout:    300 * time.Millisecond,
		}
		_, err := tools.FileWindow(context.Background(), "/lib/a.m4b", testSpec)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want DeadlineExceeded", err)
		}
	})
}

func TestFileWindow_RejectsBadInput(t *testing.T) {
	if _, err := (WindowTools{}).FileWindow(context.Background(), "/x", testSpec); !errors.Is(err, ErrWindowToolsMissing) {
		t.Errorf("empty tools err = %v", err)
	}
	bad := testSpec
	bad.LengthSec = 0
	if _, err := (WindowTools{FpcalcPath: "/bin/true", FFmpegPath: "/bin/true"}).FileWindow(context.Background(), "/x", bad); err == nil {
		t.Error("zero-length spec accepted")
	}
}

func TestParseRawFpcalcJSON(t *testing.T) {
	got, err := parseRawFpcalcJSON([]byte(`{"duration": 0.00, "fingerprint": [0, 4294967295, -1, 2147483647]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []uint32{0, 4294967295, 4294967295, 2147483647}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v want %v", got, want)
	}
	for _, bad := range []string{
		`{"fingerprint": [4294967296]}`,
		`{"fingerprint": [1.5]}`,
		`{"fingerprint": "AQAAAA"}`,
		``,
	} {
		if _, err := parseRawFpcalcJSON([]byte(bad)); !errors.Is(err, ErrWindowParse) {
			t.Errorf("parse(%q) err = %v, want ErrWindowParse", bad, err)
		}
	}
}

func TestCappedBuffer(t *testing.T) {
	b := cappedBuffer{limit: 5}
	for _, s := range []string{"abc", "defgh", "ij"} {
		if n, err := b.Write([]byte(s)); n != len(s) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", s, n, err)
		}
	}
	if b.String() != "abcde [truncated]" {
		t.Errorf("String() = %q", b.String())
	}
}

// TestFileSegments_OffsetSegmentsViaWindowPipe is the regression for the
// deleted fpcalcAt: with fpcalc present, segments at offset > 0 used to be
// decoded from `-raw -json` into a string field, fail, and be left empty.
func TestFileSegments_OffsetSegmentsViaWindowPipe(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	// Direct mode (a path argument) prints a compressed-style string; stdin
	// mode ("-" as the last argument) prints raw frames.
	fpcalc := writeScript(t, dir, "fpcalc", `for a in "$@"; do last="$a"; done
if [ "$last" = "-" ]; then
`+fakeFpcalcRaw(2400)+`
else
  printf '{"duration": 3600.00, "fingerprint": "AQAAHEAD"}\n'
fi`)
	writeScript(t, dir, "ffmpeg", fakeFFmpegOK(SegmentSeconds))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	SetResolvedFpcalcPath(fpcalc)
	t.Cleanup(func() { SetResolvedFpcalcPath("") })

	segs, err := FileSegments(filepath.Join(dir, "book.m4b"), 3600)
	if err != nil {
		t.Fatal(err)
	}
	if segs[0] != "AQAAHEAD" {
		t.Errorf("seg0 = %q, want fpcalc's direct output", segs[0])
	}
	for i := 1; i < NumSegments; i++ {
		if !IsUsefulFingerprint(segs[i]) {
			t.Fatalf("seg%d = %q, want a usable print", i, segs[i])
		}
		ints, err := decodeAnyFingerprint(segs[i])
		if err != nil || len(ints) != 2400 || ints[0] != 4000000000 {
			t.Fatalf("seg%d decodes to %d frames (first %v), err %v", i, len(ints), ints[:min(1, len(ints))], err)
		}
	}
}

// A context that is already done when FileWindow starts the tools must come
// back as that context's error. exec.Cmd.Start returns ctx.Err() for a done
// context, and wrapping it with %v as a transient ffmpeg failure dropped the
// chain: under load the per-window timeout could expire before ffmpeg started,
// and TestFileWindow_ContextKill saw "ffmpeg failed: transient" instead of
// DeadlineExceeded.
func TestFileWindow_DoneContextBeforeStart(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	tools := WindowTools{
		FFmpegPath: writeScript(t, dir, "ffmpeg", fakeFFmpegOK(120)),
		FpcalcPath: writeScript(t, dir, "fpcalc", fakeFpcalcRaw(900)),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tools.FileWindow(ctx, "/lib/a.m4b", testSpec)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want Canceled", err)
	}
	if errors.Is(err, ErrWindowTransient) || errors.Is(err, ErrWindowFFmpeg) {
		t.Fatalf("err = %v: a canceled context is not a tool failure", err)
	}
}
