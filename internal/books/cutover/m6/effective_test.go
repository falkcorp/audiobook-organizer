// file: internal/books/cutover/m6/effective_test.go
// version: 1.0.0
// guid: 7c1e4a90-3b52-4d68-8f0a-5e92b1c7d304
// last-edited: 2026-11-19

package m6_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m6"
)

// overlayURL is an operator-supplied URL: the reserved 192.0.2.0/24 range, so no
// fixture holds a real host.
const overlayURL = "http://192.0.2.99:8112/deluge-web"

func withOverlay(ref string, fields map[string]string) func(*rig) {
	return func(r *rig) {
		if r.effective == nil {
			r.effective = map[string]map[string]string{}
		}
		r.effective[ref] = fields
	}
}

// reportText is every report entry as one string, for asserting what a report
// never contains.
func reportText(r *rig) string {
	var b strings.Builder
	for _, l := range reportLines(r.report) {
		b.WriteString(l + "\n")
	}
	return b.String()
}

// TestEffective_DelugeURLIsTheOperators: HEAD's Deluge web URL is environment or
// config-file authoritative over the stored blob (internal/config
// applyEnvAuthoritativeConfig), so the migrated instance is the operator's URL
// and its trust is signed from that origin, not from the blob's stale one. The
// report says the stored value is superseded without printing either URL.
func TestEffective_DelugeURLIsTheOperators(t *testing.T) {
	r, s := runFixture(t, withOverlay("download/deluge", map[string]string{"url": overlayURL}))
	row := instances(t, s)["download/deluge"]
	if got := settingsOf(row)["url"]; got != overlayURL {
		t.Errorf("deluge url %v, want the operator's %s", got, overlayURL)
	}
	const origin = "url=http://192.0.2.99:8112"
	if row["local_origin"] != origin || row["secrets_origin"] != origin {
		t.Errorf("trust signed from %v / %v, want %s", row["local_origin"], row["secrets_origin"], origin)
	}
	text := reportText(r)
	// An origin (scheme, host, port) is reported with the trust it grants; a URL's
	// path never is.
	for _, leaked := range []string{"deluge-web", "192.0.2.20"} {
		if strings.Contains(text, leaked) {
			t.Errorf("the report prints a URL or the superseded stored host (%s):\n%s", leaked, text)
		}
	}
	if !strings.Contains(text, "superseded by the operator's") {
		t.Errorf("the superseded stored URL is not reported:\n%s", text)
	}
}

// TestEffective_DelugeStoredEmptyOperatorWins: the operator's URL wins over a
// stored empty value too, which is the case a blob-only reading loses (no
// instance is built for an empty blob URL).
func TestEffective_DelugeStoredEmptyOperatorWins(t *testing.T) {
	_, s := runFixture(t,
		withBlob(func(tree map[string]any) { tree["deluge_web_url"] = "" }),
		withOverlay("download/deluge", map[string]string{"url": overlayURL}))
	row, ok := instances(t, s)["download/deluge"]
	if !ok {
		t.Fatal("no download/deluge instance though the operator supplies the URL")
	}
	if got := settingsOf(row)["url"]; got != overlayURL {
		t.Errorf("deluge url %v, want %s", got, overlayURL)
	}
}

// TestEffective_DelugeNoURLAnywhereIsReported: neither the blob nor the operator
// has a URL, so there is no instance, and the report says so rather than being
// silent about a missing download client.
func TestEffective_DelugeNoURLAnywhereIsReported(t *testing.T) {
	r, s := runFixture(t, withBlob(func(tree map[string]any) { tree["deluge_web_url"] = "" }))
	if _, ok := instances(t, s)["download/deluge"]; ok {
		t.Error("a download/deluge instance was built with no URL")
	}
	if !strings.Contains(reportText(r), "no download/deluge instance is created") {
		t.Errorf("the missing URL is not reported:\n%s", reportText(r))
	}
}

