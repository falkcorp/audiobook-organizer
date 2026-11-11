// file: core/internal/integrationhttp/types.go
// version: 1.0.0
// guid: 0b7f3a52-6c1e-4d0a-9b84-3f2a7e51c9d4
// last-edited: 2026-11-11

package integrationhttp

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/secrets"
)

// The wire types. integrations.InstanceConfig carries no JSON tags and holds
// a *time.Location and secrets.Refs, so the routes answer with these views
// instead: snake_case names (05-http-api §3.4), no secret field in settings,
// a set/unset marker per secret field, no remote text. Their names carry the
// "Integration" prefix because an OpenAPI component is named
// <module>.<TypeName> and every core capability shares the "core" owner.

// IntegrationInstance is one instance as GET, POST and PUT answer. A secret
// field is never in Settings: SecretsSet names every secret field of the
// adapter and says whether it holds a value. Locked, Origin and the version
// are as the Registry serves them.
type IntegrationInstance struct {
	Kind       string             `json:"kind"`
	ID         string             `json:"id"`
	Adapter    string             `json:"adapter"`
	Name       string             `json:"name"`
	Enabled    bool               `json:"enabled"`
	Priority   int                `json:"priority"`
	Origin     string             `json:"origin"`
	Local      bool               `json:"local"`
	Settings   map[string]any     `json:"settings"`
	SecretsSet map[string]bool    `json:"secrets_set"`
	Limits     *IntegrationLimits `json:"limits,omitempty"`
	Version    uint64             `json:"version"`
	Updated    time.Time          `json:"updated,omitzero"`
	Locked     map[string]string  `json:"locked,omitempty"`
	Stored     bool               `json:"stored"`
}

// IntegrationLimits is integrations.Limits on the wire: durations are Go
// duration strings ("30s"), a daily zone an IANA name, a hold class its
// Class.String() name. It is both what an instance answers and what a body
// may carry; a zero field inherits (M1.3 Limits).
type IntegrationLimits struct {
	RPS           float64                `json:"rps,omitempty"`
	Burst         int                    `json:"burst,omitempty"`
	MaxRetries    int                    `json:"max_retries,omitempty"`
	Timeout       string                 `json:"timeout,omitempty"`
	RetryAfterMax string                 `json:"retry_after_max,omitempty"`
	Daily         *IntegrationDaily      `json:"daily,omitempty"`
	Breaker       *IntegrationBreaker    `json:"breaker,omitempty"`
	Holds         *IntegrationHoldPolicy `json:"holds,omitempty"`
}

// IntegrationDaily is integrations.DailyLimits on the wire.
type IntegrationDaily struct {
	Total      int      `json:"total"`
	Background int      `json:"background"`
	Zone       string   `json:"zone,omitempty"`
	Counters   []string `json:"counters,omitempty"`
}

// IntegrationBreaker is integrations.BreakerPolicy on the wire.
type IntegrationBreaker struct {
	Threshold    int    `json:"threshold,omitempty"`
	Cooldown     string `json:"cooldown,omitempty"`
	ProbeTimeout string `json:"probe_timeout,omitempty"`
}

// IntegrationHoldPolicy is integrations.HoldPolicy on the wire.
type IntegrationHoldPolicy struct {
	Durations map[string]string `json:"durations,omitempty"`
	Max       string            `json:"max,omitempty"`
	Persist   bool              `json:"persist"`
}

// IntegrationAdapter is one adapter's Meta: the settings schema is the one
// field list the form renders, secret fields marked x-mediacore-secret.
type IntegrationAdapter struct {
	ID             string             `json:"id"`
	Kind           string             `json:"kind"`
	DisplayName    string             `json:"display_name"`
	Version        string             `json:"version"`
	InfoURL        string             `json:"info_url,omitempty"`
	SettingsSchema json.RawMessage    `json:"settings_schema"`
	Egress         string             `json:"egress"`
	Limits         *IntegrationLimits `json:"limits,omitempty"`
	MaxInstances   int                `json:"max_instances"`
	Origin         string             `json:"origin"`
}

// IntegrationTestResult is Registry.Test's answer: OK, the Class and the
// protocol status, never a body or remote text (M1.5, SEC-15).
type IntegrationTestResult struct {
	OK     bool   `json:"ok"`
	Class  string `json:"class"`
	Status int    `json:"status"`
}

