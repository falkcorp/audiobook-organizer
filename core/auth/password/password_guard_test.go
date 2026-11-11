// file: core/auth/password/password_guard_test.go
// version: 3.1.0
// guid: b81d3e64-2a97-4c05-8f1e-d7a09c5e3b72
// last-edited: 2026-11-10

package password

import (
	"testing"
)

// Outside a test binary New refuses every cost-lowering field, and with none
// set it builds a production Hasher.
func TestNewRefusesLoweredCostOutsideATestBinary(t *testing.T) {
	notTest := func() bool { return false }
	for name, o := range map[string]Options{
		"time":    {Time: 1},
		"memory":  {MemoryKiB: 8},
		"compute": {Compute: func([]byte, []byte, uint32, uint32, uint8, uint32) []byte { return nil }},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: New lowered the cost outside a test binary", name)
				}
			}()
			newHasher(o, notTest)
		}()
	}
	if h := newHasher(Options{Permits: 2}, notTest); h.time != argonTime || h.memory != argonMemory || h.Permits() != 2 {
		t.Fatalf("production Hasher: time=%d memory=%d permits=%d", h.time, h.memory, h.Permits())
	}
}
