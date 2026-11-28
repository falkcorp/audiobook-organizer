// file: internal/books/cutover/m6/effective_operator_test.go
// version: 1.0.0
// guid: c58a2e14-70b3-4f96-8d1e-2a9b4c6f1d03
// last-edited: 2026-11-28

package m6_test

import (
	"context"
	"encoding/json"
	"iter"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai/openai"
	"github.com/falkcorp/mediacore/integrations/download/deluge"
	"github.com/falkcorp/mediacore/integrations/notify/mail"
	"github.com/falkcorp/mediacore/integrations/notify/webhook"

	"github.com/falkcorp/audiobook-organizer/internal/books/adapters/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m6"
)

// The operator URLs these tests supply sit in the reserved 192.0.2.0/24 and
// .invalid ranges, so no fixture holds a real host.
const (
	openAIPath  = config.Path("integrations.ai.openai.url")
	delugePath  = config.Path("integrations.download.deluge.url")
	delugeURL   = "http://192.0.2.99:8112/deluge-web"
	openAIURL   = "https://wayne-llm.invalid:8443/v1"
	delugeField = "url"
)

var (
	openAIRef = integrations.Ref{Kind: integrations.KindAI, Instance: "openai"}
	delugeRef = integrations.Ref{Kind: integrations.KindDownload, Instance: "deluge"}
)

// opVal is one operator-layer value as the config store hands it over: a raw JSON
// value and the layer that supplied it.
type opVal struct {
	path config.Path
	raw  string
	by   string
}

func (o opVal) value() config.OperatorValue {
	return config.OperatorValue{Value: json.RawMessage(o.raw), Src: config.SourceEnv, By: o.by}
}

