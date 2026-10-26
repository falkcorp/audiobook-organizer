// file: internal/transcribe/path.go
// version: 1.4.1
// guid: 7e41c9a8-2b6d-4f03-8a15-d3c0e9b57a24
// last-edited: 2026-10-26

package transcribe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai/resultjournal"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

var pathLog = logger.New("transcribe-path")

// PathJob is one clip the WORKER extracts: the first Seconds of the audio file
// at Path, a path on THIS server. The worker maps it through its own
// WHISPER_PATH_MAP onto its NFS mount, so no audio is decoded here -- that is
// the standing rule the whole path pipeline exists to honour.
type PathJob struct {
	Path    string
	Seconds int
}

// Worker-side per-job error codes (scripts/whisper_mlx_server.py ERR_*). The
// worker prefixes each error "<code>: <detail>"; callers switch on the code via
// PathErrorCode and never parse free-form ffmpeg output. Keep in step with the
// worker's list.
const (
	PathErrUnmapped      = "path_unmapped"  // refused, or its mapped root is unreadable (lost mount)
	PathErrNotFound      = "not_found"      // root readable, file absent
	PathErrFFmpeg        = "ffmpeg_error"   // a decode verdict about the file
	PathErrFFmpegTimeout = "ffmpeg_timeout" // NFS stall: not a verdict, retryable
	PathErrBadRequest    = "bad_request"    // malformed job
	PathErrWhisper       = "whisper_error"  // inference raised
	// PathErrWorkerMisconfigured: the worker cannot do the job at all (ffmpeg
	// missing from its PATH). A fault of the worker, never a verdict on a book.
	PathErrWorkerMisconfigured = "worker_misconfigured"
	// PathErrContainerRejected: ffmpeg refused the container (not on the
	// worker's format whitelist). The worker's policy, not a verdict on the file.
	PathErrContainerRejected = "container_rejected"
	// PathErrTranscribeTimeout: the decoder did not finish this clip inside the
	// worker's per-clip deadline and its process was killed and restarted. A
	// verdict about THIS file's audio (it sends the decoder into a loop), never
	// an outage: the worker is healthy and answered. One strike is retried; the
	// strike limit makes it permanent (see the maintenance ops).
	PathErrTranscribeTimeout = "transcribe_timeout"
	// PathErrDecoderCrashed: the worker's decode process DIED while it held this
	// clip (a jetsam kill, a Metal abort). Like a timeout it is evidence about
	// the clip, which the strike counter bounds so a clip that deterministically
	// kills the decoder is not re-sent forever; unlike a decode process that
	// cannot START (worker_misconfigured), which is the worker's fault.
	PathErrDecoderCrashed = "decoder_crashed"
	pathErrSeparator      = ": "
)

var pathErrCodes = []string{
	PathErrUnmapped, PathErrNotFound, PathErrFFmpeg, PathErrFFmpegTimeout, PathErrBadRequest, PathErrWhisper, PathErrWorkerMisconfigured, PathErrContainerRejected, PathErrTranscribeTimeout, PathErrDecoderCrashed,
}

// PathErrorCode returns the worker error code that prefixes a path job's error
// text, or "" when the text carries none (an old or foreign worker): callers
// treat that as an ordinary whisper error.
func PathErrorCode(errText string) string {
	code, _, ok := strings.Cut(errText, pathErrSeparator)
	if ok && slices.Contains(pathErrCodes, code) {
		return code
	}
	return ""
}

// pathRefusal is the reason the path pipeline will not use this endpoint, or ""
// to keep it. Three distinct reasons, each fail-closed, each naming what to fix:
// it cannot be read at all; it is a worker with no path map (too old, or
// WHISPER_PATH_MAP unset); or a mapped root is not readable right now.
//
// An unreadable root refuses the ENDPOINT rather than letting every job fail
// inside a 200: the whole page is then deferred with nothing written, instead
// of every book being recorded as failed because a mount dropped.
func pathRefusal(g gatedEndpoint) string {
	switch {
	case !g.Probed:
		return "path pipeline needs /health but it could not be read"
	case !g.Health.PathPipeline || len(g.Health.PathRoots) == 0:
		return "does not advertise path_pipeline (worker older than whisper_mlx_server.py 1.5.0, or WHISPER_PATH_MAP unset)"
	}
	var lost []string
	for _, r := range g.Health.PathRoots {
		if !r.Readable {
			lost = append(lost, r.Prefix)
		}
	}
	if len(lost) > 0 {
		return fmt.Sprintf("mapped root(s) %v not readable on the worker (NFS mount lost?)", lost)
	}
	return ""
}

