// file: core/internal/authhttp/keys_expiry_test.go
// version: 0.1.0
// guid: 5b8e2a17-9c04-4f63-a1d7-3e6f90c4b258
// last-edited: 2026-11-20

package authhttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

const threeYears = 1096 * 24 * time.Hour

func keyAt(at time.Time) KeyCreate {
	return KeyCreate{Name: "grapnel", Scopes: []string{"core:self"}, ExpiresAt: &at}
}

// TestKeyCreate_BodyPresenceDecodesAsPresence: an empty expires_in is
// present, not absent, so the "both fields" refusal can see it.
func TestKeyCreate_BodyPresenceDecodesAsPresence(t *testing.T) {
	var k KeyCreate
	if err := json.Unmarshal([]byte(`{"name":"n","scopes":["core:self"],"expires_in":"","expires_at":"2027-01-31T12:00:00Z"}`), &k); err != nil {
		t.Fatal(err)
	}
	if k.ExpiresIn == nil || k.ExpiresAt == nil || !k.ExpiresAt.Equal(time.Date(2027, 1, 31, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("%+v", k)
	}
	k = KeyCreate{}
	if err := json.Unmarshal([]byte(`{"name":"n","scopes":["core:self"]}`), &k); err != nil || k.ExpiresIn != nil || k.ExpiresAt != nil {
		t.Fatalf("%+v %v", k, err)
	}
	if err := json.Unmarshal([]byte(`{"expires_at":"next tuesday"}`), &k); err == nil {
		t.Fatal("a malformed instant decodes")
	}
}

// TestKeyCreate_BothExpiriesIsA400: sending expires_in and expires_at is a
// 400 whatever expires_in holds, on Validate and again on keySpec, for both
// create routes (HEAD: even expires_in_days 0).
func TestKeyCreate_BothExpiriesIsA400(t *testing.T) {
	at := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	for _, in := range []string{"24h", "0s", "", "soon", "-1h", "26304h"} {
		k := KeyCreate{Name: "n", Scopes: []string{"core:self"}, ExpiresIn: new(in), ExpiresAt: &at}
		wantErr(t, (&createKeyReq{Body: k}).Validate(), http.StatusBadRequest, "bad_request")
		wantErr(t, (&userKeyCreateReq{ID: "u-dick", Body: k}).Validate(), http.StatusBadRequest, "bad_request")
		_, err := k.keySpec("u-bruce")
		wantErr(t, err, http.StatusBadRequest, "bad_request")
	}
	// Either alone is fine.
	if err := (&createKeyReq{Body: KeyCreate{Name: "n", Scopes: []string{"core:self"}, ExpiresAt: &at}}).Validate(); err != nil {
		t.Fatal(err)
	}
	// An empty expires_in alone still means the default lifetime.
	ks, err := KeyCreate{Name: "n", Scopes: []string{"core:self"}, ExpiresIn: new("")}.keySpec("u-bruce")
	if err != nil || ks.ExpiresIn != 0 || !ks.ExpiresAt.IsZero() {
		t.Fatalf("%+v %v", ks, err)
	}
}

// TestKeyCreate_ZeroInstantIsRefused: the zero time is KeySpec's "unset" and
// would mint the default lifetime, so a body naming year 1 is refused before
// it reaches the Service.
func TestKeyCreate_ZeroInstantIsRefused(t *testing.T) {
	for _, at := range []time.Time{{}, time.Date(1, 1, 1, 5, 0, 0, 0, time.FixedZone("x", 5*3600))} {
		k := keyAt(at)
		wantErr(t, (&createKeyReq{Body: k}).Validate(), http.StatusUnprocessableEntity, "validation_failed")
		_, err := k.keySpec("u-bruce")
		wantErr(t, err, http.StatusUnprocessableEntity, "validation_failed")
	}
}

func TestKeySpec_CarriesTheInstantInUTC(t *testing.T) {
	at := time.Date(2026, 12, 1, 9, 0, 0, 0, time.FixedZone("x", 5*3600))
	ks, err := keyAt(at).keySpec("u-bruce")
	if err != nil || !ks.ExpiresAt.Equal(at) || ks.ExpiresAt.Location() != time.UTC || ks.ExpiresIn != 0 {
		t.Fatalf("%+v %v", ks, err)
	}
}

// TestMyKeys_ExpiresAtRoute: through the route and the real Service, the key
// ends at the instant asked for, and the future and 1096-day bounds hold.
func TestMyKeys_ExpiresAtRoute(t *testing.T) {
	e := newEnv(t)
	bruce := e.user("bruce", "brucebruce", rbac.RoleAdmin)
	a := e.api()
	p := e.session(bruce)
	mint := func(at time.Time) error {
		_, err := call(specMeKeyCreate(), a.createMyKey, p, createKeyReq{Body: keyAt(at)})
		return err
	}
	at := e.clock().Add(48 * time.Hour)
	created, err := call(specMeKeyCreate(), a.createMyKey, p, createKeyReq{Body: keyAt(at)})
	if err != nil || created.Body.APIKey.ExpiresAt == nil || !created.Body.APIKey.ExpiresAt.Equal(at) {
		t.Fatalf("%+v %v", created, err)
	}
	for name, bad := range map[string]time.Time{
		"now":            e.clock(),
		"in the past":    e.clock().Add(-time.Hour),
		"past the cap":   e.clock().Add(threeYears + time.Second),
		"far in future":  time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC),
		"before the era": time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		var he *httpapi.Error
		if err := mint(bad); !errors.As(err, &he) || he.Status != http.StatusBadRequest || he.Code != "bad_request" {
			t.Errorf("%s: %v, want 400 bad_request", name, err)
		}
	}
	last := e.clock().Add(threeYears)
	created, err = call(specMeKeyCreate(), a.createMyKey, p, createKeyReq{Body: keyAt(last)})
	if err != nil || !created.Body.APIKey.ExpiresAt.Equal(last) {
		t.Fatalf("the 1096-day instant: %+v %v", created, err)
	}
	// The Service refused every bad one before storing anything.
	page, err := call(specMeKeys(), a.myKeys, p, listReq{Limit: 50})
	if err != nil || page.Total != 2 {
		t.Fatalf("keys stored = %d, want 2: %v", page.Total, err)
	}
}
