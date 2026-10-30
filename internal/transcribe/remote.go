// file: internal/transcribe/remote.go
// version: 2.17.2
// guid: f7a8b9c0-d1e2-3f4a-5b6c-7d8e9f0a1b2c
// last-edited: 2026-10-30

package transcribe

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
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
const defaultWhisperBatchSize = 16

// whisperBatchSize reads the configured sub-batch size, falling back to
// defaultWhisperBatchSize. It is a function rather than a const because the
// right value is a property of the SERVER, not of this code: a worker that
// serialises inference behind one lock wants SMALL batches so work spreads
// across the pool instead of queueing at one box, while a server doing real
// batched inference wants large ones. An operator must be able to tune that
// without a rebuild.
func whisperBatchSize() int {
	if n := config.AppConfig.WhisperBatchSize; n > 0 {
		return n
	}
	return defaultWhisperBatchSize
}

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
	// Model is the model the server loaded (both bundled servers report it).
	// It is part of the result-journal key -- see newEndpointJournal; empty
	// means the endpoint's results are not journalled.
	Model string `json:"model"`
	// Backend ("mlx", ...) qualifies Model in the journal key; optional.
	Backend string `json:"backend"`
	// DecodeFingerprint is a short hash of the server's output-affecting
	// decode settings; part of the journal key when present.
	DecodeFingerprint string `json:"decode_fingerprint"`
	// PathPipeline is true when the worker serves /transcribe-path-batch: a
	// valid WHISPER_PATH_MAP is configured on it. Absent (old worker) is false.
	// It is the CAPABILITY; whether it can work right now is PathRoots.
	PathPipeline bool `json:"path_pipeline"`
	// PathRoots is the live readability of each mapped root (prefix is the
	// SERVER-side path). A lost NFS mount shows up here as readable=false.
	PathRoots []pathRoot `json:"path_roots"`
	// ExtractFingerprint hashes the worker's clip-extraction arguments; it is
	// part of path-job journal keys only. See endpointJournal.pathKey.
	ExtractFingerprint string `json:"extract_fingerprint"`
	// BusySeconds is how long the worker's current inference has been running
	// (0 when idle). BusyLimitSeconds is that inference's own deadline (null
	// when idle). Pointers because ABSENCE is the signal: a worker older than
	// whisper_mlx_server.py 1.8.0 omits busy_seconds, and no stuck judgement
	// is made for it (an idle current worker reports 0, not nothing).
	BusySeconds      *float64 `json:"busy_seconds"`
	BusyLimitSeconds *float64 `json:"busy_limit_seconds"`
	// ClipTimeoutFloorS and ClipTimeoutFactor are the worker's per-clip
	// deadline formula, max(floor, factor x audio seconds); see requestTimeout.
	ClipTimeoutFloorS float64 `json:"clip_timeout_floor_s"`
	ClipTimeoutFactor float64 `json:"clip_timeout_factor"`
	// ChildReadyTimeoutS is how long the worker waits for a freshly started
	// inference process to load the model, and ExtractTimeoutS how long it gives
	// ffmpeg to cut a path job's clip. Both elapse BEFORE a clip's own clock
	// starts, so neither is inside max(floor, factor x seconds); 0 means the
	// worker predates the field (see requestTimeout).
	ChildReadyTimeoutS float64 `json:"child_ready_timeout_s"`
	ExtractTimeoutS    float64 `json:"extract_timeout_s"`
	// ChildStartError is why the worker's decoder process could not start, when
	// it failed to within the worker's fault window; absent otherwise. A worker
	// reporting it answers every clip worker_misconfigured, after waiting out the
	// child-ready timeout, so the gate refuses it (see childFaultRefusal).
	ChildStartError string `json:"child_start_error"`
	// MachineID is the worker's opaque machine identity (whisper_mlx_server.py
	// 1.12.0+): every process on one machine reports the same value, so two
	// ports on one box are one worker. Empty for an older worker, and for a
	// newer one that has no stable machine id yet (whisper_mlx_server.py
	// 1.14.0 never reports a hostname-derived one); see gatedEndpoint.workerID.
	MachineID string `json:"machine_id"`
}

// defaultRequestTimeout is the ceiling on one batch request when the worker
// does not report its per-clip deadline (an old worker, or a failed probe). A
// variable only so tests can shorten it; production never writes it.
var defaultRequestTimeout = 600 * time.Second

// requestSlack is added to a worker-reported ceiling so the worker's own
// per-clip verdict always arrives before the client gives up on the request.
const requestSlack = 60 * time.Second

