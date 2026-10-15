// file: internal/server/server_lifecycle_boot_ops_test.go
// version: 1.1.0
// guid: 5b9e2a74-1c6d-4f38-8a07-3d4e6f2b9c18
// last-edited: 2026-10-15

package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type p6Enqueued struct {
	id     string
	params string
}

func p6Recorder(fail map[string]bool) (*[]p6Enqueued, func(context.Context, string, any) (string, error)) {
	var got []p6Enqueued
	return &got, func(_ context.Context, id string, params any) (string, error) {
		b, err := json.Marshal(params)
		if err != nil {
			return "", err
		}
		got = append(got, p6Enqueued{id: id, params: string(b)})
		if fail[id] {
			return "", errors.New("synthetic enqueue failure")
		}
		return "op-" + id, nil
	}
}

// TestStartBackfills_EnqueuesBootOps: boot enqueues each converted op with the
// params that give it the old goroutine's behaviour. A bare &Server{} panics
// in startBackfills, so the helper it calls is tested directly.
func TestStartBackfills_EnqueuesBootOps(t *testing.T) {
	got, enqueue := p6Recorder(nil)
	enqueueBootOps(context.Background(), enqueue, nil)

	want := []p6Enqueued{
		{id: "maintenance.external-id-backfill", params: `{"if_needed":true}`},
		{id: "maintenance.movement-atom-cleanup", params: `{}`},
		{id: "maintenance.malformed-m4b-remux", params: `{}`},
		{id: "maintenance.malformed-m4b-transcode", params: `{"quarantine_after":true}`},
	}
	if len(*got) != len(want) {
		t.Fatalf("enqueued %v, want %v", *got, want)
	}
	for i := range want {
		if (*got)[i] != want[i] {
			t.Errorf("enqueue %d = %+v, want %+v", i, (*got)[i], want[i])
		}
	}
}

// A failed enqueue is logged and the remaining ops are still enqueued.
func TestEnqueueBootOps_ContinuesPastAFailure(t *testing.T) {
	got, enqueue := p6Recorder(map[string]bool{"maintenance.external-id-backfill": true})
	enqueueBootOps(context.Background(), enqueue, nil)
	if len(*got) != len(bootOps) {
		t.Fatalf("attempted %d enqueues, want all %d after one failure", len(*got), len(bootOps))
	}
}

// Shutdown before startBackfills reaches the enqueue enqueues nothing.
func TestEnqueueBootOps_StopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, enqueue := p6Recorder(nil)
	enqueueBootOps(ctx, enqueue, nil)
	if len(*got) != 0 {
		t.Fatalf("enqueued %v on a cancelled context, want none", *got)
	}
}

// TestStartBackfills_NoBootGoroutineForConvertedOps: the converted work no
// longer runs as a bgWG goroutine (it would then run twice per boot). Reads
// the source from the package directory (os.Getwd: CI builds with -trimpath).
func TestStartBackfills_NoBootGoroutineForConvertedOps(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(wd + "/server_lifecycle.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"external-id-backfill", "strip-movement-atoms", "remux-malformed-m4b", "transcode+quarantine"} {
		if strings.Contains(string(src), `bgWG.Go("`+name+`"`) {
			t.Errorf("server_lifecycle.go still starts the %q goroutine; it is enqueued as an op now", name)
		}
	}
	for _, name := range []string{"versiongroup-backfill", "build-search-index"} {
		if !strings.Contains(string(src), `bgWG.Go("`+name+`"`) {
			t.Errorf("server_lifecycle.go lost the %q goroutine, which 04-P6 keeps", name)
		}
	}
}

// Owner decision 2026-10-15: while an interrupted transcode waits for an
// operator (interrupted_ask), boot does not enqueue another one. No row, or a
// row in any other (terminal) status, enqueues as before. Real Pebble store,
// resolved through the same capability startBackfills uses.
func TestEnqueueBootOps_SkipsTranscodeWhileInterruptedAsk(t *testing.T) {
	const transcode = "maintenance.malformed-m4b-transcode"
	for _, tc := range []struct {
		name       string
		status     string // "" = no row
		wantQueued bool
	}{
		{name: "no row", wantQueued: true},
		{name: "terminal row", status: "canceled", wantQueued: true},
		{name: "interrupted_ask row", status: "interrupted_ask", wantQueued: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := database.NewPebbleStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if tc.status != "" {
				if err := store.InsertOperationV2(database.OperationV2Row{ID: "op-prev", DefID: transcode, Status: "running"}); err != nil {
					t.Fatal(err)
				}
				if err := store.UpdateOperationV2Status("op-prev", tc.status, nil, nil, nil); err != nil {
					t.Fatal(err)
				}
			}
			l, ok := database.AsCapability[opStatusLookup](store)
			if !ok {
				t.Fatal("PebbleStore must satisfy opStatusLookup")
			}
			got, enqueue := p6Recorder(nil)
			enqueueBootOps(context.Background(), enqueue, l.HasOperationV2WithStatus)
			queued := false
			for _, e := range *got {
				queued = queued || e.id == transcode
			}
			if queued != tc.wantQueued {
				t.Errorf("transcode enqueued = %t, want %t", queued, tc.wantQueued)
			}
			if want := len(bootOps) - 1; len(*got) < want {
				t.Errorf("enqueued %d ops, want at least the other %d", len(*got), want)
			}
		})
	}
}

// A lookup error fails open: the transcode is still enqueued.
func TestEnqueueBootOps_LookupErrorFailsOpen(t *testing.T) {
	got, enqueue := p6Recorder(nil)
	enqueueBootOps(context.Background(), enqueue, func(string, string) (bool, error) {
		return false, errors.New("synthetic lookup failure")
	})
	if len(*got) != len(bootOps) {
		t.Fatalf("enqueued %d ops, want all %d", len(*got), len(bootOps))
	}
}