// IntegrationHold is an active hold, without its remote-text detail.
type IntegrationHold struct {
	Scope string    `json:"scope"`
	Class string    `json:"class"`
	Until time.Time `json:"until"`
	SetAt time.Time `json:"set_at"`
}

// IntegrationHealth is one instance's health. Message and RemoteVersion are
// what the adapter reported (its contract says "already redacted"); the view
// bounds both, so an adapter that forgot cannot make the route a channel for
// a long remote answer.
type IntegrationHealth struct {
	Status        string           `json:"status"`
	Message       string           `json:"message,omitempty"`
	RemoteVersion string           `json:"remote_version,omitempty"`
	LatencyMS     int64            `json:"latency_ms"`
	CheckedAt     time.Time        `json:"checked_at"`
	Hold          *IntegrationHold `json:"hold,omitempty"`
	Breaker       string           `json:"breaker"`
}

// IntegrationInstanceBody is the body of POST (create), PUT (replace) and the
// two test routes. It is the editable part of an instance: PUT replaces every
// field of it, so a client sends the whole form back.
//
// Secrets is write-only. A secret field the body omits, or sends empty, or
// sends as an echoed or mask-shaped value, keeps its stored secret while no
// destination moves (M1.3 Put, SEC3-4); the route never turns a missing field
// into the stored value. A secret is cleared by settings.<field> = null. The
// body's secret values reach the Registry as secrets.Values and nowhere else.
type IntegrationInstanceBody struct {
	// ID names the instance on create; on replace and the instance-scoped
	// test it is optional and must equal the path's.
	ID       string             `json:"id,omitempty"`
	Adapter  string             `json:"adapter,omitempty"`
	Name     string             `json:"name"`
	Enabled  *bool              `json:"enabled,omitempty"`
	Priority int                `json:"priority"`
	Local    bool               `json:"local"`
	Settings map[string]any     `json:"settings,omitempty"`
	Secrets  map[string]string  `json:"secrets,omitempty"`
	Limits   *IntegrationLimits `json:"limits,omitempty"`
	// Version is the stored row's version, required on replace (at least 1);
	// a create names none.
	Version uint64 `json:"version,omitempty"`
}

// Bounds the route applies before the Registry sees a body. They are sanity
// bounds that keep a body from being a way to store or hash megabytes, not
// product limits.
const (
	maxNameRunes      = 128
	maxPriority       = 1_000_000
	maxSettingsKeys   = 128
	maxFieldName      = 64
	maxSecretFields   = 32
	maxSecretBytes    = 8 << 10
	maxCounters       = 16
	maxHoldClasses    = 16
	maxRPS            = 10_000
	maxBurst          = 100_000
	maxRetries        = 100
	maxQuota          = 1_000_000_000
	maxLimitDuration  = 7 * 24 * time.Hour
	maxHealthMessage  = 256
	maxRemoteVersions = 64
)

// bodyMode is what a body is for.
type bodyMode uint8

const (
	modeCreate bodyMode = iota + 1
	modeReplace
	modeTest
)

