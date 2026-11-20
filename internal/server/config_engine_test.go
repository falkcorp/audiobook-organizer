// file: internal/server/config_engine_test.go
// version: 1.1.0
// guid: 7e1b9d34-5a28-4c60-8f13-b2d4a6c90e57
// last-edited: 2026-11-19

package server

import (
	"context"
	"encoding/json"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/books/appconfig"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// engineStore is an appconfig.Store over maps: what the Engine reads from
// core's store, without booting one. It validates and protects nothing (core
// does, and no route test boots core with every key registered), so it serves
// the tests that ask what the Engine PROJECTS, not what a write is allowed.
type engineStore struct {
	mu      sync.Mutex
	pass    map[coreconfig.Path]coreconfig.OperatorValue
	secrets map[secrets.Name]string
	hooks   []func(uint64)
	gen     uint64
}

func newEngineStore() *engineStore {
	return &engineStore{pass: map[coreconfig.Path]coreconfig.OperatorValue{}, secrets: map[secrets.Name]string{}}
}

func (s *engineStore) Lookup(coreconfig.Path) (coreconfig.KeyState, bool) {
	return coreconfig.KeyState{}, false
}

func (s *engineStore) Apply(context.Context, coreconfig.Patch, coreconfig.Actor) (coreconfig.Result, error) {
	return coreconfig.Result{}, nil
}

func (s *engineStore) Generation() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

func (s *engineStore) ListSecrets(_ context.Context, prefix string) ([]secrets.Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []secrets.Meta
	for n := range s.secrets {
		if strings.HasPrefix(string(n), prefix) {
			out = append(out, secrets.Meta{Name: n, Set: true, Source: secrets.SourceStored, Fingerprint: "abcd1234"})
		}
	}
	return out, nil
}

func (s *engineStore) OnCommit(fn func(uint64)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks = append(s.hooks, fn)
}

func (s *engineStore) PassthroughValues() iter.Seq2[coreconfig.Path, coreconfig.OperatorValue] {
	s.mu.Lock()
	cp := make(map[coreconfig.Path]coreconfig.OperatorValue, len(s.pass))
	for k, v := range s.pass {
		cp[k] = v
	}
	s.mu.Unlock()
	return func(yield func(coreconfig.Path, coreconfig.OperatorValue) bool) {
		for k, v := range cp {
			if !yield(k, v) {
				return
			}
		}
	}
}

func (s *engineStore) Secret(_ context.Context, n secrets.Name) (secrets.Value, secrets.Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.secrets[n]
	if !ok {
		return secrets.Value{}, secrets.Meta{Name: n}, secrets.ErrNotSet
	}
	return secrets.NewValue(v), secrets.Meta{Name: n, Set: true, Source: secrets.SourceStored, Fingerprint: "abcd1234"}, nil
}

func (s *engineStore) PutSecret(_ context.Context, n secrets.Name, v secrets.Value, _ string) (secrets.Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets[n] = v.Reveal()
	return secrets.Meta{Name: n, Set: true}, nil
}

func (s *engineStore) DeleteSecret(_ context.Context, n secrets.Name, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.secrets, n)
	return nil
}

// TestConfigGET_NeverReturnsInviteSecrets_RealEngine is
// TestConfigGET_NeverReturnsInviteSecrets over the production Facade, the real
// appconfig.Engine: the invite mailer's settings (SMTP username and host are
// Config fields tagged json:"-", the password and the Cloudflare token are
// secrets) must not appear in GET /api/v1/config, as they never did in HEAD's
// json.Marshal(Config).
//
// Engine.View is default-deny since F3-07 e2: a class-o key whose Config field is
// json:"-" (smtp_username, smtp_host) is served by name, class and lock with no
// Value. Measured before that fix, KeyView{Name:"smtp_username", Class:"o"}
// carried the username and smtp_host the host.
func TestConfigGET_NeverReturnsInviteSecrets_RealEngine(t *testing.T) {
	srv, _ := setupUserHandlerServer(t)
	saved := config.Snapshot()
	t.Cleanup(func() { config.Project(saved) })

	secretValues := []string{"smtp-pw-SENTINEL-41", "cf-token-SENTINEL-44", "mailer-SENTINEL@example.com"}
	st := newEngineStore()
	username, _ := json.Marshal(secretValues[2])
	host, _ := json.Marshal("192.0.2.25")
	st.pass["integrations.notify.email.username"] = coreconfig.OperatorValue{Value: username, Src: coreconfig.SourceEnv, By: "env SMTP_USERNAME"}
	st.pass["integrations.notify.email.host"] = coreconfig.OperatorValue{Value: host, Src: coreconfig.SourceEnv, By: "env SMTP_HOST"}
	st.secrets["integrations.notify.email.password"] = secretValues[0]

	eng := appconfig.New(st, config.Project)
	require.NoError(t, eng.Start(context.Background()))
	prev := config.Install(eng)
	t.Cleanup(func() { config.Install(prev) })

	for _, path := range []string{"/api/v1/config", "/api/v1/users/invites"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "%s: %s", path, w.Body.String())
		for _, s := range append(secretValues, "192.0.2.25") {
			if strings.Contains(w.Body.String(), s) {
				t.Fatalf("%s leaked %q through the real Engine's View", path, s)
			}
		}
	}
}
