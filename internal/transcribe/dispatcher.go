// file: internal/transcribe/dispatcher.go
// version: 1.29.0
// guid: ea9de4e6-980d-411f-a92c-878af1df490a
// last-edited: 2026-10-30

package transcribe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// Endpoint describes one Whisper server in the dispatch pool.
//
// Dispatch is a filter followed by priority-ordered spill, in that order:
//
//  1. Capability labels FILTER the candidate set. An endpoint qualifies only
//     if its capabilities contain every label the pool requires (see
//     capabilities.go). This is what "tier routing" means here — a tier is a
//     required-label set, not an entry in a routing table.
//  2. Priority ORDERS the survivors. Lower numbers are filled first (a GPU box
//     gets 1, a CPU box gets 100), one batch-sized chunk per FREE request
//     slot, with the remainder spilling to lower-priority endpoints only when
//     the higher tier's slots are all taken. Equal-priority endpoints share
//     work round-robin.
//
// So deadline-less bulk work lands on low-priority endpoints through spill,
// while work that genuinely CANNOT run somewhere (it needs a GPU, or must stay
// local) is excluded by step 1 rather than merely deprioritised in step 2.
type Endpoint struct {
	// URL is the base URL of the faster-whisper server,
	// e.g. "http://whisper-1.local:8000".
	URL string
	// Concurrency is the endpoint's request-slot count: how many requests it
	// may serve at once (enforced at the request), and so how many
	// whisperBatchSize-sized chunks allocation offers it while its slots are
	// free. Values < 1 are treated as 1.
	Concurrency int
	// Label is a human-readable name for logs ("gpu-box", "cpu-node").
	Label string
	// Priority orders allocation: lower = preferred. Healthy endpoints are
	// filled in ascending Priority order; jobs spill to higher numbers only
	// when lower-numbered endpoints are saturated or unhealthy.
	Priority int
	// RequireGPU refuses this endpoint unless it proves the "gpu" capability.
	// Sugar for adding "gpu" to this endpoint's required set; see
	// capabilities.go for why this is fail-closed.
	RequireGPU bool
	// Capabilities are labels an operator DECLARES about this endpoint, for
	// properties no probe can see ("local", "unmetered", "fast"). Labels that
	// are measured from /health instead (gpu, cpu, cuda, metal, batch, ...)
	// are ignored here and derived from the probe -- declaring them would
	// recreate the Kind illusion this replaces.
	Capabilities []string
}

// Cooldown policy moved to internal/aidispatch (Health), keyed by endpoint ID:
// an endpoint that fails a batch is benched for aidispatch.CooldownBase ×
// consecutive-failures, capped at aidispatch.CooldownMax, so a dead box is
// retried occasionally without being hammered on every page. The process-wide
// tracker persists the bench across TranscribeBatch calls. These wrappers keep
// the URL-keyed call sites below unchanged, via the same URL→ID shim as the
// in-flight slots.

func markEndpointFailure(url string) {
	aidispatch.DefaultHealth().MarkFailure(endpointIDForURL(url))
}

func markEndpointSuccess(url string) {
	aidispatch.DefaultHealth().MarkSuccess(endpointIDForURL(url))
}

func endpointInCooldown(url string) bool {
	return aidispatch.DefaultHealth().InCooldown(endpointIDForURL(url))
}

// endpointFailureGeneration is how many failures have ever been marked for url
// (aidispatch.Health.FailureGeneration): it moves when a cooldown starts, and
// stays moved however briefly the cooldown lasted.
func endpointFailureGeneration(url string) uint64 {
	return aidispatch.DefaultHealth().FailureGeneration(endpointIDForURL(url))
}

// transcribePool distributes jobs across the endpoint pool.
//
// Round loop: healthy endpoints (not in cooldown) are filled in priority
// order via allocateJobs, each endpoint's share is sent through
// transcribeRemoteWithHealth concurrently, failures bench the endpoint and leave its
// jobs in the remaining set to be re-queued to the survivors on the next
// round. A *TransportError (naming every endpoint in the pool) is returned
// ONLY when jobs remain and no healthy endpoint is left — per the locked
// decision in PLAN.md, that error carries no per-file meaning and callers
// must write nothing.
func transcribePool(ctx context.Context, endpoints []Endpoint, requires []string, jobs map[string]string, onProgress ProgressFunc, journal ResultJournal, avoid map[string]string) (map[string]BatchResult, error) {
	return dispatchPool(ctx, endpoints, requires, jobs, poolOptions{onProgress: onProgress, avoid: avoid},
		func(ctx context.Context, g gatedEndpoint, sub map[string]string, progress ProgressFunc) (map[string]BatchResult, error) {
			return transcribeRemoteWithHealth(ctx, g.Endpoint.URL, g.Health, g.Probed, g.Endpoint.Concurrency, sub, progress, journal)
		})
}

// endpointSender transcribes one endpoint's share of a dispatch. It is the only
// part of the round loop that depends on what a job IS (a WAV path to upload,
// or a server path the worker extracts itself), which is why dispatchPool is
// generic over the job type instead of being copied per kind of job.
type endpointSender[J any] func(ctx context.Context, g gatedEndpoint, jobs map[string]J, onProgress ProgressFunc) (map[string]BatchResult, error)

// poolOptions are dispatchPool's per-call settings. Every field may be zero.
type poolOptions struct {
	// onProgress: see ProgressFunc.
	onProgress ProgressFunc
	// extraRefusal lets a caller refuse endpoints the label gate cannot
	// express -- the path pipeline refuses a worker whose mapped NFS root is
	// unreadable right now. A non-empty return is the operator-facing reason
	// and is reported like any capability refusal.
	extraRefusal func(gatedEndpoint) string
	// avoid is BatchOptions.AvoidWorkers: job id -> the machines whose strike
	// the file already carries. See steerAway.
	avoid map[string]string
	// canary sends the canary clip to one endpoint (see canaryFunc); nil is
	// sendCanary. A test pool answers it like any other clip.
	canary canaryFunc
	// beforePace, beforePlacedPace and afterPlacedCheck are test seams, nil in
	// production. The round loop calls beforePace between finding no endpoint that
	// can take work and asking how long to wait (pace), and beforePlacedPace
	// between finding that the split parts could be placed on none of the ports
	// that can take work and looking for a port that came out of its cooldown
	// unprobed; afterPlacedCheck is called between that look and the question of
	// how long to wait (pace): the three moments at which a cooldown that ends has
	// to be noticed.
	beforePace, beforePlacedPace, afterPlacedCheck func()
	// afterCanaries and atRoundStart are test seams too, nil in production, given
	// the URL of every port still in the dispatch and the soloLimit then in force.
	// The round loop calls afterCanaries when the canary probes of a round whose
	// every endpoint is sick are over, before that round's answers are read, and
	// atRoundStart at the top of each pass, after the ports those probes found
	// changed hands are dropped (holdDrops).
	afterCanaries, atRoundStart func(ordered []string, soloLimit int)
}

