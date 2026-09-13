// file: internal/aidispatch/classify_test.go
// version: 1.0.0
// guid: 41cdd908-8904-43c3-aefb-e7a950c448ef
// last-edited: 2026-09-13

package aidispatch

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"syscall"
	"testing"

	"github.com/openai/openai-go/v3"
)

func TestClassifyTable(t *testing.T) {
	live := context.Background()
	done, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name   string
		parent context.Context
		err    error
		want   Class
	}{
		{"nil", live, nil, ClassOK},

		// Stop: our own budget, never endpoint evidence.
		{"parent cancelled beats a transport error", done, dialRefused, ClassStop},
		{"slot wait", live, fmt.Errorf("%w for x: %w", ErrSlotWait, context.Canceled), ClassStop},
		{"bare cancel", live, context.Canceled, ClassStop},

		// Deadline with a live parent: the per-attempt timeout.
		{"attempt deadline", live, context.DeadlineExceeded, ClassDeadline},
		{"deadline inside url.Error", live, &url.Error{Op: "Post", URL: "http://x", Err: context.DeadlineExceeded}, ClassDeadline},

		// Explicit caller marks beat inference.
		{"quality mark on a transport-shaped error", live, Quality(dialRefused), ClassNoFailover},
		{"endpoint mark on an unknown error", live, EndpointFailure(errors.New("outside root")), ClassFailover},

		// OpenAI SDK statuses (merged from ai/retry.go).
		{"openai 429 quota by type", live, &openai.Error{StatusCode: 429, Type: "insufficient_quota"}, ClassQuotaExhausted},
		{"openai 429 quota by code (prod payload shape)", live, &openai.Error{StatusCode: 429, Type: "insufficient_quota", Code: "credit_balance_exhausted"}, ClassQuotaExhausted},
		{"openai 429 credit code only", live, &openai.Error{StatusCode: 429, Code: "credit_balance_exhausted"}, ClassQuotaExhausted},
		{"openai 429 plain rate limit", live, &openai.Error{StatusCode: 429, Type: "rate_limit_error", Code: "rate_limit_exceeded"}, ClassFailover},
		{"openai 500", live, &openai.Error{StatusCode: 500}, ClassFailover},
		{"openai 502 tunnel", live, &openai.Error{StatusCode: 502}, ClassFailover},
		{"openai 503 tunnel", live, &openai.Error{StatusCode: 503}, ClassFailover},
		{"openai 401 is per-endpoint credentials", live, &openai.Error{StatusCode: 401}, ClassFailover},
		{"openai 400 validation", live, &openai.Error{StatusCode: 400}, ClassNoFailover},
		{"openai 404 model not found", live, &openai.Error{StatusCode: 404}, ClassNoFailover},
		{"openai 422 validation", live, &openai.Error{StatusCode: 422}, ClassNoFailover},
		{"wrapped openai 503", live, fmt.Errorf("parse batch: %w", &openai.Error{StatusCode: 503}), ClassFailover},

		// Non-SDK statuses (whisper server, raw Ollama).
		{"status 503", live, &StatusError{Status: 503}, ClassFailover},
		{"status 400", live, &StatusError{Status: 400}, ClassNoFailover},
		{"status 429 quota code", live, &StatusError{Status: 429, Code: "insufficient_quota"}, ClassQuotaExhausted},

		// Transport (merged from transcribe/errors.go and ai/retry.go).
		{"dial refused", live, dialRefused, ClassFailover},
		{"dns", live, &net.DNSError{Err: "no such host", Name: "x.invalid"}, ClassFailover},
		{"bare ECONNREFUSED", live, syscall.ECONNREFUSED, ClassFailover},
		{"bare EHOSTUNREACH", live, fmt.Errorf("send: %w", syscall.EHOSTUNREACH), ClassFailover},
		{"read on established conn", live, &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, ClassFailover},
		{"post EOF wording", live, errors.New(`Post "http://h/transcribe-batch": EOF`), ClassFailover},
		{"tls handshake wording", live, errors.New("net/http: TLS handshake timeout"), ClassFailover},
		{"x509 unknown authority", live, x509.UnknownAuthorityError{}, ClassFailover},

		// Quality and the unknown default: no failover.
		{"json decode", live, errors.New("json: cannot unmarshal string into Go value"), ClassNoFailover},
		{"short result array", live, errors.New("got 3 results for 8 inputs"), ClassNoFailover},
		{"novel error defaults to no failover", live, errors.New("something nobody anticipated"), ClassNoFailover},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.parent, tc.err); got != tc.want {
				t.Fatalf("Classify(%v) = %s, want %s", tc.err, got, tc.want)
			}
		})
	}
}
