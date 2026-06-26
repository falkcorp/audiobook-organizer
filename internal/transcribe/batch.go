// file: internal/transcribe/batch.go
// version: 1.9.0
// guid: d4e5f6a7-b8c9-0123-defa-234567890123
// last-edited: 2026-06-26

package transcribe

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
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

// TranscribeBatch transcribes multiple WAV files. jobs maps an opaque key to a WAV path.
//
// If WHISPER_REMOTE_URL is set (e.g. "http://192.168.1.x:8000"), jobs are sent
// to the remote faster-whisper server running scripts/whisper_server.py. On any
// failure the warning is logged and the function falls back to the local
// uv/openai-whisper path.
//
// Local path uses torch==2.0.1+cu118 for CC 6.1 GPU support (GTX 1050 Ti).
func TranscribeBatch(ctx context.Context, jobs map[string]string) (map[string]BatchResult, error) {
	if len(jobs) == 0 {
		return nil, nil
	}

	if remoteURL := os.Getenv("WHISPER_REMOTE_URL"); remoteURL != "" {
		results, err := transcribeRemote(ctx, remoteURL, jobs)
		if err != nil {
			slog.Warn("transcribe: remote whisper failed, falling back to local",
				"url", remoteURL, "err", err)
			// fall through to local uv path
		} else {
			return results, nil
		}
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

	uvBin := resolveUVBin()
	cuda := detectCUDA()

	// Build uv args dynamically from CUDA probe results.
	// --index-strategy unsafe-best-match: required when the PyTorch wheel index
	// also serves a higher version of a pinned dep (e.g. setuptools>=70 on the
	// cu118 index would block setuptools<67 under default first-match strategy).
	uvArgs := []string{
		"run",
		"--python", cuda.PythonVersion,
		"--index-strategy", "unsafe-best-match",
		"--with", "openai-whisper",
		"--with", cuda.TorchPkg,
	}
	for _, dep := range cuda.ExtraDeps {
		uvArgs = append(uvArgs, "--with", dep)
	}
	uvArgs = append(uvArgs, "python", scriptFile.Name(), "base.en", jobsFile.Name())

	cmd := exec.CommandContext(ctx, uvBin, uvArgs...)

	home := os.Getenv("HOME")
	if home == "" {
		home = "/tmp"
	}
	env := append(os.Environ(),
		"UV_CACHE_DIR="+home+"/.uv-cache",
		"UV_PYTHON_INSTALL_DIR="+home+"/.uv-python",
	)
	if cuda.ExtraIndexURL != "" {
		env = append(env, "UV_EXTRA_INDEX_URL="+cuda.ExtraIndexURL)
	}
	cmd.Env = env
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

// resolveUVBin returns the path to a uv binary that is NOT routed through
// snap-confine. Snap packages require cap_dac_override which systemd services
// typically drop. We prefer a non-snap install (e.g. installed via the
// official uv installer to ~/.local/bin/uv) over the snap at /snap/bin/uv.
func resolveUVBin() string {
	// /home/jdfalk/.local has 700 perms — world-inaccessible to the audiobook
	// service user. /home/jdfalk/uv is a copy with 755, world-executable.
	// /usr/local/bin/uv would be ideal (requires root to install there).
	candidates := []string{
		"/home/jdfalk/uv",
		"/usr/local/bin/uv",
		"/opt/uv/bin/uv",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// Fall back to PATH lookup; may be the snap version, but is better than
	// nothing (works fine for non-systemd invocations like dev/test).
	if p, err := exec.LookPath("uv"); err == nil {
		return p
	}
	return "uv"
}
