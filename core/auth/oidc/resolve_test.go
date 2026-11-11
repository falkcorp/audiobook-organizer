// file: core/auth/oidc/resolve_test.go
// version: 1.0.0
// guid: d8b8f2e1-d3a8-40f1-a464-c5df45cc1c33
// last-edited: 2026-11-11

package oidc

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

func trusting() Policy {
	return Policy{Provider: "gotham", AllowedDomains: []string{"wayne.example.test"}, LinkExistingByEmail: true}
}

func untrusting() Policy {
	return Policy{Provider: "gotham", AllowedDomains: []string{"wayne.example.test"}}
}

func claims(sub, email string) Claims {
	return Claims{Provider: "gotham", Subject: sub, Email: email, EmailVerified: true, Name: "Dick Grayson"}
}

func resolve(t testing.TB, f *fx, pol Policy, c Claims) (*auth.User, error) {
	t.Helper()
	return f.resolver(nil).Resolve(context.Background(), pol, c)
}

// TestOIDC_LinksBySubjectNotEmail: once (provider, subject) is linked, the
// subject is the account, whatever address the provider now reports; and a
// different subject reporting the same address is not that account.
func TestOIDC_LinksBySubjectNotEmail(t *testing.T) {
	f := newFx(t)
	first, err := resolve(t, f, untrusting(), claims("sub-robin", "dick@wayne.example.test"))
	if err != nil {
		t.Fatal(err)
	}
	// The same subject, now reporting another allowed address: still the
	// same account, and no second one.
	again, err := resolve(t, f, untrusting(), claims("sub-robin", "nightwing@wayne.example.test"))
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID {
		t.Fatalf("the same subject resolved to %s, want %s: linking must be by subject", again.ID, first.ID)
	}
	if again.Email != "dick@wayne.example.test" {
		t.Fatalf("a changed provider address rewrote the account's: %q", again.Email)
	}
	if n := f.countUsers(); n != 1 {
		t.Fatalf("users = %d, want 1", n)
	}
	// Another subject with the first account's address: refused, never the
	// first account.
	other, err := resolve(t, f, untrusting(), claims("sub-impostor", "dick@wayne.example.test"))
	mustIs(t, err, auth.ErrEmailInUse)
	if other != nil {
		t.Fatalf("an unseen subject got %+v", other)
	}
	if _, err := f.identity("gotham", "sub-impostor"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("an identity was linked for the impostor: %v", err)
	}
}

// TestOIDC_UntrustedProviderCannotLinkByEmail: link_existing_by_email is the
// operator's statement that the provider speaks for address owners; without
// it an unseen subject never takes an existing account.
func TestOIDC_UntrustedProviderCannotLinkByEmail(t *testing.T) {
	f := newFx(t)
	victim := f.put(&auth.User{ID: "01JBATCAVE0000000000000002", Username: "oracle", Email: "barbara@wayne.example.test", EmailVerified: true, Roles: []rbac.RoleName{rbac.RoleViewer}})

	_, err := resolve(t, f, untrusting(), claims("sub-oracle-like", "barbara@wayne.example.test"))
	mustIs(t, err, auth.ErrEmailInUse)
	if _, err := f.identity("gotham", "sub-oracle-like"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("an identity was linked by an untrusted provider: %v", err)
	}
	if f.audit.emittedWith("core.identity.link", "email_in_use") != 1 {
		t.Fatal("the refused link was not audited")
	}

	// The control: the same claims through a trusting policy link.
	got, err := resolve(t, f, trusting(), claims("sub-oracle-like", "barbara@wayne.example.test"))
	if err != nil {
		t.Fatalf("trusted link: %v", err)
	}
	if got.ID != victim.ID {
		t.Fatalf("linked %s, want %s", got.ID, victim.ID)
	}
	if id, err := f.identity("gotham", "sub-oracle-like"); err != nil || id.UserID != victim.ID {
		t.Fatalf("identity = %+v, %v", id, err)
	}
	if !slices.Contains(f.audit.actions(), "core.identity.link") {
		t.Fatalf("recorded = %v, want a core.identity.link", f.audit.actions())
	}
}