// dispatchPool is the pool round loop shared by every kind of job: gate,
// priority-ordered allocation, concurrent per-endpoint sends, cooldown on
// failure, re-queue to survivors. See poolOptions for the per-call settings.
//
// Pacing. A dispatch that finds no endpoint it can use because of something an
// EARLIER dispatch saw (every endpoint benched by a previous page's failures,
// or every worker reporting that its decoder is restarting) waits for the
// earliest one to come back, up to poolPaceMax, instead of failing at once. Without
// that, one bench drained the rest of the library as deferred within seconds:
// every later page found the same benches and gave up without asking anyone.
// A dispatch gives up early only on evidence of its own: every endpoint failed
// in THIS dispatch. When the pacing allowance runs out the error wraps
// ErrPoolUnavailable.
func dispatchPool[J any](ctx context.Context, endpoints []Endpoint, requires []string, jobs map[string]J, opts poolOptions, send endpointSender[J]) (map[string]BatchResult, error) {
	onProgress, extraRefusal := opts.onProgress, opts.extraRefusal
	canary := opts.canary
	if canary == nil {
		canary = sendCanary
	}
	if len(jobs) == 0 {
		return nil, nil
	}
	if len(endpoints) == 0 {
		return nil, classifyTransport(nil, fmt.Errorf("no whisper endpoints configured"))
	}
	paceUntil := time.Now().Add(poolPaceMax)

	// The require_gpu gate runs BEFORE the single-endpoint path below. Putting
	// it inside the pool loop instead would miss the one shape that actually
	// occurs in production today -- a single configured endpoint quietly serving
	// from a CPU backend -- because that shape never reaches the loop.
	// gateEndpoints also performs the /health probe that the batch decision
	// needs, so this adds no requests.
	//
	// gate is gateEndpoints plus every refusal the pool applies on top of it, so
	// the gate at the start of the dispatch and the re-probe that follows a wait
	// for a cooldown (regate) judge a port by the same rules. It also
	// moves canary-failure memory to a worker's new identity (and drops the
	// URLs no longer configured), but only when reconcile is set. Every gate
	// records which URLs are ports of which machine, and the strong identity a
	// URL last reported (noteCanaryProbes), since that moves no entry. The
	// re-probes that follow a wait judge a port and nothing else: the dispatch
	// keeps the identities (keyOf) its gate gave it, and the next dispatch's own
	// gate reconciles the memory.
	gate := func(eps []Endpoint, reconcile bool) ([]gatedEndpoint, []gpuRefusal) {
		gated, refused := gateEndpoints(ctx, eps, requires)
		kept := gated[:0:0]
		for _, g := range gated {
			// A wedged worker is refused for every kind of job: a request sent
			// to it is never answered, and queueing behind it is how one bad
			// clip parked the whole pool.
			if reason := stuckRefusal(g); reason != "" {
				r := newRefusal(g, reason)
				r.Stuck = true
				refused = append(refused, r)
				continue
			}
			if reason := childFaultRefusal(g); reason != "" {
				r := newRefusal(g, reason)
				r.Transient = true
				refused = append(refused, r)
				continue
			}
			if extraRefusal != nil {
				if reason := extraRefusal(g); reason != "" {
					refused = append(refused, newRefusal(g, reason))
					continue
				}
			}
			kept = append(kept, g)
		}
		if reconcile {
			// Kept and refused alike: a worker refused for what it offers reports
			// an identity like any other.
			reconcileCanaryMemory(endpoints, kept, refused)
		}
		if ctx.Err() == nil {
			// A probe that a cancellation cut short read nothing: every endpoint
			// looks unreachable, which must not start a run of refusals (D56).
			noteCanaryProbes(kept, refused)
		}
		return kept, refused
	}
	var (
		gated   []gatedEndpoint
		refused []gpuRefusal
	)
	// probed is what the dispatch knew of each port when it last probed it (the
	// gate, regate): the port's failure generation (endpointFailureGeneration)
	// and whether it was in a cooldown. A port needs a probe (needsProbe) when
	// it has failed since, or was in a cooldown when probed: a port is not given
	// work, and is not sent the canary clip, until it has been probed again
	// since the cooldown. The identity the dispatch froze for a port is only as
	// old as its last probe, and a cooldown is minutes long, in which a tunnel
	// can come back from another Mac (D60, D62).
	//
	// The failure generation, not a look at the cooldown, says whether a cooldown
	// happened: InCooldown samples, and a cooldown that another dispatch starts
	// and ends between two looks (a 30 s one inside a request of several
	// minutes) is never seen by one. The mark is taken BEFORE the probe is
	// sent, so a failure that lands while the probe is in flight is probed
	// again, and so is a port whose cooldown ends while the gate's own probe is
	// in flight.
	//
	// Every use also asks that the port be out of its cooldown and not benched
	// for another reason (recheck, waitingForProbe, the healthy list): only a
	// port that is waiting for its probe and can take it now makes the round
	// loop look again, not every port out of cooldown, which includes the ones
	// the work cannot go to and would never end. Each probe (regate) replaces
	// the mark, so every look again is followed by a probe that clears what made
	// it, and it ends unless a new failure is marked.
	type probeMark struct {
		gen    uint64
		cooled bool
	}
	probed := make(map[string]probeMark)
	markProbes := func(eps []Endpoint) map[string]probeMark {
		marks := make(map[string]probeMark, len(eps))
		for _, ep := range eps {
			marks[ep.URL] = probeMark{gen: endpointFailureGeneration(ep.URL), cooled: endpointInCooldown(ep.URL)}
		}
		return marks
	}
	needsProbe := func(url string) bool {
		m, ok := probed[url]
		return ok && (m.cooled || m.gen != endpointFailureGeneration(url))
	}
	for {
		marks := markProbes(endpoints)
		gated, refused = gate(endpoints, true)
		maps.Copy(probed, marks)
		if len(gated) > 0 || !allTransient(refused) || !time.Now().Before(paceUntil) {
			break
		}
		// Every endpoint is refused for something that clears by itself: its
		// decoder is restarting (the worker's fault window), or its /health
		// could not be read (a dropped tunnel, a sleeping host). Ask again
		// shortly rather than failing this dispatch and the next one and the
		// one after that.
		dispatchLog.Warn("every whisper endpoint is unreachable or reports its decoder cannot start; waiting %s before asking again: %s",
			poolPaceInterval, logger.SanitizeLogValue(describeRefusals(refused)))
		if err := sleepCtx(ctx, min(poolPaceInterval, time.Until(paceUntil))); err != nil {
			return nil, classifyTransport(endpointURLs(endpoints), err)
		}
	}
	for _, r := range refused {
		if r.Stuck {
			dispatchLog.Warn("worker stuck, skipping it: url=%s label=%s reason=%s",
				logger.SanitizeLogValue(r.URL), logger.SanitizeLogValue(r.Label), logger.SanitizeLogValue(r.Reason))
			continue
		}
		slog.Warn("transcribe: endpoint refused by capability requirements", "url", r.URL, "label", r.Label, "reason", r.Reason)
	}
	// noEndpointError is the error for a dispatch left with no endpoint to send
	// to, classified by what refused them. A dispatch that has to give up
	// after the gate (every port of the machines it was waiting for turned out
	// wedged or unreachable once the wait was over: resolvePending) ends the
	// same way as one the gate emptied at the start. The count in its messages is
	// of the ports the refusals judged, not of the pool: a dispatch that ends with
	// one port wedged after a wait has not found "all" of its endpoints stuck.
	noEndpointError := func(refused []gpuRefusal) error {
		if allStuck(refused) {
			// Reachable and answering /health, but wedged: not an outage, and
			// waiting it out by re-sending is the wrong response (see
			// ErrWorkerStuck).
			return classifyTransport(endpointURLs(endpoints), fmt.Errorf(
				"%w: all %d whisper endpoint(s) are stuck on a decode past its deadline: %s",
				ErrWorkerStuck, len(refused), describeRefusals(refused)))
		}
		if allTransient(refused) {
			return classifyTransport(endpointURLs(endpoints), fmt.Errorf(
				"%w: all %d whisper endpoint(s) still unreachable or unable to start their decoder after waiting %s: %s",
				ErrPoolUnavailable, len(refused), poolPaceMax, describeRefusals(refused)))
		}
		if changedHandsOrTransient(refused) {
			// Every port the dispatch had left answers as a different worker than
			// the one it knew after a wait (regate), and any other was only
			// unreachable (a dead tunnel is no capability problem). No verdict on
			// anyone and no outage: the same answer as the lone endpoint's, which
			// the ops defer on without counting toward stopping the run (it is not
			// a recognised transport failure).
			return classifyTransport(endpointURLs(endpoints), fmt.Errorf(
				"none of the %d whisper endpoint(s) can take the jobs: those that answer do so as a different worker than the one the dispatch knew before its wait (%w), so they are sent no work and the jobs are deferred: %s",
				len(refused), errPortChangedHands, describeRefusals(refused)))
		}
		if stuckBesideChangedHands(refused) {
			// Some ports are wedged and the rest changed hands (or are down). The
			// pool is not wedged as a whole -- IsWorkerStuck stays false, as in
			// poolFailure when a stuck endpoint is beside an outage -- because the
			// ports that changed hands are not stuck: the next dispatch's gate keys
			// them by what they report and asks them. The ops defer the jobs
			// without counting the page toward stopping the run. The stuck ports
			// are named so the operator sees them.
			return classifyTransport(endpointURLs(endpoints), fmt.Errorf(
				"none of the %d whisper endpoint(s) can take the jobs: some are stuck on a decode past its deadline, and the others answer as a different worker than the one the dispatch knew before its wait (%w), so the jobs are deferred: %s",
				len(refused), errPortChangedHands, describeRefusals(refused)))
		}
		// Distinct from "no whisper endpoints configured": that sends an
		// operator hunting a config typo, when the real answer is that the
		// box they configured is not on a GPU.
		return classifyTransport(endpointURLs(endpoints), fmt.Errorf(
			"all %d whisper endpoint(s) refused by capability requirements %v: %s",
			len(refused), requires, describeRefusals(refused)))
	}
	if len(gated) == 0 {
		return nil, noEndpointError(refused)
	}
	// refusedAtGate is how many refusals the start gate made. What came after it
	// (regate's) is what emptied the pool if the round loop or a wait finds
	// nothing left: poolEmptied names that, and not the gate's refusals of other
	// ports, which are not why this pool has nothing left to send to (a capability
	// refusal at the gate beside two ports that changed hands during the wait is
	// not a pool "refused by capability requirements").
	refusedAtGate := len(refused)
	poolEmptied := func() error { return noEndpointError(refused[refusedAtGate:]) }

	// forget drops a machine from the remembered canary failures once it has
	// answered, unless a newer failure of it was recorded since gen was taken
	// (another dispatch's, see canaryEntry.gen) or a port of it has not
	// answered (see hasUnheardPort). Used wherever a pass would clear the memory.
	// urls are the ports that answered: the entry may have moved to another key
	// since the dispatch began (see resolveCanaryKeyLocked). refused and gated are
	// read when it runs, so it sees the ports a wait dropped from the dispatch.
	forget := func(key string, gen uint64, urls []string) {
		forgetCanaryIf(key, gen, urls, refused, gated)
	}

	// Health is keyed by URL so allocateJobs keeps operating on []Endpoint
	// and its index-based contract with the assignment slices is untouched.
	//
	// keyFor is each endpoint's machine identity as the gate resolved it (D55:
	// by address, so a worker that missed its machine id this time is still the
	// machine its URL last reported), frozen for the dispatch, and capableFor is
	// whether its worker could report a machine id then. A pool probes a port
	// again once it has failed since it was last probed or was in a cooldown then (needsProbe, recheck, regate: the
	// waits for a cooldown, the rounds of work on other ports that outlast one, and
	// the moment a stand-in comes out of one all end in that probe) and uses it only
	// if its identity then equals keyFor (D60, D62): the probe replaces the endpoint's health (the
	// canary and the work use what the worker says now), but any difference, with
	// capableFor as the kind of worker, drops the port, and one that cannot be
	// read is not used, so an answer is never stored as a machine it did not come
	// from. Only the lone endpoint keeps a worker that gained its id and stores
	// both identities (gatedEndpoint.reprobed).
	probeByURL := make(map[string]gatedEndpoint, len(gated))
	keyFor := make(map[string]string, len(gated))
	capableFor := make(map[string]bool, len(gated))
	eligible := make([]Endpoint, 0, len(gated))
	for _, g := range gated {
		probeByURL[g.Endpoint.URL] = g
		keyFor[g.Endpoint.URL] = g.identityKey()
		capableFor[g.Endpoint.URL] = g.idCapable()
		eligible = append(eligible, g.Endpoint)
	}

	if len(eligible) == 1 {
		return dispatchSingle(ctx, probeByURL[eligible[0].URL], refused, singleGate{paceUntil: paceUntil, gate: func(eps []Endpoint) ([]gatedEndpoint, []gpuRefusal) { return gate(eps, false) }, noEndpoint: noEndpointError}, jobs, onProgress, send, canary)
	}

	ordered := make([]Endpoint, len(eligible))
	copy(ordered, eligible)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Priority < ordered[j].Priority })

	remaining := make(map[string]J, len(jobs))
	maps.Copy(remaining, jobs)
	results := make(map[string]BatchResult, len(jobs))
	total := len(jobs)

	// Pool-wide progress: each endpoint call reports its own cumulative
	// (done, total); the adapter converts that to a shared pool-wide count. A job
	// re-sent after a worker's answer was withheld is counted again by the new
	// call, so the count is clamped at total.
	var progressMu sync.Mutex
	poolDone := 0
	report := func() {
		cur := min(poolDone, total)
		progressMu.Unlock()
		onProgress(cur, total)
	}
	progressFor := func() ProgressFunc {
		if onProgress == nil {
			return nil
		}
		last := 0
		return func(d, _ int) {
			progressMu.Lock()
			poolDone += d - last
			last = d
			report()
		}
	}

	// bump advances the pool-wide count for jobs settled without a send.
	bump := func(n int) {
		if onProgress == nil || n == 0 {
			return
		}
		progressMu.Lock()
		poolDone += n
		report()
	}

	// Stuck-batch bookkeeping. A request that a reachable worker never answers
	// says nothing about WHICH of its jobs is to blame, so the failed request
	// is split and the parts retried (units); every stuck part is smaller than
	// the request it came from, so the splitting terminates. A job is only
	// condemned when it was stuck ALONE, on soloLimit distinct MACHINES: an
	// innocent job that shared a request with the culprit was stuck in a batch,
	// never alone, so it is never condemned for the culprit's sake -- and for
	// the same reason a part may be retried on a worker it was stuck on in a
	// larger batch (that says nothing about the part itself).
	//
	// Machines, not endpoints (see workerid.go): two ports on one box failing on
	// a file are one sick machine, and a condemnation is the one timeout that is
	// struck even when the dispatch failed, so it must not be earned that way.
	keyOf := func(url string) string { return keyFor[url] }
	// memGroup is, for every endpoint whose machine is remembered as having
	// failed the canary clip, the key of the entry that remembers it
	// (canaryMemoryKey), taken once as the dispatch begins. A remembered machine
	// is one group for asking and forgetting whatever its workers answer as: a
	// port whose worker restarted under memory pressure answers as "u:" while
	// its sibling ports still answer as "m:", and asking them as two machines
	// would forget the machine on the word of the ports that happen to answer as
	// "m:". The bookkeeping that benches a machine (preBenched, pendingCanary)
	// is per group (groupOf); everything else stays per keyOf.
	//
	// An endpoint whose /health could not be read is in a group only if its own
	// key is remembered. One that is merely a known port of a remembered machine
	// is not asked: it is down, and its machine stays remembered for as long as it
	// is (hasUnheardPort) while the ports that answer take work.
	//
	// Only a port that reports the remembered machine's own id (an "m:" key the
	// dispatch resolved for it, by what it reports or by the machine its address
	// last reported: resolveMachine) is asked that machine's canary clip on its
	// behalf and can forget it. A port with a weak or unknown identity ("u:", "?")
	// is in a group only if the entry is its own (rk == its key): being listed
	// among a machine's ports is where the machine's memory was recorded, not
	// evidence that this worker is that machine (a refused probe ends the link, and
	// whatever answers there afterwards stands for itself), and its answer must not
	// clear the failure of a machine that never reported its id.
	memGroup := make(map[string]string)
	for _, ep := range ordered {
		k := keyOf(ep.URL)
		rk := canaryMemoryKey(k, ep.URL)
		if rk == "" || (!isStrongKey(k) && rk != k) {
			continue
		}
		if probeByURL[ep.URL].Probed || rk == k {
			memGroup[ep.URL] = rk
		}
	}
	groupOf := func(url string) string {
		if k := memGroup[url]; k != "" {
			return k
		}
		return keyOf(url)
	}
	soloStuck := make(map[string]map[string]string) // job -> machine key -> the endpoint it was stuck on alone there
	// soloOverran is the weaker record: a lone job's request outlived its
	// ceiling on a worker that, when asked, was NOT wedged (it answered /health
	// and was inside its own decode deadline), so the request most likely waited
	// behind another one. That is no evidence about the job, so it never counts
	// toward a verdict; it only keeps the job from being placed on the same
	// endpoint again straight away. Keyed by endpoint, not machine: what it
	// records is that endpoint's queue, and another port on the same box has a
	// queue of its own.
	soloOverran := make(map[string]map[string]bool)
	machines := make(map[string]bool, len(ordered))
	var soloLimit, faultLimit int
	// countLimits counts the machines (by the identity keyOf gives each endpoint,
	// so a machine's port that answers as "u:" is not a machine of its own) and
	// the endpoints the dispatch has, and derives from them the two limits that
	// need them: the machines a lone job's condemnation needs, and the endpoints
	// a faulted job can run out of. It is the one place that counts them, at the
	// start and again whenever ports are dropped.
	//
	// An endpoint whose identity is unknown (an unreadable /health at the start:
	// a "?" key) is never counted as a machine, with or without a wait: it is
	// evidence of none, and counting it made one box with a readable port and an
	// unreadable one into "two machines" for a condemnation. It still counts as
	// an endpoint (faultLimit). A pool with no known machine has soloLimit 0 and
	// condemns nothing (see the condemnation in the round loop).
	countLimits := func() {
		clear(machines)
		for _, ep := range ordered {
			if k := keyOf(ep.URL); !unknownKey(k) {
				machines[k] = true
			}
		}
		soloLimit = min(2, len(machines))
		faultLimit = min(2, len(ordered))
	}
	countLimits()
	// dropPorts takes ports out of this dispatch for good: they are refused or
	// another worker answers there (regate's re-probe after a wait found them
	// wedged, unreachable or changed hands), so the limits that count the pool's
	// machines and endpoints are counted again, or a lone job could never find the
	// second machine its condemnation needs and a faulted job could never run out
	// of workers. A port may have taken work earlier in the dispatch; what it
	// answered stays in the results. The stuck record of a job (soloStuck) keeps
	// the machines it was stuck on alone, dropped or not, so a machine dropped
	// mid-dispatch still counts toward that job's condemnation while the limit,
	// counted again, can fall to one machine: the verdict is about the file and
	// every machine that left it stuck, not about the ports that are left.
	dropPorts := func(urls map[string]bool) {
		ordered = slices.DeleteFunc(ordered, func(ep Endpoint) bool { return urls[ep.URL] })
		countLimits()
	}
	var units [][]string
	// followerOf is the journal's content dedup seen from here: a follower was
	// never sent (its audio is byte-identical to its leader's) and shares the
	// leader's fate, so it travels with the leader's unit and is condemned with
	// it. Without this a follower of a stuck leader would be re-sent on its own
	// as if it were an unrelated file, and wedge another worker.
	followersOf := make(map[string][]string)
	followerOf := make(map[string]string)
	registerFollowers := func(fs map[string][]string) {
		for leader, ids := range fs {
			followersOf[leader] = ids
			for _, f := range ids {
				followerOf[f] = leader
			}
		}
	}

	// lastErr is the most recent failure PER ENDPOINT. One error variable
	// overwritten in goroutine-drain order made the final classification (stuck,
	// which the ops answer by stopping, versus outage, which they wait out)
	// depend on which endpoint's result arrived last. An endpoint's entry is
	// removed by a clean response from it, so it also answers "has this
	// dispatch seen this endpoint fail, and not recover since?".
	lastErr := make(map[string]error)

	// Worker-fault bookkeeping (see ErrWorkerFault). A faulted response says the
	// worker may be unwell, not which clip is to blame, so its jobs are re-queued
	// in units of at most timeoutBreakerMin-1 jobs: a unit's answer is judged per
	// clip, because two timeouts cannot make a fault, so it always ends. The two
	// kinds of fault are handled differently:
	//
	//   - A response that was mostly timeouts or crashes (a timeout-majority
	//     fault) is as often the batch's own clips as the worker: a page whose
	//     every chunk is mostly genuine loopers faults on EVERY endpoint at once.
	//     So the endpoint, and every other endpoint on its machine, is
	//     soft-benched for the rest of THIS dispatch (isSick: it gets no new
	//     work, its strikes are withheld and the jobs re-queued) only while some
	//     endpoint is not sick; the faulted jobs go back in units to those
	//     endpoints. Once every endpoint is sick (the all-loopers page) the
	//     bench is lifted for all of them: units go to any endpoint and their
	//     per-clip strikes are believed, so loopers still converge. Excluding a
	//     faulted endpoint for good (an earlier version did) left such a page
	//     with nowhere to go, deferred forever; believing a sick endpoint, or
	//     another port of its machine, while a healthy one was idle (other
	//     earlier versions) let one swapping Mac give first strikes to healthy
	//     files, which the next night's routing then sent to the only other
	//     machine.
	//   - Any other fault (worker_misconfigured: the worker cannot decode at
	//     all; see WorkerFaultError.timeoutMajority) is about the endpoint
	//     only. faultedOn remembers, per job, every
	//     endpoint whose worker-level fault held it; a job is never offered to
	//     one of those again, nor to an endpoint that failed in this dispatch
	//     and has not answered cleanly since (lastErr). A job that has faulted on
	//     faultLimit workers, or that no worker is left to take, is DEFERRED: it
	//     gets a PathErrWorkerFault result, which every caller treats as no
	//     answer, never as a strike.
	//
	// faultedHere is the "plainly sick" rule, for those endpoint faults only: an
	// endpoint that could not decode at all in this dispatch is not believed
	// about timeouts or crashes either, however few, until it answers a
	// response with none of them.
	faultedOn := make(map[string]map[string]bool)
	faultedHere := make(map[string]bool)
	// sickHere is every endpoint that faulted in this dispatch, of either kind,
	// with its error in sickErr. sickMachine is every machine (keyOf) one of
	// whose endpoints had a timeout-majority fault: a Mac that is swapping, or
	// whose decoders jetsam is killing, is ill on every port it serves, and
	// re-queued units of one or two clips sent to a sibling port can never
	// fault (a fault needs two timeouts and a majority), so their strikes would
	// be believed. A worker_misconfigured fault stays with its own port (a bad
	// environment or a broken child is usually one port's). An endpoint of
	// unknown identity is its own machine (keyOf never groups two URLs it cannot
	// tell are one box). portSick is every endpoint with a worker_misconfigured
	// fault. None of them is cleared within the dispatch: a worker that
	// answered most of a chunk with timeouts is not trusted with fresh chunks
	// again until the next dispatch (each costs a chunk of per-clip deadlines
	// before it faults again), unless its machine answers the canary clip once
	// every endpoint is sick. See benched.
	sickHere := make(map[string]bool)
	sickMachine := make(map[string]bool)
	portSick := make(map[string]bool)
	sickErr := make(map[string]error)
	// preBenched is every machine that failed the canary clip in an earlier
	// dispatch and did not answer it when asked in this one, or has not been
	// asked yet (pendingCanary): benched for the whole dispatch, asked nothing
	// more once it has failed.
	preBenched := make(map[string]bool)
	// pendingCanary is every remembered machine (see preBenched) that has a
	// port in cooldown, so could not be asked yet, while another machine could
	// take the work. It is benched (preBenched) like the others, but it has been
	// asked nothing: it must not be reported as a machine whose canary failed.
	// resolvePending asks it, after its cooldown, when no other machine can work.
	pendingCanary := make(map[string]bool)
	isSick := func(url string) bool { return portSick[url] || sickMachine[keyOf(url)] || preBenched[groupOf(url)] }
	sickCount := func() int {
		n := 0
		for _, ep := range ordered {
			if isSick(ep.URL) {
				n++
			}
		}
		return n
	}
	allSick := func() bool { return sickCount() == len(ordered) }
	// canaryRan, canaryOK and canaryBusy are the canary verdicts (SF1 of
	// review 4 of #4182), per machine, cached for the rest of the dispatch:
	// each machine is asked once. Once every endpoint is sick there is no one
	// better to ask, but a page of genuine loopers and every machine being
	// sick at once look the same (every clip times out everywhere). So every
	// port of each sick machine that faulted is sent the canary clip
	// (runCanaries). A machine all of whose faulted ports answer it is
	// decoding: its bench is lifted and its per-clip strikes are believed, so
	// loopers still converge. One with a port that does not answer stays
	// benched, none of its strikes count, and it is remembered (canaryFailed)
	// so later dispatches ask it the canary before feeding it fresh chunks
	// (preflight). One whose canary could not get a request slot (canaryBusy)
	// is neither: it stays benched and is not remembered. A machine that
	// failed in an earlier dispatch and fails again in preflight is benched for
	// the whole dispatch (preBenched). When no machine answers, the dispatch
	// ends as ErrPoolUnavailable and its jobs are deferred; when some machine
	// was only busy, it ends in an error that defers them without counting
	// toward stopping the run. Believing every endpoint whenever all had
	// faulted (an earlier version) let a lone sick Mac, on a night llm1 was out
	// of the pool, give first strikes to a page of healthy files.
	canaryRan := make(map[string]bool)
	canaryOK := make(map[string]bool)
	canaryBusy := make(map[string]bool)
	// killedMachine is every machine with a timeout-majority fault that was
	// mostly decoder_killed answers (WorkerFaultError.killMajority). Its
	// decoder_killed answers are withheld for the rest of the dispatch even
	// after its canary passes (see killHeld below): a two-second clip cannot
	// show that a decoder survives a long one under memory pressure.
	killedMachine := make(map[string]bool)
	// benched: url takes no new work and its strikes are withheld. A port with
	// a worker_misconfigured fault is benched only while some endpoint is not
	// sick (it is in cooldown anyway, and its jobs never go back to it: see
	// faultedOn); a machine with a timeout fault until it answers the canary.
	//
	// A port that could not be read after a cooldown wait (unreadable: see regate)
	// takes no work either until a later probe reads it: nothing says it is still
	// the worker the dispatch knew.
	unreadable := make(map[string]bool)
	benched := func(url string) bool {
		if unreadable[url] {
			return true
		}
		if k := keyOf(url); preBenched[groupOf(url)] || (sickMachine[k] && !canaryOK[k]) {
			return true
		}
		return portSick[url] && !allSick()
	}
	distrusts := func(url string) bool { return faultedHere[url] || benched(url) }
	// canaryVerdict is one machine's answer to the canary clip: failed if any
	// port asked did not answer it, else busy if any port's canary could not
	// get a request slot, else ok once every port asked answered.
	type canaryVerdict int
	const (
		canaryNone canaryVerdict = iota // cancelled while asking: no verdict
		canaryPassed
		canaryWasBusy
		canaryFailedV
	)
	// askCanaries sends the canary clip, concurrently, to every port listed
	// for each machine, and returns each machine's verdict. why prefixes the
	// log lines. A cancelled op returns no verdicts.
	//
	// It also returns each machine's canaryGeneration as it was before the first
	// canary was sent: the generation forget is given, so a failure another
	// dispatch records while this one waits for the answer survives it.
	askCanaries := func(asks map[string][]string, why string) (map[string]canaryVerdict, map[string]uint64) {
		gens := make(map[string]uint64, len(asks))
		for key, ports := range asks {
			gens[key] = canaryGeneration(key, ports...)
		}
		var (
			wg     sync.WaitGroup
			mu     sync.Mutex
			passed = make(map[string]int)
			failed = make(map[string]bool)
			busy   = make(map[string]bool)
		)
		for key, ports := range asks {
			for _, port := range ports {
				wg.Go(func() {
					err := canary(ctx, probeByURL[port])
					mu.Lock()
					defer mu.Unlock()
					switch {
					case ctx.Err() != nil:
					case err == nil:
						passed[key]++
					case errors.Is(err, errCanarySlotBusy):
						busy[key] = true
						dispatchLog.Warn("%s and this port's canary clip could not get a request slot: no verdict on its machine, its timeouts do not count in this dispatch: url=%s err=%v",
							why, logger.SanitizeLogValue(port), err)
					default:
						failed[key] = true
						dispatchLog.Warn("%s and this port did not answer the canary clip: its machine is sick, its timeouts do not count in this dispatch: url=%s err=%v",
							why, logger.SanitizeLogValue(port), err)
					}
				})
			}
		}
		wg.Wait()
		out := make(map[string]canaryVerdict, len(asks))
		if ctx.Err() != nil {
			return out, gens
		}
		for key, ports := range asks {
			switch {
			case failed[key]:
				out[key] = canaryFailedV
			case busy[key]:
				out[key] = canaryWasBusy
			case passed[key] == len(ports):
				out[key] = canaryPassed
			}
		}
		return out, gens
	}
	// knownPorts is the URL of every endpoint this dispatch has seen as a port of
	// machine key: the ones it is using and the ones the gate refused.
	knownPorts := func(key string) []string {
		var out []string
		for _, ep := range ordered {
			if keyOf(ep.URL) == key || groupOf(ep.URL) == key {
				out = append(out, ep.URL)
			}
		}
		return append(out, refusedPorts(refused, key)...)
	}
	// preflight is nit 1 of review 5 of #4182: a machine that failed the
	// canary clip in an earlier dispatch, and has not answered one since, is
	// sent it on every port before it is given any work, instead of a fresh
	// chunk that costs a full chunk of per-clip deadlines before it faults
	// again. One that answers on every port is forgotten as sick and takes
	// work as normal (its later faults are judged afresh), unless a port of
	// the machine was refused at the gate and so has not answered (forget).
	// One that does not, or whose canary could not get a slot, is benched for
	// the whole dispatch (preBenched) and counts as a machine whose canary was
	// run, failed or busy, for the end-of-dispatch verdict below.
	//
	// A machine with a port in cooldown cannot be asked on every port yet, and
	// asking only the others would vouch for it on partial evidence (nit 1 of
	// review 6 of #4193). It is pending (pendingCanary): benched, and asked
	// nothing, until resolvePending asks it. That is at once when no other
	// machine could take the work, and otherwise only if every machine that
	// could has gone sick or out by the time the round loop finds nothing to
	// send to: the page does not wait for a machine the others can stand in for,
	// and a pool whose stand-in fails its own canary does not report "none
	// answered" about a machine it never asked (S2 of the review of #4193's
	// follow-up).
	//
	// settle applies the verdicts of askCanaries to the machines that were
	// asked. A machine that answered on every port takes work again (and is
	// forgotten, see forget); one that did not is benched and re-recorded as
	// failed, so a concurrent dispatch's answer cannot clear the failure this
	// one just saw; one whose canary was only busy is benched and no verdict.
	settle := func(verdicts map[string]canaryVerdict, gens map[string]uint64, asked map[string][]string) {
		for _, key := range slices.Sorted(maps.Keys(verdicts)) {
			switch verdicts[key] {
			case canaryPassed:
				forget(key, gens[key], asked[key])
				delete(pendingCanary, key)
				preBenched[key] = false
				dispatchLog.Info("a whisper machine that failed the canary clip in an earlier dispatch answered it on every port: it takes work again: machine=%s ports=%d",
					logger.SanitizeLogValue(key), len(asked[key]))
				continue
			case canaryFailedV:
				markCanaryFailed(key, knownPorts(key)...)
			case canaryWasBusy:
				canaryBusy[key] = true
			}
			delete(pendingCanary, key)
			preBenched[key], canaryRan[key] = true, true
		}
	}
	// standInAble reports whether some endpoint could take work right now: not in
	// cooldown, not benched, not one that has failed in this dispatch (lastErr: it
	// is not coming back to take the page), and one whose /health was read (an
	// endpoint the gate could not probe, and kept because the pool requires
	// nothing, is a guess, not a machine to bench a remembered one for).
	standInAble := func() bool {
		return slices.ContainsFunc(ordered, func(ep Endpoint) bool {
			return !endpointInCooldown(ep.URL) && !benched(ep.URL) && probeByURL[ep.URL].Probed && lastErr[ep.URL] == nil
		})
	}
	// waitingForProbe is whether a port needs a probe (needsProbe), is out of its
	// cooldown now and is not benched for another reason: it may take work once it
	// is probed, which the round loop's next pass does. Only such a port: not every
	// port out of cooldown, which includes the ones the work cannot go to and would
	// never end (see probed).
	waitingForProbe := func() bool {
		return slices.ContainsFunc(ordered, func(ep Endpoint) bool {
			return needsProbe(ep.URL) && !endpointInCooldown(ep.URL) && !benched(ep.URL)
		})
	}
	// cooldownWaited is how long the dispatch has slept for a cooldown to end
	// (sleepForCooldown), so that an error about cooldowns says what the
	// dispatch did and not what it might have.
	var cooldownWaited time.Duration
	sleepForCooldown := func(d time.Duration) error {
		start := time.Now()
		err := sleepCtx(ctx, d)
		cooldownWaited += time.Since(start)
		return err
	}
	// holdDrops makes regate queue the ports it drops (heldDrops) instead of
	// dropping them. runCanaries sets it: it runs in the middle of a round, after
	// the round's faults are marked and before its answers are judged, and
	// dropPorts counts the machines again (countLimits), which would change
	// soloLimit and the unknown ports a lone job's verdict is judged against
	// (untriedUnknownPort) for answers that came from the dispatch as it was when
	// they were sent. The round loop applies them at its next pass
	// (applyHeldDrops), before anything is chosen or sent.
	holdDrops := false
	heldDrops := make(map[string]bool)
	dropOrHold := func(gone map[string]bool) {
		if holdDrops {
			maps.Copy(heldDrops, gone)
			return
		}
		dropPorts(gone)
	}
	applyHeldDrops := func() {
		if len(heldDrops) > 0 {
			dropPorts(heldDrops)
			clear(heldDrops)
		}
	}
	// regated is what regate found out about the ports it probed again.
	type regated struct {
		// live is the ports that answer as the worker the dispatch knew: they may
		// be used (their health is the fresh one now).
		live map[string]bool
		// waiting is the ports whose /health could not be read (or whose decoder
		// is restarting) with pacing allowance left: neither used nor given up.
		waiting map[string]bool
		// gone is the ports dropped from the dispatch (dropPorts): another worker
		// answers there, or they are refused for good.
		gone map[string]bool
		// cancelled: the op ended during the probe, so nothing was learned.
		cancelled bool
	}
	// regate probes eps again after a wait and judges every port by one rule
	// (D60): a pool uses a port after a cooldown wait only if its identity then
	// equals the identity the dispatch froze for it (keyOf). The minutes of the wait
	// are long enough for a tunnel to come back from another Mac, and everything
	// the dispatch does with a port (the canary clip, the work, the strikes and the
	// condemnations stored with its answers, the bench) is done under the frozen
	// identity, so an answer must never be credited to a machine it did not come
	// from. what names the ports in the log.
	//
	//   - Another identity, in any direction ("?" to anything, "u:" to "m:", one
	//     machine for another, a worker that cannot report an id for one that
	//     could), or the same one from another kind of worker (reprobed): the port
	//     changed hands. It is refused (ChangedHands) and dropped. The next
	//     dispatch's gate keys it by what it reports.
	//   - A port whose /health cannot be read now, whatever it was before, is
	//     Transient: there is nothing to compare, so it is not used. It is waited
	//     for like a port whose decoder is restarting, then dropped.
	//   - Wedged, or not meant for the work: refused as the gate refuses it, and
	//     dropped.
	//
	// It is the one probe after a cooldown: resolvePending's for a remembered
	// machine's ports, and the round loop's (recheck) for every other port that
	// needs one (needsProbe: it was in a cooldown when last probed, or has failed
	// since), whether the dispatch slept for it (waitOutCooldown) or the cooldown
	// came and went while the dispatch did something else (a stand-in waited out
	// inside resolvePending, a round of work on other ports). A pool left with no
	// port is the caller's to end (poolEmptied: noEndpointError for the refusals the
	// waits made). The lone endpoint keeps a
	// worker that gained a machine id (dispatchSingle, reprobed): it resolves the
	// memory at call time and has no frozen group to disagree with.
	regate := func(eps []Endpoint, what string) regated {
		out := regated{live: make(map[string]bool), waiting: make(map[string]bool), gone: make(map[string]bool)}
		marks := markProbes(eps)
		kept, refusedNow := gate(eps, false)
		if ctx.Err() != nil {
			// Every port looks unreachable because nobody could ask.
			out.cancelled = true
			return out
		}
		// Probed: whatever it found, a port is not waiting for its probe any more
		// (one that could not be read is kept out of the work by unreadable).
		maps.Copy(probed, marks)
		for _, g := range kept {
			u := g.Endpoint.URL
			if !g.Probed {
				// Kept by the gate only because the pool requires nothing.
				r := newRefusal(g, "its /health could not be read after the wait, so what answers there cannot be told from the worker the dispatch knew")
				r.Transient = true
				refusedNow = append(refusedNow, r)
				continue
			}
			// Compared before reprobed adjusts the probe: what the worker reports
			// now against what the dispatch froze, with no allowance for a worker
			// that gained an id or a frozen identity that stood for no machine.
			fresh, ok := g.reprobed(keyOf(u), capableFor[u])
			if !ok || g.identityKey() != keyOf(u) {
				r := newRefusal(g, "answers as a different worker than the one the dispatch knew (its port changed hands during the wait), so it is sent no work in this dispatch")
				r.ChangedHands = true
				refused = append(refused, r)
				out.gone[u] = true
				dispatchLog.Warn("%s answers as a different worker after the wait, so it is dropped from this dispatch: url=%s was=%s group=%s now=%s",
					what, logger.SanitizeLogValue(u), logger.SanitizeLogValue(keyOf(u)), logger.SanitizeLogValue(groupOf(u)), logger.SanitizeLogValue(g.identityKey()))
				continue
			}
			probeByURL[u] = fresh
			out.live[u] = true
		}
		for _, r := range refusedNow {
			if keepWaitingFor(r, time.Now(), paceUntil) {
				out.waiting[r.URL] = true
				continue
			}
			refused = append(refused, r)
			out.gone[r.URL] = true
			dispatchLog.Warn("%s is refused after waiting, so it is not asked and not used: url=%s reason=%s",
				what, logger.SanitizeLogValue(r.URL), logger.SanitizeLogValue(r.Reason))
		}
		if len(out.gone) > 0 {
			dropOrHold(out.gone)
		}
		return out
	}
	// resolvePending asks the pending machines the canary clip once none of
	// their ports is in cooldown, and returns when none is pending any more or
	// another endpoint could take work again (it stays pending: the round loop
	// calls this again only when nothing can work). It waits the cooldown out,
	// bounded by the same pacing allowance as the round loop's paceWait
	// (paceUntil), in steps of poolPaceInterval. A cooldown that outlasts the
	// allowance is no verdict, exactly as a canary that could not get a request
	// slot is: the machine is benched and counted busy, not remembered afresh,
	// and the dispatch defers without counting toward the ops' stop limits. The
	// error is for a dispatch left with nothing to send to. A cancelled op
	// returns nil with the machines still benched; the round loop returns the
	// cancellation.
	//
	// The ports are probed again once the wait is over (regate; S4 of the review
	// of #4193's follow-up): minutes passed since the gate, and a port that
	// wedged or entered a decoder fault window since would fail the canary because
	// of that, which would be recorded as a sick machine. A port that is refused
	// now is judged as the gate would have judged it: stuck or not meant for the
	// work, it is dropped from the dispatch (and, with nothing left, the dispatch
	// ends as ErrWorkerStuck or the capability error); unreachable (a /health that
	// cannot be read, whatever the port was before) or restarting its decoder, it
	// is waited for like a cooldown, then dropped. A port whose identity is not the
	// one the dispatch froze for it is dropped as changed hands, never asked the
	// canary clip. A machine with a port still to be waited for is asked on none
	// of them, and a machine is never forgotten while a port of it is refused
	// (forget).
	//
	// force is for a dispatch whose round loop has endpoints that could take work
	// but not THESE jobs (a lone stuck job that has already stuck on the stand-in
	// alone): the pending machines are asked although a stand-in is able, and
	// resolvePending returns only with none pending, an error, or a cancelled op.
	resolvePending := func(force bool) error {
		start := time.Now()
		waitLogged := false
		for len(pendingCanary) > 0 && ctx.Err() == nil {
			// A stand-in whose cooldown the wait below ends is probed again before it
			// takes work (needsProbe): the round loop does it before it looks for
			// ports to send to.
			portsOf := make(map[string][]string)
			for _, ep := range ordered {
				if k := groupOf(ep.URL); pendingCanary[k] {
					portsOf[k] = append(portsOf[k], ep.URL)
				}
			}
			for _, k := range slices.Sorted(maps.Keys(pendingCanary)) {
				// Forgotten means no port of the machine is remembered under ANY
				// key any more, not that the key this dispatch knew it by is gone:
				// a concurrent dispatch's gate moves the memory to a worker's new
				// identity while this one waits (B-1 of the re-review of #4193),
				// and that machine is still remembered.
				if ports := portsOf[k]; len(ports) > 0 && !slices.ContainsFunc(ports, func(u string) bool { return endpointRemembered(keyOf(u), u) }) {
					// Another dispatch asked it meanwhile and it answered: no
					// need to ask again, and nothing to bench it for.
					delete(pendingCanary, k)
					delete(portsOf, k)
					preBenched[k] = false
				}
			}
			if len(pendingCanary) == 0 || (!force && standInAble()) {
				return nil
			}
			var ready []string
			for _, k := range slices.Sorted(maps.Keys(pendingCanary)) {
				if len(portsOf[k]) == 0 {
					delete(pendingCanary, k) // every port of it has been dropped
					continue
				}
				if !slices.ContainsFunc(portsOf[k], endpointInCooldown) {
					ready = append(ready, k)
				}
			}
			if len(ready) > 0 {
				urlKey := make(map[string]string)
				var eps []Endpoint
				for _, ep := range ordered {
					if k := groupOf(ep.URL); slices.Contains(ready, k) {
						urlKey[ep.URL] = k
						eps = append(eps, ep)
					}
				}
				re := regate(eps, "a whisper port of a machine that failed the canary clip in an earlier dispatch")
				if re.cancelled {
					// Cancelled during the probe: every port looks unreachable
					// because nobody could ask, which is no evidence about it. The
					// round loop returns the cancellation.
					return nil
				}
				answers := re.live
				// A port read as live is no longer one that could not be read
				// (recheck does the same for the ports it probes).
				for u := range re.live {
					delete(unreadable, u)
				}
				waiting := make(map[string]bool)
				for u := range re.waiting {
					waiting[urlKey[u]] = true
				}
				if len(re.gone) > 0 && len(ordered) == 0 {
					return poolEmptied()
				}
				asks := make(map[string][]string)
				for _, k := range ready {
					if waiting[k] {
						continue
					}
					for _, u := range portsOf[k] {
						if answers[u] {
							asks[k] = append(asks[k], u)
						}
					}
					if len(asks[k]) == 0 {
						delete(pendingCanary, k) // nothing left of it to ask
					}
				}
				verdicts, gens := askCanaries(asks, "a whisper machine that failed the canary clip in an earlier dispatch was asked it again")
				settle(verdicts, gens, asks)
				if len(waiting) > 0 && ctx.Err() == nil {
					if sleepCtx(ctx, max(min(poolPaceInterval, time.Until(paceUntil)), time.Millisecond)) != nil {
						return nil
					}
				}
				continue
			}
			// Nothing is ready: wait for the first thing that changes that. A
			// pending machine can be asked when its LAST port is out of cooldown
			// (an earlier port leaves it just as unaskable), so its time is the
			// latest end among its ports, and the wait is for the earliest such
			// machine. Unless force is set, so is the end of a cooldown that keeps
			// another endpoint from taking work (a stand-in that has not failed
			// in this dispatch, as standInAble judges it), which needs only its
			// own end.
			var target time.Time
			earlier := func(t time.Time) {
				if !t.IsZero() && (target.IsZero() || t.Before(target)) {
					target = t
				}
			}
			for _, k := range slices.Sorted(maps.Keys(pendingCanary)) {
				earlier(latestCooldownEnd(portsOf[k]))
			}
			if !force {
				for _, ep := range ordered {
					if !pendingCanary[groupOf(ep.URL)] && !benched(ep.URL) && probeByURL[ep.URL].Probed && lastErr[ep.URL] == nil {
						earlier(earliestCooldownEnd([]string{ep.URL}))
					}
				}
			}
			wait, act := decideCooldownWait(time.Now(), paceUntil, target)
			switch act {
			case cooldownClear:
				continue // the last cooldown ended between the checks
			case cooldownDefer:
				for _, k := range slices.Sorted(maps.Keys(pendingCanary)) {
					preBenched[k], canaryRan[k], canaryBusy[k] = true, true, true
					delete(pendingCanary, k)
					dispatchLog.Warn("a whisper machine that failed the canary clip in an earlier dispatch has a port in cooldown that outlasts the %s pacing allowance (waited %s): no verdict on it, the jobs are deferred: machine=%s",
						poolPaceMax, time.Since(start).Round(time.Millisecond), logger.SanitizeLogValue(k))
				}
				return nil
			}
			if !waitLogged {
				waitLogged = true
				dispatchLog.Warn("no whisper machine that is not remembered as sick can take work and a remembered one has a port in cooldown; waiting for the cooldown to end before asking it: machines=%d",
					len(pendingCanary))
			}
			if sleepForCooldown(min(wait, poolPaceInterval)) != nil {
				return nil
			}
		}
		return nil
	}
	// recheck probes again the ports that were waited for and are out of cooldown
	// now (regate), before anything is sent to them. The gate that gave a port its
	// identity ran minutes ago, and the same rule applies as after a remembered
	// machine's wait: a port is used only if its identity is still the one the
	// dispatch froze for it (D60, D62). One /health per port per wait. A port that
	// could not be read is kept out of the work (unreadable) and, with retry set,
	// read again, until the pacing allowance is spent; retry is for the wait's own
	// steps, so a round of work is never held up by a probe of a port that is not
	// taking part in it.
	//
	// It returns the error that ends the dispatch: the op was cancelled, or every
	// port was dropped (noEndpointError).
	recheck := func(retry bool) error {
		var eps []Endpoint
		for _, ep := range ordered {
			if (needsProbe(ep.URL) || (retry && unreadable[ep.URL])) && !endpointInCooldown(ep.URL) {
				eps = append(eps, ep)
			}
		}
		if len(eps) == 0 {
			return nil
		}
		re := regate(eps, "a whisper port that was in cooldown")
		if re.cancelled {
			return classifyTransport(endpointURLs(ordered), ctx.Err())
		}
		for _, ep := range eps {
			if re.waiting[ep.URL] {
				unreadable[ep.URL] = true
			} else {
				delete(unreadable, ep.URL)
			}
		}
		if len(re.gone) > 0 && len(ordered) == 0 {
			return poolEmptied()
		}
		return nil
	}
	// waitOutCooldown is the round loop's wait for a port to come out of a cooldown
	// an earlier dispatch put it in: one step of at most poolPaceInterval (so a
	// cooldown that is cleared early is noticed, as resolvePending does), then the
	// ports that are out of it are probed again (recheck). A port that was never
	// waited for is not.
	waitOutCooldown := func(wait time.Duration) error {
		if err := sleepForCooldown(min(wait, poolPaceInterval)); err != nil {
			return classifyTransport(endpointURLs(ordered), err)
		}
		return recheck(true)
	}
	// pace is how long the round loop waits when nothing can take work, and
	// whether it does: paceWait for a cooldown this dispatch has no evidence
	// against, or one more step for a port that could not be read after a wait,
	// whose retry is what drops it once the allowance is spent.
	//
	// A port whose machine is unknown (a "?" key) is never waited for: after the
	// wait regate can only drop it (read now, it answers as a different worker than
	// the nothing it was frozen as; still unreadable, it is not used), so waiting
	// the pacing allowance for it ends with the same work undone.
	pace := func() (time.Duration, bool) {
		known := slices.DeleteFunc(slices.Clone(ordered), func(ep Endpoint) bool { return unknownKey(keyOf(ep.URL)) })
		if wait, ok := paceWait(known, lastErr, paceUntil); ok {
			return wait, true
		}
		if slices.ContainsFunc(known, func(ep Endpoint) bool { return unreadable[ep.URL] && !endpointInCooldown(ep.URL) }) {
			return max(min(poolPaceInterval, time.Until(paceUntil)), time.Millisecond), true
		}
		return 0, false
	}
	// runCanaries sends the canary clip, concurrently, to every port that
	// faulted in this dispatch of every sick machine not yet asked (SF2 of
	// review 5 of #4182: sickness can be one process's, so a sibling port's
	// answer vouches for nothing). A machine passes only when every one of
	// those ports answers. A machine with a faulted port that is misconfigured
	// or in cooldown cannot be vouched for and is asked nothing; with no canary
	// evidence against it, it is not remembered as sick either. A machine that
	// fails is remembered (canaryFailed), so later dispatches ask it the canary
	// before they give it work (nit 1 of review 5).
	//
	// A port that needs a probe (needsProbe: it was in a cooldown when last probed,
	// or has failed since, however briefly the cooldown lasted) and is out of its
	// cooldown now is probed again first (recheck, D60, D62): the clip is a request
	// to the worker the dispatch froze the port for, and its verdict is stored under
	// that identity. A port that changed hands is dropped and never asked, one whose
	// /health cannot be read is not asked either (unreadable). The dropping waits
	// for the round loop's next pass (holdDrops): the round's answers are judged
	// against the dispatch as it was when they were sent, and a pool left with no
	// port is ended there (applyHeldDrops). A cancelled op is no evidence about any
	// machine: it asks none and returns the cancellation.
	runCanaries := func() error {
		if ctx.Err() != nil {
			return classifyTransport(endpointURLs(ordered), ctx.Err())
		}
		holdDrops = true
		err := recheck(false)
		holdDrops = false
		if err != nil {
			return err
		}
		asks := make(map[string][]string)
		for _, key := range slices.Sorted(maps.Keys(sickMachine)) {
			if canaryRan[key] {
				continue
			}
			canaryRan[key] = true
			var ports []string
			unaskable := false
			for _, ep := range ordered {
				if keyOf(ep.URL) != key || !sickHere[ep.URL] || heldDrops[ep.URL] {
					continue
				}
				if portSick[ep.URL] || endpointInCooldown(ep.URL) || unreadable[ep.URL] {
					unaskable = true
					continue
				}
				ports = append(ports, ep.URL)
			}
			if unaskable || len(ports) == 0 {
				dispatchLog.Warn("every whisper endpoint faulted; a port of this machine that faulted cannot be sent the canary clip, so its timeouts do not count: machine=%s",
					logger.SanitizeLogValue(key))
				continue
			}
			asks[key] = ports
		}
		verdicts, gens := askCanaries(asks, "every whisper endpoint faulted")
		for key, v := range verdicts {
			switch v {
			case canaryFailedV:
				markCanaryFailed(key, knownPorts(key)...)
			case canaryWasBusy:
				canaryBusy[key] = true
			case canaryPassed:
				canaryOK[key] = true
				forget(key, gens[key], asks[key])
				dispatchLog.Info("every whisper endpoint faulted and every faulted port of this machine answered the canary clip: its timeouts are about the clips and count: machine=%s ports=%d",
					logger.SanitizeLogValue(key), len(asks[key]))
			}
		}
		return nil
	}
	preflight := func() error {
		// A port whose cooldown ended while the gate probed (or since) is probed
		// again before it is asked anything, like every port that needs a probe
		// (needsProbe; D60, D62): the clip is asked of the worker the dispatch froze the port
		// for, and a canary verdict is stored under that identity. One that changed
		// hands is dropped here and never asked; one that cannot be read is
		// unreadable, so its machine is pending like one with a port in cooldown.
		if err := recheck(false); err != nil {
			return err
		}
		asks := make(map[string][]string)
		unaskable := make(map[string]bool)
		for _, ep := range ordered {
			k := memGroup[ep.URL] // set only for an endpoint that endpointRemembered
			switch {
			case k == "":
			case endpointInCooldown(ep.URL) || unreadable[ep.URL]:
				unaskable[k] = true
			default:
				asks[k] = append(asks[k], ep.URL)
			}
		}
		if (len(asks) == 0 && len(unaskable) == 0) || ctx.Err() != nil {
			return nil
		}
		// A machine with a port that cannot be asked yet is asked on none.
		for k := range unaskable {
			delete(asks, k)
			pendingCanary[k], preBenched[k] = true, true
		}
		verdicts, gens := askCanaries(asks, "a whisper machine that failed the canary clip in an earlier dispatch was asked it again")
		settle(verdicts, gens, asks)
		return resolvePending(false)
	}
	isFaultJob := func(id string) bool { return len(faultedOn[id]) > 0 }
	// killedOn remembers, per job, every machine whose decoder_killed answer
	// for it was withheld (killHeld). The job is never offered to any port of
	// such a machine again: a sibling port is the same memory pressure.
	killedOn := make(map[string]map[string]bool)
	faultCandidate := func(id, url string) bool {
		return !faultedOn[id][url] && !killedOn[id][keyOf(url)] && lastErr[url] == nil
	}
	deferJob := func(id string) {
		why := faultDeferral(len(faultedOn[id]))
		settled := 0
		for _, cid := range append([]string{id}, followersOf[id]...) {
			if _, ok := remaining[cid]; !ok {
				continue
			}
			results[cid] = why
			delete(remaining, cid)
			settled++
		}
		bump(settled)
	}
	// requeueUnits puts jobs back in units too small to fault on timeouts.
	requeueUnits := func(ids []string) {
		ids = slices.DeleteFunc(slices.Clone(ids), func(id string) bool { _, ok := remaining[id]; return !ok })
		slices.Sort(ids)
		for part := range slices.Chunk(ids, timeoutBreakerMin-1) {
			units = append(units, part)
		}
	}
	// requeueFaulted is requeueUnits for a worker-level fault: the jobs also
	// remember the endpoint, so they are never offered to it again.
	requeueFaulted := func(ids []string, url string) {
		for _, id := range ids {
			if _, ok := remaining[id]; !ok {
				continue
			}
			if faultedOn[id] == nil {
				faultedOn[id] = make(map[string]bool)
			}
			faultedOn[id][url] = true
		}
		requeueUnits(ids)
	}
	// identitiesOf is every identity a stored strike or condemnation can name the
	// worker at url by, or none when its identity is unknown (it was not probed):
	// the machine the dispatch resolved for it (keyOf, D55), and the "u:" name of
	// its address. The raw identity its probe reports is one of those two: a
	// machine id is what keyOf is, and a "u:" is the address. A run that saw the
	// worker answer as "u:" stored that name for a machine it answers for with an
	// id now, and a run that stored the machine's name must match a worker that
	// answers as "u:" now. A job is steered away from a machine however its strike
	// named it, so steerAway and a condemnation, which names keyOf, agree (S-D of
	// the review of f241ec20a). A stored "u:" id made from ANOTHER port's URL
	// reaches here through avoidOf, which adds the machine of that port to the
	// avoid set (weakOwner).
	identitiesOf := func(url string) []string {
		if !probeByURL[url].Probed {
			return nil
		}
		ids := []string{"u:" + shortHash(url)}
		k := keyOf(url)
		if !strings.HasPrefix(k, "?") && !slices.Contains(ids, k) {
			ids = append(ids, k)
		}
		// A weak key does not say whose port this is: a port of a machine that
		// restarted while a probe refused it answers as "u:" now, and a job a
		// strike by that machine's id avoids must still be moved off it (S-1 of
		// review 6 of #4201). The machine a probe last attributed to the URL
		// is named too. Naming a machine that is not behind the port any more only
		// steers a job further than it needed to go (steerAway leaves a job where
		// it is when nothing better exists).
		if !isStrongKey(k) {
			if m := attributedMachine(url); m != "" && !slices.Contains(ids, m) {
				ids = append(ids, m)
			}
		}
		return ids
	}
	// steerAway is R4 of the #4182 review. A job whose file already carries a
	// timeout strike can only be settled by a strike from a machine that has
	// not struck it, but allocation fills the lowest priority tier first, so a
	// lone job went to the same priority-1 worker every night and the file was
	// re-sent forever without ever becoming permanent. After allocation, each
	// such job sitting on an endpoint of a machine in its avoid set moves to the
	// least-loaded endpoint in eps on a machine known (probed) to be different.
	// With no such endpoint it stays where it is: the worst outcome is a strike
	// the store does not count, never a job stranded with nowhere to go. The
	// moved job's byte-identical followers move with it.
	avoidOf := make(map[string]map[string]bool, len(opts.avoid))
	for id, v := range opts.avoid {
		if _, ok := jobs[id]; ok {
			if set := splitWorkers(v); len(set) > 0 {
				avoidOf[id] = set
			}
		}
	}
	// A stored "u:" id is the hash of ONE port's URL, written while the machine
	// behind that port answered as a weak identity (D55), and it names that
	// machine, not the port: when the machine answers with its id on every port
	// now, the strike must steer a job off all of them, not only off the port the
	// hash was made from (S1 of the D58 review). So each stored "u:" id of a
	// configured endpoint is resolved to the machine of that endpoint, the one
	// this dispatch resolved for it (keyOf) or, for an endpoint not in the
	// dispatch, the last machine a probe attributed to its URL, and the machine's
	// own key joins the avoid set (identitiesOf names every endpoint of the
	// machine by it). A wrong match only steers a job further away than it
	// needed to go, which is safe: steerAway never strands a job.
	weakOwner := make(map[string]string, len(endpoints))
	for _, ep := range endpoints {
		m, gated := keyFor[ep.URL]
		if !gated || !isStrongKey(m) {
			// An endpoint the dispatch did not gate, or one the gate could only
			// name by a weak key (it was refused while its machine restarted and
			// answers as "u:" now), is the machine a probe last attributed to it.
			if a := attributedMachine(ep.URL); a != "" {
				m = a
			}
		}
		if isStrongKey(m) {
			weakOwner["u:"+shortHash(ep.URL)] = m
		}
	}
	for _, set := range avoidOf {
		var machines []string
		for id := range set {
			if m := weakOwner[id]; m != "" {
				machines = append(machines, m)
			}
		}
		for _, m := range machines {
			set[m] = true
		}
	}
	steerAway := func(eps []Endpoint, asg []map[string]J) {
		if len(avoidOf) == 0 {
			return
		}
		for i, ep := range eps {
			here := identitiesOf(ep.URL)
			for _, id := range slices.Sorted(maps.Keys(asg[i])) {
				set := avoidOf[id]
				if _, still := asg[i][id]; !still || len(here) == 0 || !anyIn(set, here) {
					continue // (not still: a follower that already moved with its leader)
				}
				best := -1
				for k, alt := range eps {
					if wids := identitiesOf(alt.URL); k == i || len(wids) == 0 || anyIn(set, wids) {
						continue
					}
					if best < 0 || len(asg[k]) < len(asg[best]) {
						best = k
					}
				}
				if best < 0 {
					continue
				}
				if asg[best] == nil {
					asg[best] = make(map[string]J)
				}
				for _, cid := range append([]string{id}, followersOf[id]...) {
					if j, ok := asg[i][cid]; ok {
						asg[best][cid] = j
						delete(asg[i], cid)
					}
				}
				dispatchLog.Info("routing a job away from the machine that already struck it: job=%s from=%s to=%s",
					logger.SanitizeLogValue(id), logger.SanitizeLogValue(ep.URL), logger.SanitizeLogValue(eps[best].URL))
			}
		}
	}

	// avoids reports whether url is on a machine some job in live should be
	// routed away from (S2 of review 3): one in its avoid set, or one whose
	// identity is unknown while the job carries an avoid set. The unit loop
	// tries every other endpoint first, the same preference steerAway applies
	// to normal allocation.
	avoids := func(live []string, url string) bool {
		wids := identitiesOf(url)
		for _, id := range live {
			if set := avoidOf[id]; set != nil && (len(wids) == 0 || anyIn(set, wids)) {
				return true
			}
		}
		return false
	}

	// untriedUnknownPort reports whether some port of the dispatch whose identity is
	// unknown (a "?" key: its /health could not be read at the start) has not been
	// shown to hang job id alone (soloStuck). A request that only outlived its
	// ceiling there (soloOverran) is no evidence about the job, so it does not make
	// the port tried: the verdict must not depend on whether the unknown port
	// overran before or after the known machine hung.
	untriedUnknownPort := func(id string) bool {
		return slices.ContainsFunc(ordered, func(ep Endpoint) bool {
			k := keyOf(ep.URL)
			return unknownKey(k) && soloStuck[id][k] == ""
		})
	}

	// canTake reports whether a split part may go to url.
	canTake := func(live []string, url string) bool {
		if len(live) == 1 && (soloStuck[live[0]][keyOf(url)] != "" || soloOverran[live[0]][url]) {
			return false
		}
		for _, id := range live {
			if isFaultJob(id) && !faultCandidate(id, url) {
				return false
			}
		}
		return true
	}

	if err := preflight(); err != nil {
		return results, err
	}
	for len(remaining) > 0 {
		if err := ctx.Err(); err != nil {
			return results, classifyTransport(endpointURLs(ordered), err)
		}
		// The ports the last round's canary probes found changed hands, dropped now
		// that its answers are judged (holdDrops).
		if len(heldDrops) > 0 {
			applyHeldDrops()
			if len(ordered) == 0 {
				return results, poolEmptied()
			}
		}
		if opts.atRoundStart != nil {
			opts.atRoundStart(endpointURLs(ordered), soloLimit)
		}
		// Defer every faulted job that has run out of workers. Before the
		// cooldown check, so jobs that have nowhere left to go cannot hold the
		// dispatch open, or turn it into "all endpoints in cooldown".
		for _, id := range slices.Sorted(maps.Keys(remaining)) {
			if _, ok := remaining[id]; !ok || !isFaultJob(id) {
				continue
			}
			if len(faultedOn[id]) >= faultLimit || !slices.ContainsFunc(ordered, func(ep Endpoint) bool { return faultCandidate(id, ep.URL) }) {
				dispatchLog.Warn("deferring a job only faulted workers answered: job=%s faulted_on=%d", logger.SanitizeLogValue(id), len(faultedOn[id]))
				deferJob(id)
			}
		}
		if len(remaining) == 0 {
			break
		}
		if err := recheck(false); err != nil {
			return results, err
		}
		// healthy is every endpoint that may take work this round: not in
		// cooldown, not soft-benched for a fault in this dispatch (see benched),
		// and probed again since it last failed or was in a cooldown (needsProbe:
		// only a port whose cooldown ended, or that failed, after the recheck above
		// is still waiting for its probe).
		var healthy []Endpoint
		for _, ep := range ordered {
			if endpointInCooldown(ep.URL) {
				continue
			}
			if !needsProbe(ep.URL) && !benched(ep.URL) {
				healthy = append(healthy, ep)
			}
		}
		if len(healthy) == 0 && len(pendingCanary) > 0 {
			// Nothing can take work, and a remembered machine that was set aside
			// because another one could has never been asked: ask it (after its
			// cooldown) before anything is concluded about the pool. It leaves
			// pendingCanary on every path that does not end the dispatch, so this
			// does not repeat.
			if err := resolvePending(false); err != nil {
				return results, err
			}
			continue
		}
		if len(healthy) == 0 && allSick() && len(canaryRan) > 0 && len(canaryOK) == 0 {
			if len(canaryBusy) > 0 {
				// No machine answered the canary clip, and at least one could
				// not be asked because its request slots stayed taken by other
				// dispatches: that machine may be sound. No verdict on the pool
				// either, so the error is neither ErrPoolUnavailable nor a
				// recognized network failure: the ops defer these jobs and go on
				// without counting the page toward stopping the run.
				return results, classifyTransport(endpointURLs(ordered), fmt.Errorf(
					"%w: every whisper endpoint faulted in this dispatch or failed the canary clip in an earlier one, no machine answered it and %d machine(s) could not be sent it, so none of their timeouts count and the jobs are deferred",
					errCanarySlotBusy, len(canaryBusy)))
			}
			// Every machine faulted and none answered the canary clip: no one
			// is left whose timeouts mean anything. Waiting would not change
			// that within the dispatch; the ops wait out ErrPoolUnavailable.
			return results, classifyTransport(endpointURLs(ordered), fmt.Errorf(
				"%w: every whisper endpoint faulted in this dispatch or failed the canary clip in an earlier one, and none of the %d machine(s) answered the canary clip, so none of their timeouts count",
				ErrPoolUnavailable, len(canaryRan)))
		}
		if len(healthy) == 0 {
			if opts.beforePace != nil {
				opts.beforePace()
			}
			if wait, ok := pace(); ok {
				if err := waitOutCooldown(wait); err != nil {
					return results, err
				}
				continue
			}
			if slices.ContainsFunc(ordered, func(ep Endpoint) bool { return !endpointInCooldown(ep.URL) && !benched(ep.URL) }) {
				// A port came out of its cooldown after the healthy list was made
				// (or is out of it but waiting for its probe: needsProbe), so the pool is
				// not out of ports and has waited for nothing: look again, as
				// resolvePending does when the last cooldown ended between its checks.
				// The same two tests as healthy's, so this does not repeat for a port
				// that is not usable.
				continue
			}
			if len(lastErr) == 0 {
				if slices.ContainsFunc(refused[refusedAtGate:], func(r gpuRefusal) bool { return r.ChangedHands }) &&
					!slices.ContainsFunc(ordered, func(ep Endpoint) bool { return !unknownKey(keyOf(ep.URL)) }) {
					// A port changed hands while the dispatch waited (it was dropped),
					// and what is left is only ports it does not wait for (a "?" port:
					// see pace) that are in cooldown. Waiting for those too would have
					// dropped them, as it drops every port it cannot compare, and ended
					// as the pool emptied: say that, not an outage (S1 of the review of
					// SF1: ErrPoolUnavailable is counted by the ops, a port that changed
					// hands is not). Only then: a port refused for another reason (it
					// wedged during the wait) beside a known port that is merely in a
					// long cooldown is an outage of the pool, not an emptied one, and
					// the "?" ports are not refusals of their own: judged as the pool
					// emptied they would be named capability refusals.
					return results, poolEmptied()
				}
				return results, classifyTransport(endpointURLs(ordered), fmt.Errorf(
					"%w: every whisper endpoint that has not faulted in this dispatch is in cooldown and no wait is left to make for any of them (%d of %d faulted; slept %s for a cooldown to end in this dispatch)",
					ErrPoolUnavailable, sickCount(), len(ordered), cooldownWaited.Round(time.Millisecond)))
			}
			// The endpoints that faulted are not asked again while one that has
			// not faulted might come back: their timeouts are not believed.
			errs := maps.Clone(lastErr)
			for url, e := range sickErr {
				if errs[url] == nil {
					errs[url] = e
				}
			}
			return results, classifyTransport(endpointURLs(ordered), poolFailure(errs,
				fmt.Errorf("all %d whisper endpoints in cooldown", len(ordered))))
		}

		var (
			assignments []map[string]J
			unitOf      = make(map[int][]string)
		)
		if len(units) > 0 {
			// Split parts run before normal allocation, one part per healthy
			// endpoint. A lone stuck job avoids the endpoints it was already
			// stuck on alone, so its verdict needs soloLimit DIFFERENT workers; a
			// faulted job avoids every endpoint it faulted on.
			assignments = make([]map[string]J, len(healthy))
			var left [][]string
			placedAny := false
			inUnit := make(map[string]bool)
			for _, u := range units {
				live := slices.DeleteFunc(slices.Clone(u), func(id string) bool { _, ok := remaining[id]; return !ok })
				if len(live) == 0 {
					continue
				}
				for _, id := range live {
					inUnit[id] = true
				}
				placed := false
				// Pass 0 skips the machines the unit's files were already struck
				// by; pass 1 takes any endpoint canTake allows.
				for pass := 0; pass < 2 && !placed; pass++ {
					for idx, ep := range healthy {
						if assignments[idx] != nil || !canTake(live, ep.URL) || (pass == 0 && avoids(live, ep.URL)) {
							continue
						}
						sub := make(map[string]J, len(live))
						for _, id := range live {
							sub[id] = remaining[id]
							for _, f := range followersOf[id] {
								if fj, ok := remaining[f]; ok {
									sub[f] = fj
								}
							}
						}
						assignments[idx], unitOf[idx], placed, placedAny = sub, live, true, true
						break
					}
				}
				if !placed {
					left = append(left, live)
				}
			}
			units = left

			// Jobs outside every split part are ordinary work. Sending only the
			// parts left every other endpoint idle for as long as a part ran, and
			// a part can run for a full request ceiling; hand the free jobs to the
			// endpoints no part was placed on. A follower rides with its leader,
			// so it is free only once the leader is no longer waiting.
			free := make(map[string]J)
			for id, j := range remaining {
				if inUnit[id] {
					continue
				}
				if leader, ok := followerOf[id]; ok {
					if _, waiting := remaining[leader]; waiting {
						continue
					}
				}
				free[id] = j
			}
			var idle []Endpoint
			var idleAt []int
			for idx, ep := range healthy {
				if assignments[idx] == nil {
					idle = append(idle, ep)
					idleAt = append(idleAt, idx)
				}
			}
			if len(free) > 0 && len(idle) > 0 {
				idleAsg := allocateJobs(idle, free)
				steerAway(idle, idleAsg)
				for k, sub := range idleAsg {
					if len(sub) > 0 {
						// A leader that was never part of a stuck request (it sat
						// in a later chunk) can still have followers: send them in
						// the same request, or the leader succeeds alone, nothing
						// fans its answer out, and the follower is decoded again
						// next round.
						for id := range maps.Clone(sub) {
							for _, f := range followersOf[id] {
								if fj, ok := remaining[f]; ok {
									sub[f] = fj
								}
							}
						}
						assignments[idleAt[k]] = sub
						placedAny = true
					}
				}
			}
			if !placedAny {
				if len(pendingCanary) > 0 {
					// Nothing could be placed this round although an endpoint is
					// able to take work: it cannot take THESE parts (a lone stuck
					// job that has already stuck on it alone, or one that
					// faulted there). A remembered machine that was set aside for
					// it has never been asked, and is the other place they could
					// go: ask it before concluding that nothing can take them
					// (S-2 of the re-review of #4193). It leaves pendingCanary on
					// every path that does not end the dispatch, so this does not
					// repeat.
					if err := resolvePending(true); err != nil {
						return results, err
					}
					continue
				}
				if opts.beforePlacedPace != nil {
					opts.beforePlacedPace()
				}
				// A port that needs a probe and is out of its cooldown now (it came out
				// while recheck's probes were in flight, so healthy was made without
				// it) may take the parts once it is probed: look again.
				if waitingForProbe() {
					continue
				}
				if opts.afterPlacedCheck != nil {
					opts.afterPlacedCheck()
				}
				// An endpoint benched by an earlier dispatch may still take the
				// waiting parts once its cooldown ends: wait for it rather than
				// give up.
				if wait, ok := pace(); ok {
					if err := waitOutCooldown(wait); err != nil {
						return results, err
					}
					continue
				}
				// A cooldown that ended after the look above and before pace made
				// its list is not the end of the ports either: pace finds nothing to
				// wait for, and the parts are not stuck on a port nobody tried (the
				// same look as the one after the round loop's pace, for the same
				// reason).
				if waitingForProbe() {
					continue
				}
				// A faulted job that no endpoint can take now is deferred; the
				// stuck parts left after that are what the error below is about.
				deferred := false
				for _, u := range units {
					for _, id := range u {
						if _, ok := remaining[id]; ok && isFaultJob(id) {
							deferJob(id)
							deferred = true
						}
					}
				}
				if deferred {
					continue
				}
				return results, classifyTransport(endpointURLs(ordered), poolFailure(lastErr,
					fmt.Errorf("%w: no healthy endpoint is left that these split jobs have not already stuck", ErrWorkerStuck)))
			}
		} else {
			assignments = allocateJobs(healthy, remaining)
			steerAway(healthy, assignments)
		}

		type epResult struct {
			idx int
			res map[string]BatchResult
			err error
		}
		resCh := make(chan epResult, len(assignments))
		var wg sync.WaitGroup
		for idx, sub := range assignments {
			if len(sub) == 0 {
				continue
			}
			ep := healthy[idx]
			wg.Go(func() {
				g := probeByURL[ep.URL]
				// The request is watched for a swap of the machine behind the port
				// (workerswap.go): the identity the dispatch froze for it travels in
				// the context, as the senders take none.
				sctx := withFrozenWorker(ctx, frozenWorker{key: keyOf(ep.URL), capable: capableFor[ep.URL]})
				r, err := send(sctx, g, sub, progressFor())
				resCh <- epResult{idx: idx, res: r, err: err}
			})
		}
		wg.Wait()
		close(resCh)
		var round []epResult
		for r := range resCh {
			round = append(round, r)
		}
		// Every fault of the round is marked before any result is read, so a
		// sibling port's strikes are withheld whichever response drained first.
		for _, r := range round {
			if fault, isFault := errors.AsType[*WorkerFaultError](r.err); isFault {
				ep := healthy[r.idx]
				if fault.timeoutMajority() {
					sickMachine[keyOf(ep.URL)] = true
					if fault.killMajority() {
						killedMachine[keyOf(ep.URL)] = true
					}
				} else {
					faultedHere[ep.URL], portSick[ep.URL] = true, true
				}
				sickHere[ep.URL], sickErr[ep.URL] = true, r.err
			}
		}
		// Once every endpoint is sick, the canary decides which machines are
		// believed, before this round's answers are read (see canaryOK).
		//
		// Its error (a cancelled op) ends the dispatch, but only after the answers
		// of this round are read: work that finished in it is not thrown away, and
		// a round that settled every job is not turned into a failure (below).
		var canaryErr error
		if allSick() {
			canaryErr = runCanaries()
			if opts.afterCanaries != nil {
				opts.afterCanaries(endpointURLs(ordered), soloLimit)
			}
		}

		for _, r := range round {
			ep := healthy[r.idx]
			if swap, swapped := errors.AsType[*WorkerSwappedError](r.err); swapped {
				// Another machine answered at the port during the request (the
				// liveness poll saw it): nothing the request returned can be told from
				// the old machine's answer, and the old one may never have seen the
				// file. So no result of it is stored (earlier chunks included), no
				// strike or condemnation is written, the endpoint is not benched
				// (markEndpointFailure is process-wide and the port is not down), and
				// nothing is recorded in lastErr, soloStuck, faultedOn or the canary
				// memory. Its jobs stay in remaining for the other ports, and the port
				// is dropped like one regate found changed hands; the next dispatch's
				// gate keys it by what it reports.
				dispatchLog.Warn("a whisper port changed hands during a request, so none of the request's results are kept and the port is dropped from this dispatch: url=%s was=%s now=%s after=%s jobs=%d withheld=%d",
					logger.SanitizeLogValue(ep.URL), logger.SanitizeLogValue(swap.Was), logger.SanitizeLogValue(swap.Now),
					swap.After.Round(time.Second), len(swap.Jobs), len(r.res))
				reason := "answered as a different worker than the one the dispatch froze for it during a request (its port changed hands), so it is sent no more work in this dispatch"
				if swap.Unverified {
					reason = "its /health could not be read after it answered a request, so the answer cannot be told from another machine's (its port may have changed hands), and it is sent no more work in this dispatch"
				}
				rf := newRefusal(probeByURL[ep.URL], reason)
				rf.ChangedHands = true
				refused = append(refused, rf)
				// Queued, not dropped now: dropPorts counts the machines again, which
				// must not change soloLimit under the answers of this round that are
				// still being judged (see holdDrops).
				heldDrops[ep.URL] = true
				if u := slices.DeleteFunc(slices.Clone(unitOf[r.idx]), func(id string) bool { _, ok := remaining[id]; return !ok }); len(u) > 0 {
					units = append(units, u)
				}
				continue
			}
			fault, isFault := errors.AsType[*WorkerFaultError](r.err)
			if isFault {
				registerFollowers(fault.Followers)
			}
			// Work the endpoint completed is valid whether or not the call as a
			// whole failed: a sender returns the chunks that finished before the
			// failing one alongside its error. The exception is a strike-type
			// answer from a worker that faulted in this dispatch (distrusts):
			// it is withheld and the job re-queued, exactly like a faulted one.
			// A decoder_killed answer from a machine whose fault was mostly kills
			// (killedMachine) is withheld too even once the machine is believed
			// (killHeld); those jobs are re-queued as faulted on this endpoint and
			// never offered to that machine again (killedOn), so a job no other
			// machine can take is deferred, never struck.
			var distrusted, killHeld []string
			settledHere := make(map[string]bool, len(r.res))
			for id, br := range r.res {
				if distrusts(ep.URL) && IsStrikeError(br.Error) && !br.Condemned {
					distrusted = append(distrusted, id)
					continue
				}
				if killedMachine[keyOf(ep.URL)] && PathErrorCode(br.Error) == PathErrDecoderKilled && !br.Condemned {
					killHeld = append(killHeld, id)
					continue
				}
				if br.Endpoint == "" {
					br.Endpoint = ep.URL
					br.Worker = probeByURL[ep.URL].storedWorker()
				}
				results[id] = br
				settledHere[id] = true
			}
			if len(killHeld) > 0 {
				dispatchLog.Warn("withholding decoder kills from a machine whose fault in this dispatch was mostly kills: url=%s withheld=%d",
					logger.SanitizeLogValue(ep.URL), len(killHeld))
				for _, id := range killHeld {
					if killedOn[id] == nil {
						killedOn[id] = make(map[string]bool)
					}
					killedOn[id][keyOf(ep.URL)] = true
				}
				requeueFaulted(killHeld, ep.URL)
			}
			if r.err == nil && len(distrusted) == 0 {
				markEndpointSuccess(ep.URL)
				delete(lastErr, ep.URL)
				delete(faultedHere, ep.URL)
				// Clear the whole assigned set, not just returned IDs: the
				// transport skips unreadable WAVs without error (same as the
				// single-URL path), so a missing result is a skip, not a retry.
				// The kills withheld above stay: they were re-queued.
				for id := range assignments[r.idx] {
					if !slices.Contains(killHeld, id) {
						delete(remaining, id)
					}
				}
				continue
			}
			if r.err == nil {
				// The request went through, but the worker is still the one that
				// faulted earlier and it answered timeouts again: those jobs go
				// elsewhere, everything else it answered stands. It is not healed.
				dispatchLog.Warn("withholding timeouts from a worker that faulted earlier in this dispatch: url=%s withheld=%d",
					logger.SanitizeLogValue(ep.URL), len(distrusted))
				for id := range assignments[r.idx] {
					if !slices.Contains(distrusted, id) && !slices.Contains(killHeld, id) {
						delete(remaining, id)
					}
				}
				if faultedHere[ep.URL] {
					requeueFaulted(distrusted, ep.URL)
				} else {
					requeueUnits(distrusted)
				}
				continue
			}
			for id := range settledHere {
				delete(remaining, id)
			}
			if isFault && !faultedHere[ep.URL] {
				// A timeout-majority fault from an endpoint with no worker-level
				// fault in this dispatch: its faulted jobs go back in units of at
				// most timeoutBreakerMin-1, to any endpoint (this one included),
				// and the endpoint is not benched; see the worker-fault
				// bookkeeping above. The jobs its transport never sent stay in
				// remaining as ordinary work.
				faultedIDs := slices.Sorted(maps.Keys(fault.Faulted))
				// Strikes it answered in earlier chunks of the same request are
				// withheld too while it is not trusted (distrusts).
				faultedIDs = append(faultedIDs, distrusted...)
				dispatchLog.Warn("worker fault (mostly timeouts): benched for the rest of this dispatch unless every endpoint faults and its machine answers the canary clip, its jobs re-sent in units too small to fault: url=%s jobs=%d",
					logger.SanitizeLogValue(ep.URL), len(faultedIDs))
				requeueUnits(faultedIDs)
				if u := slices.DeleteFunc(slices.Clone(unitOf[r.idx]), func(id string) bool {
					_, ok := remaining[id]
					return !ok || slices.Contains(faultedIDs, id) || slices.Contains(killHeld, id)
				}); len(u) > 0 {
					units = append(units, u)
				}
				continue
			}
			requeued := make(map[string]bool, len(killHeld))
			for _, id := range killHeld {
				requeued[id] = true
			}
			if isFault || len(distrusted) > 0 {
				var ids []string
				if isFault {
					ids = slices.Collect(maps.Keys(fault.Faulted))
				}
				if faultedHere[ep.URL] {
					ids = append(ids, distrusted...)
				} else {
					// Withheld only because the endpoint timeout-faulted in this
					// dispatch: not a worker-level fault, so no faultedOn mark.
					requeueUnits(distrusted)
				}
				requeueFaulted(ids, ep.URL)
				for _, id := range append(ids, distrusted...) {
					requeued[id] = true
				}
			}
			stuck, isStuck := errors.AsType[*WorkerStuckError](r.err)
			// A request that went unanswered is "stuck" only while the worker is
			// still there to be stuck. If it no longer answers /health the host
			// went away mid-request, which is an outage: the ops wait it out and
			// re-send, and no file is blamed. A cancelled op is not evidence about
			// the worker at all, so it is neither probed nor benched.
			wedged := true
			if isStuck && ctx.Err() == nil {
				r.err, wedged = afterStuck(ctx, ep.URL, r.err)
				stuck, isStuck = errors.AsType[*WorkerStuckError](r.err)
			}
			// Bench the endpoint; its unfinished jobs stay in remaining and are
			// re-queued to a surviving endpoint on the next round.
			//
			// ErrSlotWait is NOT endpoint evidence: the request was never
			// sent, we only failed to get a slot (usually because the op
			// was cancelled). poolHealth is process-global and its cooldown
			// OUTLIVES the op, so benching here would make the next run
			// report "all N endpoints in cooldown" about servers that were
			// never contacted. The same holds for any failure seen after the
			// op's own context ended.
			//
			// A worker that missed a request ceiling is benched only while it
			// is still wedged. One that answers /health and reports it is
			// within its own deadline is alive: benching it would leave the
			// split parts nowhere to run, and a pool of a few workers could
			// then never narrow a stuck batch down to the file responsible.
			// A worker that cannot say (old, or /health hangs too) is benched:
			// an old worker holds its inference lock until restarted.
			if !errors.Is(r.err, ErrSlotWait) && ctx.Err() == nil && (!isStuck || wedged) {
				markEndpointFailure(ep.URL)
			}
			lastErr[ep.URL] = fmt.Errorf("%s: %w", ep.URL, r.err)

			if !isStuck || ctx.Err() != nil {
				// Not a stuck request: a part that was riding on this endpoint
				// keeps its split and goes to another one (its faulted jobs were
				// re-queued in their own parts above).
				if u := slices.DeleteFunc(slices.Clone(unitOf[r.idx]), func(id string) bool {
					_, ok := remaining[id]
					return !ok || requeued[id]
				}); len(u) > 0 {
					units = append(units, u)
				}
				continue
			}
			registerFollowers(stuck.Followers)
			failed := slices.DeleteFunc(slices.Clone(stuck.Jobs), func(id string) bool { _, ok := remaining[id]; return !ok })
			slices.Sort(failed)
			dispatchLog.Warn("worker stuck, splitting its request and retrying the parts: url=%s after=%s jobs=%d",
				logger.SanitizeLogValue(ep.URL), stuck.After.Round(time.Second), len(failed))
			switch {
			case len(failed) == 1 && !wedged:
				// Alone, but on a worker that is not wedged: the request waited,
				// it did not hang the decoder. Retry the job elsewhere, blame
				// nobody.
				id := failed[0]
				if soloOverran[id] == nil {
					soloOverran[id] = make(map[string]bool)
				}
				soloOverran[id][ep.URL] = true
				units = append(units, failed)
			case len(failed) == 1:
				id := failed[0]
				if soloStuck[id] == nil {
					soloStuck[id] = make(map[string]string)
				}
				soloStuck[id][keyOf(ep.URL)] = ep.URL
				// Only machines count: a "?" entry only keeps the job off that
				// endpoint (it is never a second machine, countLimits).
				stuckOn := maps.Clone(soloStuck[id])
				maps.DeleteFunc(stuckOn, func(k, _ string) bool { return unknownKey(k) })
				// A verdict on ONE machine waits until the job has been sent to
				// every port whose machine is not known (D66): such a port is never
				// counted as a machine (countLimits), but it may well be a second
				// one, and a file that would have been answered there must not be
				// condemned for the first machine's hang. If that port cannot take
				// the job in this dispatch (in cooldown, unreadable, benched, or
				// its request only overran), the part has nowhere to go and ends
				// the dispatch below as ErrWorkerStuck (D69): no strike and no
				// verdict, but the dispatch counts toward the ops' stuck streak, so
				// a pool that keeps hitting this stops the run instead of spinning
				// on the same jobs. (Only when the unknown port was sent the job and
				// its /health could not be read afterwards is the error poolFailure
				// builds a recognised transport failure instead, which the ops count
				// as an outage.) Two machines are the most a verdict ever asks for, so
				// it does not apply to them.
				if len(stuckOn) > 0 && len(stuckOn) >= soloLimit && (len(stuckOn) >= 2 || !untriedUnknownPort(id)) {
					// Stuck by itself on soloLimit different machines: a verdict
					// about this file, not about any worker. Byte-identical
					// followers are the same audio and get the same verdict. It
					// names every machine involved: as a strike it stands for
					// each of them (see BatchResult.Worker). The names are the
					// machine keys the endpoints were stuck under, which is the
					// identity of the worker that answered there: a port is used
					// after a wait only while that identity is still the one the
					// dispatch froze for it (regate drops it otherwise), so no key
					// here stands for two machines. An endpoint whose /health could
					// not be read when the dispatch began is no machine (a "?" key:
					// stuckOn leaves it out).
					verdict := BatchResult{Condemned: true, Error: fmt.Sprintf(
						"%s%sno result: this file alone left %d worker(s) stuck past their deadline",
						PathErrTranscribeTimeout, pathErrSeparator, len(stuckOn)),
						Endpoint: strings.Join(slices.Sorted(maps.Values(stuckOn)), WorkerSeparator),
						Worker:   joinWorkers(slices.Collect(maps.Keys(stuckOn)))}
					settled := 0
					for _, cid := range append([]string{id}, followersOf[id]...) {
						if _, ok := remaining[cid]; !ok {
							continue
						}
						results[cid] = verdict
						delete(remaining, cid)
						settled++
					}
					bump(settled)
					continue
				}
				units = append(units, failed)
			case len(failed) > 1:
				mid := len(failed) / 2
				units = append(units, failed[:mid], failed[mid:])
			}
		}
		if canaryErr != nil && len(remaining) > 0 {
			return results, canaryErr
		}
	}
	return results, nil
}

