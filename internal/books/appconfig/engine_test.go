// file: internal/books/appconfig/engine_test.go
// version: 1.0.0
// guid: 7d1b3e58-0a94-4c67-8f2d-b5c9a4e6d031
// last-edited: 2026-11-19

package appconfig

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
)

// engineOver builds an Engine over a fake store seeded with a few keys, started,
// and returns what it published.
func engineOver(t *testing.T) (*Engine, *fakeAccess, *[]config.Config) {
	t.Helper()
	f := newFake()
	f.vals["core.log.level"] = json.RawMessage(`"warn"`)
	f.vals["modules.books.scan.on_startup"] = json.RawMessage(`true`)
	f.regSecret["core.http.basic_gate.password"] = true
	var published []config.Config
	e := New(f, func(c config.Config) { published = append(published, c) })
	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return e, f, &published
}

var adminActor = config.Actor{Subject: "user:alfred", Interactive: true, Owner: true, Can: func(string) bool { return true }}

// TestEngine_ProjectsKeysSecretsAndPassthrough: Start publishes a Config whose
// fields come from the rows' canonical locations: a core key, a books key, a
// registered secret key's plaintext, an unregistered secret's plaintext and an
// operator passthrough value; a key the store does not register is left zero.
func TestEngine_ProjectsKeysSecretsAndPassthrough(t *testing.T) {
	f := newFake()
	f.vals["core.log.level"] = json.RawMessage(`"warn"`)
	f.vals["modules.books.scan.on_startup"] = json.RawMessage(`true`)
	f.regSecret["core.http.basic_gate.password"] = true
	f.secrets["core.http.basic_gate.password"] = "joker"
	f.secrets["integrations.ai.openai.api_key"] = "harley"
	f.pass["integrations.notify.email.host"] = coreconfig.OperatorValue{Value: json.RawMessage(`"mail.example.test"`), By: "env ABO_X"}
	var published []config.Config
	e := New(f, func(c config.Config) { published = append(published, c) })
	if err := e.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got := e.Snapshot()
	if got.LogLevel != "warn" || !got.ScanOnStartup {
		t.Errorf("keys: LogLevel=%q ScanOnStartup=%v", got.LogLevel, got.ScanOnStartup)
	}
	if got.BasicAuthPassword != "joker" || got.OpenAIAPIKey != "harley" {
		t.Errorf("secrets: BasicAuthPassword=%q OpenAIAPIKey=%q", got.BasicAuthPassword, got.OpenAIAPIKey)
	}
	if got.SMTPHost != "mail.example.test" {
		t.Errorf("passthrough: SMTPHost=%q", got.SMTPHost)
	}
	if got.HardcoverAPIToken != "" {
		t.Errorf("an unset secret projected %q", got.HardcoverAPIToken)
	}
	if len(published) != 1 || published[0].LogLevel != "warn" {
		t.Fatalf("published %d projections, want the one Start made", len(published))
	}
	if e.Locks()["smtp_host"] != "env ABO_X" || !slices.Equal(e.EnvLocked(), []string{"smtp_host"}) {
		t.Errorf("locks = %v, env-locked %v", e.Locks(), e.EnvLocked())
	}
}

// TestEngine_RebuildsOnAnyCommit: a commit from another writer re-projects (the
// hook), and an unreadable secret is reported, not projected.
func TestEngine_RebuildsOnAnyCommit(t *testing.T) {
	e, f, published := engineOver(t)
	before := e.Generation()
	if _, err := f.Apply(t.Context(), coreconfig.Patch{Set: map[coreconfig.Path]json.RawMessage{"core.log.level": json.RawMessage(`"debug"`)}}, adminActor); err != nil {
		t.Fatal(err)
	}
	if got := e.Snapshot().LogLevel; got != "debug" {
		t.Fatalf("LogLevel after another writer's commit = %q, want debug", got)
	}
	if e.Generation() != before+1 || len(*published) != 2 {
		t.Fatalf("generation %d->%d, published %d; want one rebuild", before, e.Generation(), len(*published))
	}
	f.secrets["integrations.ai.openai.api_key"] = "x"
	f.unreadble["integrations.ai.openai.api_key"] = true
	if err := e.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	if e.Snapshot().OpenAIAPIKey != "" {
		t.Error("an unreadable secret was projected")
	}
	for _, kv := range e.View().Keys {
		if kv.Name == "openai_api_key" && (kv.Secret == nil || !kv.Secret.Unreadable) {
			t.Errorf("view of an unreadable secret = %+v", kv.Secret)
		}
	}
}

