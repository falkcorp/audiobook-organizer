// file: internal/books/cutover/rename_fold.go
// version: 1.0.1
// guid: 3e8c5a17-9b24-4d6f-8f1a-0c7d2b95e6a3
// last-edited: 2026-11-19

package cutover

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	"github.com/falkcorp/mediacore/secrets"
)

// The forward direction of UnfoldOIDC, for the layer that writes HEAD's flat
// values through to the canonical list (F3-07 e2). It takes the existing
// canonical list and returns the new one: what the flat values determine is
// rewritten, and everything they do not (a provider's extra fields, entries
// HEAD has no name for) is carried over from prev. A pure function.
//
// root_dir and path_aliases have no forward direction here: they are class i,
// left in the settings blob by M2 and moved into core.library.roots only by M6
// at F7, so until then the projection reads and writes them in the blob
// (05-config 1.12.1). M6 owns the roots list's build (m6/roots.go).

// ---- the OIDC fold ----

// oidcFoldNames are the HEAD names the fold reads (planner.foldValue).
var oidcFoldNames = []string{oauthEnabled, "oauth_github_client_id", "oauth_google_client_id", oauthEmails, oauthDefaultRole}

// FoldOIDC builds the new core.auth.oidc.providers value from the HEAD oauth_*
// values (decoded JSON, keyed by HEAD name) and the existing list prev.
//
// The entries are built by the planner's own fold (planner.foldValue and
// fold), so M2 and this cannot disagree about what an entry holds. A name
// absent from values means "keep": it takes what prev says (the reading
// UnfoldOIDC gives prev), and prev being empty means sign-in off. The
// allow-list and the default role are access policy, so an existing entry's
// allowed_emails and default_role are never touched unless values names
// oauth_allowed_emails or oauth_default_role: a write of one client id keeps
// both.
//
// Merging into prev: an entry for a provider that stays keeps its own fields
// (kind, issuer, client_secret reference, anything else) and takes the new
// client_id; a provider the values no longer name (empty client id, or sign-in
// off) is removed; a provider that is new gets the fold's entry, which carries
// no client_secret reference (the secret is set by name, and the caller adds
// the reference) and takes the allow-list and role from the values, else from
// prev's other entries; an entry whose id HEAD does not know (neither github
// nor google) is carried over untouched, in its place. With nothing to fold
// the result is the empty list.
//
// An explicitly empty oauth_allowed_emails keeps HEAD's meaning, which is
// "no OAuth logins allowed" (config.OAuthAllowedEmails), and the canonical
// resolver admits nobody from an empty list either (core/auth/oidc admitted):
// it clears an existing allow-list to "allowed_emails": [] (written out, not
// dropped) and writes [] on a new entry. An entry that never had the key
// already admits nobody, so it is left as it is. Neither fails open.
//
// A name that is not an oauth_* fold name, or a value of the wrong type, is
// ErrConvertInput. A prev that is not a list, or whose HEAD providers
// UnfoldOIDC refuses (they disagree on the allow-list or role), is an error
// returned as it is: the flat names cannot say how to merge into it, and
// guessing would change who may sign in.
func FoldOIDC(values map[string]json.RawMessage, prev json.RawMessage) (json.RawMessage, error) {
	bad := func(what string) error { return fmt.Errorf("%w: oidc-fold: %s", ErrConvertInput, what) }
	old, err := decodeEntryList(prev, "oidc-fold")
	if err != nil {
		return nil, err
	}
	// Split prev into the entries HEAD can express and the rest.
	for _, e := range old {
		var v string
		if raw, has := e["id"]; has && json.Unmarshal(raw, &v) != nil {
			return nil, bad("a provider's id is not a string")
		}
	}
	headID := func(e map[string]json.RawMessage) string {
		var id string
		_ = json.Unmarshal(e["id"], &id) // validated above
		for _, p := range oidcProviders {
			if p.id == id {
				return id
			}
		}
		return ""
	}
	var head []json.RawMessage
	for _, e := range old {
		if headID(e) != "" {
			head = append(head, mustJSON(e))
		}
	}
	// What prev says, then what the values override.
	merged := map[string]any{}
	if len(head) > 0 {
		base, err := UnfoldOIDC(mustJSON(head))
		if err != nil {
			return nil, fmt.Errorf("the existing providers: %w", err)
		}
		for k, v := range base {
			var x any
			if err := json.Unmarshal(v, &x); err != nil {
				return nil, bad("an existing provider does not unfold to JSON")
			}
			merged[k] = x
		}
	}
	for k, v := range values {
		if !slices.Contains(oidcFoldNames, k) {
			return nil, bad("not an oauth_* fold name")
		}
		var x any
		if err := json.Unmarshal(v, &x); err != nil {
			return nil, bad("a value is not JSON")
		}
		switch x.(type) {
		case bool:
			if k != oauthEnabled {
				return nil, bad("want a string")
			}
		case string:
			if k == oauthEnabled {
				return nil, bad("want a boolean")
			}
		default:
			return nil, bad("want a boolean or a string")
		}
		merged[k] = x
	}

	// The planner's own fold.
	pl := &planner{p: &plan{Counts: map[string]int{}}, imported: map[secrets.Name]bool{}}
	names := make([]string, 0, len(merged))
	for k := range merged {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		pl.foldValue(k, merged[k])
	}
	pl.fold()
	built := map[string]map[string]json.RawMessage{}
	for _, s := range pl.p.Stages {
		if s.Path != "core.auth.oidc.providers" {
			continue
		}
		list, err := decodeEntryList(s.Value, "oidc-fold")
		if err != nil {
			return nil, err
		}
		for _, e := range list {
			built[headID(e)] = e
		}
	}

	// Assemble: prev's order, the new providers after it.
	out := []map[string]json.RawMessage{}
	placed := map[string]bool{}
	for _, e := range old {
		id := headID(e)
		if id == "" {
			out = append(out, e)
			continue
		}
		b, stays := built[id]
		if !stays {
			continue
		}
		placed[id] = true
		e["client_id"] = b["client_id"]
		// Access policy changes only when the caller named it.
		if _, named := values[oauthEmails]; named {
			if v, has := b["allowed_emails"]; has {
				e["allowed_emails"] = v
			} else if _, had := e["allowed_emails"]; had {
				// Clearing: spell the empty list out rather than drop the key.
				e["allowed_emails"] = json.RawMessage(`[]`)
			}
		}
		if _, named := values[oauthDefaultRole]; named {
			if v, has := b["default_role"]; has {
				e["default_role"] = v
			} else {
				delete(e, "default_role")
			}
		}
		out = append(out, e)
	}
	for _, p := range oidcProviders {
		if b, ok := built[p.id]; ok && !placed[p.id] {
			if _, named := values[oauthEmails]; named {
				if _, has := b["allowed_emails"]; !has {
					b["allowed_emails"] = json.RawMessage(`[]`)
				}
			}
			out = append(out, b)
		}
	}
	return mustJSON(out), nil
}

// decodeEntryList reads a JSON list of objects; null, empty and absent are the
// empty list.
func decodeEntryList(in json.RawMessage, what string) ([]map[string]json.RawMessage, error) {
	if len(in) == 0 {
		return nil, nil
	}
	var out []map[string]json.RawMessage
	if err := json.Unmarshal(in, &out); err != nil {
		return nil, fmt.Errorf("%w: %s: want a list of objects", ErrConvertInput, what)
	}
	for _, e := range out {
		if e == nil {
			return nil, fmt.Errorf("%w: %s: want a list of objects", ErrConvertInput, what)
		}
	}
	return out, nil
}

// ---- the library roots ----