// Allowances for a worker that reports its clip deadline but not the two waits
// that precede a clip (whisper_mlx_server.py 1.8.x): its own defaults, so the
// ceiling is never SHORTER than the worker's real worst case for these.
const (
	defaultChildReadyAllowance = 300 * time.Second
	defaultExtractAllowance    = 120 * time.Second
)

// requestTimeout is the HTTP ceiling for one batch request holding clips of the
// given lengths; extract is true for a path request, where the worker also cuts
// each clip with ffmpeg inside the request.
//
// A worker that reports its deadline formula bounds every clip by
// max(floor, factor x seconds) and answers a clip that blows it with a per-file
// transcribe_timeout. The ceiling is the sum of those limits plus slack, plus
// ONE allowance for each wait that is outside the clip clock and happens once
// per request on a healthy worker: loading the model into a cold child, and (for
// a path request) ffmpeg. There is no fixed 600 s floor either: the ceiling is
// built from the worker's own numbers, and for a short request it can come out
// LONGER than the old 600 s (a one-clip 90 s upload at the default 60 s floor /
// 4x factor is 360 + 60 + 300 = 720 s, and a path request adds 120 s more).
//
// This is a BACKSTOP, not a guarantee, and the sum is not a proof the request
// ends: it does not count queueing on the worker's inference lock behind another
// request, a model reload after EVERY timed-out clip (each can take up to the
// child-ready timeout), or ffmpeg runs that outlast the pool they share. What
// catches a worker that has stopped answering is superviseRequest, which polls
// /health during the request and does not wait for this ceiling. A request that
// does reach it is read as stuck, and the dispatcher splits it and probes the
// worker again, so an overrun by a healthy but busy worker costs a retry, not a
// verdict.
//
// A worker that reports no formula keeps the historical ceiling.
func requestTimeout(h remoteHealth, clipSeconds []float64, extract bool) time.Duration {
	if h.ClipTimeoutFloorS <= 0 && h.ClipTimeoutFactor <= 0 {
		return defaultRequestTimeout
	}
	var worst float64
	for _, secs := range clipSeconds {
		worst += max(h.ClipTimeoutFloorS, h.ClipTimeoutFactor*secs)
	}
	total := time.Duration(worst*float64(time.Second)) + requestSlack
	total += cmp.Or(time.Duration(h.ChildReadyTimeoutS*float64(time.Second)), defaultChildReadyAllowance)
	if extract {
		total += cmp.Or(time.Duration(h.ExtractTimeoutS*float64(time.Second)), defaultExtractAllowance)
	}
	return total
}

// requestDeadlineHeader tells the worker how long this client will wait for the
// request, in whole seconds. A worker honouring it starts no clip after that
// moment and does not keep decoding a request nobody is listening to while
// holding its one inference lock.
const requestDeadlineHeader = "X-Whisper-Request-Timeout-S"

// setRequestDeadlineHeader sends the client's ceiling ROUNDED UP to a whole
// second. The worker counts its deadline from when its handler starts, which is
// after this client started its clock (the upload, or at least the connection,
// comes first), so with the value rounded up the worker's deadline can never be
// EARLIER than the client's. Truncating it (an upload ceiling is fractional:
// clip seconds are estimated from file size) put the worker's deadline up to a
// second before the client's, and a clip boundary in that second answered
// "request abandoned" to a client still listening. The worker also codes that
// answer request_abandoned, which IsNoAnswer treats as no answer, so if it is
// ever read it defers the job instead of becoming a whisper_error.
func setRequestDeadlineHeader(req *http.Request, client *http.Client) {
	if client.Timeout > 0 {
		secs := (client.Timeout + time.Second - 1) / time.Second
		req.Header.Set(requestDeadlineHeader, strconv.FormatInt(int64(secs), 10))
	}
}

// wavBytesPerSecond is the worker's 16 kHz mono 16-bit extraction format; an
// uploaded clip's length is estimated from its size at this rate (the worker
// itself reads the WAV header).
const wavBytesPerSecond = 32000

// wavSeconds estimates an upload clip's length from its size. An unreadable
// file contributes 0: sendBatch skips it too.
func wavSeconds(path string) float64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return float64(fi.Size()) / wavBytesPerSecond
}

// pathRoot is one mapped root's /health entry.
type pathRoot struct {
	Prefix   string `json:"prefix"`
	Readable bool   `json:"readable"`
}

