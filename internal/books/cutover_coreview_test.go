// file: internal/books/cutover_coreview_test.go
// version: 0.1.0
// guid: 128edfe3-aaf1-49eb-bce8-3c2baf3a2d83
// last-edited: 2026-11-27

package books

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/keys"
)

// Core's identity records as M3 writes them, read from a scanned store
// (storeScan.coreIdentity). Core exports no reader over a closed store: its
// auth and role stores are core/internal, and the kernel's late importers read
// users and roles only, and only while a boot runs. So the suite decodes the
// records at the layout core/internal/authstore and core/internal/rbacstore
// document (keys under c/auth/ and c/rbac/, tuple-encoded by storage/keys;
// values codec.JSON[T](1) of core's type), with the same key builders those
// stores use, as seIdentityRows reads identities.
const (
	coreAuthKeyspace = "c/auth/"
	coreRBACKeyspace = "c/rbac/"
)

// coreUserKey is authstore's userKey (user/<id>) under c/auth/.
func coreUserKey(id string) string {
	return coreAuthKeyspace + string(keys.Join(keys.Str("user"), keys.Str(id)))
}

// coreRoleKey is rbacstore's roleKey (role/<name>) under c/rbac/.
func coreRoleKey(name string) string {
	return coreRBACKeyspace + string(keys.Join(keys.Str("role"), keys.Str(name)))
}

// coreIdentityKey is authstore's identityKey (identity/<provider>/<subject>)
// under c/auth/.
func coreIdentityKey(provider, subject string) string {
	return coreAuthKeyspace + string(keys.Join(keys.Str("identity"), keys.Str(provider), keys.Str(subject)))
}

// coreRecord decodes the core record at key in s: ok is false when there is
// none. A record that does not decode is an error, never "absent".
func coreRecord[T any](s *storeScan, key string) (v T, ok bool, err error) {
	raw, ok := s.coreIdentity[key]
	if !ok {
		return v, false, nil
	}
	if v, err = codec.JSON[T](1).Decode(raw); err != nil {
		return v, true, fmt.Errorf("the core record %q does not decode: %w", key, err)
	}
	return v, true, nil
}

// customRoleName is the native name of a HEAD custom role ID: custom.<slug of
// its ID> (05-rbac §6.3; 03's role-ID row; the overlay's role cases). The slug
// of a ULID, the form of every custom role ID HEAD mints, is "role-" and the ID
// lower-cased (a slug starts with a letter). An ID of another form is not
// mapped here, so it stays gone unless a core key holds it whole.
func customRoleName(id string) (string, bool) {
	if !isULID(id) {
		return "", false
	}
	return "custom.role-" + strings.ToLower(id), true
}

// isULID reports a 26-character Crockford base32 ID, upper case.
func isULID(s string) bool {
	if len(s) != 26 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", r) {
			return false
		}
	}
	return true
}

// headUserRow and headIdentityRow are the fields of HEAD's u: and oauthid:
// rows the ID check reads.
type headUserRow struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Email     string    `json:"email"`
	Status    string    `json:"status"`
	Roles     []string  `json:"roles"`
	CreatedAt time.Time `json:"created_at"`
}

type headIdentityRow struct {
	Provider string `json:"provider"`
	Subject  string `json:"subject"`
	UserID   string `json:"user_id"`
}

// headSystemUser is HEAD's _system row of the before-store: the one u: row
// with status system and username _system, M3's rule (systemuser.go), which
// imports it under core's system user ID. ok is false when the store has none.
// More than one such row is a problem: M3 refuses that store, so a cutover
// that ran on it is wrong whatever the IDs say.
func headSystemUser(before *storeScan) (row headUserRow, ok bool, problem string) {
	var found []headUserRow
	for _, k := range sortedKeys(before.identityRows) {
		if !strings.HasPrefix(k, "u:") {
			continue
		}
		var u headUserRow
		if json.Unmarshal(before.identityRows[k], &u) != nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(u.Status), string(coreauth.StatusSystem)) &&
			coreauth.FoldUsername(u.Username) == string(coreauth.SystemUserID) {
			found = append(found, u)
		}
	}
	switch len(found) {
	case 0:
		return headUserRow{}, false, ""
	case 1:
		return found[0], true, ""
	}
	return headUserRow{}, false, fmt.Sprintf("the before-store holds %d system user rows: M3 refuses such a store", len(found))
}

