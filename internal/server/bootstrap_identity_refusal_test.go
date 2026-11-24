// file: internal/server/bootstrap_identity_refusal_test.go
// version: 0.1.0
// guid: 3d9a4c71-5b08-4e62-8f1d-a2c6e0b97d35
// last-edited: 2026-11-22

package server

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// Under core identity the bootstrap token is core's, and the books route refuses
// before it reads anything. On a flipped store HEAD's hash and expiry cannot exist
// (the identity cutover deletes both rows and the claim is retired), so this holds a
// store that still has them, which is the regression the refusal guards against: a
// live pair would otherwise have the route spend the token and find or create an
// admin, and an expired pair would have it remove the token file, which core's
// token shares by name.
func TestHandleBootstrap_UnderIdentityRefusesBeforeReadingAnything(t *testing.T) {
	const want = `{"error":"invalid bootstrap token","code":"UNAUTHORIZED","status":401}`
	for _, tc := range []struct {
		name    string
		expires time.Duration
	}{
		{"live pair", 10 * time.Minute},
		{"expired pair", -time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sandboxBootstrapStateDir(t)
			s := newBootstrapAdminStore(t)
			if err := InitBootstrapToken(s, config.SecureStateDir()); err != nil {
				t.Fatal(err)
			}
			if tc.expires < 0 {
				if err := s.SetSetting(bootstrapExpiresKey, strconv.FormatInt(time.Now().Add(tc.expires).Unix(), 10), "string", false); err != nil {
					t.Fatal(err)
				}
			}
			file := BootstrapTokenPath(config.SecureStateDir())
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			token := strings.TrimSpace(string(raw))
			hash, err := s.GetSetting(bootstrapTokenKey)
			if err != nil {
				t.Fatal(err)
			}
			usersBefore := countUsers(t, s)

			srv := &Server{store: s, identity: adoptIdentity(t, nil, nil, nil)}
			for name, body := range map[string]any{"the matching token": map[string]string{"token": token}, "no token": map[string]string{}} {
				w := postBootstrap(t, srv, body)
				if w.Code != http.StatusUnauthorized || strings.TrimSpace(w.Body.String()) != want {
					t.Errorf("%s: %d %s, want 401 %s", name, w.Code, w.Body.String(), want)
				}
			}
			if _, err := os.Stat(file); err != nil {
				t.Errorf("the token file is gone: %v", err)
			}
			if got := countUsers(t, s); got != usersBefore {
				t.Errorf("users %d -> %d: the route touched an account", usersBefore, got)
			}
			if got, err := s.GetSetting(bootstrapTokenKey); err != nil || got.Value != hash.Value {
				t.Errorf("the stored hash was spent or changed: %v %v", got, err)
			}
		})
	}
}
