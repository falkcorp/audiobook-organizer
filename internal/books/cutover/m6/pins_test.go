// file: internal/books/cutover/m6/pins_test.go
// version: 1.0.2
// guid: 21b7d4e8-0c95-4a36-8f1d-7e5a39c0b264
// last-edited: 2026-11-19

package m6

import (
	"encoding"
	"encoding/json"
	"maps"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
	"github.com/falkcorp/audiobook-organizer/internal/books/adapters/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/metadata/providerhttp"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/storage"
)

// The tables M6 copies from HEAD and from the spec are pinned here against their
// sources, so a change to either is a failing test and not a silent drift. HEAD's
// own packages are imported by the test only: the step reads none of them.

// TestMetadataLimits_BuiltinTableMatchesProviderhttp pins builtinLimits and
// fallbackLimits to providerhttp's defaults, and the tier scaling to config's.
func TestMetadataLimits_BuiltinTableMatchesProviderhttp(t *testing.T) {
	for _, name := range providerhttp.KnownProviders() {
		want := providerhttp.BuiltinLimitsFor(name)
		got, ok := builtinLimits[name]
		if !ok {
			t.Errorf("providerhttp has a budget for %q that M6's table lacks", name)
			continue
		}
		if got.RPS != want.RPS || got.Burst != want.Burst || got.MaxRetries != want.MaxRetries || got.Timeout != want.Timeout {
			t.Errorf("%s: M6 has %+v, providerhttp has %+v", name, got, want)
		}
	}
	for name := range builtinLimits {
		if !slices.Contains(providerhttp.KnownProviders(), name) {
			t.Errorf("M6's table names %q, which providerhttp has no budget for", name)
		}
	}
	fb := providerhttp.BuiltinLimitsFor("a-provider-nobody-registered")
	if fallbackLimits.RPS != fb.RPS || fallbackLimits.Burst != fb.Burst || fallbackLimits.MaxRetries != fb.MaxRetries || fallbackLimits.Timeout != fb.Timeout {
		t.Errorf("fallback: M6 has %+v, providerhttp has %+v", fallbackLimits, fb)
	}

	// A tier scales RPS and burst the way config's Multiplier says, for every
	// tier and for an unknown one (which scales nothing).
	for _, tier := range []config.RateLimitTier{config.RateLimitTierLow, config.RateLimitTierMedium, config.RateLimitTierHigh, "turbo", ""} {
		src := headMetadataSource{}
		src.RateLimit.Tier = string(tier)
		l := metadataLimits("hardcover", src)
		base := providerhttp.BuiltinLimitsFor("hardcover")
		if tier == "" {
			if l != nil {
				t.Errorf("an empty rate_limit became %+v, want none", l)
			}
			continue
		}
		if want := base.RPS * tier.Multiplier(); l == nil || l.RPS != want {
			t.Errorf("tier %q: RPS %v, want %v", tier, l, want)
		}
	}
	// Burst follows the tier by the same rounding, for a provider whose burst is
	// above one and for one whose burst is one, and an unlisted provider uses the
	// fallback's.
	for _, id := range []string{"google-books", "hardcover", "a-provider-nobody-registered"} {
		base := providerhttp.BuiltinLimitsFor(id)
		for _, tier := range []config.RateLimitTier{config.RateLimitTierLow, config.RateLimitTierMedium, config.RateLimitTierHigh} {
			src := headMetadataSource{}
			src.RateLimit.Tier = string(tier)
			l := metadataLimits(id, src)
			want := base.Burst
			if tier != config.RateLimitTierMedium {
				if scaled := int(math.Round(float64(base.Burst) * tier.Multiplier())); scaled >= 1 {
					want = scaled
				}
			}
			if l == nil || l.Burst != want || l.MaxRetries != base.MaxRetries || l.Timeout != base.Timeout {
				t.Errorf("%s at tier %s: %+v, want burst %d, retries %d, timeout %v", id, tier, l, want, base.MaxRetries, base.Timeout)
			}
		}
	}
	// Each explicit field alone replaces only itself.
	base := providerhttp.BuiltinLimitsFor("google-books")
	for name, c := range map[string]struct {
		set  func(src *headMetadataSource)
		want integrations.Limits
	}{
		"rps":     {func(s *headMetadataSource) { s.RateLimit.RPS = 1.5 }, integrations.Limits{RPS: 1.5, Burst: base.Burst, MaxRetries: base.MaxRetries, Timeout: base.Timeout}},
		"burst":   {func(s *headMetadataSource) { s.RateLimit.Burst = 7 }, integrations.Limits{RPS: base.RPS, Burst: 7, MaxRetries: base.MaxRetries, Timeout: base.Timeout}},
		"retries": {func(s *headMetadataSource) { s.RateLimit.MaxRetries = 9 }, integrations.Limits{RPS: base.RPS, Burst: base.Burst, MaxRetries: 9, Timeout: base.Timeout}},
		"timeout": {func(s *headMetadataSource) { s.RateLimit.TimeoutSeconds = 5 }, integrations.Limits{RPS: base.RPS, Burst: base.Burst, MaxRetries: base.MaxRetries, Timeout: 5 * time.Second}},
	} {
		src := headMetadataSource{}
		c.set(&src)
		if l := metadataLimits("google-books", src); l == nil || !sameLimits(*l, c.want) {
			t.Errorf("explicit %s: %+v, want %+v", name, l, c.want)
		}
	}
	// An explicit field wins over the tier.
	src := headMetadataSource{}
	src.RateLimit.Tier = "high"
	src.RateLimit.RPS = 1.5
	src.RateLimit.TimeoutSeconds = 12
	if l := metadataLimits("audible", src); l == nil || l.RPS != 1.5 || l.Timeout != 12*time.Second {
		t.Errorf("explicit fields lost to the tier: %+v", l)
	}
}

