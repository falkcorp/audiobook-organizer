// file: internal/audioutil/duration.go
// version: 1.2.1
// guid: 03399668-0f87-4d27-b118-8315b574ef23
// last-edited: 2026-09-02

// Package audioutil holds small, dependency-free helpers shared by the audio
// processing packages (internal/mediainfo, internal/fingerprint,
// internal/transcode). It exists to stop those packages from independently
// reimplementing the same duration probe — see ProbeDurationSeconds, which
// tries mediainfo and falls back to ffprobe.
package audioutil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// ErrFFprobeNotAvailable is returned by LookupFFprobe when no ffprobe binary can
// be resolved. It is a sentinel so callers can distinguish "the tool is missing"
// from "the tool ran and the file was unreadable" — a distinction that matters
// because the two demand opposite responses. A missing binary means EVERY probe
// in a run will fail, so an op that treats it as a per-file error reports a
// library full of unprobeable files: a clean-looking run that actually measured
// nothing. Callers must check availability ONCE, up front, and refuse to run.
var ErrFFprobeNotAvailable = errors.New("audioutil: ffprobe not found on PATH — install ffmpeg (which ships ffprobe)")

// LookupFFprobe resolves the ffprobe binary on PATH, returning the absolute path
// for callers to pass to ProbeDurationSeconds.
//
// This mirrors the detect-or-disable convention the fingerprint package already
// uses (fingerprint.ErrNotAvailable + fingerprint.Available): resolve the binary
// once, and let a feature that depends on it announce that it cannot run rather
// than degrade into silently producing empty results.
//
// Resolving once and passing the path down also avoids re-running a PATH search
// per file, which matters for ops that probe thousands of files.
func LookupFFprobe() (string, error) {
	p, err := exec.LookPath("ffprobe")
	if err != nil {
		return "", ErrFFprobeNotAvailable
	}
	return p, nil
}

// FFprobeAvailable reports whether LookupFFprobe would succeed.
func FFprobeAvailable() bool {
	_, err := LookupFFprobe()
	return err == nil
}

// ProbeDurationSeconds returns the container's reported duration in seconds, as
// a float64 with no rounding applied.
//
// It tries mediainfo FIRST and falls back to ffprobe on any failure, including
// the empty-output-with-exit-0 case mediainfo produces for files it cannot read
// (see probeDurationMediaInfo). ffprobe remains the source of truth: every
// value mediainfo cannot answer confidently is answered by ffprobe exactly as
// before, so this changes which tool answers, not what an answer means.
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
	if bin, err := LookupMediaInfo(); err == nil {
		secs, mierr := probeDurationMediaInfo(ctx, bin, filePath)
		if mierr == nil {
			mediaInfoHits.Add(1)
			return secs, nil
		}
		noteMediaInfoFallback(filePath, mierr)
	}
	return probeDurationFFprobe(ctx, ffprobePath, filePath)
}

// Fallback accounting. A per-file log line would be 60k lines of noise across a
// library scan, but a SILENT fallback is worse: if mediainfo failed on every
// file we would quietly do two subprocess calls per file instead of one, be
// slower than before the change, and have no signal saying so. So: warn once
// with the concrete reason, then count.
var (
	mediaInfoHits      atomic.Int64
	mediaInfoFallbacks atomic.Int64
	mediaInfoWarnOnce  sync.Once
)

func noteMediaInfoFallback(filePath string, err error) {
	mediaInfoFallbacks.Add(1)
	mediaInfoWarnOnce.Do(func() {
		slog.Warn("audioutil: mediainfo duration probe failed, falling back to ffprobe "+
			"(logged once; use DurationProbeStats for totals)",
			"file", filePath, "err", err)
	})
}

// DurationProbeStats reports how many duration probes mediainfo answered and how
// many fell back to ffprobe. Exposed so an op can report the split rather than
// leaving a systematic mediainfo failure invisible.
func DurationProbeStats() (mediaInfo, fallback int64) {
	return mediaInfoHits.Load(), mediaInfoFallbacks.Load()
}

// probeDurationFFprobe is the original ffprobe implementation, unchanged. It
// remains the source of truth for duration semantics: mediainfo is an
// accelerator in front of it, and anything mediainfo cannot answer confidently
// is answered here.
func probeDurationFFprobe(ctx context.Context, ffprobePath, filePath string) (float64, error) {
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

// DurationProbeAvailable reports whether ANY duration prober is usable. Ops that
// refuse to start without a prober should gate on this rather than on
// FFprobeAvailable alone, or a box carrying only mediainfo refuses work it could
// actually do.
func DurationProbeAvailable() bool {
	return MediaInfoAvailable() || FFprobeAvailable()
}