// validate checks the body's shape for mode against the path's instance ID
// (empty for a create and the unscoped test). Every message is fixed text: a
// value of the body, a secret above all, is never quoted back.
func (b *IntegrationInstanceBody) validate(mode bodyMode, pathID string) []httpapi.FieldError {
	var errs []httpapi.FieldError
	bad := func(ptr, code, msg string) { errs = append(errs, httpapi.Field("/body"+ptr, code, msg)) }
	switch mode {
	case modeCreate:
		if b.ID == "" {
			bad("/id", "required", "an instance ID is required")
		} else if err := integrations.InstanceID(b.ID).Validate(); err != nil {
			bad("/id", "invalid", "must match [a-z][a-z0-9-]{0,31}")
		}
		if b.Adapter == "" {
			bad("/adapter", "required", "an adapter is required")
		}
		if b.Version != 0 {
			bad("/version", "invalid", "a new instance has no version")
		}
	case modeReplace:
		if b.ID != "" && b.ID != pathID {
			bad("/id", "invalid", "must equal the instance in the path")
		}
		if b.Version < 1 {
			bad("/version", "required", "the stored version is required")
		}
	case modeTest:
		if pathID != "" && b.ID != "" && b.ID != pathID {
			bad("/id", "invalid", "must equal the instance in the path")
		}
	}
	if b.Adapter != "" {
		if err := integrations.AdapterID(b.Adapter).Validate(); err != nil {
			bad("/adapter", "invalid", "must be <module>.<name>")
		}
	}
	if mode != modeTest && b.Enabled == nil {
		bad("/enabled", "required", "enabled is required")
	}
	if utf8.RuneCountInString(b.Name) > maxNameRunes {
		bad("/name", "too_long", fmt.Sprintf("must be at most %d characters", maxNameRunes))
	}
	if !validText(b.Name) {
		bad("/name", "invalid", "must be valid text without control characters")
	}
	if b.Priority < -maxPriority || b.Priority > maxPriority {
		bad("/priority", "out_of_range", fmt.Sprintf("must be within %d", maxPriority))
	}
	if len(b.Settings) > maxSettingsKeys {
		bad("/settings", "too_many", fmt.Sprintf("at most %d fields", maxSettingsKeys))
	}
	for k := range b.Settings {
		if k == "" || len(k) > maxFieldName {
			bad("/settings", "invalid", "a field name is empty or too long")
			break
		}
	}
	if len(b.Secrets) > maxSecretFields {
		bad("/secrets", "too_many", fmt.Sprintf("at most %d fields", maxSecretFields))
	}
	for k, v := range b.Secrets {
		if k == "" || len(k) > maxFieldName {
			bad("/secrets", "invalid", "a field name is empty or too long")
			break
		}
		if len(v) > maxSecretBytes {
			bad("/secrets/"+escapePointer(k), "too_long", fmt.Sprintf("must be at most %d bytes", maxSecretBytes))
		}
	}
	if b.Limits != nil {
		_, lerrs := parseLimits(b.Limits, "/body/limits")
		errs = append(errs, lerrs...)
	}
	return errs
}

// config builds the Registry's input from a validated body: the instance
// (Origin is the Registry's to set, Version only a replace names) and the
// secretsIn map, taken from the body's secrets and from nothing else.
func (b *IntegrationInstanceBody) config(ref integrations.Ref, version uint64) (integrations.InstanceConfig, map[string]secrets.Value, error) {
	limits, lerrs := parseLimits(b.Limits, "/body/limits")
	if len(lerrs) > 0 {
		return integrations.InstanceConfig{}, nil, httpapi.Invalid(lerrs...)
	}
	enabled := true // a test builds an enabled throwaway
	if b.Enabled != nil {
		enabled = *b.Enabled
	}
	inst := integrations.InstanceConfig{
		Ref: ref, Adapter: integrations.AdapterID(b.Adapter), Name: b.Name, Enabled: enabled,
		Priority: b.Priority, Local: b.Local, Settings: b.Settings, Limits: limits, Version: version,
	}
	var in map[string]secrets.Value
	if len(b.Secrets) > 0 {
		in = make(map[string]secrets.Value, len(b.Secrets))
		for f, v := range b.Secrets {
			in[f] = secrets.NewValue(v)
		}
	}
	return inst, in, nil
}

// validText reports valid UTF-8 without control characters.
func validText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

// escapePointer escapes one JSON pointer segment (RFC 6901).
func escapePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

// ---- limits ---------------------------------------------------------------

// classes is every Class by name, in value order.
func classes() []integrations.Class {
	var out []integrations.Class
	for c := integrations.ClassUnknown; c <= integrations.ClassBadOutput; c++ {
		out = append(out, c)
	}
	return out
}

