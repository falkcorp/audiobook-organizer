// file: core/examples/hello/greeting.go
// version: 1.0.1
// guid: 9ab78c0b-863f-464c-b55e-295241014cce
// last-edited: 2026-11-07

package hello

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// The defaults of section modules.hello (06 §6.12.1). The section, and the
// hot-reloadable values it gives the route, land with config.
const (
	greeting   = "Hello"
	maxNameLen = 64
)

// service is what the greeting route reads: the names hello recorded.
type service struct {
	names storage.Keyspace // m/hello/name/
}

// GreetingQuery is the greeting route's query.
type GreetingQuery struct {
	Name string `query:"name"`
}

// Greeting is the greeting route's response.
type Greeting struct {
	Message string `json:"message"`
	Seen    int64  `json:"seen"` // how often the name was recorded; 0 if unknown
}

// registerGreeting records GET /api/hello/v1/greeting (06 §6.12.2).
func registerGreeting(g *httpapi.Group, s *service) {
	httpapi.Handle(g, httpapi.Route{
		Method:  http.MethodGet,
		Path:    "/greeting",
		Name:    "getGreeting",
		Perm:    rbac.Public, // rbac.Need(PermRead) once core authentication exists
		Summary: "Greet a name and report how often it was recorded",
	}, s.greet)
}

// greet answers 422 for an empty or overlong name, and otherwise greets it
// with the count hello recorded for it.
func (s *service) greet(ctx context.Context, in *httpapi.In[GreetingQuery]) (Greeting, error) {
	name := in.Req.Name
	if name == "" || utf8.RuneCountInString(name) > maxNameLen {
		return Greeting{}, httpapi.Invalid(httpapi.Field("/query/name", "invalid_length",
			fmt.Sprintf("must be 1..%d characters", maxNameLen)))
	}
	var seen int64
	switch v, err := s.names.Get(ctx, []byte(name)); {
	case errors.Is(err, storage.ErrNotFound):
	case err != nil:
		return Greeting{}, fmt.Errorf("hello: read %q: %w", name, err)
	default:
		if seen, err = strconv.ParseInt(string(v), 10, 64); err != nil {
			return Greeting{}, fmt.Errorf("hello: bad count for %q: %w", name, err)
		}
	}
	return Greeting{Message: greeting + ", " + name, Seen: seen}, nil
}