// TestEffective_OverlayEqualToStoredChangesNothing: an operator URL that is the
// stored one is no overlay: the rows and the report are the fixture's.
func TestEffective_OverlayEqualToStoredChangesNothing(t *testing.T) {
	base, bs := runFixture(t)
	r, s := runFixture(t, withOverlay("download/deluge", map[string]string{"url": "http://192.0.2.20:8112"}))
	if d := diff(reportLines(r.report), reportLines(base.report)); d != "" {
		t.Errorf("an overlay equal to the stored URL changed the report:\n%s", d)
	}
	if string(mustJSON(t, instances(t, s))) != string(mustJSON(t, instances(t, bs))) {
		t.Error("an overlay equal to the stored URL changed the instances")
	}
}

// TestEffective_OverlaySignsTrustForAStoredURL: an operator URL over a metadata
// source's stored base_url (the stored value is kept as the instance's setting)
// re-signs Local, the secrets origin and the operator-secret origin from the
// effective origin, the signature core compares at the first boot
// (seams.d/F7-01 rows 17 and 22).
func TestEffective_OverlaySignsTrustForAStoredURL(t *testing.T) {
	r, s := runFixture(t, withOverlay("metadata/google-books", map[string]string{"base_url": "https://env.invalid:8443/v1"}))
	row := instances(t, s)["metadata/google-books"]
	const origin = "base_url=https://env.invalid:8443"
	if row["local_origin"] != origin {
		t.Errorf("local_origin %v, want %s", row["local_origin"], origin)
	}
	if row["secrets_origin"] != origin {
		t.Errorf("secrets_origin %v, want %s", row["secrets_origin"], origin)
	}
	op, _ := row["operator_secret_origin"].(map[string]any)
	if op["base_url"] != "https://env.invalid:8443" {
		t.Errorf("operator_secret_origin %v, want base_url=https://env.invalid:8443", row["operator_secret_origin"])
	}
	if got := settingsOf(row)["base_url"]; got != "https://books.invalid" {
		t.Errorf("the stored base_url %v was rewritten", got)
	}
	if strings.Contains(reportText(r), "/v1") {
		t.Errorf("the report prints the operator's URL path:\n%s", reportText(r))
	}
	if !strings.Contains(reportText(r), "the effective base_url is the operator's") {
		t.Errorf("the overlay is not reported:\n%s", reportText(r))
	}
}

// TestEffective_OverlayThatIsNoOriginGrantsNoTrustForThatField: a value that does
// not parse as an absolute URL has no origin to trust, so the field drops out of
// the signature instead of signing the stored origin the instance will not use.
func TestEffective_OverlayThatIsNoOriginGrantsNoTrustForThatField(t *testing.T) {
	_, s := runFixture(t, withOverlay("metadata/google-books", map[string]string{"base_url": "not a url"}))
	row := instances(t, s)["metadata/google-books"]
	if g, _ := row["local"].(bool); g {
		t.Errorf("trust was granted for an overlay with no origin: %v", row["local_origin"])
	}
}

// TestEffective_ErrorFailsTheBoot: a failing overlay lookup is a boot failure, not
// a silent fall back to the stored URL, which would sign trust for a destination
// the operator replaced.
func TestEffective_ErrorFailsTheBoot(t *testing.T) {
	r := newRig(t, withRows(headRows(t)), func(r *rig) { r.effectiveErr = errors.New("the config file is unreadable") })
	if _, err := r.run(t); err == nil || !strings.Contains(err.Error(), "effective destinations") {
		t.Fatalf("a failing Effective gave %v, want the boot to fail naming it", err)
	}
}

// TestEffective_ErrorFailsTheBootWithNoInstance: Deluge asks for the overlay even
// when the blob names no instance of any kind, and its failure is the boot's too,
// not "no URL, no instance".
func TestEffective_ErrorFailsTheBootWithNoInstance(t *testing.T) {
	r := newRig(t, withRows(map[string][]byte{m6.KeyBlob: blobRow(t, map[string]any{"root_dir": rootDir})}),
		// No stored OpenAI key either, so no ai/openai row is synthesised.
		func(r *rig) { r.effectiveErr = errors.New("the config file is unreadable"); clear(r.secrets.seed) })
	if _, err := r.run(t); err == nil || !strings.Contains(err.Error(), "effective destinations") {
		t.Fatalf("a failing Effective gave %v, want the boot to fail naming it", err)
	}
}
