// file: internal/server/absauth/saturation.go
// version: 1.0.0
// guid: f4881432-1772-4d8d-a653-33fde6815d7a
// last-edited: 2026-10-21

package absauth

import (
	"time"

	"github.com/gin-gonic/gin"
)

// Saturation: the aggregate login ceilings (server.loginCeiling) are spent.
//
// A ceiling that refused every login once spent would let an anonymous flood
// lock out every user, the owner included. Instead the ceiling middleware marks
// the request saturated (MarkLoginSaturated) and the login handlers, which are
// the ones that know the account, apply SaturationPolicy before any password
// work: an account with few failures of its own is delayed, never refused, and
// only an account that has already failed SoftThreshold times in the window is
// refused (429). The per-visitor limit refuses regardless, as it always did.
const (
	// SaturationDelayBase is the delay a saturated login with no failures of its
	// own on the account takes before password work.
	SaturationDelayBase = time.Second
	// SaturationDelayStep is added per failure already counted on the account.
	SaturationDelayStep = 500 * time.Millisecond
	// SaturationDelayMax caps the delay.
	SaturationDelayMax = 3 * time.Second
)

const loginSaturatedKey = "login_ceiling_saturated"

// MarkLoginSaturated records on c that the aggregate ceilings are spent.
func MarkLoginSaturated(c *gin.Context) { c.Set(loginSaturatedKey, true) }

// LoginSaturated reports whether the aggregate ceilings were spent when c was
// admitted. Never true for a request the ceilings exempt.
func LoginSaturated(c *gin.Context) bool {
	v, _ := c.Get(loginSaturatedKey)
	b, _ := v.(bool)
	return b
}

// SaturationPolicy is what a saturated login for an account with
// accountFailures failures of its own in the window gets: a delay to apply
// before password work, or a refusal once the account has failed
// SoftThreshold times. accountFailures is 0 for an unknown username.
func SaturationPolicy(accountFailures int) (delay time.Duration, refuse bool) {
	if accountFailures >= SoftThreshold {
		return 0, true
	}
	return min(SaturationDelayBase+time.Duration(accountFailures)*SaturationDelayStep, SaturationDelayMax), false
}
