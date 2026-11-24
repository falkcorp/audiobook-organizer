// file: core/internal/authstore/key_description_test.go
// version: 0.1.0
// guid: c927eb13-b214-4534-97be-d26dba002521
// last-edited: 2026-11-22

package authstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage/codec"
)

// oldKeyRecord is a key record exactly as the encoder wrote it before
// auth.APIKey had a Description (captured from the encoder at the commit
// before this field, not produced by today's). The first byte is the codec's
// version-1 envelope marker.
const oldKeyRecord = "j{\"v\":1,\"ID\":\"01JKOLDREC\",\"Owner\":\"01JBRUCE\",\"Name\":\"utility belt\",\"Scopes\":[\"gotham:patrol\"]," +
	"\"Status\":\"active\",\"CreatedAt\":\"2026-01-01T00:00:00Z\",\"ExpiresAt\":\"2027-01-01T00:00:00Z\"," +
	"\"RevokedAt\":\"0001-01-01T00:00:00Z\",\"LastUsedAt\":\"0001-01-01T00:00:00Z\",\"LastUsedIP\":\"\",\"UseCount\":0," +
	"\"Imported\":false,\"HashPrefix\":\"abababab\",\"Version\":1," +
	"\"hash\":\"abababababababababababababababababababababababababababababababab\"}"

func oldShapedKey(desc string) keyRecord {
	return keyRecord{ID: "01JKOLDREC", Owner: "01JBRUCE", Name: "utility belt", Scopes: []rbac.Permission{"gotham:patrol"},
		Status: auth.KeyActive, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ExpiresAt: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		Version: 1, HashPrefix: "abababab", Description: desc,
		Hash: strings.Repeat("ab", 32)}
}

// TestStoredKeyBeforeDescriptionDecodesUnchanged: a record written before the
// field existed decodes, with an empty Description, and every other field
// as stored. Run against the frozen bytes, never today's encoder.
func TestStoredKeyBeforeDescriptionDecodesUnchanged(t *testing.T) {
	st := newStore(t)
	rawSet(t, st, apiKeyKey("01JKOLDREC"), []byte(oldKeyRecord))
	got, err := st.APIKeyByID(context.Background(), "01JKOLDREC")
	if err != nil {
		t.Fatalf("an old record does not decode: %v", err)
	}
	if got.Description != "" || got.Name != "utility belt" || got.Owner != "01JBRUCE" || got.Status != auth.KeyActive || got.Version != 1 {
		t.Fatalf("old record decoded as %+v", got)
	}
}

// TestStoredKeyDescriptionIsOmittedWhileEmpty: a key without a description
// encodes to the very bytes the encoder wrote before the field existed (so
// no record changes until its key gets one), and one with a description
// carries it and reads it back.
func TestStoredKeyDescriptionIsOmittedWhileEmpty(t *testing.T) {
	raw, err := codec.JSON[keyRecord](1).Encode(oldShapedKey(""))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != oldKeyRecord {
		t.Fatalf("a key without a description encodes as\n%s\nwant the pre-field bytes\n%s", raw, oldKeyRecord)
	}
	with, err := codec.JSON[keyRecord](1).Encode(oldShapedKey("Bat-Signal spare"))
	if err != nil || !strings.Contains(string(with), `"Description":"Bat-Signal spare"`) {
		t.Fatalf("a described key encodes as %s (%v)", with, err)
	}
	st := newStore(t)
	rawSet(t, st, apiKeyKey("01JKOLDREC"), with)
	got, err := st.APIKeyByID(context.Background(), "01JKOLDREC")
	if err != nil || got.Description != "Bat-Signal spare" {
		t.Fatalf("read back %+v %v", got, err)
	}
}
