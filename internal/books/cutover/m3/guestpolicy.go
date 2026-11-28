// file: internal/books/cutover/m3/guestpolicy.go
// version: 0.1.0
// guid: c4f1a7d2-93b6-4e58-8a0d-2b7e65f19c03
// last-edited: 2026-11-22

package m3

import (
	"encoding/json"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// prefixGuestPolicy is books' own bookkeeping family (store.guestPolicyPrefix):
// guestpolicy:<normalized e-mail> -> guestPolicyRow. It is not part of M3's Source
// and grants nothing; it says only that the sync added the address to the
// Cloudflare guest policy for this account, so a later disable, delete or merge
// removes it again. The importer's allow-list row is core's and by address
// alone: it cannot hold that fact.
const prefixGuestPolicy = "guestpolicy:"

// guestPolicyRow is store.guestPolicyRow as the identity books build reads it,
// frozen here (this package cannot import the store).
// TestGuestPolicyCarry_RowReadsBackThroughTheStore pins the two together.
type guestPolicyRow struct {
	UserID    string    `json:"user_id"`
	GrantedAt time.Time `json:"granted_at"`
	CFAdded   bool      `json:"cf_added,omitempty"`
}

// carryGuestPolicy stages books' guestpolicy: row for a HEAD grant whose sync
// added the address to the Cloudflare guest policy, in the chunk that imports
// the allow-list row and deletes HEAD's. A grant HEAD recorded without CFAdded
// gets none: it names an address somebody put there by hand, and nothing may
// remove that. The write is a plain Set of a key nothing else writes in this
// step: the grant's key is the address, one grant per address, so a re-run of
// the chunk after a crash rewrites the same bytes.
func (m *Migration) carryGuestPolicy(w *datamigrate.StepWriter, email string, g *headInviteGrant) error {
	email = auth.NormalizeEmail(email)
	if !g.CFAdded || g.UserID == "" || email == "" {
		return nil
	}
	row, err := json.Marshal(guestPolicyRow{UserID: g.UserID, GrantedAt: g.GrantedAt, CFAdded: true})
	if err != nil {
		return err
	}
	if err := w.Set([]byte(prefixGuestPolicy+email), row); err != nil {
		return err
	}
	w.Count(OutcomeGuestPolicyCarried)
	return nil
}
