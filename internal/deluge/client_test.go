// file: internal/deluge/client_test.go
// version: 1.2.0
// guid: 0b8c9d7e-1f2a-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-10-09

package deluge

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestLogin_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "auth.login" {
			json.NewEncoder(w).Encode(rpcResponse{ID: req.ID, Result: json.RawMessage(`true`)})
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "deluge")
	if err := c.Login(); err != nil {
		t.Fatalf("login: %v", err)
	}
	if !c.authed {
		t.Error("should be authed after login")
	}
	// Idempotent.
	if err := c.Login(); err != nil {
		t.Fatalf("second login: %v", err)
	}
}

func TestLogin_BadPassword(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(rpcResponse{Result: json.RawMessage(`false`)})
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "wrong")
	if err := c.Login(); err == nil {
		t.Error("expected error on bad password")
	}
}

func TestListTorrents(t *testing.T) {
	reqCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		reqCount++
		switch req.Method {
		case "auth.login":
			json.NewEncoder(w).Encode(rpcResponse{ID: req.ID, Result: json.RawMessage(`true`)})
		case "core.get_torrents_status":
			result := map[string]TorrentStatus{
				"abc123": {Hash: "abc123", Name: "Test Book", SavePath: "/downloads/books", State: "Seeding", Progress: 100},
			}
			data, _ := json.Marshal(result)
			json.NewEncoder(w).Encode(rpcResponse{ID: req.ID, Result: data})
		}
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "deluge")
	torrents, err := c.ListTorrents()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(torrents) != 1 {
		t.Errorf("got %d torrents, want 1", len(torrents))
	}
	if torrents["abc123"].Name != "Test Book" {
		t.Errorf("name = %q", torrents["abc123"].Name)
	}
}

func TestListTorrentsByLabel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "auth.login":
			json.NewEncoder(w).Encode(rpcResponse{ID: req.ID, Result: json.RawMessage(`true`)})
		case "core.get_torrents_status":
			result := map[string]TorrentStatus{
				"a": {Hash: "a", Name: "Dune", SavePath: "/dl/dune", Label: "audiobooks"},
				"b": {Hash: "b", Name: "Linux", SavePath: "/dl/linux", Label: "linux"},
				"c": {Hash: "c", Name: "Foundation", SavePath: "/dl/foundation", Label: "Audiobooks"},
			}
			data, _ := json.Marshal(result)
			json.NewEncoder(w).Encode(rpcResponse{ID: req.ID, Result: data})
		}
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "deluge")
	got, err := c.ListTorrentsByLabel("audiobooks")
	if err != nil {
		t.Fatalf("list by label: %v", err)
	}
	// case-insensitive: "audiobooks" and "Audiobooks" both match
	if len(got) != 2 {
		t.Errorf("want 2 audiobook torrents, got %d", len(got))
	}
	for _, t2 := range got {
		if t2.Hash == "b" {
			t.Errorf("linux torrent should not be in results")
		}
	}
}

func TestListTorrentsByLabel_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "auth.login":
			json.NewEncoder(w).Encode(rpcResponse{ID: req.ID, Result: json.RawMessage(`true`)})
		case "core.get_torrents_status":
			data, _ := json.Marshal(map[string]TorrentStatus{
				"a": {Hash: "a", Name: "Dune", Label: "audiobooks"},
			})
			json.NewEncoder(w).Encode(rpcResponse{ID: req.ID, Result: data})
		}
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "deluge")
	// empty label = return all
	got, err := c.ListTorrentsByLabel("")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("want 1, got %d", len(got))
	}
}

func TestListLabels_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "auth.login":
			json.NewEncoder(w).Encode(rpcResponse{ID: req.ID, Result: json.RawMessage(`true`)})
		case "label.get_labels":
			data, _ := json.Marshal([]string{"audiobooks", "linux", "movies"})
			json.NewEncoder(w).Encode(rpcResponse{ID: req.ID, Result: data})
		}
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "deluge")
	labels, err := c.ListLabels()
	if err != nil {
		t.Fatalf("list labels: %v", err)
	}
	if len(labels) != 3 {
		t.Errorf("want 3 labels, got %d", len(labels))
	}
}

func TestListLabels_PluginNotInstalled(t *testing.T) {
	// Simulate Deluge returning an RPC error when Label plugin is absent.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "auth.login":
			json.NewEncoder(w).Encode(rpcResponse{ID: req.ID, Result: json.RawMessage(`true`)})
		case "label.get_labels":
			json.NewEncoder(w).Encode(rpcResponse{
				ID:    req.ID,
				Error: &rpcError{Code: -1, Message: "unknown method"},
			})
		}
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "deluge")
	labels, err := c.ListLabels()
	if err != nil {
		t.Fatalf("should not error when label plugin absent: %v", err)
	}
	if len(labels) != 0 {
		t.Errorf("want empty slice, got %v", labels)
	}
}

