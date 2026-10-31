// file: internal/transcribe/workerswap.go
// version: 1.2.0
// guid: f3815e00-3772-41f5-b825-2a63377081fc
// last-edited: 2026-10-30

package transcribe

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// A whisper port can be handed to another machine while a request to it is in
// flight: a tunnel that comes back from another Mac. The pool compares a port's
// identity with the one the dispatch froze for it only when it probes the port
// again after a cooldown (regate), and a request lasts minutes. Without a check
// during the request, the answer, or the timeout, of whichever machine ended up
// behind the URL is stored under the old machine's name: a strike or a
// condemnation then names a machine that never saw the file.
//
// pollWorkerLiveness already reads /health every livenessInterval for the whole
// request; it also compares the machine_id it reads with the identity the
// dispatch froze for the port (frozenWorker), and cancels the request with an
// identityChangedCause on a difference. superviseRequest turns that into a
// *WorkerSwappedError, and the dispatchers treat it as a port that changed hands:
// no result of the request is kept, no strike is written, nothing is benched,
// and the port is dropped from the dispatch.

// ErrWorkerSwapped marks a request ended because the worker behind its URL
// answered /health as another machine than the one the request was sent to. It
// is not a stuck worker (nobody is blamed for a decode) and not an outage (the
// endpoint is not benched process-wide): the jobs are deferred without a strike
// and the next dispatch's gate keys the port by what it reports.
var ErrWorkerSwapped = errors.New("whisper worker changed hands mid-request")

// WorkerSwappedError is the typed form of ErrWorkerSwapped. Its text must not
// contain any of transportWordings: classifyTransport reads those to decide
// whether a failure is a network one, and this one is neither.
type WorkerSwappedError struct {
	URL string
	// Was is the identity the dispatch froze for the port, and Now what /health
	// reported when the swap was seen (both machine keys, never an address).
	Was, Now string
	// After is how long into the request the swap was seen.
	After time.Duration
	// Jobs are the ids of the request that was ended.
	Jobs []string
}

func (e *WorkerSwappedError) Error() string {
	return fmt.Sprintf("worker swapped: %s answers /health as another machine than the one a request of %d job(s) was sent to, %s into the request",
		e.URL, len(e.Jobs), e.After.Round(time.Second))
}

// Is makes errors.Is true for ErrWorkerSwapped and for errPortChangedHands: it
// is that situation, seen during a request instead of after a wait, so the pool
// and the lone endpoint end it with the same deferral.
func (e *WorkerSwappedError) Is(target error) bool {
	return target == ErrWorkerSwapped || target == errPortChangedHands
}

// IsWorkerSwapped reports whether err is (or wraps) a mid-request swap.
func IsWorkerSwapped(err error) bool { return errors.Is(err, ErrWorkerSwapped) }

// ErrAnswerUnverified marks one response withheld because the /health probe that
// follows every response on a frozen-identity request could not be read, even
// after livenessMisses attempts, and nothing else vouched for the worker (see
// verifyAfterResponse). It is NOT a swap: an unreadable probe is the absence of
// evidence, not evidence of another machine. A worker that blocks /health while
// it is busy decoding looks exactly like this, so the error condemns nothing
// but the one response: the chunks that already passed a verified probe stand
// (and stay in the journal), the port is neither dropped nor marked as having
// changed hands, no strike is written, and nothing is benched.
var ErrAnswerUnverified = errors.New("whisper worker's answer could not be attributed to its machine")

// WorkerUnverifiedError is the typed form of ErrAnswerUnverified. Its text must
// not contain any of transportWordings, for the reason WorkerSwappedError's must
// not.
type WorkerUnverifiedError struct {
	URL string
	// Was is the identity the dispatch froze for the port.
	Was string
	// After is how long the withheld request ran.
	After time.Duration
	// Jobs are the ids of the withheld request: the one chunk whose answer was
	// not kept. Earlier chunks of the same send are not in it.
	Jobs []string
	// Probes is how many /health attempts were made and unreadable.
	Probes int
	// Followers maps a job in Jobs to the ids that were not sent because their
	// audio is byte-identical to its (the journal's content dedup): they are
	// re-sent with it or deferred with it, never alone.
	Followers map[string][]string
}

func (e *WorkerUnverifiedError) Error() string {
	return fmt.Sprintf("worker not verified: the machine behind %s could not be confirmed after a request of %d job(s) was answered (%d /health attempts), so that answer is withheld",
		e.URL, len(e.Jobs), e.Probes)
}

func (e *WorkerUnverifiedError) Is(target error) bool { return target == ErrAnswerUnverified }

// IsWorkerUnverified reports whether err is (or wraps) a withheld,
// unverifiable response.
func IsWorkerUnverified(err error) bool { return errors.Is(err, ErrAnswerUnverified) }

// identityEvidence is when a readable /health last confirmed the frozen
// identity during one send (the poll's probes and the post-response probes both
// record it). It lets a response whose own post-response probe is unreadable be
// kept when the worker was seen to be the frozen machine a moment before, and
// withheld when it was not. Shared by the poller goroutine and the sender, hence
// the lock.
type identityEvidence struct {
	mu sync.Mutex
	at time.Time
}

func (e *identityEvidence) record(at time.Time) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if at.After(e.at) {
		e.at = at
	}
}