// supportsBatch reports whether the server exposes /transcribe-batch.
// Present in v2 server; absent means old server.
func (h remoteHealth) supportsBatch() bool { return h.BatchPipeline != nil }

// healthProbeTimeout is how long a /health probe waits for an answer before the
// endpoint counts as unreachable. A variable only so a test can wait it out in
// milliseconds; nothing else changes it.
var healthProbeTimeout = 3 * time.Second

// probeRemoteHealth performs the single /health GET. ok=false means the probe
// itself did not complete (connection error, a status other than 200, non-JSON
// body); callers must not read the returned struct in that case. A non-200 body
// can still be valid JSON (FastAPI's {"detail": ...}) that decodes into an
// all-zero health, which would read as a worker with no busy fields and
// blind every liveness and stuck judgement made from it. A failed probe is NOT an error
// return: the batch decision degrades to the per-file path on failure, which
// is the historical behaviour and must be preserved.
func probeRemoteHealth(ctx context.Context, remoteURL string) (remoteHealth, bool) {
	hctx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
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
	if resp.StatusCode != http.StatusOK {
		return remoteHealth{}, false
	}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return h, false
	}
	return h, true
}

// transcribeRemoteWithHealth sends WAV jobs to the remote faster-whisper
// server, using /transcribe-batch when the health response advertises it
// (faster-whisper >=1.0.0 server) and the per-file worker pool otherwise. The
// caller has ALREADY probed /health -- the pool gate does, to decide require_gpu -- so the batch
// decision reuses that same response instead of issuing a second one.
//
// With a journal, this is also where the journal is consulted, because this is
// the first point the endpoint's model (from /health) is known and the model
// is part of the key: jobs already journalled for this model are served and
// not sent, byte-identical clips are sent once, and every result the endpoint
// returns is journalled before progress is reported for it.
func transcribeRemoteWithHealth(ctx context.Context, remoteURL string, h remoteHealth, probed bool, limit int, jobs map[string]string, onProgress ProgressFunc, journal ResultJournal) (map[string]BatchResult, error) {
	ej := newEndpointJournal(journal, remoteURL, h, probed)
	served, send, followers := ej.plan(jobs)
	total := len(jobs)

	// Progress stays in units of THIS call's jobs: served jobs count as done
	// up front, and the sender's own count is offset by them.
	sendProgress := onProgress
	if onProgress != nil && len(send) != total {
		base := len(served)
		if base > 0 {
			onProgress(base, total)
		}
		sendProgress = func(d, _ int) { onProgress(base+d, total) }
	}

	results := make(map[string]BatchResult, total)
	if len(send) > 0 {
		var (
			sent map[string]BatchResult
			err  error
		)
		if probed && h.supportsBatch() {
			sent, err = transcribeRemoteBatched(ctx, remoteURL, h, limit, send, sendProgress, ej)
		} else {
			sent, err = transcribeRemotePerFile(ctx, remoteURL, limit, send, sendProgress, ej)
		}
		// A failed send may still carry the chunks that completed before it;
		// they are valid results (and already journalled), so hand them up
		// with the error and let the dispatcher keep them.
		maps.Copy(results, sent)
		if err != nil {
			if IsWorkerSwapped(err) {
				// Withheld by the dispatcher as a whole, so not replayable from the
				// journal either (see invalidateWritten).
				ej.invalidateWritten()
			}
			fanOut(results, followers)
			// Journal-served results are as valid as the sent ones; dropping
			// them here would force every later run to re-read them.
			maps.Copy(results, served)
			if stuck, ok := errors.AsType[*WorkerStuckError](err); ok && len(followers) > 0 {
				// The followers' leader is stuck; they share its fate.
				stuck.Followers = followers
			}
			if fault, ok := errors.AsType[*WorkerFaultError](err); ok && len(followers) > 0 {
				// Likewise a faulted leader's followers: they were never sent, and
				// are re-sent with it or deferred with it, never alone.
				fault.Followers = followers
			}
			return results, err
		}
	}
	fanOut(results, followers)
	maps.Copy(results, served)
	if onProgress != nil && len(followers) > 0 {
		onProgress(len(results), total)
	}
	return results, nil
}

