// file: internal/books/cutover/m6/pins_test.go
// version: 1.0.1
// guid: 21b7d4e8-0c95-4a36-8f1d-7e5a39c0b264
// last-edited: 2026-11-19

package m6

import (
	"maps"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/metadata/providerhttp"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	"github.com/falkcorp/mediacore/integrations"
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
	for in, want := range map[string]string{
		"embed.text": "books.embed_text", "embed.text_batch": "books.embed_text_batch",
		"transcribe.batch": "books.transcribe_batch", "transcribe.intro_clip": "books.transcribe_intro_clip",
		"mystery.cape": "mystery.cape", // an ID no table knows is kept
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
