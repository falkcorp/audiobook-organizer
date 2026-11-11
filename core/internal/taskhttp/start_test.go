// file: core/internal/taskhttp/start_test.go
// version: 1.0.0
// guid: 99faaab6-513a-4a2b-b858-934b54651a82
// last-edited: 2026-11-11

package taskhttp

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/tasks"
)

func startReq(body string, key string, perms ...string) req {
	r := req{method: "POST", path: "/definitions/" + string(kindSweep) + "/runs", body: body, as: session("lucius", scopeRun)}
	if key != "" {
		r.headers = map[string]string{"Idempotency-Key": key}
	}
	return r
}

// TestStart_DryRunFieldRefused400: 5T.3.7 rule 1. The mode is a member of
// the envelope; dry_run and dryRun, at the top level or inside params, answer
// 400 tasks.mode_field_unsupported and reach no Start.
func TestStart_DryRunFieldRefused400(t *testing.T) {
	cells := []struct {
		name, body string
	}{
		{"top level snake", `{"dry_run": true}`},
		{"top level camel", `{"dryRun": false}`},
		{"in params snake", `{"params": {"dry_run": true}}`},
		{"in params camel", `{"params": {"dryRun": true, "case": "riddler"}}`},
		{"with a valid mode beside it", `{"mode": "live", "dry_run": false}`},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			g := newRig(t)
			wantProblem(t, g.do(startReq(c.body, "")), 400, "tasks.mode_field_unsupported")
			if n := len(g.client.starts); n != 0 {
				t.Fatalf("a refused body reached Start %d times", n)
			}
		})
	}

	// Controls: the refusal is not a blanket 400 on bodies that merely look
	// alike. dry_run deeper than params is the definition's own data, and
	// params that are not an object carry no member.
	for _, body := range []string{`{"params": {"nested": {"dry_run": true}}}`, `{"params": "dry_run"}`, `{"mode": "preview"}`, ``} {
		g := newRig(t)
		if rec := g.do(startReq(body, "")); rec.Code != http.StatusAccepted {
			t.Fatalf("body %q = %d, want 202\n%s", body, rec.Code, rec.Body.String())
		}
	}
}

// TestStart_IdempotencyKeyHeaderReplays is the HTTP half of the replay rule:
// the first POST starts a run (202 with its location), a second with the
// same Idempotency-Key answers 200 with the same run and
// Idempotent-Replayed: true. The header is copied into StartRequest, and the
// trigger is manual whatever the body says.
func TestStart_IdempotencyKeyHeaderReplays(t *testing.T) {
	g := newRig(t)
	const key = "robin-sweep-0001"

	first := g.do(startReq(`{"mode": "live"}`, key))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first start = %d, want 202\n%s", first.Code, first.Body.String())
	}
	if h := first.Header().Get("Idempotent-Replayed"); h != "" {
		t.Fatalf("the first start carries Idempotent-Replayed: %q", h)
	}
	var run runDTO
	decode(t, first, &run)
	if loc := first.Header().Get("Location"); loc != BasePath+"/runs/"+run.ID {
		t.Fatalf("Location = %q, want %q", loc, BasePath+"/runs/"+run.ID)
	}

	second := g.do(startReq(`{"mode": "live"}`, key))
	if second.Code != http.StatusOK {
		t.Fatalf("replay = %d, want 200\n%s", second.Code, second.Body.String())
	}
	if h := second.Header().Get("Idempotent-Replayed"); h != "true" {
		t.Fatalf("Idempotent-Replayed = %q, want true", h)
	}
	var again runDTO
	decode(t, second, &again)
	if again.ID != run.ID {
		t.Fatalf("replay named run %s, want %s", again.ID, run.ID)
	}

	if len(g.client.starts) != 2 {
		t.Fatalf("Start calls = %d, want 2 (the route asks every time; the executor replays)", len(g.client.starts))
	}
	for i, s := range g.client.starts {
		if s.Req.IdempotencyKey != key {
			t.Errorf("start %d IdempotencyKey = %q, want %q", i, s.Req.IdempotencyKey, key)
		}
		if s.Req.Trigger != tasks.TriggerManual {
			t.Errorf("start %d Trigger = %q, want manual", i, s.Req.Trigger)
		}
		if s.Req.Mode != tasks.ModeRequestLive {
			t.Errorf("start %d Mode = %v, want live", i, s.Req.Mode)
		}
	}
	if g.client.inserted != 1 {
		t.Fatalf("runs inserted = %d, want 1", g.client.inserted)
	}
}

