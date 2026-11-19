// file: internal/books/cutover/m6/instances.go
// version: 1.0.1
// guid: 8b6a74f6-b7ad-4baf-bb70-c1f2c5ccb49b
// last-edited: 2026-11-19

package m6

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// Instance IDs of HEAD's singletons (05-metadata M1.10 migration table).
const (
	instanceDeluge = "deluge"
	instanceOpenAI = "openai"
	secretOpenAI   = secrets.Name("integrations.ai.openai.api_key")
)

// headAIEndpoint is HEAD's ai_endpoints row (internal/config/ai_endpoints.go
// AIEndpoint), frozen here.
type headAIEndpoint struct {
	ID               string            `json:"id"`
	Label            string            `json:"label"`
	Protocol         string            `json:"protocol"`
	URL              string            `json:"url"`
	ChatModel        string            `json:"chat_model"`
	EmbedModel       string            `json:"embed_model"`
	CapabilityModels map[string]string `json:"capability_models"`
	Priority         int               `json:"priority"`
	Concurrency      int               `json:"concurrency"`
	Enabled          bool              `json:"enabled"`
	Capabilities     []string          `json:"capabilities"`
	Labels           []string          `json:"labels"`
	Features         []string          `json:"features"`
	RequireGPU       bool              `json:"require_gpu"`
	AuthRef          string            `json:"auth_ref"`
	HostRoots        map[string]string `json:"host_roots"`
}

// headMetadataSource is HEAD's metadata_sources entry (internal/config/config.go
// MetadataSource), frozen here. Credentials are not read: M2 moved them out of
// the blob.
type headMetadataSource struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	Priority  int    `json:"priority"`
	BaseURL   string `json:"base_url"`
	RateLimit struct {
		Tier           string  `json:"tier"`
		RPS            float64 `json:"rps"`
		Burst          int     `json:"burst"`
		MaxRetries     int     `json:"max_retries"`
		TimeoutSeconds int     `json:"timeout_seconds"`
	} `json:"rate_limit"`
}

// secretSet is the names the secrets store holds.
type secretSet map[secrets.Name]bool

// names loads the store's names once per group pass.
func (m *Migration) names(ctx context.Context) (secretSet, error) {
	ns, err := m.cfg.SecretNames(ctx)
	if err != nil {
		return nil, fmt.Errorf("m6: list the secrets store: %w", err)
	}
	set := secretSet{}
	for _, n := range ns {
		set[n] = true
	}
	return set, nil
}

// refsFor is the settings of ref's kind and instance that name a secret the
// store holds: every name under integrations.<kind>.<instance>. becomes the
// field of the rest of the name (api_key, credentials.api_key, password).
func refsFor(set secretSet, ref integrations.Ref) map[string]any {
	prefix := "integrations." + string(ref.Kind) + "." + string(ref.Instance) + "."
	out := map[string]any{}
	for n := range set {
		field, ok := strings.CutPrefix(string(n), prefix)
		if !ok || field == "" || field == "userinfo" {
			continue
		}
		out[field] = secrets.RefTo(n)
	}
	return out
}

// instanceID is the InstanceID of a HEAD ID: kept when it already matches, else
// lower-cased with _ as - and reported (05-metadata M1.10, last paragraph before
// "Trust carried over"). ok is false when even that does not match.
func instanceID(head string) (id integrations.InstanceID, renamed, ok bool) {
	if integrations.InstanceID(head).Validate() == nil {
		return integrations.InstanceID(head), false, true
	}
	n := integrations.InstanceID(strings.ToLower(strings.ReplaceAll(head, "_", "-")))
	if n.Validate() != nil {
		return "", false, false
	}
	return n, true, true
}

// splitUserinfo removes the userinfo of a URL setting (a credential belongs in a
// secret, never in a stored URL: core's checkURL refuses one). It returns the
// clean URL and the userinfo, and proven false when a credential cannot be ruled
// out. This is M2's splitter (internal/books/cutover/config_plan.go
// splitURLUserinfo) for the five URL fields M2 leaves in the blob for M6,
// pinned by a test against the same inputs.
func splitUserinfo(s string) (clean, info string, proven bool) {
	if !strings.Contains(s, "@") {
		return s, "", true
	}
	u, err := url.Parse(s)
	if err != nil {
		return s, "", false
	}
	if u.User != nil {
		info = u.User.String()
		u.User = nil
		return u.String(), info, true
	}
	if authorityHasAt(s) {
		return s, "", false
	}
	return s, "", true
}

