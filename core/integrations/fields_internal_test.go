// file: core/integrations/fields_internal_test.go
// version: 1.5.0
// guid: dfaab137-5259-4293-befc-1800491e6971
// last-edited: 2026-11-11

package integrations

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
)

// TestOriginOf pins the origin rendering the destination rules compare.
func TestOriginOf(t *testing.T) {
	for raw, want := range map[string]string{
		"http://192.0.2.20/":          "http://192.0.2.20:80",
		"HTTP://Gotham.Example/a?b=c": "http://gotham.example:80",
		"https://192.0.2.20":          "https://192.0.2.20:443",
		"https://192.0.2.20:8443/x":   "https://192.0.2.20:8443",
		"ws://192.0.2.1/":             "ws://192.0.2.1:80",
		"wss://192.0.2.1/":            "wss://192.0.2.1:443",
		"smtp://192.0.2.2":            "smtp://192.0.2.2:25",
		"smtps://192.0.2.2":           "smtps://192.0.2.2:465",
		"ftp://192.0.2.3":             "ftp://192.0.2.3:21",
		"gopher://192.0.2.4":          "gopher://192.0.2.4",
		"gopher://[2001:db8::1]":      "gopher://[2001:db8::1]",
		"http://[2001:db8::1]/":       "http://[2001:db8::1]:80",
		"/relative":                   "!%2Frelative",
		"::not a url":                 "!%3A%3Anot+a+url",
	} {
		if got := originOf(raw); got != want {
			t.Errorf("originOf(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestSignature pins LocalOrigin's and SecretsOrigin's rendering.
func TestSignature(t *testing.T) {
	for _, c := range []struct {
		in   map[string]string
		want string
	}{
		{nil, "adapter-default"},
		{map[string]string{}, "adapter-default"},
		{map[string]string{"url": "http://192.0.2.1:80"}, "url=http://192.0.2.1:80"},
		{map[string]string{"alt_url": "http://192.0.2.1:80"}, "alt_url=http://192.0.2.1:80"},
		{map[string]string{"host": "smtp://192.0.2.1:25"}, "host=smtp://192.0.2.1:25"},
		{map[string]string{"url": "http://192.0.2.1:80", "callback": "https://192.0.2.2:443"}, "callback=https://192.0.2.2:443 url=http://192.0.2.1:80"},
	} {
		if got := signature(c.in); got != c.want {
			t.Errorf("signature(%v) = %q, want %q", c.in, got, c.want)
		}
	}
	if !sameOrigins([]string{"url"}, map[string]string{"url": "a"}, map[string]string{"url": "a", "x": "b"}) {
		t.Error("sameOrigins compared a key outside keys")
	}
	if sameOrigins([]string{"url"}, map[string]string{}, map[string]string{"url": "a"}) {
		t.Error("an unset destination equals a set one")
	}
	for o, ok := range map[string]bool{
		"http://192.0.2.1:80": true, "smtp://192.0.2.2:25": true, "smtp://:25": true,
		"url=http://192.0.2.1:80": false, "the narrows": false, "": false,
	} {
		if wellFormedOrigin(o) != ok {
			t.Errorf("wellFormedOrigin(%q) = %v", o, !ok)
		}
	}
	// A signature is accepted only exactly as signature renders it.
	for sig, ok := range map[string]bool{
		"adapter-default":                                   true,
		"url=http://192.0.2.1:80":                           true,
		"host=smtp://192.0.2.2:25 url=http://192.0.2.1:80":  true,
		"http://192.0.2.1:80":                               false,
		"url=http://192.0.2.1:80 host=smtp://192.0.2.2:25":  false,
		"url=http://192.0.2.1:80  host=smtp://192.0.2.2:25": false,
		"URL=http://192.0.2.1:80":                           false,
		"url=gotham":                                        false,
		"url=":                                              false,
		"":                                                  false,
	} {
		if wellFormedSignature(sig) != ok {
			t.Errorf("wellFormedSignature(%q) = %v", sig, !ok)
		}
	}
}

// TestSignatureOneToOne (R12): a signature decodes back to the origin map
// it renders, so two different destination sets never sign alike. Each pair
// below signed alike before origins were kept free of spaces (a host or port
// holding one, a malformed URL value holding one), and an escaped space in
// an IPv6 zone, which url.Parse decodes into the host, did not decode.
func TestSignatureOneToOne(t *testing.T) {
	fs := fieldSet{urls: []string{"alt_url", "url"}, hostPort: true}
	decode := func(sig string) map[string]string {
		m := map[string]string{}
		if sig == defaultDestinations {
			return m
		}
		for part := range strings.SplitSeq(sig, " ") {
			k, o, ok := strings.Cut(part, "=")
			if !ok {
				return nil
			}
			m[k] = o
		}
		return m
	}
	for _, pair := range [][2]map[string]any{
		{
			{"host": "[gotham", "port": float64(25), "url": "http://arkham]:25"},
			{"host": "gotham:25 url=http://arkham", "port": float64(25)},
		},
		{
			{"host": "gotham", "port": "25", "url": "http://192.0.2.10:80"},
			{"host": "gotham", "port": "25 url=http://192.0.2.10:80"},
		},
		{
			{"alt_url": "gotham", "url": "http://192.0.2.10:80"},
			{"alt_url": "gotham url=http://192.0.2.10:80"},
		},
		{
			{"url": "http://[fe80::1%25cave]:80/"},
			{"url": "http://[fe80::1%25cave%20mouth]:80/", "alt_url": "http://192.0.2.10/"},
		},
	} {
		a, b := fs.origins(pair[0]), fs.origins(pair[1])
		sa, sb := signature(a), signature(b)
		if sa == sb {
			t.Errorf("%v and %v both sign %q", pair[0], pair[1], sa)
		}
		for sig, m := range map[string]map[string]string{sa: a, sb: b} {
			if !maps.Equal(decode(sig), m) {
				t.Errorf("signature %q does not decode to %v", sig, m)
			}
		}
	}
	if o := originOf("http://[fe80::1%25cave%20mouth]:80/"); o != malformedOrigin("http://[fe80::1%25cave%20mouth]:80/") {
		t.Errorf("an IPv6 zone holding a space has origin %q", o)
	}
	if o := hostPortOrigin("gotham", "25\t"); hasSpace(o) {
		t.Errorf("a port holding white space has origin %q", o)
	}
}

// TestPortString covers every port value shape the settings can hold.
func TestPortString(t *testing.T) {
	for _, c := range []struct {
		v    any
		want string
	}{
		{nil, ""}, {"25", "25"}, {float64(9000), "9000"}, {9001, "9001"}, {int64(9002), "9002"},
		{json.Number("9003"), "9003"}, {uint16(9004), "9004"},
	} {
		if got := portString(c.v); got != c.want {
			t.Errorf("portString(%#v) = %q, want %q", c.v, got, c.want)
		}
	}
	fs := fieldSet{hostPort: true}
	if got := fs.origins(map[string]any{"host": "Oracle.Example", "port": float64(25)})["host"]; got != "smtp://oracle.example:25" {
		t.Errorf("host-and-port origin = %q", got)
	}
}

// TestCoerce: env values arrive as JSON strings and are parsed for the
// property's type.
func TestCoerce(t *testing.T) {
	str := func(s string) json.RawMessage { b, _ := json.Marshal(s); return b }
	for _, c := range []struct {
		p    prop
		raw  json.RawMessage
		want any
		ok   bool
	}{
		{prop{types: []string{"integer"}}, str(" 9000 "), float64(9000), true},
		{prop{types: []string{"integer"}}, str("nine"), nil, false},
		{prop{types: []string{"number"}}, str("2.5"), 2.5, true},
		{prop{types: []string{"number"}}, str("x"), nil, false},
		{prop{types: []string{"boolean"}}, str("true"), true, true},
		{prop{types: []string{"boolean"}}, str("maybe"), nil, false},
		{prop{types: []string{"string"}}, str("gcpd"), "gcpd", true},
		{prop{types: []string{"integer"}}, json.RawMessage("7"), float64(7), true},
		{prop{}, json.RawMessage("{"), nil, false},
	} {
		got, err := coerce(c.p, c.raw)
		if (err == nil) != c.ok || (c.ok && got != c.want) {
			t.Errorf("coerce(%v, %s) = %#v, %v; want %#v ok=%v", c.p.types, c.raw, got, err, c.want, c.ok)
		}
	}
}

// TestResupplied pins what counts as a re-supplied secret (SEC3-4).
func TestResupplied(t *testing.T) {
	n := secrets.Name("integrations.notify.batcave.password")
	for v, want := range map[string]bool{
		"alfred-1": true, "a*b*c": true, "***": true,
		"": false, "  ": false, "****": false, "xx********": false, "••••": false, "●●●●": false,
		"[REDACTED]": false, "redacted": false, "<redacted>": false, "secrets.Value([REDACTED])": false,
		"integrations.notify.batcave.password": false, `{"secret":"integrations.notify.batcave.password"}`: false,
	} {
		if got := resupplied(secrets.NewValue(v), n); got != want {
			t.Errorf("resupplied(%q) = %v, want %v", v, got, want)
		}
	}
}

// TestNames pins the secret-name grammar and the credential-name check.
func TestNames(t *testing.T) {
	for s, ok := range map[string]bool{"password": true, "api-key2": true, "a": true, "": false, "2fa": false, "Pass": false, "pass word": false} {
		if validSecretSegment(s) != ok {
			t.Errorf("validSecretSegment(%q) = %v", s, !ok)
		}
	}
	for s, want := range map[string]bool{"password": true, "client_secret": true, "smtp_password": true, "bot_token": true, "channel": false, "tokens_used": false} {
		if secretLike(s) != want {
			t.Errorf("secretLike(%q) = %v", s, !want)
		}
	}
	if got := secretName(Ref{Kind: KindNotify, Instance: "batcave"}, "password"); got != "integrations.notify.batcave.password" {
		t.Errorf("secretName = %q", got)
	}
}

// TestAnalyze covers the schema classification and its refusals.
func TestAnalyze(t *testing.T) {
	type settings struct {
		URL      string      `json:"url,omitempty" schema:"format=uri"`
		Callback string      `json:"callback,omitempty" schema:"format=uri"`
		Host     string      `json:"host,omitempty"`
		Port     int         `json:"port,omitempty"`
		Password secrets.Ref `json:"password"`
		Tags     []string    `json:"tags,omitempty"`
	}
	s, err := config.SchemaFor[settings]()
	if err != nil {
		t.Fatal(err)
	}
	fs, err := analyze(s)
	if err != nil {
		t.Fatal(err)
	}
	if !fs.hostPort || len(fs.urls) != 2 || len(fs.secrets) != 1 || !fs.required["password"] || fs.required["url"] {
		t.Errorf("analyze = %+v", fs)
	}
	if keys := fs.destinationKeys(); len(keys) != 3 || keys[0] != "callback" || keys[1] != "host" {
		t.Errorf("destinationKeys = %v", keys)
	}
	if _, err := analyze(config.Schema{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a zero schema: %v", err)
	}
	type badSecret struct {
		Secret secrets.Ref `json:"Bad_Name"`
	}
	bs, err := config.SchemaFor[badSecret]()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := analyze(bs); !errors.Is(err, ErrInvalid) {
		t.Errorf("a secret field whose name is no secret segment: %v", err)
	}
}

// TestCheckSecretSetting: a secret field holds nothing, "" or its own ref.
func TestCheckSecretSetting(t *testing.T) {
	ref := Ref{Kind: KindNotify, Instance: "batcave"}
	for _, c := range []struct {
		v  any
		ok bool
	}{
		{nil, true}, {"", true}, {secrets.RefTo("integrations.notify.batcave.password"), true},
		{map[string]any{"secret": "integrations.notify.batcave.password"}, true},
		{"alfred-1", false}, {secrets.RefTo("integrations.notify.arkham.password"), false},
		{map[string]any{"secret": "core.oidc.secret"}, false}, {42, false},
	} {
		if err := checkSecretSetting(ref, "password", c.v); (err == nil) != c.ok {
			t.Errorf("checkSecretSetting(%#v) = %v, want ok=%v", c.v, err, c.ok)
		}
	}
}

// TestHealthClass maps every health status to its TestResult class.
func TestHealthClass(t *testing.T) {
	for s, want := range map[HealthStatus]struct {
		c      Class
		failed bool
	}{
		HealthOK: {ClassUnknown, false}, HealthDegraded: {ClassUnknown, false}, HealthDown: {ClassTransient, true},
		HealthAuthFailed: {ClassAuth, true}, HealthMisconfigured: {ClassInvalid, true}, HealthDisabled: {ClassRefusedLocal, true},
		HealthStatus(42): {ClassUnknown, true},
	} {
		if c, failed := healthClass(s); c != want.c || failed != want.failed {
			t.Errorf("healthClass(%v) = %v, %v", s, c, failed)
		}
	}
	if buildClass(&Error{Class: ClassAuth}) != ClassAuth || buildClass(errors.New("x")) != ClassInvalid {
		t.Error("buildClass")
	}
}

// TestCloneConfig: a clone shares no map or Limits with its source.
func TestCloneConfig(t *testing.T) {
	src := InstanceConfig{Settings: map[string]any{"a": 1}, Locked: map[string]string{"a": "env A"}, Limits: &Limits{RPS: 1}}
	c := cloneConfig(src)
	c.Settings["a"], c.Locked["a"], c.Limits.RPS = 2, "env B", 2
	if src.Settings["a"] != 1 || src.Locked["a"] != "env A" || src.Limits.RPS != 1 {
		t.Errorf("the clone aliases its source: %+v", src)
	}
}

// relaySettings is a nested object holding a URL-typed field.
type relaySettings struct {
	Endpoint string `json:"endpoint,omitempty" schema:"format=uri"`
}

// TestAnalyze_RefusesHiddenDestinations (SEC-15): a property that reads as
// a destination is a URL-typed string field or the host of a host-and-port
// pair, and no secret or destination hides below the top level; anything
// else fails registration, as a secret-named non-secret field does.
func TestAnalyze_RefusesHiddenDestinations(t *testing.T) {
	refused := map[string]func() (config.Schema, error){
		"base_url without format": func() (config.Schema, error) {
			return config.SchemaFor[struct {
				BaseURL string `json:"base_url"`
			}]()
		},
		"webhook": func() (config.Schema, error) {
			return config.SchemaFor[struct {
				Webhook string `json:"webhook"`
			}]()
		},
		"prefixed host beside a port": func() (config.Schema, error) {
			return config.SchemaFor[struct {
				SMTPHost string `json:"smtp_host"`
				Port     int    `json:"port"`
			}]()
		},
		"host without a port": func() (config.Schema, error) {
			return config.SchemaFor[struct {
				Host string `json:"host"`
			}]()
		},
		"a list of URLs": func() (config.Schema, error) {
			return config.SchemaFor[struct {
				MirrorURLs []string `json:"mirror_urls"`
			}]()
		},
		"nested URL field": func() (config.Schema, error) {
			return config.SchemaFor[struct {
				Relay relaySettings `json:"relay"`
			}]()
		},
		"nested secret": func() (config.Schema, error) {
			return config.SchemaFor[struct {
				Auth struct {
					Key secrets.Ref `json:"key"`
				} `json:"auth"`
			}]()
		},
		"nested destination name": func() (config.Schema, error) {
			return config.SchemaFor[struct {
				Upstream struct {
					Server string `json:"server"`
				} `json:"upstream"`
			}]()
		},
		"a map of secrets": func() (config.Schema, error) {
			return config.SchemaFor[struct {
				Keys map[string]secrets.Ref `json:"keys"`
			}]()
		},
		"array of objects with a URL": func() (config.Schema, error) {
			return config.SchemaFor[struct {
				Mirrors []relaySettings `json:"mirrors"`
			}]()
		},
		"proxy": func() (config.Schema, error) {
			return config.SchemaFor[struct {
				Proxy string `json:"proxy"`
			}]()
		},
		"address": func() (config.Schema, error) {
			return config.SchemaFor[struct {
				ServerAddress string `json:"listen_address"`
			}]()
		},
		"addr": func() (config.Schema, error) {
			return config.SchemaFor[struct {
				Addr string `json:"addr"`
			}]()
		},
		"a URL field named outside the key grammar": func() (config.Schema, error) {
			return config.SchemaFor[struct {
				AltURL string `json:"altUrl,omitempty" schema:"format=uri"`
			}]()
		},
		"a URL field named with a space": func() (config.Schema, error) {
			return config.SchemaFor[struct {
				AltURL string `json:"alt url,omitempty" schema:"format=uri"`
			}]()
		},
		"ip": func() (config.Schema, error) {
			return config.SchemaFor[struct {
				IP string `json:"ip"`
			}]()
		},
		"prefixed ip": func() (config.Schema, error) {
			return config.SchemaFor[struct {
				BatIP string `json:"bat_ip"`
			}]()
		},
		"domain": func() (config.Schema, error) {
			return config.SchemaFor[struct {
				Domain string `json:"domain"`
			}]()
		},
		"domains": func() (config.Schema, error) {
			return config.SchemaFor[struct {
				Domains []string `json:"mail-domains"`
			}]()
		},
		"a port beside a URL": func() (config.Schema, error) {
			return config.SchemaFor[struct {
				URL  string `json:"url,omitempty" schema:"format=uri"`
				Port int    `json:"port,omitempty"`
			}]()
		},
		"a port alone": func() (config.Schema, error) {
			return config.SchemaFor[struct {
				Port int `json:"port,omitempty"`
			}]()
		},
		"nested port": func() (config.Schema, error) {
			return config.SchemaFor[struct {
				Upstream struct {
					Port int `json:"port"`
				} `json:"upstream"`
			}]()
		},
	}
	for name, build := range refused {
		s, err := build()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := analyze(s); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: analyze = %v, want ErrInvalid", name, err)
		}
	}

	s, err := config.SchemaFor[struct {
		BaseURL  string      `json:"base_url,omitempty" schema:"format=uri"`
		Host     string      `json:"host,omitempty"`
		Port     int         `json:"port,omitempty"`
		Password secrets.Ref `json:"password"`
		Retry    struct {
			Attempts int `json:"attempts"`
		} `json:"retry"`
	}]()
	if err != nil {
		t.Fatal(err)
	}
	fs, err := analyze(s)
	if err != nil {
		t.Fatalf("a schema with only visible destinations: %v", err)
	}
	if !slices.Equal(fs.urls, []string{"base_url"}) || !fs.hostPort {
		t.Errorf("analyze = %+v", fs)
	}
}

// TestURLLike pins which property names read as destinations.
func TestURLLike(t *testing.T) {
	for name, want := range map[string]bool{
		"url": true, "base_url": true, "baseurl": true, "webhook_url": true, "callback-uri": true,
		"endpoint": true, "endpoints": true, "host": true, "smtp_host": true, "hostname": true,
		"webhook": true, "href": true, "server": true, "urls": true, "mirror_baseurls": true,
		"proxy": true, "http_proxy": true, "proxies": true, "address": true, "listen_address": true,
		"addresses": true, "addr": true, "bind-addr": true, "addrs": true,
		"port": true, "smtp_port": true, "ports": true,
		"channel": false, "timeout": false, "retries": false, "tokens_used": false,
		"password": false, "status": false, "hosting_plan": false, "report": false,
		"transport": false, "support_email": false, "proxying": false,
	} {
		if urlLike(name) != want {
			t.Errorf("urlLike(%q) = %v", name, !want)
		}
	}
}

// TestWellFormedHostPort pins the host-and-port origins the importer
// accepts.
func TestWellFormedHostPort(t *testing.T) {
	for o, ok := range map[string]bool{
		"smtp://192.0.2.60:9000": true, "smtp://:9000": true, "smtp://192.0.2.60:": true, "smtp://[2001:db8::1]:25": true,
		"smtp://:": false, "smtp://Oracle.Example:25": false, "smtp://192.0.2.60": false,
	} {
		if got := wellFormedHostPort(o) || originOf(o) == o; got != ok {
			t.Errorf("%q well-formed = %v, want %v", o, got, ok)
		}
	}
	if !wellFormedSignature("host=smtp://:2525") {
		t.Error("an origin with the host unset was refused")
	}
}
