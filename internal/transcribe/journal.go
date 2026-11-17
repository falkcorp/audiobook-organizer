// file: internal/transcribe/journal.go
// version: 1.4.0
// guid: 60c7ae00-b3ec-476b-a63e-00418bb73933
// last-edited: 2026-11-17

package transcribe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
	"io"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/ai/resultjournal"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// clipParamsVersion is folded into every whisper journal key. Bump it when
// the meaning of a journalled transcript changes for the SAME clip bytes and
// model -- e.g. the request starts carrying a language or decode option the
// servers honour, or the text post-processing a server applies changes -- so
// entries from before the change are never served for requests after it.
// The clip's own extraction parameters (duration, sample rate, channels) need
// no bump: they change the WAV bytes, and the bytes are hashed.
//
// v2 (2026-09-19): v1 journalled empty-text results, which a retuned VAD or
// decoder must be allowed to re-answer; v2 never journals them and adds the
// endpoint's decode_fingerprint to the key, so no v1 entry is ever served.
const clipParamsVersion = "whisper-clip-v2"

// pathParamsVersion keys path-job entries (the worker extracts the clip, so the
// WAV bytes are never seen here). It is a different version string from
// clipParamsVersion so the two keyspaces cannot collide: a path key hashes a
// file's identity, a clip key hashes audio bytes.
const pathParamsVersion = "whisper-path-v1"

var journalLog = logger.New("transcribe-journal")

// journalResult is the journalled form of a successful transcript.
type journalResult struct {
	Text string `json:"text"`
}

// endpointJournal is one endpoint call's view of the result journal: the
// endpoint's model identity and decode fingerprint, which with the clip bytes
// make up every key.
// A nil *endpointJournal is valid and means "no journal": plan passes every
// job through and complete is a no-op, which is the pre-journal behaviour.
//
// Immutable after construction except for written (under mu), so the per-file
// workers and the batch loop can use it without further locking; the journal
// itself is safe for concurrent use.
type endpointJournal struct {
	journal  ResultJournal
	endpoint string
	model    string
	// fingerprint is the endpoint's /health decode_fingerprint: a hash of the
	// server's output-affecting decode settings (VAD, decode path, beam size,
	// compute type, language ...). Empty for a worker that predates it; such
	// a worker is still journalled under model alone, because the running Mac
	// workers are not all restarted at once -- the cost is that retuning one
	// of those workers without also changing its model will serve transcripts
	// from its previous settings until it is restarted on a current script.
	fingerprint string
	// extract is the endpoint's /health extract_fingerprint (its ffmpeg
	// arguments). Folded into path-job keys ONLY, so retuning extraction never
	// invalidates upload-path entries and retuning decoding invalidates both.
	extract string

	// mu guards written AND model, fingerprint and extract: they start as what
	// /health said but are replaced by the identity a worker's answer carries
	// (observe), which can differ when the worker process was replaced after the
	// probe. Read them through ident(), never directly, once a call is in flight.
	//
	// written is the content key of every result this call journalled, so that a
	// call whose request is found to have been answered by another machine than
	// the one it was sent to can take them back (invalidateWritten). A key is
	// recorded only after its write succeeded.
	mu      sync.Mutex
	written []string
}

// journalIdent is one consistent reading of an endpointJournal's identity: the key
// and the label of a result are built from the SAME reading, so an observe that
// lands between the two cannot give a result a key of one model and a label of another.
type journalIdent struct {
	model, fingerprint, extract string
}

func (ej *endpointJournal) ident() journalIdent {
	ej.mu.Lock()
	defer ej.mu.Unlock()
	return journalIdent{model: ej.model, fingerprint: ej.fingerprint, extract: ej.extract}
}

// clipKey is the journal key for a clip with the given sha256.
func (id journalIdent) clipKey(clipHash string) string {
	return resultjournal.ContentKey(clipHash, id.model, id.fingerprint, clipParamsVersion)
}

