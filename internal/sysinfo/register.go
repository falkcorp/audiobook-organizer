// file: internal/sysinfo/register.go
// version: 1.1.0
// last-edited: 2026-08-19

package sysinfo

import (
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyDashboard,
		Needs:  []string{serviceregistry.KeyStore},
		Groups: []string{"core"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[dashboardStore](c, serviceregistry.KeyStore)
			return NewDashboardService(store), nil
		},
	})
}
