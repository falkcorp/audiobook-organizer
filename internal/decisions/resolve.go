// file: internal/decisions/resolve.go
// version: 1.0.1
// guid: 5d03b7a8-6c1e-4f92-b0a4-e28f91c3d576
// last-edited: 2026-10-28

package decisions

import (
	"errors"
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// ErrIDUnresolvable is what a resolver wraps when the DATA of an id keeps it
// from resolving to one entity: an ambiguous retired shell, a redirect cycle or
// a chain past the hop limit, a resolver that handed back a bad id. It is not
// a failed read. A snapshot treats the two differently: a failed read refuses
// the load (the store cannot be trusted), an unresolvable id is held per id
// (fail closed for the rows that touch it) so one bad id does not refuse every
// run.
var ErrIDUnresolvable = errors.New("decisions: id cannot be resolved")

// ErrResolverCycle is the Kind of an UnresolvableIDError for a chain that does
// not settle: a cycle, or one longer than maxTombstoneHops.
var ErrResolverCycle = errors.New("resolver chain does not settle (a cycle, or longer than the hop limit)")

// ErrResolvedIDInvalid is the Kind of an UnresolvableIDError for a resolver
// that returned an empty path or an id that is not a valid id.
var ErrResolvedIDInvalid = errors.New("resolver returned a bad id")

// UnresolvableIDError is the data-shaped resolution failure of one id. It is
// ErrIDUnresolvable and also Kind (for example ErrRetiredShellAmbiguous).
type UnresolvableIDError struct {
	EntityType string
	// ID is the id that could not be resolved. It may be an id on the way (a
	// hop of the chain) rather than the one that was asked about.
	ID   string
	Kind error
	// Related lists the other ids the failure involves: every candidate holder
	// of an ambiguous shell, the ids a cycle went through. They bound which
	// books the unresolved id may have ended at, so a caller can hold what
	// touches any of them.
	Related []string
}

func (e *UnresolvableIDError) Error() string {
	msg := fmt.Sprintf("decisions: %s %s cannot be resolved: %v", e.EntityType, e.ID, e.Kind)
	if len(e.Related) > 0 {
		msg += " (involves " + strings.Join(e.Related, ", ") + ")"
	}
	return msg
}

func (e *UnresolvableIDError) Unwrap() []error {
	if e.Kind == nil {
		return []error{ErrIDUnresolvable}
	}
	return []error{ErrIDUnresolvable, e.Kind}
}

// UnresolvedID is one entity id a snapshot could not resolve, and why.
type UnresolvedID struct {
	EntityType string
	ID         string
	Err        error
}

// UnresolvableError is what Vetoes.PairVetoed returns when it cannot rule a
// veto out because ids could not be resolved: an id of the pair asked about, or
// a stored reject that names an id that could not be resolved and could touch
// the pair. It lists EVERY such id. The caller must hold the row (fail closed),
// never read it as "no veto".
type UnresolvableError struct {
	// What says whose ids these are ("the pair asked about" or "stored rejects
	// that could touch the pair").
	What string
	IDs  []UnresolvedID
}

func (e *UnresolvableError) Error() string {
	parts := make([]string, len(e.IDs))
	for i, u := range e.IDs {
		parts[i] = fmt.Sprintf("%s %s (%v)", u.EntityType, u.ID, u.Err)
	}
	return fmt.Sprintf("decisions: %d id(s) cannot be resolved, so a veto cannot be ruled out (%s): %s",
		len(e.IDs), e.What, strings.Join(parts, "; "))
}

func (e *UnresolvableError) Is(target error) bool { return target == ErrIDUnresolvable }

func (e *UnresolvableError) Unwrap() []error {
	out := make([]error, 0, len(e.IDs))
	for _, u := range e.IDs {
		if u.Err != nil {
			out = append(out, u.Err)
		}
	}
	return out
}

// PathResolver is an IDResolver that also reports where the id went. The first
// element of the path is the id asked about and the last is where it ends (the
// IDResolver's answer); an id that does not move is a path of one. A veto
// snapshot needs the hops, not only the end: two books merged into one target
// both end there, and only their paths say which of them a retire of one onto
// the target would join to the other.
//
// A caller must treat the returned slice as read-only: a resolver may memoise
// it and hand the same one to many workers.
type PathResolver func(entityType, id string) ([]string, error)

// PathOf adapts a one-hop IDResolver: the path is the id and where it went.
func PathOf(r IDResolver) PathResolver {
	return func(entityType, id string) ([]string, error) {
		end, err := r(entityType, id)
		if err != nil {
			return nil, err
		}
		if end == id {
			return []string{id}, nil
		}
		return []string{id, end}, nil
	}
}

// ChainPathResolvers composes path resolvers into one that applies them in
// order, over and over, until none moves the id (a fixed point), and returns
// the whole path. That is what a book needs: a purged shell resolves to its
// retired-version holder, which may itself have been merged away, which
// resolves to its survivor. A chain longer than maxTombstoneHops (or one that
// loops) is an UnresolvableIDError of Kind ErrResolverCycle carrying the ids it
// went through, never a partial answer.
func ChainPathResolvers(resolvers ...PathResolver) PathResolver {
	return func(entityType, id string) ([]string, error) {
		cur := id
		path := []string{id}
		for hop := 0; hop < maxTombstoneHops; hop++ {
			moved := false
			for _, r := range resolvers {
				p, err := r(entityType, cur)
				if err != nil {
					return nil, withVisited(err, path)
				}
				if len(p) == 0 {
					return nil, &UnresolvableIDError{EntityType: entityType, ID: cur, Kind: ErrResolvedIDInvalid, Related: path}
				}
				if next := p[len(p)-1]; next != cur {
					path = append(path, p[1:]...)
					cur, moved = next, true
				}
			}
			if !moved {
				return path, nil
			}
		}
		dlog.Error("%s %s: resolver chain longer than %d hops (cycle?); refusing to resolve it",
			logger.SanitizeLogValue(entityType), logger.SanitizeLogValue(id), maxTombstoneHops)
		return nil, &UnresolvableIDError{EntityType: entityType, ID: id, Kind: ErrResolverCycle, Related: path}
	}
}

// withVisited adds the ids a chain went through to an unresolvable failure, so
// the failure names everything it touched. Any other error is returned as is.
func withVisited(err error, visited []string) error {
	var ue *UnresolvableIDError
	if !errors.As(err, &ue) {
		return err
	}
	cp := *ue
	cp.Related = append(append([]string(nil), ue.Related...), visited...)
	return &cp
}
