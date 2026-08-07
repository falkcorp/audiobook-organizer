// file: internal/transcribe/errors_test.go
// version: 1.0.0
// guid: 72b76023-5bb6-4b6b-8356-20ff576c40a6
// last-edited: 2026-08-07

package transcribe

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
)

// TestClassifyTransport_RealOutageErrors feeds the classifier the error shapes
// actually recorded during the 2026-07-01 outage. Every one of these produced a
// false whisper_error on a book; every one must now be classified as transport.
func TestClassifyTransport_RealOutageErrors(t *testing.T) {
	outage := []string{
		`Post "http://host:8000/transcribe-batch": dial tcp: connect: connection refused`,
		`Post "http://host:8000/transcribe": context deadline exceeded`,
		`Get "http://host:8000/health": dial tcp: lookup host: no such host`,
		"read tcp 10.0.0.1:5000->10.0.0.2:8000: connection reset by peer",
		"unexpected EOF",
		"net/http: TLS handshake timeout",
		"dial tcp 10.0.0.2:8000: connect: network is unreachable",
	}
	for _, msg := range outage {
		t.Run(msg, func(t *testing.T) {
			got := classifyTransport([]string{"ep1"}, errors.New(msg))
			if !IsTransport(got) {
				t.Fatalf("outage error was NOT classified as transport: %q\n"+
					"this is the exact shape that falsified 34,000 book statuses", msg)
			}
		})
	}
}

// TestClassifyTransport_StructuralNetErrors covers the typed paths, which are
// more reliable than string matching.
func TestClassifyTransport_StructuralNetErrors(t *testing.T) {
	cases := map[string]error{
		"net.OpError": &net.OpError{
			Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused"),
		},
		"url.Error": &url.Error{
			Op: "Post", URL: "http://ep/transcribe", Err: errors.New("boom"),
		},
		"wrapped net.OpError": fmt.Errorf("transcribe remote: %w",
			&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}),
		"net.ErrClosed": fmt.Errorf("send: %w", net.ErrClosed),
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			if !IsTransport(classifyTransport(nil, err)) {
				t.Fatalf("%s should classify as transport, got %v", name, err)
			}
		})
	}
}

// TestClassifyTransport_FailsClosed is the safety property: an error we do not
// recognise must be treated as transport, never as a per-file verdict.
func TestClassifyTransport_FailsClosed(t *testing.T) {
	weird := errors.New("gateway exploded in a novel and unprecedented manner")
	got := classifyTransport([]string{"ep1"}, weird)
	if !IsTransport(got) {
		t.Fatal("unrecognised error must fail CLOSED (transport), so that no " +
			"per-file status is written on an error we cannot explain")
	}
}

// TestClassifyTransport_NilStaysNil guards the happy path.
func TestClassifyTransport_NilStaysNil(t *testing.T) {
	if got := classifyTransport([]string{"ep1"}, nil); got != nil {
		t.Fatalf("nil error must stay nil, got %v", got)
	}
}

// TestTransportError_NamesEveryEndpoint — the log line must name the whole pool,
// not just whichever endpoint happened to fail last, or diagnosing a partial
// outage means guessing.
func TestTransportError_NamesEveryEndpoint(t *testing.T) {
	te := &TransportError{
		Endpoints: []string{"http://a:8000", "http://b:8000"},
		Err:       errors.New("connection refused"),
	}
	msg := te.Error()
	for _, want := range []string{"http://a:8000", "http://b:8000", "connection refused"} {
		if !strings.Contains(msg, want) {
			t.Errorf("TransportError message %q missing %q", msg, want)
		}
	}
}

// TestTransportError_Unwraps keeps errors.Is/As working through the wrapper.
func TestTransportError_Unwraps(t *testing.T) {
	sentinel := errors.New("root cause")
	te := &TransportError{Endpoints: []string{"ep"}, Err: sentinel}
	if !errors.Is(te, sentinel) {
		t.Fatal("TransportError must unwrap to its cause")
	}
}

// TestClassifyTransport_RecognizedFlag — the flag must distinguish a known
// network failure from one we defaulted on, since both write nothing and the
// log line is the only place the difference can surface.
func TestClassifyTransport_RecognizedFlag(t *testing.T) {
	var te *TransportError

	known := classifyTransport([]string{"ep"}, errors.New("connection refused"))
	if !errors.As(known, &te) || !te.Recognized {
		t.Error("a known network failure must be marked Recognized")
	}
	if !strings.Contains(known.Error(), "transport failure") {
		t.Errorf("known failure worded unexpectedly: %q", known.Error())
	}

	novel := classifyTransport([]string{"ep"}, errors.New("gateway exploded"))
	if !errors.As(novel, &te) || te.Recognized {
		t.Error("an unexplained failure must NOT be marked Recognized")
	}
	if !strings.Contains(novel.Error(), "unclassified") {
		t.Errorf("novel failure must be visibly unclassified, got %q", novel.Error())
	}
}
