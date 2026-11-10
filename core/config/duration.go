// file: core/config/duration.go
// version: 1.0.0
// guid: 65116f4b-d03a-4188-b162-63853d3463f8
// last-edited: 2026-11-10

package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Duration is a time.Duration that (un)marshals as a Go duration string
// ("90s", "24h"). Decoding is strict (05-config §1.3.6): a JSON number, such
// as a bare count of nanoseconds, is refused.
type Duration time.Duration

// Std returns d as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// MarshalJSON writes d as a JSON string in Go duration syntax, without the
// zero units time.Duration.String adds ("24h", not "24h0m0s").
func (d Duration) MarshalJSON() ([]byte, error) {
	s := time.Duration(d).String()
	if strings.HasSuffix(s, "m0s") {
		s = s[:len(s)-len("0s")]
	}
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-len("0m")]
	}
	return json.Marshal(s)
}

// UnmarshalJSON reads a JSON string in Go duration syntax.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("config: duration must be a string such as \"90s\" or \"24h\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("config: invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}
