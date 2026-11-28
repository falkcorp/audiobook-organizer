// file: core/app/corequirement_test.go
// version: 1.0.0
// guid: 5c0e7a42-9d1b-4f63-a8e5-2b7d3c9f1a64
// last-edited: 2026-11-28

package app

import (
	"testing"

	"github.com/falkcorp/mediacore/rbac"
)

// Batman names only.

// TestKernelConfig_PlumbsRouteCoRequirements: Options.RouteCoRequirements
// reaches kernel.Config (the router reads it from there), the kernel gets its
// own copy so the app editing its map after New cannot change what the router
// ANDs on, and unset it reaches the kernel as an empty map, which the router
// treats as no co-requirement at all.
func TestKernelConfig_PlumbsRouteCoRequirements(t *testing.T) {
	co := map[string]rbac.Requirement{"signalBat": rbac.Need("gotham:signal.send")}
	on, err := kernelConfig(Options{AppID: "gotham", RouteCoRequirements: co}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := on.RouteCoRequirements["signalBat"]; !ok || got.String() != "gotham:signal.send" || len(on.RouteCoRequirements) != 1 {
		t.Fatalf("kernel.Config.RouteCoRequirements = %v, want exactly signalBat -> gotham:signal.send", on.RouteCoRequirements)
	}
	co["cave"] = rbac.Need("gotham:cave.enter")
	delete(co, "signalBat")
	if _, ok := on.RouteCoRequirements["signalBat"]; !ok || len(on.RouteCoRequirements) != 1 {
		t.Errorf("the kernel's map follows the app's after New: %v", on.RouteCoRequirements)
	}

	off, err := kernelConfig(Options{AppID: "gotham"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(off.RouteCoRequirements) != 0 {
		t.Errorf("an app with no co-requirements reached the kernel with %v", off.RouteCoRequirements)
	}
}