// TestStart_IdempotencyKeyGrammar: ^[A-Za-z0-9_-]{8,128}$; a bad key is a 400
// that starts nothing, and a start with no key writes no key.
func TestStart_IdempotencyKeyGrammar(t *testing.T) {
	bad := []string{"short", strings.Repeat("k", 129), "has space 123", "semi;colon-123", "ünïcode-1234", "tab\there-123"}
	for _, k := range bad {
		g := newRig(t)
		rec := g.do(startReq(``, k))
		wantProblem(t, rec, 400, "tasks.invalid_idempotency_key")
		if len(g.client.starts) != 0 {
			t.Fatalf("key %q reached Start", k)
		}
	}
	for _, k := range []string{"abcdefgh", strings.Repeat("k", 128), "Gotham_City-1939"} {
		g := newRig(t)
		if rec := g.do(startReq(``, k)); rec.Code != http.StatusAccepted {
			t.Fatalf("key %q = %d, want 202\n%s", k, rec.Code, rec.Body.String())
		}
	}

	g := newRig(t)
	r := startReq(``, "")
	r.headers = map[string]string{}
	if rec := g.do(r); rec.Code != http.StatusAccepted {
		t.Fatalf("start with no key = %d", rec.Code)
	}
	if g.client.starts[0].Req.IdempotencyKey != "" {
		t.Fatalf("a start with no header set IdempotencyKey %q", g.client.starts[0].Req.IdempotencyKey)
	}

	// Two Idempotency-Key headers are ambiguous.
	g = newRig(t)
	hr := startReq(``, "")
	rec := g.doMulti(hr, "Idempotency-Key", "alfred-first-1", "alfred-second-2")
	wantProblem(t, rec, 400, "tasks.invalid_idempotency_key")
}

// TestTaskStart_IdempotencyKeyReplaysRunView is 05-http-api §12's case: a
// repeated key on task start answers 200 with the existing RunView.
func TestTaskStart_IdempotencyKeyReplaysRunView(t *testing.T) {
	g := newRig(t)
	const key = "oracle-sweep-0042"
	first := g.do(startReq(``, key))
	second := g.do(startReq(``, key))
	if second.Code != http.StatusOK || second.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay = %d, Idempotent-Replayed %q", second.Code, second.Header().Get("Idempotent-Replayed"))
	}
	var a, b runDTO
	decode(t, first, &a)
	decode(t, second, &b)
	if a != b {
		t.Fatalf("replayed RunView differs from the first:\n first %+v\nsecond %+v", a, b)
	}
	if b.Kind != string(kindSweep) || b.State != "queued" || b.Trigger != "manual" {
		t.Fatalf("replayed view = %+v, want the queued manual sweep run", b)
	}
}

// TestStart_ConcurrentSameIdempotencyKeyOneRun: concurrent POSTs with one key
// insert one run; one answers 202 and every other 200 with the replay header.
func TestStart_ConcurrentSameIdempotencyKeyOneRun(t *testing.T) {
	g := newRig(t)
	const n = 24
	codes := make([]int, n)
	replayed := make([]string, n)
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			rec := g.do(startReq(`{"mode":"live"}`, "joker-no-double-0007"))
			codes[i], replayed[i] = rec.Code, rec.Header().Get("Idempotent-Replayed")
			var v runDTO
			decode(t, rec, &v)
			ids[i] = v.ID
		})
	}
	wg.Wait()

	fresh := 0
	for i := range n {
		switch codes[i] {
		case http.StatusAccepted:
			fresh++
			if replayed[i] != "" {
				t.Errorf("request %d: the fresh start carries Idempotent-Replayed", i)
			}
		case http.StatusOK:
			if replayed[i] != "true" {
				t.Errorf("request %d: replay without Idempotent-Replayed", i)
			}
		default:
			t.Errorf("request %d = %d", i, codes[i])
		}
		if ids[i] != ids[0] {
			t.Errorf("request %d named run %s, request 0 named %s", i, ids[i], ids[0])
		}
	}
	if fresh != 1 || g.client.inserted != 1 {
		t.Fatalf("fresh starts = %d, runs inserted = %d, want 1 and 1", fresh, g.client.inserted)
	}
}

