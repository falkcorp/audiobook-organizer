// file: core/integrations/download/deluge/client_test.go
// version: 1.0.0
// guid: ebccaf8a-b872-4b19-a6e7-99aaf2ec63a3
// last-edited: 2026-11-11

package deluge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
)

func TestNewRPC_RefusesBadURLs(t *testing.T) {
	for _, u := range []string{"", "ftp://192.0.2.5/", "http://", "http://user:pw@192.0.2.5/", "192.0.2.5:8112", "//192.0.2.5"} {
		if _, err := newRPC(u, nil, nil); !errors.Is(err, integrations.ErrInvalid) {
			t.Errorf("newRPC(%q) = %v, want ErrInvalid", u, err)
		}
	}
	c, err := newRPC("http://192.0.2.5:8112/proxy/?x=1#f", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "http://192.0.2.5:8112/proxy/json"; c.endpoint != want {
		t.Errorf("endpoint = %q, want %q", c.endpoint, want)
	}
}

func TestLogin_SuccessIsOncePerSession(t *testing.T) {
	ui := newWebUI(t, func(rpcRequest, int) (string, *rpcError) { return "{}", nil })
	c := ui.rpc(t)
	for range 3 {
		if err := c.login(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if ui.loginCount() != 1 {
		t.Errorf("logins = %d, want 1", ui.loginCount())
	}
}

func TestLogin_SendsThePasswordAndKeepsTheCookie(t *testing.T) {
	var gotPW, gotCookie atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rq rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&rq)
		if rq.Method == "auth.login" {
			gotPW.Store(fmt.Sprint(rq.Params[0]))
			http.SetCookie(w, &http.Cookie{Name: "_session_id", Value: "robin", Path: "/"})
			_, _ = w.Write([]byte(`{"id":1,"result":true,"error":null}`))
			return
		}
		if ck, err := r.Cookie("_session_id"); err == nil {
			gotCookie.Store(ck.Value)
		}
		_, _ = w.Write([]byte(`{"id":2,"result":{},"error":null}`))
	}))
	defer srv.Close()
	c, err := newRPC(srv.URL, plainHTTP{srv.Client()}, staticSecret{pw: "hunter2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.statuses(context.Background(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if gotPW.Load() != "hunter2" || gotCookie.Load() != "robin" {
		t.Errorf("password %v cookie %v", gotPW.Load(), gotCookie.Load())
	}
}

func TestLogin_BadPassword(t *testing.T) {
	ui := newWebUI(t, func(rpcRequest, int) (string, *rpcError) { return "{}", nil })
	ui.loginOK = false
	err := ui.rpc(t).login(context.Background())
	if !errors.Is(err, errLoginRejected) {
		t.Fatalf("err = %v, want errLoginRejected", err)
	}
	if got := integrations.ClassOf(context.Background(), ui.instance(t).fail(context.Background(), "x", err)); got != integrations.ClassAuth {
		t.Errorf("class = %v, want auth", got)
	}
}

func TestLogin_SecretUnavailableSendsNothing(t *testing.T) {
	ui := newWebUI(t, func(rpcRequest, int) (string, *rpcError) { return "{}", nil })
	c, _ := newRPC(ui.srv.URL, plainHTTP{ui.srv.Client()}, staticSecret{err: integrations.ErrOperatorSecretWithheld})
	_, err := c.statuses(context.Background(), map[string]any{})
	if _, ok := errors.AsType[*secretError](err); !ok || !errors.Is(err, integrations.ErrOperatorSecretWithheld) {
		t.Fatalf("err = %v", err)
	}
	if ui.loginCount() != 0 || ui.count() != 0 {
		t.Errorf("requests were sent: logins=%d calls=%d", ui.loginCount(), ui.count())
	}
	e := ui.instance(t).fail(context.Background(), "items", err)
	if got := integrations.ClassOf(context.Background(), e); got != integrations.ClassRefusedLocal {
		t.Errorf("class = %v, want refused_local", got)
	}
}

