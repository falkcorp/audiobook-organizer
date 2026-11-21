// file: internal/config/bootstrap_key_ttl_test.go
// version: 1.1.0
// guid: 9a2f7c48-1e5b-4d93-b6a0-3c8d1f2e7b59
// last-edited: 2026-11-10

package config

import (
	"encoding/json"
	"testing"
	"time"
)

func TestResolveBootstrapKeyTTL(t *testing.T) {
	cases := []struct {
		name     string
		ttl      string
		days     int
		want     time.Duration
		wantWarn bool
	}{
		{"nothing configured is 8h", "", 0, 8 * time.Hour, false},
		{"explicit duration", "2h", 0, 2 * time.Hour, false},
		{"explicit duration over the cap", "72h", 0, 24 * time.Hour, true},
		{"unparseable falls back to 8h", "a week", 0, 8 * time.Hour, true},
		{"zero falls back, never 'never expire'", "0s", 0, 8 * time.Hour, true},
		{"negative falls back", "-1h", 0, 8 * time.Hour, true},
		{"legacy days honoured and warned", "", 1, 24 * time.Hour, true},
		{"legacy old default 30 capped at 24h", "", 30, 24 * time.Hour, true},
		{"new setting wins over legacy, with a warning", "4h", 30, 4 * time.Hour, true},
		{"non-positive legacy is unset", "", -5, 8 * time.Hour, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{BootstrapKeyTTL: tc.ttl, BootstrapKeyTTLDays: tc.days}
			got, warn := c.ResolveBootstrapKeyTTL()
			if got != tc.want {
				t.Errorf("ttl = %v, want %v", got, tc.want)
			}
			if (warn != "") != tc.wantWarn {
				t.Errorf("warning = %q, want a warning: %v", warn, tc.wantWarn)
			}
		})
	}
}

// The stored config blob once carried "bootstrap_key_ttl_days": 30 (the old
// viper default, materialized on every save). Loading such a blob must not
// make the legacy setting look configured, or every boot would use 24h and
// warn instead of using 8h.
func TestBootstrapKeyTTL_NotReadFromConfigBlob(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(`{"bootstrap_key_ttl_days":30,"bootstrap_key_ttl":"720h"}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.BootstrapKeyTTLDays != 0 || c.BootstrapKeyTTL != "" {
		t.Fatalf("blob keys leaked into the config: days=%d ttl=%q", c.BootstrapKeyTTLDays, c.BootstrapKeyTTL)
	}
	raw, err := json.Marshal(Config{BootstrapKeyTTL: "8h", BootstrapKeyTTLDays: 3})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for _, k := range []string{"bootstrap_key_ttl", "bootstrap_key_ttl_days"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s must not be written to the config blob / GET /config", k)
		}
	}
}