// TestEngine_MutateWritesTheDiffAsOneApply: changing two projected fields is one
// Apply with the canonical paths; a secret goes to the store; nothing else is
// written and the projection follows.
func TestEngine_MutateWritesTheDiffAsOneApply(t *testing.T) {
	e, f, _ := engineOver(t)
	err := e.Mutate(func(c *config.Config) {
		c.LogLevel = "debug"
		c.ScanOnStartup = false
		c.BasicAuthPassword = "riddler"
	})
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if len(f.applied) != 1 {
		t.Fatalf("%d Applies, want one", len(f.applied))
	}
	p := f.applied[0]
	if string(p.Set["core.log.level"]) != `"debug"` || string(p.Set["modules.books.scan.on_startup"]) != `false` ||
		string(p.Set["core.http.basic_gate.password"]) != `"riddler"` || len(p.Set) != 3 || len(p.Unset) != 0 {
		t.Fatalf("patch = %+v", p)
	}
	if got := e.Snapshot(); got.LogLevel != "debug" || got.ScanOnStartup || got.BasicAuthPassword != "riddler" {
		t.Fatalf("projection after Mutate = %q %v %q", got.LogLevel, got.ScanOnStartup, got.BasicAuthPassword)
	}
	// An unregistered secret goes to the secret store, not Apply.
	if err := e.Mutate(func(c *config.Config) { c.OpenAIAPIKey = "scarecrow" }); err != nil {
		t.Fatalf("Mutate secret: %v", err)
	}
	if len(f.applied) != 1 || !slices.Equal(f.puts, []secrets.Name{"integrations.ai.openai.api_key"}) || f.by[0] != "config:mutate" {
		t.Fatalf("applied=%d puts=%v by=%v", len(f.applied), f.puts, f.by)
	}
	if e.Snapshot().OpenAIAPIKey != "scarecrow" {
		t.Fatal("the projection did not follow a secret-store write")
	}
	// A no-change Mutate writes nothing.
	if err := e.Mutate(func(*config.Config) {}); err != nil || len(f.applied) != 1 {
		t.Fatalf("no-op Mutate: err=%v applies=%d", err, len(f.applied))
	}
}

// TestEngine_MutateRefusalLeavesTheProjection: a refused Apply, a change to a
// field nothing stores and a read-only passthrough field are errors that name HEAD's
// keys, and the projection is untouched.
func TestEngine_MutateRefusalLeavesTheProjection(t *testing.T) {
	e, f, published := engineOver(t)
	before := e.Snapshot()
	n := len(*published)
	f.applyErr = &coreconfig.LockedError{Path: "core.log.level", By: "env ABO_CORE_LOG_LEVEL"}
	err := e.Mutate(func(c *config.Config) { c.LogLevel = "debug" })
	var le *coreconfig.LockedError
	if !errors.As(err, &le) || le.Path != "log_level" || le.By != "env ABO_CORE_LOG_LEVEL" {
		t.Fatalf("refused Mutate err = %v, want a LockedError on log_level", err)
	}
	f.applyErr = nil
	if err := e.Mutate(func(c *config.Config) { c.DatabaseType = "sqlite" }); !errors.Is(err, coreconfig.ErrUnknownKey) {
		t.Fatalf("a field no row stores: err = %v, want ErrUnknownKey", err)
	}
	err = e.Mutate(func(c *config.Config) { c.SMTPHost = "mail.example.test" })
	if !errors.Is(err, config.ErrKeyReadOnly) {
		t.Fatalf("a passthrough operator field: err = %v, want ErrKeyReadOnly", err)
	}
	if after := e.Snapshot(); after.LogLevel != before.LogLevel || after.DatabaseType != before.DatabaseType || after.SMTPHost != before.SMTPHost {
		t.Fatalf("a refused Mutate changed the projection: %+v", after)
	}
	if len(*published) != n || len(f.applied) != 0 || len(f.puts) != 0 {
		t.Fatalf("published %d->%d, applies %d, puts %d; a refusal must publish and write nothing", n, len(*published), len(f.applied), len(f.puts))
	}
}