// coreOwnerOf is the core user ID M3 imports a HEAD owner ID under: the system
// row's ID becomes core's system user ID, every other ID is kept.
func coreOwnerOf(headID string, sys headUserRow, hasSys bool) string {
	if hasSys && headID == sys.ID {
		return string(coreauth.SystemUserID)
	}
	return headID
}

// identityIDKept reports an ID of the before-store that M3 keeps in core's
// form, looked up where M3 writes it (the ID check's second pass, after
// idResolves found neither the books row, a whole core key token nor a
// quarantine record):
//
//   - a role ID: core's role custom.<slug of the ID> (customRoleName);
//   - an OAuth identity ID: core's identity (provider, subject) of the HEAD
//     row, linked to the row's owner as M3 imports it (coreOwnerOf). Core's
//     identity records carry no ID of their own (05-authn §11), and 09 §9.2.3's
//     kept-ID list names no OAuth identity;
//   - a user ID: only HEAD's _system row's (headSystemUser), which M3 imports
//     under core's system user ID; core's record must be that row's import
//     (status system, the row's username, e-mail and creation time), not the
//     system user core's boot seeds when there is none. No other user is
//     remapped.
//
// problem is a record that does not decode or a store M3 refuses.
func identityIDKept(kind, id string, before, after *storeScan) (kept bool, problem string) {
	switch kind {
	case "role":
		name, ok := customRoleName(id)
		if !ok {
			return false, ""
		}
		_, ok, err := coreRecord[rbac.Role](after, coreRoleKey(name))
		if err != nil {
			return false, err.Error()
		}
		return ok, ""
	case "oauth identity":
		var h headIdentityRow
		if json.Unmarshal(before.identityRows["oauthid:"+id], &h) != nil || h.Provider == "" || h.Subject == "" {
			return false, ""
		}
		sys, hasSys, p := headSystemUser(before)
		if p != "" {
			return false, p
		}
		got, ok, err := coreRecord[coreauth.ExternalIdentity](after, coreIdentityKey(h.Provider, h.Subject))
		if err != nil {
			return false, err.Error()
		}
		if ok && string(got.UserID) != coreOwnerOf(h.UserID, sys, hasSys) {
			return false, fmt.Sprintf("oauth identity ID %s: core's identity links user %s, the HEAD row user %s", id, got.UserID, h.UserID)
		}
		return ok, ""
	case "user":
		sys, hasSys, p := headSystemUser(before)
		if p != "" {
			return false, p
		}
		if !hasSys || id != sys.ID {
			return false, ""
		}
		u, ok, err := coreRecord[coreauth.User](after, coreUserKey(string(coreauth.SystemUserID)))
		switch {
		case err != nil:
			return false, err.Error()
		case !ok:
			return false, ""
		}
		if u.Status != coreauth.StatusSystem || u.Username != sys.Username || u.Email != sys.Email || !u.CreatedAt.Equal(sys.CreatedAt) {
			return false, fmt.Sprintf("user ID %s: core's %s user is not the import of HEAD's system row (status %q, username %q, created %s)",
				id, coreauth.SystemUserID, u.Status, u.Username, u.CreatedAt.Format(time.RFC3339Nano))
		}
		return true, ""
	}
	return false, ""
}