// label is the Entry.Model recorded with a result: the key's model part.
func (id journalIdent) label() string {
	if id.fingerprint == "" {
		return id.model
	}
	return id.model + "#" + id.fingerprint
}

// key is the journal key for a clip with the given sha256.
func (ej *endpointJournal) key(clipHash string) string {
	return ej.ident().clipKey(clipHash)
}

// responseIdentity is the "identity" object a worker puts in every transcription answer
// (whisper_server.py 5.1.0+): the model, backend and fingerprints it decoded WITH, the
// same values it reports on /health. A worker that predates it omits the object.
type responseIdentity struct {
	Model              string `json:"model"`
	Backend            string `json:"backend"`
	DecodeFingerprint  string `json:"decode_fingerprint"`
	ExtractFingerprint string `json:"extract_fingerprint"`
}

// noIdentityLogged records the endpoints already logged for answering without an
// identity, so an old worker is mentioned once, not once per chunk.
var noIdentityLogged sync.Map

// observe makes the journal view agree with the worker that actually answered.
//
// The view starts from the /health probe the gate read, which can be up to
// gateProbeTTL old, and a worker PROCESS that was replaced since (restarted by its
// supervisor, and --device auto resolved differently, say a CUDA load that now fails)
// can be running another model than that probe said. Journalling its text under the
// probe's model would serve it as the old model's from then on. So the answer's own
// identity wins: the results of this answer, and of every later one in the call, are
// filed under it, and the cached probe for the endpoint is dropped so the next
// dispatch asks again instead of reusing the stale view.
//
// An answer with no identity (a worker older than the field) changes nothing: it is
// journalled under what /health said, as before, and that is logged once per
// endpoint. Safe for concurrent use.
func (ej *endpointJournal) observe(resp *responseIdentity) {
	if ej == nil {
		return
	}
	if resp == nil || strings.TrimSpace(resp.Model) == "" {
		if _, seen := noIdentityLogged.LoadOrStore(ej.endpoint, struct{}{}); !seen {
			journalLog.Info("worker answers carry no identity, journalling under its /health identity: url=%s", logger.SanitizeLogValue(ej.endpoint))
		}
		return
	}
	got := journalIdent{
		model:       joinModelIdentity(resp.Backend, resp.Model),
		fingerprint: strings.TrimSpace(resp.DecodeFingerprint),
		extract:     strings.TrimSpace(resp.ExtractFingerprint),
	}
	ej.mu.Lock()
	if got.extract == "" {
		// Not reported (an answer of a route that does not extract): keep the probe's.
		got.extract = ej.extract
	}
	was := journalIdent{model: ej.model, fingerprint: ej.fingerprint, extract: ej.extract}
	if got == was {
		ej.mu.Unlock()
		return
	}
	ej.model, ej.fingerprint, ej.extract = got.model, got.fingerprint, got.extract
	ej.mu.Unlock()

	journalLog.Warn("worker answered as a different model than its /health probe said, journalling under the answer's identity and dropping the cached probe: url=%s probed=%s#%s answered=%s#%s",
		logger.SanitizeLogValue(ej.endpoint), logger.SanitizeLogValue(was.model), logger.SanitizeLogValue(was.fingerprint), logger.SanitizeLogValue(got.model), logger.SanitizeLogValue(got.fingerprint))
	forgetGateProbe(ej.endpoint)
}

// joinModelIdentity is the model's journal identity: qualified by its backend when
// one is reported (the same checkpoint name on two backends is not guaranteed to
// produce the same text).
func joinModelIdentity(backend, model string) string {
	m := strings.TrimSpace(model)
	if m == "" {
		return ""
	}
	if b := strings.TrimSpace(backend); b != "" {
		return b + "/" + m
	}
	return m
}

// modelIdentity is the model the endpoint reported on /health, qualified by
// its backend when it reports one (the same checkpoint name on two backends
// is not guaranteed to produce the same text). Empty when the server did not
// report a model.
func (h remoteHealth) modelIdentity() string {
	return joinModelIdentity(h.Backend, h.Model)
}