// transcribeRemoteBatched sends jobs in sub-batches of whisperBatchSize to
// /transcribe-batch. Processing is sequential inside each sub-batch (the GPU
// handles one file at a time), but reduced HTTP round-trips and
// BatchedInferencePipeline on the server give 2-3x throughput vs per-file mode.
//
// ej (may be nil) journals each chunk's results the moment the chunk returns,
// before its progress is reported and before the next chunk is sent, so a kill
// at any point loses at most the chunk in flight.
func transcribeRemoteBatched(ctx context.Context, remoteURL string, h remoteHealth, limit int, jobs map[string]string, onProgress ProgressFunc, ej *endpointJournal) (map[string]BatchResult, error) {
	// Stable order so progress reporting is deterministic.
	ordered := make([]wavJob, 0, len(jobs))
	for id, path := range jobs {
		ordered = append(ordered, wavJob{id, path})
	}

	results := make(map[string]BatchResult, len(jobs))
	total := len(jobs)
	done := 0

	// WhisperBatchSleepMS: milliseconds to pause between sub-batches so the
	// GPU can shed heat. Defaults to 8000ms (8s). Set to 0 to disable.
	batchSleep := time.Duration(config.AppConfig.WhisperBatchSleepMS) * time.Millisecond

	size := whisperBatchSize()
	for start := 0; start < len(ordered); start += size {
		end := min(start+size, len(ordered))
		chunk := ordered[start:end]

		// Hold an endpoint slot for the request only. Acquiring around the
		// whole loop instead would serialise a caller's entire job list behind
		// one slot, which starves the other endpoints this dispatch is also
		// feeding.
		release, err := acquireInFlight(ctx, remoteURL, limit)
		if err != nil {
			// Chunks that completed before this one are valid: hand them up.
			return results, err
		}
		secs := make([]float64, len(chunk))
		ids := make([]string, len(chunk))
		for i, e := range chunk {
			secs[i], ids[i] = wavSeconds(e.path), e.id
		}
		timeout := requestTimeout(h, secs, false)
		// The ceiling is per request: it scales with the clips in THIS chunk.
		client := &http.Client{Timeout: timeout}
		var (
			batchResults map[string]BatchResult
			uploaded     map[string]string
		)
		// superviseRequest watches the worker from the side and ends the
		// request early when it stops answering; see its doc comment.
		err = superviseRequest(ctx, remoteURL, h, timeout, ids, func(rctx context.Context) error {
			var serr error
			batchResults, uploaded, serr = sendBatch(rctx, client, remoteURL, chunk)
			return serr
		})
		release()
		if err != nil {
			// Chunks that completed before this one stay in results and are
			// returned with the error: they are valid, so the dispatcher keeps
			// them rather than re-sending them elsewhere.
			slog.Warn("transcribe: chunk failed, keeping completed work from this endpoint",
				"url", remoteURL, "completed", done, "total", total, "chunk_start", start,
				"stuck", IsWorkerStuck(err), "err", err.Error())
			return results, fmt.Errorf("transcribe-batch chunk %d-%d: %w", start, end, err)
		}
		// One response is judged on its own (see workerFaultIn): what the worker
		// says about its clips is a verdict only when the worker itself is sound.
		kept, fault := splitWorkerFault(remoteURL, batchResults)
		ej.complete(kept, uploaded)
		for id, r := range kept {
			results[id] = r
			done++
			if onProgress != nil {
				onProgress(done, total)
			}
		}
		if fault != nil {
			// The faulted results are not returned: the dispatcher benches this
			// endpoint and hands those jobs to another one, and no later chunk
			// is sent to a worker that has just shown it cannot decode.
			dispatchLog.Warn("worker fault, keeping the sound results from this endpoint: url=%s kept=%d faulted=%d chunk_start=%d reason=%s",
				logger.SanitizeLogValue(remoteURL), len(kept), len(fault.Faulted), start, logger.SanitizeLogValue(fault.Reason))
			return results, fmt.Errorf("transcribe-batch chunk %d-%d: %w", start, end, fault)
		}

		// Sleep between batches (not after the last one) to let the GPU cool.
		if batchSleep > 0 && end < len(ordered) {
			select {
			case <-ctx.Done():
				return results, ctx.Err()
			case <-time.After(batchSleep):
			}
		}
	}
	return results, nil
}

