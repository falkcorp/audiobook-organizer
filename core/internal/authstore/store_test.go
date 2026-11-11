// file: core/internal/authstore/store_test.go
// version: 1.0.0
// guid: ff601fd8-fd3d-4cde-82ec-c1c09c875e1a
// last-edited: 2026-11-10

package authstore

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/authtest"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
)

// otherOwner is a second keyspace, to prove New refuses a foreign one.
const otherOwner storage.Owner = "core/gotham"

// newKeyspace boots a fresh MemFS store with the identity keyspace (and a
// foreign one) and returns both.
func newKeyspace(t testing.TB) (ks, other storage.Keyspace) {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(t.TempDir(), "main.pebble"),
		Owners: []storage.OwnerSpec{
			{Owner: Owner, Spec: StorageSpec(), Enabled: true},
			{Owner: otherOwner, Spec: storage.Spec{Keyspace: true}, Enabled: true},
		},
		Build:           "coretest+fixture",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		if cerr := o.Close(); cerr != nil {
			t.Logf("close after a failed boot: %v", cerr)
		}
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	ks, ok := db.Keyspace(Owner)
	if !ok {
		t.Fatal("no identity keyspace")
	}
	other, ok = db.Keyspace(otherOwner)
	if !ok {
		t.Fatal("no foreign keyspace")
	}
	return ks, other
}

