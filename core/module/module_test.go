// file: core/module/module_test.go
// version: 1.0.3
// guid: 2e0a9bf1-27de-4d08-af46-624ebc5146e5
// last-edited: 2026-11-07

package module

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/log"
)

// TestReservedIDs_EqualsArchitectureRow: the union of the four owner lists
// (httpapi, ui, rbac, log) is 03 §3.6's row, and a
// fresh slice per call.
func TestReservedIDs_EqualsArchitectureRow(t *testing.T) {
	want := []string{"activity", "api", "assets", "auth", "core", "custom", "events", "health", "invite",
		"livez", "login", "metrics", "process", "readyz", "settings", "setup", "static", "system", "ui", "users", "v<digits>"}
	got := ReservedIDs()
	if !slices.Equal(got, want) {
		t.Fatalf("ReservedIDs() = %v\nwant %v", got, want)
	}
	got[0] = "batcave"
	if ReservedIDs()[0] != "activity" {
		t.Fatal("ReservedIDs shares its backing array between calls")
	}
}

func TestIDValid(t *testing.T) {
	cases := map[ID]bool{
		"batcave": true, "hello": true, "b2": true, "abcdefghijklmnop": true,
		"": false, "b": false, "Batcave": false, "bat-cave": false, "2bat": false, "abcdefghijklmnopq": false,
		"core": false, "api": false, "login": false, "process": false, "v1": false, "v2026": false, "vigilante": true,
	}
	for id, want := range cases {
		if got := id.Valid(); got != want {
			t.Errorf("ID(%q).Valid() = %v, want %v", id, got, want)
		}
	}
	if !IsReserved("socket", []string{"socket"}) || IsReserved("vx", []string{"v<digits>"}) {
		t.Error("IsReserved misreads its list")
	}
}

func newTestRegistrar(t *testing.T, legacyGin bool) *Registrar {
	t.Helper()
	router := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{})
	return NewRegistrar(RegistrarConfig{Context: t.Context(), Deps: Deps{Module: "batcave"},
		HTTP: router.ModuleGroup("batcave"), LegacyAuthGin: legacyGin})
}

func TestRegistrar_RecordsAndCloses(t *testing.T) {
	r := newTestRegistrar(t, false)
	start := func(context.Context) error { return nil }
	r.Hook(Hook{Name: "batcave.signal", Start: start})
	r.Worker(Worker{Name: "patrol", Run: func(ctx context.Context) error { <-ctx.Done(); return nil }, Policy: DegradeOnExit})
	r.AuthMethod(auth.MethodSpec{Method: "batcave.signal"})
	r.LogPathRules(log.PathRule{})
	r.MountLegacy(httpapi.LegacyMount{Handler: http.NotFoundHandler(), Auth: httpapi.LegacyAuthCore})
	if err := r.Err(); err != nil {
		t.Fatalf("Err = %v", err)
	}
	rec := r.Close()
	if len(rec.Hooks) != 1 || len(rec.Workers) != 1 || len(rec.AuthMethods) != 1 || len(rec.PathRules) != 1 ||
		len(rec.Mounts) != 1 || rec.Mounts[0].Owner != "batcave" {
		t.Fatalf("recorded %+v", rec)
	}

	// After Close every call records ProblemRegistrarClosed and takes no effect.
	r.Hook(Hook{Name: "batcave.late"})
	r.HTTP()
	err := r.Err()
	if !errors.Is(err, ErrRegistrarClosed) {
		t.Fatalf("late call Err = %v, want ErrRegistrarClosed", err)
	}
	var ve *ValidationError
	if !errors.As(err, &ve) || len(ve.Problems) != 2 {
		t.Fatalf("late calls = %v, want two problems", err)
	}
	// Close is idempotent: a second Close (say, a module that called Close
	// inside Register before the kernel's own) returns the same
	// registrations, without the late hook.
	if again := r.Close(); len(again.Hooks) != 1 || again.Hooks[0].Name != "batcave.signal" || len(again.Mounts) != 1 {
		t.Fatalf("second Close = %+v, want the first Close's registrations", again)
	}
	// Close itself records nothing.
	if errors.As(r.Err(), &ve) && len(ve.Problems) != 2 {
		t.Fatalf("after a second Close Err = %v, want the same two problems", r.Err())
	}
}

// TestRegistrar_AfterCloseReported proves a late call is not silent: the
// kernel reads Err once at P6, so RegistrarConfig.AfterClose receives each
// ProblemRegistrarClosed recorded after Close.
func TestRegistrar_AfterCloseReported(t *testing.T) {
	var late []Problem
	r := NewRegistrar(RegistrarConfig{Context: t.Context(), Deps: Deps{Module: "batcave"},
		AfterClose: func(p Problem) { late = append(late, p) }})
	r.Close()
	r.Worker(Worker{Name: "late-patrol"})
	if len(late) != 1 || late[0].Kind != ProblemRegistrarClosed || !strings.Contains(late[0].Detail, "Worker") {
		t.Fatalf("AfterClose got %v, want one ProblemRegistrarClosed naming Worker", late)
	}
}