// sendBatch sends one multipart request containing len(chunk) WAV files.
// The filename of each part is the book ID so the server echoes it back as
// the result key. uploaded maps each sent id to the sha256 of the bytes that
// went into its part -- hashed while copying from the same fd, so it names
// exactly what whisper heard.
func sendBatch(ctx context.Context, client *http.Client, remoteURL string, chunk []wavJob) (results map[string]BatchResult, uploaded map[string]string, err error) {
	uploaded = make(map[string]string, len(chunk))
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
			return nil, nil, fmt.Errorf("create form file %s: %w", e.id, err)
		}
		hr := newHashingReader(f)
		if _, err := io.Copy(fw, hr); err != nil {
			f.Close()
			return nil, nil, fmt.Errorf("copy wav %s: %w", e.id, err)
		}
		f.Close()
		uploaded[e.id] = hr.sum()
	}
	mw.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, remoteURL+"/transcribe-batch", &body)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	setRequestDeadlineHeader(req, client)

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("server returned HTTP %d", resp.StatusCode)
	}

	var out struct {
		Results map[string]struct {
			Text  string  `json:"text"`
			Error *string `json:"error"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, nil, fmt.Errorf("decode batch response: %w", err)
	}

	ret := make(map[string]BatchResult, len(out.Results))
	for id, v := range out.Results {
		r := BatchResult{Text: v.Text}
		if v.Error != nil {
			r.Error = *v.Error
		}
		ret[id] = r
	}
	return ret, uploaded, nil
}

// transcribeRemotePerFile is the original per-file worker-pool path, kept as
// fallback for servers that don't expose /transcribe-batch.
//
// ej (may be nil) journals each file's result as it is drained, before its
// progress is reported.
func transcribeRemotePerFile(ctx context.Context, remoteURL string, limit int, jobs map[string]string, onProgress ProgressFunc, ej *endpointJournal) (map[string]BatchResult, error) {
	client := &http.Client{Timeout: 120 * time.Second}

	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type jobItem struct {
		id      string
		wavPath string
	}
	type resultItem struct {
		id       string
		result   BatchResult
		uploaded string // sha256 of the bytes sent
		err      error
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
				// Same registry as the batch path: an endpoint's cap is a
				// property of the server, not of the code path reaching it.
				release, aerr := acquireInFlight(batchCtx, remoteURL, limit)
				if aerr != nil {
					resultCh <- resultItem{id: j.id, err: aerr}
					cancel()
					return
				}
				r, uploaded, err := transcribeOneRemote(batchCtx, client, remoteURL, j.wavPath)
				release()
				resultCh <- resultItem{id: j.id, result: r, uploaded: uploaded, err: err}
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

	// Drain resultCh to completion even after an error, rather than returning
	// on the first one.
	//
	// resultCh is closed only after wg.Wait(), so returning early leaves the
	// remaining workers RUNNING: they keep calling acquireInFlight (which reads
	// config.AppConfig) and transcribeOneRemote (which sends real HTTP), so a
	// dispatch that has already reported failure goes on generating load
	// against the endpoint and holding in-flight slots the caller believes it
	// released. Under -race it also lets those goroutines outlive the test that
	// spawned them and collide with the next test's global-config write, which
	// is how this surfaced.
	//
	// cancel() still fires immediately, so draining costs only the time the
	// in-flight requests take to notice the cancelled context.
	results := make(map[string]BatchResult, len(jobs))
	total := len(jobs)
	var firstErr error
	for item := range resultCh {
		if item.err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("remote transcribe %s: %w", item.id, item.err)
				cancel()
			}
			continue
		}
		ej.completeOne(item.id, item.uploaded, item.result)
		results[item.id] = item.result
		if onProgress != nil {
			onProgress(len(results), total)
		}
	}
	if firstErr != nil {
		// The files that finished are valid (and already journalled).
		return results, firstErr
	}
	return results, nil
}

// transcribeOneRemote sends one WAV to /transcribe. uploaded is the sha256
// of the bytes sent, hashed while copying from the same fd.
func transcribeOneRemote(ctx context.Context, client *http.Client, remoteURL, wavPath string) (result BatchResult, uploaded string, err error) {
	f, err := os.Open(wavPath)
	if err != nil {
		return BatchResult{}, "", fmt.Errorf("open wav: %w", err)
	}
	defer f.Close()
	hr := newHashingReader(f)
	result, err = postOneWAV(ctx, client, remoteURL, filepath.Base(wavPath), hr)
	if err != nil {
		return BatchResult{}, "", err
	}
	return result, hr.sum(), nil
}

// postOneWAV uploads one WAV, read from src, to the worker's /transcribe as
// name, and returns the worker's answer: its text, or its per-file error. An
// error return is a transport failure (or a non-200), never a verdict.
func postOneWAV(ctx context.Context, client *http.Client, remoteURL, name string, src io.Reader) (BatchResult, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		return BatchResult{}, err
	}
	if _, err := io.Copy(fw, src); err != nil {
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
