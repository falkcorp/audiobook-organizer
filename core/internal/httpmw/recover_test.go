// file: core/internal/httpmw/recover_test.go
// version: 1.0.0
// guid: e04f73b9-d420-4ce3-95b5-9947ae53b881
// last-edited: 2026-11-08

package httpmw

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// rawLog is a text slog handler with no redaction of its own, so a test sees
// exactly what a stage wrote: in production the pipeline's attribute
// redactor would mask some leaks a stage let through, and hide them.
type rawLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *rawLog) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(lockedWriter{l}, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

type lockedWriter struct{ l *rawLog }

func (w lockedWriter) Write(b []byte) (int, error) {
	w.l.mu.Lock()
	defer w.l.mu.Unlock()
	return w.l.buf.Write(b)
}

func (l *rawLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// accessLines are the access records, one text line each.
func (l *rawLog) accessLines() []string {
	var out []string
	for line := range strings.SplitSeq(l.String(), "\n") {
		if strings.Contains(line, `msg="`+AccessMessage+`"`) {
			out = append(out, line)
		}
	}
	return out
}

// panickingChain is a chain whose handler panics with v, after writing a
// body when the query has write=1.
func panickingChain(t *testing.T, logs *rawLog, v any) http.Handler {
	t.Helper()
	c, err := New(Deps{Log: logs.logger()}, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	return c.Outer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("write") == "1" {
			_, _ = w.Write([]byte("partial"))
		}
		panic(v)
	}))
}

// serve runs req through h and returns the status written and the value
// that escaped the chain, if any.
func serve(h http.Handler, req *http.Request) (code int, escaped any) {
	rec := httptest.NewRecorder()
	func() {
		defer func() { escaped = recover() }()
		h.ServeHTTP(rec, req)
	}()
	return rec.Code, escaped
}

