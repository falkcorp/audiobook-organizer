// file: internal/books/cutover/rename_fold.go
// version: 1.0.1
// guid: 3e8c5a17-9b24-4d6f-8f1a-0c7d2b95e6a3
// last-edited: 2026-11-19

package cutover

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/secrets"
)

// The forward direction of UnfoldOIDC and UnfoldRoots, for the layer that
// writes HEAD's flat values through to the canonical lists (F3-07 e2). Both
// take the existing canonical list and return the new one: what the flat
// values determine is rewritten, and everything they do not (a provider's
// extra fields, a root's operator-tuned fields, entries HEAD has no name for)
// is carried over from prev. Pure functions.

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

// FoldRoots builds the new core.library.roots value from HEAD's root_dir, its
// path_aliases (a JSON list of {root, windows, unc, smb_url}) and the existing
// list prev.
//
// This mirrors M6's roots pass for root_dir and path_aliases
// (internal/books/cutover/m6/roots.go, PR #4755 at 51d3ed94e): root_dir
// becomes the books-library entry (role library, owner books, watch off,
// sentinel bootstrap, min_free 0, quarantine .failed, the default app dir);
// each alias is attached to the root whose cleaned path equals its cleaned
// root, in alias order.
//
// TODO(F7-08): once #4755 and this merge, M6 must call FoldRoots so there is
// one copy.
//
// Merging into prev: an existing books-library entry keeps every field but
// path and aliases (an operator may have tuned min_free or watch); a root_dir
// of "" is HEAD's "no root" and removes it (a partial write passes the current
// root_dir, which UnfoldRoots returns); every other entry is carried over
// untouched. An aliases of nil or empty (no JSON at all) means "keep": every
// entry's aliases stay as prev has them. Any JSON list, null and [] included,
// is HEAD's whole path_aliases: the aliases of the HEAD-derived roots
// (books-library, import-<n>) are replaced by exactly those it names.
// A new books-library entry goes first, as in M6's plan order.
//
// What M6 quarantines this refuses with ErrNotRepresentable, since a write
// has no quarantine: a root_dir that is not an absolute path or holds a NUL, a
// root_dir at the path of another root, and an alias whose root is empty or is
// no HEAD-derived root's path.
func FoldRoots(rootDir string, aliases json.RawMessage, prev json.RawMessage) (json.RawMessage, error) {
	unrep := func(what string) error { return fmt.Errorf("%w: library roots: %s", ErrNotRepresentable, what) }
	old, err := decodeEntryList(prev, "library roots")
	if err != nil {
		return nil, err
	}
	var heads []headPathAlias
	if len(aliases) > 0 { // none at all: keep prev's aliases
		if err := json.Unmarshal(aliases, &heads); err != nil {
			return nil, fmt.Errorf("%w: library roots: want a list of path aliases", ErrConvertInput)
		}
	}
	// An id or a path that is present and not a string is a corrupt prev: refuse
	// it rather than read it as "" and treat the root as foreign.
	for _, e := range old {
		for _, k := range []string{"id", "path"} {
			var v string
			if raw, has := e[k]; has && json.Unmarshal(raw, &v) != nil {
				return nil, fmt.Errorf("%w: library roots: a root's %s is not a string", ErrConvertInput, k)
			}
		}
	}
	idOf := func(e map[string]json.RawMessage) string {
		var id string
		_ = json.Unmarshal(e["id"], &id) // validated above
		return id
	}
	pathOf := func(e map[string]json.RawMessage) string {
		var p string
		_ = json.Unmarshal(e["path"], &p) // validated above
		return p
	}

	books := -1
	for i, e := range old {
		if idOf(e) == booksLibraryRootID {
			if books >= 0 {
				return nil, fmt.Errorf("%w: library roots: the books-library root is listed twice", ErrConvertInput)
			}
			books = i
		}
	}
	roots := append([]map[string]json.RawMessage(nil), old...)
	switch {
	case strings.TrimSpace(rootDir) == "":
		if books >= 0 {
			roots = append(roots[:books], roots[books+1:]...)
		}
	case strings.ContainsRune(rootDir, 0) || !filepath.IsAbs(rootDir):
		return nil, unrep("root_dir is not an absolute path")
	default:
		for i, e := range roots {
			if i != books && filepath.Clean(pathOf(e)) == filepath.Clean(rootDir) {
				return nil, unrep("root_dir is the path of another root")
			}
		}
		if books >= 0 {
			roots[books]["path"] = mustJSON(rootDir)
		} else {
			fresh, err := booksLibraryEntry(rootDir)
			if err != nil {
				return nil, err
			}
			roots = append([]map[string]json.RawMessage{fresh}, roots...)
		}
	}

	if len(aliases) == 0 {
		return mustJSON(nonNilRoots(roots)), nil
	}
	for _, e := range roots {
		if headDerivedRoot(idOf(e)) {
			delete(e, "aliases")
		}
	}
	attached := map[int][]library.Alias{}
	for _, a := range heads {
		target := -1
		for i, e := range roots {
			if a.Root != "" && headDerivedRoot(idOf(e)) && filepath.Clean(pathOf(e)) == filepath.Clean(a.Root) {
				target = i
				break
			}
		}
		if target < 0 {
			return nil, unrep("an alias names no root")
		}
		attached[target] = append(attached[target], library.Alias{Windows: a.Windows, UNC: a.UNC, SMBURL: a.SMBURL})
	}
	for i, list := range attached {
		roots[i]["aliases"] = mustJSON(list)
	}
	return mustJSON(nonNilRoots(roots)), nil
}

// nonNilRoots makes the empty list encode as [] and not null.
func nonNilRoots(roots []map[string]json.RawMessage) []map[string]json.RawMessage {
	if roots == nil {
		return []map[string]json.RawMessage{}
	}
	return roots
}

// booksLibraryEntry is the entry M6 stages for root_dir, as the JSON object core
// stores.
func booksLibraryEntry(rootDir string) (map[string]json.RawMessage, error) {
	spec := library.RootSpec{
		ID: booksLibraryRootID, Path: rootDir, Role: library.RoleLibrary, Owner: "books",
		Watch: library.WatchOff, Sentinel: library.SentinelBootstrap, MinFree: 0,
		Quarantine: ".failed", AppDir: library.DefaultAppDir,
	}
	b, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("%w: library roots: the books-library entry does not encode", ErrConvertInput)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("%w: library roots: the books-library entry does not decode", ErrConvertInput)
	}
	return out, nil
}
