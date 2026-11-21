// file: core/config/permissions_test.go
// version: 0.1.0
// guid: 5e8b2d94-0c3a-4f17-b6a1-9d47e30c8f25
// last-edited: 2026-11-21

package config_test

import (
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/config/configtest"
)

type ledgerSettings struct {
	Entries int `json:"entries"`
}

// TestStore_ReadPermission: the read permission of each registered section is
// its declared one, or 05-config 1.3.4's default; a key's path, a parent of a
// section and an unregistered path read nothing, so a filter built on it fails
// closed.
func TestStore_ReadPermission(t *testing.T) {
	r := newRig(t, rigOpts{})
	if _, err := config.Register(r.st.Registry(), config.Define("core.ledger", ledgerSettings{},
		config.Permissions("core:ledger.view", "core:ledger.edit"))); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path config.Path
		want string
		ok   bool
	}{
		{configtest.BatcavePath, "core:settings.manage", true},
		{configtest.WatchPath, "gotham:settings.manage", true},
		{"core.ledger", "core:ledger.view", true},
		{"core.ledger.entries", "", false},
		{"core", "", false},
		{"core.unregistered", "", false},
		{"", "", false},
	} {
		got, ok := r.st.ReadPermission(c.path)
		if got != c.want || ok != c.ok {
			t.Errorf("ReadPermission(%q) = %q, %v; want %q, %v", c.path, got, ok, c.want, c.ok)
		}
	}
}

// TestStore_ReadPermissionInsideSchemaFilter: Schema calls its include filter
// under the store mutex, and the settings routes ask ReadPermission from it, so
// ReadPermission must not take that mutex.
func TestStore_ReadPermissionInsideSchemaFilter(t *testing.T) {
	r := newRig(t, rigOpts{})
	seen := 0
	if _, err := r.st.Schema(func(sec config.Path) bool {
		if _, ok := r.st.ReadPermission(sec); !ok {
			t.Errorf("section %q has no read permission", sec)
		}
		seen++
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if seen == 0 {
		t.Fatal("the filter never ran")
	}
}
