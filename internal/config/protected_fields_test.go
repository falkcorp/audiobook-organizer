// file: internal/config/protected_fields_test.go
// version: 1.2.0
// guid: 4f2a8c61-d93e-4b07-a5c8-1e6b3d9f7a20
// last-edited: 2026-10-07

package config

import (
	"context"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
)

// sensitiveFieldName matches a config field whose name says it is a path, a
// program, a database, an endpoint or a sign-in setting. Every such field must
// be classified in configFieldRules, protected or not, so adding one is a
// decision someone makes rather than a default nobody saw.
var sensitiveFieldName = regexp.MustCompile(
	`(path|dir|root|url|endpoint|host|command|cmd|binary|exec|tool|file|folder|pattern|mapping|alias|settings|location|mount|socket|plugin|_mode$|database|_db|(^|[._])auth($|[._])|oauth|cf_access|password|secret|token|_key$|login|session|owner|email|role|abs_|rate_limit)`)

// configLeafPaths lists every decodable leaf of Config as a JSON path in the
// rules' notation ("[]" for each slice element or map value).
func configLeafPaths() []string {
	var out []string
	var walk func(t reflect.Type, prefix string)
	walk = func(t reflect.Type, prefix string) {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		switch t.Kind() {
		case reflect.Struct:
			if pt := reflect.PointerTo(t); pt.Implements(jsonUnmarshalerType) || pt.Implements(textUnmarshalerType) || t.PkgPath() == "time" {
				out = append(out, prefix)
				return
			}
			for i := range t.NumField() {
				f := t.Field(i)
				if !f.IsExported() {
					continue
				}
				tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
				if tag == "-" {
					continue
				}
				if tag == "" {
					tag = f.Name
				}
				walk(f.Type, joinKeyPath(prefix, tag))
			}
		case reflect.Slice, reflect.Map:
			et := t.Elem()
			for et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct || et.Kind() == reflect.Map {
				walk(et, prefix+"[]")
				return
			}
			out = append(out, prefix)
		default:
			out = append(out, prefix)
		}
	}
	walk(reflect.TypeFor[Config](), "")
	return out
}

func classifiedAt(path string) bool {
	for p := path; p != ""; p = parentFieldPath(p) {
		if _, ok := configFieldRules[p]; ok {
			return true
		}
	}
	return false
}

