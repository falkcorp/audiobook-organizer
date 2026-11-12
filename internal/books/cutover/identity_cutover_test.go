// file: internal/books/cutover/identity_cutover_test.go
// version: 1.0.0
// guid: 604a0bfc-8e8b-4b0c-b7cd-aa1c0789a9f1
// last-edited: 2026-11-12

package cutover

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// identityFamilies are the legacy prefixes M3's Source drains.
var identityFamilies = []string{"u:", "role:", "apikey:", "invite:", "inviteallow:", "oauthid:", "sess:",
	"idx:user:", "idx:role:name:", "idx:apikey:", "idx:invite:username:", "idx:oauth:", "idx:sess:user:",
	"setting:bootstrap_token_hash", "setting:bootstrap_token_expires_at", "setting:system:migration:apikey_expiry_stamp_v1_done"}

// TestIdentityCutover_FixtureFromR0 (09 §9.2.3, 05-storage §5S.3.13): M3 over
// the R0-written corpus (F2-12's stage 1, F2-13's overlay and the terminal
// merge store) completes through the kernel's runner and its P4a preflight:
// every user, role, key and identity ID is kept, an abk_ key keeps its stored
// SHA-256, no identity row is quarantined, no row of an identity family or the
// bootstrap and expiry settings is left, the ABS sessions are untouched and
// every raw session or invite token is gone from the live store.
func TestIdentityCutover_FixtureFromR0(t *testing.T) {
	for _, name := range []string{"stage1", "overlay", "blockers/blocker-merge-terminal"} {
		t.Run(name, func(t *testing.T) {
			r := newIDRig(t, name)
			before := r.before(t)
			snap, err := r.run(t)
			if err != nil {
				t.Fatalf("M3: %v\n%s", err, r.logs.String())
			}
			if snap.State != "done" {
				t.Fatalf("the step's state is %q, want done", snap.State)
			}
			if len(snap.Verify.Failures) > 0 {
				t.Errorf("Verify failed: %v", snap.Verify.Failures)
			}
			for k := range snap.Legacy {
				for _, p := range identityFamilies {
					if strings.HasPrefix(k, p) {
						t.Errorf("legacy row %q is left behind", k)
					}
				}
			}
			// IDs kept.
			if got, want := mapKeys(snap.Users), legacyIDs(before, "u:"); !slices.Equal(got, want) {
				t.Errorf("users %v, want HEAD's %v", got, want)
			}
			if got, want := mapKeys(snap.Keys), legacyIDs(before, "apikey:"); !slices.Equal(got, want) {
				t.Errorf("keys %v, want HEAD's %v", got, want)
			}
			// Every key verifies by its stored SHA-256.
			for id, sk := range snap.Keys {
				var head struct {
					TokenHash string `json:"token_hash"`
				}
				if err := json.Unmarshal(before["apikey:"+id], &head); err != nil {
					t.Fatal(err)
				}
				if sk.Hash != head.TokenHash || len(sk.Hash) != 64 {
					t.Errorf("key %s keeps hash %q, want HEAD's token hash", id, sk.Hash)
				}
				if !sk.Key.Imported {
					t.Errorf("key %s is not marked imported", id)
				}
				if len(sk.Key.Scopes) == 0 {
					t.Errorf("key %s has no scopes", id)
				}
			}
			// No raw session or invite token survives; the ABS sessions are untouched.
			if hasPrefix(snap.Legacy, "sess:", "invite:") {
				t.Errorf("a session or invite row survives")
			}
			absBefore, absAfter := 0, 0
			for k, v := range before {
				if strings.HasPrefix(k, "abs_sess:") {
					absBefore++
					if got, ok := snap.Legacy[k]; !ok || string(got) != string(v) {
						t.Errorf("ABS session %s changed", k)
					}
				}
			}
			for k := range snap.Legacy {
				if strings.HasPrefix(k, "abs_sess:") {
					absAfter++
				}
			}
			if absBefore == 0 || absBefore != absAfter {
				t.Errorf("ABS sessions: %d before, %d after (the corpus has some, and M3 leaves them)", absBefore, absAfter)
			}
			// No identity row is quarantined, whatever the cell.
			for _, q := range snap.Quarantine {
				for _, p := range []string{"u:", "apikey:", "oauthid:", "idx:"} {
					if strings.HasPrefix(q.Key, p) {
						t.Errorf("identity row %q was quarantined: %s", q.Key, q.Reason)
					}
				}
			}
		})
	}
}

