// file: internal/books/identity_user_unmerge_test.go
// version: 0.1.0
// guid: 4b7d2e91-8c35-4a60-b1f4-6e9a0d3c5f87
// last-edited: 2026-11-22

package books

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/usermerge"
	"github.com/falkcorp/mediacore/auth/oidc"
)

// F4-08 B10p, real core: the undo of an admin user merge under a real identity
// boot. Nothing is stubbed: core's kernel and auth service, its real
// PlanUserUnmerge / StageUserUnmerge, books' wiring, the real merge routes
// behind core's stage 12, the real operations registry and the one-batch
// commit.

const (
	unSubject = "batman-subject-0001"       // the external sign-in's subject id: a person at an identity provider
	unEmail   = "dick.grayson@example.test" // the absorbed account's verified address
)

// unmergeEnv is the identity merge env with a richer absorbed account: it is
// born from a verified Access sign-in (so core holds an external identity for
// it, with the subject above, and a verified address), holds one API key and
// one books row.
type unmergeEnv struct {
	*idMergeEnv
	keyID string
}

func newUnmergeEnv(t *testing.T) *unmergeEnv {
	t.Helper()
	e := &unmergeEnv{idMergeEnv: newIDMergeEnv(t)}
	idw := e.c.mod.idw
	require.NotNil(t, idw)
	idw.policy.bind(oidc.Policy{Provider: cfaccessProvider, AllowedEmails: []string{unEmail}, DefaultRole: "viewer", LinkExistingByEmail: true})
	u, err := idw.policy.Resolve(context.Background(), oidc.Policy{}, oidc.Claims{
		Provider: cfaccessProvider, Subject: unSubject, Email: unEmail, EmailVerified: true})
	require.NoError(t, err)
	require.NotNil(t, u)
	e.absorbed = string(u.ID)
	require.NotEqual(t, e.surv, e.absorbed)

	// One API key of the absorbed account's, minted by the admin.
	rec := idDo(t, e.h, http.MethodPost, "/api/core/v1/users/"+e.absorbed+"/api-keys",
		map[string]any{"name": "robin-key", "scopes": []string{"core:self"}}, e.ck)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created struct {
		APIKey struct {
			ID string `json:"id"`
		} `json:"api_key"`
		Data struct {
			APIKey struct {
				ID string `json:"id"`
			} `json:"api_key"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created), rec.Body.String())
	e.keyID = created.APIKey.ID + created.Data.APIKey.ID
	require.NotEmpty(t, e.keyID)

	require.NoError(t, e.c.store.SetUserPreferenceForUser(e.absorbed, "speed", "2.0"))
	return e
}

// coreUserBody is core's account read as the admin: the raw body.
func (e *unmergeEnv) coreUser(t *testing.T, id string) string {
	t.Helper()
	rec := idDo(t, e.h, http.MethodGet, "/api/core/v1/users/"+id, nil, e.ck)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return rec.Body.String()
}

func (e *unmergeEnv) keyOwner(t *testing.T) string {
	t.Helper()
	rec := idDo(t, e.h, http.MethodGet, "/api/core/v1/api-keys/"+e.keyID, nil, e.ck)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return rec.Body.String()
}

// relogin signs the admin in again: the merge and its undo each end the
// survivor's sessions when a sign-in moves onto or off it.
func (e *unmergeEnv) relogin(t *testing.T) {
	t.Helper()
	e.ck = idLogin(t, e.h, e.adminName, e.adminPass)
}

// merge previews and applies the merge over HTTP and waits for the journal.
func (e *unmergeEnv) merge(t *testing.T) (preview string, jid string) {
	t.Helper()
	path := "/api/v1/users/" + e.absorbed + "/merge-into/" + e.surv
	pv := idDo(t, e.h, http.MethodPost, path+"/preview", nil, e.ck)
	require.Equal(t, http.StatusOK, pv.Code, pv.Body.String())
	var plan struct {
		Digest string `json:"digest"`
		Data   struct {
			Digest string `json:"digest"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(pv.Body.Bytes(), &plan), pv.Body.String())
	ap := idDo(t, e.h, http.MethodPost, path+"/apply",
		map[string]any{"digest": plan.Digest + plan.Data.Digest, "acknowledge_owner_identity": true}, e.ck)
	require.Equal(t, http.StatusAccepted, ap.Code, ap.Body.String())
	require.Eventually(t, func() bool {
		js, err := e.um.ListJournals()
		return err == nil && len(js) == 1 && js[0].Status == "applied"
	}, 60*time.Second, 100*time.Millisecond, "the merge never applied")
	js, err := e.um.ListJournals()
	require.NoError(t, err)
	return pv.Body.String(), js[0].ID
}

func (e *unmergeEnv) undoBody(t *testing.T, jid string) (int, string) {
	t.Helper()
	rec := idDo(t, e.h, http.MethodPost, "/api/v1/users/merges/"+jid+"/undo", nil, e.ck)
	return rec.Code, rec.Body.String()
}

func (e *unmergeEnv) waitUndone(t *testing.T, jid string) *usermerge.Journal {
	t.Helper()
	require.Eventually(t, func() bool {
		j, err := e.um.LoadJournal(jid)
		return err == nil && j.Status == "undone"
	}, 60*time.Second, 100*time.Millisecond, "the undo never completed")
	j, err := e.um.LoadJournal(jid)
	require.NoError(t, err)
	return j
}

func TestUnmergeIdentity_RealCoreRestoresTheMergedAwayAccount(t *testing.T) {
	e := newUnmergeEnv(t)
	before := e.coreUser(t, e.absorbed)
	require.Contains(t, before, unEmail)
	require.NotContains(t, before, "merged_into_user_id")
	require.Contains(t, e.keyOwner(t), `"owner":"`+e.absorbed+`"`)
	require.Equal(t, e.absorbed, e.signInOwner(t), "the sign-in links the absorbed account before the merge")
	survBefore := e.coreUser(t, e.surv)

	preview, jid := e.merge(t)
	// Condition 1: the preview shows the sign-in's PROVIDER, never its subject.
	require.NotContains(t, preview, unSubject, "a preview must never carry an external subject id")
	require.Contains(t, preview, "cfaccess")

	e.relogin(t)
	merged := e.coreUser(t, e.absorbed)
	require.Contains(t, merged, `"merged_into_user_id":"`+e.surv+`"`)
	require.Contains(t, merged, `"disabled"`)
	require.Contains(t, e.keyOwner(t), `"owner":"`+e.surv+`"`, "the merge moved the key to the survivor")
	require.Equal(t, e.surv, e.signInOwner(t), "the merge moved the sign-in to the survivor")
	require.Contains(t, e.coreUser(t, e.surv), `"viewer"`, "the merge gave the survivor the source's role")
	// No read model of the merge carries the subject: not the journal list.
	list := idDo(t, e.h, http.MethodGet, "/api/v1/users/merges", nil, e.ck)
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	require.NotContains(t, list.Body.String(), unSubject)
	staleAdmin := e.ck // the survivor's session as the merge left it

	code, body := e.undoBody(t, jid)
	require.Equal(t, http.StatusAccepted, code, body)
	require.NotContains(t, body, unSubject)
	j := e.waitUndone(t, jid)
	require.Empty(t, j.UndoConflicts)

	// The sign-in moved back, so the survivor's sessions ended again.
	stale := idDo(t, e.h, http.MethodGet, "/api/core/v1/users/"+e.surv, nil, staleAdmin)
	require.Equal(t, http.StatusUnauthorized, stale.Code, "a session opened while the sign-in was on the survivor must not outlive the undo")
	e.relogin(t)

	back := e.coreUser(t, e.absorbed)
	require.NotContains(t, back, "merged_into_user_id")
	require.Contains(t, back, `"active"`)
	require.Contains(t, back, unEmail, "the address is back on the restored account")
	require.Contains(t, e.keyOwner(t), `"owner":"`+e.absorbed+`"`, "the key is back on the restored account")
	require.Equal(t, e.absorbed, e.signInOwner(t), "the sign-in is back on the restored account")
	require.NotContains(t, e.coreUser(t, e.surv), `"viewer"`, "the role the merge gave the survivor is off it again")
	require.JSONEq(t, rolesOf(t, survBefore), rolesOf(t, e.coreUser(t, e.surv)))

	got, err := e.c.store.GetUserPreferenceForUser(e.absorbed, "speed")
	require.NoError(t, err)
	require.NotNil(t, got, "the books row went back to the restored account")
	require.Equal(t, "2.0", got.Value)
	left, err := e.c.store.GetUserPreferenceForUser(e.surv, "speed")
	require.NoError(t, err)
	require.Nil(t, left)
}

func rolesOf(t *testing.T, userBody string) string {
	t.Helper()
	var u struct {
		Roles []string `json:"roles"`
		Data  struct {
			Roles []string `json:"roles"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(userBody), &u), userBody)
	raw, err := json.Marshal(append(u.Roles, u.Data.Roles...))
	require.NoError(t, err)
	return string(raw)
}

// signInOwner is the account core's Access resolver links the seeded sign-in to
// now (the same claims every time).
func (e *unmergeEnv) signInOwner(t *testing.T) string {
	t.Helper()
	u, err := e.c.mod.idw.policy.Resolve(context.Background(), oidc.Policy{}, oidc.Claims{
		Provider: cfaccessProvider, Subject: unSubject, Email: unEmail, EmailVerified: true})
	require.NoError(t, err)
	require.NotNil(t, u)
	return string(u.ID)
}

// world is everything an undo that refuses must leave alone: both core
// accounts, the key, the sign-in's owner, both users' books rows and the
// journal, byte for byte.
func (e *unmergeEnv) world(t *testing.T, jid string) string {
	t.Helper()
	e.relogin(t)
	raw, err := e.c.store.GetRaw(database.UserMergeJournalPrefix + jid)
	require.NoError(t, err)
	var prefs string
	for _, id := range []string{e.surv, e.absorbed} {
		p, err := e.c.store.GetUserPreferenceForUser(id, "speed")
		require.NoError(t, err)
		if p != nil {
			prefs += id + "=" + p.Value + ";"
		}
	}
	return e.coreUser(t, e.surv) + e.coreUser(t, e.absorbed) + e.keyOwner(t) + e.signInOwner(t) + prefs + string(raw)
}

// setJournal rewrites the journal's JSON through edit.
func (e *unmergeEnv) setJournal(t *testing.T, jid string, edit func(m map[string]any)) {
	t.Helper()
	raw, err := e.c.store.GetRaw(database.UserMergeJournalPrefix + jid)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	edit(m)
	out, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, e.c.store.SetRaw(database.UserMergeJournalPrefix+jid, out))
}

func problem(t *testing.T, body string) string {
	t.Helper()
	var b struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal([]byte(body), &b)
	return b.Code
}

func TestUnmergeIdentity_RealCoreRefusalsLeaveBothStoresUntouched(t *testing.T) {
	for _, tc := range []struct {
		name string
		prep func(t *testing.T, e *unmergeEnv, jid string)
		code string
	}{
		{"a key changed since the merge", func(t *testing.T, e *unmergeEnv, jid string) {
			e.relogin(t)
			rec := idDo(t, e.h, http.MethodDelete, "/api/core/v1/api-keys/"+e.keyID, nil, e.ck)
			require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		}, "key_moved"},
		{"the straggler sweep is pending", func(t *testing.T, e *unmergeEnv, jid string) {
			e.setJournal(t, jid, func(m map[string]any) { m["core"].(map[string]any)["sweep_pending"] = true })
		}, usermerge.CodeSweepPending},
		{"the journal has no receipt", func(t *testing.T, e *unmergeEnv, jid string) {
			e.setJournal(t, jid, func(m map[string]any) { delete(m["core"].(map[string]any), "api_key_versions") })
		}, usermerge.CodeReceiptMissing},
		{"the journal predates the flip", func(t *testing.T, e *unmergeEnv, jid string) {
			e.setJournal(t, jid, func(m map[string]any) { delete(m, "core") })
		}, usermerge.ProblemMergePermanent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newUnmergeEnv(t)
			_, jid := e.merge(t)
			tc.prep(t, e, jid)
			before := e.world(t, jid)
			code, body := e.undoBody(t, jid)
			require.Equal(t, http.StatusConflict, code, body)
			require.Equal(t, tc.code, problem(t, body), body)
			require.NotContains(t, body, unSubject)
			require.NotContains(t, body, unEmail)
			require.Equal(t, before, e.world(t, jid), "a refused undo wrote")
			// Nothing was queued either: the journal is still the applied merge.
			j, err := e.um.LoadJournal(jid)
			require.NoError(t, err)
			require.Equal(t, usermerge.StatusApplied, j.Status)
		})
	}
}

func TestUnmergeIdentity_RealCoreResumeClearsThePendingSweepAndTheUndoThenWorks(t *testing.T) {
	e := newUnmergeEnv(t)
	_, jid := e.merge(t)
	e.setJournal(t, jid, func(m map[string]any) { m["core"].(map[string]any)["sweep_pending"] = true })
	e.relogin(t)

	code, body := e.undoBody(t, jid)
	require.Equal(t, http.StatusConflict, code, body)
	require.Equal(t, usermerge.CodeSweepPending, problem(t, body))

	rec := idDo(t, e.h, http.MethodPost, "/api/v1/users/merges/"+jid+"/resume", nil, e.ck)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Eventually(t, func() bool {
		j, err := e.um.LoadJournal(jid)
		return err == nil && j.Core != nil && !j.Core.SweepPending
	}, 60*time.Second, 100*time.Millisecond, "the resume never cleared the marker")

	// A second resume is no longer a resume: every other journal stays permanent.
	again := idDo(t, e.h, http.MethodPost, "/api/v1/users/merges/"+jid+"/resume", nil, e.ck)
	require.Equal(t, http.StatusConflict, again.Code, again.Body.String())
	require.Equal(t, usermerge.ProblemMergePermanent, problem(t, again.Body.String()))

	code, body = e.undoBody(t, jid)
	require.Equal(t, http.StatusAccepted, code, body)
	e.waitUndone(t, jid)
	e.relogin(t)
	require.NotContains(t, e.coreUser(t, e.absorbed), "merged_into_user_id")
}
