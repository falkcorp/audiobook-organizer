// file: core/backup/reason_test.go
// version: 1.0.1
// guid: 9fc71041-93b7-46d4-9a79-99298c8e2c54
// last-edited: 2026-11-12

package backup_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/backup"
)

// batName is a text of n characters that take three bytes each.
func batName(n int) string { return strings.Repeat("蝙", n) }

// A reason is signed into the manifest and read back by ParseManifest with a
// bound. Create refuses one the read side would refuse before it takes the
// resource, makes a directory or touches the store: an archive that was written
// and reported made but that its own catalogue cannot list is the failure this
// closes.
func TestCreateRefusesAReasonTheCatalogueCouldNotRead(t *testing.T) {
	cases := map[string]string{
		"257 bytes":                 strings.Repeat("x", 257),
		"a CJK text of 300 bytes":   batName(100),
		"a CR and LF":               "pre-update\r\nERROR forged by the reason",
		"a NUL":                     "pre\x00update",
		"a C1 control character":    "pre\u0085update",
		"text that is not UTF-8":    "pre\xffupdate",
		"a tab":                     "pre\tupdate",
		"257 bytes of CJK and more": batName(85) + "ab", // 85 characters are 255 bytes, plus two
	}
	for name, reason := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			_, err := e.svc.Create(context.Background(), backup.CreateOptions{Reason: reason})
			if !errors.Is(err, backup.ErrInvalidReason) {
				t.Fatalf("Create = %v, want ErrInvalidReason", err)
			}
			if strings.Contains(err.Error(), "ERROR forged") || strings.Contains(err.Error(), "蝙") {
				t.Errorf("the error quotes the reason: %q", err)
			}
			if names := e.names(); len(names) != 0 {
				t.Errorf("a refused create left %v in the backup directory", names)
			}
			if _, err := os.Stat(filepath.Join(e.dbParent, ".backup-staging")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a refused create made the staging root (%v)", err)
			}
			if got := e.log.list(); len(got) != 0 {
				t.Errorf("a refused create reached the store or the journal: %v", got)
			}
			// Nothing was taken: the next create, with a good reason, is not busy.
			if _, err := e.svc.Create(context.Background(), backup.CreateOptions{Reason: "manual"}); err != nil {
				t.Fatalf("Create after the refusal: %v", err)
			}
		})
	}
}

// The bound is bytes: 256 are accepted and listed, and the catalogue reads the
// reason back whole.
func TestCreateKeepsAReasonOfTheLongestLength(t *testing.T) {
	e := newEnv(t)
	for _, reason := range []string{strings.Repeat("x", backup.MaxReasonBytes), batName(85)} {
		a := e.create(reason)
		if a.Verified == nil || !a.Verified.OK {
			t.Fatalf("verified = %+v", a.Verified)
		}
		got, err := e.svc.List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, l := range got {
			if l.ID == a.ID {
				found = true
				if l.Manifest == nil || l.Manifest.Reason != reason {
					t.Errorf("the catalogue does not list the archive with its %d-byte reason whole", len(reason))
				}
			}
		}
		if !found {
			t.Fatalf("a created archive with a %d-byte reason is not in the catalogue", len(reason))
		}
	}
}

// ValidReason and ParseManifest apply one rule to the same member. A copy of the
// rule that drifted from the reader's is how the first of them was missed.
func TestValidReasonIsTheRuleParseManifestApplies(t *testing.T) {
	keys := newBruceKeys("d1", "d1")
	for _, reason := range []string{
		"", "manual", "schedule", "pre:books.organize", strings.Repeat("x", 256), strings.Repeat("x", 257),
		batName(85), batName(86), "a\nb", "a\rb", "a\tb", "a\x7fb", "a\u0085b", "a b", "snowman ☃",
	} {
		m := manifestFixture(t, keys)
		m.Reason = reason
		b, err := backup.MarshalManifest(m)
		if err != nil {
			t.Fatal(err)
		}
		_, perr := backup.ParseManifest(b)
		verr := backup.ValidReason(reason)
		if (perr == nil) != (verr == nil) {
			t.Errorf("reason %q (%d bytes): ParseManifest = %v, ValidReason = %v", reason, len(reason), perr, verr)
		}
		if verr != nil && !errors.Is(verr, backup.ErrInvalidReason) {
			t.Errorf("ValidReason(%q) = %v, want ErrInvalidReason", reason, verr)
		}
	}
}

