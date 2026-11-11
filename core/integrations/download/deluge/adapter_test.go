// file: core/integrations/download/deluge/adapter_test.go
// version: 1.0.0
// guid: 2a6c829d-2d25-42ae-a63b-47c67bdfbcda
// last-edited: 2026-11-11

package deluge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/download"
)

// torrentsJSON is a core.get_torrents_status answer holding the given
// torrents keyed by hash.
func torrentsJSON(t *testing.T, ts ...torrentStatus) string {
	t.Helper()
	m := map[string]torrentStatus{}
	for _, s := range ts {
		m[s.Hash] = s
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func itemsOf(t *testing.T, ts ...torrentStatus) []download.Item {
	t.Helper()
	ui := newWebUI(t, func(rpcRequest, int) (string, *rpcError) { return torrentsJSON(t, ts...), nil })
	got, err := ui.instance(t).Items(context.Background(), download.ItemFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// The Deluge half of the 05-download §4.2 state table.
func TestState_Table(t *testing.T) {
	for _, tc := range []struct {
		state    string
		progress float64 // Deluge percent
		want     download.State
		seeding  bool
		unknown  bool
		canMove  bool
	}{
		{"Queued", 0, download.StateQueued, false, false, false},
		{"Queued", 40, download.StateQueued, false, false, false},
		{"Downloading", 37.5, download.StateDownloading, false, false, false},
		{"Checking", 12, download.StateDownloading, false, false, false},
		{"Checking", 100, download.StateCompleted, false, false, true},
		{"Allocating", 0, download.StateDownloading, false, false, false},
		{"Allocating", 100, download.StateCompleted, false, false, true},
		{"Moving", 55, download.StateDownloading, false, false, false},
		{"Moving", 100, download.StateCompleted, false, false, true},
		{"Seeding", 100, download.StateCompleted, true, false, false},
		{"Paused", 62, download.StatePaused, false, false, false},
		{"Paused", 100, download.StateCompleted, false, false, true},
		{"Error", 0, download.StateWarning, false, false, false},
		{"Error", 100, download.StateWarning, false, false, false},
		{"Hibernating", 100, download.StateWarning, false, true, false},
		{"", 100, download.StateWarning, false, true, false},
		{"seeding", 100, download.StateWarning, false, true, false}, // Deluge's names are exact
	} {
		got := itemsOf(t, torrentStatus{Hash: hashA, Name: "Year One", State: tc.state, Progress: tc.progress})
		if len(got) != 1 {
			t.Fatalf("%+v: %d items", tc, len(got))
		}
		it := got[0]
		_, flagged := it.Raw[download.RawUnknownState]
		if it.State != tc.want || it.Seeding != tc.seeding || flagged != tc.unknown || it.CanMoveFiles != tc.canMove || it.CanBeRemoved != tc.canMove {
			t.Errorf("%s at %v%%: state=%v seeding=%v unknown=%v canMove=%v canRemove=%v", tc.state, tc.progress, it.State, it.Seeding, flagged, it.CanMoveFiles, it.CanBeRemoved)
		}
		if tc.unknown && it.State == download.StateCompleted {
			t.Errorf("unknown state %q reported Completed", tc.state)
		}
	}
}

func TestItem_FieldMapping(t *testing.T) {
	added := 1_700_000_000.25
	got := itemsOf(t, torrentStatus{
		Hash: strings.ToUpper(hashA), Name: "The Long Halloween", SavePath: "/dl/books", State: "Seeding", Progress: 100,
		Label: "Audiobooks", TotalSize: 1000, TotalDone: 1000, Ratio: 1.75, TimeAdded: added, CompletedTime: added + 60,
		IsFinished: true, ETA: 90, Message: "OK",
	})
	it := got[0]
	switch {
	case it.Ref.ID != download.ItemID(hashA): // lowercased
		t.Errorf("id = %q", it.Ref.ID)
	case it.Ref.Instance != (integrations.Ref{Kind: integrations.KindDownload, Instance: "deluge"}):
		t.Errorf("ref instance = %v", it.Ref.Instance)
	case it.Protocol != download.Torrent, it.Name != "The Long Halloween", it.Category != "Audiobooks":
		t.Errorf("identity fields: %+v", it)
	case it.Progress != 1, it.TotalSize != 1000, it.RemainingSize != 0, it.Ratio != 1.75:
		t.Errorf("numbers: %+v", it)
	case it.RemotePath != "/dl/books", it.LocalPath != "":
		t.Errorf("paths: remote=%q local=%q (the Pool fills local)", it.RemotePath, it.LocalPath)
	case !it.SeedGoalMet:
		t.Error("a finished seeding torrent meets its seed goal")
	case it.ETA != 90*time.Second:
		t.Errorf("eta = %v", it.ETA)
	case !it.AddedAt.Equal(time.Unix(1_700_000_000, 250_000_000)), !it.CompletedAt.Equal(time.Unix(1_700_000_060, 250_000_000)):
		t.Errorf("times: added %v completed %v", it.AddedAt, it.CompletedAt)
	}
}

// Deluge reports progress in percent: the Item carries a fraction.
func TestItem_ProgressIsAFraction(t *testing.T) {
	for _, tc := range []struct{ in, want float64 }{{0, 0}, {50, 0.5}, {99.9, 0.999}, {100, 1}, {250, 1}, {-3, 0}} {
		got := itemsOf(t, torrentStatus{Hash: hashA, State: "Downloading", Progress: tc.in})
		if d := got[0].Progress - tc.want; d > 1e-9 || d < -1e-9 {
			t.Errorf("progress %v -> %v, want %v", tc.in, got[0].Progress, tc.want)
		}
	}
}

func TestItem_BoundariesAndRedaction(t *testing.T) {
	got := itemsOf(t, torrentStatus{
		Hash: hashA, State: "Error", Progress: 10, TotalSize: 100, TotalDone: 400, Ratio: -1, ETA: -1,
		Message: "tracker \x1b[31mhttps://tracker.example/announce?passkey=secret\x1b[0m said no\n" + strings.Repeat("x", 500),
	})
	it := got[0]
	if it.RemainingSize != 0 || it.Ratio != 0 || it.ETA != 0 {
		t.Errorf("clamps: remaining=%d ratio=%v eta=%v", it.RemainingSize, it.Ratio, it.ETA)
	}
	if strings.Contains(it.Message, "passkey") || strings.Contains(it.Message, "\x1b") || strings.ContainsRune(it.Message, '\n') {
		t.Errorf("message not redacted: %q", it.Message)
	}
	if !strings.Contains(it.Message, "[url]") || len([]rune(it.Message)) > maxMessage {
		t.Errorf("message = %q (len %d)", it.Message, len([]rune(it.Message)))
	}
	if !it.AddedAt.IsZero() {
		t.Errorf("a zero time_added is unknown, got %v", it.AddedAt)
	}
}

func TestItem_UnknownStateKeepsTheRawValue(t *testing.T) {
	it := itemsOf(t, torrentStatus{Hash: hashA, State: "Hibernating", Progress: 100})[0]
	if it.Raw[download.RawUnknownState] != "Hibernating" || it.State != download.StateWarning || !strings.Contains(it.Message, "Hibernating") {
		t.Errorf("item = %+v", it)
	}
}

func TestItems_FiltersAndOrder(t *testing.T) {
	all := []torrentStatus{
		{Hash: hashC, Name: "Court of Owls", State: "Seeding", Progress: 100, Label: "audiobooks"},
		{Hash: hashA, Name: "Year One", State: "Downloading", Progress: 10, Label: "Audiobooks"},
		{Hash: hashB, Name: "Hush", State: "Paused", Progress: 20, Label: "comics"},
	}
	ui := newWebUI(t, func(rpcRequest, int) (string, *rpcError) { return torrentsJSON(t, all...), nil })
	in := ui.instance(t)
	ids := func(f download.ItemFilter) []string {
		got, err := in.Items(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, it := range got {
			out = append(out, string(it.Ref.ID)[:1])
		}
		return out
	}
	for _, tc := range []struct {
		name string
		f    download.ItemFilter
		want string
	}{
		{"all, sorted by id", download.ItemFilter{}, "abc"},
		{"category is case-insensitive", download.ItemFilter{Categories: []string{"AUDIOBOOKS"}}, "ac"},
		{"states", download.ItemFilter{States: []download.State{download.StatePaused}}, "b"},
		{"tags none carried", download.ItemFilter{Tags: []string{"x"}}, ""},
		{"category and state", download.ItemFilter{Categories: []string{"audiobooks"}, States: []download.State{download.StateCompleted}}, "c"},
	} {
		if got := strings.Join(ids(tc.f), ""); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestItems_IDFilterNormalizesAndAsksDelugeByID(t *testing.T) {
	ui := newWebUI(t, func(rpcRequest, int) (string, *rpcError) {
		return torrentsJSON(t, torrentStatus{Hash: hashA, State: "Seeding", Progress: 100}), nil
	})
	got, err := ui.instance(t).Items(context.Background(), download.ItemFilter{IDs: []download.ItemID{download.ItemID(strings.ToUpper(hashA)), download.ItemID(hashA)}})
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v, %v", got, err)
	}
	filter := ui.call(0).Params[0].(map[string]any)
	if ids, _ := filter["id"].([]any); len(ids) != 1 || ids[0] != hashA {
		t.Errorf("id filter = %v, want the one lowercase id", filter)
	}
}

func TestItems_BadIDNeverReachesDeluge(t *testing.T) {
	ui := newWebUI(t, func(rpcRequest, int) (string, *rpcError) { return "{}", nil })
	_, err := ui.instance(t).Items(context.Background(), download.ItemFilter{IDs: []download.ItemID{"not-a-hash"}})
	e, _ := errors.AsType[*integrations.Error](err)
	if e == nil || e.Class != integrations.ClassInvalid || ui.count() != 0 || ui.loginCount() != 0 {
		t.Errorf("err = %v, requests = %d/%d", err, ui.count(), ui.loginCount())
	}
}

func TestItems_UsesTheListingFields(t *testing.T) {
	ui := newWebUI(t, func(rpcRequest, int) (string, *rpcError) { return "{}", nil })
	if _, err := ui.instance(t).Items(context.Background(), download.ItemFilter{}); err != nil {
		t.Fatal(err)
	}
	rq := ui.call(0)
	if rq.Method != "core.get_torrents_status" || len(rq.Params[0].(map[string]any)) != 0 {
		t.Fatalf("request = %+v", rq)
	}
	fields := map[string]bool{}
	for _, f := range rq.Params[1].([]any) {
		fields[f.(string)] = true
	}
	for _, want := range []string{"hash", "name", "save_path", "state", "progress", "label", "total_size", "is_finished"} {
		if !fields[want] {
			t.Errorf("field %q not requested", want)
		}
	}
	if fields["files"] {
		t.Error("a listing must not ask for files")
	}
}

func TestFiles(t *testing.T) {
	ui := newWebUI(t, func(rpcRequest, int) (string, *rpcError) {
		return `{"hash":"` + hashA + `","save_path":"/dl/books","files":[{"index":0,"path":"Year One/part1.m4b","size":700},{"index":1,"path":"Year One/cover.jpg","size":30}],"file_progress":[1,0.25],"file_priorities":[4,1]}`, nil
	})
	got, err := ui.instance(t).Files(context.Background(), download.ItemID(strings.ToUpper(hashA)))
	if err != nil {
		t.Fatal(err)
	}
	want := []download.File{
		{Path: "Year One/part1.m4b", Size: 700, Progress: 1, Priority: 4},
		{Path: "Year One/cover.jpg", Size: 30, Progress: 0.25, Priority: 1},
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("files = %+v, want %+v", got, want)
	}
	if rq := ui.call(0); rq.Method != "core.get_torrent_status" || rq.Params[0] != hashA {
		t.Errorf("request = %+v", rq)
	}
}

func TestFiles_UnknownTorrentIsNotFound(t *testing.T) {
	for name, h := range map[string]func(rpcRequest, int) (string, *rpcError){
		"empty status": func(rpcRequest, int) (string, *rpcError) { return "{}", nil },
		"rpc error": func(rpcRequest, int) (string, *rpcError) {
			return "", &rpcError{Code: rpcCodeMethodFailure, Message: "InvalidTorrentError: not in session"}
		},
	} {
		ui := newWebUI(t, h)
		_, err := ui.instance(t).Files(context.Background(), download.ItemID(hashA))
		if got := integrations.ClassOf(context.Background(), err); got != integrations.ClassNotFound {
			t.Errorf("%s: class = %v (%v)", name, got, err)
		}
	}
}

func TestSetLocation(t *testing.T) {
	ui := newWebUI(t, func(rpcRequest, int) (string, *rpcError) { return "null", nil })
	in := ui.instance(t)
	if err := in.SetLocation(context.Background(), []download.ItemID{download.ItemID(strings.ToUpper(hashA)), download.ItemID(hashB)}, "/vault/library"); err != nil {
		t.Fatal(err)
	}
	rq := ui.call(0)
	if rq.Method != "core.move_storage" || rq.Params[1] != "/vault/library" {
		t.Fatalf("request = %+v", rq)
	}
	if ids := rq.Params[0].([]any); len(ids) != 2 || ids[0] != hashA || ids[1] != hashB {
		t.Errorf("ids = %v", ids)
	}
	// Nothing to move sends nothing; bad input never reaches Deluge.
	before := ui.count()
	if err := in.SetLocation(context.Background(), nil, "/vault"); err != nil {
		t.Error(err)
	}
	for _, bad := range []struct {
		ids []download.ItemID
		dir string
	}{{[]download.ItemID{"zz"}, "/vault"}, {[]download.ItemID{download.ItemID(hashA)}, " "}} {
		if got := integrations.ClassOf(context.Background(), in.SetLocation(context.Background(), bad.ids, bad.dir)); got != integrations.ClassInvalid {
			t.Errorf("SetLocation(%v, %q) class = %v", bad.ids, bad.dir, got)
		}
	}
	if ui.count() != before {
		t.Errorf("requests were sent for nothing: %d -> %d", before, ui.count())
	}
}

func TestSetLocation_UnknownTorrentIsNotFound(t *testing.T) {
	ui := newWebUI(t, func(rpcRequest, int) (string, *rpcError) {
		return "", &rpcError{Code: rpcCodeMethodFailure, Message: "KeyError: 'x'"}
	})
	err := ui.instance(t).SetLocation(context.Background(), []download.ItemID{download.ItemID(hashA)}, "/vault")
	if got := integrations.ClassOf(context.Background(), err); got != integrations.ClassNotFound {
		t.Errorf("class = %v (%v)", got, err)
	}
}

func TestCaps(t *testing.T) {
	withLabels := newWebUI(t, func(rpcRequest, int) (string, *rpcError) { return `["audiobooks"]`, nil })
	c, err := withLabels.instance(t).Caps(context.Background())
	if err != nil || c != (download.Caps{Protocol: download.Torrent, Files: true, Relocate: true, Categories: true}) {
		t.Errorf("caps = %+v, %v", c, err)
	}
	noLabels := newWebUI(t, func(rpcRequest, int) (string, *rpcError) {
		return "", &rpcError{Code: rpcCodeMethodFailure, Message: "Unknown method"}
	})
	c, err = noLabels.instance(t).Caps(context.Background())
	if err != nil || c.Categories || !c.Files || !c.Relocate || c.Protocol != download.Torrent {
		t.Errorf("caps without the Label plugin = %+v, %v", c, err)
	}
}

func TestStatus(t *testing.T) {
	ui := newWebUI(t, func(rq rpcRequest, _ int) (string, *rpcError) {
		switch rq.Method {
		case "daemon.info":
			return `"2.1.1"`, nil
		case "core.get_config_value":
			return `"/dl/incoming"`, nil
		case "core.get_free_space":
			return `123456`, nil
		case "label.get_labels":
			return `["audiobooks","comics"]`, nil
		}
		return "", &rpcError{Code: rpcCodeMethodFailure, Message: "no"}
	})
	st, err := ui.instance(t).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != "2.1.1" || st.DefaultSavePath != "/dl/incoming" || st.FreeSpace != 123456 || len(st.Categories) != 2 {
		t.Errorf("status = %+v", st)
	}
	if rq := ui.call(1); rq.Params[0] != "download_location" {
		t.Errorf("config key = %v", rq.Params)
	}
}

func TestStatus_BestEffortFields(t *testing.T) {
	ui := newWebUI(t, func(rq rpcRequest, _ int) (string, *rpcError) {
		return "", &rpcError{Code: rpcCodeMethodFailure, Message: "no such method"}
	})
	st, err := ui.instance(t).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != "" || st.DefaultSavePath != "" || st.FreeSpace != -1 || st.Categories != nil {
		t.Errorf("status = %+v, want only zeros and FreeSpace -1", st)
	}
	// An auth failure is not best effort.
	bad := newWebUI(t, func(rpcRequest, int) (string, *rpcError) { return "", errNotAuthenticated })
	if _, err := bad.instance(t).Status(context.Background()); integrations.ClassOf(context.Background(), err) != integrations.ClassAuth {
		t.Errorf("err = %v, want auth", err)
	}
}

func TestHealth(t *testing.T) {
	connected := newWebUI(t, func(rq rpcRequest, _ int) (string, *rpcError) {
		if rq.Method == "web.connected" {
			return "true", nil
		}
		return `"2.1.1"`, nil
	})
	if h := connected.instance(t).Health(context.Background()); h.Status != integrations.HealthOK || h.RemoteVersion != "2.1.1" {
		t.Errorf("health = %+v", h)
	}
	disconnected := newWebUI(t, func(rq rpcRequest, _ int) (string, *rpcError) { return "false", nil })
	if h := disconnected.instance(t).Health(context.Background()); h.Status != integrations.HealthDegraded {
		t.Errorf("health = %+v, want degraded", h)
	}
	// Health never changes the remote: no connect call.
	for i := range disconnected.count() {
		if m := disconnected.call(i).Method; m == "web.connect" || m == "web.get_hosts" {
			t.Errorf("Health called %s", m)
		}
	}
	rejected := newWebUI(t, func(rpcRequest, int) (string, *rpcError) { return "true", nil })
	rejected.loginOK = false
	if h := rejected.instance(t).Health(context.Background()); h.Status != integrations.HealthAuthFailed {
		t.Errorf("health = %+v, want auth_failed", h)
	}
	downRPC, err := newRPC("http://192.0.2.1:1/", failingHTTP{}, staticSecret{pw: "x"})
	if err != nil {
		t.Fatal(err)
	}
	down := &instance{rpc: downRPC}
	if h := down.Health(context.Background()); h.Status != integrations.HealthDown {
		t.Errorf("health = %+v, want down", h)
	}
	missingRPC, err := newRPC("http://192.0.2.1:1/", failingHTTP{}, staticSecret{err: integrations.ErrNotConfigured})
	if err != nil {
		t.Fatal(err)
	}
	missing := &instance{rpc: missingRPC}
	if h := missing.Health(context.Background()); h.Status != integrations.HealthMisconfigured {
		t.Errorf("health = %+v, want misconfigured", h)
	}
}

func TestTest_ConnectsTheFirstHostWhenNoneIsConnected(t *testing.T) {
	connected := false
	ui := newWebUI(t, func(rq rpcRequest, _ int) (string, *rpcError) {
		switch rq.Method {
		case "web.connected":
			return map[bool]string{true: "true", false: "false"}[connected], nil
		case "web.get_hosts":
			return `[["abc123","192.0.2.40",58846,"Online"]]`, nil
		case "web.connect":
			connected = true
			return "null", nil
		case "label.get_labels":
			return `[]`, nil
		}
		return "", &rpcError{Code: rpcCodeMethodFailure, Message: "no"}
	})
	if err := ui.instance(t).Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	var sawConnect bool
	for i := range ui.count() {
		if rq := ui.call(i); rq.Method == "web.connect" {
			sawConnect = rq.Params[0] == "abc123"
		}
	}
	if !sawConnect {
		t.Error("the Web UI was not connected to the first host")
	}
}

func TestTest_NoHostsFails(t *testing.T) {
	ui := newWebUI(t, func(rq rpcRequest, _ int) (string, *rpcError) {
		if rq.Method == "web.connected" {
			return "false", nil
		}
		return "[]", nil
	})
	err := ui.instance(t).Test(context.Background())
	if got := integrations.ClassOf(context.Background(), err); got != integrations.ClassBadOutput {
		t.Errorf("class = %v (%v)", got, err)
	}
}

func TestTest_StillDisconnectedFails(t *testing.T) {
	ui := newWebUI(t, func(rq rpcRequest, _ int) (string, *rpcError) {
		switch rq.Method {
		case "web.connected":
			return "false", nil
		case "web.get_hosts":
			return `[["abc123","192.0.2.40",58846,"Offline"]]`, nil
		}
		return "null", nil
	})
	if got := integrations.ClassOf(context.Background(), ui.instance(t).Test(context.Background())); got != integrations.ClassTransient {
		t.Errorf("class = %v", got)
	}
}

func TestFail_StatusClasses(t *testing.T) {
	in := &instance{ref: integrations.Ref{Kind: integrations.KindDownload, Instance: "deluge"}}
	for status, want := range map[int]integrations.Class{
		401: integrations.ClassAuth, 403: integrations.ClassAuth, 429: integrations.ClassRateLimited,
		408: integrations.ClassTransient, 500: integrations.ClassTransient, 503: integrations.ClassTransient,
		400: integrations.ClassInvalid, 404: integrations.ClassInvalid,
	} {
		e, _ := errors.AsType[*integrations.Error](in.fail(context.Background(), "items", &statusError{Status: status, Excerpt: "x"}))
		if e == nil || e.Class != want || e.Status != status || e.Op != "items" || e.Ref != in.ref {
			t.Errorf("HTTP %d: %#v, want class %v", status, e, want)
		}
	}
	if in.fail(context.Background(), "x", nil) != nil {
		t.Error("nil must stay nil")
	}
}

func TestFail_DetailIsRedactedAndBounded(t *testing.T) {
	in := &instance{}
	err := in.fail(context.Background(), "items", &rpcError{Code: 4, Message: "boom http://tracker.example/a?passkey=zz " + strings.Repeat("y", 2000)})
	e, _ := errors.AsType[*integrations.Error](err)
	if e == nil || strings.Contains(e.Detail, "passkey") || len(e.Detail) > integrations.MaxDetail {
		t.Errorf("detail = %q", e.Detail)
	}
	if strings.Contains(err.Error(), "boom") {
		t.Errorf("Error() carries the remote text: %v", err)
	}
}

func TestFail_PassesAnIntegrationsErrorThrough(t *testing.T) {
	ref := integrations.Ref{Kind: integrations.KindDownload, Instance: "deluge"}
	in := &instance{ref: ref}
	orig := &integrations.Error{Class: integrations.ClassRefusedLocal, Err: errors.New("held")}
	got, _ := errors.AsType[*integrations.Error](in.fail(context.Background(), "items", orig))
	if got == nil || got.Class != integrations.ClassRefusedLocal || got.Ref != ref || got.Op != "items" {
		t.Errorf("got %#v", got)
	}
}

func TestDescriptor(t *testing.T) {
	d, err := Descriptor()
	if err != nil {
		t.Fatal(err)
	}
	m := d.Meta()
	if m.ID != AdapterID || m.Kind != integrations.KindDownload || m.Egress != integrations.EgressConfigured {
		t.Errorf("meta = %+v", m)
	}
	if m.Limits.Timeout != callTimeout || m.Limits.RPS <= 0 {
		t.Errorf("limits = %+v", m.Limits)
	}
}
