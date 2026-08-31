// file: internal/transcribe/remote.go
// version: 2.6.0
// guid: f7a8b9c0-d1e2-3f4a-5b6c-7d8e9f0a1b2c
// last-edited: 2026-08-31

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
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// wavJob pairs a book ID with its local WAV path for a transcription request.
type wavJob struct {
	id   string
	path string
}

// whisperBatchSize is the number of WAV files sent per /transcribe-batch request.
// Each file is ~2.9 MB (90s × 16kHz × 16-bit mono), so 16 files ≈ 46 MB per request —
// well within memory limits on both ends. Smaller batches give more frequent
// onProgress ticks; larger ones reduce HTTP overhead further.
// 16 is chosen so that even at ~15s/file on large-v2 the batch finishes in ~240s,
// safely below the 600s HTTP client timeout (was 32 files → 480s > 300s → deadline exceeded).
const whisperBatchSize = 16

// remoteWorkers is only used on the legacy single-file path (/transcribe).
// The batch path (/transcribe-batch) sends all files in one request so no
// concurrency is needed at the Go layer.
const remoteWorkers = 4

// remoteHealth is the decoded /health body. One probe answers two independent
// questions -- does this server batch, and is it on a GPU -- deliberately:
// asking twice would let a server restart between the calls and hand back a
// batch answer and a device answer describing different processes.
type remoteHealth struct {
	// BatchPipeline is a pointer because its ABSENCE is the signal: a
	// pre-1.0.0 server omits the key entirely. Present (even false) means a
	// v2 server. See supportsBatch.
	BatchPipeline *bool `json:"batch_pipeline"`
	// Device is the RESOLVED inference device the server loaded ("cuda",
	// "metal", "cpu", ...), not the one it was configured with. Empty when
	// the server is too old to report it, which requireGPU treats as
	// unproven rather than as CPU.
	Device string `json:"device"`
	// ComputeType is informational: it is logged with a refusal so "cpu"
	// plus "int8" reads as a coherent story rather than a bare rejection.
	ComputeType string `json:"compute_type"`
}

// supportsBatch reports whether the server exposes /transcribe-batch.
// Present in v2 server; absent means old server.
func (h remoteHealth) supportsBatch() bool { return h.BatchPipeline != nil }

// probeRemoteHealth performs the single /health GET. ok=false means the probe
// itself did not complete (connection error, non-JSON body); callers must not
// read the returned struct in that case. A failed probe is NOT an error
// return: the batch decision degrades to the per-file path on failure, which
// is the historical behaviour and must be preserved.
func probeRemoteHealth(ctx context.Context, remoteURL string) (remoteHealth, bool) {
	hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var h remoteHealth
	req, err := http.NewRequestWithContext(hctx, http.MethodGet, remoteURL+"/health", nil)
	if err != nil {
		return h, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return h, false
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return h, false
	}
	return h, true
}

// supportsRemoteBatch checks whether the server exposes /transcribe-batch by
// hitting /health and looking for "batch_pipeline" in the response. A plain
// connection error is treated as unsupported (server may be older version).
func supportsRemoteBatch(ctx context.Context, remoteURL string) bool {
	h, ok := probeRemoteHealth(ctx, remoteURL)
	return ok && h.supportsBatch()
}

// transcribeRemote sends WAV jobs to the remote faster-whisper server.
// It probes for /transcribe-batch support first (faster-whisper >=1.0.0 server)
// and falls back to the original per-file worker pool on 404 or probe failure.
func transcribeRemote(ctx context.Context, remoteURL string, jobs map[string]string, onProgress ProgressFunc) (map[string]BatchResult, error) {
	h, ok := probeRemoteHealth(ctx, remoteURL)
	return transcribeRemoteWithHealth(ctx, remoteURL, h, ok, jobs, onProgress)
}

// transcribeRemoteWithHealth is transcribeRemote for a caller that has ALREADY
// probed /health -- the pool gate does, to decide require_gpu -- so the batch
// decision reuses that same response instead of issuing a second one.
func transcribeRemoteWithHealth(ctx context.Context, remoteURL string, h remoteHealth, probed bool, jobs map[string]string, onProgress ProgressFunc) (map[string]BatchResult, error) {
	if probed && h.supportsBatch() {
		return transcribeRemoteBatched(ctx, remoteURL, jobs, onProgress)
	}
	return transcribeRemotePerFile(ctx, remoteURL, jobs, onProgress)
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
		// Each sub-batch of 16 files at ~15s/file on large-v2 ≈ 240s.
		// 600s gives 2.5× headroom for slow GPU or large clips.
		Timeout: 600 * time.Second,
	}
	results := make(map[string]BatchResult, len(jobs))
	total := len(jobs)
	done := 0

	// WhisperBatchSleepMS: milliseconds to pause between sub-batches so the
	// GPU can shed heat. Defaults to 8000ms (8s). Set to 0 to disable.
	batchSleep := time.Duration(config.AppConfig.WhisperBatchSleepMS) * time.Millisecond

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
		// Open the file BEFORE CreateFormFile — if we call CreateFormFile first
		// and then skip on Open failure, the multipart writer has already written
		// the part header with zero bytes of content. The server receives an empty
		// part and faster-whisper reports "Invalid data found when processing
		// input: '<none>'". Opening first ensures we only add parts we can fill.
		f, err := os.Open(e.path)
		if err != nil {
			// Missing WAV — skip this file rather than aborting the whole batch.
			continue
		}
		fw, err := mw.CreateFormFile("files", e.id)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("create form file %s: %w", e.id, err)
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
		wg.Go(func() {
			for j := range jobCh {
				r, err := transcribeOneRemote(batchCtx, client, remoteURL, j.wavPath)
				resultCh <- resultItem{id: j.id, result: r, err: err}
				if err != nil {
					cancel()
					return
				}
			}
		})
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
