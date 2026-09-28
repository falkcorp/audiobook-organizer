// file: internal/realtime/book_changes_test.go
// version: 1.0.0
// guid: 0c5a9e32-6b17-4d84-a3f9-7e1d2b8c4f65
// last-edited: 2026-09-27

package realtime

import (
	"fmt"
	"testing"
	"time"
)

func newTestCoalescer() (*BookChangeCoalescer, *[]*Event, *int) {
	var got []*Event
	armed := 0
	c := NewBookChangeCoalescer(250*time.Millisecond, func(e *Event) { got = append(got, e) })
	// Manual clock: record arming, never fire; the test calls Flush.
	c.afterFunc = func(time.Duration, func()) { armed++ }
	return c, &got, &armed
}

func TestBookChangeCoalescer_DedupesAndArmsOncePerWindow(t *testing.T) {
	c, got, armed := newTestCoalescer()
	for i := 0; i < 5; i++ {
		c.Add("updated", "b1")
	}
	c.Add("updated", "b2")
	c.Add("created", "n1")
	c.Add("updated", "n1") // stays "created"
	c.Add("updated", "d1")
	c.Add("deleted", "d1") // delete supersedes update
	if *armed != 1 {
		t.Fatalf("timer armed %d times, want 1 per window", *armed)
	}
	if len(*got) != 0 {
		t.Fatal("nothing may be broadcast before the window closes")
	}
	c.Flush()
	want := map[string]string{"deleted": "[d1]", "created": "[n1]", "updated": "[b1 b2]"}
	if len(*got) != 3 {
		t.Fatalf("got %d events, want 3", len(*got))
	}
	for _, e := range *got {
		if e.Type != EventBooksChanged {
			t.Fatalf("type %s", e.Type)
		}
		kind := e.Data["kind"].(string)
		if s := fmt.Sprint(e.Data["ids"]); s != want[kind] {
			t.Errorf("%s ids = %s, want %s", kind, s, want[kind])
		}
	}
	// A new window arms again.
	c.Add("updated", "b3")
	if *armed != 2 {
		t.Fatalf("second window armed=%d", *armed)
	}
}

func TestBookChangeCoalescer_SplitsLargeBatches(t *testing.T) {
	c, got, _ := newTestCoalescer()
	n := BooksChangedMaxIDs*2 + 7
	for i := 0; i < n; i++ {
		c.Add("updated", fmt.Sprintf("b%05d", i))
	}
	c.Flush()
	if len(*got) != 3 {
		t.Fatalf("got %d events for %d ids, want 3", len(*got), n)
	}
	total := 0
	for _, e := range *got {
		total += len(e.Data["ids"].([]string))
	}
	if total != n {
		t.Fatalf("ids delivered %d, want %d", total, n)
	}
}

func TestBookChangeCoalescer_RealTimerFlushes(t *testing.T) {
	done := make(chan *Event, 1)
	c := NewBookChangeCoalescer(time.Millisecond, func(e *Event) { done <- e })
	c.Add("deleted", "x")
	select {
	case e := <-done:
		if e.Data["kind"] != "deleted" {
			t.Fatalf("kind %v", e.Data["kind"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timer never flushed")
	}
}
