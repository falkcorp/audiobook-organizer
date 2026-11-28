// file: internal/arch/principal_class_test.go
// version: 0.1.0
// guid: 9a4d1c6e-2f85-4b37-8e09-c3b7a5d61f28
// last-edited: 2026-11-22

package arch

import (
	"testing"

	"github.com/falkcorp/mediacore/archtest"
)

// TestPrincipalClassFromRegistry: no ABO site asks a core principal's Method
// for its classification (F4-08 B10y). Under core identity the credential
// guard and the owner fallback ask the principal, which carries the
// Cloudflare Access extension's registered MethodSpec; asking
// coreauth.Method answers false for ext:cfaccess and refuses the Access
// sign-in that HEAD's cf_access admitted. No exemptions.
func TestPrincipalClassFromRegistry(t *testing.T) {
	archtest.PrincipalClassFromRegistry(t, repoRoot(t), nil)
}
