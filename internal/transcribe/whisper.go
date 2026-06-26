// file: internal/transcribe/whisper.go
// version: 1.2.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-06-26

// Package transcribe extracts and transcribes the opening seconds of an audio
// file. It tries backends in preference order:
//
//  1. uv run --with openai-whisper whisper  (requires uv on PATH)
//     No venv or install step — uv downloads and caches openai-whisper +
//     torch on first use, then reuses the cache. Models auto-downloaded to
//     ~/.cache/whisper/ on first whisper run.
//     Install uv: curl -LsSf https://astral.sh/uv/install.sh | sh
//  2. OpenAI Whisper API (whisper-1 — requires openai_api_key in config)
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
	IntroSeconds = 30
)

// TranscribeFirst30s extracts the first IntroSeconds of the given audio file
// and returns the transcribed text. The text is not parsed — call
// ParseAudiobookIntro to extract structured title/author/narrator fields.
func TranscribeFirst30s(ctx context.Context, audioPath string) (string, error) {
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

	// Try official OpenAI Whisper via uv run — no install step, uv caches
	// openai-whisper + torch on first use and reuses the cache thereafter.
	if _, err := exec.LookPath("uv"); err == nil {
		if text, err := runPythonWhisper(ctx, tmpWAV); err == nil {
			return text, nil
		}
	}

	// Fall back to OpenAI Whisper API.
	apiKey := config.AppConfig.OpenAIAPIKey
	if apiKey == "" {
		return "", fmt.Errorf("transcribe: uv not found on PATH and openai_api_key is not configured")
	}
	return runOpenAIWhisper(ctx, apiKey, tmpWAV)
}

// runPythonWhisper calls openai-whisper via `uv run --with openai-whisper whisper`.
// uv downloads and caches the package + torch on first use; subsequent calls
// are instant. Models auto-download to ~/.cache/whisper/ on first whisper run.
// The CLI writes a .txt file to outDir; we read and remove it.
func runPythonWhisper(ctx context.Context, wavPath string) (string, error) {
	outDir := filepath.Dir(wavPath)
	out, err := exec.CommandContext(ctx, "uv", "run",
		"--with", "openai-whisper",
		"whisper",
		wavPath,
		"--model", "base.en",
		"--output_format", "txt",
		"--output_dir", outDir,
		"--language", "en",
		"--task", "transcribe",
	).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("python whisper: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	// Output file is {stem}.txt in outDir.
	stem := strings.TrimSuffix(filepath.Base(wavPath), filepath.Ext(wavPath))
	txtPath := filepath.Join(outDir, stem+".txt")
	defer os.Remove(txtPath)

	data, err := os.ReadFile(txtPath)
	if err != nil {
		return "", fmt.Errorf("python whisper: read output: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
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
