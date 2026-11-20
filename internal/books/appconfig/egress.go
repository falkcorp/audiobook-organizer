// file: internal/books/appconfig/egress.go
// version: 1.0.0
// guid: 3e8a1c57-4d92-4f60-b7a3-9c5d0e2f8a14
// last-edited: 2026-11-19

package appconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	coreconfig "github.com/falkcorp/mediacore/config"
)

// Credentials follow their URL.
//
// A settings write can change where a service lives while keeping what it logs in
// with: the credential is stored apart from the URL, and a blank credential in a
// write means "keep the stored one". So a write that changes a source's host and
// says nothing of its credential sends the stored key to the new host, which is
// whoever the writer names. HEAD never checked; this is a rule beyond HEAD parity.
//
// The rule: a write that changes the origin (scheme, host, port) of a URL a stored
// credential travels to is refused (ErrInvalid, naming the URL field) unless the
// same write also replaces or deletes that credential. A change to the path, query
// or case of the same origin is not a change. A service with no stored credential
// has nothing to send, so its URL is free.
//
// egressRules lists every URL-valued row of the rename table that has a
// credential, and egressExempt every other URL-valued row with the reason it has
// none (TestEgress_EveryURLRowIsClassified keeps the two complete).

// egressExempt are the URL-valued rows no stored credential travels to.
var egressExempt = map[string]string{
	"otel_exporter_otlp_endpoint": "no credential is stored for the exporter (headers are not a setting)",
	"otel_metrics_otlp_endpoint":  "no credential is stored for the exporter (headers are not a setting)",
	"whisper_remote_url":          "the remote transcription server takes no credential",
	"oauth_redirect_base_url":     "where the sign-in returns to, not a host the client secret is sent to; admin-only",
	"public_base_url":             "this server's own address; operator-only (env or file)",
}

// egressSecretExempt are the class-s rows whose value is not sent to a host a
// write can choose (egressRules, egressProviders and the credential and
// webhook rules below cover the rest).
var egressSecretExempt = map[string]string{
	"acoustid_api_key":           "sent to the fixed AcoustID host; no base_url setting exists for it",
	"basic_auth_password":        "an inbound credential: the server checks it, never sends it",
	"oauth_github_client_secret": "sent to the provider's fixed token endpoint; the provider list is an admin row",
	"oauth_google_client_secret": "sent to the provider's fixed token endpoint; the provider list is an admin row",
	"abs_jwt_secret":             "signs the server's own tokens; never sent",
	"smtp_password":              "its host is an operator-only passthrough value (env or file), not a settings write",
	"cf_policy_api_token":        "sent to the fixed Cloudflare API host; no base_url setting exists for it",
}

// egressBoundSecrets are the class-s rows the rules above bind to a URL, besides
// the ones egressRules and egressProviders name.
var egressBoundSecrets = map[string]string{
	"download_client.torrent.deluge.password": "delugeRPCViolation (host and port)",
	"plugins.webhook.settings.secret":         "webhookViolation",
}

// egressProviders are the built-in metadata providers that send a global key (a
// secret row of its own, not a per-source credential) to the source's base URL.
// def is the origin the client uses when the source has no base_url; the
// effective base_url is resolved the way metadata.resolveBaseURL does (the first
// entry of that id with a non-empty base_url, else def).
var egressProviders = []struct {
	id, secret, def string
	stored          func(config.Config) bool
}{
	{"google-books", "google_books_api_key", "https://www.googleapis.com/books/v1", func(c config.Config) bool { return c.GoogleBooksAPIKey != "" }},
	{"hardcover", "hardcover_api_token", "https://api.hardcover.app/v1/graphql", func(c config.Config) bool { return c.HardcoverAPIToken != "" }},
}

// egressRule is one URL a credential travels to.
type egressRule struct {
	// url is the head of the URL row; the refusal names it.
	url string
	// secret is the head of the secret row the URL's host receives.
	secret string
	// origin is the origin the URL has in cfg (before the write).
	origin func(cfg config.Config) string
	// stored reports that the credential is stored in cfg.
	stored func(cfg config.Config) bool
	// openAIFallback: an empty URL stands for openai_base_url (which stands for
	// DefaultOpenAIBaseURL when empty), as the embedding client resolves it.
	openAIFallback bool
	// emptyIsDefault: an empty URL is DefaultOpenAIBaseURL (the SDK's default).
	emptyIsDefault bool
}

