// file: internal/server/absauth/saturation.go
// version: 1.2.0
// guid: f4881432-1772-4d8d-a653-33fde6815d7a
// last-edited: 2026-10-22

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

const (
	loginAccountKey    = "login_account_key"
	loginSatRefusedKey = "login_saturation_refused"
)

// NoteLoginAccount records on c the account key (AccountKey of the submitted
// username, whether or not that user exists) a login attempt names. The login
// ceilings read it after the handler answers, so that a successful login can
// hand back the failures the same visitor made against the same account and no
// others.
func NoteLoginAccount(c *gin.Context, accountKey string) { c.Set(loginAccountKey, accountKey) }

// LoginAccount is the key NoteLoginAccount recorded, "" when none was (a request
// that never named a username).
func LoginAccount(c *gin.Context) string {
	v, _ := c.Get(loginAccountKey)
	s, _ := v.(string)
	return s
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

// RefuseSaturatedLogin applies SaturationPolicy for a saturated login on account
// key accountKey: it applies the delay through delay when there is one and returns
// false, or, when the account has already failed SoftThreshold times, records the
// refusal on c and returns true so the caller answers 429. The policy is decided
// by the SUBMITTED name, so a name that does not exist is delayed and refused
// exactly like one that does. The refusal is recorded so the ceiling treats it as
// a failure while saturated, like a 401.
func RefuseSaturatedLogin(c *gin.Context, accountFailures int, delay func(time.Duration)) (refused bool) {
	d, refuse := SaturationPolicy(accountFailures)
	if refuse {
		c.Set(loginSatRefusedKey, true)
		return true
	}
	delay(d)
	return false
}

// LoginSaturationRefused reports whether RefuseSaturatedLogin refused c.
func LoginSaturationRefused(c *gin.Context) bool {
	v, _ := c.Get(loginSatRefusedKey)
	b, _ := v.(bool)
	return b
}
