// file: internal/activity/writer_test.go
// version: 1.1.0
// guid: f7e8d9c0-b1a2-4e3f-9c8d-7b6a5e4f3d2c

package activity

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestParseLogLine(t *testing.T) {
	cases := []struct {
		name       string
		line       string
		wantLevel  string
		wantSource string
		wantMsgPfx string // prefix check (empty = exact match with wantMsg)
		wantMsg    string // exact match if wantMsgPfx is empty
	}{
		{
			name:       "info scheduler",
			line:       "2026/03/25 17:35:08 logger.go:103: [info] scheduler: Next sync in 28m",
			wantLevel:  "info",
			wantSource: "scheduler",
			wantMsg:    "Next sync in 28m",
		},
		{
			name:       "warn server",
			line:       "2026/03/25 17:35:08 server.go:874: [warn] server: No params found",
			wantLevel:  "warn",
			wantSource: "server",
			wantMsg:    "No params found",
		},
		{
			name:       "debug metadata",
			line:       "2026/03/25 17:35:08 logger.go:103: [debug] metadata: extracting tags",
			wantLevel:  "debug",
			wantSource: "metadata",
			wantMsg:    "extracting tags",
		},
		{
			name:       "gin 200 response",
			line:       "[GIN] 2026/03/25 - 17:35:11 | 200 |    1.44s |    192.0.2.164 | GET      \"/api/v1/health\"",
			wantLevel:  "info",
			wantSource: "gin",
			wantMsgPfx: "200",
		},
		{
			name:       "plain server message",
			line:       "2026/03/25 17:35:08 server.go:965: Starting HTTPS server on 0.0.0.0:8484",
			wantLevel:  "info",
			wantSource: "server",
			wantMsg:    "Starting HTTPS server on 0.0.0.0:8484",
		},
		{
			name:       "unexpected format",
			line:       "something unexpected",
			wantLevel:  "info",
			wantSource: "server",
			wantMsg:    "something unexpected",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			level, source, msg := ParseLogLine(tc.line)
			if level != tc.wantLevel {
				t.Errorf("level: got %q, want %q", level, tc.wantLevel)
			}
			if source != tc.wantSource {
				t.Errorf("source: got %q, want %q", source, tc.wantSource)
			}
			if tc.wantMsgPfx != "" {
				if !strings.HasPrefix(msg, tc.wantMsgPfx) {
					t.Errorf("message: got %q, want prefix %q", msg, tc.wantMsgPfx)
				}
			} else if msg != tc.wantMsg {
				t.Errorf("message: got %q, want %q", msg, tc.wantMsg)
			}
		})
	}
}

func TestWriter_CapturesLogs(t *testing.T) {
	dir := t.TempDir()

	store, err := database.NewNutsActivityStore(dir)
	if err != nil {
		t.Fatalf("NewNutsActivityStore: %v", err)
	}
	defer store.Close()

	// Redirect stdout to discard during test to keep output clean
	origStdout := os.Stdout
	devNull, _ := os.Open(os.DevNull)
	os.Stdout = devNull
	defer func() {
		os.Stdout = origStdout
		devNull.Close()
	}()

	w := NewWriter(store, 100)
	w.stdout = devNull
	w.Start(context.Background()) //nolint:errcheck

	lines := []string{
		"2026/03/25 17:35:08 logger.go:103: [info] scheduler: Next sync in 28m",
		"2026/03/25 17:35:08 server.go:874: [warn] server: No params found",
		"2026/03/25 17:35:08 logger.go:103: [debug] metadata: extracting tags",
	}

	for _, line := range lines {
		fmt.Fprintln(w, line)
	}

	w.Stop(context.Background()) //nolint:errcheck

	entries, total, err := store.Query(database.ActivityFilter{Limit: 50})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if total != 3 {
		t.Fatalf("total: got %d, want 3", total)
	}
	if len(entries) != 3 {
		t.Fatalf("entries len: got %d, want 3", len(entries))
	}

	// Entries are returned newest-first; map by source for easier checking
	bySource := make(map[string]database.ActivityEntry)
	for _, e := range entries {
		bySource[e.Source] = e
	}

	if e, ok := bySource["scheduler"]; !ok {
		t.Error("missing scheduler entry")
	} else if e.Level != "info" {
		t.Errorf("scheduler level: got %q, want info", e.Level)
	}

	if e, ok := bySource["server"]; !ok {
		t.Error("missing server entry")
	} else if e.Level != "warn" {
		t.Errorf("server level: got %q, want warn", e.Level)
	}

	if e, ok := bySource["metadata"]; !ok {
		t.Error("missing metadata entry")
	} else if e.Level != "debug" {
		t.Errorf("metadata level: got %q, want debug", e.Level)
	}
}

// ---------------------------------------------------------------------------
// New edge-case tests
// ---------------------------------------------------------------------------