// singleGate is what dispatchSingle needs from dispatchPool to treat a
// remembered machine's cooldown as the pool path does.
type singleGate struct {
	// paceUntil ends the pacing allowance a wait for a cooldown is bounded by.
	paceUntil time.Time
	// gate probes endpoints again, and judges them as dispatchPool's gate does,
	// without touching what is remembered about their identities.
	gate func([]Endpoint) ([]gatedEndpoint, []gpuRefusal)
	// noEndpoint is the error for refusals that leave nothing to send to.
	noEndpoint func([]gpuRefusal) error
}

// dispatchSingle is dispatchPool for a pool of one eligible endpoint: no
// re-queue to another worker, no bench, the historical single-URL behaviour,
// except for a worker fault, where there is nobody else to hand the jobs to,
// and except that a machine remembered as having failed the canary clip is
// asked it before it gets work, once it is out of cooldown (see singleGate).
//
//   - A fault that is only a majority of timeouts or crashes is the batch's own
//     clips as often as the worker (a chunk of a book whose every track opens
//     on the same long theme). The faulted jobs are sent again to the same
//     worker in requests of at most timeoutBreakerMin-1 clips, whose answers
//     are judged per clip (two timeouts cannot make a fault), and the jobs the
//     transport never got to send are sent again too. Dropping or deferring
//     them instead left the same page's loopers unjudged on every run, in the
//     same order, forever. A timeout from the only worker can never make a
//     file permanent by itself: the store wants the second strike from a
//     different worker. Every pass settles at least the faulted jobs, so this
//     ends. Before that, on the first such fault, the worker is sent the
//     canary clip (see canaryOK in dispatchPool): a worker that does not
//     answer it is sick, not the clips, so the strikes in the faulted response
//     are withheld and the dispatch ends as ErrPoolUnavailable (the ops wait
//     it out and defer the unanswered jobs). Re-sending to a sick lone worker
//     and believing it gave first strikes to whole pages of healthy files.
//   - A worker-level fault (the decoder cannot start) defers the faulted jobs
//     (PathErrWorkerFault, never a strike) and ends the dispatch with an error
//     that is NOT a recognised outage: the ops defer the unanswered jobs and go
//     on, and the gate refuses the worker while it reports the fault (pacing,
//     see dispatchPool).
func dispatchSingle[J any](ctx context.Context, g gatedEndpoint, refused []gpuRefusal, sg singleGate, jobs map[string]J, onProgress ProgressFunc, send endpointSender[J], canary canaryFunc) (map[string]BatchResult, error) {
	url := g.Endpoint.URL
	canaryAsked := false
	// holdKills: the worker's fault was mostly decoder kills, so its
	// decoder_killed answers are deferred for the rest of the dispatch even
	// after it answers the canary clip (see WorkerFaultError.killMajority). A
	// lone endpoint has nowhere else to send them.
	holdKills := false
	results := make(map[string]BatchResult, len(jobs))
	// key is the identity the gate resolved (D55), kept for every memory call below: a
	// re-probe after a wait can replace g (its health is what the canary and the
	// work should use) but must not give the dispatch a second identity.
	key, capable := g.identityKey(), g.idCapable()
	if endpointRemembered(key, url) && ctx.Err() == nil {
		// It failed the canary clip in an earlier dispatch and has not answered
		// one since: ask it before sending it any work (see preflight in
		// dispatchPool). Like the pool path, only once the endpoint is out of
		// cooldown (N-f): a port that was just benched is not asked, it is waited
		// for, bounded by the pacing allowance, and a cooldown that outlasts
		// the allowance is no verdict. Probed again after a wait, and a port
		// that is refused by then is judged as the gate judges it.
		waited := false
		start := time.Now()
		for ctx.Err() == nil && endpointInCooldown(url) {
			wait, act := decideCooldownWait(time.Now(), sg.paceUntil, earliestCooldownEnd([]string{url}))
			if act == cooldownClear {
				break
			}
			if act == cooldownDefer {
				dispatchLog.Warn("the only whisper endpoint failed the canary clip in an earlier dispatch and is in a cooldown that outlasts the %s pacing allowance (waited %s): no verdict on it, the jobs are deferred: url=%s",
					poolPaceMax, time.Since(start).Round(time.Millisecond), logger.SanitizeLogValue(url))
				return results, classifyTransport([]string{url}, fmt.Errorf(
					"the only whisper endpoint failed the canary clip in an earlier dispatch and its cooldown outlasts the pacing allowance: %w, so it is sent no work and the jobs are deferred", errCanarySlotBusy))
			}
			waited = true
			if err := sleepCtx(ctx, min(wait, poolPaceInterval)); err != nil {
				return results, classifyTransport([]string{url}, err)
			}
		}
		for waited && ctx.Err() == nil {
			kept, refusedNow := sg.gate([]Endpoint{g.Endpoint})
			if ctx.Err() != nil {
				// Cancelled during the probe: it only looks unreachable because
				// nobody could ask, and that must not end as ErrPoolUnavailable.
				return results, classifyTransport([]string{url}, ctx.Err())
			}
			if len(kept) > 0 {
				// The health is the fresh probe's; the identity stays the one
				// the dispatch began with (key), as in dispatchPool, unless
				// another worker answers at the port now: it is not the machine
				// that failed the clip, so it is asked nothing and sent nothing,
				// and the jobs are deferred. The next dispatch's gate re-keys it.
				fresh, ok := kept[0].reprobed(key, capable)
				if !ok {
					dispatchLog.Warn("the only whisper endpoint answers as a different worker than the machine that failed the canary clip after its cooldown was waited out, so it is sent no work and the jobs are deferred: url=%s was=%s now=%s",
						logger.SanitizeLogValue(url), logger.SanitizeLogValue(key), logger.SanitizeLogValue(kept[0].identityKey()))
					return results, classifyTransport([]string{url}, fmt.Errorf(
						"the only whisper endpoint answers as a different worker than the machine that failed the canary clip in an earlier dispatch: %w, so it is sent no work and the jobs are deferred", errPortChangedHands))
				}
				g = fresh
				break
			}
			if allTransient(refusedNow) && time.Now().Before(sg.paceUntil) {
				if err := sleepCtx(ctx, max(min(poolPaceInterval, time.Until(sg.paceUntil)), time.Millisecond)); err != nil {
					return results, classifyTransport([]string{url}, err)
				}
				continue
			}
			return results, sg.noEndpoint(append(slices.Clone(refused), refusedNow...))
		}
		if ctx.Err() != nil {
			return results, classifyTransport([]string{url}, ctx.Err())
		}
		gen := canaryGeneration(key, url)
		switch cerr := canary(ctx, g); {
		case ctx.Err() != nil:
			return results, classifyTransport([]string{url}, ctx.Err())
		case cerr == nil:
			// Forgotten only if this was every port of its machine: a sibling
			// the gate refused has not answered, and when it is eligible again
			// the pool asks every port before it gets work. Nor if another
			// dispatch recorded a newer failure while this one asked.
			forgetCanaryIf(key, gen, []string{url}, refused, []gatedEndpoint{g})
		case errors.Is(cerr, errCanarySlotBusy):
			return results, classifyTransport([]string{url}, fmt.Errorf(
				"the only whisper endpoint failed the canary clip in an earlier dispatch and %w, so it is sent no work and the jobs are deferred", cerr))
		default:
			markCanaryFailed(key, append(refusedPorts(refused, key), url)...)
			return results, classifyTransport([]string{url}, fmt.Errorf(
				"%w: the only whisper endpoint failed the canary clip in an earlier dispatch and again now (%w), so it is sent no work",
				ErrPoolUnavailable, cerr))
		}
	}
	total := len(jobs)
	pending := jobs
	// pin is the one machine a worker that gained its machine id may be for the
	// whole dispatch (workerPin): the one it learned, or else the first strong
	// identity a poll sees.
	pin := newWorkerPin(g.learned)
	// small holds the faulted jobs waiting to be sent again in requests too
	// small to fault on timeouts.
	var small [][]string
	for {
		var progress ProgressFunc
		if onProgress != nil {
			base := len(results)
			progress = func(d, _ int) { onProgress(min(base+d, total), total) }
		}
		// Watched for a swap of the machine behind the port, like a pool's.
		res, err := send(withFrozenWorker(ctx, frozenWorker{key: key, capable: capable, lone: true, pin: pin}), g, pending, progress)
		if swap, swapped := errors.AsType[*WorkerSwappedError](err); swapped {
			// Another machine answered at the port during the request. Nothing the
			// request returned can be told from the old machine's answer (earlier
			// chunks included), so it is withheld as a whole; there is no other
			// port, so the unanswered jobs are deferred without a strike. The error
			// is neither stuck nor an outage, and is not counted toward stopping the
			// run (it is no recognised transport failure). Results of earlier sends
			// of this dispatch stand.
			dispatchLog.Warn("the only whisper port changed hands during a request, so none of the request's results are kept and the jobs are deferred: url=%s was=%s now=%s after=%s jobs=%d withheld=%d",
				logger.SanitizeLogValue(url), logger.SanitizeLogValue(swap.Was), logger.SanitizeLogValue(swap.Now),
				swap.After.Round(time.Second), len(swap.Jobs), len(res))
			return results, classifyTransport([]string{url}, err)
		}
		fault, isFault := errors.AsType[*WorkerFaultError](err)
		if isFault && fault.killMajority() {
			holdKills = true // before the answers below: earlier chunks of this request too
		}
		for id, br := range res {
			if holdKills && PathErrorCode(br.Error) == PathErrDecoderKilled && !br.Condemned {
				results[id] = faultDeferral(1)
				continue
			}
			// Every answer the worker gave names it, a no-answer one included,
			// as dispatchPool's do; only a result the dispatcher made itself
			// (a deferral) is unnamed.
			if br.Endpoint == "" {
				br.Endpoint = url
				br.Worker = g.answerWorker()
			}
			results[id] = br
		}
		if err != nil && !isFault {
			if IsWorkerStuck(err) && ctx.Err() == nil {
				// A request that went unanswered because the host vanished is an
				// outage, not a wedged decoder (see afterStuck).
				err, _ = afterStuck(ctx, url, err)
			}
			// Whatever the sender settled before failing is valid; hand it up
			// with the error.
			return results, classifyTransport([]string{url}, err)
		}
		if isFault && (!fault.timeoutMajority() || ctx.Err() != nil) {
			deferral := faultDeferral(1)
			for id := range fault.Faulted {
				for _, cid := range append([]string{id}, fault.Followers[id]...) {
					if _, sent := jobs[cid]; sent {
						results[cid] = deferral
					}
				}
			}
			for id := range jobs {
				if _, done := results[id]; !done {
					// Jobs left unanswered: the ops defer them on this error.
					return results, classifyTransport([]string{url}, err)
				}
			}
			return results, nil
		}
		if isFault && !canaryAsked {
			canaryAsked = true
			if cerr := canary(ctx, g); cerr != nil {
				for id, br := range res {
					if IsStrikeError(br.Error) && !br.Condemned {
						delete(results, id)
					}
				}
				if ctx.Err() != nil {
					// Cancelled while asking: no evidence about the worker.
					return results, classifyTransport([]string{url}, ctx.Err())
				}
				if errors.Is(cerr, errCanarySlotBusy) {
					// No verdict: not ErrPoolUnavailable, not a network failure,
					// so the ops defer these jobs and go on; no cooldown.
					dispatchLog.Warn("worker fault (mostly timeouts) on the only endpoint, and its canary clip could not get a request slot: no verdict, none of its timeouts count: url=%s err=%v",
						logger.SanitizeLogValue(url), cerr)
					return results, classifyTransport([]string{url}, fmt.Errorf(
						"the only whisper endpoint faulted on timeouts and %w, so none of its timeouts count and the jobs are deferred", cerr))
				}
				// Sick: remembered, so the next dispatch asks the canary before it
				// pays another chunk of per-clip deadlines (nit 1 of review 5 of
				// #4182).
				markCanaryFailed(key, append(refusedPorts(refused, key), url)...)
				dispatchLog.Warn("worker fault (mostly timeouts) on the only endpoint, and it did not answer the canary clip: sick, none of its timeouts count: url=%s err=%v",
					logger.SanitizeLogValue(url), cerr)
				return results, classifyTransport([]string{url}, fmt.Errorf(
					"%w: the only whisper endpoint faulted on timeouts and did not answer the canary clip (%w), so none of its timeouts count",
					ErrPoolUnavailable, cerr))
			}
		}
		if isFault {
			ids := slices.Sorted(maps.Keys(fault.Faulted))
			for part := range slices.Chunk(ids, timeoutBreakerMin-1) {
				unit := slices.Clone(part)
				for _, id := range part {
					unit = append(unit, fault.Followers[id]...)
				}
				small = append(small, unit)
			}
			dispatchLog.Warn("worker fault (mostly timeouts) on the only endpoint: re-sending the faulted jobs in requests too small to fault: url=%s faulted=%d",
				logger.SanitizeLogValue(url), len(ids))
		}
		inSmall := make(map[string]bool)
		for _, u := range small {
			for _, id := range u {
				inSmall[id] = true
			}
		}
		// After a clean answer, a job with no result was skipped by the
		// transport (an unreadable file), not left unsent: only a fault leaves
		// jobs unsent.
		rest := make(map[string]J)
		if isFault {
			for id, j := range pending {
				if _, done := results[id]; !done && !inSmall[id] {
					rest[id] = j
				}
			}
		}
		if len(rest) == 0 {
			for len(small) > 0 && len(rest) == 0 {
				for _, id := range small[0] {
					if _, done := results[id]; !done {
						if j, ok := jobs[id]; ok {
							rest[id] = j
						}
					}
				}
				small = small[1:]
			}
		}
		if len(rest) == 0 {
			return results, nil
		}
		pending = rest
	}
}