// TestEngine_UpdateErrorsSpeakHEADNames: Update refuses an unknown key, a
// both-set-and-unset key and a secret from a caller without core:admin, all in
// HEAD's names; a value of the wrong type is an ErrInvalid FieldError on the
// HEAD key; the permitted write reports what it applied by HEAD key.
func TestEngine_UpdateErrorsSpeakHEADNames(t *testing.T) {
	e, f, _ := engineOver(t)
	ctx := t.Context()
	_, err := e.Update(ctx, adminActor, config.Update{Set: map[string]json.RawMessage{"no_such_setting": json.RawMessage(`1`)}})
	if !errors.Is(err, coreconfig.ErrUnknownKey) || !contains(err, "no_such_setting") {
		t.Fatalf("unknown key: %v", err)
	}
	_, err = e.Update(ctx, adminActor, config.Update{Set: map[string]json.RawMessage{"log_level": json.RawMessage(`"x"`)}, Unset: []string{"log_level"}})
	var fe *coreconfig.FieldError
	if !errors.Is(err, coreconfig.ErrInvalid) || !errors.As(err, &fe) || fe.Path != "core.log.level" && fe.Path != "log_level" {
		t.Fatalf("set and unset: %v", err)
	}
	user := config.Actor{Subject: "user:robin", Interactive: true, Can: func(string) bool { return false }}
	_, err = e.Update(ctx, user, config.Update{Set: map[string]json.RawMessage{"openai_api_key": json.RawMessage(`"x"`)}})
	if !errors.Is(err, coreconfig.ErrForbidden) || !contains(err, "openai_api_key") || len(f.puts) != 0 {
		t.Fatalf("secret write without admin: err=%v puts=%v", err, f.puts)
	}
	_, err = e.Update(ctx, adminActor, config.Update{Set: map[string]json.RawMessage{"openai_api_key": json.RawMessage(`7`)}})
	if !errors.Is(err, coreconfig.ErrInvalid) {
		t.Fatalf("a non-string secret: %v", err)
	}
	res, err := e.Update(ctx, adminActor, config.Update{Set: map[string]json.RawMessage{"log_level": json.RawMessage(`"error"`), "openai_api_key": json.RawMessage(`"bane"`)}})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !slices.Equal(res.Applied, []string{"log_level", "openai_api_key"}) && !slices.Equal(res.Applied, []string{"openai_api_key", "log_level"}) {
		t.Fatalf("Applied = %v, want HEAD keys", res.Applied)
	}
	// An empty secret clears.
	if _, err := e.Update(ctx, adminActor, config.Update{Set: map[string]json.RawMessage{"openai_api_key": json.RawMessage(`""`)}}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.dels, []secrets.Name{"integrations.ai.openai.api_key"}) || e.Snapshot().OpenAIAPIKey != "" {
		t.Fatalf("clear: dels=%v key=%q", f.dels, e.Snapshot().OpenAIAPIKey)
	}
}

func contains(err error, s string) bool { return err != nil && strings.Contains(err.Error(), s) }

