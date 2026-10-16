// file: internal/importer/dedup_on_import_test.go
// version: 1.0.0
// guid: 9b2e5d71-3c84-4a0f-8e16-d47a0c5f2b39
// last-edited: 2026-10-16

package importer

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

type enqueueCall struct {
	defID  string
	params string
}

type recordingRegistry struct {
	mu    sync.Mutex
	calls []enqueueCall
}

func (f *recordingRegistry) RegisterOp(sdk.OperationDef) error { return nil }

func (f *recordingRegistry) EnqueueOp(_ context.Context, defID string, params any, _ ...sdk.EnqueueOption) (string, error) {
	raw, _ := json.Marshal(params)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, enqueueCall{defID: defID, params: string(raw)})
	return "", nil
}

func (f *recordingRegistry) snapshot() []enqueueCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]enqueueCall(nil), f.calls...)
}

// checkBookProbe returns a dedup engine whose CheckBook reports each book ID
// it is asked about on the returned channel and then stops.
func checkBookProbe(t *testing.T) (*dedup.Engine, <-chan string) {
	t.Helper()
	checked := make(chan string, 4)
	store := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			checked <- id
			return nil, errors.New("probe: stop here")
		},
	}
	engine, err := dedup.NewEngine(nil, store, nil, nil, nil, unified.DefaultScoreConfig())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return engine, checked
}

func withOnImportViaScheduler(t *testing.T, on bool) {
	t.Helper()
	prev := config.AppConfig.Dedup.OnImportViaScheduler
	config.AppConfig.Dedup.OnImportViaScheduler = on
	t.Cleanup(func() { config.AppConfig.Dedup.OnImportViaScheduler = prev })
}

func TestImportFile_DedupViaScheduler_EnqueuesOpAndStartsNoGoroutine(t *testing.T) {
	withSupportedExt(t)
	withOnImportViaScheduler(t, true)
	dir, path := stageFixture(t)

	var rows []*database.BookFile
	is := NewImportService(importStore(dir, &rows))
	engine, checked := checkBookProbe(t)
	is.SetDedupEngine(engine)
	reg := &recordingRegistry{}
	is.SetRegistry(reg)

	resp, err := is.ImportFile(&ImportFileRequest{FilePath: path})
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}

	calls := reg.snapshot()
	want := enqueueCall{defID: "dedup.check-book", params: `{"book_id":"` + resp.ID + `"}`}
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("enqueue calls = %+v, want exactly [%+v]", calls, want)
	}
	select {
	case id := <-checked:
		t.Fatalf("CheckBook ran for %s in a goroutine; the scheduler path must replace it", id)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestImportFile_DedupFlagOff_RunsGoroutineAndEnqueuesNothing(t *testing.T) {
	withSupportedExt(t)
	withOnImportViaScheduler(t, false)
	dir, path := stageFixture(t)

	var rows []*database.BookFile
	is := NewImportService(importStore(dir, &rows))
	engine, checked := checkBookProbe(t)
	is.SetDedupEngine(engine)
	reg := &recordingRegistry{}
	is.SetRegistry(reg)

	resp, err := is.ImportFile(&ImportFileRequest{FilePath: path})
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}

	select {
	case id := <-checked:
		if id != resp.ID {
			t.Fatalf("CheckBook ran for %s, want %s", id, resp.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CheckBook never ran: the flag-off goroutine path is broken")
	}
	if calls := reg.snapshot(); len(calls) != 0 {
		t.Fatalf("enqueue calls = %+v, want none with the flag off", calls)
	}
}
