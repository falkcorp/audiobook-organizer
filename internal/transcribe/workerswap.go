// file: internal/transcribe/workerswap.go
// version: 1.1.0
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
	// Unverified marks a response withheld because the /health probe that
	// follows every successful response could not be read (Now is then empty):
	// it cannot be told from the frozen machine's, so it is treated like a swap
	// (see verifyAfterResponse).
	Unverified bool
}

func (e *WorkerSwappedError) Error() string {
	if e.Unverified {
		return fmt.Sprintf("worker not verified: the machine behind %s could not be confirmed after a request of %d job(s) was answered, so the answer is withheld",
			e.URL, len(e.Jobs))
	}
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
