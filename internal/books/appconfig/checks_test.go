// file: internal/books/appconfig/checks_test.go
// version: 1.0.0
// guid: 2a6d9c14-8e37-4b50-9f12-c5e1a7b3d084
// last-edited: 2026-11-20

package appconfig

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	coreconfig "github.com/falkcorp/mediacore/config"
)

const (
	scansPath  = "modules.books.scan.concurrent_scans"
	vectorPath = "modules.books.embedding.vector_backend"
)

// problemRig is an Engine whose stored state already carries one problem
// (concurrent_scans < 0), as a state written by an older binary could.
func problemRig(t *testing.T) (*Engine, *fakeAccess) {
	t.Helper()
	f := newFake()
	f.vals["core.log.level"] = json.RawMessage(`"warn"`)
	f.vals[scansPath] = json.RawMessage(`-1`)
	f.defs[scansPath] = json.RawMessage(`2`)
	f.vals[vectorPath] = json.RawMessage(`"hnsw"`)
	valid := func() config.Config {
		var c config.Config
		c.ConcurrentScans = 2
		return c
	}
	e := New(f, nil, WithBase(valid), WithResidual(newFakeResidual()))
	if err := e.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	return e, f
}

// A write is judged by the problems it adds: a bad value already stored is not
// blamed on a save that leaves it alone, a fix and an unset that repairs it go
// through, and a new problem is refused with nothing written.
func TestValidateCandidate_BlamesOnlyWhatTheWriteAdds(t *testing.T) {
	t.Run("an unrelated save is not refused for a stored problem", func(t *testing.T) {
		e, _ := problemRig(t)
		if _, err := e.Update(t.Context(), adminActor, set("log_level", `"info"`)); err != nil {
			t.Fatalf("refused for a problem the write did not add: %v", err)
		}
	})
	t.Run("the fix is accepted", func(t *testing.T) {
		e, _ := problemRig(t)
		if _, err := e.Update(t.Context(), adminActor, set("concurrent_scans", `4`)); err != nil {
			t.Fatalf("a repair was refused: %v", err)
		}
	})
	t.Run("an unset that returns to a valid default is accepted", func(t *testing.T) {
		e, _ := problemRig(t)
		if _, err := e.Update(t.Context(), adminActor, config.Update{Unset: []string{"concurrent_scans"}}); err != nil {
			t.Fatalf("an unset to the default was refused: %v", err)
		}
	})
	t.Run("a new problem is refused and writes nothing", func(t *testing.T) {
		e, f := problemRig(t)
		gen := f.Generation()
		_, err := e.Update(t.Context(), adminActor, set("embedding.vector_backend", `"typo"`))
		if !errors.Is(err, coreconfig.ErrInvalid) || !contains(err, "embedding.vector_backend") {
			t.Fatalf("want ErrInvalid naming embedding.vector_backend, got %v", err)
		}
		if f.Generation() != gen || len(f.applied) != 0 {
			t.Fatal("a refused write changed something")
		}
	})
	t.Run("another bad value for a key that is already bad is refused", func(t *testing.T) {
		e, f := problemRig(t)
		gen := f.Generation()
		_, err := e.Update(t.Context(), adminActor, set("concurrent_scans", `-7`))
		if !errors.Is(err, coreconfig.ErrInvalid) || f.Generation() != gen {
			t.Fatalf("want a refusal with nothing written, got %v", err)
		}
	})
	t.Run("an unset that lands on an invalid base is refused", func(t *testing.T) {
		f := newFake()
		f.vals[vectorPath] = json.RawMessage(`"bruteforce"`)
		bad := func() config.Config {
			var c config.Config
			c.Embedding.VectorBackend = "typo"
			return c
		}
		e := New(f, nil, WithBase(bad), WithResidual(newFakeResidual()))
		if err := e.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		_, err := e.Update(t.Context(), adminActor, config.Update{Unset: []string{"embedding.vector_backend"}})
		if !errors.Is(err, coreconfig.ErrInvalid) {
			t.Fatalf("an unset is judged by the value it returns to, got %v", err)
		}
	})
}

