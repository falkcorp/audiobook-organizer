// file: internal/audioutil/duration.go
// version: 1.0.0
// guid: 03399668-0f87-4d27-b118-8315b574ef23
// last-edited: 2026-07-18

// Package audioutil holds small, dependency-free helpers shared by the audio
// processing packages (internal/mediainfo, internal/fingerprint,
// internal/transcode). It exists to stop those packages from independently
// reimplementing the same ffprobe subprocess call — see ProbeDurationSeconds.
package audioutil

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ProbeDurationSeconds shells out to ffprobe and returns the container's
// reported duration in seconds, as a float64 with no rounding applied.
//
// This consolidates what were previously three independent ffprobe-shelling
// implementations (internal/mediainfo.realDurationSec, internal/fingerprint's
// unexported probeDuration, and internal/transcode's unexported probeDuration /
// probeFileDuration) that had drifted in small ways (JSON vs plain-text
// ffprobe output, int vs float64 vs microsecond-int64 return types, presence
// or absence of a timeout) and were the kind of thing behind past duration
// bugs (see TODO item 20 / AP-3b). All three now call this function and
// convert/validate at their own boundary instead of reimplementing the probe.
//
// ffprobePath selects which ffprobe binary to invoke; pass "" to resolve
// "ffprobe" from PATH. ctx bounds the subprocess — pass context.Background()
// for no timeout, or a context.WithTimeout for a bounded call (mediainfo does
// this since a stuck ffprobe invocation must not hang the scanner).
//
// The returned value is NOT validated against zero/negative and callers apply
// their own validity rules: mediainfo treats <=0 as "unknown" and falls back
// to a flagged filesize/bitrate estimate, while transcode's chapter builder
// has historically accepted whatever ffprobe reports. Preserving that
// difference at the call site (rather than baking a single policy into this
// helper) is intentional — this is a consolidation of the probe mechanism,
// not a change to each caller's duration semantics.
func ProbeDurationSeconds(ctx context.Context, ffprobePath, filePath string) (float64, error) {
	bin := ffprobePath
	if bin == "" {
		bin = "ffprobe"
	}
	cmd := exec.CommandContext(ctx, bin,
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		filePath)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("ffprobe %s: %w", filePath, err)
	}
	secs, err := strconv.ParseFloat(strings.TrimSpace(stdout.String()), 64)
	if err != nil {
		return 0, fmt.Errorf("ffprobe duration parse %s: %w", filePath, err)
	}
	return secs, nil
}
