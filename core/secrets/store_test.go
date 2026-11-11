// file: core/secrets/store_test.go
// version: 1.0.1
// guid: 45016034-b979-435e-b52a-6ddb35c3501c
// last-edited: 2026-11-10

package secrets

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/storage"
)

// TestAAD_SwapBetweenNamesFails (05-config §2.9): a record copied to another
// name, or read under another app ID, fails authentication with ErrCorrupt.
func TestAAD_SwapBetweenNamesFails(t *testing.T) {
	e := newEnv(t)
	s := e.open(t, nil)
	mustPut(t, s, nameGate, valueVengeance)
	rawSet(t, e.ks, recordKey(nameModule), rawGet(t, e.ks, recordKey(nameGate)))
	_, m, err := s.Get(context.Background(), nameModule)
	if !errors.Is(err, ErrCorrupt) || !errors.Is(m.Err, ErrCorrupt) {
		t.Fatalf("Get of a record moved to another name = %v (Meta.Err %v), want ErrCorrupt", err, m.Err)
	}
	mustGet(t, s, nameGate, valueVengeance)

	rec, err := decodeRecord(rawGet(t, e.ks, recordKey(nameGate)))
	if err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	_, err = s.ring.open("arkham", nameGate, rec)
	s.mu.RUnlock()
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("a record read under another app ID = %v, want ErrCorrupt", err)
	}

	// A flipped ciphertext byte is ErrCorrupt too, never repaired.
	rec.CT[0] ^= 0x01
	raw, err := encodeRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	rawSet(t, e.ks, recordKey(nameOracle), raw)
	if _, _, err := s.Get(context.Background(), nameOracle); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Get of a tampered record = %v, want ErrCorrupt", err)
	}
	if got := rawGet(t, e.ks, recordKey(nameOracle)); !bytes.Equal(got, raw) {
		t.Fatal("a read rewrote a corrupt record")
	}
}

// TestGet_ReadOrder (05-config §2.9): an operator value beats the record; a
// cleared marker is ErrNotSet; an unreadable marker is Meta.Err; nothing at
// all is ErrNotSet.
func TestGet_ReadOrder(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	s := e.open(t, nil)

	if _, m, err := s.Get(ctx, nameGate); !errors.Is(err, ErrNotSet) || m.Set {
		t.Fatalf("Get of nothing = %v (Set %v), want ErrNotSet", err, m.Set)
	}
	if m, err := s.Meta(ctx, nameGate); err != nil || m.Set {
		t.Fatalf("Meta of nothing = %+v, %v; want Set false and no error", m, err)
	}

	put := mustPut(t, s, nameGate, valueVengeance)
	m := mustGet(t, s, nameGate, valueVengeance)
	if m.Source != SourceStored || m.KeyID != "k1" || m.UpdatedBy != byAlfred || m.UpdatedAt.IsZero() || m.Fingerprint != put.Fingerprint || len(m.Fingerprint) != 8 {
		t.Fatalf("stored Meta = %+v (Put returned %+v)", m, put)
	}

	s.Ephemeral(nameGate, NewValue(valueSerious), SourceEnv)
	m = mustGet(t, s, nameGate, valueSerious)
	if m.Source != SourceEnv || m.KeyID != "" {
		t.Fatalf("operator Meta = %+v, want SourceEnv and no key ID", m)
	}
	if _, err := s.Put(ctx, nameGate, NewValue(valueClock), byAlfred); err == nil {
		t.Fatal("Put of a name an operator value locks was accepted")
	}
	if err := s.Delete(ctx, nameGate, byAlfred); err == nil {
		t.Fatal("Delete of a name an operator value locks was accepted")
	}

	mustPut(t, s, nameModule, valueClock)
	if err := s.Delete(ctx, nameModule, byAlfred); err != nil {
		t.Fatal(err)
	}
	if _, m, err := s.Get(ctx, nameModule); !errors.Is(err, ErrNotSet) || m.Set || m.UpdatedBy != byAlfred {
		t.Fatalf("Get of a cleared marker = %v (%+v), want ErrNotSet with Set false", err, m)
	}
	if m, err := s.Meta(ctx, nameModule); err != nil || m.Set {
		t.Fatalf("Meta of a cleared marker = %+v, %v", m, err)
	}

	for reason, want := range map[string]error{reasonKey: ErrKeyUnavailable, reasonCorrupt: ErrCorrupt} {
		b := e.ks.NewBatch()
		if err := s.StageUnreadable(ctx, b, nameOracle, reason, "cutover:m2"); err != nil {
			t.Fatal(err)
		}
		if err := b.Commit(ctx, storage.Sync); err != nil {
			t.Fatal(err)
		}
		_, m, err := s.Get(ctx, nameOracle)
		if !errors.Is(err, want) || !errors.Is(m.Err, want) || !m.Set {
			t.Fatalf("Get of a %q marker = %v (%+v), want %v", reason, err, m, want)
		}
		if m, err := s.Meta(ctx, nameOracle); err != nil || !errors.Is(m.Err, want) {
			t.Fatalf("Meta of a %q marker = %+v, %v", reason, m, err)
		}
	}
	if _, _, err := s.Get(ctx, "batman"); err == nil || errors.Is(err, ErrNotSet) {
		t.Fatalf("Get of an invalid name = %v, want a grammar error", err)
	}
}