// PathPipelineAvailable reports whether any configured endpoint advertises the
// path pipeline (the CAPABILITY, not current mount health: a lost mount must
// defer pages with a clear reason, not silently flip the op back to decoding on
// the server). When false, reason says why, per endpoint.
func PathPipelineAvailable(ctx context.Context) (ok bool, reason string) {
	snap := config.Snapshot()
	endpoints := poolEndpoints(snap.WhisperEndpoints, snap.WhisperRemoteURL)
	if len(endpoints) == 0 {
		return false, "no whisper endpoints configured"
	}
	gated, refused := gateEndpoints(ctx, endpoints, snap.WhisperRequires)
	for _, g := range gated {
		if g.Probed && g.Health.PathPipeline {
			return true, ""
		}
	}
	parts := make([]string, 0, len(gated)+len(refused))
	for _, g := range gated {
		parts = append(parts, fmt.Sprintf("%s: %s", g.Endpoint.URL, pathRefusal(g)))
	}
	for _, r := range refused {
		parts = append(parts, r.String())
	}
	slices.Sort(parts)
	return false, strings.Join(parts, "; ")
}

// TranscribeByPathBatch transcribes clips the worker extracts itself. jobs maps
// an opaque key (the book ID) to the server path and clip length. It goes
// through the same pool as TranscribeBatchOpts -- capability gate, priority
// spill, cooldown, in-flight slots, journal, progress -- restricted to endpoints
// that advertise path_pipeline with every mapped root readable.
//
// Fails closed. There is no local fallback (that would decode on the server),
// and when no endpoint qualifies the error is a *TransportError naming each
// refusal, so the caller defers the page and writes no per-book status. As with
// TranscribeBatchOpts, a per-file failure is a BatchResult.Error inside a
// successful return, never an error here.
//
// On an error the returned map still holds every result that settled before
// the failure (nil only when nothing did), so the caller applies those and
// defers the rest instead of discarding finished work.
func TranscribeByPathBatch(ctx context.Context, jobs map[string]PathJob, opts BatchOptions) (map[string]BatchResult, error) {
	if len(jobs) == 0 {
		return nil, nil
	}
	snap := config.Snapshot()
	endpoints := poolEndpoints(snap.WhisperEndpoints, snap.WhisperRemoteURL)
	if len(endpoints) == 0 {
		return nil, classifyTransport(nil, fmt.Errorf(
			"no whisper endpoints configured: path jobs run on a worker, never on this server"))
	}
	journal := opts.Journal
	if journal != nil && opts.RefreshJournal {
		journal = lookupBypass{journal}
	}
	return dispatchPool(ctx, endpoints, snap.WhisperRequires, jobs, opts.OnProgress, pathRefusal,
		func(ctx context.Context, g gatedEndpoint, sub map[string]PathJob, progress ProgressFunc) (map[string]BatchResult, error) {
			return transcribePathRemote(ctx, g, sub, progress, journal)
		})
}