// The options Create writes into every manifest are judged when the service is
// made, with the reader's test, and the error names the option and not its value.
func TestNewRefusesOptionsTheManifestCouldNotCarry(t *testing.T) {
	e := newEnv(t)
	long := func(canary string) string { return canary + strings.Repeat("x", backup.MaxReasonBytes+1-len(canary)) } // 257 bytes
	cases := []struct {
		name, field, canary string
		mutate              func(o *backup.Options, v string)
		value               string
	}{
		{"AppVersion over 256 bytes", "AppVersion", "JOKER-CANARY", func(o *backup.Options, v string) { o.AppVersion = v }, long("JOKER-CANARY")},
		{"CoreVersion with a line break", "CoreVersion", "BANE-CANARY", func(o *backup.Options, v string) { o.CoreVersion = v }, "BANE-CANARY\nforged"},
		{"AppID with a CR", "AppID", "RIDDLER-CANARY", func(o *backup.Options, v string) { o.AppID = v }, "RIDDLER-CANARY\r"},
		{"a module name that is too long", "Modules", "PENGUIN-CANARY", func(o *backup.Options, v string) { o.Modules = map[string]string{v: "1.0.0"} }, long("PENGUIN-CANARY")},
		{"a module version with a control character", "Modules", "SCARECROW-CANARY", func(o *backup.Options, v string) { o.Modules = map[string]string{"books": v} }, "SCARECROW-CANARY\x00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := e.opts
			c.mutate(&o, c.value)
			_, err := backup.New(o)
			if err == nil {
				t.Fatal("New accepted the option")
			}
			if !strings.Contains(err.Error(), c.field) {
				t.Errorf("the error does not name %s: %v", c.field, err)
			}
			if strings.Contains(err.Error(), c.canary) {
				t.Errorf("the error quotes the value: %q", err)
			}
		})
	}
	// The same options at the bound, and empty versions, are fine.
	o := e.opts
	o.AppVersion, o.CoreVersion = strings.Repeat("v", 256), ""
	if _, err := backup.New(o); err != nil {
		t.Fatalf("New with a 256-byte version and an empty one: %v", err)
	}
}

// A manifest member that does not come from the caller of Create (here the
// journal's nonce) is bounded on the way in by ParseManifest and, before this
// check, nowhere on the way out. Create now refuses to write an archive its own
// reader would refuse, after the work but before the archive: nothing is left in
// the backup directory and the run is a failure, not a success nobody can list.
func TestCreateRefusesAManifestItsOwnCatalogueCannotRead(t *testing.T) {
	e := newEnv(t)
	e.markers.sb.Nonce = strings.Repeat("n", 300)
	_, err := e.svc.Create(context.Background(), backup.CreateOptions{Reason: "manual"})
	if !errors.Is(err, backup.ErrManifest) {
		t.Fatalf("Create = %v, want ErrManifest", err)
	}
	if names := e.names(); len(names) != 0 {
		t.Errorf("a create that could not read its own manifest left %v", names)
	}
	if ents, _ := os.ReadDir(filepath.Join(e.dbParent, ".backup-staging")); len(ents) != 0 {
		t.Errorf("it left %d staging entries", len(ents))
	}
	// With a nonce that fits, the same service creates.
	e.markers.sb.Nonce = "nonce-d"
	e.create("manual")
}

// A re-signed archive is held to the same test: a key ID the reader would refuse
// (here one over the bound, as the keyring's new active key) makes the rewrap
// fail before the new archive is streamed and published, and the original stays.
// The verification that follows a rewrap would refuse such an archive too, but
// only after every byte of it was written and named; the link is the witness
// that it never got that far.
func TestRewrapRefusesAManifestItsOwnCatalogueCannotRead(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	longID := strings.Repeat("k", 300)
	e.keys.bruceKeys = newBruceKeys("d1", "d1", longID)
	e.opts.Keyring, e.opts.Inspector = e.keys, &backup.StorageInspector{Base: storageBase(e), Admin: e.st.DB().Admin(), Secrets: e.keys, SecretsOwner: gothamOwner}
	e.rebuild()
	old := e.create("manual")
	e.keys.active = longID
	var links int
	e.opts.Disk = backup.Disk{Link: func(oldname, newname string) error { links++; return os.Link(oldname, newname) }}
	e.rebuild()
	_, err := e.svc.Rewrap(ctx, old.ID)
	if !errors.Is(err, backup.ErrManifest) {
		t.Fatalf("Rewrap = %v, want ErrManifest", err)
	}
	if links != 0 {
		t.Errorf("the refused manifest was streamed and given a name (%d link calls)", links)
	}
	if err != nil && strings.Contains(err.Error(), longID) {
		t.Errorf("the error quotes the key ID")
	}
	list, lerr := e.svc.List(ctx)
	if lerr != nil || len(list) != 1 || list[0].ID != old.ID {
		t.Fatalf("catalogue = %+v, %v: the original must stay and nothing else be written", list, lerr)
	}
	if l := leftovers(e); len(l) != 0 {
		t.Fatalf("leftover %v", l)
	}
}