// TestStageUnreadable_ReadsReenter (05-config §2.9): a staged unreadable
// marker reads ErrKeyUnavailable or ErrCorrupt and is listed by
// Undecryptable (health secrets_undecryptable); a Put replaces it and clears
// the listing.
func TestStageUnreadable_ReadsReenter(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	s := e.open(t, nil)
	b := e.ks.NewBatch()
	if err := s.StageUnreadable(ctx, b, nameGate, reasonKey, "cutover:m2"); err != nil {
		t.Fatal(err)
	}
	if err := s.StageUnreadable(ctx, b, nameModule, reasonCorrupt, "cutover:m2"); err != nil {
		t.Fatal(err)
	}
	if got := rows(t, e.ks); len(got) != 1 {
		t.Fatal("StageUnreadable committed before the caller did")
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	bad, err := s.Undecryptable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(bad, []Name{nameGate, nameModule}) {
		t.Fatalf("Undecryptable = %v", bad)
	}
	if _, _, err := s.Get(ctx, nameGate); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("Get = %v, want ErrKeyUnavailable", err)
	}
	if _, _, err := s.Get(ctx, nameModule); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Get = %v, want ErrCorrupt", err)
	}
	mustPut(t, s, nameGate, valueVengeance)
	mustGet(t, s, nameGate, valueVengeance)
	if bad, err = s.Undecryptable(ctx); err != nil || !slices.Equal(bad, []Name{nameModule}) {
		t.Fatalf("Undecryptable after the Put = %v, %v", bad, err)
	}
	for _, reason := range []string{"", "joker", "KEY"} {
		if err := s.StageUnreadable(ctx, e.ks.NewBatch(), nameOracle, reason, "cutover:m2"); err == nil {
			t.Errorf("StageUnreadable accepted reason %q", reason)
		}
	}
	if err := s.StageUnreadable(ctx, e.ks.NewBatch(), nameOracle, reasonKey, ""); err == nil {
		t.Error("StageUnreadable accepted no actor")
	}
}

