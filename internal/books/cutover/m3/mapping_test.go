// file: internal/books/cutover/m3/mapping_test.go
// version: 1.0.0
// guid: f46a48f5-315b-4d8c-873e-ec8dcaf2d21f
// last-edited: 2026-11-12

package m3

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// The mapping functions are pure functions of a HEAD row and the Config, so
// they are tested without a store: the cutover-level tests in the parent
// package run the same functions over real rows through the runner.

type noAuth struct{ auth.Importer }
type noRoles struct{ rbac.Importer }
type noNative struct{ NativeReader }

var testNow = time.Date(2026, 11, 12, 6, 0, 0, 0, time.UTC)

// testMigration is an M3 over a four-row atom table (a subset of 05-rbac
// §6.1's: the cases below only need a user atom, an editing atom, a core-bound
// atom and the Confining worker atom), the four seed roles and a catalog of
// core's scopes plus books'.
func testMigration(t testing.TB) *Migration {
	t.Helper()
	return testMigrationOver(t, noNative{}, func() storage.Reader { return nil })
}

// testMigrationOver is testMigration over a native reader and a legacy range.
func testMigrationOver(t testing.TB, native NativeReader, legacy func() storage.Reader) *Migration {
	t.Helper()
	specs := rbac.CoreScopes()
	for _, n := range []string{"view", "edit", "reading"} {
		specs = append(specs, rbac.PermissionSpec{Name: rbac.Permission("books:" + n), Title: n, Description: "books " + n})
	}
	specs = append(specs, rbac.PermissionSpec{Name: "books:fingerprint.worker", Title: "worker", Description: "fingerprint worker", Confining: true})
	cat, err := rbac.NewCatalog(specs...)
	if err != nil {
		t.Fatal(err)
	}
	viewer := []rbac.Permission{"core:self", "core:tasks.view", "core:metrics.read", "books:reading", "books:view"}
	m, err := New(Config{
		StepID: "books/identity-cutover",
		Atoms: []AtomRow{
			{Name: "library.view", Scopes: []rbac.Permission{"books:view", "core:tasks.view"}},
			{Name: "library.edit_metadata", Scopes: []rbac.Permission{"books:edit"}},
			{Name: "users.manage", Scopes: []rbac.Permission{"core:users.manage", "core:roles.view"}},
			{Name: "fingerprint.worker", Scopes: []rbac.Permission{"books:fingerprint.worker"}},
		},
		SeedRoles: []SeedRole{
			{ID: "admin", Permissions: []rbac.Permission{rbac.CoreAdmin}},
			{ID: "editor", Permissions: append(slices.Clone(viewer), "books:edit")},
			{ID: "viewer", Permissions: viewer},
			{ID: "fp-worker", Permissions: []rbac.Permission{"books:fingerprint.worker"}},
		},
		Catalog: cat,
		Ambient: []rbac.Permission{"core:self", "core:metrics.read", "books:reading"},
		Auth:    noAuth{}, Roles: noRoles{}, Native: native,
		Legacy: legacy, Now: func() time.Time { return testNow }, KeyTTL: 90 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSlugOf(t *testing.T) {
	long1 := "01M7ZZZ" + strings.Repeat("A", 30) + "ONE"
	long2 := "01M7ZZZ" + strings.Repeat("A", 30) + "TWO"
	for _, c := range []struct {
		id, want string
	}{
		{"01M7ZZZBATGIRL00000000001", "role-01m7zzzbatgirl00000000001"},
		{"01M7ZZZGORDON", "role-01m7zzzgordon"},
		{"Night Wing!", "night-wing"},
		{"a_b-c", "a_b-c"},
		{"7", "role-7"},
		{"", ""},
		{"---", ""},
		{"!!!", ""},
	} {
		if got := slugOf(c.id); got != c.want {
			t.Errorf("slugOf(%q) = %q, want %q", c.id, got, c.want)
		}
	}
	a, b := slugOf(long1), slugOf(long2)
	if a == b {
		t.Errorf("two long IDs that share their first 23 bytes meet at %q", a)
	}
	for _, s := range []string{a, b} {
		if len(s) > 32 {
			t.Errorf("slug %q is %d bytes, over the 32 a role name allows", s, len(s))
		}
	}
	// A slug is a valid custom role name, or the name is "".
	if n := nativeRoleName("01M7ZZZGORDON"); n != "custom.role-01m7zzzgordon" {
		t.Errorf("nativeRoleName = %q", n)
	}
	if n := nativeRoleName("---"); n != "" {
		t.Errorf("nativeRoleName of an unnameable ID = %q, want empty", n)
	}
}

func TestUserStatus(t *testing.T) {
	for _, c := range []struct {
		raw      string
		want     auth.UserStatus
		reported string
	}{
		{"", auth.StatusActive, ""},
		{"active", auth.StatusActive, ""},
		{"  ACTIVE ", auth.StatusActive, ""},
		{"inactive", auth.StatusDisabled, ""},
		{" Inactive", auth.StatusDisabled, ""},
		{"suspended", auth.StatusDisabled, "suspended"},
		{"Locked-Out", auth.StatusDisabled, "locked-out"},
		{strings.Repeat("x", 80), auth.StatusDisabled, strings.Repeat("x", 32)},
	} {
		got, rep := userStatus(c.raw)
		if got != c.want || rep != c.reported {
			t.Errorf("userStatus(%q) = %v, %q; want %v, %q", c.raw, got, rep, c.want, c.reported)
		}
	}
}

func TestKeyStatus(t *testing.T) {
	when := testNow
	var zero time.Time
	for _, c := range []struct {
		raw     string
		revoked *time.Time
		want    auth.KeyStatus
		odd     string
	}{
		{"active", nil, auth.KeyActive, ""},
		{"", nil, auth.KeyActive, ""},
		{" ACTIVE ", nil, auth.KeyActive, ""},
		{"revoked", nil, auth.KeyRevoked, ""},
		{"active", &when, auth.KeyRevoked, ""}, // revoked_at wins over a stale status
		{"inactive", nil, auth.KeyDisabled, ""},
		{"inactive", &when, auth.KeyRevoked, ""},
		{"active", &zero, auth.KeyActive, ""}, // a zero revoked_at is not a revocation
		{"frozen", nil, auth.KeyDisabled, "frozen"},
	} {
		got, odd := keyStatus(c.raw, c.revoked)
		if got != c.want || odd != c.odd {
			t.Errorf("keyStatus(%q, %v) = %v, %q; want %v, %q", c.raw, c.revoked, got, odd, c.want, c.odd)
		}
	}
}

func TestTokenHash(t *testing.T) {
	good := strings.Repeat("ab", 32)
	if h, err := tokenHash(good); err != nil || h[0] != 0xab || h[31] != 0xab {
		t.Errorf("a 64-digit hash: %x, %v", h, err)
	}
	if _, err := tokenHash(" " + strings.ToUpper(good) + "\n"); err != nil {
		t.Errorf("hex is case-insensitive and trimmed: %v", err)
	}
	for name, bad := range map[string]string{
		"empty": "", "short": good[:62], "long": good + "ab", "not hex": strings.Repeat("zz", 32), "all zero": strings.Repeat("00", 32),
	} {
		if _, err := tokenHash(bad); err == nil {
			t.Errorf("%s: accepted as a token hash", name)
		}
	}
}

func TestMapUser(t *testing.T) {
	base := func(mut func(*headUser)) *headUser {
		h := &headUser{
			ID: "01M7ZZZALFRED0000000000001", Username: "alfred", Email: "alfred@wayne.invalid", EmailSource: "admin",
			PasswordHashAlgo: "bcrypt", PasswordHash: "$2a$04$mck", Roles: []string{"viewer"}, Status: "active",
			CredentialEpoch: 4, CreatedAt: testNow.Add(-time.Hour), UpdatedAt: testNow, Version: 9,
		}
		if mut != nil {
			mut(h)
		}
		return h
	}
	roles := []rbac.RoleName{"viewer"}
	u, hash, rep := mapUser(base(nil), roles)
	if u.ID != "01M7ZZZALFRED0000000000001" || u.Username != "alfred" || hash != "$2a$04$mck" || rep != "" ||
		!u.HasPassword || u.CredentialEpoch != 4 || u.Version != 1 || u.Status != auth.StatusActive ||
		!slices.Equal(u.Roles, roles) || !u.CreatedAt.Equal(testNow.Add(-time.Hour)) || !u.UpdatedAt.Equal(testNow) {
		t.Errorf("mapUser = %+v, %q, %q", u, hash, rep)
	}
	for _, c := range []struct {
		name     string
		mut      func(*headUser)
		verified bool
		reset    bool
	}{
		{"admin-sourced address", nil, true, false},
		{"setup", func(h *headUser) { h.EmailSource = "setup" }, true, false},
		{"invite", func(h *headUser) { h.EmailSource = "invite" }, true, false},
		{"idp", func(h *headUser) { h.EmailSource = "idp" }, true, false},
		{"self-declared", func(h *headUser) { h.EmailSource = "profile" }, false, false},
		{"no source", func(h *headUser) { h.EmailSource = "" }, false, false},
		{"no address", func(h *headUser) { h.Email = "" }, false, false},
		{"blank address", func(h *headUser) { h.Email = "   " }, false, false},
		{"blanked hash", func(h *headUser) { h.PasswordHash = "" }, true, true},
		{"oauth user without a hash", func(h *headUser) { h.PasswordHash, h.PasswordHashAlgo = "", "oauth" }, true, false},
		{"system user: no algorithm, no hash", func(h *headUser) { h.PasswordHash, h.PasswordHashAlgo = "", "" }, true, false},
	} {
		u, _, _ := mapUser(base(c.mut), roles)
		if u.EmailVerified != c.verified || u.PasswordResetRequired != c.reset {
			t.Errorf("%s: verified %v reset %v; want %v, %v", c.name, u.EmailVerified, u.PasswordResetRequired, c.verified, c.reset)
		}
		if c.name == "blanked hash" && u.HasPassword {
			t.Errorf("a blanked hash is HasPassword")
		}
	}
	if u, _, rep := mapUser(base(func(h *headUser) { h.Status = "suspended" }), roles); u.Status != auth.StatusDisabled || rep != "suspended" {
		t.Errorf("an odd status: %v, %q", u.Status, rep)
	}
}

func TestMapKey(t *testing.T) {
	m := testMigration(t)
	hex64 := strings.Repeat("cd", 32)
	scopes := []rbac.Permission{"books:view"}
	live := &headKey{ID: "01M7ZZZKEYLIVE000000000001", UserID: "01M7ZZZALFRED0000000000001", Name: "bat-signal", TokenHash: hex64, Status: "active", CreatedAt: testNow.Add(-time.Hour), UseCount: 7}

	k, hash, notes, err := m.mapKey(live, scopes)
	if err != nil || hash[0] != 0xcd || !k.Imported || k.Version != 1 || k.UseCount != 7 || k.Owner != "01M7ZZZALFRED0000000000001" || k.Status != auth.KeyActive {
		t.Fatalf("mapKey = %+v, %x, %v", k, hash, err)
	}
	if !notes.stamped || !k.ExpiresAt.Equal(testNow.Add(90*24*time.Hour)) {
		t.Errorf("a live key with no expiry gets now + the default lifetime: stamped %v, expires %v", notes.stamped, k.ExpiresAt)
	}

	exp := testNow.Add(24 * time.Hour)
	withExp := *live
	withExp.ExpiresAt = &exp
	if k, _, notes, _ := m.mapKey(&withExp, scopes); !k.ExpiresAt.Equal(exp) || notes.stamped {
		t.Errorf("a key that has an expiry keeps it: %v stamped %v", k.ExpiresAt, notes.stamped)
	}

	revAt := testNow.Add(-24 * time.Hour)
	revoked := *live
	revoked.Status, revoked.RevokedAt = "revoked", &revAt
	if k, _, notes, _ := m.mapKey(&revoked, scopes); k.Status != auth.KeyRevoked || !k.RevokedAt.Equal(revAt) || !k.ExpiresAt.IsZero() || notes.stamped {
		t.Errorf("a revoked key keeps its revoked_at and is not stamped: %+v stamped %v", k, notes.stamped)
	}
	noWhen := *live
	noWhen.Status = "revoked"
	if k, _, _, _ := m.mapKey(&noWhen, scopes); k.Status != auth.KeyRevoked || !k.RevokedAt.Equal(testNow) {
		t.Errorf("a revoked key with no revoked_at is revoked as of the cutover: %v", k.RevokedAt)
	}
	odd := *live
	odd.Status = "frozen"
	if k, _, notes, _ := m.mapKey(&odd, scopes); k.Status != auth.KeyDisabled || notes.oddStatus != "frozen" {
		t.Errorf("an odd status: %v, %q", k.Status, notes.oddStatus)
	}
	last := testNow.Add(-2 * time.Hour)
	seen := *live
	seen.LastUsedAt, seen.LastUsedIP = &last, "192.0.2.7"
	if k, _, _, _ := m.mapKey(&seen, scopes); !k.LastUsedAt.Equal(last) || k.LastUsedIP.String() != "192.0.2.7" {
		t.Errorf("last use is carried: %v %v", k.LastUsedAt, k.LastUsedIP)
	}
	seen.LastUsedIP = "not an address"
	if k, _, _, err := m.mapKey(&seen, scopes); err != nil || k.LastUsedIP.IsValid() {
		t.Errorf("an unparseable last-used address is dropped, not a failure: %v %v", k.LastUsedIP, err)
	}
	bad := *live
	bad.TokenHash = "nope"
	if _, _, _, err := m.mapKey(&bad, scopes); err == nil {
		t.Errorf("a key with no usable token hash was mapped")
	}
}

// TestKeyScopes (05-rbac §6.3): the effective set at the cutover is the key's
// atoms intersected with its owner's roles, plus the ambient scopes unless
// that set is Confined; only an admin owner's every-atom or empty-list key
// gets core:admin.
func TestKeyScopes(t *testing.T) {
	m := testMigration(t)
	viewer := m.seeds["viewer"].Permissions
	editor := m.seeds["editor"].Permissions
	ambient := []string{"books:reading", "core:metrics.read", "core:self"}
	with := func(scopes ...string) []string {
		out := append(slices.Clone(scopes), ambient...)
		slices.Sort(out)
		return slices.Compact(out)
	}
	every := []string{"books:edit", "books:fingerprint.worker", "books:view", "core:roles.view", "core:tasks.view", "core:users.manage"}
	all := []string{"library.view", "library.edit_metadata", "users.manage", "fingerprint.worker"}
	for _, c := range []struct {
		name    string
		atoms   []string
		owner   []rbac.Permission
		want    []string
		unknown []string
	}{
		{"admin owner, empty list: core:admin and every row's scopes", nil, []rbac.Permission{rbac.CoreAdmin}, with(append([]string{"core:admin"}, every...)...), nil},
		{"admin owner, every atom: the same", all, []rbac.Permission{rbac.CoreAdmin}, with(append([]string{"core:admin"}, every...)...), nil},
		{"admin owner, one atom: that atom's scopes only", []string{"library.view"}, []rbac.Permission{rbac.CoreAdmin}, with("books:view", "core:tasks.view"), nil},
		{"admin owner, all but one atom: no core:admin", all[1:], []rbac.Permission{rbac.CoreAdmin}, with("books:edit", "core:roles.view", "core:users.manage", "books:fingerprint.worker"), nil},
		{"viewer owner, empty list: the owner's set", nil, viewer, with("books:view", "core:tasks.view"), nil},
		{"viewer owner, every atom: cut to the owner's set, never core:admin", all, viewer, with("books:view", "core:tasks.view"), nil},
		{"editor owner, an atom the owner holds partly", []string{"library.edit_metadata", "users.manage"}, editor, with("books:edit"), nil},
		{"viewer owner, a core-bound atom the owner lacks: ambient only", []string{"users.manage"}, viewer, with(), nil},
		{"worker owner, empty list: confined, no ambient", nil, m.seeds["fp-worker"].Permissions, []string{"books:fingerprint.worker"}, nil},
		{"worker owner, worker plus a user atom: still confined", []string{"fingerprint.worker", "library.view"}, m.seeds["fp-worker"].Permissions, []string{"books:fingerprint.worker"}, nil},
		{"unknown atom beside a known one: reported, grants nothing", []string{"library.view", "batcave.access"}, viewer, with("books:view", "core:tasks.view"), []string{"batcave.access"}},
		{"every atom unknown: ambient only, never an empty list", []string{"batcave.access"}, viewer, with(), []string{"batcave.access"}},
		{"a repeated atom counts once", []string{"library.view", "library.view"}, viewer, with("books:view", "core:tasks.view"), nil},
	} {
		got, unknown, err := m.keyScopes(c.atoms, c.owner)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		gs := make([]string, len(got))
		for i, s := range got {
			gs[i] = string(s)
		}
		slices.Sort(gs)
		want := slices.Clone(c.want)
		slices.Sort(want)
		if !slices.Equal(gs, want) {
			t.Errorf("%s:\n got %v\nwant %v", c.name, gs, want)
		}
		if !slices.Equal(unknown, c.unknown) {
			t.Errorf("%s: unknown %v, want %v", c.name, unknown, c.unknown)
		}
		if len(got) == 0 {
			t.Errorf("%s: an empty scope list", c.name)
		}
		if !rbac.GrantsAdmin(c.owner) && slices.Contains(got, rbac.CoreAdmin) {
			t.Errorf("%s: a non-admin owner's key got core:admin", c.name)
		}
	}
}

// TestVerdictOf: a seed role is deleted, a custom one imported under
// custom.<slug> with the ambient scopes, a taken name or an all-unknown role
// quarantined, never merged into another.
func TestVerdictOf(t *testing.T) {
	m := testMigration(t)
	role := func(id string, perms ...string) *headRole {
		return &headRole{ID: id, Name: "Role " + id, Permissions: perms}
	}
	for _, id := range []string{"admin", "editor", "viewer", "fp-worker"} {
		if v := m.verdictOf(role(id, "library.view"), nil); v.kind != verdictSeed || string(v.name) != id {
			t.Errorf("seed role %s: %+v", id, v)
		}
	}
	v := m.verdictOf(role("01M7ZZZDETECTIVE0000000001", "library.view", "batcave.access"), nil)
	if v.kind != verdictImport || v.name != "custom.role-01m7zzzdetective0000000001" {
		t.Fatalf("custom role: %+v", v)
	}
	if !slices.Equal(v.unknown, []string{"batcave.access"}) {
		t.Errorf("unknown atoms = %v", v.unknown)
	}
	for _, a := range []rbac.Permission{"core:self", "core:metrics.read", "books:reading", "books:view", "core:tasks.view"} {
		if !slices.Contains(v.perms, a) {
			t.Errorf("custom role lacks %s: %v", a, v.perms)
		}
	}
	if v := m.verdictOf(role("01M7ZZZDETECTIVE0000000001", "batcave.access"), nil); v.kind != verdictQuarantine || !strings.Contains(v.reason, "unknown") {
		t.Errorf("an all-unknown role: %+v", v)
	}
	if v := m.verdictOf(role("01M7ZZZDETECTIVE0000000001"), nil); v.kind != verdictImport {
		t.Errorf("a role with no permissions is imported as it was: %+v", v)
	}
	if v := m.verdictOf(role("01M7ZZZDETECTIVE0000000001", "library.view"), func(rbac.RoleName) bool { return true }); v.kind != verdictQuarantine || !strings.Contains(v.reason, "taken") {
		t.Errorf("a taken name: %+v", v)
	}
	if v := m.verdictOf(role("---", "library.view"), nil); v.kind != verdictQuarantine {
		t.Errorf("an ID with no slug: %+v", v)
	}
}

func TestOwnerPerms(t *testing.T) {
	m := testMigration(t)
	lookup := func(_ context.Context, n rbac.RoleName) ([]rbac.Permission, bool, error) {
		if n == "custom.role-x" {
			return []rbac.Permission{"books:edit"}, true, nil
		}
		return nil, false, nil
	}
	got, err := m.ownerPerms(context.Background(), []rbac.RoleName{"viewer", "custom.role-x", "custom.role-missing"}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, "books:view") || !slices.Contains(got, "books:edit") || slices.Contains(got, rbac.CoreAdmin) {
		t.Errorf("owner perms = %v", got)
	}
}

func TestNewRefusesAnIncompleteConfig(t *testing.T) {
	good := testMigration(t).cfg
	for name, mut := range map[string]func(*Config){
		"no step ID":        func(c *Config) { c.StepID = "" },
		"no atoms":          func(c *Config) { c.Atoms = nil },
		"no catalog":        func(c *Config) { c.Catalog = nil },
		"no auth importer":  func(c *Config) { c.Auth = nil },
		"no role importer":  func(c *Config) { c.Roles = nil },
		"no native reader":  func(c *Config) { c.Native = nil },
		"no legacy range":   func(c *Config) { c.Legacy = nil },
		"no clock":          func(c *Config) { c.Now = nil },
		"no key lifetime":   func(c *Config) { c.KeyTTL = 0 },
		"no ambient scopes": func(c *Config) { c.Ambient = nil },
		"a scopeless atom":  func(c *Config) { c.Atoms = append(slices.Clone(c.Atoms), AtomRow{Name: "empty"}) },
		"a repeated atom":   func(c *Config) { c.Atoms = append(slices.Clone(c.Atoms), c.Atoms[0]) },
		"no admin seed":     func(c *Config) { c.SeedRoles = c.SeedRoles[1:] },
		"a nameless seed":   func(c *Config) { c.SeedRoles = append(slices.Clone(c.SeedRoles), SeedRole{}) },
	} {
		cfg := good
		mut(&cfg)
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: New accepted it", name)
		}
	}
}