func TestCall_ReloginOnceOnAuthError(t *testing.T) {
	// The first request is answered "Not authenticated"; the replay succeeds.
	ui := newWebUI(t, func(_ rpcRequest, n int) (string, *rpcError) {
		if n == 1 {
			return "", errNotAuthenticated
		}
		return `{"` + hashA + `":{"hash":"` + hashA + `"}}`, nil
	})
	got, err := ui.rpc(t).statusesByID(context.Background(), []string{hashA})
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v err %v", got, err)
	}
	if ui.loginCount() != 2 || ui.count() != 2 {
		t.Errorf("logins=%d calls=%d, want 2 and 2", ui.loginCount(), ui.count())
	}

	// Two consecutive auth errors: returned after ONE replay, not looped.
	ui2 := newWebUI(t, func(rpcRequest, int) (string, *rpcError) { return "", errNotAuthenticated })
	_, err = ui2.rpc(t).statusesByID(context.Background(), []string{hashA})
	if !isAuthError(err) {
		t.Fatalf("err = %v, want the auth error", err)
	}
	if ui2.loginCount() != 2 || ui2.count() != 2 {
		t.Errorf("logins=%d calls=%d, want 2 and 2 (one replay only)", ui2.loginCount(), ui2.count())
	}
	if got := integrations.ClassOf(context.Background(), ui2.instance(t).fail(context.Background(), "items", err)); got != integrations.ClassAuth {
		t.Errorf("class = %v, want auth", got)
	}
}

func TestCall_TransportErrorWithAuthPhraseDoesNotRelogin(t *testing.T) {
	var logins, status atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rq rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&rq)
		if rq.Method == "auth.login" {
			logins.Add(1)
			_ = json.NewEncoder(w).Encode(rpcResponse{ID: rq.ID, Result: json.RawMessage(`true`)})
			return
		}
		status.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>502 Not authenticated upstream</html>"))
	}))
	defer srv.Close()
	c, _ := newRPC(srv.URL, plainHTTP{srv.Client()}, staticSecret{pw: "x"})
	_, err := c.statusesByID(context.Background(), []string{hashA})
	if err == nil {
		t.Fatal("want error")
	}
	if logins.Load() != 1 || status.Load() != 1 {
		t.Errorf("logins=%d status calls=%d, want 1 and 1 (no re-login, no replay)", logins.Load(), status.Load())
	}
	ui := &instance{rpc: c}
	e, _ := errors.AsType[*integrations.Error](ui.fail(context.Background(), "items", err))
	if e == nil || e.Class != integrations.ClassTransient || e.Status != http.StatusBadGateway {
		t.Errorf("error = %#v, want transient 502", e)
	}
}

func TestIsAuthError_OnlyTypedCode1(t *testing.T) {
	if !isAuthError(&rpcError{Code: 1, Message: "Not authenticated"}) {
		t.Error("code 1 should be an auth error")
	}
	if isAuthError(&rpcError{Code: 4, Message: "Not authenticated"}) {
		t.Error("code 4 must not be an auth error")
	}
	if isAuthError(errors.New("deluge error 1: Not authenticated")) {
		t.Error("untyped error must not be an auth error")
	}
	if !isAuthError(fmt.Errorf("wrapped: %w", &rpcError{Code: 1})) {
		t.Error("a wrapped code 1 should still be an auth error")
	}
}

