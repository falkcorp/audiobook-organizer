// file: core/integrations/download/deluge/helpers_test.go
// version: 1.0.0
// guid: 08620ad3-90d4-4ce9-864b-dc007eb0c3f3
// last-edited: 2026-11-11

package deluge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/secrets"
)

// Synthetic torrent ids (Batman-themed fixtures: the torrents are comics).
const (
	hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hashC = "cccccccccccccccccccccccccccccccccccccccc"
)

// plainHTTP is an integrations.HTTP over a bare client, for tests that talk to
// an httptest server; the Registry-backed tests use the real guarded client.
type plainHTTP struct{ c *http.Client }

func (p plainHTTP) Do(req *http.Request) (*http.Response, error) { return p.c.Do(req) }
func (p plainHTTP) Client() *http.Client                         { return p.c }
func (p plainHTTP) Gate() integrations.Gate                      { return nil }

// staticSecret serves one password, or the error err.
type staticSecret struct {
	pw  string
	err error
}

func (s staticSecret) Secret(context.Context, string) (secrets.Value, error) {
	if s.err != nil {
		return secrets.Value{}, s.err
	}
	return secrets.NewValue(s.pw), nil
}

// webUI is a fake Deluge Web UI that records every request. handler answers
// everything but auth.login: its result (a JSON document) or its RPC error.
type webUI struct {
	mu      sync.Mutex
	calls   []rpcRequest // every non-login request, in order
	logins  int
	loginOK bool // false: auth.login answers false
	handler func(req rpcRequest, n int) (result string, rpcErr *rpcError)
	srv     *httptest.Server
}

func newWebUI(t *testing.T, h func(req rpcRequest, n int) (string, *rpcError)) *webUI {
	t.Helper()
	ui := &webUI{loginOK: true, handler: h}
	ui.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var rq rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&rq)
		ui.mu.Lock()
		if rq.Method == "auth.login" {
			ui.logins++
			ok := ui.loginOK
			ui.mu.Unlock()
			if ok {
				http.SetCookie(w, &http.Cookie{Name: "_session_id", Value: "gotham", Path: "/"})
			}
			res := "false"
			if ok {
				res = "true"
			}
			_ = json.NewEncoder(w).Encode(rpcResponse{ID: rq.ID, Result: json.RawMessage(res)})
			return
		}
		ui.calls = append(ui.calls, rq)
		n := len(ui.calls)
		ui.mu.Unlock()
		res, e := ui.handler(rq, n)
		resp := rpcResponse{ID: rq.ID, Error: e}
		if e == nil {
			resp.Result = json.RawMessage(res)
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(ui.srv.Close)
	return ui
}

func (ui *webUI) count() int {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	return len(ui.calls)
}

func (ui *webUI) loginCount() int {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	return ui.logins
}

func (ui *webUI) call(i int) rpcRequest {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	return ui.calls[i]
}

// rpc is a client connected to ui.
func (ui *webUI) rpc(t *testing.T) *rpc {
	t.Helper()
	c, err := newRPC(ui.srv.URL, plainHTTP{ui.srv.Client()}, staticSecret{pw: "hunter2"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// instance is an adapter instance over ui.
func (ui *webUI) instance(t *testing.T) *instance {
	t.Helper()
	return &instance{rpc: ui.rpc(t), ref: integrations.Ref{Kind: integrations.KindDownload, Instance: "deluge"}}
}

var (
	errNotAuthenticated = &rpcError{Code: rpcCodeNotAuthenticated, Message: "Not authenticated"}
)

// failingHTTP never reaches anyone: every request fails at the transport.
type failingHTTP struct{}

func (failingHTTP) Do(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}
func (failingHTTP) Client() *http.Client    { return nil }
func (failingHTTP) Gate() integrations.Gate { return nil }
