// file: internal/books/cutover/rename_core_keys_test.go
// version: 1.0.0
// guid: 3a9d05c7-e218-4b6f-9d40-5f81c7a2b6e3
// last-edited: 2026-11-18

package cutover

import (
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/kernel"
)

// dynamicCoreRows are the rename-table targets whose canonical path is not
// a registered key or a container with keys below it: the members of a map
// key (core.tasks.windows.<name>.{enabled,start,end}), matched by prefix
// against the registry. Each entry is a reviewed prefix, and the test
// requires a registered key at or under it, so a typo here, or a section
// dropped from core, fails. An entry no row needs is dead and does not
// belong (core.updater.* is an ordinary container, found by keys below it).
var dynamicCoreRows = []config.Path{
	"core.tasks.windows",
}

// coreRegistered reports whether canonical path p is served by core's
// registry: itself a key, a container with keys below it, or a reviewed
// dynamic row with keys at or under its prefix.
func coreRegistered(keys map[config.Path]config.KeyState, p config.Path) bool {
	if _, ok := keys[p]; ok {
		return true
	}
	under := func(prefix config.Path) bool {
		for k := range keys {
			if k == prefix || strings.HasPrefix(string(k), string(prefix)+".") {
				return true
			}
		}
		return false
	}
	if under(p) {
		return true
	}
	if _, ok := keys["core.auth.oidc.providers"]; ok && oidcSecretName(p) {
		return true
	}
	for _, d := range dynamicCoreRows {
		if (p == d || strings.HasPrefix(string(p), string(d)+".")) && under(d) {
			return true
		}
	}
	return false
}

// oidcSecretName matches core.auth.oidc.<id>.client_secret, the secret of one
// provider entry of the list key core.auth.oidc.providers (secret:"bind"):
// the name exists per entry at runtime, so only its list key is registered.
func oidcSecretName(p config.Path) bool {
	rest, ok := strings.CutPrefix(string(p), "core.auth.oidc.")
	id, leaf, cut := strings.Cut(rest, ".")
	return ok && cut && id != "" && !strings.Contains(leaf, ".") && leaf == "client_secret"
}

func coreScopedPath(p config.Path) bool { return strings.HasPrefix(string(p), "core.") }

// coreKeys returns core's registry; the books half of "every key registered"
// is F3-07's books-sections slice.
func coreKeys(t *testing.T) map[config.Path]config.KeyState {
	t.Helper()
	keys, err := kernel.CoreKeys()
	if err != nil {
		t.Fatalf("kernel.CoreKeys: %v", err)
	}
	if len(keys) == 0 {
		t.Fatal("kernel.CoreKeys returned no keys")
	}
	return keys
}

// TestRenameTable_CoreKeysRegistered (the core half of F3-07's
// TestRenameTable_EveryKeyRegistered; 05-config §1.12.3, IMPL-1): every core
// canonical path a key, secret, entry or fold target names is registered in
// core's own registry, so M2 never stages into a path core does not know.
func TestRenameTable_CoreKeysRegistered(t *testing.T) {
	keys := coreKeys(t)
	checked := 0
	var missing []string
	for _, r := range Table().Rows {
		for _, tg := range r.Targets {
			p := tg.ConfigPath()
			if p == "" || !coreScopedPath(p) {
				continue
			}
			checked++
			if !coreRegistered(keys, p) {
				missing = append(missing, string(p)+" (row "+r.Head+")")
			}
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("core path %s is a rename-table target but no core section declares it", m)
	}
	if checked == 0 {
		t.Fatal("the rename table names no core path: the test checked nothing")
	}
}

// TestRenameTable_CoreCoinedRowsDeclared pins the two rows 05-config §1.12.3
// coins and no owner table declared (seams.d/F2-09.md "Not declared"): the
// declaration, its class and its type, so dropping a section fails here and
// in the registry test above.
func TestRenameTable_CoreCoinedRowsDeclared(t *testing.T) {
	keys := coreKeys(t)
	for head, want := range map[string]config.Path{
		"ai_journal_retention_days": "core.ai.journal_retention",
		"public_base_url":           "core.http.public_base_url",
	} {
		r, ok := Table().Row(head)
		if !ok {
			t.Fatalf("no rename-table row %s", head)
		}
		found := false
		for _, tg := range r.Targets {
			if tg.ConfigPath() == want {
				found = true
				if !tg.Coined {
					t.Errorf("%s: target %s is not marked coined", head, want)
				}
			}
		}
		if !found {
			t.Errorf("row %s has no target %s", head, want)
		}
		if _, ok := keys[want]; !ok {
			t.Errorf("%s is not a core key", want)
		}
	}
}

// TestRenameTable_RefuseVerdictsMatchConfig (F3-06's seam item, owner F3-07):
// a retired name whose canonical path is a core key refuses when config
// itself would refuse it, a ProtectAdmin-or-higher or secret key anywhere
// under the path, and warns otherwise. The golden generates its verdicts
// from the table's own admin column; this compares them with the registry's.
func TestRenameTable_RefuseVerdictsMatchConfig(t *testing.T) {
	keys := coreKeys(t)
	compared := 0
	for _, r := range Table().Rows {
		for _, re := range r.Retired {
			c := re.Canonical
			if c == "" || !coreScopedPath(c) || re.Verdict == VerdictKept {
				continue
			}
			var under []config.KeyState
			for k, ks := range keys {
				if k == c || strings.HasPrefix(string(k), string(c)+".") {
					under = append(under, ks)
				}
			}
			if len(under) == 0 {
				continue // the registered-path test reports it
			}
			wantRefuse := false
			for _, ks := range under {
				if ks.Protection >= config.ProtectAdmin || ks.Secret != nil {
					wantRefuse = true
				}
			}
			compared++
			if got := re.Verdict == VerdictRefuse; got != wantRefuse {
				t.Errorf("row %s retired name %s -> %s: golden verdict %q, config refuses = %v", r.Head, re.Name, c, re.Verdict, wantRefuse)
			}
		}
	}
	if compared == 0 {
		t.Fatal("no retired name of a core key was compared")
	}
}