// parseLimits reads a wire Limits. nil is nil (the instance inherits); every
// problem is a FieldError under at, with fixed text.
func parseLimits(l *IntegrationLimits, at string) (*integrations.Limits, []httpapi.FieldError) {
	if l == nil {
		return nil, nil
	}
	var errs []httpapi.FieldError
	bad := func(ptr, code, msg string) { errs = append(errs, httpapi.Field(at+ptr, code, msg)) }
	dur := func(ptr, raw string) time.Duration {
		if raw == "" {
			return 0
		}
		d, err := time.ParseDuration(raw)
		if err != nil || d < 0 || d > maxLimitDuration {
			bad(ptr, "invalid", "must be a duration such as 30s, between 0 and 168h")
			return 0
		}
		return d
	}
	out := &integrations.Limits{
		Burst: l.Burst, MaxRetries: l.MaxRetries,
		Timeout: dur("/timeout", l.Timeout), RetryAfterMax: dur("/retry_after_max", l.RetryAfterMax),
	}
	if math.IsNaN(l.RPS) || l.RPS < 0 || l.RPS > maxRPS {
		bad("/rps", "out_of_range", "must be between 0 and 10000")
	}
	out.RPS = l.RPS
	if l.Burst < 0 || l.Burst > maxBurst {
		bad("/burst", "out_of_range", "must be between 0 and 100000")
	}
	if l.MaxRetries < 0 || l.MaxRetries > maxRetries {
		bad("/max_retries", "out_of_range", "must be between 0 and 100")
	}
	if d := l.Daily; d != nil {
		daily := &integrations.DailyLimits{Total: d.Total, Background: d.Background}
		if d.Total < 0 || d.Total > maxQuota || d.Background < 0 || d.Background > d.Total {
			bad("/daily", "out_of_range", "total and background must be within 0 and 1e9, background at most total")
		}
		if d.Zone != "" {
			z, err := time.LoadLocation(d.Zone)
			if err != nil {
				bad("/daily/zone", "invalid", "must be an IANA time zone name")
			}
			daily.Zone = z
		}
		if len(d.Counters) > maxCounters {
			bad("/daily/counters", "too_many", "at most 16 counters")
		}
		for _, c := range d.Counters {
			if c == "" || len(c) > maxFieldName || !validText(c) {
				bad("/daily/counters", "invalid", "a counter name is empty, too long or not text")
				break
			}
		}
		daily.Counters = slices.Clone(d.Counters)
		out.Daily = daily
	}
	if b := l.Breaker; b != nil {
		if b.Threshold < 0 || b.Threshold > maxRetries*10 {
			bad("/breaker/threshold", "out_of_range", "must be between 0 and 1000")
		}
		out.Breaker = integrations.BreakerPolicy{
			Threshold: b.Threshold, Cooldown: dur("/breaker/cooldown", b.Cooldown),
			ProbeTimeout: dur("/breaker/probe_timeout", b.ProbeTimeout),
		}
	}
	if h := l.Holds; h != nil {
		hp := integrations.HoldPolicy{Max: dur("/holds/max", h.Max), Persist: h.Persist}
		if len(h.Durations) > maxHoldClasses {
			bad("/holds/durations", "too_many", "at most one entry per class")
		}
		for name, raw := range h.Durations {
			c, ok := classByName(name)
			if !ok {
				bad("/holds/durations", "invalid", "a key is not a class name")
				continue
			}
			if hp.Durations == nil {
				hp.Durations = map[integrations.Class]time.Duration{}
			}
			hp.Durations[c] = dur("/holds/durations/"+escapePointer(name), raw)
		}
		out.Holds = hp
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return out, nil
}

// classByName is Class.String()'s inverse.
func classByName(name string) (integrations.Class, bool) {
	for _, c := range classes() {
		if c.String() == name {
			return c, true
		}
	}
	return 0, false
}

// limitsView renders Limits for the wire; the zero value renders as nil.
func limitsView(l integrations.Limits) *IntegrationLimits {
	if reflect.ValueOf(l).IsZero() {
		return nil
	}
	out := &IntegrationLimits{
		RPS: l.RPS, Burst: l.Burst, MaxRetries: l.MaxRetries,
		Timeout: durString(l.Timeout), RetryAfterMax: durString(l.RetryAfterMax),
	}
	if d := l.Daily; d != nil {
		zone := ""
		if d.Zone != nil {
			zone = d.Zone.String()
		}
		out.Daily = &IntegrationDaily{Total: d.Total, Background: d.Background, Zone: zone, Counters: slices.Clone(d.Counters)}
	}
	if b := l.Breaker; b != (integrations.BreakerPolicy{}) {
		out.Breaker = &IntegrationBreaker{Threshold: b.Threshold, Cooldown: durString(b.Cooldown), ProbeTimeout: durString(b.ProbeTimeout)}
	}
	if h := l.Holds; h.Persist || h.Max != 0 || len(h.Durations) > 0 {
		hp := &IntegrationHoldPolicy{Max: durString(h.Max), Persist: h.Persist}
		for c, d := range h.Durations {
			if hp.Durations == nil {
				hp.Durations = map[string]string{}
			}
			hp.Durations[c.String()] = durString(d)
		}
		out.Holds = hp
	}
	return out
}

// durString is d as a duration string, "" for zero.
func durString(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return d.String()
}

// ---- views ----------------------------------------------------------------

// secretFieldsOf is the top-level settings properties m's schema marks
// x-mediacore-secret (the Registry requires a secret field to be one). An
// unreadable schema yields none, and instanceView then treats every value
// that is shaped like a secrets.Ref as one.
func secretFieldsOf(m integrations.Meta) map[string]bool {
	var doc struct {
		Properties map[string]struct {
			Secret bool `json:"x-mediacore-secret"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(m.Settings.JSON(), &doc); err != nil {
		return nil
	}
	out := map[string]bool{}
	for name, p := range doc.Properties {
		if p.Secret {
			out[name] = true
		}
	}
	return out
}

// isSecretShaped reports a value a settings map must never put on the wire:
// a secrets.Ref (or its decoded {"secret": name} form) or a secrets.Value.
func isSecretShaped(v any) bool {
	switch x := v.(type) {
	case secrets.Ref, *secrets.Ref, secrets.Value, *secrets.Value:
		return true
	case map[string]any:
		_, ok := x["secret"]
		return ok && len(x) == 1
	}
	return false
}

// instanceView renders cfg. secretFields is the adapter's secret fields, nil
// when the adapter is not registered in this binary (a row kept for another
// binary): its secret fields are then the ones holding a Ref.
//
// Whatever the schema says, no Ref-shaped or Value-shaped setting is
// answered: a secret leaves the server as a marker only.
func instanceView(cfg integrations.InstanceConfig, secretFields map[string]bool) IntegrationInstance {
	v := IntegrationInstance{
		Kind: string(cfg.Ref.Kind), ID: string(cfg.Ref.Instance), Adapter: string(cfg.Adapter),
		Name: cfg.Name, Enabled: cfg.Enabled, Priority: cfg.Priority, Origin: string(cfg.Origin), Local: cfg.Local,
		Settings: map[string]any{}, SecretsSet: map[string]bool{}, Version: cfg.Version, Updated: cfg.Updated,
		Locked: cfg.Locked, Stored: cfg.Stored,
	}
	for f := range secretFields {
		v.SecretsSet[f] = isSecretShaped(cfg.Settings[f])
	}
	for f, val := range cfg.Settings {
		switch {
		case secretFields[f]:
		case isSecretShaped(val):
			v.SecretsSet[f] = true
		default:
			v.Settings[f] = val
		}
	}
	if cfg.Limits != nil {
		v.Limits = limitsView(*cfg.Limits)
	}
	return v
}

// adapterView renders m.
func adapterView(m integrations.Meta) IntegrationAdapter {
	return IntegrationAdapter{
		ID: string(m.ID), Kind: string(m.Kind), DisplayName: m.DisplayName, Version: m.Version, InfoURL: m.InfoURL,
		SettingsSchema: m.Settings.JSON(), Egress: egressName(m.Egress), Limits: limitsView(m.Limits),
		MaxInstances: m.MaxInstances, Origin: m.Origin,
	}
}

// egressName is the wire name of an EgressPolicy.
func egressName(p integrations.EgressPolicy) string {
	switch p {
	case integrations.EgressConfigured:
		return "configured"
	case integrations.EgressPublicOnly:
		return "public_only"
	case integrations.EgressSameHostOrPublic:
		return "same_host_or_public"
	}
	return "unknown"
}

// breakerName is the wire name of a BreakerState.
func breakerName(s integrations.BreakerState) string {
	switch s {
	case integrations.BreakerClosed:
		return "closed"
	case integrations.BreakerHalfOpen:
		return "half_open"
	case integrations.BreakerOpen:
		return "open"
	}
	return "unknown"
}

// healthView renders h with its text bounded.
func healthView(h integrations.Health) IntegrationHealth {
	return IntegrationHealth{
		Status: h.Status.String(), Message: bound(h.Message, maxHealthMessage), RemoteVersion: bound(h.RemoteVersion, maxRemoteVersions),
		LatencyMS: h.Latency.Milliseconds(), CheckedAt: h.CheckedAt, Hold: holdView(h.Hold), Breaker: breakerName(h.Breaker),
	}
}

// bound is s as valid UTF-8 without control characters, cut to at most n
// bytes at a rune boundary.
func bound(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, "?"))
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
