// file: internal/books/identity_policy_test.go
// version: 0.1.0
// guid: 7b1d4f93-2a60-4c85-be17-d903e6a58c21
// last-edited: 2026-11-21

package books

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/oidc"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// TestIdentityPolicy_IsBuiltFromHEADsAllowListInputsOnly: the policy of an Access
// sign-in carries the allow-list HEAD's IsEmailAllowed matched (exact addresses,
// normalized, an empty list admits nobody), the default role HEAD gave a new user
// ("viewer" when unset), no domain rule, and the provider core keys identities by.
func TestIdentityPolicy_IsBuiltFromHEADsAllowListInputsOnly(t *testing.T) {
	for _, tc := range []struct {
		name, emails, role string
		want               oidc.Policy
	}{
		{"two addresses, normalized, default role", " Bruce@Wayne.Example.Test ,alfred@wayne.example.test", "",
			oidc.Policy{Provider: "cfaccess", AllowedEmails: []string{"bruce@wayne.example.test", "alfred@wayne.example.test"}, DefaultRole: "viewer", LinkExistingByEmail: true}},
		{"an empty list admits nobody", "", "editor",
			oidc.Policy{Provider: "cfaccess", AllowedEmails: []string{}, DefaultRole: "editor", LinkExistingByEmail: true}},
		{"blank entries are dropped", ",, ,dick@wayne.example.test,", "viewer",
			oidc.Policy{Provider: "cfaccess", AllowedEmails: []string{"dick@wayne.example.test"}, DefaultRole: "viewer", LinkExistingByEmail: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := identityPolicy(tc.emails, tc.role)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("policy = %+v, want %+v", got, tc.want)
			}
			if len(got.AllowedDomains) != 0 {
				t.Errorf("AllowedDomains = %v: HEAD matched exact addresses only, a domain rule admits more", got.AllowedDomains)
			}
		})
	}
	c := config.Config{OAuthAllowedEmails: "alfred@wayne.example.test", OAuthDefaultRole: "editor"}
	if got := identityPolicyFrom(c); !reflect.DeepEqual(got, identityPolicy("alfred@wayne.example.test", "editor")) {
		t.Errorf("identityPolicyFrom reads something else than OAuthAllowedEmails and OAuthDefaultRole: %+v", got)
	}
}

type recordingResolver struct {
	mu    sync.Mutex
	pols  []oidc.Policy
	calls int
}

func (r *recordingResolver) Resolve(_ context.Context, p oidc.Policy, _ oidc.Claims) (*auth.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pols = append(r.pols, p)
	r.calls++
	return &auth.User{ID: "01JBATCAVE0000000000000030"}, nil
}

// TestPolicyResolver_RefusesUntilBoundAndThenRunsTheBoundPolicy: before Register
// binds the policy the resolver admits nobody and asks nothing of core; after it
// every claim is resolved under the bound policy, never the placeholder the
// extension was built with.
func TestPolicyResolver_RefusesUntilBoundAndThenRunsTheBoundPolicy(t *testing.T) {
	inner := &recordingResolver{}
	p := newPolicyResolver(inner)
	claims := oidc.Claims{Provider: "cfaccess", Subject: "s", Email: "alfred@wayne.example.test", EmailVerified: true}
	placeholder := oidc.Policy{Provider: "cfaccess", AllowedEmails: []string{"everyone@wayne.example.test"}}
	if u, err := p.Resolve(context.Background(), placeholder, claims); u != nil || !errors.Is(err, oidc.ErrResolverUnbound) {
		t.Fatalf("unbound: %+v, %v; want nil and ErrResolverUnbound", u, err)
	}
	if inner.calls != 0 {
		t.Fatalf("an unbound policy resolver reached core %d time(s)", inner.calls)
	}
	bound := identityPolicy("alfred@wayne.example.test", "viewer")
	p.bind(bound)
	if _, err := p.Resolve(context.Background(), placeholder, claims); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inner.pols, []oidc.Policy{bound}) {
		t.Errorf("core resolved under %+v, want the bound policy %+v and never the extension's own", inner.pols, bound)
	}
}

// storeNeverWritten is an auth.Store that answers what admission asks (the invite
// allow-list) and panics on anything else: a sign-in that is refused at the gate
// reads and writes nothing, so any other call fails the test.
type storeNeverWritten struct {
	auth.Store
	invites int
}

func (s *storeNeverWritten) InviteAllowed(context.Context, string) (bool, error) {
	s.invites++
	return false, nil
}

type roleReaderNever struct{}

func (roleReaderNever) Role(context.Context, rbac.RoleName) (*rbac.Role, error) {
	panic("a refused sign-in read a role")
}
func (roleReaderNever) RoleSetGeneration(context.Context) (uint64, error) {
	panic("a refused sign-in read the role generation")
}
func (roleReaderNever) ClaimRoleSet(*storage.Batch, uint64) error {
	panic("a refused sign-in claimed the role set")
}

type lockNever struct{}

func (lockNever) LockEmails() func() { panic("a refused sign-in took the account lock") }
func (lockNever) NoteUserCreated()   { panic("a refused sign-in created a user") }

type auditNever struct{ audit.Logger }

// TestPolicyResolver_ANonAllowListedAddressCreatesNothing: the real core resolver
// under the policy built from HEAD's inputs refuses an Access identity whose
// address is not on the list: no account lock, no store read or write, no role
// read (the doubles panic on any), and the answer is the refusal core keeps for
// it, not a user.
func TestPolicyResolver_ANonAllowListedAddressCreatesNothing(t *testing.T) {
	store := &storeNeverWritten{}
	res, err := oidc.NewResolver(oidc.ResolverDeps{
		Store:    store,
		NewBatch: func() *storage.Batch { panic("a refused sign-in opened a batch") },
		Audit:    auditNever{},
		Roles:    roleReaderNever{},
		Accounts: lockNever{},
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock:    func() time.Time { return time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	p := newPolicyResolver(res)
	p.bind(identityPolicy("alfred@wayne.example.test", "viewer"))
	u, err := p.Resolve(context.Background(), oidc.Policy{Provider: "cfaccess"}, oidc.Claims{
		Provider: "cfaccess", Subject: "cf-sub-joker", Email: "joker@gotham.example.test", EmailVerified: true,
	})
	if u != nil || !errors.Is(err, oidc.ErrEmailNotAllowed) {
		t.Fatalf("a non-allow-listed address: user %+v, error %v; want nil and ErrEmailNotAllowed", u, err)
	}
	if store.invites != 1 {
		t.Errorf("the invite allow-list was asked %d time(s), want 1 (core's own, after the list missed)", store.invites)
	}
}
