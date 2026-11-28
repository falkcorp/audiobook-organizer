// file: internal/server/interim_export_authority_test.go
// version: 0.1.0
// guid: 51a0c27e-efd0-4098-b7da-1fd5f4f77a56
// last-edited: 2026-11-28

package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/mediacore/health/diagnostics"
	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// testInterimAtoms is books' atom table (internal/books/atoms.go, row for row,
// the 13 of 05-rbac §6.1) in the form books hands over. The whole table, not a
// part of it: an op with an unknown fixer asks for every atom
// (opaccess), so a short table narrows an administrator's export.
func testInterimAtoms() []InterimAtom {
	row := func(atom string, scopes ...string) InterimAtom {
		return InterimAtom{Atom: auth.Permission(atom), Scopes: scopes}
	}
	return []InterimAtom{
		row("library.view", "books:view", "core:tasks.view"),
		row("library.edit_metadata", "books:edit"),
		row("library.delete", "books:delete"),
		row("library.organize", "books:organize"),
		row("scan.trigger", "books:scan"),
		row("integrations.manage", "core:integrations.manage", "books:integrations.manage"),
		row("users.manage", "core:users.manage", "core:roles.view"),
		row("settings.manage", "core:settings.manage", "books:settings.manage", "core:tasks.manage",
			"core:system.view", "core:system.manage", "core:library.manage", "core:library.browse",
			"core:integrations.manage"),
		row("playlists.create", "books:playlists.create"),
		row("collections.manage", "books:collections.manage"),
		row("requests.create", "books:request"),
		row("requests.approve", "books:request.approve"),
		row("fingerprint.worker", "books:fingerprint.worker"),
	}
}

// viaJSON is what a start request's params become by the time the run reads
// them: the executor stores them as JSON, and ExportParams.Perms is omitempty.
func viaJSON(t *testing.T, p diagnostics.ExportParams) diagnostics.ExportParams {
	t.Helper()
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	var out diagnostics.ExportParams
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

// scopesOf is every scope of the table except the ones in drop.
func scopesOf(atoms []InterimAtom, drop ...string) []string {
	skip := map[string]bool{}
	for _, d := range drop {
		skip[d] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, a := range atoms {
		for _, sc := range a.Scopes {
			if !skip[sc] && !seen[sc] {
				seen[sc] = true
				out = append(out, sc)
			}
		}
	}
	return out
}

// exportFixture is the repairs access fixture with the admin and the
// ops-manager accounts, and the rows an export narrows.
type exportAuthorityFixture struct {
	f     *repairsOpAccessFixture
	store *database.PebbleStore
	admin *database.User
	mgr   *database.User
}

const authorityListener = "01BATGIRLLISTENER00000B"

func newExportAuthorityFixture(t *testing.T) exportAuthorityFixture {
	t.Helper()
	f := newRepairsOpAccessFixture(t)
	_ = opsManagerToken(t, f)
	store, ok := f.srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok)
	admin, err := store.GetUserByUsername("admin")
	require.NoError(t, err)
	mgr, err := store.GetUserByUsername("opsmgr")
	require.NoError(t, err)
	require.NotNil(t, admin)
	require.NotNil(t, mgr)
	t.Setenv("TMPDIR", t.TempDir())
	now := time.Now().UTC()
	res := `{"fixer_id":"maintenance.audible-read-status","results":[{"owner_user_id":"` + authorityListener + `"}]}`
	require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
		ID: "op-authority-repair", DefID: "repairs.apply", Plugin: "test", Status: "completed",
		Params:     `{"fixer_id":"maintenance.audible-read-status","target_user_id":"` + authorityListener + `"}`,
		ResultData: &res, QueuedAt: now.Add(time.Second), CompletedAt: &now,
	}))
	return exportAuthorityFixture{f: f, store: store, admin: admin, mgr: mgr}
}

func (x exportAuthorityFixture) run(t *testing.T, actor tasks.Actor, p diagnostics.ExportParams) (diagnostics.ExportResult, error) {
	t.Helper()
	rc := newInterimRC(t, diagnostics.ExportKind, tasks.ModeLive, actor, "run-authority")
	return NewInterimBackends(func() *Server { return x.f.srv }, testInterimAtoms).Export.Export(rc, viaJSON(t, p))
}

func (x exportAuthorityFixture) ops(t *testing.T, res diagnostics.ExportResult) string {
	t.Helper()
	return zipEntry(t, filepath.Join(os.TempDir(), res.File), "operations.json")
}

// (a) A run whose snapshot is missing or empty is refused, never read as
// "everything": opActorPermissions reads a nil set as uncapped (HEAD's legacy
// op-row rule), and a snapshot that names nothing cannot have come from a start
// that passed core:system.manage. Not parallel: it swaps config.
func TestExportHolds_AMissingSnapshotIsRefused(t *testing.T) {
	x := newExportAuthorityFixture(t)
	for name, perms := range map[string][]string{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			p := viaJSON(t, diagnostics.ExportParams{ActorID: x.admin.ID, Perms: perms})
			require.Empty(t, p.Perms, "nil comes back nil and [] comes back []: neither names a scope")
			_, err := x.run(t, tasks.Actor{UserID: x.admin.ID, AuthMethod: "session"}, p)
			assert.ErrorIs(t, err, ErrInterimNoSnapshot)

			_, err = x.f.srv.exportHolds(tasks.Actor{UserID: x.admin.ID, AuthMethod: "session"}, p.Perms, testInterimAtoms())
			assert.ErrorIs(t, err, ErrInterimNoSnapshot)
		})
	}
	_, err := x.f.srv.exportHolds(tasks.Actor{UserID: x.admin.ID, AuthMethod: "session"}, []string{"core:admin"}, nil)
	assert.ErrorIs(t, err, ErrInterimNoAtoms, "no table: the scopes cannot be translated, so nothing is held")
}

