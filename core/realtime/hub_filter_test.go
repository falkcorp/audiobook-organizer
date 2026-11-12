// file: core/realtime/hub_filter_test.go
// version: 1.0.0
// guid: 8d0c6b1e-3b0f-4d39-9f2a-5c6c7a1e44b2
// last-edited: 2026-11-11

package realtime_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/internal/taskhttp"
	"github.com/falkcorp/mediacore/tasks"
)

// witness is bruce's (core:admin) stream over every topic: it sees every
// event, so a test that has read n frames from it knows n events are in the
// ring.
func (c *cast) witness(t testing.TB) *reader {
	t.Helper()
	w := c.open(t, "bruce", "/events")
	w.expectOpened()
	return w
}

// topicsOf reads n data frames and returns their topics and ids.
func topicsOf(t testing.TB, rd *reader, n int) (topics, ids []string) {
	t.Helper()
	for range n {
		en, id := rd.nextData()
		topics, ids = append(topics, en.Topic), append(ids, id)
	}
	return topics, ids
}

// publishCast publishes one event on each of six topics, in order: signal
// (batcave:view), cell (batcave:run), a private-wing alarm (view, Visible
// wants batcave:run), a public-wing alarm, an Arkham alert (arkham:view) and a
// core ward notice (AnyAuthenticated).
func (e *env) publishCast() {
	e.signal(topicSignal, "", "sig", "public")
	e.signal(topicCell, "", "cell", "private")
	e.signal(topicAlarm, "", "alarm-private", "private")
	e.signal(topicAlarm, "", "alarm-public", "public")
	events.Publish(e.ark, context.Background(), topicArk, "", Signal{Lamp: "ward", Wing: "public"})
	events.Publish(e.cor, context.Background(), topicRetired, "", Retired{Ward: "gotham"})
}

const (
	tSignal  = "batcave.signal.lit.v1"
	tCell    = "batcave.cell.opened.v1"
	tAlarm   = "batcave.alarm.rang.v1"
	tArk     = "arkham.ward.alerted.v1"
	tRetired = "core.ward.retired.v1"
)

// TestHub_PerEventPermissionFilter: four subscribers with different scopes
// receive the same publishes and each sees exactly what its checker and the
// topic's Visible allow; the permission is read from the events catalog, so the
// hub declares none of its own. The opened frame lists only what the caller may
// see.
func TestHub_PerEventPermissionFilter(t *testing.T) {
	c := newCast(t, hubOptions{})
	for _, tc := range []struct {
		who    string
		opened []string
		got    []string
		alarms []string
	}{
		{"bruce", []string{tAlarm, "batcave.beacon.updated.v1", tCell, tSignal, tArk, "core.task.log-appended.v1", "core.task.run-changed.v1", tRetired},
			[]string{tSignal, tCell, tAlarm, tAlarm, tArk, tRetired}, []string{"alarm-private", "alarm-public"}},
		{"lucius", []string{tAlarm, "batcave.beacon.updated.v1", tCell, tSignal, "core.task.log-appended.v1", "core.task.run-changed.v1", tRetired},
			[]string{tSignal, tCell, tAlarm, tAlarm, tRetired}, []string{"alarm-private", "alarm-public"}},
		{"dick", []string{tAlarm, "batcave.beacon.updated.v1", tSignal, "core.task.log-appended.v1", "core.task.run-changed.v1", tRetired},
			[]string{tSignal, tAlarm, tRetired}, []string{"alarm-public"}},
		{"barbara", []string{tAlarm, "batcave.beacon.updated.v1", tSignal, tArk, "core.task.log-appended.v1", "core.task.run-changed.v1", tRetired},
			[]string{tSignal, tAlarm, tArk, tRetired}, []string{"alarm-public"}},
	} {
		t.Run(tc.who, func(t *testing.T) {
			rd := c.open(t, tc.who, "/events")
			opened := rd.expectOpened()
			slices.Sort(opened)
			want := slices.Clone(tc.opened)
			slices.Sort(want)
			if !slices.Equal(opened, want) {
				t.Errorf("opened topics = %v, want %v", opened, want)
			}
			c.publishCast()
			var got, alarms []string
			for range tc.got {
				en, _ := rd.nextData()
				got = append(got, en.Topic)
				if en.Topic == tAlarm {
					var s Signal
					_ = json.Unmarshal(en.Data, &s)
					alarms = append(alarms, s.Lamp)
				}
			}
			// Each topic has its own bus goroutine, so the order across topics is
			// not the publish order; compare as sets.
			slices.Sort(got)
			if want := slices.Sorted(slices.Values(tc.got)); !slices.Equal(got, want) {
				t.Errorf("topics = %v, want %v", got, want)
			}
			if !slices.Equal(alarms, tc.alarms) {
				t.Errorf("alarms = %v, want %v", alarms, tc.alarms)
			}
			rd.quiet(100 * time.Millisecond)
		})
	}
}