// coreImportedRecords counts the identity records core holds in s that M3's
// imports create, one per imported Source row: users (user/<id>), API keys
// (apikey/<id>), OAuth identities (identity/<provider>/<subject>), invite
// allow-list entries (inviteallow/<e-mail>) and custom roles (role/<name>,
// origin custom; the seed roles core recreates itself). Index rows differ from
// records in segment count (authstore's rule), so they are not counted.
func coreImportedRecords(s *storeScan) (n int64, problems []string) {
	for _, k := range sortedKeys(s.coreIdentity) {
		var rest string
		switch {
		case strings.HasPrefix(k, coreAuthKeyspace):
			rest = k[len(coreAuthKeyspace):]
		case strings.HasPrefix(k, coreRBACKeyspace):
			rest = k[len(coreRBACKeyspace):]
		}
		segs, err := keys.Split([]byte(rest))
		if err != nil || len(segs) == 0 {
			continue // a row outside the tuple layout (a generation row) is no record
		}
		fam := string(segs[0])
		switch {
		case strings.HasPrefix(k, coreRBACKeyspace) && fam == "role" && len(segs) == 2:
			r, _, err := coreRecord[rbac.Role](s, k)
			if err != nil {
				problems = append(problems, err.Error())
				continue
			}
			if r.Origin == rbac.OriginCustom {
				n++
			}
		case strings.HasPrefix(k, coreAuthKeyspace) && (fam == "user" || fam == "apikey" || fam == "inviteallow") && len(segs) == 2,
			strings.HasPrefix(k, coreAuthKeyspace) && fam == "identity" && len(segs) == 3 && string(segs[1]) != "u":
			n++
		}
	}
	return n, problems
}

// identityCountProblems: M3's stored report (c/storage/dm/<M3>/report) against
// the store, each count independently: Read is the rows the before-scan found
// in its Source, Quarantined the quarantine records naming its Source rows, and
// Imported the identity records core holds after the boot and did not before
// (coreImportedRecords). The prod-copy rehearsal reads these counts.
func identityCountProblems(steps []cutoverStep, before, after *storeScan) []string {
	var m3 *cutoverStep
	for i := range steps {
		if steps[i].ID == cutover.IdentityCutoverID {
			m3 = &steps[i]
		}
	}
	if m3 == nil {
		return []string{cutover.IdentityCutoverID + " is not among the build's cutover steps"}
	}
	r := m3.Report
	var out []string
	if !r.Stored {
		return append(out, m3.ID+": no stored report")
	}
	if want := before.InSource[m3.ID]; r.Read != want {
		out = append(out, fmt.Sprintf("%s: the stored report read %d rows, its Source held %d", m3.ID, r.Read, want))
	}
	var q int64
	for _, rec := range after.Quarantine {
		if rec.Step == m3.ID && m3.inSource(rec.Key) {
			q++
		}
	}
	if r.Quarantined != q {
		out = append(out, fmt.Sprintf("%s: the stored report quarantined %d rows, the store holds %d quarantine records of its Source rows", m3.ID, r.Quarantined, q))
	}
	had, p1 := coreImportedRecords(before)
	has, p2 := coreImportedRecords(after)
	out = append(append(out, p1...), p2...)
	if r.Imported != has-had {
		out = append(out, fmt.Sprintf("%s: the stored report imported %d rows, core holds %d identity records it did not hold before", m3.ID, r.Imported, has-had))
	}
	return out
}

