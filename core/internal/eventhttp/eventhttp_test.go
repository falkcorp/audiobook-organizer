// file: core/internal/eventhttp/eventhttp_test.go
// version: 1.0.1
// guid: 0f4c9a27-83d1-4e5b-b6a2-91c7d3e8f054
// last-edited: 2026-11-12

package eventhttp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/realtime"
)

type signal struct {
	Lamp string `json:"lamp"`
}

type okRevalidator struct{}

func (okRevalidator) Revalidate(context.Context, *auth.Principal) error { return nil }

// hub is a hub over a one-topic catalog; it is never started.
func hub(t testing.TB) *realtime.Hub {
	t.Helper()
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spec := events.NewTopic[signal]("batcave.signal.lit.v1").Spec(events.TopicSpec{
		Permission: rbac.AnyAuthenticated, Realtime: &events.RealtimeMeta{},
	})
	if err := bus.Declare("batcave", spec); err != nil {
		t.Fatal(err)
	}
	h, err := realtime.NewHub(realtime.Options{}, realtime.Deps{Auth: okRevalidator{}, Catalog: bus.Catalog(), Refresh: func(_ context.Context, p *auth.Principal) (*auth.Principal, error) { return p, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func router(t testing.TB, d Deps) (*httpapi.Router, error) {
	t.Helper()
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return r, Register(r.CoreGroup(), d)
}

// TestRegister_RouteTable pins the route's contract (05-realtime §3): exactly
// GET /api/core/v1/events, owned by core, a raw handler (the stream writes its
// own response), AnyAuthenticated at the router, no guards, the default rate
// class.
func TestRegister_RouteTable(t *testing.T) {
	r, err := router(t, Deps{Hub: hub(t)})
	if err != nil {
		t.Fatal(err)
	}
	var got []httpapi.RouteInfo
	for ri := range r.Routes() {
		got = append(got, ri)
	}
	if len(got) != 1 {
		t.Fatalf("%d routes registered, want 1: %+v", len(got), got)
	}
	ri := got[0]
	if ri.Owner != "core" || ri.Method != http.MethodGet || ri.Pattern != "/api/core/v1/events" || ri.Name != "streamEvents" {
		t.Errorf("route = %+v", ri)
	}
	if ri.Kind != httpapi.RouteKindRaw {
		t.Errorf("kind = %d, want raw: the handler writes its own response", ri.Kind)
	}
	if ri.Perm.Kind() != rbac.KindAuthenticated {
		t.Errorf("Perm = %s, want AnyAuthenticated", ri.Perm)
	}
	if ri.Guards != (httpapi.Guards{}) || ri.RateClass != httpapi.RateDefault || ri.OwnAuth {
		t.Errorf("guards %+v rate %d ownauth %v, want a plain read", ri.Guards, ri.RateClass, ri.OwnAuth)
	}
}

// TestRegister_NilHubRegistersNothing: a missing hub is an error, never a
// route that answers 500 or panics on first use.
func TestRegister_NilHubRegistersNothing(t *testing.T) {
	r, err := router(t, Deps{})
	if !errors.Is(err, ErrNoHub) {
		t.Fatalf("Register = %v, want ErrNoHub", err)
	}
	if n := len(slices.Collect(r.Routes())); n != 0 {
		t.Errorf("%d routes registered for a nil hub", n)
	}
}

// TestRegister_BuildFailsOnlyForWantOfAuth: until the auth stages exist a
// router refuses every non-Public route with ErrNoAuthService, and this route
// adds no other problem (a binding, a schema, a duplicate name).
func TestRegister_BuildFailsOnlyForWantOfAuth(t *testing.T) {
	r, err := router(t, Deps{Hub: hub(t)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Build()
	if err == nil {
		t.Fatal("Build succeeded without an auth service")
	}
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("Build error %T does not join its problems", err)
	}
	problems := joined.Unwrap()
	if len(problems) != 1 || !errors.Is(problems[0], httpapi.ErrNoAuthService) {
		t.Errorf("Build reported %v, want only ErrNoAuthService", err)
	}
}

// TestRegister_TwiceIsADuplicate: the route may be mounted once; Build names a
// second mount.
func TestRegister_TwiceIsADuplicate(t *testing.T) {
	r, err := router(t, Deps{Hub: hub(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := Register(r.CoreGroup(), Deps{Hub: hub(t)}); err != nil {
		t.Fatal(err)
	}
	_, err = r.Build()
	if err == nil || !strings.Contains(err.Error(), "duplicate method+pattern: GET /api/core/v1/events") {
		t.Errorf("a second mount was not reported as a duplicate: %v", err)
	}
}
