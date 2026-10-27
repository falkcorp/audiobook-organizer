// file: internal/transcribe/dispatcher.go
// version: 1.19.0
// guid: ea9de4e6-980d-411f-a92c-878af1df490a
// last-edited: 2026-10-26

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
	var (
		gated   []gatedEndpoint
		refused []gpuRefusal
	)
	for {
		gated, refused = gateEndpoints(ctx, endpoints, requires)
		kept := gated[:0:0]
		for _, g := range gated {
			pruneStaleCanaryIdentities(g)
			// A wedged worker is refused for every kind of job: a request sent
			// to it is never answered, and queueing behind it is how one bad
			// clip parked the whole pool.
			if reason := stuckRefusal(g); reason != "" {
				refused = append(refused, gpuRefusal{URL: g.Endpoint.URL, Label: g.Endpoint.Label, Reason: reason, Machine: refusalMachine(g), Stuck: true})
				continue
			}
			if reason := childFaultRefusal(g); reason != "" {
				refused = append(refused, gpuRefusal{URL: g.Endpoint.URL, Label: g.Endpoint.Label, Reason: reason, Machine: refusalMachine(g), Transient: true})
				continue
			}
			if extraRefusal != nil {
				if reason := extraRefusal(g); reason != "" {
					refused = append(refused, gpuRefusal{URL: g.Endpoint.URL, Label: g.Endpoint.Label, Reason: reason, Machine: refusalMachine(g)})
					continue
				}
			}
			kept = append(kept, g)
		}
		gated = kept
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
	if len(gated) == 0 {
		if allStuck(refused) {
			// Reachable and answering /health, but wedged: not an outage, and
			// waiting it out by re-sending is the wrong response (see
			// ErrWorkerStuck).
			return nil, classifyTransport(endpointURLs(endpoints), fmt.Errorf(
				"%w: all %d whisper endpoint(s) are stuck on a decode past its deadline: %s",
				ErrWorkerStuck, len(endpoints), describeRefusals(refused)))
		}
		if allTransient(refused) {
			return nil, classifyTransport(endpointURLs(endpoints), fmt.Errorf(
				"%w: all %d whisper endpoint(s) still unreachable or unable to start their decoder after waiting %s: %s",
				ErrPoolUnavailable, len(endpoints), poolPaceMax, describeRefusals(refused)))
		}
		// Distinct from "no whisper endpoints configured": that sends an
		// operator hunting a config typo, when the real answer is that the
		// box they configured is not on a GPU.
		return nil, classifyTransport(endpointURLs(endpoints), fmt.Errorf(
			"all %d whisper endpoint(s) refused by capability requirements %v: %s",
			len(endpoints), requires, describeRefusals(refused)))
	}

	// forget drops a machine from the remembered canary failures once it has
	// answered, unless a port of it was refused at the gate and has not (see
	// hasUnheardPort). Used wherever a pass would clear the memory.
	forget := func(key string) {
		if !hasUnheardPort(refused, key) {
			clearCanaryFailed(key)
		}
	}

	// Health is keyed by URL so allocateJobs keeps operating on []Endpoint
	// and its index-based contract with the assignment slices is untouched.
	probeByURL := make(map[string]gatedEndpoint, len(gated))
	eligible := make([]Endpoint, 0, len(gated))
	for _, g := range gated {
		probeByURL[g.Endpoint.URL] = g
		eligible = append(eligible, g.Endpoint)
	}

	if len(eligible) == 1 {
		return dispatchSingle(ctx, probeByURL[eligible[0].URL], refused, jobs, onProgress, send, canary)
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
	keyOf := func(url string) string { return probeByURL[url].machineKey() }
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
	for _, ep := range ordered {
		machines[keyOf(ep.URL)] = true
	}
	soloLimit := min(2, len(machines))
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
	// dispatch and did not answer it at the start of this one (see preflight):
	// sick for the whole dispatch, asked nothing more.
	preBenched := make(map[string]bool)
	isSick := func(url string) bool { return portSick[url] || sickMachine[keyOf(url)] || preBenched[keyOf(url)] }
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
	benched := func(url string) bool {
		if k := keyOf(url); preBenched[k] || (sickMachine[k] && !canaryOK[k]) {
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
	askCanaries := func(asks map[string][]string, why string) map[string]canaryVerdict {
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
			return out
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
		return out
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
	runCanaries := func() {
		if ctx.Err() != nil {
			// A cancelled op is no evidence about any machine: ask none (and log
			// none as sick). The round loop returns the cancellation next.
			return
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
				if keyOf(ep.URL) != key || !sickHere[ep.URL] {
					continue
				}
				if portSick[ep.URL] || endpointInCooldown(ep.URL) {
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
		for key, v := range askCanaries(asks, "every whisper endpoint faulted") {
			switch v {
			case canaryFailedV:
				markCanaryFailed(key)
			case canaryWasBusy:
				canaryBusy[key] = true
			case canaryPassed:
				canaryOK[key] = true
				forget(key)
				dispatchLog.Info("every whisper endpoint faulted and every faulted port of this machine answered the canary clip: its timeouts are about the clips and count: machine=%s ports=%d",
					logger.SanitizeLogValue(key), len(asks[key]))
			}
		}
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
	// review 6 of #4193). If another machine that is not known to be sick is in
	// the pool, it is benched for this dispatch without being asked, as before:
	// the work goes to the other machine and nothing is lost by not waiting.
	// With no such machine (the production Mac alone, on a night llm1 is out)
	// benching it ended the dispatch as ErrPoolUnavailable at once, which the
	// book op counts toward its five-page stop, so six pages in flight could
	// end the night's run inside one cooldown (at most five minutes) for a Mac
	// that would have passed the canary afterwards. So the dispatch waits the
	// cooldown out, bounded by the same pacing allowance as the round loop's
	// paceWait (paceUntil) and in steps of poolPaceInterval, then asks every
	// port. A cooldown that outlasts the allowance is no verdict, exactly as a
	// canary that could not get a request slot is: the machine is benched and
	// not remembered afresh, and the dispatch defers without counting toward
	// the ops' stop limits. A cancelled op returns from the wait with nothing
	// benched; the round loop returns the cancellation. The ops tick the
	// watchdog around the whole dispatch call (tickWhileWaiting), as for every
	// other wait in this function.
	preflight := func() {
		asks := make(map[string][]string)
		unaskable := make(map[string]bool)
		for _, ep := range ordered {
			k := keyOf(ep.URL)
			switch {
			case !failedCanaryBefore(k):
			case endpointInCooldown(ep.URL):
				unaskable[k] = true
			default:
				asks[k] = append(asks[k], ep.URL)
			}
		}
		if (len(asks) == 0 && len(unaskable) == 0) || ctx.Err() != nil {
			return
		}
		// A machine with a port that cannot be asked yet is asked on none.
		for k := range unaskable {
			delete(asks, k)
		}
		const why = "a whisper machine that failed the canary clip in an earlier dispatch was asked it again"
		// settle applies the verdicts of askCanaries.
		settle := func(verdicts map[string]canaryVerdict, asked map[string][]string) {
			for key, v := range verdicts {
				switch v {
				case canaryPassed:
					forget(key)
					dispatchLog.Info("a whisper machine that failed the canary clip in an earlier dispatch answered it on every port: it takes work again: machine=%s ports=%d",
						logger.SanitizeLogValue(key), len(asked[key]))
					continue
				case canaryWasBusy:
					canaryBusy[key] = true
				}
				preBenched[key], canaryRan[key] = true, true
			}
		}
		settle(askCanaries(asks, why), asks)
		waitLogged := false
		for len(unaskable) > 0 && ctx.Err() == nil {
			// Another machine that is not known to be sick can take the work.
			if slices.ContainsFunc(ordered, func(ep Endpoint) bool {
				k := keyOf(ep.URL)
				return !unaskable[k] && !preBenched[k]
			}) {
				for k := range unaskable {
					preBenched[k], canaryRan[k] = true, true
				}
				return
			}
			cooling := make(map[string]bool)
			var coolingURLs []string
			for _, ep := range ordered {
				if k := keyOf(ep.URL); unaskable[k] && endpointInCooldown(ep.URL) {
					cooling[k] = true
					coolingURLs = append(coolingURLs, ep.URL)
				}
			}
			ready := make(map[string][]string)
			for _, ep := range ordered {
				if k := keyOf(ep.URL); unaskable[k] && !cooling[k] {
					ready[k] = append(ready[k], ep.URL)
				}
			}
			if len(ready) > 0 {
				for k := range ready {
					delete(unaskable, k)
					if !failedCanaryBefore(k) {
						// Another dispatch asked it and cleared it meanwhile.
						delete(ready, k)
					}
				}
				settle(askCanaries(ready, why), ready)
				continue
			}
			wait, ok := cooldownWait(coolingURLs, paceUntil)
			if !ok {
				if time.Now().Before(paceUntil) {
					continue // the last cooldown ended between the checks
				}
				for k := range unaskable {
					preBenched[k], canaryRan[k], canaryBusy[k] = true, true, true
					dispatchLog.Warn("a whisper machine that failed the canary clip in an earlier dispatch still has a port in cooldown after waiting %s: no verdict on it, the jobs are deferred: machine=%s",
						poolPaceMax, logger.SanitizeLogValue(k))
				}
				return
			}
			if !waitLogged {
				waitLogged = true
				dispatchLog.Warn("every whisper machine that could take work failed the canary clip in an earlier dispatch and one has a port in cooldown; waiting for the cooldown to end before asking it: machines=%d",
					len(unaskable))
			}
			if sleepCtx(ctx, min(wait, poolPaceInterval)) != nil {
				return
			}
		}
	}
	faultLimit := min(2, len(ordered))
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
	steerAway := func(eps []Endpoint, asg []map[string]J) {
		if len(avoidOf) == 0 {
			return
		}
		for i, ep := range eps {
			here := probeByURL[ep.URL].workerID()
			for _, id := range slices.Sorted(maps.Keys(asg[i])) {
				set := avoidOf[id]
				if _, still := asg[i][id]; !still || here == "" || !set[here] {
					continue // (not still: a follower that already moved with its leader)
				}
				best := -1
				for k, alt := range eps {
					if wid := probeByURL[alt.URL].workerID(); k == i || wid == "" || set[wid] {
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
		wid := probeByURL[url].workerID()
		for _, id := range live {
			if set := avoidOf[id]; set != nil && (wid == "" || set[wid]) {
				return true
			}
		}
		return false
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

	preflight()
	for len(remaining) > 0 {
		if err := ctx.Err(); err != nil {
			return results, classifyTransport(endpointURLs(ordered), err)
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
		// healthy is every endpoint that may take work this round: not in
		// cooldown, and not soft-benched for a fault in this dispatch (see
		// benched).
		var healthy []Endpoint
		for _, ep := range ordered {
			if !endpointInCooldown(ep.URL) && !benched(ep.URL) {
				healthy = append(healthy, ep)
			}
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
			if wait, ok := paceWait(ordered, lastErr, paceUntil); ok {
				if err := sleepCtx(ctx, wait); err != nil {
					return results, classifyTransport(endpointURLs(ordered), err)
				}
				continue
			}
			if len(lastErr) == 0 {
				return results, classifyTransport(endpointURLs(ordered), fmt.Errorf(
					"%w: every whisper endpoint that has not faulted in this dispatch is in cooldown after waiting %s (%d of %d faulted)",
					ErrPoolUnavailable, poolPaceMax, sickCount(), len(ordered)))
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
				// Nothing could be placed this round. An endpoint benched by an
				// earlier dispatch may still take the waiting parts once its
				// cooldown ends: wait for it rather than give up.
				if wait, ok := paceWait(ordered, lastErr, paceUntil); ok {
					if err := sleepCtx(ctx, wait); err != nil {
						return results, classifyTransport(endpointURLs(ordered), err)
					}
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
				r, err := send(ctx, g, sub, progressFor())
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
		if allSick() {
			runCanaries()
		}

		for _, r := range round {
			ep := healthy[r.idx]
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
					br.Worker = probeByURL[ep.URL].workerID()
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
				if len(soloStuck[id]) >= soloLimit {
					// Stuck by itself on soloLimit different machines: a verdict
					// about this file, not about any worker. Byte-identical
					// followers are the same audio and get the same verdict. It
					// names every machine involved: as a strike it stands for
					// each of them (see BatchResult.Worker).
					verdict := BatchResult{Condemned: true, Error: fmt.Sprintf(
						"%s%sno result: this file alone left %d worker(s) stuck past their deadline",
						PathErrTranscribeTimeout, pathErrSeparator, len(soloStuck[id])),
						Endpoint: strings.Join(slices.Sorted(maps.Values(soloStuck[id])), WorkerSeparator),
						Worker:   joinWorkers(slices.Collect(maps.Keys(soloStuck[id])))}
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
	}
	return results, nil
}

// dispatchSingle is dispatchPool for a pool of one eligible endpoint: no
// re-queue to another worker, no bench, the historical single-URL behaviour,
// except for a worker fault, where there is nobody else to hand the jobs to.
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
func dispatchSingle[J any](ctx context.Context, g gatedEndpoint, refused []gpuRefusal, jobs map[string]J, onProgress ProgressFunc, send endpointSender[J], canary canaryFunc) (map[string]BatchResult, error) {
	url := g.Endpoint.URL
	canaryAsked := false
	// holdKills: the worker's fault was mostly decoder kills, so its
	// decoder_killed answers are deferred for the rest of the dispatch even
	// after it answers the canary clip (see WorkerFaultError.killMajority). A
	// lone endpoint has nowhere else to send them.
	holdKills := false
	results := make(map[string]BatchResult, len(jobs))
	if key := g.machineKey(); failedCanaryBefore(key) && ctx.Err() == nil {
		// It failed the canary clip in an earlier dispatch and has not answered
		// one since: ask it before sending it any work (see preflight in
		// dispatchPool).
		switch cerr := canary(ctx, g); {
		case ctx.Err() != nil:
			return results, classifyTransport([]string{url}, ctx.Err())
		case cerr == nil:
			// Forgotten only if this was every port of its machine: a sibling
			// the gate refused has not answered, and when it is eligible again
			// the pool asks every port before it gets work.
			if !hasUnheardPort(refused, key) {
				clearCanaryFailed(key)
			}
		case errors.Is(cerr, errCanarySlotBusy):
			return results, classifyTransport([]string{url}, fmt.Errorf(
				"the only whisper endpoint failed the canary clip in an earlier dispatch and %w, so it is sent no work and the jobs are deferred", cerr))
		default:
			return results, classifyTransport([]string{url}, fmt.Errorf(
				"%w: the only whisper endpoint failed the canary clip in an earlier dispatch and again now (%w), so it is sent no work",
				ErrPoolUnavailable, cerr))
		}
	}
	total := len(jobs)
	pending := jobs
	// small holds the faulted jobs waiting to be sent again in requests too
	// small to fault on timeouts.
	var small [][]string
	for {
		var progress ProgressFunc
		if onProgress != nil {
			base := len(results)
			progress = func(d, _ int) { onProgress(min(base+d, total), total) }
		}
		res, err := send(ctx, g, pending, progress)
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
				br.Worker = g.workerID()
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
				markCanaryFailed(g.machineKey())
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
	if earliest.IsZero() {
		return 0, false
	}
	return max(min(earliest.Sub(now), paceUntil.Sub(now)), time.Millisecond), true
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
