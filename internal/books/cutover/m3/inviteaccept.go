// file: internal/books/cutover/m3/inviteaccept.go
// version: 0.1.0
// guid: 7f3a92d6-0b4e-4c18-a5d7-1e68b2c04f93
// last-edited: 2026-11-22

package m3

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// prefixInviteAccept is books' own record of an accepted invite
// (store.inviteAcceptPrefix): acceptedinvite:<invite id> -> inviteAcceptRow. It is
// not part of M3's Source and grants nothing; it is what the Users page's
// accepted-invites list shows, kept because under core identity an invite is
// core's and is gone once accepted. M3 writes it for the pre-flip accepts HEAD's
// own list was still showing; every accept after the flip is written by books'
// lifecycle hook from core's InviteAccepted event.
const prefixInviteAccept = "acceptedinvite:"

// AcceptedInviteWindow is how far back HEAD's list showed accepted invites
// (handlers.acceptedInviteWindow): M3 carries the accepts that list would still
// show on the day of the flip, and no older one, because an older one is not
// listed by anything. TestAcceptedInviteWindow_IsHEADs pins the two together.
const AcceptedInviteWindow = 30 * 24 * time.Hour

// headInvite is store.Invite as the R0 binary stores it, the members the carry
// reads. The value's own "token" member is deliberately not here: the carry has no
// use for the bearer secret and must not be able to copy it.
type headInvite struct {
	Username          string     `json:"username"`
	Email             string     `json:"email,omitempty"`
	UsedAt            *time.Time `json:"used_at,omitempty"`
	AcceptedUserID    string     `json:"accepted_user_id,omitempty"`
	DisplacedUserIDs  []string   `json:"displaced_user_ids,omitempty"`
	EmailBinding      string     `json:"email_binding,omitempty"`
	SSOGrant          string     `json:"sso_grant,omitempty"`
	GuestPolicyStatus string     `json:"guest_policy_status,omitempty"`
	GuestPolicyDetail string     `json:"guest_policy_detail,omitempty"`
	GuestPolicyAt     *time.Time `json:"guest_policy_at,omitempty"`
}

// inviteAcceptRow is store.InviteAccept as the identity books build reads it,
// frozen here (this package cannot import the store).
// TestInviteAcceptCarry_RowReadsBackThroughTheStore pins the two together.
type inviteAcceptRow struct {
	InviteID          string     `json:"invite_id"`
	UserID            string     `json:"user_id"`
	Username          string     `json:"username"`
	Email             string     `json:"email,omitempty"`
	AcceptedAt        time.Time  `json:"accepted_at"`
	EmailBinding      string     `json:"email_binding,omitempty"`
	SSOGrant          string     `json:"sso_grant,omitempty"`
	DisplacedUserIDs  []string   `json:"displaced_user_ids,omitempty"`
	GuestPolicyStatus string     `json:"guest_policy_status,omitempty"`
	GuestPolicyDetail string     `json:"guest_policy_detail,omitempty"`
	GuestPolicyAt     *time.Time `json:"guest_policy_at,omitempty"`
}

// inviteIDDomain is store.inviteIDDomain: it separates an invite's public ID from
// every other hash of its token.
const inviteIDDomain = "audiobook-organizer/invite-id/v1\x00"

// headInviteID is store.InviteID: the non-secret handle HEAD's admin routes named
// an invite by, the first 128 bits of a domain-separated SHA-256 of its token. It
// cannot be turned back into the token, which is why it, never the token, keys the
// carried row. TestInviteAcceptCarry_KeyIsStoreInviteID pins it to the store's.
func headInviteID(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(inviteIDDomain + token))
	return hex.EncodeToString(sum[:16])
}

// carryInviteAccept stages books' acceptedinvite: row for the HEAD invite row
// (key, value) when that invite was used inside the window, in the chunk that
// deletes the invite. HEAD's own values are carried as HEAD recorded them, among
// them the guest-policy sync's status, detail and time (a carried accept has no
// outcome row of its own to read them from); nothing is derived and nothing is
// invented.
//
// An invite that was never used, one used before the window, and one that does not
// decode get no row, and the invite row is deleted as ever (the step deletes by
// rule; a row that does not decode is not quarantined, because a quarantined copy
// would keep the raw token the migration exists to drop). The one that does not
// decode is counted and named in the log, without its key (the key is the token).
//
// The write is a plain Set of a key nothing else writes in this step: the key is the
// invite's own ID, one row per invite, so a re-run of the chunk after a crash
// rewrites the same bytes. The window is judged on the cutover's clock (Config.Now),
// and an invite used exactly at the window's edge is carried, as HEAD's list shows
// it (it skipped a row only when UsedAt was before the edge).
func (m *Migration) carryInviteAccept(w *datamigrate.StepWriter, key string, value []byte) error {
	token := idOf(key, prefixInvite)
	if token == "" {
		return nil
	}
	var h headInvite
	if err := decodeJSON(value, &h); err != nil {
		w.Count(OutcomeReported)
		m.log.Warn("m3: an invite row does not decode; it is deleted and no accepted-invite row is carried for it", "step", m.cfg.StepID, "reason", err.Error())
		return nil
	}
	if h.UsedAt == nil || h.UsedAt.Before(m.cfg.Now().Add(-AcceptedInviteWindow)) {
		return nil
	}
	row, err := json.Marshal(inviteAcceptRow{
		InviteID: headInviteID(token), UserID: h.AcceptedUserID, Username: h.Username, Email: h.Email,
		AcceptedAt: *h.UsedAt, EmailBinding: h.EmailBinding, SSOGrant: h.SSOGrant, DisplacedUserIDs: h.DisplacedUserIDs,
		GuestPolicyStatus: h.GuestPolicyStatus, GuestPolicyDetail: h.GuestPolicyDetail, GuestPolicyAt: h.GuestPolicyAt,
	})
	if err != nil {
		return err
	}
	if err := w.Set([]byte(prefixInviteAccept+headInviteID(token)), row); err != nil {
		return err
	}
	w.Count(OutcomeInviteAcceptCarried)
	return nil
}
