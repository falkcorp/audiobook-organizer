// file: core/app/corequirement_test.go
// version: 1.0.0
// guid: 5c0e7a42-9d1b-4f63-a8e5-2b7d3c9f1a64
// last-edited: 2026-11-28

package app

import (
	"testing"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// Batman names only.

// TestKernelConfig_PlumbsRouteCoRequirements: Options.RouteCoRequirements
// reaches kernel.Config (the router reads it from there) with every field of
// an entry, the route-wide and the per-value alike, the kernel gets its own
// copy so the app editing its slice after New cannot change what the router
// ANDs on, and unset it reaches the kernel as an empty list, which the router
// treats as no co-requirement at all.
func TestKernelConfig_PlumbsRouteCoRequirements(t *testing.T) {
	co := []httpapi.CoRequirement{
		{Route: "signalBat", Need: rbac.Need("gotham:signal.send")},
		{Route: "cave", Param: "kind", Value: "bat", Need: rbac.Need("gotham:cave.enter")},
	}
	on, err := kernelConfig(Options{AppID: "gotham", RouteCoRequirements: co}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := on.RouteCoRequirements
	if len(got) != 2 || got[0].Route != "signalBat" || got[0].Need.String() != "gotham:signal.send" || got[0].Param != "" ||
		got[1].Route != "cave" || got[1].Param != "kind" || got[1].Value != "bat" || got[1].Need.String() != "gotham:cave.enter" {
		t.Fatalf("kernel.Config.RouteCoRequirements = %+v, want signalBat route-wide and cave[kind=bat]", got)
	}
	co[0].Route, co[1].Value = "edited", "edited"
	co = append(co[:1], co[1:]...)
	if on.RouteCoRequirements[0].Route != "signalBat" || on.RouteCoRequirements[1].Value != "bat" {
		t.Errorf("the kernel's list follows the app's after New: %+v", on.RouteCoRequirements)
	}

	off, err := kernelConfig(Options{AppID: "gotham"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(off.RouteCoRequirements) != 0 {
		t.Errorf("an app with no co-requirements reached the kernel with %v", off.RouteCoRequirements)
	}
}
