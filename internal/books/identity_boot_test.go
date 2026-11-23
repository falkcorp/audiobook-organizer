// file: internal/books/identity_boot_test.go
// version: 0.1.0
// guid: 5d9a2e74-1c83-4b06-a7f2-8e3b6c01d957
// last-edited: 2026-11-21

package books

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	system "github.com/falkcorp/audiobook-organizer/internal/server/handlers/system"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/storage"
)

// bootIdentity is a real composed boot (core's kernel, books' Register, the
// settings boot) with the identity predicate on and sign-in required.
func bootIdentity(t *testing.T, edit func(*config.Config)) *composed {
	t.Helper()
	return bootComposedWith(t, false, "", func(c *config.Config) {
		c.EnableAuth = true
		if edit != nil {
			edit(c)
		}
	}, nil, nil)
}

func TestIdentityBoot_Boots(t *testing.T) {
	c := bootIdentity(t, nil)
	require.True(t, c.srv.UnderCoreIdentity())
}

// statusDegraded is the authenticated GET /api/v1/system/status `degraded` list
// of a booted composition, read through the root handler a request reaches.
func statusDegraded(t *testing.T, c *composed) []string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	req.RemoteAddr = "127.0.0.1:41000"
	c.app.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Data struct {
			Degraded []string `json:"degraded"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return body.Data.Degraded
}

// authOffRemote boots the identity flip with core.auth.mode disabled on an
// install a remote caller can reach: the one posture core.auth reports
// (auth_disabled_remote), raised by a real boot.
func authOffRemote(o *app.Options) {
	o.ConfigFlags["core.auth.mode"] = "disabled"
	o.ConfigFlags["core.auth.disabled_allow_remote"] = "true"
	o.ConfigFlags["core.http.listen"] = "0.0.0.0:0"
}

// TestIdentityBoot_DegradedCarriesTheRegistryTokens: under the flip the
// authenticated `degraded` field carries the health registry's posture tokens
// (here core.auth's auth_disabled_remote), through a real ABO boot that Runs (the
// probes run at start), and the same auth-off mode on a loopback-only install
// raises none.
func TestIdentityBoot_DegradedCarriesTheRegistryTokens(t *testing.T) {
	t.Run("auth off and remote-reachable raises auth_disabled_remote", func(t *testing.T) {
		r := runComposedWith(t, nil, nil, authOffRemote, nil)
		// The probe's first result lands shortly after Run (the registry, not
		// the route, is polled: the route is rate limited). One request then
		// reads the field.
		require.Eventually(t, func() bool {
			return slices.ContainsFunc(r.app.HealthSnapshot().Components, func(c health.ComponentState) bool {
				return c.Result.Reason == "auth_disabled_remote"
			})
		}, 30*time.Second, 50*time.Millisecond, "core.auth never reported auth_disabled_remote")
		require.Contains(t, statusDegraded(t, r.composed), "auth_disabled_remote")
	})
	t.Run("auth off on loopback raises nothing", func(t *testing.T) {
		r := runComposedWith(t, nil, nil, func(o *app.Options) { o.ConfigFlags["core.auth.mode"] = "disabled" }, nil)
		// The probes have run once Run returned (start step 2 reports them).
		require.NotContains(t, statusDegraded(t, r.composed), "auth_disabled_remote")
	})
}

type fakePosture struct{ snap health.Snapshot }

func (fakePosture) CheckpointHealth() (storage.CheckpointHealth, error) {
	return storage.CheckpointHealth{}, nil
}
func (f fakePosture) HealthSnapshot() health.Snapshot { return f.snap }

// TestPostureTokens_RegistryTokensOnlyUnderTheFlip: the registry's tokens join
// the authenticated list under the flip, which is on for good since B10g (the
// off half, HEAD's checkpoint-only list, went with the selector).
func TestPostureTokens_RegistryTokensOnlyUnderTheFlip(t *testing.T) {
	src := fakePosture{snap: health.Snapshot{Components: []health.ComponentState{
		{ID: "core.auth", Result: health.Result{Status: health.StatusDegraded, Reason: "auth_disabled_remote"}},
		{ID: "core.backup", Result: health.Result{Status: health.StatusUp}},
	}}}
	t.Run("on", func(t *testing.T) {
		require.Equal(t, []system.PostureToken{{Token: "auth_disabled_remote", Subject: "core.auth"}}, postureTokens(src, time.Now()))
	})
}