func TestRegistrar_Refusals(t *testing.T) {
	cases := map[string]struct {
		call func(r *Registrar)
		want error
	}{
		"auth method interactive":  {func(r *Registrar) { r.AuthMethod(auth.MethodSpec{Method: "batcave.cowl", Interactive: true}) }, ErrAuthMethod},
		"auth method credentials":  {func(r *Registrar) { r.AuthMethod(auth.MethodSpec{Method: "batcave.cowl", MayChangeCredentials: true}) }, ErrAuthMethod},
		"auth method owner":        {func(r *Registrar) { r.AuthMethod(auth.MethodSpec{Method: "batcave.cowl", OwnerVerifier: true}) }, ErrAuthMethod},
		"auth method foreign name": {func(r *Registrar) { r.AuthMethod(auth.MethodSpec{Method: "arkham.cowl"}) }, ErrAuthMethod},
		"auth method bare id":      {func(r *Registrar) { r.AuthMethod(auth.MethodSpec{Method: "batcave."}) }, ErrAuthMethod},
		"legacy gin not allowed":   {func(r *Registrar) { r.MountLegacy(httpapi.LegacyMount{Auth: httpapi.LegacyAuthGin}) }, ErrLegacyAuth},
		"legacy auth unset":        {func(r *Registrar) { r.MountLegacy(httpapi.LegacyMount{Handler: http.NotFoundHandler()}) }, ErrLegacyAuth},
		"legacy auth unknown":      {func(r *Registrar) { r.MountLegacy(httpapi.LegacyMount{Auth: httpapi.LegacyAuthGin + 1}) }, ErrLegacyAuth},
		"worker zero policy":       {func(r *Registrar) { r.Worker(Worker{Name: "patrol", Run: func(context.Context) error { return nil }}) }, ErrWorkerPolicy},
		"worker without run":       {func(r *Registrar) { r.Worker(Worker{Name: "patrol", Policy: StopAppOnExit}) }, ErrInvalidRegistration},
		"hook outside namespace":   {func(r *Registrar) { r.Hook(Hook{Name: "arkham.cells"}) }, ErrInvalidRegistration},
		"hook twice":               {func(r *Registrar) { r.Hook(Hook{Name: "batcave.a"}); r.Hook(Hook{Name: "batcave.a"}) }, ErrInvalidRegistration},
		"other-user predicate twice": {func(r *Registrar) {
			p := func(log.Record, string) bool { return false }
			r.LogOtherUserData(p)
			r.LogOtherUserData(p)
		}, ErrInvalidRegistration},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newTestRegistrar(t, false)
			tc.call(r)
			if err := r.Err(); !errors.Is(err, tc.want) {
				t.Fatalf("Err = %v, want %v", err, tc.want)
			}
		})
	}
	// The one module LegacyAuthGinUntil names may use LegacyAuthGin.
	r := newTestRegistrar(t, true)
	r.MountLegacy(httpapi.LegacyMount{Auth: httpapi.LegacyAuthGin})
	if err := r.Err(); err != nil {
		t.Fatalf("LegacyAuthGin for the named module: %v", err)
	}
}

func TestValidationError_JoinsAndMatches(t *testing.T) {
	cause := httpapi.ErrBadBinding
	ve := &ValidationError{Problems: []Problem{
		{Module: "batcave", Kind: ProblemCoreTooOld, Detail: "needs core 9.0.0"},
		{Kind: ProblemReservedID, Detail: "\"api\" is reserved"},
		NewProblem("batcave", ProblemInvalidRegistration, cause),
	}}
	for _, want := range []error{ErrCoreTooOld, ErrReservedID, ErrInvalidRegistration, httpapi.ErrBadBinding} {
		if !errors.Is(ve, want) {
			t.Errorf("errors.Is(ve, %v) = false", want)
		}
	}
	if errors.Is(ve, ErrDuplicateID) {
		t.Error("matched a kind it does not hold")
	}
	lines := strings.Split(ve.Error(), "\n")
	if len(lines) != 3 || lines[0] != "module batcave: CoreTooOld: needs core 9.0.0" || lines[1] != "ReservedID: \"api\" is reserved" {
		t.Fatalf("Error() = %q", ve.Error())
	}
	for k := ProblemInvalidID; k <= ProblemInvalidRegistration; k++ {
		if k.sentinel() == nil || strings.HasPrefix(k.String(), "ProblemKind(") {
			t.Errorf("kind %d has no sentinel or name", k)
		}
	}
}