// TestScope_RefusesOutsidePrefix (05-config §2.9): a modules.books. scope
// cannot reach core.*, integrations.* or another module's names, and only
// modules.<id>. and core.<subsystem>. are scopes at all.
func TestScope_RefusesOutsidePrefix(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	s := e.open(t, nil)
	mustPut(t, s, nameGate, valueVengeance)
	mustPut(t, s, nameInteg, valueSerious)

	sc := s.Scope("modules.batcomputer.")
	if sc.Prefix() != "modules.batcomputer." {
		t.Fatalf("Prefix = %q", sc.Prefix())
	}
	if _, err := sc.Put(ctx, nameModule, NewValue(valueClock), byAlfred); err != nil {
		t.Fatalf("an in-scope Put: %v", err)
	}
	if v, _, err := sc.Get(ctx, nameModule); err != nil || v.Reveal() != valueClock {
		t.Fatalf("an in-scope Get failed: %v", err)
	}
	for _, n := range []Name{nameGate, nameInteg, "modules.batcomputerx.key", "modules.batcave.key", "modules.batcomputer"} {
		if _, _, err := sc.Get(ctx, n); !errors.Is(err, ErrOutOfScope) {
			t.Errorf("Get %s = %v, want ErrOutOfScope", n, err)
		}
		if _, err := sc.Meta(ctx, n); !errors.Is(err, ErrOutOfScope) {
			t.Errorf("Meta %s = %v, want ErrOutOfScope", n, err)
		}
		if _, err := sc.Put(ctx, n, NewValue(valueClock), byAlfred); !errors.Is(err, ErrOutOfScope) {
			t.Errorf("Put %s = %v, want ErrOutOfScope", n, err)
		}
		if err := sc.Delete(ctx, n, byAlfred); !errors.Is(err, ErrOutOfScope) {
			t.Errorf("Delete %s = %v, want ErrOutOfScope", n, err)
		}
	}
	mustGet(t, s, nameGate, valueVengeance)

	for _, p := range []string{"", "modules.", "modules.batcomputer", "integrations.wayne.", "core.http.basic_gate.", "Modules.x.", "modules..", "batcave.x."} {
		bad := s.Scope(p)
		if _, _, err := bad.Get(ctx, Name(p+"key")); !errors.Is(err, ErrOutOfScope) {
			t.Errorf("scope %q: Get = %v, want ErrOutOfScope", p, err)
		}
		if _, err := bad.DeriveKey("x"); !errors.Is(err, ErrOutOfScope) {
			t.Errorf("scope %q: DeriveKey = %v, want ErrOutOfScope", p, err)
		}
	}
	if _, _, err := (Scope{}).Get(ctx, nameModule); !errors.Is(err, ErrOutOfScope) {
		t.Errorf("the zero Scope: %v", err)
	}
	if _, err := s.Scope("core.http.").Meta(ctx, nameGate); err != nil {
		t.Errorf("a core scope cannot read its own name: %v", err)
	}

	// Scoped derivations never collide with each other or with the store's.
	a, err := s.Scope("modules.batcomputer.").DeriveKey("state")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Scope("modules.batcave.").DeriveKey("state")
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.DeriveKey("state")
	if err != nil {
		t.Fatal(err)
	}
	direct, err := s.DeriveKey("modules.batcomputer.state")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) || bytes.Equal(a, c) || !bytes.Equal(a, direct) {
		t.Fatal("scoped derivations are not prefixed by the scope")
	}
	af, err := s.Scope("modules.batcomputer.").DeriveKeyFor("d1", "state")
	if err != nil || !bytes.Equal(af, a) {
		t.Fatalf("Scope.DeriveKeyFor(d1) differs from Scope.DeriveKey: %v", err)
	}
}

// TestStage_TakesBatchPointer (05-config §2.9, COR2-12): Stage writes into
// the caller's batch, which another keyspace started, and lands only with
// that batch's Commit; a batch it was never committed with writes nothing; a
// batch of another store is refused.
func TestStage_TakesBatchPointer(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	s := e.open(t, nil)

	b := e.other.NewBatch()
	if err := b.Set([]byte("batsignal"), []byte("lit")); err != nil {
		t.Fatal(err)
	}
	if err := s.Stage(ctx, b, Write{Name: nameGate, Value: NewValue(valueVengeance), By: byAlfred}); err != nil {
		t.Fatal(err)
	}
	if rawGet(t, e.ks, recordKey(nameGate)) != nil {
		t.Fatal("Stage committed on its own")
	}
	if !slices.Contains(b.Participants(), secretsOwner) {
		t.Fatal("Stage did not bind c/secrets/ to the caller's batch")
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	mustGet(t, s, nameGate, valueVengeance)
	if rawGet(t, e.other, []byte("batsignal")) == nil {
		t.Fatal("the caller's own write did not land with the staged secret")
	}

	// A clearing Stage writes the cleared marker.
	cb := e.other.NewBatch()
	if err := s.Stage(ctx, cb, Write{Name: nameGate, Delete: true, By: byAlfred}); err != nil {
		t.Fatal(err)
	}
	if err := cb.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get(ctx, nameGate); !errors.Is(err, ErrNotSet) {
		t.Fatalf("Get after a clearing Stage = %v", err)
	}

	// Discarded: nothing lands.
	d := e.other.NewBatch()
	if err := s.Stage(ctx, d, Write{Name: nameModule, Value: NewValue(valueSerious), By: byAlfred}); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if rawGet(t, e.ks, recordKey(nameModule)) != nil {
		t.Fatal("a staged write of a discarded batch landed")
	}

	// The Expect makes a write committed in between conflict.
	c := e.other.NewBatch()
	if err := s.Stage(ctx, c, Write{Name: nameOracle, Value: NewValue(valueClock), By: byAlfred}); err != nil {
		t.Fatal(err)
	}
	mustPut(t, s, nameOracle, valueSerious)
	if err := c.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("a stale staged write committed: %v", err)
	}
	mustGet(t, s, nameOracle, valueSerious)

	// Another store's batch is refused.
	f := newEnv(t)
	if err := s.Stage(ctx, f.other.NewBatch(), Write{Name: nameModule, Value: NewValue(valueSerious), By: byAlfred}); !errors.Is(err, storage.ErrOutsideKeyspace) {
		t.Fatalf("Stage into another store's batch = %v, want ErrOutsideKeyspace", err)
	}
	for name, w := range map[string]Write{
		"no actor":          {Name: nameModule, Value: NewValue(valueSerious)},
		"no value":          {Name: nameModule, By: byAlfred},
		"delete with value": {Name: nameModule, Value: NewValue(valueSerious), Delete: true, By: byAlfred},
		"bad name":          {Name: "batman", Value: NewValue(valueSerious), By: byAlfred},
	} {
		if err := s.Stage(ctx, e.other.NewBatch(), w); err == nil {
			t.Errorf("%s: Stage accepted it", name)
		}
	}
	if err := s.Stage(ctx, nil, Write{Name: nameModule, Value: NewValue(valueSerious), By: byAlfred}); err == nil {
		t.Error("Stage accepted a nil batch")
	}
}