// authorityHasAt reports an @ in the part of s that would be its authority.
func authorityHasAt(s string) bool {
	rest := s
	if i := strings.Index(s, "://"); i > 0 && !strings.ContainsAny(s[:i], "/?#@") {
		rest = s[i+3:]
	} else if strings.HasPrefix(s, "//") {
		rest = s[2:]
	}
	if end := strings.IndexAny(rest, "/?#"); end >= 0 {
		rest = rest[:end]
	}
	return strings.Contains(rest, "@")
}

// pending is an instance being built, with the raw source member it came from
// (kept for quarantine) and its URL fields.
type pending struct {
	inst integrations.StoredInstance
	// member is the blob member the instance came from, and raw its JSON.
	member string
	raw    []byte
	// urls is the destination fields: settings field to its (clean) URL.
	urls map[string]string
	// userinfo is a credential split out of a URL field, to import as a secret.
	userinfo string
	// copyKey, when set, is the secret to copy the stored OpenAI key into.
	copyKey secrets.Name
	// operatorPair is one of the four operator-secret pairs of M1.4 step 2.
	operatorPair bool
	renamedFrom  string
}

// finish applies "Trust carried over" to p: Local with its origin for any
// instance with a destination, the secrets origin when it holds a secret, and the
// operator-secret origins for the four pairs.
func (p *pending) finish() {
	dests := map[string]string{}
	for field, raw := range p.urls {
		if o, ok := originOf(raw); ok {
			dests[field] = o
		}
	}
	sig := signature(dests)
	if len(dests) > 0 {
		p.inst.Config.Local = true
		p.inst.LocalOrigin = sig
		if p.operatorPair {
			p.inst.OperatorSecretOrigin = maps2(dests)
		}
	}
	holds := false
	for _, v := range p.inst.Config.Settings {
		if _, ok := v.(secrets.Ref); ok {
			holds = true
		}
	}
	if holds || p.copyKey != "" || p.userinfo != "" {
		p.inst.SecretsOrigin = sig
	}
}