// faultDeferral is the result for a job the dispatcher defers because only
// faulted workers answered it (see PathErrWorkerFault).
func faultDeferral(workers int) BatchResult {
	return BatchResult{Error: fmt.Sprintf("%s%sno answer: the %d worker(s) this file was sent to answered it only in responses that were the worker's fault, so it is deferred",
		PathErrWorkerFault, pathErrSeparator, workers)}
}

// ErrPoolUnavailable marks a dispatch that found no endpoint it could use and
// kept finding none for poolPaceMax: every endpoint benched by earlier
// failures, every endpoint unreachable at the gate, or every worker reporting
// that its decoder cannot start. It is not
// a verdict about any file. The ops treat it like an outage: wait, re-send, and
// stop the run if it persists.
var ErrPoolUnavailable = errors.New("whisper pool unavailable")

// IsPoolUnavailable reports whether err is (or wraps) ErrPoolUnavailable.
func IsPoolUnavailable(err error) bool { return errors.Is(err, ErrPoolUnavailable) }

// poolPaceInterval is how often a paced dispatch asks a refusing pool again, and
// poolPaceMax how long one dispatch may wait in total for an endpoint to come
// back. poolPaceMax is longer than aidispatch.CooldownMax and than the worker's
// default child-fault window (300 s), so a pool that is merely cooling down or
// restarting a decoder is always waited out. Variables only so tests can shorten
// them; production never writes them.
var (
	poolPaceInterval = 30 * time.Second
	poolPaceMax      = 6 * time.Minute
)