// TestIdentityCutover_OverlayCells: the overlay's M3 cells, each against its
// manifest answer (internal/books/testdata/r2corpus/manifest.json).
func TestIdentityCutover_OverlayCells(t *testing.T) {
	r := newIDRig(t, "overlay")
	snap, err := r.run(t)
	if err != nil {
		t.Fatalf("M3: %v\n%s", err, r.logs.String())
	}
	t.Run("quarantine", func(t *testing.T) {
		// A role is never a blocker: the one that does not decode, the one whose
		// every atom is unknown and the allow-list row that does not decode go to
		// quarantine; nothing else does.
		var got []string
		for _, q := range snap.Quarantine {
			got = append(got, q.Key)
			if q.Sealed {
				t.Errorf("%s is sealed: M3 quarantines no credential", q.Key)
			}
		}
		want := sortedStrings("role:"+roleCorrupt, "role:"+roleAllUnk, "inviteallow:owlman@court-of-owls.invalid")
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("quarantined %v, want %v", got, want)
		}
	})
	t.Run("oauth identity and allow list", func(t *testing.T) {
		id, ok := snap.Idents["github/oracle-clocktower-1989"]
		if !ok || id.UserID != auth.UserID(uBarbara) {
			t.Errorf("the OAuth identity is %+v (present %v), want github/oracle-clocktower-1989 linked to barbara.gordon", id, ok)
		}
		if want := []string{"alfred@wayne-manor.invalid"}; !slices.Equal(snap.Allow, want) {
			t.Errorf("allow-list %v, want %v (the other row is quarantined)", snap.Allow, want)
		}
	})
	t.Run("user statuses", func(t *testing.T) {
		for _, c := range []struct {
			id   string
			want auth.UserStatus
			why  string
		}{
			{"01M731PBFQ1JHQXGS79HP2APC6", auth.StatusActive, "' Active' is trimmed and folded (HEAD's UserIsActive)"},
			{"01M731PBKB28CMAFBQ6J83X62R", auth.StatusDisabled, "'DISABLED' is not 'inactive' and not active: disabled, reported"},
			{"01M731PBNXXRVYB143XNKPMBAD", auth.StatusActive, "an empty status is active at HEAD"},
			{"01M731PBRESVTDD6P9GZSH0BQ1", auth.StatusDisabled, "'inactive ' trims to inactive"},
			{"01M731PBTZZDNZ75S7E4KKE1N2", auth.StatusDisabled, "'suspended' is not a HEAD status: disabled, reported"},
			{uBarbara, auth.StatusActive, "an ordinary active user"},
		} {
			if got := snap.Users[c.id].User.Status; got != c.want {
				t.Errorf("user %s is %q, want %q: %s", c.id, got, c.want, c.why)
			}
		}
		for _, name := range []string{"oswald.cobblepot", "victor.fries"} {
			if !strings.Contains(r.logs.String(), "status is not one HEAD defines") {
				t.Errorf("no report of %s's unknown status", name)
			}
		}
		if u := snap.Users["01M731PBRESVTDD6P9GZSH0BQ1"].User; u.Status != auth.StatusDisabled {
			t.Errorf("pamela.isley is %q", u.Status)
		}
	})
	t.Run("key statuses and expiry", func(t *testing.T) {
		for _, c := range []struct {
			id   string
			want auth.KeyStatus
		}{
			{kPadActive, auth.KeyActive}, {kUpperInact, auth.KeyDisabled}, {kEmptyStatus, auth.KeyActive},
			{kPadRevoked, auth.KeyRevoked}, {kRevokedAt, auth.KeyRevoked}, {kPaused, auth.KeyDisabled}, {kNoExpiry, auth.KeyActive},
		} {
			if got := snap.Keys[c.id].Key.Status; got != c.want {
				t.Errorf("key %s is %q, want %q", c.id, got, c.want)
			}
		}
		k := snap.Keys[kNoExpiry].Key
		if want := idNow.Add(idKeyTTL); !k.ExpiresAt.Equal(want) {
			t.Errorf("a live key with no expiry is stamped %v, want cutover time plus the default TTL %v", k.ExpiresAt, want)
		}
		if exp := snap.Keys[kScopedView].Key.ExpiresAt; exp.Equal(idNow.Add(idKeyTTL)) {
			t.Errorf("a key that has an expiry is restamped")
		}
		// A revoked key keeps its expiry and gains none.
		if k := snap.Keys[kPadRevoked].Key; k.ExpiresAt.IsZero() || k.RevokedAt.IsZero() {
			t.Errorf("a revoked key lost its expiry or has no revocation time: %+v", k)
		}
	})
	t.Run("credential rows", func(t *testing.T) {
		// A user keeps the stored hash as it is and a password-less account stays
		// password-less (the _system user), never marked for a reset.
		if u := snap.Users[uBarbara]; !u.User.HasPassword || !strings.HasPrefix(u.Hash, "$argon2id$") || u.User.PasswordResetRequired {
			t.Errorf("barbara.gordon: has password %v, hash prefix ok %v, reset required %v", u.User.HasPassword, strings.HasPrefix(u.Hash, "$argon2id$"), u.User.PasswordResetRequired)
		}
		if u := snap.Users[uSystem]; u.User.HasPassword || u.Hash != "" || u.User.PasswordResetRequired {
			t.Errorf("_system: has password %v, hash %q, reset required %v", u.User.HasPassword, u.Hash, u.User.PasswordResetRequired)
		}
		if u := snap.Users["01M731PC0GT903F98P9WA7P2EP"].User; !u.EmailVerified || u.Email != "alfred@wayne-manor.invalid" {
			t.Errorf("alfred.pennyworth's invited address is %q, verified %v: an invite-sourced address is verified", u.Email, u.EmailVerified)
		}
		if u := snap.Users[uBarbara].User; u.EmailVerified {
			t.Errorf("a user with no address is not verified")
		}
	})
	t.Run("bootstrap and expiry rows", func(t *testing.T) {
		for _, k := range []string{"setting:bootstrap_token_hash", "setting:bootstrap_token_expires_at", "setting:system:migration:apikey_expiry_stamp_v1_done"} {
			if _, ok := snap.Legacy[k]; ok {
				t.Errorf("%s survives M3", k)
			}
		}
	})
}

