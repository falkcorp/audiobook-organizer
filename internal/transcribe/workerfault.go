// file: internal/transcribe/workerfault.go
// version: 1.1.0
// guid: ba9416a6-0f06-432d-ba9c-534d5579ab16
// last-edited: 2026-10-26

package transcribe

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// ErrWorkerFault marks a response in which the WORKER, not the clips, is what
// failed: it could not start its decoder, or most of one response's clips ran
// out their deadline (or killed the decoder) at once. A clip is not at fault when
// most of the clips beside it fail the same way in one response; the common
// cause is the worker (swapping, or sharing its box with a CI job).
//
// It is a failure of the ENDPOINT. The dispatcher benches the endpoint and
// re-queues the faulted jobs to workers they have not faulted on, in units too
// small to fault again on timeouts alone, and defers a job (PathErrWorkerFault)
// once it has faulted on two workers or on every worker left; see dispatchPool.
// The maintenance ops write no strike for a clip only a failing worker answered.
//
// It is NOT a network failure: the worker answered. isTransportishError does not
// recognise it, so an op never waits it out as an outage and re-sends the same
// jobs into the same fault.
var ErrWorkerFault = errors.New("whisper worker fault")

// WorkerFaultError is the typed form of ErrWorkerFault.
type WorkerFaultError struct {
	URL    string
	Reason string
	// Faulted holds the worker's results that were NOT trusted as verdicts
	// about their files. They are deliberately absent from the results map the
	// sender returns, so a caller cannot apply them by accident.
	Faulted map[string]BatchResult
	// Followers maps a faulted job to the ids that were not sent because their
	// audio is byte-identical to it (the upload journal's content dedup). They
	// share its fate: re-sent with it, deferred with it. Nil for path jobs.
	Followers map[string][]string
}

// workerLevel reports whether any faulted result says the worker itself cannot
// decode right now (worker_misconfigured, decoder_killed), as opposed to a
// response that is merely mostly timeouts. Single-endpoint dispatch stops
// sending to a worker-level fault; a timeout majority it works around.
func (e *WorkerFaultError) workerLevel() bool {
	for _, r := range e.Faulted {
		if isWorkerLevelCode(r.Error) {
			return true
		}
	}
	return false
}

// isWorkerLevelCode is a per-job answer that is about the worker at any count:
// one is enough to fault the response.
func isWorkerLevelCode(errText string) bool {
	switch PathErrorCode(errText) {
	case PathErrWorkerMisconfigured, PathErrDecoderKilled:
		return true
	}
	return false
}

// Error must NOT contain any of transportWordings: classifyTransport reads
// those to decide whether a failure is a network one, and a worker fault is a
// worker that ANSWERED.
func (e *WorkerFaultError) Error() string {
	return fmt.Sprintf("worker fault: %s answered but %s", e.URL, e.Reason)
}

// Is makes errors.Is(err, ErrWorkerFault) true for every *WorkerFaultError.
func (e *WorkerFaultError) Is(target error) bool { return target == ErrWorkerFault }

// IsWorkerFault reports whether err is (or wraps) a worker-fault failure.
func IsWorkerFault(err error) bool { return errors.Is(err, ErrWorkerFault) }

// timeoutBreakerMin is the fewest strike-type results in ONE response that can
// be read as a worker fault. Two was too few: two genuine loopers in a
// three-clip request are two clips, not a sick worker, and a request of two
// clips would have been a fault on a single clip's evidence.
const timeoutBreakerMin = 3

// IsStrikeError reports whether a worker's per-clip error text is of the kind a
// maintenance op counts as a strike against the clip: the decoder ran out its
// deadline on it (transcribe_timeout), or died while holding it
// (decoder_crashed). Both are evidence about the clip when they are rare in a
// response, and about the worker when they are not.
func IsStrikeError(errText string) bool {
	switch PathErrorCode(errText) {
	case PathErrTranscribeTimeout, PathErrDecoderCrashed:
		return true
	}
	return false
}

// workerFaultIn returns the results of ONE worker response that must not be
// read as verdicts about their clips. res is a single HTTP response's results,
// so the majority test has the right denominator: it is the worker's own batch.
//
//   - Every worker_misconfigured or decoder_killed result: the worker cannot
//     decode at all, or something outside the decoder killed it mid-clip.
//   - The strike-type results (IsStrikeError) when there are at least
//     timeoutBreakerMin of them and they are most of the response.
func workerFaultIn(res map[string]BatchResult) map[string]BatchResult {
	var faulted map[string]BatchResult
	add := func(id string, r BatchResult) {
		if faulted == nil {
			faulted = make(map[string]BatchResult)
		}
		faulted[id] = r
	}
	strikes := 0
	for _, r := range res {
		if IsStrikeError(r.Error) {
			strikes++
		}
	}
	strikesAreFault := strikes >= timeoutBreakerMin && strikes*2 > len(res)
	for id, r := range res {
		switch {
		case isWorkerLevelCode(r.Error):
			add(id, r)
		case strikesAreFault && IsStrikeError(r.Error):
			add(id, r)
		}
	}
	return faulted
}

// splitWorkerFault separates a response into the results to keep and the fault
// that explains the rest. fault is nil when nothing in res is faulted.
func splitWorkerFault(url string, res map[string]BatchResult) (kept map[string]BatchResult, fault *WorkerFaultError) {
	faulted := workerFaultIn(res)
	if len(faulted) == 0 {
		return res, nil
	}
	kept = make(map[string]BatchResult, len(res)-len(faulted))
	for id, r := range res {
		if _, bad := faulted[id]; !bad {
			kept[id] = r
		}
	}
	return kept, &WorkerFaultError{URL: url, Reason: describeFault(faulted, len(res)), Faulted: faulted}
}

func describeFault(faulted map[string]BatchResult, of int) string {
	codes := make(map[string]int)
	for _, r := range faulted {
		codes[PathErrorCode(r.Error)]++
	}
	parts := make([]string, 0, len(codes))
	for _, c := range slices.Sorted(maps.Keys(codes)) {
		parts = append(parts, fmt.Sprintf("%d x %s", codes[c], c))
	}
	return fmt.Sprintf("%d of its %d result(s) are the worker's, not the clips' (%s)", len(faulted), of, strings.Join(parts, ", "))
}