// The login generation guard: when an expired session makes n callers fail at
// once, exactly one of them logs in again.
func TestCall_ConcurrentExpiredSessionLogsInOnce(t *testing.T) {
	const n = 20
	var (
		mu       sync.Mutex
		valid    bool
		logins   int
		arrived  int
		released = make(chan struct{})
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rq rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&rq)
		if rq.Method == "auth.login" {
			mu.Lock()
			logins++
			valid = true
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(rpcResponse{ID: rq.ID, Result: json.RawMessage(`true`)})
			return
		}
		mu.Lock()
		ok := valid
		if !ok {
			// Hold every expired-session request until all n are in flight, so
			// all n see the auth error before any re-login completes.
			arrived++
			if arrived == n {
				close(released)
			}
		}
		mu.Unlock()
		if !ok {
			<-released
			_ = json.NewEncoder(w).Encode(rpcResponse{ID: rq.ID, Error: errNotAuthenticated})
			return
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{ID: rq.ID, Result: json.RawMessage(`{}`)})
	}))
	defer srv.Close()
	c, _ := newRPC(srv.URL, plainHTTP{srv.Client()}, staticSecret{pw: "x"})
	if err := c.login(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	valid, logins = false, 0 // the session expires server-side
	mu.Unlock()

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			_, err := c.statusesByID(context.Background(), []string{hashA})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("caller failed: %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if logins != 1 {
		t.Errorf("logins = %d, want exactly 1", logins)
	}
}

// A Deluge that accepts the connection and never answers: the ctx deadline ends
// the call and the error says so.
func TestCall_HangingServerHonoursDeadline(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	c, _ := newRPC(srv.URL, plainHTTP{srv.Client()}, staticSecret{pw: "x"})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.statusesByID(ctx, []string{hashA})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("error does not wrap context.DeadlineExceeded: %v", err)
		}
		in := &instance{rpc: c}
		if got := integrations.ClassOf(ctx, in.fail(ctx, "items", err)); got != integrations.ClassCanceled {
			t.Errorf("class = %v, want canceled (the caller's deadline passed)", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the call ignored its context deadline")
	}
}

func TestNormalizeTorrentID(t *testing.T) {
	sha1 := strings.Repeat("aB3f", 10)
	sha256 := strings.Repeat("0aF9", 16)
	for _, tc := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: sha1, want: strings.ToLower(sha1)},
		{in: sha256, want: strings.ToLower(sha256)},
		{in: "", wantErr: true},
		{in: strings.Repeat("a", 39), wantErr: true},
		{in: strings.Repeat("a", 41), wantErr: true},
		{in: strings.Repeat("a", 63), wantErr: true},
		{in: strings.Repeat("a", 65), wantErr: true},
		{in: strings.Repeat("g", 40), wantErr: true},
		{in: strings.Repeat("a", 39) + " ", wantErr: true},
	} {
		got, err := NormalizeTorrentID(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("NormalizeTorrentID(%q) = %q, %v", tc.in, got, err)
		}
	}
}

func TestStatusesByID_ChunksOfFifty(t *testing.T) {
	ui := newWebUI(t, func(rpcRequest, int) (string, *rpcError) { return "{}", nil })
	ids := make([]string, 120)
	for i := range ids {
		ids[i] = fmt.Sprintf("%040x", i+1)
	}
	if _, err := ui.rpc(t).statusesByID(context.Background(), ids); err != nil {
		t.Fatal(err)
	}
	if ui.count() != 3 {
		t.Fatalf("requests = %d, want 3", ui.count())
	}
	var sizes []int
	for i := range 3 {
		filter := ui.call(i).Params[0].(map[string]any)
		sizes = append(sizes, len(filter["id"].([]any)))
	}
	if sizes[0] != 50 || sizes[1] != 50 || sizes[2] != 20 {
		t.Errorf("chunk sizes = %v, want [50 50 20]", sizes)
	}
}

func TestMoveStorage_SendsIDsAndDestination(t *testing.T) {
	ui := newWebUI(t, func(rpcRequest, int) (string, *rpcError) { return "null", nil })
	if err := ui.rpc(t).moveStorage(context.Background(), []string{hashA, hashB}, "/vault/library"); err != nil {
		t.Fatal(err)
	}
	rq := ui.call(0)
	if rq.Method != "core.move_storage" || rq.Params[1] != "/vault/library" || len(rq.Params[0].([]any)) != 2 {
		t.Errorf("request = %+v", rq)
	}
}

