// file: internal/books/appconfig/ownerroot.go
// version: 1.0.0
// guid: 2e7b4c19-8d36-4f50-a1c7-5b9d3e0f6a82
// last-edited: 2026-11-19

package appconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	coreconfig "github.com/falkcorp/mediacore/config"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// ownerOnlyMessage is the refusal for an owner trust-root change by a caller
// that is not the owner, once an owner is set.
const ownerOnlyMessage = "Only the owner may change who the owner is or how the owner signs in"

// ownerRootRefusal is HEAD's owner trust-root rule (UpdateService.checkProtected,
// first of every check): the five settings that decide who the owner is or how
// the owner signs in (owner_email, cf_access_team_domain, cf_access_aud,
// enable_auth, oauth_allowed_emails) change through the API only as follows.
//
//   - No owner is configured: never, whoever asks, admin or not, auth on or off.
//     They are set on the server (the environment or the config file), so the
//     first owner is named by the host's operator and never by a web session.
//   - An owner is set: only the owner (Actor.Owner, which the settings handler
//     grants from auth.OwnerProofWhyNot for a verified Cloudflare Access sign-in
//     as that owner). A second admin, a password or SSO session and an API key
//     are refused.
//
// Core's ProtectOwner tag does not carry this on its own: it is an admin key
// until an owner exists, and core.auth.mode and the OIDC allow-list fold are not
// tagged at all, so each of them would skip the owner's proof.
//
// "Changed" is decided on the candidate the write path would build, and a value
// the write path would not take as given counts as a change:
//
//   - Each value is decoded into a ZERO value of the field's type and assigned,
//     as the write path does. Decoding into the existing value instead leaves it
//     in place on a JSON null or a failed decode, so a null looked like "no
//     change" while the write path read it as a different value (enable_auth null
//     converted to "disabled").
//   - A JSON null, or a value that does not decode, on one of the five is a
//     change and is refused here, not left to the field check that runs later.
//   - An unset of enable_auth is a change: it restores core's default, "required",
//     not the Go zero value the candidate would hold. An unset of the other four
//     restores the empty value.
//
// Only the API write is checked: Mutate, the process's own writes, is not a
// caller HEAD's rule ever applied to.
func (e *Engine) ownerRootRefusal(a config.Actor, before config.Config, u config.Update) error {
	candidate := before.Clone()
	var undecidable []string
	touch := func(head string, raw json.RawMessage, clear bool) {
		if !config.IsOwnerTrustRootField(head) {
			return
		}
		p, ok := e.byHead[head]
		if !ok {
			return // not projected: refused as an unknown key
		}
		f, ok := fieldAt(candidate, p.segs)
		if !ok || !f.CanSet() {
			undecidable = append(undecidable, head) // cannot be judged: refuse
			return
		}
		if clear {
			if p.target.Convert != "" {
				undecidable = append(undecidable, head)
				return
			}
			f.SetZero()
			return
		}
		v := reflect.New(f.Type())
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, v.Interface()) != nil {
			undecidable = append(undecidable, head)
			return
		}
		f.Set(v.Elem())
	}
	for _, head := range slices.Sorted(maps.Keys(u.Set)) {
		touch(head, u.Set[head], false)
	}
	for _, head := range u.Unset {
		touch(head, nil, true)
	}
	changed := slices.Compact(slices.Sorted(slices.Values(append(config.ChangedOwnerTrustRoot(&before, candidate), undecidable...))))
	if len(changed) == 0 {
		return nil
	}
	list := strings.Join(changed, ", ")
	if strings.TrimSpace(before.OwnerEmail) == "" {
		return fmt.Errorf("%w: %s: %s", coreconfig.ErrForbidden, list, config.OwnerTrustRootUnsetMessage)
	}
	if !a.Owner {
		return fmt.Errorf("%w: %s: %s", coreconfig.ErrForbidden, list, ownerOnlyMessage)
	}
	return nil
}