// paceWait is how long to wait for an endpoint this dispatch has no evidence
// against (no entry in lastErr) to come out of a cooldown an earlier dispatch
// put it in. ok is false when there is no such endpoint, or the pacing
// allowance is spent.
func paceWait(ordered []Endpoint, lastErr map[string]error, paceUntil time.Time) (time.Duration, bool) {
	var urls []string
	for _, ep := range ordered {
		if lastErr[ep.URL] == nil {
			urls = append(urls, ep.URL)
		}
	}
	return cooldownWait(urls, paceUntil)
}

// cooldownWait is how long until the first of urls that is in cooldown comes
// out of it, capped at what is left of the pacing allowance. ok is false when
// none of them is in cooldown or the allowance is spent.
func cooldownWait(urls []string, paceUntil time.Time) (time.Duration, bool) {
	now := time.Now()
	if !now.Before(paceUntil) {
		return 0, false
	}
	earliest := earliestCooldownEnd(urls)
	if earliest.IsZero() {
		return 0, false
	}
	return max(min(earliest.Sub(now), paceUntil.Sub(now)), time.Millisecond), true
}

// earliestCooldownEnd is when the first of urls that is in cooldown comes out of
// it, or the zero time when none of them is in cooldown.
func earliestCooldownEnd(urls []string) time.Time {
	var earliest time.Time
	for _, url := range urls {
		if !endpointInCooldown(url) {
			continue
		}
		until := aidispatch.DefaultHealth().CooldownUntil(endpointIDForURL(url))
		if earliest.IsZero() || until.Before(earliest) {
			earliest = until
		}
	}
	return earliest
}