// TestHub_ReplayIsFilteredLikeLive: a resume replays only what the resuming
// caller may see NOW, whatever the ring holds. The ring is full of events a
// watcher may not see (a run-scope topic, a private-wing alarm, an Arkham
// alert); a watcher resuming from the start gets none of them, a keeper gets
// the keeper's share, and the same Visible and Redact run on replay.
func TestHub_ReplayIsFilteredLikeLive(t *testing.T) {
	c := newCast(t, hubOptions{})
	w := c.witness(t)
	c.publishCast()
	_, ids := topicsOf(t, w, 6)
	from := nonceOf(ids[0]) + "-0"
	for _, tc := range []struct {
		who  string
		want []string
	}{
		{"dick", []string{tSignal, tAlarm, tRetired}},
		{"lucius", []string{tSignal, tCell, tAlarm, tAlarm, tRetired}},
		{"barbara", []string{tSignal, tAlarm, tArk, tRetired}},
	} {
		t.Run(tc.who, func(t *testing.T) {
			rd := c.open(t, tc.who, "/events", "Last-Event-ID", from)
			rd.expectOpened()
			got, _ := topicsOf(t, rd, len(tc.want))
			slices.Sort(got)
			if want := slices.Sorted(slices.Values(tc.want)); !slices.Equal(got, want) {
				t.Errorf("replay = %v, want %v", got, want)
			}
			rd.quiet(100 * time.Millisecond)
		})
	}
	// An explicit topic the watcher may not see is refused on resume too, not
	// replayed.
	_, resp := open(t, c.srv, "dick", "/events?topic="+tCell, "Last-Event-ID", from)
	if p := readProblem(t, resp); resp.StatusCode != 403 || p.Code != "topic_not_permitted" {
		t.Errorf("resume of a forbidden topic: %d %+v", resp.StatusCode, p)
	}
}

// TestHub_ExcludePattern: topic=*&exclude=core.task.log-appended.v1 carries
// no log lines, a module wildcard excludes a whole module, and a keyed log
// stream carries one run's lines only (FE2-21).
func TestHub_ExcludePattern(t *testing.T) {
	c := newCast(t, hubOptions{})
	logLine := func(run string) {
		events.Publish(c.cor, context.Background(), topicLog, run, Retired{Ward: "line-" + run})
	}
	t.Run("exclude one topic", func(t *testing.T) {
		rd := c.open(t, "dick", "/events?topic=*&exclude=core.task.log-appended.v1")
		opened := rd.expectOpened()
		if slices.Contains(opened, "core.task.log-appended.v1") || !slices.Contains(opened, tSignal) {
			t.Errorf("opened = %v", opened)
		}
		logLine("run-1")
		c.signal(topicSignal, "", "after", "public")
		en, _ := rd.nextData()
		if en.Topic != tSignal {
			t.Errorf("first frame topic = %s, want %s: the log line leaked", en.Topic, tSignal)
		}
	})
	t.Run("exclude a module", func(t *testing.T) {
		rd := c.open(t, "dick", "/events?exclude=batcave.*")
		for _, tp := range rd.expectOpened() {
			if strings.HasPrefix(tp, "batcave.") {
				t.Errorf("opened still lists %s", tp)
			}
		}
	})
	t.Run("keyed log stream", func(t *testing.T) {
		rd := c.open(t, "dick", "/events?topic=core.task.log-appended.v1&key=run-2")
		rd.expectOpened()
		logLine("run-1")
		logLine("run-2")
		logLine("run-1")
		en, _ := rd.nextData()
		if en.Key != "run-2" || !strings.Contains(string(en.Data), "line-run-2") {
			t.Errorf("frame = %+v, want run-2's line", en)
		}
		rd.quiet(100 * time.Millisecond)
	})
	t.Run("a pattern that excludes the named topic leaves it unnamed", func(t *testing.T) {
		rd := c.open(t, "dick", "/events?topic="+tCell+"&topic="+tSignal+"&exclude="+tCell)
		if got := rd.expectOpened(); !slices.Equal(got, []string{tSignal}) {
			t.Errorf("opened = %v", got)
		}
	})
}

