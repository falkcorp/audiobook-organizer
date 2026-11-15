// file: internal/books/cutover/reach_roles_test.go
// version: 1.0.0
// guid: 7e1c9a3d-5b84-4f62-a0d7-3c8e2b6f9145
// last-edited: 2026-11-15

package cutover

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
	"github.com/falkcorp/mediacore/rbac"
)

// Round-3 NIT-1: the oracle's session and key principals are users whose roles M3
// migrated, and reachWhos reads what M3 left. Nothing used to assert that what M3
// left is what principals.json says the principals are: a user that came out with
// another role, or a custom role that came out with another set, would have
// changed every verdict of its principals while the recording still described the
// old ones. reachRoleProblems states it from the data the recorder wrote.

// reachCustomRole is a custom role principals.json describes: its title and the
// HEAD atoms it holds.
type reachCustomRole struct {
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
}

// reachRoleProblems compares the migrated corpus with principals.json: each user
// holds exactly the role the table names (a seed role by its name, a custom role
// as one imported role carrying the custom role's title), and each imported custom
// role holds exactly the scopes of the atoms principals.json gives it (an atom the
// table does not have contributes none) plus the ambient scopes.
func reachRoleProblems(snap *idSnap, pd *reachPrincipalsDoc, atoms []m3.AtomRow) []string {
	var out []string
	scopesOf := map[string][]rbac.Permission{}
	for _, a := range atoms {
		scopesOf[a.Name] = a.Scopes
	}
	byTitle := map[string]rbac.Role{}
	for _, r := range snap.Roles {
		if _, dup := byTitle[r.Title]; dup {
			out = append(out, "two imported roles carry the title "+r.Title)
		}
		byTitle[r.Title] = r
	}
	roleOf := func(label string) (rbac.RoleName, bool) {
		if c, ok := pd.CustomRoles[label]; ok {
			r, ok := byTitle[c.Name]
			return r.Name, ok
		}
		return rbac.RoleName(label), true
	}
	for username, label := range pd.Users {
		var got []rbac.RoleName
		found := false
		for _, u := range snap.Users {
			if strings.EqualFold(u.User.Username, username) {
				got, found = u.User.Roles, true
			}
		}
		if !found {
			out = append(out, fmt.Sprintf("user %s: M3 left no such user", username))
			continue
		}
		want, ok := roleOf(label)
		if !ok {
			out = append(out, fmt.Sprintf("user %s: principals.json gives the role %q, and M3 imported no role of that title", username, label))
			continue
		}
		if !slices.Equal(got, []rbac.RoleName{want}) {
			out = append(out, fmt.Sprintf("user %s: migrated roles %v, principals.json says %q (%s)", username, got, label, want))
		}
	}
	for label, c := range pd.CustomRoles {
		r, ok := byTitle[c.Name]
		if !ok {
			out = append(out, fmt.Sprintf("custom role %s (%s): M3 imported no role of that title", label, c.Name))
			continue
		}
		// M3 gives every imported role the ambient scopes (05-rbac 6.3) besides its atoms'.
		want := reachAmbient()
		for _, atom := range c.Permissions {
			want = append(want, scopesOf[atom]...)
		}
		got := slices.Clone(r.Added)
		slices.Sort(want)
		want = slices.Compact(want)
		slices.Sort(got)
		got = slices.Compact(got)
		if !slices.Equal(got, want) {
			out = append(out, fmt.Sprintf("custom role %s (%s): imported with %v, principals.json's atoms give %v", label, c.Name, got, want))
		}
	}
	return out
}

func TestReachRoles_MigratedRolesAreThePrincipalTablesRoles(t *testing.T) {
	pd, _ := reachLoad(t)
	if len(pd.Users) == 0 || len(pd.CustomRoles) == 0 {
		t.Fatal("principals.json has no users or custom_roles to hold the migrated roles to")
	}
	atoms := idAtoms()
	snap, _ := reachMigrate(t, pd, atoms)
	for _, p := range reachRoleProblems(snap, pd, atoms) {
		t.Error(p)
	}
	// every user a principal uses is in the table
	for _, p := range pd.Principals {
		if p.User == "" {
			continue
		}
		if _, ok := pd.Users[p.User]; !ok {
			t.Errorf("principal %s names user %s, which principals.json's users does not list", p.Name, p.User)
		}
	}
}

func TestReachRoles_FailsOnAChangedUserRoleOrCustomRoleSet(t *testing.T) {
	pd, _ := reachLoad(t)
	atoms := idAtoms()
	snap, _ := reachMigrate(t, pd, atoms)

	var user, other string
	for id, u := range snap.Users {
		switch strings.ToLower(u.User.Username) {
		case "james.gordon":
			user = id
		case "dick.grayson":
			other = id
		}
	}
	mutate := func(f func(s *idSnap)) []string {
		c := *snap
		c.Users, c.Roles = maps.Clone(snap.Users), maps.Clone(snap.Roles)
		f(&c)
		return reachRoleProblems(&c, pd, atoms)
	}
	if got := reachRoleProblems(snap, pd, atoms); len(got) != 0 {
		t.Fatalf("the real corpus already fails: %v", got)
	}
	for _, tc := range []struct {
		name string
		f    func(*idSnap)
		want string
	}{
		{"a user holds another role", func(s *idSnap) {
			u := s.Users[user]
			u.User.Roles = []rbac.RoleName{"viewer"}
			s.Users[user] = u
		}, "james.gordon"},
		{"a user holds an extra role", func(s *idSnap) {
			u := s.Users[other]
			u.User.Roles = append(slices.Clone(u.User.Roles), "viewer")
			s.Users[other] = u
		}, "dick.grayson"},
		{"a custom role lost a scope", func(s *idSnap) {
			for n, r := range s.Roles {
				if r.Title == "Arkham Staff" {
					r.Added = r.Added[1:]
					s.Roles[n] = r
				}
			}
		}, "Arkham Staff"},
		{"a custom role gained a scope", func(s *idSnap) {
			for n, r := range s.Roles {
				if r.Title == "Wayne Enterprises" {
					r.Added = append(slices.Clone(r.Added), "books:delete")
					s.Roles[n] = r
				}
			}
		}, "Wayne Enterprises"},
		{"a custom role was not imported", func(s *idSnap) {
			for n, r := range s.Roles {
				if r.Title == "GCPD Command" {
					delete(s.Roles, n)
				}
			}
		}, "GCPD Command"},
	} {
		got := mutate(tc.f)
		if len(got) == 0 || !strings.Contains(strings.Join(got, "\n"), tc.want) {
			t.Errorf("%s: %q", tc.name, got)
		}
	}
	// and a principals.json that says something else
	changed := *pd
	changed.Users = maps.Clone(pd.Users)
	changed.Users["james.gordon"] = "custom-subset"
	if got := reachRoleProblems(snap, &changed, atoms); len(got) == 0 {
		t.Error("principals.json says james.gordon holds another custom role and the check passed")
	}
	changed = *pd
	changed.CustomRoles = maps.Clone(pd.CustomRoles)
	c := changed.CustomRoles["custom-subset"]
	c.Permissions = append(slices.Clone(c.Permissions), "library.delete")
	changed.CustomRoles["custom-subset"] = c
	if got := reachRoleProblems(snap, &changed, atoms); len(got) == 0 {
		t.Error("principals.json gives a custom role another atom and the check passed")
	}
}