// TestWebhookTopics_CoverEveryPluginEvent pins the event-name map to HEAD's plugin
// event types (05-notify section 10): every event has a topic, none is invented,
// and "all" means the webhook plugin's default list of ten.
func TestWebhookTopics_CoverEveryPluginEvent(t *testing.T) {
	events := []plugin.EventType{
		plugin.EventBookImported, plugin.EventBookDeleted, plugin.EventMetadataApplied, plugin.EventTagsWritten,
		plugin.EventFileOrganized, plugin.EventDedupDetected, plugin.EventDedupMerged, plugin.EventCoverChanged,
		plugin.EventReadStatusChanged, plugin.EventScanCompleted, plugin.EventBookQuarantined, plugin.EventBookUnquarantined,
	}
	var want []string
	for _, e := range events {
		want = append(want, string(e))
	}
	slices.Sort(want)
	if got := slices.Sorted(maps.Keys(webhookTopics)); !slices.Equal(got, want) {
		t.Errorf("topic map keys %v, want HEAD's event types %v", got, want)
	}
	for ev, topic := range webhookTopics {
		if !strings.HasPrefix(topic, "books.") || !strings.HasSuffix(topic, ".v1") {
			t.Errorf("%s maps to %q, not a books.<name>.v1 topic", ev, topic)
		}
	}
	// The default list is every event but the two quarantine events.
	wantDefault := slices.DeleteFunc(slices.Clone(want), func(e string) bool {
		return e == string(plugin.EventBookQuarantined) || e == string(plugin.EventBookUnquarantined)
	})
	gotDefault := slices.Sorted(slices.Values(webhookDefaultEvents))
	if !slices.Equal(gotDefault, wantDefault) {
		t.Errorf("default events %v, want %v", gotDefault, wantDefault)
	}
}