// TestHub_ReplayLargerThanClientBufferIsNotAnOverflow: the replay is bounded by
// the ring, not by the live queue, so a client that was away for more events
// than its queue holds still resumes without a reset.
func TestHub_ReplayLargerThanClientBufferIsNotAnOverflow(t *testing.T) {
	c := newCast(t, hubOptions{opts: sizes(64, 4)})
	nonce := c.nonceViaProbe(t) // publishes one lamp, "nonce-probe", seq 1
	w := c.witness(t)
	for i := 1; i <= 30; i++ {
		c.signal(topicSignal, "", "lamp-"+strconv.Itoa(i), "public")
		if i%3 == 0 {
			lamps(t, w, 3) // keep the witness from overflowing its own queue of 4
		}
	}
	rd := c.open(t, "dick", "/events?topic="+tSignal, "Last-Event-ID", nonce+"-0")
	rd.expectOpened()
	names, _ := lamps(t, rd, 31)
	if names[0] != "nonce-probe" || names[1] != "lamp-1" || names[30] != "lamp-30" {
		t.Errorf("replay = %v", names)
	}
	rd.quiet(80 * time.Millisecond)
}

// nonceViaProbe is the hub's boot nonce, read off the id of one event.
func (c *cast) nonceViaProbe(t testing.TB) string {
	t.Helper()
	rd := c.open(t, "dick", "/events?topic="+tSignal)
	rd.expectOpened()
	c.signal(topicSignal, "", "nonce-probe", "public")
	_, ids := lamps(t, rd, 1)
	return nonceOf(ids[0])
}

// TestHub_KeyNarrowsReplay: ?key= narrows a resume to one entity.
func TestHub_KeyNarrowsReplay(t *testing.T) {
	c := newCast(t, hubOptions{})
	w := c.witness(t)
	for _, k := range []string{"book-1", "book-2", "book-1"} {
		c.signal(topicSignal, k, "lamp-"+k, "public")
	}
	_, ids := topicsOf(t, w, 3)
	rd := c.open(t, "dick", "/events?topic="+tSignal+"&key=book-2", "Last-Event-ID", nonceOf(ids[0])+"-0")
	rd.expectOpened()
	en, _ := rd.nextData()
	if en.Key != "book-2" {
		t.Errorf("replayed key = %q, want book-2", en.Key)
	}
	rd.quiet(80 * time.Millisecond)
}

// ---- redaction ---------------------------------------------------------------

// runView is a run with a current-item label that names a user.
func runView(current string) tasks.RunView {
	return tasks.RunView{ID: "run-7", Kind: "batcave.sweep", State: tasks.StateRunning, Done: 3, Total: 9, Current: current}
}

// runPayload publishes v as core.task.run-changed.v1.
func (c *cast) runPayload(v tasks.RunView) {
	events.Publish(c.cor, context.Background(), topicRun, string(v.ID), v)
}

// streamRun reads one run frame from rd and decodes its payload.
func streamRun(t testing.TB, rd *reader) tasks.RunView {
	t.Helper()
	en, _ := rd.nextData()
	if en.Topic != "core.task.run-changed.v1" {
		t.Fatalf("topic = %s", en.Topic)
	}
	var v tasks.RunView
	if err := json.Unmarshal(en.Data, &v); err != nil {
		t.Fatalf("run payload %s: %v", en.Data, err)
	}
	return v
}

