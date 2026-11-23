// file: internal/books/identity_user_merge_test.go
// version: 0.1.0
// guid: 3a8f5c26-9d17-4e04-b6a2-81c0e7d49f35
// last-edited: 2026-11-21

package books

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/usermerge"
	coreauth "github.com/falkcorp/mediacore/auth"
)

// F4-08 B10l-op, real core: the admin user merge under a real identity boot.
// Nothing here is stubbed: core's kernel, its auth service and its real
// Admin.PlanUserMerge / StageUserMerge, books' Register wiring, the real merge
// routes behind core's stage 12, the real operations registry and the real
// grant -> Refresh -> one-batch apply.

// idDo is one request to the composed handler with the session cookie.
func idDo(t *testing.T, h http.Handler, method, path string, body any, ck *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:41000"
	if ck != nil {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func idLogin(t *testing.T, h http.Handler, user, pass string) *http.Cookie {
	t.Helper()
	rec := post(t, h, "/api/core/v1/auth/login", map[string]string{"username": user, "password": pass}, false)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	for _, ck := range rec.Result().Cookies() {
		if strings.HasSuffix(ck.Name, "session_id") {
			return ck
		}
	}
	t.Fatalf("no session cookie: %v", rec.Result().Header["Set-Cookie"])
	return nil
}

// idUserID is a user's core id by username, from the core users list.
func idUserID(t *testing.T, h http.Handler, ck *http.Cookie, username string) string {
	t.Helper()
	rec := idDo(t, h, http.MethodGet, "/api/core/v1/users?limit=200", nil, ck)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Items []struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"items"`
		Data struct {
			Items []struct {
				ID       string `json:"id"`
				Username string `json:"username"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	for _, u := range append(body.Items, body.Data.Items...) {
		if u.Username == username {
			return u.ID
		}
	}
	t.Fatalf("no user %q in %s", username, rec.Body.String())
	return ""
}

// idMergeEnv is a running identity boot with two core accounts, the bootstrap
// admin (the survivor and the actor) and Robin (the absorbed account, with one
// books row), and the admin's session.
type idMergeEnv struct {
	c              *composed
	h              http.Handler
	ck             *http.Cookie
	um             *usermerge.Service
	surv, absorbed string
	principal      func() *coreauth.Principal
	// adminName and adminPass sign the survivor in again: a merge that moves a
	// sign-in onto it ends its sessions (core bumps its credential epoch).
	adminName, adminPass string
}

func newIDMergeEnv(t *testing.T) *idMergeEnv {
	t.Helper()
	bootGrants := usermerge.DefaultGrants
	c := runComposedWith(t, func(cfg *config.Config) { cfg.EnableAuth = true }, nil, nil, nil).composed
	h := c.app.Handler()

	// The bootstrap admin is the survivor and the actor; Robin is invited and
	// accepts, so core holds a second account with a password sign-in.
	adminName, adminPass := redeemBootstrap(t, c)
	ck := idLogin(t, h, adminName, adminPass)
	inv := idDo(t, h, http.MethodPost, "/api/core/v1/users/invites",
		map[string]string{"username": "robin", "role": "viewer"}, ck)
	require.Equal(t, http.StatusCreated, inv.Code, inv.Body.String())
	var created struct {
		Link string `json:"link"`
		Data struct {
			Link string `json:"link"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(inv.Body.Bytes(), &created), inv.Body.String())
	link := created.Link + created.Data.Link
	token := link[strings.LastIndex(link, "token=")+len("token="):]
	acc := post(t, h, "/api/core/v1/auth/invites/accept", map[string]string{"token": token, "password": "alfred-pennyworth-1939!"}, false)
	require.Equal(t, http.StatusOK, acc.Code, acc.Body.String())

	e := &idMergeEnv{c: c, h: h, ck: ck, adminName: adminName, adminPass: adminPass, surv: idUserID(t, h, ck, adminName), absorbed: idUserID(t, h, ck, "robin")}
	require.NotEqual(t, e.surv, e.absorbed)

	// A books row of Robin's (the books half of the merge).
	require.NoError(t, c.store.SetUserPreferenceForUser(e.absorbed, "speed", "1.5"))

	// The wiring is live: the service is available under a real identity boot.
	e.um = c.srv.UserMerge()
	require.NotNil(t, e.um)
	require.NoError(t, e.um.CheckAvailable(), "a real identity boot hands the merge core's Admin, Users and Refresh")

	// A real principal, as stage 12 builds it for the admin's session. The merge
	// handler puts it in the grant it mints; the operation reads the process's
	// grant store when it runs, so with that store swapped for an empty one the
	// op finds nothing and writes nothing, and the grant the handler minted into
	// the boot-time store (held here as bootGrants) is ours to take. The token is
	// in the enqueued operation's params.
	e.principal = func() *coreauth.Principal {
		usermerge.DefaultGrants = usermerge.NewGrants()
		defer func() { usermerge.DefaultGrants = bootGrants }()
		path := "/api/v1/users/" + e.absorbed + "/merge-into/" + e.surv
		pv := idDo(t, h, http.MethodPost, path+"/preview", nil, ck)
		require.Equal(t, http.StatusOK, pv.Code, pv.Body.String())
		var plan struct {
			Digest string `json:"digest"`
			Data   struct {
				Digest string `json:"digest"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(pv.Body.Bytes(), &plan), pv.Body.String())
		ap := idDo(t, h, http.MethodPost, path+"/apply", map[string]any{"digest": plan.Digest + plan.Data.Digest}, ck)
		require.Equal(t, http.StatusAccepted, ap.Code, ap.Body.String())
		rows, err := c.store.ScanPrefix("")
		require.NoError(t, err)
		grantTok := regexp.MustCompile(`"grant":"([0-9a-f]{32})"`)
		for _, kv := range rows {
			if m := grantTok.FindSubmatch(kv.Value); m != nil {
				gr, ok := bootGrants.Take(string(m[1]))
				require.True(t, ok, "the grant the handler minted is not in the boot-time store")
				require.NotNil(t, gr.Principal, "the handler minted a grant without the request's principal")
				return gr.Principal
			}
		}
		t.Fatal("no enqueued merge operation carries a grant token")
		return nil
	}
	return e
}

func TestMergeIdentity_RealCoreMergesOneBatchAsTheRequestsAdmin(t *testing.T) {
	e := newIDMergeEnv(t)
	h, ck, um, surv, absorbed, c := e.h, e.ck, e.um, e.surv, e.absorbed, e.c

	path := "/api/v1/users/" + absorbed + "/merge-into/" + surv
	pv := idDo(t, h, http.MethodPost, path+"/preview", nil, ck)
	require.Equal(t, http.StatusOK, pv.Code, pv.Body.String())
	var plan struct {
		Digest string `json:"digest"`
		Data   struct {
			Digest string `json:"digest"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(pv.Body.Bytes(), &plan), pv.Body.String())
	digest := plan.Digest + plan.Data.Digest
	require.NotEmpty(t, digest, pv.Body.String())
	require.NotContains(t, pv.Body.String(), "alfred-pennyworth-1939!")

	ap := idDo(t, h, http.MethodPost, path+"/apply", map[string]any{"digest": digest}, ck)
	require.Equal(t, http.StatusAccepted, ap.Code, ap.Body.String())

	// The operation redeems the grant, re-validates the principal through core's
	// Refresh and applies the merge in one batch.
	require.Eventually(t, func() bool {
		js, err := um.ListJournals()
		return err == nil && len(js) == 1 && js[0].Status == "applied"
	}, 60*time.Second, 100*time.Millisecond, "the merge never applied")

	js, err := um.ListJournals()
	require.NoError(t, err)
	j, err := um.LoadJournal(js[0].ID)
	require.NoError(t, err)
	require.NotNil(t, j.Core, "an identity-era journal records core's half")
	require.Equal(t, surv, j.SurvivorID)
	require.Equal(t, absorbed, j.AbsorbedID)
	require.Equal(t, surv, j.ActorID)

	// Books moved its row; core disabled the source and marked it merged.
	got, err := c.store.GetUserPreferenceForUser(absorbed, "speed")
	require.NoError(t, err)
	require.Nil(t, got, "the absorbed account's preference row did not move")
	moved, err := c.store.GetUserPreferenceForUser(surv, "speed")
	require.NoError(t, err)
	require.NotNil(t, moved)
	require.Equal(t, "1.5", moved.Value)
	u := idDo(t, h, http.MethodGet, "/api/core/v1/users/"+absorbed, nil, ck)
	require.Equal(t, http.StatusOK, u.Code, u.Body.String())
	require.Contains(t, u.Body.String(), `"merged_into_user_id":"`+surv+`"`)
	require.Contains(t, u.Body.String(), `"disabled"`)
}

// TestMergeIdentity_RealCoreSealRefusesATamperedPrincipal: condition 5(c) against
// core's own Refresh and seal, not a stand-in. A module holding the principal
// stage 12 built can write its exported fields; Apply hands the principal to
// core's Refresh, whose seal check refuses one that no longer says what core
// recorded. Nothing is written for the refused apply, and the same apply with the
// untouched principal then lands, so the refusal was the seal and nothing else.
func TestMergeIdentity_RealCoreSealRefusesATamperedPrincipal(t *testing.T) {
	e := newIDMergeEnv(t)
	real := e.principal()
	require.Equal(t, e.surv, string(real.User.ID))

	ctx := coreauth.WithPrincipal(context.Background(), real)
	plan, err := e.um.BuildPlan(ctx, usermerge.PlanRequest{ActorID: e.surv, SurvivorID: e.surv, AbsorbedID: e.absorbed})
	require.NoError(t, err)
	req := usermerge.ApplyRequest{ActorID: e.surv, SurvivorID: e.surv, AbsorbedID: e.absorbed, Digest: plan.Digest,
		AcknowledgeOwnerIdentity: true, Principal: real}

	forged := *real
	forged.AuthTime = forged.AuthTime.Add(48 * time.Hour) // a "fresher sign-in" than core recorded
	bad := req
	bad.Principal = &forged
	_, err = e.um.Apply(context.Background(), bad, nil)
	require.ErrorIs(t, err, usermerge.ErrNoPrincipal, "core's seal let a rewritten principal through")
	row, err := e.c.store.GetUserPreferenceForUser(e.absorbed, "speed")
	require.NoError(t, err)
	require.NotNil(t, row, "a refused apply moved a books row")
	js, err := e.um.ListJournals()
	require.NoError(t, err)
	require.Empty(t, js, "a refused apply wrote a journal")

	_, err = e.um.Apply(context.Background(), req, nil)
	require.NoError(t, err, "the untouched principal must still be accepted")
	row, err = e.c.store.GetUserPreferenceForUser(e.absorbed, "speed")
	require.NoError(t, err)
	require.Nil(t, row)
}
