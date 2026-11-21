// file: core/kernel/identity_boot_test.go
// version: 0.1.0
// guid: 5428afc5-7971-4d76-8c20-b603d1f551f1
// last-edited: 2026-11-21

package kernel

import "testing"

// TestIdentityBootDeclared_IsTheFlagAlone pins the switch: the kernel decides
// from Config.IdentityBoot and from nothing else in its config.
func TestIdentityBootDeclared_IsTheFlagAlone(t *testing.T) {
	if (&Kernel{cfg: Config{IdentityBoot: true}}).identityBootDeclared() != true {
		t.Error("IdentityBoot=true does not declare the identity boot")
	}
	if (&Kernel{cfg: Config{}}).identityBootDeclared() {
		t.Error("the zero Config declares the identity boot")
	}
}
