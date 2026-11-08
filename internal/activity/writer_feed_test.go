// file: internal/activity/writer_feed_test.go
// version: 1.0.0
// guid: 0c7a41d9-83be-4e52-a6f1-5d92e8b3c07a
// last-edited: 2026-11-08

package activity

import (
	"bytes"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestWriter_FeedParsesWithoutTheStdoutTee: Feed turns a line into the same
// system entry Write does, but does not copy it to stdout. The log pipeline's
// activity tap feeds through it: the pipeline's primary output already
// carries the line, and a second copy would print every record twice.
func TestWriter_FeedParsesWithoutTheStdoutTee(t *testing.T) {
	const line = `time=2026-11-08T10:00:00.000-05:00 level=INFO msg="starting run" op_id=op-batcave path=/lib/cave`

	var viaWrite, viaFeed database.ActivityEntry
	for name, tc := range map[string]struct {
		feed       func(*Writer)
		got        *database.ActivityEntry
		wantStdout bool
	}{
		"Write": {func(w *Writer) { _, _ = w.Write([]byte(line + "\n")) }, &viaWrite, true},
		"Feed":  {func(w *Writer) { w.Feed([]byte(line + "\n")) }, &viaFeed, false},
	} {
		t.Run(name, func(t *testing.T) {
			w := NewWriter(newTestPebbleActivityStore(t), 8)
			var stdout bytes.Buffer
			w.stdout = &stdout
			tc.feed(w)
			select {
			case *tc.got = <-w.Chan():
			case <-time.After(2 * time.Second):
				t.Fatal("no entry parsed")
			}
			if (stdout.Len() > 0) != tc.wantStdout {
				t.Errorf("stdout = %q, want written=%v", stdout.String(), tc.wantStdout)
			}
		})
	}
	if viaWrite.Type != "system" || viaWrite.OperationID != "op-batcave" || viaWrite.Summary != "starting run path=/lib/cave" {
		t.Fatalf("Write built an unexpected entry: %+v", viaWrite)
	}
	if viaFeed.Type != viaWrite.Type || viaFeed.Tier != viaWrite.Tier || viaFeed.Level != viaWrite.Level ||
		viaFeed.Source != viaWrite.Source || viaFeed.Summary != viaWrite.Summary || viaFeed.OperationID != viaWrite.OperationID {
		t.Errorf("Feed built %+v, Write built %+v", viaFeed, viaWrite)
	}
}

// TestWriter_FeedKeepsPartialLines: Feed shares Write's line buffer, so a
// line split across two calls is one entry.
func TestWriter_FeedKeepsPartialLines(t *testing.T) {
	w := NewWriter(newTestPebbleActivityStore(t), 8)
	w.stdout = &bytes.Buffer{}
	w.Feed([]byte("time=2026-11-08T10:00:00Z level=WARN msg="))
	w.Feed([]byte("\"two-face\"\n"))
	select {
	case e := <-w.Chan():
		if e.Level != "warn" || e.Summary != "two-face" {
			t.Errorf("entry = %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no entry parsed from the split line")
	}
}
