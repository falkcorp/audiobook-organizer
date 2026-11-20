// file: internal/books/appconfig/egress_test.go
// version: 1.1.0
// guid: 8b14e6d2-5a73-4c19-90f8-2d7c3a61e5b0
// last-edited: 2026-11-19

package appconfig

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	coreconfig "github.com/falkcorp/mediacore/config"
)

const (
	openAIKeyName  = "integrations.ai.openai.api_key"
	delugePassName = "integrations.download.deluge.daemon_password"
	googleKeyName  = "integrations.metadata.google-books.api_key"
	hardcoverName  = "integrations.metadata.hardcover.api_token"
)

// egressRig is an Engine with one stored credential beside each URL it travels to.
func egressRig(t *testing.T) (*Engine, *fakeAccess, *fakeResidual) {
	t.Helper()
	f := newFake()
	f.secrets[openAIKeyName] = "harley"
	f.secrets[delugePassName] = "quinn"
	f.secrets[googleKeyName] = "selina"
	f.secrets[hardcoverName] = "bane"
	res := newFakeResidual()
	res.vals["openai_base_url"] = json.RawMessage(`"https://llm.example.test:8443/v1"`)
	res.vals["download_client.torrent.deluge.host"] = json.RawMessage(`"arkham.example.test"`)
	res.vals["download_client.torrent.deluge.port"] = json.RawMessage(`58846`)
	res.vals["metadata_sources"] = json.RawMessage(`[{"id":"gotham-books","name":"Gotham Books","enabled":true,"base_url":"https://books.example.test/api","credentials":{"api_key":"pengu1n"}},{"id":"arkham","name":"Arkham","enabled":true,"credentials":{}},{"id":"narrows","enabled":true,"base_url":"https://narrows.example.test","credentials":{"api_key":""}},{"id":"hardcover","enabled":true,"base_url":"https://graphql.example.test/v1"}]`)
	res.vals["plugins"] = json.RawMessage(`{"webhook":{"enabled":true,"settings":{"urls":"https://hooks.example.test/a, https://hooks.example.test/b","secret":"riddle"}}}`)
	res.vals["ai_endpoints"] = json.RawMessage(`[{"id":"openai","url":"https://llm.example.test:8443/v1","auth_ref":"openai_api_key"},{"id":"local","url":"http://192.0.2.9:11434"}]`)
	e := New(f, nil, WithResidual(res))
	if err := e.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	return e, f, res
}

// egressRefused asserts the update is an ErrInvalid naming field, and that
// nothing was written.
func egressRefused(t *testing.T, name string, u config.Update, field string) {
	t.Helper()
	e, f, res := egressRig(t)
	applied, gen := len(f.applied), f.Generation()
	_, err := e.Update(t.Context(), adminActor, u)
	if !errors.Is(err, coreconfig.ErrInvalid) || !contains(err, field) {
		t.Fatalf("%s: err = %v; want ErrInvalid naming %q", name, err, field)
	}
	if res.saves != 0 || len(f.puts) != 0 || len(f.dels) != 0 || len(f.applied) != applied || f.Generation() != gen {
		t.Fatalf("%s: a refused write changed something: saves=%d puts=%v dels=%v", name, res.saves, f.puts, f.dels)
	}
}

func egressAccepted(t *testing.T, name string, u config.Update) (*fakeAccess, *fakeResidual) {
	t.Helper()
	e, f, res := egressRig(t)
	if _, err := e.Update(t.Context(), adminActor, u); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return f, res
}

