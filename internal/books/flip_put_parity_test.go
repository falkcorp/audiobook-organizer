// file: internal/books/flip_put_parity_test.go
// version: 1.0.0
// guid: 7e2c5b91-3d46-4a08-b7f5-1c9a8e6d4f23
// last-edited: 2026-11-20

package books

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/app"
	coreconfig "github.com/falkcorp/mediacore/config"
)

// F3-07 ruling 4: every refusal HEAD's PUT /api/v1/config (UpdateService) made
// is either made by the flipped Engine, or replaced by a named equivalent. One
// subtest per check, each on the real core through Facade.Update, each proving
// nothing moved (generation and the stored value) when the PUT is refused.

// putUpdate runs one Update on the flipped boot and returns the result, the
// error, and whether the generation moved.
func putUpdate(t *testing.T, f config.Facade, set map[string]any, unset ...string) (config.UpdateResult, error, bool) {
	t.Helper()
	raw := map[string]json.RawMessage{}
	for k, v := range set {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		raw[k] = b
	}
	g := f.Generation()
	res, err := f.Update(t.Context(), settingsAdmin, config.Update{Set: raw, Unset: unset})
	return res, err, f.Generation() != g
}

func mustRefuse(t *testing.T, f config.Facade, set map[string]any) error {
	t.Helper()
	_, err, moved := putUpdate(t, f, set)
	if err == nil {
		t.Fatalf("PUT %v was accepted; HEAD refused it", set)
	}
	if moved {
		t.Fatalf("PUT %v was refused (%v) but the generation moved: a refused write must change nothing", set, err)
	}
	return err
}

func TestFlipPUTParity_RemovedKeysRefusedWithTheirReason(t *testing.T) {
	bootFlippedFresh(t)
	f := config.Current()
	for key, want := range map[string]string{
		"enable_sqlite":                        "SQLite is no longer selectable",
		"enable_sqlite3_i_know_the_risks":      "SQLite is no longer selectable",
		"auto_fetch_metadata":                  "nothing ever read it",
		"write_backup_before_tag_write":        "create_backups is the one switch",
		"metadata_scoring.write_backup_before": "create_backups is the one switch",
		"download_client.usenet":               "qBittorrent and SABnzbd",
		"download_client.usenet.api_key":       "qBittorrent and SABnzbd",
		"log_format":                           "nothing ever read it",
		"auth_rate_limit_per_minute":           "fixed per-IP and per-account throttles",
		"review_metadata_server_query":         "always asks the server for one filtered page",
		"activity_db_path":                     "no separate activity database",
		"invite_cf_access_client_id":           "guests now get through Cloudflare by email login",
		"api_keys":                             "nothing ever read it",
		"memory_limit_mb":                      "nothing ever read it",
	} {
		t.Run(key, func(t *testing.T) {
			err := mustRefuse(t, f, map[string]any{key: true})
			if !errors.Is(err, coreconfig.ErrUnknownKey) {
				t.Fatalf("want ErrUnknownKey, got %v", err)
			}
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("the refusal must carry HEAD's removal reason %q, got %v", want, err)
			}
		})
	}
}

func TestFlipPUTParity_UnknownAndEnvOnlyAndImmutable(t *testing.T) {
	bootFlippedFresh(t)
	f := config.Current()
	t.Run("unknown key", func(t *testing.T) {
		err := mustRefuse(t, f, map[string]any{"dedup_auto_merge_enabled": true})
		if !errors.Is(err, coreconfig.ErrUnknownKey) {
			t.Fatalf("got %v", err)
		}
	})
	// HEAD's inviteEnvOnlySettings that stay read-only: the SMTP block is a
	// passthrough to core's notify plugin, and the Cloudflare policy token is one of
	// the two operator-only secrets (05-config 1.3.7, SEC-6).
	for _, key := range []string{"smtp_host", "smtp_port", "smtp_username", "cf_policy_api_token"} {
		t.Run("operator-only "+key, func(t *testing.T) {
			err := mustRefuse(t, f, map[string]any{key: "1"})
			var prot *coreconfig.ProtectedError
			if !errors.Is(err, config.ErrKeyReadOnly) && !errors.As(err, &prot) {
				t.Fatalf("want a read-only or protected-operator refusal, got %v", err)
			}
		})
	}
	t.Run("immutable database_type", func(t *testing.T) {
		_ = mustRefuse(t, f, map[string]any{"database_type": "sqlite"})
	})
}

