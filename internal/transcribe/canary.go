// file: internal/transcribe/canary.go
// version: 1.6.0
// guid: 85242dc9-bee7-4bee-9854-d4965e925162
// last-edited: 2026-10-26

package transcribe

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// The canary clip (SF1 of review 4 of #4182). When every endpoint of a dispatch
// has faulted on timeouts, the dispatcher cannot tell a page of genuine
// loopers (the clips are to blame, and each clip's own timeout should count)
// from every machine being sick at once (a Mac swapping under a CI job, llm1
// out of the pool): both answer every clip with a timeout. So before it
// believes a faulted machine again it sends that machine this clip, two
// seconds of synthesised speech ("Welcome to Gotham City.") that any sound
// worker answers in a few seconds. A machine that answers it is decoding: its
// timeouts are about the clips, and are believed. One that does not is sick:
// none of its strikes count in that dispatch.
//
// It is 16 kHz mono 16-bit PCM, the format every worker already receives from
// the upload path, so it exercises the same decode as a real clip. Made with
// eSpeak NG 1.52.0 (its output is not covered by its licence) and ffmpeg; see
// scripts/make_canary_clip.sh.
//
//go:embed canary_clip.wav
var canaryClip []byte

// canaryFileName is the upload name of the canary clip, the key a worker logs.
const canaryFileName = "canary.wav"

// canaryTimeout bounds one canary request. The canary follows a timeout fault,
// and a worker restarts its decode child lazily, so the canary pays a model
// load first: about 7 s for whisper-small.en on the production M1 Max (measured
// from a cold worker, Hugging Face revision check included), then well under a
// second for the clip. The allowance is the worker's own per-clip floor (60 s),
// so the canary never gets more than one clip's time, with room for a slow
// load. Deriving it from child_ready_timeout_s, as requestTimeout does, would
// allow 360 s per canary; a machine that needs that long to load a model and
// decode two seconds of speech is the sick machine the canary is there to find.
var canaryTimeout = 60 * time.Second // a var so tests do not wait it out

// canaryFunc sends the canary clip to one endpoint: nil when the worker
// answered it without an error (any text, or none from a worker whose VAD
// heard nothing: the question is whether it decodes in time, not what it
// heard), else why not.
type canaryFunc func(ctx context.Context, g gatedEndpoint) error

// canaryFailed is every machine (machineKey) that failed the canary clip and
// has not answered one since, for the life of the process (nit 1 of review 5
// of #4182). Each dispatch starts with no memory of sickness, so without it
// every page of the book op, and every try of the per-file op, sent a sick
// machine a fresh chunk and paid a full chunk of per-clip deadlines (48 to 96
// minutes) before it faulted again. A cooldown cannot carry this: the
// dispatcher waits a cooldown out (poolPaceMax is longer than the longest
// one) and then sends the chunk anyway. A machine in this set is sent the
// canary first instead, which costs one clip, and gets work only once every
// one of its ports answers it. (A port in cooldown is waited for, not skipped:
// see resolvePending in dispatchPool.)
//
// Keys are machineKey values. An entry is dropped when its machine answers (if
// nothing newer was recorded meanwhile: see canaryEntry.gen), and an entry for
// an identity the endpoint no longer has is moved to the endpoint's new
// identity by pruneStaleCanaryIdentities, so the set cannot grow with dead
// identities.
var canaryFailed = struct {
	mu sync.Mutex
	// seq is the last generation handed out. It only ever grows, and it is not
	// per entry: a counter that restarted when an entry was deleted would let a
	// dispatch that snapshotted generation 1, an answer that cleared the entry
	// and a later failure that re-created it (generation 1 again) look like
	// nothing happened in between.
	seq uint64
	m   map[string]*canaryEntry
	// byURL is the last STRONG identity (an "m:" machine key) a probe reported
	// for each endpoint URL, as far as this process has seen: the one thing that
	// outlives a worker's identity changing (see noteCanaryPort). It is the
	// link between a URL and the machine remembered for it when the worker
	// answers as "u:" (it restarted under memory pressure and missed its
	// MACHINE_ID_WAIT_S) or its /health cannot be read. Bounded by the
	// configured URLs: pruneUnconfiguredCanaryURLs.
	byURL map[string]string
}{m: make(map[string]*canaryEntry), byURL: make(map[string]string)}