// quoted is a string operator value as JSON, as an environment binding delivers it.
func quoted(t testing.TB, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// seq is the operator layer as PassthroughValues iterates it: sorted by path.
func seq(vals ...opVal) iter.Seq2[config.Path, config.OperatorValue] {
	slices.SortFunc(vals, func(a, b opVal) int { return strings.Compare(string(a.path), string(b.path)) })
	return func(yield func(config.Path, config.OperatorValue) bool) {
		for _, v := range vals {
			if !yield(v.path, v.value()) {
				return
			}
		}
	}
}

func effective(t testing.TB, values iter.Seq2[config.Path, config.OperatorValue], ref integrations.Ref) map[string]string {
	t.Helper()
	got, err := m6.EffectiveFromPassthrough(values)(context.Background(), ref)
	if err != nil {
		t.Fatalf("Effective(%s): %v", ref, err)
	}
	return got
}

// TestEffectiveFromPassthrough_BothRefs: each URL path answers for its own ref,
// under the settings field the instance stores it in, as supplied and decoded
// from JSON (not the quoted bytes).
func TestEffectiveFromPassthrough_BothRefs(t *testing.T) {
	vals := func() iter.Seq2[config.Path, config.OperatorValue] {
		return seq(
			opVal{openAIPath, quoted(t, openAIURL), "env OPENAI_BASE_URL"},
			opVal{delugePath, quoted(t, delugeURL), "file abo.toml"},
		)
	}
	if got, want := effective(t, vals(), delugeRef), map[string]string{delugeField: delugeURL}; !maps.Equal(got, want) {
		t.Errorf("download/deluge: %v, want %v", got, want)
	}
	if got, want := effective(t, vals(), openAIRef), map[string]string{"url": openAIURL}; !maps.Equal(got, want) {
		t.Errorf("ai/openai: %v, want %v", got, want)
	}
}

// TestEffectiveFromPassthrough_FieldIsThePathsLastSegment: the overlay is keyed
// by the operator path's last segment, as core keys it, and that segment is the
// adapter's settings field, so the openai path is .url. HEAD's old spelling,
// integrations.ai.openai.base_url, is no operator URL any more: the rename table
// no longer sends anything there, and core would drop the unknown field.
func TestEffectiveFromPassthrough_FieldIsThePathsLastSegment(t *testing.T) {
	got := effective(t, seq(opVal{openAIPath, quoted(t, openAIURL), "env OPENAI_BASE_URL"}), openAIRef)
	if !maps.Equal(got, map[string]string{"url": openAIURL}) {
		t.Errorf("ai/openai %v, want the url field alone", got)
	}
	old := seq(opVal{"integrations.ai.openai.base_url", quoted(t, openAIURL), "env OPENAI_BASE_URL"})
	if got := effective(t, old, openAIRef); got != nil {
		t.Errorf("the retired spelling base_url answers %v, want no overlay", got)
	}
}

// TestEffectiveFromPassthrough_ValueIsAsSupplied: the URL is returned as the
// operator wrote it (M6 trims for itself), and a JSON escape is decoded.
func TestEffectiveFromPassthrough_ValueIsAsSupplied(t *testing.T) {
	const padded = "  http://192.0.2.99:8112/a?b=c&d=e  "
	got := effective(t, seq(opVal{delugePath, `"  http://192.0.2.99:8112/a?b=c&d=e  "`, "file abo.toml"}), delugeRef)
	if got[delugeField] != padded {
		t.Errorf("url %q, want %q", got[delugeField], padded)
	}
}

// TestEffectiveFromPassthrough_NoOverlayCells: nothing the builder is given
// answers an empty map or an error; the answer for "no overlay" is nil, which
// Config.Effective documents.
func TestEffectiveFromPassthrough_NoOverlayCells(t *testing.T) {
	var nilSeq iter.Seq2[config.Path, config.OperatorValue]
	cells := map[string]iter.Seq2[config.Path, config.OperatorValue]{
		"nil iterator":   nilSeq,
		"empty iterator": seq(),
		"empty string":   seq(opVal{delugePath, `""`, "env"}, opVal{openAIPath, `""`, "env"}),
		"blank string":   seq(opVal{delugePath, `" \t "`, "env"}, opVal{openAIPath, `"   "`, "env"}),
		"json null":      seq(opVal{delugePath, `null`, "file abo.toml"}, opVal{openAIPath, `null`, "file abo.toml"}),
		"other paths": seq(
			opVal{"integrations.notify.email.host", quoted(t, "smtp.wayne.invalid"), "env"},
			opVal{"integrations.notify.email.port", `"587"`, "env"},
			opVal{"integrations.ai.openai.model", quoted(t, "gpt-batmobile"), "env"},
			opVal{"integrations.download.deluge.password", quoted(t, "alfred"), "env"},
			opVal{"integrations.download.deluge.url.extra", quoted(t, delugeURL), "env"},
			opVal{"core.log.level", quoted(t, "debug"), "env"},
			opVal{"integrations", quoted(t, delugeURL), "env"},
		),
	}
	for name, vals := range cells {
		for _, ref := range []integrations.Ref{delugeRef, openAIRef, {Kind: integrations.KindNotify, Instance: "email"}} {
			got, err := m6.EffectiveFromPassthrough(vals)(context.Background(), ref)
			if err != nil {
				t.Errorf("%s, %s: %v", name, ref, err)
			}
			if got != nil {
				t.Errorf("%s, %s: %v, want a nil map", name, ref, got)
			}
		}
	}
}

// TestEffectiveFromPassthrough_RefWithNoPathIsNil: a ref the operator layer has
// no URL path for answers nil even when another ref's is set, including refs of
// the same kind and a metadata source (whose URL is a stored setting, not an
// operator one).
func TestEffectiveFromPassthrough_RefWithNoPathIsNil(t *testing.T) {
	vals := seq(opVal{delugePath, quoted(t, delugeURL), "env"})
	for _, ref := range []integrations.Ref{
		openAIRef,
		{Kind: integrations.KindAI, Instance: "deluge"},
		{Kind: integrations.KindDownload, Instance: "openai"},
		{Kind: integrations.KindMetadata, Instance: "google-books"},
		{Kind: integrations.KindNotify, Instance: "email"},
	} {
		if got := effective(t, vals, ref); got != nil {
			t.Errorf("%s: %v, want nil", ref, got)
		}
	}
}

// TestEffectiveFromPassthrough_OneRefsEmptyDoesNotHideTheOther: an empty value
// for one ref is no overlay for it and leaves the other's.
func TestEffectiveFromPassthrough_OneRefsEmptyDoesNotHideTheOther(t *testing.T) {
	vals := seq(opVal{openAIPath, `""`, "env"}, opVal{delugePath, quoted(t, delugeURL), "env"})
	if got := effective(t, vals, openAIRef); got != nil {
		t.Errorf("ai/openai: %v, want nil", got)
	}
	if got := effective(t, vals, delugeRef); got[delugeField] != delugeURL {
		t.Errorf("download/deluge: %v, want %s", got, delugeURL)
	}
}

// TestEffectiveFromPassthrough_NonStringIsAnErrorForItsRefOnly: an operator URL
// that is not a JSON string fails the ref it belongs to, naming the path and the
// layer and never the value, and does not fail the other ref.
func TestEffectiveFromPassthrough_NonStringIsAnErrorForItsRefOnly(t *testing.T) {
	for _, raw := range []string{`8112`, `true`, `{"url":"http://192.0.2.99"}`, `["http://192.0.2.99"]`, `not json`} {
		vals := seq(opVal{delugePath, raw, "file gotham.toml"}, opVal{openAIPath, quoted(t, openAIURL), "env OPENAI_BASE_URL"})
		_, err := m6.EffectiveFromPassthrough(vals)(context.Background(), delugeRef)
		if err == nil {
			t.Errorf("%s: no error", raw)
			continue
		}
		for _, want := range []string{string(delugePath), "file gotham.toml"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: error %q does not name %q", raw, err, want)
			}
		}
		if strings.Contains(err.Error(), "192.0.2.99") || strings.Contains(err.Error(), raw) {
			t.Errorf("%s: the error prints the value: %q", raw, err)
		}
		if got := effective(t, vals, openAIRef); got["url"] != openAIURL {
			t.Errorf("%s: ai/openai %v, want %s", raw, got, openAIURL)
		}
	}
}