var egressRules = []egressRule{
	{"openai_base_url", "openai_api_key",
		func(c config.Config) string { return openAIOrigin("", c.OpenAIBaseURL) },
		func(c config.Config) bool { return c.OpenAIAPIKey != "" }, false, true},
	{"deluge_web_url", "deluge_web_password",
		func(c config.Config) string { return originOf(c.DelugeWebURL) },
		func(c config.Config) bool { return c.DelugeWebPassword != "" }, false, false},
	// internal/ai/register.go: in the openai embedding modes the OpenAI key goes
	// to embedding.base_url, or to openai_base_url when that is empty.
	{"embedding.base_url", "openai_api_key",
		func(c config.Config) string { return openAIOrigin(c.Embedding.BaseURL, c.OpenAIBaseURL) },
		func(c config.Config) bool { return c.OpenAIAPIKey != "" }, true, true},
}

// openAIOrigin is the origin the OpenAI key goes to when the embedding URL is
// own and the OpenAI base URL is base (each may be empty).
func openAIOrigin(own, base string) string {
	if strings.TrimSpace(own) != "" {
		return originOf(own)
	}
	if strings.TrimSpace(base) == "" {
		base = config.DefaultOpenAIBaseURL
	}
	return originOf(base)
}

// originOf is scheme://host:port of a URL, the host in lower case, the port
// explicit (443 for https, 80 for http), the userinfo, path, query and fragment
// dropped. Text that is not a URL with a host is its trimmed self, so any change
// to it is a change of origin.
func originOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return raw
	}
	scheme, host, port := strings.ToLower(u.Scheme), strings.ToLower(u.Hostname()), u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return scheme + "://" + host + ":" + port
}

// egressViolations are the refusals one Update earns, as field errors.
func egressViolations(before config.Config, u config.Update) []error {
	var out []error
	// A secret is supplied by a write that deletes it or sets a value other than
	// the stored one; writing back the value already stored changes nothing.
	stored := map[string]string{
		"openai_api_key":                          before.OpenAIAPIKey,
		"google_books_api_key":                    before.GoogleBooksAPIKey,
		"hardcover_api_token":                     before.HardcoverAPIToken,
		"deluge_web_password":                     before.DelugeWebPassword,
		"download_client.torrent.deluge.password": before.DownloadClient.Torrent.Deluge.Password,
	}
	supplied := func(head string) bool {
		if slices.Contains(u.Unset, head) {
			return true
		}
		raw, set := u.Set[head]
		if !set {
			return false
		}
		var v string
		return json.Unmarshal(raw, &v) != nil || v != stored[head]
	}
	for _, r := range egressRules {
		raw, touched := u.Set[r.url]
		if !touched && !slices.Contains(u.Unset, r.url) {
			continue
		}
		var next string
		if touched {
			_ = json.Unmarshal(raw, &next) // a value that is not a string is refused by the field check
		}
		nextOrigin := originOf(next)
		if r.emptyIsDefault && !r.openAIFallback {
			nextOrigin = openAIOrigin("", next)
		}
		if r.openAIFallback {
			base := before.OpenAIBaseURL
			if raw, ok := u.Set["openai_base_url"]; ok {
				_ = json.Unmarshal(raw, &base)
			} else if slices.Contains(u.Unset, "openai_base_url") {
				base = ""
			}
			nextOrigin = openAIOrigin(next, base)
		}
		if nextOrigin == r.origin(before) || !r.stored(before) || supplied(r.secret) {
			continue
		}
		out = append(out, egressError(r.url, r.secret))
	}
	out = append(out, delugeRPCViolation(before, u, supplied)...)
	out = append(out, metadataViolations(before, u)...)
	out = append(out, providerKeyViolations(before, u, supplied)...)
	out = append(out, aiEndpointViolations(before, u, supplied)...)
	out = append(out, webhookViolation(before, u)...)
	return out
}

