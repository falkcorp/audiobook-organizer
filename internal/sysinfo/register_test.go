// file: internal/sysinfo/register_test.go
// version: 1.0.1
// last-edited: 2026-11-08

package sysinfo_test

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
	"github.com/falkcorp/audiobook-organizer/internal/sysinfo"
)

func TestDashboardRegistration(t *testing.T) {
	c := serviceregistry.NewContainerFrom(sysinfo.ServiceDefs()...).
		Override("store", mocks.NewMockStore(t))
	if err := c.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := c.Build(t.Context()); err != nil {
		t.Fatalf("build: %v", err)
	}
	svc := serviceregistry.Get[*sysinfo.DashboardService](c, "dashboard")
	if svc == nil {
		t.Fatal("DashboardService is nil")
	}
}
