// file: core/backup/testkit_test.go
// version: 1.0.0
// guid: 0b4e9f0a-9d6f-4f7e-8d52-0e0c6fd0a3b1
// last-edited: 2026-11-12

package backup_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/backup"
)

// bruceKeys is a journal.Keys over a fixed set of derivation keys, the active
// one named active; a retired ID is simply absent.
type bruceKeys struct {
	active string
	keys   map[string][]byte
}

func newBruceKeys(active string, ids ...string) *bruceKeys {
	k := &bruceKeys{active: active, keys: map[string][]byte{}}
	for _, id := range ids {
		k.keys[id] = []byte("wayne-manor-derive-" + id)
	}
	return k
}

func (k *bruceKeys) ActiveDeriveID() string { return k.active }
func (k *bruceKeys) DeriveKeyFor(kid, info string) ([]byte, error) {
	root, ok := k.keys[kid]
	if !ok {
		return nil, errors.New("key unavailable: " + kid)
	}
	h := hmac.New(sha256.New, root)
	h.Write([]byte(info))
	return h.Sum(nil), nil
}

func (k *bruceKeys) KeyIDs() []string {
	var out []string
	for id := range k.keys {
		out = append(out, id)
	}
	return out
}

func hexOf(s string) string {
	h := sha256.Sum256([]byte(s))
	const digits = "0123456789abcdef"
	var b strings.Builder
	for _, c := range h {
		b.WriteByte(digits[c>>4])
		b.WriteByte(digits[c&15])
	}
	return b.String()
}

// manifestFixture is a signed manifest of two entries.
func manifestFixture(t *testing.T, keys *bruceKeys) *backup.Manifest {
	t.Helper()
	m := &backup.Manifest{
		Format:      backup.FormatMediacore,
		ArchiveID:   "01JBATMAN0000000000000000A",
		App:         "gotham",
		AppVersion:  "1.0.0",
		CoreVersion: "0.9.0",
		InstallID:   "install-batcave",
		CreatedAt:   time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC),
		Reason:      "manual",
		JournalSeq:  7,
		Levels:      map[string]int{"core": 1},
		Keyspaces:   map[string]backup.KSInfo{"core": {Version: 3, Keys: 10, Bytes: 100}},
		Modules:     map[string]string{"books": "1.0.0"},
		Entries: []backup.Entry{
			{Path: "db/000001.sst", Size: 5, SHA256: hexOf("one"), Class: backup.SystemOfRecord},
			{Path: "config/config.boot.json", Size: 3, SHA256: hexOf("two"), Class: backup.SystemOfRecord},
		},
	}
	if err := backup.Sign(m, keys); err != nil {
		t.Fatal(err)
	}
	return m
}
