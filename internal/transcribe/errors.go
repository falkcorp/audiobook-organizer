// file: internal/transcribe/errors.go
// version: 1.3.2
// guid: e630c83e-990b-47c4-ac80-dea9e41b20c5
// last-edited: 2026-10-30

package transcribe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// ErrWorkerStuck marks a request that a worker accepted (its /health answered)
// and then did not answer within the request timeout. That is not an outage:
// the endpoint is reachable but wedged, usually on one clip that sends the
// decoder into an endless loop. The distinction matters because the two call
// for opposite handling. An outage is waited out and the SAME batch re-sent;
// re-sending a batch to a wedged worker parks it behind the stuck job, and
// handing it to the next worker is how one bad clip took out all four on
// 2026-10-23. A stuck batch is instead split and its parts retried elsewhere
// until the culprit is a single job (see dispatchPool).
var ErrWorkerStuck = errors.New("whisper worker stuck")

// WorkerStuckError is the typed form of ErrWorkerStuck: which endpoint, how
// long the request ran, and which jobs were in the stuck request, so the
// dispatcher can split exactly that request and nothing else.
type WorkerStuckError struct {
	URL   string
	After time.Duration
	// Jobs are the ids of the jobs in the stuck request.
	Jobs []string
	// Followers maps a job in Jobs to the ids that were NOT sent because their
	// audio is byte-identical to it (the journal's content dedup). They share
	// its fate: the dispatcher splits and condemns them with it instead of
	// re-sending each one alone as if it were an unrelated file. Nil for path
	// jobs, which are never assumed identical without reading them.
	Followers map[string][]string
}

func (e *WorkerStuckError) Error() string {
	return fmt.Sprintf("worker stuck: %s did not answer a request of %d job(s) within %s", e.URL, len(e.Jobs), e.After.Round(time.Second))
}

// Is makes errors.Is(err, ErrWorkerStuck) true for every *WorkerStuckError.
func (e *WorkerStuckError) Is(target error) bool { return target == ErrWorkerStuck }

// IsWorkerStuck reports whether err is (or wraps) a stuck-worker failure.
func IsWorkerStuck(err error) bool { return errors.Is(err, ErrWorkerStuck) }

// stuckFromHTTP converts a failed client call into a *WorkerStuckError when the
// failure is the client's own request timeout and the caller's context is
// still live and the request was already accepted, and returns err untouched
// otherwise. The ctx check is what keeps
// an operator cancelling the op, whose error text also says "context deadline
// exceeded" or "canceled", from being read as a stuck worker.
func stuckFromHTTP(ctx context.Context, err error, url string, after time.Duration, jobs []string) error {
	if err == nil || ctx.Err() != nil {
		return err
	}
	// A timeout while DIALLING means the endpoint is unreachable, which is the
	// outage case, not a wedged worker: the request never reached it.
	if op, ok := errors.AsType[*net.OpError](err); ok && op.Op == "dial" {
		return err
	}
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return &WorkerStuckError{URL: url, After: after, Jobs: jobs}
	}
	return err
}

// TransportError reports that the request never reached a working model: the
// endpoint was unreachable, refused the connection, timed out at the transport
// layer, or returned a server-side error before any audio was processed.
//
// 🔴 It carries NO per-file meaning. A caller that receives a TransportError
// learns nothing about any individual file and MUST NOT write a per-file
// transcription status. It says "we did not ask", not "the answer was no".
//
// This distinction is the whole reason this type exists. On 2026-07-01 a
// day-long endpoint outage was recorded as ~34,000 individual whisper_error
// verdicts, because the caller could not tell a dead socket from an
// unintelligible file. Repairing that took a dedicated maintenance op
// (maintenance.repair-transcribe-status, applied 2026-08-07: 30,820 books).
//
// Endpoints lists every endpoint attempted, so the log line names the whole
// pool rather than only the last one to fail.
type TransportError struct {
	Endpoints []string
	Err       error
	// Recognized is true when the cause matched a known network-failure shape.
	// False means "we could not explain this error and defaulted to transport".
	// It affects LOG WORDING ONLY — never the decision to write, which is the
	// same either way. Surfacing it keeps a novel failure mode visible instead
	// of letting it hide inside a familiar-looking bucket.
	Recognized bool
}

