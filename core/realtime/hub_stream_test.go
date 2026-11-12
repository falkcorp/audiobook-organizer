// file: core/realtime/hub_stream_test.go
// version: 1.0.0
// guid: c1c1a964-fd4e-431c-899d-7e60575af669
// last-edited: 2026-11-11

package realtime_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// seqOf is the numeric part of an event id.
func seqOf(t testing.TB, id string) int {
	t.Helper()
	_, n, ok := strings.Cut(id, "-")
	if !ok {
		t.Fatalf("id %q has no nonce-seq form", id)
	}
	v, err := strconv.Atoi(n)
	if err != nil {
		t.Fatalf("id %q: %v", id, err)
	}
	return v
}

// nonceOf is the boot nonce of an event id.
func nonceOf(id string) string {
	n, _, _ := strings.Cut(id, "-")
	return n
}

// lamps reads n data frames and returns their lamps and ids.
func lamps(t testing.TB, rd *reader, n int) (names, ids []string) {
	t.Helper()
	for range n {
		en, id := rd.nextData()
		var s Signal
		if err := json.Unmarshal(en.Data, &s); err != nil {
			t.Fatalf("frame %+v: %v", en, err)
		}
		names, ids = append(names, s.Lamp), append(ids, id)
	}
	return names, ids
}

// publishLamps publishes signals lamp-from..lamp-to on the public wing.
func (e *env) publishLamps(from, to int) {
	for i := from; i <= to; i++ {
		e.signal(topicSignal, "", fmt.Sprintf("lamp-%d", i), "public")
	}
}

// awaitLamp reads data frames until the named lamp arrives. A publish reaches
// the ring through the bus's own goroutine, so a test that wants a later
// resume to find an event in the ring waits on a witness stream first.
func awaitLamp(t testing.TB, rd *reader, lamp string) {
	t.Helper()
	for {
		en, _ := rd.nextData()
		if strings.Contains(string(en.Data), `"`+lamp+`"`) {
			return
		}
	}
}

// TestHub_ResumeFromLastEventID: a client that reconnects with the id of the
// last event it saw gets exactly the events after it, then live ones, with no
// duplicate and no gap.
func TestHub_ResumeFromLastEventID(t *testing.T) {
	c := newCast(t, hubOptions{})
	witness := c.open(t, "bruce", "/events?topic=batcave.signal.lit.v1")
	witness.expectOpened()
	first := c.open(t, "lucius", "/events?topic=batcave.signal.lit.v1")
	first.expectOpened()
	c.publishLamps(1, 3)
	names, ids := lamps(t, first, 3)
	if !slices.Equal(names, []string{"lamp-1", "lamp-2", "lamp-3"}) {
		t.Fatalf("first stream saw %v", names)
	}
	first.close()

	c.publishLamps(4, 6) // while the client is away
	awaitLamp(t, witness, "lamp-6")
	again := c.open(t, "lucius", "/events?topic=batcave.signal.lit.v1", "Last-Event-ID", ids[0])
	again.expectOpened()
	c.publishLamps(7, 7)
	names, got := lamps(t, again, 6)
	if want := []string{"lamp-2", "lamp-3", "lamp-4", "lamp-5", "lamp-6", "lamp-7"}; !slices.Equal(names, want) {
		t.Fatalf("resume saw %v, want %v", names, want)
	}
	for i := 1; i < len(got); i++ {
		if seqOf(t, got[i]) != seqOf(t, got[i-1])+1 {
			t.Errorf("ids %v are not consecutive", got)
		}
	}
	again.quiet(100 * time.Millisecond)
}

// TestHub_ResumeAfterScheduledReconnect: a client that reconnects itself after
// a backoff, sending the last id it saw as the header or as ?last_event_id=,
// replays from there (FE-8). The header wins when both are sent.
func TestHub_ResumeAfterScheduledReconnect(t *testing.T) {
	c := newCast(t, hubOptions{})
	witness := c.open(t, "bruce", "/events?topic=batcave.signal.lit.v1")
	witness.expectOpened()
	c.publishLamps(1, 4)
	awaitLamp(t, witness, "lamp-4")
	probe := c.open(t, "lucius", "/events?topic=batcave.signal.lit.v1")
	probe.expectOpened()
	c.publishLamps(5, 5)
	_, ids := lamps(t, probe, 1)
	nonce := nonceOf(ids[0])
	id := func(n int) string { return nonce + "-" + strconv.Itoa(n) }

	for _, tc := range []struct {
		name, path string
		hdr        []string
		want       []string
	}{
		{"header", "/events?topic=batcave.signal.lit.v1", []string{"Last-Event-ID", id(2)}, []string{"lamp-3", "lamp-4", "lamp-5"}},
		{"query", "/events?topic=batcave.signal.lit.v1&last_event_id=" + id(3), nil, []string{"lamp-4", "lamp-5"}},
		{"header wins", "/events?topic=batcave.signal.lit.v1&last_event_id=" + id(1), []string{"Last-Event-ID", id(4)}, []string{"lamp-5"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rd := c.open(t, "lucius", tc.path, tc.hdr...)
			rd.expectOpened()
			got, _ := lamps(t, rd, len(tc.want))
			if !slices.Equal(got, tc.want) {
				t.Errorf("replay = %v, want %v", got, tc.want)
			}
			rd.quiet(80 * time.Millisecond)
		})
	}
}

// TestHub_NativeFramesUnnamed: the native stream has no event: line. Every
// frame is an unnamed message an EventSource's onmessage would receive; data
// frames of catalog topics carry an id, control frames never do, and the first
// two frames are retry: 3000 and core.stream.opened.v1.
func TestHub_NativeFramesUnnamed(t *testing.T) {
	c := newCast(t, hubOptions{opts: hubHeartbeat(40 * time.Millisecond)})
	rd := c.open(t, "lucius", "/events?topic=batcave.signal.lit.v1")
	if first := <-rd.lines; first != "retry: 3000" {
		t.Fatalf("first line = %q, want retry: 3000", first)
	}
	rd.expectOpened()
	c.publishLamps(1, 2)
	lamps(t, rd, 2)
	time.Sleep(120 * time.Millisecond) // a heartbeat or two
	var sawHB, sawID bool
loop:
	for {
		select {
		case l := <-rd.lines:
			switch {
			case strings.HasPrefix(l, "event:"):
				t.Errorf("named frame: %q", l)
			case l == ": hb":
				sawHB = true
			case strings.HasPrefix(l, "id: "):
				sawID = true
			}
		default:
			break loop
		}
	}
	if !sawHB || !sawID {
		t.Errorf("heartbeat seen %v, id seen %v: want both", sawHB, sawID)
	}
}
