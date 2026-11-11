// file: core/integrations/download/deluge/conformance_test.go
// version: 1.0.0
// guid: da9f469b-35a1-4bb0-8724-98b2555cd1de
// last-edited: 2026-11-11

package deluge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/download"
)

// delugeFixtures is the Deluge golden table of 05-download §4.2, as the shared
// suite takes it. Progress is a fraction here; the wire speaks percent.
func delugeFixtures() coretest.DownloadFixtures {
	return coretest.DownloadFixtures{
		ReadyState:   "Downloading",
		SeedingState: "Seeding",
		States: []coretest.DownloadStateCase{
			{Native: "Queued", Progress: 0, Want: download.StateQueued},
			{Native: "Downloading", Progress: 0.375, Want: download.StateDownloading},
			{Native: "Checking", Progress: 0.1, Want: download.StateDownloading},
			{Native: "Checking", Progress: 1, Want: download.StateCompleted},
			{Native: "Allocating", Progress: 0, Want: download.StateDownloading},
			{Native: "Moving", Progress: 0.5, Want: download.StateDownloading},
			{Native: "Moving", Progress: 1, Want: download.StateCompleted},
			{Native: "Seeding", Progress: 1, Want: download.StateCompleted, Seeding: true},
			{Native: "Paused", Progress: 0.62, Want: download.StatePaused},
			{Native: "Paused", Progress: 1, Want: download.StateCompleted},
			{Native: "Error", Progress: 0.2, Want: download.StateWarning},
			{Native: "Hibernating", Progress: 1, Want: download.StateWarning, Unknown: true},
		},
	}
}

// delugeWire serves a DownloadScenario as a Deluge Web UI: the JSON-RPC
// answers of the methods phase 1 calls, in Deluge's own shapes (progress in
// percent, an empty status for an unknown torrent, code-1 errors before a
// login, code-4 method failures).
func delugeWire(t testing.TB, s *coretest.DownloadScenario) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rq rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&rq)
		reply := func(result any, e *rpcError) {
			resp := rpcResponse{ID: rq.ID, Error: e}
			if e == nil {
				resp.Result, _ = json.Marshal(result)
			}
			_ = json.NewEncoder(w).Encode(resp)
		}
		if rq.Method == "auth.login" {
			ok := s.Login()
			if ok {
				http.SetCookie(w, &http.Cookie{Name: "_session_id", Value: "gotham", Path: "/"})
			}
			reply(ok, nil)
			return
		}
		if !s.Authorized() {
			reply(nil, errNotAuthenticated)
			return
		}
		version, save, free, cats := s.Identity()
		switch rq.Method {
		case "web.connected":
			reply(true, nil)
		case "daemon.info":
			reply(version, nil)
		case "core.get_config_value":
			reply(save, nil)
		case "core.get_free_space":
			reply(free, nil)
		case "label.get_labels":
			reply(cats, nil)
		case "core.get_torrents_status":
			filter, _ := rq.Params[0].(map[string]any)
			want := map[string]bool{}
			if ids, ok := filter["id"].([]any); ok {
				for _, id := range ids {
					want[id.(string)] = true
				}
			}
			out := map[string]torrentStatus{}
			for _, it := range s.Items() {
				if len(want) > 0 && !want[string(it.ID)] {
					continue
				}
				out[string(it.ID)] = torrentStatus{
					Hash: string(it.ID), Name: it.Name, SavePath: it.SavePath, State: it.Native, Progress: it.Progress * 100,
					Label: it.Category, TotalSize: it.TotalSize, TotalDone: int64(float64(it.TotalSize) * it.Progress), Ratio: it.Ratio,
				}
			}
			reply(out, nil)
		case "core.get_torrent_status":
			it, ok := s.Item(download.ItemID(rq.Params[0].(string)))
			if !ok {
				reply(map[string]any{}, nil)
				return
			}
			d := fileDetail{Hash: string(it.ID), SavePath: it.SavePath}
			for i, f := range it.Files {
				d.Files = append(d.Files, torrentFile{Index: i, Path: f.Path, Size: f.Size})
				d.FileProgress = append(d.FileProgress, f.Progress)
				d.FilePriorities = append(d.FilePriorities, f.Priority)
			}
			reply(d, nil)
		case "core.move_storage":
			var ids []download.ItemID
			for _, id := range rq.Params[0].([]any) {
				ids = append(ids, download.ItemID(id.(string)))
			}
			if err := s.Move(ids, rq.Params[1].(string)); err != nil {
				reply(nil, &rpcError{Code: rpcCodeMethodFailure, Message: "InvalidTorrentError: torrent_id not in session."})
				return
			}
			reply(nil, nil)
		default:
			reply(nil, &rpcError{Code: rpcCodeMethodFailure, Message: "Unknown method: " + rq.Method})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The shared conformance suite against core.deluge, over recorded-shape
// JSON-RPC answers. MEDIACORE_LIVE_DOWNLOAD=1 with a real Web UI (a container)
// is the live mode of DownloadFixtures.Live and is not wired here: no
// container exists in CI.
func TestDeluge_Conformance(t *testing.T) {
	coretest.DownloadConformance(t, func(t testing.TB, s *coretest.DownloadScenario) download.Client {
		srv := delugeWire(t, s)
		c, err := newRPC(srv.URL, plainHTTP{srv.Client()}, staticSecret{pw: "hunter2"})
		if err != nil {
			t.Fatal(err)
		}
		return &instance{rpc: c, ref: integrations.Ref{Kind: integrations.KindDownload, Instance: "deluge"}}
	}, delugeFixtures())
}
