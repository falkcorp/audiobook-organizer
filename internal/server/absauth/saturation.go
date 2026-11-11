// file: internal/server/absauth/saturation.go
// version: 1.5.0
// guid: f4881432-1772-4d8d-a653-33fde6815d7a
// last-edited: 2026-11-10

package absauth

import (
	"time"

	coreauth "github.com/falkcorp/mediacore/auth"

	"github.com/gin-gonic/gin"
)

// Saturation: the aggregate login ceilings (coreauth.LoginCeiling, behind the
// server's login gate) are spent.
//
// A ceiling that refused every login once spent would let an anonymous flood
// lock out every user, the owner included. Instead the gate marks the request
// saturated (MarkLoginSaturated) and the login handlers, which are the ones
// that know the account, apply coreauth.SaturationPolicy before any password
// work: an account with few failures of its own is delayed, never refused, and
// only an account that has already failed coreauth.DefaultSoftThreshold times
// in the window is refused (429). The per-visitor limit refuses regardless, as
// it always did. The policy and its delays moved to core auth at F4-02; these
// gin request marks stay.

// RetryAfterSeconds is the Retry-After value (seconds) every handler-level login
// 429 carries (the saturated-admission refusals made in the ceiling middleware
// compute their own wait): the failure windows the budgets refill over are
// measured in minutes, so a shorter hint would only invite a client to hammer a
// refusal.
const RetryAfterSeconds = "60"

// SetRetryAfter adds the Retry-After header every handler-level 429 sends, so
// a handler's own refusals tell a well-behaved client how long to back off just
// as the ceiling middleware's do.
func SetRetryAfter(c *gin.Context) { c.Header("Retry-After", RetryAfterSeconds) }

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

// NoteLoginAccount records on c the account key (coreauth.AccountKey of the submitted
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

// RefuseSaturatedLogin applies coreauth.SaturationPolicy for a saturated login on account
// key accountKey: it applies the delay through delay when there is one and returns
// false, or, when the account has already failed coreauth.DefaultSoftThreshold times, records the
// refusal on c and returns true so the caller answers 429. The policy is decided
// by the SUBMITTED name, so a name that does not exist is delayed and refused
// exactly like one that does. The refusal is recorded so the ceiling treats it as
// a failure while saturated, like a 401.
func RefuseSaturatedLogin(c *gin.Context, accountFailures int, delay func(time.Duration)) (refused bool) {
	d, refuse := coreauth.SaturationPolicy(accountFailures)
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
