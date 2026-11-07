// file: core/examples/hello/greeting.go
// version: 1.0.0
// guid: 9ab78c0b-863f-464c-b55e-295241014cce
// last-edited: 2026-11-07

package hello

import (
	"context"
	"fmt"
	"net/http"
	"unicode/utf8"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// The defaults of section modules.hello (06 §6.12.1). The section, and the
// hot-reloadable values it gives the route, land with config.
const (
	greeting   = "Hello"
	maxNameLen = 64
)

// GreetingQuery is the greeting route's query.
type GreetingQuery struct {
	Name string `query:"name"`
}

// Greeting is the greeting route's response. Seen, the count of the name in
// hello's keyspace, lands with storage (R1-20).
type Greeting struct {
	Message string `json:"message"`
}

// registerGreeting records GET /api/hello/v1/greeting (06 §6.12.2).
func registerGreeting(g *httpapi.Group) {
	httpapi.Handle(g, httpapi.Route{
		Method:  http.MethodGet,
		Path:    "/greeting",
		Name:    "getGreeting",
		Perm:    rbac.Public, // rbac.Need(PermRead) once core authentication exists
		Summary: "Greet a name",
	}, greet)
}

// greet answers 422 for an empty or overlong name and greets anything else.
func greet(_ context.Context, in *httpapi.In[GreetingQuery]) (Greeting, error) {
	name := in.Req.Name
	if name == "" || utf8.RuneCountInString(name) > maxNameLen {
		return Greeting{}, httpapi.Invalid(httpapi.Field("/query/name", "invalid_length",
			fmt.Sprintf("must be 1..%d characters", maxNameLen)))
	}
	return Greeting{Message: greeting + ", " + name}, nil
}