// TestCapabilityID_CoversEveryHEADCapability pins the capability-ID map to HEAD's
// aidispatch registry: every capability HEAD registers lands under books., the
// llm family by prefix, and two different HEAD IDs never share a target.
func TestCapabilityID_CoversEveryHEADCapability(t *testing.T) {
	seen := map[string]string{}
	for _, spec := range aidispatch.Registry() {
		id := spec.Capability.ID()
		got := capabilityID(id)
		if !strings.HasPrefix(got, "books.") {
			t.Errorf("%s maps to %q, outside the books module", id, got)
		}
		if name, ok := strings.CutPrefix(id, "llm."); ok && got != "books."+name {
			t.Errorf("%s maps to %q, want books.%s", id, got, name)
		}
		if other, dup := seen[got]; dup {
			t.Errorf("%s and %s both map to %q", other, id, got)
		}
		seen[got] = id
	}
	if len(seen) == 0 {
		t.Fatal("aidispatch registers no capability")
	}
	// headCapabilities, the set M6 keeps ticks of, is aidispatch's registry
	// exactly: one HEAD registers and the frozen set lacks would be dropped from
	// every endpoint, and one the set has that HEAD no longer registers would be
	// carried to a capability no catalog declares.
	registered := map[string]bool{}
	for _, spec := range aidispatch.Registry() {
		registered[spec.Capability.ID()] = true
	}
	if !maps.Equal(registered, headCapabilities) {
		t.Errorf("headCapabilities %v, want aidispatch's registry %v", slices.Sorted(maps.Keys(headCapabilities)), slices.Sorted(maps.Keys(registered)))
	}
	for in, want := range map[string]string{
		"embed.text": "books.embed_text", "embed.text_batch": "books.embed_text_batch",
		"transcribe.batch": "books.transcribe_batch", "transcribe.intro_clip": "books.transcribe_intro_clip",
	} {
		if got := capabilityID(in); got != want {
			t.Errorf("capabilityID(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestTrust_OriginAndSignature pins the copied renderings of core's origin and
// signature functions (the real importer's acceptance of them is
// TestIntegrationsMigration_TrustCarried).
func TestTrust_OriginAndSignature(t *testing.T) {
	for raw, want := range map[string]string{
		"http://192.0.2.1":            "http://192.0.2.1:80",
		"HTTPS://Batcave.INVALID/x":   "https://batcave.invalid:443",
		"http://192.0.2.1:8080/a?b=c": "http://192.0.2.1:8080",
		"smtp://mail.invalid":         "smtp://mail.invalid:25",
		"smtps://mail.invalid":        "smtps://mail.invalid:465",
		"ws://r.invalid":              "ws://r.invalid:80",
		"http://[2001:db8::1]:9000/x": "http://[2001:db8::1]:9000",
		"gopher://old.invalid":        "gopher://old.invalid",
		"https://user:pw@a.invalid/x": "https://a.invalid:443",
	} {
		if got, ok := originOf(raw); !ok || got != want {
			t.Errorf("originOf(%q) = %q, %v; want %q", raw, got, ok, want)
		}
	}
	for _, bad := range []string{"", "no-scheme", "http://", "http://a b.invalid", "://x"} {
		if got, ok := originOf(bad); ok {
			t.Errorf("originOf(%q) = %q, want no origin", bad, got)
		}
	}
	if got := signature(nil); got != "adapter-default" {
		t.Errorf("signature(nil) = %q", got)
	}
	if got := signature(map[string]string{"url": "http://a:80"}); got != "url=http://a:80" {
		t.Errorf("one destination renders %q: the key is never dropped", got)
	}
	if got := signature(map[string]string{"url": "http://b:80", "alt_url": "http://a:80"}); got != "alt_url=http://a:80 url=http://b:80" {
		t.Errorf("two destinations render %q, want key order", got)
	}
}

// TestSplitUserinfo pins the splitter's cases, the ones F3-06's seam note lists
// for M2's splitURLUserinfo (internal/books/cutover/config_plan.go, unexported,
// so the two copies are compared by their cases, not by a call): what net/url
// reports moves out, a string without an @ is untouched, and an @ in the
// authority that net/url does not report is not guessed at.
func TestSplitUserinfo(t *testing.T) {
	for _, c := range []struct {
		in, clean, info string
		proven          bool
	}{
		{"http://192.0.2.1:9000/hook", "http://192.0.2.1:9000/hook", "", true},
		{"http://robin:grayson@192.0.2.1/hook", "http://192.0.2.1/hook", "robin:grayson", true},
		{"http://robin@192.0.2.1/hook", "http://192.0.2.1/hook", "robin", true},
		{"http://192.0.2.1/user@host", "http://192.0.2.1/user@host", "", true}, // @ in the path
		{"http://bat%zz@192.0.2.1/hook", "http://bat%zz@192.0.2.1/hook", "", false},
		{"robin:grayson@192.0.2.1:9000", "robin:grayson@192.0.2.1:9000", "", false}, // schemeless
		{"//robin@192.0.2.1/x", "//192.0.2.1/x", "robin", true},
	} {
		clean, info, proven := splitUserinfo(c.in)
		if proven != c.proven || (proven && (clean != c.clean || info != c.info)) {
			t.Errorf("splitUserinfo(%q) = %q, %q, %v; want %q, %q, %v", c.in, clean, info, proven, c.clean, c.info, c.proven)
		}
	}
}

// TestInstanceID pins the rename of a HEAD ID: kept when valid, lower-cased with
// _ as - otherwise, letter-prefixed when it starts with a digit, refused when no
// rename makes it a valid core ID.
func TestInstanceID(t *testing.T) {
	for _, c := range []struct {
		in      string
		want    integrations.InstanceID
		renamed bool
		ok      bool
	}{
		{"hardcover", "hardcover", false, true},
		{"Local_LLM", "local-llm", true, true},
		{"9Lives_Cat", "id-9lives-cat", true, true},
		{"0Day_Cape", "id-0day-cape", true, true}, // both ends of the digit range
		{"9_Lives", "id-9-lives", true, true},
		{"Bat Cave", "", false, false},
		{"", "", false, false},
		{strings.Repeat("a", 33), "", false, false},
		{"9" + strings.Repeat("a", 30), "", false, false}, // id- pushes it past 32
	} {
		got, renamed, ok := instanceID(c.in)
		if got != c.want || renamed != c.renamed || ok != c.ok {
			t.Errorf("instanceID(%q) = %q, %v, %v; want %q, %v, %v", c.in, got, renamed, ok, c.want, c.renamed, c.ok)
		}
	}
}

// sameLimits compares the four fields M6 sets (Limits itself holds a slice and is
// not comparable).
func sameLimits(a, b integrations.Limits) bool {
	return a.RPS == b.RPS && a.Burst == b.Burst && a.MaxRetries == b.MaxRetries && a.Timeout == b.Timeout
}

// jsonNames is the set of JSON field names of a struct type, from its tags.
func jsonNames(t *testing.T, typ reflect.Type) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	addJSONNames(t, typ, "", out)
	return out
}

// addJSONNames adds typ's JSON field names under prefix, and recurses into a
// field whose value is itself a JSON object of named fields (a struct, or a
// pointer, slice, array or map of one), as dotted paths: a field HEAD adds to
// a nested type (MetadataSource.RateLimit) is then as visible as a top-level
// one. A type that marshals itself (time.Time) is a leaf.
func addJSONNames(t *testing.T, typ reflect.Type, prefix string, out map[string]bool) {
	t.Helper()
	for f := range typ.Fields() {
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == "-" || (tag == "" && !f.Anonymous) {
			t.Fatalf("%s.%s has no JSON name: the pin cannot say what HEAD writes", typ, f.Name)
		}
		out[prefix+tag] = true
		if inner, ok := jsonObject(f.Type); ok {
			addJSONNames(t, inner, prefix+tag+".", out)
		}
	}
}

// jsonObject is the struct whose fields a value of typ is encoded as, false for
// a leaf.
func jsonObject(typ reflect.Type) (reflect.Type, bool) {
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array || typ.Kind() == reflect.Map {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil, false
	}
	marshaler := reflect.TypeFor[json.Marshaler]()
	text := reflect.TypeFor[encoding.TextMarshaler]()
	if typ.Implements(marshaler) || reflect.PointerTo(typ).Implements(marshaler) || typ.Implements(text) || reflect.PointerTo(typ).Implements(text) {
		return nil, false
	}
	return typ, true
}

// TestFrozenDecoders_CoverHEADFields holds each decoder M6 froze against the HEAD
// type whose rows it reads. Every field HEAD writes is either decoded or named in
// that type's lossy list (and a lossy field is not also decoded), and the decoder
// reads no field HEAD lacks. A field added to HEAD before the freeze is therefore a
// failing test, never a silent drop at the cutover.
func TestFrozenDecoders_CoverHEADFields(t *testing.T) {
	for _, c := range []struct {
		name         string
		head, frozen reflect.Type
		lossy        []string
	}{
		{"ai_endpoints", reflect.TypeFor[config.AIEndpoint](), reflect.TypeFor[headAIEndpoint](), nil},
		{"metadata_sources", reflect.TypeFor[config.MetadataSource](), reflect.TypeFor[headMetadataSource](), metadataSourceLossy},
		{"path_aliases", reflect.TypeFor[config.PathAlias](), reflect.TypeFor[headPathAlias](), nil},
		{"itunes.path_mappings", reflect.TypeFor[config.ITunesPathMap](), reflect.TypeFor[headPathMap](), nil},
		{"import_path", reflect.TypeFor[store.ImportPath](), reflect.TypeFor[importPathRow](), nil},
		{"aijob", reflect.TypeFor[store.AIJob](), reflect.TypeFor[aiJob](), jobLossy},
	} {
		t.Run(c.name, func(t *testing.T) {
			head, frozen := jsonNames(t, c.head), jsonNames(t, c.frozen)
			lossy := map[string]bool{}
			for _, n := range c.lossy {
				lossy[n] = true
			}
			for n := range head {
				if !frozen[n] && !lossy[n] {
					t.Errorf("HEAD writes %q and M6 neither decodes it nor lists it as lossy", n)
				}
			}
			for n := range frozen {
				if !head[n] {
					t.Errorf("the frozen decoder reads %q, which HEAD's type does not have", n)
				}
			}
			for n := range lossy {
				if !head[n] {
					t.Errorf("the lossy list names %q, which HEAD's type does not have", n)
				}
				if frozen[n] {
					t.Errorf("%q is both decoded and listed as lossy", n)
				}
			}
		})
	}
}

// endpointValidator is core's real ai endpoint validator with books'
// capabilities registered as the catalog would hold them: aidispatch's registry
// through capabilityID. Nothing it touches does I/O, so the registry's store,
// secrets and audit are never called.
func endpointValidator(t *testing.T) *ai.Router {
	t.Helper()
	reg, err := integrations.New(integrations.Options{
		Store: struct{ integrations.InstanceStore }{}, NewBatch: func() *storage.Batch { return nil },
		Secrets: struct{ integrations.SecretStore }{}, Audit: struct{ integrations.AuditRecorder }{},
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := ai.NewRouter(ai.RouterOptions{Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[aidispatch.Kind]ai.Kind{aidispatch.KindChat: ai.KindText, aidispatch.KindEmbed: ai.KindEmbed, aidispatch.KindWhisper: ai.KindTranscribe}
	for _, spec := range aidispatch.Registry() {
		if _, err := r.RegisterCapability("books", ai.CapabilitySpec{
			ID: capabilityID(spec.Capability.ID()), Kind: kinds[spec.Kind],
			Description: spec.Description, DataSent: "fixture", TypicalRequest: "fixture",
		}); err != nil {
			t.Fatalf("register %s: %v", spec.Capability.ID(), err)
		}
	}
	return r
}

// TestAIInstances_PassCoresEndpointValidator: core refuses an imported endpoint
// whole for one unknown or wildcard tick or one undeclared feature (an import has
// no stored row to grandfather a tick), so M6 drops each such value, reports it,
// and what it writes passes core's own ValidateEndpoint for every protocol.
func TestAIInstances_PassCoresEndpointValidator(t *testing.T) {
	r := endpointValidator(t)
	var every []string
	for _, spec := range aidispatch.Registry() {
		every = append(every, spec.Capability.ID())
	}
	for _, c := range []struct{ protocol, url string }{
		{"openai_compat", "http://192.0.2.40:8080/v1"},
		{"whisper_server", "http://192.0.2.41:9000"},
		{"local_process", "local:uv-openai-whisper"},
	} {
		t.Run(c.protocol, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{
				"id": "oracle-node", "label": "Oracle", "protocol": c.protocol, "url": c.url, "enabled": true,
				"capabilities":      append(slices.Clone(every), "mystery.cape", "llm.*", "*", "llm.dedup_review"),
				"features":          []string{"json_schema", "telepathy", "shared_fs:batcave-lib", "shared_fs:", "vision"},
				"capability_models": map[string]string{"llm.dedup_review": "alfred-13b", "mystery.cape": "riddler-1b"},
			})
			if err != nil {
				t.Fatal(err)
			}
			p, _, why := (&Migration{}).aiInstance(raw, 0, secretSet{})
			if p == nil {
				t.Fatalf("the endpoint was refused: %s", why)
			}
			if err := r.ValidateEndpoint(p.inst.Config.Settings, nil); err != nil {
				t.Fatalf("core refuses what M6 writes: %v", err)
			}
			for _, want := range []string{`tick "mystery.cape"`, `tick "llm.*"`, `tick "*"`, `feature "telepathy"`, `feature "shared_fs:"`, `capability_models entry "mystery.cape"`} {
				if !slices.ContainsFunc(p.dropped, func(d string) bool { return strings.Contains(d, want) }) {
					t.Errorf("the dropped %s is not reported: %v", want, p.dropped)
				}
			}
			if len(p.dropped) != 6 {
				t.Errorf("dropped %v, want exactly the six unknown values", p.dropped)
			}
			if cm, _ := p.inst.Config.Settings["capability_models"].(map[string]string); len(cm) != 1 || cm["books.dedup_review"] != "alfred-13b" {
				t.Errorf("capability_models %v, want only the registered capability's", cm)
			}
			caps, _ := p.inst.Config.Settings["capabilities"].([]string)
			if len(caps) != len(every) {
				t.Errorf("capabilities %v: every HEAD capability is kept, once", caps)
			}
			feats, _ := p.inst.Config.Settings["features"].([]string)
			if !slices.Equal(feats, []string{"json_schema", "shared_fs:batcave-lib", "vision"}) {
				t.Errorf("features %v", feats)
			}
			// The validator is not vacuous: each dropped value, put back, is refused.
			for key, bad := range map[string][]string{"capabilities": {"mystery.cape"}, "features": {"telepathy"}} {
				s := maps.Clone(p.inst.Config.Settings)
				s[key] = bad
				if r.ValidateEndpoint(s, nil) == nil {
					t.Errorf("core accepted %s %v: the pin proves nothing", key, bad)
				}
			}
		})
	}
}

// TestDerivedAlias_IsHEADsRendering pins derivedAlias against HEAD's renderer
// (web/src/utils/pathAliases.ts): toWindows trims one trailing separator run off
// the prefix and joins with a backslash, flipping every slash; toSmbURL joins
// with a slash and encodes each segment as encodeURIComponent does; an empty
// form stays empty. The want strings are what that TypeScript returns for the
// root's own path.
func TestDerivedAlias_IsHEADsRendering(t *testing.T) {
	for _, c := range []struct {
		a    headPathAlias
		rel  string
		want library.Alias
	}{
		{headPathAlias{Windows: "W:", UNC: `\\gotham\books`, SMBURL: "smb://gotham/books"}, "incoming",
			library.Alias{Windows: `W:\incoming`, UNC: `\\gotham\books\incoming`, SMBURL: "smb://gotham/books/incoming"}},
		{headPathAlias{Windows: `W:\`, UNC: `\\gotham\books\/`, SMBURL: "smb://gotham/books"}, "Knightfall/Part One",
			library.Alias{Windows: `W:\Knightfall\Part One`, UNC: `\\gotham\books\Knightfall\Part One`, SMBURL: "smb://gotham/books/Knightfall/Part%20One"}},
		{headPathAlias{SMBURL: "smb://gotham/books"}, "Hush (2002)/Año Uno & #1!~*'",
			library.Alias{SMBURL: "smb://gotham/books/Hush%20(2002)/A%C3%B1o%20Uno%20%26%20%231!~*'"}},
		{headPathAlias{Windows: "W:"}, "a?b=c/d",
			library.Alias{Windows: `W:\a?b=c\d`}},
	} {
		if got := derivedAlias(c.a, c.rel); got != c.want {
			t.Errorf("derivedAlias(%+v, %q)\n got %+v\nwant %+v", c.a, c.rel, got, c.want)
		}
	}
	if got := encodeURIComponent("a/b?c=d;e,f@g:h$i+j"); got != "a%2Fb%3Fc%3Dd%3Be%2Cf%40g%3Ah%24i%2Bj" {
		t.Errorf("encodeURIComponent reserved characters: %s", got)
	}
}

// TestCoreFeatures_PinnedBothWays: coreFeatures is exactly core's declared
// feature constants (none missing, none extra), and an endpoint carrying each
// one keeps it and passes core's validator.
func TestCoreFeatures_PinnedBothWays(t *testing.T) {
	declared := []ai.Feature{ai.FeatureJSONSchema, ai.FeatureJSONMode, ai.FeatureVision, ai.FeatureBatch, ai.FeatureTools, ai.FeatureTimestamps}
	if len(coreFeatures) != len(declared) {
		t.Errorf("coreFeatures has %d entries, core declares %d", len(coreFeatures), len(declared))
	}
	r := endpointValidator(t)
	for _, f := range declared {
		raw, err := json.Marshal(map[string]any{"id": "oracle-node", "protocol": "openai_compat", "url": "http://192.0.2.40:8080/v1", "enabled": true, "features": []string{string(f)}})
		if err != nil {
			t.Fatal(err)
		}
		p, _, why := (&Migration{}).aiInstance(raw, 0, secretSet{})
		if p == nil {
			t.Fatalf("refused: %s", why)
		}
		if got, _ := p.inst.Config.Settings["features"].([]string); !slices.Equal(got, []string{string(f)}) {
			t.Errorf("feature %s: kept %v", f, got)
		}
		if err := r.ValidateEndpoint(p.inst.Config.Settings, nil); err != nil {
			t.Errorf("feature %s: core refuses it: %v", f, err)
		}
	}
}

// TestMetadataAdapters_FoldTableIsTheAdaptersDeclaredSecrets pins metadataAdapters
// (which sources the credential fold knows and the one secret field each
// declares) against the real descriptors of the books metadata adapters, both
// ways: a source added to or removed from the adapters, or a secret field
// renamed, is a failure here and not a source whose credentials silently go
// dark or silently fold into a field that does not exist.
func TestMetadataAdapters_FoldTableIsTheAdaptersDeclaredSecrets(t *testing.T) {
	ds, err := metadata.Descriptors(metadata.Options{})
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]string{}
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
		var secretFields []string
		for name, p := range doc.Properties {
			if _, secret := p["x-mediacore-secret"]; secret {
				secretFields = append(secretFields, name)
			}
		}
		if len(secretFields) > 1 {
			t.Fatalf("%s declares %v: the fold plans one declared field", id, secretFields)
		}
		declared[id] = ""
		if len(secretFields) == 1 {
			declared[id] = secretFields[0]
		}
	}
	got := map[string]string{}
	for id, f := range metadataAdapters {
		got[id] = ""
		if f != nil {
			got[id] = f.field
			if f.order[0] != f.field {
				t.Errorf("%s: the declared field %s is not first in HEAD's order %v (the operator's secret is read first)", id, f.field, f.order)
			}
		}
	}
	if !maps.Equal(got, declared) {
		t.Errorf("the fold table is %v, the adapters declare %v", got, declared)
	}
}