// canaryEntry is what is remembered about one machine that failed the canary.
type canaryEntry struct {
	// gen is the generation of the latest failure recorded for the machine.
	// Every markCanaryFailed assigns a new one, even when the machine was
	// already remembered, so a dispatch that asked the machine and saw it
	// answer can tell that another dispatch recorded a newer failure while it
	// waited, and must not forget it (forgetCanaryIf).
	gen uint64
	// ports is every endpoint URL that a /health probe has reported as a port
	// of this machine, as far as this process has seen. An endpoint whose
	// /health cannot be read is a port of this machine only if it is in here:
	// see hasUnheardPort.
	ports map[string]bool
}

// markCanaryFailed records that machine key failed the canary clip, with the
// URLs of the ports known to belong to it. It always assigns a new generation.
func markCanaryFailed(key string, ports ...string) {
	canaryFailed.mu.Lock()
	defer canaryFailed.mu.Unlock()
	markCanaryFailedLocked(key, ports)
}

func markCanaryFailedLocked(key string, ports []string) {
	if canaryFailed.m[key] == nil && !isStrongKey(key) {
		// A weak identity with no entry of its own: an entry another dispatch
		// moved to the worker's new key (or that remembers the machine behind
		// this URL) is where the failure belongs, not a new "u:" entry beside it.
		if rk := resolveCanaryKeyLocked(key, ports); rk != "" {
			key = rk
		}
	}
	canaryFailed.seq++
	e := canaryFailed.m[key]
	if e == nil {
		e = &canaryEntry{ports: make(map[string]bool)}
		canaryFailed.m[key] = e
	}
	e.gen = canaryFailed.seq
	for _, u := range ports {
		e.ports[u] = true
	}
	if isStrongKey(key) {
		// Every URL a probe has ever attributed to this machine is one of its
		// ports, up or down right now (S1 of the re-review of #4193): a port
		// that was refused with no machine when the machine failed is never
		// among the ports the dispatch saw for it, and without this the machine
		// could be forgotten on its other ports and the down port later given
		// work with no canary.
		for u, k := range canaryFailed.byURL {
			if k == key {
				e.ports[u] = true
			}
		}
	}
}

// isStrongKey reports whether key is a machine identity ("m:" + the id the
// worker reports), as opposed to the weaker ones: "u:" (a worker too old to
// report one, or one whose lookup missed its wait) and "?" (no /health). A
// weak key is a function of one URL and says nothing about the machine.
func isStrongKey(key string) bool { return strings.HasPrefix(key, "m:") }

// resolveCanaryKeyLocked is the key of the entry that remembers an endpoint
// whose identity in the current dispatch is key and whose URLs are urls, or ""
// when none does. The endpoint's own key first. Failing that, a weak identity
// is looked up through what is known of its URLs (the strong identity a probe
// last reported for it, then any entry listing it as a port): the machine behind
// a URL does not change because its worker lost, or has not yet reported, its
// machine id, and an entry another dispatch moved to the worker's new key while
// this one waited is still the entry for it. A strong key names its machine, so
// it never resolves to another one's entry.
func resolveCanaryKeyLocked(key string, urls []string) string {
	if canaryFailed.m[key] != nil {
		return key
	}
	if isStrongKey(key) {
		return ""
	}
	for _, u := range urls {
		if k := canaryFailed.byURL[u]; k != "" && canaryFailed.m[k] != nil {
			return k
		}
	}
	var found string
	for k, e := range canaryFailed.m {
		for _, u := range urls {
			if e.ports[u] && (found == "" || k < found) {
				found = k
			}
		}
	}
	return found
}

// canaryMemoryKey is resolveCanaryKeyLocked under the lock.
func canaryMemoryKey(key string, urls ...string) string {
	canaryFailed.mu.Lock()
	defer canaryFailed.mu.Unlock()
	return resolveCanaryKeyLocked(key, urls)
}

// endpointRemembered is the one question every part of the dispatcher asks
// about an endpoint: is the machine behind it remembered as having failed the
// canary clip and not answered one since? It is when the endpoint's own key is
// remembered, or when its URL leads to a remembered entry (resolveCanaryKeyLocked).
func endpointRemembered(key, url string) bool {
	return canaryMemoryKey(key, url) != ""
}

func clearCanaryFailed(key string) {
	canaryFailed.mu.Lock()
	defer canaryFailed.mu.Unlock()
	delete(canaryFailed.m, key)
}

