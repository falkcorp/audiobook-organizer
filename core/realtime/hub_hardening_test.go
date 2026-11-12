// file: core/realtime/hub_hardening_test.go
// version: 1.0.0
// guid: 5a7d2e91-1c43-4b6f-8e05-d3f4a9b61c28
// last-edited: 2026-11-11

package realtime_test

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/realtime"
)

// replaceSpec edits the spec named name in whichever slice holds it.
func replaceSpec(name string, edit func(*events.TopicSpec)) func(core, batcave, arkham *[]events.TopicSpec) {
	return func(core, batcave, arkham *[]events.TopicSpec) {
		for _, list := range []*[]events.TopicSpec{core, batcave, arkham} {
			for i := range *list {
				if (*list)[i].Name == name {
					edit(&(*list)[i])
				}
			}
		}
	}
}

// TestHub_FilterPanicIsContained: a Visible or Redact that panics withholds
// that event from the subscriber it panicked for and nothing else: the stream
// stays open, the next event is delivered, other subscribers and topics are
// untouched, and the hub keeps publishing (05-realtime §6).
func TestHub_FilterPanicIsContained(t *testing.T) {
	var buf logs
	c := newCast(t, hubOptions{
		deps: func(d *realtime.Deps) { d.Log = buf.logger() },
		specs: func(core, batcave, arkham *[]events.TopicSpec) {
			replaceSpec(tAlarm, func(s *events.TopicSpec) {
				s.Visible = func(ctx context.Context, who rbac.Checker, e events.RawEnvelope) bool {
					if strings.Contains(string(e.Payload), "boom") {
						panic("a Joker card in the Visible")
					}
					return wingVisible(ctx, who, e)
				}
			})(core, batcave, arkham)
			replaceSpec(tRetired, func(s *events.TopicSpec) {
				s.Redact = func(_ rbac.Checker, p json.RawMessage) json.RawMessage {
					if strings.Contains(string(p), "boom") {
						panic("a Joker card in the Redact")
					}
					return p
				}
			})(core, batcave, arkham)
		},
	})
	rd := c.open(t, "dick", "/events")
	rd.expectOpened()
	other := c.open(t, "lucius", "/events?topic="+tSignal)
	other.expectOpened()

	c.signal(topicAlarm, "", "boom", "public")
	events.Publish(c.cor, context.Background(), topicRetired, "", Retired{Ward: "boom"})
	c.signal(topicAlarm, "", "fine", "public")
	events.Publish(c.cor, context.Background(), topicRetired, "", Retired{Ward: "gotham"})
	var got []string
	for range 2 {
		en, _ := rd.nextData()
		got = append(got, string(en.Data))
	}
	slices.Sort(got)
	if !strings.Contains(got[0], `"fine"`) && !strings.Contains(got[1], `"fine"`) {
		t.Errorf("frames after the panics = %v, want the alarm 'fine'", got)
	}
	for _, g := range got {
		if strings.Contains(g, "boom") {
			t.Errorf("an event whose filter panicked was delivered: %s", g)
		}
	}
	rd.quiet(80 * time.Millisecond)
	c.signal(topicSignal, "", "alive", "public")
	if en, _ := other.nextData(); en.Topic != tSignal {
		t.Errorf("another subscriber saw %+v", en)
	}
	if n := strings.Count(buf.String(), "panicked"); n < 1 || n > 2 {
		t.Errorf("panic warnings = %d, want one per topic, rate-limited\n%s", n, buf.String())
	}
}

