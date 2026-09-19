// file: internal/transcribe/journal.go
// version: 1.0.0
// guid: 60c7ae00-b3ec-476b-a63e-00418bb73933
// last-edited: 2026-09-19

package transcribe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"slices"
	"strings"

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
const clipParamsVersion = "whisper-clip-v1"

var journalLog = logger.New("transcribe-journal")

// journalResult is the journalled form of a successful transcript.
type journalResult struct {
	Text string `json:"text"`
}

// endpointJournal is one endpoint call's view of the result journal: the
// endpoint's model identity plus the content key of every job it was handed.
// A nil *endpointJournal is valid and means "no journal": plan passes every
// job through and complete is a no-op, which is the pre-journal behaviour.
//
// Built per transcribeRemoteWithHealth call and read-only after plan, so the
// per-file workers and the batch loop can use it without locking; the
// journal itself is safe for concurrent use.
type endpointJournal struct {
	journal  ResultJournal
	endpoint string
	model    string
	keys     map[string]string // job id -> content key (only jobs that could be hashed)
}

// modelIdentity is the model the endpoint reported on /health, qualified by
// its backend when it reports one (the same checkpoint name on two backends
// is not guaranteed to produce the same text). Empty when the server did not
// report a model.
func (h remoteHealth) modelIdentity() string {
	m := strings.TrimSpace(h.Model)
	if m == "" {
		return ""
	}
	if b := strings.TrimSpace(h.Backend); b != "" {
		return b + "/" + m
	}
	return m
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
	return &endpointJournal{journal: j, endpoint: endpoint, model: model}
}

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
// A clip that cannot be hashed is sent as-is, unjournalled; a lookup that
// errors is treated as a miss and the clip is re-transcribed. Both fail
// toward doing the work, never toward dropping a job.
func (ej *endpointJournal) plan(jobs map[string]string) (served map[string]BatchResult, send map[string]string, followers map[string][]string) {
	if ej == nil {
		return nil, jobs, nil
	}
	ej.keys = make(map[string]string, len(jobs))
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
		key := resultjournal.ContentKey(clipHash, ej.model, clipParamsVersion)
		ej.keys[id] = key

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
// reports progress for them. Only results with no per-file Error are
// journalled: a whisper decode error is often transient (server OOM, a
// worker restart mid-request), and a journalled error would be served
// forever, so it is retried instead. Transport failures never reach here --
// they are error returns, not results. Empty text with no error IS journalled:
// it is the model's real answer for those bytes, and the silence-retry ladder
// sends different clips (300s / second file), so different bytes and keys.
//
// A failed Complete is logged, not returned: the transcript is still valid
// and still flows to the caller, which writes it to the book; losing the
// journal entry costs only restart protection for that clip.
func (ej *endpointJournal) complete(results map[string]BatchResult) {
	if ej == nil {
		return
	}
	for id, r := range results {
		ej.completeOne(id, r)
	}
}

func (ej *endpointJournal) completeOne(id string, r BatchResult) {
	if ej == nil || r.Error != "" {
		return
	}
	key, ok := ej.keys[id]
	if !ok {
		return
	}
	if err := ej.journal.Complete(key, ej.endpoint, ej.model, journalResult{Text: r.Text}); err != nil {
		journalLog.Warn("journal write failed, result not restart-protected: job=%s err=%v", id, err)
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