// canaryGeneration is the generation of the entry that remembers key (or, for a
// weak key, one of urls: see resolveCanaryKeyLocked), or 0 when the machine is
// not remembered (generations start at 1). A dispatch takes it before it asks
// the machine the canary clip and hands it back to forgetCanaryIf.
func canaryGeneration(key string, urls ...string) uint64 {
	canaryFailed.mu.Lock()
	defer canaryFailed.mu.Unlock()
	if e := canaryFailed.m[resolveCanaryKeyLocked(key, urls)]; e != nil {
		return e.gen
	}
	return 0
}

// forgetCanaryIf drops machine key from the remembered failures after it
// answered the canary clip, but only if (a) its entry is still the one the
// dispatch saw when it began to ask (gen), so a failure another dispatch
// recorded in the meantime is not erased by this dispatch's older answer, and
// (b) no port of it is unheard (hasUnheardPort). Both are decided under one
// lock. The entry is the one that remembers key or, for a weak key, urls (the
// ports that answered): it may have moved to another key since the dispatch
// began (resolveCanaryKeyLocked). It reports whether the machine was forgotten.
func forgetCanaryIf(key string, gen uint64, urls []string, refused []gpuRefusal, kept []gatedEndpoint) bool {
	canaryFailed.mu.Lock()
	defer canaryFailed.mu.Unlock()
	rk := resolveCanaryKeyLocked(key, urls)
	e := canaryFailed.m[rk]
	if e == nil || e.gen != gen || e.hasUnheardPort(rk, refused, kept) {
		return false
	}
	delete(canaryFailed.m, rk)
	return true
}

// noteCanaryPort records that a probe of url reported identity key.
//
// A strong identity (an "m:" machine key) is what ties a URL to a machine: the
// URL belongs to one machine at a time, so it leaves every other entry's set,
// byURL points at it, and it joins key's set if the machine is remembered. A
// weaker one (key "u:" or "?") says nothing about the machine, only that the
// worker did not report one just now (B-2 of the re-review of #4193: a worker
// restarted under memory pressure answers as "u:" while its machine is still
// remembered under "m:"), so it takes nothing from any other entry and leaves
// byURL alone: it only joins its own entry's set, if it has one.
func noteCanaryPort(key, url string) {
	canaryFailed.mu.Lock()
	defer canaryFailed.mu.Unlock()
	if isStrongKey(key) {
		for k, e := range canaryFailed.m {
			if k != key {
				delete(e.ports, url)
			}
		}
		canaryFailed.byURL[url] = key
	}
	if e := canaryFailed.m[key]; e != nil {
		e.ports[url] = true
	}
}

// pruneUnconfiguredCanaryURLs drops every URL that is not one of the configured
// endpoints from byURL and from every entry's port set, so neither grows with
// endpoints an operator has since removed (nit 6 of the re-review of #4193).
// Every production caller of dispatchPool passes the whole configured pool.
func pruneUnconfiguredCanaryURLs(configured []Endpoint) {
	keep := make(map[string]bool, len(configured))
	for _, ep := range configured {
		keep[ep.URL] = true
	}
	canaryFailed.mu.Lock()
	defer canaryFailed.mu.Unlock()
	for u := range canaryFailed.byURL {
		if !keep[u] {
			delete(canaryFailed.byURL, u)
		}
	}
	for _, e := range canaryFailed.m {
		for u := range e.ports {
			if !keep[u] {
				delete(e.ports, u)
			}
		}
	}
}

// hasUnheardPort reports whether the dispatch has an endpoint that is, or may
// be, a port of machine key and did not answer the canary clip with the others
// (nit 2 of review 6 of #4193): the machine must not be forgotten as sick on
// the strength of its other ports alone, because the pool's next dispatch asks
// every port that is eligible, and would otherwise give the refused one work
// with no question asked when it came back.
//
// Such an endpoint is one the gate refused whose machine was recorded as key,
// or one whose /health could not be read at all and that is a known port of key
// (S1 of the review of #4193's follow-up). Its machine is unknown, so it could
// be anyone's, but it is key's only if a probe once reported it as a port of
// key: on a production pool every endpoint sets require_gpu, so a dead tunnel
// of an unrelated machine is refused with no machine, and counting it against
// every remembered machine kept all of them remembered for as long as it stayed
// down. An unprobed endpoint the gate KEPT (a pool with no requirement) is
// asked under its own "?" key, not as a port of key, so it counts the same way.
func hasUnheardPort(key string, refused []gpuRefusal, kept []gatedEndpoint) bool {
	canaryFailed.mu.Lock()
	defer canaryFailed.mu.Unlock()
	e := canaryFailed.m[key]
	return e != nil && e.hasUnheardPort(key, refused, kept)
}

