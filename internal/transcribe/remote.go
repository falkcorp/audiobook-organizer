// file: internal/transcribe/remote.go
// version: 2.1.0
// guid: f7a8b9c0-d1e2-3f4a-5b6c-7d8e9f0a1b2c
// last-edited: 2026-06-27

package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// wavJob pairs a book ID with its local WAV path for a transcription request.
type wavJob struct {
	id   string
	path string
}

// whisperBatchSize is the number of WAV files sent per /transcribe-batch request.
// Each file is ~2.9 MB (90s × 16kHz × 16-bit mono), so 32 files ≈ 93 MB per request —
// well within memory limits on both ends. Smaller batches give more frequent
// onProgress ticks; larger ones reduce HTTP overhead further.
const whisperBatchSize = 32

// remoteWorkers is only used on the legacy single-file path (/transcribe).
// The batch path (/transcribe-batch) sends all files in one request so no
// concurrency is needed at the Go layer.
const remoteWorkers = 4

// transcribeRemote sends WAV jobs to the remote faster-whisper server.
// It probes for /transcribe-batch support first (faster-whisper >=1.0.0 server)
// and falls back to the original per-file worker pool on 404 or probe failure.
func transcribeRemote(ctx context.Context, remoteURL string, jobs map[string]string, onProgress ProgressFunc) (map[string]BatchResult, error) {
	if supportsRemoteBatch(ctx, remoteURL) {
		return transcribeRemoteBatched(ctx, remoteURL, jobs, onProgress)
	}
	return transcribeRemotePerFile(ctx, remoteURL, jobs, onProgress)
}

