// file: internal/books/cutover/identity_refusal_test.go
// version: 1.0.0
// guid: 2a274c27-e701-4cb7-b323-40c3f32966b7
// last-edited: 2026-11-12

package cutover

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/storage"
)

// rejectingAuth is core's importer for every row but one that it refuses the
// way the real one refuses a record that breaks a Store invariant: a row the
// preflight's own mirror of the importer's rules passed, which only a rule the
// two disagree on can produce. The refusal's text quotes a value, as the real
// one's does.
type rejectingAuth struct {
	auth.Importer
	user, key, subject string
}

func (r rejectingAuth) ImportUser(b *storage.Batch, u *auth.User, hash string) error {
	if string(u.ID) == r.user {
		return fmt.Errorf("auth: import user %q: %w", u.Username, auth.ErrImportRejected)
	}
	return r.Importer.ImportUser(b, u, hash)
}

func (r rejectingAuth) ImportAPIKey(b *storage.Batch, k *auth.APIKey, sum [32]byte) error {
	if k.ID == r.key {
		return fmt.Errorf("auth: import key %q: %w", k.Name, auth.ErrImportRejected)
	}
	return r.Importer.ImportAPIKey(b, k, sum)
}

func (r rejectingAuth) ImportIdentity(b *storage.Batch, id *auth.ExternalIdentity) error {
	if id.Subject == r.subject {
		return fmt.Errorf("auth: import identity %q: %w", id.Subject, auth.ErrImportRejected)
	}
	return r.Importer.ImportIdentity(b, id)
}

// TestIdentityCutover_StepRefusalIsABlockerNeverAQuarantine (05-storage
// §5S.3.13, the defensive check; F4-06 STOP 1): a user, key or identity the
// importer refuses while the step runs fails the chunk with the same blocker
// the preflight would have given, with exit 78 and no value in the report or
// the log. The row is never quarantined, and the rows around it commit.
func TestIdentityCutover_StepRefusalIsABlockerNeverAQuarantine(t *testing.T) {
	const identSubject = "oracle-clocktower-1989"
	for _, c := range []struct {
		name, key, family string
		reject            rejectingAuth
		secret            string // a value the importer's text quotes
	}{
		{"a user", "u:" + uBarbara, "u:", rejectingAuth{user: uBarbara}, "barbara.gordon"},
		{"a key", "apikey:" + kScopedView, "apikey:", rejectingAuth{key: kScopedView}, "scoped"},
		{"an identity", "oauthid:", "oauthid:", rejectingAuth{subject: identSubject}, identSubject},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newIDRig(t, "overlay")
			r.config = func(cfg *m3.Config) {
				rej := c.reject
				rej.Importer = cfg.Auth
				cfg.Auth = rej
			}
			_, err := r.run(t)
			var cbe *storage.CutoverBlockedError
			if !errors.Is(err, storage.ErrCutoverBlocked) || !errors.As(err, &cbe) {
				t.Fatalf("the boot returned %v, want storage.ErrCutoverBlocked\n%s", err, r.logs.String())
			}
			if code := kernel.ExitCode(err); code != 78 {
				t.Errorf("the refusal exits %d, want 78", code)
			}
			if len(cbe.Blockers) != 1 {
				t.Fatalf("blockers %+v, want exactly the refused row", cbe.Blockers)
			}
			b := cbe.Blockers[0]
			if !strings.HasPrefix(string(b.Key), c.key) || b.Family != c.family || b.Step != IdentityCutoverID ||
				!strings.Contains(b.Reason, "importer rejects") || !offers(b.Fix, "fix") || !offers(b.Fix, "delete") {
				t.Errorf("blocker %+v: want the row %s, the importer's refusal and fix|delete", b, c.key)
			}
			text := strings.Join(storage.CutoverBlockerReport(cbe.Blockers), "\n") + b.Reason + r.logs.String()
			if strings.Contains(text, c.secret) {
				t.Errorf("the report or the log carries %q, a value the importer's text quoted", c.secret)
			}
			if strings.Contains(strings.ToLower(text), "quarantin") && strings.Contains(strings.ToLower(strings.Join(storage.CutoverBlockerReport(cbe.Blockers), "\n")), "quarantin") {
				t.Errorf("the report speaks of quarantine: a refused identity row is a blocker")
			}
		})
	}
}