func TestUnknownTorrentErrors(t *testing.T) {
	for name, msg := range map[string]string{
		"InvalidTorrentError": "InvalidTorrentError: torrent_id " + hashA + " not in session.",
		"not in session":      "torrent_id x not in session.",
		"KeyError":            "KeyError: '" + hashA + "'",
	} {
		ui := newWebUI(t, func(rpcRequest, int) (string, *rpcError) {
			return "", &rpcError{Code: rpcCodeMethodFailure, Message: msg}
		})
		if _, err := ui.rpc(t).files(context.Background(), hashA); !errors.Is(err, ErrTorrentNotFound) {
			t.Errorf("%s: files err = %v", name, err)
		}
		if err := ui.rpc(t).moveStorage(context.Background(), []string{hashA}, "/x"); !errors.Is(err, ErrTorrentNotFound) {
			t.Errorf("%s: move err = %v", name, err)
		}
	}
	// A code-1 error mentioning the phrase is an auth error, not a missing torrent.
	if isUnknownTorrentError(&rpcError{Code: 1, Message: "not in session"}) {
		t.Error("only the method-failure code names a missing torrent")
	}
	// An unrelated method failure stays an RPC error.
	ui := newWebUI(t, func(rpcRequest, int) (string, *rpcError) {
		return "", &rpcError{Code: rpcCodeMethodFailure, Message: "PermissionError: [Errno 13]"}
	})
	err := ui.rpc(t).moveStorage(context.Background(), []string{hashA}, "/x")
	if errors.Is(err, ErrTorrentNotFound) {
		t.Errorf("a permission error was taken for a missing torrent: %v", err)
	}
	e, _ := errors.AsType[*integrations.Error](ui.instance(t).fail(context.Background(), "relocate", err))
	if e == nil || e.Class != integrations.ClassInvalid || e.Status != rpcCodeMethodFailure {
		t.Errorf("error = %#v, want invalid with the code", e)
	}
}

func TestLabels_PluginNotInstalled(t *testing.T) {
	ui := newWebUI(t, func(rq rpcRequest, _ int) (string, *rpcError) {
		return "", &rpcError{Code: rpcCodeMethodFailure, Message: "Unknown method: " + rq.Method}
	})
	labels, ok, err := ui.rpc(t).labels(context.Background())
	if err != nil || ok || labels != nil {
		t.Errorf("labels = %v, %v, %v; want none, not ok, no error", labels, ok, err)
	}
}

func TestLabels_AuthFailureIsNotPluginAbsence(t *testing.T) {
	ui := newWebUI(t, func(rpcRequest, int) (string, *rpcError) { return "", errNotAuthenticated })
	if _, ok, err := ui.rpc(t).labels(context.Background()); err == nil || ok {
		t.Errorf("want the auth error, got ok=%v err=%v", ok, err)
	}
}

func TestResponseTooLargeAndMalformedAreBadOutput(t *testing.T) {
	for name, body := range map[string]string{"not json": "<html>login</html>", "wrong shape": `{"result": 5, "error": null}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var rq rpcRequest
			_ = json.NewDecoder(r.Body).Decode(&rq)
			if rq.Method == "auth.login" {
				_, _ = w.Write([]byte(`{"id":1,"result":true,"error":null}`))
				return
			}
			_, _ = w.Write([]byte(body))
		}))
		c, _ := newRPC(srv.URL, plainHTTP{srv.Client()}, staticSecret{pw: "x"})
		_, err := c.statuses(context.Background(), map[string]any{})
		srv.Close()
		in := &instance{rpc: c}
		e, _ := errors.AsType[*integrations.Error](in.fail(context.Background(), "items", err))
		if e == nil || e.Class != integrations.ClassBadOutput {
			t.Errorf("%s: error = %#v, want bad_output", name, e)
		}
	}
}
