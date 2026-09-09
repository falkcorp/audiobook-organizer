// file: internal/ai/retry.go
// version: 1.5.0
// guid: f6a7b8c9-d0e1-2345-fabc-678901234567
// last-edited: 2026-09-09

// Package ai — shared retry helper for OpenAI / Ollama API calls.
package ai

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"syscall"
	"time"

	"github.com/openai/openai-go/v3"
)

// PermanentError wraps an error that is known to be non-retryable — e.g. an
// HTTP 400/401/403/404 response, or a 429 carrying a quota-exhaustion marker in
// either its "type" or "code" field (see quota429Markers). Callers can
// distinguish "will never succeed" from "exhausted retries" via
// errors.As(err, &PermanentError{}).
type PermanentError struct {
	Err error
}

func (e *PermanentError) Error() string {
	return e.Err.Error()
}

func (e *PermanentError) Unwrap() error {
	return e.Err
}

// isPermanentAIError reports whether err is an OpenAI API error that can
// never succeed on retry: HTTP 400/401/403/404, or a HTTP 429 that carries a
// quota-exhaustion marker (quota exhaustion is permanent; plain rate-limit 429s
// are transient — see isPermanentQuota429 for how the two are told apart).
// Errors that are not *openai.Error (network/timeout errors,
// context cancellation, etc.) are treated as transient/unknown and retried,
// matching prior behavior.
func isPermanentAIError(err error) bool {
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.StatusCode {
	case 400, 401, 403, 404:
		return true
	case 429:
		return isPermanentQuota429(apiErr)
	default:
		return false
	}
}

// isPermanentQuota429 distinguishes the two errors that both arrive as HTTP 429:
// quota/credit exhaustion, which no retry can clear, from an ordinary rate limit,
// which is exactly what the backoff exists to absorb. Getting this wrong is costly
// in BOTH directions, which is why it is its own function:
//
//   - Too narrow (miss a real exhaustion): DoWithRetry burns the full attempt budget
//     with quadratic backoff against an API that cannot succeed. On 2026-08-16 this
//     failed all 77 batches of a scan, the phase reported no progress, and the
//     watchdog cancelled at five minutes — discarding a completed 3,917-file walk.
//   - Too broad (flag a plain rate limit): AI parsing is disabled for the remainder
//     of the run on one throttled call. Quieter, because the scan still completes,
//     but it silently degrades every batch after it.
//
// The field trap: openai-go's Error carries BOTH `Code` (json:"code") and
// `Type` (json:"type"). The captured production payload
// (internal/scanner/ai_failure_test.go:23, copied from the incident journal rather
// than composed) sets them to DIFFERENT values:
//
//	"type": "insufficient_quota",  "code": "credit_balance_exhausted"
//
// while a plain rate limit sets "type": "rate_limit_error", "code":
// "rate_limit_exceeded". Any predicate here must return true for the first and
// false for the second.
//
// Both fields are checked against every marker deliberately. Reading one field is
// what broke this before — until 2026-08-23 the branch was
// `apiErr.Code == "insufficient_quota"`, which is false on the payload above —
// and simply moving to `Type` would swap one single-field assumption for another.
// The markers are provider strings, not HTTP semantics, so a provider is free to
// report either one in either field.
func isPermanentQuota429(apiErr *openai.Error) bool {
	for _, m := range quota429Markers {
		if apiErr.Type == m || apiErr.Code == m {
			return true
		}
	}
	return false
}

// quota429Markers are the provider strings that mean the account's balance or
// quota is gone — a state the next request cannot clear. Ordinary throttling
// ("rate_limit_error" / "rate_limit_exceeded") is deliberately absent: it is
// transient and belongs to the backoff.
var quota429Markers = []string{
	"insufficient_quota",
	"credit_balance_exhausted",
}

// UnreachableError wraps an error that means no connection was ever
// established: the dial failed, or the hostname did not resolve.
//
// It is deliberately NOT a PermanentError, and the difference is load-bearing.
// PermanentError means "this request is wrong and no backend will accept it",
// and internal/scanner's parser chain aborts the whole phase on one. An
// unreachable backend says nothing about the request — the next rung in the
// chain, or the next run, may answer it perfectly well — so this type must
// stay outside isPermanentAIFailure's classification, which checks for
// *PermanentError by type and then for provider markers that no dial error
// contains. What the two share is only that retrying THIS backend inside THIS
// request is pointless.
type UnreachableError struct {
	Err error
}

func (e *UnreachableError) Error() string {
	return e.Err.Error()
}

func (e *UnreachableError) Unwrap() error {
	return e.Err
}