// TestParseLogLine_SyncCompleted verifies that a plain info message without a
// source prefix is parsed with the full message text preserved.
func TestParseLogLine_SyncCompleted(t *testing.T) {
	line := "[info] Sync completed: 321 updated, 0 new"
	level, source, msg := ParseLogLine(line)

	if level != "info" {
		t.Errorf("level: expected %q, got %q", "info", level)
	}
	if source != "server" {
		t.Errorf("source: expected %q, got %q", "server", source)
	}
	// The full sync message should be preserved, not stripped.
	wantMsg := "Sync completed: 321 updated, 0 new"
	if msg != wantMsg {
		t.Errorf("message: expected %q, got %q", wantMsg, msg)
	}
}

// TestParseLogLine_SubsystemWithColon verifies that a source subsystem name
// containing a hyphen is correctly extracted as the source field.
func TestParseLogLine_SubsystemWithColon(t *testing.T) {
	line := "[info] itunes-sync: Starting sync"
	level, source, msg := ParseLogLine(line)

	if level != "info" {
		t.Errorf("level: expected %q, got %q", "info", level)
	}
	if source != "itunes-sync" {
		t.Errorf("source: expected %q, got %q", "itunes-sync", source)
	}
	if msg != "Starting sync" {
		t.Errorf("message: expected %q, got %q", "Starting sync", msg)
	}
}

// TestParseLogLine_MessageWithMultipleColons verifies that only the first
// "source: " segment is used for source extraction and the rest of the
// message (including additional colons) is kept intact.
func TestParseLogLine_MessageWithMultipleColons(t *testing.T) {
	line := "[warn] server: path not found: /foo/bar"
	level, source, msg := ParseLogLine(line)

	if level != "warn" {
		t.Errorf("level: expected %q, got %q", "warn", level)
	}
	if source != "server" {
		t.Errorf("source: expected %q, got %q", "server", source)
	}
	// The full message after "server: " must be preserved with its colon intact.
	wantMsg := "path not found: /foo/bar"
	if msg != wantMsg {
		t.Errorf("message: expected %q, got %q", wantMsg, msg)
	}
}

// TestWriter_Flush verifies that Flush() synchronously drains all
// pending channel entries into the store.
func TestWriter_Flush(t *testing.T) {
	dir := t.TempDir()

	store, err := database.NewNutsActivityStore(dir)
	if err != nil {
		t.Fatalf("NewNutsActivityStore: %v", err)
	}
	defer store.Close()

	devNull, _ := os.Open(os.DevNull)
	defer devNull.Close()

	w := NewWriter(store, 50)
	w.stdout = devNull

	lines := []string{
		"2026/03/25 17:35:08 logger.go:103: [info] scheduler: Flush entry one",
		"2026/03/25 17:35:08 server.go:874: [warn] server: Flush entry two",
		"2026/03/25 17:35:08 logger.go:103: [debug] metadata: Flush entry three",
	}

	for _, line := range lines {
		fmt.Fprintln(w, line)
	}

	// Flush synchronously drains all buffered channel entries into the store.
	w.Flush()

	// Verify all three entries are now persisted.
	entries, total, err := store.Query(database.ActivityFilter{Limit: 50})
	if err != nil {
		t.Fatalf("Query after Flush: %v", err)
	}
	if total != 3 {
		t.Errorf("total after Flush: got %d, want 3", total)
	}
	if len(entries) != 3 {
		t.Errorf("entries len after Flush: got %d, want 3", len(entries))
	}

	// Spot-check one entry to confirm parsing worked correctly.
	bySource := make(map[string]database.ActivityEntry)
	for _, e := range entries {
		bySource[e.Source] = e
	}
	if e, ok := bySource["scheduler"]; !ok {
		t.Error("missing scheduler entry after Flush")
	} else if e.Level != "info" {
		t.Errorf("scheduler level: got %q, want info", e.Level)
	}
}

func TestWriter_DropsDebugOnBackpressure(t *testing.T) {
	dir := t.TempDir()

	store, err := database.NewNutsActivityStore(dir)
	if err != nil {
		t.Fatalf("NewNutsActivityStore: %v", err)
	}
	defer store.Close()

	devNull, _ := os.Open(os.DevNull)
	defer devNull.Close()

	// chanSize=2, drain goroutine NOT started — channel will fill up
	w := NewWriter(store, 2)
	w.stdout = devNull

	done := make(chan struct{})
	go func() {
		defer close(done)

		// Fill the channel with 2 info entries
		fmt.Fprintln(w, "2026/03/25 17:35:08 server.go:1: [info] server: entry one")
		fmt.Fprintln(w, "2026/03/25 17:35:08 server.go:2: [info] server: entry two")

		// This debug entry should be silently dropped (not block)
		fmt.Fprintln(w, "2026/03/25 17:35:08 logger.go:1: [debug] metadata: should be dropped")
	}()

	select {
	case <-done:
		// success: no hang
	case <-time.After(2 * time.Second):
		t.Fatal("test timed out: Write blocked instead of dropping debug entry")
	}
}