// A refusal counts for the machine when it names it, or when it
// names no machine or only a weak identity (the worker's /health could not be
// read, or it answered as "u:") and its URL is a known port of the machine: a
// weak identity never moves a URL off a machine (noteCanaryPort), so it cannot
// vouch that the port is anyone else's.
func (e *canaryEntry) hasUnheardPort(key string, refused []gpuRefusal, kept []gatedEndpoint) bool {
	for _, r := range refused {
		if r.Machine == key || (!isStrongKey(r.Machine) && e.ports[r.URL]) {
			return true
		}
	}
	for _, g := range kept {
		if !g.Probed && g.machineKey() != key && e.ports[g.Endpoint.URL] {
			return true
		}
	}
	return false
}

// refusedPorts is the URL of every refusal recorded as a port of machine key.
func refusedPorts(refused []gpuRefusal, key string) []string {
	var out []string
	for _, r := range refused {
		if r.Machine == key {
			out = append(out, r.URL)
		}
	}
	return out
}

// pruneStaleCanaryIdentities moves the entries of canaryFailed that name g's
// endpoint under an identity it no longer has to the identity it has now (nit 3
// of review 6 of #4193, B1 of its follow-up). A worker too old to report
// machine_id is keyed "u:" + a hash of its URL, and an endpoint that could not
// be probed "?" + the same hash; once the worker reports a machine_id its key
// is "m:...", a different key, so the old entry would never be read or cleared
// again.
//
// The failure is carried to the new key, not forgotten. The switch does not
// need a restart: scripts/whisper_mlx_server.py leaves machine_id out of
// /health when its first lookup misses MACHINE_ID_WAIT_S (2.5 s) and keeps
// retrying every MACHINE_ID_RETRY_S (30 s) in the same process, so a worker
// that failed the canary a moment ago can report an id on its next /health
// with nothing about it repaired. Dropping the entry then sent the sick machine
// a fresh chunk. Only the worker's own earlier keys are touched, and only once
// it has a better one, so another machine's memory is never read or changed.
func pruneStaleCanaryIdentities(g gatedEndpoint) {
	reconcileCanaryIdentity(g.Endpoint.URL, g.workerID())
}

// reconcileCanaryIdentity is pruneStaleCanaryIdentities for an endpoint known by
// its URL and the identity its probe reported (workerID: empty when it could
// not be probed). It is also what runs for an endpoint the gate refused for its
// capabilities (nit 5 of the re-review of #4193): its worker reports an
// identity like any other, and a machine that is refused for what it offers
// still has its memory moved when its identity changes.
func reconcileCanaryIdentity(url, workerID string) {
	if workerID == "" {
		return
	}
	h := shortHash(url)
	stale := []string{"?" + h}
	if isStrongKey(workerID) {
		stale = append(stale, "u:"+h)
	}
	canaryFailed.mu.Lock()
	defer canaryFailed.mu.Unlock()
	var carried bool
	ports := []string{url}
	for _, k := range stale {
		if k == workerID {
			continue
		}
		if e := canaryFailed.m[k]; e != nil {
			carried = true
			ports = append(ports, slices.Collect(maps.Keys(e.ports))...)
			delete(canaryFailed.m, k)
		}
	}
	if carried {
		markCanaryFailedLocked(workerID, ports)
	}
}

// reconcileCanaryMemory is what a dispatch's own gate does with what it just
// probed: drop the URLs no longer configured, then move the memory of every
// probed endpoint, kept or refused, to the identity it reports now.
func reconcileCanaryMemory(configured []Endpoint, kept []gatedEndpoint, refused []gpuRefusal) {
	pruneUnconfiguredCanaryURLs(configured)
	for _, g := range kept {
		pruneStaleCanaryIdentities(g)
	}
	for _, r := range refused {
		reconcileCanaryIdentity(r.URL, r.Machine)
	}
}