// TestEngine_ViewHasNoSecretValues: the view lists the projected rows by name, a
// secret carries only its metadata, a locked key says why.
func TestEngine_ViewHasNoSecretValues(t *testing.T) {
	e, f, _ := engineOver(t)
	f.secrets["core.http.basic_gate.password"] = "oswald"
	f.locked["core.log.level"] = "env ABO_CORE_LOG_LEVEL"
	if err := e.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	v := e.View()
	byName := map[string]config.KeyView{}
	for i, kv := range v.Keys {
		byName[kv.Name] = kv
		if i > 0 && v.Keys[i-1].Name >= kv.Name {
			t.Fatalf("keys not ordered by name: %s then %s", v.Keys[i-1].Name, kv.Name)
		}
	}
	pw := byName["basic_auth_password"]
	if pw.Value != nil || pw.Secret == nil || !pw.Secret.Set || pw.Class != config.KeyClassSecret {
		t.Fatalf("basic_auth_password view = %+v", pw)
	}
	if bs, _ := json.Marshal(v); strings.Contains(string(bs), "oswald") {
		t.Fatal("the view carries a secret's value")
	}
	if ll := byName["log_level"]; string(ll.Value) != `"warn"` || ll.LockedBy != "env ABO_CORE_LOG_LEVEL" || ll.Class != config.KeyClassKey {
		t.Fatalf("log_level view = %+v", ll)
	}
}

// TestEngine_UpdateHooksStartOpsAfterTheCommit: an UpdateHook sees the
// projection before and after the Update, its ops come back by response key, a
// failing or panicking hook lands in PostCommit without failing the write or
// skipping the next hook, and neither a refused Update nor Mutate runs hooks.
func TestEngine_UpdateHooksStartOpsAfterTheCommit(t *testing.T) {
	e, f, _ := engineOver(t)
	var seen [][2]string
	e.OnUpdate("dedup", func(_ context.Context, b, a config.Config) (map[string]string, error) {
		seen = append(seen, [2]string{b.LogLevel, a.LogLevel})
		return map[string]string{"dedup_rescore_op_id": "op-bane"}, nil
	})
	e.OnUpdate("boom", func(context.Context, config.Config, config.Config) (map[string]string, error) {
		panic("scarecrow")
	})
	e.OnUpdate("fail", func(context.Context, config.Config, config.Config) (map[string]string, error) {
		return nil, errors.New("no engine")
	})
	res, err := e.Update(t.Context(), adminActor, config.Update{Set: map[string]json.RawMessage{"log_level": json.RawMessage(`"debug"`)}})
	if err != nil {
		t.Fatalf("a failing hook failed the write: %v", err)
	}
	if !slices.Equal(seen[0][:], []string{"warn", "debug"}) {
		t.Fatalf("hook saw before/after = %v, want warn -> debug", seen)
	}
	if res.Ops["dedup_rescore_op_id"] != "op-bane" {
		t.Fatalf("Ops = %v", res.Ops)
	}
	var pc *coreconfig.PostCommitError
	if !errors.As(res.PostCommit, &pc) || !contains(res.PostCommit, "scarecrow") || !contains(res.PostCommit, "no engine") {
		t.Fatalf("PostCommit = %v, want both the panic and the error", res.PostCommit)
	}
	if e.Snapshot().LogLevel != "debug" {
		t.Fatal("a failing hook rolled the projection back")
	}
	// A refused write runs no hook.
	f.applyErr = &coreconfig.LockedError{Path: "core.log.level"}
	if _, err := e.Update(t.Context(), adminActor, config.Update{Set: map[string]json.RawMessage{"log_level": json.RawMessage(`"error"`)}}); err == nil {
		t.Fatal("expected a refusal")
	}
	f.applyErr = nil
	if err := e.Mutate(func(c *config.Config) { c.LogLevel = "error" }); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 {
		t.Fatalf("hooks ran %d times, want once (the committed Update only)", len(seen))
	}
}

