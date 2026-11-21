// file: internal/books/cutover/config_oauth_refusal_test.go
// version: 0.1.0
// guid: 3b7e9a12-6c4d-4f80-9e25-d1a8c6b04f73
// last-edited: 2026-11-20

package cutover

import (
	"encoding/json"
	"strings"
	"testing"
)

// The OAuth client ids of the refusal cells: Batman-themed, never real.
const (
	fxOAuthGitHubID = "nightwing-github-client"
	fxOAuthGoogleID = "oracle-google-client"
)

// oauthBlob sets HEAD's OAuth rows: sign-in on or off, both client ids, and
// the allow-list emails ("" = the blob holds an empty allow-list, which HEAD
// reads as "nobody").
func oauthBlob(enabled bool, emails string) func(*rig) {
	return mutateBlob(func(tree map[string]any) {
		tree["oauth_enabled"] = enabled
		tree["oauth_github_client_id"] = fxOAuthGitHubID
		tree["oauth_github_client_secret"] = fxGitHub
		tree["oauth_google_client_id"] = fxOAuthGoogleID
		tree["oauth_google_client_secret"] = fxGoogleO
		tree["oauth_allowed_emails"] = emails
		tree["oauth_default_role"] = "user"
	})
}

// withOperatorSet marks paths as set by an operator layer for the boot.
func withOperatorSet(paths ...string) func(*rig) {
	return func(r *rig) {
		r.operatorSet = map[string]string{}
		for _, p := range paths {
			r.operatorSet[p] = "env"
		}
	}
}

// An enabled OAuth sign-in with an empty allow-list and no operator provider
// list refuses the upgrade with nothing written: the providers M2 would write
// admit nobody, and an allow-list HEAD read from OAUTH_ALLOWED_EMAILS is not
// in the blob at all. The refusal names both remedy keys and no value.
func TestConfigCutover_OAuthEmptyAllowListRefuses(t *testing.T) {
	r := newRig(t, "stage1", oauthBlob(true, ""))
	report := requireBlocked(t, r)
	for _, want := range []string{oidcProvidersKey, "ABO_CORE_AUTH_OIDC_PROVIDERS", "modules.books.cfaccess.allowed_emails", "oauth_allowed_emails", "OAUTH_ALLOWED_EMAILS"} {
		if !strings.Contains(report, want) {
			t.Errorf("the refusal does not name %q:\n%s", want, report)
		}
	}
	for _, v := range []string{fxOAuthGitHubID, fxOAuthGoogleID, fxGitHub, fxGoogleO} {
		if strings.Contains(report, v) {
			t.Errorf("the refusal carries a value (%q)", v)
		}
	}
}

// The same blob proceeds once an operator layer sets the provider list: the
// operator's entries are the allow-list, and M2's own staging of the key is
// what core's lock then overrides at read time.
func TestConfigCutover_OAuthEmptyAllowListWithOperatorProvidersProceeds(t *testing.T) {
	snap, err := newRig(t, "stage1", oauthBlob(true, ""), withOperatorSet(oidcProvidersKey)).run(t)
	if err != nil {
		t.Fatalf("M2 with the operator provider list set: %v", err)
	}
	raw, ok := stagedValue(snap, oidcProvidersKey)
	if !ok {
		t.Fatalf("%s was not staged", oidcProvidersKey)
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(raw), &entries); err != nil || len(entries) != 2 {
		t.Fatalf("providers = %s (err %v), want two entries", raw, err)
	}
	for _, e := range entries {
		if emails, _ := e["allowed_emails"].([]any); len(emails) != 0 {
			t.Errorf("%v: allowed_emails = %v, want empty (the blob's list is empty)", e["id"], e["allowed_emails"])
		}
	}
}

// An operator layer that sets some other key does not lift the refusal: only
// the provider list carries the allow-list.
func TestConfigCutover_OAuthEmptyAllowListOtherOperatorKeyRefuses(t *testing.T) {
	requireBlocked(t, newRig(t, "stage1", oauthBlob(true, ""), withOperatorSet("modules.books.cfaccess.allowed_emails")))
}

// With sign-in disabled the fold writes no provider entry, so there is
// nothing to admit nobody, and the upgrade proceeds.
func TestConfigCutover_OAuthDisabledEmptyAllowListProceeds(t *testing.T) {
	snap, err := newRig(t, "stage1", oauthBlob(false, "")).run(t)
	if err != nil {
		t.Fatalf("M2 with sign-in disabled: %v", err)
	}
	if _, ok := stagedValue(snap, oidcProvidersKey); ok {
		t.Errorf("a disabled sign-in staged %s", oidcProvidersKey)
	}
}

// A non-empty allow-list proceeds without an operator layer (the cell
// TestConfigPlan_OIDCFold pins the entries' content).
func TestConfigCutover_OAuthWithAllowListProceeds(t *testing.T) {
	if _, err := newRig(t, "stage1", oauthBlob(true, "bruce@batcave.example")).run(t); err != nil {
		t.Fatalf("M2 with a stored allow-list: %v", err)
	}
}
