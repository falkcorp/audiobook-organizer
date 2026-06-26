// file: internal/transcribe/whisper.go
// version: 1.0.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-06-26

// Package transcribe extracts and transcribes the opening seconds of an audio
// file. It tries backends in preference order:
//
//  1. whisper-cpp (local binary — fastest, fully offline)
//  2. whisper / main (alternate whisper.cpp binary names)
//  3. OpenAI Whisper API (whisper-1 model — requires openai_api_key in config)
//
// Audio is extracted with ffmpeg to a 30-second 16 kHz mono WAV before being
// sent to the transcription backend.
package transcribe

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
)


const (
	// IntroSeconds is how many seconds to extract from the start of the file.
	// 30s covers the full "Title by Author. Read by Narrator" announcement in
	// virtually every commercial audiobook, even with a brief music intro.
	IntroSeconds = 30
)

// TranscribeFirst30s extracts the first IntroSeconds of the given audio file
// and returns the transcribed text. The text is not parsed — call
// ParseAudiobookIntro to extract structured title/author/narrator fields.
//
// The function creates a temporary WAV file in os.TempDir(), removes it on
// return, and honours ctx cancellation throughout.
func TranscribeFirst30s(ctx context.Context, audioPath string) (string, error) {
	// Extract first 30 seconds to a temp WAV (16 kHz mono — Whisper's native format).
	tmpWAV := filepath.Join(os.TempDir(), fmt.Sprintf("ao-intro-%d.wav", time.Now().UnixNano()))
	defer os.Remove(tmpWAV)

	ffmpegArgs := []string{
		"-y", "-i", audioPath,
		"-t", fmt.Sprintf("%d", IntroSeconds),
		"-vn",
		"-ar", "16000",
		"-ac", "1",
		"-f", "wav",
		tmpWAV,
	}
	ffCmd := exec.CommandContext(ctx, "ffmpeg", ffmpegArgs...)
	if out, err := ffCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ffmpeg extract: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	// Try local whisper.cpp binary first — zero network, fastest.
	for _, bin := range []string{"whisper-cpp", "whisper", "main"} {
		if path, err := exec.LookPath(bin); err == nil {
			text, err := runWhisperCPP(ctx, path, tmpWAV)
			if err == nil {
				return text, nil
			}
		}
	}

	// Fall back to OpenAI Whisper API.
	apiKey := config.AppConfig.OpenAIAPIKey
	if apiKey == "" {
		return "", fmt.Errorf("transcribe: no local whisper binary found and openai_api_key is not configured")
	}
	return runOpenAIWhisper(ctx, apiKey, tmpWAV)
}

// runWhisperCPP calls the whisper.cpp CLI. Output format: each line is either
// a timestamp "[HH:MM:SS.mmm --> HH:MM:SS.mmm]" or a text segment. We strip
// timestamps and join the text.
func runWhisperCPP(ctx context.Context, binPath, wavPath string) (string, error) {
	// -m: model file path — whisper.cpp requires a .bin model file. We look
	// for common installation locations. If none found we skip this backend.
	modelPath := findWhisperModel()
	if modelPath == "" {
		return "", fmt.Errorf("whisper-cpp: no model file found")
	}

	out, err := exec.CommandContext(ctx, binPath,
		"-m", modelPath,
		"-f", wavPath,
		"-otxt",
		"--no-timestamps",
		"--language", "en",
	).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("whisper-cpp: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	// whisper.cpp with -otxt writes result to wavPath+".txt"
	txtPath := wavPath + ".txt"
	defer os.Remove(txtPath)
	data, err := os.ReadFile(txtPath)
	if err != nil {
		// Some versions print to stdout instead.
		return strings.TrimSpace(string(out)), nil
	}
	return strings.TrimSpace(string(data)), nil
}

// findWhisperModel looks for a whisper model in common install locations,
// including the snap's restricted data directory (~/snap/whisper-cpp/common/).
func findWhisperModel() string {
	home := os.ExpandEnv("$HOME")
	candidates := []string{
		// snap install (strict confinement — model must live under snap data dir)
		home + "/snap/whisper-cpp/common/models/ggml-base.en.bin",
		home + "/snap/whisper-cpp/current/models/ggml-base.en.bin",
		// apt / manual install paths
		"/usr/share/whisper.cpp/models/ggml-base.en.bin",
		"/usr/local/share/whisper.cpp/models/ggml-base.en.bin",
		"/opt/whisper/models/ggml-base.en.bin",
		"/var/lib/whisper/ggml-base.en.bin",
		home + "/.local/share/whisper/ggml-base.en.bin",
		home + "/whisper.cpp/models/ggml-base.en.bin",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// runOpenAIWhisper submits the WAV to OpenAI's Whisper API (whisper-1 model).
func runOpenAIWhisper(ctx context.Context, apiKey, wavPath string) (string, error) {
	f, err := os.Open(wavPath)
	if err != nil {
		return "", fmt.Errorf("open wav: %w", err)
	}
	defer f.Close()

	client := openai.NewClient(option.WithAPIKey(apiKey))
	resp, err := client.Audio.Transcriptions.New(ctx, openai.AudioTranscriptionNewParams{
		File:     f,
		Model:    openai.AudioModelWhisper1,
		Language: param.NewOpt("en"),
	})
	if err != nil {
		return "", fmt.Errorf("openai whisper: %w", err)
	}
	return strings.TrimSpace(resp.Text), nil
}