// newEndpointJournal returns the journal view for one endpoint, or nil when
// there is no journal or the endpoint's model is unknown.
//
// Unknown model means NO journalling for that endpoint, not a fallback key:
// the key's model component is what keeps a transcript from one model from
// being served for another, and a blank or URL-derived stand-in would let two
// different models collide on one key (or keep serving the old model's text
// after a worker is re-pointed at a new one). Both bundled servers report
// "model" on /health, so this only bites a server too old to say, or a probe
// that failed -- and those still transcribe, just without restart protection.
func newEndpointJournal(j ResultJournal, endpoint string, h remoteHealth, probed bool) *endpointJournal {
	if j == nil {
		return nil
	}
	model := h.modelIdentity()
	if !probed || model == "" {
		journalLog.Warn("endpoint reports no model on /health, results from it are not journalled: url=%s probed=%t", endpoint, probed)
		return nil
	}
	fp := strings.TrimSpace(h.DecodeFingerprint)
	if fp == "" {
		journalLog.Info("endpoint reports no decode_fingerprint, journalling by model only: url=%s model=%s", endpoint, model)
	}
	return &endpointJournal{journal: j, endpoint: endpoint, model: model, fingerprint: fp, extract: strings.TrimSpace(h.ExtractFingerprint)}
}

// lookupBypass is a ResultJournal whose Lookup always misses: every clip is
// sent, and fresh results are still journalled (they overwrite old entries).
// TranscribeBatchOpts uses it for BatchOptions.RefreshJournal.
type lookupBypass struct{ ResultJournal }

func (lookupBypass) Lookup(string) (json.RawMessage, bool, error) { return nil, false, nil }

// hashingReader hashes exactly the bytes read through it, so a key computed
// from it describes what was uploaded, not what the file held earlier.
type hashingReader struct {
	r io.Reader
	h hash.Hash
}

func newHashingReader(r io.Reader) *hashingReader { return &hashingReader{r: r, h: sha256.New()} }

func (hr *hashingReader) Read(p []byte) (int, error) {
	n, err := hr.r.Read(p)
	hr.h.Write(p[:n])
	return n, err
}

func (hr *hashingReader) sum() string { return hex.EncodeToString(hr.h.Sum(nil)) }

