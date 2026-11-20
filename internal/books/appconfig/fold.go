// file: internal/books/appconfig/fold.go
// version: 1.0.0
// guid: 0b6d4e82-7a13-4c59-9e0f-3d5a8c1b7f26
// last-edited: 2026-11-19

package appconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
)

// foldInto turns the oauth_* values and the root_dir/path_aliases values of an
// Update into the one write each list takes: the new OIDC provider list
// (cutover.FoldOIDC) and the new library roots (cutover.FoldRoots), merged into
// the lists as they stand. A list is rewritten only when a value names one of its
// fields or a provider's client secret is being set (so its entry can reference
// it). byPath and also name, for the response and for errors, the HEAD fields the
// list stands for.
//
// A value that cannot be merged (the existing providers disagree on the
// allow-list or role, a root that is not an absolute path) is a field error naming
// the first field of the group, never a partial write.
func (e *Engine) foldInto(ctx context.Context, patch *coreconfig.Patch, byPath map[coreconfig.Path]string, also map[coreconfig.Path][]string,
	foldVals, rootsVals map[string]json.RawMessage, secretSets []secretOp) error {
	if len(rootsVals) > 0 {
		heads := slices.Sorted(maps.Keys(rootsVals))
		st, ok := e.acc.Lookup(libraryRootsKey)
		if !ok {
			return &coreconfig.FieldError{Path: coreconfig.Path(heads[0]), Err: errors.New("the library roots key is not registered")}
		}
		rootDir := e.Snapshot().RootDir
		if v, has := rootsVals["root_dir"]; has {
			if err := json.Unmarshal(v, &rootDir); err != nil {
				return &coreconfig.FieldError{Path: "root_dir", Err: err}
			}
		}
		out, err := cutover.FoldRoots(rootDir, rootsVals["path_aliases"], listOrEmpty(st.Effective))
		if err != nil {
			return &coreconfig.FieldError{Path: coreconfig.Path(heads[0]), Err: err}
		}
		e.claim(patch, byPath, also, libraryRootsKey, heads, out)
	}
	oidcSecretSet := map[string]bool{}
	for _, op := range secretSets {
		for id, name := range e.oidcSecret {
			if string(op.name) == name {
				oidcSecretSet[id] = true
			}
		}
	}
	for id, name := range e.oidcSecret {
		if _, in := patch.Set[coreconfig.Path(name)]; in {
			oidcSecretSet[id] = true // a registered secret key written by Apply itself
		}
	}
	if len(foldVals) == 0 && len(oidcSecretSet) == 0 {
		return nil
	}
	heads := slices.Sorted(maps.Keys(foldVals))
	if len(heads) == 0 {
		heads = []string{"oauth_" + slices.Sorted(maps.Keys(oidcSecretSet))[0] + "_client_secret"}
	}
	st, ok := e.acc.Lookup(oidcProvidersKey)
	if !ok {
		return &coreconfig.FieldError{Path: coreconfig.Path(heads[0]), Err: errors.New("the OIDC providers key is not registered")}
	}
	list := listOrEmpty(st.Effective)
	if len(foldVals) > 0 {
		var err error
		if list, err = cutover.FoldOIDC(foldVals, list); err != nil {
			return &coreconfig.FieldError{Path: coreconfig.Path(heads[0]), Err: err}
		}
	}
	list, err := e.withSecretRefs(ctx, list, oidcSecretSet)
	if err != nil {
		return &coreconfig.FieldError{Path: coreconfig.Path(heads[0]), Err: err}
	}
	e.claim(patch, byPath, also, oidcProvidersKey, heads, list)
	return nil
}

// claim adds one list write to the patch and names its HEAD fields.
func (e *Engine) claim(patch *coreconfig.Patch, byPath map[coreconfig.Path]string, also map[coreconfig.Path][]string,
	key string, heads []string, value json.RawMessage) {
	p := coreconfig.Path(key)
	patch.Set[p] = value
	byPath[p] = heads[0]
	also[p] = heads[1:]
}

func listOrEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`[]`)
	}
	return raw
}

// withSecretRefs gives every HEAD provider entry that has none the reference to
// its client secret, when that secret is set (or is being set by this Update):
// the fold builds an entry without one, because the secret is stored by name.
// An entry that already has a reference keeps it.
func (e *Engine) withSecretRefs(ctx context.Context, list json.RawMessage, setting map[string]bool) (json.RawMessage, error) {
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(list, &entries); err != nil {
		return nil, fmt.Errorf("the provider list is not a list of objects: %w", err)
	}
	changed := false
	for _, ent := range entries {
		var id string
		_ = json.Unmarshal(ent["id"], &id)
		name, known := e.oidcSecret[id]
		if !known {
			continue
		}
		if _, has := ent["client_secret"]; has {
			continue
		}
		set := setting[id]
		if !set {
			_, meta, err := e.acc.Secret(ctx, secrets.Name(name))
			set = err == nil && meta.Set
		}
		if set {
			ent["client_secret"] = mustMarshal(map[string]string{"secret": name})
			changed = true
		}
	}
	if !changed {
		return list, nil
	}
	return json.Marshal(entries)
}
