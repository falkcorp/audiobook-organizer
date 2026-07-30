// file: internal/audioutil/chapters.go
// version: 1.0.0
// guid: 69dd107e-d06d-429a-9a3b-3c85551edf7e
// last-edited: 2026-07-29

package audioutil

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
)

// ffprobeChaptersOutput mirrors the JSON shape of
// `ffprobe -show_chapters -print_format json`. ffprobe reports each
// chapter's boundaries two ways: start/end as integers paired with a
// time_base fraction, and start_time/end_time as decimal-string seconds. We
// deliberately parse the *_time string fields (see ProbeChapters) rather
// than doing the start*time_base division ourselves, since that is exactly
// what real ABS ground truth expects (float seconds with full precision,
// not reconstructed from an integer+rational pair).
type ffprobeChaptersOutput struct {
	Chapters []ffprobeChapter `json:"chapters"`
}

type ffprobeChapter struct {
	ID        int    `json:"id"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
	Tags      struct {
		Title string `json:"title"`
	} `json:"tags"`
}

// ProbeChapters shells out to ffprobe and returns the container's embedded
// chapter list, parsed from `-show_chapters -print_format json`.
//
// ffprobePath selects which ffprobe binary to invoke; pass "" to resolve
// "ffprobe" from PATH, matching ProbeDurationSeconds's convention. ctx bounds
// the subprocess.
//
// A file with no embedded chapters is NOT an error: ffprobe reports an empty
// "chapters" array for e.g. a bare mp3 track, and ProbeChapters returns
// (nil, nil) in that case so callers can distinguish "no chapters" from
// "ffprobe failed" without inspecting the error. Chapter.Title is taken from
// tags.title; if absent, it is left as the empty string and callers decide a
// fallback (see SynthesizeChapters for the multi-file fallback-to-filename
// case this mirrors in real ABS).
func ProbeChapters(ctx context.Context, ffprobePath, filePath string) ([]Chapter, error) {
	bin := ffprobePath
	if bin == "" {
		bin = "ffprobe"
	}
	cmd := exec.CommandContext(ctx, bin,
		"-v", "error",
		"-show_chapters",
		"-print_format", "json",
		filePath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffprobe chapters %s: %w: %s", filePath, err, stderr.String())
	}

	var parsed ffprobeChaptersOutput
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		return nil, fmt.Errorf("ffprobe chapters parse %s: %w", filePath, err)
	}

	if len(parsed.Chapters) == 0 {
		return nil, nil
	}

	chapters := make([]Chapter, len(parsed.Chapters))
	for i, c := range parsed.Chapters {
		start, err := strconv.ParseFloat(c.StartTime, 64)
		if err != nil {
			return nil, fmt.Errorf("ffprobe chapters %s: chapter %d start_time %q: %w", filePath, i, c.StartTime, err)
		}
		end, err := strconv.ParseFloat(c.EndTime, 64)
		if err != nil {
			return nil, fmt.Errorf("ffprobe chapters %s: chapter %d end_time %q: %w", filePath, i, c.EndTime, err)
		}
		chapters[i] = Chapter{
			ID:       c.ID,
			StartSec: start,
			EndSec:   end,
			Title:    c.Tags.Title,
		}
	}
	return chapters, nil
}