// supportsRemoteBatch checks whether the server exposes /transcribe-batch by
// hitting /health and looking for "batch_pipeline" in the response. A plain
// connection error is treated as unsupported (server may be older version).
func supportsRemoteBatch(ctx context.Context, remoteURL string) bool {
	hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(hctx, http.MethodGet, remoteURL+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var h struct {
		BatchPipeline *bool `json:"batch_pipeline"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return false
	}
	// Present in v2 server; absent or false means old server.
	return h.BatchPipeline != nil
}

// transcribeRemoteBatched sends jobs in sub-batches of whisperBatchSize to
// /transcribe-batch. Processing is sequential inside each sub-batch (the GPU
// handles one file at a time), but reduced HTTP round-trips and
// BatchedInferencePipeline on the server give 2-3x throughput vs per-file mode.
func transcribeRemoteBatched(ctx context.Context, remoteURL string, jobs map[string]string, onProgress ProgressFunc) (map[string]BatchResult, error) {
	// Stable order so progress reporting is deterministic.
	ordered := make([]wavJob, 0, len(jobs))
	for id, path := range jobs {
		ordered = append(ordered, wavJob{id, path})
	}

	client := &http.Client{
		// Each sub-batch of 32 files takes up to ~whisperBatchSize × 3s ≈ 96s.
		// Give generous headroom for slow GPU or network hiccups.
		Timeout: 300 * time.Second,
	}
	results := make(map[string]BatchResult, len(jobs))
	total := len(jobs)
	done := 0

	// WHISPER_BATCH_SLEEP_MS: milliseconds to pause between sub-batches so the
	// GPU can shed heat. Defaults to 8000ms (8s). Set to 0 to disable.
	batchSleepMs := 8000
	if v := os.Getenv("WHISPER_BATCH_SLEEP_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			batchSleepMs = n
		}
	}
	batchSleep := time.Duration(batchSleepMs) * time.Millisecond

	for start := 0; start < len(ordered); start += whisperBatchSize {
		end := start + whisperBatchSize
		if end > len(ordered) {
			end = len(ordered)
		}
		chunk := ordered[start:end]

		batchResults, err := sendBatch(ctx, client, remoteURL, chunk)
		if err != nil {
			return nil, fmt.Errorf("transcribe-batch chunk %d-%d: %w", start, end, err)
		}
		for id, r := range batchResults {
			results[id] = r
			done++
			if onProgress != nil {
				onProgress(done, total)
			}
		}

		// Sleep between batches (not after the last one) to let the GPU cool.
		if batchSleep > 0 && end < len(ordered) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(batchSleep):
			}
		}
	}
	return results, nil
}

// sendBatch sends one multipart request containing len(chunk) WAV files.
// The filename of each part is the book ID so the server echoes it back as
// the result key.
func sendBatch(ctx context.Context, client *http.Client, remoteURL string, chunk []wavJob) (map[string]BatchResult, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	for _, e := range chunk {
		fw, err := mw.CreateFormFile("files", e.id)
		if err != nil {
			return nil, fmt.Errorf("create form file %s: %w", e.id, err)
		}
		f, err := os.Open(e.path)
		if err != nil {
			// Missing WAV — skip this file rather than aborting the whole batch.
			continue
		}
		if _, err := io.Copy(fw, f); err != nil {
			f.Close()
			return nil, fmt.Errorf("copy wav %s: %w", e.id, err)
		}
		f.Close()
	}
	mw.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, remoteURL+"/transcribe-batch", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned HTTP %d", resp.StatusCode)
	}

	var out struct {
		Results map[string]struct {
			Text  string  `json:"text"`
			Error *string `json:"error"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode batch response: %w", err)
	}

	ret := make(map[string]BatchResult, len(out.Results))
	for id, v := range out.Results {
		r := BatchResult{Text: v.Text}
		if v.Error != nil {
			r.Error = *v.Error
		}
		ret[id] = r
	}
	return ret, nil
}

// transcribeRemotePerFile is the original per-file worker-pool path, kept as
// fallback for servers that don't expose /transcribe-batch.
func transcribeRemotePerFile(ctx context.Context, remoteURL string, jobs map[string]string, onProgress ProgressFunc) (map[string]BatchResult, error) {
	client := &http.Client{Timeout: 120 * time.Second}

	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type jobItem struct {
		id      string
		wavPath string
	}
	type resultItem struct {
		id     string
		result BatchResult
		err    error
	}

	jobCh := make(chan jobItem, len(jobs))
	for id, path := range jobs {
		jobCh <- jobItem{id, path}
	}
	close(jobCh)

	resultCh := make(chan resultItem, len(jobs))
	var wg sync.WaitGroup
	for range remoteWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobCh {
				r, err := transcribeOneRemote(batchCtx, client, remoteURL, j.wavPath)
				resultCh <- resultItem{id: j.id, result: r, err: err}
				if err != nil {
					cancel()
					return
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	results := make(map[string]BatchResult, len(jobs))
	total := len(jobs)
	for item := range resultCh {
		if item.err != nil {
			return nil, fmt.Errorf("remote transcribe %s: %w", item.id, item.err)
		}
		results[item.id] = item.result
		if onProgress != nil {
			onProgress(len(results), total)
		}
	}
	return results, nil
}

func transcribeOneRemote(ctx context.Context, client *http.Client, remoteURL, wavPath string) (BatchResult, error) {
	f, err := os.Open(wavPath)
	if err != nil {
		return BatchResult{}, fmt.Errorf("open wav: %w", err)
	}
	defer f.Close()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", filepath.Base(wavPath))
	if err != nil {
		return BatchResult{}, err
	}
	if _, err := io.Copy(fw, f); err != nil {
		return BatchResult{}, err
	}
	mw.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, remoteURL+"/transcribe", &body)
	if err != nil {
		return BatchResult{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := client.Do(req)
	if err != nil {
		return BatchResult{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return BatchResult{}, fmt.Errorf("remote returned HTTP %d", resp.StatusCode)
	}

	var out struct {
		Text  string  `json:"text"`
		Error *string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return BatchResult{}, fmt.Errorf("decode response: %w", err)
	}
	if out.Error != nil && *out.Error != "" {
		return BatchResult{Error: *out.Error}, nil
	}
	return BatchResult{Text: out.Text}, nil
}