// latestCooldownEnd is when the last of urls that is in cooldown comes out of
// it, or the zero time when none of them is in cooldown: when a machine all of
// whose ports must be out of cooldown can be asked.
func latestCooldownEnd(urls []string) time.Time {
	var latest time.Time
	for _, url := range urls {
		if !endpointInCooldown(url) {
			continue
		}
		if until := aidispatch.DefaultHealth().CooldownUntil(endpointIDForURL(url)); until.After(latest) {
			latest = until
		}
	}
	return latest
}

// keepWaitingFor reports whether a port that is refused now is worth waiting
// for: it may clear by itself (it is unreachable, or its decoder is restarting:
// gpuRefusal.Transient) and the pacing allowance is not spent. Any other port is
// given up on, as the gate gives up on a port it refused.
func keepWaitingFor(r gpuRefusal, now, paceUntil time.Time) bool {
	return r.Transient && now.Before(paceUntil)
}

// cooldownAction is what a dispatch that wants a machine out of cooldown does
// next (see decideCooldownWait).
type cooldownAction int

const (
	// cooldownClear: nothing is in cooldown (any more): look again, and ask.
	cooldownClear cooldownAction = iota
	// cooldownSleep: wait the returned duration, then look again.
	cooldownSleep
	// cooldownDefer: the cooldown outlasts the pacing allowance. No verdict.
	cooldownDefer
)

