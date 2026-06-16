// file: internal/updater/register.go
// version: 2.1.0
// guid: 8c9d0a1b-2c3d-4e5f-6a7b-8c9d0a1b2c3d
// last-edited: 2026-06-16
//
// Service registry registrations for the auto-updater + its scheduler.
//
// Two services:
//   - "updater":         *Updater. Real version comes from the host via
//                        Override("appversion", appVersion). Falls back to
//                        "dev" when not overridden (matches the historical
//                        inline default).
//   - "updatescheduler": *SchedulerStarterAdapter wrapping *Scheduler.
//                        Depends on "updater". Implements Starter/Stopper
//                        for Container.Start / Container.Stop hand-off
//                        once SERVER-LIFECYCLE-FLIP wires those.

package updater

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// SchedulerStarterAdapter wraps *Scheduler to implement the Starter/Stopper interfaces.
// Scheduler.Start() and Stop() take no context parameter, so we adapt them here.
type SchedulerStarterAdapter struct {
	scheduler *Scheduler
}

// Scheduler returns the wrapped *Scheduler. Nil-safe.
func (a *SchedulerStarterAdapter) Scheduler() *Scheduler {
	if a == nil {
		return nil
	}
	return a.scheduler
}

// Start implements the serviceregistry.Starter interface.
func (a *SchedulerStarterAdapter) Start(_ context.Context) error {
	if a == nil || a.scheduler == nil {
		return nil
	}
	a.scheduler.Start()
	return nil
}

// Stop implements the serviceregistry.Stopper interface.
func (a *SchedulerStarterAdapter) Stop(_ context.Context) error {
	if a == nil || a.scheduler == nil {
		return nil
	}
	a.scheduler.Stop()
	return nil
}

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "updater",
		Needs:  []string{},
		Groups: []string{"scheduler"},
		Build: func(c *serviceregistry.Container) (any, error) {
			// appversion is an Override the host (server) sets to the
			// ldflags-baked version. TryGet falls back to "dev" so the
			// container can build in isolation (tests, tooling).
			version, ok := serviceregistry.TryGet[string](c, "appversion")
			if !ok || version == "" {
				version = "dev"
			}
			return NewUpdater(version), nil
		},
	})

	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "updatescheduler",
		Needs:  []string{"updater"},
		Groups: []string{"scheduler"},
		Build: func(c *serviceregistry.Container) (any, error) {
			upd := serviceregistry.Get[*Updater](c, "updater")
			scheduler := NewScheduler(upd, func() SchedulerConfig {
				return SchedulerConfig{
					Enabled:     config.AppConfig.AutoUpdate.Enabled,
					Channel:     config.AppConfig.AutoUpdate.Channel,
					CheckMins:   config.AppConfig.AutoUpdate.CheckMinutes,
					WindowStart: config.AppConfig.AutoUpdate.WindowStart,
					WindowEnd:   config.AppConfig.AutoUpdate.WindowEnd,
				}
			})
			return &SchedulerStarterAdapter{scheduler: scheduler}, nil
		},
	})
}
