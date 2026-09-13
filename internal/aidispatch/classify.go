// file: internal/aidispatch/classify.go
// version: 1.0.0
// guid: ebee4a76-0f9a-4549-8b18-55826f23b822
// last-edited: 2026-09-13

package aidispatch

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/url"
	"strings"
	"syscall"

	"github.com/openai/openai-go/v3"
)

// Class is what the dispatcher does after an attempt returns.
//
// It merges the two classifiers that existed before this package:
// internal/transcribe/errors.go (is this a transport failure?) and
// internal/ai/retry.go (is this permanent? is this an unreachable host? is this
// 429 a quota exhaustion or a plain rate limit?). Those two keep working for
// their own callers; this one answers the dispatcher's question, which is
// neither of theirs: "may another endpoint take this request?"
type Class int

const (
	// ClassOK: success.
	ClassOK Class = iota
	// ClassFailover: the endpoint failed (connection, TLS, 5xx, tunnel 502/503,
	// plain 429 rate limit, auth). Bench it and try the next one.
	ClassFailover
	// ClassQuotaExhausted: 429 carrying a quota/credit marker. Fail over, with
	// a long cooldown.
	ClassQuotaExhausted
	// ClassNoFailover: the endpoint answered and the answer is the problem
	// (unparseable, refusal, short array, 4xx validation). Return it.
	ClassNoFailover
	// ClassDeadline: the per-attempt deadline expired while the caller's own
	// context is still live. Fail over once (PLAN decision 7).
	ClassDeadline
	// ClassStop: the caller's context is done, or no slot could be acquired.
	ClassStop
)

func (c Class) String() string {
	switch c {
	case ClassOK:
		return "ok"
	case ClassFailover:
		return "failover"
	case ClassQuotaExhausted:
		return "quota_exhausted"
	case ClassNoFailover:
		return "no_failover"
	case ClassDeadline:
		return "deadline"
	case ClassStop:
		return "stop"
	}
	return "unknown"
}

// quota429Markers mirrors internal/ai/retry.go. Both the "type" and the "code"
// field are checked against every marker: the captured production payload sets
// them to DIFFERENT values ("insufficient_quota" / "credit_balance_exhausted"),
// and reading one field is what broke this before.
var quota429Markers = []string{
	"insufficient_quota",
	"credit_balance_exhausted",
}

// Classify decides what the dispatcher does with err. parent is the CALLER's
// context, not the per-attempt one: a done parent means stop, while a deadline
// on a live parent means the per-attempt timeout fired.
//
// The order is load-bearing:
//  1. a done parent wins over everything -- the error then describes our own
//     budget, not the endpoint;
//  2. explicit caller marks (Quality, EndpointFailure) beat inference;
//  3. HTTP status beats transport shape, because a status means the endpoint
//     was reached and answered;
//  4. an error nobody recognises does NOT fail over. Failing over on a novel
//     error could double the load on a quality problem; returning it makes it
//     visible. That is the opposite default from transcribe.classifyTransport,
//     and on purpose: that one decides whether to WRITE a per-file verdict,
//     where the safe answer is "no"; this one decides whether to SEND the
//     request again, where the safe answer is also "no".
func Classify(parent context.Context, err error) Class {
	if err == nil {
		return ClassOK
	}
	if parent != nil && parent.Err() != nil {
		return ClassStop
	}
	if errors.Is(err, ErrSlotWait) {
		return ClassStop
	}
	if _, ok := errors.AsType[*QualityError](err); ok {
		return ClassNoFailover
	}
	if _, ok := errors.AsType[*EndpointError](err); ok {
		return ClassFailover
	}
	if errors.Is(err, context.Canceled) {
		return ClassStop
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ClassDeadline
	}
	if apiErr, ok := errors.AsType[*openai.Error](err); ok {
		return classifyStatus(apiErr.StatusCode, apiErr.Type, apiErr.Code)
	}
	if se, ok := errors.AsType[*StatusError](err); ok {
		return classifyStatus(se.Status, se.Type, se.Code)
	}
	if isTransportError(err) {
		return ClassFailover
	}
	return ClassNoFailover
}

// classifyStatus applies the section 4 failover table to an HTTP status.
func classifyStatus(status int, typ, code string) Class {
	switch {
	case status == 429:
		for _, m := range quota429Markers {
			if typ == m || code == m {
				return ClassQuotaExhausted
			}
		}
		// A plain rate limit is this endpoint being busy; another may not be.
		return ClassFailover
	case status >= 500:
		// Includes the tunnel's 502/503 when the Mac behind it is down.
		return ClassFailover
	case status == 401 || status == 403:
		// Credentials are per endpoint. internal/ai/retry.go calls these
		// permanent because it retries the SAME endpoint; for choosing a
		// DIFFERENT endpoint they say nothing about the request.
		return ClassFailover
	case status == 408:
		return ClassFailover
	default:
		// 400, 404, 413, 422 and every other 4xx: the request, the model name
		// or the payload was rejected. Another endpoint would reject it too,
		// or worse, accept it and hide the problem.
		return ClassNoFailover
	}
}

// isTransportError recognises a request that never reached a working model.
// Structural checks first, wording last, as in transcribe/errors.go (whose
// marker list this mirrors), since Go's HTTP stack surfaces some conditions as
// bare errors.
func isTransportError(err error) bool {
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}
	if _, ok := errors.AsType[*net.OpError](err); ok {
		return true
	}
	if _, ok := errors.AsType[*net.DNSError](err); ok {
		return true
	}
	if _, ok := errors.AsType[*url.Error](err); ok {
		return true
	}
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return true
	}
	if _, ok := errors.AsType[tls.RecordHeaderError](err); ok {
		return true
	}
	if _, ok := errors.AsType[x509.UnknownAuthorityError](err); ok {
		return true
	}
	if _, ok := errors.AsType[x509.HostnameError](err); ok {
		return true
	}
	if errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EPIPE) {
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

// transportWordings mirrors internal/transcribe/errors.go. Generic HTTP and
// network wording only, never a host address. "context deadline exceeded" is
// omitted: a deadline is classified structurally above, and a bare string
// match would turn a per-attempt timeout into an unlimited failover.
var transportWordings = []string{
	`post "http`,
	"connection refused",
	"no such host",
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
	"x509:",
}
