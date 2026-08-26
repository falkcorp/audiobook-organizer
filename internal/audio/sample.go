// file: internal/audio/sample.go
// version: 1.0.0
// guid: c1d2e3f4-a5b6-7c8d-9e0f-1a2b3c4d5e6f

package audio

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"

	"github.com/falkcorp/audiobook-organizer/internal/transcode"
)

const (
	SampleMaxDuration = 60 // seconds — hard cap per request
	SampleDefault     = 30 // seconds — default clip length
)

// SampleRequest encapsulates parameters for audio sampling.
type SampleRequest struct {
	FilePath string // path to audio file
	Start    int    // offset in seconds (default 0)
	Duration int    // clip length in seconds (default 30)
}

// ExtractSample streams a short MP3 clip from an audio file.
//
// Parameters:
//   - req.FilePath: path to audio file
//   - req.Start: offset in seconds (clamped to [0, ∞))
//   - req.Duration: clip length in seconds (capped at SampleMaxDuration)
//
// ffmpeg seeks to `req.Start` before opening the input (-ss before -i) so it
// reads only the needed frames rather than decoding from the beginning.
// Output is streamed as audio/mpeg data via the io.Writer callback.
func ExtractSample(ctx context.Context, req *SampleRequest, write func([]byte) (int, error)) error {
	if req == nil {
		return fmt.Errorf("request cannot be nil")
	}
	if req.FilePath == "" {
		return fmt.Errorf("file path is empty")
	}

	// Clamp and validate parameters
	start := max(req.Start, 0)
	dur := req.Duration
	if dur <= 0 {
		dur = SampleDefault
	}
	if dur > SampleMaxDuration {
		dur = SampleMaxDuration
	}

	ffmpegPath, err := transcode.FindFFmpeg()
	if err != nil {
		return fmt.Errorf("ffmpeg not available: %w", err)
	}

	args := []string{
		"-ss", strconv.Itoa(start), // seek before input (fast)
		"-i", req.FilePath,
		"-t", strconv.Itoa(dur),
		"-vn",                 // no video/cover stream
		"-map_chapters", "-1", // strip chapters so mp3 muxer is happy
		"-f", "mp3",
		"-q:a", "5", // ~130 kbps VBR — fine for comparison
		"pipe:1",
	}

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("pipe error: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ffmpeg start error: %w", err)
	}

	// Copy stdout to writer
	buf := make([]byte, 32*1024)
	for {
		n, readErr := stdout.Read(buf)
		if n > 0 {
			if _, writeErr := write(buf[:n]); writeErr != nil {
				_ = cmd.Wait()
				return fmt.Errorf("write error: %w", writeErr)
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				_ = cmd.Wait()
				return fmt.Errorf("read error: %w", readErr)
			}
			break
		}
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("ffmpeg wait error: %w", err)
	}

	return nil
}