// TestImporterCreateOnly (05-storage §5S.3.13's importer rule over
// secrets.Importer): the importer stages into the caller's batch and never
// commits; an absent record is created; the same value, or the same
// unreadable marker, is already current; anything else is
// storage.ErrImportConflict.
func TestImporterCreateOnly(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	s := e.open(t, nil)
	im := s.Importer()
	imp := func(n Name, v string) error {
		b := e.ks.NewBatch()
		defer func() { _ = b.Close() }()
		if err := im.ImportSecret(ctx, b, Write{Name: n, Value: NewValue(v), By: "cutover:m2"}); err != nil {
			return err
		}
		return b.Commit(ctx, storage.Sync)
	}
	impUnreadable := func(n Name, reason string) error {
		b := e.ks.NewBatch()
		defer func() { _ = b.Close() }()
		if err := im.ImportUnreadable(ctx, b, n, reason, "cutover:m2"); err != nil {
			return err
		}
		return b.Commit(ctx, storage.Sync)
	}

	b := e.ks.NewBatch()
	if err := im.ImportSecret(ctx, b, Write{Name: nameGate, Value: NewValue(valueVengeance), By: "cutover:m2"}); err != nil {
		t.Fatal(err)
	}
	if rawGet(t, e.ks, recordKey(nameGate)) != nil {
		t.Fatal("the importer committed")
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	m := mustGet(t, s, nameGate, valueVengeance)
	if m.KeyID != "k1" {
		t.Fatalf("the import is sealed under %q, want the active data key k1", m.KeyID)
	}
	before := rawGet(t, e.ks, recordKey(nameGate))
	if err := imp(nameGate, valueVengeance); err != nil {
		t.Fatalf("re-importing the same value = %v, want already current", err)
	}
	if !bytes.Equal(rawGet(t, e.ks, recordKey(nameGate)), before) {
		t.Fatal("re-importing the same value rewrote the record")
	}
	if err := imp(nameGate, valueSerious); !errors.Is(err, storage.ErrImportConflict) {
		t.Fatalf("importing another value = %v, want ErrImportConflict", err)
	}
	mustGet(t, s, nameGate, valueVengeance)

	mustPut(t, s, nameModule, valueClock)
	if err := s.Delete(ctx, nameModule, byAlfred); err != nil {
		t.Fatal(err)
	}
	if err := imp(nameModule, valueClock); !errors.Is(err, storage.ErrImportConflict) {
		t.Fatalf("importing over a cleared marker = %v, want ErrImportConflict", err)
	}

	if err := impUnreadable(nameOracle, reasonKey); err != nil {
		t.Fatal(err)
	}
	if err := impUnreadable(nameOracle, reasonKey); err != nil {
		t.Fatalf("re-importing the same marker = %v", err)
	}
	if err := impUnreadable(nameOracle, reasonCorrupt); !errors.Is(err, storage.ErrImportConflict) {
		t.Fatalf("another marker = %v, want ErrImportConflict", err)
	}
	if err := imp(nameOracle, valueClock); !errors.Is(err, storage.ErrImportConflict) {
		t.Fatalf("a value over a marker = %v, want ErrImportConflict", err)
	}
	if err := impUnreadable(nameGate, reasonKey); !errors.Is(err, storage.ErrImportConflict) {
		t.Fatalf("a marker over a value = %v, want ErrImportConflict", err)
	}

	// An operator value does not refuse the import; a delete is not
	// importable.
	s.Ephemeral(nameInteg, NewValue(valueClock), SourceEnv)
	if err := imp(nameInteg, valueSerious); err != nil {
		t.Fatalf("an import under an operator value = %v", err)
	}
	if err := im.ImportSecret(ctx, e.ks.NewBatch(), Write{Name: nameModule, Delete: true, By: "cutover:m2"}); err == nil {
		t.Fatal("the importer accepted a delete")
	}

	// A conflict found at commit: a value committed after the staging.
	race := e.ks.NewBatch()
	if err := im.ImportSecret(ctx, race, Write{Name: "core.http.basic_gate.user", Value: NewValue(valueClock), By: "cutover:m2"}); err != nil {
		t.Fatal(err)
	}
	mustPut(t, s, "core.http.basic_gate.user", valueSerious)
	if err := race.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("an import racing a write committed: %v", err)
	}
}

