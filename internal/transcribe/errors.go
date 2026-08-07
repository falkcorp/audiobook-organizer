// file: internal/transcribe/errors.go
// version: 1.0.0
// guid: e630c83e-990b-47c4-ac80-dea9e41b20c5
// last-edited: 2026-08-07

package transcribe

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

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
	if !e.Recognized {
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
func isTransportishError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
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