// TestEngine_ResidualFieldsRoundTrip: class-i fields are read from the Residual,
// written back through it by Mutate and Update (one Save, admin rows gated), and
// redacted in the view; an Engine without a Residual refuses to write them.
func TestEngine_ResidualFieldsRoundTrip(t *testing.T) {
	f := newFake()
	f.vals["core.log.level"] = json.RawMessage(`"warn"`)
	res := newFakeResidual()
	res.vals["openai_base_url"] = json.RawMessage(`"http://192.0.2.7:8080"`)
	res.vals["protected_paths"] = json.RawMessage(`["/vault"]`)
	res.redact["openai_base_url"] = true
	e := New(f, nil, WithResidual(res))
	if err := e.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := e.Snapshot()
	if got.OpenAIBaseURL != "http://192.0.2.7:8080" || !slices.Equal(got.ProtectedPaths, []string{"/vault"}) {
		t.Fatalf("residual projection: %q %v", got.OpenAIBaseURL, got.ProtectedPaths)
	}
	for _, kv := range e.View().Keys {
		if kv.Name == "openai_base_url" && (string(kv.Value) != `"********"` || kv.Class != config.KeyClassInput) {
			t.Errorf("view of a residual field = %s class %s", kv.Value, kv.Class)
		}
	}
	if err := e.Mutate(func(c *config.Config) { c.OpenAIBaseURL = "http://192.0.2.9"; c.LogLevel = "debug" }); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if res.saves != 1 || string(res.vals["openai_base_url"]) != `"http://192.0.2.9"` || len(f.applied) != 1 {
		t.Fatalf("saves=%d vals=%s applies=%d; want one Save and one Apply", res.saves, res.vals["openai_base_url"], len(f.applied))
	}
	if e.Snapshot().OpenAIBaseURL != "http://192.0.2.9" {
		t.Fatal("the projection did not follow the residual write")
	}
	// protected_paths is an admin row.
	user := config.Actor{Subject: "user:robin", Interactive: true, Can: func(string) bool { return false }}
	_, err := e.Update(t.Context(), user, config.Update{Set: map[string]json.RawMessage{"protected_paths": json.RawMessage(`["/x"]`)}})
	if !errors.Is(err, coreconfig.ErrForbidden) || !contains(err, "protected_paths") || res.saves != 1 {
		t.Fatalf("admin residual row without core:admin: err=%v saves=%d", err, res.saves)
	}
	if _, err := e.Update(t.Context(), adminActor, config.Update{Set: map[string]json.RawMessage{"protected_paths": json.RawMessage(`"not a list"`)}}); !errors.Is(err, coreconfig.ErrInvalid) {
		t.Fatalf("a value of the wrong shape: %v", err)
	}
	if _, err := e.Update(t.Context(), adminActor, config.Update{Unset: []string{"protected_paths"}}); err != nil {
		t.Fatal(err)
	}
	if _, still := res.vals["protected_paths"]; still || len(e.Snapshot().ProtectedPaths) != 0 {
		t.Fatalf("unset left protected_paths = %v", res.vals["protected_paths"])
	}
	// A failing Save is reported and the projection still follows what committed.
	res.saveEr = errors.New("disk full")
	if err := e.Mutate(func(c *config.Config) { c.OpenAIBaseURL = "http://192.0.2.10" }); err == nil || e.Snapshot().OpenAIBaseURL != "http://192.0.2.9" {
		t.Fatalf("failing Save: err=%v url=%q", err, e.Snapshot().OpenAIBaseURL)
	}
	// Without a Residual the class-i fields are not the Engine's.
	bare := New(newFake(), nil)
	if err := bare.Mutate(func(c *config.Config) { c.OpenAIBaseURL = "x" }); !errors.Is(err, coreconfig.ErrUnknownKey) {
		t.Fatalf("no Residual: err=%v", err)
	}
}
