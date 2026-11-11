// file: core/integrations/download/deluge/redirect_test.go
// version: 1.0.1
// guid: 6b0f3d1e-8c52-4a97-b3e4-d2a71f5c9e08
// last-edited: 2026-11-11

package deluge

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/download"
)

const loginPassword = "hunter2"

// sink is a second host the Web UI redirects to. It records every request it
// receives, body included.
type sink struct {
	mu   sync.Mutex
	hits []string
	srv  *httptest.Server
}

func newSink(t *testing.T) *sink {
	t.Helper()
	s := &sink{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.hits = append(s.hits, r.Method+" "+r.URL.Path+" "+string(b))
		s.mu.Unlock()
		_, _ = io.WriteString(w, `{"id":1,"result":true,"error":null}`)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *sink) got() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.hits...)
}

// redirector answers every request with status and a Location at target.
func redirector(t *testing.T, status int, target string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// An auth.login that answers with a redirect must not be followed: the
// password in the body goes to the configured host only.
func TestLogin_RedirectIsNeverFollowed(t *testing.T) {
	for _, status := range []int{
		http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			other := newSink(t)
			ui := redirector(t, status, other.srv.URL+"/json")
			// ui.Client() follows redirects by default, as a bare client does.
			c, err := newRPC(ui.URL, plainHTTP{ui.Client()}, staticSecret{pw: loginPassword})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.callAuthed(context.Background(), "web.connected")
			st, ok := errors.AsType[*statusError](err)
			if !ok || st.Status != status {
				t.Fatalf("err = %v; want a statusError %d", err, status)
			}
			if hits := other.got(); len(hits) != 0 {
				t.Fatalf("the redirect target received %d request(s): %q", len(hits), hits)
			}
			if s := err.Error(); strings.Contains(s, loginPassword) || strings.Contains(s, other.srv.URL) {
				t.Errorf("error text leaks %q", s)
			}
		})
	}
}

// A redirect on a later call is refused the same way, and the session cookie
// does not follow it either.
func TestCall_RedirectAfterLoginIsNeverFollowed(t *testing.T) {
	other := newSink(t)
	ui := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), `"auth.login"`) {
			http.SetCookie(w, &http.Cookie{Name: "_session_id", Value: "gotham", Path: "/"})
			_, _ = io.WriteString(w, `{"id":1,"result":true,"error":null}`)
			return
		}
		http.Redirect(w, r, other.srv.URL+"/json", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(ui.Close)
	c, err := newRPC(ui.URL, plainHTTP{ui.Client()}, staticSecret{pw: loginPassword})
	if err != nil {
		t.Fatal(err)
	}
	inst := &instance{rpc: c, ref: integrations.Ref{Kind: integrations.KindDownload, Instance: "deluge"}}
	_, err = inst.Items(context.Background(), download.ItemFilter{})
	if err == nil {
		t.Fatal("Items followed a redirect")
	}
	e, ok := errors.AsType[*integrations.Error](err)
	if !ok || e.Status != http.StatusTemporaryRedirect {
		t.Fatalf("err = %#v; want an *integrations.Error with status 307", err)
	}
	if hits := other.got(); len(hits) != 0 {
		t.Fatalf("the redirect target received %q", hits)
	}
}

// Whatever the server puts in an answer, the password never reaches an error
// string or an integrations.Error's Detail, even when the server echoes the
// request body back.
func TestLogin_PasswordNeverAppearsInErrors(t *testing.T) {
	for name, answer := range map[string]func(w http.ResponseWriter, body string){
		"HTTP 500 echoing the request": func(w http.ResponseWriter, body string) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "boom: "+body)
		},
		"HTTP 502 page": func(w http.ResponseWriter, body string) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "<html>"+body+"</html>")
		},
		"not JSON": func(w http.ResponseWriter, body string) { _, _ = io.WriteString(w, "echo "+body) },
		"an RPC error echoing the request": func(w http.ResponseWriter, body string) {
			_, _ = io.WriteString(w, `{"id":1,"result":null,"error":{"code":4,"message":"bad call `+strings.ReplaceAll(body, `"`, `'`)+`"}}`)
		},
		"login answers false": func(w http.ResponseWriter, body string) {
			_, _ = io.WriteString(w, `{"id":1,"result":false,"error":null}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			ui := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				answer(w, string(b))
			}))
			t.Cleanup(ui.Close)
			inst := &instance{
				rpc: mustRPC(t, ui.URL, ui.Client()),
				ref: integrations.Ref{Kind: integrations.KindDownload, Instance: "deluge"},
			}
			_, err := inst.Caps(context.Background())
			_, errItems := inst.Items(context.Background(), download.ItemFilter{})
			if errItems == nil {
				t.Fatal("Items: no error")
			}
			for _, e := range []error{err, errItems} {
				if e == nil {
					continue // Caps degrades instead of failing for some answers
				}
				if s := e.Error(); strings.Contains(s, loginPassword) {
					t.Errorf("error text leaks the password: %q", s)
				}
				if ie, ok := errors.AsType[*integrations.Error](e); ok && strings.Contains(ie.Detail, loginPassword) {
					t.Errorf("Detail leaks the password: %q", ie.Detail)
				}
			}
			// The raw client errors, before fail() classifies them.
			_, rawErr := inst.rpc.callAuthed(context.Background(), "web.connected")
			if rawErr == nil {
				t.Fatal("no error from the client")
			}
			for _, part := range collectErrorText(rawErr) {
				if strings.Contains(part, loginPassword) {
					t.Errorf("client error leaks the password: %q", part)
				}
			}
		})
	}
}

func mustRPC(t *testing.T, base string, c *http.Client) *rpc {
	t.Helper()
	r, err := newRPC(base, plainHTTP{c}, staticSecret{pw: loginPassword})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// collectErrorText is the Error text of err and of every error it wraps, plus
// the excerpt and message fields the typed errors keep.
func collectErrorText(err error) []string {
	var out []string
	for e := err; e != nil; e = errors.Unwrap(e) {
		out = append(out, e.Error())
		switch v := e.(type) {
		case *statusError:
			out = append(out, v.Excerpt)
		case *decodeError:
			out = append(out, v.Excerpt)
		case *rpcError:
			out = append(out, v.Message)
		}
	}
	return out
}