// TestStart_RefusesAndMaps: permission, unknown kind, body shape, and the
// executor's refusals on the way out.
func TestStart_RefusesAndMaps(t *testing.T) {
	t.Run("caller without the start scope", func(t *testing.T) {
		g := newRig(t)
		r := startReq(``, "")
		r.as = session("vicki", scopeSee)
		wantProblem(t, g.do(r), 403, "permission_denied")
		if len(g.client.starts) != 0 {
			t.Fatal("a caller without Perms.Start reached Start")
		}
	})
	t.Run("unknown kind", func(t *testing.T) {
		g := newRig(t)
		g.client.failWith("Start", executor.ErrUnknownKind)
		r := startReq(``, "")
		r.path = "/definitions/gotham.riddle/runs"
		wantProblem(t, g.do(r), 404, "tasks.unknown_kind")
	})
	t.Run("kind that cannot parse", func(t *testing.T) {
		g := newRig(t)
		r := startReq(``, "")
		r.path = "/definitions/NotAKind/runs"
		wantProblem(t, g.do(r), 404, "tasks.unknown_kind")
		if len(g.client.starts) != 0 {
			t.Fatal("an unparseable kind reached Start")
		}
	})
	t.Run("body carries an actor, trigger, parent or plan_ref", func(t *testing.T) {
		for _, body := range []string{`{"actor": {"user_id": "bane"}}`, `{"trigger": "schedule"}`, `{"parent": "run-1"}`, `{"plan_ref": "run-1"}`} {
			g := newRig(t)
			wantProblem(t, g.do(startReq(body, "")), 400, "bad_request")
			if len(g.client.starts) != 0 {
				t.Fatalf("body %s reached Start", body)
			}
		}
	})
	t.Run("bad mode, priority and JSON", func(t *testing.T) {
		g := newRig(t)
		wantProblem(t, g.do(startReq(`{"mode": "maybe"}`, "")), 422, "validation_failed")
		wantProblem(t, g.do(startReq(`{"priority": "urgent"}`, "")), 422, "validation_failed")
		wantProblem(t, g.do(startReq(`{"mode": `, "")), 400, "bad_request")
		wantProblem(t, g.do(startReq(`{} {}`, "")), 400, "bad_request")
	})
	t.Run("priority and params pass through", func(t *testing.T) {
		g := newRig(t)
		if rec := g.do(startReq(`{"priority": "HIGH", "params": {"case": "penguin"}}`, "")); rec.Code != http.StatusAccepted {
			t.Fatalf("= %d\n%s", rec.Code, rec.Body.String())
		}
		got := g.client.starts[0].Req
		if got.Priority != tasks.PriorityHigh || string(got.Params) != `{"case": "penguin"}` {
			t.Fatalf("StartRequest = %+v", got)
		}
	})
	t.Run("oversized body", func(t *testing.T) {
		g := newRig(t)
		wantProblem(t, g.do(startReq(`{"params": "`+strings.Repeat("x", 2<<20)+`"}`, "")), 413, "payload_too_large")
	})
	t.Run("executor refusals", func(t *testing.T) {
		for _, c := range []struct {
			err    error
			status int
			code   string
		}{
			{executor.ErrNotPermitted, 403, "tasks.actor_not_permitted"},
			{executor.ErrInvalidPriority, 422, "tasks.invalid_priority"},
			{executor.ErrPlanRequired, 422, "tasks.plan_required"},
			{executor.ErrApprovalRequired, 422, "tasks.approval_required"},
			{executor.ErrHeld, 409, "tasks.held"},
		} {
			g := newRig(t)
			g.client.failWith("Start", c.err)
			wantProblem(t, g.do(startReq(``, "")), c.status, c.code)
		}
	})
	t.Run("a started run the caller may not view still answers", func(t *testing.T) {
		g := newRig(t)
		g.client.hidden["run-001"] = true
		rec := g.do(startReq(``, ""))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("= %d\n%s", rec.Code, rec.Body.String())
		}
		var got map[string]string
		decode(t, rec, &got)
		if got["id"] != "run-001" || got["kind"] != string(kindSweep) || len(got) != 2 {
			t.Fatalf("body = %v, want only the id and kind", got)
		}
	})
}