// TestIdentityCutover_RolesAndKeyScopes (05-rbac §6.3, 05-authn §11): custom
// roles become custom.<slug> with their atoms' native scopes plus the ambient
// scopes and never core:admin; seed roles' rows are deleted, not imported; a
// key's scopes are its effective set intersected with its owner's role set
// plus the ambient scopes; only an admin owner's every-atom or empty-list key
// gets core:admin and every row's scopes.
func TestIdentityCutover_RolesAndKeyScopes(t *testing.T) {
	r := newIDRig(t, "overlay")
	snap, err := r.run(t)
	if err != nil {
		t.Fatalf("M3: %v\n%s", err, r.logs.String())
	}
	t.Run("roles", func(t *testing.T) {
		want := map[string][]string{
			string(nativeRoleName(roleEvery)):   withAmbientScopes(everyScope()...),
			string(nativeRoleName(roleSubset)):  withAmbientScopes("books:view", "core:tasks.view", "books:edit", "books:collections.manage", "books:request"),
			string(nativeRoleName(roleUnknown)): withAmbientScopes("books:view", "core:tasks.view", "books:playlists.create"),
		}
		if got := mapKeys(snap.Roles); !slices.Equal(got, mapKeys(want)) {
			t.Fatalf("native roles %v, want %v: seed roles are deleted, a role of only unknown atoms or one that does not decode is quarantined", got, mapKeys(want))
		}
		for name, w := range want {
			role := snap.Roles[name]
			if got := scopeStrings(role.Added); !slices.Equal(got, w) {
				t.Errorf("role %s holds %v, want %v", name, got, w)
			}
			if rbac.GrantsAdmin(role.Added) {
				t.Errorf("role %s grants core:admin", name)
			}
			if role.Origin != rbac.OriginCustom {
				t.Errorf("role %s origin %v", name, role.Origin)
			}
		}
		// The three events: core.role.create, imported, one per role, in the
		// chunk that imported it.
		wantEvents := sortedStrings("core.role.create "+string(nativeRoleName(roleEvery)), "core.role.create "+string(nativeRoleName(roleSubset)), "core.role.create "+string(nativeRoleName(roleUnknown)))
		if !slices.Equal(snap.Events, wantEvents) {
			t.Errorf("audit events %v, want %v", snap.Events, wantEvents)
		}
	})
	t.Run("users' role lists", func(t *testing.T) {
		for _, c := range []struct {
			id   string
			want []rbac.RoleName
		}{
			{uJamesGordon, []rbac.RoleName{nativeRoleName(roleEvery)}},
			{uHarleen, []rbac.RoleName{nativeRoleName(roleSubset)}},
			{uLuciusJr, []rbac.RoleName{nativeRoleName(roleUnknown)}},
			{uTalia, []rbac.RoleName{"viewer"}}, // the quarantined role's entry is dropped
			{uUbu, nil},                         // its only role was quarantined
			{uAdmin, []rbac.RoleName{"admin"}},
			{uDick, []rbac.RoleName{"editor"}},
			{uWorker, []rbac.RoleName{"fp-worker"}},
		} {
			got := snap.Users[c.id].User.Roles
			if len(got) == 0 && len(c.want) == 0 {
				continue
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("user %s has roles %v, want %v", c.id, got, c.want)
			}
		}
	})
	t.Run("key scopes", func(t *testing.T) {
		admin := append([]string{"core:admin"}, everyScope()...)
		for _, c := range []struct {
			id, why string
			want    []string
		}{
			{kEveryAdmin, "an admin owner's every-atom key: core:admin and every row", withAmbientScopes(admin...)},
			{kEmptyAdmin, "an admin owner's empty-list key: the same", withAmbientScopes(admin...)},
			{kBootstrap, "the revoked bootstrap key is imported with its reach, revoked", withAmbientScopes(admin...)},
			{kScopedView, "an admin owner's key of one atom is that atom's scopes, not core:admin", withAmbientScopes("books:view", "core:tasks.view")},
			{kEditorOwn, "an editor's key: its atoms are in the owner's set", withAmbientScopes("books:view", "core:tasks.view", "books:playlists.create")},
			{kWorker, "a worker's key is Confining only: no ambient scopes", []string{"books:fingerprint.worker"}},
			{kOutsideOwner, "atoms outside the owner's roles: the empty intersection keeps only the ambient scopes", withAmbientScopes()},
			{kEveryCustom, "every atom under a custom every-atom role: the union, never core:admin", withAmbientScopes(everyScope()...)},
			{kEmptyCustom, "an empty list under a custom role: the owner's native role set", withAmbientScopes("books:view", "core:tasks.view", "books:edit", "books:collections.manage", "books:request")},
		} {
			got := scopeStrings(snap.Keys[c.id].Key.Scopes)
			if !slices.Equal(got, c.want) {
				t.Errorf("key %s: %s\n got %v\nwant %v", c.id, c.why, got, c.want)
			}
		}
	})
	t.Run("invariants over every key", func(t *testing.T) {
		for id, sk := range snap.Keys {
			if len(sk.Key.Scopes) == 0 {
				t.Errorf("key %s has no scopes", id)
			}
			if rbac.GrantsAdmin(sk.Key.Scopes) {
				owner := snap.Users[string(sk.Key.Owner)].User
				if !slices.Contains(owner.Roles, "admin") {
					t.Errorf("key %s holds core:admin but its owner %s is not an admin (roles %v)", id, owner.Username, owner.Roles)
				}
			}
		}
		for name, role := range snap.Roles {
			if len(role.Added) == 0 {
				t.Errorf("role %s has no permissions", name)
			}
		}
	})
}

