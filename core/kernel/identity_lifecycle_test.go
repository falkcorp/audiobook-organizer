// file: core/kernel/identity_lifecycle_test.go
// version: 0.1.0
// guid: 6f1b3d82-7c04-4a59-b2e6-0d9a85c71f43
// last-edited: 2026-11-22

package kernel

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/module"
)

// Batman names only.

// lifecycleProbe boots the probe host with Manifest.AuthLifecycle declared by
// alfred (declare) and not by its sibling batcave.
func lifecycleProbe(t *testing.T, switchOn, declare bool) *depsProbe {
	t.Helper()
	h, c, p := probeHostDeclaring(t, switchOn, false)
	alfred, ok := c.Modules[1].(testModule)
	if !ok {
		t.Fatalf("probe module is %T, not a testModule", c.Modules[1])
	}
	c.Modules[1] = alfred.with(func(m *module.Manifest) { m.AuthLifecycle = declare })
	k := h.mustBoot(c)
	t.Cleanup(func() { h.close(k) })
	return p
}

// TestIdentityP5_AuthLifecycleHandle: only a module that declares
// Manifest.AuthLifecycle is handed Deps.AuthLifecycle, on an identity boot
// only, and it is auth's one-shot registration: a second call is refused.
func TestIdentityP5_AuthLifecycleHandle(t *testing.T) {
	t.Run("declared", func(t *testing.T) {
		p := lifecycleProbe(t, true, true)
		if p.deps.AuthLifecycle == nil {
			t.Fatal("a module declaring AuthLifecycle was handed no Deps.AuthLifecycle")
		}
		if p.sibling.AuthLifecycle != nil {
			t.Error("a module that did not declare AuthLifecycle was handed it")
		}
		if err := p.deps.AuthLifecycle(auth.Lifecycle{StatusChanged: func(context.Context, auth.StatusChanged) {}}); err != nil {
			t.Fatalf("first registration: %v", err)
		}
		if err := p.deps.AuthLifecycle(auth.Lifecycle{}); !errors.Is(err, auth.ErrLifecycleRegistered) {
			t.Fatalf("second registration = %v, want auth.ErrLifecycleRegistered", err)
		}
	})
	t.Run("not declared", func(t *testing.T) {
		if p := lifecycleProbe(t, true, false); p.deps.AuthLifecycle != nil || p.sibling.AuthLifecycle != nil {
			t.Error("Deps.AuthLifecycle was handed to a module that did not declare it")
		}
	})
	t.Run("no identity boot", func(t *testing.T) {
		if p := lifecycleProbe(t, false, true); p.deps.AuthLifecycle != nil {
			t.Error("Deps.AuthLifecycle was handed on a boot without IdentityBoot")
		}
	})
}
