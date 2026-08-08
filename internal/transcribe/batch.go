// file: internal/transcribe/batch.go
// version: 1.13.0
// guid: d4e5f6a7-b8c9-0123-defa-234567890123
// last-edited: 2026-08-07

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

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

//go:embed batch_whisper.py
var batchWhisperPy []byte

// BatchResult holds the transcript for one book.
type BatchResult struct {
	Text  string
	Error string // non-empty means transcription failed for this item
}

// ProgressFunc is an optional callback invoked after each book finishes
// transcribing on the remote path. done is the number completed so far, total
// the batch size. Callers use it to emit per-book progress — critical so the
// operation watchdog sees liveness during a multi-minute batch instead of
// cancelling it for "no progress". May be nil.
type ProgressFunc func(done, total int)

// TranscribeBatch transcribes multiple WAV files. jobs maps an opaque key to a WAV path.
//
// If WHISPER_REMOTE_URL is set (e.g. "http://192.168.1.x:8000"), jobs are sent
// to the remote faster-whisper server running scripts/whisper_server.py. On any
// failure the warning is logged and the function falls back to the local
// uv/openai-whisper path. onProgress (if non-nil) fires per book on the remote
// path; the local single-subprocess path reports only on completion.
//
// Local path uses torch==2.0.1+cu118 for CC 6.1 GPU support (GTX 1050 Ti).
func TranscribeBatch(ctx context.Context, jobs map[string]string, onProgress ProgressFunc) (map[string]BatchResult, error) {
	if len(jobs) == 0 {
		return nil, nil
	}

	// Remote precedence: non-empty whisper_endpoints → multi-endpoint pool;
	// else whisper_remote_url as a one-element pool (functionally identical to
	// the historical direct path); else the local uv path below.
	snap := config.Snapshot()
	if endpoints := poolEndpoints(snap.WhisperEndpoints, snap.WhisperRemoteURL); len(endpoints) > 0 {
		results, err := transcribePool(ctx, endpoints, jobs, onProgress)
		if err != nil {
			// Do NOT fall back to the local uv path when remote endpoints are
			// configured. The local subprocess loads the full Whisper model into
			// RAM; at batch sizes of 100–200 books this reliably OOMs the server
			// (signal: killed after 40+ minutes) and produces zero results anyway.
			// Returning the error lets the caller (processTranscribePage) log a
			// warning, skip the page, and advance to the next one — far better
			// than stalling for an hour and triggering the watchdog.
			//
			// 🔴 transcribePool returns a *TransportError (via classifyTransport)
			// ONLY when every endpoint is exhausted — a BATCH-level failure
			// carrying no per-file meaning, so the caller defers the page instead
			// of writing whisper_error to every book in it. Returning a bare
			// error here is what turned the 2026-07-01 outage into ~34,000 false
			// per-book verdicts.
			return nil, err
		}
		return results, nil
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
	// The local subprocess returns all results at once, so we can only report
	// completion (not per-book). The op def's generous ProgressTimeout covers
	// the silent gap during a local-fallback batch.
	if onProgress != nil {
		onProgress(len(results), len(jobs))
	}
	return results, nil
}

// poolEndpoints converts config-layer endpoint declarations into transcribe
// Endpoints, applying the precedence documented at the call site: a non-empty
// whisper_endpoints list wins; otherwise a non-empty whisper_remote_url
// becomes a one-element pool; otherwise nil (caller uses the local path).
// The conversion lives here so internal/config never imports
// internal/transcribe.
func poolEndpoints(cfgEndpoints []config.WhisperEndpoint, singleURL string) []Endpoint {
	if len(cfgEndpoints) > 0 {
		endpoints := make([]Endpoint, 0, len(cfgEndpoints))
		for _, e := range cfgEndpoints {
			if e.URL == "" {
				continue
			}
			endpoints = append(endpoints, Endpoint{
				URL:         e.URL,
				Concurrency: e.Concurrency,
				Label:       e.Label,
				Priority:    e.Priority,
				Kind:        e.Kind,
			})
		}
		if len(endpoints) > 0 {
			return endpoints
		}
	}
	if singleURL != "" {
		return []Endpoint{{URL: singleURL, Concurrency: 1}}
	}
	return nil
}

// resolveUVBin returns the path to a uv binary that is NOT routed through
// snap-confine. Snap packages require cap_dac_override which systemd services
// typically drop. We prefer a non-snap install (e.g. installed via the
// official uv installer to ~/.local/bin/uv) over the snap at /snap/bin/uv.
func resolveUVBin() string {
	// /home/<user>/.local has 700 perms — world-inaccessible to the audiobook
	// service user. /home/<user>/uv is a copy with 755, world-executable.
	// /usr/local/bin/uv would be ideal (requires root to install there).
	candidates := []string{
		"/home/<user>/uv",
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
