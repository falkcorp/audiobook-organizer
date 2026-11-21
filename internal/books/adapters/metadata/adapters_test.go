// file: internal/books/adapters/metadata/adapters_test.go
// version: 0.1.0
// guid: 5c59246f-a6f9-4c3f-a7be-3ee691171686
// last-edited: 2026-11-20

package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	cmeta "github.com/falkcorp/mediacore/integrations/metadata"
	"github.com/falkcorp/mediacore/secrets"

	headmeta "github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metadata/providerhttp"
)

func descriptorMetas(t *testing.T) map[integrations.AdapterID]integrations.Meta {
	t.Helper()
	reg := load(t, nil, nil)
	out := map[integrations.AdapterID]integrations.Meta{}
	for m := range reg.Adapters(integrations.KindMetadata) {
		out[m.ID] = m
	}
	return out
}

// The six adapters register through a real Registry, under the IDs M6 writes
// an instance's adapter as ("books."+instance ID), in HEAD's source IDs.
func TestDescriptors_RegisterTheSixHEADSources(t *testing.T) {
	got := descriptorMetas(t)
	want := []integrations.AdapterID{
		"books.audible", "books.audnexus", "books.google-books",
		"books.hardcover", "books.openlibrary", "books.wikipedia",
	}
	var ids []integrations.AdapterID
	for id := range got {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, want) {
		t.Fatalf("adapters = %v, want %v", ids, want)
	}
	for _, id := range ids {
		if got[id].Kind != integrations.KindMetadata {
			t.Errorf("%s kind = %q", id, got[id].Kind)
		}
	}
}

// Every source HEAD's config names has an adapter whose ID is "books."+the
// source ID: a fold cannot leave a configured source without one.
func TestDescriptors_CoverEveryHEADSourceID(t *testing.T) {
	got := descriptorMetas(t)
	for _, id := range headSourceIDs() {
		if _, ok := got[integrations.AdapterID("books."+id)]; !ok {
			t.Errorf("HEAD source %q has no adapter", id)
		}
	}
}

// headSourceIDs are the sources HEAD ships a client and a budget for: the
// providerhttp table minus the cover downloader, which is no metadata source.
func headSourceIDs() []string {
	return slices.DeleteFunc(providerhttp.KnownProviders(), func(id string) bool { return id == "cover" })
}

// The budgets are HEAD's providerhttp table, which M6 pins its own copy of. The
// two differences are deliberate and named here: Audnexus' per-attempt bound is
// core's Timeout (HEAD's AttemptTimeout; HEAD's Timeout also covered the bucket
// queue, which core's does not), and Google Books carries HEAD's daily budget.
func TestLimits_AreHEADsBudgets(t *testing.T) {
	for id, m := range descriptorMetas(t) {
		src := string(id)[len("books."):]
		head := providerhttp.BuiltinLimitsFor(src)
		want := integrations.Limits{RPS: head.RPS, Burst: head.Burst, MaxRetries: head.MaxRetries, Timeout: head.Timeout}
		if head.AttemptTimeout > 0 {
			want.Timeout = head.AttemptTimeout
		}
		if src == headmeta.SourceIDGoogleBooks {
			want.Daily = &integrations.DailyLimits{Total: 1000, Background: 800}
		}
		if !reflect.DeepEqual(m.Limits, want) {
			t.Errorf("%s limits = %+v, want %+v", id, m.Limits, want)
		}
	}
	if got := descriptorMetas(t)["books.audnexus"].Limits.Timeout; got != 10*time.Second {
		t.Errorf("audnexus timeout = %v, want 10s (HEAD's AttemptTimeout)", got)
	}
}

func TestLimits_GoogleBooksDailyBudgetIsHEADs(t *testing.T) {
	if headmeta.DefaultGoogleBooksDailyLimit != 1000 || headmeta.DefaultGoogleBooksBackgroundDailyLimit != 800 {
		t.Fatalf("HEAD's Google Books budget moved to %d/%d: the adapter's Daily limits must follow",
			headmeta.DefaultGoogleBooksDailyLimit, headmeta.DefaultGoogleBooksBackgroundDailyLimit)
	}
}

