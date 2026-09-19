// file: internal/fingerprint/window_env_test.go
// version: 1.0.0
// guid: 3b1f6d0e-2a7c-4e59-8f1d-6c0b9a4e7d21
// last-edited: 2026-09-19

package fingerprint

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tools must not inherit the caller's environment: fp-worker holds its
// API key in the process (and may have read it from AO_FP_WORKER_KEY), and
// anything in a child's environment is visible in `ps eww`.
func TestToolChildren_GetMinimalEnv(t *testing.T) {
	skipOnWindows(t)
	t.Setenv("AO_FP_WORKER_KEY", "secret-key-7c1e")
	t.Setenv("UNRELATED_SECRET", "other-secret-9d2a")
	dir := t.TempDir()
	dump := func(name string) string { return fmt.Sprintf("env > %q", filepath.Join(dir, name+".env")) }

	fpV := writeScript(t, dir, "fpcalc-v", dump("fpcalc-version")+"\necho 'fpcalc version 1.6.1'")
	ffV := writeScript(t, dir, "ffmpeg-v", dump("ffmpeg-version")+"\necho 'ffmpeg version 8.0.1'")
	if _, err := ToolVersions(context.Background(), fpV, ffV); err != nil {
		t.Fatalf("ToolVersions: %v", err)
	}
	tools := WindowTools{
		FFmpegPath: writeScript(t, dir, "ffmpeg", dump("ffmpeg-window")+"\n"+fakeFFmpegOK(120)),
		FpcalcPath: writeScript(t, dir, "fpcalc", dump("fpcalc-window")+"\n"+fakeFpcalcRaw(900)),
	}
	if _, err := tools.FileWindow(context.Background(), filepath.Join(dir, "in.mp3"), testSpec); err != nil {
		t.Fatalf("FileWindow: %v", err)
	}
	for _, name := range []string{"fpcalc-version", "ffmpeg-version", "ffmpeg-window", "fpcalc-window"} {
		b, err := os.ReadFile(filepath.Join(dir, name+".env"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		env := string(b)
		for _, bad := range []string{"secret-key-7c1e", "other-secret-9d2a", "AO_FP_WORKER_KEY", "UNRELATED_SECRET"} {
			if strings.Contains(env, bad) {
				t.Errorf("%s: child environment contains %q", name, bad)
			}
		}
		if !strings.Contains(env, "PATH=") {
			t.Errorf("%s: child environment lacks PATH", name)
		}
	}
}
