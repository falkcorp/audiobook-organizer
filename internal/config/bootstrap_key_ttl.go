// file: internal/config/bootstrap_key_ttl.go
// version: 1.0.0
// guid: 462bfc58-0e43-477d-b724-c90fd6c2cfa5
// last-edited: 2026-10-07

package config

import (
	"fmt"
	"strings"
	"time"
)

const (
	// DefaultBootstrapKeyTTL is how long a bootstrap-issued key lives when
	// nothing configures it. A bootstrap key is a full-scope admin credential
	// handed out for one working session, so it is short.
	DefaultBootstrapKeyTTL = 8 * time.Hour
	// MaxBootstrapKeyTTL caps any configured bootstrap key lifetime, new
	// setting or legacy days alike. A day covers any session that needs the
	// break-glass key.
	MaxBootstrapKeyTTL = 24 * time.Hour
)

// ResolveBootstrapKeyTTL returns how long a bootstrap-issued API key lives,
// and a warning to log when the configuration was not used as written (empty
// when it was, or when nothing was configured).
//
// Precedence: BootstrapKeyTTL when set; else the deprecated
// BootstrapKeyTTLDays when positive; else DefaultBootstrapKeyTTL. Every path
// is capped at MaxBootstrapKeyTTL, and an unparseable or non-positive
// duration falls back to the default: a bootstrap key always expires.
func (c *Config) ResolveBootstrapKeyTTL() (time.Duration, string) {
	if raw := strings.TrimSpace(c.BootstrapKeyTTL); raw != "" {
		d, err := time.ParseDuration(raw)
		switch {
		case err != nil:
			return DefaultBootstrapKeyTTL, fmt.Sprintf("bootstrap_key_ttl %q is not a duration (e.g. 8h); using %s", raw, DefaultBootstrapKeyTTL)
		case d <= 0:
			return DefaultBootstrapKeyTTL, fmt.Sprintf("bootstrap_key_ttl %q must be positive (bootstrap keys always expire); using %s", raw, DefaultBootstrapKeyTTL)
		case d > MaxBootstrapKeyTTL:
			return MaxBootstrapKeyTTL, fmt.Sprintf("bootstrap_key_ttl %q is over the %s cap; using %s", raw, MaxBootstrapKeyTTL, MaxBootstrapKeyTTL)
		}
		if c.BootstrapKeyTTLDays > 0 {
			return d, fmt.Sprintf("bootstrap_key_ttl_days is deprecated and ignored because bootstrap_key_ttl is set; remove bootstrap_key_ttl_days=%d", c.BootstrapKeyTTLDays)
		}
		return d, ""
	}
	if c.BootstrapKeyTTLDays > 0 {
		d := time.Duration(c.BootstrapKeyTTLDays) * 24 * time.Hour
		if d > MaxBootstrapKeyTTL {
			d = MaxBootstrapKeyTTL
		}
		return d, fmt.Sprintf("bootstrap_key_ttl_days=%d is deprecated; bootstrap keys now live at most %s, so using %s. Replace it with bootstrap_key_ttl (a duration, default %s)",
			c.BootstrapKeyTTLDays, MaxBootstrapKeyTTL, d, DefaultBootstrapKeyTTL)
	}
	return DefaultBootstrapKeyTTL, ""
}
