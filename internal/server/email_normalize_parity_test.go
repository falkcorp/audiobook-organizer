// file: internal/server/email_normalize_parity_test.go
// version: 1.0.0
// guid: 63fee68c-3136-4644-8c3c-272f2a9b335b
// last-edited: 2026-10-20

package server

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/cfaccesspolicy"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// cfaccesspolicy repeats util.NormalizeEmail because it imports no module
// package. The address it writes to the Cloudflare policy must be the one the
// store keys grants and verified emails by, or a revoke would miss the entry.
func TestCFAccessPolicyEmailMatchesUtil(t *testing.T) {
	for _, in := range []string{
		" Guest@Example.COM ",
		"\u212aate@example.com",
		"ro\u017fe@example.com",
		"\uff41\uff44\uff4d\uff49\uff4e@example.com",
	} {
		got, err := cfaccesspolicy.NormalizeEmail(in)
		if err != nil {
			t.Fatalf("cfaccesspolicy.NormalizeEmail(%q): %v", in, err)
		}
		if want := util.NormalizeEmail(in); got != want {
			t.Errorf("cfaccesspolicy.NormalizeEmail(%q) = %q, util.NormalizeEmail = %q", in, got, want)
		}
	}
}