// TestEgress_HostChangeWithoutNewCredentialIsRefused: every URL a stored
// credential travels to, when its host changes and the write does not replace the
// credential.
func TestEgress_HostChangeWithoutNewCredentialIsRefused(t *testing.T) {
	cases := []struct {
		name  string
		u     config.Update
		field string
	}{
		{"openai host", set("openai_base_url", `"https://evil.example.test/v1"`), "openai_base_url"},
		{"openai cleared", config.Update{Unset: []string{"openai_base_url"}}, "openai_base_url"},
		{"embedding base_url set", set("embedding.base_url", `"https://evil.example.test/v1"`), "embedding.base_url"},
		{"embedding base_url with the stored key written back", set("embedding.base_url", `"https://evil.example.test/v1"`, "openai_api_key", `"harley"`), "embedding.base_url"},
		{"openai port", set("openai_base_url", `"https://llm.example.test:9443/v1"`), "openai_base_url"},
		{"openai scheme", set("openai_base_url", `"http://llm.example.test:8443/v1"`), "openai_base_url"},
		{"deluge host row", set("download_client.torrent.deluge.host", `"192.0.2.66"`), "download_client.torrent.deluge.host"},
		{"deluge port row", set("download_client.torrent.deluge.port", `1`), "download_client.torrent.deluge.port"},
		{"deluge same password resent", set("download_client.torrent.deluge.host", `"192.0.2.66"`, "download_client.torrent.deluge.password", `"quinn"`), "download_client.torrent.deluge.host"},
		{"metadata host", set("metadata_sources", `[{"id":"gotham-books","base_url":"https://evil.example.test/api","credentials":{"api_key":""}}]`), "metadata_sources"},
		{"metadata host, credentials omitted", set("metadata_sources", `[{"id":"gotham-books","base_url":"https://evil.example.test/api"}]`), "metadata_sources"},
		{"metadata host, same credential resent", set("metadata_sources", `[{"id":"gotham-books","base_url":"https://evil.example.test/api","credentials":{"api_key":"pengu1n"}}]`), "metadata_sources"},
		{"metadata base_url removed", set("metadata_sources", `[{"id":"gotham-books","credentials":{}}]`), "metadata_sources"},
		{"deluge host unset", config.Update{Unset: []string{"download_client.torrent.deluge.host"}}, "download_client.torrent.deluge.host"},
		{"deluge port unset", config.Update{Unset: []string{"download_client.torrent.deluge.port"}}, "download_client.torrent.deluge.port"},
		{"webhook new host, same secret resent", set("plugins", `{"webhook":{"enabled":true,"settings":{"urls":"https://evil.example.test/x","secret":"riddle"}}}`), "plugins"},
		{"google books: new entry on a new host", set("metadata_sources", `[{"id":"gotham-books","base_url":"https://books.example.test/api","credentials":{"api_key":""}},{"id":"google-books","enabled":true,"base_url":"https://evil.example.test/v1"}]`), "metadata_sources"},
		{"google books: first entry on a new host", set("metadata_sources", `[{"id":"google-books","base_url":"https://evil.example.test/v1"}]`), "metadata_sources"},
		{"google books: first of two entries decides", set("metadata_sources", `[{"id":"google-books","base_url":"https://evil.example.test/v1"},{"id":"google-books"}]`), "metadata_sources"},
		{"hardcover: base_url moved", set("metadata_sources", `[{"id":"hardcover","base_url":"https://evil.example.test/graphql"}]`), "metadata_sources"},
		{"hardcover: base_url removed moves it to the default", set("metadata_sources", `[{"id":"hardcover"}]`), "metadata_sources"},
		{"webhook new host", set("plugins", `{"webhook":{"enabled":true,"settings":{"urls":"https://hooks.example.test/a, https://evil.example.test/x"}}}`), "plugins"},
		{"webhook new host, blank secret", set("plugins", `{"webhook":{"enabled":true,"settings":{"urls":"https://evil.example.test/x","secret":""}}}`), "plugins"},
		{"ai endpoint host", set("ai_endpoints", `[{"id":"openai","url":"https://evil.example.test/v1","auth_ref":"openai_api_key"}]`), "ai_endpoints"},
		{"ai endpoint new row with the ref", set("ai_endpoints", `[{"id":"mine","url":"https://evil.example.test/v1","auth_ref":"openai_api_key"}]`), "ai_endpoints"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { egressRefused(t, c.name, c.u, c.field) })
	}
}

