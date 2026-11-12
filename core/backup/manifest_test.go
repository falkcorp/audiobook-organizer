// file: core/backup/manifest_test.go
// version: 1.0.0
// guid: 4ad03fd1-6db8-4bd8-86a6-7e6ac0a60d02
// last-edited: 2026-11-12

package backup_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/backup"
)

func TestProvenanceBindsInstallAndEntries(t *testing.T) {
	keys := newBruceKeys("d1", "d1")
	sign := func(m *backup.Manifest) *backup.Manifest { // round trip through the wire form
		b, err := backup.MarshalManifest(m)
		if err != nil {
			t.Fatal(err)
		}
		out, err := backup.ParseManifest(b)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	m := sign(manifestFixture(t, keys))
	if got := backup.Classify(m, keys, "install-batcave"); got != backup.Own {
		t.Fatalf("a freshly signed manifest is %v, want own", got)
	}
	if got := backup.Classify(m, keys, ""); got != backup.Own {
		t.Fatalf("with no live store (host loss) the same manifest is %v, want own", got)
	}

	cases := map[string]func(*backup.Manifest){
		"edited entry hash":    func(m *backup.Manifest) { m.Entries[0].SHA256 = hexOf("planted") },
		"edited entry size":    func(m *backup.Manifest) { m.Entries[0].Size++ },
		"swapped entry hashes": func(m *backup.Manifest) { m.Entries[0].SHA256, m.Entries[1].SHA256 = m.Entries[1].SHA256, m.Entries[0].SHA256 },
		"dropped entry":        func(m *backup.Manifest) { m.Entries = m.Entries[:1] },
		"added entry":          func(m *backup.Manifest) { m.Entries = append(m.Entries, backup.Entry{Path: "db/x", SHA256: hexOf("x")}) },
		"edited install ID":    func(m *backup.Manifest) { m.InstallID = "install-arkham" },
		"edited journal seq":   func(m *backup.Manifest) { m.JournalSeq = 0 },
		"edited levels":        func(m *backup.Manifest) { m.Levels["core"] = 0 },
		"edited key ID":        func(m *backup.Manifest) { m.Provenance.KeyID = "d2" },
		"edited app":           func(m *backup.Manifest) { m.App = "arkham" },
		"edited reason":        func(m *backup.Manifest) { m.Reason = "pre-update" },
		"cleared MAC":          func(m *backup.Manifest) { m.Provenance.MAC = "" },
		"truncated MAC":        func(m *backup.Manifest) { m.Provenance.MAC = m.Provenance.MAC[:10] },
		"non-hex MAC":          func(m *backup.Manifest) { m.Provenance.MAC = strings.Repeat("z", 64) },
		"unknown algorithm":    func(m *backup.Manifest) { m.Provenance.Alg = "none" },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			e := sign(manifestFixture(t, keys))
			edit(e)
			if got := backup.Classify(e, keys, "install-batcave"); got != backup.Foreign {
				t.Fatalf("%s: classified %v, want foreign", name, got)
			}
		})
	}

	t.Run("another live install", func(t *testing.T) {
		if got := backup.Classify(m, keys, "install-arkham"); got != backup.Foreign {
			t.Fatalf("an archive of install-batcave on install-arkham is %v, want foreign", got)
		}
	})
	t.Run("retired or unknown key", func(t *testing.T) {
		if got := backup.Classify(m, newBruceKeys("d2", "d2"), "install-batcave"); got != backup.Foreign {
			t.Fatalf("a manifest whose key is retired is %v, want foreign", got)
		}
	})
	t.Run("another keyring with the same key ID", func(t *testing.T) {
		other := &bruceKeys{active: "d1", keys: map[string][]byte{"d1": []byte("joker-derive")}}
		if got := backup.Classify(m, other, "install-batcave"); got != backup.Foreign {
			t.Fatalf("a manifest made under another keyring is %v, want foreign", got)
		}
	})
	t.Run("planted manifest with its own MAC", func(t *testing.T) {
		// An attacker without the keyring recomputes a MAC with a key of
		// their own choosing: it cannot verify under this install's key.
		forger := &bruceKeys{active: "d1", keys: map[string][]byte{"d1": []byte("riddler-derive")}}
		f := manifestFixture(t, forger)
		if got := backup.Classify(sign(f), keys, "install-batcave"); got != backup.Foreign {
			t.Fatalf("a forged manifest is %v, want foreign", got)
		}
	})
	t.Run("nil inputs", func(t *testing.T) {
		if backup.Classify(nil, keys, "") != backup.Foreign || backup.Classify(m, nil, "") != backup.Foreign {
			t.Fatal("nil manifest or keys must classify foreign")
		}
	})
	t.Run("rotated key still verifies while it is a verify key", func(t *testing.T) {
		rotated := newBruceKeys("d2", "d1", "d2")
		if got := backup.Classify(m, rotated, "install-batcave"); got != backup.Own {
			t.Fatalf("a d1 manifest under a keyring that holds d1 as verify-only is %v, want own", got)
		}
	})
}