// Egress: the sources that honour base_url may be pointed at a private mirror
// (Local), as HEAD's could; hardcover and wikipedia dial a fixed public
// endpoint, so never a private address, whatever base_url a row carries.
func TestEgress_PublicOnlyForTheFixedEndpoints(t *testing.T) {
	for id, m := range descriptorMetas(t) {
		want := integrations.EgressConfigured
		if id == "books.hardcover" || id == "books.wikipedia" {
			want = integrations.EgressPublicOnly
		}
		if m.Egress != want {
			t.Errorf("%s egress = %v, want %v", id, m.Egress, want)
		}
	}
}

// The one secret field of google-books and hardcover are the names the operator
// pair and the spec's M1.10 table fix (integrations.metadata.<id>.api_key /
// .api_token), and they are the only secret fields any adapter declares.
func TestSettings_SecretFieldsAreTheOperatorPairNames(t *testing.T) {
	secretsOf := map[integrations.AdapterID][]string{}
	for id, m := range descriptorMetas(t) {
		var doc struct {
			Properties map[string]map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(m.Settings.JSON(), &doc); err != nil {
			t.Fatalf("%s schema: %v", id, err)
		}
		secretsOf[id] = nil
		for name, p := range doc.Properties {
			if _, secret := p["x-mediacore-secret"]; secret {
				secretsOf[id] = append(secretsOf[id], name)
			}
		}
	}
	want := map[integrations.AdapterID][]string{"books.google-books": {"api_key"}, "books.hardcover": {"api_token"}}
	for id, got := range secretsOf {
		if !slices.Equal(got, want[id]) {
			t.Errorf("%s secret fields = %v, want %v", id, got, want[id])
		}
	}
}

// ---- Accepts ---------------------------------------------------------------

func TestAccepts(t *testing.T) {
	byID := map[string]func(Query) bool{}
	for _, s := range sources(Options{}) {
		byID[string(s.id)[len("books."):]] = s.accepts
	}
	cases := []struct {
		src  string
		q    Query
		want bool
	}{
		{"audnexus", Query{Title: "Year One"}, false}, // no title search: its SearchByTitle answers nothing
		{"audnexus", Query{Title: "Year One", ASIN: "B000000001"}, true},
		{"audible", Query{Title: "Year One"}, true},
		{"audible", Query{ASIN: "B000000001"}, false},
		{"hardcover", Query{ISBN13: "9780000000002"}, true},
		{"hardcover", Query{Title: "The Long Halloween"}, true},
		{"hardcover", Query{Author: "Loeb"}, false},
		{"google-books", Query{Title: "Hush"}, true},
		{"openlibrary", Query{}, false},
		{"wikipedia", Query{Title: "Knightfall"}, true},
	}
	for _, c := range cases {
		if got := byID[c.src](c.q); got != c.want {
			t.Errorf("%s Accepts(%+v) = %v, want %v", c.src, c.q, got, c.want)
		}
	}
}

// ---- Search ladder ---------------------------------------------------------

// scripted is a MetadataSource whose rungs answer from a script, counting calls.
type scripted struct {
	ctx, ta, t result
	calls      []string
	contextual bool
}
type result struct {
	rs  []Result
	err error
}

func (s *scripted) Name() string { return "scripted" }
func (s *scripted) SearchByTitle(context.Context, string) ([]Result, error) {
	s.calls = append(s.calls, "title")
	return s.t.rs, s.t.err
}
func (s *scripted) SearchByTitleAndAuthor(context.Context, string, string) ([]Result, error) {
	s.calls = append(s.calls, "title+author")
	return s.ta.rs, s.ta.err
}

type scriptedCtx struct{ *scripted }

func (s scriptedCtx) SearchByContext(context.Context, *Query) ([]Result, error) {
	s.calls = append(s.calls, "context")
	return s.ctx.rs, s.ctx.err
}

func providerOver(src headmeta.MetadataSource) *provider {
	return &provider{src: func(context.Context) (headmeta.MetadataSource, error) { return src, nil }}
}

var (
	yearOne = Result{Title: "Batman: Year One"}
	errBoom = errors.New("the Batmobile will not start")
)

func TestSearch_LadderStopsAtFirstRungWithRecords(t *testing.T) {
	q := Query{Title: "Year One", Author: "Frank Miller"}
	for _, c := range []struct {
		name  string
		s     *scripted
		calls []string
	}{
		{"context answers", &scripted{ctx: result{rs: []Result{yearOne}}}, []string{"context"}},
		{"all rungs empty: every rung is asked", &scripted{}, []string{"context", "title+author", "title"}},
		{"title+author answers", &scripted{ta: result{rs: []Result{yearOne}}}, []string{"context", "title+author"}},
		{"falls to title", &scripted{t: result{rs: []Result{yearOne}}}, []string{"context", "title+author", "title"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.s.contextual = true
			rs, err := providerOver(scriptedCtx{c.s}).Search(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			_ = rs
			if !slices.Equal(c.s.calls, c.calls) {
				t.Errorf("rungs asked = %v, want %v", c.s.calls, c.calls)
			}
		})
	}
}

func TestSearch_NoContextRungWithoutContextualSearch(t *testing.T) {
	s := &scripted{t: result{rs: []Result{yearOne}}}
	rs, err := providerOver(s).Search(context.Background(), Query{Title: "Year One"})
	if err != nil || len(rs) != 1 {
		t.Fatalf("rs=%v err=%v", rs, err)
	}
	if !slices.Equal(s.calls, []string{"title"}) { // no author: no title+author rung
		t.Errorf("rungs = %v", s.calls)
	}
}

// HEAD recorded only the title-only rung's error, so a source whose context and
// author rungs failed and whose title rung returned nothing read as "not found".
// Here a rung's error is reported when no rung answered.
// (The title rung failing too: an empty answer from a rung that did not fail
// is an answer, tested below.)
func TestSearch_ErrorIsReportedWhenNoRungAnswered(t *testing.T) {
	s := &scripted{ctx: result{err: errBoom}, ta: result{err: errBoom}, t: result{err: errBoom}}
	s.contextual = true
	_, err := providerOver(scriptedCtx{s}).Search(context.Background(), Query{Title: "Year One", Author: "Frank Miller"})
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want it to wrap the rung error: a source that failed is not a source with no results", err)
	}
}

func TestSearch_AnEmptyAnswerBeatsAnEarlierRungError(t *testing.T) {
	s := &scripted{ta: result{err: errBoom}} // title rung answers, with nothing
	rs, err := providerOver(s).Search(context.Background(), Query{Title: "Year One", Author: "Frank Miller"})
	if err != nil || rs != nil {
		t.Fatalf("rs=%v err=%v: the title rung answered 'nothing', which is an answer", rs, err)
	}
}

func TestSearch_AnErrorOnOneRungDoesNotHideALaterRungsRecords(t *testing.T) {
	s := &scripted{ta: result{err: errBoom}, t: result{rs: []Result{yearOne}}}
	rs, err := providerOver(s).Search(context.Background(), Query{Title: "Year One", Author: "Frank Miller"})
	if err != nil || len(rs) != 1 {
		t.Fatalf("rs=%v err=%v", rs, err)
	}
}

// A secret that cannot be read fails the request before anything is sent.
func TestSearch_UnreadableSecretFailsBeforeSending(t *testing.T) {
	want := errors.New("vault sealed")
	p := &provider{src: func(context.Context) (headmeta.MetadataSource, error) { return nil, want }}
	if _, err := p.Search(context.Background(), Query{Title: "x"}); !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
}

// ---- AuthorLister ----------------------------------------------------------

func TestAuthorLister_OnlyBooksAudibleProvidesIt(t *testing.T) {
	env := integrations.Env{
		Ref:  integrations.Ref{Kind: integrations.KindMetadata, Instance: "x"},
		HTTP: rtHTTP{http.DefaultClient}, Secrets: staticSecrets{},
	}
	for _, s := range sources(Options{}) {
		p, err := s.build(context.Background(), env, integrations.InstanceConfig{})
		if err != nil {
			t.Fatalf("%s: %v", s.id, err)
		}
		_, ok := p.(AuthorLister)
		if want := s.id == Audible; ok != want {
			t.Errorf("%s is AuthorLister = %v, want %v", s.id, ok, want)
		}
		if _, ok := p.(cmeta.Caps[Query]); !ok {
			t.Errorf("%s lost Caps: the chain would spend its budget on queries it cannot use", s.id)
		}
		_ = p.Close()
	}
}

type staticSecrets map[string]string

func (s staticSecrets) Secret(_ context.Context, f string) (secrets.Value, error) {
	v, ok := s[f]
	if !ok {
		return secrets.Value{}, integrations.ErrNotConfigured
	}
	return secrets.NewValue(v), nil
}