func egressError(urlHead, what string) error {
	return &coreconfig.FieldError{Path: coreconfig.Path(urlHead), Err: fmt.Errorf(
		"this changes the host the stored %s is sent to; set a new %s in the same update (or clear it) first", what, what)}
}

// delugeRPCViolation is the rule for the Deluge RPC host and port. The password
// is a secret row of its own, so only a write that names the password row
// replaces it. (download_client itself is a container row: only its sub-key rows
// are writable.)
func delugeRPCViolation(before config.Config, u config.Update, supplied func(string) bool) []error {
	const host, port, pass = "download_client.torrent.deluge.host", "download_client.torrent.deluge.port", "download_client.torrent.deluge.password"
	d := before.DownloadClient.Torrent.Deluge
	next, touched := d, ""
	if raw, ok := u.Set[host]; ok {
		_ = json.Unmarshal(raw, &next.Host)
		touched = host
	} else if slices.Contains(u.Unset, host) {
		next.Host, touched = "", host
	}
	if raw, ok := u.Set[port]; ok {
		_ = json.Unmarshal(raw, &next.Port)
		if touched == "" {
			touched = port
		}
	} else if slices.Contains(u.Unset, port) {
		next.Port = 0
		if touched == "" {
			touched = port
		}
	}
	sameOrigin := strings.EqualFold(strings.TrimSpace(next.Host), strings.TrimSpace(d.Host)) && next.Port == d.Port
	if touched == "" || d.Password == "" || sameOrigin || supplied(pass) {
		return nil
	}
	return []error{egressError(touched, "Deluge password")}
}

// metadataViolations: a metadata source whose base_url changes origin must
// replace or delete every credential stored for it. A credential the write does
// not name, names blank or names as the value already stored is kept, and a kept
// credential would travel to the new host.
func metadataViolations(before config.Config, u config.Update) []error {
	raw, ok := u.Set["metadata_sources"]
	if !ok {
		return nil
	}
	var next []struct {
		ID          string             `json:"id"`
		BaseURL     string             `json:"base_url"`
		Credentials map[string]*string `json:"credentials"`
	}
	if json.Unmarshal(raw, &next) != nil {
		return nil // not a list of sources: the field check says so
	}
	var out []error
	for _, n := range next {
		for _, b := range before.MetadataSources {
			if b.ID != n.ID || n.ID == "" || len(b.Credentials) == 0 || originOf(n.BaseURL) == originOf(b.BaseURL) {
				continue
			}
			var kept []string
			for k, stored := range b.Credentials {
				if stored == "" {
					continue
				}
				v, named := n.Credentials[k]
				replaced := named && (v == nil || (*v != "" && *v != stored))
				if !replaced {
					kept = append(kept, k)
				}
			}
			if len(kept) > 0 {
				slices.Sort(kept)
				out = append(out, &coreconfig.FieldError{Path: "metadata_sources", Err: errors.New(
					"source " + strconv.Quote(n.ID) + ": this changes the host its stored credentials (" + strings.Join(kept, ", ") +
						") are sent to; replace or clear them in the same update first")})
			}
		}
	}
	return out
}

// providerKeyViolations: a write that gives a built-in provider an effective
// base_url on a different origin than it had (its previous entry's, or the
// provider's default when it had none) is refused while the provider's global
// key is stored, unless the same write replaces or deletes that key. It covers a
// changed base_url, a new entry for the provider (the effective origin moves from
// the default to the entry's) and a re-added one (removing the entry first leaves
// the default as the origin to compare with).
func providerKeyViolations(before config.Config, u config.Update, supplied func(string) bool) []error {
	raw, ok := u.Set["metadata_sources"]
	if !ok {
		return nil
	}
	var next []struct {
		ID      string `json:"id"`
		BaseURL string `json:"base_url"`
	}
	if json.Unmarshal(raw, &next) != nil {
		return nil
	}
	var out []error
	for _, p := range egressProviders {
		present, nextBase := false, ""
		for _, n := range next {
			if n.ID != p.id {
				continue
			}
			present = true
			if nextBase == "" {
				nextBase = n.BaseURL
			}
		}
		beforeBase := ""
		for _, b := range before.MetadataSources {
			if b.ID == p.id && b.BaseURL != "" {
				beforeBase = b.BaseURL
				break
			}
		}
		if !present || !p.stored(before) || supplied(p.secret) {
			continue
		}
		if nextBase == "" {
			nextBase = p.def
		}
		if beforeBase == "" {
			beforeBase = p.def
		}
		if originOf(nextBase) == originOf(beforeBase) {
			continue
		}
		out = append(out, &coreconfig.FieldError{Path: "metadata_sources", Err: errors.New(
			"source " + strconv.Quote(p.id) + ": this moves it to " + originOf(nextBase) + ", where the stored " + p.secret +
				" would be sent; set a new " + p.secret + " in the same update (or clear it) first")})
	}
	return out
}

