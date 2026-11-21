// file: core/auth/refresh_forgery_test.go
// version: 0.1.0
// guid: 8b3e5f21-7a49-4c06-9d12-e0c4a6b81f73
// last-edited: 2026-11-21

package auth_test

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// Batman names only. A module that holds a principal core built can write its
// exported fields (User, Method, CredentialID, Assurance, AuthTime,
// ClientAddr). Refresh rebuilds for what those fields say, so it must refuse a
// principal whose fields no longer say what auth recorded when it finished it:
// otherwise Refresh would mint a real principal for any identity.

// genuine is one principal core built, the service that refreshes it and a
// second real user a forger would want to become.
type genuine struct {
	svc    *auth.Service
	p      *auth.Principal
	victim *auth.User
}

// genuineKinds builds one genuine principal per credential shape Refresh
// serves: a session, a core key, an identity extension's and a compat key.
func genuineKinds() map[string]func(t *testing.T) genuine {
	return map[string]func(t *testing.T) genuine{
		"session": func(t *testing.T) genuine {
			f := newFx(t)
			u := f.user("barbara-gordon", rbac.RoleViewer)
			return genuine{f.svc, f.signedIn(u), f.user("harvey-dent", rbac.RoleAdmin)}
		},
		"api_key": func(t *testing.T) genuine {
			f := newFx(t)
			u := f.user("tim-drake", "gotham.detective")
			raw, _ := f.mint(f.session(u), u.ID, "gotham:patrol")
			_, got := serve(t, f.svc, request(raw, ""))
			if got.p == nil || got.p.Method != auth.MethodAPIKey {
				t.Fatalf("the key bound %+v", got.p)
			}
			return genuine{f.svc, got.p, f.user("harvey-dent", rbac.RoleAdmin)}
		},
		"ext": func(t *testing.T) genuine {
			f := newFx(t)
			u := f.user("barbara-gordon", rbac.RoleViewer)
			svc, p := extPrincipal(t, f, &auth.Assertion{User: u.ID, CredentialID: "cf-belfry",
				AuthTime: time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)}, false)
			return genuine{svc, p, f.user("harvey-dent", rbac.RoleAdmin)}
		},
		"compat": func(t *testing.T) genuine {
			f, c := compatFx(t)
			u := f.user("dick-grayson", "gotham.detective")
			raw, _ := f.mint(f.session(u), u.ID, "gotham:patrol")
			p, err := c.AuthenticateKey(context.Background(), raw, from("192.0.2.81"))
			if err != nil {
				t.Fatal(err)
			}
			return genuine{f.svc, p, f.user("harvey-dent", rbac.RoleAdmin)}
		},
	}
}

// TestRefresh_RefusesAnEditedPrincipal: one cell per credential shape and per
// field a holder can write. Every edit is refused with ErrInvalidCredentials
// naming the field, nothing is minted, and the same principal untouched still
// refreshes (so the cell fails for the edit, not for the fixture).
func TestRefresh_RefusesAnEditedPrincipal(t *testing.T) {
	edits := []struct {
		name  string
		field string // what the refusal names
		edit  func(g genuine)
	}{
		{"User replaced by another user's record", "user", func(g genuine) { g.p.User = g.victim }},
		{"User replaced by a record with the victim's ID", "user", func(g genuine) { g.p.User = &auth.User{ID: g.victim.ID} }},
		{"User.ID edited through the shared pointer", "user", func(g genuine) { g.p.User.ID = g.victim.ID }},
		{"User cleared", "user", func(g genuine) { g.p.User = nil }},
		{"Method set to a core method", "method", func(g genuine) {
			if g.p.Method == auth.MethodSessionDelegated {
				g.p.Method = auth.MethodSession
			} else {
				g.p.Method = auth.MethodSessionDelegated
			}
		}},
		{"Method set to the key method", "method", func(g genuine) {
			if g.p.Method == auth.MethodAPIKey {
				g.p.Method = auth.MethodSession
			} else {
				g.p.Method = auth.MethodAPIKey
			}
		}},
		{"CredentialID swapped", "credential", func(g genuine) { g.p.CredentialID = "another-credential" }},
		{"CredentialID cleared", "credential", func(g genuine) { g.p.CredentialID = "" }},
		{"Assurance raised to owner", "assurance", func(g genuine) { g.p.Assurance |= auth.AssuranceOwner }},
		{"AuthTime moved to now", "auth time", func(g genuine) { g.p.AuthTime = g.p.AuthTime.Add(time.Hour) }},
		{"ClientAddr changed", "client address", func(g genuine) { g.p.ClientAddr = netip.MustParseAddr("203.0.113.9") }},
	}
	for kind, build := range genuineKinds() {
		t.Run(kind, func(t *testing.T) {
			if g := build(t); refreshed(t, g.svc, g.p) == nil {
				t.Fatal("fixture: the untouched principal did not refresh")
			}
			for _, e := range edits {
				t.Run(e.name, func(t *testing.T) {
					g := build(t)
					e.edit(g)
					np, err := g.svc.Refresh(context.Background(), g.p)
					if np != nil {
						t.Errorf("an edited principal refreshed into %+v: a principal was minted", np)
					}
					if !errors.Is(err, auth.ErrInvalidCredentials) || !strings.Contains(err.Error(), e.field+" is not what auth built") {
						t.Errorf("Refresh of a principal with %s = %v, want ErrInvalidCredentials naming %q", e.name, err, e.field)
					}
				})
			}
		})
	}
}

// TestRefresh_RefusesAPrincipalMintedOutsideCore: a literal assembled from every
// exported field of a genuine principal is still not one auth built.
func TestRefresh_RefusesAPrincipalMintedOutsideCore(t *testing.T) {
	for kind, build := range genuineKinds() {
		t.Run(kind, func(t *testing.T) {
			g := build(t)
			lit := &auth.Principal{User: g.p.User, Method: g.p.Method, CredentialID: g.p.CredentialID, Perms: g.p.Perms,
				Assurance: g.p.Assurance, AuthTime: g.p.AuthTime, ClientAddr: g.p.ClientAddr}
			np, err := g.svc.Refresh(context.Background(), lit)
			if np != nil || !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Errorf("Refresh of a literal copy of a %s principal = %v, %v, want ErrInvalidCredentials", kind, np, err)
			}
		})
	}
}

// TestRefresh_ARefreshedPrincipalRefreshesAgain: Refresh seals the principal it
// builds (the credential, times and address it carried over included), so a
// holder can refresh again, and a copy of the struct keeps its seal.
func TestRefresh_ARefreshedPrincipalRefreshesAgain(t *testing.T) {
	for kind, build := range genuineKinds() {
		t.Run(kind, func(t *testing.T) {
			g := build(t)
			np := refreshed(t, g.svc, g.p)
			again := refreshed(t, g.svc, np)
			cp := *again
			refreshed(t, g.svc, &cp)
			np.CredentialID = "another-credential"
			if _, err := g.svc.Refresh(context.Background(), np); !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Errorf("a refreshed principal edited afterwards was not refused: %v", err)
			}
		})
	}
}