// TestEphemeral_NeverPersisted (05-config §2.9): an operator value is read,
// listed and locks its name, and never reaches storage.
func TestEphemeral_NeverPersisted(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	s := e.open(t, nil)
	before := rows(t, e.ks)
	s.Ephemeral(nameGate, NewValue(valueVengeance), SourceEnv)
	s.Ephemeral(nameModule, NewValue(valueSerious), SourceFile)
	mustGet(t, s, nameGate, valueVengeance)
	after := rows(t, e.ks)
	if len(after) != len(before) {
		t.Fatalf("an operator value wrote %d rows", len(after)-len(before))
	}
	for k, v := range after {
		assertNoPlaintext(t, "c/secrets/ key "+k, v)
	}
	var names []Name
	for m, err := range s.All(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, m.Name)
		if m.Name == nameModule && m.Source != SourceFile {
			t.Errorf("All lists the file value as source %d", m.Source)
		}
	}
	if !slices.Equal(names, []Name{nameGate, nameModule}) {
		t.Fatalf("All = %v", names)
	}
	if err := s.Stage(ctx, e.ks.NewBatch(), Write{Name: nameGate, Value: NewValue(valueClock), By: byAlfred}); err == nil {
		t.Fatal("Stage of a locked name was accepted")
	}
	s.Ephemeral("batman", NewValue(valueClock), SourceEnv)
	s.Ephemeral(nameOracle, NewValue(valueClock), SourceStored)
	if _, _, err := s.Get(ctx, nameOracle); !errors.Is(err, ErrNotSet) {
		t.Fatalf("an operator value with source SourceStored was registered: %v", err)
	}
	assertNoPlaintext(t, "the store's log", e.logs.String())
}

// TestFingerprint_StableNotReversible (05-config §2.9): the same value has
// the same fingerprint in one keyring and another in a different keyring;
// it is 8 hex characters and not the value.
func TestFingerprint_StableNotReversible(t *testing.T) {
	e := newEnv(t)
	s := e.open(t, nil)
	a := mustPut(t, s, nameGate, valueVengeance).Fingerprint
	b := mustPut(t, s, nameModule, valueVengeance).Fingerprint
	c := mustPut(t, s, nameOracle, valueSerious).Fingerprint
	s.Ephemeral(nameInteg, NewValue(valueVengeance), SourceEnv)
	m, err := s.Meta(context.Background(), nameInteg)
	if err != nil {
		t.Fatal(err)
	}
	if a != b || a != m.Fingerprint {
		t.Fatal("the same value has different fingerprints in one keyring")
	}
	if a == c {
		t.Fatal("two values share a fingerprint")
	}
	if len(a) != 8 || strings.Trim(a, "0123456789abcdef") != "" {
		t.Fatalf("fingerprint %q is not 8 hex characters", a)
	}
	other := newEnv(t).open(t, nil)
	if mustPut(t, other, nameGate, valueVengeance).Fingerprint == a {
		t.Fatal("two keyrings give the same fingerprint")
	}
}