// Even a trusted provider never takes an admin or a password account.
func TestOIDC_TrustedProviderStillCannotTakeAdminOrPasswordAccount(t *testing.T) {
	for _, tc := range []struct {
		name string
		u    func(f *fx) *auth.User
	}{
		{"admin", func(f *fx) *auth.User {
			return f.put(&auth.User{ID: "01JBATCAVE0000000000000003", Username: "batman", Email: "bruce@wayne.example.test", EmailVerified: true, Roles: []rbac.RoleName{rbac.RoleAdmin}})
		}},
		{"password", func(f *fx) *auth.User {
			return f.withPassword(&auth.User{ID: "01JBATCAVE0000000000000004", Username: "robin", Email: "bruce@wayne.example.test", EmailVerified: true, Roles: []rbac.RoleName{rbac.RoleViewer}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t)
			tc.u(f)
			_, err := resolve(t, f, trusting(), claims("sub-gordon", "bruce@wayne.example.test"))
			mustIs(t, err, auth.ErrEmailInUse)
			if _, err := f.identity("gotham", "sub-gordon"); !errors.Is(err, auth.ErrNotFound) {
				t.Fatalf("identity linked: %v", err)
			}
		})
	}
	t.Run("a role granting admin through a custom role", func(t *testing.T) {
		f := newFx(t)
		// custom.signal-operator holds no admin: the control that the check
		// is on what the role grants, not on its name.
		f.put(&auth.User{ID: "01JBATCAVE0000000000000005", Username: "signal", Email: "bruce@wayne.example.test", EmailVerified: true, Roles: []rbac.RoleName{roleSignal}})
		if _, err := resolve(t, f, trusting(), claims("sub-gordon", "bruce@wayne.example.test")); err != nil {
			t.Fatalf("a non-admin custom role may be linked: %v", err)
		}
	})
}

func TestOIDC_ResolveStatusGates(t *testing.T) {
	t.Run("linked account disabled", func(t *testing.T) {
		f := newFx(t)
		first, err := resolve(t, f, untrusting(), claims("sub-robin", "dick@wayne.example.test"))
		if err != nil {
			t.Fatal(err)
		}
		u := f.user(string(first.ID))
		u.Status, u.Version = auth.StatusDisabled, u.Version+1
		f.put(u)
		_, err = resolve(t, f, untrusting(), claims("sub-robin", "dick@wayne.example.test"))
		mustIs(t, err, auth.ErrInactiveUser)
	})
	t.Run("linked account restricted", func(t *testing.T) {
		f := newFx(t)
		first, _ := resolve(t, f, untrusting(), claims("sub-robin", "dick@wayne.example.test"))
		u := f.user(string(first.ID))
		u.SignInRestricted, u.Version = true, u.Version+1
		f.put(u)
		_, err := resolve(t, f, untrusting(), claims("sub-robin", "dick@wayne.example.test"))
		mustIs(t, err, auth.ErrResetRequired)
	})
	t.Run("link by email onto a disabled holder", func(t *testing.T) {
		f := newFx(t)
		f.put(&auth.User{ID: "01JBATCAVE0000000000000006", Username: "oracle", Email: "barbara@wayne.example.test", EmailVerified: true, Status: auth.StatusDisabled, Roles: []rbac.RoleName{rbac.RoleViewer}})
		_, err := resolve(t, f, trusting(), claims("sub-x", "barbara@wayne.example.test"))
		mustIs(t, err, auth.ErrInactiveUser)
		if _, err := f.identity("gotham", "sub-x"); !errors.Is(err, auth.ErrNotFound) {
			t.Fatal("an identity was linked to a disabled account")
		}
	})
	t.Run("link by email onto a restricted holder", func(t *testing.T) {
		f := newFx(t)
		f.put(&auth.User{ID: "01JBATCAVE0000000000000007", Username: "oracle", Email: "barbara@wayne.example.test", EmailVerified: true, SignInRestricted: true, Roles: []rbac.RoleName{rbac.RoleViewer}})
		_, err := resolve(t, f, trusting(), claims("sub-x", "barbara@wayne.example.test"))
		mustIs(t, err, auth.ErrResetRequired)
	})
}

func TestOIDC_ResolveAdmission(t *testing.T) {
	f := newFx(t)
	for _, tc := range []struct {
		name  string
		pol   Policy
		c     Claims
		extra func(context.Context, string) (bool, error)
		want  error
	}{
		{"unverified", untrusting(), Claims{Provider: "gotham", Subject: "s", Email: "dick@wayne.example.test"}, nil, ErrEmailNotVerified},
		{"no email", untrusting(), Claims{Provider: "gotham", Subject: "s", EmailVerified: true}, nil, ErrEmailNotVerified},
		{"outside the allowed domain", untrusting(), claims("s", "joker@arkham.example.test"), nil, ErrEmailNotAllowed},
		{"subdomain is not the domain", untrusting(), claims("s", "x@evil.wayne.example.test"), nil, ErrEmailNotAllowed},
		{"suffix trick", untrusting(), claims("s", "x@notwayne.example.test"), nil, ErrEmailNotAllowed},
		{"no allowlist at all", Policy{Provider: "gotham"}, claims("s", "dick@wayne.example.test"), nil, ErrEmailNotAllowed},
		{"extra says no", Policy{Provider: "gotham"}, claims("s", "dick@wayne.example.test"), func(context.Context, string) (bool, error) { return false, nil }, ErrEmailNotAllowed},
		{"extra errors: fail closed", Policy{Provider: "gotham"}, claims("s", "dick@wayne.example.test"), func(context.Context, string) (bool, error) { return true, errors.New("invite store down") }, auth.ErrStoreUnavailable},
		{"claims for another provider", untrusting(), Claims{Provider: "hub", Subject: "s", Email: "dick@wayne.example.test", EmailVerified: true}, nil, ErrExchange},
		{"no subject", untrusting(), claims("", "dick@wayne.example.test"), nil, ErrExchange},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := f.countUsers()
			_, err := f.resolver(tc.extra).Resolve(context.Background(), tc.pol, tc.c)
			mustIs(t, err, tc.want)
			if f.countUsers() != before {
				t.Fatal("a refused sign-in created an account")
			}
		})
	}
	t.Run("allowed by exact address, case and spacing folded", func(t *testing.T) {
		pol := Policy{Provider: "gotham", AllowedEmails: []string{"  Dick@Wayne.Example.Test "}}
		if _, err := resolve(t, f, pol, claims("s-a", "DICK@wayne.example.test")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("allowed by an @domain entry", func(t *testing.T) {
		pol := Policy{Provider: "gotham", AllowedDomains: []string{"@Wayne.Example.Test"}}
		if _, err := resolve(t, f, pol, claims("s-b", "jason@wayne.example.test")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("allowed by invite", func(t *testing.T) {
		b := f.st.NewBatch()
		defer func() { _ = b.Close() }()
		if err := f.st.ImportInviteAllow(b, "selina@gotham.example.test"); err != nil {
			t.Fatal(err)
		}
		if err := b.Commit(context.Background(), storage.Sync); err != nil {
			t.Fatal(err)
		}
		if _, err := resolve(t, f, Policy{Provider: "gotham"}, claims("s-c", "selina@gotham.example.test")); err != nil {
			t.Fatalf("an invited address: %v", err)
		}
	})
	t.Run("allowed by the extra hook", func(t *testing.T) {
		yes := func(context.Context, string) (bool, error) { return true, nil }
		_, err := f.resolver(yes).Resolve(context.Background(), Policy{Provider: "gotham"}, claims("s-d", "harvey@gotham.example.test"))
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestOIDC_ResolveCreatesWithDefaultRole(t *testing.T) {
	t.Run("default is viewer", func(t *testing.T) {
		f := newFx(t)
		u, err := resolve(t, f, untrusting(), claims("sub-robin", "dick@wayne.example.test"))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(u.Roles, []rbac.RoleName{rbac.RoleViewer}) || !u.EmailVerified || u.Status != auth.StatusActive {
			t.Fatalf("created %+v", u)
		}
		if u.Username != "dick@wayne.example.test" || u.DisplayName != "Dick Grayson" || u.HasPassword {
			t.Fatalf("created %+v", u)
		}
		if got := f.audit.actions(); !slices.Contains(got, "core.user.create") || !slices.Contains(got, "core.identity.link") {
			t.Fatalf("recorded = %v", got)
		}
		if !f.svc.HasUsers() {
			t.Fatal("HasUsers is false after a creation")
		}
	})
	t.Run("a configured non-admin role", func(t *testing.T) {
		f := newFx(t)
		pol := untrusting()
		pol.DefaultRole = roleSignal
		u, err := resolve(t, f, pol, claims("sub-robin", "dick@wayne.example.test"))
		if err != nil || !slices.Equal(u.Roles, []rbac.RoleName{roleSignal}) {
			t.Fatalf("user %+v, err %v", u, err)
		}
	})
	t.Run("a default role granting core:admin is refused", func(t *testing.T) {
		f := newFx(t)
		pol := untrusting()
		pol.DefaultRole = rbac.RoleAdmin
		_, err := resolve(t, f, pol, claims("sub-robin", "dick@wayne.example.test"))
		mustIs(t, err, auth.ErrBadInput)
		if f.countUsers() != 0 {
			t.Fatal("an account was created with an admin default role")
		}
	})
	t.Run("an unknown default role is refused", func(t *testing.T) {
		f := newFx(t)
		pol := untrusting()
		pol.DefaultRole = "custom.no-such-role"
		_, err := resolve(t, f, pol, claims("sub-robin", "dick@wayne.example.test"))
		mustIs(t, err, auth.ErrBadInput)
		if f.countUsers() != 0 {
			t.Fatal("an account was created with an unknown role")
		}
	})
	t.Run("a taken username gets a suffix", func(t *testing.T) {
		f := newFx(t)
		f.put(&auth.User{ID: "01JBATCAVE0000000000000008", Username: "dick@wayne.example.test", Email: "other@wayne.example.test", EmailVerified: true, Roles: []rbac.RoleName{rbac.RoleViewer}})
		u, err := resolve(t, f, untrusting(), claims("sub-robin", "dick@wayne.example.test"))
		if err != nil || u.Username != "dick@wayne.example.test1" {
			t.Fatalf("user %+v, err %v", u, err)
		}
	})
}

// An unverified holder of the address does not block the verified sign-in; it
// loses the address when the provider is trusted and the account passes the
// same guard, and that is audited.
func TestOIDC_VerifiedSignInDisplacesUnverifiedHolder(t *testing.T) {
	setup := func(f *fx, roles ...rbac.RoleName) *auth.User {
		return f.put(&auth.User{ID: "01JBATCAVE0000000000000009", Username: "squatter", Email: "dick@wayne.example.test", EmailVerified: false, Roles: roles})
	}
	t.Run("trusted provider displaces", func(t *testing.T) {
		f := newFx(t)
		sq := setup(f, rbac.RoleViewer)
		u, err := resolve(t, f, trusting(), claims("sub-robin", "dick@wayne.example.test"))
		if err != nil {
			t.Fatal(err)
		}
		if u.ID == sq.ID {
			t.Fatal("the unverified account was signed in instead of a new one")
		}
		if got := f.user(string(sq.ID)); got.Email != "" || got.EmailVerified {
			t.Fatalf("the squatter kept the address: %+v", got)
		}
		if !slices.Contains(f.audit.actions(), "core.identity.displace") {
			t.Fatalf("recorded = %v, want core.identity.displace", f.audit.actions())
		}
		for _, e := range f.audit.recorded {
			if e.Action == "core.identity.displace" {
				mustNot(t, e.Reason+" "+toString(e.Details), "dick@wayne")
			}
		}
	})
	t.Run("untrusted provider leaves the holder alone", func(t *testing.T) {
		f := newFx(t)
		sq := setup(f, rbac.RoleViewer)
		u, err := resolve(t, f, untrusting(), claims("sub-robin", "dick@wayne.example.test"))
		if err != nil {
			t.Fatal(err)
		}
		if got := f.user(string(sq.ID)); got.Email != "dick@wayne.example.test" {
			t.Fatalf("an untrusted provider took an address: %+v", got)
		}
		if slices.Contains(f.audit.actions(), "core.identity.displace") || u.ID == sq.ID {
			t.Fatal("untrusted displacement")
		}
	})
	t.Run("an unverified admin keeps its address", func(t *testing.T) {
		f := newFx(t)
		sq := setup(f, rbac.RoleAdmin)
		if _, err := resolve(t, f, trusting(), claims("sub-robin", "dick@wayne.example.test")); err != nil {
			t.Fatal(err)
		}
		if got := f.user(string(sq.ID)); got.Email != "dick@wayne.example.test" {
			t.Fatalf("an admin account was un-mailed: %+v", got)
		}
	})
	t.Run("an unverified account with a password keeps its address", func(t *testing.T) {
		f := newFx(t)
		sq := f.withPassword(&auth.User{ID: "01JBATCAVE0000000000000010", Username: "squatter", Email: "dick@wayne.example.test", Roles: []rbac.RoleName{rbac.RoleViewer}})
		if _, err := resolve(t, f, trusting(), claims("sub-robin", "dick@wayne.example.test")); err != nil {
			t.Fatal(err)
		}
		if got := f.user(string(sq.ID)); got.Email != "dick@wayne.example.test" {
			t.Fatalf("a password account was un-mailed: %+v", got)
		}
	})
}

// A linked account that held the address unverified gets it marked verified.
func TestOIDC_ResolveMarksLinkedAddressVerified(t *testing.T) {
	f := newFx(t)
	first, err := resolve(t, f, untrusting(), claims("sub-robin", "dick@wayne.example.test"))
	if err != nil {
		t.Fatal(err)
	}
	u := f.user(string(first.ID))
	u.EmailVerified, u.Version = false, u.Version+1
	f.put(u)
	got, err := resolve(t, f, untrusting(), claims("sub-robin", "dick@wayne.example.test"))
	if err != nil || !got.EmailVerified {
		t.Fatalf("user %+v, err %v", got, err)
	}
	if !f.user(string(first.ID)).EmailVerified {
		t.Fatal("the verified flag was not stored")
	}
}

// Concurrent first sign-ins of one subject create exactly one account: the
// whole decision runs under the shared e-mail lock.
func TestOIDC_ConcurrentSignInsCreateOneAccount(t *testing.T) {
	f := newFx(t)
	const n = 8
	ids := make([]auth.UserID, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u, err := f.resolver(nil).Resolve(context.Background(), untrusting(), claims("sub-robin", "dick@wayne.example.test"))
			errs[i] = err
			if u != nil {
				ids[i] = u.ID
			}
		}()
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("sign-in %d: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("sign-ins resolved to different accounts: %v", ids)
		}
	}
	if got := f.countUsers(); got != 1 {
		t.Fatalf("users = %d, want exactly 1", got)
	}
}

func TestNewResolverRequiresCollaborators(t *testing.T) {
	f := newFx(t)
	good := ResolverDeps{Store: f.st, NewBatch: f.st.NewBatch, Audit: f.audit, Roles: f.rb, Accounts: f.svc}
	if _, err := NewResolver(good); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*ResolverDeps){
		"store":    func(d *ResolverDeps) { d.Store = nil },
		"batch":    func(d *ResolverDeps) { d.NewBatch = nil },
		"audit":    func(d *ResolverDeps) { d.Audit = nil },
		"roles":    func(d *ResolverDeps) { d.Roles = nil },
		"accounts": func(d *ResolverDeps) { d.Accounts = nil },
	} {
		d := good
		mod(&d)
		if _, err := NewResolver(d); err == nil {
			t.Errorf("NewResolver without %s succeeded", name)
		}
	}
}

func TestSanitizeUsernameAndULID(t *testing.T) {
	if got := sanitizeUsername("  Dick+Grayson@Wayne.Example.Test<script> "); got != "dick+grayson@wayne.example.testscript" {
		t.Fatalf("sanitizeUsername = %q", got)
	}
	if sanitizeUsername("***") != "" {
		t.Fatal("a name of only forbidden characters must reduce to empty")
	}
}

func toString(m map[string]any) string {
	s := ""
	for k, v := range m {
		s += k + "=" + anyString(v) + ";"
	}
	return s
}

func anyString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	}
	return ""
}