// decideCooldownWait is the one rule for waiting out the cooldown of a machine
// the canary clip is to be sent to. earliest is when the first cooldown that
// matters ends (zero: none is in cooldown). A cooldown that has already ended
// is not waited for, even when the allowance is spent too: the machine can be
// asked, so it is not deferred. One that ends after the allowance is deferred
// at once, rather than slept through only to be deferred at the end of the
// allowance. Otherwise the wait is until it ends, at most what is left of the
// allowance.
func decideCooldownWait(now, paceUntil, earliest time.Time) (time.Duration, cooldownAction) {
	if earliest.IsZero() || !earliest.After(now) {
		return 0, cooldownClear
	}
	if earliest.After(paceUntil) || !now.Before(paceUntil) {
		return 0, cooldownDefer
	}
	return max(min(earliest.Sub(now), paceUntil.Sub(now)), time.Millisecond), cooldownSleep
}

// sleepCtx waits d or until ctx ends, returning ctx's error in the latter case.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func allTransient(refused []gpuRefusal) bool {
	if len(refused) == 0 {
		return false
	}
	for _, r := range refused {
		if !r.Transient {
			return false
		}
	}
	return true
}

// poolFailure is the error for a dispatch that ran out of endpoints, built from
// the last failure of EACH endpoint in URL order, so it does not depend on the
// order results happened to arrive in.
//
//   - Every failing endpoint was stuck: the pool is wedged, not down. The error
//     is a stuck error (IsWorkerStuck is true) and the ops stop rather than wait.
//   - Any failing endpoint was an outage: the error is an outage, and
//     IsWorkerStuck is false even though some endpoints were stuck. The ops wait
//     it out and re-send, which is right for the endpoint that is merely down;
//     stopping the run because another endpoint was wedged would turn a partial
//     outage into "restart your workers". The stuck endpoints are named in the
//     text only (not wrapped) so they cannot make the error read as stuck.
//
// fallback is returned when no endpoint recorded a failure in this dispatch.
func poolFailure(errs map[string]error, fallback error) error {
	if len(errs) == 0 {
		return fallback
	}
	var stuckErrs, outageErrs, faultErrs []error
	for _, url := range slices.Sorted(maps.Keys(errs)) {
		switch {
		case IsWorkerStuck(errs[url]):
			stuckErrs = append(stuckErrs, errs[url])
		case IsWorkerFault(errs[url]):
			faultErrs = append(faultErrs, errs[url])
		default:
			outageErrs = append(outageErrs, errs[url])
		}
	}
	// A worker fault is grouped with the outages (the pool is failing, not
	// wedged) but goes after them, so the wrapped head is a real network failure
	// whenever there is one: that is what the ops wait out and re-send, and a
	// worker fault at the head would make the whole error unrecognised.
	outageErrs = append(outageErrs, faultErrs...)
	if len(outageErrs) == 0 {
		if len(stuckErrs) == 1 {
			return stuckErrs[0]
		}
		return fmt.Errorf("%w (and %s)", stuckErrs[0], joinErrs(stuckErrs[1:]))
	}
	rest := slices.Concat(outageErrs[1:], stuckErrs)
	if len(rest) == 0 {
		return outageErrs[0]
	}
	return fmt.Errorf("%w (and %s)", outageErrs[0], joinErrs(rest))
}

