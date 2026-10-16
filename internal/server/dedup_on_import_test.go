// file: internal/server/dedup_on_import_test.go
// version: 1.0.0
// guid: 4a8d1e6c-7b25-4f93-a0c2-5e9b3d7f1c48
// last-edited: 2026-10-16

package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// dedupProbeEngine returns an engine whose CheckBook reports each book ID on
// the channel and stops at the first store read.
func dedupProbeEngine(t *testing.T) (*dedup.Engine, <-chan string) {
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

// checkBookStandIn registers a batchable op under dedup.check-book's ID on a
// real registry and reports the subject IDs of every run it gets.
func checkBookStandIn(t *testing.T) (*opsregistry.Registry, <-chan []string) {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("NewPebbleStoreInMemory: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	runs := make(chan []string, 4)
	reg := opsregistry.New(store, slog.New(slog.DiscardHandler), 1, nil)
	err = reg.RegisterOp(opsregistry.OperationDef{
		ID:           dedup.CheckBookOpID,
		Liveness:     opsregistry.LivenessManual,
		Plugin:       "test",
		DisplayName:  "check-book stand-in",
		ResumePolicy: opsregistry.ResumeDrop,
		Batchable:    true,
		BatchWindow:  50 * time.Millisecond,
		BatchMaxWait: 5 * time.Second,
		Run: func(_ context.Context, raw json.RawMessage, _ opsregistry.Reporter) error {
			var p struct {
				Subjects []database.OpSubject `json:"subjects"`
			}
			if err := json.Unmarshal(raw, &p); err != nil {
				return err
			}
			ids := make([]string, 0, len(p.Subjects))
			for _, s := range p.Subjects {
				ids = append(ids, s.ID)
			}
			runs <- ids
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	reg.Start(t.Context())
	t.Cleanup(func() { _ = reg.Shutdown(context.Background()) })
	return reg, runs
}

func setOnImportViaScheduler(t *testing.T, on bool) {
	t.Helper()
	prev := config.AppConfig.Dedup.OnImportViaScheduler
	config.AppConfig.Dedup.OnImportViaScheduler = on
	t.Cleanup(func() { config.AppConfig.Dedup.OnImportViaScheduler = prev })
}

func TestFireDedupOnImport_ViaScheduler_EnqueuesOpAndStartsNoGoroutine(t *testing.T) {
	setOnImportViaScheduler(t, true)
	engine, checked := dedupProbeEngine(t)
	reg, runs := checkBookStandIn(t)
	s := &Server{dedupEngine: engine, opRegistry: reg, bgCtx: t.Context()}

	s.fireDedupOnImport("book-1")

	if running := s.bgWG.Running(); len(running) != 0 {
		t.Fatalf("background goroutines started: %v", running)
	}
	select {
	case ids := <-runs:
		if len(ids) != 1 || ids[0] != "book-1" {
			t.Fatalf("dedup.check-book run subjects = %v, want [book-1]", ids)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no dedup.check-book run: the book was not enqueued")
	}
	select {
	case id := <-checked:
		t.Fatalf("CheckBook ran directly for %s; the op must replace the goroutine", id)
	default:
	}
}

func TestFireDedupOnImport_FlagOff_RunsGoroutine(t *testing.T) {
	setOnImportViaScheduler(t, false)
	engine, checked := dedupProbeEngine(t)
	reg, runs := checkBookStandIn(t)
	s := &Server{dedupEngine: engine, opRegistry: reg, bgCtx: t.Context()}

	s.fireDedupOnImport("book-1")
	s.bgWG.Wait()

	select {
	case id := <-checked:
		if id != "book-1" {
			t.Fatalf("CheckBook ran for %s, want book-1", id)
		}
	default:
		t.Fatal("CheckBook did not run on the flag-off path")
	}
	select {
	case ids := <-runs:
		t.Fatalf("dedup.check-book ran for %v with the flag off", ids)
	case <-time.After(300 * time.Millisecond):
	}
}
