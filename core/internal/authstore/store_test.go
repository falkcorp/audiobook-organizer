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
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/authtest"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/keys"
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
		rec := keyRecord{ID: "01JK" + strings.ToUpper(name), Owner: "01JBRUCE", Scopes: scopes,
			Status: auth.KeyActive, ExpiresAt: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), Hash: strings.Repeat("ab", 32)}
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
	rec := keyRecord{ID: "01JKBANE", Owner: "01JBRUCE", Scopes: []rbac.Permission{"gotham:patrol"},
		Status: auth.KeyActive, RevokedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ExpiresAt: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
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

// issueOneSession commits a user and one session for it and returns the raw
// token and the record as issued.
func issueOneSession(t *testing.T, st *Store) (string, *auth.SessionRecord) {
	t.Helper()
	ctx := context.Background()
	b := st.NewBatch()
	defer func() {
		if err := b.Close(); err != nil {
			t.Error(err)
		}
	}()
	u := &auth.User{ID: "01JDICK", Username: "nightwing", Status: auth.StatusActive, Version: 1}
	if err := st.PutUser(b, u); err != nil {
		t.Fatal(err)
	}
	rec := &auth.SessionRecord{UserID: u.ID, Origin: auth.OriginPassword, ExpiresAt: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}
	tok, err := st.IssueSession(b, rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	return tok, rec
}

// TestSession_TokenNeverStored (05-authn §10): after IssueSession no stored
// key or value contains the token's bytes, whole or as its secret.
func TestSession_TokenNeverStored(t *testing.T) {
	st := newStore(t)
	tok, _ := issueOneSession(t, st)
	if err := auth.ParseCredential(auth.KindSession, tok); err != nil {
		t.Fatalf("the issued token is not a session credential: %v", err)
	}
	rows := 0
	for kv, err := range st.ks.ScanPrefix(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		rows++
		for _, needle := range [][]byte{[]byte(tok), []byte(tok[4:47])} {
			if bytes.Contains(kv.Key, needle) || bytes.Contains(kv.Value, needle) {
				t.Fatalf("row %q holds the session token", kv.Key)
			}
		}
	}
	if rows < 2 {
		t.Fatalf("scanned %d rows; the check looked at nothing", rows)
	}
}

// TestSession_NativeOnlyRecord (05-authn §10): the record a session is read
// back as holds no token, and a well-formed token whose hash is unknown is
// refused.
func TestSession_NativeOnlyRecord(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	tok, rec := issueOneSession(t, st)
	got, err := st.SessionByToken(ctx, tok)
	if err != nil {
		t.Fatalf("SessionByToken: %v", err)
	}
	if got.PublicID != rec.PublicID || strings.Contains(got.PublicID, tok[4:47]) {
		t.Fatalf("read back %+v", got)
	}
	byID, err := st.SessionByPublicID(ctx, rec.PublicID)
	if err != nil || byID.UserID != rec.UserID {
		t.Fatalf("SessionByPublicID = %+v, %v", byID, err)
	}
	unknown, err := auth.MintCredential(auth.KindSession)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SessionByToken(ctx, unknown); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("an unknown token: %v, want ErrNotFound", err)
	}
}

// hexRun is 64 hex digits in a row: a SHA-256 written out.
var hexRun = regexp.MustCompile(`[0-9a-fA-F]{64}`)

// TestErrors_NeverCarryACredentialHash (STOP 3): an error about a record
// keyed by a credential's hash names the key's family and length, never the
// hash, because the stage-12 touch logs the errors it gets.
func TestErrors_NeverCarryACredentialHash(t *testing.T) {
	ks, other := newKeyspace(t)
	st, err := New(ks, Options{Now: func() time.Time { return time.Date(2026, 11, 10, 21, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	tok, rec := issueOneSession(t, st)
	h := auth.HashHex(tok)
	described := map[string][]byte{
		"session/h":          sessionKey(h),
		"apikey/h":           apiKeyHashKey(h),
		"onetime/invite":     oneTimeKey(auth.OneTimeInvite, h),
		"onetime/temp_login": oneTimeKey(auth.OneTimeTempLogin, h),
		"onetime/bootstrap":  oneTimeKey(auth.OneTimeBootstrap, h),
		"session/id":         sessionIDKey(rec.PublicID),
		"user":               userKey("01JDICK"),
	}
	for family, key := range described {
		d := keyDesc(key)
		if hexRun.MatchString(d) || strings.Contains(d, h) {
			t.Fatalf("keyDesc of a %s key carries the hash: %s", family, d)
		}
		if !strings.HasPrefix(d, family+" key len=") {
			t.Fatalf("keyDesc of a %s key = %q, want the family and the length", family, d)
		}
	}
	// A key of no family, and a family key whose second segment is not a
	// structural one, describe no segment beyond what is known.
	for want, key := range map[string][]byte{
		"unrecognised key len=": keys.Join(keys.Str(h), keys.Str("joker")),
		"session key len=":      keys.Join(keys.Str(segSession), keys.Str(h), keys.Str("joker")),
	} {
		if d := keyDesc(key); hexRun.MatchString(d) || !strings.HasPrefix(d, want) {
			t.Fatalf("keyDesc of a foreign key = %q, want %q...", d, want)
		}
	}
	if d := keyDesc([]byte("joker/" + h)); hexRun.MatchString(d) || !strings.HasPrefix(d, "unrecognised key") {
		t.Fatalf("keyDesc of an unsplittable key = %q", d)
	}

	// A staged write that fails wraps the key it was writing: poison the
	// batch with an Expect through a handle it never bound, then re-stamp
	// the session, whose record is keyed by the token's hash.
	b := st.NewBatch()
	defer func() { _ = b.Close() }()
	if err := b.Expect(other, []byte("joker"), nil); err == nil {
		t.Fatal("an Expect through an unbound handle did not fail")
	}
	err = st.RestampSession(b, rec, 2)
	if err == nil {
		t.Fatal("a poisoned batch staged a write")
	}
	if hexRun.MatchString(err.Error()) || strings.Contains(err.Error(), h) {
		t.Fatalf("the error carries the session's hash: %v", err)
	}
	if !strings.Contains(err.Error(), "session/h key len=") {
		t.Fatalf("the error does not name the key's family: %v", err)
	}
}