// isUnreachableError reports whether err means the connection was never made:
// connection refused, no route to host, network unreachable, or DNS failure.
//
// Why these stop the retry loop when other network errors do not, and why the
// cost runs in both directions — the same shape as isPermanentQuota429 above:
//
//   - Retrying them is what broke the parser chain. internal/scanner's
//     parserChain shares ONE deadline across its rungs and documents its own
//     premise as "an UNREACHABLE remote is cheap — a refused connection or a
//     DNS failure returns in milliseconds, so the next rung inherits almost the
//     full budget, which is the case this chain exists for". This function is
//     what makes that sentence true. Without it a dead host consumed the entire
//     30s budget — measured in production on 2026-09-09, when the configured
//     local base URL had a one-digit typo in its host and so named an address
//     nothing answered on: 81 of 81 library.ai-parse runs parsed zero books,
//     every one of them at the 30s ceiling. budgetRemains then skipped every
//     fallback rung, and the chain built precisely for an unreachable remote
//     was defeated by the retry loop underneath it.
//   - The cost of stopping early is real: a host that is mid-reboot would have
//     answered on attempt 2. We take that trade because a failed dial leaves no
//     partial state to lose, and because the right retry for "the backend is
//     down" is the next rung or the next run — not a 10-second in-request
//     backoff spending a budget that the fallback needs. Callers with no
//     fallback beneath them (metadata_llm_review.go, the single-item parse
//     paths in openai_parser.go) pay this: they now surface the outage in
//     ~1 attempt instead of masking a brief one. That is the intended trade,
//     not an oversight.
//
// A dial aborted by the caller's OWN cancellation or deadline is excluded and
// checked first: that error describes our budget, not the backend's health, and
// misfiling it here would report a healthy host as unreachable.
//
// NOTE — this only governs OUR retry loop. openai-go runs a second one beneath
// it (requestconfig.go: MaxRetries defaults to 2 and shouldRetry returns true
// whenever res == nil, i.e. for exactly these errors), so one DoWithRetry
// attempt is really three dials plus ~1.5s of the SDK's own backoff. That loop
// cannot be told about this classification: its opt-out is errors.As against an
// unexported `interface{ noRetry() }` declared in the SDK's internal package,
// and Go only lets same-package types satisfy an unexported interface method.
// The only lever from outside is option.WithMaxRetries, which is all-or-nothing
// and would also discard the SDK's Retry-After handling for genuine 429s — a
// wider change than this bug warrants. So a dead host costs ~5s here rather
// than the ~0ms the chain's comment imagines. That is well inside
// minRungBudget, which is what the fallback actually depends on.
func isUnreachableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if _, ok := errors.AsType[*net.DNSError](err); ok {
		return true
	}
	// Op is checked because a *net.OpError also covers read/write failures on
	// an ESTABLISHED connection, which are ordinary transient errors and must
	// keep their retries.
	if opErr, ok := errors.AsType[*net.OpError](err); ok && opErr.Op == "dial" {
		return true
	}
	// Belt and braces for errors that reached us stripped of their OpError
	// wrapper. These are the three the production failure produced.
	return errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH)
}

// DoWithRetry calls fn up to maxAttempts times. Between attempts it sleeps
// attempt² × base (quadratic back-off), respecting ctx cancellation.
// Returns nil on the first success; returns the last error if all attempts fail.
//
// Usage pattern for closures that capture their result:
//
//	var result T
//	err := DoWithRetry(ctx, p.maxRetries+1, 2*time.Second, func() error {
//	    var innerErr error
//	    result, innerErr = callAPI(ctx, ...)
//	    return innerErr
//	})
func DoWithRetry(ctx context.Context, maxAttempts int, base time.Duration, fn func() error) error {
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt*attempt) * base
			slog.Warn("ai call failed, retrying after backoff",
				"attempt", attempt, "max_attempts", maxAttempts, "backoff", backoff, "err", lastErr)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}
		if err := fn(); err != nil {
			if isPermanentAIError(err) {
				return &PermanentError{Err: err}
			}
			// Checked after the permanent classification, never before: a
			// backend can refuse the connection on a request that is ALSO
			// malformed, and "the request is wrong" is the more actionable of
			// the two — it stops the phase instead of walking a chain of
			// backends that will all reject it identically.
			if isUnreachableError(err) {
				slog.Warn("ai call reached no backend, not retrying this one",
					"attempt", attempt+1, "max_attempts", maxAttempts, "err", err)
				return &UnreachableError{Err: err}
			}
			lastErr = err
			continue
		}
		return nil
	}
	slog.Error("ai call retries exhausted", "max_attempts", maxAttempts, "err", lastErr)
	return lastErr
}