func joinErrs(errs []error) string {
	parts := make([]string, len(errs))
	for i, e := range errs {
		parts[i] = e.Error()
	}
	return strings.Join(parts, "; ")
}

// probeWorker re-probes url after one of its requests went unanswered. reachable
// is false when /health itself does not answer. wedged is true when the worker
// cannot say it is healthy: it does not report busy_seconds (an old worker holds
// its inference lock until restarted), or it is running past its deadline plus
// grace.
func probeWorker(ctx context.Context, url string) (reachable, wedged bool) {
	h, ok := probeRemoteHealth(ctx, url)
	if !ok {
		return false, true
	}
	if h.BusySeconds == nil {
		return true, true
	}
	return true, stuckRefusal(gatedEndpoint{Endpoint: Endpoint{URL: url}, Health: h, Probed: true}) != ""
}

// afterStuck decides what an unanswered request at url really was. A worker
// that no longer answers /health did not wedge: its host went away (asleep,
// partitioned, rebooted) after accepting the request, and the transport timeout
// the request died of is indistinguishable from a stuck decode until something
// asks again. That case is returned as an outage error -- the original is
// quoted with %v, NOT wrapped, so IsWorkerStuck is false -- and wedged is true
// so the endpoint is benched. A reachable worker keeps the stuck error, and
// wedged says whether it should still be benched.
func afterStuck(ctx context.Context, url string, stuckErr error) (out error, wedged bool) {
	reachable, wedged := probeWorker(ctx, url)
	if !reachable {
		return &TransportError{
			Endpoints: []string{url},
			Err: fmt.Errorf("a request went unanswered and %s no longer answers /health, so this is an outage, not a stuck decode (%v)",
				url, stuckErr),
			Recognized: true,
		}, true
	}
	return stuckErr, wedged
}

// changedHandsOrTransient reports whether the refusals are ports that changed
// hands while the dispatch waited out a cooldown (gpuRefusal.ChangedHands), with
// nothing beside them but ports that are unreachable (Transient): at least one
// changed hands and every other one is only down. A dead tunnel is not a
// capability problem, and a pool left with it and ports that changed hands has
// nothing to send to for the reason the changed ports give. A capability refusal
// beside them is not this: that error names the real cause. A stuck port beside
// them is stuckBesideChangedHands.
func changedHandsOrTransient(refused []gpuRefusal) bool {
	var changed bool
	for _, r := range refused {
		switch {
		case r.ChangedHands:
			changed = true
		case !r.Transient:
			return false
		}
	}
	return changed
}

// stuckBesideChangedHands reports whether the refusals are a mix of ports that
// are stuck (gpuRefusal.Stuck) and ports that changed hands (ChangedHands), with
// at most unreachable ports (Transient) beside them: at least one of each of the
// first two. Neither all-stuck nor only changed hands, and no capability problem.
func stuckBesideChangedHands(refused []gpuRefusal) bool {
	var stuck, changed bool
	for _, r := range refused {
		switch {
		case r.ChangedHands:
			changed = true
		case r.Stuck:
			stuck = true
		case !r.Transient:
			return false
		}
	}
	return stuck && changed
}

func allStuck(refused []gpuRefusal) bool {
	if len(refused) == 0 {
		return false
	}
	for _, r := range refused {
		if !r.Stuck {
			return false
		}
	}
	return true
}

// allocCursor rotates which equal-priority endpoint an allocation offers
// first. Without it every call starts at the same endpoint, and a caller that
// sends one small batch per call (the per-file intro op sends 8 jobs per call)
// would put every batch on endpoint 0 while the others idle.
var allocCursor atomic.Uint64

// dispatchLog carries the dispatcher's stuck-worker warnings through the
// logger package (which sanitizes line breaks), not log/slog directly.
var dispatchLog = logger.New("transcribe-dispatch")

// endpointFreeSlots is how many request slots url has free right now: its
// Concurrency (values < 1 mean 1) minus the slots currently held.
func endpointFreeSlots(ep Endpoint) int {
	return max(ep.Concurrency, 1) - inFlightDepth(ep.URL)
}

// allocateJobs assigns remaining jobs to healthy endpoints (already sorted by
// ascending Priority). See allocateJobsWith for the policy.
func allocateJobs[J any](healthy []Endpoint, remaining map[string]J) []map[string]J {
	return allocateJobsWith(healthy, remaining, endpointFreeSlots, int(allocCursor.Add(1)-1))
}

// allocateJobsWith assigns jobs capacity-first, then by overflow.
//
// Phase 1 (capacity-aware): one chunk of whisperBatchSize jobs is offered per
// FREE request slot. Endpoints are visited tier by tier in ascending Priority
// (a lower number fills first; jobs spill to the next tier only once every
// slot in the tier is taken), and within a tier of equal Priority they are
// visited round-robin starting at `start`, so equal endpoints share the work
// instead of the first one taking it all. An endpoint whose slots are full is
// skipped rather than queued behind.
//
// Phase 2 (overflow): when every slot in the pool is taken and jobs remain,
// each pass offers every endpoint up to Concurrency x whisperBatchSize jobs,
// tier by tier in priority order, round-robin from `start` inside a tier, so
// the excess queues on slots proportionally to Concurrency and a small batch
// still rotates across equal endpoints.
// Job IDs are assigned in sorted order for determinism.
func allocateJobsWith[J any](healthy []Endpoint, remaining map[string]J, free func(Endpoint) int, start int) []map[string]J {
	assignments := make([]map[string]J, len(healthy))
	ids := make([]string, 0, len(remaining))
	for id := range remaining {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	chunk := max(whisperBatchSize(), 1)

	pos := 0
	give := func(i, n int) {
		for k := 0; k < n && pos < len(ids); k++ {
			if assignments[i] == nil {
				assignments[i] = make(map[string]J)
			}
			assignments[i][ids[pos]] = remaining[ids[pos]]
			pos++
		}
	}

	// Phase 1: tiers of equal priority, round-robin inside a tier.
	for lo := 0; lo < len(healthy) && pos < len(ids); {
		hi := lo + 1
		for hi < len(healthy) && healthy[hi].Priority == healthy[lo].Priority {
			hi++
		}
		tier := hi - lo
		slots := make([]int, tier)
		for k := range tier {
			slots[k] = max(free(healthy[lo+k]), 0)
		}
		for pos < len(ids) {
			progressed := false
			for k := range tier {
				idx := (start + k) % tier
				if slots[idx] == 0 || pos >= len(ids) {
					continue
				}
				give(lo+idx, chunk)
				slots[idx]--
				progressed = true
			}
			if !progressed {
				break
			}
		}
		lo = hi
	}

	// Phase 2: every slot is taken; queue the rest. Each pass walks the tiers
	// in priority order and, inside a tier, visits endpoints round-robin from
	// `start`, one chunk per visit, up to each endpoint's Concurrency chunks. A
	// pass therefore still offers every endpoint Concurrency x whisperBatchSize
	// jobs, but a small batch no longer always lands on healthy[0].
	for pos < len(ids) {
		for lo := 0; lo < len(healthy) && pos < len(ids); {
			hi := lo + 1
			for hi < len(healthy) && healthy[hi].Priority == healthy[lo].Priority {
				hi++
			}
			tier := hi - lo
			maxConc := 0
			for k := range tier {
				maxConc = max(maxConc, max(healthy[lo+k].Concurrency, 1))
			}
			for r := 0; r < maxConc && pos < len(ids); r++ {
				for k := range tier {
					idx := (start + k) % tier
					if r < max(healthy[lo+idx].Concurrency, 1) && pos < len(ids) {
						give(lo+idx, chunk)
					}
				}
			}
			lo = hi
		}
	}
	return assignments
}

func endpointURLs(eps []Endpoint) []string {
	urls := make([]string, 0, len(eps))
	for _, ep := range eps {
		urls = append(urls, ep.URL)
	}
	return urls
}