// TestEgress_HostChangeWithNewCredentialIsAccepted: the same writes, when they
// replace or delete the credential in the same update.
func TestEgress_HostChangeWithNewCredentialIsAccepted(t *testing.T) {
	cases := []struct {
		name string
		u    config.Update
		want func(t *testing.T, f *fakeAccess, res *fakeResidual)
	}{
		{"google books: new host with a new key", set("metadata_sources", `[{"id":"google-books","base_url":"https://evil.example.test/v1"}]`, "google_books_api_key", `"oswald"`),
			func(t *testing.T, f *fakeAccess, _ *fakeResidual) {
				if f.secrets[googleKeyName] != "oswald" {
					t.Fatalf("key = %q", f.secrets[googleKeyName])
				}
			}},
		{"hardcover: new host with the token deleted", config.Update{Set: map[string]json.RawMessage{"metadata_sources": json.RawMessage(`[{"id":"hardcover","base_url":"https://evil.example.test/graphql"}]`)}, Unset: []string{"hardcover_api_token"}},
			func(t *testing.T, f *fakeAccess, _ *fakeResidual) {
				if _, kept := f.secrets[hardcoverName]; kept {
					t.Fatal("the token survived its deletion")
				}
			}},
		{"openai new key", set("openai_base_url", `"https://llm2.example.test/v1"`, "openai_api_key", `"oswald"`),
			func(t *testing.T, f *fakeAccess, res *fakeResidual) {
				if f.secrets[openAIKeyName] != "oswald" || string(res.vals["openai_base_url"]) != `"https://llm2.example.test/v1"` {
					t.Fatalf("key=%q url=%s", f.secrets[openAIKeyName], res.vals["openai_base_url"])
				}
			}},
		{"openai key deleted", config.Update{Set: map[string]json.RawMessage{"openai_base_url": json.RawMessage(`"https://llm2.example.test/v1"`)}, Unset: []string{"openai_api_key"}},
			func(t *testing.T, f *fakeAccess, _ *fakeResidual) {
				if _, kept := f.secrets[openAIKeyName]; kept {
					t.Fatal("the key survived its deletion")
				}
			}},
		{"deluge new password", set("download_client.torrent.deluge.host", `"192.0.2.66"`, "download_client.torrent.deluge.password", `"harvey"`),
			func(t *testing.T, f *fakeAccess, _ *fakeResidual) {
				if f.secrets[delugePassName] != "harvey" {
					t.Fatalf("password = %q", f.secrets[delugePassName])
				}
			}},
		{"metadata new credential", set("metadata_sources", `[{"id":"gotham-books","base_url":"https://books2.example.test/api","credentials":{"api_key":"two-face"}}]`),
			func(t *testing.T, _ *fakeAccess, res *fakeResidual) {
				if !strings.Contains(string(res.vals["metadata_sources"]), "books2.example.test") {
					t.Fatalf("not written: %s", res.vals["metadata_sources"])
				}
			}},
		{"metadata credential deleted", set("metadata_sources", `[{"id":"gotham-books","base_url":"https://books2.example.test/api","credentials":{"api_key":null}}]`),
			func(t *testing.T, _ *fakeAccess, res *fakeResidual) {
				if !strings.Contains(string(res.vals["metadata_sources"]), "books2.example.test") {
					t.Fatalf("not written: %s", res.vals["metadata_sources"])
				}
			}},
		{"webhook new secret", set("plugins", `{"webhook":{"enabled":true,"settings":{"urls":"https://evil.example.test/x","secret":"scarecrow"}}}`),
			func(t *testing.T, _ *fakeAccess, res *fakeResidual) {
				if !strings.Contains(string(res.vals["plugins"]), "evil.example.test") {
					t.Fatalf("not written: %s", res.vals["plugins"])
				}
			}},
		{"ai endpoint with a new key", set("ai_endpoints", `[{"id":"openai","url":"https://llm2.example.test/v1","auth_ref":"openai_api_key"}]`, "openai_api_key", `"oswald"`),
			func(t *testing.T, f *fakeAccess, _ *fakeResidual) {
				if f.secrets[openAIKeyName] != "oswald" {
					t.Fatalf("key = %q", f.secrets[openAIKeyName])
				}
			}},
		{"ai endpoint drops the ref", set("ai_endpoints", `[{"id":"openai","url":"https://evil.example.test/v1"}]`),
			func(t *testing.T, _ *fakeAccess, res *fakeResidual) {
				if !strings.Contains(string(res.vals["ai_endpoints"]), "evil.example.test") {
					t.Fatalf("not written: %s", res.vals["ai_endpoints"])
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, res := egressAccepted(t, c.name, c.u)
			c.want(t, f, res)
		})
	}
}

// TestEgress_SameHostKeepsTheCredential: a change that leaves scheme, host and
// port alone (path, query, userinfo, host case, the default port written out) is
// not a change of where the credential goes, and a service without a stored
// credential has nothing to send.
func TestEgress_SameHostKeepsTheCredential(t *testing.T) {
	cases := []struct {
		name string
		u    config.Update
	}{
		{"embedding base_url same origin as openai", set("embedding.base_url", `"https://llm.example.test:8443/embed"`)},
		{"openai base_url unchanged", set("openai_base_url", `"https://llm.example.test:8443/v1"`)},
		{"openai path", set("openai_base_url", `"https://llm.example.test:8443/v2/other?x=1"`)},
		{"openai host case", set("openai_base_url", `"https://LLM.Example.Test:8443/v1"`)},
		{"deluge host trimmed", set("download_client.torrent.deluge.host", `"arkham.example.test "`)},
		{"deluge host case-insensitive", set("download_client.torrent.deluge.host", `"ARKHAM.Example.Test"`)},
		{"google books: entry left on the default host", set("metadata_sources", `[{"id":"google-books","enabled":true},{"id":"google-books","base_url":"https://www.googleapis.com/books/v1/other"}]`)},
		{"hardcover: same origin", set("metadata_sources", `[{"id":"hardcover","base_url":"https://GRAPHQL.example.test:443/other"}]`)},
		{"metadata source with an empty stored credential moves freely", set("metadata_sources", `[{"id":"narrows","base_url":"https://moved.example.test","credentials":{"api_key":""}}]`)},
		{"metadata path", set("metadata_sources", `[{"id":"gotham-books","base_url":"https://books.example.test/api/v2","credentials":{"api_key":""}},{"id":"arkham","enabled":true}]`)},
		{"metadata default port spelled out", set("metadata_sources", `[{"id":"gotham-books","base_url":"https://books.example.test:443/api"}]`)},
		{"metadata source without credentials moves freely", set("metadata_sources", `[{"id":"gotham-books","base_url":"https://books.example.test/api"},{"id":"arkham","base_url":"https://anywhere.example.test"}]`)},
		{"metadata new source", set("metadata_sources", `[{"id":"gotham-books","base_url":"https://books.example.test/api"},{"id":"narrows","base_url":"https://elsewhere.example.test"}]`)},
		{"webhook known hosts, other path", set("plugins", `{"webhook":{"enabled":true,"settings":{"urls":"https://hooks.example.test/c","secret":""}}}`)},
		{"ai endpoint, same origin", set("ai_endpoints", `[{"id":"openai","url":"https://llm.example.test:8443/other","auth_ref":"openai_api_key"}]`)},
		{"ai endpoint without a ref moves freely", set("ai_endpoints", `[{"id":"local","url":"http://192.0.2.77:11434"}]`)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { egressAccepted(t, c.name, c.u) })
	}
	t.Run("nothing stored, so nothing to send", func(t *testing.T) {
		f := newFake()
		res := newFakeResidual()
		res.vals["openai_base_url"] = json.RawMessage(`"https://llm.example.test/v1"`)
		res.vals["download_client.torrent.deluge.host"] = json.RawMessage(`"192.0.2.20"`)
		res.vals["metadata_sources"] = json.RawMessage(`[{"id":"gotham-books","base_url":"https://books.example.test","credentials":{}}]`)
		res.vals["plugins"] = json.RawMessage(`{"webhook":{"enabled":true,"settings":{"urls":"https://hooks.example.test/a"}}}`)
		e := New(f, nil, WithResidual(res))
		if err := e.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		_, err := e.Update(t.Context(), adminActor, set(
			"openai_base_url", `"https://other.example.test"`,
			"download_client.torrent.deluge.host", `"192.0.2.66"`,
			"metadata_sources", `[{"id":"gotham-books","base_url":"https://other.example.test"}]`,
			"plugins", `{"webhook":{"enabled":true,"settings":{"urls":"https://other.example.test/a"}}}`))
		if err != nil {
			t.Fatal(err)
		}
	})
}

// TestEgress_ProviderWithoutAGlobalKey: no global key stored, nothing to send.
func TestEgress_ProviderWithoutAGlobalKey(t *testing.T) {
	f := newFake()
	e := New(f, nil, WithResidual(newFakeResidual()))
	if err := e.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Update(t.Context(), adminActor, set("metadata_sources", `[{"id":"google-books","base_url":"https://anywhere.example.test"},{"id":"hardcover","base_url":"https://anywhere.example.test"}]`)); err != nil {
		t.Fatal(err)
	}
}

// TestEgress_ReAddedProviderIsComparedWithTheDefault: remove the entry (allowed:
// nothing is sent to a provider that is not listed), then add it back on another
// host: the origin to compare with is the default again.
func TestEgress_ReAddedProviderIsComparedWithTheDefault(t *testing.T) {
	e, _, _ := egressRig(t)
	if _, err := e.Update(t.Context(), adminActor, set("metadata_sources", `[{"id":"gotham-books","base_url":"https://books.example.test/api","credentials":{"api_key":""}}]`)); err != nil {
		t.Fatalf("removing the entry: %v", err)
	}
	_, err := e.Update(t.Context(), adminActor, set("metadata_sources", `[{"id":"gotham-books","base_url":"https://books.example.test/api","credentials":{"api_key":""}},{"id":"hardcover","base_url":"https://evil.example.test/graphql"}]`))
	if !errors.Is(err, coreconfig.ErrInvalid) || !contains(err, "hardcover") {
		t.Fatalf("re-adding on a new host: err = %v", err)
	}
}

// TestEgress_EmbeddingFollowsOpenAIBase: an empty embedding.base_url is the
// openai_base_url (the default host when that is empty too), so setting it to the
// host it already resolves to is free, and setting it elsewhere is not.
func TestEgress_EmbeddingFollowsOpenAIBase(t *testing.T) {
	f := newFake()
	f.secrets[openAIKeyName] = "harley"
	e := New(f, nil, WithResidual(newFakeResidual()))
	if err := e.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Update(t.Context(), adminActor, set("openai_base_url", `"https://api.openai.com/v1/"`)); err != nil {
		t.Fatalf("openai_base_url: the default host, spelled out: %v", err)
	}
	if _, err := e.Update(t.Context(), adminActor, set("embedding.base_url", `"https://api.openai.com/v1"`)); err != nil {
		t.Fatalf("the default host, spelled out: %v", err)
	}
	if _, err := e.Update(t.Context(), adminActor, set("embedding.base_url", `"https://evil.example.test"`)); !errors.Is(err, coreconfig.ErrInvalid) {
		t.Fatalf("elsewhere: err = %v", err)
	}
	// Clearing it falls back to openai_base_url, which is the default: a move.
	if _, err := e.Update(t.Context(), adminActor, config.Update{Unset: []string{"embedding.base_url"}}); err != nil {
		t.Fatalf("back to the default host: %v", err)
	}
}

// TestEgress_ProviderBeforeBaseSkipsEmptyEntries: the origin a provider had is the
// first entry's non-empty base_url (metadata.resolveBaseURL), so a list that keeps
// that host is not a move even when an earlier entry of the id has none.
func TestEgress_ProviderBeforeBaseSkipsEmptyEntries(t *testing.T) {
	f := newFake()
	f.secrets[googleKeyName] = "selina"
	res := newFakeResidual()
	res.vals["metadata_sources"] = json.RawMessage(`[{"id":"google-books","enabled":true},{"id":"google-books","enabled":true,"base_url":"https://proxy.example.test/v1"}]`)
	e := New(f, nil, WithResidual(res))
	if err := e.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Update(t.Context(), adminActor, set("metadata_sources", `[{"id":"google-books","base_url":"https://proxy.example.test/v1"}]`)); err != nil {
		t.Fatalf("keeping the host the provider already used: %v", err)
	}
}

// TestEgress_EmbeddingSeesTheSameUpdatesOpenAIBase: clearing embedding.base_url in
// the update that also sets openai_base_url to the host the embedding client
// already used moves nothing for the embedding row; only openai_base_url is
// refused (its key was not replaced).
func TestEgress_EmbeddingSeesTheSameUpdatesOpenAIBase(t *testing.T) {
	e, _, _ := egressRig(t)
	if _, err := e.Update(t.Context(), adminActor, set("embedding.base_url", `"https://old-embed.example.test"`, "openai_api_key", `"oswald"`)); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := e.Update(t.Context(), adminActor, set("embedding.base_url", `""`, "openai_base_url", `"https://old-embed.example.test"`))
	if !errors.Is(err, coreconfig.ErrInvalid) || !contains(err, "openai_base_url") || contains(err, "embedding.base_url") {
		t.Fatalf("err = %v; want the openai_base_url move only", err)
	}
}

// TestEgress_DefaultOpenAIHost: with openai_base_url unset the key goes to the
// default OpenAI host, so an endpoint there may carry the reference.
func TestEgress_DefaultOpenAIHost(t *testing.T) {
	f := newFake()
	f.secrets[openAIKeyName] = "harley"
	res := newFakeResidual()
	e := New(f, nil, WithResidual(res))
	if err := e.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Update(t.Context(), adminActor, set("ai_endpoints", `[{"id":"openai","url":"https://api.openai.com/v1","auth_ref":"openai_api_key"}]`)); err != nil {
		t.Fatalf("an endpoint on the default host: %v", err)
	}
	if _, err := e.Update(t.Context(), adminActor, set("ai_endpoints", `[{"id":"openai","url":"https://evil.example.test/v1","auth_ref":"openai_api_key"}]`)); !errors.Is(err, coreconfig.ErrInvalid) {
		t.Fatalf("an endpoint elsewhere: err = %v", err)
	}
}

// TestEgress_OneRefusalListsEveryOffender: one update that breaks the rule twice
// is refused once, naming both fields.
func TestEgress_OneRefusalListsEveryOffender(t *testing.T) {
	e, _, _ := egressRig(t)
	_, err := e.Update(t.Context(), adminActor, set(
		"openai_base_url", `"https://evil.example.test/v1"`,
		"download_client.torrent.deluge.host", `"192.0.2.66"`))
	if !contains(err, "openai_base_url") || !contains(err, "download_client.torrent.deluge.host") {
		t.Fatalf("err = %v; want both fields named", err)
	}
}

// TestEgress_OriginOf pins the comparison the rule rests on.
func TestEgress_OriginOf(t *testing.T) {
	for in, want := range map[string]string{
		"":                                   "",
		"  ":                                 "",
		"https://Books.Example.Test/api?x=1": "https://books.example.test:443",
		"https://books.example.test:443/":    "https://books.example.test:443",
		"http://books.example.test":          "http://books.example.test:80",
		"http://books.example.test:8080/a/b": "http://books.example.test:8080",
		"https://u:p@books.example.test/api": "https://books.example.test:443",
		"https://[2001:db8::1]:9443/x":       "https://2001:db8::1:9443",
		"local:uv-openai-whisper":            "local:uv-openai-whisper",
		"not a url":                          "not a url",
		"ftp://files.example.test/x":         "ftp://files.example.test:",
	} {
		if got := originOf(in); got != want {
			t.Errorf("originOf(%q) = %q, want %q", in, got, want)
		}
	}
	if originOf("https://a.example.test") == originOf("http://a.example.test") || originOf("https://a.example.test") == originOf("https://a.example.test:444") ||
		originOf("https://a.example.test") == originOf("https://b.example.test") {
		t.Fatal("scheme, port and host must each change the origin")
	}
}

// TestEgress_EveryCredentialRowIsClassified: every class-s row is bound to the
// URL it travels to by a rule, or exempt with the reason no write can choose
// where it goes.
func TestEgress_EveryCredentialRowIsClassified(t *testing.T) {
	bound := map[string]bool{}
	for _, r := range egressRules {
		bound[r.secret] = true
	}
	for _, p := range egressProviders {
		bound[p.secret] = true
	}
	for h := range egressBoundSecrets {
		bound[h] = true
	}
	for _, r := range cutover.Table().Rows {
		if r.Class != cutover.ClassSecret || strings.ContainsAny(r.Head, "[]<>") {
			continue
		}
		_, exempt := egressSecretExempt[r.Head]
		switch {
		case bound[r.Head] && exempt:
			t.Errorf("%s is both bound and exempt", r.Head)
		case !bound[r.Head] && !exempt:
			t.Errorf("%s is a credential with no egress rule and no exemption: where does it get sent?", r.Head)
		}
	}
	for h := range egressSecretExempt {
		if _, ok := cutover.Table().Row(h); !ok {
			t.Errorf("exemption for %s names no row", h)
		}
	}
	for _, p := range egressProviders {
		if _, ok := cutover.Table().Row(p.secret); !ok {
			t.Errorf("provider %s names the row %s, which the table lacks", p.id, p.secret)
		}
	}
}

// TestEgress_EveryURLRowIsClassified keeps the rule complete as the table grows:
// a URL-valued row is either covered by a rule or exempt with a reason.
func TestEgress_EveryURLRowIsClassified(t *testing.T) {
	covered := map[string]bool{"download_client": true, "metadata_sources": true, "plugins": true, "ai_endpoints": true}
	for _, r := range egressRules {
		covered[r.url] = true
	}
	for _, r := range cutover.Table().Rows {
		if !r.URL || covered[r.Head] {
			continue
		}
		if why := egressExempt[r.Head]; why == "" {
			t.Errorf("%s is a URL-valued row with no egress rule and no exemption: does a stored credential travel to it?", r.Head)
		}
	}
	for head := range egressExempt {
		if covered[head] {
			t.Errorf("%s is both covered and exempt", head)
		}
	}
	for _, h := range slices.Sorted(maps.Keys(egressExempt)) {
		found := false
		for _, r := range cutover.Table().Rows {
			found = found || r.Head == h
		}
		if !found {
			t.Errorf("exemption for %s names no row", h)
		}
	}
}