// TestConfigFieldRules_EveryPathLikeFieldIsClassified fails when someone adds a
// path-, program-, endpoint- or sign-in-shaped field to Config without saying
// whether an API key may change it.
func TestConfigFieldRules_EveryPathLikeFieldIsClassified(t *testing.T) {
	leaves := configLeafPaths()
	if len(leaves) < 200 {
		t.Fatalf("walked only %d config leaves; the walker is broken", len(leaves))
	}
	var missing []string
	for _, p := range leaves {
		// Match on the field's own name segments, not the whole path, so a
		// harmless leaf below a classified parent is not double-counted and a
		// sensitive leaf below an unclassified parent is still caught.
		if sensitiveFieldName.MatchString(p) && !classifiedAt(p) {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		t.Errorf("config fields look like a path, program, endpoint or sign-in setting but have no entry in configFieldRules (protected_fields.go); classify each one:\n  %s",
			strings.Join(missing, "\n  "))
	}
}

// TestConfigFieldRules_EveryRuleResolves checks the list in the other
// direction: a rule naming a field that was renamed or removed would protect
// nothing (ChangedProtectedFields fails closed on it, but say why here).
func TestConfigFieldRules_EveryRuleResolves(t *testing.T) {
	cfg := Config{Plugins: map[string]PluginConfig{"p": {}}, MetadataSources: []MetadataSource{{}}}
	for path, rule := range configFieldRules {
		if rule.Why == "" {
			t.Errorf("rule %q has no reason", path)
		}
		if _, err := projectField(reflect.ValueOf(cfg), strings.Split(path, ".")); err != nil {
			t.Errorf("rule %q does not resolve: %v", path, err)
		}
	}
}

func TestChangedProtectedFields(t *testing.T) {
	base := Config{
		EnableAuth: true,
		RootDir:    "/library",
		Plugins:    map[string]PluginConfig{"p": {Enabled: true, Settings: map[string]string{"k": "v"}}},
	}
	cases := []struct {
		name   string
		mutate func(c *Config)
		want   []string
	}{
		{"nothing", func(*Config) {}, nil},
		{"unprotected field", func(c *Config) { c.ConcurrentScans = 9; c.OpenAIBaseURL = "http://x" }, nil},
		{"plugin toggle", func(c *Config) {
			c.Plugins = map[string]PluginConfig{"p": {Enabled: false, Settings: map[string]string{"k": "v"}}}
		}, nil},
		{"sign-in", func(c *Config) { c.OAuthDefaultRole = "admin" }, []string{"oauth_default_role"}},
		{"owner email", func(c *Config) { c.OwnerEmail = "someone@example.test" }, []string{"owner_email"}},
		{"server path", func(c *Config) { c.RootDir = "/elsewhere" }, []string{"root_dir"}},
		{"database", func(c *Config) { c.ActivityDBPath = "/tmp/a.db" }, []string{"activity_db_path"}},
		{"executable", func(c *Config) { c.Tools.Fpcalc.CustomPath = "/bin/sh" }, []string{"tools.fpcalc.custom_path"}},
		{"plugin settings", func(c *Config) {
			c.Plugins = map[string]PluginConfig{"p": {Enabled: true, Settings: map[string]string{"k": "/bin/sh"}}}
		}, []string{"plugins[].settings"}},
		{"path mapping", func(c *Config) { c.ITunes.PathMappings = []ITunesPathMap{{From: "a", To: "b"}} }, []string{"itunes.path_mappings"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			after := base.Clone()
			tc.mutate(after)
			before := base.Clone()
			if got := ChangedProtectedFields(before, after); !slices.Equal(got, tc.want) {
				t.Errorf("ChangedProtectedFields = %v, want %v", got, tc.want)
			}
		})
	}
}

// updateAs runs UpdateConfig as a caller authenticated by m, on a config with
// auth on, and returns the status, the response and the config afterwards.
func updateAs(t *testing.T, m auth.Method, authOn bool, payload map[string]any) (int, map[string]any, Config) {
	t.Helper()
	return updateWith(t, auth.WithMethod(context.Background(), m), authOn, "", payload)
}

// updateWith runs UpdateConfig with ctx as the caller, on a config whose
// owner_email is owner.
func updateWith(t *testing.T, ctx context.Context, authOn bool, owner string, payload map[string]any) (int, map[string]any, Config) {
	t.Helper()
	orig := AppConfig
	t.Cleanup(func() { AppConfig = orig })
	AppConfig = Config{
		EnableAuth:         authOn,
		RootDir:            t.TempDir(),
		OAuthDefaultRole:   "viewer",
		DatabaseType:       "pebble",
		OwnerEmail:         owner,
		CFAccessTeamDomain: "team.example.test",
		CFAccessAUD:        "aud-1",
	}
	store := mocks.NewMockStore(t)
	store.On("SetSetting", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	store.On("GetSetting", mock.Anything).Return((*database.Setting)(nil), nil).Maybe()
	status, resp := NewUpdateService(store).UpdateConfig(ctx, payload)
	return status, resp, Snapshot()
}

// TestUpdateConfig_ProtectedFieldsNeedInteractiveSession is the 2026-10-07
// security-review regression: the sign-in check used to compare payload KEYS
// exactly while the decoder matched them case-insensitively, so an API key's
// {"OAuth_Default_Role":"admin"} was decoded onto oauth_default_role and
// answered 200.
func TestUpdateConfig_ProtectedFieldsNeedInteractiveSession(t *testing.T) {
	t.Run("case variant of a sign-in key is refused", func(t *testing.T) {
		status, _, cfg := updateAs(t, auth.MethodAPIKey, true, map[string]any{"OAuth_Default_Role": "admin"})
		if status < 400 {
			t.Fatalf("status = %d, want a refusal", status)
		}
		if cfg.OAuthDefaultRole != "viewer" {
			t.Errorf("oauth_default_role = %q, want it unchanged", cfg.OAuthDefaultRole)
		}
	})
	t.Run("case variant of the immutable key is refused", func(t *testing.T) {
		status, _, cfg := updateAs(t, auth.MethodSession, true, map[string]any{"Database_Type": "sqlite"})
		if status != http.StatusBadRequest || cfg.DatabaseType != "pebble" {
			t.Errorf("status = %d, database_type = %q; want 400 and pebble", status, cfg.DatabaseType)
		}
	})
	refusedForKey := []map[string]any{
		{"oauth_default_role": "admin"},
		{"owner_email": "someone@example.test"},
		{"root_dir": "/elsewhere"},
		{"database_path": "/elsewhere/db"},
		{"tools": map[string]any{"fpcalc": map[string]any{"custom_path": "/bin/sh"}}},
		{"itunes": map[string]any{"library_write_path": "/elsewhere/lib.xml"}},
		{"plugins": map[string]any{"p": map[string]any{"settings": map[string]any{"cmd": "/bin/sh"}}}},
	}
	for _, payload := range refusedForKey {
		for _, m := range []auth.Method{auth.MethodAPIKey, auth.MethodABS, auth.MethodNone} {
			t.Run("refused/"+string(m)+"/"+firstKey(payload), func(t *testing.T) {
				status, resp, _ := updateAs(t, m, true, payload)
				if status != http.StatusForbidden {
					t.Fatalf("status = %d (%v), want 403", status, resp["error"])
				}
				if keys, _ := resp["refused_keys"].([]string); len(keys) == 0 {
					t.Errorf("refused_keys missing: %v", resp)
				}
			})
		}
		if firstKey(payload) == "owner_email" {
			// Who may set owner_email at all: TestUpdateConfig_OwnerTrustRoot.
			continue
		}
		t.Run("session allowed/"+firstKey(payload), func(t *testing.T) {
			status, resp, _ := updateAs(t, auth.MethodSession, true, payload)
			if status == http.StatusForbidden {
				t.Errorf("a signed-in session was refused: %v", resp["error"])
			}
		})
		t.Run("auth off allowed/"+firstKey(payload), func(t *testing.T) {
			status, resp, _ := updateAs(t, auth.MethodNone, false, payload)
			if status == http.StatusForbidden {
				t.Errorf("refused with auth off: %v", resp["error"])
			}
		})
	}
	t.Run("same-value round trip is allowed for a key", func(t *testing.T) {
		orig := AppConfig
		t.Cleanup(func() { AppConfig = orig })
		status, resp, _ := updateAs(t, auth.MethodAPIKey, true, map[string]any{"oauth_default_role": "viewer", "enable_auth": true, "concurrent_scans": 3})
		if status != http.StatusOK {
			t.Errorf("status = %d (%v), want 200", status, resp["error"])
		}
	})
}

func firstKey(m map[string]any) string {
	for k := range m {
		return k
	}
	return ""
}

// sessionCtx is the context of a signed-in admin: the caller every
// UpdateConfig test that is not about the interactive-session rule assumes.
func sessionCtx() context.Context {
	return auth.WithMethod(context.Background(), auth.MethodSession)
}

// accessCtx is a request signed in through Cloudflare Access as email.
func accessCtx(email string) context.Context {
	return auth.WithAccessEmail(auth.WithMethod(context.Background(), auth.MethodCFAccess), email)
}

// TestUpdateConfig_OwnerTrustRoot is the 2026-10-07 second review's BLOCKER:
// any interactive session (a second admin through Access, a stolen password
// session) and any auth-off request could change owner_email or cf_access_*,
// making itself the owner. Once an owner is set only the owner may change the
// trust root; while none is set nobody may through the API (host only).
func TestUpdateConfig_OwnerTrustRoot(t *testing.T) {
	const owner = "owner@example.test"
	session := auth.WithMethod(context.Background(), auth.MethodSession)
	apiKey := auth.WithMethod(context.Background(), auth.MethodAPIKey)
	trustRoot := []map[string]any{
		{"owner_email": "other@example.test"},
		{"owner_email": ""},
		{"cf_access_team_domain": "attacker.example.test"},
		{"cf_access_aud": "aud-attacker"},
		{"enable_auth": false},
		{"oauth_allowed_emails": "other@example.test"},
	}
	refusedCallers := []struct {
		name   string
		ctx    context.Context
		authOn bool
	}{
		{"another admin through Access", accessCtx("other@example.test"), true},
		{"a Kelvin-sign look-alike through Access", accessCtx("owner@example.tes\u212A"), true},
		{"password session", session, true},
		{"API key", apiKey, true},
		{"auth off, no sign-in", context.Background(), false},
		{"auth off, another admin through Access", accessCtx("other@example.test"), false},
	}
	for _, payload := range trustRoot {
		for _, c := range refusedCallers {
			t.Run("owner set/refused/"+c.name+"/"+firstKey(payload), func(t *testing.T) {
				p := payload
				if _, ok := p["enable_auth"]; ok {
					p = map[string]any{"enable_auth": !c.authOn} // a real change either way
				}
				status, resp, cfg := updateWith(t, c.ctx, c.authOn, owner, p)
				if status != http.StatusForbidden {
					t.Fatalf("status = %d (%v), want 403", status, resp["error"])
				}
				if cfg.OwnerEmail != owner || cfg.CFAccessTeamDomain != "team.example.test" || cfg.CFAccessAUD != "aud-1" || cfg.EnableAuth != c.authOn {
					t.Errorf("trust root changed: %+v", cfg)
				}
			})
		}
		t.Run("owner set/the owner may/"+firstKey(payload), func(t *testing.T) {
			status, resp, _ := updateWith(t, accessCtx(owner), true, owner, payload)
			if status != http.StatusOK {
				t.Errorf("status = %d (%v), want 200", status, resp["error"])
			}
		})
	}
	t.Run("owner set/unrelated settings stay open to a session", func(t *testing.T) {
		status, resp, _ := updateWith(t, session, true, owner, map[string]any{"concurrent_scans": 7, "owner_email": owner})
		if status != http.StatusOK {
			t.Errorf("status = %d (%v), want 200", status, resp["error"])
		}
	})

	// No owner yet: every trust-root field is refused through the API for
	// every caller, the would-be owner's own Access sign-in included (owner
	// decision 2026-10-07: the owner is set on the host, OWNER_EMAIL). The
	// first-set-through-Access path let any admitted Access user, or a
	// session that first repointed cf_access_*, make itself the owner.
	noOwnerCallers := []struct {
		name   string
		ctx    context.Context
		authOn bool
	}{
		{"that person through Access", accessCtx(owner), true},
		{"password session", session, true},
		{"API key", apiKey, true},
		{"another person through Access", accessCtx("other@example.test"), true},
		{"auth off, no sign-in", context.Background(), false},
		{"auth off, that person through Access", accessCtx(owner), false},
	}
	for _, payload := range append(trustRoot[:1:1], map[string]any{"owner_email": owner},
		map[string]any{"cf_access_team_domain": "new.example.test"}, map[string]any{"cf_access_aud": "aud-2"},
		map[string]any{"oauth_allowed_emails": owner}, map[string]any{"enable_auth": true}) {
		for _, c := range noOwnerCallers {
			t.Run("no owner/refused/"+c.name+"/"+firstKey(payload), func(t *testing.T) {
				p := payload
				if _, ok := p["enable_auth"]; ok {
					p = map[string]any{"enable_auth": !c.authOn}
				}
				status, resp, cfg := updateWith(t, c.ctx, c.authOn, "", p)
				if status != http.StatusForbidden {
					t.Fatalf("status = %d (%v), want 403", status, resp["error"])
				}
				if msg, _ := resp["error"].(string); !strings.Contains(msg, "OWNER_EMAIL") {
					t.Errorf("message does not point at the host setting: %q", msg)
				}
				if keys, _ := resp["refused_keys"].([]string); len(keys) == 0 {
					t.Errorf("refused_keys missing: %v", resp)
				}
				if cfg.OwnerEmail != "" || cfg.CFAccessTeamDomain != "team.example.test" || cfg.CFAccessAUD != "aud-1" || cfg.EnableAuth != c.authOn {
					t.Errorf("trust root changed: %+v", cfg)
				}
			})
		}
	}
	t.Run("no owner/unrelated settings stay open to a session", func(t *testing.T) {
		status, resp, _ := updateWith(t, session, true, "", map[string]any{"concurrent_scans": 7})
		if status != http.StatusOK {
			t.Errorf("status = %d (%v), want 200", status, resp["error"])
		}
	})
}

// Every owner trust-root field resolves and is also a sign-in rule, so an
// API key is refused on it before the owner check is even reached.
func TestOwnerTrustRootFields_AreSignInRules(t *testing.T) {
	for path, why := range ownerTrustRootFields {
		if why == "" {
			t.Errorf("trust-root field %q has no reason", path)
		}
		if ConfigFieldClass(path) != FieldSignIn {
			t.Errorf("trust-root field %q is %v, want a sign-in setting", path, ConfigFieldClass(path))
		}
		if _, err := projectField(reflect.ValueOf(Config{}), strings.Split(path, ".")); err != nil {
			t.Errorf("trust-root field %q does not resolve: %v", path, err)
		}
	}
}

// A system or factory reset (config.ResetToDefaults) keeps the host-set
// owner and Access settings: they are environment-authoritative, as at load.
// It used to clear them in memory until the next restart.
func TestResetToDefaults_KeepsHostOwner(t *testing.T) {
	orig := AppConfig
	t.Cleanup(func() { AppConfig = orig })
	t.Setenv("OWNER_EMAIL", "host-owner@example.test")
	t.Setenv("CF_ACCESS_TEAM_DOMAIN", "host-team.example.test")
	t.Setenv("CF_ACCESS_AUD", "host-aud")
	for key, env := range map[string]string{"owner_email": "OWNER_EMAIL", "cf_access_team_domain": "CF_ACCESS_TEAM_DOMAIN", "cf_access_aud": "CF_ACCESS_AUD"} {
		if err := viper.BindEnv(key, env); err != nil {
			t.Fatal(err)
		}
	}
	AppConfig = Config{OwnerEmail: "host-owner@example.test", CFAccessTeamDomain: "host-team.example.test", CFAccessAUD: "host-aud"}
	ResetToDefaults()
	got := Snapshot()
	if got.OwnerEmail != "host-owner@example.test" || got.CFAccessTeamDomain != "host-team.example.test" || got.CFAccessAUD != "host-aud" {
		t.Errorf("reset dropped host values: owner=%q team=%q aud=%q", got.OwnerEmail, got.CFAccessTeamDomain, got.CFAccessAUD)
	}
}