func TestFlipPUTParity_ValidateClassesRefused(t *testing.T) {
	bootFlippedFresh(t)
	f := config.Current()
	for name, set := range map[string]map[string]any{
		"embedding vector backend":  {"embedding.vector_backend": "typo"},
		"backup codec":              {"backup_compression": "bogus"},
		"negative concurrent scans": {"concurrent_scans": -1},
		"standdown above bound":     {"scan_standdown_grace_seconds": 601},
		"activity backend sqlite":   {"activity_backend": "sqlite"},
		"dedup ladder out of order": {"dedup.signals.band_certain_min": 0.1},
		"organization strategy":     {"organization_strategy": "nonsense"},
	} {
		t.Run(name, func(t *testing.T) {
			err := mustRefuse(t, f, set)
			if !errors.Is(err, coreconfig.ErrInvalid) && !errors.Is(err, config.ErrKeyReadOnly) && !errors.Is(err, coreconfig.ErrLocked) {
				t.Fatalf("a Validate failure must be a refusal the handler maps (422/400/409), got %v", err)
			}
		})
	}
}

func TestFlipPUTParity_EndpointsValidated(t *testing.T) {
	bootFlippedFresh(t)
	f := config.Current()
	row := func(id, proto, url string) map[string]any {
		return map[string]any{"id": id, "protocol": proto, "url": url, "enabled": true, "capabilities": []string{}}
	}
	for name, rows := range map[string][]map[string]any{
		"unknown protocol":   {row("a", "nope", "http://192.0.2.5")},
		"duplicate id":       {row("a", "openai_compat", "http://192.0.2.5"), row("a", "openai_compat", "http://192.0.2.6")},
		"embedded creds":     {row("a", "openai_compat", "http://u:p@192.0.2.5")},
		"duplicate url pair": {row("a", "openai_compat", "http://192.0.2.5"), row("b", "openai_compat", "http://192.0.2.5/")},
		"missing id":         {row("", "openai_compat", "http://192.0.2.5")},
	} {
		t.Run(name, func(t *testing.T) {
			err := mustRefuse(t, f, map[string]any{"ai_endpoints": rows})
			if !errors.Is(err, coreconfig.ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}
}

func TestFlipPUTParity_MaskShapedSecretsRefused(t *testing.T) {
	bootFlippedFresh(t)
	f := config.Current()
	for _, key := range []string{"openai_api_key", "acoustid_api_key", "google_books_api_key", "hardcover_api_token", "basic_auth_password", "oauth_github_client_secret", "deluge_web_password"} {
		t.Run(key, func(t *testing.T) {
			err := mustRefuse(t, f, map[string]any{key: "sk-****abcd"})
			if !errors.Is(err, coreconfig.ErrInvalid) || !strings.Contains(err.Error(), "masked") {
				t.Fatalf("want a masked-value refusal, got %v", err)
			}
		})
	}
	t.Run("metadata source credential", func(t *testing.T) {
		src := []map[string]any{{"id": "hardcover", "credentials": map[string]string{"api_token": "****"}}}
		err := mustRefuse(t, f, map[string]any{"metadata_sources": src})
		if !strings.Contains(err.Error(), "masked") {
			t.Fatalf("got %v", err)
		}
	})
}

// The one HEAD refusal with a different equivalent: enabled_sort_indexes is read
// once at start. HEAD refused a change; the flipped key is reload:"restart", so
// the write is stored and reported restart-pending instead of being refused.
func TestFlipPUTParity_SortIndexesAreRestartBound(t *testing.T) {
	bootFlippedFresh(t)
	f := config.Current()
	res, err, moved := putUpdate(t, f, map[string]any{"enabled_sort_indexes": []string{"year"}})
	if err != nil || !moved {
		t.Fatalf("err=%v moved=%v", err, moved)
	}
	if !slices.Contains(res.RestartPending, "enabled_sort_indexes") {
		t.Fatalf("a boot-only key must be reported restart-pending, got %v", res.RestartPending)
	}
}

// 05-config 1.3.7: "HEAD's file-only keys ... become ordinary keys with their
// declared class in R2, with two exceptions that stay operator-only", so the rest
// of HEAD's environment-only block is storable now and an admin session may write
// it. (HEAD refused a PUT naming one because a stored value was never read.)
func TestFlipPUTParity_FormerlyFileOnlyKeysAreOrdinaryKeys(t *testing.T) {
	bootFlippedFresh(t)
	f := config.Current()
	for key, val := range map[string]any{
		"cf_account_id":          "acct-1",
		"cf_guests_policy_id":    "policy-1",
		"login_saturated_rate":   5.0,
		"login_saturated_burst":  7,
		"public_base_url":        "https://192.0.2.10",
		"allowed_peer_cidrs":     "192.0.2.0/24",
		"tunnel_connector_addrs": "192.0.2.1",
	} {
		t.Run(key, func(t *testing.T) {
			if _, err, moved := putUpdate(t, f, map[string]any{key: val}); err != nil || !moved {
				t.Fatalf("an admin session must be able to store %s: err=%v moved=%v", key, err, moved)
			}
		})
	}
}

// apiKeyActor is a credential that may do everything its scopes allow (the
// full-scope bootstrap key) but is not an interactive session.
// (The handler grants FirstRunPerm only in the first-run window, never to a key.)
var apiKeyActor = config.Actor{Subject: "key:bootstrap", Interactive: false, Can: func(p string) bool { return p != config.FirstRunPerm }}

// signInFamilies is one write per family of settings HEAD refused to an API key.
var signInFamilies = map[string]map[string]any{
	"sign-in switch":      {"basic_auth_enabled": true},
	"basic credentials":   {"basic_auth_username": "robin"},
	"basic password":      {"basic_auth_password": "correct-horse-battery"},
	"oauth switch":        {"oauth_enabled": true},
	"oauth client id":     {"oauth_github_client_id": "Iv1.alfred"},
	"oauth client secret": {"oauth_github_client_secret": "gh-secret-value"},
	"oauth redirect":      {"oauth_redirect_base_url": "https://192.0.2.20"},
	"abs sign-in switch":  {"abs_api_enabled": true},
	"abs auth modes":      {"abs_auth_modes": "jwt"},
	"abs token lifetime":  {"abs_access_token_ttl": "2h"},
	"abs refresh":         {"abs_refresh_token_ttl": "48h"},
	"abs session max":     {"abs_session_max_ttl": "96h"},
	"abs refresh grace":   {"abs_refresh_grace": "1m"},
	"startup read-only":   {"write_startup_readonly_key": true},
	"peer allow-list":     {"allowed_peer_cidrs": "192.0.2.0/24"},
	"trusted proxies":     {"tunnel_connector_addrs": "192.0.2.1"},
	"public base url":     {"public_base_url": "https://192.0.2.10"},
}

// Ruling: HEAD answered 403 when an API key (including the bootstrap key) PUT a
// sign-in, executable, path or database setting. ProtectAdmin is checked as
// Can("core:admin") && Interactive (05-config 1.3.7), so the same PUT from an API
// key must be refused as protected and from a session must not be.
func TestFlipPUTParity_APIKeyCannotChangeProtectedFamilies(t *testing.T) {
	bootFlippedFresh(t)
	f := config.Current()
	families := signInFamilies
	for name, set := range families {
		raw := map[string]json.RawMessage{}
		for k, v := range set {
			b, _ := json.Marshal(v)
			raw[k] = b
		}
		t.Run(name+"/api key refused", func(t *testing.T) {
			g := f.Generation()
			_, err := f.Update(t.Context(), apiKeyActor, config.Update{Set: raw})
			var prot *coreconfig.ProtectedError
			if !errors.As(err, &prot) && !errors.Is(err, coreconfig.ErrForbidden) {
				t.Fatalf("an API key must be refused as protected, got %v", err)
			}
			if f.Generation() != g {
				t.Fatal("a refused write moved the generation")
			}
		})
		t.Run(name+"/session allowed", func(t *testing.T) {
			if _, err := f.Update(t.Context(), settingsAdmin, config.Update{Set: raw}); err != nil {
				t.Fatalf("a signed-in admin session must be allowed: %v", err)
			}
		})
	}
}

// windowKeyActor is what the handler builds for a key-authenticated request while
// the first-run window is open (no user exists, setup is not complete): not an
// interactive session, and FirstRunPerm granted by the window whoever calls. The
// window must open the wizard's keys and nothing else, so a sign-in key is still
// refused for it, alone or beside a wizard key.
var windowKeyActor = config.Actor{Subject: "key:bootstrap", Interactive: false, Can: func(string) bool { return true }}

func TestFlipPUTParity_APIKeyInTheFirstRunWindowStillCannotChangeSignIn(t *testing.T) {
	bootFlippedFresh(t)
	f := config.Current()
	wizard := map[string]any{"setup_complete": true}
	for name, one := range signInFamilies {
		for shape, set := range map[string]map[string]any{"alone": one, "beside a wizard key": merged(wizard, one)} {
			raw := map[string]json.RawMessage{}
			for k, v := range set {
				b, _ := json.Marshal(v)
				raw[k] = b
			}
			t.Run(name+"/"+shape, func(t *testing.T) {
				g := f.Generation()
				_, err := f.Update(t.Context(), windowKeyActor, config.Update{Set: raw})
				if err == nil || f.Generation() != g {
					t.Fatalf("the first-run window must not let a key change %v: err=%v", one, err)
				}
			})
		}
	}
}

func merged(a, b map[string]any) map[string]any {
	m := map[string]any{}
	maps.Copy(m, a)
	maps.Copy(m, b)
	return m
}

// HEAD's owner trust root with no owner configured: refused for EVERY caller, a
// signed-in admin included (owner decision 2026-10-07, SEC3-6), because the owner
// is set on the server, not through a request. The flipped path keeps that.
func TestFlipPUTParity_OwnerTrustRootRefusedForEveryCallerWithNoOwner(t *testing.T) {
	bootFlippedFresh(t)
	f := config.Current()
	for _, set := range []map[string]any{
		{"owner_email": "bruce@wayne.example"},
		{"cf_access_team_domain": "wayne.example"},
		{"cf_access_aud": "aud-1"},
	} {
		for who, actor := range map[string]config.Actor{"api key": apiKeyActor, "admin session": settingsAdmin} {
			raw := map[string]json.RawMessage{}
			for k, v := range set {
				b, _ := json.Marshal(v)
				raw[k] = b
			}
			g := f.Generation()
			_, err := f.Update(t.Context(), actor, config.Update{Set: raw})
			if err == nil || f.Generation() != g {
				t.Fatalf("%s: %v must be refused with nothing written, got err=%v", who, set, err)
			}
			if !strings.Contains(err.Error(), "No owner is configured") {
				t.Fatalf("%s: %v: want the no-owner refusal HEAD gave, got %v", who, set, err)
			}
		}
	}
}

// HEAD locked the Deluge connection to its environment (envLockedSettings) but
// ACCEPTED and stored a PUT naming a locked key, then overrode it at the next
// boot. Core refuses the write (409 locked) and the View says what locks the key,
// which is the Deluge env-lock check kept, with a stricter answer.
func TestFlipPUTParity_DelugeEnvLockRefusesTheWrite(t *testing.T) {
	stateDir := t.TempDir()
	bootComposedWith(t, false, stateDir, func(c *config.Config) {
		c.RootDir = ""
		c.SetupComplete = false
	}, nil, func(o *app.Options) {
		o.StateDir = stateDir
		// The boot sees only its own environment (the sandbox's retired
		// ABK_STATE_DIR is not in it): the operator names the Deluge URL.
		o.Env = bootEnv("ABO_INTEGRATIONS_DOWNLOAD_DELUGE_URL=http://192.0.2.9:8112")
	})
	f := config.Current()
	err := mustRefuse(t, f, map[string]any{"deluge_web_url": "http://192.0.2.77:8112"})
	var locked *coreconfig.LockedError
	if !errors.As(err, &locked) && !errors.Is(err, config.ErrKeyReadOnly) {
		t.Fatalf("want a locked (409) or read-only refusal, got %v", err)
	}
	if got := f.Snapshot().DelugeWebURL; got != "http://192.0.2.9:8112" {
		t.Fatalf("the operator's value must be the effective one (HEAD: env wins), got %q", got)
	}
	var lockedBy string
	for _, k := range f.View().Keys {
		if k.Name == "deluge_web_url" {
			lockedBy = k.LockedBy
		}
	}
	if !strings.Contains(lockedBy, "ABO_INTEGRATIONS_DOWNLOAD_DELUGE_URL") {
		t.Fatalf("the View must say the environment locks deluge_web_url, got LockedBy=%q", lockedBy)
	}
}

// The Deluge password lives behind the secret path, not the residual blob: with the
// operator's environment naming it, a write is still refused and the value stays
// the operator's, and the View says what locks it.
func TestFlipPUTParity_DelugePasswordEnvLockRefusesTheWrite(t *testing.T) {
	stateDir := t.TempDir()
	bootComposedWith(t, false, stateDir, func(c *config.Config) {
		c.RootDir = ""
		c.SetupComplete = false
	}, nil, func(o *app.Options) {
		o.StateDir = stateDir
		o.Env = bootEnv("ABO_INTEGRATIONS_DOWNLOAD_DELUGE_PASSWORD=operator-secret")
	})
	f := config.Current()
	err := mustRefuse(t, f, map[string]any{"deluge_web_password": "another-secret"})
	var locked *coreconfig.LockedError
	if !errors.As(err, &locked) && !errors.Is(err, config.ErrKeyReadOnly) {
		t.Fatalf("want a locked (409) or read-only refusal, got %v", err)
	}
	if got := f.Snapshot().DelugeWebPassword; got != "operator-secret" {
		t.Fatalf("the operator's value must be the effective one, got %q", got)
	}
	var lockedBy string
	for _, k := range f.View().Keys {
		if k.Name == "deluge_web_password" {
			lockedBy = k.LockedBy
		}
	}
	if !strings.Contains(lockedBy, "ABO_INTEGRATIONS_DOWNLOAD_DELUGE_PASSWORD") {
		t.Fatalf("the View must say the environment locks the password, got LockedBy=%q", lockedBy)
	}
}

// The ecosystem name OPENAI_API_KEY is bound to a passthrough secret path. HEAD
// read it into the setting and the settings page showed it as environment-set; the
// value an operator supplies is the effective one and a write is refused.
func TestFlipPUTParity_OpenAIKeyFromTheEcosystemEnvIsEffectiveAndLocked(t *testing.T) {
	stateDir := t.TempDir()
	bootComposedWith(t, false, stateDir, func(c *config.Config) {
		c.RootDir = ""
		c.SetupComplete = false
	}, nil, func(o *app.Options) {
		o.StateDir = stateDir
		o.Env = bootEnv("OPENAI_API_KEY=bane")
	})
	f := config.Current()
	if got := f.Snapshot().OpenAIAPIKey; got != "bane" {
		t.Fatalf("the ecosystem env must reach the setting, got %q", got)
	}
	err := mustRefuse(t, f, map[string]any{"openai_api_key": "another-key"})
	if _, ok := errors.AsType[*coreconfig.LockedError](err); !ok {
		t.Fatalf("want a locked refusal, got %v", err)
	}
}