// aiEndpointViolations: an AI endpoint names its credential by AuthRef and the
// secret goes to the endpoint's URL. A written endpoint that references a stored
// secret must point at an origin that secret already went to: the origin of an
// endpoint that referenced it, or the openai_base_url (default
// DefaultOpenAIBaseURL) for openai_api_key. Anything else needs the secret
// replaced or deleted in the same update. Refs with no stored secret send nothing.
func aiEndpointViolations(before config.Config, u config.Update, supplied func(string) bool) []error {
	raw, ok := u.Set["ai_endpoints"]
	if !ok {
		return nil
	}
	var next []struct {
		ID      string `json:"id"`
		URL     string `json:"url"`
		AuthRef string `json:"auth_ref"`
	}
	if json.Unmarshal(raw, &next) != nil {
		return nil
	}
	storedSecret := func(ref string) bool {
		return ref == "openai_api_key" && before.OpenAIAPIKey != ""
	}
	known := func(ref string) map[string]bool {
		m := map[string]bool{}
		for _, b := range before.AIEndpoints {
			if b.AuthRef == ref {
				m[originOf(b.URL)] = true
			}
		}
		if ref == "openai_api_key" {
			base := before.OpenAIBaseURL
			if strings.TrimSpace(base) == "" {
				base = config.DefaultOpenAIBaseURL
			}
			m[originOf(base)] = true
		}
		return m
	}
	var out []error
	for _, n := range next {
		if n.AuthRef == "" || !storedSecret(n.AuthRef) || supplied(n.AuthRef) || known(n.AuthRef)[originOf(n.URL)] {
			continue
		}
		out = append(out, &coreconfig.FieldError{Path: "ai_endpoints", Err: errors.New(
			"endpoint " + strconv.Quote(n.ID) + ": this sends the stored " + n.AuthRef + " to a host it was not sent to before (" +
				originOf(n.URL) + "); set a new " + n.AuthRef + " in the same update (or clear the reference) first")})
	}
	return out
}

// webhookViolation: the webhook plugin sends one secret to every URL it lists, so
// a URL whose origin the plugin did not already send to needs the secret replaced
// or deleted in the same update.
func webhookViolation(before config.Config, u config.Update) []error {
	raw, ok := u.Set["plugins"]
	if !ok {
		return nil
	}
	var next map[string]struct {
		Settings map[string]*string `json:"settings"`
	}
	if json.Unmarshal(raw, &next) != nil {
		return nil
	}
	nw, ok := next["webhook"]
	if !ok {
		return nil
	}
	old := before.Plugins["webhook"].Settings
	stored := old["secret"]
	if stored == "" {
		return nil
	}
	known := map[string]bool{}
	for _, one := range strings.Split(old["urls"], ",") {
		if one = strings.TrimSpace(one); one != "" {
			known[originOf(one)] = true
		}
	}
	var fresh []string
	if p := nw.Settings["urls"]; p != nil {
		for _, one := range strings.Split(*p, ",") {
			if one = strings.TrimSpace(one); one != "" && !known[originOf(one)] {
				fresh = append(fresh, originOf(one))
			}
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	s, named := nw.Settings["secret"]
	if named && (s == nil || (*s != "" && *s != stored)) {
		return nil
	}
	slices.Sort(fresh)
	return []error{&coreconfig.FieldError{Path: "plugins", Err: errors.New(
		"the webhook secret would be sent to a new host (" + strings.Join(slices.Compact(fresh), ", ") +
			"); replace or clear the secret in the same update first")}}
}