func TestParseManifestIsStrict(t *testing.T) {
	keys := newBruceKeys("d1", "d1")
	good, err := backup.MarshalManifest(manifestFixture(t, keys))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backup.ParseManifest(good); err != nil {
		t.Fatalf("a manifest this package wrote does not parse: %v", err)
	}
	str := string(good)
	cases := map[string]string{
		"unknown member (a newer additive field)": strings.Replace(str, `{"format"`, `{"encrypted":true,"format"`, 1),
		"data after the value":                    str + `{}`,
		"wrong format":                            strings.Replace(str, backup.FormatMediacore, "mediacore-backup/2", 1),
		"not JSON":                                `format: mediacore-backup/1`,
		"empty":                                   ``,
		"duplicate entry path":                    strings.Replace(str, `"config/config.boot.json"`, `"db/000001.sst"`, 1),
		"manifest listed as an entry":             strings.Replace(str, `"config/config.boot.json"`, `"`+backup.ManifestName+`"`, 1),
		"short digest":                            strings.Replace(str, hexOf("one"), "abcd", 1),
		"non-hex digest":                          strings.Replace(str, hexOf("one"), strings.Repeat("g", 64), 1),
		"negative size":                           strings.Replace(str, `"size":5`, `"size":-5`, 1),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := backup.ParseManifest([]byte(in)); !errors.Is(err, backup.ErrManifest) {
				t.Fatalf("err = %v, want ErrManifest", err)
			}
		})
	}
	big := make([]byte, backup.MaxManifestBytes+1)
	if _, err := backup.ParseManifest(big); !errors.Is(err, backup.ErrManifest) {
		t.Fatalf("an oversize manifest: %v", err)
	}
}

func TestArchiveIDsAreULIDsThatSort(t *testing.T) {
	t0 := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	a, err := backup.NewArchiveID(t0)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := backup.NewArchiveID(t0.Add(time.Millisecond))
	c, _ := backup.NewArchiveID(t0)
	if len(a) != 26 || a[0] > '7' || !backup.ValidArchiveID(string(a)) {
		t.Fatalf("ID %q is not a ULID", a)
	}
	if !(a < b) {
		t.Fatalf("IDs do not sort by time: %q !< %q", a, b)
	}
	if a == c {
		t.Fatalf("two IDs in one millisecond collide: %q", a)
	}
	for _, bad := range []string{"", "../x", "01JBATMAN0000000000000000a", "legacy-", "legacy-a/b", "legacy-..", strings.Repeat("A", 27)} {
		if backup.ValidArchiveID(bad) {
			t.Errorf("ValidArchiveID(%q) = true", bad)
		}
	}
	if !backup.ValidArchiveID("legacy-audiobooks_pebble_20260101_000000") {
		t.Error("a legacy ID was refused")
	}
}
