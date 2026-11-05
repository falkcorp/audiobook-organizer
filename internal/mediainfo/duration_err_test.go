// file: internal/mediainfo/duration_err_test.go
// version: 1.0.0
// guid: f887e2cd-a3a0-4243-8bbd-b87a475f4f35
// last-edited: 2026-11-05

package mediainfo

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/audioext"
)

// fakeFFprobe puts a stub "ffprobe" shell script first (and only) on PATH and
// disables mediainfo, so the duration probe's outcome is exactly what the script
// does. No audio is read or decoded.
func fakeFFprobe(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if script != "" {
		if err := os.WriteFile(filepath.Join(dir, "ffprobe"), []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":/bin:/usr/bin")
	if script == "" {
		t.Setenv("PATH", dir) // nothing but an empty directory: ffprobe is not installed
	}
	t.Setenv("ABK_DISABLE_MEDIAINFO", "1")
}

// junkAudio is a non-empty file with an audio extension whose tags cannot be
// read, so Extract takes the inferFromFormat path and has bytes to estimate from.
func junkAudio(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, make([]byte, 480_000), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtract_DurationErr(t *testing.T) {
	t.Run("stream read succeeds: no error, not estimated", func(t *testing.T) {
		fakeFFprobe(t, "echo 3725.4")
		info, err := Extract(junkAudio(t, "Year One.m4b"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Duration != 3725 || info.DurationEstimated || info.DurationErr != nil {
			t.Fatalf("got %+v", info)
		}
	})
	t.Run("ffprobe missing: estimate flagged and the cause is exec.ErrNotFound", func(t *testing.T) {
		fakeFFprobe(t, "")
		info, err := Extract(junkAudio(t, "Year One.m4b"))
		if err != nil {
			t.Fatal(err)
		}
		if !info.DurationEstimated || info.Duration <= 0 {
			t.Fatalf("want a flagged estimate, got %+v", info)
		}
		if !errors.Is(info.DurationErr, exec.ErrNotFound) {
			t.Fatalf("DurationErr = %v, want exec.ErrNotFound in the chain", info.DurationErr)
		}
	})
	t.Run("ffprobe exits 1: estimate flagged and the cause is the exit error", func(t *testing.T) {
		fakeFFprobe(t, "exit 1")
		info, _ := Extract(junkAudio(t, "Year One.m4b"))
		var ee *exec.ExitError
		if !info.DurationEstimated || !errors.As(info.DurationErr, &ee) {
			t.Fatalf("got %+v", info)
		}
		if errors.Is(info.DurationErr, exec.ErrNotFound) || errors.Is(info.DurationErr, fs.ErrNotExist) {
			t.Fatalf("an exit error was misclassified: %v", info.DurationErr)
		}
	})
	t.Run("ffprobe reports 0: error says so", func(t *testing.T) {
		fakeFFprobe(t, "echo 0.000000")
		info, _ := Extract(junkAudio(t, "Year One.m4b"))
		if info.DurationErr == nil || info.DurationErr.Error() != "probe returned 0s" {
			t.Fatalf("DurationErr = %v", info.DurationErr)
		}
	})
	t.Run("ffprobe prints nonsense: error is a parse error", func(t *testing.T) {
		fakeFFprobe(t, "echo N/A")
		info, _ := Extract(junkAudio(t, "Year One.m4b"))
		if info.DurationErr == nil || info.Duration <= 0 || !info.DurationEstimated {
			t.Fatalf("got %+v", info)
		}
	})
	t.Run("timeout: the cause is context.DeadlineExceeded", func(t *testing.T) {
		fakeFFprobe(t, "exec sleep 30")
		prev := ffprobeDurationTimeout
		ffprobeDurationTimeout = 200 * time.Millisecond
		t.Cleanup(func() { ffprobeDurationTimeout = prev })
		info, _ := Extract(junkAudio(t, "Year One.m4b"))
		if !errors.Is(info.DurationErr, context.DeadlineExceeded) {
			t.Fatalf("DurationErr = %v, want context.DeadlineExceeded in the chain", info.DurationErr)
		}
	})
	t.Run("no bitrate to estimate from: still carries the error with Duration 0", func(t *testing.T) {
		fakeFFprobe(t, "exit 1")
		p := filepath.Join(t.TempDir(), "empty.m4b") // zero bytes: nothing to estimate from
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		info, _ := Extract(p)
		if info.Duration != 0 || info.DurationEstimated || info.DurationErr == nil {
			t.Fatalf("got %+v", info)
		}
	})
	t.Run("file absent: Extract fails with fs.ErrNotExist", func(t *testing.T) {
		fakeFFprobe(t, "exit 1")
		_, err := Extract(filepath.Join(t.TempDir(), "gone.m4b"))
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unsupported container: no duration, error says so", func(t *testing.T) {
		fakeFFprobe(t, "exit 1")
		p := filepath.Join(t.TempDir(), "notes.txt")
		if err := os.WriteFile(p, []byte("not audio"), 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := Extract(p)
		if err == nil && (info == nil || info.DurationErr == nil) {
			t.Fatalf("an unsupported file must report an error: %+v", info)
		}
	})
}

// Every extension the library treats as audio (audioext.Default()), whether or
// not inferFromFormat has a codec table for it, gets its duration from ffprobe.
// .wav, .opus, .aax, .wma, .aac and .mka used to come back as "unsupported
// format" without ffprobe being run, so good files of those types counted failed
// probes and went unreadable after three runs.
func TestExtract_EveryAudioExtensionIsProbed(t *testing.T) {
	fakeFFprobe(t, "echo 5400.0")
	for _, ext := range audioext.Default() {
		t.Run(ext, func(t *testing.T) {
			info, err := Extract(junkAudio(t, "The Long Halloween"+ext))
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if info.Duration != 5400 || info.DurationEstimated || info.DurationErr != nil {
				t.Fatalf("want a measured 5400 s with no error, got %+v", info)
			}
		})
	}
}

// A known audio extension without a codec table that ffprobe really cannot read
// is an ordinary failed probe (counted), NOT ErrUnsupportedFormat; no estimate is
// invented for it.
func TestExtract_NoCodecTableExtensionFailure(t *testing.T) {
	fakeFFprobe(t, "exit 1")
	info, err := Extract(junkAudio(t, "Hush.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Duration != 0 || info.DurationEstimated {
		t.Fatalf("no bitrate, so no estimate: got %+v", info)
	}
	var ee *exec.ExitError
	if !errors.As(info.DurationErr, &ee) || errors.Is(info.DurationErr, ErrUnsupportedFormat) {
		t.Fatalf("DurationErr = %v, want the plain exit error", info.DurationErr)
	}
}

// countingProbes puts a stub ffprobe on PATH that counts its runs (the mediainfo
// fallback is disabled; every probe, mediainfo's included, goes through
// realDurationSec, which the refused paths never reach), so "no probe was
// started" is observable. runs() is the number of ffprobe invocations.
func countingProbes(t *testing.T) (runs func() int) {
	t.Helper()
	dir := t.TempDir()
	counter := filepath.Join(t.TempDir(), "runs")
	if err := os.WriteFile(counter, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho run >> '" + counter + "'\necho 61.0\n"
	if err := os.WriteFile(filepath.Join(dir, "ffprobe"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/bin:/usr/bin")
	t.Setenv("ABK_DISABLE_MEDIAINFO", "1")
	return func() int {
		b, err := os.ReadFile(counter)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(b), "run")
	}
}

// A path that is not an audio file is an ErrUnsupportedFormat error and starts
// NO subprocess (GetAudiobookTags hands Extract a book's directory on every
// request; mediainfo would walk the whole tree). A real audio extension still
// gets its probe.
func TestExtract_NonAudioPathsAreRefusedWithoutAProbe(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Cover.jpg"), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"directory": dir,
		"cover.jpg": filepath.Join(dir, "Cover.jpg"),
		"notes.txt": junkAudio(t, "Notes.txt"),
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			runs := countingProbes(t)
			info, err := Extract(path)
			if !errors.Is(err, ErrUnsupportedFormat) || info != nil {
				t.Fatalf("got %+v, %v; want nil and ErrUnsupportedFormat", info, err)
			}
			if n := runs(); n != 0 {
				t.Fatalf("%d ffprobe run(s) for %s", n, name)
			}
		})
	}
	t.Run("audio extension is probed", func(t *testing.T) {
		runs := countingProbes(t)
		info, err := Extract(junkAudio(t, "Hush.wav"))
		if err != nil || info.Duration != 61 {
			t.Fatalf("got %+v, %v", info, err)
		}
		if runs() == 0 {
			t.Fatal("a .wav must be probed")
		}
	})
}

// BuildFromTag on a tag type it has no table for and a non-audio path returns a
// best-effort info carrying ErrUnsupportedFormat on DurationErr, no probe.
func TestInferFromFormat_NonAudioStartsNoProbe(t *testing.T) {
	runs := countingProbes(t)
	for _, name := range []string{"Cover.jpg", "Notes.txt", "README"} {
		if info, err := inferFromFormat(filepath.Join(t.TempDir(), name), &MediaInfo{}); !errors.Is(err, ErrUnsupportedFormat) || info != nil {
			t.Fatalf("%s: got %+v, %v", name, info, err)
		}
	}
	if n := runs(); n != 0 {
		t.Fatalf("%d probe subprocess(es) started", n)
	}
}

// statCause: a stat that says the failure was the environment's (a permission
// problem, a dead mount, a missing file) is wrapped so errors.Is finds it; any
// other outcome leaves the probe error alone.
func TestStatCause(t *testing.T) {
	probeErr := errors.New("ffprobe: exit status 1")
	pe := func(e error) error { return &fs.PathError{Op: "stat", Path: "/x", Err: e} }
	cases := []struct {
		name string
		serr error
		want error // nil: probeErr unchanged
	}{
		{"stat ok", nil, nil},
		{"missing", pe(syscall.ENOENT), fs.ErrNotExist},
		{"permission", pe(syscall.EACCES), fs.ErrPermission},
		{"not permitted", pe(syscall.EPERM), fs.ErrPermission},
		{"stale handle", pe(syscall.ESTALE), syscall.ESTALE},
		{"not connected", pe(syscall.ENOTCONN), syscall.ENOTCONN},
		{"something else", pe(syscall.EIO), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := statCause(tc.serr, probeErr)
			if !errors.Is(got, probeErr) {
				t.Fatalf("the probe error was lost: %v", got)
			}
			if tc.want == nil {
				if got != probeErr {
					t.Fatalf("want the probe error unchanged, got %v", got)
				}
				return
			}
			if !errors.Is(got, tc.want) {
				t.Fatalf("errors.Is(%v, %v) = false", got, tc.want)
			}
		})
	}
}

// End to end through the real os.Stat: a file inside a directory this process
// cannot search fails ffprobe like any other file and stats as EACCES.
func TestExtract_UnsearchableDirectoryIsPermission(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a non-root POSIX user to make a directory unsearchable")
	}
	fakeFFprobe(t, "exit 1")
	dir := filepath.Join(t.TempDir(), "Arkham")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "Hush.m4b")
	if err := os.WriteFile(p, make([]byte, 480_000), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	_, err := realDurationSec(p)
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("realDurationSec = %v, want fs.ErrPermission in the chain", err)
	}
	if strings.Contains(err.Error(), "no such file") {
		t.Fatalf("misreported as absent: %v", err)
	}
}