// TestRunView_CurrentRedactedWithoutUsersView is the gate test of 05-tasks
// (COR2-2, COR3-13): the same RunView comes out of the REST serializer path
// (tasks.RedactRunView, the function taskhttp's DTO calls) and the stream path
// (tasks.RunViewRedactor through this hub, as core.task.run-changed.v1's
// catalog entry carries it), with Current blanked without core:users.view and
// kept with it.
func TestRunView_CurrentRedactedWithoutUsersView(t *testing.T) {
	c := newCast(t, hubOptions{})
	v := runView("Dick Grayson joined the sweep")
	for _, tc := range []struct {
		who         string
		keepCurrent bool
	}{{"dick", false}, {"selina", true}, {"bruce", true}} {
		t.Run(tc.who, func(t *testing.T) {
			rd := c.open(t, tc.who, "/events?topic=core.task.run-changed.v1")
			rd.expectOpened()
			c.runPayload(v)
			got := streamRun(t, rd)
			rest := tasks.RedactRunView(c.ro.get(tc.who).Perms, v)
			if got.Current != rest.Current {
				t.Errorf("stream Current = %q, REST serializer Current = %q: the surfaces disagree", got.Current, rest.Current)
			}
			if (got.Current != "") != tc.keepCurrent {
				t.Errorf("Current = %q, kept = %v, want kept = %v", got.Current, got.Current != "", tc.keepCurrent)
			}
			got.Current, v.Current = "", ""
			if got != v {
				t.Errorf("redaction changed more than Current: %+v vs %+v", got, v)
			}
			v.Current = "Dick Grayson joined the sweep"
		})
	}
}

// TestRunViewRedactor_UndecodableIsBlanked: a payload that is not a RunView is
// returned blanked to {}, never passed through unredacted (05-tasks 5T.3.10).
func TestRunViewRedactor_UndecodableIsBlanked(t *testing.T) {
	redact := tasks.RunViewRedactor()
	for _, in := range []string{`not json`, `[1,2]`, `{"Done":"many"}`, ``} {
		if got := string(redact(nil, json.RawMessage(in))); got != "{}" {
			t.Errorf("redact(%q) = %q, want {}", in, got)
		}
	}
	// A nil checker blanks Current, as RedactRunView does.
	out := redact(nil, json.RawMessage(`{"ID":"r1","Current":"Barbara Gordon"}`))
	var v tasks.RunView
	if err := json.Unmarshal(out, &v); err != nil || v.Current != "" || v.ID != "r1" {
		t.Errorf("nil-checker redact = %s (%v)", out, err)
	}
}

// restClient is a tasks.Client that serves one run, so taskhttp's real
// GET /runs/{id} serializer runs in the test.
type restClient struct {
	tasks.Client
	v tasks.RunView
}

func (r restClient) Get(context.Context, tasks.RunID) (tasks.RunView, error) { return r.v, nil }

type noApprover struct{}

func (noApprover) Approve(context.Context, tasks.RunID, tasks.ApproveRequest) error { return nil }

type restRuns struct{ taskhttp.RunReader }
type restConfig struct{ taskhttp.ConfigPort }

// TestCatalog_RedactAppliedOnEverySurface (05-events 5E.12): on
// core.task.run-changed.v1 the realtime stream and GET /runs/{id} (taskhttp's
// real handler) both blank Current without core:users.view, and both carry it
// with the scope. The topic is declared by the fixture with the catalog entry
// shape tasks will give it (Redact: tasks.RunViewRedactor()); F6-03 runs this
// on the real topic once the executor declares it.
func TestCatalog_RedactAppliedOnEverySurface(t *testing.T) {
	c := newCast(t, hubOptions{})
	v := runView("Barbara Gordon cleared the vault")
	rest, err := taskhttp.New(taskhttp.Deps{
		Client: restClient{v: v}, Approver: noApprover{}, Runs: restRuns{}, Config: restConfig{}, Audit: sink{},
	})
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(auth.WithPrincipal(r.Context(), c.ro.get(r.Header.Get("X-Test-Principal"))))
		rest.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(api.Close)
	for _, tc := range []struct {
		who  string
		kept bool
	}{{"dick", false}, {"selina", true}} {
		t.Run(tc.who, func(t *testing.T) {
			rd := c.open(t, tc.who, "/events?topic=core.task.run-changed.v1")
			rd.expectOpened()
			c.runPayload(v)
			onStream := streamRun(t, rd).Current

			req, _ := http.NewRequest(http.MethodGet, api.URL+taskhttp.BasePath+"/runs/run-7", nil)
			req.Header.Set("X-Test-Principal", tc.who)
			resp, err := api.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			var dto struct {
				Current string `json:"current"`
			}
			if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&dto) != nil {
				t.Fatalf("GET /runs/run-7 = %d", resp.StatusCode)
			}
			if onStream != dto.Current {
				t.Errorf("stream Current %q, REST Current %q", onStream, dto.Current)
			}
			if (dto.Current != "") != tc.kept {
				t.Errorf("Current kept = %v, want %v", dto.Current != "", tc.kept)
			}
		})
	}
}