// checkRoleHolders: every overlay quarantine case about a role (KeyPrefix
// role:) as its manifest entry states it, read where M3 writes (05-rbac §6.3;
// 09-abo-on-core.md, role-all-unknown): the role is quarantined (a record names
// its row) and core holds no role for it, and each user the case names is a
// holder of a role-list entry naming it, imported under its own ID with no
// record of its own and with that entry dropped: its core role list is its HEAD
// list with the case's roles removed, a seed role kept by name and another
// custom role under custom.<slug>. role-all-unknown's own words are checked by
// name: talia.al.ghul keeps viewer, ubu's list ends empty.
func checkRoleHolders(t *testing.T, c corpus, r *cutoverResult) {
	t.Helper()
	fi := newFamilyIndex()
	recorded := map[string]bool{}
	for _, rec := range r.after.Quarantine {
		if rec.Step == cutover.IdentityCutoverID {
			recorded[string(rec.Key)] = true
		}
	}
	sys, hasSys, problem := headSystemUser(r.before)
	require.Empty(t, problem)
	cases := 0
	for _, qc := range c.quarantineCases {
		if idKinds[fi.family([]byte(qc.KeyPrefix))] != "role" {
			continue
		}
		var roles, users []string
		for _, k := range qc.Keys {
			switch fi.family([]byte(k)) {
			case "role:":
				roles = append(roles, strings.TrimPrefix(k, "role:"))
			case "u:":
				users = append(users, strings.TrimPrefix(k, "u:"))
			}
		}
		if len(users) == 0 {
			continue // a role no user holds (corrupt-role): the quarantine report covers it
		}
		cases++
		t.Run(qc.Label, func(t *testing.T) {
			for _, id := range roles {
				require.Truef(t, recorded["role:"+id], "role %s is not quarantined", id)
				if name, ok := customRoleName(id); ok {
					_, has, err := coreRecord[rbac.Role](r.after, coreRoleKey(name))
					require.NoError(t, err)
					require.Falsef(t, has, "core holds %s, the quarantined role %s", name, id)
				}
			}
			byName := map[string][]string{}
			for _, id := range users {
				require.Falsef(t, recorded["u:"+id], "user %s, a holder of an entry naming the role, is quarantined", id)
				var h headUserRow
				require.NoError(t, json.Unmarshal(r.before.identityRows["u:"+id], &h), "HEAD's u:%s", id)
				u, ok, err := coreRecord[coreauth.User](r.after, coreUserKey(coreOwnerOf(id, sys, hasSys)))
				require.NoError(t, err)
				require.Truef(t, ok, "user %s (%s) is not imported", id, h.Username)
				want := []string{}
				for _, e := range h.Roles {
					switch name, custom := customRoleName(e); {
					case slices.Contains(roles, e):
					case custom:
						want = append(want, name)
					default:
						want = append(want, e)
					}
				}
				got := []string{}
				for _, n := range u.Roles {
					got = append(got, string(n))
				}
				slices.Sort(want)
				slices.Sort(got)
				require.Equalf(t, want, got, "user %s (%s): core's role list", id, h.Username)
				byName[h.Username] = got
			}
			if qc.Label == "role-all-unknown" {
				t.Run("talia.al.ghul-keeps-viewer", func(t *testing.T) {
					require.Equal(t, []string{"viewer"}, byName["talia.al.ghul"])
				})
				t.Run("ubu-ends-empty", func(t *testing.T) {
					got, ok := byName["ubu"]
					require.True(t, ok, "ubu is not a holder of the case")
					require.Empty(t, got)
				})
			}
		})
	}
	require.Positive(t, cases, "no overlay quarantine case about a role names a holder: the check proves nothing")
}

// selftestQuarantineCases is the overlay's quarantine cases as the fake step
// (moveUsers, which moves u: rows) sees them. No real case quarantines a user:
// the users a role case names are holders, imported (quarantineProblems). So
// the holders are lifted out of their cases into one case of the fake step's
// own, about users, which the fake quarantines as a real step quarantines a
// case's entity; the checks then bite on the fake as before.
func selftestQuarantineCases(cases []r2Stage2Case) []r2Stage2Case {
	fi := newFamilyIndex()
	out := make([]r2Stage2Case, 0, len(cases)+1)
	own := r2Stage2Case{Label: "selftest-users", KeyPrefix: selftestFamily, Outcome: "quarantined"}
	for _, qc := range cases {
		kind, hasKind := idKinds[fi.family([]byte(qc.KeyPrefix))]
		var kept []string
		for _, k := range qc.Keys {
			if hasKind && kind != "user" && fi.family([]byte(k)) == selftestFamily {
				own.Keys = append(own.Keys, k)
				continue
			}
			kept = append(kept, k)
		}
		qc.Keys = kept
		out = append(out, qc)
	}
	if len(own.Keys) > 0 {
		out = append(out, own)
	}
	return out
}