func newStore(t testing.TB) *Store {
	t.Helper()
	ks, _ := newKeyspace(t)
	st, err := New(ks, Options{Now: func() time.Time { return time.Date(2026, 11, 10, 21, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// rawSet commits key = raw over whatever is stored (an Expect on the
// committed bytes), the way a damaged or foreign record would sit there.
func rawSet(t testing.TB, st *Store, key, raw []byte) {
	t.Helper()
	b := st.NewBatch()
	defer func() {
		if err := b.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	cur, err := st.committed(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.set(b, key, cur, raw); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(context.Background(), storage.Sync); err != nil {
		t.Fatal(err)
	}
}

func nativeHarness() authtest.Harness {
	return authtest.Harness{New: func(t *testing.T) authtest.Fixture {
		st := newStore(t)
		return authtest.Fixture{
			Store: st, Importer: st, NewBatch: st.NewBatch,
			CorruptUser: func(t *testing.T, id auth.UserID) { rawSet(t, st, userKey(id), []byte("not a user")) },
			Raw: func(t *testing.T, kind, id string) []byte {
				var key []byte
				switch kind {
				case auth.MergeAPIKey:
					key = apiKeyKey(id)
				case auth.MergeIdentity:
					p, s, _ := strings.Cut(id, "/")
					key = identityKey(p, s)
				case auth.MergeInviteAllow:
					key = allowKey(auth.NormalizeEmail(id))
				default:
					t.Fatalf("Raw: kind %q", kind)
				}
				raw, err := st.committed(key)
				if err != nil {
					t.Fatal(err)
				}
				return raw
			},
		}
	}}
}

// TestNativeStore_RoundTrip (§10): the native store passes the conformance
// suite, the importer's cases included.
func TestNativeStore_RoundTrip(t *testing.T) {
	authtest.StoreSuite(t, nativeHarness())
}

// TestNew_RefusesForeignKeyspace: the store opens only over its own owner's
// keyspace.
func TestNew_RefusesForeignKeyspace(t *testing.T) {
	_, other := newKeyspace(t)
	if _, err := New(other, Options{}); err == nil {
		t.Fatal("New over a foreign keyspace succeeded")
	}
}

// TestStoredKeyWithoutScopeIsUndecodable (R3-1, seams.d/F4-09.md): a stored
// key whose scope list is empty is refused as damaged, never read as a key
// (an empty list compiled as nil would be a session's whole role set).
func TestStoredKeyWithoutScopeIsUndecodable(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	for name, scopes := range map[string][]rbac.Permission{"nil": nil, "empty": {}} {
		rec := keyRecord{APIKey: auth.APIKey{ID: "01JK" + strings.ToUpper(name), Owner: "01JBRUCE", Scopes: scopes,
			Status: auth.KeyActive, ExpiresAt: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}, Hash: strings.Repeat("ab", 32)}
		raw, err := codec.JSON[keyRecord](1).Encode(rec)
		if err != nil {
			t.Fatal(err)
		}
		rawSet(t, st, apiKeyKey(rec.ID), raw)
		if _, err := st.APIKeyByID(ctx, rec.ID); !errors.Is(err, auth.ErrUndecodableRecord) {
			t.Errorf("APIKeyByID(%s scopes) = %v, want ErrUndecodableRecord", name, err)
		}
	}
}

// TestStoredKeyRevokedAtWithLiveStatusIsUndecodable: a record that claims
// both a revocation time and a live status is damaged; it is refused, never
// read as active (§4.4, early fix 3).
func TestStoredKeyRevokedAtWithLiveStatusIsUndecodable(t *testing.T) {
	st := newStore(t)
	rec := keyRecord{APIKey: auth.APIKey{ID: "01JKBANE", Owner: "01JBRUCE", Scopes: []rbac.Permission{"gotham:patrol"},
		Status: auth.KeyActive, RevokedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ExpiresAt: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)},
		Hash: strings.Repeat("cd", 32)}
	raw, err := codec.JSON[keyRecord](1).Encode(rec)
	if err != nil {
		t.Fatal(err)
	}
	rawSet(t, st, apiKeyKey(rec.ID), raw)
	if _, err := st.APIKeyByID(context.Background(), rec.ID); !errors.Is(err, auth.ErrUndecodableRecord) {
		t.Fatalf("APIKeyByID = %v, want ErrUndecodableRecord", err)
	}
}

// TestNoRawCredentialStored (§4.1): after minting one credential of every
// kind, no stored byte holds a raw credential's secret.
func TestNoRawCredentialStored(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	b := st.NewBatch()
	exp := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	u := &auth.User{ID: "01JBRUCE", Username: "bruce", Status: auth.StatusActive, Version: 1}
	var raws []string
	steps := []func() (string, error){
		func() (string, error) { return "", st.PutUser(b, u) },
		func() (string, error) {
			return st.IssueSession(b, &auth.SessionRecord{UserID: u.ID, Origin: auth.OriginPassword, ExpiresAt: exp})
		},
		func() (string, error) {
			return st.IssueAPIKey(b, &auth.APIKey{Owner: u.ID, Scopes: []rbac.Permission{"gotham:patrol"}, Status: auth.KeyActive, ExpiresAt: exp})
		},
		func() (string, error) {
			return st.IssueOneTime(b, &auth.OneTimeToken{Kind: auth.OneTimeInvite, ExpiresAt: exp})
		},
		func() (string, error) {
			return st.IssueOneTime(b, &auth.OneTimeToken{Kind: auth.OneTimeTempLogin, UserID: u.ID, ExpiresAt: exp})
		},
		func() (string, error) {
			return st.IssueOneTime(b, &auth.OneTimeToken{Kind: auth.OneTimeBootstrap, ExpiresAt: exp})
		},
	}
	for _, f := range steps {
		raw, err := f()
		if err != nil {
			t.Fatal(err)
		}
		if raw != "" {
			raws = append(raws, raw)
		}
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	var rows int
	for kv, err := range st.ks.ScanPrefix(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		rows++
		for _, raw := range raws {
			secret := []byte(raw[4:47])
			if bytes.Contains(kv.Key, secret) || bytes.Contains(kv.Value, secret) {
				t.Fatalf("row %q holds the secret of a %s credential", kv.Key, raw[:4])
			}
		}
	}
	if rows == 0 || len(raws) != 5 {
		t.Fatalf("scanned %d rows over %d credentials; the check looked at nothing", rows, len(raws))
	}
}