// TestIdentityCutover_WorkerKeyStaysConfined (05-rbac §6.3, R10-2, SEC10-1): a
// key whose effective set is only Confining scopes never gains the ambient
// scopes, whoever owns it. Here the imported worker key's checker input is
// Confined, holds no core:self, core:metrics.read or books:reading, and
// satisfies none of the user routes' requirements, only the worker's. The
// live-route cell lands with F4-08.
func TestIdentityCutover_WorkerKeyStaysConfined(t *testing.T) {
	const (
		kWorkerEmpty = "01M7ZZZWORKEREMPTY00000001"
		kWorkerMixed = "01M7ZZZWORKERMIXED00000002"
	)
	r := newIDRig(t, "overlay", idRows(rowsOf(
		headKeyRows(kWorkerEmpty, uWorker, "mck-bat-signal-empty", nil, "active"),
		headKeyRows(kWorkerMixed, uWorker, "mck-bat-signal-mixed", []string{"fingerprint.worker", "library.view"}, "active"),
	)...))
	snap, err := r.run(t)
	if err != nil {
		t.Fatalf("M3: %v\n%s", err, r.logs.String())
	}
	cat := idCatalog(t)
	for _, c := range []struct{ id, why string }{
		{kWorker, "the corpus's worker key"},
		{kWorkerEmpty, "an empty list is the owner's set, which is the worker scope only"},
		{kWorkerMixed, "a worker's key naming a user atom is cut to the owner's set"},
	} {
		scopes := snap.Keys[c.id].Key.Scopes
		if got := scopeStrings(scopes); !slices.Equal(got, []string{"books:fingerprint.worker"}) {
			t.Errorf("%s: scopes %v, want only books:fingerprint.worker", c.why, got)
		}
		if !rbac.Confined(cat, scopes) {
			t.Errorf("%s: the imported key is not Confined", c.why)
		}
		for _, userRoute := range []rbac.Permission{"core:self", "core:metrics.read", "books:reading", "books:view", "core:tasks.view"} {
			if slices.Contains(scopes, userRoute) {
				t.Errorf("%s: holds %s, which a user route's requirement is satisfied by", c.why, userRoute)
			}
		}
		if !slices.Contains(scopes, "books:fingerprint.worker") {
			t.Errorf("%s: does not satisfy the worker routes' AllOf(books:fingerprint.worker)", c.why)
		}
	}
	// A key of a non-worker owner is not Confined, so it does carry the ambient scopes.
	if rbac.Confined(cat, snap.Keys[kScopedView].Key.Scopes) {
		t.Errorf("a user's key reads as Confined")
	}
}