// TestEffectiveFromPassthrough_SnapshotAndCopy: the values are read when the
// function is built (a single-use iterator is enough, and a later change to the
// operator layer does not move the answer), and a caller that edits the map it
// was handed does not change the next answer.
func TestEffectiveFromPassthrough_SnapshotAndCopy(t *testing.T) {
	layer := []opVal{{delugePath, quoted(t, delugeURL), "env"}}
	used := false
	once := func(yield func(config.Path, config.OperatorValue) bool) {
		if used {
			t.Error("the operator layer was iterated twice")
			return
		}
		used = true
		for _, v := range layer {
			if !yield(v.path, v.value()) {
				return
			}
		}
	}
	eff := m6.EffectiveFromPassthrough(once)
	layer[0].raw = quoted(t, "http://192.0.2.1:1")
	first, err := eff(context.Background(), delugeRef)
	if err != nil || first[delugeField] != delugeURL {
		t.Fatalf("first answer %v, %v", first, err)
	}
	first[delugeField] = "http://192.0.2.2:2"
	first["extra"] = "x"
	second, err := eff(context.Background(), delugeRef)
	if err != nil || !maps.Equal(second, map[string]string{delugeField: delugeURL}) {
		t.Errorf("second answer %v, %v: the first caller's edit leaked", second, err)
	}
}

// TestEffectiveFromPassthrough_RigOverlaySignsTrust drives the builder through the
// step itself, not a unit assertion on its map: the operator's openai base_url
// must re-sign ai/openai's trust from the operator's origin, which only happens if
// the overlay is keyed by the field pending.finish reads (url).
func TestEffectiveFromPassthrough_RigOverlaySignsTrust(t *testing.T) {
	r, s := runFixture(t, func(r *rig) {
		r.effectiveFn = m6.EffectiveFromPassthrough(seq(
			opVal{openAIPath, quoted(t, openAIURL), "env OPENAI_BASE_URL"},
			opVal{delugePath, quoted(t, delugeURL), "env"},
		))
	})
	row := instances(t, s)["ai/openai"]
	const origin = "url=https://wayne-llm.invalid:8443"
	if row["local_origin"] != origin {
		t.Errorf("ai/openai local_origin %v, want %s", row["local_origin"], origin)
	}
	if row["secrets_origin"] != origin {
		t.Errorf("ai/openai secrets_origin %v, want %s", row["secrets_origin"], origin)
	}
	if op, _ := row["operator_secret_origin"].(map[string]any); op["url"] != "https://wayne-llm.invalid:8443" {
		t.Errorf("ai/openai operator_secret_origin %v, want url=https://wayne-llm.invalid:8443", row["operator_secret_origin"])
	}
	if !reportHas(r, "note", "ai/openai", "the effective url is the operator's") {
		t.Errorf("the overlay is not reported:\n%s", reportText(r))
	}
	if got := settingsOf(instances(t, s)["download/deluge"])["url"]; got != delugeURL {
		t.Errorf("deluge url %v, want the operator's %s", got, delugeURL)
	}
}