// testHoldersSelftest proves quarantineProblems' holder rule on a role case of
// role-all-unknown's shape: the role is the case's entity and its users are
// holders, so a record naming a holder is a row quarantined in error, and the
// role still needs its record.
func testHoldersSelftest(t *testing.T) {
	const (
		role  = "role:01ARKHAM0000000000000LEAGUE"
		talia = "u:01ARKHAM00000000000000TALIA"
		ubu   = "u:01ARKHAM0000000000000000UBU"
	)
	step := cutoverStep{ID: cutover.IdentityCutoverID, Source: []keyRange{prefixRange("role:"), prefixRange("u:")}}
	cases := []r2Stage2Case{{Label: "role-all-unknown", KeyPrefix: "role:", Keys: []string{"idx:role:name:league of shadows", role, talia, ubu}, Outcome: "quarantined"}}
	for name, tc := range map[string]struct {
		records []string
		bites   bool
	}{
		"the-role-quarantined":                     {records: []string{role}},
		"the-role-and-a-holder-quarantined":        {records: []string{role, talia}, bites: true},
		"the-role-and-every-holder-quarantined":    {records: []string{role, talia, ubu}, bites: true},
		"a-holder-quarantined-and-the-role-is-not": {records: []string{ubu}, bites: true},
		"nothing-quarantined":                      {bites: true},
	} {
		t.Run(name, func(t *testing.T) {
			var recs []quarantineRecord
			for _, k := range tc.records {
				recs = append(recs, quarantineRecord{Key: []byte(k), Step: step.ID})
			}
			got := quarantineProblems([]cutoverStep{step}, cases, recs)
			if tc.bites {
				require.NotEmpty(t, got, "the holder rule let it pass")
			} else {
				require.Empty(t, got)
			}
		})
	}
}

