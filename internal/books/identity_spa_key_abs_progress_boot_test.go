// file: internal/books/identity_spa_key_abs_progress_boot_test.go
// version: 0.1.0
// guid: 07c21e66-72b5-4ec2-ae71-46472c298491
// last-edited: 2026-11-22

package books

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// The Settings API Keys page makes a key for an Audiobookshelf client (F4-08
// B10sk). It posts to HEAD's /api/v1/auth/api-keys with atom-name scopes, and
// the key it gets must sync listening progress, which needs books:reading, an
// ambient scope. Core's native /api/core/v1/me/api-keys adds no ambient scope,
// so the SPA must not use it; the control cell records what such a key gets.

// spaKeyPayload is exactly what web/src/components/settings/APIKeysTab.tsx posts
// after the user types a name and a description, ticks library.view and leaves
// the expiry at its default of 90 days (services/api.ts createAPIKey sends the
// request object as JSON).
func spaKeyPayload(name string, scopes []string) map[string]any {
	return map[string]any{
		"name":            name,
		"description":     "Alfred's phone",
		"scopes":          scopes,
		"expires_in_days": 90,
	}
}

// spaPickerAtoms is the list web/src/utils/apiKeyScopes.ts offers (HEAD's 12).
var spaPickerAtoms = []string{
	"library.view", "library.edit_metadata", "library.delete", "library.organize",
	"scan.trigger", "integrations.manage", "users.manage", "settings.manage",
	"playlists.create", "requests.create", "requests.approve", "fingerprint.worker",
}

// absBookWithID is absBook that also returns the book id, to read the stored position back.
func absBookWithID(t *testing.T, r *runningApp, title string) (bookID, syncID string) {
	t.Helper()
	b, err := r.store.CreateBook(&database.Book{Title: title, FilePath: filepath.Join(t.TempDir(), title+".m4b")})
	require.NoError(t, err)
	syncID, err = r.store.MintOrGetSyncID(b.ID)
	require.NoError(t, err)
	return b.ID, syncID
}