// (b) The snapshot holds core scope names; the narrowing asks for atoms. An atom
// is held only when every scope the table maps it to is in the snapshot.
func TestSnapshotAtoms_TranslatesCoreScopes(t *testing.T) {
	atoms := testInterimAtoms()
	held := func(snapshot ...string) map[auth.Permission]bool { return snapshotAtoms(atoms, snapshot) }

	got := held(scopesOf(atoms)...)
	for _, a := range atoms {
		assert.True(t, got[a.Atom], "%s: every scope present", a.Atom)
	}

	// One scope of a multi-scope atom missing drops that atom and no other.
	got = held(scopesOf(atoms, "core:roles.view")...)
	assert.False(t, got[auth.PermUsersManage], "users.manage needs core:roles.view too")
	assert.True(t, got[auth.PermSettingsManage])
	assert.True(t, got[auth.PermLibraryView])

	got = held("books:view")
	assert.False(t, got[auth.PermLibraryView], "books:view alone is not library.view (core:tasks.view is missing)")
	assert.Empty(t, got)

	// The atom name is not a scope: a snapshot of atoms holds nothing.
	assert.Empty(t, held(string(auth.PermUsersManage), string(auth.PermSettingsManage)))

	// Scopes the table does not name are dropped, not errors and not grants.
	got = held("books:scan", "core:no-such-scope", "ext:somebody.else")
	assert.Equal(t, map[auth.Permission]bool{auth.PermScanTrigger: true}, got)

	// core:admin is the one scope the checker lists alone for an administrator.
	got = held("core:admin")
	for _, a := range atoms {
		assert.True(t, got[a.Atom], "%s: core:admin reaches it", a.Atom)
	}

	// An atom naming no scope is held by nothing, core:admin included.
	assert.Empty(t, snapshotAtoms([]InterimAtom{{Atom: "x.y"}}, []string{"core:admin"}))
}

// (c) A scoped key's run, started by an administrator's key (AuthMethod token),
// whose snapshot lacks the scope that gates a section, gets that section left
// out, though the administrator's roles hold it. Parity with the unscoped run.
func TestInterimExport_ScopedKeyRunLosesWhatItsSnapshotLacks(t *testing.T) {
	x := newExportAuthorityFixture(t)
	actor := tasks.Actor{UserID: x.admin.ID, AuthMethod: "token"}

	whole, err := x.run(t, actor, diagnostics.ExportParams{ActorID: x.admin.ID, Perms: []string{"core:admin"}})
	require.NoError(t, err)
	assert.Contains(t, x.ops(t, whole), authorityListener, "an unscoped key of an administrator gets the user data")

	scoped, err := x.run(t, actor, diagnostics.ExportParams{ActorID: x.admin.ID, Perms: scopesOf(testInterimAtoms(), "core:users.manage")})
	require.NoError(t, err)
	ops := x.ops(t, scoped)
	assert.NotContains(t, ops, authorityListener, "a key without core:users.manage got user data through its owner's roles")
	assert.NotContains(t, ops, "audible-read-status", "a Repairs run the key's scopes do not cover is in its export")
}

// (d) A run that was not started from a person's own session never holds an
// interactive-only atom, even when its snapshot lists every scope of it.
func TestExportHolds_OnlyASessionHoldsAnInteractiveOnlyAtom(t *testing.T) {
	x := newExportAuthorityFixture(t)
	atoms := testInterimAtoms()
	for i := range atoms {
		if atoms[i].Atom == auth.PermUsersManage {
			atoms[i].InteractiveOnly = true
		}
	}
	// users.manage stands in for an atom with a Sensitive scope
	snapshot := scopesOf(atoms)

	for _, c := range []struct {
		method string
		holds  bool
	}{
		{"session", true},
		{"token", false},
		{"session_delegated", false},
		{"auth_disabled", false},
		{"", false},
		{"unclassified", false},
	} {
		t.Run("method="+c.method, func(t *testing.T) {
			holds, err := x.f.srv.exportHolds(tasks.Actor{UserID: x.admin.ID, AuthMethod: c.method}, snapshot, atoms)
			require.NoError(t, err)
			assert.Equal(t, c.holds, holds(auth.PermUsersManage), "users.manage")
			assert.True(t, holds(auth.PermSettingsManage), "an ordinary atom is held whatever the method")
		})
	}
}

// (e) The account's roles are read when the run executes, not when it started:
// narrowed after the start, the run loses the right though its snapshot kept it.
func TestExportHolds_ARoleNarrowedAfterTheStartLosesTheRight(t *testing.T) {
	x := newExportAuthorityFixture(t)
	actor := tasks.Actor{UserID: x.admin.ID, AuthMethod: "session"}
	snapshot := []string{"core:admin"}

	before, err := x.f.srv.exportHolds(actor, snapshot, testInterimAtoms())
	require.NoError(t, err)
	require.True(t, before(auth.PermUsersManage))

	x.admin.Roles = []string{"role-ops-manager"} // settings.manage, no users.manage
	require.NoError(t, x.store.UpdateUser(x.admin))

	after, err := x.f.srv.exportHolds(actor, snapshot, testInterimAtoms())
	require.NoError(t, err)
	assert.False(t, after(auth.PermUsersManage), "the narrowed role must not keep users.manage")
	assert.True(t, after(auth.PermSettingsManage))
}
