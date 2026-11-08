// file: core/lifecycle/notify_test.go
// version: 1.0.0
// guid: d208a686-79a3-44aa-899a-96334b983455
// last-edited: 2026-11-08

package lifecycle

import "testing"

// The name is systemd's, fixed by sd_notify(3): a rename would silently stop
// every readiness signal and the child-environment scrub.
func TestNotifySocketEnv(t *testing.T) {
	if NotifySocketEnv != "NOTIFY_SOCKET" {
		t.Fatalf("NotifySocketEnv = %q, want NOTIFY_SOCKET", NotifySocketEnv)
	}
}
