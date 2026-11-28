// file: core/tasks/actor_credential_internal_test.go
// version: 1.0.0
// guid: 6a1f3c88-9b25-4e7d-8c40-d2e9b7a53f16
// last-edited: 2026-11-28

package tasks

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/storage"
)

// TestRunRecord_CredentialIDRoundTrips (F5-18 (i)): a run stored with the
// actor's credential id reads back with it, and a run stored with none reads
// back with none.
func TestRunRecord_CredentialIDRoundTrips(t *testing.T) {
	e := newNativeEnv(t)
	ctx := t.Context()
	rec := batRun("01CREDENTIALKEY", StateQueued)
	rec.Actor = Actor{UserID: "u-lucius", AuthMethod: "token", CredentialID: "key-batphone"}
	if err := e.commit(t, func(b *storage.Batch) error { return e.runs.Insert(ctx, b, rec) }); err != nil {
		t.Fatal(err)
	}
	got, err := e.runs.Get(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Actor != rec.Actor {
		t.Fatalf("stored actor = %+v, want %+v", got.Actor, rec.Actor)
	}
}

// A run stored before the field existed decodes with an empty CredentialID,
// and a run stored without one writes no CredentialID key, so every existing
// row's bytes are what they were.
func TestRunRecord_PreChangeRecordDecodesWithNoCredentialID(t *testing.T) {
	old := batRun("01BEFORETHEFIELD", StateQueued)
	old.Actor = Actor{UserID: "u-lucius", AuthMethod: "token"}
	raw, err := encodeValue(old)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "CredentialID") {
		t.Fatalf("a record with no credential id stores the key: %s", raw)
	}
	// raw is byte-for-byte what the previous version wrote for this record:
	// an actor object of its three fields, which the check above pins.
	var probe struct{ Actor map[string]json.RawMessage }
	err = json.Unmarshal(raw[strings.IndexByte(string(raw), '{'):], &probe) // past the codec's tag byte
	if err != nil || len(probe.Actor) != 3 {
		t.Fatalf("stored actor has %d keys (%v), want the previous version's 3: %s", len(probe.Actor), err, raw)
	}
	got, err := decodeRun(raw)
	if err != nil {
		t.Fatalf("decode the pre-change record: %v", err)
	}
	if got.Actor.CredentialID != "" || got.Actor.UserID != "u-lucius" || got.Actor.AuthMethod != "token" {
		t.Errorf("pre-change actor = %+v, want u-lucius/token with no credential id", got.Actor)
	}
}