// TestUnknownAtom_ReportedNotGranted (05-rbac §6.3, SEC4-13): an atom outside
// the table grants nothing and is reported by role ID or key ID; a role whose
// every atom is unknown is quarantined with the entries naming it; a key whose
// every atom is unknown is imported with the ambient scopes only, never an
// empty list and never quarantined.
func TestUnknownAtom_ReportedNotGranted(t *testing.T) {
	const kUnknown = "01M7ZZZKEYUNKNOWNATOM000003"
	r := newIDRig(t, "overlay", idRows(headKeyRows(kUnknown, uBarbara, "mck-batcave-access", []string{"batcave.access"}, "active")...))
	snap, err := r.run(t)
	if err != nil {
		t.Fatalf("M3: %v\n%s", err, r.logs.String())
	}
	logs := r.logs.String()
	// The role naming one unknown atom: imported, the atom granted nothing, reported by role ID.
	role := snap.Roles[string(nativeRoleName(roleUnknown))]
	for _, s := range role.Added {
		if strings.Contains(string(s), "batcave") {
			t.Errorf("the unknown atom granted %s", s)
		}
	}
	if !strings.Contains(logs, "role="+roleUnknown) || !strings.Contains(logs, "batcave.access") {
		t.Errorf("the role %s naming batcave.access is not reported", roleUnknown)
	}
	// The all-unknown role: quarantined with its holders' entries dropped.
	if _, ok := snap.Roles[string(nativeRoleName(roleAllUnk))]; ok {
		t.Errorf("the all-unknown role was imported")
	}
	if !strings.Contains(logs, "role="+roleAllUnk) {
		t.Errorf("the quarantined role %s is not reported", roleAllUnk)
	}
	if got := snap.Users[uUbu].User.Roles; len(got) != 0 {
		t.Errorf("ubu still holds %v", got)
	}
	// The key naming only an unknown atom.
	k, ok := snap.Keys[kUnknown]
	if !ok {
		t.Fatalf("the key naming only an unknown atom was not imported (quarantined or blocked)")
	}
	if got := scopeStrings(k.Key.Scopes); !slices.Equal(got, withAmbientScopes()) {
		t.Errorf("the key holds %v, want only the ambient scopes", got)
	}
	if !strings.Contains(logs, kUnknown) {
		t.Errorf("the key %s naming an unknown atom is not reported", kUnknown)
	}
}