// testIdentityIDsSelftest proves identityIDKept on synthetic stores: each ID
// is kept only in the form M3 writes, and the system user remap allows exactly
// HEAD's _system row, imported, and no other user.
func testIdentityIDsSelftest(t *testing.T) {
	const (
		sysID   = "01ARKHAM000000000000SYSTEM0"
		alfred  = "01ARKHAM0000000000000ALFRED"
		roleID  = "01ARKHAM000000000000000RA5"
		identID = "01ARKHAM0000000000000ORACLE"
	)
	created := time.Date(2026, 11, 10, 11, 33, 10, 0, time.UTC)
	head := func(extra map[string]string) map[string][]byte {
		rows := map[string][]byte{
			"u:" + sysID:         fmt.Appendf(nil, `{"id":%q,"username":"_system","email":"_system@local","status":"system","created_at":%q}`, sysID, created.Format(time.RFC3339Nano)),
			"u:" + alfred:        fmt.Appendf(nil, `{"id":%q,"username":"alfred.pennyworth","status":"active","roles":[%q]}`, alfred, roleID),
			"role:" + roleID:     fmt.Appendf(nil, `{"id":%q,"name":"League"}`, roleID),
			"oauthid:" + identID: fmt.Appendf(nil, `{"id":%q,"provider":"oidc","subject":"oracle-7","user_id":%q}`, identID, sysID),
		}
		for k, v := range extra {
			rows[k] = []byte(v)
		}
		return rows
	}
	enc := func(v any) []byte {
		var raw []byte
		var err error
		switch v := v.(type) {
		case coreauth.User:
			raw, err = codec.JSON[coreauth.User](1).Encode(v)
		case rbac.Role:
			raw, err = codec.JSON[rbac.Role](1).Encode(v)
		case coreauth.ExternalIdentity:
			raw, err = codec.JSON[coreauth.ExternalIdentity](1).Encode(v)
		}
		require.NoError(t, err)
		return raw
	}
	imported := func() map[string][]byte {
		return map[string][]byte{
			coreUserKey(string(coreauth.SystemUserID)):            enc(coreauth.User{ID: coreauth.SystemUserID, Username: "_system", Email: "_system@local", Status: coreauth.StatusSystem, CreatedAt: created}),
			coreUserKey(alfred):                                   enc(coreauth.User{ID: alfred, Username: "alfred.pennyworth", Status: coreauth.StatusActive}),
			coreRoleKey("custom.role-" + strings.ToLower(roleID)): enc(rbac.Role{Name: rbac.RoleName("custom.role-" + strings.ToLower(roleID)), Origin: rbac.OriginCustom}),
			coreIdentityKey("oidc", "oracle-7"):                   enc(coreauth.ExternalIdentity{Provider: "oidc", Subject: "oracle-7", UserID: coreauth.SystemUserID}),
		}
	}
	for name, tc := range map[string]struct {
		before map[string]string // extra HEAD rows
		mutate func(after map[string][]byte)
		gone   []string // "<kind> <id>" the check must report
		bad    bool     // a problem other than a gone ID
	}{
		"a-clean-import": {},
		"the-role-under-its-upper-case-id": {mutate: func(a map[string][]byte) {
			delete(a, coreRoleKey("custom.role-"+strings.ToLower(roleID)))
			a[coreRoleKey("custom.role-"+roleID)] = enc(rbac.Role{Name: rbac.RoleName("custom.role-" + roleID), Origin: rbac.OriginCustom})
		}, gone: []string{"role " + roleID}},
		"the-role-missing":     {mutate: func(a map[string][]byte) { delete(a, coreRoleKey("custom.role-"+strings.ToLower(roleID))) }, gone: []string{"role " + roleID}},
		"the-identity-missing": {mutate: func(a map[string][]byte) { delete(a, coreIdentityKey("oidc", "oracle-7")) }, gone: []string{"oauth identity " + identID}},
		"the-identity-linked-to-the-head-ulid": {mutate: func(a map[string][]byte) {
			a[coreIdentityKey("oidc", "oracle-7")] = enc(coreauth.ExternalIdentity{Provider: "oidc", Subject: "oracle-7", UserID: sysID})
		}, bad: true},
		"a-second-user-remapped": {mutate: func(a map[string][]byte) {
			delete(a, coreUserKey(alfred))
			a[coreUserKey("_alfred")] = enc(coreauth.User{ID: "_alfred", Username: "alfred.pennyworth", Status: coreauth.StatusActive})
		}, gone: []string{"user " + alfred}},
		"the-system-row-dropped-and-core-seeded-its-own": {mutate: func(a map[string][]byte) {
			a[coreUserKey(string(coreauth.SystemUserID))] = enc(coreauth.User{ID: coreauth.SystemUserID, Username: "_system", Status: coreauth.StatusSystem, CreatedAt: created.Add(time.Hour)})
		}, bad: true},
		"the-system-row-imported-active": {mutate: func(a map[string][]byte) {
			a[coreUserKey(string(coreauth.SystemUserID))] = enc(coreauth.User{ID: coreauth.SystemUserID, Username: "_system", Email: "_system@local", Status: coreauth.StatusActive, CreatedAt: created})
		}, bad: true},
		"two-system-rows": {before: map[string]string{"u:01ARKHAM00000000000SYSTEM2": `{"id":"01ARKHAM00000000000SYSTEM2","username":"_System","status":"System"}`}, bad: true},
	} {
		t.Run(name, func(t *testing.T) {
			after := imported()
			if tc.mutate != nil {
				tc.mutate(after)
			}
			before := syntheticScan(t, nil, head(tc.before))
			got := idProblems(before, syntheticScan(t, nil, after))
			var gone, other []string
			for _, p := range got {
				if g, ok := strings.CutSuffix(p, " is gone after the cutover"); ok {
					gone = append(gone, g)
				} else {
					other = append(other, p)
				}
			}
			var want []string
			for _, g := range tc.gone {
				i := strings.LastIndexByte(g, ' ')
				want = append(want, g[:i]+" ID"+g[i:])
			}
			require.ElementsMatch(t, want, gone)
			require.Equal(t, tc.bad, len(other) > 0, "other problems: %v", other)
		})
	}
}

