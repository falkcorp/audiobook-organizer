// file: internal/transcribe/whisper.go
// version: 2.0.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-11-03

// Package transcribe extracts and transcribes the opening seconds of an audio
// file.
//
// TranscribeIntroClip cuts a short clip with ffmpeg (30-second, 16 kHz, mono
// WAV) and sends it through TranscribeBatchOpts, i.e. the self-hosted Whisper
// worker pool the intro-transcribe ops use. It has no backend of its own: no
// in-process model and no paid cloud API. A pool outage is returned as an error
// (a *TransportError when every endpoint is exhausted) and never turned into a
// transcript.
package transcribe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// IntroSeconds is how many seconds to extract from the start of the file.
	IntroSeconds = 30
)

// introJobKey is the opaque job key for the single clip a TranscribeIntroClip
// call sends.
const introJobKey = "intro"

// transcribeBatch is a test seam over TranscribeBatchOpts so tests can stub the
// pool. Production code never reassigns it.
var transcribeBatch = TranscribeBatchOpts

// TranscribeIntroClip extracts the first IntroSeconds of the given audio file
// and returns the text the Whisper pool transcribed from it. The text is not
// parsed; call ParseAudiobookIntro to extract structured title/author/narrator
// fields.
//
// journal may be nil. When set it is the same restart-safe result journal the
// intro-transcribe ops use (see BatchOptions.Journal).
//
// An empty transcript is an error: a caller treats "the pool answered with
// nothing" the same as "the pool failed", and neither is a verdict about the
// book.
func TranscribeIntroClip(ctx context.Context, audioPath string, journal ResultJournal) (string, error) {
	tmpWAV := filepath.Join(os.TempDir(), fmt.Sprintf("ao-intro-%d.wav", time.Now().UnixNano()))
	defer os.Remove(tmpWAV)

	ffCmd := exec.CommandContext(ctx, "ffmpeg",
		"-y", "-i", audioPath,
		"-t", fmt.Sprintf("%d", IntroSeconds),
		"-vn", "-ar", "16000", "-ac", "1", "-f", "wav",
		tmpWAV,
	)
	if out, err := ffCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ffmpeg extract: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	res, err := transcribeBatch(ctx, map[string]string{introJobKey: tmpWAV}, BatchOptions{Journal: journal})
	return introTextFromBatch(res, err)
}

// introTextFromBatch turns the outcome of the single-clip batch into text or an
// error. A batch-level error (including *TransportError / pool unavailable) wins
// over any partial result and is returned wrapped so errors.As / IsPoolUnavailable
// still see it. A per-job Error or Condemned verdict, a missing result and an
// empty transcript are all errors.
func introTextFromBatch(res map[string]BatchResult, err error) (string, error) {
	if err != nil {
		return "", fmt.Errorf("whisper pool: %w", err)
	}
	r, ok := res[introJobKey]
	switch {
	case !ok:
		return "", errors.New("whisper pool: no result for the intro clip")
	case r.Condemned:
		return "", fmt.Errorf("whisper pool: clip condemned (%s)", r.Error)
	case r.Error != "":
		return "", fmt.Errorf("whisper pool: %s", r.Error)
	}
	text := strings.TrimSpace(r.Text)
	if text == "" {
		return "", errors.New("whisper pool: empty transcript")
	}
	return text, nil
}
