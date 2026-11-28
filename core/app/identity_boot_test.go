// file: core/app/identity_boot_test.go
// version: 0.1.0
// guid: dd591d24-14d7-4273-8101-0cd969f70a0d
// last-edited: 2026-11-21

package app

import (
	"testing"

	"github.com/falkcorp/mediacore/auth"
)

// TestKernelConfig_IdentityBootIsTheExplicitFlag: only Options.IdentityBoot
// reaches the kernel's switch. ABO sets Auth and Authenticators before its
// flip and must not turn the identity boot on by doing so.
func TestKernelConfig_IdentityBootIsTheExplicitFlag(t *testing.T) {
	on, err := kernelConfig(Options{AppID: "gotham", IdentityBoot: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !on.IdentityBoot {
		t.Error("Options.IdentityBoot=true did not reach kernel.Config")
	}
	abo, err := kernelConfig(Options{
		AppID:          "gotham",
		Auth:           auth.Options{ImportedKeyPrefixes: []string{"abk_"}, DefaultDisabledAllowRemote: true, OIDCCallbackBase: "/api/v1/auth/oauth"},
		Authenticators: []auth.Extension{{}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if abo.IdentityBoot {
		t.Error("an app that sets Auth and Authenticators but not IdentityBoot declared the identity boot")
	}
}