// r0SystemUserID is the HEAD ID of the committed R0 corpus' _system row
// (testdata/r2corpus/answers/identity.json), which the prefreeze-written corpus
// inherits.
const r0SystemUserID = "01M72KQH0DNEW356YZS9YA1EVD"

// checkSystemUserRemap: the one user ID the cutover does not keep is HEAD's
// _system row's, imported under core's system user ID (systemuser.go), and on
// the committed corpora it is exactly r0SystemUserID. Core holds no user under
// that HEAD ID, and holds the import of the row under the system ID.
func checkSystemUserRemap(t *testing.T, c corpus, r *cutoverResult) {
	t.Helper()
	sys, ok, problem := headSystemUser(r.before)
	require.Empty(t, problem)
	require.True(t, ok, "the corpus holds no _system row")
	if c.name == "r0" || c.name == "prefreeze-written" {
		require.Equal(t, r0SystemUserID, sys.ID, "the corpus' _system row")
	}
	kept, problem := identityIDKept("user", sys.ID, r.before, r.after)
	require.Empty(t, problem)
	require.True(t, kept, "HEAD's _system row is not imported under %s", coreauth.SystemUserID)
	_, under, err := coreRecord[coreauth.User](r.after, coreUserKey(sys.ID))
	require.NoError(t, err)
	require.False(t, under, "core holds a user under the system row's HEAD ID %s too", sys.ID)
}

// testM3WritesSelftest proves the byte check excludes the books families M3
// fills beside its Source (shared_families.golden's rewrite entries for them)
// and only those: a store that gains one such row stays byte-identical with
// the generated list, and the same store fails by name when the family's entry
// is missing from it.
func testM3WritesSelftest(t *testing.T) {
	shared := loadSharedFamilies(t)
	fi := newFamilyIndex()
	scan := func(shared []sharedEntry, rows map[string]string) *storeScan {
		spec := scanSpec{shared: shared, keepRows: true}
		s := newStoreScan(spec)
		for _, k := range sortedKeys(rows) {
			s.add(fi, spec, []byte(k), []byte(rows[k]))
		}
		return s
	}
	base := map[string]string{"preference:db_version": `{"id":1,"key":"db_version","value":"{\"version\":66}"}`}
	for fam, key := range map[string]string{
		"acceptedinvite:": "acceptedinvite:01ARKHAM000000000000INVITE",
		"guestpolicy:":    "guestpolicy:oracle@example.test",
	} {
		after := map[string]string{key: `{"user_id":"01ARKHAM000000000000BARBARA"}`}
		for k, v := range base {
			after[k] = v
		}
		t.Run(fam+"listed", func(t *testing.T) {
			require.True(t, slices.ContainsFunc(shared, func(e sharedEntry) bool { return e.Class == "rewrite" && e.Value == fam }),
				"shared_families.golden has no rewrite entry for %s", fam)
			require.Empty(t, byteProblems(scan(shared, base), scan(shared, after)))
		})
		t.Run(fam+"unlisted", func(t *testing.T) {
			without := slices.DeleteFunc(slices.Clone(shared), func(e sharedEntry) bool { return e.Value == fam })
			got := byteProblems(scan(without, base), scan(without, after))
			require.Equal(t, []string{"family " + fam + ": 1 keys appeared"}, got)
		})
	}
}