func TestMoveStorage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "auth.login":
			json.NewEncoder(w).Encode(rpcResponse{ID: req.ID, Result: json.RawMessage(`true`)})
		case "core.move_storage":
			json.NewEncoder(w).Encode(rpcResponse{ID: req.ID, Result: json.RawMessage(`null`)})
		}
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "deluge")
	err := c.MoveStorage([]string{"abc123"}, "/new/path")
	if err != nil {
		t.Fatalf("move: %v", err)
	}
}

// --- detail / remove tests (synthetic ids only) ---

const (
	hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// rpcRecorder is an httptest fake that records every non-login request.
type rpcRecorder struct {
	mu      sync.Mutex
	calls   []rpcRequest
	logins  int
	handler func(req rpcRequest, n int) (result string, rpcErr string)
}

func (r *rpcRecorder) serve(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var rq rpcRequest
		_ = json.NewDecoder(req.Body).Decode(&rq)
		r.mu.Lock()
		if rq.Method == "auth.login" {
			r.logins++
			r.mu.Unlock()
			_ = json.NewEncoder(w).Encode(rpcResponse{ID: rq.ID, Result: json.RawMessage(`true`)})
			return
		}
		r.calls = append(r.calls, rq)
		n := len(r.calls)
		r.mu.Unlock()
		res, e := r.handler(rq, n)
		resp := rpcResponse{ID: rq.ID}
		if e != "" {
			resp.Error = &rpcError{Message: e, Code: 4}
		} else {
			resp.Result = json.RawMessage(res)
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	c, _ := New(srv.URL, "deluge")
	return c
}

func (r *rpcRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func asStrings(t *testing.T, v any) []string {
	t.Helper()
	raw, ok := v.([]any)
	if !ok {
		t.Fatalf("want []any, got %T", v)
	}
	out := make([]string, len(raw))
	for i, x := range raw {
		out[i] = x.(string)
	}
	return out
}

func TestGetTorrentDetail_RequestsDetailFields(t *testing.T) {
	rec := &rpcRecorder{handler: func(req rpcRequest, _ int) (string, string) {
		return `{"hash":"` + hashA + `","name":"Example Book","save_path":"/downloads/example","state":"Seeding",
"progress":100,"label":"books","total_size":300,"ratio":1.75,"seeding_time":7200,"time_added":1700000000,
"completed_time":1700000100.5,"is_finished":true,"total_done":300,
"files":[{"index":0,"path":"Example Book/a.mp3","size":100,"offset":0},{"index":1,"path":"Example Book/b.mp3","size":200,"offset":100}],
"file_progress":[1,0.5],"file_priorities":[1,0]}`, ""
	}}
	c := rec.serve(t)
	d, err := c.GetTorrentDetail(strings.ToUpper(hashA))
	if err != nil {
		t.Fatal(err)
	}
	req := rec.calls[0]
	if req.Method != "core.get_torrent_status" {
		t.Fatalf("method %s", req.Method)
	}
	if req.Params[0] != hashA {
		t.Errorf("first param = %v, want lowercased hash", req.Params[0])
	}
	if !reflect.DeepEqual(asStrings(t, req.Params[1]), torrentDetailFields) {
		t.Errorf("second param = %v, want torrentDetailFields", req.Params[1])
	}
	if d.Ratio != 1.75 || d.SeedingTime != 7200 || d.TimeAdded != 1700000000 || d.CompletedTime != 1700000100.5 ||
		!d.IsFinished || d.TotalDone != 300 || d.Name != "Example Book" {
		t.Errorf("bad decode: %+v", d)
	}
	if len(d.Files) != 2 || d.Files[1].Size != 200 || d.Files[1].Offset != 100 ||
		!reflect.DeepEqual(d.FileProgress, []float64{1, 0.5}) || !reflect.DeepEqual(d.FilePriorities, []int{1, 0}) {
		t.Errorf("bad files: %+v", d)
	}
	if got := d.AbsPath(d.Files[0]); got != "/downloads/example/Example Book/a.mp3" {
		t.Errorf("AbsPath = %q", got)
	}
}

func TestGetTorrentDetail_UnknownTorrent(t *testing.T) {
	for name, h := range map[string]func(rpcRequest, int) (string, string){
		"rpc error": func(rpcRequest, int) (string, string) {
			return "", "InvalidTorrentError: Torrent id was not in the dictionary"
		},
		"empty object": func(rpcRequest, int) (string, string) { return `{}`, "" },
	} {
		t.Run(name, func(t *testing.T) {
			rec := &rpcRecorder{handler: h}
			c := rec.serve(t)
			if _, err := c.GetTorrentDetail(hashA); !errors.Is(err, ErrTorrentNotFound) {
				t.Fatalf("err = %v, want ErrTorrentNotFound", err)
			}
		})
	}
}

func TestListTorrentDetails_FilterAndChunking(t *testing.T) {
	rec := &rpcRecorder{handler: func(rpcRequest, int) (string, string) { return `{}`, "" }}
	c := rec.serve(t)
	var hashes []string
	for i := 0; i < 120; i++ {
		hashes = append(hashes, fmt.Sprintf("%040x", i+1))
	}
	if _, err := c.ListTorrentDetails(hashes); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(rec.calls))
	}
	var sizes []int
	for _, req := range rec.calls {
		if req.Method != "core.get_torrents_status" {
			t.Fatalf("method %s", req.Method)
		}
		filter := req.Params[0].(map[string]any)
		sizes = append(sizes, len(asStrings(t, filter["id"])))
		if !reflect.DeepEqual(asStrings(t, req.Params[1]), torrentDetailFields) {
			t.Errorf("fields = %v", req.Params[1])
		}
	}
	if !reflect.DeepEqual(sizes, []int{50, 50, 20}) {
		t.Errorf("chunk sizes = %v", sizes)
	}
}

func TestListTorrentDetails_SortedByHash(t *testing.T) {
	rec := &rpcRecorder{handler: func(rpcRequest, int) (string, string) {
		return `{"` + hashB + `":{"hash":"` + hashB + `"},"` + hashA + `":{"hash":"` + hashA + `"}}`, ""
	}}
	c := rec.serve(t)
	got, err := c.ListTorrentDetails([]string{hashB, hashA})
	if err != nil || len(got) != 2 || got[0].Hash != hashA || got[1].Hash != hashB {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestListTorrentDetailsByLabel_UsesLightListFirst(t *testing.T) {
	rec := &rpcRecorder{handler: func(req rpcRequest, n int) (string, string) {
		if n == 1 {
			return `{"` + hashA + `":{"hash":"` + hashA + `","label":"Books"},"` + hashB + `":{"hash":"` + hashB + `","label":"movies"}}`, ""
		}
		return `{"` + hashA + `":{"hash":"` + hashA + `","label":"Books","files":[{"index":0,"path":"x","size":1,"offset":0}]}}`, ""
	}}
	c := rec.serve(t)
	got, err := c.ListTorrentDetailsByLabel("bOOks")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Hash != hashA || len(got[0].Files) != 1 {
		t.Fatalf("got %+v", got)
	}
	if len(rec.calls) != 2 {
		t.Fatalf("calls = %d", len(rec.calls))
	}
	first, second := rec.calls[0], rec.calls[1]
	if len(first.Params[0].(map[string]any)) != 0 {
		t.Errorf("first filter not empty: %v", first.Params[0])
	}
	if !reflect.DeepEqual(asStrings(t, first.Params[1]), torrentFields) {
		t.Errorf("first fields = %v, want light torrentFields", first.Params[1])
	}
	ids := asStrings(t, second.Params[0].(map[string]any)["id"])
	if !reflect.DeepEqual(ids, []string{hashA}) {
		t.Errorf("second filter ids = %v", ids)
	}
	if !reflect.DeepEqual(asStrings(t, second.Params[1]), torrentDetailFields) {
		t.Errorf("second fields = %v", second.Params[1])
	}
}

func TestRemoveTorrent_SendsHashAndRemoveData(t *testing.T) {
	rec := &rpcRecorder{handler: func(rpcRequest, int) (string, string) { return `true`, "" }}
	c := rec.serve(t)
	for _, flag := range []bool{true, false} {
		ok, err := c.RemoveTorrent(strings.ToUpper(hashA), flag)
		if err != nil || !ok {
			t.Fatalf("remove(%v): %v %v", flag, ok, err)
		}
	}
	for i, flag := range []bool{true, false} {
		req := rec.calls[i]
		if req.Method != "core.remove_torrent" || len(req.Params) != 2 || req.Params[0] != hashA || req.Params[1] != flag {
			t.Errorf("call %d = %+v", i, req)
		}
	}
}

func TestRemoveTorrent_FalseResultIsError(t *testing.T) {
	rec := &rpcRecorder{handler: func(rpcRequest, int) (string, string) { return `false`, "" }}
	c := rec.serve(t)
	ok, err := c.RemoveTorrent(hashA, true)
	if ok || !errors.Is(err, ErrRemoveRefused) {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestRemoveTorrent_RPCError(t *testing.T) {
	rec := &rpcRecorder{handler: func(rpcRequest, int) (string, string) {
		return "", "InvalidTorrentError: Torrent does not exist"
	}}
	c := rec.serve(t)
	if ok, err := c.RemoveTorrent(hashA, true); ok || !errors.Is(err, ErrTorrentNotFound) {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	rec2 := &rpcRecorder{handler: func(rpcRequest, int) (string, string) { return "", "disk exploded" }}
	c2 := rec2.serve(t)
	ok, err := c2.RemoveTorrent(hashA, true)
	if ok || err == nil || errors.Is(err, ErrTorrentNotFound) || !strings.Contains(err.Error(), hashA) {
		t.Fatalf("ok=%v err=%v, want error wrapped with hash", ok, err)
	}
}

func TestNormalizeTorrentID(t *testing.T) {
	h64 := strings.Repeat("ab", 32)
	cases := []struct {
		in, want string
		bad      bool
	}{
		{strings.ToUpper(hashA), hashA, false},
		{hashA, hashA, false},
		{strings.ToUpper(h64), h64, false},
		{"", "", true},
		{" ", "", true},
		{"abc", "", true},
		{hashA + "a", "", true},
		{strings.Repeat("g", 40), "", true},
		{" " + hashA[1:], "", true},
		{hashA[:39] + "\n", "", true},
	}
	for _, tc := range cases {
		got, err := NormalizeTorrentID(tc.in)
		if (err != nil) != tc.bad || got != tc.want {
			t.Errorf("NormalizeTorrentID(%q) = %q, %v", tc.in, got, err)
		}
	}
}

func TestRemoveTorrent_RejectsBadIDs(t *testing.T) {
	rec := &rpcRecorder{handler: func(rpcRequest, int) (string, string) { return `true`, "" }}
	c := rec.serve(t)
	for _, id := range []string{"", " ", "abc", hashA + "a", strings.Repeat("z", 40)} {
		if ok, err := c.RemoveTorrent(id, true); ok || err == nil {
			t.Errorf("id %q: ok=%v err=%v", id, ok, err)
		}
		if _, err := c.GetTorrentDetail(id); err == nil {
			t.Errorf("GetTorrentDetail(%q) accepted", id)
		}
		if _, err := c.ListTorrentDetails([]string{hashA, id}); err == nil {
			t.Errorf("ListTorrentDetails with %q accepted", id)
		}
	}
	if rec.count() != 0 || rec.logins != 0 {
		t.Errorf("requests sent for bad ids: calls=%d logins=%d", rec.count(), rec.logins)
	}
}

func TestCall_ReloginOnceOnAuthError(t *testing.T) {
	// First status call says Not authenticated, replay succeeds.
	rec := &rpcRecorder{}
	rec.handler = func(req rpcRequest, n int) (string, string) {
		if n == 1 {
			return "", "Not authenticated"
		}
		return `{"` + hashA + `":{"hash":"` + hashA + `"}}`, ""
	}
	c := rec.serve(t)
	got, err := c.ListTorrentDetails([]string{hashA})
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v err %v", got, err)
	}
	if rec.logins != 2 || rec.count() != 2 {
		t.Errorf("logins=%d calls=%d, want 2 and 2", rec.logins, rec.count())
	}

	// Two consecutive auth errors: returned, not looped.
	rec2 := &rpcRecorder{handler: func(rpcRequest, int) (string, string) { return "", "Not authenticated" }}
	c2 := rec2.serve(t)
	if _, err := c2.RemoveTorrent(hashA, true); err == nil || !strings.Contains(err.Error(), "Not authenticated") {
		t.Fatalf("err = %v", err)
	}
	if rec2.logins != 2 || rec2.count() != 2 {
		t.Errorf("logins=%d calls=%d, want 2 and 2 (one replay only)", rec2.logins, rec2.count())
	}
}

func TestExistingListPathUnchanged(t *testing.T) {
	rec := &rpcRecorder{handler: func(rpcRequest, int) (string, string) { return `{}`, "" }}
	c := rec.serve(t)
	if _, err := c.ListTorrents(); err != nil {
		t.Fatal(err)
	}
	want := []string{"hash", "name", "save_path", "state", "progress", "label", "total_size"}
	if got := asStrings(t, rec.calls[0].Params[1]); !reflect.DeepEqual(got, want) {
		t.Errorf("fields = %v", got)
	}
	if len(rec.calls[0].Params[0].(map[string]any)) != 0 {
		t.Errorf("filter = %v", rec.calls[0].Params[0])
	}
}