func TestRemovedKeys_RefusedWithTheirReason(t *testing.T) {
	e, f := problemRig(t)
	gen := f.Generation()
	_, err := e.Update(t.Context(), adminActor, set("auto_fetch_metadata", `true`, "log_format", `"json"`, "no_such_key", `1`))
	if !errors.Is(err, coreconfig.ErrUnknownKey) {
		t.Fatalf("got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"nothing ever read it", "auto_fetch_metadata cannot be set", "log_format cannot be set", "no_such_key"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing %q in %q", want, msg)
		}
	}
	if strings.Contains(msg, "no_such_key cannot be set") {
		t.Fatal("an unknown key was given a removal reason")
	}
	if f.Generation() != gen {
		t.Fatal("a refused write moved the generation")
	}
	if r, ok := removedKeyReason("download_client.usenet.api_key"); !ok || !strings.Contains(r, "qBittorrent") {
		t.Fatalf("a key below a removed block carries the block's reason, got %q %v", r, ok)
	}
	if _, ok := removedKeyReason("download_client.torrent.deluge.host"); ok {
		t.Fatal("a live key below download_client was called removed")
	}
}

func TestEndpointsAreValidatedOnAWriteThatNamesThem(t *testing.T) {
	e, f := problemRig(t)
	gen := f.Generation()
	for name, list := range map[string]string{
		"unknown protocol": `[{"id":"a","protocol":"nope","url":"http://192.0.2.5"}]`,
		"duplicate id":     `[{"id":"a","protocol":"openai_compat","url":"http://192.0.2.5"},{"id":"a","protocol":"openai_compat","url":"http://192.0.2.6"}]`,
		"url credentials":  `[{"id":"a","protocol":"openai_compat","url":"http://u:p@192.0.2.5"}]`,
		"bad auth ref":     `[{"id":"a","protocol":"openai_compat","url":"http://192.0.2.5","auth_ref":"hardcover_api_token"}]`,
		"wildcard":         `[{"id":"a","protocol":"openai_compat","url":"http://192.0.2.5","capabilities":["*"]}]`,
	} {
		_, err := e.Update(t.Context(), adminActor, set("ai_endpoints", list))
		if !errors.Is(err, coreconfig.ErrInvalid) || !contains(err, "ai_endpoints") {
			t.Fatalf("%s: want ErrInvalid naming ai_endpoints, got %v", name, err)
		}
	}
	if f.Generation() != gen {
		t.Fatal("a refused write moved the generation")
	}
	if _, err := e.Update(t.Context(), adminActor, set("log_level", `"info"`)); err != nil {
		t.Fatalf("a write that does not name ai_endpoints must not be judged by them: %v", err)
	}
}

func TestMaskShapedSecretsRefusedForHEADsFieldsOnly(t *testing.T) {
	e, _ := problemRig(t)
	for _, key := range []string{"openai_api_key", "hardcover_api_token", "basic_auth_password"} {
		_, err := e.Update(t.Context(), adminActor, set(key, `"sk-****wxyz"`))
		if !errors.Is(err, coreconfig.ErrInvalid) || !contains(err, "masked") {
			t.Fatalf("%s: want a masked-value refusal, got %v", key, err)
		}
	}
	if _, err := e.Update(t.Context(), adminActor, set("openai_api_key", `"sk-real-secret"`)); err != nil {
		t.Fatalf("a real secret was refused: %v", err)
	}
	src := `[{"id":"gotham-books","enabled":true,"credentials":{"api_key":"abc****"}}]`
	if _, err := e.Update(t.Context(), adminActor, set("metadata_sources", src)); !contains(err, "masked") {
		t.Fatalf("a masked metadata credential must be refused, got %v", err)
	}
	if !maskShaped("****") || maskShaped("***") {
		t.Fatal("a mask is four or more asterisks")
	}
	if maskCheckedHeads["smtp_password"] {
		t.Fatal("the check is HEAD's list, not every secret")
	}
}

// A write by a credential that is not an interactive session is refused for a
// key that needs the admin role (a secret here; core refuses the protected
// sign-in keys the same way, which flip_put_parity_test.go covers on the real
// store); the same write by a session goes through.
func TestAPIKeyIsRefusedWhereASessionIsNot(t *testing.T) {
	// The handler grants FirstRunPerm only in the first-run window, never to a key.
	apiKey := config.Actor{Subject: "key:bootstrap", Interactive: false, Can: func(p string) bool { return p != config.FirstRunPerm }}
	e, f := problemRig(t)
	gen := f.Generation()
	_, err := e.Update(t.Context(), apiKey, set("openai_api_key", `"sk-real-secret"`))
	if !errors.Is(err, coreconfig.ErrForbidden) {
		t.Fatalf("an API key must be refused, got %v", err)
	}
	if f.Generation() != gen {
		t.Fatal("a refused write moved the generation")
	}
	if _, err := e.Update(t.Context(), adminActor, set("openai_api_key", `"sk-real-secret"`)); err != nil {
		t.Fatalf("a session was refused: %v", err)
	}
}
