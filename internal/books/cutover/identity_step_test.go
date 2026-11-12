// file: internal/books/cutover/identity_step_test.go
// version: 1.0.0
// guid: f72f0d52-8bc1-480f-ad40-ff6c5a2c7089
// last-edited: 2026-11-12

package cutover

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// TestIdentityStep_Declaration: M3 is declared the way a one-way, boot-blocking,
// in-place cutover step must be (03 §3.8 row M3; 05-storage §5S.3.13): the
// identity source ranges, after books' level step, raising books to the
// cutover level, sequential with its reason, sensitive on the credential
// families, and carrying the preflight and its two operator actions.
func TestIdentityStep_Declaration(t *testing.T) {
	r := newIDRig(t, "stage1")
	dm := r.migration()
	if dm.ID != "books.identity-cutover" || IdentityCutoverID != "books.identity-cutover" || dm.Owner != BooksOwner {
		t.Errorf("identity %s/%s", dm.Owner, dm.ID)
	}
	if dm.Mode != datamigrate.BootBlocking {
		t.Errorf("mode %v: the identity cutover blocks the boot", dm.Mode)
	}
	if !dm.Cutover || !dm.RewritesInPlace || dm.RaisesLevel != BooksCutoverLevel || dm.Compat != storage.Breaking {
		t.Errorf("cutover %v rewrites-in-place %v raises-level %d compat %v", dm.Cutover, dm.RewritesInPlace, dm.RaisesLevel, dm.Compat)
	}
	if want := []storage.StepRef{BooksLevelStepRef(r.levelVersion())}; !slices.Equal(dm.After, want) {
		t.Errorf("After = %v, want %v", dm.After, want)
	}
	if got := dm.Parallelism.String(); !strings.HasPrefix(got, "sequential: ") || len(got) < len("sequential: ")+20 || dm.Parallelism.Workers() != 1 {
		t.Errorf("parallelism %q (%d workers): M3 is sequential, with the reason", got, dm.Parallelism.Workers())
	}
	if want := m3.SensitivePrefixes(); !slices.Equal(dm.Sensitive, want) {
		t.Errorf("Sensitive = %v, want %v", dm.Sensitive, want)
	}
	// inviteallow: rows hold the raw invite token their accept recorded, and a
	// quarantined one keeps the row as it was.
	for _, p := range []string{"u:", "apikey:", "oauthid:", "inviteallow:"} {
		if !slices.Contains(dm.Sensitive, p) {
			t.Errorf("%s holds credentials and is not Sensitive", p)
		}
	}
	if dm.Step == nil || dm.Verify == nil || dm.Preflight == nil || dm.FixBlocker == nil || dm.DeleteBlocker == nil {
		t.Errorf("a callback is missing: step %v verify %v preflight %v fix %v delete %v",
			dm.Step != nil, dm.Verify != nil, dm.Preflight != nil, dm.FixBlocker != nil, dm.DeleteBlocker != nil)
	}

	// Source: every identity prefix once, then the two exact bootstrap rows,
	// sorted and disjoint, each range holding exactly what it names.
	src := dm.Source
	if want := len(m3.SourcePrefixes()) + len(m3.SourceKeys()); len(src) != want {
		t.Fatalf("Source has %d ranges, want %d", len(src), want)
	}
	for i := 1; i < len(src); i++ {
		if bytes.Compare(src[i-1].End, src[i].Start) > 0 || bytes.Compare(src[i-1].Start, src[i].Start) >= 0 {
			t.Errorf("Source ranges %q and %q are not sorted and disjoint", src[i-1].Start, src[i].Start)
		}
	}
	covers := func(key string) bool {
		for _, rg := range src {
			if bytes.Compare([]byte(key), rg.Start) >= 0 && bytes.Compare([]byte(key), rg.End) < 0 {
				return true
			}
		}
		return false
	}
	for _, p := range m3.SourcePrefixes() {
		if !covers(p) || !covers(p+"\xff\xff") {
			t.Errorf("Source does not cover the whole prefix %s", p)
		}
	}
	for _, k := range m3.SourceKeys() {
		if !covers(k) || covers(k+"x") || covers(k[:len(k)-1]) {
			t.Errorf("Source must cover exactly the key %s", k)
		}
	}
	// What is not identity's is outside: another idx: family, the other settings rows, the journals.
	for _, k := range []string{"idx:book:title:x", "idx:apikey", "setting:config_blob", "setting:log_level", m3.JournalPrefix + "j", expiryStampRow, "book:1"} {
		if covers(k) {
			t.Errorf("Source covers %s, which is not identity's", k)
		}
	}
}

// expiryStampRow is HEAD's one-shot API-key expiry flag: outside Source,
// deleted by the step (ownership table row 16).
const expiryStampRow = "setting:system:migration:apikey_expiry_stamp_v1_done"

// TestIdentityStep_RefusesWhatItCannotRun: the constructor refuses a level, a
// predecessor or an ID that would register the step in the wrong place, and a
// config the step cannot run on.
func TestIdentityStep_RefusesWhatItCannotRun(t *testing.T) {
	r := newIDRig(t, "stage1")
	var good m3.Config
	r.config = func(c *m3.Config) { good = *c }
	_ = r.migration()
	after := BooksLevelStepRef(r.levelVersion())

	if _, err := IdentityStep(good, BooksCutoverLevel, after); err != nil {
		t.Fatalf("the rig's own config: %v", err)
	}
	withID := good
	withID.StepID = IdentityCutoverID
	if _, err := IdentityStep(withID, BooksCutoverLevel, after); err != nil {
		t.Errorf("the step's own ID in the config is accepted: %v", err)
	}
	for name, call := range map[string]func() error{
		"no level to raise to": func() error { _, err := IdentityStep(good, 0, after); return err },
		"a negative level":     func() error { _, err := IdentityStep(good, -1, after); return err },
		"no predecessor":       func() error { _, err := IdentityStep(good, BooksCutoverLevel, storage.StepRef{}); return err },
		"another owner's step": func() error {
			_, err := IdentityStep(good, BooksCutoverLevel, storage.StepRef{Owner: "core/auth", Version: 3})
			return err
		},
		"a predecessor with no version": func() error {
			_, err := IdentityStep(good, BooksCutoverLevel, storage.StepRef{Owner: BooksOwner})
			return err
		},
		"another step's ID": func() error {
			c := good
			c.StepID = "books.config-cutover"
			_, err := IdentityStep(c, BooksCutoverLevel, after)
			return err
		},
		"an incomplete config": func() error {
			c := good
			c.Atoms = nil
			_, err := IdentityStep(c, BooksCutoverLevel, after)
			return err
		},
	} {
		if err := call(); err == nil {
			t.Errorf("%s: IdentityStep accepted it", name)
		}
	}
}