// TestEffectiveFromPassthrough_RigErrorFailsTheBoot: a non-string operator URL
// reaches the step as Effective's error and fails it.
func TestEffectiveFromPassthrough_RigErrorFailsTheBoot(t *testing.T) {
	r := newRig(t, withRows(headRows(t)), func(r *rig) {
		r.effectiveFn = m6.EffectiveFromPassthrough(seq(opVal{delugePath, `8112`, "file gotham.toml"}))
	})
	if _, err := r.run(t); err == nil || !strings.Contains(err.Error(), "effective destinations") || !strings.Contains(err.Error(), string(delugePath)) {
		t.Fatalf("an operator URL that is not a string gave %v, want the boot to fail naming the effective destinations and the path", err)
	}
}

// TestOperatorURLPaths_AccountForEveryPassthroughRow pins the builder's tables to
// the rename table: every integrations passthrough path the table sends a retired
// name to (and every env binding) is either mapped to an instance field or listed
// as deliberately not a URL setting. A new row, such as a second destination,
// fails here until someone decides which it is.
func TestOperatorURLPaths_AccountForEveryPassthroughRow(t *testing.T) {
	secret := map[string]bool{}
	var paths []string
	for _, row := range cutover.Table().Rows {
		for _, tg := range row.Targets {
			switch {
			case tg.Kind == cutover.TargetSecret && tg.Passthrough:
				// A credential is never an overlay URL.
				secret[tg.Path] = true
			case tg.Kind == cutover.TargetPassthrough:
				paths = append(paths, tg.Path)
			}
		}
	}
	for _, b := range cutover.ConfigEnvBindings() {
		if !secret[string(b.Path)] {
			paths = append(paths, string(b.Path))
		}
	}
	if len(paths) == 0 {
		t.Fatal("the rename table has no integrations passthrough paths: the walk found nothing to check")
	}
	for _, p := range paths {
		if !strings.HasPrefix(p, "integrations.") {
			t.Errorf("passthrough path %q is outside integrations", p)
		}
		if !m6.AccountedFor(config.Path(p)) {
			t.Errorf("passthrough path %s is neither a mapped operator URL nor listed as not a URL setting", p)
		}
	}
	// The check can fail: a path no table names is not accounted for.
	for _, p := range []config.Path{"integrations.download.deluge.base_url", "integrations.ai.openai.base_url", "integrations.notify.email.tls_url"} {
		if m6.AccountedFor(p) {
			t.Errorf("%s is accounted for though no table names it", p)
		}
	}
}

// TestOperatorURLPaths_AreRealPassthroughPaths: both tables name only paths core
// config will accept as passthrough, so a typo in either is not a silent dead
// row, and every mapped path lands on the ref and field the tests expect.
func TestOperatorURLPaths_AreRealPassthroughPaths(t *testing.T) {
	all := m6.URLPathsAndNotURLPaths()
	if len(all) < 6 {
		t.Fatalf("%d paths in the two tables, want the 2 mapped and 4 not-URL ones", len(all))
	}
	for _, p := range all {
		if !cutover.ConfigPassthrough(p) {
			t.Errorf("%s is not a passthrough path", p)
		}
	}
	for p, want := range map[config.Path][2]string{
		openAIPath: {openAIRef.String(), "url"},
		delugePath: {delugeRef.String(), delugeField},
	} {
		ref, field, ok := m6.OperatorField(p)
		if !ok || ref != want[0] || field != want[1] {
			t.Errorf("%s maps to %s/%s (%v), want %s/%s", p, ref, field, ok, want[0], want[1])
		}
	}
}

// jsonFields is the JSON field names of a settings struct, embedded structs
// flattened as encoding/json does, each with its schema tag.
func jsonFields(t reflect.Type) map[string]string {
	out := map[string]string{}
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct {
			maps.Copy(out, jsonFields(f.Type))
			continue
		}
		if name == "" || name == "-" {
			continue
		}
		out[name] = f.Tag.Get("schema")
	}
	return out
}

