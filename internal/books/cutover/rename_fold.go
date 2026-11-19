// file: internal/books/cutover/rename_fold.go
// version: 1.0.0
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
// absent from values keeps what prev says (the same reading UnfoldOIDC gives
// it); prev being empty means sign-in off.
//
// Merging into prev: an entry for a provider that stays keeps its own fields
// (kind, issuer, client_secret reference, anything else) and takes the new
// client_id, allowed_emails and default_role; a provider the values no longer
// name (empty client id, or sign-in off) is removed; a provider that is new
// gets the fold's entry, which carries no client_secret reference (the secret
// is set by name, and the caller adds the reference); an entry whose id HEAD
// does not know (neither github nor google) is carried over untouched, in its
// place. With nothing to fold the result is the empty list.
//
// A name that is not an oauth_* fold name, or a value of the wrong type, is
// ErrConvertInput.
func FoldOIDC(values map[string]json.RawMessage, prev json.RawMessage) (json.RawMessage, error) {
	bad := func(what string) error { return fmt.Errorf("%w: oidc-fold: %s", ErrConvertInput, what) }
	old, err := decodeEntryList(prev, "oidc-fold")
	if err != nil {
		return nil, err
	}
	// Split prev into the entries HEAD can express and the rest.
	headID := func(e map[string]json.RawMessage) string {
		var id string
		_ = json.Unmarshal(e["id"], &id)
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
		if base, err := UnfoldOIDC(mustJSON(head)); err == nil {
			for k, v := range base {
				var x any
				_ = json.Unmarshal(v, &x)
				merged[k] = x
			}
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
	for _, e := range old {
		for _, p := range oidcProviders {
			if headID(e) != p.id {
				continue
			}
			var ref struct {
				Secret string `json:"secret"`
			}
			if json.Unmarshal(e["client_secret"], &ref) == nil && ref.Secret == p.secret {
				pl.imported[secrets.Name(p.secret)] = true
			}
		}
	}
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
		for _, k := range []string{"allowed_emails", "default_role"} {
			if v, has := b[k]; has {
				e[k] = v
			} else {
				delete(e, k)
			}
		}
		out = append(out, e)
	}
	for _, p := range oidcProviders {
		if b, ok := built[p.id]; ok && !placed[p.id] {
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
// of "" removes it; every other entry is carried over untouched except that
// the aliases of the HEAD-derived roots (books-library, import-<n>) are
// replaced by the ones path_aliases gives (an alias list is HEAD's whole list).
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
	if len(aliases) > 0 {
		if err := json.Unmarshal(aliases, &heads); err != nil {
			return nil, fmt.Errorf("%w: library roots: want a list of path aliases", ErrConvertInput)
		}
	}
	idOf := func(e map[string]json.RawMessage) string {
		var id string
		_ = json.Unmarshal(e["id"], &id)
		return id
	}
	pathOf := func(e map[string]json.RawMessage) string {
		var p string
		_ = json.Unmarshal(e["path"], &p)
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
	if roots == nil {
		roots = []map[string]json.RawMessage{}
	}
	return mustJSON(roots), nil
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
