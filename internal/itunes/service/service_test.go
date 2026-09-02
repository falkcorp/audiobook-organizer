// file: internal/itunes/service/service_test.go
// version: 1.1.1
// guid: 4ab6d921-bccd-4265-b04b-31faaacd5826
// last-edited: 2026-09-02

package itunesservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestNewDisabled_ReturnsService(t *testing.T) {
	svc := NewDisabled()
	if svc == nil {
		t.Fatal("NewDisabled returned nil")
	}
	if svc.Enabled() {
		t.Error("disabled service should report Enabled() == false")
	}
}

func TestNew_WithDisabledConfig_ReturnsDisabledService(t *testing.T) {
	svc, err := New(Deps{Config: Config{Enabled: false}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if svc.Enabled() {
		t.Error("service constructed with Enabled=false should report Enabled() == false")
	}
}

func TestService_StartShutdown_Disabled_NoOp(t *testing.T) {
	svc := NewDisabled()
	if err := svc.Start(context.Background()); err != nil {
		t.Errorf("Start on disabled: %v", err)
	}
	if err := svc.Shutdown(100 * time.Millisecond); err != nil {
		t.Errorf("Shutdown on disabled: %v", err)
	}
}

func TestErrITunesDisabled_Exported(t *testing.T) {
	// Sanity check that ErrITunesDisabled exists and is an error. Prevents
	// an accidental rename from breaking call sites that sentinel-check.
	if !errors.Is(ErrITunesDisabled, ErrITunesDisabled) {
		t.Fatal("ErrITunesDisabled failed errors.Is identity check")
	}
}

// ---------------------------------------------------------------------------
// New — enabled happy path
// ---------------------------------------------------------------------------

// minimalDeps returns the smallest Deps that passes New with Enabled=true.
// No real Store methods are called during construction, so a zero-value
// MockStore is sufficient.
func minimalDeps() Deps {
	return Deps{
		Store:  &database.MockStore{},
		Config: Config{Enabled: true},
	}
}

func TestNew_Enabled_ConstructsAllSubComponents(t *testing.T) {
	svc, err := New(minimalDeps())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if svc == nil {
		t.Fatal("New returned nil")
	}
	if !svc.Enabled() {
		t.Error("service constructed with Enabled=true should report Enabled() == true")
	}

	// All sub-components must be wired — nil means a wiring step was skipped.
	if svc.Batcher == nil {
		t.Error("Batcher is nil")
	}
	if svc.Provisioner == nil {
		t.Error("Provisioner is nil")
	}
	if svc.Positions == nil {
		t.Error("Positions is nil")
	}
	if svc.Playlists == nil {
		t.Error("Playlists is nil")
	}
	if svc.Paths == nil {
		t.Error("Paths is nil")
	}
	if svc.Repair == nil {
		t.Error("Repair is nil")
	}
	if svc.Transfer == nil {
		t.Error("Transfer is nil")
	}
	if svc.Importer == nil {
		t.Error("Importer is nil")
	}
}

func TestNew_Enabled_NilLoggerDefaulted(t *testing.T) {
	// Passing a nil Logger must not panic — New injects a default.
	deps := minimalDeps()
	deps.Logger = nil
	svc, err := New(deps)
	if err != nil {
		t.Fatalf("New with nil logger: %v", err)
	}
	if svc == nil {
		t.Fatal("New returned nil")
	}
}

// ---------------------------------------------------------------------------
// Start / Shutdown — enabled path
// ---------------------------------------------------------------------------

func TestService_Start_Enabled_NoError(t *testing.T) {
	svc, err := New(minimalDeps())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Errorf("Start on enabled service: %v", err)
	}
}

func TestService_Shutdown_Enabled_NoError(t *testing.T) {
	svc, err := New(minimalDeps())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Shutdown(100 * time.Millisecond); err != nil {
		t.Errorf("Shutdown on enabled service: %v", err)
	}
}

func TestService_StartShutdown_Enabled_Full(t *testing.T) {
	svc, err := New(minimalDeps())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := svc.Shutdown(200 * time.Millisecond); err != nil {
		t.Fatalf("Shutdown after Start: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Enabled() accessor — all states
// ---------------------------------------------------------------------------

func TestEnabled_DisabledService(t *testing.T) {
	svc := NewDisabled()
	if svc.Enabled() {
		t.Error("NewDisabled().Enabled() should return false")
	}
}

func TestEnabled_EnabledService(t *testing.T) {
	svc, err := New(minimalDeps())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !svc.Enabled() {
		t.Error("Enabled service should report Enabled() == true")
	}
}

// ---------------------------------------------------------------------------
// Disabled-mode propagation via Start / Shutdown
// ---------------------------------------------------------------------------

func TestDisabledService_StartShutdown_MultipleCallsAreNoOps(t *testing.T) {
	svc := NewDisabled()

	for i := range 3 {
		if err := svc.Start(context.Background()); err != nil {
			t.Errorf("Start[%d] on disabled: %v", i, err)
		}
	}
	for i := range 3 {
		if err := svc.Shutdown(0); err != nil {
			t.Errorf("Shutdown[%d] on disabled: %v", i, err)
		}
	}
}