// TestDeriveKey_IndependentOfDataKey (05-config §2.9): replacing the active
// data key in the keyring leaves DeriveKey and fingerprints unchanged.
func TestDeriveKey_IndependentOfDataKey(t *testing.T) {
	e := newEnv(t)
	s := e.open(t, nil)
	k, err := s.DeriveKey("core.backup/age-identity/v1")
	if err != nil {
		t.Fatal(err)
	}
	fp := mustPut(t, s, nameGate, valueVengeance).Fingerprint
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	f := e.readKeyringFile(t)
	nk := make(keyMaterial, keyBytes)
	if _, err := rand.Read(nk); err != nil {
		t.Fatal(err)
	}
	f.Keys[0] = keyringKey{ID: "k2", Use: useData, Created: f.Keys[0].Created, Status: statusActive, Material: nk}
	if err := writeKeyring(e.root, defaultKeyFile, f); err != nil {
		t.Fatal(err)
	}
	r := e.open(t, nil)
	k2, err := r.DeriveKey("core.backup/age-identity/v1")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(k, k2) {
		t.Fatal("DeriveKey changed with the data key")
	}
	if got := mustPut(t, r, nameModule, valueVengeance); got.Fingerprint != fp || got.KeyID != "k2" {
		t.Fatalf("after the data-key swap: fingerprint same=%v, key %q", got.Fingerprint == fp, got.KeyID)
	}
	if _, _, err := r.Get(context.Background(), nameGate); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("a record under the removed data key = %v, want ErrKeyUnavailable", err)
	}
	if bytes.Equal(k2, mustSubkey(t, nk, "core.backup/age-identity/v1")) {
		t.Fatal("DeriveKey derives from the data key")
	}
	for _, info := range []string{"", "mediacore/secrets/aes-256-gcm/v1", "mediacore/secrets/fingerprint/v1", "mediacore/secrets/anything"} {
		if _, err := r.DeriveKey(info); err == nil {
			t.Errorf("DeriveKey(%q) was accepted", info)
		}
	}
}

