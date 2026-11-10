// file: core/kernel/health.go
// version: 1.0.0
// guid: c95c4573-5303-4883-ab14-5c79d5e895ea
// last-edited: 2026-11-10

package kernel

import (
	"context"
	"errors"
	"fmt"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/storage"
)

// The kernel's own health components (05-health §5H.3.1, §5H.10 F2). They
// need no module handle: the kernel holds the store and the keyring itself, so
// it registers them kernel-side with the App's one health.Registry, which is
// what lets F3's cutover raise storage_quarantine on core.storage and
// secrets_undecryptable / secrets_keyring_not_exported on core.secrets from
// its first boot (IMPL-12).
const (
	// componentStorage is the open store: Critical and Readiness, probed once
	// at registration by an engine ping (replacing HEAD's CountAuthors).
	componentStorage health.ComponentID = "core.storage"
	// componentSecrets is the keyring: neither Critical nor Readiness, push-only,
	// Up until F3-03's secrets raises a token on it.
	componentSecrets health.ComponentID = "core.secrets"
)

// The Reasons the engine ping reports.
const reasonEnginePingFailed = "engine_ping_failed"

// enginePinger is the part of the open store the core.storage probe reads:
// *storage.Admin, from storage.DB.Admin. EngineSample reads the engine's
// in-memory counters under the store's lease, so it is cheap and a closed
// store answers storage.ErrClosed.
type enginePinger interface {
	EngineSample() (storage.EngineSample, error)
}

// kernelReporters are the push handles of the kernel's own components. The
// kernel keeps them for later phases: F3's storage and secrets tokens, the
// executor's Ready wait on core.storage.
type kernelReporters struct {
	Storage health.Reporter // core.storage
	Secrets health.Reporter // core.secrets
}

// buildHealth builds the App's health.Registry and registers core.storage and
// core.secrets on it. core.storage's first Result is its probe's: Up when
// the engine answers, Down with engine_ping_failed when it does not (an
// error is returned only when the registration itself fails). core.secrets
// starts Up. F2-16's boot calls this once the store is open; it exposes
// nothing on app.App, and the probe is not scheduled (F8).
func buildHealth(store enginePinger) (*health.Registry, kernelReporters, error) {
	if store == nil {
		return nil, kernelReporters{}, errors.New("kernel: health: core.storage needs the open store")
	}
	reg := health.NewRegistry()
	var rep kernelReporters
	var err error
	if rep.Storage, err = reg.Register(health.Check{
		ID:        componentStorage,
		Title:     "Storage engine",
		Critical:  true,
		Readiness: true,
		Probe:     enginePing(store),
	}); err != nil {
		return nil, kernelReporters{}, fmt.Errorf("kernel: health: %w", err)
	}
	if rep.Secrets, err = reg.Register(health.Check{
		ID:    componentSecrets,
		Title: "Secrets keyring",
	}); err != nil {
		return nil, kernelReporters{}, fmt.Errorf("kernel: health: %w", err)
	}
	rep.Secrets.Report(health.Result{Status: health.StatusUp})
	return reg, rep, nil
}

// enginePing is core.storage's probe: one engine read.
func enginePing(store enginePinger) func(context.Context) health.Result {
	return func(ctx context.Context) health.Result {
		if err := ctx.Err(); err != nil {
			return health.Result{Status: health.StatusDown, Reason: reasonEnginePingFailed, Detail: err.Error()}
		}
		if _, err := store.EngineSample(); err != nil {
			return health.Result{Status: health.StatusDown, Reason: reasonEnginePingFailed, Detail: err.Error()}
		}
		return health.Result{Status: health.StatusUp}
	}
}
