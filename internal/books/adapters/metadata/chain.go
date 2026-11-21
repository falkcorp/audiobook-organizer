// file: internal/books/adapters/metadata/chain.go
// version: 0.1.0
// guid: c22563b7-882f-48df-98a4-2b671e440e8c
// last-edited: 2026-11-20

package metadata

import (
	"github.com/falkcorp/mediacore/integrations"
	cmeta "github.com/falkcorp/mediacore/integrations/metadata"
	"github.com/falkcorp/mediacore/tasks"
)

// chainWorkers bounds the fan-out of an All or Stream search. Six sources exist
// and each is paced by its own gate bucket, so the bound is for the walker's
// goroutines, not the sources' request rate: four are enough to keep every
// source's bucket busy while a slow one (Audible, 2 rps) waits on its tokens.
const chainWorkers = 4

// NewChain is books' metadata chain over reg: core's walker (05-metadata M3.1)
// with the declared fan-out, replacing HEAD's `switch src.ID` over
// config.MetadataSources (internal/metafetch source_chain_walk.go). It asks
// the enabled metadata instances that are built as Provider[Query, Result], by
// Priority then instance ID, so a reordered, disabled or edited source takes
// effect on the next search; Accepts keeps a query a source cannot use from
// spending its budget; the gate's holds and breakers skip a source that is
// down. mode is FirstHit (HEAD's walk stops at the first source with
// records) or All.
func NewChain(reg *integrations.Registry, mode cmeta.ChainMode) (*cmeta.Chain[Query, Result], error) {
	return cmeta.NewChain[Query, Result](reg, cmeta.ChainOptions[Query]{
		Parallelism: tasks.Fixed(chainWorkers, "six rate-limited metadata sources; each gate paces its own requests"),
		Mode:        mode,
	})
}
