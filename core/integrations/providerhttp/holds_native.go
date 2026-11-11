// file: core/integrations/providerhttp/holds_native.go
// version: 1.0.0
// guid: c262d903-994f-4de9-ae06-9d7e83132fdf
// last-edited: 2026-11-11

package providerhttp

import (
	"errors"
	"fmt"

	"github.com/falkcorp/mediacore/tasks"
)

// ReleaseHolds is the NATIVE body of core.integrations.release-holds (05-metadata
// M2.4): it lists every active hold of every scope (quota, auth and rate-limit
// holds alike) as a line of the run's log and, in a Live run, releases each
// through ReleaseHold. A Preview run lists and releases nothing. Quota counters
// and breaker state are left as they are.
//
// It has the kind's Run signature, so *Registry satisfies InterimHolds and
// ReleaseHoldsTask(registry) is the native registration. F7-09 switches the
// registration to it and deletes the interface; until then ABO registers its
// own backend and nothing calls this.
func (r *Registry) ReleaseHolds(rc *tasks.RC, _ ReleaseHoldsParams) (ReleaseHoldsResult, error) {
	var res ReleaseHoldsResult
	var errs []error
	live := rc.Mode() == tasks.ModeLive
	for h := range r.Holds() {
		res.Listed++
		rc.Log().Info("provider hold", "scope", h.Scope, "class", h.Class.String(), "until", h.Until, "reason", h.Detail, "live", live)
		if !live {
			continue
		}
		if err := rc.Context().Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if err := r.ReleaseHold(rc.Context(), h.Scope); err != nil {
			errs = append(errs, fmt.Errorf("release a %s hold: %w", h.Class, err))
			continue
		}
		res.Released++
	}
	return res, errors.Join(errs...)
}
