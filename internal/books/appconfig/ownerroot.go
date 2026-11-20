// file: internal/books/appconfig/ownerroot.go
// version: 1.0.0
// guid: 2e7b4c19-8d36-4f50-a1c7-5b9d3e0f6a82
// last-edited: 2026-11-19

package appconfig

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	coreconfig "github.com/falkcorp/mediacore/config"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// ownerRootRefusal is HEAD's first owner trust-root rule, kept exactly: while no
// owner is configured, nothing may change who the owner is or how the owner signs
// in (owner_email, cf_access_team_domain, cf_access_aud, enable_auth,
// oauth_allowed_emails) through the API, whoever asks, admin or not, auth on or
// off. They can be set only on the server (the environment or the config file), so
// the first owner is named by the host's operator and never by a web session.
// HEAD ran this check before every other (UpdateService.checkProtected), compared
// on the decoded values so a save that sends a value back unchanged is not a
// change.
//
// Core's ProtectOwner key needs the owner's proof only once an owner is set and
// is checked as an admin key until then (05-config 1.3.7), so on its own it would
// let any admin session set the first owner and switch sign-in off; this closes
// that. Once an owner is set core's rule applies as before: the update needs the
// owner's proof (Actor.Owner), which the handler grants for a verified Cloudflare
// Access sign-in as the owner only.
//
// Only the API write is checked: Mutate, the process's own writes, is not a
// caller HEAD's rule ever applied to.
func (e *Engine) ownerRootRefusal(before config.Config, u config.Update) error {
	if strings.TrimSpace(before.OwnerEmail) != "" {
		return nil
	}
	candidate := before.Clone()
	apply := func(head string, raw json.RawMessage, clear bool) {
		p, ok := e.byHead[head]
		if !ok || p.kind == kindAI {
			return
		}
		f, ok := fieldAt(candidate, p.segs)
		if !ok || !f.CanSet() {
			return
		}
		if clear {
			f.SetZero()
			return
		}
		// A value that does not fit the field is the field check's refusal, not
		// this rule's; whatever decoded stays in the candidate.
		_ = json.Unmarshal(raw, f.Addr().Interface())
	}
	for _, head := range slices.Sorted(maps.Keys(u.Set)) {
		apply(head, u.Set[head], false)
	}
	for _, head := range u.Unset {
		apply(head, nil, true)
	}
	changed := config.ChangedOwnerTrustRoot(&before, candidate)
	if len(changed) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s: %s", coreconfig.ErrForbidden, strings.Join(changed, ", "), config.OwnerTrustRootUnsetMessage)
}