// hashClip returns the hex sha256 of the WAV at path.
func hashClip(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// plan splits jobs into:
//   - served: jobs whose clip already has a journalled transcript from this
//     model; they are NOT sent,
//   - send: the jobs to transcribe, with byte-identical clips collapsed to one
//     representative so the same content is never sent twice in one call,
//   - followers: representative id -> the other job ids with the same content,
//     which receive the representative's result (see fanOut).
//
// A clip that cannot be hashed is sent as-is; a lookup that errors is treated
// as a miss and the clip is re-transcribed. Both fail toward doing the work,
// never toward dropping a job. plan's hash only decides lookups and dedup;
// the key a result is journalled under comes from the bytes actually
// uploaded (see completeOne), so a clip replaced between plan and upload is
// never journalled under the old bytes' key.
func (ej *endpointJournal) plan(jobs map[string]string) (served map[string]BatchResult, send map[string]string, followers map[string][]string) {
	if ej == nil {
		return nil, jobs, nil
	}
	served = make(map[string]BatchResult)
	send = make(map[string]string, len(jobs))
	followers = make(map[string][]string)
	repByKey := make(map[string]string)

	// Sorted so the representative of a content group is deterministic.
	ids := make([]string, 0, len(jobs))
	for id := range jobs {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	for _, id := range ids {
		path := jobs[id]
		clipHash, err := hashClip(path)
		if err != nil {
			send[id] = path
			continue
		}
		key := ej.key(clipHash)

		raw, ok, lerr := ej.journal.Lookup(key)
		if lerr != nil {
			journalLog.Warn("journal lookup failed, re-transcribing: job=%s err=%v", id, lerr)
		}
		if ok {
			var jr journalResult
			uerr := json.Unmarshal(raw, &jr)
			if uerr == nil {
				served[id] = BatchResult{Text: jr.Text}
				continue
			}
			journalLog.Warn("journalled result undecodable, re-transcribing: job=%s err=%v", id, uerr)
		}
		if rep, dup := repByKey[key]; dup {
			followers[rep] = append(followers[rep], id)
			continue
		}
		repByKey[key] = id
		send[id] = path
	}
	return served, send, followers
}

// complete journals every successful result in results before the caller
// reports progress for them. uploaded maps job id -> sha256 of the bytes that
// were actually sent for it; a job without one is not journalled.
//
// Only results with non-empty text and no per-file Error are journalled. A
// whisper decode error is often transient (server OOM, a worker restart
// mid-request) and would be served forever. Empty text is the answer VAD or
// decoder tuning exists to change, and retry_silence must reach the server
// for it; journalling "" would serve it to every retry instead. Transport
// failures never reach here -- they are error returns, not results.
//
// A failed Complete is logged, not returned: the transcript is still valid
// and still flows to the caller, which writes it to the book; losing the
// journal entry costs only restart protection for that clip.
func (ej *endpointJournal) complete(results map[string]BatchResult, uploaded map[string]string) {
	if ej == nil {
		return
	}
	for id, r := range results {
		ej.completeOne(id, uploaded[id], r)
	}
}

func (ej *endpointJournal) completeOne(id, uploadedHash string, r BatchResult) {
	if ej == nil || uploadedHash == "" {
		return
	}
	ident := ej.ident()
	ej.put(id, ident.clipKey(uploadedHash), ident.label(), r)
}

// put journals one result under key and label (built from one journalIdent),
// applying the same rules for every kind of job: only a non-empty transcript with
// no per-file error is journalled (see complete for why), and a failed write is
// logged, not returned.
func (ej *endpointJournal) put(id, key, label string, r BatchResult) {
	if ej == nil || r.Error != "" || r.Text == "" {
		return
	}
	if err := ej.journal.Complete(key, ej.endpoint, label, journalResult{Text: r.Text}); err != nil {
		journalLog.Warn("journal write failed, result not restart-protected: job=%s err=%v", id, err)
		return
	}
	ej.mu.Lock()
	ej.written = append(ej.written, key)
	ej.mu.Unlock()
}

// invalidateWritten removes from the journal every result this call journalled.
// The caller does it when its request ended as a WorkerSwappedError: the
// dispatcher withholds every answer of such a call (they cannot be told from
// the swapped-in machine's), so none may be replayed to a later run, and the
// journal is keyed by clip content and model, not by machine, so any endpoint
// running that model would be served them. Entries are written as each chunk
// completes (restart protection), so the removal is what keeps the journal in
// step with what the dispatcher keeps. A key some other call journalled for the
// same clip is removed with it, which costs only a repeated decode. A failed
// removal is logged: the entries then stay, which is the state before this
// existed.
func (ej *endpointJournal) invalidateWritten() {
	if ej == nil {
		return
	}
	ej.mu.Lock()
	keys := slices.Clone(ej.written)
	ej.written = nil
	ej.mu.Unlock()
	if len(keys) == 0 {
		return
	}
	if err := ej.journal.Invalidate(keys); err != nil {
		journalLog.Warn("journal entries of a swapped request could not be removed, they may be replayed: url=%s entries=%d err=%v", ej.endpoint, len(keys), err)
	}
}

// fanOut gives every follower its representative's result.
func fanOut(results map[string]BatchResult, followers map[string][]string) {
	for rep, ids := range followers {
		r, ok := results[rep]
		if !ok {
			continue
		}
		for _, id := range ids {
			results[id] = r
		}
	}
}
