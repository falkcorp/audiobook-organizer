// file: core/secrets/record.go
// version: 1.0.1
// guid: 0283d1b3-ef8d-4434-91ba-fe49f9ce7a85
// last-edited: 2026-11-10

package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/falkcorp/mediacore/storage/keys"
)

// The record formats of c/secrets/ (05-config §2.3 "Formats", normative).
const (
	recordVersion = 1
	// aadPrefix starts every record's AAD: aadPrefix + AppID + "\x00" + name,
	// so a ciphertext moved to another name or app fails authentication.
	aadPrefix = "mediacore/secrets/v1\x00"

	// The reasons of an unreadable marker (StageUnreadable).
	reasonKey     = "key"
	reasonCorrupt = "corrupt"

	// recordSegment is the first key segment of every record:
	// c/secrets/ + keys.Join("v", <name>).
	recordSegment = "v"
	// metaSegment is the store metadata's key: c/secrets/ + keys.Join("meta").
	metaSegment = "meta"
)

// record is one value at c/secrets/v/<name>: a sealed value (kid, nonce, ct),
// a cleared marker or an unreadable marker. Nonce and ct are base64 in JSON
// (encoding/json's []byte rule).
type record struct {
	V          int       `json:"v"`
	KID        string    `json:"kid,omitempty"`
	Nonce      []byte    `json:"nonce,omitempty"`
	CT         []byte    `json:"ct,omitempty"`
	Cleared    bool      `json:"cleared,omitempty"`
	Unreadable string    `json:"unreadable,omitempty"`
	At         time.Time `json:"at"`
	By         string    `json:"by"`
}

// sealed reports whether r carries ciphertext.
func (r *record) sealed() bool { return !r.Cleared && r.Unreadable == "" }

// validate checks that r is exactly one of the three shapes.
func (r *record) validate() error {
	if r.V != recordVersion {
		return fmt.Errorf("record version %d, want %d", r.V, recordVersion)
	}
	switch {
	case r.Cleared:
		if r.Unreadable != "" || r.KID != "" || len(r.Nonce) != 0 || len(r.CT) != 0 {
			return errors.New("a cleared marker carries other fields")
		}
	case r.Unreadable != "":
		if r.Unreadable != reasonKey && r.Unreadable != reasonCorrupt {
			return fmt.Errorf("unreadable reason %q, want key or corrupt", r.Unreadable)
		}
		if r.KID != "" || len(r.Nonce) != 0 || len(r.CT) != 0 {
			return errors.New("an unreadable marker carries ciphertext fields")
		}
	default:
		if r.KID == "" || len(r.Nonce) == 0 || len(r.CT) == 0 {
			return errors.New("a sealed record lacks kid, nonce or ct")
		}
	}
	return nil
}

// decodeRecord decodes a record strictly. A record that fails here reads as
// ErrCorrupt; its error text never quotes the bytes.
func decodeRecord(raw []byte) (*record, error) {
	var r record
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, errors.New("the record does not decode")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("data after the record object")
	}
	if err := r.validate(); err != nil {
		return nil, err
	}
	return &r, nil
}

// encodeRecord encodes r.
func encodeRecord(r *record) ([]byte, error) { return encodeJSON(r) }

// encodeJSON encodes one of the package's own JSON documents.
func encodeJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("secrets: encode: %w", err)
	}
	return b, nil
}

// aad is the additional authenticated data of name's record under appID.
func aad(appID string, name Name) []byte {
	b := make([]byte, 0, len(aadPrefix)+len(appID)+1+len(name))
	b = append(b, aadPrefix...)
	b = append(b, appID...)
	b = append(b, 0)
	b = append(b, name...)
	return b
}

// recordKey is name's record key, relative to the c/secrets/ keyspace.
func recordKey(name Name) []byte { return keys.Join(keys.Str(recordSegment), keys.Str(string(name))) }

// recordPrefix is the prefix of every record key.
func recordPrefix() []byte { return keys.Prefix(keys.Str(recordSegment)) }

// metaKey is the store metadata's key.
func metaKey() []byte { return keys.Join(keys.Str(metaSegment)) }

// nameOfRecordKey is the name a record key holds, or false for a key that is
// not one.
func nameOfRecordKey(key []byte) (Name, bool) {
	segs, err := keys.Split(key)
	if err != nil || len(segs) != 2 || string(segs[0]) != recordSegment {
		return "", false
	}
	n := Name(segs[1])
	if n.Validate() != nil {
		return "", false
	}
	return n, true
}

// seal encrypts plaintext for name with the active data key of r.
func (r *keyring) seal(appID string, name Name, plaintext []byte, at time.Time, by string) (*record, error) {
	k := r.data
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secrets: random nonce: %w", err)
	}
	ct := k.aead.Seal(nil, nonce, plaintext, aad(appID, name))
	return &record{V: recordVersion, KID: k.ID, Nonce: nonce, CT: ct, At: at, By: by}, nil
}

// open decrypts a sealed record: ErrKeyUnavailable when its key is not held
// (or held retired), ErrCorrupt when authentication fails.
func (r *keyring) open(appID string, name Name, rec *record) ([]byte, error) {
	k, ok := r.byID[rec.KID]
	if !ok || k.Use != useData || k.aead == nil {
		return nil, ErrKeyUnavailable
	}
	if len(rec.Nonce) != k.aead.NonceSize() {
		return nil, ErrCorrupt
	}
	pt, err := k.aead.Open(nil, rec.Nonce, rec.CT, aad(appID, name))
	if err != nil {
		return nil, ErrCorrupt
	}
	return pt, nil
}

// storeMeta is c/secrets/meta.
type storeMeta struct {
	AppID         string    `json:"app_id"`
	KeyringFormat string    `json:"keyring_format"`
	Created       time.Time `json:"created"`
	// MaxKeyID is the highest key number ("k3" and "d3" are 3) of any
	// keyring an Open loaded, so a regenerated keyring numbers past it.
	MaxKeyID int `json:"max_key_id,omitempty"`
}

// decodeStoreMeta decodes c/secrets/meta (unknown fields allowed: a later
// format may add one; the app ID is what Open compares).
func decodeStoreMeta(raw []byte) (*storeMeta, error) {
	var m storeMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("secrets: c/secrets/meta does not decode: %w", err)
	}
	if m.AppID == "" {
		return nil, errors.New("secrets: c/secrets/meta names no app ID")
	}
	return &m, nil
}