// noteCanaryProbes records the identity every probed endpoint reported, kept or
// refused (see noteCanaryPort). Every gate does it, the re-probes that follow a
// wait included: it moves no entry, and the URL-to-machine link it keeps is
// what lets the dispatcher recognise a remembered machine again whatever its
// worker answers as next.
func noteCanaryProbes(kept []gatedEndpoint, refused []gpuRefusal) {
	for _, g := range kept {
		if g.Probed {
			noteCanaryPort(g.machineKey(), g.Endpoint.URL)
		}
	}
	for _, r := range refused {
		if r.Machine != "" {
			noteCanaryPort(r.Machine, r.URL)
		}
	}
}

// failedCanaryBefore reports whether key itself is remembered. A dispatch asks
// endpointRemembered instead: a machine's worker does not keep one key.
func failedCanaryBefore(key string) bool {
	canaryFailed.mu.Lock()
	defer canaryFailed.mu.Unlock()
	return canaryFailed.m[key] != nil
}

// errCanarySlotBusy is a canary that was never sent: every request slot on its
// endpoint stayed taken for canarySlotWait (SF1 of review 5 of #4182). That is
// no evidence about the machine, which may be decoding other dispatches' clips
// perfectly well, so the dispatcher neither believes it nor judges it sick: its
// strikes stay withheld, it is not put in cooldown, and a dispatch left with no
// machine to believe ends in an error the ops defer on without counting it
// toward stopping the run (not ErrPoolUnavailable). Its text must not contain
// any of transportWordings, or classifyTransport would read it as a network
// failure; in particular it never wraps the slot error, which carries "context
// deadline exceeded".
var errCanarySlotBusy = errors.New("canary clip not sent: every request slot on the endpoint stayed taken")

// canaryHolderClipSeconds is the clip length canarySlotWait assumes for the
// request ahead of the canary: the 90 s first-pass clip both intro ops send,
// which is what nearly every request on the pool carries.
const canaryHolderClipSeconds = 90

// canarySlotWait bounds how long the canary waits for its endpoint's request
// slot, which it takes like every real request (a worker decodes one request
// at a time behind its inference lock, so a canary that skipped the slot
// queued on the worker behind another dispatch's chunk and ran out its own
// timeout there, failing a healthy machine). The wait must outlast one full
// request already holding the slot: anything shorter loses to a chunk of
// genuine loopers (16 clips at the worker's 360 s per-clip limit is over an
// hour and a half), and a shorter wait would also turn a sick machine's
// verdict into "busy" whenever other pages hold its slots, so the run would
// never stop for it. It is that request's own ceiling, requestTimeout for a
// full batch of first-pass clips by this worker's reported formula: with the
// production defaults (60 s floor, 4x factor, 300 s child start, 120 s
// ffmpeg) 16 x 360 + 60 + 300 + 120 s = 104 min. A request that holds the slot
// longer than its ceiling is stuck, which the dispatch that sent it judges.
//
// The slot channel is not first-come first-served, so with several pages
// queued on one port the canary can still lose the slot to them for the
// whole wait; that ends as errCanarySlotBusy, a deferral, never a verdict.
// A var so tests can shorten it.
var canarySlotWait = func(g gatedEndpoint) time.Duration {
	return requestTimeout(g.Health, slices.Repeat([]float64{canaryHolderClipSeconds}, whisperBatchSize()), true)
}

// sendCanary is the canaryFunc of every production dispatch: the clip goes
// through /transcribe, the single-upload route both worker servers serve. It
// first takes the endpoint's request slot (bounded by canarySlotWait); the
// canaryTimeout clock starts only once the slot is held, so the canary times
// the worker's own decode and never another request's queue. A cancelled ctx
// returns ctx.Err() as it is.
func sendCanary(ctx context.Context, g gatedEndpoint) error {
	url := g.Endpoint.URL
	wait := canarySlotWait(g)
	slotCtx, cancelSlot := context.WithTimeout(ctx, wait)
	release, err := acquireInFlight(slotCtx, url, g.Endpoint.Concurrency)
	cancelSlot()
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return fmt.Errorf("%w for %s", errCanarySlotBusy, wait.Round(time.Second))
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, canaryTimeout)
	defer cancel()
	r, err := postOneWAV(ctx, &http.Client{Timeout: canaryTimeout}, url, canaryFileName, bytes.NewReader(canaryClip))
	if err != nil {
		return fmt.Errorf("canary clip: %w", err)
	}
	if r.Error != "" {
		return fmt.Errorf("canary clip: %s", r.Error)
	}
	return nil
}