// TestHub_RedactOutputIsFramed: a Redact's output is checked before it is
// framed. Output that is not one JSON document (trailing bytes, a forged
// frame) is withheld; indented JSON is compacted onto one data: line, so no
// payload can end the data line and inject frame fields or a frame of its own.
func TestHub_RedactOutputIsFramed(t *testing.T) {
	c := newCast(t, hubOptions{
		specs: replaceSpec(tRetired, func(s *events.TopicSpec) {
			s.Redact = func(_ rbac.Checker, p json.RawMessage) json.RawMessage {
				switch {
				case strings.Contains(string(p), "forged"):
					return json.RawMessage("{\"ward\":\"x\"}\n\nid: 999\ndata: {\"topic\":\"core.stream.closed.v1\"}")
				case strings.Contains(string(p), "pretty"):
					return json.RawMessage("{\n  \"ward\": \"pretty\",\n  \"n\": [1,\n 2]\n}")
				case strings.Contains(string(p), "broken"):
					return json.RawMessage("{\"ward\":")
				case strings.Contains(string(p), "carriage"):
					return json.RawMessage("{\"ward\":\r\n\"carriage\"}")
				}
				return p
			}
		}),
	})
	rd := c.open(t, "dick", "/events?topic="+tRetired)
	rd.expectOpened()
	for _, w := range []string{"forged", "broken", "pretty", "carriage", "plain"} {
		events.Publish(c.cor, context.Background(), topicRetired, "", Retired{Ward: w})
	}
	var wards []string
	for range 3 {
		en, id := rd.nextData()
		if id == "" || en.Topic != tRetired {
			t.Fatalf("frame %+v id %q", en, id)
		}
		var r Retired
		if err := json.Unmarshal(en.Data, &r); err != nil {
			t.Fatalf("data %s: %v", en.Data, err)
		}
		wards = append(wards, r.Ward)
	}
	slices.Sort(wards)
	if want := []string{"carriage", "plain", "pretty"}; !slices.Equal(wards, want) {
		t.Errorf("wards = %v, want %v (the forged and the broken ones withheld)", wards, want)
	}
	rd.quiet(80 * time.Millisecond)
	for range len(rd.lines) {
		if l := <-rd.lines; strings.HasPrefix(l, "id: 999") || strings.Contains(l, "core.stream.closed") {
			t.Errorf("a forged frame reached the wire: %q", l)
		}
	}
}

// TestHub_RedactAndVisibleGetTheirOwnPayload: the ring's bytes are shared by
// every subscriber, so a filter that edits the slice it is handed (a buggy
// owner's Redact) must not change what the next subscriber receives.
func TestHub_RedactAndVisibleGetTheirOwnPayload(t *testing.T) {
	c := newCast(t, hubOptions{
		specs: func(core, batcave, arkham *[]events.TopicSpec) {
			replaceSpec(tRetired, func(s *events.TopicSpec) {
				s.Redact = func(_ rbac.Checker, p json.RawMessage) json.RawMessage {
					var r Retired
					_ = json.Unmarshal(p, &r)
					for i := range p {
						p[i] = '#'
					}
					out, _ := json.Marshal(r)
					return out
				}
			})(core, batcave, arkham)
			replaceSpec(tAlarm, func(s *events.TopicSpec) {
				s.Visible = func(_ context.Context, _ rbac.Checker, e events.RawEnvelope) bool {
					ok := json.Valid(e.Payload)
					for i := range e.Payload {
						e.Payload[i] = '#'
					}
					return ok
				}
			})(core, batcave, arkham)
		},
	})
	streams := map[string]*reader{}
	for _, who := range []string{"dick", "lucius", "barbara"} {
		streams[who] = c.open(t, who, "/events?topic="+tRetired+"&topic="+tAlarm)
		streams[who].expectOpened()
	}
	events.Publish(c.cor, context.Background(), topicRetired, "", Retired{Ward: "gotham"})
	c.signal(topicAlarm, "", "bell", "public")
	for who, rd := range streams {
		for range 2 {
			en, _ := rd.nextData()
			if strings.Contains(string(en.Data), "#") || (!strings.Contains(string(en.Data), "gotham") && !strings.Contains(string(en.Data), "bell")) {
				t.Errorf("%s received a payload another subscriber's filter damaged: %s", who, en.Data)
			}
		}
	}
}