func maps2(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// cleanURL splits userinfo out of one URL field into p and returns the URL to
// store, and false when a credential cannot be ruled out (the caller
// quarantines the member).
func (p *pending) cleanURL(field, raw string) (string, bool) {
	clean, info, proven := splitUserinfo(raw)
	if !proven {
		return "", false
	}
	if info != "" {
		p.userinfo = info
	}
	if clean != "" {
		p.urls[field] = clean
	}
	return clean, true
}

// writeInstance imports p, and the secrets it brings (the userinfo split from
// its URL and an OpenAI key copy), into the chunk batch. A refusal quarantines
// the member the instance came from, once.
func (m *Migration) writeInstance(ctx context.Context, c *chunk, p *pending, quarantined map[string]bool) (wrote bool, err error) {
	p.finish()
	ref := p.inst.Config.Ref
	detail := string(p.inst.Config.Adapter)
	if p.renamedFrom != "" {
		detail += "; id " + p.renamedFrom + " renamed"
	}
	m.report(Entry{Kind: "instance", ID: ref.String(), Detail: detail})
	if p.inst.Config.Local {
		m.report(Entry{Kind: "trust", ID: ref.String(), Detail: "Local, origin " + p.inst.LocalOrigin})
	}
	for _, field := range slices.Sorted(maps.Keys(p.urls)) {
		if u, err := url.Parse(p.urls[field]); err != nil || u.Scheme == "" || u.Host == "" {
			// Kept as stored and never granted trust: the instance's own health
			// reports it misconfigured until an admin corrects it (05-metadata
			// M1.10), not a row M6 can decline.
			m.report(Entry{Kind: "note", ID: ref.String(), Detail: "misconfigured: the " + field + " does not parse as a URL; kept as stored"})
		}
	}
	if m.cfg.DryRun {
		return true, nil
	}
	b := c.batch()
	// Read the key to copy first and stage the secrets only after the instance
	// import succeeds: a refused or conflicting instance is quarantined, and a
	// secret staged for it would be left in the store with no instance.
	var copyVal string
	if p.copyKey != "" {
		val, found, err := m.cfg.SecretValue(ctx, secretOpenAI)
		switch {
		case err != nil:
			return false, fmt.Errorf("m6: read the OpenAI key: %w", err)
		case !found:
			// HEAD's key lived only in the environment, or is unreadable: no copy;
			// the instance reports integration_secret_missing until an admin stores one.
			c.note(OutcomeSecretAbsent)
			m.report(Entry{Kind: "note", ID: ref.String(), Detail: "auth_ref openai_api_key but no stored key to copy"})
			p.copyKey = ""
		default:
			copyVal = val
			p.inst.Config.Settings["api_key"] = secrets.RefTo(p.copyKey)
			p.inst.SecretsOrigin = signature(destsOf(p.urls))
		}
	}
	err = m.cfg.Integrations.ImportInstance(ctx, b, p.inst)
	switch {
	case errors.Is(err, storage.ErrImportConflict):
		return false, m.quarantineMember(c, p.member, p.raw, reasonInstanceConflict, quarantined)
	case err != nil && refused(err):
		return false, m.quarantineMember(c, p.member, p.raw, reasonRefused, quarantined)
	case err != nil:
		return false, fmt.Errorf("m6: import %s: %w", ref, err)
	}
	if p.copyKey != "" {
		if err := m.cfg.Secrets.ImportSecret(ctx, b, secrets.Write{Name: p.copyKey, Value: secrets.NewValue(copyVal), By: m.by}); err != nil && !errors.Is(err, storage.ErrImportConflict) {
			return false, fmt.Errorf("m6: copy the OpenAI key to %s: %w", p.copyKey, err)
		}
		c.note(OutcomeSecretCopy)
	}
	if p.userinfo != "" {
		name := secretName(ref, "userinfo")
		if err := m.cfg.Secrets.ImportSecret(ctx, b, secrets.Write{Name: name, Value: secrets.NewValue(p.userinfo), By: m.by}); err != nil && !errors.Is(err, storage.ErrImportConflict) {
			return false, fmt.Errorf("m6: move the userinfo of %s: %w", ref, err)
		}
		c.note(OutcomeUserinfo)
	}
	c.note(OutcomeInstance)
	if p.inst.Config.Local {
		c.note(OutcomeLocalGrant)
	}
	return true, nil
}

func destsOf(urls map[string]string) map[string]string {
	d := map[string]string{}
	for f, raw := range urls {
		if o, ok := originOf(raw); ok {
			d[f] = o
		}
	}
	return d
}

// refused reports a validation refusal by one of core's importers: the value is
// bad, the store is fine.
func refused(err error) bool {
	var fe *config.FieldError
	return errors.Is(err, integrations.ErrInvalid) || errors.Is(err, config.ErrInvalid) ||
		errors.Is(err, config.ErrUnknownKey) || errors.As(err, &fe)
}

// quarantineMember quarantines a blob member under the synthetic key
// setting:config_blob#<member> with its raw JSON, once per member.
func (m *Migration) quarantineMember(c *chunk, member string, raw []byte, reason string, done map[string]bool) error {
	if done[member] {
		return nil
	}
	done[member] = true
	return c.quarantine(KeyBlob+"#"+member, raw, reason)
}

// metadataInstance builds metadata/<id> from one metadata_sources entry.
func (m *Migration) metadataInstance(raw json.RawMessage, i int, set secretSet) (*pending, string) {
	member := fmt.Sprintf("metadata_sources.%d", i)
	var src headMetadataSource
	if err := json.Unmarshal(raw, &src); err != nil || strings.TrimSpace(src.ID) == "" {
		return nil, member
	}
	id, renamed, ok := instanceID(src.ID)
	if !ok {
		return nil, member
	}
	ref := integrations.Ref{Kind: integrations.KindMetadata, Instance: id}
	adapter := integrations.AdapterID("books." + string(id))
	p := &pending{member: member, raw: raw, urls: map[string]string{}, operatorPair: id == "google-books" || id == "hardcover"}
	if renamed {
		p.renamedFrom = src.ID
	}
	settings := refsFor(set, ref)
	if src.BaseURL != "" {
		clean, ok := p.cleanURL("base_url", src.BaseURL)
		if !ok {
			return nil, member
		}
		settings["base_url"] = clean
	}
	p.inst = integrations.StoredInstance{Config: integrations.InstanceConfig{
		Ref: ref, Adapter: adapter, Name: src.Name, Enabled: src.Enabled, Priority: src.Priority,
		Settings: settings, Limits: metadataLimits(string(id), src),
	}}
	return p, ""
}

// builtinLimits are HEAD's per-provider budgets (internal/metadata/providerhttp
// defaultLimits and fallbackLimits), the anchor a tier multiplies. Copied here;
// TestMetadataLimits_BuiltinTableMatchesProviderhttp pins it.
var builtinLimits = map[string]integrations.Limits{
	"hardcover":    {RPS: 1.0, Burst: 1, MaxRetries: 3, Timeout: 30 * time.Second},
	"audible":      {RPS: 2.0, Burst: 2, MaxRetries: 3, Timeout: 30 * time.Second},
	"audnexus":     {RPS: 2.0, Burst: 2, MaxRetries: 3, Timeout: 30 * time.Second},
	"google-books": {RPS: 3.0, Burst: 3, MaxRetries: 3, Timeout: 30 * time.Second},
	"openlibrary":  {RPS: 3.0, Burst: 3, MaxRetries: 3, Timeout: 30 * time.Second},
	"wikipedia":    {RPS: 2.0, Burst: 2, MaxRetries: 3, Timeout: 30 * time.Second},
	"cover":        {RPS: 4.0, Burst: 4, MaxRetries: 2, Timeout: 60 * time.Second},
}

var fallbackLimits = integrations.Limits{RPS: 2.0, Burst: 2, MaxRetries: 3, Timeout: 30 * time.Second}

// metadataLimits is a source's rate_limit as native Limits (05-metadata M2.7:
// "a source's limits.tier becomes its Limits"), resolved exactly as HEAD's
// effectiveProviderLimits does: the tier multiplies the provider's built-in
// RPS (low 0.5, high 2, anything else 1), burst scales with it, and any explicit
// field wins. nil when the source sets nothing, so the adapter's Meta.Limits
// apply ("else the adapter's Meta.Limits"). Fields HEAD leaves at the built-in
// are written as that built-in, because core reads a zero field as "inherit".
func metadataLimits(id string, src headMetadataSource) *integrations.Limits {
	rl := src.RateLimit
	if rl.Tier == "" && rl.RPS == 0 && rl.Burst == 0 && rl.MaxRetries == 0 && rl.TimeoutSeconds == 0 {
		return nil
	}
	base, ok := builtinLimits[id]
	if !ok {
		base = fallbackLimits
	}
	mult := 1.0
	switch rl.Tier {
	case "low":
		mult = 0.5
	case "high":
		mult = 2.0
	}
	l := integrations.Limits{RPS: base.RPS * mult, Burst: base.Burst, MaxRetries: base.MaxRetries, Timeout: base.Timeout}
	if mult != 1.0 {
		if scaled := int(math.Round(float64(base.Burst) * mult)); scaled >= 1 {
			l.Burst = scaled
		}
	}
	if rl.RPS > 0 {
		l.RPS = rl.RPS
	}
	if rl.Burst > 0 {
		l.Burst = rl.Burst
	}
	if rl.MaxRetries > 0 {
		l.MaxRetries = rl.MaxRetries
	}
	if rl.TimeoutSeconds > 0 {
		l.Timeout = time.Duration(rl.TimeoutSeconds) * time.Second
	}
	return &l
}

// aiInstance builds ai/<id> from one ai_endpoints row (05-ai section 10).
func (m *Migration) aiInstance(raw json.RawMessage, i int, set secretSet) (*pending, string) {
	member := fmt.Sprintf("ai_endpoints.%d", i)
	var ep headAIEndpoint
	if err := json.Unmarshal(raw, &ep); err != nil || strings.TrimSpace(ep.ID) == "" {
		return nil, member
	}
	id, renamed, ok := instanceID(ep.ID)
	if !ok {
		return nil, member
	}
	adapter, ok := protocolAdapter(ep.Protocol)
	if !ok {
		return nil, member
	}
	ref := integrations.Ref{Kind: integrations.KindAI, Instance: id}
	p := &pending{member: member, raw: raw, urls: map[string]string{}, operatorPair: id == instanceOpenAI}
	if renamed {
		p.renamedFrom = ep.ID
	}
	settings := refsFor(set, ref)
	// A whisper process endpoint's URL names no network address
	// (config.LocalUVEndpointURL "local:uv-openai-whisper"): it is carried as is
	// and grants no trust.
	if ep.URL != "" {
		if adapter == "books.whisper_local" {
			settings["url"] = ep.URL
		} else {
			clean, ok := p.cleanURL("url", ep.URL)
			if !ok {
				return nil, member
			}
			settings["url"] = clean
		}
	}
	settings["concurrency"] = max(ep.Concurrency, 1) // HEAD treats 0 as 1; core rejects 0
	if caps := mapCaps(ep.Capabilities); len(caps) > 0 {
		settings["capabilities"] = caps
	}
	if ep.ChatModel != "" {
		settings["text_model"] = ep.ChatModel
	}
	if ep.EmbedModel != "" {
		settings["embed_model"] = ep.EmbedModel
	}
	if len(ep.CapabilityModels) > 0 {
		cm := map[string]string{}
		for k, v := range ep.CapabilityModels {
			cm[capabilityID(k)] = v
		}
		settings["capability_models"] = cm
	}
	if len(ep.Labels) > 0 {
		settings["labels"] = ep.Labels
	}
	if len(ep.Features) > 0 {
		settings["features"] = ep.Features
	}
	if ep.RequireGPU {
		settings["require_gpu"] = true
	}
	if len(ep.HostRoots) > 0 {
		settings["root_paths"] = ep.HostRoots
	}
	// auth_ref: the native design has none. The openai row keeps the secret M2
	// moved to its name; any other row that named openai_api_key gets its own copy.
	if ep.AuthRef == "openai_api_key" && id != instanceOpenAI {
		p.copyKey = secretName(ref, "api_key")
		delete(settings, "api_key")
	}
	// HEAD calls any endpoint with an auth_ref cloud whatever its host
	// (aidispatch/locality.go:43-70); native auto would judge by URL and could
	// let a private-address cloud proxy hold local-only work.
	if ep.AuthRef != "" {
		settings["locality"] = string(ai.LocalityCloud)
	}
	name := ep.Label
	p.inst = integrations.StoredInstance{Config: integrations.InstanceConfig{
		Ref: ref, Adapter: adapter, Name: name, Enabled: ep.Enabled, Priority: ep.Priority, Settings: settings,
	}}
	return p, ""
}

// mapCaps maps each tick through the capability-ID map, dropping a duplicate the
// map creates, keeping order.
func mapCaps(in []string) []string {
	var out []string
	for _, c := range in {
		id := capabilityID(c)
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// delugeInstance builds download/deluge from deluge_web_url (05-download
// section 10); nil when HEAD had no web URL (the instance would have no
// destination).
func (m *Migration) delugeInstance(webURL string, set secretSet) (*pending, bool) {
	if strings.TrimSpace(webURL) == "" {
		return nil, true
	}
	ref := integrations.Ref{Kind: integrations.KindDownload, Instance: instanceDeluge}
	p := &pending{member: "deluge_web_url", raw: []byte(webURL), urls: map[string]string{}, operatorPair: true}
	// The member's raw bytes go to quarantine only on a refusal; the URL may carry
	// userinfo, which is why the step declares the family Sensitive.
	settings := refsFor(set, ref)
	clean, ok := p.cleanURL("url", webURL)
	if !ok {
		return nil, false
	}
	settings["url"] = clean
	p.inst = integrations.StoredInstance{Config: integrations.InstanceConfig{
		Ref: ref, Adapter: "core.deluge", Name: "Deluge", Enabled: true, Settings: settings,
	}}
	return p, true
}

// openAIInstance is the ai/openai row HEAD synthesises from openai_base_url and
// the stored key (05-metadata M1.10 migration table): HEAD's own defaults for the
// URL, priority and concurrency, the batch and vision features, and the key by
// reference. Its capability ticks are not recoverable from a row that was never
// stored, so none are set, which is default-deny until an admin ticks them.
func (m *Migration) openAIInstance(base string, set secretSet) *pending {
	ref := integrations.Ref{Kind: integrations.KindAI, Instance: instanceOpenAI}
	if strings.TrimSpace(base) == "" {
		base = defaultOpenAIBaseURL
	}
	p := &pending{member: "openai_base_url", raw: []byte(base), urls: map[string]string{}, operatorPair: true}
	clean, ok := p.cleanURL("url", base)
	if !ok {
		return nil
	}
	settings := refsFor(set, ref)
	settings["url"] = clean
	settings["concurrency"] = 4
	settings["features"] = []string{"batch_api", "vision"}
	// HEAD's openai row carries auth_ref openai_api_key, and HEAD's locality
	// heuristic calls any endpoint with an auth_ref cloud (aidispatch/locality.go).
	settings["locality"] = string(ai.LocalityCloud)
	p.inst = integrations.StoredInstance{Config: integrations.InstanceConfig{
		Ref: ref, Adapter: "core.openai_compat", Name: "OpenAI (migrated from openai_api_key)", Enabled: true, Priority: 20, Settings: settings,
	}}
	return p
}

// defaultOpenAIBaseURL is HEAD's config.DefaultOpenAIBaseURL, pinned by a test.
const defaultOpenAIBaseURL = "https://api.openai.com/v1"
