// file: core/kernel/verified_method_test.go
// version: 0.1.0
// guid: c5e83a17-0b9d-4f26-8a41-7d3f19e6b2c0
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// The kernel's verified-method rule for task approval (F4-08 A11; 05-rbac
// §4.10, SEC-24).

const (
	scopeApproveSignal rbac.Permission = "gotham:signal.approve"
	scopeOtherSignal   rbac.Permission = "gotham:signal.view"
)

// vmKeys is auth.Service.APIKeys over a fixed list, with an optional error
// yielded first.
func vmKeys(err error, keys ...*auth.APIKey) func(context.Context, auth.UserID) iter.Seq2[*auth.APIKey, error] {
	return func(_ context.Context, owner auth.UserID) iter.Seq2[*auth.APIKey, error] {
		return func(yield func(*auth.APIKey, error) bool) {
			if err != nil && !yield(nil, err) {
				return
			}
			for _, k := range keys {
				if k.Owner == owner && !yield(k, nil) {
					return
				}
			}
		}
	}
}

func vmPrincipal(user string, m auth.Method, credential string) *auth.Principal {
	return &auth.Principal{User: &auth.User{ID: auth.UserID(user)}, Method: m, CredentialID: credential}
}

type vmNoAuth struct{}

func (vmNoAuth) Name() string                                        { return "noop" }
func (vmNoAuth) Authenticate(*http.Request) (*auth.Assertion, error) { return nil, nil }

// TestVerifiedMethodRule: every cell of the rule, each refusal one input away
// from a cell that verifies.
func TestVerifiedMethodRule(t *testing.T) {
	exts := []auth.Extension{
		{Authenticator: vmNoAuth{}, Spec: auth.MethodSpec{Method: "ext:cfaccess", Interactive: true}},
		{Authenticator: vmNoAuth{}, Spec: auth.MethodSpec{Method: "ext:badge"}},
	}
	keys := vmKeys(nil,
		&auth.APIKey{ID: "k-explicit", Owner: "u-lucius", Scopes: []rbac.Permission{scopeApproveSignal}},
		&auth.APIKey{ID: "k-admin", Owner: "u-lucius", Scopes: []rbac.Permission{"core:admin"}},
		&auth.APIKey{ID: "k-other", Owner: "u-lucius", Scopes: []rbac.Permission{scopeOtherSignal}},
		&auth.APIKey{ID: "k-selina", Owner: "u-selina", Scopes: []rbac.Permission{scopeApproveSignal}},
	)
	rule := verifiedMethodRule(exts, keys)
	need := rbac.Need(scopeApproveSignal)
	cases := []struct {
		name string
		p    *auth.Principal
		need rbac.Requirement
		want bool
	}{
		{"interactive session", vmPrincipal("u-lucius", auth.MethodSession, "s-1"), need, true},
		{"interactive extension", vmPrincipal("u-lucius", "ext:cfaccess", ""), need, true},
		{"key naming the scope", vmPrincipal("u-lucius", auth.MethodAPIKey, "k-explicit"), need, true},
		{"key naming one scope of an AnyOf", vmPrincipal("u-lucius", auth.MethodAPIKey, "k-explicit"), rbac.AnyOf(scopeOtherSignal, scopeApproveSignal), true},

		{"nil principal", nil, need, false},
		{"principal without a user", &auth.Principal{Method: auth.MethodSession}, need, false},
		{"delegated session", vmPrincipal("u-lucius", auth.MethodSessionDelegated, "s-2"), need, false},
		{"auth disabled", vmPrincipal("u-lucius", auth.MethodAuthDisabled, ""), need, false},
		{"non-interactive extension", vmPrincipal("u-lucius", "ext:badge", ""), need, false},
		{"unregistered extension", vmPrincipal("u-lucius", "ext:joker", ""), need, false},
		{"compat method", vmPrincipal("u-lucius", "arkham.gate", ""), need, false},
		{"key through core:admin", vmPrincipal("u-lucius", auth.MethodAPIKey, "k-admin"), need, false},
		{"key naming another scope", vmPrincipal("u-lucius", auth.MethodAPIKey, "k-other"), need, false},
		{"another user's key id", vmPrincipal("u-lucius", auth.MethodAPIKey, "k-selina"), need, false},
		{"unknown key id", vmPrincipal("u-lucius", auth.MethodAPIKey, "k-missing"), need, false},
		{"key principal without a credential id", vmPrincipal("u-lucius", auth.MethodAPIKey, ""), need, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := rule(c.p, c.need); got != c.want {
				t.Fatalf("verified = %v, want %v", got, c.want)
			}
		})
	}
}

// TestVerifiedMethodRule_KeyReadFailureRefuses: a key scan that fails refuses,
// even when the key would verify, and a rule with no key reader refuses every
// key.
func TestVerifiedMethodRule_KeyReadFailureRefuses(t *testing.T) {
	k := &auth.APIKey{ID: "k-explicit", Owner: "u-lucius", Scopes: []rbac.Permission{scopeApproveSignal}}
	p := vmPrincipal("u-lucius", auth.MethodAPIKey, "k-explicit")
	need := rbac.Need(scopeApproveSignal)
	if !verifiedMethodRule(nil, vmKeys(nil, k))(p, need) {
		t.Fatal("control: the key does not verify")
	}
	if verifiedMethodRule(nil, vmKeys(errors.New("the batcomputer is down"), k))(p, need) {
		t.Error("a failed key scan verified")
	}
	if verifiedMethodRule(nil, nil)(p, need) {
		t.Error("a rule with no key reader verified a key")
	}
	if !verifiedMethodRule(nil, nil)(vmPrincipal("u-lucius", auth.MethodSession, "s-1"), need) {
		t.Error("a rule with no key reader refused a session")
	}
}