// pathSignature identifies the INPUT of a path job without reading it: a hash of
// the server path, the file's size and mtime, and the clip length. A stat is
// all the server does -- no audio is read. A file replaced in place changes
// size or mtime and so changes the key, which is what keeps a journalled
// transcript from being served for different audio. "" means the file could not
// be stat'ed; the job is sent anyway (the worker reports the real reason) and
// its result is simply not journalled.
func pathSignature(j PathJob) string {
	fi, err := os.Stat(j.Path)
	if err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	h := sha256.New()
	for _, part := range []string{j.Path, strconv.FormatInt(fi.Size(), 10), strconv.FormatInt(fi.ModTime().UnixNano(), 10), strconv.Itoa(j.Seconds)} {
		h.Write([]byte(strconv.Itoa(len(part))))
		h.Write([]byte{':'})
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// pathKey is the journal key for a path job with the given signature. Its own
// version string and the extract fingerprint keep it disjoint from clip keys.
func (ej *endpointJournal) pathKey(sig string) string {
	return resultjournal.ContentKey(sig, ej.model, ej.fingerprint, ej.extract, pathParamsVersion)
}

// planPath is plan for path jobs: journalled jobs are served and not sent, the
// rest are sent, and sigs records each sent job's signature so its result can
// be journalled under the key computed BEFORE the send. Unlike plan there are
// no followers: two files are never assumed identical without reading them. A
// nil receiver sends everything.
func (ej *endpointJournal) planPath(jobs map[string]PathJob) (served map[string]BatchResult, send map[string]PathJob, sigs map[string]string) {
	if ej == nil {
		return nil, jobs, nil
	}
	served = make(map[string]BatchResult)
	send = make(map[string]PathJob, len(jobs))
	sigs = make(map[string]string, len(jobs))
	for id, job := range jobs {
		sig := pathSignature(job)
		if sig == "" {
			send[id] = job
			continue
		}
		raw, ok, lerr := ej.journal.Lookup(ej.pathKey(sig))
		if lerr != nil {
			journalLog.Warn("journal lookup failed, re-transcribing: job=%s err=%v", id, lerr)
		}
		if ok {
			var jr journalResult
			if uerr := json.Unmarshal(raw, &jr); uerr == nil {
				served[id] = BatchResult{Text: jr.Text}
				continue
			} else {
				journalLog.Warn("journalled result undecodable, re-transcribing: job=%s err=%v", id, uerr)
			}
		}
		send[id] = job
		sigs[id] = sig
	}
	return served, send, sigs
}

// completePath journals results under the keys planPath computed.
func (ej *endpointJournal) completePath(results map[string]BatchResult, sigs map[string]string) {
	if ej == nil {
		return
	}
	for id, r := range results {
		if sig := sigs[id]; sig != "" {
			ej.put(id, ej.pathKey(sig), r)
		}
	}
}

// transcribePathRemote is transcribeRemoteWithHealth for path jobs: journal
// plan, then chunked sends, with progress in units of THIS call's jobs.
func transcribePathRemote(ctx context.Context, g gatedEndpoint, jobs map[string]PathJob, onProgress ProgressFunc, journal ResultJournal) (map[string]BatchResult, error) {
	url := g.Endpoint.URL
	ej := newEndpointJournal(journal, url, g.Health, g.Probed)
	served, send, sigs := ej.planPath(jobs)
	total := len(jobs)

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
		sent, err := transcribePathChunks(ctx, url, g.Health, g.Endpoint.Concurrency, send, sigs, sendProgress, ej)
		// A failed send still carries the chunks that completed before it.
		maps.Copy(results, sent)
		if err != nil {
			maps.Copy(results, served)
			return results, err
		}
	}
	maps.Copy(results, served)
	return results, nil
}

type pathJobEntry struct {
	id  string
	job PathJob
}

// transcribePathChunks sends jobs in whisperBatchSize chunks to
// /transcribe-path-batch. Same shape as transcribeRemoteBatched: a slot is held
// per request and each chunk is journalled before its progress is reported. A
// failed chunk returns the chunks that completed before it alongside the error.
func transcribePathChunks(ctx context.Context, remoteURL string, h remoteHealth, limit int, jobs map[string]PathJob, sigs map[string]string, onProgress ProgressFunc, ej *endpointJournal) (map[string]BatchResult, error) {
	ordered := make([]pathJobEntry, 0, len(jobs))
	for id, j := range jobs {
		ordered = append(ordered, pathJobEntry{id, j})
	}
	slices.SortFunc(ordered, func(a, b pathJobEntry) int { return strings.Compare(a.id, b.id) })

	results := make(map[string]BatchResult, len(jobs))
	total := len(jobs)
	done := 0
	batchSleep := time.Duration(config.AppConfig.WhisperBatchSleepMS) * time.Millisecond

	size := whisperBatchSize()
	for start := 0; start < len(ordered); start += size {
		end := min(start+size, len(ordered))
		chunk := ordered[start:end]

		release, err := acquireInFlight(ctx, remoteURL, limit)
		if err != nil {
			// Chunks that completed before this one are valid: hand them up.
			return results, err
		}
		// Extraction plus inference both happen inside the request, so the
		// ceiling scales with the clips in THIS chunk (see requestTimeout).
		secs := make([]float64, len(chunk))
		ids := make([]string, len(chunk))
		for i, e := range chunk {
			secs[i], ids[i] = float64(e.job.Seconds), e.id
		}
		timeout := requestTimeout(h, secs, true)
		client := &http.Client{Timeout: timeout}
		var batchResults map[string]BatchResult
		err = superviseRequest(ctx, remoteURL, h, timeout, ids, func(rctx context.Context) error {
			var serr error
			batchResults, serr = sendPathBatch(rctx, client, remoteURL, chunk)
			return serr
		})
		release()
		if err != nil {
			pathLog.Warn("path chunk failed, keeping completed work from this endpoint: url=%s completed=%d total=%d chunk_start=%d stuck=%t err=%s",
				remoteURL, done, total, start, IsWorkerStuck(err), err.Error())
			return results, fmt.Errorf("transcribe-path-batch chunk %d-%d: %w", start, end, err)
		}
		// One response is judged on its own (see workerFaultIn).
		kept, fault := splitWorkerFault(remoteURL, batchResults)
		ej.completePath(kept, sigs)
		for id, r := range kept {
			results[id] = r
			done++
			if onProgress != nil {
				onProgress(done, total)
			}
		}
		if fault != nil {
			pathLog.Warn("path chunk: worker fault, keeping the sound results from this endpoint: url=%s kept=%d faulted=%d chunk_start=%d reason=%s",
				logger.SanitizeLogValue(remoteURL), len(kept), len(fault.Faulted), start, logger.SanitizeLogValue(fault.Reason))
			return results, fmt.Errorf("transcribe-path-batch chunk %d-%d: %w", start, end, fault)
		}
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

// sendPathBatch POSTs one chunk. Keys are echoed back verbatim by the worker.
func sendPathBatch(ctx context.Context, client *http.Client, remoteURL string, chunk []pathJobEntry) (map[string]BatchResult, error) {
	type wireJob struct {
		Path    string `json:"path"`
		Seconds int    `json:"seconds"`
	}
	wire := make(map[string]wireJob, len(chunk))
	for _, e := range chunk {
		wire[e.id] = wireJob{Path: e.job.Path, Seconds: e.job.Seconds}
	}
	body, err := json.Marshal(map[string]any{"jobs": wire})
	if err != nil {
		return nil, fmt.Errorf("encode path batch: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, remoteURL+"/transcribe-path-batch", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	setRequestDeadlineHeader(req, client)
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
		return nil, fmt.Errorf("decode path batch response: %w", err)
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