func TestSPAKey_SyncsABSProgress(t *testing.T) {
	var h headKeys
	fx := newFlipFixture(t, seedHEAD(t, &h), "")
	r := bootOverHEADStore(t, fx)
	bruce := loginCoreAdmin(t, r, wayne, h.password)
	lucius := loginCoreAdmin(t, r, "lucius.fox", h.password)
	luciusID := userIDByEmail(t, bruce, "lucius.fox@wayne-enterprises.example.test")

	position := func(t *testing.T, bookID string) float64 {
		t.Helper()
		pos, err := r.store.GetUserPosition(luciusID, bookID)
		require.NoError(t, err)
		if pos == nil {
			return -1
		}
		return pos.PositionSeconds
	}

	t.Run("every scope the picker offers is a HEAD atom the books route accepts", func(t *testing.T) {
		for _, atom := range spaPickerAtoms {
			require.Truef(t, auth.IsKnown(auth.Permission(atom)), "the picker offers %q, which HEAD does not know", atom)
		}
	})

	t.Run("the server accepts the atoms the picker offers and refuses ones outside them with HEAD's 400", func(t *testing.T) {
		for _, atom := range spaPickerAtoms {
			code, res, raw := v1Create(t, r, spaKeyPayload("picker-"+atom, []string{atom}), bruce, "")
			require.Equalf(t, http.StatusCreated, code, "the picker's %q: %s", atom, raw)
			require.Equal(t, []string{atom}, res.Scopes)
		}
		// An atom HEAD does not define, and a core permission name (what the
		// SPA's picker sent before this fix) are both HEAD's unknown-scope 400.
		for _, bad := range []string{"library.fly", "books:view"} {
			code, _, raw := v1Create(t, r, spaKeyPayload("picker-bad", []string{bad}), bruce, "")
			require.Equalf(t, http.StatusBadRequest, code, "%q: %s", bad, raw)
			require.Contains(t, raw, "unknown scope: "+bad)
		}
	})

	t.Run("a key made with the SPA's payload syncs ABS progress and the progress is stored", func(t *testing.T) {
		bookID, syncID := absBookWithID(t, r, "The Long Halloween")
		res := mustCreate(t, r, spaKeyPayload("alfred-phone", []string{"library.view"}), lucius, "")
		require.Equal(t, []string{"library.view"}, res.Scopes, "HEAD answers the atoms asked for")
		require.Equal(t, -1.0, position(t, bookID), "no position before the sync")

		code, body := absProgress(t, r, syncID, res.Token)
		require.Equal(t, http.StatusOK, code, body)
		require.Equal(t, 42.0, position(t, bookID), "the 200 did not store the progress")
		// core:self is ambient: the key can read its own identity on core.
		meCode, method := keyCoreMe(t, r, res.Token)
		require.Equal(t, http.StatusOK, meCode)
		require.Equal(t, "api_key", method)
	})

	t.Run("the key's core record carries the ambient scopes, books:reading among them", func(t *testing.T) {
		res := mustCreate(t, r, spaKeyPayload("alfred-tablet", []string{"library.view"}), lucius, "")
		scopes := coreScopes(t, bruce, res.ID)
		require.Contains(t, scopes, "books:reading")
		for _, s := range ambientScopes() {
			require.Contains(t, scopes, string(s), "the SPA's key lacks ambient scope %s: %v", s, scopes)
		}
	})

	t.Run("a key made with the SPA's untouched default (no scope ticked) syncs ABS progress too", func(t *testing.T) {
		bookID, syncID := absBookWithID(t, r, "Dark Victory")
		res := mustCreate(t, r, spaKeyPayload("alfred-default", []string{}), lucius, "")
		code, body := absProgress(t, r, syncID, res.Token)
		require.Equal(t, http.StatusOK, code, body)
		require.Equal(t, 42.0, position(t, bookID))
		require.Contains(t, coreScopes(t, bruce, res.ID), "books:reading")
	})

	t.Run("control: a key made on core's native route gets no ambient scope", func(t *testing.T) {
		// library.view is books:view and core:tasks.view on core (the atom table).
		// The native route stores exactly the scopes it is given: no core:self,
		// core:metrics.read or books:reading. ABS progress is not scope-gated, on
		// HEAD either: ABSRequireAuth only authenticates, Bind intersects the key's
		// scopes with its owner's roles (internal/server/middleware/absauth.go
		// 529-541), and the one ABS handler that checks a permission is the
		// collections write gate (handlers/abs/collections.go canManageCollections,
		// auth.Can(PermCollectionsManage)). So books:reading gates nothing on ABS
		// at HEAD, and a native key syncing progress is HEAD parity, not a gap.
		// What a native key cannot do is read its own identity on core, and it
		// holds none of the ambient scopes the books route mints. The SPA must
		// not make keys here.
		bookID, syncID := absBookWithID(t, r, "Haunted Knight")
		raw, id := lucius.mintKey("alfred-native", []string{"books:view", "core:tasks.view"})
		scopes := coreScopes(t, bruce, id)
		require.ElementsMatch(t, []string{"books:view", "core:tasks.view"}, scopes)
		for _, s := range ambientScopes() {
			require.NotContains(t, scopes, string(s), "core's native route added ambient scope %s", s)
		}
		code, _ := keyCoreMe(t, r, raw)
		require.Equal(t, http.StatusForbidden, code, "a native key reads its own core identity (no core:self)")

		pc, body := absProgress(t, r, syncID, raw)
		require.Equal(t, http.StatusOK, pc, body)
		require.Equal(t, 42.0, position(t, bookID), "recorded: ABS progress is not scope-gated at this base")
	})

	t.Run("the one scope check HEAD made on ABS: the collections write gate admits only a key holding collections.manage", func(t *testing.T) {
		// POST /api/collections is gated by auth.Can(PermCollectionsManage)
		// (handlers/abs/collections.go canManageCollections). Whatever route made
		// the key, one without the scope must get HEAD's refusal; admitting it
		// would be a widening.
		n := 0
		create := func(raw string) (int, string) {
			n++ // a collection name is unique, so an admitted create takes its own
			rec := coreDo(t, r, http.MethodPost, "/api/collections", map[string]any{"name": fmt.Sprintf("Court of Owls %d", n), "books": []string{}}, nil, raw)
			return rec.Code, rec.Body.String()
		}
		const refusal = "permission denied: collections.manage"

		// An admin owner (who holds collections.manage) with a key that does not name it.
		spaNo := mustCreate(t, r, spaKeyPayload("owl-view", []string{"library.view"}), bruce, "")
		code, body := create(spaNo.Token)
		require.Equal(t, http.StatusForbidden, code, body)
		require.Contains(t, body, refusal)
		nativeNo, _ := bruce.mintKey("owl-native-view", []string{"books:view", "core:tasks.view"})
		code, body = create(nativeNo)
		require.Equal(t, http.StatusForbidden, code, body)
		require.Contains(t, body, refusal)
		// An owner who does not hold it, whatever the key names.
		luciusNo, _ := lucius.mintKey("owl-editor", []string{"books:view", "core:tasks.view"})
		code, body = create(luciusNo)
		require.Equal(t, http.StatusForbidden, code, body)
		require.Contains(t, body, refusal)

		// A key that names it, owned by someone who holds it, is admitted.
		spaYes := mustCreate(t, r, spaKeyPayload("owl-manage", []string{"collections.manage"}), bruce, "")
		code, body = create(spaYes.Token)
		require.Equal(t, http.StatusOK, code, body)
		nativeYes, _ := bruce.mintKey("owl-native-manage", []string{"books:collections.manage"})
		code, body = create(nativeYes)
		require.Equal(t, http.StatusOK, code, body)
	})

	t.Run("control: core's native route refuses the SPA's untouched default (no scope ticked)", func(t *testing.T) {
		rec := coreDo(t, r, http.MethodPost, "/api/core/v1/me/api-keys", map[string]any{"name": "alfred-default", "scopes": []string{}}, lucius.cookie, "")
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), "/body/scopes")
	})
}
