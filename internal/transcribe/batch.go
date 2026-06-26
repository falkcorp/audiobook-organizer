// file: internal/transcribe/batch.go
// version: 1.3.0
// guid: d4e5f6a7-b8c9-0123-defa-234567890123
// last-edited: 2026-06-26

package transcribe

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

//go:embed batch_whisper.py
var batchWhisperPy []byte

// BatchResult holds the transcript for one book.
type BatchResult struct {
	Text  string
	Error string // non-empty means transcription failed for this item
}

// TranscribeBatch transcribes multiple WAV files in a single Python process,
// loading the Whisper model only once. jobs maps an opaque key to a WAV path.
//
// Uses torch==2.0.1+cu118 so older NVIDIA cards (CC 6.1, e.g. GTX 1050 Ti)
// get GPU acceleration; the Python script falls back to CPU automatically when
// CUDA is absent or incompatible.
//
// Requires uv on PATH.
func TranscribeBatch(ctx context.Context, jobs map[string]string) (map[string]BatchResult, error) {
	if len(jobs) == 0 {
		return nil, nil
	}

	// Write jobs JSON to temp file.
	jobsFile, err := os.CreateTemp("", "ao-batch-jobs-*.json")
	if err != nil {
		return nil, fmt.Errorf("transcribe batch: create jobs file: %w", err)
	}
	defer os.Remove(jobsFile.Name())
	if err := json.NewEncoder(jobsFile).Encode(jobs); err != nil {
		jobsFile.Close()
		return nil, fmt.Errorf("transcribe batch: encode jobs: %w", err)
	}
	jobsFile.Close()

	// Write the embedded Python script to a temp file.
	scriptFile, err := os.CreateTemp("", "ao-batch-whisper-*.py")
	if err != nil {
		return nil, fmt.Errorf("transcribe batch: create script: %w", err)
	}
	defer os.Remove(scriptFile.Name())
	if _, err := scriptFile.Write(batchWhisperPy); err != nil {
		scriptFile.Close()
		return nil, fmt.Errorf("transcribe batch: write script: %w", err)
	}
	scriptFile.Close()

	// --python 3.11 is required: torch==2.0.1 has no wheels for python 3.12+.
	// cu118 build supports CC 6.1 (GTX 1050 Ti); newer torch dropped sm_61.
	cmd := exec.CommandContext(ctx, "uv", "run",
		"--python", "3.11",
		"--with", "openai-whisper",
		"--with", "torch==2.0.1+cu118",
		"python", scriptFile.Name(), "base.en", jobsFile.Name(),
	)
	// PyTorch wheel index for cu118 — supports CC 6.1 (Pascal) that newer
	// torch builds dropped. The driver version check is forward-compatible
	// with CUDA 12.x drivers.
	// UV_CACHE_DIR: the audiobook service runs with HOME=/var/lib/audiobook-organizer
	// where ~/.cache/uv is not writable. /tmp is always writable.
	cmd.Env = append(os.Environ(),
		"UV_EXTRA_INDEX_URL=https://download.pytorch.org/whl/cu118",
		"UV_CACHE_DIR=/tmp/ao-uv-cache",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if len(errMsg) > 500 {
			errMsg = errMsg[len(errMsg)-500:] // keep tail (most relevant)
		}
		return nil, fmt.Errorf("transcribe batch: uv run: %w: %s", err, errMsg)
	}

	var raw map[string]struct {
		Text  string  `json:"text"`
		Error *string `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		return nil, fmt.Errorf("transcribe batch: parse output: %w", err)
	}

	results := make(map[string]BatchResult, len(raw))
	for k, v := range raw {
		r := BatchResult{Text: v.Text}
		if v.Error != nil {
			r.Error = *v.Error
		}
		results[k] = r
	}
	return results, nil
}
