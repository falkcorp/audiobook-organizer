// file: core/internal/confighttp/types.go
// version: 1.0.0
// guid: 13d07bdf-ad17-4938-8662-60b0bdcf535e
// last-edited: 2026-11-10

package confighttp

import (
	"encoding/json"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
)

// SettingsRequest is PATCH /settings' body (05-config §1.3.4): the keys to
// store and the keys to return to their default. A secret key's value is a
// JSON string, and listing it in Unset deletes it. Nothing about the request
// is ever echoed back.
type SettingsRequest struct {
	Set   map[string]json.RawMessage `json:"set,omitempty"`
	Unset []string                   `json:"unset,omitempty"`
}

// SettingsResponse is PATCH /settings' 200 body. RestartPending names the
// applied keys whose stored value is not yet effective; PostCommitError lists
// the sections whose after-commit hook failed, with the change still
// committed. Neither carries a value.
type SettingsResponse struct {
	Generation      uint64              `json:"generation"`
	Applied         []string            `json:"applied"`
	RestartPending  []string            `json:"restart_pending,omitempty"`
	PostCommitError []PostCommitFailure `json:"post_commit_error,omitempty"`
}

// PostCommitFailure is one section whose after-commit hook failed. The
// hook's own text is withheld: it is code outside this package and may quote
// anything.
type PostCommitFailure struct {
	Section string `json:"section"`
	Message string `json:"message"`
}

// SettingsView is GET /settings' body: {path: key state} for the keys of the
// sections the caller may read.
type SettingsView map[string]KeyStateView

// KeyStateView is one key as the settings UI reads it. It is not
// config.KeyState: that type has no JSON shape, carries an error value whose
// text can quote a stored value, and its secret meta carries a fingerprint
// and a key ID that this surface never returns.
type KeyStateView struct {
	Path string `json:"path"`
	// Section is the section the key belongs to.
	Section string `json:"section"`
	// Schema is the key's own sub-schema.
	Schema json.RawMessage `json:"schema"`
	// Effective is the value in force. It is absent for a secret key.
	Effective json.RawMessage `json:"effective,omitempty"`
	// Source is the layer the effective value comes from: default, file, env,
	// flag, boot_record or override.
	Source string `json:"source"`
	// LockedBy names the operator layer that supplies the key ("env
	// ABO_CORE_LOG_LEVEL"), empty when the key is free.
	LockedBy string `json:"locked_by,omitempty"`
	// Reload is live, restart or boot.
	Reload string `json:"reload"`
	// Protection is none, interactive, admin, owner or operator.
	Protection string `json:"protection"`
	FailClosed bool   `json:"fail_closed,omitempty"`
	// Pending is true when a restart-required change is stored but not
	// effective.
	Pending bool `json:"pending,omitempty"`
	// Quarantined is true when a stored override was ignored at boot. The
	// reason is not shown: it can quote the stored value.
	Quarantined bool `json:"quarantined,omitempty"`
	// Secret is set for a secret key and is all this surface shows of it.
	Secret *SecretView `json:"secret,omitempty"`
}

// SecretView is a secret key's meta: whether a value is set, where it comes
// from and who wrote it last. There is no value, no mask, no hash and no
// fingerprint: a secret is write-only (05-config C9).
type SecretView struct {
	Set bool `json:"set"`
	// Source is stored, env or file (empty while nothing is set).
	Source    string    `json:"source,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	UpdatedBy string    `json:"updated_by,omitempty"`
	// Unavailable is true when the stored record cannot be read (the key
	// ring is missing or the record is corrupt).
	Unavailable bool `json:"unavailable,omitempty"`
}

// secretView renders m without its fingerprint, key ID or error text.
func secretView(m *secrets.Meta) *SecretView {
	if m == nil {
		return nil
	}
	v := &SecretView{Set: m.Set, UpdatedAt: m.UpdatedAt, UpdatedBy: m.UpdatedBy, Unavailable: m.Err != nil}
	if m.Set {
		v.Source = secretSource(m.Source)
	}
	return v
}

// secretSource names a secret's source as the settings UI spells it; a source
// this package does not know is empty rather than a guess.
func secretSource(s secrets.Source) string {
	switch s {
	case secrets.SourceStored:
		return "stored"
	case secrets.SourceEnv:
		return "env"
	case secrets.SourceFile:
		return "file"
	}
	return ""
}

// keyView renders ks. A secret key never carries an Effective value, whatever
// the state holds.
func keyView(ks config.KeyState) KeyStateView {
	v := KeyStateView{
		Path: string(ks.Path), Section: string(ks.Section), Schema: ks.Schema, Effective: ks.Effective,
		Source: ks.Source.String(), LockedBy: ks.LockedBy, Reload: ks.Reload.String(),
		Protection: ks.Protection.String(), FailClosed: ks.FailClosed, Pending: ks.Pending,
		Quarantined: ks.Quarantined != nil,
	}
	if ks.Secret != nil {
		v.Effective = nil
		v.Secret = secretView(ks.Secret)
	}
	return v
}
