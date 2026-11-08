// file: core/storage/restorejournal/restorejournal_test.go
// version: 1.0.0
// guid: 8fca2d0f-d53c-4cee-a545-9c2ff8169c20
// last-edited: 2026-11-07

package restorejournal

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// TestRestoreJournalKindRewriteRoundTrip: an Intent of KindRewrite with a
// StepID, and a Record carrying StepID, survive a JSON round trip with
// step_id set and every other kind's fields unset (omitempty), and all five
// Kind constants are distinct strings (05-storage §5S.3.15).
func TestRestoreJournalKindRewriteRoundTrip(t *testing.T) {
	at := time.Date(1939, 5, 1, 12, 0, 0, 0, time.UTC)
	db := filepath.Join(t.TempDir(), "wayne", "main.pebble")
	in := Intent{
		ID:            "rewrite-gotham",
		Kind:          KindRewrite,
		DBPath:        db,
		Targets:       []Target{{From: db + ".fresh", To: db}},
		StagedByBuild: "wayne-1.0.0",
		ExpiresAt:     at.Add(time.Hour),
		RequestedBy:   "core/storage",
		RequestedAt:   at,
		StepID:        "storage/8",
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal intent: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatalf("decode intent fields: %v", err)
	}
	if got := string(fields["step_id"]); got != `"storage/8"` {
		t.Fatalf("step_id = %s, want \"storage/8\"", got)
	}
	if got := string(fields["kind"]); got != `"rewrite"` {
		t.Fatalf("kind = %s, want \"rewrite\"", got)
	}
	// Every field another kind owns is omitted from a rewrite's intent.
	for _, other := range []string{
		"reset", "archive_id", "staging", "install_id", "manifest_sha256",
		"delete_library_files", "delete", "hook_args", "hook_paths",
	} {
		if v, ok := fields[other]; ok {
			t.Errorf("rewrite intent carries %q = %s, want it omitted", other, v)
		}
	}
	// The fields every kind carries are always present.
	for _, always := range []string{
		"id", "kind", "db_path", "targets", "staged_by_build", "expires_at",
		"requested_by", "requested_at",
	} {
		if _, ok := fields[always]; !ok {
			t.Errorf("rewrite intent lacks %q", always)
		}
	}
	var back Intent
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal intent: %v", err)
	}
	if !reflect.DeepEqual(back, in) {
		t.Fatalf("intent round trip:\n got %+v\nwant %+v", back, in)
	}

	rec := Record{At: at, Step: "staged", Actor: "core/storage", Note: string(KindRewrite), StepID: "storage/8"}
	rb, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	var recBack Record
	if err := json.Unmarshal(rb, &recBack); err != nil {
		t.Fatalf("unmarshal record: %v", err)
	}
	if !reflect.DeepEqual(recBack, rec) {
		t.Fatalf("record round trip:\n got %+v\nwant %+v", recBack, rec)
	}
	if recBack.StepID != "storage/8" {
		t.Fatalf("record StepID = %q after the round trip", recBack.StepID)
	}

	kinds := []Kind{KindArchive, KindRevert, KindCheckpoint, KindReset, KindRewrite}
	seen := make(map[Kind]bool, len(kinds))
	for _, k := range kinds {
		if k == "" {
			t.Errorf("a Kind constant is empty")
		}
		if seen[k] {
			t.Errorf("Kind %q declared twice", k)
		}
		seen[k] = true
	}
	if ResetStore == ResetFactory || ResetStore == "" || ResetFactory == "" {
		t.Errorf("ResetKind constants not distinct: %q, %q", ResetStore, ResetFactory)
	}
}

// TestCreateIntentWritesPending: the intent lands whole at
// restore/pending.json, mode 0600, with no temp file left beside it.
func TestCreateIntentWritesPending(t *testing.T) {
	state := t.TempDir()
	in := Intent{ID: "archive-joker", Kind: KindArchive, DBPath: filepath.Join(state, "arkham.pebble"), ArchiveID: "arkham-1"}
	if err := CreateIntent(state, in); err != nil {
		t.Fatalf("CreateIntent: %v", err)
	}
	dir := filepath.Join(state, "restore")
	b, err := os.ReadFile(filepath.Join(dir, "pending.json"))
	if err != nil {
		t.Fatalf("read pending.json: %v", err)
	}
	var got Intent
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("decode pending.json: %v", err)
	}
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("pending.json = %+v, want %+v", got, in)
	}
	fi, err := os.Stat(filepath.Join(dir, "pending.json"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Errorf("pending.json mode = %o, want 600", mode)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("restore/ holds %v, want only pending.json", names)
	}
}

// TestCreateIntentSlotTaken: a second create while an intent is staged gets
// ErrSwapPending, leaves the first intent as it was, and leaves no temp file.
func TestCreateIntentSlotTaken(t *testing.T) {
	state := t.TempDir()
	first := Intent{ID: "reset-bane", Kind: KindReset, Reset: ResetStore}
	if err := CreateIntent(state, first); err != nil {
		t.Fatalf("first CreateIntent: %v", err)
	}
	err := CreateIntent(state, Intent{ID: "revert-riddler", Kind: KindRevert})
	if !errors.Is(err, ErrSwapPending) {
		t.Fatalf("second CreateIntent = %v, want ErrSwapPending", err)
	}
	b, err := os.ReadFile(filepath.Join(state, "restore", "pending.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got Intent
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != first.ID {
		t.Fatalf("pending.json holds %q, want the first intent %q", got.ID, first.ID)
	}
	entries, err := os.ReadDir(filepath.Join(state, "restore"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("restore/ holds %d entries after a refused create, want 1", len(entries))
	}
}

// TestCreateIntentRefusedWhileInflight: a swap in flight
// (restore/<id>/inflight.json) refuses a new intent and writes nothing.
func TestCreateIntentRefusedWhileInflight(t *testing.T) {
	state := t.TempDir()
	run := filepath.Join(state, "restore", "checkpoint-penguin")
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "inflight.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := CreateIntent(state, Intent{ID: "archive-catwoman", Kind: KindArchive})
	if !errors.Is(err, ErrSwapPending) {
		t.Fatalf("CreateIntent while in flight = %v, want ErrSwapPending", err)
	}
	if _, err := os.Stat(filepath.Join(state, "restore", "pending.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending.json exists after a refused create (stat err %v)", err)
	}
}

// TestCreateIntentConcurrentOneWins: concurrent creates in one process never
// both succeed; every loser gets ErrSwapPending. The two-process cell is
// 05-backup's TestSwapSlotExclusive (R1-21).
func TestCreateIntentConcurrentOneWins(t *testing.T) {
	state := t.TempDir()
	const n = 16
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
		errs []error
	)
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			err := CreateIntent(state, Intent{ID: "robin-" + string(rune('a'+i)), Kind: KindArchive})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
				return
			}
			errs = append(errs, err)
		})
	}
	close(start)
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d creates won, want exactly 1", wins)
	}
	for _, err := range errs {
		if !errors.Is(err, ErrSwapPending) {
			t.Errorf("losing create = %v, want ErrSwapPending", err)
		}
	}
}

// TestCreateIntentRefusesRelativeStateDir: the slot is addressed by an
// absolute state directory only, so two processes never disagree on it.
func TestCreateIntentRefusesRelativeStateDir(t *testing.T) {
	if err := CreateIntent("alfred-state", Intent{ID: "x", Kind: KindArchive}); err == nil {
		t.Fatal("CreateIntent with a relative state directory succeeded")
	}
}
