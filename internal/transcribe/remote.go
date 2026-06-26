// file: internal/transcribe/remote.go
// version: 1.0.0
// guid: f7a8b9c0-d1e2-3f4a-5b6c-7d8e9f0a1b2c
// last-edited: 2026-06-26

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
)

// remoteWorkers is the number of concurrent HTTP requests to the remote
// whisper server. Two lets us pipeline network transfer with GPU compute
// (one file uploading while the previous is being transcribed).
const remoteWorkers = 2

// transcribeRemote sends WAV jobs to a remote faster-whisper HTTP server.
// If the server is unreachable or any request fails, returns an error so
// the caller can fall back to local transcription.
func transcribeRemote(ctx context.Context, remoteURL string, jobs map[string]string) (map[string]BatchResult, error) {
	client := &http.Client{Timeout: 120 * time.Second}

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
				r, err := transcribeOneRemote(ctx, client, remoteURL, j.wavPath)
				resultCh <- resultItem{id: j.id, result: r, err: err}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	results := make(map[string]BatchResult, len(jobs))
	for item := range resultCh {
		if item.err != nil {
			return nil, fmt.Errorf("remote transcribe %s: %w", item.id, item.err)
		}
		results[item.id] = item.result
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