// TestRecovery_PanicWritesOneAccessLine: a panic unwinds past stage 4, so
// stage 3 writes the access record: exactly one per request, with the 500
// it answered, or the status already sent when the connection is aborted,
// and the redacted target. A request that does not panic gets stage 4's one
// record, not two.
func TestRecovery_PanicWritesOneAccessLine(t *testing.T) {
	const secret = "abk_penguin-umbrella-key"
	for _, tc := range []struct {
		name, target string
		panicWith    any
		status       string
		escapes      bool
	}{
		{"panic", "/api/v1/gadgets?token=" + secret, "Bane broke the bat", "status=500", false},
		{"panic after writing", "/api/v1/gadgets?write=1&token=" + secret, "Bane broke the bat", "status=200", true},
		{"abort", "/api/v1/gadgets?token=" + secret, http.ErrAbortHandler, "status=200", true},
		{"broken pipe", "/api/v1/gadgets?token=" + secret, syscall.EPIPE, "status=200", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := &rawLog{}
			code, escaped := serve(panickingChain(t, logs, tc.panicWith), httptest.NewRequest(http.MethodGet, tc.target, nil))
			if (escaped != nil) != tc.escapes {
				t.Fatalf("escaped panic = %v, want escape %v", escaped, tc.escapes)
			}
			if tc.name == "panic" && code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", code)
			}
			lines := logs.accessLines()
			if len(lines) != 1 {
				t.Fatalf("access lines = %d, want exactly 1:\n%s", len(lines), logs.String())
			}
			if !strings.Contains(lines[0], tc.status) || !strings.Contains(lines[0], "token=[REDACTED]") {
				t.Fatalf("access line = %s, want %s and the redacted target", lines[0], tc.status)
			}
			if strings.Contains(logs.String(), secret) {
				t.Fatalf("the log carries the credential:\n%s", logs.String())
			}
		})
	}
	// No panic: stage 4's record alone.
	logs := &rawLog{}
	c, err := New(Deps{Log: logs.logger()}, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	h := c.Outer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/gadgets", nil))
	if lines := logs.accessLines(); len(lines) != 1 || !strings.Contains(lines[0], "status=418") {
		t.Fatalf("access lines = %v, want one with status=418", lines)
	}
	// Stage 4 logged, then panicked (here: its log handler did): stage 3
	// recovers and answers, but writes no second record.
	logs = &rawLog{}
	c, err = New(Deps{Log: slog.New(panicAfterAccess{logs.logger().Handler()})}, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	h = c.Outer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/gadgets", nil))
	if lines := logs.accessLines(); len(lines) != 1 {
		t.Fatalf("access lines = %d after stage 4 logged and panicked, want 1:\n%s", len(lines), logs.String())
	}
}

// panicAfterAccess writes every record, then panics once it has written an
// access record.
type panicAfterAccess struct{ slog.Handler }

func (p panicAfterAccess) Handle(ctx context.Context, r slog.Record) error {
	err := p.Handler.Handle(ctx, r)
	if r.Message == AccessMessage {
		panic("Scarecrow's toxin in the log pipe")
	}
	return err
}

// TestRecovery_LogsNoQueryCredential (HEAD's RedactingRecovery test, on
// core's stage 3): the request line stage 3 logs masks a credential query
// value on the panic path and every lost-connection path, a credential
// after a "?" sent as %3F inside the path included, and a lost-connection
// error that quotes a URL with its api_key.
func TestRecovery_LogsNoQueryCredential(t *testing.T) {
	const secret = "abk_" + "recovery-secret"
	resetWithKey := &url.Error{Op: "Get", URL: "https://batcave.example.test/feed?api_key=" + secret,
		Err: fmt.Errorf("read: %w", syscall.ECONNRESET)}
	for name, tc := range map[string]struct {
		panicWith any
		target    string
		want      string // the masked request line, as logged
	}{
		"broken pipe":        {&net.OpError{Op: "write", Err: &os.SyscallError{Syscall: "write", Err: syscall.EPIPE}}, "/api/items/a/file/b?token=" + secret, "/api/items/a/file/b?token=[REDACTED]"},
		"abort":              {http.ErrAbortHandler, "/api/items/a/file/b?token=" + secret, "/api/items/a/file/b?token=[REDACTED]"},
		"wrapped epipe":      {fmt.Errorf("write body: %w", syscall.EPIPE), "/api/items/a/file/b?token=" + secret, "/api/items/a/file/b?token=[REDACTED]"},
		"wrapped econnreset": {fmt.Errorf("read: %w", syscall.ECONNRESET), "/api/items/a/file/b?token=" + secret, "/api/items/a/file/b?token=[REDACTED]"},
		"panic":              {"boom", "/api/items/a/file/b?token=" + secret, "/api/items/a/file/b?token=[REDACTED]"},
		"panic %3F":          {"boom", "/api/items/a/file/b%3Ftoken=" + secret, "/api/items/a/file/b%3Ftoken=[REDACTED]"},
		"broken pipe %3F":    {syscall.EPIPE, "/api/items/a/file/b%3Fwidth=4&token=" + secret + "?w=1", "/api/items/a/file/b%3Fwidth=4&token=[REDACTED]?w=1"},
		"econnreset api_key": {resetWithKey, "/api/items/a/file/b?api_key=" + secret, "/api/items/a/file/b?api_key=[REDACTED]"},
	} {
		t.Run(name, func(t *testing.T) {
			logs := &rawLog{}
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			req.Header.Set("Authorization", "Bearer "+secret)
			serve(panickingChain(t, logs, tc.panicWith), req)
			out := logs.String()
			if strings.Contains(out, "recovery-secret") {
				t.Fatalf("stage 3 leaked the credential:\n%s", out)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("stage 3 lost the request line %q:\n%s", tc.want, out)
			}
		})
	}
}

// TestRecovery_LogsNoInviteToken (HEAD's): an invite token in the path is
// masked in stage 3's request line on the panic and the broken-pipe path.
func TestRecovery_LogsNoInviteToken(t *testing.T) {
	const tok = "recovery-invite-token-example"
	for _, rt := range []struct{ method, target, want string }{
		{http.MethodGet, "/api/v1/auth/invite/" + tok, "/api/v1/auth/invite/[REDACTED] "},
		{http.MethodDelete, "/api/v1/users/invites/" + tok, "/api/v1/users/invites/[REDACTED] "},
		{http.MethodPost, "/api/v1/users/invites/" + tok + "/resend", "/api/v1/users/invites/[REDACTED]/resend "},
		{http.MethodGet, "/invite/" + tok, "/invite/[REDACTED] "},
	} {
		for name, panicWith := range map[string]any{"panic": "boom", "broken pipe": syscall.EPIPE} {
			t.Run(rt.method+" "+rt.target+" "+name, func(t *testing.T) {
				logs := &rawLog{}
				serve(panickingChain(t, logs, panicWith), httptest.NewRequest(rt.method, rt.target, nil))
				out := logs.String()
				if strings.Contains(out, tok) {
					t.Fatalf("stage 3 leaked the invite token:\n%s", out)
				}
				if !strings.Contains(out, rt.want) {
					t.Fatalf("stage 3 lost the readable path %q:\n%s", rt.want, out)
				}
			})
		}
	}
}

// TestRecovery_LogsNoEncodedSlashInviteToken (HEAD's): a token behind an
// encoded slash, a decoy segment or a dot segment is still masked. Stage 3
// logs the decoded path (the access record's target), so %2F arrives as "/"
// and the invite rule sees it.
func TestRecovery_LogsNoEncodedSlashInviteToken(t *testing.T) {
	const tok = "recovery-encoded-slash-token-example"
	for _, target := range []string{
		"/api/v1/auth/invite%2F" + tok,
		"/api/v1/auth/invite%2f" + tok + "?x=1",
		"/invite%2F" + tok,
		"/api/v1/users/invites%2F" + tok + "/resend",
		"/invite/%2F/" + tok,
		"/invite%2F%2E%2F" + tok,
		"/invite/./" + tok,
		"/invite%252F" + tok,
		"/invite%2F" + tok + "/..",
		"/x/../invite/" + tok + "/..",
	} {
		for name, panicWith := range map[string]any{"panic": "boom", "broken pipe": syscall.EPIPE} {
			t.Run(target+" "+name, func(t *testing.T) {
				logs := &rawLog{}
				serve(panickingChain(t, logs, panicWith), httptest.NewRequest(http.MethodGet, target, nil))
				out := logs.String()
				if strings.Contains(out, tok) {
					t.Fatalf("stage 3 leaked the invite token:\n%s", out)
				}
				if !strings.Contains(out, `request="GET /`) {
					t.Fatalf("stage 3 lost the request line:\n%s", out)
				}
			})
		}
	}
}