func mustSubkey(t *testing.T, m []byte, info string) []byte {
	t.Helper()
	k, err := subkey(m, info)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// TestAll_NeverValues (a test name coined by F3-03): All lists every record
// and operator value as Meta, sorted, undecryptable ones with Meta.Err, and
// no value or plaintext reaches it.
func TestAll_NeverValues(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	s := e.open(t, nil)
	mustPut(t, s, nameModule, valueSerious)
	mustPut(t, s, nameGate, valueVengeance)
	mustPut(t, s, nameOracle, valueClock)
	if err := s.Delete(ctx, nameOracle, byAlfred); err != nil {
		t.Fatal(err)
	}
	rawSet(t, e.ks, recordKey(nameInteg), []byte(`{"v":1,"batarang":true}`))
	rawSet(t, e.ks, []byte("not-a-record"), []byte("x"))
	var got []string
	for m, err := range s.All(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s set=%v err=%v", m.Name, m.Set, m.Err))
		assertNoPlaintext(t, "All", fmt.Sprintf("%#v", m))
	}
	want := []string{
		string(nameGate) + " set=true err=<nil>",
		string(nameInteg) + " set=true err=" + ErrCorrupt.Error(),
		string(nameModule) + " set=true err=<nil>",
		string(nameOracle) + " set=false err=<nil>",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("All =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	n := 0
	for range s.All(ctx) {
		n++
		break
	}
	if n != 1 {
		t.Fatal("All did not stop when the consumer did")
	}
}

// TestKeyIDsReferenced (a test name coined by F3-03): every key ID the
// sealed records of a reader name, sorted, markers and malformed records
// skipped.
func TestKeyIDsReferenced(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	s := e.open(t, nil)
	mustPut(t, s, nameGate, valueVengeance)
	rec, err := decodeRecord(rawGet(t, e.ks, recordKey(nameGate)))
	if err != nil {
		t.Fatal(err)
	}
	rec.KID = "k7"
	raw, err := encodeRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	rawSet(t, e.ks, recordKey(nameModule), raw)
	rawSet(t, e.ks, recordKey(nameInteg), []byte(`garbage`))
	if err := s.Delete(ctx, nameOracle, byAlfred); err != nil {
		t.Fatal(err)
	}
	ids, err := s.KeyIDsReferenced(ctx, e.ks)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids, []string{"k1", "k7"}) {
		t.Fatalf("KeyIDsReferenced = %v", ids)
	}
	if _, err := s.KeyIDsReferenced(ctx, nil); err == nil {
		t.Fatal("a nil reader was accepted")
	}
}

// TestPut_SerializesPerName (a test name coined by F3-03): concurrent Puts
// of one name all succeed (the keyed mutex plus the conflict retry), and the
// record ends as one of them.
func TestPut_SerializesPerName(t *testing.T) {
	e := newEnv(t)
	s := e.open(t, nil)
	values := []string{valueVengeance, valueSerious, valueClock}
	var wg sync.WaitGroup
	errs := make(chan error, 30)
	for i := range 30 {
		wg.Go(func() {
			n := []Name{nameGate, nameModule}[i%2]
			if _, err := s.Put(context.Background(), n, NewValue(values[i%3]), byAlfred); err != nil {
				errs <- err
			}
			if _, _, err := s.Get(context.Background(), n); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("a concurrent Put or Get failed: %v", err)
	}
	v, _, err := s.Get(context.Background(), nameGate)
	if err != nil || !slices.Contains(values, v.Reveal()) {
		t.Fatalf("the record holds none of the values put: %v", err)
	}
}

// TestClose_WipesAndRefuses (a test name coined by F3-03): after Close the
// key material is zero and every method refuses.
func TestClose_WipesAndRefuses(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	s := e.open(t, nil)
	mustPut(t, s, nameGate, valueVengeance)
	s.Ephemeral(nameModule, NewValue(valueSerious), SourceEnv)
	data := s.ring.data.Material
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("a second Close: %v", err)
	}
	if !bytes.Equal(data, make([]byte, keyBytes)) {
		t.Fatal("Close left key material in memory")
	}
	if _, _, err := s.Get(ctx, nameGate); !errors.Is(err, errClosed) {
		t.Errorf("Get after Close = %v", err)
	}
	if _, err := s.Put(ctx, nameGate, NewValue(valueClock), byAlfred); !errors.Is(err, errClosed) {
		t.Errorf("Put after Close = %v", err)
	}
	if _, err := s.DeriveKey("x"); !errors.Is(err, errClosed) {
		t.Errorf("DeriveKey after Close = %v", err)
	}
	if err := s.Importer().ImportSecret(ctx, e.ks.NewBatch(), Write{Name: nameOracle, Value: NewValue(valueClock), By: "x"}); !errors.Is(err, errClosed) {
		t.Errorf("ImportSecret after Close = %v", err)
	}
	if s.ActiveDeriveID() != "" || s.KeyIDs() != nil {
		t.Error("a closed store still reports its keys")
	}
}

// TestNoSecretInLogsOrErrors (a test name coined by F3-03): no plaintext
// value reaches a log line, an error, a Meta or a c/secrets/ row, on any
// path, failing ones included.
func TestNoSecretInLogsOrErrors(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	s := e.open(t, nil)
	var texts []string
	note := func(err error) {
		if err != nil {
			texts = append(texts, err.Error())
		}
	}
	m, err := s.Put(ctx, nameGate, NewValue(valueVengeance), byAlfred)
	note(err)
	texts = append(texts, fmt.Sprintf("%+v %#v", m, m))
	_, err = s.Put(ctx, nameGate, NewValue(valueSerious), "")
	note(err)
	s.Ephemeral(nameModule, NewValue(valueClock), SourceEnv)
	_, err = s.Put(ctx, nameModule, NewValue(valueSerious), byAlfred)
	note(err)
	note(s.Importer().ImportSecret(ctx, e.ks.NewBatch(), Write{Name: nameGate, Value: NewValue(valueSerious), By: "cutover:m2"}))
	rawSet(t, e.ks, recordKey(nameOracle), rawGet(t, e.ks, recordKey(nameGate)))
	v, m2, err := s.Get(ctx, nameOracle)
	note(err)
	texts = append(texts, fmt.Sprintf("%v %+v", v, m2))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(e.keyringPath()); err != nil {
		t.Fatal(err)
	}
	_, err = Open(ctx, e.opts())
	note(err)
	texts = append(texts, e.logs.String())
	for k, v := range rows(t, e.ks) {
		texts = append(texts, k, v)
	}
	for i, s := range texts {
		assertNoPlaintext(t, fmt.Sprintf("surface %d", i), s)
	}
}

// TestMeta_StorageErrorIsAnError (a test name coined by F3-03): a storage
// failure is returned, not folded into Meta.
func TestMeta_StorageErrorIsAnError(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	s := e.open(t, nil)
	if err := e.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Meta(ctx, nameGate); err == nil || errors.Is(err, ErrNotSet) {
		t.Fatalf("Meta on a closed store = %v, want a storage error", err)
	}
	if _, err := s.Put(ctx, nameGate, NewValue(valueVengeance), byAlfred); err == nil {
		t.Fatal("Put on a closed store succeeded")
	}
}

// TestUnexportedKeys_ConcurrentWithClose (a test name coined by the F3-03
// follow-up): UnexportedKeys running beside Close never digests key material
// Close is wiping. A digest taken after the read lock is released reads the
// keyring's memory while Close clears it; -race does not report that (clear
// and the assembly SHA-256 are not instrumented, measured), but the digest of
// zeroed bytes is not in the export record, so a fully exported keyring is
// reported as unexported. So the test asserts the answer: each call says
// "every key exported" or errClosed, nothing else. Run it with -race too.
func TestUnexportedKeys_ConcurrentWithClose(t *testing.T) {
	for round := range 10 {
		e := newEnv(t)
		s := e.open(t, nil)
		if _, err := ExportKeyring(e.root, "", filepath.Join(t.TempDir(), "keyring.export"), e.clock.now()); err != nil {
			t.Fatal(err)
		}
		if out, err := s.UnexportedKeys(); err != nil || len(out) != 0 {
			t.Fatalf("round %d: before Close: UnexportedKeys = %v, %v, want none", round, out, err)
		}
		// Close starts once every reader has answered once, so the readers
		// are mid-loop when it wipes the keyring.
		var running, wg sync.WaitGroup
		running.Add(4)
		for range 4 {
			wg.Go(func() {
				for i := 0; ; i++ {
					out, err := s.UnexportedKeys()
					if i == 0 {
						running.Done()
					}
					switch {
					case errors.Is(err, errClosed):
						if i == 0 {
							t.Errorf("round %d: UnexportedKeys returned errClosed before Close", round)
						}
						return
					case err != nil || len(out) != 0:
						t.Errorf("round %d: UnexportedKeys beside Close = %v, %v, want none or errClosed", round, out, err)
						return
					}
				}
			})
		}
		running.Wait()
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		wg.Wait()
	}
}

// TestEphemeral_ReplaceBesideGet (a test name coined by the F3-03 follow-up):
// Get, Meta and All running beside Ephemeral replacing an operator value
// always see one whole value, never the replaced value's wiped (zeroed)
// bytes: readers copy under the store's lock, and the replace wipes the old
// value only once the new one is in place, under the write lock. Run it with
// -race.
func TestEphemeral_ReplaceBesideGet(t *testing.T) {
	e := newEnv(t)
	s := e.open(t, nil)
	ctx := context.Background()
	values := []string{valueVengeance, valueSerious}
	fps := map[string]bool{}
	for _, v := range values {
		fps[s.ring.fingerprint([]byte(v))] = true
	}
	s.Ephemeral(nameGate, NewValue(values[0]), SourceEnv)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				v, m, err := s.Get(ctx, nameGate)
				if err != nil || !slices.Contains(values, v.Reveal()) || !fps[m.Fingerprint] {
					t.Errorf("Get beside a replace = %q (fingerprint %s), %v; want one whole value", v.Bytes(), m.Fingerprint, err)
					return
				}
				if m, err := s.Meta(ctx, nameGate); err != nil || !fps[m.Fingerprint] {
					t.Errorf("Meta beside a replace = fingerprint %s, %v", m.Fingerprint, err)
					return
				}
				for m, err := range s.All(ctx) {
					if err != nil || (m.Name == nameGate && !fps[m.Fingerprint]) {
						t.Errorf("All beside a replace = %+v, %v", m, err)
						return
					}
				}
			}
		})
	}
	for i := range 2000 {
		s.Ephemeral(nameGate, NewValue(values[(i+1)%2]), SourceEnv)
	}
	close(stop)
	wg.Wait()
}