func (e *TransportError) Error() string {
	kind := "transport failure"
	switch {
	case IsWorkerStuck(e.Err):
		kind = "worker stuck (reachable, but did not answer)"
	case !e.Recognized:
		kind = "unclassified batch failure (treated as transport)"
	}
	if len(e.Endpoints) == 0 {
		return fmt.Sprintf("whisper %s: %v", kind, e.Err)
	}
	return fmt.Sprintf("whisper %s (tried %s): %v",
		kind, strings.Join(e.Endpoints, ", "), e.Err)
}

func (e *TransportError) Unwrap() error { return e.Err }

// IsTransport reports whether err is (or wraps) a TransportError.
func IsTransport(err error) bool {
	var te *TransportError
	return errors.As(err, &te)
}

// classifyTransport wraps a batch-level error as a TransportError.
//
// 🔴 IT WRAPS EVERY ERROR, not only the recognised network ones. That is
// deliberate, and it rests on a property of this API:
//
//	A per-file transcription failure is NEVER reported through this error.
//	It arrives as BatchResult.Error inside a SUCCESSFUL return.
//
// So a non-nil error from the batch call is always about the batch as a whole —
// a dead endpoint, a JSON encode failure, a temp-file error — and none of those
// license a verdict about any individual file. "Errors write nothing" is
// therefore both simpler and stricter than "transport errors write nothing",
// and it cannot be defeated by a failure mode nobody anticipated.
//
// Recognized records whether the cause matched a known network shape, purely so
// a novel failure is visible in the log rather than silently filed under a
// familiar heading.
//
// Compare repair_transcribe_status.isTransportFailure, which is conservative in
// the OPPOSITE direction: there an unrecognised error means "leave the existing
// row alone". Both default to refusing to assert a per-file verdict they cannot
// justify — the asymmetry is in which action is the safe one, not in the
// principle.
func classifyTransport(endpoints []string, err error) error {
	if err == nil {
		return nil
	}
	if IsTransport(err) {
		return err
	}
	return &TransportError{
		Endpoints:  endpoints,
		Err:        err,
		Recognized: isTransportishError(err),
	}
}

// isTransportishError recognises network-layer failures structurally where it
// can (net.Error, *url.Error, net.OpError) and by wording only as a fallback,
// since Go's HTTP stack surfaces some conditions as bare errors.
//
// A worker fault (ErrWorkerFault) is deliberately NOT recognised, even when it
// arrives wrapped in a net-looking chain: the worker answered. The per-file op
// waits out and re-sends a recognised failure five times and then stops the run,
// which for a fault caused by the batch's own clips (a response that is mostly
// genuine loopers) re-sent the same clips into the same fault and then stopped
// every later run at the same batch. A fault is handled by the dispatcher
// instead (re-queue elsewhere, then defer), and what reaches an op is deferred.
// A mid-request swap (ErrWorkerSwapped) is not one either: the request was ended
// on purpose because another machine answers at the URL, and the jobs are
// deferred without counting toward stopping a run. Nor is a response withheld
// because its /health probe could not be read (ErrAnswerUnverified): the worker
// answered, and nothing is known about it.
func isTransportishError(err error) bool {
	if IsWorkerFault(err) || IsWorkerSwapped(err) || IsWorkerUnverified(err) {
		return false
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}
	if _, ok := errors.AsType[*net.OpError](err); ok {
		return true
	}
	if _, ok := errors.AsType[*url.Error](err); ok {
		return true
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}

	e := strings.ToLower(err.Error())
	for _, m := range transportWordings {
		if strings.Contains(e, m) {
			return true
		}
	}
	return false
}

// transportWordings mirrors the marker list in
// internal/plugins/maintenance/repair_transcribe_status.go so the forward path
// and the repair path agree on what a transport failure looks like. Matching is
// on generic HTTP/network wording, never on any host address.
var transportWordings = []string{
	`post "http`,
	"connection refused",
	"no such host",
	"context deadline exceeded",
	"connection reset",
	"i/o timeout",
	"unexpected eof",
	": eof",
	"dial tcp",
	"server misbehaving",
	"network is unreachable",
	"no route to host",
	"broken pipe",
	"tls handshake",
}