// vouchesFor reports whether the identity was confirmed within the evidence
// window of t: livenessInterval, the longest the poll leaves a port unwatched,
// plus healthProbeTimeout, the longest the probe that confirmed it could take.
func (e *identityEvidence) vouchesFor(t time.Time) bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.at.IsZero() {
		return false
	}
	return t.Sub(e.at) <= livenessInterval+healthProbeTimeout
}

// errLivenessIdentityChanged is the supervisor's cause for a swap, like
// errLivenessUnreachable and errLivenessWedged; the cause actually set is an
// *identityChangedCause, which carries what /health reported.
var errLivenessIdentityChanged = errors.New("worker answers as another machine")

// identityChangedCause is the context cause pollWorkerLiveness sets on a swap.
type identityChangedCause struct{ was, now string }

func (c *identityChangedCause) Error() string { return errLivenessIdentityChanged.Error() }

func (c *identityChangedCause) Is(target error) bool { return target == errLivenessIdentityChanged }

// frozenWorker is the identity a dispatch froze for the port a request goes to.
type frozenWorker struct {
	// key is the identity the gate resolved (gatedEndpoint.identityKey) and
	// capable whether the worker could report a machine id then.
	key     string
	capable bool
	// lone marks the lone endpoint (dispatchSingle), which keeps a worker that
	// gained a machine id (gatedEndpoint.reprobed); a pool drops it.
	lone bool
	// pin is the lone endpoint's record of the machine a worker that gained its
	// id has shown (see workerPin). Nil for a pool port.
	pin *workerPin
	// seen is when a probe last confirmed the identity during the send (see
	// identityEvidence); withFrozenWorker creates it.
	seen *identityEvidence
}

// workerPin is the one machine a lone endpoint's weakly-frozen worker may be.
// Such a worker (frozen as "u:") can gain its machine id during the dispatch,
// and reprobed accepts that without knowing whether it is the machine that was
// behind the URL or another that took it over. The pin makes the acceptance
// consistent: it starts as the machine the dispatch already learned
// (gatedEndpoint.learned), or else the first strong identity any poll of the
// dispatch sees, and a later poll that reports a different strong identity is a
// swap. It lives for the whole dispatchSingle, so it holds across the requests
// (chunks) of a send and across sends. Shared by the poller goroutine and the
// post-response probe, hence the lock.
type workerPin struct {
	mu sync.Mutex
	id string
}

func newWorkerPin(learned string) *workerPin { return &workerPin{id: learned} }

// accept reports whether the strong identity now is the pinned machine, pinning
// it if none is yet.
func (p *workerPin) accept(now string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.id == "" {
		p.id = now
	}
	return p.id == now
}

type frozenWorkerCtxKey struct{}

// withFrozenWorker returns ctx carrying the identity the dispatch froze for the
// port its request goes to. The endpoint senders take no identity argument, so
// it travels with the context to superviseRequest; a request without it is not
// checked.
func withFrozenWorker(ctx context.Context, fw frozenWorker) context.Context {
	if fw.seen == nil {
		fw.seen = &identityEvidence{}
	}
	return context.WithValue(ctx, frozenWorkerCtxKey{}, fw)
}

func frozenWorkerFrom(ctx context.Context) (frozenWorker, bool) {
	fw, ok := ctx.Value(frozenWorkerCtxKey{}).(frozenWorker)
	return fw, ok
}

// swappedTo reports whether the /health h of url is another machine than the one
// frozen, and the identity it answers as. It judges a probe as regate does after
// a cooldown wait (D60, D62), by the same rules: the identity is resolved by
// address (resolveMachine), so a worker that missed its machine id and answers
// as "u:" is still the machine last seen behind the URL; any other difference
// is another worker. A frozen identity standing for no machine ("" or "?") has
// nothing to compare. It reads the canary memory and never writes it: the next
// dispatch's gate does that.
func (fw frozenWorker) swappedTo(url string, h remoteHealth) (now string, swapped bool) {
	if !fw.comparable() {
		return "", false
	}
	g := gatedEndpoint{Endpoint: Endpoint{URL: url}, Health: h, Probed: true}
	g.resolved = resolveMachine(url, g.machineKey(), g.idCapable())
	now = g.identityKey()
	if _, ok := g.reprobed(fw.key, fw.capable); !ok {
		return now, true
	}
	if !fw.lone {
		return now, now != fw.key
	}
	// The lone endpoint accepts a worker that gained its id (reprobed above), but
	// only the machine it has already shown: a strong identity that contradicts
	// the pin, learned at the start or seen first during the dispatch, is another
	// machine, and no answer may be stored under a learned machine the poll has
	// contradicted.
	if fw.pin != nil && isStrongKey(now) && !isStrongKey(fw.key) {
		return now, !fw.pin.accept(now)
	}
	return now, false
}

// comparable reports whether the frozen identity names a machine at all.
func (fw frozenWorker) comparable() bool { return fw.key != "" && !unknownKey(fw.key) }

// confirms reports whether h, a readable probe that swappedTo found no swap in,
// is positive evidence that the worker is the frozen machine. A strong frozen
// identity is confirmed only by a probe that reports a machine_id of its own: a
// "u:" worker that merely missed its id wait resolves to the machine last seen
// at the URL (resolveMachine), which is the very link a swap breaks and so
// proves nothing. A weak frozen identity has nothing stronger to be confirmed
// by, so a readable probe that is not a swap counts.
func (fw frozenWorker) confirms(h remoteHealth) bool {
	return !isStrongKey(fw.key) || cleanMachineID(h.MachineID) != ""
}
