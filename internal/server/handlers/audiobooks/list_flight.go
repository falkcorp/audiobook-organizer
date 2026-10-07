// file: internal/server/handlers/audiobooks/list_flight.go
// version: 1.1.0
// guid: e5a1c7d3-4b28-4f9e-8c60-2d9b7f1e3a54
// last-edited: 2026-10-06

package audiobookshandler

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/flight"
)

// listFlightBuildTimeout is the hard ceiling on one shared list build. The
// slowest builds measured on production (cold memdb and list cache right after
// a restart) took 44-160 s; a build still running at 5 minutes is not going to
// produce an answer anyone is waiting for.
const listFlightBuildTimeout = 5 * time.Minute

// listFlight collapses concurrent identical list-cache misses into one build,
// and cancels that build when the LAST caller waiting on it leaves. The
// mechanism lives in internal/flight (shared with the scoped tag facets);
// this wrapper fixes the list's build timeout.
type listFlight struct {
	g flight.Group[gin.H]
}

// do returns build's result for key; see flight.Group.Do.
func (f *listFlight) do(reqCtx context.Context, key string, build func(ctx context.Context) (gin.H, error)) (gin.H, error) {
	return f.g.Do(reqCtx, key, listFlightBuildTimeout, build)
}