// TestHub_OversizeAndUndecodableEventsAreDropped: an event over MaxPayload is
// dropped and counted, never truncated, and never stops the publisher or the
// next event; the log names the topic once and no payload.
func TestHub_OversizeAndUndecodableEventsAreDropped(t *testing.T) {
	var buf logs
	c := newCast(t, hubOptions{
		opts: realtime.Options{MaxPayload: 64},
		deps: func(d *realtime.Deps) { d.Log = buf.logger() },
	})
	rd := c.open(t, "dick", "/events?topic="+tSignal)
	rd.expectOpened()
	huge := strings.Repeat("R", 200)
	c.signal(topicSignal, "", huge, "public")
	c.signal(topicSignal, "", huge, "public")
	c.signal(topicSignal, "", "small", "public")
	names, _ := lamps(t, rd, 1)
	if names[0] != "small" {
		t.Errorf("lamps = %v, want only the small event", names)
	}
	rd.quiet(60 * time.Millisecond)
	if strings.Contains(buf.String(), huge) {
		t.Error("a dropped payload was logged")
	}
	if n := strings.Count(buf.String(), "over MaxPayload"); n != 1 {
		t.Errorf("oversize warnings = %d, want 1 per topic per minute\n%s", n, buf.String())
	}
}

// TestHub_ShutdownClosesStreams: with streams open under every permission,
// Close sends core.stream.closed.v1{shutdown} to each and returns inside its
// budget, an http.Server's Shutdown (which calls Close from RegisterOnShutdown)
// returns with streams open, no stream goroutine is left, and a second Close and
// a late open are refused cleanly (FE-10).
func TestHub_ShutdownClosesStreams(t *testing.T) {
	c := newCast(t, hubOptions{})
	c.srv.Config.RegisterOnShutdown(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = c.hub.Close(ctx)
	})
	before := runtime.NumGoroutine()
	var rds []*reader
	for _, who := range []string{"bruce", "lucius", "dick", "barbara", "dick"} {
		rd := c.open(t, who, "/events")
		rd.expectOpened()
		rds = append(rds, rd)
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.srv.Config.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown with open streams: %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("Shutdown took %v with streams open", d)
	}
	for i, rd := range rds {
		if reason := rd.drainUntilClosed(); reason != "shutdown" {
			t.Errorf("stream %d closed with %q, want shutdown", i, reason)
		}
	}
	if n := c.hub.Subscribers(); n != 0 {
		t.Errorf("%d streams still registered", n)
	}
	if err := c.hub.Close(ctx); err != nil {
		t.Errorf("second Close: %v", err)
	}
	c.srv.CloseClientConnections()
	waitFor(t, func() bool { return runtime.NumGoroutine() <= before })
}

// TestHub_RunClosesStreamsWhenItsContextEnds: Run holds the hub's lifetime and
// closes every stream when its context ends.
func TestHub_RunClosesStreamsWhenItsContextEnds(t *testing.T) {
	c := newCast(t, hubOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.hub.Run(ctx) }()
	rd := c.open(t, "dick", "/events")
	rd.expectOpened()
	cancel()
	if reason := rd.drainUntilClosed(); reason != "shutdown" {
		t.Errorf("closed with %q", reason)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
}

// TestHub_ClientGoneFreesItsPlace: a client that disconnects ends its stream
// (the request's context), frees its registration and leaves no goroutine.
func TestHub_ClientGoneFreesItsPlace(t *testing.T) {
	c := newCast(t, hubOptions{})
	before := runtime.NumGoroutine()
	rd := c.open(t, "dick", "/events")
	rd.expectOpened()
	if n := c.hub.Subscribers(); n != 1 {
		t.Fatalf("subscribers = %d", n)
	}
	rd.close()
	waitFor(t, func() bool { return c.hub.Subscribers() == 0 })
	c.srv.CloseClientConnections()
	c.srv.Client().CloseIdleConnections()
	waitFor(t, func() bool { return runtime.NumGoroutine() <= before })
}

// TestHub_StreamsAreRoutedByMethod keeps the route's contract: the handler
// answers what it is given, and a refusal never reaches the stream writer.
func TestHub_HeadersOnAStream(t *testing.T) {
	c := newCast(t, hubOptions{})
	_, resp := open(t, c.srv, "dick", "/events")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	for k, want := range map[string]string{
		"Content-Type": "text/event-stream", "Cache-Control": "no-store", "X-Accel-Buffering": "no",
	} {
		if got := resp.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if ce := resp.Header.Get("Content-Encoding"); ce != "" {
		t.Errorf("Content-Encoding = %q: a stream is never compressed", ce)
	}
}