// adapterFields is the json fields of every adapter Settings an integrations
// passthrough path can name, by "<kind>/<instance>": the reflected Settings of the
// openai, deluge, email and webhook adapters, and the schema properties of each
// books metadata adapter's descriptor (read from the descriptors, so a new source
// is a new row here and a renamed field is a failure).
func adapterFields(t *testing.T) map[string]map[string]string {
	t.Helper()
	out := map[string]map[string]string{
		openAIRef.String():   jsonFields(reflect.TypeFor[openai.Settings]()),
		delugeRef.String():   jsonFields(reflect.TypeFor[deluge.Settings]()),
		"notify/email":       jsonFields(reflect.TypeFor[mail.Settings]()),
		"notify/webhook-<n>": jsonFields(reflect.TypeFor[webhook.Settings]()),
	}
	ds, err := metadata.Descriptors(metadata.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		m := d.Meta()
		id, ok := strings.CutPrefix(string(m.ID), "books.")
		if !ok {
			t.Fatalf("adapter %s is not a books adapter", m.ID)
		}
		var doc struct {
			Properties map[string]map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(m.Settings.JSON(), &doc); err != nil {
			t.Fatalf("%s settings schema: %v", id, err)
		}
		fields := map[string]string{}
		for name, prop := range doc.Properties {
			fields[name] = ""
			if f, _ := prop["format"].(string); f == "uri" {
				fields[name] = "format=uri"
			}
		}
		out["metadata/"+id] = fields
	}
	return out
}

// TestOperatorURLPaths_FieldsAreAdapterSettings pins the invariant the overlay
// rests on: core keys an operator value by the path's last segment, so EVERY
// integrations passthrough path the rename table and the env bindings name has a
// last segment that is a json field of its adapter's Settings, and none is
// skipped: a path whose adapter this test cannot resolve fails (an adapter that is
// not written yet must declare the field before its path lands, which is what
// stops a hardcover api_token with no Settings field being dropped silently); and
// the mapped URL paths are exactly the URL-typed (schema format=uri) fields of the
// openai and deluge Settings, no more and no fewer. The old base_url spelling
// fails the first check and is why openai's path is .url.
func TestOperatorURLPaths_FieldsAreAdapterSettings(t *testing.T) {
	all := adapterFields(t)
	settings := map[string]map[string]string{
		openAIRef.String(): all[openAIRef.String()],
		delugeRef.String(): all[delugeRef.String()],
	}
	if _, ok := settings[openAIRef.String()]["url"]; !ok || len(settings[delugeRef.String()]) != 3 {
		t.Fatalf("the reflected Settings look wrong: %v", settings)
	}
	var paths []config.Path
	for _, row := range cutover.Table().Rows {
		for _, tg := range row.Targets {
			if tg.Kind == cutover.TargetPassthrough || tg.Kind == cutover.TargetSecret && tg.Passthrough {
				paths = append(paths, config.Path(tg.Path))
			}
		}
	}
	for _, b := range cutover.ConfigEnvBindings() {
		paths = append(paths, b.Path)
	}
	checked := 0
	for _, p := range paths {
		if !strings.HasPrefix(string(p), "integrations.") {
			continue
		}
		ref, field, ok := m6.OperatorField(p)
		if !ok {
			t.Errorf("%s is an integrations passthrough path that is not <kind>.<instance>.<field>: core cannot key it", p)
			continue
		}
		fields, known := all[ref]
		if !known {
			t.Errorf("%s: no adapter Settings known for %s (add the adapter to adapterFields, or the path is one no adapter reads)", p, ref)
			continue
		}
		checked++
		if _, isField := fields[field]; !isField {
			t.Errorf("%s: %q is not a json field of %s's Settings %v", p, field, ref, slices.Sorted(maps.Keys(fields)))
		}
	}
	if checked < 12 {
		t.Fatalf("only %d passthrough paths were checked against a Settings type: the walk found nothing", checked)
	}
	mapped := map[string]bool{}
	for _, p := range m6.URLPaths() {
		ref, field, _ := m6.OperatorField(p)
		if tag := settings[ref][field]; !strings.Contains(tag, "format=uri") {
			t.Errorf("%s is mapped as an operator URL but %s.%s is not a URL-typed Settings field (schema %q)", p, ref, field, tag)
		}
		mapped[ref+"."+field] = true
	}
	for ref, fields := range settings {
		for field, tag := range fields {
			if strings.Contains(tag, "format=uri") && !mapped[ref+"."+field] {
				t.Errorf("%s.%s is a URL-typed Settings field with no operator URL mapping", ref, field)
			}
		}
	}
}
