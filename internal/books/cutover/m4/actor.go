// file: internal/books/cutover/m4/actor.go
// version: 1.0.0
// guid: 4a9e1c72-b385-4d06-9f1a-2c7d8e5b3a64
// last-edited: 2026-11-28

package m4

import (
	"log/slog"

	"github.com/falkcorp/mediacore/tasks"
)

// Outcome counted once per imported actor whose auth method the mapping does
// not list. The value is kept verbatim (never dropped or guessed), so an
// unlisted method can only ever fail to match a live actor, which is the
// narrow side.
const OutcomeAuthMethodUnmapped = "auth-method-unmapped"

// authMethodMap is HEAD's auth.Method vocabulary (internal/auth/context.go:
// 39-56, string(auth.Method)) in the one core stamps live runs with
// (core/internal/taskhttp/principal.go actorOf: string(p.Method), with
// api_key written as "token"). Only the three that differ are listed; the
// four that agree are in authMethodSame.
//
//   - api_key -> token: tasks.Actor's own word for a key.
//   - cf_access -> ext:cfaccess: the method the Cloudflare Access identity
//     extension registers (internal/cfaccess/extension.go; the spelling is
//     pinned against the registration by legacyauth's
//     TestMethodNamesMatchTheirRegistrations).
//   - abs -> books.abs: the compat method books registers for the ABS surface
//     (internal/books/module.go absAuthMethod).
//
// The import stamps the actor a live principal of the same user and method
// would carry, so the executor's own-run arm (control.go mayCancel, which
// compares the whole tasks.Actor) matches it. HEAD compared the user id alone
// (opsv3/runs.go startedBy), so a match here is never wider than HEAD's own
// compare. See actor_parity_test.go for the per-method cells.
var authMethodMap = map[string]string{
	"api_key":   "token",
	"cf_access": "ext:cfaccess",
	"abs":       "books.abs",
}

// authMethodSame are the methods HEAD and core spell alike, and "" (a run with
// no recorded method).
var authMethodSame = map[string]bool{
	"":                  true,
	"session":           true,
	"session_delegated": true,
	"system":            true,
}

// NormalizeAuthMethod is HEAD's auth method as the native actor spells it, and
// whether the mapping lists it. A method it does not list comes back verbatim
// with false: the caller counts it (OutcomeAuthMethodUnmapped) and logs it.
func NormalizeAuthMethod(m string) (string, bool) {
	if n, ok := authMethodMap[m]; ok {
		return n, true
	}
	return m, authMethodSame[m]
}

// importActor is HEAD's actor as the native one, and whether its method is
// listed. The one place an imported actor is built from a HEAD actor.
func importActor(a headActor) (tasks.Actor, bool) {
	m, ok := NormalizeAuthMethod(a.AuthMethod)
	return tasks.Actor{UserID: a.UserID, AuthMethod: m}, ok
}

// outcomeCounter is the one method of datamigrate.StepWriter the report needs.
type outcomeCounter interface{ Count(outcome string) }

// noteUnmappedMethods counts and logs each actor method NormalizeAuthMethod
// did not list, once per actor: it was kept as HEAD wrote it (so it can only
// fail to match a live actor), and the M4 report says how many there were.
func noteUnmappedMethods(w outcomeCounter, logger *slog.Logger, runID string, methods []string) {
	for _, method := range methods {
		w.Count(OutcomeAuthMethodUnmapped)
		logger.Warn("m4: an actor's auth method is not in the mapping and was kept verbatim", "run", runID, "method", method)
	}
}
